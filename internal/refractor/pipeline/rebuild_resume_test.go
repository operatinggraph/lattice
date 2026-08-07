package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

const (
	sentinelAgreementRsm1 = "TsntTagreementRsm111"
	sentinelAgreementRsm2 = "TsntVagreementRsm222"
)

// blockingAdapter holds every Upsert until release is closed, so a delivered
// message stays un-acked and the consumer keeps it outstanding.
type blockingAdapter struct{ release chan struct{} }

func (b *blockingAdapter) Upsert(ctx context.Context, _ map[string]any, _ map[string]any, _ uint64) error {
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *blockingAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (b *blockingAdapter) Probe(context.Context) error                          { return nil }
func (b *blockingAdapter) Close() error                                         { return nil }

// reporterOn opens a health bucket of its own and returns a reporter for ruleID.
func reporterOn(t *testing.T, env *pipelineEnv, bucket, ruleID string) *health.Reporter {
	t.Helper()
	_, err := env.js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bucket})
	require.NoError(t, err)
	kv, err := env.conn.OpenKV(context.Background(), bucket)
	require.NoError(t, err)
	return health.New(kv, ruleID)
}

// TestPipeline_Run_ResumesAnInterruptedRebuild covers the restart half of the
// rebuild lifecycle. The watcher that ends a rebuild lives in the process that
// armed it; the rescan does not, because the rebuild IS the durable's reset
// cursor and JetStream keeps that across a restart. So a lens whose process
// dies mid-rebuild comes back with a "rebuilding" health entry and nothing left
// to retire it — the status then reads "rebuilding" for the rest of the lens's
// life, and the auth-plane convergence sweep, which suppresses itself on a
// rebuilding lens, silently stops checking that lens forever.
//
// The drain here is already complete when the process starts (no Core KV
// backlog), which is the case that must un-latch.
func TestPipeline_Run_ResumesAnInterruptedRebuild(t *testing.T) {
	env := startPipelineEnv(t)

	origPoll := pipeline.RebuildPollInterval
	pipeline.RebuildPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pipeline.RebuildPollInterval = origPoll })

	reporter := reporterOn(t, env, "HEALTH-rule-rsm-clear", "rule-rsm-clear")

	// The state a killed process leaves behind: "rebuilding" persisted, no
	// watcher anywhere.
	require.NoError(t, reporter.SetRebuilding(context.Background()))

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	p, err := pipeline.New("rule-rsm-clear", "nats_kv", coreKVBucket, env.adjKV, env.coreKV,
		&blockingAdapter{release: closedChan()}, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	startPipeline(t, env, p, "rule-rsm-clear")

	pollUntil(t, 5*time.Second, func() bool {
		entry, gerr := reporter.GetStatus(context.Background())
		return gerr == nil && entry.Status == health.StatusActive
	})
	assert.False(t, p.RebuildInFlight(), "the resumed watcher must retire itself once it has cleared the status")
}

// TestPipeline_Run_InterruptedRebuildStillDrainingStaysRebuilding is the other
// half, and the reason the fix re-arms the watcher instead of clearing the
// status at startup: an interrupted rebuild whose backlog has NOT drained is
// genuinely still rebuilding. Writing "active" over it would be exactly the
// premature transition the watcher's outstanding-not-backlog check exists to
// prevent — and this is the shape that makes it wrong rather than cosmetic,
// because the in-flight message can still fail and redeliver.
func TestPipeline_Run_InterruptedRebuildStillDrainingStaysRebuilding(t *testing.T) {
	env := startPipelineEnv(t)

	origPoll := pipeline.RebuildPollInterval
	pipeline.RebuildPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pipeline.RebuildPollInterval = origPoll })

	reporter := reporterOn(t, env, "HEALTH-rule-rsm-hold", "rule-rsm-hold")
	require.NoError(t, reporter.SetRebuilding(context.Background()))

	// Backlog the rescan has not reached yet, written before the consumer exists.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRsm1, map[string]any{"id": "rsm1", "isDeleted": false})
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRsm2, map[string]any{"id": "rsm2", "isDeleted": false})

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	ba := &blockingAdapter{release: make(chan struct{})}
	p, err := pipeline.New("rule-rsm-hold", "nats_kv", coreKVBucket, env.adjKV, env.coreKV, ba, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	startPipeline(t, env, p, "rule-rsm-hold")

	// The watcher polls every 5ms across this window; the held Upsert keeps the
	// message delivered-but-unacked, so the consumer never reads as drained.
	require.Never(t, func() bool {
		entry, gerr := reporter.GetStatus(context.Background())
		return gerr == nil && entry.Status == health.StatusActive
	}, 300*time.Millisecond, 5*time.Millisecond,
		"a rebuild whose backlog is still outstanding must not be published active")
	assert.True(t, p.RebuildInFlight(), "the resumed watcher must still hold the rebuild window open")

	// Releasing the drain is what ends the rebuild — the same transition the
	// original in-process watcher would have made.
	close(ba.release)
	pollUntil(t, 5*time.Second, func() bool {
		entry, gerr := reporter.GetStatus(context.Background())
		return gerr == nil && entry.Status == health.StatusActive
	})
}

