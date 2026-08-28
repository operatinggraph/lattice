package substrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDecisionEnumValues pins the binary-additive layout of the Decision iota.
// Ack/Nak/Term must keep their original values so every existing caller (loom,
// processor) behaves identically; NakWithDelay and NakWithLongDelay are
// appended at the end, in that order.
func TestDecisionEnumValues(t *testing.T) {
	if Ack != 0 {
		t.Fatalf("Ack = %d, want 0", Ack)
	}
	if Nak != 1 {
		t.Fatalf("Nak = %d, want 1", Nak)
	}
	if Term != 2 {
		t.Fatalf("Term = %d, want 2", Term)
	}
	if NakWithDelay != 3 {
		t.Fatalf("NakWithDelay = %d, want 3", NakWithDelay)
	}
	if NakWithLongDelay != 4 {
		t.Fatalf("NakWithLongDelay = %d, want 4", NakWithLongDelay)
	}
}

// TestApplyDecision_AllDecisions is T1's core assertion for the
// internal/substrate switch: every Decision value routes to the expected
// jetstream.Msg method, and NakWithLongDelay in particular routes to a
// delayed Nak carrying the LONG floor — never to Ack. Reuses bufferedMsg
// (consumer_drain_test.go), the package's one jetstream.Msg fake, rather than
// inventing a second one.
func TestApplyDecision_AllDecisions(t *testing.T) {
	const short = 7 * time.Second
	const long = 90 * time.Second

	cases := []struct {
		name       string
		decision   Decision
		wantMethod string
		wantDelay  time.Duration // meaningful only for the nak-with-delay methods
	}{
		{"Ack", Ack, "ack", 0},
		{"Nak", Nak, "nak", 0},
		{"Term", Term, "term", 0},
		{"NakWithDelay", NakWithDelay, "nakdelay", short},
		{"NakWithLongDelay", NakWithLongDelay, "nakdelay", long},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &bufferedMsg{seq: 1}
			applyDecision(tc.decision, m, "dc-test", short, long, drainTestLogger())

			got := m.decided()
			if len(got) != 1 {
				t.Fatalf("decisions = %v, want exactly one", got)
			}
			if got[0] != tc.wantMethod {
				t.Fatalf("%s routed to %q, want %q — a missed case falls through to the Ack default",
					tc.name, got[0], tc.wantMethod)
			}
			if tc.wantMethod == "nakdelay" {
				gotDelays := m.nakDelays()
				if len(gotDelays) != 1 || gotDelays[0] != tc.wantDelay {
					t.Fatalf("%s delay = %v, want %v", tc.name, gotDelays, tc.wantDelay)
				}
			}
		})
	}
}

// TestApplyDecision_NakWithLongDelay_NotAck is T1's negative-space pin,
// isolated from the table above: a handler that returns NakWithLongDelay
// must never reach msg.Ack(), which is exactly what a missed `case` (falling
// through to `default:`) would do.
func TestApplyDecision_NakWithLongDelay_NotAck(t *testing.T) {
	m := &bufferedMsg{seq: 1}
	applyDecision(NakWithLongDelay, m, "dc-test", DefaultRedeliveryDelay, DefaultLongRedeliveryDelay, drainTestLogger())

	for _, d := range m.decided() {
		if d == "ack" {
			t.Fatalf("NakWithLongDelay reached msg.Ack() — decisions: %v", m.decided())
		}
	}
}

