// Package bypass — Phase 1 Gate 3: Capability Lens adversarial test suite.
//
// Vector #5 — Stale projection resurrection (captured-retry + adjacency-watch).
//
// Attack (the confirmed-reachable D-INTEGRITY bug): Refractor's retry queue
// captures an evaluation RESULT (a row), not a re-evaluation. An open-era grant
// projection (Upsert) fails transiently and is captured. The task then closes;
// the close reprojects the actor to zero grants → Delete. The captured open-era
// Upsert is then replayed by the retry goroutine AFTER the delete, re-writing the
// revoked grant. No further CDC event re-deletes it (the task is already closed),
// so the revoked ephemeral grant is resurrected on the security plane.
//
// A second, independent vector is the adjacency-watch path (handleAdjUpdate),
// which writes guarded keys with no stream sequence; an adjacency reprojection
// firing after a close could likewise resurrect the key.
//
// Defense (Story 12.1a): every guarded per-actor write carries projectionSeq =
// the JetStream stream sequence of the triggering CDC message. The NATS-KV
// adapter writes conditionally (CAS); a lower-or-equal-seq replay is dropped as
// an idempotent no-op, and a Delete becomes a soft tombstone carrying the
// watermark so the high-water mark survives physical absence (Contract #6 §6.2,
// §6.8). The adjacency-watch path skips guarded-key writes entirely — only a
// stream-sequenced write may advance or clear a guarded watermark.
//
// DEFENDED when: the stale lower-seq replay of the open-era Upsert cannot
// overwrite the close-era tombstone (the key stays an isDeleted tombstone with
// the close-era watermark). The fail-without/pass-with structure is explicit:
// the identical chain against an UNGUARDED adapter (today's main behavior)
// resurrects the grant; against the GUARDED adapter it is rejected.
//
// Report row:
//
//	Vector #5 | Stale projection resurrection (retry + adj-watch) | DEFENDED | Refractor monotonic projection-write guard (projectionSeq CAS + soft tombstone; §6.2/§6.8)
package bypass

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// resurrectionEphKey is the disjoint ephemeral key the captured-retry chain
// targets (Contract #1 / §6.6 key shape).
const resurrectionEphKey = "cap.ephemeral.identity." + capadvNanoID6

// openGrantRow is the open-era projection body: a live ephemeral grant present
// for the actor. The exact field shape is immaterial to the guard — what matters
// is that "isDeleted" is absent (a live grant) versus the close-era tombstone.
func openGrantRow() map[string]any {
	return map[string]any{
		"key":   resurrectionEphKey,
		"actor": "vtx.identity." + capadvNanoID6,
		"ephemeralGrants": []any{
			map[string]any{
				"taskKey":       "vtx.task." + capadvNanoID7,
				"operationType": "ApproveLeaseApplication",
				"target":        "vtx.leaseApp." + capadvNanoID8,
			},
		},
	}
}

// runCapturedRetryChain replays the exact captured-retry ordering against adpt:
//
//  1. open-era Upsert at the (lower) open stream sequence — the captured result;
//  2. close-era Delete at a (higher) close stream sequence — task closed, zero
//     grants → ErrDeleteProjection → Delete;
//  3. the retry goroutine fires LAST, replaying the captured open-era Upsert at
//     its ORIGINAL (lower) sequence.
//
// With a guarded adapter step 3 must lose; with an unguarded adapter step 3
// resurrects the grant (the bug).
func runCapturedRetryChain(t *testing.T, ctx context.Context, adpt *adapter.NatsKVAdapter) {
	t.Helper()
	const openSeq = uint64(100)
	const closeSeq = uint64(200)
	keys := map[string]any{"key": resurrectionEphKey}

	if err := adpt.Upsert(ctx, keys, openGrantRow(), openSeq); err != nil {
		t.Fatalf("v5: open-era upsert: %v", err)
	}
	if err := adpt.Delete(ctx, keys, closeSeq); err != nil {
		t.Fatalf("v5: close-era delete: %v", err)
	}
	// The captured open-era result replays at its original (lower) sequence.
	if err := adpt.Upsert(ctx, keys, openGrantRow(), openSeq); err != nil {
		t.Fatalf("v5: captured-retry replay upsert: %v", err)
	}
}

