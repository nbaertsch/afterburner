package host

import (
	"context"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/reconcile"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type ControlPlaneConfig struct {
	Clock    Clock
	Observer *Observer
	Journal  *Journal
	Arbiter  ArbiterConfig
}

type ControlPlane struct {
	Registry *Registry
	Arbiter  *Arbiter
	Journal  *Journal
}

func NewControlPlane(cfg ControlPlaneConfig) *ControlPlane {
	clock := cfg.Clock
	if clock == nil {
		clock = realClock{}
	}
	journal := cfg.Journal
	if journal == nil {
		journal = NewJournal(clock)
	}
	registry := NewRegistry(RegistryConfig{Clock: clock, Observer: cfg.Observer, Journal: journal})
	arbiterCfg := cfg.Arbiter
	arbiterCfg.Clock = clock
	if arbiterCfg.Observer == nil {
		arbiterCfg.Observer = cfg.Observer
	}
	return &ControlPlane{Registry: registry, Arbiter: NewArbiter(arbiterCfg), Journal: journal}
}

func (c *ControlPlane) RegisterAndOpen(ctx context.Context, descriptor surface.Descriptor) (InstanceIdentity, error) {
	identity, err := c.Registry.Register(ctx, descriptor)
	if err != nil {
		return InstanceIdentity{}, err
	}
	if err := c.Registry.Open(ctx, descriptor.ID); err != nil {
		return InstanceIdentity{}, err
	}
	return identity, c.Registry.Activate(ctx, descriptor.ID)
}

func (c *ControlPlane) RenderSnapshot(ctx context.Context, surfaceID string, tree component.Tree) (reconcile.Snapshot, error) {
	return c.Registry.ApplySnapshot(ctx, surfaceID, tree)
}

func (c *ControlPlane) ApplyPatch(ctx context.Context, surfaceID string, patch bridge.Patch, opts reconcile.ApplyOptions) (reconcile.ApplyResult, error) {
	return c.Registry.ApplyPatch(ctx, surfaceID, patch, opts)
}

func (c *ControlPlane) RequestForeground(ctx context.Context, req VisibilityRequest) (ArbitrationResult, error) {
	return c.Arbiter.Request(ctx, req)
}
