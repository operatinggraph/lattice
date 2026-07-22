package loom

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPatternSource_StartPrunesStalePriorBootDurable proves the fix for the
// per-boot durable leak: a durable left behind by a prior boot under the
// loom-pattern-source-<instance> prefix is deleted, once it ages past
// substrate.PruneStaleDurableAge, when a new instance's patternSource.start
// runs; and the new instance's own durable is deleted in turn once its
// context is cancelled (clean shutdown), so it does not become the next
// boot's stale entry. Not t.Parallel(): it shrinks the package-level
// substrate.PruneStaleDurableAge, which every concurrent patternSource.start
// in this package reads (the age guard — refractor-lens-registry-restart-
// integrity-design.md §4.1 — protects a live sibling's durable from a
// concurrent boot's prune; a real 10-minute wait isn't practical here).
func TestPatternSource_StartPrunesStalePriorBootDurable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	origAge := substrate.PruneStaleDurableAge
	substrate.PruneStaleDurableAge = 100 * time.Millisecond
	t.Cleanup(func() { substrate.PruneStaleDurableAge = origAge })
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

	// Age the stale durable past the (shrunk) threshold so the age guard lets
	// PruneStaleDurables remove it below.
	time.Sleep(2 * substrate.PruneStaleDurableAge)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	subCtx, subCancel := context.WithCancel(ctx)
	src := newPatternSource(conn, bucket, "new-instance", logger)
	require.NoError(t, src.start(subCtx))

	newDurablePrefix := patternSourceDurablePrefix + "-new-instance-"
	var newDurable string
	require.Eventually(t, func() bool {
		newDurable = durableWithPrefix(ctx, t, js, "KV_"+bucket, newDurablePrefix)
		return newDurable != ""
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

// TestPatternSource_StableInstanceGetsFreshDurableEachBoot proves the fix for
// the cold-registry bug: a Loom operator following docs/components/loom.md's
// guidance to set a STABLE Instance across restarts (for dashboard/alerting
// attributability) must still get a never-before-seen durable name on every
// boot, because JetStream only honors DeliverPolicy at consumer creation — an
// existing durable of the identical name resumes from its persisted ack
// floor regardless of the DeliverAllPolicy requested, leaving a
// crash-restarted engine's in-memory pattern registry cold. The second
// "boot" below never cancels the first boot's context (simulating a crash,
// not a clean shutdown), so the first boot's durable is never
// self-deleted — the second boot's start must not reuse its name anyway.
func TestPatternSource_StableInstanceGetsFreshDurableEachBoot(t *testing.T) {
	t.Parallel()
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

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	durablePrefix := patternSourceDurablePrefix + "-stable-instance-"

	firstCtx, firstCancel := context.WithCancel(ctx)
	defer firstCancel()
	first := newPatternSource(conn, bucket, "stable-instance", logger)
	require.NoError(t, first.start(firstCtx))

	var durable1 string
	require.Eventually(t, func() bool {
		durable1 = durableWithPrefix(ctx, t, js, "KV_"+bucket, durablePrefix)
		return durable1 != ""
	}, 5*time.Second, 50*time.Millisecond, "first boot should create its durable")

	secondCtx, secondCancel := context.WithCancel(ctx)
	defer secondCancel()
	second := newPatternSource(conn, bucket, "stable-instance", logger)
	require.NoError(t, second.start(secondCtx))

	// The first boot's durable is still within substrate.PruneStaleDurableAge
	// (age-guarded — §4.1), so it survives the second boot's prune call
	// alongside the new one; look for a durable other than durable1 rather
	// than assuming only one match exists.
	var durable2 string
	require.Eventually(t, func() bool {
		durable2 = durableWithPrefixExcluding(ctx, t, js, "KV_"+bucket, durablePrefix, durable1)
		return durable2 != ""
	}, 5*time.Second, 50*time.Millisecond, "second boot should create a durable distinct from the first")

	require.NotEqual(t, durable1, durable2,
		"the same stable Instance across two boots must not reuse the prior durable name")
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

// durableWithPrefix returns the name of the (at most one expected) JetStream
// durable consumer on stream whose name starts with prefix, or "" if none
// exists yet.
func durableWithPrefix(ctx context.Context, t *testing.T, js jetstream.JetStream, stream, prefix string) string {
	t.Helper()
	st, err := js.Stream(ctx, stream)
	require.NoError(t, err)
	lister := st.ConsumerNames(ctx)
	found := ""
	for name := range lister.Name() {
		if strings.HasPrefix(name, prefix) {
			found = name
		}
	}
	require.NoError(t, lister.Err())
	return found
}

// durableWithPrefixExcluding returns the name of a JetStream durable
// consumer on stream whose name starts with prefix and is not exclude, or ""
// if none exists yet. Used where more than one durable under the prefix may
// legitimately coexist (e.g. an age-guarded prior-boot durable alongside a
// fresh one) and the test cares about "a distinct one exists", not "the
// only one".
func durableWithPrefixExcluding(ctx context.Context, t *testing.T, js jetstream.JetStream, stream, prefix, exclude string) string {
	t.Helper()
	st, err := js.Stream(ctx, stream)
	require.NoError(t, err)
	lister := st.ConsumerNames(ctx)
	found := ""
	for name := range lister.Name() {
		if strings.HasPrefix(name, prefix) && name != exclude {
			found = name
		}
	}
	require.NoError(t, lister.Err())
	return found
}
