package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/terminal"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

func TestGoldenComponentCatalogPlainSnapshot(t *testing.T) {
	tree := catalogTree(t)
	frame, err := NewPlainRenderer(testOptions(72, ColorModeMono, true, DefaultTheme())).RenderFrame(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	assertNoANSI(t, frame.Body)
	golden := filepath.Join("testdata", "golden", "component-catalog.plain.golden")
	if os.Getenv("UPDATE_RENDER_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(frame.Body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	expectedText := strings.ReplaceAll(string(expected), "\r\n", "\n")
	if expectedText != frame.Body+"\n" {
		t.Fatalf("plain golden mismatch\n--- expected\n%s\n--- actual\n%s", expectedText, frame.Body)
	}
}

func TestRendererMatrixAcrossWidthsThemesColorModesAndUnicode(t *testing.T) {
	tree := catalogTree(t)
	modes := []ColorMode{ColorModeTrueColor, ColorModeANSI256, ColorModeANSI16, ColorModeMono, ColorModeHighContrast}
	themes := []Theme{DefaultTheme(), LightTheme(), HighContrastTheme()}
	widths := []int{36, 60, 96}
	for _, theme := range themes {
		for _, mode := range modes {
			for _, width := range widths {
				for _, unicodeMode := range []bool{true, false} {
					name := theme.ID + "/" + string(mode) + "/" + strconvItoa(width) + "/" + strconvBool(unicodeMode)
					t.Run(name, func(t *testing.T) {
						frame, err := NewBubbleRenderer(testOptions(width, mode, unicodeMode, theme)).RenderFrame(context.Background(), tree)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(frame.Plain, "[31m") || strings.Contains(frame.Plain, "\x1b") {
							t.Fatalf("extension ANSI leaked: %q", frame.Plain)
						}
						for _, line := range strings.Split(frame.Plain, "\n") {
							if len([]rune(line)) > width+2 {
								t.Fatalf("line exceeded width %d: %q", width, line)
							}
						}
						if mode == ColorModeMono {
							assertNoANSI(t, frame.Body)
						}
						if !unicodeMode && strings.ContainsAny(frame.Plain, "╭╮╰╯│─•›▾▸☑☐█░…") {
							t.Fatalf("ascii mode emitted unicode chrome: %q", frame.Plain)
						}
					})
				}
			}
		}
	}
}

func TestSupportedCatalogIncludesW0RequestedComponents(t *testing.T) {
	want := []string{"root", "stack", "row", "column", "grid", "box", "panel", "card", "section", "split", "tabs", "scroll", "disclosure", "text", "markdown", "code", "keyValue", "detail", "badge", "link", "separator", "empty", "table", "list", "tree", "timeline", "log", "progress", "meter", "bar", "sparkline", "statusGrid", "form", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "button", "toolbar", "actionBar", "commandPalette", "contextMenu", "breadcrumb", "pagination", "help", "alert", "toast", "loading", "errorBoundary", "confirmation", "prompt"}
	got := map[string]bool{}
	for _, kind := range SupportedComponentKinds() {
		got[string(kind)] = true
	}
	for _, kind := range want {
		if !got[kind] {
			t.Fatalf("supported catalog missing %s", kind)
		}
		if _, err := NewPlainRenderer(testOptions(50, ColorModeMono, true, DefaultTheme())).RenderFrame(context.Background(), component.Tree{SurfaceID: "s", Revision: 1, Root: sampleNode(t, component.Kind(kind))}); err != nil {
			t.Fatalf("render %s: %v", kind, err)
		}
	}
}

func TestRendererConsumesSDKShapedCatalogProps(t *testing.T) {
	kinds := SupportedComponentKinds()
	for _, kind := range kinds {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			tree := sdkContractTree(t, kind)
			for _, cfg := range []struct {
				name  string
				width int
				plain bool
			}{
				{name: "plain-narrow", width: 32, plain: true},
				{name: "bubble-narrow", width: 32},
				{name: "bubble-wide", width: 80},
			} {
				t.Run(cfg.name, func(t *testing.T) {
					var (
						frame Frame
						err   error
					)
					opts := testOptions(cfg.width, ColorModeMono, false, DefaultTheme())
					if cfg.plain {
						frame, err = NewPlainRenderer(opts).RenderFrame(context.Background(), tree)
					} else {
						frame, err = NewBubbleRenderer(opts).RenderFrame(context.Background(), tree)
					}
					if err != nil {
						t.Fatal(err)
					}
					assertNoANSI(t, frame.Plain)
					if kind != component.KindSpacer && strings.TrimSpace(frame.Plain) == "" {
						t.Fatalf("%s rendered blank from SDK-shaped props", kind)
					}
					for _, line := range strings.Split(frame.Plain, "\n") {
						if len([]rune(line)) > cfg.width+2 {
							t.Fatalf("line exceeded width %d: %q", cfg.width, line)
						}
					}
				})
			}
		})
	}

	markdownFrame, err := NewPlainRenderer(testOptions(80, ColorModeMono, true, DefaultTheme())).RenderFrame(context.Background(), sdkContractTree(t, component.KindMarkdown))
	if err != nil || !strings.Contains(markdownFrame.Plain, "SDK markdown") {
		t.Fatalf("markdown prop not rendered: frame=%q err=%v", markdownFrame.Plain, err)
	}
	codeFrame, err := NewPlainRenderer(testOptions(80, ColorModeMono, true, DefaultTheme())).RenderFrame(context.Background(), sdkContractTree(t, component.KindCode))
	if err != nil || !strings.Contains(codeFrame.Plain, "fmt.Println") || !strings.Contains(codeFrame.Plain, "go") {
		t.Fatalf("code/language props not rendered: frame=%q err=%v", codeFrame.Plain, err)
	}
	failover := NewFailoverRenderer(failingEngine{}, NewPlainRenderer(testOptions(80, ColorModeMono, true, DefaultTheme())))
	fallback, err := failover.RenderFrame(context.Background(), sdkContractTree(t, kindStatusGrid))
	if err != nil || fallback.Renderer != RendererPlain || !strings.Contains(fallback.Plain, "ok") {
		t.Fatalf("failover SDK fixture mismatch: frame=%#v err=%v", fallback, err)
	}
}

