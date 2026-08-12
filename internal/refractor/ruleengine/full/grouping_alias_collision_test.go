package full

// One alias, two items — the shape that makes the analysis's alias sets stop
// describing the executor's partition.
//
// A projecting clause keys its groups on the VALUE OF EVERY non-aggregating
// ITEM, index-tagged (projectItems). The analysis reasons in ALIASES. The two
// agree exactly while each alias names one item. They stop agreeing the moment
// an alias names two, and two things break at once:
//
//   - The row's value for that alias is whichever item wrote it last, so a
//     later clause carrying the alias forward carries a value the analysis did
//     not record under that name.
//   - The analysis's key — an alias SET — is COARSER than the executor's real
//     item-indexed key, so an aggregate of that clause is no longer a function
//     of it either.
//
// Both make a downstream redundancy claim an over-merge, which on a read-grant
// producer is an over-grant. So a clause whose aliases are not unique determines
// NOTHING, and every witness below pins that.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// secondWithOf returns the second projecting clause of body — the clause every
// witness here drives directly, since what is under test is the mask the
// analysis hands the executor for it.
func secondWithOf(t *testing.T, body string) (*With, []bool) {
	t.Helper()
	q := mustParseQuery(t, body)
	withs := []*With{}
	for _, c := range q.Clauses {
		if w, isWith := c.(*With); isWith {
			withs = append(withs, w)
		}
	}
	require.GreaterOrEqual(t, len(withs), 2, "the witness needs two WITH clauses")
	return withs[1], analyseGroupingRedundancy(q)[withs[1]]
}

// An alias named twice across an aggregating and a non-aggregating item leaves
// NOTHING determined. Recording the alias in both sets let the next clause read
// it as determined-and-carried, drop it from the key, and merge every group.
func TestAnalyseGrouping_DuplicateAliasAcrossAggAndNonAggLeavesNoDependence(t *testing.T) {
	const body = `
MATCH (n:thing)
WITH n.owner AS a, collect(n.key) AS a
WITH a, collect(a) AS s
RETURN a AS k, s AS v`

	plans := groupingPlans(t, body)
	require.Len(t, plans, 3)

	require.Contains(t, plans[0].Refusal, "twice")
	require.Equal(t, []string{"a"}, plans[0].Key)

	require.Equal(t, []bool{false, false}, plans[1].Redundant,
		"the refused clause determines nothing, so its successor may claim nothing")
	require.Equal(t, []string{"a"}, plans[1].Key,
		"`a` must stay in the grouping key — it is the only thing keeping two rows apart")

	// The executable half: the mask the analysis hands the executor must keep
	// two differently-valued rows in two groups.
	second, mask := secondWithOf(t, body)
	ex := &executor{ctx: context.Background()}
	rows, err := ex.projectItems([]binding{{"a": int64(1)}, {"a": int64(2)}}, second.Items, mask, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2,
		"two rows with different `a` are two groups; one row means the key was emptied")
}

