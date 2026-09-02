package clinicreminders

// Rule-engine proof of the visitSeriesDue convergence lens, driven through the
// `full` engine against an embedded NATS Core/Adjacency KV — the rolling-recurring
// mirror of followUpReminders.
//
// What decides "due" is a recorded FACT, not a clock: the instant the @at this
// lens armed actually fired, recorded on the series under this target's own
// byTarget key. No $now is supplied to the due-lens vectors — the cypher
// references none, and passing one would let a clock-reading regression pass
// unnoticed.
//
//   - PENDING (no recorded lapse at nextDueAt, active): not violating;
//     freshUntil = nextDueAt.
//   - DUE (a lapse recorded at or after nextDueAt, active): violating;
//     missing_series_advance true; freshUntil null.
//   - PAUSED: never violating regardless of the recorded lapse; freshUntil null.
//   - PAST activeUntil (nextDueAt > activeUntil): never violating (clean
//     termination); freshUntil null.
//   - NO activeUntil: active is governed by paused alone.

import (
	"context"
	"testing"

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

// TestVisitSeriesDue_Pending — nextDueAt still future, active: not violating, but
// freshUntil = nextDueAt arms the @at timer. Patient + provider linked to prove
// one-row-per-anchor.
func TestVisitSeriesDue_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.vtx(t, "alice", "patient")
	f.vtx(t, "drsam", "provider")
	f.edge(t, "forPatient", "series", "alice")
	f.edge(t, "withProvider", "series", "drsam")

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "exactly one row per series even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.visitseries."+f.ids["series"], v["entityKey"])
	require.Equal(t, false, v["missing_series_advance"], "no timer has fired on this series — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-15T09:00:00Z", v["freshUntil"], "freshUntil = nextDueAt arms the @at timer while no lapse is recorded")
	require.Equal(t, true, v["active"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestVisitSeriesDue_Due — a timer this target armed fired at nextDueAt and the
// lapse is recorded, series active: the gap OPENS (missing_series_advance +
// violating true). freshUntil null once the lapse lands — the violating-path
// dispatches, not a timer.
func TestVisitSeriesDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-06-29T09:00:00Z", 2, nil)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: "2026-06-29T09:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "a recorded lapse at nextDueAt → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → nothing left to wait for → no armed timer")
	require.Equal(t, float64(2), v["occurrenceCount"])
}

// TestVisitSeriesDue_Paused — the deadline has lapsed but the series is paused:
// never violating, freshUntil null, regardless of the recorded fire.
func TestVisitSeriesDue_Paused(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	yes := true
	f.mkVisitSeries(t, "series", 30, "", "2026-06-29T09:00:00Z", 2, &yes)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: "2026-06-29T09:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "paused → never due")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "paused → no armed timer")
	require.Equal(t, false, v["active"])
}

// TestVisitSeriesDue_ExplicitlyResumed — paused explicitly set false (the
// ResumeVisitSeries shape): behaves identically to never-paused (the Pending case).
func TestVisitSeriesDue_ExplicitlyResumed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	no := false
	f.mkVisitSeries(t, "series", 30, "", "2026-06-29T09:00:00Z", 2, &no)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: "2026-06-29T09:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "explicitly resumed + a recorded lapse at nextDueAt → due")
	require.Equal(t, true, v["active"])
}

// TestVisitSeriesDue_PastActiveUntil — nextDueAt would fall past the series'
// activeUntil termination: never violating, freshUntil null (clean termination, no
// cancel op needed) even with the lapse at nextDueAt recorded.
func TestVisitSeriesDue_PastActiveUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// activeUntil is BEFORE nextDueAt → terminated.
	f.mkVisitSeries(t, "series", 30, "2026-06-20T09:00:00Z", "2026-06-29T09:00:00Z", 5, nil)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: "2026-06-29T09:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "nextDueAt past activeUntil → terminated, never due")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Equal(t, false, v["active"])
}

