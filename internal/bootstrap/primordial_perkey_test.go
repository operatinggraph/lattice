package bootstrap

// Direct tests for seedPrimordialPerKey — the concurrent-bootstrap fallback
// seeding path. The batch-rejection entry into the fallback is proven
// end-to-end in primordial_seed_fallback_test.go; the tests here drive the
// fallback loop's own per-key branches, including the Get-miss →
// Create-conflict window that a full SeedPrimordial run cannot reach
// deterministically. They live in-package because seedPrimordialPerKey and
// kvEntry are unexported.

import (
	"context"
	"errors"
	"log/slog"
	"os"
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

// startPerKeyNATS starts an embedded JetStream server for the in-package
// fallback tests (the external-package harness in service_actor_e2e_test.go
// is not visible from package bootstrap).
func startPerKeyNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// newPerKeySeeder populates the primordial ID globals (the envelope helpers
// stamp BootstrapIdentityKey/BootstrapOpKey as provenance), provisions a bare
// Core KV bucket — the only bucket the fallback loop touches — and returns a
// Seeder plus the KV handle.
func newPerKeySeeder(ctx context.Context, t *testing.T) (*Seeder, jetstream.KeyValue) {
	t.Helper()
	populateForTest(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startPerKeyNATS(t)
	seeder, err := NewSeeder(nc, logger)
	require.NoError(t, err)
	kv, err := seeder.js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: CoreKVBucket})
	require.NoError(t, err)
	return seeder, kv
}

// identityEntry builds one primordial-style kvEntry with a fresh Contract #1
// NanoID and a valid vertex envelope.
func identityEntry(t *testing.T, note string) kvEntry {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("identity", id)
	val, err := MakeVertexEnvelope(key, "identity", map[string]any{"note": note})
	require.NoError(t, err)
	return kvEntry{key: key, value: val}
}

// TestSeedPrimordialPerKey_SkipsExistingKey proves the fallback's Get-probe
// skip branch: a key that already exists is left untouched (same revision,
// same value), and the entries after it are still created.
func TestSeedPrimordialPerKey_SkipsExistingKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, kv := newPerKeySeeder(ctx, t)

	existing := identityEntry(t, "ours — must be skipped")
	fresh := identityEntry(t, "created by the fallback")
	rival, err := MakeVertexEnvelope(existing.key, "identity",
		map[string]any{"note": "landed before the fallback ran"})
	require.NoError(t, err)
	preRev, err := kv.Create(ctx, existing.key, rival)
	require.NoError(t, err)

	require.NoError(t, seeder.seedPrimordialPerKey(ctx, kv, []kvEntry{existing, fresh}))

	entry, err := kv.Get(ctx, existing.key)
	require.NoError(t, err)
	require.Equal(t, preRev, entry.Revision(), "pre-existing key must be skipped, not rewritten")
	require.Equal(t, rival, entry.Value(), "pre-existing value must survive untouched")

	created, err := kv.Get(ctx, fresh.key)
	require.NoError(t, err, "entries after a skipped key must still be seeded")
	require.Equal(t, fresh.value, created.Value())
}

// createRaceKV wraps a real jetstream.KeyValue and simulates a concurrent
// bootstrapper winning the Get→Create window for one key: when the fallback
// calls Create on raceKey, the rival value is landed first, so the forwarded
// Create fails with the server's real key-exists conflict.
type createRaceKV struct {
	jetstream.KeyValue
	t         *testing.T
	raceKey   string
	raceValue []byte
}

func (w *createRaceKV) Create(ctx context.Context, key string, value []byte, opts ...jetstream.KVCreateOpt) (uint64, error) {
	if key == w.raceKey {
		_, err := w.KeyValue.Create(ctx, key, w.raceValue)
		require.NoError(w.t, err, "rival create must land inside the Get→Create window")
	}
	return w.KeyValue.Create(ctx, key, value, opts...)
}

// TestSeedPrimordialPerKey_ConcurrentCreateConflictSkips proves the
// IsRevisionConflict skip branch: a key created concurrently between the
// fallback's Get probe and its Create is treated as already seeded — the
// conflict is absorbed, the concurrent winner's value survives, and the loop
// continues to the remaining entries. The conflict error comes from the real
// server, so the test also proves IsRevisionConflict matches what NATS
// actually returns for a lost create race.
func TestSeedPrimordialPerKey_ConcurrentCreateConflictSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, kv := newPerKeySeeder(ctx, t)

	raced := identityEntry(t, "ours — must lose the race")
	fresh := identityEntry(t, "created after the conflict")
	rival, err := MakeVertexEnvelope(raced.key, "identity",
		map[string]any{"note": "concurrent bootstrapper"})
	require.NoError(t, err)

	wrapped := &createRaceKV{KeyValue: kv, t: t, raceKey: raced.key, raceValue: rival}
	require.NoError(t, seeder.seedPrimordialPerKey(ctx, wrapped, []kvEntry{raced, fresh}),
		"a create-time conflict means a concurrent seeder won the key — skip, not failure")

	entry, err := kv.Get(ctx, raced.key)
	require.NoError(t, err)
	require.Equal(t, rival, entry.Value(), "the concurrent winner's value must survive untouched")

	created, err := kv.Get(ctx, fresh.key)
	require.NoError(t, err, "the loop must continue past the conflicted key")
	require.Equal(t, fresh.value, created.Value())
}

// failCreateKV wraps a real jetstream.KeyValue and fails Create for one key
// with a non-conflict error — the store misbehaving, not a concurrent creator.
type failCreateKV struct {
	jetstream.KeyValue
	failKey string
	failErr error
}

func (w *failCreateKV) Create(ctx context.Context, key string, value []byte, opts ...jetstream.KVCreateOpt) (uint64, error) {
	if key == w.failKey {
		return 0, w.failErr
	}
	return w.KeyValue.Create(ctx, key, value, opts...)
}

// TestSeedPrimordialPerKey_NonConflictCreateErrorFails is the negative
// sibling of the conflict-skip test: only revision conflicts are absorbed as
// skips. Any other Create failure aborts the seed with the failed key named,
// and no later entry is written.
func TestSeedPrimordialPerKey_NonConflictCreateErrorFails(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seeder, kv := newPerKeySeeder(ctx, t)

	failing := identityEntry(t, "create fails hard")
	never := identityEntry(t, "must not be reached")
	injected := errors.New("kv backend unavailable")

	wrapped := &failCreateKV{KeyValue: kv, failKey: failing.key, failErr: injected}
	err := seeder.seedPrimordialPerKey(ctx, wrapped, []kvEntry{failing, never})
	require.Error(t, err, "a non-conflict create failure must abort the seed")
	require.ErrorIs(t, err, injected, "the underlying failure must be preserved in the wrap")
	require.Contains(t, err.Error(), failing.key, "the error must name the failed key")

	_, getErr := kv.Get(ctx, failing.key)
	require.ErrorIs(t, getErr, jetstream.ErrKeyNotFound)
	_, getErr = kv.Get(ctx, never.key)
	require.ErrorIs(t, getErr, jetstream.ErrKeyNotFound, "seeding must abort at the failed key, not continue")
}
