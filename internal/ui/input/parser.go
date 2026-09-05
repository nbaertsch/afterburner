package input

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultEscapeTimeout     = 25 * time.Millisecond
	DefaultDoubleClickWindow = 500 * time.Millisecond
	maxBufferedInput         = 1 << 20
)

type ParserConfig struct {
	EscapeTimeout     time.Duration
	DoubleClickWindow time.Duration
	Now               func() time.Time
}

type Parser struct {
	cfg         ParserConfig
	buf         []byte
	escStarted  time.Time
	paste       bool
	pasteBuf    []byte
	lastClick   MouseEvent
	lastClickAt time.Time
}

func NewParser(cfg ParserConfig) *Parser {
	if cfg.EscapeTimeout <= 0 {
		cfg.EscapeTimeout = DefaultEscapeTimeout
	}
	if cfg.DoubleClickWindow <= 0 {
		cfg.DoubleClickWindow = DefaultDoubleClickWindow
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Parser{cfg: cfg}
}

func (p *Parser) Feed(data []byte) ([]SemanticEvent, []BrokerMessage) {
	return p.FeedAt(data, p.now())
}

func (p *Parser) FeedAt(data []byte, at time.Time) ([]SemanticEvent, []BrokerMessage) {
	if len(data) > 0 {
		p.buf = append(p.buf, data...)
		if len(p.buf)+len(p.pasteBuf) > maxBufferedInput {
			if len(p.buf) > maxBufferedInput/2 {
				p.buf = append([]byte(nil), p.buf[len(p.buf)-maxBufferedInput/2:]...)
			}
			if len(p.pasteBuf) > maxBufferedInput/2 {
				p.pasteBuf = append([]byte(nil), p.pasteBuf[len(p.pasteBuf)-maxBufferedInput/2:]...)
			}
		}
	}
	var events []SemanticEvent
	var broker []BrokerMessage
	for {
		if p.paste {
			idx := bytes.Index(p.buf, []byte("\x1b[201~"))
			if idx < 0 {
				p.pasteBuf = append(p.pasteBuf, p.buf...)
				p.buf = nil
				break
			}
			p.pasteBuf = append(p.pasteBuf, p.buf[:idx]...)
			p.buf = p.buf[idx+6:]
			events = append(events, SemanticEvent{Type: EventPaste, Paste: PasteEvent{Text: string(p.pasteBuf)}, At: at})
			p.pasteBuf = nil
			p.paste = false
			continue
		}
		if len(p.buf) == 0 {
			p.escStarted = time.Time{}
			break
		}
		event, msg, n, needMore := p.parseOne(at)
		if needMore {
			if len(p.buf) > 0 && p.buf[0] == 0x1b && p.escStarted.IsZero() {
				p.escStarted = at
			}
			break
		}
		if n <= 0 {
			p.buf = p.buf[1:]
			continue
		}
		p.buf = p.buf[n:]
		p.escStarted = time.Time{}
		if msg.Type != "" {
			broker = append(broker, msg)
			continue
		}
		if event.Type != "" {
			events = append(events, event)
		}
	}
	return events, broker
}

func (p *Parser) Advance(now time.Time) ([]SemanticEvent, []BrokerMessage) {
	if len(p.buf) > 0 && p.buf[0] == 0x1b {
		if p.escStarted.IsZero() {
			p.escStarted = now
			return nil, nil
		}
		if now.Sub(p.escStarted) >= p.cfg.EscapeTimeout {
			p.buf = p.buf[1:]
			p.escStarted = time.Time{}
			return []SemanticEvent{{Type: EventKey, Key: KeyEvent{Name: KeyEsc}, At: now}}, nil
		}
	}
	return p.FeedAt(nil, now)
}

func (p *Parser) parseOne(at time.Time) (SemanticEvent, BrokerMessage, int, bool) {
	b := p.buf[0]
	if b == 0x1b {
		return p.parseEscape(at)
	}
	if b < 0x20 || b == 0x7f {
		return SemanticEvent{Type: EventKey, Key: controlKey(b), At: at}, BrokerMessage{}, 1, false
	}
	r, size := utf8.DecodeRune(p.buf)
	if r == utf8.RuneError && size == 1 && !utf8.FullRune(p.buf) {
		return SemanticEvent{}, BrokerMessage{}, 0, true
	}
	return SemanticEvent{Type: EventKey, Key: KeyEvent{Name: KeyRune, Rune: r, Text: string(r)}, At: at}, BrokerMessage{}, size, false
}

func (p *Parser) parseEscape(at time.Time) (SemanticEvent, BrokerMessage, int, bool) {
	if len(p.buf) == 1 {
		if p.escStarted.IsZero() {
			p.escStarted = at
		}
		if at.Sub(p.escStarted) >= p.cfg.EscapeTimeout {
			return SemanticEvent{Type: EventKey, Key: KeyEvent{Name: KeyEsc}, At: at}, BrokerMessage{}, 1, false
		}
		return SemanticEvent{}, BrokerMessage{}, 0, true
	}
	second := p.buf[1]
	if second == '[' {
		return p.parseCSI(at)
	}
	if second == 'O' {
		return p.parseSS3(at)
	}
	if second == ']' {
		return p.parseOSC(at)
	}
	if second >= 0x20 && second != 0x7f {
		r, size := utf8.DecodeRune(p.buf[1:])
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(p.buf[1:]) {
			return SemanticEvent{}, BrokerMessage{}, 0, true
		}
		key := KeyEvent{Name: KeyRune, Rune: r, Text: string(r), Modifiers: Modifiers{Alt: true}}
		return SemanticEvent{Type: EventKey, Key: key, At: at}, BrokerMessage{}, 1 + size, false
	}
	return SemanticEvent{Type: EventKey, Key: KeyEvent{Name: KeyEsc}, At: at}, BrokerMessage{}, 1, false
}

func (p *Parser) parseCSI(at time.Time) (SemanticEvent, BrokerMessage, int, bool) {
	end := -1
	for i := 2; i < len(p.buf); i++ {
		c := p.buf[i]
		if c >= 0x40 && c <= 0x7e {
			end = i
			break
		}
	}
	if end < 0 {
		return SemanticEvent{}, BrokerMessage{}, 0, true
	}
	seq := string(p.buf[:end+1])
	body := string(p.buf[2:end])
	final := p.buf[end]
	if seq == "\x1b[200~" {
		p.paste = true
		return SemanticEvent{}, BrokerMessage{}, end + 1, false
	}
	if isQueryReply(body, final) {
		return SemanticEvent{}, BrokerMessage{Type: "terminal-query-reply", Sequence: seq, Payload: append([]byte(nil), p.buf[:end+1]...), At: at}, end + 1, false
	}
	if final == 'I' && body == "" {
		return SemanticEvent{Type: EventFocus, Focus: FocusEvent{Focused: true}, At: at}, BrokerMessage{}, end + 1, false
	}
	if final == 'O' && body == "" {
		return SemanticEvent{Type: EventFocus, Focus: FocusEvent{Focused: false}, At: at}, BrokerMessage{}, end + 1, false
	}
	if strings.HasPrefix(body, "<") && (final == 'M' || final == 'm') {
		mouse, ok := p.parseSGRMouse(body[1:], final, at)
		if ok {
			return SemanticEvent{Type: EventMouse, Mouse: mouse, At: at}, BrokerMessage{}, end + 1, false
		}
	}
	if final == 't' {
		parts := parseParams(body)
		if len(parts) >= 3 && parts[0] == 8 {
			return SemanticEvent{Type: EventResize, Resize: ResizeEvent{Rows: parts[1], Columns: parts[2]}, At: at}, BrokerMessage{}, end + 1, false
		}
	}
	key, ok := csiKey(body, final)
	if ok {
		return SemanticEvent{Type: EventKey, Key: key, At: at}, BrokerMessage{}, end + 1, false
	}
	return SemanticEvent{}, BrokerMessage{Type: "terminal-control", Sequence: seq, Payload: append([]byte(nil), p.buf[:end+1]...), At: at}, end + 1, false
}

func (p *Parser) parseSS3(at time.Time) (SemanticEvent, BrokerMessage, int, bool) {
	if len(p.buf) < 3 {
		return SemanticEvent{}, BrokerMessage{}, 0, true
	}
	name := map[byte]KeyName{'P': "f1", 'Q': "f2", 'R': "f3", 'S': "f4", 'H': KeyHome, 'F': KeyEnd}[p.buf[2]]
	if name != "" {
		return SemanticEvent{Type: EventKey, Key: KeyEvent{Name: name}, At: at}, BrokerMessage{}, 3, false
	}
	return SemanticEvent{}, BrokerMessage{Type: "terminal-control", Sequence: string(p.buf[:3]), Payload: append([]byte(nil), p.buf[:3]...), At: at}, 3, false
}

func (p *Parser) parseOSC(at time.Time) (SemanticEvent, BrokerMessage, int, bool) {
	for i := 2; i < len(p.buf); i++ {
		if p.buf[i] == 0x07 {
			return SemanticEvent{}, BrokerMessage{Type: "terminal-query-reply", Sequence: string(p.buf[:i+1]), Payload: append([]byte(nil), p.buf[:i+1]...), At: at}, i + 1, false
		}
		if p.buf[i] == 0x1b && i+1 < len(p.buf) && p.buf[i+1] == '\\' {
			return SemanticEvent{}, BrokerMessage{Type: "terminal-query-reply", Sequence: string(p.buf[:i+2]), Payload: append([]byte(nil), p.buf[:i+2]...), At: at}, i + 2, false
		}
	}
	return SemanticEvent{}, BrokerMessage{}, 0, true
}

func (p *Parser) parseSGRMouse(body string, final byte, at time.Time) (MouseEvent, bool) {
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return MouseEvent{}, false
	}
	code, err1 := strconv.Atoi(parts[0])
	x, err2 := strconv.Atoi(parts[1])
	y, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return MouseEvent{}, false
	}
	mod := Modifiers{Shift: code&4 != 0, Alt: code&8 != 0, Ctrl: code&16 != 0}
	base := code & 3
	button := []MouseButton{ButtonLeft, ButtonMiddle, ButtonRight, ButtonNone}[base]
	action := MousePress
	delta := 0
	if code&64 != 0 {
		action = MouseWheel
		if base == 0 {
			button, delta = ButtonWheelUp, -1
		} else {
			button, delta = ButtonWheelDown, 1
		}
	} else if code&32 != 0 {
		action = MouseDrag
	} else if final == 'm' {
		action = MouseRelease
	} else {
		action = MouseClick
	}
	mouse := MouseEvent{Action: action, Button: button, X: x, Y: y, DeltaY: delta, Modifiers: mod}
	if action == MouseClick && p.lastClick.Action == MouseClick && p.lastClick.Button == button && p.lastClick.X == x && p.lastClick.Y == y && at.Sub(p.lastClickAt) <= p.cfg.DoubleClickWindow {
		mouse.Action = MouseDoubleClick
	}
	if action == MouseClick || action == MouseDoubleClick {
		p.lastClick = MouseEvent{Action: MouseClick, Button: button, X: x, Y: y}
		p.lastClickAt = at
	}
	return mouse, true
}

