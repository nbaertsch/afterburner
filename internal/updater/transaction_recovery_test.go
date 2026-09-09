package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverInterruptedCoreUpdateRestoresRegistry(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, snapshot); err != nil {
		t.Fatal(err)
	}
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		t.Fatal(err)
	}
	transaction.OwnerPID = 1<<30 - 1
	if err := saveCoreUpdateTransaction(root, transaction); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedCoreUpdate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("interrupted transaction was not recovered")
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("recovered registry = %q", data)
	}
	for _, path := range []string{snapshot, coreUpdateTransactionPath(root)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("transaction artifact still exists at %s: %v", path, err)
		}
	}
}

func TestAbortCoreUpdateRestoresRegistryWhenExecutableSnapshotIsUnavailable(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, snapshot); err != nil {
		t.Fatal(err)
	}
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, candidate, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(transaction.OriginalTargetSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AbortCoreUpdateTransaction(root); err == nil {
		t.Fatal("expected executable rollback failure")
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("registry = %q", data)
	}
	if _, err := os.Stat(coreUpdateTransactionPath(root)); err != nil {
		t.Fatalf("failed rollback discarded recovery journal: %v", err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("failed rollback discarded registry snapshot: %v", err)
	}
}

func TestRecoverInterruptedCoreUpdateLeavesActiveTransaction(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	snapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, snapshot); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedCoreUpdate(root)
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("active transaction was recovered")
	}
	if _, err := os.Stat(coreUpdateTransactionPath(root)); err != nil {
		t.Fatalf("active transaction journal was removed: %v", err)
	}
	if err := AbortCoreUpdateTransaction(root); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverInterruptedCoreUpdateCompletesPublishedExecutable(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failureSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, failureSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	successSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetCoreUpdateSuccessSnapshot(root, successSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := RestoreRegistrySnapshot(root, failureSnapshot); err != nil {
		t.Fatal(err)
	}
	copyFile(t, source, target)
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		t.Fatal(err)
	}
	transaction.OwnerPID = 1<<30 - 1
	if err := saveCoreUpdateTransaction(root, transaction); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedCoreUpdate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("published executable transaction was not recovered")
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("recovered registry = %q", data)
	}
	if err := RestoreRegistrySnapshot(root, CoreRollbackRegistrySnapshot(root)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("rollback registry = %q", data)
	}
}

func TestRecoverInterruptedCoreUpdateAfterSuccessSnapshotCleanup(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failureSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, failureSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	successSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetCoreUpdateSuccessSnapshot(root, successSnapshot); err != nil {
		t.Fatal(err)
	}
	copyFile(t, source, target)
	if err := PreserveCoreRollbackRegistrySnapshot(root, failureSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := DiscardRegistrySnapshot(root, successSnapshot); err != nil {
		t.Fatal(err)
	}
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		t.Fatal(err)
	}
	transaction.OwnerPID = 1<<30 - 1
	if err := saveCoreUpdateTransaction(root, transaction); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedCoreUpdate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("partially committed transaction was not recovered")
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("recovered registry = %q", data)
	}
	if _, err := os.Stat(failureSnapshot); !os.IsNotExist(err) {
		t.Fatalf("failure snapshot still exists: %v", err)
	}
}

func TestRecoverInterruptedCoreUpdatePublishesPreparedBinDirectory(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failureSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, failureSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	successSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetCoreUpdateSuccessSnapshot(root, successSnapshot); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(root, ".replacement-bin-test")
	retired := filepath.Join(root, ".retired-bin-test")
	if err := os.MkdirAll(prepared, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(t, source, filepath.Join(prepared, "afterburn.exe"))
	copyFile(t, target, filepath.Join(prepared, "afterburn.previous.exe"))
	if err := SetCoreUpdateSwapPaths(root, prepared, retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(target), retired); err != nil {
		t.Fatal(err)
	}
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		t.Fatal(err)
	}
	transaction.OwnerPID = 1<<30 - 1
	if err := saveCoreUpdateTransaction(root, transaction); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedCoreUpdate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("directory-swap transaction was not recovered")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("published executable = %q", data)
	}
	data, err = os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("recovered registry = %q", data)
	}
}

func TestBeginCoreUpdateTransactionRejectsConcurrentTransaction(t *testing.T) {
	root := t.TempDir()
	source, target, previous := transactionExecutablePaths(t, root, "new", "old")
	snapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, snapshot); err != nil {
		t.Fatal(err)
	}
	secondSnapshot, err := SnapshotRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCoreUpdateTransaction(root, source, target, previous, secondSnapshot); err == nil {
		t.Fatal("concurrent transaction was accepted")
	}
	if err := DiscardRegistrySnapshot(root, secondSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := AbortCoreUpdateTransaction(root); err != nil {
		t.Fatal(err)
	}
}

func transactionExecutablePaths(t *testing.T, root, candidate, current string) (string, string, string) {
	t.Helper()
	source := filepath.Join(root, "update-staging", "transaction", "afterburn.exe")
	target := filepath.Join(root, "bin", "afterburn.exe")
	previous := filepath.Join(root, "bin", "afterburn.previous.exe")
	for path, content := range map[string]string{source: candidate, target: current, previous: "previous"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return source, target, previous
}