func TestRendererConsumesSDKTableShapeAndProgressRanges(t *testing.T) {
	table := component.Tree{SurfaceID: "sdk-table", Revision: 1, Root: nodeWithProps(t, component.KindTable, sdkTableProps("Audit event", "Info", 65))}
	frame, err := NewPlainRenderer(testOptions(96, ColorModeMono, false, DefaultTheme())).RenderFrame(context.Background(), table)
	if err != nil {
		t.Fatal(err)
	}
	assertNoANSI(t, frame.Plain)
	for _, want := range []string{"Event Type", "Severity", "Duration", "Audit event", "Info", "65 ms"} {
		if !strings.Contains(frame.Plain, want) {
			t.Fatalf("SDK table render missing %q: %q", want, frame.Plain)
		}
	}
	for _, notWant := range []string{"row-1", "eventType", "{", "}"} {
		if strings.Contains(frame.Plain, notWant) {
			t.Fatalf("SDK table render leaked structural fallback %q: %q", notWant, frame.Plain)
		}
	}

	cases := []struct {
		name  string
		props map[string]any
		want  string
	}{
		{name: "unit-value", props: map[string]any{"value": 0.65}, want: "65%"},
		{name: "zero-to-hundred", props: map[string]any{"value": 65}, want: "65%"},
		{name: "explicit-range", props: map[string]any{"value": 5, "min": 0, "max": 10}, want: "50%"},
		{name: "clamped", props: map[string]any{"value": 125, "min": 0, "max": 100}, want: "100%"},
		{name: "bad-range", props: map[string]any{"value": 5, "min": 1, "max": 1}, want: "0%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := component.Tree{SurfaceID: "sdk-progress", Revision: 1, Root: nodeWithProps(t, component.KindProgress, tc.props)}
			progressFrame, err := NewPlainRenderer(testOptions(32, ColorModeMono, false, DefaultTheme())).RenderFrame(context.Background(), tree)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(progressFrame.Plain, tc.want) {
				t.Fatalf("progress render = %q, want %s", progressFrame.Plain, tc.want)
			}
		})
	}
}

func TestSDKShapedRenderFixtureGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "sdk-shaped-table-progress.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tree component.Tree
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	frame, err := NewPlainRenderer(testOptions(96, ColorModeMono, false, DefaultTheme())).RenderFrame(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "sdk-shaped-table-progress.plain.golden")
	if os.Getenv("UPDATE_RENDER_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(frame.Body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	expectedText := strings.ReplaceAll(string(expected), "\r\n", "\n")
	if expectedText != frame.Body+"\n" {
		t.Fatalf("SDK-shaped fixture golden mismatch\n--- expected\n%s\n--- actual\n%s", expectedText, frame.Body)
	}
}

func TestBlackBoxFixtureUsesSDKShapedRenderProps(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "tooling", "testdata", "fixtures", "black-box-certification.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Tree component.Tree `json:"tree"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var sawTable, sawProgress bool
	walkSDKProps(t, fixture.Tree.Root, &sawTable, &sawProgress)
	if !sawTable || !sawProgress {
		t.Fatalf("black-box fixture coverage table=%t progress=%t", sawTable, sawProgress)
	}
	frame, err := NewPlainRenderer(testOptions(96, ColorModeMono, false, DefaultTheme())).RenderFrame(context.Background(), fixture.Tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Event Type", "Severity", "Duration", "ui.observation", "info", "12 ms", "100%"} {
		if !strings.Contains(frame.Plain, want) {
			t.Fatalf("black-box fixture render missing %q: %q", want, frame.Plain)
		}
	}
}

