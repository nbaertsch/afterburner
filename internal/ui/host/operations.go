package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

var ErrUnknownOperation = errors.New("unknown operation")

type OperationKind string

const (
	OperationDataSource OperationKind = "dataSource"
	OperationStream     OperationKind = "stream"
	OperationAction     OperationKind = "action"
)

type OperationState string

const (
	OperationStarting     OperationState = "starting"
	OperationRunning      OperationState = "running"
	OperationCancelling   OperationState = "cancelling"
	OperationSuperseded   OperationState = "superseded"
	OperationCompleted    OperationState = "completed"
	OperationFailed       OperationState = "failed"
	OperationBackpressure OperationState = "backpressured"
)

type OperationRecord struct {
	ID            string            `json:"id"`
	Kind          OperationKind     `json:"kind"`
	State         OperationState    `json:"state"`
	Generation    uint64            `json:"generation"`
	DescriptorID  string            `json:"descriptorId,omitempty"`
	CorrelationID string            `json:"correlationId,omitempty"`
	StartedAt     time.Time         `json:"startedAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type OperationManager struct {
	mu     sync.Mutex
	clock  interface{ Now() time.Time }
	next   uint64
	record map[string]*operationEntry
	byKey  map[string]string
}

type operationEntry struct {
	record OperationRecord
	cancel context.CancelFunc
}

func NewOperationManager(clock interface{ Now() time.Time }) *OperationManager {
	if clock == nil {
		clock = realClock{}
	}
	return &OperationManager{clock: clock, record: map[string]*operationEntry{}, byKey: map[string]string{}}
}

func (m *OperationManager) StartDataSource(parent context.Context, descriptor surface.DataSourceDescriptor) (context.Context, OperationRecord) {
	return m.start(parent, OperationDataSource, "", descriptor.ID, "")
}

func (m *OperationManager) StartStream(parent context.Context, descriptor surface.StreamDescriptor) (context.Context, OperationRecord) {
	return m.start(parent, OperationStream, "", descriptor.ID, "")
}

func (m *OperationManager) StartAction(parent context.Context, invocation surface.ActionInvocation) (context.Context, OperationRecord) {
	key := invocation.ActionID
	if invocation.ComponentID != "" {
		key += ":" + invocation.ComponentID
	}
	return m.start(parent, OperationAction, invocation.ID, key, invocation.CorrelationID)
}

func (m *OperationManager) Complete(id string) (OperationRecord, error) {
	return m.setState(id, OperationCompleted, nil)
}

func (m *OperationManager) Fail(id string, metadata map[string]string) (OperationRecord, error) {
	return m.setState(id, OperationFailed, metadata)
}

func (m *OperationManager) Backpressure(id string, metadata map[string]string) (OperationRecord, error) {
	return m.setState(id, OperationBackpressure, metadata)
}

func (m *OperationManager) Cancel(id string) (OperationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.record[id]
	if entry == nil {
		return OperationRecord{}, ErrUnknownOperation
	}
	entry.record.State = OperationCancelling
	entry.record.UpdatedAt = m.clock.Now()
	entry.cancel()
	return cloneOperation(entry.record), nil
}

func (m *OperationManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.record {
		switch entry.record.State {
		case OperationRunning, OperationStarting, OperationBackpressure:
			entry.record.State = OperationCancelling
			entry.record.UpdatedAt = m.clock.Now()
			entry.cancel()
		}
	}
}

func (m *OperationManager) Get(id string) (OperationRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.record[id]
	if entry == nil {
		return OperationRecord{}, false
	}
	return cloneOperation(entry.record), true
}

func (m *OperationManager) List() []OperationRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]OperationRecord, 0, len(m.record))
	for _, entry := range m.record {
		out = append(out, cloneOperation(entry.record))
	}
	return out
}

func (m *OperationManager) start(parent context.Context, kind OperationKind, id, supersessionKey, correlationID string) (context.Context, OperationRecord) {
	if parent == nil {
		parent = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if previousID := m.byKey[string(kind)+":"+supersessionKey]; previousID != "" {
		if previous := m.record[previousID]; previous != nil {
			previous.record.State = OperationSuperseded
			previous.record.UpdatedAt = m.clock.Now()
			previous.cancel()
		}
	}
	m.next++
	if id == "" {
		id = string(kind) + "-" + strconvFormat(m.next)
	}
	now := m.clock.Now()
	ctx, cancel := context.WithCancel(parent)
	record := OperationRecord{ID: id, Kind: kind, State: OperationRunning, Generation: m.next, DescriptorID: supersessionKey, CorrelationID: correlationID, StartedAt: now, UpdatedAt: now, Metadata: map[string]string{}}
	m.record[id] = &operationEntry{record: record, cancel: cancel}
	m.byKey[string(kind)+":"+supersessionKey] = id
	return ctx, cloneOperation(record)
}

func (m *OperationManager) setState(id string, state OperationState, metadata map[string]string) (OperationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.record[id]
	if entry == nil {
		return OperationRecord{}, ErrUnknownOperation
	}
	entry.record.State = state
	entry.record.UpdatedAt = m.clock.Now()
	if metadata != nil {
		entry.record.Metadata = metadata
	}
	entry.cancel()
	return cloneOperation(entry.record), nil
}

func cloneOperation(in OperationRecord) OperationRecord {
	data, _ := json.Marshal(in)
	var out OperationRecord
	_ = json.Unmarshal(data, &out)
	return out
}

func strconvFormat(v uint64) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
