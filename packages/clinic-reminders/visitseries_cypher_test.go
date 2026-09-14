package clinicreminders

// Rule-engine proof of the visitSeriesDue convergence lens, driven through the
// `full` engine against an embedded NATS Core/Adjacency KV.
//
// An occurrence is credited by a VISIT, never by the clock: the gap opens once a
// qualifying appointment exists — forPatient the series' own patient, not
// cancelled or noShow, scheduled at or after the series' own nextDueAt, and (when
// the series carries a provider) held with that same provider — and closes the
// instant AdvanceVisitSeries re-anchors nextDueAt past it. No $now is supplied to
// any due-lens vector — the cypher references none, and passing one would let a
// clock-reading regression pass unnoticed.
//
//   - NO QUALIFYING VISIT (active): not violating; handledAt null.
//   - A QUALIFYING VISIT at/after nextDueAt (active, same provider or no
//     provider on the series): violating; missing_series_advance true;
//     handledAt = that visit's startsAt.
//   - PAUSED / PAST activeUntil: never violating regardless of a qualifying
//     visit.
//   - NO PATIENT (link absent, or the patient tombstoned): no row at all —
//     forPatient is the REQUIRED anchor walk, the same population bound
//     visitSeriesRead projects.
//   - freshUntil is never projected — the gap needs no armed timer.

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/stretchr/testify/require"
)

// mkVisitSeries seeds one visitseries vertex with a .series {intervalDays,
// activeUntil?} + a .progress {nextDueAt, occurrenceCount} aspect, and optionally a
// .paused {value} aspect. The anchor is named so projectSeries targets it.
func (f *remFixture) mkVisitSeries(t *testing.T, name string, intervalDays int, activeUntil, nextDueAt string, occurrenceCount int, paused *bool) {
	t.Helper()
	f.vtx(t, name, "visitseries")
	series := map[string]any{"intervalDays": intervalDays, "startAt": "2026-06-01T09:00:00Z"}
	if activeUntil != "" {
		series["activeUntil"] = activeUntil
	}
	f.aspect(t, name, "series", "visitSeriesDefinition", series)
	f.aspect(t, name, "progress", "visitSeriesProgress", map[string]any{"nextDueAt": nextDueAt, "occurrenceCount": occurrenceCount})
	if paused != nil {
		f.aspect(t, name, "paused", "visitSeriesPaused", map[string]any{"value": *paused})
	}
}

// linkPatient seeds a patient vertex and the series' forPatient link. That walk
// is the due lens' REQUIRED match, so every vector that is not itself about a
// missing or tombstoned patient carries it.
func (f *remFixture) linkPatient(t *testing.T, seriesName, patientName string) {
	t.Helper()
	f.vtx(t, patientName, "patient")
	f.edge(t, "forPatient", seriesName, patientName)
}

// linkAppointment seeds one appointment vertex holding the visit-credit
// candidate shape the due lens's reverse forPatient walk reads: a .schedule
// {startsAt, endsAt} + a .status {value} (status == "" leaves the .status
// aspect unwritten entirely — the null-safe-status vector, since an absent
// status is neither 'cancelled' nor 'noShow'), the forPatient edge appointment
// -> patientName, and — when providerName is non-empty — the withProvider edge
// appointment -> providerName. patientName / providerName must already be
// seeded (linkPatient / f.vtx) before this call.
func (f *remFixture) linkAppointment(t *testing.T, patientName, apptName, startsAt, status, providerName string) {
	t.Helper()
	f.vtx(t, apptName, "appointment")
	f.aspect(t, apptName, "schedule", "appointmentSchedule", map[string]any{"startsAt": startsAt, "endsAt": startsAt})
	if status != "" {
		f.aspect(t, apptName, "status", "appointmentStatus", map[string]any{"value": status})
	}
	f.edge(t, "forPatient", apptName, patientName)
	if providerName != "" {
		f.edge(t, "withProvider", apptName, providerName)
	}
}

