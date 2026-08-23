package pipeline_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

// Sentinel NanoIDs for the guarded audit-gate fixtures (Contract #1, 20 chars,
// Lattice alphabet).
const (
	sentinelAgreementDecl1 = "TsntTagreementDeck11"
	sentinelAgreementAccp1 = "TsntVagreementAccp11"
)

// declinedWatermark is the projectionSeq pre-stamped on the target row the
// guard must refuse to overwrite. It is far above any stream sequence an
// embedded server hands out inside one test, so the refusal is a property of
// the fixture rather than a race with the substrate's own numbering.
const declinedWatermark = 1_000_000_000

// outcomeProbe wraps a real NatsKVAdapter and records the UpsertOutcome behind
// each write, so a test can assert what the pipeline's audit gate was handed
// rather than inferring it from the target's after-state.
//
// It holds the adapter as a NAMED FIELD and forwards every method explicitly.
// Embedding would promote UpsertWithOutcome — the method writeResults actually
// calls — straight past any override, the trap adapter.OutcomeUpserter
// documents; naming the field makes forwarding the only route a call has, so
// the recording cannot be silently bypassed.
type outcomeProbe struct {
	inner *adapter.NatsKVAdapter

	calls atomic.Int64
	mu    sync.Mutex
	seen  []adapter.UpsertOutcome
}

var (
	_ adapter.Adapter         = (*outcomeProbe)(nil)
	_ adapter.OutcomeUpserter = (*outcomeProbe)(nil)
	_ adapter.SeqGuarded      = (*outcomeProbe)(nil)
)

func (o *outcomeProbe) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	_, err := o.UpsertWithOutcome(ctx, keys, row, seq)
	return err
}

func (o *outcomeProbe) UpsertWithOutcome(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	outcome, err := o.inner.UpsertWithOutcome(ctx, keys, row, seq)
	if err == nil {
		o.mu.Lock()
		o.seen = append(o.seen, outcome)
		o.mu.Unlock()
		o.calls.Add(1)
	}
	return outcome, err
}

func (o *outcomeProbe) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	return o.inner.Delete(ctx, keys, seq)
}
func (o *outcomeProbe) Probe(ctx context.Context) error { return o.inner.Probe(ctx) }
func (o *outcomeProbe) Close() error                    { return o.inner.Close() }
func (o *outcomeProbe) Guarded() bool                   { return o.inner.Guarded() }

// outcomes copies out every outcome recorded so far.
func (o *outcomeProbe) outcomes() []adapter.UpsertOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]adapter.UpsertOutcome(nil), o.seen...)
}

