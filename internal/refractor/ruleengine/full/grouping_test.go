package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mustParseQuery compiles body and hands back its AST, for tests that assert on
// the grouping analysis rather than on an evaluation.
func mustParseQuery(t *testing.T, body string) *Query {
	t.Helper()
	cr, err := New().Parse(body)
	require.NoError(t, err, "test query must parse:\n%s", body)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)
	return compiled.Query
}

// groupingPlans returns the analysis's plans for body, one per projecting
// clause in source order.
func groupingPlans(t *testing.T, body string) []groupingPlan {
	t.Helper()
	return analyseGrouping(mustParseQuery(t, body))
}

// A staged accumulator carried across later grouping clauses is redundant from
// the clause after the one that produced it, and the effective key stays the
// single actor variable — the shape §4.2's worked table predicts for every
// generated read-grant producer.
func TestAnalyseGrouping_CarriedAccumulatorIsRedundant(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, collect(DISTINCT bk.key) AS s1
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
WITH identity, s0, s1, collect(DISTINCT inst.key) AS s2
RETURN identity.key AS actorKey, s0 AS a, s1 AS b, s2 AS c
`)
	require.Len(t, plans, 4)

	require.Empty(t, plans[0].Refusal)
	require.Equal(t, []string{"identity"}, plans[0].Key)
	require.Equal(t, []bool{false, false}, plans[0].Redundant)

	require.Empty(t, plans[1].Refusal)
	require.Equal(t, []string{"identity"}, plans[1].Key)
	require.Equal(t, []bool{false, true, false}, plans[1].Redundant,
		"s0 is a bare carry of an alias the previous clause aggregated — it cannot discriminate")

	require.Empty(t, plans[2].Refusal)
	require.Equal(t, []string{"identity"}, plans[2].Key)
	require.Equal(t, []bool{false, true, true, false}, plans[2].Redundant)

	// The RETURN carries no aggregator, so the executor never renders a key for
	// it; the analysis still records that `identity` arrives as a property
	// access rather than a bare carry, which is what ends the dependence chain.
	require.False(t, plans[3].Grouping)
	require.NotEmpty(t, plans[3].Refusal)
	require.Equal(t, []bool{false, false, false, false}, plans[3].Redundant)
}

// A rename breaks the chain: the value survives under a name nothing has proved
// a dependence for, so the clause keeps every item in its key.
func TestAnalyseGrouping_RenamedKeyCarryFailsClosed(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity AS actor, s0, collect(DISTINCT bk.key) AS s1
RETURN actor.key AS actorKey, s1 AS b
`)
	require.Len(t, plans, 3)
	require.Contains(t, plans[1].Refusal, `"identity"`,
		"the refusal must name the key alias that stopped being carried")
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant)
	require.Equal(t, []string{"actor", "s0"}, plans[1].Key,
		"a refused clause keeps every non-aggregating item in its key")
}

// A renamed ACCUMULATOR is not a carry either: `s0 AS acc` re-enters the key
// under a name no dependence was proved for.
func TestAnalyseGrouping_RenamedAccumulatorIsNotRedundant(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0 AS acc, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, acc AS a, s1 AS b
`)
	require.Len(t, plans, 3)
	require.Empty(t, plans[1].Refusal, "the key alias `identity` is still carried, so the clause is analysed")
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant,
		"`s0 AS acc` is a rename, not a bare carry")
	require.Equal(t, []string{"acc", "identity"}, plans[1].Key)
}

// Two items under one alias: the second overwrites the first in the projected
// row, so the walk refuses rather than judge which expression the alias names.
func TestAnalyseGrouping_DuplicateAliasFailsClosed(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, s0, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, s1 AS b
`)
	require.Len(t, plans, 3)
	require.Contains(t, plans[1].Refusal, "twice")
	require.Equal(t, []bool{false, false, false, false}, plans[1].Redundant)
}

// A key alias arriving as a computed value rather than a bare carry ends the
// chain — the name survives, the binding it named does not.
func TestAnalyseGrouping_ComputedValueUnderKeyAliasFailsClosed(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity.key AS identity, s0, collect(DISTINCT bk.key) AS s1
RETURN s1 AS b
`)
	require.Len(t, plans, 3)
	require.Contains(t, plans[1].Refusal, `"identity"`)
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant)
}

// An accumulator reached through a property access is not a bare carry: it
// reads a field of the value rather than the value, and the analysis is
// deliberately not generalized past bare carries.
func TestAnalyseGrouping_PropertyAccessOfAccumulatorIsNotRedundant(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0.first AS s0, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, s1 AS b
`)
	require.Len(t, plans, 3)
	require.Empty(t, plans[1].Refusal)
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant)
	require.Equal(t, []string{"identity", "s0"}, plans[1].Key)
}

