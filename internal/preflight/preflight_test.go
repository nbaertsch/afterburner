package preflight

import (
	"bytes"
	"context"
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
	preparedPath := filepath.Join(root, "prepared")
	if err := os.MkdirAll(preparedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preparedPath, "app.js"), []byte("wrapper"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := compatibility.Profiles()[0]
	tuple := Tuple{
		SchemaVersion: 1,
		Package: copilot.Package{
			Version: "known", Path: base, AppSHA256: appHash, RuntimeSHA256: runtimeHash, Complete: true,
		},
		ProfileID:      profile.ID,
		RuntimeVersion: "known-runtime",
		RuntimePath:    preparedPath,
		ValidatedAt:    time.Now(),
	}
	tuple.RegistryFingerprint, _ = registryFingerprint(root)
	if err := save(root, tuple); err != nil {
		t.Fatal(err)
	}
	layout := home.Layout{Root: root}
	current := compatibility.Selection{
		Package: copilot.Package{Version: "bad", Path: filepath.Join(root, "bad"), Complete: true},
		Profile: profile,
	}
	var stderr bytes.Buffer
	result, err := Ensure(
		context.Background(),
		layout,
		fake,
		current,
		runtimepkg.Prepared{Version: "bad-runtime", Path: filepath.Join(root, "bad-wrapper")},
		append(os.Environ(), "AFTERBURNER_TEST_EXIT_CODE=9"),
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
