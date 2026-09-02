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
//     CreateSession/CreateSessionSeries (write-time, canonical UTC). While no
//     timer has recorded a lapse at that deadline the lens projects
//     freshUntil = remindAt → Weaver arms an @at at remindAt. missing_reminder
//     is false.
//   - At remindAt: the @at fires → handleFiredTimer submits MarkExpired, whose
//     freshnessExpiry marker write on THIS booking records the fired instant
//     under this target's byTarget key AND re-projects the row → the recorded
//     lapse now reaches remindAt → missing_reminder flips true AND freshUntil
//     goes null (a one-shot wake-up, not re-armed).
//   - Weaver dispatches directOp(RecordBookingReminder) — driven by the
//     missing_reminder violating row, NOT a timer — with
//     Params{remindedFor: row.startsAt} so the op stamps .reminder =
//     {sentAt, remindedFor = the startsAt it reminded for} → re-projection →
//     remindedFor = startsAt → missing_reminder false. Converged.
//   - On a class time move (wellness-domain's ReassignSession rewrites the
//     session's .schedule with a new startsAt + a re-derived remindAt):
//     remindedFor (the OLD startsAt) now differs from the new startsAt →
//     the gate re-opens → the new remindAt outruns any instant already
//     recorded, so freshUntil re-arms a fresh @at with no clearing write at
//     all; if the new remindAt is already past, the row projects it verbatim
//     and the overdue @at fires immediately.
//
// The lens reads NO clock. Both operands of every time comparison are stored
// graph data, so the row is a pure function of the subgraph and two
// projections at different wall-clock instants over the same graph agree.
//
// A booking that is waitlisted, attended, or noShow is never violating —
// only a `booked` seat gets reminded (a waitlisted booker has no confirmed
// seat to be reminded about; SetBookingAttendance moving a booking off
// `booked` means the class already happened or the booker was marked absent,
// so a reminder is moot either way).
//
// "Never remind for a class that has already started" is NOT a term of this
// gate — it is RecordBookingReminder's own guard
// (time.rfc3339_utc(op.submittedAt) < startsAt, ddls.go), which refuses the
// write once the class has begun. No timer arms at startsAt (§7 role (c): a
// second deadline with no schedule slot to carry it), so between startsAt and
// endsAt a never-reminded booked seat stays `missing_reminder` here and the op
// declines each dispatch — rather than the gate silently closing on a reminder
// that never went out.
//
// "The class is OVER", by contrast, IS a term, and a recorded one: the sibling
// pastDueBookings target arms its own @at at the session's endsAt on this same
// booking anchor, so its fired marker entry is the evidence the class ended.
// NOT (b.freshnessExpiry.data.byTarget.pastDueBookings >= se.schedule.data.endsAt)
// closes the gate at that point, which bounds the op's refusals by the class's
// own length instead of letting them run until the retry budget escalates. The
// nil-false lands on the right side: while nothing has fired, NOT(false) leaves
// the gap open, which is the default a not-yet-ended class needs.
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
  CASE WHEN (b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.pastDueBookings >= se.schedule.data.endsAt) AND NOT (b.freshnessExpiry.data.byTarget.wellnessBookingReminders >= se.schedule.data.remindAt) THEN se.schedule.data.remindAt ELSE null END AS freshUntil,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (b.freshnessExpiry.data.byTarget.wellnessBookingReminders >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.pastDueBookings >= se.schedule.data.endsAt)) AS missing_reminder,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (b.freshnessExpiry.data.byTarget.wellnessBookingReminders >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.pastDueBookings >= se.schedule.data.endsAt)) AS violating`
