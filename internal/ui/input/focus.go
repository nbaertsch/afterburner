package input

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/component"
)

type FocusReason string

const (
	FocusReasonInitial FocusReason = "initial"
	FocusReasonTab     FocusReason = "tab"
	FocusReasonRequest FocusReason = "request"
	FocusReasonRestore FocusReason = "restore"
	FocusReasonModal   FocusReason = "modal"
	FocusReasonCrash   FocusReason = "crash"
	FocusReasonResize  FocusReason = "resize"
)

type FocusNode struct {
	ComponentID string         `json:"componentId"`
	ParentID    string         `json:"parentId,omitempty"`
	SurfaceID   string         `json:"surfaceId,omitempty"`
	Kind        component.Kind `json:"kind"`
	TabIndex    int            `json:"tabIndex,omitempty"`
	Order       int            `json:"order,omitempty"`
	Focusable   bool           `json:"focusable"`
	Enabled     bool           `json:"enabled"`
	Visible     bool           `json:"visible"`
	Trap        bool           `json:"trap,omitempty"`
	Secret      bool           `json:"secret,omitempty"`
	Modal       bool           `json:"modal,omitempty"`
}

type FocusRequest struct {
	ComponentID string      `json:"componentId"`
	SurfaceID   string      `json:"surfaceId,omitempty"`
	Priority    int         `json:"priority,omitempty"`
	Reason      FocusReason `json:"reason,omitempty"`
	RequestedAt time.Time   `json:"requestedAt,omitempty"`
}

type FocusChange struct {
	PreviousID string      `json:"previousId,omitempty"`
	CurrentID  string      `json:"currentId,omitempty"`
	Reason     FocusReason `json:"reason"`
	Accepted   bool        `json:"accepted"`
	Message    string      `json:"message,omitempty"`
}

type FocusManager struct {
	nodes     map[string]FocusNode
	order     []string
	current   string
	restore   []string
	trapStack []string
	lastReq   FocusRequest
	sequence  int
}

func NewFocusManager() *FocusManager {
	return &FocusManager{nodes: map[string]FocusNode{}}
}

func (m *FocusManager) UpdateTree(tree component.Tree) FocusChange {
	if m.nodes == nil {
		m.nodes = map[string]FocusNode{}
	}
	previous := m.current
	m.nodes = map[string]FocusNode{}
	m.order = nil
	m.sequence = 0
	m.walk(tree.SurfaceID, "", tree.Root)
	m.sortOrder()
	m.rebuildTraps()
	if previous != "" && m.isFocusable(previous) {
		m.current = previous
		return FocusChange{PreviousID: previous, CurrentID: previous, Reason: FocusReasonRestore, Accepted: true}
	}
	if trap := m.activeTrap(); trap != "" {
		if previous != "" && m.isFocusableIgnoringTrap(previous) && !m.isDescendantOrSelf(previous, trap) {
			m.restore = prependUnique(previous, m.restore)
		}
		m.current = m.firstInActiveScope()
		return FocusChange{PreviousID: previous, CurrentID: m.current, Reason: FocusReasonModal, Accepted: m.current != ""}
	}
	if restored := m.restoreCandidate(); restored != "" {
		m.current = restored
		return FocusChange{PreviousID: previous, CurrentID: restored, Reason: FocusReasonRestore, Accepted: true}
	}
	m.current = m.firstInActiveScope()
	return FocusChange{PreviousID: previous, CurrentID: m.current, Reason: FocusReasonInitial, Accepted: m.current != ""}
}

func (m *FocusManager) Current() string { return m.current }

func (m *FocusManager) Node(id string) (FocusNode, bool) {
	n, ok := m.nodes[id]
	return n, ok
}

func (m *FocusManager) TabOrder() []string { return append([]string(nil), m.activeOrder()...) }

func (m *FocusManager) MoveNext() FocusChange { return m.move(1, FocusReasonTab) }

func (m *FocusManager) MovePrevious() FocusChange { return m.move(-1, FocusReasonTab) }

