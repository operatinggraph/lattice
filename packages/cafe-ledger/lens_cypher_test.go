package cafeledger

// Rule-engine proof of both business lenses, driven through the `full` engine
// (engine:"full") against an embedded NATS Core/Adjacency KV — the harness
// clinic-ledger / loftspace-ledger / cafe-domain use.
//
// The two lenses make opposite claims about their MATCH shapes, and each claim
// is what these tests hold:
//
//   - ledgerHistory's postedTo/heldFor hops are REQUIRED, so a cafetransaction
//     projects a row only when it is genuinely posted to a live cafeaccount
//     held for a live lease. A dangling transaction must project NOTHING; a
//     lens that relaxed those to OPTIONAL would emit rows with a null
//     accountKey, and a reader summing amountCents per account would drop them.
//   - leaseAccounts anchors on the LEASE, not the account, so a lease that has
//     never been charged still gets a row with a null accountKey — the "has
//     this lease opened a café account yet" question the FE asks before its
//     first charge, which a lens anchored on the account cannot answer.
//
// Unlike loftspace-ledger's, this ledgerHistory has no authorizedBy hop: café
// charges carry no semantic-contracts clause.

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

func lensKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natstest.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cafeled-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cafeled-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-cafeled-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-cafeled-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// lensNanoID derives a deterministic 20-char NanoID over the limited alphabet
// from a test-local name, so a failure names the same key every run.
func lensNanoID(name string) string {
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

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := lensKVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lensNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs one of the package's lens specs. Neither lens is anchored, so
// the engine enumerates its own roots and no actorKey is supplied.
func (f *lensFixture) project(t *testing.T, specName, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "%s cypher must parse on the full engine", specName)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkPostedCharge seeds the whole shape a committed Charge produces: a
// cafetransaction posted to a cafeaccount held for a lease.
func (f *lensFixture) mkPostedCharge(t *testing.T, prefix string, amountCents float64, memo string) {
	t.Helper()
	f.vtx(t, prefix+"_lease", "leaseapp")
	f.vtx(t, prefix+"_acct", "cafeaccount")
	f.vtx(t, prefix+"_tx", "cafetransaction")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_lease")
	f.edge(t, "postedTo", prefix+"_tx", prefix+"_acct")
	f.aspect(t, prefix+"_tx", "entry", "cafetransaction", map[string]any{
		"type":        "debit",
		"amountCents": amountCents,
		"memo":        memo,
		"postedAt":    "2026-07-25T00:00:00Z",
	})
}

func TestCafeLedgerHistory_PostedCharge_ProjectsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "posted", 850, "Flat white")

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.cafetransaction."+f.ids["posted_tx"], v["key"])
	require.Equal(t, "vtx.cafetransaction."+f.ids["posted_tx"], v["transactionKey"])
	require.Equal(t, "vtx.cafeaccount."+f.ids["posted_acct"], v["accountKey"])
	require.Equal(t, "vtx.leaseapp."+f.ids["posted_lease"], v["leaseAppKey"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 850.0, v["amountCents"])
	require.Equal(t, "Flat white", v["memo"])
	require.Equal(t, "2026-07-25T00:00:00Z", v["postedAt"])
}

func TestCafeLedgerHistory_UnpostedTransaction_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// A cafetransaction with its entry aspect but no postedTo edge — the shape
	// a half-written commit would leave behind.
	f.vtx(t, "orphan_tx", "cafetransaction")
	f.aspect(t, "orphan_tx", "entry", "cafetransaction", map[string]any{
		"type": "debit", "amountCents": 500.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "postedTo is a REQUIRED match — an unposted transaction must not project a row with a null accountKey")
}

func TestCafeLedgerHistory_AccountNotHeldForLease_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "loose_acct", "cafeaccount")
	f.vtx(t, "loose_tx", "cafetransaction")
	f.edge(t, "postedTo", "loose_tx", "loose_acct")
	f.aspect(t, "loose_tx", "entry", "cafetransaction", map[string]any{
		"type": "credit", "amountCents": 900.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "heldFor is a REQUIRED match — an account with no lease must not project")
}

func TestCafeLeaseAccounts_LeaseWithNoAccount_ProjectsNullAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "fresh_lease", "leaseapp")

	rows := f.project(t, "cafeLeaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1, "the lens anchors on the LEASE, so a never-charged lease still gets a row")
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["fresh_lease"], v["leaseAppKey"])
	require.Nil(t, v["accountKey"], "no café account opened yet — this is the row the FE reads before a first charge")
}

func TestCafeLeaseAccounts_LeaseWithAccount_ProjectsAccountKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "held_lease", "leaseapp")
	f.vtx(t, "held_acct", "cafeaccount")
	f.edge(t, "heldFor", "held_acct", "held_lease")

	rows := f.project(t, "cafeLeaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["held_lease"], v["leaseAppKey"])
	require.Equal(t, "vtx.cafeaccount."+f.ids["held_acct"], v["accountKey"], "the heldFor hop is walked INBOUND from the lease")
}
