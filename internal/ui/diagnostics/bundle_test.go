package diagnostics

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMetadataOnlyBundleOmitsPayloadHashesAndExplainsRedaction(t *testing.T) {
	builder := NewBuilder("opaque-bundle-1", true)
	builder.Clock = func() time.Time { return time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC) }
	if err := builder.AddJSONArtifact("audit-summary", "audit", map[string]any{"payload": "TOPSECRET", "decision": "deny"}, []string{"decision"}, "payload excluded by metadata-only mode"); err != nil {
		t.Fatal(err)
	}
	manifest := builder.Manifest()
	if !manifest.MetadataOnly || len(manifest.Artifacts) != 1 || manifest.Artifacts[0].SHA256 != "" || manifest.Artifacts[0].SizeBytes != 0 || !manifest.Artifacts[0].MetadataOnly {
		t.Fatalf("unexpected metadata-only manifest: %#v", manifest)
	}
	if len(manifest.Explanations) != 1 || !strings.Contains(manifest.Explanations[0].Message, "metadata-only") {
		t.Fatalf("missing explainability: %#v", manifest.Explanations)
	}
	path, err := builder.WriteManifest(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "TOPSECRET") {
		t.Fatalf("diagnostics manifest leaked payload content: %s", data)
	}
}

func TestMetricsRejectContentIdentifiers(t *testing.T) {
	var counters CounterSet
	if err := counters.Add("opaque-counter-1", 2); err != nil {
		t.Fatal(err)
	}
	if err := counters.Add("prompt:hello", 1); err == nil {
		t.Fatal("expected prompt-like counter id to be rejected")
	}
	var spans SpanRecorder
	if err := spans.Record(Span{OpaqueID: "opaque-span-1", Kind: "policy", Duration: 15 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := spans.Snapshot(); len(got) != 1 || got[0].Millis != 15 {
		encoded, _ := json.Marshal(got)
		t.Fatalf("span snapshot = %s", encoded)
	}
}
