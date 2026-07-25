package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A reconciliation write carries the pipeline's last-applied stream sequence,
// which is PER-PROCESS state that starts at zero. Until the consumer acks
// anything, that token loses to every stored watermark under the §6.2 guard —
// so a write over an existing row is dropped by the guard while the caller
// reads it as a heal. Caught live: the sweep re-healed the same two actors
// every tick, each write silently rejected, with the divergence issue held open
// on a repair that never landed.

func TestReproject_RefusesToOverwriteWithoutAnOrderingToken(t *testing.T) {
	// A stored row that differs from the recomputed one, at token zero.
	adpt := &recordingAdapter{present: true, stored: map[string]any{"key": "cap.identity.x", "roles": []any{"vtx.role.a"}}}
	p := newReprojectPipeline(t, adpt)
	// The missing-actor branch yields a Delete over a present row, which is
	// equally unable to outrank the stored watermark.
	_, err := p.Reproject(context.Background(), reprojectActor)
	require.ErrorIs(t, err, ErrNoOrderingToken)
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes,
		"a write the guard would reject must not be issued, or the sweep churns forever")
}

func TestReproject_CreatesAnAbsentRowWithoutAnOrderingToken(t *testing.T) {
	// The lost-first-projection case must still heal from a cold pipeline: an
	// absent key takes the guard's Create branch, which has no stored watermark
	// to lose to. Refusing here would disable the reconciliation this design
	// exists for exactly when it is needed most — right after a restart.
	adpt := &recordingAdapter{present: false}
	p := newReprojectPipeline(t, adpt)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.True(t, res.Converged, "an already-absent row is converged, not a write")
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
}

func TestSweepPass_AbandonsThePassWhenTheTokenIsUnusable(t *testing.T) {
	// The refusal is per-pipeline, so the sweep must stop the pass rather than
	// grind through the batch logging one refusal per actor.
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	adpt.present = true
	adpt.stored = map[string]any{"key": orphan}
	p := newSweepPipeline(t, adpt, 10)
	// lastAppliedSeq deliberately left at zero.

	p.Sweeper().pass(context.Background())

	require.Empty(t, adpt.deletes)
	require.Empty(t, adpt.upserts)
	st := p.Sweeper().Status()
	require.Zero(t, st.Reconciled, "a write the guard rejects is not a heal and must never be counted")
	require.Zero(t, st.DivergentStreak,
		"an unrepairable pass must not hold CapabilityCoverageDivergence open")
	require.Equal(t, 1, st.FailedStreak,
		"a pass that verified nothing before abandoning is not a converged pass either")
	require.Zero(t, st.FailingActors, "a pass-level fault names no actor")
}
