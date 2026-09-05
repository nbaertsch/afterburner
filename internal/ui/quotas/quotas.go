package quotas

import (
	"container/list"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrInvalidQuota = errors.New("invalid quota")

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type Decision struct {
	Allowed          bool
	Key              string
	TokensRemaining  int64
	RetryAfter       time.Duration
	Backpressure     bool
	Reason           string
	Dropped          int64
	Coalesced        int64
	QueueDepth       int
	QueueLimit       int
	MemoryBytes      int64
	MemoryLimitBytes int64
}

type BucketConfig struct {
	Capacity     int64
	RefillTokens int64
	RefillEvery  time.Duration
}

func (c BucketConfig) validate() error {
	if c.Capacity <= 0 || c.RefillTokens <= 0 || c.RefillEvery <= 0 {
		return ErrInvalidQuota
	}
	return nil
}

type TokenBucket struct {
	mu       sync.Mutex
	cfg      BucketConfig
	clock    Clock
	tokens   int64
	lastFill time.Time
}

func NewTokenBucket(cfg BucketConfig, clock Clock) (*TokenBucket, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = RealClock{}
	}
	now := clock.Now()
	return &TokenBucket{cfg: cfg, clock: clock, tokens: cfg.Capacity, lastFill: now}, nil
}

func (b *TokenBucket) Allow(cost int64) Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cost <= 0 {
		cost = 1
	}
	b.refillLocked(b.clock.Now())
	if cost <= b.tokens {
		b.tokens -= cost
		return Decision{Allowed: true, TokensRemaining: b.tokens}
	}
	missing := cost - b.tokens
	intervals := (missing + b.cfg.RefillTokens - 1) / b.cfg.RefillTokens
	retry := time.Duration(intervals) * b.cfg.RefillEvery
	return Decision{Allowed: false, TokensRemaining: b.tokens, RetryAfter: retry, Backpressure: true, Reason: "token-bucket-empty"}
}

func (b *TokenBucket) Snapshot() Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked(b.clock.Now())
	return Decision{Allowed: b.tokens > 0, TokensRemaining: b.tokens, Backpressure: b.tokens == 0, Reason: "snapshot"}
}

func (b *TokenBucket) refillLocked(now time.Time) {
	if now.Before(b.lastFill) {
		b.lastFill = now
		return
	}
	elapsed := now.Sub(b.lastFill)
	if elapsed < b.cfg.RefillEvery {
		return
	}
	intervals := int64(elapsed / b.cfg.RefillEvery)
	add := intervals * b.cfg.RefillTokens
	b.tokens += add
	if b.tokens > b.cfg.Capacity {
		b.tokens = b.cfg.Capacity
	}
	b.lastFill = b.lastFill.Add(time.Duration(intervals) * b.cfg.RefillEvery)
}

type Limiter struct {
	mu      sync.Mutex
	cfg     BucketConfig
	clock   Clock
	buckets map[string]*TokenBucket
}

func NewLimiter(cfg BucketConfig, clock Clock) (*Limiter, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Limiter{cfg: cfg, clock: clock, buckets: map[string]*TokenBucket{}}, nil
}

func (l *Limiter) Allow(key string, cost int64) Decision {
	l.mu.Lock()
	bucket := l.buckets[key]
	if bucket == nil {
		bucket, _ = NewTokenBucket(l.cfg, l.clock)
		l.buckets[key] = bucket
	}
	l.mu.Unlock()
	decision := bucket.Allow(cost)
	decision.Key = key
	return decision
}

type CoalescePolicy string

const (
	PolicyLatestWins CoalescePolicy = "latest-wins"
	PolicyDropNewest CoalescePolicy = "drop-newest"
)

type Item[T any] struct {
	Key      string
	Value    T
	Size     int64
	Sequence uint64
	At       time.Time
}

type DropDiagnostic struct {
	Key              string
	Reason           string
	Dropped          int64
	Coalesced        int64
	QueueDepth       int
	QueueLimit       int
	MemoryBytes      int64
	MemoryLimitBytes int64
	At               time.Time
}

type CoalescerConfig struct {
	MaxItems       int
	MaxBytes       int64
	Policy         CoalescePolicy
	DropDiagnostic func(DropDiagnostic)
}

type coalescedEntry[T any] struct {
	item Item[T]
	node *list.Element
}

