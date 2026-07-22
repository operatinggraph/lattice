package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The bridge-facing half of the reminder mechanism. recordReminderScript /
// recordFollowUpReminderScript (ddls.go / followups.go) already emit
// external.notification off their own transactional outbox when they write the
// .reminder / .followUpReminder marker (keyed on appointmentKey + remindedFor,
// so a redelivery of the same due reminder dedups at the adapter while a
// reschedule mints a fresh key and sends again). This file holds the replyOps
// the bridge posts back on completion: RecordAppointmentReminderNotification /
// RecordFollowUpReminderNotification, each writing an audit-only
// .reminderNotification / .followUpReminderNotification aspect on the
// appointment. Neither gates the appointmentReminders/followUpReminders
// convergence lenses — those still key on .reminder/.followUpReminder,
// unchanged. No Loom pattern, no claim vertex: the bridge's dispatch path is
// fully generic (internal/bridge/dispatch.go) and needs neither. See
// _bmad-output/implementation-artifacts/clinic-reminders-notification-adapter-design.md.
const (
	reminderNotificationOpDDL     = "appointmentReminderNotificationOp"
	reminderNotificationAspectDDL = "appointmentReminderNotification"
	reminderNotificationOp        = "RecordAppointmentReminderNotification"

	followUpReminderNotificationOpDDL     = "followUpReminderNotificationOp"
	followUpReminderNotificationAspectDDL = "followUpReminderNotification"
	followUpReminderNotificationOp        = "RecordFollowUpReminderNotification"
)

// notificationDDLs returns the four new DDL meta-vertices (op handler + aspect
// gate, for each of the appointment-reminder and follow-up-reminder
// notification outcomes).
func notificationDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordReminderNotificationVertexTypeDDL(),
		reminderNotificationAspectTypeDDL(),
		recordFollowUpReminderNotificationVertexTypeDDL(),
		followUpReminderNotificationAspectTypeDDL(),
	}
}

// recordReminderNotificationVertexTypeDDL owns the
// RecordAppointmentReminderNotification script — the externalTask-style replyOp
// the bridge submits after its "notification" adapter Executes. The bridge
// submits it with no ContextHint.Reads (internal/bridge's generic dispatch
// path), so the op reads NOTHING from state: it reconstructs the appointment
// key from the bare externalRef segment and writes the .reminderNotification
// aspect as a create-only mutation (once per remindedFor — a redelivered reply
// conflicts on the existing key and is rejected, the same FR58 redelivery
// defense lease-signing's .outcome aspect uses).
func recordReminderNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{reminderNotificationOp},
		Description: "Appointment-reminder notification-outcome replyOp (clinic-reminders). RecordAppointmentReminderNotification{externalRef, status, result?} " +
			"is the op the bridge submits after its \"notification\" adapter Executes for the external.notification event " +
			"recordReminderScript emitted. externalRef is the bare appointmentKey:remindedFor token; the op reconstructs the " +
			"appointment key (the segment before the first ':') and writes vtx.appointment.<NanoID>.reminderNotification = " +
			"{status, remindedFor, sentAt} as a CREATE-ONLY mutation (class appointmentReminderNotification) — once per " +
			"remindedFor, so a redelivered reply conflicts and is rejected (FR58). Audit/observability only: it does NOT " +
			"gate the appointmentReminders convergence lens (still keyed on .reminder, unchanged). Submitted under the " +
			"bridge's service-actor (operator-equivalent) authority. Reads nothing (the bridge submits no ContextHint.Reads).",
		Script: recordReminderNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The bare appointmentKey:remindedFor token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare appointmentKey:remindedFor token (the same one recordReminderScript emitted as instanceKey/idempotencyKey). The op splits on the first ':' to recover the appointment key and the remindedFor value.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .reminderNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordAppointmentReminderNotification — record a sent notification",
				Payload: map[string]any{"externalRef": "vtx.appointment.<NanoID>:2026-07-01T15:00:00Z", "status": "completed", "result": "notification sent for vtx.appointment.<NanoID>:2026-07-01T15:00:00Z"},
				ExpectedOutcome: "Splits externalRef on the first ':' to recover the appointment key + remindedFor. Writes " +
					"vtx.appointment.<NanoID>.reminderNotification = {status: completed, remindedFor, sentAt: op.submittedAt} as a " +
					"create-only mutation. Rejects a second reply for the same externalRef (FR58 once-only guard).",
			},
		},
	}
}

// reminderNotificationAspectTypeDDL declares the .reminderNotification aspect
// (class appointmentReminderNotification) — the step-6 write gate for
// RecordAppointmentReminderNotification. Declaration-only. NON-sensitive: it
// carries only a status + timestamp (no PII), on a vtx.appointment (not an
// identity).
func reminderNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{reminderNotificationOp},
		Description: "Appointment reminder notification-outcome aspect (clinic-reminders). Stored as " +
			"vtx.appointment.<NanoID>.reminderNotification (class appointmentReminderNotification) = {status, remindedFor, sentAt}. " +
			"Non-sensitive. Written ONLY by RecordAppointmentReminderNotification (create-only, once per remindedFor); " +
			"declaration-only, no op handler. Audit/observability marker — does NOT gate the appointmentReminders lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"remindedFor":{"type":"string","description":"The appointment startsAt this notification was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The adapter's terminal verdict (completed|failed).",
			"remindedFor": "The appointment startsAt this notification was for.",
			"sentAt":      "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment reminder notification-outcome aspect",
				Payload:         map[string]any{"status": "completed", "remindedFor": "2026-07-01T15:00:00Z", "sentAt": "2026-06-30T15:00:05Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.reminderNotification; written by RecordAppointmentReminderNotification.",
			},
		},
	}
}

