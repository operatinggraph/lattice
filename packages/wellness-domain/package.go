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
//	vtx.studio.<id>   class=studio    root {}   .profile  {name}
//	vtx.session.<id>  class=session   root {}   .schedule {name, startsAt, endsAt, capacity}
//	                                             .slot<cellcode> claims on the STUDIO hub (studioSlotClaim)
//	                                             .seat<n> claims on the SESSION hub (sessionSeatClaim)
//	vtx.booking.<id>  class=booking   root {}   .status   {value: booked, rate: standard|resident, seat}
//	lnk.studio.<id>.locatedAt.<locType>.<locId>        (studio → location, optional; browse reachability, no authZ)
//	lnk.session.<id>.atStudio.studio.<id>              (session → studio, later-arriving source)
//	lnk.booking.<id>.forSession.session.<id>           (booking → session, later-arriving source)
//	lnk.booking.<id>.bookedBy.identity.<id>            (booking → identity, later-arriving source)
//	lnk.booking.<id>.residentRate.leaseapp.<id>        (booking → leaseapp, only when rate=resident)
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
// Six ops (known-key kv.Read only — no kv.Links enumeration, no raw prefix
// scans):
//
//	CreateStudio / TombstoneStudio
//	CreateSession (validates the studio alive + class, discretizes the grid,
//	  claims one studioSlotClaim per covered cell — StudioConflict on
//	  collision) / TombstoneSession (releases the held cells)
//	CreateBooking (validates the session + booker alive + class, claims the
//	  first free seat within capacity — SessionFull once exhausted — and,
//	  when an optional leaseAppKey is supplied, verifies the booker is that
//	  lease's actual applicant via the applicationFor link before granting
//	  the resident rate; a mismatch falls through to the standard rate,
//	  never a hard rejection) / CancelBooking (releases the held seat)
//
// Three PROJECTION lenses are the P5 query surface a wellness FE reads
// (never Core KV): wellnessStudios (the studio picker), wellnessSessions
// (the schedule grid, joined to studio), and wellnessBookings (the roster /
// my-classes query surface, joined to session). None carry PHI/PII, so —
// unlike clinic-domain — no protected Postgres/RLS layer is needed.
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
//   - A per-booker double-booking guard (a resident holding two
//     simultaneous class enrollments) — a business choice, not a platform
//     gap; no demand row asks for it.
//   - Cascade-on-tombstone. TombstoneStudio / TombstoneSession soft-delete
//     ONLY the named vertex root — orphaned sessions/bookings simply drop
//     from the projection lenses' joins (mirrors clinic-domain / location-
//     domain: no platform owner-tombstone-cascade trigger exists).
//
// Install via `lattice-pkg install packages/wellness-domain`. Depends
// lease-signing (documentation only — the leaseapp CreateBooking reads by
// known key; install order/honesty, mirrors loftspace-ledger's own Depends
// comment). See _bmad-output/implementation-artifacts/wellness-vertical-design.md.
package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "wellness-domain",
	Version:     "0.8.1",
	Description: "Wellness bookable domain: studio / session / booking vertex types + their aspects and links, written by Create*/Tombstone*/Cancel* ops. CreateSession claims a deterministic studioSlotClaim per covered 15-minute cell (double-session lock, mirrors clinic-domain's providerSlotClaim). CreateBooking claims the first free sessionSeatClaim within the session's capacity (SessionFull once exhausted — the same CreateOnly idiom extended over a seat-index dimension) and, given an optional leaseAppKey, verifies residency via lease-signing's applicationFor link before granting the resident rate (a mismatch falls through to standard, never a hard failure). Three projection lenses (wellnessStudios, wellnessSessions, wellnessBookings) are the P5 read models a wellness FE reads. No PHI/PII, no protected Postgres layer. Depends lease-signing (documentation only — CreateBooking reads its leaseapp by known key, no install-order requirement enforced at the Starlark level). CreateBooking and CancelBooking each carry an op-meta with the edge-manifest descriptor vocabulary (presentation/inputSchema/dispatch, edge-showcase-app-design.md §3.3, Fire 5 adoption) — metadata only; a client still needs a service-catalog path (permitsOperation) to discover these ops.",
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
	OpMetas:     OpMetas(),
}
