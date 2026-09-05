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
	"sort"
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

// The tail-headed spelling of the two cross-package specs: the same edge, in
// the same direction, written with the leaseapp at the head of its own MATCH so
// nothing above it binds that variable.
//
// It is a TEST VECTOR, not a fossil. The engine reaches an unbound head by
// scanning the head label's whole corpus rather than by stepping an adjacency
// hop, which leaves ScanRootHopIndex ungrounded and the lens without a
// neighbour-retraction transport — so the rows the two spellings project must
// be identical, and the pattern graphs must not be. Both halves are asserted
// below; the equivalence half is what makes the shipped spelling a free choice
// and the index half is what makes it the right one.
const (
	tailHeadedClinicEntriesSpec = `MATCH (t:clinictransaction)
MATCH (t)-[:postedTo]->(a:clinicaccount)
MATCH (a)-[:heldFor]->(pt:patient)
MATCH (pt)-[:identifiedBy]->(id:identity)
MATCH (l:leaseapp)-[:applicationFor]->(id)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  'clinic' AS source`

	tailHeadedWellnessEntriesSpec = `MATCH (t:wellnesstransaction)
MATCH (t)-[:postedTo]->(a:wellnessaccount)
MATCH (a)-[:heldFor]->(id:identity)
MATCH (l:leaseapp)-[:applicationFor]->(id)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  'wellness' AS source`
)

// TestOneBill_ReversedPatternProjectsSameRows proves the shipped clinic and
// wellness specs project exactly what their tail-headed spelling does, and that
// only the shipped one has a grounded scan-root pattern graph.
//
// The row comparison is on the marshalled projection, key set included, so a
// difference in a single column's value or in which columns exist at all fails
// here rather than being lost in a field-by-field walk that forgets one. The
// ROW SET is compared, not one row: the way these two spellings can differ is in
// which rows they select, and a fixture with one of everything makes every
// spelling of the join agree by construction (see confound).
func TestOneBill_ReversedPatternProjectsSameRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name       string
		shipped    string
		tailHeaded string
		seed       func(f *obFixture, t *testing.T)
		// confound seeds the graph the two spellings could disagree on. A
		// fixture holding one transaction, one identity and one leaseapp makes
		// every spelling of the join look alike: the row is unique, so a
		// pattern that bound `l` by scanning every leaseapp and a pattern that
		// stepped to it from the identity return the same thing. These are the
		// shapes where they need not.
		confound func(f *obFixture, t *testing.T)
	}{
		{
			name: "clinicEntries", shipped: clinicEntriesSpec, tailHeaded: tailHeadedClinicEntriesSpec,
			seed: func(f *obFixture, t *testing.T) { f.mkClinicTx(t, "clinictx", 4500) },
			confound: func(f *obFixture, t *testing.T) {
				// A SECOND leaseapp for the same identity: the `l` binding is
				// no longer a function of the transaction, so a spelling that
				// bound it differently would project a different number of rows
				// (or the wrong one of the two) rather than a differing column.
				f.vtx(t, "clinictx_lease2", "leaseapp")
				f.edge(t, "applicationFor", "clinictx_lease2", "clinictx_identity")
				// An identity with a leaseapp and NO transaction: the
				// head-bound spelling scans every leaseapp, so a spelling that
				// leaked one would project a row with null transaction columns.
				f.vtx(t, "spare_identity", "identity")
				f.vtx(t, "spare_lease", "leaseapp")
				f.edge(t, "applicationFor", "spare_lease", "spare_identity")
				// A transaction whose patient has no identity at all: it must
				// drop out of BOTH spellings, and a spelling that kept it would
				// project a null leaseAppKey the target's key column rejects.
				f.vtx(t, "orphan_tx", "clinictransaction")
				f.aspect(t, "orphan_tx", "entry", "transactionEntry", map[string]any{
					"type": "debit", "amountCents": 900.0, "memo": "Unlinked", "postedAt": "2026-06-05T00:00:00Z",
				})
				f.vtx(t, "orphan_acct", "clinicaccount")
				f.vtx(t, "orphan_patient", "patient")
				f.edge(t, "postedTo", "orphan_tx", "orphan_acct")
				f.edge(t, "heldFor", "orphan_acct", "orphan_patient")
			},
		},
		{
			name: "wellnessEntries", shipped: wellnessEntriesSpec, tailHeaded: tailHeadedWellnessEntriesSpec,
			seed: func(f *obFixture, t *testing.T) { f.mkWellnessTx(t, "wellnesstx", 3000) },
			confound: func(f *obFixture, t *testing.T) {
				f.vtx(t, "wellnesstx_lease2", "leaseapp")
				f.edge(t, "applicationFor", "wellnesstx_lease2", "wellnesstx_identity")
				f.vtx(t, "spare_identity", "identity")
				f.vtx(t, "spare_lease", "leaseapp")
				f.edge(t, "applicationFor", "spare_lease", "spare_identity")
				// A transaction posted to an account held for nobody — the
				// wellness chain's own "no identity" shape.
				f.vtx(t, "orphan_tx", "wellnesstransaction")
				f.aspect(t, "orphan_tx", "entry", "transactionEntry", map[string]any{
					"type": "debit", "amountCents": 700.0, "memo": "Unlinked", "postedAt": "2026-06-06T00:00:00Z",
				})
				f.vtx(t, "orphan_acct", "wellnessaccount")
				f.edge(t, "postedTo", "orphan_tx", "orphan_acct")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newObFixture(t)
			tc.seed(f, t)
			tc.confound(f, t)

			shippedRows := f.project(t, tc.shipped)
			tailRows := f.project(t, tc.tailHeaded)
			require.Len(t, shippedRows, 2,
				"the confounding graph must actually multiply the rows — one transaction against two leaseapps for its "+
					"identity — or the comparison below is back to the single-row fixture that cannot tell the spellings apart")
			require.Equal(t, len(shippedRows), len(tailRows),
				"the two spellings must select the same rows — a difference in COUNT is the failure mode a "+
					"single-row fixture cannot see, and it is the one a head-bound scan produces")

			require.JSONEq(t, rowSetJSON(t, tailRows), rowSetJSON(t, shippedRows),
				"the two spellings walk the same edge in the same direction and must project the same rows")

			eng := full.New()
			shippedCR, err := eng.Parse(tc.shipped)
			require.NoError(t, err)
			tailCR, err := eng.Parse(tc.tailHeaded)
			require.NoError(t, err)
			require.True(t, shippedCR.(*full.CompiledRule).ScanRootHopIndex().Complete,
				"the shipped spec's scan-root pattern graph must be complete — without it no neighbour event can be narrowed "+
					"to the anchors it affects, and the lens has no retraction transport")
			require.False(t, tailCR.(*full.CompiledRule).ScanRootHopIndex().Complete,
				"the tail-headed spelling must leave the graph ungrounded, or this test is comparing two spellings that "+
					"were never different and proves nothing about the shipped one")
		})
	}
}

// rowSetJSON renders a projection result set as one order-independent document,
// so two spellings are compared on the rows they SELECT rather than on the order
// a scan happened to visit them in. The rows are sorted by their own marshalled
// form, which is total and needs no key to be nominated.
func rowSetJSON(t *testing.T, rows []ruleengine.ProjectionResult) string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		raw, err := json.Marshal(r.Values)
		require.NoError(t, err)
		out = append(out, string(raw))
	}
	sort.Strings(out)
	joined, err := json.Marshal(out)
	require.NoError(t, err)
	return string(joined)
}
