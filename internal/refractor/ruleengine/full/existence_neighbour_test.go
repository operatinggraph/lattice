// ExistenceDependsOnNeighbour is the classifier the business plane's
// retraction-transport gate runs on every plain lens
// (secure-plain-lens-retraction-and-audit-design.md §4.4): a lens whose rows
// can be dropped by a NEIGHBOUR event needs a transport no anchor event names.
//
// The vectors below are the design's own contract for it, plus the two
// directions that make the exhaustive flag load-bearing: an alias whose
// provenance the resolver cannot reconstruct, and a rule that never went
// through Parse — both of which must read "could not tell", never "does not
// depend".
package full_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func TestExistenceDependsOnNeighbour_Contract(t *testing.T) {
	t.Run("a required hop depends", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)-[:applicationFor]->(unit:unit)
RETURN app.key AS key, unit.data.label AS unit_label
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
		assert.NotEmpty(t, reasons)
		assert.Contains(t, reasons[0], "unit")
	})

	t.Run("an anchor-only MATCH does not depend", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
RETURN app.key AS key, app.data.status AS status
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends)
		assert.Empty(t, reasons)
	})

	t.Run("an OPTIONAL-only neighbour does not depend", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(unit:unit)
RETURN app.key AS key, unit.data.label AS unit_label
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends)
	})

	t.Run("a WHERE on an OPTIONAL neighbour's own MATCH does not depend", func(t *testing.T) {
		// The null-restore semantics: a failed optional predicate restores
		// nulls for that pattern's bindings rather than removing the row.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(unit:unit)
WHERE unit.data.label = 'A'
RETURN app.key AS key, unit.data.label AS unit_label
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends)
	})

	t.Run("a WHERE on a non-anchor variable depends", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(unit:unit)
WITH app, unit
WHERE unit.data.label = 'A'
RETURN app.key AS key, unit.data.label AS unit_label
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
		require.NotEmpty(t, reasons)
		assert.Contains(t, reasons[0], "unit")
	})

	t.Run("a WHERE on an anchor-only alias does not depend", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
WITH app, app.data.status AS status
WHERE status = 'open'
RETURN app.key AS key, status AS status
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends)
	})

	t.Run("a WHERE on an aggregate over an OPTIONAL neighbour depends", func(t *testing.T) {
		// The shape the classifier exists for: the WHERE names only `n`, which
		// no pattern binds, and the OPTIONAL MATCH above it cannot drop the
		// row on its own — but `count(unit) > 0` does.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(unit:unit)
WITH app, count(unit) AS n
WHERE n > 0
RETURN app.key AS key, n AS unit_count
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
		require.NotEmpty(t, reasons)
		assert.Contains(t, reasons[0], "unit")
		assert.Contains(t, reasons[0], "n")
	})

	t.Run("an unparsed rule carrying a WITH is not exhaustive", func(t *testing.T) {
		// A directly-constructed rule never went through Parse, so no alias
		// environment exists — and an empty environment is exactly what a
		// WITH-free query yields, which must stay admissible. Collapsing the
		// two is the fail-open shape the withAliasResolved flag exists for.
		cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{
			&full.Match{Patterns: []full.PathPattern{{
				Nodes: []full.NodePattern{{Variable: "app", Label: "leaseapp"}},
			}}},
			&full.With{Items: []full.ProjectionItem{
				{Expr: &full.VariableRef{Name: "app"}, Alias: "app"},
			}, Where: &full.BinaryOp{
				Op:    "=",
				Left:  &full.PropertyAccess{Target: &full.VariableRef{Name: "app"}, Key: "status"},
				Right: &full.Literal{Value: "open"},
			}},
			&full.Return{Items: []full.ProjectionItem{
				{Expr: &full.PropertyAccess{Target: &full.VariableRef{Name: "app"}, Key: "key"}, Alias: "key"},
			}},
		}}}
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		assert.False(t, exhaustive)
		assert.False(t, depends)
		assert.Empty(t, reasons)
	})

	t.Run("a nil rule is not exhaustive", func(t *testing.T) {
		var cr *full.CompiledRule
		_, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		assert.False(t, exhaustive)
	})
}

