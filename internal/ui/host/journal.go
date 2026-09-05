package host

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type JournalEventType string

const (
	JournalLifecycle       JournalEventType = "lifecycle"
	JournalSnapshotApplied JournalEventType = "snapshot"
	JournalPatch           JournalEventType = "patch"
	JournalArbitration     JournalEventType = "arbitration"
	JournalQuota           JournalEventType = "quota"
)

type JournalEvent struct {
	Epoch      uint64           `json:"epoch"`
	Sequence   uint64           `json:"sequence"`
	At         time.Time        `json:"at"`
	Type       JournalEventType `json:"type"`
	SurfaceID  string           `json:"surfaceId,omitempty"`
	InstanceID string           `json:"instanceId,omitempty"`
	State      LifecycleState   `json:"state,omitempty"`
	Revision   uint64           `json:"revision,omitempty"`
	Digest     string           `json:"digest,omitempty"`
	Active     bool             `json:"active,omitempty"`
	LeaseID    string           `json:"leaseId,omitempty"`
	Reason     string           `json:"reason,omitempty"`
}

type JournalSnapshot struct {
	Epoch      uint64         `json:"epoch"`
	Sequence   uint64         `json:"sequence"`
	Events     []JournalEvent `json:"events"`
	CapturedAt time.Time      `json:"capturedAt"`
}

type Journal struct {
	mu       sync.RWMutex
	clock    Clock
	epoch    uint64
	sequence uint64
	events   []JournalEvent
	limit    int
}

func NewJournal(clock Clock) *Journal {
	if clock == nil {
		clock = realClock{}
	}
	return &Journal{clock: clock, epoch: uint64(clock.Now().UnixNano()), limit: 4096}
}

func (j *Journal) Epoch() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.epoch
}

func (j *Journal) AdvanceEpoch() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.epoch++
	if j.epoch == 0 {
		j.epoch = 1
	}
	return j.epoch
}

func (j *Journal) Append(event JournalEvent) JournalEvent {
	if j == nil {
		return event
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if event.Epoch == 0 {
		event.Epoch = j.epoch
	}
	j.sequence++
	event.Sequence = j.sequence
	if event.At.IsZero() {
		event.At = j.clock.Now()
	}
	j.events = append(j.events, event)
	if j.limit > 0 && len(j.events) > j.limit {
		copy(j.events, j.events[len(j.events)-j.limit:])
		j.events = j.events[:j.limit]
	}
	return event
}

func (j *Journal) Snapshot() JournalSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return JournalSnapshot{Epoch: j.epoch, Sequence: j.sequence, Events: append([]JournalEvent(nil), j.events...), CapturedAt: j.clock.Now()}
}

type ReconnectInput struct {
	PeerEpoch    uint64
	OpenSurfaces map[string]uint64
	ActiveLease  *Lease
}

type ReconcileAction string

const (
	ActionReopen          ReconcileAction = "reopen"
	ActionRequestSnapshot ReconcileAction = "requestSnapshot"
	ActionSafeStaleClose  ReconcileAction = "safeStaleClose"
	ActionContainCrash    ReconcileAction = "containCrash"
	ActionRestoreOwner    ReconcileAction = "restoreOwner"
)

type ReconcileStep struct {
	Action     ReconcileAction `json:"action"`
	SurfaceID  string          `json:"surfaceId,omitempty"`
	InstanceID string          `json:"instanceId,omitempty"`
	Revision   uint64          `json:"revision,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

type ReconnectPlan struct {
	Epoch       uint64          `json:"epoch"`
	Steps       []ReconcileStep `json:"steps"`
	ActiveOwner *Lease          `json:"activeOwner,omitempty"`
}

func (r *Registry) ReconcileReconnect(ctx context.Context, input ReconnectInput, arbiter *Arbiter) (ReconnectPlan, error) {
	if err := ctx.Err(); err != nil {
		return ReconnectPlan{}, err
	}
	if input.OpenSurfaces == nil {
		input.OpenSurfaces = map[string]uint64{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	plan := ReconnectPlan{Epoch: r.epoch}
	for surfaceID := range input.OpenSurfaces {
		if r.instances[surfaceID] == nil {
			reason := "surface is absent from host registry"
			if input.PeerEpoch < r.epoch {
				reason = "peer epoch stale and surface is absent"
			}
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionSafeStaleClose, SurfaceID: surfaceID, Reason: reason})
		}
	}
	for surfaceID, record := range r.instances {
		state := record.instance.State
		if state == StateOpening || state == StateUpdating || state == StateRecovering {
			record.instance.State = StateRecovering
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionContainCrash, SurfaceID: surfaceID, InstanceID: record.instance.Identity.InstanceID, Revision: record.instance.Revision, Reason: "surface interrupted during non-terminal lifecycle"})
		}
		peerRevision, peerOpen := input.OpenSurfaces[surfaceID]
		if peerOpen && (state == StateClosed || state == StateDisposed) {
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionSafeStaleClose, SurfaceID: surfaceID, InstanceID: record.instance.Identity.InstanceID, Revision: record.instance.Revision, Reason: "peer has closed host surface open"})
			continue
		}
		if !peerOpen && state != StateClosed && state != StateDisposed {
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionReopen, SurfaceID: surfaceID, InstanceID: record.instance.Identity.InstanceID, Revision: record.instance.Revision, Reason: "open instance missing after reconnect"})
			continue
		}
		if peerOpen && peerRevision != record.instance.Revision {
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionRequestSnapshot, SurfaceID: surfaceID, InstanceID: record.instance.Identity.InstanceID, Revision: record.instance.Revision, Reason: "revision mismatch"})
		}
	}
	if arbiter != nil {
		plan.ActiveOwner = arbiter.Owner()
		if plan.ActiveOwner != nil {
			plan.Steps = append(plan.Steps, ReconcileStep{Action: ActionRestoreOwner, SurfaceID: plan.ActiveOwner.SurfaceID, InstanceID: plan.ActiveOwner.InstanceID, Reason: "restore active foreground owner"})
		}
	} else if input.ActiveLease != nil {
		lease := *input.ActiveLease
		plan.ActiveOwner = &lease
	}
	sort.SliceStable(plan.Steps, func(i, k int) bool {
		if plan.Steps[i].SurfaceID == plan.Steps[k].SurfaceID {
			return plan.Steps[i].Action < plan.Steps[k].Action
		}
		return plan.Steps[i].SurfaceID < plan.Steps[k].SurfaceID
	})
	data, _ := json.Marshal(plan)
	r.journal.Append(JournalEvent{Epoch: r.epoch, At: r.clock.Now(), Type: JournalLifecycle, Reason: string(data)})
	return plan, nil
}

func (r *Registry) JournalSnapshot() JournalSnapshot {
	return r.journal.Snapshot()
}
