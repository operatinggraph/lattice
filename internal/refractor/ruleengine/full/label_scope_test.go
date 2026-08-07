package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// label_scope_test.go pins WHERE a label has to appear before ReferencedLabels
// may report its set as exhaustive. `exhaustive` licenses the callers that skip
// work — the plain reproject gate, the client relevance gate, the actor-aware
// narrowed filter — so a label that does not actually constrain what survives
// must not excuse an unlabeled sighting. Reporting one that does not is how a
// grant stops updating.
//
// The three scopes, and what makes each one what it is:
//
//	required MATCH  constrains the whole segment, both directions — applied to
//	                an already-bound variable it DROPS the bindings that fail,
//	                pruning an earlier whole-bucket seed (executor.applyMatch)
//	OPTIONAL MATCH  constrains from its own clause onward — the path binds as a
//	                unit or null-binds the variables NEW to it, and a failed
//	                match restores any earlier binding intact
//	                (executor.nullBindNewVars)
//	WHERE / comprehension  constrains nothing — the bindings are discarded
//	                (executor.existsAsPredicate, executor.evalPatternComprehension),
//	                so a later MATCH on that name is a fresh whole-bucket seed
//
// and both scopes end at a WITH, which rebuilds every binding from its
// projection items alone.

func labelsOf(t *testing.T, spec string) (map[string]struct{}, bool) {
	t.Helper()
	cr, err := New().Parse(spec)
	require.NoError(t, err)
	full, isFull := cr.(*CompiledRule)
	require.True(t, isFull)
	return full.ReferencedLabels()
}

func TestReferencedLabels_RequiredMatchConstrainsBothDirections(t *testing.T) {
	labels, exhaustive := labelsOf(t, `
MATCH (a)-[:manages]->(b:role)
MATCH (a:identity)
RETURN b.key AS key`)
	require.True(t, exhaustive,
		"a required label prunes the bindings an earlier clause made, so it constrains backward too")
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}}, labels)
}

func TestReferencedLabels_OptionalLabelCannotExcuseAnEarlierSighting(t *testing.T) {
	_, exhaustive := labelsOf(t, `
MATCH (i:identity {key: $actorKey})
MATCH (i)-[:owns]->(b)
OPTIONAL MATCH (b:role)-[:grantedBy]->(p:permission)
RETURN b.key AS key`)
	require.False(t, exhaustive,
		"the optional match cannot drop the whole-bucket binding b already has, so b holds any type")
}

func TestReferencedLabels_OptionalLabelAtFirstSightingStillNarrows(t *testing.T) {
	labels, exhaustive := labelsOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:holdsRole]->(r:role)
OPTIONAL MATCH (r)-[:grantedBy]->(p:permission)
RETURN p.key AS key`)
	require.True(t, exhaustive,
		"r is new to the optional clause that labels it, so downstream it is a role or null")
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels)
}

func TestReferencedLabels_PredicateLabelConstrainsNothing(t *testing.T) {
	labels, exhaustive := labelsOf(t, `
MATCH (i:identity {key: $actorKey})
WHERE NOT (i)-[:blocked]->(b:badge)
MATCH (b)-[:issuedBy]->(o:org)
RETURN o.key AS key`)
	require.False(t, exhaustive,
		"a pattern-expression binding never reaches the row, so MATCH (b) is a fresh bucket scan")
	require.Contains(t, labels, "badge",
		"the set still widens to every type a predicate can read — widening never makes exhaustive wrongly true")
}

// A clause's comma-separated paths are threaded one into the next, and a later
// path's failure null-binds only what is still ABSENT from the row — so an
// earlier path's whole-bucket binding survives, of a type the later path's label
// never constrained. The unit that binds-or-nulls is the path, not the clause.
func TestReferencedLabels_OptionalLabelIsScopedToItsOwnPath(t *testing.T) {
	_, exhaustive := labelsOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:owns]->(x), (x:role)-[:grantedBy]->(p:permission)
RETURN x.key AS key`)
	require.False(t, exhaustive,
		"the second path's failure leaves x bound by the first path's whole-bucket seed, of any type")

	// The mirror: a label on the path that BINDS the variable still narrows, so
	// the scoping costs nothing a comma-separated clause legitimately earns.
	labels, exhaustive := labelsOf(t, `
MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:owns]->(x:role), (x)-[:grantedBy]->(p:permission)
RETURN x.key AS key`)
	require.True(t, exhaustive,
		"x is labeled on the path that binds it, so a later path re-referencing it is constrained")
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels)
}

func TestReferencedLabels_OptionalScopeEndsAtTheWith(t *testing.T) {
	_, exhaustive := labelsOf(t, `
MATCH (a:identity {key: $actorKey})
OPTIONAL MATCH (a)-[:holdsRole]->(role:role)
WITH a
MATCH (role)-[:grantedBy]->(perm:permission)
RETURN perm.key AS key`)
	require.False(t, exhaustive,
		"the WITH drops role, so re-using the name re-seeds through the whole-bucket scan")

	_, exhaustive = labelsOf(t, `
MATCH (a:identity {key: $actorKey})
OPTIONAL MATCH (a)-[:holdsRole]->(role:role)
WITH a
WHERE (role)-[:grantedBy]->(perm:permission)
RETURN a.key AS key`)
	require.False(t, exhaustive,
		"a WITH's own WHERE reads the carried scope, where a dropped optional label no longer applies")
}

func TestReferencedLabels_WithCarriesAnOptionalLabel(t *testing.T) {
	// The shape pkgmgr generates: one required head, every walk-chain clause an
	// OPTIONAL MATCH, staged behind a WITH. Dropping the optional scope at the
	// carry would cost exactly the generated read-grant lenses their narrowing.
	labels, exhaustive := labelsOf(t, `
MATCH (a:identity {key: $actorKey})
OPTIONAL MATCH (a)-[:holdsRole]->(role:role)
WITH a, role
MATCH (role)-[:grantedBy]->(perm:permission)
RETURN perm.key AS key`)
	require.True(t, exhaustive,
		"a carried bare reference keeps its binding, so downstream role is a role or null")
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels)

	labels, exhaustive = labelsOf(t, `
MATCH (a:identity {key: $actorKey})
OPTIONAL MATCH (a)-[:holdsRole]->(role:role)
WITH a, role AS r
MATCH (r)-[:grantedBy]->(perm:permission)
RETURN perm.key AS key`)
	require.True(t, exhaustive, "a renamed carry keeps the label under the new name")
	require.Equal(t, map[string]struct{}{"identity": {}, "role": {}, "permission": {}}, labels)
}
