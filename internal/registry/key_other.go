//go:build !windows

package registry

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const registryMACKeySize = 32

func loadRegistryMACKey(root string) ([]byte, error) {
	data, err := os.ReadFile(registryMACKeyPath(root))
	if err != nil {
		return nil, fmt.Errorf("read registry MAC key: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) != registryMACKeySize {
		return nil, fmt.Errorf("registry MAC key has invalid length")
	}
	return append([]byte(nil), data...), nil
}

func loadOrCreateRegistryMACKey(root string) ([]byte, error) {
	if key, err := loadRegistryMACKey(root); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	path := registryMACKeyPath(root)
	key := make([]byte, registryMACKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate registry MAC key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create registry key directory: %w", err)
	}
	if err := os.WriteFile(path, append(key, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write registry MAC key: %w", err)
	}
	return key, nil
}

func registryMACKeyPath(root string) string {
	return filepath.Join(root, "keys", "registry-mac.key")
}
