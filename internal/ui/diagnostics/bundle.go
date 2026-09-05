package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type RedactionMode string

const (
	RedactionMetadataOnly RedactionMode = "metadata-only"
	RedactionAllowlist    RedactionMode = "allowlist"
)

type BundleManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Protocol      string               `json:"protocol"`
	Revision      int                  `json:"revision"`
	BundleID      string               `json:"bundleId"`
	MetadataOnly  bool                 `json:"metadataOnly"`
	CreatedAt     time.Time            `json:"createdAt"`
	Artifacts     []ArtifactDescriptor `json:"artifacts"`
	Explanations  []Explanation        `json:"explanations,omitempty"`
}

type ArtifactDescriptor struct {
	ID             string        `json:"id"`
	Kind           string        `json:"kind"`
	Filename       string        `json:"filename,omitempty"`
	SHA256         string        `json:"sha256,omitempty"`
	SizeBytes      int64         `json:"sizeBytes"`
	Redaction      RedactionMode `json:"redaction"`
	AllowedFields  []string      `json:"allowedFields,omitempty"`
	ExcludedFields []string      `json:"excludedFields,omitempty"`
	MetadataOnly   bool          `json:"metadataOnly"`
}

type Explanation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Builder struct {
	BundleID     string
	MetadataOnly bool
	Clock        func() time.Time

	artifacts    []ArtifactDescriptor
	explanations []Explanation
}

func NewBuilder(bundleID string, metadataOnly bool) *Builder {
	return &Builder{BundleID: bundleID, MetadataOnly: metadataOnly}
}

func (b *Builder) AddJSONArtifact(id, kind string, document any, allowedFields []string, explanation string) error {
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if b.MetadataOnly {
		payload = nil
	}
	descriptor := ArtifactDescriptor{ID: id, Kind: kind, Redaction: RedactionAllowlist, AllowedFields: append([]string(nil), allowedFields...), ExcludedFields: []string{"payload", "prompt", "response", "token", "secret", "source"}, MetadataOnly: b.MetadataOnly}
	if len(payload) > 0 {
		sum := sha256.Sum256(payload)
		descriptor.SHA256 = hex.EncodeToString(sum[:])
		descriptor.SizeBytes = int64(len(payload))
		descriptor.Filename = safeName(id) + ".json"
	}
	b.artifacts = append(b.artifacts, descriptor)
	if explanation != "" {
		b.explanations = append(b.explanations, Explanation{Code: id, Message: explanation})
	}
	return nil
}

func (b *Builder) Manifest() BundleManifest {
	return BundleManifest{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, BundleID: b.BundleID, MetadataOnly: b.MetadataOnly, CreatedAt: b.now(), Artifacts: append([]ArtifactDescriptor(nil), b.artifacts...), Explanations: append([]Explanation(nil), b.explanations...)}
}

func (b *Builder) WriteManifest(directory string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "diagnostics-manifest.json")
	data, err := json.MarshalIndent(b.Manifest(), "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	return path, os.WriteFile(path, data, 0o600)
}

func (b *Builder) now() time.Time {
	if b.Clock != nil {
		return b.Clock().UTC()
	}
	return time.Now().UTC()
}

func safeName(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	var out strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "artifact"
	}
	return out.String()
}

// CounterSet and SpanRecorder intentionally accept only opaque identifiers and numeric values.
type CounterSet struct {
	mu     sync.Mutex
	values map[string]uint64
}

func (c *CounterSet) Add(opaqueID string, delta uint64) error {
	if !opaqueIdentifier(opaqueID) {
		return fmt.Errorf("counter id must be opaque")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]uint64{}
	}
	c.values[opaqueID] += delta
	return nil
}

func (c *CounterSet) Snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]uint64{}
	for key, value := range c.values {
		out[key] = value
	}
	return out
}

type Span struct {
	OpaqueID string        `json:"opaqueId"`
	Kind     string        `json:"kind"`
	Duration time.Duration `json:"-"`
	Millis   int64         `json:"durationMillis"`
}

type SpanRecorder struct {
	mu    sync.Mutex
	spans []Span
}

func (r *SpanRecorder) Record(span Span) error {
	if !opaqueIdentifier(span.OpaqueID) {
		return fmt.Errorf("span id must be opaque")
	}
	if span.Millis == 0 && span.Duration > 0 {
		span.Millis = span.Duration.Milliseconds()
	}
	span.Duration = 0
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, span)
	return nil
}

func (r *SpanRecorder) Snapshot() []Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Span(nil), r.spans...)
}

func opaqueIdentifier(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ':' {
			continue
		}
		return false
	}
	return !strings.Contains(strings.ToLower(value), "payload") && !strings.Contains(strings.ToLower(value), "prompt")
}
