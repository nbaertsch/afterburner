//go:build windows

package platform

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const createNewProcessGroup = 0x00000200

func ConfigureChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func InterruptProcess(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}