// projectSeries runs the anchored visitSeriesDue spec for one series. NO clock
// parameter is supplied — the cypher references none, and passing one would let
// a clock-reading regression pass unnoticed.
func (f *remFixture) projectSeries(t *testing.T, seriesName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(visitSeriesDueSpec)
	require.NoError(t, err, "visitSeriesDue cypher must parse on the full engine")
	seriesKey := "vtx.visitseries." + f.ids[seriesName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": seriesKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestVisitSeriesDue_NoQualifyingVisit_NotViolating — an active series with no
// appointment at all: not violating, handledAt null. Patient + provider are
// linked to prove one-row-per-anchor (no fan-out).
func TestVisitSeriesDue_NoQualifyingVisit_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.linkPatient(t, "series", "alice")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "withProvider", "series", "drsam")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "exactly one row per series even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.visitseries."+f.ids["series"], v["entityKey"])
	require.Nil(t, v["handledAt"], "no appointment at all → no qualifying visit")
	require.Equal(t, false, v["missing_series_advance"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, true, v["active"])
	require.Nil(t, v["freshUntil"], "freshUntil is never projected — the gap needs no armed timer")
}

// TestVisitSeriesDue_QualifyingVisitSameProvider_Violating — a visit scheduled
// at nextDueAt, held with the series' own provider: the gap OPENS.
func TestVisitSeriesDue_QualifyingVisitSameProvider_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 2, nil)
	f.linkPatient(t, "series", "alice")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "withProvider", "series", "drsam")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "drsam")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "a qualifying visit at nextDueAt, same provider → due")
	require.Equal(t, true, v["violating"])
	require.Equal(t, nextDueAt, v["handledAt"])
	require.Equal(t, float64(2), v["occurrenceCount"])
	require.Nil(t, v["freshUntil"])
}

// TestVisitSeriesDue_VisitBeforeNextDueAt_NotViolating — a visit exists but it
// is BEFORE nextDueAt: it does not credit the current occurrence.
func TestVisitSeriesDue_VisitBeforeNextDueAt_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", "2026-07-01T09:00:00Z", "scheduled", "")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "a visit before nextDueAt does not qualify")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["handledAt"])
}

// TestVisitSeriesDue_CancelledAndNoShowVisits_NotViolating — two visits at/after
// nextDueAt, one cancelled and one noShow: neither qualifies.
func TestVisitSeriesDue_CancelledAndNoShowVisits_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", "2026-07-16T09:00:00Z", "cancelled", "")
	f.linkAppointment(t, "alice", "visit2", "2026-07-17T09:00:00Z", "noShow", "")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "one row per series even with two candidate appointments")
	v := rows[0].Values
	require.Equal(t, false, v["missing_series_advance"], "cancelled/noShow visits never qualify")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["handledAt"])
}

// TestVisitSeriesDue_WrongProviderThenRightProviderQualifies — the series names
// a provider; a visit at/after nextDueAt with a DIFFERENT provider does not
// qualify, but adding one with the SAME provider does.
func TestVisitSeriesDue_WrongProviderThenRightProviderQualifies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "withProvider", "series", "drsam")
	f.vtx(t, "drother", "provider")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "drother")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "one row per series even with a candidate appointment")
	v := rows[0].Values
	require.Equal(t, false, v["missing_series_advance"], "a visit with a DIFFERENT provider than the series' own does not qualify")
	require.Nil(t, v["handledAt"])

	f.linkAppointment(t, "alice", "visit2", nextDueAt, "scheduled", "drsam")
	rows = f.projectSeries(t, "series")
	require.Len(t, rows, 1, "one row per series even with two candidate appointments")
	v = rows[0].Values
	require.Equal(t, true, v["missing_series_advance"], "adding a SAME-provider visit qualifies")
	require.Equal(t, nextDueAt, v["handledAt"])
}

