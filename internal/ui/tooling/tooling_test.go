package tooling

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

func TestValidateManifestAcceptsUIDeclaration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "$schema": "https://github.com/nbaertsch/afterburner/schemas/extension-v1.schema.json",
  "schemaVersion": 1,
  "id": "sample-ui",
  "displayName": "Sample UI",
  "visibility": "private",
  "requires": { "afterburner": ">=1.0.0" },
  "runtime": { "execution": "in-process", "entrypoint": "runtime/extension.mjs" },
  "ui": {
    "protocol": "afterburner.ui",
    "revision": 1,
    "surfaces": [{ "id": "sample-panel", "kind": "panel" }],
    "components": ["panel", "text", "button"],
    "capabilities": ["ui.render.components", "ui.action.invoke"]
  }
}`
	path := filepath.Join(root, "afterburner.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ValidateManifest(path)
	if !result.Valid || result.UI == nil {
		t.Fatalf("manifest invalid: %#v", result)
	}
}

func TestValidateManifestRejectsBadUIRevision(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "afterburner.json")
	manifest := `{"schemaVersion":1,"id":"bad-ui","displayName":"Bad","visibility":"private","requires":{"afterburner":"1"},"runtime":{"execution":"in-process","entrypoint":"extension.mjs"},"ui":{"protocol":"afterburner.ui","revision":99}}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ValidateManifest(path)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "ui.revision") {
		t.Fatalf("expected ui.revision error, got %#v", result)
	}
}

func TestValidateManifestRejectsUnsupportedUISurface(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "afterburner.json")
	manifest := `{"schemaVersion":1,"id":"bad-surface","displayName":"Bad Surface","visibility":"private","requires":{"afterburner":"1"},"runtime":{"execution":"in-process","entrypoint":"extension.mjs"},"ui":{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"main","kind":"sideQuest"}]}}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ValidateManifest(path)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "unsupported ui surface kind \"sideQuest\"") {
		t.Fatalf("expected unsupported surface error, got %#v", result)
	}
}

func TestValidateManifestRejectsUnsupportedUIComponent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "afterburner.json")
	manifest := `{"schemaVersion":1,"id":"bad-component","displayName":"Bad Component","visibility":"private","requires":{"afterburner":"1"},"runtime":{"execution":"in-process","entrypoint":"extension.mjs"},"ui":{"protocol":"afterburner.ui","revision":1,"components":["panel","madeUpWidget"]}}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ValidateManifest(path)
	if result.Valid || !strings.Contains(strings.Join(result.Errors, "\n"), "unsupported component kind \"madeUpWidget\"") {
		t.Fatalf("expected unsupported component error, got %#v", result)
	}
}

