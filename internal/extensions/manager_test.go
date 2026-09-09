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
	"time"

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

	manager := Manager{
		Layout: layout, Stdout: os.Stdout,
		BuiltinFetcher: repositoryBuiltinFetcher("byo-models", "black-box"),
	}
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

func TestRollbackRejectsTamperedPreviousPackage(t *testing.T) {
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
	manager := Manager{Layout: layout, Stdout: io.Discard}
	if err := manager.Install(source); err != nil {
		t.Fatal(err)
	}
	writeExtensionFixture(t, source, "two")
	if err := manager.Update("fixture"); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	previous := value.Extensions["fixture"].PreviousPackage
	if previous == nil {
		t.Fatal("update did not retain a verified rollback package")
	}
	if err := os.WriteFile(filepath.Join(previous.ActivePath, "runtime.mjs"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Rollback("fixture"); err == nil ||
		!strings.Contains(err.Error(), "changed after installation") {
		t.Fatalf("tampered rollback error = %v", err)
	}
}

func TestCompatibilityEnforcedOnInstallAndEnable(t *testing.T) {
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
	incompatible := Manager{Layout: layout, Stdout: io.Discard, CoreVersion: "1.0.0"}
	if err := incompatible.Install(source); err == nil || !strings.Contains(err.Error(), "Afterburner") {
		t.Fatalf("incompatible install error = %v", err)
	}
	compatible := Manager{Layout: layout, Stdout: io.Discard, CoreVersion: "0.2.106"}
	if err := compatible.Install(source); err != nil {
		t.Fatal(err)
	}
	incompatible.CopilotVersion = "1.0.0"
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry := value.Extensions["fixture"]
	entry.Manifest.Requires.CopilotCLI = []string{">=2.0.0"}
	entry, err = registry.SealEntry(layout.Root, entry)
	if err != nil {
		t.Fatal(err)
	}
	value.Extensions["fixture"] = entry
	if err := registry.Save(layout.Root, value); err != nil {
		t.Fatal(err)
	}
	incompatible.CoreVersion = "0.2.106"
	if err := incompatible.SetEnabled("fixture", true); err == nil ||
		!strings.Contains(err.Error(), "Copilot CLI") {
		t.Fatalf("incompatible enable error = %v", err)
	}
}

func TestPrunePackageCachePreservesActiveAndRollbackPackages(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{Extensions: filepath.Join(root, "extensions")}
	extensionRoot := filepath.Join(layout.Extensions, "fixture")
	active := filepath.Join(extensionRoot, "active")
	previous := filepath.Join(extensionRoot, "previous")
	orphan := filepath.Join(extensionRoot, "orphan")
	for _, path := range []string{active, previous, orphan} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{active, previous, orphan} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	value := registry.Registry{Extensions: map[string]registry.Entry{
		"fixture": {
			ActivePath: active,
			PreviousPackage: &registry.PackageReference{
				ActivePath: previous,
			},
		},
	}}
	if err := prunePackageCache(layout, value, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active, previous} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained package %s: %v", path, err)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan package still exists: %v", err)
	}
}

