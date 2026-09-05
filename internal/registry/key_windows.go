//go:build windows

package registry

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const registryMACKeySize = 32

func loadRegistryMACKey(root string) ([]byte, error) {
	data, err := os.ReadFile(registryMACKeyPath(root))
	if err != nil {
		return nil, fmt.Errorf("read registry MAC key: %w", err)
	}
	return unprotectRegistryMACKey(strings.TrimSpace(string(data)))
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
	sealed, err := protectRegistryMACKey(key)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create registry key directory: %w", err)
	}
	if err := restrictRegistryKeyPath(dir); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(sealed+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write registry MAC key: %w", err)
	}
	if err := restrictRegistryKeyPath(path); err != nil {
		return nil, err
	}
	return key, nil
}

func registryMACKeyPath(root string) string {
	return filepath.Join(root, "keys", "registry-mac.dpapi")
}

func restrictRegistryKeyPath(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve registry key owner: %w", err)
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + sid + ")")
	if err != nil {
		return fmt.Errorf("create registry key security descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read registry key DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("secure registry key ACL: %w", err)
	}
	return nil
}

func protectRegistryMACKey(key []byte) (string, error) {
	in := bytesToBlob(key)
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString("Afterburner registry integrity key")
	if err := windows.CryptProtectData(&in, name, nil, 0, nil, 0, &out); err != nil {
		return "", fmt.Errorf("protect registry MAC key with DPAPI: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck // Windows-owned buffer
	return "dpapi-v1:" + base64.StdEncoding.EncodeToString(blobBytes(out)), nil
}

func unprotectRegistryMACKey(encoded string) ([]byte, error) {
	payload, ok := strings.CutPrefix(encoded, "dpapi-v1:")
	if !ok {
		return nil, fmt.Errorf("registry MAC key is not DPAPI protected")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode registry MAC key: %w", err)
	}
	in := bytesToBlob(ciphertext)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("unprotect registry MAC key with DPAPI: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) //nolint:errcheck // Windows-owned buffer
	key := append([]byte(nil), blobBytes(out)...)
	if len(key) != registryMACKeySize {
		return nil, fmt.Errorf("registry MAC key has invalid length")
	}
	return key, nil
}

func bytesToBlob(value []byte) windows.DataBlob {
	if len(value) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
}

func blobBytes(blob windows.DataBlob) []byte {
	if blob.Data == nil || blob.Size == 0 {
		return nil
	}
	return unsafe.Slice(blob.Data, blob.Size)
}
