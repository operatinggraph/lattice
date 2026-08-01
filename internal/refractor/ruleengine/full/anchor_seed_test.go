package full

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// Event-seeded evaluation (refractor-footprint-reduction-design.md §D2 Phase
// 1): EventContext.SeedAnchor narrows the query's ANCHOR pattern — the first
// MATCH clause's first node — to the event vertex, so an anchor-labeled event
// re-derives only that anchor's rows instead of rescanning the whole type.
// Every other pattern, and every seed the engine cannot prove binds the
// anchor, keeps the scan it always had.

// seedUnitsSpec is the unanchored whole-type-scan shape the business-lens
// corpus is made of: no {key: …} on the anchor, so the anchor pattern's
// candidate set is otherwise every unit in the bucket.
const seedUnitsSpec = `MATCH (u:unit) RETURN u.key AS key, u.city AS city`

// TestSeedAnchor_NarrowsToTheEventVertex proves the core of Phase 1: with a
// seed set, the anchor pattern binds ONLY the seeded vertex, though the bucket
// holds several of its type that the unseeded evaluation returns.
func TestSeedAnchor_NarrowsToTheEventVertex(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitA := putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})
	putVertex(t, reg, coreKV, "unitC", "unit", map[string]any{"city": "Faro"})

	seeded := parseExec(t, seedUnitsSpec,
		ruleengine.EventContext{SeedAnchor: unitA}, adjKV, coreKV)
	require.Len(t, seeded, 1, "a seeded anchor evaluates exactly one anchor")
	require.Equal(t, unitA, seeded[0].Values["key"])
	require.Equal(t, "Lisbon", seeded[0].Values["city"])
}

// TestSeedAnchor_EmptySeedScansEveryAnchor pins the unseeded behaviour the
// seeded case above narrows from — the zero value is byte-identical to what
// every caller got before seeding existed, and it is what makes the one-row
// assertion above a real narrowing proof rather than a fixture artifact.
func TestSeedAnchor_EmptySeedScansEveryAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})
	putVertex(t, reg, coreKV, "unitC", "unit", map[string]any{"city": "Faro"})

	all := parseExec(t, seedUnitsSpec, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, all, 3, "no seed means the whole-type scan, exactly as before")
}

// TestSeedAnchor_MismatchedLabelFallsBackToScan proves the engine's own
// structural check: a seed key whose vertex type is not the anchor pattern's
// label says nothing about which anchors changed, so the pattern scans. The
// pipeline never sends such a seed — this is the engine refusing to narrow on
// a caller's word alone.
func TestSeedAnchor_MismatchedLabelFallsBackToScan(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})
	landlord := putVertex(t, reg, coreKV, "landlord", "identity", nil)

	results := parseExec(t, seedUnitsSpec,
		ruleengine.EventContext{SeedAnchor: landlord}, adjKV, coreKV)
	require.Len(t, results, 2, "an identity seed cannot narrow a unit anchor")
}

// TestSeedAnchor_UnlabeledAnchorFallsBackToScan pins the second structural
// refusal: an unlabeled anchor pattern binds any vertex type, so one vertex is
// not its candidate set.
func TestSeedAnchor_UnlabeledAnchorFallsBackToScan(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitA := putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})

	results := parseExec(t, `MATCH (u) RETURN u.key AS key`,
		ruleengine.EventContext{SeedAnchor: unitA}, adjKV, coreKV)
	require.Len(t, results, 2, "an unlabeled anchor keeps its whole-bucket scan")
}

// TestSeedAnchor_KeyedAnchorPatternIgnoresSeed proves a pattern that already
// point-seeds through its own `{key: …}` property is never displaced by the
// seed: the query's own key wins, so a lens explicitly scoped to one vertex
// can't be redirected to a different one by an event.
func TestSeedAnchor_KeyedAnchorPatternIgnoresSeed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitA := putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	unitB := putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})

	results := parseExec(t, `MATCH (u:unit {key: $k}) RETURN u.key AS key`,
		ruleengine.EventContext{
			SeedAnchor: unitA,
			Parameters: map[string]any{"k": unitB},
		}, adjKV, coreKV)
	require.Len(t, results, 1)
	require.Equal(t, unitB, results[0].Values["key"],
		"the pattern's own key property must win over the seed")
}

// TestSeedAnchor_MissingVertexYieldsZeroRows proves a seed pointing at a
// missing or soft-deleted vertex resolves to zero bindings — exactly what a
// scan that matched nothing yields, never an error and never a fallback scan
// that would re-derive every sibling anchor's rows.
func TestSeedAnchor_MissingVertexYieldsZeroRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	gone := putVertex(t, reg, coreKV, "unitGone", "unit", map[string]any{"city": "Porto", "isDeleted": true})

	tombstoned := parseExec(t, seedUnitsSpec,
		ruleengine.EventContext{SeedAnchor: gone}, adjKV, coreKV)
	require.Empty(t, tombstoned, "a tombstoned anchor derives no rows")

	absent := parseExec(t, seedUnitsSpec,
		ruleengine.EventContext{SeedAnchor: "vtx.unit." + c1NanoID("neverWritten")}, adjKV, coreKV)
	require.Empty(t, absent, "an absent anchor derives no rows")
}

