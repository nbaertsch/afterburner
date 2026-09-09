package registry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// IdentityBinding records the immutable material that a runtime grant was bound to.
// Hashes are hex-encoded SHA-256 values prefixed with "sha256:".
type IdentityBinding struct {
	ExtensionID       string `json:"extensionId"`
	ManifestHash      string `json:"manifestHash"`
	TreeHash          string `json:"treeHash"`
	SourceType        string `json:"sourceType"`
	SourceValue       string `json:"sourceValue"`
	SourceVersion     string `json:"sourceVersion,omitempty"`
	SourceCommit      string `json:"sourceCommit,omitempty"`
	SignerID          string `json:"signerId,omitempty"`
	SignerFingerprint string `json:"signerFingerprint,omitempty"`
	BuiltinSigned     bool   `json:"builtinSigned,omitempty"`
	RegistryEpoch     uint64 `json:"registryEpoch"`
	GrantEpoch        uint64 `json:"grantEpoch"`
	BoundAt           string `json:"boundAt"`
	RegistryMAC       string `json:"registryMac,omitempty"`
}

func (b IdentityBinding) IsZero() bool {
	return b.ExtensionID == "" && b.ManifestHash == "" && b.TreeHash == ""
}

func (b IdentityBinding) ValidateFor(entry Entry) error {
	return b.validateFor(entry, b.ManifestHash, b.TreeHash)
}

func (b IdentityBinding) ValidateForContent(root string, entry Entry, manifestHash, treeHash string) error {
	if err := b.validateFor(entry, manifestHash, treeHash); err != nil {
		return err
	}
	mac, err := RegistryMAC(root, entry)
	if err != nil {
		return err
	}
	if b.RegistryMAC == "" || !hmac.Equal([]byte(b.RegistryMAC), []byte(mac)) {
		return fmt.Errorf("identity binding registry MAC mismatch")
	}
	return nil
}

func (b IdentityBinding) validateFor(entry Entry, manifestHash, treeHash string) error {
	if b.ExtensionID == "" || b.ExtensionID != entry.Manifest.ID {
		return fmt.Errorf("identity binding extension mismatch")
	}
	if b.ManifestHash == "" || b.TreeHash == "" {
		return fmt.Errorf("identity binding missing content hashes")
	}
	if b.ManifestHash != manifestHash || b.TreeHash != treeHash {
		return fmt.Errorf("identity binding content hash mismatch")
	}
	if b.SourceType != entry.Source.Type || b.SourceValue != entry.Source.Value {
		return fmt.Errorf("identity binding source mismatch")
	}
	if entry.Source.Version != "" && b.SourceVersion != entry.Source.Version {
		return fmt.Errorf("identity binding source version mismatch")
	}
	if entry.Source.Commit != "" && b.SourceCommit != entry.Source.Commit {
		return fmt.Errorf("identity binding source commit mismatch")
	}

	if b.BuiltinSigned {
		if entry.Manifest.Visibility != "builtin" {
			return fmt.Errorf("non-built-in extension is bound as a signed built-in")
		}
		if !IsTrustedBuiltinSourceType(entry.Source.Type) || !IsTrustedBuiltinSourceType(b.SourceType) || entry.Source.Value != entry.Manifest.ID || b.SourceValue != entry.Manifest.ID {
			return fmt.Errorf("signed built-in identity must be bound to a verified built-in source")
		}
		if b.SignerID != "afterburner-release" || b.SignerFingerprint == "" ||
			b.SignerFingerprint != entry.Source.SignerFingerprint ||
			entry.Source.Version == "" || entry.Source.Commit == "" ||
			entry.Source.Digest == "" || entry.Source.ManifestDigest == "" {
			return fmt.Errorf("signed built-in identity has an untrusted signer")
		}
	}
	if entry.Manifest.Visibility == "builtin" && !b.BuiltinSigned {
		return fmt.Errorf("built-in extension is not bound to a signed default")
	}
	return nil
}

func (b IdentityBinding) IsTrustedBuiltinFor(id string) bool {
	return b.ExtensionID == id && b.BuiltinSigned && b.ManifestHash != "" && b.TreeHash != "" &&
		IsTrustedBuiltinSourceType(b.SourceType) && b.SourceValue == id &&
		b.SignerID == "afterburner-release" && b.SignerFingerprint != "" && b.GrantEpoch > 0
}

type registryMACPayload struct {
	Enabled            bool              `json:"enabled"`
	ActivePath         string            `json:"activePath"`
	PreviousActivePath *string           `json:"previousActivePath,omitempty"`
	PreviousSource     *Source           `json:"previousSource,omitempty"`
	PreviousPackage    *PackageReference `json:"previousPackage,omitempty"`
	Manifest           Manifest          `json:"manifest"`
	Source             Source            `json:"source"`
	Identity           IdentityBinding   `json:"identity"`
	UpdatedAt          string            `json:"updatedAt"`
}

func RegistryMAC(root string, entry Entry) (string, error) {
	key, err := loadRegistryMACKey(root)
	if err != nil {
		return "", err
	}
	entry.Identity.RegistryMAC = ""
	payload := registryMACPayload{
		Enabled:            entry.Enabled,
		ActivePath:         entry.ActivePath,
		PreviousActivePath: entry.PreviousActivePath,
		PreviousSource:     entry.PreviousSource,
		PreviousPackage:    entry.PreviousPackage,
		Manifest:           entry.Manifest,
		Source:             entry.Source,
		Identity:           entry.Identity,
		UpdatedAt:          entry.UpdatedAt,
	}
	encoded, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)), nil
}

func SealEntry(root string, entry Entry) (Entry, error) {
	normalizeManifestNames(&entry.Manifest)
	key, err := loadOrCreateRegistryMACKey(root)
	if err != nil {
		return Entry{}, err
	}
	entry.Identity.RegistryMAC = ""
	payload := registryMACPayload{
		Enabled:            entry.Enabled,
		ActivePath:         entry.ActivePath,
		PreviousActivePath: entry.PreviousActivePath,
		PreviousSource:     entry.PreviousSource,
		PreviousPackage:    entry.PreviousPackage,
		Manifest:           entry.Manifest,
		Source:             entry.Source,
		Identity:           entry.Identity,
		UpdatedAt:          entry.UpdatedAt,
	}
	encoded, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	entry.Identity.RegistryMAC = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	entry.Verified = true
	return entry, nil
}
