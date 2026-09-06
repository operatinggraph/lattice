package wellnessdomain

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

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

// WellnessMembersBucket is the NATS-KV read model the wellnessMembers lens
// projects into — the **P5 query surface** for the one question the front
// desk's book-a-member picker has to answer before offering a name: "does this
// staffer's workplace reach this member". One row per lease, carrying the
// member and the set of locations that COVER them; the wellness app reads THIS
// bucket, never Core KV. The Refractor auto-creates the bucket on lens load.
const WellnessMembersBucket = "wellness-members"

// WellnessBookersBucket is the NATS-KV read model the wellnessBookers lens
// projects into — the **P5 query surface** for the guest half of the question
// WellnessMembersBucket answers for tenants: "does this staffer's workplace
// reach this booker". One row per live booking, carrying the person who made
// it and the set of locations that COVER the class they booked; the wellness
// app reads THIS bucket, never Core KV. The Refractor auto-creates the bucket
// on lens load.
const WellnessBookersBucket = "wellness-bookers"

// OrphanedBookingSettlementTarget is the §10.8 TargetID ==
// wellnessOrphanedBookingSettlement's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads (targets.go).
const OrphanedBookingSettlementTarget = "wellnessOrphanedBookingSettlement"

// WaitlistPromotionTarget is the §10.8 TargetID ==
// wellnessWaitlistPromotion's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads (targets.go).
const WaitlistPromotionTarget = "wellnessWaitlistPromotion"

