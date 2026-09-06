package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComputeSessions_ProjectsResidentPriceCents proves a session's row
// carries residentPriceCents through to the schedule-grid JSON — the "never
// set or seen" half of verticals.md's wellness resident-price row: the
// reassign form's prefill (app.js) reads this field off the roster-sessions
// response, which shares this same decode path.
func TestComputeSessions_ProjectsResidentPriceCents(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.session.s1": map[string]any{
			"sessionKey":         "vtx.session.s1",
			"name":               "Vinyasa Flow",
			"startsAt":           "2026-07-08T09:00:00Z",
			"endsAt":             "2026-07-08T09:30:00Z",
			"capacity":           20.0,
			"priceCents":         1500.0,
			"residentPriceCents": 1000.0,
		},
	})
	rows := computeSessions([]string{"vtx.session.s1"}, get, map[string]int{})
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ResidentPriceCents)
	require.Equal(t, int64(1000), *rows[0].ResidentPriceCents)
}

// TestComputeSessions_NoResidentPriceCentsIsNilNotZero proves an omitted
// residentPriceCents stays nil through to the JSON row rather than collapsing
// to 0 — the distinction the reassign form's blank-vs-"$0.00" prefill and its
// diff-only-if-changed submit both depend on.
func TestComputeSessions_NoResidentPriceCentsIsNilNotZero(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.session.s2": map[string]any{
			"sessionKey": "vtx.session.s2",
			"name":       "Power Vinyasa",
			"startsAt":   "2026-07-08T09:00:00Z",
			"endsAt":     "2026-07-08T09:30:00Z",
			"capacity":   20.0,
			"priceCents": 1800.0,
		},
	})
	rows := computeSessions([]string{"vtx.session.s2"}, get, map[string]int{})
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].ResidentPriceCents)
}

// TestComputeSessions_ProjectsSeriesKey proves an occurrence's seriesKey
// survives the decode with its VALUE intact (not merely non-empty) and that a
// one-off leaves it empty. The FE hands this exact string back as
// TombstoneSessionSeries' seriesKey payload and derives the atStudio link key
// from its NanoID, so a value picked off the wrong row would submit against
// another series; and its presence alone is what decides whether the series
// controls render at all.
func TestComputeSessions_ProjectsSeriesKey(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.session.s1": map[string]any{
			"sessionKey": "vtx.session.s1",
			"name":       "Evening Flow",
			"startsAt":   "2026-07-08T18:00:00Z",
			"endsAt":     "2026-07-08T19:00:00Z",
			"capacity":   20.0,
			"seriesKey":  "vtx.sessionseries.SER1234567890123456",
		},
		"vtx.session.s2": map[string]any{
			"sessionKey": "vtx.session.s2",
			"name":       "Drop-in Sculpt",
			"startsAt":   "2026-07-09T18:00:00Z",
			"endsAt":     "2026-07-09T19:00:00Z",
			"capacity":   12.0,
		},
	})
	rows := computeSessions([]string{"vtx.session.s1", "vtx.session.s2"}, get, map[string]int{})
	require.Len(t, rows, 2)
	require.Equal(t, "vtx.session.s1", rows[0].SessionKey)
	require.Equal(t, "vtx.sessionseries.SER1234567890123456", rows[0].SeriesKey)
	require.Empty(t, rows[1].SeriesKey, "a one-off class carries no series key")
}