// TestExistenceDependsOnNeighbour_UnresolvableAliasIsNotExhaustive proves the
// second half of the refusal: a WHERE that reads an alias the WITH-alias
// resolver cannot reconstruct answers "could not tell". The vector is a
// positive one first — the same query with a resolvable alias answers
// exhaustively — so the refusal is not being read off a query that fails for
// an unrelated reason.
func TestExistenceDependsOnNeighbour_UnresolvableAliasIsNotExhaustive(t *testing.T) {
	resolvable := compileForValidation(t, `
MATCH (app:leaseapp)
WITH app, app.data.status AS status
WHERE status = 'open'
RETURN app.key AS key, status AS status
`)
	_, _, exhaustive := resolvable.ExistenceDependsOnNeighbour()
	require.True(t, exhaustive, "the positive vector must answer exhaustively before the refusal means anything")

	// A CASE expression is outside the shapes substituteAliases reconstructs,
	// so the alias it binds has an unmodelled provenance and the WHERE reading
	// it cannot be judged.
	unresolvable := compileForValidation(t, `
MATCH (app:leaseapp)
WITH app, CASE WHEN app.data.status = 'open' THEN 1 ELSE 0 END AS flag
WHERE flag = 1
RETURN app.key AS key, flag AS flag
`)
	depends, _, exhaustive := unresolvable.ExistenceDependsOnNeighbour()
	assert.False(t, exhaustive)
	assert.False(t, depends)
}

// TestExistenceDependsOnNeighbour_AnonymousElements holds the classifier to the
// elements a pattern reaches under NO NAME.
//
// The bindings a pattern declares are not the elements it traverses:
// `MATCH (a:x)-[:rel]->(:y)` binds only the anchor while its row lives or dies
// with a link and a `:y`, and `WHERE NOT (a)-->(:role)` gates existence on a
// neighbourhood no variable appears in. Read off the bindings alone both answer
// "does not depend" — a lens with a real retraction obligation activating with
// none, which is the one direction this classifier must never take.
//
// The positive vector runs first: an anchor-only rule must still answer false,
// or a green result here would be a classifier that calls everything a
// dependency.
func TestExistenceDependsOnNeighbour_AnonymousElements(t *testing.T) {
	t.Run("an anchor-only rule still does not depend", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
RETURN app.key AS key, app.data.status AS status
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends, "without this the vectors below prove only that the classifier says yes to everything")
	})

	t.Run("a required MATCH to an anonymous node depends", func(t *testing.T) {
		// The anchor's OWN clause is where an anonymous hop is expressible: a
		// later required MATCH naming no new variable is refused outright
		// (ValidateRequiredMatchIntroducesBinding), and its refusal message
		// sends an author to a WHERE — which is the shape the next vector
		// covers.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)-[:applicationFor]->(:unit)
RETURN app.key AS key, app.data.status AS status
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends, "the unit's tombstone drops this row and no anchor event names it")
		require.NotEmpty(t, reasons)
		assert.Contains(t, reasons[0], ":unit", "the reason names the pattern, which is all an author has to find it by")
	})

	t.Run("an anonymous node in a WHERE pattern predicate depends", func(t *testing.T) {
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
WHERE (app)-[:applicationFor]->(:unit)
RETURN app.key AS key, app.data.status AS status
`)
		depends, reasons, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
		require.NotEmpty(t, reasons)
		assert.Contains(t, reasons[0], ":unit")
	})

	t.Run("a negated pattern predicate depends", func(t *testing.T) {
		// The row exists BECAUSE the neighbourhood is empty, so a link arriving
		// two hops out drops it — the same obligation in the other direction.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
WHERE NOT (app)-->(:role)
RETURN app.key AS key, app.data.status AS status
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
	})

	t.Run("a self-loop depends", func(t *testing.T) {
		// Both endpoints are the anchor, so no non-anchor BINDING exists — and
		// the row still turns on a link, which another event can remove.
		// Fail-closed is the correct reading: the transport is owed.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)-[:supersedes]->(app)
RETURN app.key AS key, app.data.status AS status
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.True(t, depends)
	})

	t.Run("an OPTIONAL MATCH to an anonymous node does not depend", func(t *testing.T) {
		// The null-restore semantics are unchanged by anonymity: a failed
		// optional pattern restores nulls rather than removing the row.
		cr := compileForValidation(t, `
MATCH (app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(:unit)
RETURN app.key AS key, app.data.status AS status
`)
		depends, _, exhaustive := cr.ExistenceDependsOnNeighbour()
		require.True(t, exhaustive)
		assert.False(t, depends)
	})
}
