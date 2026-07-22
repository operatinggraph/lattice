// Package bypass holds the outcome-level adversarial residual for the
// Capability Lens security plane — assemblies that don't reduce to one
// mechanism's colocated white-box test.
//
// Guarded-projection rebuild integrity.
//
// This vector proves the rebuild path on a GUARDED bucket is both correct and
// safe, covering the primary capability lens (cap.identity.<id>) as well as the
// disjoint guarded keys:
//
//	(a) Restore: a rebuild of a guarded bucket carrying live HIGH-seq watermarks
//	    correctly restores every key when the historical LOWER-seq events replay.
//	    The force-truncate rule (Story 12.1b) clears the watermarks with the data
//	    so the first replay write wins — no rejected-write holes. Without the
//	    truncate, the guard would reject the lower-seq replays against the live
//	    high-seq watermarks and silently restore nothing.
//
//	(b) Resurrection-safety: a stale captured retry that fires DURING/AFTER the
//	    rebuild (replaying an open-era upsert at its original lower seq, after the
//	    close-era tombstone has been re-established by the replay) cannot resurrect
//	    a revoked grant. The guard stays always-on across the rebuild (force-truncate
//	    keeps the guard enabled — there is no bypass window the concurrent
//	    retry-queue writer could exploit).
//
// DEFENDED when: (a) the post-rebuild bucket is key-equal to a from-scratch
// projection AND (b) the post-rebuild stale retry is rejected (the primary
// cap.identity.<id> key stays a tombstone at the close-era watermark).
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// rebuildPrimaryKey is the PRIMARY per-actor capability doc key the rebuild
// vector targets (Contract #1 / §6.2 key shape). It is distinct from the
// disjoint ephemeral key vector #5 uses, exercising the primary lens now guarded
// by Story 12.1b.
const rebuildPrimaryKey = "cap.identity." + capadvNanoID1

// primaryGrantRow is the open-era primary-capability body: a live platform
// permission present for the actor. As with vector #5 the field shape is
// immaterial to the guard — what matters is that "isDeleted" is absent (a live
// doc) versus the close-era tombstone.
func primaryGrantRow() map[string]any {
	return map[string]any{
		"key":     rebuildPrimaryKey,
		"actor":   "vtx.identity." + capadvNanoID1,
		"version": "1.0",
		"platformPermissions": []any{
			map[string]any{"operationType": "ApproveLeaseApplication", "scope": "platform"},
		},
	}
}

// liveDocPresent reports whether the persisted key surfaces a live capability doc
// (no isDeleted) to a reader. An absent key or an isDeleted tombstone is no live
// doc.
func liveDocPresent(t *testing.T, ctx context.Context, kv *substrate.KV, key string) bool {
	t.Helper()
	entry, err := kv.Get(ctx, key)
	if err != nil {
		return false
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v6: unmarshal persisted body: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); isDeleted {
		return false
	}
	perms, _ := body["platformPermissions"].([]any)
	return len(perms) > 0
}