// liveGrantResurrected reports whether the persisted key would surface a live
// ephemeral grant to a reader (i.e. the resurrection succeeded). A live body
// (no isDeleted) with grants present means the revoked grant is back; an absent
// key or an isDeleted tombstone means no grant — the deny is intact.
func liveGrantResurrected(t *testing.T, ctx context.Context, kv *substrate.KV) bool {
	t.Helper()
	entry, err := kv.Get(ctx, resurrectionEphKey)
	if err != nil {
		return false // absent → no grant
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v5: unmarshal persisted body: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); isDeleted {
		return false // tombstone → no grant
	}
	grants, _ := body["ephemeralGrants"].([]any)
	return len(grants) > 0
}

// TestCapAdv_V5_StaleReplay_FailsWithoutGuard pins the bug: against the UNGUARDED
// adapter (today's main behavior, last-writer-wins) the captured-retry replay
// resurrects the revoked grant. This is the fail-without half of the
// fail-without/pass-with proof — it documents what main does wrong.
func TestCapAdv_V5_StaleReplay_FailsWithoutGuard(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v5: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v5: build adapter: %v", err)
	}
	// Guard NOT enabled — this is the main / pre-12.1a posture.

	runCapturedRetryChain(t, ctx, adpt)

	if !liveGrantResurrected(t, ctx, kv) {
		t.Fatalf("v5: expected the UNGUARDED adapter to resurrect the revoked grant (the bug); it did not — the fail-without proof is invalid")
	}
	t.Logf("v5 fail-without: UNGUARDED adapter resurrects the revoked grant (confirms the D-INTEGRITY bug on main)")
}

// TestCapAdv_V5_StaleReplay_DefendedWithGuard is the DEFENDED vector: with the
// monotonic projection-write guard the identical captured-retry chain cannot
// resurrect the grant — the close-era tombstone (higher watermark) survives the
// lower-seq replay.
func TestCapAdv_V5_StaleReplay_DefendedWithGuard(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v5: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v5: build adapter: %v", err)
	}
	adpt.SetGuarded(true) // 12.1a guard enabled

	runCapturedRetryChain(t, ctx, adpt)

	if liveGrantResurrected(t, ctx, kv) {
		t.Fatalf("v5: GUARDED adapter must NOT resurrect the revoked grant; the stale lower-seq replay was accepted")
	}

	// The key must remain an isDeleted tombstone carrying the close-era watermark.
	entry, err := kv.Get(ctx, resurrectionEphKey)
	if err != nil {
		t.Fatalf("v5: guarded delete must leave a tombstone, not remove the key: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v5: unmarshal tombstone: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); !isDeleted {
		t.Fatalf("v5: persisted body must be an isDeleted tombstone, got %+v", body)
	}
	if seq, _ := body["projectionSeq"].(float64); uint64(seq) != 200 {
		t.Fatalf("v5: tombstone watermark must remain the close-era seq (200), got %v", body["projectionSeq"])
	}
	t.Logf("v5 DEFENDED: monotonic projection-write guard rejects the stale lower-seq replay; key stays a tombstone at the close-era watermark")
}

// TestCapAdv_V5_AdjWatch_CannotAdvanceWatermark documents the second resurrection
// vector and its closure. The adjacency-watch path is not message-driven (no
// stream sequence). A guarded write attempted there would carry seq 0, which the
// guard drops unconditionally as a fail-closed no-op — a sequence-less write
// carries no ordering, so it can neither overwrite a close-era tombstone nor
// create a clobberable seq-0 key. The pipeline additionally skips guarded-key
// writes on the adjacency-watch path entirely (the stream consumer owns the
// watermark); the adapter-level seq-0 drop is the backstop.
func TestCapAdv_V5_AdjWatch_CannotAdvanceWatermark(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v5 adj: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v5 adj: build adapter: %v", err)
	}
	adpt.SetGuarded(true)

	keys := map[string]any{"key": resurrectionEphKey}
	// Close the actor's projection at a real stream sequence (tombstone).
	if err := adpt.Delete(ctx, keys, 50); err != nil {
		t.Fatalf("v5 adj: close delete: %v", err)
	}
	// An adjacency-watch reprojection would write with seq 0 (no stream message).
	// The guard must reject it against the seq-50 tombstone.
	if err := adpt.Upsert(ctx, keys, openGrantRow(), 0); err != nil {
		t.Fatalf("v5 adj: seq-0 upsert returned error (should be a silent no-op): %v", err)
	}

	if liveGrantResurrected(t, ctx, kv) {
		t.Fatalf("v5 adj: a seq-0 (adjacency-watch) write must NOT resurrect a tombstoned guarded key")
	}
	t.Logf("v5 adj-watch: a non-stream-sequenced (seq 0) write cannot advance or clear a guarded watermark")
}
