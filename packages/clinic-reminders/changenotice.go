package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The appointment-change notice: a patient whose visit the desk cancelled,
// or moved to a new time, is told once per change. The
// appointmentChangeNotices lens (lenses.go) projects the two level-triggered
// gaps; RecordAppointmentChangeNotice is the directOp its playbook
// (targets.go) dispatches, and the .changeNotice marker it writes is what
// closes them. The op mirrors RecordAppointmentReminder (ddls.go): the same
// Weaver-actor guard, the same declared-read posture, the same
// external.notification egress off its own outbox. The bridge's replyOp for
// that egress, RecordAppointmentChangeNotification, lives beside the
// reminder's replyOp in notifications.go.
//
//   - appointmentChangeNoticeOp (vertexType) — owns the
//     RecordAppointmentChangeNotice script. Mints NO vertex of its own type; it
//     writes the .changeNotice aspect on an existing clinic-domain appointment
//     (the freshnessMarker idiom).
//   - appointmentChangeNotice (aspectType) — declares .changeNotice =
//     {cancelledFor?, movedFor?, sentAt} and admits RecordAppointmentChangeNotice
//     as its writer, so the Processor's step-6 validator permits the marker
//     write. Declaration-only: no op handler.
const (
	changeNoticeOpDDL     = "appointmentChangeNoticeOp"
	changeNoticeAspectDDL = "appointmentChangeNotice"

	// changeNoticeOp is the single operation the appointmentChangeNotices
	// playbook dispatches, for both of its gaps.
	changeNoticeOp = "RecordAppointmentChangeNotice"
)

// changeNoticeDDLs returns the two DDL meta-vertices (op handler + aspect
// gate) for the appointment-change notice.
func changeNoticeDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordChangeNoticeVertexTypeDDL(),
		changeNoticeAspectTypeDDL(),
	}
}

