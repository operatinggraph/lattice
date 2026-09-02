package health_test

import (
	"context"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

// TestReporter_SetPeakBindingRows_OverwritesBecauseItIsAGauge pins the
// difference between this setter and the cumulative counters beside it: the
// value is the observation window's CURRENT maximum, so it must be allowed to
// fall as a spike ages out. A setter that accumulated (or clamped upward)
// would turn the gauge back into an all-time high-water mark.
func TestReporter_SetPeakBindingRows_OverwritesBecauseItIsAGauge(t *testing.T) {
	env := startLagServer(t)
	ctx := context.Background()

	const ruleID = "rule-peak-gauge"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(ctx))

	require.NoError(t, reporter.SetPeakBindingRows(ctx, 5000))
	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5000), entry.PeakBindingRows)

	require.NoError(t, reporter.SetPeakBindingRows(ctx, 12))
	entry, err = reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(12), entry.PeakBindingRows,
		"the gauge falls when the spike ages out of the window")
}

// TestReporter_SetPeakBindingRows_PreservesEveryOtherField pins the
// read-modify-write: the setter shares the entry with lag, status, and the
// cumulative counters, and must not blank any of them.
func TestReporter_SetPeakBindingRows_PreservesEveryOtherField(t *testing.T) {
	env := startLagServer(t)
	ctx := context.Background()

	const ruleID = "rule-peak-rmw"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(ctx))
	require.NoError(t, reporter.SetProjectionProgress(ctx, 41, time.Now().UTC(), time.Now().UTC(), 7, time.Now().UTC()))
	require.NoError(t, reporter.RecordSecureRedactions(ctx, 3))

	require.NoError(t, reporter.SetPeakBindingRows(ctx, 900))

	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(900), entry.PeakBindingRows)
	require.Equal(t, uint64(41), entry.ProjectionLag)
	require.Equal(t, uint64(7), entry.AckPending)
	require.Equal(t, uint64(3), entry.SecureRedactions)
	require.Equal(t, health.StatusActive, entry.Status)
	require.Equal(t, ruleID, entry.RuleID)
}

// TestLagPoller_PublishesPeakBindingRows pins the transport: the poller's
// optional source lands on the per-lens entry beside projectionLag.
func TestLagPoller_PublishesPeakBindingRows(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 30 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-peak-publish"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	lp.SetPeakRowsFunc(func() (uint64, bool) { return 8421, true })
	_ = startPoller(lp, ctx)

	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.PeakBindingRows == 8421
	}, 2*time.Second, 10*time.Millisecond, "the poller must publish the peak onto the lens entry")
}

// TestLagPoller_EmptyWindowNeverBlanksAStoredPeak pins the lifetime rule that a
// restart, a pause, or a quiet lens must not erase the last real observation.
// The source reports "no sample", and the poller must write NOTHING — a
// fabricated zero here would delete exactly the number an operator diagnosing
// the previous refusal came to read.
//
// The positive vector runs first in the same test: the same poller, with the
// same reporter, DOES publish while the source has a sample, so the assertion
// below is proving suppression rather than a poller that never ran.
//
// "Several further poll cycles ran" is proved by the metrics publish, not by
// Health-KV's lastUpdated: a quiet lag/progress/peak triple is exactly the
// shape the unchanged-value skip (poll, lag_poller.go) now collapses to a
// single Health-KV write, so lastUpdated advancing is no longer evidence the
// poller kept ticking. The metrics publish stays unconditional every tick.
func TestLagPoller_EmptyWindowNeverBlanksAStoredPeak(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 20 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-peak-quiet"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	var mu sync.Mutex
	haveSample := true
	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	// The empty case returns the ZERO value alongside false, exactly as
	// Pipeline.PeakBindingRows does — so a poller that ignored the second
	// return would write 0 over the stored 777 and the assertion below would
	// catch it.
	lp.SetPeakRowsFunc(func() (uint64, bool) {
		mu.Lock()
		defer mu.Unlock()
		if !haveSample {
			return 0, false
		}
		return 777, true
	})

	msgCh := make(chan *nats.Msg, 10)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = startPoller(lp, ctx)

	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.PeakBindingRows == 777
	}, 2*time.Second, 10*time.Millisecond, "positive vector: the poller publishes while the window has a sample")

	mu.Lock()
	haveSample = false
	mu.Unlock()

	// Drain whatever the positive vector already buffered, so the messages
	// waited for next are genuinely post-flip cycles.
	for len(msgCh) > 0 {
		<-msgCh
	}

	// Let several further poll cycles run and confirm the stored peak is
	// untouched throughout each one.
	for i := 0; i < 3; i++ {
		select {
		case <-msgCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for lag metric message #%d", i+1)
		}
		entry, gerr := reporter.GetStatus(context.Background())
		require.NoError(t, gerr)
		require.Equal(t, uint64(777), entry.PeakBindingRows,
			"a window with no sample must leave the stored peak alone")
	}
}

// TestLagPoller_NoPeakSourceLeavesTheFieldAbsent pins that a caller which never
// wires the source is unchanged — the field stays absent rather than being
// written as zero.
//
// "The poller runs its normal cycle" is proved by the metrics publish, not by
// Health-KV's lastUpdated: with no peak source and a constant zero lag, every
// SetProjectionProgress input is identical cycle over cycle, which the
// unchanged-value skip (poll, lag_poller.go) now collapses to one write —
// lastUpdated advancing is no longer evidence the poller kept ticking. The
// metrics publish stays unconditional every tick.
func TestLagPoller_NoPeakSourceLeavesTheFieldAbsent(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 20 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-peak-unwired"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	msgCh := make(chan *nats.Msg, 10)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	_ = startPoller(lp, ctx)

	for i := 0; i < 3; i++ {
		select {
		case <-msgCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for lag metric message #%d", i+1)
		}
		entry, gerr := reporter.GetStatus(context.Background())
		require.NoError(t, gerr)
		require.Zero(t, entry.PeakBindingRows)
	}
}
