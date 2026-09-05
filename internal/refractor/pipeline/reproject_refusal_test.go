package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReproject_IndexBehindRefusesAnEdgeDerivedRetraction is T11: the
// retraction arm's precondition, and the exact shape it applies to.
//
// A Reproject concludes "the actor no longer holds this anchor" by subtracting
// the executor's walk from the stored key set, and that walk reads its edges
// from the refractor-adjacency index — a separately cursored consumer that can
// sit arbitrarily far behind the lens. When it has not applied everything up to
// the token this call would write under, an edge it reports absent may already
// be live in Core KV, so the arm refuses rather than tombstoning a live grant.
//
// The table runs the cursor across the boundary in both directions, plus the
// two unknown readings (no source wired; a process that has retired and polled
// nothing), which refuse with the rest.
func TestReproject_IndexBehindRefusesAnEdgeDerivedRetraction(t *testing.T) {
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
			ctx := context.Background()
			p, adpt, _ := newOrderingFixture(t, "index-behind")
			// The entry is stored and the index holds no edge for it, so the
			// prefix diff derives a dropped-anchor tombstone — the one result
			// shape whose absence verdict came from the index.
			require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
				map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
			p.SetAdjacencyAppliedFn(tc.cursor)
			p.recordAppliedSeq(token)

			res, err := p.Reproject(ctx, orderingActor)
			require.NoError(t, err)

			if tc.wantWritten {
				require.True(t, res.Deleted)
				require.False(t, entryIsLive(t, adpt))
				return
			}
			require.True(t, entryIsLive(t, adpt), "a refused retraction writes nothing at all")
			require.False(t, res.Deleted)
			require.False(t, res.Wrote)
			require.Equal(t, VerdictBlocked, res.Verdict)
			require.Equal(t, BlockedUnknown, res.BlockedClass,
				"no stored watermark was consulted, so the class cannot be proven")
			require.Equal(t, adjacencyBehindReason, res.VerdictReason)
		})
	}
}

