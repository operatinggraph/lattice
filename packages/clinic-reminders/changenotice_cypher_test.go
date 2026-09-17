package clinicreminders

// Rule-engine proof of the appointmentChangeNotices convergence lens, driven
// through the `full` engine against an embedded NATS Core/Adjacency KV — the
// same harness lens_cypher_test.go uses for the reminder lens.
//
// The predicate reads no clock and arms no timer: both gaps are level states
// over recorded facts. Each vector seeds the facts (.status {value, at, by},
// .schedule {startsAt, endsAt, movedAt, movedBy}) and the marker (or omits
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

// cnAppt is one seeded appointment's facts. Empty strings are omitted from
// the aspects they belong to, so an absent fact is genuinely absent (a
// missing key), never an empty string the engine would compare as present.
type cnAppt struct {
	status       string
	statusAt     string // .status.at
	statusBy     string // .status.by
	startsAt     string
	endsAt       string
	movedAt      string // .schedule.movedAt
	movedBy      string // .schedule.movedBy
	cancelledFor string // .changeNotice.cancelledFor
	movedFor     string // .changeNotice.movedFor
	displaced    *bool  // .displacement.displaced (nil = no .displacement aspect unless displacedAt is set)
	displacedAt  string // .displacement.at
	displacedFor string // .changeNotice.displacedFor
}

// mkChangeNoticeAppt seeds one appointment {.status, .schedule} + a
// .changeNotice marker when either notice field is set.
func (f *remFixture) mkChangeNoticeAppt(t *testing.T, name string, a cnAppt) {
	t.Helper()
	f.vtx(t, name, "appointment")
	status := map[string]any{"value": a.status}
	if a.statusAt != "" {
		status["at"] = a.statusAt
	}
	if a.statusBy != "" {
		status["by"] = a.statusBy
	}
	f.aspect(t, name, "status", "appointmentStatus", status)
	sched := map[string]any{"startsAt": a.startsAt, "endsAt": a.endsAt}
	if a.movedAt != "" {
		sched["movedAt"] = a.movedAt
	}
	if a.movedBy != "" {
		sched["movedBy"] = a.movedBy
	}
	f.aspect(t, name, "schedule", "appointmentSchedule", sched)
	if a.displaced != nil || a.displacedAt != "" {
		disp := map[string]any{"checkedFor": "CRtimeOffRef1HJKMNPQ"}
		if a.displaced != nil {
			disp["displaced"] = *a.displaced
		}
		if a.displacedAt != "" {
			disp["at"] = a.displacedAt
		}
		if a.displaced != nil && *a.displaced {
			disp["from"] = cnDisplacedFrom
			disp["to"] = cnDisplacedTo
		}
		f.aspect(t, name, "displacement", "appointmentDisplacement", disp)
	}
	if a.cancelledFor != "" || a.movedFor != "" || a.displacedFor != "" {
		marker := map[string]any{"sentAt": cnNoticeSentAt}
		if a.cancelledFor != "" {
			marker["cancelledFor"] = a.cancelledFor
		}
		if a.movedFor != "" {
			marker["movedFor"] = a.movedFor
		}
		if a.displacedFor != "" {
			marker["displacedFor"] = a.displacedFor
		}
		f.aspect(t, name, "changeNotice", "appointmentChangeNotice", marker)
	}
}

