package processor

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestLaneSpecs_PerLaneBacklogIsolation proves the Fire-2 lane split end-to-end
// against a real JetStream stream: the four durables LaneSpecs declares isolate
// their backlogs (a backlog on ops.default is visible ONLY on processor-default;
// the other three lanes report zero), so Contract #5 §5.4 lane_lag is per-lane
// real, and the meta lane is serialized (MaxAckPending=1, §3.7).
//
// The durables are provisioned from LaneSpecs' own output (so the test tracks the
// spec, not a hand-rolled copy) and NumPending is read directly via cons.Info —
// no supervised pump runs, so the measurement is deterministic (a running pump
// pre-fetches the backlog into the client and would hide it).
func TestLaneSpecs_PerLaneBacklogIsolation(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "lane-split-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	provisionHarness(t, ctx, conn)

	js := conn.JetStream()
	noop := func(context.Context, substrate.Message) (substrate.Decision, error) {
		return substrate.Ack, nil
	}

	// Provision each lane durable from its LaneSpecs entry (FilterSubject +
	// MaxAckPending exactly as the production supervisor would create it).
	specsByDurable := map[string]substrate.ConsumerSpec{}
	for _, spec := range LaneSpecs(testStream, noop, 30*time.Second, nil, nil) {
		specsByDurable[spec.Name] = spec
		if _, cerr := js.CreateOrUpdateConsumer(ctx, testStream, jetstream.ConsumerConfig{
			Durable:       spec.Name,
			FilterSubject: spec.FilterSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			MaxAckPending: spec.MaxAckPending,
		}); cerr != nil {
			t.Fatalf("create %q: %v", spec.Name, cerr)
		}
	}

	durables := LaneDurables()

	// Publish a backlog to ops.default only. Publish (JetStream, store-acked)
	// guarantees each message is persisted before the read below.
	const n = 6
	for i := 0; i < n; i++ {
		if err := conn.Publish(ctx, "ops.default", []byte(`{"requestId":"x","operationType":"Noop"}`), nil); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	pending := func(durable string) uint64 {
		t.Helper()
		cons, err := js.Consumer(ctx, testStream, durable)
		if err != nil {
			t.Fatalf("consumer %q: %v", durable, err)
		}
		info, err := cons.Info(ctx)
		if err != nil {
			t.Fatalf("info %q: %v", durable, err)
		}
		return info.NumPending
	}

	// default carries the whole backlog…
	if got := pending(durables["default"]); got != n {
		t.Fatalf("processor-default NumPending = %d, want %d", got, n)
	}
	// …and the backlog does NOT leak to any other lane (isolation).
	for _, lane := range []string{"urgent", "system", "meta"} {
		if got := pending(durables[lane]); got != 0 {
			t.Fatalf("lane %q NumPending = %d, want 0 (a default backlog must not appear on other lanes)", lane, got)
		}
	}

	// The meta lane is serialized server-side (Contract #2 §3.7).
	metaCons, err := js.Consumer(ctx, testStream, durables["meta"])
	if err != nil {
		t.Fatalf("meta consumer: %v", err)
	}
	metaInfo, err := metaCons.Info(ctx)
	if err != nil {
		t.Fatalf("meta info: %v", err)
	}
	if metaInfo.Config.MaxAckPending != 1 {
		t.Fatalf("meta MaxAckPending = %d, want 1 (serialized)", metaInfo.Config.MaxAckPending)
	}
	if specsByDurable[durables["meta"]].MaxAckPending != 1 {
		t.Fatalf("LaneSpecs meta MaxAckPending = %d, want 1", specsByDurable[durables["meta"]].MaxAckPending)
	}
}
