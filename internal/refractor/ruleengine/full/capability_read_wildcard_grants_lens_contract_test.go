// Contract #6 §6.14 conformance test for the base ALL-ACCESS read-grant
// PRODUCER lens (capabilityReadWildcardGrants, D1 design §3.4 M5) — the
// wildcard sibling of capabilityReadGrants. It runs the LITERAL bootstrap
// cypher through the same `full` auth-plane engine selected at activation and
// asserts the projected grant rows: exactly one per identity holding the
// primordial `operator` role via `holdsRole` (Contract #7 §7.7,
// root-designation-topology-reconverge 2026-07-03), carrying the reserved
// WildcardAnchor ("*") — never for an ordinary actor, and never for a stale
// `data.protected = true` bit alone. This is exactly the grant the
// Postgres-RLS wildcard OR-clause (internal/refractor/adapter.BuildProtectedTableDDL)
// matches — a root-equivalent actor reads every row of every protected table.
package full_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func TestCapabilityReadWildcardGrantsLens_ProjectsOnlyOperatorHolders(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	adjKV, coreKV := contractStartKVs(t)

	// TWO operator-role-holding (root-equivalent, e.g. admin + a service actor)
	// identities and one ordinary identity — proves the WHERE admits every
	// operator-holder (not just a coincidental single match) while still
	// excluding the ordinary one. The ordinary identity also carries a stale
	// `protected:true` bit to prove that literal alone confers nothing
	// (root-designation-topology-reconverge, 2026-07-03).
	adminKey := contractSeedOperatorHolder(t, coreKV, adjKV, "admin")
	loomKey := contractSeedOperatorHolder(t, coreKV, adjKV, "loom")
	_ = contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice", "protected": true})

	body := bootstrap.CapabilityReadWildcardGrantsLensDefinition().CypherRule
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityReadWildcardGrants cypher must parse on the full engine")

	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{}}, adjKV, coreKV)
	require.NoError(t, err, "literal capabilityReadWildcardGrants cypher must execute")
	require.Len(t, out, 2, "one wildcard grant row per operator-holding identity — never the protected-only ordinary one")

	byActor := map[string]map[string]any{}
	for _, r := range out {
		actor, ok := r.Values["actor_id"].(string)
		require.True(t, ok, "actor_id must be a string")
		byActor[actor] = r.Values
	}

	for _, k := range []string{adminKey, loomKey} {
		id := nanoFromVertexKey(t, k)
		row, ok := byActor[id]
		require.Truef(t, ok, "missing wildcard grant for %s (bare NanoID %s); got actors %v", k, id, byActor)
		require.Equal(t, id, row["actor_id"], "actor_id is the operator-holding identity's bare NanoID")
		require.Equal(t, adapter.WildcardAnchor, row["anchor_id"], "anchor_id is the reserved WildcardAnchor")
		require.Equal(t, "cap-read.root", row["grant_source"], "grant_source is the wildcard producer's own disjoint slice id")
	}
}
