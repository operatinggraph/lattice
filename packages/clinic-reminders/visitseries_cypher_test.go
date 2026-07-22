package clinicreminders

// Rule-engine proof of the visitSeriesDue convergence lens, driven through the
// `full` engine against an embedded NATS Core/Adjacency KV — the rolling-recurring
// mirror of followUpReminders. With an INJECTED $now it pins the time-gated
// predicate:
//
//   - PENDING (nextDueAt > $now, active): not violating; freshUntil = nextDueAt.
//   - DUE (nextDueAt <= $now, active): violating; missing_series_advance true; freshUntil null.
//   - PAUSED: never violating regardless of nextDueAt; freshUntil null.
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
// .paused {value} aspect. The anchor is named so projectSeriesAt targets it.
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

// projectSeriesAt runs the anchored visitSeriesDue spec for one series with an
// INJECTED $now (the same param executeFullForActor supplies live).
func (f *remFixture) projectSeriesAt(t *testing.T, seriesName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(visitSeriesDueSpec)
	require.NoError(t, err, "visitSeriesDue cypher must parse on the full engine")
	seriesKey := "vtx.visitseries." + f.ids[seriesName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    seriesKey,
		"now":         now,
		"projectedAt": now,
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

	rows := f.projectSeriesAt(t, "series", remNow)
	require.Len(t, rows, 1, "exactly one row per series even with patient + provider linked")
	v := rows[0].Values
	require.Equal(t, "vtx.visitseries."+f.ids["series"], v["entityKey"])
	require.Equal(t, false, v["missing_series_advance"], "nextDueAt still future — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-15T09:00:00Z", v["freshUntil"], "freshUntil = nextDueAt arms the @at timer")
	require.Equal(t, true, v["active"])
	require.Equal(t, "vtx.patient."+f.ids["alice"], v["patientKey"])
	require.Equal(t, "vtx.provider."+f.ids["drsam"], v["providerKey"])
}

// TestVisitSeriesDue_Due — nextDueAt has passed, active: the gap OPENS (missing_series_advance +
// violating true). freshUntil null once due — the violating-path dispatches, not a
// timer.
func TestVisitSeriesDue_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	f.mkVisitSeries(t, "series", 30, "", "2026-06-29T09:00:00Z", 2, nil)

	v := f.projectSeriesAt(t, "series", remNow)[0].Values
	require.Equal(t, true, v["missing_series_advance"], "nextDueAt passed → due")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "already due → no future deadline → no armed timer")
	require.Equal(t, float64(2), v["occurrenceCount"])
}

// TestVisitSeriesDue_Paused — nextDueAt has passed but the series is paused: never
// violating, freshUntil null, regardless of the deadline.
func TestVisitSeriesDue_Paused(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	yes := true
	f.mkVisitSeries(t, "series", 30, "", "2026-06-29T09:00:00Z", 2, &yes)

	v := f.projectSeriesAt(t, "series", remNow)[0].Values
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

	v := f.projectSeriesAt(t, "series", remNow)[0].Values
	require.Equal(t, true, v["missing_series_advance"], "explicitly resumed + nextDueAt passed → due")
	require.Equal(t, true, v["active"])
}

// TestVisitSeriesDue_PastActiveUntil — nextDueAt would fall past the series'
// activeUntil termination: never violating, freshUntil null (clean termination, no
// cancel op needed) even though nextDueAt itself has passed $now.
func TestVisitSeriesDue_PastActiveUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newRemFixture(t)
	// activeUntil is BEFORE nextDueAt → terminated.
	f.mkVisitSeries(t, "series", 30, "2026-06-20T09:00:00Z", "2026-06-29T09:00:00Z", 5, nil)

	v := f.projectSeriesAt(t, "series", remNow)[0].Values
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

	v := f.projectSeriesAt(t, "series", remNow)[0].Values
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

	rows := f.projectSeriesAt(t, "series", remNow)
	require.Len(t, rows, 1, "one row per series anchor even with no links")
	v := rows[0].Values
	require.Nil(t, v["patientKey"])
	require.Nil(t, v["providerKey"])
}
