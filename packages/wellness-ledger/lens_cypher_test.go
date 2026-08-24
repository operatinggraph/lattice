package wellnessledger

// Rule-engine proof of the wellnessNoShowSettlement and
// wellnessClassPriceSettlement convergence lenses, driven through the `full`
// engine (engine:"full") against an embedded NATS Core/Adjacency KV — the
// same harness clinic-ledger / cafe-domain / semantic-contracts use.
//
// wellnessNoShowSettlement (noShowSettlementSpec):
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
//
// wellnessClassPriceSettlement (classPriceSettlementSpec) — the OTHER
// wellness billing gap, UNCONDITIONAL on booking .status:
//   - NO_PRICE: a booking whose session carries no priceCents (or 0) never
//     violates, regardless of attendance/account state.
//   - PRICED_NO_ACCOUNT: session priced, the booker has no wellness-ledger
//     account yet — never violates (no missing_account gap, mirrors the
//     no-show lens's identical rationale).
//   - PRICED_ACCOUNT_NO_CHARGE: session priced, account exists, no
//     wellnesstransaction settlesClassPrice this booking yet —
//     missing_price_charge true.
//   - PRICED_CHARGED: session priced, account exists, a wellnesstransaction
//     settlesClassPrice this booking — converged.
//   - RESIDENT_RATE: a resident-rate booking on a session with
//     residentPriceCents charges residentPriceCents, not priceCents; absent
//     residentPriceCents it falls back to priceCents like a standard booker;
//     a standard-rate booking is unaffected by residentPriceCents either way.

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

// projectClassPriceAt runs the anchored wellnessClassPriceSettlement spec for
// one booking — the classPriceSettlementSpec counterpart to projectAt.
func (f *wlFixture) projectClassPriceAt(t *testing.T, bookingName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(classPriceSettlementSpec)
	require.NoError(t, err, "wellnessClassPriceSettlement cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    bookingKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkPricedBooking seeds one booking forSession a session carrying the given
// (optional, nil to omit) priceCents, and bookedBy a fresh identity —
// classPriceSettlementSpec's counterpart to mkBooking. Status is
// deliberately NOT set: the class-price gap is unconditional on attendance.
func (f *wlFixture) mkPricedBooking(t *testing.T, name string, priceCents any) {
	t.Helper()
	f.vtx(t, name, "booking")
	f.vtx(t, name+"_session", "session")
	schedData := map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0}
	if priceCents != nil {
		schedData["priceCents"] = priceCents
	}
	f.aspect(t, name+"_session", "schedule", "sessionSchedule", schedData)
	f.edge(t, "forSession", name, name+"_session")
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
	requireIntColumn(t, v, "maxretries_charge", maxChargeRetries)
}

// requireIntColumn asserts a lens-projected column is present and equals want
// as an integer. The full engine returns a numeric literal as int64 (the
// cypher parser's strconv.ParseInt path), so accept int/int64/float64 alike —
// what matters is the integer value, mirroring lease-signing's own
// requireIntColumn (lens_cypher_test.go).
func requireIntColumn(t *testing.T, v map[string]any, col string, want int) {
	t.Helper()
	got, ok := v[col]
	require.Truef(t, ok, "row must carry the %s column", col)
	switch n := got.(type) {
	case int:
		require.Equalf(t, want, n, "%s", col)
	case int64:
		require.Equalf(t, want, int(n), "%s", col)
	case float64:
		require.Equalf(t, want, int(n), "%s", col)
	default:
		t.Fatalf("%s is %T, not a numeric cap", col, got)
	}
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

func TestWellnessClassPriceSettlement_NoPrice_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "freebkg", nil)

	v := f.projectClassPriceAt(t, "freebkg")[0].Values
	require.Equal(t, "vtx.booking."+f.ids["freebkg"], v["entityKey"])
	require.Nil(t, v["priceCents"], "no priceCents set — a free class")
	require.Equal(t, false, v["missing_price_charge"], "no price to charge — never violates")
	require.Equal(t, false, v["violating"])
}