func TestValidateManifestRejectsUnknownUICapability(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "afterburner.json")
	manifest := `{"schemaVersion":1,"id":"bad-ui-cap","displayName":"Bad UI Cap","visibility":"private","requires":{"afterburner":"1"},"runtime":{"execution":"in-process","entrypoint":"extension.mjs"},"ui":{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"main","kind":"panel","requiredCapabilities":["ui.surface.pnael"]}],"capabilities":["ui.render.componnets"],"grantPolicy":{"schemaVersion":1,"protocol":"afterburner.ui","revision":1,"extensionId":"bad-ui-cap","denyByDefault":true,"grants":[{"id":"typo","effect":"allow","capabilities":["ui.action.invkoe"]}]}}}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ValidateManifest(path)
	errors := strings.Join(result.Errors, "\n")
	if result.Valid || !strings.Contains(errors, "invalid surface capability \"ui.surface.pnael\"") || !strings.Contains(errors, "invalid ui capability \"ui.render.componnets\"") || !strings.Contains(errors, "invalid ui grant capability \"ui.action.invkoe\"") {
		t.Fatalf("expected unknown UI capability errors, got %#v", result)
	}
}

func TestGrantRejectsUnknownCapability(t *testing.T) {
	home := t.TempDir()
	if _, err := Grant(home, "sample-ui", "ui.action.invkoe", "surface/*", "test"); err == nil || !strings.Contains(err.Error(), "unknown ui capability \"ui.action.invkoe\"") {
		t.Fatalf("expected unknown capability error, got %v", err)
	}
	if _, err := os.Stat(GrantPath(home)); !os.IsNotExist(err) {
		t.Fatalf("grant file should not be written for invalid capability, stat err=%v", err)
	}
}

func TestGrantServicePersistsDeterministically(t *testing.T) {
	home := t.TempDir()
	record, err := Grant(home, "sample-ui", "ui.action.invoke", "surface/*", "test")
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "action-invoke:surface-all" {
		t.Fatalf("grant id = %q", record.ID)
	}
	file, err := LoadGrantFile(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Records) != 1 || file.Records[0].RevokedAt != nil {
		t.Fatalf("unexpected grant file: %#v", file)
	}
	revoked, ok, err := Revoke(home, "sample-ui", record.ID, "test revoke")
	if err != nil || !ok {
		t.Fatalf("revoke ok=%t err=%v", ok, err)
	}
	if revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(DeterministicTime) {
		t.Fatalf("revocation was not deterministic: %#v", revoked)
	}
}

func TestRenderFixtureAndCertificationAreDeterministic(t *testing.T) {
	ctx := context.Background()
	first, err := RenderFixture(ctx, "black-box-certification", RenderOptions{Width: 72, Height: 20, Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderFixture(ctx, "black-box-certification", RenderOptions{Width: 72, Height: 20, Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Frame.Plain != second.Frame.Plain || StableHash(first.Frame.Plain) == "" {
		t.Fatal("fixture rendering is not deterministic")
	}
	certA, err := Certify(ctx, CertificationOptions{ExtensionID: "sample-ui", SurfaceID: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	certB, err := Certify(ctx, CertificationOptions{ExtensionID: "sample-ui", SurfaceID: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	jsonA, _ := MarshalDeterministic(certA.Report)
	jsonB, _ := MarshalDeterministic(certB.Report)
	if string(jsonA) != string(jsonB) {
		t.Fatalf("certification report changed between runs\n%s\n%s", jsonA, jsonB)
	}
	if certA.Report.Summary.Fail != 0 {
		t.Fatalf("certification failed: %s", certA.Human)
	}
}

func TestTraceRedactsAndSorts(t *testing.T) {
	home := t.TempDir()
	traceRoot := filepath.Join(home, "state", "ui-traces")
	if err := os.MkdirAll(traceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(traceRoot, "sample-ui.jsonl")
	input := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:07Z","extensionId":"sample-ui","kind":"ui.event","attributes":{"prompt":"hide","statusCode":200}}`,
		`{"timestamp":"2026-01-02T03:04:06Z","extensionId":"sample-ui","eventType":"component.snapshot","attributes":{"durationMs":5}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := Trace(TraceOptions{HomeRoot: home, ExtensionID: "sample-ui", Redacted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Records) != 2 || view.Records[0].Timestamp > view.Records[1].Timestamp {
		t.Fatalf("trace was not sorted: %#v", view.Records)
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hide") || !strings.Contains(string(data), "redacted") {
		t.Fatalf("trace not redacted: %s", data)
	}
}

func TestAllComponentsFixtureCoversPublicCatalog(t *testing.T) {
	fixture := AllComponentsFixture()
	covered := map[string]bool{}
	var walk func(component.Node)
	walk = func(node component.Node) {
		covered[string(node.Kind)] = true
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(fixture.Tree.Root)
	for _, entry := range component.PublicCatalog() {
		if !covered[string(entry.Kind)] {
			t.Fatalf("all-components fixture missing public kind %q", entry.Kind)
		}
	}
}

func TestGeneratedAllComponentsFixtureIsCurrent(t *testing.T) {
	actual, err := json.MarshalIndent(AllComponentsFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	path := filepath.Join("testdata", "fixtures", "all-components.generated.json")
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_UI_FIXTURES") == "1" {
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if strings.ReplaceAll(string(expected), "\r\n", "\n") != string(actual) {
		t.Fatalf("%s is stale; run UPDATE_UI_FIXTURES=1 go test ./internal/ui/tooling -run TestGeneratedAllComponentsFixtureIsCurrent", path)
	}
}

func TestFixtureFilesAreValidAndAbuseEnvelopeFails(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "fixtures", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("expected fixture files")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatalf("fixture JSON invalid: %v", err)
			}
		})
	}
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "malformed-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateEnvelope(envelope); err == nil {
		t.Fatal("malformed envelope fixture unexpectedly validated")
	}
}

func TestProtocolSimulatorRejectsBadEnvelope(t *testing.T) {
	sim := NewProtocolSimulator()
	bad := protocol.NewEnvelope(protocol.EnvelopeHello, "bad", protocol.Actor{Kind: protocol.ActorExtension, ID: "x"}, protocol.Actor{Kind: protocol.ActorHost, ID: "host"}, rawProps(map[string]any{"ok": true}))
	bad.Revision = 99
	if err := sim.Dispatch(context.Background(), bad); err == nil {
		t.Fatal("expected bad revision to be rejected")
	}
}
