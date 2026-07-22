package full

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// putRawVertex writes a vertex whose KEY type-segment and CLASS field may
// differ — needed to model a service actor whose key is vtx.identity.<id> but
// whose class is the non-plain identity.system.* marker. The standard
// putVertex helper derives the key from the class and so cannot express this.
// `extra` is written under the `data` envelope (where the anchor cypher reads
// node properties as node.data.<field>).
func putRawVertex(t *testing.T, reg *fixtureRegistry, kv interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
}, name, keyType, class string, data map[string]any) string {
	t.Helper()
	id := c1NanoID(name)
	vtxKey := "vtx." + keyType + "." + id
	reg.byName[name] = vtxKey
	reg.idByName[name] = id
	reg.typeByID[id] = keyType
	props := map[string]any{"key": vtxKey, "class": class, "data": data}
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), vtxKey, raw)
	require.NoError(t, err)
	return vtxKey
}

// putOperatorRoleHolder wires actorName (already a registered identity) to a
// freshly-seeded `operator` role via `holdsRole` (Contract #7 §7.7,
// root-designation-topology-reconverge 2026-07-03). The role's canonicalName
// is written as a real ASPECT entry, mirroring production, so the cypher's
// role.canonicalName.data.value chain exercises the actual aspect point-read
// path (resolveProperty) — not a root-property shortcut.
func putOperatorRoleHolder(t *testing.T, reg *fixtureRegistry, coreKV, adjKV *substrate.KV, actorName string) {
	t.Helper()
	roleKey := putVertex(t, reg, coreKV, actorName+"-operator-role", "role", nil)
	aspectKey := substrate.AspectKey(roleKey, "canonicalName")
	body := map[string]any{"key": aspectKey, "class": "canonicalName", "data": map[string]any{"value": "operator"}}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), aspectKey, raw)
	require.NoError(t, err)
	putEdge(t, reg, adjKV, "holdsRole", actorName, actorName+"-operator-role")
}

// TestCapabilityLens_PrimordialAnchor_OperatorHolderGrantsRoot proves the
// shrunk primordial-identity anchor projects the fixed kernel root-grant set
// for identities holding the primordial `operator` role via `holdsRole`
// (Contract #7 §7.7) and NOTHING for ordinary actors — without any
// rbac-permission graph vocabulary. Both directions:
//
//   - loom: key vtx.identity.<id>, class identity.system.loom, holds the
//     `operator` role → projects the operator's scope:any root grants. The
//     non-plain class does NOT prevent projection (the cypher anchors on the
//     :identity key segment); the `holdsRole` topology is what selects it.
//   - ordinary: a plain identity holding no role at all → ZERO rows, so core
//     writes NO cap.<actor> doc. Ordinary actors read their role-derived
//     grants from rbac-domain's cap.roles.<actor> projection instead.
func TestCapabilityLens_PrimordialAnchor_OperatorHolderGrantsRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// Operator-holding system actor with the non-plain class but
	// vtx.identity.<id> key.
	loomKey := putRawVertex(t, reg, coreKV, "loom", "identity", "identity.system.loom",
		map[string]any{"name": "loom"})
	putOperatorRoleHolder(t, reg, coreKV, adjKV, "loom")
	// Ordinary actor: plain identity, holds no role.
	ordinaryKey := putRawVertex(t, reg, coreKV, "ordinary", "identity", "identity",
		map[string]any{"name": "ordinary"})

	body := bootstrap.CapabilityLensDefinition().CypherRule
	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err)

	project := func(actorKey string) []ruleengine.ProjectionResult {
		params := map[string]any{
			"actorKey":    actorKey,
			"now":         float64(time.Now().Unix()),
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		out, execErr := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		require.NoError(t, execErr)
		return out
	}

	// loom (operator-holder) → exactly one row carrying the fixed root-grant set.
	loomRows := project(loomKey)
	require.Len(t, loomRows, 1, "an operator-holding system identity must project exactly one row")
	require.Equal(t, loomKey, loomRows[0].Values["actorKey"])
	pp, _ := loomRows[0].Values["platformPermissions"].([]any)
	wantOps := map[string]bool{
		"CreateMetaVertex": false, "UpdateMetaVertex": false, "TombstoneMetaVertex": false,
		"InstallPackage": false, "UninstallPackage": false, "UpgradePackage": false,
	}
	for _, e := range pp {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if op, _ := m["operationType"].(string); op != "" {
			require.Equal(t, "any", m["scope"], "every anchor grant is scope:any")
			if _, known := wantOps[op]; known {
				wantOps[op] = true
			}
		}
	}
	for op, seen := range wantOps {
		require.Truef(t, seen, "operator-holding identity must carry the %q root grant: %v", op, pp)
	}

	// ordinary (holds no role) → ZERO rows: core writes no cap.<actor> doc.
	ordinaryRows := project(ordinaryKey)
	require.Empty(t, ordinaryRows,
		"an ordinary identity holding no role must project no row from the core anchor")
}
