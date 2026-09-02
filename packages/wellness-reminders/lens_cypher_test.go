package wellnessreminders

// Rule-engine proof of the wellnessBookingReminders convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-reminders / wellness-domain use.
// Mirrors clinic-reminders/lens_cypher_test.go, anchored on booking instead of
// appointment (a session has many bookers, so the reminder marker — and the gate
// — lives per-booking).
//
// The predicate reads no clock: what decides whether the reminder is due is the
// freshnessExpiry marker this BOOKING carries — the instant a timer armed by a
// named target actually fired — compared against the deadline stored on the
// session. So every vector seeds a marker (or deliberately omits one) rather
// than injecting a projection instant, and no $now is supplied at all.
//
//   - PENDING (no recorded lapse at remindAt): not violating; freshUntil =
//     remindAt (arms the @at timer) — the reminder is not yet due.
//   - DUE (a lapse recorded at or after remindAt, not sent, status=booked, the
//     class not yet ended): violating; missing_reminder true — Weaver dispatches
//     the directOp.
//   - SENT (.reminder.remindedFor = startsAt): not violating; freshUntil null
//     (timer cleared) — converged.
//   - WAITLISTED / ENDED: never violating; freshUntil null.
//   - one row per anchor even with session + booker linked (0..1 × 0..1 = 1).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type remFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newRemFixture(t *testing.T) *remFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &remFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *remFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *remFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *remFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// projectReminders runs the anchored wellnessBookingReminders spec for one
// booking. NO clock parameter is supplied: the cypher references none, and
// passing one would let a clock-reading regression pass unnoticed here.
func (f *remFixture) projectReminders(t *testing.T, bookingName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(wellnessBookingRemindersSpec)
	require.NoError(t, err, "wellnessBookingReminders cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": bookingKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits onto the
// BOOKING when a target's @at fires: the instant the timer fired for, recorded
// under that target's own key in byTarget, with expiredAt carrying the
// entity-wide maximum. The deadline lives on the session neighbour, but the
// marker lands on the row's own anchor, which is what both booking targets read.
func (f *remFixture) recordLapse(t *testing.T, name string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, name, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// mkBookingSpan seeds a booking whose class SPAN is explicit — startsAt and
// endsAt differ — for the vectors that turn on "has the class ended", which
// mkBooking (endsAt = startsAt) cannot express.
func (f *remFixture) mkBookingSpan(t *testing.T, name, status, sessionName, startsAt, endsAt, remindAt string) {
	t.Helper()
	f.vtx(t, name, "booking")
	f.aspect(t, name, "status", "bookingStatus", map[string]any{"value": status})
	f.vtx(t, sessionName, "session")
	f.aspect(t, sessionName, "schedule", "sessionSchedule", map[string]any{
		"name": "Vinyasa Flow", "startsAt": startsAt, "endsAt": endsAt, "capacity": 20, "remindAt": remindAt})
	f.edge(t, "forSession", name, sessionName)
}

// mkBooking seeds one booking {status} + (when sessionName is non-empty) a
// LIVE session {startsAt, remindAt} linked via forSession, optionally a
// .reminder {sentAt, remindedFor}. A sent reminder records the startsAt it
// was for (remindedFor) — the gate converges on remindedFor = startsAt and
// re-opens when ReassignSession moves startsAt away from it.
func (f *remFixture) mkBooking(t *testing.T, name, status, sessionName, startsAt, remindAt, sentAt, remindedFor string) {
	t.Helper()
	f.vtx(t, name, "booking")
	f.aspect(t, name, "status", "bookingStatus", map[string]any{"value": status})
	if sessionName != "" {
		f.vtx(t, sessionName, "session")
		f.aspect(t, sessionName, "schedule", "sessionSchedule", map[string]any{
			"name": "Vinyasa Flow", "startsAt": startsAt, "endsAt": startsAt, "capacity": 20, "remindAt": remindAt})
		f.edge(t, "forSession", name, sessionName)
	}
	if sentAt != "" {
		marker := map[string]any{"sentAt": sentAt}
		if remindedFor != "" {
			marker["remindedFor"] = remindedFor
		}
		f.aspect(t, name, "reminder", "bookingReminder", marker)
	}
}

// TestReminders_Pending — a future class whose remindAt has NOT passed: not
// violating, but freshUntil = remindAt arms the @at timer. bookedBy is
// linked too, to prove one-row-per-anchor (no fan-out).
func TestReminders_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 5 days out, remindAt 4 days out — both AFTER now (2026-06-30T12:00Z).
	f.mkBooking(t, "bk", "booked", "flow", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "", "")
	f.vtx(t, "alice", "identity")
	f.edge(t, "bookedBy", "bk", "alice")

	rows := f.projectReminders(t, "bk")
	require.Len(t, rows, 1, "exactly one row per booking even with session + booker linked")
	v := rows[0].Values
	require.Equal(t, "vtx.booking."+f.ids["bk"], v["entityKey"])
	require.Equal(t, "vtx.booking."+f.ids["bk"], v["actorKey"])
	require.Equal(t, false, v["missing_reminder"], "no timer has fired on this booking — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "freshUntil = remindAt arms the @at timer")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "vtx.session."+f.ids["flow"], v["sessionKey"])
	require.Equal(t, "vtx.identity."+f.ids["alice"], v["bookerKey"])
}

// TestReminders_Due — the reminder timer has FIRED and its lapse is recorded at
// remindAt, the reminder was never sent, the seat is booked and the class has not
// ended: the gap OPENS (missing_reminder + violating true). freshUntil is NULL
// once the lapse is recorded — no timer re-arms; the violating row itself drives
// the dispatch.
func TestReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "", "")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"], "a lapse recorded at remindAt + not sent + booked + class not ended → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → nothing to wait for → no armed timer (violating-path dispatches)")
	require.Nil(t, v["reminderSentAt"])
}

// TestReminders_Sent — once a reminder is recorded for the CURRENT startsAt
// (remindedFor = startsAt) the gap is closed and freshUntil goes null.
// Converged.
func TestReminders_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "2026-06-29T15:00:05Z", "2026-06-30T15:00:00Z")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "remindedFor = startsAt → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current time — no timer re-arms")
	require.Equal(t, "2026-06-29T15:00:05Z", v["reminderSentAt"])
	require.Equal(t, "2026-06-30T15:00:00Z", v["remindedFor"])
}

