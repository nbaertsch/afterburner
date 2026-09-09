package extensions

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
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

func TestPackInstallUpdateAndRollbackArchive(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExtensionFixture(t, source, "one")
	archivePath := filepath.Join(root, "fixture.zip")
	if err := PackPackage(source, archivePath); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{
		Root:        filepath.Join(root, "home"),
		CopilotHome: filepath.Join(root, "home", "copilot-home"),
		Config:      filepath.Join(root, "home", "config"),
		Extensions:  filepath.Join(root, "home", "extensions"),
		Staging:     filepath.Join(root, "home", "staging"),
	}
	manager := Manager{Layout: layout, Stdout: io.Discard, CoreVersion: "0.2.106"}
	if err := manager.Install(archivePath); err != nil {
		t.Fatal(err)
	}
	first, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	firstEntry := first.Extensions["fixture"]
	if firstEntry.Source.Type != "archive" || firstEntry.Source.Digest == "" ||
		firstEntry.Source.Value != archivePath {
		t.Fatalf("archive source = %#v", firstEntry.Source)
	}
	writeExtensionFixture(t, source, "two")
	if err := PackPackage(source, archivePath); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update("fixture"); err != nil {
		t.Fatal(err)
	}
	updated, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Extensions["fixture"].ActivePath == firstEntry.ActivePath ||
		updated.Extensions["fixture"].Source.Digest == firstEntry.Source.Digest {
		t.Fatalf("archive update did not advance: %#v", updated.Extensions["fixture"])
	}
	if err := manager.Rollback("fixture"); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Extensions["fixture"].ActivePath != firstEntry.ActivePath {
		t.Fatalf("rollback path = %q, want %q", rolledBack.Extensions["fixture"].ActivePath, firstEntry.ActivePath)
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
