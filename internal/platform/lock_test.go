package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectoryLockReportsLiveOwnerAndReleasesSafely(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := AcquireDirectoryLock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "owner.json")); err != nil {
		t.Fatalf("owner metadata missing: %v", err)
	}
	if _, err := AcquireDirectoryLock(path, 10*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "held by process") {
		t.Fatalf("expected live owner diagnostic, got %v", err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock was not released: %v", err)
	}
}

func TestDirectoryLockReclaimsDeadOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := directoryLockOwner{
		PID: 2147483647, CreatedAt: time.Now().Add(-time.Hour), Token: "dead-owner",
	}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireDirectoryLock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	current, err := readDirectoryLockOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Token == owner.Token || current.PID != os.Getpid() {
		t.Fatalf("stale lock was not replaced: %#v", current)
	}
	release()
}
