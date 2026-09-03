package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAndSelect(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"1.0.83-2", "1.0.83-3"} {
		path := filepath.Join(root, version)
		if err := os.MkdirAll(filepath.Join(path, "prebuilds", runtimePlatform()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "app.js"), []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "prebuilds", runtimePlatform(), "runtime.node"), []byte("runtime-"+version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := Discover(DiscoveryOptions{AdditionalRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectNewestComplete(found)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Version != "1.0.83-3" {
		t.Fatalf("selected %s", selected.Version)
	}
}
