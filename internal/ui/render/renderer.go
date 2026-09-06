package render

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/style"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type BubbleRenderer struct {
	opts Options
}

type PlainRenderer struct {
	opts Options
}

type FailoverRenderer struct {
	primary  Engine
	fallback Engine
}

type TCellRenderer struct {
	Reason string
}

type session struct {
	mu         sync.Mutex
	surfaceID  string
	descriptor surface.Descriptor
	engine     Engine
	sink       Sink
	last       component.Tree
	closed     bool
}

type renderContext struct {
	opts     Options
	plain    bool
	failures []error
}

type bubbleModel struct {
	frame Frame
}

func (m bubbleModel) Init() tea.Cmd                       { return nil }
func (m bubbleModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m bubbleModel) View() tea.View                      { return tea.NewView(m.frame.Body) }

func (r *BubbleRenderer) RenderFrame(ctx context.Context, tree component.Tree) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	opts := normalizeOptions(r.opts)
	rc := renderContext{opts: opts}
	body, err := rc.renderNode(tree.Root, opts.Width)
	if err != nil {
		if opts.FailureMode == FailurePlain {
			return NewPlainRenderer(opts).RenderFrame(ctx, tree)
		}
		return Frame{}, fmt.Errorf("%w: %v", ErrRenderFailed, err)
	}
	body = fitBlock(body, opts.Width, opts.Height, opts.Unicode)
	plain := stripANSI(body)
	frame := Frame{
		SurfaceID: tree.SurfaceID,
		Revision:  tree.Revision,
		Renderer:  RendererBubble,
		Width:     opts.Width,
		Height:    opts.Height,
		ThemeID:   opts.Theme.ID,
		ColorMode: opts.ColorMode,
		Body:      body,
		Plain:     plain,
		Rendered:  opts.Now(),
	}
	model := bubbleModel{frame: frame}
	view := model.View()
	frame.Body = view.Content
	frame.Plain = stripANSI(view.Content)
	return frame, nil
}

func (r *PlainRenderer) RenderFrame(ctx context.Context, tree component.Tree) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	opts := normalizeOptions(r.opts)
	opts.ColorMode = ColorModeMono
	rc := renderContext{opts: opts, plain: true}
	body, err := rc.renderNode(tree.Root, opts.Width)
	if err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrRenderFailed, err)
	}
	body = fitBlock(stripANSI(body), opts.Width, opts.Height, opts.Unicode)
	return Frame{SurfaceID: tree.SurfaceID, Revision: tree.Revision, Renderer: RendererPlain, Width: opts.Width, Height: opts.Height, ThemeID: opts.Theme.ID, ColorMode: ColorModeMono, Body: body, Plain: body, Rendered: opts.Now()}, nil
}

func (r *FailoverRenderer) RenderFrame(ctx context.Context, tree component.Tree) (Frame, error) {
	if r == nil || r.primary == nil {
		if r != nil && r.fallback != nil {
			return r.fallback.RenderFrame(ctx, tree)
		}
		return Frame{}, ErrRenderFailed
	}
	frame, err := r.primary.RenderFrame(ctx, tree)
	if err == nil {
		return frame, nil
	}
	if r.fallback == nil {
		return Frame{}, err
	}
	fallback, fallbackErr := r.fallback.RenderFrame(ctx, tree)
	if fallbackErr != nil {
		return Frame{}, fmt.Errorf("%w; fallback: %v", err, fallbackErr)
	}
	fallback.Renderer = RendererPlain
	return fallback, nil
}

func (r *BubbleRenderer) OpenSurface(_ context.Context, descriptor surface.Descriptor) (bridge.RendererSession, error) {
	return &session{surfaceID: descriptorSurfaceID(descriptor), descriptor: descriptor, engine: r, sink: r.opts.Sink}, nil
}

func (r *PlainRenderer) NegotiateUI(_ context.Context, requested protocol.Revision, capabilities []capability.Descriptor) (protocol.Revision, []capability.Descriptor, error) {
	return requested, capabilities, nil
}

func (r *PlainRenderer) OpenSurface(_ context.Context, descriptor surface.Descriptor) (bridge.RendererSession, error) {
	return &session{surfaceID: descriptorSurfaceID(descriptor), descriptor: descriptor, engine: r, sink: r.opts.Sink}, nil
}

func (r *TCellRenderer) NegotiateUI(_ context.Context, requested protocol.Revision, capabilities []capability.Descriptor) (protocol.Revision, []capability.Descriptor, error) {
	return requested, capabilities, nil
}

