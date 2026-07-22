package servicelocation

// Rule-engine proof of the staffReadGrants producer, driven through the `full`
// engine against an embedded NATS Core/Adjacency KV — the harness front-desk's
// lens test uses (packages/front-desk/lens_cypher_test.go).
//
// The grant is a workplace READ token: it opens every Protected row anchored on
// that building. So the vectors that matter most are the ones proving it is NOT
// granted — one link short, wrong role, wrong location level — plus the
// activation gate that keeps the retraction sound.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func slCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	for _, b := range []string{"adj-svcloc-cypher-test", "core-svcloc-cypher-test"} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b})
		require.NoError(t, err)
	}
	adjKV, err = conn.OpenKV(ctx, "adj-svcloc-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-svcloc-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func slNanoID(name string) string {
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

type slFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newSlFixture(t *testing.T) *slFixture {
	adjKV, coreKV := slCypherKVs(t)
	return &slFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

// vtx seeds a vertex. class is the ENVELOPE class, which differs from the key
// type for a location (every unit/building/property carries class=location) —
// the distinction the lens's :building label depends on.
func (f *slFixture) vtx(t *testing.T, name, typ, class string) string {
	t.Helper()
	id := slNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": class, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *slFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *slFixture) edge(t *testing.T, name, fromName, toName string) {
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

func (f *slFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkRole seeds a role vertex carrying the canonicalName aspect the lens reads.
func (f *slFixture) mkRole(t *testing.T, name, canonical string) {
	t.Helper()
	f.vtx(t, name, "role", "role")
	f.aspect(t, name, "canonicalName", "roleCanonicalName", map[string]any{"value": canonical})
}

func TestStaffReadGrants_GrantsWorkplaceToken(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newSlFixture(t)
	f.vtx(t, "staff", "identity", "identity")
	f.mkRole(t, "foh", "frontOfHouse")
	f.vtx(t, "bldg", "building", "location")
	f.edge(t, "holdsRole", "staff", "foh")
	f.edge(t, "worksAt", "staff", "bldg")

	rows := f.project(t, staffReadGrantsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, f.ids["staff"], v["actor_id"])
	require.Equal(t, f.ids["bldg"], v["anchor_id"], "the anchor is the BUILDING's NanoID, never the actor's or a resident's")
	require.Equal(t, "cap-read.staff", v["grant_source"])
}

// Two workplaces, two tokens — the grant is per building, so retracting one
// workplace must not disturb the other.
func TestStaffReadGrants_OneRowPerWorkplace(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newSlFixture(t)
	f.vtx(t, "staff", "identity", "identity")
	f.mkRole(t, "foh", "frontOfHouse")
	f.vtx(t, "bldgA", "building", "location")
	f.vtx(t, "bldgB", "building", "location")
	f.edge(t, "holdsRole", "staff", "foh")
	f.edge(t, "worksAt", "staff", "bldgA")
	f.edge(t, "worksAt", "staff", "bldgB")

	rows := f.project(t, staffReadGrantsSpec)
	require.Len(t, rows, 2)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Values["anchor_id"].(string)] = true
	}
	require.True(t, got[f.ids["bldgA"]] && got[f.ids["bldgB"]])
}

// The fail-closed half: each link alone grants nothing. These are the vectors
// the retraction rides on — an unwire shrinks the row set to exactly these, and
// the target-diff revokes the difference.
func TestStaffReadGrants_RequiresBothLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("role without worksAt grants nothing", func(t *testing.T) {
		f := newSlFixture(t)
		f.vtx(t, "staff", "identity", "identity")
		f.mkRole(t, "foh", "frontOfHouse")
		f.vtx(t, "bldg", "building", "location")
		f.edge(t, "holdsRole", "staff", "foh")

		require.Empty(t, f.project(t, staffReadGrantsSpec))
	})

	t.Run("worksAt without the role grants nothing", func(t *testing.T) {
		f := newSlFixture(t)
		f.vtx(t, "staff", "identity", "identity")
		f.vtx(t, "bldg", "building", "location")
		f.edge(t, "worksAt", "staff", "bldg")

		require.Empty(t, f.project(t, staffReadGrantsSpec))
	})
}

// A different role at the same workplace is not front desk. Without the
// canonicalName predicate every role-holder who works anywhere would receive a
// workplace token.
func TestStaffReadGrants_OtherRoleGrantsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newSlFixture(t)
	f.vtx(t, "staff", "identity", "identity")
	f.mkRole(t, "consumer", "consumer")
	f.vtx(t, "bldg", "building", "location")
	f.edge(t, "holdsRole", "staff", "consumer")
	f.edge(t, "worksAt", "staff", "bldg")

	require.Empty(t, f.project(t, staffReadGrantsSpec))
}

// worksAt accepts any class=location target, so the :building label is what
// holds the token at building granularity. A unit workplace grants nothing —
// were it to grant, the token would be a unit NanoID, and any lens anchoring
// rows on a unit would silently open them.
func TestStaffReadGrants_NonBuildingWorkplaceGrantsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newSlFixture(t)
	f.vtx(t, "staff", "identity", "identity")
	f.mkRole(t, "foh", "frontOfHouse")
	f.vtx(t, "unit", "unit", "location")
	f.edge(t, "holdsRole", "staff", "foh")
	f.edge(t, "worksAt", "staff", "unit")

	require.Empty(t, f.project(t, staffReadGrantsSpec))
}

// The activation gate Refractor applies to any DiffRetraction lens. Asserting it
// here means a later edit reintroducing an $actorKey seed fails in this package,
// rather than at activation on a live stack — where the lens would go dark and
// every staff grant would silently stop being maintained.
func TestStaffReadGrants_PassesUnanchoredActivationGate(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(staffReadGrantsSpec)
	require.NoError(t, err)
	compiled, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	require.NoError(t, compiled.ValidateUnanchoredForDiffRetraction(),
		"the target-diff is exact only for a whole-scan query")
}
