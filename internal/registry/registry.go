package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nbaertsch/afterburner/internal/platform"
)

type Manifest struct {
	SchemaVersion    int               `json:"schemaVersion"`
	ID               string            `json:"id"`
	Name             string            `json:"name,omitempty"`
	DisplayName      string            `json:"displayName,omitempty"`
	Description      string            `json:"description,omitempty"`
	Visibility       string            `json:"visibility"`
	Requires         Requirements      `json:"requires"`
	Runtime          RuntimeManifest   `json:"runtime"`
	Capabilities     []string          `json:"capabilities,omitempty"`
	UI               *UIManifest       `json:"ui,omitempty"`
	SessionExtension *SessionExtension `json:"sessionExtension,omitempty"`
}

type UIManifest struct {
	Protocol string      `json:"protocol"`
	Revision int         `json:"revision"`
	Surfaces []UISurface `json:"surfaces"`
}

type UISurface struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
}

type Requirements struct {
	Afterburner string   `json:"afterburner"`
	CopilotCLI  []string `json:"copilotCli,omitempty"`
}

type RuntimeManifest struct {
	Execution  string `json:"execution"`
	Entrypoint string `json:"entrypoint"`
}

type SessionExtension struct {
	Entrypoint string `json:"entrypoint"`
}

type Source struct {
	Type              string  `json:"type"`
	Value             string  `json:"value"`
	Version           string  `json:"version,omitempty"`
	Ref               *string `json:"ref,omitempty"`
	Commit            string  `json:"commit,omitempty"`
	Digest            string  `json:"digest,omitempty"`
	ManifestDigest    string  `json:"manifestDigest,omitempty"`
	SignerFingerprint string  `json:"signerFingerprint,omitempty"`
}

type Entry struct {
	Enabled            bool              `json:"enabled"`
	ActivePath         string            `json:"activePath"`
	PreviousActivePath *string           `json:"previousActivePath"`
	PreviousSource     *Source           `json:"previousSource,omitempty"`
	PreviousPackage    *PackageReference `json:"previousPackage,omitempty"`
	Manifest           Manifest          `json:"manifest"`
	Source             Source            `json:"source"`
	Identity           IdentityBinding   `json:"identity,omitempty"`
	UpdatedAt          string            `json:"updatedAt"`
	Verified           bool              `json:"-"`
}

type PackageReference struct {
	ActivePath        string `json:"activePath"`
	ManifestHash      string `json:"manifestHash"`
	TreeHash          string `json:"treeHash"`
	Source            Source `json:"source"`
	BuiltinSigned     bool   `json:"builtinSigned,omitempty"`
	SignerID          string `json:"signerId,omitempty"`
	SignerFingerprint string `json:"signerFingerprint,omitempty"`
}

const (
	OpenAIServerID       = "openai-server"
	SubagentPolicyID     = "subagent-policy"
	LegacyOpenAIServerID = "copilot-openai"
	OpenAIServerName     = "OpenAI Server"
	UIProtocol           = "afterburner.ui"
	UIRevision           = 1
	MaxUISurfaces        = 16
)

var validUISurfaceID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Registry struct {
	SchemaVersion int              `json:"schemaVersion"`
	Epoch         uint64           `json:"epoch,omitempty"`
	Extensions    map[string]Entry `json:"extensions"`
}

func Path(root string) string {
	return filepath.Join(root, "registry.json")
}

