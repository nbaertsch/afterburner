package accessibility

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type SourceNode struct {
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Props         map[string]any `json:"props,omitempty"`
	Accessibility *Node          `json:"accessibility,omitempty"`
	Children      []SourceNode   `json:"children,omitempty"`
}

type SourceTree struct {
	Root      SourceNode `json:"root"`
	SurfaceID string     `json:"surfaceId,omitempty"`
	Locale    string     `json:"locale,omitempty"`
	Direction string     `json:"direction,omitempty"`
}

type ProjectionOptions struct {
	HighContrast bool
	NoColor      bool
	KeyboardOnly bool
	Now          func() time.Time
}

type SemanticTree struct {
	SurfaceID    string       `json:"surfaceId,omitempty"`
	Locale       string       `json:"locale,omitempty"`
	Direction    string       `json:"direction,omitempty"`
	HighContrast bool         `json:"highContrast,omitempty"`
	NoColor      bool         `json:"noColor,omitempty"`
	Root         SemanticNode `json:"root"`
	LiveSummary  string       `json:"liveSummary,omitempty"`
	GeneratedAt  time.Time    `json:"generatedAt,omitempty"`
}

type SemanticNode struct {
	ID              string              `json:"id"`
	Role            Role                `json:"role,omitempty"`
	Name            string              `json:"name,omitempty"`
	Description     string              `json:"description,omitempty"`
	States          map[string]string   `json:"states,omitempty"`
	Relations       map[string][]string `json:"relations,omitempty"`
	Focusable       bool                `json:"focusable,omitempty"`
	KeyboardActions []Shortcut          `json:"keyboardActions,omitempty"`
	Live            LivePoliteness      `json:"live,omitempty"`
	Children        []SemanticNode      `json:"children,omitempty"`
}

func Project(tree SourceTree, opts ProjectionOptions) SemanticTree {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	direction := tree.Direction
	if direction == "" {
		direction = "ltr"
	}
	semantic := SemanticTree{SurfaceID: tree.SurfaceID, Locale: tree.Locale, Direction: direction, HighContrast: opts.HighContrast, NoColor: opts.NoColor, GeneratedAt: opts.Now()}
	semantic.Root = projectNode(tree.Root, opts)
	semantic.LiveSummary = SummarizeLiveRegions(semantic.Root, 3)
	return semantic
}

func projectNode(source SourceNode, opts ProjectionOptions) SemanticNode {
	if isHiddenNode(source) {
		return SemanticNode{}
	}
	a11y := source.Accessibility
	node := SemanticNode{ID: source.ID, Role: roleForKind(source.Kind), Name: accessibleName(source), Description: accessibleDescription(source.Props), States: map[string]string{}, Relations: map[string][]string{}}
	if role := roleProp(source.Props); role != "" {
		node.Role = role
	}
	if a11y != nil {
		if a11y.Role != "" {
			node.Role = a11y.Role
		}
		if a11y.Name != "" {
			node.Name = a11y.Name
		}
		if a11y.Description != "" {
			node.Description = a11y.Description
		}
		node.Live = a11y.Live
		if len(a11y.LabelledBy) > 0 {
			node.Relations["labelledBy"] = append([]string(nil), a11y.LabelledBy...)
		}
		if len(a11y.DescribedBy) > 0 {
			node.Relations["describedBy"] = append([]string(nil), a11y.DescribedBy...)
		}
		addBoolState(node.States, "disabled", a11y.Disabled)
		addBoolState(node.States, "readonly", a11y.ReadOnly)
		addBoolState(node.States, "required", a11y.Required)
		if a11y.Invalid != "" {
			node.States["invalid"] = a11y.Invalid
		}
		addPtrState(node.States, "expanded", a11y.Expanded)
		addPtrState(node.States, "selected", a11y.Selected)
		addPtrState(node.States, "checked", a11y.Checked)
		if a11y.Current != "" {
			node.States["current"] = a11y.Current
		}
		if a11y.Level > 0 {
			node.States["level"] = fmt.Sprint(a11y.Level)
		}
		if a11y.PositionInSet > 0 {
			node.States["positionInSet"] = fmt.Sprint(a11y.PositionInSet)
		}
		if a11y.SetSize > 0 {
			node.States["setSize"] = fmt.Sprint(a11y.SetSize)
		}
		if a11y.FocusOrder > 0 {
			node.States["focusOrder"] = fmt.Sprint(a11y.FocusOrder)
		}
		addBoolState(node.States, "atomic", a11y.Atomic)
		if len(a11y.Relevant) > 0 {
			node.States["relevant"] = strings.Join(a11y.Relevant, " ")
		}
		node.KeyboardActions = append(node.KeyboardActions, a11y.KeyboardShortcut...)
	}
	for _, key := range []string{"disabled", "required", "readonly", "selected", "checked", "expanded", "invalid"} {
		if value, ok := source.Props[key]; ok {
			node.States[key] = valueString(value)
		}
	}
	addAriaAliases(node.States, node.Relations, source.Props)
	addInputStates(node.States, source.Kind, source.Props)
	if isDialogKind(source.Kind) {
		node.States["modal"] = fmt.Sprint(boolProp(source.Props, "modal", false))
	}
	if source.Kind == "loading" || source.Kind == "spinner" {
		node.States["busy"] = "true"
	}
	addRangeStates(node.States, source.Kind, source.Props)
	node.Focusable = isFocusableKind(source.Kind) && node.States["disabled"] != "true"
	if opts.KeyboardOnly || node.Focusable {
		node.KeyboardActions = mergeShortcuts(node.KeyboardActions, keyboardForKind(source.Kind, source.Props)...)
	}
	if node.Live == "" {
		node.Live = liveProp(source.Props)
	}
	if node.Live == "" {
		node.Live = liveForKind(source.Kind)
	}
	for _, child := range source.Children {
		projected := projectNode(child, opts)
		if projected.Role == "" && projected.Name == "" && len(projected.Children) == 0 {
			continue
		}
		node.Children = append(node.Children, projected)
	}
	if len(node.States) == 0 {
		node.States = nil
	}
	if len(node.Relations) == 0 {
		node.Relations = nil
	}
	return node
}

