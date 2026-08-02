package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The bridge-facing half of the reminder mechanism. recordReminderScript
// (ddls.go) already emits external.notification off its own transactional
// outbox when it writes the .reminder marker (keyed on bookingKey +
// remindedFor, so a redelivery of the same due reminder dedups at the
// adapter while a ReassignSession time move mints a fresh key and sends
// again). This file holds the replyOp the bridge posts back on completion:
// RecordBookingReminderNotification, writing an audit-only
// .reminderNotification aspect on the booking. It does NOT gate the
// wellnessBookingReminders convergence lens — that still keys on .reminder,
// unchanged. No Loom pattern, no claim vertex: the bridge's dispatch path is
// fully generic (internal/bridge/dispatch.go) and needs neither. Mirrors
// clinic-reminders/notifications.go exactly, substituting booking for
// appointment and dropping the follow-up half (wellness has no encounter /
// follow-up concept).
const (
	reminderNotificationOpDDL     = "bookingReminderNotificationOp"
	reminderNotificationAspectDDL = "bookingReminderNotification"
	reminderNotificationOp        = "RecordBookingReminderNotification"
)

// notificationDDLs returns the two new DDL meta-vertices (op handler +
// aspect gate) for the booking-reminder notification outcome.
func notificationDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordReminderNotificationVertexTypeDDL(),
		reminderNotificationAspectTypeDDL(),
	}
}

// recordReminderNotificationVertexTypeDDL owns the
// RecordBookingReminderNotification script — the externalTask-style replyOp
// the bridge submits after its "notification" adapter Executes. The bridge
// submits it with no ContextHint.Reads (internal/bridge's generic dispatch
// path), so the op reads NOTHING from state: it reconstructs the booking key
// from the bare externalRef segment and writes the .reminderNotification
// aspect as a create-only mutation (once per remindedFor — a redelivered
// reply conflicts on the existing key and is rejected, the same FR58
// redelivery defense lease-signing's .outcome aspect uses).
func recordReminderNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{reminderNotificationOp},
		Description: "Booking-reminder notification-outcome replyOp (wellness-reminders). RecordBookingReminderNotification{externalRef, status, result?} " +
			"is the op the bridge submits after its \"notification\" adapter Executes for the external.notification event " +
			"recordReminderScript emitted. externalRef is the bare bookingKey:remindedFor token; the op reconstructs the " +
			"booking key (the segment before the first ':') and writes vtx.booking.<NanoID>.reminderNotification = " +
			"{status, remindedFor, sentAt} as a CREATE-ONLY mutation (class bookingReminderNotification) — once per " +
			"remindedFor, so a redelivered reply conflicts and is rejected (FR58). Audit/observability only: it does NOT " +
			"gate the wellnessBookingReminders convergence lens (still keyed on .reminder, unchanged). Submitted under the " +
			"bridge's service-actor (operator-equivalent) authority. Reads nothing (the bridge submits no ContextHint.Reads).",
		Script: recordReminderNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The bare bookingKey:remindedFor token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare bookingKey:remindedFor token (the same one recordReminderScript emitted as instanceKey/idempotencyKey). The op splits on the first ':' to recover the booking key and the remindedFor value.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .reminderNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordBookingReminderNotification — record a sent notification",
				Payload: map[string]any{"externalRef": "vtx.booking.<NanoID>:2026-07-01T15:00:00Z", "status": "completed", "result": "notification sent for vtx.booking.<NanoID>:2026-07-01T15:00:00Z"},
				ExpectedOutcome: "Splits externalRef on the first ':' to recover the booking key + remindedFor. Writes " +
					"vtx.booking.<NanoID>.reminderNotification = {status: completed, remindedFor, sentAt: op.submittedAt} as a " +
					"create-only mutation. Rejects a second reply for the same externalRef (FR58 once-only guard).",
			},
		},
	}
}

// reminderNotificationAspectTypeDDL declares the .reminderNotification
// aspect (class bookingReminderNotification) — the step-6 write gate for
// RecordBookingReminderNotification. Declaration-only. NON-sensitive: it
// carries only a status + timestamp (no PII), on a vtx.booking (not an
// identity).
func reminderNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{reminderNotificationOp},
		Description: "Booking reminder notification-outcome aspect (wellness-reminders). Stored as " +
			"vtx.booking.<NanoID>.reminderNotification (class bookingReminderNotification) = {status, remindedFor, sentAt}. " +
			"Non-sensitive. Written ONLY by RecordBookingReminderNotification (create-only, once per remindedFor); " +
			"declaration-only, no op handler. Audit/observability marker — does NOT gate the wellnessBookingReminders lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"remindedFor":{"type":"string","description":"The session startsAt this notification was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The adapter's terminal verdict (completed|failed).",
			"remindedFor": "The session startsAt this notification was for.",
			"sentAt":      "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking reminder notification-outcome aspect",
				Payload:         map[string]any{"status": "completed", "remindedFor": "2026-07-01T15:00:00Z", "sentAt": "2026-06-30T15:00:05Z"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.reminderNotification; written by RecordBookingReminderNotification.",
			},
		},
	}
}

// recordReminderNotificationScript handles RecordBookingReminderNotification.
// It reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is split on the first ':' to recover the booking key +
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

OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def split_external_ref(ref):
    idx = ref.find(":")
    if idx <= 0:
        fail("InvalidArgument: externalRef: required <bookingKey>:<remindedFor>; got " + ref)
    return ref[:idx], ref[idx+1:]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordBookingReminderNotification":
        ext_ref = required_string(p, "externalRef")
        booking_key, reminded_for = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = booking_key + ".reminderNotification"
        mutations = [
            {"op": "create", "key": marker_key,
             "document": {"class": "bookingReminderNotification", "vertexKey": booking_key,
                          "localName": "reminderNotification", "isDeleted": False,
                          "data": {"status": status, "remindedFor": reminded_for, "sentAt": sent_at}}},
        ]
        events = [{"class": "wellness.bookingReminderNotificationRecorded",
                   "data": {"bookingKey": booking_key, "status": status, "remindedFor": reminded_for}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": booking_key}}

    fail("bookingReminderNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor)
// the right to submit the notification-outcome replyOp.
func notificationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: reminderNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordBookingReminderNotification — the replyOp the bridge posts after its \"notification\" adapter Executes.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the replyOp for discoverability (hygiene, not
// strictly required — the bridge resolves the replyOp from the event body
// directly, not via forOperation).
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: reminderNotificationOp},
	}
}
