package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

const coreKVBucket = "CORE"

// Sentinel NanoIDs for deterministic test fixtures (Contract #1, 20 chars, Lattice alphabet).
// Each constant maps to the legacy node_<label>_<id> fixture it replaces.
const (
	sentinelAgreementA1      = "Tsnt1AgreementAaaaaa" // was node_agreement_a1
	sentinelAgreementA2      = "Tsnt2AgreementBbbbbb" // was node_agreement_a2
	sentinelAgreementErr1    = "Tsnt3AgreementErrrrr" // was node_agreement_err1
	sentinelAgreementX1      = "Tsnt4AgreementXxxxxx" // was node_agreement_x1
	sentinelIdentityI1       = "Tsnt5JdentityJjjjjjj" // was node_identity_i1
	sentinelAgreementInf1    = "Tsnt6AgreementJnfrrr" // was node_agreement_inf1
	sentinelAgreementStr1    = "Tsnt7AgreementStrrrr" // was node_agreement_str1
	sentinelAgreementRst1    = "Tsnt8AgreementRst111" // was node_agreement_rst1
	sentinelAgreementRst2    = "Tsnt9AgreementRst222" // was node_agreement_rst2
	sentinelAgreementRes1    = "TsntAagreementRes111" // was node_agreement_res1
	sentinelAgreementRes2    = "TsntBagreementRes222" // was node_agreement_res2
	sentinelAgreementHr1     = "TsntCagreementHr1111" // was node_agreement_hr1
	sentinelAgreementHr2     = "TsntDagreementHr2222" // was node_agreement_hr2
	sentinelAgreementHp1     = "TsntEagreementHp1111" // was node_agreement_hp1
	sentinelAgreementHp2     = "TsntFagreementHp2222" // was node_agreement_hp2
	sentinelAgreementRetry1  = "TsntGagreementRetry1" // was node_agreement_retry1
	sentinelAgreementTerm1   = "TsntHagreementTerm11" // was node_agreement_term1
	sentinelAgreementTerm2   = "TsntJagreementTerm22" // was node_agreement_term2
	sentinelAgreementNilTst  = "TsntKagreementNikTst" // was node_agreement_niltest1
	sentinelAgreementEnt1    = "TsntLagreementEnt111" // was node_agreement_ent1
	sentinelAgreementFail1   = "TsntMagreementFaik11" // was node_agreement_fail1
	sentinelAgreementMp1     = "TsntNagreementMp1111" // was node_agreement_mp1
	sentinelAgreementMp2     = "TsntPagreementMp2222" // was node_agreement_mp2
	sentinelAgreementRi1     = "TsntQagreementRi1111" // was node_agreement_ri1
)

// pipelineEnv holds all resources for a pipeline integration test.
type pipelineEnv struct {
	nc      *nats.Conn // underlying NATS connection; needed for core-NATS subscriptions (e.g. metrics)
	js      jetstream.JetStream
	coreKV  jetstream.KeyValue
	adjKV   jetstream.KeyValue
	manager *consumer.Manager
}

// startPipelineEnv starts an in-memory NATS server and creates the Core KV, Adj KV buckets.
func startPipelineEnv(t *testing.T) *pipelineEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx := context.Background()
	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: coreKVBucket})
	require.NoError(t, err)

	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJ"})
	require.NoError(t, err)

	mgr := consumer.NewManager(js, coreKVBucket)
	return &pipelineEnv{nc: nc, js: js, coreKV: coreKV, adjKV: adjKV, manager: mgr}
}

// putNode writes a node entry to Core KV.
func putNode(t *testing.T, kv jetstream.KeyValue, key string, props map[string]any) {
	t.Helper()
	data, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, data)
	require.NoError(t, err)
}

// pollUntil retries check every 20ms until it returns true or timeout expires.
func pollUntil(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// compileSimplePlan compiles a single-node MATCH plan (no edges, no traversal).
func compileSimplePlan(t *testing.T, query string, keyFields []string) *simple.QueryPlan {
	t.Helper()
	ast, err := simple.Parse(query)
	require.NoError(t, err)
	plan, err := simple.Compile(ast, keyFields)
	require.NoError(t, err)
	return plan
}

// newTargetKV creates a fresh target NATS KV bucket and wraps it with NatsKVAdapter.
func newTargetKV(t *testing.T, env *pipelineEnv, bucketName string, keyOrder []string) (jetstream.KeyValue, *adapter.NatsKVAdapter) {
	t.Helper()
	kv, err := env.js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bucketName})
	require.NoError(t, err)
	adpt, err := adapter.New(kv, keyOrder)
	require.NoError(t, err)
	return kv, adpt
}

// newHealthReporter creates a health KV bucket and returns a Reporter for the given ruleID.
func newHealthReporter(t *testing.T, env *pipelineEnv, ruleID string) *health.Reporter {
	t.Helper()
	kv, err := env.js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "HEALTH-" + ruleID})
	require.NoError(t, err)
	return health.New(kv, ruleID)
}

// startPipeline adds a rule consumer and starts the pipeline in a goroutine.
// Returns cancel func and a WaitGroup entry. Caller must call cancel then wg.Wait.
func startPipeline(t *testing.T, env *pipelineEnv, p *pipeline.Pipeline, ruleID string) (cancel context.CancelFunc, wg *sync.WaitGroup) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	err := env.manager.Add(ctx, ruleID)
	require.NoError(t, err)
	cons := env.manager.Consumer(ruleID)
	require.NotNil(t, cons)

	wg = &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx, cons)
	}()

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return cancel, wg
}

