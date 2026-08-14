package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// parseRule compiles a cypher spec to the concrete *CompiledRule the accessor
// hangs off, so every positional case below is asserted against the AST the
// production parser really builds rather than a hand-assembled one.
func parseRule(t *testing.T, spec string) *CompiledRule {
	t.Helper()
	cr, err := New().Parse(spec)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)
	return compiled
}

// TestReferencesParam_EverySyntacticPosition walks the positions a parameter
// can legally occupy. The accessor's whole value is that a caller may treat a
// (false, true) answer as authoritative, so a position the walk misses is not a
// missing feature — it is a wrong answer, delivered confidently.
func TestReferencesParam_EverySyntacticPosition(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"WHERE", `
MATCH (u:unit)
WHERE u.listing.data.since < $now
RETURN u.key AS key
`},
		{"RETURN", `
MATCH (u:unit)
RETURN u.key AS key, $now AS observedAt
`},
		{"CASE", `
MATCH (u:unit)
RETURN u.key AS key, CASE WHEN u.name = $now THEN 'yes' ELSE 'no' END AS flag
`},
		{"NOT", `
MATCH (u:unit)
WHERE NOT (u.name = $now)
RETURN u.key AS key
`},
		{"WITH projection", `
MATCH (u:unit)
WITH u, $now AS at
RETURN u.key AS key, at AS at
`},
		{"WITH where", `
MATCH (u:unit)
WITH u, u.name AS n
WHERE n <> $now
RETURN u.key AS key, n AS n
`},
		{"pattern property map", `
MATCH (u:unit {name: $now})
RETURN u.key AS key
`},
		{"function argument", `
MATCH (u:unit)
RETURN u.key AS key, coalesce(u.name, $now) AS n
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := parseRule(t, tc.spec)
			referenced, exhaustive := cr.ReferencesParam("now")
			require.True(t, exhaustive, "every clause and expression in this spec is modelled")
			require.True(t, referenced, "$now is referenced in the %s position", tc.name)
		})
	}
}

// TestReferencesParam_AbsentParamIsAnAuthoritativeNo is the positive vector for
// every refusal test built on this accessor: without it, a green "does not
// reference $now" could equally come from a walk that never looks.
func TestReferencesParam_AbsentParamIsAnAuthoritativeNo(t *testing.T) {
	cr := parseRule(t, `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.key AS key, u.name AS name
`)
	referenced, exhaustive := cr.ReferencesParam("now")
	require.False(t, referenced)
	require.True(t, exhaustive)

	// And the walk discriminates between parameters rather than reporting any
	// parameter sighting.
	anchored := parseRule(t, `
MATCH (u:unit {key: $actorKey})
RETURN u.key AS key
`)
	referenced, exhaustive = anchored.ReferencesParam("now")
	require.False(t, referenced)
	require.True(t, exhaustive)
	referenced, exhaustive = anchored.ReferencesParam("actorKey")
	require.True(t, referenced)
	require.True(t, exhaustive)
}

// TestReferencesParam_UnmodelledNodeKindIsNotExhaustive pins the fail-closed
// direction at both levels of the AST. The pair (false, false) is the answer a
// caller must read as "assume it does" — never as an absence.
func TestReferencesParam_UnmodelledNodeKindIsNotExhaustive(t *testing.T) {
	t.Run("expression", func(t *testing.T) {
		cr := &CompiledRule{Query: &Query{Clauses: []Clause{
			&Match{Patterns: []PathPattern{{Nodes: []NodePattern{{Variable: "u", Label: "unit"}}}}},
			&Return{Items: []ProjectionItem{{Expr: fakeUnknownExpr{}, Alias: "key"}}},
		}}}
		referenced, exhaustive := cr.ReferencesParam("now")
		require.False(t, exhaustive, "an Expr shape the walk does not model cannot be ruled out")
		require.False(t, referenced)
	})

	t.Run("clause", func(t *testing.T) {
		cr := &CompiledRule{Query: &Query{Clauses: []Clause{
			&Match{Patterns: []PathPattern{{Nodes: []NodePattern{{Variable: "u", Label: "unit"}}}}},
			fakeUnknownClause{},
			&Return{Items: []ProjectionItem{{Expr: &VariableRef{Name: "u"}, Alias: "key"}}},
		}}}
		referenced, exhaustive := cr.ReferencesParam("now")
		require.False(t, exhaustive, "a Clause shape the walk does not model cannot be ruled out")
		require.False(t, referenced)
	})

	t.Run("a sighting alongside an unmodelled node still reports the sighting", func(t *testing.T) {
		// found and exhaustive are independent: an unmodelled node degrades the
		// confidence in a NEGATIVE, and must not suppress a positive the walk
		// did make.
		cr := &CompiledRule{Query: &Query{Clauses: []Clause{
			&Match{
				Patterns: []PathPattern{{Nodes: []NodePattern{{Variable: "u", Label: "unit"}}}},
				Where:    &BinaryOp{Op: "=", Left: &VariableRef{Name: "u"}, Right: &ParameterRef{Name: "now"}},
			},
			&Return{Items: []ProjectionItem{{Expr: fakeUnknownExpr{}, Alias: "key"}}},
		}}}
		referenced, exhaustive := cr.ReferencesParam("now")
		require.True(t, referenced)
		require.False(t, exhaustive)
	})
}

// TestReferencesParam_NilRuleIsNotExhaustive keeps the zero value on the safe
// side: a caller holding no compiled rule learns nothing, and must not read
// that as "no reference".
func TestReferencesParam_NilRuleIsNotExhaustive(t *testing.T) {
	var cr *CompiledRule
	referenced, exhaustive := cr.ReferencesParam("now")
	require.False(t, referenced)
	require.False(t, exhaustive)

	referenced, exhaustive = (&CompiledRule{}).ReferencesParam("now")
	require.False(t, referenced)
	require.False(t, exhaustive)
}
