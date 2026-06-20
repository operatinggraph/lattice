package bridge_test

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/bridge"
)

func TestRegistry_RegisterAndLookup(t *testing.T) {
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
	r := bridge.NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("Lookup: want ok=false for an unregistered adapter (surfaced as a config error, never silent)")
	}
}

func TestRegistry_RejectsBlankNameNilAndDuplicate(t *testing.T) {
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
