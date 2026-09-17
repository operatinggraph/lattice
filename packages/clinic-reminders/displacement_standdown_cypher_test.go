package clinicreminders

// Rule-engine proof that the two appointment deadline gates stand down for a
// DISPLACED visit — one the provider's later-declared time-off covers, as
// recorded on the visit's own .displacement aspect — and only for one:
//
//   - appointmentReminders' missing_reminder / violating read false on a
//     displaced visit whose lapse is recorded; freshUntil is untouched.
//   - pastDueAppointments' missing_noshow_transition / violating read false
//     on a displaced visit whose lapse is recorded; freshUntil still arms
//     (the recorded end every sibling reads is still written).
//   - A visit with NO .displacement, or one recorded clear, reads exactly as
//     before on both gates (nil-false on `null = true`).
//   - A reinstatement (displaced → false, the same recorded lapse) re-opens
//     both gaps on the next projection with no clearing write.
//
// Same harness as lens_cypher_test.go / pastdue_cypher_test.go; no clock
// parameter is supplied to any vector.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// displacement writes the visit's .displacement aspect in the shape
// EvaluateAppointmentDisplacement leaves.
func (f *remFixture) displacement(t *testing.T, name string, displaced bool) {
	t.Helper()
	disp := map[string]any{"displaced": displaced, "checkedFor": dpSetRef, "at": "2026-06-21T09:15:03Z"}
	if displaced {
		disp["from"] = "2026-06-30T00:00:00Z"
		disp["to"] = "2026-07-01T00:00:00Z"
	}
	f.aspect(t, name, "displacement", "appointmentDisplacement", disp)
}

// TestReminders_DisplacedStandsDown — the reminder lapse is recorded and the
// reminder never sent (TestReminders_Due's shape), and the visit is
// displaced: the gap reads false. freshUntil is null here for the same reason
// it is on the due row (the lapse is recorded), so the vector that shows
// freshUntil is untouched by the conjunct is TestReminders_DisplacedStillArmsTheTimer.
func TestReminders_DisplacedStandsDown(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})
	f.displacement(t, "appt", true)

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"], "a displaced visit is not reminded — the provider cannot attend it")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → no armed timer, exactly as on a non-displaced due row")
}

// TestReminders_DisplacedStillArmsTheTimer — freshUntil binds to its own
// four terms alone, never the displaced test: a displaced visit whose lapse is NOT yet
// recorded still arms the @at at remindAt, so a reinstatement lands on a
// recorded lapse and is reminded on the next projection.
func TestReminders_DisplacedStillArmsTheTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-07-05T15:00:00Z", "2026-07-04T15:00:00Z", "scheduled", "", "")
	f.displacement(t, "appt", true)

	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, false, v["missing_reminder"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-04T15:00:00Z", v["freshUntil"], "the @at still arms for a displaced visit — the conjunct is on the dispatch bools only")
}

// TestReminders_NotDisplacedUnchanged — a visit recorded CLEAR (displaced:
// false) and a visit with no .displacement at all both read exactly as the
// due row does: nil-false on `null = true`, false on `false = true`.
func TestReminders_NotDisplacedUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("recorded clear", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")
		f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})
		f.displacement(t, "appt", false)
		v := f.projectReminders(t, "appt")[0].Values
		require.Equal(t, true, v["missing_reminder"], "displaced: false → the gate is untouched")
		require.Equal(t, true, v["violating"])
	})
	t.Run("no .displacement", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")
		f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})
		v := f.projectReminders(t, "appt")[0].Values
		require.Equal(t, true, v["missing_reminder"], "no .displacement → null = true is false → NOT false → the gate is untouched")
		require.Equal(t, true, v["violating"])
	})
}

