package cafedomain

// Rule-engine proof of the cafeTabSettlement convergence lens, driven through
// the `full` engine (engine:"full") against an embedded NATS Core/Adjacency
// KV — the same harness semantic-contracts / lease-signing / clinic-reminders
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
//   - SETTLED_PAID_*: the counter-payment gap. A tab recording
//     paidAtSettleCents opens missing_payment only once a DEBIT
//     cafetransaction settles it (the charge is posted), and closes it once a
//     CREDIT one settles it too; a settled tab recording no paidAtSettleCents
//     never opens it. Both entries carry the same settles hop — the lens
//     tells them apart by .entry.type, so every settling fixture here seeds
//     a typed .entry (settlesEntry).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type cdFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newCdFixture(t *testing.T) *cdFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &cdFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *cdFixture) vtx(t *testing.T, name, typ string) string {
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

func (f *cdFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// tombstoneVtx marks a previously-seeded vertex isDeleted — the exact
// footprint the 2026-08-23 duplicate-listing reap left on a unit a live
// lease still applies to (mirrors wellness-domain's own tombstoneVtx), so a
// test can put appliesToUnit's target into the state that gave 11 café
// leases an empty coveringLocations with a live link.
func (f *cdFixture) tombstoneVtx(t *testing.T, name string) {
	t.Helper()
	id := f.ids[name]
	key := "vtx." + f.types[id] + "." + id
	body := map[string]any{"key": key, "class": f.types[id], "isDeleted": true, "data": map[string]any{}}
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

// settlesEntry seeds one cafetransaction with a typed .entry aspect and a
// settles link to the tab — the exact shape cafe-ledger's post_entry leaves
// behind for a playbook-posted charge (type debit) or counter payment (type
// credit). The type is load-bearing: the lens counts settling debits and
// settling credits separately, and an entry with no .entry at all counts as
// neither.
func (f *cdFixture) settlesEntry(t *testing.T, name, tabName, entryType string) {
	t.Helper()
	f.vtx(t, name, "cafetransaction")
	f.aspect(t, name, "entry", "transactionEntry", map[string]any{"type": entryType, "amountCents": 1200.0})
	f.edge(t, "settles", name, tabName)
}

// projectAt runs the anchored cafeTabSettlement spec for one tab. NO clock
// parameter is supplied — the cypher references none.
func (f *cdFixture) projectAt(t *testing.T, tabName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(tabSettlementSpec)
	require.NoError(t, err, "cafeTabSettlement cypher must parse on the full engine")
	tabKey := "vtx.tab." + f.ids[tabName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": tabKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// valuesAt is projectAt for the single-row case. Indexing the slice directly
// panics when a spec change drops the row, which aborts the whole test binary
// and hides every later failure behind the first one; require.Len reports the
// real cause and lets the rest of the package still run.
func (f *cdFixture) valuesAt(t *testing.T, tabName string) map[string]any {
	t.Helper()
	rows := f.projectAt(t, tabName)
	require.Len(t, rows, 1, "cafeTabSettlement must project exactly one row per tab")
	return rows[0].Values
}

// mkTab seeds one tab against a fresh leaseapp, with the given status, in the
// exact link shape production leaves it in: `chargedTo` always, and `openFor`
// only while the tab is open — Settle tombstones that hop (ddls.go), so a
// settled tab genuinely has no `openFor` link to walk.
func (f *cdFixture) mkTab(t *testing.T, name string, status string, totalCents float64) {
	t.Helper()
	f.vtx(t, name, "tab")
	f.aspect(t, name, "status", "tabStatus", map[string]any{"value": status, "totalCents": totalCents, "openedAt": "2026-07-07T12:00:00Z"})
	f.vtx(t, name+"_lease", "leaseapp")
	f.edge(t, "chargedTo", name, name+"_lease")
	if status == "open" {
		f.edge(t, "openFor", name, name+"_lease")
	}
}

func TestCafeTabSettlement_OpenNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "opentab", "open", 850)

	v := f.valuesAt(t, "opentab")
	require.Equal(t, "vtx.tab."+f.ids["opentab"], v["entityKey"])
	require.Equal(t, "open", v["status"])
	require.Equal(t, "2026-07-07T12:00:00Z", v["openedAt"])
	require.Nil(t, v["settledAt"], "still open — never settled")
	require.Equal(t, false, v["missing_account"], "still open — never violates")
	require.Equal(t, false, v["missing_charge"], "still open — never violates")
	require.Equal(t, false, v["violating"])
}

func TestCafeTabSettlement_SettledStatusAndTimestampsProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "settledtab", "tab")
	f.aspect(t, "settledtab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1200.0,
		"openedAt": "2026-07-07T12:00:00Z", "settledAt": "2026-07-07T13:00:00Z",
	})
	f.vtx(t, "settledtab_lease", "leaseapp")
	f.edge(t, "chargedTo", "settledtab", "settledtab_lease")
	f.aspect(t, "settledtab_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.settlesEntry(t, "settledtab_tx", "settledtab", "debit")

	v := f.valuesAt(t, "settledtab")
	require.Equal(t, "settled", v["status"])
	require.Equal(t, "2026-07-07T12:00:00Z", v["openedAt"])
	require.Equal(t, "2026-07-07T13:00:00Z", v["settledAt"])
	require.Equal(t, false, v["violating"], "fully posted — converged")
}

