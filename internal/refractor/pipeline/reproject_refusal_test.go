package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReproject_IndexBehindRefusesTheRetraction is T11: the retraction arm's
// precondition that the ordering token cannot supply.
//
// A Reproject concludes "the actor no longer holds this anchor" from an EDGE
// view the refractor-adjacency index serves, and that index is a separately
// cursored consumer that can sit arbitrarily far behind the lens. When it has
// not applied everything up to the token this call would write under, an edge
// it reports absent may already be live in Core KV — so the arm refuses rather
// than tombstoning a live grant, and the sweep's next pass retries.
//
// The table runs the cursor across the boundary in both directions, plus the
// two unknown readings (no source wired; a process that has applied and polled
// nothing), which refuse with the rest.
func TestReproject_IndexBehindRefusesTheRetraction(t *testing.T) {
	const token = 4242

	cases := []struct {
		name        string
		cursor      func() uint64
		wantWritten bool
	}{{
		name:        "cursor behind the token refuses",
		cursor:      func() uint64 { return token - 1 },
		wantWritten: false,
	}, {
		name:        "cursor level with the token writes",
		cursor:      func() uint64 { return token },
		wantWritten: true,
	}, {
		name:        "cursor ahead of the token writes",
		cursor:      func() uint64 { return token + 100 },
		wantWritten: true,
	}, {
		name:        "no cursor source wired refuses",
		cursor:      nil,
		wantWritten: false,
	}, {
		name:        "a cursor of zero refuses — it is the never-measured reading",
		cursor:      func() uint64 { return 0 },
		wantWritten: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
			p := newReprojectPipeline(t, adpt)
			p.SetAdjacencyAppliedFn(tc.cursor)
			p.recordAppliedSeq(token)

			res, err := p.Reproject(context.Background(), reprojectActor)
			require.NoError(t, err)

			if tc.wantWritten {
				require.True(t, res.Deleted)
				require.Len(t, adpt.deletes, 1)
				require.Equal(t, uint64(token), adpt.deletes[0].seq)
				return
			}
			require.Empty(t, adpt.deletes, "a refused retraction writes nothing at all")
			require.False(t, res.Deleted)
			require.False(t, res.Wrote)
			require.Equal(t, VerdictBlocked, res.Verdict)
			require.Equal(t, BlockedUnknown, res.BlockedClass,
				"no stored watermark was consulted, so the class cannot be proven")
			require.Equal(t, adjacencyBehindReason, res.VerdictReason)
		})
	}
}

// TestReproject_IndexBehindLeavesTheUpsertArmAlone pins the refusal's SCOPE.
// The precondition is about a conclusion drawn from an absent edge — a
// retraction — and an upsert draws no such conclusion: it recomputes a body
// from edges it DID read. A refusal that spread to the upsert arm would stop
// the sweep healing a missing row for as long as the index lagged, which is
// exactly when a row is most likely to be missing.
func TestReproject_IndexBehindLeavesTheUpsertArmAlone(t *testing.T) {
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	installProjectingRule(t, p)
	writeProjectableAnchor(t, p, sweepActorA) // a live anchor ⇒ an upsert, not a retraction
	p.SetAdjacencyAppliedFn(func() uint64 { return 1 })
	p.recordAppliedSeq(4242)

	res, err := p.Reproject(context.Background(), sweepActorA)
	require.NoError(t, err)
	require.True(t, res.Wrote, "an upsert is not gated on the adjacency cursor")
	require.Len(t, adpt.upserts, 1)
	require.Equal(t, uint64(4242), adpt.upserts[0].seq)
}

// TestReproject_RebuildMovedAbandonsEveryRemainingWrite is T12: a
// reconciliation pass a rebuild opened under abandons its writes.
//
// A rescan truncates the lens's keys and replays them from the stream, so every
// write this call still holds describes a target that no longer exists and
// carries a token above the replay's first writes. The sweep already refuses to
// START a pass during a rebuild; this closes the pass that was already running.
//
// The two conditions are asserted separately because each catches an
// interleaving the other misses: a rebuild still IN FLIGHT, and one that opened
// and already finished (the generation moved, the count is back at zero).
func TestReproject_RebuildMovedAbandonsEveryRemainingWrite(t *testing.T) {
	t.Run("a rebuild still in flight abandons", func(t *testing.T) {
		adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
		p := newReprojectPipeline(t, adpt)
		p.SetAdjacencyAppliedFn(func() uint64 { return 9999 })
		p.recordAppliedSeq(4242)
		p.rebuildWatchMu.Lock()
		p.openRebuildWindowLocked()
		p.rebuildWatchMu.Unlock()

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)
		require.Empty(t, adpt.deletes, "no write survives a rebuild opened under the pass")
		require.Equal(t, VerdictBlocked, res.Verdict)
		require.Equal(t, rebuildMovedReason, res.VerdictReason)
	})

	t.Run("a rebuild that opened and closed under the pass abandons", func(t *testing.T) {
		adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
		p := newReprojectPipeline(t, adpt)
		p.SetAdjacencyAppliedFn(func() uint64 { return 9999 })
		p.recordAppliedSeq(4242)

		// The rebuild opens and ends entirely between this call's token
		// capture and its write phase, so RebuildInFlight is false again by
		// the time the loop asks — the generation is the only witness left.
		var once sync.Once
		adpt.onGetRow = func() {
			once.Do(func() {
				p.rebuildWatchMu.Lock()
				sig := p.openRebuildWindowLocked()
				p.rebuildWatchMu.Unlock()
				p.endRebuild(sig)
			})
		}

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)
		require.False(t, p.RebuildInFlight(), "the rebuild is over; only the generation records it happened")
		require.Empty(t, adpt.deletes)
		require.Equal(t, VerdictBlocked, res.Verdict)
		require.Equal(t, rebuildMovedReason, res.VerdictReason)
	})

	t.Run("no rebuild writes", func(t *testing.T) {
		adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
		p := newReprojectPipeline(t, adpt)
		p.SetAdjacencyAppliedFn(func() uint64 { return 9999 })
		p.recordAppliedSeq(4242)

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)
		require.True(t, res.Deleted, "the positive vector: the same call at an unmoved generation writes")
		require.Len(t, adpt.deletes, 1)
	})
}

// TestRebuildGeneration_RaisesWithEveryOpenedWindow pins the counter itself:
// it moves once per opened window, in lockstep with the window count, and never
// falls back when a window closes — a caller holding an old generation must be
// able to see that a rebuild happened after the rescan has finished.
func TestRebuildGeneration_RaisesWithEveryOpenedWindow(t *testing.T) {
	p := &Pipeline{ruleID: "gen"}
	require.Equal(t, uint64(0), p.rebuildGeneration())

	p.rebuildWatchMu.Lock()
	first := p.openRebuildWindowLocked()
	p.rebuildWatchMu.Unlock()
	require.Equal(t, uint64(1), p.rebuildGeneration())
	require.True(t, p.RebuildInFlight())

	p.endRebuild(first)
	require.False(t, p.RebuildInFlight())
	require.Equal(t, uint64(1), p.rebuildGeneration(), "the generation never falls back")

	p.rebuildWatchMu.Lock()
	p.openRebuildWindowLocked()
	p.rebuildWatchMu.Unlock()
	require.Equal(t, uint64(2), p.rebuildGeneration())
}
