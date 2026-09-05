//go:build windows

package terminal

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ErrConPTYUnsupported = syscall.ERROR_PROC_NOT_FOUND

type ConPTYBackend struct{}

func NewConPTYBackend() *ConPTYBackend { return &ConPTYBackend{} }

func (b *ConPTYBackend) Start(ctx context.Context, cmd Command, handler OutputHandler) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cmd.Path == "" {
		return nil, fmt.Errorf("terminal command path is required")
	}
	size := normalizeSize(cmd.Size)
	pipeSecurity := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var inputRead, inputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, pipeSecurity, 0); err != nil {
		return nil, fmt.Errorf("create ConPTY input pipe: %w", err)
	}
	var outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&outputRead, &outputWrite, pipeSecurity, 0); err != nil {
		_ = windows.CloseHandle(inputRead)
		_ = windows.CloseHandle(inputWrite)
		return nil, fmt.Errorf("create ConPTY output pipe: %w", err)
	}
	if err := windows.SetHandleInformation(inputWrite, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("protect ConPTY input writer: %w", err)
	}
	if err := windows.SetHandleInformation(outputRead, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("protect ConPTY output reader: %w", err)
	}

	var pseudoConsole windows.Handle
	if err := windows.CreatePseudoConsole(toWindowsCoord(size), inputRead, outputWrite, 0, &pseudoConsole); err != nil {
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("create process attribute list: %w", err)
	}
	defer attrList.Delete()
	if err := updatePseudoConsoleAttribute(attrList, pseudoConsole); err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("attach pseudoconsole attribute: %w", err)
	}

	// CreateProcess's CommandLine is argv[0..], where argv[0] is conventionally
	// the program name/path even though applicationName is also supplied
	// separately. cmd.Args holds only the real arguments the child should
	// observe (matching exec.Cmd.Args semantics), so prepend cmd.Path as the
	// argv[0] placeholder before composing, otherwise the child's argument
	// parser (which skips argv[0]) would silently drop the first real
	// argument.
	commandLineArgs := append([]string{cmd.Path}, cmd.Args...)
	commandLineText := windows.ComposeCommandLine(commandLineArgs)
	if commandLineText == "" {
		commandLineText = " "
	}
	commandLine, err := windows.UTF16FromString(commandLineText)
	if err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("encode command line: %w", err)
	}
	applicationName, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("encode executable path: %w", err)
	}
	var cwd *uint16
	if cmd.Cwd != "" {
		cwd, err = windows.UTF16PtrFromString(cmd.Cwd)
		if err != nil {
			windows.ClosePseudoConsole(pseudoConsole)
			closeHandles(inputRead, inputWrite, outputRead, outputWrite)
			return nil, fmt.Errorf("encode cwd: %w", err)
		}
	}
	envBlock, err := makeEnvBlock(cmd.Env)
	if err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, err
	}
	var env *uint16
	if len(envBlock) > 0 {
		env = &envBlock[0]
	}

	startupInfo := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:    uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attrList.List(),
	}
	var processInfo windows.ProcessInformation
	creationFlags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP)
	if err := windows.CreateProcess(applicationName, &commandLine[0], nil, nil, false, creationFlags, env, cwd, &startupInfo.StartupInfo, &processInfo); err != nil {
		windows.ClosePseudoConsole(pseudoConsole)
		closeHandles(inputRead, inputWrite, outputRead, outputWrite)
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)
	_ = windows.CloseHandle(processInfo.Thread)

	process := &conPTYProcess{
		console:    pseudoConsole,
		input:      inputWrite,
		output:     outputRead,
		process:    processInfo.Process,
		pid:        processInfo.ProcessId,
		handler:    handler,
		exited:     make(chan struct{}),
		outputDone: make(chan struct{}),
	}
	go process.readLoop()
	go process.waitLoop()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-process.exited:
		}
	}()
	return process, nil
}

type conPTYProcess struct {
	console windows.Handle
	input   windows.Handle
	output  windows.Handle
	process windows.Handle
	pid     uint32
	handler OutputHandler

	closeConsoleOnce sync.Once
	closeOutputOnce  sync.Once
	closeProcessOnce sync.Once
	writeMu          sync.Mutex

	status     ExitStatus
	waitErr    error
	exited     chan struct{}
	outputDone chan struct{}
}

func (p *conPTYProcess) WriteInput(data []byte) (int, error) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	writtenTotal := 0
	for writtenTotal < len(data) {
		var written uint32
		if err := windows.WriteFile(p.input, data[writtenTotal:], &written, nil); err != nil {
			return writtenTotal, err
		}
		writtenTotal += int(written)
		if written == 0 {
			return writtenTotal, ioErrShortWrite()
		}
	}
	return writtenTotal, nil
}

