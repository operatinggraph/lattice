package full

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// putRawVertex writes a vertex whose KEY type-segment and CLASS field may
// differ — needed to model a service actor whose key is vtx.identity.<id> but
// whose class is the non-plain identity.system.* marker. The standard
// putVertex helper derives the key from the class and so cannot express this.
func putRawVertex(t *testing.T, reg *fixtureRegistry, kv interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
}, name, keyType, class string, extra map[string]any) string {
	t.Helper()
	id := c1NanoID(name)
	vtxKey := "vtx." + keyType + "." + id
	reg.byName[name] = vtxKey
	reg.idByName[name] = id
	reg.typeByID[id] = keyType
	props := map[string]any{"key": vtxKey, "class": class}
	for k, v := range extra {
		props[k] = v
	}
	data, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), vtxKey, data)
	require.NoError(t, err)
	return vtxKey
}

// TestCapabilityLens_ServiceActorClass_TopologyGrantsRoot proves Story 7.3's
// §7.7 non-gating invariant at the cypher level, both directions:
//
//   - loom: key vtx.identity.<id>, class identity.system.loom, WITH a
//     holdsRole → operator edge → projects the operator's scope:any
//     platformPermissions (root-equivalent). The non-plain class does NOT
//     prevent projection (the cypher anchors on the :identity key segment).
//   - imposter: same loom class, NO holdsRole edge → projects ZERO
//     platformPermissions. Class alone never grants capability — only
//     topology does.
func TestCapabilityLens_ServiceActorClass_TopologyGrantsRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// Service actors with the non-plain class but vtx.identity.<id> key.
	loomKey := putRawVertex(t, reg, coreKV, "loom", "identity", "identity.system.loom", map[string]any{"name": "loom"})
	imposterKey := putRawVertex(t, reg, coreKV, "imposter", "identity", "identity.system.loom", map[string]any{"name": "imposter"})

	// Operator role + a scope:any permission.
	putVertex(t, reg, coreKV, "operator", "role", map[string]any{"canonicalName": "operator"})
	putVertex(t, reg, coreKV, "permCreate", "permission", map[string]any{
		"data": map[string]any{"operationType": "CreateMetaVertex", "scope": "any"},
	})
	putEdge(t, reg, adjKV, "grantedBy", "permCreate", "operator")

	// ONLY loom holds the operator role; imposter has the same class but no edge.
	putEdge(t, reg, adjKV, "holdsRole", "loom", "operator")

	body := bootstrap.CapabilityLensDefinition().CypherRule
	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err)

	project := func(actorKey string) []any {
		params := map[string]any{
			"actorKey":    actorKey,
			"now":         float64(time.Now().Unix()),
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		require.NoError(t, err)
		require.Len(t, out, 1, "exactly one row per actor")
		require.Equal(t, actorKey, out[0].Values["actorKey"])
		pp, _ := out[0].Values["platformPermissions"].([]any)
		return pp
	}

	// loom WITH topology → root-equivalent (the scope:any permission projects).
	loomPerms := project(loomKey)
	foundAny := false
	for _, e := range loomPerms {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["operationType"] == "CreateMetaVertex" && m["scope"] == "any" {
			foundAny = true
		}
	}
	require.True(t, foundAny,
		"loom (class identity.system.loom) WITH holdsRole→operator must project the operator's scope:any permission: %v", loomPerms)

	// imposter SAME class, NO topology → zero real permissions. The cypher's
	// OPTIONAL MATCH yields a single degenerate (all-null) collect entry; no
	// row carries a real {operationType, scope}.
	imposterPerms := project(imposterKey)
	for _, e := range imposterPerms {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		require.Nil(t, m["operationType"],
			"imposter (class alone, no holdsRole) must project NO real permission — class never grants: %v", imposterPerms)
	}
}
