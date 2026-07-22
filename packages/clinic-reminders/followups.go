package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The follow-up-reminder sibling of the appointment reminder. A documented visit
// (clinic-domain RecordEncounter) can request a follow-up with a followUpDate; this
// machinery fires a one-shot @at reminder AT that date so a requested follow-up does
// not silently fall through (the clinic vertical's forcing function for the
// follow-ups worklist). It is the SAME convergence mechanism as the appointment
// reminder — aspect + op + freshUntil-armed @at lens + directOp playbook — applied
// to a different anchor field (the appointment's .encounter.followUpDate instead of
// .schedule.remindAt). The appointment-reminder definitions live alongside in
// ddls.go / lenses.go / targets.go; this file holds the follow-up-specific set.
//
//	vtx.appointment.<id>.followUpReminder = {sentAt, remindedFor}  (class followUpReminder — this package)
//	op RecordFollowUpReminder{appointmentKey, remindedFor?}  (writes .followUpReminder on a live appointment)
//	lens followUpReminders (weaver-target, full)   (freshUntil = followUpDate; remindedFor <> followUpDate gate)
//	playbook missing_followup_reminder → directOp(RecordFollowUpReminder, remindedFor: row.followUpDate)
//
// Unlike the appointment reminder (which fires 24h BEFORE a future appointment), the
// follow-up reminder fires AT the followUpDate — the visit itself is already in the
// past, and followUpDate is the soft target the provider chose, so no lead offset is
// applied. clinic-domain normalizes the captured date-only followUpDate to a full
// RFC3339 instant (anchored to 09:00:00Z) so Weaver's @at temporal lane can parse it
// as a freshUntil deadline.
const (
	followUpReminderOpDDL     = "followUpReminderOp"
	followUpReminderAspectDDL = "followUpReminder"

	// followUpReminderOp is the single follow-up operation the playbook dispatches.
	followUpReminderOp = "RecordFollowUpReminder"

	// FollowUpRemindersTarget is the §10.8 TargetID == the followUpReminders lens's
	// OutputKeyPattern prefix (the §10.2↔§10.8 binding Weaver reads).
	FollowUpRemindersTarget = "followUpReminders"
)

// followUpReminderDDLs returns the follow-up reminder's two DDL meta-vertices (the
// RecordFollowUpReminder op handler + the .followUpReminder aspect-type gate) — the
// appointment-reminder mirror, attaching a SECOND marker aspect onto clinic-domain's
// appointment.
func followUpReminderDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordFollowUpReminderVertexTypeDDL(),
		followUpReminderAspectTypeDDL(),
	}
}

