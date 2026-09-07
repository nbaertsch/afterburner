package surface

import (
	"encoding/json"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
)

type Kind string

type CatalogEntry struct {
	Kind        Kind                 `json:"kind"`
	Stability   capability.Stability `json:"stability"`
	Description string               `json:"description"`
}

const (
	KindTerminal       Kind = "terminal"
	KindModal          Kind = "modal"
	KindPanel          Kind = "panel"
	KindInline         Kind = "inline"
	KindStatusLine     Kind = "statusLine"
	KindCommandPalette Kind = "commandPalette"
	KindOverlay        Kind = "overlay"
)

func PublicCatalog() []CatalogEntry {
	return []CatalogEntry{
		{KindTerminal, capability.StabilityStable, "Terminal-backed interactive surface."},
		{KindModal, capability.StabilityStable, "Host-managed modal surface."},
		{KindPanel, capability.StabilityStable, "Persistent side-panel surface."},
		{KindInline, capability.StabilityStable, "Inline embedded surface."},
		{KindStatusLine, capability.StabilityStable, "Compact status-line surface."},
		{KindCommandPalette, capability.StabilityStable, "Command palette surface."},
		{KindOverlay, capability.StabilityStable, "Overlay surface."},
	}
}

type LifecycleState string

const (
	StateDeclared    LifecycleState = "declared"
	StateCreated     LifecycleState = "created"
	StateMounted     LifecycleState = "mounted"
	StateRendering   LifecycleState = "rendering"
	StateInteractive LifecycleState = "interactive"
	StateSuspended   LifecycleState = "suspended"
	StateDisposing   LifecycleState = "disposing"
	StateDisposed    LifecycleState = "disposed"
	StateFailed      LifecycleState = "failed"
)

type Descriptor struct {
	ID                   string                     `json:"id"`
	Kind                 Kind                       `json:"kind"`
	OwnerExtensionID     string                     `json:"ownerExtensionId,omitempty"`
	Title                string                     `json:"title,omitempty"`
	Lifecycle            LifecycleState             `json:"lifecycle"`
	RequiredCapabilities []capability.ID            `json:"requiredCapabilities,omitempty"`
	SupportedComponents  []component.Kind           `json:"supportedComponents,omitempty"`
	Actions              []ActionDescriptor         `json:"actions,omitempty"`
	DataSources          []DataSourceDescriptor     `json:"dataSources,omitempty"`
	Streams              []StreamDescriptor         `json:"streams,omitempty"`
	CreatedAt            time.Time                  `json:"createdAt,omitempty"`
	Metadata             map[string]json.RawMessage `json:"metadata,omitempty"`
}

type LifecycleEvent struct {
	SurfaceID     string         `json:"surfaceId"`
	PreviousState LifecycleState `json:"previousState,omitempty"`
	State         LifecycleState `json:"state"`
	Reason        string         `json:"reason,omitempty"`
	At            time.Time      `json:"at"`
}

type ActionEffect string

const (
	ActionRead     ActionEffect = "read"
	ActionWrite    ActionEffect = "write"
	ActionExecute  ActionEffect = "execute"
	ActionNavigate ActionEffect = "navigate"
	ActionDismiss  ActionEffect = "dismiss"
)

type ActionDescriptor struct {
	ID                   string              `json:"id"`
	Title                string              `json:"title"`
	Description          string              `json:"description,omitempty"`
	Effect               ActionEffect        `json:"effect"`
	InputSchema          json.RawMessage     `json:"inputSchema,omitempty"`
	OutputSchema         json.RawMessage     `json:"outputSchema,omitempty"`
	RequiredCapabilities []capability.ID     `json:"requiredCapabilities,omitempty"`
	Confirmation         *ConfirmationPolicy `json:"confirmation,omitempty"`
	Timeout              time.Duration       `json:"timeout,omitempty"`
}

