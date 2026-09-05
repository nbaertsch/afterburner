package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestScreenMirrorStyledGraphemeSnapshotAndRepaint(t *testing.T) {
	surface := NewScreenMirror(Size{Cols: 12, Rows: 3})
	surface.Consume([]byte("\x1b]2;prod title\a\x1b[1;31me\u0301\x1b[0m \x1b]8;;https://example.test\aL\x1b]8;;\a 👩‍💻"))

	snap := surface.Snapshot()
	if snap.Title != "prod title" {
		t.Fatalf("title = %q", snap.Title)
	}
	accent := snap.Rows[0][0]
	if accent.Grapheme != "e\u0301" || accent.Width != 1 || accent.Style.Foreground.Index != 1 || accent.Style.Attrs&TerminalAttrBold == 0 {
		t.Fatalf("accent cell = %#v", accent)
	}
	link := snap.Rows[0][2]
	if link.Grapheme != "L" || link.Hyperlink != "https://example.test" {
		t.Fatalf("hyperlink cell = %#v", link)
	}
	emoji := snap.Rows[0][4]
	if emoji.Grapheme != "👩‍💻" || emoji.Width != 2 || !snap.Rows[0][5].Continuation {
		t.Fatalf("emoji cells = %#v %#v", emoji, snap.Rows[0][5])
	}
	repaint := surface.Repaint()
	for _, want := range []string{"\x1b]2;prod title\x1b\\", "\x1b[0;1;31m", "e\u0301", "\x1b]8;;https://example.test\x1b\\", "👩‍💻"} {
		if !strings.Contains(repaint, want) {
			t.Fatalf("repaint missing %q: %q", want, repaint)
		}
	}
}

func TestVTScreenAltScreenCursorAndModes(t *testing.T) {
	screen := newVTScreen(Size{Cols: 10, Rows: 3})
	screen.Consume([]byte("main\x1b[2;3H\x1b[?25l\x1b[5 q\x1b[?1049halt"))
	alt := screen.Snapshot()
	if !alt.AlternateScreen || vtRowString(screen.active().cells[0]) != "alt" {
		t.Fatalf("alt snapshot = %#v row=%q", alt, vtRowString(screen.active().cells[0]))
	}
	if alt.Cursor.Visible || alt.Cursor.Shape != CursorShapeBlinkingBar {
		t.Fatalf("cursor = %#v", alt.Cursor)
	}
	screen.Consume([]byte("\x1b[?1049l"))
	snap := screen.Snapshot()
	if snap.AlternateScreen || vtRowString(screen.active().cells[0]) != "main" || snap.Cursor.Row != 1 || snap.Cursor.Col != 2 {
		t.Fatalf("main restore snapshot = %#v row=%q", snap, vtRowString(screen.active().cells[0]))
	}
}

func TestVTScreenScrollRegionInsertDeleteEraseVariants(t *testing.T) {
	screen := newVTScreen(Size{Cols: 5, Rows: 5})
	screen.Consume([]byte("11111\r\n22222\r\n33333\r\n44444\r\n55555"))
	screen.Consume([]byte("\x1b[2;4r\x1b[4;1H\n"))
	if got := rowsText(screen); got[0] != "11111" || got[1] != "33333" || got[2] != "44444" || got[3] != "" || got[4] != "55555" {
		t.Fatalf("after region scroll = %#v", got)
	}
	screen.Consume([]byte("\x1b[r\x1b[2;1H\x1b[Labc\x1b[2;2H\x1b[1@Z\x1b[2;4H\x1b[1P\x1b[3X"))
	got := rowsText(screen)
	if !strings.HasPrefix(got[1], "aZb") {
		t.Fatalf("insert/delete/erase row = %#v", got)
	}
}

func TestTerminalSurfaceInvalidSequenceRecoveryAndPartialUTF8(t *testing.T) {
	screen := newVTScreen(Size{Cols: 8, Rows: 2})
	screen.Consume([]byte("A"))
	screen.Consume([]byte{0xe7, 0x95})
	screen.Consume([]byte{0x8c})
	if got := vtRowString(screen.active().cells[0]); got != "A界" {
		t.Fatalf("partial UTF-8 was not preserved: %q", got)
	}
	screen.Consume([]byte("\x1b[" + strings.Repeat("1", vtMaxSeqBytes+10) + "\x1b[HOK"))
	if repaint := screen.Repaint(); !strings.Contains(repaint, "OK") {
		t.Fatalf("screen did not recover after malformed output: %q", repaint)
	}
}

