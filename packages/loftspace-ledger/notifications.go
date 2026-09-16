package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The bridge-facing half of the arrears reminder. EvaluateLoftspaceArrears
// (accountDDLScript) already emits external.notification off its own
// transactional outbox on the commit that stamps .arrears.sentAt, keyed on
// (accountKey, dueAt, headKey) so a redelivery of the same due episode dedups
// at the adapter while a later episode mints a fresh key — the head's own
// transaction key is in the key because recorded due dates repeat across
// charges on one lease, and a key of the account and the date alone would
// have the adapter dedup a later episode's reminder away. This file holds the replyOp
// the bridge posts back on completion: RecordLoftspaceArrearsReminderNotification,
// writing an audit-only .arrearsNotification aspect on the account. It does NOT
// gate the loftspaceArrearsReminders convergence lens — that keys on .arrears
// (remindedFor), unchanged.
//
// This file mirrors wellness-ledger's notifications.go whole (the closer
// precedent: a ledger that stores no balance). One deliberate difference from
// the wellness-reminders precedent BOTH mirror (RecordBookingReminderNotification,
// notifications.go there):
// that one writes its outcome aspect CREATE-ONLY on a single key, so a second
// reply for the same booking is rejected outright. That is sound where an
// entity is reminded about at most once in practice; it is wrong here. Arrears
// RECUR — a tenant pays off a balance, runs another one up, and falls behind
// again — and every episode replies onto the same
// vtx.account.<id>.arrearsNotification key. A create-only write would
// reject the SECOND episode's outcome and every one after it, so the aspect
// would record the tenant's first-ever reminder forever. The write is therefore
// an idempotent overwrite: a redelivered reply for the same episode rewrites
// identical content (the same externalRef yields the same status and
// remindedFor), and a new episode's reply replaces the old record with the
// current one. The once-only guarantee that actually matters — one
// notification per episode — is enforced upstream by .arrears.sentAt and by the
// adapter's own dedup on the episode key, neither of which this audit aspect
// contributes to.
const (
	arrearsNotificationOpDDL     = "loftspaceArrearsNotificationOp"
	arrearsNotificationAspectDDL = "loftspaceAccountArrearsNotification"
	arrearsNotificationOp        = "RecordLoftspaceArrearsReminderNotification"
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
// RecordLoftspaceArrearsReminderNotification script — the externalTask-style
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
		Description: "LoftSpace rent-arrears notification-outcome replyOp (loftspace-ledger). " +
			"RecordLoftspaceArrearsReminderNotification{externalRef, status, result?} is the op the bridge submits after " +
			"its \"notification\" adapter Executes for the external.notification event EvaluateLoftspaceArrears emitted. " +
			"externalRef is the bare accountKey:dueAt:headTransactionKey token; the op parses it explicitly — the " +
			"account key is everything before the first ':', the dueAt is the canonical 20-character RFC3339 UTC " +
			"instant (YYYY-MM-DDTHH:MM:SSZ, which itself contains ':') that follows, and the head key is the rest after " +
			"the ':' that closes it — REFUSES it unless the account key is a well-formed vtx.account.<NanoID>, the " +
			"dueAt has exactly that shape and the head key is a well-formed vtx.transaction.<NanoID>, and writes " +
			"vtx.account.<NanoID>.arrearsNotification = {status, remindedFor, sentAt} " +
			"(class loftspaceAccountArrearsNotification) as an idempotent OVERWRITE — not create-only, because arrears " +
			"episodes recur on one account and every episode replies onto the same key, so a create-only write would " +
			"reject every outcome after the first. Audit/observability only: it does NOT gate the " +
			"loftspaceArrearsReminders convergence lens (still keyed on .arrears, unchanged), and the once-per-episode " +
			"guarantee lives upstream in .arrears.sentAt. This aspect is the DELIVERY fact; .arrears.sentAt is the " +
			"op's send intent. Submitted under the bridge's service-actor (operator-equivalent) authority. Reads " +
			"nothing (the bridge submits no ContextHint.Reads).",
		Script: recordArrearsNotificationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The bare accountKey:dueAt:headTransactionKey token the adapter event carried (echoed verbatim by the bridge). Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict, copied verbatim from Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form Detail string (audit only, not parsed)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.account.<NanoID> the notification outcome was recorded on."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare accountKey:dueAt:headTransactionKey token (the same one EvaluateLoftspaceArrears emitted as instanceKey/idempotencyKey). The op parses it explicitly — account key before the first ':', then the 20-character canonical RFC3339 UTC dueAt (which contains ':' itself), then the head transaction key — and refuses a ref that does not match that shape exactly; the recovered account key must be a well-formed vtx.account.<NanoID> and the head key a well-formed vtx.transaction.<NanoID>.",
			"status":      "The adapter's terminal verdict (completed|failed), written to the .arrearsNotification aspect.",
			"result":      "The adapter's free-form Detail string, carried for audit only (not written to the aspect data).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordLoftspaceArrearsReminderNotification — record a sent arrears reminder",
				Payload: map[string]any{"externalRef": "vtx.account.<NanoID>:2026-09-08T00:00:00Z:vtx.transaction.<NanoID>", "status": "completed", "result": "notification sent for vtx.account.<NanoID>:2026-09-08T00:00:00Z:vtx.transaction.<NanoID>"},
				ExpectedOutcome: "Parses externalRef into the account key, the dueAt reminded for and the head transaction key. Writes " +
					"vtx.account.<NanoID>.arrearsNotification = {status: completed, remindedFor, sentAt: op.submittedAt} " +
					"as an idempotent overwrite — a redelivered reply rewrites identical content, and the tenant's NEXT " +
					"arrears episode replaces it with that episode's outcome.",
			},
		},
	}
}

