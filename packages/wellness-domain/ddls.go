package wellnessdomain

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// instructorRoleKey is identity-domain's "provider" role key, computed
// deterministically (pkgmgr.RoleID mirrors what the installer mints at
// install time — no KV read required). BindInstructorIdentity's script pins
// its holdsRole grant against this literal rather than trusting any live
// vtx.role.* the caller supplies — mirrors clinic-domain's providerRoleKey
// pin (packages/clinic-domain/ddls.go): the grant matrix already restricts
// who can call the op, but the op's OWN script should not be steerable into
// granting a different role to the bound identity.
var instructorRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")

// Canonical names. Four vertexType DDLs own the op scripts (each op is
// admitted by EXACTLY ONE vertexType DDL — the operationType→script index
// drops an op claimed by two, so no overlap is allowed there). Aspect-type
// DDLs are step-6 write gates only, mirroring clinic-domain's split.
const (
	studioVertexDDL        = "studio"
	sessionVertexDDL       = "session"
	sessionSeriesVertexDDL = "sessionseries"
	bookingVertexDDL       = "booking"
	instructorVertexDDL    = "instructor"
	refundVertexDDL        = "wellnessrefund"

	studioProfileAspectDDL           = "studioProfile"
	sessionScheduleAspectDDL         = "sessionSchedule"
	studioSlotClaimAspectDDL         = "studioSlotClaim"
	instructorSlotClaimAspectDDL     = "instructorSlotClaim"
	bookerSlotClaimAspectDDL         = "bookerSlotClaim"
	sessionSeatClaimAspectDDL        = "sessionSeatClaim"
	sessionWaitlistClaimAspectDDL    = "sessionWaitlistClaim"
	sessionBookerClaimAspectDDL      = "sessionBookerClaim"
	bookingStatusAspectDDL           = "bookingStatus"
	sessionSeriesDefinitionAspectDDL = "sessionSeriesDefinition"
	sessionSeriesHorizonAspectDDL    = "sessionSeriesHorizon"
	refundDetailAspectDDL            = "wellnessRefundDetail"

	instructorProfileAspectDDL       = "instructorProfile"
	instructorIdentityClaimAspectDDL = "instructorIdentityClaim"
	identityInstructorClaimAspectDDL = "identityInstructorClaim"
)

// DDLs returns the package's DDL meta-vertex declarations, plus the
// booking-change notification-outcome replyOp pair (notifications.go) — the
// audit half of the mechanism a promotion/move notice (wellness-reminders)
// or a call-off notice (this package's own ReleaseOrphanedBooking) reports
// its outcome through:
//
//   - studio (vertexType) — owns CreateStudio + TombstoneStudio.
//   - session (vertexType) — owns CreateSession + TombstoneSession +
//     ReassignSession + CreateSessionSeries + TombstoneSessionSeries +
//     ReassignSessionSeries + ExtendSessionSeries (the four series-wide ops
//     that touch occurrences; each shares this DDL's write gate for the
//     session vertices/aspects it mints, tombstones or rewrites, alongside
//     its own sessionseries DDL below).
//   - sessionseries (vertexType) — owns CreateSessionSeries +
//     TombstoneSessionSeries + ReassignSessionSeries + ExtendSessionSeries +
//     StopSessionSeries, the studio's recurring-class parent record
//     (§ sessionSeriesVertexTypeDDL).
//   - booking (vertexType) — owns CreateBooking + CancelBooking + JoinWaitlist.
//   - instructor (vertexType) — owns CreateInstructor + TombstoneInstructor +
//     BindInstructorIdentity (the provider-archetype binding,
//     persona-worlds-design.md Fire W0).
//   - studioProfile / sessionSchedule / studioSlotClaim / instructorSlotClaim /
//     bookerSlotClaim / sessionSeatClaim / sessionWaitlistClaim /
//     sessionBookerClaim / bookingStatus / instructorProfile /
//     instructorIdentityClaim / identityInstructorClaim /
//     sessionSeriesDefinition / sessionSeriesHorizon (aspectType) — step-6
//     write gates.
//   - bookingChangeNotificationOp (vertexType) — owns
//     RecordBookingChangeNotification; bookingChangeNotification
//     (aspectType) — its step-6 write gate (notifications.go).
//
// Architectural rules (binding — the known-key discipline of clinic-domain /
// loftspace-domain): the scripts read ONLY by known key or by the bounded,
// paginated kv.Links enumeration idiom (Contract #2 §2.5.1, category (e) —
// the operator-role walk, the workplace-confinement walk, and
// find_promotion_candidate below all use it). No prefix scans, no raw
// adjacency lookups, no unbounded scan.
func DDLs() []pkgmgr.DDLSpec {
	ddls := []pkgmgr.DDLSpec{
		studioVertexTypeDDL(),
		sessionVertexTypeDDL(),
		sessionSeriesVertexTypeDDL(),
		bookingVertexTypeDDL(),
		instructorVertexTypeDDL(),
		studioProfileAspectTypeDDL(),
		sessionScheduleAspectTypeDDL(),
		studioSlotClaimAspectTypeDDL(),
		instructorSlotClaimAspectTypeDDL(),
		bookerSlotClaimAspectTypeDDL(),
		sessionSeatClaimAspectTypeDDL(),
		sessionWaitlistClaimAspectTypeDDL(),
		sessionBookerClaimAspectTypeDDL(),
		bookingStatusAspectTypeDDL(),
		instructorProfileAspectTypeDDL(),
		instructorIdentityClaimAspectTypeDDL(),
		identityInstructorClaimAspectTypeDDL(),
		sessionSeriesDefinitionAspectTypeDDL(),
		sessionSeriesHorizonAspectTypeDDL(),
		refundVertexTypeDDL(),
		refundDetailAspectTypeDDL(),
	}
	return append(ddls, notificationDDLs()...)
}

func studioVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     studioVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateStudio", "TombstoneStudio", "SetStudioProfile"},
		Description: "Wellness studio DDL. Vertex shape: vtx.studio.<NanoID>, class=studio, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateStudio mints the studio + writes the " +
			".profile aspect {name (required), noShowFeeCents (optional)} atomically, and — when the optional location param is supplied — " +
			"the studio locatedAt location LINK (lnk.studio.<id>.locatedAt.<locType>.<locId>, class \"locatedAt\"; " +
			"source = the later-arriving studio, target = the pre-existing location, Contract #1 §1.1). locatedAt " +
			"carries NO authorization meaning — it exists so reachability walks (edge-manifest's entity lenses) " +
			"can find the studio from a resident's containedIn chain; service-access authZ stays entirely on " +
			"service-location's availableAt. A studio with no location is legal and simply un-browsable. " +
			"TombstoneStudio soft-deletes one (no cascade onto its " +
			"sessions — the projection lenses anchor on the live root, mirroring clinic-domain's no-cascade rule) " +
			"and refuses HasUpcomingClasses while any live session at the studio still has a .schedule.startsAt " +
			"after submittedAt (walking the studio's inbound atStudio links, bounded; StudioSessionFanoutTooLarge " +
			"past the page cap) — call the class off first; a class that has already started is history and never " +
			"blocks the retire. Granted to operator + frontOfHouse; a front-of-house caller is confined in-script to a " +
			"studio at a building they worksAt (resolved off the studio's own locatedAt link; an unlocated studio " +
			"stays operator-only). SetStudioProfile edits the .profile aspect of a live studio — name and/or " +
			"noShowFeeCents, at least one required; each supplied field replaces its stored value and an omitted one " +
			"is carried forward (an OCC upsert on the profile's read revision) — under the same operator + " +
			"frontOfHouse grant and the same locatedAt confinement as TombstoneStudio. noShowFeeCents is the " +
			"studio's no-show policy in whole cents, validated a non-negative integer at both writers: " +
			"SetBookingAttendance bills it on a noShow mark that names no fee of its own (0 = fee-free), and a " +
			"studio with no policy recorded bills SetBookingAttendance's documented default.",
		Script: studioDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string","description":"The studio's display name (CreateStudio: required; SetStudioProfile: optional, replaces the stored name when supplied)."},` +
			`"noShowFeeCents":{"type":"integer","minimum":0,"description":"Optional no-show policy in whole cents (CreateStudio, SetStudioProfile). A non-negative integer; 0 means a no-show at this studio bills no fee. Absent on CreateStudio = no policy recorded (SetBookingAttendance bills its documented 2500 default); on SetStudioProfile an omitted value keeps the stored one."},` +
			`"studioId":{"type":"string","description":"Optional bare NanoID for the new studio vertex (CreateStudio); absent → minted."},` +
			`"location":{"type":"string","description":"Optional vtx.<locType>.<NanoID> location the studio is at (CreateStudio; validated alive + an admitted location type segment; writes the locatedAt link). Listed in ContextHint.Reads when supplied."},` +
			`"studioKey":{"type":"string","description":"vtx.studio.<NanoID> of an existing studio (TombstoneStudio, SetStudioProfile; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.studio.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"name":           "The studio's display name. Stored on the .profile aspect (CreateStudio: required; SetStudioProfile: optional — supplied replaces it, omitted keeps it).",
			"noShowFeeCents": "The studio's no-show policy in whole cents, stored on the .profile aspect (CreateStudio, SetStudioProfile; optional). Validated a non-negative integer at both writers (InvalidArgument otherwise). SetBookingAttendance reads it when a noShow mark names no fee of its own: a positive value is billed, 0 bills nothing, and a studio with no policy recorded bills the documented 2500 default. On SetStudioProfile an omitted value keeps the stored policy.",
			"studioId":       "Optional bare NanoID (no dots / key segments) for the new studio vertex. Absent → minted with nanoid.new().",
			"location":       "Optional full vtx.<locType>.<NanoID> key of a location-domain location (e.g. a building) the studio is at. Validated alive + an admitted location type segment; CreateStudio writes the studio locatedAt location link (no authZ meaning — browse reachability only). MUST be listed in ContextHint.Reads when supplied.",
			"studioKey":      "Full vtx.studio.<NanoID> key of an existing studio vertex to tombstone (TombstoneStudio) or whose profile to edit (SetStudioProfile; its .profile aspect MUST be listed in ContextHint.Reads alongside the vertex).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateStudio — register a studio",
				Payload: map[string]any{"name": "Sunrise Yoga Room"},
				ExpectedOutcome: "Mints vtx.studio.<NanoID> (class=studio, root {}) + the .profile aspect " +
					"{name}. Returns primaryKey (the studio key).",
			},
			{
				Name:    "CreateStudio — register a studio at a location",
				Payload: map[string]any{"name": "Sunrise Yoga Room", "location": "vtx.building.<NanoID>"},
				ExpectedOutcome: "Mints the studio + .profile as above, validates the location is alive + " +
					"an admitted location type segment, and writes lnk.studio.<id>.locatedAt.building.<NanoID> (class locatedAt). " +
					"Rejects a dead location or a key that is not a location key.",
			},
			{
				Name:    "CreateStudio — register a studio with a no-show policy",
				Payload: map[string]any{"name": "Sunrise Yoga Room", "noShowFeeCents": 1000},
				ExpectedOutcome: "Mints the studio + .profile {name, noShowFeeCents: 1000}: a member marked noShow " +
					"on one of its classes with no fee named on the mark is billed $10. Rejects a negative or " +
					"fractional noShowFeeCents (InvalidArgument).",
			},
			{
				Name:            "TombstoneStudio — retire a studio",
				Payload:         map[string]any{"studioKey": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the studio vertex. Returns primaryKey. Rejects an absent / already-dead studio, a studio with a still-upcoming class (HasUpcomingClasses), and a front-of-house caller who does not worksAt the studio's building (AuthDenied).",
			},
			{
				Name:    "SetStudioProfile — record a fee-free no-show policy",
				Payload: map[string]any{"studioKey": "vtx.studio.<NanoID>", "noShowFeeCents": 0},
				ExpectedOutcome: "Upserts .profile with noShowFeeCents: 0 and the stored name carried forward " +
					"(OCC on the profile's read revision). Returns primaryKey. Rejects an absent / dead studio " +
					"(UnknownStudio), a payload naming neither name nor noShowFeeCents, a negative or fractional " +
					"fee (InvalidArgument), and a front-of-house caller who does not worksAt the studio's building (AuthDenied).",
			},
		},
	}
}

func studioProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     studioProfileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateStudio", "SetStudioProfile"},
		Description: "Studio profile aspect (wellness). Stored as vtx.studio.<NanoID>.profile (class " +
			"studioProfile) = {name, noShowFeeCents?}. Non-sensitive. Written by CreateStudio (mints it) and " +
			"SetStudioProfile (merges supplied fields over it under OCC); the studio vertexType DDL owns the " +
			"script and this aspect-type DDL is the step-6 write gate. noShowFeeCents is the studio's no-show " +
			"policy in whole cents — a non-negative integer, 0 meaning fee-free, absent meaning no policy " +
			"recorded — read by SetBookingAttendance when a noShow mark names no fee of its own. " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string"},"noShowFeeCents":{"type":"integer","minimum":0}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name":           "The studio's display name.",
			"noShowFeeCents": "The studio's no-show policy in whole cents (non-negative integer; 0 = fee-free; absent = no policy recorded, so SetBookingAttendance bills its documented default).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "studio profile aspect",
				Payload:         map[string]any{"name": "Sunrise Yoga Room"},
				ExpectedOutcome: "Stored as vtx.studio.<NanoID>.profile; written by CreateStudio.",
			},
			{
				Name:            "studio profile aspect with a no-show policy",
				Payload:         map[string]any{"name": "Sunrise Yoga Room", "noShowFeeCents": 1000},
				ExpectedOutcome: "Stored as vtx.studio.<NanoID>.profile; written by CreateStudio or SetStudioProfile. A noShow mark on one of the studio's classes that names no fee bills $10.",
			},
		},
	}
}

func sessionVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries", "TombstoneSessionSeries", "ReassignSessionSeries", "ExtendSessionSeries"},
		Description: "Wellness session DDL. Vertex shape: vtx.session.<NanoID>, class=session, root data = {} " +
			"(minimal, D5). CreateSession validates the studio is alive + class=studio, then atomically mints the " +
			"session + the .schedule aspect {name, startsAt, endsAt, capacity, priceCents?, residentPriceCents?} + the atStudio link " +
			"(session→studio, Contract #1 §1.1 later-arriving source) + one atLocation link (session→location) per " +
			"location the studio sits at right now, per its locatedAt link(s) — a snapshot of where the class MEETS, " +
			"re-taken from the new studio's locatedAt links whenever ReassignSession moves the class, so it always " +
			"names the room the class most recently sat in. session_locations falls back to it once the studio is " +
			"tombstoned (TombstoneStudio soft-deletes with no cascade onto " +
			"locatedAt), mirroring clinic-domain's atSite fallback for a tombstoned provider. The studio's booking grid is a mandatory " +
			"15-minute cadence (mirrors clinic-domain's appointment grid exactly): CreateSession discretizes " +
			"[startsAt,endsAt) into its covered 15-minute cells and CLAIMS a deterministic studioSlotClaim aspect " +
			"per cell on the studio hub (vtx.studio.<s>.slot<cellcode>) — the write-path CreateOnly/expectedRevision " +
			"conditioning on each cell key IS the double-book lock (Capability-KV §06 — the op's own Starlark " +
			"logic): a live claim on any covered cell rejects with StudioConflict (no two overlapping sessions in " +
			"the same studio). CreateSession also accepts an optional instructor param (vtx.instructor.<NanoID>, " +
			"validated alive + class=instructor): when supplied it writes the session ledBy instructor LINK " +
			"(lnk.session.<id>.ledBy.instructor.<iid>, Contract #1 §1.1 later-arriving source), beside atStudio — " +
			"omitted means the session carries no instructor (persona-worlds-design.md Fire W0). CreateSession also " +
			"accepts an optional priceCents (integer, >= 0 when supplied) — the class's price for a booking, stored " +
			"on .schedule alongside capacity; 0 or omitted means a free class (verticals.md \"a wellness class still " +
			"has no price\"). It also accepts an optional residentPriceCents (integer, >= 0 when supplied) — the " +
			"price a booking whose .status.rate is \"resident\" is charged instead of priceCents; omitted means a " +
			"resident pays the same priceCents as a standard booker (verticals.md \"a verified resident is charged " +
			"the same class price as a walk-in\"). wellness-ledger's wellnessClassPriceSettlement lens reads both, " +
			"selecting residentPriceCents only when the booking's own rate is resident AND the session declares one — " +
			"a resident booking on a session with no residentPriceCents falls back to priceCents, exactly like a " +
			"standard booking. wellness-ledger's wellnessClassPriceSettlement lens reads it to auto-charge the " +
			"booker's ledger account once one exists — the same convergence idiom SetBookingAttendance's " +
			"noShowFeeCents uses, just unconditional on attendance rather than gated on a noShow. TombstoneSession " +
			"requires the session's actual studio (verified via the atStudio link) to release the held slot cells " +
			"in the same atomic batch, then soft-deletes the session (no cascade onto its bookings — they simply " +
			"drop from the wellnessBookings roster's session join). It refuses SessionStarted once the class has " +
			"begun (submittedAt compared against .schedule.startsAt, same at-the-boundary reading as CancelBooking: " +
			"starting exactly at submittedAt counts as started) — a started class is a record, not a booking, and " +
			"TombstoneSessionSeries's own occurrence walk skips history for the same reason. TombstoneSession's standing guard: the " +
			"operator passes unconditionally; a bound instructor may additionally cancel only a class THEY " +
			"lead — the caller supplies the instructor param and the script requires BOTH " +
			"lnk.session.<id>.ledBy.instructor.<iid> AND lnk.instructor.<iid>.identifiedBy.identity.<actor> to be " +
			"alive (known keys), rejecting AuthDenied otherwise; front-of-house staff hold no TombstoneSession " +
			"grant. ReassignSession edits a LIVE session's instructor, studio and/or time WITHOUT cancelling its " +
			"bookings — the gap TombstoneSession's all-or-nothing cancel otherwise forces a sub or a moved class " +
			"through (releasing every seat). It requires sessionKey and at least one of: newInstructor " +
			"(assign/replace the class's instructor — validated alive + class=instructor), clearInstructor=true " +
			"(remove it, no replacement), newStudio (move the class to a different live studio — operator-only, " +
			"validated alive + class=studio), a startsAt+endsAt pair (move the class; re-validated against the " +
			"15-minute grid + 96-cell span cap, exactly like CreateSession), or an edit to name/capacity/" +
			"priceCents/residentPriceCents (re-validated with CreateSession's own bounds). studio (the session's CURRENT studio, " +
			"validated via atStudio, same as TombstoneSession) is required UNLESS the caller is an operator " +
			"supplying newStudio, in which case it is derived server-side off the session's still-live atStudio " +
			"link — the repair path for a session whose OWN studio TombstoneStudio already tombstoned " +
			"(verticals.md \"retiring a studio strands its classes\"), since wellnessSessionsSpec's projection " +
			"(lenses.go) can no longer hand a caller that dead key back to round-trip. Instructor swap tombstones " +
			"the session's current ledBy link (if any, found by the same bounded link-enumeration idiom " +
			"studio_locations already uses in this file for a studio's locatedAt links — a session carries at most " +
			"one) and writes a new one, mirroring identity-hygiene's tombstone-old+create-new link-swap idiom " +
			"(ddls.go). A studio move (newStudio, operator-only regardless of hat) tombstones the CURRENT atStudio " +
			"link and writes a new one the same way, releasing every cell the OLD studio held for the session's " +
			"span and claiming every cell the NEW studio needs for it — a hub change, not a delta, so no " +
			"symmetric-difference optimization applies. It also RE-SNAPSHOTS the session's atLocation links from " +
			"the new studio's locatedAt targets — tombstoning the links naming rooms the class no longer meets in " +
			"and writing (or reviving, for a room it has sat in before) one per room it now does — so the " +
			"where-does-this-class-meet fact the booker read model and the retired-studio fallback both rely on " +
			"follows the class instead of naming the room it was created in. A location the old and new studio " +
			"share keeps its live link untouched. A time move on an UNCHANGED studio instead computes the " +
			"OLD and NEW covered cell sets and mutates only the symmetric difference — cells the new span still " +
			"covers are left untouched (no release+reclaim round-trip against itself), cells only the old span " +
			"covered are released, and cells only the new span covers are claimed via the same CreateOnly " +
			"claim_cell double-book guard CreateSession uses (StudioConflict on collision with another session); " +
			"the schedule aspect's name/capacity/priceCents/residentPriceCents are edited when the caller supplies " +
			"them (name non-empty, capacity re-validated 1..200, priceCents/residentPriceCents re-validated >= 0 — " +
			"the same bounds CreateSession enforces) and otherwise carried forward unchanged, OCC-conditioned on its " +
			"current revision — a class no longer needs TombstoneSession + recreate (stranding every booking) just " +
			"to add seats, fix a typo'd name, or reprice. A capacity LOWER than the current one is refused " +
			"CapacityBelowSeated when any seat cell in the removed range (new capacity+1 .. current capacity) is " +
			"claimed — judged by the HIGHEST claimed seat index, not the seated count, because seat indexes are never " +
			"compacted (seats 1 and 5 claimed on a class of five refuse a shrink to 3 even though two fit), and the " +
			"refusal names that seat. The refusal is judged on the hydrated cells and is advisory against a " +
			"concurrent claim: the op writes no seat cell, so a shrink to 4 racing a CreateBooking that claims seat5 " +
			"both commit — that seat sits one above capacity, invisible to claim_free_seats (which walks " +
			"1..capacity), released by its own cancellation, and the card's seated count reads one above capacity " +
			"for that seat's life; nothing is orphaned. The shrink reads only the removed range (at most the delta, " +
			"≤ 199 cells; a dispatcher declaring none of them, such as the lattice CLI, pays that delta as live " +
			"reads — up to 199 for a 200→1 shrink); a raise reads no seat cell. " +
			"Standing binder mirrors the union of CreateSession's and TombstoneSession's: the operator passes " +
			"unconditionally; a non-operator caller supplying its own bound instructor (validated via ledBy + " +
			"identifiedBy, same as TombstoneSession) may reassign/reschedule only a class THEY currently lead; " +
			"absent that, front-of-house staff may act workplace-confined to the studio's own location " +
			"(enforce_workplace, same as CreateSession's staff path). CreateSessionSeries (dispatched under the " +
			"sessionseries DDL below, sessionSeriesVertexTypeDDL) mints occurrenceCount session vertices on a " +
			"cadence, TombstoneSessionSeries (same DDL) tombstones the still-upcoming ones and releases their " +
			"cells, and ReassignSessionSeries (same DDL) moves the still-upcoming ones by one shared delta, " +
			"re-writing each one's .schedule and migrating its cells — this DDL's PermittedCommands lists all three " +
			"because every session/schedule/slot-claim mutation " +
			"they emit is still governed, per mutation, by ITS OWN class's write gate (step6_validate.go), regardless " +
			"of which DDL's script executed the op.",
		Script: sessionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"studio":{"type":"string","description":"vtx.studio.<NanoID> the session runs at (CreateSession; required, validated alive + class=studio; on TombstoneSession/ReassignSession it must be the session's actual studio, validated via the atStudio link — on ReassignSession only, omittable when an operator supplies newStudio, in which case it is derived server-side instead)."},` +
			`"newStudio":{"type":"string","description":"vtx.studio.<NanoID> to move the session to (ReassignSession; operator-only regardless of hat; validated alive + class=studio). Releases every cell the old studio held for the session's span and claims every cell the new one needs (StudioConflict on collision), and re-snapshots the session's atLocation links from the new studio's locations so the class's where-it-meets fact follows the move."},` +
			`"name":{"type":"string","description":"The session's display name, e.g. Vinyasa Flow (CreateSession; required. ReassignSession; optional edit — non-empty when supplied, else carried forward unchanged)."},` +
			`"startsAt":{"type":"string","description":"Session start, RFC3339 (CreateSession; required. ReassignSession; optional, must be paired with endsAt). Aligned to the 15-minute booking grid (:00/:15/:30/:45; SlotGridViolation otherwise)."},` +
			`"endsAt":{"type":"string","description":"Session end, RFC3339 (CreateSession; required. ReassignSession; optional, must be paired with startsAt). Aligned to the 15-minute grid; span capped at 96 cells / 24h (SessionTooLong)."},` +
			`"capacity":{"type":"integer","description":"Maximum concurrent bookings (CreateSession; required, 1..200). Bounds the seat-claim loop CreateBooking walks. ReassignSession; optional edit, re-validated 1..200, else carried forward unchanged; a shrink is refused CapacityBelowSeated when a seat above the new capacity is claimed (by the highest claimed seat index, since seats are never compacted)."},` +
			`"priceCents":{"type":"integer","description":"Optional class price in integer cents (CreateSession; optional, must be >= 0 when supplied). 0 or omitted means a free class. Stored on the .schedule aspect; wellness-ledger's wellnessClassPriceSettlement lens reads it to auto-charge the booker's ledger account. ReassignSession; optional edit (re-validated >= 0), else carried forward unchanged."},` +
			`"residentPriceCents":{"type":"integer","description":"Optional resident class price in integer cents (CreateSession; optional, must be >= 0 when supplied). Charged instead of priceCents to a booking whose .status.rate is resident; omitted means a resident pays priceCents same as a standard booker. Stored on the .schedule aspect. ReassignSession; optional edit (re-validated >= 0), else carried forward unchanged."},` +
			`"instructor":{"type":"string","description":"Optional vtx.instructor.<NanoID> leading the session (CreateSession; validated alive + class=instructor; writes the ledBy link). Listed in ContextHint.Reads when supplied. On TombstoneSession/ReassignSession, required for an instructor (non-operator) caller acting on their OWN class — validated via ledBy + identifiedBy."},` +
			`"sessionId":{"type":"string","description":"Optional bare NanoID for the new session vertex (CreateSession); absent → minted."},` +
			`"sessionKey":{"type":"string","description":"vtx.session.<NanoID> of an existing session (TombstoneSession/ReassignSession; required, validated alive)."},` +
			`"newInstructor":{"type":"string","description":"vtx.instructor.<NanoID> to assign as the session's instructor going forward (ReassignSession; validated alive + class=instructor; mutually exclusive with clearInstructor). Replaces any existing ledBy link."},` +
			`"clearInstructor":{"type":"boolean","description":"Remove the session's instructor with no replacement (ReassignSession; mutually exclusive with newInstructor)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.session.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"studio":             "Full vtx.studio.<NanoID> key the session runs at. CreateSession validates it is alive + class=studio, writes the atStudio link, and claims one studioSlotClaim aspect per covered 15-minute cell. TombstoneSession/ReassignSession also require it (the session's actual studio, validated via the atStudio link) — TombstoneSession to release the held cells, ReassignSession to workplace-confine a front-of-house caller and to claim/release cells on a time move. On ReassignSession only, an operator supplying newStudio may omit it — the script derives the current studio off the session's atStudio link instead, the repair path for a session whose own studio was already tombstoned.",
			"newStudio":          "Full vtx.studio.<NanoID> key to move the session to (ReassignSession; operator-only regardless of hat, validated alive + class=studio). Tombstones the current atStudio link and writes a new one; releases every cell the old studio held for the session's span and claims every cell the new one needs (StudioConflict on collision with another session); and re-snapshots the session's atLocation links from the new studio's own locatedAt targets, so the class's guest bookers and the retired-studio fallback both resolve to the room it now meets in rather than the one it was created in.",
			"name":               "The session's display name (CreateSession; required. ReassignSession; optional edit — non-empty when supplied, else carried forward unchanged).",
			"startsAt":           "Session start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateSession; required. ReassignSession; optional — moves the class, must be paired with endsAt). Must align to the 15-minute grid (SlotGridViolation).",
			"endsAt":             "Session end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateSession; required. ReassignSession; optional — must be paired with startsAt). Must align to the 15-minute grid; span capped at 96 cells / 24h (SessionTooLong).",
			"capacity":           "Maximum concurrent bookings, an integer 1..200 (CreateSession; required). Stored on the .schedule aspect; CreateBooking reads it to bound the seat-claim loop (SessionFull once exhausted). ReassignSession; optional edit, re-validated 1..200, else carried forward unchanged. A shrink below the current capacity is refused CapacityBelowSeated when any seat cell in the removed range is still claimed — judged by the HIGHEST claimed seat index, not the seated count (seat indexes are never compacted: seats 1 and 5 claimed on a class of five refuse a shrink to 3), and the refusal names that seat. A raise reads no seat cell.",
			"priceCents":         "Optional class price in integer cents (CreateSession; must be >= 0 when supplied). Stored on the .schedule aspect; 0 or omitted means a free class. wellness-ledger's wellnessClassPriceSettlement lens reads it to auto-charge the booker's ledger account once one exists. ReassignSession; optional edit (re-validated >= 0), else carried forward unchanged, exactly like name/capacity.",
			"residentPriceCents": "Optional resident class price in integer cents (CreateSession; must be >= 0 when supplied). Stored on the .schedule aspect; charged instead of priceCents to a booking whose .status.rate is resident. Omitted means a resident pays the same priceCents as a standard booker. ReassignSession; optional edit (re-validated >= 0), else carried forward unchanged, exactly like priceCents.",
			"instructor":         "Optional full vtx.instructor.<NanoID> key leading the session. CreateSession validates it is alive + class=instructor and writes the ledBy link; MUST be listed in ContextHint.Reads when supplied. TombstoneSession's/ReassignSession's standing guard requires it (plus the caller's own identifiedBy binding to it) for a non-operator, instructor-role caller to act on their own class.",
			"sessionId":          "Optional bare NanoID (no dots / key segments) for the new session vertex. Absent → minted with nanoid.new().",
			"sessionKey":         "Full vtx.session.<NanoID> key of an existing session. TombstoneSession releases its held studioSlotClaim cells then tombstones it. ReassignSession edits it in place.",
			"newInstructor":      "Full vtx.instructor.<NanoID> key to assign as the session's instructor going forward (ReassignSession; validated alive + class=instructor). Replaces any existing ledBy link; mutually exclusive with clearInstructor. Omitting both newInstructor and clearInstructor leaves the instructor unchanged.",
			"clearInstructor":    "Boolean (ReassignSession). true removes the session's instructor with no replacement; mutually exclusive with newInstructor.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateSession — schedule a class",
				Payload: map[string]any{
					"studio":   "vtx.studio.<NanoID>",
					"name":     "Vinyasa Flow",
					"startsAt": "2026-07-08T09:00:00Z",
					"endsAt":   "2026-07-08T10:00:00Z",
					"capacity": 20,
				},
				ExpectedOutcome: "Validates the studio is alive + class=studio and startsAt/endsAt align to the " +
					"15-minute grid. Atomically commits vtx.session.<NanoID> (root {}) + .schedule {name, startsAt, " +
					"endsAt, capacity} + the atStudio link + one studioSlotClaim aspect per covered 15-minute cell. " +
					"Returns primaryKey. Rejects on an absent/dead/wrong-class studio, a misaligned start/end " +
					"(SlotGridViolation), or a studio double-book (StudioConflict).",
			},
			{
				Name: "CreateSession — schedule a class with an instructor",
				Payload: map[string]any{
					"studio":     "vtx.studio.<NanoID>",
					"instructor": "vtx.instructor.<NanoID>",
					"name":       "Vinyasa Flow",
					"startsAt":   "2026-07-08T09:00:00Z",
					"endsAt":     "2026-07-08T10:00:00Z",
					"capacity":   20,
				},
				ExpectedOutcome: "As above, plus validates the instructor is alive + class=instructor and writes " +
					"lnk.session.<id>.ledBy.instructor.<NanoID>. Rejects a dead / wrong-class instructor.",
			},
			{
				Name: "CreateSession — schedule a priced class",
				Payload: map[string]any{
					"studio":     "vtx.studio.<NanoID>",
					"name":       "Vinyasa Flow",
					"startsAt":   "2026-07-08T09:00:00Z",
					"endsAt":     "2026-07-08T10:00:00Z",
					"capacity":   20,
					"priceCents": 1500,
				},
				ExpectedOutcome: "As the plain CreateSession, plus .schedule carries priceCents: 1500. A booking on " +
					"this session converges wellness-ledger's wellnessClassPriceSettlement gap once the booker has a " +
					"ledger account. Omitting priceCents (or supplying 0) leaves the class free — no charge is ever " +
					"generated. A negative priceCents is rejected (InvalidArgument).",
			},
			{
				Name: "CreateSession — schedule a class with a resident price",
				Payload: map[string]any{
					"studio":             "vtx.studio.<NanoID>",
					"name":               "Vinyasa Flow",
					"startsAt":           "2026-07-08T09:00:00Z",
					"endsAt":             "2026-07-08T10:00:00Z",
					"capacity":           20,
					"priceCents":         1500,
					"residentPriceCents": 1000,
				},
				ExpectedOutcome: "As the priced-class CreateSession, plus .schedule carries residentPriceCents: 1000. " +
					"A booking whose .status.rate is resident converges wellnessClassPriceSettlement at 1000; a " +
					"standard booking still converges at priceCents: 1500. Omitting residentPriceCents charges every " +
					"booker priceCents regardless of rate. A negative residentPriceCents is rejected (InvalidArgument).",
			},
			{
				Name:    "TombstoneSession — cancel a scheduled session (operator or front-of-house)",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Validates the session is alive + class=session and the supplied studio is its " +
					"actual studio (via the atStudio link), releases every held studioSlotClaim cell, then soft-" +
					"deletes the session. Returns primaryKey.",
			},
			{
				Name:    "TombstoneSession — an instructor cancels their own class",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "instructor": "vtx.instructor.<NanoID>"},
				ExpectedOutcome: "As above, plus (for a non-operator caller) requires lnk.session.<id>.ledBy.instructor.<NanoID> " +
					"AND lnk.instructor.<NanoID>.identifiedBy.identity.<actor> to both be alive — rejects AuthDenied " +
					"if the caller is not the session's own bound instructor.",
			},
			{
				Name: "ReassignSession — sub in a substitute instructor",
				Payload: map[string]any{
					"sessionKey":    "vtx.session.<NanoID>",
					"studio":        "vtx.studio.<NanoID>",
					"newInstructor": "vtx.instructor.<NanoID>",
				},
				ExpectedOutcome: "Validates the session is alive + class=session, the supplied studio is its actual " +
					"studio, and the new instructor is alive + class=instructor. Tombstones any existing ledBy link " +
					"and writes the new one. Bookings, schedule, and slot claims are untouched. Returns primaryKey.",
			},
			{
				Name:    "ReassignSession — move a class to a new time",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "startsAt": "2026-07-08T10:00:00Z", "endsAt": "2026-07-08T10:30:00Z"},
				ExpectedOutcome: "Validates the new span against the 15-minute grid + 96-cell cap, releases studioSlotClaim " +
					"cells only the OLD span held, and claims cells only the NEW span needs (StudioConflict on collision " +
					"with another session). The .schedule aspect's startsAt/endsAt update; name/capacity/priceCents/" +
					"residentPriceCents/bookings carry forward unchanged. Returns primaryKey.",
			},
			{
				Name:    "ReassignSession — grow capacity and reprice a scheduled class",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "capacity": 30, "priceCents": 1800},
				ExpectedOutcome: "Re-validates capacity in [1, 200] and priceCents >= 0 (same bounds CreateSession " +
					"enforces), then OCC-conditions the .schedule aspect with the new values — a full class no longer " +
					"needs TombstoneSession + recreate (which would strand every existing booking) just to add seats " +
					"or fix a price. name/residentPriceCents/startsAt/endsAt/instructor/bookings/slot claims are " +
					"untouched. Rejects InvalidArgument on capacity > 200 or a negative price. Returns primaryKey.",
			},
			{
				Name:    "ReassignSession — shrink a class under a claimed seat",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "capacity": 3},
				ExpectedOutcome: "On a class of five with seats 1 and 5 claimed: refused CapacityBelowSeated naming seat 5 " +
					"(\"seat 5 is claimed; capacity cannot drop below 5\") — the removed range 4..5 is read from the top " +
					"and the first claimed cell decides, so the rule is the highest claimed index, not the seated count " +
					"(two members fit in three seats, but seat 5's holder would be orphaned; seats are never compacted). " +
					"With seats 1 and 2 claimed instead, capacity 3 (and 2) is accepted and capacity 1 is refused naming " +
					"seat 2. Nothing is written on a refusal.",
			},
			{
				Name:    "ReassignSession — fix a typo'd class name",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "name": "Vinyasa Flow"},
				ExpectedOutcome: "Rewrites the .schedule aspect's name field only; capacity/priceCents/residentPriceCents/" +
					"startsAt/endsAt/instructor/bookings/slot claims are untouched. Returns primaryKey.",
			},
			{
				Name:    "ReassignSession — operator moves a class to a different studio",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "newStudio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Operator-only. Validates the new studio is alive + class=studio, tombstones the CURRENT " +
					"atStudio link and writes a new one, releases every cell the OLD studio held for the session's span, " +
					"and claims every cell the NEW studio needs for it (StudioConflict on collision). The session's " +
					"atLocation links are re-snapshotted from the new studio's locations. Instructor, time, " +
					"bookings and other schedule fields are untouched. Returns primaryKey.",
			},
			{
				Name:    "ReassignSession — operator repairs a session whose studio was already tombstoned",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "newStudio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "studio is omitted — only an operator supplying newStudio may do this. The script derives " +
					"the session's CURRENT (now-dead) studio off its still-live atStudio link, then moves the session " +
					"exactly as the studio-move example above. Rejects AuthDenied for any non-operator caller, and " +
					"InvalidArgument if newStudio is also omitted.",
			},
			{
				Name:    "ReassignSession — an instructor reschedules their own class",
				Payload: map[string]any{"sessionKey": "vtx.session.<NanoID>", "studio": "vtx.studio.<NanoID>", "instructor": "vtx.instructor.<NanoID>", "startsAt": "2026-07-08T11:00:00Z", "endsAt": "2026-07-08T11:30:00Z"},
				ExpectedOutcome: "As above, plus (for a non-operator caller) requires lnk.session.<id>.ledBy.instructor.<NanoID> " +
					"AND lnk.instructor.<NanoID>.identifiedBy.identity.<actor> to both be alive — rejects AuthDenied if " +
					"the caller is not the class's own bound instructor. A front-of-house caller instead needs no " +
					"instructor param but must worksAt a location covering the studio.",
			},
		},
	}
}

// sessionSeriesVertexTypeDDL declares the studio recurring-class series
// parent — CreateSession run occurrenceCount times on a fixed intervalDays
// cadence, eagerly, in one atomic op ("Every occurrence of a weekly class is
// hand-created", verticals.md). Mirrors clinic-reminders' visitSeries family
// in spirit (a rolling recurrence definition) but NOT its mechanism: a
// visitSeries only tracks a rolling "next due" deadline for a human to act
// on later (its AdvanceVisitSeries writes no appointment), whereas a
// wellness class series must exist AHEAD of time so a member can browse and
// book it — there is nothing to wait on, every occurrence's shape (name,
// studio, capacity, price, instructor) is fully known at series-creation
// time. So this stays a bounded, eager batch (CreateSession's own
// mutation-building extended over a loop) rather than a lens + Weaver
// directOp rolling series: no new engine primitive, no unbounded background
// materialization to design. (The @every/ScheduleEvery schedule primitive
// — the backlog row's other cited precedent — is engine-internal only,
// internal/weaver's own temporal sweep; no package script can invoke it,
// confirmed by grep across packages/.) A series the desk marks `rolling` is
// the same eager batch with a moving window: CreateSessionSeries records a
// .horizon aspect (the next occurrence on the cadence, and extendAt, the
// start of the window's earliest class), the wellnessSeriesHorizon lens arms
// a deadline on extendAt, and the recorded lapse dispatches
// ExtendSessionSeries (Weaver's actor only), which mints exactly one
// occurrence with the shared mint_occurrence shape and moves the horizon by
// one interval — so a rolling series always has occurrenceCount cadence
// slots on the books ahead of its earliest one. The move shifts the roll;
// the call-off stops it when its walk succeeds, and StopSessionSeries stops
// it without walking (the off switch for a run whose history has outgrown
// the walk); a non-rolling series carries no .horizon and is untouched.
func sessionSeriesVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionSeriesVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateSessionSeries", "TombstoneSessionSeries", "ReassignSessionSeries", "ExtendSessionSeries", "StopSessionSeries"},
		Description: "Wellness recurring-class series DDL. Vertex shape: vtx.sessionseries.<NanoID>, " +
			"class=sessionseries, root data = {} (minimal, D5 — the data lives in the .definition aspect). " +
			"CreateSessionSeries validates the studio alive + class=studio and, when supplied, the instructor alive " +
			"+ class=instructor — ONCE, shared by every occurrence — applies the same staff-standing workplace " +
			"confinement CreateSession uses (require_workplace off the studio's own location, operator-exempt), then " +
			"for each occurrence i in [0, occurrenceCount): mints vtx.session.<NanoID> with .schedule {name, " +
			"startsAt: startsAt + i·intervalDays days, endsAt: endsAt + i·intervalDays days, capacity, priceCents?, " +
			"residentPriceCents?} " +
			"(identical shape to CreateSession's own), the atStudio + per-location atLocation links, the optional " +
			"ledBy instructor link, one studioSlotClaim per covered 15-minute cell (StudioConflict on any " +
			"occurrence's collision rejects the WHOLE series atomically — no partial series left behind), and a " +
			"partOf link back to the series (lnk.session.<id>.partOf.sessionseries.<sid>, sentence \"session partOf " +
			"sessionseries\" — Contract #1 §1.1: the series is the pre-existing anchor its occurrences point at, " +
			"mirroring atStudio's own studio-is-target direction). Mints the sessionseries vertex + its .definition " +
			"aspect {name, capacity, priceCents?, residentPriceCents?, intervalDays, occurrenceCount, firstStartsAt, firstEndsAt} once, " +
			"plus its own atStudio link. With the optional boolean rolling true it also mints the .horizon aspect " +
			"(class sessionSeriesHorizon) = {nextStartsAt, nextEndsAt, extendAt, instructor?, mintedCount}: next* is " +
			"the occurrence after the batch's last on the cadence, extendAt = nextStartsAt − occurrenceCount·intervalDays " +
			"days (= firstStartsAt at creation, the start of the window's earliest class), instructor the series' " +
			"authored instructor key (absent when none was named), mintedCount = occurrenceCount. The " +
			"wellnessSeriesHorizon lens (lenses.go) arms freshUntil on extendAt and, once the recorded lapse " +
			"reaches it, dispatches ExtendSessionSeries {seriesKey, studio, startsAt, endsAt, instructor?} — " +
			"restricted to Weaver's dispatch actor (AuthDenied otherwise) — which refuses UnknownSeries, " +
			"WrongStudio (the series' own atStudio link, walked), NotRolling (no .horizon.extendAt) and " +
			"StaleHorizon (payload startsAt/endsAt/instructor ≠ .horizon's next*/instructor — a stale row is " +
			"refused, never trusted), then mints-or-skips: the occurrence is skipped (no session) when startsAt is " +
			"before submittedAt (PastOccurrence — the stack was away) or any of its studio/instructor cells is live " +
			"(StudioConflict/InstructorConflict — the slot was booked one-off, so the run passes that week as the " +
			"desk would), otherwise one occurrence is minted with CreateSessionSeries' per-occurrence shape off " +
			".definition's name/capacity/prices — led by the horizon's instructor, or unled when that instructor " +
			"has since been retired (the horizon drops them; the desk assigns a leader per class or stops the " +
			"run); either way .horizon advances by one interval (next* and extendAt " +
			"+= intervalDays days, mintedCount + 1 when minted) and wellness.sessionSeriesExtended {seriesKey, " +
			"studio, startsAt, sessionKey?, skipped?, instructorDropped?} is emitted; returns primaryKey = the " +
			"series key (the op writes its .horizon). Invariant: a rolling series always has occurrenceCount " +
			"cadence slots minted ahead of extendAt (a skipped slot is still a slot). ReassignSessionSeries " +
			"shifts the horizon, and two verbs stop it: TombstoneSessionSeries when its walk succeeds, and " +
			"StopSessionSeries {seriesKey, studio} — walk-free, same grant and workplace confinement, the " +
			"series' atStudio link as the studio confirmation (WrongStudio), NotRolling when there is no " +
			".horizon.extendAt — which rewrites .horizon without extendAt (stoppedAt = submittedAt), touches " +
			"nothing on the grid, emits wellness.sessionSeriesStopped {seriesKey, studio} and returns primaryKey " +
			"= the series key. It is the off switch for a long-lived rolling run whose partOf history has " +
			"outgrown the walk the other two verbs run (see SeriesWalkBound below); what remains on the grid is " +
			"then cancelled or moved per class with TombstoneSession / ReassignSession. " +
			"occurrenceCount is bounded [2, 52] (a single class is CreateSession's job; " +
			"52 is a generous year-of-weekly backstop, not an expected ceiling, mirroring MAX_SLOT_CELLS' own " +
			"backstop framing) and intervalDays [1, 365]. Every minted occurrence remains an ordinary vtx.session — " +
			"ReassignSession/TombstoneSession still edit or cancel any ONE of them individually afterward. " +
			"TombstoneSessionSeries is the whole-run counterpart: given a seriesKey and the series' own studio " +
			"(confirmed against the series' atStudio link, WrongStudio otherwise, the same confirmation shape " +
			"TombstoneSession requires), it walks the series' partOf-in occurrences and cancels every one that is " +
			"still live, still at that studio, and STARTS AFTER submittedAt — tombstoning each session and " +
			"releasing its studioSlotClaim cells plus its current instructor's instructorSlotClaim cells, exactly " +
			"the footprint TombstoneSession leaves per class. An occurrence that has already started is left alone: " +
			"it is history, and cancelling it would hand its attended bookings to ReleaseOrphanedBooking, which " +
			"drains the seat and refunds a class that actually ran. An occurrence ReassignSession has since moved " +
			"to a DIFFERENT studio is also left alone (cancel it with TombstoneSession, which confirms its own " +
			"studio) — the standing this op clears is one studio's. On a rolling series it also stops the roll: " +
			".horizon is rewritten without extendAt and with stoppedAt = submittedAt (next*, instructor and " +
			"mintedCount carried), so the lens arms nothing further, and a rolling series with nothing left to " +
			"cancel still stops rather than refusing. On a non-rolling series zero eligible occurrences is a refusal " +
			"(NoUpcomingOccurrences), not a silent no-op. The SERIES VERTEX IS NOT TOMBSTONED: its already-run " +
			"occurrences stay partOf it, and that parentage is the only record they were one recurring class. " +
			"Bounded read cost on an eager series: at most occurrenceCount (<= 52) occurrences, each costing one " +
			"read (its .schedule — history is skipped on that alone) plus, for a still-upcoming one, its liveness " +
			"and its atStudio link and, for the ones actually cancelled, the ledBy walk (its own liveness " +
			"re-check and one single-link page) — the enumeration itself is 2 pages of 64, which strictly " +
			"exceeds the largest set an eager series can have, and a cursor still open past that budget refuses " +
			"SeriesWalkBound rather than reporting a partial call-off. A ROLLING series' partOf history grows by " +
			"one per window move and is never pruned, so a long-lived rolling run outgrows that budget (and, " +
			"before it, the Starlark wall on the per-occurrence reads): its whole-run call-off and move refuse " +
			"SeriesWalkBound, StopSessionSeries is its off switch, and per-class TombstoneSession / " +
			"ReassignSession handle what remains. Emits wellness.sessionSeriesCancelled {seriesKey, studio, sessionKeys, stoppedRolling} and returns NO primaryKey " +
			"(the reply constraint admits only a key the op wrote, and the occurrences it cancels are the reply's subject, not the series). Standing is CreateSessionSeries's own workplace confinement on the same studio " +
			"(operator-exempt); there is no instructor path — an instructor cancels the class they lead, not a " +
			"studio's standing booking. ReassignSessionSeries is the whole-run counterpart of ReassignSession's " +
			"time move, on the same seriesKey + studio confirmation, standing binder and partOf-in walk as " +
			"TombstoneSessionSeries: the caller PINS the anchor — anchorKey, the occurrence it saw as the next " +
			"class, and anchorStartsAt, that class's start as it saw it — and startsAt/endsAt name the anchor's " +
			"NEW instants; every still-upcoming occurrence still held at the confirmed studio moves by the same " +
			"shift (startsAt − anchorStartsAt) onto a span of the anchor's new length, on the same studio, each " +
			"keeping its own instructor and its bookings (the seat aspects hang off the untouched session " +
			"vertex). The pin is checked against the walk: when the walk's earliest still-upcoming occurrence is " +
			"not anchorKey, or its schedule no longer starts at anchorStartsAt (the class began or was cancelled " +
			"since the roster loaded, or ReassignSession moved it — the OCC re-execution re-reads both declared " +
			"keys), the op refuses AnchorMoved rather than carrying the run by a shift the desk never intended. " +
			"The new span is validated exactly as ReassignSession's (canonical UTC, strictly ordered, 15-minute " +
			"grid, ≤ 96 cells) and must start after submittedAt (SessionInPast); a shift beyond 366 days, and a " +
			"span identical to the anchor's current one (nothing moves), are refused (InvalidArgument). Cells move " +
			"as ONE batch per hub: across every moved " +
			"occurrence, the cells only the old spans held are released and only the cells no old span held are " +
			"claimed (StudioConflict / InstructorConflict on collision, naming the occurrence that collided, and " +
			"the whole move is rejected — no partial run left behind), so a shift equal to the series' own " +
			"interval, where each occurrence lands on the cells its successor vacates in the same op, is " +
			"accepted rather than tripping over the batch's own releases. Each occurrence's .schedule is " +
			"OCC-rewritten with name/capacity/priceCents/residentPriceCents carried forward and remindAt " +
			"re-derived; the series' .definition is NOT rewritten (firstStartsAt/firstEndsAt stay the minted " +
			"fact — the occurrences' schedules are the schedule of record, as after any ReassignSession), while a " +
			"rolling series' .horizon next*/extendAt shift by the same delta in the same batch (the cadence moved " +
			"with the classes). Same " +
			"skips as the call-off (already-started, individually cancelled, or moved to another studio), the " +
			"same NoUpcomingOccurrences / WrongStudio / SeriesWalkBound refusals, and the same bounded read " +
			"cost plus the ledBy walk per moved occurrence. One operation commits as one atomic batch of at most " +
			"1000 messages (substrate.MaxBatchMessages, two of them the Processor's own), so the assembled " +
			"mutation count is capped at 990 (SeriesTooLarge above it — a 52-occurrence, 5-cell, instructor-led " +
			"run shifted clear of its own cells is 1092, reachable; the same run shifted by its own interval " +
			"writes only the difference and fits). Emits wellness.sessionSeriesMoved {seriesKey, studio, " +
			"sessionKeys, shiftSeconds} and returns NO primaryKey, for the call-off's reason: the occurrences it " +
			"moves are the reply's subject, not the series, whose .horizon it shifts only on a rolling run.",
		Script: sessionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"studio":{"type":"string","description":"vtx.studio.<NanoID> every occurrence runs at (required, validated alive + class=studio, shared by the whole series)."},` +
			`"name":{"type":"string","description":"The display name every occurrence shares, e.g. Evening Flow with Sam (required)."},` +
			`"startsAt":{"type":"string","description":"First occurrence's start, RFC3339 (CreateSessionSeries; required). Aligned to the 15-minute booking grid; later occurrences offset by i*intervalDays days preserve alignment automatically. On ReassignSessionSeries: the NEW start of the earliest still-upcoming occurrence at the confirmed studio (required; must be after submittedAt) — every other still-upcoming occurrence shifts by the same delta."},` +
			`"endsAt":{"type":"string","description":"First occurrence's end, RFC3339 (CreateSessionSeries; required). Aligned to the 15-minute grid; span capped at 96 cells / 24h per occurrence (SessionTooLong). On ReassignSessionSeries: the NEW end of that same earliest occurrence (required); every moved occurrence takes on this span's length."},` +
			`"capacity":{"type":"integer","description":"Maximum concurrent bookings, shared by every occurrence (required, 1..200)."},` +
			`"priceCents":{"type":"integer","description":"Optional per-occurrence class price in integer cents (optional, >= 0 when supplied). 0 or omitted means every occurrence is free."},` +
			`"residentPriceCents":{"type":"integer","description":"Optional per-occurrence resident class price in integer cents (optional, >= 0 when supplied). Charged instead of priceCents to a booking whose .status.rate is resident; omitted means a resident pays priceCents same as a standard booker."},` +
			`"instructor":{"type":"string","description":"Optional vtx.instructor.<NanoID> leading every occurrence (validated alive + class=instructor; writes each occurrence's ledBy link). Listed in ContextHint.Reads when supplied."},` +
			`"intervalDays":{"type":"integer","description":"Days between occurrences, e.g. 7 for weekly (required, 1..365)."},` +
			`"occurrenceCount":{"type":"integer","description":"How many occurrences to mint, first included (required, 2..52 — for a single class use CreateSession instead)."},` +
			`"rolling":{"type":"boolean","description":"Optional (CreateSessionSeries). When true the series keeps occurrenceCount classes on the books: a .horizon aspect is minted and ExtendSessionSeries mints the next occurrence each time the window's earliest class starts, until StopSessionSeries (or a call-off) stops it. Omitted or false: the batch is the series' whole life."},` +
			`"seriesKey":{"type":"string","description":"vtx.sessionseries.<NanoID> of an existing series to call off, move or extend (TombstoneSessionSeries/ReassignSessionSeries/ExtendSessionSeries; required, validated alive + class=sessionseries)."},` +
			`"anchorKey":{"type":"string","description":"vtx.session.<NanoID> the caller saw as the series' next still-upcoming occurrence at the studio (ReassignSessionSeries; required). AnchorMoved unless it is still the walk's earliest eligible occurrence."},` +
			`"anchorStartsAt":{"type":"string","description":"That occurrence's startsAt as the caller saw it, RFC3339 (ReassignSessionSeries; required). AnchorMoved unless it still matches; the shift is startsAt − anchorStartsAt."}},` +
			// Empty because this DDL admits FOUR ops with disjoint required
			// sets, the same union shape every other multi-op DDL in this file
			// declares; each op's own required_string/required_int in the
			// script is what actually rejects a missing field.
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.sessionseries.<NanoID> CreateSessionSeries minted, or the series ExtendSessionSeries advanced the .horizon of. StopSessionSeries returns it too (it writes only the series' .horizon). TombstoneSessionSeries and ReassignSessionSeries return NO primaryKey at all: the reply constraint admits only a key the op actually wrote, and their subject is the occurrences (every mutation roots at an occurrence or a slot hub — except the .horizon the call-off stops and the move shifts on a rolling series, which is a side effect on a key the caller already holds, not the reply's subject). The affected session keys are not returned either — the op envelope's response permits only primaryKey (InvalidReturnShape otherwise); they ride the emitted event, and the occurrences show up on (or drop off) the studio's own wellnessSessions schedule grid."}}}`,
		FieldDescription: map[string]string{
			"seriesKey":          "Full vtx.sessionseries.<NanoID> key of an existing series to call off (TombstoneSessionSeries), move (ReassignSessionSeries), extend by one occurrence (ExtendSessionSeries) or stop rolling (StopSessionSeries). Validated alive + class=sessionseries. The series vertex itself survives every one of them — only its still-upcoming occurrences are cancelled or moved, and only its .horizon advances, shifts or stops.",
			"studio":             "Full vtx.studio.<NanoID> key every occurrence runs at. Validated alive + class=studio; the whole series claims one studioSlotClaim set per occurrence on it. TombstoneSessionSeries, ReassignSessionSeries, ExtendSessionSeries and StopSessionSeries require it too, as the confirmation param: it must be the series' own studio (WrongStudio otherwise), and only occurrences still at it are cancelled or moved.",
			"rolling":            "Optional (CreateSessionSeries). True keeps occurrenceCount classes on the books: the series carries a .horizon and the platform mints the next occurrence each time the window's earliest class starts, until the run is called off.",
			"anchorKey":          "Full vtx.session.<NanoID> key of the occurrence the caller saw as the next still-upcoming class of the series at the confirmed studio (ReassignSessionSeries; required). Declared in ContextHint.Reads with its .schedule. The op refuses AnchorMoved unless the walk's earliest eligible occurrence is exactly this key.",
			"anchorStartsAt":     "That occurrence's start as the caller saw it (RFC3339, canonical UTC; ReassignSessionSeries; required). The shift every moved occurrence takes is startsAt − anchorStartsAt; AnchorMoved unless the anchor's live .schedule.startsAt still equals it.",
			"name":               "The display name every occurrence shares.",
			"startsAt":           "First occurrence's start (RFC3339, canonical UTC; CreateSessionSeries). Must align to the 15-minute grid (SlotGridViolation). On ExtendSessionSeries: the occurrence to mint, which must equal .horizon.nextStartsAt (StaleHorizon otherwise). On ReassignSessionSeries: the NEW start of the earliest still-upcoming occurrence at the confirmed studio, after submittedAt (SessionInPast otherwise); every other still-upcoming occurrence there shifts by the same delta, at most 366 days (InvalidArgument beyond, as is a span identical to the anchor's current one).",
			"endsAt":             "First occurrence's end (RFC3339, canonical UTC; CreateSessionSeries). Must align to the 15-minute grid; span capped at 96 cells / 24h per occurrence (SessionTooLong). On ExtendSessionSeries: must equal .horizon.nextEndsAt (StaleHorizon otherwise). On ReassignSessionSeries: the NEW end of that same earliest occurrence; every moved occurrence takes on this span's length.",
			"capacity":           "Maximum concurrent bookings, an integer 1..200, shared by every occurrence.",
			"priceCents":         "Optional per-occurrence class price in integer cents (>= 0). Omitted or 0 means every occurrence is free.",
			"residentPriceCents": "Optional per-occurrence resident class price in integer cents (>= 0). Charged instead of priceCents to a booking whose .status.rate is resident. Omitted means a resident pays priceCents same as a standard booker.",
			"instructor":         "Optional full vtx.instructor.<NanoID> key leading every occurrence. Validated alive + class=instructor; MUST be listed in ContextHint.Reads when supplied. On ExtendSessionSeries: the instructor the .horizon records (its presence or absence must match — StaleHorizon otherwise).",
			"intervalDays":       "Days between occurrences (1..365), e.g. 7 for weekly, 14 for biweekly.",
			"occurrenceCount":    "How many occurrences to mint including the first (2..52).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateSessionSeries — a weekly class for 8 weeks",
				Payload: map[string]any{
					"studio": "vtx.studio.<NanoID>", "name": "Evening Flow with Sam",
					"startsAt": "2026-08-03T18:00:00Z", "endsAt": "2026-08-03T19:00:00Z",
					"capacity": 20, "intervalDays": 7, "occurrenceCount": 8,
				},
				ExpectedOutcome: "Mints vtx.sessionseries.<NanoID> (root {}) + .definition + its atStudio link, plus " +
					"8 vtx.session.<NanoID> occurrences one week apart, each with its own .schedule/atStudio/" +
					"atLocation/studioSlotClaim mutations (identical to 8 separate CreateSession calls) and a partOf " +
					"link back to the series. Returns primaryKey (the series key only — the 8 session keys are not in " +
					"the response; they appear on the studio's own schedule grid). Rejects StudioConflict — the WHOLE " +
					"series, no partial commit — if any single occurrence's cells collide with an existing booking.",
			},
			{
				Name: "CreateSessionSeries — an instructor-led priced series",
				Payload: map[string]any{
					"studio": "vtx.studio.<NanoID>", "instructor": "vtx.instructor.<NanoID>",
					"name": "Evening Flow with Sam", "startsAt": "2026-08-03T18:00:00Z", "endsAt": "2026-08-03T19:00:00Z",
					"capacity": 20, "priceCents": 1500, "residentPriceCents": 1000, "intervalDays": 7, "occurrenceCount": 8,
				},
				ExpectedOutcome: "As above, plus each occurrence validates the instructor alive + class=instructor, " +
					"writes its own ledBy link, and carries priceCents:1500/residentPriceCents:1000 on its .schedule — " +
					"wellness-ledger's wellnessClassPriceSettlement lens converges a charge per booking on each " +
					"occurrence independently, 1000 for a resident booking and 1500 for a standard one.",
			},
			{
				Name: "CreateSessionSeries — a rolling weekly class",
				Payload: map[string]any{
					"studio": "vtx.studio.<NanoID>", "name": "Evening Flow with Sam",
					"startsAt": "2026-08-03T18:00:00Z", "endsAt": "2026-08-03T19:00:00Z",
					"capacity": 20, "intervalDays": 7, "occurrenceCount": 4, "rolling": true,
				},
				ExpectedOutcome: "As the 8-week example, with 4 occurrences, plus the series' .horizon = " +
					"{nextStartsAt: 2026-08-31T18:00:00Z, nextEndsAt: 2026-08-31T19:00:00Z, extendAt: 2026-08-03T18:00:00Z, " +
					"mintedCount: 4}. When the first class starts (extendAt) the recorded lapse opens the " +
					"wellnessSeriesHorizon gap and Weaver dispatches ExtendSessionSeries for the Aug 31 class; the " +
					"horizon then reads {nextStartsAt: 2026-09-07T18:00:00Z, ..., extendAt: 2026-08-10T18:00:00Z, " +
					"mintedCount: 5}, so four classes are always on the books ahead of the earliest one.",
			},
			{
				Name: "ExtendSessionSeries — Weaver mints the next occurrence of a rolling class",
				Payload: map[string]any{
					"seriesKey": "vtx.sessionseries.<NanoID>", "studio": "vtx.studio.<NanoID>",
					"startsAt": "2026-08-31T18:00:00Z", "endsAt": "2026-08-31T19:00:00Z",
				},
				ExpectedOutcome: "Submitted by Weaver's dispatch actor (AuthDenied for any other) with startsAt/endsAt equal " +
					"to the series' .horizon.nextStartsAt/nextEndsAt and no instructor when the horizon records none " +
					"(StaleHorizon otherwise): mints one vtx.session occurrence with the series' .definition shape " +
					"(schedule, atStudio, atLocation, studioSlotClaim cells, partOf), or skips it — no session — when " +
					"startsAt is already before submittedAt (PastOccurrence) or a studio/instructor cell of its span " +
					"is held (StudioConflict/InstructorConflict); either way advances .horizon by one interval and " +
					"emits wellness.sessionSeriesExtended {seriesKey, studio, startsAt, sessionKey | skipped, " +
					"instructorDropped?}. An instructor the horizon names who has since been retired mints the " +
					"class unled, is dropped from the horizon and named in instructorDropped. " +
					"Returns primaryKey (the series). Rejects WrongStudio for a studio that is not the series' own " +
					"and NotRolling for a series without a .horizon.extendAt (never rolling, or stopped).",
			},
			{
				Name:    "TombstoneSessionSeries — call off the rest of a recurring class",
				Payload: map[string]any{"seriesKey": "vtx.sessionseries.<NanoID>", "studio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Walks the series' partOf-in occurrences and tombstones every one still live, " +
					"still at the named studio, and starting after submittedAt — releasing each one's studioSlotClaim " +
					"cells and its current instructor's instructorSlotClaim cells. Occurrences that have already " +
					"started, that were already cancelled individually, or that were moved to another studio are " +
					"left untouched, as is the series vertex itself. Emits wellness.sessionSeriesCancelled with the " +
					"cancelled session keys and stoppedRolling, and returns no primaryKey. On a rolling series it also " +
					"rewrites .horizon without extendAt (stoppedAt recorded), which is what stops the platform minting " +
					"the next window — so a rolling series with nothing left to cancel still succeeds. Rejects " +
					"WrongStudio for a studio that is not the series' own, and NoUpcomingOccurrences when nothing " +
					"was eligible on a non-rolling series.",
			},
			{
				Name:    "StopSessionSeries — stop a rolling class from minting further occurrences",
				Payload: map[string]any{"seriesKey": "vtx.sessionseries.<NanoID>", "studio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Rewrites the series' .horizon without extendAt (stoppedAt = submittedAt; next*, " +
					"instructor and mintedCount carried), so wellnessSeriesHorizon arms nothing and no further " +
					"occurrence is minted; every class already on the grid is left as it is (cancel or move them " +
					"per class with TombstoneSession / ReassignSession, or with the whole-run verbs while the run's " +
					"history still fits their walk). Walk-free, so it works on a rolling run of any age. Emits " +
					"wellness.sessionSeriesStopped {seriesKey, studio} and returns primaryKey (the series). Rejects " +
					"WrongStudio for a studio that is not the series' own and NotRolling for a series with no " +
					".horizon.extendAt (never rolling, or already stopped).",
			},
			{
				Name: "ReassignSessionSeries — move the rest of a recurring class to a new weekday and time",
				Payload: map[string]any{
					"seriesKey": "vtx.sessionseries.<NanoID>", "studio": "vtx.studio.<NanoID>",
					"anchorKey": "vtx.session.<NanoID>", "anchorStartsAt": "2026-08-03T18:00:00Z",
					"startsAt": "2026-08-05T19:00:00Z", "endsAt": "2026-08-05T20:15:00Z",
				},
				ExpectedOutcome: "With anchorKey still the earliest still-upcoming occurrence at that studio and still standing at 2026-08-03T18:00Z–19:00Z, " +
					"shifts it and every later still-upcoming occurrence there by +2 days 1 hour onto a 75-minute span: each " +
					"one's .schedule is OCC-rewritten (name/capacity/price carried forward, remindAt re-derived), the studio " +
					"and instructor cells only the old spans held are released and only the cells no old span held are " +
					"claimed, in one batch. Bookings ride with their sessions; the series' .definition is untouched, and " +
					"a rolling series' .horizon next*/extendAt shift by the same +2 days 1 hour. Emits " +
					"wellness.sessionSeriesMoved with the moved session keys and shiftSeconds; returns no primaryKey. " +
					"Rejects StudioConflict / InstructorConflict (naming the occurrence that collided, nothing moved) when " +
					"any new cell is held by another class; AnchorMoved when the anchor is no longer the next class or no " +
					"longer starts at anchorStartsAt (reload the roster); SeriesTooLarge past 990 assembled mutations; " +
					"WrongStudio, NoUpcomingOccurrences, SlotGridViolation, SessionTooLong, InvalidArgument for a " +
					"no-change span, and SessionInPast when the new start is not after submittedAt.",
			},
		},
	}
}

func sessionScheduleAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionScheduleAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSession", "ReassignSession", "CreateSessionSeries", "ReassignSessionSeries", "ExtendSessionSeries"},
		Description: "Session schedule aspect (wellness). Stored as vtx.session.<NanoID>.schedule (class " +
			"sessionSchedule) = {name, startsAt, endsAt, capacity, priceCents?, residentPriceCents?, remindAt}. " +
			"Non-sensitive. Written by " +
			"CreateSession (mints, priceCents omitted or 0 for a free class), ReassignSession (OCC-conditioned; " +
			"startsAt/endsAt update on a time move, name/capacity/priceCents/residentPriceCents update when the " +
			"caller supplies them (re-validated with CreateSession's own bounds) else carried forward unchanged, " +
			"remindAt " +
			"re-derived), CreateSessionSeries (mints one per occurrence, same shape as CreateSession's), " +
			"ExtendSessionSeries (mints the one occurrence a rolling series' window moves onto, the same " +
			"per-occurrence shape), and " +
			"ReassignSessionSeries (OCC-conditioned per still-upcoming occurrence; startsAt/endsAt shifted by one " +
			"shared delta, everything else carried forward, remindAt re-derived) — all owned " +
			"by the session vertexType DDL's script; this aspect-type DDL is the step-6 write gate. Declaration-only: " +
			"no op handler. CreateBooking reads capacity on demand (kv.Read) to bound its seat-claim loop; " +
			"wellness-ledger's wellnessClassPriceSettlement lens reads priceCents and residentPriceCents, charging a " +
			"resident booking residentPriceCents when the session declares one, else priceCents same as a standard " +
			"booking; " +
			"wellness-reminders' wellnessBookingReminders lens reads remindAt (mirroring clinic-domain's " +
			"remindAt on the appointment .schedule aspect) to arm the ~24h-ahead class reminder.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string"},"startsAt":{"type":"string"},"endsAt":{"type":"string"},"capacity":{"type":"integer"},"priceCents":{"type":"integer"},"residentPriceCents":{"type":"integer"},"remindAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name":               "The session's display name.",
			"startsAt":           "Session start (RFC3339).",
			"endsAt":             "Session end (RFC3339).",
			"capacity":           "Maximum concurrent bookings (integer 1..200).",
			"priceCents":         "Optional class price in integer cents (>= 0). Omitted or 0 means a free class.",
			"residentPriceCents": "Optional resident class price in integer cents (>= 0), charged instead of priceCents to a booking whose .status.rate is resident. Omitted means a resident pays priceCents same as a standard booker.",
			"remindAt":           "Precomputed reminder deadline (RFC3339, canonical UTC) = startsAt − 24h. Derived by CreateSession/CreateSessionSeries/ExtendSessionSeries/ReassignSession/ReassignSessionSeries, not a caller input; wellness-reminders' convergence lens projects it as freshUntil to arm the @at class-reminder timer.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "session schedule aspect",
				Payload:         map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T10:00:00Z", "capacity": 20},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.schedule; written by CreateSession. priceCents is omitted here (a free class).",
			},
			{
				Name:            "session schedule aspect — priced class",
				Payload:         map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T10:00:00Z", "capacity": 20, "priceCents": 1500},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.schedule; written by CreateSession. wellness-ledger's wellnessClassPriceSettlement lens reads priceCents to converge the class-price charge.",
			},
			{
				Name:            "session schedule aspect — resident-priced class",
				Payload:         map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T10:00:00Z", "capacity": 20, "priceCents": 1500, "residentPriceCents": 1000},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.schedule; written by CreateSession. wellnessClassPriceSettlement charges 1000 for a resident booking, 1500 for a standard one.",
			},
		},
	}
}

// sessionSeriesDefinitionAspectTypeDDL declares the .definition aspect —
// the series' shared template, written once by CreateSessionSeries.
func sessionSeriesDefinitionAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionSeriesDefinitionAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSessionSeries"},
		Description: "Session-series definition aspect (wellness). Stored as vtx.sessionseries.<NanoID>.definition " +
			"(class sessionSeriesDefinition) = {name, capacity, priceCents?, residentPriceCents?, intervalDays, occurrenceCount, " +
			"firstStartsAt, firstEndsAt}. Non-sensitive. Written ONCE by CreateSessionSeries (whose sessionseries " +
			"vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. Declaration-only: no op " +
			"handler. No op ever edits it: the shape a series was authored with is the minted fact, and a " +
			"rolling series' moving window lives on its separate .horizon aspect (sessionSeriesHorizon), which " +
			"ExtendSessionSeries reads this aspect's name/capacity/prices/intervalDays from to mint each next " +
			"occurrence.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string"},"capacity":{"type":"integer"},"priceCents":{"type":"integer"},"residentPriceCents":{"type":"integer"},` +
			`"intervalDays":{"type":"integer"},"occurrenceCount":{"type":"integer"},` +
			`"firstStartsAt":{"type":"string"},"firstEndsAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name":               "The series' class name, shared by every occurrence.",
			"capacity":           "The series' seat capacity, shared by every occurrence.",
			"priceCents":         "The series' per-occurrence price in integer cents, when set.",
			"residentPriceCents": "The series' per-occurrence resident price in integer cents, when set. Charged instead of priceCents to a booking whose .status.rate is resident.",
			"intervalDays":       "Days between occurrences.",
			"occurrenceCount":    "How many session occurrences this series minted.",
			"firstStartsAt":      "RFC3339 start of the first occurrence.",
			"firstEndsAt":        "RFC3339 end of the first occurrence.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "session series definition",
				Payload: map[string]any{
					"name": "Evening Flow with Sam", "capacity": 20, "intervalDays": 7, "occurrenceCount": 8,
					"firstStartsAt": "2026-08-03T18:00:00Z", "firstEndsAt": "2026-08-03T19:00:00Z",
				},
				ExpectedOutcome: "Stored as vtx.sessionseries.<NanoID>.definition; written by CreateSessionSeries.",
			},
		},
	}
}

// sessionSeriesHorizonAspectTypeDDL declares the .horizon aspect — a
// rolling series' moving window: the next occurrence on the cadence and the
// instant the window next moves. Five writers, one per transition:
// CreateSessionSeries mints it (rolling: true), ExtendSessionSeries advances
// it by one interval, ReassignSessionSeries shifts it by the move's delta,
// and TombstoneSessionSeries (when its walk succeeds) or StopSessionSeries
// (walk-free) closes it (extendAt dropped, stoppedAt recorded).
// Every writer reaches it through a hydrated read (declared by the playbook
// or derived by derive_reads) and writes it bare, so the Processor's
// hydrated-revision conditioning is the OCC guard. Never reset; it dies with
// nothing, since the series vertex is never tombstoned.
func sessionSeriesHorizonAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionSeriesHorizonAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSessionSeries", "ExtendSessionSeries", "ReassignSessionSeries", "TombstoneSessionSeries", "StopSessionSeries"},
		Description: "Session-series horizon aspect (wellness). Stored as vtx.sessionseries.<NanoID>.horizon " +
			"(class sessionSeriesHorizon) = {nextStartsAt, nextEndsAt, extendAt?, instructor?, mintedCount, " +
			"stoppedAt?}. Non-sensitive. Present only on a series created rolling. nextStartsAt/nextEndsAt are " +
			"the occurrence the platform mints next, on the cadence after the last one minted; extendAt is the " +
			"start of the window's earliest class — the instant the wellnessSeriesHorizon lens arms as freshUntil " +
			"and whose recorded lapse (.freshnessExpiry.byTarget.wellnessSeriesHorizon) opens the gap that " +
			"dispatches ExtendSessionSeries; instructor is the series' authored instructor key, carried so the " +
			"led gap can pass it back; mintedCount counts the occurrences minted so far (occurrenceCount at " +
			"creation, +1 per minted extension). Written by CreateSessionSeries (mints; extendAt = firstStartsAt), " +
			"ExtendSessionSeries (next* and extendAt += intervalDays days), ReassignSessionSeries (next* and " +
			"extendAt shifted by the move's delta) and TombstoneSessionSeries (extendAt dropped, stoppedAt = " +
			"submittedAt — the roll is stopped and the lens arms nothing further) and StopSessionSeries (the same " +
			"stop, without the call-off's walk) — all owned by the sessionseries vertexType DDL's script; this " +
			"aspect-type DDL is the step-6 write gate. Declaration-only: no op handler. The window counts CADENCE " +
			"SLOTS, not live classes: a skipped extension still moves it, and a move that carries the run " +
			"backward can land extendAt at or before the lapse already recorded on the series, in which case the " +
			"gap opens at once with no timer and the platform mints one slot per dispatch until the horizon " +
			"stands occurrenceCount slots ahead of the moved cadence — accepted, not clamped: the desk asked for " +
			"the run to start earlier, and that is what an earlier run has on its books.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"nextStartsAt":{"type":"string"},"nextEndsAt":{"type":"string"},"extendAt":{"type":"string"},` +
			`"instructor":{"type":"string"},"mintedCount":{"type":"integer"},"stoppedAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"nextStartsAt": "RFC3339 start of the occurrence the platform mints next (the one after the last minted, on the cadence).",
			"nextEndsAt":   "RFC3339 end of that occurrence.",
			"extendAt":     "RFC3339 start of the window's earliest class — the instant the next occurrence is minted at. Absent once StopSessionSeries or TombstoneSessionSeries has stopped the roll.",
			"instructor":   "The series' authored instructor key (vtx.instructor.<NanoID>), when one was named; every minted occurrence is led by it. Dropped by the extension that finds them retired, which mints that class unled.",
			"mintedCount":  "How many occurrences the series has minted in all: occurrenceCount at creation, plus one per extension that minted (a skipped extension does not count).",
			"stoppedAt":    "RFC3339 submittedAt of the StopSessionSeries or TombstoneSessionSeries that stopped the roll; absent while the series is rolling.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "session series horizon — rolling",
				Payload: map[string]any{
					"nextStartsAt": "2026-08-31T18:00:00Z", "nextEndsAt": "2026-08-31T19:00:00Z",
					"extendAt": "2026-08-03T18:00:00Z", "mintedCount": 4,
				},
				ExpectedOutcome: "Stored as vtx.sessionseries.<NanoID>.horizon by CreateSessionSeries (rolling: true, 4 weekly occurrences from Aug 3). The Aug 31 class is minted when the Aug 3 class starts.",
			},
			{
				Name: "session series horizon — stopped",
				Payload: map[string]any{
					"nextStartsAt": "2026-09-07T18:00:00Z", "nextEndsAt": "2026-09-07T19:00:00Z",
					"mintedCount": 5, "stoppedAt": "2026-08-12T10:00:00Z",
				},
				ExpectedOutcome: "Rewritten by StopSessionSeries or TombstoneSessionSeries: extendAt is gone, so wellnessSeriesHorizon arms nothing and no further occurrence is minted.",
			},
		},
	}
}

// studioSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect (class
// studioSlotClaim) — a deterministic per-15-minute-cell existence marker on
// the studio hub. The step-6 write gate for CreateSession / TombstoneSession
// (create / release). Declaration-only; NON-sensitive. Mirrors clinic-domain's
// providerSlotClaimAspectTypeDDL exactly, renamed hub (studio, not provider) —
// see wellness-vertical-design.md §1. One aspect per occupied grid cell,
// created ON DEMAND — never pre-seeded by CreateStudio.
func studioSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     studioSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries", "TombstoneSessionSeries", "ReassignSessionSeries", "ExtendSessionSeries"},
		Description: "Studio 15-minute slot-claim aspect (wellness). Stored as vtx.studio.<NanoID>.slot<cellcode> " +
			"(class studioSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the " +
			"cell's canonical whole-second UTC start with '-'/':' stripped and lowercased. CreateSession claims " +
			"one per covered cell (CreateOnly — the key collision across two concurrent sessions for the same cell " +
			"IS the double-book lock: StudioConflict on commit-time rejection); TombstoneSession tombstones all " +
			"held cells on cancellation, freeing them. ReassignSession's time-move path claims/releases only the " +
			"symmetric difference between the old and new covered cells (a cell both spans cover is left alone). " +
			"CreateSessionSeries claims one set per occurrence (the whole batch rejects StudioConflict together if " +
			"any occurrence collides — no partial series), and TombstoneSessionSeries releases the cells of every " +
			"occurrence it cancels, exactly as TombstoneSession does per class. ReassignSessionSeries applies " +
			"ReassignSession's symmetric-difference delta to the UNION of every moved occurrence's cells in one " +
			"batch (a cell one occurrence vacates and its sibling takes in the same op is left alone, which is what " +
			"lets a run shift by its own interval). Non-sensitive; created on demand, no CreateStudio init " +
			"needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "studio slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.studio.<NanoID>.slot<cellcode>; claimed by CreateSession, released by TombstoneSession.",
			},
		},
	}
}

// instructorSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect (class
// instructorSlotClaim) — a deterministic per-15-minute-cell existence marker
// on the instructor hub, the exact clinic-domain providerSlotClaim mirror the
// backlog names ("One instructor can teach two classes at once" —
// verticals.md): studioSlotClaim alone only locks the ROOM, so a live probe
// scheduled one instructor into two overlapping classes at two different
// studios. The step-6 write gate for CreateSession / CreateSessionSeries /
// TombstoneSession / ReassignSession (create / release). Declaration-only;
// NON-sensitive. One aspect per occupied grid cell, created ON DEMAND, and
// only when a session actually names an instructor — a session with no
// instructor claims none.
func instructorSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSession", "TombstoneSession", "ReassignSession", "CreateSessionSeries", "TombstoneSessionSeries", "ReassignSessionSeries", "ExtendSessionSeries"},
		Description: "Instructor 15-minute slot-claim aspect (wellness). Stored as vtx.instructor.<NanoID>.slot<cellcode> " +
			"(class instructorSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the " +
			"cell's canonical whole-second UTC start with '-'/':' stripped and lowercased. CreateSession claims " +
			"one per covered cell for the session's optional instructor (CreateOnly — the key collision across two " +
			"concurrent sessions naming the same instructor for an overlapping span IS the double-book lock: " +
			"InstructorConflict on commit-time rejection); TombstoneSession tombstones all held cells on " +
			"cancellation, freeing them. ReassignSession migrates the claim on an instructor swap/clear (release " +
			"the old instructor's cells for whatever span it held, claim the new instructor's for whatever span " +
			"applies now) and, when the instructor is unchanged, claims/releases only the symmetric difference " +
			"between the old and new covered cells on a time move — the same two mechanisms studioSlotClaim uses, " +
			"applied to a hub that can itself change. CreateSessionSeries claims one set per occurrence sharing " +
			"the series' single instructor (the whole batch rejects InstructorConflict together if any occurrence " +
			"collides — no partial series); TombstoneSessionSeries releases each cancelled occurrence's cells from " +
			"whichever instructor leads THAT occurrence at cancellation time, which a mid-run ReassignSession sub " +
			"makes different from the series' original; ReassignSessionSeries migrates them the same per-occurrence " +
			"way, as one symmetric-difference batch per instructor hub over every occurrence that instructor leads. " +
			"Non-sensitive; created on demand only when a session names an " +
			"instructor. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "instructor slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.instructor.<NanoID>.slot<cellcode>; claimed by CreateSession/CreateSessionSeries, released by TombstoneSession or migrated by ReassignSession.",
			},
		},
	}
}

// bookerSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect (class
// bookerSlotClaim) — a deterministic per-15-minute-cell existence marker on
// the booker's OWN identity hub, the clinic-domain patientSlotClaim mirror
// (verticals.md): the existing sessionBookerClaim only catches a booker
// double-booking the SAME session twice, never a booker booked into two
// DIFFERENT overlapping sessions — which the same live probe found ("booked
// the same member into both"). The step-6 write gate for CreateBooking /
// JoinWaitlist / CancelBooking / ReleaseOrphanedBooking (create / release).
// Declaration-only; NON-sensitive. One aspect per occupied grid cell, created
// ON DEMAND.
func bookerSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     bookerSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking"},
		Description: "Booker 15-minute slot-claim aspect (wellness). Stored as vtx.identity.<NanoID>.slot<cellcode> " +
			"(class bookerSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the " +
			"cell's canonical whole-second UTC start with '-'/':' stripped and lowercased, computed from the " +
			"BOOKED SESSION's [startsAt, endsAt) span. CreateBooking and JoinWaitlist each claim one per covered " +
			"cell on the booker's own identity hub (CreateOnly — the key collision across two overlapping " +
			"sessions IS the double-book lock: BookerConflict on commit-time rejection; a data.protected booker is " +
			"refused before any cell is claimed — ProtectedBooker — since a cell under a protected root could never " +
			"be tombstoned); CancelBooking tombstones " +
			"all held cells for the cancelled booking's session on release; ReleaseOrphanedBooking does the same " +
			"for a booking whose session TombstoneSession already killed (a called-off class does not cascade). " +
			"Non-sensitive; created on demand, no CreateBooking init needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booker slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.slot<cellcode>; claimed by CreateBooking/JoinWaitlist, released by CancelBooking or ReleaseOrphanedBooking.",
			},
		},
	}
}

func bookingVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     bookingVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateBooking", "CancelBooking", "JoinWaitlist", "SetBookingAttendance", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"},
		Description: "Wellness booking DDL. Vertex shape: vtx.booking.<NanoID>, class=booking, root data = {} " +
			"(minimal, D5). CreateBooking validates the session is alive + class=session and the booker is alive " +
			"+ class=identity and NOT a data.protected kernel root (ProtectedBooker — the Processor lets a create " +
			"under a protected root through but refuses every later tombstone, so the booker's slot cells could " +
			"never be released), reads the session's .schedule.capacity, and claims the first free " +
			"vtx.session.<s>.seat<n> for n in 1..capacity (SessionFull once every seat is claimed) — the SAME " +
			"CreateOnly/expectedRevision write-path idiom studioSlotClaim uses, applied over an enumerated seat-" +
			"index dimension instead of a time-cell dimension (Capability-KV §06). It then atomically mints the " +
			"booking + the .status aspect {value: booked, rate, seat, className, classStartsAt, bookedAt, priceCents} + the forSession " +
			"link (booking→session) + the bookedBy link (booking→identity) — className/classStartsAt are a " +
			"point-in-time snapshot of the session's own .schedule.name/.schedule.startsAt, taken here because the " +
			"session can later be TombstoneSession'd (bookingStatusAspectTypeDDL above); bookedAt is the claim's own " +
			"submittedAt; priceCents is the price this seat pays, snapshotted at the claim (residentPriceCents for a " +
			"resident-rate booking on a class that declares one, else priceCents, else 0) so a later re-price never " +
			"relabels what the seat is charged. Past-class guard (SessionInPast): on the self-service leg (the " +
			"consumer scope=self grant, the validated target) the class must not have started — submittedAt before " +
			".schedule.startsAt; on the desk leg (a staff or operator submission) a walk-in may be seated until the " +
			"class ENDS — submittedAt before .schedule.endsAt — and is refused once it has ended, the refusal saying " +
			"the class has ended. JoinWaitlist shares every " +
			"one of those checks (factored into prepare_booking_common, ddls.go) but claims the SAME CreateOnly " +
			"idiom over a SEPARATE vtx.session.<s>.wl<n> dimension instead (WaitlistFull once " +
			"MAX_WAITLIST_SIZE=200 is exhausted) and mints .status {value: waitlisted, rate, waitlistSlot, " +
			"className, classStartsAt, bookedAt} (no priceCents until seated) — its past-class guard is startsAt on " +
			"EVERY leg, since a waitlist slot on a class already under way can never be promoted into (both promotion " +
			"paths refuse SessionInPast past startsAt). A caller may hold at most one LIVE claim per " +
			"session regardless of which state it is in, since both ops claim the identical sessionBookerClaim " +
			"guard (DoubleBooked either way). Both refuse CreditHold when the booker's wellness-ledger account carries " +
			"an arrears episode a reminder has gone out for (.arrears.sentAt present, class wellnessAccountArrears — " +
			"the account resolved from the booker's own heldFor in-link, never the payload; a wrong-class document is " +
			"InvalidState): a reminded debtor claims no new seat, booked or waitlisted, on any leg, until the balance " +
			"is paid or written off. dueAt alone (overdue, not yet reminded) is not a hold, and neither is a member with " +
			"no account. The hold answers after the workplace confinement and before the schedule read, so a staffer " +
			"elsewhere learns nothing and a held member gets no capacity oracle. CancelBooking, beyond releasing the cancelling booking's own seat, " +
			"now ALSO runs find_promotion_candidate — a bounded kv.Links walk over the session's inbound " +
			"forSession links picking the live waitlisted booking with the LOWEST waitlistSlot — and if one is " +
			"found, hands it the just-freed seat directly (an OCC upsert of ITS OWN .status to " +
			"value=booked/seat=<the freed index>, plus tombstoning its .wl<n> slot) INSTEAD of tombstoning the " +
			"seat cell back open: the seat-claim aspect is a pure existence marker with no owner field, so " +
			"reassigning ownership is just flipping the winning booking's own .status, and doing it in the SAME " +
			"mutation batch as the cancellation closes the race a two-step release-then-reclaim would leave open " +
			"for an unrelated new CreateBooking caller to win the seat instead. Exhausting the bounded walk " +
			"degrades softly (no promotion this round, same as before this feature existed) rather than failing " +
			"the cancellation — a member must always be able to cancel their own seat regardless of how much " +
			"waitlist history a popular session has accumulated. Resident-rate: an optional leaseAppKey, when supplied, " +
			"qualifies for rate=resident only when ALL THREE hold: the leaseapp is alive, its .tenancy aspect " +
			"is present and not ended (CreateOnly-stamped on the leaseapp's FIRST DecideLeaseApplication approve — " +
			"the only signal an application actually became an active tenancy, not merely pending or declined; " +
			"an endedAt recorded by EndTenancy means the term has run out), and " +
			"lnk.leaseapp.<id>.applicationFor.identity.<bookerId> is live (known-key kv.Read, the lease-signing " +
			"renewal-verification idiom) — a match writes the residentRate link (booking→leaseapp, the " +
			"ratifying audit link a future billing composition lens can walk); failing any one check is NOT a " +
			"hard failure, it falls through to rate=standard (a booker " +
			"naming a lease they don't hold never over-grants the discount, but is still allowed to book). " +
			"CancelBooking validates the booking is alive + class=booking and the supplied session is its actual " +
			"session (via the forSession link), rejects once .status.value is neither booked nor waitlisted (AttendanceRecorded " +
			"— a marked or forfeited booking is no longer cancellable, so a no-show or a forfeit cannot be self-erased) and once the " +
			"session's own .schedule.startsAt has passed (SessionStarted, the mirror of SetBookingAttendance's " +
			"SessionNotStarted; checked second because a marked booking's class has necessarily already begun, so " +
			"AttendanceRecorded is the more specific rejection whenever both would apply), then reads the " +
			"booking's own .status.seat (stored at create time — no stored " +
			"back-reference needed to recompute it) and releases that seat cell; the booking itself is soft-deleted " +
			"unless it still owes (below). " +
			"A cancellation inside the two hours before startsAt is still accepted but forfeits the class price: " +
			"no wellnessrefund marker is minted for it, so a settlesClassPrice charge that already posted stands " +
			"unreversed — the same standing charge a no-show leaves — and a wellness.lateCancelForfeited event is " +
			"emitted instead of wellness.classPriceRefundQueued. A booking that still owes stays: when the late " +
			"window meets a positive effective class price for this booking (the .status.priceCents snapshot taken " +
			"at seating when present, else — a seat claimed before the snapshot existed — residentPriceCents for a " +
			"resident-rate booking on a class that declares one, else priceCents — wellness-ledger's own settlement " +
			"rule), the " +
			"booking is NOT soft-deleted but kept live under the terminal status forfeited, carrying rate / booker / " +
			"session / className / classStartsAt / promotedAt / bookedAt / priceCents and no seat, so wellnessBookers keeps the guest reachable for the " +
			"desk that collects the standing charge; a free class (price absent or 0) has nothing to forfeit and " +
			"is soft-deleted like an early cancel. Exactly on the two-hour mark forfeits (the same " +
			"at-the-boundary-the-stricter-rule-wins inequality as SessionStarted). It forfeits the class price " +
			"only and posts no separate no-show fee, and it applies to any caller — the window is about timing, " +
			"not who submits. A seat handed from the waitlist inside the window is exempt: the window is a rule " +
			"about notice, and a booking whose .status.promotedAt is at or after the cutoff never had the two hours " +
			"to give, so its cancellation refunds (marker minted, booking soft-deleted) right up to SessionStarted; " +
			"a promotion that landed before the cutoff is an ordinary seat and forfeits as any other, and a direct " +
			"CreateBooking inside the window is unchanged. A waitlisted booking is unaffected: it never carried a " +
			"class-price charge to forfeit. " +
			"SetBookingAttendance records who actually showed: it moves .status.value from booked to " +
			"attended or noShow, carrying rate / seat / booker / session / className / classStartsAt / promotedAt / bookedAt / priceCents forward " +
			"unchanged (an OCC upsert on the aspect's own revision) — those fields now only matter for " +
			"ReleaseOrphanedBooking (and, for className/classStartsAt, wellness-ledger's wellnessLedgerHistory " +
			"lens), since a marked booking is " +
			"no longer CancelBooking-eligible. Transitioning to noShow also stores a noShowFeeCents amount on " +
			".status — caller-supplied, or when omitted resolved from the STUDIO the session is at now: its " +
			".profile.noShowFeeCents policy when recorded (CreateStudio / SetStudioProfile), else the documented " +
			"2500 no-policy default (the same idiom clinic-domain's SetAppointmentStatus uses, with the amount " +
			"the studio's rather than the script's). A fee of 0 — caller-supplied or the studio's policy — means " +
			"no fee at all, and the field is left off .status entirely rather than written as 0 — that's how the " +
			"automated pastDueBookingsTarget sweep marks a documentation lapse (nobody at the desk checked the " +
			"member in) and how the desk waives a fee, as distinct from a billable no-show; a negative value is " +
			"rejected, and a recorded studio policy that is not a non-negative whole number is refused " +
			"(InvalidState) rather than billed. When present and " +
			"positive, wellness-ledger's wellnessNoShowSettlement lens reads it to post a DebitAccount charge " +
			"against the booker's ledger account. It is re-markable — " +
			"attended and noShow correct each other. noShowFeeCents itself is NOT in the carry-forward field set " +
			"(only rate/seat/booker/session/className/classStartsAt/promotedAt/bookedAt/priceCents are), so a re-mark to attended drops it — and, " +
			"if a no-show-fee charge already posted, reverses it too: the same settles-relation lookup " +
			"ReleaseOrphanedBooking uses below mints a fresh wellnessrefund marker (memo \"No-show fee refund\"), " +
			"guarded by an existing-reverses-link check so a later noShow<->attended cycle never double-credits. It also " +
			"rejects a session that has not begun (SessionNotStarted, the mirror of CreateBooking's " +
			"SessionInPast), and a booking that is still `waitlisted` or already `forfeited` (InvalidState — a waitlisted booker " +
			"never held a confirmed seat and a forfeited one released its seat at cancellation, so there is no attendance to record; only a `booked` start, or a " +
			"re-mark of an already-attended/noShow booking, is valid). Its standing " +
			"guard mirrors TombstoneSession's: the operator passes unconditionally; a bound instructor may mark " +
			"only a booking on a class THEY lead, the caller supplying the instructor param and the script " +
			"requiring BOTH lnk.instructor.<iid>.identifiedBy.identity.<actor> AND " +
			"lnk.session.<sid>.ledBy.instructor.<iid> to be alive (known keys), rejecting AuthDenied otherwise; " +
			"front-of-house staff hold no SetBookingAttendance grant. ReleaseOrphanedBooking is the Weaver-only " +
			"counterpart to TombstoneSession's deliberate no-cascade (package.go): TombstoneSession soft-deletes " +
			"only the session root, so a called-off class otherwise leaves its live bookings, claimed seat/waitlist " +
			"cells and double-book guards stranded forever. ReleaseOrphanedBooking{bookingKey} — restricted to " +
			"Weaver's own dispatch actor (an in-script primordial-actor guard, on top of the standing operator " +
			"grant), no session param — reads the booking's own .status.session anchor (stored by CreateBooking/JoinWaitlist, " +
			"carried forward by SetBookingAttendance), confirms that session is genuinely dead (SessionStillLive " +
			"otherwise — a live session must go through CancelBooking), then releases whichever cell its OWN " +
			"status.value names — .status.seat's cell when still booked OR already noShow (noShow only ever mints " +
			"from booked), .status.waitlistSlot's cell when still waitlisted, never more than one — and the " +
			"double-book guard, soft-deleting the booking exactly as CancelBooking does (minus any promotion " +
			"attempt: the session is dead, so there is no seat left to hand anyone). Also reverses whichever " +
			"charges the booking already carries at release time: a posted settlesClassPrice charge (any status — " +
			"classPriceSettlementSpec bills unconditional on attendance) and, for an already-noShow booking, a " +
			"posted no-show-fee charge (relation settles) too — the studio called off the class either way, so " +
			"BOTH reverse unconditionally, the same posted-charge-always-reverses policy CancelBooking's own " +
			"late-cancellation carve-out never applies here. Dispatched by the wellnessOrphanedBookingSettlement " +
			"Weaver target (targets.go) against the missing_release gap its convergence lens computes (now " +
			"matching status=booked OR status=waitlisted OR status=noShow, lenses.go); never client-invoked. In the " +
			"same batch it also emits an external.notification event (replyOp RecordBookingChangeNotification, " +
			"instanceKey/idempotencyKey <bookingKey>:calledOff:<sessionKey>) so the drained member is told the class " +
			"was called off. " +
			"PromoteWaitlistedBookings is the other Weaver-only op, and the other half of the promotion story: " +
			"CancelBooking hands a seat it FREES to the earliest waitlisted booking in its own batch, but a seat " +
			"that goes free without a cancellation — ReassignSession raising a full class's capacity — has no such " +
			"carrier, so a waitlisted member stays stranded on a class with room. Given only a session, this op " +
			"walks that session's waitlist (collect_waitlist_candidates, the same bounded paginated forSession-in " +
			"walk CancelBooking's promotion uses) and seats EVERY candidate the class has room for in ONE batch, " +
			"lowest waitlistSlot first, into the free seat cells claim_free_seats reads — each promotion the exact " +
			"CancelBooking footprint (seat cell claimed, .wl<n> released, .status OCC-upserted to booked with seat " +
			"set, waitlistSlot dropped, promotedAt stamped and priceCents snapshotted at this seating, " +
			"wellness.waitlistPromoted emitted). All-in-one because Weaver holds a " +
			"dispatched gap for the lease: one seat per dispatch would seat one member per lease window on a class " +
			"that just gained four. Rejects a class that has already begun (SessionInPast, the same startsAt inequality " +
			"CreateBooking's self-service leg uses), declines NothingToPromote when a walk that reached the end of the session's " +
			"booking links finds no live waitlisted booking, or when no seat is free, and declines " +
			"WaitlistWalkBound when the walk ran out of pages first, since an unfinished walk cannot say the " +
			"waitlist is empty — a recorded decline in every case, never a silent empty commit. Dispatched by the " +
			"wellnessWaitlistPromotion Weaver target (targets.go) against the missing_promotion gap its " +
			"convergence lens computes; never client-invoked.",
		Script: bookingDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"session":{"type":"string","description":"vtx.session.<NanoID> being booked or waitlisted (CreateBooking / JoinWaitlist; required, validated alive + class=session; on CancelBooking and SetBookingAttendance it must be the booking's actual session, validated via the forSession link; on PromoteWaitlistedBookings it is the ONLY param — the class whose waitlist is seated, validated alive + class=session)."},` +
			`"booker":{"type":"string","description":"vtx.identity.<NanoID> making the booking or joining the waitlist (CreateBooking / JoinWaitlist; required, validated alive + class=identity)."},` +
			`"leaseAppKey":{"type":"string","description":"Optional vtx.leaseapp.<NanoID> the booker claims residency under (CreateBooking / JoinWaitlist; optional). Checked against the lease's applicationFor link — a mismatch falls through to the standard rate, never a hard failure."},` +
			`"bookingId":{"type":"string","description":"Optional bare NanoID for the new booking vertex (CreateBooking / JoinWaitlist); absent → minted."},` +
			`"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> of an existing booking (CancelBooking / SetBookingAttendance / ReleaseOrphanedBooking; required, validated alive)."},` +
			`"status":{"type":"string","description":"attended | noShow (SetBookingAttendance; required). Re-markable — either value corrects the other."},` +
			`"instructor":{"type":"string","description":"vtx.instructor.<NanoID> the caller is bound to (SetBookingAttendance; required for an instructor (non-operator) caller marking a booking on their OWN class — validated via identifiedBy + ledBy)."},` +
			`"noShowFeeCents":{"type":"number","description":"Optional no-show fee in integer cents, only meaningful when status is noShow (SetBookingAttendance; optional). 0 means no fee is charged (the automated pastDueBookings sweep sends it to mark a documentation lapse rather than a billable no-show; the desk sends it to waive the fee); any other supplied value must be positive. When omitted the fee is the studio's recorded noShowFeeCents policy (the studio the session is at now; 0 bills nothing), or 2500 for a studio with no policy recorded. Stored on .status; wellness-ledger's wellnessNoShowSettlement lens reads it to post a DebitAccount charge against the booker's ledger account."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the operation wrote; vtx.session.<NanoID> for PromoteWaitlistedBookings, which writes across a whole class's waitlist rather than onto one booking."}}}`,
		FieldDescription: map[string]string{
			"session":        "Full vtx.session.<NanoID> key being booked or waitlisted. CreateBooking validates it is alive + class=session, reads its capacity, claims a free seat, and writes the forSession link — refused SessionInPast once its .schedule.startsAt has passed on the self-service leg, and once its .schedule.endsAt has passed on the desk (staff/operator) leg, which may seat a walk-in while the class is under way; JoinWaitlist runs the identical validation but claims a waitlist slot instead and is refused SessionInPast past startsAt on every leg — both also store this key on the booking's own .status aspect (single anchor, not a relationship — the forSession link carries the relationship), the same idiom .status.booker already uses, so a later cleanup can find the session even after it is gone. CancelBooking also requires it (the booking's actual session, validated via the forSession link) to release the held seat. SetBookingAttendance requires it to resolve the class's start time and, for an instructor caller, the ledBy binding. PromoteWaitlistedBookings takes it ALONE: the class is the whole subject, since the condition it converges — free seats plus a live waitlist — is a fact about the class, not about any one booking, and naming a booking would be the queue-jump the op exists to prevent.",
			"booker":         "Full vtx.identity.<NanoID> key of the person booking or waitlisting. CreateBooking / JoinWaitlist validate it is alive + class=identity and write the bookedBy link.",
			"leaseAppKey":    "Optional full vtx.leaseapp.<NanoID> key the booker claims residency under (CreateBooking / JoinWaitlist). Verified via the lease's applicationFor link before granting rate=resident; a mismatch or absent lease silently falls back to rate=standard.",
			"bookingId":      "Optional bare NanoID (no dots / key segments) for the new booking vertex. Absent → minted with nanoid.new().",
			"bookingKey":     "Full vtx.booking.<NanoID> key of an existing booking to cancel (CancelBooking), record attendance on (SetBookingAttendance), or release (ReleaseOrphanedBooking, Weaver-dispatched only).",
			"status":         "attended | noShow (SetBookingAttendance). Replaces .status.value; rate, seat and booker are carried forward unchanged.",
			"instructor":     "Full vtx.instructor.<NanoID> key the caller claims to be. Required for a non-operator SetBookingAttendance caller: the script requires the caller's own identifiedBy binding to it AND the session's ledBy link to it, so a forged value only fails closed.",
			"noShowFeeCents": "Optional no-show fee in integer cents (SetBookingAttendance, only meaningful when status is noShow). 0 is allowed and means no fee is charged — the automated pastDueBookings sweep sends this to signal a documentation lapse (nobody checked the member in) rather than a billable no-show, and the desk sends it to waive the fee; any other supplied value must be positive. When omitted the fee is resolved from the studio the session is at now: its .profile.noShowFeeCents policy when recorded (a policy of 0 bills nothing), else 2500 for a studio with no policy recorded. Stored on the .status aspect; read by wellness-ledger's wellnessNoShowSettlement lens to post a DebitAccount charge. NOT carried forward on a later re-mark to attended (only rate/seat/booker/session/className/classStartsAt/promotedAt/bookedAt/priceCents are).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateBooking — standard rate",
				Payload: map[string]any{"session": "vtx.session.<NanoID>", "booker": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates the session + booker are alive/typed, refuses CreditHold if the booker's wellness " +
					"account carries a reminded arrears episode (.arrears.sentAt), refuses SessionInPast once the class has " +
					"started (self-service leg) or ended (desk leg), claims the first free seat " +
					"(SessionFull if none), and commits vtx.booking.<NanoID> (root {}) + .status {value: booked, " +
					"rate: standard, seat, className, classStartsAt, bookedAt: the claim's submittedAt, priceCents: the " +
					"class's price at this instant} + forSession + bookedBy links. Returns primaryKey.",
			},
			{
				Name:    "CreateBooking — the desk seats a walk-in after the class has started",
				Payload: map[string]any{"session": "vtx.session.<NanoID>", "booker": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Submitted by front-of-house staff or an operator (never the consumer scope=self target) at " +
					"09:10 on a class running 09:00–09:30: accepted — the desk leg admits until .schedule.endsAt — and " +
					"committed exactly as above, .status.bookedAt = 09:10, so the roster can mark the walk-in attended at " +
					"once and wellness-reminders never opens a reminder for a seat claimed after the class began. The same " +
					"submission at 09:30 or later is refused SessionInPast (\"the class has ended\"); the same submission by " +
					"the member themselves at 09:10 is refused SessionInPast (\"not in the future\").",
			},
			{
				Name: "CreateBooking — resident rate",
				Payload: map[string]any{
					"session":     "vtx.session.<NanoID>",
					"booker":      "vtx.identity.<NanoID>",
					"leaseAppKey": "vtx.leaseapp.<NanoID>",
				},
				ExpectedOutcome: "As above, but when the supplied leaseAppKey's applicationFor link names this " +
					"booker, .status.rate = resident and a residentRate link (booking→leaseapp) is written. A " +
					"leaseAppKey belonging to a DIFFERENT identity falls through to rate=standard, never rejected.",
			},
			{
				Name:    "JoinWaitlist — a full class",
				Payload: map[string]any{"session": "vtx.session.<NanoID>", "booker": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Same session/booker/rate validation as CreateBooking (the CreditHold refusal included — " +
					"a reminded debtor may not queue for a seat either), but claims the first free " +
					"vtx.session.<s>.wl<n> slot instead of a seat (WaitlistFull once MAX_WAITLIST_SIZE=200 is " +
					"exhausted) and commits vtx.booking.<NanoID> (root {}) + .status {value: waitlisted, " +
					"rate: standard, waitlistSlot, className, classStartsAt, bookedAt} + forSession + bookedBy links (no " +
					"priceCents until seated). Rejected DoubleBooked if the " +
					"booker already holds a live booking OR waitlist slot on this session, and SessionInPast once the class " +
					"has started, on every leg — the desk's until-endsAt admission is CreateBooking's alone. Returns primaryKey.",
			},
			{
				Name:            "CancelBooking — release a seat",
				Payload:         map[string]any{"bookingKey": "vtx.booking.<NanoID>", "session": "vtx.session.<NanoID>"},
				ExpectedOutcome: "Validates the booking is alive + class=booking and the supplied session is its actual session, releases the held seat, and soft-deletes the booking — unless it still owes: submitted inside the two hours before startsAt on a class whose effective price for this booking is positive, the booking is kept live under status forfeited with no seat instead. If the session carries a live waitlisted booking, the LOWEST-waitlistSlot one is handed the freed seat directly (its own .status flips to booked, its .wl<n> slot is released) instead of the seat cell being freed for ordinary first-come booking. Submitted more than two hours before startsAt, an already-posted class-price charge is reversed by a fresh wellnessrefund marker; submitted inside that window it is forfeited (no marker, charge stands, booking stays live as forfeited) — unless the booking's own seat was handed from the waitlist inside that window (.status.promotedAt at or after the cutoff), in which case it refunds like an early cancel. Returns primaryKey.",
			},
			{
				Name: "SetBookingAttendance — an instructor marks their own class",
				Payload: map[string]any{
					"bookingKey": "vtx.booking.<NanoID>",
					"session":    "vtx.session.<NanoID>",
					"status":     "attended",
					"instructor": "vtx.instructor.<NanoID>",
				},
				ExpectedOutcome: "Requires the caller's identifiedBy binding to the named instructor and that instructor's " +
					"ledBy link to the session, then moves .status.value to attended with rate / seat / booker / session / " +
					"className / classStartsAt unchanged. " +
					"SessionNotStarted before the class begins; AuthDenied for any other instructor's class.",
			},
			{
				Name: "SetBookingAttendance — mark a no-show",
				Payload: map[string]any{
					"bookingKey": "vtx.booking.<NanoID>",
					"session":    "vtx.session.<NanoID>",
					"status":     "noShow",
				},
				ExpectedOutcome: "As the attended case, but upserts .status {value: noShow, noShowFeeCents: <fee>, " +
					"...} where <fee> is the studio's recorded noShowFeeCents policy (omitted from .status when that " +
					"policy is 0) or 2500 for a studio with no policy recorded, since noShowFeeCents was omitted from " +
					"the payload — wellness-ledger's wellnessNoShowSettlement lens reads it to post a DebitAccount " +
					"charge against the booker's ledger account once one exists.",
			},
			{
				Name: "SetBookingAttendance — correct a no-show back to attended",
				Payload: map[string]any{
					"bookingKey": "vtx.booking.<NanoID>",
					"session":    "vtx.session.<NanoID>",
					"status":     "attended",
				},
				ExpectedOutcome: "The re-mark itself is the same attended-case upsert (noShowFeeCents drops out of " +
					"the carry-forward set). If a no-show-fee charge already posted for this booking (a live settles " +
					"link), also mints a fresh vtx.wellnessrefund.<NanoID> + .detail (memo \"No-show fee refund\") " +
					"reversing it — a no-op if no such charge exists yet, or if this exact charge was already " +
					"reversed by an earlier correction.",
			},
			{
				Name:    "PromoteWaitlistedBookings — seat a waitlist onto a class that gained room",
				Payload: map[string]any{"session": "vtx.session.<NanoID>"},
				ExpectedOutcome: "Operator (Weaver) only. Walks the session's waitlist and its seat cells and " +
					"promotes every live waitlisted booking the class has a free seat for, lowest waitlistSlot " +
					"first, in ONE batch: per promotion a seat cell claimed, the booking's .wl<n> slot released, " +
					"its .status OCC-upserted to {value: booked, seat, rate/booker/session/className/classStartsAt/bookedAt " +
					"carried forward, waitlistSlot dropped, promotedAt: this dispatch's submittedAt, priceCents: the " +
					"class's price at this seating}, and wellness.waitlistPromoted emitted. SessionInPast " +
					"once the class has begun; NothingToPromote when the waitlist is empty or every seat is " +
					"claimed; WaitlistWalkBound when the session carries more booking links than one walk " +
					"covers. Returns primaryKey (the session).",
			},
			{
				Name:    "ReleaseOrphanedBooking — drain a booking whose class was called off",
				Payload: map[string]any{"bookingKey": "vtx.booking.<NanoID>"},
				ExpectedOutcome: "Operator (Weaver) only. Reads the booking's own .status.session anchor, confirms " +
					"that session is genuinely tombstoned (SessionStillLive otherwise), then releases whichever cell " +
					"its .status.value names (seat if still booked, waitlist slot if still waitlisted) and the " +
					"double-book guard and soft-deletes the booking — the same footprint CancelBooking leaves, " +
					"minus the caller-supplied session cross-check CancelBooking needs and this dispatch doesn't. " +
					"Also emits an external.notification event (replyOp RecordBookingChangeNotification) telling " +
					"the drained member the class was called off.",
			},
		},
	}
}

func instructorVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateInstructor", "TombstoneInstructor", "SetInstructorProfile", "BindInstructorIdentity"},
		Description: "Wellness instructor DDL. Vertex shape: vtx.instructor.<NanoID>, class=instructor, root data = " +
			"{} (minimal, D5 — the data lives in the .profile aspect). CreateInstructor mints the instructor + " +
			"writes the .profile aspect {displayName (required)} atomically, and — when the optional studio param " +
			"is supplied — the instructor teachesAt studio LINK (lnk.instructor.<id>.teachesAt.studio.<sid>, class " +
			"\"teachesAt\"; source = the later-arriving instructor, target = the pre-existing studio, Contract #1 " +
			"§1.1; validated alive + class=studio). SetInstructorProfile edits an existing instructor's .profile — " +
			"it REPLACES the aspect with the supplied displayName (still required, so the instructorName column " +
			"wellnessSessions / wellnessInstructors project can never be nulled). It is the instructor hat's " +
			"record-administering op: a standing binder passes an operator unconditionally and otherwise requires " +
			"the caller be identifiedBy-bound to THIS instructor (mirrors clinic-domain's SetProviderHours). " +
			"TombstoneInstructor soft-deletes one (no cascade onto sessions " +
			"it leads — the projection lenses anchor on the live root, mirroring the studio/session no-cascade " +
			"rule). BindInstructorIdentity binds an existing instructor to a pre-minted vtx.identity (both " +
			"validated alive + typed): it mints lnk.instructor.<id>.identifiedBy.identity.<id> (instructor " +
			"identifiedBy identity, Contract #1 §1.1), claims a CreateOnly guard aspect on EACH side " +
			"(.identityClaim on the instructor, .instructorClaim on the identity — mutually exclusive: one " +
			"identity per instructor, one wellness instructor per identity), and idempotently grants the " +
			"identity-domain `provider` role via holdsRole (mirrors clinic-domain's BindProviderIdentity verbatim " +
			"— persona-worlds-design.md Fire W0; a link already alive is left untouched rather than re-created).",
		Script: instructorDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"displayName":{"type":"string","description":"The instructor's display name (CreateInstructor / SetInstructorProfile; required)."},` +
			`"studio":{"type":"string","description":"Optional vtx.studio.<NanoID> the instructor teaches at (CreateInstructor; validated alive + class=studio; writes the teachesAt link). Listed in ContextHint.Reads when supplied."},` +
			`"instructorId":{"type":"string","description":"Optional bare NanoID for the new instructor vertex (CreateInstructor); absent → minted."},` +
			`"instructorKey":{"type":"string","description":"vtx.instructor.<NanoID> of an existing instructor (TombstoneInstructor / SetInstructorProfile / BindInstructorIdentity; required, validated alive)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity to bind to the instructor (BindInstructorIdentity; required, validated alive + class=identity)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.instructor.<NanoID> the operation wrote (BindInstructorIdentity returns the identifiedBy link key instead)."}}}`,
		FieldDescription: map[string]string{
			"displayName":   "The instructor's display name. Stored on the .profile aspect (CreateInstructor mints it, SetInstructorProfile replaces it; required in both).",
			"studio":        "Optional full vtx.studio.<NanoID> key the instructor teaches at. Validated alive + class=studio; CreateInstructor writes the instructor teachesAt studio link. MUST be listed in ContextHint.Reads when supplied.",
			"instructorId":  "Optional bare NanoID (no dots / key segments) for the new instructor vertex. Absent → minted with nanoid.new().",
			"instructorKey": "Full vtx.instructor.<NanoID> key of an existing instructor vertex (TombstoneInstructor tombstones it; SetInstructorProfile edits its profile; BindInstructorIdentity binds it to a login identity). For SetInstructorProfile it is auto-filled by the client from the instructor being viewed (dispatch.targetField), not user-entered.",
			"identityKey":   "Full vtx.identity.<NanoID> key of a pre-minted identity to bind (BindInstructorIdentity; required). Must be alive + class=identity; wires the identifiedBy link, claims CreateOnly guard aspects on BOTH sides (rejected if either side is already bound), and idempotently grants the identity the provider role.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateInstructor — register an instructor",
				Payload: map[string]any{"displayName": "Kai Nakamura"},
				ExpectedOutcome: "Mints vtx.instructor.<NanoID> (class=instructor, root {}) + the .profile aspect " +
					"{displayName}. Returns primaryKey (the instructor key).",
			},
			{
				Name:    "CreateInstructor — register an instructor who teaches at a studio",
				Payload: map[string]any{"displayName": "Kai Nakamura", "studio": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Mints the instructor + .profile as above, validates the studio is alive + " +
					"class=studio, and writes lnk.instructor.<id>.teachesAt.studio.<NanoID>. Rejects a dead / " +
					"wrong-class studio.",
			},
			{
				Name:            "TombstoneInstructor — remove an instructor",
				Payload:         map[string]any{"instructorKey": "vtx.instructor.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the instructor vertex. Returns primaryKey. Rejects an absent / already-dead instructor.",
			},
			{
				Name:    "SetInstructorProfile — a bound instructor edits their own display name",
				Payload: map[string]any{"instructorKey": "vtx.instructor.<NanoID>", "displayName": "Kai Nakamura, RYT-500"},
				ExpectedOutcome: "Requires the caller hold operator, or be identifiedBy-bound to THIS instructor. " +
					"Replaces the .profile aspect with {displayName}. AuthDenied for any other instructor's record " +
					"(including a bound clinic provider or service provider, who hold the same `provider` role).",
			},
			{
				Name:    "BindInstructorIdentity — bind an instructor to its login identity",
				Payload: map[string]any{"instructorKey": "vtx.instructor.<NanoID>", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates both endpoints alive + typed, mints lnk.instructor.<id>.identifiedBy.identity.<id>, " +
					"claims CreateOnly guard aspects on both sides (rejected if either side is already bound), and " +
					"idempotently grants the identity the provider role via holdsRole. Returns primaryKey (the identifiedBy link key).",
			},
		},
	}
}

func bookingStatusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     bookingStatusAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "SetBookingAttendance", "PromoteWaitlistedBookings"},
		Description: "Booking status aspect (wellness). Stored as vtx.booking.<NanoID>.status (class " +
			"bookingStatus) = {value: booked|waitlisted|attended|noShow|forfeited, rate: standard|resident, seat?, " +
			"waitlistSlot?, booker, session, className?, classStartsAt?, promotedAt?, noShowFeeCents?}. Non-sensitive. Written " +
			"by CreateBooking (value=booked, seat), JoinWaitlist (value=waitlisted, waitlistSlot — the mirror-image " +
			"write, sharing every other field with CreateBooking via prepare_booking_common, ddls.go), CancelBooking " +
			"(two writes: an OCC upsert on a PROMOTED waitlisted booking — value flips waitlisted→booked, waitlistSlot is " +
			"dropped, seat is set to the seat the cancelling booking just vacated, promotedAt is stamped with the " +
			"cancellation's submittedAt — and, when the cancellation lands " +
			"inside the late-cancel window on a seat that was not itself handed from the waitlist inside that window, " +
			"an OCC upsert on ITS OWN booking to value=forfeited carrying " +
			"rate / booker / session / className / classStartsAt / promotedAt forward and dropping seat: the seat is released or " +
			"handed to the promoted waitlister, the class-price charge stands, and the booking stays live so the " +
			"desk can still reach the guest who owes it. A booked cancellation outside the window, a booked cancellation " +
			"inside it on a seat promoted inside it, and any " +
			"waitlisted cancellation, tombstones CancelBooking's own booking outright), PromoteWaitlistedBookings (the SAME promotion upsert, applied to " +
			"every candidate a class with free seats can take rather than to the one CancelBooking just freed a " +
			"seat for, promotedAt stamped with the dispatch's submittedAt), and SetBookingAttendance (value=attended|noShow, an OCC upsert " +
			"carrying rate / seat / booker / session / className / classStartsAt / promotedAt forward untouched, and — only " +
			"when transitioning to noShow — a noShowFeeCents amount: caller-supplied, else the studio's recorded " +
			"policy, else a 2500 default) — the " +
			"booking vertexType DDL owns all four scripts; this aspect-type DDL is the step-6 write gate. seat / " +
			"waitlistSlot / booker / session are internal bookkeeping (the claimed seat or waitlist-slot index, " +
			"the booker's identity key, the session key) CancelBooking (seat, booker, and — for its promotion " +
			"target — waitlistSlot), PromoteWaitlistedBookings (waitlistSlot, rate and booker off every candidate " +
			"it seats) and ReleaseOrphanedBooking (session, booker, and whichever of " +
			"seat/waitlistSlot value names) read to recompute which vtx.session.<s>.seat<n> / vtx.session.<s>.wl<n> " +
			"cell and vtx.session.<s>.bkr<b> double-book guard to release — a single anchor each, NOT a stored " +
			"relationship-as-key-list (the booker relationship stays the bookedBy link, the session relationship " +
			"stays the forSession link, Contract #1). A booking is never both booked and waitlisted at once — seat " +
			"and waitlistSlot are mutually exclusive by construction (CreateBooking writes only the former, " +
			"JoinWaitlist only the latter; a promotion upsert adds seat and drops waitlistSlot in the same write). " +
			"session is what lets ReleaseOrphanedBooking find a booking's session AFTER TombstoneSession has killed " +
			"it — the forSession link survives but the OPTIONAL MATCH lens walk to it would not (a tombstoned " +
			"target simply drops from the join). className/classStartsAt are the same survives-the-tombstone " +
			"snapshot idiom applied to the session's own .schedule.name/.schedule.startsAt, taken once by " +
			"CreateBooking/JoinWaitlist (mirrors clinic-reminders' atSite link precedent, commit 4da005a0) — " +
			"wellness-ledger's wellnessLedgerHistory lens (lenses.go) reads them off THIS aspect (and off the " +
			"wellnessrefund marker's own .detail snapshot for a refund row) instead of walking forSession→session, " +
			"so a member's billing history still names the class after TombstoneSession kills the session vertex " +
			"the charge or refund was for. promotedAt is the instant (RFC3339 UTC, the promoting op's submittedAt) a " +
			"waitlisted booking was handed a seat, present only on a booking that was promoted and carried forward by " +
			"every later writer — CancelBooking's late-cancel rule reads it (a seat promoted at or after the two-hour " +
			"cutoff is exempt from the forfeit), and the wellnessBookings lens projects it so the member's app and " +
			"the desk roster can badge the seating. noShowFeeCents is NOT in the carry-forward field set (only " +
			"rate/seat/waitlistSlot/booker/session/className/classStartsAt/promotedAt are), so re-marking a noShow booking to " +
			"attended drops it — wellness-ledger's wellnessNoShowSettlement lens reads it to post a DebitAccount " +
			"charge against the booker's ledger account. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["booked","waitlisted","attended","noShow","forfeited"]},"rate":{"type":"string","enum":["standard","resident"]},"seat":{"type":"integer"},"waitlistSlot":{"type":"integer"},"booker":{"type":"string"},"session":{"type":"string"},"className":{"type":"string"},"classStartsAt":{"type":"string"},"bookedAt":{"type":"string"},"priceCents":{"type":"number"},"promotedAt":{"type":"string"},"noShowFeeCents":{"type":"number"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value":          "Booking status: booked at CreateBooking time, waitlisted at JoinWaitlist time; attended | noShow once SetBookingAttendance records who showed; forfeited when CancelBooking cancels a booked seat inside the late-cancel window (a seat handed from the waitlist inside that window — promotedAt at or after the cutoff — is exempt and is tombstoned with a refund instead) — the class price is forfeited, the seat is released, and the booking stays live under this terminal value carrying no seat (neither CancelBooking nor SetBookingAttendance accepts it afterwards); a waitlisted booking transitions straight to booked if CancelBooking or PromoteWaitlistedBookings promotes it (never through attended/noShow), promotedAt stamped.",
			"rate":           "standard | resident, derived by CreateBooking / JoinWaitlist from the optional leaseAppKey residency check, and carried forward unchanged on promotion by either promoting path (CancelBooking, PromoteWaitlistedBookings).",
			"seat":           "The claimed seat index on the session (internal bookkeeping; present once value is booked and carried through attended/noShow, absent while waitlisted and absent once forfeited — CancelBooking's late-window upsert drops it as it releases the cell). CancelBooking / ReleaseOrphanedBooking read it to release the correct seat cell; PromoteWaitlistedBookings is what SETS it on a promoted booking, to the free cell it claimed in the same batch.",
			"waitlistSlot":   "The claimed waitlist-slot index on the session (internal bookkeeping; present once value is waitlisted, absent once booked). CancelBooking's promotion walk, PromoteWaitlistedBookings and ReleaseOrphanedBooking read it to release the correct vtx.session.<s>.wl<n> cell.",
			"booker":         "The booker's full vtx.identity.<NanoID> key (internal bookkeeping; CancelBooking / ReleaseOrphanedBooking read it to release the correct per-(session, booker) double-book guard). A single anchor, not a relationship — the bookedBy link carries the relationship.",
			"session":        "The full vtx.session.<NanoID> key this booking is for (internal bookkeeping, the same single-anchor idiom as booker). The FAST PATH ReleaseOrphanedBooking reads to re-confirm the session's liveness after TombstoneSession has killed the vertex; when it is absent that op enumerates the forSession link instead, which is the source of truth — this field is not a relationship.",
			"className":      "The session's .schedule.name at the moment this booking was created or waitlisted (a point-in-time snapshot, not a live relationship). Carried forward unchanged by CancelBooking's promotion upsert and SetBookingAttendance. wellness-ledger's wellnessLedgerHistory lens reads it so a member's billing history still names the class after TombstoneSession kills the session vertex.",
			"classStartsAt":  "The session's .schedule.startsAt at the moment this booking was created or waitlisted, the same snapshot idiom as className, RFC3339 UTC. Carried forward and read by wellnessLedgerHistory alongside className.",
			"bookedAt":       "The instant this booking's claim was made — CreateBooking's or JoinWaitlist's submittedAt, RFC3339 UTC — written at the claim and carried forward unchanged by every later writer (both promotion upserts, CancelBooking's forfeit upsert, SetBookingAttendance); never reset, gone with the booking's tombstone. wellness-reminders' wellnessBookingReminders lens reads it: a seat claimed at or after the class's startsAt (a walk-in the desk seated after the class began) is never reminded. The wellnessBookings lens projects it. Absent on a booking claimed before the stamp existed.",
			"priceCents":     "The price this seat pays, in integer cents, snapshotted at the instant it was seated: by CreateBooking at its claim, by CancelBooking's promotion upsert or PromoteWaitlistedBookings at seating (a waitlisted booking carries none until it holds a seat). Resolved as the session's residentPriceCents when rate is resident and the schedule declares one, else the schedule's priceCents, else 0 — a seat booked free stays free. Carried forward unchanged by the forfeit upsert and the attendance mark. A class re-priced after the claim never changes it: wellness-ledger's wellnessClassPriceSettlement charges this first, CancelBooking's owes test reads it first, and the wellnessBookings lens projects it as the row's priceCents. Absent on a seat claimed before the snapshot existed, which falls back to the schedule's current price at each reader.",
			"promotedAt":     "The instant a waitlisted booking was handed its seat — the promoting op's submittedAt, RFC3339 UTC — present only on a booking that CancelBooking or PromoteWaitlistedBookings promoted (never on a direct CreateBooking seat), carried forward unchanged by SetBookingAttendance and CancelBooking's forfeit upsert, and gone with the booking's tombstone. CancelBooking reads it to exempt a seat promoted at or after the two-hour cutoff from the late-cancel forfeit; the wellnessBookings lens projects it so the seating can be badged.",
			"noShowFeeCents": "Optional no-show fee in integer cents, present only when value is noShow and the fee is positive (caller-supplied, else the studio's recorded .profile.noShowFeeCents policy, else a 2500 default for a studio with no policy). Read by wellness-ledger's wellnessNoShowSettlement lens to post a DebitAccount charge.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking status aspect — booked",
				Payload:         map[string]any{"value": "booked", "rate": "resident", "seat": 3, "booker": "vtx.identity.MQsmTTAgNkngkdEjQz9L", "session": "vtx.session.QsmTTAgNkngkdEjQz9LM", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z", "bookedAt": "2026-07-07T12:00:00Z", "priceCents": 1000},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.status; written by CreateBooking. bookedAt is the claim's submittedAt; priceCents is the resident price the class declared at that instant (1000, not its 1500 standard price) and stays 1000 however the class is re-priced afterwards.",
			},
			{
				Name:            "booking status aspect — booked from the waitlist",
				Payload:         map[string]any{"value": "booked", "rate": "standard", "seat": 1, "booker": "vtx.identity.MQsmTTAgNkngkdEjQz9L", "session": "vtx.session.QsmTTAgNkngkdEjQz9LM", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z", "bookedAt": "2026-07-06T18:30:00Z", "promotedAt": "2026-07-08T07:57:00Z", "priceCents": 1500},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.status; written by CancelBooking's promotion upsert or PromoteWaitlistedBookings on a booking that was waitlisted. bookedAt (the JoinWaitlist claim) is carried; promotedAt is the promoting op's submittedAt and priceCents is the class's price at that seating; here the promotion lands inside the two-hour window before the 09:00 start, so a CancelBooking on this booking before the class begins refunds rather than forfeits.",
			},
			{
				Name:            "booking status aspect — waitlisted",
				Payload:         map[string]any{"value": "waitlisted", "rate": "standard", "waitlistSlot": 2, "booker": "vtx.identity.MQsmTTAgNkngkdEjQz9L", "session": "vtx.session.QsmTTAgNkngkdEjQz9LM", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z", "bookedAt": "2026-07-06T18:30:00Z"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.status; written by JoinWaitlist. No priceCents yet — a waitlisted booking pays nothing until it holds a seat. Flips to booked (seat set, waitlistSlot dropped, promotedAt stamped, priceCents snapshotted) if CancelBooking's promotion walk or PromoteWaitlistedBookings later picks this booking.",
			},
			{
				Name:            "booking status aspect — forfeited",
				Payload:         map[string]any{"value": "forfeited", "rate": "standard", "booker": "vtx.identity.MQsmTTAgNkngkdEjQz9L", "session": "vtx.session.QsmTTAgNkngkdEjQz9LM", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z", "bookedAt": "2026-07-07T12:00:00Z", "priceCents": 1500},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.status; written by CancelBooking on its own booking when the cancellation lands inside the late-cancel window. No seat (the cell is released or handed to the promoted waitlister), the class-price charge stands, and the booking stays live; terminal — CancelBooking and SetBookingAttendance both refuse it.",
			},
		},
	}
}

// sessionSeatClaimAspectTypeDDL declares the .seat<n> aspect (class
// sessionSeatClaim) — a deterministic per-seat-index existence marker on the
// session hub. The capacity-bounded extension of studioSlotClaim's exact
// mechanism (CreateOnly key-collision at commit), applied over an enumerated
// seat-index dimension instead of a time-cell dimension — see
// wellness-vertical-design.md §1(2). Declaration-only; NON-sensitive.
func sessionSeatClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionSeatClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateBooking", "CancelBooking", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"},
		Description: "Session seat-claim aspect (wellness). Stored as vtx.session.<NanoID>.seat<n> (class " +
			"sessionSeatClaim) = {} — a pure existence marker, no relationship field. <n> is a 1-based seat index, " +
			"1..capacity. CreateBooking walks n=1..capacity in a bounded loop and claims the FIRST cell it reads " +
			"absent (CreateOnly — the key collision across two concurrent bookings racing for the same seat IS the " +
			"capacity lock: two callers both reading a seat absent both emit op:create for the identical key, but " +
			"CreateOnly at revision 0 commits exactly once, the loser's batch RevisionConflicts and the Processor " +
			"retries against the now-live seat). CancelBooking tombstones the ONE seat cell recorded on the " +
			"booking's own .status.seat, freeing it for a future claimant. PromoteWaitlistedBookings claims the same " +
			"cells through the same walk, taking as many free ones as the session's waitlist has candidates in a " +
			"single batch — the path that seats a waitlist onto a class whose capacity was raised, where no " +
			"cancellation ever frees a cell. Non-sensitive; created on demand, no " +
			"CreateSession init needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (session hub + seat index), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "session seat-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.seat<n>; claimed by CreateBooking, released by CancelBooking.",
			},
		},
	}
}

// sessionWaitlistClaimAspectTypeDDL declares the .wl<n> aspect (class
// sessionWaitlistClaim) — a deterministic per-waitlist-slot-index existence
// marker on the session hub. The SAME CreateOnly key-collision mechanism
// sessionSeatClaim uses, applied over its own bounded index dimension
// (waitlist position, MAX_WAITLIST_SIZE=200) instead of the seat dimension —
// kept separate from seat so a full waitlist never contends with seat-
// capacity math, and so a promoted booking's slot number (recorded on its
// OWN .status.waitlistSlot) stays a stable handle CancelBooking's promotion
// walk can release even after the booking itself has flipped to booked.
// Declaration-only; NON-sensitive.
func sessionWaitlistClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionWaitlistClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking", "PromoteWaitlistedBookings"},
		Description: "Session waitlist-claim aspect (wellness). Stored as vtx.session.<NanoID>.wl<n> (class " +
			"sessionWaitlistClaim) = {} — a pure existence marker, no relationship field. <n> is a 1-based " +
			"waitlist-slot index, 1..MAX_WAITLIST_SIZE (200) — unbounded by session capacity, since a waitlist has " +
			"no seat-count ceiling of its own. JoinWaitlist walks n=1..200 in a bounded loop and claims the FIRST " +
			"cell it reads absent (CreateOnly — the identical race-safety property claim_first_free_seat relies " +
			"on, ddls.go), storing the claimed n on the new booking's own .status.waitlistSlot. CancelBooking " +
			"tombstones the ONE waitlist cell recorded on whichever booking its promotion walk (ddls.go, " +
			"find_promotion_candidate) picks, freeing that slot the instant the booking it belonged to is handed " +
			"a real seat. PromoteWaitlistedBookings tombstones the same cell for every candidate it seats in its own " +
			"batch. ReleaseOrphanedBooking tombstones it instead of a seat cell when the booking it is " +
			"draining was still waitlisted (never booked) at the moment its session was called off. Non-sensitive; " +
			"created on demand, no CreateSession init needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (session hub + waitlist-slot index), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "session waitlist-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.wl<n>; claimed by JoinWaitlist, released by CancelBooking (on promotion) or ReleaseOrphanedBooking (on a called-off class).",
			},
		},
	}
}

// sessionBookerClaimAspectTypeDDL declares the .bkr<bookerId> aspect (class
// sessionBookerClaim) — a deterministic per-(session, booker) existence marker
// on the session hub that enforces at-most-one LIVE CLAIM (booked OR
// waitlisted) per booker per session. The exact repeatable-session uniqueness
// idiom cafe-domain's cafeOpenTabGuard uses (create-only on a lease's first
// tab, OCC-revived from a prior settled+tombstoned guard) and clinic-domain's
// patientSlotClaim (a per-actor guard aspect with a dimension-encoded
// localName) — here the dimension is the booker id. CreateBooking AND
// JoinWaitlist both claim the identical key (a booker cannot hold a seat AND
// a waitlist slot on the same session, nor two of either — DoubleBooked
// either way); CancelBooking tombstones it, so a later re-book OCC-revives
// it. Declaration-only; NON-sensitive; created on demand, no CreateSession
// init needed.
func sessionBookerClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionBookerClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateBooking", "JoinWaitlist", "CancelBooking", "ReleaseOrphanedBooking"},
		Description: "Session booker-claim guard aspect (wellness). Stored as vtx.session.<NanoID>.bkr<bookerId> " +
			"(class sessionBookerClaim) = {} — a pure existence marker, no relationship field (the booker " +
			"relationship stays the bookedBy link, Contract #1). <bookerId> is the booking booker's bare " +
			"Contract #1 identity id, so the KEY ITSELF is the (session, booker) lock: CreateBooking AND " +
			"JoinWaitlist both claim it CreateOnly, and a SECOND live claim (booked or waitlisted) by the same " +
			"booker on the same session collides at revision 0 and is rejected (DoubleBooked) regardless of which " +
			"op tries it, while a booker acting on a DIFFERENT session claims a different key and is unaffected. " +
			"Repeatable, mirroring cafe-domain's cafeOpenTabGuard: CancelBooking tombstones the guard for the " +
			"booking it cancels (unconditioned — a stale-tombstone race can only free it early, re-earnable on " +
			"the next book) — the guard belonging to a booking CancelBooking's promotion walk PROMOTES stays " +
			"untouched, since that booker's claim is still live, just upgraded from waitlisted to booked. Non-" +
			"sensitive; created on demand, no CreateSession init needed. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The lock's job is done by the KEY (session hub + booker id), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "session booker-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.bkr<bookerId>; claimed by CreateBooking or JoinWaitlist, released by CancelBooking. A second live claim by the same booker on the same session is rejected.",
			},
		},
	}
}

// instructorProfileAspectTypeDDL declares the .profile aspect (class
// instructorProfile) — the step-6 write gate for CreateInstructor +
// SetInstructorProfile. Declaration-only; NON-sensitive.
func instructorProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorProfileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateInstructor", "SetInstructorProfile"},
		Description: "Instructor profile aspect (wellness). Stored as vtx.instructor.<NanoID>.profile (class " +
			"instructorProfile) = {displayName}. Non-sensitive. Written by CreateInstructor (mints it) and " +
			"SetInstructorProfile (replaces it) — both owned by the instructor vertexType DDL's script; this " +
			"aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"displayName":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"displayName": "The instructor's display name.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "instructor profile aspect",
				Payload:         map[string]any{"displayName": "Kai Nakamura"},
				ExpectedOutcome: "Stored as vtx.instructor.<NanoID>.profile; written by CreateInstructor.",
			},
		},
	}
}

// instructorIdentityClaimAspectTypeDDL declares the .identityClaim guard
// aspect on the INSTRUCTOR side of a BindInstructorIdentity pair — the
// entity-keyed half of the bind's mutual-exclusivity guard
// (identityInstructorClaimAspectTypeDDL below is the identity-keyed half).
// Mirrors clinic-domain's providerIdentityClaimAspectTypeDDL exactly.
// Declaration-only; NON-sensitive.
func instructorIdentityClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorIdentityClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindInstructorIdentity"},
		Description: "Instructor identity-claim guard aspect. Stored as vtx.instructor.<NanoID>.identityClaim " +
			"(class instructorIdentityClaim) = {} — a pure existence marker, no relationship field. " +
			"BindInstructorIdentity writes ONE per claimed instructorKey, CreateOnly: the key ITSELF is the lock " +
			"— a second, different identity binding the SAME instructor collides at commit (RevisionConflict), " +
			"never a silent double-bind. Declaration-only: no op handler (BindInstructorIdentity's script, owned " +
			"by the instructor vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the instructor), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "instructor identity-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.instructor.<NanoID>.identityClaim; claimed once by BindInstructorIdentity. A second, different identity binding the same instructor is rejected.",
			},
		},
	}
}

// identityInstructorClaimAspectTypeDDL declares the .instructorClaim aspect
// ATTACHED onto an identity-domain vtx.identity — the identity-keyed half of
// BindInstructorIdentity's mutual-exclusivity guard, mirroring
// clinic-domain's identityProviderClaimAspectTypeDDL exactly, just keyed
// "instructorClaim" instead of "providerClaim". Declaration-only;
// NON-sensitive.
func identityInstructorClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     identityInstructorClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindInstructorIdentity"},
		Description: "Identity instructor-claim guard aspect (wellness, attached onto an identity-domain vertex). " +
			"Stored as vtx.identity.<NanoID>.instructorClaim (class identityInstructorClaim) = {} — a pure " +
			"existence marker, no relationship field. BindInstructorIdentity writes ONE per claimed identityKey, " +
			"CreateOnly: the key ITSELF (identical regardless of WHICH instructor is claiming) is the lock — a " +
			"second, different instructor passing the same identityKey collides at commit (RevisionConflict), " +
			"never a silent double-bind. Declaration-only: no op handler (BindInstructorIdentity's script, owned " +
			"by the instructor vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the identity), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "identity instructor-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.instructorClaim; claimed once by BindInstructorIdentity's identityKey wiring. A second, different instructor claiming the same identity is rejected.",
			},
		},
	}
}

func refundVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: refundVertexDDL,
		// Deliberately meta.ddl.aspectType, not vertexType, even though a
		// wellnessrefund IS a full vertex (root + .detail aspect), not an
		// aspect. CancelBooking is already the booking vertexType DDL's own
		// op (bookingVertexDDL claims it) — ddl_cache.go's buildByCommand
		// only counts vertexType-Kind DDLs toward the operationType→class
		// ambiguity check (an op admitted by two vertexType DDLs is DROPPED
		// from the index, breaking dispatch for every CancelBooking caller).
		// step6_validate.go's write-gate itself resolves governance by class
		// name alone, irrespective of vertexType/aspectType Kind — an
		// aspectType-Kind DDL still fully gates this vertex's create
		// mutation. So this Kind choice is the same "declaration-only,
		// write-gate but not a dispatch owner" idiom every other aspectType
		// DDL in this file already uses, just applied to a vertex-shaped
		// mutation instead of a literal .aspect key.
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CancelBooking", "ReleaseOrphanedBooking", "SetBookingAttendance"},
		Description: "Wellness refund marker — reverses either a class-price charge or a no-show fee, the " +
			"two independent wellnesstransaction shapes a booking can carry. Vertex shape: " +
			"vtx.wellnessrefund.<NanoID>, class=wellnessrefund, root data = {} (minimal, D5 — the data lives in " +
			"the .detail aspect). Minted by CancelBooking (booking vertexType DDL, ddls.go) when the booking " +
			"being cancelled already carries a posted settlesClassPrice charge AND the cancellation lands more " +
			"than two hours before the session's startsAt — inside that late-cancellation window the charge is " +
			"forfeited rather than reversed, so no marker mints at all and the debit stands exactly as a " +
			"no-show's does — unless the seat was itself handed from the waitlist inside that window " +
			"(.status.promotedAt at or after the cutoff), in which case the marker mints as for an early cancel. " +
			"Also minted by ReleaseOrphanedBooking under the same posted-charge condition, " +
			"unconditionally: the member did nothing to cause a studio-initiated cancellation, so there is no " +
			"late-cancellation window to check — a posted charge always reverses, for BOTH shapes: a " +
			"still-booked/waitlisted orphaned booking's settlesClassPrice charge, and an already-noShow orphaned " +
			"booking's settles (no-show fee) charge — the studio called off the class either way, so the fee a " +
			"member never had a chance to avoid reverses the same as the class price does. Also minted by " +
			"SetBookingAttendance when a re-mark corrects a booking from noShow back to attended and a " +
			"no-show-fee charge already posted — the fee was wrong, not merely forgiven, so it reverses the same " +
			"way; guarded against minting twice for the same charge across a later noShow<->attended cycle by " +
			"checking for an existing reverses link first (booking vertexType DDL, ddls.go). A posted " +
			"charge is the one case wellness-ledger's own " +
			"wellnessClassPriceSettlement/wellnessNoShowSettlement lenses cannot self-correct, since their " +
			"`MATCH (bk:booking {key: $actorKey})` simply stops matching a tombstoned booking (no charge is " +
			"ever posted for a booking cancelled BEFORE it was charged; that half of each gap needs no fix). " +
			"Exists because the booking itself is tombstoned in the SAME mutation batch that discovers the " +
			"charge — Contract #1's isDeleted read-filtering means no post-tombstone lens walk could ever find " +
			"a marker written onto the booking's own aspects (the exact ReleaseOrphanedBooking .status.session " +
			"precedent this mirrors, bookingStatusAspectTypeDDL above), so the marker must live on a vertex " +
			"that survives the cancellation — a fresh one, not the booking. Carries the reverses link " +
			"(wellnessrefund→wellnesstransaction, this refund is the later-arriving vertex — Contract #1 §1.1) " +
			"to the original charge, which wellness-ledger's wellnessRefundSettlement lens (lenses.go) walks to " +
			"converge the refund by dispatching WellnessCreditAccount{accountKey, amountCents, refundRef}. Also " +
			"carries a memo (which charge shape this reverses — the settlement lens projects it verbatim rather " +
			"than a hardcoded label, since one marker type now serves two) and a className/classStartsAt " +
			"snapshot, read off the cancelled booking's own .status (bookingStatusAspectTypeDDL above) at mint " +
			"time — wellnessLedgerHistory reads them off THIS marker for a refund row, since by the time that " +
			"lens runs the booking itself is already tombstoned.",
		Script: refundDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string"},"amountCents":{"type":"number"},"bookingKey":{"type":"string"},"memo":{"type":"string"},"className":{"type":"string"},"classStartsAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey":    "The wellnessaccount the reversed charge was posted to. Stored on the .detail aspect, not root data (D5).",
			"amountCents":   "The exact amountCents of the original charge (read off its .entry aspect at mint time, not re-derived from a current price/fee — the refund must match what was actually charged).",
			"bookingKey":    "The cancelled/released booking this refund traces back to. Internal bookkeeping context only — the reverses link, not this field, carries the relationship to the original charge transaction.",
			"memo":          "\"Class price refund\" or \"No-show fee refund\", set by the minting op to say which charge shape this marker reverses. wellness-ledger's wellnessRefundSettlement lens projects it verbatim onto the credit's own memo instead of a hardcoded label.",
			"className":     "The booking's own .status.className at mint time (bookkeeping only, not a relationship). wellness-ledger's wellnessLedgerHistory lens reads it for a refund row since the booking itself is tombstoned by the time that lens runs.",
			"classStartsAt": "The booking's own .status.classStartsAt at mint time, the same snapshot idiom as className.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "wellnessrefund — a class-price refund marker",
				Payload:         map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 1500, "bookingKey": "vtx.booking.<NanoID>", "memo": "Class price refund", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.wellnessrefund.<NanoID> (root {}) + .detail aspect; minted by CancelBooking when a settlesClassPrice charge already existed for the booking and the cancellation lands more than two hours before startsAt, or by ReleaseOrphanedBooking unconditionally on a posted charge. wellness-ledger's wellnessRefundSettlement lens converges it into a WellnessCreditAccount.",
			},
			{
				Name:            "wellnessrefund — a no-show-fee refund marker",
				Payload:         map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "bookingKey": "vtx.booking.<NanoID>", "memo": "No-show fee refund", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z"},
				ExpectedOutcome: "Minted by ReleaseOrphanedBooking when the booking it releases was already noShow and carries a posted no-show-fee charge (a settles link) — the studio called off the class after the auto-no-show sweep had already fired, so the fee reverses the same way a class-price charge does.",
			},
		},
	}
}

func refundDetailAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     refundDetailAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CancelBooking", "ReleaseOrphanedBooking", "SetBookingAttendance"},
		Description: "Refund-marker detail aspect (wellness). Stored as vtx.wellnessrefund.<NanoID>.detail " +
			"(class wellnessRefundDetail) = {accountKey, amountCents, bookingKey, memo, className?, " +
			"classStartsAt?}. Non-sensitive. Written once, atomically alongside the wellnessrefund vertex it " +
			"belongs to, by CancelBooking, ReleaseOrphanedBooking, or SetBookingAttendance (booking vertexType " +
			"DDL, ddls.go) — never updated afterward. memo says which charge shape this reverses (\"Class price refund\" or " +
			"\"No-show fee refund\") — wellness-ledger's wellnessRefundSettlement lens projects it verbatim " +
			"instead of a hardcoded label, since one marker type now serves both. className/classStartsAt are " +
			"the booking's own .status snapshot at mint time, carried onto this marker because the booking " +
			"itself is tombstoned in the same mutation batch. Declaration-only: no op handler of its own.",
		Script:       refundDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"accountKey":{"type":"string"},"amountCents":{"type":"number"},"bookingKey":{"type":"string"},"memo":{"type":"string"},"className":{"type":"string"},"classStartsAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey":    "The wellnessaccount the original charge posted to.",
			"amountCents":   "The exact amountCents of the original charge, read off its .entry aspect at mint time.",
			"bookingKey":    "The cancelled/released booking this refund traces back to (bookkeeping only).",
			"memo":          "\"Class price refund\" or \"No-show fee refund\" — which charge shape this marker reverses.",
			"className":     "The booking's own .status.className at mint time (bookkeeping only).",
			"classStartsAt": "The booking's own .status.classStartsAt at mint time (bookkeeping only).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "wellnessrefund detail aspect",
				Payload:         map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 1500, "bookingKey": "vtx.booking.<NanoID>", "memo": "Class price refund", "className": "Vinyasa Flow", "classStartsAt": "2026-07-08T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.wellnessrefund.<NanoID>.detail; written once by CancelBooking or ReleaseOrphanedBooking alongside the wellnessrefund vertex it names.",
			},
		},
	}
}

// refundDeclarationOnlyScript is the shared no-op script for both
// wellnessrefund DDLs above — CancelBooking's script (bookingDDLScript,
// owned by the booking vertexType DDL) is what actually mints these, so
// this never executes as a dispatch target. Mirrors aspectDeclarationOnlyScript
// below, worded for a vertex+aspect pair instead of a lone aspect.
const refundDeclarationOnlyScript = `
def execute(state, op):
    fail("InvalidState: this DDL is declaration-only; wellnessrefund is minted by CancelBooking, owned by the booking vertexType DDL")
`

// aspectDeclarationOnlyScript is the shared no-op script for every
// declaration-only aspect-type DDL — its op handler lives on the owning
// vertexType DDL, so this script never executes as a dispatch target (the
// operationType→script index always resolves to the vertexType DDL first).
// Mirrors clinic-domain's identical helper.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("InvalidState: this aspect-type DDL is declaration-only; its op is owned by a vertexType DDL")
`

// studioDDLScript handles CreateStudio + TombstoneStudio. Known-key reads
// off the hydrated state — the optional location endpoint is a required
// declared read when the param is supplied, never a kv.Read — plus the
// sanctioned bounded enumerations (Contract #2 §2.5.1): the holdsRole /
// worksAt confinement walks shared with the session script, TombstoneStudio's
// one-page locatedAt walk that resolves the studio's own building for that
// confinement, and its paged inbound atStudio walk (with a per-candidate
// session + .schedule follow-up read) that refuses a retire while any class
// at the studio is still upcoming.
const studioDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, rev):
    return {"op": "update", "key": vtx_key + "." + local_name, "expectedRevision": rev,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_fee_cents(p, name):
    # An optional money amount in whole cents: absent or null is "not
    # supplied"; anything else must be a non-negative integer, refused at
    # the mint rather than tolerated. The value is load-bearing downstream --
    # SetBookingAttendance derives a member's no-show charge from it -- so a
    # fractional, negative, boolean or string amount is never stored for the
    # reader to trip over. A whole-valued JSON number arrives as a Starlark
    # int (the sandbox converts 25.0 to 25); a fractional one arrives as a
    # float and is refused by the type test.
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be a whole number of cents; got " + str(v))
    if v < 0:
        fail("InvalidArgument: " + name + ": must not be negative; got " + str(v))
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def optional_text(p, name):
    # An optional text field on an EDIT: absent, null or blank is "not
    # supplied" (the stored value is carried); any non-string is refused
    # rather than silently read as absent, so a form that sends a number
    # where a name belongs learns so instead of having its edit dropped.
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(""):
        fail("InvalidArgument: " + name + ": must be a string; got " + str(v))
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

# The concrete location levels a studio may be locatedAt (Contract #6 §6.9).
# location-domain owns the vertices; this script references them by KEY TYPE —
# a location vertex's class IS its own key type, so no single class value names
# the family.
LOCATION_TYPES = ["unit", "building", "property"]

# The full set of classes a live location vertex may carry: its own key type,
# the class every location gets.
LOCATION_CLASSES = LOCATION_TYPES

def require_live_location(state, key, name):
    # Alive, keyed vtx.<locationType>.<NanoID> at an admitted location level,
    # AND carrying a location class.
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is the type authority — it is what a lens
    # label resolves against, and it is the only thing that can say "any
    # location" across the three levels, since a location's class is its own
    # key type. The CLASS is what proves location-domain minted the vertex: a
    # foreign package writing vtx.unit.<id> with a class of its own passes the
    # key check and must still be refused.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    lt, _ = parts_of(key, name, "")
    if lt not in LOCATION_TYPES:
        fail("NotALocation: " + name + ": " + key + " has type segment " + str(lt) + ", required one of unit, building, property")
    cls = class_of(state, key)
    if cls not in LOCATION_CLASSES:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required its own location type")

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self caller is bound instead by its own op's ownership probe (the
    # applicationFor / identifiedBy indirection): a resident legitimately holds
    # no worksAt link, and confining them by a rule written for staff would deny
    # every self-service write. The two guards are complementary, not
    # alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def studio_locations(studio_key):
    # The studio's own building -- the studio -locatedAt-> location link
    # CreateStudio writes. The caller has already proved the studio alive off
    # the declared read, so no vertex gate precedes the walk here.
    # read-posture: (e) relation=locatedAt epoch=none -- CreateStudio writes
    # at most ONE locatedAt link and no op repoints or adds to it, so a page
    # of one is the whole set; the list shape is the consumer's contract.
    page, _ = kv.Links(studio_key, "locatedAt", "out", None, 1)
    locs = []
    for lk in page:
        if not lk.isDeleted:
            locs.append(lk.targetVertex)
    return locs

STUDIO_SESSION_PAGE_LIMIT = 256
MAX_STUDIO_SESSION_PAGES = 64

def require_no_upcoming_classes(studio_key, submitted):
    # A studio still holding an upcoming class is refused from
    # TombstoneStudio, not silently retired out from under its booked, charged
    # classes -- call the class off first (TombstoneSession /
    # TombstoneSessionSeries release its seats and refund through
    # wellnessOrphanedBookingSettlement). Enumerated via the sanctioned bounded
    # kv.Links (Contract #2 §2.5.1), direction "in" -- the studio is the
    # atStudio link's TARGET (session is source, per Contract #1 §1.1). A
    # session ReassignSession moved to another studio has this studio's link
    # tombstoned and is passed over; a live LINK alone does not mean a class:
    # TombstoneSession leaves the atStudio link live, so each candidate's
    # session vertex is read and only a live one whose .schedule.startsAt is
    # still ahead of submittedAt blocks. A class that has started is HISTORY
    # and never blocks a retire -- the studio's record is not what is being
    # removed. Canonical-UTC RFC3339 compares lexically == chronologically,
    # the same at-the-boundary reading TombstoneSessionSeries uses: starting
    # exactly at submittedAt counts as started.
    #
    # This is a prove-absence walk over the studio's LIFETIME of atStudio
    # links -- history keeps its link and its vertex, so an accepted retire
    # reads every class the studio ever hosted. The .schedule is read first
    # and a started or scheduleless class is passed over on that one read;
    # the vertex read (is it tombstoned?) is paid only for a class the
    # schedule says is still ahead. One page of 256 links costs at most
    # 1 + 256 reads on a fully-historical studio, so the wall (the script
    # budget, not the live-read budget) is what bounds a long-lived studio.
    cursor = None
    for _page in range(MAX_STUDIO_SESSION_PAGES):
        # read-posture: (e) relation=atStudio epoch=none (read-only guard: a
        # class scheduled concurrently with the retire slips past -- accepted,
        # the same posture identity-hygiene's open-task guard records; the
        # stranded session then reads missingStudio and ReassignSession's
        # operator repair path moves it)
        links, cursor = kv.Links(studio_key, "atStudio", "in", cursor, STUDIO_SESSION_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            sess_key = lk.sourceVertex
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key)
            sched = kv.Read(sess_key + ".schedule")
            if sched == None or sched.isDeleted:
                continue
            starts_at = sched.data.get("startsAt")
            if starts_at == None or not (submitted < starts_at):
                continue
            # A live LINK plus an upcoming schedule is still not a class:
            # TombstoneSession leaves both behind and tombstones only the
            # vertex, so the root decides.
            # read-posture: (e) per-candidate follow-up read, same walk.
            sess = kv.Read(sess_key)
            if sess == None or sess.isDeleted:
                continue
            fail("HasUpcomingClasses: " + studio_key + " still has an upcoming class " + sess_key + " starting " + starts_at + "; call it off first")
        if cursor == None:
            return
    fail("StudioSessionFanoutTooLarge: " + studio_key + " has too many atStudio links to enumerate at retire time; call off enough classes to bring it under the page cap first")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateStudio":
        name = required_string(p, "name")
        no_show_fee = optional_fee_cents(p, "noShowFeeCents")
        loc = optional_string(p, "location")

        # Staff-standing confinement: a studio is opened AT a location, and the
        # candidate is the one location this op is about to link -- the only
        # place the new studio will ever sit. An OMITTED location yields an
        # empty candidate list, which require_workplace denies for anyone but an
        # operator: a studio with no location is legal but un-browsable, and
        # minting one stays an operator ceremony rather than a hole a staffer
        # could open a studio through and locate afterwards.
        #
        # It answers ahead of require_live_typed below so a staffer outside the
        # building learns nothing about which location keys exist; a malformed
        # key is a denial here too (worksAt_covers rejects a non-3-segment key)
        # rather than an InvalidArgument that would distinguish the two.
        # workplace-exempt: (no-validated-path) CreateStudio is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            loc_candidates = []
            if loc != None:
                loc_candidates.append(loc)
            require_workplace(loc_candidates, "cannot open a studio at " + str(loc))

        sid = bare_nanoid_or_mint(p, "studioId")
        skey = "vtx.studio." + sid
        # The no-show policy is recorded only when supplied: a profile with no
        # noShowFeeCents means "no policy recorded", which SetBookingAttendance
        # bills at its documented default. A supplied 0 IS a policy (fee-free)
        # and is stored as such.
        profile = {"name": name}
        if no_show_fee != None:
            profile["noShowFeeCents"] = no_show_fee
        mutations = [
            make_vtx(skey, "studio", {}),
            make_aspect(skey, "profile", "studioProfile", profile),
        ]
        if loc != None:
            ltype, lid = parts_of(loc, "location", "")
            require_live_location(state, loc, "location")
            mutations.append(make_link("lnk.studio." + sid + ".locatedAt." + ltype + "." + lid,
                                       skey, loc, "locatedAt", "locatedAt", {}))
        events = [{"class": "wellness.studioCreated", "data": {"studioKey": skey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": skey}}

    if ot == "TombstoneStudio":
        skey = required_string(p, "studioKey")
        if not vertex_alive(state, skey):
            fail("UnknownStudio: " + skey)
        # Staff-standing confinement: a front-of-house caller retires only a
        # studio at a building they worksAt, resolved off the studio's own
        # locatedAt link. An unlocated studio yields an empty candidate list,
        # which require_workplace denies for anyone but an operator -- the
        # same posture CreateStudio takes on minting one. It answers BEFORE
        # the upcoming-classes walk below, so a staffer at another building
        # learns nothing about this studio's schedule from the refusal.
        # workplace-exempt: (no-validated-path) TombstoneStudio is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no
        # task mints it, so nothing but the operator escape reaches the
        # exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(skey), "cannot retire " + skey)
        require_no_upcoming_classes(skey, time.rfc3339_utc(op.submittedAt))
        mutations = [make_tombstone(skey)]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": skey}}

    if ot == "SetStudioProfile":
        skey = required_string(p, "studioKey")
        parts_of(skey, "studioKey", "studio")
        if not vertex_alive(state, skey):
            fail("UnknownStudio: " + skey)
        cls = class_of(state, skey)
        if cls != "studio":
            fail("WrongClass: studioKey: " + skey + " has class " + str(cls) + ", required studio")
        name = optional_text(p, "name")
        no_show_fee = optional_fee_cents(p, "noShowFeeCents")
        if name == None and no_show_fee == None:
            fail("InvalidArgument: at least one of name, noShowFeeCents is required")
        # Staff-standing confinement: the same studio-at-a-building-they-worksAt
        # rule TombstoneStudio applies, resolved off the studio's own locatedAt
        # link; an unlocated studio yields an empty candidate list and stays an
        # operator ceremony. It answers before the profile is read, so a
        # staffer at another building learns nothing about this studio's
        # policy from the refusal.
        # workplace-exempt: (no-validated-path) SetStudioProfile is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no
        # task mints it, so nothing but the operator escape reaches the
        # exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(skey), "cannot set the profile of " + skey)
        profile_key = skey + ".profile"
        # read-posture: (a) declared read at SetStudioProfile dispatch -- the
        # profile is MERGED, not replaced: a descriptor-driven form cannot
        # pre-fill an editable field from the studio row, so a form that
        # changes only the fee would otherwise have to re-type the name (or
        # blank it). Each supplied field overwrites its stored value and an
        # omitted one is carried forward off this read; the OCC upsert below
        # is keyed on the read's revision so two concurrent edits commit one.
        # CreateStudio always mints the aspect, so it is absent only when it
        # was removed out of band: a submitter that DECLARES the read faults
        # HydrationMiss before this branch runs, and one that does not falls
        # through to a live get that finds any profile that exists -- so the
        # refusal below is reached exactly by an undeclared submitter on a
        # studio whose profile is genuinely gone, and there is nothing to
        # merge over.
        profile = kv.Read(profile_key)
        if profile == None or profile.isDeleted:
            fail("InvalidState: " + profile_key + " is missing; CreateStudio mints it, so the aspect was removed out of band -- nothing to merge over")
        merged = {}
        for field in ["name", "noShowFeeCents"]:
            carried = profile.data.get(field)
            if carried != None:
                merged[field] = carried
        if name != None:
            merged["name"] = name
        if no_show_fee != None:
            merged["noShowFeeCents"] = no_show_fee
        elif "noShowFeeCents" in merged:
            # A carried policy is re-validated on the way through: the stored
            # value is what SetBookingAttendance bills, and a rename must not
            # re-write a malformed one (a row that predates the mint check)
            # under a fresh revision as if it were sound. The repair is the
            # other field of this same op.
            carried_fee = merged["noShowFeeCents"]
            if type(carried_fee) != type(0) or carried_fee < 0:
                fail("InvalidState: " + profile_key + ".noShowFeeCents is " + str(carried_fee) + ", not a non-negative whole number of cents; supply noShowFeeCents to repair it")
        mutations = [make_aspect_upsert_occ(skey, "profile", "studioProfile", merged, profile.revision)]
        events = [{"class": "wellness.studioProfileSet", "data": {"studioKey": skey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": skey}}

    fail("UnknownOperation: " + ot)
`

// sessionDDLScript handles CreateSession + TombstoneSession, mirroring
// clinic-domain's appointment DDL's slot_cells/claim_cell double-book guard
// exactly (hub renamed provider→studio; no patient-side symmetric claim —
// see wellness-vertical-design.md §1).
const sessionDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, rev):
    return {"op": "update", "key": vtx_key + "." + local_name, "expectedRevision": rev,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # A BARE update -- no expectedRevision. For a key this operation HYDRATED
    # (declared by the dispatcher, or derived by derive_reads below) the
    # Processor conditions the write on the step-4 revision itself and
    # re-executes in-process on a conflict; an explicit pin on such a key
    # would forfeit that retry and surface as an unretried RevisionConflict.
    # Used for the series' .horizon, which every writer reaches through a
    # hydrated read (ExtendSessionSeries' playbook declares it, and
    # derive_reads derives it for the call-off and the move).
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_link_revive_occ(key, source, target, cls, local_name, expected_revision):
    # A Lattice tombstone is soft — the link key still occupies its subject,
    # so make_link's CreateOnly write (expectedRevision 0) rejects
    # RevisionConflict against a key that has EVER been minted before, even
    # long-dead. Any swap that can land on a key it (or an earlier op) has
    # already tombstoned once — ReassignSession's atStudio/ledBy swaps move
    # a session back and forth across a small, reused set of studios/
    # instructors — needs this OCC-conditioned revive instead, mirroring
    # clinic-domain's AssignProviderSite (site.go) exactly.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_create_or_revive(key, source, target, cls, local_name):
    # read-posture: (d) declared optionalReads at CreateSession/ReassignSession
    # dispatch — the create/revive idempotency branch this and clinic-domain's
    # AssignProviderSite share; an absent link is the common case for a
    # never-before-used pairing, never a required read.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail("InvalidState: " + key + " is already live — this should have been tombstoned first")
    if existing != None:
        return make_link_revive_occ(key, source, target, cls, local_name, existing.revision)
    return make_link(key, source, target, cls, local_name, {})

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def make_tombstone_occ(key, expected_revision):
    # A CAS-guarded tombstone, mirroring make_aspect_upsert_occ /
    # make_link_revive_occ above: the revision comes from a read this same
    # call site took, so the retraction and the check are atomic.
    return {"op": "tombstone", "key": key, "expectedRevision": expected_revision}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_int(p, name, lo, hi):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be an integer; got " + type(v))
    if v < lo or v > hi:
        fail("InvalidArgument: " + name + ": must be in [" + str(lo) + ", " + str(hi) + "]; got " + str(v))
    return v

def optional_int(p, name, lo):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be an integer; got " + type(v))
    if v < lo:
        fail("InvalidArgument: " + name + ": must be >= " + str(lo) + "; got " + str(v))
    return v

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64
# A page of one is not enough for a REPOINTED single-valued relation:
# ListLinks returns tombstoned links in the page too, keys sort by target id,
# and a repoint tombstones the old key and writes a new one -- so the live
# link can sort behind its own tombstoned predecessor. ReassignSession
# repoints both atStudio (a studio move) and ledBy (an instructor swap); both
# readers page until they find the live link.
LIVE_LINK_PAGE_LIMIT = 8
MAX_LIVE_LINK_PAGES = 4
# A session's atLocation snapshot names every room its studio sits at, a
# handful at most, and ReassignSession needs the FULL set (live and
# tombstoned) to decide revive/create/tombstone per room, so this walks every
# page rather than stopping at the first.
ATLOCATION_PAGE_LIMIT = 20
MAX_ATLOCATION_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self caller is bound instead by its own op's ownership probe (the
    # applicationFor / identifiedBy indirection): a resident legitimately holds
    # no worksAt link, and confining them by a rule written for staff would deny
    # every self-service write. The two guards are complementary, not
    # alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def studio_locations(studio_key):
    # A session's location is where its studio sits -- the studio -locatedAt->
    # location link wellness-domain writes at CreateStudio.
    #
    # The studio VERTEX first: TombstoneStudio soft-deletes it with no cascade
    # onto locatedAt, so a decommissioned studio would otherwise keep conferring
    # its old building.
    if not vertex_live(studio_key):
        return []
    # read-posture: (e) relation=locatedAt epoch=none -- CreateStudio writes
    # at most ONE locatedAt link and no op repoints or adds to it, so a page
    # of one is the whole set; the list shape is the consumer's contract.
    page, _ = kv.Links(studio_key, "locatedAt", "out", None, 1)
    locs = []
    for lk in page:
        if not lk.isDeleted:
            locs.append(lk.targetVertex)
    return locs

GRID_MINUTES_STR = ["00", "15", "30", "45"]
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

# TombstoneSessionSeries' and ReassignSessionSeries' partOf walk. For an
# EAGER series the bound is exact: CreateSessionSeries mints at most
# occurrenceCount (<= 52) partOf links and nothing adds to them, so 2 pages
# of 64 strictly exceeds the largest set that can exist, the walk always
# reaches its end, and "found no eligible occurrence" is never a bound
# artifact. A ROLLING series is different: ExtendSessionSeries hangs one more
# occurrence off the series per window move, its history is never pruned
# (the series is never tombstoned), so a long-lived rolling run's partOf set
# grows past this budget -- and, before it does, past the Starlark wall (one
# live read per historic occurrence). The whole-run call-off and move of such
# a run refuse SeriesWalkBound (or time out) rather than act on part of it;
# the off switch that never walks is StopSessionSeries, which closes the
# horizon alone, and what remains on the grid is cancelled or moved per class
# with TombstoneSession / ReassignSession. PromoteWaitlistedBookings'
# WaitlistWalkBound has the same shape for the same reason (a forSession-in
# set that grows with history).
SERIES_OCCURRENCE_PAGE_LIMIT = 64
SERIES_OCCURRENCE_MAX_PAGES = 2

def enforce_grid(starts_at, ends_at):
    for label, t in [("startsAt", starts_at), ("endsAt", ends_at)]:
        if len(t) != 20:
            fail("SlotGridViolation: " + label + ": must be a canonical whole-second UTC instant; got " + t)
        if t[17:19] != "00" or t[14:16] not in GRID_MINUTES_STR:
            fail("SlotGridViolation: " + label + " must align to the 15-minute booking grid (:00/:15/:30/:45); got " + t)

def slot_cells(starts_at, ends_at):
    cells = []
    cur = starts_at
    for _i in range(MAX_SLOT_CELLS + 1):
        if not (cur < ends_at):
            return cells
        cells.append(cur)
        cur = time.rfc3339_add(cur, GRID_STEP)
    fail("SessionTooLong: session spans more than " + str(MAX_SLOT_CELLS) + " 15-minute slots (24h); shorten the interval")

def slot_cellcode(cell_start):
    return cell_start.replace("-", "").replace(":", "").lower()

def instant_seconds(t):
    # Whole seconds since 1970-01-01T00:00:00Z of a canonical whole-second UTC
    # instant ("YYYY-MM-DDTHH:MM:SSZ" -- the form time.rfc3339_utc emits and
    # every .schedule stores). The time module exposes no epoch or subtraction
    # builtin, only rfc3339_add over a Go duration string, so the DIFFERENCE
    # between two instants (ReassignSessionSeries' shift, and the span it
    # carries onto every occurrence) is derived here from the civil date by
    # pure integer arithmetic: days-from-civil over the proleptic Gregorian
    # calendar (400-year eras of 146097 days, the year rebased to begin in
    # March so the leap day falls last in it), then the seconds_of_day builtin
    # for the time part. Deterministic -- a function of the string alone, no
    # clock.
    y = int(t[0:4])
    m = int(t[5:7])
    d = int(t[8:10])
    if m <= 2:
        y -= 1
    era = y // 400
    yoe = y - era * 400
    doy = (153 * ((m + 9) % 12) + 2) // 5 + d - 1
    doe = yoe * 365 + yoe // 4 - yoe // 100 + doy
    days = era * 146097 + doe - 719468
    return days * 86400 + time.seconds_of_day(t)

# ReassignSessionSeries refuses a move that carries the run further than this
# from where it stands: a Go duration string (rfc3339_add's second argument)
# overflows at roughly 292 years, and a term-wide move measured in years is
# not a time move a front desk makes -- the bound keeps the refusal a clean
# InvalidArgument rather than an opaque duration-parse failure at the edge.
SERIES_MAX_SHIFT_SECONDS = 366 * 86400

# ReassignSessionSeries' mutation ceiling. One operation commits as ONE
# JetStream atomic batch of at most substrate.MaxBatchMessages (1000, NATS
# 2.14 ADR-50) messages, two of which the Processor spends on the idempotency
# tracker and the event outbox (step8_commit.go), so the business budget is
# 998; past it the commit dies as a batch-too-large fault, never a refusal the
# desk can read. A move at CreateSessionSeries' own ceiling -- 52 occurrences,
# 5 cells each, instructor-led, shifted clear of its old cells -- assembles
# 52 * (5 + 5 + 5 + 5 + 1) = 1092 mutations, so the shape is reachable. The
# script counts what it assembled and refuses SeriesTooLarge above this,
# with headroom under the 998 so a commit-time addition never reopens the
# fault. A shift onto cells the run already holds (its own interval) writes
# only the difference and lands well under it.
SERIES_MOVE_MAX_MUTATIONS = 990

def claim_cell(hub, cellcode, cls, conflict_code, who):
    key = hub + ".slot" + cellcode
    # read-posture: (d) optionalReads — derived server-side by this script's
    # own derive_reads(op) for CreateSession/CreateSessionSeries, still
    # client-declared for ReassignSession (see derive_reads' doc comment for
    # why that op is excluded), and a class-(e) follow-up of the partOf walk
    # for ReassignSessionSeries (each cell is a function of a walked
    # occurrence's schedule). An absent cell is the common case (no
    # existing booking), never a required read.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})

def cell_live(hub, cellcode):
    # Is this slot cell held? ExtendSessionSeries' mint-or-skip probe: the
    # same key claim_cell reads, answered without refusing, because a held
    # cell on the occurrence a rolling window would mint means the slot was
    # booked one-off and the run passes that week -- the horizon still moves.
    # read-posture: (d) optionalReads -- derived server-side by this script's
    # own derive_reads(op) for ExtendSessionSeries (CreateSession's own cell
    # arm); an absent cell is the common case, never a required read.
    existing = kv.Read(hub + ".slot" + cellcode)
    return existing != None and not existing.isDeleted

def mint_occurrence(series_id, series_key, studio, studio_id, studio_locs, instructor, instructor_id,
                    name, capacity, price_cents, resident_price_cents, occ_starts, occ_ends):
    # One occurrence of a series: CreateSession's own mutation shape -- the
    # session vertex, .schedule with remindAt, atStudio, one atLocation per
    # studio location, the optional ledBy link, a studioSlotClaim per covered
    # cell and an instructorSlotClaim per cell when led -- plus the partOf
    # link back to the series. Shared by CreateSessionSeries (the eager batch,
    # once per occurrence) and ExtendSessionSeries (the one occurrence a
    # rolling window moves onto), so the two can never mint different shapes.
    # Returns (session key, mutations). A cell collision refuses
    # StudioConflict/InstructorConflict from claim_cell exactly as
    # CreateSession's does.
    cells = slot_cells(occ_starts, occ_ends)

    sess_id = nanoid.new()
    sess_key = "vtx.session." + sess_id

    # remindAt = startsAt - 24h, the deadline wellness-reminders' lens arms
    # (the constrained rule engine has no date arithmetic of its own).
    occ_remind_at = time.rfc3339_add(occ_starts, "-24h")
    sched = {"name": name, "startsAt": occ_starts, "endsAt": occ_ends, "capacity": capacity, "remindAt": occ_remind_at}
    if price_cents != None:
        sched["priceCents"] = price_cents
    if resident_price_cents != None:
        sched["residentPriceCents"] = resident_price_cents

    mutations = [
        make_vtx(sess_key, "session", {}),
        make_aspect(sess_key, "schedule", "sessionSchedule", sched),
        make_link("lnk.session." + sess_id + ".atStudio.studio." + studio_id,
                  sess_key, studio, "atStudio", "atStudio", {}),
    ]
    # A snapshot of the studio's live locatedAt link(s), independent of the
    # studio's LATER status -- CreateSession's own atLocation idiom (see its
    # arm for why a tombstoned studio needs it).
    for loc in studio_locs:
        ltype, lid = parts_of(loc, "location", "")
        mutations.append(make_link("lnk.session." + sess_id + ".atLocation." + ltype + "." + lid,
                                   sess_key, loc, "atLocation", "atLocation", {}))
    if instructor != None:
        mutations.append(make_link("lnk.session." + sess_id + ".ledBy.instructor." + instructor_id,
                                   sess_key, instructor, "ledBy", "ledBy", {}))
    # "session partOf sessionseries" (Contract #1 §1.1) -- the series is the
    # pre-existing anchor its occurrences point at, mirroring atStudio's own
    # studio-is-target direction.
    mutations.append(make_link("lnk.session." + sess_id + ".partOf.sessionseries." + series_id,
                               sess_key, series_key, "partOf", "partOf", {}))
    for c in cells:
        cc = slot_cellcode(c)
        mutations.append(claim_cell(studio, cc, "studioSlotClaim", "StudioConflict", "studio"))
    # Instructor slot-claim per occurrence (providerSlotClaim mirror,
    # verticals.md): the studio's cell lock alone only guards the ROOM.
    if instructor != None:
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(instructor, cc, "instructorSlotClaim", "InstructorConflict", "instructor"))
    return sess_key, mutations

def require_matching_studio(sess_id, studio):
    _, studio_id = parts_of(studio, "studio", "studio")
    at_studio_lnk = "lnk.session." + sess_id + ".atStudio.studio." + studio_id
    # read-posture: (a) declared reads at TombstoneSession/ReassignSession
    # dispatch (validation link; absence means the caller named the wrong
    # studio — WrongStudio).
    asl = kv.Read(at_studio_lnk)
    if asl == None or asl.isDeleted:
        fail("WrongStudio: studio " + studio + " is not the studio of session vtx.session." + sess_id)
    return studio_id

def release_cells_mutations(studio, sched):
    if sched == None or sched.isDeleted:
        return []
    s_starts = sched.data.get("startsAt")
    s_ends = sched.data.get("endsAt")
    if s_starts == None or s_ends == None:
        return []
    out = []
    for c in slot_cells(s_starts, s_ends):
        cc = slot_cellcode(c)
        out.append(make_tombstone(studio + ".slot" + cc))
    return out

def session_ledby_link(sess_key):
    # A session carries AT MOST ONE LIVE instructor, so this returns its live
    # (link key, instructor vertex key, link revision) or (None, None, None).
    # ReassignSession's instructor swap tombstones the current ledBy link and
    # writes a new one (a REPOINT, not a write-once fact), so a page can hold
    # the tombstone before the live link -- this pages until it finds one.
    # The target vertex is what TombstoneSession / ReassignSession need to
    # release or migrate the instructor's instructorSlotClaim cells; the link
    # key is what they tombstone to drop the ledBy edge itself, and the
    # revision is what lets that tombstone be CAS-guarded rather than blind.
    if not vertex_live(sess_key):
        return None, None, None
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=ledBy epoch=none -- bounded, never a
        # keyspace scan.
        page, cursor = kv.Links(sess_key, "ledBy", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                return lk.key, lk.targetVertex, lk.revision
        if cursor == None:
            break
    return None, None, None

def session_atstudio_link(sess_key):
    # A session carries EXACTLY ONE LIVE studio, so this returns its live
    # (link key, studio vertex key) or (None, None) -- the same bounded,
    # known-hub idiom session_ledby_link uses just above. ReassignSession's
    # studio move tombstones the current atStudio link and writes a new one
    # (a REPOINT), so a page can hold the tombstone before the live link;
    # this pages until it finds one. This is what lets ReassignSession's
    # operator-repair path (below) name the CURRENT studio server-side when
    # the studio is already tombstoned: TombstoneStudio never cascades onto
    # this link (package.go's "no cascade" doctrine), so the link survives
    # even though wellnessSessionsSpec's OPTIONAL MATCH on a live :studio
    # vertex (lenses.go) can no longer hand the FE that key back to
    # round-trip as the studio confirmation param.
    if not vertex_live(sess_key):
        return None, None
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=atStudio epoch=none -- bounded, never a
        # keyspace scan.
        page, cursor = kv.Links(sess_key, "atStudio", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                return lk.key, lk.targetVertex
        if cursor == None:
            break
    return None, None

def session_atlocation_links(sess_key):
    # Every atLocation link the session carries, live AND tombstoned, keyed by
    # link key. ReassignSession re-snapshots this set on a studio move, and a
    # Lattice tombstone is soft -- the key still occupies its subject -- so a
    # move BACK to a room the class already sat in must revive the dead link
    # rather than CreateOnly-collide on it. Carrying each link's revision out
    # of this one enumeration is what lets that revive happen with no
    # per-location point read behind it. This needs the FULL set (live and
    # tombstoned both), so it walks every page rather than stopping at the
    # first.
    cursor = None
    out = {}
    for _page in range(MAX_ATLOCATION_PAGES):
        # read-posture: (e) relation=atLocation epoch=none -- a session
        # snapshots at most one link per location its studio sits at, a
        # handful at most, off the session key the caller has already
        # proved alive; bounded, never a keyspace scan.
        page, cursor = kv.Links(sess_key, "atLocation", "out", cursor, ATLOCATION_PAGE_LIMIT)
        for lk in page:
            out[lk.key] = lk
        if cursor == None:
            break
    return out

def valid_vertex_key(key, want_type):
    # Lenient key-shape check for a pre-pass that must never fault (objects-base's
    # derive_reads sets this precedent): a malformed/wrong-type key derives
    # nothing rather than raising, leaving execute()'s own parts_of to fault the
    # real InvalidArgument.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    return len(parts) == 3 and parts[0] == "vtx" and parts[1] == want_type and parts[2] != ""

def derive_reads(op):
    # Contract #2 §2.5 class (g). CreateSession/CreateSessionSeries/
    # ExtendSessionSeries's studioSlotClaim/instructorSlotClaim cells are
    # entirely a function of the payload (studio/instructor/startsAt/endsAt),
    # so the caller does not declare them (cmd/wellness-app/web/app.js carries
    # no cell arithmetic). Mirrors this script's own slot_cells/slot_cellcode
    # exactly, so a derived key always matches what claim_cell and cell_live
    # actually read.
    #
    # The studio/instructor ROOTS ride the same declaration: require_live_typed
    # (state, key, ...) below decides UnknownEndpoint by testing key not in
    # state, which cannot tell "genuinely absent" from "never declared or
    # derived" apart, so an undeclared submitter would see a live endpoint
    # refused as unknown.
    #
    # ExtendSessionSeries additionally derives its series root, .definition
    # and .horizon (its playbook declares the same three as required reads;
    # the envelope's disposition wins on a key both name), so a submitter
    # that declares nothing still hydrates every key the arm reads by name.
    #
    # TombstoneSessionSeries, ReassignSessionSeries and StopSessionSeries
    # derive, off the payload's seriesKey, the series root, its .horizon, the
    # studio confirmation link and the studio root (and the move's anchor
    # pin) as optionalReads: a rolling series' .horizon must be hydrated by EVERY
    # dispatcher -- the app, Facet, a CLI -- because the call-off rewrites it
    # to stop the roll and the move shifts it, and a client that could omit
    # the declaration would otherwise cancel the classes and leave the horizon
    # minting the next window. Every key past these hangs off the partOf
    # walk, and kv is a failing stub in this pre-pass.
    #
    # ReassignSession is deliberately NOT covered here: its "edit only what's
    # supplied, carry the rest forward unchanged" semantics mean the cells a
    # move must claim are keyed off session state this pre-hydration pass has
    # no access to (kv is a failing stub here) whenever that state ISN'T also
    # in the payload -- the current schedule (session_atstudio_link's
    # kv.Links walk / kv.Read(sess_key + ".schedule") when studio/time are
    # unconfirmed) and, independently, the current instructor
    # (session_ledby_link) whenever the call doesn't ALSO name
    # newInstructor/clearInstructor. Even a call supplying newStudio AND an
    # explicit startsAt/endsAt still claims new_instr_cells against a
    # KV-resolved old_instructor when the instructor itself isn't touched, so
    # there is no payload shape (short of also editing the instructor on
    # every call) that is fully derivable. A partial derivation would be a
    # partial, silently-wrong read set on every other call shape. The
    # client-declared optionalReads stays load-bearing for this op.
    #
    ot = op.operationType
    p = op.payload
    if ot == "TombstoneSessionSeries" or ot == "ReassignSessionSeries" or ot == "StopSessionSeries":
        keys = []
        series_key = optional_string(p, "seriesKey")
        studio = optional_string(p, "studio")
        if valid_vertex_key(series_key, "sessionseries"):
            keys.append(series_key)
            keys.append(series_key + ".horizon")
            if valid_vertex_key(studio, "studio"):
                keys.append("lnk.sessionseries." + series_key.split(".")[2] + ".atStudio.studio." + studio.split(".")[2])
        if valid_vertex_key(studio, "studio"):
            keys.append(studio)
        if ot == "ReassignSessionSeries":
            anchor_key = optional_string(p, "anchorKey")
            if valid_vertex_key(anchor_key, "session"):
                keys.append(anchor_key)
                keys.append(anchor_key + ".schedule")
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    if ot != "CreateSession" and ot != "CreateSessionSeries" and ot != "ExtendSessionSeries":
        return {}
    # optional_string, never required_string: a malformed/empty/whitespace-only
    # field derives nothing rather than faulting the pre-pass — execute()'s own
    # required_string/optional_string still raises the real InvalidArgument
    # (objects-base's derive_reads sets this precedent). Raw getattr would see
    # "" as a truthy-looking key fragment where optional_string sees None.
    studio = optional_string(p, "studio")
    instructor = optional_string(p, "instructor")
    starts_at_raw = optional_string(p, "startsAt")
    ends_at_raw = optional_string(p, "endsAt")

    # The endpoint roots are derived independently of the time-span checks
    # below -- they are require_live_typed's own subject, unrelated to
    # slot-cell arithmetic, so a payload with a malformed span still gets its
    # endpoints hydrated (execute()'s own required_string/rfc3339_utc raises
    # the real rejection on the span).
    keys = []
    if valid_vertex_key(studio, "studio"):
        keys.append(studio)
    if valid_vertex_key(instructor, "instructor"):
        keys.append(instructor)
    if ot == "ExtendSessionSeries":
        series_key = optional_string(p, "seriesKey")
        if valid_vertex_key(series_key, "sessionseries"):
            keys += [series_key, series_key + ".definition", series_key + ".horizon"]

    if studio == None or starts_at_raw == None or ends_at_raw == None:
        return {} if len(keys) == 0 else {"optionalReads": keys}
    starts_at = time.rfc3339_utc(starts_at_raw)
    ends_at = time.rfc3339_utc(ends_at_raw)
    if not (starts_at < ends_at):
        return {} if len(keys) == 0 else {"optionalReads": keys}
    # slot_cells fails (SessionTooLong) past MAX_SLOT_CELLS -- a real rejection,
    # but a worse one to raise HERE: derive_reads runs before enforce_grid/
    # execute()'s own checks, so a too-long span would fault as an opaque
    # DeriveReadsFailed instead of execute()'s clean SessionTooLong/
    # SlotGridViolation. Bounding by the identical 24h ceiling slot_cells
    # enforces (MAX_SLOT_CELLS * GRID_STEP) and returning {} defers to that.
    if ends_at > time.rfc3339_add(starts_at, "24h"):
        return {} if len(keys) == 0 else {"optionalReads": keys}

    if ot == "CreateSession" or ot == "ExtendSessionSeries":
        cells = slot_cells(starts_at, ends_at)
        keys += [studio + ".slot" + slot_cellcode(c) for c in cells]
        if instructor != None:
            keys += [instructor + ".slot" + slot_cellcode(c) for c in cells]
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}

    # CreateSessionSeries: same per-occurrence cells CreateSession claims,
    # repeated occurrenceCount times on the intervalDays cadence -- mirrors
    # this script's own occurrence loop exactly (offset_hours_step below).
    interval_days = getattr(p, "intervalDays", None)
    occurrence_count = getattr(p, "occurrenceCount", None)
    if interval_days == None or occurrence_count == None:
        return {} if len(keys) == 0 else {"optionalReads": keys}
    # Mirror execute()'s own required_int(p, "occurrenceCount", 2, 52) bound:
    # this loop runs before that validation, so an out-of-range value here
    # would otherwise cost real Starlark budget before execute() ever gets to
    # reject it the normal way.
    if type(occurrence_count) != type(0) or occurrence_count < 2 or occurrence_count > 52:
        return {} if len(keys) == 0 else {"optionalReads": keys}
    if type(interval_days) != type(0) or interval_days < 1 or interval_days > 365:
        return {} if len(keys) == 0 else {"optionalReads": keys}
    offset_hours_step = interval_days * 24
    occ_starts = starts_at
    occ_ends = ends_at
    for i in range(occurrence_count):
        if i > 0:
            occ_starts = time.rfc3339_add(occ_starts, str(offset_hours_step) + "h")
            occ_ends = time.rfc3339_add(occ_ends, str(offset_hours_step) + "h")
        cells = slot_cells(occ_starts, occ_ends)
        keys += [studio + ".slot" + slot_cellcode(c) for c in cells]
        if instructor != None:
            keys += [instructor + ".slot" + slot_cellcode(c) for c in cells]
    if len(keys) == 0:
        return {}
    return {"optionalReads": keys}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateSession":
        studio = required_string(p, "studio")
        _, studio_id = parts_of(studio, "studio", "studio")
        require_live_typed(state, studio, "studio", "studio")

        # Staff-standing confinement: the location comes from the studio's OWN
        # locatedAt link, never the payload. A studio wired to no location is
        # operator-only by construction (an empty candidate list denies).
        # workplace-exempt: (no-validated-path) CreateSession is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot create a session at studio " + studio)

        # The studio's live location(s) at THIS moment, snapshotted below onto
        # the session's own atLocation link(s) -- a second bounded read off the
        # same studio, not a second class of access.
        studio_locs = studio_locations(studio)

        name = required_string(p, "name")
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)
        capacity = required_int(p, "capacity", 1, 200)
        # priceCents is OPTIONAL — 0 or omitted means a free class (verticals.md
        # "a wellness class still has no price or pass"; pass/membership is out
        # of scope for this increment). wellness-ledger's
        # wellnessClassPriceSettlement lens reads it to converge a per-booking
        # charge, the same convergence idiom SetBookingAttendance's
        # noShowFeeCents uses, just unconditional on attendance.
        price_cents = optional_int(p, "priceCents", 0)
        # residentPriceCents mirrors priceCents exactly, OPTIONAL: charged
        # instead of priceCents to a booking whose .status.rate is resident
        # (verticals.md "a verified resident is charged the same class price
        # as a walk-in"). Omitted means a resident pays priceCents same as a
        # standard booker — wellnessClassPriceSettlement's CASE WHEN falls
        # back exactly like a missing priceCents falls back to free.
        resident_price_cents = optional_int(p, "residentPriceCents", 0)

        enforce_grid(starts_at, ends_at)
        cells = slot_cells(starts_at, ends_at)

        sess_id = bare_nanoid_or_mint(p, "sessionId")
        sess_key = "vtx.session." + sess_id

        at_studio_lnk = "lnk.session." + sess_id + ".atStudio.studio." + studio_id

        # remindAt = startsAt − 24h, precomputed the same way clinic-domain
        # derives an appointment's .schedule.remindAt: wellness-reminders'
        # convergence lens has no date arithmetic of its own (the constrained
        # rule engine), so the deadline must be baked in at write time.
        remind_at = time.rfc3339_add(starts_at, "-24h")

        sched = {"name": name, "startsAt": starts_at, "endsAt": ends_at, "capacity": capacity, "remindAt": remind_at}
        if price_cents != None:
            sched["priceCents"] = price_cents
        if resident_price_cents != None:
            sched["residentPriceCents"] = resident_price_cents

        mutations = [
            make_vtx(sess_key, "session", {}),
            make_aspect(sess_key, "schedule", "sessionSchedule", sched),
            make_link(at_studio_lnk, sess_key, studio, "atStudio", "atStudio", {}),
        ]
        # A snapshot of the studio's live locatedAt link(s), independent of the
        # studio's LATER status: TombstoneStudio soft-deletes the studio with no
        # cascade onto locatedAt, so studio_locations' vertex_live gate goes to
        # [] for a tombstoned studio -- stranding front-of-house staff on a
        # session (and its outstanding bookings) that predates the tombstone.
        # session_locations falls back to this atLocation link when the studio
        # is dead, mirroring clinic-domain's atSite fallback for a tombstoned
        # provider (appointment_sites). A studio wired to no location writes no
        # atLocation link, exactly as it confers no workplace above.
        for loc in studio_locs:
            ltype, lid = parts_of(loc, "location", "")
            mutations.append(make_link("lnk.session." + sess_id + ".atLocation." + ltype + "." + lid,
                                       sess_key, loc, "atLocation", "atLocation", {}))
        # Optional instructor leading the class (persona-worlds-design.md Fire
        # W0): validated alive + typed, minted beside atStudio. Sentence:
        # "session ledBy instructor". Omitted → the session carries no
        # instructor, exactly the studio locatedAt idiom's optional-endpoint
        # shape (studioDDLScript's CreateStudio, above).
        instructor = optional_string(p, "instructor")
        if instructor != None:
            require_live_typed(state, instructor, "instructor", "instructor")
            _, instructor_id = parts_of(instructor, "instructor", "instructor")
            mutations.append(make_link("lnk.session." + sess_id + ".ledBy.instructor." + instructor_id,
                                       sess_key, instructor, "ledBy", "ledBy", {}))
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(studio, cc, "studioSlotClaim", "StudioConflict", "studio"))
        # Instructor slot-claim (providerSlotClaim mirror, verticals.md): the
        # studio's cell lock alone only guards the ROOM -- naming the same
        # instructor at two different studios for an overlapping span must
        # collide too. Claimed only when an instructor is named; a session
        # with no instructor claims none.
        if instructor != None:
            for c in cells:
                cc = slot_cellcode(c)
                mutations.append(claim_cell(instructor, cc, "instructorSlotClaim", "InstructorConflict", "instructor"))
        events = [{"class": "wellness.sessionCreated", "data": {"sessionKey": sess_key, "studio": studio}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": sess_key}}

    if ot == "CreateSessionSeries":
        # CreateSession, run occurrenceCount times on a fixed intervalDays
        # cadence, eagerly, in one atomic op — see sessionSeriesVertexTypeDDL's
        # doc comment for why this stays a bounded batch (every occurrence's
        # shape is fully known up front; there is nothing to wait on), and how
        # a rolling series is the same batch with a moving window.
        studio = required_string(p, "studio")
        _, studio_id = parts_of(studio, "studio", "studio")
        require_live_typed(state, studio, "studio", "studio")

        # Same staff-standing confinement as CreateSession, checked ONCE —
        # the whole series shares one studio.
        # workplace-exempt: (no-validated-path) CreateSessionSeries is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot create a session series at studio " + studio)
        studio_locs = studio_locations(studio)

        name = required_string(p, "name")
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)
        capacity = required_int(p, "capacity", 1, 200)
        price_cents = optional_int(p, "priceCents", 0)
        resident_price_cents = optional_int(p, "residentPriceCents", 0)
        interval_days = required_int(p, "intervalDays", 1, 365)
        occurrence_count = required_int(p, "occurrenceCount", 2, 52)
        enforce_grid(starts_at, ends_at)
        rolling = getattr(p, "rolling", None)
        if rolling != None and type(rolling) != type(True):
            fail("InvalidArgument: rolling: must be a boolean; got " + type(rolling))
        rolling = rolling == True

        # Validated ONCE, shared by every occurrence — CreateSession's own
        # per-call validation, hoisted above the loop.
        instructor = optional_string(p, "instructor")
        instructor_id = None
        if instructor != None:
            require_live_typed(state, instructor, "instructor", "instructor")
            _, instructor_id = parts_of(instructor, "instructor", "instructor")

        series_id = nanoid.new()
        series_key = "vtx.sessionseries." + series_id
        series_def = {
            "name": name, "capacity": capacity, "intervalDays": interval_days,
            "occurrenceCount": occurrence_count, "firstStartsAt": starts_at, "firstEndsAt": ends_at,
        }
        if price_cents != None:
            series_def["priceCents"] = price_cents
        if resident_price_cents != None:
            series_def["residentPriceCents"] = resident_price_cents

        mutations = [
            make_vtx(series_key, "sessionseries", {}),
            make_aspect(series_key, "definition", "sessionSeriesDefinition", series_def),
            make_link("lnk.sessionseries." + series_id + ".atStudio.studio." + studio_id,
                       series_key, studio, "atStudio", "atStudio", {}),
        ]

        # Each occurrence offsets the FIRST occurrence's startsAt/endsAt by
        # i*intervalDays days — a whole number of hours (a multiple of 24),
        # itself a multiple of the 15-minute grid step, so grid alignment
        # (already enforced above on the first occurrence) carries forward
        # without re-checking every occurrence.
        offset_hours_step = interval_days * 24
        occ_starts = starts_at
        occ_ends = ends_at
        session_keys = []
        for i in range(occurrence_count):
            if i > 0:
                occ_starts = time.rfc3339_add(occ_starts, str(offset_hours_step) + "h")
                occ_ends = time.rfc3339_add(occ_ends, str(offset_hours_step) + "h")
            # The whole batch rejects StudioConflict/InstructorConflict
            # together if any occurrence collides -- no partial series
            # (claim_cell, inside mint_occurrence).
            sess_key, occ_mutations = mint_occurrence(series_id, series_key, studio, studio_id, studio_locs,
                                                      instructor, instructor_id, name, capacity,
                                                      price_cents, resident_price_cents, occ_starts, occ_ends)
            session_keys.append(sess_key)
            mutations.extend(occ_mutations)

        if rolling:
            # The moving window: next* is the occurrence after the batch's
            # last on the cadence, and extendAt is the start of the window's
            # earliest class -- the first occurrence at creation. The
            # wellnessSeriesHorizon lens arms its deadline on extendAt, and
            # the recorded lapse dispatches ExtendSessionSeries for next*, so
            # occurrenceCount classes stay minted ahead of the earliest one.
            horizon = {
                "nextStartsAt": time.rfc3339_add(occ_starts, str(offset_hours_step) + "h"),
                "nextEndsAt": time.rfc3339_add(occ_ends, str(offset_hours_step) + "h"),
                "extendAt": starts_at,
                "mintedCount": occurrence_count,
            }
            if instructor != None:
                horizon["instructor"] = instructor
            mutations.append(make_aspect(series_key, "horizon", "sessionSeriesHorizon", horizon))

        # response permits ONLY primaryKey (InvalidReturnShape otherwise) — the
        # occurrenceCount session keys go on the event instead, mirroring how
        # every other multi-mutation op here surfaces only its primary key
        # and leaves detail to the projection lenses / event stream.
        events = [{"class": "wellness.sessionSeriesCreated",
                   "data": {"seriesKey": series_key, "studio": studio, "occurrenceCount": occurrence_count,
                             "sessionKeys": session_keys, "rolling": rolling}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": series_key}}

    if ot == "ExtendSessionSeries":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind
        # this op is operator/Scope:"any", wider than the one engine
        # (wellnessSeriesHorizon, targets.go) that ever dispatches it, and the
        # op mints a bookable class on a studio's grid off nothing but a
        # series key and two instants -- a wider submitter set would let any
        # operator-role holder put classes on a studio's grid outside the
        # desk's workplace confinement, which this op never runs. First
        # statement in the branch: it also denies every oracle beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: ExtendSessionSeries is restricted to Weaver's dispatch actor; got " + op.actor)

        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "sessionseries")
        if not vertex_alive(state, series_key):
            fail("UnknownSeries: " + series_key)
        cls = class_of(state, series_key)
        if cls != "sessionseries":
            fail("WrongClass: seriesKey: " + series_key + " has class " + str(cls) + ", required sessionseries")

        # The studio confirmation: the series' OWN atStudio link, walked --
        # the playbook declares the walk ({row.seriesKey atStudio out}) and
        # the row's studioKey is what the lens read off that same link, so a
        # mismatch means the row is stale (the series has no live studio) or
        # forged. Ordered before any read the studio would key.
        studio = required_string(p, "studio")
        _, studio_id = parts_of(studio, "studio", "studio")
        # read-posture: (e) relation=atStudio epoch=none -- CreateSessionSeries
        # writes exactly ONE atStudio link on a series and no op repoints it,
        # so a page of one is the whole set.
        page, _ = kv.Links(series_key, "atStudio", "out", None, 1)
        series_studio = None
        for lk in page:
            if not lk.isDeleted:
                series_studio = lk.targetVertex
        if series_studio != studio:
            fail("WrongStudio: studio " + studio + " is not the studio of series " + series_key)
        require_live_typed(state, studio, "studio", "studio")

        # read-posture: (a) declared reads at ExtendSessionSeries dispatch (the
        # playbook's Reads, targets.go); derive_reads derives the same key for
        # any other submitter.
        definition = kv.Read(series_key + ".definition")
        if definition == None or definition.isDeleted:
            fail("InvalidState: " + series_key + ".definition is missing; the series cannot be extended")
        # read-posture: (a) declared reads at ExtendSessionSeries dispatch (the
        # playbook's Reads, targets.go); derive_reads derives the same key for
        # any other submitter. A series without a horizon was never rolling;
        # one whose horizon has no extendAt was stopped (StopSessionSeries or the call-off).
        horizon = kv.Read(series_key + ".horizon")
        if horizon == None or horizon.isDeleted or horizon.data.get("extendAt") == None:
            fail("NotRolling: series " + series_key + " is not rolling; nothing to extend")

        # The row's pin: the payload names the occurrence the dispatching row
        # projected, and it must be the one the horizon records NOW. A row
        # that lagged an extension (or a move) names a class this horizon has
        # already minted or shifted past, and is refused rather than trusted
        # -- ReassignSessionSeries' AnchorMoved, on the horizon.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        instructor = optional_string(p, "instructor")
        if starts_at != horizon.data.get("nextStartsAt") or ends_at != horizon.data.get("nextEndsAt") or instructor != horizon.data.get("instructor"):
            fail("StaleHorizon: series " + series_key + " next mints " + str(horizon.data.get("nextStartsAt")) + " to " +
                 str(horizon.data.get("nextEndsAt")) + " led by " + str(horizon.data.get("instructor")) + "; got " +
                 starts_at + " to " + ends_at + " led by " + str(instructor))

        interval_days = definition.data.get("intervalDays")
        if type(interval_days) != type(0) or interval_days < 1:
            fail("InvalidState: " + series_key + ".definition.intervalDays is not a positive integer; got " + str(interval_days))
        step = str(interval_days * 24) + "h"

        # Mint-or-skip. The horizon moves either way -- a skipped occurrence
        # is the run passing that week, exactly as the desk would let it, and
        # the next class on the cadence is still owed. submittedAt is the
        # dispatch instant (the host clock is not exposed to Starlark): an
        # occurrence already in the past when the dispatch lands means the
        # stack was away past its start, and a class nobody could have
        # booked is not minted. A live cell on the occurrence's span means
        # the slot was booked one-off since the horizon was recorded.
        submitted = time.rfc3339_utc(op.submittedAt)
        # A retired instructor does not park the run. TombstoneInstructor has
        # no upcoming-classes guard, so the horizon can name an instructor who
        # is gone; the class is minted unled and the horizon drops them, so
        # the desk assigns a leader per class (ReassignSession) or stops the
        # run -- a refusal here would spend the gap's budget and leave the
        # schedule to run out, the exact outcome the horizon exists to prevent.
        # The instructor root is hydrated (derive_reads derives it off the
        # payload), so liveness is answered off state.
        led_by = instructor
        if led_by != None and not vertex_alive(state, led_by):
            led_by = None
        skipped = None
        if starts_at < submitted:
            skipped = "PastOccurrence"
        else:
            for c in slot_cells(starts_at, ends_at):
                cc = slot_cellcode(c)
                if cell_live(studio, cc):
                    skipped = "StudioConflict"
                    break
                if led_by != None and cell_live(led_by, cc):
                    skipped = "InstructorConflict"
                    break

        mutations = []
        sess_key = None
        if skipped == None:
            instructor_id = None
            if led_by != None:
                require_live_typed(state, led_by, "instructor", "instructor")
                _, instructor_id = parts_of(led_by, "instructor", "instructor")
            sess_key, mutations = mint_occurrence(series_id, series_key, studio, studio_id, studio_locations(studio),
                                                  led_by, instructor_id, definition.data.get("name"),
                                                  definition.data.get("capacity"), definition.data.get("priceCents"),
                                                  definition.data.get("residentPriceCents"), starts_at, ends_at)

        minted_count = horizon.data.get("mintedCount")
        if type(minted_count) != type(0):
            minted_count = 0
        if skipped == None:
            minted_count += 1
        new_horizon = {
            "nextStartsAt": time.rfc3339_add(starts_at, step),
            "nextEndsAt": time.rfc3339_add(ends_at, step),
            "extendAt": time.rfc3339_add(horizon.data.get("extendAt"), step),
            "mintedCount": minted_count,
        }
        if led_by != None:
            new_horizon["instructor"] = led_by
        # Bare: .horizon is hydrated (declared or derived), so the Processor
        # conditions this on the step-4 revision and retries in-process. The
        # write moves extendAt past the recorded lapse, which closes the gap
        # and re-arms freshUntil on the new deadline.
        mutations.append(make_aspect_upsert(series_key, "horizon", "sessionSeriesHorizon", new_horizon))

        event_data = {"seriesKey": series_key, "studio": studio, "startsAt": starts_at}
        if sess_key != None:
            event_data["sessionKey"] = sess_key
        if skipped != None:
            event_data["skipped"] = skipped
        if instructor != None and led_by == None:
            event_data["instructorDropped"] = instructor
        events = [{"class": "wellness.sessionSeriesExtended", "data": event_data}]
        # primaryKey is the series: this op writes its .horizon, so the key
        # lies within the write footprint the reply constraint admits.
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "TombstoneSession":
        sess_key = required_string(p, "sessionKey")
        _, sess_id = parts_of(sess_key, "sessionKey", "session")
        if not vertex_alive(state, sess_key):
            fail("UnknownSession: " + sess_key)
        cls = class_of(state, sess_key)
        if cls != "session":
            fail("WrongClass: sessionKey: " + sess_key + " has class " + str(cls) + ", required session")

        # Standing binder: operator passes unconditionally; a
        # bound instructor may additionally cancel only a class THEY lead —
        # the caller supplies the instructor param and BOTH the session's
        # ledBy link to it AND the caller's own identifiedBy binding to it
        # must be alive (known keys, mirroring clinic-domain's
        # actor_bound_to_provider two-hop shape). Absent an instructor param,
        # a front-of-house caller must worksAt a location the session's OWN
        # studio sits at, resolved off the atStudio link via
        # session_atstudio_link + this script's own studio_locations (never
        # the caller-supplied studio below) — this script carries no
        # session_locations helper (Starlark scripts share no globals across
        # DDLs), so the studio is resolved here the same way
        # ReassignSession's operator-repair path just below already does.
        # Falls back to the session's own atLocation snapshot
        # (CreateSession/CreateSessionSeries write one per location the
        # studio sat at, at mint time) when studio_locations comes back
        # empty — TombstoneStudio does not cascade onto atStudio, so a
        # decommissioned studio must not strand a staffer who still needs
        # to clear its now-orphaned classes off the grid (the exact reason
        # the booking script's own session_locations carries this same
        # fallback).
        if not actor_holds_operator(op.actor):
            instr_key = optional_string(p, "instructor")
            if instr_key != None:
                _, instr_id = parts_of(instr_key, "instructor", "instructor")
                _, actor_id = parts_of(op.actor, "actor", "identity")
                # The caller's own binding answers first. It is keyed on op.actor, so
                # it can only ever say "am I this instructor?" — whereas the ledBy
                # check below answers about the SESSION, and every bound instructor in
                # the deployment holds this grant. Ahead of the binding, one
                # instructor could walk a studio's published roster against a
                # stranger's session and read off who leads it, by which of the two
                # denials came back.
                # read-posture: (d) declared optionalReads by TombstoneSession's dispatcher.
                bound = kv.Read("lnk.instructor." + instr_id + ".identifiedBy.identity." + actor_id)
                if bound == None or bound.isDeleted:
                    fail("AuthDenied: " + op.actor + " is not identifiedBy-bound to instructor " + instr_key)
                # read-posture: (d) declared optionalReads by TombstoneSession's
                # dispatcher for the instructor-standing path (absence is a
                # meaningful AuthDenied, not a correctness error).
                led_by = kv.Read("lnk.session." + sess_id + ".ledBy.instructor." + instr_id)
                if led_by == None or led_by.isDeleted:
                    fail("AuthDenied: " + instr_key + " does not lead session " + sess_key)
            else:
                _, confine_studio = session_atstudio_link(sess_key)
                confine_locs = studio_locations(confine_studio)
                if not confine_locs:
                    aloc_cursor = None
                    for _page in range(MAX_ATLOCATION_PAGES):
                        # read-posture: (e) relation=atLocation epoch=none --
                        # a session carries at most a handful of atLocation
                        # snapshot links, bounded, never a keyspace scan.
                        aloc_page, aloc_cursor = kv.Links(sess_key, "atLocation", "out", aloc_cursor, ATLOCATION_PAGE_LIMIT)
                        for lk in aloc_page:
                            if not lk.isDeleted:
                                confine_locs.append(lk.targetVertex)
                        if aloc_cursor == None:
                            break
                enforce_workplace(confine_locs, "cannot cancel session " + sess_key)

        # The session's studio, needed below to release its held cells. Verified
        # as genuinely THIS session's studio, and ordered after the binder: the
        # check answers differently for the real studio than any other, so ahead
        # of the guard it would tell any caller holding a TombstoneSession grant
        # where a class they have no part in is held.
        studio = required_string(p, "studio")
        require_matching_studio(sess_id, studio)

        # read-posture: (a) declared reads at TombstoneSession dispatch —
        # required for cell release.
        sched = kv.Read(sess_key + ".schedule")

        # A session that has already started is a record, not a booking --
        # TombstoneSessionSeries skips exactly these occurrences for the same
        # reason (its occurrence walk, this file): tombstoning a class that ran hands every
        # one of its bookings to ReleaseOrphanedBooking, which drains the seat
        # and refunds a class price for a class that actually happened. Same
        # inequality (and same at-the-boundary reading) as CancelBooking's own
        # SessionStarted guard: starting exactly at submittedAt counts as
        # started.
        if sched == None or sched.isDeleted:
            fail("InvalidState: " + sess_key + ".schedule is missing; cannot cancel")
        starts_at = sched.data.get("startsAt")
        if starts_at == None:
            fail("InvalidState: " + sess_key + ".schedule.startsAt is missing; cannot cancel")
        submitted = time.rfc3339_utc(op.submittedAt)
        if submitted >= starts_at:
            fail("SessionStarted: session " + sess_key + " started at " + str(starts_at) + ", cannot cancel a class once it has begun (submitted " + submitted + ")")

        mutations = [make_tombstone(sess_key)]
        mutations.extend(release_cells_mutations(studio, sched))
        # The session's CURRENT instructor (regardless of which standing path
        # authorized this call), read fresh here rather than trusting the
        # caller-supplied instructor param used only above for the
        # instructor-standing auth branch -- an operator call carries no such
        # param at all. Releases that instructor's instructorSlotClaim cells
        # too, the same providerSlotClaim-mirror lock CreateSession claimed.
        _, cur_instructor, _ = session_ledby_link(sess_key)
        if cur_instructor != None:
            mutations.extend(release_cells_mutations(cur_instructor, sched))
        events = [{"class": "wellness.sessionCancelled", "data": {"sessionKey": sess_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": sess_key}}

    if ot == "TombstoneSessionSeries":
        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "sessionseries")
        if not vertex_alive(state, series_key):
            fail("UnknownSessionSeries: " + series_key)
        cls = class_of(state, series_key)
        if cls != "sessionseries":
            fail("WrongClass: seriesKey: " + series_key + " has class " + str(cls) + ", required sessionseries")

        # Standing: the SAME workplace confinement CreateSessionSeries applies
        # to the same studio -- calling a recurring class off a studio's grid
        # is the same front-desk beat as putting it there, so it is bound the
        # same way. There is deliberately no instructor-standing path (the
        # branch TombstoneSession carries): an instructor cancels the one
        # class they lead, never a studio's whole standing booking, and no
        # provider-role grant reaches this op (permissions.go).
        #
        # The walk runs off the CALLER-SUPPLIED studio rather than one resolved
        # from the series (TombstoneSession's shape), and that is sound here
        # only because the confirmation below then requires that same studio to
        # BE the series': the conjunction is "works at S" and "S is the series'
        # studio", which is exactly "works at the series' studio". A studio
        # TombstoneStudio has since retired resolves no locations, so a
        # non-operator is denied it -- the same fail-closed posture
        # CreateSessionSeries already has for that studio, and an operator
        # remains able to clear the stranded run.
        studio = required_string(p, "studio")
        # workplace-exempt: (no-validated-path) TombstoneSessionSeries is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot call off a session series at studio " + studio)

        # The studio confirmation param, verified against the SERIES' own
        # atStudio link -- require_matching_studio's shape one vertex type
        # over (CreateSessionSeries writes exactly one atStudio link on the
        # series). Ordered AFTER the standing binder for the reason
        # TombstoneSession orders its own that way: the check answers
        # differently for the real studio than for any other, so ahead of the
        # guard it would tell any caller holding the grant where a series they
        # have no part in runs.
        _, studio_id = parts_of(studio, "studio", "studio")
        # read-posture: (d) declared optionalReads at TombstoneSessionSeries
        # dispatch (validation link; absence means the caller named the wrong
        # studio -- WrongStudio).
        series_at_studio = kv.Read("lnk.sessionseries." + series_id + ".atStudio.studio." + studio_id)
        if series_at_studio == None or series_at_studio.isDeleted:
            fail("WrongStudio: studio " + studio + " is not the studio of series " + series_key)

        # Canonical-UTC RFC3339 compares lexically == chronologically, and
        # startsAt is already stored canonical (sessionSchedule), so this one
        # normalization is all the "now" the loop below needs -- the same soft,
        # caller-supplied submittedAt guard CreateBooking's SessionInPast uses
        # (the host clock is not exposed to Starlark).
        submitted = time.rfc3339_utc(op.submittedAt)

        mutations = []
        cancelled_keys = []
        seen = {}
        cursor = None
        for _page in range(SERIES_OCCURRENCE_MAX_PAGES):
            # read-posture: (e) relation=partOf epoch=none -- the series' own
            # occurrence set, bounded by CreateSessionSeries's occurrenceCount
            # ceiling and by SERIES_OCCURRENCE_PAGE_LIMIT above; never a
            # keyspace scan. The occurrences are not declarable: a caller holds
            # only the series key, and which sessions hang off it is exactly
            # what this walk is for.
            links, cursor = kv.Links(series_key, "partOf", "in", cursor, SERIES_OCCURRENCE_PAGE_LIMIT)
            for lk in links:
                if lk.isDeleted:
                    continue
                sess_key = lk.sourceVertex
                # A link may be delivered on more than one page (the pages are
                # a cursor over a live keyspace, not a snapshot), and one
                # occurrence tombstoned twice in a single batch is a duplicate
                # mutation -- the same dedup collect_waitlist_candidates keeps.
                if sess_key in seen:
                    continue
                seen[sess_key] = True
                # The schedule FIRST, before the liveness and studio reads: a
                # rolling run's partOf history grows by one occurrence per
                # extension and is never pruned, so most of what this walk
                # meets on a long-lived run is history, and history must cost
                # one read per occurrence, not three, to stay inside the
                # Starlark wall. TombstoneSession never cascades onto
                # .schedule, so a cancelled occurrence still answers here and
                # is skipped by its liveness read below like any other.
                # read-posture: (e) per-occurrence follow-up read off the
                # enumeration above (data-derived key -- the occurrence is
                # unknown until it resolves from the link).
                sched = kv.Read(sess_key + ".schedule")
                if sched == None or sched.isDeleted:
                    continue
                starts_at = sched.data.get("startsAt")
                if starts_at == None:
                    continue
                # An occurrence that has already started is HISTORY, not part
                # of "the rest of the series": its seats were taken and sat in,
                # and tombstoning it would hand every one of those bookings to
                # ReleaseOrphanedBooking, which drains the seat and refunds the
                # class price for a class that actually ran. Same inequality
                # (and same at-the-boundary reading) as CreateBooking's
                # SessionInPast: starting exactly at submittedAt counts as
                # started.
                if not (submitted < starts_at):
                    continue
                # Already-cancelled occurrences released their cells when
                # TombstoneSession ran. A direct read rather than
                # vertex_live(sess_key), because the tombstone below needs the
                # revision this read observes to pin its own CAS --
                # vertex_live's internal read does not expose one.
                # read-posture: (e) per-occurrence follow-up read, same walk.
                sess_doc = kv.Read(sess_key)
                if sess_doc == None or sess_doc.isDeleted:
                    continue
                _, occ_id = parts_of(sess_key, "occurrence", "session")
                # This occurrence's OWN atStudio link to the CONFIRMED studio,
                # the same deterministic key require_matching_studio reads. An
                # occurrence ReassignSession has since moved to a DIFFERENT
                # studio has this link tombstoned and is passed over: the
                # standing cleared above is one studio's, and releasing cells
                # on a hub this call never confirmed would tombstone whatever
                # OTHER session now holds them. Cancel a moved occurrence with
                # TombstoneSession, which confirms its own studio.
                # read-posture: (e) per-occurrence follow-up read, same walk.
                occ_at_studio = kv.Read("lnk.session." + occ_id + ".atStudio.studio." + studio_id)
                if occ_at_studio == None or occ_at_studio.isDeleted:
                    continue
                mutations.append(make_tombstone_occ(sess_key, sess_doc.revision))
                mutations.extend(release_cells_mutations(studio, sched))
                # The occurrence's CURRENT instructor, read fresh per
                # occurrence rather than off the series: ReassignSession subs
                # one class of a run without touching its siblings, so the
                # series' original instructor is not who holds these cells.
                _, cur_instructor, _ = session_ledby_link(sess_key)
                if cur_instructor != None:
                    mutations.extend(release_cells_mutations(cur_instructor, sched))
                cancelled_keys.append(sess_key)
            if cursor == None:
                break
        # The page budget strictly exceeds the largest occurrence set
        # CreateSessionSeries can mint, so a cursor still open here means
        # some other writer has hung occurrences off this series -- refuse
        # rather than report a partial call-off as success (no reply channel
        # carries the cancelled set back; PromoteWaitlistedBookings's
        # WaitlistWalkBound is the house shape).
        if cursor != None:
            fail("SeriesWalkBound: series " + series_key + " has more partOf occurrences than " +
                 str(SERIES_OCCURRENCE_MAX_PAGES * SERIES_OCCURRENCE_PAGE_LIMIT) + "; nothing cancelled")

        # A rolling series stops rolling: .horizon is rewritten without
        # extendAt (stoppedAt recorded, next*/instructor/mintedCount carried),
        # so wellnessSeriesHorizon arms nothing further and no next window is
        # minted -- the stop IS the work on a rolling run, so zero eligible
        # occurrences is not a refusal for one. Read hydrated: derive_reads
        # derives the key off the payload's seriesKey for every dispatcher,
        # and the write is bare on that hydrated revision.
        # read-posture: (d) optionalReads -- derived server-side by this
        # script's own derive_reads(op) for TombstoneSessionSeries; absent on
        # a series that was never rolling.
        horizon = kv.Read(series_key + ".horizon")
        stopped_rolling = False
        if horizon != None and not horizon.isDeleted and horizon.data.get("extendAt") != None:
            stopped = {
                "nextStartsAt": horizon.data.get("nextStartsAt"),
                "nextEndsAt": horizon.data.get("nextEndsAt"),
                "mintedCount": horizon.data.get("mintedCount"),
                "stoppedAt": submitted,
            }
            if horizon.data.get("instructor") != None:
                stopped["instructor"] = horizon.data.get("instructor")
            mutations.append(make_aspect_upsert(series_key, "horizon", "sessionSeriesHorizon", stopped))
            stopped_rolling = True

        if len(cancelled_keys) == 0 and not stopped_rolling:
            fail("NoUpcomingOccurrences: series " + series_key + " has no live occurrence at studio " + studio +
                 " starting after " + submitted)

        # The series VERTEX is deliberately left alive: its already-run
        # occurrences stay partOf it, and that parentage is the only record
        # that they were one recurring class rather than a pile of one-offs.
        # Nothing reads a stopped or non-rolling series for schedulability --
        # its occurrences were minted eagerly (sessionSeriesVertexTypeDDL) --
        # so a live series with no future occurrence left is inert, not
        # stale.
        # NO primaryKey. The reply constraint (Contract #3) admits only a key
        # this op actually WROTE -- the write path is not a read channel --
        # and the occurrences it cancels are what the caller asked about, not
        # the series (whose .horizon a rolling stop rewrites, but which the
        # caller already holds). Which occurrences were cancelled rides the
        # event, the same place CreateSessionSeries puts its own minted keys.
        events = [{"class": "wellness.sessionSeriesCancelled",
                   "data": {"seriesKey": series_key, "studio": studio, "sessionKeys": cancelled_keys,
                            "stoppedRolling": stopped_rolling}}]
        return {"mutations": mutations, "events": events}

    if ot == "StopSessionSeries":
        # The rolling run's off switch, and the ONLY one that never walks:
        # TombstoneSessionSeries stops the roll too, but only when its partOf
        # walk succeeds, and a long-lived rolling run's history outgrows that
        # walk (SERIES_OCCURRENCE_PAGE_LIMIT). This closes the horizon alone --
        # nothing on the grid is touched; what remains is cancelled or moved
        # per class with TombstoneSession / ReassignSession -- off two
        # hydrated keys and one page of one link, whatever the run's history.
        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "sessionseries")
        if not vertex_alive(state, series_key):
            fail("UnknownSessionSeries: " + series_key)
        cls = class_of(state, series_key)
        if cls != "sessionseries":
            fail("WrongClass: seriesKey: " + series_key + " has class " + str(cls) + ", required sessionseries")

        # Standing: TombstoneSessionSeries's binder verbatim -- the workplace
        # confinement CreateSessionSeries applies to the same studio, off the
        # CALLER-SUPPLIED studio, sound only because the confirmation just
        # below then requires that studio to BE the series' (see the
        # TombstoneSessionSeries arm for the conjunction). No instructor path.
        studio = required_string(p, "studio")
        # workplace-exempt: (no-validated-path) StopSessionSeries is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot stop a session series at studio " + studio)

        # The studio confirmation against the SERIES' own atStudio link,
        # ordered after the binder for the reason TombstoneSessionSeries gives.
        _, studio_id = parts_of(studio, "studio", "studio")
        # read-posture: (d) optionalReads -- derived server-side by this
        # script's own derive_reads(op) for StopSessionSeries, and declared by
        # the op-meta's dispatch (validation link; absence means the caller
        # named the wrong studio -- WrongStudio).
        series_at_studio = kv.Read("lnk.sessionseries." + series_id + ".atStudio.studio." + studio_id)
        if series_at_studio == None or series_at_studio.isDeleted:
            fail("WrongStudio: studio " + studio + " is not the studio of series " + series_key)

        # read-posture: (d) optionalReads -- derived server-side by this
        # script's own derive_reads(op) for StopSessionSeries; absent on a
        # series that was never rolling, and without extendAt on one already
        # stopped -- both NotRolling, there is nothing to switch off.
        horizon = kv.Read(series_key + ".horizon")
        if horizon == None or horizon.isDeleted or horizon.data.get("extendAt") == None:
            fail("NotRolling: series " + series_key + " is not rolling; nothing to stop")

        submitted = time.rfc3339_utc(op.submittedAt)
        stopped = {
            "nextStartsAt": horizon.data.get("nextStartsAt"),
            "nextEndsAt": horizon.data.get("nextEndsAt"),
            "mintedCount": horizon.data.get("mintedCount"),
            "stoppedAt": submitted,
        }
        if horizon.data.get("instructor") != None:
            stopped["instructor"] = horizon.data.get("instructor")
        # Bare on the hydrated revision, TombstoneSessionSeries's own stop
        # write: extendAt dropped, so the lens arms nothing further.
        mutations = [make_aspect_upsert(series_key, "horizon", "sessionSeriesHorizon", stopped)]
        events = [{"class": "wellness.sessionSeriesStopped", "data": {"seriesKey": series_key, "studio": studio}}]
        # primaryKey is the series: the op writes its .horizon, so the key
        # lies within the write footprint the reply constraint admits.
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "ReassignSessionSeries":
        # The whole-run counterpart of ReassignSession's time move, the way
        # TombstoneSessionSeries is TombstoneSession's: one submission carries
        # every still-upcoming occurrence at the confirmed studio by the SAME
        # delta -- startsAt/endsAt name the new instants of the ANCHOR, the
        # earliest still-upcoming occurrence there, pinned by the caller as
        # anchorKey + anchorStartsAt -- and every sibling moves by
        # shift = startsAt - anchorStartsAt onto a span of the anchor's new
        # length. Same studio, each occurrence keeps its own instructor, its
        # bookings ride with it (the seat aspects hang off the session vertex,
        # which is not touched), and the series' own .definition stays the
        # minted fact -- firstStartsAt/firstEndsAt record what was authored,
        # not where the run now stands; the occurrences' .schedule aspects are
        # the schedule of record, exactly as they are after ReassignSession
        # moves one of them.
        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "sessionseries")
        if not vertex_alive(state, series_key):
            fail("UnknownSessionSeries: " + series_key)
        cls = class_of(state, series_key)
        if cls != "sessionseries":
            fail("WrongClass: seriesKey: " + series_key + " has class " + str(cls) + ", required sessionseries")

        # Standing: TombstoneSessionSeries's binder verbatim -- the workplace
        # confinement CreateSessionSeries applies to the same studio, off the
        # CALLER-SUPPLIED studio, sound only because the confirmation just
        # below then requires that studio to BE the series' (see the
        # TombstoneSessionSeries arm for the conjunction). No instructor path:
        # an instructor reschedules the one class they lead (ReassignSession),
        # never a studio's whole standing booking.
        studio = required_string(p, "studio")
        # workplace-exempt: (no-validated-path) ReassignSessionSeries is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no task
        # mints it, so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot move a session series at studio " + studio)

        # The studio confirmation param against the SERIES' own atStudio link,
        # ordered after the binder for the reason TombstoneSessionSeries gives.
        _, studio_id = parts_of(studio, "studio", "studio")
        # read-posture: (d) declared optionalReads at ReassignSessionSeries
        # dispatch (validation link; absence means the caller named the wrong
        # studio -- WrongStudio).
        series_at_studio = kv.Read("lnk.sessionseries." + series_id + ".atStudio.studio." + studio_id)
        if series_at_studio == None or series_at_studio.isDeleted:
            fail("WrongStudio: studio " + studio + " is not the studio of series " + series_key)

        # The anchor is PINNED by the caller, never inferred from the walk:
        # anchorKey is the occurrence the desk saw as the next class and
        # anchorStartsAt is its start as the desk saw it. Inferred from the
        # walk alone, the anchor would drift silently -- a roster opened at
        # 17:55 for a run whose next class is at 18:00 and submitted at 18:02
        # would find today's class started, take NEXT week's as the anchor and
        # carry the whole run six days EARLIER; a concurrent ReassignSession of
        # the anchor would do the same through the OCC re-execution on fresh
        # state. Both pins are (a)-declared reads at dispatch, so the
        # re-execution sees the anchor's fresh schedule and the comparison
        # after the walk refuses AnchorMoved instead. Liveness is deliberately
        # not checked here: a cancelled anchor is never the walk's earliest
        # eligible occurrence, and "the next class is no longer X" is the
        # honest answer for it too.
        anchor_key = required_string(p, "anchorKey")
        parts_of(anchor_key, "anchorKey", "session")
        anchor_cls = class_of(state, anchor_key)
        if anchor_cls != None and anchor_cls != "session":
            fail("WrongClass: anchorKey: " + anchor_key + " has class " + str(anchor_cls) + ", required session")
        anchor_starts_at = time.rfc3339_utc(required_string(p, "anchorStartsAt"))

        # The anchor's new span: ReassignSession's own reschedule validation
        # (canonical UTC, strictly ordered, on the 15-minute grid, at most 24h
        # -- slot_cells raises SessionTooLong past MAX_SLOT_CELLS), plus a
        # future guard the single-class ops leave to the caller: a run moved
        # to a next class that has already begun is a mistyped date, and
        # CreateBooking's SessionInPast is the house word for it. The span's
        # length is what every moved occurrence takes on, whatever length it
        # had.
        new_starts = time.rfc3339_utc(required_string(p, "startsAt"))
        new_ends = time.rfc3339_utc(required_string(p, "endsAt"))
        if not (new_starts < new_ends):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + new_starts + " endsAt=" + new_ends)
        enforce_grid(new_starts, new_ends)
        slot_cells(new_starts, new_ends)
        submitted = time.rfc3339_utc(op.submittedAt)
        if not (submitted < new_starts):
            fail("SessionInPast: the next occurrence would start at " + new_starts + ", not in the future (submitted " + submitted + ")")
        duration_seconds = instant_seconds(new_ends) - instant_seconds(new_starts)

        # The walk: TombstoneSessionSeries's occurrence filter verbatim -- live,
        # still at the confirmed studio, carrying a schedule, and starting
        # after submittedAt -- collected rather than mutated, because the
        # anchor pin is checked against the whole eligible set (the walk
        # returns occurrences in key order, not cadence order) and the
        # mutation count is refused as a whole.
        eligible = []
        seen = {}
        cursor = None
        for _page in range(SERIES_OCCURRENCE_MAX_PAGES):
            # read-posture: (e) relation=partOf epoch=none -- the series' own
            # occurrence set, bounded by CreateSessionSeries's occurrenceCount
            # ceiling and by SERIES_OCCURRENCE_PAGE_LIMIT above; never a
            # keyspace scan. The occurrences are not declarable: a caller holds
            # only the series key, and which sessions hang off it is exactly
            # what this walk is for.
            links, cursor = kv.Links(series_key, "partOf", "in", cursor, SERIES_OCCURRENCE_PAGE_LIMIT)
            for lk in links:
                if lk.isDeleted:
                    continue
                sess_key = lk.sourceVertex
                # A link may be delivered on more than one page (the pages are
                # a cursor over a live keyspace, not a snapshot), and one
                # occurrence moved twice in a single batch is a duplicate
                # mutation -- the same dedup TombstoneSessionSeries keeps.
                if sess_key in seen:
                    continue
                seen[sess_key] = True
                # The schedule FIRST, for TombstoneSessionSeries' reason: a
                # rolling run's history is most of what this walk meets, and
                # it must cost one read per occurrence to stay inside the
                # Starlark wall.
                # read-posture: (e) per-occurrence follow-up read off the
                # enumeration above (data-derived key -- the occurrence is
                # unknown until it resolves from the link).
                sched = kv.Read(sess_key + ".schedule")
                if sched == None or sched.isDeleted:
                    continue
                starts_at = sched.data.get("startsAt")
                ends_at = sched.data.get("endsAt")
                if starts_at == None or ends_at == None:
                    continue
                # An occurrence that has already started is HISTORY: its seats
                # were sat in, and moving it would rewrite a class that ran.
                # Same inequality (and same at-the-boundary reading) as
                # CreateBooking's SessionInPast: starting exactly at
                # submittedAt counts as started.
                if not (submitted < starts_at):
                    continue
                # A cancelled occurrence holds no cells and has nothing to
                # move.
                # read-posture: (e) per-occurrence follow-up read, same walk.
                if not vertex_live(sess_key):
                    continue
                _, occ_id = parts_of(sess_key, "occurrence", "session")
                # This occurrence's OWN atStudio link to the CONFIRMED studio.
                # An occurrence ReassignSession has since moved to a DIFFERENT
                # studio has this link tombstoned and is passed over: its cells
                # sit on a hub this call never confirmed, and releasing them
                # there would tombstone whatever OTHER session now holds them.
                # read-posture: (e) per-occurrence follow-up read, same walk.
                occ_at_studio = kv.Read("lnk.session." + occ_id + ".atStudio.studio." + studio_id)
                if occ_at_studio == None or occ_at_studio.isDeleted:
                    continue
                eligible.append({"key": sess_key, "sched": sched, "startsAt": starts_at, "endsAt": ends_at})
            if cursor == None:
                break
        # The page budget strictly exceeds the largest occurrence set
        # CreateSessionSeries can mint, so a cursor still open here means some
        # other writer has hung occurrences off this series -- refuse rather
        # than move part of a run (TombstoneSessionSeries's SeriesWalkBound).
        if cursor != None:
            fail("SeriesWalkBound: series " + series_key + " has more partOf occurrences than " +
                 str(SERIES_OCCURRENCE_MAX_PAGES * SERIES_OCCURRENCE_PAGE_LIMIT) + "; nothing moved")
        if len(eligible) == 0:
            fail("NoUpcomingOccurrences: series " + series_key + " has no live occurrence at studio " + studio +
                 " starting after " + submitted)

        # The pin: the walk's earliest still-upcoming occurrence at this
        # studio (canonical UTC compares lexically == chronologically) must BE
        # the anchor the desk named, at the start the desk saw. Anything else
        # -- the anchor started or was cancelled since the roster loaded, or
        # ReassignSession moved it -- means the shift the desk intended is not
        # the shift this run would take, and the honest answer is to reload.
        earliest = eligible[0]
        for occ in eligible:
            if occ["startsAt"] < earliest["startsAt"]:
                earliest = occ
        if earliest["key"] != anchor_key or earliest["startsAt"] != anchor_starts_at:
            fail("AnchorMoved: the next class is no longer " + anchor_starts_at + " at " + anchor_key +
                 "; reload the roster and try again")

        # The payload's startsAt names where the anchor lands, and every
        # sibling follows by the same delta. Both instants sit on the
        # 15-minute grid, so the shift is a whole number of grid steps and
        # every moved span stays aligned without a per-occurrence enforce_grid.
        shift_seconds = instant_seconds(new_starts) - instant_seconds(anchor_starts_at)
        if shift_seconds > SERIES_MAX_SHIFT_SECONDS or -shift_seconds > SERIES_MAX_SHIFT_SECONDS:
            fail("InvalidArgument: startsAt: moves the next occurrence (" + anchor_starts_at + ") by more than " +
                 str(SERIES_MAX_SHIFT_SECONDS // 86400) + " days; got " + new_starts)
        # A move that changes nothing is a refusal, as ReassignSession's
        # no-edit call is: the anchor's span is exactly what it already holds,
        # so nothing would move and every sibling would be re-written to the
        # values it has.
        if shift_seconds == 0 and duration_seconds == instant_seconds(earliest["endsAt"]) - instant_seconds(anchor_starts_at):
            fail("InvalidArgument: nothing moves: the next class already runs " + anchor_starts_at + " to " +
                 earliest["endsAt"] + "; at least one of startsAt or endsAt must change")
        shift_dur = str(shift_seconds) + "s"
        span_dur = str(duration_seconds) + "s"

        # Cells move as ONE batch per hub, not one occurrence at a time. Across
        # every occurrence being moved: the union of the cells the run holds
        # today, the union of the cells it will hold, and only the DIFFERENCE
        # is written -- tombstone (old - new), claim (new - old). This is
        # ReassignSession's same-hub reschedule delta generalized from one
        # occurrence to N, and it is load-bearing rather than tidy: a shift
        # equal to the series' own interval ("every class one week later")
        # lands occurrence i on exactly the cells occurrence i+1 vacates in
        # this same op, and claim_cell reads LIVE KV -- this batch's own
        # tombstones are not applied to the state it sees -- so claimed per
        # occurrence it would find those cells still held by the sibling and
        # refuse StudioConflict for a move that collides with nothing. The
        # instructor hubs get the identical treatment, grouped by whoever leads
        # each occurrence TODAY (session_ledby_link, read fresh per occurrence
        # as TombstoneSessionSeries does: ReassignSession subs one class of a
        # run without touching its siblings, so the series' original
        # instructor is not who holds these cells); an occurrence with no
        # instructor has no instructor cells on either side.
        #
        # Each "new" set remembers which occurrence first wanted the cell, so
        # a collision names the class that collided -- the desk sees WHICH
        # week hit another booking, not just that one did -- and a cell two
        # occurrences of this run would BOTH need after the move (possible only
        # when ReassignSession has since pulled a sibling off the cadence) is
        # refused outright rather than claimed once for two classes.
        old_studio_cells = {}
        new_studio_cells = {}
        old_instr_cells = {}
        new_instr_cells = {}
        moved = []
        for occ in eligible:
            sess_key = occ["key"]
            sched = occ["sched"]
            occ_new_starts = time.rfc3339_add(occ["startsAt"], shift_dur)
            occ_new_ends = time.rfc3339_add(occ_new_starts, span_dur)
            occ_old = slot_cells(occ["startsAt"], occ["endsAt"])
            occ_new = slot_cells(occ_new_starts, occ_new_ends)
            _, instructor, _ = session_ledby_link(sess_key)
            if instructor != None:
                if instructor not in old_instr_cells:
                    old_instr_cells[instructor] = {}
                    new_instr_cells[instructor] = {}
            for c in occ_old:
                old_studio_cells[c] = occ["startsAt"]
                if instructor != None:
                    old_instr_cells[instructor][c] = occ["startsAt"]
            for c in occ_new:
                if c in new_studio_cells:
                    fail("StudioConflict: the classes of " + new_studio_cells[c] + " and " + occ["startsAt"] +
                         " would both need studio " + studio + " slot " + slot_cellcode(c) + " after the move")
                new_studio_cells[c] = occ["startsAt"]
                if instructor != None:
                    if c in new_instr_cells[instructor]:
                        fail("InstructorConflict: the classes of " + new_instr_cells[instructor][c] + " and " + occ["startsAt"] +
                             " would both need instructor " + instructor + " slot " + slot_cellcode(c) + " after the move")
                    new_instr_cells[instructor][c] = occ["startsAt"]
            # remindAt re-derived from the new start, exactly as ReassignSession
            # does; name/capacity/price carried forward unchanged, a missing
            # price field staying missing rather than arriving as null.
            new_sched = {
                "name": sched.data.get("name"),
                "startsAt": occ_new_starts, "endsAt": occ_new_ends,
                "capacity": sched.data.get("capacity"),
                "remindAt": time.rfc3339_add(occ_new_starts, "-24h"),
            }
            if sched.data.get("priceCents") != None:
                new_sched["priceCents"] = sched.data.get("priceCents")
            if sched.data.get("residentPriceCents") != None:
                new_sched["residentPriceCents"] = sched.data.get("residentPriceCents")
            moved.append({"key": sess_key, "startsAt": occ["startsAt"], "sched": new_sched, "revision": sched.revision})

        mutations = []
        for c in old_studio_cells:
            if c not in new_studio_cells:
                mutations.append(make_tombstone(studio + ".slot" + slot_cellcode(c)))
        # claim_cell's read on each cell the run does not already hold is a
        # class-(e) follow-up of the partOf walk: which cells these are is a
        # function of each walked occurrence's schedule, which no caller
        # holding a series key can name up front (the same reason
        # derive_reads covers neither this op nor ReassignSession).
        for c in new_studio_cells:
            if c not in old_studio_cells:
                mutations.append(claim_cell(studio, slot_cellcode(c), "studioSlotClaim", "StudioConflict",
                                            "moving the class of " + new_studio_cells[c] + ": studio"))
        for instructor in old_instr_cells:
            for c in old_instr_cells[instructor]:
                if c not in new_instr_cells[instructor]:
                    mutations.append(make_tombstone(instructor + ".slot" + slot_cellcode(c)))
            for c in new_instr_cells[instructor]:
                if c not in old_instr_cells[instructor]:
                    mutations.append(claim_cell(instructor, slot_cellcode(c), "instructorSlotClaim", "InstructorConflict",
                                                "moving the class of " + new_instr_cells[instructor][c] + ": instructor"))
        moved_keys = []
        for mv in moved:
            # OCC on the revision this walk itself read -- the guard rests on
            # this script's own follow-up read, never on a caller declaration.
            mutations.append(make_aspect_upsert_occ(mv["key"], "schedule", "sessionSchedule", mv["sched"], mv["revision"]))
            moved_keys.append(mv["key"])

        # A rolling series' horizon moves with its classes: nextStartsAt and
        # extendAt shift by the same delta, and nextEndsAt takes on the span
        # every moved occurrence takes on, in the same batch, so the window
        # the platform mints next stays on the moved cadence. A BACKWARD move
        # can land the shifted extendAt at or before the lapse the fired
        # timer already recorded on the series: the gap then opens at once,
        # with no timer, and the platform mints one slot per dispatch until
        # the horizon stands occurrenceCount slots ahead of the moved cadence
        # -- accepted, not clamped. The window counts cadence slots, not live
        # classes (a skipped slot is still a slot), and an earlier run has
        # exactly that on its books. Read hydrated
        # (derive_reads derives the key off the payload's seriesKey for every
        # dispatcher); the write is bare on that hydrated revision. Counted
        # toward the batch ceiling below with everything else assembled.
        # read-posture: (d) optionalReads -- derived server-side by this
        # script's own derive_reads(op) for ReassignSessionSeries; absent on a
        # series that was never rolling.
        horizon = kv.Read(series_key + ".horizon")
        if horizon != None and not horizon.isDeleted and horizon.data.get("extendAt") != None:
            next_starts = time.rfc3339_add(horizon.data.get("nextStartsAt"), shift_dur)
            shifted = {
                "nextStartsAt": next_starts,
                "nextEndsAt": time.rfc3339_add(next_starts, span_dur),
                "extendAt": time.rfc3339_add(horizon.data.get("extendAt"), shift_dur),
                "mintedCount": horizon.data.get("mintedCount"),
            }
            if horizon.data.get("instructor") != None:
                shifted["instructor"] = horizon.data.get("instructor")
            mutations.append(make_aspect_upsert(series_key, "horizon", "sessionSeriesHorizon", shifted))

        # The batch ceiling, checked on what was actually assembled rather
        # than on the run's shape: a shift onto the run's own cells writes only
        # the difference, so the same 52-occurrence run fits or does not by
        # where it is going, not by how long it is (SERIES_MOVE_MAX_MUTATIONS).
        if len(mutations) > SERIES_MOVE_MAX_MUTATIONS:
            fail("SeriesTooLarge: moving " + str(len(moved_keys)) + " occurrences takes " + str(len(mutations)) +
                 " mutations, more than the " + str(SERIES_MOVE_MAX_MUTATIONS) +
                 " one operation can commit; move the run in two halves (cancel the later half with " +
                 "TombstoneSessionSeries and schedule it again with CreateSessionSeries), or shift it onto " +
                 "cells it already holds")

        # NO primaryKey, for TombstoneSessionSeries's reason: the occurrences
        # this op moves are its subject, and every mutation for them roots at
        # an occurrence or a slot hub; the .horizon shift above is a side
        # effect on a key the caller already holds, never the reply's
        # subject. Which occurrences moved, and by how much, rides the event.
        events = [{"class": "wellness.sessionSeriesMoved",
                   "data": {"seriesKey": series_key, "studio": studio, "sessionKeys": moved_keys,
                            "shiftSeconds": shift_seconds}}]
        return {"mutations": mutations, "events": events}

    if ot == "ReassignSession":
        sess_key = required_string(p, "sessionKey")
        _, sess_id = parts_of(sess_key, "sessionKey", "session")
        if not vertex_alive(state, sess_key):
            fail("UnknownSession: " + sess_key)
        cls = class_of(state, sess_key)
        if cls != "session":
            fail("WrongClass: sessionKey: " + sess_key + " has class " + str(cls) + ", required session")

        new_studio = optional_string(p, "newStudio")
        studio = optional_string(p, "studio")
        if studio != None:
            require_matching_studio(sess_id, studio)
        else:
            # studio is omittable ONLY for an operator repairing a session
            # whose studio TombstoneStudio already removed (verticals.md
            # "retiring a studio strands its classes") — wellnessSessionsSpec's
            # OPTIONAL MATCH on a live :studio (lenses.go) can no longer hand
            # the FE a key to round-trip once the vertex is tombstoned, so
            # requiring it unconditionally would make this repair path
            # unreachable by construction. session_atstudio_link resolves the
            # CURRENT studio server-side instead, off the untouched link.
            if new_studio == None:
                fail("InvalidArgument: studio is required unless newStudio is also supplied by an operator")
            if not actor_holds_operator(op.actor):
                fail("AuthDenied: only an operator may reassign a session's studio without confirming the current one")
            _, studio = session_atstudio_link(sess_key)
            if studio == None:
                fail("InvalidState: session " + sess_key + " carries no atStudio link to replace")

        studio_changed = new_studio != None and new_studio != studio
        if studio_changed and not actor_holds_operator(op.actor):
            fail("AuthDenied: only an operator may move a session to a different studio")
        if new_studio != None:
            # (a)-declared required read — require_live_typed validates it
            # alive + class=studio before the swap (mirrors newInstructor's
            # convention just below in this same function).
            require_live_typed(state, new_studio, "newStudio", "studio")

        # Standing binder — the union of CreateSession's and TombstoneSession's:
        # operator passes unconditionally; a bound instructor may reassign or
        # reschedule only a class THEY currently lead (same ledBy+identifiedBy
        # shape TombstoneSession uses); absent that, a front-of-house caller
        # must worksAt a location covering the studio (same enforce_workplace
        # shape CreateSession's staff path uses).
        if not actor_holds_operator(op.actor):
            instr_key = optional_string(p, "instructor")
            if instr_key != None:
                _, instr_id = parts_of(instr_key, "instructor", "instructor")
                _, actor_id = parts_of(op.actor, "actor", "identity")
                # read-posture: (d) declared optionalReads by ReassignSession's
                # dispatcher for the instructor-standing path (absence is a
                # meaningful AuthDenied, not a correctness error) — mirrors
                # TombstoneSession's identical two reads above.
                bound = kv.Read("lnk.instructor." + instr_id + ".identifiedBy.identity." + actor_id)
                if bound == None or bound.isDeleted:
                    fail("AuthDenied: " + op.actor + " is not identifiedBy-bound to instructor " + instr_key)
                # read-posture: (d) declared optionalReads by ReassignSession's dispatcher.
                led_by = kv.Read("lnk.session." + sess_id + ".ledBy.instructor." + instr_id)
                if led_by == None or led_by.isDeleted:
                    fail("AuthDenied: " + instr_key + " does not lead session " + sess_key)
            else:
                enforce_workplace(studio_locations(studio), "cannot reassign session " + sess_key)

        new_instructor = optional_string(p, "newInstructor")
        clear_instructor = hasattr(p, "clearInstructor") and getattr(p, "clearInstructor") == True
        if new_instructor != None and clear_instructor:
            fail("InvalidArgument: newInstructor and clearInstructor are mutually exclusive")

        new_starts_raw = optional_string(p, "startsAt")
        new_ends_raw = optional_string(p, "endsAt")
        if (new_starts_raw == None) != (new_ends_raw == None):
            fail("InvalidArgument: startsAt and endsAt must be supplied together")
        reschedule = new_starts_raw != None

        # name/capacity/priceCents/residentPriceCents — edit-if-supplied,
        # carry-forward-if-omitted, the same optionality shape startsAt/endsAt
        # already have on this op (required on CreateSession, optional here).
        # optional_string already treats "" as "no change" (a class always
        # needs SOME name, so an empty edit reads as omitted, not a blank-out).
        # capacity's upper bound is checked separately since optional_int only
        # enforces a floor.
        new_name = optional_string(p, "name")
        new_capacity = optional_int(p, "capacity", 1)
        if new_capacity != None and new_capacity > 200:
            fail("InvalidArgument: capacity: must be in [1, 200]; got " + str(new_capacity))
        new_price_cents = optional_int(p, "priceCents", 0)
        new_resident_price_cents = optional_int(p, "residentPriceCents", 0)
        editing_schedule = new_name != None or new_capacity != None or new_price_cents != None or new_resident_price_cents != None

        if new_instructor == None and not clear_instructor and not reschedule and not studio_changed and not editing_schedule:
            fail("InvalidArgument: at least one of newInstructor, clearInstructor, newStudio, startsAt+endsAt, name, capacity, priceCents, or residentPriceCents is required")

        mutations = []

        # Read the session's CURRENT instructor unconditionally — needed below
        # to migrate instructorSlotClaim cells even on a reschedule-only call
        # that never touches newInstructor/clearInstructor.
        cur_ledby, old_instructor, cur_ledby_revision = session_ledby_link(sess_key)
        if new_instructor != None:
            new_instructor_final = new_instructor
        elif clear_instructor:
            new_instructor_final = None
        else:
            new_instructor_final = old_instructor

        # Instructor swap: tombstone the CURRENT ledBy link (if any) and write
        # the new one, mirroring identity-hygiene's tombstone-old+create-new
        # link-swap idiom (ddls.go). clearInstructor alone tombstones with no
        # replacement.
        if new_instructor != None or clear_instructor:
            if cur_ledby != None:
                mutations.append(make_tombstone_occ(cur_ledby, cur_ledby_revision))
            if new_instructor != None:
                require_live_typed(state, new_instructor, "newInstructor", "instructor")
                _, new_instr_id = parts_of(new_instructor, "newInstructor", "instructor")
                # revive, not plain create: a tombstone still occupies its
                # subject, so swapping BACK to an instructor this session led
                # before (or that led ANOTHER session that later swapped away)
                # would CreateOnly-reject on a key that already exists dead.
                mutations.append(make_link_create_or_revive("lnk.session." + sess_id + ".ledBy.instructor." + new_instr_id,
                                                              sess_key, new_instructor, "ledBy", "ledBy"))

        # The schedule is ALWAYS read and ALWAYS re-written (OCC-conditioned),
        # whether or not this call reschedules: an instructor-only swap
        # mutates only link keys (no key sharing sess_key's 3-segment vertex
        # root), and the write-path reply-constraint (Processor
        # commit_path.go's primaryKeyInCommit) rejects a script-named
        # primaryKey that lies outside the mutation batch's write footprint —
        # a link is never vertex-rooted. The schedule aspect IS vertex-rooted
        # to sess_key, so touching it (name/capacity carried forward
        # unchanged when this call is not a reschedule) is what keeps
        # sess_key inside the footprint on every branch, not merely the
        # time-move one.
        #
        # read-posture: (a) declared reads at ReassignSession dispatch —
        # required to carry name/capacity forward onto the re-written
        # schedule and, on a reschedule, to compute the OLD covered cells
        # below.
        sched = kv.Read(sess_key + ".schedule")
        if sched == None or sched.isDeleted:
            fail("UnknownSession: " + sess_key + " has no schedule")
        cur_starts = sched.data.get("startsAt")
        cur_ends = sched.data.get("endsAt")

        # Time move: release cells only the OLD span held, claim cells only
        # the NEW span needs, and leave a cell both spans cover untouched — a
        # release+reclaim round-trip against itself would spuriously read its
        # own about-to-be-released claim as a StudioConflict, since this
        # script's own mutations have not yet been applied to the state its
        # reads see.
        if reschedule:
            new_starts = time.rfc3339_utc(new_starts_raw)
            new_ends = time.rfc3339_utc(new_ends_raw)
            if not (new_starts < new_ends):
                fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + new_starts + " endsAt=" + new_ends)
            enforce_grid(new_starts, new_ends)
        else:
            new_starts = cur_starts
            new_ends = cur_ends

        old_cells = []
        if cur_starts != None and cur_ends != None:
            old_cells = slot_cells(cur_starts, cur_ends)
        new_cells = []
        if (reschedule or studio_changed) and new_starts != None and new_ends != None:
            new_cells = slot_cells(new_starts, new_ends)

        if studio_changed:
            # Hub genuinely changed (mirrors the instructor hub-change branch
            # below, over a studio instead of an instructor,
            # verticals.md "retiring a studio strands its classes"): release
            # ALL of the OLD studio's cells for the span it held and claim
            # ALL of the NEW studio's for the span this call leaves the
            # session holding — different keys, so no self-conflict risk the
            # way a same-hub reschedule delta has to guard against.
            for c in old_cells:
                mutations.append(make_tombstone(studio + ".slot" + slot_cellcode(c)))
            for c in new_cells:
                mutations.append(claim_cell(new_studio, slot_cellcode(c), "studioSlotClaim", "StudioConflict", "studio"))
            # studio already names the validated/derived CURRENT studio at
            # this point (either confirmed by require_matching_studio or
            # resolved by session_atstudio_link above) — deriving the OLD
            # link key from it directly, rather than re-walking
            # session_atstudio_link a second time, avoids a redundant live
            # read and the (unreachable today, but needless) chance of the
            # two enumerations disagreeing.
            _, old_studio_id = parts_of(studio, "studio", "studio")
            mutations.append(make_tombstone("lnk.session." + sess_id + ".atStudio.studio." + old_studio_id))
            _, new_studio_id = parts_of(new_studio, "newStudio", "studio")
            # revive, not plain create: a tombstone still occupies its
            # subject, so moving a session BACK to a studio it held before
            # (park in B, move back to A once A is usable again — exactly
            # the scenario this repair path exists for) would CreateOnly-
            # reject on a key that already exists dead.
            mutations.append(make_link_create_or_revive("lnk.session." + sess_id + ".atStudio.studio." + new_studio_id,
                                                          sess_key, new_studio, "atStudio", "atStudio"))
            # The session's atLocation snapshot names the room the class MEETS
            # in, so a move re-takes it from the NEW studio's live locatedAt
            # targets -- the same set CreateSession snapshots at mint time.
            # Left pointed at the old room, it would confine the moved class's
            # guest bookers to the desk of a room the class no longer uses
            # (wellnessBookersSpec walks atLocation alone, lenses.go), and it
            # would make the retired-studio fallback (session_locations here,
            # wellnessSessionsSpec on the read side) resolve a later-tombstoned
            # studio back to the ORIGINAL room instead of the one the class was
            # last moved to.
            #
            # A location the old and new studio share keeps its live link
            # untouched: re-writing it would be a tombstone and a revive of one
            # key in a single batch for no change. A room the class has sat in
            # before comes back through the revive branch, since its link key
            # already exists dead. A new studio wired to no location writes no
            # atLocation link, exactly as CreateSession confers none.
            new_locs = studio_locations(new_studio)
            prior_atloc = session_atlocation_links(sess_key)
            wanted_atloc = {}
            for loc in new_locs:
                ltype, lid = parts_of(loc, "newStudio location", "")
                wanted_atloc["lnk.session." + sess_id + ".atLocation." + ltype + "." + lid] = loc
            for lkey in prior_atloc:
                if lkey not in wanted_atloc and not prior_atloc[lkey].isDeleted:
                    mutations.append(make_tombstone_occ(lkey, prior_atloc[lkey].revision))
            for lkey in wanted_atloc:
                prior = prior_atloc.get(lkey)
                if prior == None:
                    mutations.append(make_link(lkey, sess_key, wanted_atloc[lkey], "atLocation", "atLocation", {}))
                elif prior.isDeleted:
                    mutations.append(make_link_revive_occ(lkey, sess_key, wanted_atloc[lkey], "atLocation", "atLocation", prior.revision))
        elif reschedule:
            for c in old_cells:
                if c not in new_cells:
                    mutations.append(make_tombstone(studio + ".slot" + slot_cellcode(c)))
            for c in new_cells:
                if c not in old_cells:
                    mutations.append(claim_cell(studio, slot_cellcode(c), "studioSlotClaim", "StudioConflict", "studio"))

        # Instructor slot-claim migration (providerSlotClaim mirror,
        # verticals.md), the instructor-hub twin of the studio cell handling
        # above — but the HUB itself can also change here, which studio's
        # never does. Same hub, span moved: release only the symmetric
        # difference, exactly like studio (avoids self-conflicting against a
        # cell this script is about to release). Hub genuinely changed
        # (swap/newly-assigned/cleared): release ALL of the old hub's cells
        # and claim ALL of the new hub's — no self-conflict risk since they
        # are different keys.
        old_instr_cells = []
        if old_instructor != None and cur_starts != None and cur_ends != None:
            old_instr_cells = slot_cells(cur_starts, cur_ends)
        new_instr_cells = []
        if new_instructor_final != None:
            new_instr_cells = slot_cells(new_starts, new_ends)

        if old_instructor == new_instructor_final:
            if old_instructor != None:
                for c in old_instr_cells:
                    if c not in new_instr_cells:
                        mutations.append(make_tombstone(old_instructor + ".slot" + slot_cellcode(c)))
                for c in new_instr_cells:
                    if c not in old_instr_cells:
                        mutations.append(claim_cell(new_instructor_final, slot_cellcode(c), "instructorSlotClaim", "InstructorConflict", "instructor"))
        else:
            for c in old_instr_cells:
                mutations.append(make_tombstone(old_instructor + ".slot" + slot_cellcode(c)))
            for c in new_instr_cells:
                mutations.append(claim_cell(new_instructor_final, slot_cellcode(c), "instructorSlotClaim", "InstructorConflict", "instructor"))

        # A shrink is refused under a claimed seat. Seat cells are
        # sessionSeatClaim aspects, alive while claimed and tombstoned on
        # release (claim_free_seats), and a seat index is never compacted: a
        # class of five with seats 1 and 5 claimed has TWO seated members and a
        # highest claimed index of 5, so a capacity of 3 would orphan seat 5's
        # holder even though the count fits. The refusal is therefore by the
        # HIGHEST claimed index in the range the shrink would remove — the
        # cells from new_capacity+1 up to the current capacity, walked from
        # the top so the first live cell found is the one the message names —
        # not by the seated count.
        #
        # The refusal is judged on the HYDRATED cells, and it is advisory
        # against a concurrent claim: this op never writes a seat cell, and
        # the commit is conditioned only on the keys it mutates (the
        # schedule's own revision), so a shrink to 4 racing a CreateBooking
        # that claims seat5 commits alongside it. That claim then sits one
        # seat above capacity: invisible to claim_free_seats (which walks
        # 1..capacity, so no later claim lands beside it), released by its
        # own cancellation like any other seat, and counted by the card's
        # seated count — which can read one above capacity for that seat's
        # life. The window is accepted; nothing is orphaned by it.
        #
        # A raise reads nothing (there is nothing above the current capacity
        # to check), and a shrink reads at most the delta, bounded by
        # MAX_SESSION_CAPACITY (200). A dispatcher that declares none of the
        # removed cells (the lattice CLI) pays that delta as live reads — up
        # to 199 for a 200 -> 1 shrink.
        cur_capacity = sched.data.get("capacity")
        if new_capacity != None and cur_capacity != None and new_capacity < cur_capacity:
            for n in range(cur_capacity, new_capacity, -1):
                # read-posture: (d) declared optionalReads at ReassignSession
                # dispatch — the seat cells between the new and the current
                # capacity (cmd/wellness-app app.js reassignSession declares
                # them on a shrink). An absent cell is a never-claimed seat.
                seat_cell = kv.Read(sess_key + ".seat" + str(n))
                if seat_cell != None and not seat_cell.isDeleted:
                    fail("CapacityBelowSeated: seat " + str(n) + " is claimed; capacity cannot drop below " + str(n))

        # Re-derive remindAt = new_starts − 24h unconditionally: on a genuine
        # reschedule this moves the deadline (re-arming wellness-reminders'
        # gate, mirroring clinic-domain's RescheduleAppointment); when nothing
        # moved (new_starts == cur_starts) it recomputes the same value, so no
        # branch is needed either way.
        new_remind_at = time.rfc3339_add(new_starts, "-24h")
        new_sched = {
            "name": new_name if new_name != None else sched.data.get("name"),
            "startsAt": new_starts, "endsAt": new_ends,
            "capacity": new_capacity if new_capacity != None else sched.data.get("capacity"),
            "remindAt": new_remind_at,
        }
        # priceCents/residentPriceCents: an explicit edit wins; otherwise carry
        # forward UNCHANGED — a session created before either field existed (or
        # created free / with no resident rate) simply has none to carry, so
        # the key stays omitted rather than being introduced as null.
        cur_price_cents = sched.data.get("priceCents")
        if new_price_cents != None:
            new_sched["priceCents"] = new_price_cents
        elif cur_price_cents != None:
            new_sched["priceCents"] = cur_price_cents
        cur_resident_price_cents = sched.data.get("residentPriceCents")
        if new_resident_price_cents != None:
            new_sched["residentPriceCents"] = new_resident_price_cents
        elif cur_resident_price_cents != None:
            new_sched["residentPriceCents"] = cur_resident_price_cents
        mutations.append(make_aspect_upsert_occ(sess_key, "schedule", "sessionSchedule", new_sched, sched.revision))

        final_studio = new_studio if studio_changed else studio
        events = [{"class": "wellness.sessionReassigned", "data": {"sessionKey": sess_key, "studio": final_studio}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": sess_key}}

    fail("UnknownOperation: " + ot)
`

// bookingDDLScript handles CreateBooking + CancelBooking. The seat-claim loop
// is the SAME CreateOnly-key-collision idiom sessionDDLScript's claim_cell
// uses, applied over an enumerated seat-index dimension — see
// wellness-vertical-design.md §1(2). The residency check reads
// lease-signing's applicationFor link by known key (no cross-package write,
// no declared package dependency needed at the Starlark level — the same
// "read another package's vertex by known key" idiom loftspace-ledger's
// heldFor / cafe-domain's cafeTabSettlement already use).
const bookingDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, rev):
    return {"op": "update", "key": vtx_key + "." + local_name, "expectedRevision": rev,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

# GRID_STEP/MAX_SLOT_CELLS/slot_cells/slot_cellcode/claim_cell mirror
# sessionDDLScript's identical functions (ddls.go) byte-for-byte — this
# script's own copy, the same cross-script duplication providerSlotClaim/
# patientSlotClaim's claim_cell already uses across clinic-domain's DDL
# scripts. Grid alignment itself was already enforced when the session's
# startsAt/endsAt were written (CreateSession's enforce_grid), so this script
# only discretizes, never re-validates the grid.
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

# The late-cancellation window, expressed as the negative offset from a
# session's own startsAt that opens it. A cancellation submitted at or after
# that instant forfeits the class-price charge (CancelBooking below).
LATE_CANCEL_WINDOW_OFFSET = "-2h"

def slot_cells(starts_at, ends_at):
    cells = []
    cur = starts_at
    for _i in range(MAX_SLOT_CELLS + 1):
        if not (cur < ends_at):
            return cells
        cells.append(cur)
        cur = time.rfc3339_add(cur, GRID_STEP)
    fail("SessionTooLong: session spans more than " + str(MAX_SLOT_CELLS) + " 15-minute slots (24h); shorten the interval")

def slot_cellcode(cell_start):
    return cell_start.replace("-", "").replace(":", "").lower()

def claim_cell(hub, cellcode, cls, conflict_code, who):
    key = hub + ".slot" + cellcode
    # read-posture: (d) declared optionalReads at CreateBooking/JoinWaitlist
    # dispatch — an absent cell is the common case (no existing booking),
    # never a required read.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def optional_number(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def require_bookable_identity(state, booker):
    # A kernel root is not a member. The Processor's commit-time guard refuses
    # every update/tombstone under a root carrying data.protected (the
    # primordial admin and service identities) but lets a create through, so
    # the bookerSlotClaim cells this op mints on the booker's own hub could be
    # written and never released: CancelBooking / ReleaseOrphanedBooking would
    # be refused at commit (ProtectedKey) for as long as the booking lived.
    # Refusing the booker here, before any cell is claimed, is the only place
    # the domain knows an identity is being used as a member. The root doc is
    # the declared read require_live_typed just proved alive; the flag is the
    # same bool the kernel guard tests.
    if state[booker].data.get("protected") == True:
        fail("ProtectedBooker: " + booker + " is a kernel identity, not a member; its slot cells could never be released")

def require_matching_session(book_id, session):
    _, sess_id = parts_of(session, "session", "session")
    for_session_lnk = "lnk.booking." + book_id + ".forSession.session." + sess_id
    # read-posture: (a) declared reads at CancelBooking / SetBookingAttendance
    # dispatch (validation link; absence means the caller named the wrong
    # session — WrongSession).
    fs = kv.Read(for_session_lnk)
    if fs == None or fs.isDeleted:
        fail("WrongSession: session " + session + " is not the session of booking vtx.booking." + book_id)
    return sess_id

ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

MAX_SESSION_CAPACITY = 200

def claim_free_seats(session_key, capacity, want):
    # Bounded for-range (Starlark has no while-loop) — the SAME enumerate-then-
    # CreateOnly-claim idiom as the session DDL's claim_cell, over seat indices
    # instead of time cells, walking the seat dimension once and returning up to
    # "want" (index, mutation) pairs for the cells it reads free. kv.Read is
    # LAZY (§2.5 idiom): it only decides which candidates to claim; the safety
    # property is the atomic batch's CreateOnly / expectedRevision conditioning
    # at commit — two callers racing for the same open seat both read it absent
    # and both emit op:create for the identical key, but CreateOnly at revision
    # 0 commits exactly once.
    #
    # Returning an EMPTY list rather than failing leaves the "class is full"
    # verdict to the caller: CreateBooking's single-seat wrapper below turns it
    # into SessionFull, while PromoteWaitlistedBookings turns it into
    # NothingToPromote, and the two are different facts about the same read.
    seats = []
    for n in range(1, MAX_SESSION_CAPACITY + 1):
        if n > capacity or len(seats) >= want:
            break
        seat_key = session_key + ".seat" + str(n)
        # read-posture: (d) declared optionalReads at CreateBooking dispatch;
        # undeclared-by-design bounded lazy read at PromoteWaitlistedBookings
        # dispatch (seat indexes derive from capacity, not a row column —
        # read_drift_baseline.txt). An absent seat is the common case at the
        # CreateBooking dispatch; at the promotion dispatch the bound is this
        # loop's own MAX_SESSION_CAPACITY ceiling, stated in that op's doc
        # comment, since its dispatcher (targets.go) has no row column to
        # template a seat key off.
        existing = kv.Read(seat_key)
        if existing == None:
            seats.append((n, make_aspect(session_key, "seat" + str(n), "sessionSeatClaim", {})))
        elif existing.isDeleted:
            seats.append((n, make_aspect_upsert_occ(session_key, "seat" + str(n), "sessionSeatClaim", {}, existing.revision)))
    return seats

def claim_first_free_seat(session_key, capacity):
    # CreateBooking's single-seat view of claim_free_seats: one free cell, or
    # SessionFull. The walk stops at the first free index it finds, so one seat
    # costs one read per held cell below it.
    seats = claim_free_seats(session_key, capacity, 1)
    if len(seats) == 0:
        fail("SessionFull: " + session_key + " has no open seats (capacity " + str(capacity) + ")")
    return seats[0][0], seats[0][1]

MAX_WAITLIST_SIZE = 200

def claim_first_free_waitlist_slot(session_key):
    # SAME enumerate-then-CreateOnly-claim idiom as claim_first_free_seat
    # above, over its OWN bounded index dimension (waitlist position) instead
    # of the seat dimension — unbounded by session capacity, since a waitlist
    # has no seat-count ceiling of its own.
    for n in range(1, MAX_WAITLIST_SIZE + 1):
        slot_key = session_key + ".wl" + str(n)
        # read-posture: (d) declared optionalReads at JoinWaitlist dispatch
        # (first-free-slot claim; an absent slot is the common case).
        existing = kv.Read(slot_key)
        if existing == None:
            return n, make_aspect(session_key, "wl" + str(n), "sessionWaitlistClaim", {})
        if existing.isDeleted:
            return n, make_aspect_upsert_occ(session_key, "wl" + str(n), "sessionWaitlistClaim", {}, existing.revision)
    fail("WaitlistFull: " + session_key + " has no open waitlist slots (max " + str(MAX_WAITLIST_SIZE) + ")")

WAITLIST_PROMOTION_PAGE_LIMIT = 50
MAX_WAITLIST_PROMOTION_PAGES = 4
MAX_WAITLIST_PROMOTION_PAGES_EXHAUSTIVE = 40

def collect_waitlist_candidates(session_key, max_pages):
    # The session's promotable waitlist, EARLIEST SLOT FIRST, plus whether the
    # walk reached the END of the session's forSession-in set: a bounded,
    # paginated kv.Links "in" walk off forSession (the session is the link's
    # TARGET, the booking its SOURCE, Contract #1 §1.1) — the identical shape
    # identity-hygiene's identity_has_open_tasks uses
    # (packages/identity-hygiene/ddls.go), collecting every live waitlisted
    # booking instead of answering a boolean. A session's forSession-in set
    # covers every booking ever made against THIS ONE class occurrence
    # (booked, cancelled, attended, noShow, waitlisted alike) — bounded by
    # real usage of a single session, not by the whole package's history, so
    # the same bound identity_has_open_tasks trusts for "how many tasks can
    # one identity accumulate" is the right order of magnitude here too.
    #
    # max_pages is the caller's, because the two callers need different things
    # from an exhausted bound. CancelBooking passes
    # MAX_WAITLIST_PROMOTION_PAGES: a cancellation must never be held hostage
    # by how much history a popular session has accumulated, and a walk that
    # gives up early just leaves the freed seat open to ordinary first-come
    # booking this round — a harmless no-op. PromoteWaitlistedBookings passes
    # MAX_WAITLIST_PROMOTION_PAGES_EXHAUSTIVE, because for IT an unfinished
    # walk that found nobody is indistinguishable from an empty waitlist, and
    # declining "nothing to promote" on that reading would be a false statement
    # about the class; it reads the second return value and declines
    # WaitlistWalkBound instead.
    #
    # A link may be delivered on more than one page (the pages are a cursor
    # over a live keyspace, not a snapshot), so a booking already collected is
    # skipped: emitting it twice would tombstone one waitlist cell twice and
    # OCC-upsert one status twice in a single batch.
    cands = []
    seen = {}
    cursor = None
    reached_end = False
    for _page in range(max_pages):
        # read-posture: (e) relation=forSession epoch=none (a single class
        # occurrence's own booking history — bounded real-world churn, never
        # a keyspace scan; a booking created concurrently with this walk
        # slips past and is picked up by a later cancellation or a later
        # promotion dispatch instead).
        links, cursor = kv.Links(session_key, "forSession", "in", cursor, WAITLIST_PROMOTION_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            if lk.sourceVertex in seen:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key — the booking is unknown
            # until it resolves from the link).
            cand_status = kv.Read(lk.sourceVertex + ".status")
            if cand_status == None or cand_status.isDeleted:
                continue
            if cand_status.data.get("value") != "waitlisted":
                continue
            slot = cand_status.data.get("waitlistSlot")
            if slot == None:
                continue
            seen[lk.sourceVertex] = True
            cands.append({
                "key": lk.sourceVertex,
                "slot": slot,
                "rate": cand_status.data.get("rate"),
                "booker": cand_status.data.get("booker"),
                "bookedAt": cand_status.data.get("bookedAt"),
                "revision": cand_status.revision,
            })
        if cursor == None:
            reached_end = True
            break
    # One sort at the end rather than an insertion per candidate: the pages
    # arrive in keyspace order, not slot order, and order IS the fairness rule
    # here — after the sort the earliest joiner THE WALK REACHED is candidate 0.
    # The sort is stable, so two bookings holding the same slot keep the order
    # the walk found them in.
    return sorted(cands, key=lambda c: c["slot"]), reached_end

def find_promotion_candidate(session_key):
    # CancelBooking's promotion lookup: the earliest candidate on the session's
    # waitlist, or nothing. One seat is freed by a cancellation, so one
    # candidate is all it can use, and an unfinished walk is a no-op it can
    # absorb — hence the soft page bound and the ignored end-of-walk flag.
    cands, _reached_end = collect_waitlist_candidates(session_key, MAX_WAITLIST_PROMOTION_PAGES)
    if len(cands) == 0:
        return None, None, None, None, None, None
    best = cands[0]
    return best["key"], best["slot"], best["rate"], best["booker"], best["revision"], best["bookedAt"]

def promotion_status(rate, seat_n, booker, session, sched, starts_at, promoted_at, booked_at):
    # The .status a promotion writes — CancelBooking's in-batch hand-over and
    # PromoteWaitlistedBookings share it so the two seating paths can never
    # drift: value booked, the seat, every field the waitlisted booking
    # carried (rate, booker, session, className, classStartsAt, bookedAt),
    # waitlistSlot dropped, promotedAt stamped with the promoting op's own
    # submittedAt, and priceCents snapshotted at THIS instant by the rule
    # CreateBooking applies at its own claim (effective_price_at_claim). A
    # waitlisted booking carries no priceCents until it is seated. bookedAt
    # is absent on a slot claimed before the stamp existed and is then
    # simply not carried — never written as null.
    promoted = {"value": "booked", "rate": rate, "seat": seat_n, "booker": booker, "session": session, "className": sched.data.get("name"), "classStartsAt": starts_at, "promotedAt": promoted_at, "priceCents": effective_price_at_claim(sched, rate)}
    if booked_at != None:
        promoted["bookedAt"] = booked_at
    return promoted

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64
# A page of one is not enough for a REPOINTED single-valued relation:
# ListLinks returns tombstoned links in the page too, keys sort by target id,
# and a repoint tombstones the old key and writes a new one -- so the live
# link can sort behind its own tombstoned predecessor. ReassignSession
# repoints atStudio on a studio move; session_studio pages until it finds the
# live link.
LIVE_LINK_PAGE_LIMIT = 8
MAX_LIVE_LINK_PAGES = 4
# A session's atLocation snapshot names every room its studio sits at, a
# handful at most, and this walks the FULL live set (session_locations), so
# it pages through every page rather than stopping at the first.
ATLOCATION_PAGE_LIMIT = 20
MAX_ATLOCATION_PAGES = 4

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self caller is bound instead by its own op's ownership probe (the
    # applicationFor / identifiedBy indirection): a resident legitimately holds
    # no worksAt link, and confining them by a rule written for staff would deny
    # every self-service write. The two guards are complementary, not
    # alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def studio_locations(studio_key):
    # A studio's OWN locatedAt link(s) -- duplicated from sessionDDLScript's
    # copy of the same helper, since Starlark scripts share no globals across
    # DDLs. The studio VERTEX first: TombstoneStudio soft-deletes it with no
    # cascade onto locatedAt, so a decommissioned studio would otherwise keep
    # conferring its old building.
    if not vertex_live(studio_key):
        return []
    # read-posture: (e) relation=locatedAt epoch=none -- CreateStudio writes
    # at most ONE locatedAt link and no op repoints or adds to it, so a page
    # of one is the whole set; the list shape is the consumer's contract.
    page, _ = kv.Links(studio_key, "locatedAt", "out", None, 1)
    locs = []
    for lk in page:
        if not lk.isDeleted:
            locs.append(lk.targetVertex)
    return locs

def session_studio(session_key):
    # The session's OWN LIVE studio, resolved from the graph (never a
    # caller-supplied payload field -- a caller cannot forge which studio it
    # is writing against). Factored out of session_locations below, mirroring
    # clinic-domain's appointment_provider / appointment_sites split.
    # ReassignSession's studio move tombstones the current atStudio link and
    # writes a new one (a REPOINT), so a page can hold the tombstone before
    # the live link; this pages until it finds one.
    cursor = None
    studio = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=atStudio epoch=none -- bounded, never a
        # keyspace scan.
        page, cursor = kv.Links(session_key, "atStudio", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                studio = lk.targetVertex
        if studio != None or cursor == None:
            break
    return studio

# The fee a studio with no recorded policy bills, in cents. The only place
# the literal lives; the studio vertex DDL and SetBookingAttendance's
# descriptor name it.
NO_POLICY_NO_SHOW_FEE_CENTS = 2500

def studio_no_show_fee(session_key):
    # The fee a noShow mark that names no fee of its own bills: the policy
    # recorded on the studio the session is at NOW (session_studio -- the
    # same live atStudio walk the front-of-house confinement runs, so a
    # class ReassignSession moved to another studio is billed by that
    # studio's policy, the same rule its confinement already applies).
    # Absent policy -- no live studio link, no profile, or a profile with no
    # noShowFeeCents -- bills the no-policy default; the studio VERTEX is not
    # gated, since a retired studio's recorded policy still governs the
    # classes it hosted. A recorded value that is not a non-negative
    # integer is refused rather than billed: both writers validate at the
    # mint (optional_fee_cents in the studio script), so a malformed value
    # is a row that predates the check, and the named refusal is what
    # points an operator at SetStudioProfile to repair it.
    studio = session_studio(session_key)
    if studio == None:
        return NO_POLICY_NO_SHOW_FEE_CENTS
    # read-posture: (e) per-candidate follow-up read off the atStudio
    # enumeration above (data-derived key -- the studio is unknown until the
    # walk resolves it, and no dispatcher can name it up front).
    profile = kv.Read(studio + ".profile")
    if profile == None or profile.isDeleted:
        return NO_POLICY_NO_SHOW_FEE_CENTS
    fee = profile.data.get("noShowFeeCents")
    if fee == None:
        return NO_POLICY_NO_SHOW_FEE_CENTS
    if type(fee) != type(0) or fee < 0:
        fail("InvalidState: " + studio + ".profile.noShowFeeCents is " + str(fee) + ", not a non-negative whole number of cents; repair it with SetStudioProfile before recording a no-show")
    return fee

def session_locations(session_key):
    # A booking's location is where its session's studio sits -- the session
    # -atStudio-> studio link CreateSession writes, then that studio's own
    # locatedAt links (studio_locations, which also re-proves the studio VERTEX
    # alive: TombstoneStudio does not cascade onto atStudio, so the caller
    # having proved the SESSION alive says nothing about the studio).
    locs = studio_locations(session_studio(session_key))
    if locs:
        return locs
    # TombstoneStudio soft-deletes the studio with no cascade onto locatedAt,
    # so studio_locations now returns [] for a session whose studio is dead --
    # stranding front-of-house staff who need to manage a session (and its
    # outstanding bookings) that predates the tombstone. Fall back to the
    # session's own atLocation link(s): CreateSession snapshots the studio's
    # then-live locatedAt target(s) at write time and ReassignSession re-takes
    # that snapshot on every studio move, so they name the room the class LAST
    # met in, independent of that studio's current status and never
    # caller-forged. Mirrors clinic-domain's appointment_sites fallback to
    # atSite for a tombstoned provider. A session whose studio carried no
    # location when the snapshot was last taken has no atLocation link and is
    # denied here, the same way a studio with no location confers no workplace
    # to begin with.
    # The snapshot holds at most one link per location the session's studio
    # sits at, a handful at most, but ALL of them are the answer, so this
    # walks every page rather than stopping at the first.
    cursor = None
    out = []
    for _page in range(MAX_ATLOCATION_PAGES):
        # read-posture: (e) relation=atLocation epoch=none -- bounded, off
        # the session key already proven alive by the caller, never a
        # keyspace scan.
        apage, cursor = kv.Links(session_key, "atLocation", "out", cursor, ATLOCATION_PAGE_LIMIT)
        for lk in apage:
            if not lk.isDeleted:
                out.append(lk.targetVertex)
        if cursor == None:
            break
    return out

def wellness_account_for_booker(booker_key):
    # The wellness-ledger account held for this member, or None where no
    # live one exists -- a member nothing has ever charged (the settlement
    # playbooks open the account lazily on the first priced booking or
    # no-show) owes nothing and cannot be on hold. The account is resolved
    # from the GRAPH, never from the payload: wellness-ledger's
    # WellnessCreateAccount is what writes the heldFor link (wellnessaccount
    # -> identity), so the caller has no field to omit or forge that would
    # reach a different account. The identity carries at most one live
    # wellnessaccount (wellness-ledger's wellnessLedgerAccountGuard is a
    # create-only per-member guard) but other verticals' ledgers anchor
    # their accounts on other holder types, never on the bare identity, so
    # the type filter below is defence in depth rather than a live
    # disambiguation. Paged with the bounded first-live cursor loop
    # (LIVE_LINK_PAGE_LIMIT x MAX_LIVE_LINK_PAGES = 32 links): a page can
    # hold a tombstoned link ahead of the live one. The bound exceeds what
    # the heldFor->identity writer can ever put on one member -- one link,
    # never tombstoned -- so running out of pages cannot happen here and the
    # final None below is unreachable; a copy of this walk into a relation
    # with wider fan-in must fail closed on exhaustion the way
    # actor_holds_operator does, not answer "no account".
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=heldFor epoch=none -- an identity carries
        # at most one live heldFor in-link from a wellnessaccount, so this is
        # never a keyspace scan. An account created concurrently with this
        # booking has no arrears episode yet and so nothing to hold on.
        page, cursor = kv.Links(booker_key, "heldFor", "in", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            if lk.sourceVertex.startswith("vtx.wellnessaccount."):
                return lk.sourceVertex
        if cursor == None:
            return None
    return None

def require_no_credit_hold(booker_key):
    # The credit hold: a member whose wellness account carries an arrears
    # episode a reminder has already gone out for claims no new seat.
    # wellness-ledger's EvaluateWellnessArrears writes .arrears.sentAt on the
    # commit that emits the reminder's outbox event -- the SEND INTENT; the
    # adapter's delivery outcome lands on .arrearsNotification, which this
    # guard does not read -- and carries it across every write of the same
    # episode, dropping it only when the evaluation finds the balance back at
    # zero (the episode ends). So sentAt present means exactly "this member
    # was reminded and still owes", while dueAt alone (overdue, not yet
    # reminded) or a bare {evaluatedAt} (nothing owed) is not a hold. The
    # rule holds on EVERY leg -- staff standing and member self alike --
    # because the debt is the member's, not the caller's, and it binds both
    # writers of a new claim on a session (CreateBooking and JoinWaitlist,
    # through this shared preamble): a waitlisted claim is seated later with
    # no further gate, so a hold on the booking alone would be a side door
    # through the waitlist. The state is reached by the walk above rather
    # than a caller-declared read: a hold that rested on the submitter's
    # declaration would be a hold the submitter could decline to declare.
    acct_key = wellness_account_for_booker(booker_key)
    if acct_key == None:
        return
    # read-posture: (e) per-candidate follow-up read off the enumeration
    # above -- the account is unknown until the heldFor walk resolves it, so
    # its .arrears key is data-derived and undeclarable client-side.
    arrears = kv.Read(acct_key + ".arrears")
    if arrears == None or arrears.isDeleted:
        return
    # The CLASS, not just the key: wellness-ledger is the sole writer of a
    # .arrears aspect and writes exactly this class, so a document of any
    # other class here is a fault to refuse, never state to decide a hold on.
    if not hasattr(arrears, "class") or getattr(arrears, "class") != "wellnessAccountArrears":
        fail("InvalidState: this member's wellness account arrears aspect is not a wellnessAccountArrears")
    sent_at = arrears.data.get("sentAt")
    if sent_at != None:
        fail("CreditHold: this member owes a balance a reminder went out for on " + str(sent_at)[:10] + "; it must be paid or written off before a new class is booked")

def effective_price_at_claim(sched, rate):
    # The price a seat pays, resolved at the instant it is claimed: the
    # session's residentPriceCents when the booking's rate is resident and
    # the schedule declares one, else its priceCents, else 0 — the same
    # resolution wellness-ledger's wellnessClassPriceSettlement charges by
    # (wellness-ledger/lenses.go). It is SNAPSHOTTED onto .status.priceCents
    # by every writer that seats a booking (CreateBooking, and both promotion
    # upserts at seating time), so a class re-priced after the claim never
    # relabels what the seat already paid: the ledger's settlement lens and
    # the member's card both read the snapshot first. A class that carries
    # no price at claim time snapshots 0 — a seat booked free stays free.
    price = sched.data.get("priceCents")
    if rate == "resident" and sched.data.get("residentPriceCents") != None:
        price = sched.data.get("residentPriceCents")
    if price == None:
        return 0
    return price

def prepare_booking_common(state, op, p, ot):
    # Shared CreateBooking / JoinWaitlist validation + guard/rate computation
    # — both mint a booking vertex anchored to a session, differing only in
    # which claim dimension (seat vs waitlist slot) they occupy. Factored so
    # the self-scope check, workplace confinement, credit hold, past-class
    # guard, double-book guard and resident-rate lookup can never drift
    # between the two entry points. ot names the calling op: the past-class
    # guard below is the one rule the two ops apply differently, and it
    # answers off ot rather than off anything a payload could carry. Returns
    # (session, sess_id, booker, booker_id, sched, rate, lease_key,
    # booker_guard_mut, booker_cell_muts, submitted) — submitted is the
    # canonical-UTC op.submittedAt both ops record as .status.bookedAt.
    # Capacity is deliberately NOT read here: only CreateBooking needs it (to
    # bound claim_first_free_seat), JoinWaitlist's own claim dimension has no
    # capacity ceiling.
    session = required_string(p, "session")
    _, sess_id = parts_of(session, "session", "session")
    require_live_typed(state, session, "session", "session")

    booker = required_string(p, "booker")
    _, booker_id = parts_of(booker, "booker", "identity")
    require_live_typed(state, booker, "booker", "identity")
    require_bookable_identity(state, booker)

    # Consumer self-scope (scope=self grant only): step 3 authorizes via
    # authContext.target == actor (Contract #6); the payload.booker field
    # IS the identity making the booking (no patient-style indirection
    # needed here, unlike clinic-domain's CreateAppointment), so the
    # script closes the gap with a direct field compare — no extra kv.Read.
    # Empty for the standing operator grant (scope=any never sets
    # authContext), so this check is a no-op there.
    # authcontext-target: (payload-bind) the target must equal payload.booker;
    # on a CREATE there is no bookedBy link to probe yet, so a forged target
    # only narrows who the caller may book for.
    if op.authContextTarget != "" and op.authContextTarget != booker:
        fail("AuthDenied: a consumer may only book a class for themselves")

    # Staff-standing confinement: the location comes from the SESSION's own
    # topology (session -atStudio-> studio -locatedAt-> location), never
    # from a payload field, so a staffer books a member into a class at a
    # place they work and nowhere else. A session whose studio is wired to
    # no location is operator-only by construction (an empty candidate list
    # denies). It answers before the schedule read below, so a staffer at
    # another building cannot use capacity or start-time errors as an
    # oracle on a class they may not touch.
    # workplace-exempt: (resource-bind) the consumer scope=self path is the
    # only validated one, and the compare directly above binds that target
    # to payload.booker -- the field that decides whose booking this is --
    # so an exempted caller is booking for itself.
    if not workplace_exempt():
        require_workplace(session_locations(session), "cannot book a seat on " + session)

    # The credit hold, in ORACLE order: after the workplace confinement, so a
    # staffer at another building learns nothing about a member's debt from a
    # refusal they were never entitled to reach; before the schedule read, so
    # a held member's request never gets as far as a capacity or start-time
    # answer that would leak which classes still have room. Both legs pass
    # through here -- the debt is the member's whoever submits.
    require_no_credit_hold(booker)

    # read-posture: (a) declared reads at CreateBooking/JoinWaitlist dispatch.
    sched = kv.Read(session + ".schedule")
    if sched == None or sched.isDeleted:
        fail("InvalidState: " + session + ".schedule is missing; cannot book")

    # Soft past-time guard (Capability-KV §06 — the op's own Starlark logic),
    # mirroring clinic-domain's enforce_future. submittedAt is caller-supplied
    # (the host clock is not exposed to Starlark), so this is a soft guard
    # appropriate to the trusted single-identity model. Normalize submittedAt
    # to canonical whole-second UTC (time.rfc3339_utc — pure, no clock read);
    # startsAt / endsAt are already stored canonical UTC (sessionSchedule),
    # and canonical-UTC RFC3339 compares lexically == chronologically.
    #
    # The deadline depends on WHO is booking and WHAT they are claiming. The
    # self-service leg (op.authTargetValidated — the consumer scope=self
    # grant, the only validated path) must claim before the class starts: a
    # member cannot book themselves into a class already under way. The desk
    # leg (a staff or operator submission, never target-validated) seating a
    # walk-in at the door may CreateBooking until the class ENDS — the person
    # is standing there, the seat is used, the charge and the attendance mark
    # follow — and is refused once it has ended, with a message that says so.
    # JoinWaitlist keeps the startsAt rule on every leg: a waitlist slot on a
    # class already under way can never be promoted into (both promotion
    # paths refuse SessionInPast past startsAt), so it is pointless whoever
    # asks for it.
    starts_at = sched.data.get("startsAt")
    if starts_at == None:
        fail("InvalidState: " + session + ".schedule.startsAt is missing; cannot book")
    ends_at = sched.data.get("endsAt")
    if ends_at == None:
        fail("InvalidState: " + session + ".schedule.endsAt is missing; cannot book")
    submitted = time.rfc3339_utc(op.submittedAt)
    if ot == "CreateBooking" and not op.authTargetValidated:
        if not (submitted < ends_at):
            fail("SessionInPast: session " + session + " ended at " + str(ends_at) + ", the class has ended (submitted " + submitted + ")")
    elif not (submitted < starts_at):
        fail("SessionInPast: session " + session + " starts at " + str(starts_at) + ", not in the future (submitted " + submitted + ")")

    # Booker slot-claim (patientSlotClaim mirror, verticals.md): the
    # per-SESSION sessionBookerClaim guard below only catches the same
    # session booked twice — it cannot see a booker claimed into two
    # DIFFERENT overlapping sessions, since each session's cells are disjoint
    # keys. Claiming one instructorSlotClaim-shaped cell per covered
    # 15-minute grid cell on the booker's OWN identity hub closes that gap;
    # the collision (BookerConflict) is what a probe found ("booked the same
    # member into both").
    booker_cell_muts = []
    for c in slot_cells(starts_at, ends_at):
        booker_cell_muts.append(claim_cell(booker, slot_cellcode(c), "bookerSlotClaim", "BookerConflict", "booker"))

    # Double-book guard (Capability-KV §06): a deterministic per-(session,
    # booker) existence marker on the session hub, the SAME create-only +
    # OCC-revive idiom cafe-domain's cafeOpenTabGuard uses. The KEY alone is
    # the lock — a second LIVE claim (booked OR waitlisted) by this booker on
    # this session collides at revision 0 (DoubleBooked); a booker acting on a
    # different session claims a different key. Present+alive → reject;
    # present+tombstoned (a prior claim was cancelled and released it) →
    # OCC-revive keyed on its own revision; absent → mint fresh. read-posture:
    # (d) declared optionalReads at CreateBooking/JoinWaitlist dispatch (the
    # guard hydrates into state).
    booker_guard_local = "bkr" + booker_id
    booker_guard_key = session + "." + booker_guard_local
    if booker_guard_key in state:
        if vertex_alive(state, booker_guard_key):
            fail("DoubleBooked: " + booker + " already holds a live booking or waitlist slot on " + session)
        booker_guard_mut = make_aspect_upsert_occ(session, booker_guard_local, "sessionBookerClaim", {}, state[booker_guard_key].revision)
    else:
        booker_guard_mut = make_aspect(session, booker_guard_local, "sessionBookerClaim", {})

    rate = "standard"
    lease_key = optional_string(p, "leaseAppKey")
    if lease_key != None:
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
        # read-posture: (d) declared optionalReads at CreateBooking/
        # JoinWaitlist dispatch (resident-rate lookup; absent → falls through
        # to standard rate, never a hard failure).
        lease_doc = kv.Read(lease_key)
        lease_alive = lease_doc != None and not lease_doc.isDeleted
        # .tenancy is stamped CreateOnly on a leaseapp's FIRST
        # DecideLeaseApplication approve (lease-signing/scripts.go) — its
        # presence is the only signal that this application actually
        # became an active tenancy, not merely a pending or declined one,
        # and its endedAt (recorded by EndTenancy once the term has run out
        # with no signed renewal) is the signal that the tenancy is over.
        # Without both checks a pending or declined applicant (the
        # applicationFor link stays live in both cases) or a moved-out
        # former tenant would wrongly qualify for the resident rate.
        # read-posture: (d) declared optionalReads at CreateBooking/JoinWaitlist dispatch.
        tenancy_doc = kv.Read(lease_key + ".tenancy")
        tenancy_present = tenancy_doc != None and not tenancy_doc.isDeleted and tenancy_doc.data.get("endedAt") == None
        # read-posture: (d) declared optionalReads at CreateBooking/JoinWaitlist dispatch.
        app_for_lnk = kv.Read("lnk.leaseapp." + lease_id + ".applicationFor.identity." + booker_id)
        link_live = app_for_lnk != None and not app_for_lnk.isDeleted
        if lease_alive and tenancy_present and link_live:
            rate = "resident"

    return session, sess_id, booker, booker_id, sched, rate, lease_key, booker_guard_mut, booker_cell_muts, submitted

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateBooking":
        # workplace-exempt: (resource-bind) prepare_booking_common's internal
        # workplace_exempt() call is discharged by ITS OWN authContextTarget
        # == payload.booker compare -- the consumer scope=self path is the
        # only validated one, and that compare binds the target to
        # payload.booker, the field that decides whose booking this is, so an
        # exempted caller is booking for itself.
        session, sess_id, booker, booker_id, sched, rate, lease_key, booker_guard_mut, booker_cell_muts, submitted = prepare_booking_common(state, op, p, ot)

        capacity = sched.data.get("capacity")
        if capacity == None:
            fail("InvalidState: " + session + ".schedule.capacity is missing; cannot book")
        seat_n, seat_mutation = claim_first_free_seat(session, capacity)

        book_id = bare_nanoid_or_mint(p, "bookingId")
        book_key = "vtx.booking." + book_id

        for_session_lnk = "lnk.booking." + book_id + ".forSession.session." + sess_id
        booked_by_lnk = "lnk.booking." + book_id + ".bookedBy.identity." + booker_id

        mutations = [
            make_vtx(book_key, "booking", {}),
            make_aspect(book_key, "status", "bookingStatus", {"value": "booked", "rate": rate, "seat": seat_n, "booker": booker, "session": session, "className": sched.data.get("name"), "classStartsAt": sched.data.get("startsAt"), "bookedAt": submitted, "priceCents": effective_price_at_claim(sched, rate)}),
            make_link(for_session_lnk, book_key, session, "forSession", "forSession", {}),
            make_link(booked_by_lnk, book_key, booker, "bookedBy", "bookedBy", {}),
            seat_mutation,
            booker_guard_mut,
        ]
        mutations.extend(booker_cell_muts)
        if rate == "resident":
            _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
            resident_rate_lnk = "lnk.booking." + book_id + ".residentRate.leaseapp." + lease_id
            mutations.append(make_link(resident_rate_lnk, book_key, lease_key, "residentRate", "residentRate", {}))

        events = [{"class": "wellness.bookingCreated", "data": {"bookingKey": book_key, "session": session, "booker": booker, "rate": rate}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "JoinWaitlist":
        # workplace-exempt: (resource-bind) prepare_booking_common's internal
        # workplace_exempt() call is discharged by ITS OWN authContextTarget
        # == payload.booker compare, identical to CreateBooking's discharge
        # above -- the consumer scope=self path is the only validated one,
        # and that compare binds the target to payload.booker, the field
        # that decides whose waitlist slot this is.
        session, sess_id, booker, booker_id, sched, rate, lease_key, booker_guard_mut, booker_cell_muts, submitted = prepare_booking_common(state, op, p, ot)

        slot_n, slot_mutation = claim_first_free_waitlist_slot(session)

        book_id = bare_nanoid_or_mint(p, "bookingId")
        book_key = "vtx.booking." + book_id

        for_session_lnk = "lnk.booking." + book_id + ".forSession.session." + sess_id
        booked_by_lnk = "lnk.booking." + book_id + ".bookedBy.identity." + booker_id

        mutations = [
            make_vtx(book_key, "booking", {}),
            make_aspect(book_key, "status", "bookingStatus", {"value": "waitlisted", "rate": rate, "waitlistSlot": slot_n, "booker": booker, "session": session, "className": sched.data.get("name"), "classStartsAt": sched.data.get("startsAt"), "bookedAt": submitted}),
            make_link(for_session_lnk, book_key, session, "forSession", "forSession", {}),
            make_link(booked_by_lnk, book_key, booker, "bookedBy", "bookedBy", {}),
            slot_mutation,
            booker_guard_mut,
        ]
        mutations.extend(booker_cell_muts)
        if rate == "resident":
            _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
            resident_rate_lnk = "lnk.booking." + book_id + ".residentRate.leaseapp." + lease_id
            mutations.append(make_link(resident_rate_lnk, book_key, lease_key, "residentRate", "residentRate", {}))

        events = [{"class": "wellness.waitlistJoined", "data": {"bookingKey": book_key, "session": session, "booker": booker, "rate": rate}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "CancelBooking":
        book_key = required_string(p, "bookingKey")
        _, book_id = parts_of(book_key, "bookingKey", "booking")
        if not vertex_alive(state, book_key):
            fail("UnknownBooking: " + book_key)
        cls = class_of(state, book_key)
        if cls != "booking":
            fail("WrongClass: bookingKey: " + book_key + " has class " + str(cls) + ", required booking")

        # Consumer self-scope (scope=self grant only): step 3 authorizes via
        # authContext.target == actor (Contract #6), but the op's endpoint is
        # the BOOKING vertex, not an identity. The script closes the gap by
        # requiring the target identity to be THIS booking's actual bookedBy
        # identity — mirrors clinic-domain's SetAppointmentStatus self-cancel
        # guard, over the bookedBy link instead of identifiedBy. Empty for the
        # standing operator grant (scope=any never sets authContext), so this
        # check is a no-op there.
        #
        # It answers before the session check below, which needs only the
        # booking key the caller already supplied. The session check answers
        # differently for this booking's session than any other, and class
        # schedules are public, so ahead of the binding it would tell a
        # self-scoped caller which class a stranger attends.
        # authcontext-target: (ownership) the target must be this booking's
        # own bookedBy identity, so a forged one only fails closed.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            booked_by_lnk = "lnk.booking." + book_id + ".bookedBy.identity." + target_identity_id
            # read-posture: (d) declared optionalReads by the self-service
            # caller — it already knows payload.bookingKey and its own
            # authContext.target before submitting, so it computes this key
            # client-side and declares it.
            booked_by = kv.Read(booked_by_lnk)
            if booked_by == None or booked_by.isDeleted:
                fail("AuthDenied: a consumer may only cancel their own booking")

        session = required_string(p, "session")
        require_matching_session(book_id, session)

        # Staff-standing confinement, resolved from the session's own topology
        # exactly as CreateBooking's is. It answers AFTER require_matching_session
        # above, and that order is load-bearing: the caller supplies the session
        # key, so confining on it before it is bound to THIS booking would let a
        # staffer name a class at their own building and cancel a seat in a
        # class somewhere else.
        # workplace-exempt: (ownership-bound) the consumer scope=self path is
        # the only validated one, and it is discharged above by the bookedBy
        # link read, which requires the exempted target to be this booking's own
        # booker.
        if not workplace_exempt():
            require_workplace(session_locations(session), "cannot cancel a seat on " + session)

        # read-posture: (a) declared reads at CancelBooking dispatch. Checked
        # before the past-class guard below: attendance can only ever be
        # recorded once a class has begun (SetBookingAttendance's own
        # SessionNotStarted), so a marked booking would always also trip
        # SessionStarted — checking AttendanceRecorded first gives the more
        # specific, more useful rejection in that overlap. A waitlisted
        # booking is equally cancellable (a member leaving the waitlist) —
        # attended/noShow are terminal, and so is forfeited: a booking that
        # already forfeited its class price inside the late window holds no
        # seat and owes what it owes, so there is nothing left to cancel.
        status = kv.Read(book_key + ".status")
        if status == None or status.isDeleted:
            fail("InvalidState: " + book_key + ".status is missing; cannot cancel")
        value = status.data.get("value")
        if value != "booked" and value != "waitlisted":
            fail("AttendanceRecorded: " + book_key + " is already " + str(value) + " (attendance recorded or class price forfeited); cannot cancel")

        # read-posture: (a) declared reads at CancelBooking dispatch — a
        # booking can only be cancelled before its class begins, the mirror
        # of SetBookingAttendance's SessionNotStarted. Both sides are
        # rfc3339-normalized UTC, so lexically == chronologically.
        sched = kv.Read(session + ".schedule")
        if sched == None or sched.isDeleted:
            fail("InvalidState: " + session + ".schedule is missing; cannot cancel")
        starts_at = sched.data.get("startsAt")
        if starts_at == None:
            fail("InvalidState: " + session + ".schedule.startsAt is missing; cannot cancel")
        submitted = time.rfc3339_utc(op.submittedAt)
        if submitted >= starts_at:
            fail("SessionStarted: session " + session + " started at " + str(starts_at) + ", cannot cancel a booking once the class has begun (submitted " + submitted + ")")

        # A cancellation inside the late window is still allowed — it just
        # forfeits the class price (the refund branch below). Computed here,
        # beside the guard that already resolved starts_at, so both time
        # rules read together. rfc3339_add re-emits canonical whole-second
        # UTC, the same form rfc3339_utc gives submitted, so the comparison
        # is lexical == chronological exactly as the guard above is. The
        # inequality matches that guard's too: at the boundary the stricter
        # rule wins, so cancelling exactly on the two-hour mark forfeits.
        #
        # The window is a rule about NOTICE: a member who held the seat
        # through the two hours and gave none forfeits. A seat handed from
        # the waitlist INSIDE the window (.status.promotedAt at or after the
        # cutoff — the stamp both promotion paths write) never had those two
        # hours to give, so its cancellation refunds like an early one, right
        # up to the SessionStarted guard above. A promotion that landed
        # before the cutoff is an ordinary seat (the member had the window to
        # cancel free), and a direct CreateBooking inside the window is
        # unchanged (the member chose the seat with the window disclosed).
        # The exemption turns on the recorded stamp, not on who submits — the
        # same posture as the window itself. promotedAt is written by
        # rfc3339_utc too, so this comparison is lexical == chronological
        # like the other two; at the boundary the exemption wins, mirroring
        # how the cutoff itself is inclusive. The member's app applies the
        # same rule before its cancel confirm (isLateCancel /
        # promotedInsideWindow, cmd/wellness-app/web/app.js).
        late_cancel_cutoff = time.rfc3339_add(starts_at, LATE_CANCEL_WINDOW_OFFSET)
        promoted_at = status.data.get("promotedAt")
        promoted_inside_window = promoted_at != None and promoted_at >= late_cancel_cutoff
        is_late_cancel = submitted >= late_cancel_cutoff and not promoted_inside_window

        # The class-price charge already posted against this booking, if any.
        # Enumerated once, here, because two decisions below read it: the
        # booked branch's "still owes" test (a posted charge is owed whatever
        # the class's CURRENT price says — a class re-priced to free after
        # charging still has the debit standing) and the refund/forfeit walk
        # after both branches, which reverses or keeps that same charge.
        # read-posture: (e) relation=settlesClassPrice epoch=none -- a
        # booking carries at most one live settlesClassPrice transaction
        # (wellnessClassPriceSettlement's own txCount=0 gate is single-fire).
        charge_page, _ = kv.Links(book_key, "settlesClassPrice", "in", None, 1)
        has_live_charge = False
        for lk in charge_page:
            if not lk.isDeleted:
                has_live_charge = True

        if value == "booked":
            seat_n = status.data.get("seat")
            if seat_n == None:
                fail("InvalidState: " + book_key + ".status.seat is missing; cannot cancel")

            # find_promotion_candidate (ddls.go): a bounded, paginated walk
            # over this session's waitlist looking for the earliest live
            # candidate. Handing it the just-freed seat DIRECTLY, in the SAME
            # mutation batch, instead of tombstoning the seat cell back open,
            # closes the race a two-step release-then-reclaim would leave for
            # an unrelated new CreateBooking caller to win the seat instead —
            # the seat-claim aspect is a pure existence marker with no owner
            # field, so reassigning ownership is just flipping the winning
            # booking's own .status.
            promo_book_key, promo_slot, promo_rate, promo_booker, promo_revision, promo_booked_at = find_promotion_candidate(session)

            # A booking that still owes stays. Inside the late window the
            # class price is forfeited (the refund branch below mints
            # nothing), so the booking keeps its vertex under the terminal
            # status 'forfeited' — the no-show twin's shape: a standing
            # charge on a live booking. Liveness is what keeps the guest
            # reachable for the desk that has to collect: wellnessBookers
            # covers a booker through ANY live booking, and
            # wellnessIdentitiesRead's booking fan-out is what lets that desk
            # decrypt the guest's name. The upsert carries the attendance
            # carry-forward set WITHOUT seat: the seat is released (or handed
            # to the promoted waitlister) exactly as an early cancel does, and
            # waitlistPromotion counts seat-holders off .status.seat, so
            # capacity frees the moment this status is written.
            #
            # "Still owes" is the late window AND a positive class price for
            # THIS booking. The price is the one wellness-ledger's
            # wellnessClassPriceSettlement charges (wellness-ledger/lenses.go):
            # the .status.priceCents snapshot the seating writer recorded at
            # claim time when present, else — for a seat claimed before the
            # snapshot existed — the schedule's current price by the same
            # rate rule (effective_price_at_claim); 0 means the class charges
            # this booker nothing, so a late cancel of a free seat has nothing
            # to forfeit and is tombstoned like an early one — the member's
            # app warns of a forfeit only on a priced class, and no card
            # should say "class price forfeited" where none was. A charge
            # that already posted is owed regardless of what either says now.
            # Outside the window nothing is owed either, so the booking is
            # tombstoned outright. promotedAt, bookedAt and priceCents ride
            # along with the other carried fields: how and when the seat was
            # obtained, and what it paid, stay on the record for as long as
            # the booking lives.
            effective_price = status.data.get("priceCents")
            if effective_price == None:
                effective_price = effective_price_at_claim(sched, status.data.get("rate"))
            owes_class_price = effective_price > 0 or has_live_charge
            if is_late_cancel and owes_class_price:
                forfeited = {"value": "forfeited"}
                for field in ["rate", "booker", "session", "className", "classStartsAt", "promotedAt", "bookedAt", "priceCents"]:
                    carried = status.data.get(field)
                    if carried != None:
                        forfeited[field] = carried
                mutations = [make_aspect_upsert_occ(book_key, "status", "bookingStatus", forfeited, status.revision)]
            else:
                mutations = [make_tombstone(book_key)]
            events = [{"class": "wellness.bookingCancelled", "data": {"bookingKey": book_key}}]
            # promotedAt records the instant the seat was handed over, in the
            # same canonical UTC form as submitted: it is what the late-cancel
            # rule above reads when THIS promoted booking later cancels, and
            # what the member's app badges the seating with. priceCents is
            # snapshotted HERE, at seating, by the same rule CreateBooking
            # applies (effective_price_at_claim): a waitlisted booking pays
            # nothing until it holds a seat, and what it pays is the price at
            # the instant it got one. bookedAt (the instant the waitlist was
            # joined) rides along unchanged.
            if promo_book_key != None:
                mutations.append(make_tombstone(session + ".wl" + str(promo_slot)))
                mutations.append(make_aspect_upsert_occ(promo_book_key, "status", "bookingStatus",
                    promotion_status(promo_rate, seat_n, promo_booker, session, sched, starts_at, submitted, promo_booked_at),
                    promo_revision))
                events.append({"class": "wellness.waitlistPromoted", "data": {"bookingKey": promo_book_key, "session": session, "seat": seat_n}})
            else:
                mutations.append(make_tombstone(session + ".seat" + str(seat_n)))
        else:
            # A waitlisted booking cancelling itself frees nothing for anyone
            # else to be promoted into — it never held a seat — so this is
            # just its own .wl<n> slot releasing, no promotion walk needed.
            slot_n = status.data.get("waitlistSlot")
            if slot_n == None:
                fail("InvalidState: " + book_key + ".status.waitlistSlot is missing; cannot cancel")
            mutations = [make_tombstone(book_key), make_tombstone(session + ".wl" + str(slot_n))]
            events = [{"class": "wellness.waitlistLeft", "data": {"bookingKey": book_key}}]

        # A class-price charge already posted (settlesClassPrice) before this
        # cancellation cannot be found by any post-tombstone lens walk over
        # THIS booking (Contract #1 isDeleted read-filtering — the exact
        # ReleaseOrphanedBooking .status.session precedent, bookingStatusAspectTypeDDL
        # above) — so the refund marker mints HERE, while the booking is
        # still alive, or never. Cancelling BEFORE a charge posts needs no
        # such marker: once tombstoned, wellnessClassPriceSettlement's own
        # MATCH (bk:booking, key=actorKey) simply stops matching
        # (wellness-ledger/lenses.go), and a late cancel that keeps its
        # vertex sits at status 'forfeited', which that lens's status =
        # 'booked' filter never matches either — so the class-price gap it
        # converges never fires for a cancelled booking in the first place,
        # and a kept-but-uncharged forfeit owes nothing. This branch exists
        # only to REVERSE a charge that already landed.
        # Run after both branches above rather than only the "booked" one:
        # classPriceSettlementSpec only ever posts a charge once status is
        # 'booked' (wellness-ledger/lenses.go), so a booking still waitlisted
        # at cancel time can never have one and this lookup is a guaranteed
        # no-op for it — sharing the one lookup costs nothing and keeps the
        # branches from diverging on this reversal logic. charge_page is the
        # enumeration taken above the branch, shared with the owes test.
        for lk in charge_page:
            if lk.isDeleted:
                continue
            charge_tx_key = lk.sourceVertex
            _, charge_tx_id = parts_of(charge_tx_key, "chargeTransactionKey", "wellnesstransaction")
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key) -- the exact amount
            # actually charged, not re-derived from the session's current
            # (possibly since-changed) price.
            entry = kv.Read(charge_tx_key + ".entry")
            if entry == None or entry.isDeleted:
                continue
            amount_cents = entry.data.get("amountCents")
            if amount_cents == None:
                continue
            # read-posture: (e) relation=postedTo epoch=none -- a
            # wellnesstransaction carries exactly one postedTo link (written
            # once, atomically, at mint time by WellnessDebitAccount/
            # WellnessCreditAccount; never rewired).
            acct_page, _ = kv.Links(charge_tx_key, "postedTo", "out", None, 1)
            account_key = None
            for alk in acct_page:
                if not alk.isDeleted:
                    account_key = alk.targetVertex
            if account_key == None:
                continue
            # The late-cancellation window: inside it, the charge simply is
            # not reversed (is_late_cancel above already exempts a seat that
            # was handed from the waitlist inside the window — that booking
            # refunds here like an early cancel). Nothing is minted, so the debit that already
            # posted stands — the same standing charge a no-show leaves
            # (SetBookingAttendance never reverses one either), on a booking
            # the booked branch above kept live as 'forfeited' (the charge
            # having posted means the class priced this booking). Without the
            # window a cancellation one second before the start would refund
            # in full, so a member intending to skip would always cancel
            # rather than no-show and pay nothing, while a member who simply
            # does not turn up pays the class price plus the no-show fee.
            #
            # Forfeiting the class price is the whole consequence: this is
            # deliberately not full no-show parity, which would also post the
            # separate noShowFeeCents debit and needs a marker type and a
            # wellness-ledger convergence lens of its own. That is a distinct
            # increment, not an unfinished edge of this one.
            #
            # It reaches only a 'booked' booking in practice — a waitlisted
            # one has nothing to forfeit, since classPriceSettlementSpec only
            # posts once status is 'booked' (wellness-ledger/lenses.go), so
            # this loop never finds a charge for it at all.
            if is_late_cancel:
                events.append({"class": "wellness.lateCancelForfeited", "data": {"bookingKey": book_key, "session": session, "accountKey": account_key, "amountCents": amount_cents}})
                break
            refund_id = nanoid.new()
            refund_key = "vtx.wellnessrefund." + refund_id
            reverses_lnk = "lnk.wellnessrefund." + refund_id + ".reverses.wellnesstransaction." + charge_tx_id
            mutations.append(make_vtx(refund_key, "wellnessrefund", {}))
            mutations.append(make_aspect(refund_key, "detail", "wellnessRefundDetail",
                {"accountKey": account_key, "amountCents": amount_cents, "bookingKey": book_key, "memo": "Class price refund", "className": status.data.get("className"), "classStartsAt": status.data.get("classStartsAt")}))
            mutations.append(make_link(reverses_lnk, refund_key, charge_tx_key, "reverses", "reverses", {}))
            events.append({"class": "wellness.classPriceRefundQueued", "data": {"bookingKey": book_key, "refundKey": refund_key, "accountKey": account_key, "amountCents": amount_cents}})
            break

        # Release the per-(session, booker) double-book guard so this booker can
        # re-book the session (cafe-domain's Settle→cafeOpenTabGuard release,
        # unconditioned: a stale-tombstone race can only free it early, and the
        # guard is re-earnable on the next book). The booker is read off the
        # booking's own .status.booker (stored by CreateBooking/JoinWaitlist) —
        # CancelBooking's operator path carries no authContext.target, so it
        # cannot derive the booker from authorization. A legacy booking
        # predating this field (and thus predating the guard) has no guard to
        # release, so skip cleanly.
        booker = status.data.get("booker")
        if booker != None:
            _, guard_booker_id = parts_of(booker, "booker", "identity")
            mutations.append(make_tombstone(session + ".bkr" + guard_booker_id))
            # Release the booker's bookerSlotClaim cells too (patientSlotClaim
            # mirror, verticals.md) — the SAME span CreateBooking/JoinWaitlist
            # claimed on the booker's own identity hub, freeing them to book an
            # overlapping class elsewhere. ends_at is already validated present
            # on this session's schedule (CreateSession requires it); a legacy
            # booking predating the guard field above already skipped that
            # branch, so this stays inside it too.
            ends_at = sched.data.get("endsAt")
            if ends_at != None:
                for c in slot_cells(starts_at, ends_at):
                    mutations.append(make_tombstone(booker + ".slot" + slot_cellcode(c)))
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "SetBookingAttendance":
        book_key = required_string(p, "bookingKey")
        _, book_id = parts_of(book_key, "bookingKey", "booking")
        if not vertex_alive(state, book_key):
            fail("UnknownBooking: " + book_key)
        cls = class_of(state, book_key)
        if cls != "booking":
            fail("WrongClass: bookingKey: " + book_key + " has class " + str(cls) + ", required booking")

        value = required_string(p, "status")
        if value != "attended" and value != "noShow":
            fail("InvalidArgument: status: must be attended or noShow; got " + value)

        session = required_string(p, "session")
        _, sess_id = parts_of(session, "session", "session")

        # Standing binder, the shape TombstoneSession uses: the operator passes
        # unconditionally; a bound instructor may additionally mark only a
        # booking on a class THEY lead — the caller supplies the instructor
        # param and BOTH the session's ledBy link to it AND the caller's own
        # identifiedBy binding to it must be alive (known keys). A caller with
        # no instructor param is the front-of-house path, confined by
        # workplace below instead of a lead binding.
        staff_confine_needed = False
        if not actor_holds_operator(op.actor):
            instr_key = optional_string(p, "instructor")
            if instr_key != None:
                _, instr_id = parts_of(instr_key, "instructor", "instructor")
                _, actor_id = parts_of(op.actor, "actor", "identity")
                # The caller's own binding answers first. It is keyed on op.actor, so
                # it can only ever say "am I this instructor?" — whereas the ledBy
                # check below answers about the SESSION, and every bound instructor in
                # the deployment holds this grant. Ahead of the binding, one
                # instructor could walk a studio's published schedule against a
                # stranger's class and read off who leads it, by which of the two
                # denials came back.
                # read-posture: (d) declared optionalReads by SetBookingAttendance's dispatcher.
                bound = kv.Read("lnk.instructor." + instr_id + ".identifiedBy.identity." + actor_id)
                if bound == None or bound.isDeleted:
                    fail("AuthDenied: " + op.actor + " is not identifiedBy-bound to instructor " + instr_key)
                # read-posture: (d) declared optionalReads by SetBookingAttendance's
                # dispatcher for the instructor-standing path (absence is a
                # meaningful AuthDenied, not a correctness error).
                led_by = kv.Read("lnk.session." + sess_id + ".ledBy.instructor." + instr_id)
                if led_by == None or led_by.isDeleted:
                    fail("AuthDenied: " + instr_key + " does not lead session " + session)
            else:
                staff_confine_needed = True

        # Ordered after the instructor binder: that answers differently for
        # THIS booking's session than any other, so ahead of the guard it
        # would tell any caller holding the grant which class a stranger's
        # booking is for. The front-of-house workplace check is ordered
        # AFTER this instead (below), mirroring CancelBooking: confining on
        # the caller-supplied session before it is proven to be THIS
        # booking's actual session would let a staffer name a session at
        # their own building to probe a stranger's booking.
        require_matching_session(book_id, session)

        if staff_confine_needed:
            # Front-of-house confinement, the same session -atStudio->
            # studio -locatedAt-> location walk CancelBooking uses.
            # workplace-exempt: (no-validated-path) SetBookingAttendance
            # grants no scope=self row and mints no task, so
            # op.authTargetValidated is never legitimately true here — this
            # always falls through to the worksAt walk.
            require_workplace(session_locations(session), "cannot record attendance on " + session)

        # read-posture: (a) declared reads at SetBookingAttendance dispatch —
        # attendance is only meaningful once the class has begun, the mirror of
        # CreateBooking's SessionInPast. Both sides are rfc3339-normalized UTC,
        # so lexically == chronologically.
        sched = kv.Read(session + ".schedule")
        if sched == None or sched.isDeleted:
            fail("InvalidState: " + session + ".schedule is missing; cannot record attendance")
        starts_at = sched.data.get("startsAt")
        if starts_at == None:
            fail("InvalidState: " + session + ".schedule.startsAt is missing; cannot record attendance")
        submitted = time.rfc3339_utc(op.submittedAt)
        if submitted < starts_at:
            fail("SessionNotStarted: session " + session + " starts at " + str(starts_at) + ", attendance cannot be recorded yet (submitted " + submitted + ")")

        status_key = book_key + ".status"
        # read-posture: (a) declared reads at SetBookingAttendance dispatch — the
        # live status carries the rate / seat / booker / session bookkeeping this
        # write must preserve. Overwriting it with value alone would strip .seat
        # and .booker, and CancelBooking reads BOTH to release the seat cell and
        # the per-(session, booker) double-book guard: a marked booking would
        # then fail to cancel and leak its guard, locking the booker out of
        # re-booking. .session is the same single-anchor idiom, carried forward
        # so ReleaseOrphanedBooking's convergence lens can still find it after
        # the class is over. .className/.classStartsAt are the same carry-forward
        # idiom for the class-name snapshot CreateBooking/JoinWaitlist took off
        # the session's .schedule at booking time — wellness-ledger's
        # wellnessLedgerHistory lens reads them off THIS aspect (never the
        # session) so a member's billing history still names the class after
        # TombstoneSession kills the session vertex the booking was for.
        status = kv.Read(status_key)
        if status == None or status.isDeleted:
            fail("InvalidState: " + status_key + " is missing; cannot record attendance")

        # A waitlisted booking never held a confirmed seat, so there is no
        # attendance to record for it (pastDueBookingsSpec's own status='booked'
        # restriction already documents this — a waitlisted entry is invisible
        # to the auto-no-show sweep for the identical reason). Closes a real gap:
        # nothing above checks the CURRENT value, only that the NEW one is
        # attended/noShow, and the carry-forward loop below does not carry
        # waitlistSlot — so an unguarded transition would silently strand a
        # waitlisted booking in a status with neither .seat nor .waitlistSlot,
        # a shape ReleaseOrphanedBooking could never cleanly release. A
        # forfeited booking is refused for the same reason from the other
        # side: CancelBooking's late-window branch released its seat and kept
        # the vertex only so the standing class-price charge stays on a
        # reachable booker, so it too has nothing to attend or miss, and the
        # carry-forward loop below would write a status with neither field.
        # Re-marking attended<->noShow is still free (either value corrects
        # the other); a live 'waitlisted' or 'forfeited' start is refused.
        current_value = status.data.get("value")
        if current_value == "waitlisted":
            fail("InvalidState: " + book_key + " is waitlisted, never held a seat; nothing to record attendance for")
        if current_value == "forfeited":
            fail("InvalidState: " + book_key + " is forfeited, its seat was released at cancellation; nothing to record attendance for")

        merged = {"value": value}
        for field in ["rate", "seat", "booker", "session", "className", "classStartsAt", "promotedAt", "bookedAt", "priceCents"]:
            carried = status.data.get(field)
            if carried != None:
                merged[field] = carried

        # No-show fee (billing consequence for a missed class): only meaningful
        # when transitioning TO noShow. A caller-supplied noShowFeeCents wins
        # outright; an omitted one is resolved from the STUDIO's recorded
        # policy (studio_no_show_fee -- the studio the session is at now,
        # walked on every leg, operator included, since the fee is a billing
        # fact and not a confinement). Mirrors clinic-domain's
        # SetAppointmentStatus in spirit, but unlike clinic (which splits the
        # automated sweep and the staff path into two separate ops) wellness
        # dispatches both through this one SetBookingAttendance op, so the
        # "no fee" signal has to be a value, not the absence of a call: an
        # explicit noShowFeeCents:0 means the automated pastDueBookings sweep
        # marked a documentation lapse (nobody at the desk checked the member
        # in), or the desk waived the fee, so no fee is written at all; a
        # studio policy of 0 lands the same way. Negative values are
        # rejected; positive values are stored as-is. Deliberately NOT in
        # the carry-forward loop above: a later re-mark to attended drops it
        # (the field only matters while the booking IS a noShow) — and
        # reverses whichever no-show-fee charge already posted, below.
        if value == "noShow":
            fee_cents = optional_number(p, "noShowFeeCents")
            if fee_cents == None:
                fee_cents = studio_no_show_fee(session)
            elif fee_cents < 0:
                fail("InvalidArgument: noShowFeeCents: must not be negative")
            if fee_cents > 0:
                merged["noShowFeeCents"] = fee_cents

        # The OCC upsert is keyed on the status aspect's own revision, so two
        # markers racing the same booking commit exactly one. Either value
        # corrects the other — re-marking is the affordance, not a defect.
        mutations = [make_aspect_upsert_occ(book_key, "status", "bookingStatus", merged, status.revision)]
        events = [{"class": "wellness.attendanceRecorded",
                   "data": {"bookingKey": book_key, "session": session, "value": value}}]

        # A re-mark from noShow to attended means the fee was wrong, not
        # merely forgiven — reverse whichever no-show-fee charge already
        # posted, the same settles-relation lookup-and-mint
        # ReleaseOrphanedBooking uses below. Only this direction reverses
        # anything: the opposite correction (attended to noShow) posts a
        # fresh charge itself, through wellness-ledger's own
        # wellnessNoShowSettlement lens, same as an original noShow mark.
        if value == "attended" and current_value == "noShow":
            # read-posture: (e) relation=settles epoch=none -- a booking
            # carries at most one live settles transaction EVER
            # (wellnessNoShowSettlement's txCount=0 gate never re-fires once
            # one has posted, even across a later noShow re-mark) — the same
            # single-live-link guarantee ReleaseOrphanedBooking's identical
            # lookup below relies on.
            noshow_charge_page, _ = kv.Links(book_key, "settles", "in", None, 1)
            for nlk in noshow_charge_page:
                if nlk.isDeleted:
                    continue
                noshow_tx_key = nlk.sourceVertex
                _, noshow_tx_id = parts_of(noshow_tx_key, "chargeTransactionKey", "wellnesstransaction")
                # read-posture: (e) relation=reverses epoch=none -- idempotency
                # guard. CancelBooking is exempt (it tombstones the booking
                # in the same batch, so there is no later dispatch left to
                # re-walk this settles link at all). SetBookingAttendance
                # itself is re-markable: a second noShow->attended cycle
                # would walk into the SAME settles-linked transaction (the
                # gate above never lets a second one post) and, without this
                # check, mint a second refund for money already credited
                # back once. ReleaseOrphanedBooking's own noShow branch
                # carries the identical guard for the identical reason: an
                # attendance cycle can reverse this same charge before the
                # session is ever tombstoned, and release re-walks the same
                # settles link with no memory of that.
                already_refunded_page, _ = kv.Links(noshow_tx_key, "reverses", "in", None, 1)
                already_refunded = False
                for arlk in already_refunded_page:
                    if not arlk.isDeleted:
                        already_refunded = True
                        break
                if already_refunded:
                    continue
                # read-posture: (e) per-candidate follow-up read off the
                # enumeration above (data-derived key) -- the exact amount
                # actually charged.
                noshow_entry = kv.Read(noshow_tx_key + ".entry")
                if noshow_entry == None or noshow_entry.isDeleted:
                    continue
                noshow_amount_cents = noshow_entry.data.get("amountCents")
                if noshow_amount_cents == None:
                    continue
                # read-posture: (e) relation=postedTo epoch=none -- same
                # single-live-link guarantee as ReleaseOrphanedBooking's
                # identical lookup below.
                noshow_acct_page, _ = kv.Links(noshow_tx_key, "postedTo", "out", None, 1)
                noshow_account_key = None
                for nalk in noshow_acct_page:
                    if not nalk.isDeleted:
                        noshow_account_key = nalk.targetVertex
                if noshow_account_key == None:
                    continue
                noshow_refund_id = nanoid.new()
                noshow_refund_key = "vtx.wellnessrefund." + noshow_refund_id
                noshow_reverses_lnk = "lnk.wellnessrefund." + noshow_refund_id + ".reverses.wellnesstransaction." + noshow_tx_id
                mutations.append(make_vtx(noshow_refund_key, "wellnessrefund", {}))
                mutations.append(make_aspect(noshow_refund_key, "detail", "wellnessRefundDetail",
                    {"accountKey": noshow_account_key, "amountCents": noshow_amount_cents, "bookingKey": book_key, "memo": "No-show fee refund", "className": merged.get("className"), "classStartsAt": merged.get("classStartsAt")}))
                mutations.append(make_link(noshow_reverses_lnk, noshow_refund_key, noshow_tx_key, "reverses", "reverses", {}))
                events.append({"class": "wellness.noShowFeeRefundQueued", "data": {"bookingKey": book_key, "refundKey": noshow_refund_key, "accountKey": noshow_account_key, "amountCents": noshow_amount_cents}})
                break

        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "ReleaseOrphanedBooking":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind
        # this op is operator/Scope:"any", far wider than the one engine
        # (wellnessOrphanedBookingSettlement, targets.go) that ever dispatches
        # it -- and this branch forwards bookingKey/session/status into an
        # external.notification body the bridge turns into a real vendor
        # send, so a wider submitter set is a forged notification: an
        # arbitrary operator releasing an unrelated booking and having the
        # platform notify that booking's member. First statement in the
        # branch: it also denies every oracle beneath it (liveness, shape,
        # the refund lookups).
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: ReleaseOrphanedBooking is restricted to Weaver's dispatch actor; got " + op.actor)

        book_key = required_string(p, "bookingKey")
        _, book_id = parts_of(book_key, "bookingKey", "booking")
        if not vertex_alive(state, book_key):
            fail("UnknownBooking: " + book_key)
        cls = class_of(state, book_key)
        if cls != "booking":
            fail("WrongClass: bookingKey: " + book_key + " has class " + str(cls) + ", required booking")

        # Unlike CancelBooking there is no caller-supplied session to
        # validate a claim against, so this is ALSO confined to the standing
        # operator grant (actor_holds_operator), on top of the primordial
        # actor check above -- the wellnessOrphanedBookingSettlement target
        # is the only submitter (permissions.go).
        if not actor_holds_operator(op.actor):
            fail("AuthDenied: " + op.actor + " may not release " + book_key)

        # read-posture: (a) declared reads at ReleaseOrphanedBooking dispatch.
        status = kv.Read(book_key + ".status")
        if status == None or status.isDeleted:
            fail("InvalidState: " + book_key + ".status is missing; nothing to release")
        value = status.data.get("value")
        if value != "booked" and value != "waitlisted" and value != "noShow":
            fail("InvalidState: " + book_key + " is not in booked, waitlisted, or noShow status; nothing to release")
        # The class this booking was made for, from either of the two places
        # that name it. The .status.session anchor is the fast path -- hydrated
        # up front off the dispatch's declared optionalReads (targets.go) --
        # and the forSession link is the source of truth: CreateBooking and
        # JoinWaitlist each write the booking vertex, its status and both its
        # links in one atomic batch, so every booking carries exactly one
        # forSession link whether or not its aspect carries the anchor.
        session = status.data.get("session")
        if session == None:
            # read-posture: (e) relation=forSession epoch=none -- a booking
            # carries exactly one LIVE forSession link, written atomically
            # with the booking itself by CreateBooking/JoinWaitlist; the page
            # is bounded by that one-live-link invariant, not by "one link
            # total", so a tombstoned entry sorting first cannot starve the
            # live one out of a page of 1.
            session_page, _ = kv.Links(book_key, "forSession", "out", None, 8)
            for slk in session_page:
                if not slk.isDeleted:
                    session = slk.targetVertex
                    break
        if session == None:
            fail("InvalidState: " + book_key + " names no session, by anchor or by forSession link; nothing to release")

        # A LIVE session must go through CancelBooking instead. This op exists
        # only to drain a booking whose session TombstoneSession already
        # killed -- wellness-domain deliberately does not cascade tombstones
        # (package.go), so nothing else releases the seat/waitlist cell +
        # double-book guard a called-off class leaves claimed; re-checking
        # liveness here, rather than trusting the dispatching lens row, keeps
        # a stale gap evaluation from ever touching a booking on a class that
        # is still on.
        # read-posture: (d) declared optionalReads at ReleaseOrphanedBooking
        # dispatch, or (e) follow-up off the forSession enumeration above when
        # the aspect carries no anchor.
        sess_doc = kv.Read(session)
        if sess_doc != None and not sess_doc.isDeleted:
            fail("SessionStillLive: " + session + " has not been cancelled; use CancelBooking instead")

        # A noShow booking mints from "booked" only (SetBookingAttendance never
        # moves a waitlisted seat to noShow -- a waitlisted booker never held
        # a confirmed seat), so it shares "booked"'s seat cell, never a
        # waitlist slot.
        mutations = [make_tombstone(book_key)]
        if value == "booked" or value == "noShow":
            seat_n = status.data.get("seat")
            if seat_n == None:
                fail("InvalidState: " + book_key + ".status.seat is missing; cannot release")
            mutations.append(make_tombstone(session + ".seat" + str(seat_n)))
        else:
            slot_n = status.data.get("waitlistSlot")
            if slot_n == None:
                fail("InvalidState: " + book_key + ".status.waitlistSlot is missing; cannot release")
            mutations.append(make_tombstone(session + ".wl" + str(slot_n)))

        events = [{"class": "wellness.bookingCancelled", "data": {"bookingKey": book_key, "session": session}}]

        # The call-off notice fires off THIS op's own transactional outbox,
        # in the same batch that tombstones the booking -- the only place
        # the notice is guaranteed: a lens gap anchored on the booking would
        # race this same release and lose once the row is gone. Keyed on
        # (bookingKey, "calledOff", session) so a redelivery of the SAME
        # release dedups at the adapter; no distinct dedup dimension is
        # needed since a booking is only ever released once. className and
        # classStartsAt are dropped from params when the status carries
        # neither, so the event body never carries a null.
        calloff_ref = book_key + ":calledOff:" + session
        calloff_params = {"bookingKey": book_key, "changeType": "calledOff", "changeRef": session,
                           "sessionKey": session, "status": value}
        calloff_class_name = status.data.get("className")
        if calloff_class_name != None:
            calloff_params["className"] = calloff_class_name
        calloff_class_starts_at = status.data.get("classStartsAt")
        if calloff_class_starts_at != None:
            calloff_params["classStartsAt"] = calloff_class_starts_at
        events.append({"class": "external.notification",
                        "data": {"instanceKey": calloff_ref, "adapter": "notification",
                                 "replyOp": "RecordBookingChangeNotification",
                                 "externalRef": calloff_ref, "idempotencyKey": calloff_ref,
                                 "params": calloff_params}})

        # A class-price charge already posted (settlesClassPrice) before the
        # studio tombstoned this class cannot be found by any post-tombstone
        # lens walk over THIS booking (Contract #1 isDeleted read-filtering —
        # the same reasoning CancelBooking's identical lookup below documents
        # in full) — so the refund marker mints HERE, while the booking is
        # still alive, or never. Unlike CancelBooking there is no
        # late-cancellation forfeiture branch: the booker did nothing to
        # cause this cancellation, the studio called off the class, so a
        # posted charge always reverses regardless of how close to class
        # time TombstoneSession ran. Shared across the "booked", "waitlisted",
        # and "noShow" branches above: a waitlisted booking can never carry a
        # charge (wellness-ledger's classPriceSettlementSpec posts only once
        # status is 'booked'), so this is a guaranteed no-op for it, but a
        # noShow booking genuinely can — classPriceSettlementSpec charges the
        # class price unconditional on attendance, so a member who no-showed
        # a class the studio then called off can carry BOTH a class-price
        # charge (found here) and a no-show-fee charge (found below).
        # read-posture: (e) relation=settlesClassPrice epoch=none -- a
        # booking carries at most one live settlesClassPrice transaction
        # (wellnessClassPriceSettlement's own txCount=0 gate is single-fire).
        charge_page, _ = kv.Links(book_key, "settlesClassPrice", "in", None, 1)
        for lk in charge_page:
            if lk.isDeleted:
                continue
            charge_tx_key = lk.sourceVertex
            _, charge_tx_id = parts_of(charge_tx_key, "chargeTransactionKey", "wellnesstransaction")
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key) -- the exact amount
            # actually charged, not re-derived from the session's current
            # (possibly since-changed) price.
            entry = kv.Read(charge_tx_key + ".entry")
            if entry == None or entry.isDeleted:
                continue
            amount_cents = entry.data.get("amountCents")
            if amount_cents == None:
                continue
            # read-posture: (e) relation=postedTo epoch=none -- a
            # wellnesstransaction carries exactly one postedTo link (written
            # once, atomically, at mint time by WellnessDebitAccount/
            # WellnessCreditAccount; never rewired).
            acct_page, _ = kv.Links(charge_tx_key, "postedTo", "out", None, 1)
            account_key = None
            for alk in acct_page:
                if not alk.isDeleted:
                    account_key = alk.targetVertex
            if account_key == None:
                continue
            refund_id = nanoid.new()
            refund_key = "vtx.wellnessrefund." + refund_id
            reverses_lnk = "lnk.wellnessrefund." + refund_id + ".reverses.wellnesstransaction." + charge_tx_id
            mutations.append(make_vtx(refund_key, "wellnessrefund", {}))
            mutations.append(make_aspect(refund_key, "detail", "wellnessRefundDetail",
                {"accountKey": account_key, "amountCents": amount_cents, "bookingKey": book_key, "memo": "Class price refund", "className": status.data.get("className"), "classStartsAt": status.data.get("classStartsAt")}))
            mutations.append(make_link(reverses_lnk, refund_key, charge_tx_key, "reverses", "reverses", {}))
            events.append({"class": "wellness.classPriceRefundQueued", "data": {"bookingKey": book_key, "refundKey": refund_key, "accountKey": account_key, "amountCents": amount_cents}})
            break

        # The no-show-fee sibling of the class-price lookup above: a booking
        # this op releases while it is already "noShow" (the auto-no-show
        # sweep fired before the studio tombstoned the class -- an ordering
        # accident, not a meaningful distinction) may carry a posted no-show
        # fee (relation "settles", written by wellness-ledger's
        # WellnessDebitAccount{bookingRef}, wellnessNoShowSettlement's own
        # missing_charge dispatch) -- the member never had a chance to avoid
        # it either way, so it reverses on release exactly like the class
        # price does: unconditionally, no late-window forfeiture. Guaranteed
        # a no-op for "booked"/"waitlisted" (WellnessDebitAccount{bookingRef}
        # is dispatched only once status is 'noShow'). A DISTINCT relation
        # from settlesClassPrice (ddls.go's bookingRef/priceBookingRef split)
        # so the two settlement gaps never collide in a count(), and a
        # SEPARATE wellnessrefund marker from the one above -- a booking can
        # owe both a class-price and a no-show-fee refund at once, and each
        # marker's own reverses link must name its own distinct charge.
        # read-posture: (e) relation=settles epoch=none -- a booking carries
        # at most one live settles transaction (wellnessNoShowSettlement's
        # own txCount=0 gate is single-fire, mirroring settlesClassPrice's).
        noshow_charge_page, _ = kv.Links(book_key, "settles", "in", None, 1)
        for nlk in noshow_charge_page:
            if nlk.isDeleted:
                continue
            noshow_tx_key = nlk.sourceVertex
            _, noshow_tx_id = parts_of(noshow_tx_key, "chargeTransactionKey", "wellnesstransaction")
            # read-posture: (e) relation=reverses epoch=none -- idempotency
            # guard, mirroring SetBookingAttendance's identical probe on
            # this same settles-linked transaction above. Unlike the
            # class-price branch above (settlesClassPrice's charge can only
            # ever be reversed HERE, by this op), a no-show fee can already
            # have been reversed by an attended re-mark before the session
            # was ever tombstoned: fee charged on noShow -> attended
            # (SetBookingAttendance mints a marker reversing it) -> noShow
            # again (the settles gate is single-fire, so no second charge
            # posts) -> TombstoneSession -> this release walks the SAME
            # settles link and, without this check, would mint a second
            # marker reversing the same already-reversed charge.
            noshow_already_refunded_page, _ = kv.Links(noshow_tx_key, "reverses", "in", None, 1)
            noshow_already_refunded = False
            for narlk in noshow_already_refunded_page:
                if not narlk.isDeleted:
                    noshow_already_refunded = True
                    break
            if noshow_already_refunded:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key).
            noshow_entry = kv.Read(noshow_tx_key + ".entry")
            if noshow_entry == None or noshow_entry.isDeleted:
                continue
            noshow_amount_cents = noshow_entry.data.get("amountCents")
            if noshow_amount_cents == None:
                continue
            # read-posture: (e) relation=postedTo epoch=none -- same
            # single-live-link guarantee as the class-price lookup above.
            noshow_acct_page, _ = kv.Links(noshow_tx_key, "postedTo", "out", None, 1)
            noshow_account_key = None
            for nalk in noshow_acct_page:
                if not nalk.isDeleted:
                    noshow_account_key = nalk.targetVertex
            if noshow_account_key == None:
                continue
            noshow_refund_id = nanoid.new()
            noshow_refund_key = "vtx.wellnessrefund." + noshow_refund_id
            noshow_reverses_lnk = "lnk.wellnessrefund." + noshow_refund_id + ".reverses.wellnesstransaction." + noshow_tx_id
            mutations.append(make_vtx(noshow_refund_key, "wellnessrefund", {}))
            mutations.append(make_aspect(noshow_refund_key, "detail", "wellnessRefundDetail",
                {"accountKey": noshow_account_key, "amountCents": noshow_amount_cents, "bookingKey": book_key, "memo": "No-show fee refund", "className": status.data.get("className"), "classStartsAt": status.data.get("classStartsAt")}))
            mutations.append(make_link(noshow_reverses_lnk, noshow_refund_key, noshow_tx_key, "reverses", "reverses", {}))
            events.append({"class": "wellness.noShowFeeRefundQueued", "data": {"bookingKey": book_key, "refundKey": noshow_refund_key, "accountKey": noshow_account_key, "amountCents": noshow_amount_cents}})
            break

        # Release the per-(session, booker) double-book guard, the same
        # unconditioned release CancelBooking performs. The session is
        # already confirmed dead above, so there is no seat left to promote
        # anyone into here (unlike CancelBooking's live-session path).
        booker = status.data.get("booker")
        if booker == None:
            # read-posture: (e) relation=bookedBy epoch=none -- a booking
            # carries exactly one LIVE bookedBy link, written atomically with
            # the booking itself by CreateBooking/JoinWaitlist, so it names
            # the booker for a booking whose aspect carries no anchor; the
            # page is bounded by that one-live-link invariant, not by "one
            # link total", so a tombstoned entry sorting first cannot starve
            # the live one out of a page of 1.
            booker_page, _ = kv.Links(book_key, "bookedBy", "out", None, 8)
            for blk in booker_page:
                if not blk.isDeleted:
                    booker = blk.targetVertex
                    break
        if booker != None:
            _, guard_booker_id = parts_of(booker, "booker", "identity")
            mutations.append(make_tombstone(session + ".bkr" + guard_booker_id))
            # Release the booker's bookerSlotClaim cells too (patientSlotClaim
            # mirror, verticals.md). TombstoneSession tombstones only the
            # session's vertex ROOT, never cascading onto its .schedule aspect
            # (package.go's "no cascade" doctrine) — the same reason this op
            # exists at all — so the span is still readable here even though
            # the session itself is now dead.
            # read-posture: (d) declared optionalReads at ReleaseOrphanedBooking
            # dispatch, or (e) follow-up off the forSession enumeration above
            # when the aspect carries no anchor.
            sess_sched = kv.Read(session + ".schedule")
            if sess_sched != None and not sess_sched.isDeleted:
                sess_starts = sess_sched.data.get("startsAt")
                sess_ends = sess_sched.data.get("endsAt")
                if sess_starts != None and sess_ends != None:
                    for c in slot_cells(sess_starts, sess_ends):
                        mutations.append(make_tombstone(booker + ".slot" + slot_cellcode(c)))
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "PromoteWaitlistedBookings":
        # Weaver-only: seat a class's whole waitlist into the seats that class
        # already has free. CancelBooking hands a freed seat DIRECTLY to the
        # earliest candidate in its own batch, so it covers the cancellation
        # path completely; the seat that goes free WITHOUT a cancellation --
        # ReassignSession raising a full class's capacity -- has no such
        # carrier, and the waitlisted member is stranded with a disabled
        # button and a CreateBooking that refuses BookerConflict on their own
        # standing claim. This op is that carrier.
        #
        # ALL promotable candidates in ONE dispatch, not one per gap. Weaver's
        # mark holds the gap for the lease and a still-open gap does not
        # re-dispatch until reclaim, so a one-seat-per-dispatch op would seat
        # one member per lease window on a class that just gained four seats.
        #
        # Reads: the session vertex (declared, vertex_alive below) and its
        # .schedule (declared -- capacity + the past-class guard), targets.go.
        # The forSession-in walk is the declared enumeration; its per-candidate
        # .status reads are class-(e) follow-ups off it. claim_free_seats's own
        # seat-cell reads are the one lazy family here: a seat index derives
        # from the session's capacity, not from a row column, so no dispatcher
        # can declare it -- bounded by MAX_SESSION_CAPACITY (200) reads, and in
        # practice by the class's real capacity, since the walk stops as soon
        # as it has one free cell per candidate.
        session = required_string(p, "session")
        parts_of(session, "session", "session")
        if not vertex_alive(state, session):
            fail("UnknownSession: " + session)
        sess_cls = class_of(state, session)
        if sess_cls != "session":
            fail("WrongClass: session: " + session + " has class " + str(sess_cls) + ", required session")

        # Confined to the standing operator grant alone, like
        # ReleaseOrphanedBooking: there is no caller-supplied booking to bind a
        # self-scope claim to, and choosing WHICH waitlisted booking gets a
        # seat is exactly the queue-jump this op exists to prevent, so the
        # wellnessWaitlistPromotion target is the only sanctioned submitter
        # (permissions.go).
        if not actor_holds_operator(op.actor):
            fail("AuthDenied: " + op.actor + " may not promote waitlisted bookings on " + session)

        # read-posture: (a) declared reads at PromoteWaitlistedBookings
        # dispatch.
        promo_sched = kv.Read(session + ".schedule")
        if promo_sched == None or promo_sched.isDeleted:
            fail("InvalidState: " + session + ".schedule is missing; cannot promote")
        promo_starts_at = promo_sched.data.get("startsAt")
        if promo_starts_at == None:
            fail("InvalidState: " + session + ".schedule.startsAt is missing; cannot promote")

        # The SAME inequality CreateBooking's own guard uses
        # (prepare_booking_common): a seat handed out once the class has begun
        # is a seat nobody can use, and the lens's freshUntil deadline is armed
        # at this exact instant, so the two agree on the boundary. A stale gap
        # evaluation that reaches this op after the class started is refused
        # here rather than trusted.
        promo_submitted = time.rfc3339_utc(op.submittedAt)
        if not (promo_submitted < promo_starts_at):
            fail("SessionInPast: session " + session + " starts at " + str(promo_starts_at) + ", not in the future (submitted " + promo_submitted + ")")

        promo_capacity = promo_sched.data.get("capacity")
        if promo_capacity == None:
            fail("InvalidState: " + session + ".schedule.capacity is missing; cannot promote")

        # The EXHAUSTIVE page budget, not CancelBooking's: "nobody is waiting"
        # is only true of a walk that reached the end of the session's
        # forSession-in set, and this op's decline says exactly that. 40 pages x
        # 50 links bounds the walk at 2,000 links, well inside the script wall
        # and the live-read budget.
        promo_cands, promo_walked_all = collect_waitlist_candidates(session, MAX_WAITLIST_PROMOTION_PAGES_EXHAUSTIVE)
        if len(promo_cands) == 0:
            if not promo_walked_all:
                fail("WaitlistWalkBound: " + session + " has more than " + str(MAX_WAITLIST_PROMOTION_PAGES_EXHAUSTIVE * WAITLIST_PROMOTION_PAGE_LIMIT) + " booking links; waitlist not fully walked")
            fail("NothingToPromote: " + session + " carries no live waitlisted booking")
        promo_seats = claim_free_seats(session, promo_capacity, len(promo_cands))
        if len(promo_seats) == 0:
            fail("NothingToPromote: " + session + " has no free seat (capacity " + str(promo_capacity) + ")")

        # A decline, not a silent no-op, in all three cases above: Weaver
        # records a refused dispatch against the gap's own retry budget, so a
        # target firing on a row that is no longer true is visible instead of
        # being absorbed as a successful empty commit — and the three reasons
        # stay distinct, because "the waitlist is empty", "every seat is taken"
        # and "the walk never finished" call for different answers.

        promo_take = len(promo_seats)
        if len(promo_cands) < promo_take:
            promo_take = len(promo_cands)

        mutations = []
        events = []
        for i in range(promo_take):
            cand = promo_cands[i]
            seat_n = promo_seats[i][0]
            # The promotion write is CancelBooking's block (promotion_status):
            # claim the seat cell, release the candidate's own .wl<n> slot, and
            # OCC-upsert its .status from waitlisted to booked -- seat set,
            # waitlistSlot dropped, every other field (rate, booker, session,
            # className, classStartsAt, bookedAt) carried forward, promotedAt
            # stamped with this dispatch's own submittedAt (the instant
            # CancelBooking's late-cancel rule measures the window against
            # when this booking later cancels), and priceCents snapshotted at
            # this seating. The OCC revision is the one the candidate walk
            # read, so a booking that cancelled or was promoted between the
            # walk and the commit loses the batch rather than being seated
            # twice.
            mutations.append(promo_seats[i][1])
            mutations.append(make_tombstone(session + ".wl" + str(cand["slot"])))
            mutations.append(make_aspect_upsert_occ(cand["key"], "status", "bookingStatus",
                promotion_status(cand["rate"], seat_n, cand["booker"], session, promo_sched, promo_starts_at, promo_submitted, cand["bookedAt"]),
                cand["revision"]))
            events.append({"class": "wellness.waitlistPromoted", "data": {"bookingKey": cand["key"], "session": session, "seat": seat_n}})

        # The per-(session, booker) sessionBookerClaim guard and the booker's
        # own bookerSlotClaim cells stay untouched: a promoted booker's claim
        # was already live, just waitlisted, and both cells are claimed at
        # JoinWaitlist time for the same span the seat covers.
        return {"mutations": mutations, "events": events, "response": {"primaryKey": session}}

    fail("UnknownOperation: " + ot)
`

// instructorDDLScript handles CreateInstructor + TombstoneInstructor +
// BindInstructorIdentity. BindInstructorIdentity mirrors clinic-domain's
// BindProviderIdentity verbatim (identifiedBy mint + idempotent holdsRole
// grant + CreateOnly mutual-exclusivity guards on both sides) —
// instructorDDLScript is derived from instructorDDLScriptTemplate by pinning
// the placeholder — identity-domain's own "provider" role key — to its real,
// deterministic value (see instructorRoleKey above).
var instructorDDLScript = strings.ReplaceAll(instructorDDLScriptTemplate, "__EXPECTED_PROVIDER_ROLE_KEY__", instructorRoleKey)

const instructorDDLScriptTemplate = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    # Endpoint validation: the linked vertex MUST be alive AND the expected
    # class. A dead or wrong-class studio/identityKey is never wired.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned update: create-if-absent / overwrite-if-present. .profile
    # always exists (CreateInstructor mints it), so this is the overwrite path.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def actor_bound_to_instructor(actor_key, instructor_key):
    # The standing instructor-binding guard: an actor identifiedBy-bound to
    # THIS SPECIFIC instructor may edit its own profile even without an
    # operator grant -- complementary to actor_holds_operator, never a
    # replacement (mirrors clinic-domain's actor_bound_to_provider verbatim).
    #
    # Load-bearing, not decorative: BindInstructorIdentity grants the SAME
    # generic identity-domain provider role that clinic's BindProviderIdentity
    # and service-domain's BindServiceProviderIdentity grant, so the permission
    # row's provider grant is reachable by a bound clinic provider and a bound
    # service provider too. This guard -- not the grant -- is what confines the
    # write to the caller's own instructor record.
    _, actor_id = parts_of(actor_key, "actor", "identity")
    _, target_instructor_id = parts_of(instructor_key, "instructorKey", "instructor")
    # read-posture: (d) declared in contextHint.optionalReads by the standing
    # caller's dispatcher (probing whether THIS actor is bound to the TARGET
    # instructor; absent -> AuthDenied, mirroring claim-style absence-tolerance)
    lnk = kv.Read("lnk.instructor." + target_instructor_id + ".identifiedBy.identity." + actor_id)
    return lnk != None and not lnk.isDeleted

def claim_instructor_identity(instr_key):
    # Entity-keyed guard: at most one identity may ever bind THIS instructor
    # (nothing releases the claim, so it is never tombstoned) — mirrors
    # clinic-domain's claim_provider_identity idiom, keyed on the INSTRUCTOR
    # side of the pair. kv.Read here is LAZY (§2.5 idiom) — it only picks the
    # error message; the safety property is the atomic batch's CreateOnly
    # conditioning at commit.
    # read-posture: (d) declared in contextHint.optionalReads by
    # BindInstructorIdentity's dispatcher (absence is the common first-bind case)
    existing = kv.Read(instr_key + ".identityClaim")
    if existing != None:
        fail("InstructorAlreadyBound: " + instr_key + " is already bound to another identity")
    return make_aspect(instr_key, "identityClaim", "instructorIdentityClaim", {})

def claim_identity_instructor(identity_key):
    # Identity-keyed guard: at most one wellness instructor may ever bind
    # THIS identity (nothing releases the claim, so it is never tombstoned)
    # — mirrors clinic-domain's claim_identity_provider idiom and its
    # cross-package aspect-attachment shape exactly, just keyed
    # "instructorClaim".
    # read-posture: (d) declared in contextHint.optionalReads by
    # BindInstructorIdentity's dispatcher (absence is the common first-bind case)
    existing = kv.Read(identity_key + ".instructorClaim")
    if existing != None:
        fail("IdentityAlreadyBoundToInstructor: " + identity_key + " is already bound to another wellness instructor")
    return make_aspect(identity_key, "instructorClaim", "identityInstructorClaim", {})

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateInstructor":
        display_name = required_string(p, "displayName")
        iid = bare_nanoid_or_mint(p, "instructorId")
        ikey = "vtx.instructor." + iid
        mutations = [
            make_vtx(ikey, "instructor", {}),
            make_aspect(ikey, "profile", "instructorProfile", {"displayName": display_name}),
        ]
        studio = optional_string(p, "studio")
        if studio != None:
            require_live_typed(state, studio, "studio", "studio")
            _, studio_id = parts_of(studio, "studio", "studio")
            mutations.append(make_link("lnk.instructor." + iid + ".teachesAt.studio." + studio_id,
                                       ikey, studio, "teachesAt", "teachesAt", {}))
        events = [{"class": "wellness.instructorCreated", "data": {"instructorKey": ikey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": ikey}}

    if ot == "TombstoneInstructor":
        ikey = required_string(p, "instructorKey")
        parts_of(ikey, "instructorKey", "instructor")
        if not vertex_alive(state, ikey):
            fail("UnknownInstructor: " + ikey)
        mutations = [make_tombstone(ikey)]
        events = [{"class": "wellness.instructorTombstoned", "data": {"instructorKey": ikey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": ikey}}

    if ot == "SetInstructorProfile":
        ikey = required_string(p, "instructorKey")
        parts_of(ikey, "instructorKey", "instructor")
        if not vertex_alive(state, ikey):
            fail("UnknownInstructor: " + ikey)
        cls = class_of(state, ikey)
        if cls != "instructor":
            fail("WrongClass: instructorKey: " + ikey + " has class " + str(cls) + ", required instructor")
        # Standing binder: operator passes unconditionally; otherwise the actor
        # must be identifiedBy-bound to THIS instructor (an instructor edits only
        # THEIR OWN profile). Two binders, complementary, mirroring clinic's
        # SetProviderHours actor_holds_operator/actor_bound_to_provider framing.
        if not actor_holds_operator(op.actor):
            if not actor_bound_to_instructor(op.actor, ikey):
                fail("AuthDenied: " + op.actor + " may not set the profile of instructor " + ikey)
        display_name = required_string(p, "displayName")
        # Unconditioned upsert REPLACING the whole .profile aspect (it always
        # exists -- CreateInstructor mints it). displayName stays REQUIRED: it is
        # the only field the aspect carries, and both wellnessSessions
        # (instructorName) and wellnessInstructors project it, so an edit that
        # could drop it would null a column the member-facing class list and the
        # staff scheduling form both depend on.
        mutations = [make_aspect_upsert(ikey, "profile", "instructorProfile", {"displayName": display_name})]
        events = [{"class": "wellness.instructorProfileSet", "data": {"instructorKey": ikey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": ikey}}

    if ot == "BindInstructorIdentity":
        ikey = required_string(p, "instructorKey")
        _, instr_id = parts_of(ikey, "instructorKey", "instructor")
        require_live_typed(state, ikey, "instructorKey", "instructor")

        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")
        require_live_typed(state, identity_key, "identityKey", "identity")

        # instructor identifiedBy identity (Contract #1 §1.1: the
        # later-arriving instructor is the source, the pre-existing identity
        # is the target). Sentence: "instructor identifiedBy identity".
        # Mirrors clinic-domain's provider identifiedBy mint verbatim.
        identified_by_lnk = "lnk.instructor." + instr_id + ".identifiedBy.identity." + identity_id
        mutations = [make_link(identified_by_lnk, ikey, identity_key, "identifiedBy", "identifiedBy", {})]

        # Mutual exclusivity, both sides.
        mutations.append(claim_instructor_identity(ikey))
        mutations.append(claim_identity_instructor(identity_key))

        # Grant the provider role, exactly as clinic-domain's
        # BindProviderIdentity does — IDEMPOTENT (mirrors rbac AssignRole's
        # state-check branch): a holdsRole link already alive is left
        # untouched rather than re-created.
        provider_role_key = "__EXPECTED_PROVIDER_ROLE_KEY__"
        provider_role_id = provider_role_key[len("vtx.role."):]
        holds_role_lnk = "lnk.identity." + identity_id + ".holdsRole.role." + provider_role_id
        # read-posture: (d) declared in contextHint.optionalReads by
        # BindInstructorIdentity's dispatcher (absence is the common
        # first-bind case, mirroring rbac's AssignRole idempotency check)
        existing_role_grant = kv.Read(holds_role_lnk)
        if existing_role_grant == None:
            mutations.append(make_link(holds_role_lnk, identity_key, provider_role_key, "holdsRole", "holdsRole", {}))
        elif existing_role_grant.isDeleted:
            # Re-grant of a TOMBSTONED link: update, not create. A create asserts
            # revision 0 and the tombstone sits at a later revision, so a create
            # RevisionConflicts forever — a re-bound instructor whose prior grant
            # was tombstoned could never be re-granted the provider role. The key
            # is a declared optionalReads read, so its revision is hydrated for
            # the update's OCC.
            mutations.append({"op": "update", "key": holds_role_lnk,
                              "document": {"class": "holdsRole", "isDeleted": False,
                                           "sourceVertex": identity_key, "targetVertex": provider_role_key,
                                           "localName": "holdsRole", "data": {}}})

        events = [{"class": "wellness.instructorIdentityBound",
                   "data": {"instructorKey": ikey, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": identified_by_lnk}}

    fail("UnknownOperation: " + ot)
`
