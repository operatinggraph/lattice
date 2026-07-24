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
	studioVertexDDL     = "studio"
	sessionVertexDDL    = "session"
	bookingVertexDDL    = "booking"
	instructorVertexDDL = "instructor"

	studioProfileAspectDDL      = "studioProfile"
	sessionScheduleAspectDDL    = "sessionSchedule"
	studioSlotClaimAspectDDL    = "studioSlotClaim"
	sessionSeatClaimAspectDDL   = "sessionSeatClaim"
	sessionBookerClaimAspectDDL = "sessionBookerClaim"
	bookingStatusAspectDDL      = "bookingStatus"

	instructorProfileAspectDDL       = "instructorProfile"
	instructorIdentityClaimAspectDDL = "instructorIdentityClaim"
	identityInstructorClaimAspectDDL = "identityInstructorClaim"
)

// DDLs returns the package's thirteen DDL meta-vertex declarations:
//
//   - studio (vertexType) — owns CreateStudio + TombstoneStudio.
//   - session (vertexType) — owns CreateSession + TombstoneSession.
//   - booking (vertexType) — owns CreateBooking + CancelBooking.
//   - instructor (vertexType) — owns CreateInstructor + TombstoneInstructor +
//     BindInstructorIdentity (the provider-archetype binding,
//     persona-worlds-design.md Fire W0).
//   - studioProfile / sessionSchedule / studioSlotClaim / sessionSeatClaim /
//     sessionBookerClaim / bookingStatus / instructorProfile /
//     instructorIdentityClaim / identityInstructorClaim (aspectType) —
//     step-6 write gates.
//
// Architectural rules (binding — the known-key discipline of clinic-domain /
// loftspace-domain): the scripts read ONLY by known key. No prefix scans, no
// kv.Links enumeration, no raw adjacency lookups.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		studioVertexTypeDDL(),
		sessionVertexTypeDDL(),
		bookingVertexTypeDDL(),
		instructorVertexTypeDDL(),
		studioProfileAspectTypeDDL(),
		sessionScheduleAspectTypeDDL(),
		studioSlotClaimAspectTypeDDL(),
		sessionSeatClaimAspectTypeDDL(),
		sessionBookerClaimAspectTypeDDL(),
		bookingStatusAspectTypeDDL(),
		instructorProfileAspectTypeDDL(),
		instructorIdentityClaimAspectTypeDDL(),
		identityInstructorClaimAspectTypeDDL(),
	}
}

func studioVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     studioVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateStudio", "TombstoneStudio"},
		Description: "Wellness studio DDL. Vertex shape: vtx.studio.<NanoID>, class=studio, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateStudio mints the studio + writes the " +
			".profile aspect {name (required)} atomically, and — when the optional location param is supplied — " +
			"the studio locatedAt location LINK (lnk.studio.<id>.locatedAt.<locType>.<locId>, class \"locatedAt\"; " +
			"source = the later-arriving studio, target = the pre-existing location, Contract #1 §1.1). locatedAt " +
			"carries NO authorization meaning — it exists so reachability walks (edge-manifest's entity lenses) " +
			"can find the studio from a resident's containedIn chain; service-access authZ stays entirely on " +
			"service-location's availableAt. A studio with no location is legal and simply un-browsable. " +
			"TombstoneStudio soft-deletes one (no cascade onto its " +
			"sessions — the projection lenses anchor on the live root, mirroring clinic-domain's no-cascade rule).",
		Script: studioDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string","description":"The studio's display name (CreateStudio; required)."},` +
			`"studioId":{"type":"string","description":"Optional bare NanoID for the new studio vertex (CreateStudio); absent → minted."},` +
			`"location":{"type":"string","description":"Optional vtx.<locType>.<NanoID> location the studio is at (CreateStudio; validated alive + class=location; writes the locatedAt link). Listed in ContextHint.Reads when supplied."},` +
			`"studioKey":{"type":"string","description":"vtx.studio.<NanoID> of an existing studio (TombstoneStudio; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.studio.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"name":      "The studio's display name. Stored on the .profile aspect (CreateStudio; required).",
			"studioId":  "Optional bare NanoID (no dots / key segments) for the new studio vertex. Absent → minted with nanoid.new().",
			"location":  "Optional full vtx.<locType>.<NanoID> key of a location-domain location (e.g. a building) the studio is at. Validated alive + class=location; CreateStudio writes the studio locatedAt location link (no authZ meaning — browse reachability only). MUST be listed in ContextHint.Reads when supplied.",
			"studioKey": "Full vtx.studio.<NanoID> key of an existing studio vertex to tombstone (TombstoneStudio).",
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
					"class=location, and writes lnk.studio.<id>.locatedAt.building.<NanoID> (class locatedAt). " +
					"Rejects a dead / wrong-class location.",
			},
			{
				Name:            "TombstoneStudio — remove a studio",
				Payload:         map[string]any{"studioKey": "vtx.studio.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the studio vertex. Returns primaryKey. Rejects an absent / already-dead studio.",
			},
		},
	}
}

func studioProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     studioProfileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateStudio"},
		Description: "Studio profile aspect (wellness). Stored as vtx.studio.<NanoID>.profile (class " +
			"studioProfile) = {name}. Non-sensitive. Written by CreateStudio (whose studio vertexType DDL owns " +
			"the script); this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name": "The studio's display name.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "studio profile aspect",
				Payload:         map[string]any{"name": "Sunrise Yoga Room"},
				ExpectedOutcome: "Stored as vtx.studio.<NanoID>.profile; written by CreateStudio.",
			},
		},
	}
}

func sessionVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateSession", "TombstoneSession"},
		Description: "Wellness session DDL. Vertex shape: vtx.session.<NanoID>, class=session, root data = {} " +
			"(minimal, D5). CreateSession validates the studio is alive + class=studio, then atomically mints the " +
			"session + the .schedule aspect {name, startsAt, endsAt, capacity} + the atStudio link " +
			"(session→studio, Contract #1 §1.1 later-arriving source). The studio's booking grid is a mandatory " +
			"15-minute cadence (mirrors clinic-domain's appointment grid exactly): CreateSession discretizes " +
			"[startsAt,endsAt) into its covered 15-minute cells and CLAIMS a deterministic studioSlotClaim aspect " +
			"per cell on the studio hub (vtx.studio.<s>.slot<cellcode>) — the write-path CreateOnly/expectedRevision " +
			"conditioning on each cell key IS the double-book lock (Capability-KV §06 — the op's own Starlark " +
			"logic): a live claim on any covered cell rejects with StudioConflict (no two overlapping sessions in " +
			"the same studio). CreateSession also accepts an optional instructor param (vtx.instructor.<NanoID>, " +
			"validated alive + class=instructor): when supplied it writes the session ledBy instructor LINK " +
			"(lnk.session.<id>.ledBy.instructor.<iid>, Contract #1 §1.1 later-arriving source), beside atStudio — " +
			"omitted means the session carries no instructor (persona-worlds-design.md Fire W0). TombstoneSession " +
			"requires the session's actual studio (verified via the atStudio link) to release the held slot cells " +
			"in the same atomic batch, then soft-deletes the session (no cascade onto its bookings — they simply " +
			"drop from the wellnessBookings roster's session join). TombstoneSession's standing guard: the " +
			"operator passes unconditionally; a bound instructor may additionally cancel only a class THEY " +
			"lead — the caller supplies the instructor param and the script requires BOTH " +
			"lnk.session.<id>.ledBy.instructor.<iid> AND lnk.instructor.<iid>.identifiedBy.identity.<actor> to be " +
			"alive (known keys), rejecting AuthDenied otherwise; front-of-house staff hold no TombstoneSession " +
			"grant.",
		Script: sessionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"studio":{"type":"string","description":"vtx.studio.<NanoID> the session runs at (CreateSession; required, validated alive + class=studio; on TombstoneSession it must be the session's actual studio, validated via the atStudio link)."},` +
			`"name":{"type":"string","description":"The session's display name, e.g. Vinyasa Flow (CreateSession; required)."},` +
			`"startsAt":{"type":"string","description":"Session start, RFC3339 (CreateSession; required). Aligned to the 15-minute booking grid (:00/:15/:30/:45; SlotGridViolation otherwise)."},` +
			`"endsAt":{"type":"string","description":"Session end, RFC3339 (CreateSession; required). Aligned to the 15-minute grid; span capped at 96 cells / 24h (SessionTooLong)."},` +
			`"capacity":{"type":"integer","description":"Maximum concurrent bookings (CreateSession; required, 1..200). Bounds the seat-claim loop CreateBooking walks."},` +
			`"instructor":{"type":"string","description":"Optional vtx.instructor.<NanoID> leading the session (CreateSession; validated alive + class=instructor; writes the ledBy link). Listed in ContextHint.Reads when supplied. On TombstoneSession, required for an instructor (non-operator) caller cancelling their OWN class — validated via ledBy + identifiedBy."},` +
			`"sessionId":{"type":"string","description":"Optional bare NanoID for the new session vertex (CreateSession); absent → minted."},` +
			`"sessionKey":{"type":"string","description":"vtx.session.<NanoID> of an existing session (TombstoneSession; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.session.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"studio":     "Full vtx.studio.<NanoID> key the session runs at. CreateSession validates it is alive + class=studio, writes the atStudio link, and claims one studioSlotClaim aspect per covered 15-minute cell. TombstoneSession also requires it (the session's actual studio, validated via the atStudio link) to release the held cells.",
			"name":       "The session's display name (CreateSession; required).",
			"startsAt":   "Session start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateSession; required). Must align to the 15-minute grid (SlotGridViolation).",
			"endsAt":     "Session end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateSession; required). Must align to the 15-minute grid; span capped at 96 cells / 24h (SessionTooLong).",
			"capacity":   "Maximum concurrent bookings, an integer 1..200 (CreateSession; required). Stored on the .schedule aspect; CreateBooking reads it to bound the seat-claim loop (SessionFull once exhausted).",
			"instructor": "Optional full vtx.instructor.<NanoID> key leading the session. CreateSession validates it is alive + class=instructor and writes the ledBy link; MUST be listed in ContextHint.Reads when supplied. TombstoneSession's standing guard requires it (plus the caller's own identifiedBy binding to it) for a non-operator, instructor-role caller to cancel their own class.",
			"sessionId":  "Optional bare NanoID (no dots / key segments) for the new session vertex. Absent → minted with nanoid.new().",
			"sessionKey": "Full vtx.session.<NanoID> key of an existing session (TombstoneSession releases its held studioSlotClaim cells then tombstones it).",
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
		},
	}
}

func sessionScheduleAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionScheduleAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateSession"},
		Description: "Session schedule aspect (wellness). Stored as vtx.session.<NanoID>.schedule (class " +
			"sessionSchedule) = {name, startsAt, endsAt, capacity}. Non-sensitive. Written by CreateSession (whose " +
			"session vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. Declaration-" +
			"only: no op handler. CreateBooking reads capacity on demand (kv.Read) to bound its seat-claim loop.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string"},"startsAt":{"type":"string"},"endsAt":{"type":"string"},"capacity":{"type":"integer"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name":     "The session's display name.",
			"startsAt": "Session start (RFC3339).",
			"endsAt":   "Session end (RFC3339).",
			"capacity": "Maximum concurrent bookings (integer 1..200).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "session schedule aspect",
				Payload:         map[string]any{"name": "Vinyasa Flow", "startsAt": "2026-07-08T09:00:00Z", "endsAt": "2026-07-08T10:00:00Z", "capacity": 20},
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.schedule; written by CreateSession.",
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
		PermittedCommands: []string{"CreateSession", "TombstoneSession"},
		Description: "Studio 15-minute slot-claim aspect (wellness). Stored as vtx.studio.<NanoID>.slot<cellcode> " +
			"(class studioSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the " +
			"cell's canonical whole-second UTC start with '-'/':' stripped and lowercased. CreateSession claims " +
			"one per covered cell (CreateOnly — the key collision across two concurrent sessions for the same cell " +
			"IS the double-book lock: StudioConflict on commit-time rejection); TombstoneSession tombstones all " +
			"held cells on cancellation, freeing them. Non-sensitive; created on demand, no CreateStudio init " +
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

func bookingVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     bookingVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateBooking", "CancelBooking"},
		Description: "Wellness booking DDL. Vertex shape: vtx.booking.<NanoID>, class=booking, root data = {} " +
			"(minimal, D5). CreateBooking validates the session is alive + class=session and the booker is alive " +
			"+ class=identity, reads the session's .schedule.capacity, and claims the first free " +
			"vtx.session.<s>.seat<n> for n in 1..capacity (SessionFull once every seat is claimed) — the SAME " +
			"CreateOnly/expectedRevision write-path idiom studioSlotClaim uses, applied over an enumerated seat-" +
			"index dimension instead of a time-cell dimension (Capability-KV §06). It then atomically mints the " +
			"booking + the .status aspect {value: booked, rate, seat} + the forSession link (booking→session) + " +
			"the bookedBy link (booking→identity). Resident-rate: an optional leaseAppKey, when supplied, " +
			"qualifies for rate=resident only when ALL THREE hold: the leaseapp is alive, its .tenancy aspect " +
			"is present (CreateOnly-stamped on the leaseapp's FIRST DecideLeaseApplication approve — the only " +
			"signal an application actually became an active tenancy, not merely pending or declined), and " +
			"lnk.leaseapp.<id>.applicationFor.identity.<bookerId> is live (known-key kv.Read, the lease-signing " +
			"renewal-verification idiom) — a match writes the residentRate link (booking→leaseapp, the " +
			"ratifying audit link a future billing composition lens can walk); failing any one check is NOT a " +
			"hard failure, it falls through to rate=standard (a booker " +
			"naming a lease they don't hold never over-grants the discount, but is still allowed to book). " +
			"CancelBooking validates the booking is alive + class=booking and the supplied session is its actual " +
			"session (via the forSession link), reads the booking's own .status.seat (stored at create time — no " +
			"stored back-reference needed to recompute it), then releases that seat cell and soft-deletes the " +
			"booking.",
		Script: bookingDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"session":{"type":"string","description":"vtx.session.<NanoID> being booked (CreateBooking; required, validated alive + class=session; on CancelBooking it must be the booking's actual session, validated via the forSession link)."},` +
			`"booker":{"type":"string","description":"vtx.identity.<NanoID> making the booking (CreateBooking; required, validated alive + class=identity)."},` +
			`"leaseAppKey":{"type":"string","description":"Optional vtx.leaseapp.<NanoID> the booker claims residency under (CreateBooking; optional). Checked against the lease's applicationFor link — a mismatch falls through to the standard rate, never a hard failure."},` +
			`"bookingId":{"type":"string","description":"Optional bare NanoID for the new booking vertex (CreateBooking); absent → minted."},` +
			`"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> of an existing booking (CancelBooking; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"session":     "Full vtx.session.<NanoID> key being booked. CreateBooking validates it is alive + class=session, reads its capacity, claims a free seat, and writes the forSession link. CancelBooking also requires it (the booking's actual session, validated via the forSession link) to release the held seat.",
			"booker":      "Full vtx.identity.<NanoID> key of the person booking. CreateBooking validates it is alive + class=identity and writes the bookedBy link.",
			"leaseAppKey": "Optional full vtx.leaseapp.<NanoID> key the booker claims residency under (CreateBooking). Verified via the lease's applicationFor link before granting rate=resident; a mismatch or absent lease silently falls back to rate=standard.",
			"bookingId":   "Optional bare NanoID (no dots / key segments) for the new booking vertex. Absent → minted with nanoid.new().",
			"bookingKey":  "Full vtx.booking.<NanoID> key of an existing booking to cancel (CancelBooking).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateBooking — standard rate",
				Payload: map[string]any{"session": "vtx.session.<NanoID>", "booker": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates the session + booker are alive/typed, claims the first free seat " +
					"(SessionFull if none), and commits vtx.booking.<NanoID> (root {}) + .status {value: booked, " +
					"rate: standard, seat} + forSession + bookedBy links. Returns primaryKey.",
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
				Name:            "CancelBooking — release a seat",
				Payload:         map[string]any{"bookingKey": "vtx.booking.<NanoID>", "session": "vtx.session.<NanoID>"},
				ExpectedOutcome: "Validates the booking is alive + class=booking and the supplied session is its actual session, releases the held seat, and soft-deletes the booking. Returns primaryKey.",
			},
		},
	}
}

func instructorVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateInstructor", "TombstoneInstructor", "BindInstructorIdentity"},
		Description: "Wellness instructor DDL. Vertex shape: vtx.instructor.<NanoID>, class=instructor, root data = " +
			"{} (minimal, D5 — the data lives in the .profile aspect). CreateInstructor mints the instructor + " +
			"writes the .profile aspect {displayName (required)} atomically, and — when the optional studio param " +
			"is supplied — the instructor teachesAt studio LINK (lnk.instructor.<id>.teachesAt.studio.<sid>, class " +
			"\"teachesAt\"; source = the later-arriving instructor, target = the pre-existing studio, Contract #1 " +
			"§1.1; validated alive + class=studio). TombstoneInstructor soft-deletes one (no cascade onto sessions " +
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
			`{"displayName":{"type":"string","description":"The instructor's display name (CreateInstructor; required)."},` +
			`"studio":{"type":"string","description":"Optional vtx.studio.<NanoID> the instructor teaches at (CreateInstructor; validated alive + class=studio; writes the teachesAt link). Listed in ContextHint.Reads when supplied."},` +
			`"instructorId":{"type":"string","description":"Optional bare NanoID for the new instructor vertex (CreateInstructor); absent → minted."},` +
			`"instructorKey":{"type":"string","description":"vtx.instructor.<NanoID> of an existing instructor (TombstoneInstructor / BindInstructorIdentity; required, validated alive)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity to bind to the instructor (BindInstructorIdentity; required, validated alive + class=identity)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.instructor.<NanoID> the operation wrote (BindInstructorIdentity returns the identifiedBy link key instead)."}}}`,
		FieldDescription: map[string]string{
			"displayName":   "The instructor's display name. Stored on the .profile aspect (CreateInstructor; required).",
			"studio":        "Optional full vtx.studio.<NanoID> key the instructor teaches at. Validated alive + class=studio; CreateInstructor writes the instructor teachesAt studio link. MUST be listed in ContextHint.Reads when supplied.",
			"instructorId":  "Optional bare NanoID (no dots / key segments) for the new instructor vertex. Absent → minted with nanoid.new().",
			"instructorKey": "Full vtx.instructor.<NanoID> key of an existing instructor vertex (TombstoneInstructor tombstones it; BindInstructorIdentity binds it to a login identity).",
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
		PermittedCommands: []string{"CreateBooking"},
		Description: "Booking status aspect (wellness). Stored as vtx.booking.<NanoID>.status (class " +
			"bookingStatus) = {value: booked, rate: standard|resident, seat, booker}. Non-sensitive. Written by " +
			"CreateBooking (whose booking vertexType DDL owns the script); this aspect-type DDL is the step-6 " +
			"write gate. seat + booker are internal bookkeeping (the claimed seat index, the booker's identity " +
			"key) CancelBooking reads to recompute which vtx.session.<s>.seat<n> cell and vtx.session.<s>.bkr<b> " +
			"double-book guard to release — a single anchor each, NOT a stored relationship-as-key-list (the booker " +
			"relationship stays the bookedBy link, Contract #1). Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["booked"]},"rate":{"type":"string","enum":["standard","resident"]},"seat":{"type":"integer"},"booker":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value":  "Booking status: booked (the only value this increment writes).",
			"rate":   "standard | resident, derived by CreateBooking from the optional leaseAppKey residency check.",
			"seat":   "The claimed seat index on the session (internal bookkeeping; CancelBooking reads it to release the correct seat cell).",
			"booker": "The booker's full vtx.identity.<NanoID> key (internal bookkeeping; CancelBooking reads it to release the correct per-(session, booker) double-book guard). A single anchor, not a relationship — the bookedBy link carries the relationship.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking status aspect",
				Payload:         map[string]any{"value": "booked", "rate": "resident", "seat": 3, "booker": "vtx.identity.MQsmTTAgNkngkdEjQz9L"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.status; written by CreateBooking.",
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
		PermittedCommands: []string{"CreateBooking", "CancelBooking"},
		Description: "Session seat-claim aspect (wellness). Stored as vtx.session.<NanoID>.seat<n> (class " +
			"sessionSeatClaim) = {} — a pure existence marker, no relationship field. <n> is a 1-based seat index, " +
			"1..capacity. CreateBooking walks n=1..capacity in a bounded loop and claims the FIRST cell it reads " +
			"absent (CreateOnly — the key collision across two concurrent bookings racing for the same seat IS the " +
			"capacity lock: two callers both reading a seat absent both emit op:create for the identical key, but " +
			"CreateOnly at revision 0 commits exactly once, the loser's batch RevisionConflicts and the Processor " +
			"retries against the now-live seat). CancelBooking tombstones the ONE seat cell recorded on the " +
			"booking's own .status.seat, freeing it for a future claimant. Non-sensitive; created on demand, no " +
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

// sessionBookerClaimAspectTypeDDL declares the .bkr<bookerId> aspect (class
// sessionBookerClaim) — a deterministic per-(session, booker) existence marker
// on the session hub that enforces at-most-one live booking per booker per
// session. The exact repeatable-session uniqueness idiom cafe-domain's
// cafeOpenTabGuard uses (create-only on a lease's first tab, OCC-revived from a
// prior settled+tombstoned guard) and clinic-domain's patientSlotClaim (a
// per-actor guard aspect with a dimension-encoded localName) — here the
// dimension is the booker id. CreateBooking claims it (a second live booking by
// the same booker on the same session collides at revision 0 → DoubleBooked);
// CancelBooking tombstones it, so a later re-book OCC-revives it. Declaration-
// only; NON-sensitive; created on demand, no CreateSession init needed.
func sessionBookerClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sessionBookerClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateBooking", "CancelBooking"},
		Description: "Session booker-claim guard aspect (wellness). Stored as vtx.session.<NanoID>.bkr<bookerId> " +
			"(class sessionBookerClaim) = {} — a pure existence marker, no relationship field (the booker " +
			"relationship stays the bookedBy link, Contract #1). <bookerId> is the booking booker's bare " +
			"Contract #1 identity id, so the KEY ITSELF is the (session, booker) lock: CreateBooking claims it " +
			"CreateOnly and a SECOND live booking by the same booker on the same session collides at revision 0 " +
			"and is rejected (DoubleBooked), while a booker booking a DIFFERENT session claims a different key and " +
			"is unaffected. Repeatable, mirroring cafe-domain's cafeOpenTabGuard: CancelBooking tombstones the " +
			"guard (unconditioned — a stale-tombstone race can only free it early, re-earnable on the next book), " +
			"so a later re-book OCC-revives it from its prior revision. Non-sensitive; created on demand, no " +
			"CreateSession init needed. Declaration-only: no op handler.",
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
				ExpectedOutcome: "Stored as vtx.session.<NanoID>.bkr<bookerId>; claimed by CreateBooking, released by CancelBooking. A second live booking by the same booker on the same session is rejected.",
			},
		},
	}
}

// instructorProfileAspectTypeDDL declares the .profile aspect (class
// instructorProfile) — the step-6 write gate for CreateInstructor.
// Declaration-only; NON-sensitive.
func instructorProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     instructorProfileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateInstructor"},
		Description: "Instructor profile aspect (wellness). Stored as vtx.instructor.<NanoID>.profile (class " +
			"instructorProfile) = {displayName}. Non-sensitive. Written by CreateInstructor (whose instructor " +
			"vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. Declaration-only: no " +
			"op handler.",
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
// only — the optional location endpoint is a required declared read (state)
// when the param is supplied, never a kv.Read.
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

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

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

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateStudio":
        name = required_string(p, "name")
        sid = bare_nanoid_or_mint(p, "studioId")
        skey = "vtx.studio." + sid
        mutations = [
            make_vtx(skey, "studio", {}),
            make_aspect(skey, "profile", "studioProfile", {"name": name}),
        ]
        loc = optional_string(p, "location")
        if loc != None:
            ltype, lid = parts_of(loc, "location", "")
            require_live_typed(state, loc, "location", "location")
            mutations.append(make_link("lnk.studio." + sid + ".locatedAt." + ltype + "." + lid,
                                       skey, loc, "locatedAt", "locatedAt", {}))
        events = [{"class": "wellness.studioCreated", "data": {"studioKey": skey}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": skey}}

    if ot == "TombstoneStudio":
        skey = required_string(p, "studioKey")
        if not vertex_alive(state, skey):
            fail("UnknownStudio: " + skey)
        mutations = [make_tombstone(skey)]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": skey}}

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

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

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
WORKPLACE_ROLE_PAGE_LIMIT = 50
WORKPLACE_PARENT_PAGE_LIMIT = 20
WORKPLACE_MAX_DEPTH = 8

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
    # roles, so this is never a keyspace scan. A role granted concurrently with
    # this write is not a race worth closing: it can only widen authority, and
    # the confined branch is the safe one.
    page, _ = kv.Links(actor_key, "holdsRole", "out", None, WORKPLACE_ROLE_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key -- the role is unknown until it resolves).
        cn = kv.Read(lk.targetVertex + ".canonicalName")
        if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
            return True
    return False

def worksAt_covers(actor_id, location_key):
    # Walks the location's containedIn chain upward, testing the actor's
    # deterministic worksAt link at each level. The location itself is tested
    # first, so a staff member wired to an exact unit matches too; one wired to
    # the building matches everything containedIn it.
    cur = location_key
    for _ in range(WORKPLACE_MAX_DEPTH):
        if cur == None:
            return False
        parts = cur.split(".")
        if len(parts) != 3:
            return False
        # read-posture: (e) per-candidate follow-up read off the containedIn
        # enumeration below (data-derived key -- the ancestor chain is not
        # knowable client-side, so it cannot be pre-declared).
        lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
        if lnk != None and not lnk.isDeleted:
            return True
        # read-posture: (e) relation=containedIn epoch=none -- a location has at
        # most a few parents; containment is provisioned topology, not written
        # concurrently with this op.
        page, _ = kv.Links(cur, "containedIn", "out", None, WORKPLACE_PARENT_PAGE_LIMIT)
        nxt = None
        for lk in page:
            if not lk.isDeleted:
                nxt = lk.targetVertex
        cur = nxt
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authContextTarget != "" or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # submit with no authContext (scope=any never sets one). A scope=self caller
    # is bound instead by its own op's ownership probe (the applicationFor /
    # identifiedBy indirection): a resident legitimately holds no worksAt link,
    # and confining them by a rule written for staff would deny every
    # self-service write. The two guards are complementary, not alternatives --
    # each binds the path the other cannot see.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if op.authContextTarget != "":
        return
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def studio_locations(studio_key):
    # A session's location is where its studio sits -- the studio -locatedAt->
    # location link wellness-domain writes at CreateStudio.
    # read-posture: (e) relation=locatedAt epoch=none -- a studio sits at a
    # handful of locations at most, so this is never a keyspace scan.
    page, _ = kv.Links(studio_key, "locatedAt", "out")
    locs = []
    for lk in page:
        if not lk.isDeleted:
            locs.append(lk.targetVertex)
    return locs

GRID_MINUTES_STR = ["00", "15", "30", "45"]
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

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

def claim_cell(hub, cellcode, cls, conflict_code, who):
    key = hub + ".slot" + cellcode
    # read-posture: (d) declared optionalReads at CreateSession dispatch — an
    # absent cell is the common case (no existing booking), never a required read.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})

def require_matching_studio(sess_id, studio):
    _, studio_id = parts_of(studio, "studio", "studio")
    at_studio_lnk = "lnk.session." + sess_id + ".atStudio.studio." + studio_id
    # read-posture: (a) declared reads at TombstoneSession dispatch (validation
    # link; absence means the caller named the wrong studio — WrongStudio).
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
        if not workplace_exempt():
            require_workplace(studio_locations(studio), "cannot create a session at studio " + studio)

        name = required_string(p, "name")
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)
        capacity = required_int(p, "capacity", 1, 200)

        enforce_grid(starts_at, ends_at)
        cells = slot_cells(starts_at, ends_at)

        sess_id = bare_nanoid_or_mint(p, "sessionId")
        sess_key = "vtx.session." + sess_id

        at_studio_lnk = "lnk.session." + sess_id + ".atStudio.studio." + studio_id

        sched = {"name": name, "startsAt": starts_at, "endsAt": ends_at, "capacity": capacity}

        mutations = [
            make_vtx(sess_key, "session", {}),
            make_aspect(sess_key, "schedule", "sessionSchedule", sched),
            make_link(at_studio_lnk, sess_key, studio, "atStudio", "atStudio", {}),
        ]
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
        events = [{"class": "wellness.sessionCreated", "data": {"sessionKey": sess_key, "studio": studio}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": sess_key}}

    if ot == "TombstoneSession":
        sess_key = required_string(p, "sessionKey")
        _, sess_id = parts_of(sess_key, "sessionKey", "session")
        if not vertex_alive(state, sess_key):
            fail("UnknownSession: " + sess_key)
        cls = class_of(state, sess_key)
        if cls != "session":
            fail("WrongClass: sessionKey: " + sess_key + " has class " + str(cls) + ", required session")

        studio = required_string(p, "studio")
        require_matching_studio(sess_id, studio)

        # Standing binder: operator passes unconditionally (unchanged); a
        # bound instructor may additionally cancel only a class THEY lead —
        # the caller supplies the instructor param and BOTH the session's
        # ledBy link to it AND the caller's own identifiedBy binding to it
        # must be alive (known keys, mirroring clinic-domain's
        # actor_bound_to_provider two-hop shape). front-of-house holds no
        # TombstoneSession grant, so no workplace binder is needed here.
        if not actor_holds_operator(op.actor):
            instr_key = optional_string(p, "instructor")
            if instr_key == None:
                fail("AuthDenied: " + op.actor + " may not cancel session " + sess_key + " (no instructor supplied)")
            _, instr_id = parts_of(instr_key, "instructor", "instructor")
            _, actor_id = parts_of(op.actor, "actor", "identity")
            # read-posture: (d) declared optionalReads by TombstoneSession's
            # dispatcher for the instructor-standing path (absence is a
            # meaningful AuthDenied, not a correctness error).
            led_by = kv.Read("lnk.session." + sess_id + ".ledBy.instructor." + instr_id)
            if led_by == None or led_by.isDeleted:
                fail("AuthDenied: " + instr_key + " does not lead session " + sess_key)
            # read-posture: (d) declared optionalReads by TombstoneSession's dispatcher.
            bound = kv.Read("lnk.instructor." + instr_id + ".identifiedBy.identity." + actor_id)
            if bound == None or bound.isDeleted:
                fail("AuthDenied: " + op.actor + " is not identifiedBy-bound to instructor " + instr_key)

        # read-posture: (a) declared reads at TombstoneSession dispatch —
        # required for cell release.
        sched = kv.Read(sess_key + ".schedule")
        mutations = [make_tombstone(sess_key)]
        mutations.extend(release_cells_mutations(studio, sched))
        events = [{"class": "wellness.sessionCancelled", "data": {"sessionKey": sess_key}}]
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
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

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

def require_matching_session(book_id, session):
    _, sess_id = parts_of(session, "session", "session")
    for_session_lnk = "lnk.booking." + book_id + ".forSession.session." + sess_id
    # read-posture: (a) declared reads at CancelBooking dispatch (validation
    # link; absence means the caller named the wrong session — WrongSession).
    fs = kv.Read(for_session_lnk)
    if fs == None or fs.isDeleted:
        fail("WrongSession: session " + session + " is not the session of booking vtx.booking." + book_id)
    return sess_id

MAX_SESSION_CAPACITY = 200

def claim_first_free_seat(session_key, capacity):
    # Bounded for-range (Starlark has no while-loop) — the SAME enumerate-then-
    # CreateOnly-claim idiom as the session DDL's claim_cell, over seat indices
    # instead of time cells. kv.Read is LAZY (§2.5 idiom): it only decides which
    # candidate to claim; the safety property is the atomic batch's CreateOnly /
    # expectedRevision conditioning at commit — two callers racing for the same
    # open seat both read it absent and both emit op:create for the identical
    # key, but CreateOnly at revision 0 commits exactly once.
    for n in range(1, MAX_SESSION_CAPACITY + 1):
        if n > capacity:
            fail("SessionFull: " + session_key + " has no open seats (capacity " + str(capacity) + ")")
        seat_key = session_key + ".seat" + str(n)
        # read-posture: (d) declared optionalReads at CreateBooking dispatch
        # (first-free-seat claim; an absent seat is the common case).
        existing = kv.Read(seat_key)
        if existing == None:
            return n, make_aspect(session_key, "seat" + str(n), "sessionSeatClaim", {})
        if existing.isDeleted:
            return n, make_aspect_upsert_occ(session_key, "seat" + str(n), "sessionSeatClaim", {}, existing.revision)
    fail("SessionFull: " + session_key + " has no open seats (capacity " + str(capacity) + ")")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateBooking":
        session = required_string(p, "session")
        _, sess_id = parts_of(session, "session", "session")
        require_live_typed(state, session, "session", "session")

        booker = required_string(p, "booker")
        _, booker_id = parts_of(booker, "booker", "identity")
        require_live_typed(state, booker, "booker", "identity")

        # Consumer self-scope (scope=self grant only): step 3 authorizes via
        # authContext.target == actor (Contract #6); the payload.booker field
        # IS the identity making the booking (no patient-style indirection
        # needed here, unlike clinic-domain's CreateAppointment), so the
        # script closes the gap with a direct field compare — no extra kv.Read.
        # Empty for the standing operator grant (scope=any never sets
        # authContext), so this check is a no-op there.
        if op.authContextTarget != "" and op.authContextTarget != booker:
            fail("AuthDenied: a consumer may only book a class for themselves")

        # read-posture: (a) declared reads at CreateBooking dispatch.
        sched = kv.Read(session + ".schedule")
        if sched == None or sched.isDeleted:
            fail("InvalidState: " + session + ".schedule is missing; cannot book")
        capacity = sched.data.get("capacity")
        if capacity == None:
            fail("InvalidState: " + session + ".schedule.capacity is missing; cannot book")

        # Soft past-time guard (Capability-KV §06 — the op's own Starlark logic),
        # mirroring clinic-domain's enforce_future: the session MUST start strictly
        # after op.submittedAt, so a booking on an already-started or already-ended
        # class is rejected. submittedAt is caller-supplied (the host clock is not
        # exposed to Starlark), so this is a soft guard appropriate to the trusted
        # single-identity model. Normalize submittedAt to canonical whole-second
        # UTC (time.rfc3339_utc — pure, no clock read); startsAt is already stored
        # canonical UTC (sessionSchedule), and canonical-UTC RFC3339 compares
        # lexically == chronologically.
        starts_at = sched.data.get("startsAt")
        if starts_at == None:
            fail("InvalidState: " + session + ".schedule.startsAt is missing; cannot book")
        submitted = time.rfc3339_utc(op.submittedAt)
        if not (submitted < starts_at):
            fail("SessionInPast: session " + session + " starts at " + str(starts_at) + ", not in the future (submitted " + submitted + ")")

        # Double-book guard (Capability-KV §06): a deterministic per-(session,
        # booker) existence marker on the session hub, the SAME create-only +
        # OCC-revive idiom cafe-domain's cafeOpenTabGuard uses. The KEY alone is
        # the lock — a second LIVE booking by this booker on this session collides
        # at revision 0 (DoubleBooked); a booker booking a different session claims
        # a different key. Present+alive → reject; present+tombstoned (a prior
        # booking was cancelled and released it) → OCC-revive keyed on its own
        # revision; absent → mint fresh. read-posture: (d) declared optionalReads
        # at CreateBooking dispatch (the guard hydrates into state).
        booker_guard_local = "bkr" + booker_id
        booker_guard_key = session + "." + booker_guard_local
        if booker_guard_key in state:
            if vertex_alive(state, booker_guard_key):
                fail("DoubleBooked: " + booker + " already holds a live booking on " + session)
            booker_guard_mut = make_aspect_upsert_occ(session, booker_guard_local, "sessionBookerClaim", {}, state[booker_guard_key].revision)
        else:
            booker_guard_mut = make_aspect(session, booker_guard_local, "sessionBookerClaim", {})

        seat_n, seat_mutation = claim_first_free_seat(session, capacity)

        rate = "standard"
        resident_mutation = None
        lease_key = optional_string(p, "leaseAppKey")
        if lease_key != None:
            _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
            # read-posture: (d) declared optionalReads at CreateBooking
            # dispatch (resident-rate lookup; absent → falls through to
            # standard rate, never a hard failure).
            lease_doc = kv.Read(lease_key)
            lease_alive = lease_doc != None and not lease_doc.isDeleted
            # .tenancy is stamped CreateOnly on a leaseapp's FIRST
            # DecideLeaseApplication approve (lease-signing/scripts.go) — its
            # presence is the only signal that this application actually
            # became an active tenancy, not merely a pending or declined one.
            # Without this check a pending or declined applicant (the
            # applicationFor link stays live in both cases) would wrongly
            # qualify for the resident rate.
            # read-posture: (d) declared optionalReads at CreateBooking dispatch.
            tenancy_doc = kv.Read(lease_key + ".tenancy")
            tenancy_present = tenancy_doc != None and not tenancy_doc.isDeleted
            # read-posture: (d) declared optionalReads at CreateBooking dispatch.
            app_for_lnk = kv.Read("lnk.leaseapp." + lease_id + ".applicationFor.identity." + booker_id)
            link_live = app_for_lnk != None and not app_for_lnk.isDeleted
            if lease_alive and tenancy_present and link_live:
                rate = "resident"

        book_id = bare_nanoid_or_mint(p, "bookingId")
        book_key = "vtx.booking." + book_id

        for_session_lnk = "lnk.booking." + book_id + ".forSession.session." + sess_id
        booked_by_lnk = "lnk.booking." + book_id + ".bookedBy.identity." + booker_id

        mutations = [
            make_vtx(book_key, "booking", {}),
            make_aspect(book_key, "status", "bookingStatus", {"value": "booked", "rate": rate, "seat": seat_n, "booker": booker}),
            make_link(for_session_lnk, book_key, session, "forSession", "forSession", {}),
            make_link(booked_by_lnk, book_key, booker, "bookedBy", "bookedBy", {}),
            seat_mutation,
            booker_guard_mut,
        ]
        if rate == "resident":
            resident_rate_lnk = "lnk.booking." + book_id + ".residentRate.leaseapp." + lease_id
            mutations.append(make_link(resident_rate_lnk, book_key, lease_key, "residentRate", "residentRate", {}))

        events = [{"class": "wellness.bookingCreated", "data": {"bookingKey": book_key, "session": session, "booker": booker, "rate": rate}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

    if ot == "CancelBooking":
        book_key = required_string(p, "bookingKey")
        _, book_id = parts_of(book_key, "bookingKey", "booking")
        if not vertex_alive(state, book_key):
            fail("UnknownBooking: " + book_key)
        cls = class_of(state, book_key)
        if cls != "booking":
            fail("WrongClass: bookingKey: " + book_key + " has class " + str(cls) + ", required booking")

        session = required_string(p, "session")
        require_matching_session(book_id, session)

        # Consumer self-scope (scope=self grant only): step 3 authorizes via
        # authContext.target == actor (Contract #6), but the op's endpoint is
        # the BOOKING vertex, not an identity. The script closes the gap by
        # requiring the target identity to be THIS booking's actual bookedBy
        # identity — mirrors clinic-domain's SetAppointmentStatus self-cancel
        # guard, over the bookedBy link instead of identifiedBy. Empty for the
        # standing operator grant (scope=any never sets authContext), so this
        # check is a no-op there.
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

        # read-posture: (a) declared reads at CancelBooking dispatch.
        status = kv.Read(book_key + ".status")
        if status == None or status.isDeleted:
            fail("InvalidState: " + book_key + ".status is missing; cannot cancel")
        seat_n = status.data.get("seat")
        if seat_n == None:
            fail("InvalidState: " + book_key + ".status.seat is missing; cannot cancel")

        mutations = [
            make_tombstone(book_key),
            make_tombstone(session + ".seat" + str(seat_n)),
        ]
        # Release the per-(session, booker) double-book guard so this booker can
        # re-book the session (cafe-domain's Settle→cafeOpenTabGuard release,
        # unconditioned: a stale-tombstone race can only free it early, and the
        # guard is re-earnable on the next book). The booker is read off the
        # booking's own .status.booker (stored by CreateBooking) — CancelBooking's
        # operator path carries no authContext.target, so it cannot derive the
        # booker from authorization. A legacy booking predating this field (and
        # thus predating the guard) has no guard to release, so skip cleanly.
        booker = status.data.get("booker")
        if booker != None:
            _, guard_booker_id = parts_of(booker, "booker", "identity")
            mutations.append(make_tombstone(session + ".bkr" + guard_booker_id))
        events = [{"class": "wellness.bookingCancelled", "data": {"bookingKey": book_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": book_key}}

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
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

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
