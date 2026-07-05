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

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// startKV starts an in-memory NATS server with JetStream and returns a KV bucket for testing.
func startKV(t *testing.T) *substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
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

	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "test-target"})
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	kv, err := conn.OpenKV(context.Background(), "test-target")
	require.NoError(t, err)
	return kv
}

// newAdapter is a test helper that requires New to succeed, using the default
// hard delete mode.
func newAdapter(t *testing.T, kv *substrate.KV, keyOrder []string) *adapter.NatsKVAdapter {
	return newAdapterMode(t, kv, keyOrder, adapter.DeleteModeHard)
}

// newAdapterMode is like newAdapter but lets the caller choose the delete mode.
func newAdapterMode(t *testing.T, kv *substrate.KV, keyOrder []string, mode adapter.DeleteMode) *adapter.NatsKVAdapter {
	t.Helper()
	a, err := adapter.New(kv, keyOrder, mode)
	require.NoError(t, err)
	return a
}

// dumpBucket reads every live key in the bucket and returns a stable JSON map of
// key→decoded-value, excluding tombstones (purged/absent keys read as
// ErrKeyNotFound; tombstones are skipped so two buckets that differ only in
// physical tombstone presence still compare equal on their live contents).
func dumpBucket(t *testing.T, ctx context.Context, kv *substrate.KV) string {
	t.Helper()
	keys, err := kv.ListKeys(ctx)
	require.NoError(t, err)
	out := map[string]any{}
	for _, k := range keys {
		entry, err := kv.Get(ctx, k)
		if err != nil {
			require.ErrorIs(t, err, substrate.ErrKeyNotFound)
			continue
		}
		var v map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &v))
		if del, _ := v["isDeleted"].(bool); del {
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	require.NoError(t, err)
	return string(b)
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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

	// The default hard mode physically removes the key.
	_, err := kv.Get(context.Background(), "e1")
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
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
	require.Contains(t, string(entry.Value), `"isDeleted":true`)
}

func TestNatsKVAdapter_Delete_NeverExisted_Hard(t *testing.T) {
	kv := startKV(t)
	a := newAdapterMode(t, kv, []string{"id"}, adapter.DeleteModeHard)

	// Hard-deleting a key that was never upserted must succeed (no-op /
	// idempotent): substrate.ErrKeyNotFound is swallowed.
	err := a.Delete(context.Background(), map[string]any{"id": "ghost"}, 0)
	require.NoError(t, err)

	// And the key must still be absent afterwards.
	_, gErr := kv.Get(context.Background(), "ghost")
	require.ErrorIs(t, gErr, substrate.ErrKeyNotFound)
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
func guardedAdapter(t *testing.T, kv *substrate.KV, keyOrder []string) *adapter.NatsKVAdapter {
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
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
	require.NoError(t, json.Unmarshal(entry.Value, &got))
	require.Equal(t, "low", got["v"], "unguarded adapter must keep last-writer-wins")
	_, hasSeq := got["projectionSeq"]
	require.False(t, hasSeq, "unguarded adapter must not inject projectionSeq")
}

// TestNatsKVAdapter_Truncate_GetReturnsKeyNotFound proves the property the
// force-truncate correctness depends on: after Truncate purges a key, a
// subsequent Get returns ErrKeyNotFound (so a guarded rebuild takes the
// absent→Create path and never reads a stale watermark).
func TestNatsKVAdapter_GetRow_AbsentKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"})
	row, ok, err := a.GetRow(context.Background(), map[string]any{"key": "flow.abc"})
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, row)
}

func TestNatsKVAdapter_GetRow_RoundTripsUnguardedRow(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "flow.abc"}
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"status": "running"}, 0))

	row, ok, err := a.GetRow(ctx, keys)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "running", row["status"])
}

func TestNatsKVAdapter_GetRow_StripsProjectionSeqFromGuardedRow(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "flow.abc"}
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"status": "running"}, 5))

	row, ok, err := a.GetRow(ctx, keys)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "running", row["status"])
	_, hasSeq := row["projectionSeq"]
	assert.False(t, hasSeq, "GetRow must strip the guard's internal projectionSeq bookkeeping field")
}