func TestWellnessClassPriceSettlement_ZeroPrice_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "zerobkg", 0.0)

	v := f.projectClassPriceAt(t, "zerobkg")[0].Values
	require.Equal(t, 0.0, v["priceCents"])
	require.Equal(t, false, v["missing_price_charge"], "priceCents=0 — a free class — never violates")
	require.Equal(t, false, v["violating"])
}

func TestWellnessClassPriceSettlement_PricedNoAccount_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "noacctpbkg", 1500.0)

	v := f.projectClassPriceAt(t, "noacctpbkg")[0].Values
	require.Nil(t, v["accountKey"], "booker has no wellness-ledger account yet")
	require.Equal(t, false, v["missing_price_charge"], "no account to charge yet — this gap doesn't gate (no missing_account gap)")
	require.Equal(t, false, v["violating"])
}

func TestWellnessClassPriceSettlement_PricedWithAccountNoCharge_MissingPriceCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "unchargedpbkg", 1500.0)
	f.vtx(t, "unchargedpbkg_acct", "wellnessaccount")
	f.edge(t, "heldFor", "unchargedpbkg_acct", "unchargedpbkg_identity")

	v := f.projectClassPriceAt(t, "unchargedpbkg")[0].Values
	require.Equal(t, "vtx.wellnessaccount."+f.ids["unchargedpbkg_acct"], v["accountKey"])
	require.Equal(t, 1500.0, v["priceCents"])
	require.Equal(t, "Vinyasa Flow", v["sessionName"])
	require.Equal(t, true, v["missing_price_charge"], "no wellnesstransaction settlesClassPrice this booking yet — violating")
	require.Equal(t, true, v["violating"])
	requireIntColumn(t, v, "maxretries_price_charge", maxPriceChargeRetries)
}

func TestWellnessClassPriceSettlement_PricedCharged_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "chargedpbkg", 1500.0)
	f.vtx(t, "chargedpbkg_acct", "wellnessaccount")
	f.edge(t, "heldFor", "chargedpbkg_acct", "chargedpbkg_identity")
	f.vtx(t, "chargedpbkg_tx", "wellnesstransaction")
	f.edge(t, "settlesClassPrice", "chargedpbkg_tx", "chargedpbkg")

	v := f.projectClassPriceAt(t, "chargedpbkg")[0].Values
	require.Equal(t, false, v["missing_price_charge"], "a wellnesstransaction settlesClassPrice this booking — converged")
	require.Equal(t, false, v["violating"])
}

// mkResidentPricedBooking is mkPricedBooking's counterpart for the resident-rate
// gap: the booking's .status carries rate (standard|resident) and the session
// additionally carries the given (optional, nil to omit) residentPriceCents.
func (f *wlFixture) mkResidentPricedBooking(t *testing.T, name string, priceCents any, residentPriceCents any, rate string) {
	t.Helper()
	f.vtx(t, name, "booking")
	f.aspect(t, name, "status", "bookingStatus", map[string]any{"value": "booked", "rate": rate})
	f.vtx(t, name+"_session", "session")
	schedData := map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0}
	if priceCents != nil {
		schedData["priceCents"] = priceCents
	}
	if residentPriceCents != nil {
		schedData["residentPriceCents"] = residentPriceCents
	}
	f.aspect(t, name+"_session", "schedule", "sessionSchedule", schedData)
	f.edge(t, "forSession", name, name+"_session")
	f.vtx(t, name+"_identity", "identity")
	f.edge(t, "bookedBy", name, name+"_identity")
}

func TestWellnessClassPriceSettlement_ResidentRate_ChargedResidentPrice(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkResidentPricedBooking(t, "residentbkg", 1500.0, 1000.0, "resident")

	v := f.projectClassPriceAt(t, "residentbkg")[0].Values
	require.Equal(t, 1000.0, v["priceCents"], "a resident booking on a session with residentPriceCents charges the resident price, not the standard priceCents")
}

