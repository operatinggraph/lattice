package full

// HopIndex's taxonomy-expansion generalization (dynamic-type-taxonomy-
// design.md §5.1's sixth mechanism, alongside the four executor sites):
// PositionsBinding, AnchorSideSeeds and StepsFrom's far-end label all read a
// `*` position by set membership against its taxonomy-resolved downward
// closure instead of string equality, and an anchor position itself
// carrying `*` makes the whole index Incomplete (walkToAnchors cannot build
// a single vertex-key prefix for an expanded anchor).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// locationExpansion is the resolved set a `location*` position admits in
// these tests: unit and building, not location itself (abstract, §3.4).
var locationExpansion = map[string]map[string]struct{}{
	"location": {"unit": {}, "building": {}},
}

func TestHopIndex_PositionsBinding_LabelExpand(t *testing.T) {
	ix := indexOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:manages]->(l:location*)
RETURN i.key AS actorKey, l.key AS loc
`)
	require.True(t, ix.Complete, "must be indexable: %s", ix.Incomplete)
	locPos := -1
	for i, l := range ix.Labels {
		if l == "location" {
			locPos = i
		}
	}
	require.NotEqual(t, -1, locPos)
	require.True(t, ix.LabelExpand[locPos])

	// Unresolved: WithLabelExpansion was never called, so the `*` position
	// must admit nothing — never fall back to bare "location" equality
	// (which would match no real key type anyway, since location is
	// abstract, but the fail-closed posture must hold regardless).
	require.Empty(t, ix.PositionsBinding("unit"))

	resolved := ix.WithLabelExpansion(locationExpansion)
	require.Contains(t, resolved.PositionsBinding("unit"), locPos)
	require.Contains(t, resolved.PositionsBinding("building"), locPos)
	require.NotContains(t, resolved.PositionsBinding("identity"), locPos,
		"identity is not in location's resolved set")

	// The identity anchor position itself binds by bare equality regardless
	// of the OTHER position's expansion.
	require.Contains(t, resolved.PositionsBinding("identity"), ix.Anchor)
}

func TestHopIndex_WithLabelExpansion_DoesNotMutateOriginal(t *testing.T) {
	ix := indexOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:manages]->(l:location*)
RETURN i.key AS actorKey, l.key AS loc
`)
	require.True(t, ix.Complete)
	resolved := ix.WithLabelExpansion(locationExpansion)
	require.Nil(t, ix.Expanded, "the original index must be left untouched")
	require.NotNil(t, resolved.Expanded)
}

func TestHopIndex_AnchorSideSeeds_LabelExpand(t *testing.T) {
	ix := indexOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:manages]->(l:location*)
RETURN i.key AS actorKey, l.key AS loc
`).WithLabelExpansion(locationExpansion)
	require.True(t, ix.Complete)

	// A "unit manages" link (unit is in location's resolved set) must seed
	// the identity anchor side.
	seeds := ix.AnchorSideSeeds("identity", "manages", "unit")
	require.Len(t, seeds, 1)
	require.Equal(t, ix.Anchor, seeds[0].Pos)

	// A type OUTSIDE the resolved set must not match at all.
	require.Empty(t, ix.AnchorSideSeeds("identity", "manages", "owner"))
}

func TestHopIndex_StepsFrom_CarriesExpansion(t *testing.T) {
	ix := indexOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:manages]->(l:location*)
RETURN i.key AS actorKey, l.key AS loc
`).WithLabelExpansion(locationExpansion)
	require.True(t, ix.Complete)

	var toLoc *PatternStep
	for _, s := range ix.StepsFrom(ix.Anchor) {
		if s.Rel == "manages" {
			s := s
			toLoc = &s
		}
	}
	require.NotNil(t, toLoc)
	require.True(t, toLoc.ToLabelExpand)
	require.Equal(t, map[string]struct{}{"unit": {}, "building": {}}, toLoc.ToExpanded)
}

// TestHopIndex_AnchorLabelExpand_RefusesComplete pins that an anchor
// pattern itself carrying `*` makes the whole index Incomplete, because
// walkToAnchors builds the anchor's vertex key from a single literal prefix
// and a `*` anchor's realized instances can be any of several concrete
// types.
func TestHopIndex_AnchorLabelExpand_RefusesComplete(t *testing.T) {
	ix := indexOf(t, `
MATCH (l:location* {key: $actorKey})
RETURN l.key AS actorKey
`)
	require.False(t, ix.Complete)
	require.Contains(t, ix.Incomplete, "taxonomy-expansion sigil")
}

// TestHopIndex_LabelExpand_UnrelatedNonExpandQueryUnaffected pins inertness
// at the HopIndex layer: a query with no `*` anywhere gets an all-false
// LabelExpand slice, so PositionsBinding derives purely from Labels'
// bare-equality reading whether or not WithLabelExpansion is ever called on
// it.
func TestHopIndex_LabelExpand_UnrelatedNonExpandQueryUnaffected(t *testing.T) {
	ix := indexOf(t, shippedCapabilityRoles)
	require.True(t, ix.Complete)
	for _, e := range ix.LabelExpand {
		require.False(t, e)
	}
	same := ix.WithLabelExpansion(map[string]map[string]struct{}{"role": {"role": {}}})
	require.Nil(t, same.Expanded, "no position to expand means WithLabelExpansion is a no-op")
	require.Equal(t, ix.PositionsBinding("role"), same.PositionsBinding("role"))
}
