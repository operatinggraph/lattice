package clinicreminders

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// AppointmentRemindersTarget is the §10.8 TargetID == the appointmentReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const AppointmentRemindersTarget = "appointmentReminders"

// AppointmentChangeNoticesTarget is the §10.8 TargetID == the
// appointmentChangeNotices lens's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads.
const AppointmentChangeNoticesTarget = "appointmentChangeNotices"

// nonTerminalAppointment is the "this visit has not reached a terminal outcome"
// test, as one cypher fragment spliced into BOTH appointment-anchored deadline
// lenses (appointmentReminders here, pastDueAppointments in pastdue.go).
//
// The two must agree on the terminal set EXACTLY, and sharing the fragment is
// what makes that structural rather than a convention. They are coupled through
// the marker: appointmentReminders closes its gate on
// byTarget.pastDueAppointments — the recorded end of the visit — and only
// pastDueAppointments arms the timer that writes it. A status this fragment
// excludes therefore projects no pastDueAppointments freshUntil, no @at is
// armed, no lapse is ever recorded, and any reminder gate still open on that
// appointment has no term left that can close it. The two lists agreeing is the
// property; one list is how it is held.
//
// TERMINAL_STATUSES is clinic-domain's (ddls.go): completed, cancelled, noShow.
const nonTerminalAppointment = `(a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow')`

// Lenses returns the package's weaver-target convergence lenses: appointmentReminders
// (the ~24h-ahead appointment reminder), appointmentChangeNotices (the
// level-triggered desk-cancel / desk-move notice — changenotice.go owns the op;
// the lens is below), followUpReminders (the at-the-date
// follow-up reminder, followups.go), visitSeriesDue (the recurring visit-series
// gap, visitseries.go), visitSeriesSiteBackfill (the series'
// missing atSite link, visitseries_site.go — the one gap here that is not
// deadline-driven at all: it converges a MISSING RELATIONSHIP, the
// clinicSiteBackfill idiom), and pastDueAppointments (the auto
// no-show closer, pastdue.go). The reminder and follow-up lenses invert lease-signing's
// freshness re-open — where lease projects freshUntil to RE-OPEN a converged gap at
// a deadline, these project freshUntil = the deadline to OPEN the reminder gap when
// it passes (see appointmentRemindersSpec / followUpRemindersSpec). visitSeriesDue
// is level-triggered on a different fact entirely — a qualifying visit at or after
// nextDueAt, never a clock lapse — and projects no freshUntil column at all: each
// AdvanceVisitSeries re-anchors nextDueAt on the crediting visit's own start time,
// folding the gap shut on the next projection with no clearing write.
// pastDueAppointments applies the freshness inversion a second way: it binds
// freshUntil DIRECTLY to a mutable business timestamp (.schedule.endsAt) rather than a
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
				BodyColumns:      []string{"violating", "missing_reminder", "entityKey", "freshUntil", "startsAt", "endsAt", "remindAt", "reminderSentAt", "remindedFor", "status", "patientKey", "providerKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			CanonicalName:  "appointmentChangeNotices",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           appointmentChangeNoticesSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "appointment",
				OutputKeyPattern: "appointmentChangeNotices.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_cancel_notice", "missing_move_notice", "entityKey", "patientKey", "status", "statusAt", "statusBy", "startsAt", "endsAt", "movedAt", "movedBy", "cancelledFor", "movedFor", "noticeSentAt"},
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
// AND a non-terminal status AND no recorded lapse at endsAt):
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
//   - nonTerminalAppointment — a cancelled, completed or no-show appointment is
//     never reminded. The list is the SHARED fragment, not a local one, because
//     the term below closes this gate on a marker only pastDueAppointments
//     writes and only for a non-terminal appointment: a status excluded there
//     but admitted here would hold this gap open with no term left that could
//     ever close it.
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
// projecting the row. That window is NOT free: this target declares no
// maxretries_reminder, so Weaver applies defaultDirectOpRetryBudget = 3
// (internal/weaver/evaluator.go:1379) against a 30-minute mark lease and a
// 1-minute sweep (internal/weaver/reconciler.go:17,21), and a visit longer than
// roughly an hour spends the budget before it ends — escalateExhaustedGap
// (evaluator.go:1572) raises a standing per-(target, entity, gap)
// GapBudgetExhausted warning.
//
// What the recorded end does is CLOSE the column, and a closed column retires
// that latch: handleRow's closed-gap leg calls retireClosedGapIssues
// (evaluator.go:1064), which clears issueKeyGapEntity(targetId, entityId,
// gapColumn) — the exact key the budget warning stands on (evaluator.go:1191).
// The sweep legs retire from the same function (reconciler.go:568, :792,
// :1237). So the standing issue is bounded by the visit, and it is the
// DISPATCHES that are bounded by the budget — not the other way round.
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
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors'
// static reach; its advisory asks for a hand check for a narrowing range
// bound inside a NEGATED pattern, and there is none — the cypher has no
// negated relationship pattern at all, only scalar NOT comparisons.
var appointmentRemindersSpec = fmt.Sprintf(`MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt,
  a.schedule.data.remindAt AS remindAt,
  a.reminder.data.sentAt AS reminderSentAt,
  a.reminder.data.remindedFor AS remindedFor,
  a.status.data.value AS status,
  p.key AS patientKey,
  pr.key AS providerKey,
  CASE WHEN (a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt) AND NOT (a.freshnessExpiry.data.byTarget.%[3]s >= a.schedule.data.remindAt) THEN a.schedule.data.remindAt ELSE null END AS freshUntil,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.freshnessExpiry.data.byTarget.%[3]s >= a.schedule.data.remindAt) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS missing_reminder,
  ((a.reminder.data.remindedFor <> a.schedule.data.startsAt) AND (a.freshnessExpiry.data.byTarget.%[3]s >= a.schedule.data.remindAt) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS violating`,
	nonTerminalAppointment, PastDueAppointmentsTarget, AppointmentRemindersTarget)

