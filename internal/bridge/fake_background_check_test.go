package bridge_test

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/bridge"
)

// TestFakeBackgroundCheck_ClearedStatusCompleted: a normal check clears with the
// terminal OutcomeCompleted verdict.
func TestFakeBackgroundCheck_ClearedStatusCompleted(t *testing.T) {
	t.Parallel()
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
// TestFakeBackgroundCheck_DeclineAll_EverySubjectFails: blanket-decline mode (the
// BRIDGE_FAKE_DECLINE demo affordance) makes a NORMAL subject — not the per-subject
// trigger — return a terminal OutcomeFailed, so an operator can drive the
// declined-application experience live.
func TestFakeBackgroundCheck_DeclineAll_EverySubjectFails(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeBackgroundCheck()
	a.SetDeclineAll(true)
	res, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "bg-all", Subject: "vtx.identity.normal"})
	if err != nil {
		t.Fatalf("a decline is a terminal verdict, not a transient error: %v", err)
	}
	if res.Result.Status != bridge.OutcomeFailed {
		t.Fatalf("under declineAll a normal subject must fail: Status = %q, want %q", res.Result.Status, bridge.OutcomeFailed)
	}
}

func TestFakeBackgroundCheck_DeclineIsTerminalFailure(t *testing.T) {
	t.Parallel()
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

// TestFakeBackgroundCheck_PollUnsupported: this adapter is synchronous
// (Execute never returns Pending), so Poll must surface a clear error rather
// than silently resolving a ref it never issued.
func TestFakeBackgroundCheck_PollUnsupported(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeBackgroundCheck()
	if _, err := a.Poll(context.Background(), "some-ref"); err == nil {
		t.Fatal("Poll: want an error for a synchronous adapter, got nil")
	}
}