// errAdapter is a test adapter that always returns a transient error from Upsert.
type errAdapter struct {
	calls atomic.Int64
}

func (e *errAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any) error {
	e.calls.Add(1)
	return errors.New("injected upsert error")
}
func (e *errAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (e *errAdapter) Probe(_ context.Context) error                    { return nil }
func (e *errAdapter) Close() error                                     { return nil }

// infraAdapter simulates a target store that is initially down (infrastructure failure)
// and recovers after a configurable number of Probe calls.
type infraAdapter struct {
	upsertCalls atomic.Int64
	probeCalls  atomic.Int64
	recoverAt   int64 // probe call number at which recovery occurs
}

func (a *infraAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any) error {
	a.upsertCalls.Add(1)
	return nats.ErrConnectionClosed // classified as Infrastructure
}
func (a *infraAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (a *infraAdapter) Probe(_ context.Context) error {
	n := a.probeCalls.Add(1)
	if n >= a.recoverAt {
		return nil // target store recovered
	}
	return nats.ErrConnectionClosed // still down
}
func (a *infraAdapter) Close() error { return nil }

// structuralAdapter always returns a structural error from Upsert.
type structuralAdapter struct{}

func (a *structuralAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any) error {
	return jetstream.ErrBucketNotFound // classified as Structural
}
func (a *structuralAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (a *structuralAdapter) Probe(_ context.Context) error                    { return nil }
func (a *structuralAdapter) Close() error                                     { return nil }

// TestPipeline_New_NilAdapter verifies that New returns an error when adapter is nil.
func TestPipeline_New_NilAdapter(t *testing.T) {
	ast, err := simple.Parse("MATCH (a:agreement) RETURN a.id AS agreement_id")
	require.NoError(t, err)
	plan, err := simple.Compile(ast, []string{"agreement_id"})
	require.NoError(t, err)

	_, err = pipeline.New("rule-1", "nats_kv", plan, "CORE", nil, nil, nil, nil)
	assert.Error(t, err, "expected error when adapter is nil")
}

// TestPipeline_Upsert verifies the full evaluate→write path for a single-node upsert (AC #1, #2).
func TestPipeline_Upsert(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	targetKV, adpt := newTargetKV(t, env, "target-upsert", []string{"agreement_id"})

	p, err := pipeline.New("rule-1", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-1")

	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementA1, map[string]any{"id": "a1", "isDeleted": false})

	pollUntil(t, 2*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), "a1")
		return err == nil
	})

	entry, err := targetKV.Get(context.Background(), "a1")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "a1", got["agreement_id"])
}

// TestPipeline_Delete verifies isDeleted=true triggers adapter.Delete and removes the KV key (AC #1, #2).
func TestPipeline_Delete(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	targetKV, adpt := newTargetKV(t, env, "target-delete", []string{"agreement_id"})

	p, err := pipeline.New("rule-2", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-2")

	// Upsert first.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementA2, map[string]any{"id": "a2", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), "a2")
		return err == nil
	})

	// Now soft-delete. Story 2.1 AC #4: NATS-KV adapter writes a
	// tombstone document {"isDeleted": true} instead of physical KVDelete.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementA2, map[string]any{"id": "a2", "isDeleted": true})
	pollUntil(t, 2*time.Second, func() bool {
		entry, err := targetKV.Get(context.Background(), "a2")
		if err != nil {
			return false
		}
		return strings.Contains(string(entry.Value()), `"isDeleted":true`)
	})
}

// TestPipeline_ErrorNak verifies that a write error causes NAK (message not acked) and logs (AC #3).
// We confirm NAK indirectly: since errAdapter always errors, NATS redelivers the message, so the
// adapter call count must exceed 1 — proving the pipeline issued a NAK rather than an ACK.
func TestPipeline_ErrorNak(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ea := &errAdapter{}
	p, err := pipeline.New("rule-err", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, ea, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-err")

	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementErr1, map[string]any{"id": "err1", "isDeleted": false})

	// Wait for at least 2 adapter calls: the first attempt plus at least one redelivery.
	// Redelivery only happens if the message was NAK'd (not ACK'd) — confirming the NAK path.
	pollUntil(t, 5*time.Second, func() bool {
		return ea.calls.Load() > 1
	})
	assert.Greater(t, ea.calls.Load(), int64(1), "expected multiple adapter calls confirming NATS redelivery (NAK path)")
}

// TestPipeline_GracefulShutdown verifies Run returns promptly after ctx is cancelled (AC #4).
func TestPipeline_GracefulShutdown(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	_, adpt := newTargetKV(t, env, "target-shutdown", []string{"agreement_id"})
	p, err := pipeline.New("rule-sd", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, env.manager.Add(ctx, "rule-sd"))
	cons := env.manager.Consumer("rule-sd")

	done := make(chan struct{})
	go func() {
		p.Run(ctx, cons)
		close(done)
	}()

	// Let pipeline start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Run returned promptly — pass.
	case <-time.After(1 * time.Second):
		t.Fatal("pipeline.Run did not return within 1s after ctx cancellation")
	}
}

