package policy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

func TestDenyByDefaultDeclarationGrantExpiryRevokeAndRotation(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	store := NewGrantStore(func() time.Time { return now })
	engine := NewEngine(store, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true})
	binding := registry.IdentityBinding{ExtensionID: "ext", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "src", RegistryEpoch: 1, GrantEpoch: store.Epoch(), BoundAt: now.Format(time.RFC3339Nano)}
	if err := engine.RegisterExtension(ExtensionState{ID: "ext", DeclaredCapabilities: []capability.ID{capability.ActionInvoke}, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.DataRead, Resource: "items", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "capability-not-declared" {
		t.Fatalf("undeclared capability decision = %#v", got)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ActionInvoke, Resource: "submit", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "grant-required" {
		t.Fatalf("missing grant decision = %#v", got)
	}
	expires := now.Add(time.Minute)
	grant := store.Grant("ext", capability.GrantDescriptor{ID: "invoke-submit", Capability: capability.ActionInvoke, Resources: []string{"submit"}}, &expires, "operator approved")
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ActionInvoke, Resource: "submit", At: now}); got.Result != bridge.DecisionAllow || got.MatchedGrant != grant.ID {
		t.Fatalf("active grant decision = %#v", got)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ActionInvoke, Resource: "submit", At: expires}); got.Result != bridge.DecisionDeny {
		t.Fatalf("expired grant should deny, got %#v", got)
	}
	store.Rotate("ext", "key rotation")
	if !store.Revoke("ext", "invoke-submit", "operator revoked") {
		t.Fatal("revoke failed")
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ActionInvoke, Resource: "submit", At: now}); got.Result != bridge.DecisionDeny {
		t.Fatalf("revoked grant should deny, got %#v", got)
	}
	seenRotate := false
	for _, event := range store.Events() {
		if event.Action == ActionRotate {
			seenRotate = true
		}
	}
	if !seenRotate || store.Epoch() < 4 {
		t.Fatalf("grant epoch/event rotation not recorded: epoch=%d events=%#v", store.Epoch(), store.Events())
	}
}

func TestDenyByDefaultFalseAllowsDeclaredCapabilitiesWithoutGrant(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	engine := NewEngine(NewGrantStore(func() time.Time { return now }), EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: false, Constraints: []Constraint{{ID: "deny-secret", Effect: ConstraintDeny, Capabilities: []capability.ID{capability.DataRead}, Resources: []string{"secret"}}}})
	binding := registry.IdentityBinding{ExtensionID: "ext", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "src", RegistryEpoch: 1, GrantEpoch: 1, BoundAt: now.Format(time.RFC3339Nano)}
	if err := engine.RegisterExtension(ExtensionState{ID: "ext", DeclaredCapabilities: []capability.ID{capability.DataRead}, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.DataRead, Resource: "public", At: now}); got.Result != bridge.DecisionAllow || got.MatchedGrant != "" {
		t.Fatalf("deny-by-default=false decision = %#v", got)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.DataRead, Resource: "secret", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "deny-secret" {
		t.Fatalf("explicit deny under deny-by-default=false = %#v", got)
	}
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.DataWrite, Resource: "public", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "capability-not-declared" {
		t.Fatalf("undeclared capability under deny-by-default=false = %#v", got)
	}
}

func TestPolicyDeniesIdentityMismatchRevokedSignerAndSafeMode(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	store := NewGrantStore(func() time.Time { return now })
	engine := NewEngine(store, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true, SafeMode: true, Constraints: []Constraint{{ID: "revoked", Effect: ConstraintAllow, Capabilities: []capability.ID{capability.ObservabilitySink}, RevokedSigners: []string{"bad-fp"}}}})
	if err := engine.RegisterExtension(ExtensionState{ID: "ext", DeclaredCapabilities: []capability.ID{capability.ObservabilitySink}, Binding: registry.IdentityBinding{ExtensionID: "other"}}); err == nil {
		t.Fatal("expected identity mismatch registration failure")
	}
	if err := engine.RegisterExtension(ExtensionState{ID: "ext", DeclaredCapabilities: []capability.ID{capability.ObservabilitySink}, Binding: registry.IdentityBinding{ExtensionID: "ext", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "src", SignerFingerprint: "bad-fp", RegistryEpoch: 1, GrantEpoch: 1, BoundAt: now.Format(time.RFC3339Nano)}}); err != nil {
		t.Fatal(err)
	}
	store.Grant("ext", capability.GrantDescriptor{ID: "obs", Capability: capability.ObservabilitySink, Resources: []string{"*"}}, nil, "test")
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ObservabilitySink, Resource: "metadata", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "safe-mode" {
		t.Fatalf("safe mode decision = %#v", got)
	}
	engine.Enterprise.SafeMode = false
	if got := engine.Evaluate(Request{ExtensionID: "ext", Capability: capability.ObservabilitySink, Resource: "metadata", At: now}); got.Result != bridge.DecisionDeny || got.Reason != "revoked" {
		t.Fatalf("revoked signer decision = %#v", got)
	}
}

