package tooling

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TraceOptions struct {
	HomeRoot    string
	ExtensionID string
	Redacted    bool
}

type TraceRecord struct {
	Timestamp   string         `json:"timestamp"`
	Source      string         `json:"source"`
	Kind        string         `json:"kind"`
	ExtensionID string         `json:"extensionId,omitempty"`
	Summary     string         `json:"summary"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

type TraceView struct {
	SchemaVersion int           `json:"schemaVersion"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	ExtensionID   string        `json:"extensionId"`
	Redacted      bool          `json:"redacted"`
	Sources       []string      `json:"sources"`
	Records       []TraceRecord `json:"records"`
}

func Trace(opts TraceOptions) (TraceView, error) {
	view := TraceView{SchemaVersion: SchemaVersion, GeneratedAt: DeterministicTime, ExtensionID: opts.ExtensionID, Redacted: opts.Redacted}
	if opts.ExtensionID == "" {
		return view, fmt.Errorf("extension id is required")
	}
	for _, path := range traceCandidatePaths(opts.HomeRoot, opts.ExtensionID) {
		records, err := readTraceJSONL(path, opts)
		if err != nil {
			continue
		}
		if len(records) > 0 {
			view.Sources = append(view.Sources, path)
			view.Records = append(view.Records, records...)
		}
	}
	sort.SliceStable(view.Records, func(i, j int) bool {
		if view.Records[i].Timestamp == view.Records[j].Timestamp {
			return view.Records[i].Source+view.Records[i].Kind+view.Records[i].Summary < view.Records[j].Source+view.Records[j].Kind+view.Records[j].Summary
		}
		return view.Records[i].Timestamp < view.Records[j].Timestamp
	})
	sort.Strings(view.Sources)
	return view, nil
}

func FormatTrace(view TraceView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "UI trace for %s (%d record(s), redacted=%t)\n", view.ExtensionID, len(view.Records), view.Redacted)
	for _, record := range view.Records {
		fmt.Fprintf(&b, "%s %-18s %-16s %s\n", valueOr(record.Timestamp, "-"), record.Source, record.Kind, record.Summary)
	}
	return b.String()
}

func traceCandidatePaths(homeRoot, extensionID string) []string {
	if homeRoot == "" {
		return nil
	}
	var paths []string
	paths = append(paths, filepath.Join(homeRoot, "state", "ui-traces", extensionID+".jsonl"))
	blackBoxRoot := filepath.Join(homeRoot, "extension-data", "black-box")
	_ = filepath.WalkDir(blackBoxRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths
}

func readTraceJSONL(path string, opts TraceOptions) ([]TraceRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []TraceRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			records = append(records, TraceRecord{Timestamp: "", Source: filepath.Base(path), Kind: "malformed", Summary: "malformed JSONL record"})
			continue
		}
		if !recordMatchesExtension(raw, opts.ExtensionID) {
			continue
		}
		if opts.Redacted {
			raw = redactMap(raw)
		}
		records = append(records, summarizeTraceRecord(filepath.Base(path), raw))
	}
	return records, scanner.Err()
}

func recordMatchesExtension(raw map[string]any, extensionID string) bool {
	if stringField(raw, "extensionId") == extensionID || stringField(raw, "extensionID") == extensionID {
		return true
	}
	if attrs, ok := raw["attributes"].(map[string]any); ok {
		return stringField(attrs, "extensionId") == extensionID || stringField(attrs, "extensionID") == extensionID
	}
	if source, ok := raw["source"].(map[string]any); ok {
		return stringField(source, "extensionId") == extensionID
	}
	return extensionID == "black-box" && stringField(raw, "eventType") != ""
}

func summarizeTraceRecord(source string, raw map[string]any) TraceRecord {
	attrs := map[string]any{}
	if input, ok := raw["attributes"].(map[string]any); ok {
		for key, value := range input {
			attrs[key] = value
		}
	}
	kind := firstNonEmpty(stringField(raw, "kind"), stringField(raw, "type"), stringField(raw, "eventType"), "event")
	extensionID := firstNonEmpty(stringField(raw, "extensionId"), stringField(attrs, "extensionId"))
	summary := firstNonEmpty(stringField(raw, "message"), stringField(raw, "eventType"), stringField(attrs, "detailCode"), kind)
	return TraceRecord{Timestamp: firstNonEmpty(stringField(raw, "timestamp"), stringField(raw, "observedAt")), Source: source, Kind: kind, ExtensionID: extensionID, Summary: summary, Attributes: attrs}
}

func redactMap(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		if sensitiveKey(key) {
			out[key] = "[redacted]"
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			out[key] = redactMap(typed)
		case []any:
			items := make([]any, len(typed))
			for i, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					items[i] = redactMap(nested)
				} else {
					items[i] = item
				}
			}
			out[key] = items
		default:
			out[key] = value
		}
	}
	return out
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, token := range []string{"prompt", "response", "secret", "token", "source", "toolarguments", "toolresult", "body"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func stringField(raw map[string]any, key string) string {
	if value, ok := raw[key].(string); ok {
		return value
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
