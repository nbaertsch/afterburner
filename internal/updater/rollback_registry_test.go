package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPreserveCoreRollbackRegistrySnapshot(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreserveCoreRollbackRegistrySnapshot(root, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("transaction snapshot was removed before commit: %v", err)
	}
	if err := DiscardRegistrySnapshot(root, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreRegistrySnapshot(root, CoreRollbackRegistrySnapshot(root)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("restored registry = %q", data)
	}
}

func TestCoreRollbackSwapsExecutableAndRegistryState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("replacement semantics are validated on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "bin", "afterburn.exe")
	previous := filepath.Join(root, "bin", "afterburn.previous.exe")
	source := filepath.Join(root, "update-staging", "rollback", "afterburn.exe")
	registryPath := filepath.Join(root, "registry.json")

	copyFile(t, buildFixtureExecutable(t, "new", true), target)
	copyFile(t, buildFixtureExecutable(t, "old", true), previous)
	copyFile(t, previous, source)

	if err := os.WriteFile(registryPath, []byte("old-registry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreserveCoreRollbackRegistrySnapshot(root, oldSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := DiscardRegistrySnapshot(root, oldSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new-registry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, currentSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := SetCoreUpdateSuccessSnapshot(root, CoreRollbackRegistrySnapshot(root)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyReplacement(
		0,
		source,
		target,
		previous,
		currentSnapshot,
		CoreRollbackRegistrySnapshot(root),
	); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, target, "old")
	assertVersion(t, previous, "new")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old-registry\n" {
		t.Fatalf("rollback registry = %q", data)
	}
	if err := RestoreRegistrySnapshot(root, CoreRollbackRegistrySnapshot(root)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-registry\n" {
		t.Fatalf("next rollback registry = %q", data)
	}
}
