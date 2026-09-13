package pipeline

// What a completed background check costs to project, measured on the shipped
// supersededBackgroundChecks pattern (packages/lease-signing/lenses.go) rather
// than argued from it.
//
// The lens binds the leaseServiceInstance meta from the ANCHOR's instanceOf hop
// and from nowhere else, and that is a cost decision: this walk follows every hop
// incident to the position it stands at, in both directions (HopIndex.StepsFrom),
// so a meta position reachable from the SUCCESSOR side would put every live
// inbound instanceOf edge of that meta one step behind any instance's `.outcome`
// write — every instance the package owns reprojected for one completion, against
// a read cap of DefaultDerivationReadCap and an actor-set ceiling above it. The
// shape the lens ships instead reaches exactly the applicant's own instances, and
// this test is what holds it there: restoring `(newer)-[:instanceOf]->(m)` puts
// the foreign applicant's instance in the derived set and fails here.
//
// The narrowing belongs to the DERIVATION, and the test measures that too: the
// BFS it replaces scopes by relation name alone and does cross the meta, so the
// applicant-scoped answer is what the acting mode (the built-in default) buys and
// what `off`/`shadow` or a declined derivation give up.
//
// Derivation is pattern- and type-directed: it prunes on relation name, arrow
// direction and the far end's key type, and never evaluates a WHERE predicate. So
// the fixture carries the graph shape alone — the class / canonicalName /
// completed-outcome conjuncts are proved where they are enforced, in
// packages/lease-signing's own rule-engine lens test.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
)

// supersededBgchecksSpec is the installed cypher, read off the package's own
// Lens declaration so this test cannot drift from what ships.
func supersededBgchecksSpec(t *testing.T) string {
	t.Helper()
	for _, l := range leasesigning.Lenses() {
		if l.CanonicalName == "supersededBackgroundChecks" {
			require.NotEmpty(t, l.Spec, "the shipped lens must carry a cypher")
			return l.Spec
		}
	}
	t.Fatal("packages/lease-signing declares no supersededBackgroundChecks lens")
	return ""
}

// supersededDerivationPipeline builds the actor-aware pipeline over the shipped
// cypher, with the service actor type the lens's anchor position carries (the
// derivation declines outright when the two disagree).
func supersededDerivationPipeline(t *testing.T, adjKV *substrate.KV) *Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(supersededBgchecksSpec(t))
	require.NoError(t, err, "the shipped supersededBackgroundChecks cypher must parse")
	p := &Pipeline{ruleID: "supersededBackgroundChecks", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "service"))
	p.UseFullEngine(eng, cr)
	return p
}

// TestDeriveAnchors_SupersededBgchecks_StaysInsideTheApplicant seeds the shape the
// cost question is about: one leaseServiceInstance meta M owning three background
// checks, two of them (A, B) providedTo applicant S1 and one (X) providedTo a
// different applicant S2. B completing — a `.outcome` aspect write on B — must
// derive S1's two instances and nothing else. X shares only the meta with them,
// and the meta is the position no service write may cross.
func TestDeriveAnchors_SupersededBgchecks_StaysInsideTheApplicant(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("m", "meta")
	f.vertex("s1", "identity")
	f.vertex("s2", "identity")
	f.vertex("a", "service")
	f.vertex("b", "service")
	f.vertex("x", "service")
	// Direction follows the cypher and Contract #1 §1.1: the later-arriving
	// instance sources both of its links.
	f.edge("instanceOf", "a", "m")
	f.edge("instanceOf", "b", "m")
	f.edge("instanceOf", "x", "m")
	f.edge("providedTo", "a", "s1")
	f.edge("providedTo", "b", "s1")
	f.edge("providedTo", "x", "s2")

	ctx := context.Background()
	p := supersededDerivationPipeline(t, adjKV)
	derived, ok, err := p.deriveAnchorsForAspect(ctx, p.ruleState(), f.key("b", "service")+".outcome")
	require.NoError(t, err)
	require.True(t, ok, "the shipped pattern must be derivable — a decline hands the event to the BFS below")
	require.ElementsMatch(t, []string{f.key("a", "service"), f.key("b", "service")}, derived,
		"one completion reprojects the applicant's own instances; X is another applicant's and shares only the meta")

	// The comparison that makes the line above a narrowing rather than a fixture
	// artifact, and that pins how far the narrowing reaches: the BFS this
	// derivation replaces prunes by relation NAME only, and instanceOf is in the
	// anchor's own scope, so it walks B → M → every owned instance and returns X
	// as well. So the applicant-scoped answer holds under the acting derivation
	// (the built-in default) and NOT under `off`/`shadow` or a declined
	// derivation, where this wider set is what reprojects.
	bfs, err := p.actorEnumerator.Enumerate(ctx, f.key("b", "service"), "service")
	require.NoError(t, err)
	require.Contains(t, bfs, f.key("x", "service"),
		"the BFS really does cross the meta — the derivation's exclusion of X is its own, and only its")
}
