package tooling

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/accessibility"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/render"
)

type Fixture struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tree        component.Tree `json:"tree"`
	Abuse       bool           `json:"abuse,omitempty"`
}

type RenderOptions struct {
	Width     int
	Height    int
	Theme     string
	ColorMode render.ColorMode
	Unicode   bool
	Plain     bool
}

type RenderResult struct {
	Fixture       string                     `json:"fixture"`
	Frame         render.Frame               `json:"frame"`
	Accessibility accessibility.SemanticTree `json:"accessibility"`
	Components    []string                   `json:"components"`
	Warnings      []string                   `json:"warnings,omitempty"`
}

func FixtureNames() []string {
	return []string{"all-components", "component-gallery", "malformed-envelope", "abuse", "black-box-certification"}
}

func LoadFixture(nameOrPath string) (Fixture, error) {
	if nameOrPath == "" {
		nameOrPath = "all-components"
	}
	if data, err := os.ReadFile(nameOrPath); err == nil {
		var fixture Fixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			return Fixture{}, err
		}
		if fixture.Name == "" {
			fixture.Name = filepath.Base(nameOrPath)
		}
		return fixture, nil
	}
	switch strings.ToLower(nameOrPath) {
	case "all-components", "all", "component-catalog":
		return AllComponentsFixture(), nil
	case "component-gallery", "gallery", "design-gallery":
		return ComponentGalleryFixture(), nil
	case "malformed-envelope", "malformed":
		return MalformedEnvelopeFixture(), nil
	case "abuse", "abuse-components":
		return AbuseFixture(), nil
	case "black-box-certification", "black-box":
		return BlackBoxCertificationFixture(), nil
	default:
		return Fixture{}, fmt.Errorf("unknown fixture %q (known: %s)", nameOrPath, strings.Join(FixtureNames(), ", "))
	}
}

func RenderFixture(ctx context.Context, nameOrPath string, opts RenderOptions) (RenderResult, error) {
	fixture, err := LoadFixture(nameOrPath)
	if err != nil {
		return RenderResult{}, err
	}
	options := render.Options{Width: opts.Width, Height: opts.Height, Theme: themeByID(opts.Theme), ColorMode: opts.ColorMode, Unicode: opts.Unicode, Now: func() time.Time { return DeterministicTime }, FailureMode: render.FailurePlain}
	if options.Width == 0 {
		options.Width = 96
	}
	if options.Height == 0 {
		options.Height = 40
	}
	var engine render.Engine
	if opts.Plain {
		engine = render.NewPlainRenderer(options)
	} else {
		engine = render.NewFailoverRenderer(render.NewBubbleRenderer(options), render.NewPlainRenderer(options))
	}
	frame, err := engine.RenderFrame(ctx, fixture.Tree)
	if err != nil {
		return RenderResult{}, err
	}
	semantic := accessibility.Project(ToAccessibilitySource(fixture.Tree), accessibility.ProjectionOptions{HighContrast: strings.Contains(strings.ToLower(opts.Theme), "contrast"), NoColor: opts.ColorMode == render.ColorModeMono, KeyboardOnly: true, Now: func() time.Time { return DeterministicTime }})
	return RenderResult{Fixture: fixture.Name, Frame: frame, Accessibility: semantic, Components: componentKinds(fixture.Tree)}, nil
}

func AllComponentsFixture() Fixture {
	kinds := SortedSupportedComponentKinds()
	children := make([]component.Node, 0, len(kinds))
	for _, kind := range kinds {
		if kind == string(component.KindApplication) || kind == "root" {
			continue
		}
		children = append(children, sampleNode(component.Kind(kind)))
	}
	return Fixture{Name: "all-components", Description: "Every public and host-rendered component kind in a deterministic surface.", Tree: component.Tree{Root: component.Node{ID: "all-components-root", Kind: component.KindApplication, Props: rawProps(map[string]any{"title": "Afterburner UI Fixture"}), Children: children}, Revision: 1, SurfaceID: "fixture-all-components", ThemeID: "afterburner.dark", Locale: "en-US", Capabilities: []string{"ui.render.components", "ui.accessibility.inspect"}}}
}

