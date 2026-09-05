package render

import (
	"regexp"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

var markdownTokenRE = regexp.MustCompile(`(?m)(^#{1,6}\s*|[*_` + "`" + `>]+|\[([^\]]+)\]\(([^)]+)\))`)

func sanitize(text string) string {
	text = stripANSI(text)
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '\n', '\t':
			b.WriteRune(r)
		case '\r':
			b.WriteRune('\n')
		default:
			if r == utf8RuneError() || unicode.IsControl(r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func utf8RuneError() rune { return '\uFFFD' }

func stripANSI(s string) string {
	var b strings.Builder
	state := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch state {
		case 0:
			if c == 0x1b {
				state = 1
				continue
			}
			b.WriteByte(c)
		case 1:
			switch c {
			case '[':
				state = 2
			case ']':
				state = 3
			case 'P', '^', '_':
				state = 4
			default:
				state = 0
			}
		case 2:
			if c >= 0x40 && c <= 0x7e {
				state = 0
			}
		case 3:
			if c == 0x07 {
				state = 0
			} else if c == 0x1b {
				state = 5
			}
		case 4:
			if c == 0x1b {
				state = 5
			}
		case 5:
			if c == '\\' {
				state = 0
			} else if c == 0x1b {
				state = 5
			} else {
				state = 4
			}
		}
	}
	return b.String()
}

func renderMarkdownText(text string) string {
	text = markdownTokenRE.ReplaceAllStringFunc(text, func(token string) string {
		if strings.HasPrefix(token, "[") && strings.Contains(token, "](") {
			close := strings.Index(token, "](")
			return token[1:close]
		}
		return ""
	})
	return text
}

func wrapBlock(text string, width int, unicodeMode bool) string {
	text = sanitize(text)
	if width <= 0 {
		return text
	}
	var lines []string
	for _, original := range strings.Split(text, "\n") {
		words := strings.Fields(original)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				for lipgloss.Width(word) > width {
					prefix, rest := splitWidth(word, width)
					lines = append(lines, prefix)
					word = rest
				}
				line = word
				continue
			}
			if lipgloss.Width(line)+1+lipgloss.Width(word) <= width {
				line += " " + word
				continue
			}
			lines = append(lines, fitLine(line, width, unicodeMode))
			line = ""
			for lipgloss.Width(word) > width {
				prefix, rest := splitWidth(word, width)
				lines = append(lines, prefix)
				word = rest
			}
			line = word
		}
		if line != "" {
			lines = append(lines, fitLine(line, width, unicodeMode))
		}
	}
	return strings.Join(lines, "\n")
}

func fitBlock(text string, width, height int, unicodeMode bool) string {
	if width <= 0 {
		width = DefaultWidth
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for lipgloss.Width(stripANSI(line)) > width {
			prefix, rest := splitWidth(line, width-1)
			out = append(out, prefix+ellipsis(unicodeMode))
			line = rest
		}
		out = append(out, fitLine(line, width, unicodeMode))
	}
	if height > 0 && len(out) > height {
		out = out[:height]
		if height > 0 {
			out[height-1] = fitLine(strings.TrimRight(out[height-1], " ")+ellipsis(unicodeMode), width, unicodeMode)
		}
	}
	return strings.Join(out, "\n")
}

func fitLine(line string, width int, unicodeMode bool) string {
	line = stripUnsafeControls(line)
	if width <= 0 {
		return line
	}
	if lipgloss.Width(stripANSI(line)) <= width {
		return line
	}
	prefix, _ := splitWidth(stripANSI(line), maxInt(0, width-lipgloss.Width(ellipsis(unicodeMode))))
	return prefix + ellipsis(unicodeMode)
}

func stripUnsafeControls(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == 0x1b {
			b.WriteByte(c)
			continue
		}
		if c < 0x20 && c != '\n' && c != '\t' && c != '\r' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func splitWidth(s string, width int) (string, string) {
	if width <= 0 {
		return "", s
	}
	var b strings.Builder
	used := 0
	for idx, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width {
			return b.String(), s[idx:]
		}
		b.WriteRune(r)
		used += w
	}
	return b.String(), ""
}

func oneLine(text string, width int) string {
	line := strings.ReplaceAll(strings.TrimSpace(stripANSI(text)), "\n", " ")
	return fitLine(line, width, true)
}

func padRight(text string, width int) string {
	visible := lipgloss.Width(stripANSI(text))
	if visible >= width {
		return text
	}
	return text + strings.Repeat(" ", width-visible)
}

func prefixFirstLine(prefix, text string) string {
	if text == "" {
		return prefix
	}
	lines := strings.Split(text, "\n")
	lines[0] = prefix + lines[0]
	return strings.Join(lines, "\n")
}

func indent(text, prefix string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return "\n" + strings.Join(lines, "\n")
}

func joinColumns(left, right string, leftWidth, rightWidth int, sep string) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	height := maxInt(len(leftLines), len(rightLines))
	var out []string
	for i := 0; i < height; i++ {
		l, r := "", ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		out = append(out, padRight(fitLine(l, leftWidth, true), leftWidth)+sep+fitLine(r, rightWidth, true))
	}
	return strings.Join(out, "\n")
}

func ellipsis(unicodeMode bool) string {
	if unicodeMode {
		return "…"
	}
	return "."
}

func asciiSymbol(symbol string) string {
	switch symbol {
	case "•", "›":
		return "-"
	case "├─":
		return "+-"
	case "◷":
		return "o"
	case "│":
		return "|"
	case "□":
		return "[]"
	default:
		return symbol
	}
}

func sanitizeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, sanitize(value))
	}
	return out
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func roleFromProps(props propMap) string {
	for _, key := range []string{"severity", "variant", "role", "status"} {
		if value := props.String(key); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(value, lo, hi int) int {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}

func clampFloat(value, lo, hi float64) float64 {
	if value < lo {
		return lo
	}
	if value > hi {
		return hi
	}
	return value
}