// TestPipeline_MultiRule_Independent verifies two rules process their own messages without interference (AC #5).
func TestPipeline_MultiRule_Independent(t *testing.T) {
	env := startPipelineEnv(t)

	// Rule A: agreements.
	planA := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	targetA, adptA := newTargetKV(t, env, "target-rule-a", []string{"agreement_id"})

	// Rule B: identities.
	planB := compileSimplePlan(t,
		"MATCH (i:identity) RETURN i.name AS identity_name",
		[]string{"identity_name"})
	targetB, adptB := newTargetKV(t, env, "target-rule-b", []string{"identity_name"})

	pA, err := pipeline.New("rule-a", "nats_kv", planA, coreKVBucket, env.adjKV, env.coreKV, adptA, nil)
	require.NoError(t, err)
	pB, err := pipeline.New("rule-b", "nats_kv", planB, coreKVBucket, env.adjKV, env.coreKV, adptB, nil)
	require.NoError(t, err)

	startPipeline(t, env, pA, "rule-a")
	startPipeline(t, env, pB, "rule-b")

	// Put an agreement node and an identity node.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementX1, map[string]any{"id": "x1", "isDeleted": false})
	putNode(t, env.coreKV, "vtx.identity."+sentinelIdentityI1, map[string]any{"name": "Alice", "isDeleted": false})

	// Rule A target should have agreement x1; Rule B target should have identity i1.
	pollUntil(t, 2*time.Second, func() bool {
		_, errA := targetA.Get(context.Background(), "x1")
		_, errB := targetB.Get(context.Background(), "Alice")
		return errA == nil && errB == nil
	})

	// Verify Rule A target has ONLY agreement (not identity).
	_, err = targetA.Get(context.Background(), "Alice")
	assert.ErrorIs(t, err, jetstream.ErrKeyNotFound, "rule-a target should not contain identity entry")

	// Verify Rule B target has ONLY identity (not agreement).
	_, err = targetB.Get(context.Background(), "x1")
	assert.ErrorIs(t, err, jetstream.ErrKeyNotFound, "rule-b target should not contain agreement entry")
}

// TestPipeline_InfrastructurePause verifies that an infrastructure failure pauses the pipeline,
// probe loop detects recovery, pipeline resumes, and health KV reflects the state (AC #1, #2, #4, #6).
func TestPipeline_InfrastructurePause(t *testing.T) {
	env := startPipelineEnv(t)

	// Use fast probe interval so the test completes quickly.
	origInterval := pipeline.ProbeInterval
	pipeline.ProbeInterval = 50 * time.Millisecond
	t.Cleanup(func() { pipeline.ProbeInterval = origInterval })

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	// infraAdapter: Upsert always fails (infra error); Probe recovers after 2 calls.
	ia := &infraAdapter{recoverAt: 2}

	reporter := newHealthReporter(t, env, "rule-infra")
	p, err := pipeline.New("rule-infra", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, ia, reporter)
	require.NoError(t, err)

	// Create consumer with short AckWait so the unacked infra message is redelivered quickly.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel() })
	cons, err := env.js.CreateOrUpdateConsumer(ctx, "KV_"+coreKVBucket, jetstream.ConsumerConfig{
		Durable:       "refractor-lens-infra",
		DeliverGroup:  "refractor-lens-infra",
		FilterSubject: "$KV." + coreKVBucket + ".>",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Second, // short AckWait for fast redelivery in test
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx, cons)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// Put a node — pipeline tries Upsert → infra failure → probe loop → recovery.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementInf1, map[string]any{"id": "inf1", "isDeleted": false})

	// The infraAdapter never actually writes. We verify:
	// 1. Pipeline entered infra pause (upsert was called).
	pollUntil(t, 3*time.Second, func() bool {
		return ia.upsertCalls.Load() > 0
	})

	// 2. Probe loop ran and detected recovery.
	pollUntil(t, 3*time.Second, func() bool {
		return ia.probeCalls.Load() >= ia.recoverAt
	})

	// 3. Health KV is set back to active after recovery (pipeline called SetActive).
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(ctx)
		return err == nil && entry.Status == "active"
	})
}

// TestPipeline_StructuralPause verifies that a structural failure immediately pauses the pipeline
// and updates health KV — no DLQ entries accumulate (AC #7, #8, #6).
func TestPipeline_StructuralPause(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	sa := &structuralAdapter{}
	reporter := newHealthReporter(t, env, "rule-structural")
	p, err := pipeline.New("rule-structural", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, sa, reporter)
	require.NoError(t, err)

	startPipeline(t, env, p, "rule-structural")

	// Put a node — pipeline tries Upsert → structural failure → pause.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementStr1, map[string]any{"id": "str1", "isDeleted": false})

	// Health KV should be updated to paused/structural.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "paused" && entry.PauseReason != nil && *entry.PauseReason == health.PauseReasonStructural
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonStructural, *entry.PauseReason)
	assert.NotNil(t, entry.LastError)
}