// TestReminders_RescheduledAfterSent is the RE-ARM vector, and the whole argument
// for comparing the marker against the deadline rather than testing its presence.
// A reminder already fired and was recorded for an earlier class time;
// ReassignSession then moved the class so its remindAt OUTRUNS the recorded
// instant. Nothing clears the marker — MarkExpired never tombstones it — so a
// presence test would leave this booking permanently "due" and never re-arm. The
// comparison self-corrects with no clearing write at all.
func TestReminders_RescheduledAfterSent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-26T15:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "the recorded lapse is BEHIND the new remindAt → not yet due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "a lapse the current deadline has outrun does not disarm it — the @at re-arms with no clearing write")
}

// TestReminders_RescheduledIntoWindow is the DEADLINE-MOVED-EARLIER row of the
// state table, asserted deliberately rather than tolerated: ReassignSession moved
// the class to a time < 24h out, so its new remindAt falls BELOW an instant this
// target already fired at. That reads expired, which is CORRECT — a timer did
// fire at or after the new deadline — and the row goes due immediately with no
// second fire.
func TestReminders_RescheduledIntoWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T18:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindedFor <> new startsAt + a recorded lapse past the new remindAt → due now")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already lapsed → no armed timer (violating-path dispatches)")
}

// TestReminders_WaitlistedNeverReminded — a waitlisted booking (no confirmed
// seat) is never reminded, even with the lapse recorded; freshUntil null.
func TestReminders_WaitlistedNeverReminded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "waitlisted", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "", "")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "waitlisted (no confirmed seat) → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_NoShowNeverReminded — a booking already marked noShow (the
// class already happened) is never reminded.
func TestReminders_NoShowNeverReminded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "noShow", "flow", "2026-06-29T15:00:00Z", "2026-06-28T15:00:00Z", "", "")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-28T15:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "noShow → class already happened, never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_StartedButNotEnded is the middle leg of the role-(c) guard's
// three: the class has begun, no reminder ever went out, and it is NOT over — the
// pastDueBookings target's own @at at the session's endsAt has not fired, so
// nothing records the end. The gap therefore STAYS OPEN and the lens keeps
// projecting the row; RecordBookingReminder's own guard
// (time.rfc3339_utc(op.submittedAt) < startsAt) is what declines each dispatch.
// The refusals are bounded by the class's length, not by the retry budget —
// TestReminders_EndedClosesTheGap is the other end of that bound.
func TestReminders_StartedButNotEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingSpan(t, "bk", "booked", "flow", "2026-06-30T09:00:00Z", "2026-06-30T10:00:00Z", "2026-06-29T09:00:00Z")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"],
		"a started, never-reminded, not-yet-ended booked seat still projects the gap — the op refuses, not the lens")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the reminder lapse is recorded, so no timer re-arms")
}

