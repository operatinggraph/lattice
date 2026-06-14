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
	p, err := New("rule-force-trunc", "nats_kv", nil, "CORE", nil, nil, ad, nil)
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