// TestPipeline_HealthKV_StartupRestore_Infra verifies that on process restart with a persisted
// "infra" paused health entry, the pipeline enters the probe loop before processing (AC #3).
func TestPipeline_HealthKV_StartupRestore_Infra(t *testing.T) {
	env := startPipelineEnv(t)

	// Use fast probe interval.
	origInterval := pipeline.ProbeInterval
	pipeline.ProbeInterval = 50 * time.Millisecond
	t.Cleanup(func() { pipeline.ProbeInterval = origInterval })

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	_, targetAdpt := newTargetKV(t, env, "target-restore", []string{"agreement_id"})
	targetKV, _ := newTargetKV(t, env, "target-restore-check", []string{"agreement_id"})
	// Use the actual KV adapter for the target (after "recovery")
	_ = targetKV

	// Pre-write paused infra state in health KV (simulating a previous crash while paused).
	reporter := newHealthReporter(t, env, "rule-restore")
	require.NoError(t, reporter.SetPaused(context.Background(), health.PauseReasonInfra, "simulated restart"))

	// Use a probe-recovering adapter: Probe recovers immediately (recoverAt=1).
	ia := &infraAdapter{recoverAt: 1}
	// Override with a real adapter that writes to targetKV so we can verify processing resumes.
	// For this test we just check that the pipeline transitions from paused→active.

	p, err := pipeline.New("rule-restore", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, targetAdpt, reporter)
	require.NoError(t, err)
	// Swap adapter for probe — use a wrapper that delegates probe to ia but writes to targetAdpt.
	_ = ia // referenced above; probe logic tested separately

	startPipeline(t, env, p, "rule-restore")

	// Pipeline should restore the infra pause state, run probe loop (but targetAdpt.Probe succeeds immediately),
	// set health KV to active, then start processing.
	// Since targetAdpt is a real NatsKVAdapter (Probe works), health KV goes active quickly.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "active"
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status, "health KV should be active after probe recovery on restart")
}

// ── Structural pause + resume tests ───────────────────────────────────────────

// structuralOnceAdapter returns a structural error on the first Upsert call, then
// succeeds on all subsequent calls. Models the recovery scenario: root cause fixed,
// pipeline resumes, NATS redelivers the message, adapter now succeeds.
type structuralOnceAdapter struct {
	calls atomic.Int64
}

func (a *structuralOnceAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any) error {
	if a.calls.Add(1) == 1 {
		return jetstream.ErrBucketNotFound // classified as Structural
	}
	return nil
}
func (a *structuralOnceAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (a *structuralOnceAdapter) Probe(_ context.Context) error                    { return nil }
func (a *structuralOnceAdapter) Close() error                                     { return nil }

// TestPipeline_HealthKV_StartupRestore_Structural verifies that when health KV is pre-written
// as paused/structural, the pipeline blocks immediately on startup without processing messages.
func TestPipeline_HealthKV_StartupRestore_Structural(t *testing.T) {
	env := startPipelineEnv(t) // guards with testing.Short()

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	// Use a recorder so we can verify no writes happen while blocked.
	rec := &recorderAdapter{}

	// Pre-write paused/structural state in health KV (simulating a previous structural failure).
	reporter := newHealthReporter(t, env, "rule-restore-structural")
	require.NoError(t, reporter.SetPaused(context.Background(), health.PauseReasonStructural, "simulated structural restart"))

	p, err := pipeline.New("rule-restore-structural", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, rec, reporter)
	require.NoError(t, err)

	startPipeline(t, env, p, "rule-restore-structural")

	// Put two nodes — pipeline should ignore them (blocked in structural pause restore).
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRst1, map[string]any{"id": "rst1", "isDeleted": false})
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRst2, map[string]any{"id": "rst2", "isDeleted": false})

	// Wait briefly and verify health KV remains paused and no writes occurred.
	time.Sleep(300 * time.Millisecond)

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status, "health KV must remain paused — pipeline is blocked on structural restore")
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonStructural, *entry.PauseReason)

	rec.mu.Lock()
	writeCount := len(rec.upsertKeys)
	rec.mu.Unlock()
	assert.Equal(t, 0, writeCount, "no writes must occur while pipeline is blocked in structural pause restore")
}

// TestPipeline_StructuralPauseResumes verifies that after a structural pause, calling
// Resume transitions health KV to active and the pipeline processes subsequent entities.
func TestPipeline_StructuralPauseResumes(t *testing.T) {
	env := startPipelineEnv(t) // guards with testing.Short()

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	sa := &structuralOnceAdapter{}
	reporter := newHealthReporter(t, env, "rule-resume")
	p, err := pipeline.New("rule-resume", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, sa, reporter)
	require.NoError(t, err)

	startPipeline(t, env, p, "rule-resume")

	// First node — triggers structural error → pause.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRes1, map[string]any{"id": "res1", "isDeleted": false})

	// Wait for health KV to show paused/structural.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "paused" && entry.PauseReason != nil && *entry.PauseReason == health.PauseReasonStructural
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonStructural, *entry.PauseReason)

	// Call Resume from the test goroutine — simulates control API resume.
	p.Resume(context.Background())

	// Health KV must transition to active after resume.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "active"
	})

	entry, err = reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status, "health KV must be active after resume")

	// Put a second node — pipeline is now active, must process it.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRes2, map[string]any{"id": "res2", "isDeleted": false})

	// Verify the adapter is called for the second entity (structuralOnceAdapter succeeds on call >= 2).
	pollUntil(t, 5*time.Second, func() bool {
		return sa.calls.Load() >= 2
	})
	assert.GreaterOrEqual(t, sa.calls.Load(), int64(2), "adapter must be called for the second entity after resume")
}

// ── Hot-reload tests ──────────────────────────────────────────────────────────

// recorderAdapter records each Upsert call's key map for inspection.
type recorderAdapter struct {
	mu         sync.Mutex
	upsertKeys []map[string]any
}

