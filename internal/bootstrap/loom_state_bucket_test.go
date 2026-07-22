package bootstrap_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// TestLoomStateBucket_Provisioned asserts the loom-state operational bucket
// (Contract #10 §10.3) joins the primordial create list and is TTL-capable,
// matching its weaver-state sibling.
func TestLoomStateBucket_Provisioned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Re-running must stay idempotent (CreateOrUpdateKeyValue).
	require.NoError(t, seeder.ProvisionBuckets(ctx), "first ProvisionBuckets must not error")
	require.NoError(t, seeder.ProvisionBuckets(ctx), "re-run ProvisionBuckets must not error")

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	kv, err := js.KeyValue(ctx, bootstrap.LoomStateBucket)
	require.NoError(t, err, "loom-state bucket must exist after ProvisionBuckets")
	require.Equal(t, bootstrap.LoomStateBucket, kv.Bucket())

	// TTL-capable: the backing stream must allow per-message TTL (LimitMarkerTTL),
	// like weaver-state.
	stream, err := js.Stream(ctx, "KV_"+bootstrap.LoomStateBucket)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.True(t, info.Config.AllowMsgTTL, "loom-state must be provisioned TTL-capable")

	// AllowAtomicPublish: loom-state's writer is Loom's per-transition
	// AtomicBatch (Contract #10 §10.3); without this flag Conn.AtomicBatch on
	// loom-state is rejected. The flag must survive an idempotent re-provision.
	require.True(t, info.Config.AllowAtomicPublish,
		"loom-state must be provisioned with AllowAtomicPublish (Conn.AtomicBatch requires it)")
}