func TestEvaluateActionUsesOwnerExtensionAndSurfaceGrant(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	store := NewGrantStore(func() time.Time { return now })
	engine := NewEngine(store, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true})
	if err := engine.RegisterExtension(ExtensionState{ID: "black-box", DeclaredCapabilities: []capability.ID{capability.ActionInvoke}, Binding: registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "extensions/BlackBox", RegistryEpoch: 1, GrantEpoch: store.Epoch(), BoundAt: now.Format(time.RFC3339Nano)}}); err != nil {
		t.Fatal(err)
	}
	grant := store.Grant("black-box", capability.GrantDescriptor{ID: "black-box-panel-render", Capability: capability.ActionInvoke, Resources: []string{"afterburner-black-box-live"}}, nil, "Black Box surface actions")

	for _, actionID := range []string{"refresh", "doctor", "export", "close"} {
		decision, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-" + actionID, OwnerExtensionID: "black-box", SurfaceID: "afterburner-black-box-live", ActionID: actionID, RequestedAt: now})
		if err != nil || decision.Result != bridge.DecisionAllow || decision.MatchedGrant != grant.ID {
			t.Fatalf("Black Box %s action decision = %#v err=%v", actionID, decision, err)
		}
	}

	decision, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-refresh", SurfaceID: "afterburner-black-box-live", ActionID: "refresh", RequestedAt: now})
	if err != ErrPolicyDenied || decision.Result != bridge.DecisionDeny || decision.Reason != "owner-extension-required" {
		t.Fatalf("missing owner decision = %#v err=%v", decision, err)
	}
}

func TestEvaluateActionDeniesCrossExtensionOwner(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	store := NewGrantStore(func() time.Time { return now })
	engine := NewEngine(store, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true})
	engine.SurfaceOwnerResolver = func(surfaceID string) (string, bool) {
		if surfaceID == "afterburner-black-box-live" {
			return "black-box", true
		}
		return "", false
	}
	for _, extensionID := range []string{"black-box", "attacker"} {
		if err := engine.RegisterExtension(ExtensionState{ID: extensionID, DeclaredCapabilities: []capability.ID{capability.ActionInvoke}, Binding: registry.IdentityBinding{ExtensionID: extensionID, ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: extensionID, RegistryEpoch: 1, GrantEpoch: store.Epoch(), BoundAt: now.Format(time.RFC3339Nano)}}); err != nil {
			t.Fatal(err)
		}
	}
	store.Grant("black-box", capability.GrantDescriptor{ID: "black-box-panel-render", Capability: capability.ActionInvoke, Resources: []string{"afterburner-black-box-live"}}, nil, "Black Box surface actions")
	store.Grant("attacker", capability.GrantDescriptor{ID: "attacker-panel-render", Capability: capability.ActionInvoke, Resources: []string{"*"}}, nil, "attacker surface actions")

	resolved, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-refresh", SurfaceID: "afterburner-black-box-live", ActionID: "refresh", RequestedAt: now})
	if err != nil || resolved.Result != bridge.DecisionAllow || resolved.MatchedGrant != "black-box-panel-render" {
		t.Fatalf("resolved owner action decision = %#v err=%v", resolved, err)
	}
	decision, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-refresh", OwnerExtensionID: "attacker", SurfaceID: "afterburner-black-box-live", ActionID: "refresh", RequestedAt: now})
	if err != ErrPolicyDenied || decision.Result != bridge.DecisionDeny || decision.Reason != "owner-mismatch" {
		t.Fatalf("cross-extension action decision = %#v err=%v", decision, err)
	}
}

func TestEvaluateActionScopesAndDenyPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	store := NewGrantStore(func() time.Time { return now })
	engine := NewEngine(store, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true})
	if err := engine.RegisterExtension(ExtensionState{ID: "black-box", DeclaredCapabilities: []capability.ID{capability.ActionInvoke}, Binding: registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "extensions/BlackBox", RegistryEpoch: 1, GrantEpoch: store.Epoch(), BoundAt: now.Format(time.RFC3339Nano)}}); err != nil {
		t.Fatal(err)
	}
	store.Grant("black-box", capability.GrantDescriptor{ID: "export-only", Capability: capability.ActionInvoke, Resources: []string{"afterburner-black-box-live/export"}}, nil, "operator approved export")

	exportDecision, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-export", OwnerExtensionID: "black-box", SurfaceID: "afterburner-black-box-live", ActionID: "export", RequestedAt: now})
	if err != nil || exportDecision.Result != bridge.DecisionAllow || exportDecision.MatchedGrant != "export-only" {
		t.Fatalf("action-scoped export decision = %#v err=%v", exportDecision, err)
	}
	doctorDecision, err := engine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-doctor", OwnerExtensionID: "black-box", SurfaceID: "afterburner-black-box-live", ActionID: "doctor", RequestedAt: now})
	if err != ErrPolicyDenied || doctorDecision.Result != bridge.DecisionDeny || doctorDecision.Reason != "grant-required" {
		t.Fatalf("action-scoped doctor decision = %#v err=%v", doctorDecision, err)
	}

	denyStore := NewGrantStore(func() time.Time { return now })
	denyEngine := NewEngine(denyStore, EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true, Constraints: []Constraint{{ID: "deny-export", Effect: ConstraintDeny, Capabilities: []capability.ID{capability.ActionInvoke}, Resources: []string{"afterburner-black-box-live/export"}}}})
	if err := denyEngine.RegisterExtension(ExtensionState{ID: "black-box", DeclaredCapabilities: []capability.ID{capability.ActionInvoke}, Binding: registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "extensions/BlackBox", RegistryEpoch: 1, GrantEpoch: denyStore.Epoch(), BoundAt: now.Format(time.RFC3339Nano)}}); err != nil {
		t.Fatal(err)
	}
	denyStore.Grant("black-box", capability.GrantDescriptor{ID: "surface-actions", Capability: capability.ActionInvoke, Resources: []string{"afterburner-black-box-live"}}, nil, "surface actions")
	denied, err := denyEngine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-export", OwnerExtensionID: "black-box", SurfaceID: "afterburner-black-box-live", ActionID: "export", RequestedAt: now})
	if err != ErrPolicyDenied || denied.Result != bridge.DecisionDeny || denied.Reason != "deny-export" {
		t.Fatalf("deny precedence decision = %#v err=%v", denied, err)
	}
	allowed, err := denyEngine.EvaluateAction(context.Background(), surface.ActionInvocation{ID: "invoke-doctor", OwnerExtensionID: "black-box", SurfaceID: "afterburner-black-box-live", ActionID: "doctor", RequestedAt: now})
	if err != nil || allowed.Result != bridge.DecisionAllow || allowed.MatchedGrant != "surface-actions" {
		t.Fatalf("surface-scoped doctor decision = %#v err=%v", allowed, err)
	}
}

