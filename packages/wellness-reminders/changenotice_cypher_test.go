package wellnessreminders

// Rule-engine proof of the wellnessBookingChangeNotices convergence lens,
// driven through the `full` engine against an embedded NATS Core/Adjacency KV
// — the same harness lens_cypher_test.go uses for the reminder lens.
//
// The predicate reads no clock and arms no timer: both gaps are level
// states over recorded facts. Each vector seeds the facts (a promotedAt, a
// classStartsAt, the session's current startsAt) and the marker (or omits
// it) and asserts BOTH gap columns and `violating`, so a conjunct dropped
// from one gap's expression shows up as the wrong column flipping, not as a
// row that merely still projects.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// cnBooking is one seeded booking's facts. Empty strings are omitted from
// the aspects they belong to, so an absent fact is genuinely absent (a
// missing key), never an empty string the engine would compare as present.
type cnBooking struct {
	status        string
	classStartsAt string // .status.classStartsAt (the time the seat was claimed for)
	promotedAt    string // .status.promotedAt
	startsAt      string // the session's current .schedule.startsAt
	endsAt        string // the session's .schedule.endsAt
	promotedFor   string // .changeNotice.promotedFor
	movedFor      string // .changeNotice.movedFor
}

// mkChangeNoticeBooking seeds one booking {status, classStartsAt?,
// promotedAt?, className} + a LIVE session {startsAt, endsAt} linked via
// forSession, + a .changeNotice marker when either notice field is set.
func (f *remFixture) mkChangeNoticeBooking(t *testing.T, name, sessionName string, b cnBooking) {
	t.Helper()
	f.vtx(t, name, "booking")
	status := map[string]any{"value": b.status, "className": "Vinyasa Flow"}
	if b.classStartsAt != "" {
		status["classStartsAt"] = b.classStartsAt
	}
	if b.promotedAt != "" {
		status["promotedAt"] = b.promotedAt
	}
	f.aspect(t, name, "status", "bookingStatus", status)
	if sessionName != "" {
		f.vtx(t, sessionName, "session")
		f.aspect(t, sessionName, "schedule", "sessionSchedule", map[string]any{
			"name": "Vinyasa Flow", "startsAt": b.startsAt, "endsAt": b.endsAt, "capacity": 20})
		f.edge(t, "forSession", name, sessionName)
	}
	if b.promotedFor != "" || b.movedFor != "" {
		marker := map[string]any{"sentAt": "2026-06-20T12:00:05Z"}
		if b.promotedFor != "" {
			marker["promotedFor"] = b.promotedFor
		}
		if b.movedFor != "" {
			marker["movedFor"] = b.movedFor
		}
		f.aspect(t, name, "changeNotice", "bookingChangeNotice", marker)
	}
}

// projectChangeNotices runs the anchored wellnessBookingChangeNotices spec
// for one booking. NO clock parameter is supplied: the cypher references
// none, and passing one would let a clock-reading regression pass unnoticed.
func (f *remFixture) projectChangeNotices(t *testing.T, bookingName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(wellnessBookingChangeNoticesSpec)
	require.NoError(t, err, "wellnessBookingChangeNotices cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": bookingKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per booking")
	return out[0].Values
}

// requireGaps asserts both gap columns and violating in one place, so every
// vector checks the column it is NOT about too.
func requireGaps(t *testing.T, v map[string]any, promotion, move bool, why string) {
	t.Helper()
	require.Equal(t, promotion, v["missing_promotion_notice"], "missing_promotion_notice: "+why)
	require.Equal(t, move, v["missing_move_notice"], "missing_move_notice: "+why)
	require.Equal(t, promotion || move, v["violating"], "violating = either gap: "+why)
	_, hasFreshUntil := v["freshUntil"]
	require.False(t, hasFreshUntil, "the change-notice lens arms no timer — no freshUntil column")
}

const (
	cnClassAt   = "2026-07-05T15:00:00Z"
	cnClassEnd  = "2026-07-05T16:00:00Z"
	cnMovedTo   = "2026-07-06T18:00:00Z"
	cnMovedNext = "2026-07-07T18:00:00Z"
	cnPromoted  = "2026-06-20T20:41:00Z"
)

// TestChangeNotices_PromotedUntold — a seat handed over from the waitlist
// (promotedAt present) with no marker: the promotion gap opens; the class
// has not moved (startsAt = classStartsAt), so the move gap stays closed.
// bookedBy is linked too, to prove one-row-per-anchor.
func TestChangeNotices_PromotedUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnClassAt, endsAt: cnClassEnd})
	f.vtx(t, "alice", "identity")
	f.edge(t, "bookedBy", "bk", "alice")

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, true, false, "promotedAt recorded, no promotedFor, class never moved")
	require.Equal(t, "vtx.booking."+f.ids["bk"], v["entityKey"])
	require.Equal(t, "vtx.session."+f.ids["flow"], v["sessionKey"])
	require.Equal(t, "vtx.identity."+f.ids["alice"], v["bookerKey"])
	require.Equal(t, cnPromoted, v["promotedAt"], "the target templates changeRef off this column")
	require.Equal(t, cnClassAt, v["startsAt"])
	require.Equal(t, "Vinyasa Flow", v["className"])
	require.Nil(t, v["promotedFor"])
	require.Nil(t, v["noticeSentAt"])
}