type ConfirmationPolicy struct {
	Required bool   `json:"required"`
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// ActionInvocation identifies the owning extension independently from the
// surface and action IDs. ActionInvoke grants use the surface ID as the base
// resource; callers can scope a grant to one action with "surfaceId/actionId".
type ActionInvocation struct {
	ID               string          `json:"id"`
	ActionID         string          `json:"actionId"`
	SurfaceID        string          `json:"surfaceId"`
	OwnerExtensionID string          `json:"ownerExtensionId,omitempty"`
	ComponentID      string          `json:"componentId,omitempty"`
	Parameters       json.RawMessage `json:"parameters,omitempty"`
	CorrelationID    string          `json:"correlationId,omitempty"`
	RequestedAt      time.Time       `json:"requestedAt"`
}

type ActionResult struct {
	InvocationID string          `json:"invocationId"`
	Status       ResultStatus    `json:"status"`
	Output       json.RawMessage `json:"output,omitempty"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	CompletedAt  time.Time       `json:"completedAt"`
}

type ResultStatus string

const (
	ResultSucceeded ResultStatus = "succeeded"
	ResultRejected  ResultStatus = "rejected"
	ResultFailed    ResultStatus = "failed"
	ResultCancelled ResultStatus = "cancelled"
)

type DataSourceKind string

const (
	DataStatic       DataSourceKind = "static"
	DataQuery        DataSourceKind = "query"
	DataMutation     DataSourceKind = "mutation"
	DataSubscription DataSourceKind = "subscription"
	DataStream       DataSourceKind = "stream"
)

type DataSourceDescriptor struct {
	ID              string          `json:"id"`
	Kind            DataSourceKind  `json:"kind"`
	Schema          json.RawMessage `json:"schema,omitempty"`
	Capabilities    []capability.ID `json:"capabilities,omitempty"`
	Retention       RetentionPolicy `json:"retention,omitempty"`
	RefreshInterval time.Duration   `json:"refreshInterval,omitempty"`
}

type RetentionPolicy struct {
	Class      string        `json:"class,omitempty"`
	MaxAge     time.Duration `json:"maxAge,omitempty"`
	MaxRecords int64         `json:"maxRecords,omitempty"`
}

type DataPage struct {
	SourceID   string          `json:"sourceId"`
	Items      json.RawMessage `json:"items"`
	Cursor     string          `json:"cursor,omitempty"`
	NextCursor string          `json:"nextCursor,omitempty"`
	Complete   bool            `json:"complete"`
}

type StreamEncoding string

const (
	StreamUTF8  StreamEncoding = "utf8"
	StreamJSON  StreamEncoding = "json"
	StreamVT    StreamEncoding = "vt"
	StreamBytes StreamEncoding = "bytes"
)

type StreamLifecycle string

const (
	StreamOpening       StreamLifecycle = "opening"
	StreamOpen          StreamLifecycle = "open"
	StreamDraining      StreamLifecycle = "draining"
	StreamClosed        StreamLifecycle = "closed"
	StreamErrored       StreamLifecycle = "errored"
	StreamBackpressured StreamLifecycle = "backpressured"
)

type StreamDescriptor struct {
	ID                 string         `json:"id"`
	Encoding           StreamEncoding `json:"encoding"`
	Readable           bool           `json:"readable"`
	Writable           bool           `json:"writable"`
	Backpressure       string         `json:"backpressure,omitempty"`
	Replay             string         `json:"replay,omitempty"`
	MaxFrameBytes      int64          `json:"maxFrameBytes,omitempty"`
	RequiredCapability capability.ID  `json:"requiredCapability,omitempty"`
}

type StreamFrame struct {
	StreamID   string          `json:"streamId"`
	Sequence   uint64          `json:"sequence"`
	Lifecycle  StreamLifecycle `json:"lifecycle"`
	Data       json.RawMessage `json:"data,omitempty"`
	Encoding   StreamEncoding  `json:"encoding,omitempty"`
	WindowSize int64           `json:"windowSize,omitempty"`
	ErrorCode  string          `json:"errorCode,omitempty"`
	At         time.Time       `json:"at"`
}