type Coalescer[T any] struct {
	mu        sync.Mutex
	cfg       CoalescerConfig
	clock     Clock
	items     map[string]*coalescedEntry[T]
	order     *list.List
	bytes     int64
	sequence  uint64
	dropped   int64
	coalesced int64
}

func NewCoalescer[T any](cfg CoalescerConfig, clock Clock) (*Coalescer[T], error) {
	if cfg.MaxItems <= 0 || cfg.MaxBytes <= 0 {
		return nil, ErrInvalidQuota
	}
	if cfg.Policy == "" {
		cfg.Policy = PolicyLatestWins
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Coalescer[T]{cfg: cfg, clock: clock, items: map[string]*coalescedEntry[T]{}, order: list.New()}, nil
}

func (c *Coalescer[T]) Enqueue(key string, value T, size int64) Decision {
	if key == "" {
		key = fmt.Sprintf("anon-%p", &value)
	}
	if size < 0 {
		size = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	if size > c.cfg.MaxBytes {
		c.dropped++
		c.reportLocked(DropDiagnostic{Key: key, Reason: "item-too-large", Dropped: c.dropped, Coalesced: c.coalesced, At: now})
		return c.decisionLocked(false, "item-too-large")
	}
	if existing := c.items[key]; existing != nil {
		c.bytes -= existing.item.Size
		c.sequence++
		existing.item = Item[T]{Key: key, Value: value, Size: size, Sequence: c.sequence, At: now}
		c.bytes += size
		c.order.MoveToBack(existing.node)
		c.coalesced++
		c.trimLocked(now)
		return c.decisionLocked(true, "coalesced")
	}
	if c.cfg.Policy == PolicyDropNewest && (c.order.Len() >= c.cfg.MaxItems || c.bytes+size > c.cfg.MaxBytes) {
		c.dropped++
		c.reportLocked(DropDiagnostic{Key: key, Reason: "drop-newest", Dropped: c.dropped, Coalesced: c.coalesced, At: now})
		return c.decisionLocked(false, "queue-full")
	}
	c.sequence++
	entry := &coalescedEntry[T]{item: Item[T]{Key: key, Value: value, Size: size, Sequence: c.sequence, At: now}}
	entry.node = c.order.PushBack(key)
	c.items[key] = entry
	c.bytes += size
	c.trimLocked(now)
	return c.decisionLocked(true, "queued")
}

func (c *Coalescer[T]) Drain(limit int) []Item[T] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 || limit > c.order.Len() {
		limit = c.order.Len()
	}
	out := make([]Item[T], 0, limit)
	for len(out) < limit {
		front := c.order.Front()
		if front == nil {
			break
		}
		key := front.Value.(string)
		entry := c.items[key]
		delete(c.items, key)
		c.order.Remove(front)
		c.bytes -= entry.item.Size
		out = append(out, entry.item)
	}
	return out
}

func (c *Coalescer[T]) Snapshot() Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decisionLocked(c.order.Len() < c.cfg.MaxItems && c.bytes < c.cfg.MaxBytes, "snapshot")
}

func (c *Coalescer[T]) trimLocked(now time.Time) {
	for c.order.Len() > c.cfg.MaxItems || c.bytes > c.cfg.MaxBytes {
		front := c.order.Front()
		if front == nil {
			return
		}
		key := front.Value.(string)
		entry := c.items[key]
		delete(c.items, key)
		c.order.Remove(front)
		c.bytes -= entry.item.Size
		c.dropped++
		c.reportLocked(DropDiagnostic{Key: key, Reason: "bounded-memory", Dropped: c.dropped, Coalesced: c.coalesced, At: now})
	}
}

func (c *Coalescer[T]) decisionLocked(allowed bool, reason string) Decision {
	return Decision{
		Allowed: allowed, Reason: reason, Backpressure: !allowed, Dropped: c.dropped, Coalesced: c.coalesced,
		QueueDepth: c.order.Len(), QueueLimit: c.cfg.MaxItems, MemoryBytes: c.bytes, MemoryLimitBytes: c.cfg.MaxBytes,
	}
}

func (c *Coalescer[T]) reportLocked(diag DropDiagnostic) {
	diag.QueueDepth = c.order.Len()
	diag.QueueLimit = c.cfg.MaxItems
	diag.MemoryBytes = c.bytes
	diag.MemoryLimitBytes = c.cfg.MaxBytes
	if c.cfg.DropDiagnostic != nil {
		c.cfg.DropDiagnostic(diag)
	}
}
