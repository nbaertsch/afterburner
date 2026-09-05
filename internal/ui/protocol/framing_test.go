package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestFrameHandlesFragmentationLimitsDepthAndCancellation(t *testing.T) {
	envelope := validEnvelope()
	var buf bytes.Buffer
	if err := WriteJSONFrame(context.Background(), shortWriter{w: &buf, limit: 3}, envelope, FramingOptions{MaxFrameBytes: 4096, MaxJSONDepth: 16}); err != nil {
		t.Fatalf("write fragmented frame: %v", err)
	}
	var decoded Envelope
	if err := ReadJSONFrame(context.Background(), shortReader{r: bytes.NewReader(buf.Bytes()), limit: 2}, &decoded, FramingOptions{MaxFrameBytes: 4096, MaxJSONDepth: 16}); err != nil {
		t.Fatalf("read fragmented frame: %v", err)
	}
	if decoded.EffectiveMessageID() != envelope.MessageID || decoded.Sequence != envelope.Sequence {
		t.Fatalf("decoded envelope mismatch: %#v", decoded)
	}

	oversized := new(bytes.Buffer)
	_ = binary.Write(oversized, binary.BigEndian, uint32(5))
	oversized.WriteString("12345")
	if err := ReadJSONFrame(context.Background(), oversized, &decoded, FramingOptions{MaxFrameBytes: 4}); !hasCode(err, ErrorFrameTooLarge) {
		t.Fatalf("oversized frame error = %v, want %s", err, ErrorFrameTooLarge)
	}

	partialPrefix := bytes.NewReader([]byte{0, 0})
	if err := ReadJSONFrame(context.Background(), partialPrefix, &decoded, FramingOptions{}); !hasCode(err, ErrorInvalidFrame) {
		t.Fatalf("partial length error = %v, want %s", err, ErrorInvalidFrame)
	}

	deep := []byte(`[[[[[[[[{"x":1}]]]]]]]]`)
	if err := WriteFrame(context.Background(), io.Discard, deep, FramingOptions{MaxFrameBytes: 256, MaxJSONDepth: 4}); !hasCode(err, ErrorJSONTooDeep) {
		t.Fatalf("deep JSON error = %v, want %s", err, ErrorJSONTooDeep)
	}

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := ReadFrame(ctx, left, FramingOptions{}); !hasCode(err, ErrorDeadlineExceeded) {
		t.Fatalf("deadline read error = %v, want %s", err, ErrorDeadlineExceeded)
	}
}

func TestStreamConcurrentWritesPreserveWholeFrames(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	writer := NewStream(left, FramingOptions{MaxFrameBytes: 4096})
	reader := NewStream(right, FramingOptions{MaxFrameBytes: 4096})

	const count = 20
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		i := i
		go func() {
			defer wg.Done()
			envelope := validEnvelope()
			envelope.MessageID = "msg-" + strconv.Itoa(i)
			envelope.ID = envelope.MessageID
			envelope.Sequence = uint64(i + 1)
			if err := writer.WriteEnvelope(context.Background(), envelope); err != nil {
				t.Errorf("write envelope %d: %v", i, err)
			}
		}()
	}

	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		envelope, err := reader.ReadEnvelope(context.Background())
		if err != nil {
			t.Fatalf("read envelope %d: %v", i, err)
		}
		seen[envelope.MessageID] = true
	}
	wg.Wait()
	if len(seen) != count {
		t.Fatalf("read %d unique frames, want %d", len(seen), count)
	}
}

func TestNegotiationRejectsDowngradeAndSelectsStableRevision(t *testing.T) {
	local := DefaultHello(EndpointMetadata{Name: "host", Locale: "en-US", Theme: "dark"})
	local.SupportedRevisions = []int{1, 2}
	local.PreferredRevision = 2
	remote := DefaultHello(EndpointMetadata{Name: "extension"})
	remote.SupportedRevisions = []int{1, 2}
	remote.PreferredRevision = 1
	result, err := NegotiateHello(local, remote)
	if err != nil || !result.Accepted || result.Revision != 2 {
		t.Fatalf("negotiation = %#v, %v", result, err)
	}
	remote.SupportedRevisions = []int{1}
	local.RejectDowngrade = true
	if result, err := NegotiateHello(local, remote); !hasCode(err, ErrorDowngradeRejected) || result.Accepted {
		t.Fatalf("downgrade negotiation = %#v, %v; want rejected", result, err)
	}
}