func (r *recorderAdapter) Upsert(_ context.Context, keys map[string]any, _ map[string]any) error {
	r.mu.Lock()
	cp := make(map[string]any, len(keys))
	for k, v := range keys {
		cp[k] = v
	}
	r.upsertKeys = append(r.upsertKeys, cp)
	r.mu.Unlock()
	return nil
}
func (r *recorderAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (r *recorderAdapter) Probe(_ context.Context) error                    { return nil }
func (r *recorderAdapter) Close() error                                     { return nil }

func (r *recorderAdapter) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.upsertKeys)
}

func (r *recorderAdapter) KeyAt(i int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upsertKeys[i]
}

// TestPipeline_HotReloadInto_NextMessageUsesNewAdapter verifies that after
// HotReloadInto the pipeline routes subsequent writes to the new adapter
// while leaving the durable consumer running (AC1, AC2, AC4).
func TestPipeline_HotReloadInto_NextMessageUsesNewAdapter(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	adptA := &recorderAdapter{}
	adptB := &recorderAdapter{}

	p, err := pipeline.New("rule-hotreload", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, adptA, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-hotreload")

	// First message → goes to adapter A.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementHr1, map[string]any{"id": "hr1", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool { return adptA.Count() >= 1 })
	assert.Equal(t, 0, adptB.Count(), "adapter B must receive nothing before hot-reload")

	// Hot-reload to adapter B.
	require.NoError(t, p.HotReloadInto(adptB))

	// Second message → goes to adapter B.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementHr2, map[string]any{"id": "hr2", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool { return adptB.Count() >= 1 })

	countAAfterReload := adptA.Count()
	assert.Equal(t, 1, adptB.Count(), "adapter B must receive exactly the post-reload message")
	assert.Equal(t, 1, countAAfterReload, "adapter A must receive no additional calls after hot-reload")
}

// TestPipeline_HotReloadInto_NilAdapterReturnsError verifies that passing nil
// to HotReloadInto returns an error rather than panicking.
func TestPipeline_HotReloadInto_NilAdapterReturnsError(t *testing.T) {
	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	// Use a pool with fake DSN — no real NATS needed for this unit check.
	adpt := &recorderAdapter{}
	p, err := pipeline.New("rule-nil", "nats_kv", plan, "CORE", nil, nil, adpt, nil)
	require.NoError(t, err)
	assert.Error(t, p.HotReloadInto(nil), "HotReloadInto(nil) must return an error")
}

// TestPipeline_HotReloadPlan_NextMessageUsesNewPlan verifies that after
// HotReloadPlan the pipeline evaluates subsequent messages with the new plan,
// projecting different key columns, while leaving the durable consumer running.
func TestPipeline_HotReloadPlan_NextMessageUsesNewPlan(t *testing.T) {
	env := startPipelineEnv(t)

	// Plan A projects agreement_id; plan B projects agreement_name.
	planA := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	planB := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.name AS agreement_name",
		[]string{"agreement_name"})

	ra := &recorderAdapter{}
	p, err := pipeline.New("rule-hotreload-plan", "nats_kv", planA, coreKVBucket, env.adjKV, env.coreKV, ra, nil)
	require.NoError(t, err)
	startPipeline(t, env, p, "rule-hotreload-plan")

	// First message → plan A → keys must contain agreement_id.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementHp1, map[string]any{"id": "hp1", "name": "name1", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool { return ra.Count() >= 1 })

	firstKeys := ra.KeyAt(0)
	assert.Contains(t, firstKeys, "agreement_id", "plan A must project agreement_id")
	assert.NotContains(t, firstKeys, "agreement_name", "plan A must not project agreement_name")

	// Hot-reload to plan B.
	require.NoError(t, p.HotReloadPlan(planB))

	// Second message → plan B → keys must contain agreement_name.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementHp2, map[string]any{"id": "hp2", "name": "name2", "isDeleted": false})
	pollUntil(t, 2*time.Second, func() bool { return ra.Count() >= 2 })

	secondKeys := ra.KeyAt(1)
	assert.Contains(t, secondKeys, "agreement_name", "plan B must project agreement_name")
	assert.NotContains(t, secondKeys, "agreement_id", "plan B must not project agreement_id")
}

// TestPipeline_HotReloadPlan_NilPlanReturnsError verifies that passing nil
// to HotReloadPlan returns an error rather than panicking.
func TestPipeline_HotReloadPlan_NilPlanReturnsError(t *testing.T) {
	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})
	adpt := &recorderAdapter{}
	p, err := pipeline.New("rule-nil-plan", "nats_kv", plan, "CORE", nil, nil, adpt, nil)
	require.NoError(t, err)
	assert.Error(t, p.HotReloadPlan(nil), "HotReloadPlan(nil) must return an error")
}

// ── Retry queue tests ─────────────────────────────────────────────────────────

