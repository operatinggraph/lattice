package bespokecontracts

// Rule-engine proof of the clauseSatisfaction convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness lease-signing / clinic-reminders /
// objects-base use.
//
//   - UNCHARGED: no transaction authorizedBy the clause — violating,
//     missing_charge true.
//   - CHARGED: a transaction authorizedBy the clause exists — not violating,
//     missing_charge false (converged; the row lingers non-violating per
//     the design's R3 v1 constraint, never deleted).
//   - one row per anchor even with the chargesTo account linked.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func bcCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-bespcon-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-bespcon-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-bespcon-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-bespcon-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func bcNanoID(name string) string {
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

type bcFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newBcFixture(t *testing.T) *bcFixture {
	adjKV, coreKV := bcCypherKVs(t)
	return &bcFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *bcFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := bcNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *bcFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *bcFixture) edge(t *testing.T, name, fromName, toName string) {
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

// projectAt runs the anchored clauseSatisfaction spec for one clause.
func (f *bcFixture) projectAt(t *testing.T, clauseName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(clauseSatisfactionSpec)
	require.NoError(t, err, "clauseSatisfaction cypher must parse on the full engine")
	clauseKey := "vtx.clause." + f.ids[clauseName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    clauseKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkClause seeds one clause with .terms{amountCents} + .status{active}, linked
// to a charged account.
func (f *bcFixture) mkClause(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "clause")
	f.aspect(t, name, "terms", "clauseTerms", map[string]any{"kind": "computational", "amountCents": amountCents, "period": "oneTime"})
	f.aspect(t, name, "status", "clauseStatus", map[string]any{"state": "active"})
	f.vtx(t, name+"_acct", "account")
	f.edge(t, "chargesTo", name, name+"_acct")
}

// TestClauseSatisfaction_Uncharged — no authorizedBy transaction yet: violating,
// missing_charge true, amountCents/accountKey project through.
func TestClauseSatisfaction_Uncharged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkClause(t, "clause1", 4500)

	rows := f.projectAt(t, "clause1")
	require.Len(t, rows, 1, "exactly one row per clause anchor")
	v := rows[0].Values
	require.Equal(t, "vtx.clause."+f.ids["clause1"], v["entityKey"])
	require.Equal(t, "vtx.clause."+f.ids["clause1"], v["clauseKey"])
	require.Equal(t, "vtx.account."+f.ids["clause1_acct"], v["accountKey"])
	require.Equal(t, 4500.0, v["amountCents"])
	require.Equal(t, true, v["missing_charge"], "no authorizedBy transaction yet — violating")
	require.Equal(t, true, v["violating"])
}

// TestClauseSatisfaction_Charged — an authorizedBy transaction exists: not
// violating, missing_charge false. Converged.
func TestClauseSatisfaction_Charged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkClause(t, "clause2", 4500)
	f.vtx(t, "tx1", "transaction")
	f.edge(t, "authorizedBy", "tx1", "clause2")

	v := f.projectAt(t, "clause2")[0].Values
	require.Equal(t, false, v["missing_charge"], "an authorizedBy transaction exists — converged")
	require.Equal(t, false, v["violating"])
}

// TestClauseSatisfaction_TwoClausesSameAccount — one row per clause anchor
// even when two clauses charge the same account (no fan-out cross-talk).
func TestClauseSatisfaction_TwoClausesSameAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "acct", "account")

	f.vtx(t, "clauseA", "clause")
	f.aspect(t, "clauseA", "terms", "clauseTerms", map[string]any{"kind": "computational", "amountCents": 1000.0, "period": "oneTime"})
	f.edge(t, "chargesTo", "clauseA", "acct")

	f.vtx(t, "clauseB", "clause")
	f.aspect(t, "clauseB", "terms", "clauseTerms", map[string]any{"kind": "computational", "amountCents": 2000.0, "period": "oneTime"})
	f.edge(t, "chargesTo", "clauseB", "acct")
	f.vtx(t, "txB", "transaction")
	f.edge(t, "authorizedBy", "txB", "clauseB")

	va := f.projectAt(t, "clauseA")[0].Values
	require.Equal(t, true, va["missing_charge"], "clauseA has no charge of its own")
	require.Equal(t, 1000.0, va["amountCents"])

	vb := f.projectAt(t, "clauseB")[0].Values
	require.Equal(t, false, vb["missing_charge"], "clauseB's own charge converges it")
	require.Equal(t, 2000.0, vb["amountCents"])
}
