package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

const DefaultMaxBytes int64 = 8 << 20

var AllowedAttributeKeys = map[string]bool{
	"hostId": true, "surfaceId": true, "instanceId": true, "extensionId": true, "capability": true,
	"resource": true, "grantId": true, "grantEpoch": true, "registryEpoch": true, "policyVersion": true,
	"reason": true, "state": true, "result": true, "opaqueId": true, "counter": true,
}

var sensitiveFragments = []string{"token", "secret", "password", "credential", "payload", "prompt", "response", "tool", "content"}

type Event struct {
	SchemaVersion int                `json:"schemaVersion"`
	Protocol      string             `json:"protocol"`
	Revision      int                `json:"revision"`
	ID            string             `json:"id"`
	Type          string             `json:"type"`
	ActorKind     protocol.ActorKind `json:"actorKind"`
	ActorID       string             `json:"actorId"`
	SurfaceID     string             `json:"surfaceId,omitempty"`
	EnvelopeID    string             `json:"envelopeId,omitempty"`
	Decision      string             `json:"decision,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	At            time.Time          `json:"at"`
	Attributes    map[string]string  `json:"attributes,omitempty"`
}

type Writer struct {
	Directory string
	Filename  string
	MaxBytes  int64
	Clock     func() time.Time
}

func (w Writer) Record(ctx context.Context, record bridge.AuditRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	event := sanitize(record, w.now())
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := w.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := w.rotateIfNeeded(path, int64(len(data))); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}

func (w Writer) path() string {
	name := w.Filename
	if name == "" {
		name = "ui-audit.jsonl"
	}
	return filepath.Join(w.Directory, name)
}

func (w Writer) rotateIfNeeded(path string, incoming int64) error {
	maxBytes := w.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) || info.Size()+incoming <= maxBytes {
		return nil
	}
	if err != nil {
		return err
	}
	rotated := strings.TrimSuffix(path, filepath.Ext(path)) + "." + w.now().Format("20060102T150405.000000000Z") + filepath.Ext(path)
	if err := os.Rename(path, rotated); err != nil {
		return err
	}
	return os.Chmod(rotated, 0o600)
}

func sanitize(record bridge.AuditRecord, now time.Time) Event {
	if record.At.IsZero() {
		record.At = now
	}
	event := Event{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, ID: record.ID, Type: record.Type, ActorKind: record.Actor.Kind, ActorID: record.Actor.ID, SurfaceID: record.SurfaceID, EnvelopeID: record.EnvelopeID, At: record.At.UTC(), Attributes: map[string]string{}}
	if record.Decision != nil {
		event.Decision = string(record.Decision.Result)
		event.Reason = redactString(record.Decision.Reason)
	}
	for key, raw := range record.Attributes {
		if !AllowedAttributeKeys[key] || sensitiveKey(key) {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			event.Attributes[key] = redactString(typed)
		case float64, bool:
			event.Attributes[key] = fmt.Sprint(typed)
		default:
			encoded, _ := json.Marshal(typed)
			event.Attributes[key] = redactString(string(encoded))
		}
	}
	if len(event.Attributes) == 0 {
		event.Attributes = nil
	}
	return event
}

func (w Writer) now() time.Time {
	if w.Clock != nil {
		return w.Clock().UTC()
	}
	return time.Now().UTC()
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if lower == "source" {
		return true
	}
	for _, part := range sensitiveFragments {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func redactString(value string) string {
	lower := strings.ToLower(value)
	for _, part := range sensitiveFragments {
		if strings.Contains(lower, part) {
			return "[redacted]"
		}
	}
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
