package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	regpkg "github.com/nbaertsch/afterburner/internal/registry"
)

type capturedInvocation struct {
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	ProxyReady bool              `json:"proxyReady"`
}

func TestNativeArgumentFidelity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher acceptance")
	}
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	bin := filepath.Join(t.TempDir(), "afterburn.exe")
	fake := filepath.Join(t.TempDir(), "fakecopilot.exe")
	build(t, goExe, bin, ".")
	build(t, goExe, fake, "../../internal/testutil/fakecopilot")

	tests := [][]string{
		{},
		{"--version"},
		{"-p", "hello world"},
		{"--resume=7c21f952-884d-498d-937b-47b4ce7b3b19"},
		{"--flag=value", "--flag=value2", "", `a"b`, "Unicode-世界"},
		{"run", "install", ""},
		{"--", "update", "--check"},
		{"--", "--safe-mode", "--disable-extension", "black-box"},
	}
	for _, args := range tests {
		name := "zero"
		if len(args) > 0 {
			name = args[0]
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			createFakePackage(t, root)
			capturePath := filepath.Join(root, "capture.json")
			cmd := exec.Command(bin, args...)
			cmd.Env = append(os.Environ(),
				"AFTERBURNER_HOME="+filepath.Join(root, "afterburner"),
				"AFTERBURNER_NORMAL_COPILOT_HOME="+filepath.Join(root, "normal"),
				"AFTERBURNER_COPILOT_EXECUTABLE="+fake,
				"AFTERBURNER_COPILOT_PACKAGE_ROOTS="+filepath.Join(root, "packages"),
				"AFTERBURNER_ALLOW_UNPROFILED=1",
				"AFTERBURNER_SKIP_PREFLIGHT=1",
				"AFTERBURNER_TEST_CAPTURE="+capturePath,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("afterburn failed: %v\n%s", err, output)
			}
			data, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			var got capturedInvocation
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			want := args
			if len(args) > 0 && (args[0] == "run" || args[0] == "--") {
				want = args[1:]
			}
			if len(got.Args) < 2 || got.Args[0] != "--prefer-version" {
				t.Fatalf("missing injected prefer-version: %#v", got.Args)
			}
			if !reflect.DeepEqual(got.Args[2:], want) {
				t.Fatalf("forwarded args %#v, want %#v", got.Args[2:], want)
			}
			if got.Env["COPILOT_HOME"] == "" || got.Env["AFTERBURNER_BASE_PACKAGE"] == "" {
				t.Fatalf("managed environment incomplete: %#v", got.Env)
			}
		})
	}
}