// TestCafeTabSettlement_ItemsMemoProjectsThrough proves the settlement lens
// row's itemsMemo column carries whatever the settled tab's own .status
// aspect froze there — the exact "Latte, Latte" shape
// TestChargeVoidSettleItemsMemo_ProjectsLiveNonVoidedLines
// (integration_test.go) drives Charge/VoidCharge/Settle to produce, seeded
// directly here since this lens's own column is a plain pass-through.
func TestCafeTabSettlement_ItemsMemoProjectsThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "memotab", "tab")
	f.aspect(t, "memotab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 800.0, "itemsMemo": "Latte, Latte",
		"lines": []any{
			map[string]any{"id": "line-1", "description": "Croissant", "amountCents": 400.0, "voided": true},
			map[string]any{"id": "line-2", "description": "Latte", "amountCents": 400.0, "voided": false},
			map[string]any{"id": "line-3", "description": "Latte", "amountCents": 400.0, "voided": false},
		},
		"openedAt": "2026-07-22T12:00:00Z", "settledAt": "2026-07-22T13:00:00Z",
	})
	f.vtx(t, "memotab_lease", "leaseapp")
	f.edge(t, "chargedTo", "memotab", "memotab_lease")

	v := f.valuesAt(t, "memotab")
	require.Equal(t, "Latte, Latte", v["itemsMemo"], "the settlement lens row must carry the same frozen memo the op wrote")
}

func TestCafeTabSettlement_SettledZeroTotal_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "zerotab", "settled", 0)

	v := f.valuesAt(t, "zerotab")
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

	v := f.valuesAt(t, "noaccttab")
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

	v := f.valuesAt(t, "unchargedtab")
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
	f.settlesEntry(t, "chargedtab_tx", "chargedtab", "debit")

	v := f.valuesAt(t, "chargedtab")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"], "a cafetransaction settles this tab — converged")
	require.Equal(t, false, v["violating"])
}

// TestCafeTabSettlement_PaidNoDebit_MissingChargeOnly is the ORDERING pin: a
// settled tab that records cash taken at the counter still opens ONLY
// missing_charge until the debit posts. cafe-ledger's payment cap reads the
// account's live .balance, so a credit dispatched before the charge is
// refused PaymentExceedsBalance for money the resident already handed over —
// the txCount > 0 conjunct on missing_payment is what keeps the two gaps in
// sequence and never both live. Dropping that conjunct fails this pin.
func TestCafeTabSettlement_PaidNoDebit_MissingChargeOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "paidnodebit", "settled", 1200)
	f.aspect(t, "paidnodebit", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1200.0, "openedAt": "2026-07-07T12:00:00Z",
		"paidAtSettleCents": 1200.0, "paidAtSettleBy": "vtx.identity.BBFAKESTAFFHJKMNPQRS",
	})
	f.aspect(t, "paidnodebit_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})

	v := f.valuesAt(t, "paidnodebit")
	require.Equal(t, 1200.0, v["paidAtSettleCents"], "the recorded counter payment projects through")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, true, v["missing_charge"], "no cafetransaction settles this tab yet — the debit comes first")
	require.Equal(t, false, v["missing_payment"], "the payment gap must not open before the debit is posted")
	require.Equal(t, true, v["violating"])
}

// TestCafeTabSettlement_PaidAndDebited_MissingPayment: once the debit is
// posted (a cafetransaction settles the tab), the recorded counter payment is
// the one posting still owed — missing_payment opens, missing_charge closes.
func TestCafeTabSettlement_PaidAndDebited_MissingPayment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "paiddebited", "settled", 1200)
	f.aspect(t, "paiddebited", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1200.0, "openedAt": "2026-07-07T12:00:00Z",
		"paidAtSettleCents": 1200.0, "paidAtSettleBy": "vtx.identity.BBFAKESTAFFHJKMNPQRS",
	})
	f.aspect(t, "paiddebited_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.settlesEntry(t, "paiddebited_tx", "paiddebited", "debit")

	v := f.valuesAt(t, "paiddebited")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"], "the debit is posted")
	require.Equal(t, true, v["missing_payment"], "cash was taken at settle and no credit settles this tab yet — violating")
	require.Equal(t, true, v["violating"])
}

// TestCafeTabSettlement_PaidCreditAlone_MissingChargeOnly is the type
// discrimination pin: the charge and the counter payment share one settles
// hop, so a CREDIT settling the tab with no DEBIT must satisfy neither
// count the wrong way — missing_charge stays open (no debit yet) and
// missing_payment stays closed (its debit conjunct is unmet, and the credit
// is not a charge). A count over the bare hop, blind to type, would read the
// credit as the charge and mark the tab converged with nothing charged.
func TestCafeTabSettlement_PaidCreditAlone_MissingChargeOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "creditalone", "settled", 1200)
	f.aspect(t, "creditalone", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1200.0, "openedAt": "2026-07-07T12:00:00Z",
		"paidAtSettleCents": 1200.0, "paidAtSettleBy": "vtx.identity.BBFAKESTAFFHJKMNPQRS",
	})
	f.aspect(t, "creditalone_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.settlesEntry(t, "creditalone_px", "creditalone", "credit")

	v := f.valuesAt(t, "creditalone")
	require.Equal(t, true, v["missing_charge"], "a settling CREDIT is not the charge — the debit is still owed")
	require.Equal(t, false, v["missing_payment"], "no debit yet, so the payment gap cannot open")
	require.Equal(t, true, v["violating"])
}

// TestCafeTabSettlement_PaidDebitedAndCredited_Converged: the credit the
// Weaver posts writes the same settles hop with .entry.type credit, and the
// row converges on every gap.
func TestCafeTabSettlement_PaidDebitedAndCredited_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "paidconverged", "settled", 1200)
	f.aspect(t, "paidconverged", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1200.0, "openedAt": "2026-07-07T12:00:00Z",
		"paidAtSettleCents": 1200.0, "paidAtSettleBy": "vtx.identity.BBFAKESTAFFHJKMNPQRS",
	})
	f.aspect(t, "paidconverged_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.settlesEntry(t, "paidconverged_tx", "paidconverged", "debit")
	f.settlesEntry(t, "paidconverged_px", "paidconverged", "credit")

	v := f.valuesAt(t, "paidconverged")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"])
	require.Equal(t, false, v["missing_payment"], "a credit settles this tab — converged")
	require.Equal(t, false, v["violating"])
}

