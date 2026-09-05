package quotas

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1000, 0).UTC()} }
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

func TestTokenBucketBackpressureAndRefill(t *testing.T) {
	clock := newFakeClock()
	bucket, err := NewTokenBucket(BucketConfig{Capacity: 2, RefillTokens: 1, RefillEvery: time.Second}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if d := bucket.Allow(2); !d.Allowed || d.TokensRemaining != 0 {
		t.Fatalf("initial allow = %#v", d)
	}
	if d := bucket.Allow(1); d.Allowed || !d.Backpressure || d.RetryAfter != time.Second {
		t.Fatalf("empty decision = %#v", d)
	}
	clock.Advance(time.Second)
	if d := bucket.Allow(1); !d.Allowed || d.TokensRemaining != 0 {
		t.Fatalf("refilled decision = %#v", d)
	}
}

func TestLatestWinsCoalescerBoundsMemoryAndReportsDrops(t *testing.T) {
	clock := newFakeClock()
	var diagnostics []DropDiagnostic
	coalescer, err := NewCoalescer[string](CoalescerConfig{MaxItems: 2, MaxBytes: 10, Policy: PolicyLatestWins, DropDiagnostic: func(d DropDiagnostic) { diagnostics = append(diagnostics, d) }}, clock)
	if err != nil {
		t.Fatal(err)
	}
	coalescer.Enqueue("a", "old", 4)
	coalescer.Enqueue("b", "two", 4)
	if d := coalescer.Enqueue("a", "new", 4); d.Coalesced != 1 || d.QueueDepth != 2 {
		t.Fatalf("coalesce decision = %#v", d)
	}
	if d := coalescer.Enqueue("c", "three", 4); d.Dropped != 1 || d.QueueDepth != 2 || d.MemoryBytes > d.MemoryLimitBytes {
		t.Fatalf("bounded decision = %#v", d)
	}
	items := coalescer.Drain(0)
	if len(items) != 2 || items[0].Key != "a" || items[0].Value != "new" || items[1].Key != "c" {
		t.Fatalf("drain order/items = %#v", items)
	}
	if len(diagnostics) != 1 || diagnostics[0].Reason != "bounded-memory" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestCoalescerConcurrentAccessStaysBounded(t *testing.T) {
	clock := newFakeClock()
	coalescer, err := NewCoalescer[int](CoalescerConfig{MaxItems: 4, MaxBytes: 1024, Policy: PolicyLatestWins}, clock)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			coalescer.Enqueue("same", i, 1)
			coalescer.Enqueue(string(rune('a'+i%8)), i, 1)
		}(i)
	}
	wg.Wait()
	if snapshot := coalescer.Snapshot(); snapshot.QueueDepth > 4 || snapshot.MemoryBytes > 1024 {
		t.Fatalf("coalescer exceeded bounds: %#v", snapshot)
	}
}
