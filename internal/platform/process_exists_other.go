//go:build !windows

package platform

import (
	"os"
	"syscall"
)

func ProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}
