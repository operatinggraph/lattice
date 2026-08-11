package full

// `WITH DISTINCT` de-duplicates the rows a WITH projects. The keyword is parsed
// (visitor.go sets With.Distinct) and honoured by applyWith; the pins below are
// its outcome, its interaction with the clause's own WHERE, and the failure the
// gap it closed would eventually have caused — a de-duplication the author
// reasonably believes happened, feeding an aggregate that then over-counts.
//
// Every case is paired with the same query WITHOUT the keyword. The paired run
// is the positive vector: it proves the corpus really does produce duplicates,
// so a DISTINCT assertion cannot pass because there was nothing to collapse.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

func TestExec_WithDistinctCollapsesDuplicateRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"name": "bob"})
	for _, bk := range []struct{ name, who string }{
		{"bk1", "alice"}, {"bk2", "alice"}, {"bk3", "alice"}, {"bk4", "bob"}, {"bk5", "bob"},
	} {
		putVertex(t, reg, coreKV, bk.name, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk.name, bk.who)
	}

	duplicated := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH id
RETURN id.key AS key`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, duplicated, 5,
		"without the keyword the WITH carries one row per booking — five bookings, five rows")

	distinct := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id
RETURN id.key AS key`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, distinct, 2, "two members booked, so DISTINCT carries two rows")

	keys := map[string]bool{}
	for _, r := range distinct {
		keys[r.Values["key"].(string)] = true
	}
	require.Equal(t, map[string]bool{vtxKey(reg, "alice"): true, vtxKey(reg, "bob"): true}, keys,
		"de-duplication removes copies, never a member")
}

// The clause's own WHERE reads the DISTINCT set: `WITH DISTINCT … WHERE …` is
// "filter the distinct rows", not "distinct the filtered rows".
//
// With no ORDER BY/SKIP/LIMIT in a With the two readings select the same rows —
// duplicates are identical, so a row predicate keeps every copy or none — so
// what this pins is the conjunction: the keyword collapses the duplicates AND
// the filter still applies to what survives. The ordering itself buys one
// predicate evaluation per distinct row instead of one per duplicate.
func TestExec_WithDistinctWhereFiltersTheDistinctSet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"name": "bob"})
	for _, bk := range []struct{ name, who string }{
		{"bk1", "alice"}, {"bk2", "alice"}, {"bk3", "bob"}, {"bk4", "bob"},
	} {
		putVertex(t, reg, coreKV, bk.name, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk.name, bk.who)
	}

	unfiltered := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH id WHERE id.name = 'alice'
RETURN id.key AS key`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, unfiltered, 2, "alice booked twice, and without the keyword both rows survive the filter")

	filtered := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id WHERE id.name = 'alice'
RETURN id.key AS key`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, filtered, 1, "one distinct member passes the filter")
	require.Equal(t, vtxKey(reg, "alice"), filtered[0].Values["key"])

	excluded := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id WHERE id.name = 'nobody'
RETURN id.key AS key`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Empty(t, excluded, "the WHERE still excludes, it is not bypassed by the de-duplication")
}

// The failure the gap would eventually have caused: a de-duplication the author
// believes happened, feeding an aggregate. Silent, because the numbers look
// plausible — a count of bookings where the query asked for a count of members.
func TestExec_WithDistinctBeforeAggregateDoesNotDoubleCount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "bob", "identity", nil)
	for _, bk := range []struct{ name, who string }{
		{"bk1", "alice"}, {"bk2", "alice"}, {"bk3", "alice"}, {"bk4", "bob"},
	} {
		putVertex(t, reg, coreKV, bk.name, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk.name, bk.who)
	}

	overCounted := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH id
RETURN count(id) AS members, collect(id.key) AS memberKeys`,
		ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, overCounted, 1)
	require.EqualValues(t, 4, overCounted[0].Values["members"],
		"without the keyword the aggregate counts bookings, not members")

	counted := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id
RETURN count(id) AS members, collect(id.key) AS memberKeys`,
		ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, counted, 1)
	require.EqualValues(t, 2, counted[0].Values["members"],
		"the aggregate sees the distinct set — two members, not four bookings")
	memberKeys, ok := counted[0].Values["memberKeys"].([]any)
	require.True(t, ok)
	require.Len(t, memberKeys, 2)
}

// A node column is compared by the injective rendering, so the de-duplication
// collapses only rows that really are the same row — the applyWith twin of
// TestExec_DistinctKeepsRowsDifferingOnlyByNode. Both members book twice, so
// the expected count of 2 is wrong in BOTH directions: 4 if the keyword is not
// honoured, 1 if a node column rendered as `{}` and made two members look alike.
func TestExec_WithDistinctKeepsRowsDifferingOnlyByNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "bob", "identity", nil)
	for _, bk := range []struct{ name, who string }{
		{"bk1", "alice"}, {"bk2", "alice"}, {"bk3", "bob"}, {"bk4", "bob"},
	} {
		putVertex(t, reg, coreKV, bk.name, "booking", nil)
		putEdge(t, reg, adjKV, "bookedBy", bk.name, bk.who)
	}

	rows := parseExec(t, `
MATCH (bk:booking)-[:bookedBy]->(id:identity)
WITH DISTINCT id
RETURN id AS member`, ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, rows, 2, "four bookings, two members: neither under- nor over-collapsed")

	keys := map[string]bool{}
	for _, r := range rows {
		ref, isNode := r.Values["member"].(*nodeRef)
		require.True(t, isNode, "the carried column is the node binding itself")
		keys[ref.key] = true
	}
	require.Equal(t, map[string]bool{vtxKey(reg, "alice"): true, vtxKey(reg, "bob"): true}, keys)
}
