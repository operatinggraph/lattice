package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComputeStudios_ThreadsNoShowFeeCents proves a studio's recorded
// no-show policy survives the decode to the /api/studios row unchanged —
// the value the roster's attendanceActions and the Studios admin card's fee
// line both resolve their label from (cmd/wellness-app/web/app.js).
func TestComputeStudios_ThreadsNoShowFeeCents(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.studio.s1": map[string]any{
			"studioKey":      "vtx.studio.s1",
			"name":           "Flow Room",
			"noShowFeeCents": 1000.0,
		},
	})
	rows := computeStudios([]string{"vtx.studio.s1"}, get)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].NoShowFeeCents)
	require.Equal(t, int64(1000), *rows[0].NoShowFeeCents)
}

// TestComputeStudios_ZeroNoShowFeeCentsIsThreadedNotDropped proves a
// recorded fee-free policy (noShowFeeCents: 0, distinct from no policy
// recorded at all) survives the decode as an explicit *int64 pointing at 0,
// not as a nil that would read the same as "no policy" on the roster.
func TestComputeStudios_ZeroNoShowFeeCentsIsThreadedNotDropped(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.studio.s2": map[string]any{
			"studioKey":      "vtx.studio.s2",
			"name":           "Power Room",
			"noShowFeeCents": 0.0,
		},
	})
	rows := computeStudios([]string{"vtx.studio.s2"}, get)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].NoShowFeeCents)
	require.Equal(t, int64(0), *rows[0].NoShowFeeCents)
}

// TestComputeStudios_NoNoShowFeeCentsIsNilNotZero proves a studio with no
// recorded policy threads through as a nil pointer (omitted from the JSON
// response) rather than collapsing to 0 — the distinction the roster's
// resolveNoShowFeeCents and the studio card's fee line both depend on to
// tell "bills the $25 default" from "bills nothing".
func TestComputeStudios_NoNoShowFeeCentsIsNilNotZero(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.studio.s3": map[string]any{
			"studioKey": "vtx.studio.s3",
			"name":      "Restore Room",
		},
	})
	rows := computeStudios([]string{"vtx.studio.s3"}, get)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].NoShowFeeCents)
}
