package clinicreminders

// Rule-engine proof of the followUpReminders convergence lens, driven through the
// `full` engine against an embedded NATS Core/Adjacency KV — the appointment-reminder
// mirror keyed on the documented visit's .documentation.followUpDate instead of
// .schedule.remindAt. With an INJECTED $now it pins the time-gated predicate:
//
//   - PENDING (followUpDate > $now): not violating; freshUntil = followUpDate.
//   - DUE (followUpDate <= $now, not sent): violating; missing_followup_reminder true.
//   - SENT (remindedFor = followUpDate): not violating; freshUntil null — converged.
//   - REDOCUMENTED (remindedFor = old date, followUpDate = new future date): re-opens
//     + freshUntil = new date re-arms.
//   - NO FOLLOW-UP / NO ENCOUNTER / CANCELLED: never violating; freshUntil null.

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/stretchr/testify/require"
)

// projectFollowUp runs the anchored followUpReminders spec for one appointment.
// NO clock parameter is supplied — the cypher references none, and passing one
// would let a clock-reading regression pass unnoticed.
func (f *remFixture) projectFollowUp(t *testing.T, apptName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(followUpRemindersSpec)
	require.NoError(t, err, "followUpReminders cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": apptKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkFollowUpAppt seeds one documented appointment with a .status + a
// .documentation aspect ({followUpRequested, followUpDate?, documentedAt}) plus
// its sibling sensitive .encounter aspect, optionally a .followUpReminder
// {sentAt, remindedFor}. encounterPresent=false seeds NEITHER aspect (an
// appointment whose visit was never documented). The anchor is named so
// projectFollowUp targets it. A .schedule is seeded too (a real appointment always
// has one) but the follow-up gate does not read it.
func (f *remFixture) mkFollowUpAppt(t *testing.T, name, status string, encounterPresent, followUpRequested bool, followUpDate, sentAt, remindedFor string) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": "2026-06-20T15:00:00Z", "endsAt": "2026-06-20T15:30:00Z", "remindAt": "2026-06-19T15:00:00Z"})
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
	if encounterPresent {
		f.aspect(t, name, "encounter", "appointmentEncounter", map[string]any{"summary": "visit note"})
		doc := map[string]any{"documentedAt": "2026-06-20T16:00:00Z", "followUpRequested": followUpRequested}
		if followUpRequested && followUpDate != "" {
			doc["followUpDate"] = followUpDate
		}
		f.aspect(t, name, "documentation", "appointmentDocumentation", doc)
	}
	if sentAt != "" {
		marker := map[string]any{"sentAt": sentAt}
		if remindedFor != "" {
			marker["remindedFor"] = remindedFor
		}
		f.aspect(t, name, "followUpReminder", "followUpReminder", marker)
	}
}

// TestFollowUpReminders_Pending — a documented visit requested a follow-up whose
// followUpDate is still in the future: not violating, but freshUntil = followUpDate
// arms the @at timer. Patient + provider linked to prove one-row-per-anchor.
func TestFollowUpReminders_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// followUpDate 6 months out (after now 2026-06-30T12:00Z).
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2027-01-15T09:00:00Z", "", "")
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "forPatient", "appt", "alice")
	f.edge(t, "withProvider", "appt", "drsam")

	rows := f.projectFollowUp(t, "appt")
	require.Len(t, rows, 1, "exactly one row per appointment even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, false, v["missing_followup_reminder"], "no timer has fired on this appointment → not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2027-01-15T09:00:00Z", v["freshUntil"], "freshUntil = followUpDate arms the @at timer while no lapse is recorded")
	require.Equal(t, "2027-01-15T09:00:00Z", v["followUpDate"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestFollowUpReminders_Due — a timer this target armed fired at followUpDate and
// the lapse is recorded, not yet reminded: the gap OPENS
// (missing_followup_reminder + violating true). freshUntil null once the lapse
// lands (the violating-path dispatches, not a timer).
func TestFollowUpReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2026-06-29T09:00:00Z", "", "")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "a recorded lapse at followUpDate + not reminded → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → nothing left to wait for → no armed timer")
	require.Nil(t, v["followUpReminderSentAt"])
}

// TestFollowUpReminders_Sent — once a reminder is recorded for the CURRENT
// followUpDate (remindedFor = followUpDate) the gap is closed and freshUntil goes
// null. Converged.
func TestFollowUpReminders_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2026-06-29T09:00:00Z", "2026-06-29T09:00:05Z", "2026-06-29T09:00:00Z")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "remindedFor = followUpDate → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current followUpDate")
	require.Equal(t, "2026-06-29T09:00:05Z", v["followUpReminderSentAt"])
	require.Equal(t, "2026-06-29T09:00:00Z", v["remindedFor"])
}

// TestFollowUpReminders_Redocumented is the RE-ARM vector: a reminder already
// fired and was recorded for an EARLIER followUpDate, then the visit was
// re-documented with a date that OUTRUNS that recorded instant. Nothing clears
// the marker — MarkExpired never tombstones it — so a presence test would leave
// this appointment permanently due and never re-arm. The comparison self-corrects
// with no clearing write at all.
func TestFollowUpReminders_Redocumented(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2027-01-15T09:00:00Z", "2026-06-01T09:00:05Z", "2026-06-15T09:00:00Z")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2026-06-15T09:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "the recorded lapse is BEHIND the new followUpDate → not yet due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2027-01-15T09:00:00Z", v["freshUntil"], "a lapse the current date has outrun does not disarm it — the @at re-arms with no clearing write")
}

// TestFollowUpReminders_RedocumentedPast is the DEADLINE-MOVED-EARLIER row of the
// state table, asserted deliberately so a later reader does not "fix" it: the
// visit is re-documented with a new followUpDate BELOW an instant this target
// already fired at. That reads lapsed — correctly, a timer did fire at or after
// the new date — so the row is due at once with no second fire.
func TestFollowUpReminders_RedocumentedPast(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2026-06-28T09:00:00Z", "2026-06-01T09:00:05Z", "2026-05-15T09:00:00Z")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "the recorded fire is after the new followUpDate, so it IS a lapse of it")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already lapsed → no armed timer")
}