func Load(root string) (Registry, error) {
	path := Path(root)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Registry{SchemaVersion: 1, Extensions: map[string]Entry{}}, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read extension registry: %w", err)
	}
	var value Registry
	if err := json.Unmarshal(data, &value); err != nil {
		return Registry{}, fmt.Errorf("parse extension registry: %w", err)
	}
	if value.SchemaVersion != 1 {
		return Registry{}, fmt.Errorf("unsupported extension registry schema %d", value.SchemaVersion)
	}
	if value.Epoch == 0 {
		value.Epoch = 1
	}
	if value.Extensions == nil {
		value.Extensions = map[string]Entry{}
	}
	type verificationResult struct {
		manifestHash string
		treeHash     string
		err          error
	}
	verifiedPackages := make(map[string]verificationResult)
	for id, entry := range value.Extensions {
		normalizeManifestNames(&entry.Manifest)
		if entry.Manifest.ID != id {
			return Registry{}, fmt.Errorf("registry key %q does not match manifest ID %q", id, entry.Manifest.ID)
		}
		if !validVisibility(entry.Manifest.Visibility) {
			return Registry{}, fmt.Errorf("extension %q has invalid visibility %q", id, entry.Manifest.Visibility)
		}
		if entry.ActivePath == "" || !Within(entry.ActivePath, filepath.Join(root, "extensions")) {
			return Registry{}, fmt.Errorf("extension %q active path escapes the managed package root", id)
		}
		entry.Verified = false
		if !entry.Identity.IsZero() {
			key := strings.ToLower(filepath.Clean(entry.ActivePath))
			result, ok := verifiedPackages[key]
			if !ok {
				result.manifestHash, result.treeHash, result.err = VerifyActivePackage(entry)
				verifiedPackages[key] = result
			}
			if result.err == nil {
				entry.Verified = entry.Identity.ValidateForContent(root, entry, result.manifestHash, result.treeHash) == nil
			}
		}
		if IsReservedBuiltinID(id) && !allowedReservedBuiltinEntry(id, entry) {
			return Registry{}, fmt.Errorf("extension %q uses a reserved built-in ID outside the built-in installer route", id)
		}
		value.Extensions[id] = entry
	}
	return value, nil
}

func MigrateOpenAIServerAlias(value *Registry) bool {
	if value == nil || value.Extensions == nil {
		return false
	}
	legacy, ok := value.Extensions[LegacyOpenAIServerID]
	if !ok {
		return false
	}
	if current, exists := value.Extensions[OpenAIServerID]; exists {
		if legacy.Enabled && !current.Enabled {
			current.Enabled = true
		}
		if current.PreviousActivePath == nil && legacy.ActivePath != "" && legacy.ActivePath != current.ActivePath {
			previous := legacy.ActivePath
			current.PreviousActivePath = &previous
			source := legacy.Source
			current.PreviousSource = &source
		}
		value.Extensions[OpenAIServerID] = current
		delete(value.Extensions, LegacyOpenAIServerID)
		return true
	}
	legacy.Manifest.ID = OpenAIServerID
	if legacy.Manifest.DisplayName == "" || strings.EqualFold(legacy.Manifest.DisplayName, "Copilot OpenAI Bridge") {
		legacy.Manifest.DisplayName = OpenAIServerName
	}
	if legacy.Manifest.Name == "" || strings.EqualFold(legacy.Manifest.Name, "Copilot OpenAI Bridge") {
		legacy.Manifest.Name = OpenAIServerName
	}
	if legacy.Identity.ExtensionID == LegacyOpenAIServerID {
		legacy.Identity.ExtensionID = OpenAIServerID
	}
	value.Extensions[OpenAIServerID] = legacy
	delete(value.Extensions, LegacyOpenAIServerID)
	return true
}

func NormalizeManifest(manifest *Manifest) {
	normalizeManifestNames(manifest)
}

