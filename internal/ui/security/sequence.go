package security

import (
	"sort"
	"sync"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

type SequenceTracker struct {
	mu      sync.Mutex
	streams map[string]*sequenceState
	window  uint64
}

type sequenceState struct {
	epoch     uint64
	highWater uint64
	pending   map[uint64]bool
}

func NewSequenceTracker(window uint64) *SequenceTracker {
	if window == 0 {
		window = 1024
	}
	return &SequenceTracker{streams: map[string]*sequenceState{}, window: window}
}

func (t *SequenceTracker) Accept(streamID string, epoch, sequence uint64) (protocol.SequenceAck, error) {
	if streamID == "" {
		streamID = "default"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.streams == nil {
		t.streams = map[string]*sequenceState{}
	}
	state := t.streams[streamID]
	if state == nil {
		state = &sequenceState{epoch: epoch, pending: map[uint64]bool{}}
		t.streams[streamID] = state
	}
	if epoch < state.epoch {
		return state.ack(true, false), protocol.StructuredError{Code: protocol.ErrorStaleEpoch, Message: "sequence epoch is stale", Recoverable: false, Target: "epoch"}
	}
	if epoch > state.epoch {
		state.epoch = epoch
		state.highWater = 0
		state.pending = map[uint64]bool{}
	}
	if sequence <= state.highWater || state.pending[sequence] {
		ack := state.ack(true, false)
		return ack, protocol.StructuredError{Code: protocol.ErrorStaleSequence, Message: "duplicate or stale sequence rejected", Recoverable: false, Target: "sequence"}
	}
	if sequence > state.highWater+t.window {
		return state.ack(false, true), protocol.StructuredError{Code: protocol.ErrorStaleSequence, Message: "sequence is outside receive window", Recoverable: true, Target: "sequence"}
	}
	if sequence == state.highWater+1 {
		state.highWater = sequence
		for state.pending[state.highWater+1] {
			delete(state.pending, state.highWater+1)
			state.highWater++
		}
	} else {
		state.pending[sequence] = true
	}
	return state.ack(false, false), nil
}

func (s *sequenceState) ack(duplicate, outOfWindow bool) protocol.SequenceAck {
	received := make([]uint64, 0, len(s.pending))
	for sequence := range s.pending {
		received = append(received, sequence)
	}
	sort.Slice(received, func(i, j int) bool { return received[i] < received[j] })
	missing := []uint64{}
	if len(received) > 0 {
		for sequence := s.highWater + 1; sequence < received[len(received)-1]; sequence++ {
			if !s.pending[sequence] {
				missing = append(missing, sequence)
			}
		}
	}
	return protocol.SequenceAck{Epoch: s.epoch, HighWater: s.highWater, Received: received, Missing: missing, Contiguous: len(s.pending) == 0, Duplicate: duplicate, OutOfWindow: outOfWindow}
}
