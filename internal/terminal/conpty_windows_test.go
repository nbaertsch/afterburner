//go:build windows

package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConPTYRunsCommandWithEnvCwdResizeAndExitCode(t *testing.T) {
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := NewConPTYBackend().Start(ctx, Command{
		Path: cmdPath,
		Args: []string{"/d", "/c", "echo ENV:%AFTERBURNER_CONPTY_TEST%&echo CWD:%CD%&exit /b 7"},
		Env:  append(os.Environ(), "AFTERBURNER_CONPTY_TEST=ok"),
		Cwd:  cwd,
		Size: Size{Cols: 100, Rows: 30},
	}, WriterOutputHandler(&output))
	if err != nil {
		if strings.Contains(err.Error(), "CreatePseudoConsole") || strings.Contains(err.Error(), "procedure") {
			t.Skipf("ConPTY unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := process.Resize(Size{Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	status, err := process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 7 {
		t.Fatalf("exit code = %d, want 7; output: %q", status.Code, output.String())
	}
	text := strings.ReplaceAll(output.String(), "\r\n", "\n")
	if !strings.Contains(text, "ENV:ok") {
		t.Fatalf("missing env in output: %q", text)
	}
	if !strings.Contains(strings.ToLower(text), "cwd:"+strings.ToLower(filepath.Clean(cwd))) {
		t.Fatalf("missing cwd %q in output: %q", cwd, text)
	}
}

func TestConPTYPreservesArgumentVector(t *testing.T) {
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	fake := filepath.Join(t.TempDir(), "fakecopilot.exe")
	build := exec.Command(goExe, "build", "-o", fake, "..\\testutil\\fakecopilot")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake copilot: %v\n%s", err, output)
	}
	root := t.TempDir()
	capturePath := filepath.Join(root, "capture.json")
	args := []string{"--prefer-version", "1.2.3", "", `a"b`, "Unicode-世界"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := NewConPTYBackend().Start(ctx, Command{
		Path: fake,
		Args: args,
		Env:  append(os.Environ(), "AFTERBURNER_TEST_CAPTURE="+capturePath),
		Size: Size{Cols: 80, Rows: 24},
	}, WriterOutputHandler(io.Discard))
	if err != nil {
		if strings.Contains(err.Error(), "CreatePseudoConsole") || strings.Contains(err.Error(), "procedure") {
			t.Skipf("ConPTY unavailable: %v", err)
		}
		t.Fatal(err)
	}
	status, err := process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 0 {
		t.Fatalf("exit code = %d, want 0", status.Code)
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured.Args, args) {
		t.Fatalf("args = %#v, want %#v", captured.Args, args)
	}
}

func TestConPTYCloseTerminatesChild(t *testing.T) {
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := NewConPTYBackend().Start(ctx, Command{
		Path: cmdPath,
		Args: []string{"/d", "/q", "/k"},
		Env:  os.Environ(),
		Size: Size{Cols: 80, Rows: 24},
	}, WriterOutputHandler(io.Discard))
	if err != nil {
		if strings.Contains(err.Error(), "CreatePseudoConsole") || strings.Contains(err.Error(), "procedure") {
			t.Skipf("ConPTY unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConPTYWritesInput(t *testing.T) {
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := NewConPTYBackend().Start(ctx, Command{
		Path: cmdPath,
		Args: []string{"/d", "/q", "/k"},
		Env:  os.Environ(),
		Size: Size{Cols: 80, Rows: 24},
	}, WriterOutputHandler(&output))
	if err != nil {
		if strings.Contains(err.Error(), "CreatePseudoConsole") || strings.Contains(err.Error(), "procedure") {
			t.Skipf("ConPTY unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := process.WriteInput([]byte("echo INPUT-OK\r\nexit /b 0\r\n")); err != nil {
		t.Fatal(err)
	}
	status, err := process.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 0 {
		t.Fatalf("exit code = %d, want 0; output: %q", status.Code, output.String())
	}
	if !strings.Contains(output.String(), "INPUT-OK") {
		t.Fatalf("missing input echo in output: %q", output.String())
	}
}
