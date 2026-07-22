// Contract #6 §6.14 conformance test for the base read-grant PRODUCER lens
// (capabilityReadGrants, D1.3) — the Postgres GrantTable twin of
// capabilityReadLens. It runs the LITERAL bootstrap cypher through the same
// `full` auth-plane engine selected at activation and asserts the projected
// grant rows: one per identity, carrying the self-anchor as
// (actor_id == anchor_id == nanoIdFromKey(key), grant_source == 'cap-read').
// This is exactly the grant the Postgres-RLS set-membership policy matches — an
// actor may always read its own vertex (A sees only A's protected rows).
//
// The Postgres round-trip (this row → GrantWriterAdapter → actor_read_grants →
// RLS) is the already-green Fire-1b POSTGRES_TEST_DSN seam proof; the cypher's
// grant derivation is proven here, deterministically, with no Postgres.
package full_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// nanoFromVertexKey returns the bare NanoID (3rd dot-segment) of a vtx.<type>.<id>
// key — the §6.14 anchor representation nanoIdFromKey produces.
func nanoFromVertexKey(t *testing.T, key string) string {
	t.Helper()
	parts := strings.Split(key, ".")
	require.Len(t, parts, 3, "vertex key must be vtx.<type>.<id>: %q", key)
	return parts[2]
}

func TestCapabilityReadGrantsLens_ProjectsSelfGrants(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	adjKV, coreKV := contractStartKVs(t)

	// Two ORDINARY (non-protected) identities — self-read is the universal,
	// package-independent grant every actor holds, not a kernel privilege.
	aliceKey := contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice"})
	bobKey := contractPutVertex(t, coreKV, "identity", "bob", map[string]any{"name": "bob"})

	body := bootstrap.CapabilityReadGrantsLensDefinition().CypherRule
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityReadGrants cypher must parse on the full engine")

	// Unparameterized plain projection: MATCH (identity:identity) → one row per
	// identity (not $actorKey-scoped like the NATS-KV twin).
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{}}, adjKV, coreKV)
	require.NoError(t, err, "literal capabilityReadGrants cypher must execute")
	require.Len(t, out, 2, "one grant row per identity")

	byActor := map[string]map[string]any{}
	for _, r := range out {
		actor, ok := r.Values["actor_id"].(string)
		require.True(t, ok, "actor_id must be a string")
		byActor[actor] = r.Values
	}

	for _, k := range []string{aliceKey, bobKey} {
		id := nanoFromVertexKey(t, k)
		row, ok := byActor[id]
		require.Truef(t, ok, "missing self-grant for %s (bare NanoID %s); got actors %v", k, id, byActor)
		require.Equal(t, id, row["actor_id"], "actor_id is the bare NanoID")
		require.Equal(t, id, row["anchor_id"], "self-anchor: anchor_id == actor_id (an actor reads its own vertex)")
		require.Equal(t, "cap-read", row["grant_source"], "grant_source is the base slice id")
	}
}