// TestVisitSeriesDue_VisitWithNoProviderLinkDoesNotQualify — the series names
// a provider; a visit at/after nextDueAt carrying NO withProvider link at all
// (not merely a different one) does not qualify either — `apr.key = pr.key`
// compares two nils-vs-non-nil, never true, the same as an explicit mismatch.
func TestVisitSeriesDue_VisitWithNoProviderLinkDoesNotQualify(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "withProvider", "series", "drsam")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "a provider-bound series' visit with NO provider link does not qualify")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["handledAt"])
}

// TestVisitSeriesDue_ProviderlessSeriesAnyProviderQualifies — a series with no
// withProvider link accepts a visit with ANY provider.
func TestVisitSeriesDue_ProviderlessSeriesAnyProviderQualifies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.vtx(t, "drsam", "provider")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "drsam")

	v := f.projectSeries(t, "series")[0].Values
	require.Nil(t, v["providerKey"], "series carries no withProvider link")
	require.Equal(t, true, v["missing_series_advance"], "a provider-less series accepts any provider")
	require.Equal(t, nextDueAt, v["handledAt"])
}

// TestVisitSeriesDue_LatestHandledAt — two visits both qualify: handledAt is
// the LATEST one (max() over the candidate set), not the earliest. Crediting
// the latest qualifying visit is what lets one AdvanceVisitSeries consume the
// WHOLE booked run — see TestVisitSeriesDue_AdvanceConsumesWholeBookedRun for
// the closing-gap proof.
func TestVisitSeriesDue_LatestHandledAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", "2026-08-01T09:00:00Z", "scheduled", "")
	f.linkAppointment(t, "alice", "visit2", "2026-07-20T09:00:00Z", "scheduled", "")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "one row per series even with two candidate appointments")
	v := rows[0].Values
	require.Equal(t, true, v["missing_series_advance"])
	require.Equal(t, "2026-08-01T09:00:00Z", v["handledAt"], "the LATER of two qualifying visits")
}

// TestVisitSeriesDue_AdvanceConsumesWholeBookedRun is the closing-gap pin: one
// AdvanceVisitSeries dispatch must consume every visit booked ahead of the
// gap, however many there are, so the row cannot stay violating with a moving
// handledAt across repeated dispatches. The interval (3 days) is deliberately
// SHORT relative to how far apart the three booked visits are (up to 9 days):
// crediting only the EARLIEST qualifying visit would roll nextDueAt just 3
// days past it, nowhere near past the other two, so this vector is sensitive
// to which visit the fold picks — crediting the LATEST is what closes the
// whole run in one step. The vector runs the real two-step shape: project the
// PRE-advance row to read handledAt, roll nextDueAt from THAT value exactly as
// AdvanceVisitSeries would, re-seed the fixture with the result (standing in
// for the op's own write), then project again — the gap must be fully closed,
// not left open for the visits the first dispatch's handledAt did not name.
func TestVisitSeriesDue_AdvanceConsumesWholeBookedRun(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const intervalDays = 3
	f.mkVisitSeries(t, "series", intervalDays, "", "2026-07-15T09:00:00Z", 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", "2026-08-01T09:00:00Z", "scheduled", "")
	f.linkAppointment(t, "alice", "visit2", "2026-08-05T09:00:00Z", "scheduled", "")
	f.linkAppointment(t, "alice", "visit3", "2026-08-10T09:00:00Z", "scheduled", "")

	before := f.projectSeries(t, "series")
	require.Len(t, before, 1, "one row per series even with three candidate appointments")
	handledAt, ok := before[0].Values["handledAt"].(string)
	require.True(t, ok, "a qualifying visit must be found before the advance can be simulated")

	rolled, err := time.Parse(time.RFC3339, handledAt)
	require.NoError(t, err)
	postAdvanceNextDueAt := rolled.AddDate(0, 0, intervalDays).Format(time.RFC3339)
	f.mkVisitSeries(t, "series", intervalDays, "", postAdvanceNextDueAt, 1, nil)

	after := f.projectSeries(t, "series")
	require.Len(t, after, 1, "one row per series even with three candidate appointments")
	v := after[0].Values
	require.Nil(t, v["handledAt"], "every booked visit must now be BEFORE the rolled nextDueAt — none re-qualifies")
	require.Equal(t, false, v["missing_series_advance"], "the whole booked run is consumed by the one advance — the gap is closed, not left open for visits the advance's handledAt did not name")
	require.Equal(t, false, v["violating"])
}

// TestVisitSeriesDue_PausedWithQualifyingVisit_NotViolating — a qualifying visit
// exists but the series is paused: never violating regardless.
func TestVisitSeriesDue_PausedWithQualifyingVisit_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	yes := true
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, &yes)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, nextDueAt, v["handledAt"], "the visit is still recorded as a candidate")
	require.Equal(t, false, v["active"])
	require.Equal(t, false, v["missing_series_advance"], "paused → never violating even with a qualifying visit")
	require.Equal(t, false, v["violating"])
}