func ValidateManifestUI(manifest Manifest) error {
	if manifest.UI == nil {
		return nil
	}
	if manifest.UI.Protocol != UIProtocol {
		return fmt.Errorf("unsupported UI protocol %q", manifest.UI.Protocol)
	}
	if manifest.UI.Revision != UIRevision {
		return fmt.Errorf("unsupported UI revision %d", manifest.UI.Revision)
	}
	if len(manifest.UI.Surfaces) == 0 {
		return fmt.Errorf("UI manifest must declare at least one surface")
	}
	if len(manifest.UI.Surfaces) > MaxUISurfaces {
		return fmt.Errorf("UI manifest exceeds the %d-surface limit", MaxUISurfaces)
	}
	if !containsCapability(manifest.Capabilities, "modal-canvas") {
		return fmt.Errorf("UI surfaces require the modal-canvas capability")
	}
	seen := make(map[string]struct{}, len(manifest.UI.Surfaces))
	for _, surface := range manifest.UI.Surfaces {
		if !validUISurfaceID.MatchString(surface.ID) {
			return fmt.Errorf("invalid UI surface id %q", surface.ID)
		}
		if surface.Kind != "modal" {
			return fmt.Errorf("unsupported UI surface kind %q", surface.Kind)
		}
		if _, exists := seen[surface.ID]; exists {
			return fmt.Errorf("duplicate UI surface id %q", surface.ID)
		}
		seen[surface.ID] = struct{}{}
	}
	return nil
}

func containsCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func normalizeManifestNames(manifest *Manifest) {
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.DisplayName = strings.TrimSpace(manifest.DisplayName)
	if manifest.DisplayName == "" {
		manifest.DisplayName = manifest.Name
	}
	if manifest.Name == "" {
		manifest.Name = manifest.DisplayName
	}
}

func ValidVisibility(visibility string) bool {
	return validVisibility(visibility)
}

func IsReservedBuiltinID(id string) bool {
	switch id {
	case "black-box", "byo-models", OpenAIServerID, SubagentPolicyID:
		return true
	default:
		return false
	}
}

func allowedReservedBuiltinEntry(id string, entry Entry) bool {
	return entry.Manifest.Visibility == "builtin" && entry.Source.Value == id
}

func IsTrustedBuiltinSourceType(sourceType string) bool {
	return sourceType == "signed-release"
}

func IsTrustedBuiltinEntry(entry Entry) bool {
	return entry.Verified && entry.Manifest.Visibility == "builtin" &&
		entry.Source.Value == entry.Manifest.ID &&
		entry.Identity.IsTrustedBuiltinFor(entry.Manifest.ID) &&
		(IsTrustedBuiltinSourceType(entry.Source.Type) || entry.Source.Type == "builtin")
}

func validVisibility(visibility string) bool {
	switch visibility {
	case "builtin", "public", "private":
		return true
	default:
		return false
	}
}

func VerifyActivePackage(entry Entry) (manifestHash, treeHash string, err error) {
	manifestHash, treeHash, err = hashPackageTree(entry.ActivePath)
	if err != nil {
		return "", "", fmt.Errorf("hash package tree: %w", err)
	}
	if manifestHash == "" {
		return "", "", fmt.Errorf("hash manifest: afterburner.json is missing")
	}
	return "sha256:" + manifestHash, "sha256:" + treeHash, nil
}

func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func HashTree(root string) (string, error) {
	_, treeHash, err := hashPackageTree(root)
	return treeHash, err
}

func hashPackageTree(root string) (manifestHash, treeHash string, err error) {
	hash := sha256.New()
	var manifest []byte
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
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
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		hash.Write([]byte(filepath.ToSlash(relative)))
		hash.Write([]byte{0})
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == "afterburner.json" {
			manifest = data
		}
		hash.Write(data)
		hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if manifest != nil {
		manifestSum := sha256.Sum256(manifest)
		manifestHash = hex.EncodeToString(manifestSum[:])
	}
	return manifestHash, hex.EncodeToString(hash.Sum(nil)), nil
}

func Save(root string, value Registry) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode extension registry: %w", err)
	}
	data = append(data, '\n')
	path := Path(root)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".registry-*.tmp")
	if err != nil {
		return fmt.Errorf("create registry transaction: %w", err)
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
	if err := platform.ReplaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("commit extension registry: %w", err)
	}
	return nil
}

func Within(path, parent string) bool {
	child, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}
