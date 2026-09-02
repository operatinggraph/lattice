package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// AppointmentRemindersTarget is the §10.8 TargetID == the appointmentReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const AppointmentRemindersTarget = "appointmentReminders"

// Lenses returns the package's weaver-target convergence lenses: appointmentReminders
// (the ~24h-ahead appointment reminder), followUpReminders (the at-the-date
// follow-up reminder, followups.go), visitSeriesDue (the rolling recurring
// visit-series deadline, visitseries.go), visitSeriesSiteBackfill (the series'
// missing atSite link, visitseries_site.go — the one gap here that is not
// deadline-driven at all: it converges a MISSING RELATIONSHIP, the
// clinicSiteBackfill idiom), and pastDueAppointments (the auto
// no-show closer, pastdue.go). The first two invert lease-signing's
// freshness re-open — where lease projects freshUntil to RE-OPEN a converged gap at
// a deadline, these project freshUntil = the deadline to OPEN the reminder gap when
// it passes (see appointmentRemindersSpec / followUpRemindersSpec). visitSeriesDue
// applies the same inversion but never converges to a permanent close — each
// AdvanceVisitSeries re-arms a NEW future freshUntil, rolling the series forward.
// pastDueAppointments applies the same inversion a THIRD way: it binds freshUntil
// DIRECTLY to a mutable business timestamp (.schedule.endsAt) rather than a
// derived lead-offset deadline (the unroutedTasks idiom, orchestration-base).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "appointmentReminders",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           appointmentRemindersSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "appointment",
				OutputKeyPattern: "appointmentReminders.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_reminder", "entityKey", "freshUntil", "startsAt", "remindAt", "reminderSentAt", "remindedFor", "patientKey", "providerKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		followUpRemindersLens(),
		visitSeriesDueLens(),
		visitSeriesSiteBackfillLens(),
		visitSeriesReadLens(),
		pastDueAppointmentsLens(),
	}
}