// projectChangeNotices runs the anchored appointmentChangeNotices spec for
// one appointment. NO clock parameter is supplied: the cypher references
// none, and passing one would let a clock-reading regression pass unnoticed.
func (f *remFixture) projectChangeNotices(t *testing.T, apptName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(appointmentChangeNoticesSpec)
	require.NoError(t, err, "appointmentChangeNotices cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": apptKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per appointment")
	return out[0].Values
}

// requireNoticeGaps asserts the cancel and move gap columns and violating in
// one place, so every vector checks the column it is NOT about too. The
// displaced gap is asserted closed here — every cancel / move vector seeds no
// displacement — and requireAllNoticeGaps is the three-column form.
func requireNoticeGaps(t *testing.T, v map[string]any, cancel, move bool, why string) {
	t.Helper()
	requireAllNoticeGaps(t, v, cancel, move, false, why)
}

// requireAllNoticeGaps asserts all three gap columns and violating.
func requireAllNoticeGaps(t *testing.T, v map[string]any, cancel, move, displaced bool, why string) {
	t.Helper()
	require.Equal(t, cancel, v["missing_cancel_notice"], "missing_cancel_notice: "+why)
	require.Equal(t, move, v["missing_move_notice"], "missing_move_notice: "+why)
	require.Equal(t, displaced, v["missing_displaced_notice"], "missing_displaced_notice: "+why)
	require.Equal(t, cancel || move || displaced, v["violating"], "violating = any gap: "+why)
	_, hasFreshUntil := v["freshUntil"]
	require.False(t, hasFreshUntil, "the change-notice lens arms no timer — no freshUntil column")
}

const (
	cnVisitAt        = "2026-07-05T15:00:00Z"
	cnVisitEnd       = "2026-07-05T15:30:00Z"
	cnCancelAt       = "2026-06-20T14:02:11Z"
	cnMoveAt         = "2026-06-21T09:30:00Z"
	cnMoveNext       = "2026-06-22T11:00:00Z"
	cnNoticeSentAt   = "2026-06-20T14:02:15Z"
	cnDisplacedAt    = "2026-06-21T09:15:03Z"
	cnDisplacedNext  = "2026-06-23T08:00:02Z"
	cnDisplacedFrom  = "2026-07-05T00:00:00Z"
	cnDisplacedTo    = "2026-07-06T00:00:00Z"
	cnSchedStampedAt = "2026-06-01T10:00:00Z"
)

// TestChangeNotices_DisplacedUntold — the visit's provider's time-off covers
// it (displaced: true, at stamped) and no displacedFor: the displaced gap
// opens; nothing was cancelled or moved. The columns the target templates off
// (entityKey, displacedAt) are non-null.
func TestChangeNotices_DisplacedUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt})

	v := f.projectChangeNotices(t, "appt")
	requireAllNoticeGaps(t, v, false, false, true, "displaced, at stamped, no displacedFor")
	require.Equal(t, true, v["displaced"])
	require.Equal(t, cnDisplacedAt, v["displacedAt"], "the target templates changeRef off this column")
	require.Nil(t, v["displacedFor"])
}

// TestChangeNotices_DisplacedTold — displacedFor = displacement.at: converged.
// A re-evaluation that leaves the visit displaced CARRIES at (clinic-domain's
// stamp_displacement), so the same equality holds and the patient is not
// re-told.
func TestChangeNotices_DisplacedTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt, displacedFor: cnDisplacedAt})

	v := f.projectChangeNotices(t, "appt")
	requireAllNoticeGaps(t, v, false, false, false, "displacedFor = at closes the displaced gap")
	require.Equal(t, cnDisplacedAt, v["displacedFor"])
}

// TestChangeNotices_DisplacedAfreshReopens — told once (displacedFor = the
// first at), reinstated, then displaced again by a later time-off write: the
// fresh at differs from displacedFor and the gap reopens for a fresh notice.
func TestChangeNotices_DisplacedAfreshReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedNext, displacedFor: cnDisplacedAt})

	v := f.projectChangeNotices(t, "appt")
	requireAllNoticeGaps(t, v, false, false, true, "at <> displacedFor (the last displacement the patient was told of) → told again")
}

// TestChangeNotices_NotDisplacedNotTold — a visit recorded CLEAR (displaced:
// false, the booking writers' shape or a reinstatement), and one with no
// .displacement at all: `false = true` / `null = true` are false, nothing to
// tell. A reinstatement is not told (the reminder resumes and says the visit
// stands), even when a displaced notice went out earlier.
func TestChangeNotices_NotDisplacedNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("recorded clear", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(false), displacedAt: cnDisplacedAt})
		requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "displaced: false → nothing to tell")
	})
	t.Run("reinstated after a displaced notice", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(false), displacedAt: cnDisplacedNext, displacedFor: cnDisplacedAt})
		requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "a reinstatement is not told, whatever displacedFor says")
	})
	t.Run("no .displacement", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
		v := f.projectChangeNotices(t, "appt")
		requireAllNoticeGaps(t, v, false, false, false, "no .displacement → null = true is false")
		require.Nil(t, v["displaced"])
		require.Nil(t, v["displacedAt"])
	})
}

