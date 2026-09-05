package terminal

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const defaultTerminalQueryReplyTimeout = 100 * time.Millisecond

type BrokerOptions struct {
	InitialOwner              Owner
	Output                    io.Writer
	OutputSink                OutputHandler
	Renderers                 []Renderer
	InputRouter               InputRouter
	TerminalQueryReplyTimeout time.Duration
}

type Broker struct {
	backend Backend
	router  InputRouter

	mu                           sync.Mutex
	state                        State
	owner                        Owner
	process                      Process
	output                       OutputHandler
	renderers                    []Renderer
	terminalQueryReplies         terminalSequenceSplitter
	terminalQueryReplyTimeout    time.Duration
	terminalQueryReplyTimer      *time.Timer
	terminalQueryReplyGeneration uint64
}

func NewBroker(backend Backend, opts BrokerOptions) *Broker {
	owner := opts.InitialOwner
	if owner == "" {
		owner = OwnerCopilot
	}
	router := opts.InputRouter
	if router == nil {
		router = DefaultInputRouter()
	}
	output := opts.OutputSink
	if output == nil && opts.Output != nil {
		output = WriterOutputHandler(opts.Output)
	}
	replyTimeout := opts.TerminalQueryReplyTimeout
	if replyTimeout <= 0 {
		replyTimeout = defaultTerminalQueryReplyTimeout
	}
	return &Broker{
		backend:                   backend,
		router:                    router,
		state:                     StateIdle,
		owner:                     owner,
		output:                    output,
		renderers:                 append([]Renderer(nil), opts.Renderers...),
		terminalQueryReplies:      newTerminalQueryResponseSplitter(),
		terminalQueryReplyTimeout: replyTimeout,
	}
}

func (b *Broker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Broker) Owner() Owner {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owner
}

func (b *Broker) ProcessID() uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if provider, ok := b.process.(ProcessIDProvider); ok {
		return provider.ProcessID()
	}
	return 0
}

func (b *Broker) SetOwner(owner Owner) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owner = owner
	if owner != OwnerModal {
		b.terminalQueryReplies.Reset()
		b.cancelTerminalQueryReplyTimerLocked()
	}
}

func (b *Broker) Start(ctx context.Context, cmd Command) error {
	b.mu.Lock()
	if b.state != StateIdle {
		state := b.state
		b.mu.Unlock()
		return fmt.Errorf("terminal broker already started: %s", state)
	}
	b.state = StateRunning
	b.mu.Unlock()

	process, err := b.backend.Start(ctx, cmd, OutputHandlerFunc(b.handleOutput))
	if err != nil {
		b.mu.Lock()
		b.state = StateClosed
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	b.process = process
	b.mu.Unlock()
	return nil
}

func (b *Broker) WriteInput(data []byte) (int, error) {
	b.mu.Lock()
	process := b.process
	owner := b.owner
	state := b.state
	router := b.router
	if owner == OwnerModal {
		responses, remainder := b.terminalQueryReplies.Split(data)
		b.updateTerminalQueryReplyTimerLocked()
		b.mu.Unlock()
		if state != StateRunning || process == nil {
			return 0, fmt.Errorf("terminal broker is not running: %s", state)
		}
		if len(responses) > 0 {
			if _, err := process.WriteInput(responses); err != nil {
				return 0, err
			}
		}
		if len(remainder) == 0 {
			return len(data), nil
		}
		if _, err := router.RouteInput(owner, remainder, process); err != nil {
			return 0, err
		}
		return len(data), nil
	}
	data = b.terminalQueryReplies.PrependPendingAndReset(data)
	b.cancelTerminalQueryReplyTimerLocked()
	b.mu.Unlock()
	if state != StateRunning || process == nil {
		return 0, fmt.Errorf("terminal broker is not running: %s", state)
	}
	return router.RouteInput(owner, data, process)
}

func (b *Broker) PendingTerminalQueryReplyBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.terminalQueryReplies.PendingLen()
}

