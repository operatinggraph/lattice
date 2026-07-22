package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// AppointmentRemindersTarget is the §10.8 TargetID == the appointmentReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const AppointmentRemindersTarget = "appointmentReminders"

// Lenses returns the package's weaver-target convergence lenses: appointmentReminders
// (the ~24h-ahead appointment reminder), followUpReminders (the at-the-date
// follow-up reminder, followups.go), and visitSeriesDue (the rolling recurring
// visit-series deadline, visitseries.go). The first two invert lease-signing's
// freshness re-open — where lease projects freshUntil to RE-OPEN a converged gap at
// a deadline, these project freshUntil = the deadline to OPEN the reminder gap when
// it passes (see appointmentRemindersSpec / followUpRemindersSpec). visitSeriesDue
// applies the same inversion but never converges to a permanent close — each
// AdvanceVisitSeries re-arms a NEW future freshUntil, rolling the series forward.
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
		visitSeriesReadLens(),
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
//     clinic-domain, write-time, canonical UTC). While the deadline is in the
//     FUTURE the lens projects freshUntil = remindAt → Weaver arms an @at at
//     remindAt. missing_reminder is false (nothing to do yet).
//   - At remindAt: the @at fires → handleFiredTimer submits MarkExpired, whose
//     freshnessExpiry marker write on THIS appointment re-projects the row with a
//     fresh $now → remindAt <= $now now holds → missing_reminder flips true AND
//     freshUntil goes null (the deadline is no longer in the future; the timer was
//     a one-shot wake-up and is not re-armed).
//   - Weaver dispatches directOp(RecordAppointmentReminder) — driven by the
//     missing_reminder violating row, NOT a timer — with Params{remindedFor:
//     row.startsAt} so the op stamps .reminder = {sentAt, remindedFor = the
//     startsAt it reminded for} → re-projection → remindedFor = startsAt →
//     missing_reminder false. Converged.
//   - On RESCHEDULE (clinic-domain RescheduleAppointment rewrites .schedule with a
//     new startsAt + a re-derived remindAt): remindedFor (the OLD startsAt) now
//     differs from the new startsAt → the gate re-opens → if the new remindAt is
//     still future, freshUntil = remindAt re-arms a fresh @at; if it is already
//     past (a <24h move), missing_reminder is true at once. The new reminder
//     dispatch stamps remindedFor = the new startsAt → converged again.
//
// freshUntil arms ONLY while remindAt > $now (a future wake-up). Once the deadline
// has passed the gap is open and Weaver's gap-dispatch (violating) path owns it —
// no timer is needed, so freshUntil projects null. This means exactly ONE @at fire
// per (startsAt) reminder, and a <24h booking (remindAt already past) arms no
// timer at all: it is violating on the creation CDC and dispatched at once.
//
// The four-term gate (remindedFor <> startsAt AND remindAt <= $now AND
// startsAt > $now AND status <> 'cancelled'):
//
//   - remindedFor <> startsAt — NOT yet reminded for the CURRENT scheduled time.
//     This single term subsumes never-reminded (no .reminder aspect → remindedFor
//     resolves null → null <> startsAt is true in the full engine → due) AND
//     reminded-for-a-stale-time (a reschedule moved startsAt away from the recorded
//     remindedFor → due again). A reminder sent for the current startsAt reads
//     remindedFor = startsAt → false → converged. (sentAt stays as a purely
//     informational "when did it fire" column; the gate keys on remindedFor.)
//   - remindAt <= $now — the reminder deadline has passed. Lexical RFC3339 compare
//     = chronological on canonical UTC (the proven validUntil > $now idiom).
//   - startsAt > $now — the appointment is still in the future (never remind for a
//     past appointment).
//   - status <> 'cancelled' — a cancelled appointment is never reminded.
//
// Edge cases fall out of the same predicate: a booking < 24h out has a past
// remindAt → reminds immediately; a cancelled/past appointment is never violating
// and projects freshUntil null (no armed timer); an old appointment with no
// remindAt (pre-feature) reads null <= $now → false → silently no reminder.
// (A reminder recorded by a pre-`remindedFor` build carries no remindedFor → it
// reads as stale once and is re-sent once on the next due projection, then sticks.)
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
  CASE WHEN (a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.status.data.value <> 'cancelled') AND (a.schedule.data.startsAt > $now) AND (a.schedule.data.remindAt > $now) THEN a.schedule.data.remindAt ELSE null END AS freshUntil,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.schedule.data.remindAt <= $now) AND (a.schedule.data.startsAt > $now) AND (a.status.data.value <> 'cancelled')) AS missing_reminder,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.schedule.data.remindAt <= $now) AND (a.schedule.data.startsAt > $now) AND (a.status.data.value <> 'cancelled')) AS violating`
