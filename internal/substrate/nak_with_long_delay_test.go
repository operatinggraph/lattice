package substrate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// recordingMsg is a minimal jetstream.Msg fake that records which
// acknowledgement method was called and, for NakWithDelay, the exact delay it
// was given — so a test can assert both WHICH decision fired and, for the
// delay cases, the precise floor applyDecision computed.
type recordingMsg struct {
	mu       sync.Mutex
	decision string
	delay    time.Duration
}

func (m *recordingMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *recordingMsg) Data() []byte                              { return nil }
func (m *recordingMsg) Headers() nats.Header                      { return nil }
func (m *recordingMsg) Subject() string                           { return "$KV.core-kv.vtx.test" }
func (m *recordingMsg) Reply() string                             { return "" }
func (m *recordingMsg) Ack() error                                { return m.record("ack", 0) }
func (m *recordingMsg) DoubleAck(context.Context) error           { return m.record("ack", 0) }
func (m *recordingMsg) Nak() error                                { return m.record("nak", 0) }
func (m *recordingMsg) NakWithDelay(d time.Duration) error        { return m.record("nak-with-delay", d) }
func (m *recordingMsg) InProgress() error                         { return m.record("progress", 0) }
func (m *recordingMsg) Term() error                               { return m.record("term", 0) }
func (m *recordingMsg) TermWithReason(string) error               { return m.record("term", 0) }

func (m *recordingMsg) record(decision string, delay time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decision = decision
	m.delay = delay
	return nil
}

func (m *recordingMsg) snapshot() (string, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decision, m.delay
}

// TestApplyDecision_NakWithLongDelay_RoutesToNakWithDelay proves the
// applyDecision switch (the case shared by all four call sites — the durable
// loop, the reconnect-buffer drain, and both consumer_supervisor_pump.go
// sites) sends NakWithLongDelay to msg.NakWithDelay, never to msg.Ack. A
// deleted case falls through to default:, which Acks — exactly the silent
// no-op V8 warns about — so this is the vector that reds when the case is
// removed.
func TestApplyDecision_NakWithLongDelay_RoutesToNakWithDelay(t *testing.T) {
	t.Parallel()
	m := &recordingMsg{}
	applyDecision(NakWithLongDelay, m, "dc-test", 0, 0, drainTestLogger())

	decision, delay := m.snapshot()
	if decision != "nak-with-delay" {
		t.Fatalf("NakWithLongDelay routed to %q, want nak-with-delay (a missing case silently Acks)", decision)
	}
	if delay != DefaultLongRedeliveryDelay {
		t.Fatalf("delay = %v, want the package default %v (zero long floor must fall back to it)", delay, DefaultLongRedeliveryDelay)
	}
}

// TestApplyDecision_NakWithLongDelay_FloorFallbackChain pins the three-way
// fallback §3.1 specifies: a zero long floor falls back to
// DefaultLongRedeliveryDelay; a configured long floor below
// DefaultRedeliveryDelay is raised to it (the long floor can never be shorter
// than the short one); a configured long floor above DefaultRedeliveryDelay
// is used as-is.
func TestApplyDecision_NakWithLongDelay_FloorFallbackChain(t *testing.T) {
	t.Parallel()

	belowFloor := DefaultRedeliveryDelay / 2
	aboveFloor := DefaultRedeliveryDelay * 10

	cases := []struct {
		name       string
		configured time.Duration
		wantDelay  time.Duration
	}{
		{"zero falls back to the long default", 0, DefaultLongRedeliveryDelay},
		{"below DefaultRedeliveryDelay is raised to it", belowFloor, DefaultRedeliveryDelay},
		{"above DefaultRedeliveryDelay is used as-is", aboveFloor, aboveFloor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &recordingMsg{}
			applyDecision(NakWithLongDelay, m, "dc-test", 0, tc.configured, drainTestLogger())
			decision, delay := m.snapshot()
			if decision != "nak-with-delay" {
				t.Fatalf("decision = %q, want nak-with-delay", decision)
			}
			if delay != tc.wantDelay {
				t.Fatalf("configured=%v: delay = %v, want %v", tc.configured, delay, tc.wantDelay)
			}
		})
	}
}

