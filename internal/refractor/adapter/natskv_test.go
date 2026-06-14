package adapter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// startKV starts an in-memory NATS server with JetStream and returns a KV bucket for testing.
func startKV(t *testing.T) jetstream.KeyValue {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	kv, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "test-target"})
	require.NoError(t, err)
	return kv
}

// newAdapter is a test helper that requires New to succeed, using the default
// hard delete mode.
func newAdapter(t *testing.T, kv jetstream.KeyValue, keyOrder []string) *adapter.NatsKVAdapter {
	return newAdapterMode(t, kv, keyOrder, adapter.DeleteModeHard)
}

// newAdapterMode is like newAdapter but lets the caller choose the delete mode.
func newAdapterMode(t *testing.T, kv jetstream.KeyValue, keyOrder []string, mode adapter.DeleteMode) *adapter.NatsKVAdapter {
	t.Helper()
	a, err := adapter.New(kv, keyOrder, mode)
	require.NoError(t, err)
	return a
}

func TestNatsKVAdapter_New_EmptyKeyOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	kv := startKV(t)
	_, err := adapter.New(kv, nil, adapter.DeleteModeHard)
	require.Error(t, err)
	_, err = adapter.New(kv, []string{}, adapter.DeleteModeHard)
	require.Error(t, err)
}

func TestNatsKVAdapter_Upsert_SingleKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"agreement_id"})

	keys := map[string]any{"agreement_id": "abc123"}
	row := map[string]any{"party_name": "Acme Corp", "status": "active"}

	err := a.Upsert(context.Background(), keys, row, 0)
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "abc123")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "Acme Corp", got["party_name"])
	assert.Equal(t, "active", got["status"])
}

func TestNatsKVAdapter_Upsert_CompositeKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"account_id", "agreement_id"})

	keys := map[string]any{"account_id": "acct-001", "agreement_id": "abc123"}
	row := map[string]any{"name": "Widget Agreement"}

	err := a.Upsert(context.Background(), keys, row, 0)
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "acct-001.abc123")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "Widget Agreement", got["name"])
}

func TestNatsKVAdapter_Upsert_Idempotent(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})

	keys := map[string]any{"id": "e1"}

	err := a.Upsert(context.Background(), keys, map[string]any{"value": "first"}, 0)
	require.NoError(t, err)

	err = a.Upsert(context.Background(), keys, map[string]any{"value": "second"}, 0)
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "e1")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "second", got["value"], "expected latest value after two upserts")
}

func TestNatsKVAdapter_Upsert_AbsentKeyField(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id", "account_id"})

	// "account_id" is absent from the keys map.
	err := a.Upsert(context.Background(), map[string]any{"id": "e1"}, map[string]any{"x": 1}, 0)
	require.Error(t, err)
}

func TestNatsKVAdapter_Delete_Hard(t *testing.T) {
	kv := startKV(t)
	a := newAdapterMode(t, kv, []string{"id"}, adapter.DeleteModeHard)

	keys := map[string]any{"id": "e1"}

	require.NoError(t, a.Upsert(context.Background(), keys, map[string]any{"x": 1}, 0))

	require.NoError(t, a.Delete(context.Background(), keys, 0))

	// Story 1.5.12: the default hard mode physically removes the key.
	_, err := kv.Get(context.Background(), "e1")
	require.ErrorIs(t, err, jetstream.ErrKeyNotFound)
}

func TestNatsKVAdapter_Delete_Soft(t *testing.T) {
	kv := startKV(t)
	a := newAdapterMode(t, kv, []string{"id"}, adapter.DeleteModeSoft)

	keys := map[string]any{"id": "e1"}

	require.NoError(t, a.Upsert(context.Background(), keys, map[string]any{"x": 1}, 0))

	require.NoError(t, a.Delete(context.Background(), keys, 0))

	// Soft mode writes a tombstone document {"isDeleted": true} instead of a
	// physical KV delete (opt-in audit/forensic behavior).
	entry, err := kv.Get(context.Background(), "e1")
	require.NoError(t, err)
	require.Contains(t, string(entry.Value()), `"isDeleted":true`)
}

func TestNatsKVAdapter_Delete_NeverExisted_Hard(t *testing.T) {
	kv := startKV(t)
	a := newAdapterMode(t, kv, []string{"id"}, adapter.DeleteModeHard)

	// Hard-deleting a key that was never upserted must succeed (no-op /
	// idempotent): jetstream.ErrKeyNotFound is swallowed.
	err := a.Delete(context.Background(), map[string]any{"id": "ghost"}, 0)
	require.NoError(t, err)

	// And the key must still be absent afterwards.
	_, gErr := kv.Get(context.Background(), "ghost")
	require.ErrorIs(t, gErr, jetstream.ErrKeyNotFound)
}

