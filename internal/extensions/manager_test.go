package extensions

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func writeBuiltinFixture(t *testing.T, root, id, marker string) {
	t.Helper()
	manifest := fmt.Sprintf(`{
	  "schemaVersion": 1,
	  "id": %q,
	  "name": "Fixture Builtin",
	  "visibility": "builtin",
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`, id)
	if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default "+marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if id == "black-box" {
		if err := os.WriteFile(filepath.Join(root, "config.example.json"), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallBuiltinsUsesDevelopmentSourceOverride(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "override-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "black-box", "one")
	layout := home.Layout{
		Root:            filepath.Join(root, "home"),
		CopilotHome:     filepath.Join(root, "home", "copilot-home"),
		Config:          filepath.Join(root, "home", "config"),
		ExtensionData:   filepath.Join(root, "home", "extension-data"),
		Extensions:      filepath.Join(root, "home", "extensions"),
		Staging:         filepath.Join(root, "home", "staging"),
		BYOModelsConfig: filepath.Join(root, "home", "config", "byomodels.json"),
	}
	for _, path := range []string{layout.CopilotHome, layout.Config, layout.ExtensionData, layout.Extensions} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "black-box="+source)
	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.Extensions["black-box"]
	if entry.Source.Type != "builtin" || !entry.Enabled {
		t.Fatalf("override entry = %#v", entry)
	}
	first := entry.ActivePath

	writeBuiltinFixture(t, source, "black-box", "two")
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err = registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry = value.Extensions["black-box"]
	if entry.ActivePath == first {
		t.Fatalf("override reinstall did not pick up the modified source: %#v", entry)
	}
}

func TestInstallBuiltinsRejectsMismatchedSourceOverride(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "override-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "not-black-box", "one")
	layout := home.Layout{
		Root:        filepath.Join(root, "home"),
		CopilotHome: filepath.Join(root, "home", "copilot-home"),
		Config:      filepath.Join(root, "home", "config"),
		Extensions:  filepath.Join(root, "home", "extensions"),
		Staging:     filepath.Join(root, "home", "staging"),
	}
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "black-box="+source)
	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil {
		t.Fatal("expected an error for a mismatched override manifest identity")
	}
}

// fakeBuiltinFetcher is a test double for BuiltinFetcher that either returns
// a fixed directory or a fixed error per built-in ID.
type fakeBuiltinFetcher struct {
	paths map[string]string
	errs  map[string]error
}

func (fetcher fakeBuiltinFetcher) FetchBuiltin(id string) (string, func(), error) {
	if err, ok := fetcher.errs[id]; ok {
		return "", nil, err
	}
	return fetcher.paths[id], func() {}, nil
}

func TestInstallBuiltinsPrefersFetcherOverEmbedded(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "fetched-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "black-box", "fetched")
	layout := home.Layout{
		Root:            filepath.Join(root, "home"),
		CopilotHome:     filepath.Join(root, "home", "copilot-home"),
		Config:          filepath.Join(root, "home", "config"),
		ExtensionData:   filepath.Join(root, "home", "extension-data"),
		Extensions:      filepath.Join(root, "home", "extensions"),
		Staging:         filepath.Join(root, "home", "staging"),
		BYOModelsConfig: filepath.Join(root, "home", "config", "byomodels.json"),
	}
	for _, path := range []string{layout.CopilotHome, layout.Config, layout.ExtensionData, layout.Extensions} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := Manager{
		Layout: layout, Stdout: os.Stdout,
		BuiltinFetcher: fakeBuiltinFetcher{paths: map[string]string{"black-box": source}},
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.Extensions["black-box"]
	if entry.Source.Type != "builtin" || !entry.Enabled {
		t.Fatalf("fetched entry = %#v", entry)
	}
	content, err := os.ReadFile(filepath.Join(entry.ActivePath, "runtime.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "fetched") {
		t.Fatalf("installed content = %q, expected fetched source to win over embedded", content)
	}
}

func TestInstallBuiltinsFallsBackToEmbeddedWhenFetcherFails(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{
		Root:            filepath.Join(root, "home"),
		CopilotHome:     filepath.Join(root, "home", "copilot-home"),
		Config:          filepath.Join(root, "home", "config"),
		ExtensionData:   filepath.Join(root, "home", "extension-data"),
		Extensions:      filepath.Join(root, "home", "extensions"),
		Staging:         filepath.Join(root, "home", "staging"),
		BYOModelsConfig: filepath.Join(root, "home", "config", "byomodels.json"),
	}
	for _, path := range []string{layout.CopilotHome, layout.Config, layout.ExtensionData, layout.Extensions} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := Manager{
		Layout: layout, Stdout: os.Stdout,
		BuiltinFetcher: fakeBuiltinFetcher{errs: map[string]error{"black-box": fmt.Errorf("network unavailable")}},
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatalf("expected fallback to the embedded built-in, got error: %v", err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.Extensions["black-box"]
	if entry.Source.Type != "builtin" || !entry.Enabled {
		t.Fatalf("fallback entry = %#v", entry)
	}
}

func TestUpdateAllReportsBuiltinLockstepWithoutFetcher(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "builtin-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "black-box", "one")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), ExtensionData: filepath.Join(root, "home", "extension-data"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "black-box="+source)
	var out strings.Builder
	manager := Manager{Layout: layout, Stdout: &out}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "")
	if err := manager.UpdateAll(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pinned to the installed Afterburner core release") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSyncBuiltinsRequiresPinnedFetcherAndPreservesLockstep(t *testing.T) {
	root := t.TempDir()
	oldSource := filepath.Join(root, "old")
	newSource := filepath.Join(root, "new")
	if err := os.MkdirAll(oldSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newSource, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, oldSource, "black-box", "old")
	writeBuiltinFixture(t, newSource, "black-box", "new")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), ExtensionData: filepath.Join(root, "home", "extension-data"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "black-box="+oldSource)
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE", "")
	manager.BuiltinFetcher = fakeBuiltinFetcher{paths: map[string]string{"black-box": newSource}}
	if err := manager.SyncBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.Extensions["black-box"]
	content, err := os.ReadFile(filepath.Join(entry.ActivePath, "runtime.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new") || entry.Source.Type != "builtin" {
		t.Fatalf("entry/content = %#v %q", entry, content)
	}
}
