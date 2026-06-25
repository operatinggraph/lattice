package loom

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// TestPatternSource_StartPrunesStalePriorBootDurable proves the fix for the
// per-boot durable leak: a durable left behind by a prior boot under the
// loom-pattern-source-<instance> prefix is deleted when a new instance's
// patternSource.start runs, and the new instance's own durable is deleted in
// turn once its context is cancelled (clean shutdown), so it does not become
// the next boot's stale entry.
func TestPatternSource_StartPrunesStalePriorBootDurable(t *testing.T) {
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

	const bucket = "core-kv"
	js := conn.JetStream()
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	stream, err := js.Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, cfg)
	require.NoError(t, err)

	// Simulate a prior boot's parked durable.
	staleDurable := patternSourceDurablePrefix + "-old-instance"
	_, err = js.CreateOrUpdateConsumer(ctx, "KV_"+bucket, jetstream.ConsumerConfig{
		Durable:       staleDurable,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	require.True(t, consumerExists(ctx, t, js, "KV_"+bucket, staleDurable), "stale durable should exist before start")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	subCtx, subCancel := context.WithCancel(ctx)
	src := newPatternSource(conn, bucket, "new-instance", logger)
	require.NoError(t, src.start(subCtx))

	newDurable := patternSourceDurablePrefix + "-new-instance"
	require.Eventually(t, func() bool {
		return consumerExists(ctx, t, js, "KV_"+bucket, newDurable)
	}, 5*time.Second, 50*time.Millisecond, "new instance durable should be created")

	require.False(t, consumerExists(ctx, t, js, "KV_"+bucket, staleDurable),
		"stale prior-boot durable should be pruned on start")

	// Clean shutdown: cancelling the subscription context must delete this
	// instance's own durable so it does not leak either.
	subCancel()
	require.Eventually(t, func() bool {
		return !consumerExists(ctx, t, js, "KV_"+bucket, newDurable)
	}, 5*time.Second, 50*time.Millisecond, "own durable should be deleted on clean shutdown")
}

func consumerExists(ctx context.Context, t *testing.T, js jetstream.JetStream, stream, name string) bool {
	t.Helper()
	_, err := js.Consumer(ctx, stream, name)
	if err == nil {
		return true
	}
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return false
	}
	t.Fatalf("Consumer(%s, %s): %v", stream, name, err)
	return false
}
