package main

import (
	"context"

	"github.com/operatinggraph/lattice/internal/projectionhealth"
)

// withProjectionHealth annotates a protected-read response with whether
// ruleID's Refractor projection is currently paused, so the FE can tell a
// paused projection apart from a genuinely empty roster instead of rendering
// both as "no results". Silent when the health lookup itself could not
// resolve a status (nil conn, unreachable health-kv, a rule that has never
// reported) — never a false projectionHealthy either way.
func (s *server) withProjectionHealth(ctx context.Context, ruleID string, resp map[string]any) map[string]any {
	st := projectionhealth.Check(ctx, s.conn, ruleID)
	if !st.Known {
		return resp
	}
	resp["projectionHealthy"] = !st.Paused
	if st.Paused {
		resp["projectionPauseReason"] = st.PauseReason
	}
	return resp
}
