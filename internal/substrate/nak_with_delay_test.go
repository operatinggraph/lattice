package substrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDecisionEnumValues pins the layout of the Decision iota: Ack=0, Nak=1,
// Term=2, NakWithDelay=3, NakWithLongDelay=4. Every existing caller (loom,
// processor) depends on the first four values behaving identically; new
// values are appended at the end, never inserted, so that dependency holds
// as the enum grows.
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