func (r *TCellRenderer) OpenSurface(context.Context, surface.Descriptor) (bridge.RendererSession, error) {
	return nil, fmt.Errorf("%w: %s", ErrTCellUnavailable, r.Reason)
}

func (s *session) SurfaceID() string { return s.surfaceID }

func (s *session) Render(ctx context.Context, tree component.Tree) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if tree.SurfaceID == "" {
		tree.SurfaceID = s.surfaceID
	}
	frame, err := s.engine.RenderFrame(ctx, tree)
	if err != nil {
		return err
	}
	s.last = tree
	if s.sink != nil {
		return s.sink(ctx, frame)
	}
	return nil
}

func (s *session) ApplyPatch(ctx context.Context, patch bridge.Patch) error {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last.Root.ID == "" {
		return fmt.Errorf("%w: no base tree", ErrRenderFailed)
	}
	if patch.SurfaceID != "" && patch.SurfaceID != s.surfaceID {
		return ErrUnsupportedSurface
	}
	for _, op := range patch.Operations {
		switch op.Op {
		case bridge.PatchReplace:
			if op.Path == "/root" || op.Path == "root" {
				if err := json.Unmarshal(op.Value, &last.Root); err != nil {
					return err
				}
			} else if strings.HasSuffix(op.Path, "/props") {
				if err := setPropsByPath(&last.Root, strings.TrimSuffix(op.Path, "/props"), op.Value); err != nil {
					return err
				}
			}
		case bridge.PatchSetProps:
			if err := setPropsByPath(&last.Root, op.Path, op.Value); err != nil {
				return err
			}
		case bridge.PatchAdd, bridge.PatchRemove, bridge.PatchMove, bridge.PatchCopy, bridge.PatchTest, bridge.PatchBindData, bridge.PatchBindAction:
			return fmt.Errorf("%w: patch operation %s requires reconciler", ErrRenderFailed, op.Op)
		}
	}
	if patch.NextRevision > 0 {
		last.Revision = patch.NextRevision
	}
	return s.Render(ctx, last)
}

func (s *session) HandleEvent(context.Context, bridge.Event) error { return nil }

