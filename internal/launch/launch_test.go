package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/terminal"
)

func TestBrokerEnabledByDefaultWithEmergencyOptOut(t *testing.T) {
	t.Setenv("AFTERBURNER_DISABLE_TERMINAL_BROKER", "")
	if !brokerFeatureEnabled() {
		t.Fatal("broker disabled by default")
	}
	for _, value := range []string{"1", "true", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AFTERBURNER_DISABLE_TERMINAL_BROKER", value)
			if brokerFeatureEnabled() {
				t.Fatalf("broker enabled despite opt-out %q", value)
			}
		})
	}
}

func TestBrokerRequiresFileStreams(t *testing.T) {
	t.Setenv("AFTERBURNER_TERMINAL_BROKER", "1")
	if shouldUseBroker(Options{Stdin: bytes.NewBuffer(nil), Stdout: os.Stdout}) {
		t.Fatal("broker enabled for non-file stdin")
	}
	if shouldUseBroker(Options{Stdin: os.Stdin, Stdout: bytes.NewBuffer(nil)}) {
		t.Fatal("broker enabled for non-file stdout")
	}
}

func TestPreissuedModalRegistrationsRequireDeclaredSurfaces(t *testing.T) {
	value := &registry.Registry{Extensions: map[string]registry.Entry{
		registry.LegacyOpenAIServerID: {
			Enabled:  true,
			Verified: true,
			Manifest: registry.Manifest{
				ID:           registry.LegacyOpenAIServerID,
				Capabilities: []string{"modal-canvas"},
			},
		},
	}}
	got := preissuedModalRegistrations(value)
	if len(got) != 0 {
		t.Fatalf("undeclared registrations = %#v", got)
	}
}

func TestRunDirectFallbackPreservesBytesEnvArgsAndExit(t *testing.T) {
	t.Setenv("AFTERBURNER_TERMINAL_BROKER", "1")
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	args := []string{"-test.run=TestHelperProcess", "--", "--flag=value", "", `a"b`, "Unicode-世界"}
	env := append(withoutModalEnv(os.Environ()),
		"GO_WANT_HELPER_PROCESS=launch-direct",
		"AFTERBURNER_TEST_CAPTURE="+capturePath,
	)
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), Options{
		Executable: os.Args[0],
		Args:       args,
		Env:        env,
		Stdin:      strings.NewReader("stdin-bytes"),
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if code != 37 {
		t.Fatalf("exit code = %d, want 37", code)
	}
	if got, want := stdout.String(), "stdout:stdin-bytes\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "stderr:--flag=value||a\"b|Unicode-世界\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	var captured struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--flag=value", "", `a"b`, "Unicode-世界"}
	if !reflect.DeepEqual(captured.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", captured.Args, wantArgs)
	}
	for _, key := range []string{"AFTERBURNER_MODAL_PIPE", "AFTERBURNER_MODAL_SECRET", "AFTERBURNER_MODAL_BOOTSTRAP", "AFTERBURNER_MODAL_OWNER_EXTENSION_ID", "AFTERBURNER_MODAL_CANVAS_ID", "AFTERBURNER_MODAL_SURFACE_ID", "AFTERBURNER_SESSION_ROUTE"} {
		if captured.Env[key] != "" {
			t.Fatalf("direct fallback leaked modal env: %#v", captured.Env)
		}
	}
}

