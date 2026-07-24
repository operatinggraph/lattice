package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for wellness-domain's two consumer-invocable
// (scope=self) ops — CreateBooking and CancelBooking — mirroring clinic-
// domain's adoption (Fire 5 Inc 1) and service-domain's original RequestService
// op-meta.
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
				"leaseAppKey": "Optional — your own lease application, if you have one. When it names you as the applicant, your booking gets the resident rate; otherwise you still book, at the standard rate.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:         "booking",
				AuthContext:   "self",
				TargetField:   "session",
				TargetType:    "session",
				ContextParams: map[string]string{"booker": "{actor}"},
			},
		},
		{
			OperationType: "CancelBooking",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Cancel booking",
				Description: "Cancel this booking and release your seat.",
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
				// error. The targetField fallback declares the booking vertex
				// but never its aspects.
				Reads: []string{"{payload.bookingKey}", "{payload.bookingKey}.status"},
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
				`"studio":{"type":"string","description":"vtx.studio.<NanoID> — must be the session's actual studio."},` +
				`"instructor":{"type":"string","description":"vtx.instructor.<NanoID> of your own instructor record — required when cancelling as an instructor rather than staff."}},` +
				`"required":["sessionKey","studio"]}`,
			FieldDescriptions: map[string]string{
				"sessionKey": "The session being cancelled — auto-filled by the client from the session being viewed (dispatch.targetField), not user-entered.",
				"studio":     "Must match the session's actual atStudio link — a client renders this from the session row it already loaded.",
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
				ContextParams: map[string]string{"instructor": "{me.instructor}"},
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
	}
}
