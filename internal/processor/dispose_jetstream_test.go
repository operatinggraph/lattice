package processor

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// disposeRecordingMsg is a minimal jetstream.Msg fake that records which
// acknowledgement method disposeJetstream called and, for NakWithDelay, the
// exact delay it was given.
type disposeRecordingMsg struct {
	mu       sync.Mutex
	decision string
	delay    time.Duration
}

func (m *disposeRecordingMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *disposeRecordingMsg) Data() []byte                              { return nil }
func (m *disposeRecordingMsg) Headers() nats.Header                      { return nil }
func (m *disposeRecordingMsg) Subject() string                           { return "$KV.core-kv.vtx.test" }
func (m *disposeRecordingMsg) Reply() string                             { return "" }
func (m *disposeRecordingMsg) Ack() error                                { return m.record("ack", 0) }
func (m *disposeRecordingMsg) DoubleAck(context.Context) error           { return m.record("ack", 0) }
func (m *disposeRecordingMsg) Nak() error                                { return m.record("nak", 0) }
func (m *disposeRecordingMsg) NakWithDelay(d time.Duration) error {
	return m.record("nak-with-delay", d)
}
func (m *disposeRecordingMsg) InProgress() error           { return m.record("progress", 0) }
func (m *disposeRecordingMsg) Term() error                 { return m.record("term", 0) }
func (m *disposeRecordingMsg) TermWithReason(string) error { return m.record("term", 0) }

func (m *disposeRecordingMsg) record(decision string, delay time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decision = decision
	m.delay = delay
	return nil
}

func (m *disposeRecordingMsg) snapshot() (string, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decision, m.delay
}

// disposeTestCommitPath builds a CommitPath with only what disposeJetstream
// reads (deps.Logger, and deps.AckerFactory for the Ack arm), bypassing
// NewCommitPath's required-field panics — this test exercises the
// disposition switch in isolation, not the full pipeline.
func disposeTestCommitPath() *CommitPath {
	return &CommitPath{deps: Deps{
		Logger:       slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		AckerFactory: DefaultAckerFactory,
	}}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestDisposeJetstream_NakWithLongDelay_RoutesToNakWithDelay proves
// disposeJetstream's explicit NakWithLongDelay case sends the decision to
// msg.NakWithDelay(DefaultLongRedeliveryDelay), never to the default arm's
// Ack. The default arm Acks, so a missing case here is a SILENT no-op, not a
// compile error — this is the vector that reds when the case is removed.
func TestDisposeJetstream_NakWithLongDelay_RoutesToNakWithDelay(t *testing.T) {
	cp := disposeTestCommitPath()
	m := &disposeRecordingMsg{}
	cp.disposeJetstream(context.Background(), substrate.NakWithLongDelay, m)

	decision, delay := m.snapshot()
	if decision != "nak-with-delay" {
		t.Fatalf("NakWithLongDelay routed to %q, want nak-with-delay (the default arm Acks — a missing case is a silent no-op)", decision)
	}
	if delay != substrate.DefaultLongRedeliveryDelay {
		t.Fatalf("delay = %v, want %v", delay, substrate.DefaultLongRedeliveryDelay)
	}
}

// TestDisposeJetstream_NakWithDelay_StillWorks is the adjacent positive
// vector: disposeJetstream's pre-existing NakWithDelay case is unaffected by
// the new one.
func TestDisposeJetstream_NakWithDelay_StillWorks(t *testing.T) {
	cp := disposeTestCommitPath()
	m := &disposeRecordingMsg{}
	cp.disposeJetstream(context.Background(), substrate.NakWithDelay, m)

	decision, delay := m.snapshot()
	if decision != "nak-with-delay" {
		t.Fatalf("NakWithDelay routed to %q, want nak-with-delay", decision)
	}
	if delay != substrate.DefaultRedeliveryDelay {
		t.Fatalf("delay = %v, want %v", delay, substrate.DefaultRedeliveryDelay)
	}
}