func TestPlainRendererDeterministicAndSanitizesANSI(t *testing.T) {
	tree := component.Tree{SurfaceID: "s", Revision: 1, Root: nodeWithProps(t, "text", map[string]any{"text": "safe \x1b[31mred\x1b[0m text"})}
	r := NewPlainRenderer(testOptions(30, ColorModeMono, true, DefaultTheme()))
	first, err := r.RenderFrame(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.RenderFrame(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	if first.Body != second.Body || first.Plain != second.Plain {
		t.Fatalf("plain renderer is not deterministic: %#v %#v", first, second)
	}
	assertNoANSI(t, first.Body)
	if !strings.Contains(first.Body, "safe red text") {
		t.Fatalf("sanitized text missing content: %q", first.Body)
	}
}

func TestRendererFailureFallback(t *testing.T) {
	fallback := NewPlainRenderer(testOptions(40, ColorModeMono, true, DefaultTheme()))
	renderer := NewFailoverRenderer(failingEngine{}, fallback)
	frame, err := renderer.RenderFrame(context.Background(), component.Tree{SurfaceID: "s", Revision: 1, Root: nodeWithProps(t, "text", map[string]any{"text": "fallback"})})
	if err != nil {
		t.Fatal(err)
	}
	if frame.Renderer != RendererPlain || frame.Body != "fallback" {
		t.Fatalf("fallback frame = %#v", frame)
	}
}

func TestBridgeSessionPatchAndSink(t *testing.T) {
	var frames []Frame
	r := NewPlainRenderer(testOptions(40, ColorModeMono, true, DefaultTheme()))
	r.opts.Sink = func(_ context.Context, frame Frame) error {
		frames = append(frames, frame)
		return nil
	}
	session, err := r.OpenSurface(context.Background(), surface.Descriptor{ID: "surface-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Render(context.Background(), component.Tree{SurfaceID: "surface-1", Revision: 1, Root: nodeWithProps(t, "text", map[string]any{"text": "first"})}); err != nil {
		t.Fatal(err)
	}
	props, _ := json.Marshal(map[string]any{"text": "second"})
	patch := bridgePatch("surface-1", props)
	if err := session.ApplyPatch(context.Background(), patch); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[1].Body != "second" {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestTerminalSurfaceRepaintSource(t *testing.T) {
	mirror := terminal.NewScreenMirror(terminal.Size{Cols: 12, Rows: 2})
	mirror.ConsumeOutput([]byte("hello\x1b[31mred"))
	frame, err := RenderTerminal(context.Background(), NewPlainRenderer(testOptions(12, ColorModeMono, true, DefaultTheme())), "term", 1, mirror)
	if err != nil {
		t.Fatal(err)
	}
	assertNoANSI(t, frame.Body)
	if !strings.Contains(frame.Body, "hellored") {
		t.Fatalf("terminal frame missing repaint content: %q", frame.Body)
	}
}

func TestRendererDoesNotOwnTerminal(t *testing.T) {
	root := "."
	bad := [][]string{{"New", "Program"}, {"With", "Input"}, {"With", "Output"}, {"Enter", "Alt", "Screen"}, {"Make", "Raw"}}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, parts := range bad {
			needle := strings.Join(parts, "")
			if strings.Contains(text, needle) {
				return errors.New(path + " contains terminal ownership API " + needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type failingEngine struct{}

func (failingEngine) RenderFrame(context.Context, component.Tree) (Frame, error) {
	return Frame{}, errors.New("boom")
}

func testOptions(width int, mode ColorMode, unicode bool, theme Theme) Options {
	return Options{Width: width, Height: 200, ColorMode: mode, Unicode: unicode, Theme: theme, Now: func() time.Time { return time.Unix(1, 0).UTC() }, MaxListItems: 20}
}

func sdkContractTree(t *testing.T, kind component.Kind) component.Tree {
	t.Helper()
	node := sdkContractNode(t, kind)
	data, err := json.Marshal(component.Tree{SurfaceID: "sdk-contract", Revision: 1, Root: node})
	if err != nil {
		t.Fatal(err)
	}
	var tree component.Tree
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}

func sdkContractNode(t *testing.T, kind component.Kind) component.Node {
	t.Helper()
	id := "sdk-" + strings.ToLower(strings.ReplaceAll(string(kind), ".", "-"))
	child := nodeWithProps(t, component.KindText, map[string]any{"value": "child content"})
	switch kind {
	case component.KindApplication, component.KindWindow, component.KindSurface, component.KindViewport, component.KindStack, kindColumn, component.KindRow, component.KindPanel, component.KindCard, kindBox, kindSection, kindSplit, component.KindForm, component.KindToolbar, kindActionBar, component.KindCanvas, component.KindExtensionOutlet, kindRoot:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"title": string(kind) + " title"}), Children: []component.Node{child}}
	case component.KindGrid:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"columns": 2}), Children: []component.Node{child, nodeWithProps(t, component.KindText, map[string]any{"value": "second cell"})}}
	case kindStatusGrid:
		return nodeWithProps(t, kind, sdkTableProps(string(kind), "ok", 50))
	case component.KindText:
		return nodeWithProps(t, kind, map[string]any{"value": "SDK text value"})
	case component.KindMarkdown:
		return nodeWithProps(t, kind, map[string]any{"markdown": "**SDK markdown** body"})
	case component.KindCode:
		return nodeWithProps(t, kind, map[string]any{"code": "fmt.Println(\"sdk\")", "language": "go"})
	case component.KindIcon, component.KindBadge, component.KindLink, component.KindKeybindingHint:
		return nodeWithProps(t, kind, map[string]any{"label": string(kind) + " label", "href": "https://example.invalid"})
	case kindKeyValue, kindDetail:
		return nodeWithProps(t, kind, map[string]any{"items": []map[string]any{{"key": "sdk", "value": string(kind)}}})
	case component.KindTable:
		return nodeWithProps(t, kind, sdkTableProps(string(kind), "ok", 65))
	case component.KindList, component.KindTree, kindTimeline, kindLog, kindContextMenu:
		return nodeWithProps(t, kind, map[string]any{"items": []string{"alpha", "beta"}, "selected": "alpha"})
	case component.KindProgress, kindMeter, kindBar:
		return nodeWithProps(t, kind, map[string]any{"value": 65, "min": 0, "max": 100, "label": string(kind)})
	case kindSparkline, component.KindChart:
		return nodeWithProps(t, kind, map[string]any{"value": 0.65, "label": string(kind)})
	case component.KindTextInput, kindPasswordInput, kindSearchInput, kindNumberInput, component.KindTextArea, component.KindSelect, component.KindCheckbox, component.KindRadioGroup, component.KindToggle, component.KindSlider, kindDateInput, kindFileInput:
		return nodeWithProps(t, kind, map[string]any{"label": string(kind) + " control", "value": "sample", "checked": true, "options": []string{"one", "two"}})
	case component.KindButton:
		return nodeWithProps(t, kind, map[string]any{"label": "button control", "actionId": "go"})
	case component.KindTabs:
		return nodeWithProps(t, kind, map[string]any{"items": []string{"Alpha", "Beta"}, "selected": "Alpha"})
	case kindScroll:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"start": 0, "count": 1, "total": 2}), Children: []component.Node{nodeWithProps(t, component.KindText, map[string]any{"value": "scroll line"})}}
	case kindDisclosure:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"title": "Details", "expanded": true}), Children: []component.Node{child}}
	case component.KindSeparator:
		return nodeWithProps(t, kind, map[string]any{"label": "separator"})
	case component.KindSpacer:
		return nodeWithProps(t, kind, map[string]any{"size": 1})
	case kindEmpty:
		return nodeWithProps(t, kind, map[string]any{"message": "empty state"})
	case component.KindCommandPalette:
		return nodeWithProps(t, kind, map[string]any{"query": "deploy", "items": []string{"Deploy"}})
	case component.KindBreadcrumb:
		return nodeWithProps(t, kind, map[string]any{"items": []string{"Home", "Project"}, "selected": "Home"})
	case kindPagination:
		return nodeWithProps(t, kind, map[string]any{"page": 1, "total": 2})
	case kindHelp:
		return nodeWithProps(t, kind, map[string]any{"title": "help title"})
	case kindAlert, component.KindToast:
		return nodeWithProps(t, kind, map[string]any{"message": string(kind) + " message", "severity": "info"})
	case kindLoading, component.KindSpinner:
		return nodeWithProps(t, kind, map[string]any{"message": "loading"})
	case kindErrorBoundary:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"title": "error boundary"}), Children: []component.Node{child}}
	case kindConfirmation, kindPrompt, component.KindDialog:
		return component.Node{ID: id, Kind: kind, Props: mustJSON(t, map[string]any{"title": string(kind), "modal": false}), Children: []component.Node{child}}
	case component.KindTerminal:
		return nodeWithProps(t, kind, map[string]any{"title": "terminal title", "alt": "terminal output"})
	case component.KindImage, component.KindVideo:
		return nodeWithProps(t, kind, map[string]any{"title": string(kind), "alt": string(kind) + " alternative text"})
	default:
		return nodeWithProps(t, kind, map[string]any{"title": string(kind) + " title", "value": string(kind) + " value"})
	}
}

