package pipeline

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
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

func TestReproject_AnAlreadyAbsentRowIsConvergedNotAWrite(t *testing.T) {
	// The missing-actor branch over a row that is already gone: nothing to
	// retract, so the token is never needed and no write is issued.
	adpt := &recordingAdapter{present: false}
	p := newReprojectPipeline(t, adpt)

	res, err := p.Reproject(context.Background(), reprojectActor)
	require.NoError(t, err)
	require.True(t, res.Converged, "an already-absent row is converged, not a write")
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
}

// installProjectingRule gives the pipeline a rule that binds any live identity
// anchor, so a seeded anchor yields an UPSERT-shaped result rather than the
// missing-actor retraction every other token test exercises.
func installProjectingRule(t *testing.T, p *Pipeline) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse("MATCH (i:identity {key: $actorKey}) RETURN i.key AS actorKey")
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"actorKey"}
	require.NoError(t, fullCR.ValidateKeyColumns())
	p.fullEngine, p.fullCR = eng, fullCR
}

func TestReproject_RefusesToCreateAnAbsentRowWithoutAnOrderingToken(t *testing.T) {
	// The guard drops a token-less write BEFORE it looks for a stored
	// watermark, so an absent row is no more writable than a present one. Left
	// unrefused, the adapter returns nil having written nothing and the caller
	// books a heal that did not happen — inflating Reconciled, logging a repair,
	// and scoring a phantom hit into the prefilter hints' earned share.
	//
	// The refusal is scoped to the block an own-row-reading (NATS-KV) adapter
	// enters, because a SQL-guarded target conditions only its UPDATE branch
	// and its absent-row insert really does land at token zero.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	installProjectingRule(t, p)
	writeProjectableAnchor(t, p, sweepActorA) // live anchor ⇒ an upsert, not a retraction
	// lastAppliedSeq deliberately left at zero.

	_, err := p.Reproject(context.Background(), sweepActorA)
	require.ErrorIs(t, err, ErrNoOrderingToken)
	require.Empty(t, adpt.upserts,
		"a write the guard drops must not be issued")
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
