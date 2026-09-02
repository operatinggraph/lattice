package clinicreminders

// Rule-engine proof of the appointmentReminders convergence lens, driven through
// the `full` engine (engine:"full") against an embedded NATS Core/Adjacency KV —
// the same harness lease-signing / objects-base / clinic-domain use.
//
// The predicate reads no clock: what decides whether the reminder is due is the
// freshnessExpiry marker this appointment carries — the instant a timer armed by
// a named target actually fired — compared against the stored deadline. So every
// vector below seeds a marker (or deliberately omits one) rather than injecting
// a projection instant, and no $now is supplied at all: the cypher references
// none, and passing one would let a clock-reading regression pass unnoticed.
//
//   - PENDING (no recorded lapse at remindAt): not violating; freshUntil =
//     remindAt (arms the @at timer) — the reminder is not yet due.
//   - DUE (a lapse recorded at or after remindAt, not sent, the visit not yet
//     ended): violating; missing_reminder true — Weaver dispatches the directOp.
//   - SENT (.reminder.remindedFor = startsAt): not violating; freshUntil null
//     (timer cleared) — converged.
//   - CANCELLED / ENDED: never violating; freshUntil null.
//   - one row per anchor even with patient + provider linked (0..1 × 0..1 = 1).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// project runs an UNANCHORED spec (a full seed-scan, like clinic-domain's
// clinicAppointmentsReadSpec) and returns every row — the shape a protected
// Postgres read model uses, as opposed to projectReminders' single-anchor
// {key: $actorKey} convergence lenses.
func (f *remFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "clinic-reminders lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// projectReminders runs the anchored appointmentReminders spec for one
// appointment. NO clock parameter is supplied: the cypher references none, and
// passing one would let a clock-reading regression pass unnoticed here.
func (f *remFixture) projectReminders(t *testing.T, apptName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(appointmentRemindersSpec)
	require.NoError(t, err, "appointmentReminders cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": apptKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits onto an
// entity when a target's @at fires: the instant the timer fired for, recorded
// under that target's own key in byTarget, with expiredAt carrying the
// entity-wide maximum. A marker at or after a stored deadline is a recorded
// lapse of it; one before it is a fire for an earlier deadline the current one
// has outrun. byTarget takes several entries because one appointment carries
// three targets in one marker slot.
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

// mkAppt seeds one appointment with a .schedule {startsAt, remindAt} + a .status,
// optionally a .reminder {sentAt, remindedFor}. A sent reminder records the
// startsAt it was for (remindedFor) — the gate converges on remindedFor = startsAt
// and re-opens when a reschedule moves startsAt away from it. The anchor is named
// so projectReminders targets it.
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

// mkApptSpan seeds an appointment whose visit SPAN is explicit — startsAt and
// endsAt differ — for the vectors that turn on "has the visit ended", which
// mkAppt (endsAt = startsAt) cannot express.
func (f *remFixture) mkApptSpan(t *testing.T, name, startsAt, endsAt, remindAt, status string) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": startsAt, "endsAt": endsAt, "remindAt": remindAt})
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
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

	rows := f.projectReminders(t, "appt")
	require.Len(t, rows, 1, "exactly one row per appointment even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["actorKey"])
	require.Equal(t, false, v["missing_reminder"], "no timer has fired on this appointment — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "freshUntil = remindAt arms the @at timer")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestReminders_Due — the reminder timer has FIRED and its lapse is recorded at
// remindAt, the reminder was never sent, and the visit has not ended: the gap
// OPENS (missing_reminder + violating true). freshUntil is NULL once the lapse is
// recorded — there is nothing left to wait for, so no timer is re-armed; the
// violating row itself drives the dispatch (the gap-dispatch path, not a timer).
func TestReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"], "a lapse recorded at remindAt + not sent + visit not ended → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → nothing to wait for → no armed timer (violating-path dispatches)")
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
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "remindedFor = startsAt → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current time — no timer re-arms")
	require.Equal(t, "2026-06-29T15:00:05Z", v["reminderSentAt"])
	require.Equal(t, "2026-06-30T15:00:00Z", v["remindedFor"])
}

// TestReminders_RescheduledAfterSent is the RE-ARM vector, and the whole argument
// for comparing the marker against the deadline rather than testing its presence.
// A reminder already fired and was recorded for an earlier startsAt; the
// appointment then moved to a later time whose remindAt OUTRUNS the recorded
// instant. Nothing clears the marker — MarkExpired never tombstones it — so a
// presence test would leave this appointment permanently "due" and never re-arm.
// The comparison self-corrects with no clearing write at all.
func TestReminders_RescheduledAfterSent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// New startsAt 5 days past the old one, new remindAt 2026-07-04; the marker
	// still records the fire for the OLD 2026-06-26 deadline.
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-26T15:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "the recorded lapse is BEHIND the new remindAt → not yet due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "a lapse the current deadline has outrun does not disarm it — the @at re-arms with no clearing write")
}

// TestReminders_RescheduledIntoWindow is the DEADLINE-MOVED-EARLIER row of the
// state table, and it is asserted deliberately rather than tolerated: a reminder
// was sent for an earlier startsAt, then the appointment moved to a time < 24h
// out so the new remindAt falls BELOW an instant this target already fired at.
// That reads expired, which is CORRECT — a timer did fire at or after the new
// deadline — and the row goes due immediately with no second fire.
func TestReminders_RescheduledIntoWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// The move put remindAt BEHIND the instant already recorded for this target, so
	// the standing marker is a lapse of the new deadline too — the row is due at
	// once with no further fire.
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "2026-06-25T15:00:05Z", "2026-06-26T15:00:00Z")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T18:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"], "remindedFor <> new startsAt + a recorded lapse past the new remindAt → due now")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already lapsed → no armed timer (violating-path dispatches)")
}