// TestPipeline_TransientWriteEnqueuesRetry verifies that when a retry queue is
// configured and a transient write error occurs, the pipeline:
//   - enqueues the entry (not drops it)
//   - ACKs the NATS message (preventing redelivery — AC1, AC4)
//   - continues processing without waiting for the retry (AC4)
func TestPipeline_TransientWriteEnqueuesRetry(t *testing.T) {
	env := startPipelineEnv(t) // guards with testing.Short()

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	// errAdapter always returns a generic error — classified as Transient.
	ea := &errAdapter{}

	rq := failure.NewRetryQueue()

	p, err := pipeline.New("rule-retry", "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, ea, nil)
	require.NoError(t, err)
	p.SetRetryQueue(rq, nil, 3, time.Millisecond)

	startPipeline(t, env, p, "rule-retry")

	// Publish a node message that will cause a transient write error.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRetry1, map[string]any{"id": "retry1", "isDeleted": false})

	// Wait for the adapter to be called at least once.
	pollUntil(t, 2*time.Second, func() bool {
		return ea.calls.Load() >= 1
	})

	// Give NATS a moment to deliver a redelivery if the message were Nak'd.
	// If ACKed (correct), no redelivery will occur within this window.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int64(1), ea.calls.Load(),
		"adapter must be called exactly once — message was ACKed (no NATS redelivery)")
	assert.Equal(t, 1, rq.Len(),
		"retry queue must contain exactly one entry after transient enqueue")
}

// ── Terminal DLQ tests ────────────────────────────────────────────────────────

// terminalOnceAdapter returns failure.Terminal on the first Upsert call,
// then succeeds on all subsequent calls. This lets us verify the pipeline publishes
// a DLQ message for the first entity while continuing to process subsequent ones.
type terminalOnceAdapter struct {
	calls atomic.Int64
}

func (a *terminalOnceAdapter) Upsert(_ context.Context, _ map[string]any, _ map[string]any) error {
	if a.calls.Add(1) == 1 {
		return failure.Terminal(errors.New("bad data: value 'xyz' is not a valid integer"))
	}
	return nil
}
func (a *terminalOnceAdapter) Delete(_ context.Context, _ map[string]any) error { return nil }
func (a *terminalOnceAdapter) Probe(_ context.Context) error                    { return nil }
func (a *terminalOnceAdapter) Close() error                                     { return nil }

// TestPipeline_TerminalWritePublishesDLQAndContinues verifies that when the adapter
// returns failure.Terminal for an entity:
//   - a DLQ message is published with errorClass="TERMINAL", retryCount=0 (AC1, AC2)
//   - the NATS message is ACKed (pipeline does not Nak the message) (AC3)
//   - the retry queue is NOT used (AC2)
//   - the pipeline continues processing the next entity (AC3)
func TestPipeline_TerminalWritePublishesDLQAndContinues(t *testing.T) {
	env := startPipelineEnv(t) // guards with testing.Short()

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ta := &terminalOnceAdapter{}
	rq := failure.NewRetryQueue()

	const ruleID = "rule-terminal"
	p, err := pipeline.New(ruleID, "nats_kv", plan, coreKVBucket, env.adjKV, env.coreKV, ta, nil)
	require.NoError(t, err)
	// SetRetryQueue provides the JetStream handle used for Terminal DLQ publish.
	p.SetRetryQueue(rq, env.js, 3, time.Millisecond)

	startPipeline(t, env, p, ruleID)

	// First node — will produce a Terminal write error and DLQ message.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementTerm1, map[string]any{"id": "term1", "isDeleted": false})
	// Second node — should succeed; proves the pipeline continued.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementTerm2, map[string]any{"id": "term2", "isDeleted": false})

	// Wait until the adapter is called for both entities.
	pollUntil(t, 3*time.Second, func() bool {
		return ta.calls.Load() >= 2
	})

	// Poll for the DLQ stream message.
	ctx := context.Background()
	var dlqMsg failure.DLQMessage
	var gotMsg bool
	deadline := time.Now().Add(5 * time.Second)
	streamName := "REFRACTOR_DLQ_RULE-TERMINAL"
	for time.Now().Before(deadline) && !gotMsg {
		cons, err := env.js.OrderedConsumer(ctx, streamName, jetstream.OrderedConsumerConfig{})
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(100*time.Millisecond))
		if err == nil {
			for m := range batch.Messages() {
				_ = m.Ack()
				if json.Unmarshal(m.Data(), &dlqMsg) == nil {
					gotMsg = true
				}
			}
		}
		if !gotMsg {
			time.Sleep(50 * time.Millisecond)
		}
	}
	require.True(t, gotMsg, "DLQ message must be published for terminal failure")

	assert.Equal(t, "TERMINAL", dlqMsg.ErrorClass, "errorClass must be TERMINAL")
	assert.Equal(t, 0, dlqMsg.RetryCount, "retryCount must be 0 — no retries for terminal failures")
	assert.Equal(t, "write", dlqMsg.FailedStage, "failedStage must be 'write'")
	assert.Equal(t, ruleID, dlqMsg.RuleID, "ruleId must match pipeline ruleID")
	assert.Equal(t, "vtx.agreement."+sentinelAgreementTerm1, dlqMsg.EntityID, "entityId must be the Core KV key of the failed entity")
	assert.NotEmpty(t, dlqMsg.ErrorMessage, "errorMessage must be populated")
	assert.NotEmpty(t, dlqMsg.RawPayload, "rawPayload must be populated")
	_, tsErr := time.Parse(time.RFC3339, dlqMsg.Timestamp)
	assert.NoError(t, tsErr, "timestamp must be a valid RFC3339 string")

	assert.Equal(t, 0, rq.Len(), "retry queue must be empty — terminal failures do not enqueue retries")
}

