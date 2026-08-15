package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Canonical names (Contract #1 §1.5 — globally unique). The split mirrors
// clinic-reminders' appointmentReminderOp/appointmentReminder pair
// (freshnessMarker vertexType, owns the op script + freshnessExpiry
// aspectType, the step-6 write gate) — substituting booking for appointment,
// since a wellness class has MANY bookers (one appointment has exactly one
// patient), so the reminder marker must live on the per-booker booking, not
// on the shared session:
//
//   - bookingReminderOp (vertexType) — owns the RecordBookingReminder script.
//     It mints NO vertex of its own type; it writes the .reminder aspect on
//     an existing wellness-domain booking (the freshnessMarker idiom).
//   - bookingReminder (aspectType) — declares the .reminder = {sentAt,
//     remindedFor} aspect and admits RecordBookingReminder as its writer, so
//     the Processor's step-6 validator permits the marker write.
//     Declaration-only: no op handler.
const (
	reminderOpDDL     = "bookingReminderOp"
	reminderAspectDDL = "bookingReminder"

	// reminderOp is the single operation this package's playbook dispatches.
	reminderOp = "RecordBookingReminder"
)

// DDLs returns the package's DDL meta-vertices: the booking-reminder op
// handler (vertexType) + its .reminder aspect-type gate, and the
// notification-outcome replyOp pair (notifications.go). wellness-domain owns
// the booking vertex + its .status aspect; this package ATTACHES the
// .reminder marker aspect onto it (the loftspace-domain idiom of a package
// adding an aspect onto another package's vertex type, mirroring
// clinic-reminders' attachment onto clinic-domain's appointment).
func DDLs() []pkgmgr.DDLSpec {
	ddls := []pkgmgr.DDLSpec{
		recordReminderVertexTypeDDL(),
		reminderAspectTypeDDL(),
	}
	return append(ddls, notificationDDLs()...)
}

// recordReminderVertexTypeDDL owns the RecordBookingReminder script. The op
// is the directOp the wellnessBookingReminders playbook dispatches when
// missing_reminder opens: it writes vtx.booking.<id>.reminder = {sentAt} on
// a LIVE booking. It is read-guarded (ContextHint.Reads = [bookingKey]) so
// it never marks a reminder on an absent/tombstoned booking, and the write
// is an UNCONDITIONED update (overwrite-if-present) — idempotent in effect
// (re-running re-stamps a later sentAt; the gap stays closed once any sentAt
// is present), so a redelivery or sweep reclaim is harmless without a
// revision condition (the MarkExpired idiom, mirroring
// recordReminderVertexTypeDDL in clinic-reminders/ddls.go). The script also
// fires the actual notification send off its own transactional outbox to
// the bridge's "notification" adapter (notifications.go) — no Loom pattern
// needed, the bridge dispatch path is generic.
func recordReminderVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{reminderOp},
		Description: "Booking-reminder op handler (wellness-reminders). RecordBookingReminder{bookingKey, remindedFor?} writes " +
			"vtx.booking.<NanoID>.reminder = {sentAt, remindedFor} on a LIVE booking (class bookingReminder), recording that " +
			"the ~24h-ahead class reminder fired for the startsAt in remindedFor. It is the directOp the wellnessBookingReminders " +
			"§10.8 playbook dispatches when the missing_reminder gap opens (the booking's session's .schedule.remindAt deadline " +
			"passed); the playbook supplies remindedFor = row.startsAt so a later class time move (ReassignSession, which moves " +
			"startsAt) re-opens the gap and re-arms the reminder. Reads [bookingKey] to liveness-guard the booking (never marks " +
			"a reminder on an absent/tombstoned/cancelled booking). The write is an UNCONDITIONED update (create-if-absent / " +
			"overwrite-if-present), so it is idempotent in effect and re-run-safe under at-least-once. Submitted under Weaver's " +
			"service-actor authority. Mints NO vertex of its own type (the freshnessMarker idiom). It also emits " +
			"external.notification off its own outbox (keyed on bookingKey:remindedFor) so the bridge's \"notification\" " +
			"adapter actually sends; see RecordBookingReminderNotification (notifications.go) for the replyOp that records the " +
			"outcome. It guards liveness (isDeleted) but NOT status: a booking cancelled in the narrow window between the gap " +
			"opening and this op committing still gets a .reminder marker and a notification send. That is a rare-window " +
			"best-effort gap, not a hard guarantee, mirroring clinic-reminders' identical tradeoff.",
		Script: recordReminderScript,
		InputSchema: `{"type":"object","properties":` +
			`{"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> the reminder fired for (required; validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"remindedFor":{"type":"string","description":"The session startsAt (RFC3339, canonical UTC) this reminder is for (optional; the playbook supplies row.startsAt). Recorded so a session time move re-arms the reminder."}},` +
			`"required":["bookingKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the reminder marker was written on."}}}`,
		FieldDescription: map[string]string{
			"bookingKey":  "Full vtx.booking.<NanoID> key the reminder fired for. RecordBookingReminder validates it is alive and writes the .reminder aspect on it. The caller MUST list this key in ContextHint.Reads.",
			"remindedFor": "The session startsAt (RFC3339, canonical UTC) this reminder is for. The wellnessBookingReminders playbook supplies it as row.startsAt; stored on .reminder so the convergence gate (remindedFor <> startsAt) re-opens — and re-arms the reminder — when ReassignSession moves the class's startsAt.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordBookingReminder — mark a reminder as sent for a startsAt",
				Payload: map[string]any{"bookingKey": "vtx.booking.<NanoID>", "remindedFor": "2026-07-01T15:00:00Z"},
				ExpectedOutcome: "Validates the booking is alive, then writes vtx.booking.<NanoID>.reminder = {sentAt: " +
					"op.submittedAt (canonical UTC), remindedFor} as an unconditioned update (create-if-absent / overwrite-if-present), " +
					"emits wellness.bookingReminderSent, and returns primaryKey. Re-runs cleanly (idempotent in effect). Rejects " +
					"with a ScriptError if the booking is absent / tombstoned.",
			},
		},
	}
}

