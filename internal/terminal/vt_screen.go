package terminal

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	vtDefaultCols         = 80
	vtDefaultRows         = 24
	vtMaxCols             = 500
	vtMaxRows             = 200
	vtMaxSeqBytes         = 4096
	vtMaxTitleBytes       = 1024
	vtMaxHyperlinkBytes   = 2048
	vtMaxGraphemeBytes    = 128
	vtWideContinuation    = rune(-1)
	vtReplacementGrapheme = "�"
)

type vtScreen struct {
	cols          int
	rows          int
	main          *vtBuffer
	alt           *vtBuffer
	useAlt        bool
	cursorVisible bool
	cursorShape   CursorShape
	title         string
	state         string
	seq           []byte
	utf8Pending   []byte
}

type vtBuffer struct {
	cells            [][]vtCell
	row              int
	col              int
	pendingWrap      bool
	scrollTop        int
	scrollBottom     int
	insertMode       bool
	currentStyle     TerminalStyle
	currentHyperlink string
	savedCursor      vtCursorState
	csiSavedCursor   vtCursorState
}

type vtCell struct {
	cluster      string
	width        int
	style        TerminalStyle
	hyperlink    string
	continuation bool
}

type vtCursorState struct {
	row       int
	col       int
	wrap      bool
	style     TerminalStyle
	hyperlink string
	valid     bool
}

func newVTScreen(size Size) *vtScreen {
	cols, rows := boundedVTSize(size)
	return &vtScreen{cols: cols, rows: rows, main: newVTBuffer(cols, rows), alt: newVTBuffer(cols, rows), cursorVisible: true}
}

func boundedVTSize(size Size) (int, int) {
	cols := int(size.Cols)
	rows := int(size.Rows)
	if cols <= 0 {
		cols = vtDefaultCols
	}
	if rows <= 0 {
		rows = vtDefaultRows
	}
	if cols > vtMaxCols {
		cols = vtMaxCols
	}
	if rows > vtMaxRows {
		rows = vtMaxRows
	}
	return cols, rows
}

func newVTBuffer(cols, rows int) *vtBuffer {
	b := &vtBuffer{cells: make([][]vtCell, rows), scrollBottom: rows - 1}
	for r := range b.cells {
		b.cells[r] = make([]vtCell, cols)
		fillCells(b.cells[r], blankVTCell(TerminalStyle{}, ""))
	}
	return b
}

func (s *vtScreen) active() *vtBuffer {
	if s.useAlt {
		return s.alt
	}
	return s.main
}

func (s *vtScreen) Resize(size Size) {
	cols, rows := boundedVTSize(size)
	if cols == s.cols && rows == s.rows {
		return
	}
	s.cols, s.rows = cols, rows
	s.main = s.main.resized(cols, rows)
	s.alt = s.alt.resized(cols, rows)
}

func (b *vtBuffer) resized(cols, rows int) *vtBuffer {
	n := newVTBuffer(cols, rows)
	for r := 0; r < minInt(len(b.cells), rows); r++ {
		copy(n.cells[r], b.cells[r][:minInt(len(b.cells[r]), cols)])
		sanitizeWideRow(n.cells[r])
	}
	n.row = clampInt(b.row, 0, rows-1)
	n.col = clampInt(b.col, 0, cols-1)
	n.pendingWrap = b.pendingWrap && n.col == cols-1
	n.currentStyle = b.currentStyle
	n.currentHyperlink = b.currentHyperlink
	n.insertMode = b.insertMode
	n.savedCursor = b.savedCursor.clamped(rows, cols)
	n.csiSavedCursor = b.csiSavedCursor.clamped(rows, cols)
	n.scrollBottom = rows - 1
	return n
}

func (c vtCursorState) clamped(rows, cols int) vtCursorState {
	if !c.valid {
		return c
	}
	c.row = clampInt(c.row, 0, rows-1)
	c.col = clampInt(c.col, 0, cols-1)
	return c
}

func (s *vtScreen) Consume(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(s.utf8Pending) > 0 {
		combined := make([]byte, 0, len(s.utf8Pending)+len(data))
		combined = append(combined, s.utf8Pending...)
		combined = append(combined, data...)
		data = combined
		s.utf8Pending = nil
	}
	for len(data) > 0 {
		ch := data[0]
		switch s.state {
		case "esc":
			data = data[1:]
			s.consumeEsc(ch)
		case "esc_hash":
			data = data[1:]
			if ch == '8' {
				s.active().decAlignmentTest()
			}
			s.state = ""
		case "csi":
			data = data[1:]
			s.consumeControlSequence(ch)
		case "osc":
			data = data[1:]
			s.consumeStringSequence(ch, true)
		case "dcs":
			data = data[1:]
			s.consumeStringSequence(ch, false)
		default:
			if ch == 0x1b {
				s.state = "esc"
				s.seq = s.seq[:0]
				data = data[1:]
				continue
			}
			if ch < 0x20 || ch == 0x7f {
				s.consumeControl(ch)
				data = data[1:]
				continue
			}
			if !utf8.FullRune(data) && len(data) <= utf8.UTFMax {
				s.utf8Pending = append(s.utf8Pending[:0], data...)
				return
			}
			r, size := utf8.DecodeRune(data)
			if r == utf8.RuneError && size == 1 {
				r = '�'
			}
			s.active().writeRune(r)
			data = data[size:]
		}
	}
}

