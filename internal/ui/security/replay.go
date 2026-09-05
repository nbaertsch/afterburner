package security

import (
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type NonceCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	nonces  map[string]time.Time
	nowFunc func() time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	if ttl == 0 {
		ttl = DefaultMaxSkew * 2
	}
	return &NonceCache{ttl: ttl, nonces: map[string]time.Time{}}
}

func (c *NonceCache) Add(nonce string, _ time.Time) error {
	if nonce == "" {
		return protocol.StructuredError{Code: protocol.ErrorReplayDetected, Message: "nonce is required", Recoverable: false, Target: "auth.nonce"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nonces == nil {
		c.nonces = map[string]time.Time{}
	}
	now := c.now()
	for key, seenAt := range c.nonces {
		if now.Sub(seenAt) > c.ttl {
			delete(c.nonces, key)
		}
	}
	if seenAt, ok := c.nonces[nonce]; ok && now.Sub(seenAt) <= c.ttl {
		return protocol.StructuredError{Code: protocol.ErrorReplayDetected, Message: "duplicate nonce rejected", Recoverable: false, Target: "auth.nonce"}
	}
	c.nonces[nonce] = now
	return nil
}

func (c *NonceCache) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc().UTC()
	}
	return time.Now().UTC()
}

type FreshnessValidator struct {
	CurrentEpoch      uint64
	MinimumEpoch      uint64
	CurrentGeneration uint64
	MaxSkew           time.Duration
	Now               func() time.Time
}

func (v FreshnessValidator) Validate(envelope protocol.Envelope) error {
	if v.MinimumEpoch != 0 && envelope.Epoch < v.MinimumEpoch {
		return protocol.StructuredError{Code: protocol.ErrorStaleEpoch, Message: "envelope epoch is no longer accepted", Recoverable: false, Target: "epoch"}
	}
	if v.CurrentEpoch != 0 && envelope.Epoch > v.CurrentEpoch {
		return protocol.StructuredError{Code: protocol.ErrorStaleEpoch, Message: "envelope epoch is from the future", Recoverable: true, Target: "epoch"}
	}
	if v.CurrentGeneration != 0 && envelope.Generation != 0 && envelope.Generation != v.CurrentGeneration {
		return protocol.StructuredError{Code: protocol.ErrorStaleGeneration, Message: "envelope generation does not match current host generation", Recoverable: false, Target: "generation"}
	}
	maxSkew := v.MaxSkew
	if maxSkew == 0 {
		maxSkew = DefaultMaxSkew
	}
	now := v.now()
	if !envelope.Timestamp.IsZero() && (envelope.Timestamp.Before(now.Add(-maxSkew)) || envelope.Timestamp.After(now.Add(maxSkew))) {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "envelope timestamp is outside allowed skew", Recoverable: false, Target: "timestamp"}
	}
	return nil
}

func (v FreshnessValidator) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}
