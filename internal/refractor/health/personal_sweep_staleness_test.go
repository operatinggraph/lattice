package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// TestPersonalSweepVerdict_StaleTokenMatchesThePipelineVocabulary pins the one
// string this package writes onto the field against the constant the licence's
// own vocabulary declares.
//
// health cannot import pipeline (pipeline imports health), so the token is
// spelled twice. Two spellings of one closed vocabulary drift silently, and the
// direction of that drift is a lens entry carrying a token no reader recognizes
// while the sweeper's own `clean` keeps standing.
func TestPersonalSweepVerdict_StaleTokenMatchesThePipelineVocabulary(t *testing.T) {
	require.Equal(t, pipeline.PersonalHealerVerdictStale, health.PersonalSweepVerdictStale,
		"the escalation token and the licence's vocabulary must be the same string")
}

// TestReporter_SetPersonalSweepVerdict covers the second writer of one field and
// the rule that makes two writers safe.
func TestReporter_SetPersonalSweepVerdict(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)

	t.Run("it escalates a verdict the sweep already published", func(t *testing.T) {
		r := health.New(kv, "personal-stale-rule")
		require.NoError(t, r.SetPersonalSweepProgress(ctx, "Hj4kPmRtw9nbCxz5vQ2y", time.Now(), 2, "clean"))
		require.NoError(t, r.SetPersonalSweepVerdict(ctx, health.PersonalSweepVerdictStale))

		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, "stale", entry.PersonalSweepVerdict)
		// Only the token. The cursor and the cycle claim are the SWEEP's
		// coverage record and this writer knows nothing about them; erasing them
		// to report the sweep's silence would destroy the evidence of what it
		// had covered before it went quiet.
		assert.Equal(t, "Hj4kPmRtw9nbCxz5vQ2y", entry.PersonalSweepCursor)
		assert.NotEmpty(t, entry.PersonalSweepCycleCompletedAt)
		assert.Equal(t, uint64(2), entry.PersonalSweepQueueDepth)
	})

	t.Run("a lens the sweep has never reported on is left alone", func(t *testing.T) {
		// Every non-personal lens in the corpus is in this state. Writing
		// `stale` onto one would claim a standing-healer relationship it does
		// not have, which is worse than saying nothing.
		r := health.New(kv, "non-personal-rule")
		require.NoError(t, r.SetActive(ctx))
		require.NoError(t, r.SetPersonalSweepVerdict(ctx, health.PersonalSweepVerdictStale))

		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Empty(t, entry.PersonalSweepVerdict,
			"a lens with no sweep verdict has no healer to be silent")
	})

	t.Run("the sweep's own next pass takes the field back", func(t *testing.T) {
		// The two-writer ownership rule, stated as behaviour: this path writes
		// only `stale` and the sweep never does, so a recovered healer's verdict
		// is simply the later write and the field converges without either
		// writer knowing about the other.
		r := health.New(kv, "personal-recovering-rule")
		require.NoError(t, r.SetPersonalSweepProgress(ctx, "Kx3TmZpq7RvwNsY2Hc9L", time.Time{}, 0, "clean"))
		require.NoError(t, r.SetPersonalSweepVerdict(ctx, health.PersonalSweepVerdictStale))
		require.NoError(t, r.SetPersonalSweepProgress(ctx, "Wq7bNmXt4RkzPy2LcH8v", time.Time{}, 0, "clean"))

		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, "clean", entry.PersonalSweepVerdict)
	})
}

// TestInterestReconcilerCensus_CountsFromConstruction pins conjunct 2's fourth
// writer.
//
// The InterestReconciler's orphan reap WIDENS what personalinterest.IsRelevant
// admits for the identity whose registration it removed, and a personal lens
// reads that answer live — so it is as much an Interest Set writer as the
// control plane's three arms. cmd/refractor builds it inside the very activation
// arm that registers the first personal lens, so the licence cannot sample it at
// registration and reads this census live instead.
func TestInterestReconcilerCensus_CountsFromConstruction(t *testing.T) {
	before := health.InterestReconcilersWithoutSink()
	builtBefore := health.InterestReconcilersConstructed()

	// Built directly rather than through a fixture: this test touches only the
	// census and the accessor, never Run, so the constructor's own conn/kv are
	// not exercised and a fixture would only obscure which call is under test.
	r := health.NewInterestReconciler(nil, nil, "SYNC-census", nil, nil)
	assert.Equal(t, builtBefore+1, health.InterestReconcilersConstructed(),
		"the census must be REACHED — a zero unarmed count that is zero because nothing was recorded reads exactly like one that is zero because everything is armed")
	assert.Equal(t, before+1, health.InterestReconcilersWithoutSink(),
		"a reconciler is counted UNARMED from construction: one nobody ever hands a sink to would otherwise never reach the census at all, which is the one shape it exists to catch")
	assert.False(t, r.InterestChangeSinkInstalled())

	r.SetInterestChangeSink(func(string) {})
	assert.True(t, r.InterestChangeSinkInstalled())
	assert.Equal(t, before, health.InterestReconcilersWithoutSink(),
		"arming it clears the entry, so conjunct 2 stops refusing")

	// And back: a sink removed is a fourth writer gone silent again.
	r.SetInterestChangeSink(nil)
	assert.Equal(t, before+1, health.InterestReconcilersWithoutSink())
	r.SetInterestChangeSink(func(string) {})
}