func TestNatsKVAdapter_GetRow_TombstoneReadsAsAbsent(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()
	keys := map[string]any{"key": "flow.abc"}
	require.NoError(t, a.Upsert(ctx, keys, map[string]any{"status": "running"}, 5))
	require.NoError(t, a.Delete(ctx, keys, 8))

	row, ok, err := a.GetRow(ctx, keys)
	require.NoError(t, err)
	require.False(t, ok, "a soft-delete tombstone must read as absent, not as a live row")
	require.Nil(t, row)
}

func TestNatsKVAdapter_ListKeys_CompositeKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"app_id", "landlord_id"})
	ctx := context.Background()

	require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": "appA", "landlord_id": "lordX"}, map[string]any{"v": 1}, 0))
	require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": "appB", "landlord_id": "lordY"}, map[string]any{"v": 2}, 0))

	got, err := a.ListKeys(ctx)
	require.NoError(t, err)
	want := []map[string]any{
		{"app_id": "appA", "landlord_id": "lordX"},
		{"app_id": "appB", "landlord_id": "lordY"},
	}
	assert.ElementsMatch(t, want, got)
}

func TestNatsKVAdapter_ListKeys_ExcludesHardDeleted(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"})
	ctx := context.Background()

	require.NoError(t, a.Upsert(ctx, map[string]any{"key": "cap.identity.A"}, map[string]any{"v": 1}, 0))
	require.NoError(t, a.Upsert(ctx, map[string]any{"key": "cap.identity.B"}, map[string]any{"v": 2}, 0))
	require.NoError(t, a.Delete(ctx, map[string]any{"key": "cap.identity.A"}, 0))

	got, err := a.ListKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{{"key": "cap.identity.B"}}, got)
}

