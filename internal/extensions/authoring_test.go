package extensions

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAndPackNativeUIExample(t *testing.T) {
	source := filepath.Join("..", "..", "examples", "native-ui-extension")
	manifest, err := ValidatePackage(source)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.UI == nil || len(manifest.UI.Surfaces) != 1 || manifest.UI.Surfaces[0].ID != "settings" {
		t.Fatalf("unexpected UI declaration: %#v", manifest.UI)
	}

	archivePath := filepath.Join(t.TempDir(), "native-ui-example.zip")
	if err := PackPackage(source, archivePath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("archive not created: info=%#v err=%v", info, err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	names := map[string]bool{}
	for _, file := range archive.File {
		names[file.Name] = true
	}
	for _, name := range []string{"afterburner.json", "extension.mjs"} {
		if !names[name] {
			t.Fatalf("archive missing %s: %#v", name, names)
		}
	}
}

func TestPackPackageIncludesRuntimeDependencies(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "node_modules", "payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		  "schemaVersion": 1,
		  "id": "dependency-example",
		  "displayName": "Dependency Example",
		  "visibility": "private",
		  "requires": { "afterburner": ">=0.2.0" },
		  "runtime": { "execution": "in-process", "entrypoint": "node_modules/payload/extension.mjs" }
		}`
	if err := os.WriteFile(filepath.Join(source, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "node_modules", "payload", "extension.mjs"), []byte("export function activate() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "dependency-example.zip")
	if err := PackPackage(source, archivePath); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name == "node_modules/payload/extension.mjs" {
			return
		}
	}
	t.Fatal("archive omitted declared runtime dependency")
}
