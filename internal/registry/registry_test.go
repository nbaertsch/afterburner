package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signedReleaseSource(id string) Source {
	return Source{
		Type:              "signed-release",
		Value:             id,
		Version:           "v1.0.0",
		Commit:            strings.Repeat("a", 40),
		Digest:            "sha256:" + strings.Repeat("b", 64),
		ManifestDigest:    "sha256:" + strings.Repeat("c", 64),
		SignerFingerprint: "sha256:" + strings.Repeat("d", 64),
	}
}

func TestSaveDoesNotRewriteUnchangedRegistry(t *testing.T) {
	root := t.TempDir()
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("unchanged registry was rewritten")
	}
}

func TestHashTreeIncludesExecutableDependencyDirectories(t *testing.T) {
	root := t.TempDir()
	dependency := filepath.Join(root, "node_modules", "payload", "index.mjs")
	if err := os.MkdirAll(filepath.Dir(dependency), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("export const value = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := HashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("export const value = 2;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := HashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("node_modules modification did not change package tree hash")
	}
}

func TestSaveReplacesExistingRegistry(t *testing.T) {
	root := t.TempDir()
	first := Registry{SchemaVersion: 1, Extensions: map[string]Entry{}}
	if err := Save(root, first); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "extensions", "example", "v1")
	second := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"example": {
			Enabled:    true,
			ActivePath: active,
			Manifest:   Manifest{ID: "example", DisplayName: "Example", Visibility: "private"},
		},
	}}
	if err := Save(root, second); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Extensions["example"].Enabled {
		t.Fatal("replacement registry was not persisted")
	}
}

func TestLoadNormalizesLegacyManifestName(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "example", "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"example": {
			Enabled:    true,
			ActivePath: active,
			Manifest: Manifest{
				ID:         "example",
				Name:       "Legacy Example",
				Visibility: "public",
			},
		},
	}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := got.Extensions["example"].Manifest
	if manifest.DisplayName != "Legacy Example" || manifest.Name != "Legacy Example" {
		t.Fatalf("manifest names were not normalized: %#v", manifest)
	}
}

func TestMigrateOpenAIServerAlias(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", LegacyOpenAIServerID, "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		LegacyOpenAIServerID: {
			Enabled:    true,
			ActivePath: active,
			Manifest: Manifest{
				ID:          LegacyOpenAIServerID,
				DisplayName: "Copilot OpenAI Bridge",
				Visibility:  "private",
			},
			Source:   Source{Type: "path", Value: active},
			Identity: IdentityBinding{ExtensionID: LegacyOpenAIServerID},
		},
	}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got.Extensions[LegacyOpenAIServerID]; !exists {
		t.Fatal("load should preserve the legacy identity until an explicit migration transaction")
	}
	if !MigrateOpenAIServerAlias(&got) {
		t.Fatal("legacy openai server registry entry was not migrated")
	}
	if _, exists := got.Extensions[LegacyOpenAIServerID]; exists {
		t.Fatal("legacy openai server registry key was retained")
	}
	entry, exists := got.Extensions[OpenAIServerID]
	if !exists || !entry.Enabled || entry.Manifest.ID != OpenAIServerID || entry.Manifest.DisplayName != OpenAIServerName || entry.Identity.ExtensionID != OpenAIServerID {
		t.Fatalf("migrated entry = %#v", entry)
	}
}

func TestLoadMergesDuplicateLegacyOpenAIServerAlias(t *testing.T) {
	root := t.TempDir()
	legacyActive := filepath.Join(root, "extensions", LegacyOpenAIServerID, "v1")
	currentActive := filepath.Join(root, "extensions", OpenAIServerID, "v1")
	for _, path := range []string{legacyActive, currentActive} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		LegacyOpenAIServerID: {Enabled: true, ActivePath: legacyActive, Manifest: Manifest{ID: LegacyOpenAIServerID, DisplayName: "Copilot OpenAI Bridge", Visibility: "private"}, Source: Source{Type: "path", Value: legacyActive}},
		OpenAIServerID: {Enabled: false, ActivePath: currentActive, Manifest: Manifest{ID: OpenAIServerID, DisplayName: OpenAIServerName, Visibility: "builtin"}, Source: Source{
			Type:              "signed-release",
			Value:             OpenAIServerID,
			Version:           "v1.0.0",
			Commit:            strings.Repeat("a", 40),
			Digest:            "sha256:" + strings.Repeat("b", 64),
			ManifestDigest:    "sha256:" + strings.Repeat("c", 64),
			SignerFingerprint: "sha256:" + strings.Repeat("d", 64),
		}},
	}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	MigrateOpenAIServerAlias(&got)
	if len(got.Extensions) != 1 || !got.Extensions[OpenAIServerID].Enabled || got.Extensions[OpenAIServerID].PreviousActivePath == nil {
		t.Fatalf("duplicate alias was not merged: %#v", got.Extensions)
	}
}

func TestLoadRejectsInvalidVisibility(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "example", "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"example": {
			Enabled:    true,
			ActivePath: active,
			Manifest:   Manifest{ID: "example", DisplayName: "Example", Visibility: "internal"},
		},
	}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("expected invalid visibility to be rejected")
	}
}

