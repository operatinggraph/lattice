package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The audit half of the booking-change-notice mechanism. A promotion or a
// time move emits its external.notification from wellness-reminders' own
// notice op; a call-off emits its own from ReleaseOrphanedBooking's batch
// (ddls.go), in this package. Both name RecordBookingChangeNotification as
// their replyOp, so the op that records the bridge's outcome has to be
// installed beneath whichever emitter fires — wellness-domain, the lower
// package both depend on, owns it. RecordBookingChangeNotification
// bare-upserts vtx.booking.<NanoID>.changeNotification = {kind, changeRef,
// status, sentAt} (class bookingChangeNotification): audit only, latest
// outcome wins, never gates a lens. It lands on a live booking for a
// promotion or move and on the just-tombstoned booking for a call-off alike
// — a record of a send, not a fact any reader converges on, so it carries no
// liveness guard.
const (
	changeNotificationOpDDL     = "bookingChangeNotificationOp"
	changeNotificationAspectDDL = "bookingChangeNotification"

	// changeNotificationOp is the single operation this file's DDLs own.
	changeNotificationOp = "RecordBookingChangeNotification"
)

// notificationDDLs returns the two DDL meta-vertices (op handler + aspect
// gate) for the booking-change notification outcome.
func notificationDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordChangeNotificationVertexTypeDDL(),
		changeNotificationAspectTypeDDL(),
	}
}

// recordChangeNotificationVertexTypeDDL owns the
// RecordBookingChangeNotification script — the replyOp the bridge submits
// after its "notification" adapter Executes for the external.notification
// event either wellness-reminders' notice op (a promotion or a move) or this
// package's own ReleaseOrphanedBooking (a call-off) emits. The bridge
// submits it with no ContextHint.Reads (internal/bridge's generic dispatch
// path), so the op reads NOTHING from state: it splits externalRef into the
// booking key, the change kind, and the change reference, and bare-upserts
// the .changeNotification aspect — create-if-absent, overwrite-if-present,
// so a redelivered reply or a second distinct change both land cleanly, the
// latest outcome always winning. It runs no liveness check on the booking:
// a call-off notice's booking is tombstoned by the same batch that emitted
// it (ReleaseOrphanedBooking), by design, so requiring the booking to still
// be alive would make every call-off reply fail.
func recordChangeNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{changeNotificationOp},
		Description: "Booking-change notification-outcome replyOp (wellness-domain). RecordBookingChangeNotification{externalRef, status, result?} " +
			"is the op the bridge submits after its \"notification\" adapter Executes for the external.notification event " +
			"a promotion/move notice (wellness-reminders) or a call-off notice (this package's ReleaseOrphanedBooking) " +
			"emitted. externalRef is <bookingKey>:<kind>:<changeRef> — split on the FIRST ':' to recover the booking key, " +
			"then on the SECOND ':' to split kind (promoted|moved|calledOff) from changeRef (an RFC3339 instant or a vtx " +
			"key; the segment left over after the second ':', so it may itself carry colons). The op writes " +
			"vtx.booking.<NanoID>.changeNotification = {kind, changeRef, status, sentAt} (class bookingChangeNotification) " +
			"as an UNCONDITIONED update — create-if-absent, overwrite-if-present, latest outcome wins — and runs NO " +
			"liveness check on the booking: a call-off notice's own booking is tombstoned in the very batch that emitted " +
			"it, so this replyOp must still be able to land on it. Audit/observability only: it gates no lens. Submitted " +
			"under the bridge's service-actor (operator-equivalent) authority. Reads nothing (the bridge submits no " +
			"ContextHint.Reads).",
		Script: recordChangeNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The <bookingKey>:<kind>:<changeRef> token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The <bookingKey>:<kind>:<changeRef> token (the same one the emitting op minted as instanceKey/idempotencyKey). The op splits on the first ':' to recover the booking key, then on the second ':' to split kind from changeRef.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .changeNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordBookingChangeNotification — a moved class",
				Payload: map[string]any{"externalRef": "vtx.booking.<NanoID>:moved:2026-10-01T15:00:00Z", "status": "completed", "result": "notification sent"},
				ExpectedOutcome: "Splits externalRef into the booking key, kind=moved, and changeRef=2026-10-01T15:00:00Z. " +
					"Writes vtx.booking.<NanoID>.changeNotification = {kind: moved, changeRef: 2026-10-01T15:00:00Z, " +
					"status: completed, sentAt: op.submittedAt} as an unconditioned update.",
			},
			{
				Name:    "RecordBookingChangeNotification — a called-off class, booking already tombstoned",
				Payload: map[string]any{"externalRef": "vtx.booking.<NanoID>:calledOff:vtx.session.<NanoID>", "status": "completed"},
				ExpectedOutcome: "Splits externalRef into the booking key, kind=calledOff, and changeRef=vtx.session.<NanoID> " +
					"(the session key). Writes the .changeNotification aspect on the booking even though " +
					"ReleaseOrphanedBooking already tombstoned it — no liveness check runs.",
			},
		},
	}
}

