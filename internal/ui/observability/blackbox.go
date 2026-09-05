package observability

import (
	"encoding/json"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

const (
	BlackBoxExtensionID = "black-box"
	BlackBoxSinkID      = "black-box.ui.events"
	BlackBoxEventType   = "afterburner.ui.observation"
)

type SinkRequirement string

const (
	SinkOptional SinkRequirement = "optional"
	SinkRequired SinkRequirement = "required"
)

type EventSinkDescriptor struct {
	ID              string                  `json:"id"`
	ExtensionID     string                  `json:"extensionId"`
	Requirement     SinkRequirement         `json:"requirement"`
	Capability      capability.ID           `json:"capability"`
	AcceptedKinds   []protocol.EnvelopeKind `json:"acceptedKinds"`
	Redaction       RedactionPolicy         `json:"redaction"`
	FailureBehavior string                  `json:"failureBehavior"`
}

type RedactionPolicy struct {
	Mode          string   `json:"mode"`
	DeniedFields  []string `json:"deniedFields,omitempty"`
	AllowedFields []string `json:"allowedFields,omitempty"`
	MetadataOnly  bool     `json:"metadataOnly"`
}

type Observation struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Protocol      string                     `json:"protocol"`
	Revision      int                        `json:"revision"`
	Type          string                     `json:"type"`
	SinkID        string                     `json:"sinkId"`
	EnvelopeID    string                     `json:"envelopeId,omitempty"`
	EnvelopeKind  protocol.EnvelopeKind      `json:"envelopeKind,omitempty"`
	SurfaceID     string                     `json:"surfaceId,omitempty"`
	ExtensionID   string                     `json:"extensionId,omitempty"`
	At            time.Time                  `json:"at"`
	Attributes    map[string]json.RawMessage `json:"attributes,omitempty"`
}

func BlackBoxSinkDescriptor() EventSinkDescriptor {
	return EventSinkDescriptor{
		ID:          BlackBoxSinkID,
		ExtensionID: BlackBoxExtensionID,
		Requirement: SinkOptional,
		Capability:  capability.BlackBoxEventSink,
		AcceptedKinds: []protocol.EnvelopeKind{
			protocol.EnvelopeComponentSnapshot,
			protocol.EnvelopeComponentPatch,
			protocol.EnvelopeUIEvent,
			protocol.EnvelopeLifecycle,
			protocol.EnvelopeAuditEvent,
			protocol.EnvelopeObservation,
		},
		Redaction: RedactionPolicy{
			Mode:         "metadata-only",
			MetadataOnly: true,
			DeniedFields: []string{"payload.prompt", "payload.response", "payload.toolArguments", "payload.toolResult", "payload.source"},
		},
		FailureBehavior: "ignore-when-absent",
	}
}