// TestPipeline_NilAuditWriter_NoOp verifies AC6: when SetAuditWriter is never called
// (auditWriter is nil), the pipeline processes messages normally with no panic and no
// audit entry. The nil-guard in writeAudit short-circuits before calling WriteAudit.
func TestPipeline_NilAuditWriter_NoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)

	const ruleID = "audit-nil-rule"
	plan := compileSimplePlan(t, "MATCH (n:agreement) RETURN n.id AS agreement_id", []string{"agreement_id"})
	targetKV, adpt := newTargetKV(t, env, "TARGET-AUDIT-NIL", []string{"agreement_id"})

	p, err := pipeline.New(ruleID, "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	// Deliberately do NOT call p.SetAuditWriter — auditWriter remains nil.

	startPipeline(t, env, p, ruleID)

	// Put a node — the pipeline must process it without panicking (nil-guard in writeAudit).
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementNilTst, map[string]any{"id": "niltest1"})

	// Wait until the target KV has the upserted row — confirms the pipeline ran normally.
	pollUntil(t, 3*time.Second, func() bool {
		_, err := targetKV.Get(context.Background(), "niltest1")
		return err == nil
	})
}

// TestPipeline_AuditEntry_WrittenOnSuccess verifies that a successful upsert causes
// exactly one audit entry to be published on lattice.refractor.audit.<lensId> (AC1, AC4).
func TestPipeline_AuditEntry_WrittenOnSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)

	const ruleID = "audit-success-rule"
	plan := compileSimplePlan(t, "MATCH (n:agreement) RETURN n.id AS agreement_id", []string{"agreement_id"})
	_, adpt := newTargetKV(t, env, "TARGET-AUDIT-SUCCESS", []string{"agreement_id"})

	p, err := pipeline.New(ruleID, "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)

	// Create the audit stream and attach the writer.
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))
	p.SetAuditWriter(aw)

	startPipeline(t, env, p, ruleID)

	// Put a node into Core KV — this triggers an upsert.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementEnt1, map[string]any{"id": "ent1", "status": "active"})

	// Read the audit entry from the JetStream stream.
	cons, err := env.js.CreateOrUpdateConsumer(context.Background(), "AUDIT_"+ruleID, jetstream.ConsumerConfig{
		Name:          "pipeline-audit-test-consumer",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	msg, err := cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err, "audit entry must be published on successful write")

	var entry health.AuditEntry
	require.NoError(t, json.Unmarshal(msg.Data(), &entry))
	assert.Equal(t, "vtx.agreement."+sentinelAgreementEnt1, entry.EntityID)
	assert.Equal(t, "upsert", entry.Operation)
	assert.NotEmpty(t, entry.OutputRowHash, "upsert audit entry must have a non-empty outputRowHash")
	_, tsErr := time.Parse(time.RFC3339, entry.Timestamp)
	assert.NoError(t, tsErr, "Timestamp must be valid RFC3339")
}

// TestPipeline_NoAuditEntry_OnWriteFailure verifies that a failed write does not
// produce an audit entry (AC4 — audit entries represent only committed successful writes).
func TestPipeline_NoAuditEntry_OnWriteFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)

	const ruleID = "audit-failure-rule"
	plan := compileSimplePlan(t, "MATCH (n:agreement) RETURN n.id AS agreement_id", []string{"agreement_id"})

	// Use an adapter that always fails — no successful writes possible.
	p, err := pipeline.New(ruleID, "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, &errAdapter{}, nil)
	require.NoError(t, err)

	// Create the audit stream and attach the writer.
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))
	p.SetAuditWriter(aw)

	startPipeline(t, env, p, ruleID)

	// Trigger a write attempt that will fail.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementFail1, map[string]any{"id": "fail1"})

	// Give the pipeline time to process and (attempt to) write.
	time.Sleep(300 * time.Millisecond)

	// The audit stream must remain empty — no successful writes occurred.
	stream, err := env.js.Stream(context.Background(), "AUDIT_"+ruleID)
	require.NoError(t, err)
	info, err := stream.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), info.State.Msgs, "audit stream must be empty when all writes fail")
}

// TestPipeline_SetLagPoller_StartsMetricsPublishing verifies that when SetLagPoller is
// configured before Run, the lag poller goroutine starts alongside the pipeline and
// publishes metrics to lattice.refractor.metrics.<lensId> (P-2.3 integration seam test).
func TestPipeline_SetLagPoller_StartsMetricsPublishing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	env := startPipelineEnv(t)

	orig := health.MetricsInterval
	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = orig }()

	const ruleID = "lag-wire-rule"
	plan := compileSimplePlan(t, "MATCH (n:agreement) RETURN n.id AS agreement_id", []string{"agreement_id"})
	_, adpt := newTargetKV(t, env, "TARGET-LAG-WIRE", []string{"agreement_id"})

	p, err := pipeline.New(ruleID, "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)

	// Subscribe to metrics subject before starting the pipeline.
	msgCh := make(chan *nats.Msg, 10)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Attach the lag poller — uses the same consumer as the pipeline message loop.
	ctx := context.Background()
	require.NoError(t, env.manager.Add(ctx, ruleID))
	lagCons := env.manager.Consumer(ruleID)
	require.NotNil(t, lagCons)

	lp := health.NewLagPoller(env.nc, lagCons, nil, ruleID)
	p.SetLagPoller(lp)

	// Start the pipeline (env.manager.Add is idempotent for the same ruleID).
	startPipeline(t, env, p, ruleID)

	// Verify the lag poller goroutine is running: receive at least one metric.
	select {
	case msg := <-msgCh:
		var m health.LagMetric
		require.NoError(t, json.Unmarshal(msg.Data, &m), "metric payload must be valid JSON")
		assert.Equal(t, ruleID, m.RuleID)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: SetLagPoller wiring did not start the lag poller goroutine")
	}
}