func (s *session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func setPropsByPath(root *component.Node, path string, value json.RawMessage) error {
	if path == "" || path == "/" || path == "/root" || path == "root" {
		root.Props = value
		return nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 && parts[0] == "root" {
		parts = parts[1:]
	}
	node := root
	for len(parts) > 0 {
		if parts[0] != "children" || len(parts) < 2 {
			return fmt.Errorf("unsupported patch path %q", path)
		}
		idx, err := strconv.Atoi(parts[1])
		if err != nil || idx < 0 || idx >= len(node.Children) {
			return fmt.Errorf("invalid patch path %q", path)
		}
		node = &node.Children[idx]
		parts = parts[2:]
	}
	node.Props = value
	return nil
}

func (rc *renderContext) renderNode(node component.Node, width int) (string, error) {
	if width <= 0 {
		width = rc.opts.Width
	}
	props := parseProps(node.Props)
	if props.Bool("fail") || node.Metadata != nil && len(node.Metadata["render.fail"]) > 0 {
		return "", fmt.Errorf("component %s requested failure", node.ID)
	}
	kind := node.Kind
	if kind == "" {
		kind = component.KindText
	}
	statePrefix := rc.statePrefix(props)
	available := maxInt(1, width-lipgloss.Width(statePrefix))
	var out string
	var err error
	switch kind {
	case component.KindApplication, component.KindWindow, component.KindSurface, component.KindViewport, kindRoot, component.KindCanvas, component.KindExtensionOutlet:
		out, err = rc.renderContainer(node, available)
	case component.KindStack, kindColumn, component.KindForm:
		out, err = rc.renderStack(node, available)
	case component.KindRow, component.KindToolbar, component.KindActionBar:
		out, err = rc.renderRow(node, available)
	case component.KindGrid, component.KindStatusGrid:
		out, err = rc.renderGrid(node, available)
	case kindBox, component.KindPanel, component.KindCard, kindSection, component.KindDialog:
		out, err = rc.renderBox(node, available)
	case kindSplit:
		out, err = rc.renderSplit(node, available)
	case component.KindTabs:
		out, err = rc.renderTabs(node, available)
	case kindScroll:
		out, err = rc.renderScroll(node, available)
	case kindDisclosure:
		out, err = rc.renderDisclosure(node, available)
	case component.KindSeparator:
		out = rc.rule(props.StringDefault("label", props.String("text")), available)
	case component.KindSpacer:
		out = ""
	case component.KindText, component.KindMarkdown, component.KindCode, component.KindIcon:
		out = rc.renderTextLike(kind, props, available)
	case component.KindKeyValue, component.KindDetail:
		out = rc.renderKeyValue(props, available)
	case component.KindBadge:
		out = rc.styled("badge", " "+sanitize(props.First("label", "text", "status", "value"))+" ")
	case component.KindLink:
		out = rc.renderLink(props, available)
	case component.KindEmpty:
		out = rc.styled("empty", sanitize(props.StringDefault("message", "No content")))
	case component.KindTable:
		out = rc.renderTable(props, available)
	case component.KindList:
		out = rc.renderList("list", props, available)
	case component.KindTree:
		out = rc.renderList("tree", props, available)
	case kindTimeline:
		out = rc.renderList("timeline", props, available)
	case kindLog:
		out = rc.renderList("log", props, available)
	case component.KindProgress, kindMeter, kindBar:
		out = rc.renderProgress(props, available)
	case component.KindSparkline, component.KindChart:
		out = rc.renderSparkline(props, available)
	case component.KindTextInput, kindPasswordInput, kindSearchInput, kindNumberInput, component.KindTextArea, component.KindSelect, component.KindCheckbox, component.KindRadioGroup, component.KindToggle, component.KindSlider, kindDateInput, kindFileInput:
		out = rc.renderInput(kind, props, available)
	case component.KindButton:
		out = rc.renderButton(props, available)
	case component.KindCommandPalette:
		out, err = rc.renderCommandPalette(node, available)
	case kindContextMenu:
		out = rc.renderList("menu", props, available)
	case component.KindBreadcrumb:
		out = rc.renderBreadcrumb(props, available)
	case kindPagination:
		out = rc.renderPagination(props, available)
	case kindHelp, component.KindKeybindingHint:
		out = rc.renderHelp(props, available)
	case component.KindAlert, component.KindToast:
		out = rc.renderAlert(kind, props, available)
	case kindLoading, component.KindSpinner:
		out = rc.renderLoading(props, available)
	case kindErrorBoundary:
		out, err = rc.renderErrorBoundary(node, available)
	case kindConfirmation, kindPrompt:
		out, err = rc.renderPrompt(kind, node, available)
	case component.KindTerminal:
		out = rc.renderTerminal(props, available)
	case component.KindImage, component.KindVideo:
		out = rc.styled("muted", "["+string(kind)+": "+sanitize(props.First("alt", "label", "src"))+"]")
	default:
		out, err = rc.renderUnknown(node, available)
	}
	if err != nil {
		return "", err
	}
	if statePrefix != "" {
		out = prefixFirstLine(statePrefix, out)
	}
	return fitBlock(out, width, rc.opts.Height, rc.opts.Unicode), nil
}

func (rc *renderContext) renderContainer(node component.Node, width int) (string, error) {
	parts := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		part, err := rc.renderNode(child, width)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(stripANSI(part)) != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		props := parseProps(node.Props)
		return sanitize(props.First("text", "value", "label", "title", "message", "body", "content", "markdown", "code", "alt")), nil
	}
	return strings.Join(parts, "\n"), nil
}

func (rc *renderContext) renderStack(node component.Node, width int) (string, error) {
	return rc.renderContainer(node, width)
}

func (rc *renderContext) renderRow(node component.Node, width int) (string, error) {
	parts := make([]string, 0, len(node.Children))
	childWidth := maxInt(1, (width-maxInt(0, len(node.Children)-1)*3)/maxInt(1, len(node.Children)))
	for _, child := range node.Children {
		part, err := rc.renderNode(child, childWidth)
		if err != nil {
			return "", err
		}
		parts = append(parts, oneLine(part, childWidth))
	}
	if len(parts) == 0 {
		return "", nil
	}
	sep := " │ "
	if !rc.opts.Unicode {
		sep = " | "
	}
	return fitLine(strings.Join(parts, sep), width, rc.opts.Unicode), nil
}

func (rc *renderContext) renderGrid(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	cols := props.IntDefault("columns", 2)
	if cols < 1 {
		cols = 1
	}
	cellWidth := maxInt(1, (width-(cols-1)*3)/cols)
	var lines []string
	for i := 0; i < len(node.Children); i += cols {
		var row []string
		for j := 0; j < cols && i+j < len(node.Children); j++ {
			part, err := rc.renderNode(node.Children[i+j], cellWidth)
			if err != nil {
				return "", err
			}
			row = append(row, padRight(oneLine(part, cellWidth), cellWidth))
		}
		sep := " │ "
		if !rc.opts.Unicode {
			sep = " | "
		}
		lines = append(lines, fitLine(strings.Join(row, sep), width, rc.opts.Unicode))
	}
	if len(lines) == 0 {
		if len(props.Array("rows")) > 0 {
			return rc.renderTable(props, width), nil
		}
		if len(props.Array("items")) > 0 {
			return rc.renderList("grid", props, width), nil
		}
		return fitLine(sanitize(props.First("title", "label", "text", "value", "message")), width, rc.opts.Unicode), nil
	}
	return strings.Join(lines, "\n"), nil
}

func (rc *renderContext) renderBox(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	body, err := rc.renderContainer(node, maxInt(1, width-4))
	if err != nil {
		return "", err
	}
	if body == "" {
		body = sanitize(props.First("body", "content", "text", "value", "message", "description", "markdown", "code", "alt"))
	}
	title := sanitize(props.First("title", "label"))
	return rc.box(title, body, width), nil
}

func (rc *renderContext) renderSplit(node component.Node, width int) (string, error) {
	if len(node.Children) == 0 {
		return "", nil
	}
	leftWidth := maxInt(1, width/2-1)
	rightWidth := maxInt(1, width-leftWidth-3)
	left, err := rc.renderNode(node.Children[0], leftWidth)
	if err != nil {
		return "", err
	}
	right := ""
	if len(node.Children) > 1 {
		right, err = rc.renderNode(node.Children[1], rightWidth)
		if err != nil {
			return "", err
		}
	}
	sep := " │ "
	if !rc.opts.Unicode {
		sep = " | "
	}
	return joinColumns(left, right, leftWidth, rightWidth, sep), nil
}

func (rc *renderContext) renderTabs(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	labels := props.Strings("tabs")
	if len(labels) == 0 {
		labels = props.Strings("items")
	}
	if len(labels) == 0 {
		labels = props.Strings("options")
	}
	if len(labels) == 0 {
		for _, child := range node.Children {
			labels = append(labels, parseProps(child.Props).First("title", "label", "text", "value"))
		}
	}
	selected := clamp(props.IntDefault("selected", props.IntDefault("selectedIndex", 0)), 0, maxInt(0, len(labels)-1))
	if selectedValue := props.String("selected"); selectedValue != "" {
		for i, label := range labels {
			if label == selectedValue {
				selected = i
				break
			}
		}
	}
	for i, label := range labels {
		label = sanitize(label)
		if i == selected {
			labels[i] = rc.styled("selected", "["+label+"]")
		} else {
			labels[i] = label
		}
	}
	header := fitLine(strings.Join(labels, " "), width, rc.opts.Unicode)
	if selected < len(node.Children) {
		body, err := rc.renderNode(node.Children[selected], width)
		if err != nil {
			return "", err
		}
		return header + "\n" + body, nil
	}
	return header, nil
}

func (rc *renderContext) renderScroll(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	body, err := rc.renderContainer(node, width)
	if err != nil {
		return "", err
	}
	lines := strings.Split(body, "\n")
	start := clamp(props.IntDefault("offset", props.IntDefault("start", 0)), 0, len(lines))
	count := props.IntDefault("count", rc.opts.Height)
	if count <= 0 || start+count > len(lines) {
		count = len(lines) - start
	}
	window := lines[start : start+count]
	if props.Bool("virtualized") || props.Int("total") > len(lines) {
		window = append(window, rc.styled("muted", fmt.Sprintf("%s %d/%d", ellipsis(rc.opts.Unicode), start+len(window), maxInt(props.Int("total"), len(lines)))))
	}
	return strings.Join(window, "\n"), nil
}

func (rc *renderContext) renderDisclosure(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	marker := "▸"
	if props.BoolDefault("expanded", true) {
		marker = "▾"
	}
	if !rc.opts.Unicode {
		if marker == "▾" {
			marker = "v"
		} else {
			marker = ">"
		}
	}
	header := fitLine(marker+" "+sanitize(props.First("title", "label", "text")), width, rc.opts.Unicode)
	if !props.BoolDefault("expanded", true) {
		return header, nil
	}
	body, err := rc.renderContainer(node, maxInt(1, width-2))
	if err != nil {
		return "", err
	}
	return header + indent(body, "  "), nil
}

func (rc *renderContext) renderTextLike(kind component.Kind, props propMap, width int) string {
	keys := []string{"text", "value", "body", "label", "content"}
	switch kind {
	case component.KindMarkdown:
		keys = []string{"markdown", "text", "value", "body", "content", "label"}
	case component.KindCode:
		keys = []string{"code", "text", "value", "body", "content", "label"}
	}
	text := sanitize(props.First(keys...))
	if text == "" {
		text = sanitize(props.String("children"))
	}
	switch kind {
	case component.KindMarkdown:
		text = renderMarkdownText(text)
	case component.KindCode:
		lang := sanitize(props.String("language"))
		title := "code"
		if lang != "" {
			title += " (" + lang + ")"
		}
		return rc.box(title, text, width)
	}
	return wrapBlock(rc.styled(roleFromProps(props), text), width, rc.opts.Unicode)
}

func (rc *renderContext) renderKeyValue(props propMap, width int) string {
	pairs := props.Pairs()
	if len(pairs) == 0 {
		key := sanitize(props.First("key", "label", "name"))
		value := sanitize(props.First("value", "text", "description", "title", "label"))
		if key != "" || value != "" {
			pairs = append(pairs, pair{key, value})
		}
	}
	var lines []string
	for _, p := range pairs {
		left := rc.styled("muted", sanitize(p.Key)+":")
		lines = append(lines, fitLine(left+" "+sanitize(p.Value), width, rc.opts.Unicode))
	}
	return strings.Join(lines, "\n")
}

func (rc *renderContext) renderLink(props propMap, width int) string {
	label := sanitize(props.First("label", "text", "href", "url"))
	href := sanitize(props.First("href", "url"))
	if href != "" && href != label {
		label += " <" + href + ">"
	}
	return fitLine(rc.styled("link", label), width, rc.opts.Unicode)
}

func (rc *renderContext) renderTable(props propMap, width int) string {
	columns := props.TableColumns()
	rows := props.TableRows(columns)
	if len(columns) == 0 && len(rows) > 0 {
		for key := range rows[0] {
			columns = append(columns, tableColumn{ID: key, Title: key})
		}
		sort.Slice(columns, func(i, j int) bool { return columns[i].ID < columns[j].ID })
	}
	if len(columns) == 0 {
		return rc.renderList("table", props, width)
	}
	colWidth := maxInt(3, (width-(len(columns)-1)*3)/maxInt(1, len(columns)))
	var lines []string
	header := make([]string, len(columns))
	for i, col := range columns {
		title := firstNonEmpty(col.Title, col.ID)
		header[i] = padRight(fitLine(sanitize(title), colWidth, rc.opts.Unicode), colWidth)
	}
	sep := " │ "
	if !rc.opts.Unicode {
		sep = " | "
	}
	lines = append(lines, rc.styled("accent", fitLine(strings.Join(header, sep), width, rc.opts.Unicode)))
	lines = append(lines, rc.rule("", width))
	for _, row := range rows[:minInt(len(rows), rc.opts.MaxListItems)] {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cells[i] = padRight(fitLine(sanitize(row[col.ID]), colWidth, rc.opts.Unicode), colWidth)
		}
		lines = append(lines, fitLine(strings.Join(cells, sep), width, rc.opts.Unicode))
	}
	if len(rows) > rc.opts.MaxListItems {
		lines = append(lines, rc.styled("muted", fmt.Sprintf("… %d more", len(rows)-rc.opts.MaxListItems)))
	}
	return strings.Join(lines, "\n")
}