// TestSeedAnchor_ConsumedByTheAnchorPatternOnly proves the seed is spent
// exactly once, by the anchor. A second MATCH clause on the SAME label is the
// discriminating shape: if the seed persisted, the second clause would collapse
// to the same single vertex and the cross-product would be 1×1 instead of 1×N.
func TestSeedAnchor_ConsumedByTheAnchorPatternOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unitA := putVertex(t, reg, coreKV, "unitA", "unit", map[string]any{"city": "Lisbon"})
	putVertex(t, reg, coreKV, "unitB", "unit", map[string]any{"city": "Porto"})
	putVertex(t, reg, coreKV, "unitC", "unit", map[string]any{"city": "Faro"})

	results := parseExec(t,
		`MATCH (u:unit) MATCH (v:unit) RETURN u.key AS key, v.key AS other`,
		ruleengine.EventContext{SeedAnchor: unitA}, adjKV, coreKV)
	require.Len(t, results, 3,
		"the anchor is pinned to the seed; the later clause still scans every unit")
	for _, r := range results {
		require.Equal(t, unitA, r.Values["key"])
	}
}

// TestSeedAnchor_SeededAnchorStillExpandsItsWalk proves seeding constrains the
// ANCHOR BINDING, not pattern expansion: a lens producing several rows per
// anchor still produces all of them under a seed.
func TestSeedAnchor_SeededAnchorStillExpandsItsWalk(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	svc := putVertex(t, reg, coreKV, "svc", "service", nil)
	putVertex(t, reg, coreKV, "svcOther", "service", nil)
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "bob", "identity", nil)
	putEdge(t, reg, adjKV, "providedTo", "svc", "alice")
	putEdge(t, reg, adjKV, "providedTo", "svc", "bob")
	putEdge(t, reg, adjKV, "providedTo", "svcOther", "alice")

	results := parseExec(t,
		`MATCH (s:service) MATCH (s)-[:providedTo]->(i:identity) RETURN s.key AS key, i.key AS holder`,
		ruleengine.EventContext{SeedAnchor: svc}, adjKV, coreKV)
	require.Len(t, results, 2, "the seeded anchor still expands its whole walk")
	holders := map[string]bool{}
	for _, r := range results {
		require.Equal(t, svc, r.Values["key"])
		holders[r.Values["holder"].(string)] = true
	}
	require.Len(t, holders, 2)
}

// TestSeedAnchor_OptionalMatchNeighborUnaffected proves an OPTIONAL MATCH's
// own pattern keeps its candidate set: the seeded anchor's optional neighbor
// still resolves, so a seeded row carries the same columns an unseeded one does.
func TestSeedAnchor_OptionalMatchNeighborUnaffected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	svc := putVertex(t, reg, coreKV, "svc", "service", nil)
	alice := putVertex(t, reg, coreKV, "alice", "identity", nil)
	putEdge(t, reg, adjKV, "providedTo", "svc", "alice")

	spec := `MATCH (s:service) OPTIONAL MATCH (s)-[:providedTo]->(i:identity) RETURN s.key AS key, i.key AS holder`
	seeded := parseExec(t, spec, ruleengine.EventContext{SeedAnchor: svc}, adjKV, coreKV)
	unseeded := parseExec(t, spec, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, seeded, 1)
	require.Equal(t, unseeded, seeded,
		"the only service's seeded evaluation must match the unseeded one exactly")
	require.Equal(t, alice, seeded[0].Values["holder"])
}

// TestAnchorLabel_DerivesFirstMatchNodeLabel pins the derivation the pipeline's
// seeding eligibility is built on — the same first-MATCH-node label
// AnchorProjectionKey discriminates a tombstoned anchor by.
func TestAnchorLabel_DerivesFirstMatchNodeLabel(t *testing.T) {
	eng := New()
	for _, tc := range []struct {
		name  string
		body  string
		label string
		ok    bool
	}{
		{"labeled anchor", seedUnitsSpec, "unit", true},
		{"anchor is the FIRST match node, not a later one",
			`MATCH (u:unit) MATCH (u)-[:managedBy]->(i:identity) RETURN u.key AS key`, "unit", true},
		{"unlabeled anchor", `MATCH (u) RETURN u.key AS key`, "", false},
		{"keyed anchor still exposes its label",
			`MATCH (u:unit {key: $k}) RETURN u.key AS key`, "unit", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := eng.Parse(tc.body)
			require.NoError(t, err)
			compiled, isFull := cr.(*CompiledRule)
			require.True(t, isFull)
			label, ok := compiled.AnchorLabel()
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.label, label)
		})
	}
}

// TestAnchorLabel_NilSafe pins the defensive contract the pipeline relies on at
// install time: a nil rule or a rule with no query reports no anchor rather
// than panicking, so seeding stays disarmed.
func TestAnchorLabel_NilSafe(t *testing.T) {
	var nilCR *CompiledRule
	label, ok := nilCR.AnchorLabel()
	require.False(t, ok)
	require.Empty(t, label)

	empty := &CompiledRule{}
	label, ok = empty.AnchorLabel()
	require.False(t, ok)
	require.Empty(t, label)
}
