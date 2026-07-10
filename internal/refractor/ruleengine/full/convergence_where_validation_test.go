// ValidateNoFilteringWhereForConvergence is the activation-time backstop for
// the convergence-lens no-filtering-WHERE authoring invariant
// (negative-filter-retraction-projection-design.md's review carry-out;
// docs/components/refractor.md): a plain (non-actorAggregate) lens
// projecting into the shared weaver-targets bucket must never carry a
// filtering WHERE that can drop its anchor out of the RETURN row set.
package full_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateNoFilteringWhereForConvergence_NoWhere_OK(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (task:task {key: $actorKey})-[:queuedFor]->(role:role)
RETURN task.key AS key, true AS violating
`)
	assert.NoError(t, cr.ValidateNoFilteringWhereForConvergence())
}

func TestValidateNoFilteringWhereForConvergence_RequiredMatchWhere_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (task:task {key: $actorKey})-[:queuedFor]->(role:role)
WHERE task.data.status = 'open'
RETURN task.key AS key, true AS violating
`)
	err := cr.ValidateNoFilteringWhereForConvergence()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filtering WHERE")
}

func TestValidateNoFilteringWhereForConvergence_OptionalMatchWhere_OK(t *testing.T) {
	// A WHERE on an OPTIONAL MATCH cannot drop the anchor row — a failed
	// optional predicate restores nulls for that pattern's bindings rather
	// than removing the row (the null-restore semantics,
	// docs/components/refractor.md).
	cr := compileForValidation(t, `
MATCH (task:task {key: $actorKey})
OPTIONAL MATCH (task)-[:queuedFor]->(role:role)
WHERE role.data.status = 'open'
RETURN task.key AS key, true AS violating
`)
	assert.NoError(t, cr.ValidateNoFilteringWhereForConvergence())
}

func TestValidateNoFilteringWhereForConvergence_WithWhere_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (task:task {key: $actorKey})
WITH task
WHERE task.data.status = 'open'
RETURN task.key AS key, true AS violating
`)
	err := cr.ValidateNoFilteringWhereForConvergence()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filtering WHERE")
}
