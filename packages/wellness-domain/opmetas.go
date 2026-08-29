package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for wellness-domain's client-invocable ops — the
// three consumer (scope=self) ones, CreateBooking, JoinWaitlist and
// CancelBooking; the staff standing ones, CreateStudio, CreateSession,
// CreateSessionSeries, TombstoneStudio and CreateInstructor; and the
// provider-hat standing ones, TombstoneSession, SetBookingAttendance and
// SetInstructorProfile — mirroring clinic-domain's adoption (Fire 5 Inc 1)
// and service-domain's original RequestService op-meta.
//
// TombstoneStudio and CreateInstructor are granted `operator` alone
// (permissions.go's mk() helper — entity provisioning stays a trusted-tool
// ceremony, mirroring clinic-domain's CreateProvider/TombstoneProvider), so
// both are AuthContext "standing"; cmd/wellness-app wires real staff forms to
// both (the app-seam rule, vertical-package-standard.md §15), which is what
// makes them user-facing by demonstration despite the operator-only grant.
//
// Dispatch.Class on each entry is "booking" — the booking DDL's own
// CanonicalName (bookingVertexDDL), the Contract #2 §2.1 envelope `class`
// DDL-hint (never the vertical name "wellness" — see clinic-domain's
// opmetas.go doc comment for the regression that mistake caused).
//
// CreateBooking's booker field uses Dispatch.ContextParams ({"booker":
// "{actor}"}) rather than a user-entered field — the first real use of the
// contextParams substitution vocabulary (edge-showcase-app-design.md §3.3
// names `{actor}` as a template but no shipped op-meta had used it yet).
// This is possible here, and wasn't for clinic-domain's patient field,
// because a wellness booking's booker IS the caller's own identity directly
// (permissions.go), not a business vertex a linked identity must resolve
// through — so the caller never needs to name it, the client can just fill
// it silently from context (widget vocabulary: "dispatch.contextParams
// fields are auto-filled and hidden").
//
// CancelBooking's `session` field is the same auto-fill argument one step
// out: its value must be the booking's ACTUAL forSession target, so it is a
// value the client reads off the booking row it is displaying rather than one
// the visitor types. That is the first use of `{entity.<column>}` — the
// viewed manifest.ent row as a substitution source (edge-showcase-app-design.md
// §3.3) — filled from the `sessionKey` column edge-manifest's
// edgeEntityBookings lens projects alongside the booking.
//
// TombstoneSession's op-meta (persona-worlds-design.md Fire W0) is the
// PROVIDER-hat standing (AuthContext "standing", not "self") counterpart: a
// bound instructor cancels a class THEY lead. `instructor` is declared
// `{me.instructor}` — the same self-anchor substitution ReportIssue's
// `location` field uses for `{me.workplace}` (maintenance-domain/
// permissions.go) — since edge-manifest's edgeIdentity lens projects a
// bound instructor as a `selfAnchors` entry of type `instructor`; an
// identity with no instructor binding cannot answer it and a descriptor-
// driven client declines to offer the self-cancel path, leaving the
// operator/front-of-house surface untouched.
//
// SetBookingAttendance is the same provider-hat standing shape one entity
// deeper: the target is the BOOKING, so `instructor` is the same
// `{me.instructor}` self-anchor while `session` is the `{entity.sessionKey}`
// auto-fill CancelBooking already uses — the instructor names neither, and
// types only who showed. Its `status` field is therefore the one genuinely
// user-entered value in the whole set.
//
// ReassignSession (PROVIDER-hat standing, like TombstoneSession) covers only
// the self-service RESCHEDULE case — moving a class's time — not the
// newInstructor/clearInstructor substitute-instructor case, which stays a
// staff-console-only ceremony (cmd/wellness-app) for now: there is no
// established descriptor-vocabulary form for "pick another live instructor
// from a list" (every self-anchor idiom in this file resolves the caller's
// OWN record, never an arbitrary third party's), so exposing it here would
// mean asking a person to hand-type a vtx.instructor.<NanoID>. Unlike
// TombstoneSession's `{me.instructor}` (no `?`, so that op-meta is
// invitation-only to a bound instructor's hat, per its own doc comment
// above), `instructor` here is `{me.instructor?}` — OPTIONAL, the same
// marker CreateBooking's `leaseAppKey` uses — because ReassignSession's
// grant (permissions.go) is genuinely dual: a front-of-house viewer has no
// instructor self-anchor, so the field is silently omitted and the
// Starlark script's own else-branch (workplace confinement) answers;
// a bound instructor gets it auto-filled and is confined to a class they
// lead. One op-meta, both hats — mirroring the client-side omission
// idiom without duplicating the presentation.
func reassignSessionOpMeta() pkgmgr.OpMetaSpec {
	return pkgmgr.OpMetaSpec{
		OperationType: "ReassignSession",
		Presentation: &pkgmgr.OpPresentationSpec{
			Title:       "Reschedule class",
			Description: "Move this class to a new time.",
			Icon:        "calendar",
			Tone:        "primary",
			SubmitLabel: "Reschedule",
		},
		InputSchema: `{"type":"object","properties":` +
			`{"sessionKey":{"type":"string","description":"vtx.session.<NanoID> of the session to reschedule — auto-filled from the session being viewed."},` +
			`"studio":{"type":"string","title":"Studio","description":"vtx.studio.<NanoID> — must be the session's actual studio."},` +
			`"instructor":{"type":"string","description":"vtx.instructor.<NanoID> of your own instructor record — required when rescheduling as an instructor rather than staff."},` +
			`"startsAt":{"type":"string","format":"date-time","title":"New start","description":"The class's new start time, aligned to the 15-minute grid."},` +
			`"endsAt":{"type":"string","format":"date-time","title":"New end","description":"The class's new end time, aligned to the 15-minute grid."}},` +
			`"required":["sessionKey","studio","startsAt","endsAt"]}`,
		FieldDescriptions: map[string]string{
			"sessionKey": "The session being rescheduled — auto-filled by the client from the session being viewed (dispatch.targetField), not user-entered.",
			"studio":     "The studio this class runs at — it must be the class's own studio, so a mismatched value is rejected.",
			"instructor": "Your own instructor record — auto-filled from your identity's own instructor self-anchor when you have one. Omitted for a staff (front-of-house) caller, who is confined instead to a studio at a location they work at.",
			"startsAt":   "When the class will now start. Must align to the 15-minute grid; the studio cannot already be booked for any part of the new span.",
			"endsAt":     "When the class will now end. Must align to the 15-minute grid.",
		},
		Dispatch: &pkgmgr.OpDispatchSpec{
			Class:       sessionVertexDDL,
			AuthContext: "standing",
			TargetField: "sessionKey",
			TargetType:  sessionVertexDDL,
			// `{me.instructor?}` — see the doc comment above for why this is
			// the one OPTIONAL self-anchor in the file: it is what lets a
			// single op-meta serve both ReassignSession's grantees.
			// `{entity.studioKey}` mirrors TombstoneSession's identical field.
			ContextParams: map[string]string{
				"instructor": "{me.instructor?}",
				"studio":     "{entity.studioKey}",
			},
			// The session + its schedule are REQUIRED: the script always
			// re-reads and re-writes the schedule (carrying name/capacity
			// forward), whether or not this call reschedules — see ddls.go's
			// write-footprint comment on ReassignSession. The ledBy/
			// identifiedBy pair are the OPTIONAL ownership probes for the
			// instructor-standing path, same shape as TombstoneSession's.
			Reads: []string{
				"{payload.sessionKey}",
				"{payload.sessionKey}.schedule",
			},
			OptionalReads: []string{
				"lnk.session.{payload.sessionKey:id}.ledBy.instructor.{payload.instructor:id}",
				"lnk.instructor.{payload.instructor:id}.identifiedBy.identity.{actor:id}",
			},
		},
	}
}