// dumpGuardedBucket reads every live (non-tombstone) key in the bucket and
// returns a stable JSON map of key→decoded-body for equivalence comparison.
func dumpGuardedBucket(t *testing.T, ctx context.Context, kv *substrate.KV) string {
	t.Helper()
	keys, err := kv.ListKeys(ctx)
	if err != nil {
		t.Fatalf("v6: list keys: %v", err)
	}
	out := map[string]any{}
	for _, k := range keys {
		entry, err := kv.Get(ctx, k)
		if err != nil {
			continue // purged/absent
		}
		var v map[string]any
		if err := json.Unmarshal(entry.Value, &v); err != nil {
			t.Fatalf("v6: unmarshal %s: %v", k, err)
		}
		if del, _ := v["isDeleted"].(bool); del {
			continue
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("v6: marshal dump: %v", err)
	}
	return string(b)
}

// historicalRebuildEvent is one event in the rebuild's stream-ordered replay.
type historicalRebuildEvent struct {
	key string
	row map[string]any
	del bool
	seq uint64
}

// rebuildHistoricalStream is the set of events (in seq order) the rebuild replays.
// It covers the primary key plus two more actors, with the primary actor revoked
// in the open era and re-granted, exercising the upsert/tombstone interleave a
// real rebuild reconstructs.
func rebuildHistoricalStream() []historicalRebuildEvent {
	keyB := "cap.identity." + capadvNanoID2
	keyC := "cap.identity." + capadvNanoID3
	rowB := map[string]any{"key": keyB, "actor": "vtx.identity." + capadvNanoID2, "version": "1.0",
		"platformPermissions": []any{map[string]any{"operationType": "ReadLease", "scope": "platform"}}}
	rowC := map[string]any{"key": keyC, "actor": "vtx.identity." + capadvNanoID3, "version": "1.0",
		"platformPermissions": []any{map[string]any{"operationType": "ListLease", "scope": "platform"}}}
	return []historicalRebuildEvent{
		{key: rebuildPrimaryKey, row: primaryGrantRow(), seq: 1},
		{key: keyB, row: rowB, seq: 2},
		{key: keyC, row: rowC, seq: 3},
		{key: rebuildPrimaryKey, row: primaryGrantRow(), seq: 4}, // re-projected at a higher seq
	}
}

func replayHistorical(t *testing.T, ctx context.Context, adpt *adapter.NatsKVAdapter, stream []historicalRebuildEvent) {
	t.Helper()
	for _, e := range stream {
		var err error
		if e.del {
			err = adpt.Delete(ctx, map[string]any{"key": e.key}, e.seq)
		} else {
			err = adpt.Upsert(ctx, map[string]any{"key": e.key}, e.row, e.seq)
		}
		if err != nil {
			t.Fatalf("v6: replay %s (seq %d): %v", e.key, e.seq, err)
		}
	}
}

// TestCapAdv_V6_GuardedRebuild_RestoresEveryKey is the (a) restore proof: a
// rebuild of a guarded bucket carrying live HIGH-seq watermark state correctly
// restores every key (force-truncate then replay), producing a bucket key-equal
// to a from-scratch projection. The fail-without half is asserted inline: a
// lower-seq replay against the un-truncated live watermark is rejected (a hole).
func TestCapAdv_V6_GuardedRebuild_RestoresEveryKey(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	js := conn.JetStream()
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v6: open capability-kv: %v", err)
	}

	guarded, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v6: build guarded adapter: %v", err)
	}
	guarded.SetGuarded(true)

	stream := rebuildHistoricalStream()

	// Fail-without proof: seed a live HIGH-seq doc, then replay a historical
	// LOWER-seq event WITHOUT truncating. The guard rejects it → the live doc is
	// unchanged (the hole the force-truncate removes).
	require := func(cond bool, msg string) {
		if !cond {
			t.Fatal(msg)
		}
	}
	require(guarded.Upsert(ctx, map[string]any{"key": rebuildPrimaryKey},
		map[string]any{"key": rebuildPrimaryKey, "actor": "vtx.identity." + capadvNanoID1, "version": "1.0",
			"platformPermissions": []any{map[string]any{"operationType": "LIVE-HIGH-SEQ", "scope": "platform"}}}, 500) == nil,
		"v6: live high-seq seed failed")
	if err := guarded.Upsert(ctx, map[string]any{"key": rebuildPrimaryKey}, primaryGrantRow(), 4); err != nil {
		t.Fatalf("v6: lower-seq replay (no truncate) errored: %v", err)
	}
	entry, err := kv.Get(ctx, rebuildPrimaryKey)
	if err != nil {
		t.Fatalf("v6: get after no-truncate replay: %v", err)
	}
	var noTruncBody map[string]any
	if err := json.Unmarshal(entry.Value, &noTruncBody); err != nil {
		t.Fatalf("v6: unmarshal: %v", err)
	}
	perms, _ := noTruncBody["platformPermissions"].([]any)
	first, _ := perms[0].(map[string]any)
	if first["operationType"] != "LIVE-HIGH-SEQ" {
		t.Fatalf("v6 fail-without: a lower-seq replay against a live high-seq watermark must be rejected (the hole); got %v", first["operationType"])
	}
	t.Logf("v6 fail-without: lower-seq replay against the un-truncated live watermark is rejected (rejected-write hole confirmed)")

	// Pass-with proof: force-truncate the bucket, then replay the historical
	// stream. The result must be key-equal to a from-scratch projection.
	if err := guarded.Truncate(ctx); err != nil {
		t.Fatalf("v6: truncate: %v", err)
	}
	replayHistorical(t, ctx, guarded, stream)
	liveDump := dumpGuardedBucket(t, ctx, kv)

	// Fresh from-scratch projection of the same stream into an empty guarded bucket.
	// Mirror the harness bucket's LimitMarkerTTL so the two buckets are configured
	// identically and the equality assertion compares like with like.
	freshBucket := "capability-kv-fresh"
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: freshBucket, LimitMarkerTTL: time.Second}); err != nil {
		t.Fatalf("v6: create fresh bucket: %v", err)
	}
	freshKV, err := conn.OpenKV(ctx, freshBucket)
	if err != nil {
		t.Fatalf("v6: open fresh bucket: %v", err)
	}
	fresh, err := adapter.New(freshKV, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v6: build fresh adapter: %v", err)
	}
	fresh.SetGuarded(true)
	replayHistorical(t, ctx, fresh, stream)
	freshDump := dumpGuardedBucket(t, ctx, freshKV)

	if liveDump != freshDump {
		t.Fatalf("v6: post-rebuild bucket must equal a from-scratch projection (no holes)\n got:  %s\n want: %s", liveDump, freshDump)
	}
	if !liveDocPresent(t, ctx, kv, rebuildPrimaryKey) {
		t.Fatalf("v6: the primary cap.identity key must be restored after rebuild")
	}
	t.Logf("v6 DEFENDED (a): guarded rebuild force-truncates and restores every key — post-rebuild == from-scratch")
}

