// Package bypass holds the outcome-level adversarial residual for the
// Capability Lens security plane — assemblies that don't reduce to one
// mechanism's colocated white-box test.
//
// The adjacency-watch resurrection vector below: the adjacency-watch path
// (handleAdjUpdate) writes guarded keys with no stream sequence, so an
// adjacency reprojection firing after a close could in principle resurrect a
// revoked ephemeral grant. Defense (Story 12.1a): every guarded per-actor
// write carries projectionSeq = the JetStream stream sequence of the
// triggering CDC message; the adjacency-watch path's sequence-less write
// (seq 0) is dropped unconditionally as a fail-closed no-op against any real
// tombstone watermark (Contract #6 §6.2, §6.8).
package bypass

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// resurrectionEphKey is the disjoint ephemeral key the adjacency-watch vector
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

// TestCapAdv_V5_AdjWatch_CannotAdvanceWatermark proves the adjacency-watch
// resurrection vector is closed. The adjacency-watch path is not
// message-driven (no stream sequence). A guarded write attempted there would
// carry seq 0, which the guard drops unconditionally as a fail-closed no-op —
// a sequence-less write carries no ordering, so it can neither overwrite a
// close-era tombstone nor create a clobberable seq-0 key. The pipeline
// additionally skips guarded-key writes on the adjacency-watch path entirely
// (the stream consumer owns the watermark); the adapter-level seq-0 drop is
// the backstop.
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
