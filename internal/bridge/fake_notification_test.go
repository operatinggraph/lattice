package bridge_test

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/bridge"
)

// TestFakeNotification_IdempotentOnRepeatedKey is the literal proof of
// external idempotency: a repeat idempotencyKey returns the SAME Result and
// performs NO second side-effect (a redelivered reminder must not double-send).
func TestFakeNotification_IdempotentOnRepeatedKey(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeNotification()
	req := bridge.Request{IdempotencyKey: "vtx.appointment.abc:2026-07-01T15:00:00Z"}

	first, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	if first.Disposition != bridge.Resolved {
		t.Fatalf("a synchronous send must return Resolved, got %v", first.Disposition)
	}
	if first.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("Status = %q, want %q", first.Result.Status, bridge.OutcomeCompleted)
	}
	if got := a.SideEffects(req.IdempotencyKey); got != 1 {
		t.Fatalf("after first Execute: side effects = %d, want 1", got)
	}

	second, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute repeat: %v", err)
	}
	if got := a.SideEffects(req.IdempotencyKey); got != 1 {
		t.Fatalf("after repeat Execute: side effects = %d, want 1 (no second send)", got)
	}
	if first.Result != second.Result {
		t.Fatalf("repeat Execute returned a different Result: %+v vs %+v", first.Result, second.Result)
	}
}

// TestFakeNotification_DistinctKeysEachSendOnce proves a reschedule (which
// changes remindedFor and so mints a fresh idempotencyKey) sends again, and
// each distinct key is its own independent side-effect.
func TestFakeNotification_DistinctKeysEachSendOnce(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeNotification()
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

// TestFakeNotification_PollUnsupported proves the sync-only convention:
// Poll must never be reachable since Execute never returns Pending.
func TestFakeNotification_PollUnsupported(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeNotification()
	if _, err := a.Poll(context.Background(), "some-ref"); err == nil {
		t.Fatal("Poll must error: FakeNotification is synchronous-only")
	}
}
