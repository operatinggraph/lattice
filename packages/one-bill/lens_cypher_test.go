package onebill

// Rule-engine proof of the two one-bill lenses, driven through the `full`
// engine (engine:"full") against an embedded NATS Core/Adjacency KV — the
// same harness cafe-domain's cafeTabSettlement lens test uses
// (packages/cafe-domain/lens_cypher_test.go). Unlike that lens (anchored on
// one actor via $actorKey), oneBillRentEntries/oneBillCafeEntries are
// unanchored whole-graph scans — the same shape as loftspace-ledger's own
// production ledgerHistorySpec — so no Parameters are needed.
//
//   - TestOneBill_RentEntries_ProjectsTaggedRow: a rent transaction posted to
//     a loftspace-ledger account/lease projects one row via
//     oneBillRentEntries, tagged source:"rent".
//   - TestOneBill_CafeEntries_ProjectsTaggedRow: a café transaction posted to
//     a cafe-ledger account/lease projects one row via oneBillCafeEntries,
//     tagged source:"cafe".
//   - TestOneBill_KeysDoNotCollide: both lenses run over a graph holding
//     BOTH a rent and a café transaction for the same lease — each lens
//     projects only its own row, and the two keys are disjoint (vtx.
//     transaction.* vs vtx.cafetransaction.*), so sharing one bucket is safe.

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

func obCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-onebill-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-onebill-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-onebill-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-onebill-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func obNanoID(name string) string {
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

type obFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newObFixture(t *testing.T) *obFixture {
	adjKV, coreKV := obCypherKVs(t)
	return &obFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *obFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := obNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *obFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *obFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs the given spec unanchored (no Parameters — both lenses are
// whole-graph scans, same as loftspace-ledger's production ledgerHistorySpec).
func (f *obFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkRentTx seeds a loftspace-ledger transaction posted to an account held
// for a lease: t -[:postedTo]-> a -[:heldFor]-> l.
func (f *obFixture) mkRentTx(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "transaction")
	f.aspect(t, name, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "memo": "June rent", "postedAt": "2026-06-01T00:00:00Z",
	})
	f.vtx(t, name+"_acct", "account")
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "postedTo", name, name+"_acct")
	f.edge(t, "heldFor", name+"_acct", name+"_lease")
}

// mkCafeTx seeds a cafe-ledger transaction posted to a café account held for
// a lease: t -[:postedTo]-> a -[:heldFor]-> l.
func (f *obFixture) mkCafeTx(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "cafetransaction")
	f.aspect(t, name, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "memo": "Latte", "postedAt": "2026-06-02T00:00:00Z",
	})
	f.vtx(t, name+"_acct", "cafeaccount")
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "postedTo", name, name+"_acct")
	f.edge(t, "heldFor", name+"_acct", name+"_lease")
}

func TestOneBill_RentEntries_ProjectsTaggedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObFixture(t)
	f.mkRentTx(t, "renttx", 150000)

	rows := f.project(t, rentEntriesSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.transaction."+f.ids["renttx"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["renttx_lease"], v["leaseAppKey"])
	require.Equal(t, "rent", v["source"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 150000.0, v["amountCents"])
}

func TestOneBill_CafeEntries_ProjectsTaggedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObFixture(t)
	f.mkCafeTx(t, "cafetx", 850)

	rows := f.project(t, cafeEntriesSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.cafetransaction."+f.ids["cafetx"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["cafetx_lease"], v["leaseAppKey"])
	require.Equal(t, "cafe", v["source"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 850.0, v["amountCents"])
}

// TestOneBill_KeysDoNotCollide seeds BOTH a rent and a café transaction for
// the SAME lease and runs both lenses over the shared graph: each lens
// projects only its own vertex-class row, and the two projected keys are
// disjoint — proving the "share one bucket, no namespacing needed" claim in
// lenses.go actually holds against a real mixed graph, not just in theory.
func TestOneBill_KeysDoNotCollide(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObFixture(t)
	f.vtx(t, "sharedlease", "leaseapp")

	f.vtx(t, "renttx", "transaction")
	f.aspect(t, "renttx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 150000.0, "postedAt": "2026-06-01T00:00:00Z",
	})
	f.vtx(t, "renttx_acct", "account")
	f.edge(t, "postedTo", "renttx", "renttx_acct")
	f.edge(t, "heldFor", "renttx_acct", "sharedlease")

	f.vtx(t, "cafetx", "cafetransaction")
	f.aspect(t, "cafetx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 850.0, "postedAt": "2026-06-02T00:00:00Z",
	})
	f.vtx(t, "cafetx_acct", "cafeaccount")
	f.edge(t, "postedTo", "cafetx", "cafetx_acct")
	f.edge(t, "heldFor", "cafetx_acct", "sharedlease")

	rentRows := f.project(t, rentEntriesSpec)
	cafeRows := f.project(t, cafeEntriesSpec)
	require.Len(t, rentRows, 1, "rent lens must not pick up the café transaction")
	require.Len(t, cafeRows, 1, "café lens must not pick up the rent transaction")

	rentKey, _ := rentRows[0].Values["key"].(string)
	cafeKey, _ := cafeRows[0].Values["key"].(string)
	require.NotEqual(t, rentKey, cafeKey, "the two lenses' output keys must be disjoint to share one bucket safely")
	require.Equal(t, "vtx.leaseapp."+f.ids["sharedlease"], rentRows[0].Values["leaseAppKey"])
	require.Equal(t, "vtx.leaseapp."+f.ids["sharedlease"], cafeRows[0].Values["leaseAppKey"])
}
