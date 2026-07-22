package processor

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestE2E_FullCommitWithRealMutation drives the full Story 1.7 commit
// path end-to-end: a Starlark script that proposes a real CreateIdentity
// mutation runs through hydrate→execute→validate→commit, and the result
// lands in Core KV.
func TestE2E_FullCommitWithRealMutation(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// Replace the noop script with one that creates a real identity.
	script := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    return {\"mutations\": [{\"op\": \"create\", \"key\": \"vtx.identity.` + testNanoID2 + `\", \"document\": {\"class\": \"identity\", \"data\": {\"name\": op.payload.name}}}], \"events\": [{\"class\": \"identity.created\", \"data\": {\"identityKey\": \"vtx.identity.` + testNanoID2 + `\"}}]}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", script); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	cp, cons := newRealPipeline(t, ctx, conn)
	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// The mutation key should be present in Core KV.
	entry, err := conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2)
	if err != nil {
		t.Fatalf("mutation not committed: %v", err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(entry.Value, &doc)
	if doc["createdByOp"] != TrackerKey(env.RequestID) {
		t.Fatalf("createdByOp = %v, want %s", doc["createdByOp"], TrackerKey(env.RequestID))
	}

	// Tracker should carry mutationKeys and eventClasses.
	te, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID))
	if err != nil {
		t.Fatalf("tracker: %v", err)
	}
	tr, _ := ParseTracker(te.Value)
	mks, _ := tr.Data["mutationKeys"].([]interface{})
	if len(mks) != 1 {
		t.Fatalf("tracker mutationKeys = %v", tr.Data["mutationKeys"])
	}
}

// TestE2E_DDLViolationRejects: when the script proposes a key with
// class whose DDL forbids the operationType, the commit path rejects
// with DDLViolation.
func TestE2E_DDLViolationRejects(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// identity DDL permittedCommands = ["CreateIdentity"]; we publish
	// operationType "DeleteIdentity" which is not in that list. The
	// script returns a mutation for the identity vertex.
	script := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    return {\"mutations\": [{\"op\": \"create\", \"key\": \"vtx.identity.` + testNanoID2 + `\", \"document\": {\"class\": \"identity\", \"data\": {}}}], \"events\": []}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", script); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	cp, cons := newRealPipeline(t, ctx, conn)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteIdentity" // not in permittedCommands
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	// Mutation must NOT be present (atomic batch never ran).
	if _, err := conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2); err == nil {
		t.Fatalf("mutation should not have been committed after DDL violation")
	}
	// Tracker must NOT be present either.
	if _, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID)); err == nil {
		t.Fatalf("tracker should not exist after DDL violation")
	}
}

// newRealPipeline wires a CommitPath with real Validator and Committer
// (Story 1.7). Distinct from newPipeline (which uses the legacy stubs)
// so existing tests are untouched. Uses a distinct durable so it does
// not collide with newPipeline's consumer.
func newRealPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testLogger()
	authz := NewStubAuthorizer(logger)
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, testHealthBucket, "proc-test-real", 10*time.Second, metrics, logger)
	cache := NewDDLCache(conn, testCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	committer := NewCommitter(conn, testCoreBucket, cache, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  testCoreBucket,
		HealthKV:    testHealthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydratorWithCache(conn, testCoreBucket, cache, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   NewValidator(cache, conn, testCoreBucket, logger),
		Committer:   committer,
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     testStream,
		Durable:        testDurable + "-real",
		FilterSubjects: []string{"ops.default"},
		AckWait:        2 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	_ = slog.LevelInfo
	return cp, cons
}
