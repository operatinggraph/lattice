package bridge_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/bridge"
)

// TestFakeBackgroundCheck_ClearedStatusCompleted: a normal check clears with the
// terminal OutcomeCompleted verdict.
func TestFakeBackgroundCheck_ClearedStatusCompleted(t *testing.T) {
	a := bridge.NewFakeBackgroundCheck()
	res, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "bg-ok", Subject: "vtx.identity.normal"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Disposition != bridge.Resolved {
		t.Fatalf("a synchronous check must return Resolved, got %v", res.Disposition)
	}
	if res.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("Status = %q, want %q", res.Result.Status, bridge.OutcomeCompleted)
	}
}

// TestFakeBackgroundCheck_DeclineIsTerminalFailure: a Request whose Subject is
// the decline trigger returns a terminal OutcomeFailed with err == nil (a
// definitive rejection, not a transient error), memoized so a repeat key replays
// the same verdict.
func TestFakeBackgroundCheck_DeclineIsTerminalFailure(t *testing.T) {
	a := bridge.NewFakeBackgroundCheck()
	req := bridge.Request{IdempotencyKey: "bg-declined", Subject: bridge.BackgroundCheckDeclineSubject}

	res, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("a decline is a terminal verdict, not a transient error: %v", err)
	}
	if res.Result.Status != bridge.OutcomeFailed {
		t.Fatalf("Status = %q, want %q", res.Result.Status, bridge.OutcomeFailed)
	}

	res2, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("repeat Execute: %v", err)
	}
	if res2.Result != res.Result {
		t.Fatalf("repeat decline returned a different Result: %+v vs %+v", res2.Result, res.Result)
	}
}
