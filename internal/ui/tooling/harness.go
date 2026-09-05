package tooling

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/host"
	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/reconcile"
	"github.com/nbaertsch/afterburner/internal/ui/render"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type TestHost struct {
	Control   *host.ControlPlane
	Renderer  bridge.Renderer
	Frames    []render.Frame
	Envelopes []protocol.Envelope
	mu        sync.Mutex
}

func NewTestHost() *TestHost {
	h := &TestHost{Control: host.NewControlPlane(host.ControlPlaneConfig{})}
	h.Renderer = render.NewPlainRenderer(render.Options{Width: 80, Height: 24, ColorMode: render.ColorModeMono, Now: func() time.Time { return DeterministicTime }, Sink: func(_ context.Context, frame render.Frame) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.Frames = append(h.Frames, frame)
		return nil
	}})
	return h
}

func (h *TestHost) OpenAndRender(ctx context.Context, descriptor surface.Descriptor, tree component.Tree) error {
	if descriptor.ID == "" {
		descriptor.ID = tree.SurfaceID
	}
	if descriptor.Kind == "" {
		descriptor.Kind = surface.KindPanel
	}
	if descriptor.Lifecycle == "" {
		descriptor.Lifecycle = surface.StateDeclared
	}
	if _, err := h.Control.RegisterAndOpen(ctx, descriptor); err != nil {
		return err
	}
	if _, err := h.Control.RenderSnapshot(ctx, descriptor.ID, tree); err != nil {
		return err
	}
	session, err := h.Renderer.OpenSurface(ctx, descriptor)
	if err != nil {
		return err
	}
	defer session.Close(ctx)
	return session.Render(ctx, tree)
}

type ProtocolSimulator struct {
	Validator protocol.PayloadValidator
	Seen      []protocol.Envelope
}

func NewProtocolSimulator() *ProtocolSimulator {
	return &ProtocolSimulator{Validator: protocol.JSONShapeValidator{MaxDepth: protocol.DefaultMaxJSONDepth}}
}

func (s *ProtocolSimulator) Dispatch(ctx context.Context, envelope protocol.Envelope) error {
	dispatcher := protocol.Dispatcher{Validator: s.Validator, Handlers: map[protocol.EnvelopeKind]protocol.Handler{}}
	for _, kind := range protocol.SupportedTransportEnvelopeKinds() {
		kind := kind
		dispatcher.Handlers[kind] = func(context.Context, protocol.Envelope) error { return nil }
	}
	if err := dispatcher.Dispatch(ctx, envelope); err != nil {
		return err
	}
	s.Seen = append(s.Seen, envelope)
	return nil
}

func (s *ProtocolSimulator) ComponentSnapshot(id string, tree component.Tree) protocol.Envelope {
	payload, _ := json.Marshal(tree)
	return protocol.NewEnvelope(protocol.EnvelopeComponentSnapshot, id, protocol.Actor{Kind: protocol.ActorExtension, ID: tree.SurfaceID}, protocol.Actor{Kind: protocol.ActorHost, ID: "test-host"}, payload)
}

func SimulatorCapabilities() []capability.Descriptor { return capability.CoreDescriptors() }
func SimulatorBlackBoxSink() observability.EventSinkDescriptor {
	return observability.BlackBoxSinkDescriptor()
}

func ApplyFixturePatch(ctx context.Context, base component.Tree, patch bridge.Patch) (component.Tree, error) {
	result, err := (reconcile.Engine{}).Apply(ctx, base, patch)
	if err != nil {
		return component.Tree{}, fmt.Errorf("apply fixture patch: %w", err)
	}
	return result, nil
}