// recordChangeNoticeVertexTypeDDL owns the RecordAppointmentChangeNotice
// script. The op is the directOp the appointmentChangeNotices playbook
// dispatches when missing_cancel_notice or missing_move_notice opens. It
// re-checks the change the row named against the LIVE aspect before telling
// anyone — .status {value: cancelled, by: staff, at = changeRef} for a
// cancel, .schedule {movedAt = changeRef, movedBy: staff} on a non-terminal
// visit for a move — and refuses StaleChange when the row it was dispatched
// from has been outrun (a visit moved twice between projection and dispatch
// is told about the CURRENT move on the next dispatch, never the
// intermediate one). The .changeNotice write is a create when the marker is
// absent and a BARE update when it exists: the marker is a declared
// optionalRead, so the Processor conditions the update on the step-4
// revision (§3.2) as a defaulted, retry-eligible condition — two kinds
// converging on one appointment re-execute on conflict and carry each
// other's field, rather than one being rejected outright by an explicit CAS.
func recordChangeNoticeVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNoticeOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{changeNoticeOp},
		Description: "Appointment-change notice op handler (clinic-reminders). RecordAppointmentChangeNotice{appointmentKey, " +
			"kind: cancelled|moved, changeRef} tells a patient once about one change the desk made to their visit and " +
			"records that it did: it writes vtx.appointment.<NanoID>.changeNotice = {cancelledFor?, movedFor?, sentAt} " +
			"(class appointmentChangeNotice) on a LIVE appointment, setting cancelledFor = changeRef for kind=cancelled " +
			"or movedFor = changeRef for kind=moved and carrying the other kind's field forward, and emits " +
			"external.notification off its own outbox (instanceKey = idempotencyKey = externalRef = " +
			"<appointmentKey>:<kind>:<changeRef>) to the bridge's \"notification\" adapter; " +
			"RecordAppointmentChangeNotification (notifications.go) records the outcome. It is the directOp the " +
			"appointmentChangeNotices §10.8 playbook dispatches for both of that lens's gaps (missing_cancel_notice " +
			"with changeRef = row.statusAt; missing_move_notice with changeRef = row.movedAt). Reads [appointmentKey, " +
			"appointmentKey.status, appointmentKey.schedule] and optionally [appointmentKey.changeNotice]: it " +
			"liveness-guards the appointment (UnknownAppointment) and re-checks the change against the live aspect — " +
			"kind=cancelled requires .status.value = cancelled AND .status.by = staff AND .status.at = changeRef; " +
			"kind=moved requires .schedule.movedAt = changeRef AND .schedule.movedBy = staff (StaleChange otherwise) " +
			"on a non-terminal status (InvalidState otherwise — a moved-then-cancelled visit gets the cancel notice " +
			"only) — so a stale row is refused, not trusted. A patient's own cancel or move (by/movedBy = patient) " +
			"and a legacy status with no at are never told. The marker write is a create when the aspect is absent " +
			"and a bare update on the hydrated key when it exists (§3.2-conditioned on the step-4 revision, " +
			"retry-eligible in-process), so a cancel notice and a move notice converging on one appointment " +
			"re-execute on conflict and never drop each other's field. Submitted under Weaver's service-actor " +
			"authority only. Mints NO vertex of its own type.",
		Script: recordChangeNoticeScript,
		InputSchema: `{"type":"object","properties":` +
			`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> whose visit changed (required; validated alive). The caller MUST list it, appointmentKey.status and appointmentKey.schedule in ContextHint.Reads."},` +
			`"kind":{"type":"string","enum":["cancelled","moved"],"description":"Which change this notice is for: cancelled (the desk cancelled the visit) or moved (the desk moved it to a new time). Required."},` +
			`"changeRef":{"type":"string","description":"The value that identifies WHICH change: the .status.at instant for kind=cancelled, the .schedule.movedAt instant for kind=moved (RFC3339, canonical UTC). Required; refused StaleChange when it no longer matches the live aspect."}},` +
			`"required":["appointmentKey","kind","changeRef"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the change-notice marker was written on."}}}`,
		FieldDescription: map[string]string{
			"appointmentKey": "Full vtx.appointment.<NanoID> key whose visit changed. RecordAppointmentChangeNotice validates it is alive, re-checks the change against its live .status / .schedule, then writes the .changeNotice aspect on it. The caller MUST list this key, appointmentKey.status and appointmentKey.schedule in ContextHint.Reads.",
			"kind":           "cancelled or moved — which change this notice tells the patient about, and which .changeNotice field (cancelledFor / movedFor) records it.",
			"changeRef":      "The change's own identifier, re-checked against the live aspect before anything is sent: .status.at for cancelled, .schedule.movedAt for moved. Recorded verbatim so the lens's equality closes the gap, and a later move (a new movedAt) reopens it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordAppointmentChangeNotice — a visit the desk cancelled",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "kind": "cancelled", "changeRef": "2026-09-17T14:02:11Z"},
				ExpectedOutcome: "Validates the appointment is alive and that .status = {value: cancelled, by: staff, at: 2026-09-17T14:02:11Z}, then " +
					"writes vtx.appointment.<NanoID>.changeNotice = {cancelledFor: 2026-09-17T14:02:11Z, sentAt: op.submittedAt} (carrying " +
					"any existing movedFor), emits clinic.appointmentChangeNoticeSent and external.notification keyed " +
					"vtx.appointment.<NanoID>:cancelled:2026-09-17T14:02:11Z, and returns primaryKey.",
			},
			{
				Name:    "RecordAppointmentChangeNotice — a visit the desk moved",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "kind": "moved", "changeRef": "2026-09-17T09:30:00Z"},
				ExpectedOutcome: "Validates the appointment is alive, non-terminal, and that .schedule = {movedAt: 2026-09-17T09:30:00Z, movedBy: staff}, " +
					"then writes movedFor: 2026-09-17T09:30:00Z (carrying any existing cancelledFor) and emits the notice keyed " +
					"vtx.appointment.<NanoID>:moved:2026-09-17T09:30:00Z. Refuses StaleChange if the visit has moved again since " +
					"the row was projected — the next dispatch carries the current move.",
			},
		},
	}
}

