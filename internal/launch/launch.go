package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/nbaertsch/afterburner/internal/platform"
)

type Options struct {
	Executable string
	Args       []string
	Env        []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

func FindCopilot() (string, error) {
	if explicit := os.Getenv("AFTERBURNER_COPILOT_EXECUTABLE"); explicit != "" {
		return validateExecutable(explicit)
	}
	path, err := exec.LookPath("copilot")
	if err != nil {
		return "", fmt.Errorf("find Copilot CLI: %w", err)
	}
	return validateExecutable(path)
}

func Run(ctx context.Context, opts Options) (int, error) {
	cmd := exec.CommandContext(ctx, opts.Executable, opts.Args...)
	cmd.Env = opts.Env
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	platform.ConfigureChild(cmd)
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start Copilot CLI: %w", err)
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupts:
			_ = platform.InterruptProcess(cmd.Process.Pid)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	signal.Stop(interrupts)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("wait for Copilot CLI: %w", err)
	}
	return 0, nil
}

func validateExecutable(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Copilot executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect Copilot executable: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("Copilot executable is a directory: %s", absolute)
	}
	base := strings.ToLower(filepath.Base(absolute))
	if base == "afterburn.exe" || base == "afterburn" {
		return "", fmt.Errorf("Copilot executable resolves recursively to Afterburner: %s", absolute)
	}
	return absolute, nil
}
