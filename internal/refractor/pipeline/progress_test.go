package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/pipeline"
)

// TestPipeline_Progress_TracksAppliedSeqAndProjectedAt verifies the two
// projection-liveness clocks (lens-projection-liveness-design.md §3.1):
// lastAppliedSeq advances on every acked event, including ack-and-skip (an
// edge-shaped payload the pipeline acks without ever calling evaluate), while
// lastProjectedAt advances only when a real adapter write reaches the target —
// staying frozen through the ack-and-skip.
func TestPipeline_Progress_TracksAppliedSeqAndProjectedAt(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	targetKV, adpt := newTargetKV(t, env, "target-progress", []string{"agreement_id"})

	p, err := pipeline.New("rule-progress", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-progress")

	initial := p.Progress()
	assert.Equal(t, uint64(0), initial.LastAppliedSeq, "no event processed yet")
	assert.True(t, initial.LastProjectedAt.IsZero(), "no projection yet")

	// Edge-shaped payload (carries nodeId): the pipeline acks-and-skips it before
	// ever reaching evaluate/write — advances the applied cursor, not the
	// projection clock.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementA1, map[string]any{"nodeId": "edge-1"})
	pollUntil(t, 2*time.Second, func() bool {
		return p.Progress().LastAppliedSeq > 0
	})
	afterSkip := p.Progress()
	assert.True(t, afterSkip.LastProjectedAt.IsZero(), "ack-and-skip must not advance lastProjectedAt")

	// A real vertex event: matches the plan and writes through the adapter —
	// advances both clocks.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementA2, map[string]any{"id": "a2", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), "a2")
		return err == nil
	})
	pollUntil(t, 2*time.Second, func() bool {
		return !p.Progress().LastProjectedAt.IsZero()
	})
	afterProject := p.Progress()
	assert.Greater(t, afterProject.LastAppliedSeq, afterSkip.LastAppliedSeq,
		"the second event must advance the applied cursor further")
	assert.False(t, afterProject.LastProjectedAt.IsZero())
}

// TestPipeline_Progress_FrozenOnWriteError verifies that a Nak'd (redelivered)
// message never advances either progress clock — the message has not actually
// been consumed, so neither "applied" nor "projected" may advance.
func TestPipeline_Progress_FrozenOnWriteError(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ea := &errAdapter{}
	p, err := pipeline.New("rule-progress-err", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, ea, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-progress-err")

	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementErr1, map[string]any{"id": "err1", "isDeleted": false})

	// Wait for at least one redelivery (proves the Nak path is exercised).
	pollUntil(t, 5*time.Second, func() bool {
		return ea.calls.Load() > 1
	})

	progress := p.Progress()
	assert.Equal(t, uint64(0), progress.LastAppliedSeq, "a Nak'd message must never advance lastAppliedSeq")
	assert.True(t, progress.LastProjectedAt.IsZero(), "a failed write must never advance lastProjectedAt")
}