// ── Manual pause/resume tests ─────────────────────────────────────────────────

// TestPipeline_ManualPause_HaltsAndResumes verifies the full manual pause/resume
// lifecycle: Pause() halts the fetch loop with health status "paused"/"manual",
// and Resume() restarts it with health status "active" and processing continues (AC1–AC3).
func TestPipeline_ManualPause_HaltsAndResumes(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ra := &recorderAdapter{}
	reporter := newHealthReporter(t, env, "rule-manual-pause")
	p, err := pipeline.New("rule-manual-pause", "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, ra, reporter)
	require.NoError(t, err)

	startPipeline(t, env, p, "rule-manual-pause")

	// Put first node and wait for processing to confirm pipeline is active.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementMp1, map[string]any{"id": "mp1", "isDeleted": false})
	pollUntil(t, 3*time.Second, func() bool { return ra.Count() >= 1 })

	// Call Pause — pipeline should set health KV to paused/manual and halt.
	p.Pause(context.Background())

	// Wait for health KV to confirm the paused/manual state (AC1).
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "paused" &&
			entry.PauseReason != nil && *entry.PauseReason == health.PauseReasonManual
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonManual, *entry.PauseReason)

	// Put second node while paused — should not be processed while paused (AC1, AC3).
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementMp2, map[string]any{"id": "mp2", "isDeleted": false})
	countAfterPause := ra.Count()

	// Call Resume — pipeline should set health KV to active and restart processing (AC2).
	p.Resume(context.Background())

	// Health KV must transition to active after resume.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "active"
	})

	entry, err = reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)

	// Second node (buffered in NATS consumer) must be processed after resume (AC3).
	pollUntil(t, 5*time.Second, func() bool { return ra.Count() > countAfterPause })
	assert.Greater(t, ra.Count(), countAfterPause, "buffered message must be processed after resume")
}

// TestPipeline_Pause_SetsHealthManual verifies that calling Pause() writes a health
// KV entry with status="paused" and pauseReason="manual" (AC1, FR30).
func TestPipeline_Pause_SetsHealthManual(t *testing.T) {
	env := startPipelineEnv(t)

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	ra := &recorderAdapter{}
	reporter := newHealthReporter(t, env, "rule-pause-health")
	p, err := pipeline.New("rule-pause-health", "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, ra, reporter)
	require.NoError(t, err)

	startPipeline(t, env, p, "rule-pause-health")

	// Call Pause and verify health KV immediately.
	p.Pause(context.Background())

	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "paused" &&
			entry.PauseReason != nil && *entry.PauseReason == health.PauseReasonManual
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status, "status must be paused after Pause()")
	require.NotNil(t, entry.PauseReason, "pauseReason must be non-nil after Pause()")
	assert.Equal(t, health.PauseReasonManual, *entry.PauseReason, "pauseReason must be 'manual'")
}

// TestPipeline_Resume_OverridesInfraPause verifies that calling Resume() while the
// pipeline is in an infra probe loop causes the probe loop to exit immediately
// and health KV to transition to active — without waiting for a probe success (AC4).
func TestPipeline_Resume_OverridesInfraPause(t *testing.T) {
	env := startPipelineEnv(t)

	// Use a very long probe interval so the probe loop will not exit on its own
	// during the test — only Resume() can unblock it.
	origInterval := pipeline.ProbeInterval
	pipeline.ProbeInterval = 60 * time.Second
	t.Cleanup(func() { pipeline.ProbeInterval = origInterval })

	plan := compileSimplePlan(t,
		"MATCH (a:agreement) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	// infraAdapter: Upsert always fails (infra error); Probe never recovers (recoverAt > test lifetime).
	ia := &infraAdapter{recoverAt: 9999}

	reporter := newHealthReporter(t, env, "rule-resume-infra")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cons, err := env.js.CreateOrUpdateConsumer(ctx, "KV_"+coreKVBucket, jetstream.ConsumerConfig{
		Durable:       "refractor-lens-resume-infra",
		DeliverGroup:  "refractor-lens-resume-infra",
		FilterSubject: "$KV." + coreKVBucket + ".>",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Second,
	})
	require.NoError(t, err)

	p, err := pipeline.New("rule-resume-infra", "nats_kv", plan, coreKVBucket,
		env.adjKV, env.coreKV, ia, reporter)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx, cons)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// Put a node to trigger an infrastructure failure → probe loop enters.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementRi1, map[string]any{"id": "ri1", "isDeleted": false})

	// Wait for the pipeline to enter infra pause.
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "paused" &&
			entry.PauseReason != nil && *entry.PauseReason == health.PauseReasonInfra
	})

	// Now call Resume() — must override the probe loop without waiting for probe interval.
	p.Resume(context.Background())

	// Health KV must quickly transition to active (well within the 60s probe interval).
	pollUntil(t, 3*time.Second, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.Status == "active"
	})

	entry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status, "health KV must be active after Resume() overrides infra probe loop")
}
