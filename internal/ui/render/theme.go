package render

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/nbaertsch/afterburner/internal/ui/style"
)

type Theme struct {
	ID     string
	Name   string
	Mode   style.ThemeMode
	Tokens map[style.TokenID]string
}

func DefaultTheme() Theme {
	return Theme{
		ID:   "afterburner.dark",
		Name: "Afterburner Dark",
		Mode: style.ThemeModeDark,
		Tokens: map[style.TokenID]string{
			style.ColorBackground:       "#0d1117",
			style.ColorBackgroundRaised: "#161b22",
			style.ColorForeground:       "#e6edf3",
			style.ColorForegroundMuted:  "#8b949e",
			style.ColorAccent:           "#2f81f7",
			style.ColorAccentForeground: "#ffffff",
			style.ColorSuccess:          "#3fb950",
			style.ColorWarning:          "#d29922",
			style.ColorDanger:           "#f85149",
			style.ColorInfo:             "#58a6ff",
			style.ColorBorder:           "#30363d",
			style.ColorFocusRing:        "#79c0ff",
			style.ColorSelection:        "#264f78",
		},
	}
}

func HighContrastTheme() Theme {
	base := DefaultTheme()
	base.ID = "afterburner.highContrast"
	base.Name = "Afterburner High Contrast"
	base.Mode = style.ThemeModeHighContrast
	base.Tokens[style.ColorBackground] = "#000000"
	base.Tokens[style.ColorBackgroundRaised] = "#000000"
	base.Tokens[style.ColorForeground] = "#ffffff"
	base.Tokens[style.ColorForegroundMuted] = "#ffffff"
	base.Tokens[style.ColorAccent] = "#ffff00"
	base.Tokens[style.ColorAccentForeground] = "#000000"
	base.Tokens[style.ColorSuccess] = "#00ff00"
	base.Tokens[style.ColorWarning] = "#ffff00"
	base.Tokens[style.ColorDanger] = "#ff0000"
	base.Tokens[style.ColorInfo] = "#00ffff"
	base.Tokens[style.ColorBorder] = "#ffffff"
	base.Tokens[style.ColorFocusRing] = "#ffff00"
	base.Tokens[style.ColorSelection] = "#ffffff"
	return base
}

func LightTheme() Theme {
	base := DefaultTheme()
	base.ID = "afterburner.light"
	base.Name = "Afterburner Light"
	base.Mode = style.ThemeModeLight
	base.Tokens[style.ColorBackground] = "#ffffff"
	base.Tokens[style.ColorBackgroundRaised] = "#f6f8fa"
	base.Tokens[style.ColorForeground] = "#24292f"
	base.Tokens[style.ColorForegroundMuted] = "#57606a"
	base.Tokens[style.ColorBorder] = "#d0d7de"
	base.Tokens[style.ColorSelection] = "#ddf4ff"
	return base
}

func ThemeFromContract(theme style.Theme) Theme {
	out := DefaultTheme()
	if theme.ID != "" {
		out.ID = theme.ID
	}
	if theme.DisplayName != "" {
		out.Name = theme.DisplayName
	}
	if theme.Mode != "" {
		out.Mode = theme.Mode
	}
	if len(theme.Tokens) > 0 {
		out.Tokens = cloneTokens(out.Tokens)
		for _, token := range theme.Tokens {
			out.Tokens[token.ID] = token.Value
		}
	}
	return out
}

func cloneTokens(in map[style.TokenID]string) map[style.TokenID]string {
	out := make(map[style.TokenID]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (t Theme) token(id style.TokenID) string {
	if t.Tokens == nil {
		return DefaultTheme().Tokens[id]
	}
	if value := t.Tokens[id]; value != "" {
		return value
	}
	return DefaultTheme().Tokens[id]
}

func (t Theme) styleFor(role string, mode ColorMode) lipgloss.Style {
	if mode == ColorModeMono {
		return lipgloss.NewStyle()
	}
	if mode == ColorModeHighContrast || t.Mode == style.ThemeModeHighContrast {
		t = HighContrastTheme()
	}
	fg := t.token(style.ColorForeground)
	s := lipgloss.NewStyle()
	switch strings.ToLower(role) {
	case "muted", "disabled", "empty":
		fg = t.token(style.ColorForegroundMuted)
	case "accent", "focus", "selected", "button", "link":
		fg = t.token(style.ColorAccent)
	case "success", "checked":
		fg = t.token(style.ColorSuccess)
	case "warning":
		fg = t.token(style.ColorWarning)
	case "danger", "error":
		fg = t.token(style.ColorDanger)
	case "info", "loading":
		fg = t.token(style.ColorInfo)
	case "badge":
		s = s.Background(colorForMode(t.token(style.ColorAccent), mode)).Foreground(colorForMode(t.token(style.ColorAccentForeground), mode))
		return s
	}
	return s.Foreground(colorForMode(fg, mode))
}

func colorForMode(hex string, mode ColorMode) color.Color {
	switch mode {
	case ColorModeMono:
		return lipgloss.Color("")
	case ColorModeANSI16:
		return lipgloss.Color(nearestANSI16(hex))
	case ColorModeANSI256:
		return lipgloss.Color(nearestANSI256(hex))
	case ColorModeHighContrast:
		return lipgloss.Color(hex)
	default:
		return lipgloss.Color(hex)
	}
}

func nearestANSI16(hex string) string {
	switch strings.ToLower(hex) {
	case "#f85149", "#ff0000":
		return "1"
	case "#3fb950", "#00ff00":
		return "2"
	case "#d29922", "#ffff00":
		return "3"
	case "#2f81f7", "#58a6ff", "#00ffff":
		return "4"
	case "#ffffff", "#e6edf3":
		return "15"
	case "#8b949e", "#57606a", "#d0d7de", "#30363d":
		return "8"
	default:
		return "7"
	}
}

func nearestANSI256(hex string) string {
	switch strings.ToLower(hex) {
	case "#0d1117", "#000000":
		return "16"
	case "#161b22":
		return "234"
	case "#e6edf3", "#ffffff":
		return "15"
	case "#8b949e", "#57606a", "#d0d7de", "#30363d":
		return "245"
	case "#2f81f7", "#58a6ff":
		return "39"
	case "#3fb950", "#00ff00":
		return "40"
	case "#d29922", "#ffff00":
		return "220"
	case "#f85149", "#ff0000":
		return "203"
	case "#00ffff":
		return "51"
	default:
		return "7"
	}
}
