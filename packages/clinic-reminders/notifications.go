package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The bridge-facing half of the reminder and change-notice mechanisms.
// recordReminderScript / recordFollowUpReminderScript (ddls.go / followups.go)
// emit external.notification off their own transactional outbox when they
// write the .reminder / .followUpReminder marker (keyed on appointmentKey +
// remindedFor, so a redelivery of the same due reminder dedups at the adapter
// while a reschedule mints a fresh key and sends again), and
// recordChangeNoticeScript (changenotice.go) does the same when it writes the
// .changeNotice marker (keyed on appointmentKey:kind:changeRef). This file
// holds the replyOps the bridge posts back on completion:
// RecordAppointmentReminderNotification / RecordFollowUpReminderNotification
// (create-only, once per remindedFor) and RecordAppointmentChangeNotification
// (bare upsert, latest outcome wins), each writing an audit-only
// .reminderNotification / .followUpReminderNotification / .changeNotification
// aspect on the appointment. None gates a convergence lens — those still key
// on .reminder / .followUpReminder / .changeNotice, unchanged. No Loom
// pattern, no claim vertex: the bridge's dispatch path is fully generic
// (internal/bridge/dispatch.go) and needs neither. See
// _bmad-output/implementation-artifacts/clinic-reminders-notification-adapter-design.md.
const (
	reminderNotificationOpDDL     = "appointmentReminderNotificationOp"
	reminderNotificationAspectDDL = "appointmentReminderNotification"
	reminderNotificationOp        = "RecordAppointmentReminderNotification"

	followUpReminderNotificationOpDDL     = "followUpReminderNotificationOp"
	followUpReminderNotificationAspectDDL = "followUpReminderNotification"
	followUpReminderNotificationOp        = "RecordFollowUpReminderNotification"

	changeNotificationOpDDL     = "appointmentChangeNotificationOp"
	changeNotificationAspectDDL = "appointmentChangeNotification"
	changeNotificationOp        = "RecordAppointmentChangeNotification"
)

// notificationDDLs returns the six DDL meta-vertices (op handler + aspect
// gate, for each of the appointment-reminder, follow-up-reminder and
// appointment-change notification outcomes).
func notificationDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordReminderNotificationVertexTypeDDL(),
		reminderNotificationAspectTypeDDL(),
		recordFollowUpReminderNotificationVertexTypeDDL(),
		followUpReminderNotificationAspectTypeDDL(),
		recordChangeNotificationVertexTypeDDL(),
		changeNotificationAspectTypeDDL(),
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
			`"remindedFor":{"type":"string","description":"The documentation followUpDate this notification was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded, canonical UTC."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The adapter's terminal verdict (completed|failed).",
			"remindedFor": "The documentation followUpDate this notification was for.",
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

// recordChangeNotificationVertexTypeDDL owns the
// RecordAppointmentChangeNotification script — the replyOp the bridge submits
// after its "notification" adapter Executes for the external.notification
// event recordChangeNoticeScript (changenotice.go) emits. The bridge submits
// it with no ContextHint.Reads (internal/bridge's generic dispatch path), so
// the op reads NOTHING from state: it splits externalRef into the appointment
// key, the change kind, and the change reference, and bare-upserts the
// .changeNotification aspect — create-if-absent, overwrite-if-present, so a
// redelivered reply or a second distinct change both land cleanly, the latest
// outcome always winning. It runs no liveness check on the appointment: a
// reply is an audit record of a send that already happened, and a visit
// tombstoned after its notice went out must still be able to record how that
// send ended.
func recordChangeNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{changeNotificationOp},
		Description: "Appointment-change notification-outcome replyOp (clinic-reminders). RecordAppointmentChangeNotification{externalRef, status, result?} " +
			"is the op the bridge submits after its \"notification\" adapter Executes for the external.notification event " +
			"RecordAppointmentChangeNotice emitted. externalRef is <appointmentKey>:<kind>:<changeRef> — split on the FIRST " +
			"':' to recover the appointment key, then on the SECOND ':' to split kind (cancelled|moved) from changeRef (an " +
			"RFC3339 instant; the segment left over after the second ':', so it carries its own colons). The op writes " +
			"vtx.appointment.<NanoID>.changeNotification = {kind, changeRef, status, sentAt} (class appointmentChangeNotification) " +
			"as an UNCONDITIONED update — create-if-absent, overwrite-if-present, latest outcome wins — and runs NO " +
			"liveness check on the appointment: the reply is an audit record of a send that already happened, so it must " +
			"still be able to land on a visit tombstoned since. Audit/observability only: it gates no lens. Submitted " +
			"under the bridge's service-actor (operator-equivalent) authority. Reads nothing (the bridge submits no " +
			"ContextHint.Reads).",
		Script: recordChangeNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The <appointmentKey>:<kind>:<changeRef> token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The <appointmentKey>:<kind>:<changeRef> token (the same one RecordAppointmentChangeNotice minted as instanceKey/idempotencyKey). The op splits on the first ':' to recover the appointment key, then on the second ':' to split kind from changeRef.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .changeNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordAppointmentChangeNotification — a cancelled visit",
				Payload: map[string]any{"externalRef": "vtx.appointment.<NanoID>:cancelled:2026-09-17T14:02:11Z", "status": "completed", "result": "notification sent"},
				ExpectedOutcome: "Splits externalRef into the appointment key, kind=cancelled, and changeRef=2026-09-17T14:02:11Z. " +
					"Writes vtx.appointment.<NanoID>.changeNotification = {kind: cancelled, changeRef: 2026-09-17T14:02:11Z, " +
					"status: completed, sentAt: op.submittedAt} as an unconditioned update.",
			},
			{
				Name:    "RecordAppointmentChangeNotification — a moved visit, second outcome overwrites",
				Payload: map[string]any{"externalRef": "vtx.appointment.<NanoID>:moved:2026-09-17T09:30:00Z", "status": "failed", "result": "SMS gateway timeout"},
				ExpectedOutcome: "Splits externalRef into the appointment key, kind=moved, and changeRef=2026-09-17T09:30:00Z. " +
					"Overwrites any .changeNotification already on the appointment — the latest outcome wins.",
			},
		},
	}
}

// changeNotificationAspectTypeDDL declares the .changeNotification aspect
// (class appointmentChangeNotification) — the step-6 write gate for
// RecordAppointmentChangeNotification. Declaration-only. NON-sensitive: it
// carries only a kind/changeRef/status/timestamp (no PII), on a
// vtx.appointment (not an identity).
func changeNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{changeNotificationOp},
		Description: "Appointment change-notification-outcome aspect (clinic-reminders). Stored as " +
			"vtx.appointment.<NanoID>.changeNotification (class appointmentChangeNotification) = {kind, changeRef, status, sentAt}. " +
			"Non-sensitive. Written ONLY by RecordAppointmentChangeNotification (unconditioned update — latest outcome wins); " +
			"declaration-only, no op handler. Audit/observability marker — gates no lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"kind":{"type":"string","enum":["cancelled","moved"],"description":"The kind of change this notice was for."},` +
			`"changeRef":{"type":"string","description":"The value that identifies WHICH change: the .status.at instant for a cancel, the .schedule.movedAt instant for a move (RFC3339)."},` +
			`"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"kind":      "The kind of change this notice was for: cancelled or moved.",
			"changeRef": "The value identifying which change: the .status.at instant for a cancel, the .schedule.movedAt instant for a move.",
			"status":    "The adapter's terminal verdict (completed|failed).",
			"sentAt":    "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment change-notification-outcome aspect",
				Payload:         map[string]any{"kind": "moved", "changeRef": "2026-09-17T09:30:00Z", "status": "completed", "sentAt": "2026-09-17T09:30:05Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.changeNotification; written by RecordAppointmentChangeNotification.",
			},
		},
	}
}

// recordChangeNotificationScript handles RecordAppointmentChangeNotification.
// It reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is split into the appointment key (refused unless it is a
// vtx.appointment.<NanoID> — the token is minted by the emitter and echoed by
// the bridge, so the shape is validated here, at the write), the change kind,
// and the change reference, and the .changeNotification aspect is written as
// an UNCONDITIONED update — create-if-absent, overwrite-if-present — so a
// redelivered reply or a later, different change both land cleanly with no
// OCC pin needed.
const recordChangeNotificationScript = `
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
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

