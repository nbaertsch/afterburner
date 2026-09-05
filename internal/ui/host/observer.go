package host

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/observability"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type ObservationKind string

const (
	ObservationLifecycle   = "ui.host.lifecycle"
	ObservationArbitration = "ui.host.arbitration"
	ObservationPatch       = "ui.host.patch"
	ObservationQuota       = "ui.host.quota"
	ObservationRecovery    = "ui.host.recovery"
)

type ObservationSink interface {
	Publish(ctx context.Context, observation observability.Observation) error
}

type Observer struct {
	Sink      ObservationSink
	HostID    string
	Disabled  atomic.Bool
	failCount atomic.Uint64
}

func NewObserver(sink ObservationSink, hostID string) *Observer {
	return &Observer{Sink: sink, HostID: hostID}
}

func (o *Observer) Observe(ctx context.Context, kind ObservationKind, attrs map[string]string) {
	if o == nil || o.Sink == nil || o.Disabled.Load() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	values := map[string]json.RawMessage{}
	for key, value := range attrs {
		encoded, _ := json.Marshal(value)
		values[key] = encoded
	}
	if o.HostID != "" {
		encoded, _ := json.Marshal(o.HostID)
		values["hostId"] = encoded
	}
	observation := observability.Observation{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, Type: string(kind), SinkID: observability.BlackBoxSinkID, At: time.Now().UTC(), Attributes: values}
	if err := o.Sink.Publish(ctx, observation); err != nil {
		o.failCount.Add(1)
	}
}

func (o *Observer) Failures() uint64 {
	if o == nil {
		return 0
	}
	return o.failCount.Load()
}
