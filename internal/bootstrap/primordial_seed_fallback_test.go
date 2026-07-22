package bootstrap_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestSeedPrimordial_ConcurrentBootstrapFallback proves the
// seedPrimordialPerKey fallback path (primordial.go): when the primordial
// atomic batch is rejected because a key already exists — the documented
// "a concurrent bootstrapper raced us" case — SeedPrimordial falls back to
// the idempotent per-key path instead of failing the whole seed, skipping
// the pre-existing key untouched and creating the rest.
func TestSeedPrimordial_ConcurrentBootstrapFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
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
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	// Simulate a concurrent bootstrapper: MetaRootKey lands before this
	// seeder's atomic batch runs, so the CreateOnly batch is rejected on
	// revision conflict and SeedPrimordial must fall back to the per-key
	// path rather than error out.
	sentinel, err := bootstrap.MakeVertexEnvelope(bootstrap.MetaRootKey, "meta.ddl.vertexType",
		map[string]any{"raced": true})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, bootstrap.MetaRootKey, sentinel)
	require.NoError(t, err)

	require.NoError(t, seeder.SeedPrimordial(ctx))

	// The rest of the primordial set must have been seeded via the fallback.
	_, err = conn.KVGet(ctx, bootstrap.CoreKVBucket, bootstrap.BootstrapOpKey)
	require.NoError(t, err, "op tracker must exist after fallback seeding")
	_, err = conn.KVGet(ctx, bootstrap.CoreKVBucket, bootstrap.BootstrapIdentityKey)
	require.NoError(t, err, "bootstrap identity must exist after fallback seeding")

	// The pre-existing key must NOT have been overwritten — the fallback's
	// "key already exists, skipping" branch, not a blind re-create.
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, bootstrap.MetaRootKey)
	require.NoError(t, err)
	var rootDoc struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(entry.Value, &rootDoc))
	require.Equal(t, true, rootDoc.Data["raced"], "concurrently-seeded key must survive the fallback untouched")
}
