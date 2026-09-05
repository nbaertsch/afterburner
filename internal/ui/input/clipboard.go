package input

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

var (
	ErrClipboardDenied   = errors.New("clipboard operation denied")
	ErrSecretClipboard   = errors.New("secret fields cannot be copied or pasted without explicit grant")
	ErrClipboardTooLarge = errors.New("clipboard payload exceeds limit")
)

type ClipboardOperation string

const (
	ClipboardRead  ClipboardOperation = "read"
	ClipboardWrite ClipboardOperation = "write"
)

type ClipboardBackend interface {
	ReadText(ctx context.Context) (string, error)
	WriteText(ctx context.Context, text string) error
}

type ClipboardGrant struct {
	Operation   ClipboardOperation `json:"operation"`
	Capability  capability.ID      `json:"capability"`
	SurfaceID   string             `json:"surfaceId,omitempty"`
	ComponentID string             `json:"componentId,omitempty"`
	Secret      bool               `json:"secret,omitempty"`
	Bytes       int                `json:"bytes,omitempty"`
}

type ClipboardGrantDecision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Audit   bool   `json:"audit,omitempty"`
}

type ClipboardGrantEvaluator interface {
	EvaluateClipboard(ctx context.Context, grant ClipboardGrant) (ClipboardGrantDecision, error)
}

type ClipboardAuditSink interface {
	Record(ctx context.Context, record bridge.AuditRecord) error
}

type ClipboardBrokerConfig struct {
	Backend       ClipboardBackend
	Grants        ClipboardGrantEvaluator
	Audit         ClipboardAuditSink
	MaxBytes      int
	AllowSecretIO bool
	Actor         protocol.Actor
	Now           func() time.Time
}

type ClipboardBroker struct {
	cfg ClipboardBrokerConfig
	seq uint64
}

type ClipboardRequest struct {
	SurfaceID   string `json:"surfaceId,omitempty"`
	ComponentID string `json:"componentId,omitempty"`
	Text        string `json:"text,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func NewClipboardBroker(cfg ClipboardBrokerConfig) *ClipboardBroker {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 * 1024
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Actor.Kind == "" {
		cfg.Actor = protocol.Actor{Kind: protocol.ActorHost, ID: "ui-input"}
	}
	return &ClipboardBroker{cfg: cfg}
}

func (b *ClipboardBroker) Copy(ctx context.Context, req ClipboardRequest) error {
	if req.Secret && !b.cfg.AllowSecretIO {
		b.audit(ctx, "ui.clipboard.copy.denied", req, "secret-copy")
		return ErrSecretClipboard
	}
	if len(req.Text) > b.cfg.MaxBytes {
		b.audit(ctx, "ui.clipboard.copy.denied", req, "too-large")
		return ErrClipboardTooLarge
	}
	if err := b.authorize(ctx, ClipboardGrant{Operation: ClipboardWrite, Capability: capability.DataWrite, SurfaceID: req.SurfaceID, ComponentID: req.ComponentID, Secret: req.Secret, Bytes: len(req.Text)}); err != nil {
		b.audit(ctx, "ui.clipboard.copy.denied", req, err.Error())
		return err
	}
	if b.cfg.Backend == nil {
		b.audit(ctx, "ui.clipboard.copy", req, "no-backend")
		return nil
	}
	if err := b.cfg.Backend.WriteText(ctx, req.Text); err != nil {
		b.audit(ctx, "ui.clipboard.copy.failed", req, err.Error())
		return err
	}
	b.audit(ctx, "ui.clipboard.copy", req, "allowed")
	return nil
}

func (b *ClipboardBroker) Paste(ctx context.Context, req ClipboardRequest) (string, error) {
	if req.Secret && !b.cfg.AllowSecretIO {
		b.audit(ctx, "ui.clipboard.paste.denied", req, "secret-paste")
		return "", ErrSecretClipboard
	}
	if err := b.authorize(ctx, ClipboardGrant{Operation: ClipboardRead, Capability: capability.DataRead, SurfaceID: req.SurfaceID, ComponentID: req.ComponentID, Secret: req.Secret}); err != nil {
		b.audit(ctx, "ui.clipboard.paste.denied", req, err.Error())
		return "", err
	}
	if b.cfg.Backend == nil {
		b.audit(ctx, "ui.clipboard.paste", req, "no-backend")
		return "", nil
	}
	text, err := b.cfg.Backend.ReadText(ctx)
	if err != nil {
		b.audit(ctx, "ui.clipboard.paste.failed", req, err.Error())
		return "", err
	}
	if len(text) > b.cfg.MaxBytes {
		b.audit(ctx, "ui.clipboard.paste.denied", req, "too-large")
		return "", ErrClipboardTooLarge
	}
	b.audit(ctx, "ui.clipboard.paste", ClipboardRequest{SurfaceID: req.SurfaceID, ComponentID: req.ComponentID, Secret: req.Secret, Text: ""}, "allowed")
	return text, nil
}

func (b *ClipboardBroker) authorize(ctx context.Context, grant ClipboardGrant) error {
	if b.cfg.Grants == nil {
		return nil
	}
	decision, err := b.cfg.Grants.EvaluateClipboard(ctx, grant)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		if decision.Reason == "" {
			decision.Reason = "grant-denied"
		}
		return errors.Join(ErrClipboardDenied, errors.New(decision.Reason))
	}
	return nil
}

func (b *ClipboardBroker) audit(ctx context.Context, typ string, req ClipboardRequest, reason string) {
	if b.cfg.Audit == nil {
		return
	}
	b.seq++
	attrs, _ := json.Marshal(map[string]any{"componentId": req.ComponentID, "reason": reason, "secret": req.Secret, "bytes": len(req.Text)})
	_ = b.cfg.Audit.Record(ctx, bridge.AuditRecord{SchemaVersion: 1, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, ID: "clipboard-" + strconvFormatUint(b.seq), Type: typ, Actor: b.cfg.Actor, SurfaceID: req.SurfaceID, At: b.now(), Attributes: map[string]json.RawMessage{"clipboard": attrs}})
}

func (b *ClipboardBroker) now() time.Time {
	if b.cfg.Now != nil {
		return b.cfg.Now()
	}
	return time.Now()
}

func strconvFormatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