func TestNatsKVAdapter_Delete_NeverExisted_Soft(t *testing.T) {
	kv := startKV(t)
	a := newAdapterMode(t, kv, []string{"id"}, adapter.DeleteModeSoft)

	// Soft-deleting a key that was never upserted must succeed (Put creates a
	// tombstone) — idempotent.
	err := a.Delete(context.Background(), map[string]any{"id": "ghost"}, 0)
	require.NoError(t, err)
}

// guardedAdapter builds a guarded NatsKVAdapter for the projection-write-guard
// tests.
func guardedAdapter(t *testing.T, kv jetstream.KeyValue, keyOrder []string) *adapter.NatsKVAdapter {
	t.Helper()
	a := newAdapterMode(t, kv, keyOrder, adapter.DeleteModeHard)
	a.SetGuarded(true)
	return a
}

func TestNatsKVAdapter_Guarded_StampsProjectionSeqIntoBody(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})

	keys := map[string]any{"key": "cap.ephemeral.identity.A"}
	require.NoError(t, a.Upsert(context.Background(), keys, map[string]any{"grants": 1}, 7))

	entry, err := kv.Get(context.Background(), "cap.ephemeral.identity.A")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, float64(7), got["projectionSeq"], "guarded upsert must stamp projectionSeq into the body")
	require.Equal(t, float64(1), got["grants"], "guarded upsert must round-trip the row")
}

func TestNatsKVAdapter_Guarded_RejectsLowerSeqUpsert(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "cap.ephemeral.identity.B"}

	// A newer projection lands at seq 10.
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"grants": "fresh"}, 10))
	// A stale replay at seq 5 must be dropped as an idempotent no-op.
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"grants": "stale"}, 5))

	entry, err := kv.Get(ctx, "cap.ephemeral.identity.B")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, "fresh", got["grants"], "lower-seq replay must not overwrite a newer projection")
	require.Equal(t, float64(10), got["projectionSeq"])
}

func TestNatsKVAdapter_Guarded_DeleteWritesTombstoneWithWatermark(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "my-tasks.identity.C"}

	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"openTasks": 1}, 3))
	// Close (delete) at a higher seq must soft-tombstone, not physically remove.
	require.NoError(t, a.Delete(ctx, keys, 8))

	entry, err := kv.Get(ctx, "my-tasks.identity.C")
	require.NoError(t, err, "guarded delete must leave a tombstone, not remove the key")
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, true, got["isDeleted"], "guarded delete must be a soft tombstone")
	require.Equal(t, float64(8), got["projectionSeq"], "tombstone must carry the watermark")
}

// TestNatsKVAdapter_Guarded_StaleReplayCannotResurrectTombstone is the unit-level
// shape of the resurrection bug: an open-era upsert (low seq) replayed AFTER a
// close-era delete (higher seq) must NOT bring the key back to a live state.
func TestNatsKVAdapter_Guarded_StaleReplayCannotResurrectTombstone(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "cap.ephemeral.identity.D"}

	// Open era: a grant projection lands at seq 4 (this is the captured retry).
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"grants": "present"}, 4))
	// Close era: zero grants → soft-tombstone at seq 9.
	require.NoError(t, a.Delete(ctx, keys, 9))
	// The retry of the open-era upsert fires LAST, replaying the original seq 4.
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"grants": "present"}, 4))

	entry, err := kv.Get(ctx, "cap.ephemeral.identity.D")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, true, got["isDeleted"], "stale replay must not resurrect the revoked grant")
	require.Equal(t, float64(9), got["projectionSeq"], "watermark must remain at the close-era seq")
}

func TestNatsKVAdapter_Guarded_EqualSeqIsNoOp(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "cap.ephemeral.identity.E"}

	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"v": "first"}, 5))
	// Equal seq must be treated as already-applied (idempotent no-op).
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"v": "second"}, 5))

	entry, err := kv.Get(ctx, "cap.ephemeral.identity.E")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, "first", got["v"], "equal-seq write must be a no-op")
}

func TestNatsKVAdapter_Unguarded_IgnoresProjectionSeq(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"}) // not guarded
	ctx := context.Background()
	keys := map[string]any{"key": "plain.A"}

	// Unguarded: last-writer-wins regardless of seq ordering; no projectionSeq
	// field is injected into the body.
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"v": "high"}, 10))
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"v": "low"}, 1))

	entry, err := kv.Get(ctx, "plain.A")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	require.Equal(t, "low", got["v"], "unguarded adapter must keep last-writer-wins")
	_, hasSeq := got["projectionSeq"]
	require.False(t, hasSeq, "unguarded adapter must not inject projectionSeq")
}

func TestNatsKVAdapter_Probe_Success(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})
	err := a.Probe(context.Background())
	assert.NoError(t, err, "Probe should succeed on a live bucket")
}

func TestNatsKVAdapter_Close(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})
	assert.NoError(t, a.Close())
}
