//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func prepareInterruptInput() func() {
	handle := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return func() {}
	}
	next := (mode | windows.ENABLE_VIRTUAL_TERMINAL_INPUT) &^
		(windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if next == mode {
		return func() {}
	}
	if err := windows.SetConsoleMode(handle, next); err != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }
}