func ComponentGalleryFixture() Fixture {
	section := func(id, title string, children ...component.Node) component.Node {
		return component.Node{ID: id, Kind: component.KindSection, Props: rawProps(map[string]any{"title": title}), Children: children, Accessibility: &accessibility.Node{Name: title}}
	}
	text := func(id, value string) component.Node {
		return component.Node{ID: id, Kind: component.KindText, Props: rawProps(map[string]any{"value": value})}
	}
	return Fixture{Name: "component-gallery", Description: "Curated extension author gallery showing composable public UI primitives in grouped layouts.", Tree: component.Tree{Root: component.Node{ID: "component-gallery-root", Kind: component.KindApplication, Props: rawProps(map[string]any{"title": "Afterburner UI Component Gallery"}), Children: []component.Node{
		section("gallery-layout", "Layout and structure",
			component.Node{ID: "gallery-layout-split", Kind: component.KindSplit, Children: []component.Node{
				{ID: "gallery-layout-card", Kind: component.KindCard, Props: rawProps(map[string]any{"title": "Composable card"}), Children: []component.Node{text("gallery-layout-card-text", "Cards, panels, sections, rows, columns, grids, and split panes can be nested.")}},
				{ID: "gallery-layout-disclosure", Kind: component.KindDisclosure, Props: rawProps(map[string]any{"title": "Expandable details", "expanded": true}), Children: []component.Node{text("gallery-layout-disclosure-text", "Disclosure content remains keyboard and screen-reader reachable.")}},
			}},
		),
		section("gallery-status", "Status and feedback",
			component.Node{ID: "gallery-status-grid", Kind: component.KindStatusGrid, Props: rawProps(map[string]any{"columns": []string{"name", "status"}, "rows": [][]string{{"Recorder", "healthy"}, {"Storage", "warning"}}})},
			component.Node{ID: "gallery-alert", Kind: component.KindAlert, Props: rawProps(map[string]any{"severity": "warning", "message": "Warnings, toasts, loading, and error boundaries preserve nested context."}), Children: []component.Node{text("gallery-alert-child", "Nested remediation text is visible in plain and terminal renderers.")}},
			component.Node{ID: "gallery-progress", Kind: component.KindProgress, Props: rawProps(map[string]any{"label": "Storage", "value": 65, "min": 0, "max": 100})},
			component.Node{ID: "gallery-sparkline", Kind: component.KindSparkline, Props: rawProps(map[string]any{"label": "Trend", "values": []float64{1, 3, 2, 5, 4, 8}})},
		),
		section("gallery-data", "Collections and data",
			component.Node{ID: "gallery-table", Kind: component.KindTable, Props: rawProps(map[string]any{"columns": []string{"Component", "State"}, "rows": [][]string{{"table", "ready"}, {"timeline", "ready"}}})},
			component.Node{ID: "gallery-timeline", Kind: component.KindTimeline, Props: rawProps(map[string]any{"items": []string{"session.start", "model.changed", "task.complete"}})},
			component.Node{ID: "gallery-log", Kind: component.KindLog, Props: rawProps(map[string]any{"items": []string{"metadata only", "redacted payload", "export ready"}})},
		),
		section("gallery-actions", "Inputs and actions",
			component.Node{ID: "gallery-action-bar", Kind: component.KindActionBar, Children: []component.Node{
				{ID: "gallery-refresh", Kind: component.KindButton, Props: rawProps(map[string]any{"label": "Refresh"})},
				{ID: "gallery-search", Kind: component.KindSearchInput, Props: rawProps(map[string]any{"label": "Search", "placeholder": "Filter records"})},
				{ID: "gallery-toggle", Kind: component.KindToggle, Props: rawProps(map[string]any{"label": "Live", "checked": true})},
			}},
			component.Node{ID: "gallery-prompt", Kind: component.KindPrompt, Props: rawProps(map[string]any{"title": "Confirm export", "message": "Export sanitized metadata?"}), Children: []component.Node{text("gallery-prompt-child", "Prompts can embed explanatory child content.")}},
		),
	}}, Revision: 1, SurfaceID: "fixture-component-gallery", ThemeID: "afterburner.dark", Locale: "en-US", Capabilities: []string{"ui.render.components", "ui.accessibility.inspect"}}}
}

func MalformedEnvelopeFixture() Fixture {
	return Fixture{Name: "malformed-envelope", Description: "Intentionally malformed protocol envelope represented as text for validator abuse tests.", Tree: component.Tree{Root: component.Node{ID: "malformed-root", Kind: component.KindApplication, Children: []component.Node{{ID: "malformed-text", Kind: component.KindCode, Props: rawProps(map[string]any{"language": "json", "code": `{"schemaVersion":1,"protocol":"afterburner.ui","revision":999,"payload":"missing required actor fields"}`})}}}, Revision: 1, SurfaceID: "fixture-malformed"}}
}

func AbuseFixture() Fixture {
	return Fixture{Name: "abuse", Description: "Oversized, hostile, and fallback-triggering component cases without sensitive content.", Abuse: true, Tree: component.Tree{Root: component.Node{ID: "abuse-root", Kind: component.KindApplication, Props: rawProps(map[string]any{"title": "Abuse Fixture"}), Children: []component.Node{
		{ID: "ansi-text", Kind: component.KindText, Props: rawProps(map[string]any{"text": "ANSI should be stripped: \u001b[31mred\u001b[0m"})},
		{ID: "wide-text", Kind: component.KindText, Props: rawProps(map[string]any{"text": strings.Repeat("界", 32)})},
		{ID: "large-list", Kind: component.KindList, Props: rawProps(map[string]any{"items": []string{"alpha", "beta", "gamma", "delta", "epsilon"}})},
	}}, Revision: 1, SurfaceID: "fixture-abuse", Capabilities: []string{"ui.render.components"}}}
}

