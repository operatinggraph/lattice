package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// Canonical names (Contract #1 §1.5 — globally unique). The split mirrors
// orchestration-base's freshnessMarker (vertexType, owns the op script) +
// freshnessExpiry (aspectType, the step-6 write gate):
//
//   - appointmentReminderOp (vertexType) — owns the RecordAppointmentReminder
//     script. It mints NO vertex of its own type; it writes the .reminder aspect
//     on an existing clinic-domain appointment (the freshnessMarker idiom).
//   - appointmentReminder (aspectType) — declares the .reminder = {sentAt} aspect
//     and admits RecordAppointmentReminder as its writer, so the Processor's
//     step-6 validator (which keys permittedCommands on the MUTATION document's
//     class) permits the marker write. Declaration-only: no op handler.
const (
	reminderOpDDL     = "appointmentReminderOp"
	reminderAspectDDL = "appointmentReminder"

	// reminderOp is the single operation this package's playbook dispatches.
	reminderOp = "RecordAppointmentReminder"
)

// DDLs returns the package's two DDL meta-vertices: the RecordAppointmentReminder
// op handler (vertexType) and the .reminder aspect-type gate. clinic-domain owns
// the appointment vertex + its .schedule/.status aspects; this package only
// ATTACHES the .reminder aspect (the loftspace-domain idiom of a package adding an
// aspect onto another package's vertex type — the step-6 gate keys on the aspect
// class, not the host vertex's owner).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordReminderVertexTypeDDL(),
		reminderAspectTypeDDL(),
	}
}

// recordReminderVertexTypeDDL owns the RecordAppointmentReminder script. The op is
// the directOp the appointmentReminders playbook dispatches when missing_reminder
// opens: it writes vtx.appointment.<id>.reminder = {sentAt} on a LIVE appointment.
// It is read-guarded (ContextHint.Reads = [appointmentKey]) so it never marks a
// reminder on an absent/tombstoned appointment, and the write is an UNCONDITIONED
// update (overwrite-if-present) — idempotent in effect (re-running re-stamps a
// later sentAt; the gap stays closed once any sentAt is present), so a redelivery
// or sweep reclaim is harmless without a revision condition (the MarkExpired
// idiom). The actual notification channel (email/SMS) is the deferred bridge-
// adapter work; recording sentAt + the FE surfacing it is the demonstrable slice.
func recordReminderVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{reminderOp},
		Description: "Appointment-reminder op handler (clinic-reminders). RecordAppointmentReminder{appointmentKey} writes " +
			"vtx.appointment.<NanoID>.reminder = {sentAt} on a LIVE appointment (class appointmentReminder), recording that " +
			"the ~24h-ahead reminder fired. It is the directOp the appointmentReminders §10.8 playbook dispatches when the " +
			"missing_reminder gap opens (the appointment's .schedule.remindAt deadline passed). Reads [appointmentKey] to " +
			"liveness-guard the parent (never marks a reminder on an absent/tombstoned appointment). The write is an " +
			"UNCONDITIONED update (create-if-absent / overwrite-if-present), so it is idempotent in effect and re-run-safe " +
			"under at-least-once. Submitted under Weaver's service-actor authority. Mints NO vertex of its own type (the " +
			"freshnessMarker idiom). NOTE: the actual notification delivery (email/SMS) is the deferred real-adapter work; " +
			"this records that the reminder became DUE, not that a notification was sent. It guards liveness " +
			"(isDeleted) but NOT status: an appointment cancelled in the narrow window between the gap opening and this " +
			"op committing still gets a .reminder marker. That is harmless while the marker is inert; the authoritative " +
			"\"do not actually notify a cancelled/changed appointment\" check belongs at the deferred notification-delivery " +
			"point (which must read live state at send time anyway), not here.",
		Script: recordReminderScript,
		InputSchema: `{"type":"object","properties":` +
			`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> the reminder fired for (required; validated alive). The caller MUST list it in ContextHint.Reads."}},` +
			`"required":["appointmentKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the reminder marker was written on."}}}`,
		FieldDescription: map[string]string{
			"appointmentKey": "Full vtx.appointment.<NanoID> key the reminder fired for. RecordAppointmentReminder validates it is alive and writes the .reminder aspect on it. The caller MUST list this key in ContextHint.Reads.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordAppointmentReminder — mark a reminder as sent",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>"},
				ExpectedOutcome: "Validates the appointment is alive, then writes vtx.appointment.<NanoID>.reminder = {sentAt: " +
					"op.submittedAt (canonical UTC)} as an unconditioned update (create-if-absent / overwrite-if-present), emits " +
					"clinic.appointmentReminderSent, and returns primaryKey. Re-runs cleanly (idempotent in effect). Rejects with " +
					"a ScriptError if the appointment is absent / tombstoned.",
			},
		},
	}
}