func TestWellnessClassPriceSettlement_ResidentRateNoResidentPrice_FallsBackToStandard(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkResidentPricedBooking(t, "residentnofallbkg", 1500.0, nil, "resident")

	v := f.projectClassPriceAt(t, "residentnofallbkg")[0].Values
	require.Equal(t, 1500.0, v["priceCents"], "no residentPriceCents on the session — a resident pays priceCents same as a standard booker")
}

func TestWellnessClassPriceSettlement_StandardRateWithResidentPrice_ChargedStandardPrice(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkResidentPricedBooking(t, "standardbkg", 1500.0, 1000.0, "standard")

	v := f.projectClassPriceAt(t, "standardbkg")[0].Values
	require.Equal(t, 1500.0, v["priceCents"], "a standard booking is unaffected by a session's residentPriceCents — must not accidentally receive the discount")
}

// TestWellnessClassPriceSettlement_NoShowSettlesLinkDoesNotConverge proves
// the two settlement gaps are genuinely independent: a `settles` link (the
// no-show fee's relation) does NOT satisfy classPriceSettlementSpec's
// `settlesClassPrice`-keyed OPTIONAL MATCH — the class-price gap stays
// violating even though a (differently-purposed) transaction already
// references this booking.
func TestWellnessClassPriceSettlement_NoShowSettlesLinkDoesNotConverge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPricedBooking(t, "crossedbkg", 1500.0)
	f.vtx(t, "crossedbkg_acct", "wellnessaccount")
	f.edge(t, "heldFor", "crossedbkg_acct", "crossedbkg_identity")
	f.vtx(t, "crossedbkg_tx", "wellnesstransaction")
	f.edge(t, "settles", "crossedbkg_tx", "crossedbkg")

	v := f.projectClassPriceAt(t, "crossedbkg")[0].Values
	require.Equal(t, true, v["missing_price_charge"], "a settles (no-show) link must not satisfy the settlesClassPrice gap")
	require.Equal(t, true, v["violating"])
}

