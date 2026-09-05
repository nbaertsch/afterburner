package localization

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolverFallbackInterpolationPluralAndBidi(t *testing.T) {
	bundles := loadBundles(t, filepath.Join("testdata", "fixtures", "locales.json"))
	resolver := NewResolver(bundles, WithDefaultLocale("en"))
	text := resolver.Resolve("en-US", Reference{MessageID: "items", Values: map[string]string{"count": "2", "name": "Noah"}})
	if text.Locale != "en" || !text.Fallback || StripBidiIsolates(text.Text) != "Noah has 2 items" || text.Direction != DirectionLTR {
		t.Fatalf("fallback/plural text mismatch: %#v", text)
	}
	arabic := resolver.Resolve("ar", Reference{MessageID: "hello", Values: map[string]string{"name": "Noah"}})
	if arabic.Direction != DirectionRTL || arabic.Text == StripBidiIsolates(arabic.Text) {
		t.Fatalf("rtl bidi isolation missing: %#v", arabic)
	}
	missing := resolver.Resolve("fr-CA", Reference{MessageID: "missing", Fallback: "Hi {name}", Values: map[string]string{"name": "Ada"}})
	if StripBidiIsolates(missing.Text) != "Hi Ada" || !missing.Fallback {
		t.Fatalf("fallback text mismatch: %#v", missing)
	}
}

func TestFormatters(t *testing.T) {
	if got := FormatNumber(1.5, "fr-FR"); got != "1,5" {
		t.Fatalf("fr number = %q", got)
	}
	if got := FormatDate(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), "en-US"); got != "Sep 4, 2026" {
		t.Fatalf("date = %q", got)
	}
	if got := FormatDuration(90*time.Second, "en"); got != "1m 30s" {
		t.Fatalf("duration = %q", got)
	}
	if got := FormatBytes(1536, "en"); got != "1.5 KB" {
		t.Fatalf("bytes = %q", got)
	}
}

func loadBundles(t *testing.T, path string) []Bundle {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundles []Bundle
	if err := json.Unmarshal(data, &bundles); err != nil {
		t.Fatal(err)
	}
	return bundles
}
