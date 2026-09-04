package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAnchorProjectionShape_WithAliasResolution is the shape table for the
// closure predicate's WITH arm. Every case is a SHAPE rather than a lens: the
// corpus census (internal/refractor/plain_with_alias_closure_census_test.go)
// pins which installed lenses land where, and this pins WHY, on the smallest
// query that carries the shape.
//
// The two directions are equally load-bearing. An admitted case that should
// refuse hands a Delete against live rows a key it cannot prove belongs to the
// anchor; a refused case that should admit costs a whole-corpus rescan and a
// lost retraction.
func TestAnchorProjectionShape_WithAliasResolution(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		keyCols []string
		admit   bool
		why     string
	}{
		{
			name:    "single WITH passthrough",
			spec:    "MATCH (app:leaseapp) WITH app.key AS entityKey RETURN nanoIdFromKey(entityKey) AS app_id",
			keyCols: []string{"app_id"},
			admit:   true,
			why:     "the alias resolves to nanoIdFromKey(app.key) — the anchor's own key, one boundary back",
		},
		{
			name:    "chained WITHs compose",
			spec:    "MATCH (app:leaseapp) WITH app.key AS k1 WITH nanoIdFromKey(k1) AS k2 RETURN k2 AS app_id",
			keyCols: []string{"app_id"},
			admit:   true,
			why:     "each boundary resolves against the one before it, so the chain composes to nanoIdFromKey(app.key)",
		},
		{
			name:    "WITH carrying the anchor binding bare",
			spec:    "MATCH (op:meta)-[:usedBy]->(role:role) WITH op, role RETURN op.data.operationType AS operationType",
			keyCols: []string{"operationType"},
			admit:   true,
			why:     "a binding carried under its own name still binds what it always bound, so the key column needs no substitution",
		},
		{
			name:    "alias shadowed, last boundary binds the anchor",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH u.key AS v, app WITH app.key AS v RETURN nanoIdFromKey(v) AS app_id",
			keyCols: []string{"app_id"},
			admit:   true,
			why:     "the name is resolved against the boundary that last bound it, which here is the anchor's key",
		},
		{
			name:    "alias shadowed, last boundary binds a neighbour",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH app.key AS v, u WITH u.key AS v RETURN v AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why:     "the last binding wins, and it is the unit's key — a neighbour-keyed row the anchor does not determine",
		},
		{
			name:    "aggregate in the resolved key",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH app, count(u) AS n RETURN n AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why:     "an aggregate's value depends on the grouped row set, which a read-free single-anchor binding fabricates",
		},
		{
			name:    "key column resolves to a non-anchor variable",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH u.key AS unitKey, app RETURN unitKey AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why:     "resolution answers WHICH variable, and this one is the unit, so the row is keyed by a neighbour",
		},
		{
			name:    "dropped then re-referenced anchor",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH u.key AS unitKey RETURN app.key AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why: "the boundary dropped `app`, so the RETURN's `app` is a FRESH whole-bucket binding rather than " +
				"the matched anchor — the resolved expression reads as the anchor's key and is not one",
		},
		{
			name:    "pattern variable renamed away",
			spec:    "MATCH (app:leaseapp) WITH app AS a2 RETURN a2.key AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why:     "the binding travels under a name no pattern position holds",
		},
		{
			name:    "renamed onto a pattern variable",
			spec:    "MATCH (app:leaseapp)-[:appliesToUnit]->(u:unit) WITH app.key AS x, u WITH x AS u RETURN u AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why:     "a value carried under a pattern variable's own name grafts onto that position",
		},
		{
			name:    "unmodelled node reached from a key column",
			spec:    "MATCH (app:leaseapp) WITH CASE WHEN app.status = 'live' THEN app.key ELSE app.key END AS c RETURN c AS app_id",
			keyCols: []string{"app_id"},
			admit:   false,
			why: "the resolver reconstructs a value and does not model a CASE, so which variable produced this " +
				"one is unknown — and an unresolved node left in place would be read as naming pattern variables",
		},
		{
			name: "unmodelled node reached only by a sibling item",
			spec: "MATCH (app:leaseapp) WITH app.key AS entityKey, CASE WHEN app.status = 'live' THEN 1 ELSE 2 END AS flag " +
				"RETURN nanoIdFromKey(entityKey) AS app_id",
			keyCols: []string{"app_id"},
			admit:   true,
			why:     "resolution is driven from the key columns, so an item no key column reaches is irrelevant to the verdict",
		},
		{
			name:    "no WITH, and a shape the resolver does not model",
			spec:    "MATCH (app:leaseapp) RETURN CASE WHEN app.status = 'live' THEN app.key ELSE app.key END AS app_id",
			keyCols: []string{"app_id"},
			admit:   true,
			why: "a query with no boundary substitutes nothing — its RETURN already names pattern variables, and " +
				"the conjuncts below judge it exactly as written",
		},
		{
			name:    "a later MATCH after a WITH that carries the anchor",
			spec:    "MATCH (a:x) WITH a, a.key AS k MATCH (b:y {key: k}) RETURN nanoIdFromKey(a.key) AS id",
			keyCols: []string{"id"},
			admit:   true,
			why:     "the boundary still carries `a` under its own name, so the key column's own `a.key` never crosses through the dropped alias at all",
		},
		{
			name:    "a later MATCH after a WITH that drops the anchor, then the RETURN re-reads it",
			spec:    "MATCH (a:x) WITH a.key AS k MATCH (b:y {key: k}) RETURN a.key AS id",
			keyCols: []string{"id"},
			admit:   false,
			why:     "the boundary dropped `a`, so the RETURN's `a` is step 1's re-reference hazard — a fresh whole-bucket binding, not the matched anchor",
		},
		{
			name:    "WITH DISTINCT still resolves the key column",
			spec:    "MATCH (a:x) WITH DISTINCT a, a.key AS k RETURN k AS id",
			keyCols: []string{"id"},
			admit:   true,
			why:     "DISTINCT changes row de-duplication, not which pattern variable a carried alias resolves back to",
		},
		{
			name:    "alias name collides with a later MATCH's node variable",
			spec:    "MATCH (a:x) WITH a.key AS b MATCH (b:y)-[:r]->(c:z) RETURN b AS id",
			keyCols: []string{"id"},
			admit:   false,
			why:     "withCarries refuses a computed item projected under `b` once the whole-query node scan has already seen `b` name a later pattern's node variable",
		},
	}

	eng := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := parseForShape(t, eng, tc.spec, tc.keyCols)
			require.Equalf(t, tc.admit, cr.HasAnchorOnlyKeyColumns(), "%s", tc.why)
		})
	}
}