func TestRunDirectProvidesNativeIdentityAssertionsWithoutModalTransport(t *testing.T) {
	root := t.TempDir()
	afterburnerHome := filepath.Join(root, "afterburner")
	activePath := filepath.Join(afterburnerHome, "extensions", "black-box", "test")
	if err := os.MkdirAll(activePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := registry.Manifest{SchemaVersion: 1, ID: "black-box", DisplayName: "Black Box", Visibility: "builtin", Requires: registry.Requirements{Afterburner: ">=0.1.0 <1.0.0"}, Runtime: registry.RuntimeManifest{Execution: "in-process", Entrypoint: "runtime.mjs"}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activePath, "afterburner.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activePath, "runtime.mjs"), []byte("export async function activate() {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestHash, treeHash, err := registry.VerifyActivePackage(registry.Entry{ActivePath: activePath})
	if err != nil {
		t.Fatal(err)
	}
	source := registry.Source{Type: "signed-release", Value: "black-box", Version: "v1.0.0", Commit: strings.Repeat("a", 40), Digest: "sha256:" + strings.Repeat("b", 64), ManifestDigest: "sha256:" + strings.Repeat("c", 64), SignerFingerprint: "sha256:" + strings.Repeat("d", 64)}
	entry := registry.Entry{Enabled: true, ActivePath: activePath, Manifest: manifest, Source: source, UpdatedAt: "2026-09-05T00:00:00Z"}
	entry.Identity = registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: manifestHash, TreeHash: treeHash, SourceType: source.Type, SourceValue: source.Value, SourceVersion: source.Version, SourceCommit: source.Commit, SignerID: "afterburner-release", SignerFingerprint: source.SignerFingerprint, BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: entry.UpdatedAt}
	entry, err = registry.SealEntry(afterburnerHome, entry)
	if err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(root, "capture.json")
	env := append(withoutModalEnv(os.Environ()),
		"GO_WANT_HELPER_PROCESS=launch-direct",
		"AFTERBURNER_TEST_CAPTURE="+capturePath,
	)
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), Options{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestHelperProcess", "--"},
		Env:        env,
		Stdin:      strings.NewReader(""),
		Stdout:     &stdout,
		Stderr:     &stderr,
		ExtensionRegistry: &registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
			"black-box": entry,
		}},
	})
	if err != nil || code != 37 {
		t.Fatalf("Run returned code=%d err=%v", code, err)
	}
	var captured struct {
		Env             map[string]string `json:"env"`
		NativeBootstrap struct {
			SessionRoute       string `json:"sessionRoute"`
			SessionID          string `json:"sessionId"`
			ModalSurfaces      []any  `json:"modalSurfaces"`
			VerifiedExtensions []struct {
				ExtensionID    string `json:"extensionId"`
				ActivePath     string `json:"activePath"`
				TrustedBuiltin bool   `json:"trustedBuiltin"`
			} `json:"verifiedExtensions"`
		} `json:"nativeBootstrap"`
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Env["AFTERBURNER_MODAL_BOOTSTRAP"] != "" || captured.Env["AFTERBURNER_MODAL_PIPE"] != "" {
		t.Fatalf("direct mode exposed modal transport: %#v", captured.Env)
	}
	if captured.NativeBootstrap.SessionID == "" || captured.NativeBootstrap.SessionID == captured.NativeBootstrap.SessionRoute {
		t.Fatalf("direct mode did not provide a distinct native session id: %#v", captured.NativeBootstrap)
	}
	if captured.Env["AFTERBURNER_SESSION_ROUTE"] == "" || captured.Env["AFTERBURNER_SESSION_ROUTE"] != captured.NativeBootstrap.SessionRoute {
		t.Fatalf("direct mode did not expose the host-issued session route: %#v", captured.Env)
	}
	if captured.Env["AFTERBURNER_NATIVE_BOOTSTRAP"] == "" || len(captured.NativeBootstrap.VerifiedExtensions) != 1 {
		t.Fatalf("direct mode did not provide native identity assertion: %#v", captured)
	}
	assertion := captured.NativeBootstrap.VerifiedExtensions[0]
	if assertion.ExtensionID != "black-box" || assertion.ActivePath != activePath || !assertion.TrustedBuiltin || len(captured.NativeBootstrap.ModalSurfaces) != 0 {
		t.Fatalf("unexpected direct native bootstrap: %#v", captured.NativeBootstrap)
	}
}

