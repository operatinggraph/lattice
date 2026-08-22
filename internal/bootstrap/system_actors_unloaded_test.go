package bootstrap

import (
	"context"
	"errors"
	"testing"
)

// TestSystemActorKeys_UnloadedIdentifiersError asserts the whole predicate
// refuses to run before Load/LoadOrGenerate has populated the identifier
// table. RoleOperatorID is the entire matcher for the holdsRole→operator
// predicate (system_actors.go:52), so an empty one matches no link and yields
// an empty set — a result no caller can distinguish from "this deployment has
// no system actors", and one that silently routes the primordial admin and
// every kernel service actor as an ordinary actor at each consumer.
//
// The nil conn is the second half of the assertion: the guard must fire before
// any substrate call, so a caller wired wrong learns from the error rather
// than from an expensive listing that returns nothing.
//
// The positive vector — a populated table reaching the substrate and
// returning the six primordial operator holders — is
// TestSystemActorKeys_DiscoversByOperatorTopology (system_actors_test.go:24),
// so this guard cannot be masking a permanent refusal.
//
// This is an internal test because it drives a package var. No test in this
// package runs in parallel, and the original value is restored, so the
// mutation is invisible to the rest of the run.
func TestSystemActorKeys_UnloadedIdentifiersError(t *testing.T) {
	saved := RoleOperatorID
	RoleOperatorID = ""
	t.Cleanup(func() { RoleOperatorID = saved })

	keys, err := SystemActorKeys(context.Background(), nil)
	if !errors.Is(err, ErrPrimordialIDsUnloaded) {
		t.Fatalf("err = %v, want it to wrap ErrPrimordialIDsUnloaded", err)
	}
	if keys != nil {
		t.Errorf("keys = %v, want nil alongside the error", keys)
	}
}
