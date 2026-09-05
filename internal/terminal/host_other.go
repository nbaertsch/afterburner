//go:build !windows

package terminal

import "os"

type HostMode struct{}

func PrepareHost(_, _ *os.File) (*HostMode, error) { return &HostMode{}, nil }

func (h *HostMode) Restore() error { return nil }

func IsTerminal(file *os.File) bool { return false }

func ConsoleSize(file *os.File) Size { return Size{Cols: 80, Rows: 24} }
