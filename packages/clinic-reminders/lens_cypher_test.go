package clinicreminders

// Rule-engine proof of the appointmentReminders convergence lens, driven through
// the `full` engine (engine:"full") against an embedded NATS Core/Adjacency KV —
// the same harness lease-signing / objects-base / clinic-domain use. With an
// INJECTED $now it pins the time-gated convergence predicate deterministically:
//
//   - PENDING (remindAt > $now): not violating; freshUntil = remindAt (arms the
//     @at timer) — the appointment is in the future, reminder not yet due.
//   - DUE (remindAt <= $now < startsAt, not sent): violating; missing_reminder
//     true — Weaver dispatches the directOp.
//   - SENT (.reminder.sentAt present): not violating; freshUntil null (timer
//     cleared) — converged.
//   - CANCELLED / PAST: never violating; freshUntil null.
//   - one row per anchor even with patient + provider linked (0..1 × 0..1 = 1).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

// The injected projection instant for every case below.
const remNow = "2026-06-30T12:00:00Z"

func remCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-clinrem-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-clinrem-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-clinrem-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-clinrem-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func remNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type remFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newRemFixture(t *testing.T) *remFixture {
	adjKV, coreKV := remCypherKVs(t)
	return &remFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *remFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := remNanoID(name)
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

// projectAt runs the anchored appointmentReminders spec for one appointment with
// an INJECTED $now (the same param executeFullForActor supplies live).
func (f *remFixture) projectAt(t *testing.T, apptName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(appointmentRemindersSpec)
	require.NoError(t, err, "appointmentReminders cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    apptKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkAppt seeds one appointment with a .schedule {startsAt, remindAt} + a .status,
// optionally a .reminder {sentAt, remindedFor}. A sent reminder records the
// startsAt it was for (remindedFor) — the gate converges on remindedFor = startsAt
// and re-opens when a reschedule moves startsAt away from it. The anchor is named
// so projectAt targets it.
func (f *remFixture) mkAppt(t *testing.T, name, startsAt, remindAt, status, sentAt, remindedFor string) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": startsAt, "endsAt": startsAt, "remindAt": remindAt})
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
	if sentAt != "" {
		marker := map[string]any{"sentAt": sentAt}
		if remindedFor != "" {
			marker["remindedFor"] = remindedFor
		}
		f.aspect(t, name, "reminder", "appointmentReminder", marker)
	}
}

// TestReminders_Pending — a future appointment whose remindAt has NOT passed: not
// violating, but freshUntil = remindAt arms the @at timer. Patient + provider are
// linked to prove one-row-per-anchor (no fan-out).
func TestReminders_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 5 days out, remindAt 4 days out — both AFTER now (2026-06-30T12:00Z).
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "", "")
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	rows := f.projectAt(t, "appt", remNow)
	require.Len(t, rows, 1, "exactly one row per appointment even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["actorKey"])
	require.Equal(t, false, v["missing_reminder"], "remindAt is still in the future — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "freshUntil = remindAt arms the @at timer")
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestReminders_Due — remindAt has passed, startsAt is still future, not yet sent:
// the gap OPENS (missing_reminder + violating true). freshUntil is NULL once due —
// the deadline is no longer in the future, so no timer is armed; the violating row
// itself drives the dispatch (the gap-dispatch path, not a timer).
func TestReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 3h out (future), remindAt yesterday (< now) — due.
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindAt passed + not sent + appointment future → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no future deadline → no armed timer (violating-path dispatches)")
	require.Nil(t, v["reminderSentAt"])
}

// TestReminders_Sent — once a reminder is recorded for the CURRENT startsAt
// (remindedFor = startsAt) the gap is closed and freshUntil goes null (the @at
// timer clears). Converged.
func TestReminders_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// Reminded FOR the current startsAt (remindedFor == startsAt) → converged.
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "2026-06-29T15:00:05Z", "2026-06-30T15:00:00Z")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "remindedFor = startsAt → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current time — no timer re-arms")
	require.Equal(t, "2026-06-29T15:00:05Z", v["reminderSentAt"])
	require.Equal(t, "2026-06-30T15:00:00Z", v["remindedFor"])
}

// TestReminders_RescheduledAfterSent — a reminder was already sent FOR an earlier
// startsAt, then the appointment was rescheduled to a new (later) time whose
// remindAt is still in the future: remindedFor (old) <> startsAt (new) re-opens the
// gate, and because the new remindAt is future, freshUntil = remindAt RE-ARMS the
// @at for the new time (it is not yet violating — the reminder will fire at the new
// deadline). This is the reschedule re-arm the remindedFor column enables.
func TestReminders_RescheduledAfterSent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// Now is 2026-06-30T12:00Z. New startsAt is 5 days out, new remindAt 4 days out
	// (both future). remindedFor records an OLD startsAt the reminder already fired
	// for.
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "new remindAt is still future → not yet due, but armed")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "remindedFor <> new startsAt → freshUntil = new remindAt re-arms the @at")
}

// TestReminders_RescheduledIntoWindow — a reminder was sent for an earlier startsAt,
// then the appointment was moved to a time < 24h out (new remindAt already past):
// remindedFor (old) <> startsAt (new) AND remindAt <= now → due immediately (the
// violating-path dispatches a fresh reminder for the new time at once).
func TestReminders_RescheduledIntoWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// New startsAt 3h out (future); new remindAt = startsAt − 24h = 21h ago (< now).
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindedFor <> new startsAt + new remindAt past → due now")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no armed timer (violating-path dispatches)")
}

// TestReminders_Cancelled — a cancelled appointment is never reminded, even with a
// passed remindAt; freshUntil null.
func TestReminders_Cancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "cancelled", "", "")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "cancelled → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_PastAppointment — an appointment already in the past (startsAt <=
// $now) is never reminded; freshUntil null.
func TestReminders_PastAppointment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt yesterday (< now), remindAt two days ago.
	f.mkAppt(t, "appt", "2026-06-29T15:00:00Z", "2026-06-28T15:00:00Z", "scheduled", "", "")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_reminder"], "past appointment (startsAt <= now) → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_LastMinuteBooking — booked < 24h out so remindAt is already past
// at creation: reminds immediately (due now).
func TestReminders_LastMinuteBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// startsAt 6h out; remindAt = startsAt − 24h = 18h ago (< now) → due immediately.
	f.mkAppt(t, "appt", "2026-06-30T18:00:00Z", "2026-06-29T18:00:00Z", "scheduled", "", "")

	v := f.projectAt(t, "appt", remNow)[0].Values
	require.Equal(t, true, v["missing_reminder"], "a <24h booking has a past remindAt → reminded immediately")
	require.Equal(t, true, v["violating"])
}