// TestChangeNotices_DisplacedWithoutAtNotTold — the at <> null conjunct on
// its own: a .displacement carrying displaced: true but no at has no
// changeRef to dispatch, so the gap stays closed instead of opening one the
// op can only refuse (StaleChange: no at). With no marker either,
// `displacedFor <> at` is already `null <> null` false, so the conjunct is
// discriminated by the second vector: a marker from an earlier notice
// (displacedFor set) against an aspect with no at reads `"x" <> null` true,
// and only the at <> null term keeps that gap closed.
func TestChangeNotices_DisplacedWithoutAtNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("no marker", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true)})
		requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "displaced: true but no at → no changeRef to dispatch")
	})
	t.Run("marker from an earlier notice", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedFor: cnDisplacedAt})
		requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "displaced: true, no at, displacedFor set → still no changeRef to dispatch")
	})
}

// TestChangeNotices_DisplacedThenCancelledByStaff — displaced (untold) and
// then cancelled by the desk: the cancel gap opens, the displaced gap does
// NOT (nonTerminalAppointment is false on cancelled) — the patient is told
// the visit is off, never that the provider is away for a visit that will
// not happen. Every terminal status keeps the displaced gap closed.
func TestChangeNotices_DisplacedThenCancelledByStaff(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt})
	requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), true, false, false, "displaced-then-cancelled → the cancel notice only")

	for _, status := range []string{"completed", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeAppt(t, "appt", cnAppt{status: status, statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt})
			requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "a "+status+" visit is over; nothing to tell")
		})
	}
}

// TestChangeNotices_DisplacedOnNonTerminalTold — every non-terminal status
// opens the displaced gap: a confirmed or checked-in visit the provider can
// no longer attend is still told.
func TestChangeNotices_DisplacedOnNonTerminalTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"scheduled", "confirmed", "checkedIn"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeAppt(t, "appt", cnAppt{status: status, statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt})
			requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, true, "a "+status+" displaced visit is told")
		})
	}
}

// TestChangeNotices_DisplacedVisitEndedNotTold — a displaced visit the
// sibling pastDueAppointments target has recorded as over: the notice is
// moot and the gap closes.
func TestChangeNotices_DisplacedVisitEndedNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, displaced: boolp(true), displacedAt: cnDisplacedAt})
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: cnVisitEnd})
	requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, false, "the visit ENDED (a recorded pastDueAppointments fire >= endsAt) — the notice is moot")
}

// TestChangeNotices_DisplacedAndMovedBothOpen — a desk move untold AND a
// displacement untold on one visit: both gaps read open, and violating
// follows. RescheduleAppointment writes .schedule and .displacement in ONE
// batch (the moved visit reads clear), so the committed state this vector
// seeds is a later re-displacement of the moved slot — a fresh time-off
// write covering the new time, a legitimate second notice — or a per-key
// CDC ordering transient between the two aspects' projections. Either way
// the op re-checks each fact against the live aspect at dispatch, so a
// transient is refused StaleChange there, never told.
func TestChangeNotices_DisplacedAndMovedBothOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: cnSchedStampedAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff", displaced: boolp(true), displacedAt: cnDisplacedAt})
	requireAllNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, true, true, "two untold facts → two open gaps")
}

// TestChangeNotices_StaffCancelUntold — the desk cancelled (by: staff, at
// stamped) and no marker: the cancel gap opens; nothing moved, so the move
// gap stays closed. forPatient is linked to prove one-row-per-anchor.
func TestChangeNotices_StaffCancelUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "appt", "alice")

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, true, false, "a desk cancel with no cancelledFor; nothing moved")
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, cnCancelAt, v["statusAt"], "the target templates changeRef off this column")
	require.Equal(t, "staff", v["statusBy"])
	require.Equal(t, "cancelled", v["status"])
	require.Equal(t, cnVisitAt, v["startsAt"])
	require.Equal(t, cnVisitEnd, v["endsAt"])
	require.Nil(t, v["cancelledFor"])
	require.Nil(t, v["movedAt"])
	require.Nil(t, v["noticeSentAt"])
}

