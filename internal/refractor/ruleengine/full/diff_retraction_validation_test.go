// ValidateUnanchoredForDiffRetraction is the activation-time backstop for
// Fire 3's target-diff retraction (negative-filter-retraction-projection-
// design.md §2.4): the diff is only sound when the compiled query never
// scopes its MATCH to the triggering vertex via $actorKey, since the
// mechanism compares the target's FULL live key set against the
// re-execute's FULL freshly-computed row set.
package full_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func compileForValidation(t *testing.T, body string) *full.CompiledRule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err)
	fcr, ok := cr.(*full.CompiledRule)
	require.True(t, ok, "expected *full.CompiledRule")
	return fcr
}

func TestValidateUnanchoredForDiffRetraction_UnanchoredWholeScan_OK(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(landlord.key) AS landlord_id
`)
	assert.NoError(t, cr.ValidateUnanchoredForDiffRetraction())
}

func TestValidateUnanchoredForDiffRetraction_ActorKeyInNodeProperties_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (app:leaseapp {key: $actorKey})
RETURN app.key AS key
`)
	err := cr.ValidateUnanchoredForDiffRetraction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unanchored")
}

func TestValidateUnanchoredForDiffRetraction_ActorKeyInWhere_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (app:leaseapp)
WHERE app.key = $actorKey
RETURN app.key AS key
`)
	err := cr.ValidateUnanchoredForDiffRetraction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unanchored")
}

func TestValidateUnanchoredForDiffRetraction_ActorKeyInRelProperties_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (app:leaseapp)-[r:appliesToUnit {key: $actorKey}]->(u:unit)
RETURN app.key AS key
`)
	err := cr.ValidateUnanchoredForDiffRetraction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unanchored")
}

func TestValidateUnanchoredForDiffRetraction_ActorKeyInReturn_Rejected(t *testing.T) {
	cr := compileForValidation(t, `
MATCH (app:leaseapp)
RETURN app.key AS key, $actorKey AS triggeredBy
`)
	err := cr.ValidateUnanchoredForDiffRetraction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unanchored")
}

func TestValidateUnanchoredForDiffRetraction_NowAndProjectedAtParamsAllowed(t *testing.T) {
	// $now / $projectedAt are global-per-execution values, not per-anchor
	// scoping seeds — a query using them (but never $actorKey) stays valid.
	cr := compileForValidation(t, `
MATCH (app:leaseapp)
RETURN app.key AS key, $now AS asOf
`)
	assert.NoError(t, cr.ValidateUnanchoredForDiffRetraction())
}
