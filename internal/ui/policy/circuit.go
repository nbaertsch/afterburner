package policy

import (
	"sync"
	"time"
)

type CircuitPolicy struct {
	MaxCrashes     int           `json:"maxCrashes"`
	Window         time.Duration `json:"-"`
	WindowMillis   int64         `json:"windowMillis,omitempty"`
	Cooldown       time.Duration `json:"-"`
	CooldownMillis int64         `json:"cooldownMillis,omitempty"`
	SafeModeOnOpen bool          `json:"safeModeOnOpen"`
}

type CircuitState string

const (
	CircuitClosed CircuitState = "closed"
	CircuitOpen   CircuitState = "open"
)

type CircuitBreaker struct {
	mu      sync.Mutex
	policy  CircuitPolicy
	clock   func() time.Time
	crashes map[string][]time.Time
	opened  map[string]time.Time
}

func NewCircuitBreaker(policy CircuitPolicy, clock func() time.Time) *CircuitBreaker {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if policy.MaxCrashes == 0 {
		policy.MaxCrashes = 3
	}
	if policy.Window == 0 && policy.WindowMillis > 0 {
		policy.Window = time.Duration(policy.WindowMillis) * time.Millisecond
	}
	if policy.Window == 0 {
		policy.Window = time.Minute
	}
	if policy.Cooldown == 0 && policy.CooldownMillis > 0 {
		policy.Cooldown = time.Duration(policy.CooldownMillis) * time.Millisecond
	}
	if policy.Cooldown == 0 {
		policy.Cooldown = 5 * time.Minute
	}
	return &CircuitBreaker{policy: policy, clock: clock, crashes: map[string][]time.Time{}, opened: map[string]time.Time{}}
}

func (b *CircuitBreaker) RecordCrash(extensionID string) CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	cutoff := now.Add(-b.policy.Window)
	kept := b.crashes[extensionID][:0]
	for _, at := range b.crashes[extensionID] {
		if at.After(cutoff) || at.Equal(cutoff) {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	b.crashes[extensionID] = kept
	if len(kept) >= b.policy.MaxCrashes {
		b.opened[extensionID] = now
		return CircuitOpen
	}
	return CircuitClosed
}

func (b *CircuitBreaker) State(extensionID string) CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	openedAt, ok := b.opened[extensionID]
	if !ok {
		return CircuitClosed
	}
	if b.clock().Sub(openedAt) >= b.policy.Cooldown {
		delete(b.opened, extensionID)
		b.crashes[extensionID] = nil
		return CircuitClosed
	}
	return CircuitOpen
}

func (b *CircuitBreaker) SafeMode(extensionID string) bool {
	return b.policy.SafeModeOnOpen && b.State(extensionID) == CircuitOpen
}
