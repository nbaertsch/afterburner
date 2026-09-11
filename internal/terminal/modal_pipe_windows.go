//go:build windows

package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	modalPipeBufferSize  = 64 * 1024
	modalPipeMaxInstance = 8
)

var (
	errModalPipeReadTimeout     = errors.New("modal pipe request read timed out")
	modalPipeRequestReadTimeout = 5 * time.Second
)

// ModalPipeListener serves ModalServer requests over a Windows named pipe
// dedicated to a single Afterburner launch. Multiple pipe instances are kept
// accepting concurrently so a long-polling runtime connection cannot block
// open/update/close requests.
type ModalPipeListener struct {
	name            string
	server          *ModalServer
	allowedIdentity *modalIdentity
	closed          atomic.Bool

	mu        sync.Mutex
	handles   map[windows.Handle]struct{}
	initial   []windows.Handle
	done      chan struct{}
	serveOnce sync.Once
	closeOnce sync.Once
}

// NewModalPipeListener creates all pipe instances before the child is
// launched, so early extension-runtime connections have available instances
// even if accept goroutines have not been scheduled yet.
func NewModalPipeListener(server *ModalServer) (*ModalPipeListener, error) {
	return newModalPipeListener(server, nil)
}

// NewModalPipeListenerForCapability creates a distinct, unguessable pipe
// endpoint scoped to exactly one pre-registered modal surface.
func NewModalPipeListenerForCapability(server *ModalServer, capability ModalCapability) (*ModalPipeListener, error) {
	identity, err := normalizeModalRegistration(ModalRegistration{
		OwnerExtensionID: capability.OwnerExtensionID,
		CanvasID:         capability.CanvasID,
		SurfaceID:        capability.SurfaceID,
	})
	if err != nil {
		return nil, err
	}
	return newModalPipeListener(server, &identity)
}

func newModalPipeListener(server *ModalServer, allowedIdentity *modalIdentity) (*ModalPipeListener, error) {
	if server != nil {
		server.RequireAuthenticatedClients()
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate modal pipe name: %w", err)
	}
	name := fmt.Sprintf(`\\.\pipe\afterburner-modal-%d-%s`, os.Getpid(), hex.EncodeToString(nonce))
	listener := &ModalPipeListener{name: name, server: server, allowedIdentity: allowedIdentity, handles: make(map[windows.Handle]struct{}), done: make(chan struct{})}
	for i := 0; i < modalPipeMaxInstance; i++ {
		handle, err := listener.createInstance(i == 0)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		listener.initial = append(listener.initial, handle)
	}
	return listener, nil
}

// PipeName returns the endpoint recorded in the native bootstrap for one
// pre-authorized modal surface.
func (l *ModalPipeListener) PipeName() string { return l.name }

func (l *ModalPipeListener) createInstance(first bool) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return 0, fmt.Errorf("encode modal pipe name: %w", err)
	}
	openMode := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		openMode |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	pipeMode := uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	security, err := modalPipeSecurityAttributes()
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateNamedPipe(
		namePtr,
		openMode,
		pipeMode,
		modalPipeMaxInstance,
		modalPipeBufferSize,
		modalPipeBufferSize,
		0,
		security,
	)
	if err != nil {
		return 0, fmt.Errorf("create modal pipe: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed.Load() {
		_ = windows.CloseHandle(handle)
		return 0, windows.ERROR_OPERATION_ABORTED
	}
	l.handles[handle] = struct{}{}
	return handle, nil
}

func (l *ModalPipeListener) takeInitialHandles() []windows.Handle {
	l.mu.Lock()
	defer l.mu.Unlock()
	handles := append([]windows.Handle(nil), l.initial...)
	l.initial = nil
	return handles
}

// Serve accepts connections until Close is called, dispatching each to the
// ModalServer. It is intended to run in its own goroutine for the lifetime
// of the broker.
func (l *ModalPipeListener) Serve() {
	l.serveOnce.Do(func() {
		for _, handle := range l.takeInitialHandles() {
			go l.acceptLoop(handle)
		}
	})
	<-l.done
}