// TestChangeNotices_StaffCancelTold — cancelledFor = status.at: converged.
func TestChangeNotices_StaffCancelTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, cancelledFor: cnCancelAt})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "cancelledFor = at closes the cancel gap; nothing moved")
	require.Equal(t, cnCancelAt, v["cancelledFor"])
	require.Equal(t, cnNoticeSentAt, v["noticeSentAt"])
}

// TestChangeNotices_PatientCancelNotTold — the patient cancelled their own
// visit (by: patient): `'patient' = 'staff'` is false, the desk made no
// change to tell them about.
func TestChangeNotices_PatientCancelNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "patient", startsAt: cnVisitAt, endsAt: cnVisitEnd})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "a patient's own cancel is never told")
	require.Equal(t, "patient", v["statusBy"])
}

// TestChangeNotices_NoAtByCancelNotTold — a cancelled .status carrying
// neither at nor by: `null = 'staff'` is false (nil-false) and `null <> null`
// is false, so a cancel with no recorded moment or author is silent rather
// than told about a moment that was never recorded.
func TestChangeNotices_NoAtByCancelNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", startsAt: cnVisitAt, endsAt: cnVisitEnd})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "no at, no by → no recorded moment → nothing to tell")
	require.Nil(t, v["statusAt"])
	require.Nil(t, v["statusBy"])
}

// TestChangeNotices_CancelByWithoutAtNotTold — the at <> null conjunct on
// its own: a cancelled status carrying by: staff but no at has no changeRef
// to dispatch, so the gap stays closed instead of opening one the op can
// only refuse (StaleChange: no at). With no marker `cancelledFor <> at` is
// already `null <> null` false, so only the second vector — a marker from an
// earlier notice against an aspect with no at, where `"x" <> null` reads
// true — discriminates the conjunct.
func TestChangeNotices_CancelByWithoutAtNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("no marker", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "by: staff but no at → no changeRef to dispatch")
	})
	t.Run("marker from an earlier notice", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, cancelledFor: cnCancelAt})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "cancelled by staff, no at, cancelledFor set → still no changeRef to dispatch")
	})
}

// TestChangeNotices_CancelAfterVisitEndedNotTold — the sibling
// pastDueAppointments target recorded a fire at or after endsAt on a visit
// the desk cancelled: the visit is over, the notice is moot. The cancel's own
// at is BEFORE endsAt here, so only the recorded-end conjunct closes it. The
// sibling fire for an EARLIER endsAt the schedule has outrun is not evidence
// this visit ended — that gap stays open.
func TestChangeNotices_CancelAfterVisitEndedNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("fired at endsAt", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: cnVisitEnd})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "the visit ENDED (a recorded pastDueAppointments fire >= endsAt) — the notice is moot")
	})
	t.Run("fired for an outrun endsAt", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-07-01T10:00:00Z"})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), true, false, "a past-due fire for an OUTRUN endsAt does not say this visit has ended")
	})
	t.Run("sibling target fire does not close it", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
		f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-07-05T16:00:00Z"})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), true, false, "only the pastDueAppointments key is the visit's recorded end")
	})
}

// TestChangeNotices_CancelStampedAfterEndNotTold — the at < endsAt conjunct
// on its own: the desk cancelled a visit AFTER its own end (a manual noShow /
// completed corrected to cancelled — the sibling pastDueAppointments timer is
// disarmed on a terminal status, so NO lapse is ever recorded and the
// recorded-end conjunct has nothing to close on). A cancel stamped at or
// after endsAt is a correction, not news; one second before endsAt is still
// told.
func TestChangeNotices_CancelStampedAfterEndNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name, at string
		open     bool
	}{
		{"stamped after endsAt", "2026-07-05T16:00:00Z", false},
		{"stamped at endsAt exactly", cnVisitEnd, false},
		{"stamped one second before endsAt", "2026-07-05T15:29:59Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: tc.at, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})
			requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), tc.open, false, "no recorded lapse; at "+tc.at+" vs endsAt "+cnVisitEnd)
		})
	}
}

