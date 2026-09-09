package updater

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/registry"
)

const (
	registrySnapshotPattern      = "core-update-registry-backup-*.json"
	coreRollbackRegistrySnapshot = "core-rollback-registry.json"
	coreUpdateTransactionFile    = "core-update-transaction.json"
)

type registrySnapshot struct {
	SchemaVersion int    `json:"schemaVersion"`
	Existed       bool   `json:"existed"`
	Data          []byte `json:"data,omitempty"`
}

type coreUpdateTransaction struct {
	SchemaVersion          int    `json:"schemaVersion"`
	OwnerPID               int    `json:"ownerPid"`
	ReplacementPID         int    `json:"replacementPid,omitempty"`
	SourcePath             string `json:"sourcePath"`
	TargetPath             string `json:"targetPath"`
	PreviousPath           string `json:"previousPath"`
	PreviousSnapshot       string `json:"previousSnapshot,omitempty"`
	PreviousExisted        bool   `json:"previousExisted"`
	OriginalTargetSnapshot string `json:"originalTargetSnapshot"`
	PreparedBinPath        string `json:"preparedBinPath,omitempty"`
	RetiredBinPath         string `json:"retiredBinPath,omitempty"`
	OriginalTargetSHA256   string `json:"originalTargetSha256"`
	CandidateSHA256        string `json:"candidateSha256"`
	FailureSnapshot        string `json:"failureSnapshot"`
	SuccessSnapshot        string `json:"successSnapshot,omitempty"`
	StartedAt              string `json:"startedAt"`
}

const coreUpdatePIDTrustWindow = 15 * time.Minute

func SnapshotRegistry(root string) (string, error) {
	snapshot := registrySnapshot{SchemaVersion: 1}
	data, err := os.ReadFile(registry.Path(root))
	if err == nil {
		snapshot.Existed = true
		snapshot.Data = data
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("snapshot extension registry: %w", err)
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return "", fmt.Errorf("create extension registry snapshot directory: %w", err)
	}
	file, err := os.CreateTemp(stateRoot, registrySnapshotPattern)
	if err != nil {
		return "", fmt.Errorf("create extension registry snapshot: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close extension registry snapshot: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("prepare extension registry snapshot: %w", err)
	}
	if err := replaceData(path, encoded); err != nil {
		return "", fmt.Errorf("write extension registry snapshot: %w", err)
	}
	return path, nil
}

func RestoreRegistrySnapshot(root, path string) error {
	if path == "" {
		return nil
	}
	if !registrySnapshotWithinRoot(root, path) {
		return fmt.Errorf("registry snapshot is outside the managed state root")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read extension registry snapshot: %w", err)
	}
	var snapshot registrySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.SchemaVersion != 1 {
		return fmt.Errorf("invalid extension registry snapshot")
	}
	target := registry.Path(root)
	if !snapshot.Existed {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove extension registry during rollback: %w", err)
		}
		return nil
	}
	if err := replaceData(target, snapshot.Data); err != nil {
		return fmt.Errorf("restore extension registry snapshot: %w", err)
	}
	return nil
}

func DiscardRegistrySnapshot(root, path string) error {
	if path == "" {
		return nil
	}
	if !registrySnapshotWithinRoot(root, path) {
		return fmt.Errorf("registry snapshot is outside the managed state root")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove extension registry snapshot: %w", err)
	}
	return nil
}

func CoreRollbackRegistrySnapshot(root string) string {
	return filepath.Join(root, "state", coreRollbackRegistrySnapshot)
}

func PreserveCoreRollbackRegistrySnapshot(root, path string) error {
	if path == "" {
		return nil
	}
	if !registrySnapshotWithinRoot(root, path) {
		return fmt.Errorf("registry snapshot is outside the managed state root")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read extension registry snapshot: %w", err)
	}
	var snapshot registrySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.SchemaVersion != 1 {
		return fmt.Errorf("invalid extension registry snapshot")
	}
	target := CoreRollbackRegistrySnapshot(root)
	if err := replaceData(target, data); err != nil {
		return fmt.Errorf("preserve core rollback registry snapshot: %w", err)
	}
	return nil
}

