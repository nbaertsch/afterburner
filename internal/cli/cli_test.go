package cli

import (
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

func TestReplacementRegistrySnapshotAcceptsOldAndNewProtocols(t *testing.T) {
	oldArgs := []string{"replace", "--parent", "1", "--source", "source", "--target", "target", "--previous", "previous"}
	if snapshot, ok := replacementRegistrySnapshot(oldArgs); !ok || snapshot != "" {
		t.Fatalf("old protocol = %q, %t", snapshot, ok)
	}
	newArgs := append(append([]string(nil), oldArgs...), "--registry-snapshot", "snapshot")
	if snapshot, ok := replacementRegistrySnapshot(newArgs); !ok || snapshot != "snapshot" {
		t.Fatalf("new protocol = %q, %t", snapshot, ok)
	}
	if _, ok := replacementRegistrySnapshot(append(oldArgs, "--unknown", "value")); ok {
		t.Fatal("unknown replacement protocol was accepted")
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

func TestDisabledExtensionsForRuntimeIncludesOpenAIServerAlias(t *testing.T) {
	got := disabledExtensionsForRuntime([]string{"copilot-openai", "black-box"})
	want := []string{"openai-server", "copilot-openai", "black-box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled extensions = %#v", got)
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
