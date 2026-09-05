package tooling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/accessibility"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/diagnostics"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/policy"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/quotas"
	"github.com/nbaertsch/afterburner/internal/ui/render"
)

type CertificationOptions struct {
	HomeRoot    string
	ExtensionID string
	SurfaceID   string
}

type CertificationResult struct {
	Report Report `json:"report"`
	Human  string `json:"human"`
}

func Certify(ctx context.Context, opts CertificationOptions) (CertificationResult, error) {
	if opts.ExtensionID == "" {
		opts.ExtensionID = "extension-under-test"
	}
	if opts.SurfaceID == "" {
		opts.SurfaceID = "certification-surface"
	}
	report := NewReport(opts.ExtensionID, opts.SurfaceID)
	componentCheck(ctx, &report)
	keyboardAccessibilityCheck(ctx, &report)
	fallbackCheck(ctx, &report)
	securityGrantCheck(&report, opts.ExtensionID)
	protocolCheck(ctx, &report)
	quotaCheck(&report)
	recoveryCheck(ctx, &report)
	observabilityCheck(&report, opts.HomeRoot)
	nativeRequirementsCheck(&report)
	final := report.Finalize()
	return CertificationResult{Report: final, Human: HumanReport(final)}, nil
}

func componentCheck(ctx context.Context, report *Report) {
	fixture := AllComponentsFixture()
	result, err := RenderFixture(ctx, fixture.Name, RenderOptions{Width: 100, Height: 80, Theme: "afterburner.dark", ColorMode: render.ColorModeMono, Plain: true, Unicode: false})
	if err != nil {
		report.Add("cert.components", "component-support", StatusFail, err.Error(), nil)
		return
	}
	want := supportedKindSet()
	got := map[string]bool{}
	for _, kind := range result.Components {
		got[kind] = true
	}
	var missing []string
	for kind := range want {
		if !got[kind] && kind != "root" {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		report.Add("cert.components", "component-support", StatusFail, "fixture did not cover every supported component kind", map[string]any{"missing": missing})
		return
	}
	report.Add("cert.components", "component-support", StatusPass, "all supported component kinds render in the full fixture", map[string]any{"count": len(got), "plainHash": StableHash(result.Frame.Plain)})
}

func keyboardAccessibilityCheck(ctx context.Context, report *Report) {
	result, err := RenderFixture(ctx, "all-components", RenderOptions{Width: 80, Height: 80, Theme: "afterburner.highContrast", ColorMode: render.ColorModeMono, Plain: true, Unicode: false})
	if err != nil {
		report.Add("cert.accessibility", "keyboard-accessibility", StatusFail, err.Error(), nil)
		return
	}
	focusable, actions := countA11y(result.Accessibility.Root)
	if focusable == 0 || actions == 0 {
		report.Add("cert.accessibility", "keyboard-accessibility", StatusFail, "semantic projection lacks focusable nodes or keyboard actions", map[string]int{"focusable": focusable, "actions": actions})
		return
	}
	report.Add("cert.accessibility", "keyboard-accessibility", StatusPass, "keyboard-only and accessibility projection is available", map[string]int{"focusable": focusable, "actions": actions})
}

func fallbackCheck(ctx context.Context, report *Report) {
	fixture := AbuseFixture()
	fallback, fallbackErr := render.NewFailoverRenderer(failingCertEngine{}, render.NewPlainRenderer(render.Options{Width: 80, Height: 20, Now: func() time.Time { return DeterministicTime }})).RenderFrame(ctx, fixture.Tree)
	if fallbackErr != nil || fallback.Plain == "" {
		report.Add("cert.fallback", "fallback", StatusFail, "render failover did not recover from an abuse fixture", map[string]any{"fallbackError": fmt.Sprint(fallbackErr)})
		return
	}
	report.Add("cert.fallback", "fallback", StatusPass, "plain fallback recovers from renderer failure", map[string]string{"fallbackHash": StableHash(fallback.Plain)})
}

func securityGrantCheck(report *Report, extensionID string) {
	grantReport := EvaluateGrantScenario(extensionID)
	for _, check := range grantReport.Checks {
		report.Checks = append(report.Checks, check)
	}
	store := policy.NewGrantStore(func() time.Time { return DeterministicTime })
	engine := policy.NewEngine(store, policy.EnterprisePolicy{SchemaVersion: 1, Version: "cert", DenyByDefault: true, Constraints: []policy.Constraint{{ID: "builtin-only-observability", Effect: policy.ConstraintAllow, Capabilities: []capability.ID{capability.BlackBoxEventSink}, RequireBuiltinSigned: true}}})
	_ = engine.RegisterExtension(policy.ExtensionState{ID: extensionID, DeclaredCapabilities: []capability.ID{capability.BlackBoxEventSink}})
	decision := engine.Evaluate(policy.Request{ExtensionID: extensionID, Capability: capability.BlackBoxEventSink, Resource: "observation", At: DeterministicTime})
	if decision.Result != bridge.DecisionDeny {
		report.Add("cert.security.builtin", "security-grants", StatusFail, "unsigned extension was allowed to claim Black Box observability", decision)
	} else {
		report.Add("cert.security.builtin", "security-grants", StatusPass, "enterprise policy rejects unsigned Black Box observability claims", map[string]string{"reason": decision.Reason})
	}
}

func protocolCheck(ctx context.Context, report *Report) {
	if err := validateProtocolRoundTrip(ctx); err != nil {
		report.Add("cert.protocol", "protocol-compatibility", StatusFail, err.Error(), nil)
		return
	}
	bad := protocol.NewEnvelope(protocol.EnvelopeComponentSnapshot, "bad-revision", protocol.Actor{Kind: protocol.ActorExtension, ID: "cert"}, protocol.Actor{Kind: protocol.ActorHost, ID: "host"}, rawProps(map[string]any{"schemaVersion": 1}))
	bad.Revision = protocol.ProtocolRevision + 1
	if err := protocol.ValidateEnvelope(bad); err == nil {
		report.Add("cert.protocol", "protocol-compatibility", StatusFail, "unsupported protocol revision was accepted", nil)
		return
	}
	report.Add("cert.protocol", "protocol-compatibility", StatusPass, "current protocol passes and incompatible revisions are rejected", protocol.CurrentRevision())
}

func quotaCheck(report *Report) {
	clock := fixedClock{now: DeterministicTime}
	bucket, err := quotas.NewTokenBucket(quotas.BucketConfig{Capacity: 2, RefillTokens: 1, RefillEvery: time.Second}, clock)
	if err != nil {
		report.Add("cert.quotas", "quotas", StatusFail, err.Error(), nil)
		return
	}
	first := bucket.Allow(1)
	second := bucket.Allow(2)
	if !first.Allowed || second.Allowed || !second.Backpressure {
		report.Add("cert.quotas", "quotas", StatusFail, "token bucket did not enforce backpressure", map[string]any{"first": first, "second": second})
		return
	}
	report.Add("cert.quotas", "quotas", StatusPass, "quota enforcement produces deterministic backpressure", map[string]any{"retryAfterMillis": second.RetryAfter.Milliseconds()})
}

func recoveryCheck(ctx context.Context, report *Report) {
	result, err := RenderFixture(ctx, "abuse", RenderOptions{Width: 40, Height: 12, Theme: "afterburner.dark", ColorMode: render.ColorModeMono, Plain: false, Unicode: false})
	if err != nil {
		report.Add("cert.recovery", "recovery", StatusFail, err.Error(), nil)
		return
	}
	if strings.Contains(result.Frame.Plain, "\x1b") {
		report.Add("cert.recovery", "recovery", StatusFail, "recovered plain frame contains ANSI control sequences", nil)
		return
	}
	report.Add("cert.recovery", "recovery", StatusPass, "abuse fixture recovers to sanitized deterministic frame", map[string]string{"plainHash": StableHash(result.Frame.Plain)})
}

func observabilityCheck(report *Report, homeRoot string) {
	sink := observability.BlackBoxSinkDescriptor()
	if !sink.Redaction.MetadataOnly || sink.FailureBehavior != "ignore-when-absent" {
		report.Add("cert.observability", "observability", StatusFail, "Black Box sink contract is not metadata-only optional", sink)
		return
	}
	status := StatusPass
	message := "observability uses optional metadata-only Black Box sink contract"
	if homeRoot != "" {
		if entry, ok, err := loadRegistryEntry(homeRoot, observability.BlackBoxExtensionID); err != nil {
			status = StatusWarn
			message = err.Error()
		} else if !ok || !entry.Enabled {
			status = StatusInfo
			message = "Black Box is absent or disabled; diagnostics remain local and metadata-only"
		}
	}
	report.Add("cert.observability", "observability", status, message, sink)
}

func nativeRequirementsCheck(report *Report) {
	builder := diagnostics.NewBuilder("ui-certification", true)
	builder.Clock = func() time.Time { return DeterministicTime }
	_ = builder.AddJSONArtifact("ui-certification", "report", map[string]any{"protocol": protocol.CurrentRevision()}, []string{"protocol", "revision"}, "metadata-only certification artifact")
	manifest := builder.Manifest()
	if !manifest.MetadataOnly || len(manifest.Artifacts) != 1 {
		report.Add("cert.native", "native-requirements", StatusFail, "diagnostics bundle metadata contract failed", manifest)
		return
	}
	report.Add("cert.native", "native-requirements", StatusPass, "native diagnostics requirements are expressed as metadata-only artifacts", manifest)
}

func HumanReport(report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Afterburner UI certification: %s\n", strings.ToUpper(string(report.Summary.Status)))
	fmt.Fprintf(&b, "Extension: %s  Surface: %s\n", valueOr(report.ExtensionID, "-"), valueOr(report.SurfaceID, "-"))
	fmt.Fprintf(&b, "Checks: %d pass, %d fail, %d warn, %d info\n", report.Summary.Pass, report.Summary.Fail, report.Summary.Warn, report.Summary.Info)
	for _, check := range report.Checks {
		fmt.Fprintf(&b, "[%s] %s: %s\n", strings.ToUpper(string(check.Status)), check.ID, check.Message)
	}
	return b.String()
}

func StableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func countA11y(node accessibility.SemanticNode) (int, int) {
	focusable := 0
	actions := len(node.KeyboardActions)
	if node.Focusable {
		focusable++
	}
	for _, child := range node.Children {
		f, a := countA11y(child)
		focusable += f
		actions += a
	}
	return focusable, actions
}

type failingCertEngine struct{}

func (failingCertEngine) RenderFrame(context.Context, component.Tree) (render.Frame, error) {
	return render.Frame{}, fmt.Errorf("synthetic renderer failure")
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func MarshalDeterministic(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