// appointmentRemindersSpec is the one-row-per-appointment reminder-convergence
// cypher. It INVERTS the lease-signing freshness mechanism: the freshUntil column
// arms Weaver's @at temporal timer (internal/weaver/temporal.go), but here the gap
// OPENS (rather than re-opens) when the deadline passes.
//
// The reminder lifecycle for one appointment:
//
//   - At CreateAppointment: .schedule.remindAt = startsAt − 24h is stamped (by
//     clinic-domain, write-time, canonical UTC). While no timer has recorded a
//     lapse at that deadline the lens projects freshUntil = remindAt → Weaver
//     arms an @at at remindAt. missing_reminder is false (nothing to do yet).
//   - At remindAt: the @at fires → handleFiredTimer submits MarkExpired, whose
//     freshnessExpiry marker write on THIS appointment records the fired instant
//     under this target's byTarget key AND re-projects the row → the recorded
//     lapse now reaches remindAt → missing_reminder flips true AND freshUntil
//     goes null (the deadline has lapsed; the timer was a one-shot wake-up and
//     is not re-armed).
//   - Weaver dispatches directOp(RecordAppointmentReminder) — driven by the
//     missing_reminder violating row, NOT a timer — with Params{remindedFor:
//     row.startsAt} so the op stamps .reminder = {sentAt, remindedFor = the
//     startsAt it reminded for} → re-projection → remindedFor = startsAt →
//     missing_reminder false. Converged.
//   - On RESCHEDULE (clinic-domain RescheduleAppointment rewrites .schedule with a
//     new startsAt + a re-derived remindAt): remindedFor (the OLD startsAt) now
//     differs from the new startsAt → the gate re-opens → if the new remindAt is
//     unlapsed, freshUntil = the new remindAt arms a fresh @at; if the new
//     remindAt is already past, the row projects it verbatim, Weaver publishes
//     an overdue @at that fires at once, and the recorded lapse opens the gap on
//     the next delivery. The new reminder dispatch stamps remindedFor = the new
//     startsAt → converged again.
//
// The lens reads NO clock. Both operands of every time comparison are stored
// graph data, so the row is a pure function of the subgraph and two projections
// at different wall-clock instants over the same graph agree — which is what
// makes the sweep's deep-verify comparison meaningful.
//
// freshUntil arms while this target has recorded no lapse reaching remindAt.
// Once the lapse is recorded the gap is open and Weaver's gap-dispatch
// (violating) path owns it — no timer is needed, so freshUntil projects null.
// That is exactly ONE @at fire per (startsAt) reminder. A <24h booking has a
// remindAt already in the past and no marker, so it projects that past instant
// verbatim: Weaver publishes an overdue @at, NATS releases it immediately, and
// the lapse is recorded on the spot (internal/weaver/temporal.go). Nulling a
// past deadline here would arm nothing and the gap would never open at all.
//
// The four-term gate (remindedFor <> startsAt AND a recorded lapse at remindAt
// AND status <> 'cancelled' AND no recorded lapse at endsAt):
//
//   - remindedFor <> startsAt — NOT yet reminded for the CURRENT scheduled time.
//     This single term subsumes never-reminded (no .reminder aspect → remindedFor
//     resolves null → null <> startsAt is true in the full engine → due) AND
//     reminded-for-a-stale-time (a reschedule moved startsAt away from the recorded
//     remindedFor → due again). A reminder sent for the current startsAt reads
//     remindedFor = startsAt → false → converged. (sentAt stays as a purely
//     informational "when did it fire" column; the gate keys on remindedFor.)
//   - freshnessExpiry.data.byTarget.appointmentReminders >= remindAt — a timer
//     armed by THIS target fired at or after the reminder deadline. Lexical
//     RFC3339 compare = chronological on canonical UTC. compareAny answers false
//     whenever either operand is nil, so an appointment no timer has fired on,
//     and one carrying no remindAt at all, both read not-due.
//   - status <> 'cancelled' — a cancelled appointment is never reminded.
//   - NOT (freshnessExpiry.data.byTarget.pastDueAppointments >= endsAt) — the
//     visit has not ended. "Never remind for an appointment that is over" is a
//     recorded fact, not a clock reading: the sibling pastDueAppointments target
//     arms its own @at at endsAt on this same anchor, and its fired marker is the
//     evidence the appointment ended. The nil-false lands on the right side —
//     while nothing has fired, NOT(false) leaves the gap open, which is the
//     default a not-yet-ended appointment needs.
//
// Between startsAt and endsAt the gap therefore stays open and
// RecordAppointmentReminder's own guard (time.rfc3339_utc(op.submittedAt) <
// startsAt, ddls.go) refuses each dispatch — the op declines, the lens keeps
// projecting the row. Once the visit ends the past-due lapse closes the gate,
// so the refusals are bounded by the visit's own length rather than running
// until the retry budget escalates.
//
// Edge cases: a booking < 24h out has a past remindAt → reminds on the overdue
// @at; a cancelled appointment is never violating and projects freshUntil null
// (no armed timer); an old appointment with no remindAt (pre-feature) has
// nothing to compare against and never reads due. (A reminder recorded by a
// pre-`remindedFor` build carries no remindedFor → it reads as stale once and
// is re-sent once on the next due projection, then sticks.)
//
// One-row-per-anchor: forPatient / withProvider are 0..1 (CreateAppointment writes
// exactly one of each, deterministic keys), so the OPTIONAL walks do not fan out —
// a clean flat (no-WITH) projection like clinicAppointments. patientKey /
// providerKey / startsAt / remindAt / reminderSentAt / remindedFor are INFORMATIONAL
// columns (operator/FE observability); only entityKey + freshUntil + the two bools
// are load-bearing for Weaver's dispatch + temporal lane.
const appointmentRemindersSpec = `MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.remindAt AS remindAt,
  a.reminder.data.sentAt AS reminderSentAt,
  a.reminder.data.remindedFor AS remindedFor,
  p.key AS patientKey,
  pr.key AS providerKey,
  CASE WHEN (a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.status.data.value <> 'cancelled') AND NOT (a.freshnessExpiry.data.byTarget.pastDueAppointments >= a.schedule.data.endsAt) AND NOT (a.freshnessExpiry.data.byTarget.appointmentReminders >= a.schedule.data.remindAt) THEN a.schedule.data.remindAt ELSE null END AS freshUntil,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.freshnessExpiry.data.byTarget.appointmentReminders >= a.schedule.data.remindAt) AND (a.status.data.value <> 'cancelled') AND NOT (a.freshnessExpiry.data.byTarget.pastDueAppointments >= a.schedule.data.endsAt)) AS missing_reminder,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.freshnessExpiry.data.byTarget.appointmentReminders >= a.schedule.data.remindAt) AND (a.status.data.value <> 'cancelled') AND NOT (a.freshnessExpiry.data.byTarget.pastDueAppointments >= a.schedule.data.endsAt)) AS violating`