// TestReminders_ReinstatedReopens — displaced, then reinstated (a time-off
// clear or a move records displaced: false) on the same recorded lapse: the
// gap re-opens with no clearing write, off the lapse already recorded.
func TestReminders_ReinstatedReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkAppt(t, "appt", "2026-06-30T15:00:00Z", "2026-06-29T15:00:00Z", "scheduled", "", "")
	f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-06-29T15:00:00Z"})
	f.displacement(t, "appt", true)
	require.Equal(t, false, f.projectReminders(t, "appt")[0].Values["violating"], "displaced: stood down")

	f.displacement(t, "appt", false)
	v := f.projectReminders(t, "appt")[0].Values
	require.Equal(t, true, v["missing_reminder"], "reinstated → reminded on the lapse already recorded")
	require.Equal(t, true, v["violating"])
}

// TestPastDue_DisplacedStandsDown — the lapse at endsAt is recorded and the
// visit is still scheduled (TestPastDue_Due's shape), and the visit is
// displaced: the sweep reads false. The missed visit is the provider's
// unavailability, not the patient's no-show; the desk closes it.
func TestPastDue_DisplacedStandsDown(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "confirmed")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})
	f.displacement(t, "appt", true)

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"], "a displaced visit is never swept to no-show")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse IS recorded — freshUntil stays null exactly as any other past-due row")
}

// TestPastDue_DisplacedStillArmsTheTimer — freshUntil binds to
// nonTerminalAppointment alone, not the displaced exclusion: a displaced
// visit with NO lapse recorded yet still arms the @at at endsAt, because the
// recorded end is the fact every sibling gate (the reminder, the notices,
// the displacement check itself) closes on.
func TestPastDue_DisplacedStillArmsTheTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T15:00:00Z", "2026-06-30T15:30:00Z", "scheduled")
	f.displacement(t, "appt", true)

	v := f.projectPastDue(t, "appt")
	require.Equal(t, false, v["missing_noshow_transition"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-06-30T15:30:00Z", v["freshUntil"], "the @at still arms for a displaced visit — the recorded end is still written")
}

// TestPastDue_NotDisplacedUnchanged — a visit recorded clear and a visit with
// no .displacement both sweep exactly as before.
func TestPastDue_NotDisplacedUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("recorded clear", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "confirmed")
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})
		f.displacement(t, "appt", false)
		v := f.projectPastDue(t, "appt")
		require.Equal(t, true, v["missing_noshow_transition"], "displaced: false → the sweep is untouched")
		require.Equal(t, true, v["violating"])
	})
	t.Run("no .displacement", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "confirmed")
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})
		v := f.projectPastDue(t, "appt")
		require.Equal(t, true, v["missing_noshow_transition"], "no .displacement → the sweep is untouched")
		require.Equal(t, true, v["violating"])
	})
}

// TestPastDue_ReinstatedReopens — displaced then reinstated on the same
// recorded lapse: the sweep re-opens with no clearing write.
func TestPastDue_ReinstatedReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkApptEnds(t, "appt", "2026-06-30T09:00:00Z", "2026-06-30T09:30:00Z", "scheduled")
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-06-30T09:30:00Z"})
	f.displacement(t, "appt", true)
	require.Equal(t, false, f.projectPastDue(t, "appt")["violating"], "displaced: stood down")

	f.displacement(t, "appt", false)
	v := f.projectPastDue(t, "appt")
	require.Equal(t, true, v["missing_noshow_transition"], "reinstated → swept on the lapse already recorded")
	require.Equal(t, true, v["violating"])
}

// TestAppointmentLenses_ShareOneDisplacedTest pins that both dispatch gates
// splice the SAME notDisplacedAppointment fragment, and that neither
// freshUntil CASE carries it: the fragment appears exactly twice per spec
// (the gap bool and the violating repeat), never three times.
func TestAppointmentLenses_ShareOneDisplacedTest(t *testing.T) {
	for name, spec := range map[string]string{
		"appointmentReminders": appointmentRemindersSpec,
		"pastDueAppointments":  pastDueAppointmentsSpec,
	} {
		require.Equalf(t, 2, strings.Count(spec, notDisplacedAppointment),
			"%s must splice notDisplacedAppointment into its gap bool and its violating repeat, and NOT into freshUntil — the @at must still arm for a displaced visit", name)
	}
	require.NotContains(t, appointmentDisplacementsSpec, notDisplacedAppointment,
		"the displacement check itself evaluates displaced visits — it must not stand down on the verdict it maintains")
}
