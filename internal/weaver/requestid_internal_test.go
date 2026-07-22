package weaver

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestDeriveEpisodeRequestID_Deterministic proves the OCC/idempotency seam of
// AC 5: a crash-sim re-fire of the SAME dispatch episode (same target, entity,
// gap, mark create revision) reproduces the identical requestId — collapsing
// on the Contract #4 tracker — while a legitimately re-opened gap (new
// CAS-create → new revision) yields a NEW requestId.
func TestDeriveEpisodeRequestID_Deterministic(t *testing.T) {
	t.Parallel()
	a := deriveEpisodeRequestID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_onboarding", 7)
	b := deriveEpisodeRequestID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_onboarding", 7)
	if a != b {
		t.Fatalf("same episode must derive the same requestId: %q vs %q", a, b)
	}
	if !substrate.IsValidNanoID(a) {
		t.Fatalf("derived requestId %q is not a canonical 20-char NanoID", a)
	}

	reopened := deriveEpisodeRequestID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_onboarding", 8)
	if reopened == a {
		t.Fatalf("a re-opened gap (new mark revision) must derive a NEW requestId")
	}

	otherGap := deriveEpisodeRequestID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_payment", 7)
	if otherGap == a {
		t.Fatalf("a different gap column must derive a different requestId")
	}
	otherTarget := deriveEpisodeRequestID("targetB", "Lk2Pn6mQrtwzKbcXvP3T", "missing_onboarding", 7)
	if otherTarget == a {
		t.Fatalf("a different target must derive a different requestId")
	}
}

// TestDeriveStableTaskID_StableAndDisjoint proves the §10.3 idempotency seam for
// userTask dispatch: the assignTask task-id (a) is namespace-disjoint from the op
// requestId, (b) is STABLE across reclaims of the same open episode (same claimId
// ⇒ same taskId, independent of markRevision — that is what stops the 30-min
// duplicate), and (c) is FRESH across a close→reopen (a new claimId ⇒ a new
// taskId). The Loom instanceId derivation shares these properties and is disjoint
// from the task id.
func TestDeriveStableTaskID_StableAndDisjoint(t *testing.T) {
	t.Parallel()
	const claimA = "Lk2Pn6mQrtwzKbcXvP3T"
	req := deriveEpisodeRequestID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_signature", 3)
	task := deriveStableTaskID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_signature", claimA)
	if req == task {
		t.Fatalf("task id and op requestId must be namespace-disjoint, both were %q", req)
	}
	if !substrate.IsValidNanoID(task) {
		t.Fatalf("derived taskId %q is not a canonical 20-char NanoID", task)
	}
	// Stable across reclaims: same claimId derives the same taskId.
	if again := deriveStableTaskID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_signature", claimA); task != again {
		t.Fatalf("same open episode (claimId) must re-supply the same taskId: %q vs %q", task, again)
	}
	// Fresh across reopen: a new claimId derives a new taskId.
	const claimB = "Zz9Yx8Wv7Ut6Sr5Qp4N"
	if reopened := deriveStableTaskID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_signature", claimB); reopened == task {
		t.Fatalf("a reopened gap (new claimId) must derive a NEW taskId")
	}
	// The Loom instanceId derivation is disjoint from the task id for the same episode.
	inst := deriveStableInstanceID("targetA", "Lk2Pn6mQrtwzKbcXvP3T", "missing_signature", claimA)
	if inst == task {
		t.Fatalf("instanceId and taskId must be namespace-disjoint, both were %q", inst)
	}
	if !substrate.IsValidNanoID(inst) {
		t.Fatalf("derived instanceId %q is not a canonical 20-char NanoID", inst)
	}
}
