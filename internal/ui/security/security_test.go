package security

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

func TestCanonicalJSONIsStableForAuthentication(t *testing.T) {
	left := map[string]any{"b": []any{json.Number("2"), "x"}, "a": map[string]any{"z": true}}
	right := map[string]any{"a": map[string]any{"z": true}, "b": []any{json.Number("2"), "x"}}
	leftJSON, err := CanonicalJSON(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := CanonicalJSON(right)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftJSON) != `{"a":{"z":true},"b":[2,"x"]}` || string(leftJSON) != string(rightJSON) {
		t.Fatalf("canonical JSON mismatch: %s vs %s", leftJSON, rightJSON)
	}
}

func TestHMACSignVerifyReplayAndBadSignature(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	root := NewRootSecret([]byte("super-secret-root-material-with-entropy"))
	identity := ExtensionIdentity{SessionID: "session-1", ExtensionID: "extension-1", Protocol: protocol.Protocol}
	replayCache := NewNonceCache(time.Minute)
	replayCache.nowFunc = func() time.Time { return now }
	signer := Signer{Ring: NewKeyRing(1, root), ReplayCache: replayCache, Now: func() time.Time { return now }}
	envelope := signedEnvelope()
	if err := signer.Sign(&envelope, identity); err != nil {
		t.Fatalf("sign envelope: %v", err)
	}
	if envelope.Auth == nil || envelope.Auth.Signature == "" {
		t.Fatalf("missing signature: %#v", envelope.Auth)
	}
	if err := signer.Verify(envelope, identity); err != nil {
		t.Fatalf("verify signed envelope: %v", err)
	}
	if err := signer.Verify(envelope, identity); !hasProtocolCode(err, protocol.ErrorReplayDetected) {
		t.Fatalf("replay verify error = %v, want %s", err, protocol.ErrorReplayDetected)
	}

	tampered := envelope
	tampered.Payload = json.RawMessage(`{"tampered":true}`)
	noReplay := Signer{Ring: NewKeyRing(1, root), Now: func() time.Time { return now }}
	err := noReplay.Verify(tampered, identity)
	if !hasProtocolCode(err, protocol.ErrorAuthenticationFailed) {
		t.Fatalf("tampered verify error = %v, want auth failure", err)
	}
	if strings.Contains(err.Error(), "super-secret-root-material") || strings.Contains(err.Error(), envelope.Auth.Signature) {
		t.Fatalf("security error leaked sensitive material: %v", err)
	}
}

func TestTimestampSkewKeyRotationAndGenerationValidation(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC)
	root1 := NewRootSecret([]byte("epoch-one-root-material"))
	root2 := NewRootSecret([]byte("epoch-two-root-material"))
	identity := ExtensionIdentity{SessionID: "session-1", ExtensionID: "extension-1", Protocol: protocol.Protocol}
	ring := NewKeyRing(2, root2)
	ring.Add(1, root1)
	signer := Signer{Ring: ring, Now: func() time.Time { return now }}

	oldEpoch := signedEnvelope()
	oldEpoch.Epoch = 1
	if err := signer.Sign(&oldEpoch, identity); err != nil {
		t.Fatalf("sign epoch 1: %v", err)
	}
	if err := signer.Verify(oldEpoch, identity); err != nil {
		t.Fatalf("verify overlapping epoch 1: %v", err)
	}
	ring.DropBefore(2)
	if err := signer.Verify(oldEpoch, identity); !hasProtocolCode(err, protocol.ErrorAuthenticationFailed) {
		t.Fatalf("verify dropped epoch error = %v, want auth failure", err)
	}

	freshness := FreshnessValidator{CurrentEpoch: 2, MinimumEpoch: 2, CurrentGeneration: 7, MaxSkew: time.Minute, Now: func() time.Time { return now }}
	stale := signedEnvelope()
	stale.Epoch = 1
	stale.Generation = 7
	if err := freshness.Validate(stale); !hasProtocolCode(err, protocol.ErrorStaleEpoch) {
		t.Fatalf("stale epoch validation = %v", err)
	}
	wrongGeneration := signedEnvelope()
	wrongGeneration.Epoch = 2
	wrongGeneration.Generation = 6
	if err := freshness.Validate(wrongGeneration); !hasProtocolCode(err, protocol.ErrorStaleGeneration) {
		t.Fatalf("stale generation validation = %v", err)
	}
	wrongGeneration.Generation = 7
	wrongGeneration.Timestamp = now.Add(2 * time.Minute)
	if err := freshness.Validate(wrongGeneration); !hasProtocolCode(err, protocol.ErrorAuthenticationFailed) {
		t.Fatalf("timestamp skew validation = %v", err)
	}
}