// TestReminders_Cancelled — a cancelled appointment is never reminded, even with
// the reminder lapse recorded; freshUntil null.
func TestReminders_Cancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "cancelled", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "cancelled → never reminded, even with the lapse recorded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_StartedButNotEnded is the middle leg of the role-(c) guard's
// three: the visit has begun, no reminder ever went out, and the class is NOT
// over — the pastDueAppointments target's own @at at endsAt has not fired, so
// nothing records the end. The gap therefore STAYS OPEN and the lens keeps
// projecting the row; RecordAppointmentReminder's own guard
// (time.rfc3339_utc(op.submittedAt) < startsAt) is what declines each dispatch.
// The refusals are bounded by the visit's length, not by the retry budget —
// TestReminders_EndedClosesTheGap is the other end of that bound.
func TestReminders_StartedButNotEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptSpan(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "2026-06-29T09:00:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"],
		"a started, never-reminded, not-yet-ended appointment still projects the gap — the op refuses, not the lens")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the reminder lapse is recorded, so no timer re-arms")
}

// TestReminders_EndedClosesTheGap is the closing term. "The appointment is over"
// is a recorded fact, not a clock reading: the sibling pastDueAppointments target
// arms its own @at at endsAt on this same anchor, and its fired marker entry is
// the evidence. Once it lands, the reminder gate closes and Weaver stops
// dispatching an op that could only be refused.
func TestReminders_EndedClosesTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptSpan(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "2026-06-29T09:00:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{
		AppointmentRemindersTarget: "2026-06-29T09:00:00Z",
		PastDueAppointmentsTarget:  "2026-06-30T09:30:00Z",
	})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "the visit ENDED — a reminder is moot and the gate closes")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_SiblingPastDueLapseBeforeTheEndDoesNotClose is the isolation half
