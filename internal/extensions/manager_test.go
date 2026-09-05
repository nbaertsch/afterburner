package extensions

import (
	"encoding/json"
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
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default "+marker), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadManifestAcceptsDisplayNameAndLegacyName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity string
		want     string
	}{
		{name: "displayName", identity: `"displayName": "Display Fixture"`, want: "Display Fixture"},
		{name: "legacy-name", identity: `"name": "Legacy Fixture"`, want: "Legacy Fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := fmt.Sprintf(`{
			  "schemaVersion": 1,
			  "id": "fixture",
			  %s,
			  "visibility": "private",
			  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
			  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
			}`, tc.identity)
			if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if got.DisplayName != tc.want || got.Name != tc.want {
				t.Fatalf("manifest names = (%q, %q), want %q", got.DisplayName, got.Name, tc.want)
			}
		})
	}
}

func TestReadManifestRequiresValidVisibility(t *testing.T) {
	root := t.TempDir()
	manifest := `{
	  "schemaVersion": 1,
	  "id": "fixture",
	  "displayName": "Fixture",
	  "visibility": "internal",
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(root); err == nil {
		t.Fatal("expected invalid visibility to be rejected")
	}
}

func TestGenericInstallRejectsBuiltinVisibilitySpoof(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "spoof", "one")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.Install(source); err == nil {
		t.Fatal("expected local extension declaring builtin visibility to be rejected")
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Extensions) != 0 {
		t.Fatalf("spoofed builtin persisted in registry: %#v", value.Extensions)
	}
}

func TestGenericInstallRejectsReservedBuiltinID(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "schemaVersion": 1,
	  "id": "black-box",
	  "name": "Spoofed Black Box",
	  "visibility": "private",
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(source, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.Install(source); err == nil {
		t.Fatal("expected local extension using reserved black-box ID to be rejected")
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Extensions) != 0 {
		t.Fatalf("reserved ID spoof persisted in registry: %#v", value.Extensions)
	}
}

func TestGitInstallRejectsReservedBuiltinID(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "schemaVersion": 1,
	  "id": "black-box",
	  "name": "Spoofed Black Box",
	  "visibility": "private",
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(source, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.invalid")
	runGit(t, source, "config", "user.name", "Afterburner Test")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-m", "reserved")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.Install("file:///" + filepath.ToSlash(source)); err == nil {
		t.Fatal("expected Git extension using reserved black-box ID to be rejected")
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Extensions) != 0 {
		t.Fatalf("reserved Git ID spoof persisted in registry: %#v", value.Extensions)
	}
}

func TestGitInstallRejectsBuiltinVisibilitySpoof(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, source, "spoof", "one")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "test@example.invalid")
	runGit(t, source, "config", "user.name", "Afterburner Test")
	runGit(t, source, "add", ".")
	runGit(t, source, "commit", "-m", "spoof")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.Install("file:///" + filepath.ToSlash(source)); err == nil {
		t.Fatal("expected Git extension declaring builtin visibility to be rejected")
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Extensions) != 0 {
		t.Fatalf("spoofed Git builtin persisted in registry: %#v", value.Extensions)
	}
}

func TestSchemaAllowsDisplayNameOrLegacyNameAndRequiresVisibility(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "extension-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string              `json:"required"`
		AnyOf    []map[string][]string `json:"anyOf"`
		Props    map[string]any        `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if !containsString(schema.Required, "visibility") || containsString(schema.Required, "displayName") {
		t.Fatalf("schema required fields = %#v", schema.Required)
	}
	if _, ok := schema.Props["name"]; !ok {
		t.Fatal("schema is missing legacy name property")
	}
	if len(schema.AnyOf) != 2 || !containsString(schema.AnyOf[0]["required"], "displayName") || !containsString(schema.AnyOf[1]["required"], "name") {
		t.Fatalf("schema name/displayName compatibility = %#v", schema.AnyOf)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	if firstEntry.Identity.RegistryMAC == "" || secondEntry.Identity.RegistryMAC == "" || firstEntry.Identity.RegistryMAC == secondEntry.Identity.RegistryMAC {
		t.Fatalf("Git update did not rotate registry integrity records: first=%#v second=%#v", firstEntry.Identity, secondEntry.Identity)
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
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
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

func TestInstallBuiltinsRejectsUnverifiedDevelopmentSourceOverride(t *testing.T) {
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
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil {
		t.Fatal("expected unverified development source override to be rejected for built-in trust")
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := value.Extensions["black-box"]; ok {
		t.Fatal("unverified development override was persisted as a built-in")
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
	if entry.Source.Type != "signed-release" || !entry.Enabled || !entry.Identity.BuiltinSigned {
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
	if entry.Source.Type != "embedded" || !entry.Enabled || !entry.Identity.BuiltinSigned {
		t.Fatalf("fallback entry = %#v", entry)
	}
}

func TestUpdateAllReportsBuiltinLockstepWithoutFetcher(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), ExtensionData: filepath.Join(root, "home", "extension-data"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	var out strings.Builder
	manager := Manager{Layout: layout, Stdout: &out}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := manager.UpdateAll(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pinned to the installed Afterburner core release") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSyncBuiltinsRequiresPinnedFetcherAndPreservesLockstep(t *testing.T) {
	root := t.TempDir()
	newSource := filepath.Join(root, "new")
	if err := os.MkdirAll(newSource, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, newSource, "black-box", "new")
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), ExtensionData: filepath.Join(root, "home", "extension-data"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	manager := Manager{Layout: layout, Stdout: io.Discard}
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
	entry := value.Extensions["black-box"]
	content, err := os.ReadFile(filepath.Join(entry.ActivePath, "runtime.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new") || entry.Source.Type != "signed-release" || !entry.Identity.BuiltinSigned {
		t.Fatalf("entry/content = %#v %q", entry, content)
	}
}