func Linearize(tree SemanticTree) []string { return LinearizeNode(tree.Root) }

func LinearizeNode(node SemanticNode) []string {
	var out []string
	linearize(&out, node, 0)
	return out
}

func linearize(out *[]string, node SemanticNode, depth int) {
	if node.Role != "" || node.Name != "" {
		parts := []string{strings.Repeat("  ", depth) + string(node.Role)}
		if node.Name != "" {
			parts = append(parts, QuoteForSpeech(node.Name))
		}
		if node.Description != "" {
			parts = append(parts, "- "+QuoteForSpeech(node.Description))
		}
		if len(node.States) > 0 {
			parts = append(parts, stateSummary(node.States))
		}
		if node.Focusable {
			parts = append(parts, "focusable")
		}
		*out = append(*out, strings.Join(parts, " "))
	}
	for _, child := range node.Children {
		linearize(out, child, depth+1)
	}
}

type LiveRegionThrottler struct {
	MinInterval time.Duration
	MaxItems    int
	last        time.Time
	pending     []string
}

func (t *LiveRegionThrottler) Push(now time.Time, message string) (string, bool) {
	if t.MinInterval <= 0 {
		t.MinInterval = 250 * time.Millisecond
	}
	if t.MaxItems <= 0 {
		t.MaxItems = 3
	}
	if message != "" {
		t.pending = append(t.pending, message)
	}
	if t.last.IsZero() || now.Sub(t.last) >= t.MinInterval {
		t.last = now
		return t.flush(), true
	}
	return "", false
}

func (t *LiveRegionThrottler) Flush(now time.Time) string {
	t.last = now
	return t.flush()
}

func (t *LiveRegionThrottler) flush() string {
	if len(t.pending) == 0 {
		return ""
	}
	items := t.pending
	if len(items) > t.MaxItems {
		items = append(append([]string(nil), items[:t.MaxItems]...), fmt.Sprintf("%d more updates", len(t.pending)-t.MaxItems))
	}
	t.pending = nil
	return strings.Join(items, "; ")
}

