package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The bridge-facing half of the arrears reminder. EvaluateWellnessArrears
// (accountDDLScript) already emits external.notification off its own
// transactional outbox on the commit that stamps .arrears.sentAt, keyed on
// (accountKey, dueAt) so a redelivery of the same due episode dedups at the
// adapter while a later episode mints a fresh key. This file holds the replyOp
// the bridge posts back on completion: RecordWellnessArrearsReminderNotification,
// writing an audit-only .arrearsNotification aspect on the account. It does NOT
// gate the wellnessArrearsReminders convergence lens — that keys on .arrears
// (remindedFor), unchanged.
//
// One deliberate difference from the wellness-reminders precedent this
// otherwise mirrors (RecordBookingReminderNotification, notifications.go there):
// that one writes its outcome aspect CREATE-ONLY on a single key, so a second
// reply for the same booking is rejected outright. That is sound where an
// entity is reminded about at most once in practice; it is wrong here. Arrears
// RECUR — a member pays off a balance, runs another one up, and falls behind
// again — and every episode replies onto the same
// vtx.wellnessaccount.<id>.arrearsNotification key. A create-only write would
// reject the SECOND episode's outcome and every one after it, so the aspect
// would record the member's first-ever reminder forever. The write is therefore
// an idempotent overwrite: a redelivered reply for the same episode rewrites
// identical content (the same externalRef yields the same status and
// remindedFor), and a new episode's reply replaces the old record with the
// current one. The once-only guarantee that actually matters — one
// notification per episode — is enforced upstream by .arrears.sentAt and by the
// adapter's own dedup on the episode key, neither of which this audit aspect
// contributes to.
const (
	arrearsNotificationOpDDL     = "wellnessArrearsNotificationOp"
	arrearsNotificationAspectDDL = "wellnessAccountArrearsNotification"
	arrearsNotificationOp        = "RecordWellnessArrearsReminderNotification"
)

// notificationDDLs returns the two DDL meta-vertices (op handler + aspect gate)
// for the arrears-reminder notification outcome.
func notificationDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordArrearsNotificationVertexTypeDDL(),
		arrearsNotificationAspectTypeDDL(),
	}
}

// recordArrearsNotificationVertexTypeDDL owns the
// RecordWellnessArrearsReminderNotification script — the externalTask-style
// replyOp the bridge submits after its "notification" adapter Executes. The
// bridge submits it with no ContextHint.Reads (internal/bridge's generic
// dispatch path), so the op reads NOTHING from state: it reconstructs the
// account key from the bare externalRef segment and writes the
// .arrearsNotification aspect.
func recordArrearsNotificationVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     arrearsNotificationOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{arrearsNotificationOp},
		Description: "Wellness arrears-reminder notification-outcome replyOp (wellness-ledger). " +
			"RecordWellnessArrearsReminderNotification{externalRef, status, result?} is the op the bridge submits after " +
			"its \"notification\" adapter Executes for the external.notification event EvaluateWellnessArrears emitted. " +
			"externalRef is the bare accountKey:dueAt token; the op reconstructs the account key (the segment before " +
			"the first ':'), REFUSES it unless it is a well-formed vtx.wellnessaccount.<NanoID>, and writes " +
			"vtx.wellnessaccount.<NanoID>.arrearsNotification = {status, remindedFor, sentAt} " +
			"(class wellnessAccountArrearsNotification) as an idempotent OVERWRITE — not create-only, because arrears " +
			"episodes recur on one account and every episode replies onto the same key, so a create-only write would " +
			"reject every outcome after the first. Audit/observability only: it does NOT gate the " +
			"wellnessArrearsReminders convergence lens (still keyed on .arrears, unchanged), and the once-per-episode " +
			"guarantee lives upstream in .arrears.sentAt. This aspect is the DELIVERY fact; .arrears.sentAt is the " +
			"op's send intent. Submitted under the bridge's service-actor (operator-equivalent) authority. Reads " +
			"nothing (the bridge submits no ContextHint.Reads).",
		Script: recordArrearsNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The bare accountKey:dueAt token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare accountKey:dueAt token (the same one EvaluateWellnessArrears emitted as instanceKey/idempotencyKey). The op splits on the first ':' to recover the account key and the dueAt the reminder was for; the recovered key must be a well-formed vtx.wellnessaccount.<NanoID> or the operation is refused.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .arrearsNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordWellnessArrearsReminderNotification — record a sent arrears reminder",
				Payload: map[string]any{"externalRef": "vtx.wellnessaccount.<NanoID>:2026-08-06T14:20:00Z", "status": "completed", "result": "notification sent for vtx.wellnessaccount.<NanoID>:2026-08-06T14:20:00Z"},
				ExpectedOutcome: "Splits externalRef on the first ':' to recover the account key + the dueAt reminded for. Writes " +
					"vtx.wellnessaccount.<NanoID>.arrearsNotification = {status: completed, remindedFor, sentAt: op.submittedAt} " +
					"as an idempotent overwrite — a redelivered reply rewrites identical content, and the member's NEXT " +
					"arrears episode replaces it with that episode's outcome.",
			},
		},
	}
}

