package input

import "time"

type EventType string

const (
	EventKey    EventType = "key"
	EventFocus  EventType = "focus"
	EventMouse  EventType = "mouse"
	EventPaste  EventType = "paste"
	EventResize EventType = "resize"
)

type KeyName string

const (
	KeyRune       KeyName = "rune"
	KeyEsc        KeyName = "esc"
	KeyEnter      KeyName = "enter"
	KeyTab        KeyName = "tab"
	KeyBackspace  KeyName = "backspace"
	KeyDelete     KeyName = "delete"
	KeyInsert     KeyName = "insert"
	KeyHome       KeyName = "home"
	KeyEnd        KeyName = "end"
	KeyPageUp     KeyName = "pageUp"
	KeyPageDown   KeyName = "pageDown"
	KeyArrowUp    KeyName = "arrowUp"
	KeyArrowDown  KeyName = "arrowDown"
	KeyArrowLeft  KeyName = "arrowLeft"
	KeyArrowRight KeyName = "arrowRight"
)

type Modifiers struct {
	Ctrl  bool `json:"ctrl,omitempty"`
	Alt   bool `json:"alt,omitempty"`
	Shift bool `json:"shift,omitempty"`
	Meta  bool `json:"meta,omitempty"`
}

func (m Modifiers) Empty() bool { return !m.Ctrl && !m.Alt && !m.Shift && !m.Meta }

type KeyEvent struct {
	Name      KeyName   `json:"name"`
	Rune      rune      `json:"rune,omitempty"`
	Text      string    `json:"text,omitempty"`
	Modifiers Modifiers `json:"modifiers,omitempty"`
}

type FocusEvent struct {
	Focused bool `json:"focused"`
}

type MouseAction string

const (
	MousePress       MouseAction = "press"
	MouseRelease     MouseAction = "release"
	MouseClick       MouseAction = "click"
	MouseDoubleClick MouseAction = "doubleClick"
	MouseDrag        MouseAction = "drag"
	MouseWheel       MouseAction = "wheel"
)

type MouseButton string

const (
	ButtonNone      MouseButton = "none"
	ButtonLeft      MouseButton = "left"
	ButtonMiddle    MouseButton = "middle"
	ButtonRight     MouseButton = "right"
	ButtonWheelUp   MouseButton = "wheelUp"
	ButtonWheelDown MouseButton = "wheelDown"
)

type MouseEvent struct {
	Action      MouseAction `json:"action"`
	Button      MouseButton `json:"button,omitempty"`
	X           int         `json:"x"`
	Y           int         `json:"y"`
	DeltaY      int         `json:"deltaY,omitempty"`
	Modifiers   Modifiers   `json:"modifiers,omitempty"`
	ComponentID string      `json:"componentId,omitempty"`
}

type PasteEvent struct {
	Text string `json:"text"`
}

type ResizeEvent struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

type SemanticEvent struct {
	Type              EventType   `json:"type"`
	Key               KeyEvent    `json:"key,omitempty"`
	Focus             FocusEvent  `json:"focus,omitempty"`
	Mouse             MouseEvent  `json:"mouse,omitempty"`
	Paste             PasteEvent  `json:"paste,omitempty"`
	Resize            ResizeEvent `json:"resize,omitempty"`
	TargetComponentID string      `json:"targetComponentId,omitempty"`
	At                time.Time   `json:"at,omitempty"`
}

type BrokerMessage struct {
	Type     string    `json:"type"`
	Sequence string    `json:"sequence"`
	Payload  []byte    `json:"payload,omitempty"`
	At       time.Time `json:"at,omitempty"`
}