func (s *vtScreen) consumeControl(ch byte) {
	switch ch {
	case '\r':
		s.active().carriageReturn()
	case '\n', '\v', '\f':
		s.active().lineFeed()
	case '\b':
		s.active().backspace()
	case '\t':
		s.active().tab()
	}
}

func (s *vtScreen) consumeEsc(ch byte) {
	s.active().resetJoinState()
	switch ch {
	case '[':
		s.state = "csi"
		s.seq = s.seq[:0]
	case ']':
		s.state = "osc"
		s.seq = s.seq[:0]
	case 'P':
		s.state = "dcs"
		s.seq = s.seq[:0]
	case '7':
		s.active().saveCursor(true)
		s.state = ""
	case '8':
		s.active().restoreCursor(true)
		s.state = ""
	case 'D':
		s.active().lineFeed()
		s.state = ""
	case 'M':
		s.active().reverseIndex()
		s.state = ""
	case 'E':
		s.active().lineFeed()
		s.active().carriageReturn()
		s.state = ""
	case 'c':
		s.reset()
	case '#':
		s.state = "esc_hash"
	case 0x1b:
		s.state = "esc"
		s.seq = s.seq[:0]
	default:
		s.state = ""
	}
}

func (s *vtScreen) reset() {
	s.main = newVTBuffer(s.cols, s.rows)
	s.alt = newVTBuffer(s.cols, s.rows)
	s.useAlt = false
	s.cursorVisible = true
	s.cursorShape = CursorShapeDefault
	s.title = ""
	s.state = ""
	s.seq = s.seq[:0]
	s.utf8Pending = nil
}

func (s *vtScreen) consumeControlSequence(ch byte) {
	if ch == 0x1b {
		s.state = "esc"
		s.seq = s.seq[:0]
		return
	}
	if ch >= 0x40 && ch <= 0x7e {
		raw := string(s.seq)
		s.state = ""
		s.seq = s.seq[:0]
		s.handleCSI(raw, ch)
		return
	}
	if len(s.seq) >= vtMaxSeqBytes {
		s.state = ""
		s.seq = s.seq[:0]
		return
	}
	s.seq = append(s.seq, ch)
}

func (s *vtScreen) consumeStringSequence(ch byte, osc bool) {
	if ch == 0x07 {
		raw := string(s.seq)
		s.state = ""
		s.seq = s.seq[:0]
		if osc {
			s.handleOSC(raw)
		}
		return
	}
	if ch == '\\' && len(s.seq) > 0 && s.seq[len(s.seq)-1] == 0x1b {
		raw := string(s.seq[:len(s.seq)-1])
		s.state = ""
		s.seq = s.seq[:0]
		if osc {
			s.handleOSC(raw)
		}
		return
	}
	if len(s.seq) >= vtMaxSeqBytes {
		s.state = ""
		s.seq = s.seq[:0]
		return
	}
	s.seq = append(s.seq, ch)
}

func (s *vtScreen) handleCSI(raw string, final byte) {
	private, paramsRaw, intermediates := splitCSI(raw)
	params := parseCSIParams(paramsRaw)
	b := s.active()
	b.resetJoinState()
	switch final {
	case 'H', 'f':
		b.setCursor(csiParam(params, 0, 1)-1, csiParam(params, 1, 1)-1)
	case 'A':
		b.moveCursor(-csiParam(params, 0, 1), 0)
	case 'B', 'e':
		b.moveCursor(csiParam(params, 0, 1), 0)
	case 'C', 'a':
		b.moveCursor(0, csiParam(params, 0, 1))
	case 'D':
		b.moveCursor(0, -csiParam(params, 0, 1))
	case 'E':
		b.moveCursor(csiParam(params, 0, 1), 0)
		b.carriageReturn()
	case 'F':
		b.moveCursor(-csiParam(params, 0, 1), 0)
		b.carriageReturn()
	case 'G', '`':
		b.setCursor(b.row, csiParam(params, 0, 1)-1)
	case 'd':
		b.setCursor(csiParam(params, 0, 1)-1, b.col)
	case 'J':
		b.eraseDisplay(csiParam(params, 0, 0))
	case 'K':
		b.eraseLine(csiParam(params, 0, 0))
	case 'X':
		b.eraseChars(csiParam(params, 0, 1))
	case 'P':
		b.deleteChars(csiParam(params, 0, 1))
	case '@':
		b.insertChars(csiParam(params, 0, 1))
	case 'L':
		b.insertLines(csiParam(params, 0, 1))
	case 'M':
		b.deleteLines(csiParam(params, 0, 1))
	case 'S':
		b.scrollUp(csiParam(params, 0, 1))
	case 'T':
		b.scrollDown(csiParam(params, 0, 1))
	case 'r':
		b.setScrollRegion(params)
	case 'm':
		b.setSGR(parseSGRParams(paramsRaw))
	case 's':
		b.saveCSICursor()
	case 'u':
		b.restoreCSICursor()
	case 'q':
		if intermediates == " " {
			s.cursorShape = cursorShapeFromParam(csiParam(params, 0, 0))
		}
	case 'h':
		s.handleModeSet(private, params, true)
	case 'l':
		s.handleModeSet(private, params, false)
	}
}