// arrearsNotificationAspectTypeDDL declares the .arrearsNotification aspect
// (class loftspaceAccountArrearsNotification) — the step-6 write gate for
// RecordLoftspaceArrearsReminderNotification. Declaration-only. NON-sensitive: a
// status and two timestamps (no money, no PII), on a vtx.account (not
// an identity).
func arrearsNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     arrearsNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{arrearsNotificationOp},
		Description: "LoftSpace rent-arrears notification-outcome aspect (loftspace-ledger). Stored as " +
			"vtx.account.<NanoID>.arrearsNotification (class loftspaceAccountArrearsNotification) = " +
			"{status, remindedFor, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordLoftspaceArrearsReminderNotification, as an idempotent overwrite (arrears episodes recur on one " +
			"account, so it holds the LATEST episode's outcome, not the first). Declaration-only, no op handler. " +
			"Audit/observability marker — the adapter's DELIVERY fact beside .arrears.sentAt's send intent; does " +
			"NOT gate the loftspaceArrearsReminders lens.",
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
				ExpectedOutcome: "Stored as vtx.account.<NanoID>.arrearsNotification; written by RecordLoftspaceArrearsReminderNotification.",
			},
		},
	}
}

// recordArrearsNotificationScript handles RecordLoftspaceArrearsReminderNotification.
// It reads NOTHING from state (the bridge submits no ContextHint.Reads):
// externalRef is parsed into the account key, the dueAt reminded for and the
// head transaction key. The account key is the op's ONLY say over which vertex
// it writes to and it arrives from outside the platform, so it is held to the
// full vertex grammar AND to the account type before anything is built from
// it; the dueAt is held to the canonical whole-second UTC shape the evaluation
// writes (its own ':' characters are why the token cannot simply be split on
// ':'), and the head key to the transaction grammar, so a token of any other
// shape is refused rather than partially trusted. The
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

DUE_AT_LEN = 20

def is_canonical_utc(v):
    # YYYY-MM-DDTHH:MM:SSZ exactly: the whole-second canonical UTC shape
    # time.rfc3339_utc writes and the evaluation records as dueAt. Positional
    # separators, digits everywhere else.
    if len(v) != DUE_AT_LEN:
        return False
    for i in range(DUE_AT_LEN):
        ch = v[i]
        if i in [4, 7]:
            if ch != "-":
                return False
        elif i == 10:
            if ch != "T":
                return False
        elif i in [13, 16]:
            if ch != ":":
                return False
        elif i == 19:
            if ch != "Z":
                return False
        elif ch not in "0123456789":
            return False
    return True

def split_external_ref(ref):
    # <accountKey>:<dueAt>:<headTransactionKey>. Keys carry no ':' but the
    # dueAt does (its time-of-day), so the token is parsed by position: the
    # account key ends at the first ':', the dueAt is the fixed-width
    # canonical instant after it, and the head key is whatever follows the
    # ':' that closes the instant.
    shape = "required <accountKey>:<YYYY-MM-DDTHH:MM:SSZ>:<headTransactionKey>; got "
    idx = ref.find(":")
    if idx <= 0:
        fail("InvalidArgument: externalRef: " + shape + ref)
    rest = ref[idx+1:]
    if len(rest) < DUE_AT_LEN + 2 or rest[DUE_AT_LEN] != ":":
        fail("InvalidArgument: externalRef: " + shape + ref)
    due_at = rest[:DUE_AT_LEN]
    if not is_canonical_utc(due_at):
        fail("InvalidArgument: externalRef: " + shape + ref)
    return ref[:idx], due_at, rest[DUE_AT_LEN+1:]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordLoftspaceArrearsReminderNotification":
        ext_ref = required_string(p, "externalRef")
        account_key, reminded_for, head_key = split_external_ref(ext_ref)
        # The whole vertex grammar, and the TYPE: externalRef is the one thing
        # this op takes on trust, echoed back by the bridge from an adapter
        # event, and the key it recovers becomes the vertex an aspect is
        # written onto. Without the type check any 3-segment vtx key names a
        # target — an externalRef of "vtx.identity.<NanoID>:<dueAt>" would
        # hang a loftspaceAccountArrearsNotification aspect off a tenant's
        # identity.
        parts_of(account_key, "externalRef", "account")
        # The head key is not written anywhere, but a token whose third
        # segment is not a transaction key is not a token this package
        # emitted, and the type check is what keeps the parse honest.
        parts_of(head_key, "externalRef", "transaction")
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = account_key + ".arrearsNotification"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "loftspaceAccountArrearsNotification", "vertexKey": account_key,
                          "localName": "arrearsNotification", "isDeleted": False,
                          "data": {"status": status, "remindedFor": reminded_for, "sentAt": sent_at}}},
        ]
        events = [{"class": "loftspace.arrearsReminderNotificationRecorded",
                   "data": {"accountKey": account_key, "status": status, "remindedFor": reminded_for}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": account_key}}

    fail("loftspaceArrearsNotificationOp DDL: unknown operationType: " + ot)
`

// notificationPermissions grants the operator (the bridge's service actor) the
// right to submit the notification-outcome replyOp.
func notificationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: arrearsNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordLoftspaceArrearsReminderNotification — the replyOp the bridge posts after its \"notification\" adapter Executes for an arrears reminder. Not a console operation: nothing in Loupe or loftspace-app dispatches it, the bridge does, so the grant needs no consoleOperator counterpart.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// notificationOpMetas declares the replyOp for discoverability (hygiene, not
// strictly required — the bridge resolves the replyOp from the event body
// directly, not via forOperation), parity with wellness-ledger.
func notificationOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: arrearsNotificationOp},
	}
}
