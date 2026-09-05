package terminal

import (
	"context"
	"errors"
	"io"
)

// Owner identifies the component that currently owns terminal input.
type Owner string

const (
	OwnerCopilot Owner = "copilot"
	OwnerModal   Owner = "modal"
)

type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateExited  State = "exited"
	StateClosed  State = "closed"
)

type Size struct {
	Cols uint16
	Rows uint16
}

type Command struct {
	Path string
	Args []string
	Env  []string
	Cwd  string
	Size Size
}

type Output struct {
	Owner Owner
	Data  []byte
}

type ExitStatus struct {
	Code int
}

type Process interface {
	WriteInput([]byte) (int, error)
	Resize(Size) error
	Interrupt() error
	Kill() error
	Wait() (ExitStatus, error)
	Close() error
}

type ProcessIDProvider interface {
	ProcessID() uint32
}

type Backend interface {
	Start(context.Context, Command, OutputHandler) (Process, error)
}

type OutputHandler interface {
	HandleOutput(Output)
}

type OutputHandlerFunc func(Output)

func (f OutputHandlerFunc) HandleOutput(output Output) { f(output) }

type Renderer interface {
	Render(Output)
}

type InputRouter interface {
	RouteInput(owner Owner, data []byte, process Process) (int, error)
}

type InputRouterFunc func(Owner, []byte, Process) (int, error)

func (f InputRouterFunc) RouteInput(owner Owner, data []byte, process Process) (int, error) {
	return f(owner, data, process)
}

var ErrModalInputUnhandled = errors.New("modal terminal input is not handled")

func DefaultInputRouter() InputRouter {
	return InputRouterFunc(func(owner Owner, data []byte, process Process) (int, error) {
		switch owner {
		case OwnerCopilot:
			return process.WriteInput(data)
		case OwnerModal:
			return 0, ErrModalInputUnhandled
		default:
			return 0, ErrModalInputUnhandled
		}
	})
}

type writerOutputHandler struct {
	writer io.Writer
}

func WriterOutputHandler(writer io.Writer) OutputHandler {
	return writerOutputHandler{writer: writer}
}

func (h writerOutputHandler) HandleOutput(output Output) {
	if h.writer != nil && len(output.Data) > 0 {
		_, _ = h.writer.Write(output.Data)
	}
}
