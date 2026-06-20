package bridge_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/bridge"
)

// TestFakeStripe_IdempotentOnRepeatedKey is the literal proof of external
// idempotency: a repeat idempotencyKey returns the SAME Result and performs NO
// second side-effect (the FR58 charge that must never double-bill).
func TestFakeStripe_IdempotentOnRepeatedKey(t *testing.T) {
	a := bridge.NewFakeStripe()
	req := bridge.Request{IdempotencyKey: "claim-1", Subject: "vtx.leaseApp.abc"}

	first, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	if first.Disposition != bridge.Resolved {
		t.Fatalf("a synchronous charge must return Resolved, got %v", first.Disposition)
	}
	if got := a.SideEffects("claim-1"); got != 1 {
		t.Fatalf("after first Execute: side effects = %d, want 1", got)
	}

	second, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute repeat: %v", err)
	}
	if got := a.SideEffects("claim-1"); got != 1 {
		t.Fatalf("after repeat Execute: side effects = %d, want 1 (no second charge)", got)
	}
	if first.Result != second.Result {
		t.Fatalf("repeat Execute returned a different Result: %+v vs %+v", first.Result, second.Result)
	}
}

func TestFakeStripe_DistinctKeysEachChargeOnce(t *testing.T) {
	a := bridge.NewFakeStripe()
	if _, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	if a.SideEffects("k1") != 1 || a.SideEffects("k2") != 1 {
		t.Fatalf("distinct keys: k1=%d k2=%d, want 1 each", a.SideEffects("k1"), a.SideEffects("k2"))
	}
}

// TestFakeStripe_FailNextChargesNothingThenRetrySucceedsOnce proves the failure
// mode the FR58 idempotency proof relies on: the first Execute hard-fails WITHOUT
// charging (zero side-effect), and a retry on the SAME key then succeeds with
// exactly one side-effect — the failed attempt did not bill, so the eventual
// single success is the only charge.
func TestFakeStripe_FailNextChargesNothingThenRetrySucceedsOnce(t *testing.T) {
	a := bridge.NewFakeStripe()
	a.FailNext()
	req := bridge.Request{IdempotencyKey: "claim-x", Subject: "vtx.leaseApp.xyz"}

	if _, err := a.Execute(context.Background(), req); err == nil {
		t.Fatal("first Execute: want an injected failure")
	}
	if got := a.SideEffects("claim-x"); got != 0 {
		t.Fatalf("a failed charge must record NO side-effect, got %d", got)
	}

	res, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("retry Execute: %v", err)
	}
	if got := a.SideEffects("claim-x"); got != 1 {
		t.Fatalf("after retry: side effects = %d, want exactly 1", got)
	}
	if res.Result.Detail == "" {
		t.Fatal("successful charge must carry a confirmation Detail")
	}
}

// TestFakeStripe_HappyPathStatusCompleted: a normal charge carries the terminal
// OutcomeCompleted verdict (the {completed,failed} vocabulary the replyOp acts
// on, distinct from the free-form Detail).
func TestFakeStripe_HappyPathStatusCompleted(t *testing.T) {
	a := bridge.NewFakeStripe()
	res, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "k-ok", Subject: "vtx.identity.normal"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("Status = %q, want %q", res.Result.Status, bridge.OutcomeCompleted)
	}
}

// TestFakeStripe_DeclineIsTerminalFailureNoCharge: a Request whose Subject is the
// decline trigger returns a terminal OutcomeFailed with err == nil (a verdict,
// not a transient error) and performs NO charge (SideEffects stays 0). The
// decline is memoized, so a repeat key replays the same Failed verdict — never a
// retry into a charge.
func TestFakeStripe_DeclineIsTerminalFailureNoCharge(t *testing.T) {
	a := bridge.NewFakeStripe()
	req := bridge.Request{IdempotencyKey: "k-declined", Subject: bridge.StripeDeclineSubject}

	res, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("a decline is a terminal verdict, not a transient error: %v", err)
	}
	if res.Result.Status != bridge.OutcomeFailed {
		t.Fatalf("Status = %q, want %q", res.Result.Status, bridge.OutcomeFailed)
	}
	if got := a.SideEffects("k-declined"); got != 0 {
		t.Fatalf("a declined charge must record NO side-effect, got %d", got)
	}

	// A repeat key replays the same Failed verdict (memoized), still no charge.
	res2, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("repeat Execute: %v", err)
	}
	if res2.Result != res.Result {
		t.Fatalf("repeat decline returned a different Result: %+v vs %+v", res2.Result, res.Result)
	}
	if got := a.SideEffects("k-declined"); got != 0 {
		t.Fatalf("repeat decline must still record NO side-effect, got %d", got)
	}
}

// TestFakeStripe_FailUntilFailsNThenSucceeds proves the fail-n toggle spans
// multiple attempts before the real charge lands.
func TestFakeStripe_FailUntilFailsNThenSucceeds(t *testing.T) {
	a := bridge.NewFakeStripe()
	a.FailUntil(2)
	req := bridge.Request{IdempotencyKey: "claim-n"}

	for i := 0; i < 2; i++ {
		if _, err := a.Execute(context.Background(), req); err == nil {
			t.Fatalf("attempt %d: want an injected failure", i+1)
		}
	}
	if got := a.SideEffects("claim-n"); got != 0 {
		t.Fatalf("two failed charges must record NO side-effect, got %d", got)
	}
	if _, err := a.Execute(context.Background(), req); err != nil {
		t.Fatalf("third Execute: %v", err)
	}
	if got := a.SideEffects("claim-n"); got != 1 {
		t.Fatalf("after the fail window closes: side effects = %d, want 1", got)
	}
}
