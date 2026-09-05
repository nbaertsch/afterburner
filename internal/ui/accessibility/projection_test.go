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
	modal := byID["sdk-dialog-modal"]
	if modal.States["modal"] != "true" || !hasShortcut(modal.KeyboardActions, "Tab", "cycle focus") {
		t.Fatalf("modal dialog semantics mismatch: %#v", modal)
	}
	nonModal := byID["sdk-dialog"]
	if nonModal.States["modal"] != "false" || hasShortcut(nonModal.KeyboardActions, "Tab", "cycle focus") {
		t.Fatalf("non-modal dialog semantics mismatch: %#v", nonModal)
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
	return []string{"application", "window", "surface", "viewport", "stack", "row", "grid", "panel", "card", "separator", "spacer", "text", "markdown", "code", "icon", "badge", "button", "link", "textInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "progress", "spinner", "list", "table", "tree", "form", "toolbar", "tabs", "breadcrumb", "dialog", "toast", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet"}
}

func sdkProps(kind string) map[string]any {
	switch kind {
	case "text":
		return map[string]any{"value": "SDK text"}
	case "markdown":
		return map[string]any{"markdown": "**SDK markdown**"}
	case "code":
		return map[string]any{"code": "fmt.Println(\"sdk\")", "language": "go"}
	case "button", "textInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider":
		return map[string]any{"label": kind + " control", "value": "sample", "checked": true, "options": []string{"one", "two"}}
	case "list", "tree", "tabs", "breadcrumb":
		return map[string]any{"items": []string{"alpha", "beta"}, "selected": "alpha"}
	case "table":
		return map[string]any{"columns": []string{"name", "status"}, "rows": [][]string{{"sdk", "ok"}}}
	case "progress", "chart":
		return map[string]any{"value": 0.5, "label": kind}
	case "terminal", "canvas", "image", "video":
		return map[string]any{"title": kind, "alt": kind + " alternative text"}
	case "toast":
		return map[string]any{"message": "toast message"}
	case "dialog":
		return map[string]any{"title": "Non-modal dialog", "modal": false}
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