func (l *ModalPipeListener) acceptLoop(initial windows.Handle) {
	handle := initial
	for !l.closed.Load() {
		if handle == 0 || handle == windows.InvalidHandle {
			var err error
			handle, err = l.createInstance(false)
			if err != nil {
				if l.closed.Load() {
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		if l.closed.Load() {
			l.closeHandle(handle)
			return
		}
		err := windows.ConnectNamedPipe(handle, nil)
		if err != nil && err != windows.ERROR_PIPE_CONNECTED {
			l.closeHandle(handle)
			handle = 0
			if l.closed.Load() {
				return
			}
			continue
		}
		l.handleConnected(handle)
		handle = 0
	}
	l.closeHandle(handle)
}

func (l *ModalPipeListener) handleConnected(handle windows.Handle) {
	conn := &namedPipeConn{handle: handle, readTimeout: modalPipeRequestReadTimeout, closeHandle: l.closeHandle}
	if l.server != nil {
		l.server.HandleAuthorizedSurfaceConnection(conn, namedPipeClientPID(handle), l.allowedIdentity)
	}
	if !conn.closed.Load() {
		_ = windows.FlushFileBuffers(handle)
		windows.DisconnectNamedPipe(handle) //nolint:errcheck // best-effort cleanup before closing the instance
		l.closeHandle(handle)
	}
}

func modalPipeSecurityAttributes() (*windows.SecurityAttributes, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve modal pipe owner: %w", err)
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")")
	if err != nil {
		return nil, fmt.Errorf("create modal pipe security descriptor: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, nil
}

func namedPipeClientPID(handle windows.Handle) uint32 {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(handle, &pid); err != nil {
		return 0
	}
	return pid
}

// Close stops Serve from accepting further connections, closes active pipes,
// and unblocks any pending ConnectNamedPipe/ReadFile operations.
func (l *ModalPipeListener) Close() error {
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		l.mu.Lock()
		handles := make([]windows.Handle, 0, len(l.handles))
		for handle := range l.handles {
			handles = append(handles, handle)
		}
		l.handles = make(map[windows.Handle]struct{})
		l.mu.Unlock()
		for _, handle := range handles {
			closeWindowsPipeHandleAsync(handle)
		}
		close(l.done)
	})
	return nil
}

func (l *ModalPipeListener) closeHandle(handle windows.Handle) {
	if handle == 0 || handle == windows.InvalidHandle {
		return
	}
	l.mu.Lock()
	if _, ok := l.handles[handle]; ok {
		delete(l.handles, handle)
		l.mu.Unlock()
		closeWindowsPipeHandleAsync(handle)
		return
	}
	l.mu.Unlock()
}

func closeWindowsPipeHandleAsync(handle windows.Handle) {
	go func() {
		windows.CancelIoEx(handle, nil)     //nolint:errcheck // best-effort cancellation
		windows.DisconnectNamedPipe(handle) //nolint:errcheck // best-effort disconnect
		windows.CloseHandle(handle)         //nolint:errcheck // best-effort cleanup
	}()
}

// namedPipeConn adapts a raw pipe handle to io.ReadWriter for ModalServer.
type namedPipeConn struct {
	handle      windows.Handle
	readTimeout time.Duration
	closeHandle func(windows.Handle)
	closed      atomic.Bool
}

func (c *namedPipeConn) Read(p []byte) (int, error) {
	if c.readTimeout <= 0 {
		return c.readFile(p)
	}
	type readResult struct {
		count int
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		count, err := c.readFile(p)
		result <- readResult{count: count, err: err}
	}()
	timer := time.NewTimer(c.readTimeout)
	defer timer.Stop()
	select {
	case res := <-result:
		return res.count, res.err
	case <-timer.C:
		c.close()
		return 0, errModalPipeReadTimeout
	}
}

func (c *namedPipeConn) readFile(p []byte) (int, error) {
	var read uint32
	err := windows.ReadFile(c.handle, p, &read, nil)
	if err == windows.ERROR_MORE_DATA {
		return int(read), nil
	}
	if err != nil {
		return int(read), err
	}
	return int(read), nil
}

func (c *namedPipeConn) Write(p []byte) (int, error) {
	var written uint32
	err := windows.WriteFile(c.handle, p, &written, nil)
	if err != nil {
		return int(written), err
	}
	return int(written), nil
}

func (c *namedPipeConn) close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	if c.closeHandle != nil {
		c.closeHandle(c.handle)
		return
	}
	_ = windows.CloseHandle(c.handle)
}
