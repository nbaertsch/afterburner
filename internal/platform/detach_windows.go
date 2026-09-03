//go:build windows

package platform

import (
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const detachedProcess = 0x00000008

func StartDetached(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
	return command.Start()
}

func WaitForPID(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return syscall.ETIMEDOUT
	}
	return nil
}
