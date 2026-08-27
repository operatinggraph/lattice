package wellnessdomain

// Rule-engine proof of the wellness projection lenses (wellnessStudios,
// wellnessSessions, wellnessBookings, wellnessInstructors, wellnessMembers).
// These drive the lens specs through the `full` rule engine directly — the
// engine selected at activation via engine:"full" — against an embedded NATS
// Core/Adjacency KV, the same harness clinic-domain / lease-signing use for
// their lens cypher tests.
//
// What they prove that the unit/structure tests cannot:
//   - wellnessSessions is ONE ROW PER SESSION even with a studio linked
//     (0..1 = 1) — no fan-out, no output-key collision.
//   - the NEIGHBOUR aspect-hop resolves (s.profile.data.name off the
//     OPTIONAL-matched studio).
//   - wellnessBookings joins BOTH session and booker neighbours in one flat
//     row.
//   - wellnessStudios / a WHERE presence filter excludes a studio with no
//     .profile.
//   - both containment walks — a session's and a member's — reach exactly the
//     depths their write-side guard reaches, and project an EMPTY set rather
//     than going missing when the topology is unwired, so the read boundaries
//     that intersect them deny.

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

type wdFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newWdFixture(t *testing.T) *wdFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &wdFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *wdFixture) vtx(t *testing.T, name, typ string) string {
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

func (f *wdFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *wdFixture) edge(t *testing.T, name, fromName, toName string) {
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

// project runs a spec (no actor anchor — unanchored projections that
// seed-scan the graph, mirroring clinic-domain's lensFixture.project) and
// returns the rows.
func (f *wdFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "wellness lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func wdRowByKey(rows []ruleengine.ProjectionResult, key string) map[string]any {
	for _, r := range rows {
		if r.Values["key"] == key {
			return r.Values
		}
	}
	return nil
}

// tombstoneVtx marks a previously-seeded vertex isDeleted — TombstoneSession's
// own footprint (ddls.go's make_tombstone), so a test can put a booking's
// forSession target into exactly the state ReleaseOrphanedBooking's
// convergence target is meant to find.
func (f *wdFixture) tombstoneVtx(t *testing.T, name string) {
	t.Helper()
	id := f.ids[name]
	key := "vtx." + f.types[id] + "." + id
	body := map[string]any{"key": key, "class": f.types[id], "isDeleted": true, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// projectOrphanBookingAt runs the anchored wellnessOrphanedBookingSettlement
// spec for one booking, mirroring clinic-ledger's projectAt.
func (f *wdFixture) projectOrphanBookingAt(t *testing.T, bookingName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(orphanedBookingSettlementSpec)
	require.NoError(t, err, "wellnessOrphanedBookingSettlement cypher must parse on the full engine")
	bookingKey := "vtx." + f.types[f.ids[bookingName]] + "." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    bookingKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkOrphanBooking seeds a booking vtx + .status aspect (value, session anchor)
// and, when session is non-empty, forSession + a live session vertex — the
// booking-side shape CreateBooking's ddls.go writes.
func (f *wdFixture) mkOrphanBooking(t *testing.T, name, status, sessionName string) {
	t.Helper()
	f.vtx(t, name, "booking")
	statusData := map[string]any{"value": status}
	if sessionName != "" {
		statusData["session"] = "vtx." + f.types[f.ids[sessionName]] + "." + f.ids[sessionName]
		f.edge(t, "forSession", name, sessionName)
	}
	f.aspect(t, name, "status", "bookingStatus", statusData)
}

// TestWellnessStudios_RostersNamedStudios proves the studio picker projects
// one row per NAMED studio, excluding a studio with no .profile aspect (the
// WHERE presence filter), mirroring clinicProviders.
func TestWellnessStudios_RostersNamedStudios(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	namedKey := f.vtx(t, "sunrise", "studio")
	f.aspect(t, "sunrise", "profile", "studioProfile", map[string]any{"name": "Sunrise Yoga Room"})
	// A studio with NO .profile aspect must be excluded by the WHERE filter.
	f.vtx(t, "ghost", "studio")

	rows := f.project(t, wellnessStudiosSpec)
	require.Len(t, rows, 1, "only the named studio rosters; the profile-less one is filtered out")
	v := wdRowByKey(rows, namedKey)
	require.NotNil(t, v)
	require.Equal(t, namedKey, v["studioKey"])
	require.Equal(t, "Sunrise Yoga Room", v["name"])
}

// TestWellnessSessions_JoinsStudio proves the schedule-grid join: one row per
// session, with the neighbour aspect-hop (studioName) and anchor hops
// (startsAt/endsAt/capacity) resolved.
func TestWellnessSessions_JoinsStudio(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	sessKey := f.vtx(t, "flow", "session")
	studioKey := f.vtx(t, "sunrise", "studio")
	f.aspect(t, "sunrise", "profile", "studioProfile", map[string]any{"name": "Sunrise Yoga Room"})
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1, "exactly one row per session even with studio joined")
	v := rows[0].Values
	require.Equal(t, sessKey, v["key"])
	require.Equal(t, sessKey, v["sessionKey"])
	require.Equal(t, "Vinyasa Flow", v["name"])
	require.Equal(t, "2026-07-08T09:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-08T09:30:00Z", v["endsAt"])
	require.Equal(t, 20.0, v["capacity"])
	require.Equal(t, studioKey, v["studioKey"])
	require.Equal(t, "Sunrise Yoga Room", v["studioName"], "neighbour aspect-hop s.profile.data.name")
}

// TestWellnessSessions_NoStudioNullSafe proves a session with no resolvable
// studio link still projects one row, with null studio columns (null-safe by
// key-shape, the clinicAppointments OPTIONAL idiom).
func TestWellnessSessions_NoStudioNullSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "orphan", "session")
	f.aspect(t, "orphan", "schedule", "sessionSchedule", map[string]any{
		"name": "Orphan Class", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 5.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["studioKey"])
	require.Nil(t, rows[0].Values["studioName"])
}

// TestWellnessSessions_ProjectsPriceCents proves priceCents is projected off
// the .schedule aspect, same as capacity.
func TestWellnessSessions_ProjectsPriceCents(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "priced", "session")
	f.aspect(t, "priced", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0, "priceCents": 1500.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Equal(t, 1500.0, rows[0].Values["priceCents"])
}

// TestWellnessSessions_ProjectsResidentPriceCents proves residentPriceCents
// is projected off the .schedule aspect alongside priceCents — the FE gap
// this closes: neither the create nor reassign form could set/see a
// session's resident rate because this lens never carried the column
// (verticals.md "a wellness class's resident price can be charged but never
// set or seen").
func TestWellnessSessions_ProjectsResidentPriceCents(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "residentpriced", "session")
	f.aspect(t, "residentpriced", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
		"priceCents": 1500.0, "residentPriceCents": 1000.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Equal(t, 1000.0, rows[0].Values["residentPriceCents"])
}

// TestWellnessSessions_NoResidentPriceCentsNullSafe proves a session with no
// residentPriceCents projects a null column, not a decode error — the common
// case (most sessions declare no resident override).
func TestWellnessSessions_NoResidentPriceCentsNullSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "noresidentprice", "session")
	f.aspect(t, "noresidentprice", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0, "priceCents": 1500.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["residentPriceCents"])
}

// TestWellnessBookings_JoinsSessionAndBooker proves the roster / my-classes
// join: one row per booking, with both the session neighbour (sessionName,
// startsAt/endsAt) and booker neighbour (bookerKey) resolved.
func TestWellnessBookings_JoinsSessionAndBooker(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	bookingKey := f.vtx(t, "booking1", "booking")
	sessKey := f.vtx(t, "flow", "session")
	bookerKey := f.vtx(t, "alice", "identity")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.aspect(t, "booking1", "status", "bookingStatus", map[string]any{"value": "booked", "rate": "resident", "seat": 1.0})
	f.edge(t, "forSession", "booking1", "flow")
	f.edge(t, "bookedBy", "booking1", "alice")

	rows := f.project(t, wellnessBookingsSpec)
	require.Len(t, rows, 1, "exactly one row per booking even with session + booker joined")
	v := rows[0].Values
	require.Equal(t, bookingKey, v["key"])
	require.Equal(t, bookingKey, v["bookingKey"])
	require.Equal(t, "booked", v["status"])
	require.Equal(t, "resident", v["rate"])
	require.Equal(t, sessKey, v["sessionKey"])
	require.Equal(t, "Vinyasa Flow", v["sessionName"], "neighbour aspect-hop se.schedule.data.name")
	require.Equal(t, "2026-07-08T09:00:00Z", v["startsAt"])
	require.Equal(t, "2026-07-08T09:30:00Z", v["endsAt"])
	require.Equal(t, bookerKey, v["bookerKey"])
}

// TestWellnessBookings_ProjectsPriceCents proves priceCents is projected off
// the joined session's .schedule aspect, same as sessionName/startsAt/endsAt.
func TestWellnessBookings_ProjectsPriceCents(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "booking2", "booking")
	f.vtx(t, "priced2", "session")
	f.aspect(t, "priced2", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0, "priceCents": 1500.0,
	})
	f.aspect(t, "booking2", "status", "bookingStatus", map[string]any{"value": "booked", "rate": "standard", "seat": 1.0})
	f.edge(t, "forSession", "booking2", "priced2")

	rows := f.project(t, wellnessBookingsSpec)
	require.Len(t, rows, 1)
	require.Equal(t, 1500.0, rows[0].Values["priceCents"])
}

