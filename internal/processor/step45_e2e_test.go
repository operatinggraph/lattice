package processor

import (
	"context"
	"testing"
)

// TestE2E_FullPipelineCleanExecution exercises the entire commit path
// steps 1→5 with a real Hydrator and Executor. The noop script seeded
// by setupTestPipeline returns an empty mutation set. Story 1.6's
// stubbed step 6+8 swallow the empty result; step 9 acks.
func TestE2E_FullPipelineCleanExecution(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
	if _, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID)); err != nil {
		t.Fatalf("tracker not written: %v", err)
	}
}

// TestE2E_HydrationMissTerminates publishes an envelope whose contextHint
// references a missing key. Step 4 returns *HydrationError; the commit
// path emits a HydrationFailed reply and terms the message.
func TestE2E_HydrationMissTerminates(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{"vtx.identity.AAAAAAAAAAAAAAAAAAAA"}}
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	if metrics.OpsRejected.Load() != 1 {
		t.Fatalf("OpsRejected = %d, want 1", metrics.OpsRejected.Load())
	}
	if metrics.OpsCommitted.Load() != 0 {
		t.Fatalf("OpsCommitted = %d, want 0 (hydration missed)", metrics.OpsCommitted.Load())
	}
}

// TestE2E_ScriptErrorTerminates seeds a script that calls fail(). Step 5
// returns *ScriptError; the commit path emits a ScriptFailed reply.
func TestE2E_ScriptErrorTerminates(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	// Replace the noop script with a failing one.
	failingScript := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    fail(\"deliberate test failure\")\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", failingScript); err != nil {
		t.Fatalf("seed failing script: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	if metrics.OpsRejected.Load() != 1 {
		t.Fatalf("OpsRejected = %d, want 1", metrics.OpsRejected.Load())
	}
}

// TestE2E_SandboxViolationTerminates seeds a script that references `os`
// AND — even if the sandbox check were bypassed — attempts to mutate a
// real target key. Step 5 returns *ScriptError with Code=SandboxViolation;
// the commit path terms the message before step 6+8 ever run, so the
// rogue mutation must never reach Core KV.
func TestE2E_SandboxViolationTerminates(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	rogueKey := "vtx.identity.rogueSandboxTargetAAAA"
	violating := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    x = os.getenv(\"SECRET\")\n    return {\"mutations\": [{\"op\": \"create\", \"key\": \"` + rogueKey + `\", \"document\": {\"class\": \"identity\"}}], \"events\": []}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", violating); err != nil {
		t.Fatalf("seed sandbox-violating script: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)
	if metrics.OpsRejected.Load() != 1 {
		t.Fatalf("OpsRejected = %d, want 1", metrics.OpsRejected.Load())
	}

	// The rogue mutation must never have reached Core KV: SandboxViolation
	// halts step 5, so step 6 (validate) and step 8 (commit) never run.
	if _, err := conn.KVGet(ctx, testCoreBucket, rogueKey); err == nil {
		t.Fatalf("BYPASS ESCAPED: rogue mutation key %q found in Core KV after SandboxViolation", rogueKey)
	}
}

// TestE2E_ContextHintHydratedAndPassedToScript proves the script can
// read hydrated state placed there by step 4.
func TestE2E_ContextHintHydratedAndPassedToScript(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	actorKey := "vtx.identity." + testNanoID2
	if _, err := conn.KVPut(ctx, testCoreBucket, actorKey,
		[]byte(`{"class":"identity","isDeleted":false,"data":{"name":"Andrew"}}`)); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	// Replace script with one that asserts presence of actor.
	script := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    if state.get(op.actor) == None:\n        fail(\"actor not hydrated\")\n    if state[op.actor].data[\"name\"] != \"Andrew\":\n        fail(\"actor name mismatch\")\n    return {\"mutations\": [], \"events\": []}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", script); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	logger := testLogger()
	authz := NewStubAuthorizer(logger)
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, testHealthBucket, "proc-test-readstate", 10, metrics, logger)
	committer := NewStubCommitter(conn, testCoreBucket, logger, nil)
	cp := NewCommitPath(Deps{
		Conn: conn, CoreBucket: testCoreBucket, HealthKV: testHealthBucket,
		Authorizer: authz,
		Hydrator:   NewHydrator(conn, testCoreBucket, logger),
		Executor:   NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:  &StubValidator{logger: logger},
		Committer:  committer,
		Metrics:    metrics, Heartbeater: hb, Logger: logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName: testStream, Durable: testDurable + "-readstate",
		FilterSubjects: []string{"ops.default"},
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{actorKey}}
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
	_ = context.Background
}
