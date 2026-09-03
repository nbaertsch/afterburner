package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type capturedInvocation struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func TestNativeArgumentFidelity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher acceptance")
	}
	goExe := filepath.Join(os.Getenv("USERPROFILE"), ".afterburner", "toolchains", "go1.27.1", "bin", "go.exe")
	if _, err := os.Stat(goExe); err != nil {
		goExe = "go"
	}
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

func TestChildExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher acceptance")
	}
	goExe := filepath.Join(os.Getenv("USERPROFILE"), ".afterburner", "toolchains", "go1.27.1", "bin", "go.exe")
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
