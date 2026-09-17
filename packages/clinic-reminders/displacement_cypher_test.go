package clinicreminders

// Rule-engine proof of the appointmentDisplacements convergence lens, driven
// through the `full` engine against an embedded NATS Core/Adjacency KV — the
// same harness lens_cypher_test.go uses for the reminder lens.
//
// The predicate reads no clock and arms no timer: the gap is a level state
// over two recorded facts — the provider's .timeOff.setRef (an opaque
// per-write key) and the visit's .displacement.checkedFor. Each vector seeds
// the two and asserts the gap column, `violating`, and the columns the
// playbook templates off (providerKey, timeOffSetRef), so a conjunct dropped
// from the expression shows up as the wrong column flipping.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

const (
	dpVisitAt  = "2026-07-20T09:00:00Z"
	dpVisitEnd = "2026-07-20T09:30:00Z"
	// Two time-off writes' setRefs (requestIds) and the one instant they
	// share — the whole-second setAt cannot tell them apart, the ref can.
	dpSetRef     = "CRtimeOffRef1HJKMNPQ"
	dpSetRefNext = "CRtimeOffRef2HJKMNPQ"
	dpSetAt      = "2026-06-21T09:15:00Z"
)

// dpAppt is one seeded appointment's facts for the displacement lens. Empty
// strings are omitted from the aspects they belong to, so an absent fact is
// genuinely absent (a missing key), never an empty string the engine would
// compare as present.
type dpAppt struct {
	status     string
	startsAt   string
	endsAt     string
	checkedFor string // .displacement.checkedFor
	displaced  *bool  // .displacement.displaced (nil = no .displacement aspect unless checkedFor is set)
	provider   bool   // seed the withProvider walk
	timeOff    bool   // seed the provider's .timeOff aspect (always carrying setAt)
	setRef     string // .timeOff.setRef ("" = the aspect carries none)
}

func boolp(b bool) *bool { return &b }

// mkDisplacementAppt seeds one appointment {.status, .schedule} + its
// .displacement when any displacement fact is given, and, when asked, its
// provider with a .timeOff aspect.
func (f *remFixture) mkDisplacementAppt(t *testing.T, name string, a dpAppt) {
	t.Helper()
	f.vtx(t, name, "appointment")
	f.aspect(t, name, "status", "appointmentStatus", map[string]any{"value": a.status})
	f.aspect(t, name, "schedule", "appointmentSchedule", map[string]any{"startsAt": a.startsAt, "endsAt": a.endsAt})
	if a.checkedFor != "" || a.displaced != nil {
		disp := map[string]any{"at": "2026-06-21T09:15:03Z"}
		if a.displaced != nil {
			disp["displaced"] = *a.displaced
		} else {
			disp["displaced"] = false
		}
		if a.checkedFor != "" {
			disp["checkedFor"] = a.checkedFor
		}
		f.aspect(t, name, "displacement", "appointmentDisplacement", disp)
	}
	if a.provider {
		f.vtx(t, name+"-provider", "provider")
		f.edge(t, "withProvider", name, name+"-provider")
		if a.timeOff {
			off := map[string]any{"ranges": []any{map[string]any{"from": "2026-07-20T00:00:00Z", "to": "2026-07-21T00:00:00Z"}}, "setAt": dpSetAt}
			if a.setRef != "" {
				off["setRef"] = a.setRef
			}
			f.aspect(t, name+"-provider", "timeOff", "providerTimeOff", off)
		}
	}
}

// projectDisplacements runs the anchored appointmentDisplacements spec for
// one appointment. NO clock parameter is supplied: the cypher references
// none, and passing one would let a clock-reading regression pass unnoticed.
func (f *remFixture) projectDisplacements(t *testing.T, apptName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(appointmentDisplacementsSpec)
	require.NoError(t, err, "appointmentDisplacements cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": apptKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per appointment")
	return out[0].Values
}

// requireDisplacementGap asserts the gap column and violating together, and
// that the lens arms no timer.
func requireDisplacementGap(t *testing.T, v map[string]any, open bool, why string) {
	t.Helper()
	require.Equal(t, open, v["missing_displacement_check"], "missing_displacement_check: "+why)
	require.Equal(t, open, v["violating"], "violating: "+why)
	_, hasFreshUntil := v["freshUntil"]
	require.False(t, hasFreshUntil, "the displacement lens arms no timer — no freshUntil column")
}

// TestDisplacements_SetRefWithNoVerdictOpens — the provider has recorded a
// time-off write and the visit carries no .displacement at all: `null <>
// setRef` is true, the gap opens, and the columns the playbook templates off
// (entityKey, providerKey, timeOffSetRef) are all non-null; timeOffSetAt
// rides along as the human-readable instant.
func TestDisplacements_SetRefWithNoVerdictOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})

	v := f.projectDisplacements(t, "appt")
	requireDisplacementGap(t, v, true, "a recorded time-off write, no verdict on the visit")
	require.Equal(t, "vtx.appointment."+f.ids["appt"], v["entityKey"])
	require.Equal(t, "vtx.provider."+f.ids["appt-provider"], v["providerKey"], "the target templates providerKey off this column")
	require.Equal(t, dpSetRef, v["timeOffSetRef"], "the target templates checkedFor off this column")
	require.Equal(t, dpSetAt, v["timeOffSetAt"], "informational: the write's instant")
	require.Nil(t, v["checkedFor"])
	require.Nil(t, v["displaced"])
	require.Equal(t, dpVisitAt, v["startsAt"])
	require.Equal(t, dpVisitEnd, v["endsAt"])
	require.Equal(t, "scheduled", v["status"])
}

