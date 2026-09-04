package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestReprojectPersonalActor_CapturesTheRevisionAfterTheReprojection is the
// server half of T5 (personal-lens-grant-change-trigger-design.md §10).
//
// It pins the one deliberate divergence from Hydrate, and it pins it against
// the condition that makes the divergence matter: a concurrent CDC evaluation
// acking WHILE this reprojection runs. That is not a contrived window. The
// pipeline's forward cursor advances only on ack, so an in-flight evaluation is
// invisible to a snapshot taken before it acks — and the interval between an
// evaluation starting and acking is stretched well past one evaluation by the
// guarded adapter's CAS retry loop (up to 8 attempts) or a retry-queue backoff.
// T5's own brief calls for the assertion under contention rather than on the
// fast path, which is what the injected mid-evaluation ack models.
//
// Capture-BEFORE (Hydrate's posture) under-claims. For a bulk cold snapshot
// that is right: the worst case is a row that arrives again anyway. For a
// retraction it is fatal, and the client-side half of T5 shows exactly how.
func TestReprojectPersonalActor_CapturesTheRevisionAfterTheReprojection(t *testing.T) {
	const (
		before = uint64(100)
		after  = uint64(140)
	)

	newPipe := func(t *testing.T) (*Pipeline, *fakePersonalTarget) {
		t.Helper()
		target := &fakePersonalTarget{}
		p, coreKV := newPersonalTestPipeline(t, target)
		putPersonalVertex(t, coreKV, substrate.VertexKey("identity", personalActorA), "identity", nil)
		// A self-anchored cypher, so the evaluation produces a row (and runs
		// the envelope) from the identity alone — the capture points are what
		// is under test here, not the traversal.
		cr, err := full.New().Parse(`
MATCH (identity {key: $actorKey})
RETURN identity.key AS anchor, "identity" AS kind, identity.key AS entityId
`)
		require.NoError(t, err)
		selfCR, ok := cr.(*full.CompiledRule)
		require.True(t, ok)
		selfCR.KeyColumns = []string{"entityId"}
		require.NoError(t, selfCR.ValidateKeyColumns())
		p.fullCR = selfCR
		p.recordAppliedSeq(before)

		// A concurrent CDC evaluation acks mid-reprojection. The envelope runs
		// during the evaluation, so hooking it here places the advance exactly
		// between the two candidate capture points and nowhere else.
		inner := p.envelopeFn
		p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
			p.recordAppliedSeq(after)
			return inner(row, keys, params)
		})
		return p, target
	}

	t.Run("the reprojection frame claims the post-evaluation revision", func(t *testing.T) {
		p, target := newPipe(t)

		require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeAll()))

		frames := target.snapshot()
		require.Len(t, frames, 1)
		assert.Equal(t, after, frames[0].revision,
			"a retraction frame that under-claims its revision cannot retract — the client either drops it whole or exempts the very key it names")
	})

	t.Run("Hydrate still claims the pre-evaluation revision", func(t *testing.T) {
		p, target := newPipe(t)

		hw, err := p.Hydrate(context.Background(), personalActorA)
		require.NoError(t, err)

		assert.Equal(t, before, hw, "Hydrate keeps capture-before: a cold snapshot must never regress a fresher live delta")
		frames := target.snapshot()
		require.Len(t, frames, 1)
		assert.Equal(t, before, frames[0].revision)
	})
}