// reminderAspectTypeDDL declares the .reminder aspect (class bookingReminder)
// — the step-6 write gate for RecordBookingReminder. Declaration-only (the
// script lives on the bookingReminderOp vertexType DDL). NON-sensitive: it
// carries only a fire timestamp (no PII), and it attaches to a vtx.booking
// (not an identity), so step-6's sensitiveAspectScope does not fire.
func reminderAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     reminderAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{reminderOp},
		Description: "Booking reminder marker aspect (wellness-reminders). Stored as vtx.booking.<NanoID>.reminder " +
			"(class bookingReminder) = {sentAt, remindedFor}. Non-sensitive. Written ONLY by RecordBookingReminder (whose " +
			"bookingReminderOp vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler. remindedFor = the session startsAt this reminder was for; the gate " +
			"(remindedFor = startsAt) closing it converges, and a class time move (ReassignSession) re-opens it.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"sentAt":{"type":"string","description":"RFC3339 instant the reminder fired (the op's submittedAt, canonical UTC)."},` +
			`"remindedFor":{"type":"string","description":"The session startsAt (RFC3339, canonical UTC) this reminder was for."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"sentAt":      "RFC3339 instant the reminder fired (op.submittedAt, canonical UTC).",
			"remindedFor": "The session startsAt (RFC3339, canonical UTC) this reminder was for. remindedFor = the current startsAt closes the convergence gap; a ReassignSession that moves startsAt re-opens it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking reminder marker aspect",
				Payload:         map[string]any{"sentAt": "2026-06-30T15:00:00Z", "remindedFor": "2026-07-01T15:00:00Z"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.reminder; written by RecordBookingReminder.",
			},
		},
	}
}

// recordReminderScript handles RecordBookingReminder. It reads the booking
// ROOT (the OCC read declared in ContextHint.Reads) to assert it exists + is
// alive before writing the marker — without the guard the op would mint a
// .reminder aspect (and a 4-segment aspect key) on an absent/tombstoned
// booking. The write is UNCONDITIONED (no expectedRevision): idempotent in
// effect for at-least-once delivery. Mirrors clinic-reminders'
// recordReminderScript exactly, substituting booking for appointment.
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
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
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

    if ot == "RecordBookingReminder":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # wellnessBookingReminders directOp playbook. bookingKey arrives off the
        # payload and is forwarded in the external.notification body the bridge
        # turns into a real message to the client, so a wider submitter set is a
        # forged send: an arbitrary operator naming any booking it likes and
        # having the platform notify that client. First statement in the branch:
        # it also denies the payload-shape and vertex-alive oracles beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: RecordBookingReminder is restricted to Weaver's dispatch actor; got " + op.actor)

        booking_key = required_string(p, "bookingKey")
        parts_of(booking_key, "bookingKey", "booking")

        # Liveness guard: never mark a reminder on an absent/tombstoned booking.
        # The op hydrates [bookingKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, booking_key):
            fail("UnknownBooking: " + booking_key + " is absent or tombstoned; no reminder written")

        # sentAt is the op's own timestamp, normalized to canonical UTC so a
        # downstream lexical compare is sound (the lease-signing idiom).
        sent_at = time.rfc3339_utc(op.submittedAt)

        # remindedFor: the session startsAt this reminder is FOR (supplied by
        # the wellnessBookingReminders playbook as Params{remindedFor:
        # row.startsAt} — already canonical UTC from wellness-domain, stored
        # verbatim so the lens's remindedFor <> startsAt compare is
        # byte-exact). It is what makes the reminder re-arm when
        # ReassignSession moves the class's startsAt away from this recorded
        # value → the convergence gate re-opens → a fresh reminder fires for
        # the new time, stamping the new remindedFor. Absent (a manual call
        # without it) → the gap never converges by remindedFor; the playbook
        # always supplies it.
        reminded_for = None
        if hasattr(p, "remindedFor"):
            rf = getattr(p, "remindedFor")
            if rf != None and type(rf) == type("") and len(rf.strip()) > 0:
                reminded_for = rf.strip()

        # UNCONDITIONED update (no revision condition): create-if-absent /
        # overwrite-if-present. Idempotent in effect — a redelivery or sweep
        # reclaim re-stamps the marker; the gap (remindedFor = startsAt) stays
        # closed once the reminder for the current time is recorded. MarkExpired idiom.
        marker_key = booking_key + ".reminder"
        marker = {"sentAt": sent_at}
        if reminded_for != None:
            marker["remindedFor"] = reminded_for
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "bookingReminder", "vertexKey": booking_key,
                          "localName": "reminder", "isDeleted": False,
                          "data": marker}},
        ]
        events = [{"class": "wellness.bookingReminderSent",
                   "data": {"bookingKey": booking_key, "sentAt": sent_at, "remindedFor": reminded_for}}]

        # Fire the actual notification send off this op's own transactional
        # outbox (notifications.go). No Loom pattern needed — the bridge's
        # dispatch path is fully generic (internal/bridge/dispatch.go). The
        # external ref keys on (bookingKey, remindedFor): a redelivery of the
        # SAME due reminder reuses the same key so the adapter dedups (no
        # double-send), while a rescheduled class (a new remindedFor) mints a
        # fresh key and sends again — the same re-arm semantics the
        # .reminder marker already has.
        if reminded_for != None:
            ext_ref = booking_key + ":" + reminded_for
            events.append({"class": "external.notification",
                            "data": {"instanceKey": ext_ref, "adapter": "notification",
                                     "replyOp": "RecordBookingReminderNotification",
                                     "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                     "params": {"bookingKey": booking_key, "reminderType": "class", "remindedFor": reminded_for}}})

        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": booking_key}}

    fail("bookingReminderOp DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// bookingReminder aspect-type DDL. The .reminder aspect is written by the
// bookingReminderOp vertexType DDL's RecordBookingReminder branch; this
// aspect DDL is a step-6 gate only and fails closed if ever dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
