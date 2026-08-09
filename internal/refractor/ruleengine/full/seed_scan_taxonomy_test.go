package full

// seedNodes' unseeded SCAN path (dynamic-type-taxonomy-design.md §5): a `*`
// pattern with no {key: …} and no armed anchor seed lists candidates per
// CONCRETE member type instead of the abstract label's own "vtx.<label>."
// prefix, which never has instances (§3.2). Requires real Core KV, unlike
// label_expansion_test.go's site-level unit tests, because what is under
// test is specifically what seedNodes lists.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// TestSeedNodes_LabelExpand_ScanListsEveryConcreteMember pins the scan's
// per-member-type listing: an UNSEEDED evaluation (boot, Rebuild, a
// neighbour-triggered re-execute) of a `*`-anchored lens must bind every
// concrete instance the resolver's expansion names, never zero — listing
// only the literal (and always-instanceless) abstract prefix would silently
// bind nothing regardless of what the resolver resolved.
func TestSeedNodes_LabelExpand_ScanListsEveryConcreteMember(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitKey := putVertex(t, reg, coreKV, "loft1", "unit", map[string]any{"city": "Lisbon"})
	buildingKey := putVertex(t, reg, coreKV, "tower1", "building", map[string]any{"city": "Porto"})
	// A type OUTSIDE location's resolved set must never appear.
	putVertex(t, reg, coreKV, "bob", "identity", nil)

	eng := New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	withExp := WithLabelExpansion(cr.(*CompiledRule),
		map[string]map[string]struct{}{"location": {"unit": {}, "building": {}}})

	results, err := eng.ExecuteWith(t.Context(), withExp, ruleengine.EventContext{}, adjKV, coreKV)
	require.NoError(t, err)
	var keys []string
	for _, r := range results {
		keys = append(keys, r.Values["key"].(string))
	}
	require.ElementsMatch(t, []string{unitKey, buildingKey}, keys)
}

// TestSeedNodes_LabelExpand_UnresolvedFailsClosed pins the fail-closed
// posture: a LabelExpand pattern with no entry in LabelExpansion binds
// NOTHING — never falls back to scanning the bare (abstract, always-empty)
// label prefix, and never scans the whole bucket either.
func TestSeedNodes_LabelExpand_UnresolvedFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "loft1", "unit", nil)

	eng := New()
	cr, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	// No WithLabelExpansion call — LabelExpansion stays nil.

	results, err := eng.ExecuteWith(t.Context(), cr, ruleengine.EventContext{}, adjKV, coreKV)
	require.NoError(t, err)
	require.Empty(t, results)
}

// TestSeedNodes_LabelExpand_SeededAndUnseededAgree is the mass-revoke guard
// itself: the SAME `*`-anchored lens, evaluated once event-seeded (the
// pointCandidate fast path) and once unseeded (the scan path), must derive
// the SAME row for the seeded anchor. A seeded evaluation projecting a
// unit's row while the unseeded re-execute of the identical lens returns
// zero rows for it is exactly the disagreement filter-retraction reads as
// "this anchor is gone" — a spurious mass Delete on a grant-shaped lens.
func TestSeedNodes_LabelExpand_SeededAndUnseededAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitKey := putVertex(t, reg, coreKV, "loft1", "unit", nil)

	eng := New()
	cr, err := eng.Parse(`MATCH (l:location* {key: $key}) RETURN l.key AS key`)
	require.NoError(t, err)
	scanCR, err := eng.Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	exp := map[string]map[string]struct{}{"location": {"unit": {}}}
	withExpKeyed := WithLabelExpansion(cr.(*CompiledRule), exp)
	withExpScan := WithLabelExpansion(scanCR.(*CompiledRule), exp)

	// The point-seeded read (mirrors the anchor-seeded pointCandidate path —
	// {key: $key} is a resolvable expression, so seedNodes' fast path
	// short-circuits the scan entirely).
	seeded, err := eng.ExecuteWith(t.Context(), withExpKeyed,
		ruleengine.EventContext{Parameters: map[string]any{"key": unitKey}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, seeded, 1)
	require.Equal(t, unitKey, seeded[0].Values["key"])

	// The unseeded re-execute (the scan path).
	unseeded, err := eng.ExecuteWith(t.Context(), withExpScan, ruleengine.EventContext{}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, unseeded, 1, "the unseeded scan must agree with the seeded read — a shrunk row set here is the mass-revoke hazard")
	require.Equal(t, unitKey, unseeded[0].Values["key"])
}