func TestIdempotencyCacheReturnsCachedRetryAndRejectsConflict(t *testing.T) {
	cache := NewIdempotencyCache(time.Minute)
	calls := 0
	request := map[string]any{"messageId": "m1", "payload": map[string]any{"value": "same"}}
	response, replayed, err := cache.Resolve("idem-1", request, func() (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil || replayed || string(response) != `{"ok":true}` {
		t.Fatalf("first resolve = %s replayed=%v err=%v", response, replayed, err)
	}
	response, replayed, err = cache.Resolve("idem-1", request, func() (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"ok":false}`), nil
	})
	if err != nil || !replayed || string(response) != `{"ok":true}` || calls != 1 {
		t.Fatalf("retry resolve = %s replayed=%v calls=%d err=%v", response, replayed, calls, err)
	}
	_, _, err = cache.Resolve("idem-1", map[string]any{"messageId": "m1", "payload": "different"}, func() (json.RawMessage, error) {
		return json.RawMessage(`{"ok":false}`), nil
	})
	if !hasProtocolCode(err, protocol.ErrorIdempotencyConflict) {
		t.Fatalf("conflicting resolve error = %v", err)
	}
}

func TestSequenceTrackerAckGapsDuplicatesAndEpochs(t *testing.T) {
	tracker := NewSequenceTracker(8)
	ack, err := tracker.Accept("stream", 1, 1)
	if err != nil || ack.HighWater != 1 || !ack.Contiguous {
		t.Fatalf("accept seq 1 = %#v, %v", ack, err)
	}
	ack, err = tracker.Accept("stream", 1, 3)
	if err != nil || ack.HighWater != 1 || ack.Contiguous || len(ack.Missing) != 1 || ack.Missing[0] != 2 {
		t.Fatalf("accept seq 3 gap = %#v, %v", ack, err)
	}
	ack, err = tracker.Accept("stream", 1, 2)
	if err != nil || ack.HighWater != 3 || !ack.Contiguous {
		t.Fatalf("accept seq 2 closes gap = %#v, %v", ack, err)
	}
	if ack, err = tracker.Accept("stream", 1, 2); !hasProtocolCode(err, protocol.ErrorStaleSequence) || !ack.Duplicate {
		t.Fatalf("duplicate seq = %#v, %v", ack, err)
	}
	if _, err = tracker.Accept("stream", 0, 4); !hasProtocolCode(err, protocol.ErrorStaleEpoch) {
		t.Fatalf("stale epoch = %v", err)
	}
}

func TestCachesAreRaceSafe(t *testing.T) {
	cache := NewNonceCache(time.Minute)
	tracker := NewSequenceTracker(4096)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cache.Add("nonce-"+strconv.Itoa(i), time.Now())
			_, _ = tracker.Accept("race", 1, uint64(i+1))
		}()
	}
	wg.Wait()
}

func FuzzCanonicalJSON(f *testing.F) {
	f.Add([]byte(`{"b":2,"a":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if !json.Valid(data) {
			return
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return
		}
		first, err := CanonicalJSON(value)
		if err != nil {
			return
		}
		second, err := CanonicalJSON(value)
		if err != nil || string(first) != string(second) {
			t.Fatalf("canonical JSON unstable: %s vs %s err=%v", first, second, err)
		}
	})
}

func signedEnvelope() protocol.Envelope {
	return protocol.Envelope{
		SchemaVersion: protocol.SchemaVersion,
		Protocol:      protocol.Protocol,
		Revision:      protocol.ProtocolRevision,
		SessionID:     "session-1",
		ExtensionID:   "extension-1",
		MessageID:     "message-1",
		ID:            "message-1",
		Epoch:         1,
		Generation:    7,
		Sequence:      1,
		Timestamp:     time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC),
		Kind:          protocol.EnvelopeUIEvent,
		Source:        protocol.Actor{Kind: protocol.ActorExtension, ID: "extension-1"},
		Target:        protocol.Actor{Kind: protocol.ActorHost, ID: "host"},
		Compatibility: []string{protocol.CompatibilityRevision},
		ContentType:   "application/json",
		Payload:       json.RawMessage(`{"schemaVersion":1,"protocol":"afterburner.ui","revision":1,"id":"event-1","type":"submit","surfaceId":"surface-1","timestamp":"2026-09-04T20:21:10Z","trusted":true}`),
	}
}

func hasProtocolCode(err error, code protocol.ErrorCode) bool {
	if err == nil {
		return false
	}
	var structured protocol.StructuredError
	if errors.As(err, &structured) {
		return structured.Code == code
	}
	return false
}