// TestChangeNotices_StaffMoveUntold — the desk moved the visit (movedAt,
// movedBy: staff) and no marker: the move gap opens; the status is
// scheduled, so the cancel gap stays closed.
func TestChangeNotices_StaffMoveUntold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-01T10:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff"})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, true, "a desk move with no movedFor; the visit is not cancelled")
	require.Equal(t, cnMoveAt, v["movedAt"], "the target templates changeRef off this column")
	require.Equal(t, "staff", v["movedBy"])
	require.Nil(t, v["movedFor"])
}

// TestChangeNotices_StaffMoveTold — movedFor = movedAt: converged.
func TestChangeNotices_StaffMoveTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-01T10:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff", movedFor: cnMoveAt})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "movedFor = movedAt closes the move gap")
	require.Equal(t, cnMoveAt, v["movedFor"])
}

// TestChangeNotices_SecondMoveReopens — the patient was told about the first
// move (movedFor = the first movedAt); the desk moved AGAIN, stamping a
// fresh movedAt that differs from movedFor: the gap reopens for a fresh
// notice.
func TestChangeNotices_SecondMoveReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-01T10:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveNext, movedBy: "staff", movedFor: cnMoveAt})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, true, "movedAt <> movedFor (the last move the patient was told of) → a second notice is due")
	require.Equal(t, cnMoveNext, v["movedAt"])
	require.Equal(t, cnMoveAt, v["movedFor"])
}

// TestChangeNotices_PatientMoveNotTold — the patient moved their own visit
// (movedBy: patient): the desk made no change to tell them about. A
// schedule carrying a movedAt but no movedBy reads `null = 'staff'` false
// the same way.
func TestChangeNotices_PatientMoveNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("movedBy patient", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-01T10:00:00Z", statusBy: "patient", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "patient"})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "a patient's own move is never told")
	})
	t.Run("no movedBy", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "movedAt with no movedBy → null = 'staff' is false → not told")
	})
}

// TestChangeNotices_NeverMovedNotTold — a visit created and never
// rescheduled carries no movedAt: `null <> null` is false, the move gap
// never opens on the absence of the fact.
func TestChangeNotices_NeverMovedNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-01T10:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "no movedAt → nothing to tell; scheduled is not cancelled")
	require.Nil(t, v["movedAt"])
}

// TestChangeNotices_MovedThenCancelledByStaff — the desk moved the visit
// (untold) and then cancelled it: the cancel gap opens, the move gap does
// NOT (nonTerminalAppointment is false on cancelled) — the patient is told
// the visit is off, never about a time it will not happen at.
func TestChangeNotices_MovedThenCancelledByStaff(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff"})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, true, false, "moved-then-cancelled → the cancel notice only")
}

// TestChangeNotices_MoveOnTerminalNotTold — with a desk move untold, every
// terminal status keeps the move gap closed; only cancelled-by-staff opens
// the cancel gap.
func TestChangeNotices_MoveOnTerminalNotTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"completed", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeAppt(t, "appt", cnAppt{status: status, statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff"})
			requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, false, "a "+status+" visit has no time left to be told about, and is not cancelled")
		})
	}
}

// TestChangeNotices_MoveOnNonTerminalTold — with a desk move untold, every
// non-terminal status opens the move gap: a confirmed or checked-in visit
// the desk moved is still told.
func TestChangeNotices_MoveOnNonTerminalTold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"scheduled", "confirmed", "checkedIn"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkChangeNoticeAppt(t, "appt", cnAppt{status: status, statusAt: "2026-06-01T10:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff"})
			requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, true, "a "+status+" visit the desk moved is told")
		})
	}
}

