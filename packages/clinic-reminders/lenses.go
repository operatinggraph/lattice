package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// AppointmentRemindersTarget is the §10.8 TargetID == the appointmentReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const AppointmentRemindersTarget = "appointmentReminders"

// Lenses returns the package's single weaver-target convergence lens. It is the
// inversion of lease-signing's freshness re-open: where lease projects freshUntil
// to RE-OPEN a converged gap at a deadline, this projects freshUntil = remindAt to
// OPEN the reminder gap at the deadline (see appointmentRemindersSpec).
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
				BodyColumns:      []string{"violating", "missing_reminder", "entityKey", "freshUntil", "startsAt", "remindAt", "reminderSentAt", "patientKey", "providerKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
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
//     missing_reminder violating row, NOT a timer — → .reminder.sentAt is written →
//     re-projection → reminderSentAt <> null → missing_reminder false. Converged.
//
// freshUntil arms ONLY while remindAt > $now (a future wake-up). Once the deadline
// has passed the gap is open and Weaver's gap-dispatch (violating) path owns it —
// no timer is needed, so freshUntil projects null. This means exactly ONE @at fire
// per reminder (the wake-up), and a <24h booking (remindAt already past) arms no
// timer at all: it is violating on the creation CDC and dispatched at once.
//
// The four-term gate (reminderSentAt = null AND remindAt <= $now AND
// startsAt > $now AND status <> 'cancelled'):
//
//   - reminderSentAt = null — not yet reminded (the §full-engine null idiom, NOT
//     the unsupported IS NULL; the lease-signing / objectLiveness convention).
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
//
// One-row-per-anchor: forPatient / withProvider are 0..1 (CreateAppointment writes
// exactly one of each, deterministic keys), so the OPTIONAL walks do not fan out —
// a clean flat (no-WITH) projection like clinicAppointments. patientKey /
// providerKey / startsAt / remindAt / reminderSentAt are INFORMATIONAL columns
// (operator/FE observability); only entityKey + freshUntil + the two bools are
// load-bearing for Weaver's dispatch + temporal lane.
const appointmentRemindersSpec = `MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.remindAt AS remindAt,
  a.reminder.data.sentAt AS reminderSentAt,
  p.key AS patientKey,
  pr.key AS providerKey,
  CASE WHEN (a.reminder.data.sentAt = null) AND (a.status.data.value <> 'cancelled') AND (a.schedule.data.startsAt > $now) AND (a.schedule.data.remindAt > $now) THEN a.schedule.data.remindAt ELSE null END AS freshUntil,
  ((a.reminder.data.sentAt = null) AND (a.schedule.data.remindAt <= $now) AND (a.schedule.data.startsAt > $now) AND (a.status.data.value <> 'cancelled')) AS missing_reminder,
  ((a.reminder.data.sentAt = null) AND (a.schedule.data.remindAt <= $now) AND (a.schedule.data.startsAt > $now) AND (a.status.data.value <> 'cancelled')) AS violating`
