package bootstrap_test

// DecideReseed is the extraction of cmd/bootstrap's re-seed decision — the
// branch that used to live untested in package main. These tests pin its
// three outcomes directly, without a binary.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func TestDecideReseed_FreshlyGeneratedSkipsProbe(t *testing.T) {
	testutil.EnsurePrimordials(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// No NATS connection wired into the seeder's KV lookups is exercised
	// here — freshlyGenerated=true must short-circuit before any probe, so
	// a nil seeder must never be dereferenced.
	shouldSeed, err := bootstrap.DecideReseed(context.Background(), nil, filepath.Join(t.TempDir(), "lattice.bootstrap.json"), true, logger)
	require.NoError(t, err)
	require.True(t, shouldSeed, "a freshly generated or crash-recovered ID set must always seed")
}

func TestDecideReseed_SeededBucketSkipsReseed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	testutil.EnsurePrimordials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	require.NoError(t, bootstrap.PersistCommitted(path))

	shouldSeed, err := bootstrap.DecideReseed(ctx, seeder, path, false, logger)
	require.NoError(t, err)
	require.False(t, shouldSeed, "an already-seeded Core KV must not be reseeded")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"status": "committed"`,
		"the file must be left untouched when the bucket agrees with it")
}

func TestDecideReseed_RecreatedBucketReopensWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	testutil.EnsurePrimordials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	require.NoError(t, bootstrap.PersistCommitted(path))
	idBefore := bootstrap.BootstrapIdentityID

	// Destroy and re-provision Core KV behind the surviving committed file —
	// the exact disagreement DecideReseed exists to catch.
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, js.DeleteKeyValue(ctx, bootstrap.CoreKVBucket))
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	shouldSeed, err := bootstrap.DecideReseed(ctx, seeder, path, false, logger)
	require.NoError(t, err)
	require.True(t, shouldSeed, "a recreated Core KV must be reseeded even though the file says committed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"status": "in-progress"`,
		"the two-phase commit window must reopen before the caller seeds")
	require.False(t, strings.Contains(string(data), `"status": "committed"`),
		"the stale committed claim must not survive the reopen")
	require.Equal(t, idBefore, bootstrap.BootstrapIdentityID,
		"reopening the window must not change the stable NanoIDs the reseed will use")
}

func TestDecideReseed_ProbeErrorSurfaces(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	testutil.EnsurePrimordials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	// Deliberately skip ProvisionBuckets — Core KV never exists, so the
	// probe itself fails rather than returning a seeded/unseeded verdict.
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	require.NoError(t, bootstrap.PersistCommitted(path))

	_, err = bootstrap.DecideReseed(ctx, seeder, path, false, logger)
	require.Error(t, err, "a probe failure must surface, not be swallowed as unseeded")
}