func TestNativeProxyStartsBeforeCopilot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher acceptance")
	}
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	bin := filepath.Join(t.TempDir(), "afterburn.exe")
	fake := filepath.Join(t.TempDir(), "fakecopilot.exe")
	build(t, goExe, bin, ".")
	build(t, goExe, fake, "../../internal/testutil/fakecopilot")

	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	afterburnerHome := filepath.Join(root, "afterburner")
	activePath := filepath.Join(afterburnerHome, "extensions", "byo-models", "test")
	if err := os.MkdirAll(activePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := regpkg.Manifest{SchemaVersion: 1, ID: "byo-models", Name: "BYOModels", DisplayName: "BYOModels", Visibility: "builtin"}
	manifestData, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(activePath, "afterburner.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestHash, treeHash, err := regpkg.VerifyActivePackage(regpkg.Entry{ActivePath: activePath})
	if err != nil {
		t.Fatal(err)
	}
	entry := regpkg.Entry{Enabled: true, ActivePath: activePath, Manifest: manifest, Source: regpkg.Source{Type: "embedded", Value: "byo-models"}, UpdatedAt: "2026-09-03T00:00:00Z"}
	entry.Identity = regpkg.IdentityBinding{ExtensionID: "byo-models", ManifestHash: manifestHash, TreeHash: treeHash, SourceType: "embedded", SourceValue: "byo-models", SignerID: "afterburner-core", SignerFingerprint: "builtin:byo-models", BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: "2026-09-03T00:00:00Z"}
	entry, err = regpkg.SealEntry(afterburnerHome, entry)
	if err != nil {
		t.Fatal(err)
	}
	registryData, err := json.Marshal(regpkg.Registry{SchemaVersion: 1, Extensions: map[string]regpkg.Entry{"byo-models": entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(afterburnerHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afterburnerHome, "registry.json"), registryData, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "byomodels.json")
	config := fmt.Sprintf(`{
  "version": 1,
  "providers": [{
    "name": "test",
    "baseUrl": %q,
    "requestCompatibility": {
      "maxInputItemIdLength": 64,
      "proxyPort": %d
    }
  }]
}`, upstream.URL, port)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	createFakePackage(t, root)
	capturePath := filepath.Join(root, "capture.json")
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(),
		"AFTERBURNER_HOME="+afterburnerHome,
		"AFTERBURNER_NORMAL_COPILOT_HOME="+filepath.Join(root, "normal"),
		"AFTERBURNER_COPILOT_EXECUTABLE="+fake,
		"AFTERBURNER_COPILOT_PACKAGE_ROOTS="+filepath.Join(root, "packages"),
		"AFTERBURNER_ALLOW_UNPROFILED=1",
		"AFTERBURNER_SKIP_PREFLIGHT=1",
		"AFTERBURNER_BYOMODELS_CONFIG="+configPath,
		"AFTERBURNER_TEST_PROXY_URL="+fmt.Sprintf("http://127.0.0.1:%d", port),
		"AFTERBURNER_TEST_CAPTURE="+capturePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("afterburn failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var got capturedInvocation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.ProxyReady {
		t.Fatal("BYOModels proxy was not ready when Copilot started")
	}
}

func TestChildExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher acceptance")
	}
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	bin := filepath.Join(t.TempDir(), "afterburn.exe")
	fake := filepath.Join(t.TempDir(), "fakecopilot.exe")
	build(t, goExe, bin, ".")
	build(t, goExe, fake, "../../internal/testutil/fakecopilot")
	root := t.TempDir()
	createFakePackage(t, root)
	cmd := exec.Command(bin, "--version")
	cmd.Env = append(os.Environ(),
		"AFTERBURNER_HOME="+filepath.Join(root, "afterburner"),
		"AFTERBURNER_NORMAL_COPILOT_HOME="+filepath.Join(root, "normal"),
		"AFTERBURNER_COPILOT_EXECUTABLE="+fake,
		"AFTERBURNER_COPILOT_PACKAGE_ROOTS="+filepath.Join(root, "packages"),
		"AFTERBURNER_ALLOW_UNPROFILED=1",
		"AFTERBURNER_SKIP_PREFLIGHT=1",
		"AFTERBURNER_TEST_CAPTURE="+filepath.Join(root, "capture.json"),
		"AFTERBURNER_TEST_EXIT_CODE=37",
	)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 37 {
		t.Fatalf("exit = %v, want 37", err)
	}
}

func build(t *testing.T, goExe, output, packagePath string) {
	t.Helper()
	cmd := exec.Command(goExe, "build", "-o", output, packagePath)
	cmd.Dir = filepath.Dir(filepath.Dir(filepath.Dir(output)))
	cmd.Dir, _ = os.Getwd()
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, data)
	}
}

func createFakePackage(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "packages", "1.0.83-3")
	native := filepath.Join(path, "prebuilds", testRuntimePlatform())
	if err := os.MkdirAll(native, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "app.js"), []byte("export {};"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "runtime.node"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mapTestArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch
}

func testRuntimePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32-" + mapTestArch(runtime.GOARCH)
	}
	return runtime.GOOS + "-" + mapTestArch(runtime.GOARCH)
}
