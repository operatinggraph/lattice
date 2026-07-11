package bridge_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/bridge"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
	t.Parallel()
	r := bridge.NewRegistry()
	a := bridge.NewFakeBackgroundCheck()
	if err := r.Register("backgroundCheck", a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Lookup("backgroundCheck")
	if !ok {
		t.Fatal("Lookup: want ok=true for a registered adapter")
	}
	if got != a {
		t.Fatal("Lookup: returned a different adapter than was registered")
	}
}

func TestRegistry_LookupMissingIsConfigError(t *testing.T) {
	t.Parallel()
	r := bridge.NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("Lookup: want ok=false for an unregistered adapter (surfaced as a config error, never silent)")
	}
}

func TestRegistry_RejectsBlankNameNilAndDuplicate(t *testing.T) {
	t.Parallel()
	r := bridge.NewRegistry()
	if err := r.Register("", bridge.NewFakeBackgroundCheck()); err == nil {
		t.Error("Register: want error for a blank name")
	}
	if err := r.Register("x", nil); err == nil {
		t.Error("Register: want error for a nil adapter")
	}
	if err := r.Register("dup", bridge.NewFakeBackgroundCheck()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register("dup", bridge.NewFakeBackgroundCheck()); err == nil {
		t.Error("Register: want error re-registering an already-bound name")
	}
}

// TestFakeBackgroundCheck_IdempotentOnRepeatedKey is the literal proof of
// external idempotency: a repeat idempotencyKey returns the SAME Result and
// performs NO second side-effect.
func TestFakeBackgroundCheck_IdempotentOnRepeatedKey(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeBackgroundCheck()
	req := bridge.Request{IdempotencyKey: "claim-1", Subject: "vtx.identity.abc"}

	first, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	if first.Disposition != bridge.Resolved {
		t.Fatalf("a synchronous adapter must return a Resolved disposition, got %v", first.Disposition)
	}
	if got := a.SideEffects("claim-1"); got != 1 {
		t.Fatalf("after first Execute: side effects = %d, want 1", got)
	}

	second, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute repeat: %v", err)
	}
	if got := a.SideEffects("claim-1"); got != 1 {
		t.Fatalf("after repeat Execute: side effects = %d, want 1 (no second side-effect)", got)
	}
	if first.Result != second.Result {
		t.Fatalf("repeat Execute returned a different Result: %+v vs %+v", first.Result, second.Result)
	}
}

func TestFakeBackgroundCheck_DistinctKeysEachActOnce(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeBackgroundCheck()
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

// TestAdapterFunc_ExecuteDelegatesToTheWrappedFunction proves AdapterFunc is a
// transparent adapter over a plain synchronous function.
func TestAdapterFunc_ExecuteDelegatesToTheWrappedFunction(t *testing.T) {
	t.Parallel()
	var gotReq bridge.Request
	f := bridge.AdapterFunc(func(_ context.Context, req bridge.Request) (bridge.Dispatch, error) {
		gotReq = req
		return bridge.Dispatch{Disposition: bridge.Resolved, Result: bridge.Result{Status: bridge.OutcomeCompleted}}, nil
	})
	req := bridge.Request{IdempotencyKey: "k1", Subject: "vtx.identity.x"}
	d, err := f.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if d.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("Status = %q, want %q", d.Result.Status, bridge.OutcomeCompleted)
	}
	if gotReq.IdempotencyKey != req.IdempotencyKey || gotReq.Subject != req.Subject {
		t.Fatalf("the wrapped function did not receive the same Request: got %+v, want %+v", gotReq, req)
	}
}

// TestAdapterFunc_PollUnsupported: a function-only adapter is synchronous by
// construction (it never returns Pending), so its Poll is unreachable in
// practice — it must return a clear error rather than a silent zero Dispatch.
func TestAdapterFunc_PollUnsupported(t *testing.T) {
	t.Parallel()
	f := bridge.AdapterFunc(func(_ context.Context, _ bridge.Request) (bridge.Dispatch, error) {
		return bridge.Dispatch{}, nil
	})
	if _, err := f.Poll(context.Background(), "some-ref"); err == nil {
		t.Fatal("Poll: want an error for a synchronous AdapterFunc, got nil")
	}
}
