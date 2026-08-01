package wellnessledger

// Rule-engine proof of the wellnessNoShowSettlement convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-ledger / cafe-domain /
// semantic-contracts use.
//
//   - BOOKED: a booked (non-noShow) booking never violates, regardless of
//     fee/account state.
//   - NOSHOW_NO_FEE: a noShow booking with no noShowFeeCents (set before this
//     lens existed) never violates.
//   - NOSHOW_NO_ACCOUNT: noShow, carries a fee, the booker has no
//     wellness-ledger account yet — never violates (no missing_account gap;
//     this lens only converges once an account exists).
//   - NOSHOW_ACCOUNT_NO_CHARGE: noShow, carries a fee, account exists, no
//     wellnesstransaction settles this booking yet — missing_charge true.
//   - NOSHOW_CHARGED: noShow, carries a fee, account exists, a
//     wellnesstransaction settles this booking — converged.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type wlFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newWlFixture(t *testing.T) *wlFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &wlFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *wlFixture) vtx(t *testing.T, name, typ string) string {
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

func (f *wlFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *wlFixture) edge(t *testing.T, name, fromName, toName string) {
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

// projectAt runs the anchored wellnessNoShowSettlement spec for one booking.
func (f *wlFixture) projectAt(t *testing.T, bookingName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(noShowSettlementSpec)
	require.NoError(t, err, "wellnessNoShowSettlement cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    bookingKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkBooking seeds one booking bookedBy a fresh identity, with the given
// status and (optional, nil to omit) noShowFeeCents.
func (f *wlFixture) mkBooking(t *testing.T, name string, status string, feeCents any) {
	t.Helper()
	f.vtx(t, name, "booking")
	statusData := map[string]any{"value": status}
	if feeCents != nil {
		statusData["noShowFeeCents"] = feeCents
	}
	f.aspect(t, name, "status", "bookingStatus", statusData)
	f.vtx(t, name+"_identity", "identity")
	f.edge(t, "bookedBy", name, name+"_identity")
}

func TestWellnessNoShowSettlement_BookedNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkBooking(t, "bookedbkg", "booked", nil)

	v := f.projectAt(t, "bookedbkg")[0].Values
	require.Equal(t, "vtx.booking."+f.ids["bookedbkg"], v["entityKey"])
	require.Equal(t, "booked", v["status"])
	require.Equal(t, false, v["missing_charge"], "not a noShow — never violates")
	require.Equal(t, false, v["violating"])
}

func TestWellnessNoShowSettlement_NoShowNoFee_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkBooking(t, "nofeebkg", "noShow", nil)

	v := f.projectAt(t, "nofeebkg")[0].Values
	require.Equal(t, "noShow", v["status"])
	require.Nil(t, v["feeCents"], "no noShowFeeCents set")
	require.Equal(t, false, v["missing_charge"], "no fee to charge — never violates")
	require.Equal(t, false, v["violating"])
}

func TestWellnessNoShowSettlement_NoShowNoAccount_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkBooking(t, "noacctbkg", "noShow", 2500.0)

	v := f.projectAt(t, "noacctbkg")[0].Values
	require.Nil(t, v["accountKey"], "booker has no wellness-ledger account yet")
	require.Equal(t, false, v["missing_charge"], "no account to charge yet — this gap doesn't gate (no missing_account gap)")
	require.Equal(t, false, v["violating"])
}

func TestWellnessNoShowSettlement_NoShowWithAccountNoCharge_MissingCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkBooking(t, "unchargedbkg", "noShow", 2500.0)
	f.aspect(t, "unchargedbkg_identity", "wellnessLedgerAccount", "wellnessLedgerAccountGuard", map[string]any{"accountKey": "vtx.wellnessaccount.BBFAKEACCTHJKMNPQRST"})
	f.vtx(t, "unchargedbkg_acct", "wellnessaccount")
	f.edge(t, "heldFor", "unchargedbkg_acct", "unchargedbkg_identity")

	v := f.projectAt(t, "unchargedbkg")[0].Values
	require.Equal(t, "vtx.wellnessaccount."+f.ids["unchargedbkg_acct"], v["accountKey"])
	require.Equal(t, 2500.0, v["feeCents"])
	require.Equal(t, true, v["missing_charge"], "no wellnesstransaction settles this booking yet — violating")
	require.Equal(t, true, v["violating"])
}

func TestWellnessNoShowSettlement_NoShowCharged_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkBooking(t, "chargedbkg", "noShow", 2500.0)
	f.vtx(t, "chargedbkg_acct", "wellnessaccount")
	f.edge(t, "heldFor", "chargedbkg_acct", "chargedbkg_identity")
	f.vtx(t, "chargedbkg_tx", "wellnesstransaction")
	f.edge(t, "settles", "chargedbkg_tx", "chargedbkg")

	v := f.projectAt(t, "chargedbkg")[0].Values
	require.Equal(t, false, v["missing_charge"], "a wellnesstransaction settles this booking — converged")
	require.Equal(t, false, v["violating"])
}
