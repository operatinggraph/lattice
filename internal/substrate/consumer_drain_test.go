package substrate

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// drainTestLogger discards the reconnect path's diagnostics: these vectors
// assert on what the handler saw, not on what was logged.
func drainTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// bufferedMsg is one message sitting in the client's prefetch buffer: the
// server has delivered it and counts it ack-pending, but the handler has not
// seen it yet.
type bufferedMsg struct {
	seq uint64

	mu        sync.Mutex
	decisions []string
}

func (m *bufferedMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{
		Sequence:     jetstream.SequencePair{Stream: m.seq},
		NumDelivered: 1,
	}, nil
}
func (m *bufferedMsg) Data() []byte         { return nil }
func (m *bufferedMsg) Headers() nats.Header { return nil }
func (m *bufferedMsg) Subject() string      { return "$KV.core-kv.vtx.test" }
func (m *bufferedMsg) Reply() string        { return "" }
func (m *bufferedMsg) Ack() error           { return m.record("ack") }
func (m *bufferedMsg) DoubleAck(context.Context) error {
	return m.record("ack")
}
func (m *bufferedMsg) Nak() error                       { return m.record("nak") }
func (m *bufferedMsg) NakWithDelay(time.Duration) error { return m.record("nak") }
func (m *bufferedMsg) InProgress() error                { return m.record("progress") }
func (m *bufferedMsg) Term() error                      { return m.record("term") }
func (m *bufferedMsg) TermWithReason(string) error      { return m.record("term") }

func (m *bufferedMsg) record(d string) error {
	m.mu.Lock()
	m.decisions = append(m.decisions, d)
	m.mu.Unlock()
	return nil
}

func (m *bufferedMsg) decided() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.decisions...)
}

// prefetchIterator is a jetstream.MessagesContext over a fixed prefetch buffer,
// reproducing the three behaviours of the vendored pull subscription that
// drainDurable's correctness rests on (nats.go v1.52.0 jetstream/pull.go):
// Stop() closes the iterator WITHOUT the draining flag so Next() refuses to
// hand back a buffered message (:768, :620); Drain() sets both flags so Next()
// keeps returning the buffer and reports ErrMsgIteratorClosed only once it is
// empty (:786, :646); and whichever runs second is a no-op, because both guard
// on the same closed flag.
type prefetchIterator struct {
	mu         sync.Mutex
	buffer     []*bufferedMsg
	handed     int
	failAt     int
	failed     bool
	closed     bool
	draining   bool
	drainCalls int
	closedCh   chan struct{}
	// postDrainErrs is how many Next calls after Drain() report an error
	// instead of a buffered message, and postDrainNexts counts every Next after
	// Drain() — together they drive the drain's tolerance for an error that won
	// the race with a message that was ready.
	postDrainErrs  int
	postDrainNexts int
}

// newPrefetchIterator builds an iterator over buffer that reports a receive
// error — the shape a stalled link produces, since ReportMissingHeartbeats is
// on by default — after handing back failAt messages, leaving the rest in the
// buffer. A negative failAt never fails.
func newPrefetchIterator(buffer []*bufferedMsg, failAt int) *prefetchIterator {
	return &prefetchIterator{buffer: buffer, failAt: failAt, closedCh: make(chan struct{})}
}

func (it *prefetchIterator) Next(_ ...jetstream.NextOpt) (jetstream.Msg, error) {
	for {
		it.mu.Lock()
		if it.draining {
			it.postDrainNexts++
			if it.postDrainErrs > 0 {
				it.postDrainErrs--
				it.mu.Unlock()
				return nil, jetstream.ErrNoHeartbeat
			}
		}
		switch {
		case it.closed && !it.draining:
			it.mu.Unlock()
			return nil, jetstream.ErrMsgIteratorClosed
		case !it.failed && it.failAt >= 0 && it.handed == it.failAt:
			it.failed = true
			it.mu.Unlock()
			return nil, jetstream.ErrNoHeartbeat
		case it.handed < len(it.buffer):
			m := it.buffer[it.handed]
			it.handed++
			it.mu.Unlock()
			return m, nil
		case it.draining:
			it.mu.Unlock()
			return nil, jetstream.ErrMsgIteratorClosed
		}
		// Buffer exhausted on a live iterator: the real Next blocks here until
		// a message, an error or a close arrives.
		it.mu.Unlock()
		<-it.closedCh
	}
}