func splitCSI(raw string) (byte, string, string) {
	private := byte(0)
	if raw != "" {
		switch raw[0] {
		case '?', '>', '!', '=':
			private = raw[0]
			raw = raw[1:]
		}
	}
	at := len(raw)
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x20 && raw[i] <= 0x2f {
			at = i
			break
		}
	}
	return private, raw[:at], raw[at:]
}
func parseCSIParams(raw string) []int {
	if raw == "" {
		return nil
	}
	fs := strings.Split(raw, ";")
	ps := make([]int, len(fs))
	for i, f := range fs {
		if j := strings.IndexByte(f, ':'); j >= 0 {
			f = f[:j]
		}
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err == nil && v >= 0 {
			ps[i] = v
		}
	}
	return ps
}
func parseSGRParams(raw string) []int {
	if raw == "" {
		return []int{0}
	}
	ps := parseCSIParams(strings.ReplaceAll(raw, ":", ";"))
	if len(ps) == 0 {
		return []int{0}
	}
	return ps
}
func csiParam(params []int, idx, fallback int) int {
	if idx >= len(params) || params[idx] == 0 {
		return fallback
	}
	return params[idx]
}
func hasCSIParam(params []int, values ...int) bool {
	for _, p := range params {
		for _, v := range values {
			if p == v {
				return true
			}
		}
	}
	return false
}

func (s *vtScreen) handleModeSet(private byte, params []int, enabled bool) {
	b := s.active()
	if private == '?' {
		for _, p := range params {
			switch p {
			case 25:
				s.cursorVisible = enabled
			case 47, 1047:
				if enabled {
					s.alt = newVTBuffer(s.cols, s.rows)
					s.useAlt = true
				} else {
					s.useAlt = false
				}
			case 1048:
				if enabled {
					b.saveCursor(true)
				} else {
					b.restoreCursor(true)
				}
			case 1049:
				if enabled {
					s.main.saveCursor(true)
					s.alt = newVTBuffer(s.cols, s.rows)
					s.useAlt = true
				} else {
					s.useAlt = false
					s.main.restoreCursor(true)
				}
			}
		}
		return
	}
	for _, p := range params {
		if p == 4 {
			b.insertMode = enabled
		}
	}
}

