package full

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// label_key_type_binding_test.go pins the invariant that a cypher pattern label
// IS the Contract #1 vertex key type — `(l:building)` binds a vertex keyed
// `vtx.building.<id>` and nothing else, whatever its body says.
//
// The invariant is load-bearing, not stylistic. Four shipped mechanisms already
// resolve a label as the key type and narrow on the result: the labeled seed
// scan (executor.seedNodes), event seeding (seedAnchorBinds), anchor retraction
// and the D2 seeding eligibility the pipeline reads off it
// (CompiledRule.AnchorLabel in anchor_delete.go), and the narrowing derivation
// (CompiledRule.ReferencedLabels, consumed by the plain reproject gate, the
// client relevance gate and the actor-aware narrowed filter). If the binder
// admitted a second resolution, those four would narrow on a label set the
// executor does not honor — and on the four auth-plane lenses that narrowing
// decides, a row that never updates is a grant that never retracts.
//
// The shared-discriminator need the body offers is served by a property
// predicate — `(l {class: "location"})` — which the last case pins as working
// in the same position.

// putBodyVertex writes a vertex at vtx.<keyType>.<id> whose ROOT body carries
// the given fields verbatim, so a test can express a body whose `class` or
// `label` disagrees with the key's type segment.
func putBodyVertex(t *testing.T, reg *fixtureRegistry, kv *substrate.KV, name, keyType string, body map[string]any) string {
	t.Helper()
	id := c1NanoID(name)
	key := "vtx." + keyType + "." + id
	reg.byName[name] = key
	reg.idByName[name] = id
	reg.typeByID[id] = keyType
	props := map[string]any{"key": key}
	for k, v := range body {
		props[k] = v
	}
	raw, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func TestLabelBindsKeyTypeOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// The divergence the location package really writes: three key types
	// sharing one body class.
	putBodyVertex(t, reg, coreKV, "tower", "building", map[string]any{"class": "location"})
	putBodyVertex(t, reg, coreKV, "unit12", "unit", map[string]any{"class": "location"})
	// A body `label` field, the other resolution a binder could offer.
	putBodyVertex(t, reg, coreKV, "gizmo", "widget", map[string]any{"label": "gadget"})
	putEdge(t, reg, adjKV, "containedIn", "unit12", "tower")
	putEdge(t, reg, adjKV, "attachedTo", "unit12", "gizmo")

	// An aspect key, to prove a labeled point read cannot bind a non-vertex
	// document either — an aspect body's class is its localName.
	aspectKey := substrate.AspectKey(vtxKey(reg, "tower"), "canonicalName")
	raw, err := json.Marshal(map[string]any{
		"key": aspectKey, "class": "canonicalName",
		"data": map[string]any{"value": "tower"},
	})
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), aspectKey, raw)
	require.NoError(t, err)

	run := func(t *testing.T, body string, params map[string]any) []ruleengine.ProjectionResult {
		t.Helper()
		return parseExec(t, body, ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	}

	t.Run("traversal target binds by key type, not body class", func(t *testing.T) {
		out := run(t, `MATCH (u:unit)-[:containedIn]->(l:location) RETURN u.key AS key`, nil)
		require.Empty(t, out, "a vertex keyed vtx.building.<id> is not a :location")

		out = run(t, `MATCH (u:unit)-[:containedIn]->(l:building) RETURN u.key AS key`, nil)
		require.Len(t, out, 1, "its key type is what the label names")
		require.Equal(t, vtxKey(reg, "unit12"), out[0].Values["key"])
	})

	t.Run("the shared discriminator is a property predicate", func(t *testing.T) {
		out := run(t, `MATCH (u:unit)-[:containedIn]->(l {class: "location"}) RETURN u.key AS key`, nil)
		require.Len(t, out, 1, "the body class is readable as a property, in this same position")
		require.Equal(t, vtxKey(reg, "unit12"), out[0].Values["key"])
	})

	t.Run("a body label field names nothing", func(t *testing.T) {
		out := run(t, `MATCH (u:unit)-[:attachedTo]->(g:gadget) RETURN u.key AS key`, nil)
		require.Empty(t, out, "a root `label` property is an ordinary field, not an address")

		out = run(t, `MATCH (u:unit)-[:attachedTo]->(g:widget) RETURN u.key AS key`, nil)
		require.Len(t, out, 1)
	})

	t.Run("seed position agrees with traversal position", func(t *testing.T) {
		out := run(t, `MATCH (l:location) RETURN l.key AS key`, nil)
		require.Empty(t, out, "the labeled seed scan lists vtx.location. — an empty prefix")

		out = run(t, `MATCH (l:building) RETURN l.key AS key`, nil)
		require.Len(t, out, 1, "the same query in seed position must reach the same vertex")
		require.Equal(t, vtxKey(reg, "tower"), out[0].Values["key"])
	})

	t.Run("a labeled point read binds only a well-formed vertex key", func(t *testing.T) {
		out := run(t, `MATCH (a:canonicalName {key: $k}) RETURN a.key AS key`,
			map[string]any{"k": aspectKey})
		require.Empty(t, out, "a 4-segment aspect key is not a vertex of any type")

		out = run(t, `MATCH (b:building {key: $k}) RETURN b.key AS key`,
			map[string]any{"k": vtxKey(reg, "tower")})
		require.Len(t, out, 1)
	})

	t.Run("an unlabeled pattern still imposes no key-shape constraint", func(t *testing.T) {
		// The whole-bucket scan's KindUnknown admission belongs to the unlabeled
		// arm, where no label is resolved at all — narrowing it would silently
		// shrink every unlabeled pattern's candidate set.
		out := run(t, `MATCH (n {class: "location"}) RETURN n.key AS key`, nil)
		require.Len(t, out, 2, "both key types carrying the class are candidates")
	})
}