// TestWellnessBookings_ProjectsResidentPriceCents proves residentPriceCents
// is projected off the joined session's .schedule aspect too — cmd/wellness-app's
// computeBookings resolves this against the booking's own rate to show the
// member the price they'll actually be charged (bookings.go), the same
// resolution wellnessClassPriceSettlement's CASE WHEN performs server-side.
func TestWellnessBookings_ProjectsResidentPriceCents(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "booking3", "booking")
	f.vtx(t, "residentpriced2", "session")
	f.aspect(t, "residentpriced2", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
		"priceCents": 1500.0, "residentPriceCents": 1000.0,
	})
	f.aspect(t, "booking3", "status", "bookingStatus", map[string]any{"value": "booked", "rate": "resident", "seat": 1.0})
	f.edge(t, "forSession", "booking3", "residentpriced2")

	rows := f.project(t, wellnessBookingsSpec)
	require.Len(t, rows, 1)
	require.Equal(t, 1000.0, rows[0].Values["residentPriceCents"])
}

// TestWellnessSessions_JoinsInstructor proves the ledBy hop the instructor
// hat rests on: a session led by a bound instructor projects that
// instructor's key (which scopes their own-roster read) and display name
// (which the public schedule renders as "with …").
func TestWellnessSessions_JoinsInstructor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	sessKey := f.vtx(t, "flow", "session")
	f.vtx(t, "sunrise", "studio")
	f.aspect(t, "sunrise", "profile", "studioProfile", map[string]any{"name": "Sunrise Yoga Room"})
	instrKey := f.vtx(t, "sam", "instructor")
	f.aspect(t, "sam", "profile", "instructorProfile", map[string]any{"displayName": "Sam Okafor"})
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Evening Flow", "startsAt": "2026-07-08T18:00:00Z", "endsAt": "2026-07-08T19:00:00Z", "capacity": 12.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")
	f.edge(t, "ledBy", "flow", "sam")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1, "one row per session even with BOTH studio and instructor joined")
	v := rows[0].Values
	require.Equal(t, sessKey, v["sessionKey"])
	require.Equal(t, instrKey, v["instructorKey"])
	require.Equal(t, "Sam Okafor", v["instructorName"], "neighbour aspect-hop i.profile.data.displayName")
}

