package wellnessreminders

// Rule-engine proof of the wellnessBookingReminders convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-reminders / wellness-domain use.
// With an INJECTED $now it pins the time-gated convergence predicate
// deterministically. Mirrors clinic-reminders/lens_cypher_test.go, anchored on
// booking instead of appointment (a session has many bookers, so the
// reminder marker — and the gate — lives per-booking):
//
//   - PENDING (remindAt > $now): not violating; freshUntil = remindAt (arms the
//     @at timer) — the class is in the future, reminder not yet due.
//   - DUE (remindAt <= $now < startsAt, not sent, status=booked): violating;
//     missing_reminder true — Weaver dispatches the directOp.
//   - SENT (.reminder.sentAt present for the current startsAt): not violating;
//     freshUntil null (timer cleared) — converged.
//   - WAITLISTED / PAST: never violating; freshUntil null.
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

// The injected projection instant for every case below.
const remNow = "2026-06-30T12:00:00Z"

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

// projectAt runs the anchored wellnessBookingReminders spec for one booking
// with an INJECTED $now (the same param executeFullForActor supplies live).
func (f *remFixture) projectAt(t *testing.T, bookingName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(wellnessBookingRemindersSpec)
	require.NoError(t, err, "wellnessBookingReminders cypher must parse on the full engine")
	bookingKey := "vtx.booking." + f.ids[bookingName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    bookingKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
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

	rows := f.projectAt(t, "bk", remNow)
	require.Len(t, rows, 1, "exactly one row per booking even with session + booker linked")
	v := rows[0].Values
	require.Equal(t, "vtx.booking."+f.ids["bk"], v["entityKey"])
	require.Equal(t, "vtx.booking."+f.ids["bk"], v["actorKey"])
	require.Equal(t, false, v["missing_reminder"], "remindAt is still in the future — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "freshUntil = remindAt arms the @at timer")
	require.Equal(t, "vtx.session."+f.ids["flow"], v["sessionKey"])
	require.Equal(t, "vtx.identity."+f.ids["alice"], v["bookerKey"])
}

// TestReminders_Due — remindAt has passed, startsAt is still future, not yet
// sent, status booked: the gap OPENS (missing_reminder + violating true).
// freshUntil is NULL once due — no timer armed; the violating row itself
// drives the dispatch.
func TestReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 3h out (future), remindAt yesterday (< now) — due.
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "", "")

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindAt passed + not sent + booked + class future → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no future deadline → no armed timer (violating-path dispatches)")
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

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "remindedFor = startsAt → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current time — no timer re-arms")
	require.Equal(t, "2026-06-29T15:00:05Z", v["reminderSentAt"])
	require.Equal(t, "2026-06-30T15:00:00Z", v["remindedFor"])
}

// TestReminders_RescheduledAfterSent — a reminder was already sent FOR an
// earlier startsAt, then ReassignSession moved the class to a new (later)
// time whose remindAt is still in the future: remindedFor (old) <> startsAt
// (new) re-opens the gate, and because the new remindAt is future,
// freshUntil = remindAt RE-ARMS the @at for the new time.
func TestReminders_RescheduledAfterSent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "new remindAt is still future → not yet due, but armed")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "remindedFor <> new startsAt → freshUntil = new remindAt re-arms the @at")
}

// TestReminders_RescheduledIntoWindow — a reminder was sent for an earlier
// startsAt, then ReassignSession moved the class to a time < 24h out (new
// remindAt already past): due immediately.
func TestReminders_RescheduledIntoWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindedFor <> new startsAt + new remindAt past → due now")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no armed timer (violating-path dispatches)")
}

// TestReminders_WaitlistedNeverReminded — a waitlisted booking (no confirmed
// seat) is never reminded, even with a passed remindAt; freshUntil null.
func TestReminders_WaitlistedNeverReminded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "waitlisted", "flow", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "", "")

	v := f.projectAt(t, "bk", remNow)[0].Values
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

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "noShow → class already happened, never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_PastSession — a class already in the past (startsAt <= $now)
// is never reminded; freshUntil null.
func TestReminders_PastSession(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-29T15:00:00Z", "2026-06-28T15:00:00Z", "", "")

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "past class (startsAt <= now) → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_LastMinuteBooking — booked < 24h out so remindAt is already
// past at creation: reminds immediately (due now).
func TestReminders_LastMinuteBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 6h out; remindAt = startsAt − 24h = 18h ago (< now) → due immediately.
	f.mkBooking(t, "bk", "booked", "flow", "2026-06-30T18:00:00Z", "2026-06-29T18:00:00Z", "", "")

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "a <24h booking has a past remindAt → reminded immediately")
	require.Equal(t, true, v["violating"])
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

	v := f.projectAt(t, "bk", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "no session anchor → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}