func (rc *renderContext) renderList(kind string, props propMap, width int) string {
	items := props.Array("items")
	if len(items) == 0 {
		items = props.Array("rows")
	}
	if len(items) == 0 {
		return rc.styled("empty", "No items")
	}
	start := clamp(props.IntDefault("start", props.IntDefault("offset", 0)), 0, len(items))
	count := props.IntDefault("count", rc.opts.MaxListItems)
	if count <= 0 || start+count > len(items) {
		count = len(items) - start
	}
	items = items[start : start+count]
	var lines []string
	selectedIndex := props.IntDefault("selected", props.IntDefault("selectedIndex", -1))
	selectedValue := sanitize(props.String("selected"))
	for i, item := range items {
		label := sanitize(valueString(item))
		prefix := "•"
		switch kind {
		case "tree":
			prefix = "├─"
		case "timeline":
			prefix = "◷"
		case "log":
			prefix = "│"
		case "menu":
			prefix = "›"
		case "grid":
			prefix = "□"
		}
		if !rc.opts.Unicode {
			prefix = asciiSymbol(prefix)
		}
		role := ""
		if i == selectedIndex-start || selectedValue != "" && label == selectedValue {
			role = "selected"
		}
		lines = append(lines, fitLine(rc.styled(role, prefix+" "+label), width, rc.opts.Unicode))
	}
	if props.Bool("virtualized") || props.Int("total") > start+len(items) {
		lines = append(lines, rc.styled("muted", fmt.Sprintf("%s %d/%d", ellipsis(rc.opts.Unicode), start+len(items), maxInt(props.Int("total"), start+len(items)))))
	}
	return strings.Join(lines, "\n")
}