func (p *conPTYProcess) Resize(size Size) error {
	return windows.ResizePseudoConsole(p.console, toWindowsCoord(normalizeSize(size)))
}

func (p *conPTYProcess) ProcessID() uint32 { return p.pid }

func (p *conPTYProcess) Interrupt() error {
	if p.pid == 0 {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, p.pid)
}

func (p *conPTYProcess) Kill() error {
	select {
	case <-p.exited:
		return nil
	default:
	}
	err := windows.TerminateProcess(p.process, 1)
	p.closeConsole()
	return err
}

func (p *conPTYProcess) Wait() (ExitStatus, error) {
	<-p.exited
	return p.status, p.waitErr
}

func (p *conPTYProcess) Close() error {
	select {
	case <-p.exited:
	default:
		_ = p.Kill()
		<-p.exited
	}
	p.closeOutput()
	return nil
}

func (p *conPTYProcess) readLoop() {
	defer close(p.outputDone)
	defer p.closeOutput()
	buffer := make([]byte, 32*1024)
	for {
		var read uint32
		err := windows.ReadFile(p.output, buffer, &read, nil)
		if read > 0 && p.handler != nil {
			data := append([]byte(nil), buffer[:read]...)
			p.handler.HandleOutput(Output{Owner: OwnerCopilot, Data: data})
		}
		if err != nil {
			return
		}
		if read == 0 {
			return
		}
	}
}

func (p *conPTYProcess) waitLoop() {
	waitResult, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	if err != nil {
		p.waitErr = err
	} else if waitResult != windows.WAIT_OBJECT_0 {
		p.waitErr = fmt.Errorf("unexpected process wait result: %d", waitResult)
	}
	var code uint32
	if exitErr := windows.GetExitCodeProcess(p.process, &code); exitErr != nil && p.waitErr == nil {
		p.waitErr = exitErr
	} else {
		p.status.Code = int(code)
	}
	p.closeConsole()
	select {
	case <-p.outputDone:
	case <-time.After(2 * time.Second):
	}
	p.closeProcess()
	close(p.exited)
}

func (p *conPTYProcess) closeConsole() {
	p.closeConsoleOnce.Do(func() {
		_ = windows.CloseHandle(p.input)
		windows.ClosePseudoConsole(p.console)
	})
}

func (p *conPTYProcess) closeOutput() {
	p.closeOutputOnce.Do(func() {
		_ = windows.CancelIoEx(p.output, nil)
		_ = windows.CloseHandle(p.output)
	})
}

func (p *conPTYProcess) closeProcess() {
	p.closeProcessOnce.Do(func() {
		_ = windows.CloseHandle(p.process)
	})
}

func closeHandles(handles ...windows.Handle) {
	for _, handle := range handles {
		if handle != 0 && handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}
}

func normalizeSize(size Size) Size {
	if size.Cols == 0 {
		size.Cols = 80
	}
	if size.Rows == 0 {
		size.Rows = 24
	}
	return size
}

func toWindowsCoord(size Size) windows.Coord {
	return windows.Coord{X: int16(size.Cols), Y: int16(size.Rows)}
}

func makeEnvBlock(env []string) ([]uint16, error) {
	if env == nil {
		return nil, nil
	}
	items := append([]string(nil), env...)
	for _, item := range items {
		if strings.ContainsRune(item, '\x00') {
			return nil, fmt.Errorf("environment entry contains NUL")
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToUpper(envKey(items[i])) < strings.ToUpper(envKey(items[j]))
	})
	block := make([]uint16, 0)
	for _, item := range items {
		block = append(block, utf16.Encode([]rune(item))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

func envKey(item string) string {
	if index := strings.IndexByte(item, '='); index > 0 {
		return item[:index]
	}
	return item
}

var procUpdateProcThreadAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

func updatePseudoConsoleAttribute(attrList *windows.ProcThreadAttributeListContainer, pseudoConsole windows.Handle) error {
	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE expects the HPCON handle value itself
	// (sized sizeof(HPCON)), not a pointer to a variable holding it; see
	// Microsoft's CreatePseudoConsoleAndPipes sample.
	r1, _, err := syscall.SyscallN(
		procUpdateProcThreadAttribute.Addr(),
		uintptr(unsafe.Pointer(attrList.List())),
		0,
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		uintptr(pseudoConsole),
		unsafe.Sizeof(pseudoConsole),
		0,
		0,
	)
	if r1 == 0 {
		if err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

func ioErrShortWrite() error { return io.ErrShortWrite }
