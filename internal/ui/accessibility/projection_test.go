package accessibility

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectionLinearizationAndLiveSummary(t *testing.T) {
	checked := true
	tree := SourceTree{SurfaceID: "s", Locale: "en-US", Root: SourceNode{ID: "app", Kind: "application", Props: map[string]any{"label": "Afterburner"}, Children: []SourceNode{
		{ID: "name", Kind: "textInput", Props: map[string]any{"label": "Name", "required": true}},
		{ID: "agree", Kind: "checkbox", Accessibility: &Node{Name: "Agree", Checked: &checked}},
		{ID: "toast", Kind: "toast", Props: map[string]any{"message": "Saved"}, Accessibility: &Node{Live: LivePolite, Name: "Saved"}},
	}}}
	projected := Project(tree, ProjectionOptions{KeyboardOnly: true, HighContrast: true, NoColor: true, Now: func() time.Time { return time.Unix(1, 0).UTC() }})
	lines := strings.Join(Linearize(projected), "\n") + "\n"
	want, err := os.ReadFile(filepath.Join("testdata", "golden", "linearized.golden"))
	if err != nil {
		t.Fatal(err)
	}
	wantText := strings.ReplaceAll(string(want), "\r\n", "\n")
	if lines != wantText {
		t.Fatalf("linearization mismatch\nwant:\n%q\ngot:\n%q", wantText, lines)
	}
	if projected.LiveSummary != "Saved" || !projected.HighContrast || !projected.NoColor {
		t.Fatalf("projection metadata mismatch: %#v", projected)
	}
}

func TestSDKShapedCatalogProjectionAndDialogModality(t *testing.T) {
	tree := sdkSourceTree(t)
	projected := Project(tree, ProjectionOptions{KeyboardOnly: true, Now: func() time.Time { return time.Unix(2, 0).UTC() }})
	byID := map[string]SemanticNode{}
	var index func(SemanticNode)
	index = func(node SemanticNode) {
		byID[node.ID] = node
		for _, child := range node.Children {
			index(child)
		}
	}
	index(projected.Root)
	for _, kind := range sdkCatalogKinds() {
		id := "sdk-" + strings.ToLower(kind)
		if kind == "application" {
			id = "sdk-root"
		}
		node, ok := byID[id]
		if !ok {
			t.Fatalf("missing projected SDK component %s (%s)", kind, id)
		}
		if node.Role == "" && node.Name == "" {
			t.Fatalf("blank semantic node for SDK component %s: %#v", kind, node)
		}
	}
	if got := byID["sdk-markdown"].Name; !strings.Contains(got, "SDK markdown") {
		t.Fatalf("markdown prop did not provide accessible name: %q", got)
	}
	if got := byID["sdk-code"].Name; !strings.Contains(got, "fmt.Println") {
		t.Fatalf("code prop did not provide accessible name: %q", got)
	}
	if byID["sdk-pagination"].Role != RoleNavigation || !hasShortcut(byID["sdk-pagination"].KeyboardActions, "Arrow keys", "navigate") {
		t.Fatalf("pagination should project as keyboard navigation: %#v", byID["sdk-pagination"])
	}
	if byID["sdk-breadcrumb"].Role != RoleNavigation {
		t.Fatalf("breadcrumb should project as a navigation landmark: %#v", byID["sdk-breadcrumb"])
	}
	if !hasShortcut(byID["sdk-dateinput"].KeyboardActions, "Enter", "confirm date") || !hasShortcut(byID["sdk-fileinput"].KeyboardActions, "Enter", "browse files") {
		t.Fatalf("date/file inputs should expose keyboard actions: date=%#v file=%#v", byID["sdk-dateinput"], byID["sdk-fileinput"])
	}
	if byID["sdk-contextmenu"].Role != RoleMenu || !hasShortcut(byID["sdk-contextmenu"].KeyboardActions, "Enter", "select") {
		t.Fatalf("context menu should project as keyboard menu: %#v", byID["sdk-contextmenu"])
	}
	if !byID["sdk-actionbar"].Focusable || !byID["sdk-toolbar"].Focusable {
		t.Fatalf("action bars and toolbars should be focusable: action=%#v toolbar=%#v", byID["sdk-actionbar"], byID["sdk-toolbar"])
	}
	if !hasShortcut(byID["sdk-terminal"].KeyboardActions, "PageUp/PageDown", "page terminal output") {
		t.Fatalf("terminal should expose keyboard paging semantics: %#v", byID["sdk-terminal"])
	}
	if byID["sdk-keybindinghint"].Role != RoleTooltip {
		t.Fatalf("keybinding hints should project as tooltips: %#v", byID["sdk-keybindinghint"])
	}
	if byID["sdk-loading"].Live != LivePolite || byID["sdk-spinner"].Live != LivePolite || byID["sdk-loading"].States["busy"] != "true" || byID["sdk-spinner"].States["busy"] != "true" {
		t.Fatalf("loading indicators should be polite busy live regions: loading=%#v spinner=%#v", byID["sdk-loading"], byID["sdk-spinner"])
	}
	if byID["sdk-slider"].States["value"] != "0.5" || byID["sdk-progress"].States["value"] != "0.5" || byID["sdk-meter"].States["value"] != "0.5" || byID["sdk-bar"].States["value"] != "0.5" {
		t.Fatalf("range-like components should expose value state: slider=%#v progress=%#v meter=%#v bar=%#v", byID["sdk-slider"], byID["sdk-progress"], byID["sdk-meter"], byID["sdk-bar"])
	}
	modal := byID["sdk-dialog-modal"]
	if modal.States["modal"] != "true" || !hasShortcut(modal.KeyboardActions, "Tab", "cycle focus") {
		t.Fatalf("modal dialog semantics mismatch: %#v", modal)
	}
	nonModal := byID["sdk-dialog"]
	if nonModal.States["modal"] != "false" || hasShortcut(nonModal.KeyboardActions, "Tab", "cycle focus") {
		t.Fatalf("non-modal dialog semantics mismatch: %#v", nonModal)
	}
}