// of the term above: the past-due timer may have fired for an EARLIER endsAt the
// current schedule has outrun (a rescheduled visit). That recorded instant is not
// a lapse of THIS endsAt, so the reminder gate stays open — a bare presence test
// on the pastDueAppointments entry would close it wrongly.
func TestReminders_SiblingPastDueLapseBeforeTheEndDoesNotClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptSpan(t, "appt", "2026-07-10T09:00:00Z", "2026-07-10T09:30:00Z", "2026-06-29T09:00:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{
		AppointmentRemindersTarget: "2026-06-29T09:00:00Z",
		PastDueAppointmentsTarget:  "2026-06-30T09:30:00Z",
	})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"], "a past-due fire for an OUTRUN endsAt does not say this visit has ended")
	require.Equal(t, true, v["violating"])
}

// TestReminders_LastMinuteBooking — booked < 24h out so remindAt is already past
// at creation. This is the PAST-DEADLINE-AT-FIRST-PROJECTION vector, and it is
// what makes the conversion of freshUntil load-bearing rather than cosmetic: with
// no marker yet the row must project that past instant VERBATIM so Weaver
// publishes an overdue @at, NATS releases it at once, and THAT fire records the
// lapse. Nulling a past deadline here (the shape the clock-reading form had) arms
// nothing, so the gap would never open at all and the reminder would never go out.
func TestReminders_LastMinuteBooking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const remindAt = "2026-06-29T18:00:00Z"
	f.mkAppt(t, "appt", "2026-06-30T18:00:00Z", remindAt, "scheduled", "", "")

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, remindAt, v["freshUntil"],
		"an already-past remindAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_reminder"], "nothing has fired yet, so the gap is not open until the marker lands")

	// The overdue @at fires and MarkExpired records it: the gap opens on the next
	// delivery, which is the half a gap-only conversion cannot reach.
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: remindAt})
	v = f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"], "the recorded lapse opens the gap")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestReminders_SiblingTargetLapseAloneIsNotThisTargetsLapse is the per-target
// isolation vector. One appointment carries THREE targets in ONE marker aspect
// (appointmentReminders, followUpReminders, pastDueAppointments), so reading the
// aspect's presence — or its entity-wide expiredAt maximum — would let another
// target's timer open this gap.
func TestReminders_SiblingTargetLapseAloneIsNotThisTargetsLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "", "")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2099-01-01T00:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestReminders_BoundaryMarkerEqualsDeadline pins which side of the `>=` the equal
// instant falls on: the timer fires AT the deadline and records that instant, so
// equality is the ordinary lapse rather than an edge case that leaves the row
// armed forever.
func TestReminders_BoundaryMarkerEqualsDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const remindAt = "2026-07-04T15:00:00Z"
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", remindAt, "scheduled", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: remindAt})

	v := f.projectReminders(t, "appt")[0].Values
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
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "", "")
	f.aspect(t, "appt", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "2099-01-01T00:00:00Z"})

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"])
}

// TestReminders_ReferencesNoClockParameter is the structural half of the
// conversion, asserted on the compiled cypher rather than on any one row: a lens
// that returns $now or $projectedAt reproduces a per-anchor evaluation
// differently from the whole-corpus rescan it stands in for, and its projected
// body is a clock reading the sweep's deep verify cannot compare.
func TestReminders_ReferencesNoClockParameter(t *testing.T) {
	requireClockFree(t, "appointmentReminders", appointmentRemindersSpec)
}

// requireClockFree asserts a converted cypher references neither clock parameter
// the projection pipeline supplies. It is the structural companion to the row
// vectors: those prove the recorded fact decides the verdict, this proves no
// clock is left to decide it differently on a re-projection.
func requireClockFree(t *testing.T, name, spec string) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoErrorf(t, err, "%s must parse on the full engine", name)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.Truef(t, isFull, "%s must compile to the full engine", name)
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		require.Truef(t, exhaustive, "%s: the query shape must be provably free of $%s", name, param)
		require.Falsef(t, referenced,
			"%s must reference no $%s — expiry is a recorded fact, not a clock reading", name, param)
	}
}