// TestChangeNotices_PromotedTold — promotedFor = promotedAt: converged.
func TestChangeNotices_PromotedTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnClassAt, endsAt: cnClassEnd, promotedFor: cnPromoted})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "promotedFor = promotedAt closes the promotion gap; class never moved")
	require.Equal(t, cnPromoted, v["promotedFor"])
	require.Equal(t, "2026-06-20T12:00:05Z", v["noticeSentAt"])
}

// TestChangeNotices_NeverPromoted — a seat booked directly (no promotedAt)
// has nothing to be told: `null <> null` is false, so the promotion gap
// never opens on the absence of the fact.
func TestChangeNotices_NeverPromoted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "no promotedAt → nothing to tell; class never moved")
	require.Nil(t, v["promotedAt"])
}

// TestChangeNotices_MarkerWithoutFactStaysClosed — a promotion marker whose
// fact is gone (promotedFor recorded, .status carries no promotedAt: a
// status rewritten by a writer that dropped the field). `promotedFor <>
// null` alone would read TRUE and open a gap the op can only refuse
// (StaleChange: no promotedAt) until the budget is spent; the promotedAt <>
// null conjunct is what keeps the gap on the FACT, not on the marker.
func TestChangeNotices_MarkerWithoutFactStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnClassAt, endsAt: cnClassEnd, promotedFor: cnPromoted})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "no promotedAt on the status → no promotion to tell, whatever the marker says")
}

// TestChangeNotices_MovedUntold — the session's startsAt differs from the
// classStartsAt the seat was claimed for and no move notice is recorded:
// the move gap opens (coalesce falls through to classStartsAt).
func TestChangeNotices_MovedUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, true, "startsAt <> classStartsAt with no movedFor → the member was last told the original time")
	require.Equal(t, cnMovedTo, v["startsAt"], "the target templates changeRef off this column")
	require.Equal(t, cnClassAt, v["classStartsAt"])
	require.Nil(t, v["movedFor"])
}

// TestChangeNotices_MovedTold — movedFor = the current startsAt: converged,
// even though startsAt still differs from classStartsAt (coalesce prefers the
// notice over the original claim).
func TestChangeNotices_MovedTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z", movedFor: cnMovedTo})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "movedFor = startsAt closes the move gap regardless of classStartsAt")
	require.Equal(t, cnMovedTo, v["movedFor"])
}

// TestChangeNotices_SecondMoveReopens — the member was told about the first
// move (movedFor = the first new time); the class moved AGAIN, so startsAt
// has drifted from movedFor and the gap reopens for a fresh notice.
func TestChangeNotices_SecondMoveReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, startsAt: cnMovedNext, endsAt: "2026-07-07T19:00:00Z", movedFor: cnMovedTo})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, true, "startsAt <> movedFor (the last time the member was told) → a second notice is due")
	require.Equal(t, cnMovedNext, v["startsAt"])
	require.Equal(t, cnMovedTo, v["movedFor"])
}

// TestChangeNotices_LegacyNoClassStartsAt — a booked seat claimed before the
// classStartsAt snapshot existed carries no baseline: the move gap must
// stay closed even though startsAt <> coalesce(null, null) would read true,
// or every legacy seat would be told its class moved when it never did.
func TestChangeNotices_LegacyNoClassStartsAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "no classStartsAt → no baseline to compare → the move gap stays quiet")
	require.Nil(t, v["classStartsAt"])
}

// TestChangeNotices_WaitlistedNever — a waitlisted booker has no seat that
// was moved or handed over yet; neither gap opens even with both facts
// present and untold.
func TestChangeNotices_WaitlistedNever(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "waitlisted", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "waitlisted → no confirmed seat to tell")
}

