package protocol

import (
	"encoding/json"
	"time"
)

const (
	Protocol              = "afterburner.ui"
	ProtocolRevision      = 1
	MinimumHostRevision   = 1
	SchemaVersion         = 1
	CompatibilityFamily   = "afterburner.ui.contract"
	CompatibilityRevision = "afterburner.ui.r1"
)

const (
	SLOFirstFrameLatencyP95       = "afterburner.ui.slo.first-frame-latency.p95"
	SLOInteractionToPatchP95      = "afterburner.ui.slo.interaction-to-patch.p95"
	SLOPatchApplyLatencyP95       = "afterburner.ui.slo.patch-apply-latency.p95"
	SLOStreamBackpressureRecovery = "afterburner.ui.slo.stream-backpressure-recovery"
	SLOAccessibilityConformance   = "afterburner.ui.slo.accessibility-conformance"
)

type Revision struct {
	Protocol        string   `json:"protocol"`
	Revision        int      `json:"revision"`
	MinimumHost     int      `json:"minimumHostRevision"`
	CompatibilityID string   `json:"compatibilityId"`
	SLOIDs          []string `json:"sloIds,omitempty"`
}

func CurrentRevision() Revision {
	return Revision{
		Protocol:        Protocol,
		Revision:        ProtocolRevision,
		MinimumHost:     MinimumHostRevision,
		CompatibilityID: CompatibilityRevision,
		SLOIDs: []string{
			SLOFirstFrameLatencyP95,
			SLOInteractionToPatchP95,
			SLOPatchApplyLatencyP95,
			SLOStreamBackpressureRecovery,
			SLOAccessibilityConformance,
		},
	}
}

type EnvelopeKind string

const (
	EnvelopeHello             EnvelopeKind = "hello"
	EnvelopeHelloResult       EnvelopeKind = "hello.result"
	EnvelopeComponentSnapshot EnvelopeKind = "component.snapshot"
	EnvelopeComponentPatch    EnvelopeKind = "component.patch"
	EnvelopeUIEvent           EnvelopeKind = "ui.event"
	EnvelopeGrantPolicy       EnvelopeKind = "grant.policy"
	EnvelopeLifecycle         EnvelopeKind = "lifecycle"
	EnvelopeAuditEvent        EnvelopeKind = "audit.event"
	EnvelopeObservation       EnvelopeKind = "observation"
	EnvelopeError             EnvelopeKind = "error"
	EnvelopeAck               EnvelopeKind = "ack"
	EnvelopeBackpressure      EnvelopeKind = "backpressure"
)

type ActorKind string

const (
	ActorHost        ActorKind = "host"
	ActorExtension   ActorKind = "extension"
	ActorRenderer    ActorKind = "renderer"
	ActorSurface     ActorKind = "surface"
	ActorSDK         ActorKind = "sdk"
	ActorBlackBox    ActorKind = "black-box"
	ActorPolicy      ActorKind = "policy"
	ActorReconciler  ActorKind = "reconciler"
	ActorTerminal    ActorKind = "terminal"
	ActorUnspecified ActorKind = "unspecified"
)

type Actor struct {
	Kind ActorKind `json:"kind"`
	ID   string    `json:"id"`
}