func TestWithEnvReplacesCaseInsensitivelyAndCopies(t *testing.T) {
	original := []string{`Path=C:\bin`, "AFTERBURNER_MODAL_PIPE=old"}
	updated := withEnv(original, "AFTERBURNER_MODAL_PIPE", `\\.\pipe\new`)
	updated = withEnv(updated, "AFTERBURNER_MODAL_SECRET", "secret")
	if original[1] != "AFTERBURNER_MODAL_PIPE=old" {
		t.Fatalf("original env mutated: %#v", original)
	}
	want := []string{`Path=C:\bin`, `AFTERBURNER_MODAL_PIPE=\\.\pipe\new`, "AFTERBURNER_MODAL_SECRET=secret"}
	for index := range want {
		if updated[index] != want[index] {
			t.Fatalf("updated[%d] = %q, want %q (all %#v)", index, updated[index], want[index], updated)
		}
	}
}

func TestWithModalEnvUsesPerLaunchValuesAndCopies(t *testing.T) {
	original := []string{
		`Path=C:\bin`,
		"AFTERBURNER_MODAL_PIPE=stale-pipe",
		"AFTERBURNER_MODAL_SECRET=stale-token",
		"AFTERBURNER_MODAL_OWNER_EXTENSION_ID=stale-owner",
	}
	updated := withModalEnv(original, `C:\temp\modal-bootstrap.json`)
	if !reflect.DeepEqual(original, []string{`Path=C:\bin`, "AFTERBURNER_MODAL_PIPE=stale-pipe", "AFTERBURNER_MODAL_SECRET=stale-token", "AFTERBURNER_MODAL_OWNER_EXTENSION_ID=stale-owner"}) {
		t.Fatalf("original env mutated: %#v", original)
	}
	want := []string{
		`Path=C:\bin`,
		`AFTERBURNER_NATIVE_BOOTSTRAP=C:\temp\modal-bootstrap.json`,
		`AFTERBURNER_MODAL_BOOTSTRAP=C:\temp\modal-bootstrap.json`,
	}
	if !reflect.DeepEqual(updated, want) {
		t.Fatalf("updated = %#v, want %#v", updated, want)
	}
}