func TestBuiltinSignedDefaultsAndSidecarResourcePolicy(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	engine := NewEngine(NewGrantStore(func() time.Time { return now }), EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true, Constraints: []Constraint{{ID: "builtin-only", Effect: ConstraintAllow, Capabilities: []capability.ID{capability.BlackBoxEventSink}, RequireBuiltinSigned: true}}})
	binding := registry.IdentityBinding{ExtensionID: "black-box", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "builtin", SourceValue: "black-box", SignerID: "afterburner-core", SignerFingerprint: "builtin:black-box", BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: now.Format(time.RFC3339Nano)}
	if err := engine.RegisterExtension(ExtensionState{ID: "black-box", DeclaredCapabilities: []capability.ID{capability.BlackBoxEventSink}, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if got := engine.Evaluate(Request{ExtensionID: "black-box", Capability: capability.BlackBoxEventSink, Resource: "afterburner.ui/envelopes/metadata", At: now}); got.Result != bridge.DecisionAllow {
		t.Fatalf("built-in default grant decision = %#v", got)
	}
	enforcer := LocalDeclarationEnforcer{}
	allowed, err := enforcer.Enforce(context.Background(), EnforcementRequest{OpaqueRequestID: "opaque-1234", ExtensionID: "black-box", Capability: capability.BlackBoxEventSink, Policy: ResourcePolicy{Filesystem: FilesystemPolicy{Mode: FilesystemInherit}, Network: NetworkPolicy{Mode: NetworkInherit}, Environment: RestrictedEnvironmentPolicy{ClearInherited: true}}})
	if err != nil || !allowed.Allowed || allowed.Reason != "enforced" {
		t.Fatalf("sidecar inherit-only decision = %#v err=%v", allowed, err)
	}
	denied, err := enforcer.Enforce(context.Background(), EnforcementRequest{OpaqueRequestID: "opaque-1234", ExtensionID: "black-box", Capability: capability.BlackBoxEventSink, Policy: ResourcePolicy{MaxProcesses: 1, MaxProcessMemory: 64 << 20, KillOnClose: true, Filesystem: FilesystemPolicy{Mode: FilesystemRead, ReadRoots: []string{"C:\\safe"}}, Network: NetworkPolicy{Mode: NetworkDeny}, Environment: RestrictedEnvironmentPolicy{ClearInherited: true}}})
	if err != nil || denied.Allowed || !strings.Contains(denied.Reason, "isolation capability unavailable") || !strings.Contains(denied.Reason, "filesystem.read") || !strings.Contains(denied.Reason, "network.deny") {
		t.Fatalf("unavailable fs/network isolation should fail closed: %#v err=%v", denied, err)
	}
	missing, err := enforcer.Enforce(context.Background(), EnforcementRequest{OpaqueRequestID: "opaque-1234", ExtensionID: "black-box", Capability: capability.BlackBoxEventSink})
	if err != nil || missing.Allowed || !strings.Contains(missing.Reason, "valid filesystem") {
		t.Fatalf("missing resource declarations should deny: %#v err=%v", missing, err)
	}
}

func TestForgedBuiltinBindingDoesNotReceiveDefaultGrants(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	engine := NewEngine(NewGrantStore(func() time.Time { return now }), EnterprisePolicy{SchemaVersion: 1, Version: "test", DenyByDefault: true})
	forged := registry.IdentityBinding{ExtensionID: "spoof", ManifestHash: "sha256:m", TreeHash: "sha256:t", SourceType: "path", SourceValue: "C:\\tmp\\spoof", SignerID: "afterburner-core", SignerFingerprint: "builtin:spoof", BuiltinSigned: true, RegistryEpoch: 1, GrantEpoch: 1, BoundAt: now.Format(time.RFC3339Nano)}
	if err := engine.RegisterExtension(ExtensionState{ID: "spoof", DeclaredCapabilities: []capability.ID{capability.BlackBoxEventSink}, Binding: forged}); err == nil {
		t.Fatal("expected forged built-in identity to be rejected")
	}
}

func TestCircuitBreakerOpensCrashLoopAndRecovers(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	breaker := NewCircuitBreaker(CircuitPolicy{MaxCrashes: 2, Window: time.Minute, Cooldown: time.Minute, SafeModeOnOpen: true}, func() time.Time { return now })
	if breaker.RecordCrash("ext") != CircuitClosed {
		t.Fatal("first crash should not open circuit")
	}
	if breaker.RecordCrash("ext") != CircuitOpen || !breaker.SafeMode("ext") {
		t.Fatal("second crash should open safe-mode circuit")
	}
	now = now.Add(2 * time.Minute)
	if breaker.State("ext") != CircuitClosed {
		t.Fatal("circuit did not recover after cooldown")
	}
}
