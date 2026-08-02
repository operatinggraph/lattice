package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The auto-no-show sibling of the appointment reminder: a past visit that never
// got a staff status update sits open (and unbilled) forever, since
// SetAppointmentStatus(noShow) is staff-submitted only. This convergence closes
// that gap the same way the reminder does — an @at convergence over
// clinic-domain's appointment — but INVERTS the timestamp it binds to: instead of
// a derived lead-offset deadline (remindAt = startsAt − 24h), it binds DIRECTLY to
// the appointment's own .schedule.endsAt (the unroutedTasks/orphanedTaskGrants
// idiom, orchestration-base — a mutable business timestamp IS the staleness
// threshold, no separate marker needed).
//
//	lens pastDueAppointments (weaver-target, full)  (freshUntil = endsAt; status non-terminal AND endsAt <= $now gate)
//	playbook missing_noshow_transition → directOp(MarkPastDueNoShow, appointmentKey: row.entityKey)
//
// MarkPastDueNoShow (clinic-domain, appointmentDDLScript) is a DEDICATED
// operationType, not a directOp against SetAppointmentStatus itself: that op
// requires provider + patient as CALLER-SUPPLIED params validated against the
// withProvider/forPatient LINKS (require_matching_provider/patient), and a
// GapActionSpec's Reads can only template row.<column> / row.<column>.<aspect>
// keys (Contract #10 §10.8) — it cannot express that link read. MarkPastDueNoShow
// instead resolves provider/patient LIVE off the appointment's own links (the
// same bounded read-posture (e) appointment_provider already uses elsewhere in
// clinic-domain's script), so the playbook here only ever needs to route the
// appointment key itself.
const (
	// PastDueAppointmentsTarget is the §10.8 TargetID == the pastDueAppointments
	// lens's OutputKeyPattern prefix (the §10.2↔§10.8 binding Weaver reads).
	PastDueAppointmentsTarget = "pastDueAppointments"

	// pastDueNoShowOp is the clinic-domain operation this package's playbook
	// dispatches — owned by clinic-domain (its own appointment vertexType DDL),
	// not this package, since it must reuse clinic-domain's own
	// release_cells_mutations / appointment_provider / appointment_patient
	// helpers, which are private to that script.
	pastDueNoShowOp = "MarkPastDueNoShow"
)

// pastDueAppointmentsLens is the auto-no-show convergence lens.
func pastDueAppointmentsLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  "pastDueAppointments",
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           pastDueAppointmentsSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "appointment",
			OutputKeyPattern: "pastDueAppointments.{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_noshow_transition", "entityKey", "freshUntil", "endsAt", "status", "patientKey", "providerKey"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
		},
	}
}

// pastDueAppointmentsSpec is the one-row-per-appointment auto-no-show convergence
// cypher. Unlike appointmentRemindersSpec (freshUntil = a DERIVED lead-offset
// deadline), this binds freshUntil DIRECTLY to .schedule.endsAt — the
// unroutedTasks idiom (orchestration-base): the appointment's own end-of-visit
// timestamp already IS the staleness threshold, so no separate marker/derived
// field is needed. While endsAt is in the future the lens arms a one-shot @at at
// endsAt; once it passes, the gap OPENS (not a timer wake-up — the violating row
// itself drives dispatch, the appointmentReminders idiom).
//
// The three-term gate (status is non-terminal AND endsAt <= $now):
//
//   - status <> 'completed' AND status <> 'cancelled' AND status <> 'noShow' —
//     the appointment has NOT already reached a terminal outcome. A staff status
//     update (checked in and completed, or cancelled) at any point before endsAt
//     — or even after, racing the @at fire — converges the gate permanently
//     (TERMINAL_STATUSES are final, clinic-domain ddls.go); MarkPastDueNoShow
//     itself also no-ops defensively against this exact race (a dispatch that
//     lands after a legitimate terminal transition beat it here).
//   - endsAt <= $now — the visit's scheduled end has passed with no terminal
//     status recorded.
//
// freshUntil = endsAt while endsAt > $now (arms the @at); once due, freshUntil is
// null (the gap-dispatch path owns it, same as appointmentReminders). One-row-
// per-anchor: forPatient / withProvider are 0..1 (CreateAppointment writes
// exactly one of each), so the OPTIONAL walks do not fan out. patientKey /
// providerKey / status are INFORMATIONAL (operator/FE observability) — the
// playbook does not template them (MarkPastDueNoShow resolves them live); only
// entityKey + freshUntil + the two bools are load-bearing for dispatch + the
// temporal lane.
const pastDueAppointmentsSpec = `MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.schedule.data.endsAt AS endsAt,
  a.status.data.value AS status,
  p.key AS patientKey,
  pr.key AS providerKey,
  CASE WHEN (a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt > $now) THEN a.schedule.data.endsAt ELSE null END AS freshUntil,
  ((a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt <= $now)) AS missing_noshow_transition,
  ((a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt <= $now)) AS violating`

// pastDueAppointmentsTarget returns the §10.8 playbook for the auto-no-show
// convergence: the single missing_noshow_transition gap → directOp(MarkPastDueNoShow)
// over the appointment, routing only the candidate key — MarkPastDueNoShow
// resolves provider/patient itself, so no row.<column> beyond entityKey is
// needed (unlike appointmentRemindersTarget's remindedFor param).
func pastDueAppointmentsTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: PastDueAppointmentsTarget,
		LensRef:  "pastDueAppointments",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_noshow_transition": {
				Action:    "directOp",
				Operation: pastDueNoShowOp,
				Params:    map[string]string{"appointmentKey": "row.entityKey"},
				Reads:     []string{"row.entityKey"},
			},
		},
	}
}