// changeNoticeAspectTypeDDL declares the .changeNotice aspect (class
// appointmentChangeNotice) — the step-6 write gate for
// RecordAppointmentChangeNotice. Declaration-only (the script lives on the
// appointmentChangeNoticeOp vertexType DDL). NON-sensitive: it carries only
// instants (no PII), on a vtx.appointment (not an identity), so step-6's
// sensitiveAspectScope does not fire.
func changeNoticeAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNoticeAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{changeNoticeOp},
		Description: "Appointment change-notice marker aspect (clinic-reminders). Stored as vtx.appointment.<NanoID>.changeNotice " +
			"(class appointmentChangeNotice) = {cancelledFor?, movedFor?, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordAppointmentChangeNotice (whose appointmentChangeNoticeOp vertexType DDL owns the script) as a " +
			"create-or-update carrying the other kind's field forward; this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler. cancelledFor = the .status.at the cancel notice was for (equality closes " +
			"missing_cancel_notice); movedFor = the .schedule.movedAt the last move notice was for (equality closes " +
			"missing_move_notice; a further move reopens it). Created at the first notice, carried per kind, never " +
			"reset; dies with the appointment.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"cancelledFor":{"type":"string","description":"The .status.at instant (RFC3339, canonical UTC) the cancel notice was for."},` +
			`"movedFor":{"type":"string","description":"The .schedule.movedAt instant (RFC3339, canonical UTC) the last move notice was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the latest notice of either kind was recorded (the op's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"cancelledFor": "The .status.at instant the cancel notice was for. cancelledFor = statusAt closes the cancel gap.",
			"movedFor":     "The .schedule.movedAt instant the last move notice was for. movedFor = the current movedAt closes the move gap; a further RescheduleAppointment reopens it.",
			"sentAt":       "RFC3339 instant the latest notice of either kind was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment change-notice marker aspect",
				Payload:         map[string]any{"cancelledFor": "2026-09-17T14:02:11Z", "movedFor": "2026-09-17T09:30:00Z", "sentAt": "2026-09-17T14:02:15Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.changeNotice; written by RecordAppointmentChangeNotice.",
			},
		},
	}
}

// recordChangeNoticeScript handles RecordAppointmentChangeNotice. It reads
// the appointment ROOT (declared read) to assert the visit is alive, its
// .status and .schedule aspects (declared reads) to re-check the change the
// row named and for the visit times every notice carries, and the
// appointment's own .changeNotice (declared optionalRead) so the write can
// carry the other kind's field. The change is re-checked against the live
// aspect before anything is emitted: a row Weaver dispatched from is a
// snapshot, and a cancel or move it names that the graph has since outrun is
// refused StaleChange rather than told.
const recordChangeNoticeScript = `
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

CHANGE_KINDS = ["cancelled", "moved"]

# TERMINAL_STATUSES is clinic-domain's own list (ddls.go): a visit at one of
# these has no future time left to be told about.
TERMINAL_STATUSES = ["completed", "cancelled", "noShow"]

