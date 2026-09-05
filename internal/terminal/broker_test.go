package terminal

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeBackend struct {
	process *fakeProcess
	handler OutputHandler
	command Command
}

func (b *fakeBackend) Start(_ context.Context, command Command, handler OutputHandler) (Process, error) {
	b.command = command
	b.handler = handler
	return b.process, nil
}

type fakeProcess struct {
	input      []byte
	resized    []Size
	intercepts int
	closed     bool
	status     ExitStatus
}

func (p *fakeProcess) WriteInput(data []byte) (int, error) {
	p.input = append(p.input, data...)
	return len(data), nil
}

func (p *fakeProcess) Resize(size Size) error {
	p.resized = append(p.resized, size)
	return nil
}

func (p *fakeProcess) Interrupt() error {
	p.intercepts++
	return nil
}

func (p *fakeProcess) Kill() error { return nil }

func (p *fakeProcess) Wait() (ExitStatus, error) { return p.status, nil }

func (p *fakeProcess) Close() error {
	p.closed = true
	return nil
}

type captureRenderer struct {
	outputs []Output
}

func (r *captureRenderer) Render(output Output) {
	r.outputs = append(r.outputs, output)
}

func TestBrokerRoutesCopilotInputAndOutput(t *testing.T) {
	process := &fakeProcess{status: ExitStatus{Code: 37}}
	backend := &fakeBackend{process: process}
	var output bytes.Buffer
	renderer := &captureRenderer{}
	broker := NewBroker(backend, BrokerOptions{Output: &output, Renderers: []Renderer{renderer}})
	command := Command{Path: "copilot", Args: []string{"-p", "hello"}, Env: []string{"A=B"}, Cwd: "C:\\work", Size: Size{Cols: 120, Rows: 40}}
	if err := broker.Start(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if backend.command.Path != command.Path || !reflect.DeepEqual(backend.command.Args, command.Args) || backend.command.Cwd != command.Cwd {
		t.Fatalf("command = %#v, want %#v", backend.command, command)
	}
	if _, err := broker.WriteInput([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	if string(process.input) != "typed" {
		t.Fatalf("input = %q", process.input)
	}
	if err := broker.Resize(Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(process.resized, []Size{{Cols: 100, Rows: 30}}) {
		t.Fatalf("resizes = %#v", process.resized)
	}
	backend.handler.HandleOutput(Output{Owner: OwnerCopilot, Data: []byte("copilot-output")})
	if output.String() != "copilot-output" {
		t.Fatalf("output = %q", output.String())
	}
	if len(renderer.outputs) != 1 || string(renderer.outputs[0].Data) != "copilot-output" {
		t.Fatalf("renderer outputs = %#v", renderer.outputs)
	}
	status, err := broker.Wait()
	if err != nil || status.Code != 37 {
		t.Fatalf("wait = %#v, %v", status, err)
	}
	if broker.State() != StateExited {
		t.Fatalf("state = %s", broker.State())
	}
}

func TestBrokerModalInputExtensionPoint(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	broker := NewBroker(backend, BrokerOptions{})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(OwnerModal)
	if _, err := broker.WriteInput([]byte("modal")); !errors.Is(err, ErrModalInputUnhandled) {
		t.Fatalf("modal input error = %v", err)
	}
	if len(process.input) != 0 {
		t.Fatalf("modal input leaked to Copilot: %q", process.input)
	}
}

func TestBrokerFlushesLoneEscapeToModal(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	var server *ModalServer
	broker := NewBroker(backend, BrokerOptions{
		TerminalQueryReplyTimeout: 10 * time.Millisecond,
		InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
			if owner == OwnerModal && server != nil {
				return server.HandleInput(data)
			}
			return process.WriteInput(data)
		}),
	})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	renderer := &testModalRenderer{}
	var err error
	server, err = NewModalServer(broker, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box", "title": "Black Box"})
	if !response.OK || broker.Owner() != OwnerModal {
		t.Fatalf("open response=%#v owner=%s", response, broker.Owner())
	}
	if _, err := broker.WriteInput([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 500*time.Millisecond, func() bool { return server.ActiveCount() == 0 })
	if broker.Owner() != OwnerCopilot || renderer.hidden != 1 {
		t.Fatalf("escape did not close modal: owner=%s hidden=%d active=%d", broker.Owner(), renderer.hidden, server.ActiveCount())
	}
	if len(process.input) != 0 || broker.PendingTerminalQueryReplyBytes() != 0 {
		t.Fatalf("lone escape leaked or remained pending: process=%q pending=%d", process.input, broker.PendingTerminalQueryReplyBytes())
	}
}

func TestBrokerRoutesArrowKeysToModal(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	var modalInput []byte
	broker := NewBroker(backend, BrokerOptions{InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
		if owner == OwnerModal {
			modalInput = append(modalInput, data...)
		}
		return len(data), nil
	})})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(OwnerModal)
	if _, err := broker.WriteInput([]byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.WriteInput([]byte("\x1b[15~")); err != nil {
		t.Fatal(err)
	}
	if string(modalInput) != "\x1b[B\x1b[15~" || len(process.input) != 0 || broker.PendingTerminalQueryReplyBytes() != 0 {
		t.Fatalf("key routing modal=%q process=%q pending=%d", modalInput, process.input, broker.PendingTerminalQueryReplyBytes())
	}
}

func TestBrokerAllowsTerminalQueryRepliesDuringModalOwnership(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	humanRouted := false
	broker := NewBroker(backend, BrokerOptions{InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
		if owner == OwnerModal && string(data) == "x" {
			humanRouted = true
		}
		return len(data), nil
	})})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(OwnerModal)
	if _, err := broker.WriteInput([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if !humanRouted || len(process.input) != 0 {
		t.Fatalf("human input routed=%v leaked=%q", humanRouted, process.input)
	}
	reply := []byte("\x1b[12;34R\x1b[?1;2c\x1b[0n\x1b]10;rgb:aaaa/bbbb/cccc\x1b\\")
	if _, err := broker.WriteInput(reply); err != nil {
		t.Fatal(err)
	}
	if string(process.input) != string(reply) {
		t.Fatalf("terminal query replies were not forwarded: %q", process.input)
	}
	if _, err := broker.WriteInput([]byte("a\x1b[56;78Rb")); err != nil {
		t.Fatal(err)
	}
	if string(process.input) != string(reply)+"\x1b[56;78R" {
		t.Fatalf("mixed terminal query reply was not forwarded: %q", process.input)
	}
}

func TestBrokerAllowsSplitTerminalQueryRepliesDuringModalOwnership(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	var modalInput []byte
	broker := NewBroker(backend, BrokerOptions{InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
		if owner == OwnerModal {
			modalInput = append(modalInput, data...)
		}
		return len(data), nil
	})})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(OwnerModal)
	for _, chunk := range [][]byte{
		[]byte("\x1b"), []byte("[12"), []byte(";34R"),
		[]byte("\x1b["), []byte("0"), []byte("n"),
		[]byte("x\x1b]10"), []byte(";rgb:aa/bb/cc"), []byte("\x1b\\"),
		[]byte("\x1bP1"), []byte("$r0 q"), []byte("\x1b\\"),
	} {
		if _, err := broker.WriteInput(chunk); err != nil {
			t.Fatal(err)
		}
	}
	wantProcess := "\x1b[12;34R\x1b[0n\x1b]10;rgb:aa/bb/cc\x1b\\\x1bP1$r0 q\x1b\\"
	if string(process.input) != wantProcess {
		t.Fatalf("split terminal query replies forwarded to process = %q, want %q", process.input, wantProcess)
	}
	if string(modalInput) != "x" {
		t.Fatalf("modal input = %q, want only ordinary input", modalInput)
	}
}

func TestBrokerSplitTerminalRepliesDoNotCloseModal(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	var server *ModalServer
	broker := NewBroker(backend, BrokerOptions{
		TerminalQueryReplyTimeout: 100 * time.Millisecond,
		InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
			if owner == OwnerModal && server != nil {
				return server.HandleInput(data)
			}
			return process.WriteInput(data)
		}),
	})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	renderer := &testModalRenderer{}
	var err error
	server, err = NewModalServer(broker, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	server.pollTimeout = time.Millisecond
	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box", "title": "Black Box"})
	if !response.OK {
		t.Fatalf("open response=%#v", response)
	}
	for _, chunk := range [][]byte{[]byte("\x1b"), []byte("[0n"), []byte("\x1b]10;rgb:aa/bb/cc"), []byte("\x1b\\")} {
		if _, err := broker.WriteInput(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if want := "\x1b[0n\x1b]10;rgb:aa/bb/cc\x1b\\"; string(process.input) != want {
		t.Fatalf("terminal replies = %q, want %q", process.input, want)
	}
	time.Sleep(150 * time.Millisecond)
	if server.ActiveCount() != 1 || renderer.hidden != 0 || broker.PendingTerminalQueryReplyBytes() != 0 {
		t.Fatalf("terminal reply affected modal: active=%d hidden=%d pending=%d", server.ActiveCount(), renderer.hidden, broker.PendingTerminalQueryReplyBytes())
	}
	response = callModalServer(t, server, map[string]any{"type": "poll", "id": "black-box"})
	if response.Event != nil {
		t.Fatalf("terminal reply generated modal event: %#v", response)
	}
}

func TestTerminalQueryRequestExtraction(t *testing.T) {
	input := []byte("paint\x1b[31mred\x1b[6n\x1b[c\x1b[>0c\x1b[?25$p\x1b]10;?\x1b\\\x1bP$q q\x1b\\tail")
	got := ExtractTerminalQueryRequests(input)
	want := "\x1b[6n\x1b[c\x1b[>0c\x1b[?25$p\x1b]10;?\x1b\\\x1bP$q q\x1b\\"
	if string(got) != want {
		t.Fatalf("extracted query requests = %q, want %q", got, want)
	}
}

func TestTerminalQueryResponseClassifier(t *testing.T) {
	allowed := [][]byte{
		[]byte("\x1b[12;34R"),
		[]byte("\x1b[?1;2c"),
		[]byte("\x1b[>85;95;0c"),
		[]byte("\x1b[0n"),
		[]byte("\x1b[?25;1$y"),
		[]byte("\x1b]10;rgb:aaaa/bbbb/cccc\x1b\\"),
		[]byte("\x1b]4;1;rgb:aaaa/bbbb/cccc\a"),
		[]byte("\x1bP1$r0 q\x1b\\"),
	}
	for _, data := range allowed {
		if !IsTerminalQueryResponse(data) {
			t.Fatalf("expected terminal reply: %q", data)
		}
	}
	blocked := [][]byte{
		[]byte("q"),
		[]byte("\x1b[A"),
		[]byte("\x1b[31m"),
		[]byte("\x1b]52;c;secret\x07"),
		append([]byte("\x1b["), bytes.Repeat([]byte("1"), maxTerminalQueryResponseBytes)...),
	}
	for _, data := range blocked {
		if IsTerminalQueryResponse(data) {
			t.Fatalf("unexpected terminal reply classification: %q", data)
		}
	}
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestBrokerCustomInputRouter(t *testing.T) {
	process := &fakeProcess{}
	backend := &fakeBackend{process: process}
	routed := false
	broker := NewBroker(backend, BrokerOptions{InputRouter: InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
		routed = owner == OwnerModal && string(data) == "owned"
		return len(data), nil
	})})
	if err := broker.Start(context.Background(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(OwnerModal)
	if _, err := broker.WriteInput([]byte("owned")); err != nil {
		t.Fatal(err)
	}
	if !routed {
		t.Fatal("custom input router was not invoked")
	}
}
