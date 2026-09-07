package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want Route
	}{
		{"zero", nil, Route{Command: "run", Args: []string{}}},
		{"ordinary", []string{"--version"}, Route{Command: "run", Args: []string{"--version"}}},
		{"unknown command", []string{"prompt", "hello"}, Route{Command: "run", Args: []string{"prompt", "hello"}}},
		{"management", []string{"doctor", "--json"}, Route{Command: "doctor", Args: []string{"--json"}}},
		{"ui", []string{"ui", "doctor"}, Route{Command: "ui", Args: []string{"doctor"}}},
		{"explicit run", []string{"run", "install"}, Route{Command: "run", Args: []string{"install"}, ForcedPassthrough: true}},
		{"terminator", []string{"--", "update", ""}, Route{Command: "run", Args: []string{"update", ""}, ForcedPassthrough: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Classify(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCoreUpdateStatusWarningReportsFailedReplacement(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "core-update-status.json"), []byte(`{"schemaVersion":1,"status":"failed","completedAt":"2026-01-02T03:04:05Z","error":"Access is denied"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	warning, ok := coreUpdateStatusWarning(root)
	if !ok || !strings.Contains(warning, "previous Afterburner core update failed") || !strings.Contains(warning, "Access is denied") {
		t.Fatalf("warning ok=%t value=%q", ok, warning)
	}
}

func TestUICatalogCommand(t *testing.T) {
	var stdout bytes.Buffer
	code, err := Run(context.Background(), []string{"ui", "catalog"}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("Run returned code=%d err=%v output=%s", code, err, stdout.String())
	}
	for _, want := range []string{"Afterburner UI afterburner.ui revision 1", "Components", "Surfaces", "Capabilities", "ui.observability.black-box.sink"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("catalog output missing %q: %q", want, stdout.String())
		}
	}

	stdout.Reset()
	code, err = Run(context.Background(), []string{"ui", "catalog", "--json"}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("JSON catalog returned code=%d err=%v output=%s", code, err, stdout.String())
	}
	for _, want := range []string{"\"components\"", "\"surfaces\"", "\"capabilities\""} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("catalog JSON missing %q: %q", want, stdout.String())
		}
	}
}

func TestUIValidateManifestCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte("export {};"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "afterburner.json")
	manifest := `{"schemaVersion":1,"id":"sample-ui","displayName":"Sample UI","visibility":"private","requires":{"afterburner":"1"},"runtime":{"execution":"in-process","entrypoint":"extension.mjs"},"ui":{"protocol":"afterburner.ui","revision":1,"surfaces":[{"id":"panel","kind":"panel"}]}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	code, err := Run(context.Background(), []string{"ui", "validate-manifest", manifestPath}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("Run returned code=%d err=%v output=%s", code, err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "OK") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestUIRenderFixtureCommand(t *testing.T) {
	var stdout bytes.Buffer
	code, err := Run(context.Background(), []string{"ui", "render-fixture", "black-box-certification"}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("Run returned code=%d err=%v output=%s", code, err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Metadata-only Observability") {
		t.Fatalf("unexpected fixture output: %q", stdout.String())
	}
	stdout.Reset()
	code, err = Run(context.Background(), []string{"ui", "render-fixture", "component-gallery"}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("gallery returned code=%d err=%v output=%s", code, err, stdout.String())
	}
	for _, want := range []string{"Layout and structure", "Status and feedback", "Collections and data", "Inputs and actions"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("gallery output missing %q: %q", want, stdout.String())
		}
	}
}

func TestExtractLaunchOptions(t *testing.T) {
	options, forwarded, err := extractLaunchOptions([]string{
		"--safe-mode", "--resume=abc", "--disable-extension", "black-box", "-p", "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.safeMode || !reflect.DeepEqual(options.disabledExtensions, []string{"black-box"}) {
		t.Fatalf("options = %#v", options)
	}
	if !reflect.DeepEqual(forwarded, []string{"--resume=abc", "-p", "hello"}) {
		t.Fatalf("forwarded = %#v", forwarded)
	}
}