// arrearsNotificationAspectTypeDDL declares the .arrearsNotification aspect
// (class wellnessAccountArrearsNotification) — the step-6 write gate for
// RecordWellnessArrearsReminderNotification. Declaration-only. NON-sensitive: a
// status and two timestamps (no money, no PII), on a vtx.wellnessaccount (not
// an identity).
func arrearsNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     arrearsNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{arrearsNotificationOp},
		Description: "Wellness arrears-reminder notification-outcome aspect (wellness-ledger). Stored as " +
			"vtx.wellnessaccount.<NanoID>.arrearsNotification (class wellnessAccountArrearsNotification) = " +
			"{status, remindedFor, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordWellnessArrearsReminderNotification, as an idempotent overwrite (arrears episodes recur on one " +
			"account, so it holds the LATEST episode's outcome, not the first). Declaration-only, no op handler. " +
			"Audit/observability marker — the adapter's DELIVERY fact beside .arrears.sentAt's send intent; does " +
			"NOT gate the wellnessArrearsReminders lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"remindedFor":{"type":"string","description":"The arrears dueAt this notification was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The adapter's terminal verdict (completed|failed).",
			"remindedFor": "The arrears dueAt this notification was for — the same value .arrears.remindedFor carries.",
			"sentAt":      "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears notification-outcome aspect",
				Payload:         map[string]any{"status": "completed", "remindedFor": "2026-08-06T14:20:00Z", "sentAt": "2026-08-22T09:00:05Z"},
				ExpectedOutcome: "Stored as vtx.wellnessaccount.<NanoID>.arrearsNotification; written by RecordWellnessArrearsReminderNotification.",
			},
		},
	}
}

// recordArrearsNotificationScript handles RecordWellnessArrearsReminderNotification.
// It reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is split on the first ':' to recover the account key + the dueAt
// reminded for. That key is the op's ONLY say over which vertex it writes to and
// it arrives from outside the platform, so it is held to the full vertex grammar
// AND to the wellnessaccount type before anything is built from it. The
// .arrearsNotification aspect is then written as an UNCONDITIONED update —
// create-if-absent, overwrite-if-present. An unconditioned update is the right
// verb precisely because this key is written once per arrears EPISODE on a
// recurring account: a create-only write would reject every episode after the
// first, and a conditioned one has no revision to pin (the op declares no reads
// at all). Redelivery is harmless — the same externalRef reconstructs the same
// account key and the same remindedFor, so the rewrite is identical apart from
// sentAt.
const recordArrearsNotificationScript = `
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

def split_external_ref(ref):
    idx = ref.find(":")
    if idx <= 0:
        fail("InvalidArgument: externalRef: required <accountKey>:<dueAt>; got " + ref)
    return ref[:idx], ref[idx+1:]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordWellnessArrearsReminderNotification":
        ext_ref = required_string(p, "externalRef")
        account_key, reminded_for = split_external_ref(ext_ref)
        # The whole vertex grammar, and the TYPE: externalRef is the one thing
        # this op takes on trust, echoed back by the bridge from an adapter
        # event, and the key it recovers becomes the vertex an aspect is
        # written onto. Without the type check any 3-segment vtx key names a
        # target — an externalRef of "vtx.identity.<NanoID>:<dueAt>" would
        # hang a wellnessAccountArrearsNotification aspect off a member's
        # identity.
        parts_of(account_key, "externalRef", "wellnessaccount")
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = account_key + ".arrearsNotification"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "wellnessAccountArrearsNotification", "vertexKey": account_key,
                          "localName": "arrearsNotification", "isDeleted": False,
                          "data": {"status": status, "remindedFor": reminded_for, "sentAt": sent_at}}},
        ]
        events = [{"class": "wellness.arrearsReminderNotificationRecorded",
                   "data": {"accountKey": account_key, "status": status, "remindedFor": reminded_for}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": account_key}}

    fail("wellnessArrearsNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor) the
// right to submit the notification-outcome replyOp.
func notificationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: arrearsNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordWellnessArrearsReminderNotification — the replyOp the bridge posts after its \"notification\" adapter Executes for an arrears reminder. Not a console operation: nothing in Loupe or wellness-app dispatches it, the bridge does, so the grant needs no consoleOperator counterpart.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the replyOp for discoverability (hygiene, not
// strictly required — the bridge resolves the replyOp from the event body
// directly, not via forOperation), parity with wellness-reminders.
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: arrearsNotificationOp},
	}
}
