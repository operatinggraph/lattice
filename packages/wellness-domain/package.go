// Package wellnessdomain is the wellness-domain Capability Package — the
// bookable foundation of the wellness vertical (studio / session / booking).
//
// SELF-CONTAINED except for one cross-package known-key read: CreateBooking's
// optional resident-rate check reads lease-signing's leaseapp
// applicationFor link (no declared install dependency needed at the
// Starlark level — the same "read another package's vertex by known key"
// idiom loftspace-ledger's heldFor / cafe-domain's cafeTabSettlement lens
// already use for leaseapp).
//
//	vtx.studio.<id>        class=studio        root {}   .profile    {name}
//	vtx.session.<id>       class=session       root {}   .schedule   {name, startsAt, endsAt, capacity, priceCents?, remindAt}
//	                                                      .slot<cellcode> claims on the STUDIO hub (studioSlotClaim)
//	                                                      .seat<n> claims on the SESSION hub (sessionSeatClaim)
//	vtx.sessionseries.<id> class=sessionseries  root {}   .definition {name, capacity, priceCents?, intervalDays,
//	                                                       occurrenceCount, firstStartsAt, firstEndsAt}
//	vtx.booking.<id>       class=booking        root {}   .status     {value: booked|waitlisted, rate: standard|resident, seat?, waitlistSlot?, session}
//	vtx.instructor.<id>    class=instructor     root {}   .profile    {displayName}
//	lnk.studio.<id>.locatedAt.<locType>.<locId>        (studio → location, optional; browse reachability, no authZ)
//	lnk.session.<id>.atStudio.studio.<id>              (session → studio, later-arriving source)
//	lnk.session.<id>.atLocation.<locType>.<locId>      (session → location, snapshotted from the studio's locatedAt
//	                                                     link(s) at CreateSession; TombstoneStudio fallback)
//	lnk.session.<id>.ledBy.instructor.<id>             (session → instructor, optional, later-arriving source)
//	lnk.session.<id>.partOf.sessionseries.<id>         (session → sessionseries, only when minted by
//	                                                     CreateSessionSeries; later-arriving source)
//	lnk.sessionseries.<id>.atStudio.studio.<id>        (sessionseries → studio, later-arriving source)
//	lnk.instructor.<id>.teachesAt.studio.<id>          (instructor → studio, optional, later-arriving source)
//	lnk.instructor.<id>.identifiedBy.identity.<id>     (instructor → identity, the provider-archetype login binding)
//	lnk.booking.<id>.forSession.session.<id>           (booking → session, later-arriving source)
//	lnk.booking.<id>.bookedBy.identity.<id>            (booking → identity, later-arriving source)
//	lnk.booking.<id>.residentRate.leaseapp.<id>        (booking → leaseapp, only when rate=resident)
//
// BindInstructorIdentity binds an instructor to a real login identity —
// identity-domain's `provider` archetype role (persona-worlds-design.md Fire
// W0), mirroring clinic-domain's BindProviderIdentity: an identifiedBy link +
// CreateOnly mutual-exclusivity guards on both sides + an idempotent holdsRole
// grant. A bound instructor gains a standing TombstoneSession grant confined,
// in-script, to sessions they lead (ledBy).
//
// The studio's booking grid is a mandatory 15-minute cadence: double-session
// detection is a WRITE-PATH deterministic-key claim on the studio hub
// (studioSlotClaim), mirroring clinic-domain's providerSlotClaim exactly —
// see wellness-vertical-design.md §1(1). Session CAPACITY (an N-seat roster,
// not a 1:1 exclusivity lock) extends the SAME CreateOnly-key-collision
// mechanism over an enumerated seat-index dimension on the session hub
// (sessionSeatClaim) — see wellness-vertical-design.md §1(2). No genuinely
// new primitive: both are the identical write-path claim idiom applied to a
// different candidate-key dimension.
//
// Ten ops (known-key kv.Read plus bounded, paginated kv.Links enumeration
// only — the holdsRole/workplace walks shared with clinic-domain, and
// CancelBooking's own promotion-candidate walk below — no raw prefix scans):
//
//	CreateStudio / TombstoneStudio
//	CreateSession (validates the studio alive + class, discretizes the grid,
//	  claims one studioSlotClaim per covered cell — StudioConflict on
//	  collision; accepts an optional priceCents (>= 0), stored on .schedule,
//	  carried forward unchanged by ReassignSession, 0/omitted meaning a free
//	  class — wellness-ledger's wellnessClassPriceSettlement lens reads it to
//	  auto-charge the booker once an account exists) / TombstoneSession
//	  (releases the held cells; does NOT cascade to bookings — see
//	  ReleaseOrphanedBooking below)
//	CreateSessionSeries (CreateSession run occurrenceCount times on a fixed
//	  intervalDays cadence, eagerly, in one atomic op — mints a
//	  vtx.sessionseries parent + occurrenceCount ordinary vtx.session
//	  occurrences, each partOf-linked back to it; the whole series rejects
//	  StudioConflict together if any single occurrence collides. Every
//	  occurrence stays individually editable via ReassignSession/
//	  TombstoneSession afterward — there is no TombstoneSessionSeries)
//	CreateBooking (validates the session + booker alive + class, claims the
//	  first free seat within capacity — SessionFull once exhausted — and,
//	  when an optional leaseAppKey is supplied, verifies the booker is that
//	  lease's actual applicant via the applicationFor link before granting
//	  the resident rate; a mismatch falls through to the standard rate,
//	  never a hard rejection) / JoinWaitlist (the SAME validation, factored
//	  into prepare_booking_common, ddls.go, but claims a separate
//	  sessionWaitlistClaim slot instead of a seat — WaitlistFull once
//	  MAX_WAITLIST_SIZE=200 is exhausted; a booker may hold at most one live
//	  claim, booked or waitlisted, per session) / CancelBooking (releases the
//	  held seat or waitlist slot, rejecting once the class has begun or
//	  attendance is recorded; cancelling a booked seat runs a bounded walk —
//	  find_promotion_candidate, ddls.go — that hands the freed seat directly
//	  to the session's earliest live waitlisted booking, if any, in the same
//	  mutation batch) / SetBookingAttendance (records attended | noShow once
//	  the class has begun, carrying the seat / booker bookkeeping forward for
//	  ReleaseOrphanedBooking's convergence lens — a marked booking is no
//	  longer CancelBooking-eligible) / ReleaseOrphanedBooking (operator-only,
//	  Weaver-dispatched: drains a still-`booked` or still-`waitlisted`
//	  booking whose session was tombstoned out from under it — the
//	  wellnessOrphanedBookingSettlement convergence lens/target below, never
//	  client-invoked)
//
// Five PROJECTION lenses are the P5 query surface a wellness FE reads
// (never Core KV): wellnessStudios (the studio picker), wellnessSessions
// (the schedule grid, joined to studio), wellnessBookings (the roster /
// my-classes query surface, joined to session), wellnessInstructors (the
// scheduling form's ledBy picker), and wellnessMembers (the front desk's
// book-a-member picker, one row per lease carrying its member and the
// locations that cover them — cafe-domain's cafeLeaseWorkplaces shape, so a
// staffer is offered the members whose building they work at). None carry
// PHI/PII, so — unlike clinic-domain — no protected Postgres/RLS layer is
// needed. A sixth, wellnessOrphanedBookingSettlement, is not an FE query
// surface at all — it is the weaver-targets convergence lens
// WeaverTargets() (targets.go) dispatches ReleaseOrphanedBooking over.
//
// OUT of scope this increment (the thin FE and the mixed-use composition
// surfaces that consume it are separate, sequenced items —
// verticals.md):
//   - cmd/wellness-app (Inc 2, mirrors cmd/clinic-app's schedule-grid /
//     roster / my-bookings shape).
//   - Provider-style .hours / .timeOff availability layers on the studio —
//     no demand row asks for studio business hours; YAGNI.
//   - A booked-count aggregate lens column — the lens engine has no
//     aggregate COUNT; a consuming FE derives it client-side from
//     wellnessBookings (the cmd/cafe-app computeTabs idiom).
//   - Cascade-on-tombstone for STUDIOS. TombstoneStudio soft-deletes ONLY the
//     named vertex root — orphaned sessions simply drop from the projection
//     lenses' joins (mirrors clinic-domain / location-domain: no platform
//     owner-tombstone-cascade trigger exists). SESSIONS are the one
//     exception: TombstoneSession still soft-deletes only the session root,
//     but the wellnessOrphanedBookingSettlement Weaver target (targets.go)
//     converges the gap it leaves — a `booked` or `waitlisted` booking whose
//     session died — by dispatching ReleaseOrphanedBooking, the lens-driven convergence
//     idiom clinic-ledger's no-show settlement already uses, not an
//     in-script cascade.
//
// Install via `lattice-pkg install packages/wellness-domain`. Depends
// lease-signing (documentation only — the leaseapp CreateBooking reads by
// known key; install order/honesty, mirrors loftspace-ledger's own Depends
// comment). See _bmad-output/implementation-artifacts/wellness-vertical-design.md.
package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "wellness-domain",
	Version:       "0.22.4",
	Description:   "Wellness bookable domain: studio / session / sessionseries / booking / instructor vertex types + their aspects and links, written by Create*/Tombstone*/Cancel*/Bind*/Release* ops. CreateSession claims a deterministic studioSlotClaim per covered 15-minute cell (double-session lock, mirrors clinic-domain's providerSlotClaim) and, when an optional instructor is named, an instructorSlotClaim per covered cell on the instructor's own hub too (InstructorConflict — the studio lock alone only guards the ROOM, so the same instructor double-booked across two different studios for an overlapping span now collides too, mirroring clinic-domain's providerSlotClaim a second time on a second hub; TombstoneSession releases it and ReassignSession migrates it on an instructor swap or a time move, the same two mechanisms studioSlotClaim uses) and accepts an optional priceCents (>= 0, 0/omitted meaning a free class) plus an optional residentPriceCents (>= 0, charged instead of priceCents to a booking whose .status.rate is resident; omitted means a resident pays priceCents same as a standard booker) stored on .schedule and carried forward unchanged by ReassignSession — wellness-ledger's wellnessClassPriceSettlement lens reads both to auto-charge the booker's ledger account once one exists, the class-price half of the item this vertical's board flags ('a wellness class still has no price or pass'; pass/membership stays out of scope). CreateSessionSeries is CreateSession run occurrenceCount times (bounded 2..52) on a fixed intervalDays cadence (bounded 1..365), eagerly, in one atomic op — the studio's Mon/Wed/Fri weekly class no longer needs occurrenceCount separate CreateSession calls; it mints a vtx.sessionseries parent + its .definition once, plus occurrenceCount ordinary vtx.session occurrences each partOf-linked back to it (StudioConflict on any single occurrence's cell collision rejects the whole series atomically, no partial series), and every occurrence stays individually editable via ReassignSession/TombstoneSession afterward — there is no TombstoneSessionSeries this increment. CreateBooking claims the first free sessionSeatClaim within the session's capacity (SessionFull once exhausted — the same CreateOnly idiom extended over a seat-index dimension), rejects a booking on an already-started/ended session (SessionInPast, mirroring clinic's ScheduleInPast) and a booker's second live booking on the same session (DoubleBooked, a deterministic per-(session, booker) sessionBookerClaim guard mirroring cafe's cafeOpenTabGuard, released by CancelBooking so a re-book re-claims it) and, on the booker's OWN identity hub, one bookerSlotClaim per covered 15-minute cell of the session's span (BookerConflict, mirroring clinic-domain's patientSlotClaim — sessionBookerClaim alone cannot see a booker claimed into two DIFFERENT overlapping sessions, since each session's cells are disjoint keys; CancelBooking and ReleaseOrphanedBooking both release these cells the same way they release the sessionBookerClaim guard) and, given an optional leaseAppKey, verifies residency via lease-signing's applicationFor link before granting the resident rate (a mismatch falls through to standard, never a hard failure). JoinWaitlist runs the identical validation (factored into prepare_booking_common) but claims a separate sessionWaitlistClaim slot instead (WaitlistFull once MAX_WAITLIST_SIZE=200 is exhausted) and mints .status {value: waitlisted, waitlistSlot} — the SAME sessionBookerClaim guard CreateBooking claims is shared, so a booker may hold at most one live claim, booked or waitlisted, per session (DoubleBooked either way). CancelBooking on a still-`booked` seat additionally runs find_promotion_candidate, a bounded paginated kv.Links walk over the session's forSession-in bookings that picks the live waitlisted one with the lowest waitlistSlot and hands it the just-freed seat directly, in the SAME mutation batch, instead of tombstoning the seat cell back open — closing the race a two-step release-then-reclaim would leave open for an unrelated new CreateBooking caller to win the seat instead; exhausting the bound degrades softly (no promotion this round) rather than failing the cancellation. A waitlisted booking cancelling itself just releases its own waitlist slot, no promotion involved. BindInstructorIdentity binds an instructor to a real login identity via the identity-domain `provider` archetype role (persona-worlds-design.md Fire W0), mirroring clinic-domain's BindProviderIdentity; a bound instructor's TombstoneSession grant is confined, in-script, to sessions they lead. SetBookingAttendance moves a booking's .status.value from booked to attended or noShow, carrying rate / seat / booker / session forward on an OCC upsert of the aspect's own revision — those fields now only matter to ReleaseOrphanedBooking, since a marked booking is no longer CancelBooking-eligible (AttendanceRecorded); either value corrects the other, a class that has not begun is rejected (SessionNotStarted, the mirror of SessionInPast), and the same standing guard confines a bound instructor to bookings on sessions they lead. Transitioning to noShow also stores a noShowFeeCents amount on .status (caller-supplied or a 2500 default, mirroring clinic-domain's SetAppointmentStatus) that wellness-ledger's wellnessNoShowSettlement Weaver playbook reads to auto-charge the booker's ledger account once one exists; the field is NOT carried forward on a later re-mark to attended, though a charge already posted stands. TombstoneSession does not cascade to bookings, so ReleaseOrphanedBooking (operator-only, never client-invoked) drains a still-booked OR still-waitlisted booking whose session died — dispatched by the wellnessOrphanedBookingSettlement Weaver target (targets.go) over the missing_release gap its convergence lens computes (now matching status=booked OR status=waitlisted), mirroring clinic-ledger's no-show settlement idiom; it re-confirms the session is genuinely dead before releasing whichever cell its own status names (seat or waitlist slot) and the double-book guard and soft-deleting the booking. Six lenses (wellnessStudios, wellnessSessions, wellnessBookings, wellnessInstructors, wellnessMembers, wellnessOrphanedBookingSettlement) plus the protected wellnessIdentitiesRead are the P5 read models a wellness FE (or Weaver) reads; wellnessMembers carries each member's covering locations so the front desk's book-a-member picker offers only the members whose building the staffer works at, mirroring cafe-domain's cafeLeaseWorkplaces. No PHI/PII, no protected Postgres layer for the booking-side data. Depends lease-signing (documentation only — CreateBooking reads its leaseapp by known key, no install-order requirement enforced at the Starlark level). SetInstructorProfile is the instructor hat's record-administering op — it replaces an instructor's .profile displayName, gated by the same standing binder (operator, else the caller's own identifiedBy binding to THAT instructor) clinic-domain's SetProviderHours uses; it is what makes a bound instructor's Facet hat chip resolve an op instead of rendering inert. CreateBooking, JoinWaitlist, CancelBooking, TombstoneSession, SetBookingAttendance, SetInstructorProfile, CreateSession, and CreateSessionSeries each carry an op-meta with the edge-manifest descriptor vocabulary (presentation/inputSchema/dispatch, edge-showcase-app-design.md §3.3, Fire 5 adoption) — metadata only; a client still needs a service-catalog path (permitsOperation) to discover these ops. ReleaseOrphanedBooking carries no op-meta — it is Weaver-only. CancelBooking also now mints a wellnessrefund marker vertex (root {} + .detail aspect {accountKey, amountCents, bookingKey}, reverses link to the original wellnesstransaction) whenever the booking being cancelled already carries a posted settlesClassPrice charge — the booking is tombstoned in the same mutation batch, so no post-tombstone lens walk could ever find a marker written onto its own aspects; wellness-ledger's wellnessRefundSettlement lens walks the reverses link to converge the refund into a WellnessCreditAccount.",
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	OpMetas:       OpMetas(),
	WeaverTargets: WeaverTargets(),
}
