package wellnessreminders

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

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
				BodyColumns:      []string{"violating", "missing_reminder", "entityKey", "freshUntil", "startsAt", "endsAt", "remindAt", "reminderSentAt", "remindedFor", "status", "sessionKey", "bookerKey"},
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
// closes the gate at that point. The nil-false lands on the right side: while
// nothing has fired, NOT(false) leaves the gap open, which is the default a
// not-yet-ended class needs.
//
// That open window is NOT free. This target declares no maxretries_reminder, so
// Weaver applies defaultDirectOpRetryBudget = 3
// (internal/weaver/evaluator.go:1379) against a 30-minute mark lease and a
// 1-minute sweep (internal/weaver/reconciler.go:17,21): a class running much
// past an hour spends the budget before it ends, and escalateExhaustedGap
// (evaluator.go:1572) raises a standing per-(target, entity, gap)
// GapBudgetExhausted warning. What the recorded end does is CLOSE the column,
// and a closed column retires that latch — handleRow's closed-gap leg calls
// retireClosedGapIssues (evaluator.go:1064), which clears
// issueKeyGapEntity(targetId, entityId, gapColumn), the exact key the warning
// stands on (evaluator.go:1191); the sweep legs retire through the same
// function (reconciler.go:568, :792, :1237). So the standing ISSUE is bounded
// by the class, and the DISPATCHES are bounded by the budget.
//
// Both booking lenses gate on status = 'booked', so — unlike the appointment
// pair, which must agree on a terminal-status EXCLUSION list — the coupling here
// is an equality on one value and cannot drift apart into a status this lens
// admits and the marker-writing one does not.
//
// One-row-per-anchor: forSession is 0..1 (CreateBooking writes exactly one,
// deterministic keys), so the OPTIONAL walk does not fan out — a clean flat
// (no-WITH) projection like wellnessBookingsSpec. sessionKey / bookerKey /
// startsAt / remindAt / reminderSentAt / remindedFor are INFORMATIONAL
// columns (operator/FE observability); only entityKey + freshUntil + the two
// bools are load-bearing for Weaver's dispatch + temporal lane.
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors'
// static reach; its advisory asks for a hand check for a narrowing range
// bound inside a NEGATED pattern, and there is none — the cypher has no
// negated relationship pattern at all, only scalar NOT comparisons.
var wellnessBookingRemindersSpec = fmt.Sprintf(`MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS actorKey,
  b.key AS entityKey,
  se.key AS sessionKey,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.remindAt AS remindAt,
  b.reminder.data.sentAt AS reminderSentAt,
  b.reminder.data.remindedFor AS remindedFor,
  b.status.data.value AS status,
  id.key AS bookerKey,
  CASE WHEN (b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt) AND NOT (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) THEN se.schedule.data.remindAt ELSE null END AS freshUntil,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS missing_reminder,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS violating`,
	PastDueBookingsTarget, WellnessBookingRemindersTarget)
