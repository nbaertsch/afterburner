package preflight

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/compatibility"
	"github.com/nbaertsch/afterburner/internal/copilot"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/runtimepkg"
)

func TestValidateSelfTestOutputDistinguishesFallbackFromBrokerTransport(t *testing.T) {
	output := []byte(`noise
	{
	  "projection": {"hasReasoningColumn": true, "hasContextColumn": true},
	  "runtimeObservers": {"diagnostics": {}},
	  "modal": {
	    "fallbackAPIOK": true,
	    "brokerExpected": false,
	    "brokerTransportOK": false,
	    "updateBeforeOpenRejected": true,
	    "actionOK": true,
	    "diagnostics": {"registered": 1, "closed": 1}
	  }
	}`)
	if err := validateSelfTestOutput(output, false); err != nil {
		t.Fatalf("fallback-only self-test should pass without broker expectation: %v", err)
	}
	if err := validateSelfTestOutput(output, true); err == nil {
		t.Fatal("expected missing broker transport to fail when broker env is present")
	}
}

func TestValidateSelfTestOutputRequiresFallbackAPIHealth(t *testing.T) {
	output := []byte(`{
	  "projection": {"hasReasoningColumn": true, "hasContextColumn": true},
	  "runtimeObservers": {},
	  "modal": {
	    "fallbackAPIOK": false,
	    "brokerTransportOK": true,
	    "updateBeforeOpenRejected": true,
	    "actionOK": true,
	    "diagnostics": {"registered": 1, "closed": 1}
	  }
	}`)
	if err := validateSelfTestOutput(output, true); err == nil {
		t.Fatal("expected unhealthy fallback API to fail self-test")
	}
}

func TestModalBrokerConfiguredRequiresBootstrapWithoutGlobalPipe(t *testing.T) {
	if modalBrokerConfigured([]string{"AFTERBURNER_MODAL_PIPE=p"}) {
		t.Fatal("pipe without bootstrap must not count as broker transport")
	}
	if modalBrokerConfigured([]string{"AFTERBURNER_MODAL_PIPE=p", "AFTERBURNER_MODAL_SECRET=s"}) {
		t.Fatal("process-wide secret must not count as broker transport")
	}
	if !modalBrokerConfigured([]string{"AFTERBURNER_MODAL_BOOTSTRAP=b"}) {
		t.Fatal("scoped bootstrap should enable broker transport expectation")
	}
}

func TestFailedPreflightUsesValidLastKnownGood(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable fixture")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "fakecopilot.exe")
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	command := exec.Command(goExe, "build", "-o", fake, "./testutil/fakecopilot")
	command.Dir = filepath.Join("..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Copilot: %v\n%s", err, output)
	}
	base := filepath.Join(root, "base")
	native := filepath.Join(base, "prebuilds", runtimePlatform())
	if err := os.MkdirAll(native, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "app.js"), []byte("known-app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "runtime.node"), []byte("known-runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	appHash, _ := hashFile(filepath.Join(base, "app.js"))
	runtimeHash, _ := hashFile(filepath.Join(native, "runtime.node"))
	profile := compatibility.Profiles()[0]
	basePackage := copilot.Package{
		Version: "known", Path: base, AppSHA256: appHash, RuntimeSHA256: runtimeHash, Complete: true,
	}
	prepared, err := runtimepkg.Prepare(home.Layout{CopilotHome: filepath.Join(root, "copilot-home")}, basePackage)
	if err != nil {
		t.Fatal(err)
	}
	tuple := Tuple{
		SchemaVersion:  1,
		Package:        basePackage,
		ProfileID:      profile.ID,
		RuntimeVersion: prepared.Version,
		RuntimePath:    prepared.Path,
		ValidatedAt:    time.Now(),
	}
	tuple.RegistryFingerprint, _ = registryFingerprint(nil)
	if err := save(root, tuple); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{Root: root}
	current := compatibility.Selection{
		Package: copilot.Package{Version: "bad", Path: filepath.Join(root, "bad"), Complete: true},
		Profile: profile,
	}

	var stderr bytes.Buffer
	capturePath := filepath.Join(root, "unexpected-self-test.json")
	result, err := Ensure(
		context.Background(),
		layout,
		fake,
		current,
		runtimepkg.Prepared{Version: "bad-runtime", Path: filepath.Join(root, "bad-wrapper")},
		append(os.Environ(), "AFTERBURNER_TEST_CAPTURE="+capturePath, "AFTERBURNER_TEST_EXIT_CODE=9"),
		nil,
		&stderr,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.UsedFallback || result.Package.Path != base {
		t.Fatalf("fallback result = %#v", result)
	}
	if stderr.Len() == 0 {
		t.Fatal("fallback warning was not emitted")
	}
	if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
		t.Fatal("normal fallback validation invoked the Copilot self-test executable")
	}
}