// projectRefundAt runs the anchored wellnessRefundSettlement spec for one
// wellnessrefund marker — refundSettlementSpec's counterpart to
// projectClassPriceAt, anchored on wellnessrefund rather than booking (the
// booking is already tombstoned by the time a refund marker exists).
func (f *wlFixture) projectRefundAt(t *testing.T, refundName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(refundSettlementSpec)
	require.NoError(t, err, "wellnessRefundSettlement cypher must parse on the full engine")
	refundKey := "vtx.wellnessrefund." + f.ids[refundName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    refundKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkRefund seeds one wellnessrefund marker carrying the given (optional, nil
// to omit) accountKey/amountCents on its .detail aspect — mirroring what
// wellness-domain's CancelBooking mints. accountName, when non-empty, also
// seeds a real wellnessaccount vertex so accountKey resolves to a live key.
func (f *wlFixture) mkRefund(t *testing.T, name string, accountName string, amountCents any) {
	t.Helper()
	f.vtx(t, name, "wellnessrefund")
	detail := map[string]any{}
	if accountName != "" {
		f.vtx(t, accountName, "wellnessaccount")
		detail["accountKey"] = "vtx.wellnessaccount." + f.ids[accountName]
	}
	if amountCents != nil {
		detail["amountCents"] = amountCents
	}
	f.aspect(t, name, "detail", "wellnessRefundDetail", detail)
}

func TestWellnessRefundSettlement_NoCredit_MissingRefund(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkRefund(t, "openrefund", "openrefund_acct", 1500.0)

	v := f.projectRefundAt(t, "openrefund")[0].Values
	require.Equal(t, "vtx.wellnessrefund."+f.ids["openrefund"], v["entityKey"])
	require.Equal(t, "vtx.wellnessaccount."+f.ids["openrefund_acct"], v["accountKey"])
	require.Equal(t, 1500.0, v["amountCents"])
	require.Equal(t, true, v["missing_refund"], "no wellnesstransaction settlesRefund this marker yet — violating")
	require.Equal(t, true, v["violating"])
	requireIntColumn(t, v, "maxretries_refund", maxRefundRetries)
}

func TestWellnessRefundSettlement_Credited_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkRefund(t, "paidrefund", "paidrefund_acct", 1500.0)
	f.vtx(t, "paidrefund_tx", "wellnesstransaction")
	f.edge(t, "settlesRefund", "paidrefund_tx", "paidrefund")

	v := f.projectRefundAt(t, "paidrefund")[0].Values
	require.Equal(t, false, v["missing_refund"], "a wellnesstransaction settlesRefund this marker — converged")
	require.Equal(t, false, v["violating"])
}

// TestWellnessRefundSettlement_ClassPriceLinkDoesNotConverge proves the
// refund gap is independent of the settlesClassPrice relation it mirrors: a
// settlesClassPrice-linked transaction (a DIFFERENT relation, and in
// practice never even reachable from a wellnessrefund's own OPTIONAL MATCH,
// which keys on settlesRefund) does not satisfy this gap.
func TestWellnessRefundSettlement_ClassPriceLinkDoesNotConverge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkRefund(t, "crossedrefund", "crossedrefund_acct", 1500.0)
	f.vtx(t, "crossedrefund_tx", "wellnesstransaction")
	f.edge(t, "settlesClassPrice", "crossedrefund_tx", "crossedrefund")

	v := f.projectRefundAt(t, "crossedrefund")[0].Values
	require.Equal(t, true, v["missing_refund"], "a settlesClassPrice link must not satisfy the settlesRefund gap")
	require.Equal(t, true, v["violating"])
}

// project runs the unanchored wellnessLedgerHistory spec — the engine
// enumerates its own roots and no actorKey is supplied, mirroring
// clinic-ledger/lens_cypher_test.go's `project` helper.
func (f *wlFixture) project(t *testing.T, specName, spec string) []ruleengine.ProjectionResult {
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

// mkPostedTransaction seeds the shape a committed WellnessDebitAccount/
// WellnessCreditAccount produces: a wellnesstransaction posted to a
// wellnessaccount held for an identity.
func (f *wlFixture) mkPostedTransaction(t *testing.T, prefix string, amountCents float64, memo string) {
	t.Helper()
	f.vtx(t, prefix+"_identity", "identity")
	f.vtx(t, prefix+"_acct", "wellnessaccount")
	f.vtx(t, prefix+"_tx", "wellnesstransaction")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_identity")
	f.edge(t, "postedTo", prefix+"_tx", prefix+"_acct")
	f.aspect(t, prefix+"_tx", "entry", "transactionEntry", map[string]any{
		"type":        "debit",
		"amountCents": amountCents,
		"memo":        memo,
		"postedAt":    "2026-08-21T00:00:00Z",
	})
}

func TestWellnessLedgerHistory_SettlesBooking_ProjectsClassName(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPostedTransaction(t, "noshow", 2500, "No-show fee")
	f.vtx(t, "noshow_booking", "booking")
	f.vtx(t, "noshow_session", "session")
	f.aspect(t, "noshow_session", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-08-19T09:00:00Z", "endsAt": "2026-08-19T10:00:00Z", "capacity": 20,
	})
	f.edge(t, "forSession", "noshow_booking", "noshow_session")
	f.edge(t, "settles", "noshow_tx", "noshow_booking")

	rows := f.project(t, "wellnessLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.booking."+f.ids["noshow_booking"], v["bookingKey"],
		"the settles link ties this charge to the booking that caused it")
	require.Equal(t, "Vinyasa Flow", v["className"])
	require.Equal(t, "2026-08-19T09:00:00Z", v["classStartsAt"])
}

func TestWellnessLedgerHistory_NoSettlesLink_ProjectsNullClassName(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	f.mkPostedTransaction(t, "payment", 5000, "Front-desk payment")

	rows := f.project(t, "wellnessLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["bookingKey"], "a payment settles no booking — OPTIONAL MATCH leaves it null")
	require.Nil(t, v["className"])
	require.Nil(t, v["classStartsAt"])
}