// TestCapAdv_V6_GuardedRebuild_ConcurrentStaleRetryCannotResurrect is the (b)
// resurrection-safety proof: after a rebuild re-establishes a close-era tombstone
// on the primary key, a stale captured retry (open-era upsert at its original
// lower seq) firing AFTER the rebuild cannot resurrect the revoked grant — the
// guard stays always-on across the rebuild (force-truncate has no bypass window).
func TestCapAdv_V6_GuardedRebuild_ConcurrentStaleRetryCannotResurrect(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v6: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v6: build adapter: %v", err)
	}
	adpt.SetGuarded(true)

	keys := map[string]any{"key": rebuildPrimaryKey}
	const openSeq = uint64(10)
	const closeSeq = uint64(20)

	// Rebuild from empty: the stream replays the open-era grant (seq 10) then the
	// close-era revoke (seq 20 → tombstone).
	if err := adpt.Truncate(ctx); err != nil {
		t.Fatalf("v6: truncate: %v", err)
	}
	if err := adpt.Upsert(ctx, keys, primaryGrantRow(), openSeq); err != nil {
		t.Fatalf("v6: replay open-era upsert: %v", err)
	}
	if err := adpt.Delete(ctx, keys, closeSeq); err != nil {
		t.Fatalf("v6: replay close-era delete: %v", err)
	}

	// A stale captured retry fires AFTER the rebuild, replaying the open-era
	// upsert at its original lower seq. The guard must reject it.
	if err := adpt.Upsert(ctx, keys, primaryGrantRow(), openSeq); err != nil {
		t.Fatalf("v6: post-rebuild stale retry upsert errored (should be a no-op): %v", err)
	}

	if liveDocPresent(t, ctx, kv, rebuildPrimaryKey) {
		t.Fatalf("v6: a post-rebuild stale retry must NOT resurrect the revoked primary capability doc")
	}
	entry, err := kv.Get(ctx, rebuildPrimaryKey)
	if err != nil {
		t.Fatalf("v6: primary key must remain a tombstone, not be removed: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v6: unmarshal tombstone: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); !isDeleted {
		t.Fatalf("v6: primary key must stay an isDeleted tombstone, got %+v", body)
	}
	if seq, _ := body["projectionSeq"].(float64); uint64(seq) != closeSeq {
		t.Fatalf("v6: tombstone watermark must remain the close-era seq (%d), got %v", closeSeq, body["projectionSeq"])
	}
	t.Logf("v6 DEFENDED (b): the guard stays always-on across rebuild; a post-rebuild stale retry cannot resurrect the primary doc")
}
