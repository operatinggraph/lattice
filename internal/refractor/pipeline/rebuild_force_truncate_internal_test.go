package pipeline

import (
	"context"
	"testing"

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

// rebuildTruncates constructs a pipeline over ad and calls Rebuild(truncate).
// Rebuild's truncate branch runs before the supervisor reset, so it returns the
// no-supervisor error after the truncate decision — exactly the window this test
// inspects. Returns whether Truncate was invoked.
func rebuildTruncates(t *testing.T, ad *guardedTruncAdapter, truncate bool) bool {
	t.Helper()
	p, err := New("rule-force-trunc", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)
	// No supervisor configured: Rebuild errors after the truncate branch.
	require.Error(t, p.Rebuild(context.Background(), truncate))
	return ad.truncated
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

// TestRebuild_ClearsThePreviousRebuildsProgress: a finished rebuild's timestamp
// must not be inherited by the next one, or a fresh rebuild is judged on a clock
// it never started.
func TestRebuild_ClearsThePreviousRebuildsProgress(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: true}
	p, err := New("rule-rebuild-progress-reset", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)

	p.recordRebuildProgress(10)
	_, before := p.RebuildProgress()
	require.False(t, before.IsZero())

	require.Error(t, p.Rebuild(context.Background(), false)) // no supervisor
	outstanding, after := p.RebuildProgress()

	assert.Zero(t, outstanding)
	assert.True(t, after.IsZero(), "a new rebuild starts with no progress record of its own")
}
