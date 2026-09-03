package grantchange_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// The personal derivation licence's staleness window, pinned across the three
// packages that each hold one piece of it — the shape
// health/idle_sweep_backoff_test.go uses for IdleSweepBackoffEvery against
// DefaultCapabilitySweepStallCycles, and for the same reason: three constants
// that must stay in a relationship no single package can see.
//
// pipeline holds the window (PersonalHealerStaleCycles) and the cadence it
// assumes for a verdict that carries none; grantchange holds the cadence the
// sweeper actually runs on; health holds the platform's own definition of a
// stalled sweep. Nothing in any of them fails if they drift apart — the licence
// simply starts revoking on jitter, or stops revoking until long after an
// operator has been told the healer stopped.
func TestPersonalHealerStaleWindow_SitsInsideTheSweepStallAlert(t *testing.T) {
	// The assumed cadence must BE the shipped one. pipeline cannot import
	// grantchange (grantchange reaches pipeline through projection), so its
	// default is a mirror — and a mirror that drifts makes every staleness
	// judgement about a clock nothing keeps.
	require.Equal(t, grantchange.DefaultPersonalSweepInterval, pipeline.DefaultPersonalHealerInterval,
		"the licence's assumed healer cadence must equal the sweeper's own default, or a verdict carrying no interval is judged against a clock nothing runs on")

	// Above one, or ordinary tick jitter — a pass a second late behind a slow
	// Core-KV listing — revokes the narrowing on every personal lens at once.
	require.GreaterOrEqual(t, pipeline.PersonalHealerStaleCycles, 2,
		"a one-interval window revokes the licence on tick jitter, which is a cliff bought for nothing")

	// And well inside the platform's own stall threshold, so the narrowing is
	// already gone by the time an operator is told the healer stopped. A licence
	// that outlives the alert about its own healer is one nobody is holding to
	// account.
	require.LessOrEqual(t, pipeline.PersonalHealerStaleCycles*2, health.DefaultCapabilitySweepStallCycles,
		"the licence must revoke well before the sweep-stall alert fires, not after it")
}