// TestVisitSeriesDue_PastActiveUntilWithQualifyingVisit_NotViolating — nextDueAt
// falls past the series' own activeUntil (clean termination): never violating
// even with a qualifying visit.
func TestVisitSeriesDue_PastActiveUntilWithQualifyingVisit_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	// activeUntil is BEFORE nextDueAt → terminated.
	f.mkVisitSeries(t, "series", 30, "2026-06-20T09:00:00Z", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, nextDueAt, v["handledAt"])
	require.Equal(t, false, v["active"])
	require.Equal(t, false, v["missing_series_advance"], "nextDueAt past activeUntil → terminated, never due")
	require.Equal(t, false, v["violating"])
}

// TestVisitSeriesDue_NoPatientLinkProducesNoRow — forPatient is the REQUIRED
// anchor walk, so a series carrying no patient link projects NO row: no
// violating row to dispatch AdvanceVisitSeries.
func TestVisitSeriesDue_NoPatientLinkProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)

	rows := f.projectSeries(t, "series")
	require.Empty(t, rows, "no forPatient link → no row at all")
}

// TestVisitSeriesDue_TombstonedPatientProducesNoRow is the headline vector: a
// deleted patient's standing cadence leaves the due population. TombstonePatient
// cascades to no link, but Contract #1 filters the dead vertex out of every
// graph walk, so the REQUIRED forPatient match drops the row — the series stops
// dispatching AdvanceVisitSeries, exactly as it already stopped being readable
// by any hat.
func TestVisitSeriesDue_TombstonedPatientProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "scheduled", "")
	require.Len(t, f.projectSeries(t, "series"), 1, "the live patient's series is due — the positive vector this negative rests on")

	f.tombstoneVertex(t, "alice")
	require.Empty(t, f.projectSeries(t, "series"), "the patient is gone → the series leaves the due population")
}

// TestVisitSeriesDue_PatientWithoutProviderStillProjects — withProvider stays
// OPTIONAL: a series whose provider link is absent still projects its row,
// providerKey null.
func TestVisitSeriesDue_PatientWithoutProviderStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.linkPatient(t, "series", "alice")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "forPatient present is all the row needs")
	v := rows[0].Values
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Nil(t, v["providerKey"], "no withProvider link → null providerKey, row intact")
	require.Nil(t, v["handledAt"], "no appointment linked → no qualifying visit")
	require.Nil(t, v["freshUntil"], "freshUntil is never projected")
}

// TestVisitSeriesDue_StatusAbsentVisit_Violating — a visit at/after nextDueAt
// carrying NO .status aspect at all still qualifies: the null-safe '<>' test
// reads an absent status as neither 'cancelled' nor 'noShow', the same idiom
// `s.paused.data.value <> true` relies on for a never-paused series.
func TestVisitSeriesDue_StatusAbsentVisit_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.linkPatient(t, "series", "alice")
	f.linkAppointment(t, "alice", "visit1", nextDueAt, "", "")

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "a visit with no .status aspect at all still qualifies (null-safe <>)")
	require.Equal(t, nextDueAt, v["handledAt"])
}