// TestWellnessSessions_NoInstructorNullSafe proves the common case — most
// classes have no instructor — still projects a row, with null instructor
// columns. The Go decode relies on this: a nil column lands as "" in a
// non-pointer string field, which is what the roster's ledBy check treats as
// "nobody leads this".
func TestWellnessSessions_NoInstructorNullSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "solo", "session")
	f.aspect(t, "solo", "schedule", "sessionSchedule", map[string]any{
		"name": "Unled Class", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 5.0,
	})

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["instructorKey"])
	require.Nil(t, rows[0].Values["instructorName"])
}

// TestWellnessInstructors_RostersNamedInstructors proves the scheduling
// form's picker source: named instructors only, with their teachesAt studio
// when they have one.
func TestWellnessInstructors_RostersNamedInstructors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	namedKey := f.vtx(t, "sam", "instructor")
	f.aspect(t, "sam", "profile", "instructorProfile", map[string]any{"displayName": "Sam Okafor"})
	studioKey := f.vtx(t, "sunrise", "studio")
	f.edge(t, "teachesAt", "sam", "sunrise")
	// An instructor with NO .profile aspect must be excluded by the WHERE.
	f.vtx(t, "ghost", "instructor")

	rows := f.project(t, wellnessInstructorsSpec)
	require.Len(t, rows, 1, "only the named instructor rosters; the profile-less one is filtered out")
	v := wdRowByKey(rows, namedKey)
	require.NotNil(t, v)
	require.Equal(t, namedKey, v["instructorKey"])
	require.Equal(t, "Sam Okafor", v["displayName"])
	require.Equal(t, studioKey, v["studioKey"])
}