func TestDispatcherValidatesEnvelopeBeforeDispatch(t *testing.T) {
	called := false
	dispatcher := Dispatcher{
		Validator: JSONShapeValidator{MaxDepth: 8},
		Handlers: map[EnvelopeKind]Handler{
			EnvelopeUIEvent: func(context.Context, Envelope) error {
				called = true
				return nil
			},
		},
	}
	envelope := validEnvelope()
	envelope.Kind = EnvelopeUIEvent
	envelope.Payload = json.RawMessage(`{"schemaVersion":1,"protocol":"afterburner.ui","revision":1,"id":"event-1","type":"submit","surfaceId":"surface-1","timestamp":"2026-09-04T20:21:10Z","trusted":true}`)
	if err := dispatcher.Dispatch(context.Background(), envelope); err != nil {
		t.Fatalf("dispatch valid envelope: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
	envelope.Payload = json.RawMessage(`{"schemaVersion":1}`)
	if err := dispatcher.Dispatch(context.Background(), envelope); !hasCode(err, ErrorInvalidEnvelope) {
		t.Fatalf("dispatch invalid payload error = %v, want %s", err, ErrorInvalidEnvelope)
	}
}

func TestDispatcherValidatesSDKShapedPayloadFixtures(t *testing.T) {
	cases := []struct {
		name string
		path string
		kind EnvelopeKind
	}{
		{"snapshot", "sdk-component-snapshot-envelope.valid.json", EnvelopeComponentSnapshot},
		{"patch", "sdk-component-patch-envelope.valid.json", EnvelopeComponentPatch},
		{"event", "sdk-ui-event-envelope.valid.json", EnvelopeUIEvent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := readEnvelopeFixture(t, tc.path)
			if envelope.Kind != tc.kind {
				t.Fatalf("fixture kind = %s, want %s", envelope.Kind, tc.kind)
			}
			if !hasCompatibility(envelope.Compatibility, CompatibilityRevision) {
				t.Fatalf("fixture lost %s compatibility: %#v", CompatibilityRevision, envelope.Compatibility)
			}
			called := false
			dispatcher := fixtureDispatcher(func(ctx context.Context, envelope Envelope) error {
				called = true
				return nil
			})
			if err := dispatcher.Dispatch(context.Background(), envelope); err != nil {
				t.Fatalf("dispatch SDK-shaped fixture: %v", err)
			}
			if !called {
				t.Fatal("fixture handler was not called")
			}
			if envelope.Kind == EnvelopeComponentSnapshot {
				var payload struct {
					Revision  uint64         `json:"revision"`
					SurfaceID string         `json:"surfaceId"`
					Root      map[string]any `json:"root"`
				}
				if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
					t.Fatalf("snapshot payload unmarshal: %v", err)
				}
				if envelope.Revision != ProtocolRevision || payload.Revision != 7 || payload.SurfaceID != "surface-main" || payload.Root["kind"] != "application" {
					t.Fatalf("snapshot revision/surface shape mismatch: envelope=%d payload=%#v", envelope.Revision, payload)
				}
			}
		})
	}
}