// TestReminders_EndedClosesTheGap is the closing term. "The class is over" is a
// recorded fact, not a clock reading: the sibling pastDueBookings target arms its
// own @at at the session's endsAt on this same booking anchor, and its fired
// marker entry is the evidence. Once it lands, the reminder gate closes and
// Weaver stops dispatching an op that could only be refused.
func TestReminders_EndedClosesTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingSpan(t, "bk", "booked", "flow", "2026-06-30T09:00:00Z", "2026-06-30T10:00:00Z", "2026-06-29T09:00:00Z")
	f.recordLapse(t, "bk", map[string]string{
		WellnessBookingRemindersTarget: "2026-06-29T09:00:00Z",
		PastDueBookingsTarget:          "2026-06-30T10:00:00Z",
	})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "the class ENDED — a reminder is moot and the gate closes")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_SiblingPastDueLapseBeforeTheEndDoesNotClose is the isolation half
// of the term above: the past-due timer may have fired for an EARLIER endsAt the
// current schedule has outrun (a reassigned class). That recorded instant is not
// a lapse of THIS endsAt, so the reminder gate stays open — a bare presence test
// on the pastDueBookings entry would close it wrongly.
func TestReminders_SiblingPastDueLapseBeforeTheEndDoesNotClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingSpan(t, "bk", "booked", "flow", "2026-07-10T09:00:00Z", "2026-07-10T10:00:00Z", "2026-06-29T09:00:00Z")
	f.recordLapse(t, "bk", map[string]string{
		WellnessBookingRemindersTarget: "2026-06-29T09:00:00Z",
		PastDueBookingsTarget:          "2026-06-30T10:00:00Z",
	})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"], "a past-due fire for an OUTRUN endsAt does not say this class has ended")
	require.Equal(t, true, v["violating"])
}

// TestReminders_LastMinuteBooking — booked < 24h out so remindAt is already past
// at creation. This is the PAST-DEADLINE-AT-FIRST-PROJECTION vector, and it is
// what makes the conversion of freshUntil load-bearing rather than cosmetic: with
// no marker yet the row must project that past instant VERBATIM so Weaver
// publishes an overdue @at, NATS releases it at once, and THAT fire records the
// lapse. Nulling a past deadline here arms nothing, so the gap would never open
// and the reminder would never go out.
func TestReminders_LastMinuteBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const remindAt = "2026-06-29T18:00:00Z"
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T18:00:00Z", remindAt, "", "")

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, remindAt, v["freshUntil"],
		"an already-past remindAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_reminder"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: remindAt})
	v = f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"], "the recorded lapse opens the gap")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_SiblingTargetLapseAloneIsNotThisTargetsLapse is the per-target
// isolation vector. A booking anchors BOTH wellnessBookingReminders and
// pastDueBookings in ONE marker aspect, so reading the aspect's presence — or its
// entity-wide expiredAt maximum — would let the past-due timer open the reminder
// gap.
func TestReminders_SiblingTargetLapseAloneIsNotThisTargetsLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBookingSpan(t, "bk", "booked", "flow", "2099-07-05T15:00:00Z", "2099-07-05T16:00:00Z", "2099-07-04T15:00:00Z")
	f.recordLapse(t, "bk", map[string]string{PastDueBookingsTarget: "2026-06-30T10:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2099-07-04T15:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestReminders_BoundaryMarkerEqualsDeadline pins which side of the `>=` the
// equal instant falls on: the timer fires AT the deadline and records that
// instant, so equality is the ordinary lapse rather than an edge case that leaves
// the row armed forever.
func TestReminders_BoundaryMarkerEqualsDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const remindAt = "2026-07-04T15:00:00Z"
	f.mkBooking(t, "bk", "booked", "flow", "2026-07-05T15:00:00Z", remindAt, "", "")
	f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: remindAt})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, true, v["missing_reminder"], "marker == deadline is a lapse (>= boundary)")
	require.Nil(t, v["freshUntil"])
}

// TestReminders_MarkerWithNoByTargetMapReadsUnlapsed pins the shape a marker
// written before byTarget existed carries. `expiredAt` alone says which entity
// last lapsed, never which target, so a lens that read it would answer for a
// sibling's fire. The four-hop read resolves to nil and compareAny answers false:
// unlapsed, and the timer stays armed.
func TestReminders_MarkerWithNoByTargetMapReadsUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "", "")
	f.aspect(t, "bk", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "2099-01-01T00:00:00Z"})

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"])
}