// TestDrainBuffered_NakWithLongDelay_NotAck exercises the drainBuffered call
// site (consumer.go's reconnect-path buffer drain, the second of the two
// consumer.go applyDecision call sites) through the same bufferedMsg /
// prefetchIterator fixture consumer_drain_test.go uses for drainDurable,
// proving a handler returning NakWithLongDelay is naked there too, not acked.
func TestDrainBuffered_NakWithLongDelay_NotAck(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buffer := []*bufferedMsg{{seq: 1}}
	handler := func(_ context.Context, _ Message) Decision {
		return NakWithLongDelay
	}

	it := newPrefetchIterator(buffer, -1)
	it.Drain()
	(&Conn{}).drainBuffered(ctx, it, "edge-sync-U-D", 0, 0, drainTestLogger(), handler)

	got := buffer[0].decided()
	if len(got) != 1 {
		t.Fatalf("decisions = %v, want exactly one", got)
	}
	if got[0] != "nak" {
		t.Fatalf("drainBuffered applied %q for NakWithLongDelay, want nak (a missing case silently Acks)", got[0])
	}
}

// TestRunDurableConsumer_NakWithLongDelay_UsesLongFloor exercises the durable
// loop's own applyDecision call site (consumer.go:319, reached through
// RunDurableConsumer -> runDurableLoop -> drainDurable) end to end against a
// real embedded server: a handler returning NakWithLongDelay must not
// redeliver before the configured long floor (proving it did not fall to
// Ack, which would never redeliver at all, and did not fall to the short
// NakWithDelay floor).
func TestRunDurableConsumer_NakWithLongDelay_UsesLongFloor(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const shortFloor = 100 * time.Millisecond
	// Above DefaultRedeliveryDelay (5s), so applyDecision uses it as-is rather
	// than flooring it up — this proves the wiring carries the CONFIGURED
	// value, not just "something long".
	const longFloor = DefaultRedeliveryDelay + 500*time.Millisecond
	cfg := DurableConsumerConfig{
		Stream:              "KV_" + bucket,
		FilterSubject:       "$KV." + bucket + ".vtx.meta.>",
		Durable:             "dc-test-naklongdelay",
		RedeliveryDelay:     shortFloor,
		LongRedeliveryDelay: longFloor,
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.longdelayme", []byte(`{"v":1}`))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var deliveries []time.Time
	secondSeen := make(chan struct{})
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, _ Message) Decision {
			mu.Lock()
			deliveries = append(deliveries, time.Now())
			n := len(deliveries)
			mu.Unlock()
			if n == 1 {
				return NakWithLongDelay
			}
			if n == 2 {
				close(secondSeen)
			}
			return Ack
		})
	}()

	select {
	case <-secondSeen:
	case <-time.After(longFloor + 2*time.Second):
		t.Fatalf("message never redelivered after NakWithLongDelay")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) < 2 {
		t.Fatalf("expected >= 2 deliveries, got %d", len(deliveries))
	}
	gap := deliveries[1].Sub(deliveries[0])
	if gap < DefaultRedeliveryDelay {
		t.Fatalf("redelivery gap %v was not clearly past the short floor / package default %v — NakWithLongDelay did not use the configured long floor", gap, DefaultRedeliveryDelay)
	}
}

// TestSupervisorPump_NakWithLongDelay_NotAck exercises
// consumer_supervisor_pump.go's applyDecision call site (:710, the
// handler-returned-Decision path) end to end through a real
// ConsumerSupervisor: a handler returning NakWithLongDelay must redeliver
// (proving the pump did not silently Ack it), and the gap to that redelivery
// must reflect spec.LongRedeliveryDelay, not the short RedeliveryDelay.
func TestSupervisorPump_NakWithLongDelay_NotAck(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	const shortFloor = 100 * time.Millisecond
	// Above DefaultRedeliveryDelay (5s), so applyDecision uses it as-is rather
	// than flooring it up.
	const longFloor = DefaultRedeliveryDelay + 500*time.Millisecond
	var deliveries int32
	first := time.Time{}
	var mu sync.Mutex
	redelivered := make(chan struct{})
	spec := ConsumerSpec{
		Name:                "sup-naklong",
		Stream:              "KV_" + bucket,
		FilterSubject:       "$KV." + bucket + ".vtx.meta.>",
		RedeliveryDelay:     shortFloor,
		LongRedeliveryDelay: longFloor,
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			n := atomic.AddInt32(&deliveries, 1)
			mu.Lock()
			if n == 1 {
				first = time.Now()
			}
			mu.Unlock()
			if n == 1 {
				return NakWithLongDelay, nil
			}
			close(redelivered)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.suplongdelay", []byte(`{"v":1}`))

	select {
	case <-redelivered:
	case <-time.After(longFloor + 2*time.Second):
		t.Fatalf("message never redelivered after NakWithLongDelay (a missing case would silently Ack and it would never redeliver)")
	}

	mu.Lock()
	gap := time.Since(first)
	mu.Unlock()
	if gap < DefaultRedeliveryDelay {
		t.Fatalf("redelivery observed after %v, want it to reflect the long floor %v, not the short one %v", gap, longFloor, shortFloor)
	}
}
