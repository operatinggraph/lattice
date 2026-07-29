package edgemanifest

// Coverage proof for the read-grant / lens dual-enumeration seam (the "footgun"
// the package header names). A non-self-anchored Personal Lens projects a row
// keyed on some OTHER vertex (a service, op meta, task, session, provider,
// booking, work order…), and Refractor's D1 `readableAnchors` gate
// (internal/refractor/projection/personal.go's personalEnvelopeFn →
// capabilityread.IsReadable) SILENTLY drops that row unless a read-grant
// PRODUCER lens in the same package grants the anchor's bare NanoID. The data
// walk and the grant walk are hand-authored twice and nothing compiles one from
// the other, so a producer that forgets a slice — the exact shape of the Fire-1
// bug that left only edgeIdentity's self-anchor reaching a live tenant — fails
// closed with nothing reporting why.
//
// This is the Stage-1 half of the dual-enumeration hardening (lattice.md): a
// coverage proof that executes every data lens and every grant producer over a
// seeded topology and asserts every projected non-self anchor lands in the grant
// set. It deliberately does NOT derive the grants from the data lenses — that
// would make D1's gate a tautology and delete the security boundary; the two
// enumerations stay independent and this test proves they AGREE. (Stage 2, the
// single-source LensSpec declaration pkgmgr compiles both artifacts from, is a
// separate Designer item.)
//
// It generalizes TestStaffReadGrants_CoverTheWorkOrderAnchors from one anchor
// kind (workorder) to every kind a persona reaches, per persona world.

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/stretchr/testify/require"
)

// dataLens pairs a lens's canonical name with its cypher so a coverage gap names
// the offending lens.
type dataLens struct {
	name string
	spec string
}