func TestReinstallRejectsTamperedManagedPackage(t *testing.T) {
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
	writeExtensionFixture(t, source, "original")
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
		BuiltinFetcher: repositoryBuiltinFetcher(registry.OpenAIServerID),
	}
	if err := manager.Install(source); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	active := value.Extensions["fixture"].ActivePath
	if err := os.WriteFile(filepath.Join(active, "runtime.mjs"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = manager.Install(source)
	if err == nil || !strings.Contains(err.Error(), "changed after installation") {
		t.Fatalf("tampered reinstall error = %v", err)
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

func TestReadManifestValidatesNativeUISurfaces(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runtime.mjs"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(ui string) {
		t.Helper()
		manifest := fmt.Sprintf(`{
		  "schemaVersion": 1,
		  "id": "fixture",
		  "displayName": "Fixture",
		  "visibility": "private",
		  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
		  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"},
		  "capabilities": ["modal-canvas"],
		  "ui": %s
		}`, ui)
		if err := os.WriteFile(filepath.Join(root, "afterburner.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"settings","kind":"modal"},{"id":"details","kind":"modal"}]}`)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.UI == nil || len(manifest.UI.Surfaces) != 2 {
		t.Fatalf("parsed UI declaration = %#v", manifest.UI)
	}
	for name, declaration := range map[string]string{
		"duplicate":       `{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"settings","kind":"modal"},{"id":"settings","kind":"modal"}]}`,
		"unknown-kind":    `{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"settings","kind":"panel"}]}`,
		"future-revision": `{"protocol":"afterburner.ui","revision":2,"surfaces":[{"id":"settings","kind":"modal"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			write(declaration)
			if _, err := readManifest(root); err == nil {
				t.Fatal("expected invalid UI declaration to be rejected")
			}
		})
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
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
		BuiltinFetcher: repositoryBuiltinFetcher("black-box"),
	}
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

func TestInstallBuiltinsRequiresSignedReleaseSource(t *testing.T) {
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
	manager := Manager{Layout: layout, Stdout: os.Stdout}
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil ||
		!strings.Contains(err.Error(), "requires a signed release source") {
		t.Fatalf("missing source error = %v", err)
	}
}

func TestInstallBuiltinsRejectsMismatchedFetchedManifest(t *testing.T) {
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
	manager := Manager{
		Layout: layout, Stdout: os.Stdout,
		BuiltinFetcher: fakeBuiltinFetcher{paths: map[string]string{"black-box": source}},
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil {
		t.Fatal("expected an error for a mismatched fetched manifest identity")
	}
}

// fakeBuiltinFetcher is a test double for BuiltinFetcher that either returns
// a fixed directory or a fixed error per built-in ID.
type fakeBuiltinFetcher struct {
	paths map[string]string
	errs  map[string]error
}

func (fetcher fakeBuiltinFetcher) FetchBuiltin(id string) (FetchedBuiltin, error) {
	if err, ok := fetcher.errs[id]; ok {
		return FetchedBuiltin{}, err
	}
	return FetchedBuiltin{
		Path: fetcher.paths[id],
		Source: registry.Source{
			Type:              "signed-release",
			Value:             id,
			Version:           "v1.0.0",
			Commit:            strings.Repeat("a", 40),
			Digest:            "sha256:" + strings.Repeat("b", 64),
			ManifestDigest:    "sha256:" + strings.Repeat("c", 64),
			SignerFingerprint: "sha256:" + strings.Repeat("d", 64),
		},
		Cleanup: func() {},
	}, nil
}

func repositoryBuiltinFetcher(ids ...string) fakeBuiltinFetcher {
	directories := map[string]string{
		"black-box":     "BlackBox",
		"byo-models":    "BYOModels",
		"openai-server": "OpenAIServer",
	}
	paths := make(map[string]string, len(ids))
	for _, id := range ids {
		paths[id] = filepath.Join("..", "..", "extensions", directories[id])
	}
	return fakeBuiltinFetcher{paths: paths}
}

func TestInstallBuiltinsUsesSignedReleaseFetcher(t *testing.T) {
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
		t.Fatalf("installed content = %q, expected fetched source", content)
	}
}

func TestInstallBuiltinsFailsClosedWhenFetcherFails(t *testing.T) {
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
	if err := manager.InstallBuiltins([]string{"black-box"}); err == nil ||
		!strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("fetch failure = %v", err)
	}
}

func TestUpdateAllReportsBuiltinLockstepWithoutFetcher(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{Root: filepath.Join(root, "home"), CopilotHome: filepath.Join(root, "home", "copilot-home"), Config: filepath.Join(root, "home", "config"), ExtensionData: filepath.Join(root, "home", "extension-data"), Extensions: filepath.Join(root, "home", "extensions"), Staging: filepath.Join(root, "home", "staging")}
	var out strings.Builder
	manager := Manager{
		Layout: layout, Stdout: &out,
		BuiltinFetcher: repositoryBuiltinFetcher("black-box"),
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	manager.BuiltinFetcher = nil
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
	oldSource := filepath.Join(root, "old")
	if err := os.MkdirAll(oldSource, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBuiltinFixture(t, oldSource, "black-box", "old")
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
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
	entry := value.Extensions["black-box"]
	content, err := os.ReadFile(filepath.Join(entry.ActivePath, "runtime.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new") || entry.Source.Type != "signed-release" || !entry.Identity.BuiltinSigned {
		t.Fatalf("entry/content = %#v %q", entry, content)
	}
}

func TestOpenAIServerBuiltinInstallsDisabledByDefaultAndMigratesLegacy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "openai-source")
	legacyActive := filepath.Join(root, "home", "extensions", "copilot-openai", "old")
	for _, path := range []string{source, legacyActive} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBuiltinFixture(t, source, registry.OpenAIServerID, "openai")
	legacyManifest := `{
	  "schemaVersion": 1,
	  "id": "copilot-openai",
	  "displayName": "Copilot OpenAI Bridge",
	  "visibility": "private",
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(legacyActive, "afterburner.json"), []byte(legacyManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyActive, "runtime.mjs"), []byte("export default 'legacy'"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{
		Root:          filepath.Join(root, "home"),
		CopilotHome:   filepath.Join(root, "home", "copilot-home"),
		Config:        filepath.Join(root, "home", "config"),
		ExtensionData: filepath.Join(root, "home", "extension-data"),
		Extensions:    filepath.Join(root, "home", "extensions"),
		Staging:       filepath.Join(root, "home", "staging"),
	}
	manager := Manager{Layout: layout, Stdout: io.Discard, BuiltinFetcher: fakeBuiltinFetcher{paths: map[string]string{registry.OpenAIServerID: source}}}
	if err := manager.InstallBuiltins([]string{registry.OpenAIServerID}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if value.Extensions[registry.OpenAIServerID].Enabled {
		t.Fatal("openai-server should install disabled by default")
	}

	legacyManifestValue, err := readManifestWithCanonicalID(legacyActive, false)
	if err != nil {
		t.Fatal(err)
	}
	legacyManifestHash, legacyTreeHash, err := registry.VerifyActivePackage(registry.Entry{ActivePath: legacyActive})
	if err != nil {
		t.Fatal(err)
	}
	legacySource := registry.Source{Type: "path", Value: legacyActive, Version: "legacy"}
	legacy := registry.Entry{
		Enabled:    true,
		ActivePath: legacyActive,
		Manifest:   legacyManifestValue,
		Source:     legacySource,
		UpdatedAt:  "2026-01-01T00:00:00Z",
	}
	legacy.Identity = identityBinding(legacy.Manifest, legacy.Source,
		strings.TrimPrefix(legacyManifestHash, "sha256:"),
		strings.TrimPrefix(legacyTreeHash, "sha256:"), 1, legacy.UpdatedAt, false)
	legacy, err = registry.SealEntry(layout.Root, legacy)
	if err != nil {
		t.Fatal(err)
	}
	delete(value.Extensions, registry.OpenAIServerID)
	value.Extensions[registry.LegacyOpenAIServerID] = legacy
	if err := registry.Save(layout.Root, value); err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallBuiltins([]string{registry.OpenAIServerID}); err != nil {
		t.Fatal(err)
	}
	migrated, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := migrated.Extensions[registry.LegacyOpenAIServerID]; exists {
		t.Fatal("legacy openai-server entry was retained")
	}
	entry := migrated.Extensions[registry.OpenAIServerID]
	if !entry.Enabled || entry.PreviousActivePath == nil || *entry.PreviousActivePath != legacyActive || entry.Manifest.Visibility != "builtin" {
		t.Fatalf("migrated openai-server entry = %#v", entry)
	}
	if err := manager.Rollback(registry.OpenAIServerID); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := rolledBack.Extensions[registry.OpenAIServerID]; exists {
		t.Fatal("canonical entry remained after rollback to the legacy package")
	}
	legacyEntry, exists := rolledBack.Extensions[registry.LegacyOpenAIServerID]
	if !exists || !legacyEntry.Enabled || legacyEntry.ActivePath != legacyActive || legacyEntry.Manifest.ID != registry.LegacyOpenAIServerID {
		t.Fatalf("legacy rollback entry = %#v", legacyEntry)
	}
	if err := manager.SetBuiltinsEnabled([]string{registry.OpenAIServerID}, false); err != nil {
		t.Fatal(err)
	}
	disabled, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Extensions[registry.LegacyOpenAIServerID].Enabled {
		t.Fatal("canonical management ID did not disable the rolled-back legacy entry")
	}
	if err := manager.Rollback(registry.OpenAIServerID); err != nil {
		t.Fatal(err)
	}
	restoredCanonical, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalEntry, exists := restoredCanonical.Extensions[registry.OpenAIServerID]
	if !exists || canonicalEntry.Manifest.Visibility != "builtin" || !canonicalEntry.Identity.BuiltinSigned ||
		!registry.IsTrustedBuiltinSourceType(canonicalEntry.Source.Type) || canonicalEntry.Source.Value != registry.OpenAIServerID {
		t.Fatalf("canonical built-in was not restored with trusted source metadata: %#v", canonicalEntry)
	}
	if err := manager.Rollback(registry.OpenAIServerID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyActive, "runtime.mjs"), []byte("export default 'legacy-updated'"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(registry.OpenAIServerID); err != nil {
		t.Fatal(err)
	}
	updatedLegacy, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if updatedLegacy.Extensions[registry.LegacyOpenAIServerID].ActivePath == legacyActive {
		t.Fatal("canonical update ID did not update the rolled-back legacy entry")
	}
}

func TestOpenAIServerBuiltinManagementCollapsesDuplicateAliases(t *testing.T) {
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
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
		BuiltinFetcher: repositoryBuiltinFetcher(registry.OpenAIServerID),
	}
	if err := manager.InstallBuiltins([]string{registry.OpenAIServerID}); err != nil {
		t.Fatal(err)
	}
	value, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(layout.Extensions, registry.LegacyOpenAIServerID, "legacy")
	if err := os.MkdirAll(legacyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	value.Extensions[registry.LegacyOpenAIServerID] = registry.Entry{
		Enabled:    true,
		ActivePath: legacyPath,
		Manifest: registry.Manifest{
			ID: registry.LegacyOpenAIServerID, Visibility: "private",
		},
		Source: registry.Source{Type: "path", Value: legacyPath},
	}
	if err := registry.Save(layout.Root, value); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetBuiltinsEnabled([]string{registry.OpenAIServerID}, false); err != nil {
		t.Fatal(err)
	}
	value, err = registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := value.Extensions[registry.LegacyOpenAIServerID]; exists {
		t.Fatal("legacy alias remained after canonical management")
	}
	if value.Extensions[registry.OpenAIServerID].Enabled {
		t.Fatal("canonical extension remained enabled")
	}

	value.Extensions[registry.LegacyOpenAIServerID] = registry.Entry{
		Enabled:    true,
		ActivePath: legacyPath,
		Manifest: registry.Manifest{
			ID: registry.LegacyOpenAIServerID, Visibility: "private",
		},
		Source: registry.Source{Type: "path", Value: legacyPath},
	}
	if err := registry.Save(layout.Root, value); err != nil {
		t.Fatal(err)
	}
	if err := manager.UninstallBuiltins([]string{registry.OpenAIServerID}); err != nil {
		t.Fatal(err)
	}
	value, err = registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, canonical := value.Extensions[registry.OpenAIServerID]; canonical {
		t.Fatal("canonical alias remained after uninstall")
	}
	if _, legacy := value.Extensions[registry.LegacyOpenAIServerID]; legacy {
		t.Fatal("legacy alias remained after uninstall")
	}
}

func TestOpenAIServerReservedIDCannotBeClaimedByNamedLocalPath(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "CopilotOpenAI-spoof")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "schemaVersion": 1,
	  "id": "openai-server",
	  "displayName": "Spoof",
	  "visibility": "private",
	  "requires": {"afterburner": ">=0.1.0 <1.0.0"},
	  "runtime": {"execution": "in-process", "entrypoint": "runtime.mjs"}
	}`
	if err := os.WriteFile(filepath.Join(source, "afterburner.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime.mjs"), []byte("export default 'spoof'"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{
		Root:          filepath.Join(root, "home"),
		CopilotHome:   filepath.Join(root, "home", "copilot-home"),
		Config:        filepath.Join(root, "home", "config"),
		ExtensionData: filepath.Join(root, "home", "extension-data"),
		Extensions:    filepath.Join(root, "home", "extensions"),
		Staging:       filepath.Join(root, "home", "staging"),
	}
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
		BuiltinFetcher: repositoryBuiltinFetcher("black-box"),
	}
	if err := manager.Install(source); err == nil {
		t.Fatal("expected local path to be rejected for the reserved openai-server ID")
	}
}

func TestInstallingOtherBuiltinDoesNotMigrateLegacyOpenAIServer(t *testing.T) {
	root := t.TempDir()
	layout := home.Layout{
		Root:          filepath.Join(root, "home"),
		CopilotHome:   filepath.Join(root, "home", "copilot-home"),
		Config:        filepath.Join(root, "home", "config"),
		ExtensionData: filepath.Join(root, "home", "extension-data"),
		Extensions:    filepath.Join(root, "home", "extensions"),
		Staging:       filepath.Join(root, "home", "staging"),
	}
	legacyActive := filepath.Join(layout.Extensions, registry.LegacyOpenAIServerID, "old")
	if err := os.MkdirAll(legacyActive, 0o755); err != nil {
		t.Fatal(err)
	}
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		registry.LegacyOpenAIServerID: {
			Enabled:    true,
			ActivePath: legacyActive,
			Manifest: registry.Manifest{
				SchemaVersion: 1,
				ID:            registry.LegacyOpenAIServerID,
				DisplayName:   "Copilot OpenAI Bridge",
				Visibility:    "private",
			},
			Source: registry.Source{Type: "path", Value: legacyActive},
		},
	}}
	if err := registry.Save(layout.Root, value); err != nil {
		t.Fatal(err)
	}
	manager := Manager{
		Layout: layout, Stdout: io.Discard,
		BuiltinFetcher: repositoryBuiltinFetcher("black-box"),
	}
	if err := manager.InstallBuiltins([]string{"black-box"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(layout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := loaded.Extensions[registry.LegacyOpenAIServerID]; !exists {
		t.Fatalf("unrelated built-in install migrated legacy entry: %#v", loaded.Extensions)
	}
	if _, exists := loaded.Extensions[registry.OpenAIServerID]; exists {
		t.Fatalf("unrelated built-in install created canonical entry: %#v", loaded.Extensions)
	}
}
