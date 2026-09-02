package clinicreminders

// Rule-engine proof of the pastDueAppointments convergence lens — the auto
// no-show closer — driven through the `full` engine against an embedded NATS
// Core/Adjacency KV, the same harness lens_cypher_test.go uses. Unlike
// appointmentRemindersSpec (freshUntil = a DERIVED lead-offset deadline), this
// binds freshUntil DIRECTLY to .schedule.endsAt (the unroutedTasks idiom).
//
// What decides "past due" is a recorded FACT, not a clock: the instant the @at
// this lens armed actually fired, recorded on the appointment under this
// target's own byTarget key. No $now is supplied to any vector below — the
// cypher references none, and passing one would let a clock-reading regression
// pass unnoticed.
//
//   - OPEN (still scheduled/confirmed/checkedIn, no recorded lapse at endsAt):
//     not violating; freshUntil = endsAt arms the @at timer.
//   - PAST-DUE (a lapse recorded at or after endsAt, still non-terminal):
//     violating; the gap-dispatch path (not a timer) owns it — freshUntil null.
//   - TERMINAL (completed/cancelled/noShow), even with the lapse recorded:
//     never violating; freshUntil null.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// mkApptEnds seeds one appointment with a .schedule {startsAt, endsAt} + a
// .status — the pastDueAppointments fixture (appointmentRemindersSpec's mkAppt
// pins endsAt = startsAt, which the reminder tests never distinguish; these
// tests need a genuinely separate endsAt).
func (f *remFixture) mkApptEnds(t *testing.T, name, startsAt, endsAt, status string) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": startsAt, "endsAt": endsAt})
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
}

// projectPastDue runs the anchored pastDueAppointments spec for one
// appointment. NO clock parameter is supplied — the cypher references none.
func (f *remFixture) projectPastDue(t *testing.T, apptName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(pastDueAppointmentsSpec)
	require.NoError(t, err, "pastDueAppointments cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": apptKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per appointment")
	return out[0].Values
}

// TestPastDue_StillOpen — no timer has fired on this appointment, status
// non-terminal: not violating; freshUntil = endsAt arms the @at timer.
func TestPastDue_StillOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "scheduled")

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "freshUntil = endsAt arms the @at timer while no lapse is recorded")
}

// TestPastDue_Due — a timer this target armed fired at endsAt and the lapse is
// recorded, status still non-terminal: the gap OPENS. freshUntil is null once the
// lapse lands — the violating row itself drives dispatch.
func TestPastDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "confirmed")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "a recorded lapse at endsAt + still confirmed → past-due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → no armed timer (violating-path dispatches)")
}

// TestPastDue_CheckedIn — checkedIn is non-terminal too: a patient checked in
// but never marked completed still converges to past-due once the lapse at
// endsAt is recorded (the clinic never closed the loop either way).
func TestPastDue_CheckedIn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "checkedIn")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"])
	require.Equal(t, true, v["violating"])
}

// TestPastDue_Completed — a completed visit is never past-due, even with the
// lapse recorded: terminal statuses converge permanently.
func TestPastDue_Completed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "completed")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"], "completed → never past-due")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_Cancelled — a cancelled appointment is never past-due either.
func TestPastDue_Cancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "cancelled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_AlreadyNoShow — an already-noShow appointment (staff already
// hand-marked it, or a prior dispatch already converged it) never re-fires.
func TestPastDue_AlreadyNoShow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "noShow")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_FutureStillScheduled — an appointment days out is not past-due
// and arms a timer, same as TestPastDue_StillOpen at a longer horizon — proves
// the gate is purely about the recorded lapse vs endsAt, independent of startsAt.
func TestPastDue_FutureStillScheduled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-07-05T15:00:00Z", "2026-07-05T15:30:00Z", "scheduled")

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-05T15:30:00Z", v["freshUntil"])
}

// TestPastDue_PastEndsAtProjectedVerbatim is the PAST-DEADLINE-AT-FIRST-PROJECTION
// vector, and the one place a "null when the deadline is already past" guard
// would be tempting. An appointment whose end came and went while no target was
// watching carries no marker, so nothing has recorded the lapse; the only thing
// that records it is this row projecting the past instant, Weaver publishing the
// overdue @at, and NATS releasing it immediately. Nulling it here arms nothing
// and the no-show is never closed at all. The second half asserts the other end
// of that path: once the marker lands, the gap opens.
func TestPastDue_PastEndsAtProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const longPast = "2020-06-01T09:30:00Z"
	f.mkApptEnds(t, "appt", "2020-06-01T09:00:00Z", longPast, "scheduled")

	v := f.projectPastDue(t, "appt")
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past endsAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_noshow_transition"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: longPast})
	v = f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_RescheduledPastTheRecordedLapse is the RE-ARM vector: the marker is
// permanent and nothing clears it, so a visit moved to a later slot must arm
// again off the stored comparison alone. Testing the marker's PRESENCE instead
// would leave every rescheduled appointment permanently past-due.
func TestPastDue_RescheduledPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const newEndsAt = "2026-07-20T09:30:00Z"
	f.mkApptEnds(t, "appt", "2026-07-20T09:00:00Z", newEndsAt, "scheduled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"], "a lapse the current endsAt has outrun is not a lapse of THIS deadline")
	require.Equal(t, newEndsAt, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestPastDue_RescheduledEarlierThanTheRecordedLapse is the DEADLINE-MOVED-EARLIER
// row of the state table, asserted deliberately so a later reader does not "fix"
// it: a visit pulled forward below an instant this target already fired at reads
// past-due at once. That is CORRECT — a timer did fire at or after the new
// endsAt — and it is the same answer a clock would have given.
func TestPastDue_RescheduledEarlierThanTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-20T09:00:00Z", "2026-06-20T09:30:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "the recorded fire is after the new endsAt, so it IS a lapse of it")
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_RevivedAtTheSameDeadline is the REVIVED-SAME-DEADLINE row: an
// appointment re-created at the deadline it already lapsed on reads past-due
// immediately. Also correct, and also deliberate — the deadline did lapse, and
// no second fire is needed to say so.
func TestPastDue_RevivedAtTheSameDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const endsAt = "2026-06-30T09:30:00Z"
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", endsAt, "scheduled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: endsAt})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "the same deadline, already lapsed, is still lapsed")
}

// TestPastDue_SiblingTargetLapseDoesNotOpenThisGap is the isolation vector: an
// appointment carries three targets in one marker aspect, so reading the
// aspect's presence, or its entity-wide expiredAt maximum, would let the reminder
// timer's fire close a visit as a no-show.
func TestPastDue_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2099-01-01T00:00:00Z"})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestPastDue_BoundaryMarkerEqualsEndsAt pins which side of the `>=` the equal
// instant falls on: the timer fires AT endsAt and records that instant, so
// equality is the ordinary lapse rather than an edge case that leaves the visit
// open forever.
func TestPastDue_BoundaryMarkerEqualsEndsAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const endsAt = "2026-06-30T15:30:00Z"
	f.mkApptEnds(t, "appt", "2026-06-30T15:00:00Z", endsAt, "scheduled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: endsAt})

	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "marker == endsAt is a lapse (>= boundary)")
}

// TestPastDue_ReferencesNoClockParameter — the structural half of the conversion.
func TestPastDue_ReferencesNoClockParameter(t *testing.T) {
	requireClockFree(t, "pastDueAppointments", pastDueAppointmentsSpec)
}
