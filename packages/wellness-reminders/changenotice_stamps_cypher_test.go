package wellnessreminders

// Rule-engine proof of the two stamp-keyed gaps on wellnessBookingChangeNotices:
// a seat is told once of a change to who leads (instructorChangedAt) or where
// the class meets (studioChangedAt) made at or after its own claim, and the
// notice's own marker field (instructorFor / roomFor) is what closes it.
// Same harness and fixture as changenotice_cypher_test.go.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	cnBookedAt  = "2026-06-20T10:00:00Z"
	cnSwappedAt = "2026-06-21T09:00:00Z"
	cnSwapAgain = "2026-06-22T09:00:00Z"
)

// A swap after the claim, untold: the instructor gap opens and the row names
// who leads now; the other three stay closed.
func TestChangeNotices_InstructorChangedUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, instructorChangedAt: cnSwappedAt, instructorName: "Sam Okafor"})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, true, false, "instructorChangedAt >= bookedAt with no instructorFor")
	require.Equal(t, cnSwappedAt, v["instructorChangedAt"], "the target templates changeRef off this column")
	require.Equal(t, "Sam Okafor", v["instructorName"], "the notice names who leads now")
	require.Nil(t, v["instructorFor"])
}

// Told: instructorFor = the stamp closes the gap.
func TestChangeNotices_InstructorChangedTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, instructorChangedAt: cnSwappedAt, instructorFor: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, false, false, "instructorFor = instructorChangedAt")
}

// A second swap advances the stamp past what the member was told: reopens.
func TestChangeNotices_SecondInstructorChangeReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, instructorChangedAt: cnSwapAgain, instructorFor: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, true, false, "instructorFor <> the advanced instructorChangedAt")
}

// A seat claimed AFTER the swap saw the new instructor when it booked: quiet.
func TestChangeNotices_BookedAfterTheChangeNeverTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnSwapAgain, instructorChangedAt: cnSwappedAt, studioChangedAt: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, false, false, "a change before the claim is not a change to this seat")
}

// A claim in the same second as the change is told (>=): true, at worst
// redundant — never a change silently lost to a tie.
func TestChangeNotices_ChangeInTheClaimsSecondIsTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnSwappedAt, instructorChangedAt: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, true, false, "instructorChangedAt = bookedAt is told")
}

// A legacy seat with no bookedAt has no baseline: quiet for every change.
func TestChangeNotices_LegacyNoBookedAtStaysQuiet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		instructorChangedAt: cnSwappedAt, studioChangedAt: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, false, false, "no bookedAt → `stamp >= null` is false")
}

// A class whose leader and room never changed carries no stamp: closed, and
// an un-led class (no ledBy walk) projects no instructor.
func TestChangeNotices_NoStampNoGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd, bookedAt: cnBookedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, false, false, "no stamp → `null <> null` is false")
	require.Nil(t, v["instructorName"])
	require.Nil(t, v["instructorChangedAt"])
}

// A room move after the claim, untold: the room gap opens naming the studio;
// told: closed. A cleared instructor (no ledBy) still opens the instructor
// gap — being un-led is a change too — with no name to give.
func TestChangeNotices_RoomChangedAndCleared(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, studioChangedAt: cnSwappedAt, studioName: "Riverside", instructorChangedAt: cnSwappedAt})
	v := f.projectChangeNotices(t, "bk")
	requireAllGaps(t, v, false, false, true, true, "both stamps after the claim, neither told")
	require.Equal(t, "Riverside", v["studioName"])
	require.Equal(t, cnSwappedAt, v["studioChangedAt"])
	require.Nil(t, v["instructorName"], "an un-led class names nobody")

	f.mkChangeNoticeBooking(t, "told", "flow2", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, studioChangedAt: cnSwappedAt, roomFor: cnSwappedAt, instructorChangedAt: cnSwappedAt, instructorFor: cnSwappedAt})
	requireAllGaps(t, f.projectChangeNotices(t, "told"), false, false, false, false, "both told")
}

// Only a confirmed seat is told, and a class that is over closes the gaps —
// the same conjuncts the promotion and move gaps carry.
func TestChangeNotices_StampGapsWaitlistedAndOverClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "wl", "flow", cnBooking{status: "waitlisted", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, instructorChangedAt: cnSwappedAt, studioChangedAt: cnSwappedAt})
	requireAllGaps(t, f.projectChangeNotices(t, "wl"), false, false, false, false, "a waitlisted booker has no seat to be told about")

	f.mkChangeNoticeBooking(t, "over", "flow2", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd,
		bookedAt: cnBookedAt, instructorChangedAt: cnSwappedAt, studioChangedAt: cnSwappedAt})
	f.aspect(t, "over", "freshnessExpiry", "freshnessExpiry", map[string]any{"byTarget": map[string]any{PastDueBookingsTarget: cnClassEnd}})
	requireAllGaps(t, f.projectChangeNotices(t, "over"), false, false, false, false, "the class is over")
}