func (m *FocusManager) RequestFocus(req FocusRequest) FocusChange {
	if req.Reason == "" {
		req.Reason = FocusReasonRequest
	}
	if req.ComponentID == "" || !m.isFocusable(req.ComponentID) {
		return FocusChange{PreviousID: m.current, CurrentID: m.current, Reason: req.Reason, Accepted: false, Message: "target is not focusable"}
	}
	if !m.inActiveTrap(req.ComponentID) {
		return FocusChange{PreviousID: m.current, CurrentID: m.current, Reason: req.Reason, Accepted: false, Message: "focus trapped by modal/dialog"}
	}
	if req.Priority < m.lastReq.Priority && req.RequestedAt.Before(m.lastReq.RequestedAt.Add(time.Second)) {
		return FocusChange{PreviousID: m.current, CurrentID: m.current, Reason: req.Reason, Accepted: false, Message: "superseded by higher priority request"}
	}
	prev := m.current
	if prev != "" && prev != req.ComponentID {
		m.restore = append([]string{prev}, m.restore...)
	}
	m.current = req.ComponentID
	m.lastReq = req
	return FocusChange{PreviousID: prev, CurrentID: m.current, Reason: req.Reason, Accepted: true}
}

func (m *FocusManager) PushTrap(componentID string) FocusChange {
	n, ok := m.nodes[componentID]
	if !ok {
		return FocusChange{PreviousID: m.current, CurrentID: m.current, Reason: FocusReasonModal, Accepted: false, Message: "unknown trap"}
	}
	if !n.Modal || !n.Trap {
		return FocusChange{PreviousID: m.current, CurrentID: m.current, Reason: FocusReasonModal, Accepted: false, Message: "target is not a modal focus trap"}
	}
	m.trapStack = append(m.trapStack, componentID)
	if m.current != "" {
		m.restore = append([]string{m.current}, m.restore...)
	}
	first := m.firstInScope(componentID)
	prev := m.current
	m.current = first
	return FocusChange{PreviousID: prev, CurrentID: first, Reason: FocusReasonModal, Accepted: first != ""}
}

func (m *FocusManager) PopTrap(componentID string) FocusChange {
	for i := len(m.trapStack) - 1; i >= 0; i-- {
		if m.trapStack[i] == componentID {
			m.trapStack = append(m.trapStack[:i], m.trapStack[i+1:]...)
			break
		}
	}
	prev := m.current
	m.current = m.restoreCandidate()
	if m.current == "" {
		m.current = m.firstInActiveScope()
	}
	return FocusChange{PreviousID: prev, CurrentID: m.current, Reason: FocusReasonRestore, Accepted: m.current != ""}
}

func (m *FocusManager) Restore(reason FocusReason) FocusChange {
	prev := m.current
	if restored := m.restoreCandidate(); restored != "" {
		m.current = restored
		return FocusChange{PreviousID: prev, CurrentID: restored, Reason: reason, Accepted: true}
	}
	if m.isFocusable(prev) {
		return FocusChange{PreviousID: prev, CurrentID: prev, Reason: reason, Accepted: true}
	}
	m.current = m.firstInActiveScope()
	return FocusChange{PreviousID: prev, CurrentID: m.current, Reason: reason, Accepted: m.current != ""}
}

