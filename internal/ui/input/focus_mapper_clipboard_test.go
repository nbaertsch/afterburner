package input

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
)

func TestFocusGraphTabOrderTrapRestoreAndArbitration(t *testing.T) {
	tree := component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: []component.Node{
		node("later", component.KindButton, map[string]any{"tabIndex": 2}),
		node("first", component.KindTextInput, map[string]any{"tabIndex": 1}),
		{ID: "dialog", Kind: component.KindDialog, Props: raw(map[string]any{"modal": true}), Children: []component.Node{
			node("cancel", component.KindButton, nil),
			node("ok", component.KindButton, nil),
		}},
	}}}
	fm := NewFocusManager()
	change := fm.UpdateTree(tree)
	if !change.Accepted || fm.Current() != "cancel" {
		t.Fatalf("modal trap should focus first dialog control: %#v current=%s", change, fm.Current())
	}
	if got := fm.TabOrder(); len(got) != 2 || got[0] != "cancel" || got[1] != "ok" {
		t.Fatalf("trapped tab order = %#v", got)
	}
	if c := fm.RequestFocus(FocusRequest{ComponentID: "first", Priority: 100, RequestedAt: time.Unix(1, 0)}); c.Accepted {
		t.Fatalf("focus leaked outside trap: %#v", c)
	}
	fm.PopTrap("dialog")
	if got := fm.TabOrder(); len(got) < 4 || got[0] != "first" || got[1] != "later" || got[len(got)-2] != "cancel" || got[len(got)-1] != "ok" {
		t.Fatalf("deterministic tab order = %#v", got)
	}
	low := fm.RequestFocus(FocusRequest{ComponentID: "later", Priority: 1, RequestedAt: time.Unix(1, 0)})
	high := fm.RequestFocus(FocusRequest{ComponentID: "first", Priority: 5, RequestedAt: time.Unix(1, 0)})
	stale := fm.RequestFocus(FocusRequest{ComponentID: "later", Priority: 1, RequestedAt: time.Unix(1, 500)})
	if !low.Accepted || !high.Accepted || stale.Accepted || fm.Current() != "first" {
		t.Fatalf("arbitration mismatch low=%#v high=%#v stale=%#v current=%s", low, high, stale, fm.Current())
	}
	updated := component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: []component.Node{node("first", component.KindTextInput, nil)}}}
	fm.UpdateTree(updated)
	if fm.Current() != "first" {
		t.Fatalf("restore after render lost focus: %s", fm.Current())
	}
}

func TestDialogModalityControlsFocusTrapAndRestore(t *testing.T) {
	base := component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: []component.Node{
		node("first", component.KindTextInput, map[string]any{"tabIndex": 1}),
		{ID: "dialog", Kind: component.KindDialog, Props: raw(map[string]any{"modal": false}), Children: []component.Node{
			node("cancel", component.KindButton, nil),
			node("ok", component.KindButton, nil),
		}},
		node("after", component.KindButton, nil),
	}}}
	fm := NewFocusManager()
	if change := fm.UpdateTree(base); !change.Accepted || fm.Current() != "first" {
		t.Fatalf("non-modal dialog should not steal focus: change=%#v current=%s", change, fm.Current())
	}
	if got := fm.TabOrder(); len(got) != 5 || got[0] != "first" || got[1] != "dialog" || got[2] != "cancel" || got[3] != "ok" || got[4] != "after" {
		t.Fatalf("non-modal tab order should include page and dialog controls: %#v", got)
	}
	if change := fm.RequestFocus(FocusRequest{ComponentID: "after", Priority: 1, RequestedAt: time.Unix(1, 0)}); !change.Accepted || fm.Current() != "after" {
		t.Fatalf("non-modal dialog should allow outside focus: change=%#v current=%s", change, fm.Current())
	}
	if change := fm.PushTrap("dialog"); change.Accepted {
		t.Fatalf("non-modal dialog should not accept manual focus trap: %#v", change)
	}

	modal := base
	modal.Root.Children[1].Props = raw(map[string]any{"modal": true})
	if change := fm.UpdateTree(modal); !change.Accepted || fm.Current() != "cancel" {
		t.Fatalf("modal dialog should trap focus in first control: change=%#v current=%s", change, fm.Current())
	}
	if got := fm.TabOrder(); len(got) != 2 || got[0] != "cancel" || got[1] != "ok" {
		t.Fatalf("modal tab order should be trapped: %#v", got)
	}
	if change := fm.RequestFocus(FocusRequest{ComponentID: "after", Priority: 10, RequestedAt: time.Unix(2, 0)}); change.Accepted {
		t.Fatalf("modal dialog allowed outside focus: %#v", change)
	}
	if change := fm.Restore(FocusReasonResize); !change.Accepted || fm.Current() != "cancel" {
		t.Fatalf("restore while modal should keep focus inside without dropping outside target: change=%#v current=%s", change, fm.Current())
	}
	closed := component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: []component.Node{
		node("first", component.KindTextInput, map[string]any{"tabIndex": 1}),
		node("after", component.KindButton, nil),
	}}}
	if change := fm.UpdateTree(closed); !change.Accepted || fm.Current() != "after" {
		t.Fatalf("closing modal should restore previous outside focus: change=%#v current=%s", change, fm.Current())
	}
}