func TestValidateManifestUI(t *testing.T) {
	base := Manifest{
		Capabilities: []string{"modal-canvas"},
		UI: &UIManifest{
			Protocol: UIProtocol,
			Revision: UIRevision,
			Surfaces: []UISurface{{ID: "settings", Kind: "modal"}},
		},
	}
	if err := ValidateManifestUI(base); err != nil {
		t.Fatalf("valid UI manifest rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "protocol", mutate: func(value *Manifest) { value.UI.Protocol = "afterburner.ui.future" }},
		{name: "revision", mutate: func(value *Manifest) { value.UI.Revision = 2 }},
		{name: "empty", mutate: func(value *Manifest) { value.UI.Surfaces = nil }},
		{name: "invalid-id", mutate: func(value *Manifest) { value.UI.Surfaces[0].ID = "Settings!" }},
		{name: "kind", mutate: func(value *Manifest) { value.UI.Surfaces[0].Kind = "panel" }},
		{name: "duplicate", mutate: func(value *Manifest) { value.UI.Surfaces = append(value.UI.Surfaces, value.UI.Surfaces[0]) }},
		{name: "capability", mutate: func(value *Manifest) { value.Capabilities = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := base
			ui := *base.UI
			ui.Surfaces = append([]UISurface(nil), base.UI.Surfaces...)
			value.UI = &ui
			tc.mutate(&value)
			if err := ValidateManifestUI(value); err == nil {
				t.Fatal("expected invalid UI manifest to be rejected")
			}
		})
	}
}

func TestLoadRejectsReservedBuiltinIDFromGenericSource(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "black-box", "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"black-box": {
			Enabled:    true,
			ActivePath: active,
			Manifest:   Manifest{ID: "black-box", DisplayName: "Spoof", Visibility: "public"},
			Source:     Source{Type: "path", Value: active},
			Identity: IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: active,
				SignerID: "local", SignerFingerprint: "sha256:t", RegistryEpoch: 1, GrantEpoch: 1},
		},
	}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("expected reserved built-in ID from a generic source to be rejected")
	}
}

func TestLoadAcceptsReservedBuiltinIDWithVerifiedReleaseIdentity(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "black-box", "v1")
	manifest := Manifest{SchemaVersion: 1, ID: "black-box", DisplayName: "Black Box", Visibility: "builtin", Requires: Requirements{Afterburner: ">=0.1.0 <1.0.0"}, Runtime: RuntimeManifest{Execution: "in-process", Entrypoint: "runtime.mjs"}}
	writeRegistryPackage(t, active, manifest, "export default {}")
	entry := signedTestEntry(t, active, manifest, signedReleaseSource("black-box"))
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{"black-box": entry}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatalf("expected verified release built-in to load: %v", err)
	}
	if !loaded.Extensions["black-box"].Verified {
		t.Fatal("content-bound built-in identity was not verified")
	}
}

func TestLoadDoesNotVerifyModifiedPackage(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "example", "v1")
	manifest := Manifest{SchemaVersion: 1, ID: "example", DisplayName: "Example", Visibility: "private", Requires: Requirements{Afterburner: ">=0.1.0 <1.0.0"}, Runtime: RuntimeManifest{Execution: "in-process", Entrypoint: "runtime.mjs"}}
	writeRegistryPackage(t, active, manifest, "export default {}")
	manifestHash, treeHash, err := VerifyActivePackage(Entry{ActivePath: active})
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{Enabled: true, ActivePath: active, Manifest: manifest, Source: Source{Type: "path", Value: active}, UpdatedAt: "2026-09-08T00:00:00Z"}
	entry.Identity = IdentityBinding{ExtensionID: manifest.ID, ManifestHash: manifestHash, TreeHash: treeHash, SourceType: entry.Source.Type, SourceValue: entry.Source.Value, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: entry.UpdatedAt}
	entry, err = SealEntry(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(root, Registry{SchemaVersion: 1, Extensions: map[string]Entry{"example": entry}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "runtime.mjs"), []byte("export default { modified: true }"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Extensions["example"].Verified {
		t.Fatal("modified package retained verified authority")
	}
}

func writeRegistryPackage(t *testing.T, active string, manifest Manifest, runtimeSource string) {
	t.Helper()
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "afterburner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, manifest.Runtime.Entrypoint), []byte(runtimeSource), 0o600); err != nil {
		t.Fatal(err)
	}
}

func signedTestEntry(t *testing.T, active string, manifest Manifest, source Source) Entry {
	t.Helper()
	manifestHash, treeHash, err := VerifyActivePackage(Entry{ActivePath: active})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(active)))
	entry := Entry{Enabled: true, ActivePath: active, Manifest: manifest, Source: source, UpdatedAt: "2026-01-01T00:00:00Z"}
	entry.Identity = IdentityBinding{ExtensionID: manifest.ID, ManifestHash: manifestHash, TreeHash: treeHash, SourceType: source.Type, SourceValue: source.Value, SourceVersion: source.Version, SourceCommit: source.Commit, SignerID: "afterburner-release", SignerFingerprint: source.SignerFingerprint, BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: entry.UpdatedAt}
	entry, err = SealEntry(root, entry)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestRegistryMACUsesPerInstallKey(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	activeA := filepath.Join(rootA, "extensions", "black-box", "v1")
	activeB := filepath.Join(rootB, "extensions", "black-box", "v1")
	manifest := Manifest{SchemaVersion: 1, ID: "black-box", DisplayName: "Black Box", Visibility: "builtin", Requires: Requirements{Afterburner: ">=0.1.0 <1.0.0"}, Runtime: RuntimeManifest{Execution: "in-process", Entrypoint: "runtime.mjs"}}
	writeRegistryPackage(t, activeA, manifest, "export default {}")
	writeRegistryPackage(t, activeB, manifest, "export default {}")
	entryA := signedTestEntry(t, activeA, manifest, signedReleaseSource("black-box"))
	entryB := signedTestEntry(t, activeB, manifest, signedReleaseSource("black-box"))
	if entryA.Identity.RegistryMAC == "" || entryB.Identity.RegistryMAC == "" {
		t.Fatal("registry MAC was not populated")
	}
	if entryA.Identity.RegistryMAC == entryB.Identity.RegistryMAC {
		t.Fatal("registry MAC reused a global/static key across installs")
	}
}
