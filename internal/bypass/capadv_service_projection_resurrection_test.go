// Package bypass — Phase 1 Gate 3: Capability Lens adversarial test suite.
//
// Vector #7 (service-plane resurrection) — Stale availableAt-era service grant
// replayed after a close-era tombstone.
//
// Attack: service-location's capabilityServiceAccess lens projects an actor's
// service access to the auth-plane key cap.svc.<actor> (bucket capability-kv).
// An availableAt-era Upsert (the actor's residence/availability granted access
// to a service) fails transiently and is captured by Refractor's retry queue.
// The topology then changes — the availability is withdrawn (e.g. an
// unavailableAt building-level marker, or the residence link removed) — and the
// actor reprojects to zero service access → Delete (tombstone). The captured
// availableAt-era Upsert then replays AFTER the delete. Without the guard it
// re-writes the revoked serviceAccess on the security plane; no further CDC
// event re-deletes it, so the withdrawn service grant is resurrected.
//
// Defense (Story 12.1a, lens-agnostic): the cap.svc key is an auth-plane key in
// the guarded bucket (capability-kv), so the monotonic projection-write guard is
// engaged. Every guarded per-actor write carries projectionSeq = the JetStream
// stream sequence of the triggering CDC message; the NATS-KV adapter writes
// conditionally (CAS) and drops a lower-or-equal-seq replay; a Delete becomes a
// soft tombstone carrying the watermark (Contract #6 §6.2 / §6.8). The guard is
// structural (keyed on the bucket + key + projectionSeq), not per-lens — it
// defends the cap.svc service-access key exactly as it defends the primary and
// cap.ephemeral keys.
//
// DEFENDED when: the stale lower-seq replay of the availableAt-era Upsert cannot
// overwrite the close-era tombstone (the key stays an isDeleted tombstone at the
// close-era watermark). The fail-without/pass-with structure is explicit: the
// identical chain against an UNGUARDED adapter resurrects the grant; against the
// GUARDED adapter it is rejected.
//
// Covered by the Vector #5 report row (Refractor monotonic projection-write
// guard) — this is that guard applied to the service-access (cap.svc) plane; it
// does not add a separate gate row.
package bypass

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// svcResurrectionKey is the auth-plane service-access key the captured-retry
// chain targets (Contract #1 / §6.5 key shape: cap.svc.identity.<NanoID>).
const svcResurrectionKey = "cap.svc.identity." + svcBleedActorID

// availableServiceRow is the availableAt-era projection body: a live service
// grant present for the actor. What matters to the guard is that "isDeleted" is
// absent (a live grant) versus the close-era tombstone.
func availableServiceRow() map[string]any {
	return map[string]any{
		"key":   svcResurrectionKey,
		"actor": "vtx.identity." + svcBleedActorID,
		"serviceAccess": []any{
			map[string]any{
				"service":     svcBleedServiceX,
				"resolvedVia": []any{"vtx.unit.penthouse"},
				"allowedOperations": []any{
					map[string]any{"operationType": svcBleedOpAllowed},
				},
			},
		},
	}
}

// runServiceCapturedRetryChain replays the captured-retry ordering against adpt
// for the cap.svc key:
//
//  1. availableAt-era Upsert at the (lower) open stream sequence — the captured
//     result;
//  2. close-era Delete at a (higher) sequence — availability withdrawn, zero
//     service access → ErrDeleteProjection → Delete;
//  3. the retry goroutine fires LAST, replaying the captured availableAt-era
//     Upsert at its ORIGINAL (lower) sequence.
//
// With a guarded adapter step 3 must lose; with an unguarded adapter step 3
// resurrects the service grant (the bug).
func runServiceCapturedRetryChain(t *testing.T, ctx context.Context, adpt *adapter.NatsKVAdapter) {
	t.Helper()
	const openSeq = uint64(100)
	const closeSeq = uint64(200)
	keys := map[string]any{"key": svcResurrectionKey}

	if err := adpt.Upsert(ctx, keys, availableServiceRow(), openSeq); err != nil {
		t.Fatalf("v7 svc: availableAt-era upsert: %v", err)
	}
	if err := adpt.Delete(ctx, keys, closeSeq); err != nil {
		t.Fatalf("v7 svc: close-era delete: %v", err)
	}
	// The captured availableAt-era result replays at its original (lower) sequence.
	if err := adpt.Upsert(ctx, keys, availableServiceRow(), openSeq); err != nil {
		t.Fatalf("v7 svc: captured-retry replay upsert: %v", err)
	}
}

