package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestReconcilePreservesOrdinaryPluginsAndRemovesStaleManaged(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{
		Root:        root,
		CopilotHome: filepath.Join(root, "copilot-home"),
		Extensions:  filepath.Join(root, "extensions"),
	}
	active := filepath.Join(layout.Extensions, "example", "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "plugin.json"), []byte(`{"name":"afterburner-example","version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.CopilotHome, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "// User settings belong in settings.json.\n// This file is managed automatically.\n" + `{"otherSetting":true,"installedPlugins":[
		{"name":"ordinary","cache_path":"C:\\ordinary","source":{"path":"C:\\ordinary"}},
		{"name":"stale","cache_path":"` + filepath.ToSlash(filepath.Join(layout.Extensions, "stale")) + `","source":{"path":"` + filepath.ToSlash(filepath.Join(layout.Extensions, "stale")) + `"}}
	]}`
	if err := os.WriteFile(filepath.Join(layout.CopilotHome, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"example": {
			Enabled:    true,
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
	body, _, err := splitConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	plugins := got["installedPlugins"].([]any)
	if len(plugins) != 2 {
		t.Fatalf("plugins = %#v", plugins)
	}
	if got["otherSetting"] != true {
		t.Fatal("unrelated configuration was not preserved")
	}
	if string(data[:2]) != "//" {
		t.Fatal("managed JSONC header was not preserved")
	}
}