func TestNatsKVAdapter_ListKeys_EmptyBucket(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"})
	got, err := a.ListKeys(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestNatsKVAdapter_Truncate_GetReturnsKeyNotFound(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()

	require.NoError(t, a.Upsert(ctx, map[string]any{"key": "cap.identity.A"}, map[string]any{"v": 1}, 5))
	require.NoError(t, a.Upsert(ctx, map[string]any{"key": "cap.identity.B"}, map[string]any{"v": 2}, 6))
	// A tombstone too — Truncate must clear it like any other key.
	require.NoError(t, a.Delete(ctx, map[string]any{"key": "cap.identity.C"}, 7))

	require.NoError(t, a.Truncate(ctx))

	for _, k := range []string{"cap.identity.A", "cap.identity.B", "cap.identity.C"} {
		_, err := kv.Get(ctx, k)
		require.ErrorIs(t, err, substrate.ErrKeyNotFound, "key %s must read absent after Truncate", k)
	}
}

func TestNatsKVAdapter_Truncate_EmptyBucket(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	require.NoError(t, a.Truncate(context.Background()), "Truncate on an empty bucket is a no-op")
}

// TestNatsKVAdapter_Truncate_RebuildEquivalence is the AC#3 rebuild-equivalence
// proof at the adapter level: a guarded bucket carrying live HIGH-seq watermarks
// (an upsert at a high seq + a tombstone) is force-truncated and then the
// historical LOWER-seq events replay. The result must be key-equal to projecting
// those same lower-seq events into a fresh empty bucket — every key present, none
// missing, no stale tombstone left behind. Without the Truncate the guard would
// reject the lower-seq replays against the live watermarks (rejected-write holes);
// with it the first replay write wins.
func TestNatsKVAdapter_Truncate_RebuildEquivalence(t *testing.T) {
	ctx := context.Background()

	// The "historical" stream, in seq order, that a rebuild replays.
	type ev struct {
		key string
		row map[string]any
		del bool
		seq uint64
	}
	historical := []ev{
		{key: "cap.identity.A", row: map[string]any{"grants": "a1"}, seq: 1},
		{key: "cap.identity.B", row: map[string]any{"grants": "b1"}, seq: 2},
		{key: "cap.identity.A", row: map[string]any{"grants": "a2"}, seq: 3},
		{key: "cap.identity.C", row: map[string]any{"grants": "c1"}, seq: 4},
	}
	replay := func(a *adapter.NatsKVAdapter) {
		for _, e := range historical {
			if e.del {
				require.NoError(t, a.Delete(ctx, map[string]any{"key": e.key}, e.seq))
			} else {
				require.NoError(t, a.Upsert(ctx, map[string]any{"key": e.key}, e.row, e.seq))
			}
		}
	}

	// Fresh bucket: project the historical events into an empty guarded bucket.
	freshKV := startKV(t)
	fresh := guardedAdapter(t, freshKV, []string{"key"})
	replay(fresh)

	// Live bucket: seed HIGH-seq watermark state, then force-truncate + replay.
	liveKV := startKV(t)
	live := guardedAdapter(t, liveKV, []string{"key"})
	require.NoError(t, live.Upsert(ctx, map[string]any{"key": "cap.identity.A"}, map[string]any{"grants": "live-A"}, 100))
	require.NoError(t, live.Delete(ctx, map[string]any{"key": "cap.identity.B"}, 101)) // live tombstone at high seq
	require.NoError(t, live.Upsert(ctx, map[string]any{"key": "cap.identity.Z"}, map[string]any{"grants": "gone"}, 102))
	require.NoError(t, live.Truncate(ctx))
	replay(live)

	require.JSONEq(t,
		dumpBucket(t, ctx, freshKV),
		dumpBucket(t, ctx, liveKV),
		"post-rebuild bucket must be key-equal to a from-scratch projection (no holes, no stale tombstone)",
	)
}

// TestNatsKVAdapter_Truncate_RebuildEquivalence_FailsWithoutTruncate pins the AC#3
// requirement that the test FAILS without the force-truncate: replaying the
// lower-seq historical events against the live high-seq watermarks leaves holes.
func TestNatsKVAdapter_Truncate_RebuildEquivalence_FailsWithoutTruncate(t *testing.T) {
	ctx := context.Background()
	liveKV := startKV(t)
	live := guardedAdapter(t, liveKV, []string{"key"})

	// Live high-seq state.
	require.NoError(t, live.Upsert(ctx, map[string]any{"key": "cap.identity.A"}, map[string]any{"grants": "live-A"}, 100))
	// Replay a historical LOWER-seq event WITHOUT truncating first.
	require.NoError(t, live.Upsert(ctx, map[string]any{"key": "cap.identity.A"}, map[string]any{"grants": "a2"}, 3))

	entry, err := liveKV.Get(ctx, "cap.identity.A")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &got))
	require.Equal(t, "live-A", got["grants"],
		"without force-truncate the lower-seq replay is rejected by the guard (the hole this story removes)")
}

// TestNatsKVAdapter_RoleIndexLens_Unguarded documents the capabilityRoleIndex
// exclusion (Contract #6 §6.2/§6.3): the role-index lens is keyed by
// operationType (cap.role-by-operation.<op>), an operation-aggregate with no
// per-actor revoke→resurrect race, so its adapter is left unguarded. The wiring
// (cmd/refractor/main.go case "capabilityRoleIndex") deliberately omits the
// enableProjectionGuard call, leaving the adapter in its default state. This
// asserts the default — a guard-family-membership guardrail so a future careless
// edit that flips it is caught.
func TestNatsKVAdapter_RoleIndexLens_Unguarded(t *testing.T) {
	kv := startKV(t)
	// The role-index adapter is built like any other default lens adapter:
	// New + no SetGuarded(true) call.
	a := newAdapter(t, kv, []string{"operationType"})
	require.False(t, a.Guarded(), "capabilityRoleIndex must remain unguarded (operation-aggregate, not actor-aggregate)")
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