func (b *Broker) FlushPendingTerminalQueryReply() error {
	data, router, owner, process, state := b.takePendingTerminalQueryReply(0)
	if len(data) == 0 {
		return nil
	}
	if state != StateRunning || process == nil {
		return fmt.Errorf("terminal broker is not running: %s", state)
	}
	_, err := router.RouteInput(owner, data, process)
	return err
}

func (b *Broker) updateTerminalQueryReplyTimerLocked() {
	if !b.terminalQueryReplies.HasPending() {
		b.cancelTerminalQueryReplyTimerLocked()
		return
	}
	b.terminalQueryReplyGeneration++
	generation := b.terminalQueryReplyGeneration
	if b.terminalQueryReplyTimer != nil {
		b.terminalQueryReplyTimer.Stop()
	}
	b.terminalQueryReplyTimer = time.AfterFunc(b.terminalQueryReplyTimeout, func() {
		_ = b.flushPendingTerminalQueryReplyGeneration(generation)
	})
}

func (b *Broker) cancelTerminalQueryReplyTimerLocked() {
	b.terminalQueryReplyGeneration++
	if b.terminalQueryReplyTimer != nil {
		b.terminalQueryReplyTimer.Stop()
		b.terminalQueryReplyTimer = nil
	}
}

func (b *Broker) flushPendingTerminalQueryReplyGeneration(generation uint64) error {
	data, router, owner, process, state := b.takePendingTerminalQueryReply(generation)
	if len(data) == 0 {
		return nil
	}
	if state != StateRunning || process == nil {
		return fmt.Errorf("terminal broker is not running: %s", state)
	}
	_, err := router.RouteInput(owner, data, process)
	return err
}

func (b *Broker) takePendingTerminalQueryReply(generation uint64) ([]byte, InputRouter, Owner, Process, State) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if generation != 0 && generation != b.terminalQueryReplyGeneration {
		return nil, nil, "", nil, b.state
	}
	if b.owner != OwnerModal {
		b.terminalQueryReplies.Reset()
		b.cancelTerminalQueryReplyTimerLocked()
		return nil, nil, b.owner, b.process, b.state
	}
	data := b.terminalQueryReplies.FlushPending()
	if len(data) == 0 {
		b.cancelTerminalQueryReplyTimerLocked()
		return nil, nil, b.owner, b.process, b.state
	}
	b.cancelTerminalQueryReplyTimerLocked()
	return data, b.router, b.owner, b.process, b.state
}

func (b *Broker) Resize(size Size) error {
	b.mu.Lock()
	process := b.process
	state := b.state
	b.mu.Unlock()
	if state != StateRunning || process == nil {
		return fmt.Errorf("terminal broker is not running: %s", state)
	}
	return process.Resize(size)
}

func (b *Broker) Interrupt() error {
	b.mu.Lock()
	process := b.process
	state := b.state
	b.mu.Unlock()
	if state != StateRunning || process == nil {
		return nil
	}
	return process.Interrupt()
}

func (b *Broker) Wait() (ExitStatus, error) {
	b.mu.Lock()
	process := b.process
	state := b.state
	b.mu.Unlock()
	if state != StateRunning || process == nil {
		return ExitStatus{}, fmt.Errorf("terminal broker is not running: %s", state)
	}
	status, err := process.Wait()
	b.mu.Lock()
	if b.state == StateRunning {
		b.state = StateExited
	}
	b.mu.Unlock()
	return status, err
}

func (b *Broker) Close() error {
	b.mu.Lock()
	process := b.process
	b.state = StateClosed
	b.cancelTerminalQueryReplyTimerLocked()
	b.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.Close()
}

func (b *Broker) handleOutput(output Output) {
	data := append([]byte(nil), output.Data...)
	output.Data = data
	b.mu.Lock()
	sink := b.output
	renderers := append([]Renderer(nil), b.renderers...)
	b.mu.Unlock()
	if sink != nil {
		sink.HandleOutput(output)
	}
	for _, renderer := range renderers {
		renderer.Render(output)
	}
}