// TestFollowUpReminders_PastDateProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector. A provider can document a follow-up
// date that is already behind — the visit is over and the target is a soft one —
// and with no marker yet the row must carry that past instant so Weaver publishes
// an overdue @at that fires at once. Nulling it would arm nothing and the reminder
// would never go out.
func TestFollowUpReminders_PastDateProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const followUpDate = "2020-06-01T09:00:00Z"
	f.mkFollowUpAppt(t, "appt", "completed", true, true, followUpDate, "", "")

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, followUpDate, v["freshUntil"],
		"an already-past followUpDate with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_followup_reminder"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: followUpDate})
	v = f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_SiblingTargetLapseDoesNotOpenThisGap is the isolation
// vector: the appointment reminder's own fire sits in the same marker aspect, and
// reading the aspect's presence — or its entity-wide expiredAt — would send a
// follow-up reminder off it.
func TestFollowUpReminders_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2027-01-15T09:00:00Z", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2099-01-01T00:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2027-01-15T09:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestFollowUpReminders_BoundaryMarkerEqualsFollowUpDate pins the `>=` boundary:
// the timer fires AT the deadline and records that instant, so equality is the
// ordinary lapse rather than an edge case that leaves the row armed forever.
func TestFollowUpReminders_BoundaryMarkerEqualsFollowUpDate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const followUpDate = "2027-01-15T09:00:00Z"
	f.mkFollowUpAppt(t, "appt", "completed", true, true, followUpDate, "", "")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: followUpDate})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "marker == followUpDate is a lapse (>= boundary)")
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_ReferencesNoClockParameter — the structural half.
func TestFollowUpReminders_ReferencesNoClockParameter(t *testing.T) {
	requireClockFree(t, "followUpReminders", followUpRemindersSpec)
}

// TestFollowUpReminders_NoFollowUp — a documented visit that did NOT request a
// follow-up (followUpRequested false, no followUpDate): never violating; freshUntil
// null.
func TestFollowUpReminders_NoFollowUp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, false, "", "", "")

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "no follow-up requested → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_NoEncounter — an appointment whose visit was never documented
// (neither .encounter nor .documentation): the followUpDate terms resolve null →
// never violating; freshUntil null. One row per anchor is still produced.
func TestFollowUpReminders_NoEncounter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "scheduled", false, false, "", "", "")

	rows := f.projectFollowUp(t, "appt")
	require.Len(t, rows, 1, "one row per appointment anchor even with no encounter")
	v := rows[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "no encounter → no followUpDate → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_EncounterWithoutDocumentation pins the half-documented
// appointment at this package's own consumer: one carrying the SENSITIVE
// .encounter aspect but no .documentation sibling resolves every
// a.documentation.data.* reference to null, so it is never violating and arms no
// @at timer. A corpus predating the sibling aspect holds exactly that shape, and
// reminding off it would fire against a deadline the lens cannot see. Distinct
// from TestFollowUpReminders_NoEncounter, which has NEITHER aspect.
func TestFollowUpReminders_EncounterWithoutDocumentation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.vtx(t, "appt", "appointment")
	f.aspect(t, "appt", "schedule", "appointmentSchedule", map[string]any{
		"startsAt": "2026-06-20T15:00:00Z", "endsAt": "2026-06-20T15:30:00Z", "remindAt": "2026-06-19T15:00:00Z"})
	f.aspect(t, "appt", "status", "appointmentStatus", map[string]any{"value": "completed"})
	f.aspect(t, "appt", "encounter", "appointmentEncounter", map[string]any{"summary": "visit note, no documentation aspect yet"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "no .documentation aspect → followUpRequested/followUpDate null → never due")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Nil(t, v["followUpDate"])
	require.Nil(t, v["followUpRequested"])
}

// TestFollowUpReminders_Cancelled — the follow-up deadline has lapsed but the
// appointment is cancelled: never reminded; freshUntil null.
func TestFollowUpReminders_Cancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "cancelled", true, true, "2026-06-29T09:00:00Z", "", "")
	f.recordLapse(t, "appt", map[string]string{FollowUpRemindersTarget: "2026-06-29T09:00:00Z"})

	v := f.projectFollowUp(t, "appt")[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "cancelled → never reminded, even with the lapse recorded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}