// TestCafeTabSettlement_UnpaidAndDebited_NoPaymentGap: a settled tab that
// records NO paidAtSettleCents never opens missing_payment, debit posted or
// not — absent means no counter payment was taken, never "unpaid" (the
// resident pays later against the ledger). This is also the null-comparison
// pin: the full engine reads `paidAtSettleCents > 0` as false when the field
// is absent (ruleengine/full/values.go compareAny), so no null guard is
// needed in the spec.
func TestCafeTabSettlement_UnpaidAndDebited_NoPaymentGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "unpaiddebited", "settled", 1200)
	f.aspect(t, "unpaiddebited_lease", "cafeLedgerAccount", "cafeLedgerAccountGuard", map[string]any{"accountKey": "vtx.cafeaccount.BBFAKEACCTHJKMNPQRST"})
	f.settlesEntry(t, "unpaiddebited_tx", "unpaiddebited", "debit")

	v := f.valuesAt(t, "unpaiddebited")
	require.Nil(t, v["paidAtSettleCents"], "no counter payment recorded")
	require.Equal(t, false, v["missing_charge"])
	require.Equal(t, false, v["missing_payment"], "no paidAtSettleCents — nothing to post; absent is not unpaid")
	require.Equal(t, false, v["violating"])
}

// TestCafeTabSettlement_SurvivesTheOpenForRetraction pins which of a tab's two
// lease links the convergence walks, because getting it wrong loses money
// silently rather than loudly.
//
// Settle tombstones `openFor` — that retraction is what bounds a resident's
// edgeEntityTabs read grant to their open tabs — and it lands in the SAME
// mutation set that flips .status to settled. So the instant a tab starts
// owing a posting is the instant `openFor` stops existing. A convergence lens
// that required that hop would project no row at all for the one tab shape it
// exists to catch, and with EmptyBehavior "delete" the target row would be
// removed and Weaver would dispatch nothing: an unposted house tab, no error
// anywhere. Only `chargedTo` survives settlement, so only `chargedTo` can
// carry this walk.
func TestCafeTabSettlement_SurvivesTheOpenForRetraction(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "posttab", "tab")
	f.aspect(t, "posttab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 1750.0,
		"openedAt": "2026-07-07T12:00:00Z", "settledAt": "2026-07-07T13:30:00Z",
	})
	f.vtx(t, "posttab_lease", "leaseapp")
	// Exactly what Settle leaves behind: chargedTo standing, no openFor.
	f.edge(t, "chargedTo", "posttab", "posttab_lease")

	rows := f.projectAt(t, "posttab")
	require.Len(t, rows, 1, "a settled tab must still project — its posting has not happened yet")
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["posttab_lease"], v["leaseAppKey"],
		"the lease must still be reachable after the openFor retraction")
	require.Equal(t, true, v["missing_account"], "settled + owes money + no account — Weaver must be told")
	require.Equal(t, true, v["violating"])
}

// TestCafeTabSettlement_OpenForAloneDoesNotAnchor proves the retraction is
// load-bearing in the other direction too: a tab wired ONLY with the transient
// hop projects nothing, so no lingering `openFor` can quietly keep a
// pre-`chargedTo` tab converging and mask a missing permanent link.
func TestCafeTabSettlement_OpenForAloneDoesNotAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "legacytab", "tab")
	f.aspect(t, "legacytab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 900.0, "openedAt": "2026-07-07T12:00:00Z"})
	f.vtx(t, "legacytab_lease", "leaseapp")
	f.edge(t, "openFor", "legacytab", "legacytab_lease")

	require.Empty(t, f.projectAt(t, "legacytab"),
		"chargedTo is the settlement anchor; openFor must not stand in for it")
}