// vtxData seeds a vertex like emFixture.vtx but with a non-empty root `data`
// map — needed for the task lenses' `task.data.status = "open"` filter, which
// reads the vertex root, not an aspect.
func (f *emFixture) vtxData(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// grantedAnchorIDs runs every producer lens for the actor and unions their
// readableAnchors into the set of granted bare NanoIDs. A branch whose OPTIONAL
// MATCH found nothing collects a placeholder element with a null anchorId; those
// are skipped, exactly as Refractor's IsReadable ignores them.
func (f *emFixture) grantedAnchorIDs(t *testing.T, actorKey string, producers []string) map[string]bool {
	t.Helper()
	granted := map[string]bool{}
	for _, p := range producers {
		for _, row := range f.project(t, p, actorKey) {
			anchors, _ := row.Values["readableAnchors"].([]any)
			for _, a := range anchors {
				m, ok := a.(map[string]any)
				if !ok {
					continue
				}
				if id, _ := m["anchorId"].(string); id != "" {
					granted[id] = true
				}
			}
		}
	}
	return granted
}

// assertAnchorsCovered is the coverage proof: every non-self anchor a data lens
// projects for the actor must be granted by some producer. It also asserts each
// data lens projected at least one row, so a topology that fails to exercise a
// lens fails loudly instead of passing vacuously (a coverage claim over an empty
// projection proves nothing).
func (f *emFixture) assertAnchorsCovered(t *testing.T, actorKey string, dataLenses []dataLens, producers []string) {
	t.Helper()
	granted := f.grantedAnchorIDs(t, actorKey, producers)
	for _, dl := range dataLenses {
		rows := f.project(t, dl.spec, actorKey)
		require.NotEmptyf(t, rows,
			"data lens %s projected no rows for the actor — the seeded topology does not exercise it, so its coverage claim is vacuous; fix the seed",
			dl.name)
		for _, row := range rows {
			id, _ := row.Values["entityId"].(string)
			require.NotEmptyf(t, id, "data lens %s row carries no entityId (the bare anchor NanoID D1 matches on)", dl.name)
			require.Truef(t, granted[id],
				"COVERAGE GAP: data lens %s projects anchor %s but NO read-grant producer grants it — Refractor's D1 gate would silently drop this row (the 'forgot the slice' dual-enumeration bug). Granted set: %v",
				dl.name, id, sortedKeys(granted))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- Resident (base) persona -------------------------------------------------
//
// A resident reaches the seven non-self anchor kinds edgeManifestReadGrants
// covers, each off the residence chain or the resident's own inbound links:
//
//	resident —residesIn→ home (= container, the *0.. zero-hop case)
//	  home ←availableAt— svcTpl(service) —permitsOperation→ opMeta(meta)
//	  home ←locatedAt— studio ←atStudio— sess(session)
//	  home ←practicesAt— prov(provider)
//	resident ←assignedTo— openTask(task, status=open)
//	resident ←providedTo— inst(service instance)
//	resident ←bookedBy— booking(booking)
//	resident ←applicationFor— lease(leaseapp) ←openFor— tab(tab, status=open)
//	  home ←servedAt— item(menuitem)
func emResidentWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)
	f.vtx(t, "resident", "identity")
	f.vtx(t, "home", "unit")
	f.vtx(t, "svcTpl", "service")
	f.vtx(t, "opMeta", "meta")
	f.vtxData(t, "openTask", "task", map[string]any{"status": "open"})
	f.vtx(t, "inst", "service")
	f.vtx(t, "studio", "studio")
	f.vtx(t, "sess", "session")
	f.vtx(t, "prov", "provider")
	f.vtx(t, "booking", "booking")
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "tab", "tab")
	f.vtx(t, "item", "menuitem")

	f.aspect(t, "home", "presentation", "locationPresentation", map[string]any{"name": "Unit 1"})
	f.aspect(t, "tab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 450, "openedAt": "2026-07-26T09:00:00Z"})
	f.aspect(t, "item", "price", "menuItemPrice", map[string]any{"name": "Latte", "priceCents": 450})

	f.edge(t, "residesIn", "resident", "home")
	f.edge(t, "availableAt", "svcTpl", "home")
	f.edge(t, "permitsOperation", "svcTpl", "opMeta")
	f.edge(t, "assignedTo", "openTask", "resident")
	f.edge(t, "providedTo", "inst", "resident")
	f.edge(t, "locatedAt", "studio", "home")
	f.edge(t, "atStudio", "sess", "studio")
	f.edge(t, "practicesAt", "prov", "home")
	f.edge(t, "bookedBy", "booking", "resident")
	f.edge(t, "applicationFor", "lease", "resident")
	f.edge(t, "appliesToUnit", "lease", "home")
	f.edge(t, "openFor", "tab", "lease")
	f.edge(t, "servedAt", "item", "home")
	return f
}

// TestManifestAnchorCoverage_ResidentWorld proves edgeManifestReadGrants covers
// every non-self anchor the seven base Personal lenses project — the slice whose
// Fire-1 omission left edgeServices/edgeCatalog/edgeTasks/edgeInstances invisible
// on a live tenant.
func TestManifestAnchorCoverage_ResidentWorld(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := emResidentWorld(t)
	f.assertAnchorsCovered(t, f.key("resident"),
		[]dataLens{
			{"edgeServices", emComposedSpec(t, "edgeServices")},
			{"edgeCatalog", emComposedSpecBranch(t, "edgeCatalog", 0)},
			{"edgeTasks", emComposedSpec(t, "edgeTasks")},
			{"edgeInstances", emComposedSpec(t, "edgeInstances")},
			{"edgeEntitySessions", emComposedSpecBranch(t, "edgeEntitySessions", 0)},
			{"edgeEntityProviders", emComposedSpec(t, "edgeEntityProviders")},
			{"edgeEntityBookings", emComposedSpec(t, "edgeEntityBookings")},
			{"edgeEntityTabs", emComposedSpec(t, "edgeEntityTabs")},
			{"edgeEntityMenuItems", emComposedSpec(t, "edgeEntityMenuItems")},
		},
		[]string{emComposedSpec(t, "edgeManifestReadGrants")})
}

// TestEdgeEntityTabs_ProjectsOnlyTheActorsOwnOpenTab pins the two things the
// walk decides: the row set is the actor's OWN lease's tabs (not locality —
// a neighbour's bar tab is nobody's business), and a settled tab stops being
// offered as a charge target while the grant that reaches it is unchanged.
func TestEdgeEntityTabs_ProjectsOnlyTheActorsOwnOpenTab(t *testing.T) {
	f := emResidentWorld(t)
	f.vtx(t, "neighbour", "identity")
	f.vtx(t, "otherLease", "leaseapp")
	f.vtx(t, "otherTab", "tab")
	f.aspect(t, "otherTab", "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 900, "openedAt": "2026-07-26T09:30:00Z"})
	f.edge(t, "applicationFor", "otherLease", "neighbour")
	f.edge(t, "openFor", "otherTab", "otherLease")

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeEntityTabs"), f.key("resident")))
	require.Len(t, rows, 1, "only the actor's own lease's open tab projects")
	own, ok := rows[f.ids["tab"]]
	require.True(t, ok, "the actor's own open tab must project")
	require.Equal(t, "tab", own["entityType"])
	require.Equal(t, f.key("tab"), own["entityKey"])
	require.Equal(t, "Unit 1", own["title"], "the lease's unit names the tab, which has no name of its own")

	f.aspect(t, "tab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 450, "openedAt": "2026-07-26T09:00:00Z"})
	settled := f.project(t, emComposedSpec(t, "edgeEntityTabs"), f.key("resident"))
	require.Empty(t, settled, "a settled tab is no longer a charge target")
}

// TestEdgeEntityTabs_GrantIsBoundedByTheOpenForHop is the grant-side half of
// the row set the test above pins, and the reason a resident's cap-read slice
// does not grow with every tab they ever open.
//
// A walk chain is node patterns only, so this producer can never read the
// `.status` aspect the presentation tail filters on. The narrowing is
// structural instead: `Settle` tombstones the tab's `openFor` link
// (cafe-domain/ddls.go), which is the only hop this walk traverses, so a
// settled tab leaves the granted set by losing its edge rather than by failing
// a filter that does not exist here. The fixture seeds exactly that shape — a
// settled tab with no `openFor` edge — and the open tab beside it proves the
// walk still reaches what it should.
func TestEdgeEntityTabs_GrantIsBoundedByTheOpenForHop(t *testing.T) {
	f := emResidentWorld(t)
	f.vtx(t, "settledTab", "tab")
	f.aspect(t, "settledTab", "status", "tabStatus", map[string]any{
		"value": "settled", "totalCents": 450,
		"openedAt": "2026-07-26T08:00:00Z", "settledAt": "2026-07-26T08:45:00Z"})
	// Post-Settle shape: same lease as the open tab, chargedTo standing (which
	// this walk does not traverse), openFor gone.
	f.edge(t, "chargedTo", "settledTab", "lease")

	granted := f.grantedAnchorIDs(t, f.key("resident"), []string{emComposedSpec(t, "edgeManifestReadGrants")})
	require.True(t, granted[f.ids["tab"]], "the open tab must still be granted")
	require.False(t, granted[f.ids["settledTab"]],
		"a settled tab must leave the grant — otherwise the slice grows with the lease's lifetime tab count")
}

// TestEdgeEntityMenuItems_IsBoundedByTheResidenceChain pins the two things the
// walk decides. A catalog is locality-scoped, not private — every item served
// where the actor lives projects, which is what makes it a pickable set — and
// an item served somewhere the actor's residence chain does not reach does NOT,
// even though a menu is otherwise public information. The bound is the walk's,
// so widening it would take a link, not a filter.
func TestEdgeEntityMenuItems_IsBoundedByTheResidenceChain(t *testing.T) {
	f := emResidentWorld(t)
	f.vtx(t, "elsewhere", "unit")
	f.vtx(t, "otherItem", "menuitem")
	f.aspect(t, "otherItem", "price", "menuItemPrice", map[string]any{"name": "Flat White", "priceCents": 400})
	f.edge(t, "servedAt", "otherItem", "elsewhere")

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeEntityMenuItems"), f.key("resident")))
	require.Len(t, rows, 1, "only items served where the actor lives project")
	own, ok := rows[f.ids["item"]]
	require.True(t, ok, "an item served at the actor's own home must project")
	require.Equal(t, "menuitem", own["entityType"])
	require.Equal(t, f.key("item"), own["entityKey"])
	require.Equal(t, "Latte", own["title"], "the item's own .price aspect names it")
	require.Equal(t, "Unit 1", own["subtitle"], "the serving place names whose menu this is")
	require.EqualValues(t, 450, own["priceCents"], "priceCents rides along; the renderer formats any *Cents column as money")
}

// TestEdgeEntityMenuItems_DropsARetiredItem proves the tombstone path needs no
// filter in the tail: a retired item leaves the MATCH, the same way menuCatalog
// relies on (cafe-domain/lenses.go). Without this the picker would keep
// offering an item Charge then rejects with UnknownMenuItem.
func TestEdgeEntityMenuItems_DropsARetiredItem(t *testing.T) {
	f := emResidentWorld(t)
	f.tombstone(t, "item")

	rows := f.project(t, emComposedSpec(t, "edgeEntityMenuItems"), f.key("resident"))
	require.Empty(t, rows, "a retired catalog item stops being offered")
}

// --- Staff persona -----------------------------------------------------------
//
// Extends emStaffWorld (the worksAt/containedIn/locatedAt work-order spine) with
// the role-standing-grant reachability the two catalog/queue staff lenses need:
//
//	tech —holdsRole→ maintRole ←grantedBy— fixPerm —forOperation→ fixOp(meta)
//	tech —holdsRole→ maintRole ←queuedFor— queuedTask(task, status=open)
//	tech —worksAt→ bldgA ←locatedAt— studioA (the workplace-spine studio)
func emStaffWorldFull(t *testing.T) *emFixture {
	f := emStaffWorld(t)
	f.vtx(t, "maintRole", "role")
	f.vtx(t, "fixPerm", "permission")
	f.vtx(t, "fixOp", "meta")
	f.vtxData(t, "queuedTask", "task", map[string]any{"status": "open"})
	f.vtx(t, "studioA", "studio")

	f.aspect(t, "studioA", "profile", "studioProfile", map[string]any{"name": "The Loft"})

	f.edge(t, "holdsRole", "tech", "maintRole")
	f.edge(t, "grantedBy", "fixPerm", "maintRole")
	f.edge(t, "forOperation", "fixPerm", "fixOp")
	f.edge(t, "queuedFor", "queuedTask", "maintRole")
	f.edge(t, "locatedAt", "studioA", "bldgA")
	return f
}

// TestManifestAnchorCoverage_StaffWorld generalizes
// TestStaffReadGrants_CoverTheWorkOrderAnchors from the workorder slice to all
// three staff anchor kinds (role-granted op metas, role-queued tasks, workplace
// work orders), asserting edgeManifestStaffReadGrants covers every one.
func TestManifestAnchorCoverage_StaffWorld(t *testing.T) {
	f := emStaffWorldFull(t)
	f.assertAnchorsCovered(t, f.key("tech"),
		[]dataLens{
			{"edgeStaffWorkOrders", emComposedSpec(t, "edgeStaffWorkOrders")},
			{"edgeCatalog (role branch)", emComposedSpecBranch(t, "edgeCatalog", 1)},
			{"edgeTasksQueued", emComposedSpec(t, "edgeTasksQueued")},
			{"edgeEntityStudios", emComposedSpec(t, "edgeEntityStudios")},
		},
		[]string{emComposedSpec(t, "edgeManifestStaffReadGrants")})
}

// TestEdgeEntityStudios_IsBoundedByTheWorkplace pins that the studio browse
// rows follow the WORKPLACE spine rather than residence: CreateSession's own
// script confines a front-desk caller to a studio in a building they work at,
// so projecting studios anywhere else would offer a target the Processor then
// refuses.
func TestEdgeEntityStudios_IsBoundedByTheWorkplace(t *testing.T) {
	f := emStaffWorldFull(t)
	f.vtx(t, "studioB", "studio")
	f.aspect(t, "studioB", "profile", "studioProfile", map[string]any{"name": "Elsewhere Studio"})
	f.edge(t, "locatedAt", "studioB", "bldgB")

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeEntityStudios"), f.key("tech")))
	require.Len(t, rows, 1, "only studios at the actor's own workplace project")
	own, ok := rows[f.ids["studioA"]]
	require.True(t, ok, "the studio at the workplace must project")
	require.Equal(t, "studio", own["entityType"])
	require.Equal(t, "The Loft", own["title"])
	require.Equal(t, "Riverside Building", own["subtitle"], "a multi-building staff actor needs to see which studio is which")
}

// TestEdgeEntityStudios_WalksDownFromTheWorkplace covers the same inbound
// variable-length hop edgeStaffWorkOrders depends on: a studio inside a UNIT
// of the workplace, not directly at the building, must still project.
func TestEdgeEntityStudios_WalksDownFromTheWorkplace(t *testing.T) {
	f := emStaffWorldFull(t)
	f.vtx(t, "studioInUnit", "studio")
	f.aspect(t, "studioInUnit", "profile", "studioProfile", map[string]any{"name": "Mezzanine"})
	f.edge(t, "locatedAt", "studioInUnit", "unitA1")

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeEntityStudios"), f.key("tech")))
	_, ok := rows[f.ids["studioInUnit"]]
	require.True(t, ok, "a studio at a unit inside the workplace must project (the *0.. inbound hop)")
}

// --- Provider persona --------------------------------------------------------
//
// A provider-hat identity reaches the three anchor kinds
// edgeManifestProviderReadGrants covers, each off the actor's OWN inbound
// identifiedBy binding to a role entity (a person wearing three provider hats):
//
//	providerId ←identifiedBy— prov(provider)      ←withProvider— appt(appointment)
//	providerId ←identifiedBy— sp(serviceprovider) ←providedBy—  tpl(service) ←instanceOf— inst(service)
//	providerId ←identifiedBy— instr(instructor)   ←ledBy—       sess(session)
func emProviderWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)
	f.vtx(t, "providerId", "identity")
	f.vtx(t, "prov", "provider")
	f.vtx(t, "appt", "appointment")
	f.vtx(t, "sp", "serviceprovider")
	f.vtx(t, "tpl", "service")
	f.vtx(t, "inst", "service")
	f.vtx(t, "instr", "instructor")
	f.vtx(t, "sess", "session")

	f.edge(t, "identifiedBy", "prov", "providerId")
	f.edge(t, "withProvider", "appt", "prov")
	f.edge(t, "identifiedBy", "sp", "providerId")
	f.edge(t, "providedBy", "tpl", "sp")
	f.edge(t, "instanceOf", "inst", "tpl")
	f.edge(t, "identifiedBy", "instr", "providerId")
	f.edge(t, "ledBy", "sess", "instr")
	return f
}

// TestManifestAnchorCoverage_ProviderWorld asserts edgeManifestProviderReadGrants
// covers every anchor the three provider-hat lenses project (own appointments,
// own service queue, own led sessions — the last now edgeEntitySessions'
// domainProvider branch) — the slice persona-worlds Fire W0 added alongside
// its lenses so D1 would not silently drop every provider-hat row.
func TestManifestAnchorCoverage_ProviderWorld(t *testing.T) {
	f := emProviderWorld(t)
	f.assertAnchorsCovered(t, f.key("providerId"),
		[]dataLens{
			{"edgeProviderSchedule", emComposedSpec(t, "edgeProviderSchedule")},
			{"edgeProviderQueue", emComposedSpec(t, "edgeProviderQueue")},
			{"edgeEntitySessions (provider branch)", emComposedSpecBranch(t, "edgeEntitySessions", 1)},
		},
		[]string{emComposedSpec(t, "edgeManifestProviderReadGrants")})
}
