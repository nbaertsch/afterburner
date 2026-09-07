package accessibility_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/accessibility"
	"github.com/nbaertsch/afterburner/internal/ui/component"
)

func TestProjectionCoversPublicComponentCatalog(t *testing.T) {
	children := make([]accessibility.SourceNode, 0, len(component.PublicCatalog()))
	for _, entry := range component.PublicCatalog() {
		if entry.Kind == component.KindApplication {
			continue
		}
		kind := string(entry.Kind)
		children = append(children, accessibility.SourceNode{
			ID:    "public-" + strings.ToLower(strings.ReplaceAll(kind, ":", "-")),
			Kind:  kind,
			Props: sdkProps(kind),
		})
	}
	data, err := json.Marshal(accessibility.SourceTree{
		SurfaceID: "public-catalog",
		Root: accessibility.SourceNode{
			ID:       "public-root",
			Kind:     string(component.KindApplication),
			Props:    map[string]any{"title": "Public catalog"},
			Children: children,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tree accessibility.SourceTree
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	projected := accessibility.Project(tree, accessibility.ProjectionOptions{KeyboardOnly: true, Now: func() time.Time { return time.Unix(3, 0).UTC() }})
	byID := map[string]accessibility.SemanticNode{}
	var index func(accessibility.SemanticNode)
	index = func(node accessibility.SemanticNode) {
		byID[node.ID] = node
		for _, child := range node.Children {
			index(child)
		}
	}
	index(projected.Root)
	for _, entry := range component.PublicCatalog() {
		kind := string(entry.Kind)
		id := "public-" + strings.ToLower(strings.ReplaceAll(kind, ":", "-"))
		if entry.Kind == component.KindApplication {
			id = "public-root"
		}
		node, ok := byID[id]
		if !ok {
			t.Fatalf("missing semantic projection for public kind %s", kind)
		}
		if node.Role == "" && node.Name == "" {
			t.Fatalf("blank semantic projection for public kind %s: %#v", kind, node)
		}
	}
}

func sdkProps(kind string) map[string]any {
	switch kind {
	case "text":
		return map[string]any{"value": "SDK text"}
	case "markdown":
		return map[string]any{"markdown": "**SDK markdown**"}
	case "code":
		return map[string]any{"code": "fmt.Println(\"sdk\")", "language": "go"}
	case "button", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput":
		return map[string]any{"label": kind + " control", "value": "sample", "checked": true, "options": []string{"one", "two"}}
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
