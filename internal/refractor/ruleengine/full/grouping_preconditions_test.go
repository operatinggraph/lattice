// The two executor properties the grouping-key reduction's theorem rests on,
// asserted directly rather than inferred from the code that implements them.
package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// TestExec_BoundVariableFiltersNeverRebinds is the first precondition: between
// the clause that determines an alias and the clause that carries it, a bound
// variable can be FILTERED but never overwritten. If a re-reference could
// rebind, a row reaching the later clause might hold a (key, accumulator) pair
// no earlier clause ever emitted, and the dependence the reduction relies on
// would not exist.
//
// Both outcomes are asserted, because only the pair proves the property: where
// the re-reference agrees the value is unchanged, and where it disagrees the
// ROW is dropped — never the variable re-seeded to whatever would have matched.
func TestExec_BoundVariableFiltersNeverRebinds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "admin", "role", nil)
	putVertex(t, reg, coreKV, "teamA", "team", nil)
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	putEdge(t, reg, adjKV, "worksAt", "alice", "teamA")

	params := ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}}

	// A pattern head re-referencing a bound variable whose label AGREES keeps
	// the row, with the same node still bound.
	agreeing := parseExec(t, `
MATCH (i:identity {key: $k})-[:holdsRole]->(r:role)
MATCH (r:role)<-[:holdsRole]-(z:identity)
RETURN r.key AS rkey`, params, adjKV, coreKV)
	require.Len(t, agreeing, 1)
	require.Equal(t, vtxKey(reg, "admin"), agreeing[0].Values["rkey"])

	// The same head where the label DISAGREES drops the row. If it instead
	// re-seeded, `r` would come back bound to alice — an identity that holds a
	// role, sitting in the very bucket the seed scan would walk — and the
	// assertion below would see a row.
	disagreeing := parseExec(t, `
MATCH (i:identity {key: $k})-[:holdsRole]->(r:role)
MATCH (r:identity)-[:holdsRole]->(y:role)
RETURN r.key AS rkey`, params, adjKV, coreKV)
	require.Empty(t, disagreeing,
		"a bound variable re-referenced under a label it does not satisfy must drop the row, not rebind")

	// A traversal DESTINATION that is already bound is the same story: arriving
	// at the same node keeps the row…
	sameDestination := parseExec(t, `
MATCH (i:identity {key: $k})-[:holdsRole]->(r:role)
MATCH (i)-[:holdsRole]->(r)<-[:holdsRole]-(z:identity)
RETURN r.key AS rkey`, params, adjKV, coreKV)
	require.Len(t, sameDestination, 1)
	require.Equal(t, vtxKey(reg, "admin"), sameDestination[0].Values["rkey"])

	// …and arriving somewhere else drops it. alice worksAt teamA, so the second
	// traversal reaches a real vertex — it just is not the one `r` holds. A
	// rebind would have produced a row with `r` = teamA.
	otherDestination := parseExec(t, `
MATCH (i:identity {key: $k})-[:holdsRole]->(r:role)
MATCH (i)-[:worksAt]->(r)<-[:worksAt]-(z:identity)
RETURN r.key AS rkey`, params, adjKV, coreKV)
	require.Empty(t, otherDestination,
		"a traversal reaching a different node than the destination variable holds must drop the row, not rebind it")
}

// TestProjectItems_RedundantItemProjectsFirstRowValue is the second
// precondition: a group's non-aggregating values are written once, from the row
// that created the group. That is why a redundant item must still be EVALUATED
// — its value is what the group row carries forward — and why skipping only the
// key fragment is sound.
//
// Driven through projectItems directly with a mask that is deliberately WRONG
// for the data (two rows whose `a` values differ are forced into one group), so
// the first-row-wins behaviour is observable rather than inferred: under a
// correct mask the merged rows agree by construction and nothing could be seen.
func TestProjectItems_RedundantItemProjectsFirstRowValue(t *testing.T) {
	ex := &executor{ctx: context.Background()}
	items := []ProjectionItem{
		{Expr: &VariableRef{Name: "a"}},
		{Expr: &FunctionCall{Name: "collect", Args: []Expr{&VariableRef{Name: "b"}}}, Alias: "bs"},
	}
	bindings := []binding{
		{"a": "first", "b": "x"},
		{"a": "second", "b": "y"},
	}

	unreduced, err := ex.projectItems(bindings, items, nil)
	require.NoError(t, err)
	require.Len(t, unreduced, 2, "with `a` in the key the two rows are two groups")
	require.Equal(t, "first", unreduced[0]["a"])
	require.Equal(t, "second", unreduced[1]["a"])

	reduced, err := ex.projectItems(bindings, items, []bool{true, false})
	require.NoError(t, err)
	require.Len(t, reduced, 1, "with `a` out of the key both rows fall in one group")
	require.Equal(t, "first", reduced[0]["a"],
		"the group's non-aggregating value comes from the row that created the group")
	require.Equal(t, []any{"x", "y"}, reduced[0]["bs"],
		"every row still folds into the aggregate — only the key fragment was skipped")
}