// TestCafeTabSettlement_OpenTabWithoutChargedToStillAnchors is the mirror of
// OpenForAloneDoesNotAnchor for the one case openFor legitimately DOES
// anchor: a tab that is still OPEN and predates the chargedTo write
// entirely. Without this fallback such a tab has no row at all — invisible
// to every reader, its lease's open-tab guard claimed forever. Settle
// backfills chargedTo unconditionally (ddls.go), so this state is transient:
// the row exists just long enough for a surface to find the tab and close it.
func TestCafeTabSettlement_OpenTabWithoutChargedToStillAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "predatestab", "tab")
	f.aspect(t, "predatestab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 650.0, "openedAt": "2026-07-20T10:00:00Z"})
	f.vtx(t, "predatestab_lease", "leaseapp")
	f.edge(t, "openFor", "predatestab", "predatestab_lease")

	v := f.valuesAt(t, "predatestab")
	require.Equal(t, "vtx.leaseapp."+f.ids["predatestab_lease"], v["leaseAppKey"],
		"openFor must resolve the lease while the tab is still open and chargedTo is absent")
	require.Equal(t, false, v["missing_account"], "still open — never violates")
	require.Equal(t, false, v["missing_charge"], "still open — never violates")
	require.Equal(t, false, v["violating"])
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits when a
// target's @at fires: the instant the timer fired for, recorded under that
// target's own key in byTarget, with expiredAt carrying the entity-wide
// maximum — orchestration-base/lens_cypher_test.go's own recordLapse,
// applied to a tab instead of a task. byTarget takes several entries because
// a tab shares this one marker slot with cafeTabSettlement's own target.
func (f *cdFixture) recordLapse(t *testing.T, tabName string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, tabName, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// projectStaleAt runs the anchored cafeStaleTabSettlement spec for one tab.
// NO clock parameter is supplied — the cypher references none; the tests
// below pin the outcome against a recorded freshnessExpiry marker instead of
// an injected instant.
func (f *cdFixture) projectStaleAt(t *testing.T, tabName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(staleTabSettlementSpec)
	require.NoError(t, err, "cafeStaleTabSettlement cypher must parse on the full engine")
	tabKey := "vtx.tab." + f.ids[tabName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": tabKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func (f *cdFixture) valuesAtStale(t *testing.T, tabName string) map[string]any {
	t.Helper()
	rows := f.projectStaleAt(t, tabName)
	require.Len(t, rows, 1, "cafeStaleTabSettlement must project exactly one row per tab")
	return rows[0].Values
}

// TestCafeStaleTabSettlement_OpenAndFresh_ArmsFreshUntilNotViolating proves
// the one-shot @at arms at staleAt while no lapse of this target's deadline
// has been recorded yet — the pastDueAppointments idiom (clinic-reminders),
// never violating this early.
func TestCafeStaleTabSettlement_OpenAndFresh_ArmsFreshUntilNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "freshtab", "tab")
	f.aspect(t, "freshtab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})

	v := f.valuesAtStale(t, "freshtab")
	require.Equal(t, "open", v["status"])
	require.Equal(t, "2026-07-08T12:00:00Z", v["staleAt"])
	require.Equal(t, "2026-07-08T12:00:00Z", v["freshUntil"], "no recorded lapse — arms the one-shot @at, even when staleAt is already past")
	require.Equal(t, false, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "staleAt is present — nothing to backfill")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_RecordedLapseAtStaleAt_AllThreeOccurrencesAgree
// proves the gate opens once this target's own @at has fired at or after
// staleAt — the violating row itself drives dispatch from there, not a
// repeated timer wake-up — and that all three converted occurrences
// (freshUntil, missing_settle, violating's first disjunct) read the marker
// identically, since violating's disjunct repeats the comparison rather than
// naming missing_settle's alias.
func TestCafeStaleTabSettlement_RecordedLapseAtStaleAt_AllThreeOccurrencesAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "pastduetab", "tab")
	f.aspect(t, "pastduetab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})
	f.recordLapse(t, "pastduetab", map[string]string{StaleTabSettlementTarget: "2026-07-08T12:00:00Z"})

	v := f.valuesAtStale(t, "pastduetab")
	require.Nil(t, v["freshUntil"], "the lapse is recorded — freshUntil goes null, the gap-dispatch path owns it now")
	require.Equal(t, true, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "staleAt is present — this is missing_settle's gap, not missing_staleat's")
	require.Equal(t, true, v["violating"])
}

// TestCafeStaleTabSettlement_ExtendedPastTheRecordedLapse is the RE-ARM
// vector: nothing clears the marker, so a tab whose staleAt was recomputed
// later than an earlier fire (a re-issued deadline, or a marker predating
// this feature) must arm again off the stored comparison alone — the
// orchestration-base precedent, TestUnroutedTasks_ExtendedPastTheRecordedLapse.
func TestCafeStaleTabSettlement_ExtendedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "extendedtab", "tab")
	f.aspect(t, "extendedtab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-09T12:00:00Z",
	})
	f.recordLapse(t, "extendedtab", map[string]string{StaleTabSettlementTarget: "2026-07-08T12:00:00Z"})

	v := f.valuesAtStale(t, "extendedtab")
	require.Equal(t, false, v["missing_settle"], "a lapse the current staleAt has outrun is not a lapse of THIS deadline")
	require.Equal(t, "2026-07-09T12:00:00Z", v["freshUntil"], "and the @at re-arms with no clearing write")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_SiblingTargetLapseDoesNotOpenThisGap is the
// isolation vector: a tab is also the anchor of cafeTabSettlement, sharing
// this one marker slot, so reading any entry but this target's own would let
// the sibling's fire surface this gate — orchestration-base's
// TestUnroutedTasks_SiblingTargetLapseDoesNotOpenThisGap.
func TestCafeStaleTabSettlement_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "siblingtab", "tab")
	f.aspect(t, "siblingtab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})
	f.recordLapse(t, "siblingtab", map[string]string{TabSettlementTarget: "2099-01-01T00:00:00Z"})

	v := f.valuesAtStale(t, "siblingtab")
	require.Equal(t, false, v["missing_settle"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-07-08T12:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_MarkerWithNoByTargetMapReadsUnlapsed pins the
// shape a marker written before byTarget existed carries: `expiredAt` alone,
// which says that SOMETHING lapsed on this tab and never which target. A
// convergence target must not answer for a sibling's fire, so the four-hop read
// of its own entry resolves to nil, compareAny answers false, and the tab reads
// unlapsed with its timer still armed — the same reading
// TestUnroutedTasks_MarkerWithNoByTargetMapReadsUnlapsed pins on
// orchestration-base's half.
//
// It is the absence vector the sibling-target test cannot stand in for: that
// one has a byTarget map with the wrong key in it, this one has no map at all,
// and a read that defaulted to expiredAt would pass the first and fail here.
func TestCafeStaleTabSettlement_MarkerWithNoByTargetMapReadsUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "legacymarkertab", "tab")
	f.aspect(t, "legacymarkertab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})
	// Written directly rather than through recordLapse: that helper always
	// derives a byTarget map, and this shape is precisely the one without it.
	f.aspect(t, "legacymarkertab", "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": "2099-01-01T00:00:00Z",
	})

	v := f.valuesAtStale(t, "legacymarkertab")
	require.Equal(t, false, v["missing_settle"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, "2026-07-08T12:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_Settled_NeverViolatesRegardlessOfStaleAt proves
// a legitimate staff Settle at any point permanently converges the gate:
// Settle's status_data rewrite drops staleAt entirely (ddls.go), and
// status='open' is the only terminal-state check this spec needs (unlike an
// appointment's three-way status, a tab is only ever open or settled) — even
// with a recorded lapse standing on the tab, the status gate excludes it.
func TestCafeStaleTabSettlement_Settled_NeverViolatesRegardlessOfStaleAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "settledstaletab", "tab")
	f.aspect(t, "settledstaletab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "settledAt": "2026-07-07T13:00:00Z",
	})
	f.recordLapse(t, "settledstaletab", map[string]string{StaleTabSettlementTarget: "2026-09-01T00:00:00Z"})

	v := f.valuesAtStale(t, "settledstaletab")
	require.Nil(t, v["staleAt"], "Settle drops staleAt from the rewritten aspect")
	require.Nil(t, v["freshUntil"])
	require.Equal(t, false, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "settled — the status='open' guard excludes it from both gaps")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_NoStaleAt_FreshUntilNullByTheThenBranch covers a
// tab seeded without staleAt (a tab opened before this field shipped,
// af451062, or any residual showcase data predating it): the marker
// comparison and every ordering test against staleAt resolve false or null,
// so missing_settle alone can never see it — such a tab would be invisible
// to that gap alone, forever. missing_staleat is the dedicated gap that
// catches it instead, dispatching BackfillTabStaleAt (ddls.go) to compute the
// missing value so the NEXT cycle's missing_settle can see it. freshUntil's
// CASE takes its THEN branch for this row — NOT (marker >= null) is
// NOT false — but the column still comes out null, because THEN
// t.status.data.staleAt is itself null: the accident the doc comment names.
func TestCafeStaleTabSettlement_NoStaleAt_FreshUntilNullByTheThenBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "legacytab", "open", 500)

	v := f.valuesAtStale(t, "legacytab")
	require.Nil(t, v["staleAt"])
	require.Nil(t, v["freshUntil"], "the THEN branch is taken, but its own value (staleAt) is null too")
	require.Equal(t, false, v["missing_settle"], "null staleAt compares false both ways — missing_settle alone never catches it")
	require.Equal(t, true, v["missing_staleat"], "an open tab with no staleAt at all is what this gap exists to catch")
	require.Equal(t, true, v["violating"])
}

