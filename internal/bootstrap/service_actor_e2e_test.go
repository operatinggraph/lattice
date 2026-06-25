package bootstrap_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
)

func startBootstrapNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func seedFresh(t *testing.T, nc *nats.Conn, logger *slog.Logger) {
	t.Helper()
	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))
}

// TestSeedPrimordial_ServiceActorsIdempotent proves AC #4: re-running
// SeedPrimordial after the op tracker exists is a no-op for the Loom/Weaver
// vertices and links — no duplicates, no error. The new entries ride the
// existing op-tracker-present idempotent skip; nothing new was added to the
// idempotence machinery.
func TestSeedPrimordial_ServiceActorsIdempotent(t *testing.T) {
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
	require.NoError(t, seeder.SeedPrimordial(ctx))

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)

	serviceKeys := []string{
		bootstrap.LoomIdentityKey,
		bootstrap.WeaverIdentityKey,
		bootstrap.BridgeIdentityKey,
		bootstrap.LoomHoldsRoleLinkKey,
		bootstrap.WeaverHoldsRoleLinkKey,
		bootstrap.BridgeHoldsRoleLinkKey,
	}
	revBefore := map[string]uint64{}
	for _, k := range serviceKeys {
		entry, getErr := coreKV.Get(ctx, k)
		require.NoError(t, getErr, "service-actor key %q must exist after first seed", k)
		revBefore[k] = entry.Revision()
	}

	// Second seed — op tracker present, so the whole batch is skipped.
	require.NoError(t, seeder.SeedPrimordial(ctx))

	for _, k := range serviceKeys {
		entry, getErr := coreKV.Get(ctx, k)
		require.NoError(t, getErr, "service-actor key %q must still exist after re-seed", k)
		require.Equal(t, revBefore[k], entry.Revision(),
			"re-seed must not rewrite service-actor key %q (idempotent)", k)
	}
}

// TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections proves the
// AC #4 readiness gate: WaitForBootstrapComplete does NOT return ready until
// the admin, Loom, Weaver, AND Bridge cap.* projections all exist — and that a
// missing projection times out cleanly within the caller's bound rather than
// hanging.
func TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)
	seedFresh(t, nc, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, bootstrap.MarkBootstrapComplete(ctx, nc, logger))

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	capKV, err := js.KeyValue(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	capKey := func(id string) string { return "cap.identity." + id }
	put := func(id string) {
		_, perr := capKV.Put(ctx, capKey(id), []byte(`{"key":"`+capKey(id)+`"}`))
		require.NoError(t, perr)
	}

	adminID := bootstrap.BootstrapIdentityID
	loomID := bootstrap.LoomIdentityID
	weaverID := bootstrap.WeaverIdentityID
	bridgeID := bootstrap.BridgeIdentityID

	// With the health marker present but NO cap.* projections, the gate must
	// time out cleanly (never hang past the bound).
	shortCtx, shortCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	err = bootstrap.WaitForBootstrapComplete(shortCtx, nc, logger)
	shortCancel()
	require.Error(t, err, "gate must NOT be ready with missing cap.* projections")
	require.Contains(t, err.Error(), "timed out")

	// Land admin + loom + weaver only; bridge still missing → still not ready.
	// The budget is comfortably above many 500ms poll ticks so the deadline
	// fires only after several clean polls have settled lastMissing on the
	// bridge key, even under embedded-NATS read jitter.
	put(adminID)
	put(loomID)
	put(weaverID)
	shortCtx2, shortCancel2 := context.WithTimeout(ctx, 10*time.Second)
	err = bootstrap.WaitForBootstrapComplete(shortCtx2, nc, logger)
	shortCancel2()
	require.Error(t, err, "gate must NOT be ready while bridge cap.* is missing")
	require.Contains(t, err.Error(), "bridge")

	// Land bridge — all four present → ready.
	put(bridgeID)
	readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Second)
	err = bootstrap.WaitForBootstrapComplete(readyCtx, nc, logger)
	readyCancel()
	require.NoError(t, err, "gate must be ready once admin + loom + weaver + bridge cap.* all exist")
}
