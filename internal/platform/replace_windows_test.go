//go:build windows

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceFileWhileDestinationExecutableIsMapped(t *testing.T) {
	if os.Getenv("AFTERBURNER_REPLACE_FILE_HELPER") == "1" {
		time.Sleep(time.Minute)
		return
	}

	root := t.TempDir()
	running := filepath.Join(root, "afterburn.exe")
	source := filepath.Join(root, "replacement.exe")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(running, data, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement")
	if err := os.WriteFile(source, replacement, 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(running, "-test.run=TestReplaceFileWhileDestinationExecutableIsMapped")
	command.Env = append(os.Environ(), "AFTERBURNER_REPLACE_FILE_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()
	time.Sleep(100 * time.Millisecond)

	retired, err := RetiredFilePath(running)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFileRetiring(source, running, retired); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(running)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement content = %q", got)
	}
	if !ProcessExists(command.Process.Pid) {
		t.Fatal("mapped executable process exited during replacement")
	}
}
