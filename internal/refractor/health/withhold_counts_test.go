package health_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
)

// TestReporter_SetWithholdCounts_PreservesEveryOtherField pins the
// read-modify-write. The setter shares one entry with lag, status and the
// counters beside it, so writing the two tallies must leave every other field
// exactly where it was.
func TestReporter_SetWithholdCounts_PreservesEveryOtherField(t *testing.T) {
	env := startLagServer(t)
	ctx := context.Background()

	const ruleID = "rule-withhold-rmw"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(ctx))
	require.NoError(t, reporter.SetProjectionProgress(ctx, 41, time.Now().UTC(), time.Now().UTC(), 7, time.Now().UTC()))
	require.NoError(t, reporter.SetPeakBindingRows(ctx, 900))
	require.NoError(t, reporter.RecordSecureRedactions(ctx, 3))

	require.NoError(t, reporter.SetWithholdCounts(ctx, 12345, 2))

	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(12345), entry.EntriesWithheld)
	require.Equal(t, uint64(2), entry.WithholdReadFailures)
	require.Equal(t, uint64(900), entry.PeakBindingRows)
	require.Equal(t, uint64(41), entry.ProjectionLag)
	require.Equal(t, uint64(7), entry.AckPending)
	require.Equal(t, uint64(3), entry.SecureRedactions)
	require.Equal(t, health.StatusActive, entry.Status)
	require.Equal(t, ruleID, entry.RuleID)
}

// TestReporter_SetWithholdCounts_OverwritesRatherThanAccumulates pins the
// division of labour with the pipeline. The pipeline's own counters are already
// cumulative for the life of the process, so this setter MIRRORS them; adding
// here would double-count every poll and make the published number diverge from
// the source it claims to report.
//
// The second write's lower values are the restart case: the counters describe
// one process, and a reader takes a rate from two samples of one process.
func TestReporter_SetWithholdCounts_OverwritesRatherThanAccumulates(t *testing.T) {
	env := startLagServer(t)
	ctx := context.Background()

	const ruleID = "rule-withhold-overwrite"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(ctx))

	require.NoError(t, reporter.SetWithholdCounts(ctx, 500, 4))
	entry, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(500), entry.EntriesWithheld)

	require.NoError(t, reporter.SetWithholdCounts(ctx, 3, 0))
	entry, err = reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), entry.EntriesWithheld, "the published value mirrors the source, it does not accumulate")
	require.Equal(t, uint64(0), entry.WithholdReadFailures)
}

// TestLagPoller_PublishesWithholdCounts is T8's transport half: the pipeline's
// two tallies reach the per-lens entry on the poll cycle, and a lens whose host
// wired no source has its fields left entirely alone rather than written as a
// measured zero.
func TestLagPoller_PublishesWithholdCounts(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 30 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	t.Run("a wired source is published", func(t *testing.T) {
		const ruleID = "rule-withhold-publish"
		reporter := health.New(env.healthKV, ruleID)
		require.NoError(t, reporter.SetActive(context.Background()))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var mu sync.Mutex
		withheld, failures := uint64(1200), uint64(1)
		lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
		lp.SetWithholdCountsFunc(func() (uint64, uint64) {
			mu.Lock()
			defer mu.Unlock()
			return withheld, failures
		})
		_ = startPoller(lp, ctx)

		require.Eventually(t, func() bool {
			entry, err := reporter.GetStatus(context.Background())
			return err == nil && entry.EntriesWithheld == 1200 && entry.WithholdReadFailures == 1
		}, 2*time.Second, 10*time.Millisecond, "the poller must publish both tallies onto the lens entry")

		// A moving counter keeps being published: the unchanged-value skip must
		// suppress a repeated write, never a changed one.
		mu.Lock()
		withheld, failures = 1300, 2
		mu.Unlock()
		require.Eventually(t, func() bool {
			entry, err := reporter.GetStatus(context.Background())
			return err == nil && entry.EntriesWithheld == 1300 && entry.WithholdReadFailures == 2
		}, 2*time.Second, 10*time.Millisecond, "a changed pair must still reach the entry")
	})

	t.Run("no wired source leaves a stored value alone", func(t *testing.T) {
		const ruleID = "rule-withhold-unwired"
		reporter := health.New(env.healthKV, ruleID)
		ctx := context.Background()
		require.NoError(t, reporter.SetActive(ctx))
		require.NoError(t, reporter.SetWithholdCounts(ctx, 42, 7))

		pollCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
		// No SetWithholdCountsFunc: a lens whose host has not wired the source
		// has not measured zero.
		_ = startPoller(lp, pollCtx)

		// The poller runs; the fields must still hold what was stored. Proved
		// by waiting for a DIFFERENT field the poller does write, so this is
		// not merely asserting against a poller that never ticked.
		require.Eventually(t, func() bool {
			entry, err := reporter.GetStatus(ctx)
			return err == nil && entry.LagProgressAt != ""
		}, 2*time.Second, 10*time.Millisecond, "the poller must have completed a cycle")

		entry, err := reporter.GetStatus(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(42), entry.EntriesWithheld)
		require.Equal(t, uint64(7), entry.WithholdReadFailures)
	})
}
