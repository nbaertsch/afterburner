//go:build windows

package launch

import (
	"os"

	"golang.org/x/sys/windows"
)

func preserveInterruptInputBytes(file *os.File) (func(), error) {
	if file == nil {
		return func() {}, nil
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return func() {}, nil
	}
	if mode&windows.ENABLE_PROCESSED_INPUT == 0 {
		return func() {}, nil
	}
	next := mode &^ windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(handle, next); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, nil
}
