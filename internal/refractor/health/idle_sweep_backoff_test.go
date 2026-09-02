package health_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// TestIdleSweepBackoffEvery_StaysInsideTheSweepStallWindow pins the
// relationship between two constants in two different packages: the sweep's
// idle back-off (pipeline.IdleSweepBackoffEvery,
// internal/refractor/pipeline/sweep.go) and
// CapabilitySweepStalled/LensSweepStalled's staleness window
// (health.DefaultCapabilitySweepStallCycles).
//
// A skipped idle tick never advances SweepStatus.LastPassAt — only a real
// pass does (pipeline/sweep.go's pass/updateIdleCycle) — so once the back-off
// engages, a real pass recurs only every IdleSweepBackoffEvery ticks. An idle,
// perfectly-converged lens must age its own liveness clock only a fraction of
// the stall window between real passes, or it periodically crosses the
// threshold and raises a false CapabilitySweepStalled/LensSweepStalled
// warning purely from ticking less often. A 2x margin (real-pass recurrence
// at most half the stall window) keeps that comfortable: 5 sweep-interval
// recurrence against a 10-interval window at the 60s auth-plane default, 25
// minutes against 50 at the 5-minute BusinessSweepInterval.
func TestIdleSweepBackoffEvery_StaysInsideTheSweepStallWindow(t *testing.T) {
	require.LessOrEqual(t, pipeline.IdleSweepBackoffEvery*2, health.DefaultCapabilitySweepStallCycles,
		"the idle back-off's real-pass recurrence must stay well inside the sweep-stall alert window, "+
			"or a converged idle lens ages toward its own false stall alert")
}
