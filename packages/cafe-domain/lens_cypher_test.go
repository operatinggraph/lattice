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

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func cdCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natsfixture.Server(t)
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
