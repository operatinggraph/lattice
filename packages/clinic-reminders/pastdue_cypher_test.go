package clinicreminders

// Rule-engine proof of the pastDueAppointments convergence lens — the auto
// no-show closer — driven through the `full` engine against an embedded NATS
// Core/Adjacency KV, the same harness lens_cypher_test.go uses. Unlike
// appointmentRemindersSpec (freshUntil = a DERIVED lead-offset deadline), this
// binds freshUntil DIRECTLY to .schedule.endsAt (the unroutedTasks idiom):
//
//   - OPEN (still scheduled/confirmed/checkedIn, endsAt in the future): not
//     violating; freshUntil = endsAt arms the @at timer.
//   - PAST-DUE (endsAt passed, still non-terminal): violating; the gap-dispatch
//     path (not a timer) owns it — freshUntil null.
//   - TERMINAL (completed/cancelled/noShow), even with endsAt long passed:
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

// projectPastDueAt runs the anchored pastDueAppointments spec for one
// appointment with an INJECTED $now.
func (f *remFixture) projectPastDueAt(t *testing.T, apptName, now string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(pastDueAppointmentsSpec)
	require.NoError(t, err, "pastDueAppointments cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    apptKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per appointment")
	return out[0].Values
}

// TestPastDue_StillOpen — endsAt is still in the future, status non-terminal:
// not violating; freshUntil = endsAt arms the @at timer.
func TestPastDue_StillOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "scheduled")

	v := f.projectPastDueAt(t, "appt", remNow)
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "freshUntil = endsAt arms the @at timer while it is future")
}

// TestPastDue_Due — endsAt has passed, status still non-terminal: the gap
// OPENS. freshUntil is null once due — the violating row itself drives dispatch.
func TestPastDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "confirmed")

	v := f.projectPastDueAt(t, "appt", remNow)
	require.Equal(t, true, v["missing_noshow_transition"], "endsAt passed + still confirmed → past-due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no armed timer (violating-path dispatches)")
}

// TestPastDue_CheckedIn — checkedIn is non-terminal too: a patient checked in
// but never marked completed still converges to past-due once endsAt passes
// (the clinic never closed the loop either way).
func TestPastDue_CheckedIn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "checkedIn")

	v := f.projectPastDueAt(t, "appt", remNow)
	require.Equal(t, true, v["missing_noshow_transition"])
	require.Equal(t, true, v["violating"])
}

// TestPastDue_Completed — a completed visit is never past-due, even with
// endsAt long passed: terminal statuses converge permanently.
func TestPastDue_Completed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "completed")

	v := f.projectPastDueAt(t, "appt", remNow)
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

	v := f.projectPastDueAt(t, "appt", remNow)
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

	v := f.projectPastDueAt(t, "appt", remNow)
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestPastDue_FutureStillScheduled — an appointment days out is not past-due
// and arms a future timer, same as TestPastDue_StillOpen at a longer horizon —
// proves the gate is purely about endsAt vs $now, independent of startsAt.
func TestPastDue_FutureStillScheduled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-07-05T15:00:00Z", "2026-07-05T15:30:00Z", "scheduled")

	v := f.projectPastDueAt(t, "appt", remNow)
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-05T15:30:00Z", v["freshUntil"])
}
