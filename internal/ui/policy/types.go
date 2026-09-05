package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	uierrors "github.com/nbaertsch/afterburner/internal/ui/errors"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

var ErrPolicyDenied = errors.New("policy denied")

type Action string

const (
	ActionGrant  Action = "grant"
	ActionRevoke Action = "revoke"
	ActionExpire Action = "expire"
	ActionRotate Action = "rotate"
)

type GrantRecord struct {
	ID          string                  `json:"id"`
	ExtensionID string                  `json:"extensionId"`
	Capability  capability.ID           `json:"capability"`
	Resources   []string                `json:"resources,omitempty"`
	Effect      capability.PolicyEffect `json:"effect"`
	Epoch       uint64                  `json:"epoch"`
	CreatedAt   time.Time               `json:"createdAt"`
	ExpiresAt   *time.Time              `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time              `json:"revokedAt,omitempty"`
	Reason      string                  `json:"reason,omitempty"`
}

type GrantEvent struct {
	SchemaVersion int         `json:"schemaVersion"`
	Protocol      string      `json:"protocol"`
	Revision      int         `json:"revision"`
	Action        Action      `json:"action"`
	Grant         GrantRecord `json:"grant"`
	At            time.Time   `json:"at"`
	PreviousEpoch uint64      `json:"previousEpoch,omitempty"`
}

type GrantStore struct {
	mu      sync.RWMutex
	epoch   uint64
	records map[string]GrantRecord
	events  []GrantEvent
	clock   func() time.Time
}

func NewGrantStore(clock func() time.Time) *GrantStore {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &GrantStore{epoch: 1, records: map[string]GrantRecord{}, clock: clock}
}

func (s *GrantStore) Epoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

func (s *GrantStore) Grant(extensionID string, descriptor capability.GrantDescriptor, expiresAt *time.Time, reason string) GrantRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	record := GrantRecord{ID: descriptor.ID, ExtensionID: extensionID, Capability: descriptor.Capability, Resources: append([]string(nil), descriptor.Resources...), Effect: capability.PolicyAllow, Epoch: s.epoch, CreatedAt: s.clock(), ExpiresAt: expiresAt, Reason: reason}
	s.records[recordKey(extensionID, descriptor.ID)] = record
	s.events = append(s.events, newGrantEvent(ActionGrant, record, record.CreatedAt, 0))
	return record
}

func (s *GrantStore) Revoke(extensionID, grantID, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey(extensionID, grantID)
	record, ok := s.records[key]
	if !ok {
		return false
	}
	now := s.clock()
	s.epoch++
	record.RevokedAt = &now
	record.Epoch = s.epoch
	record.Reason = reason
	s.records[key] = record
	s.events = append(s.events, newGrantEvent(ActionRevoke, record, now, 0))
	return true
}

func (s *GrantStore) Rotate(extensionID, reason string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.epoch
	s.epoch++
	now := s.clock()
	for key, record := range s.records {
		if record.ExtensionID != extensionID || record.RevokedAt != nil {
			continue
		}
		record.Epoch = s.epoch
		record.Reason = reason
		s.records[key] = record
		s.events = append(s.events, newGrantEvent(ActionRotate, record, now, previous))
	}
	return s.epoch
}

func (s *GrantStore) Active(extensionID string, capabilityID capability.ID, resource string, at time.Time) (GrantRecord, bool) {
	return s.ActiveAny(extensionID, capabilityID, []string{resource}, at)
}

func (s *GrantStore) ActiveAny(extensionID string, capabilityID capability.ID, resources []string, at time.Time) (GrantRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, record := range s.records {
		if record.ExtensionID != extensionID || record.Capability != capabilityID || record.Effect != capability.PolicyAllow || record.RevokedAt != nil {
			continue
		}
		if record.ExpiresAt != nil && !at.Before(*record.ExpiresAt) {
			continue
		}
		if resourceMatchesAny(record.Resources, resources) {
			return record, true
		}
	}
	return GrantRecord{}, false
}

func (s *GrantStore) Events() []GrantEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]GrantEvent(nil), s.events...)
}

type ExtensionState struct {
	ID                   string
	DeclaredCapabilities []capability.ID
	Binding              registry.IdentityBinding
	SafeModeAllowed      bool
}

type ConstraintEffect string

const (
	ConstraintAllow ConstraintEffect = "allow"
	ConstraintDeny  ConstraintEffect = "deny"
)

type Constraint struct {
	ID                   string           `json:"id"`
	Effect               ConstraintEffect `json:"effect"`
	Capabilities         []capability.ID  `json:"capabilities,omitempty"`
	Resources            []string         `json:"resources,omitempty"`
	RequireBuiltinSigned bool             `json:"requireBuiltinSigned,omitempty"`
	MinimumRegistryEpoch uint64           `json:"minimumRegistryEpoch,omitempty"`
	RevokedSigners       []string         `json:"revokedSigners,omitempty"`
	Reason               string           `json:"reason,omitempty"`
}

type EnterprisePolicy struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	// DenyByDefault=true requires an active runtime grant after hard checks pass.
	// DenyByDefault=false permits declared capabilities without a grant, but still
	// enforces capability declarations, safe mode, identity checks, and denies.
	DenyByDefault bool         `json:"denyByDefault"`
	SafeMode      bool         `json:"safeMode,omitempty"`
	Constraints   []Constraint `json:"constraints,omitempty"`
}

type SurfaceOwnerResolver func(surfaceID string) (ownerExtensionID string, ok bool)

type Engine struct {
	Store                 *GrantStore
	Enterprise            EnterprisePolicy
	BuiltinSignedDefaults bool
	SurfaceOwnerResolver  SurfaceOwnerResolver
	Now                   func() time.Time

	mu         sync.RWMutex
	extensions map[string]ExtensionState
}

func NewEngine(store *GrantStore, enterprise EnterprisePolicy) *Engine {
	if store == nil {
		store = NewGrantStore(nil)
	}
	return &Engine{Store: store, Enterprise: enterprise, BuiltinSignedDefaults: true, extensions: map[string]ExtensionState{}}
}

func (e *Engine) RegisterExtension(state ExtensionState) error {
	if state.ID == "" {
		state.ID = state.Binding.ExtensionID
	}
	if state.ID == "" {
		return fmt.Errorf("extension id is required")
	}
	if state.Binding.ExtensionID != "" && state.Binding.ExtensionID != state.ID {
		return fmt.Errorf("identity binding extension mismatch")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.extensions == nil {
		e.extensions = map[string]ExtensionState{}
	}
	if state.Binding.BuiltinSigned && !isTrustedBuiltinBinding(state.ID, state.Binding) {
		return fmt.Errorf("signed built-in identity is not trusted")
	}
	e.extensions[state.ID] = ExtensionState{ID: state.ID, DeclaredCapabilities: append([]capability.ID(nil), state.DeclaredCapabilities...), Binding: state.Binding, SafeModeAllowed: state.SafeModeAllowed}
	if e.BuiltinSignedDefaults && isTrustedBuiltinBinding(state.ID, state.Binding) {
		for _, capID := range state.DeclaredCapabilities {
			e.Store.Grant(state.ID, capability.GrantDescriptor{ID: "builtin:" + string(capID), Capability: capID, Resources: []string{"*"}}, nil, "signed built-in default")
		}
	}
	return nil
}

type Request struct {
	ExtensionID     string
	Capability      capability.ID
	Resource        string
	ResourceAliases []string
	At              time.Time
}

func (e *Engine) Evaluate(req Request) bridge.Decision {
	if req.At.IsZero() {
		req.At = e.now()
	}
	state, ok := e.extension(req.ExtensionID)
	if !ok {
		return deny("unknown-extension", "extension is not registered")
	}
	if state.Binding.ExtensionID != "" && state.Binding.ExtensionID != req.ExtensionID {
		return deny("identity-mismatch", "extension identity binding does not match request")
	}
	if e.Enterprise.SafeMode && !state.SafeModeAllowed {
		return deny("safe-mode", "enterprise safe mode is enabled")
	}
	if !slices.Contains(state.DeclaredCapabilities, req.Capability) {
		return deny("capability-not-declared", "capability was not declared by the extension")
	}
	resources := requestResources(req)
	if decision := e.evaluateConstraints(state, req, resources); decision.Result == bridge.DecisionDeny {
		return decision
	}
	grant, ok := e.Store.ActiveAny(req.ExtensionID, req.Capability, resources, req.At)
	if ok {
		return bridge.Decision{Result: bridge.DecisionAllow, Reason: "active runtime grant", MatchedGrant: grant.ID}
	}
	if e.Enterprise.DenyByDefault {
		return deny("grant-required", "deny-by-default requires an active runtime grant")
	}
	return bridge.Decision{Result: bridge.DecisionAllow, Reason: "deny-by-default disabled; declared capability allowed"}
}

func (e *Engine) EvaluateEnvelope(_ context.Context, envelope protocol.Envelope) (bridge.Decision, error) {
	capID := capability.ID("ui.envelope." + string(envelope.Kind))
	if envelope.Kind == protocol.EnvelopeUIEvent {
		capID = capability.ActionInvoke
	}
	decision := e.Evaluate(Request{ExtensionID: envelope.ExtensionID, Capability: capID, Resource: string(envelope.Kind), At: envelope.Timestamp})
	return decision, decisionError(decision)
}

func (e *Engine) EvaluateAction(_ context.Context, invocation surface.ActionInvocation) (bridge.Decision, error) {
	ownerExtensionID, ownerResolved := e.actionOwnerExtensionID(invocation)
	if ownerResolved && invocation.OwnerExtensionID != "" && invocation.OwnerExtensionID != ownerExtensionID {
		decision := deny("owner-mismatch", "action invocation owner extension id does not match the registered surface owner")
		return decision, decisionError(decision)
	}
	if ownerExtensionID == "" {
		decision := deny("owner-extension-required", "action invocation owner extension id is required")
		return decision, decisionError(decision)
	}
	actionID := invocation.ActionID
	if actionID == "" {
		actionID = invocation.ID
	}
	resources := actionInvocationResources(invocation.SurfaceID, actionID)
	decision := e.Evaluate(Request{ExtensionID: ownerExtensionID, Capability: capability.ActionInvoke, Resource: resources[0], ResourceAliases: resources[1:], At: invocation.RequestedAt})
	return decision, decisionError(decision)
}

func (e *Engine) EvaluateGrant(_ context.Context, extensionID string, requested capability.GrantDescriptor) (bridge.Decision, error) {
	decision := e.Evaluate(Request{ExtensionID: extensionID, Capability: requested.Capability, Resource: firstResource(requested.Resources)})
	return decision, decisionError(decision)
}

func (e *Engine) actionOwnerExtensionID(invocation surface.ActionInvocation) (string, bool) {
	if e.SurfaceOwnerResolver == nil || invocation.SurfaceID == "" {
		return invocation.OwnerExtensionID, false
	}
	ownerExtensionID, ok := e.SurfaceOwnerResolver(invocation.SurfaceID)
	if !ok || ownerExtensionID == "" {
		return invocation.OwnerExtensionID, false
	}
	return ownerExtensionID, true
}

func (e *Engine) extension(id string) (ExtensionState, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.extensions[id]
	return state, ok
}

func (e *Engine) evaluateConstraints(state ExtensionState, req Request, resources []string) bridge.Decision {
	for _, constraint := range e.Enterprise.Constraints {
		if len(constraint.Capabilities) > 0 && !slices.Contains(constraint.Capabilities, req.Capability) {
			continue
		}
		if len(constraint.Resources) > 0 && !resourceMatchesAny(constraint.Resources, resources) {
			continue
		}
		if constraint.RequireBuiltinSigned && !isTrustedBuiltinBinding(state.ID, state.Binding) {
			return deny(constraint.ID, defaultReason(constraint.Reason, "capability requires a trusted signed built-in extension"))
		}
		if constraint.MinimumRegistryEpoch > 0 && state.Binding.RegistryEpoch < constraint.MinimumRegistryEpoch {
			return deny(constraint.ID, defaultReason(constraint.Reason, "extension registry epoch is too old"))
		}
		if len(constraint.RevokedSigners) > 0 && slices.Contains(constraint.RevokedSigners, state.Binding.SignerFingerprint) {
			return deny(constraint.ID, defaultReason(constraint.Reason, "extension signer is revoked"))
		}
		if constraint.Effect == ConstraintDeny {
			return deny(constraint.ID, defaultReason(constraint.Reason, "enterprise policy denied the request"))
		}
	}
	return bridge.Decision{Result: bridge.DecisionAllow}
}

func LoadEnterprisePolicy(data []byte) (EnterprisePolicy, error) {
	var document EnterprisePolicy
	if err := json.Unmarshal(data, &document); err != nil {
		return EnterprisePolicy{}, err
	}
	if document.SchemaVersion != 1 {
		return EnterprisePolicy{}, fmt.Errorf("unsupported enterprise policy schema %d", document.SchemaVersion)
	}
	if document.Version == "" {
		return EnterprisePolicy{}, fmt.Errorf("enterprise policy version is required")
	}
	return document, nil
}

func deny(reason, message string) bridge.Decision {
	return bridge.Decision{Result: bridge.DecisionDeny, Reason: reason, Error: &uierrors.ContractError{Code: uierrors.PolicyDenied, Message: message, Recoverable: false}}
}

func decisionError(decision bridge.Decision) error {
	if decision.Result == bridge.DecisionDeny {
		return ErrPolicyDenied
	}
	return nil
}

func defaultReason(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func firstResource(resources []string) string {
	if len(resources) == 0 {
		return "*"
	}
	return resources[0]
}

func requestResources(req Request) []string {
	resources := make([]string, 0, 1+len(req.ResourceAliases))
	appendResource(&resources, req.Resource)
	for _, resource := range req.ResourceAliases {
		appendResource(&resources, resource)
	}
	if len(resources) == 0 {
		resources = append(resources, "*")
	}
	return resources
}

func actionInvocationResources(surfaceID, actionID string) []string {
	resources := make([]string, 0, 2)
	// ui.action.invoke grants use the surface ID as the base resource. A
	// narrower action scope is expressed as "surfaceId/actionId".
	if surfaceID != "" && actionID != "" {
		appendResource(&resources, surfaceID+"/"+actionID)
	}
	appendResource(&resources, surfaceID)
	if len(resources) == 0 {
		appendResource(&resources, actionID)
	}
	if len(resources) == 0 {
		resources = append(resources, "*")
	}
	return resources
}

func appendResource(resources *[]string, resource string) {
	if resource == "" {
		return
	}
	for _, existing := range *resources {
		if existing == resource {
			return
		}
	}
	*resources = append(*resources, resource)
}

func resourceMatchesAny(patterns []string, resources []string) bool {
	if len(resources) == 0 {
		return resourceMatches(patterns, "")
	}
	for _, resource := range resources {
		if resourceMatches(patterns, resource) {
			return true
		}
	}
	return false
}

func resourceMatches(patterns []string, resource string) bool {
	if len(patterns) == 0 {
		return true
	}
	if resource == "" {
		resource = "*"
	}
	for _, pattern := range patterns {
		if pattern == "*" || pattern == resource {
			return true
		}
		if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
			prefix := pattern[:len(pattern)-1]
			if len(resource) >= len(prefix) && resource[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

func newGrantEvent(action Action, record GrantRecord, at time.Time, previous uint64) GrantEvent {
	return GrantEvent{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Action: action, Grant: record, At: at, PreviousEpoch: previous}
}

func recordKey(extensionID, grantID string) string { return extensionID + "\x00" + grantID }

func isTrustedBuiltinBinding(extensionID string, binding registry.IdentityBinding) bool {
	return binding.BuiltinSigned && binding.ExtensionID == extensionID && binding.SourceType == "builtin" && binding.SourceValue == extensionID && binding.SignerID == "afterburner-core" && binding.SignerFingerprint == "builtin:"+extensionID
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