def required_kind(p):
    kind = required_string(p, "kind")
    if kind not in CHANGE_KINDS:
        fail("InvalidArgument: kind: must be one of cancelled, moved; got " + kind)
    return kind

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordAppointmentChangeNotice":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # appointmentChangeNotices directOp playbook. appointmentKey arrives off
        # the payload and is forwarded in the external.notification body the
        # bridge turns into a real message to the patient, so a wider submitter
        # set is a forged send: an arbitrary operator naming any appointment it
        # likes and having the platform notify that patient. First statement in
        # the branch: it also denies the payload-shape and vertex-alive oracles
        # beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: RecordAppointmentChangeNotice is restricted to Weaver's dispatch actor; got " + op.actor)

        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")
        kind = required_kind(p)
        change_ref = required_string(p, "changeRef")

        # Liveness guard: never tell anyone about an absent/tombstoned appointment.
        # The op hydrates [appointmentKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key + " is absent or tombstoned; no notice sent")

        # The live status: the cancel re-check reads its value/by/at, the move
        # re-check its value (a terminal visit has no time left to move).
        # read-posture: (a) declared in contextHint.reads — the
        # appointmentChangeNotices target's Reads list (targets.go) declares
        # appointmentKey.status.
        status = kv.Read(appt_key + ".status")
        if status == None or status.isDeleted:
            fail("InvalidState: " + appt_key + ".status is absent; cannot tell a visit with no status")
        status_value = status.data.get("value")

        # The visit's times, for the move re-check (movedAt / movedBy) and for
        # every notice's params (a cancel notice tells the patient WHICH visit).
        # read-posture: (a) declared in contextHint.reads — the
        # appointmentChangeNotices target's Reads list (targets.go) declares
        # appointmentKey.schedule.
        schedule = kv.Read(appt_key + ".schedule")
        if schedule == None or schedule.isDeleted:
            fail("MissingSchedule: " + appt_key + ".schedule is absent; cannot tell a visit with no time")
        starts_at = schedule.data.get("startsAt")
        ends_at = schedule.data.get("endsAt")
        if starts_at == None or ends_at == None:
            fail("MissingSchedule: " + appt_key + ".schedule carries no startsAt/endsAt")

        # The change is re-checked against the LIVE aspect, never trusted off
        # the row: the row is a projection snapshot, and a cancel or move it
        # names that the graph has since outrun is refused, not told. The next
        # dispatch carries the current value. A patient's own change (by /
        # movedBy = patient) is never told from here, whatever the row said.
        if kind == "cancelled":
            if status_value != "cancelled":
                fail("StaleChange: " + appt_key + " is " + str(status_value) + ", not cancelled; nothing to tell")
            if status.data.get("by") != "staff":
                fail("StaleChange: " + appt_key + " was cancelled by " + str(status.data.get("by")) + ", not the desk; nothing to tell")
            status_at = status.data.get("at")
            if status_at == None:
                fail("StaleChange: " + appt_key + ".status carries no at; nothing to tell")
            if status_at != change_ref:
                fail("StaleChange: " + appt_key + " status at " + status_at + " is not the dispatched changeRef " + change_ref)
        else:
            moved_at = schedule.data.get("movedAt")
            if moved_at == None:
                fail("StaleChange: " + appt_key + ".schedule carries no movedAt; nothing to tell")
            if moved_at != change_ref:
                fail("StaleChange: " + appt_key + " movedAt " + moved_at + " is not the dispatched changeRef " + change_ref +
                     "; the visit moved again since the row was projected")
            if schedule.data.get("movedBy") != "staff":
                fail("StaleChange: " + appt_key + " was moved by " + str(schedule.data.get("movedBy")) + ", not the desk; nothing to tell")
            # A moved-then-cancelled visit gets the cancel notice only: there
            # is no future time to tell the patient about.
            if status_value in TERMINAL_STATUSES:
                fail("InvalidState: " + appt_key + " is " + str(status_value) + "; a moved visit that has since ended is not told about the move")

        sent_at = time.rfc3339_utc(op.submittedAt)

        # The marker carries BOTH kinds' fields: a cancel notice and a move
        # notice converging on one appointment each set their own field and
        # carry the other's forward, so neither write erases the other's
        # evidence.
        # read-posture: (d) declared optionalReads at appointmentChangeNotices
        # dispatch (the create-or-update branch below).
        existing = kv.Read(appt_key + ".changeNotice")
        marker = {}
        if existing != None and not existing.isDeleted:
            for field in ["cancelledFor", "movedFor"]:
                carried = existing.data.get(field)
                if carried != None:
                    marker[field] = carried
        if kind == "cancelled":
            marker["cancelledFor"] = change_ref
        else:
            marker["movedFor"] = change_ref
        marker["sentAt"] = sent_at

        marker_key = appt_key + ".changeNotice"
        marker_doc = {"class": "appointmentChangeNotice", "vertexKey": appt_key,
                      "localName": "changeNotice", "isDeleted": False, "data": marker}
        if existing != None:
            # A BARE update on the hydrated key (a logically-deleted marker is
            # still a live KV envelope, revived through the same path): the
            # Processor conditions it on the step-4 revision the optionalRead
            # observed (Contract #3 §3.2) and records the condition as
            # defaulted, which is what makes a conflict retry-eligible
            # in-process — re-hydrate, re-execute, carry the winner's field.
            # That is how two concurrent kinds converge on one marker. An
            # explicit expectedRevision would be an unretried CAS: the loser
            # is rejected outright and waits out the mark lease instead.
            marker_mut = {"op": "update", "key": marker_key, "document": marker_doc}
        else:
            marker_mut = {"op": "create", "key": marker_key, "document": marker_doc}

        events = [{"class": "clinic.appointmentChangeNoticeSent",
                   "data": {"appointmentKey": appt_key, "kind": kind, "changeRef": change_ref, "sentAt": sent_at}}]

        # Fire the actual notification send off this op's own transactional
        # outbox. The external ref keys on (appointmentKey, kind, changeRef): a
        # redelivery of the SAME change reuses the key so the adapter dedups,
        # while a second move (a new movedAt) mints a fresh key and sends
        # again — the same reopen semantics the .changeNotice marker has.
        ext_ref = appt_key + ":" + kind + ":" + change_ref
        params = {"appointmentKey": appt_key, "changeType": kind, "changeRef": change_ref,
                  "startsAt": starts_at, "endsAt": ends_at}
        events.append({"class": "external.notification",
                       "data": {"instanceKey": ext_ref, "adapter": "notification",
                                "replyOp": "RecordAppointmentChangeNotification",
                                "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                "params": params}})

        return {"mutations": [marker_mut], "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointmentChangeNoticeOp DDL: unknown operationType: " + ot)
`

// changeNoticePermissions grants RecordAppointmentChangeNotice to the
// `operator` role (scope any) — Weaver's service actor dispatches the
// directOp under operator authority; the script's own actor guard narrows
// the effective submitter set to Weaver's dispatch actor.
func changeNoticePermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: changeNoticeOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordAppointmentChangeNotice operations (orchestration-internal: the appointmentChangeNotices directOp playbook, dispatched by Weaver's service actor).",
			GrantsTo:      []string{"operator"},
		},
	}
}

// changeNoticeOpMetas makes RecordAppointmentChangeNotice
// forOperation-resolvable for discoverability, the way the reminder op's
// bare meta is declared; the playbook dispatches it directly, so the meta is
// not load-bearing for dispatch.
func changeNoticeOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: changeNoticeOp},
	}
}
