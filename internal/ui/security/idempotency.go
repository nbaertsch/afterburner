package security

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type IdempotencyCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]idempotencyEntry
	nowFunc func() time.Time
}

type idempotencyEntry struct {
	requestHash []byte
	response    json.RawMessage
	storedAt    time.Time
}

func NewIdempotencyCache(ttl time.Duration) *IdempotencyCache {
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return &IdempotencyCache{ttl: ttl, entries: map[string]idempotencyEntry{}}
}

func (c *IdempotencyCache) Resolve(key string, request any, compute func() (json.RawMessage, error)) (json.RawMessage, bool, error) {
	if key == "" {
		response, err := compute()
		return response, false, err
	}
	requestHash, err := CanonicalJSON(request)
	if err != nil {
		return nil, false, protocol.StructuredError{Code: protocol.ErrorInvalidEnvelope, Message: "idempotent request cannot be canonicalized", Recoverable: false}
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]idempotencyEntry{}
	}
	c.cleanupLocked(c.now())
	if entry, ok := c.entries[key]; ok {
		if !bytes.Equal(entry.requestHash, requestHash) {
			c.mu.Unlock()
			return nil, true, protocol.StructuredError{Code: protocol.ErrorIdempotencyConflict, Message: "idempotency key was reused with a different request", Recoverable: false, Target: "idempotencyKey"}
		}
		response := append(json.RawMessage(nil), entry.response...)
		c.mu.Unlock()
		return response, true, nil
	}
	c.mu.Unlock()

	response, err := compute()
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	c.entries[key] = idempotencyEntry{requestHash: requestHash, response: append(json.RawMessage(nil), response...), storedAt: c.now()}
	c.mu.Unlock()
	return response, false, nil
}

func (c *IdempotencyCache) cleanupLocked(now time.Time) {
	for key, entry := range c.entries {
		if now.Sub(entry.storedAt) > c.ttl {
			delete(c.entries, key)
		}
	}
}

func (c *IdempotencyCache) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc().UTC()
	}
	return time.Now().UTC()
}
