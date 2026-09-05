package host

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(2000, 0).UTC()} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type captureSink struct {
	mu           sync.Mutex
	observations []observability.Observation
	fail         bool
}

func (s *captureSink) Publish(_ context.Context, observation observability.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, observation)
	if s.fail {
		return context.Canceled
	}
	return nil
}

func TestRegistryLifecycleValidatesTransitionsAndIdentity(t *testing.T) {
	clock := newFakeClock()
	sink := &captureSink{}
	registry := NewRegistry(RegistryConfig{Clock: clock, Observer: NewObserver(sink, "host-test")})
	ctx := context.Background()
	identity, err := registry.Register(ctx, surface.Descriptor{ID: "s1", Kind: surface.KindModal, OwnerExtensionID: "ext"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.SurfaceID != "s1" || identity.InstanceID == "" || identity.Epoch == 0 || identity.Generation != 1 {
		t.Fatalf("bad identity: %#v", identity)
	}
	if err := registry.Activate(ctx, "s1"); err == nil {
		t.Fatal("activate from registered should be invalid")
	}
	if err := registry.Open(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Activate(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	instance, ok := registry.Get("s1")
	if !ok || instance.State != StateActive || instance.Identity.Generation != 2 {
		t.Fatalf("active instance = %#v", instance)
	}
	if len(registry.JournalSnapshot().Events) < 3 {
		t.Fatalf("journal did not record lifecycle: %#v", registry.JournalSnapshot())
	}
	if len(sink.observations) == 0 {
		t.Fatal("optional observer did not receive metadata")
	}
}

func TestArbiterDeterministicPriorityGestureFIFOAndTimeout(t *testing.T) {
	clock := newFakeClock()
	arbiter := NewArbiter(ArbiterConfig{Clock: clock, DefaultLeaseDuration: 10 * time.Second, AgingQuantum: time.Second, MaxAgingBoost: 1})
	ctx := context.Background()
	first, err := arbiter.Request(ctx, VisibilityRequest{SurfaceID: "s1", InstanceID: "i1", Priority: PriorityPanel, LeaseDuration: 10 * time.Second})
	if err != nil || !first.Granted {
		t.Fatalf("first grant = %#v err=%v", first, err)
	}
	_, _ = arbiter.Request(ctx, VisibilityRequest{SurfaceID: "s2", InstanceID: "i2", Priority: PriorityModal, GestureRank: 1})
	_, _ = arbiter.Request(ctx, VisibilityRequest{SurfaceID: "s3", InstanceID: "i3", Priority: PriorityModal, GestureRank: 2})
	_, _ = arbiter.Request(ctx, VisibilityRequest{SurfaceID: "s4", InstanceID: "i4", Priority: PriorityModal, GestureRank: 2})
	queue := arbiter.Queue()
	if len(queue) != 3 || queue[0].InstanceID != "i3" || queue[1].InstanceID != "i4" || queue[2].InstanceID != "i2" {
		t.Fatalf("unexpected queue order: %#v", queue)
	}
	if owner := arbiter.Owner(); owner == nil || owner.InstanceID != "i1" {
		t.Fatalf("expected one foreground owner i1, got %#v", owner)
	}
	clock.Advance(11 * time.Second)
	next, err := arbiter.Tick(ctx)
	if err != nil || !next.Granted || next.Lease.InstanceID != "i3" {
		t.Fatalf("next grant = %#v err=%v", next, err)
	}
	if bg, err := arbiter.Request(ctx, VisibilityRequest{SurfaceID: "s5", InstanceID: "i5", Priority: PriorityBackground}); err != nil || bg.Granted || bg.QueuePosition != -1 {
		t.Fatalf("background request should not take foreground: %#v err=%v", bg, err)
	}
}

func TestArbiterBoundedAgingCanLiftWaitingRequest(t *testing.T) {
	clock := newFakeClock()
	arbiter := NewArbiter(ArbiterConfig{Clock: clock, DefaultLeaseDuration: 10 * time.Second, AgingQuantum: time.Second, MaxAgingBoost: 2})
	ctx := context.Background()
	owner, _ := arbiter.Request(ctx, VisibilityRequest{SurfaceID: "owner", InstanceID: "owner", Priority: PriorityPanel})
	oldTime := clock.Now()
	_, _ = arbiter.Request(ctx, VisibilityRequest{SurfaceID: "old", InstanceID: "old", Priority: PriorityPanel, RequestedAt: oldTime})
	clock.Advance(3 * time.Second)
	_, _ = arbiter.Request(ctx, VisibilityRequest{SurfaceID: "new", InstanceID: "new", Priority: PriorityModal, RequestedAt: clock.Now()})
	queue := arbiter.Queue()
	if len(queue) != 2 || queue[0].InstanceID != "old" || queue[1].InstanceID != "new" {
		t.Fatalf("bad queue snapshot: %#v", queue)
	}
	result, _ := arbiter.Release(ctx, owner.Lease.ID)
	if !result.Granted || result.Lease.InstanceID != "old" {
		t.Fatalf("aging did not lift old request: %#v", result)
	}
}

func TestOperationSupersessionCancelsPreviousLifecycle(t *testing.T) {
	clock := newFakeClock()
	manager := NewOperationManager(clock)
	ctx1, rec1 := manager.StartDataSource(context.Background(), surface.DataSourceDescriptor{ID: "items", Kind: surface.DataQuery})
	ctx2, rec2 := manager.StartDataSource(context.Background(), surface.DataSourceDescriptor{ID: "items", Kind: surface.DataQuery})
	if rec1.ID == rec2.ID {
		t.Fatal("expected distinct operation generations")
	}
	if ctx1.Err() == nil || ctx2.Err() != nil {
		t.Fatalf("supersession cancellation wrong: ctx1=%v ctx2=%v", ctx1.Err(), ctx2.Err())
	}
	stored, ok := manager.Get(rec1.ID)
	if !ok || stored.State != OperationSuperseded {
		t.Fatalf("previous operation state = %#v ok=%v", stored, ok)
	}
}

func TestReconnectReconcilesOpenInstancesActiveOwnerAndStaleClose(t *testing.T) {
	clock := newFakeClock()
	plane := NewControlPlane(ControlPlaneConfig{Clock: clock, Arbiter: ArbiterConfig{DefaultLeaseDuration: time.Second}})
	ctx := context.Background()
	identity, err := plane.RegisterAndOpen(ctx, surface.Descriptor{ID: "s1", Kind: surface.KindModal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.RenderSnapshot(ctx, "s1", component.Tree{SurfaceID: "s1", Revision: 7, Root: component.Node{ID: "root", Kind: component.KindSurface}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.RequestForeground(ctx, VisibilityRequest{SurfaceID: "s1", InstanceID: identity.InstanceID, Priority: PriorityModal}); err != nil {
		t.Fatal(err)
	}
	plan, err := plane.Registry.ReconcileReconnect(ctx, ReconnectInput{PeerEpoch: 1, OpenSurfaces: map[string]uint64{"ghost": 1, "s1": 6}}, plane.Arbiter)
	if err != nil {
		t.Fatal(err)
	}
	want := map[ReconcileAction]bool{ActionSafeStaleClose: true, ActionRequestSnapshot: true, ActionRestoreOwner: true}
	for _, step := range plan.Steps {
		delete(want, step.Action)
	}
	if len(want) != 0 || plan.ActiveOwner == nil || plan.ActiveOwner.InstanceID != identity.InstanceID {
		data, _ := json.Marshal(plan)
		t.Fatalf("unexpected reconnect plan %s; missing %#v", data, want)
	}
}
