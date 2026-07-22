package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for clinic-domain's three consumer-invocable
// (scope=self) ops — CreateAppointment, RescheduleAppointment, and
// SetAppointmentStatus — mirroring service-domain's RequestService op-meta,
// the only other package to adopt the vocabulary so far (Fire 5,
// edge-showcase-app-design.md §7 "adoption across clinic/café/wellness
// consumer-shaped ops").
//
// Each InputSchema below is the narrow, consumer-facing slice of the DDL's
// full merged schema (appointmentVertexTypeDDL's InputSchema) — the fields a
// self-service patient actually supplies, not the operator-only ones
// (site/leaseAppKey on CreateAppointment; every non-cancel status value on
// SetAppointmentStatus, which the self grant rejects in-script anyway).
// SetAppointmentStatus's op-meta describes ONLY the cancel path: the operator
// continues to call the op directly (no descriptor needed — the trusted admin
// tool hardcodes its own status transitions), so narrowing the one op-meta to
// what a consumer can actually submit is honest, not a loss of operator
// capability.
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
// Dispatch.Class on each entry is "appointment" — the appointment DDL's own
// CanonicalName (appointmentVertexDDL), the Contract #2 §2.1 envelope `class`
// DDL-hint (mirrors service-domain's RequestService op-meta doc comment:
// Dispatch.Class = the owning DDL's CanonicalName, never the vertical name).
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
				`{"patient":{"type":"string","description":"vtx.patient.<NanoID> of your own patient record."},` +
				`"provider":{"type":"string","description":"vtx.provider.<NanoID> of the provider to book with — auto-filled from the provider being viewed."},` +
				`"startsAt":{"type":"string","description":"Appointment start, RFC3339, aligned to the 15-minute booking grid."},` +
				`"endsAt":{"type":"string","description":"Appointment end, RFC3339, aligned to the 15-minute booking grid."},` +
				`"reason":{"type":"string","description":"Optional visit reason."}},` +
				`"required":["patient","provider","startsAt","endsAt"]}`,
			FieldDescriptions: map[string]string{
				"patient":  "Your own patient record — must be linked, via identifiedBy, to your identity (self-scope grant requirement).",
				"provider": "The provider this appointment is with — auto-filled by the client from the provider being viewed (dispatch.targetField), not user-entered.",
				"startsAt": "Appointment start time. Must land in the future and align to the clinic's 15-minute grid.",
				"endsAt":   "Appointment end time. Must align to the 15-minute grid; span capped at 24 hours.",
				"reason":   "Optional visit reason / chief complaint.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "provider",
				TargetType:  "provider",
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
				`"provider":{"type":"string","description":"vtx.provider.<NanoID> — must be the appointment's actual provider."},` +
				`"patient":{"type":"string","description":"vtx.patient.<NanoID> — must be the appointment's actual patient."},` +
				`"startsAt":{"type":"string","description":"New start, RFC3339, aligned to the 15-minute booking grid."},` +
				`"endsAt":{"type":"string","description":"New end, RFC3339, aligned to the 15-minute booking grid."},` +
				`"reason":{"type":"string","description":"Optional visit reason; omitted clears the existing one."}},` +
				`"required":["appointmentKey","provider","patient","startsAt","endsAt"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being rescheduled — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"provider":       "Must match the appointment's actual withProvider link.",
				"patient":        "Must match the appointment's actual forPatient link, which must be linked to your identity (self-scope grant requirement).",
				"startsAt":       "New start time. Must land in the future and align to the 15-minute grid.",
				"endsAt":         "New end time. Must align to the 15-minute grid; span capped at 24 hours.",
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
				`"status":{"type":"string","enum":["cancelled"],"default":"cancelled","description":"Fixed to cancelled — the only self-service transition."},` +
				`"note":{"type":"string","description":"Optional cancellation reason."}},` +
				`"required":["appointmentKey","status"]}`,
			FieldDescriptions: map[string]string{
				"appointmentKey": "The appointment being cancelled — auto-filled by the client from the appointment being viewed (dispatch.targetField), not user-entered.",
				"status":         "Fixed to \"cancelled\" — a self-service caller cannot set any other status (rejected in-script).",
				"note":           "Optional cancellation reason, stored on the appointment's .status aspect.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "appointment",
				AuthContext: "self",
				TargetField: "appointmentKey",
				TargetType:  "appointment",
			},
		},
	}
}