// TestChangeNotices_ClassOverCloses — both facts untold, but the sibling
// pastDueBookings target has recorded a fire at or after the session's
// endsAt: the class is over, both gaps close and the row stops dispatching.
func TestChangeNotices_ClassOverCloses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-07-06T19:00:00Z"})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "the class ENDED (a recorded pastDueBookings fire >= endsAt) — both notices are moot")
}

// TestChangeNotices_ClassOverLapseBeforeTheEndDoesNotClose is the isolation
// half of the class-over term: a pastDueBookings fire for an EARLIER endsAt
// the current schedule has outrun (a reassigned class) is not evidence this
// class ended, so both gaps stay open.
func TestChangeNotices_ClassOverLapseBeforeTheEndDoesNotClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: cnClassEnd})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, true, true, "a past-due fire for an OUTRUN endsAt does not say this class has ended")
}

// TestChangeNotices_BothOpen — promoted AND moved, neither told: both gaps
// true, violating true. The two are independent columns over one row; the
// target dispatches one op per open gap.
func TestChangeNotices_BothOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, true, true, "promotedAt untold AND startsAt drifted from classStartsAt untold")
}

// TestChangeNotices_OneToldOtherOpen — the marker carries one kind's field
// only: the told gap closes, the other stays open, and violating follows the
// open one. Both directions.
func TestChangeNotices_OneToldOtherOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("promotion told, move open", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z", promotedFor: cnPromoted})
		requireGaps(t, f.projectChangeNotices(t, "bk"), false, true, "promotedFor = promotedAt; movedFor absent and startsAt drifted")
	})
	t.Run("move told, promotion open", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z", movedFor: cnMovedTo})
		requireGaps(t, f.projectChangeNotices(t, "bk"), true, false, "movedFor = startsAt; promotedFor absent")
	})
}

// TestChangeNotices_NoSessionNeverViolates — the forSession walk binds
// nothing (a tombstoned session drops out of the walk, as it does for every
// seat of a called-off class until wellness-domain's release drains it): the
// session's startsAt is null, and without the startsAt <> null guard the move
// gap would read `null <> classStartsAt` as a move and the promotion gap would
// dispatch with a null sessionKey. Both stay closed; the call-off notice is
// ReleaseOrphanedBooking's own.
func TestChangeNotices_NoSessionNeverViolates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted})

	v := f.projectChangeNotices(t, "bk")
	requireGaps(t, v, false, false, "no session bound → no schedule to tell about; the call-off leg is the release's")
	require.Nil(t, v["sessionKey"])
	require.Nil(t, v["startsAt"])
}

// TestChangeNotices_NonBookedNeverViolates — with both facts untold, only the
// status term decides, and every seat that is not `booked` reads closed.
func TestChangeNotices_NonBookedNeverViolates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"waitlisted", "attended", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: status, classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z"})
			requireGaps(t, f.projectChangeNotices(t, "bk"), false, false, "a "+status+" seat is never told")
		})
	}
}

// TestChangeNotices_BodyColumnsMatchReturn pins the §10.2↔§10.8 column seam:
// every RETURN column except actorKey is a BodyColumn (so the target's
// row.<column> templates resolve) and no BodyColumn is missing from RETURN.
func TestChangeNotices_BodyColumnsMatchReturn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	var body []string
	for _, l := range Lenses() {
		if l.CanonicalName == "wellnessBookingChangeNotices" {
			body = l.Output.BodyColumns
		}
	}
	require.NotEmpty(t, body)
	var gaps map[string]pkgmgr.GapActionSpec
	for _, tgt := range WeaverTargets() {
		if tgt.TargetID == WellnessBookingChangeNoticesTarget {
			gaps = tgt.Gaps
		}
	}
	require.NotNil(t, gaps)
	f := newRemFixture(t)
	f.mkChangeNoticeBooking(t, "bk", "flow", cnBooking{status: "booked", classStartsAt: cnClassAt, promotedAt: cnPromoted, startsAt: cnMovedTo, endsAt: "2026-07-06T19:00:00Z", promotedFor: cnPromoted, movedFor: cnMovedTo})
	v := f.projectChangeNotices(t, "bk")
	for col := range v {
		if col == "actorKey" {
			continue
		}
		require.Containsf(t, body, col, "RETURN column %q is not a BodyColumn — a row.%s template would resolve absent", col, col)
	}
	for _, col := range body {
		_, ok := v[col]
		require.Truef(t, ok, "BodyColumn %q is not a RETURN column — the row would carry a key the cypher never fills", col)
	}
	for _, gap := range []string{"missing_promotion_notice", "missing_move_notice"} {
		_, declared := gaps[gap]
		require.Truef(t, declared, "gap column %q must be declared in the target's Gaps map", gap)
	}
}