func BeginCoreUpdateTransaction(root, source, target, previous, failureSnapshot string) error {
	if !registrySnapshotWithinRoot(root, failureSnapshot) {
		return fmt.Errorf("registry snapshot is outside the managed state root")
	}
	if !registry.Within(source, filepath.Join(root, "update-staging")) ||
		!registry.Within(target, filepath.Join(root, "bin")) ||
		!registry.Within(previous, filepath.Join(root, "bin")) {
		return fmt.Errorf("core update paths are outside the managed installation")
	}
	originalDigest, err := fileSHA256(target)
	if err != nil {
		return fmt.Errorf("hash installed executable: %w", err)
	}
	candidateDigest, err := fileSHA256(source)
	if err != nil {
		return fmt.Errorf("hash staged executable: %w", err)
	}
	originalTargetSnapshot, targetExisted, err := snapshotManagedFile(root, target)
	if err != nil {
		return fmt.Errorf("snapshot installed executable: %w", err)
	}
	if !targetExisted {
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("installed executable does not exist")
	}
	previousSnapshot, previousExisted, err := snapshotManagedFile(root, previous)
	if err != nil {
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("snapshot previous executable: %w", err)
	}
	transaction := coreUpdateTransaction{
		SchemaVersion:          2,
		OwnerPID:               os.Getpid(),
		SourcePath:             filepath.Clean(source),
		TargetPath:             filepath.Clean(target),
		PreviousPath:           filepath.Clean(previous),
		PreviousSnapshot:       previousSnapshot,
		PreviousExisted:        previousExisted,
		OriginalTargetSnapshot: originalTargetSnapshot,
		OriginalTargetSHA256:   originalDigest,
		CandidateSHA256:        candidateDigest,
		FailureSnapshot:        failureSnapshot,
		StartedAt:              time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(coreUpdateTransactionPath(root), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		_ = discardManagedFileSnapshot(root, previousSnapshot)
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("another core update transaction is already in progress")
	}
	if err != nil {
		_ = discardManagedFileSnapshot(root, previousSnapshot)
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("create core update transaction journal: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		file.Close()
		_ = os.Remove(coreUpdateTransactionPath(root))
		_ = discardManagedFileSnapshot(root, previousSnapshot)
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("write core update transaction journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(coreUpdateTransactionPath(root))
		_ = discardManagedFileSnapshot(root, previousSnapshot)
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("sync core update transaction journal: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(coreUpdateTransactionPath(root))
		_ = discardManagedFileSnapshot(root, previousSnapshot)
		_ = discardManagedFileSnapshot(root, originalTargetSnapshot)
		return fmt.Errorf("close core update transaction journal: %w", err)
	}
	return nil
}

func SetCoreUpdateSwapPaths(root, preparedBinPath, retiredBinPath string) error {
	transaction, err := loadCoreUpdateTransaction(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !registry.Within(preparedBinPath, root) || !registry.Within(retiredBinPath, root) {
		return fmt.Errorf("core update swap paths are outside the managed installation")
	}
	transaction.PreparedBinPath = filepath.Clean(preparedBinPath)
	transaction.RetiredBinPath = filepath.Clean(retiredBinPath)
	return saveCoreUpdateTransaction(root, transaction)
}

func SetCoreUpdateSuccessSnapshot(root, successSnapshot string) error {
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil {
		return err
	}
	if !registrySnapshotWithinRoot(root, successSnapshot) {
		return fmt.Errorf("registry snapshot is outside the managed state root")
	}
	transaction.SuccessSnapshot = successSnapshot
	return saveCoreUpdateTransaction(root, transaction)
}

func ClaimCoreUpdateTransaction(root string, ownerPID int, source, target, previous, failureSnapshot, successSnapshot string) error {
	transaction, err := loadCoreUpdateTransaction(root)
	if os.IsNotExist(err) {
		if failureSnapshot == "" && successSnapshot == "" {
			return nil
		}
		return fmt.Errorf("core update transaction journal is missing")
	}
	if err != nil {
		return err
	}
	expectedOwnerPID := ownerPID
	if expectedOwnerPID <= 0 {
		expectedOwnerPID = os.Getpid()
	}
	if transaction.OwnerPID != expectedOwnerPID ||
		!sameFilePath(transaction.SourcePath, source) ||
		!sameFilePath(transaction.TargetPath, target) ||
		!sameFilePath(transaction.PreviousPath, previous) ||
		!sameFilePath(transaction.FailureSnapshot, failureSnapshot) ||
		!sameFilePath(transaction.SuccessSnapshot, successSnapshot) {
		return fmt.Errorf("core update transaction does not match the replacement request")
	}
	transaction.ReplacementPID = os.Getpid()
	return saveCoreUpdateTransaction(root, transaction)
}

func CompleteCoreUpdateTransaction(root string) error {
	transaction, err := loadCoreUpdateTransaction(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := discardManagedFileSnapshot(root, transaction.PreviousSnapshot); err != nil {
			return err
		}
		if err := discardManagedFileSnapshot(root, transaction.OriginalTargetSnapshot); err != nil {
			return err
		}
		if transaction.PreparedBinPath != "" {
			if err := os.RemoveAll(transaction.PreparedBinPath); err != nil {
				return fmt.Errorf("remove prepared bin directory: %w", err)
			}
		}
		if transaction.RetiredBinPath != "" {
			_ = os.RemoveAll(transaction.RetiredBinPath)
		}
	}
	path := coreUpdateTransactionPath(root)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove core update transaction journal: %w", err)
	}
	return nil
}

func AbortCoreUpdateTransaction(root string) error {
	transaction, err := loadCoreUpdateTransaction(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := restoreOriginalTarget(root, transaction); err != nil {
		return err
	}
	if err := RestoreRegistrySnapshot(root, transaction.FailureSnapshot); err != nil {
		return err
	}
	if err := restorePreviousExecutable(root, transaction); err != nil {
		return err
	}
	if err := DiscardRegistrySnapshot(root, transaction.FailureSnapshot); err != nil {
		return err
	}
	if transaction.SuccessSnapshot != "" &&
		!sameFilePath(transaction.SuccessSnapshot, CoreRollbackRegistrySnapshot(root)) {
		if err := DiscardRegistrySnapshot(root, transaction.SuccessSnapshot); err != nil {
			return err
		}
	}
	if transaction.PreparedBinPath != "" {
		if err := os.RemoveAll(transaction.PreparedBinPath); err != nil {
			return fmt.Errorf("remove prepared bin directory: %w", err)
		}
	}
	return CompleteCoreUpdateTransaction(root)
}

func RecoverInterruptedCoreUpdate(root string) (bool, error) {
	transaction, err := loadCoreUpdateTransaction(root)
	if os.IsNotExist(err) {
		pruneRetiredBinDirectories(root)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, transaction.StartedAt)
	if time.Since(startedAt) <= coreUpdatePIDTrustWindow &&
		(platform.ProcessExists(transaction.OwnerPID) || platform.ProcessExists(transaction.ReplacementPID)) {
		return false, nil
	}
	if _, err := os.Stat(transaction.TargetPath); os.IsNotExist(err) {
		switch {
		case directoryExists(transaction.PreparedBinPath):
			if err := os.Rename(transaction.PreparedBinPath, filepath.Dir(transaction.TargetPath)); err != nil {
				return false, fmt.Errorf("publish prepared bin directory during recovery: %w", err)
			}
		case directoryExists(transaction.RetiredBinPath):
			if err := os.Rename(transaction.RetiredBinPath, filepath.Dir(transaction.TargetPath)); err != nil {
				return false, fmt.Errorf("restore retired bin directory during recovery: %w", err)
			}
		default:
			return false, fmt.Errorf("interrupted core update left no recoverable bin directory")
		}
	} else if err != nil {
		return false, fmt.Errorf("inspect installed executable during recovery: %w", err)
	}
	targetDigest, digestErr := fileSHA256(transaction.TargetPath)
	if digestErr != nil {
		return false, fmt.Errorf("inspect executable during interrupted core update recovery: %w", digestErr)
	}
	if targetDigest == transaction.CandidateSHA256 {
		if transaction.SuccessSnapshot == "" {
			return false, fmt.Errorf("interrupted core update is missing its success registry snapshot")
		}
		failureExists := true
		if _, err := os.Stat(transaction.FailureSnapshot); os.IsNotExist(err) {
			failureExists = false
		} else if err != nil {
			return false, fmt.Errorf("inspect interrupted core update rollback snapshot: %w", err)
		}
		successExists := true
		if _, err := os.Stat(transaction.SuccessSnapshot); os.IsNotExist(err) {
			successExists = false
		} else if err != nil {
			return false, fmt.Errorf("inspect interrupted core update success snapshot: %w", err)
		}
		if successExists {
			if err := RestoreRegistrySnapshot(root, transaction.SuccessSnapshot); err != nil {
				return false, fmt.Errorf("restore successful core update registry: %w", err)
			}
		} else if _, err := os.Stat(CoreRollbackRegistrySnapshot(root)); err != nil {
			return false, fmt.Errorf("interrupted core update is missing both its success and rollback registry snapshots")
		}
		if failureExists {
			if err := PreserveCoreRollbackRegistrySnapshot(root, transaction.FailureSnapshot); err != nil {
				return false, err
			}
		}
		if successExists && !sameFilePath(transaction.SuccessSnapshot, CoreRollbackRegistrySnapshot(root)) {
			if err := DiscardRegistrySnapshot(root, transaction.SuccessSnapshot); err != nil {
				return false, err
			}
		}
		if failureExists {
			if err := DiscardRegistrySnapshot(root, transaction.FailureSnapshot); err != nil {
				return false, err
			}
		}
		if err := CompleteCoreUpdateTransaction(root); err != nil {
			return false, err
		}
		return true, nil
	}
	if targetDigest != transaction.OriginalTargetSHA256 {
		return false, fmt.Errorf("installed executable does not match either side of the interrupted core update")
	}
	if _, err := os.Stat(transaction.FailureSnapshot); os.IsNotExist(err) {
		return false, fmt.Errorf("interrupted core update is missing its registry snapshot")
	} else if err != nil {
		return false, fmt.Errorf("inspect interrupted core update registry snapshot: %w", err)
	}
	if err := AbortCoreUpdateTransaction(root); err != nil {
		return false, fmt.Errorf("recover interrupted core update: %w", err)
	}
	return true, nil
}

func saveCoreUpdateTransaction(root string, transaction coreUpdateTransaction) error {
	encoded, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	return replaceData(coreUpdateTransactionPath(root), append(encoded, '\n'))
}

func loadCoreUpdateTransaction(root string) (coreUpdateTransaction, error) {
	data, err := os.ReadFile(coreUpdateTransactionPath(root))
	if err != nil {
		return coreUpdateTransaction{}, err
	}
	var transaction coreUpdateTransaction
	if err := json.Unmarshal(data, &transaction); err != nil ||
		transaction.SchemaVersion != 2 ||
		!registry.Within(transaction.SourcePath, filepath.Join(root, "update-staging")) ||
		!registry.Within(transaction.TargetPath, filepath.Join(root, "bin")) ||
		!registry.Within(transaction.PreviousPath, filepath.Join(root, "bin")) ||
		(transaction.PreviousExisted && transaction.PreviousSnapshot == "") ||
		(transaction.PreviousSnapshot != "" && !registrySnapshotWithinRoot(root, transaction.PreviousSnapshot)) ||
		!registrySnapshotWithinRoot(root, transaction.OriginalTargetSnapshot) ||
		(transaction.PreparedBinPath != "" && !registry.Within(transaction.PreparedBinPath, root)) ||
		(transaction.RetiredBinPath != "" && !registry.Within(transaction.RetiredBinPath, root)) ||
		len(transaction.OriginalTargetSHA256) != 64 ||
		len(transaction.CandidateSHA256) != 64 ||
		func() bool {
			_, err := time.Parse(time.RFC3339Nano, transaction.StartedAt)
			return err != nil
		}() ||
		!registrySnapshotWithinRoot(root, transaction.FailureSnapshot) ||
		(transaction.SuccessSnapshot != "" && !registrySnapshotWithinRoot(root, transaction.SuccessSnapshot)) {
		return coreUpdateTransaction{}, fmt.Errorf("invalid core update transaction journal")
	}
	return transaction, nil
}

func coreUpdateTransactionPath(root string) string {
	return filepath.Join(root, "state", coreUpdateTransactionFile)
}

func registrySnapshotWithinRoot(root, path string) bool {
	return registry.Within(path, filepath.Join(root, "state"))
}

func sameFilePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func snapshotManagedFile(root, path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return "", false, err
	}
	file, err := os.CreateTemp(stateRoot, "core-update-previous-*.exe")
	if err != nil {
		return "", false, err
	}
	snapshot := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(snapshot)
		return "", false, err
	}
	if err := os.Remove(snapshot); err != nil {
		return "", false, err
	}
	if err := replaceData(snapshot, data); err != nil {
		return "", false, err
	}
	return snapshot, true, nil
}

func restorePreviousExecutable(root string, transaction coreUpdateTransaction) error {
	if !transaction.PreviousExisted {
		if err := os.Remove(transaction.PreviousPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove previous executable during rollback: %w", err)
		}
		return nil
	}
	if !registrySnapshotWithinRoot(root, transaction.PreviousSnapshot) {
		return fmt.Errorf("previous executable snapshot is outside the managed state root")
	}
	data, err := os.ReadFile(transaction.PreviousSnapshot)
	if err != nil {
		return fmt.Errorf("read previous executable snapshot: %w", err)
	}
	if err := replaceData(transaction.PreviousPath, data); err != nil {
		return fmt.Errorf("restore previous executable: %w", err)
	}
	return nil
}

func restoreOriginalTarget(root string, transaction coreUpdateTransaction) error {
	if !registrySnapshotWithinRoot(root, transaction.OriginalTargetSnapshot) {
		return fmt.Errorf("installed executable snapshot is outside the managed state root")
	}
	data, err := os.ReadFile(transaction.OriginalTargetSnapshot)
	if err != nil {
		return fmt.Errorf("read installed executable snapshot: %w", err)
	}
	if err := replaceData(transaction.TargetPath, data); err != nil {
		return fmt.Errorf("restore installed executable: %w", err)
	}
	digest, err := fileSHA256(transaction.TargetPath)
	if err != nil {
		return fmt.Errorf("verify restored installed executable: %w", err)
	}
	if digest != transaction.OriginalTargetSHA256 {
		return fmt.Errorf("restored installed executable does not match the transaction snapshot")
	}
	return nil
}

func discardManagedFileSnapshot(root, path string) error {
	if path == "" {
		return nil
	}
	if !registrySnapshotWithinRoot(root, path) {
		return fmt.Errorf("managed file snapshot is outside the state root")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func directoryExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pruneRetiredBinDirectories(root string) {
	paths, err := filepath.Glob(filepath.Join(root, ".retired-bin-*"))
	if err != nil {
		return
	}
	for _, path := range paths {
		if registry.Within(path, root) {
			_ = os.RemoveAll(path)
		}
	}
}

func replaceData(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".afterburn-transaction-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return platform.ReplaceFile(temporaryPath, path)
}
