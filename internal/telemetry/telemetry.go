package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const maximumLogBytes = 5 << 20

type Event struct {
	SchemaVersion int            `json:"schemaVersion"`
	Timestamp     time.Time      `json:"timestamp"`
	ProcessID     int            `json:"processId"`
	Type          string         `json:"type"`
	Attributes    map[string]any `json:"attributes,omitempty"`
}

func Record(root, eventType string, attributes map[string]any) {
	if root == "" || eventType == "" {
		return
	}
	directory := filepath.Join(root, "state")
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	path := filepath.Join(directory, "launcher.jsonl")
	if info, err := os.Stat(path); err == nil && info.Size() >= maximumLogBytes {
		previous := filepath.Join(directory, "launcher.previous.jsonl")
		_ = os.Remove(previous)
		_ = os.Rename(path, previous)
	}
	data, err := json.Marshal(Event{
		SchemaVersion: 1,
		Timestamp:     time.Now().UTC(),
		ProcessID:     os.Getpid(),
		Type:          eventType,
		Attributes:    attributes,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(data)
	_ = file.Close()
}
