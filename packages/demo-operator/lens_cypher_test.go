package demooperator

// Rule-engine proof of demoOperatorReadGrants, driven through the `full`
// engine — the one activation selects via engine:"full" — against an embedded
// NATS Core/Adjacency KV. Same harness shape as edge-manifest / clinic-domain's
// lens cypher tests.
//
// This is a GrantTable lens: every row it emits is a read-side authorization,
// carrying anchor_id '*' — a wildcard over the Protected plane. It backs the
// PUBLIC read-only Loupe demo, so its blast radius is a stranger's browser
// rather than an operator's. The package's other tests check only structure
// (the spec literal, the adapter kind, parse success), and neither of the two
// things that actually matter here is visible that way:
//
//   - the `role.canonicalName.data.value = 'demoOperator'` filter is the
//     ONLY thing standing between the demo boundary and handing that wildcard
//     to every role-holder in the graph. A filter that silently matched
//     nothing fails safe (the demo's reads go RLS-empty); a filter that
//     matched too much fails open and looks perfectly healthy.
//   - `nanoIdFromKey(identity.key)` must yield the BARE NanoID, because the
//     Postgres RLS predicate compares actor_id against `lattice.actor_id`,
//     which the adapter sets to a bare NanoID. A full `vtx.identity.<id>` key
//     in that column denies every row while projecting a full-looking table.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func doCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natstest.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-demooperator-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-demooperator-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-demooperator-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-demooperator-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// doNanoID returns a deterministic 20-char Contract #1 NanoID from a logical
// name (the edge-manifest / wellness-domain helper's derivation).
func doNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type doFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newDoFixture(t *testing.T) *doFixture {
	adjKV, coreKV := doCypherKVs(t)
	return &doFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *doFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := doNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *doFixture) key(name string) string {
	return "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
}

func (f *doFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

func (f *doFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// role creates a role vertex plus the canonicalName aspect the lens filters on.
func (f *doFixture) role(t *testing.T, name, canonical string) {
	t.Helper()
	f.vtx(t, name, "role")
	f.aspect(t, name, "canonicalName", "canonicalName", map[string]any{"value": canonical})
}

func (f *doFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "demoOperatorReadGrants must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// doGrantWorld seeds one demoOperator holder alongside two identities that must
// NOT receive the wildcard: a consoleOperator — this package's near-identical
// sibling, and the one role a copy-paste between the two specs would wrongly
// admit — and an identity holding no role at all.
func doGrantWorld(t *testing.T) *doFixture {
	f := newDoFixture(t)
	f.role(t, "demoRole", "demoOperator")
	f.role(t, "consoleRole", "consoleOperator")

	f.vtx(t, "demo", "identity")
	f.vtx(t, "console", "identity")
	f.vtx(t, "roleless", "identity")

	f.edge(t, "holdsRole", "demo", "demoRole")
	f.edge(t, "holdsRole", "console", "consoleRole")
	return f
}

func TestDemoOperatorReadGrants_GrantsOnlyTheDemoRoleHolder(t *testing.T) {
	f := doGrantWorld(t)

	rows := f.project(t, demoOperatorReadGrantsSpec)
	require.Len(t, rows, 1,
		"only the demoOperator holder may receive the read-side wildcard — a consoleOperator reads through its OWN package's grant, never this one. got %v", rows)

	row := rows[0].Values
	require.Equal(t, doNanoID("demo"), row["actor_id"],
		"actor_id must be the BARE NanoID — Postgres RLS compares it against lattice.actor_id, which is bare; a full vtx key denies every row while the table still looks populated")
	require.Equal(t, "*", row["anchor_id"])
	require.Equal(t, "cap-read.demoOperator", row["grant_source"],
		"the grant_source must stay disjoint from cap-read.consoleOperator and the kernel's cap-read.root so the three producers never overwrite each other")
}

func TestDemoOperatorReadGrants_ProjectsNothingWithoutTheRole(t *testing.T) {
	f := newDoFixture(t)
	// Every ingredient except the role itself: identities exist, one even holds
	// a role — just not this one.
	f.role(t, "otherRole", "backOfHouse")
	f.vtx(t, "backDesk", "identity")
	f.vtx(t, "roleless", "identity")
	f.edge(t, "holdsRole", "backDesk", "otherRole")

	rows := f.project(t, demoOperatorReadGrantsSpec)
	require.Empty(t, rows,
		"absence of the demoOperator role must mean absence of the grant — the read plane is default-deny. got %v", rows)
}

func TestDemoOperatorReadGrants_ActivatesWithItsDefaultKeyColumns(t *testing.T) {
	eng := full.New()
	compiled, err := eng.Parse(demoOperatorReadGrantsSpec)
	require.NoError(t, err)
	cr, ok := compiled.(*full.CompiledRule)
	require.True(t, ok)
	cr.KeyColumns = []string{"actor_id", "anchor_id", "grant_source"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the grant row's own columns must be a valid key set — the activation-time gate a GrantTable lens dies on")
}
