package wellnessreminders

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// WellnessBookingRemindersTarget is the §10.8 TargetID == the
// wellnessBookingReminders lens's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads.
const WellnessBookingRemindersTarget = "wellnessBookingReminders"

// WellnessBookingChangeNoticesTarget is the §10.8 TargetID == the
// wellnessBookingChangeNotices lens's OutputKeyPattern prefix — the
// §10.2↔§10.8 binding Weaver reads.
const WellnessBookingChangeNoticesTarget = "wellnessBookingChangeNotices"

// Lenses returns the package's weaver-target convergence lenses:
// wellnessBookingReminders, the ~24h-ahead class reminder (mirrors
// clinic-reminders' appointmentReminders lens, anchored on booking instead
// of appointment — a wellness session has MANY bookers, so the reminder
// marker, and hence the anchor, lives per-booking, not per-session);
// wellnessBookingChangeNotices, the level-triggered promotion / time-move
// notice (changenotice.go owns the op; the lens is below); and
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
		{
			CanonicalName:  "wellnessBookingChangeNotices",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           wellnessBookingChangeNoticesSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: "wellnessBookingChangeNotices.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_promotion_notice", "missing_move_notice", "entityKey", "sessionKey", "bookerKey", "status", "startsAt", "endsAt", "classStartsAt", "promotedAt", "className", "promotedFor", "movedFor", "noticeSentAt"},
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
// A seat claimed AFTER the class began is never reminded. The desk may seat a
// walk-in at the door until the class ends (wellness-domain's CreateBooking
// admits a staff or operator submission until .schedule.endsAt), and that
// booking's remindAt is a day in the past at the instant it is written: with
// no such term the row would arm an already-overdue @at, the fire would open
// missing_reminder, RecordBookingReminder would refuse ClassAlreadyStarted
// on every dispatch, and the retry budget would leave a GapBudgetExhausted
// warning standing until the class ended — all for a member who is already
// in the room. So freshUntil and both gap columns carry
// NOT (b.status.data.bookedAt >= se.schedule.data.startsAt): bookedAt is the
// claim stamp CreateBooking/JoinWaitlist write on .status and every later
// writer carries (wellness-domain ddls.go), and a claim at or after the start
// arms nothing and opens nothing. The comparison is two-valued like every
// other here — `null >= x` is false, so a legacy seat that carries no
// bookedAt reads NOT(false) and keeps today's behaviour, reminded exactly as
// before the stamp existed.
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
  CASE WHEN (b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND NOT (b.status.data.bookedAt >= se.schedule.data.startsAt) AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt) AND NOT (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) THEN se.schedule.data.remindAt ELSE null END AS freshUntil,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND NOT (b.status.data.bookedAt >= se.schedule.data.startsAt) AND (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS missing_reminder,
  ((b.reminder.data.remindedFor <> se.schedule.data.startsAt) AND (b.status.data.value = 'booked') AND NOT (b.status.data.bookedAt >= se.schedule.data.startsAt) AND (b.freshnessExpiry.data.byTarget.%[2]s >= se.schedule.data.remindAt) AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS violating`,
	PastDueBookingsTarget, WellnessBookingRemindersTarget)

// wellnessBookingChangeNoticesSpec is the one-row-per-booking change-notice
// convergence cypher: a member whose seat was handed over from the waitlist,
// or whose class was moved to a new time, is told once per change. Unlike
// the reminder spec above it projects NO freshUntil and arms NO timer — both
// gaps are level-triggered states over recorded facts, not deadlines: a
// promotion is `promotedAt` present, a move is the session's startsAt having
// drifted from the time the member was last told. There is nothing to wait
// for; the row is violating the moment the fact lands and converged the
// moment the notice is recorded.
//
// The lifecycle for one booking:
//
//   - PromoteWaitlistedBookings (wellness-domain) stamps .status.promotedAt
//     on the seat it hands over; that instant never changes again. With no
//     .changeNotice yet, promotedFor is null and `null <> promotedAt` is true
//     → missing_promotion_notice opens. RecordBookingChangeNotice{kind:
//     promoted, changeRef: row.promotedAt} writes .changeNotice.promotedFor =
//     promotedAt → the equality closes the gap. Because promotedAt is written
//     once, this gap converges exactly once per booking.
//   - CreateBooking / JoinWaitlist snapshot the class time the seat was
//     claimed for as .status.classStartsAt, and every later .status writer
//     carries it. ReassignSession rewrites the session's .schedule.startsAt
//     and records nothing about the move, so "moved since the member was
//     told" is startsAt <> coalesce(movedFor, classStartsAt): movedFor is the
//     startsAt the last move notice was for, and classStartsAt is the time
//     the member booked against — the coalesce is "the last time the member
//     was told", whichever notice that was. RecordBookingChangeNotice{kind:
//     moved, changeRef: row.startsAt} writes movedFor = startsAt → closed. A
//     SECOND move makes startsAt drift from the recorded movedFor → the gap
//     reopens and a fresh notice (a fresh externalRef) goes out.
//   - classStartsAt <> null guards the move gap: a booked seat claimed
//     before the snapshot existed carries no classStartsAt, so coalesce
//     would resolve null and `startsAt <> null` would read TRUE — a false
//     "your class moved" to a member whose class never moved. Such a seat
//     stays quiet for every move rather than being told a fiction once:
//     with no baseline there is nothing to compare the current time against.
//   - se.schedule.data.startsAt <> null guards BOTH gaps: a tombstoned
//     session unbinds the OPTIONAL forSession walk (the rule engine drops a
//     dead neighbour), so a called-off class projects null startsAt for
//     every seat until wellness-domain's release drains it. Without the
//     guard the move gap would read `null <> classStartsAt` as a move, and
//     the promotion gap would dispatch an op whose sessionKey param is null.
//     The call-off notice is ReleaseOrphanedBooking's own, emitted in the
//     batch that tombstones the booking (wellness-domain) — never this lens's.
//   - status = 'booked': only a confirmed seat is told. A waitlisted booker
//     has no seat to be moved or promoted into yet; attended / noShow means
//     the class already happened.
//   - NOT (b.freshnessExpiry.data.byTarget.pastDueBookings >= endsAt) — the
//     class is OVER, a recorded fact from the sibling pastDueBookings
//     target's fired @at on this same booking anchor (exactly the conjunct
//     the reminder spec reads). A notice for a class that has ended is moot,
//     and the closed column retires any GapBudgetExhausted latch the open
//     window accumulated. While nothing has fired, NOT(false) leaves the
//     gaps open — the default a not-yet-ended class needs.
//
// Every operand is stored graph data; the lens reads no clock. `<>` is the
// engine's two-valued null test (null <> 'x' true, null <> null false), which
// is what lets the absent-marker case open the gap and the absent-fact case
// (no promotedAt, no classStartsAt) keep it closed. violating repeats both
// gap expressions verbatim — the engine has no column references in RETURN.
//
// One-row-per-anchor: forSession / bookedBy are 0..1 (CreateBooking /
// JoinWaitlist write exactly one of each), so the OPTIONAL walks do not fan
// out. bookerKey / className / noticeSentAt / endsAt are INFORMATIONAL;
// entityKey, sessionKey, promotedAt, startsAt and the three bools are
// load-bearing for dispatch (the target's Params template off them). Built
// with fmt.Sprintf so the sibling target id comes from its constant; the
// cypher has no negated relationship pattern, only scalar NOT comparisons.
var wellnessBookingChangeNoticesSpec = fmt.Sprintf(`MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS actorKey,
  b.key AS entityKey,
  se.key AS sessionKey,
  id.key AS bookerKey,
  b.status.data.value AS status,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  b.status.data.classStartsAt AS classStartsAt,
  b.status.data.promotedAt AS promotedAt,
  b.status.data.className AS className,
  b.changeNotice.data.promotedFor AS promotedFor,
  b.changeNotice.data.movedFor AS movedFor,
  b.changeNotice.data.sentAt AS noticeSentAt,
  ((b.status.data.promotedAt <> null) AND (b.changeNotice.data.promotedFor <> b.status.data.promotedAt) AND (se.schedule.data.startsAt <> null) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS missing_promotion_notice,
  ((b.status.data.classStartsAt <> null) AND (se.schedule.data.startsAt <> null) AND (se.schedule.data.startsAt <> coalesce(b.changeNotice.data.movedFor, b.status.data.classStartsAt)) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) AS missing_move_notice,
  (((b.status.data.promotedAt <> null) AND (b.changeNotice.data.promotedFor <> b.status.data.promotedAt) AND (se.schedule.data.startsAt <> null) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt)) OR ((b.status.data.classStartsAt <> null) AND (se.schedule.data.startsAt <> null) AND (se.schedule.data.startsAt <> coalesce(b.changeNotice.data.movedFor, b.status.data.classStartsAt)) AND (b.status.data.value = 'booked') AND NOT (b.freshnessExpiry.data.byTarget.%[1]s >= se.schedule.data.endsAt))) AS violating`,
	PastDueBookingsTarget)
