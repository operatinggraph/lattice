package loom

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestDeriveRequestID_DeterministicAndValid proves the write-ahead token is a
// valid Contract #1 NanoID and is stable for a given (instanceId, cursor) —
// the property that makes systemOp re-attempt idempotent (AC #6).
func TestDeriveRequestID_DeterministicAndValid(t *testing.T) {
	t.Parallel()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatal(err)
	}
	a := deriveRequestID(id, 0)
	b := deriveRequestID(id, 0)
	if a != b {
		t.Fatalf("deriveRequestID not deterministic: %q != %q", a, b)
	}
	if !substrate.IsValidNanoID(a) {
		t.Fatalf("deriveRequestID produced invalid NanoID: %q", a)
	}
	// Different cursors must produce different tokens.
	if deriveRequestID(id, 0) == deriveRequestID(id, 1) {
		t.Fatal("cursor 0 and 1 produced the same token")
	}
	// Different instances must produce different tokens.
	id2, _ := substrate.NewNanoID()
	if deriveRequestID(id, 0) == deriveRequestID(id2, 0) {
		t.Fatal("distinct instances produced the same token")
	}
}

// TestDeriveTaskID_DeterministicValidAndDisjoint proves the userTask taskId is a
// valid stable NanoID AND is disjoint from the same step's CreateTask requestId
// — the two derivations are the completion-correlation handle and the submission
// idempotency handle respectively, and must never collide.
func TestDeriveTaskID_DeterministicValidAndDisjoint(t *testing.T) {
	t.Parallel()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatal(err)
	}
	a := deriveTaskID(id, 0)
	if a != deriveTaskID(id, 0) {
		t.Fatal("deriveTaskID not deterministic")
	}
	if !substrate.IsValidNanoID(a) {
		t.Fatalf("deriveTaskID produced invalid NanoID: %q", a)
	}
	if deriveTaskID(id, 0) == deriveTaskID(id, 1) {
		t.Fatal("cursor 0 and 1 produced the same taskId")
	}
	// Disjoint from the same step's CreateTask requestId.
	if deriveTaskID(id, 0) == deriveRequestID(id, 0) {
		t.Fatal("taskId collided with the step's requestId (handles must be disjoint)")
	}
}

// TestDeriveInstanceID_DeterministicValidAndDisjoint proves the externalTask
// instance handle is a valid stable bare NanoID, that the three derivations for
// the same (instanceId, cursor) are mutually distinct (so the parked handle and
// the instanceOp's own submission requestId never collide), and that the handle
// is dot-free so it is NOT a userTask token (it routes to the systemOp-style
// deadline probe, never the userTask creation-probe).
func TestDeriveInstanceID_DeterministicValidAndDisjoint(t *testing.T) {
	t.Parallel()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatal(err)
	}
	a := deriveInstanceID(id, 0)
	if a != deriveInstanceID(id, 0) {
		t.Fatal("deriveInstanceID not deterministic")
	}
	if !substrate.IsValidNanoID(a) {
		t.Fatalf("deriveInstanceID produced invalid NanoID: %q", a)
	}
	if deriveInstanceID(id, 0) == deriveInstanceID(id, 1) {
		t.Fatal("cursor 0 and 1 produced the same instance handle")
	}
	// The three derivations for the same (instanceId, cursor) must be mutually
	// distinct: requestId (submission idempotency handle), taskId, and the
	// instance handle each live in their own namespace.
	req := deriveRequestID(id, 0)
	task := deriveTaskID(id, 0)
	if a == req || a == task || req == task {
		t.Fatalf("the three derivations collided: requestId=%q taskId=%q instance=%q", req, task, a)
	}
	// The handle is a bare NanoID (dot-free), so isUserTaskToken is false — the
	// externalTask token is disjoint from the vtx.task.* userTask namespace.
	if isUserTaskToken(a) {
		t.Fatalf("instance handle %q must not be a userTask token (it must route to the systemOp-style probe)", a)
	}
}
