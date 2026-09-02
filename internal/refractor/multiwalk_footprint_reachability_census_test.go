// Multi-walk / footprint-validation reachability census —
// refractor-hub-walk-and-periodic-load-design.md §9.2.
//
// mergeFootprints (internal/refractor/pipeline/branchmerge.go) folds a
// multi-walk lens's per-branch footprints into one certificate and reports a
// disagreement between branches as EvalFootprint.Torn, which footprintValid
// rejects. That mechanism can only ever FIRE for a lens that is both
// multi-walk (ruleState.branches > 1, so executeBranches takes the branch
// loop and calls the merge at all) and footprint-validating
// (pipeline.needsFootprintValidation: an actor-aggregate envelope, an
// auth-plane target, and a multi-binding conjunct unit).
//
// The design asked the builder to pin whether the corpus holds such a lens.
// It does not — and the reason is structural rather than accidental, which is
// the part worth pinning: the two populations are kept disjoint by two
// separate guards, and this census fails the moment either one moves. A
// zero-row table over shipped cypher would not: it would keep passing
// vacuously while the guard that empties it was removed.
//
// WHEN THIS FAILS: a multi-walk lens has become reachable by footprint
// validation, and EvalFootprint.Torn is now live rather than latent. That is
// not a defect — the mechanism is built and tested — but the design's
// reachability claim, and the cost note that goes with it (a torn footprint
// costs a re-execution), are now wrong and need re-reading.
package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// corpusMultiWalkLenses returns every shipped lens whose declaration composes
// into more than one branch, by asking pkgmgr's own composition rather than by
// reading cypher: ExpandReadGrantWalks is the pass cmd/refractor runs, and
// SpecBranches is exactly what reaches lens.LensSpec.CypherBranches and, from
// there, ruleState.branches.
func corpusMultiWalkLenses(t *testing.T) map[string]pkgmgr.LensSpec {
	t.Helper()
	out := map[string]pkgmgr.LensSpec{}
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		require.Truef(t, ok, "registered package %q must resolve", name)
		expanded, err := def.ExpandReadGrantWalks()
		require.NoErrorf(t, err, "%s read-grant walks must compose", name)
		for _, l := range expanded.Lenses {
			if len(l.SpecBranches) > 1 {
				out[l.CanonicalName] = l
			}
		}
	}
	return out
}

// TestCorpusMultiWalk_IsNeverFootprintValidating pins the empty intersection
// and, for each multi-walk lens, WHICH conjunct of needsFootprintValidation it
// fails. Two of the three fail for every one of them, so the emptiness does
// not rest on a single guard.
func TestCorpusMultiWalk_IsNeverFootprintValidating(t *testing.T) {
	multiWalk := corpusMultiWalkLenses(t)

	// A population floor, so the intersection cannot go empty because the
	// multi-walk population did. These three are the corpus's only multi-walk
	// lenses; the count is the assertion, the names locate a change.
	require.Len(t, multiWalk, 3,
		"the multi-walk population changed — got %v", keysOf(multiWalk))
	for _, name := range []string{"edgeCatalog", "edgeTasks", "edgeEntitySessions"} {
		require.Containsf(t, multiWalk, name, "%s must still compose to multiple branches", name)
	}

	for name, l := range multiWalk {
		// Conjunct 2 (auth-plane). pkgmgr refuses Walks on any lens that is
		// not a Personal nats-subject lens, and a Personal lens's runtime
		// target is nats_subject — which satisfies neither arm of
		// IsAuthPlane (nats_kv into capability-kv, or postgres with a grant
		// table). Asserted through the rule the census builds the same way
		// cmd/refractor does, not through the declaration.
		rule := corpusLensRule(t, name, l)
		require.Truef(t, projection.IsPersonalLens(rule),
			"%s is multi-walk, so pkgmgr's parseWalks must have required it to be a Personal nats-subject lens", name)
		require.Falsef(t, projection.IsAuthPlane(rule),
			"%s is now auth-plane: a multi-walk lens can reach footprint validation", name)

		// Conjunct 3 (requiresFootprintValidation), whose only setter is
		// projection.InstallActorAggregate. A Personal lens takes a different
		// install arm, and TestCorpusLensRule_MatchesTheInstallSwitch pins
		// corpus-wide that the two kinds never coincide — so it suffices here
		// to pin that this lens is not declared actor-aggregate.
		require.NotEqualf(t, projection.ActorAggregateKind, l.ProjectionKind,
			"%s is multi-walk AND actor-aggregate: the install arms are supposed to be exclusive", name)
	}
}