// TestPipeline_Run_ActiveLensDoesNotArmARebuildWatcher pins the negative: the
// resume is conditioned on the persisted status, so an ordinary boot must not
// open a rebuild window that nothing asked for.
func TestPipeline_Run_ActiveLensDoesNotArmARebuildWatcher(t *testing.T) {
	env := startPipelineEnv(t)

	origPoll := pipeline.RebuildPollInterval
	pipeline.RebuildPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { pipeline.RebuildPollInterval = origPoll })

	reporter := reporterOn(t, env, "HEALTH-rule-rsm-active", "rule-rsm-active")
	require.NoError(t, reporter.SetActive(context.Background()))

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	p, err := pipeline.New("rule-rsm-active", "nats_kv", coreKVBucket, env.adjKV, env.coreKV,
		&blockingAdapter{release: closedChan()}, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	startPipeline(t, env, p, "rule-rsm-active")

	require.Never(t, p.RebuildInFlight, 200*time.Millisecond, 5*time.Millisecond,
		"an active lens must not boot into a rebuild window")
}

// closedChan returns an already-closed channel, so a blockingAdapter built on
// it never actually blocks.
func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// TestPipeline_Rebuild_FailureDoesNotLatchRebuilding is the sibling of the
// restart case above, and the hole it leaves. That test covers a process killed
// MID-rebuild; this one covers a rebuild that never got going — and until now the
// two ended in the same place, because the only writer of the rebuilding → active
// transition is the completion watcher, which Rebuild launches on its SUCCESS
// path only. Every error return cleared the in-flight flag and left the status
// latched at "rebuilding" with nothing remaining to retire it. resumeInterrupted
// Rebuild does not help: it runs at process start, and this process is still up.
//
// Which matters most for a NARROWED lens, and that is why it is fixed here.
// Sweeper.suppressed refuses any tick whose status is not "active", so the
// convergence sweep — the standing healer ActorAwareNarrowingLabels counts as one
// of §4.2's conjuncts before it will narrow anything — stops running for the life
// of the process. A failed rebuild is the single event able to switch off BOTH of
// the two recoveries ConsumerFilter names for a wrong narrow: the rebuild did not
// happen, and the status it left behind suppressed the sweep that covers for it.
//
// RebuildInFlight() is asserted false as well, because it is the FIRST thing
// suppressed() checks — a fix that restored the status but left the flag set
// would keep the sweep suppressed by the other branch and this test would still
// have passed.
func TestPipeline_Rebuild_FailureDoesNotLatchRebuilding(t *testing.T) {
	env := startPipelineEnv(t)

	reporter := reporterOn(t, env, "HEALTH-rule-rbf-latch", "rule-rbf-latch")

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	p, err := pipeline.New("rule-rbf-latch", "nats_kv", coreKVBucket, env.adjKV, env.coreKV,
		&blockingAdapter{release: closedChan()}, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	// Never started, so there is no supervisor to reset — Rebuild fails at its
	// step 3 guard. Any of the three error returns reaches the same exit; this is
	// the one reachable without breaking a collaborator.
	require.Error(t, p.Rebuild(context.Background(), false),
		"a rebuild with no supervisor must fail rather than report success")

	assert.False(t, p.RebuildInFlight(),
		"the in-flight flag is suppressed()'s first check, so a failed rebuild must clear it")

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status,
		"a failed rebuild must not leave the status that suppresses the convergence sweep forever")
	require.NotNil(t, entry.LastError,
		"active is only honest paired with the recorded cause — the lens is live AND at fault")
	assert.Contains(t, *entry.LastError, "no supervisor configured",
		"the recorded cause must name what actually failed")
}