// Lenses returns the package's six flat projection lenses,
// wellnessIdentitiesRead (the one protected Postgres/RLS layer this package
// carries), and two convergence lenses targets.go's WeaverTargets dispatches
// over: wellnessOrphanedBookingSettlement (missing_release →
// ReleaseOrphanedBooking) and wellnessWaitlistPromotion (missing_promotion →
// PromoteWaitlistedBookings). No aggregation (no WITH) on the six flat
// ones, so OPTIONAL-matched neighbour bindings are live directly in RETURN —
// the same §4-B1 no-WITH-drop shape clinic-domain's lenses use.
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
		{
			CanonicalName: "wellnessMembers",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessMembersBucket,
			Engine:        "full",
			Spec:          wellnessMembersSpec,
		},
		{
			CanonicalName: "wellnessBookers",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        WellnessBookersBucket,
			Engine:        "full",
			Spec:          wellnessBookersSpec,
		},
		{
			// wellnessIdentitiesRead — the protected Postgres identity-name
			// lens closing the cross-vertical "Signed in as <NanoID>" gap
			// (verticals.md): wellness-app has no roster of named identities
			// to resolve the signed-in actor's own name against, so it falls
			// back to printing the raw key. NAME ONLY, mirroring
			// loftspace-domain's applicantRosterRead SECURE LENS (Contract #3
			// §3.10) — the identity `name` is a sensitive aspect, so Core KV
			// holds only its ciphertext envelope, and the cypher RETURNs the
			// envelope whole for Refractor to decrypt at projection time.
			//
			// SELF-ANCHORED, unlike applicantRosterRead's empty/wildcard-only
			// set: each row's authz_anchors carries the identity's OWN bare
			// NanoID, so the platform's base cap-read self-grant (every
			// actor's actor_id==anchor_id=='s own key) lets a signed-in
			// member, instructor, or staffer read their OWN row with no extra
			// grant declaration — the landlordUnitsRead idiom
			// (loftspace-domain/lenses.go), not clinic's indirect two-lens
			// patientIdentityReadGrants (there is no business vertex between
			// the row and the login identity to route through — the anchor
			// IS the identity). Each row ALSO carries every workplace
			// building that covers the identity's own lease
			// (applicationFor -> appliesToUnit -> containedIn*0..7, the same
			// walk wellnessMembersSpec's own coveringLocations column runs
			// and cafe-domain's cafeIdentitiesRead precedent), so a
			// worksAt-anchored front-desk/instructor actor (permissions.go's
			// worksAt_covers confinement) can resolve the name of every
			// member whose lease their workplace covers, not only
			// themselves. A staffer holding the reserved WildcardAnchor
			// grant still reads every row.
			CanonicalName: "wellnessIdentitiesRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_wellness_identities",
			Engine:        "full",
			Spec:          wellnessIdentitiesReadSpec,
			Protected:     true,
			IntoKey:       []string{"identity_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "identity_key", Type: "text"},
				{Name: "name", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "name", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			CanonicalName:  OrphanedBookingSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           orphanedBookingSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: OrphanedBookingSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_release", "entityKey", "bookingKey", "sessionKey", "status", "maxretries_release"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  WaitlistPromotionTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           waitlistPromotionSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "session",
				OutputKeyPattern: WaitlistPromotionTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_promotion", "entityKey", "sessionKey", "startsAt", "capacity", "seatedCount", "waitlistedCount", "freshUntil", "maxretries_promotion"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
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

// wellnessMembersSpec projects one row per lease carrying the member who holds
// it and coveringLocations — every location that COVERS them: the applied-to
// unit plus each of that unit's `containedIn` ancestors. It is the source of
// the front desk's book-a-member picker: a staffer is offered the members whose
// building they work at, intersecting this set with their own `worksAt` keys,
// which is the same answer `worksAt_covers` computes by walking upward from a
// location (ddls.go). Mirrors cafe-domain's `cafeLeaseWorkplaces` — the read
// side of workplace confinement, keyed on the lease — with the applicant column
// added, because a picker has to name a person and not merely a lease.
//
// The picker is an AFFORDANCE, not the authority. `CreateBooking` confines a
// front-desk caller by the SESSION's location — never by who the booker is
// (permissions.go, and `require_workplace(session_locations(session), …)` in
// ddls.go) — so what this lens decides is who a staffer is OFFERED, and the
// script still decides what they may write. Narrowing it is a read boundary:
// republishing the whole building's members to a staffer at another building
// would undo the boundary the resident-directory deletion drew.
//
// applicationFor is a REQUIRED match (lease-signing's own
// leaseApplicationSummary idiom, lenses.go): every leaseapp writes that link at
// CreateLeaseApplication, and a row with no member is a row this picker cannot
// offer — unlike coveringLocations, where an empty set is meaningful and must
// project. That set stays projected for every matched lease precisely because an
// absent row and an empty set have to deny alike: an unwired unit is covered by
// nobody, the same answer require_workplace gives an empty location list.
//
// `landlordDecision` is projected so the boundary can drop the REJECTED.
// `DecideLeaseApplication` tombstones neither the leaseapp nor its
// applicationFor link on a decline (lease-signing/scripts.go), so a refused
// applicant keeps a live link to the building that refused them — publishing
// them to that building's front desk as a member is the one disclosure this
// directory must not make. It is projected rather than filtered in the cypher
// because the column is THREE-state — approved, declined, and the null of an
// undecided application — and only the first two are decidable against a
// literal; the reader drops `declined` and keeps the rest, which is the
// café front-desk directory's own posture (cafeLeaseWorkplaces filters no
// decision at all) minus the refusal.
//
// Deliberately NOT filtered on `.tenancy`, though that is the stricter signal
// and the one CreateBooking reads for the resident RATE. A rate is a claim
// about money and belongs on proof of tenancy; a directory is a claim about
// who is around, and a signed-but-undecided applicant living in the building is
// exactly who the front desk books in. The rate still answers separately — the
// picker passes the lease and CreateBooking re-derives it — so the strict
// signal keeps its job without emptying the directory of everyone waiting on a
// landlord. A member with no lease at all is not offerable here: the directory
// is lease-anchored by construction, as the resident directory it replaces was.
//
// Bounds match wellnessSessionsSpec's above for the same reason: zero-hop is
// load-bearing (a staffer wired to the exact unit matches, not only one wired to
// the building), and the upper bound is WORKPLACE_MAX_DEPTH - 1 because `*0..N`
// admits depths 0..N inclusive while the Starlark walk tests 0..7 — `*0..8`
// would offer a staffer nine levels up members their writes cannot reach. The
// list-comprehension form (lease-signing's authz_anchors idiom) keeps the row
// one-per-lease; an OPTIONAL MATCH on a multi-parent unit would fan it out
// instead. A member holding two leases is deliberately two rows: the lease is
// what carries both the covering location and the resident-rate hint the picker
// passes to CreateBooking.
const wellnessMembersSpec = `MATCH (l:leaseapp)
MATCH (l)-[:applicationFor]->(id:identity)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  id.key AS bookerKey,
  l.decision.data.value AS landlordDecision,
  [(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

// wellnessBookersSpec projects one row per LIVE booking, in any status,
// carrying the person who made it and the locations that cover the class they
// booked. It is the guest half of the front desk's money surfaces: every one of
// them (the billing picker, the arrears grid, the ledger's own visibility
// check) confines through a member directory, and wellnessMembersSpec above is
// lease-anchored by construction — so a booker who walked in without a lease is
// billed for their class and then invisible to the desk that has to settle it.
// A booking is the standing relationship a guest DOES have, so it anchors this
// one, and their coverage is the building of the class they booked: the same
// reach the staffer's own worksAt grant already gives them over that booking.
//
// Anchored on `bk:booking` and keyed on `bk.key`, never on `id.key` — the row
// key is what the read model partitions on, and keying a booking-anchored row
// on the booker would make two bookings by one person collide on a single
// output key, each rewriting the other's coverage. One person with two bookings
// is deliberately two rows, the same one-row-per-anchor posture wellnessMembers
// keeps for a member holding two leases; the reader dedupes by bookerKey.
//
// `bookedBy` is a REQUIRED match, wellnessMembers' applicationFor idiom: every
// booking writes that link atomically at CreateBooking/JoinWaitlist (ddls.go),
// and a row naming no person is not one a desk could offer or bill. The
// coverage comprehension is OPTIONAL by shape and must project EMPTY rather
// than drop the row — a booking whose session was called off (TombstoneSession
// leaves the booking alive, package.go's no-cascade doctrine) walks to nothing,
// and an empty set is the denial every workplace intersection here reads.
//
// The walk is `forSession -> atLocation -> containedIn*0..7`, the same one
// wellnessIdentitiesReadSpec's booking fan-out runs. `atLocation` is the
// SESSION's location relation (ddls.go); a studio's own is `locatedAt`. The
// `*0..7` bound matches wellnessMembersSpec's for the same reason: zero-hop is
// load-bearing (a staffer wired to the exact room matches) and the upper bound
// is WORKPLACE_MAX_DEPTH - 1, since `*0..N` admits depths 0..N inclusive while
// the Starlark walk tests 0..7.
const wellnessBookersSpec = `MATCH (bk:booking)
MATCH (bk)-[:bookedBy]->(id:identity)
RETURN
  bk.key AS key,
  bk.key AS bookingKey,
  id.key AS bookerKey,
  bk.status.data.value AS status,
  [(bk)-[:forSession]->(se:session)-[:atLocation]->(pl)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

// wellnessSessionsSpec projects one row per session, walking atStudio, ledBy
// and partOf (each 0..1, so the row stays one-per-anchor — the §10.2 shape,
// mirroring clinicAppointmentsSpec's forPatient/withProvider walk).
// studioName/studioKey are null when the studio link is absent — CreateSession
// always writes atStudio, so in practice this means the studio was later
// TombstoneStudio'd (no cascade onto the link, ddls.go), not that the
// session never had one (verticals.md "retiring a studio strands its
// classes"). missingStudio names that gap explicitly, mirroring lease-
// signing's missing_manager convergence-flag shape (c643cf06): a session in
// this state is still live and bookable, just orphaned, and
// ReassignSession's operator-only newStudio repair path (ddls.go) is what a
// consumer flagging this column should point staff at.
// instructorKey/instructorName are null for the many
// sessions nobody leads, CreateSession's instructor param being optional.
// missingInstructor names that gap explicitly, mirroring missingStudio, for
// a roster consumer to point staff at ReassignSession's newInstructor repair.
//
// instructorKey is what scopes a bound instructor's own-roster read: their
// `identifiedBy` anchor names their instructor vertex, and this column is the
// only projection that answers which sessions that vertex leads.
// instructorName is the instructor's professional display name and is
// projected on this OPEN read model deliberately — a class instructor is the
// analog of clinic's provider directory, which stays public precisely while
// patient names moved behind the Protected lens.
//
// seriesKey names the recurring-class parent CreateSessionSeries linked this
// occurrence to (`session partOf sessionseries`, ddls.go), and is null for
// the many sessions CreateSession minted one at a time. Nothing else on this
// row distinguishes an occurrence from a one-off, which is what left a whole
// series unaddressable: TombstoneSessionSeries takes a seriesKey, and no
// reader could name one because the series vertex is reachable only from its
// occurrences' own links. A consumer additionally derives "how many
// occurrences are still upcoming" by grouping the rows it already holds on
// this column — the same client-side aggregation bookedCount uses below,
// for the same reason (no aggregate COUNT).
//
// The series' OWN .definition (name, capacity, cadence, occurrenceCount) is
// deliberately NOT projected alongside it: every occurrence carries its own
// .schedule copy of the fields a reader renders, and each stays individually
// editable by ReassignSession afterward, so re-projecting the parent's
// original intent would show a reader the series as authored rather than the
// occurrence as it now stands.
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
// staffer nine levels up whose writes require_workplace refuses. The location
// nodes carry no label because the chain must admit a location at ANY level —
// a building, a floor, a room — and a bare node is the simplest way to say so,
// the same reason edge-manifest's workplace chains leave them bare.
// (`:location*`, the abstract label with the taxonomy sigil, would say it
// precisely, but it makes the walk depend on the taxonomy resolver being armed
// for no gain here.) The labelled `(s:studio)` head is what keeps the
// comprehension anchored rather than seeding the whole keyspace.
const wellnessSessionsSpec = `MATCH (se:session)
OPTIONAL MATCH (se)-[:atStudio]->(s:studio)
OPTIONAL MATCH (se)-[:ledBy]->(i:instructor)
OPTIONAL MATCH (se)-[:partOf]->(ss:sessionseries)
RETURN
  se.key AS key,
  se.key AS sessionKey,
  se.schedule.data.name AS name,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.capacity AS capacity,
  se.schedule.data.priceCents AS priceCents,
  se.schedule.data.residentPriceCents AS residentPriceCents,
  s.key AS studioKey,
  s.profile.data.name AS studioName,
  i.key AS instructorKey,
  i.profile.data.displayName AS instructorName,
  ss.key AS seriesKey,
  [(s)-[:locatedAt]->(pl)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations,
  (s.key = null) AS missingStudio,
  (i.key = null) AS missingInstructor`

// wellnessBookingsSpec projects one row per booking, walking forSession and
// bookedBy (each 0..1). bookerKey (not a name) is projected — identity
// carries no display name of its own; a consuming FE scopes "my classes" by
// comparing bookerKey to the logged-in actor's own identity key, the same
// bare-key scoping cmd/loftspace-app's applicant views use.
//
// studioKey/studioName walk one further hop off the session, mirroring
// wellnessSessionsSpec's own atStudio walk above — My Classes otherwise names
// a class with no place to show up at. Both go null when sessionKey does
// (the session was tombstoned) OR when the session's OWN studio was
// TombstoneStudio'd out from under it (missingStudio, same gap and same
// meaning as wellnessSessionsSpec's own column above).
const wellnessBookingsSpec = `MATCH (b:booking)
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (se)-[:atStudio]->(s:studio)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS key,
  b.key AS bookingKey,
  b.status.data.value AS status,
  b.status.data.rate AS rate,
  b.status.data.waitlistSlot AS waitlistSlot,
  se.key AS sessionKey,
  se.schedule.data.name AS sessionName,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.priceCents AS priceCents,
  se.schedule.data.residentPriceCents AS residentPriceCents,
  s.key AS studioKey,
  s.profile.data.name AS studioName,
  ((se.key <> null) AND (s.key = null)) AS missingStudio,
  id.key AS bookerKey`

// orphanedBookingSettlementSpec is the one-row-per-booking convergence
// cypher: TombstoneSession deliberately does not cascade (package.go), so a
// called-off class otherwise leaves its live bookings, claimed seat cells and
// double-book guards stranded forever.
//
// The violation predicate is `liveSessionKey = null` alone: the OPTIONAL
// MATCH over forSession binds nothing once the session vertex is tombstoned,
// because a lens walk cannot see a link whose target is dead (the rule
// engine's traversal drops the neighbour, so neither the node nor the
// relationship variable binds — internal/refractor/ruleengine/full).
// `sessionKey` — the anchor CreateBooking stamps on the booking's OWN .status
// aspect (ddls.go), which survives the session's tombstone because it lives on
// the booking — is PROJECTED but is not part of the predicate: it is the
// hydration convenience targets.go templates the session's optional reads off,
// so Weaver hands ReleaseOrphanedBooking a pre-read session document when the
// column is there and drops those reads when it is null.
//
// Every live booking carries exactly one forSession link by construction —
// CreateBooking and JoinWaitlist are its only minters (ddls.go), each writing
// the booking vertex, its .status aspect and the link in one atomic batch — so
// for a booking still in `booked`/`waitlisted`/`noShow`, `liveSessionKey = null`
// says the session is tombstoned whether or not the aspect names it, and a row
// carrying no anchor is released off its link instead (ReleaseOrphanedBooking's
// own forSession enumeration, ddls.go).
//
// Transient adjacency is the script's problem either way: a booking whose
// forSession edge is not yet indexed also reads `se` null, and the script's
// SessionStillLive re-check is the backstop that keeps such a row from
// releasing anything — the lens row is a candidate, not a trusted command.
//
//   - `missing_release` — the booking is still in `booked`, `waitlisted` OR
//     `noShow` status and its session is no longer LIVE
//     (`se` null via the OPTIONAL MATCH — the forSession link itself is
//     untouched by TombstoneSession, only the session vertex is gone).
//     Weaver dispatches ReleaseOrphanedBooking{bookingKey} (targets.go),
//     which re-reads and re-confirms the session is dead before releasing
//     anything (whichever of seat/waitlistSlot its OWN status names) — the
//     lens row is a candidate, not a trusted command. Once released the
//     booking itself is tombstoned, so the anchor row (`b:booking`) stops
//     existing and the gap converges by the row disappearing, the same
//     EmptyBehavior:"delete" shape every actorAggregate target here uses.
//     All three live statuses belong to the condition, because each one holds
//     bookkeeping a called-off class strands. A `waitlisted` booking is as
//     orphaned as a `booked` one: its .wl<n> slot and double-book guard
//     (JoinWaitlist, ddls.go) stay held until something releases them, and no
//     other path can see them. `noShow` belongs because the auto-no-show
//     sweep (wellness-reminders' pastDueBookings) and TombstoneSession race
//     independently — a booking reaches `noShow` before its class is called
//     off as readily as after, and from the member's own FE
//     (`cancelled = !b.sessionName`, app.js) a dead session reads "the studio
//     called off this class" whichever fired first. Excluding it makes that
//     ordering accident permanent: My Classes renders an un-cancellable
//     "Class cancelled" card, and any no-show fee posted for the booking is
//     reachable by no other convergence.
//
// A booking already attended, or one whose session is still live, never
// violates — SetBookingAttendance's attended value and CancelBooking are the
// paths that own those; this lens only ever answers for a class that was
// called off out from under a still-`booked`/`waitlisted`/`noShow` seat or
// slot. `attended` is deliberately excluded: unlike the three states
// above, an attended booking is a member's genuine participation record —
// wellness-app's My Classes reads it as history, so tombstoning it once its
// session is later archived would trade real history away, not just release
// stranded bookkeeping. (Its seat cell does go unreleased in that case, and
// a standing no-show fee that a re-mark-to-attended left charged — the
// settles link, still live per SetBookingAttendance's own carry-forward note
// above — is not reachable by any automated reversal. It is still reachable
// by a HUMAN one: the transaction itself is a permanent, independent record
// (postedTo an account), so it stays visible in wellnessLedgerHistory and
// front-desk-waivable via WellnessCreditAccount{reason:"waiver"} regardless
// of whether the booking or session later disappear — the same manual
// escape hatch every other discretionary credit in this package already
// relies on. An automated reversal here would need its own mechanism, keyed
// on the RE-MARK in SetBookingAttendance, not a release path that would
// otherwise have to choose between a member's history and their money.)
// orphanedBookingSettlementSpec is built once at package init: the retry cap
// (maxReleaseRetries) bakes into the constant maxretries_release column, the
// §10.2 "the policy lives in the cypher" convention lease-signing's
// leaseApplicationCompleteSpec established. The cypher carries no literal '%'.
var orphanedBookingSettlementSpec = fmt.Sprintf(`MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
WITH
  b.key AS entityKey,
  b.status.data.value AS status,
  b.status.data.session AS sessionKey,
  se.key AS liveSessionKey
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS bookingKey,
  sessionKey,
  status,
  (((status = 'booked') OR (status = 'waitlisted') OR (status = 'noShow')) AND (liveSessionKey = null)) AS missing_release,
  (((status = 'booked') OR (status = 'waitlisted') OR (status = 'noShow')) AND (liveSessionKey = null)) AS violating,
  %d AS maxretries_release
`, maxReleaseRetries)

// waitlistPromotionSpec is the one-row-per-SESSION waitlist-promotion
// convergence cypher: a class that has both a live waitlist and an unclaimed
// seat is a stranded member, and the gap it opens dispatches
// PromoteWaitlistedBookings (targets.go).
//
// ANCHORED ON THE SESSION, not the booking, because the condition is a
// property of the CLASS — "are there more seats than seat-holders, with
// someone still waiting" — and neither half is visible from one booking. The
// seat count is an aggregate over the session's whole forSession-in booking
// set, so this is the package's one lens that needs an AGGREGATING WITH
// (lease-signing's leaseApplicationCompleteSpec
// `count(DISTINCT CASE WHEN … END)` shape). Tombstoned bookings never reach
// the aggregate: Contract #1 isDeleted read-filtering drops them from the
// walk, so a cancelled seat frees capacity the moment its booking dies.
//
// THE CAPACITY COMPARISON IS THE GAP. seatedCount < capacity is the same
// question claim_free_seats asks by enumeration (ddls.go), asked in
// aggregate: CreateBooking claims seat cells 1..capacity, and CancelBooking's
// own promotion walk hands a freed seat straight to the earliest waitlisted
// booking, so the only way a seat goes free while someone waits is a path
// that frees capacity WITHOUT freeing a seat cell — ReassignSession raising
// `capacity` on a full class (the live case: capacity 1→5 with a
// waitlistSlot 1 booking left untouched), or a session whose seats were
// released by a path that never looked at the waitlist.
//
// BOTH COUNTS ARE OF CELL-HOLDERS, not of a status word, because a cell is
// what the op contends for. seatedCount counts bookings carrying a
// .status.seat: a class that ran and was then rescheduled forward carries
// attended and noShow bookings that STILL hold their seat cells (nothing
// releases them, and ReassignSession has no started-class guard), so counting
// `value = 'booked'` would read those seats as free and dispatch an op that
// then finds every cell claimed. waitlistedCount counts bookings carrying a
// .status.waitlistSlot — exactly the population collect_waitlist_candidates
// acts on, which skips a waitlisted status with no slot recorded.
//
// NO CLOCK. Following pastDueBookings (wellness-reminders) and Andrew's
// 2026-09-01 rule that a time fact is recorded on the entity, the lens never
// reads $now: freshUntil binds DIRECTLY to the session's own
// .schedule.startsAt, and the gate reads the lapse the fired MarkExpired
// records under THIS target's key on the SESSION (the row's entityKey). So a
// class that has started is closed out by a stored fact — the timer this row
// armed fired at startsAt, recorded byTarget.wellnessWaitlistPromotion, and
// every later projection reads the gap shut — rather than by a clock read the
// projection would have to re-evaluate. compareAny answers false when either
// operand is nil, so a session no timer has fired on, and one carrying no
// schedule at all, both read not-lapsed.
//
// freshUntil arms on waitlistedCount > 0 ALONE, not on the gap: a class that
// is full today is exactly the one whose waitlist matters tomorrow, so the
// timer must already be armed when a later cancellation or capacity raise
// opens the seat. A session with no waitlist projects freshUntil null, which
// cancels nothing already armed (weaver.md) — harmless, since a waitlist can
// never appear after the class starts (JoinWaitlist's own SessionInPast
// guard, prepare_booking_common) and the lapse the armed timer records is
// what shuts the row anyway.
var waitlistPromotionSpec = fmt.Sprintf(`MATCH (se:session {key: $actorKey})
OPTIONAL MATCH (se)<-[:forSession]-(b:booking)
WITH
  se.key AS entityKey,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.capacity AS capacity,
  se.freshnessExpiry.data.byTarget.%[1]s AS lapsedAt,
  count(DISTINCT CASE WHEN NOT (b.status.data.seat = null) THEN b.key ELSE null END) AS seatedCount,
  count(DISTINCT CASE WHEN NOT (b.status.data.waitlistSlot = null) THEN b.key ELSE null END) AS waitlistedCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS sessionKey,
  startsAt,
  capacity,
  seatedCount,
  waitlistedCount,
  CASE WHEN (waitlistedCount > 0) AND NOT (lapsedAt >= startsAt) THEN startsAt ELSE null END AS freshUntil,
  ((waitlistedCount > 0) AND (seatedCount < capacity) AND NOT (lapsedAt >= startsAt)) AS missing_promotion,
  ((waitlistedCount > 0) AND (seatedCount < capacity) AND NOT (lapsedAt >= startsAt)) AS violating,
  %[2]d AS maxretries_promotion
`, WaitlistPromotionTarget, maxPromotionRetries)

// wellnessIdentitiesReadSpec projects one row per NAMED identity — the
// roster wellness-app resolves the signed-in actor's own name against. The
// WHERE keeps only identities carrying a `.name` aspect via ciphertext
// presence (`i.name.data.ct <> null` — there is no plaintext `value` field
// at rest), mirroring loftspace-domain's applicantRosterReadSpec.
// authz_anchors carries the identity's OWN bare NanoID (see the Lenses()
// declaration above for why that self-anchor is the right shape here) PLUS
// the workplace fan-out and the booking fan-out below.
//
// The workplace fan-out is a pattern comprehension anchored on `i`, one hop
// further out than wellnessMembersSpec's own coveringLocations walk:
// identity -> leaseapp -> unit -> building. `applicationFor` runs
// leaseapp -> identity (Contract #1 §1.1: the later-arriving leaseapp is the
// source), so the walk reads `(i)<-[:applicationFor]-(l:leaseapp)`; the
// `*0..7` containedIn bound matches wellnessMembersSpec's own upper bound, so
// a worksAt-anchored staffer's confinement (permissions.go's
// worksAt_covers) resolves the name of any member whose lease that building
// covers — the same gap cafe-domain's cafeIdentitiesRead already closed for
// its front desk.
//
// The booking fan-out is a second pattern comprehension, mirroring
// applicantRosterReadSpec's multi-arm `+` concatenation
// (loftspace-domain/lenses.go): identity <- bookedBy - booking -
// forSession -> session -atLocation-> location -containedIn*0..7-> building.
// `bookedBy` runs booking -> identity and `forSession` runs
// booking -> session (Contract #1 §1.1: the later-arriving vertex is the
// source), so the walk reads
// `(i)<-[:bookedBy]-(bk:booking)-[:forSession]->(se:session)-[:atLocation]->(pl)`.
// A returning guest has no lease or workplace grant to anchor on — their
// only standing relationship is the booking itself — so a front-desk
// staffer's worksAt-building grant resolves the guest's name only for the
// building(s) their booked session's location falls within, never
// platform-wide: the fan-out scopes disclosure at least as tight as the
// booking row the staffer already sees. An identity with no booking still
// projects on the self-anchor alone (the walk finds no match and the
// fan-out is simply empty).
const wellnessIdentitiesReadSpec = `MATCH (i:identity)
WHERE i.name.data.ct <> null
RETURN
  nanoIdFromKey(i.key)   AS identity_id,
  i.key                  AS identity_key,
  i.name.data            AS name,
  [nanoIdFromKey(i.key)] + [(i)<-[:applicationFor]-(l:leaseapp)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | nanoIdFromKey(c.key)] + [(i)<-[:bookedBy]-(bk:booking)-[:forSession]->(se:session)-[:atLocation]->(pl)-[:containedIn*0..7]->(c) | nanoIdFromKey(c.key)]
                         AS authz_anchors
`
