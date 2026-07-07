package cafedomain

// Rule-engine proof of the cafeTabSettlement convergence lens, driven through
// the `full` engine (engine:"full") against an embedded NATS Core/Adjacency
// KV — the same harness bespoke-contracts / lease-signing / clinic-reminders
// use.
//
//   - OPEN: a tab still open never violates either gap, regardless of total.
//   - SETTLED_ZERO: a settled tab with totalCents=0 never violates either gap
//     (no house-tab posting is needed for a zero-amount visit).
//   - SETTLED_NO_ACCOUNT: settled, owes money, lease has no café-ledger
//     account yet — missing_account true, missing_charge false.
//   - SETTLED_ACCOUNT_NO_CHARGE: settled, owes money, account exists, no
//     cafetransaction settles this tab yet — missing_charge true,
//     missing_account false.
//   - SETTLED_CHARGED: settled, owes money, account exists, a cafetransaction
//     settles this tab — both gaps false, converged.

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

func cdCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
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
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cafedom-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cafedom-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-cafedom-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-cafedom-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func cdNanoID(name string) string {
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

type cdFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newCdFixture(t *testing.T) *cdFixture {
	adjKV, coreKV := cdCypherKVs(t)
	return &cdFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *cdFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := cdNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *cdFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *cdFixture) edge(t *testing.T, name, fromName, toName string) {
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

// projectAt runs the anchored cafeTabSettlement spec for one tab.
func (f *cdFixture) projectAt(t *testing.T, tabName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(tabSettlementSpec)
	require.NoError(t, err, "cafeTabSettlement cypher must parse on the full engine")
	tabKey := "vtx.tab." + f.ids[tabName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    tabKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkTab seeds one tab openFor a fresh leaseapp, with the given status.
func (f *cdFixture) mkTab(t *testing.T, name string, status string, totalCents float64) {
	t.Helper()
	f.vtx(t, name, "tab")
	f.aspect(t, name, "status", "tabStatus", map[string]any{"value": status, "totalCents": totalCents, "openedAt": "2026-07-07T12:00:00Z"})
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "openFor", name, name+"_lease")
}

func TestCafeTabSettlement_OpenNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "opentab", "open", 850)

	v := f.projectAt(t, "opentab")[0].Values
	require.Equal(t, "vtx.tab."+f.ids["opentab"], v["entityKey"])
	require.Equal(t, false, v["missing_account"], "still open — never violates")
	require.Equal(t, false, v["missing_charge"], "still open — never violates")
	require.Equal(t, false, v["violating"])
}

func TestCafeTabSettlement_SettledZeroTotal_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "zerotab", "settled", 0)

	v := f.projectAt(t, "zerotab")[0].Values
	require.Equal(t, false, v["missing_account"], "zero total needs no posting")
	require.Equal(t, false, v["missing_charge"], "zero total needs no posting")
	require.Equal(t, false, v["violating"])
}

func TestCafeTabSettlement_SettledNoAccount_MissingAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "noaccttab", "settled", 1200)

	v := f.projectAt(t, "noaccttab")[0].Values
	require.Nil(t, v["accountKey"], "lease has no café-ledger account yet")
	require.Equal(t, true, v["missing_account"], "settled + owes money + no account — violating")
	require.Equal(t, false, v["missing_charge"], "no account to charge yet — this gap doesn't gate")
	require.Equal(t, true, v["violating"])
}

func TestCafeTabSettlement_SettledWithAccountNoCharge_MissingCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "unchargedtab", "settled", 1200)
	f.aspect(t, "unchargedtab_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})

	v := f.projectAt(t, "unchargedtab")[0].Values
	require.Equal(t, "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST", v["accountKey"])
	require.Equal(t, false, v["missing_account"], "account already exists")
	require.Equal(t, true, v["missing_charge"], "no cafetransaction settles this tab yet — violating")
	require.Equal(t, true, v["violating"])
}

func TestCafeTabSettlement_SettledAndCharged_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "chargedtab", "settled", 1200)
	f.aspect(t, "chargedtab_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.vtx(t, "chargedtab_tx", "cafetransaction")
	f.edge(t, "settles", "chargedtab_tx", "chargedtab")

	v := f.projectAt(t, "chargedtab")[0].Values
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"], "a cafetransaction settles this tab — converged")
	require.Equal(t, false, v["violating"])
}
