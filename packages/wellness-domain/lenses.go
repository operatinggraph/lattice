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

// Lenses returns the package's three flat projection lenses. No aggregation
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

// wellnessSessionsSpec projects one row per session, walking atStudio (0..1,
// so the row stays one-per-anchor — the §10.2 shape, mirroring
// clinicAppointmentsSpec's forPatient/withProvider walk). studioName is null
// when the studio link is absent (should not happen post-CreateSession, but
// the OPTIONAL keeps the lens null-safe rather than dropping the row).
// bookedCount is DELIBERATELY not projected here — the lens engine has no
// aggregate COUNT; a consuming FE derives it client-side from
// wellnessBookings, the same client-side aggregation idiom
// cmd/cafe-app's computeTabs already uses (see wellness-vertical-design.md).
const wellnessSessionsSpec = `MATCH (se:session)
OPTIONAL MATCH (se)-[:atStudio]->(s:studio)
RETURN
  se.key AS key,
  se.key AS sessionKey,
  se.schedule.data.name AS name,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.capacity AS capacity,
  s.key AS studioKey,
  s.profile.data.name AS studioName`

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
