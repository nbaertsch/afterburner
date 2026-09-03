//go:build !windows

package platform

import (
	"os/exec"
	"syscall"
	"time"
)

func StartDetached(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command.Start()
}

func WaitForPID(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.ETIMEDOUT
}