// recordFollowUpReminderVertexTypeDDL owns the RecordFollowUpReminder script — the
// directOp the followUpReminders playbook dispatches when missing_followup_reminder
// opens (the encounter's followUpDate deadline passed). It writes
// vtx.appointment.<id>.followUpReminder = {sentAt, remindedFor} on a LIVE
// appointment, read-guarded on [appointmentKey] (never marks a follow-up reminder on
// an absent/tombstoned appointment), as an UNCONDITIONED update (idempotent in
// effect, re-run-safe under at-least-once — the RecordAppointmentReminder idiom).
// It also fires the actual notification send off its own transactional outbox
// (notifications.go) — the RecordAppointmentReminder idiom.
func recordFollowUpReminderVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     followUpReminderOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{followUpReminderOp},
		Description: "Follow-up-reminder op handler (clinic-reminders). RecordFollowUpReminder{appointmentKey, remindedFor?} writes " +
			"vtx.appointment.<NanoID>.followUpReminder = {sentAt, remindedFor} on a LIVE appointment (class followUpReminder), recording that " +
			"the follow-up reminder fired for the followUpDate in remindedFor. It is the directOp the followUpReminders §10.8 playbook dispatches when the " +
			"missing_followup_reminder gap opens (the documented visit's .encounter.followUpDate deadline passed); the playbook supplies remindedFor = row.followUpDate so a later " +
			"re-documentation that moves the followUpDate re-opens the gap and re-arms the reminder. Reads [appointmentKey] to " +
			"liveness-guard the parent. The write is an UNCONDITIONED update (create-if-absent / overwrite-if-present), so it is idempotent in effect and re-run-safe " +
			"under at-least-once. Submitted under Weaver's service-actor authority. Mints NO vertex of its own type (the " +
			"freshnessMarker idiom). It also emits external.notification off its own outbox (keyed on appointmentKey:" +
			"remindedFor) so the bridge's \"notification\" adapter actually sends; see RecordFollowUpReminderNotification " +
			"(notifications.go) for the replyOp that records the outcome.",
		Script: recordFollowUpReminderScript,
		InputSchema: `{"type":"object","properties":` +
			`{"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> whose documented visit requested the follow-up (required; validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"remindedFor":{"type":"string","description":"The encounter followUpDate (RFC3339, canonical UTC) this reminder is for (optional; the playbook supplies row.followUpDate). Recorded so a re-documented follow-up date re-arms the reminder."}},` +
			`"required":["appointmentKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the follow-up reminder marker was written on."}}}`,
		FieldDescription: map[string]string{
			"appointmentKey": "Full vtx.appointment.<NanoID> key whose visit requested the follow-up. RecordFollowUpReminder validates it is alive and writes the .followUpReminder aspect on it. The caller MUST list this key in ContextHint.Reads.",
			"remindedFor":    "The encounter followUpDate (RFC3339, canonical UTC) this reminder is for. The followUpReminders playbook supplies it as row.followUpDate; stored on .followUpReminder so the convergence gate (remindedFor <> followUpDate) re-opens — and re-arms the reminder — when a re-documentation moves the followUpDate.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordFollowUpReminder — mark a follow-up reminder as sent for a followUpDate",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "remindedFor": "2027-01-15T09:00:00Z"},
				ExpectedOutcome: "Validates the appointment is alive, then writes vtx.appointment.<NanoID>.followUpReminder = {sentAt: " +
					"op.submittedAt (canonical UTC), remindedFor} as an unconditioned update, emits clinic.followUpReminderSent, and returns " +
					"primaryKey. Re-runs cleanly (idempotent in effect). Rejects with a ScriptError if the appointment is absent / tombstoned.",
			},
		},
	}
}