// A MATCH between two grouping clauses adds bindings, filters rows or drops
// them — it cannot rebind a bound name — so a dependence proved before it
// survives it.
func TestAnalyseGrouping_MatchBetweenClausesKeepsDependence(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, s1 AS b
`)
	require.Len(t, plans, 3)
	require.Equal(t, []bool{false, true, false}, plans[1].Redundant)
	require.Equal(t, []string{"identity"}, plans[1].Key)
}

// A projected variable the previous clause never bound is still a bare carry —
// it just is not DETERMINED by anything, so it stays in the key.
func TestAnalyseGrouping_UndeterminedCarryStaysInKey(t *testing.T) {
	plans := groupingPlans(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, task, collect(DISTINCT task.key) AS s0
RETURN identity.key AS actorKey, s0 AS a
`)
	require.Len(t, plans, 2)
	require.Empty(t, plans[0].Refusal)
	require.Equal(t, []bool{false, false, false}, plans[0].Redundant)
	require.Equal(t, []string{"identity", "task"}, plans[0].Key)
}

// An expression form CollectVariableRefs has no case for could depend on a
// binding this walk cannot see, so the clause holding it is refused.
func TestAnalyseGrouping_UnmodelledExpressionFailsClosed(t *testing.T) {
	q := &Query{Clauses: []Clause{
		&With{Items: []ProjectionItem{
			{Expr: &VariableRef{Name: "identity"}},
			{Expr: &FunctionCall{Name: "collect", Args: []Expr{&VariableRef{Name: "task"}}}, Alias: "s0"},
		}},
		&With{Items: []ProjectionItem{
			{Expr: &VariableRef{Name: "identity"}},
			{Expr: &VariableRef{Name: "s0"}},
			{Expr: fakeUnknownExpr{}, Alias: "odd"},
			{Expr: &FunctionCall{Name: "collect", Args: []Expr{&VariableRef{Name: "bk"}}}, Alias: "s1"},
		}},
	}}
	plans := analyseGrouping(q)
	require.Len(t, plans, 2)
	require.Contains(t, plans[1].Refusal, `"odd"`)
	require.Equal(t, []bool{false, false, false, false}, plans[1].Redundant)
}

// A clause shape the walk has no case for could bind anything, so every
// dependence is dropped across it and the next clause claims nothing.
func TestAnalyseGrouping_UnmodelledClauseDropsDependence(t *testing.T) {
	q := &Query{Clauses: []Clause{
		&With{Items: []ProjectionItem{
			{Expr: &VariableRef{Name: "identity"}},
			{Expr: &FunctionCall{Name: "collect", Args: []Expr{&VariableRef{Name: "task"}}}, Alias: "s0"},
		}},
		fakeUnknownClause{},
		&With{Items: []ProjectionItem{
			{Expr: &VariableRef{Name: "identity"}},
			{Expr: &VariableRef{Name: "s0"}},
			{Expr: &FunctionCall{Name: "collect", Args: []Expr{&VariableRef{Name: "bk"}}}, Alias: "s1"},
		}},
	}}
	plans := analyseGrouping(q)
	require.Len(t, plans, 2)
	require.Empty(t, plans[1].Refusal)
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant,
		"nothing may be claimed redundant across a clause shape the walk cannot model")
	require.Equal(t, []string{"identity", "s0"}, plans[1].Key)
}

// fakeUnknownClause is a Clause this package's grouping walk was never updated
// for — the default-deny arm's positive vector.
type fakeUnknownClause struct{}

func (fakeUnknownClause) isClause() {}

// The executor-facing map holds only the clauses that have something redundant,
// and a query with nothing redundant carries no map at all — which is the same
// nil a directly constructed *CompiledRule gets.
func TestAnalyseGroupingRedundancy_MapHoldsOnlyReducibleClauses(t *testing.T) {
	q := mustParseQuery(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, s1 AS b
`)
	m := analyseGroupingRedundancy(q)
	require.Len(t, m, 1, "only the second WITH has a redundant item")
	for c, mask := range m {
		w, isWith := c.(*With)
		require.True(t, isWith)
		require.Len(t, w.Items, 3)
		require.Equal(t, []bool{false, true, false}, mask)
	}

	require.Nil(t, analyseGroupingRedundancy(mustParseQuery(t,
		`MATCH (i:identity {key: $k}) RETURN i.key AS k, collect(i.key) AS collected`)))
	require.Nil(t, analyseGroupingRedundancy(nil))
}

// A directly constructed rule — the shape 42 test files build — carries no
// analysis, so every evaluation of it renders every item into the key.
func TestCompiledRule_HandBuiltCarriesNoGroupingAnalysis(t *testing.T) {
	cr := &CompiledRule{Query: mustParseQuery(t,
		`MATCH (i:identity {key: $k}) RETURN i.key AS k, collect(i.key) AS collected`)}
	require.Nil(t, cr.groupingRedundant)
	ex := &executor{}
	require.Nil(t, ex.redundantFor(cr.Query.Clauses[0]))
}

// Parse arms the analysis, and WithLabelExpansion's copy carries it — a
// re-derived rule must not silently fall back to the unreduced path.
func TestParse_ArmsGroupingAnalysisAndTheCopyKeepsIt(t *testing.T) {
	cr, err := New().Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
WITH identity, collect(DISTINCT task.key) AS s0
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
WITH identity, s0, collect(DISTINCT bk.key) AS s1
RETURN identity.key AS actorKey, s1 AS b
`)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)
	require.Len(t, compiled.groupingRedundant, 1)
	require.Equal(t, compiled.groupingRedundant,
		WithLabelExpansion(compiled, map[string]map[string]struct{}{}).groupingRedundant)
}
