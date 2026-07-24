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
	id := emNanoID(name)
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

	f.edge(t, "residesIn", "resident", "home")
	f.edge(t, "availableAt", "svcTpl", "home")
	f.edge(t, "permitsOperation", "svcTpl", "opMeta")
	f.edge(t, "assignedTo", "openTask", "resident")
	f.edge(t, "providedTo", "inst", "resident")
	f.edge(t, "locatedAt", "studio", "home")
	f.edge(t, "atStudio", "sess", "studio")
	f.edge(t, "practicesAt", "prov", "home")
	f.edge(t, "bookedBy", "booking", "resident")
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
			{"edgeServices", edgeServicesSpec},
			{"edgeCatalog", edgeCatalogSpec},
			{"edgeTasks", edgeTasksSpec},
			{"edgeInstances", edgeInstancesSpec},
			{"edgeEntitySessions", edgeEntitySessionsSpec},
			{"edgeEntityProviders", edgeEntityProvidersSpec},
			{"edgeEntityBookings", edgeEntityBookingsSpec},
		},
		[]string{edgeManifestReadGrantsSpec})
}

// --- Staff persona -----------------------------------------------------------
//
// Extends emStaffWorld (the worksAt/containedIn/locatedAt work-order spine) with
// the role-standing-grant reachability the two catalog/queue staff lenses need:
//
//	tech —holdsRole→ maintRole ←grantedBy— fixPerm —forOperation→ fixOp(meta)
//	tech —holdsRole→ maintRole ←queuedFor— queuedTask(task, status=open)
func emStaffWorldFull(t *testing.T) *emFixture {
	f := emStaffWorld(t)
	f.vtx(t, "maintRole", "role")
	f.vtx(t, "fixPerm", "permission")
	f.vtx(t, "fixOp", "meta")
	f.vtxData(t, "queuedTask", "task", map[string]any{"status": "open"})

	f.edge(t, "holdsRole", "tech", "maintRole")
	f.edge(t, "grantedBy", "fixPerm", "maintRole")
	f.edge(t, "forOperation", "fixPerm", "fixOp")
	f.edge(t, "queuedFor", "queuedTask", "maintRole")
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
			{"edgeStaffWorkOrders", edgeStaffWorkOrdersSpec},
			{"edgeCatalogRoles", edgeCatalogRolesSpec},
			{"edgeTasksQueued", edgeTasksQueuedSpec},
		},
		[]string{edgeManifestStaffReadGrantsSpec})
}