// TestWellnessSessions_CoveringLocations proves the read-side workplace term:
// a session's coveringLocations carries its studio's own location AND every
// containedIn ancestor, so a staff read boundary intersecting it with the
// caller's `worksAt` keys matches whether that staffer is wired to the exact
// room or to the building above it — the read-model mirror of the write side's
// worksAt_covers walk (facet-staff-worlds-design.md §9).
func TestWellnessSessions_CoveringLocations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "flow", "session")
	f.vtx(t, "sunrise", "studio")
	roomKey := f.vtx(t, "room3", "location")
	campusKey := f.vtx(t, "campus", "location")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")
	f.edge(t, "locatedAt", "sunrise", "room3")
	f.edge(t, "containedIn", "room3", "campus")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1, "the comprehension must not fan the session into one row per ancestor")
	covering := rows[0].Values["coveringLocations"]
	require.ElementsMatch(t, []any{roomKey, campusKey}, covering,
		"depth-0 (the studio's own room) and its containedIn ancestor both cover the session")
}

// TestWellnessSessions_NoLocationEmptyCovering proves a session whose studio
// sits nowhere projects an EMPTY covering set rather than a null or a missing
// column: the staff boundary reads that as "no workplace covers this row" and
// denies, matching require_workplace's empty-location_keys denial.
func TestWellnessSessions_NoLocationEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "flow", "session")
	f.vtx(t, "sunrise", "studio")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0].Values, "coveringLocations",
		"the column must project even when the set is empty")
	require.Empty(t, rows[0].Values["coveringLocations"],
		"an unwired studio covers nothing; the boundary must not read that as unrestricted")
}

// TestWellnessSessions_MultiLocationStudioUnionsBothChains proves a studio that
// sits at more than one place contributes BOTH chains to one row — the union
// the write side's studio_locations builds by enumerating every locatedAt link,
// and still one row per session rather than one per location.
func TestWellnessSessions_MultiLocationStudioUnionsBothChains(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "flow", "session")
	f.vtx(t, "sunrise", "studio")
	northKey := f.vtx(t, "north", "location")
	southKey := f.vtx(t, "south", "location")
	campusKey := f.vtx(t, "campus", "location")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")
	f.edge(t, "locatedAt", "sunrise", "north")
	f.edge(t, "locatedAt", "sunrise", "south")
	f.edge(t, "containedIn", "north", "campus")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1, "two locations must union into one row, not fan into two")
	require.ElementsMatch(t, []any{northKey, southKey, campusKey}, rows[0].Values["coveringLocations"],
		"both locatedAt branches and the ancestor of the one that has it")
}