func TestTerminalModalRendererRestoresAltScreenModeSemantically(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 10, Rows: 3})
	renderer.ShowModal(ModalFrame{Title: "Modal"})
	renderer.WriteCopilotOutput([]byte("\x1b[?1049halt"))
	renderer.HideModal()
	if text := output.String(); !strings.Contains(text, "\x1b[?1049h") || !strings.Contains(text, "alt") {
		t.Fatalf("alt screen was not restored semantically: %q", text)
	}

	output.Reset()
	renderer.WriteCopilotOutput([]byte("\x1b[?1049hvisible-alt"))
	renderer.ShowModal(ModalFrame{Title: "Modal"})
	renderer.WriteCopilotOutput([]byte("\x1b[?1049lmain-now"))
	renderer.HideModal()
	if text := output.String(); !strings.Contains(text, "\x1b[?1049l") || !strings.Contains(text, "main-now") {
		t.Fatalf("main screen exit was not restored semantically: %q", text)
	}
}

func TestTerminalModalRendererSemanticRestoreAfterResizeAndCrashCloseAll(t *testing.T) {
	var output bytes.Buffer
	renderer := NewTerminalModalRendererWithSize(&output, Size{Cols: 10, Rows: 3})
	renderer.WriteCopilotOutput([]byte("old\rcurrent"))
	renderer.ShowModal(ModalFrame{Title: "Modal", Body: "body"})
	renderer.WriteCopilotOutput([]byte("\rhidden raw\rfinal"))
	renderer.Resize(Size{Cols: 6, Rows: 3})
	renderer.HideModal()
	text := output.String()
	if strings.Contains(text, "hidden raw") || !strings.Contains(text, "final") || strings.Count(text, "Modal") < 2 {
		t.Fatalf("semantic restore failed: %q", text)
	}

	broker := NewBroker(&fakeBackend{process: &fakeProcess{}}, BrokerOptions{})
	if err := broker.Start(t.Context(), Command{Path: "copilot"}); err != nil {
		t.Fatal(err)
	}
	server, err := NewModalServer(broker, renderer)
	if err != nil {
		t.Fatal(err)
	}
	registerLegacyModalForTest(t, server)
	response := callModalServer(t, server, map[string]any{"type": "open", "id": "black-box"})
	if !response.OK {
		t.Fatalf("open = %#v", response)
	}
	renderer.WriteCopilotOutput([]byte("\rcrash-current"))
	server.CloseAll()
	crashText := output.String()
	if !strings.Contains(crashText, "crash-") || !strings.Contains(crashText, "curren") {
		t.Fatalf("crash close did not repaint current screen: %q", crashText)
	}
}

func TestTerminalSurfaceContractCompatibility(t *testing.T) {
	var _ TerminalSurface = NewScreenMirror(Size{Cols: 1, Rows: 1})
	var _ OutputHandler = NewTerminalModalRendererWithSize(&bytes.Buffer{}, Size{Cols: 1, Rows: 1})
}

func rowsText(screen *vtScreen) []string {
	rows := make([]string, len(screen.active().cells))
	for i := range rows {
		rows[i] = vtRowString(screen.active().cells[i])
	}
	return rows
}

func FuzzVTScreenMalformedChildOutput(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("plain"),
		[]byte("\x1b[31mred"),
		[]byte("\x1b]8;;https://example\x1b\\link"),
		[]byte{0xff, 0xfe, 0x1b, '[', '?', '2'},
		[]byte("👩‍💻界e\u0301"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		screen := newVTScreen(Size{Cols: 20, Rows: 5})
		screen.Consume(data)
		screen.Resize(Size{Cols: 12, Rows: 4})
		_ = screen.Snapshot()
		if repaint := screen.Repaint(); len(repaint) > 64*1024 {
			t.Fatalf("repaint too large: %d", len(repaint))
		}
	})
}
