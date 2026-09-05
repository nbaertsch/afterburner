package bridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	uierrors "github.com/nbaertsch/afterburner/internal/ui/errors"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type Negotiator interface {
	NegotiateUI(ctx context.Context, requested protocol.Revision, capabilities []capability.Descriptor) (protocol.Revision, []capability.Descriptor, error)
}

type Renderer interface {
	Negotiator
	OpenSurface(ctx context.Context, descriptor surface.Descriptor) (RendererSession, error)
}

type RendererSession interface {
	SurfaceID() string
	Render(ctx context.Context, tree component.Tree) error
	ApplyPatch(ctx context.Context, patch Patch) error
	HandleEvent(ctx context.Context, event Event) error
	Close(ctx context.Context) error
}

type TerminalSurface interface {
	ID() string
	Descriptor(ctx context.Context) (surface.Descriptor, error)
	Resize(ctx context.Context, columns, rows int) error
	Write(ctx context.Context, frame surface.StreamFrame) error
	Subscribe(ctx context.Context) (<-chan surface.StreamFrame, error)
	Close(ctx context.Context) error
}

type Reconciler interface {
	Diff(ctx context.Context, previous, next component.Tree) (Patch, error)
	Apply(ctx context.Context, base component.Tree, patch Patch) (component.Tree, error)
	Validate(ctx context.Context, tree component.Tree) error
}

type PolicyEngine interface {
	EvaluateEnvelope(ctx context.Context, envelope protocol.Envelope) (Decision, error)
	EvaluateAction(ctx context.Context, invocation surface.ActionInvocation) (Decision, error)
	EvaluateGrant(ctx context.Context, extensionID string, requested capability.GrantDescriptor) (Decision, error)
}

type Auditor interface {
	Record(ctx context.Context, record AuditRecord) error
}

type EventSink interface {
	Descriptor() observability.EventSinkDescriptor
	Publish(ctx context.Context, observation observability.Observation) error
}

type SDKBridge interface {
	Negotiator
	RegisterSurface(ctx context.Context, descriptor surface.Descriptor) error
	RegisterAction(ctx context.Context, surfaceID string, action surface.ActionDescriptor) error
	RegisterDataSource(ctx context.Context, surfaceID string, source surface.DataSourceDescriptor) error
	EmitEvent(ctx context.Context, event Event) error
	Render(ctx context.Context, surfaceID string, tree component.Tree) error
	Patch(ctx context.Context, surfaceID string, patch Patch) error
	Observe(ctx context.Context, observation observability.Observation) error
}

type SchemaRegistry interface {
	Register(ctx context.Context, id string, schema json.RawMessage) error
	Validate(ctx context.Context, id string, document json.RawMessage) error
	Resolve(ctx context.Context, id string) (json.RawMessage, error)
}

type PatchOperation string

const (
	PatchAdd        PatchOperation = "add"
	PatchRemove     PatchOperation = "remove"
	PatchReplace    PatchOperation = "replace"
	PatchMove       PatchOperation = "move"
	PatchCopy       PatchOperation = "copy"
	PatchTest       PatchOperation = "test"
	PatchSetProps   PatchOperation = "setProps"
	PatchBindData   PatchOperation = "bindData"
	PatchBindAction PatchOperation = "bindAction"
)

type Patch struct {
	SchemaVersion int       `json:"schemaVersion"`
	Protocol      string    `json:"protocol"`
	Revision      int       `json:"revision"`
	SurfaceID     string    `json:"surfaceId"`
	BaseRevision  uint64    `json:"baseRevision"`
	NextRevision  uint64    `json:"nextRevision"`
	Operations    []PatchOp `json:"operations"`
}

type PatchOp struct {
	Op     PatchOperation  `json:"op"`
	Path   string          `json:"path"`
	From   string          `json:"from,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	Guard  json.RawMessage `json:"guard,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

type EventPhase string

const (
	EventCapture EventPhase = "capture"
	EventTarget  EventPhase = "target"
	EventBubble  EventPhase = "bubble"
)

type Event struct {
	SchemaVersion   int             `json:"schemaVersion"`
	Protocol        string          `json:"protocol"`
	Revision        int             `json:"revision"`
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	SurfaceID       string          `json:"surfaceId"`
	ComponentID     string          `json:"componentId,omitempty"`
	ActionID        string          `json:"actionId,omitempty"`
	Phase           EventPhase      `json:"phase,omitempty"`
	Timestamp       time.Time       `json:"timestamp"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Trusted         bool            `json:"trusted"`
	PreventDefault  bool            `json:"preventDefault,omitempty"`
	StopPropagation bool            `json:"stopPropagation,omitempty"`
}

type DecisionResult string

const (
	DecisionAllow DecisionResult = "allow"
	DecisionDeny  DecisionResult = "deny"
	DecisionAudit DecisionResult = "audit"
)

type Decision struct {
	Result       DecisionResult             `json:"result"`
	Reason       string                     `json:"reason,omitempty"`
	MatchedGrant string                     `json:"matchedGrant,omitempty"`
	Error        *uierrors.ContractError    `json:"error,omitempty"`
	Obligations  []DecisionObligation       `json:"obligations,omitempty"`
	Metadata     map[string]json.RawMessage `json:"metadata,omitempty"`
}

type DecisionObligation struct {
	Type       string            `json:"type"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type AuditRecord struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Protocol      string                     `json:"protocol"`
	Revision      int                        `json:"revision"`
	ID            string                     `json:"id"`
	Type          string                     `json:"type"`
	Actor         protocol.Actor             `json:"actor"`
	SurfaceID     string                     `json:"surfaceId,omitempty"`
	EnvelopeID    string                     `json:"envelopeId,omitempty"`
	Decision      *Decision                  `json:"decision,omitempty"`
	At            time.Time                  `json:"at"`
	Attributes    map[string]json.RawMessage `json:"attributes,omitempty"`
}