// TestChangeNotices_VisitEndedClosesBoth — a desk move untold on a visit
// the sibling pastDueAppointments target has recorded as over (a fire at or
// after endsAt): both gaps close and the row stops dispatching. The status
// here is checkedIn (the one non-terminal value the sweep leaves alone), so
// only the recorded end decides.
func TestChangeNotices_VisitEndedClosesBoth(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "checkedIn", statusAt: "2026-07-05T14:55:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff"})
	f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: cnVisitEnd})

	v := f.projectChangeNotices(t, "appt")
	requireNoticeGaps(t, v, false, false, "the visit ENDED (a recorded pastDueAppointments fire >= endsAt) — both notices are moot")
}

// TestChangeNotices_CancelToldMoveOpen — the marker carries the cancel
// notice only, on a visit that was cancelled, then corrected back to
// scheduled and moved: the cancel gap is closed (status is no longer
// cancelled), the move gap open, and violating follows the open one. The
// converse — a move told, then a fresh desk cancel — opens the cancel gap
// alone.
func TestChangeNotices_OneToldOtherOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("move told, cancel open", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff", movedFor: cnMoveAt})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), true, false, "movedFor = movedAt; cancelledFor absent on a fresh desk cancel")
	})
	t.Run("cancel told, then revived and moved", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "scheduled", statusAt: "2026-06-21T09:00:00Z", statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff", cancelledFor: cnCancelAt})
		requireNoticeGaps(t, f.projectChangeNotices(t, "appt"), false, true, "status is scheduled again; movedFor absent on a fresh desk move")
	})
}

// TestChangeNotices_ReferencesNoClockParameter pins that the cypher reads
// no $now / $projectedAt: the row is a pure function of the subgraph.
func TestChangeNotices_ReferencesNoClockParameter(t *testing.T) {
	require.NotContains(t, appointmentChangeNoticesSpec, "$now")
	require.NotContains(t, appointmentChangeNoticesSpec, "$projectedAt")
	require.NotContains(t, appointmentChangeNoticesSpec, "freshUntil", "the change-notice lens arms no timer")
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
		if l.CanonicalName == "appointmentChangeNotices" {
			body = l.Output.BodyColumns
		}
	}
	require.NotEmpty(t, body)
	var gaps map[string]pkgmgr.GapActionSpec
	for _, tgt := range WeaverTargets() {
		if tgt.TargetID == AppointmentChangeNoticesTarget {
			gaps = tgt.Gaps
		}
	}
	require.NotNil(t, gaps)
	f := newRemFixture(t)
	f.mkChangeNoticeAppt(t, "appt", cnAppt{status: "cancelled", statusAt: cnCancelAt, statusBy: "staff", startsAt: cnVisitAt, endsAt: cnVisitEnd, movedAt: cnMoveAt, movedBy: "staff", cancelledFor: cnCancelAt, movedFor: cnMoveAt})
	f.vtx(t, "alice", "patient")
	f.edge(t, "forPatient", "appt", "alice")
	v := f.projectChangeNotices(t, "appt")
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
	for _, gap := range []string{"missing_cancel_notice", "missing_move_notice", "missing_displaced_notice"} {
		_, declared := gaps[gap]
		require.Truef(t, declared, "gap column %q must be declared in the target's Gaps map", gap)
	}
	require.Equal(t, "row.statusAt", gaps["missing_cancel_notice"].Params["changeRef"], "the cancel gap's changeRef is the recorded status moment")
	require.Equal(t, "row.movedAt", gaps["missing_move_notice"].Params["changeRef"], "the move gap's changeRef is the recorded move moment")
	require.Equal(t, "row.displacedAt", gaps["missing_displaced_notice"].Params["changeRef"], "the displaced gap's changeRef is the recorded displacement moment")
	require.Equal(t, "displaced", gaps["missing_displaced_notice"].Params["kind"])
	require.Equal(t, []string{"row.entityKey.changeNotice", "row.entityKey.displacement"}, gaps["missing_displaced_notice"].OptionalReads,
		"the displaced re-check reads the visit's .displacement — declared beside the marker")
}
