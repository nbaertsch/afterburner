package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nbaertsch/afterburner/internal/platform"
	"github.com/nbaertsch/afterburner/internal/registry"
)

const registrySnapshotPattern = "core-update-registry-backup-*.json"

type registrySnapshot struct {
	SchemaVersion int    `json:"schemaVersion"`
	Existed       bool   `json:"existed"`
	Data          []byte `json:"data,omitempty"`
}

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

func registrySnapshotWithinRoot(root, path string) bool {
	return registry.Within(path, filepath.Join(root, "state"))
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
