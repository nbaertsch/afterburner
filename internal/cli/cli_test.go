package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nbaertsch/afterburner/internal/preflight"
	"github.com/nbaertsch/afterburner/internal/telemetry"
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

func TestCompatibilityRetryRequestsExplicitDeepValidation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AFTERBURNER_HOME", root)
	var stdout bytes.Buffer
	code, err := runCompatibility([]string{"retry"}, Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err != nil || code != 0 {
		t.Fatalf("retry returned code=%d err=%v", code, err)
	}
	if !strings.Contains(stdout.String(), "deep validation") {
		t.Fatalf("retry output = %q", stdout.String())
	}
	if err := preflight.ValidateStored(root); !os.IsNotExist(err) {
		t.Fatalf("retry unexpectedly created or retained a tuple: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "deep-validation-requested")); err != nil {
		t.Fatalf("retry did not request explicit deep validation: %v", err)
	}
}

func TestRunDrainsAcceptedTelemetryBeforeReturning(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AFTERBURNER_HOME", t.TempDir())
	telemetry.Record(root, "launch.completed", map[string]any{"exitCode": 0})

	code, err := Run(context.Background(), []string{"version"}, Options{
		Version: "test",
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run returned code=%d err=%v", code, err)
	}

	file, err := os.Open(filepath.Join(root, "state", "launcher.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing final telemetry event: %v", scanner.Err())
	}
	var event telemetry.Event
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "launch.completed" {
		t.Fatalf("event type = %q", event.Type)
	}
}

func TestCLIProcessPersistsFinalTelemetryBeforeExit(t *testing.T) {
	if os.Getenv("GO_WANT_CLI_TELEMETRY_HELPER") == "1" {
		telemetry.Record(os.Getenv("AFTERBURNER_TEST_TELEMETRY_ROOT"), "launch.completed", map[string]any{"exitCode": 0})
		code, err := Run(context.Background(), []string{"version"}, Options{
			Version: "test",
			Stdout:  &bytes.Buffer{},
			Stderr:  &bytes.Buffer{},
		})
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)
	}

	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestCLIProcessPersistsFinalTelemetryBeforeExit")
	command.Env = append(os.Environ(),
		"GO_WANT_CLI_TELEMETRY_HELPER=1",
		"AFTERBURNER_TEST_TELEMETRY_ROOT="+root,
		"AFTERBURNER_HOME="+t.TempDir(),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "state", "launcher.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var event telemetry.Event
	if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "launch.completed" {
		t.Fatalf("event type = %q", event.Type)
	}
}

func TestReplacementRegistrySnapshotAcceptsOldAndNewProtocols(t *testing.T) {
	oldArgs := []string{"replace", "--parent", "1", "--source", "source", "--target", "target", "--previous", "previous"}
	if snapshot, success, ok := replacementRegistrySnapshots(oldArgs); !ok || snapshot != "" || success != "" {
		t.Fatalf("old protocol = %q, %q, %t", snapshot, success, ok)
	}
	newArgs := append(append([]string(nil), oldArgs...), "--registry-snapshot", "snapshot")
	if snapshot, success, ok := replacementRegistrySnapshots(newArgs); !ok || snapshot != "snapshot" || success != "" {
		t.Fatalf("new protocol = %q, %q, %t", snapshot, success, ok)
	}
	rollbackArgs := append(append([]string(nil), newArgs...), "--success-registry-snapshot", "rollback")
	if snapshot, success, ok := replacementRegistrySnapshots(rollbackArgs); !ok || snapshot != "snapshot" || success != "rollback" {
		t.Fatalf("rollback protocol = %q, %q, %t", snapshot, success, ok)
	}
	if _, _, ok := replacementRegistrySnapshots(append(oldArgs, "--unknown", "value")); ok {
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
