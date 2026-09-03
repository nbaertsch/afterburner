//go:build !windows

package platform

import (
	"os"
	"os/exec"
)

func ConfigureChild(command *exec.Cmd) {}

func InterruptProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}