// TestAnchorProjectionShape_StarProjectionNeverReachesTheShape records where
// the `WITH *` refusal actually lives. The scope walk the closure predicate
// composes with does model the shape — an empty projection list, whose carried
// set is not merely unknown but unrepresented — but no rule carrying one can
// ever reach the predicate, because Parse refuses the star outright, in front
// of the author. The case is pinned here rather than in the shape table above
// so that a later relaxation of the parser's refusal fails this test instead of
// silently handing the closure predicate a shape nothing downstream tests.
func TestAnchorProjectionShape_StarProjectionNeverReachesTheShape(t *testing.T) {
	_, err := New().Parse("MATCH (app:leaseapp) WITH * RETURN app.key AS app_id")
	require.Error(t, err, "a WITH * spec must not compile at all")
	require.Contains(t, err.Error(), "a WITH projection may not use `*`")
}

// TestAnchorProjectionShape_UnparsedRuleRefusesWith is the fail-closed half of
// the alias resolution's lifetime, and it has no other guard: the environment
// is built by Parse, so a *CompiledRule assembled directly — a test rule, or
// any future non-Parse construction — carries none, and a WITH-bearing query on
// it must refuse rather than be judged against an environment nobody built.
//
// Both vectors, because the refusal is only meaningful against the admission:
// the SAME query through Parse is admitted, so the direct construction is what
// the refusal is about, not the query.
func TestAnchorProjectionShape_UnparsedRuleRefusesWith(t *testing.T) {
	const spec = "MATCH (app:leaseapp) WITH app.key AS entityKey RETURN nanoIdFromKey(entityKey) AS app_id"
	eng := New()

	parsed := parseForShape(t, eng, spec, []string{"app_id"})
	require.True(t, parsed.HasAnchorOnlyKeyColumns(),
		"positive vector: through Parse the alias resolves and the lens is admitted")

	direct := &CompiledRule{Query: parsed.Query, KeyColumns: []string{"app_id"}}
	require.False(t, direct.HasAnchorOnlyKeyColumns(),
		"a directly constructed rule has no alias environment, so the same WITH-bearing query must refuse")
	require.False(t, direct.ProjectsOneRowPerAnchor(),
		"and the write licence's own conjunct inherits that refusal through the shared shape")

	directNoWith := &CompiledRule{
		Query:      parseForShape(t, eng, "MATCH (app:leaseapp) RETURN nanoIdFromKey(app.key) AS app_id", []string{"app_id"}).Query,
		KeyColumns: []string{"app_id"},
	}
	require.True(t, directNoWith.HasAnchorOnlyKeyColumns(),
		"the missing environment refuses only a WITH-bearing query — a directly constructed rule with no "+
			"boundary is unaffected, which is why an unresolved rule and an empty environment are separate signals")
}

func parseForShape(t *testing.T, eng *Engine, spec string, keyCols []string) *CompiledRule {
	t.Helper()
	compiled, err := eng.Parse(spec)
	require.NoErrorf(t, err, "spec must parse: %s", spec)
	cr, isFull := compiled.(*CompiledRule)
	require.True(t, isFull)
	cr.KeyColumns = keyCols
	require.NoErrorf(t, cr.ValidateKeyColumns(), "key columns must be RETURN aliases: %s", spec)
	return cr
}