type Envelope struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Protocol       string          `json:"protocol"`
	Revision       int             `json:"revision"`
	SessionID      string          `json:"sessionId,omitempty"`
	ExtensionID    string          `json:"extensionId,omitempty"`
	MessageID      string          `json:"messageId,omitempty"`
	ID             string          `json:"id,omitempty"`
	CorrelationID  string          `json:"correlationId,omitempty"`
	CausationID    string          `json:"causationId,omitempty"`
	Epoch          uint64          `json:"epoch,omitempty"`
	Generation     uint64          `json:"generation,omitempty"`
	Sequence       uint64          `json:"sequence,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
	Kind           EnvelopeKind    `json:"kind"`
	Source         Actor           `json:"source"`
	Target         Actor           `json:"target"`
	Compatibility  []string        `json:"compatibility,omitempty"`
	ContentType    string          `json:"contentType,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	Auth           *AuthHeader     `json:"auth,omitempty"`
	Ack            *SequenceAck    `json:"ack,omitempty"`
	Retry          *RetryPolicy    `json:"retry,omitempty"`
	Backpressure   *Backpressure   `json:"backpressure,omitempty"`
	Extensions     map[string]any  `json:"extensions,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

type AuthHeader struct {
	Algorithm string    `json:"algorithm"`
	KeyID     string    `json:"keyId"`
	Epoch     uint64    `json:"epoch"`
	Nonce     string    `json:"nonce"`
	Timestamp time.Time `json:"timestamp"`
	Signature string    `json:"signature,omitempty"`
}

type SequenceAck struct {
	Epoch       uint64   `json:"epoch"`
	HighWater   uint64   `json:"highWater"`
	Received    []uint64 `json:"received,omitempty"`
	Missing     []uint64 `json:"missing,omitempty"`
	Contiguous  bool     `json:"contiguous"`
	Duplicate   bool     `json:"duplicate,omitempty"`
	OutOfWindow bool     `json:"outOfWindow,omitempty"`
}

type RetryPolicy struct {
	Retryable       bool          `json:"retryable"`
	After           time.Duration `json:"-"`
	AfterMillis     int64         `json:"afterMillis,omitempty"`
	MaxAttempts     int           `json:"maxAttempts,omitempty"`
	IdempotencySafe bool          `json:"idempotencySafe,omitempty"`
	Reason          string        `json:"reason,omitempty"`
}

type Backpressure struct {
	Active           bool   `json:"active"`
	Reason           string `json:"reason,omitempty"`
	QueueDepth       int    `json:"queueDepth,omitempty"`
	QueueLimit       int    `json:"queueLimit,omitempty"`
	RetryAfterMillis int64  `json:"retryAfterMillis,omitempty"`
}

type ErrorCode string

const (
	ErrorInvalidFrame         ErrorCode = "ui.transport.invalidFrame"
	ErrorFrameTooLarge        ErrorCode = "ui.transport.frameTooLarge"
	ErrorInvalidJSON          ErrorCode = "ui.transport.invalidJSON"
	ErrorJSONTooDeep          ErrorCode = "ui.transport.jsonTooDeep"
	ErrorInvalidEnvelope      ErrorCode = "ui.transport.invalidEnvelope"
	ErrorUnsupportedRevision  ErrorCode = "ui.transport.unsupportedRevision"
	ErrorDowngradeRejected    ErrorCode = "ui.transport.downgradeRejected"
	ErrorAuthenticationFailed ErrorCode = "ui.security.authenticationFailed"
	ErrorReplayDetected       ErrorCode = "ui.security.replayDetected"
	ErrorStaleSequence        ErrorCode = "ui.security.staleSequence"
	ErrorStaleEpoch           ErrorCode = "ui.security.staleEpoch"
	ErrorStaleGeneration      ErrorCode = "ui.security.staleGeneration"
	ErrorIdempotencyConflict  ErrorCode = "ui.security.idempotencyConflict"
	ErrorBackpressure         ErrorCode = "ui.transport.backpressure"
	ErrorDeadlineExceeded     ErrorCode = "ui.transport.deadlineExceeded"
	ErrorCanceled             ErrorCode = "ui.transport.canceled"
)

type StructuredError struct {
	Code        ErrorCode         `json:"code"`
	Message     string            `json:"message"`
	Target      string            `json:"target,omitempty"`
	Recoverable bool              `json:"recoverable"`
	Retry       *RetryPolicy      `json:"retry,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

func (e StructuredError) Error() string {
	if e.Target == "" {
		return string(e.Code) + ": " + e.Message
	}
	return string(e.Code) + ": " + e.Message + " (" + e.Target + ")"
}

func NewEnvelope(kind EnvelopeKind, id string, source, target Actor, payload json.RawMessage) Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Protocol:      Protocol,
		Revision:      ProtocolRevision,
		MessageID:     id,
		ID:            id,
		Kind:          kind,
		Source:        source,
		Target:        target,
		Timestamp:     time.Now().UTC(),
		Compatibility: []string{CompatibilityRevision},
		ContentType:   "application/json",
		Payload:       payload,
	}
}

func (e Envelope) EffectiveMessageID() string {
	if e.MessageID != "" {
		return e.MessageID
	}
	return e.ID
}

func SupportedEnvelopeKinds() []EnvelopeKind {
	return []EnvelopeKind{
		EnvelopeComponentSnapshot,
		EnvelopeComponentPatch,
		EnvelopeUIEvent,
		EnvelopeGrantPolicy,
		EnvelopeLifecycle,
		EnvelopeAuditEvent,
		EnvelopeObservation,
	}
}

func SupportedTransportEnvelopeKinds() []EnvelopeKind {
	return []EnvelopeKind{
		EnvelopeHello,
		EnvelopeHelloResult,
		EnvelopeComponentSnapshot,
		EnvelopeComponentPatch,
		EnvelopeUIEvent,
		EnvelopeGrantPolicy,
		EnvelopeLifecycle,
		EnvelopeAuditEvent,
		EnvelopeObservation,
		EnvelopeError,
		EnvelopeAck,
		EnvelopeBackpressure,
	}
}

func RetryAfter(d time.Duration, attempts int, idempotencySafe bool, reason string) RetryPolicy {
	return RetryPolicy{Retryable: true, After: d, AfterMillis: d.Milliseconds(), MaxAttempts: attempts, IdempotencySafe: idempotencySafe, Reason: reason}
}
