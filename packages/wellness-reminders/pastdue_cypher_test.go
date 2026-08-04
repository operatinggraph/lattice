package wellnessreminders

// Rule-engine proof of the pastDueBookings convergence lens — the auto
// no-show closer — driven through the `full` engine against an embedded NATS
// Core/Adjacency KV, the same harness lens_cypher_test.go uses. Mirrors
// clinic-reminders/pastdue_cypher_test.go, anchored on booking instead of
// appointment:
//
//   - OPEN (booked, endsAt in the future): not violating; freshUntil = endsAt
//     arms the @at timer.
//   - PAST-DUE (booked, endsAt passed): violating; the gap-dispatch path (not
//     a timer) owns it — freshUntil null.
//   - WAITLISTED / ATTENDED / already NOSHOW, even with endsAt long passed:
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

// projectPastDueAt runs the anchored pastDueBookings spec for one booking
// with an INJECTED $now.
func (f *remFixture) projectPastDueAt(t *testing.T, bookingName, now string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(pastDueBookingsSpec)
	require.NoError(t, err, "pastDueBookings cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    bookingKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per booking")
	return out[0].Values
}

// TestWellnessPastDue_StillOpen — endsAt is still in the future, status
// booked: not violating; freshUntil = endsAt arms the @at timer.
func TestWellnessPastDue_StillOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "booked")

	v := f.projectPastDueAt(t, "bk", remNow)
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "freshUntil = endsAt arms the @at timer while it is future")
}

// TestWellnessPastDue_Due — endsAt has passed, status still booked: the gap
// OPENS. freshUntil is null once due — the violating row itself drives dispatch.
func TestWellnessPastDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "booked")

	v := f.projectPastDueAt(t, "bk", remNow)
	require.Equal(t, true, v["missing_noshow_transition"], "endsAt passed + still booked → past-due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no armed timer (violating-path dispatches)")
}

// TestWellnessPastDue_Waitlisted — a waitlisted booker never held a
// confirmed seat, so there is no attendance to record even once endsAt
// passes: never violating.
func TestWellnessPastDue_Waitlisted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "waitlisted")

	v := f.projectPastDueAt(t, "bk", remNow)
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_Attended — an already-attended booking is never
// past-due, even with endsAt long passed: terminal statuses converge
// permanently.
func TestWellnessPastDue_Attended(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "attended")

	v := f.projectPastDueAt(t, "bk", remNow)
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

	v := f.projectPastDueAt(t, "bk", remNow)
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestWellnessPastDue_FutureStillBooked — a class days out is not past-due
// and arms a future timer, same as TestWellnessPastDue_StillOpen at a longer
// horizon — proves the gate is purely about endsAt vs $now.
func TestWellnessPastDue_FutureStillBooked(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingEnds(t, "bk", "se", "2026-07-05T15:00:00Z", "2026-07-05T15:30:00Z", "booked")

	v := f.projectPastDueAt(t, "bk", remNow)
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-05T15:30:00Z", v["freshUntil"])
}
