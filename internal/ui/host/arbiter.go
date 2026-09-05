package host

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type PriorityClass int

const (
	PriorityBackground PriorityClass = iota
	PriorityInline
	PriorityPanel
	PriorityModal
	PrioritySystem
	PriorityCritical
)

type VisibilityRequest struct {
	SurfaceID     string
	InstanceID    string
	Priority      PriorityClass
	GestureRank   int
	LeaseDuration time.Duration
	RequestedAt   time.Time
	Reason        string
}

type Lease struct {
	ID         string        `json:"id"`
	SurfaceID  string        `json:"surfaceId"`
	InstanceID string        `json:"instanceId"`
	Priority   PriorityClass `json:"priority"`
	Sequence   uint64        `json:"sequence"`
	GrantedAt  time.Time     `json:"grantedAt"`
	ExpiresAt  time.Time     `json:"expiresAt"`
	Reason     string        `json:"reason,omitempty"`
}

type ArbitrationResult struct {
	Granted       bool
	Lease         Lease
	Owner         *Lease
	SurfaceID     string
	InstanceID    string
	QueuePosition int
	RetryAfter    time.Duration
	Reason        string
}

type ArbiterConfig struct {
	Clock                Clock
	DefaultLeaseDuration time.Duration
	AgingQuantum         time.Duration
	MaxAgingBoost        int
	Observer             *Observer
}

type Arbiter struct {
	mu       sync.Mutex
	clock    Clock
	observer *Observer
	cfg      ArbiterConfig
	seq      uint64
	owner    *Lease
	queue    []*queuedRequest
}

type queuedRequest struct {
	request VisibilityRequest
	seq     uint64
}

func NewArbiter(cfg ArbiterConfig) *Arbiter {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.DefaultLeaseDuration <= 0 {
		cfg.DefaultLeaseDuration = 30 * time.Second
	}
	if cfg.AgingQuantum <= 0 {
		cfg.AgingQuantum = 5 * time.Second
	}
	if cfg.MaxAgingBoost < 0 {
		cfg.MaxAgingBoost = 0
	}
	return &Arbiter{clock: cfg.Clock, observer: cfg.Observer, cfg: cfg}
}

func (a *Arbiter) Request(ctx context.Context, req VisibilityRequest) (ArbitrationResult, error) {
	if err := ctx.Err(); err != nil {
		return ArbitrationResult{}, err
	}
	if req.SurfaceID == "" || req.InstanceID == "" {
		return ArbitrationResult{}, fmt.Errorf("visibility request requires surface and instance id")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now(req.RequestedAt)
	if a.expireLocked(now) && len(a.queue) > 0 {
		a.grantNextLocked(now)
	}
	if req.LeaseDuration <= 0 {
		req.LeaseDuration = a.cfg.DefaultLeaseDuration
	}
	if req.RequestedAt.IsZero() {
		req.RequestedAt = now
	}
	if req.Priority == PriorityBackground {
		return ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), SurfaceID: req.SurfaceID, InstanceID: req.InstanceID, QueuePosition: -1, Reason: "background-update-allowed"}, nil
	}
	if a.owner == nil {
		lease := a.grantLocked(req, now)
		a.observer.Observe(ctx, ObservationArbitration, map[string]string{"surfaceId": req.SurfaceID, "instanceId": req.InstanceID, "granted": "true"})
		return ArbitrationResult{Granted: true, Lease: lease, Reason: "granted"}, nil
	}
	for _, existing := range a.queue {
		if existing.request.InstanceID == req.InstanceID {
			existing.request = req
			a.sortLocked(now)
			pos := a.positionLocked(existing.seq)
			return ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), SurfaceID: req.SurfaceID, InstanceID: req.InstanceID, QueuePosition: pos, RetryAfter: a.retryAfterLocked(pos, now), Reason: "updated-queued-request"}, nil
		}
	}
	a.seq++
	queued := &queuedRequest{request: req, seq: a.seq}
	a.queue = append(a.queue, queued)
	a.sortLocked(now)
	pos := a.positionLocked(queued.seq)
	a.observer.Observe(ctx, ObservationArbitration, map[string]string{"surfaceId": req.SurfaceID, "instanceId": req.InstanceID, "granted": "false", "position": fmt.Sprint(pos)})
	return ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), SurfaceID: req.SurfaceID, InstanceID: req.InstanceID, QueuePosition: pos, RetryAfter: a.retryAfterLocked(pos, now), Reason: "queued"}, nil
}

