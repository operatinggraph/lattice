package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardedTruncAdapter is a minimal adapter that reports a configurable Guarded()
// state and records whether Truncate was called. It lets the rebuild force-rule
// be asserted without a real NATS bucket or supervisor.
type guardedTruncAdapter struct {
	guarded   bool
	truncated bool
}

func (a *guardedTruncAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (a *guardedTruncAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (a *guardedTruncAdapter) Probe(context.Context) error                          { return nil }
func (a *guardedTruncAdapter) Close() error                                         { return nil }
func (a *guardedTruncAdapter) Guarded() bool                                        { return a.guarded }
func (a *guardedTruncAdapter) Truncate(context.Context) error {
	a.truncated = true
	return nil
}

// rebuildTruncates reports whether a rebuild over ad would clear the target for
// the requested truncate — resolveTruncate's answer, which is the force rule
// itself. It is asked directly rather than through Rebuild because a rebuild
// refuses to truncate a target whose consumer the supervisor does not manage
// (a target you cannot reset is a target you must not clear), and a
// supervisor-less fixture is in exactly that state. That refusal has its own
// test below; the real truncate is exercised end-to-end against a live pipeline
// in cmd/refractor's taxonomy shrink coverage.
func rebuildTruncates(t *testing.T, ad *guardedTruncAdapter, truncate bool) bool {
	t.Helper()
	return resolveTruncate(ad, "rule-force-trunc", truncate)
}

// TestRebuild_UnmanagedConsumerRefusesBeforeTruncating pins the ordering the
// helper above works around, and the reason for it: a rebuild whose consumer the
// supervisor does not manage cannot reset the durable, so nothing will replay
// the rows a truncate removes. Discovering that AFTER the purge — which is what
// deciding it at the reset would mean — leaves the target empty with no path
// back. The live window is a lens deletion: pipelineDeleter.Delete removes the
// durable BEFORE it cancels the run context.
func TestRebuild_UnmanagedConsumerRefusesBeforeTruncating(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: true}
	p, err := New("rule-unmanaged-trunc", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)

	require.Error(t, p.Rebuild(context.Background(), true))
	assert.False(t, ad.truncated,
		"a rebuild that cannot reset the consumer must refuse before it clears the target")
}

// TestRebuild_GuardedBucketForcesTruncate asserts the force rule: a guarded
// adapter is truncated even when truncate=false is requested.
func TestRebuild_GuardedBucketForcesTruncate(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: true}
	assert.True(t, rebuildTruncates(t, ad, false),
		"a guarded bucket must force truncate even when truncate=false is requested")
}

// TestRebuild_UnguardedBucketHonorsRequest asserts the unchanged behavior: an
// unguarded adapter is NOT truncated when truncate=false is requested.
func TestRebuild_UnguardedBucketHonorsRequest(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: false}
	assert.False(t, rebuildTruncates(t, ad, false),
		"an unguarded bucket must honor truncate=false (no truncation)")
}

// TestRebuild_UnguardedBucketTruncatesWhenRequested asserts that an unguarded
// adapter still truncates when the operator explicitly requests truncate=true.
func TestRebuild_UnguardedBucketTruncatesWhenRequested(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: false}
	assert.True(t, rebuildTruncates(t, ad, true),
		"an unguarded bucket must truncate when truncate=true is explicitly requested")
}

// TestRebuildProgress_AdvancesOnlyWhenTheBacklogSHRINKS is what separates a
// rebuild that is draining from one that is wedged. "Last observed" is true of a
// wedged rebuild on every poll and would report it healthy forever; only "last
// went down" is evidence of progress.
func TestRebuildProgress_AdvancesOnlyWhenTheBacklogShrinks(t *testing.T) {
	p, err := New("rule-rebuild-progress", "nats_kv", "CORE", nil, nil, &guardedTruncAdapter{}, nil)
	require.NoError(t, err)

	// The first poll of a rebuild establishes the baseline: there is nothing to
	// have decreased from yet, so it counts as progress.
	p.recordRebuildProgress(500)
	outstanding, first := p.RebuildProgress()
	require.EqualValues(t, 500, outstanding)
	require.False(t, first.IsZero(), "the first poll baselines the clock")

	// A poll at the SAME count is the wedge: the count is fresh, the rebuild is
	// not moving.
	p.recordRebuildProgress(500)
	_, held := p.RebuildProgress()
	assert.Equal(t, first, held, "an unchanged backlog is not progress")

	// A backlog that GROWS is not progress either — a rebuild racing new writes
	// can legitimately grow, but an oscillating count must not keep resetting
	// the clock and mask a rebuild that never gets closer to done.
	p.recordRebuildProgress(600)
	_, grown := p.RebuildProgress()
	assert.Equal(t, first, grown, "a growing backlog does not reset the progress clock")

	// A strict decrease is progress.
	p.recordRebuildProgress(400)
	outstanding, drained := p.RebuildProgress()
	require.EqualValues(t, 400, outstanding)
	assert.True(t, drained.After(first), "a shrinking backlog advances the progress clock")
}

// TestRebuild_RebaselinesTheProgressClockOnItsOwnWindow: a finished rebuild's
// timestamp must not be inherited by the next one, or a fresh rebuild is judged
// on a clock it never started — and the fresh clock starts at the OPEN rather
// than at zero, because zero reads as "unknown, not wedged" and would leave a
// rebuild whose watcher only errors permanently unjudgeable.
func TestRebuild_RebaselinesTheProgressClockOnItsOwnWindow(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: true}
	p, err := New("rule-rebuild-progress-reset", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)

	p.recordRebuildProgress(10)
	_, before := p.RebuildProgress()
	require.False(t, before.IsZero())

	require.Error(t, p.Rebuild(context.Background(), false)) // no supervisor
	outstanding, after := p.RebuildProgress()

	assert.Zero(t, outstanding, "the previous rebuild's backlog is not this one's")
	assert.True(t, after.After(before), "the clock restarts at this rebuild's own window")
}

// TestBeginRebuild_StampsTheProgressClockSoASilentWindowIsJudgeable is the same
// invariant at the seam that matters: the window a personal lens is silent
// through is opened here, and the completion watcher records NO progress on a
// poll that errors — deliberately, since that retry is unbounded. With a zero
// timestamp health.evalRebuildWedged reads the whole window as unknown, so a
// watcher erroring forever would keep the lens silent with nothing surfacing it.
func TestBeginRebuild_StampsTheProgressClockSoASilentWindowIsJudgeable(t *testing.T) {
	p, err := New("rule-rebuild-window-clock", "nats_kv", "CORE", nil, nil, &guardedTruncAdapter{}, nil)
	require.NoError(t, err)
	_, unstarted := p.RebuildProgress()
	require.True(t, unstarted.IsZero(), "no rebuild has started, so there is nothing to judge")

	opened := time.Now()
	sig := p.beginRebuild()

	outstanding, at := p.RebuildProgress()
	assert.Zero(t, outstanding, "no poll has observed a count yet")
	require.False(t, at.IsZero(), "a started rebuild is never unknown")
	assert.False(t, at.Before(opened.Add(-time.Second)), "the clock is this window's own")

	p.endRebuild(sig)
}
