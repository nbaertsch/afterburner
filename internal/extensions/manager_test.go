package extensions

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestBuiltinLifecyclePreservesUserData(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{
		Root:            root,
		CopilotHome:     filepath.Join(root, "copilot-home"),
		Config:          filepath.Join(root, "config"),
		ExtensionData:   filepath.Join(root, "extension-data"),
		Extensions:      filepath.Join(root, "extensions"),
		Staging:         filepath.Join(root, "staging"),
		BYOModelsConfig: filepath.Join(root, "config", "byomodels.json"),
	}
	for _, path := range []string{layout.CopilotHome, layout.Config, layout.ExtensionData, layout.Extensions} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.InstallBuiltins([]string{"byo-models", "black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Extensions["byo-models"].Enabled || !value.Extensions["black-box"].Enabled {
		t.Fatal("built-ins were not enabled")
	}
	configData := []byte(`{"userOwned":true}`)
	if err := os.WriteFile(layout.BYOModelsConfig, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	durable := filepath.Join(layout.ExtensionData, "black-box", "state.json")
	if err := os.MkdirAll(filepath.Dir(durable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(durable, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetBuiltinsEnabled([]string{"black-box"}, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.UninstallBuiltins([]string{"byo-models", "black-box"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(layout.BYOModelsConfig); err != nil || string(got) != string(configData) {
		t.Fatalf("BYOModels config changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(durable); err != nil || string(got) != "durable" {
		t.Fatalf("Black Box data changed: %q, %v", got, err)
	}
}

func TestLocalUpdateAndRollbackPreserveEnablement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	layout := home.Layout{
		Root:        filepath.Join(root, "home"),
		CopilotHome: filepath.Join(root, "home", "copilot-home"),
		Config:      filepath.Join(root, "home", "config"),
		Extensions:  filepath.Join(root, "home", "extensions"),
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExtensionFixture(t, source, "one")
	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.Install(source); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled("fixture", true); err != nil {
		t.Fatal(err)
	}
	writeExtensionFixture(t, source, "two")
	if err := manager.Update("fixture"); err != nil {
		t.Fatal(err)
	}
	updated, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := updated.Extensions["fixture"]
	if !entry.Enabled || entry.PreviousActivePath == nil {
		t.Fatalf("updated entry = %#v", entry)
	}
	updatedPath := entry.ActivePath
	if err := manager.Rollback("fixture"); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry = rolledBack.Extensions["fixture"]
	if !entry.Enabled || entry.ActivePath == updatedPath {
		t.Fatalf("rolled-back entry = %#v", entry)
	}
}

func writeExtensionFixture(t *testing.T, root, marker string) {
	t.Helper()
	manifest := `{
	  "schemaVersion": 1,
	  "id": "fixture",
	  "name": "Fixture",
	  "visibility": "private",
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default "+marker), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitInstallAndUpdateUseImmutableCommits(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExtensionFixture(t, source, "one")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.invalid")
	runGit(t, source, "config", "user.name", "Afterburner Test")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-m", "first")
	layout := home.Layout{
		Root:        filepath.Join(root, "home"),
		CopilotHome: filepath.Join(root, "home", "copilot-home"),
		Config:      filepath.Join(root, "home", "config"),
		Extensions:  filepath.Join(root, "home", "extensions"),
		Staging:     filepath.Join(root, "home", "staging"),
	}
	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.Install("file:///" + filepath.ToSlash(source)); err != nil {
		t.Fatal(err)
	}
	first, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	firstEntry := first.Extensions["fixture"]
	if firstEntry.Source.Type != "git" || firstEntry.Source.Commit == "" {
		t.Fatalf("Git source metadata = %#v", firstEntry.Source)
	}
	writeExtensionFixture(t, source, "two")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-m", "second")
	if err := manager.Update("fixture"); err != nil {
		t.Fatal(err)
	}
	second, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	secondEntry := second.Extensions["fixture"]
	if secondEntry.Source.Commit == firstEntry.Source.Commit || secondEntry.ActivePath == firstEntry.ActivePath {
		t.Fatalf("Git update did not advance: %#v", secondEntry)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
