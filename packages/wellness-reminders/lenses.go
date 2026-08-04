package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WellnessBookingRemindersTarget is the §10.8 TargetID == the
// wellnessBookingReminders lens's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads.
const WellnessBookingRemindersTarget = "wellnessBookingReminders"

// Lenses returns the package's weaver-target convergence lenses:
// wellnessBookingReminders, the ~24h-ahead class reminder (mirrors
// clinic-reminders' appointmentReminders lens, anchored on booking instead
// of appointment — a wellness session has MANY bookers, so the reminder
// marker, and hence the anchor, lives per-booking, not per-session), and
// pastDueBookings, the auto-no-show closer (pastdue.go; mirrors
// clinic-reminders' pastDueAppointments).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "wellnessBookingReminders",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           wellnessBookingRemindersSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: "wellnessBookingReminders.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_reminder", "entityKey", "freshUntil", "startsAt", "remindAt", "reminderSentAt", "remindedFor", "sessionKey", "bookerKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		pastDueBookingsLens(),
	}
}

// wellnessBookingRemindersSpec is the one-row-per-booking reminder-convergence
// cypher. It INVERTS the lease-signing freshness mechanism exactly like
// clinic-reminders' appointmentRemindersSpec: the freshUntil column arms
// Weaver's @at temporal timer (internal/weaver/temporal.go), but here the gap
// OPENS (rather than re-opens) when the deadline passes.
//
// The reminder lifecycle for one booking:
//
//   - At CreateBooking/JoinWaitlist: the booking's session already carries
//     .schedule.remindAt = startsAt − 24h, stamped by wellness-domain's
//     CreateSession/CreateSessionSeries (write-time, canonical UTC). While
//     the deadline is in the FUTURE the lens projects freshUntil = remindAt
//     → Weaver arms an @at at remindAt. missing_reminder is false.
//   - At remindAt: the @at fires → handleFiredTimer submits MarkExpired,
//     whose freshnessExpiry marker write on THIS booking re-projects the row
//     with a fresh $now → remindAt <= $now now holds → missing_reminder
//     flips true AND freshUntil goes null (a one-shot wake-up, not re-armed).
//   - Weaver dispatches directOp(RecordBookingReminder) — driven by the
//     missing_reminder violating row, NOT a timer — with
//     Params{remindedFor: row.startsAt} so the op stamps .reminder =
//     {sentAt, remindedFor = the startsAt it reminded for} → re-projection →
//     remindedFor = startsAt → missing_reminder false. Converged.
//   - On a class time move (wellness-domain's ReassignSession rewrites the
//     session's .schedule with a new startsAt + a re-derived remindAt):
//     remindedFor (the OLD startsAt) now differs from the new startsAt →
//     the gate re-opens → if the new remindAt is still future, freshUntil
//     re-arms a fresh @at; if it is already past (a <24h move),
//     missing_reminder is true at once.
//
// A booking that is waitlisted, attended, or noShow is never violating —
// only a `booked` seat gets reminded (a waitlisted booker has no confirmed
// seat to be reminded about; SetBookingAttendance moving a booking off
// `booked` means the class already happened or the booker was marked absent,
// so a reminder is moot either way).
//
// One-row-per-anchor: forSession is 0..1 (CreateBooking writes exactly one,
// deterministic keys), so the OPTIONAL walk does not fan out — a clean flat
// (no-WITH) projection like wellnessBookingsSpec. sessionKey / bookerKey /
// startsAt / remindAt / reminderSentAt / remindedFor are INFORMATIONAL
// columns (operator/FE observability); only entityKey + freshUntil + the two
// bools are load-bearing for Weaver's dispatch + temporal lane.
const wellnessBookingRemindersSpec = `MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS actorKey,
  b.key AS entityKey,
  se.key AS sessionKey,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.remindAt AS remindAt,
  b.reminder.data.sentAt AS reminderSentAt,
  b.reminder.data.remindedFor AS remindedFor,
  id.key AS bookerKey,
  CASE WHEN (b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (se.schedule.data.startsAt > $now) AND (se.schedule.data.remindAt > $now) THEN se.schedule.data.remindAt ELSE null END AS freshUntil,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (se.schedule.data.remindAt <= $now) AND (se.schedule.data.startsAt > $now)) AS missing_reminder,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (se.schedule.data.remindAt <= $now) AND (se.schedule.data.startsAt > $now)) AS violating`
