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

// disposeTestMsg is a minimal jetstream.Msg fake recording which method
// disposeJetstream invoked and, for NakWithDelay, the delay it was given.
// disposeJetstream never reads Metadata/Data/Headers/Subject/Reply, so those
// return zero values.
type disposeTestMsg struct {
	mu        sync.Mutex
	decisions []string
	delays    []time.Duration
}

func (m *disposeTestMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *disposeTestMsg) Data() []byte                              { return nil }
func (m *disposeTestMsg) Headers() nats.Header                      { return nil }
func (m *disposeTestMsg) Subject() string                           { return "" }
func (m *disposeTestMsg) Reply() string                             { return "" }
func (m *disposeTestMsg) Ack() error                                { return m.record("ack") }
func (m *disposeTestMsg) DoubleAck(context.Context) error           { return m.record("ack") }
func (m *disposeTestMsg) Nak() error                                { return m.record("nak") }
func (m *disposeTestMsg) NakWithDelay(d time.Duration) error {
	m.mu.Lock()
	m.delays = append(m.delays, d)
	m.mu.Unlock()
	return m.record("nakdelay")
}
func (m *disposeTestMsg) InProgress() error           { return m.record("progress") }
func (m *disposeTestMsg) Term() error                 { return m.record("term") }
func (m *disposeTestMsg) TermWithReason(string) error { return m.record("term") }

func (m *disposeTestMsg) record(d string) error {
	m.mu.Lock()
	m.decisions = append(m.decisions, d)
	m.mu.Unlock()
	return nil
}

func (m *disposeTestMsg) decided() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.decisions...)
}

func (m *disposeTestMsg) nakDelays() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.delays...)
}

// panicAcker fails the test loudly if disposeJetstream ever reaches the
// step-9 Acker boundary for a non-Ack Decision — exactly what a missed
// `case` (falling through to the `default:` Ack arm) would do.
type panicAcker struct{ t *testing.T }

func (a panicAcker) Ack(context.Context) error {
	a.t.Fatalf("disposeJetstream reached the Acker for a non-Ack Decision")
	return nil
}

// recordingAcker is the Ack-case control: it lets the Ack Decision reach the
// Acker (as it must) and records that it was called.
type recordingAcker struct{ called *bool }

func (a recordingAcker) Ack(context.Context) error {
	*a.called = true
	return nil
}

// newDisposeTestCommitPath builds a CommitPath with only the two fields
// disposeJetstream reads: Logger, and an AckerFactory that fails the test if
// invoked for a non-Ack Decision. Bypasses NewCommitPath's
// Conn/CoreBucket/Authorizer/Committer panics, which disposeJetstream never
// touches.
func newDisposeTestCommitPath(t *testing.T) *CommitPath {
	return &CommitPath{deps: Deps{
		Logger: slog.Default(),
		AckerFactory: func(jetstream.Msg, *slog.Logger) Acker {
			return panicAcker{t: t}
		},
	}}
}

// TestDisposeJetstream_AllDecisions is T1's core assertion for the
// internal/processor switch: every Decision value routes to the expected
// jetstream.Msg method, and NakWithLongDelay in particular routes to a
// delayed Nak carrying substrate.DefaultLongRedeliveryDelay — never to the
// step-9 Acker.
func TestDisposeJetstream_AllDecisions(t *testing.T) {
	cases := []struct {
		name       string
		decision   substrate.Decision
		wantMethod string
		wantDelay  time.Duration
	}{
		{"Ack", substrate.Ack, "ack", 0},
		{"Nak", substrate.Nak, "nak", 0},
		{"Term", substrate.Term, "term", 0},
		{"NakWithDelay", substrate.NakWithDelay, "nakdelay", substrate.DefaultRedeliveryDelay},
		{"NakWithLongDelay", substrate.NakWithLongDelay, "nakdelay", substrate.DefaultLongRedeliveryDelay},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newDisposeTestCommitPath(t)
			acked := false
			if tc.decision == substrate.Ack {
				// Ack is the one Decision that MUST reach the Acker (the
				// step-9 boundary) — swap in a control that permits it and
				// records the call, rather than the panicking default.
				cp.deps.AckerFactory = func(jetstream.Msg, *slog.Logger) Acker {
					return recordingAcker{called: &acked}
				}
			}
			msg := &disposeTestMsg{}
			cp.disposeJetstream(context.Background(), tc.decision, msg)

			if tc.decision == substrate.Ack {
				if !acked {
					t.Fatalf("Ack did not reach the Acker")
				}
				return
			}

			got := msg.decided()
			if len(got) != 1 {
				t.Fatalf("decisions = %v, want exactly one", got)
			}
			if got[0] != tc.wantMethod {
				t.Fatalf("%s routed to %q, want %q — a missed case falls through to the Ack default",
					tc.name, got[0], tc.wantMethod)
			}
			if tc.wantMethod == "nakdelay" {
				gotDelays := msg.nakDelays()
				if len(gotDelays) != 1 || gotDelays[0] != tc.wantDelay {
					t.Fatalf("%s delay = %v, want %v", tc.name, gotDelays, tc.wantDelay)
				}
			}
		})
	}
}

// TestDisposeJetstream_NakWithLongDelay_NotAcked is T1's negative-space pin
// for the processor switch, isolated from the table above: NakWithLongDelay
// must call msg.NakWithDelay and must never reach the AckerFactory.
func TestDisposeJetstream_NakWithLongDelay_NotAcked(t *testing.T) {
	cp := newDisposeTestCommitPath(t)
	msg := &disposeTestMsg{}
	cp.disposeJetstream(context.Background(), substrate.NakWithLongDelay, msg)

	got := msg.decided()
	if len(got) != 1 || got[0] != "nakdelay" {
		t.Fatalf("decisions = %v, want exactly one nakdelay", got)
	}
	gotDelays := msg.nakDelays()
	if len(gotDelays) != 1 || gotDelays[0] != substrate.DefaultLongRedeliveryDelay {
		t.Fatalf("delay = %v, want %v", gotDelays, substrate.DefaultLongRedeliveryDelay)
	}
}