func SummarizeLiveRegions(root SemanticNode, max int) string {
	var messages []string
	var walk func(SemanticNode)
	walk = func(n SemanticNode) {
		if n.Live == LivePolite || n.Live == LiveAssertive {
			text := strings.TrimSpace(strings.Join([]string{n.Name, n.Description}, " "))
			if text != "" {
				messages = append(messages, text)
			}
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	if max <= 0 || len(messages) <= max {
		return strings.Join(messages, "; ")
	}
	return strings.Join(append(messages[:max], fmt.Sprintf("%d more live regions", len(messages)-max)), "; ")
}

func QuoteForSpeech(text string) string {
	text = stripANSI(text)
	text = strings.Join(strings.Fields(text), " ")
	return text
}

func HighContrastTokens(noColor bool) map[string]string {
	if noColor {
		return map[string]string{"focus": "[focus]", "selected": "[selected]", "error": "ERROR", "disabled": "disabled"}
	}
	return map[string]string{"focus": "reverse", "selected": "bold", "error": "bright-red", "disabled": "dim"}
}

func roleForKind(kind string) Role {
	switch kind {
	case "application":
		return RoleApplication
	case "button":
		return RoleButton
	case "link":
		return RoleLink
	case "textInput", "passwordInput", "searchInput", "numberInput", "textArea":
		return RoleTextbox
	case "select":
		return RoleCombobox
	case "checkbox":
		return RoleCheckbox
	case "radioGroup":
		return RoleRadioGroup
	case "toggle":
		return RoleSwitch
	case "slider", "progress", "meter", "bar":
		return RoleProgressBar
	case "list":
		return RoleList
	case "grid", "statusGrid":
		return RoleGrid
	case "table":
		return RoleTable
	case "tree":
		return RoleTree
	case "form":
		return RoleGroup
	case "toolbar", "actionBar":
		return RoleToolbar
	case "contextMenu":
		return RoleMenu
	case "tabs":
		return RoleTabList
	case "pagination", "breadcrumb":
		return RoleNavigation
	case "dialog", "confirmation", "prompt":
		return RoleDialog
	case "toast", "alert":
		return RoleStatus
	case "log":
		return RoleLog
	case "commandPalette":
		return RoleSearch
	case "separator":
		return RoleSeparator
	case "image", "video", "chart", "sparkline":
		return RoleImage
	case "code":
		return RoleCode
	case "markdown":
		return RoleDocument
	case "keybindingHint":
		return RoleTooltip
	case "panel", "card", "box", "section", "split", "scroll", "disclosure", "surface", "viewport", "loading", "empty", "help", "errorBoundary":
		return RoleRegion
	default:
		return RoleGroup
	}
}

func roleProp(props map[string]any) Role {
	value := strings.ToLower(strings.TrimSpace(stringPropAny(props, "ariaRole", "role", "landmark")))
	switch value {
	case string(RoleApplication), string(RoleArticle), string(RoleBanner), string(RoleButton), string(RoleCheckbox), string(RoleCode), string(RoleColumnHeader), string(RoleCombobox), string(RoleComplementary), string(RoleContentInfo), string(RoleDialog), string(RoleDocument), string(RoleGrid), string(RoleGridCell), string(RoleGroup), string(RoleHeading), string(RoleImage), string(RoleLink), string(RoleList), string(RoleListItem), string(RoleLog), string(RoleMain), string(RoleMenu), string(RoleMenuItem), string(RoleNavigation), string(RoleOption), string(RoleProgressBar), string(RoleRadio), string(RoleRadioGroup), string(RoleRegion), string(RoleRow), string(RoleRowHeader), string(RoleSearch), string(RoleSeparator), string(RoleStatus), string(RoleSwitch), string(RoleTab), string(RoleTabList), string(RoleTabPanel), string(RoleTable), string(RoleTextbox), string(RoleTimer), string(RoleToolbar), string(RoleTooltip), string(RoleTree), string(RoleTreeItem):
		return Role(value)
	default:
		return ""
	}
}

func accessibleName(source SourceNode) string {
	keys := []string{"ariaLabel", "label", "title", "name", "text", "value", "markdown", "code", "message", "content", "placeholder", "alt"}
	if isSecretInput(source) {
		keys = []string{"ariaLabel", "label", "title", "name", "placeholder"}
	}
	for _, key := range keys {
		if value := stringProp(source.Props, key); value != "" {
			return value
		}
	}
	return source.ID
}

func accessibleDescription(props map[string]any) string {
	for _, key := range []string{"ariaDescription", "description", "help"} {
		if value := stringProp(props, key); value != "" {
			return value
		}
	}
	return ""
}

func isSecretInput(source SourceNode) bool {
	return strings.Contains(strings.ToLower(source.Kind), "password") || strings.EqualFold(stringProp(source.Props, "type"), "password") || boolProp(source.Props, "secret", false)
}

func isHiddenNode(source SourceNode) bool {
	for _, key := range []string{"hidden", "ariaHidden", "aria-hidden"} {
		if boolProp(source.Props, key, false) {
			return true
		}
	}
	return source.Accessibility != nil && source.Accessibility.Hidden
}

func isDialogKind(kind string) bool {
	switch kind {
	case "dialog", "confirmation", "prompt":
		return true
	default:
		return false
	}
}

func isFocusableKind(kind string) bool {
	switch kind {
	case "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "table", "list", "tree", "tabs", "commandPalette", "dialog", "confirmation", "prompt", "pagination", "log", "toolbar", "actionBar", "contextMenu":
		return true
	default:
		return false
	}
}

func keyboardForKind(kind string, props map[string]any) []Shortcut {
	switch kind {
	case "button", "link", "checkbox", "toggle":
		return []Shortcut{{Key: "Enter", Description: "activate"}, {Key: "Space", Description: "activate"}}
	case "textInput", "passwordInput", "searchInput", "numberInput", "textArea":
		return []Shortcut{{Key: "Tab", Description: "leave field"}}
	case "dateInput":
		return []Shortcut{{Key: "Arrow keys", Description: "adjust date"}, {Key: "Enter", Description: "confirm date"}, {Key: "Tab", Description: "leave field"}}
	case "fileInput":
		return []Shortcut{{Key: "Enter", Description: "browse files"}, {Key: "Tab", Description: "leave field"}}
	case "table", "list", "tree", "tabs", "select", "radioGroup", "slider", "pagination", "log", "commandPalette", "toolbar", "actionBar", "contextMenu":
		return []Shortcut{{Key: "Arrow keys", Description: "navigate"}, {Key: "Enter", Description: "select"}}
	case "terminal":
		return []Shortcut{{Key: "Arrow keys", Description: "navigate terminal history"}, {Key: "PageUp/PageDown", Description: "page terminal output"}, {Key: "Tab", Description: "leave terminal"}}
	case "dialog", "confirmation", "prompt":
		shortcuts := []Shortcut{{Key: "Esc", Description: "dismiss"}}
		if boolProp(props, "modal", false) {
			shortcuts = append(shortcuts, Shortcut{Key: "Tab", Description: "cycle focus"})
		}
		return shortcuts
	default:
		return nil
	}
}

func liveProp(props map[string]any) LivePoliteness {
	for _, key := range []string{"ariaLive", "live"} {
		value := strings.ToLower(strings.TrimSpace(stringProp(props, key)))
		switch value {
		case string(LiveOff):
			return LiveOff
		case string(LivePolite):
			return LivePolite
		case string(LiveAssertive):
			return LiveAssertive
		}
	}
	return ""
}

func liveForKind(kind string) LivePoliteness {
	if kind == "toast" || kind == "alert" {
		return LivePolite
	}
	if kind == "log" || kind == "loading" || kind == "spinner" {
		return LivePolite
	}
	return LiveOff
}

func mergeShortcuts(existing []Shortcut, add ...Shortcut) []Shortcut {
	seen := map[string]bool{}
	out := make([]Shortcut, 0, len(existing)+len(add))
	for _, s := range append(existing, add...) {
		key := s.Key + "\x00" + s.Description
		if !seen[key] {
			seen[key] = true
			out = append(out, s)
		}
	}
	return out
}

func stateSummary(states map[string]string) string {
	keys := make([]string, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+states[key])
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func addBoolState(states map[string]string, key string, value bool) {
	if value {
		states[key] = "true"
	}
}

func addPtrState(states map[string]string, key string, value *bool) {
	if value != nil {
		states[key] = fmt.Sprint(*value)
	}
}

func addRangeStates(states map[string]string, kind string, props map[string]any) {
	switch kind {
	case "slider", "progress", "meter", "bar":
	default:
		return
	}
	for _, key := range []string{"value", "min", "max"} {
		if value, ok := props[key]; ok {
			states[key] = valueString(value)
		}
	}
}

func addInputStates(states map[string]string, kind string, props map[string]any) {
	switch kind {
	case "textInput", "passwordInput", "searchInput", "numberInput", "textArea":
	default:
		return
	}
	for state, keys := range map[string][]string{
		"placeholder":  {"ariaPlaceholder", "aria-placeholder", "placeholder"},
		"autocomplete": {"ariaAutoComplete", "ariaAutocomplete", "aria-autocomplete", "autocomplete"},
		"multiline":    {"ariaMultiline", "aria-multiline", "multiline"},
	} {
		if value := stringPropAny(props, keys...); value != "" {
			states[state] = value
		}
	}
	if kind == "textArea" {
		states["multiline"] = "true"
	}
}

func addAriaAliases(states map[string]string, relations map[string][]string, props map[string]any) {
	for state, keys := range map[string][]string{
		"disabled": {"ariaDisabled", "aria-disabled"},
		"readonly": {"ariaReadOnly", "aria-readonly"},
		"required": {"ariaRequired", "aria-required"},
		"selected": {"ariaSelected", "aria-selected"},
		"checked":  {"ariaChecked", "aria-checked"},
		"expanded": {"ariaExpanded", "aria-expanded"},
		"pressed":  {"ariaPressed", "aria-pressed", "pressed"},
		"busy":     {"ariaBusy", "aria-busy"},
	} {
		if value := stringPropAny(props, keys...); value != "" {
			states[state] = value
		}
	}
	if invalid := stringPropAny(props, "ariaInvalid", "aria-invalid"); invalid != "" {
		states["invalid"] = invalid
	}
	if current := stringProp(props, "ariaCurrent"); current != "" {
		states["current"] = current
	} else if current := stringProp(props, "current"); current != "" {
		states["current"] = current
	}
	if position := stringPropAny(props, "ariaPosInSet", "aria-posinset", "positionInSet"); position != "" {
		states["positionInSet"] = position
	}
	if size := stringPropAny(props, "ariaSetSize", "aria-setsize", "setSize"); size != "" {
		states["setSize"] = size
	}
	if level := stringPropAny(props, "ariaLevel", "aria-level", "level"); level != "" {
		states["level"] = level
	}
	if focusOrder := stringPropAny(props, "focusOrder", "tabIndex"); focusOrder != "" {
		states["focusOrder"] = focusOrder
	}
	if atomic := stringPropAny(props, "ariaAtomic", "aria-atomic", "atomic"); atomic != "" {
		states["atomic"] = atomic
	}
	if relevant := stringListProp(props, "ariaRelevant", "aria-relevant", "relevant"); len(relevant) > 0 {
		states["relevant"] = strings.Join(relevant, " ")
	}
	if labelledBy := stringListProp(props, "ariaLabelledBy", "aria-labelledby", "labelledBy"); len(labelledBy) > 0 {
		relations["labelledBy"] = labelledBy
	}
	if describedBy := stringListProp(props, "ariaDescribedBy", "aria-describedby", "describedBy"); len(describedBy) > 0 {
		relations["describedBy"] = describedBy
	}
	if controls := stringListProp(props, "ariaControls", "aria-controls", "controls"); len(controls) > 0 {
		relations["controls"] = controls
	}
	if activeDescendant := stringListProp(props, "ariaActiveDescendant", "aria-activedescendant", "activeDescendant"); len(activeDescendant) > 0 {
		relations["activeDescendant"] = activeDescendant
	}
}

func boolProp(props map[string]any, key string, fallback bool) bool {
	if props == nil {
		return fallback
	}
	switch v := props[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return fallback
	}
}

func stringProp(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	switch v := props[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64, bool, int:
		return fmt.Sprint(v)
	default:
		return ""
	}
}

func stringPropAny(props map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringProp(props, key); value != "" {
			return value
		}
	}
	return ""
}

func stringListProp(props map[string]any, keys ...string) []string {
	if props == nil {
		return nil
	}
	for _, key := range keys {
		switch v := props[key].(type) {
		case string:
			fields := strings.Fields(v)
			if len(fields) > 0 {
				return fields
			}
		case []string:
			if len(v) > 0 {
				return append([]string(nil), v...)
			}
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if text := strings.TrimSpace(valueString(item)); text != "" {
					out = append(out, text)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func valueString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return fmt.Sprint(v)
	case float64:
		return fmt.Sprint(v)
	case json.Number:
		return v.String()
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(text string) string { return ansiPattern.ReplaceAllString(text, "") }
