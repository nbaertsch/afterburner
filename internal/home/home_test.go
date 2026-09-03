package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedHomesDoNotInheritNormalMCPConfiguration(t *testing.T) {
	root := t.TempDir()
	normal := filepath.Join(root, "normal")
	managed := filepath.Join(root, "managed")
	if err := os.MkdirAll(normal, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(normal, "settings.json"), []byte(`{"theme":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mcp := []byte(`{"mcpServers":{"untrusted":{"command":"untrusted.exe"}}}`)
	if err := os.WriteFile(filepath.Join(normal, "mcp-config.json"), mcp, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFTERBURNER_HOME", managed)
	t.Setenv("AFTERBURNER_NORMAL_COPILOT_HOME", normal)
	layout, err := Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.CopilotHome, "settings.json")); err != nil {
		t.Fatal("ordinary settings were not inherited")
	}
	if _, err := os.Stat(filepath.Join(layout.CopilotHome, "mcp-config.json")); !os.IsNotExist(err) {
		t.Fatalf("normal MCP configuration was inherited: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.CopilotHome, "mcp-config.json"), mcp, 0o600); err != nil {
		t.Fatal(err)
	}
	layout, err = Initialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.CopilotHome, "mcp-config.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy auto-imported MCP configuration was not removed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.CopilotHome, "mcp-config.json"), []byte(`{"mcpServers":{"explicit":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	launchHome, cleanup, err := CreateLaunchHome(layout)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(launchHome.CopilotHome, "mcp-config.json")); !os.IsNotExist(err) {
		t.Fatalf("safe-mode home inherited managed MCP configuration: %v", err)
	}
}