func catalogTree(t *testing.T) component.Tree {
	t.Helper()
	kinds := SupportedComponentKinds()
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	children := make([]component.Node, 0, len(kinds))
	for _, kind := range kinds {
		if kind == component.KindApplication || kind == component.KindWindow || kind == component.KindSurface || kind == kindRoot {
			continue
		}
		children = append(children, sampleNode(t, kind))
	}
	return component.Tree{SurfaceID: "catalog", Revision: 7, Root: component.Node{ID: "root", Kind: kindRoot, Children: children}}
}

func sampleNode(t *testing.T, kind component.Kind) component.Node {
	t.Helper()
	base := map[string]any{"title": string(kind), "label": string(kind), "text": string(kind) + " text with unicode Ω and ansi \x1b[31mred\x1b[0m", "value": string(kind) + " value"}
	switch kind {
	case component.KindGrid, kindStatusGrid:
		return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, map[string]any{"columns": 2}), Children: []component.Node{nodeWithProps(t, "text", map[string]any{"text": "cell 1"}), nodeWithProps(t, "text", map[string]any{"text": "cell 2"})}}
	case component.KindRow, component.KindToolbar, kindActionBar, kindSplit, component.KindForm:
		return component.Node{ID: string(kind), Kind: kind, Children: []component.Node{nodeWithProps(t, "button", map[string]any{"label": "Run"}), nodeWithProps(t, "badge", map[string]any{"label": "OK"})}}
	case component.KindStack, kindColumn, component.KindApplication, component.KindWindow, component.KindSurface, component.KindViewport, kindRoot:
		return component.Node{ID: string(kind), Kind: kind, Children: []component.Node{nodeWithProps(t, "text", base)}}
	case kindBox, component.KindPanel, component.KindCard, kindSection, component.KindDialog, kindConfirmation, kindPrompt:
		return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, base), Children: []component.Node{nodeWithProps(t, "text", base)}}
	case component.KindTabs:
		return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, map[string]any{"tabs": []string{"One", "Two"}, "selected": 1}), Children: []component.Node{nodeWithProps(t, "text", map[string]any{"text": "one"}), nodeWithProps(t, "text", map[string]any{"text": "two"})}}
	case kindScroll:
		return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, map[string]any{"start": 1, "count": 2, "total": 5, "virtualized": true}), Children: []component.Node{nodeWithProps(t, "text", map[string]any{"text": "a\nb\nc\nd"})}}
	case kindDisclosure:
		return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, map[string]any{"title": "Details", "expanded": true}), Children: []component.Node{nodeWithProps(t, "text", base)}}
	case component.KindTable:
		return nodeWithProps(t, kind, map[string]any{"columns": []string{"Name", "State"}, "rows": []map[string]any{{"Name": "Build", "State": "green"}, {"Name": "Test", "State": "blue"}}})
	case component.KindList, component.KindTree, kindTimeline, kindLog, kindContextMenu:
		return nodeWithProps(t, kind, map[string]any{"items": []any{"alpha", map[string]any{"label": "beta"}}, "selected": 1, "virtualized": true, "total": 4})
	case component.KindProgress, kindMeter, kindBar:
		return nodeWithProps(t, kind, map[string]any{"percent": 0.42})
	case kindSparkline, component.KindChart:
		return nodeWithProps(t, kind, map[string]any{"values": []float64{1, 4, 2, 8, 3}})
	case kindKeyValue, kindDetail, kindHelp, component.KindKeybindingHint:
		return nodeWithProps(t, kind, map[string]any{"items": []map[string]any{{"key": "Esc", "value": "Close"}, {"key": "Enter", "value": "Accept"}}})
	case component.KindCheckbox:
		return nodeWithProps(t, kind, map[string]any{"label": "Agree", "checked": true})
	case component.KindRadioGroup, component.KindSelect:
		return nodeWithProps(t, kind, map[string]any{"label": string(kind), "options": []string{"a", "b"}, "selected": "a"})
	case component.KindSlider:
		return nodeWithProps(t, kind, map[string]any{"label": "Volume", "percent": 0.7})
	case kindAlert, component.KindToast:
		return nodeWithProps(t, kind, map[string]any{"severity": "warning", "message": "Heads up"})
	case kindLoading, component.KindSpinner:
		return nodeWithProps(t, kind, map[string]any{"message": "Loading"})
	case kindErrorBoundary:
		return nodeWithProps(t, kind, map[string]any{"error": "Failure"})
	case component.KindCommandPalette:
		return nodeWithProps(t, kind, map[string]any{"query": "deploy", "items": []string{"Deploy app", "Rollback"}})
	case component.KindBreadcrumb:
		return nodeWithProps(t, kind, map[string]any{"items": []string{"Home", "Project", "Run"}})
	case kindPagination:
		return nodeWithProps(t, kind, map[string]any{"page": 2, "total": 5})
	case component.KindTerminal:
		return nodeWithProps(t, kind, map[string]any{"repaint": "terminal\x1b[31m red"})
	case component.KindImage, component.KindVideo:
		return nodeWithProps(t, kind, map[string]any{"alt": string(kind) + " alt"})
	default:
		return nodeWithProps(t, kind, base)
	}
}

