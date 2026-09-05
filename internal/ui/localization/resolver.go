package localization

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type LocalizedText struct {
	Locale    Locale    `json:"locale"`
	Direction Direction `json:"direction"`
	Text      string    `json:"text"`
	MessageID MessageID `json:"messageId,omitempty"`
	Fallback  bool      `json:"fallback,omitempty"`
}

type Resolver struct {
	bundles       map[Locale]Bundle
	defaultLocale Locale
}

type ResolverOption func(*Resolver)

func WithDefaultLocale(locale Locale) ResolverOption {
	return func(r *Resolver) { r.defaultLocale = locale }
}

func NewResolver(bundles []Bundle, opts ...ResolverOption) *Resolver {
	r := &Resolver{bundles: map[Locale]Bundle{}, defaultLocale: "en"}
	for _, opt := range opts {
		opt(r)
	}
	for _, bundle := range bundles {
		if bundle.Direction == "" {
			bundle.Direction = DirectionForLocale(bundle.Locale)
		}
		r.bundles[bundle.Locale] = bundle
		if r.defaultLocale == "" {
			r.defaultLocale = bundle.Locale
		}
	}
	return r
}

func (r *Resolver) Resolve(locale Locale, ref Reference) LocalizedText {
	if ref.MessageID == "" {
		text := Interpolate(ref.Fallback, stringMapToAny(ref.Values))
		return LocalizedText{Locale: locale, Direction: DirectionForLocale(locale), Text: BidiIsolate(text, DirectionForLocale(locale)), Fallback: true}
	}
	for _, candidate := range r.localeChain(locale) {
		bundle, ok := r.bundles[candidate]
		if !ok {
			continue
		}
		if text, ok := bundle.Messages[ref.MessageID]; ok {
			values := stringMapToAny(ref.Values)
			resolved := ResolvePlural(Interpolate(text.Value, values), values)
			dir := bundle.Direction
			if dir == "" {
				dir = DirectionForLocale(candidate)
			}
			return LocalizedText{Locale: candidate, Direction: dir, Text: BidiIsolate(resolved, dir), MessageID: ref.MessageID, Fallback: candidate != locale}
		}
		if bundle.Fallback != "" {
			if fallback, ok := r.bundles[bundle.Fallback]; ok {
				if text, ok := fallback.Messages[ref.MessageID]; ok {
					values := stringMapToAny(ref.Values)
					resolved := ResolvePlural(Interpolate(text.Value, values), values)
					dir := fallback.Direction
					if dir == "" {
						dir = DirectionForLocale(bundle.Fallback)
					}
					return LocalizedText{Locale: bundle.Fallback, Direction: dir, Text: BidiIsolate(resolved, dir), MessageID: ref.MessageID, Fallback: true}
				}
			}
		}
	}
	text := ref.Fallback
	if text == "" {
		text = string(ref.MessageID)
	}
	dir := DirectionForLocale(locale)
	return LocalizedText{Locale: locale, Direction: dir, Text: BidiIsolate(ResolvePlural(Interpolate(text, stringMapToAny(ref.Values)), stringMapToAny(ref.Values)), dir), MessageID: ref.MessageID, Fallback: true}
}

func (r *Resolver) localeChain(locale Locale) []Locale {
	var out []Locale
	add := func(l Locale) {
		if l == "" {
			return
		}
		for _, existing := range out {
			if existing == l {
				return
			}
		}
		out = append(out, l)
	}
	add(locale)
	if idx := strings.IndexAny(string(locale), "-_"); idx > 0 {
		add(Locale(string(locale)[:idx]))
	}
	add(r.defaultLocale)
	add("en")
	return out
}

func Interpolate(template string, values map[string]any) string {
	out := template
	for key, value := range values {
		out = strings.ReplaceAll(out, "{"+key+"}", formatAny(value))
	}
	return out
}

func ResolvePlural(template string, values map[string]any) string {
	start := strings.Index(template, "{count, plural,")
	if start < 0 {
		return template
	}
	end := findMatchingBrace(template, start)
	if end < 0 {
		return template
	}
	body := strings.TrimSpace(strings.TrimPrefix(template[start+1:end], "count, plural,"))
	count := numberValue(values["count"])
	category := PluralCategory(count)
	selected := pluralOption(body, category)
	if selected == "" {
		selected = pluralOption(body, "other")
	}
	selected = strings.ReplaceAll(selected, "#", FormatNumber(count, "en"))
	return template[:start] + selected + template[end+1:]
}

func PluralCategory(n float64) string {
	if math.Abs(n) == 1 {
		return "one"
	}
	return "other"
}

func FormatNumber(value any, locale Locale) string {
	n := numberValue(value)
	text := strconv.FormatFloat(n, 'f', -1, 64)
	if strings.HasPrefix(string(locale), "fr") || strings.HasPrefix(string(locale), "de") {
		text = strings.ReplaceAll(text, ".", ",")
	}
	return text
}

func FormatDate(t time.Time, locale Locale) string {
	if strings.HasPrefix(string(locale), "en-US") || locale == "en" || locale == "" {
		return t.Format("Jan 2, 2006")
	}
	return t.Format("2006-01-02")
}

func FormatDuration(d time.Duration, locale Locale) string {
	if d < 0 {
		return "-" + FormatDuration(-d, locale)
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int64(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int64(d.Minutes()), int64(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int64(d.Hours()), int64(d.Minutes())%60)
}

func FormatBytes(bytes int64, locale Locale) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(bytes)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unit])
	}
	return FormatNumber(math.Round(value*10)/10, locale) + " " + units[unit]
}

func DirectionForLocale(locale Locale) Direction {
	lang := strings.ToLower(string(locale))
	if idx := strings.IndexAny(lang, "-_"); idx > 0 {
		lang = lang[:idx]
	}
	switch lang {
	case "ar", "fa", "he", "iw", "ur", "ps", "dv", "yi":
		return DirectionRTL
	case "":
		return DirectionLTR
	default:
		return DirectionLTR
	}
}

func BidiIsolate(text string, direction Direction) string {
	if text == "" || direction == DirectionAuto {
		return text
	}
	if strings.HasPrefix(text, "\u2066") || strings.HasPrefix(text, "\u2067") {
		return text
	}
	if direction == DirectionRTL {
		return "\u2067" + text + "\u2069"
	}
	return "\u2066" + text + "\u2069"
}

func StripBidiIsolates(text string) string {
	return strings.NewReplacer("\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "").Replace(text)
}

func stringMapToAny(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			out[key] = parsed
		} else {
			out[key] = value
		}
	}
	return out
}

func formatAny(value any) string {
	switch v := value.(type) {
	case time.Time:
		return FormatDate(v, "en")
	case time.Duration:
		return FormatDuration(v, "en")
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return FormatNumber(v, "en")
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func numberValue(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case string:
		parsed, _ := strconv.ParseFloat(v, 64)
		return parsed
	default:
		return 0
	}
}

func findMatchingBrace(text string, start int) int {
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func pluralOption(body, category string) string {
	needle := category + " {"
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle) - 1
	end := findMatchingBrace(body, start)
	if end < 0 {
		return ""
	}
	return body[start+1 : end]
}