CHANGE_KINDS = ["cancelled", "moved"]

def split_external_ref(ref):
    # Keys carry dots, never colons, so the first ':' ends the key; the kind
    # carries neither, so the second ':' ends it; the changeRef is an RFC3339
    # instant that carries its own colons, so it is whatever is left — split
    # at most twice.
    idx1 = ref.find(":")
    if idx1 <= 0:
        fail("InvalidArgument: externalRef: required <appointmentKey>:<kind>:<changeRef>; got " + ref)
    appt_key = ref[:idx1]
    # Only a vtx.appointment.<NanoID> is a place this op may write: the bridge
    # echoes externalRef verbatim, so the key it recovers is shaped by
    # whoever minted the token, and an aspect on any other vertex type would
    # be an unguarded cross-type write under the operator grant.
    parts_of(appt_key, "externalRef", "appointment")
    rest = ref[idx1 + 1:]
    idx2 = rest.find(":")
    if idx2 <= 0:
        fail("InvalidArgument: externalRef: required <appointmentKey>:<kind>:<changeRef>; got " + ref)
    kind = rest[:idx2]
    change_ref = rest[idx2 + 1:]
    if kind not in CHANGE_KINDS:
        fail("InvalidArgument: externalRef: kind must be one of cancelled, moved; got " + kind)
    if len(change_ref) == 0:
        fail("InvalidArgument: externalRef: changeRef segment is empty; got " + ref)
    return appt_key, kind, change_ref

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordAppointmentChangeNotification":
        ext_ref = required_string(p, "externalRef")
        appt_key, kind, change_ref = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = appt_key + ".changeNotification"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "appointmentChangeNotification", "vertexKey": appt_key,
                          "localName": "changeNotification", "isDeleted": False,
                          "data": {"kind": kind, "changeRef": change_ref, "status": status, "sentAt": sent_at}}},
        ]
        events = [{"class": "clinic.appointmentChangeNotificationRecorded",
                   "data": {"appointmentKey": appt_key, "kind": kind, "changeRef": change_ref, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointmentChangeNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor)
// the right to submit the three notification-outcome replyOps.
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
		{
			OperationType: changeNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordAppointmentChangeNotification — the replyOp the bridge posts after its \"notification\" adapter Executes for a cancel or move notice.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the three replyOps for discoverability
// (hygiene, not strictly required — the bridge resolves the replyOp from the
// event body directly, not via forOperation).
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: reminderNotificationOp},
		{OperationType: followUpReminderNotificationOp},
		{OperationType: changeNotificationOp},
	}
}
