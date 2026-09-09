package extensions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
)

type staticFetchedBuiltin struct {
	value FetchedBuiltin
	err   error
}

func (fetcher staticFetchedBuiltin) FetchBuiltin(string) (FetchedBuiltin, error) {
	return fetcher.value, fetcher.err
}

func TestInstallBuiltinsRejectsIncompleteReleaseProvenance(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "black-box", "one")
	layout := home.Layout{
		Root:        filepath.Join(root, "home"),
		CopilotHome: filepath.Join(root, "home", "copilot-home"),
		Config:      filepath.Join(root, "home", "config"),
		Extensions:  filepath.Join(root, "home", "extensions"),
		Staging:     filepath.Join(root, "home", "staging"),
	}
	manager := Manager{
		Layout: layout,
		Stdout: io.Discard,
		BuiltinFetcher: staticFetchedBuiltin{
			value: FetchedBuiltin{
				Path: source,
				Source: registry.Source{
					Type:    "signed-release",
					Value:   "black-box",
					Version: "v1.0.0",
				},
			},
		},
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil ||
		!strings.Contains(err.Error(), "incomplete signed provenance") {
		t.Fatalf("incomplete provenance error = %v", err)
	}
}

func TestFailedBuiltinTransactionCleansNewPackages(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "black-box")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "black-box", "one")
	layout := home.Layout{
		Root:          filepath.Join(root, "home"),
		CopilotHome:   filepath.Join(root, "home", "copilot-home"),
		Config:        filepath.Join(root, "home", "config"),
		ExtensionData: filepath.Join(root, "home", "extension-data"),
		Extensions:    filepath.Join(root, "home", "extensions"),
		Staging:       filepath.Join(root, "home", "staging"),
	}
	manager := Manager{
		Layout: layout,
		Stdout: io.Discard,
		BuiltinFetcher: fakeBuiltinFetcher{
			paths: map[string]string{"black-box": source},
			errs:  map[string]error{"byo-models": fmt.Errorf("fetch failed")},
		},
	}
	if err := manager.InstallBuiltins([]string{"black-box", "byo-models"}); err == nil {
		t.Fatal("expected multi-package transaction to fail")
	}
	packages, err := os.ReadDir(filepath.Join(layout.Extensions, "black-box"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(packages) != 0 {
		t.Fatalf("failed transaction retained package directories: %#v", packages)
	}
}

func TestBuiltinRollbackRejectsTamperedSignedPackage(t *testing.T) {
	root := t.TempDir()
	oldSource := filepath.Join(root, "old")
	newSource := filepath.Join(root, "new")
	for _, path := range []string{oldSource, newSource} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBuiltinFixture(t, oldSource, "black-box", "old")
	writeBuiltinFixture(t, newSource, "black-box", "new")
	layout := home.Layout{
		Root:          filepath.Join(root, "home"),
		CopilotHome:   filepath.Join(root, "home", "copilot-home"),
		Config:        filepath.Join(root, "home", "config"),
		ExtensionData: filepath.Join(root, "home", "extension-data"),
		Extensions:    filepath.Join(root, "home", "extensions"),
		Staging:       filepath.Join(root, "home", "staging"),
	}
	manager := Manager{
		Layout:         layout,
		Stdout:         io.Discard,
		BuiltinFetcher: fakeBuiltinFetcher{paths: map[string]string{"black-box": oldSource}},
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	manager.BuiltinFetcher = fakeBuiltinFetcher{paths: map[string]string{"black-box": newSource}}
	if err := manager.SyncBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	previous := value.Extensions["black-box"].PreviousPackage
	if previous == nil || !previous.BuiltinSigned {
		t.Fatalf("signed rollback reference = %#v", previous)
	}
	if err := os.WriteFile(filepath.Join(previous.ActivePath, "runtime.mjs"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Rollback("black-box"); err == nil ||
		!strings.Contains(err.Error(), "changed after installation") {
		t.Fatalf("tampered signed rollback error = %v", err)
	}
}
