package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// These tests pin the OPTIONAL MATCH ... WHERE null-restore semantics: when a
// WHERE filters out EVERY real neighbor of an optional pattern, the anchor row
// must be preserved with the optional variables bound null — never dropped.
// Dropping the anchor makes a downstream actor-aggregate read the row as an
// entity deletion. The cyphers are type-neutral (anchor / widget — no
// leaseapp/service) so the guarantee is proven generically for ALL cyphers.

// TestApplyMatch_OptionalWhereFiltersAllNeighbors_PreservesAnchor reproduces the
// dedicated-filtered-optional shape the lease lens's freshUntil column needs: a
// required anchor, a sole neighbor reachable by the optional pattern, and a WHERE
// that excludes that neighbor. The neighbor exists (so matchPath returns a real
// expansion and no null-bound row is synthesised upstream), then the WHERE drops
// it — exactly the case the old restore loop could not recover because it
// searched the expansion set for a null row that was never produced. The anchor
// must survive with the projected optional property null.
func TestApplyMatch_OptionalWhereFiltersAllNeighbors_PreservesAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "anchor1", "anchor", map[string]any{"name": "anchor1"})
	// A single neighbor whose status excludes it from the WHERE — the sole real
	// match the filter removes.
	putVertex(t, reg, coreKV, "w1", "widget", map[string]any{"status": "stale"})
	putEdge(t, reg, adjKV, "linkedTo", "anchor1", "w1")

	results := parseExec(t,
		`MATCH (a:anchor {key: $k})
		 OPTIONAL MATCH (a)-[:linkedTo]->(w:widget) WHERE w.status = 'active'
		 RETURN a.key AS anchorKey, w.status AS picked`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "anchor1")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1,
		"a fully-WHERE-filtered OPTIONAL MATCH must preserve the anchor row with nulls, not drop it")
	require.Equal(t, vtxKey(reg, "anchor1"), results[0].Values["anchorKey"],
		"the required anchor key must survive (not collapse to null)")
	require.Nil(t, results[0].Values["picked"],
		"the filtered optional variable projects null on the preserved anchor")
}

// TestApplyMatch_OptionalWhere_RealMatchSurvives is the complementary case: when
// a neighbor PASSES the WHERE, the real match is kept (and the null fallback is
// NOT additionally emitted), so there is exactly one row carrying the matched
// value. This guards the fix from over-restoring (emitting a spurious null row
// alongside a real match).
func TestApplyMatch_OptionalWhere_RealMatchSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "anchor1", "anchor", map[string]any{"name": "anchor1"})
	putVertex(t, reg, coreKV, "w1", "widget", map[string]any{"status": "active"})
	putEdge(t, reg, adjKV, "linkedTo", "anchor1", "w1")

	results := parseExec(t,
		`MATCH (a:anchor {key: $k})
		 OPTIONAL MATCH (a)-[:linkedTo]->(w:widget) WHERE w.status = 'active'
		 RETURN a.key AS anchorKey, w.status AS picked`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "anchor1")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1, "a passing optional match yields exactly one row (no spurious null fallback)")
	require.Equal(t, vtxKey(reg, "anchor1"), results[0].Values["anchorKey"])
	require.Equal(t, "active", results[0].Values["picked"],
		"the surviving real match carries its value, not null")
}

// TestApplyMatch_OptionalWhere_OneOfManyPasses pins multi-neighbor selectivity:
// of several neighbors only those passing the WHERE survive, and the anchor is
// never duplicated by a null fallback when at least one real match passes.
func TestApplyMatch_OptionalWhere_OneOfManyPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "anchor1", "anchor", map[string]any{"name": "anchor1"})
	putVertex(t, reg, coreKV, "w1", "widget", map[string]any{"status": "stale"})
	putVertex(t, reg, coreKV, "w2", "widget", map[string]any{"status": "active"})
	putEdge(t, reg, adjKV, "linkedTo", "anchor1", "w1")
	putEdge(t, reg, adjKV, "linkedTo", "anchor1", "w2")

	results := parseExec(t,
		`MATCH (a:anchor {key: $k})
		 OPTIONAL MATCH (a)-[:linkedTo]->(w:widget) WHERE w.status = 'active'
		 RETURN a.key AS anchorKey, w.status AS picked`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "anchor1")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1, "only the passing neighbor survives; no null fallback row is added")
	require.Equal(t, "active", results[0].Values["picked"])
}