func TestHiddenNodesAreRemovedFromAccessibilityProjection(t *testing.T) {
	tree := SourceTree{SurfaceID: "s", Root: SourceNode{ID: "root", Kind: "application", Children: []SourceNode{
		{ID: "visible", Kind: "text", Props: map[string]any{"text": "Visible"}},
		{ID: "hidden-prop", Kind: "text", Props: map[string]any{"text": "Hidden prop", "hidden": true}},
		{ID: "hidden-a11y", Kind: "text", Props: map[string]any{"text": "Hidden a11y"}, Accessibility: &Node{Hidden: true}},
	}}}
	projected := Project(tree, ProjectionOptions{KeyboardOnly: true, Now: func() time.Time { return time.Unix(5, 0).UTC() }})
	lines := strings.Join(Linearize(projected), "\n")
	if !strings.Contains(lines, "Visible") || strings.Contains(lines, "Hidden prop") || strings.Contains(lines, "Hidden a11y") {
		t.Fatalf("hidden nodes should be removed from accessibility output: %q", lines)
	}
}

func TestSecretInputsDoNotExposeValueAsAccessibleName(t *testing.T) {
	tree := SourceTree{SurfaceID: "s", Root: SourceNode{ID: "root", Kind: "application", Children: []SourceNode{
		{ID: "password", Kind: "passwordInput", Props: map[string]any{"value": "super-secret", "placeholder": "Password"}},
		{ID: "typed", Kind: "textInput", Props: map[string]any{"type": "password", "value": "hidden-token"}},
	}}}
	projected := Project(tree, ProjectionOptions{KeyboardOnly: true, Now: func() time.Time { return time.Unix(4, 0).UTC() }})
	lines := strings.Join(Linearize(projected), "\n")
	if strings.Contains(lines, "super-secret") || strings.Contains(lines, "hidden-token") {
		t.Fatalf("secret input value leaked in accessibility projection: %q", lines)
	}
	if !strings.Contains(lines, "Password") || !strings.Contains(lines, "typed") {
		t.Fatalf("secret inputs should keep safe labels or IDs: %q", lines)
	}
}

func TestAriaPropsPopulateAccessibilityProjection(t *testing.T) {
	tree := SourceTree{SurfaceID: "s", Root: SourceNode{ID: "root", Kind: "application", Children: []SourceNode{
		{ID: "assertive", Kind: "statusGrid", Props: map[string]any{"label": "Build failed", "ariaLive": "assertive", "ariaDescription": "See failed job details", "ariaInvalid": "spelling", "ariaCurrent": "page", "ariaPosInSet": 2, "ariaSetSize": 5, "ariaAtomic": true, "ariaRelevant": []any{"additions", "text"}, "ariaLabelledBy": "heading summary", "ariaDescribedBy": []any{"details", "hint"}}},
		{ID: "quiet", Kind: "alert", Props: map[string]any{"message": "Saved", "live": "off"}},
	}}}
	projected := Project(tree, ProjectionOptions{KeyboardOnly: true, Now: func() time.Time { return time.Unix(6, 0).UTC() }})
	byID := map[string]SemanticNode{}
	var index func(SemanticNode)
	index = func(node SemanticNode) {
		byID[node.ID] = node
		for _, child := range node.Children {
			index(child)
		}
	}
	index(projected.Root)
	if byID["assertive"].Live != LiveAssertive || byID["quiet"].Live != LiveOff {
		t.Fatalf("live props should control announcement politeness: assertive=%#v quiet=%#v", byID["assertive"], byID["quiet"])
	}
	assertive := byID["assertive"]
	if assertive.Description != "See failed job details" || assertive.States["invalid"] != "spelling" || assertive.States["current"] != "page" || assertive.States["positionInSet"] != "2" || assertive.States["setSize"] != "5" || assertive.States["atomic"] != "true" || assertive.States["relevant"] != "additions text" {
		t.Fatalf("ARIA state aliases should populate accessible description and states: %#v", assertive)
	}
	if strings.Join(assertive.Relations["labelledBy"], ",") != "heading,summary" || strings.Join(assertive.Relations["describedBy"], ",") != "details,hint" {
		t.Fatalf("ARIA relation aliases should populate relation IDs: %#v", assertive.Relations)
	}
	if projected.LiveSummary != "Build failed See failed job details" {
		t.Fatalf("live summary should include only active live regions: %q", projected.LiveSummary)
	}
}

