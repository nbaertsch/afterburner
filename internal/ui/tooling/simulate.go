package tooling

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/policy"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/render"
)

type SimulationOptions struct {
	HomeRoot    string
	ExtensionID string
	SurfaceID   string
}

type SimulationResult struct {
	Report       Report         `json:"report"`
	Manifest     *ManifestBrief `json:"manifest,omitempty"`
	RenderMatrix []RenderProbe  `json:"renderMatrix"`
}

type ManifestBrief struct {
	ID             string   `json:"id"`
	DisplayName    string   `json:"displayName"`
	Enabled        bool     `json:"enabled"`
	ActivePath     string   `json:"activePath,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	UIComponents   []string `json:"uiComponents,omitempty"`
	UISurfaces     []string `json:"uiSurfaces,omitempty"`
	UICapabilities []string `json:"uiCapabilities,omitempty"`
}

type RenderProbe struct {
	Theme     string `json:"theme"`
	ColorMode string `json:"colorMode"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Renderer  string `json:"renderer"`
	PlainHash string `json:"plainHash"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

func Simulate(ctx context.Context, opts SimulationOptions) (SimulationResult, error) {
	report := NewReport(opts.ExtensionID, opts.SurfaceID)
	entry, found, err := loadRegistryEntry(opts.HomeRoot, opts.ExtensionID)
	if err != nil {
		report.Add("extension.registry", "manifest", StatusWarn, err.Error(), nil)
	} else if found {
		brief := manifestBrief(entry)
		report.Add("extension.registry", "manifest", StatusPass, "extension is installed in the Afterburner registry", brief)
	} else if opts.ExtensionID != "" {
		report.Add("extension.registry", "manifest", StatusWarn, "extension is not installed; simulator used declared id only", nil)
	}
	fixture := AllComponentsFixture()
	if opts.SurfaceID != "" {
		fixture.Tree.SurfaceID = opts.SurfaceID
	}
	matrix := []RenderProbe{}
	for _, theme := range []string{"afterburner.dark", "afterburner.light", "afterburner.highContrast"} {
		for _, mode := range []render.ColorMode{render.ColorModeTrueColor, render.ColorModeANSI256, render.ColorModeMono, render.ColorModeHighContrast} {
			for _, width := range []int{40, 80, 120} {
				result, renderErr := RenderFixture(ctx, fixture.Name, RenderOptions{Width: width, Height: 30, Theme: theme, ColorMode: mode, Unicode: true})
				probe := RenderProbe{Theme: theme, ColorMode: string(mode), Width: width, Height: 30, OK: renderErr == nil}
				if renderErr != nil {
					probe.Error = renderErr.Error()
				} else {
					probe.Renderer = string(result.Frame.Renderer)
					probe.PlainHash = StableHash(result.Frame.Plain)
				}
				matrix = append(matrix, probe)
			}
		}
	}
	failed := 0
	for _, probe := range matrix {
		if !probe.OK {
			failed++
		}
	}
	if failed == 0 {
		report.Add("simulate.render-matrix", "render", StatusPass, "component fixture rendered across theme, size, and color-mode matrix", map[string]any{"probes": len(matrix)})
	} else {
		report.Add("simulate.render-matrix", "render", StatusFail, "one or more render probes failed", map[string]any{"failed": failed})
	}
	accessibilityResult, err := RenderFixture(ctx, "all-components", RenderOptions{Width: 80, Height: 40, Theme: "afterburner.highContrast", ColorMode: render.ColorModeMono, Plain: true, Unicode: false})
	if err != nil {
		report.Add("simulate.accessibility", "accessibility", StatusFail, err.Error(), nil)
	} else if len(accessibilityResult.Accessibility.Root.Children) == 0 {
		report.Add("simulate.accessibility", "accessibility", StatusFail, "semantic tree did not contain projected children", nil)
	} else {
		report.Add("simulate.accessibility", "accessibility", StatusPass, "keyboard-only semantic tree projected deterministically", map[string]any{"liveSummary": accessibilityResult.Accessibility.LiveSummary})
	}
	if err := validateProtocolRoundTrip(ctx); err != nil {
		report.Add("simulate.protocol", "protocol", StatusFail, err.Error(), nil)
	} else {
		report.Add("simulate.protocol", "protocol", StatusPass, "current protocol envelope validates", protocol.CurrentRevision())
	}
	var brief *ManifestBrief
	if found {
		b := manifestBrief(entry)
		brief = &b
	}
	return SimulationResult{Report: report.Finalize(), Manifest: brief, RenderMatrix: matrix}, nil
}

func Doctor(homeRoot string) Report {
	report := NewReport("", "")
	report.Add("doctor.protocol", "protocol", StatusPass, "afterburner.ui protocol is available", protocol.CurrentRevision())
	report.Add("doctor.components", "components", StatusPass, "component catalog is available", map[string]any{"supported": len(SortedSupportedComponentKinds())})
	report.Add("doctor.capabilities", "capabilities", StatusPass, "core UI capability descriptors are available", map[string]any{"count": len(capability.CoreDescriptors())})
	if _, err := LoadFixture("all-components"); err != nil {
		report.Add("doctor.fixtures", "fixtures", StatusFail, err.Error(), nil)
	} else {
		report.Add("doctor.fixtures", "fixtures", StatusPass, "built-in certification fixtures are available", map[string]any{"fixtures": FixtureNames()})
	}
	if homeRoot != "" {
		if entry, ok, err := loadRegistryEntry(homeRoot, "black-box"); err == nil && ok && entry.Enabled {
			report.Add("doctor.black-box", "observability", StatusPass, "Black Box is installed and enabled as an optional metadata sink", manifestBrief(entry))
		} else if err != nil {
			report.Add("doctor.black-box", "observability", StatusWarn, err.Error(), nil)
		} else {
			report.Add("doctor.black-box", "observability", StatusInfo, "Black Box is not installed or not enabled; UI tooling remains metadata-only", nil)
		}
	}
	return report.Finalize()
}

func InspectInstalled(homeRoot, extensionID string) (Report, *ManifestBrief, error) {
	report := NewReport(extensionID, "")
	entry, ok, err := loadRegistryEntry(homeRoot, extensionID)
	if err != nil {
		return report, nil, err
	}
	if !ok {
		report.Add("inspect.registry", "manifest", StatusFail, "extension is not installed", nil)
		final := report.Finalize()
		return final, nil, nil
	}
	brief := manifestBrief(entry)
	report.Add("inspect.registry", "manifest", StatusPass, "extension registry entry loaded", brief)
	manifestPath := filepath.Join(entry.ActivePath, "afterburner.json")
	validation := ValidateManifest(manifestPath)
	status := StatusPass
	if !validation.Valid {
		status = StatusFail
	}
	report.Add("inspect.manifest", "manifest", status, "extension manifest validation completed", validation)
	if validation.UI != nil {
		report.Add("inspect.ui", "manifest", StatusPass, "extension declares afterburner.ui surfaces", validation.UI)
	} else {
		report.Add("inspect.ui", "manifest", StatusInfo, "extension has no optional afterburner.ui declaration", nil)
	}
	final := report.Finalize()
	return final, &brief, nil
}

func validateProtocolRoundTrip(ctx context.Context) error {
	envelope := protocol.NewEnvelope(protocol.EnvelopeHello, "sim-hello", protocol.Actor{Kind: protocol.ActorExtension, ID: "sim"}, protocol.Actor{Kind: protocol.ActorHost, ID: "host"}, rawProps(map[string]any{"revision": protocol.CurrentRevision()}))
	dispatcher := protocol.Dispatcher{Handlers: map[protocol.EnvelopeKind]protocol.Handler{protocol.EnvelopeHello: func(context.Context, protocol.Envelope) error { return nil }}}
	return dispatcher.Dispatch(ctx, envelope)
}

func loadRegistryEntry(homeRoot, extensionID string) (registry.Entry, bool, error) {
	if homeRoot == "" || extensionID == "" {
		return registry.Entry{}, false, nil
	}
	reg, err := registry.Load(homeRoot)
	if err != nil {
		return registry.Entry{}, false, err
	}
	entry, ok := reg.Extensions[extensionID]
	return entry, ok, nil
}

func manifestBrief(entry registry.Entry) ManifestBrief {
	brief := ManifestBrief{ID: entry.Manifest.ID, DisplayName: entry.Manifest.DisplayName, Enabled: entry.Enabled, ActivePath: entry.ActivePath, Capabilities: append([]string(nil), entry.Manifest.Capabilities...)}
	if len(entry.Manifest.UI) > 0 {
		if ui, errs, _ := validateUIDeclaration(entry.Manifest.UI); len(errs) == 0 && ui != nil {
			brief.UIComponents = append([]string(nil), ui.Components...)
			brief.UICapabilities = append([]string(nil), ui.Capabilities...)
			for _, s := range ui.Surfaces {
				brief.UISurfaces = append(brief.UISurfaces, s.ID+":"+s.Kind)
			}
		}
	}
	sort.Strings(brief.Capabilities)
	sort.Strings(brief.UIComponents)
	sort.Strings(brief.UISurfaces)
	sort.Strings(brief.UICapabilities)
	return brief
}

func EvaluateGrantScenario(extensionID string) Report {
	report := NewReport(extensionID, "")
	store := policy.NewGrantStore(func() time.Time { return DeterministicTime })
	engine := policy.NewEngine(store, policy.EnterprisePolicy{SchemaVersion: 1, Version: "cert", DenyByDefault: true})
	_ = engine.RegisterExtension(policy.ExtensionState{ID: extensionID, DeclaredCapabilities: []capability.ID{capability.ActionInvoke}})
	before := engine.Evaluate(policy.Request{ExtensionID: extensionID, Capability: capability.ActionInvoke, Resource: "action", At: DeterministicTime})
	store.Grant(extensionID, capability.GrantDescriptor{ID: "cert-action", Capability: capability.ActionInvoke, Resources: []string{"action"}}, nil, "certification")
	after := engine.Evaluate(policy.Request{ExtensionID: extensionID, Capability: capability.ActionInvoke, Resource: "action", At: DeterministicTime})
	if before.Result != "deny" || after.Result != "allow" {
		report.Add("grants.evaluate", "security", StatusFail, "grant policy did not deny before grant and allow after grant", map[string]any{"before": before, "after": after})
	} else {
		report.Add("grants.evaluate", "security", StatusPass, "grant policy denies by default and allows active grants", map[string]any{"before": before.Reason, "after": after.MatchedGrant})
	}
	return report.Finalize()
}
