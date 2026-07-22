package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// TestPipeline_Progress_TracksAppliedSeqAndProjectedAt verifies the two
// projection-liveness clocks (lens-projection-liveness-design.md §3.1):
// lastAppliedSeq advances on every acked event, including ack-and-skip (an
// edge-shaped payload the pipeline acks without ever calling evaluate), while
// lastProjectedAt advances only when a real adapter write reaches the target —
// staying frozen through the ack-and-skip.
func TestPipeline_Progress_TracksAppliedSeqAndProjectedAt(t *testing.T) {
	env := startPipelineEnv(t)

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	targetKV, adpt := newTargetKV(t, env, "target-progress", []string{"agreement_id"})

	p, err := pipeline.New("rule-progress", "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
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

// TestPipeline_Progress_SeedsFromDurableAckFloorOnRestart proves the
// restart-inert residual (capability-projection-reconciliation-design.md
// §3.4) is fixed: a fresh Pipeline instance bound to a durable that already
// has ack history (simulating a process restart against the same durable
// name) holds a NONZERO lastAppliedSeq immediately on Run — before it has
// applied any event itself. Before this fix, lastAppliedSeq was purely
// in-process state that always restarted at zero, so a reconciliation write
// over an existing row kept hitting ErrNoOrderingToken until new CDC traffic
// happened to arrive on that lens.
func TestPipeline_Progress_SeedsFromDurableAckFloorOnRestart(t *testing.T) {
	env := startPipelineEnv(t)
	const ruleID = "rule-restart-seed"

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	targetKV, adpt := newTargetKV(t, env, "target-restart-seed", []string{"agreement_id"})

	// First "process": run the pipeline, apply one real event, then stop it —
	// the durable consumer (named identically by specFor(ruleID)) survives the
	// Stop, per Run's own doctrine ("Stop the pump without deleting the
	// durable — its persisted position is the point of durability").
	p1, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p1.UseFullEngine(eng, cr)
	p1.RunOn(env.conn, specFor(ruleID))

	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() {
		p1.Run(ctx1)
		close(done1)
	}()

	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRi1, map[string]any{"id": "ri1", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), "ri1")
		return err == nil
	})
	seededAt := p1.Progress().LastAppliedSeq
	require.Greater(t, seededAt, uint64(0), "precondition: the first process must have applied at least one event")

	cancel1()
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("p1.Run did not return after cancel")
	}

	// Second "process": a brand-new Pipeline, never having applied anything
	// itself (lastAppliedSeq starts at its zero value), bound to the SAME
	// durable name. Its Run must seed lastAppliedSeq from the durable's
	// persisted ack floor before any new event arrives.
	p2, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p2.UseFullEngine(eng, cr)
	startPipeline(t, env, p2, ruleID)

	pollUntil(t, 2*time.Second, func() bool {
		return p2.Progress().LastAppliedSeq > 0
	})
	assert.GreaterOrEqual(t, p2.Progress().LastAppliedSeq, seededAt,
		"a restarted pipeline must seed lastAppliedSeq from the durable's persisted ack floor, not start cold at zero")
}

// TestPipeline_Progress_FrozenOnWriteError verifies that a Nak'd (redelivered)
// message never advances either progress clock — the message has not actually
// been consumed, so neither "applied" nor "projected" may advance.
func TestPipeline_Progress_FrozenOnWriteError(t *testing.T) {
	env := startPipelineEnv(t)

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ea := &errAdapter{}
	p, err := pipeline.New("rule-progress-err", "nats_kv", coreKVBucket, env.adjKV, env.coreKV, ea, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
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