// TestCafeStaleTabSettlement_ReferencesNoClockParameter is the structural
// half of the conversion, asserted on the compiled cypher rather than on any
// one row: a lens that returns $now or $projectedAt projects a clock reading
// the sweep's deep verify cannot compare — orchestration-base's
// TestTaskDeadlineLenses_ReferenceNoClockParameter.
func TestCafeStaleTabSettlement_ReferencesNoClockParameter(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(staleTabSettlementSpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull, "must compile to the full engine")
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		require.Truef(t, exhaustive, "the query shape must be provably free of $%s", param)
		require.Falsef(t, referenced,
			"cafeStaleTabSettlement must reference no $%s — staleness is a recorded fact, not a clock reading", param)
	}
}

// TestCafeStaleTabSettlement_ReadsItsOwnTargetsMarkerEntry binds the two
// halves that can silently drift apart: the §10.8 TargetID Weaver fires a
// timer under, and the byTarget key the lens compares against its deadline —
// orchestration-base's TestTaskDeadlineLenses_ReadTheirOwnTargetsMarkerEntry,
// applied to this package's own target/lens pair.
func TestCafeStaleTabSettlement_ReadsItsOwnTargetsMarkerEntry(t *testing.T) {
	specs := map[string]string{}
	for _, l := range Lenses() {
		specs[l.CanonicalName] = l.Spec
	}
	var checked int
	for _, tgt := range WeaverTargets() {
		spec, ok := specs[tgt.LensRef]
		require.Truef(t, ok, "target %s names lens %s, which this package must declare", tgt.TargetID, tgt.LensRef)
		if !strings.Contains(spec, "freshnessExpiry") {
			continue
		}
		require.Containsf(t, spec, "byTarget."+tgt.TargetID,
			"lens %s reads a freshness marker but not under its own target id %q — the timer that fires writes an entry this cypher never reads",
			tgt.LensRef, tgt.TargetID)
		checked++
	}
	require.Equal(t, 1, checked,
		"cafeStaleTabSettlement reads a recorded lapse; a drop here is a lens that went back to a clock")
}

// project runs an UNANCHORED spec (cafeLeaseWorkplaces takes no $actorKey,
// unlike the tab-anchored convergence lens projectAt drives).
func (f *cdFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestCafeLeaseWorkplaces_CoveringLocations proves the read-side workplace
// term: a lease's coveringLocations carries its applied-to unit AND every
// containedIn ancestor, so a staff read boundary intersecting it with the
// caller's `worksAt` keys matches whether that staffer is wired to the exact
// unit or to the building above it — the read-model mirror of this package's
// own leaseapp_unit + worksAt_covers walk (facet-staff-worlds-design.md §9).
func TestCafeLeaseWorkplaces_CoveringLocations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	unitKey := f.vtx(t, "unit4b", "unit")
	buildingKey := f.vtx(t, "riverside", "location")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.edge(t, "containedIn", "unit4b", "riverside")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "the comprehension must not fan the lease into one row per ancestor")
	require.ElementsMatch(t, []any{unitKey, buildingKey}, rows[0].Values["coveringLocations"],
		"depth-0 (the lease's own unit) and its containedIn ancestor both cover the lease")
	require.Equal(t, false, rows[0].Values["missingLocation"],
		"a live, wired unit is not a data gap — the OPTIONAL MATCH head (bound to `u`) must not be confused with the comprehension's own re-walk (bound to `wu`)")
}

// TestCafeLeaseWorkplaces_DeepChainWalksEveryLevel proves the chain is walked
// past one hop — unit -> floor -> building — so a staffer wired at any level
// above the unit matches. A `*0..1` bound would pass the test above and fail
// this one.
func TestCafeLeaseWorkplaces_DeepChainWalksEveryLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	unitKey := f.vtx(t, "unit4b", "unit")
	floorKey := f.vtx(t, "floor4", "location")
	buildingKey := f.vtx(t, "riverside", "location")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.edge(t, "containedIn", "unit4b", "floor4")
	f.edge(t, "containedIn", "floor4", "riverside")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, []any{unitKey, floorKey, buildingKey}, rows[0].Values["coveringLocations"],
		"every level of the containment chain covers the lease, not just the first")
}

// TestCafeLeaseWorkplaces_NoUnitEmptyCovering proves a lease with no
// appliesToUnit still projects one row, with an EMPTY covering set rather than
// a null or a missing column: staffCoveredLeases reads that as "no SPECIFIC
// workplace covers this row" and denies via the per-workplace path, matching
// require_workplace's empty-location_keys denial. The comprehension's head is
// the matched lease, so this is also the vector that would catch it seeding
// the whole keyspace instead of binding empty. missingLocation is true here
// too (`u` never binds) — readauth.go's unattributableLeases is what actually
// keeps this row from being invisible to every front-desk staffer everywhere;
// this lens-level test only proves the flag, not the read-boundary behavior
// (cmd/cafe-app/authz_test.go's TestStaff_UnattributableLease_VisibleToAnyFrontDesk
// proves that).
func TestCafeLeaseWorkplaces_NoUnitEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "an unwired lease must still project a row, so its denial is explicit")
	require.Empty(t, rows[0].Values["coveringLocations"],
		"an unwired lease is covered by nobody SPECIFIC; the per-workplace boundary must not read that as unrestricted")
	require.Equal(t, true, rows[0].Values["missingLocation"],
		"no appliesToUnit at all is the same data-gap shape as a tombstoned target — both leave `u` unbound")
}