func (s *vtScreen) handleOSC(raw string) {
	parts := strings.SplitN(raw, ";", 2)
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "0", "2":
		if len(parts) == 2 {
			s.title = sanitizeOSCField(parts[1], vtMaxTitleBytes)
		}
	case "8":
		rest := ""
		if len(parts) == 2 {
			rest = parts[1]
		}
		hp := strings.SplitN(rest, ";", 2)
		if len(hp) == 2 {
			s.active().currentHyperlink = sanitizeOSCField(hp[1], vtMaxHyperlinkBytes)
		}
	}
}
func sanitizeOSCField(v string, maxBytes int) string {
	var out strings.Builder
	for _, r := range v {
		if out.Len() >= maxBytes {
			break
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		n := utf8.RuneLen(r)
		if n < 0 || out.Len()+n > maxBytes {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}
func cursorShapeFromParam(p int) CursorShape {
	switch p {
	case 1:
		return CursorShapeBlinkingBlock
	case 2:
		return CursorShapeSteadyBlock
	case 3:
		return CursorShapeBlinkingUnderline
	case 4:
		return CursorShapeSteadyUnderline
	case 5:
		return CursorShapeBlinkingBar
	case 6:
		return CursorShapeSteadyBar
	default:
		return CursorShapeDefault
	}
}

func (b *vtBuffer) writeRune(r rune) {
	if r == 0 {
		return
	}
	if b.shouldJoinRune(r) {
		b.appendToPrevious(r)
		return
	}
	if b.pendingWrap {
		b.lineFeed()
		b.carriageReturn()
	}
	w := runeDisplayWidth(r)
	if w <= 0 {
		b.appendToPrevious(r)
		return
	}
	if w > len(b.cells[0]) {
		w = 1
	}
	if w > 1 && b.col+w > len(b.cells[0]) {
		b.lineFeed()
		b.carriageReturn()
	}
	if b.insertMode {
		b.insertCells(w)
	}
	b.putCluster(string(r), w)
}
func (b *vtBuffer) shouldJoinRune(r rune) bool {
	if isCombiningRune(r) || isVariationSelector(r) || isEmojiModifier(r) || r == '\u200d' {
		return true
	}
	row, col, ok := b.previousCellPosition()
	if !ok {
		return false
	}
	cell := b.cells[row][col]
	if strings.HasSuffix(cell.cluster, "\u200d") {
		return true
	}
	if isRegionalIndicator(r) && utf8.RuneCountInString(cell.cluster) == 1 {
		prev, _ := utf8.DecodeRuneInString(cell.cluster)
		return isRegionalIndicator(prev)
	}
	return false
}
func (b *vtBuffer) appendToPrevious(r rune) {
	row, col, ok := b.previousCellPosition()
	if !ok {
		b.putCluster(vtReplacementGrapheme, 1)
		return
	}
	cell := &b.cells[row][col]
	add := string(r)
	if len(cell.cluster)+len(add) > vtMaxGraphemeBytes {
		cell.cluster = vtReplacementGrapheme
		cell.width = 1
		b.clearContinuationAfter(row, col)
		return
	}
	cell.cluster += add
	if nw := clusterDisplayWidth(cell.cluster); nw > cell.width && col+nw <= len(b.cells[row]) {
		cell.width = nw
		for i := 1; i < nw; i++ {
			b.cells[row][col+i] = continuationVTCell(cell.style, cell.hyperlink)
		}
	}
}
func (b *vtBuffer) putCluster(cluster string, width int) {
	row := b.cells[b.row]
	start := b.col
	end := start + width
	if end > len(row) {
		end = len(row)
		width = end - start
	}
	start, end = normalizeWideRange(row, start, end)
	fillCells(row[start:end], b.blank())
	row[start] = vtCell{cluster: cluster, width: width, style: b.currentStyle, hyperlink: b.currentHyperlink}
	for c := start + 1; c < start+width && c < len(row); c++ {
		row[c] = continuationVTCell(b.currentStyle, b.currentHyperlink)
	}
	if start+width >= len(row) {
		b.col = len(row) - 1
		b.pendingWrap = true
		return
	}
	b.col = start + width
	b.pendingWrap = false
}
func (b *vtBuffer) previousCellPosition() (int, int, bool) {
	row := b.row
	col := b.col
	if b.pendingWrap {
		col = len(b.cells[row]) - 1
	} else if col > 0 {
		col--
	} else {
		return 0, 0, false
	}
	if b.cells[row][col].continuation && col > 0 {
		col--
	}
	if b.cells[row][col].cluster == "" || b.cells[row][col].cluster == " " {
		return 0, 0, false
	}
	return row, col, true
}
func (b *vtBuffer) clearContinuationAfter(row, col int) {
	for i := col + 1; i < len(b.cells[row]) && b.cells[row][i].continuation; i++ {
		b.cells[row][i] = b.blank()
	}
}

func (b *vtBuffer) carriageReturn() { b.col = 0; b.pendingWrap = false; b.resetJoinState() }
func (b *vtBuffer) lineFeed() {
	b.pendingWrap = false
	b.resetJoinState()
	if b.row == b.scrollBottom {
		b.scrollUpRegion(b.scrollTop, b.scrollBottom, 1)
		return
	}
	if b.row < len(b.cells)-1 {
		b.row++
	}
}
func (b *vtBuffer) reverseIndex() {
	b.pendingWrap = false
	b.resetJoinState()
	if b.row == b.scrollTop {
		b.scrollDownRegion(b.scrollTop, b.scrollBottom, 1)
		return
	}
	if b.row > 0 {
		b.row--
	}
}
func (b *vtBuffer) backspace() {
	b.pendingWrap = false
	b.resetJoinState()
	if b.col > 0 {
		b.col--
		if b.cells[b.row][b.col].continuation && b.col > 0 {
			b.col--
		}
	}
}
func (b *vtBuffer) tab() {
	b.pendingWrap = false
	b.resetJoinState()
	n := ((b.col / 8) + 1) * 8
	if n >= len(b.cells[0]) {
		n = len(b.cells[0]) - 1
	}
	b.col = n
}
func (b *vtBuffer) setCursor(row, col int) {
	b.row = clampInt(row, 0, len(b.cells)-1)
	b.col = clampInt(col, 0, len(b.cells[0])-1)
	b.pendingWrap = false
	b.resetJoinState()
}
func (b *vtBuffer) moveCursor(dr, dc int)           { b.setCursor(b.row+dr, b.col+dc) }
func (b *vtBuffer) saveCursor(saveStyle bool)       { b.savedCursor = b.cursorState(saveStyle) }
func (b *vtBuffer) restoreCursor(restoreStyle bool) { b.applyCursorState(b.savedCursor, restoreStyle) }
func (b *vtBuffer) saveCSICursor()                  { b.csiSavedCursor = b.cursorState(false) }
func (b *vtBuffer) restoreCSICursor()               { b.applyCursorState(b.csiSavedCursor, false) }
func (b *vtBuffer) cursorState(saveStyle bool) vtCursorState {
	st := vtCursorState{row: b.row, col: b.col, wrap: b.pendingWrap, valid: true}
	if saveStyle {
		st.style = b.currentStyle
		st.hyperlink = b.currentHyperlink
	}
	return st
}
func (b *vtBuffer) applyCursorState(st vtCursorState, restoreStyle bool) {
	if !st.valid {
		return
	}
	b.row = clampInt(st.row, 0, len(b.cells)-1)
	b.col = clampInt(st.col, 0, len(b.cells[0])-1)
	b.pendingWrap = st.wrap && b.col == len(b.cells[0])-1
	if restoreStyle {
		b.currentStyle = st.style
		b.currentHyperlink = st.hyperlink
	}
	b.resetJoinState()
}

func (b *vtBuffer) eraseDisplay(mode int) {
	b.pendingWrap = false
	b.resetJoinState()
	switch mode {
	case 0:
		for r := b.row; r < len(b.cells); r++ {
			start := 0
			if r == b.row {
				start = wideRangeStart(b.cells[r], b.col)
			}
			fillCells(b.cells[r][start:], b.blank())
		}
	case 1:
		for r := 0; r <= b.row; r++ {
			end := len(b.cells[r])
			if r == b.row {
				end = b.cursorInclusiveEnd(b.cells[r])
			}
			fillCells(b.cells[r][:end], b.blank())
		}
	case 2, 3:
		for r := range b.cells {
			fillCells(b.cells[r], b.blank())
		}
	}
}
func (b *vtBuffer) eraseLine(mode int) {
	b.pendingWrap = false
	b.resetJoinState()
	row := b.cells[b.row]
	switch mode {
	case 0:
		fillCells(row[wideRangeStart(row, b.col):], b.blank())
	case 1:
		fillCells(row[:b.cursorInclusiveEnd(row)], b.blank())
	case 2:
		fillCells(row, b.blank())
	}
}
func (b *vtBuffer) eraseChars(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	if count <= 0 {
		count = 1
	}
	row := b.cells[b.row]
	start, end := normalizeWideRange(row, b.col, b.col+count)
	fillCells(row[start:end], b.blank())
}
func (b *vtBuffer) deleteChars(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	if count <= 0 {
		count = 1
	}
	row := b.cells[b.row]
	if b.col >= len(row) {
		return
	}
	start, end := normalizeWideRange(row, b.col, b.col+count)
	copy(row[start:], row[end:])
	fillCells(row[len(row)-(end-start):], b.blank())
	sanitizeWideRow(row)
}
func (b *vtBuffer) insertChars(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	if count <= 0 {
		count = 1
	}
	b.insertCells(count)
}
func (b *vtBuffer) insertCells(count int) {
	row := b.cells[b.row]
	if b.col >= len(row) {
		return
	}
	start := wideRangeStart(row, b.col)
	count = minInt(count, len(row)-start)
	copy(row[start+count:], row[start:len(row)-count])
	fillCells(row[start:start+count], b.blank())
	sanitizeWideRow(row)
}
func (b *vtBuffer) insertLines(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	if count <= 0 {
		count = 1
	}
	if b.row < b.scrollTop || b.row > b.scrollBottom {
		return
	}
	bottom := b.scrollBottom
	count = minInt(count, bottom-b.row+1)
	for r := bottom; r >= b.row+count; r-- {
		copy(b.cells[r], b.cells[r-count])
	}
	for r := b.row; r < b.row+count; r++ {
		fillCells(b.cells[r], b.blank())
	}
}
func (b *vtBuffer) deleteLines(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	if count <= 0 {
		count = 1
	}
	if b.row < b.scrollTop || b.row > b.scrollBottom {
		return
	}
	bottom := b.scrollBottom
	count = minInt(count, bottom-b.row+1)
	for r := b.row; r+count <= bottom; r++ {
		copy(b.cells[r], b.cells[r+count])
	}
	for r := bottom - count + 1; r <= bottom; r++ {
		fillCells(b.cells[r], b.blank())
	}
}
func (b *vtBuffer) scrollUp(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	b.scrollUpRegion(b.scrollTop, b.scrollBottom, count)
}
func (b *vtBuffer) scrollDown(count int) {
	b.pendingWrap = false
	b.resetJoinState()
	b.scrollDownRegion(b.scrollTop, b.scrollBottom, count)
}
func (b *vtBuffer) scrollUpRegion(top, bottom, count int) {
	if count <= 0 {
		count = 1
	}
	if top < 0 || bottom >= len(b.cells) || top > bottom {
		return
	}
	count = minInt(count, bottom-top+1)
	for r := top; r+count <= bottom; r++ {
		copy(b.cells[r], b.cells[r+count])
	}
	for r := bottom - count + 1; r <= bottom; r++ {
		fillCells(b.cells[r], b.blank())
	}
}
func (b *vtBuffer) scrollDownRegion(top, bottom, count int) {
	if count <= 0 {
		count = 1
	}
	if top < 0 || bottom >= len(b.cells) || top > bottom {
		return
	}
	count = minInt(count, bottom-top+1)
	for r := bottom; r >= top+count; r-- {
		copy(b.cells[r], b.cells[r-count])
	}
	for r := top; r < top+count; r++ {
		fillCells(b.cells[r], b.blank())
	}
}
func (b *vtBuffer) setScrollRegion(params []int) {
	top := csiParam(params, 0, 1) - 1
	bottom := csiParam(params, 1, len(b.cells)) - 1
	if top < 0 || bottom < top || bottom >= len(b.cells) {
		top = 0
		bottom = len(b.cells) - 1
	}
	b.scrollTop = top
	b.scrollBottom = bottom
	b.setCursor(0, 0)
}
func (b *vtBuffer) decAlignmentTest() {
	for r := range b.cells {
		for c := range b.cells[r] {
			b.cells[r][c] = vtCell{cluster: "E", width: 1, style: b.currentStyle, hyperlink: b.currentHyperlink}
		}
	}
	b.setCursor(0, 0)
}

func (b *vtBuffer) setSGR(params []int) {
	if len(params) == 0 {
		params = []int{0}
	}
	for i := 0; i < len(params); i++ {
		p := params[i]
		switch {
		case p == 0:
			b.currentStyle = TerminalStyle{}
		case p == 1:
			b.currentStyle.Attrs |= TerminalAttrBold
		case p == 2:
			b.currentStyle.Attrs |= TerminalAttrDim
		case p == 3:
			b.currentStyle.Attrs |= TerminalAttrItalic
		case p == 4:
			b.currentStyle.Attrs |= TerminalAttrUnderline
		case p == 5:
			b.currentStyle.Attrs |= TerminalAttrBlink
		case p == 7:
			b.currentStyle.Attrs |= TerminalAttrInverse
		case p == 8:
			b.currentStyle.Attrs |= TerminalAttrHidden
		case p == 9:
			b.currentStyle.Attrs |= TerminalAttrStrike
		case p == 22:
			b.currentStyle.Attrs &^= TerminalAttrBold | TerminalAttrDim
		case p == 23:
			b.currentStyle.Attrs &^= TerminalAttrItalic
		case p == 24:
			b.currentStyle.Attrs &^= TerminalAttrUnderline
		case p == 25:
			b.currentStyle.Attrs &^= TerminalAttrBlink
		case p == 27:
			b.currentStyle.Attrs &^= TerminalAttrInverse
		case p == 28:
			b.currentStyle.Attrs &^= TerminalAttrHidden
		case p == 29:
			b.currentStyle.Attrs &^= TerminalAttrStrike
		case p >= 30 && p <= 37:
			b.currentStyle.Foreground = TerminalColor{Mode: TerminalColorPalette, Index: uint8(p - 30)}
		case p == 39:
			b.currentStyle.Foreground = TerminalColor{}
		case p >= 40 && p <= 47:
			b.currentStyle.Background = TerminalColor{Mode: TerminalColorPalette, Index: uint8(p - 40)}
		case p == 49:
			b.currentStyle.Background = TerminalColor{}
		case p >= 90 && p <= 97:
			b.currentStyle.Foreground = TerminalColor{Mode: TerminalColorPalette, Index: uint8(p - 90 + 8)}
		case p >= 100 && p <= 107:
			b.currentStyle.Background = TerminalColor{Mode: TerminalColorPalette, Index: uint8(p - 100 + 8)}
		case p == 38 || p == 48:
			color, used, ok := parseExtendedColor(params[i+1:])
			if ok {
				if p == 38 {
					b.currentStyle.Foreground = color
				} else {
					b.currentStyle.Background = color
				}
				i += used
			}
		}
	}
}
func parseExtendedColor(params []int) (TerminalColor, int, bool) {
	if len(params) < 2 {
		return TerminalColor{}, 0, false
	}
	switch params[0] {
	case 5:
		if params[1] < 0 || params[1] > 255 {
			return TerminalColor{}, 0, false
		}
		return TerminalColor{Mode: TerminalColorPalette, Index: uint8(params[1])}, 2, true
	case 2:
		if len(params) < 4 {
			return TerminalColor{}, 0, false
		}
		return TerminalColor{Mode: TerminalColorRGB, R: uint8(clampInt(params[1], 0, 255)), G: uint8(clampInt(params[2], 0, 255)), B: uint8(clampInt(params[3], 0, 255))}, 4, true
	default:
		return TerminalColor{}, 0, false
	}
}

func (s *vtScreen) Snapshot() TerminalSnapshot {
	b := s.active()
	rows := make([][]TerminalCell, len(b.cells))
	for r := range b.cells {
		rows[r] = make([]TerminalCell, len(b.cells[r]))
		for c, cell := range b.cells[r] {
			rows[r][c] = cell.snapshot()
		}
	}
	return TerminalSnapshot{Size: Size{Cols: uint16(s.cols), Rows: uint16(s.rows)}, Rows: rows, Cursor: TerminalCursor{Row: b.row, Col: b.col, Visible: s.cursorVisible, Shape: s.cursorShape}, AlternateScreen: s.useAlt, ScrollTop: b.scrollTop, ScrollBottom: b.scrollBottom, Title: s.title}
}
func (c vtCell) snapshot() TerminalCell {
	return TerminalCell{Grapheme: c.cluster, Width: c.width, Style: c.style, Hyperlink: c.hyperlink, Continuation: c.continuation}
}

func (s *vtScreen) Repaint() string {
	b := s.active()
	var out strings.Builder
	if s.useAlt {
		out.WriteString("\x1b[?1049h")
	}
	if s.title != "" {
		out.WriteString("\x1b]2;")
		out.WriteString(s.title)
		out.WriteString("\x1b\\")
	}
	out.WriteString("\x1b[?25l\x1b[0m\x1b[H\x1b[2J")
	curStyle := TerminalStyle{}
	curLink := ""
	for r := 0; r < s.rows; r++ {
		if r > 0 {
			out.WriteString("\r\n")
		}
		end := vtRowEnd(b.cells[r])
		for c := 0; c < end; c++ {
			cell := b.cells[r][c]
			if cell.continuation {
				continue
			}
			if curLink != cell.hyperlink {
				writeOSC8(&out, "")
				curLink = ""
				if cell.hyperlink != "" {
					writeOSC8(&out, cell.hyperlink)
					curLink = cell.hyperlink
				}
			}
			if curStyle != cell.style {
				out.WriteString(sgrForStyle(cell.style))
				curStyle = cell.style
			}
			if cell.cluster == "" {
				out.WriteByte(' ')
			} else {
				out.WriteString(cell.cluster)
			}
		}
	}
	if curLink != "" {
		writeOSC8(&out, "")
	}
	out.WriteString("\x1b[0m")
	out.WriteString(fmt.Sprintf("\x1b[%d;%dH", b.row+1, clampInt(b.col, 0, s.cols-1)+1))
	if seq := cursorShapeSequence(s.cursorShape); seq != "" {
		out.WriteString(seq)
	}
	if s.cursorVisible {
		out.WriteString("\x1b[?25h")
	} else {
		out.WriteString("\x1b[?25l")
	}
	return out.String()
}
func sgrForStyle(st TerminalStyle) string {
	if st == (TerminalStyle{}) {
		return "\x1b[0m"
	}
	ps := []string{"0"}
	if st.Attrs&TerminalAttrBold != 0 {
		ps = append(ps, "1")
	}
	if st.Attrs&TerminalAttrDim != 0 {
		ps = append(ps, "2")
	}
	if st.Attrs&TerminalAttrItalic != 0 {
		ps = append(ps, "3")
	}
	if st.Attrs&TerminalAttrUnderline != 0 {
		ps = append(ps, "4")
	}
	if st.Attrs&TerminalAttrBlink != 0 {
		ps = append(ps, "5")
	}
	if st.Attrs&TerminalAttrInverse != 0 {
		ps = append(ps, "7")
	}
	if st.Attrs&TerminalAttrHidden != 0 {
		ps = append(ps, "8")
	}
	if st.Attrs&TerminalAttrStrike != 0 {
		ps = append(ps, "9")
	}
	appendColor := func(prefix int, color TerminalColor) {
		switch color.Mode {
		case TerminalColorPalette:
			idx := int(color.Index)
			if idx < 8 {
				ps = append(ps, strconv.Itoa(prefix+idx))
			} else if idx < 16 {
				ps = append(ps, strconv.Itoa(prefix+60+idx-8))
			} else {
				ps = append(ps, strconv.Itoa(prefix+8), "5", strconv.Itoa(idx))
			}
		case TerminalColorRGB:
			ps = append(ps, strconv.Itoa(prefix+8), "2", strconv.Itoa(int(color.R)), strconv.Itoa(int(color.G)), strconv.Itoa(int(color.B)))
		}
	}
	appendColor(30, st.Foreground)
	appendColor(40, st.Background)
	return "\x1b[" + strings.Join(ps, ";") + "m"
}
func cursorShapeSequence(shape CursorShape) string {
	if shape == CursorShapeDefault {
		return ""
	}
	return fmt.Sprintf("\x1b[%d q", int(shape))
}
func writeOSC8(out *strings.Builder, uri string) {
	out.WriteString("\x1b]8;;")
	out.WriteString(uri)
	out.WriteString("\x1b\\")
}

func (b *vtBuffer) blank() vtCell { return blankVTCell(b.currentStyle, b.currentHyperlink) }
func blankVTCell(style TerminalStyle, hyperlink string) vtCell {
	return vtCell{cluster: " ", width: 1, style: style, hyperlink: hyperlink}
}
func continuationVTCell(style TerminalStyle, hyperlink string) vtCell {
	return vtCell{width: 0, style: style, hyperlink: hyperlink, continuation: true}
}
func fillCells(cells []vtCell, cell vtCell) {
	for i := range cells {
		cells[i] = cell
	}
}
func (b *vtBuffer) resetJoinState() {}
func (b *vtBuffer) cursorInclusiveEnd(row []vtCell) int {
	if b.col >= len(row) {
		return len(row)
	}
	_, end := normalizeWideRange(row, b.col, b.col+1)
	return end
}
func wideRangeStart(row []vtCell, col int) int {
	start, _ := normalizeWideRange(row, col, len(row))
	return start
}
func normalizeWideRange(row []vtCell, start, end int) (int, int) {
	start = clampInt(start, 0, len(row))
	end = clampInt(end, start, len(row))
	if start < len(row) && row[start].continuation && start > 0 {
		start--
	}
	if end < len(row) && end > 0 && row[end].continuation {
		end++
	}
	return start, end
}
func sanitizeWideRow(row []vtCell) {
	for c := 0; c < len(row); c++ {
		cell := row[c]
		switch {
		case cell.continuation:
			if c == 0 || row[c-1].width < 2 || !coversContinuation(row, c-1, c) {
				row[c] = blankVTCell(TerminalStyle{}, "")
			}
		case cell.width > 1:
			ok := true
			for i := 1; i < cell.width; i++ {
				if c+i >= len(row) || !row[c+i].continuation {
					ok = false
					break
				}
			}
			if !ok {
				row[c] = blankVTCell(TerminalStyle{}, "")
				continue
			}
			c += cell.width - 1
		}
	}
}
func coversContinuation(row []vtCell, start, cont int) bool {
	cell := row[start]
	return !cell.continuation && cell.width > cont-start && cont < len(row)
}
func vtRowEnd(row []vtCell) int {
	end := len(row)
	for end > 0 && isDefaultBlankCell(row[end-1]) {
		end--
	}
	return end
}
func isDefaultBlankCell(c vtCell) bool {
	return !c.continuation && c.cluster == " " && c.width == 1 && c.style == (TerminalStyle{}) && c.hyperlink == ""
}
func vtRowString(row []vtCell) string {
	end := vtRowEnd(row)
	var out strings.Builder
	for _, c := range row[:end] {
		if !c.continuation {
			out.WriteString(c.cluster)
		}
	}
	return out.String()
}
func clusterDisplayWidth(cluster string) int {
	w := 0
	for _, r := range cluster {
		if isCombiningRune(r) || isVariationSelector(r) || isEmojiModifier(r) || r == '\u200d' {
			continue
		}
		if rw := runeDisplayWidth(r); rw > w {
			w = rw
		}
	}
	if strings.ContainsRune(cluster, '\u200d') && w < 2 {
		return 2
	}
	if countRegionalIndicators(cluster) >= 2 {
		return 2
	}
	if w == 0 {
		return 1
	}
	return w
}
func countRegionalIndicators(s string) int {
	n := 0
	for _, r := range s {
		if isRegionalIndicator(r) {
			n++
		}
	}
	return n
}
func runeDisplayWidth(r rune) int {
	if r == 0 || isCombiningRune(r) || isVariationSelector(r) || isEmojiModifier(r) || r == '\u200d' {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}
func isCombiningRune(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r)
}
func isVariationSelector(r rune) bool {
	return (r >= 0xfe00 && r <= 0xfe0f) || (r >= 0xe0100 && r <= 0xe01ef)
}
func isEmojiModifier(r rune) bool     { return r >= 0x1f3fb && r <= 0x1f3ff }
func isRegionalIndicator(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f:
		return true
	case r >= 0x2329 && r <= 0x232a:
		return true
	case r >= 0x2e80 && r <= 0xa4cf && r != 0x303f:
		return true
	case r >= 0xac00 && r <= 0xd7a3:
		return true
	case r >= 0xf900 && r <= 0xfaff:
		return true
	case r >= 0xfe10 && r <= 0xfe19:
		return true
	case r >= 0xfe30 && r <= 0xfe6f:
		return true
	case r >= 0xff00 && r <= 0xff60:
		return true
	case r >= 0xffe0 && r <= 0xffe6:
		return true
	case r >= 0x1f1e6 && r <= 0x1f1ff:
		return false
	case r >= 0x1f300 && r <= 0x1f64f:
		return true
	case r >= 0x1f680 && r <= 0x1f6ff:
		return true
	case r >= 0x1f900 && r <= 0x1f9ff:
		return true
	case r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}
func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