// reminderAspectTypeDDL declares the .reminder aspect (class appointmentReminder)
// — the step-6 write gate for RecordAppointmentReminder. Declaration-only (the
// script lives on the appointmentReminderOp vertexType DDL). NON-sensitive: it
// carries only a fire timestamp (no PII), and it attaches to a vtx.appointment
// (not an identity), so step-6's sensitiveAspectScope does not fire.
func reminderAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{reminderOp},
		Description: "Appointment reminder marker aspect (clinic-reminders). Stored as vtx.appointment.<NanoID>.reminder " +
			"(class appointmentReminder) = {sentAt}. Non-sensitive. Written ONLY by RecordAppointmentReminder (whose " +
			"appointmentReminderOp vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler. sentAt non-null is the fact that closes the missing_reminder convergence gap.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"sentAt":{"type":"string","description":"RFC3339 instant the reminder fired (the op's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"sentAt": "RFC3339 instant the reminder fired (op.submittedAt, canonical UTC). Its presence (non-null) closes the missing_reminder gap.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment reminder marker aspect",
				Payload:         map[string]any{"sentAt": "2026-06-30T15:00:00Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.reminder; written by RecordAppointmentReminder.",
			},
		},
	}
}

// recordReminderScript handles RecordAppointmentReminder. It reads the appointment
// ROOT (the OCC read declared in ContextHint.Reads) to assert it exists + is alive
// before writing the marker — without the guard the op would mint a .reminder
// aspect (and a 4-segment aspect key) on an absent/tombstoned appointment. It names
// the appointment type explicitly (the op is clinic-specific, unlike the
// type-agnostic MarkExpired). The write is UNCONDITIONED (no expectedRevision):
// idempotent in effect for at-least-once delivery.
const recordReminderScript = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] != want_type:
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordAppointmentReminder":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")

        # Liveness guard: never mark a reminder on an absent/tombstoned appointment.
        # The op hydrates [appointmentKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key + " is absent or tombstoned; no reminder written")

        # sentAt is the op's own timestamp, normalized to canonical UTC so a
        # downstream lexical compare is sound (the lease-signing idiom).
        sent_at = time.rfc3339_utc(op.submittedAt)

        # UNCONDITIONED update (no revision condition): create-if-absent /
        # overwrite-if-present. Idempotent in effect — a redelivery or sweep
        # reclaim re-stamps sentAt; the missing_reminder gap (reminderSentAt =
        # null) stays closed once ANY sentAt is present. The MarkExpired idiom.
        marker_key = appt_key + ".reminder"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "appointmentReminder", "vertexKey": appt_key,
                          "localName": "reminder", "isDeleted": False,
                          "data": {"sentAt": sent_at}}},
        ]
        events = [{"class": "clinic.appointmentReminderSent",
                   "data": {"appointmentKey": appt_key, "sentAt": sent_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointmentReminderOp DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// appointmentReminder aspect-type DDL. The .reminder aspect is written by the
// appointmentReminderOp vertexType DDL's RecordAppointmentReminder branch; this
// aspect DDL is a step-6 gate only and fails closed if ever dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
