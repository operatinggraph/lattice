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

import (
	"context"
	"encoding/json"
	"fmt"
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
	f.vtx(t, "settledtab_tx", "cafetransaction")
	f.edge(t, "settles", "settledtab_tx", "settledtab")

	v := f.valuesAt(t, "settledtab")
	require.Equal(t, "settled", v["status"])
	require.Equal(t, "2026-07-07T12:00:00Z", v["openedAt"])
	require.Equal(t, "2026-07-07T13:00:00Z", v["settledAt"])
	require.Equal(t, false, v["violating"], "fully posted — converged")
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
	f.vtx(t, "chargedtab_tx", "cafetransaction")
	f.edge(t, "settles", "chargedtab_tx", "chargedtab")

	v := f.valuesAt(t, "chargedtab")
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"], "a cafetransaction settles this tab — converged")
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

// projectStaleAt runs the anchored cafeStaleTabSettlement spec for one tab at
// an explicit `now` — unlike projectAt (wall-clock time.Now()), the tests
// below need to pin deterministic points on either side of a tab's own
// staleAt deadline.
func (f *cdFixture) projectStaleAt(t *testing.T, tabName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(staleTabSettlementSpec)
	require.NoError(t, err, "cafeStaleTabSettlement cypher must parse on the full engine")
	tabKey := "vtx.tab." + f.ids[tabName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    tabKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func (f *cdFixture) valuesAtStale(t *testing.T, tabName, now string) map[string]any {
	t.Helper()
	rows := f.projectStaleAt(t, tabName, now)
	require.Len(t, rows, 1, "cafeStaleTabSettlement must project exactly one row per tab")
	return rows[0].Values
}

// TestCafeStaleTabSettlement_OpenAndFresh_ArmsFreshUntilNotViolating proves
// the one-shot @at arms at staleAt while the deadline is still ahead — the
// pastDueAppointments idiom (clinic-reminders), never violating this early.
func TestCafeStaleTabSettlement_OpenAndFresh_ArmsFreshUntilNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "freshtab", "tab")
	f.aspect(t, "freshtab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})

	v := f.valuesAtStale(t, "freshtab", "2026-07-08T00:00:00Z")
	require.Equal(t, "open", v["status"])
	require.Equal(t, "2026-07-08T12:00:00Z", v["staleAt"])
	require.Equal(t, "2026-07-08T12:00:00Z", v["freshUntil"], "still ahead of now — arms the one-shot @at")
	require.Equal(t, false, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "staleAt is present — nothing to backfill")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_OpenAndPastDue_Violating proves the gate opens
// once staleAt passes with the tab still open — the violating row itself
// drives dispatch from there, not a repeated timer wake-up.
func TestCafeStaleTabSettlement_OpenAndPastDue_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "pastduetab", "tab")
	f.aspect(t, "pastduetab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z",
	})

	v := f.valuesAtStale(t, "pastduetab", "2026-07-08T13:00:00Z")
	require.Nil(t, v["freshUntil"], "past the deadline — freshUntil goes null, the gap-dispatch path owns it now")
	require.Equal(t, true, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "staleAt is present — this is missing_settle's gap, not missing_staleat's")
	require.Equal(t, true, v["violating"])
}

// TestCafeStaleTabSettlement_Settled_NeverViolatesRegardlessOfStaleAt proves
// a legitimate staff Settle at any point permanently converges the gate:
// Settle's status_data rewrite drops staleAt entirely (ddls.go), and
// status='open' is the only terminal-state check this spec needs (unlike an
// appointment's three-way status, a tab is only ever open or settled).
func TestCafeStaleTabSettlement_Settled_NeverViolatesRegardlessOfStaleAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "settledstaletab", "tab")
	f.aspect(t, "settledstaletab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 850.0, "openedAt": "2026-07-07T12:00:00Z", "settledAt": "2026-07-07T13:00:00Z",
	})

	v := f.valuesAtStale(t, "settledstaletab", "2026-09-01T00:00:00Z")
	require.Nil(t, v["staleAt"], "Settle drops staleAt from the rewritten aspect")
	require.Nil(t, v["freshUntil"])
	require.Equal(t, false, v["missing_settle"])
	require.Equal(t, false, v["missing_staleat"], "settled — the status='open' guard excludes it from both gaps")
	require.Equal(t, false, v["violating"])
}

// TestCafeStaleTabSettlement_LegacyTabWithNoStaleAt_ViolatesViaMissingStaleat
// covers a tab seeded without staleAt (a tab opened before this field
// shipped, af451062, or any residual showcase data predating it): compareAny
// (full engine) treats a null operand as incomparable, so both '>' and '<='
// against $now resolve false for missing_settle — such a tab would be
// invisible to that gap alone, forever. missing_staleat is the dedicated
// gap that catches it instead, dispatching BackfillTabStaleAt (ddls.go) to
// compute the missing value so the NEXT cycle's missing_settle can see it.
func TestCafeStaleTabSettlement_LegacyTabWithNoStaleAt_ViolatesViaMissingStaleat(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.mkTab(t, "legacytab", "open", 500)

	v := f.valuesAtStale(t, "legacytab", "2026-12-01T00:00:00Z")
	require.Nil(t, v["staleAt"])
	require.Nil(t, v["freshUntil"])
	require.Equal(t, false, v["missing_settle"], "null staleAt compares false both ways — missing_settle alone never catches it")
	require.Equal(t, true, v["missing_staleat"], "an open tab with no staleAt at all is what this gap exists to catch")
	require.Equal(t, true, v["violating"])
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
// a null or a missing column: the staff boundary reads that as "no workplace
// covers this row" and denies, matching require_workplace's
// empty-location_keys denial. The comprehension's head is the matched lease,
// so this is also the vector that would catch it seeding the whole keyspace
// instead of binding empty.
func TestCafeLeaseWorkplaces_NoUnitEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newCdFixture(t)
	f.vtx(t, "lease", "leaseapp")

	rows := f.project(t, leaseWorkplacesSpec)
	require.Len(t, rows, 1, "an unwired lease must still project a row, so its denial is explicit")
	require.Empty(t, rows[0].Values["coveringLocations"],
		"an unwired lease is covered by nobody; the boundary must not read that as unrestricted")
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
