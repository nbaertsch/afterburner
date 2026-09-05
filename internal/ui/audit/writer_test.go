package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

func TestWriterRedactsAllowlistedJSONLAndRotates(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	writer := Writer{Directory: t.TempDir(), MaxBytes: 220, Clock: func() time.Time { return now }}
	secret, _ := json.Marshal("bearer token")
	resource, _ := json.Marshal("safe-resource")
	prompt, _ := json.Marshal("user prompt should not persist")
	record := bridge.AuditRecord{ID: "audit-1", Type: "policy.decision", Actor: protocol.Actor{Kind: protocol.ActorPolicy, ID: "policy-engine"}, SurfaceID: "surface-1", Decision: &bridge.Decision{Result: bridge.DecisionDeny, Reason: "secret should redact"}, At: now, Attributes: map[string]json.RawMessage{"resource": resource, "token": secret, "prompt": prompt, "unlisted": resource}}
	if err := writer.Record(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := writer.Record(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(writer.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected rotated audit files, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(writer.Directory, "ui-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "bearer") || strings.Contains(text, "prompt") || strings.Contains(text, "unlisted") || strings.Contains(text, "secret should") {
		t.Fatalf("audit record leaked disallowed content: %s", text)
	}
	if !strings.Contains(text, "safe-resource") || !strings.Contains(text, "[redacted]") {
		t.Fatalf("audit record did not retain allowlisted redacted metadata: %s", text)
	}
}