func (a *Arbiter) Release(ctx context.Context, leaseID string) (ArbitrationResult, error) {
	if err := ctx.Err(); err != nil {
		return ArbitrationResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.owner == nil || a.owner.ID != leaseID {
		return ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), Reason: "lease-not-owner"}, nil
	}
	a.owner = nil
	result := a.grantNextLocked(a.clock.Now())
	a.observer.Observe(ctx, ObservationArbitration, map[string]string{"leaseId": leaseID, "released": "true", "nextGranted": fmt.Sprint(result.Granted)})
	return result, nil
}

func (a *Arbiter) Renew(ctx context.Context, leaseID string, extend time.Duration) (Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, false, err
	}
	if extend <= 0 {
		extend = a.cfg.DefaultLeaseDuration
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	a.expireLocked(now)
	if a.owner == nil || a.owner.ID != leaseID {
		return Lease{}, false, nil
	}
	a.owner.ExpiresAt = now.Add(extend)
	return *a.owner, true, nil
}

func (a *Arbiter) Tick(ctx context.Context) (ArbitrationResult, error) {
	if err := ctx.Err(); err != nil {
		return ArbitrationResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	expired := a.expireLocked(now)
	if expired {
		result := a.grantNextLocked(now)
		a.observer.Observe(ctx, ObservationArbitration, map[string]string{"expired": "true", "nextGranted": fmt.Sprint(result.Granted)})
		return result, nil
	}
	return ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), Reason: "no-expiration"}, nil
}

func (a *Arbiter) Owner() *Lease {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked(a.clock.Now())
	return cloneLeasePtr(a.owner)
}

func (a *Arbiter) Queue() []ArbitrationResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	a.sortLocked(now)
	out := make([]ArbitrationResult, 0, len(a.queue))
	for i, req := range a.queue {
		out = append(out, ArbitrationResult{Granted: false, Owner: cloneLeasePtr(a.owner), SurfaceID: req.request.SurfaceID, InstanceID: req.request.InstanceID, QueuePosition: i + 1, RetryAfter: a.retryAfterLocked(i+1, now), Reason: req.request.Reason})
	}
	return out
}

func (a *Arbiter) BackgroundUpdateAllowed(instanceID string) bool { return instanceID != "" }

func (a *Arbiter) grantLocked(req VisibilityRequest, now time.Time) Lease {
	a.seq++
	lease := Lease{ID: fmt.Sprintf("lease-%d", a.seq), SurfaceID: req.SurfaceID, InstanceID: req.InstanceID, Priority: req.Priority, Sequence: a.seq, GrantedAt: now, ExpiresAt: now.Add(req.LeaseDuration), Reason: req.Reason}
	a.owner = &lease
	return lease
}

func (a *Arbiter) grantNextLocked(now time.Time) ArbitrationResult {
	if len(a.queue) == 0 {
		return ArbitrationResult{Granted: false, Reason: "queue-empty"}
	}
	a.sortLocked(now)
	next := a.queue[0]
	a.queue = a.queue[1:]
	lease := a.grantLocked(next.request, now)
	return ArbitrationResult{Granted: true, Lease: lease, Reason: "granted-next"}
}

func (a *Arbiter) expireLocked(now time.Time) bool {
	if a.owner != nil && !a.owner.ExpiresAt.After(now) {
		a.owner = nil
		return true
	}
	return false
}

func (a *Arbiter) sortLocked(now time.Time) {
	sort.SliceStable(a.queue, func(i, j int) bool {
		left, right := a.queue[i], a.queue[j]
		lp, rp := a.effectivePriority(left, now), a.effectivePriority(right, now)
		if lp != rp {
			return lp > rp
		}
		if left.request.GestureRank != right.request.GestureRank {
			return left.request.GestureRank > right.request.GestureRank
		}
		return left.seq < right.seq
	})
}

func (a *Arbiter) effectivePriority(req *queuedRequest, now time.Time) int {
	age := 0
	if now.After(req.request.RequestedAt) {
		age = int(now.Sub(req.request.RequestedAt) / a.cfg.AgingQuantum)
	}
	if age > a.cfg.MaxAgingBoost {
		age = a.cfg.MaxAgingBoost
	}
	return int(req.request.Priority) + age
}

func (a *Arbiter) positionLocked(seq uint64) int {
	for i, req := range a.queue {
		if req.seq == seq {
			return i + 1
		}
	}
	return 0
}

func (a *Arbiter) retryAfterLocked(position int, now time.Time) time.Duration {
	if position <= 0 {
		return 0
	}
	if a.owner == nil {
		return 0
	}
	remaining := a.owner.ExpiresAt.Sub(now)
	if remaining < 0 {
		return 0
	}
	return remaining + time.Duration(position-1)*a.cfg.DefaultLeaseDuration
}

func (a *Arbiter) now(candidate time.Time) time.Time {
	if candidate.IsZero() {
		return a.clock.Now()
	}
	return candidate
}

func cloneLeasePtr(in *Lease) *Lease {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