func controlKey(b byte) KeyEvent {
	switch b {
	case '\r', '\n':
		return KeyEvent{Name: KeyEnter, Text: "\n"}
	case '\t':
		return KeyEvent{Name: KeyTab, Text: "\t"}
	case 0x7f, 0x08:
		return KeyEvent{Name: KeyBackspace}
	case 0x1b:
		return KeyEvent{Name: KeyEsc}
	}
	if b >= 1 && b <= 26 {
		return KeyEvent{Name: KeyRune, Rune: rune('a' + b - 1), Text: string(rune('a' + b - 1)), Modifiers: Modifiers{Ctrl: true}}
	}
	return KeyEvent{Name: KeyRune, Rune: rune(b), Text: string(rune(b)), Modifiers: Modifiers{Ctrl: true}}
}

func csiKey(body string, final byte) (KeyEvent, bool) {
	mods := Modifiers{}
	parts := strings.Split(body, ";")
	if len(parts) > 1 {
		if m, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			mods = decodeXTermModifiers(m)
		}
	}
	if body == "" || strings.Contains(body, ";") {
		switch final {
		case 'A':
			return KeyEvent{Name: KeyArrowUp, Modifiers: mods}, true
		case 'B':
			return KeyEvent{Name: KeyArrowDown, Modifiers: mods}, true
		case 'C':
			return KeyEvent{Name: KeyArrowRight, Modifiers: mods}, true
		case 'D':
			return KeyEvent{Name: KeyArrowLeft, Modifiers: mods}, true
		case 'H':
			return KeyEvent{Name: KeyHome, Modifiers: mods}, true
		case 'F':
			return KeyEvent{Name: KeyEnd, Modifiers: mods}, true
		}
	}
	if final == '~' {
		code := parts[0]
		name := map[string]KeyName{"1": KeyHome, "2": KeyInsert, "3": KeyDelete, "4": KeyEnd, "5": KeyPageUp, "6": KeyPageDown, "7": KeyHome, "8": KeyEnd, "11": "f1", "12": "f2", "13": "f3", "14": "f4", "15": "f5", "17": "f6", "18": "f7", "19": "f8", "20": "f9", "21": "f10", "23": "f11", "24": "f12"}[code]
		if name != "" {
			return KeyEvent{Name: name, Modifiers: mods}, true
		}
	}
	return KeyEvent{}, false
}

func decodeXTermModifiers(value int) Modifiers {
	value--
	return Modifiers{Shift: value&1 != 0, Alt: value&2 != 0, Ctrl: value&4 != 0, Meta: value&8 != 0}
}

func parseParams(body string) []int {
	if body == "" {
		return nil
	}
	parts := strings.Split(body, ";")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		v, _ := strconv.Atoi(strings.TrimLeft(part, "?"))
		out = append(out, v)
	}
	return out
}

func isQueryReply(body string, final byte) bool {
	if final == 'R' {
		return true
	}
	if final == 'c' {
		return true
	}
	return false
}

func (p *Parser) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}