// TestWellnessSessions_NoStudioEmptyCovering proves a session with no studio at
// all still projects one row with an EMPTY covering set. The comprehension's
// head is the OPTIONAL-matched studio, so this is the vector that would catch
// it seeding the whole keyspace instead of null-binding.
func TestWellnessSessions_NoStudioEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "orphan", "session")
	// A studio sitting somewhere, linked to NO session: if the comprehension
	// ever came unanchored it would pull this location in.
	f.vtx(t, "elsewhere", "studio")
	f.vtx(t, "faraway", "location")
	f.aspect(t, "orphan", "schedule", "sessionSchedule", map[string]any{
		"name": "Orphan Class", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 5.0,
	})
	f.edge(t, "locatedAt", "elsewhere", "faraway")

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.Empty(t, rows[0].Values["coveringLocations"],
		"a session with no studio is covered by nothing, not by every studio's location")
}

// TestWellnessSessions_HopBoundMatchesTheWriteSide pins the exact depth the
// covering set reaches. The write side walks `range(WORKPLACE_MAX_DEPTH)` = 8
// iterations testing depths 0..7, so the read side must admit depths 0..7 and
// NO further: `*0..8` would admit a staffer nine levels up whose writes
// require_workplace refuses — a read the write side would not have allowed.
// The two bounds are written in different languages with different counting
// conventions, which is why this needs pinning rather than reading.
func TestWellnessSessions_HopBoundMatchesTheWriteSide(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "flow", "session")
	f.vtx(t, "sunrise", "studio")
	roomKey := f.vtx(t, "room", "location")
	f.aspect(t, "flow", "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T09:30:00Z", "capacity": 20.0,
	})
	f.edge(t, "atStudio", "flow", "sunrise")
	f.edge(t, "locatedAt", "sunrise", "room")

	// room(0) -> a1(1) -> ... -> a8(8): one level deeper than either side reaches.
	want := []any{roomKey}
	prev := "room"
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("a%d", i)
		key := f.vtx(t, name, "location")
		f.edge(t, "containedIn", prev, name)
		if i <= 7 {
			want = append(want, key)
		}
		prev = name
	}

	rows := f.project(t, wellnessSessionsSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, want, rows[0].Values["coveringLocations"],
		"depths 0..7 cover the session and depth 8 does not — the write side's own reach")
}

// TestWellnessMembers_JoinsMemberAndCoveringLocations proves the front desk's
// picker source: one row per lease, carrying the member who holds it and the
// locations that cover them — the applied-to unit at depth 0 and its
// containedIn ancestors above — so a staffer wired to either the unit or the
// building matches on a set intersection.
func TestWellnessMembers_JoinsMemberAndCoveringLocations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	leaseKey := f.vtx(t, "lease", "leaseapp")
	memberKey := f.vtx(t, "member", "identity")
	unitKey := f.vtx(t, "unit12", "location")
	buildingKey := f.vtx(t, "building", "location")
	f.edge(t, "applicationFor", "lease", "member")
	f.edge(t, "appliesToUnit", "lease", "unit12")
	f.edge(t, "containedIn", "unit12", "building")

	rows := f.project(t, wellnessMembersSpec)
	require.Len(t, rows, 1, "the comprehension must not fan the lease into one row per ancestor")
	row := wdRowByKey(rows, leaseKey)
	require.Equal(t, leaseKey, row["leaseAppKey"])
	require.Equal(t, memberKey, row["bookerKey"], "a picker has to name a person, not only a lease")
	require.ElementsMatch(t, []any{unitKey, buildingKey}, row["coveringLocations"],
		"depth-0 (the member's own unit) and its containedIn ancestor both cover them")
}

// TestWellnessMembers_NoUnitEmptyCovering proves a lease with no applied-to
// unit projects an EMPTY covering set rather than a null or a missing column:
// the picker reads that as "no workplace reaches this member" and offers them
// to nobody, matching require_workplace's empty-location_keys denial.
func TestWellnessMembers_NoUnitEmptyCovering(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "member", "identity")
	f.edge(t, "applicationFor", "lease", "member")

	rows := f.project(t, wellnessMembersSpec)
	require.Len(t, rows, 1, "the row must project so an unwired lease DENIES rather than going missing")
	// Contains before Empty: require.Empty passes identically on an ABSENT map
	// key, so on its own it could not tell "projected empty" from "the column
	// is not projected at all" — which is the distinction this test is named
	// for.
	require.Contains(t, rows[0].Values, "coveringLocations",
		"the column must project even when the set is empty")
	require.Empty(t, rows[0].Values["coveringLocations"],
		"an unwired unit covers nobody; the picker must not read that as unrestricted")
}

