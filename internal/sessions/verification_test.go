package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestReconcileRemovesUnverifiedManagedSessionExtension(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{
		Root:        root,
		CopilotHome: filepath.Join(root, "copilot-home"),
		Extensions:  filepath.Join(root, "extensions"),
	}
	active := filepath.Join(layout.Extensions, "example", "v1")
	if err := os.MkdirAll(layout.CopilotHome, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"installedPlugins":[{"name":"afterburner-example","cache_path":"` +
		filepath.ToSlash(active) + `","source":{"path":"` + filepath.ToSlash(active) + `"}}]}`
	if err := os.WriteFile(filepath.Join(layout.CopilotHome, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"example": {
			Enabled:    true,
			Verified:   false,
			ActivePath: active,
			Manifest: registry.Manifest{
				ID: "example", SessionExtension: &registry.SessionExtension{Entrypoint: "extension.mjs"},
			},
		},
	}}
	if err := Reconcile(layout, value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(layout.CopilotHome, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if plugins := got["installedPlugins"].([]any); len(plugins) != 0 {
		t.Fatalf("unverified managed plugin was retained: %#v", plugins)
	}
}