func TestWriteNativeBootstrapContainsOnlyPreissuedSurfaces(t *testing.T) {
	surfaces := []terminal.ModalCapability{{OwnerExtensionID: "black-box", CanvasID: "black-box", SurfaceID: "black-box", Pipe: `\\.\pipe\test`}}
	path, cleanup, err := writeNativeBootstrap(surfaces, []nativeIdentityAssertion{{ExtensionID: "black-box", ActivePath: `C:\tmp\black-box`, ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "signed-release", SourceValue: "black-box", TrustedBuiltin: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["pipe"] != nil || payload["registrationToken"] != nil || payload["token"] != nil || payload["modalCapabilities"] != nil {
		t.Fatalf("bootstrap payload leaked global transport or registration authority: %#v", payload)
	}
	if route, ok := payload["sessionRoute"].(string); !ok || len(route) < 32 || strings.ContainsAny(route, `\\/:*?"<>|`) {
		t.Fatalf("bootstrap did not contain a Windows-safe session route: %#v", payload["sessionRoute"])
	}
	sessionID, ok := payload["sessionId"].(string)
	if !ok || len(sessionID) < 32 || strings.ContainsAny(sessionID, `\\/:*?"<>|`) {
		t.Fatalf("bootstrap did not contain a Windows-safe native session id: %#v", payload["sessionId"])
	}
	got, ok := payload["modalSurfaces"].([]any)
	if !ok || len(got) != 1 {
		t.Fatalf("bootstrap surfaces = %#v", payload["modalSurfaces"])
	}
	surface := got[0].(map[string]any)
	if surface["sessionId"] != sessionID {
		t.Fatalf("bootstrap surface did not carry the native session id: %#v", surface)
	}
	if surface["pipe"] != `\\.\pipe\test` {
		t.Fatalf("bootstrap surface did not carry its scoped pipe: %#v", surface)
	}
	if _, leaked := surface["token"]; leaked {
		t.Fatalf("bootstrap surface leaked token: %#v", surface)
	}
	assertions, ok := payload["verifiedExtensions"].([]any)
	if !ok || len(assertions) != 1 || assertions[0].(map[string]any)["registryMac"] != nil {
		t.Fatalf("bootstrap assertions leaked registry MAC material: %#v", payload["verifiedExtensions"])
	}
}

func TestNativeIdentityAssertionsRetainSealedContentHashes(t *testing.T) {
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"example": {
			Enabled:    true,
			Verified:   true,
			ActivePath: filepath.Join(t.TempDir(), "example"),
			Manifest:   registry.Manifest{ID: "example"},
			Source:     registry.Source{Type: "path", Value: "source"},
			Identity: registry.IdentityBinding{
				ExtensionID:  "example",
				ManifestHash: "sha256:sealed-manifest",
				TreeHash:     "sha256:sealed-tree",
			},
		},
	}}
	assertions := nativeIdentityAssertions(&value)
	if len(assertions) != 1 {
		t.Fatalf("assertions = %#v", assertions)
	}
	if assertions[0].ManifestHash != "sha256:sealed-manifest" || assertions[0].TreeHash != "sha256:sealed-tree" {
		t.Fatalf("bootstrap replaced sealed hashes: %#v", assertions[0])
	}
}

func TestPreissueModalCapabilitiesRequiresVerifiedModalCapability(t *testing.T) {
	server, err := terminal.NewModalServer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	generic := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"black-box": {
			Enabled:  true,
			Manifest: registry.Manifest{ID: "black-box", Visibility: "public", Capabilities: []string{"modal-canvas"}},
			Source:   registry.Source{Type: "path", Value: `C:\\tmp\\black-box`},
			Identity: registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: `C:\\tmp\\black-box`, GrantEpoch: 1},
		},
	}}
	if got, err := preissueModalCapabilities(server, &generic); err != nil || len(got) != 0 {
		t.Fatalf("unverified black-box received capabilities: %#v", got)
	}

	verified := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"black-box":     verifiedEntry("black-box", "builtin", "signed-release", "black-box", true, []string{"modal-canvas"}),
		"openai-server": verifiedEntry("openai-server", "builtin", "signed-release", "openai-server", true, []string{"modal-canvas"}),
		"no-modal":      verifiedEntry("no-modal", "private", "path", `C:\\repo\\extensions\\NoModal`, false, []string{"session-command"}),
	}}
	verified.Extensions["black-box"] = withUISurfaces(verified.Extensions["black-box"], "afterburner-black-box-live", "black-box")
	verified.Extensions["openai-server"] = withUISurfaces(verified.Extensions["openai-server"], "openai-server", "copilot-openai")
	got, err := preissueModalCapabilities(server, &verified)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("verified modal surfaces = %#v", got)
	}
	if got[0].OwnerExtensionID != "black-box" || got[0].SurfaceID != "afterburner-black-box-live" || got[1].SurfaceID != "black-box" {
		t.Fatalf("black-box compatibility surfaces = %#v", got[:2])
	}
	if got[2].OwnerExtensionID != "openai-server" || got[2].CanvasID != "openai-server" || got[2].SurfaceID != "openai-server" {
		t.Fatalf("openai-server modal surface = %#v", got[2])
	}
	if got[3].OwnerExtensionID != "openai-server" || got[3].CanvasID != "copilot-openai" || got[3].SurfaceID != "copilot-openai" {
		t.Fatalf("legacy openai-server modal surface = %#v", got[3])
	}
}