func TestValidateProvidesVerifiedExtensionBootstrap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable fixture")
	}
	root := t.TempDir()
	fake := filepath.Join(root, "fakecopilot.exe")
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	command := exec.Command(goExe, "build", "-o", fake, "./testutil/fakecopilot")
	command.Dir = filepath.Join("..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Copilot: %v\n%s", err, output)
	}
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"fixture": {
			Enabled:    true,
			Verified:   true,
			ActivePath: filepath.Join(root, "extensions", "fixture", "local-test"),
			Manifest: registry.Manifest{
				ID:         "fixture",
				Visibility: "private",
			},
			Source: registry.Source{Type: "path", Value: filepath.Join(root, "source")},
			Identity: registry.IdentityBinding{
				ManifestHash: "sha256:manifest",
				TreeHash:     "sha256:tree",
			},
		},
	}}
	if err := validate(context.Background(), fake, "test-runtime", os.Environ(), &value); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryFingerprintIgnoresNonRuntimeRegistryChanges(t *testing.T) {
	active := filepath.Join(t.TempDir(), "extensions", "example", "v1")
	entry := registry.Entry{
		Enabled: true, Verified: true, ActivePath: active,
		Manifest: registry.Manifest{ID: "example", Visibility: "private"},
		Source:   registry.Source{Type: "path", Value: active},
		Identity: registry.IdentityBinding{
			ExtensionID: "example", ManifestHash: "sha256:manifest", TreeHash: "sha256:tree",
		},
		UpdatedAt: "2026-01-01T00:00:00Z",
	}

	first := registry.Registry{SchemaVersion: 1, Epoch: 1, Extensions: map[string]registry.Entry{
		"example": entry,
		"disabled": {
			Enabled: false, ActivePath: filepath.Join(active, "disabled"),
			Manifest: registry.Manifest{ID: "disabled", Visibility: "private"},
		},
	}}
	entry.UpdatedAt = "2026-09-10T00:00:00Z"
	second := registry.Registry{SchemaVersion: 1, Epoch: 99, Extensions: map[string]registry.Entry{
		"example": entry,
		"disabled": {
			Enabled: false, ActivePath: filepath.Join(active, "elsewhere"),
			Manifest: registry.Manifest{ID: "disabled", Visibility: "public"},
		},
	}}
	a, err := registryFingerprint(&first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := registryFingerprint(&second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("semantically irrelevant registry changes invalidated preflight")
	}
	secondEntry := second.Extensions["example"]
	secondEntry.Identity.TreeHash = "sha256:changed"
	second.Extensions["example"] = secondEntry
	c, err := registryFingerprint(&second)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("runtime-relevant extension change did not invalidate preflight")
	}
}

