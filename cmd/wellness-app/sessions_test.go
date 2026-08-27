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
