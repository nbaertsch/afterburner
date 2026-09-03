package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbaertsch/afterburner/internal/platform"
)

type Manifest struct {
	SchemaVersion    int               `json:"schemaVersion"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Visibility       string            `json:"visibility"`
	Runtime          RuntimeManifest   `json:"runtime"`
	SessionExtension *SessionExtension `json:"sessionExtension,omitempty"`
}

type RuntimeManifest struct {
	Execution  string `json:"execution"`
	Entrypoint string `json:"entrypoint"`
}

type SessionExtension struct {
	Entrypoint string `json:"entrypoint"`
}

type Source struct {
	Type    string  `json:"type"`
	Value   string  `json:"value"`
	Version string  `json:"version,omitempty"`
	Ref     *string `json:"ref,omitempty"`
	Commit  string  `json:"commit,omitempty"`
}

type Entry struct {
	Enabled            bool     `json:"enabled"`
	ActivePath         string   `json:"activePath"`
	PreviousActivePath *string  `json:"previousActivePath"`
	Manifest           Manifest `json:"manifest"`
	Source             Source   `json:"source"`
	UpdatedAt          string   `json:"updatedAt"`
}

type Registry struct {
	SchemaVersion int              `json:"schemaVersion"`
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
	if value.Extensions == nil {
		value.Extensions = map[string]Entry{}
	}
	for id, entry := range value.Extensions {
		if entry.Manifest.ID != id {
			return Registry{}, fmt.Errorf("registry key %q does not match manifest ID %q", id, entry.Manifest.ID)
		}
		if entry.ActivePath == "" || !Within(entry.ActivePath, filepath.Join(root, "extensions")) {
			return Registry{}, fmt.Errorf("extension %q active path escapes the managed package root", id)
		}
	}
	return value, nil
}

func Save(root string, value Registry) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode extension registry: %w", err)
	}
	data = append(data, '\n')
	path := Path(root)
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