func TestLiveRegionThrottlerSummarizes(t *testing.T) {
	throttler := LiveRegionThrottler{MinInterval: time.Second, MaxItems: 2}
	now := time.Unix(1, 0)
	if msg, ok := throttler.Push(now, "one"); !ok || msg != "one" {
		t.Fatalf("first live message = %q %v", msg, ok)
	}
	if msg, ok := throttler.Push(now.Add(100*time.Millisecond), "two"); ok || msg != "" {
		t.Fatalf("throttled message = %q %v", msg, ok)
	}
	throttler.Push(now.Add(200*time.Millisecond), "three")
	throttler.Push(now.Add(300*time.Millisecond), "four")
	if got := throttler.Flush(now.Add(time.Second)); got != "two; three; 1 more updates" {
		t.Fatalf("summary = %q", got)
	}
}

func TestHighContrastTokensHaveNoColorMode(t *testing.T) {
	if HighContrastTokens(true)["error"] != "ERROR" || HighContrastTokens(false)["error"] == "" {
		t.Fatal("missing high-contrast/no-color tokens")
	}
}

func sdkSourceTree(t *testing.T) SourceTree {
	t.Helper()
	children := make([]SourceNode, 0, len(sdkCatalogKinds())+1)
	for _, kind := range sdkCatalogKinds() {
		if kind == "application" {
			continue
		}
		children = append(children, SourceNode{ID: "sdk-" + strings.ToLower(kind), Kind: kind, Props: sdkProps(kind)})
	}
	children = append(children, SourceNode{ID: "sdk-dialog-modal", Kind: "dialog", Props: map[string]any{"title": "Modal dialog", "modal": true}})
	data, err := json.Marshal(SourceTree{SurfaceID: "sdk-contract", Root: SourceNode{ID: "sdk-root", Kind: "application", Props: map[string]any{"title": "SDK app"}, Children: children}})
	if err != nil {
		t.Fatal(err)
	}
	var tree SourceTree
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}

func sdkCatalogKinds() []string {
	return []string{"application", "window", "surface", "viewport", "stack", "column", "row", "grid", "box", "section", "split", "scroll", "disclosure", "statusGrid", "panel", "card", "separator", "spacer", "empty", "text", "markdown", "code", "icon", "badge", "keyValue", "detail", "alert", "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "progress", "meter", "bar", "sparkline", "spinner", "loading", "list", "table", "tree", "timeline", "log", "form", "toolbar", "actionBar", "contextMenu", "tabs", "breadcrumb", "pagination", "help", "dialog", "toast", "errorBoundary", "confirmation", "prompt", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet"}
}

func sdkProps(kind string) map[string]any {
	switch kind {
	case "text":
		return map[string]any{"value": "SDK text"}
	case "markdown":
		return map[string]any{"markdown": "**SDK markdown**"}
	case "code":
		return map[string]any{"code": "fmt.Println(\"sdk\")", "language": "go"}
	case "button", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "dateInput", "fileInput":
		return map[string]any{"label": kind + " control", "value": "sample", "checked": true, "options": []string{"one", "two"}}
	case "slider":
		return map[string]any{"label": kind + " control", "value": 0.5, "min": 0, "max": 1}
	case "list", "tree", "timeline", "log", "contextMenu", "tabs", "breadcrumb":
		return map[string]any{"items": []string{"alpha", "beta"}, "selected": "alpha"}
	case "table", "grid", "statusGrid":
		return map[string]any{"columns": []string{"name", "status"}, "rows": [][]string{{"sdk", "ok"}}}
	case "progress", "meter", "bar", "sparkline", "chart":
		return map[string]any{"value": 0.5, "label": kind}
	case "terminal", "canvas", "image", "video":
		return map[string]any{"title": kind, "alt": kind + " alternative text"}
	case "toast", "alert":
		return map[string]any{"message": kind + " message"}
	case "dialog", "confirmation", "prompt":
		return map[string]any{"title": kind + " dialog", "modal": false}
	case "empty", "loading", "help", "errorBoundary":
		return map[string]any{"message": kind + " message"}
	default:
		return map[string]any{"title": kind}
	}
}

func hasShortcut(shortcuts []Shortcut, key, description string) bool {
	for _, shortcut := range shortcuts {
		if shortcut.Key == key && shortcut.Description == description {
			return true
		}
	}
	return false
}
