package terminal

import "sync"

// TerminalAttr is a bit-set of SGR text attributes captured by the terminal
// surface. The values are stable so host UI renderers can translate them into
// their own style systems without taking over terminal stdin/stdout handling.
type TerminalAttr uint16

const (
	TerminalAttrBold TerminalAttr = 1 << iota
	TerminalAttrDim
	TerminalAttrItalic
	TerminalAttrUnderline
	TerminalAttrBlink
	TerminalAttrInverse
	TerminalAttrHidden
	TerminalAttrStrike
)

// TerminalColorMode identifies how a captured SGR color is represented.
type TerminalColorMode uint8

const (
	TerminalColorDefault TerminalColorMode = iota
	TerminalColorPalette
	TerminalColorRGB
)

// TerminalColor stores either a default, 8/16/256-color palette index, or RGB
// color from SGR 38/48 sequences.
type TerminalColor struct {
	Mode  TerminalColorMode
	Index uint8
	R     uint8
	G     uint8
	B     uint8
}

// TerminalStyle is the complete style for a visible terminal cell.
type TerminalStyle struct {
	Foreground TerminalColor
	Background TerminalColor
	Attrs      TerminalAttr
}

// CursorShape mirrors DECSCUSR cursor shapes. The zero value leaves the host's
// default cursor shape unchanged.
type CursorShape uint8

const (
	CursorShapeDefault CursorShape = iota
	CursorShapeBlinkingBlock
	CursorShapeSteadyBlock
	CursorShapeBlinkingUnderline
	CursorShapeSteadyUnderline
	CursorShapeBlinkingBar
	CursorShapeSteadyBar
)

// TerminalCursor describes the modeled cursor in zero-based cell coordinates.
type TerminalCursor struct {
	Row     int
	Col     int
	Visible bool
	Shape   CursorShape
}

// TerminalCell is one grid cell. Continuation cells for wide grapheme clusters
// have Continuation=true, Width=0, and an empty Grapheme.
type TerminalCell struct {
	Grapheme     string
	Width        int
	Style        TerminalStyle
	Hyperlink    string
	Continuation bool
}

// TerminalSnapshot is a copy of the current terminal surface. It is semantic
// state, not a raw byte replay log, so it can survive modal overlays, crashes,
// and resizes.
type TerminalSnapshot struct {
	Size            Size
	Rows            [][]TerminalCell
	Cursor          TerminalCursor
	AlternateScreen bool
	ScrollTop       int
	ScrollBottom    int
	Title           string
}

// TerminalSurface is the host-facing contract for terminal-backed UI. It lets
// renderers consume child output and inspect/repaint the modeled screen without
// ceding stdin/stdout/raw-mode ownership to a UI library.
type TerminalSurface interface {
	Consume(data []byte)
	ConsumeOutput(data []byte)
	Resize(size Size)
	Snapshot() TerminalSnapshot
	Repaint() string
}

// ScreenMirror is a style-aware, bounded VT screen mirror suitable for terminal
// restoration and host UI rendering.
type ScreenMirror struct {
	mu     sync.Mutex
	screen *vtScreen
}

func NewScreenMirror(size Size) *ScreenMirror {
	return &ScreenMirror{screen: newVTScreen(size)}
}

func NewTerminalSurface(size Size) TerminalSurface {
	return NewScreenMirror(size)
}

func (m *ScreenMirror) Consume(data []byte) {
	m.ConsumeOutput(data)
}

func (m *ScreenMirror) ConsumeOutput(data []byte) {
	if m == nil || len(data) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screen.Consume(data)
}

func (m *ScreenMirror) Resize(size Size) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screen.Resize(size)
}

func (m *ScreenMirror) Snapshot() TerminalSnapshot {
	if m == nil {
		return TerminalSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.screen.Snapshot()
}

func (m *ScreenMirror) Repaint() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.screen.Repaint()
}
