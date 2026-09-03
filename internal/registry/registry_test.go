package registry

import (
	"path/filepath"
	"testing"
)

func TestSaveReplacesExistingRegistry(t *testing.T) {
	root := t.TempDir()
	first := Registry{SchemaVersion: 1, Extensions: map[string]Entry{}}
	if err := Save(root, first); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "extensions", "example", "v1")
	second := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"example": {
			Enabled:    true,
			ActivePath: active,
			Manifest:   Manifest{ID: "example"},
		},
	}}
	if err := Save(root, second); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Extensions["example"].Enabled {
		t.Fatal("replacement registry was not persisted")
	}
}