// changeNotificationAspectTypeDDL declares the .changeNotification aspect
// (class bookingChangeNotification) — the step-6 write gate for
// RecordBookingChangeNotification. Declaration-only. NON-sensitive: it
// carries only a kind/changeRef/status/timestamp (no PII), on a vtx.booking
// (not an identity).
func changeNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{changeNotificationOp},
		Description: "Booking change-notification-outcome aspect (wellness-domain). Stored as " +
			"vtx.booking.<NanoID>.changeNotification (class bookingChangeNotification) = {kind, changeRef, status, sentAt}. " +
			"Non-sensitive. Written ONLY by RecordBookingChangeNotification (unconditioned update — latest outcome wins); " +
			"declaration-only, no op handler. Audit/observability marker — gates no lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"kind":{"type":"string","enum":["promoted","moved","calledOff"],"description":"The kind of change this notice was for."},` +
			`"changeRef":{"type":"string","description":"The value that identifies WHICH change: the promotion/move instant (RFC3339), or the session key for a call-off."},` +
			`"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"kind":      "The kind of change this notice was for: promoted, moved, or calledOff.",
			"changeRef": "The value identifying which change: an RFC3339 instant for a promotion/move, a session key for a call-off.",
			"status":    "The adapter's terminal verdict (completed|failed).",
			"sentAt":    "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking change-notification-outcome aspect",
				Payload:         map[string]any{"kind": "moved", "changeRef": "2026-10-01T15:00:00Z", "status": "completed", "sentAt": "2026-09-30T15:00:05Z"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.changeNotification; written by RecordBookingChangeNotification.",
			},
		},
	}
}

// recordChangeNotificationScript handles RecordBookingChangeNotification. It
// reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is split into the booking key (refused unless it is a
// vtx.booking.<NanoID> — the token is minted by the emitter and echoed by
// the bridge, so the shape is validated here, at the write), the change kind,
// and the change reference, and the .changeNotification aspect is written as an
// UNCONDITIONED update — create-if-absent, overwrite-if-present — so a
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

CHANGE_KINDS = ["promoted", "moved", "calledOff"]

def split_external_ref(ref):
    idx1 = ref.find(":")
    if idx1 <= 0:
        fail("InvalidArgument: externalRef: required <bookingKey>:<kind>:<changeRef>; got " + ref)
    booking_key = ref[:idx1]
    # Only a vtx.booking.<NanoID> is a place this op may write: the bridge
    # echoes externalRef verbatim, so the key it recovers is shaped by
    # whoever minted the token, and an aspect on any other vertex type would
    # be an unguarded cross-type write under the operator grant.
    parts_of(booking_key, "externalRef", "booking")
    rest = ref[idx1 + 1:]
    idx2 = rest.find(":")
    if idx2 <= 0:
        fail("InvalidArgument: externalRef: required <bookingKey>:<kind>:<changeRef>; got " + ref)
    kind = rest[:idx2]
    change_ref = rest[idx2 + 1:]
    if kind not in CHANGE_KINDS:
        fail("InvalidArgument: externalRef: kind must be one of promoted, moved, calledOff; got " + kind)
    if len(change_ref) == 0:
        fail("InvalidArgument: externalRef: changeRef segment is empty; got " + ref)
    return booking_key, kind, change_ref

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordBookingChangeNotification":
        ext_ref = required_string(p, "externalRef")
        booking_key, kind, change_ref = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = booking_key + ".changeNotification"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "bookingChangeNotification", "vertexKey": booking_key,
                          "localName": "changeNotification", "isDeleted": False,
                          "data": {"kind": kind, "changeRef": change_ref, "status": status, "sentAt": sent_at}}},
        ]
        events = [{"class": "wellness.bookingChangeNotificationRecorded",
                   "data": {"bookingKey": booking_key, "kind": kind, "changeRef": change_ref, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": booking_key}}

    fail("bookingChangeNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor)
// the right to submit the change-notification-outcome replyOp.
func notificationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: changeNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordBookingChangeNotification — the replyOp the bridge posts after its \"notification\" adapter Executes for a promotion, move, or call-off notice.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the replyOp for discoverability (hygiene, not
// strictly required — the bridge resolves the replyOp from the event body
// directly, not via forOperation).
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: changeNotificationOp},
	}
}
