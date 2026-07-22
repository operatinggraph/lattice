package weaver

import (
	"context"
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

// TestTargetSource_StableInstanceGetsFreshDurableEachBoot proves the fix for
// the cold-registry bug on Weaver's registry source (the twin of Loom's
// pattern source): an operator following WEAVER_INSTANCE guidance to set a
// STABLE Instance across restarts (for dashboard/alerting attributability)
// must still get a never-before-seen durable name on every boot, because
// JetStream only honors DeliverPolicy at consumer creation — an existing
// durable of the identical name resumes from its persisted ack floor
// regardless of the DeliverAllPolicy requested, leaving a crash-restarted
// engine's in-memory target registry cold. The second "boot" below never
// cancels the first boot's context (simulating a crash, not a clean
// shutdown), so the first boot's durable is never self-deleted — the second
// boot's start must not reuse its name anyway.
func TestTargetSource_StableInstanceGetsFreshDurableEachBoot(t *testing.T) {
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

	logger := discardLogger()
	durablePrefix := targetSourceDurablePrefix + "-stable-instance-"

	firstCtx, firstCancel := context.WithCancel(ctx)
	defer firstCancel()
	first := newTargetSource(conn, bucket, "stable-instance", newIssueCache(), logger)
	require.NoError(t, first.start(firstCtx))

	var durable1 string
	require.Eventually(t, func() bool {
		durable1 = targetDurableWithPrefix(ctx, t, js, "KV_"+bucket, durablePrefix)
		return durable1 != ""
	}, 5*time.Second, 50*time.Millisecond, "first boot should create its durable")

	secondCtx, secondCancel := context.WithCancel(ctx)
	defer secondCancel()
	second := newTargetSource(conn, bucket, "stable-instance", newIssueCache(), logger)
	require.NoError(t, second.start(secondCtx))

	// The first boot's durable is still within substrate.PruneStaleDurableAge
	// (age-guarded — refractor-lens-registry-restart-integrity-design.md
	// §4.1), so it survives the second boot's prune call alongside the new
	// one; look for a durable other than durable1 rather than assuming only
	// one match exists.
	var durable2 string
	require.Eventually(t, func() bool {
		durable2 = targetDurableWithPrefixExcluding(ctx, t, js, "KV_"+bucket, durablePrefix, durable1)
		return durable2 != ""
	}, 5*time.Second, 50*time.Millisecond, "second boot should create a durable distinct from the first")

	require.NotEqual(t, durable1, durable2,
		"the same stable Instance across two boots must not reuse the prior durable name")
}

// targetDurableWithPrefix returns the name of the (at most one expected)
// JetStream durable consumer on stream whose name starts with prefix, or ""
// if none exists yet.
func targetDurableWithPrefix(ctx context.Context, t *testing.T, js jetstream.JetStream, stream, prefix string) string {
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

// targetDurableWithPrefixExcluding returns the name of a JetStream durable
// consumer on stream whose name starts with prefix and is not exclude, or ""
// if none exists yet. Used where more than one durable under the prefix may
// legitimately coexist (e.g. an age-guarded prior-boot durable alongside a
// fresh one) and the test cares about "a distinct one exists", not "the
// only one".
func targetDurableWithPrefixExcluding(ctx context.Context, t *testing.T, js jetstream.JetStream, stream, prefix, exclude string) string {
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
