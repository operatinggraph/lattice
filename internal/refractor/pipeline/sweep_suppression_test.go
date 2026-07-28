// The sweep's own liveness. Every verdict the sweep publishes — heal count,
// divergent streak, failing actors — describes the last pass that reached one,
// so a sweep that stops reaching them republishes its final verdict forever. A
// suppressed tick reaches no verdict at all, which is why it must leave a
// record of WHY, and must not touch the liveness clock that says how old the
// published verdict is.
package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
)

// attachReporter gives a sweep pipeline a real health Reporter. The reporter
// keys its entry on the bare ruleID, which no anchor or target key shape can
// collide with, so the pipeline's own Core KV bucket serves as the health bucket
// and the test needs no second server.
func attachReporter(t *testing.T, p *Pipeline) *health.Reporter {
	t.Helper()
	r := health.New(p.coreKV, p.ruleID)
	p.reporter = r
	return r
}

func TestSweepPass_ASuppressedTickRecordsWhyAndDoesNotAgeTheClock(t *testing.T) {
	// A rebuild legitimately supersedes the sweep, but nothing bounds how long
	// it holds: if the tick returns silently, the lens keeps publishing the
	// verdict of whichever pass last ran while verifying nothing.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	writeAnchor(t, p, sweepActorA, false)
	sw := p.Sweeper()

	sw.pass(context.Background())
	firstPassAt := sw.Status().LastPassAt
	require.False(t, firstPassAt.IsZero(), "a completed pass stamps the clock")
	require.Empty(t, sw.Status().Suppression)

	p.rebuildInFlight.Store(true)
	sw.pass(context.Background())

	st := sw.Status()
	require.Contains(t, st.Suppression, "rebuild in flight")
	require.Equal(t, firstPassAt, st.LastPassAt,
		"a suppressed tick verified nothing, so the liveness clock must keep aging")
}

func TestSweepPass_AnUnreadableLensStatusSuppressesWithItsCause(t *testing.T) {
	// The fail-closed skip: an unreadable health entry means the pause state is
	// unknown, so the sweep stands down — indefinitely, if the read keeps
	// failing. The cause has to travel, or the operator sees a converged lens.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	attachReporter(t, p)
	// An entry that cannot be unmarshalled is the reachable form of the
	// unreadable branch; a missing entry is legitimately "active".
	_, err := p.coreKV.Put(context.Background(), p.ruleID, []byte("{not json"))
	require.NoError(t, err)
	sw := p.Sweeper()

	sw.pass(context.Background())

	st := sw.Status()
	require.Contains(t, st.Suppression, "unreadable")
	require.True(t, st.LastPassAt.IsZero(), "no pass ever reached a verdict")
}

func TestSweepPass_APausedLensNamesThePauseAsTheSuppression(t *testing.T) {
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	r := attachReporter(t, p)
	require.NoError(t, r.SetPaused(context.Background(), "operator", ""))

	p.Sweeper().pass(context.Background())

	require.Contains(t, p.Sweeper().Status().Suppression, "paused")
}

func TestSweepPass_ATickThatRunsClearsAStaleSuppression(t *testing.T) {
	// The reason describes the LAST tick, not a high-water mark: a rebuild that
	// finishes must not leave the lens looking suppressed forever.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	sw := p.Sweeper()

	p.rebuildInFlight.Store(true)
	sw.pass(context.Background())
	require.NotEmpty(t, sw.Status().Suppression)

	p.rebuildInFlight.Store(false)
	sw.pass(context.Background())
	require.Empty(t, sw.Status().Suppression)
	require.False(t, sw.Status().LastPassAt.IsZero())
}

func TestSweepPass_AFaultedPassStillAgesTheClockForward(t *testing.T) {
	// A pass-level fault DID reach a verdict (FailedStreak carries it), so it is
	// not a stall — the repair-failing signal owns that case, and double-counting
	// it as a stalled sweep would name the wrong remediation.
	p := newSweepPipeline(t, &listingAdapter{listErr: errUnwritableTarget}, 10)
	writeAnchor(t, p, sweepActorA, false)
	sw := p.Sweeper()

	sw.pass(context.Background())

	st := sw.Status()
	require.False(t, st.LastPassAt.IsZero())
	require.Empty(t, st.Suppression, "a fault is a verdict, not a suppression")
	require.Equal(t, 1, st.FailedStreak)
}

func TestSweepSuppression_TruncatesAnOversizeCause(t *testing.T) {
	// The reason rides into a Health-KV document, which has a NATS payload limit.
	sw := newSweepPipeline(t, &listingAdapter{}, 10).Sweeper()

	sw.noteSuppressed(strings.Repeat("x", maxFailureText*3))

	require.LessOrEqual(t, len(sw.Status().Suppression), maxFailureText+len("…"))
}

func TestSweeper_IntervalReportsTheAppliedDefault(t *testing.T) {
	// The heartbeat scales its staleness window off this, so a zero would make
	// the window zero and flag every lens as stalled on its first beat.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	require.Equal(t, time.Hour, p.Sweeper().Interval())

	bare := newSweeper(p, SweepPlan{AnchorType: "identity", AnchorFromKey: sweepAnchorFromKey})
	require.Equal(t, DefaultSweepInterval, bare.Interval())
}