// appointmentChangeNoticesSpec is the one-row-per-appointment change-notice
// convergence cypher: a patient whose visit the desk cancelled, or moved to a
// new time, is told once per change. Unlike appointmentRemindersSpec it
// projects NO freshUntil and arms NO timer — both gaps are level-triggered
// states over recorded facts, not deadlines: a cancel is .status {value:
// cancelled, by: staff, at}, a move is .schedule {movedAt, movedBy: staff},
// both stamped by clinic-domain's writers at the transition. There is
// nothing to wait for; the row is violating the moment the fact lands and
// converged the moment the notice is recorded.
//
// The lifecycle for one appointment:
//
//   - SetAppointmentStatus(cancelled) / CorrectAppointmentStatus(cancelled)
//     (clinic-domain) stamp .status.at = op.submittedAt and .status.by =
//     staff on the desk's leg. With no .changeNotice yet, cancelledFor is
//     null and `null <> at` is true → missing_cancel_notice opens.
//     RecordAppointmentChangeNotice{kind: cancelled, changeRef: row.statusAt}
//     writes .changeNotice.cancelledFor = at → the equality closes the gap.
//     A same-value re-write (a note added to the cancelled visit) CARRIES at,
//     so the gap stays closed; a correction to another value and back stamps
//     a fresh at and is told again, once.
//   - RescheduleAppointment stamps .schedule.movedAt = op.submittedAt and
//     movedBy on every call. With no movedFor, `null <> movedAt` is true →
//     missing_move_notice opens. RecordAppointmentChangeNotice{kind: moved,
//     changeRef: row.movedAt} writes movedFor = movedAt → closed. A SECOND
//     move stamps a fresh movedAt that differs from the recorded movedFor →
//     the gap reopens and a fresh notice (a fresh externalRef) goes out.
//   - by = 'staff' / movedBy = 'staff': only the desk's change is told; a
//     patient's own cancel or move (by/movedBy = patient) and the sweep's
//     no-show (by = sweep) are not. staff covers every non-self writer —
//     the front desk, an operator, and the bound provider acting on their own
//     schedule (clinic-domain's status_author labels by the proven
//     self-service target alone). `=` on a null operand is FALSE in this
//     engine (nil-false), so a .status carrying no by reads `null = 'staff'`
//     false and is never told: a status with no recorded author has no
//     desk-made change to tell, and no backfill fabricates one.
//   - at <> null guards the cancel gap the same way: a cancelled status with
//     a by but no at (the two fields are independent keys) has no changeRef
//     to dispatch and stays closed rather than opening a gap the op can only
//     refuse. movedAt <> null guards the move gap for the same reason.
//   - at < endsAt on the cancel gap: a cancel stamped at or after the visit's
//     own end is a book-keeping correction, not news — a visit closed out by
//     hand as noShow / completed (which disarms the sibling pastDueAppointments
//     timer, so no lapse is ever recorded on it) and later corrected to
//     cancelled by the desk stamps an at past endsAt, and the recorded-end
//     conjunct below has no marker to close on. Both operands are canonical
//     UTC, so the lexical compare is chronological; the op re-checks the same
//     bound (StaleChange). The move gap needs no such term: nonTerminal
//     already excludes every closed-out visit.
//   - Two desk moves inside one whole second share a movedAt (rfc3339_utc is
//     whole seconds), so the second is not re-told; the op re-checks the
//     LIVE .schedule before sending, so the one message carries the latest
//     times. Accepted.
//   - nonTerminalAppointment on the move gap only: a moved-then-cancelled
//     visit gets the cancel notice alone (there is no future time to tell
//     the patient about), and a completed / noShow visit is over. The cancel
//     gap carries no status-list conjunct beyond `= 'cancelled'` — cancelled
//     IS terminal, and it is the one terminal value the desk is told about.
//   - NOT (a.freshnessExpiry.data.byTarget.pastDueAppointments >= endsAt) —
//     the visit is OVER, a recorded fact from the sibling pastDueAppointments
//     target's fired @at on this same appointment anchor (exactly the conjunct
//     appointmentRemindersSpec reads). A notice for a visit that has ended is
//     moot (a cancel after the recorded end is a book-keeping correction, not
//     news), and the closed column retires any GapBudgetExhausted latch the
//     open window accumulated. While nothing has fired, NOT(false) leaves the
//     gaps open — the default a not-yet-ended visit needs. A desk cancel
//     before the visit never arms that timer (nonTerminalAppointment is
//     false on the sibling lens), so the cancel gap stays open until told.
//
// Every operand is stored graph data; the lens reads no clock. `<>` is the
// engine's two-valued null test (null <> 'x' true, null <> null false), which
// is what lets the absent-marker case open the gap and the absent-fact case
// (no at, no movedAt) keep it closed. violating repeats both gap expressions
// verbatim — the engine has no column references in RETURN.
//
// One-row-per-anchor: forPatient is 0..1 (CreateAppointment writes exactly
// one), so the OPTIONAL walk does not fan out; it is INFORMATIONAL (no
// Params binds patientKey). entityKey, statusAt and movedAt are load-bearing
// for dispatch (the target's Params template off them); the rest is
// observability. Built with fmt.Sprintf so the shared nonTerminalAppointment
// fragment and the sibling target id come from their constants; the cypher
// has no negated relationship pattern, only scalar NOT comparisons.
var appointmentChangeNoticesSpec = fmt.Sprintf(`MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  p.key AS patientKey,
  a.status.data.value AS status,
  a.status.data.at AS statusAt,
  a.status.data.by AS statusBy,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt,
  a.schedule.data.movedAt AS movedAt,
  a.schedule.data.movedBy AS movedBy,
  a.changeNotice.data.cancelledFor AS cancelledFor,
  a.changeNotice.data.movedFor AS movedFor,
  a.changeNotice.data.sentAt AS noticeSentAt,
  ((a.status.data.value = 'cancelled') AND (a.status.data.by = 'staff') AND (a.status.data.at <> null) AND (a.status.data.at < a.schedule.data.endsAt) AND (a.changeNotice.data.cancelledFor <> a.status.data.at) AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS missing_cancel_notice,
  ((a.schedule.data.movedAt <> null) AND (a.schedule.data.movedBy = 'staff') AND (a.changeNotice.data.movedFor <> a.schedule.data.movedAt) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS missing_move_notice,
  (((a.status.data.value = 'cancelled') AND (a.status.data.by = 'staff') AND (a.status.data.at <> null) AND (a.status.data.at < a.schedule.data.endsAt) AND (a.changeNotice.data.cancelledFor <> a.status.data.at) AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) OR ((a.schedule.data.movedAt <> null) AND (a.schedule.data.movedBy = 'staff') AND (a.changeNotice.data.movedFor <> a.schedule.data.movedAt) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt))) AS violating`,
	nonTerminalAppointment, PastDueAppointmentsTarget)