func (it *prefetchIterator) Stop() {
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.closed {
		return
	}
	it.closed = true
	close(it.closedCh)
}

func (it *prefetchIterator) Drain() {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.drainCalls++
	if it.closed {
		return
	}
	it.closed = true
	it.draining = true
	close(it.closedCh)
}

func (it *prefetchIterator) drainedCalls() int {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.drainCalls
}

func (it *prefetchIterator) nextsAfterDrain() int {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.postDrainNexts
}

// resumeFloor is the contract every caller that owns its own resume position
// keeps over this primitive — internal/edge/sync's deliveryFloor is the live
// one: a cursor that never passes a sequence the handler has asked to be given
// again. It is reproduced here because it is that contract the prefetch buffer
// silently breaks, and it is the level drainDurable lives at.
type resumeFloor struct {
	pending map[uint64]struct{}
	highest uint64
}

func newResumeFloor() *resumeFloor {
	return &resumeFloor{pending: map[uint64]struct{}{}}
}

func (f *resumeFloor) hold(seq uint64) { f.pending[seq] = struct{}{} }

func (f *resumeFloor) release(seq uint64) {
	delete(f.pending, seq)
	if seq > f.highest {
		f.highest = seq
	}
}

func (f *resumeFloor) cursor() uint64 {
	floor := f.highest
	for outstanding := range f.pending {
		if outstanding-1 < floor {
			floor = outstanding - 1
		}
	}
	return floor
}

// TestDrainDurable_ReconnectHandsBackTheWholePrefetchBuffer pins the reconnect
// path's obligation to the messages the server has already delivered.
//
// Sequences 3, 4 and 5 are in the client's buffer when the iterator reports a
// receive error, and 4 is the one the handler cannot process and Naks. Abandoning
// the iterator without draining discards all three unhandled: the handler never
// learns 4 is outstanding, so when the reopened iterator delivers sequence 6 —
// which the server has never delivered and so offers immediately, while 4 waits
// out AckWait — the resume floor sails past 4. The next attach then starts above
// it, and because attaching DELETES the durable (internal/edge/transport/
// natstransport), the server-side ack floor that would have redelivered 4 is
// destroyed with it.
func TestDrainDurable_ReconnectHandsBackTheWholePrefetchBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const heldSeq = uint64(4)
	buffer := []*bufferedMsg{{seq: 1}, {seq: 2}, {seq: 3}, {seq: heldSeq}, {seq: 5}}

	floor := newResumeFloor()
	var handled []uint64
	handler := func(_ context.Context, msg Message) Decision {
		handled = append(handled, msg.Sequence)
		if msg.Sequence == heldSeq {
			floor.hold(msg.Sequence)
			return Nak
		}
		floor.release(msg.Sequence)
		return Ack
	}

	c := &Conn{}
	logger := drainTestLogger()

	// The link stalls after two messages have reached the handler; 3, 4 and 5
	// are buffered behind it.
	stalled := newPrefetchIterator(buffer, 2)
	c.drainDurable(ctx, stalled, "edge-sync-U-D", 0, 0, logger, handler)

	// The loop reopens the iterator on the same consumer and the server offers
	// the next never-delivered sequence.
	reopened := newPrefetchIterator([]*bufferedMsg{{seq: 6}}, 1)
	c.drainDurable(ctx, reopened, "edge-sync-U-D", 0, 0, logger, handler)

	if got := floor.cursor(); got >= heldSeq {
		t.Fatalf("resume floor advanced to %d, past the unresolved sequence %d: the prefetch buffer was discarded unhandled (handled %v)",
			got, heldSeq, handled)
	}
	if got, want := floor.cursor(), heldSeq-1; got != want {
		t.Fatalf("resume floor = %d, want %d (the contiguous floor below the held sequence)", got, want)
	}

	wantHandled := []uint64{1, 2, 3, 4, 5, 6}
	if len(handled) != len(wantHandled) {
		t.Fatalf("handler saw %v, want every delivered sequence %v", handled, wantHandled)
	}
	for i, seq := range wantHandled {
		if handled[i] != seq {
			t.Fatalf("handler saw %v, want %v", handled, wantHandled)
		}
	}
	for _, m := range buffer {
		if d := m.decided(); len(d) != 1 {
			t.Fatalf("sequence %d got decisions %v, want exactly one", m.seq, d)
		}
	}
	if d := buffer[3].decided(); d[0] != "nak" {
		t.Fatalf("the held sequence got %v, want a nak", d)
	}
}