// TestWellnessMembers_NoApplicantDropsRow proves the applicationFor match is
// REQUIRED, not optional: a lease with no applicant names no member, so it is
// not a row the picker could offer. This is the one column where absence drops
// the row rather than denying on it — coveringLocations must still project
// empty (the test above), because there an empty set is the denial.
func TestWellnessMembers_NoApplicantDropsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "unit12", "location")
	f.edge(t, "appliesToUnit", "lease", "unit12")

	rows := f.project(t, wellnessMembersSpec)
	require.Empty(t, rows, "a lease naming no applicant is not a member the picker can offer")
}

// TestWellnessMembers_DepthBoundMatchesWriteSide pins the containment reach to
// the write side's own: WORKPLACE_MAX_DEPTH - 1, because `*0..7` admits depths
// 0..7 inclusive while the Starlark walk runs range(WORKPLACE_MAX_DEPTH) over
// the same span. A staffer nine levels up must NOT be offered a member their
// writes could not reach.
func TestWellnessMembers_DepthBoundMatchesWriteSide(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "member", "identity")
	unitKey := f.vtx(t, "unit12", "location")
	f.edge(t, "applicationFor", "lease", "member")
	f.edge(t, "appliesToUnit", "lease", "unit12")

	// unit12(0) -> a1(1) -> ... -> a8(8): one level deeper than either side reaches.
	want := []any{unitKey}
	prev := "unit12"
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("a%d", i)
		key := f.vtx(t, name, "location")
		f.edge(t, "containedIn", prev, name)
		if i <= 7 {
			want = append(want, key)
		}
		prev = name
	}

	rows := f.project(t, wellnessMembersSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, want, rows[0].Values["coveringLocations"],
		"depths 0..7 cover the member and depth 8 does not — the write side's own reach")
}

// TestWellnessMembers_ProjectsTheLandlordDecision proves the column the read
// boundary drops a refused applicant on. It is projected, not filtered in the
// cypher, because it is three-state: an application still awaiting a landlord
// carries null here and stays in the directory, while only 'declined' is
// disqualifying. Both live states appear in one graph so neither can pass by
// the fixture being empty.
func TestWellnessMembers_ProjectsTheLandlordDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "unit12", "location")

	refusedLease := f.vtx(t, "refusedLease", "leaseapp")
	f.vtx(t, "refused", "identity")
	f.edge(t, "applicationFor", "refusedLease", "refused")
	f.edge(t, "appliesToUnit", "refusedLease", "unit12")
	f.aspect(t, "refusedLease", "decision", "decision", map[string]any{"value": "declined", "reason": "no"})

	pendingLease := f.vtx(t, "pendingLease", "leaseapp")
	f.vtx(t, "pending", "identity")
	f.edge(t, "applicationFor", "pendingLease", "pending")
	f.edge(t, "appliesToUnit", "pendingLease", "unit12")

	rows := f.project(t, wellnessMembersSpec)
	require.Len(t, rows, 2, "the lens carries both; the reader is what drops the refusal")
	require.Equal(t, "declined", wdRowByKey(rows, refusedLease)["landlordDecision"])
	require.Nil(t, wdRowByKey(rows, pendingLease)["landlordDecision"],
		"an undecided application projects null, not a decision the reader could mistake for one")
}

// envelopeData is an at-rest sensitive-aspect data map as step 6.5's
// encrypt-on-write commits it: base64 ct/nonce + the wrapping key id, no
// plaintext field — mirrors loftspace-domain/lens_cypher_test.go's helper of
// the same name.
func envelopeData() map[string]any {
	return map[string]any{"ct": "3q2+7w==", "nonce": "AAAAAAAAAAAAAAAA", "keyId": "k1"}
}

// TestWellnessIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors proves a
// named identity projects one row: the name column carries the ciphertext
// envelope MAP whole (for the Secure-Lens decryptor, never the engine), and
// authz_anchors carries exactly the identity's OWN bare NanoID — the
// self-anchor that lets the signed-in actor read their own row via the
// platform's base cap-read self-grant with no extra grant declaration
// (mirrors loftspace-domain's landlordUnitsRead self-anchor idiom, NOT
// applicantRosterRead's empty/wildcard-only set).
func TestWellnessIdentitiesRead_ProjectsEnvelopeWholeAndSelfAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	aliceKey := f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "name", "name", envelopeData())

	rows := f.project(t, wellnessIdentitiesReadSpec)
	require.Len(t, rows, 1, "exactly one roster row for the one named identity")
	v := rows[0].Values
	require.Equal(t, aliceKey, v["identity_key"], "identity_key names the row's owner for its consumers; the decryptor opens the row under the holder the ciphertext names")
	name, ok := v["name"].(map[string]any)
	require.True(t, ok, "name must be the ciphertext envelope map, got %T (%v)", v["name"], v["name"])
	require.Equal(t, "3q2+7w==", name["ct"], "the envelope reaches the decryptor whole")
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Len(t, anchors, 1, "the row is self-anchored on the identity's own bare NanoID, not empty/wildcard-only")
}

// TestWellnessIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities
// proves the ciphertext-presence WHERE: an identity with no .name aspect and
// an identity whose .name data is plaintext-shaped ({value}, no ct — a shape
// step 6.5 can never commit) both project NO row, so the lens can neither
// roster unnamed actors nor carry plaintext PII by itself.
func TestWellnessIdentitiesRead_ExcludesUnnamedAndPlaintextShapedIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "svc", "identity") // no .name at all
	f.vtx(t, "legacy", "identity")
	f.aspect(t, "legacy", "name", "name", map[string]any{"value": "Plain Text"})
	bobKey := f.vtx(t, "bob", "identity")
	f.aspect(t, "bob", "name", "name", envelopeData())

	rows := f.project(t, wellnessIdentitiesReadSpec)
	require.Len(t, rows, 1, "only the ciphertext-named identity projects")
	require.Equal(t, bobKey, rows[0].Values["identity_key"])
}

// TestWellnessIdentitiesRead_WorkplaceAnchorFanOut proves a member identity's
// authz_anchors carries every building that covers their own lease's unit —
// the wellness console gap: a worksAt-anchored staffer's cap-read.staff grant
// token is one of these building keys, not the member's own NanoID, so
// without this fan-out a real front-desk/instructor actor matched no row but
// its own. Mirrors TestWellnessMembers_JoinsMemberAndCoveringLocations'
// containedIn chain, walked from the identity side via applicationFor
// (leaseapp -> identity, Contract #1 §1.1: the later-arriving leaseapp is the
// source) — the same fan-out cafe-domain's cafeIdentitiesRead already proved
// for its own front desk.
func TestWellnessIdentitiesRead_WorkplaceAnchorFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	aliceKey := f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "name", "name", envelopeData())
	f.vtx(t, "lease", "leaseapp")
	f.vtx(t, "unit4b", "location")
	f.vtx(t, "building", "location")
	f.edge(t, "applicationFor", "lease", "alice")
	f.edge(t, "appliesToUnit", "lease", "unit4b")
	f.edge(t, "containedIn", "unit4b", "building")

	rows := f.project(t, wellnessIdentitiesReadSpec)
	require.Len(t, rows, 1)
	require.Equal(t, aliceKey, rows[0].Values["identity_key"])
	require.ElementsMatch(t, []any{f.ids["alice"], f.ids["unit4b"], f.ids["building"]}, rows[0].Values["authz_anchors"],
		"authz_anchors must carry the self-anchor PLUS the bare NanoID of the unit and every building covering it")
}

