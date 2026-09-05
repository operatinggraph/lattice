package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for clinic-domain's three consumer-invocable
// (scope=self) ops — CreateAppointment, RescheduleAppointment, and
// SetAppointmentStatus — mirroring service-domain's RequestService op-meta,
// the only other package to adopt the vocabulary so far (Fire 5,
// edge-showcase-app-design.md §7 "adoption across clinic/café/wellness
// consumer-shaped ops") — plus SetProviderHours, SetProviderTimeOff, and
// RecordEncounter, the PROVIDER-hat standing (AuthContext "standing", not
// "self") ops persona-worlds-design.md Fire W0 adds: granted-but-meta-less
// ops are invisible to a descriptor-driven client (forOperation links mint
// only with a meta), so a bound provider's own availability/time-off/
// documentation ops need one too.
//
// Each InputSchema below is the narrow, consumer/provider-facing slice of the
// DDL's full merged schema (appointmentVertexTypeDDL's / providerVertexTypeDDL's
// InputSchema) — the fields a self-service or staff caller actually supplies,
// not the true operator-only ones (site on CreateAppointment). CreateAppointment
// and SetAppointmentStatus describe the FULL patient-or-staff reachable
// surface, not just the self-scope grant's slice — `{me.patient?}` on
// CreateAppointment and the widened `status` enum on SetAppointmentStatus both
// mirror wellness-domain's ReassignSession OPTIONAL-self-anchor idiom: one
// op-meta serves both hats, and the actual authority stays entirely in-script
// (workplace confinement + the self-scope status=cancelled restriction) —
// widening the op-meta only makes it stop underselling what the script already
// permits. The trusted admin tool still calls SetProviderHours/SetProviderTimeOff/
// etc. directly for its true operator-only surface (no descriptor needed there).
//
// Adding these op-metas does not by itself make the ops Facet-visible: the
// edge-manifest catalog lens (edgeCatalogSpec) only reaches an op-meta via a
// service template's permitsOperation link, and no clinic service template
// exists yet (clinic-domain has no service-domain integration). That
// catalog-path wiring — a clinic "book an appointment" service template,
// `availableAt` a clinic building, `permitsOperation`-linked to these op-metas
// — is the named next increment; this one lands the metadata layer so that
// wiring has descriptors to link to.
//
// Dispatch.Class on each entry is the owning vertexType DDL's own
// CanonicalName ("appointment" for the four appointment ops, "provider" for
// SetProviderHours/SetProviderTimeOff — providerVertexDDL), the Contract #2
// §2.1 envelope `class` DDL-hint (mirrors service-domain's RequestService
// op-meta doc comment: Dispatch.Class = the owning DDL's CanonicalName, never
// the vertical name).
//
// CreatePatient is the third AuthContext "standing" entry and the only one that
// names no TargetField: it MINTS the patient, so there is no pre-existing vertex
// for a client to derive the field from. Its optional identityKey is the one
// read it declares — absence-tolerant on both counts, since the field itself is
// optional and the patientClaim guard it probes is absent on every first bind.
//
// CreateProvider / SetProviderProfile / SetSiteProfile / AssignProviderSite /
// RemoveProviderSite are all operator-only (permissions.go's mk() helper —
// scope=any, GrantsTo: [operator], no consumer/provider/frontOfHouse row), so
// each is AuthContext "standing" (the same posture as SetProviderHours/
// SetProviderTimeOff above): the authority is the operator ROLE, not a
// relationship to the target.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreateAppointment",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Book appointment",
				Description: "Book an appointment with a provider.",
				Icon:        "calendar",
				Tone:        "primary",
				SubmitLabel: "Book",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"patient":{"type":"string","title":"Patient","description":"vtx.patient.<NanoID> — your own patient record if you're a patient, or the patient to book for if you're front-desk/provider staff."},` +
				`"provider":{"type":"string","description":"vtx.provider.<NanoID> of the provider to book with — auto-filled from the provider being viewed."},` +
				`"startsAt":{"type":"string","format":"date-time","title":"Starts","description":"Appointment start, aligned to the 15-minute booking grid."},` +
				`"endsAt":{"type":"string","format":"date-time","title":"Ends","description":"Appointment end, aligned to the 15-minute booking grid."},` +
				`"reason":{"type":"string","title":"Reason","description":"Optional visit reason."}},` +
				`"required":["patient","provider","startsAt","endsAt"]}`,
			FieldDescriptions: map[string]string{
				"patient":  "Your own patient record — auto-filled from your identity's own patient self-anchor when you're a patient. Front-desk/provider staff select any patient at their workplace here.",
				"provider": "The provider this appointment is with — auto-filled by the client from the provider being viewed (dispatch.targetField), not user-entered.",
				"startsAt": "When the appointment starts. Must land in the future and align to the clinic's 15-minute grid.",
				"endsAt":   "When the appointment ends. Must align to the 15-minute grid; span capped at 24 hours.",
				"reason":   "Optional visit reason / chief complaint.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "provider",
				TargetType:  "provider",
				// `{me.patient?}` — the ReassignSession/TombstoneSession
				// pattern: OPTIONAL, so this one op-meta serves both grantees.
				// A patient-self actor gets `patient` auto-filled/overridden
				// from their own selfAnchor (identical to the old hard
				// `{me.patient}` bind). A front-desk/provider actor has no
				// patient selfAnchor, so the binding is silently skipped and
				// the payload's own explicit `patient` value (the staff
				// caller's patient picker) passes through untouched — the
				// script's workplace-confinement branch (ddls.go
				// enforce_workplace_confined on CreateAppointment) is what
				// actually authorizes that path, exactly as it already does
				// live; the op-meta was previously underselling it.
				ContextParams: map[string]string{"patient": "{me.patient?}"},
				// Both endpoints are required, live, class-checked reads
				// (appointmentDDLScript's require_live_typed on patient and
				// provider) — every CreateAppointment call validates them
				// before minting.
				Reads: []string{"{payload.patient}", "{payload.provider}"},
				// The self-scope ownership probe (does op.authContextTarget —
				// which step 3 requires to equal op.actor for a scope=self
				// grant — identify this patient?). {actor:id} substitutes the
				// identical value {scopedTo:id} would here, without exercising
				// that placeholder's first-ever use in the corpus. Absence is a
				// meaningful rejection the script renders (AuthDenied), not a
				// correctness error.
				OptionalReads: []string{"lnk.patient.{payload.patient:id}.identifiedBy.identity.{actor:id}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "RescheduleAppointment",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Reschedule appointment",
				Description: "Move your appointment to a new time.",
				Icon:        "calendar",
				Tone:        "primary",
				SubmitLabel: "Reschedule",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment to reschedule — auto-filled from the appointment being viewed."},` +
				`"provider":{"type":"string","title":"Provider","x-entityRef":"provider","description":"vtx.provider.<NanoID> — must be the appointment's actual provider."},` +
				`"patient":{"type":"string","title":"Patient","description":"vtx.patient.<NanoID> — must be the appointment's actual patient."},` +
				`"startsAt":{"type":"string","format":"date-time","title":"New start","description":"New start, aligned to the 15-minute booking grid."},` +
				`"endsAt":{"type":"string","format":"date-time","title":"New end","description":"New end, aligned to the 15-minute booking grid."},` +
				`"reason":{"type":"string","title":"Reason","description":"Optional visit reason; omitted clears the existing one."}},` +
				`"required":["appointmentKey","provider","patient","startsAt","endsAt"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being rescheduled — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"provider":       "The provider the appointment is with — a rescheduled appointment keeps its provider, so a different one is rejected.",
				"patient":        "The appointment's own patient — you can only reschedule your own appointment.",
				"startsAt":       "The new start time. Must land in the future and align to the 15-minute grid.",
				"endsAt":         "The new end time. Must align to the 15-minute grid; span capped at 24 hours.",
				"reason":         "Optional visit reason. Omitted clears the appointment's existing reason.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
				// The appointment's own .schedule is REQUIRED: the script reads
				// it (appointmentDDLScript's "read-posture: (a)" old_sched read)
				// to know which cells the move releases, and faults InvalidState
				// on its absence rather than rendering a business rejection. The
				// provider/patient match probes are the SAME "(a)" required class
				// (require_matching_provider/require_matching_patient's own doc
				// comments name RescheduleAppointment's dispatcher explicitly) —
				// every reschedule recomputes the held cells against them, so
				// they are never absence-tolerant here the way CancelBooking's
				// analogous probes are.
				Reads: []string{
					"{payload.appointmentKey}",
					"{payload.appointmentKey}.schedule",
					"lnk.appointment.{payload.appointmentKey:id}.withProvider.provider.{payload.provider:id}",
					"lnk.appointment.{payload.appointmentKey:id}.forPatient.patient.{payload.patient:id}",
				},
				// The self-scope ownership probe, same shape and rationale as
				// CreateAppointment's above.
				OptionalReads: []string{"lnk.patient.{payload.patient:id}.identifiedBy.identity.{actor:id}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetAppointmentStatus",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Update status",
				Description: "Set this appointment's status.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Save",
			},
			// The op-meta now describes the FULL script-reachable status
			// surface, not just the self-scope grant's slice — mirroring
			// CreateAppointment's `{me.patient?}` widening above. The actual
			// authority stays entirely in-script (appointmentDDLScript's
			// SetAppointmentStatus branch): a self-scoped caller
			// (op.authContextTarget set) is rejected with AuthDenied for any
			// status but "cancelled"; a staff/operator caller may set any of
			// APPOINTMENT_STATUSES, workplace-confined to their own site. A
			// descriptor client (Facet) renders the same widened form for
			// every actor; the non-cancel options simply AuthDeny for a
			// patient, same as attempting them via any other client already
			// does today.
			// provider/patient are required by this schema unconditionally,
			// though the script only reads them on a terminal transition
			// (appointmentDDLScript's SetAppointmentStatus terminal branch,
			// to release the held slot-claim cells) — both are cheap,
			// already-known values (auto-filled from the appointment being
			// viewed), so requiring them up front costs nothing and keeps one
			// schema for every transition. RescheduleAppointment, in this
			// same package, requires the identical pair for the identical
			// reason.
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment — auto-filled from the appointment being viewed."},` +
				`"status":{"type":"string","title":"Status","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"],"default":"cancelled","description":"The appointment's new status. Self-service patients may only cancel; front-desk/provider staff may set any status."},` +
				`"provider":{"type":"string","description":"vtx.provider.<NanoID> — must be the appointment's actual provider. Required to release the appointment's held slot-claim cells on a terminal transition."},` +
				`"patient":{"type":"string","description":"vtx.patient.<NanoID> — must be the appointment's actual patient. Required to release the appointment's held slot-claim cells on a terminal transition."},` +
				`"note":{"type":"string","title":"Note","description":"Optional status note (e.g. cancellation or no-show reason)."}},` +
				`"required":["appointmentKey","status","provider","patient"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being updated — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"status":         "The new status. A self-service patient may only cancel (the script enforces this); front-desk/provider staff may set any status.",
				"provider":       "The appointment's own provider — auto-filled by the client from the appointment being viewed, not user-entered. Must be the appointment's actual provider.",
				"patient":        "The appointment's own patient — auto-filled by the client from the appointment being viewed, not user-entered. Must be the appointment's actual patient.",
				"note":           "Optional status note, kept with the appointment.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
				// The current .status is OPTIONAL: absence is the legit
				// first-set case (appointmentDDLScript's own "read-posture: (d)"
				// comment on this exact key). The provider/patient match probes
				// are REQUIRED — the SAME "(a)" class RescheduleAppointment's
				// identical probes use, verified against
				// require_matching_provider/require_matching_patient's own doc
				// comments, which name SetAppointmentStatus's terminal branch
				// explicitly alongside RescheduleAppointment.
				Reads: []string{
					"{payload.appointmentKey}",
					"lnk.appointment.{payload.appointmentKey:id}.withProvider.provider.{payload.provider:id}",
					"lnk.appointment.{payload.appointmentKey:id}.forPatient.patient.{payload.patient:id}",
				},
				OptionalReads: []string{
					"{payload.appointmentKey}.status",
					// The self-scope ownership probe, same shape and rationale
					// as CreateAppointment's / RescheduleAppointment's above.
					"lnk.patient.{payload.patient:id}.identifiedBy.identity.{actor:id}",
				},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "CorrectAppointmentStatus",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Correct status",
				Description: "Correct a wrong terminal status (e.g. a no-show who actually showed). Does not reverse a no-show fee already charged — use a manual credit for that.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Correct status",
			},
			// The status enum is the THREE terminal values, not
			// SetAppointmentStatus's full six: this op refuses a non-terminal
			// target in-script, so offering scheduled/confirmed/checkedIn on
			// the form would render a choice that can only ever reject.
			// note is required here where SetAppointmentStatus leaves it
			// optional — a correction rewrites a record already treated as
			// final, so the reason is part of the write.
			// No provider/patient: the first terminal transition already
			// released the slot-claim cells, and a terminal→terminal move
			// touches none, so there is nothing for them to validate against.
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment — auto-filled from the appointment being viewed."},` +
				`"status":{"type":"string","title":"What actually happened","enum":["completed","cancelled","noShow"],` +
				`"enumLabels":{"completed":"Completed — the visit happened","cancelled":"Cancelled — the visit was called off","noShow":"No-show — the patient never came"},` +
				`"description":"What the appointment's outcome actually was. Only the terminal values — this op corrects a final call, it does not re-open the appointment."},` +
				`"note":{"type":"string","title":"Reason","maxLength":500,"description":"Why the recorded status was wrong. Required — the correction's audit trail."}},` +
				`"required":["appointmentKey","status","note"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being corrected — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"status":         "The outcome that actually happened: completed, cancelled or noShow. The appointment must already be in one of those three states.",
				"note":           "Required reason for the correction, kept on the appointment alongside the status it replaced.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "appointment",
				// standing, not "self": this op carries no scope=self grant
				// (staff-only correction), so it has no authContext target to
				// bind — the SetAppointmentSite posture, not
				// SetAppointmentStatus's.
				AuthContext: "standing",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
				Reads:       []string{"{payload.appointmentKey}"},
				// The current .status is OPTIONAL for the same reason
				// SetAppointmentStatus declares it so — absence is a legitimate
				// state of the appointment, answered here by this op's own
				// NotTerminal rejection rather than a correctness error.
				OptionalReads: []string{"{payload.appointmentKey}.status"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetProviderHours",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set working hours",
				Description: "Set your recurring weekly availability windows.",
				Icon:        "clock",
				Tone:        "primary",
				SubmitLabel: "Save hours",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of the provider these hours belong to — auto-filled from the provider being viewed."},` +
				`"windows":{"type":"array","title":"Weekly hours","description":"Your recurring weekly availability windows. Each {day:0-6 (Sun=0), openSec:0-86400, closeSec:0-86400} with openSec<closeSec; UTC seconds-of-day. An empty array clears the constraint.","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}}},` +
				`"required":["providerKey","windows"]}`,
			FieldDescriptions: map[string]string{
				"providerKey": "The provider whose hours are being set — auto-filled by the client from the provider being viewed (dispatch.targetField), not user-entered.",
				"windows":     "Your recurring weekly availability windows. Pass an empty array to clear all constraints (become unconstrained).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "provider",
				AuthContext: "standing",
				TargetField: "providerKey",
				TargetType:  "provider",
				Reads:       []string{"{payload.providerKey}"},
				// The standing-caller ownership probe (actor_bound_to_provider):
				// does the actor identifiedBy-bind to THIS provider? Absent ->
				// AuthDenied unless the actor also holds operator (that walk is
				// script-derived and undeclarable) — a meaningful rejection, not
				// a correctness error.
				OptionalReads: []string{"lnk.provider.{payload.providerKey:id}.identifiedBy.identity.{actor:id}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetProviderTimeOff",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set time off",
				Description: "Set date-specific blackout ranges on top of your weekly hours.",
				Icon:        "calendar-off",
				Tone:        "primary",
				SubmitLabel: "Save time off",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of the provider this time off belongs to — auto-filled from the provider being viewed."},` +
				`"ranges":{"type":"array","title":"Time off","description":"Your date-specific blackout ranges, on top of your weekly hours. Each {from, to, reason?} with from/to RFC3339 UTC instants and from<to. An empty array clears all blackouts.","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}}}}},` +
				`"required":["providerKey","ranges"]}`,
			FieldDescriptions: map[string]string{
				"providerKey": "The provider whose time off is being set — auto-filled by the client from the provider being viewed (dispatch.targetField), not user-entered.",
				"ranges":      "Your date-specific blackout ranges, on top of your weekly hours. Pass an empty array to clear all blackouts.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "provider",
				AuthContext: "standing",
				TargetField: "providerKey",
				TargetType:  "provider",
				Reads:       []string{"{payload.providerKey}"},
				// The standing-caller ownership probe, same shape and
				// rationale as SetProviderHours's above.
				OptionalReads: []string{"lnk.provider.{payload.providerKey:id}.identifiedBy.identity.{actor:id}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "RecordEncounter",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Document visit",
				Description: "Record the clinical note for your completed visit.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Save documentation",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment to document — auto-filled from the appointment being viewed."},` +
				`"summary":{"type":"string","title":"Summary","description":"Visit summary / clinical note. Required."},` +
				`"assessment":{"type":"string","title":"Assessment","description":"Optional clinical assessment / diagnosis."},` +
				`"plan":{"type":"string","title":"Plan","description":"Optional treatment plan / orders."},` +
				`"followUpRequested":{"type":"boolean","title":"Follow-up needed","description":"Whether the visit calls for a follow-up."},` +
				`"followUpDate":{"type":"string","format":"date","title":"Follow-up date","description":"Suggested follow-up date, when a follow-up is requested."}},` +
				`"required":["appointmentKey","summary"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey":    "The appointment being documented — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"summary":           "Required visit summary / clinical note. RAW clinical content — encrypted at rest, read back only through clinicEncountersRead by the treating provider.",
				"assessment":        "Optional clinical assessment / diagnosis. RAW PHI — encrypted at rest, read back only through clinicEncountersRead by the treating provider.",
				"plan":              "Optional treatment plan / orders. RAW PHI — encrypted at rest, read back only through clinicEncountersRead by the treating provider.",
				"followUpRequested": "Whether this visit calls for a follow-up. OPERATIONAL, non-PHI — projected.",
				"followUpDate":      "Suggested follow-up date, when followUpRequested is true. OPERATIONAL, non-PHI — projected.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "standing",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
				Reads:       []string{"{payload.appointmentKey}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetAppointmentSite",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set appointment site",
				Description: "Set the clinic site for your appointment.",
				Icon:        "map-pin",
				Tone:        "primary",
				SubmitLabel: "Set site",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment — auto-filled from the appointment being viewed."},` +
				`"site":{"type":"string","title":"Site","x-entityRef":"building","description":"vtx.building.<NanoID> clinic site this appointment is at."}},` +
				`"required":["appointmentKey","site"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being set — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"site":           "The clinic site this appointment is at. Must be a site you (the appointment's provider) practicesAt. No-op if the appointment already has a site.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "standing",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
				// site is required (unlike CreateAppointment's optional site)
				// and require_site_membership hard-fails UnknownSite on its
				// absence — a correctness error on THIS op, even though the
				// shared helper's own doc comment describes the class-(d)
				// posture CreateAppointment's optional site uses.
				Reads: []string{"{payload.appointmentKey}", "{payload.site}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "CreatePatient",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Register patient",
				Description: "Add a new patient to the practice roster.",
				Icon:        "user-plus",
				Tone:        "primary",
				SubmitLabel: "Register",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"fullName":{"type":"string","title":"Full name","description":"The patient's full name."},` +
				`"identityKey":{"type":"string","title":"Identity to link","description":"vtx.identity.<NanoID> of a pre-minted identity carrying the patient's contact details."}},` +
				`"required":["fullName"]}`,
			FieldDescriptions: map[string]string{
				"fullName":    "The patient's full name, as it should appear on the roster.",
				"identityKey": "Optional. An existing sign-in identity to connect this patient to, so their contact details follow them. An identity can back at most one patient.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "patient",
				AuthContext: "standing",
				// Only the identity vertex, never its .patientClaim guard. A
				// template that hangs a suffix off an OPTIONAL field cannot
				// express "declare this only when the field is supplied": an
				// omitted identityKey substitutes empty and leaves the bare
				// aspect suffix, which is not a valid KV key and rejects the
				// whole operation. The guard is read lazily in-script for
				// exactly this reason (ddls.go claim_identity), and the
				// hand-written dispatcher declares it conditionally
				// (cmd/clinic-app/web/app.js), which the vocabulary has no
				// form for.
				OptionalReads: []string{"{payload.identityKey}"},
				// Where the registration happened: the script enumerates the
				// caller's own worksAt links once and records a
				// registeredAtSite link per building on the new patient
				// (ddls.go — "patient registeredAtSite building", the roster
				// row's pre-appointment workplace anchor in clinicPatientsRead).
				// A class-(e) bounded enumeration, so it declares its walk here
				// as metadata rather than any key: the targets are unknowable
				// client-side, and the hub is {actor}, which the Processor
				// resolves server-side.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "worksAt", Direction: "out"},
				},
			},
		},
		{
			OperationType: "CreateProvider",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Register provider",
				Description: "Add a new provider to the practice roster.",
				Icon:        "user-plus",
				Tone:        "primary",
				SubmitLabel: "Register",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"fullName":{"type":"string","title":"Full name","description":"The provider's full name."},` +
				`"specialty":{"type":"string","title":"Specialty","description":"The provider's clinical specialty, e.g. Cardiology."},` +
				`"credentials":{"type":"string","title":"Credentials","description":"Optional post-nominal credentials, e.g. MD."},` +
				`"bio":{"type":"string","title":"Bio","description":"Optional short provider bio."}},` +
				`"required":["fullName","specialty"]}`,
			FieldDescriptions: map[string]string{
				"fullName":    "The provider's full name, as it should appear on the roster.",
				"specialty":   "The provider's clinical specialty (e.g. Cardiology).",
				"credentials": "Optional post-nominal credentials (e.g. MD, RN).",
				"bio":         "Optional short provider bio.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "provider",
				AuthContext: "standing",
				// Mints the provider — no pre-existing vertex for a client to
				// derive a target from (the CreatePatient idiom above).
			},
		},
		{
			OperationType: "SetProviderProfile",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Edit provider profile",
				Description: "Update a provider's name, specialty, credentials, and bio.",
				Icon:        "user",
				Tone:        "primary",
				SubmitLabel: "Save profile",
			},
			// A full replace (ddls.go SetProviderProfile), so the schema names
			// every .profile field, not just the ones being changed.
			InputSchema: `{"type":"object","properties":` +
				`{"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of the provider being edited — auto-filled from the provider being viewed."},` +
				`"fullName":{"type":"string","title":"Full name","description":"The provider's full name."},` +
				`"specialty":{"type":"string","title":"Specialty","description":"The provider's clinical specialty, e.g. Cardiology."},` +
				`"credentials":{"type":"string","title":"Credentials","description":"Optional post-nominal credentials, e.g. MD."},` +
				`"bio":{"type":"string","title":"Bio","description":"Optional short provider bio."}},` +
				`"required":["providerKey","fullName","specialty"]}`,
			FieldDescriptions: map[string]string{
				"providerKey": "The provider being edited — auto-filled by the client from the provider being viewed (dispatch.targetField), not user-entered.",
				"fullName":    "The provider's full name. REPLACES the stored value, so a re-submit must resupply it.",
				"specialty":   "The provider's clinical specialty. REPLACES the stored value, so a re-submit must resupply it.",
				"credentials": "Optional post-nominal credentials. Omitted clears any existing value (full replace).",
				"bio":         "Optional short provider bio. Omitted clears any existing value (full replace).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "provider",
				AuthContext: "standing",
				TargetField: "providerKey",
				TargetType:  "provider",
				Reads:       []string{"{payload.providerKey}"},
			},
		},
		{
			OperationType: "SetSiteProfile",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Name clinic site",
				Description: "Set the display name for a clinic site.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Save",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"buildingKey":{"type":"string","description":"vtx.building.<NanoID> of the site — auto-filled from the site being viewed."},` +
				`"name":{"type":"string","title":"Site name","description":"The clinic site/branch display name."}},` +
				`"required":["buildingKey","name"]}`,
			FieldDescriptions: map[string]string{
				"buildingKey": "The site being named — auto-filled by the client from the site being viewed (dispatch.targetField), not user-entered.",
				"name":        "The clinic site/branch display name. REPLACES the stored value, so a re-submit must resupply it.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinicSite",
				AuthContext: "standing",
				TargetField: "buildingKey",
				TargetType:  "building",
				Reads:       []string{"{payload.buildingKey}"},
			},
		},
		{
			OperationType: "AssignProviderSite",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Assign provider to site",
				Description: "Record that a provider practices at a site.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Assign",
			},
			// Both endpoints pre-exist and neither is "the entity in view" —
			// the shipped picker (cmd/clinic-app/web/app.js
			// submitAssignProviderSite) offers free-choice provider AND site
			// selects from a standalone assignment screen, so no TargetField
			// is named below (the ClaimIdentity/CreatePatient precedent for
			// "no natural target to derive").
			InputSchema: `{"type":"object","properties":` +
				`{"provider":{"type":"string","title":"Provider","description":"vtx.provider.<NanoID> of the provider."},` +
				`"building":{"type":"string","title":"Site","description":"vtx.building.<NanoID> of the site."}},` +
				`"required":["provider","building"]}`,
			FieldDescriptions: map[string]string{
				"provider": "The provider being assigned.",
				"building": "The site the provider will practice at.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinicSiteAssignment",
				AuthContext: "standing",
				Reads:       []string{"{payload.provider}", "{payload.building}"},
				// The per-pair practicesAt link is read on demand for the
				// create/revive/no-op idempotency branch (site.go
				// clinicSiteAssignmentDDLScript's own "read-posture: (d)"
				// annotation) — absence is the normal first-assignment case,
				// so it can never be a required read.
				OptionalReads: []string{"lnk.provider.{payload.provider:id}.practicesAt.building.{payload.building:id}"},
			},
		},
		{
			OperationType: "RemoveProviderSite",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Remove site assignment",
				Description: "Revoke a provider's assignment to a site.",
				Icon:        "building",
				Tone:        "destructive",
				SubmitLabel: "Remove",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"provider":{"type":"string","title":"Provider","description":"vtx.provider.<NanoID> of the provider."},` +
				`"building":{"type":"string","title":"Site","description":"vtx.building.<NanoID> of the site."}},` +
				`"required":["provider","building"]}`,
			FieldDescriptions: map[string]string{
				"provider": "The provider whose assignment is being removed.",
				"building": "The site the provider is being removed from.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinicSiteAssignment",
				AuthContext: "standing",
				// The script never validates provider/building aliveness on
				// this branch (site.go clinicSiteAssignmentDDLScript) — both
				// values only build the link key string, so no vertex read
				// grounds this op.
				OptionalReads: []string{"lnk.provider.{payload.provider:id}.practicesAt.building.{payload.building:id}"},
			},
		},
	}
}