// followUpReminderAspectTypeDDL declares the .followUpReminder aspect (class
// followUpReminder) — the step-6 write gate for RecordFollowUpReminder.
// Declaration-only (the script lives on the followUpReminderOp vertexType DDL).
// NON-sensitive: only a fire timestamp + the followUpDate it reminded for (no PHI —
// the clinical reason for the follow-up lives in the unprojected .encounter.plan).
func followUpReminderAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     followUpReminderAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{followUpReminderOp},
		Description: "Follow-up reminder marker aspect (clinic-reminders). Stored as vtx.appointment.<NanoID>.followUpReminder " +
			"(class followUpReminder) = {sentAt, remindedFor}. Non-sensitive (a fire timestamp + the followUpDate it reminded for; the " +
			"clinical reason lives in the unprojected .encounter.plan). Written ONLY by RecordFollowUpReminder (whose " +
			"followUpReminderOp vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler. remindedFor = the encounter followUpDate this reminder was for; the gate " +
			"(remindedFor = followUpDate) closing it converges, and a re-documented follow-up date re-opens it.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"sentAt":{"type":"string","description":"RFC3339 instant the follow-up reminder fired (the op's submittedAt, canonical UTC)."},` +
			`"remindedFor":{"type":"string","description":"The encounter followUpDate (RFC3339, canonical UTC) this reminder was for."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"sentAt":      "RFC3339 instant the follow-up reminder fired (op.submittedAt, canonical UTC).",
			"remindedFor": "The encounter followUpDate (RFC3339, canonical UTC) this reminder was for. remindedFor = the current followUpDate closes the convergence gap; a re-documented follow-up date re-opens it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "follow-up reminder marker aspect",
				Payload:         map[string]any{"sentAt": "2027-01-15T09:00:00Z", "remindedFor": "2027-01-15T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.followUpReminder; written by RecordFollowUpReminder.",
			},
		},
	}
}

// recordFollowUpReminderScript handles RecordFollowUpReminder. It mirrors
// recordReminderScript (read-guard the appointment root → unconditioned marker
// write) for the .followUpReminder aspect. Self-contained Starlark (its own helper
// defs) so it runs independently of the appointment-reminder op's script.
const recordFollowUpReminderScript = `
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

    if ot == "RecordFollowUpReminder":
        appt_key = required_string(p, "appointmentKey")
        parts_of(appt_key, "appointmentKey", "appointment")

        # Liveness guard: never mark a follow-up reminder on an absent/tombstoned
        # appointment. The op hydrates [appointmentKey] (ContextHint.Reads).
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key + " is absent or tombstoned; no follow-up reminder written")

        sent_at = time.rfc3339_utc(op.submittedAt)

        # remindedFor: the encounter followUpDate this reminder is FOR (the
        # followUpReminders playbook supplies it as Params{remindedFor:
        # row.followUpDate} — already canonical UTC from clinic-domain's normalize,
        # stored verbatim so the lens's remindedFor <> followUpDate compare is
        # byte-exact). It is what makes the reminder re-arm on re-documentation.
        reminded_for = None
        if hasattr(p, "remindedFor"):
            rf = getattr(p, "remindedFor")
            if rf != None and type(rf) == type("") and len(rf.strip()) > 0:
                reminded_for = rf.strip()

        marker_key = appt_key + ".followUpReminder"
        marker = {"sentAt": sent_at}
        if reminded_for != None:
            marker["remindedFor"] = reminded_for
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "followUpReminder", "vertexKey": appt_key,
                          "localName": "followUpReminder", "isDeleted": False,
                          "data": marker}},
        ]
        events = [{"class": "clinic.followUpReminderSent",
                   "data": {"appointmentKey": appt_key, "sentAt": sent_at, "remindedFor": reminded_for}}]

        # Fire the actual notification send off this op's own transactional
        # outbox (notifications.go) — the RecordAppointmentReminder idiom (no
        # Loom pattern; the bridge's dispatch path is generic).
        if reminded_for != None:
            ext_ref = appt_key + ":" + reminded_for
            events.append({"class": "external.notification",
                            "data": {"instanceKey": ext_ref, "adapter": "notification",
                                     "replyOp": "RecordFollowUpReminderNotification",
                                     "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                     "params": {"appointmentKey": appt_key, "reminderType": "followUp", "remindedFor": reminded_for}}})

        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("followUpReminderOp DDL: unknown operationType: " + ot)
`

// followUpRemindersLens is the follow-up-reminder convergence lens — the
// appointment-reminder mirror keyed on the appointment's .encounter.followUpDate.
func followUpRemindersLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  "followUpReminders",
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           followUpRemindersSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "appointment",
			OutputKeyPattern: "followUpReminders.{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_followup_reminder", "entityKey", "freshUntil", "followUpDate", "followUpReminderSentAt", "remindedFor", "patientKey", "providerKey"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
		},
	}
}

// followUpRemindersSpec is the one-row-per-appointment follow-up-reminder
// convergence cypher. It mirrors appointmentRemindersSpec but keys on the
// .encounter.followUpDate deadline instead of .schedule.remindAt, and fires AT the
// deadline (no lead offset — the visit is already past and followUpDate is the
// provider's soft target).
//
// The four-term gate (remindedFor <> followUpDate AND followUpRequested = true AND
// followUpDate <= $now AND status <> 'cancelled'):
//
//   - remindedFor <> followUpDate — NOT yet reminded for the CURRENT follow-up date.
//     Subsumes never-reminded (no .followUpReminder → remindedFor null → null <>
//     followUpDate true in the full engine → due) AND reminded-for-a-stale-date (a
//     re-documentation moved followUpDate). A reminder sent for the current date
//     reads remindedFor = followUpDate → false → converged.
//   - followUpRequested = true — the documented visit asked for a follow-up. When no
//     visit is documented, or no follow-up was requested, .encounter.followUpDate is
//     absent → the followUpDate terms are null → not due.
//   - followUpDate <= $now — the follow-up deadline has arrived/passed (lexical
//     RFC3339 compare = chronological on canonical UTC — clinic-domain normalizes
//     the captured date-only followUpDate to a full RFC3339 instant).
//   - status <> 'cancelled' — a cancelled appointment is never reminded.
//
// freshUntil = followUpDate while followUpDate > $now (a future wake-up arming
// Weaver's @at temporal lane); once the deadline passes the gap is open and the
// gap-dispatch path owns it, so freshUntil is null — exactly ONE @at fire per
// followUpDate. forPatient / withProvider are 0..1 so the OPTIONAL walks do not fan
// out (a clean flat projection). followUpDate / followUpReminderSentAt / remindedFor
// / patientKey / providerKey are INFORMATIONAL columns; only entityKey + freshUntil
// + the two bools are load-bearing for dispatch + the temporal lane.
const followUpRemindersSpec = `MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.encounter.data.followUpRequested AS followUpRequested,
  a.encounter.data.followUpDate AS followUpDate,
  a.followUpReminder.data.sentAt AS followUpReminderSentAt,
  a.followUpReminder.data.remindedFor AS remindedFor,
  a.status.data.value AS status,
  p.key AS patientKey,
  pr.key AS providerKey,
  CASE WHEN (a.followUpReminder.data.remindedFor <> a.encounter.data.followUpDate) AND (a.encounter.data.followUpRequested = true) AND (a.status.data.value <> 'cancelled') AND (a.encounter.data.followUpDate > $now) THEN a.encounter.data.followUpDate ELSE null END AS freshUntil,
  ((a.followUpReminder.data.remindedFor <> a.encounter.data.followUpDate) AND (a.encounter.data.followUpRequested = true) AND (a.encounter.data.followUpDate <= $now) AND (a.status.data.value <> 'cancelled')) AS missing_followup_reminder,
  ((a.followUpReminder.data.remindedFor <> a.encounter.data.followUpDate) AND (a.encounter.data.followUpRequested = true) AND (a.encounter.data.followUpDate <= $now) AND (a.status.data.value <> 'cancelled')) AS violating`

// followUpRemindersTarget returns the §10.8 playbook for the follow-up reminder: the
// single missing_followup_reminder gap → directOp(RecordFollowUpReminder) over the
// appointment, supplying remindedFor = row.followUpDate so a re-documented follow-up
// date re-arms the reminder. Both entityKey and followUpDate are followUpReminders
// BodyColumns (the §10.2↔§10.8 column seam).
func followUpRemindersTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: FollowUpRemindersTarget,
		LensRef:  "followUpReminders",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_followup_reminder": {
				Action:    "directOp",
				Operation: followUpReminderOp,
				Params:    map[string]string{"appointmentKey": "row.entityKey", "remindedFor": "row.followUpDate"},
				Reads:     []string{"row.entityKey"},
			},
		},
	}
}

// followUpReminderPermission grants RecordFollowUpReminder to the operator role
// (scope any) — Weaver's service actor dispatches the directOp under operator
// authority (the appointment-reminder grant idiom).
func followUpReminderPermission() pkgmgr.PermissionSpec {
	return pkgmgr.PermissionSpec{
		OperationType: followUpReminderOp,
		Scope:         "any",
		Note:          "Grants the operator the right to submit RecordFollowUpReminder operations (orchestration-internal: the followUpReminders directOp playbook, dispatched by Weaver's service actor).",
		GrantsTo:      []string{"operator"},
	}
}