func (rc *renderContext) renderProgress(props propMap, width int) string {
	pct := normalizedProgress(props)
	barWidth := maxInt(1, minInt(maxInt(1, width-7), props.IntDefault("width", maxInt(1, width-7))))
	if !rc.plain && rc.opts.ColorMode != ColorModeMono && rc.opts.Unicode {
		model := progress.New(progress.WithWidth(barWidth), progress.WithoutPercentage(), progress.WithColors(colorForMode(rc.opts.Theme.token(style.ColorAccent), rc.opts.ColorMode)))
		return fitLine(model.ViewAs(pct)+fmt.Sprintf(" %3.0f%%", pct*100), width, rc.opts.Unicode)
	}
	fullChar, emptyChar := "█", "░"
	if !rc.opts.Unicode {
		fullChar, emptyChar = "#", "-"
	}
	filled := int(math.Round(float64(barWidth) * pct))
	return fitLine("["+strings.Repeat(fullChar, filled)+strings.Repeat(emptyChar, maxInt(0, barWidth-filled))+"] "+fmt.Sprintf("%3.0f%%", pct*100), width, rc.opts.Unicode)
}

func normalizedProgress(props propMap) float64 {
	if props.Has("percent") {
		return normalizeUnitValue(props.FloatDefault("percent", 0))
	}
	value := props.FloatDefault("value", 0)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if props.Has("min") || props.Has("max") {
		minValue := props.FloatDefault("min", 0)
		maxValue := props.FloatDefault("max", 1)
		if !props.Has("max") && value > 1 {
			maxValue = 100
		}
		if math.IsNaN(minValue) || math.IsInf(minValue, 0) || math.IsNaN(maxValue) || math.IsInf(maxValue, 0) || maxValue == minValue {
			return 0
		}
		if maxValue < minValue {
			minValue, maxValue = maxValue, minValue
		}
		return clampFloat((value-minValue)/(maxValue-minValue), 0, 1)
	}
	return normalizeUnitValue(value)
}

