//go:build windows

package terminal

import (
	"os"

	"golang.org/x/sys/windows"
)

type HostMode struct {
	input         windows.Handle
	output        windows.Handle
	inputMode     uint32
	outputMode    uint32
	inputChanged  bool
	outputChanged bool
}

func PrepareHost(input, output *os.File) (*HostMode, error) {
	host := &HostMode{input: windows.Handle(input.Fd()), output: windows.Handle(output.Fd())}
	var inputMode uint32
	if err := windows.GetConsoleMode(host.input, &inputMode); err == nil {
		host.inputMode = inputMode
		mode := (inputMode | windows.ENABLE_VIRTUAL_TERMINAL_INPUT) &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT)
		if mode != inputMode {
			if err := windows.SetConsoleMode(host.input, mode); err != nil {
				return nil, err
			}
			host.inputChanged = true
		}
	}
	var outputMode uint32
	if err := windows.GetConsoleMode(host.output, &outputMode); err == nil {
		host.outputMode = outputMode
		mode := outputMode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		if mode != outputMode {
			if err := windows.SetConsoleMode(host.output, mode); err != nil {
				_ = host.Restore()
				return nil, err
			}
			host.outputChanged = true
		}
	}
	return host, nil
}

func (h *HostMode) Restore() error {
	if h == nil {
		return nil
	}
	var err error
	if h.inputChanged {
		err = windows.SetConsoleMode(h.input, h.inputMode)
	}
	if h.outputChanged {
		if outputErr := windows.SetConsoleMode(h.output, h.outputMode); err == nil {
			err = outputErr
		}
	}
	return err
}

func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	fileType, err := windows.GetFileType(windows.Handle(file.Fd()))
	return err == nil && fileType == windows.FILE_TYPE_CHAR
}

func ConsoleSize(file *os.File) Size {
	if file == nil {
		return Size{Cols: 80, Rows: 24}
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(file.Fd()), &info); err != nil {
		return Size{Cols: 80, Rows: 24}
	}
	cols := info.Window.Right - info.Window.Left + 1
	rows := info.Window.Bottom - info.Window.Top + 1
	if cols <= 0 || rows <= 0 {
		return Size{Cols: 80, Rows: 24}
	}
	return Size{Cols: uint16(cols), Rows: uint16(rows)}
}
