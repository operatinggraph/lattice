// objectLiveness consumer-filter pin — untyped-hop-anchor-derivation-design.md
// §4.1/§11.3.
//
// The lens's cypher binds no relationship, which is what lets the narrowing
// gate (auth-plane-projection-latency-design.md §4.2) take the relation-narrowed
// branch with an EMPTY relation list: one label, one subject, and the `lnk.*`
// forms are not emitted at all. This file pins that decision against the SHIPPED
// declaration, driven through the production install, because the hop-less
// shape's whole value is the filter — not the projection, which reads the same
// off any pattern anchored on the object.
package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	objectsbase "github.com/operatinggraph/lattice/packages/objects-base"
)

// shippedLensSpec returns a package's declared LensSpec by canonical name. The
// declaration is the thing under test: a filter derived from a cypher retyped
// here would pin this file's arithmetic rather than what installs.
func shippedLensSpec(t *testing.T, lenses []pkgmgr.LensSpec, name string) pkgmgr.LensSpec {
	t.Helper()
	for _, l := range lenses {
		if l.CanonicalName == name {
			return l
		}
	}
	require.FailNowf(t, "missing lens", "the package must declare a %q lens", name)
	return pkgmgr.LensSpec{}
}

// TestObjectLiveness_ConsumerFilterIsRelationNarrowed asserts the DELIVERY
// decision, not a projected row: a projection assertion passes identically
// whether or not the narrowing happened, so it cannot see the filter at all.
//
// The sweep assertion is not incidental. A consumer filter that narrows too far
// is unrecoverable by revert — a JetStream filter update never rewinds the
// cursor — so the recovery path for anything the narrowed filter withholds is
// the convergence sweep or a rebuild. `objectLiveness` must be sweep-enrolled
// for the narrowing to be a trade rather than a hole, and the enrolment comes
// from the production installer here, not from the fixture.
func TestObjectLiveness_ConsumerFilterIsRelationNarrowed(t *testing.T) {
	l := shippedLensSpec(t, objectsbase.Lenses(), "objectLiveness")

	eng := full.New()
	cr, err := eng.Parse(l.Spec)
	require.NoError(t, err, "the shipped objectLiveness cypher must parse")
	rule := corpusLensRule(t, "objectLiveness", l)
	rule.CompiledRule = cr
	p := corpusInstalledPipeline(t, "objectLiveness", eng, cr, rule)

	filterSubjects, filterSubject, decision := p.ConsumerFilter()
	require.Empty(t, filterSubject,
		"objectLiveness must narrow, not fall back to the broad Core-KV filter")
	require.Equal(t, health.FilterModeNarrowedRelation, decision.Mode,
		"a pattern binding no relationship has an exhaustive (empty) relation set, "+
			"so the relation-narrowed branch is the one the gate takes")
	require.Equal(t, 1, decision.LabelCount,
		"the pattern references exactly one label, the anchor's own")

	// Exactly one subject: the `object` vertex form, which by Contract #1's
	// vtx.<type>.<id>.<localName> shape covers the type's aspects under the same
	// wildcard tail. No link subject is emitted, because the relation list is
	// empty — an empty list emits nothing rather than rendering a wildcard.
	require.Equal(t, []string{subjects.CoreKVVertexFilter(bootstrap.CoreKVBucket, "object")},
		filterSubjects,
		"the lens must receive vtx.object.> and nothing else")

	require.NotNil(t, p.Sweeper(),
		"objectLiveness must be sweep-enrolled: the sweep is the recovery path for "+
			"everything the narrowed filter no longer delivers, and a filter narrowing "+
			"is not undone by reverting the cypher")
}