// TestDisplacements_StaleVerdictOpens — the visit was evaluated against an
// OLDER write (checkedFor <> the live setRef): the gap opens again, whatever
// the recorded verdict was — and it does so for two writes sharing one
// whole-second setAt, which is exactly the case the ref exists for.
func TestDisplacements_StaleVerdictOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, displaced := range []bool{false, true} {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, checkedFor: dpSetRef, displaced: boolp(displaced), provider: true, timeOff: true, setRef: dpSetRefNext})
		v := f.projectDisplacements(t, "appt")
		requireDisplacementGap(t, v, true, "checkedFor is the previous setRef; the time-off was written again")
		require.Equal(t, dpSetRef, v["checkedFor"])
		require.Equal(t, dpSetAt, v["timeOffSetAt"], "the two writes share one instant — only the ref tells them apart")
		require.Equal(t, displaced, v["displaced"])
	}
}

// TestDisplacements_CurrentVerdictClosed — checkedFor = setRef: the visit was
// evaluated against the current write; converged, whatever the verdict.
func TestDisplacements_CurrentVerdictClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, displaced := range []bool{false, true} {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, checkedFor: dpSetRef, displaced: boolp(displaced), provider: true, timeOff: true, setRef: dpSetRef})
		requireDisplacementGap(t, f.projectDisplacements(t, "appt"), false, "checkedFor = setRef closes the gap")
	}
}

// TestDisplacements_NoTimeOffClosed — the provider has no .timeOff, or one
// carrying no setRef: there is no event to check against, so the gap stays
// closed even on a visit with no verdict (and the templated columns are null
// — which is exactly why the first conjunct must be what closes it). A
// .timeOff carrying a setAt but no setRef is such an aspect: the instant
// alone is not the key.
func TestDisplacements_NoTimeOffClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("no .timeOff", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true})
		v := f.projectDisplacements(t, "appt")
		requireDisplacementGap(t, v, false, "no .timeOff → no setRef → nothing to check against")
		require.Nil(t, v["timeOffSetRef"])
	})
	t.Run(".timeOff carrying setAt but no setRef", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true})
		v := f.projectDisplacements(t, "appt")
		requireDisplacementGap(t, v, false, "a .timeOff with no setRef records no write to check against — setAt alone is not the key")
		require.Nil(t, v["timeOffSetRef"])
		require.Equal(t, dpSetAt, v["timeOffSetAt"])
	})
	t.Run("stale checkedFor but no setRef", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, checkedFor: dpSetRef, provider: true, timeOff: true})
		requireDisplacementGap(t, f.projectDisplacements(t, "appt"), false, "checkedFor <> null is true, but setRef <> null is false")
	})
}

// TestDisplacements_NoProviderWalkClosed — the withProvider walk is missing:
// providerKey and timeOffSetRef project null and the gap is closed, so the
// playbook is never asked to template a null column.
func TestDisplacements_NoProviderWalkClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd})
	v := f.projectDisplacements(t, "appt")
	requireDisplacementGap(t, v, false, "no withProvider walk → no provider → no setRef")
	require.Nil(t, v["providerKey"])
	require.Nil(t, v["timeOffSetRef"])
}

// TestDisplacements_TerminalClosed — a cancelled / completed / no-show visit
// has nothing to displace: closed even with a stale verdict.
func TestDisplacements_TerminalClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"cancelled", "completed", "noShow"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkDisplacementAppt(t, "appt", dpAppt{status: status, startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})
			requireDisplacementGap(t, f.projectDisplacements(t, "appt"), false, "a "+status+" visit has nothing to displace")
		})
	}
}

