package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/ui/security"
)

type Signer struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject,omitempty"`
}

type IdentityInput struct {
	Manifest       registry.Manifest `json:"manifest"`
	Source         registry.Source   `json:"source"`
	PackageRoot    string            `json:"-"`
	Signer         Signer            `json:"signer"`
	RegistryEpoch  uint64            `json:"registryEpoch"`
	GrantEpoch     uint64            `json:"grantEpoch"`
	BuiltinDefault bool              `json:"builtinDefault,omitempty"`
	BoundAt        time.Time         `json:"boundAt"`
}

func BindIdentity(input IdentityInput) (registry.IdentityBinding, error) {
	if input.Manifest.ID == "" {
		return registry.IdentityBinding{}, fmt.Errorf("extension manifest id is required")
	}
	if input.PackageRoot == "" {
		return registry.IdentityBinding{}, fmt.Errorf("package root is required")
	}
	manifestHash, err := fileHash(filepath.Join(input.PackageRoot, "afterburner.json"))
	if err != nil {
		return registry.IdentityBinding{}, err
	}
	treeHash, err := TreeHash(input.PackageRoot)
	if err != nil {
		return registry.IdentityBinding{}, err
	}
	if input.RegistryEpoch == 0 {
		input.RegistryEpoch = uint64(time.Now().UTC().UnixNano())
	}
	if input.GrantEpoch == 0 {
		input.GrantEpoch = 1
	}
	if input.BoundAt.IsZero() {
		input.BoundAt = time.Now().UTC()
	}
	return registry.IdentityBinding{
		ExtensionID:       input.Manifest.ID,
		ManifestHash:      "sha256:" + manifestHash,
		TreeHash:          "sha256:" + treeHash,
		SourceType:        input.Source.Type,
		SourceValue:       input.Source.Value,
		SourceVersion:     input.Source.Version,
		SourceCommit:      input.Source.Commit,
		SignerID:          input.Signer.ID,
		SignerFingerprint: input.Signer.Fingerprint,
		BuiltinSigned:     input.BuiltinDefault,
		RegistryEpoch:     input.RegistryEpoch,
		GrantEpoch:        input.GrantEpoch,
		BoundAt:           input.BoundAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func VerifyIdentity(entry registry.Entry, packageRoot string) error {
	if entry.Identity.IsZero() {
		return fmt.Errorf("identity binding is missing")
	}
	if err := entry.Identity.ValidateFor(entry); err != nil {
		return err
	}
	if packageRoot != "" {
		manifestHash, err := fileHash(filepath.Join(packageRoot, "afterburner.json"))
		if err != nil {
			return err
		}
		if entry.Identity.ManifestHash != "sha256:"+manifestHash {
			return fmt.Errorf("manifest hash mismatch")
		}
		treeHash, err := TreeHash(packageRoot)
		if err != nil {
			return err
		}
		if entry.Identity.TreeHash != "sha256:"+treeHash {
			return fmt.Errorf("tree hash mismatch")
		}
	}
	return nil
}

func BindingDigest(binding registry.IdentityBinding) (string, error) {
	canonical, err := security.CanonicalJSON(binding)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func TreeHash(root string) (string, error) {
	hash := sha256.New()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("extension packages may not contain symbolic links: %s", relative)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "bin" || entry.Name() == "obj" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, path := range files {
		relative, _ := filepath.Rel(root, path)
		hash.Write([]byte(filepath.ToSlash(relative)))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func CanonicalManifest(manifest registry.Manifest) ([]byte, error) {
	return json.Marshal(manifest)
}