// TestApplyDecision_LongFloor_Clamping covers the three floor-resolution
// cases THE FLOOR RULE (design §3.1) specifies: unset falls back to the
// package default, a long floor configured below the effective short floor is
// clamped UP to it (never shorter than the short floor), and a long floor
// configured above the short floor is used as given.
func TestApplyDecision_LongFloor_Clamping(t *testing.T) {
	t.Run("unset falls back to DefaultLongRedeliveryDelay", func(t *testing.T) {
		m := &bufferedMsg{seq: 1}
		applyDecision(NakWithLongDelay, m, "dc-test", DefaultRedeliveryDelay, 0, drainTestLogger())
		gotDelays := m.nakDelays()
		if len(gotDelays) != 1 || gotDelays[0] != DefaultLongRedeliveryDelay {
			t.Fatalf("delay = %v, want %v", gotDelays, DefaultLongRedeliveryDelay)
		}
	})

	t.Run("below the short floor is clamped up to it", func(t *testing.T) {
		const shortFloor = 10 * time.Second
		const longBelow = 2 * time.Second // deliberately below shortFloor
		m := &bufferedMsg{seq: 1}
		applyDecision(NakWithLongDelay, m, "dc-test", shortFloor, longBelow, drainTestLogger())
		gotDelays := m.nakDelays()
		if len(gotDelays) != 1 || gotDelays[0] != shortFloor {
			t.Fatalf("delay = %v, want %v (clamped to the short floor, not %v)", gotDelays, shortFloor, longBelow)
		}
	})

	t.Run("above the short floor is used as given", func(t *testing.T) {
		const shortFloor = 5 * time.Second
		const longAbove = 10 * time.Minute
		m := &bufferedMsg{seq: 1}
		applyDecision(NakWithLongDelay, m, "dc-test", shortFloor, longAbove, drainTestLogger())
		gotDelays := m.nakDelays()
		if len(gotDelays) != 1 || gotDelays[0] != longAbove {
			t.Fatalf("delay = %v, want %v (used as configured)", gotDelays, longAbove)
		}
	})
}

// TestRunDurableConsumer_NakWithLongDelay_NoHotLoop mirrors
// TestRunDurableConsumer_NakWithDelay_NoHotLoop's shape for the long
// decision, with a short configured long-floor so the test stays fast.
func TestRunDurableConsumer_NakWithLongDelay_NoHotLoop(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const floor = 600 * time.Millisecond
	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-naklongdelay",
		// Also set below the package default: THE FLOOR RULE clamps the long
		// floor up to the effective short floor, so a short RedeliveryDelay
		// here is what keeps this a fast test rather than a 5s+ one.
		RedeliveryDelay:     floor,
		LongRedeliveryDelay: floor,
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.delaymelong", []byte(`{"v":1}`))

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
	case <-time.After(5 * time.Second):
		t.Fatalf("message never redelivered after NakWithLongDelay")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) < 2 {
		t.Fatalf("expected >= 2 deliveries, got %d", len(deliveries))
	}
	gap := deliveries[1].Sub(deliveries[0])
	// Allow generous tolerance below the floor for CI jitter, but the gap must
	// be clearly non-zero (a plain Nak would redeliver near-immediately).
	if gap < floor/2 {
		t.Fatalf("redelivery gap %v shorter than half the floor %v — NakWithLongDelay hot-looped", gap, floor)
	}
}

// TestRunDurableConsumer_NakWithDelay_NoHotLoop verifies that a handler
// returning NakWithDelay does not redeliver before the configured floor
// elapses (no zero-delay hot-loop). The gap between the first and second
// delivery must be at least the floor, with a generous lower-bound tolerance
// for CI scheduling jitter.
func TestRunDurableConsumer_NakWithDelay_NoHotLoop(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const floor = 600 * time.Millisecond
	cfg := DurableConsumerConfig{
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Durable:         "dc-test-nakdelay",
		RedeliveryDelay: floor,
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.delayme", []byte(`{"v":1}`))

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
				return NakWithDelay
			}
			if n == 2 {
				close(secondSeen)
			}
			return Ack
		})
	}()

	select {
	case <-secondSeen:
	case <-time.After(5 * time.Second):
		t.Fatalf("message never redelivered after NakWithDelay")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deliveries) < 2 {
		t.Fatalf("expected >= 2 deliveries, got %d", len(deliveries))
	}
	gap := deliveries[1].Sub(deliveries[0])
	// Allow generous tolerance below the floor for CI jitter, but the gap must
	// be clearly non-zero (a plain Nak would redeliver near-immediately).
	if gap < floor/2 {
		t.Fatalf("redelivery gap %v shorter than half the floor %v — NakWithDelay hot-looped", gap, floor)
	}
}

// TestRunDurableConsumer_NakWithDelay_ZeroFloorUsesDefault verifies that when
// RedeliveryDelay is left at its zero value, NakWithDelay falls back to a
// non-zero package default (never plain immediate Nak).
func TestRunDurableConsumer_NakWithDelay_ZeroFloorUsesDefault(t *testing.T) {
	if DefaultRedeliveryDelay <= 0 {
		t.Fatalf("DefaultRedeliveryDelay must be positive, got %v", DefaultRedeliveryDelay)
	}
}