// The scalar-aggregator form of the same collision — nothing about the defect
// was specific to collect().
func TestAnalyseGrouping_DuplicateAliasWithScalarAggregatorLeavesNoDependence(t *testing.T) {
	const body = `
MATCH (n:thing)
WITH n.owner AS a, max(n.rank) AS a
WITH a, collect(a) AS s
RETURN a AS k, s AS v`

	plans := groupingPlans(t, body)
	require.Contains(t, plans[0].Refusal, "twice")
	require.Equal(t, []bool{false, false}, plans[1].Redundant)
	require.Equal(t, []string{"a"}, plans[1].Key)

	second, mask := secondWithOf(t, body)
	ex := &executor{ctx: context.Background()}
	rows, err := ex.projectItems([]binding{{"a": int64(1)}, {"a": int64(2)}}, second.Items, mask, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

// The alarming instance: the collided alias is the ACTOR the whole read-grant
// producer family groups on. Shadowing it with a collect() under its own name
// is one keystroke from the shape every generated producer emits, and merging
// on it is precisely the cross-actor over-grant §4.4 argues cannot happen.
func TestAnalyseGrouping_AggregateShadowingTheActorLeavesNoDependence(t *testing.T) {
	const body = `
MATCH (identity:identity)
OPTIONAL MATCH (identity)<-[:assignedTo]-(x:task)
WITH identity, collect(x.key) AS identity
WITH identity, collect(identity) AS s
RETURN identity AS k, s AS v`

	plans := groupingPlans(t, body)
	require.Contains(t, plans[0].Refusal, "twice")
	require.Equal(t, []bool{false, false}, plans[1].Redundant,
		"the actor alias names two items, so nothing downstream may treat it as determined")
	require.Equal(t, []string{"identity"}, plans[1].Key)

	second, mask := secondWithOf(t, body)
	ex := &executor{ctx: context.Background()}
	rows, err := ex.projectItems(
		[]binding{{"identity": "actorA"}, {"identity": "actorB"}}, second.Items, mask, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2, "two actors must never share a group")
}

// Two NON-aggregating items sharing one alias is the same defect reached
// without any intersection between the alias sets: the analysis's key {a} is
// coarser than the executor's real (owner, rank) key, so the clause's clean
// aggregate `b` is NOT a function of it either. A `det` of "the aggregating
// aliases minus the non-aggregating ones" would still be disjoint from key —
// and still wrong — which is why a clause with a duplicated alias determines
// nothing at all rather than something smaller.
func TestAnalyseGrouping_DuplicateNonAggAliasAlsoUndeterminesTheCleanAggregate(t *testing.T) {
	const body = `
MATCH (n:thing)
WITH n.owner AS a, n.rank AS a, collect(n.key) AS b
WITH a, b, collect(a) AS c
RETURN a AS k, b AS v, c AS w`

	plans := groupingPlans(t, body)
	require.Contains(t, plans[0].Refusal, "twice")
	require.Equal(t, []bool{false, false, false}, plans[1].Redundant,
		"`b` is an aggregate of a key the analysis can only describe coarsely — not determined")

	// Two rows agreeing on `a` and differing on `b` come from two different
	// groups of the refused clause: dropping `b` from the key merges them.
	second, mask := secondWithOf(t, body)
	ex := &executor{ctx: context.Background()}
	rows, err := ex.projectItems([]binding{
		{"a": int64(1), "b": []any{"x"}},
		{"a": int64(1), "b": []any{"y"}},
	}, second.Items, mask, nil)
	require.NoError(t, err)
	require.Len(t, rows, 2,
		"two groups of the refused clause must not be merged by the clause after it")
}

// The disjointness the whole induction rests on, asserted as an outcome at
// every clause of every witness above — the property that was claimed "by
// construction" and was not.
func TestAnalyseGrouping_KeyAndDeterminedSetsNeverIntersect(t *testing.T) {
	for _, body := range []string{
		`MATCH (n:thing)
WITH n.owner AS a, collect(n.key) AS a
WITH a, collect(a) AS s
RETURN a AS k, s AS v`,
		`MATCH (n:thing)
WITH n.owner AS a, n.rank AS a, collect(n.key) AS b
WITH a, b, collect(a) AS c
RETURN a AS k, b AS v, c AS w`,
		`MATCH (identity:identity)
OPTIONAL MATCH (identity)<-[:assignedTo]-(x:task)
WITH identity, collect(x.key) AS identity
WITH identity, collect(identity) AS s
RETURN identity AS k, s AS v`,
	} {
		key, det := map[string]struct{}{}, map[string]struct{}{}
		for _, c := range mustParseQuery(t, body).Clauses {
			w, isWith := c.(*With)
			if !isWith {
				continue
			}
			_, key, det = analyseGroupingClause(c, w.Items, key, det)
			require.Emptyf(t, firstIn(det, key),
				"an alias may not be both in the grouping key and determined by it: %q\n%s",
				firstIn(det, key), body)
		}
	}
}