func TestPreissuedModalRegistrationsUseDeclaredExtensionLocalSurfaces(t *testing.T) {
	value := &registry.Registry{Extensions: map[string]registry.Entry{
		"alpha": verifiedEntry("alpha", "private", "path", `C:\extensions\alpha`, false, []string{"modal-canvas"}),
		"beta":  verifiedEntry("beta", "private", "path", `C:\extensions\beta`, false, []string{"modal-canvas"}),
	}}
	value.Extensions["alpha"] = withUISurfaces(value.Extensions["alpha"], "settings", "details")
	value.Extensions["beta"] = withUISurfaces(value.Extensions["beta"], "settings")
	got := preissuedModalRegistrations(value)
	want := []terminal.ModalRegistration{
		{OwnerExtensionID: "alpha", CanvasID: "settings", SurfaceID: "settings"},
		{OwnerExtensionID: "alpha", CanvasID: "details", SurfaceID: "details"},
		{OwnerExtensionID: "beta", CanvasID: "settings", SurfaceID: "settings"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registrations = %#v, want %#v", got, want)
	}
}

func withUISurfaces(entry registry.Entry, ids ...string) registry.Entry {
	entry.Manifest.UI = &registry.UIManifest{Protocol: registry.UIProtocol, Revision: registry.UIRevision}
	for _, id := range ids {
		entry.Manifest.UI.Surfaces = append(entry.Manifest.UI.Surfaces, registry.UISurface{ID: id, Kind: "modal"})
	}
	return entry
}

func verifiedEntry(id, visibility, sourceType, sourceValue string, builtinSigned bool, capabilities []string) registry.Entry {
	source := registry.Source{Type: sourceType, Value: sourceValue}
	signerID := ""
	signerFingerprint := ""
	if builtinSigned {
		source.Version = "v1.0.0"
		source.Commit = strings.Repeat("a", 40)
		source.Digest = "sha256:" + strings.Repeat("b", 64)
		source.ManifestDigest = "sha256:" + strings.Repeat("c", 64)
		source.SignerFingerprint = "sha256:" + strings.Repeat("d", 64)
		signerID = "afterburner-release"
		signerFingerprint = source.SignerFingerprint
	}
	return registry.Entry{
		Enabled:  true,
		Manifest: registry.Manifest{ID: id, Visibility: visibility, Capabilities: capabilities},
		Source:   source,
		Identity: registry.IdentityBinding{ExtensionID: id, ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: sourceType, SourceValue: sourceValue, SourceVersion: source.Version, SourceCommit: source.Commit, SignerID: signerID, SignerFingerprint: signerFingerprint, BuiltinSigned: builtinSigned, RegistryEpoch: 1, GrantEpoch: 1},
		Verified: true,
	}
}

func TestBrokerCleanupOrderAndIdempotence(t *testing.T) {
	var calls []string
	cleanup := brokerCleanup(
		func() error { calls = append(calls, "pipe"); return nil },
		func() { calls = append(calls, "modals") },
		func() error { calls = append(calls, "broker"); return nil },
	)
	cleanup()
	cleanup()
	want := []string{"pipe", "modals", "broker"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("cleanup calls = %#v, want %#v", calls, want)
	}
}

func TestNormalizeExitCode(t *testing.T) {
	if got := normalizeExitCode(37, nil); got != 37 {
		t.Fatalf("normal exit = %d, want 37", got)
	}
	if got := normalizeExitCode(-1, nil); got != 1 {
		t.Fatalf("negative exit = %d, want 1", got)
	}
	if got := normalizeExitCode(0, context.Canceled); got != 1 {
		t.Fatalf("errored zero exit = %d, want 1", got)
	}
}

func TestWriteBrokerInputForwardsCtrlCAsInterrupt(t *testing.T) {
	process := &launchFakeProcess{}
	broker := terminal.NewBroker(&launchFakeBackend{process: process}, terminal.BrokerOptions{})
	if err := broker.Start(context.Background(), terminal.Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := writeBrokerInput(broker, []byte("ab\x03cd")); err != nil {
		t.Fatal(err)
	}
	if got, want := string(process.input), "ab\x03cd"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if process.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", process.interrupts)
	}
}

func TestWriteBrokerInputRoutesCtrlCToModalOwner(t *testing.T) {
	process := &launchFakeProcess{}
	var routedOwner terminal.Owner
	var routedInput []byte
	broker := terminal.NewBroker(&launchFakeBackend{process: process}, terminal.BrokerOptions{
		InputRouter: terminal.InputRouterFunc(func(owner terminal.Owner, data []byte, _ terminal.Process) (int, error) {
			routedOwner = owner
			routedInput = append(routedInput, data...)
			return len(data), nil
		}),
	})
	if err := broker.Start(context.Background(), terminal.Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(terminal.OwnerModal)
	if err := writeBrokerInput(broker, []byte("\x03")); err != nil {
		t.Fatal(err)
	}
	if process.interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0", process.interrupts)
	}
	if got, want := routedOwner, terminal.OwnerModal; got != want {
		t.Fatalf("routed owner = %q, want %q", got, want)
	}
	if got, want := string(routedInput), "\x03"; got != want {
		t.Fatalf("routed input = %q, want %q", got, want)
	}
}

func TestInterruptBrokerIfCopilotOwnerRespectsOwner(t *testing.T) {
	process := &launchFakeProcess{}
	broker := terminal.NewBroker(&launchFakeBackend{process: process}, terminal.BrokerOptions{})
	if err := broker.Start(context.Background(), terminal.Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	broker.SetOwner(terminal.OwnerModal)
	if err := interruptBrokerIfCopilotOwner(broker); err != nil {
		t.Fatal(err)
	}
	if process.interrupts != 0 {
		t.Fatalf("modal-owned interrupts = %d, want 0", process.interrupts)
	}
	broker.SetOwner(terminal.OwnerCopilot)
	if err := interruptBrokerIfCopilotOwner(broker); err != nil {
		t.Fatal(err)
	}
	if process.interrupts != 1 {
		t.Fatalf("copilot-owned interrupts = %d, want 1", process.interrupts)
	}
	if got, want := string(process.input), "\x03"; got != want {
		t.Fatalf("copilot-owned interrupt input = %q, want %q", got, want)
	}
}

func TestResizeBrokerAndRendererResizesProcess(t *testing.T) {
	process := &launchFakeProcess{}
	broker := terminal.NewBroker(&launchFakeBackend{process: process}, terminal.BrokerOptions{})
	if err := broker.Start(context.Background(), terminal.Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	resizeBrokerAndRenderer(broker, terminal.NewTerminalModalRendererWithSize(io.Discard, terminal.Size{Cols: 80, Rows: 24}), terminal.Size{Cols: 132, Rows: 43})
	if got, want := process.resizes, []terminal.Size{{Cols: 132, Rows: 43}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resizes = %#v, want %#v", got, want)
	}
}

func TestResizeBrokerAndRendererUpdatesModalRepaintDimensions(t *testing.T) {
	process := &launchFakeProcess{}
	broker := terminal.NewBroker(&launchFakeBackend{process: process}, terminal.BrokerOptions{})
	if err := broker.Start(context.Background(), terminal.Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	renderer := terminal.NewTerminalModalRendererWithSize(&output, terminal.Size{Cols: 80, Rows: 24})
	renderer.ShowModal(terminal.ModalFrame{ID: "black-box", Title: "modal"})
	resizeBrokerAndRenderer(broker, renderer, terminal.Size{Cols: 160, Rows: 44})
	renderer.WriteCopilotOutput([]byte("\x1b[44;1Hresize-marker\x1b[1;1H"))
	renderer.HideModal()

	repaintPrefix := "\x1b[?25l\x1b[0m\x1b[H\x1b[2J"
	repaint := output.String()
	if index := strings.LastIndex(repaint, repaintPrefix); index >= 0 {
		repaint = repaint[index:]
	}
	lines := strings.Split(strings.ReplaceAll(repaint, "\r", ""), "\n")
	markerLine := -1
	for index, line := range lines {
		if strings.Contains(line, "resize-marker") {
			markerLine = index
			break
		}
	}
	if markerLine < 43 {
		t.Fatalf("resize marker line = %d, want at least 44th line", markerLine+1)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "launch-direct" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	input, _ := io.ReadAll(os.Stdin)
	_, _ = fmt.Fprintf(os.Stdout, "stdout:%s\n", input)
	_, _ = fmt.Fprintf(os.Stderr, "stderr:%s\n", strings.Join(args, "|"))
	capturePath := os.Getenv("AFTERBURNER_TEST_CAPTURE")
	var nativeBootstrap map[string]any
	if path := os.Getenv("AFTERBURNER_NATIVE_BOOTSTRAP"); path != "" {
		if bootstrapData, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(bootstrapData, &nativeBootstrap)
		}
	}
	data, _ := json.Marshal(struct {
		Args            []string          `json:"args"`
		Env             map[string]string `json:"env"`
		NativeBootstrap map[string]any    `json:"nativeBootstrap,omitempty"`
	}{
		Args: args,
		Env: map[string]string{
			"AFTERBURNER_MODAL_PIPE":               os.Getenv("AFTERBURNER_MODAL_PIPE"),
			"AFTERBURNER_MODAL_BOOTSTRAP":          os.Getenv("AFTERBURNER_MODAL_BOOTSTRAP"),
			"AFTERBURNER_NATIVE_BOOTSTRAP":         os.Getenv("AFTERBURNER_NATIVE_BOOTSTRAP"),
			"AFTERBURNER_MODAL_SECRET":             os.Getenv("AFTERBURNER_MODAL_SECRET"),
			"AFTERBURNER_MODAL_OWNER_EXTENSION_ID": os.Getenv("AFTERBURNER_MODAL_OWNER_EXTENSION_ID"),
			"AFTERBURNER_MODAL_CANVAS_ID":          os.Getenv("AFTERBURNER_MODAL_CANVAS_ID"),
			"AFTERBURNER_MODAL_SURFACE_ID":         os.Getenv("AFTERBURNER_MODAL_SURFACE_ID"),
			"AFTERBURNER_SESSION_ROUTE":            os.Getenv("AFTERBURNER_SESSION_ROUTE"),
		},
		NativeBootstrap: nativeBootstrap,
	})
	_ = os.WriteFile(capturePath, data, 0o600)
	os.Exit(37)
}

type launchFakeBackend struct {
	process *launchFakeProcess
}

func (b *launchFakeBackend) Start(_ context.Context, _ terminal.Command, _ terminal.OutputHandler) (terminal.Process, error) {
	return b.process, nil
}

type launchFakeProcess struct {
	input      []byte
	interrupts int
	resizes    []terminal.Size
}

func (p *launchFakeProcess) WriteInput(data []byte) (int, error) {
	p.input = append(p.input, data...)
	return len(data), nil
}

func (p *launchFakeProcess) Resize(size terminal.Size) error {
	p.resizes = append(p.resizes, size)
	return nil
}

func (p *launchFakeProcess) Interrupt() error {
	p.interrupts++
	return nil
}

func (p *launchFakeProcess) Kill() error { return nil }

func (p *launchFakeProcess) Wait() (terminal.ExitStatus, error) { return terminal.ExitStatus{}, nil }

func (p *launchFakeProcess) Close() error { return nil }

func withoutModalEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if ok && (strings.HasPrefix(upper, "AFTERBURNER_MODAL_") || upper == "AFTERBURNER_SESSION_ROUTE") {
			continue
		}
		result = append(result, entry)
	}
	return result
}
