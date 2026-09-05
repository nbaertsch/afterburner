package input

import (
	"strings"
	"testing"
	"time"
)

func TestParserSemanticEventsAndBrokerReplies(t *testing.T) {
	now := time.Unix(10, 0)
	p := NewParser(ParserConfig{Now: func() time.Time { return now }})
	events, broker := p.Feed([]byte("a\x03\x1b[A\x1b[1;5B\x1b[I\x1b[8;40;120t"))
	if len(broker) != 0 {
		t.Fatalf("unexpected broker messages: %#v", broker)
	}
	if len(events) != 6 {
		t.Fatalf("events len = %d: %#v", len(events), events)
	}
	if events[0].Key.Rune != 'a' || !events[1].Key.Modifiers.Ctrl || events[1].Key.Rune != 'c' {
		t.Fatalf("key parsing failed: %#v", events[:2])
	}
	if events[2].Key.Name != KeyArrowUp || events[3].Key.Name != KeyArrowDown || !events[3].Key.Modifiers.Ctrl {
		t.Fatalf("csi key parsing failed: %#v", events[2:4])
	}
	if events[4].Type != EventFocus || !events[4].Focus.Focused {
		t.Fatalf("focus parsing failed: %#v", events[4])
	}
	if events[5].Type != EventResize || events[5].Resize.Columns != 120 || events[5].Resize.Rows != 40 {
		t.Fatalf("resize parsing failed: %#v", events[5])
	}

	events, broker = p.Feed([]byte("\x1b[?1;2c\x1b[12;24R"))
	if len(events) != 0 || len(broker) != 2 {
		t.Fatalf("query replies should be brokered, events=%#v broker=%#v", events, broker)
	}
}

func TestParserPasteMouseAndEscapeTimeout(t *testing.T) {
	now := time.Unix(20, 0)
	p := NewParser(ParserConfig{Now: func() time.Time { return now }, EscapeTimeout: 10 * time.Millisecond, DoubleClickWindow: time.Second})
	events, _ := p.Feed([]byte("\x1b[200~hello"))
	if len(events) != 0 {
		t.Fatalf("incomplete paste emitted events: %#v", events)
	}
	events, _ = p.Feed([]byte("\nworld\x1b[201~"))
	if len(events) != 1 || events[0].Paste.Text != "hello\nworld" {
		t.Fatalf("paste event mismatch: %#v", events)
	}

	events, _ = p.FeedAt([]byte("\x1b[<0;10;5M"), now)
	if len(events) != 1 || events[0].Mouse.Action != MouseClick || events[0].Mouse.Button != ButtonLeft {
		t.Fatalf("mouse click mismatch: %#v", events)
	}
	events, _ = p.FeedAt([]byte("\x1b[<0;10;5M"), now.Add(100*time.Millisecond))
	if len(events) != 1 || events[0].Mouse.Action != MouseDoubleClick {
		t.Fatalf("double click mismatch: %#v", events)
	}
	events, _ = p.FeedAt([]byte("\x1b[<64;10;5M\x1b[<32;11;5M"), now.Add(2*time.Second))
	if len(events) != 2 || events[0].Mouse.Action != MouseWheel || events[1].Mouse.Action != MouseDrag {
		t.Fatalf("wheel/drag mismatch: %#v", events)
	}

	events, _ = p.FeedAt([]byte("\x1b"), now)
	if len(events) != 0 {
		t.Fatalf("bare escape emitted early: %#v", events)
	}
	events, _ = p.Advance(now.Add(11 * time.Millisecond))
	if len(events) != 1 || events[0].Key.Name != KeyEsc {
		t.Fatalf("escape timeout mismatch: %#v", events)
	}
	events, _ = p.FeedAt([]byte("\x1b["), now)
	if len(events) != 0 {
		t.Fatalf("incomplete csi emitted early: %#v", events)
	}
	events, _ = p.Advance(now.Add(11 * time.Millisecond))
	if len(events) != 1 || events[0].Key.Name != KeyEsc {
		t.Fatalf("incomplete csi timeout mismatch: %#v", events)
	}
}

func FuzzParserDoesNotPanic(f *testing.F) {
	for _, seed := range []string{"abc", "\x1b[A", "\x1b[200~paste\x1b[201~", "\x1b[<0;1;1M", "\x1b[?1;2c"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		p := NewParser(ParserConfig{EscapeTimeout: time.Nanosecond})
		for _, chunk := range chunks([]byte(data), 3) {
			p.Feed(chunk)
		}
		p.Advance(time.Now().Add(time.Second))
	})
}

func chunks(data []byte, n int) [][]byte {
	if len(data) == 0 {
		return nil
	}
	var out [][]byte
	for len(data) > 0 {
		if len(data) < n {
			n = len(data)
		}
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func TestParserAltKey(t *testing.T) {
	p := NewParser(ParserConfig{})
	events, _ := p.Feed([]byte("\x1bb"))
	if len(events) != 1 || events[0].Key.Rune != 'b' || !events[0].Key.Modifiers.Alt || strings.ToLower(events[0].Key.Text) != "b" {
		t.Fatalf("alt key mismatch: %#v", events)
	}
}
