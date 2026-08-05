package onebill

// Rule-engine proof of the one-bill lenses, driven through the `full`
// engine (engine:"full") against an embedded NATS Core/Adjacency KV — the
// same harness cafe-domain's cafeTabSettlement lens test uses
// (packages/cafe-domain/lens_cypher_test.go). Unlike that lens (anchored on
// one actor via $actorKey), all four one-bill lenses are unanchored
// whole-graph scans — the same shape as loftspace-ledger's own production
// ledgerHistorySpec — so no Parameters are needed.
//
//   - TestOneBill_RentEntries_ProjectsTaggedRow: a rent transaction posted to
//     a loftspace-ledger account/lease projects one row via
//     oneBillRentEntries, tagged source:"rent".
//   - TestOneBill_CafeEntries_ProjectsTaggedRow: a café transaction posted to
//     a cafe-ledger account/lease projects one row via oneBillCafeEntries,
//     tagged source:"cafe".
//   - TestOneBill_ClinicEntries_ProjectsTaggedRow: a clinic transaction posted
//     to a clinic-ledger account held for a patient who is identifiedBy an
//     identity that is itself a lease's applicant projects one row via
//     oneBillClinicEntries, tagged source:"clinic".
//   - TestOneBill_WellnessEntries_ProjectsTaggedRow: a wellness transaction
//     posted to a wellness-ledger account held directly for a lease's
//     applicant identity projects one row via oneBillWellnessEntries, tagged
//     source:"wellness".
//   - TestOneBill_KeysDoNotCollide: all four lenses run over a graph holding
//     a rent, café, clinic AND wellness transaction for the same lease — each
//     lens projects only its own row, and the four keys are disjoint (vtx.
//     transaction.* / vtx.cafetransaction.* / vtx.clinictransaction.* /
//     vtx.wellnesstransaction.*), so sharing one bucket is safe.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type obFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newObFixture(t *testing.T) *obFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &obFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *obFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
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

// mkClinicTx seeds a clinic-ledger transaction posted to an account held for
// a patient who is identifiedBy an identity that is itself applicationFor a
// lease: t -[:postedTo]-> a -[:heldFor]-> pt -[:identifiedBy]-> id
// <-[:applicationFor]- l.
func (f *obFixture) mkClinicTx(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "clinictransaction")
	f.aspect(t, name, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "memo": "Visit copay", "postedAt": "2026-06-03T00:00:00Z",
	})
	f.vtx(t, name+"_acct", "clinicaccount")
	f.vtx(t, name+"_patient", "patient")
	f.vtx(t, name+"_identity", "identity")
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "postedTo", name, name+"_acct")
	f.edge(t, "heldFor", name+"_acct", name+"_patient")
	f.edge(t, "identifiedBy", name+"_patient", name+"_identity")
	f.edge(t, "applicationFor", name+"_lease", name+"_identity")
}

// mkWellnessTx seeds a wellness-ledger transaction posted to an account held
// directly for an identity that is applicationFor a lease:
// t -[:postedTo]-> a -[:heldFor]-> id <-[:applicationFor]- l.
func (f *obFixture) mkWellnessTx(t *testing.T, name string, amountCents float64) {
	t.Helper()
	f.vtx(t, name, "wellnesstransaction")
	f.aspect(t, name, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "memo": "Yoga class", "postedAt": "2026-06-04T00:00:00Z",
	})
	f.vtx(t, name+"_acct", "wellnessaccount")
	f.vtx(t, name+"_identity", "identity")
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "postedTo", name, name+"_acct")
	f.edge(t, "heldFor", name+"_acct", name+"_identity")
	f.edge(t, "applicationFor", name+"_lease", name+"_identity")
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

func TestOneBill_ClinicEntries_ProjectsTaggedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObFixture(t)
	f.mkClinicTx(t, "clinictx", 4500)

	rows := f.project(t, clinicEntriesSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.clinictransaction."+f.ids["clinictx"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["clinictx_lease"], v["leaseAppKey"])
	require.Equal(t, "clinic", v["source"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 4500.0, v["amountCents"])
}

func TestOneBill_WellnessEntries_ProjectsTaggedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newObFixture(t)
	f.mkWellnessTx(t, "wellnesstx", 3000)

	rows := f.project(t, wellnessEntriesSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.wellnesstransaction."+f.ids["wellnesstx"], v["key"])
	require.Equal(t, "vtx.leaseapp."+f.ids["wellnesstx_lease"], v["leaseAppKey"])
	require.Equal(t, "wellness", v["source"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 3000.0, v["amountCents"])
}

// TestOneBill_KeysDoNotCollide seeds a rent, café, clinic AND wellness
// transaction for the SAME lease and runs all four lenses over the shared
// graph: each lens projects only its own vertex-class row, and all four
// projected keys are disjoint — proving the "share one bucket, no
// namespacing needed" claim in lenses.go actually holds against a real mixed
// graph, not just in theory.
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

	f.vtx(t, "sharedidentity", "identity")
	f.edge(t, "applicationFor", "sharedlease", "sharedidentity")

	f.vtx(t, "clinictx", "clinictransaction")
	f.aspect(t, "clinictx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 4500.0, "postedAt": "2026-06-03T00:00:00Z",
	})
	f.vtx(t, "clinictx_acct", "clinicaccount")
	f.vtx(t, "clinictx_patient", "patient")
	f.edge(t, "postedTo", "clinictx", "clinictx_acct")
	f.edge(t, "heldFor", "clinictx_acct", "clinictx_patient")
	f.edge(t, "identifiedBy", "clinictx_patient", "sharedidentity")

	f.vtx(t, "wellnesstx", "wellnesstransaction")
	f.aspect(t, "wellnesstx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 3000.0, "postedAt": "2026-06-04T00:00:00Z",
	})
	f.vtx(t, "wellnesstx_acct", "wellnessaccount")
	f.edge(t, "postedTo", "wellnesstx", "wellnesstx_acct")
	f.edge(t, "heldFor", "wellnesstx_acct", "sharedidentity")

	rentRows := f.project(t, rentEntriesSpec)
	cafeRows := f.project(t, cafeEntriesSpec)
	clinicRows := f.project(t, clinicEntriesSpec)
	wellnessRows := f.project(t, wellnessEntriesSpec)
	require.Len(t, rentRows, 1, "rent lens must not pick up the other transactions")
	require.Len(t, cafeRows, 1, "café lens must not pick up the other transactions")
	require.Len(t, clinicRows, 1, "clinic lens must not pick up the other transactions")
	require.Len(t, wellnessRows, 1, "wellness lens must not pick up the other transactions")

	keys := map[string]bool{}
	for _, rows := range [][]ruleengine.ProjectionResult{rentRows, cafeRows, clinicRows, wellnessRows} {
		k, _ := rows[0].Values["key"].(string)
		require.False(t, keys[k], "the four lenses' output keys must be disjoint to share one bucket safely")
		keys[k] = true
		require.Equal(t, "vtx.leaseapp."+f.ids["sharedlease"], rows[0].Values["leaseAppKey"])
	}
}
