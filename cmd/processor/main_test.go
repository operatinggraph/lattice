package main

// The kernel topology link set is fail-OPEN when unset: an empty set protects
// no link, which is deliberately what every processor fixture that does not
// wire one gets (the fail-closed reading — "every link is protected" — would
// refuse every link write in the deployment). Nothing downstream can therefore
// notice a wiring that quietly drops it: the pipeline builds, every op commits,
// and the only symptom is that RevokeRole on the primordial admin's own
// holdsRole edge succeeds again.
//
// So the wiring is pinned here instead, at the one place production composes
// it, through the same load the binary performs at start-up.

import (
	"errors"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func TestBuildAuthWiring_WiresTheLoadedKernelTopologyLinkSet(t *testing.T) {
	// A real load of a real table, the way the binary starts up: reaching an
	// id-keyed composition through hand-set package vars is how a caller ends
	// up asserting over a table that was never loaded.
	testutil.EnsurePrimordials(t)

	want, err := bootstrap.KernelTopologyLinkKeys()
	if err != nil {
		t.Fatalf("KernelTopologyLinkKeys: %v", err)
	}

	wiring, err := buildAuthWiring([]string{"vtx.identity." + bootstrap.BootstrapIdentityID})
	if err != nil {
		t.Fatalf("buildAuthWiring: %v", err)
	}

	if len(wiring.KernelLinkKeys) == 0 {
		t.Fatal("KernelLinkKeys is empty after Load — the guard would protect no link in production")
	}
	if len(wiring.KernelLinkKeys) != len(want) {
		t.Fatalf("wired %d kernel link keys, KernelTopologyLinkKeys names %d", len(wiring.KernelLinkKeys), len(want))
	}
	for i := range want {
		if wiring.KernelLinkKeys[i] != want[i] {
			t.Errorf("kernel link key %d: wired %s, want %s", i, wiring.KernelLinkKeys[i], want[i])
		}
	}
}

// An unloaded table must stop start-up rather than wire an empty set. The
// positive vector above is what proves this refusal is not permanent.
//
// The refusal is asserted by CLASS, not as "some error came back": the wiring
// wraps bootstrap's own sentinel, and a start-up path that one day failed here
// for an unrelated reason would satisfy a bare non-nil check while proving
// nothing about the unloaded table.
func TestBuildAuthWiring_RefusesAnUnloadedTable(t *testing.T) {
	// Populate first, so the table this blanks one identifier of is a real one
	// and the refusal cannot be an artefact of nothing having ever loaded.
	testutil.EnsurePrimordials(t)

	saved := bootstrap.RoleOperatorID
	bootstrap.RoleOperatorID = ""
	t.Cleanup(func() { bootstrap.RoleOperatorID = saved })

	_, err := buildAuthWiring(nil)
	if err == nil {
		t.Fatal("buildAuthWiring succeeded on an unloaded table, want a refusal")
	}
	if !errors.Is(err, bootstrap.ErrPrimordialIDsUnloaded) {
		t.Fatalf("err = %v, want it to wrap bootstrap.ErrPrimordialIDsUnloaded", err)
	}
	if !strings.Contains(err.Error(), "roleOperator") {
		t.Errorf("err = %q, want it to name the identifier that was empty", err)
	}
}

// TestBuildAuthWiring_PrimordialActorsAreExactlyTheTwoEngines pins the
// MEMBERSHIP of the admitted set, not just that the map is populated.
//
// The set is load-bearing twice over. It is the `primordialActor` global the
// seven package-authored actor pins compare against, and it is the admitted set
// of the Processor's egress admission predicate
// (processor.HydratorImpl.PrimordialActors): an operation may declare a
// class-(f) egress read only if its actor is a member, so a member is a party
// the platform will carry data outside the platform on behalf of.
//
// Nothing else stands in front of this literal. verify-kernel checks
// bootstrap.PrimordialVertexKeys() — the kernel's KV keys — not this map, and
// no lint reads it, so without this test adding a third engine is a one-line
// edit with no signal at all. The test does not decide how wide the set should
// be; it makes changing the width a deliberate, visible edit.
func TestBuildAuthWiring_PrimordialActorsAreExactlyTheTwoEngines(t *testing.T) {
	testutil.EnsurePrimordials(t)

	wiring, err := buildAuthWiring([]string{"vtx.identity." + bootstrap.BootstrapIdentityID})
	if err != nil {
		t.Fatalf("buildAuthWiring: %v", err)
	}

	want := map[string]string{
		"loom":   bootstrap.LoomIdentityKey,
		"weaver": bootstrap.WeaverIdentityKey,
	}
	if len(wiring.PrimordialActors) != len(want) {
		t.Fatalf("PrimordialActors = %v, want exactly %d members (%v) — widening the admitted set widens who may declare an egress read",
			wiring.PrimordialActors, len(want), want)
	}
	for name, key := range want {
		// Non-empty first: an unloaded key would wire "" for the name, and the
		// equality below would then pass by agreeing on nothing.
		if strings.TrimSpace(key) == "" {
			t.Fatalf("bootstrap key for %q is empty — the table did not load, so this assertion would compare two blanks", name)
		}
		got, ok := wiring.PrimordialActors[name]
		if !ok {
			t.Fatalf("PrimordialActors = %v, want a %q entry", wiring.PrimordialActors, name)
		}
		if got != key {
			t.Errorf("PrimordialActors[%q] = %q, want the bootstrap-derived %q", name, got, key)
		}
	}
}