// liveServiceGrantResurrected reports whether the persisted cap.svc key would
// surface a live service grant to a reader (i.e. the resurrection succeeded). A
// live body (no isDeleted) with serviceAccess present means the revoked grant is
// back; an absent key or an isDeleted tombstone means no grant — the deny holds.
func liveServiceGrantResurrected(t *testing.T, ctx context.Context, kv *substrate.KV) bool {
	t.Helper()
	entry, err := kv.Get(ctx, svcResurrectionKey)
	if err != nil {
		return false // absent → no grant
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v7 svc: unmarshal persisted body: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); isDeleted {
		return false // tombstone → no grant
	}
	access, _ := body["serviceAccess"].([]any)
	return len(access) > 0
}

// TestCapAdv_V7Svc_StaleReplay_FailsWithoutGuard pins the bug on the service
// plane: against the UNGUARDED adapter (last-writer-wins) the captured-retry
// replay resurrects the withdrawn service grant. This is the fail-without half
// of the fail-without/pass-with proof.
func TestCapAdv_V7Svc_StaleReplay_FailsWithoutGuard(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v7 svc: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v7 svc: build adapter: %v", err)
	}
	// Guard NOT enabled — the main / pre-12.1a posture.

	runServiceCapturedRetryChain(t, ctx, adpt)

	if !liveServiceGrantResurrected(t, ctx, kv) {
		t.Fatalf("v7 svc: expected the UNGUARDED adapter to resurrect the withdrawn service grant (the bug); it did not — the fail-without proof is invalid")
	}
	t.Logf("v7 svc fail-without: UNGUARDED adapter resurrects the withdrawn cap.svc grant (confirms the D-INTEGRITY bug on main)")
}

// TestCapAdv_V7Svc_StaleReplay_DefendedWithGuard is the DEFENDED vector: with
// the monotonic projection-write guard the identical captured-retry chain cannot
// resurrect the cap.svc grant — the close-era tombstone (higher watermark)
// survives the lower-seq replay. This proves the guard is engaged on the
// service-access (cap.svc) plane, not only the ephemeral/primary planes.
func TestCapAdv_V7Svc_StaleReplay_DefendedWithGuard(t *testing.T) {
	ctx, conn := setupCapAdvHarness(t)
	kv, err := conn.OpenKV(ctx, capadvCapBucket)
	if err != nil {
		t.Fatalf("v7 svc: open capability-kv: %v", err)
	}

	adpt, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("v7 svc: build adapter: %v", err)
	}
	adpt.SetGuarded(true) // 12.1a guard enabled

	runServiceCapturedRetryChain(t, ctx, adpt)

	if liveServiceGrantResurrected(t, ctx, kv) {
		t.Fatalf("v7 svc: GUARDED adapter must NOT resurrect the withdrawn service grant; the stale lower-seq replay was accepted")
	}

	// The key must remain an isDeleted tombstone carrying the close-era watermark.
	entry, err := kv.Get(ctx, svcResurrectionKey)
	if err != nil {
		t.Fatalf("v7 svc: guarded delete must leave a tombstone, not remove the key: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("v7 svc: unmarshal tombstone: %v", err)
	}
	if isDeleted, _ := body["isDeleted"].(bool); !isDeleted {
		t.Fatalf("v7 svc: persisted body must be an isDeleted tombstone, got %+v", body)
	}
	if seq, _ := body["projectionSeq"].(float64); uint64(seq) != 200 {
		t.Fatalf("v7 svc: tombstone watermark must remain the close-era seq (200), got %v", body["projectionSeq"])
	}
	t.Logf("v7 svc DEFENDED: monotonic projection-write guard rejects the stale lower-seq cap.svc replay; key stays a tombstone at the close-era watermark")
}
