package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maximumLogBytes    = 5 << 20
	defaultQueueLength = 256
)

type record struct {
	root string
	data []byte
}

type dispatcher struct {
	queue  chan record
	done   chan struct{}
	closed bool
}

var (
	dispatchMu       sync.Mutex
	activeDispatcher *dispatcher
	writeRecord      = writeRecordSync
)

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
	dispatchMu.Lock()
	current := activeDispatcher
	if current == nil {
		current = newDispatcher(defaultQueueLength)
		activeDispatcher = current
	}
	if current.closed {
		dispatchMu.Unlock()
		return
	}
	select {
	case current.queue <- record{root: root, data: data}:
	default:
	}
	dispatchMu.Unlock()
}

// Shutdown drains accepted telemetry records and stops the background writer.
// A later Record call starts a fresh writer.
func Shutdown(ctx context.Context) error {
	dispatchMu.Lock()
	current := activeDispatcher
	if current == nil {
		dispatchMu.Unlock()
		return nil
	}
	if !current.closed {
		current.closed = true
		close(current.queue)
	}
	dispatchMu.Unlock()

	select {
	case <-current.done:
		dispatchMu.Lock()
		if activeDispatcher == current {
			activeDispatcher = nil
		}
		dispatchMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newDispatcher(queueLength int) *dispatcher {
	value := &dispatcher{
		queue: make(chan record, queueLength),
		done:  make(chan struct{}),
	}
	go func() {
		defer func() {
			dispatchMu.Lock()
			if activeDispatcher == value {
				activeDispatcher = nil
			}
			dispatchMu.Unlock()
			close(value.done)
		}()
		for next := range value.queue {
			writeRecord(next)
		}
	}()
	return value
}

func writeRecordSync(next record) {
	directory := filepath.Join(next.root, "state")
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	path := filepath.Join(directory, "launcher.jsonl")
	if info, err := os.Stat(path); err == nil && info.Size() >= maximumLogBytes {
		previous := filepath.Join(directory, "launcher.previous.jsonl")
		_ = os.Remove(previous)
		_ = os.Rename(path, previous)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(next.data)
	_ = file.Close()
}

func RecordUIMetadata(root, eventType string, attributes map[string]any) {
	Record(root, eventType, metadataOnly(attributes))
}

func metadataOnly(attributes map[string]any) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	allowed := map[string]bool{"hostId": true, "extensionId": true, "surfaceId": true, "instanceId": true, "state": true, "reason": true, "result": true, "opaqueId": true, "durationMillis": true, "counter": true}
	out := map[string]any{}
	for key, value := range attributes {
		if allowed[key] {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
