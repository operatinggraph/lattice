package bootstrap_test

// Core KV — not `lattice.bootstrap.json` — is the authority on whether a
// given bucket has been seeded. These tests pin PrimordialSeeded's two
// answers and the recovery they exist to enable: a Core KV that has been
// recreated or wiped behind a surviving committed file is re-seeded rather
// than skipped.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// newFreshnessSeeder provisions the buckets against a fresh embedded server,
// leaving Core KV present but unseeded.
func newFreshnessSeeder(ctx context.Context, t *testing.T) (*bootstrap.Seeder, *nats.Conn) {
	t.Helper()
	testutil.EnsurePrimordials(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	return seeder, nc
}

// TestPersistInProgress_ReopensWindowKeepingIDs covers the file half of the
// re-seed path. Seeding against an already-`committed` file without first
// reopening the two-phase window would make a partway-dead run
// indistinguishable from a finished one: the op tracker is written FIRST
// (§7.7), so its presence marks a seed "started", not "done", and the next
// run's probe would read that lone key as proof of a complete kernel.
//
// The reopen must also leave the NanoIDs exactly as recorded — re-seeding
// from the file is only worth doing because it restores the ids existing
// packages and data already reference. (That `in-progress` then drives a
// re-seed is pinned by TestLoadOrGenerate_FreshThenRecoverThenCommitted.)
func TestPersistInProgress_ReopensWindowKeepingIDs(t *testing.T) {
	testutil.EnsurePrimordials(t)
	path := filepath.Join(t.TempDir(), "lattice.bootstrap.json")

	require.NoError(t, bootstrap.PersistCommitted(path))
	idBefore := bootstrap.BootstrapIdentityID

	require.NoError(t, bootstrap.PersistInProgress(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"status": "in-progress"`,
		"an interrupted re-seed must be retryable, so the window has to reopen")
	require.False(t, strings.Contains(string(data), `"status": "committed"`),
		"the committed claim must not survive the reopen")
	require.Equal(t, idBefore, bootstrap.BootstrapIdentityID,
		"reopening the window must not change the admin NanoID — existing references would be orphaned")
	require.Contains(t, string(data), idBefore,
		"the reopened file must still carry the stable NanoIDs the re-seed will use")
}

// TestPrimordialSeeded_EmptyBucketReportsUnseeded proves the probe's negative
// answer: a provisioned but never-seeded Core KV holds no op tracker.
func TestPrimordialSeeded_EmptyBucketReportsUnseeded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, _ := newFreshnessSeeder(ctx, t)

	seeded, err := seeder.PrimordialSeeded(ctx)
	require.NoError(t, err)
	require.False(t, seeded, "a provisioned but unseeded Core KV must report unseeded")
}

// TestPrimordialSeeded_SeededBucketReportsSeeded is the positive sibling: once
// SeedPrimordial has committed, the same probe reports the set present.
func TestPrimordialSeeded_SeededBucketReportsSeeded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, _ := newFreshnessSeeder(ctx, t)

	require.NoError(t, seeder.SeedPrimordial(ctx))

	seeded, err := seeder.PrimordialSeeded(ctx)
	require.NoError(t, err)
	require.True(t, seeded, "a seeded Core KV must report seeded")
}

// TestCoreKVEmpty_EmptyThenPopulated pins the file-independent discriminator
// make up uses after a verify mismatch: a provisioned-but-unseeded Core KV
// reports empty (keep the file, re-seed at stable NanoIDs), and a seeded Core
// KV reports populated (a different set is live, discard the stale file).
func TestCoreKVEmpty_EmptyThenPopulated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, nc := newFreshnessSeeder(ctx, t)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	empty, err := bootstrap.CoreKVEmpty(ctx, conn)
	require.NoError(t, err)
	require.True(t, empty, "a provisioned but unseeded Core KV must report empty")

	require.NoError(t, seeder.SeedPrimordial(ctx))

	empty, err = bootstrap.CoreKVEmpty(ctx, conn)
	require.NoError(t, err)
	require.False(t, empty, "a seeded Core KV must report populated")
}

// TestCoreKVEmpty_MissingBucketReportsEmpty covers the absent-bucket branch: a
// stack whose NATS was recreated may not have re-provisioned Core KV yet, and
// that must read as empty (keep the file), not as an error.
func TestCoreKVEmpty_MissingBucketReportsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, nc := newFreshnessSeeder(ctx, t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, js.DeleteKeyValue(ctx, bootstrap.CoreKVBucket))

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	empty, err := bootstrap.CoreKVEmpty(ctx, conn)
	require.NoError(t, err)
	require.True(t, empty, "an absent Core KV bucket must report empty, not error")
}

// TestPrimordialSeeded_RecreatedBucketReSeeds is the regression for the
// stale-file gap: after Core KV is destroyed and re-provisioned — the state a
// surviving status="committed" file would otherwise mask — the probe reports
// unseeded and SeedPrimordial repopulates the set at the same stable NanoIDs,
// so existing package and data references stay valid.
func TestPrimordialSeeded_RecreatedBucketReSeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, nc := newFreshnessSeeder(ctx, t)

	require.NoError(t, seeder.SeedPrimordial(ctx))
	identityKey := bootstrap.BootstrapIdentityKey

	// Destroy and re-provision Core KV, leaving the loaded ID set untouched:
	// exactly the "recreated bucket behind a surviving committed file" case.
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, js.DeleteKeyValue(ctx, bootstrap.CoreKVBucket))
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	seeded, err := seeder.PrimordialSeeded(ctx)
	require.NoError(t, err)
	require.False(t, seeded, "a recreated Core KV must report unseeded even though the file says committed")

	require.NoError(t, seeder.SeedPrimordial(ctx), "an unseeded Core KV must re-seed, not skip")

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = conn.KVGet(ctx, bootstrap.CoreKVBucket, bootstrap.BootstrapOpKey)
	require.NoError(t, err, "op tracker must be restored by the re-seed")
	_, err = conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey)
	require.NoError(t, err, "the re-seed must restore the identity at its original stable NanoID")

	seeded, err = seeder.PrimordialSeeded(ctx)
	require.NoError(t, err)
	require.True(t, seeded, "the probe must report seeded once the re-seed has committed")
}
