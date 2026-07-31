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
// InputSchema) — the fields a self-service caller actually supplies, not the
// operator-only ones (site/leaseAppKey on CreateAppointment; every non-cancel
// status value on SetAppointmentStatus, which the self grant rejects in-script
// anyway). SetAppointmentStatus's op-meta describes ONLY the cancel path: the
// operator continues to call the op directly (no descriptor needed — the
// trusted admin tool hardcodes its own status transitions), so narrowing the
// one op-meta to what a consumer can actually submit is honest, not a loss of
// operator capability.
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
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreateAppointment",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Book appointment",
				Description: "Book an appointment for yourself with a provider.",
				Icon:        "calendar",
				Tone:        "primary",
				SubmitLabel: "Book",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"patient":{"type":"string","title":"Patient","description":"vtx.patient.<NanoID> of your own patient record."},` +
				`"provider":{"type":"string","description":"vtx.provider.<NanoID> of the provider to book with — auto-filled from the provider being viewed."},` +
				`"startsAt":{"type":"string","format":"date-time","title":"Starts","description":"Appointment start, aligned to the 15-minute booking grid."},` +
				`"endsAt":{"type":"string","format":"date-time","title":"Ends","description":"Appointment end, aligned to the 15-minute booking grid."},` +
				`"reason":{"type":"string","title":"Reason","description":"Optional visit reason."}},` +
				`"required":["patient","provider","startsAt","endsAt"]}`,
			FieldDescriptions: map[string]string{
				"patient":  "Your own patient record — you can only book for yourself.",
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
				// `{me.patient}` addresses the `patient` selfAnchor
				// edgeIdentity projects for an identity a patient record is
				// identifiedBy-bound to — the script requires exactly that
				// binding, so the unmarked (offer-gating) form is correct:
				// an identity with no patient record could only ever be
				// rejected server-side, and now sees the honest "needs your
				// own Patient" card instead of a form that cannot succeed.
				ContextParams: map[string]string{"patient": "{me.patient}"},
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
			},
		},
		{
			OperationType: "SetAppointmentStatus",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Cancel appointment",
				Description: "Cancel this appointment.",
				Icon:        "cancel",
				Tone:        "destructive",
				SubmitLabel: "Cancel appointment",
			},
			// The self-scope grant is restricted, in-script, to status=cancelled
			// only — this op-meta describes exactly that consumer-reachable
			// slice, not the operator's full status-transition surface (see
			// package doc comment above).
			InputSchema: `{"type":"object","properties":` +
				`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of the appointment to cancel — auto-filled from the appointment being viewed."},` +
				`"status":{"type":"string","title":"Status","enum":["cancelled"],"default":"cancelled","description":"Fixed to cancelled — the only self-service transition."},` +
				`"note":{"type":"string","title":"Note","description":"Optional cancellation reason."}},` +
				`"required":["appointmentKey","status"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being cancelled — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"status":         "Fixed to \"cancelled\" — cancelling is the only change you can make here.",
				"note":           "Optional cancellation reason, kept with the appointment.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
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
				"summary":           "Required visit summary / clinical note. RAW clinical content — captured, never projected into a read model.",
				"assessment":        "Optional clinical assessment / diagnosis. RAW PHI — captured, never projected.",
				"plan":              "Optional treatment plan / orders. RAW PHI — captured, never projected.",
				"followUpRequested": "Whether this visit calls for a follow-up. OPERATIONAL, non-PHI — projected.",
				"followUpDate":      "Suggested follow-up date, when followUpRequested is true. OPERATIONAL, non-PHI — projected.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "standing",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
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
			},
		},
	}
}