func sdkTableProps(eventType, severity string, duration int) map[string]any {
	return map[string]any{
		"columns": []map[string]any{
			{"id": "eventType", "title": "Event Type"},
			{"id": "severity", "title": "Severity"},
			{"id": "durationMs", "title": "Duration"},
		},
		"rows": []map[string]any{
			{
				"id": "row-1",
				"cells": map[string]any{
					"eventType":  map[string]any{"display": map[string]any{"text": eventType}, "plain": eventType},
					"severity":   map[string]any{"display": map[string]any{"kind": "badge", "props": map[string]any{"label": severity}}, "plain": strings.ToLower(severity)},
					"durationMs": map[string]any{"value": duration, "plain": fmt.Sprintf("%d ms", duration)},
				},
			},
		},
	}
}

func walkSDKProps(t *testing.T, node component.Node, sawTable, sawProgress *bool) {
	t.Helper()
	props := parseProps(node.Props)
	switch node.Kind {
	case component.KindTable, kindStatusGrid:
		columns, rows := props.Array("columns"), props.Array("rows")
		if len(columns) == 0 || len(rows) == 0 {
			t.Fatalf("%s fixture table missing columns/rows", node.ID)
		}
		for _, rawColumn := range columns {
			column, ok := rawColumn.(map[string]any)
			if !ok || column["id"] == nil || column["title"] == nil {
				t.Fatalf("%s fixture column is not SDK-shaped: %#v", node.ID, rawColumn)
			}
		}
		for _, rawRow := range rows {
			row, ok := rawRow.(map[string]any)
			if !ok || row["id"] == nil {
				t.Fatalf("%s fixture row missing id: %#v", node.ID, rawRow)
			}
			cells, ok := row["cells"].(map[string]any)
			if !ok || len(cells) == 0 {
				t.Fatalf("%s fixture row missing SDK cells: %#v", node.ID, rawRow)
			}
		}
		*sawTable = true
	case component.KindProgress, kindMeter:
		if !props.Has("value") || !props.Has("min") || !props.Has("max") {
			t.Fatalf("%s fixture progress missing value/min/max", node.ID)
		}
		*sawProgress = true
	}
	for _, child := range node.Children {
		walkSDKProps(t, child, sawTable, sawProgress)
	}
}

func nodeWithProps(t *testing.T, kind component.Kind, props map[string]any) component.Node {
	t.Helper()
	return component.Node{ID: string(kind), Kind: kind, Props: mustJSON(t, props)}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoANSI(t *testing.T, text string) {
	t.Helper()
	if text != stripANSI(text) {
		t.Fatalf("ANSI present in %q", text)
	}
}

func bridgePatch(surfaceID string, props json.RawMessage) bridge.Patch {
	return bridge.Patch{SurfaceID: surfaceID, NextRevision: 2, Operations: []bridge.PatchOp{{Op: bridge.PatchSetProps, Path: "/root", Value: props}}}
}

func strconvItoa(v int) string {
	return fmt.Sprintf("%d", v)
}

func strconvBool(v bool) string {
	if v {
		return "unicode"
	}
	return "ascii"
}
