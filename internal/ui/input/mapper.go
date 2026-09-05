package input

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

const (
	UIEventFocus    = "ui.focus.change"
	UIEventAction   = "ui.action.invoke"
	UIEventInput    = "ui.input.change"
	UIEventValidate = "ui.form.validate"
	UIEventNavigate = "ui.collection.navigate"
	UIEventDismiss  = "ui.dismiss"
	UIEventSearch   = "ui.search"
	UIEventCopy     = "ui.clipboard.copy"
	UIEventPaste    = "ui.clipboard.paste"
	UIEventResize   = "ui.resize"
	UIEventMouse    = "ui.mouse"
)

type MappingOptions struct {
	SurfaceID string
	Now       func() time.Time
}

type MappingResult struct {
	Events       []bridge.Event `json:"events"`
	Consumed     bool           `json:"consumed"`
	FocusChanged bool           `json:"focusChanged,omitempty"`
	FocusChange  FocusChange    `json:"focusChange,omitempty"`
}

type EventMapper struct {
	focus *FocusManager
	nodes map[string]component.Node
	opts  MappingOptions
	seq   uint64
}

func NewEventMapper(focus *FocusManager, opts MappingOptions) *EventMapper {
	if focus == nil {
		focus = NewFocusManager()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &EventMapper{focus: focus, opts: opts, nodes: map[string]component.Node{}}
}

func (m *EventMapper) UpdateTree(tree component.Tree) FocusChange {
	m.opts.SurfaceID = firstNonEmpty(m.opts.SurfaceID, tree.SurfaceID)
	m.nodes = map[string]component.Node{}
	indexNodes(m.nodes, tree.Root)
	return m.focus.UpdateTree(tree)
}

func (m *EventMapper) Map(event SemanticEvent) MappingResult {
	if event.Type == "" {
		return MappingResult{}
	}
	if event.At.IsZero() {
		event.At = m.now()
	}
	switch event.Type {
	case EventFocus:
		return m.emit(UIEventFocus, m.focus.Current(), "", eventPayload(event), true)
	case EventResize:
		change := m.focus.Restore(FocusReasonResize)
		result := m.emit(UIEventResize, m.focus.Current(), "", eventPayload(event), true)
		result.FocusChanged = change.Accepted
		result.FocusChange = change
		return result
	case EventPaste:
		return m.mapPaste(event)
	case EventMouse:
		return m.mapMouse(event)
	case EventKey:
		return m.mapKey(event)
	default:
		return MappingResult{}
	}
}

func (m *EventMapper) mapKey(event SemanticEvent) MappingResult {
	key := event.Key
	if key.Name == KeyTab {
		var change FocusChange
		if key.Modifiers.Shift {
			change = m.focus.MovePrevious()
		} else {
			change = m.focus.MoveNext()
		}
		result := m.emit(UIEventFocus, change.CurrentID, "", map[string]any{"key": key, "previousId": change.PreviousID, "currentId": change.CurrentID, "reason": change.Reason}, true)
		result.FocusChanged = change.Accepted
		result.FocusChange = change
		return result
	}
	target := firstNonEmpty(event.TargetComponentID, m.focus.Current())
	node, ok := m.nodes[target]
	if !ok {
		return MappingResult{Consumed: true}
	}
	kind := string(node.Kind)
	if key.Modifiers.Ctrl && (key.Rune == 'c' || strings.EqualFold(key.Text, "c")) {
		return m.emit(UIEventCopy, target, actionFor(node, "copy"), map[string]any{"key": key}, true)
	}
	if key.Modifiers.Ctrl && (key.Rune == 'v' || strings.EqualFold(key.Text, "v")) {
		return m.emit(UIEventPaste, target, actionFor(node, "paste"), map[string]any{"key": key}, true)
	}
	if key.Modifiers.Ctrl && (key.Rune == 'f' || strings.EqualFold(key.Text, "f")) {
		return m.emit(UIEventSearch, target, actionFor(node, "search"), map[string]any{"key": key}, true)
	}
	if key.Name == KeyEsc {
		if isDismissible(kind) {
			return m.emit(UIEventDismiss, target, actionFor(node, "dismiss"), map[string]any{"key": key}, true)
		}
		return MappingResult{Consumed: true}
	}
	if isActivationKey(key) && isActionKind(kind) {
		return m.emit(UIEventAction, target, actionFor(node, "activate"), map[string]any{"key": key}, true)
	}
	if isTextualKind(kind) {
		if key.Name == KeyRune || key.Name == KeyBackspace || key.Name == KeyDelete || key.Name == KeyEnter {
			typeName := UIEventInput
			if kind == "form" && key.Name == KeyEnter {
				typeName = UIEventValidate
			}
			return m.emit(typeName, target, actionFor(node, "change"), map[string]any{"key": key}, true)
		}
	}
	if isNavigationKey(key) && isNavigableKind(kind) {
		return m.emit(UIEventNavigate, target, actionFor(node, "navigate"), map[string]any{"key": key, "direction": directionForKey(key)}, true)
	}
	if key.Name == KeyPageUp || key.Name == KeyPageDown {
		return m.emit(UIEventNavigate, target, actionFor(node, "page"), map[string]any{"key": key, "direction": directionForKey(key)}, true)
	}
	return MappingResult{Consumed: true}
}

func (m *EventMapper) mapMouse(event SemanticEvent) MappingResult {
	target := firstNonEmpty(event.Mouse.ComponentID, event.TargetComponentID)
	if target == "" {
		return MappingResult{Consumed: true}
	}
	change := m.focus.RequestFocus(FocusRequest{ComponentID: target, Priority: 10, Reason: FocusReasonRequest, RequestedAt: event.At})
	node := m.nodes[target]
	kind := string(node.Kind)
	typeName := UIEventMouse
	action := ""
	if event.Mouse.Action == MouseClick || event.Mouse.Action == MouseDoubleClick {
		if isActionKind(kind) {
			typeName = UIEventAction
			action = actionFor(node, "activate")
		} else if isNavigableKind(kind) {
			typeName = UIEventNavigate
			action = actionFor(node, "select")
		}
	}
	if event.Mouse.Action == MouseWheel || event.Mouse.Action == MouseDrag {
		typeName = UIEventNavigate
		action = actionFor(node, "scroll")
	}
	result := m.emit(typeName, target, action, map[string]any{"mouse": event.Mouse, "keyboardEquivalent": KeyboardEquivalent(node.Kind, event.Mouse)}, true)
	result.FocusChanged = change.Accepted
	result.FocusChange = change
	return result
}

func (m *EventMapper) mapPaste(event SemanticEvent) MappingResult {
	target := firstNonEmpty(event.TargetComponentID, m.focus.Current())
	node := m.nodes[target]
	payload := map[string]any{"text": event.Paste.Text, "bytes": len(event.Paste.Text)}
	if isSecretNode(node) {
		payload["protected"] = true
		payload["text"] = ""
	}
	return m.emit(UIEventPaste, target, actionFor(node, "paste"), payload, true)
}

func (m *EventMapper) emit(typeName, componentID, actionID string, payload any, consumed bool) MappingResult {
	m.seq++
	data, _ := json.Marshal(payload)
	return MappingResult{Consumed: consumed, Events: []bridge.Event{{SchemaVersion: 1, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, ID: fmt.Sprintf("input-%d", m.seq), Type: typeName, SurfaceID: m.opts.SurfaceID, ComponentID: componentID, ActionID: actionID, Phase: bridge.EventTarget, Timestamp: m.now(), Payload: data, Trusted: true, PreventDefault: consumed}}}
}

func (m *EventMapper) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

func InteractiveKinds() []component.Kind {
	return []component.Kind{
		component.KindButton, component.KindLink, component.KindTextInput, "passwordInput", "searchInput", "numberInput", component.KindTextArea, component.KindSelect, component.KindCheckbox, component.KindRadioGroup, component.KindToggle, component.KindSlider, "dateInput", "fileInput", component.KindForm, component.KindTable, component.KindList, component.KindTree, component.KindTabs, "split", component.KindCommandPalette, component.KindDialog, "confirmation", "prompt", "actionBar", component.KindToolbar, "pagination", "log", "contextMenu", component.KindTerminal,
	}
}

func KeyboardEquivalent(kind component.Kind, mouse MouseEvent) KeyEvent {
	if mouse.Action == MouseWheel {
		if mouse.DeltaY < 0 {
			return KeyEvent{Name: KeyArrowUp}
		}
		return KeyEvent{Name: KeyArrowDown}
	}
	if isActionKind(string(kind)) {
		return KeyEvent{Name: KeyEnter}
	}
	if isNavigableKind(string(kind)) {
		return KeyEvent{Name: KeyArrowDown}
	}
	return KeyEvent{Name: KeyTab}
}

func indexNodes(out map[string]component.Node, node component.Node) {
	id := node.ID
	if id == "" {
		id = node.Key
	}
	if id != "" {
		out[id] = node
	}
	for _, child := range node.Children {
		indexNodes(out, child)
	}
}

func isActivationKey(key KeyEvent) bool {
	return key.Name == KeyEnter || (key.Name == KeyRune && key.Rune == ' ')
}

func isNavigationKey(key KeyEvent) bool {
	switch key.Name {
	case KeyArrowUp, KeyArrowDown, KeyArrowLeft, KeyArrowRight, KeyHome, KeyEnd:
		return true
	default:
		return false
	}
}

func directionForKey(key KeyEvent) string {
	switch key.Name {
	case KeyArrowUp:
		return "previous"
	case KeyArrowDown:
		return "next"
	case KeyArrowLeft:
		return "left"
	case KeyArrowRight:
		return "right"
	case KeyHome:
		return "first"
	case KeyEnd:
		return "last"
	case KeyPageUp:
		return "pageUp"
	case KeyPageDown:
		return "pageDown"
	default:
		return ""
	}
}

func isActionKind(kind string) bool {
	switch kind {
	case "button", "link", "checkbox", "toggle", "radioGroup", "select", "fileInput", "dateInput", "commandPalette", "dialog", "confirmation", "prompt", "actionBar", "toolbar", "contextMenu":
		return true
	default:
		return false
	}
}

func isTextualKind(kind string) bool {
	switch kind {
	case "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "form", "commandPalette":
		return true
	default:
		return false
	}
}

func isNavigableKind(kind string) bool {
	switch kind {
	case "table", "list", "tree", "tabs", "split", "pagination", "log", "terminal", "select", "radioGroup", "slider", "commandPalette", "contextMenu":
		return true
	default:
		return false
	}
}

func isDismissible(kind string) bool {
	switch kind {
	case "dialog", "commandPalette", "confirmation", "prompt", "contextMenu":
		return true
	default:
		return false
	}
}

func actionFor(node component.Node, fallback string) string {
	if node.ActionBindings != nil {
		for _, key := range []string{fallback, "on" + strings.Title(fallback), "default"} {
			if value := node.ActionBindings[key]; value != "" {
				return value
			}
		}
	}
	props := parseNodeProps(node.Props)
	for _, key := range []string{"actionId", fallback + "Action", "on" + strings.Title(fallback)} {
		if value := stringProp(props, key); value != "" {
			return value
		}
	}
	return fallback
}

func eventPayload(event SemanticEvent) map[string]any { return map[string]any{"event": event} }

func isSecretNode(node component.Node) bool {
	props := parseNodeProps(node.Props)
	return strings.Contains(strings.ToLower(string(node.Kind)), "password") || boolProp(props, "secret", false) || strings.EqualFold(stringProp(props, "type"), "password")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
