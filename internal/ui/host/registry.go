package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/reconcile"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

var (
	ErrUnknownSurface     = errors.New("unknown surface")
	ErrInvalidTransition  = errors.New("invalid lifecycle transition")
	ErrDuplicateSurfaceID = errors.New("duplicate surface id")
)

type LifecycleState string

const (
	StateRegistered LifecycleState = "registered"
	StateOpening    LifecycleState = "opening"
	StateActive     LifecycleState = "active"
	StateUpdating   LifecycleState = "updating"
	StateRecovering LifecycleState = "recovering"
	StateClosing    LifecycleState = "closing"
	StateClosed     LifecycleState = "closed"
	StateDisposed   LifecycleState = "disposed"
	StateFailed     LifecycleState = "failed"
)

type InstanceIdentity struct {
	SurfaceID   string    `json:"surfaceId"`
	InstanceID  string    `json:"instanceId"`
	ExtensionID string    `json:"extensionId,omitempty"`
	Epoch       uint64    `json:"epoch"`
	Generation  uint64    `json:"generation"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Instance struct {
	Identity   InstanceIdentity           `json:"identity"`
	Descriptor surface.Descriptor         `json:"descriptor"`
	State      LifecycleState             `json:"state"`
	Revision   uint64                     `json:"revision"`
	Digest     string                     `json:"digest,omitempty"`
	UpdatedAt  time.Time                  `json:"updatedAt"`
	Metadata   map[string]json.RawMessage `json:"metadata,omitempty"`
}

type RegistryConfig struct {
	Clock    Clock
	Observer *Observer
	Journal  *Journal
}

type Registry struct {
	mu        sync.RWMutex
	clock     Clock
	observer  *Observer
	journal   *Journal
	epoch     uint64
	nextID    uint64
	instances map[string]*instanceRecord
}

type instanceRecord struct {
	instance Instance
	store    *reconcile.Store
	ops      *OperationManager
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

func NewRegistry(cfg RegistryConfig) *Registry {
	clock := cfg.Clock
	if clock == nil {
		clock = realClock{}
	}
	journal := cfg.Journal
	if journal == nil {
		journal = NewJournal(clock)
	}
	return &Registry{clock: clock, observer: cfg.Observer, journal: journal, epoch: journal.Epoch(), instances: map[string]*instanceRecord{}}
}

func (r *Registry) AdvanceEpoch() uint64 {
	next := r.journal.AdvanceEpoch()
	r.mu.Lock()
	r.epoch = next
	r.mu.Unlock()
	return next
}

func (r *Registry) Register(ctx context.Context, descriptor surface.Descriptor) (InstanceIdentity, error) {
	if err := ctx.Err(); err != nil {
		return InstanceIdentity{}, err
	}
	if descriptor.ID == "" {
		return InstanceIdentity{}, fmt.Errorf("%w: descriptor id is required", ErrUnknownSurface)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.instances[descriptor.ID]; exists {
		return InstanceIdentity{}, fmt.Errorf("%w: %s", ErrDuplicateSurfaceID, descriptor.ID)
	}
	r.nextID++
	now := r.clock.Now()
	identity := InstanceIdentity{SurfaceID: descriptor.ID, InstanceID: fmt.Sprintf("%s#%d", descriptor.ID, r.nextID), ExtensionID: descriptor.OwnerExtensionID, Epoch: r.epoch, Generation: 1, CreatedAt: now}
	if descriptor.CreatedAt.IsZero() {
		descriptor.CreatedAt = now
	}
	record := &instanceRecord{instance: Instance{Identity: identity, Descriptor: descriptor, State: StateRegistered, UpdatedAt: now, Metadata: map[string]json.RawMessage{}}, store: reconcile.NewStore(descriptor.ID), ops: NewOperationManager(clockAdapter{clock: r.clock})}
	r.instances[descriptor.ID] = record
	r.recordLocked(ctx, record, StateRegistered, "registered")
	return identity, nil
}

func (r *Registry) Open(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateOpening, "open requested")
}

func (r *Registry) Activate(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateActive, "activated")
}

func (r *Registry) BeginUpdate(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateUpdating, "update started")
}

func (r *Registry) CompleteUpdate(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateActive, "update completed")
}

func (r *Registry) Recover(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateRecovering, "recovering")
}

func (r *Registry) Close(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateClosing, "close requested")
}

func (r *Registry) MarkClosed(ctx context.Context, surfaceID string) error {
	return r.transition(ctx, surfaceID, StateClosed, "closed")
}

func (r *Registry) Dispose(ctx context.Context, surfaceID string) error {
	if err := r.transition(ctx, surfaceID, StateDisposed, "disposed"); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.instances, surfaceID)
	r.mu.Unlock()
	return nil
}

func (r *Registry) Fail(ctx context.Context, surfaceID, reason string) error {
	if reason == "" {
		reason = "failed"
	}
	return r.transition(ctx, surfaceID, StateFailed, reason)
}

func (r *Registry) Get(surfaceID string) (Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	record := r.instances[surfaceID]
	if record == nil {
		return Instance{}, false
	}
	return cloneInstance(record.instance), true
}

func (r *Registry) List() []Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Instance, 0, len(r.instances))
	for _, record := range r.instances {
		out = append(out, cloneInstance(record.instance))
	}
	return out
}

func (r *Registry) ApplySnapshot(ctx context.Context, surfaceID string, tree component.Tree) (reconcile.Snapshot, error) {
	if err := r.transition(ctx, surfaceID, StateUpdating, "snapshot applying"); err != nil {
		return reconcile.Snapshot{}, err
	}
	r.mu.RLock()
	record := r.instances[surfaceID]
	r.mu.RUnlock()
	if record == nil {
		return reconcile.Snapshot{}, ErrUnknownSurface
	}
	snapshot, err := record.store.ApplySnapshot(tree)
	r.mu.Lock()
	if current := r.instances[surfaceID]; current != nil && err == nil {
		current.instance.Revision = snapshot.Revision
		current.instance.Digest = snapshot.Digest
		current.instance.UpdatedAt = r.clock.Now()
	}
	r.mu.Unlock()
	if err != nil {
		_ = r.Fail(ctx, surfaceID, err.Error())
		return reconcile.Snapshot{}, err
	}
	_ = r.transition(ctx, surfaceID, StateActive, "snapshot applied")
	r.journal.Append(JournalEvent{Epoch: r.epoch, At: r.clock.Now(), Type: JournalSnapshotApplied, SurfaceID: surfaceID, Revision: snapshot.Revision, Digest: snapshot.Digest, Reason: "snapshot applied"})
	r.observer.Observe(ctx, ObservationPatch, map[string]string{"surfaceId": surfaceID, "revision": fmt.Sprint(snapshot.Revision), "kind": "snapshot"})
	return snapshot, nil
}

func (r *Registry) ApplyPatch(ctx context.Context, surfaceID string, patch bridge.Patch, opts reconcile.ApplyOptions) (reconcile.ApplyResult, error) {
	if err := r.transition(ctx, surfaceID, StateUpdating, "patch applying"); err != nil {
		return reconcile.ApplyResult{}, err
	}
	r.mu.RLock()
	record := r.instances[surfaceID]
	r.mu.RUnlock()
	if record == nil {
		return reconcile.ApplyResult{}, ErrUnknownSurface
	}
	result, err := record.store.ApplyPatch(ctx, patch, opts)
	r.mu.Lock()
	if current := r.instances[surfaceID]; current != nil && err == nil && !result.Conflict {
		current.instance.Revision = result.Snapshot.Revision
		current.instance.Digest = result.Snapshot.Digest
		current.instance.UpdatedAt = r.clock.Now()
	}
	r.mu.Unlock()
	if err != nil {
		if result.Conflict {
			_ = r.transition(ctx, surfaceID, StateRecovering, "patch conflict requesting snapshot")
			return result, err
		}
		_ = r.Fail(ctx, surfaceID, err.Error())
		return reconcile.ApplyResult{}, err
	}
	_ = r.transition(ctx, surfaceID, StateActive, "patch applied")
	r.journal.Append(JournalEvent{Epoch: r.epoch, At: r.clock.Now(), Type: JournalPatch, SurfaceID: surfaceID, Revision: result.Snapshot.Revision, Digest: result.Snapshot.Digest, Reason: "patch applied"})
	r.observer.Observe(ctx, ObservationPatch, map[string]string{"surfaceId": surfaceID, "revision": fmt.Sprint(result.Snapshot.Revision), "kind": "patch"})
	return result, nil
}

func (r *Registry) Snapshot(surfaceID string) (reconcile.Snapshot, bool) {
	r.mu.RLock()
	record := r.instances[surfaceID]
	r.mu.RUnlock()
	if record == nil {
		return reconcile.Snapshot{}, false
	}
	return record.store.Snapshot(), true
}

func (r *Registry) OperationManager(surfaceID string) (*OperationManager, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	record := r.instances[surfaceID]
	if record == nil {
		return nil, false
	}
	return record.ops, true
}

func (r *Registry) transition(ctx context.Context, surfaceID string, next LifecycleState, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.instances[surfaceID]
	if record == nil {
		return ErrUnknownSurface
	}
	previous := record.instance.State
	if !canTransition(previous, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, previous, next)
	}
	record.instance.State = next
	record.instance.UpdatedAt = r.clock.Now()
	if next == StateOpening || next == StateRecovering {
		record.instance.Identity.Generation++
	}
	if next == StateClosing || next == StateClosed || next == StateDisposed || next == StateFailed {
		record.ops.CancelAll()
	}
	r.recordLocked(ctx, record, next, reason)
	return nil
}

func canTransition(from, to LifecycleState) bool {
	if from == to {
		return true
	}
	if to == StateFailed {
		return from != StateDisposed
	}
	switch from {
	case StateRegistered:
		return to == StateOpening || to == StateClosing || to == StateDisposed
	case StateOpening:
		return to == StateActive || to == StateRecovering || to == StateClosing
	case StateActive:
		return to == StateUpdating || to == StateRecovering || to == StateClosing
	case StateUpdating:
		return to == StateActive || to == StateRecovering || to == StateClosing
	case StateRecovering:
		return to == StateUpdating || to == StateActive || to == StateClosing || to == StateFailed
	case StateClosing:
		return to == StateClosed || to == StateDisposed || to == StateFailed
	case StateClosed:
		return to == StateOpening || to == StateDisposed
	case StateFailed:
		return to == StateRecovering || to == StateClosing || to == StateDisposed
	case StateDisposed:
		return false
	default:
		return false
	}
}

func (r *Registry) recordLocked(ctx context.Context, record *instanceRecord, state LifecycleState, reason string) {
	event := JournalEvent{Epoch: r.epoch, At: r.clock.Now(), Type: JournalLifecycle, SurfaceID: record.instance.Identity.SurfaceID, InstanceID: record.instance.Identity.InstanceID, State: state, Revision: record.instance.Revision, Digest: record.instance.Digest, Reason: reason}
	r.journal.Append(event)
	r.observer.Observe(ctx, ObservationLifecycle, map[string]string{"surfaceId": event.SurfaceID, "instanceId": event.InstanceID, "state": string(state), "reason": reason})
}

func cloneInstance(in Instance) Instance {
	data, _ := json.Marshal(in)
	var out Instance
	_ = json.Unmarshal(data, &out)
	return out
}

type clockAdapter struct{ clock Clock }

func (c clockAdapter) Now() time.Time { return c.clock.Now() }

func hostActor(id string) protocol.Actor { return protocol.Actor{Kind: protocol.ActorHost, ID: id} }

func blackBoxSinkObservation(kind string, attrs map[string]string) observability.Observation {
	values := map[string]json.RawMessage{}
	for key, value := range attrs {
		encoded, _ := json.Marshal(value)
		values[key] = encoded
	}
	return observability.Observation{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Type: kind, SinkID: observability.BlackBoxSinkID, At: time.Now().UTC(), Attributes: values}
}