// TestPipeline_GuardedUpsertDeclinedByWatermark_PublishesNoAudit pins the audit
// gate to what actually COMMITTED, against the real guarded NatsKVAdapter.
//
// The guarded upsert path reports Wrote:true on every call by design —
// advancing or deliberately holding the projectionSeq watermark is its job
// whatever the row says — so an audit gate reading Wrote publishes an entry,
// complete with the outputRowHash of the row it claims to have written, for a
// write the ordering guard refused. On a capability-plane lens under rebuild
// that is most of the traffic on the audit stream, and it evicts every other
// lens's forensic trail to make room for assertions that are not true.
//
// Both halves run through ONE pipeline, one adapter and one audit stream, so
// the declined and accepted keys differ in exactly one thing: whether the
// target row already carries a watermark this evaluation cannot beat.
func TestPipeline_GuardedUpsertDeclinedByWatermark_PublishesNoAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)
	ctx := context.Background()

	const ruleID = "audit-guard-decline-rule"
	eng, cr := compileFullRule(t,
		"MATCH (n:agreement {key: $actorKey}) RETURN n.id AS agreement_id, n.status AS status",
		[]string{"agreement_id"})
	targetKV, inner := newTargetKV(t, env, "TARGET-AUDIT-GUARD-DECLINE", []string{"agreement_id"})
	inner.SetGuarded(true)
	adpt := &outcomeProbe{inner: inner}

	// The row the guard must decline: a watermark no evaluation in this test can
	// outrank, plus a marker field the guarded body has no column for — so a
	// write that did land would be visible as the marker disappearing, not only
	// as a revision bump.
	seeded, err := json.Marshal(map[string]any{
		"projectionSeq": uint64(declinedWatermark),
		"marker":        "untouched",
	})
	require.NoError(t, err)
	_, err = targetKV.Put(ctx, "declined1", seeded)
	require.NoError(t, err)
	seededEntry, err := targetKV.Get(ctx, "declined1")
	require.NoError(t, err)

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket,
		env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	require.NoError(t, health.EnsureAuditStream(ctx, env.conn))
	p.SetAuditWriter(health.NewAuditWriter(env.conn, ruleID))

	startPipeline(t, env, p, ruleID)

	auditCons, err := env.js.CreateOrUpdateConsumer(ctx, health.AuditStreamName, jetstream.ConsumerConfig{
		Name:          "pipeline-audit-guard-decline-consumer",
		FilterSubject: subjects.Audit(ruleID),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	// The declined half. The CDC event projects onto the pre-stamped key, so the
	// guard refuses the write and returns nil.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementDecl1,
		map[string]any{"id": "declined1", "status": "open"})

	// Waiting on the probe separates "the gate ran and published nothing" from
	// "the write never reached the gate at all" — the second would satisfy every
	// assertion below for entirely the wrong reason.
	pollUntil(t, 3*time.Second, func() bool {
		return adpt.calls.Load() >= 1
	})

	declined := adpt.outcomes()
	require.Len(t, declined, 1, "exactly one upsert reached the adapter")
	require.True(t, declined[0].DeclinedByWatermark,
		"the fixture must actually exercise the ordering guard's decline")
	require.False(t, declined[0].Committed, "a declined write stored no row")
	require.True(t, declined[0].Wrote,
		"Wrote stays true on this path, which is exactly why the gate cannot read it")

	declinedEntry, err := targetKV.Get(ctx, "declined1")
	require.NoError(t, err)
	require.Equal(t, seededEntry.Revision, declinedEntry.Revision,
		"a declined write must leave the target's revision where it found it")
	var declinedRow map[string]any
	require.NoError(t, json.Unmarshal(declinedEntry.Value, &declinedRow))
	require.Equal(t, "untouched", declinedRow["marker"],
		"the stored row must be exactly as seeded — a guarded body carries no marker field")

	_, err = auditCons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
	require.Error(t, err,
		"a guarded upsert the ordering guard declined wrote no row, so it must publish no audit entry")

	// The read model did not change, so its freshness clock must not say it did.
	require.True(t, p.Progress().LastProjectedAt.IsZero(),
		"lastProjectedAt is the clock of the last row that landed, and none has")

	// The accepted half: same lens, same adapter, same stream — an absent target
	// key, so the guard commits.
	seqBeforeAccepted := p.Progress().LastAppliedSeq
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementAccp1,
		map[string]any{"id": "accepted1", "status": "open"})
	pollUntil(t, 3*time.Second, func() bool {
		return p.Progress().LastAppliedSeq > seqBeforeAccepted
	})

	acceptedEntry, err := targetKV.Get(ctx, "accepted1")
	require.NoError(t, err, "the guard must have committed the write to an unstamped key")
	var acceptedRow map[string]any
	require.NoError(t, json.Unmarshal(acceptedEntry.Value, &acceptedRow))
	assert.Equal(t, "open", acceptedRow["status"])

	msg, err := auditCons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err, "a guarded upsert that committed must publish an audit entry")
	var entry health.AuditEntry
	require.NoError(t, json.Unmarshal(msg.Data(), &entry))
	assert.Equal(t, "vtx.agreement."+sentinelAgreementAccp1, entry.EntityID,
		"the only audit entry on this lens's subject must be the write that actually landed")
	assert.Equal(t, "upsert", entry.Operation)
	assert.NotEmpty(t, entry.OutputRowHash)

	_, err = auditCons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
	require.Error(t, err, "exactly one audit entry — the committed write's — belongs on the stream")

	assert.False(t, p.Progress().LastProjectedAt.IsZero(),
		"a committed write is what advances the freshness clock")
}
