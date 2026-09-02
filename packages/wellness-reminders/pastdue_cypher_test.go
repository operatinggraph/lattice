package wellnessreminders

// Rule-engine proof of the pastDueBookings convergence lens — the auto
// no-show closer — driven through the `full` engine against an embedded NATS
// Core/Adjacency KV, the same harness lens_cypher_test.go uses. Mirrors
// clinic-reminders/pastdue_cypher_test.go, anchored on booking instead of
// appointment.
//
// What decides "past due" is a recorded FACT, not a clock: the instant the @at
// this lens armed actually fired, recorded on the BOOKING under this target's own
// byTarget key — the deadline lives on the session neighbour, but the marker
// lands on the row's anchor. No $now is supplied — the cypher references none.
//
//   - OPEN (booked, no recorded lapse at endsAt): not violating; freshUntil =
//     endsAt arms the @at timer.
//   - PAST-DUE (booked, a lapse recorded at or after endsAt): violating; the
//     gap-dispatch path (not a timer) owns it — freshUntil null.
//   - WAITLISTED / ATTENDED / already NOSHOW, even with the lapse recorded:
//     never violating; freshUntil null.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// mkBookingEnds seeds one booking + its session, linked forSession, with the
// session's .schedule {startsAt, endsAt} and the booking's own .status —
// the pastDueBookings fixture (lens_cypher_test.go's mkBooking-style helpers
// pin remindAt too, which these tests don't need).
func (f *remFixture) mkBookingEnds(t *testing.T, bookingName, sessionName, startsAt, endsAt, status string) {
	t.Helper()
	f.vtx(t, sessionName, "session")
	f.aspect(t, sessionName, "schedule", "wellnessSchedule", map[string]any{"startsAt": startsAt, "endsAt": endsAt})
	f.vtx(t, bookingName, "booking")
	f.aspect(t, bookingName, "status", "bookingStatus", map[string]any{"value": status})
	f.edge(t, "forSession", bookingName, sessionName)
}

// projectPastDue runs the anchored pastDueBookings spec for one booking. NO
// clock parameter is supplied — the cypher references none.
func (f *remFixture) projectPastDue(t *testing.T, bookingName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(pastDueBookingsSpec)
	require.NoError(t, err, "pastDueBookings cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": bookingKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per booking")
	return out[0].Values
}

// TestWellnessPastDue_StillOpen — no timer has fired on this booking, status
// booked: not violating; freshUntil = endsAt arms the @at timer.
func TestWellnessPastDue_StillOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "booked")

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "freshUntil = endsAt arms the @at timer while no lapse is recorded")
}

// TestWellnessPastDue_Due — a timer this target armed fired at the session's
// endsAt and the lapse is recorded, status still booked: the gap OPENS.
// freshUntil is null once the lapse lands — the violating row drives dispatch.
func TestWellnessPastDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "booked")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, true, v["missing_noshow_transition"], "a recorded lapse at the session's endsAt + still booked → past-due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → no armed timer (violating-path dispatches)")
}

// TestWellnessPastDue_Waitlisted — a waitlisted booker never held a
// confirmed seat, so there is no attendance to record even once the lapse is
// recorded: never violating.
func TestWellnessPastDue_Waitlisted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "waitlisted")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_Attended — an already-attended booking is never past-due,
// even with the lapse recorded: terminal statuses converge permanently.
func TestWellnessPastDue_Attended(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "attended")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"], "attended → never past-due")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_AlreadyNoShow — an already-noShow booking (staff
// already hand-marked it, or a prior dispatch already converged it) never
// re-fires.
func TestWellnessPastDue_AlreadyNoShow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "noShow")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_FutureStillBooked — a class days out is not past-due and
// arms a timer, same as TestWellnessPastDue_StillOpen at a longer horizon —
// proves the gate is purely about the recorded lapse vs the session's endsAt.
func TestWellnessPastDue_FutureStillBooked(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-07-05T15:00:00Z", "2026-07-05T15:30:00Z", "booked")

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-05T15:30:00Z", v["freshUntil"])
}

// TestWellnessPastDue_PastEndsAtProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector, and the one place a "null when the
// deadline is already past" guard would be tempting. A class that ended while no
// target was watching carries no marker, so nothing has recorded the lapse; the
// only thing that records it is this row projecting the past instant, Weaver
// publishing the overdue @at, and NATS releasing it immediately. Nulling it here
// arms nothing and the seat is never resolved.
func TestWellnessPastDue_PastEndsAtProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const longPast = "2020-06-01T09:30:00Z"
	f.mkBookingEnds(t, "bk", "se", "2020-06-01T09:00:00Z", longPast, "booked")

	v := f.projectPastDue(t, "bk")
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past endsAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_noshow_transition"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: longPast})
	v = f.projectPastDue(t, "bk")
	require.Equal(t, true, v["missing_noshow_transition"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_ReassignedPastTheRecordedLapse is the RE-ARM vector: the
// marker is permanent and nothing clears it, so a class moved to a later slot
// must arm again off the stored comparison alone. Testing the marker's PRESENCE
// instead would leave every reassigned booking permanently past-due.
func TestWellnessPastDue_ReassignedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const newEndsAt = "2026-07-20T09:30:00Z"
	f.mkBookingEnds(t, "bk", "se", "2026-07-20T09:00:00Z", newEndsAt, "booked")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"], "a lapse the current endsAt has outrun is not a lapse of THIS deadline")
	require.Equal(t, newEndsAt, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestWellnessPastDue_ReassignedEarlierThanTheRecordedLapse is the
// DEADLINE-MOVED-EARLIER row of the state table, asserted deliberately so a later
// reader does not "fix" it: a class pulled forward below an instant this target
// already fired at reads past-due at once. Correct — a timer did fire at or after
// the new endsAt.
func TestWellnessPastDue_ReassignedEarlierThanTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-20T09:00:00Z", "2026-06-20T09:30:00Z", "booked")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T09:30:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, true, v["missing_noshow_transition"], "the recorded fire is after the new endsAt, so it IS a lapse of it")
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_RevivedAtTheSameDeadline is the REVIVED-SAME-DEADLINE row:
// a booking re-created against the class time it already lapsed on reads past-due
// immediately. Also correct, and also deliberate — the deadline did lapse, and no
// second fire is needed to say so.
func TestWellnessPastDue_RevivedAtTheSameDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const endsAt = "2026-06-30T09:30:00Z"
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", endsAt, "booked")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: endsAt})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, true, v["missing_noshow_transition"], "the same deadline, already lapsed, is still lapsed")
}

// TestWellnessPastDue_SiblingTargetLapseDoesNotOpenThisGap is the isolation
// vector: a booking carries both this target and wellnessBookingReminders in one
// marker aspect, so reading the aspect's presence — or its entity-wide expiredAt
// maximum — would mark a seat a no-show off the reminder timer's fire.
func TestWellnessPastDue_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "booked")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2099-01-01T00:00:00Z"})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, false, v["missing_noshow_transition"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestWellnessPastDue_BoundaryMarkerEqualsEndsAt pins which side of the `>=` the
// equal instant falls on: the timer fires AT endsAt and records that instant, so
// equality is the ordinary lapse rather than an edge case that leaves the seat
// open forever.
func TestWellnessPastDue_BoundaryMarkerEqualsEndsAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const endsAt = "2026-06-30T15:30:00Z"
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T15:00:00Z", endsAt, "booked")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: endsAt})

	v := f.projectPastDue(t, "bk")
	require.Equal(t, true, v["missing_noshow_transition"], "marker == endsAt is a lapse (>= boundary)")
}
