package full

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// putAspect writes an aspect body at the Contract #1 aspect key
// vtx.<class>.<id>.<localName>. The body mirrors what a package's
// make_aspect emits (location-domain/ddls.go), so the engine sees the same
// shape it sees on a live stack: envelope fields plus a `data` object.
func putAspect(t *testing.T, reg *fixtureRegistry, kv *substrate.KV, vertexName, localName string, data map[string]any) {
	t.Helper()
	vk := vtxKey(reg, vertexName)
	require.NotEmpty(t, vk, "fixture: %q not registered", vertexName)
	body, err := json.Marshal(map[string]any{
		"class": localName, "isDeleted": false,
		"vertexKey": vk, "localName": localName, "data": data,
	})
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), vk+"."+localName, body)
	require.NoError(t, err)
}

// The two expression shapes edge-manifest's edgeIdentitySpec depends on, and
// which display-name-convention-design.md §3 N3's live tail could not
// separate from a stale compiled rule: the emitted manifest.me row carried
// neither `sealedName` nor an anchor `name`, with the correct spec installed
// and a reprojection demonstrably running. The design names two candidates —
// (a) Refractor executing a rule older than the spec it holds, or (b) the
// engine not resolving these forms — and calls for exactly this test to tell
// them apart before touching the lens or the renderer.
//
//   - an aspect's whole `.data` object in scalar alias position
//     (identity.name.data AS sealedName), where every other corpus use
//     navigates one field deeper to a leaf (…data.value);
//   - a neighbour's aspect hop INSIDE a collect() map literal
//     (collect({name: loc.presentation.data.name})), where the corpus's
//     collected aspect hops are all off the anchor, not off an
//     OPTIONAL MATCH neighbour.
//
// Both shapes route through the single expression evaluator at
// executor.go's resolveProperty call site, so a divergence here would be a
// real engine gap rather than a lens bug.

// TestAspectExpr_WholeDataObject_InScalarAliasPosition: `x.<aspect>.data`
// with no further navigation yields the aspect's whole data object, not
// null. This is the sealedName shape — the { ct, nonce, keyId } envelope is
// the value the edge engine decrypts, so a null here would mean the N3
// self-name could never arrive regardless of the lens.
func TestAspectExpr_WholeDataObject_InScalarAliasPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putAspect(t, reg, coreKV, "alice", "name", map[string]any{
		"ct": "Y2lwaGVy", "nonce": "bm9uY2U", "keyId": "vtx.key.Kk1aaaaaaaaaaaaaaaaa",
	})

	results := parseExec(t, `
MATCH (identity:identity {key: $actorKey})
RETURN
  identity.key AS anchor,
  identity.name.data AS sealedName,
  identity.name.data.value AS displayName
`, ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	sealed, ok := results[0].Values["sealedName"].(map[string]any)
	require.True(t, ok, "an aspect's whole .data object must resolve in scalar alias position, got %#v",
		results[0].Values["sealedName"])
	require.Equal(t, "Y2lwaGVy", sealed["ct"])
	require.Equal(t, "vtx.key.Kk1aaaaaaaaaaaaaaaaa", sealed["keyId"])
	require.Nil(t, results[0].Values["displayName"],
		"a sealed name has no plaintext .value — this is why N3 projects the envelope")
}

// TestAspectExpr_NeighbourAspectHop_InsideCollect: an OPTIONAL MATCH
// neighbour's aspect field resolves inside a collect() map literal. This is
// the anchors shape — {key, name, container, containerName} where name and
// containerName are aspect hops off two different neighbours.
func TestAspectExpr_NeighbourAspectHop_InsideCollect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "unit1", "unit", nil)
	putVertex(t, reg, coreKV, "bldg", "building", nil)
	putAspect(t, reg, coreKV, "unit1", "presentation", map[string]any{"name": "Unit 1"})
	putAspect(t, reg, coreKV, "bldg", "presentation", map[string]any{"name": "Riverside Building"})
	putEdge(t, reg, adjKV, "residesIn", "alice", "unit1")
	putEdge(t, reg, adjKV, "containedIn", "unit1", "bldg")

	results := parseExec(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
RETURN
  identity.key AS anchor,
  collect(DISTINCT {key: loc.key, name: loc.presentation.data.name, container: container.key, containerName: container.presentation.data.name}) AS anchors
`, ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	anchors, ok := results[0].Values["anchors"].([]any)
	require.True(t, ok, "anchors must collect, got %#v", results[0].Values["anchors"])
	require.Len(t, anchors, 1)
	entry, ok := anchors[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, vtxKey(reg, "unit1"), entry["key"])
	require.Equal(t, "Unit 1", entry["name"],
		"a neighbour's aspect hop must resolve inside a collect map literal")
	require.Equal(t, "Riverside Building", entry["containerName"],
		"a second-hop neighbour's aspect must resolve inside the same map literal")
}