// TestWellnessIdentitiesRead_NoLeaseKeepsSelfAnchorOnly proves an identity
// with no leaseapp application at all (e.g. an instructor or staffer with no
// residence of their own) still projects — the self-anchor survives on its
// own, and the variable-length walk finding no leaseapp yields an empty
// fan-out rather than dropping the row or erroring, the same posture
// wellnessMembersSpec's own *0.. hop takes for an unwired lease
// (TestWellnessMembers_NoUnitEmptyCovering).
func TestWellnessIdentitiesRead_NoLeaseKeepsSelfAnchorOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "staffonly", "identity")
	f.aspect(t, "staffonly", "name", "name", envelopeData())

	rows := f.project(t, wellnessIdentitiesReadSpec)
	require.Len(t, rows, 1)
	require.ElementsMatch(t, []any{f.ids["staffonly"]}, rows[0].Values["authz_anchors"],
		"no lease application means no fan-out, but the self-anchor must still be present")
}

// TestWellnessOrphanedBookingSettlement_BookedLiveSession_NotViolating proves
// a booking on a session that hasn't been called off never violates — the
// common case, the positive vector proving the gap doesn't fire spuriously.
func TestWellnessOrphanedBookingSettlement_BookedLiveSession_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "livesess", "session")
	f.mkOrphanBooking(t, "livebooking", "booked", "livesess")

	v := f.projectOrphanBookingAt(t, "livebooking")[0].Values
	require.Equal(t, "booked", v["status"])
	require.NotNil(t, v["sessionKey"], "the .status.session anchor is present")
	require.Equal(t, false, v["missing_release"], "the session is still live — nothing to release")
	require.Equal(t, false, v["violating"])
}

// TestWellnessOrphanedBookingSettlement_BookedSessionTombstoned_MissingRelease
// proves the actual gap: TombstoneSession leaves the session vertex dead but
// the forSession link untouched, so the OPTIONAL MATCH to a LIVE session
// drops (se null) while .status.session (read straight off the booking's own
// aspect, not walked) still names it — exactly the condition
// ReleaseOrphanedBooking exists to drain.
func TestWellnessOrphanedBookingSettlement_BookedSessionTombstoned_MissingRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "deadsess", "session")
	f.mkOrphanBooking(t, "orphanbooking", "booked", "deadsess")
	f.tombstoneVtx(t, "deadsess")

	v := f.projectOrphanBookingAt(t, "orphanbooking")[0].Values
	require.Equal(t, "booked", v["status"])
	require.Equal(t, "vtx.session."+f.ids["deadsess"], v["sessionKey"], "the anchor survives the session's own tombstone")
	require.Equal(t, true, v["missing_release"], "still booked, session anchor present, session no longer live")
	require.Equal(t, true, v["violating"])
	requireIntColumn(t, v, "maxretries_release", maxReleaseRetries)
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

// TestWellnessOrphanedBookingSettlement_AttendedSessionTombstoned_NotViolating
// proves the gap is scoped to `booked` only: a booking already marked
// attended/noShow before (or after) its session is called off is
// SetBookingAttendance's/history's record, not a leak this convergence lens
// should touch.
func TestWellnessOrphanedBookingSettlement_AttendedSessionTombstoned_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.vtx(t, "pastsess", "session")
	f.mkOrphanBooking(t, "attendedbooking", "attended", "pastsess")
	f.tombstoneVtx(t, "pastsess")

	v := f.projectOrphanBookingAt(t, "attendedbooking")[0].Values
	require.Equal(t, "attended", v["status"])
	require.Equal(t, false, v["missing_release"], "not `booked` — SetBookingAttendance already owns this booking's history")
	require.Equal(t, false, v["violating"])
}

// TestWellnessOrphanedBookingSettlement_NoSessionAnchor_NotViolating proves
// the null-guard on sessionKey: a legacy booking created before CreateBooking
// stamped .status.session (or any malformed row missing the anchor) never
// violates rather than crashing ReleaseOrphanedBooking on a nil session.
func TestWellnessOrphanedBookingSettlement_NoSessionAnchor_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWdFixture(t)
	f.mkOrphanBooking(t, "legacybooking", "booked", "")

	v := f.projectOrphanBookingAt(t, "legacybooking")[0].Values
	require.Equal(t, "booked", v["status"])
	require.Nil(t, v["sessionKey"], "no session anchor stamped")
	require.Equal(t, false, v["missing_release"], "no anchor to release against")
	require.Equal(t, false, v["violating"])
}
