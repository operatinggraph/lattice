package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WellnessStudiosBucket is the NATS-KV read model the wellnessStudios lens
// projects into — the **P5 query surface** for "which studios exist": the
// wellness FE reads THIS bucket (one row per named studio) to render the
// studio picker, never Core KV (lattice-architecture.md P5).
const WellnessStudiosBucket = "wellness-studios"

// WellnessSessionsBucket is the NATS-KV read model the wellnessSessions lens
// projects into — the **P5 query surface** for "what classes are scheduled":
// the schedule-grid view reads THIS bucket (one row per session, joined to
// its studio), never Core KV.
const WellnessSessionsBucket = "wellness-sessions"

// WellnessBookingsBucket is the NATS-KV read model the wellnessBookings lens
// projects into — the **P5 query surface** for "who booked what": the
// roster / my-classes views read THIS bucket (one row per booking, joined to
// its session), never Core KV.
const WellnessBookingsBucket = "wellness-bookings"

// WellnessInstructorsBucket is the NATS-KV read model the wellnessInstructors
// lens projects into — the **P5 query surface** for "who teaches here": the
// staff class-scheduling form reads THIS bucket to offer the instructor a
// class is `ledBy`, never Core KV.
const WellnessInstructorsBucket = "wellness-instructors"

// Lenses returns the package's four flat projection lenses. No aggregation
// (no WITH), so OPTIONAL-matched neighbour bindings are live directly in
// RETURN — the same §4-B1 no-WITH-drop shape clinic-domain's lenses use.
// None of these carry PHI/PII, so — unlike clinic-domain's patient/provider
// lenses — no protected Postgres/RLS layer is needed this increment.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "wellnessStudios",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessStudiosBucket,
			Engine:        "full",
			Spec:          wellnessStudiosSpec,
		},
		{
			CanonicalName: "wellnessSessions",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessSessionsBucket,
			Engine:        "full",
			Spec:          wellnessSessionsSpec,
		},
		{
			CanonicalName: "wellnessBookings",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessBookingsBucket,
			Engine:        "full",
			Spec:          wellnessBookingsSpec,
		},
		{
			CanonicalName: "wellnessInstructors",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessInstructorsBucket,
			Engine:        "full",
			Spec:          wellnessInstructorsSpec,
		},
	}
}

// wellnessStudiosSpec projects one row per NAMED studio — the studio picker.
// The WHERE keeps only studios carrying a .profile aspect (the
// availableListings/clinicProviders aspect-presence idiom). Per-row key is
// the studio key (the IntoKey default); studioKey repeats it in the body.
const wellnessStudiosSpec = `MATCH (s:studio)
WHERE s.profile.data.name <> null
RETURN
  s.key AS key,
  s.key AS studioKey,
  s.profile.data.name AS name`

// wellnessInstructorsSpec projects one row per NAMED instructor — the
// "who leads this class" picker on the staff scheduling form, mirroring
// wellnessStudiosSpec's aspect-presence WHERE. teachesAt (0..1) is projected
// so the form can offer the instructors attached to the studio being
// scheduled first.
//
// displayName is a professional name on an OPEN read model, the same posture
// wellnessSessions' instructorName carries and the same one clinic's public
// provider directory already sets — an instructor is listed so members can
// find their classes, unlike a patient or a booker, who stay bare keys.
const wellnessInstructorsSpec = `MATCH (i:instructor)
WHERE i.profile.data.displayName <> null
OPTIONAL MATCH (i)-[:teachesAt]->(s:studio)
RETURN
  i.key AS key,
  i.key AS instructorKey,
  i.profile.data.displayName AS displayName,
  s.key AS studioKey`

// wellnessSessionsSpec projects one row per session, walking atStudio and
// ledBy (each 0..1, so the row stays one-per-anchor — the §10.2 shape,
// mirroring clinicAppointmentsSpec's forPatient/withProvider walk).
// studioName is null when the studio link is absent (should not happen
// post-CreateSession, but the OPTIONAL keeps the lens null-safe rather than
// dropping the row); instructorKey/instructorName are null for the many
// sessions nobody leads, CreateSession's instructor param being optional.
//
// instructorKey is what scopes a bound instructor's own-roster read: their
// `identifiedBy` anchor names their instructor vertex, and this column is the
// only projection that answers which sessions that vertex leads.
// instructorName is the instructor's professional display name and is
// projected on this OPEN read model deliberately — a class instructor is the
// analog of clinic's provider directory, which stays public precisely while
// patient names moved behind the Protected lens.
//
// bookedCount is DELIBERATELY not projected here — the lens engine has no
// aggregate COUNT; a consuming FE derives it client-side from
// wellnessBookings, the same client-side aggregation idiom
// cmd/cafe-app's computeTabs already uses (see wellness-vertical-design.md).
//
// coveringLocations is the read-side half of workplace confinement
// (facet-staff-worlds-design.md §9): every location that COVERS this session —
// the studio's own `locatedAt` location plus each of that location's
// `containedIn` ancestors. A staff read boundary intersects it with the
// caller's `worksAt` keys, which is exactly what the write side's
// `worksAt_covers` computes by walking up from the location (ddls.go). The
// zero-hop lower bound is load-bearing: the depth-0 entry is the studio's own
// location, so a staffer wired to the exact room matches. The list-comprehension
// form is lease-signing's authz_anchors idiom (lease-signing/lenses.go), which
// keeps the row one-per-session — an OPTIONAL MATCH on a multi-location studio
// would fan the session into several rows instead. The upper bound is
// WORKPLACE_MAX_DEPTH - 1, not WORKPLACE_MAX_DEPTH, because the two sides count
// differently and the goal is that neither reaches a depth the other refuses:
// the Starlark walk runs `range(WORKPLACE_MAX_DEPTH)` testing depths 0..7,
// while `*0..N` here admits depths 0..N inclusive (the executor matches the
// zero-hop node and THEN runs hops 1..N). `*0..8` would therefore admit a
// staffer nine levels up whose writes require_workplace refuses. The location nodes carry no label because a location
// is any vertex of class `location` whatever its type segment — a building, a
// floor, a room — the same reason edge-manifest's workplace chains leave them
// bare; the labelled `(s:studio)` head is what keeps the comprehension anchored
// rather than seeding the whole keyspace.
const wellnessSessionsSpec = `MATCH (se:session)
OPTIONAL MATCH (se)-[:atStudio]->(s:studio)
OPTIONAL MATCH (se)-[:ledBy]->(i:instructor)
RETURN
  se.key AS key,
  se.key AS sessionKey,
  se.schedule.data.name AS name,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.capacity AS capacity,
  s.key AS studioKey,
  s.profile.data.name AS studioName,
  i.key AS instructorKey,
  i.profile.data.displayName AS instructorName,
  [(s)-[:locatedAt]->(pl)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

// wellnessBookingsSpec projects one row per booking, walking forSession and
// bookedBy (each 0..1). bookerKey (not a name) is projected — identity
// carries no display name of its own; a consuming FE scopes "my classes" by
// comparing bookerKey to the logged-in actor's own identity key, the same
// bare-key scoping cmd/loftspace-app's applicant views use.
const wellnessBookingsSpec = `MATCH (b:booking)
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS key,
  b.key AS bookingKey,
  b.status.data.value AS status,
  b.status.data.rate AS rate,
  se.key AS sessionKey,
  se.schedule.data.name AS sessionName,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  id.key AS bookerKey`