func normalizeUnitValue(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value > 1 && value <= 100 {
		value /= 100
	}
	return clampFloat(value, 0, 1)
}

func (rc *renderContext) renderSparkline(props propMap, width int) string {
	values := props.Floats("values")
	if len(values) == 0 {
		values = props.Floats("data")
	}
	if len(values) == 0 {
		values = props.Floats("points")
	}
	if len(values) == 0 && props.Has("value") {
		values = []float64{props.FloatDefault("value", 0)}
	}
	if len(values) == 0 {
		values = []float64{0, .25, .5, .75, 1}
	}
	bars := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if !rc.opts.Unicode {
		bars = []string{"_", ".", ":", "-", "=", "+", "*", "#"}
	}
	minV, maxV := values[0], values[0]
	for _, v := range values {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	var b strings.Builder
	for _, v := range values[:minInt(len(values), width)] {
		idx := 0
		if maxV > minV {
			idx = int(math.Round((v - minV) / (maxV - minV) * float64(len(bars)-1)))
		}
		b.WriteString(bars[clamp(idx, 0, len(bars)-1)])
	}
	out := b.String()
	if label := sanitize(props.First("label", "title")); label != "" {
		out = label + " " + out
	}
	return rc.styled("accent", fitLine(out, width, rc.opts.Unicode))
}

func (rc *renderContext) renderInput(kind component.Kind, props propMap, width int) string {
	label := sanitize(props.First("label", "name", "title"))
	value := sanitize(props.First("value", "text", "selected"))
	if kind == kindPasswordInput && value != "" {
		value = strings.Repeat("•", utf8.RuneCountInString(value))
		if !rc.opts.Unicode {
			value = strings.Repeat("*", len(value))
		}
	}
	if !rc.plain && (kind == component.KindTextInput || kind == kindSearchInput || kind == kindPasswordInput || kind == kindNumberInput) {
		model := textinput.New()
		model.Placeholder = sanitize(props.String("placeholder"))
		model.SetValue(value)
		model.SetWidth(maxInt(1, width-lipgloss.Width(label)-3))
		value = model.View()
	}
	switch kind {
	case component.KindCheckbox:
		mark := "☐"
		if props.Bool("checked") {
			mark = "☑"
		}
		if !rc.opts.Unicode {
			if props.Bool("checked") {
				mark = "[x]"
			} else {
				mark = "[ ]"
			}
		}
		return fitLine(mark+" "+firstNonEmpty(label, value), width, rc.opts.Unicode)
	case component.KindToggle:
		state := "off"
		if props.Bool("checked") || props.Bool("on") {
			state = "on"
		}
		return fitLine(firstNonEmpty(label, "toggle")+": "+state, width, rc.opts.Unicode)
	case component.KindSlider:
		return fitLine(firstNonEmpty(label, "slider")+" "+rc.renderProgress(props, maxInt(1, width-lipgloss.Width(label)-1)), width, rc.opts.Unicode)
	case component.KindRadioGroup, component.KindSelect:
		opts := props.Strings("options")
		if len(opts) == 0 {
			opts = []string{value}
		}
		return fitLine(firstNonEmpty(label, string(kind))+": "+strings.Join(sanitizeStrings(opts), ", "), width, rc.opts.Unicode)
	case component.KindTextArea:
		return fitLine(firstNonEmpty(label, "textarea")+":", width, rc.opts.Unicode) + "\n" + wrapBlock(value, width, rc.opts.Unicode)
	case kindFileInput, kindDateInput:
		return fitLine(firstNonEmpty(label, string(kind))+": "+value, width, rc.opts.Unicode)
	default:
		return fitLine(firstNonEmpty(label, string(kind))+": "+value, width, rc.opts.Unicode)
	}
}

func (rc *renderContext) renderButton(props propMap, width int) string {
	label := sanitize(props.First("label", "text", "title", "action"))
	if label == "" {
		label = "Button"
	}
	if props.Bool("disabled") {
		return rc.styled("disabled", fitLine("[ "+label+" ]", width, rc.opts.Unicode))
	}
	return rc.styled("button", fitLine("[ "+label+" ]", width, rc.opts.Unicode))
}

func (rc *renderContext) renderCommandPalette(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	query := sanitize(props.First("query", "value", "placeholder"))
	body := "> " + query
	list := rc.renderList("menu", props, width)
	if strings.TrimSpace(list) != "" {
		body += "\n" + list
	}
	return rc.box(sanitize(props.StringDefault("title", "Command Palette")), body, width), nil
}

func (rc *renderContext) renderBreadcrumb(props propMap, width int) string {
	parts := sanitizeStrings(props.Strings("items"))
	if len(parts) == 0 {
		parts = sanitizeStrings(strings.Split(props.First("path", "text"), "/"))
	}
	sep := " › "
	if !rc.opts.Unicode {
		sep = " > "
	}
	return fitLine(strings.Join(nonEmpty(parts), sep), width, rc.opts.Unicode)
}

func (rc *renderContext) renderPagination(props propMap, width int) string {
	page := props.IntDefault("page", 1)
	total := props.IntDefault("total", props.IntDefault("pages", 1))
	return fitLine(fmt.Sprintf("Page %d of %d  [Prev] [Next]", page, total), width, rc.opts.Unicode)
}

func (rc *renderContext) renderHelp(props propMap, width int) string {
	pairs := props.Pairs()
	if len(pairs) > 0 {
		return rc.renderKeyValue(props, width)
	}
	return wrapBlock(sanitize(props.First("text", "message", "description", "title", "label", "content", "markdown")), width, rc.opts.Unicode)
}

func (rc *renderContext) renderAlert(kind component.Kind, props propMap, width int) string {
	role := props.StringDefault("severity", props.StringDefault("variant", "info"))
	label := strings.ToUpper(string(kind))
	msg := sanitize(props.First("message", "text", "title", "description"))
	return fitLine(rc.styled(role, label+": "+msg), width, rc.opts.Unicode)
}

func (rc *renderContext) renderLoading(props propMap, width int) string {
	msg := sanitize(props.StringDefault("message", "Loading"))
	spin := "…"
	if !rc.plain && rc.opts.ColorMode != ColorModeMono && rc.opts.Unicode {
		model := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		spin = model.View()
	} else if !rc.opts.Unicode {
		spin = "..."
	}
	return fitLine(rc.styled("loading", spin+" "+msg), width, rc.opts.Unicode)
}

func (rc *renderContext) renderErrorBoundary(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	if msg := props.First("error", "message"); msg != "" {
		return fitLine(rc.styled("error", "ERROR: "+sanitize(msg)), width, rc.opts.Unicode), nil
	}
	body, err := rc.renderContainer(node, width)
	if err != nil {
		return fitLine(rc.styled("error", "ERROR: "+sanitize(err.Error())), width, rc.opts.Unicode), nil
	}
	return body, nil
}

func (rc *renderContext) renderPrompt(kind component.Kind, node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	body := sanitize(props.First("message", "text", "title"))
	if body == "" {
		body = string(kind)
	}
	if len(node.Children) > 0 {
		children, err := rc.renderContainer(node, maxInt(1, width-4))
		if err != nil {
			return "", err
		}
		body += "\n" + children
	}
	return rc.box(strings.Title(string(kind)), body, width), nil
}

func (rc *renderContext) renderTerminal(props propMap, width int) string {
	text := sanitize(props.First("repaint", "screen", "text", "value", "content", "title", "alt"))
	return fitBlock(text, width, rc.opts.Height, rc.opts.Unicode)
}

func (rc *renderContext) renderUnknown(node component.Node, width int) (string, error) {
	props := parseProps(node.Props)
	if len(node.Children) > 0 {
		return rc.renderContainer(node, width)
	}
	return fitLine("["+sanitize(string(node.Kind))+"] "+sanitize(props.First("text", "label", "value", "message")), width, rc.opts.Unicode), nil
}

func (rc *renderContext) box(title, body string, width int) string {
	if width < 4 {
		return fitBlock(body, width, rc.opts.Height, rc.opts.Unicode)
	}
	innerWidth := maxInt(1, width-4)
	body = fitBlock(body, innerWidth, rc.opts.Height, rc.opts.Unicode)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = "│ " + padRight(line, innerWidth) + " │"
	}
	if !rc.opts.Unicode {
		for i, line := range lines {
			lines[i] = strings.NewReplacer("│", "|").Replace(line)
		}
	}
	top, bottom := "╭"+strings.Repeat("─", innerWidth+2)+"╮", "╰"+strings.Repeat("─", innerWidth+2)+"╯"
	if !rc.opts.Unicode {
		top, bottom = "+"+strings.Repeat("-", innerWidth+2)+"+", "+"+strings.Repeat("-", innerWidth+2)+"+"
	}
	if title != "" {
		title = " " + fitLine(title, maxInt(1, innerWidth), rc.opts.Unicode) + " "
		runes := []rune(top)
		for i, r := range []rune(title) {
			if i+1 < len(runes)-1 {
				runes[i+1] = r
			}
		}
		top = string(runes)
	}
	return strings.Join(append(append([]string{rc.styled("accent", top)}, lines...), rc.styled("accent", bottom)), "\n")
}

func (rc *renderContext) rule(label string, width int) string {
	char := "─"
	if !rc.opts.Unicode {
		char = "-"
	}
	if label == "" {
		return strings.Repeat(char, maxInt(0, width))
	}
	label = " " + sanitize(label) + " "
	rem := maxInt(0, width-lipgloss.Width(label))
	left := rem / 2
	return strings.Repeat(char, left) + label + strings.Repeat(char, rem-left)
}

func (rc *renderContext) styled(role, text string) string {
	if rc.plain || role == "" {
		return text
	}
	return rc.opts.Theme.styleFor(role, rc.opts.ColorMode).Render(text)
}

func (rc *renderContext) statePrefix(props propMap) string {
	var states []string
	for _, key := range []string{"loading", "disabled", "focused", "selected", "checked", "expanded"} {
		if props.Bool(key) {
			states = append(states, key)
		}
	}
	if msg := props.String("error"); msg != "" {
		states = append(states, "error")
	}
	if len(states) == 0 {
		return ""
	}
	return rc.styled(states[0], "("+strings.Join(states, ",")+") ")
}