// ReleaseOrphanedBooking carries none deliberately, the cafe-ledger
// CreateAccount/DebitAccount precedent: it is granted at scope=any to
// `operator` alone (permissions.go) and dispatched only by the
// wellnessOrphanedBookingSettlement Weaver target (targets.go) — no human or
// client ever submits it, so there is no presentation/inputSchema/dispatch
// surface to describe.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreateBooking",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Book a class",
				Description: "Book yourself into this session.",
				Icon:        "calendar",
				Tone:        "primary",
				SubmitLabel: "Book",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"session":{"type":"string","description":"vtx.session.<NanoID> of the session to book — auto-filled from the session being viewed."},` +
				`"leaseAppKey":{"type":"string","description":"Optional vtx.leaseapp.<NanoID> if you hold a residency you'd like the resident rate for."}},` +
				`"required":["session"]}`,
			FieldDescriptions: map[string]string{
				"session":     "The session this booking is for — auto-filled by the client from the session being viewed (dispatch.targetField), not user-entered.",
				"leaseAppKey": "Booked at the resident rate automatically when you live here.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "booking",
				AuthContext: "self",
				TargetField: "session",
				TargetType:  "session",
				// leaseAppKey is the resident-rate eligibility param, filled
				// from the booker's own lease self-anchor rather than asked for
				// as a raw vertex key. The trailing `?` OPTIONAL marker is
				// load-bearing: a booker with no lease is the DESIGNED
				// standard-rate branch, so the param is omitted silently and
				// the op stays offered — the field is never rendered either way.
				ContextParams: map[string]string{
					"booker":      "{actor}",
					"leaseAppKey": "{me.leaseapp?}",
				},
				// The session + its own .schedule + the booker's own vertex
				// are all required, live checks (prepare_booking_common's
				// require_live_typed on session/booker, plus the .schedule
				// read that rejects a booking once the class has started,
				// SessionStarted) — every CreateBooking call validates them,
				// with a clean UnknownEndpoint/InvalidState fail on absence,
				// not a designed branch.
				Reads: []string{
					"{payload.session}",
					"{payload.session}.schedule",
					"{actor}",
				},
				// The per-(session, booker) double-book guard. It must be
				// DECLARED (not merely relied on via CreateOnly-at-commit like
				// the seat claim): the script reads its current state to
				// distinguish absent (mint) from tombstoned (OCC-revive a prior
				// cancelled booking's guard) from alive (clean DoubleBooked
				// reject) — an undeclared guard would fail the re-book-after-
				// cancel path. booker = {actor} here, so {actor:id} is its bare
				// id. Absence is the common case (first book), hence optional.
				OptionalReads: []string{
					"vtx.session.{payload.session:id}.bkr{actor:id}",
				},
			},
		},
		{
			// JoinWaitlist mirrors CreateBooking's op-meta shape exactly —
			// same self-anchored booker, same optional resident-rate lease,
			// same declared booker-guard optionalRead (the guard is shared
			// between the two ops, ddls.go's sessionBookerClaim) — only the
			// presentation strings and title differ, since the offered
			// affordance is "join the waitlist" not "book".
			OperationType: "JoinWaitlist",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Join the waitlist",
				Description: "This class is full — join the waitlist and get the next open seat automatically.",
				Icon:        "calendar",
				Tone:        "secondary",
				SubmitLabel: "Join waitlist",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"session":{"type":"string","description":"vtx.session.<NanoID> of the session to waitlist for — auto-filled from the session being viewed."},` +
				`"leaseAppKey":{"type":"string","description":"Optional vtx.leaseapp.<NanoID> if you hold a residency you'd like the resident rate for once promoted."}},` +
				`"required":["session"]}`,
			FieldDescriptions: map[string]string{
				"session":     "The session to join the waitlist for — auto-filled by the client from the session being viewed (dispatch.targetField), not user-entered.",
				"leaseAppKey": "Booked at the resident rate automatically when you live here, once you're promoted off the waitlist.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "booking",
				AuthContext: "self",
				TargetField: "session",
				TargetType:  "session",
				ContextParams: map[string]string{
					"booker":      "{actor}",
					"leaseAppKey": "{me.leaseapp?}",
				},
				// Same shape and rationale as CreateBooking's above — the two
				// ops share prepare_booking_common.
				Reads: []string{
					"{payload.session}",
					"{payload.session}.schedule",
					"{actor}",
				},
				OptionalReads: []string{
					"vtx.session.{payload.session:id}.bkr{actor:id}",
				},
			},
		},
		{
			OperationType: "CancelBooking",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Cancel booking",
				Description: "Cancel this booking and release your seat. Only available before the class begins and before attendance is recorded.",
				Icon:        "cancel",
				Tone:        "destructive",
				SubmitLabel: "Cancel booking",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> of the booking to cancel — auto-filled from the booking being viewed."},` +
				`"session":{"type":"string","description":"vtx.session.<NanoID> — must be the booking's actual session."}},` +
				`"required":["bookingKey","session"]}`,
			FieldDescriptions: map[string]string{
				"bookingKey": "The booking being cancelled — auto-filled by the client from the booking being viewed (dispatch.targetField), not user-entered.",
				"session":    "Must match the booking's actual forSession link — a client renders this from the booking record it already loaded, not a user-entered value.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "booking",
				AuthContext: "self",
				TargetField: "bookingKey",
				TargetType:  "booking",
				// The booking's session is not the visitor's to type: it must
				// be the booking's ACTUAL forSession target (the script
				// rebuilds the seat-cell key from it and validates it against
				// the link), so the client fills it from the booking row it is
				// already displaying — the manifest.ent `sessionKey` column
				// edge-manifest's edgeEntityBookings lens projects.
				ContextParams: map[string]string{"session": "{entity.sessionKey}"},
				// The booking's own .status aspect is REQUIRED, not optional:
				// the script reads the seat index it carries to rebuild the
				// seat cell it releases, so its absence is a correctness
				// error. The session's .schedule is required for the same
				// reason — the script rejects once the class has begun
				// (SessionStarted, the mirror of SetBookingAttendance's
				// SessionNotStarted requirement below). The targetField
				// fallback declares the booking vertex but never its aspects.
				Reads: []string{
					"{payload.bookingKey}",
					"{payload.bookingKey}.status",
					"{payload.session}.schedule",
				},
				// The session-match and self-scope ownership probes. Absence
				// of either is a meaningful rejection the script renders
				// (WrongSession / AuthDenied), not a correctness error — the
				// same shape cafe-domain's Settle uses for its applicationFor
				// ownership probe.
				OptionalReads: []string{
					"lnk.booking.{payload.bookingKey:id}.forSession.session.{payload.session:id}",
					"lnk.booking.{payload.bookingKey:id}.bookedBy.identity.{actor:id}",
				},
			},
		},
		{
			OperationType: "TombstoneSession",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Cancel class",
				Description: "Cancel this class.",
				Icon:        "cancel",
				Tone:        "destructive",
				SubmitLabel: "Cancel class",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"sessionKey":{"type":"string","description":"vtx.session.<NanoID> of the session to cancel — auto-filled from the session being viewed."},` +
				`"studio":{"type":"string","title":"Studio","description":"vtx.studio.<NanoID> — must be the session's actual studio."},` +
				`"instructor":{"type":"string","description":"vtx.instructor.<NanoID> of your own instructor record — required when cancelling as an instructor rather than staff."}},` +
				`"required":["sessionKey","studio"]}`,
			FieldDescriptions: map[string]string{
				"sessionKey": "The session being cancelled — auto-filled by the client from the session being viewed (dispatch.targetField), not user-entered.",
				"studio":     "The studio this class runs at — it must be the class's own studio, so a mismatched value is rejected.",
				"instructor": "Your own instructor record — auto-filled from your identity's own instructor self-anchor. Required when cancelling as an instructor (a class you lead); staff cancel with no instructor field.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "session",
				AuthContext: "standing",
				TargetField: "sessionKey",
				TargetType:  "session",
				// `{me.instructor}` addresses the `instructor` selfAnchor
				// edgeIdentity's edge-manifest lens projects for a bound
				// instructor (Fire W0) — the exact vocabulary ReportIssue's
				// `{me.workplace}` proves (maintenance-domain/permissions.go).
				// `{entity.studioKey}` fills the studio from the session row
				// being viewed (edge-manifest projects the column), so the
				// one value only the machine knows is never asked of the
				// instructor.
				ContextParams: map[string]string{
					"instructor": "{me.instructor}",
					"studio":     "{entity.studioKey}",
				},
				// The ledBy and identifiedBy ownership probes. Absence of
				// either is a meaningful rejection the script renders
				// (AuthDenied), not a correctness error — the same shape
				// CancelBooking's OptionalReads use above.
				OptionalReads: []string{
					"lnk.session.{payload.sessionKey:id}.ledBy.instructor.{payload.instructor:id}",
					"lnk.instructor.{payload.instructor:id}.identifiedBy.identity.{actor:id}",
				},
			},
		},
		reassignSessionOpMeta(),
		{
			OperationType: "SetBookingAttendance",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record attendance",
				Description: "Mark whether this member showed up for the class.",
				Icon:        "check",
				Tone:        "primary",
				SubmitLabel: "Record",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> being marked — auto-filled from the booking being viewed."},` +
				`"session":{"type":"string","description":"vtx.session.<NanoID> — must be the booking's actual session."},` +
				`"status":{"type":"string","title":"Attendance","enum":["attended","noShow"],"enumLabels":{"attended":"Attended","noShow":"No-show"},"description":"Whether the member showed up."},` +
				`"instructor":{"type":"string","description":"vtx.instructor.<NanoID> of your own instructor record — required when marking as an instructor rather than staff."}},` +
				`"required":["bookingKey","session","status"]}`,
			FieldDescriptions: map[string]string{
				"bookingKey": "The booking being marked — auto-filled by the client from the booking being viewed (dispatch.targetField), not user-entered.",
				"session":    "Must match the booking's actual forSession link — a client renders this from the booking record it already loaded.",
				"status":     "Did the member show up? Either answer corrects the other, so a mistaken mark can be restated.",
				"instructor": "Your own instructor record — auto-filled from your identity's own instructor self-anchor. Required when marking as an instructor (a class you lead); staff mark with no instructor field.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "booking",
				AuthContext: "standing",
				TargetField: "bookingKey",
				TargetType:  "booking",
				ContextParams: map[string]string{
					"session":    "{entity.sessionKey}",
					"instructor": "{me.instructor}",
				},
				// The booking's own .status is REQUIRED: the script carries its
				// rate / seat / booker forward onto the attendance write, so its
				// absence is a correctness error, not a rejection. The session's
				// .schedule is required for the same reason — attendance before
				// the class begins is SessionNotStarted, and the start time is
				// what answers that. The targetField fallback declares the
				// booking vertex but never its aspects.
				Reads: []string{
					"{payload.bookingKey}",
					"{payload.bookingKey}.status",
					"{payload.session}.schedule",
				},
				// The session-match and the two ownership probes. Absence of any
				// is a meaningful rejection the script renders (WrongSession /
				// AuthDenied), not a correctness error — the same shape
				// CancelBooking and TombstoneSession use above.
				OptionalReads: []string{
					"lnk.booking.{payload.bookingKey:id}.forSession.session.{payload.session:id}",
					"lnk.session.{payload.session:id}.ledBy.instructor.{payload.instructor:id}",
					"lnk.instructor.{payload.instructor:id}.identifiedBy.identity.{actor:id}",
				},
			},
		},
		{
			OperationType: "CreateSession",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Schedule a class",
				Description: "Put a class on a studio's grid.",
				Icon:        "calendar",
				Tone:        "primary",
				SubmitLabel: "Schedule class",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"studio":{"type":"string","description":"vtx.studio.<NanoID> the class runs at — auto-filled from the studio being viewed."},` +
				`"name":{"type":"string","title":"Name","description":"What the class is called, e.g. Vinyasa Flow."},` +
				`"startsAt":{"type":"string","format":"date-time","title":"Starts","description":"Class start, aligned to the 15-minute grid."},` +
				`"endsAt":{"type":"string","format":"date-time","title":"Ends","description":"Class end, aligned to the 15-minute grid."},` +
				`"capacity":{"type":"integer","title":"Capacity","minimum":1,"maximum":200,"description":"How many people may book a seat."},` +
				`"instructor":{"type":"string","title":"Instructor","description":"vtx.instructor.<NanoID> leading the class."}},` +
				`"required":["studio","name","startsAt","endsAt","capacity"]}`,
			FieldDescriptions: map[string]string{
				"studio":     "The studio the class runs in — auto-filled by the client from the studio being viewed (dispatch.targetField), not user-entered. A front-desk caller may only schedule at a studio in a building they work at.",
				"name":       "What the class is called, as members will see it.",
				"startsAt":   "When the class starts. Must align to the 15-minute grid; the studio cannot already be booked for any part of the span.",
				"endsAt":     "When the class ends. Must align to the 15-minute grid and be at most 24 hours after the start.",
				"capacity":   "How many seats the class has, 1 to 200.",
				"instructor": "Optional. The instructor leading the class. Omitted leaves the class unassigned.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       sessionVertexDDL,
				AuthContext: "standing",
				TargetField: "studio",
				TargetType:  studioVertexDDL,
				// The instructor is validated alive + typed only when supplied,
				// so it is optional on both counts. The studio comes free with
				// the targetField fallback.
				//
				// The per-cell slot claims are NOT declared here, and this
				// Dispatch.OptionalReads template vocabulary still has no form
				// for them: the script reads one guard key per 15-minute cell
				// the class covers (up to 96), each derived from startsAt/
				// endsAt by arithmetic the template substitutes values but
				// never computes. That no longer matters for EITHER caller
				// shape, though: ddls.go's own derive_reads(op) computes the
				// full set server-side from this same payload (Contract #2
				// §2.5 class (g)), unconditionally, before any caller's
				// declaration is even consulted — so every caller gets the
				// script's own StudioConflict/InstructorConflict on collision,
				// never a bare commit-time RevisionConflict.
				OptionalReads: []string{"{payload.instructor}"},
			},
		},
		{
			OperationType: "CreateSessionSeries",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Schedule a recurring class",
				Description: "Put a whole run of the same class on a studio's grid at once — weekly, biweekly, whatever the cadence.",
				Icon:        "repeat",
				Tone:        "primary",
				SubmitLabel: "Schedule series",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"studio":{"type":"string","description":"vtx.studio.<NanoID> every occurrence runs at — auto-filled from the studio being viewed."},` +
				`"name":{"type":"string","title":"Name","description":"What every occurrence is called, e.g. Vinyasa Flow."},` +
				`"startsAt":{"type":"string","format":"date-time","title":"First class starts","description":"First occurrence's start, aligned to the 15-minute grid."},` +
				`"endsAt":{"type":"string","format":"date-time","title":"First class ends","description":"First occurrence's end, aligned to the 15-minute grid."},` +
				`"capacity":{"type":"integer","title":"Capacity","minimum":1,"maximum":200,"description":"How many people may book a seat, shared by every occurrence."},` +
				`"intervalDays":{"type":"integer","title":"Repeat every (days)","minimum":1,"maximum":365,"description":"Days between occurrences — 7 for weekly."},` +
				`"occurrenceCount":{"type":"integer","title":"Number of occurrences","minimum":2,"maximum":52,"description":"How many classes to schedule, first included."},` +
				`"instructor":{"type":"string","title":"Instructor","description":"vtx.instructor.<NanoID> leading every occurrence."}},` +
				`"required":["studio","name","startsAt","endsAt","capacity","intervalDays","occurrenceCount"]}`,
			FieldDescriptions: map[string]string{
				"studio":          "The studio every occurrence runs in — auto-filled by the client from the studio being viewed (dispatch.targetField), not user-entered. A front-desk caller may only schedule at a studio in a building they work at.",
				"name":            "What every occurrence is called, as members will see it.",
				"startsAt":        "When the first occurrence starts. Must align to the 15-minute grid; the studio cannot already be booked for any part of ANY occurrence's span.",
				"endsAt":          "When the first occurrence ends. Must align to the 15-minute grid and be at most 24 hours after the start.",
				"capacity":        "How many seats each occurrence has, 1 to 200, shared by the whole series.",
				"intervalDays":    "How many days apart each occurrence falls — 7 for a weekly class, 14 for biweekly.",
				"occurrenceCount": "How many occurrences to schedule at once, first included (2 to 52). For a single class, use Schedule a class instead.",
				"instructor":      "Optional. The instructor leading every occurrence. Omitted leaves the series unassigned.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       sessionSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "studio",
				TargetType:  studioVertexDDL,
				// Same undeclarable-template shape as CreateSession's own
				// op-meta, multiplied by occurrenceCount: the per-cell slot
				// claims of EVERY occurrence are data-derived from startsAt/
				// endsAt/intervalDays by arithmetic this Dispatch.OptionalReads
				// template vocabulary has no form for. ddls.go's own
				// derive_reads(op) computes the whole per-occurrence set
				// server-side instead, for every caller shape alike (Contract
				// #2 §2.5 class (g)) — see CreateSession's op-meta comment
				// above.
				OptionalReads: []string{"{payload.instructor}"},
			},
		},
		{
			OperationType: "CreateStudio",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open a studio",
				Description: "Add a studio at the building you work at.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Open studio",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","title":"Name","description":"What the studio is called, e.g. Flow Room."},` +
				`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the building it sits in — auto-filled from where you work."}},` +
				`"required":["name","location"]}`,
			FieldDescriptions: map[string]string{
				"name":     "What the studio is called, as members will see it on the schedule.",
				"location": "The building the studio sits in — filled by the client from where you work, not typed.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       studioVertexDDL,
				AuthContext: "standing",
				// No TargetField/TargetType, and `location` is the submitter's
				// OWN workplace — the same shape maintenance-domain's
				// ReportIssue takes, and for the same two reasons. No entity
				// lens projects places as browsable rows, so declaring a
				// targetType would make the op permanently unresolvable; and
				// the location a staffer may open a studio at is precisely the
				// one they worksAt, which edgeIdentity already projects as the
				// `workplace` selfAnchor. An identity with no workplace cannot
				// answer it and the client declines to offer the op — the
				// client-side mirror of the script's own denial, where an empty
				// candidate list confines everyone but an operator.
				//
				// `location` is REQUIRED here even though the script treats the
				// payload field as optional: an unlocated studio is an operator
				// ceremony (nothing but the operator escape can reach it), so
				// offering it on a descriptor-driven staff form would only ever
				// render a control that fails closed.
				ContextParams: map[string]string{"location": "{me.workplace}"},
				Reads:         []string{"{payload.location}"},
			},
		},
		{
			OperationType: "TombstoneStudio",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Remove studio",
				Description: "Remove a studio. Does not cascade onto its sessions or bookings.",
				Icon:        "building",
				Tone:        "destructive",
				SubmitLabel: "Remove studio",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"studioKey":{"type":"string","description":"vtx.studio.<NanoID> of the studio to remove — auto-filled from the studio being viewed."}},` +
				`"required":["studioKey"]}`,
			FieldDescriptions: map[string]string{
				"studioKey": "The studio being removed — auto-filled by the client from the studio being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       studioVertexDDL,
				AuthContext: "standing",
				TargetField: "studioKey",
				TargetType:  studioVertexDDL,
				Reads:       []string{"{payload.studioKey}"},
			},
		},
		{
			OperationType: "CreateInstructor",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Register instructor",
				Description: "Add a new instructor.",
				Icon:        "user-plus",
				Tone:        "primary",
				SubmitLabel: "Register",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"displayName":{"type":"string","title":"Name","description":"The instructor's display name."},` +
				`"studio":{"type":"string","title":"Studio","description":"Optional vtx.studio.<NanoID> the instructor teaches at."}},` +
				`"required":["displayName"]}`,
			FieldDescriptions: map[string]string{
				"displayName": "The instructor's display name, as members will see it on the class list.",
				"studio":      "Optional. A studio this instructor teaches at — writes the teachesAt link. Leave blank to register the instructor unassigned.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       instructorVertexDDL,
				AuthContext: "standing",
				// Mints the instructor — no pre-existing vertex for a client to
				// derive a target from (clinic-domain's CreateProvider idiom).
				// studio is validated alive+typed (require_live_typed) only when
				// supplied — a whole-entry optionalRead, never a required one,
				// mirroring clinic-domain's CreatePatient identityKey.
				OptionalReads: []string{"{payload.studio}"},
			},
		},
		{
			// The instructor hat's record-administering op, the counterpart to
			// clinic-domain's SetProviderHours. Facet's hatOps filter
			// (cmd/facet/web/app.js) offers an op on an anchor only when BOTH
			// dispatchClass AND dispatchTargetType equal the anchor's type, so
			// both are instructorVertexDDL — which is what makes this op
			// reachable from a bound instructor's own hat.
			//
			// It is self-service where clinic-domain's SetProviderProfile is
			// operator-only, and the distinction is deliberate: that aspect
			// carries a clinician's specialty and credentials, which nobody
			// self-attests. This one carries a display name.
			OperationType: "SetInstructorProfile",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Edit my profile",
				Description: "Change the name members see on your classes.",
				Icon:        "user",
				Tone:        "primary",
				SubmitLabel: "Save profile",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"instructorKey":{"type":"string","description":"vtx.instructor.<NanoID> of the instructor record being edited — auto-filled from the instructor being viewed."},` +
				`"displayName":{"type":"string","title":"Name","description":"Your professional display name, as members see it on the class list."}},` +
				`"required":["instructorKey","displayName"]}`,
			FieldDescriptions: map[string]string{
				"instructorKey": "The instructor record being edited — auto-filled by the client from the instructor being viewed (dispatch.targetField), not user-entered.",
				"displayName":   "Your professional display name. Members see it on every class you lead, and the staff scheduling form lists you by it. Required — the profile is replaced wholesale, so there is no way to clear it.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       instructorVertexDDL,
				AuthContext: "standing",
				TargetField: "instructorKey",
				TargetType:  instructorVertexDDL,
				// The instructor being edited itself: the script's
				// vertex_alive/class_of pair (ddls.go's SetInstructorProfile
				// branch) reads it out of `state`. Declared explicitly here
				// per Contract #2 section 2.5's read posture ("declared, not
				// lazy") rather than left to a client's own fallback --
				// internal/descriptorform/form.mjs independently auto-pushes
				// a resolved targetField value onto `reads` (mirroring
				// cmd/facet/web/app.js's identical fallback), so a
				// descriptor-driven client was never actually broken by this
				// being undeclared; the explicit declaration here is the
				// correct posture regardless of any client-side safety net,
				// and is what a NON-descriptor-driven submitter (one that
				// resolves reads straight off this Dispatch block with no
				// fallback of its own) needs to see.
				Reads: []string{"{payload.instructorKey}"},
				// The standing guard's own-binding probe, declared rather than
				// left live (Contract #2 §2.5): absence is a meaningful
				// rejection the script renders as AuthDenied, not a correctness
				// error, so it is an optionalRead — the same shape
				// service-domain's RecordServiceOutcome uses for its
				// serviceprovider→identity hop. Declared here deliberately:
				// clinic's SetProviderHours leaves the equivalent probe
				// undeclared, and that is the class-(b) debt not to copy.
				OptionalReads: []string{
					"lnk.instructor.{payload.instructorKey:id}.identifiedBy.identity.{actor:id}",
				},
			},
		},
	}
}