func TestDispatcherRejectsPayloadSchemaMismatches(t *testing.T) {
	snapshot := readEnvelopeFixture(t, "sdk-component-snapshot-envelope.valid.json")
	patch := readEnvelopeFixture(t, "sdk-component-patch-envelope.valid.json")
	event := readEnvelopeFixture(t, "sdk-ui-event-envelope.valid.json")
	cases := []struct {
		name     string
		envelope Envelope
		code     ErrorCode
	}{
		{"malformed payload json", withRawPayload(snapshot, json.RawMessage(`{"root":`)), ErrorInvalidEnvelope},
		{"unknown envelope field in snapshot payload", withPayloadField(t, snapshot, "schemaVersion", float64(1)), ErrorInvalidEnvelope},
		{"missing required snapshot root", withoutPayloadField(t, snapshot, "root"), ErrorInvalidEnvelope},
		{"snapshot kind rejects event payload", withRawPayload(snapshot, event.Payload), ErrorInvalidEnvelope},
		{"patch payload revision mismatch", withPayloadField(t, patch, "revision", float64(2)), ErrorInvalidEnvelope},
		{"event rejects envelope-level session id", withPayloadField(t, event, "sessionId", "session-001"), ErrorInvalidEnvelope},
		{"envelope protocol revision mismatch", withEnvelopeRevision(snapshot, ProtocolRevision+1), ErrorUnsupportedRevision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fixtureDispatcher(func(context.Context, Envelope) error { return nil }).Dispatch(context.Background(), tc.envelope)
			if !hasCode(err, tc.code) {
				t.Fatalf("dispatch error = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSDKShapedEnvelopeFrameRejectsOversize(t *testing.T) {
	envelope := readEnvelopeFixture(t, "sdk-component-snapshot-envelope.valid.json")
	var buf bytes.Buffer
	if err := WriteJSONFrame(context.Background(), &buf, envelope, FramingOptions{MaxFrameBytes: 64, MaxJSONDepth: 16}); !hasCode(err, ErrorFrameTooLarge) {
		t.Fatalf("oversized SDK envelope frame error = %v, want %s", err, ErrorFrameTooLarge)
	}
}

func TestHelloGoldenCompatibilityFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "hello-r1.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hello Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		t.Fatalf("hello fixture unmarshal: %v", err)
	}
	if err := hello.Validate(); err != nil {
		t.Fatalf("hello fixture validate: %v", err)
	}
	if hello.Protocol != Protocol || hello.PreferredRevision != ProtocolRevision || hello.Compatibility[0] != CompatibilityRevision {
		t.Fatalf("hello fixture is not r1-compatible: %#v", hello)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadFrame(context.Background(), bytes.NewReader(data), FramingOptions{MaxFrameBytes: 128, MaxJSONDepth: 8})
	})
}

func validEnvelope() Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Protocol:      Protocol,
		Revision:      ProtocolRevision,
		SessionID:     "session-1",
		ExtensionID:   "extension-1",
		MessageID:     "message-1",
		ID:            "message-1",
		Epoch:         1,
		Generation:    1,
		Sequence:      1,
		Timestamp:     time.Date(2026, 9, 4, 20, 21, 10, 0, time.UTC),
		Kind:          EnvelopeComponentSnapshot,
		Source:        Actor{Kind: ActorExtension, ID: "extension-1"},
		Target:        Actor{Kind: ActorRenderer, ID: "renderer-1"},
		Compatibility: []string{CompatibilityRevision},
		ContentType:   "application/json",
		Payload:       json.RawMessage(`{"root":{"id":"root","kind":"application"},"revision":1,"surfaceId":"surface-1"}`),
	}
}

func readEnvelopeFixture(t *testing.T, name string) Envelope {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("fixture %s unmarshal: %v", name, err)
	}
	return envelope
}

func fixtureDispatcher(handler Handler) Dispatcher {
	return Dispatcher{
		Validator: JSONShapeValidator{MaxDepth: 16},
		Handlers: map[EnvelopeKind]Handler{
			EnvelopeComponentSnapshot: handler,
			EnvelopeComponentPatch:    handler,
			EnvelopeUIEvent:           handler,
		},
	}
}

func withRawPayload(envelope Envelope, payload json.RawMessage) Envelope {
	envelope.Payload = payload
	return envelope
}

func withEnvelopeRevision(envelope Envelope, revision int) Envelope {
	envelope.Revision = revision
	return envelope
}

func withPayloadField(t *testing.T, envelope Envelope, field string, value any) Envelope {
	t.Helper()
	object := payloadObject(t, envelope.Payload)
	object[field] = value
	envelope.Payload = marshalPayload(t, object)
	return envelope
}

func withoutPayloadField(t *testing.T, envelope Envelope, field string) Envelope {
	t.Helper()
	object := payloadObject(t, envelope.Payload)
	delete(object, field)
	envelope.Payload = marshalPayload(t, object)
	return envelope
}

func payloadObject(t *testing.T, payload json.RawMessage) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	return object
}

func marshalPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("payload marshal: %v", err)
	}
	return data
}

func hasCompatibility(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasCode(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}
	var structured StructuredError
	if errors.As(err, &structured) {
		return structured.Code == code
	}
	return false
}

type shortWriter struct {
	w     io.Writer
	limit int
}

func (w shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.w.Write(p)
}

type shortReader struct {
	r     io.Reader
	limit int
}

func (r shortReader) Read(p []byte) (int, error) {
	if len(p) > r.limit {
		p = p[:r.limit]
	}
	return r.r.Read(p)
}