// TestReproject_IndexBehindSparesEveryRetractionTheIndexDidNotDecide pins the
// refusal's SCOPE from the other side, with a cursor of 0 — the reading that
// refuses everything the refusal applies to.
//
// Two retractions reach the same arm with an absence no index lag can distort,
// and both must land or the reconciler stops healing the cases it exists for:
//
//   - a DOC-MODE actor-aggregate lens (cap.roles, cap.svc, cap.ephemeral,
//     my-tasks) has no edge-derived prefix diff at all — its delete is the one
//     key it owns for an actor that fetchVertexProps read absent.
//   - a perEntry MISSING-ACTOR retraction, whose absence verdict is the same
//     live Core KV read of the actor vertex; the whole prefix is retracted
//     because the actor is gone, not because an edge was.
//
// Neither ever lost the incidental fence withholding removes, so neither is
// what refusal 1 replaces.
func TestReproject_IndexBehindSparesEveryRetractionTheIndexDidNotDecide(t *testing.T) {
	t.Run("a doc-mode actor-aggregate delete lands", func(t *testing.T) {
		adpt := &recordingAdapter{stored: map[string]any{"key": "cap.identity.x"}, present: true}
		p := newReprojectPipeline(t, adpt)
		// No SetAdjacencyAppliedFn at all: the cursor reads 0, the refusing
		// value for anything the refusal governs.
		p.recordAppliedSeq(4242)

		res, err := p.Reproject(context.Background(), reprojectActor)
		require.NoError(t, err)
		require.True(t, res.Deleted, "a doc-mode lens has no index-derived absence to refuse on")
		require.Len(t, adpt.deletes, 1)
		require.Equal(t, uint64(4242), adpt.deletes[0].seq)
	})

	t.Run("a perEntry missing-actor retraction lands", func(t *testing.T) {
		ctx := context.Background()
		p, adpt, _ := newOrderingFixture(t, "missing-actor")
		require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
			map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
		// The actor vertex is gone from Core KV, so fetchVertexProps reads it
		// absent — a live read — and the whole prefix is retracted.
		require.NoError(t, p.coreKV.Delete(ctx, orderingActor))
		p.SetAdjacencyAppliedFn(func() uint64 { return 0 })
		p.recordAppliedSeq(4242)

		res, err := p.Reproject(ctx, orderingActor)
		require.NoError(t, err)
		require.True(t, res.Deleted, "an absent actor vertex is a live Core KV fact, not an index one")
		require.False(t, entryIsLive(t, adpt))
	})
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
		ctx := context.Background()
		p, adpt, _ := newOrderingFixture(t, "rebuild-moved-gen")
		require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
			map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
		p.recordAppliedSeq(4242)

		// The rebuild opens and ends entirely between this call's generation
		// capture (at entry) and its write phase, driven from inside the
		// evaluation's own prefix listing — so RebuildInFlight is false again
		// by the time the write loop asks, and the generation is the only
		// witness that a rescan happened at all.
		var once sync.Once
		adpt.mu.Lock()
		adpt.onListKeysPrefix = func() {
			once.Do(func() {
				p.rebuildWatchMu.Lock()
				sig := p.openRebuildWindowLocked()
				p.rebuildWatchMu.Unlock()
				p.endRebuild(sig)
			})
		}
		adpt.mu.Unlock()

		res, err := p.Reproject(ctx, orderingActor)
		require.NoError(t, err)
		require.False(t, p.RebuildInFlight(), "the rebuild is over; only the generation records it happened")
		require.True(t, entryIsLive(t, adpt), "no write survives a rebuild that opened under the pass")
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

// TestReproject_AConvergedVerdictAlsoAbandonsUnderARebuild pins the ORDER of
// the two checks the reconciliation loop makes per result, which is item 4's
// whole subject: the withholding verdict is folded BELOW the abandon check, not
// above it.
//
// An Unchanged verdict says "the target already holds this body". A rebuild
// that opened under the pass has TRUNCATED that target, so the verdict is about
// keys that are gone until the replay reaches them. Folded above the abandon
// check it would report the actor converged — telling the sweep the rows are
// correct at the one moment they are not there at all — and it would count a
// withhold that saved no write.
//
// The positive vector runs first: the same call, with no rebuild, does fold
// Converged and does count the withhold.
func TestReproject_AConvergedVerdictAlsoAbandonsUnderARebuild(t *testing.T) {
	ctx := context.Background()

	// The store holds exactly what the walk derives, so the evaluation reaches
	// an Unchanged verdict for the entry.
	seed := func(t *testing.T, ruleID string) (*Pipeline, *recordingEntryAdapter) {
		t.Helper()
		p, adpt, _ := newOrderingFixture(t, ruleID)
		grantTheRole(t, p)
		require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
			map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
		p.recordAppliedSeq(4242)
		return p, adpt
	}

	t.Run("no rebuild: the verdict folds converged and counts", func(t *testing.T) {
		p, _ := seed(t, "converged-control")
		before := p.EntriesWithheld()

		res, err := p.Reproject(ctx, orderingActor)
		require.NoError(t, err)
		require.Equal(t, VerdictConverged, res.Verdict)
		require.True(t, res.Converged)
		require.False(t, res.Wrote)
		require.Equal(t, before+1, p.EntriesWithheld(),
			"a withhold reached through the reconciler is a withhold, and is counted like one")
	})

	t.Run("a rebuild in flight: blocked, not converged, and counted as neither", func(t *testing.T) {
		p, _ := seed(t, "converged-abandoned")
		p.rebuildWatchMu.Lock()
		p.openRebuildWindowLocked()
		p.rebuildWatchMu.Unlock()
		before := p.EntriesWithheld()

		res, err := p.Reproject(ctx, orderingActor)
		require.NoError(t, err)
		require.Equal(t, VerdictBlocked, res.Verdict,
			"the target was truncated under this pass, so its rows are not verified — they are gone")
		require.Equal(t, rebuildMovedReason, res.VerdictReason)
		require.False(t, res.Converged)
		require.Equal(t, before, p.EntriesWithheld(),
			"an abandoned result saved no write, so it is not a withhold")
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

// TestRebuildGeneration_ReactivationTruncateRaisesItToo pins the one purge that
// opens no window. TruncateForReactivation clears the lens's keys on the seam
// between a stopped pipeline and the same lens ID activating again, and it does
// so without a rebuild window — so RebuildInFlight never goes true and a
// reconciliation pass holding a token captured before the purge has no other
// way to learn that the keys it is reasoning about are gone. The generation is
// that signal, and a purge that did not raise it would leave the pass writing
// against a target it has no evidence about.
func TestRebuildGeneration_ReactivationTruncateRaisesItToo(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newOrderingFixture(t, "reactivation-truncate")
	adpt.SetKeyPrefix("child.")
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
		map[string]any{"key": orderingEntry, "role": orderingRole}, 1))

	before := p.rebuildGeneration()
	purged, err := p.TruncateForReactivation(ctx, true)
	require.NoError(t, err)
	require.True(t, purged, "the fixture must actually purge, or the assertion below proves nothing")
	require.False(t, entryIsLive(t, adpt), "the keys really are gone")

	require.Equal(t, before+1, p.rebuildGeneration(),
		"a purge with no window of its own must still move the generation")
	require.False(t, p.RebuildInFlight(),
		"and it opens no window, which is exactly why the generation has to carry it")
}