// TestCafeLeaseWorkplaces_TombstonedUnit_MissingLocationTrue proves the shape
// that actually hit production: appliesToUnit's LINK survives, but its target
// unit is tombstoned (the 2026-08-23 duplicate-listing reap, before the
// reap script's live-tenancy guard existed, `9a3a7807`) — 11 café leases hit
// exactly this. `u` binds to nothing (Cypher does not match through a
// tombstoned vertex), so missingLocation is true and coveringLocations is
// empty, identical to the never-wired case above; the read boundary treats
// both as one data-gap category (readauth.go's unattributableLeases).
func TestCafeLeaseWorkplaces_TombstonedUnit_MissingLocationTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "unit4b", "unit")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.tombstoneVtx(t, "unit4b")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "a lease whose unit was tombstoned out from under it still projects a row")
	require.Empty(t, rows[0].Values["coveringLocations"],
		"Cypher does not match through a tombstoned vertex, so the comprehension binds empty")
	require.Equal(t, true, rows[0].Values["missingLocation"],
		"the appliesToUnit LINK is alive but its target is gone — this must read as a data gap, not a genuine no-workplace answer")
}

// TestCafeLeaseWorkplaces_UnwiredUnitCoversItself proves the depth-0 entry
// survives on its own when the unit has no parent at all — the staffer wired
// to a standalone unit still matches, and the set is not emptied by the
// variable-length hop finding nothing.
func TestCafeLeaseWorkplaces_UnwiredUnitCoversItself(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	unitKey := f.vtx(t, "unit4b", "unit")
	f.edge(t, "appliesToUnit", "lease", "unit4b")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, []any{unitKey}, rows[0].Values["coveringLocations"],
		"*0.. must keep the unit itself when the upward walk finds no parent")
}

// TestCafeLeaseWorkplaces_OneRowPerLease proves two leases at different
// buildings project two rows whose covering sets do NOT bleed into each other
// — the discriminating pair the whole confinement rests on, since a staffer at
// one building is admitted by the first and must be refused by the second.
func TestCafeLeaseWorkplaces_OneRowPerLease(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "northLease", "leaseapp")
	f.vtx(t, "southLease", "leaseapp")
	northUnit := f.vtx(t, "northUnit", "unit")
	southUnit := f.vtx(t, "southUnit", "unit")
	northBuilding := f.vtx(t, "north", "location")
	southBuilding := f.vtx(t, "south", "location")
	f.edge(t, "appliesToUnit", "northLease", "northUnit")
	f.edge(t, "appliesToUnit", "southLease", "southUnit")
	f.edge(t, "containedIn", "northUnit", "north")
	f.edge(t, "containedIn", "southUnit", "south")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 2)
	got := map[string]any{}
	for _, r := range rows {
		got[r.Values["leaseAppKey"].(string)] = r.Values["coveringLocations"]
	}
	require.ElementsMatch(t, []any{northUnit, northBuilding}, got["vtx.leaseapp."+f.ids["northLease"]])
	require.ElementsMatch(t, []any{southUnit, southBuilding}, got["vtx.leaseapp."+f.ids["southLease"]],
		"the south lease must not inherit the north building")
}

// TestCafeLeaseWorkplaces_LeaseEndProjectsFromTenancy proves the lens's own
// leaseEnd column: the resident-readable half of the same .tenancy fact
// front-desk's frontDeskLeaseDetails projects for staff
// (packages/front-desk/lenses.go) — a lease carrying a tenancy end projects
// it verbatim, same shape, same source aspect.
func TestCafeLeaseWorkplaces_LeaseEndProjectsFromTenancy(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	f.aspect(t, "lease", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-01-01T00:00:00Z", "leaseEnd": "2026-12-31T00:00:00Z",
	})

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1)
	require.Equal(t, "2026-12-31T00:00:00Z", rows[0].Values["leaseEnd"])
}

// TestCafeLeaseWorkplaces_NoTenancyLeaseEndNull proves a lease with no
// .tenancy aspect at all — approved before lease-signing minted terms —
// still projects a row, with leaseEnd null rather than the row dropping,
// mirroring frontDeskLeaseDetails' own TestFrontDeskLeaseDetails_NullsWhenNoUnit.
func TestCafeLeaseWorkplaces_NoTenancyLeaseEndNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "a lease with no .tenancy at all must still project a row")
	require.Nil(t, rows[0].Values["leaseEnd"])
}

// TestCafeLeaseWorkplaces_MultiParentUnitUnionsBothChains proves a unit with two
// containment parents contributes BOTH to one row: a staffer at either parent is
// equally entitled to the lease. This is the read half of one rule, and
// `worksAt_covers` (ddls.go) is the write half — it walks every containedIn
// branch to the same depth, so the covering set a staffer is shown and the one
// they may write at are the same set. The write side's own multi-parent pin is
// TestWorkplace_SharedRoomCoveredByEveryContainmentParent
// (wellness-domain/workplace_confinement_test.go).
func TestCafeLeaseWorkplaces_MultiParentUnitUnionsBothChains(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	unitKey := f.vtx(t, "unit4b", "unit")
	towerKey := f.vtx(t, "tower", "location")
	campusKey := f.vtx(t, "campus", "location")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.edge(t, "containedIn", "unit4b", "tower")
	f.edge(t, "containedIn", "unit4b", "campus")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "two parents must union into one row, not fan into two")
	require.ElementsMatch(t, []any{unitKey, towerKey, campusKey}, rows[0].Values["coveringLocations"])
}