// TestVisitSeriesDue_WithinActiveUntil — nextDueAt is still on-or-before
// activeUntil (not yet terminated) and still future: active + pending, freshUntil
// armed.
func TestVisitSeriesDue_WithinActiveUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// activeUntil is AFTER nextDueAt → still active.
	f.mkVisitSeries(t, "series", 30, "2028-01-01T00:00:00Z", "2026-07-15T09:00:00Z", 1, nil)

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"])
	require.Equal(t, true, v["active"])
	require.Equal(t, "2026-07-15T09:00:00Z", v["freshUntil"])
}

// TestVisitSeriesDue_NoLinks — a series with no patient/provider linked still
// produces exactly one row (informational columns null).
func TestVisitSeriesDue_NoLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)

	rows := f.projectSeries(t, "series")
	require.Len(t, rows, 1, "one row per series anchor even with no links")
	v := rows[0].Values
	require.Nil(t, v["patientKey"])
	require.Nil(t, v["providerKey"])
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

// TestVisitSeriesDue_AdvancedPastTheRecordedLapse is the RE-ARM vector, and the
// shape this lens lives in: AdvanceVisitSeries rewrites nextDueAt to the NEXT
// deadline on the cadence grid, past the instant the fire recorded. Nothing
// clears the marker — MarkExpired never tombstones it — so a presence test would
// leave every advanced series permanently due and the roll would stop after one
// occurrence. The comparison self-corrects with no clearing write at all.
func TestVisitSeriesDue_AdvancedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-29T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 3, nil)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: "2026-06-29T09:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "the recorded lapse is BEHIND the advanced nextDueAt → not yet due")
	require.Equal(t, nextDueAt, v["freshUntil"], "and the next @at re-arms with no clearing write — this is what keeps the series rolling")
}

// TestVisitSeriesDue_PastNextDueAtProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector. A series whose cadence fell behind
// while no target was watching — one started before this target shipped, or one
// whose advance was late — carries no marker, so the only path to recording the
// lapse is projecting the past instant, Weaver publishing the overdue @at, and
// NATS releasing it at once. Nulling a past deadline here arms nothing and the
// series never advances again.
func TestVisitSeriesDue_PastNextDueAtProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const longPast = "2020-06-01T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", longPast, 1, nil)

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past nextDueAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_series_advance"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: longPast})
	v = f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestVisitSeriesDue_BoundaryMarkerEqualsNextDueAt pins the `>=` boundary: the
// timer fires AT nextDueAt and records that instant, so equality is the ordinary
// lapse — the common case here, since the cadence grid and the fire instant are
// the same value.
func TestVisitSeriesDue_BoundaryMarkerEqualsNextDueAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	const nextDueAt = "2026-07-15T09:00:00Z"
	f.mkVisitSeries(t, "series", 30, "", nextDueAt, 0, nil)
	f.recordLapse(t, "series", map[string]string{VisitSeriesDueTarget: nextDueAt})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, true, v["missing_series_advance"], "marker == nextDueAt is a lapse (>= boundary)")
	require.Nil(t, v["freshUntil"])
}

// TestVisitSeriesDue_SiblingTargetLapseDoesNotOpenThisGap is the isolation
// vector. A visitseries anchors visitSeriesDue and visitSeriesSiteBackfill; only
// the former arms a timer today, but the marker slot is shared by construction,
// so reading the aspect's presence or its entity-wide expiredAt would advance a
// series off an unrelated fire.
func TestVisitSeriesDue_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-07-15T09:00:00Z", 0, nil)
	f.recordLapse(t, "series", map[string]string{VisitSeriesSiteBackfillTarget: "2099-01-01T00:00:00Z"})

	v := f.projectSeries(t, "series")[0].Values
	require.Equal(t, false, v["missing_series_advance"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-07-15T09:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestVisitSeriesDue_ReferencesNoClockParameter — the structural half.
func TestVisitSeriesDue_ReferencesNoClockParameter(t *testing.T) {
	requireClockFree(t, "visitSeriesDue", visitSeriesDueSpec)
}