func (m *FocusManager) move(delta int, reason FocusReason) FocusChange {
	order := m.activeOrder()
	prev := m.current
	if len(order) == 0 {
		m.current = ""
		return FocusChange{PreviousID: prev, CurrentID: "", Reason: reason, Accepted: false}
	}
	idx := -1
	for i, id := range order {
		if id == m.current {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
	} else {
		idx = (idx + delta + len(order)) % len(order)
	}
	m.current = order[idx]
	return FocusChange{PreviousID: prev, CurrentID: m.current, Reason: reason, Accepted: true}
}

func (m *FocusManager) walk(surfaceID, parent string, node component.Node) {
	m.sequence++
	props := parseNodeProps(node.Props)
	id := node.ID
	if id == "" {
		id = node.Key
	}
	if id != "" {
		n := FocusNode{ComponentID: id, ParentID: parent, SurfaceID: surfaceID, Kind: node.Kind, Order: m.sequence, TabIndex: intProp(props, "tabIndex", 0), Enabled: !boolProp(props, "disabled", false), Visible: !boolProp(props, "hidden", false)}
		n.Focusable = boolProp(props, "focusable", defaultFocusable(node.Kind))
		n.Modal = isDialogLike(node.Kind) && boolProp(props, "modal", false)
		n.Trap = n.Modal && boolProp(props, "focusTrap", true)
		n.Secret = boolProp(props, "secret", false) || strings.Contains(strings.ToLower(string(node.Kind)), "password") || strings.EqualFold(stringProp(props, "type"), "password")
		if node.Accessibility != nil {
			if node.Accessibility.FocusOrder != 0 {
				n.TabIndex = node.Accessibility.FocusOrder
			}
			n.Enabled = n.Enabled && !node.Accessibility.Disabled
			n.Visible = n.Visible && !node.Accessibility.Hidden
		}
		m.nodes[id] = n
		if n.Focusable && n.Enabled && n.Visible && n.TabIndex >= 0 {
			m.order = append(m.order, id)
		}
		parent = id
	}
	for _, child := range node.Children {
		m.walk(surfaceID, parent, child)
	}
}

func (m *FocusManager) sortOrder() {
	sort.SliceStable(m.order, func(i, j int) bool {
		left, right := m.nodes[m.order[i]], m.nodes[m.order[j]]
		if left.TabIndex != right.TabIndex {
			if left.TabIndex == 0 {
				return false
			}
			if right.TabIndex == 0 {
				return true
			}
			return left.TabIndex < right.TabIndex
		}
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return left.ComponentID < right.ComponentID
	})
}

func (m *FocusManager) rebuildTraps() {
	valid := m.trapStack[:0]
	for _, id := range m.trapStack {
		if n, ok := m.nodes[id]; ok && n.Trap {
			valid = append(valid, id)
		}
	}
	m.trapStack = valid
	for _, id := range m.order {
		if n := m.nodes[id]; n.Trap && n.Modal && !containsID(m.trapStack, id) {
			m.trapStack = append(m.trapStack, id)
		}
	}
}

func (m *FocusManager) activeTrap() string {
	if len(m.trapStack) == 0 {
		return ""
	}
	return m.trapStack[len(m.trapStack)-1]
}

func (m *FocusManager) activeOrder() []string {
	trap := m.activeTrap()
	if trap == "" {
		return append([]string(nil), m.order...)
	}
	out := make([]string, 0, len(m.order))
	for _, id := range m.order {
		if id != trap && m.isDescendantOrSelf(id, trap) {
			out = append(out, id)
		}
	}
	if len(out) == 0 && m.isFocusable(trap) {
		out = append(out, trap)
	}
	return out
}

func (m *FocusManager) firstInActiveScope() string {
	order := m.activeOrder()
	if len(order) == 0 {
		return ""
	}
	return order[0]
}

func (m *FocusManager) firstInScope(scope string) string {
	for _, id := range m.order {
		if id != scope && m.isDescendantOrSelf(id, scope) {
			return id
		}
	}
	if m.isFocusable(scope) {
		return scope
	}
	return ""
}

func (m *FocusManager) inActiveTrap(id string) bool {
	trap := m.activeTrap()
	return trap == "" || m.isDescendantOrSelf(id, trap)
}

func (m *FocusManager) isDescendantOrSelf(id, ancestor string) bool {
	for id != "" {
		if id == ancestor {
			return true
		}
		n := m.nodes[id]
		id = n.ParentID
	}
	return false
}

func (m *FocusManager) isFocusable(id string) bool {
	return m.isFocusableIgnoringTrap(id) && m.inActiveTrap(id)
}

func (m *FocusManager) isFocusableIgnoringTrap(id string) bool {
	n, ok := m.nodes[id]
	return ok && n.Focusable && n.Enabled && n.Visible && n.TabIndex >= 0
}

func (m *FocusManager) restoreCandidate() string {
	for len(m.restore) > 0 {
		id := m.restore[0]
		if m.isFocusable(id) {
			m.restore = m.restore[1:]
			return id
		}
		if m.activeTrap() != "" && m.isFocusableIgnoringTrap(id) {
			return ""
		}
		m.restore = m.restore[1:]
	}
	return ""
}

func isDialogLike(kind component.Kind) bool {
	switch string(kind) {
	case "dialog", "confirmation", "prompt":
		return true
	default:
		return false
	}
}

func defaultFocusable(kind component.Kind) bool {
	switch string(kind) {
	case "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "table", "list", "tree", "tabs", "commandPalette", "dialog", "confirmation", "prompt", "pagination", "log", "terminal":
		return true
	default:
		return false
	}
}

func parseNodeProps(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func boolProp(props map[string]any, key string, fallback bool) bool {
	if v, ok := props[key].(bool); ok {
		return v
	}
	return fallback
}

func intProp(props map[string]any, key string, fallback int) int {
	switch v := props[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

func stringProp(props map[string]any, key string) string {
	if v, ok := props[key].(string); ok {
		return v
	}
	return ""
}

func prependUnique(id string, ids []string) []string {
	out := []string{id}
	for _, candidate := range ids {
		if candidate != id {
			out = append(out, candidate)
		}
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