// TestCafeLeaseWorkplaces_HopBoundMatchesTheWriteSide pins the exact depth the
// covering set reaches. The write side walks `range(WORKPLACE_MAX_DEPTH)` = 8
// iterations testing depths 0..7, so the read side must admit depths 0..7 and
// NO further: `*0..8` would admit a staffer nine levels up whose writes
// require_workplace refuses — a read the write side would not have allowed.
// Nothing else pins this, and the two bounds are written in different
// languages with different counting conventions, so it is exactly the kind of
// divergence that survives review.
func TestCafeLeaseWorkplaces_HopBoundMatchesTheWriteSide(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	unitKey := f.vtx(t, "unit", "unit")
	f.edge(t, "appliesToUnit", "lease", "unit")

	// unit(0) -> a1(1) -> ... -> a8(8): one level deeper than either side reaches.
	want := []any{unitKey}
	prev := "unit"
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("a%d", i)
		key := f.vtx(t, name, "location")
		f.edge(t, "containedIn", prev, name)
		if i <= 7 {
			want = append(want, key)
		}
		prev = name
	}

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, want, rows[0].Values["coveringLocations"],
		"depths 0..7 cover the lease and depth 8 does not — the write side's own reach")
}

// TestMenuCatalog_CoveringLocations proves menuCatalogSpec's own
// coveringLocations column — the leaseWorkplacesSpec shape re-anchored on a
// menu item's servedAt link instead of a lease's appliesToUnit — so the
// front-desk Manage Menu grid can confine itself to a staffer's own
// workplace the same way staffCoveredLeases confines /api/leases: a staffer
// wired to the BUILDING must still match an item served at a UNIT inside it.
func TestMenuCatalog_CoveringLocations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "item", "menuitem")
	f.aspect(t, "item", "price", "menuItemPrice", map[string]any{"name": "Latte", "priceCents": 450.0})
	unitKey := f.vtx(t, "unit4b", "unit")
	buildingKey := f.vtx(t, "riverside", "location")
	f.edge(t, "servedAt", "item", "unit4b")
	f.edge(t, "containedIn", "unit4b", "riverside")

	rows := f.project(t, menuCatalogSpec)
	require.Len(t, rows, 1)
	require.Equal(t, unitKey, rows[0].Values["servedAt"])
	require.ElementsMatch(t, []any{unitKey, buildingKey}, rows[0].Values["coveringLocations"],
		"depth-0 (the item's own servedAt unit) and its containedIn ancestor both cover the item")
}

// TestMenuCatalog_NoServedAtEmptyCovering proves an item minted with no
// servedAt link still projects a row (the OPTIONAL MATCH above), with an
// EMPTY covering set rather than a null or missing column — the same
// fail-closed denial leaseWorkplacesSpec gives an unwired lease.
func TestMenuCatalog_NoServedAtEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "item", "menuitem")
	f.aspect(t, "item", "price", "menuItemPrice", map[string]any{"name": "Muffin", "priceCents": 300.0})

	rows := f.project(t, menuCatalogSpec)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["servedAt"])
	require.Empty(t, rows[0].Values["coveringLocations"],
		"an unlinked item is covered by nobody; the boundary must not read that as unrestricted")
}

// TestMenuCatalog_AvailableFlag proves menuCatalogSpec's own `available`
// column: an item whose .price carries available:false projects false, and
// an item whose .price carries no available field at all (never toggled)
// projects true — the
// coalesce default, never toggled means available.
func TestMenuCatalog_AvailableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	soldoutKey := f.vtx(t, "soldout", "menuitem")
	f.aspect(t, "soldout", "price", "menuItemPrice", map[string]any{"name": "Latte", "priceCents": 450.0, "available": false})
	legacyKey := f.vtx(t, "legacy", "menuitem")
	f.aspect(t, "legacy", "price", "menuItemPrice", map[string]any{"name": "Muffin", "priceCents": 300.0})

	rows := f.project(t, menuCatalogSpec)
	byKey := map[string]any{}
	for _, r := range rows {
		byKey[r.Values["menuItemKey"].(string)] = r.Values["available"]
	}
	require.Equal(t, false, byKey[soldoutKey], "an item explicitly marked unavailable projects false")
	require.Equal(t, true, byKey[legacyKey], "an item with no available field on .price projects true")
}

// identity seeds a bare vtx.identity vertex — cafeIdentitiesReadSpec's own
// anchor type, distinct from cdFixture.vtx's tab/leaseapp/unit/location
// vertices above.
func (f *cdFixture) identity(t *testing.T, name string) string {
	t.Helper()
	return f.vtx(t, name, "identity")
}

// envelopeData is an at-rest sensitive-aspect data map as step 6.5's
// encrypt-on-write commits it: base64 ct/nonce + the wrapping key id, no
// plaintext field — mirrors loftspace-domain/lens_cypher_test.go's helper of
// the same name.
func envelopeData() map[string]any {
	return map[string]any{"ct": "3q2+7w==", "nonce": "AAAAAAAAAAAAAAAA", "keyId": "k1"}
}

// TestCafeIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors proves a named
// identity projects one row: the name column carries the ciphertext envelope
// MAP whole (for the Secure-Lens decryptor, never the engine), and
// authz_anchors carries exactly the identity's OWN bare NanoID — the
// self-anchor that lets the signed-in actor read their own row via the
// platform's base cap-read self-grant with no extra grant declaration
// (mirrors loftspace-domain's landlordUnitsRead self-anchor idiom, NOT
// applicantRosterRead's empty/wildcard-only set).
func TestCafeIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	aliceKey := f.identity(t, "alice")
	f.aspect(t, "alice", "name", "name", envelopeData())

	rows := f.project(t, cafeIdentitiesReadSpec)
	require.Len(t, rows, 1, "exactly one roster row for the one named identity")
	v := rows[0].Values
	require.Equal(t, f.ids["alice"], v["identity_id"], "identity_id is the bare NanoID (nanoIdFromKey)")
	require.Equal(t, aliceKey, v["identity_key"], "identity_key names the row's owner for its consumers; the decryptor opens the row under the holder the ciphertext names")
	name, ok := v["name"].(map[string]any)
	require.True(t, ok, "name must be the ciphertext envelope map, got %T (%v)", v["name"], v["name"])
	require.Equal(t, "3q2+7w==", name["ct"], "the envelope reaches the decryptor whole")
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.ElementsMatch(t, []any{f.ids["alice"]}, anchors,
		"the row is self-anchored on the identity's own bare NanoID, not empty/wildcard-only")
}

// TestCafeIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities proves
// the ciphertext-presence WHERE: an identity with no .name aspect and an
// identity whose .name data is plaintext-shaped ({value}, no ct — a shape
// step 6.5 can never commit) both project NO row, so the lens can neither
// roster unnamed actors nor carry plaintext PII by itself.
func TestCafeIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.identity(t, "svc") // no .name at all
	f.identity(t, "legacy")
	f.aspect(t, "legacy", "name", "name", map[string]any{"value": "Plain Text"})
	bobKey := f.identity(t, "bob")
	f.aspect(t, "bob", "name", "name", envelopeData())

	rows := f.project(t, cafeIdentitiesReadSpec)
	require.Len(t, rows, 1, "only the ciphertext-named identity projects")
	require.Equal(t, bobKey, rows[0].Values["identity_key"])
}

// TestCafeIdentitiesRead_WorkplaceAnchorFanOut proves a resident identity's
// authz_anchors carries every building that covers their own lease's unit —
// the front-desk roster gap: a worksAt-anchored staffer's cap-read.staff
// grant token is one of these building keys, not the resident's own NanoID,
// so without this fan-out a real front-desk actor matched no row but its own.
// Mirrors TestCafeLeaseWorkplaces_OneRowPerLease's containedIn chain, walked
// from the identity side via applicationFor (leaseapp -> identity, Contract
// #1 §1.1: the later-arriving leaseapp is the source).
func TestCafeIdentitiesRead_WorkplaceAnchorFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	aliceKey := f.identity(t, "alice")
	f.aspect(t, "alice", "name", "name", envelopeData())
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "unit4b", "unit")
	f.vtx(t, "tower", "location")
	f.edge(t, "applicationFor", "lease", "alice")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.edge(t, "containedIn", "unit4b", "tower")

	rows := f.project(t, cafeIdentitiesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, aliceKey, rows[0].Values["identity_key"])
	require.ElementsMatch(t, []any{f.ids["alice"], f.ids["unit4b"], f.ids["tower"]}, rows[0].Values["authz_anchors"],
		"authz_anchors must carry the self-anchor PLUS the bare NanoID of the unit and every building covering it")
}

// TestCafeIdentitiesRead_NoLeaseKeepsSelfAnchorOnly proves an identity with no
// leaseapp application at all (e.g. a staffer with no residence of their own)
// still projects — the self-anchor survives on its own, and the variable-length
// walk finding no leaseapp yields an empty fan-out rather than dropping the row
// or erroring, the same posture leaseWorkplacesSpec's own *0.. hop takes for an
// unwired lease (TestCafeLeaseWorkplaces_Unwired*).
func TestCafeIdentitiesRead_NoLeaseKeepsSelfAnchorOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.identity(t, "staffonly")
	f.aspect(t, "staffonly", "name", "name", envelopeData())

	rows := f.project(t, cafeIdentitiesReadSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, []any{f.ids["staffonly"]}, rows[0].Values["authz_anchors"],
		"no lease application means no fan-out, but the self-anchor must still be present")
}

// projectExpanded runs spec with the `location*` taxonomy label resolved to
// the three concrete levels location-domain declares — the expansion the
// Refractor pipeline installs from the live subtypeOf snapshot
// (UseFullEngine), applied here by hand the way the engine's own seed-scan
// taxonomy tests do, since this fixture has no pipeline.
func (f *cdFixture) projectExpanded(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	expanded := full.WithLabelExpansion(cr.(*full.CompiledRule),
		map[string]map[string]struct{}{"location": {"unit": {}, "building": {}, "property": {}}})
	out, err := eng.ExecuteWith(context.Background(), expanded, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestHousePolicies_OneRowPerPolicyLocation proves housePoliciesSpec: a
// location carrying a .cafePolicy projects one row keyed by its own key with
// tabLimitCents and its .presentation name; a location at any level with no
// policy projects nothing (absent = no limit recorded); a location whose
// aspect carries a foreign class projects nothing either.
func TestHousePolicies_OneRowPerPolicyLocation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	buildingKey := f.vtx(t, "riverside", "building")
	f.aspect(t, "riverside", "cafePolicy", "cafeHousePolicy", map[string]any{"tabLimitCents": 5000.0})
	f.aspect(t, "riverside", "presentation", "locationPresentation", map[string]any{"name": "Riverside"})
	propertyKey := f.vtx(t, "estate", "property")
	f.aspect(t, "estate", "cafePolicy", "cafeHousePolicy", map[string]any{"tabLimitCents": 0.0})
	f.vtx(t, "unit4b", "unit")
	f.vtx(t, "annex", "building")
	f.aspect(t, "annex", "cafePolicy", "somethingElse", map[string]any{"tabLimitCents": 100.0})

	rows := f.projectExpanded(t, housePoliciesSpec)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["key"].(string)] = r.Values
	}
	require.Len(t, rows, 2, "only the two locations carrying a cafeHousePolicy aspect project: %v", byKey)
	require.Equal(t, buildingKey, byKey[buildingKey]["locationKey"])
	require.Equal(t, 5000.0, byKey[buildingKey]["tabLimitCents"])
	require.Equal(t, "Riverside", byKey[buildingKey]["name"])
	require.Equal(t, 0.0, byKey[propertyKey]["tabLimitCents"], "a $0 policy (self-service closed) projects, not dropped")
	require.Nil(t, byKey[propertyKey]["name"], "a location with no .presentation projects a null name")
}