// TestReminders_NoSession — a booking with no forSession link at all (a
// pathological state CreateBooking never leaves, but the link is declared
// optional at the type level) never violates: the OPTIONAL MATCH walk finds
// nothing, so every session-derived column is null and every gate term
// involving them reads false, not true.
func TestReminders_NoSession(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "", "", "", "", "")

	v := f.projectReminders(t, "bk")[0].Values
	require.Equal(t, false, v["missing_reminder"], "no session anchor → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestBookingLenses_ReferenceNoClockParameter is the structural half of the
// conversion, asserted on the compiled cyphers rather than on any one row: a lens
// that returns $now or $projectedAt projects a clock reading the sweep's deep
// verify cannot compare, which is the divergence this conversion removes.
func TestBookingLenses_ReferenceNoClockParameter(t *testing.T) {
	for _, tc := range []struct{ name, spec string }{
		{"wellnessBookingReminders", wellnessBookingRemindersSpec},
		{"pastDueBookings", pastDueBookingsSpec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := full.New()
			cr, err := eng.Parse(tc.spec)
			require.NoError(t, err)
			fullCR, isFull := cr.(*full.CompiledRule)
			require.True(t, isFull, "must compile to the full engine")
			for _, param := range []string{"now", "projectedAt"} {
				referenced, exhaustive := fullCR.ReferencesParam(param)
				require.Truef(t, exhaustive, "%s: the query shape must be provably free of $%s", tc.name, param)
				require.Falsef(t, referenced,
					"%s must reference no $%s — expiry is a recorded fact, not a clock reading", tc.name, param)
			}
		})
	}
}

// TestBookingLenses_ReadTheirOwnTargetsMarkerEntry binds the two halves that can
// silently drift apart: the §10.8 TargetID Weaver fires a timer under, and the
// byTarget key the lens compares against its deadline. A rename of one without
// the other leaves a lens reading an entry nothing ever writes — a gap that can
// never open, with every row still projecting and every seeded-marker test still
// passing.
func TestBookingLenses_ReadTheirOwnTargetsMarkerEntry(t *testing.T) {
	specs := map[string]string{}
	for _, l := range Lenses() {
		specs[l.CanonicalName] = l.Spec
	}
	var checked int
	for _, tgt := range WeaverTargets() {
		spec, ok := specs[tgt.LensRef]
		require.Truef(t, ok, "target %s names lens %s, which this package must declare", tgt.TargetID, tgt.LensRef)
		require.Containsf(t, spec, "byTarget."+tgt.TargetID,
			"lens %s must read the marker under its own target id %q — the timer that fires writes that entry and no other",
			tgt.LensRef, tgt.TargetID)
		checked++
	}
	require.Equal(t, 2, checked,
		"wellnessBookingReminders and pastDueBookings each read a recorded lapse; a drop here is a lens that went back to a clock")
}

// TestBookingLenses_AgreeOnTheStatusThatArmsTheEndTimer pins the wellness half of
// the coupling clinic-reminders' nonTerminalAppointment fragment holds.
//
// wellnessBookingReminders closes its gate on byTarget.pastDueBookings — the
// recorded end of the class — and pastDueBookings is the only writer of that
// entry. A booking status the reminder lens admits but the past-due lens does
// not would hold the reminder gap open with no term able to close it, so the op
// would refuse every dispatch until the retry budget stood exhausted.
//
// Here the two agree by construction rather than by a shared exclusion list:
// both require status = 'booked' EXACTLY, a positive equality on one value. That
// is stronger than the appointment pair's agreement and cannot drift into a
// partial overlap — but only while it stays an equality on the same literal,
// which is what this pins.
func TestBookingLenses_AgreeOnTheStatusThatArmsTheEndTimer(t *testing.T) {
	for name, spec := range map[string]string{
		"wellnessBookingReminders": wellnessBookingRemindersSpec,
		"pastDueBookings":          pastDueBookingsSpec,
	} {
		require.Containsf(t, spec, "(b.status.data.value = 'booked')",
			"%s must gate on status = 'booked' exactly — the reminder gate closes on a marker only "+
				"pastDueBookings writes, and only for a booked seat", name)
		for _, other := range []string{"attended", "noShow", "waitlisted"} {
			require.NotContainsf(t, spec, "'"+other+"'",
				"%s must decide on the positive 'booked' equality alone; naming %s would make the two lenses' "+
					"status agreement a list that can drift", name, other)
		}
	}
}

// TestReminders_NonBookedNeverViolates is the row-level companion: with the
// reminder lapse recorded, only the status term can decide, and every seat that
// is not `booked` reads closed — the states for which pastDueBookings arms no
// end timer, and so for which no recorded end could ever arrive.
func TestReminders_NonBookedNeverViolates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"waitlisted", "attended", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkBookingSpan(t, "bk", status, "flow", "2026-06-30T09:00:00Z", "2026-06-30T10:00:00Z", "2026-06-29T09:00:00Z")
			f.recordLapse(t, "bk", map[string]string{WellnessBookingRemindersTarget: "2026-06-29T09:00:00Z"})

			v := f.projectReminders(t, "bk")[0].Values
			require.Equalf(t, false, v["missing_reminder"],
				"a %s seat is never reminded — and pastDueBookings arms no end timer for it, so nothing could close this gap", status)
			require.Equal(t, false, v["violating"])
			require.Nil(t, v["freshUntil"])
		})
	}
}