func BlackBoxCertificationFixture() Fixture {
	return Fixture{Name: "black-box-certification", Description: "Metadata-only Black Box observability fixture using public afterburner.ui contracts.", Tree: component.Tree{Root: component.Node{ID: "black-box-cert-root", Kind: component.KindApplication, Props: rawProps(map[string]any{"title": "Black Box Certification"}), Children: []component.Node{
		{ID: "metadata-panel", Kind: component.KindPanel, Props: rawProps(map[string]any{"title": "Metadata-only Observability"}), Children: []component.Node{
			{ID: "privacy", Kind: component.KindText, Props: rawProps(map[string]any{"text": "Prompts, responses, tool arguments, and source bodies are excluded."})},
			{ID: "events", Kind: component.KindTable, Props: rawProps(map[string]any{"columns": []string{"eventType", "severity", "durationMs"}, "rows": [][]string{{"ui.observation", "info", "12"}}})},
			{ID: "export", Kind: component.KindButton, Props: rawProps(map[string]any{"label": "Export sanitized bundle"}), ActionBindings: map[string]string{"press": "black-box.export"}},
		}},
	}}, Revision: 1, SurfaceID: "fixture-black-box", ThemeID: "afterburner.highContrast", Capabilities: []string{"ui.observability.black-box.sink", "ui.accessibility.inspect"}}}
}

func sampleNode(kind component.Kind) component.Node {
	id := strings.ReplaceAll(string(kind), ".", "-")
	node := component.Node{ID: "cmp-" + id, Kind: kind, Accessibility: &accessibility.Node{Name: string(kind)}}
	switch kind {
	case component.KindText, component.KindMarkdown, component.KindCode, component.KindIcon, component.KindBadge, component.KindLink, component.KindKeybindingHint:
		node.Props = rawProps(map[string]any{"text": string(kind) + " sample", "label": string(kind) + " label", "href": "https://example.invalid"})
	case component.KindButton, component.KindCheckbox, component.KindToggle, component.KindSelect, component.KindRadioGroup, component.KindSlider, component.KindTextInput, component.KindTextArea:
		node.Props = rawProps(map[string]any{"label": string(kind) + " control", "value": "sample", "checked": true, "options": []string{"one", "two"}})
	case component.KindList, component.KindTree, component.KindTabs, component.KindBreadcrumb:
		node.Props = rawProps(map[string]any{"items": []string{"alpha", "beta", "gamma"}, "selected": "alpha"})
	case component.KindTable, "statusGrid":
		node.Props = rawProps(map[string]any{"columns": []string{"name", "status"}, "rows": [][]string{{string(kind), "ok"}}})
	case component.KindProgress, "meter", "bar", "sparkline":
		node.Props = rawProps(map[string]any{"value": 0.65, "label": string(kind)})
	case component.KindImage, component.KindVideo, component.KindChart, component.KindTerminal, component.KindCanvas:
		node.Props = rawProps(map[string]any{"title": string(kind), "alt": string(kind) + " alternative text"})
	default:
		node.Props = rawProps(map[string]any{"title": string(kind)})
		node.Children = []component.Node{{ID: "cmp-" + id + "-text", Kind: component.KindText, Props: rawProps(map[string]any{"text": string(kind)})}}
	}
	return node
}

func ToAccessibilitySource(tree component.Tree) accessibility.SourceTree {
	return accessibility.SourceTree{Root: toAccessibilityNode(tree.Root), SurfaceID: tree.SurfaceID, Locale: tree.Locale}
}

func toAccessibilityNode(node component.Node) accessibility.SourceNode {
	props := map[string]any{}
	if len(node.Props) > 0 {
		_ = json.Unmarshal(node.Props, &props)
	}
	children := make([]accessibility.SourceNode, 0, len(node.Children))
	for _, child := range node.Children {
		children = append(children, toAccessibilityNode(child))
	}
	return accessibility.SourceNode{ID: node.ID, Kind: string(node.Kind), Props: props, Accessibility: node.Accessibility, Children: children}
}

func componentKinds(tree component.Tree) []string {
	set := map[string]bool{}
	var walk func(component.Node)
	walk = func(node component.Node) {
		set[string(node.Kind)] = true
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(tree.Root)
	out := make([]string, 0, len(set))
	for kind := range set {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func rawProps(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func themeByID(id string) render.Theme {
	switch strings.ToLower(id) {
	case "light", "afterburner.light":
		return render.LightTheme()
	case "high-contrast", "contrast", "afterburner.highcontrast":
		return render.HighContrastTheme()
	default:
		return render.DefaultTheme()
	}
}
