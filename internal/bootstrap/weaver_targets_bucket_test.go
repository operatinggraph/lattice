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

// TestWeaverTargetsBucket_Provisioned asserts the shared weaver-targets
// target-Lens projection bucket (Contract #10 §10.2) joins the primordial
// create list. Target rows are durable Lens projections: no per-key TTL keys
// live here (marks with TTL live in weaver-state), history stays the KV
// default 1 (what DeliverLastPerSubject CDC wants), and no AllowAtomicPublish
// (Weaver does no atomic batch on this bucket).
func TestWeaverTargetsBucket_Provisioned(t *testing.T) {
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

	kv, err := js.KeyValue(ctx, bootstrap.WeaverTargetsBucket)
	require.NoError(t, err, "weaver-targets bucket must exist after ProvisionBuckets")
	require.Equal(t, bootstrap.WeaverTargetsBucket, kv.Bucket())

	stream, err := js.Stream(ctx, "KV_"+bootstrap.WeaverTargetsBucket)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.False(t, info.Config.AllowMsgTTL,
		"weaver-targets rows are durable projections — the bucket must not be TTL-capable")
	require.False(t, info.Config.AllowAtomicPublish,
		"weaver-targets has no AtomicBatch writer — AllowAtomicPublish must stay off")
	require.EqualValues(t, 1, info.Config.MaxMsgsPerSubject,
		"weaver-targets must keep the KV default history of 1 (DeliverLastPerSubject CDC)")
}
