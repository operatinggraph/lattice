package bridge_test

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/bridge"
)

// TestFakeAsyncCheck_ExecuteIsPendingWithDeterministicRef: Execute always returns
// a Pending Dispatch carrying a vendor Ref that is deterministic in the
// idempotencyKey (so a redelivery yields the same Ref), and is idempotent — a
// repeat Execute on the same key returns the same Ref with NO second side-effect.
func TestFakeAsyncCheck_ExecuteIsPendingWithDeterministicRef(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeAsyncCheck(1)
	req := bridge.Request{IdempotencyKey: "claim-async-1", Subject: "vtx.identity.abc"}

	first, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	if first.Disposition != bridge.Pending {
		t.Fatalf("Disposition = %v, want Pending", first.Disposition)
	}
	if first.Ref == "" {
		t.Fatal("a Pending dispatch must carry a vendor Ref")
	}
	if got := a.SideEffects("claim-async-1"); got != 1 {
		t.Fatalf("after first Execute: side effects = %d, want 1", got)
	}

	second, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute repeat: %v", err)
	}
	if second.Ref != first.Ref {
		t.Fatalf("repeat Execute returned a different Ref: %q vs %q", second.Ref, first.Ref)
	}
	if got := a.SideEffects("claim-async-1"); got != 1 {
		t.Fatalf("after repeat Execute: side effects = %d, want 1 (no second side-effect)", got)
	}
}

// TestFakeAsyncCheck_PollPendingThenResolved: Poll returns Pending for the first
// PollsUntilResolved calls on a ref, then a Resolved Dispatch with a terminal
// OutcomeCompleted Result; once resolved it stays resolved on every later Poll.
func TestFakeAsyncCheck_PollPendingThenResolved(t *testing.T) {
	t.Parallel()
	const pollsUntilResolved = 2
	a := bridge.NewFakeAsyncCheck(pollsUntilResolved)

	exec, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "claim-async-2"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ref := exec.Ref

	for i := 0; i < pollsUntilResolved; i++ {
		d, err := a.Poll(context.Background(), ref)
		if err != nil {
			t.Fatalf("Poll %d: %v", i+1, err)
		}
		if d.Disposition != bridge.Pending {
			t.Fatalf("Poll %d: Disposition = %v, want Pending (still in flight)", i+1, d.Disposition)
		}
	}

	resolved, err := a.Poll(context.Background(), ref)
	if err != nil {
		t.Fatalf("Poll resolve: %v", err)
	}
	if resolved.Disposition != bridge.Resolved {
		t.Fatalf("after %d pending polls: Disposition = %v, want Resolved", pollsUntilResolved, resolved.Disposition)
	}
	if resolved.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("resolved Result.Status = %q, want %q", resolved.Result.Status, bridge.OutcomeCompleted)
	}

	// Idempotent once resolved: a later Poll returns the same Resolved Result.
	again, err := a.Poll(context.Background(), ref)
	if err != nil {
		t.Fatalf("Poll again: %v", err)
	}
	if again.Disposition != bridge.Resolved || again.Result != resolved.Result {
		t.Fatalf("a resolved ref must stay resolved: got %+v, want %+v", again, resolved)
	}
}

// TestFakeAsyncCheck_ResolvesOnFirstPollWhenZero: pollsUntilResolved == 0 means
// the first Poll already resolves (the default-fast configuration).
func TestFakeAsyncCheck_ResolvesOnFirstPollWhenZero(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeAsyncCheck(0)
	exec, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "claim-async-3"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	d, err := a.Poll(context.Background(), exec.Ref)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d.Disposition != bridge.Resolved {
		t.Fatalf("with pollsUntilResolved=0 the first Poll must resolve, got %v", d.Disposition)
	}
}