func TestEnsureNeverInvokesBehavioralSelfTestForMissingOrChangedTuple(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable fixture")
	}
	root := t.TempDir()
	fake := buildFakeCopilot(t, root)
	selection, prepared := staticSelectionFixture(t, root)
	layout := home.Layout{Root: root}
	capturePath := filepath.Join(root, "self-test-capture.json")
	env := append(os.Environ(), "AFTERBURNER_TEST_CAPTURE="+capturePath, "AFTERBURNER_TEST_EXIT_CODE=9")

	for _, name := range []string{"missing", "changed"} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(capturePath)
			_ = os.Remove(compatibleStatePath(root))
			if name == "changed" {
				stale := tupleFor(selection, prepared, "stale-fingerprint")
				stale.RuntimeVersion = "old-runtime"
				if err := saveCompatible(root, stale); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Ensure(
				context.Background(), layout, fake, selection, prepared, env, nil, io.Discard,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.UsedFallback || result.Package.Path != selection.Package.Path {
				t.Fatalf("normal validation result = %#v", result)
			}
			if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
				t.Fatal("normal Ensure invoked the behavioral self-test")
			}
			stored, err := loadCompatible(root)
			if err != nil {
				t.Fatal(err)
			}
			if !tupleMatches(stored, selection, prepared, stored.RegistryFingerprint) {
				t.Fatalf("compatible static tuple was not persisted: %#v", stored)
			}
		})
	}
}

func TestExplicitDeepValidationInvokesSelfTestAndPersistsTuple(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable fixture")
	}
	root := t.TempDir()
	fake := buildFakeCopilot(t, root)
	selection, prepared := staticSelectionFixture(t, root)
	layout := home.Layout{Root: root}
	capturePath := filepath.Join(root, "self-test-capture.json")
	active := filepath.Join(root, "extensions", "fixture", "v1")
	value := registry.Registry{SchemaVersion: 1, Extensions: map[string]registry.Entry{
		"fixture": {
			Enabled: true, Verified: true, ActivePath: active,
			Manifest: registry.Manifest{ID: "fixture", Visibility: "private"},
			Source:   registry.Source{Type: "path", Value: active},
			Identity: registry.IdentityBinding{
				ExtensionID: "fixture", ManifestHash: "sha256:manifest", TreeHash: "sha256:tree",
			},
		},
	}}
	if err := RequestDeepValidation(root); err != nil {
		t.Fatal(err)
	}
	result, err := Ensure(
		context.Background(), layout, fake, selection, prepared,
		append(os.Environ(), "AFTERBURNER_TEST_CAPTURE="+capturePath),
		&value, io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedFallback {
		t.Fatalf("deep validation unexpectedly fell back: %#v", result)
	}
	if _, err := os.Stat(capturePath); err != nil {
		t.Fatalf("explicit deep validation did not invoke self-test: %v", err)
	}
	if deepValidationRequested(root) {
		t.Fatal("successful explicit validation request was not cleared")
	}
	fingerprint, err := registryFingerprint(&value)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !tupleMatches(stored, selection, prepared, fingerprint) {
		t.Fatalf("deep validation did not persist the selected tuple: %#v", stored)
	}
}

func buildFakeCopilot(t *testing.T, root string) string {
	t.Helper()
	fake := filepath.Join(root, "fakecopilot.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", fake, "./testutil/fakecopilot")
	command.Dir = filepath.Join("..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Copilot: %v\n%s", err, output)
	}
	return fake
}

func staticSelectionFixture(t *testing.T, root string) (compatibility.Selection, runtimepkg.Prepared) {
	t.Helper()
	base := filepath.Join(root, "base")
	native := filepath.Join(base, "prebuilds", runtimePlatform())
	if err := os.MkdirAll(native, 0o755); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(base, "app.js")
	runtimePath := filepath.Join(native, "runtime.node")
	if err := os.WriteFile(appPath, []byte("known-app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("known-runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	appHash, err := hashFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHash, err := hashFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	selection := compatibility.Selection{
		Package: copilot.Package{
			Version: "current", Path: base, AppSHA256: appHash,
			RuntimeSHA256: runtimeHash, Complete: true,
		},
		Profile: compatibility.Profiles()[0],
	}
	prepared, err := runtimepkg.Prepare(
		home.Layout{CopilotHome: filepath.Join(root, "copilot-home")},
		selection.Package,
	)
	if err != nil {
		t.Fatal(err)
	}
	return selection, prepared
}