// TestConvergenceLenses_ReadTheirOwnTargetsMarkerEntry binds the two halves that
// can silently drift apart: the §10.8 TargetID Weaver fires a timer under, and
// the byTarget key the lens compares against its deadline. A rename of one
// without the other leaves a lens reading an entry nothing ever writes — a gap
// that can never open, with every row still projecting and every test about a
// seeded marker still passing. Derived from the shipped target specs, so a new
// deadline-driven target is covered the day it lands rather than when someone
// remembers to add a row.
func TestConvergenceLenses_ReadTheirOwnTargetsMarkerEntry(t *testing.T) {
	specs := map[string]string{}
	for _, l := range Lenses() {
		specs[l.CanonicalName] = l.Spec
	}
	var checked int
	for _, tgt := range WeaverTargets() {
		spec, ok := specs[tgt.LensRef]
		require.Truef(t, ok, "target %s names lens %s, which this package must declare", tgt.TargetID, tgt.LensRef)
		if !strings.Contains(spec, "freshnessExpiry") {
			continue
		}
		require.Containsf(t, spec, "byTarget."+tgt.TargetID,
			"lens %s reads a freshness marker but not under its own target id %q — the timer that fires writes an entry this cypher never reads",
			tgt.LensRef, tgt.TargetID)
		checked++
	}
	require.Equal(t, 4, checked,
		"appointmentReminders, followUpReminders, visitSeriesDue and pastDueAppointments each read a recorded lapse; a drop here is a lens that went back to a clock")
}

// TestReminders_TerminalStatusNeverViolates is the agreement vector between the
// two appointment-anchored deadline lenses, and it is a correctness test rather
// than a tidiness one.
//
// appointmentReminders closes its gate on byTarget.pastDueAppointments — the
// recorded END of the visit — and pastDueAppointments is the ONLY writer of that
// entry, arming its @at only for a non-terminal appointment. So a status
// pastDueAppointments excludes but appointmentReminders admits is a trap with no
// exit: the reminder gap reads true, no past-due timer ever arms, no lapse is
// ever recorded, RecordAppointmentReminder refuses every dispatch, and the
// per-(target, entity, gap) GapBudgetExhausted warning stands for the life of
// the appointment with nothing able to retire it.
//
// Both lenses splice the SAME nonTerminalAppointment fragment, so the lists
// cannot drift; these vectors pin the behaviour that fragment buys, per status,
// with the reminder lapse recorded so only the status term can decide the row.
func TestReminders_TerminalStatusNeverViolates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"completed", "cancelled", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkApptSpan(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "2026-06-29T09:00:00Z", status)
			f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T09:00:00Z"})

			v := f.projectReminders(t, "appt")[0].Values
			require.Equalf(t, false, v["missing_reminder"],
				"a %s appointment is never reminded — and it must not be, because pastDueAppointments arms no timer for it, so nothing could ever close this gap", status)
			require.Equal(t, false, v["violating"])
			require.Nilf(t, v["freshUntil"],
				"and no reminder timer arms for a %s appointment either", status)
		})
	}
}

// TestAppointmentLenses_ShareOneTerminalStatusSet is the structural half of the
// vector above: the same fragment reaches both cyphers. Asserted on the shipped
// specs rather than on the constant, because what matters is that the SPLICE
// happened in both — a lens that inlined its own copy would satisfy a test of
// the constant alone while drifting freely from that moment on.
func TestAppointmentLenses_ShareOneTerminalStatusSet(t *testing.T) {
	for _, terminal := range []string{"completed", "cancelled", "noShow"} {
		require.Containsf(t, nonTerminalAppointment, "<> '"+terminal+"'",
			"clinic-domain's TERMINAL_STATUSES includes %s, so the shared fragment must exclude it", terminal)
	}
	for name, spec := range map[string]string{
		"appointmentReminders": appointmentRemindersSpec,
		"pastDueAppointments":  pastDueAppointmentsSpec,
	} {
		require.Containsf(t, spec, nonTerminalAppointment,
			"%s must splice the SHARED nonTerminalAppointment fragment — the reminder gate closes on a marker "+
				"only pastDueAppointments writes, and only for a non-terminal appointment, so a status one lens "+
				"admits and the other excludes holds a gap open with no term that can ever close it", name)
	}
}
