package errors

import "fmt"

type Code string

const (
	InvalidEnvelope       Code = "ui.invalidEnvelope"
	UnsupportedRevision   Code = "ui.unsupportedRevision"
	UnknownComponentKind  Code = "ui.unknownComponentKind"
	InvalidComponentTree  Code = "ui.invalidComponentTree"
	InvalidPatch          Code = "ui.invalidPatch"
	PolicyDenied          Code = "ui.policyDenied"
	GrantRequired         Code = "ui.grantRequired"
	QuotaExceeded         Code = "ui.quotaExceeded"
	ActionRejected        Code = "ui.actionRejected"
	ActionFailed          Code = "ui.actionFailed"
	RenderFailed          Code = "ui.renderFailed"
	SurfaceUnavailable    Code = "ui.surfaceUnavailable"
	StreamClosed          Code = "ui.streamClosed"
	StreamBackpressured   Code = "ui.streamBackpressured"
	DataUnavailable       Code = "ui.dataUnavailable"
	LocalizationMissing   Code = "ui.localizationMissing"
	AccessibilityInvalid  Code = "ui.accessibilityInvalid"
	ObservabilityOptional Code = "ui.observabilityOptional"
)

type ContractError struct {
	Code        Code              `json:"code"`
	Message     string            `json:"message"`
	Target      string            `json:"target,omitempty"`
	Recoverable bool              `json:"recoverable"`
	Details     map[string]string `json:"details,omitempty"`
}

func (e ContractError) Error() string {
	if e.Target == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Target)
}

func PublicCodes() []Code {
	return []Code{
		InvalidEnvelope, UnsupportedRevision, UnknownComponentKind, InvalidComponentTree,
		InvalidPatch, PolicyDenied, GrantRequired, QuotaExceeded, ActionRejected,
		ActionFailed, RenderFailed, SurfaceUnavailable, StreamClosed, StreamBackpressured,
		DataUnavailable, LocalizationMissing, AccessibilityInvalid, ObservabilityOptional,
	}
}