// TestDisplacements_NonTerminalOpens — every non-terminal status is
// re-checked, a checked-in visit included (the desk has the patient in the
// room; the provider's time-off still says whether they can be seen).
func TestDisplacements_NonTerminalOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, status := range []string{"scheduled", "confirmed", "checkedIn"} {
		t.Run(status, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkDisplacementAppt(t, "appt", dpAppt{status: status, startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})
			requireDisplacementGap(t, f.projectDisplacements(t, "appt"), true, "a "+status+" visit is re-checked")
		})
	}
}

// TestDisplacements_VisitEndedClosed — the sibling pastDueAppointments target
// recorded a fire at or after endsAt: the visit is over and is not
// re-checked. A fire for an EARLIER endsAt the schedule has outrun, or a
// fire under another target's key, is not evidence this visit ended.
func TestDisplacements_VisitEndedClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("fired at endsAt", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: dpVisitEnd})
		requireDisplacementGap(t, f.projectDisplacements(t, "appt"), false, "the visit ENDED (a recorded pastDueAppointments fire >= endsAt)")
	})
	t.Run("fired for an outrun endsAt", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})
		f.recordLapse(t, "appt", map[string]string{PastDueAppointmentsTarget: "2026-07-01T10:00:00Z"})
		requireDisplacementGap(t, f.projectDisplacements(t, "appt"), true, "a past-due fire for an OUTRUN endsAt does not say this visit has ended")
	})
	t.Run("sibling target fire does not close it", func(t *testing.T) {
		f := newRemFixture(t)
		f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, provider: true, timeOff: true, setRef: dpSetRef})
		f.recordLapse(t, "appt", map[string]string{AppointmentRemindersTarget: "2026-07-20T10:00:00Z"})
		requireDisplacementGap(t, f.projectDisplacements(t, "appt"), true, "only the pastDueAppointments key is the visit's recorded end")
	})
}

// TestDisplacements_ReferencesNoClockParameter pins that the cypher reads
// no $now / $projectedAt and arms no timer: the row is a pure function of
// the subgraph.
func TestDisplacements_ReferencesNoClockParameter(t *testing.T) {
	require.NotContains(t, appointmentDisplacementsSpec, "$now")
	require.NotContains(t, appointmentDisplacementsSpec, "$projectedAt")
	require.NotContains(t, appointmentDisplacementsSpec, "freshUntil", "the displacement lens arms no timer")
}

// TestDisplacements_BodyColumnsMatchReturn pins the §10.2↔§10.8 column
// seam: every RETURN column except actorKey is a BodyColumn (so the target's
// row.<column> templates resolve) and no BodyColumn is missing from RETURN;
// the gap is declared in the target and its Params bind the three columns
// the design names, its Reads the visit's schedule and the provider's
// time-off, its one enumeration the withProvider walk.
func TestDisplacements_BodyColumnsMatchReturn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	var body []string
	for _, l := range Lenses() {
		if l.CanonicalName == "appointmentDisplacements" {
			body = l.Output.BodyColumns
		}
	}
	require.NotEmpty(t, body)
	var gaps map[string]pkgmgr.GapActionSpec
	for _, tgt := range WeaverTargets() {
		if tgt.TargetID == AppointmentDisplacementsTarget {
			gaps = tgt.Gaps
		}
	}
	require.NotNil(t, gaps)
	f := newRemFixture(t)
	f.mkDisplacementAppt(t, "appt", dpAppt{status: "scheduled", startsAt: dpVisitAt, endsAt: dpVisitEnd, checkedFor: dpSetRef, displaced: boolp(true), provider: true, timeOff: true, setRef: dpSetRefNext})
	v := f.projectDisplacements(t, "appt")
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
	gap, declared := gaps["missing_displacement_check"]
	require.True(t, declared, "gap column missing_displacement_check must be declared in the target's Gaps map")
	require.Equal(t, "EvaluateAppointmentDisplacement", gap.Operation)
	require.Equal(t, map[string]string{"appointmentKey": "row.entityKey", "providerKey": "row.providerKey", "checkedFor": "row.timeOffSetRef"}, gap.Params)
	require.Equal(t, []string{"row.entityKey", "row.entityKey.schedule", "row.providerKey.timeOff"}, gap.Reads, "the provider ROOT is never read by the op, so it is not declared")
	require.Equal(t, []string{"row.entityKey.status", "row.entityKey.displacement"}, gap.OptionalReads)
	require.Equal(t, []pkgmgr.EnumerationSpec{{Hub: "row.entityKey", Relation: "withProvider", Direction: "out"}}, gap.Enumerations)
}
