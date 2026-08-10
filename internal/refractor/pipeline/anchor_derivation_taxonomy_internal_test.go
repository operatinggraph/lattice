package pipeline

// The pattern-directed affected-anchor derivation (auth-plane-projection-
// latency-design.md §4.7) for a `*` taxonomy-expansion pattern (dynamic-
// type-taxonomy-design.md §5.1's HopIndex-shaped sixth mechanism). Mirrors
// Fire B's real consumer shape (service-location's capabilityServiceAccess:
// an identity anchor traversing to `:location*`): a leaf-typed event (a
// `vtx.unit.<id>` vertex, or the link that wires it to its actor) must
// derive a non-empty anchor set through PositionsBinding/AnchorSideSeeds —
// bare string equality against the literal, always-instanceless "location"
// would silently skip the recompute a real grant change requires.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// locationTaxonomySpec mirrors capabilityServiceAccess's shape: a concrete
// identity anchor, one hop out to an abstract-labeled traversal target.
const locationTaxonomySpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:manages]->(l:location*)
RETURN identity.key AS actorKey, l.key AS loc
`

// derivationPipelineWithTaxonomy is derivationPipeline plus an armed
// resolver, installed BEFORE UseFullEngine so the published HopIndex
// carries the resolved expansion.
func derivationPipelineWithTaxonomy(t *testing.T, adjKV *substrate.KV, spec string, resolver *taxonomy.Resolver) *Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "testLens", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
	p.SetTaxonomyResolver(resolver)
	require.NoError(t, p.UseFullEngine(eng, cr))
	return p
}

func armedLocationResolver() *taxonomy.Resolver {
	r := taxonomy.New()
	r.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: taxID("TAXderivLocationMeta"), CanonicalName: "location", Abstract: true},
		{ID: taxID("TAXderivUnitMeta"), CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: taxID("TAXderivBuildingMeta"), CanonicalName: "building", SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	return r
}

// TestDeriveAnchorsForVertex_LabelExpand_LeafTypeDerivesNonEmptySet pins the
// headline case: a `vtx.unit.<id>` vertex event — a CONCRETE leaf type,
// never the literal (and always-instanceless) "location" string — must
// still derive the identity anchor that manages it, through
// PositionsBinding's taxonomy-resolved set membership.
func TestDeriveAnchorsForVertex_LabelExpand_LeafTypeDerivesNonEmptySet(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("loft1", "unit")
	f.edge("manages", "alice", "loft1")

	p := derivationPipelineWithTaxonomy(t, adjKV, locationTaxonomySpec, armedLocationResolver())
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "the pattern graph must be indexable: %s", rs.anchorHops.Incomplete)

	ctx := context.Background()
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("loft1", "unit"), "unit")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"a leaf-type (unit) event must derive the anchor that manages it — bare equality against the literal "+
			"abstract label 'location' would silently derive nothing here")
}

// TestDeriveAnchorsForLink_LabelExpand_LeafTypeDerivesNonEmptySet is the link
// half, exercising AnchorSideSeeds against a leaf-typed link endpoint.
func TestDeriveAnchorsForLink_LabelExpand_LeafTypeDerivesNonEmptySet(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("loft1", "unit")
	f.edge("manages", "alice", "loft1")

	p := derivationPipelineWithTaxonomy(t, adjKV, locationTaxonomySpec, armedLocationResolver())
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "the pattern graph must be indexable: %s", rs.anchorHops.Incomplete)

	ctx := context.Background()
	linkKey := f.link("manages", "alice", "identity", "loft1", "unit")
	derived, ok, err := p.deriveAnchorsForLink(ctx, rs, linkKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived)
}

// TestDeriveAnchorsForVertex_LabelExpand_UnrelatedTypeStillEmpty pins the
// other direction: a type genuinely OUTSIDE location's resolved set derives
// nothing — the taxonomy-resolved set membership is a real constraint, not
// "match everything".
func TestDeriveAnchorsForVertex_LabelExpand_UnrelatedTypeStillEmpty(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("loft1", "unit")
	f.vertex("bob", "owner")
	f.edge("manages", "alice", "loft1")

	p := derivationPipelineWithTaxonomy(t, adjKV, locationTaxonomySpec, armedLocationResolver())
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete)

	ctx := context.Background()
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("bob", "owner"), "owner")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, derived, "owner is not a member of location's resolved set")
}

// twoHopLocationTaxonomySpec puts the `*` position in the MIDDLE of the
// pattern graph, not at a leaf: identity -manages-> location* -hosts->
// device. Every derivation test above seeds AT the `*` position (the
// vertex test) or at the anchor, which the walk never expands from (the
// link test) — walkToAnchors' StepsFrom is therefore only ever called
// pointed AT the `*` position, never FROM it, so anchor_derivation.go's
// far-end prune (the `step.ToLabelExpand` branch) is never entered by
// either. Seeding at the device leaf and stepping BACK through the `*`
// position on the way to the anchor is the only shape that calls
// StepsFrom(locationPos) and exercises that branch.
const twoHopLocationTaxonomySpec = `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:manages]->(l:location*)-[:hosts]->(d:device)
RETURN i.key AS actorKey, d.key AS dev
`

// TestDeriveAnchorsForVertex_LabelExpand_MiddlePositionPrunesFarEnd drives a
// leaf-type (device) event two hops from the anchor, through the `*`
// position, so the walk must call StepsFrom on the `*` position itself and
// prune its OWN far end (the device→location hop) by taxonomy-resolved set
// membership rather than by string equality.
func TestDeriveAnchorsForVertex_LabelExpand_MiddlePositionPrunesFarEnd(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("loft1", "unit")
	f.vertex("therm1", "device")
	f.edge("manages", "alice", "loft1")
	f.edge("hosts", "loft1", "therm1")

	p := derivationPipelineWithTaxonomy(t, adjKV, twoHopLocationTaxonomySpec, armedLocationResolver())
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "the pattern graph must be indexable: %s", rs.anchorHops.Incomplete)

	ctx := context.Background()
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("therm1", "device"), "device")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"a device event two hops from the anchor, through a `*` position, must still derive the managing "+
			"identity — the walk steps FROM the device INTO the `*` position and must admit 'unit' there")

	// Negative: a device hosted by a type OUTSIDE location's resolved set
	// must be pruned at that hop and derive nothing.
	f.vertex("office1", "office")
	f.vertex("therm2", "device")
	f.edge("hosts", "office1", "therm2")

	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, f.key("therm2", "device"), "device")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, derived,
		"office is not a member of location's resolved set — the hop from therm2 must be pruned, "+
			"not kept on the 'cannot confirm' theory that applies only to a missing OtherType")
}

// TestDerivationIndex_UnresolvedExpansion_DeclinesInsteadOfPruning is the
// backstop for the one direction this whole derivation may not move in. A
// `*` position with no resolved concrete set admits NOTHING, so every far end
// the walk reaches through it is PRUNED — and an empty derived set on a
// Complete index is read by affectedAnchors as a real answer with no BFS
// behind it. The anchor whose grant the event revokes is simply never
// reprojected.
//
// So the index is declined whole, and the caller keeps the shipped
// ActorEnumerator BFS, exactly as it does for every other shape the
// derivation cannot resolve. The same rule state with its expansion intact
// derives the anchor, which is what makes the empty set a real miss rather
// than a truthful "nothing is affected".
func TestDerivationIndex_UnresolvedExpansion_DeclinesInsteadOfPruning(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("loft1", "unit")
	f.edge("manages", "alice", "loft1")

	p := derivationPipelineWithTaxonomy(t, adjKV, locationTaxonomySpec, armedLocationResolver())
	ctx := context.Background()

	resolved := p.ruleState()
	require.True(t, resolved.anchorHops.Complete)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, resolved, f.key("loft1", "unit"), "unit")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"precondition: with the expansion resolved, this event DOES affect alice's row")

	// The same lens, same pattern graph, with the `*` position's expansion
	// missing — what a rule state published while the resolver could not
	// answer carries.
	unresolved := resolved
	unresolved.anchorHops.Expanded = nil
	require.GreaterOrEqual(t, unresolved.anchorHops.UnresolvedExpansionPosition(), 0)

	_, ok = p.derivationIndex(unresolved)
	require.False(t, ok, "an index with an unresolved `*` position must be declined, not walked")

	derived, ok, err = p.deriveAnchorsForVertex(ctx, unresolved, f.key("loft1", "unit"), "unit")
	require.NoError(t, err)
	require.False(t, ok,
		"the derivation must DECLINE so the caller falls back to the BFS — returning an empty set with "+
			"ok==true reports 'no anchor is affected' for an event that revokes alice's grant")
	require.Empty(t, derived)
}
