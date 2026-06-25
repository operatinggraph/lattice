package loom

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
)

// TestRelay_NakWithDelayDoesNotHotLoop proves AC #3/#5: when the relay's publish
// fails (no ops stream), the handler returns NakWithDelay and redelivery does
// not arrive before the DefaultRedeliveryDelay floor (5s) — it does not hot-loop
// at zero delay. The relay durable is driven through a real ConsumerSupervisor.
func TestRelay_NakWithDelayDoesNotHotLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	const bucket = "loom-state"
	js := conn.JetStream()
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	stream, err := js.Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, cfg)
	require.NoError(t, err)
	// Deliberately NO ops.> stream — every relay publish fails.

	r := newRelay(conn, bucket, testRelayLogger())

	var deliveries int64
	countingHandler := func(hctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		atomic.AddInt64(&deliveries, 1)
		return r.handle(hctx, msg)
	}

	sup := substrate.NewConsumerSupervisor(conn)
	spec := substrate.ConsumerSpec{
		Name:          relayDurable,
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + "." + outboxPrefix + ">",
		DeliverPolicy: substrate.DeliverAll,
		Handler:       countingHandler,
		Logger:        testRelayLogger(),
	}
	require.NoError(t, sup.Add(ctx, spec))
	defer sup.Stop()

	// Write one outbox record — the relay will try to publish it, fail, and
	// NakWithDelay.
	rec := outboxRecord{RequestID: "req-1", Operation: "DoThing", Lane: "system", Actor: "vtx.identity.x"}
	body, _ := json.Marshal(rec)
	_, err = conn.KVPut(ctx, bucket, outboxKey(rec.RequestID), body)
	require.NoError(t, err)

	// Within a window well under the 5s floor, the handler must be invoked only a
	// small bounded number of times (first delivery + no immediate redelivery).
	time.Sleep(3 * time.Second)
	n := atomic.LoadInt64(&deliveries)
	require.LessOrEqual(t, n, int64(2),
		"NakWithDelay must not hot-loop: at most ~1 delivery before the 5s floor, got %d", n)
	require.GreaterOrEqual(t, n, int64(1), "the outbox record should have been delivered at least once")
}

func testRelayLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
