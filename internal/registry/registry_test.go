package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
		OpenAIServerID:       {Enabled: false, ActivePath: currentActive, Manifest: Manifest{ID: OpenAIServerID, DisplayName: OpenAIServerName, Visibility: "builtin"}, Source: Source{Type: "embedded", Value: OpenAIServerID}},
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

func TestLoadAcceptsReservedBuiltinIDWithVerifiedEmbeddedIdentity(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "black-box", "v1")
	manifest := Manifest{SchemaVersion: 1, ID: "black-box", DisplayName: "Black Box", Visibility: "builtin", Requires: Requirements{Afterburner: ">=0.1.0 <1.0.0"}, Runtime: RuntimeManifest{Execution: "in-process", Entrypoint: "runtime.mjs"}}
	writeRegistryPackage(t, active, manifest, "export default {}")
	entry := signedTestEntry(t, active, manifest, Source{Type: "embedded", Value: "black-box"})
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{"black-box": entry}}
	if err := Save(root, value); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("expected verified embedded built-in to load: %v", err)
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
	entry.Identity = IdentityBinding{ExtensionID: manifest.ID, ManifestHash: manifestHash, TreeHash: treeHash, SourceType: source.Type, SourceValue: source.Value, SignerID: "afterburner-core", SignerFingerprint: "builtin:" + manifest.ID, BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: entry.UpdatedAt}
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
	entryA := signedTestEntry(t, activeA, manifest, Source{Type: "embedded", Value: "black-box"})
	entryB := signedTestEntry(t, activeB, manifest, Source{Type: "embedded", Value: "black-box"})
	if entryA.Identity.RegistryMAC == "" || entryB.Identity.RegistryMAC == "" {
		t.Fatal("registry MAC was not populated")
	}
	if entryA.Identity.RegistryMAC == entryB.Identity.RegistryMAC {
		t.Fatal("registry MAC reused a global/static key across installs")
	}
}

func TestRegistryPreservesUIManifestDeclaration(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "extensions", "example", "v1")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	uiDeclaration := json.RawMessage(`{"protocol":"afterburner.ui","revision":1}`)
	value := Registry{SchemaVersion: 1, Extensions: map[string]Entry{
		"example": {
			Enabled:    true,
			ActivePath: active,
			Manifest: Manifest{
				ID:          "example",
				DisplayName: "Example",
				Visibility:  "public",
				UI:          uiDeclaration,
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
	var gotUI, wantUI map[string]any
	if err := json.Unmarshal(got.Extensions["example"].Manifest.UI, &gotUI); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(uiDeclaration, &wantUI); err != nil {
		t.Fatal(err)
	}
	if gotUI["protocol"] != wantUI["protocol"] || gotUI["revision"] != wantUI["revision"] {
		t.Fatalf("UI manifest declaration was not preserved: %s", got.Extensions["example"].Manifest.UI)
	}
}