// recordReminderNotificationScript handles RecordAppointmentReminderNotification.
// It reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is split on the first ':' to recover the appointment key +
// remindedFor, and the .reminderNotification aspect is written as a
// CREATE-ONLY mutation — the once-only guarantee (a redelivered reply
// conflicts on the existing key and the batch is rejected).
const recordReminderNotificationScript = `
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
    return v

OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def split_external_ref(ref):
    idx = ref.find(":")
    if idx <= 0:
        fail("InvalidArgument: externalRef: required <appointmentKey>:<remindedFor>; got " + ref)
    return ref[:idx], ref[idx+1:]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordAppointmentReminderNotification":
        ext_ref = required_string(p, "externalRef")
        appt_key, reminded_for = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = appt_key + ".reminderNotification"
        mutations = [
            {"op": "create", "key": marker_key,
             "document": {"class": "appointmentReminderNotification", "vertexKey": appt_key,
                          "localName": "reminderNotification", "isDeleted": False,
                          "data": {"status": status, "remindedFor": reminded_for, "sentAt": sent_at}}},
        ]
        events = [{"class": "clinic.appointmentReminderNotificationRecorded",
                   "data": {"appointmentKey": appt_key, "status": status, "remindedFor": reminded_for}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointmentReminderNotificationOp DDL: unknown operationType: " + ot)
`

// recordFollowUpReminderNotificationVertexTypeDDL is the follow-up-reminder
// mirror of recordReminderNotificationVertexTypeDDL.
func recordFollowUpReminderNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     followUpReminderNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{followUpReminderNotificationOp},
		Description: "Follow-up-reminder notification-outcome replyOp (clinic-reminders). RecordFollowUpReminderNotification{externalRef, status, result?} " +
			"mirrors RecordAppointmentReminderNotification for the follow-up reminder's external.notification event " +
			"(recordFollowUpReminderScript). Writes vtx.appointment.<NanoID>.followUpReminderNotification = {status, remindedFor, sentAt} " +
			"as a CREATE-ONLY mutation (class followUpReminderNotification). Audit/observability only — does NOT gate the " +
			"followUpReminders convergence lens (still keyed on .followUpReminder, unchanged). Reads nothing.",
		Script: recordFollowUpReminderNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The bare appointmentKey:remindedFor token the adapter event carried. Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare appointmentKey:remindedFor token. Split on the first ':' to recover the appointment key + remindedFor.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .followUpReminderNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordFollowUpReminderNotification — record a sent notification",
				Payload: map[string]any{"externalRef": "vtx.appointment.<NanoID>:2027-01-15T09:00:00Z", "status": "completed", "result": "notification sent for vtx.appointment.<NanoID>:2027-01-15T09:00:00Z"},
				ExpectedOutcome: "Splits externalRef, writes vtx.appointment.<NanoID>.followUpReminderNotification = {status: completed, " +
					"remindedFor, sentAt} as a create-only mutation. Rejects a second reply for the same externalRef.",
			},
		},
	}
}

// followUpReminderNotificationAspectTypeDDL declares the
// .followUpReminderNotification aspect — the follow-up mirror of
// reminderNotificationAspectTypeDDL.
func followUpReminderNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     followUpReminderNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{followUpReminderNotificationOp},
		Description: "Follow-up reminder notification-outcome aspect (clinic-reminders). Stored as " +
			"vtx.appointment.<NanoID>.followUpReminderNotification (class followUpReminderNotification) = {status, remindedFor, sentAt}. " +
			"Non-sensitive. Written ONLY by RecordFollowUpReminderNotification (create-only); declaration-only, no op handler. " +
			"Audit/observability marker — does NOT gate the followUpReminders lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"remindedFor":{"type":"string","description":"The encounter followUpDate this notification was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded, canonical UTC."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The adapter's terminal verdict (completed|failed).",
			"remindedFor": "The encounter followUpDate this notification was for.",
			"sentAt":      "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "follow-up reminder notification-outcome aspect",
				Payload:         map[string]any{"status": "completed", "remindedFor": "2027-01-15T09:00:00Z", "sentAt": "2027-01-15T09:00:05Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.followUpReminderNotification; written by RecordFollowUpReminderNotification.",
			},
		},
	}
}

// recordFollowUpReminderNotificationScript mirrors
// recordReminderNotificationScript for the follow-up reminder's outcome.
const recordFollowUpReminderNotificationScript = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def split_external_ref(ref):
    idx = ref.find(":")
    if idx <= 0:
        fail("InvalidArgument: externalRef: required <appointmentKey>:<remindedFor>; got " + ref)
    return ref[:idx], ref[idx+1:]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordFollowUpReminderNotification":
        ext_ref = required_string(p, "externalRef")
        appt_key, reminded_for = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = appt_key + ".followUpReminderNotification"
        mutations = [
            {"op": "create", "key": marker_key,
             "document": {"class": "followUpReminderNotification", "vertexKey": appt_key,
                          "localName": "followUpReminderNotification", "isDeleted": False,
                          "data": {"status": status, "remindedFor": reminded_for, "sentAt": sent_at}}},
        ]
        events = [{"class": "clinic.followUpReminderNotificationRecorded",
                   "data": {"appointmentKey": appt_key, "status": status, "remindedFor": reminded_for}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("followUpReminderNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor)
// the right to submit both notification-outcome replyOps.
func notificationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: reminderNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordAppointmentReminderNotification — the replyOp the bridge posts after its \"notification\" adapter Executes.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: followUpReminderNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordFollowUpReminderNotification — the replyOp the bridge posts after its \"notification\" adapter Executes.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the two replyOps for discoverability (hygiene,
// not strictly required — the bridge resolves the replyOp from the event body
// directly, not via forOperation).
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: reminderNotificationOp},
		{OperationType: followUpReminderNotificationOp},
	}
}