// projectSeriesSite runs the anchored visitSeriesSiteBackfill spec for one
// series. Unlike the due lens this gap is not time-gated at all — it converges a
// MISSING RELATIONSHIP — but $now is still supplied, exactly as
// executeFullForActor supplies it to every anchored projection.
func (f *remFixture) projectSeriesSite(t *testing.T, seriesName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(visitSeriesSiteBackfillSpec)
	require.NoError(t, err, "visitSeriesSiteBackfill cypher must parse on the full engine")
	seriesKey := "vtx.visitseries." + f.ids[seriesName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    seriesKey,
		"now":         remNow,
		"projectedAt": remNow,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per series")
	return out[0].Values
}

// TestVisitSeriesSiteBackfill_MissingSite — a series with no atSite link is the
// gap: missing_series_site and violating both true, and providerKey names the
// provider whose site assignment the remediation will consult.
func TestVisitSeriesSiteBackfill_MissingSite(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.vtx(t, "drsam", "provider")
	f.edge(t, "withProvider", "series", "drsam")

	v := f.projectSeriesSite(t, "series")
	require.Equal(t, "vtx.visitseries."+f.ids["series"], v["entityKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
	require.Equal(t, true, v["missing_series_site"], "no atSite link → the gap is open")
	require.Equal(t, true, v["violating"])
}

// TestVisitSeriesSiteBackfill_Sited — a series that already names its site is
// converged, and stays converged.
func TestVisitSeriesSiteBackfill_Sited(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.vtx(t, "drsam", "provider")
	f.vtx(t, "riverside", "building")
	f.edge(t, "withProvider", "series", "drsam")
	f.edge(t, "atSite", "series", "riverside")

	v := f.projectSeriesSite(t, "series")
	require.Equal(t, false, v["missing_series_site"], "a live atSite link closes the gap")
	require.Equal(t, false, v["violating"])
}

// TestVisitSeriesSiteBackfill_NotGatedOnLifecycle — a paused series, and one
// past its own activeUntil, are each as invisible to their front desk as a live
// one once the provider is tombstoned, and staff still need to reach a finished
// cadence to read its history. So the gap is deliberately NOT gated on the
// series' lifecycle state, unlike visitSeriesDue's own `active` predicate.
func TestVisitSeriesSiteBackfill_NotGatedOnLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	yes := true
	cases := []struct {
		name        string
		paused      *bool
		activeUntil string
	}{
		{"paused", &yes, ""},
		{"ended", nil, "2026-06-01T09:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newRemFixture(t)
			f.mkVisitSeries(t, "series", 30, c.activeUntil, "2026-07-15T09:00:00Z", 0, c.paused)

			v := f.projectSeriesSite(t, "series")
			require.Equal(t, true, v["missing_series_site"],
				"a %s series with no site is still missing one — staff visibility outlives the cadence", c.name)
		})
	}
}

// TestVisitSeriesSiteBackfill_NoProviderStillProjects — withProvider is OPTIONAL:
// a series with no provider link still projects a violating row (providerKey
// null). The remediation resolves zero sites for it and cleanly no-ops, which is
// exactly the permanently-open-but-harmless shape the lens doc records.
func TestVisitSeriesSiteBackfill_NoProviderStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)

	v := f.projectSeriesSite(t, "series")
	require.Nil(t, v["providerKey"], "no withProvider link → null providerKey")
	require.Equal(t, true, v["missing_series_site"])
}

// TestVisitSeriesDue_ReferencesNoClockParameter — the structural half.
func TestVisitSeriesDue_ReferencesNoClockParameter(t *testing.T) {
	requireClockFree(t, "visitSeriesDue", visitSeriesDueSpec)
}