func TestFocusOrderCoversInteractiveCatalogComponents(t *testing.T) {
	children := make([]component.Node, 0, len(InteractiveKinds()))
	for _, kind := range InteractiveKinds() {
		children = append(children, node(string(kind)+"-focus", kind, nil))
	}
	fm := NewFocusManager()
	fm.UpdateTree(component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: children}})
	order := fm.TabOrder()
	if len(order) != len(children) {
		t.Fatalf("focus order should include every interactive kind: got %d want %d order=%#v", len(order), len(children), order)
	}
	seen := map[string]bool{}
	for _, id := range order {
		seen[id] = true
	}
	for _, kind := range InteractiveKinds() {
		id := string(kind) + "-focus"
		if !seen[id] {
			t.Fatalf("focus order missing %s (%s): %#v", kind, id, order)
		}
	}
}

func TestEventMapperCoversInteractiveCatalogComponents(t *testing.T) {
	for _, kind := range InteractiveKinds() {
		t.Run(string(kind), func(t *testing.T) {
			id := string(kind) + "-id"
			tree := component.Tree{SurfaceID: "s", Root: component.Node{ID: "root", Kind: component.KindApplication, Children: []component.Node{node(id, kind, map[string]any{"actionId": "act"})}}}
			mapper := NewEventMapper(nil, MappingOptions{SurfaceID: "s", Now: func() time.Time { return time.Unix(2, 0) }})
			mapper.UpdateTree(tree)
			key := KeyEvent{Name: KeyEnter}
			if string(kind) == "table" || string(kind) == "list" || string(kind) == "tree" || string(kind) == "tabs" || string(kind) == "split" || string(kind) == "pagination" || string(kind) == "log" || string(kind) == "terminal" || string(kind) == "slider" {
				key = KeyEvent{Name: KeyArrowDown}
			}
			if string(kind) == "textInput" || string(kind) == "passwordInput" || string(kind) == "searchInput" || string(kind) == "numberInput" || string(kind) == "textArea" || string(kind) == "form" {
				key = KeyEvent{Name: KeyRune, Rune: 'x', Text: "x"}
			}
			result := mapper.Map(SemanticEvent{Type: EventKey, Key: key, TargetComponentID: id})
			if !result.Consumed || len(result.Events) == 0 || !result.Events[0].PreventDefault || !result.Events[0].Trusted {
				t.Fatalf("kind %s did not map to trusted consumed event: %#v", kind, result)
			}
			mouse := mapper.Map(SemanticEvent{Type: EventMouse, Mouse: MouseEvent{Action: MouseClick, Button: ButtonLeft, ComponentID: id}})
			if !mouse.Consumed || len(mouse.Events) == 0 {
				t.Fatalf("kind %s lacks mouse mapping: %#v", kind, mouse)
			}
		})
	}
}

func TestClipboardBrokerProtectsSecretsAndAudits(t *testing.T) {
	audit := &auditSink{}
	backend := &memoryClipboard{text: "paste"}
	broker := NewClipboardBroker(ClipboardBrokerConfig{Backend: backend, Audit: audit, MaxBytes: 10})
	if err := broker.Copy(context.Background(), ClipboardRequest{SurfaceID: "s", ComponentID: "password", Text: "secret", Secret: true}); !errors.Is(err, ErrSecretClipboard) {
		t.Fatalf("secret copy err = %v", err)
	}
	if _, err := broker.Paste(context.Background(), ClipboardRequest{SurfaceID: "s", ComponentID: "password", Secret: true}); !errors.Is(err, ErrSecretClipboard) {
		t.Fatalf("secret paste err = %v", err)
	}
	if err := broker.Copy(context.Background(), ClipboardRequest{SurfaceID: "s", ComponentID: "field", Text: "safe"}); err != nil {
		t.Fatal(err)
	}
	if backend.text != "safe" || len(audit.records) < 3 {
		t.Fatalf("clipboard/audit mismatch text=%q audit=%d", backend.text, len(audit.records))
	}
}

type memoryClipboard struct{ text string }

func (m *memoryClipboard) ReadText(context.Context) (string, error)       { return m.text, nil }
func (m *memoryClipboard) WriteText(_ context.Context, text string) error { m.text = text; return nil }

type auditSink struct{ records []bridge.AuditRecord }

func (a *auditSink) Record(_ context.Context, record bridge.AuditRecord) error {
	a.records = append(a.records, record)
	return nil
}

func node(id string, kind component.Kind, props map[string]any) component.Node {
	return component.Node{ID: id, Kind: kind, Props: raw(props)}
}

func raw(value map[string]any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	return data
}
