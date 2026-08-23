package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

const sentinelAgreementRtry2 = "TsntWagreementRtry22"

// failOnceAdapter fails its first upsert transiently, then delegates every
// later call to a real NatsKVAdapter — the shape a target that blips and
// recovers presents, and the only shape that reaches the retry queue's WriteFn
// with a write that can actually land.
//
// The adapter is a NAMED FIELD, forwarded explicitly, for the reason
// adapter.OutcomeUpserter documents: embedding would promote UpsertWithOutcome
// past the injected failure, and UpsertWithOutcome is the call the pipeline
// makes.
type failOnceAdapter struct {
	inner *adapter.NatsKVAdapter
	calls atomic.Int64
}

var (
	_ adapter.Adapter         = (*failOnceAdapter)(nil)
	_ adapter.OutcomeUpserter = (*failOnceAdapter)(nil)
)

func (a *failOnceAdapter) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	_, err := a.UpsertWithOutcome(ctx, keys, row, seq)
	return err
}

func (a *failOnceAdapter) UpsertWithOutcome(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	if a.calls.Add(1) == 1 {
		// A bare error classifies as transient, which is what routes the result
		// to the retry queue rather than the DLQ.
		return adapter.UpsertOutcome{}, errors.New("injected transient write blip")
	}
	return a.inner.UpsertWithOutcome(ctx, keys, row, seq)
}

func (a *failOnceAdapter) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return a.inner.Delete(ctx, keys, seq)
}
func (a *failOnceAdapter) Probe(ctx context.Context) error { return a.inner.Probe(ctx) }
func (a *failOnceAdapter) Close() error                    { return a.inner.Close() }

// TestPipeline_RetriedWriteThatLands_PublishesItsAuditEntry covers the write
// path that reaches the target without passing through writeResults' own audit
// step: a transient failure hands the result to the retry queue, and the replay
// there is what finally stores the row.
//
// The first attempt errors, so it audits nothing — correctly, since nothing was
// written. If the replay audited nothing either, a row that IS in the target
// would have no entry anywhere, and the trail would be silently missing exactly
// the writes that had trouble landing.
func TestPipeline_RetriedWriteThatLands_PublishesItsAuditEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)
	ctx := context.Background()

	const ruleID = "audit-retry-rule"
	eng, cr := compileFullRule(t,
		"MATCH (n:agreement {key: $actorKey}) RETURN n.id AS agreement_id, n.status AS status",
		[]string{"agreement_id"})
	targetKV, inner := newTargetKV(t, env, "TARGET-AUDIT-RETRY", []string{"agreement_id"})
	adpt := &failOnceAdapter{inner: inner}

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket,
		env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	rq := failure.NewRetryQueue()
	p.SetRetryQueue(rq, env.conn, 3, time.Millisecond)
	rctx, cancelRetry := context.WithCancel(ctx)
	t.Cleanup(cancelRetry)
	go rq.Run(rctx)

	require.NoError(t, health.EnsureAuditStream(ctx, env.conn))
	p.SetAuditWriter(health.NewAuditWriter(env.conn, ruleID))

	startPipeline(t, env, p, ruleID)

	auditCons, err := env.js.CreateOrUpdateConsumer(ctx, health.AuditStreamName, jetstream.ConsumerConfig{
		Name:          "pipeline-audit-retry-consumer",
		FilterSubject: subjects.Audit(ruleID),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRtry2,
		map[string]any{"id": "retried1", "status": "open"})

	// The row reaching the target is the retry's replay landing — the first
	// attempt never got that far.
	pollUntil(t, 5*time.Second, func() bool {
		_, gerr := targetKV.Get(ctx, "retried1")
		return gerr == nil
	})
	require.GreaterOrEqual(t, adpt.calls.Load(), int64(2),
		"the row can only be there because a second, replayed write stored it")

	msg, err := auditCons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err, "a replayed write that landed belongs in the trail like any other")
	var entry health.AuditEntry
	require.NoError(t, json.Unmarshal(msg.Data(), &entry))
	assert.Equal(t, "vtx.agreement."+sentinelAgreementRtry2, entry.EntityID)
	assert.Equal(t, "upsert", entry.Operation)
	assert.NotEmpty(t, entry.OutputRowHash)

	_, err = auditCons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
	require.Error(t, err,
		"one row landed once, so the trail carries one entry — the failed first attempt added none")

	// The trail and the freshness clock mark the same event, so a row landing on
	// this path marks both. A frozen lastProjectedAt beside a published audit
	// entry would have the two surfaces contradict each other about the same
	// write.
	assert.False(t, p.Progress().LastProjectedAt.IsZero(),
		"a replayed write that landed advances the read model's freshness clock like any other")
}