// TestDrainDurable_ShutdownDiscardsThePrefetchBuffer pins the other exit. On ctx
// cancellation the loop is going away, nothing downstream can record progress
// past the buffer, and prompt teardown is what a caller waiting on shutdown
// wants — so the iterator is stopped, not drained.
func TestDrainDurable_ShutdownDiscardsThePrefetchBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buffer := []*bufferedMsg{{seq: 1}}
	handled := make(chan uint64, len(buffer))
	handler := func(_ context.Context, msg Message) Decision {
		handled <- msg.Sequence
		return Ack
	}

	// Never fails: the iterator blocks on an exhausted buffer exactly as the
	// real one does, so only the ctx watcher's Stop() can end this run.
	it := newPrefetchIterator(buffer, -1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Conn{}).drainDurable(ctx, it, "edge-sync-U-D", 0, 0, drainTestLogger(), handler)
	}()

	if got := <-handled; got != 1 {
		t.Fatalf("handler saw sequence %d first, want 1", got)
	}
	cancel()
	<-done

	if n := it.drainedCalls(); n != 0 {
		t.Fatalf("Drain called %d times on the shutdown path, want 0", n)
	}
}

// TestDrainDurable_DrainRidesOutAnErrorThatBeatAReadyMessage covers the race
// Next itself has: it selects between the message channel and the error channel,
// so a heartbeat or connection-status error can be returned while the buffer
// still holds messages. Giving up on the first one would put the discard back.
func TestDrainDurable_DrainRidesOutAnErrorThatBeatAReadyMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buffer := []*bufferedMsg{{seq: 1}, {seq: 2}, {seq: 3}}
	var handled []uint64
	handler := func(_ context.Context, msg Message) Decision {
		handled = append(handled, msg.Sequence)
		return Ack
	}

	it := newPrefetchIterator(buffer, 1)
	it.postDrainErrs = drainBufferedMaxErrs - 1
	(&Conn{}).drainDurable(ctx, it, "edge-sync-U-D", 0, 0, drainTestLogger(), handler)

	if len(handled) != len(buffer) {
		t.Fatalf("handler saw %v, want every buffered sequence", handled)
	}
}

// TestDrainDurable_DrainGivesUpOnAnErrorRun pins the other side of that
// tolerance. A link that answers every Next with an error is not going to hand
// the buffer back, and spinning against it until the whole budget expires buys
// nothing — the drain gives up after a bounded run.
func TestDrainDurable_DrainGivesUpOnAnErrorRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	it := newPrefetchIterator([]*bufferedMsg{{seq: 1}, {seq: 2}}, 1)
	it.postDrainErrs = 1_000
	started := time.Now()
	(&Conn{}).drainDurable(ctx, it, "edge-sync-U-D", 0, 0, drainTestLogger(), func(context.Context, Message) Decision {
		return Ack
	})

	if got := it.nextsAfterDrain(); got != drainBufferedMaxErrs {
		t.Fatalf("the drain made %d Next calls after Drain(), want the %d-error bound", got, drainBufferedMaxErrs)
	}
	if elapsed := time.Since(started); elapsed >= drainBufferedBudget {
		t.Fatalf("the drain ran %s, want it bounded well inside the %s budget", elapsed, drainBufferedBudget)
	}
}