// TestMultiWalkRequiresAPersonalLens pins the guard that makes the whole
// argument above hold: a lens declaring more than one Walk is refused outright
// unless it is a Personal nats-subject lens. Without it, an auth-plane
// actor-aggregate lens could declare two walks and land squarely in the
// intersection.
//
// The positive control is the point of the second half: the SAME declaration
// with the Personal nats-subject flags set does compose to two branches, so
// the refusal above is about those flags and not about the fixture being
// malformed some other way.
func TestMultiWalkRequiresAPersonalLens(t *testing.T) {
	walks := []pkgmgr.AnchorWalk{
		{GrantDomain: "base", AnchorType: "task", AnchorVar: "t",
			Chain: []string{"(identity)<-[:assignedTo]-(t:task)"}},
		{GrantDomain: "staff", AnchorType: "task", AnchorVar: "t",
			Chain: []string{"(identity)-[:worksAt]->(w)", "(w)<-[:queuedFor]-(t:task)"}},
	}
	definitionWith := func(adapter string, personal bool) pkgmgr.Definition {
		return pkgmgr.Definition{
			Name:             "fixture",
			Version:          "1.0.0",
			ReadGrantDomains: []pkgmgr.ReadGrantDomainSpec{{Name: "base"}, {Name: "staff"}},
			Lenses: []pkgmgr.LensSpec{{
				CanonicalName: "multi",
				Class:         "meta.lens",
				Adapter:       adapter,
				SubjectPrefix: "lattice.sync.user",
				Stream:        "SYNC",
				Personal:      personal,
				Engine:        "full",
				IntoKey:       []string{"__actor", "ns", "entityId"},
				Walks:         walks,
				Spec:          "\nRETURN t.key AS anchor\n",
			}},
		}
	}

	for _, tc := range []struct {
		name     string
		adapter  string
		personal bool
	}{
		{"nats-kv actor-aggregate shape", "", false},
		{"postgres shape", "postgres", false},
		{"nats-subject but not Personal", "nats-subject", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := definitionWith(tc.adapter, tc.personal).ExpandReadGrantWalks()
			require.Error(t, err, "Walks on a non-Personal lens must be refused, not composed")
			require.ErrorContains(t, err, "not a Personal (nats-subject) lens")
		})
	}

	t.Run("Personal nats-subject composes", func(t *testing.T) {
		expanded, err := definitionWith("nats-subject", true).ExpandReadGrantWalks()
		require.NoError(t, err)
		require.Len(t, expanded.Lenses[0].SpecBranches, 2,
			"the control: the same walks DO compose to two branches for a Personal lens")
	})
}

// TestIsAuthPlane_RecognizesBothArms is the other control: the auth-plane
// assertion above is a negative, and a negative proves nothing if the
// predicate answers false for everything. Both arms must still answer true
// for the shapes they exist to catch.
func TestIsAuthPlane_RecognizesBothArms(t *testing.T) {
	require.True(t, projection.IsAuthPlane(&lens.Rule{
		Into: lens.IntoConfig{Target: "nats_kv", Bucket: projection.AuthPlaneBucket},
	}), "a nats_kv lens writing the capability bucket is auth-plane")
	require.True(t, projection.IsAuthPlane(&lens.Rule{
		Into: lens.IntoConfig{Target: "postgres", GrantTable: true},
	}), "a postgres lens writing a grant table is auth-plane")
}

// keysOf renders a map's keys for a failure message.
func keysOf(m map[string]pkgmgr.LensSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
