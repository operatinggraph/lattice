package clinicreminders

// Rule-engine proof of the followUpReminders convergence lens, driven through the
// `full` engine against an embedded NATS Core/Adjacency KV — the appointment-reminder
// mirror keyed on the documented visit's .encounter.followUpDate instead of
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

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/stretchr/testify/require"
)

// projectFollowUpAt runs the anchored followUpReminders spec for one appointment
// with an INJECTED $now (the same param executeFullForActor supplies live).
func (f *remFixture) projectFollowUpAt(t *testing.T, apptName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(followUpRemindersSpec)
	require.NoError(t, err, "followUpReminders cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    apptKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkFollowUpAppt seeds one documented appointment with a .status + a .encounter
// aspect ({followUpRequested, followUpDate?, documentedAt}), optionally a
// .followUpReminder {sentAt, remindedFor}. encounterPresent=false seeds NO encounter
// (an appointment whose visit was never documented). The anchor is named so
// projectFollowUpAt targets it. A .schedule is seeded too (a real appointment always
// has one) but the follow-up gate does not read it.
func (f *remFixture) mkFollowUpAppt(t *testing.T, name, status string, encounterPresent, followUpRequested bool, followUpDate, sentAt, remindedFor string) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": "2026-06-20T15:00:00Z", "endsAt": "2026-06-20T15:30:00Z", "remindAt": "2026-06-19T15:00:00Z"})
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": status})
	if encounterPresent {
		enc := map[string]any{"summary": "visit note", "documentedAt": "2026-06-20T16:00:00Z", "followUpRequested": followUpRequested}
		if followUpRequested && followUpDate != "" {
			enc["followUpDate"] = followUpDate
		}
		f.aspect(t, name, "encounter", "appointmentEncounter", enc)
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

	rows := f.projectFollowUpAt(t, "appt", remNow)
	require.Len(t, rows, 1, "exactly one row per appointment even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, false, v["missing_followup_reminder"], "followUpDate still future → not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2027-01-15T09:00:00Z", v["freshUntil"], "freshUntil = followUpDate arms the @at timer")
	require.Equal(t, "2027-01-15T09:00:00Z", v["followUpDate"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestFollowUpReminders_Due — followUpDate has passed, not yet reminded: the gap
// OPENS (missing_followup_reminder + violating true). freshUntil null once due (the
// violating-path dispatches, not a timer).
func TestFollowUpReminders_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// followUpDate yesterday (< now) → due.
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2026-06-29T09:00:00Z", "", "")

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "followUpDate passed + not reminded → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no future deadline → no armed timer")
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

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "remindedFor = followUpDate → gap closed")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "freshUntil null once reminded for the current followUpDate")
	require.Equal(t, "2026-06-29T09:00:05Z", v["followUpReminderSentAt"])
	require.Equal(t, "2026-06-29T09:00:00Z", v["remindedFor"])
}

// TestFollowUpReminders_Redocumented — a reminder was already sent for an EARLIER
// followUpDate, then the visit was re-documented with a new (future) followUpDate:
// remindedFor (old) <> followUpDate (new) re-opens the gate, and because the new date
// is future, freshUntil = the new date RE-ARMS the @at. (Not yet violating.)
func TestFollowUpReminders_Redocumented(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// New followUpDate 6 months out (future); remindedFor records an OLD date.
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2027-01-15T09:00:00Z", "2026-06-01T09:00:05Z", "2026-06-15T09:00:00Z")

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "new followUpDate still future → not yet due, but armed")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2027-01-15T09:00:00Z", v["freshUntil"], "remindedFor <> new followUpDate → freshUntil = new date re-arms the @at")
}

// TestFollowUpReminders_RedocumentedPast — re-documented with a new followUpDate that
// is already past: remindedFor (old) <> followUpDate (new) AND new date <= now → due
// immediately (the violating-path dispatches a fresh reminder for the new date).
func TestFollowUpReminders_RedocumentedPast(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "completed", true, true, "2026-06-28T09:00:00Z", "2026-06-01T09:00:05Z", "2026-05-15T09:00:00Z")

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, true, v["missing_followup_reminder"], "remindedFor <> new followUpDate + new date past → due now")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no armed timer")
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

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "no follow-up requested → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_NoEncounter — an appointment whose visit was never documented
// (no .encounter aspect): the followUpDate terms resolve null → never violating;
// freshUntil null. One row per anchor is still produced.
func TestFollowUpReminders_NoEncounter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "scheduled", false, false, "", "", "")

	rows := f.projectFollowUpAt(t, "appt", remNow)
	require.Len(t, rows, 1, "one row per appointment anchor even with no encounter")
	v := rows[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "no encounter → no followUpDate → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestFollowUpReminders_Cancelled — a follow-up due date has passed but the
// appointment is cancelled: never reminded; freshUntil null.
func TestFollowUpReminders_Cancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkFollowUpAppt(t, "appt", "cancelled", true, true, "2026-06-29T09:00:00Z", "", "")

	v := f.projectFollowUpAt(t, "appt", remNow)[0].Values
	require.Equal(t, false, v["missing_followup_reminder"], "cancelled → never reminded")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}
