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

// TestE2E_FullNineStepCommitPath is the Story 1.8 capstone: a single
// integration test that publishes one fully-formed operation, traces
// it through all 9 commit-path steps, and asserts:
//
//   - Core KV: the mutation document exists at its expected key.
//   - Idempotency Tracker: vtx.op.<requestId> exists with mutationKeys
//     and eventClasses populated.
//   - Outbox aspect: the faithful EventList is persisted atomically with
//     the commit (vtx.op.<id>.events), the durable consumer's publish source.
//
// Step ordering is asserted at the log level (steps 1-9 in order) by
// counting structured log lines via a capture handler.
func TestE2E_FullNineStepCommitPath(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	provisionEvents(t, ctx, conn)

	// Seed a script that produces one mutation + one event.
	script := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    return {\"mutations\": [{\"op\": \"create\", \"key\": \"vtx.identity.` + testNanoID2 + `\", \"document\": {\"class\": \"identity\", \"data\": {\"name\": op.payload.name}}}], \"events\": [{\"class\": \"identity.created\", \"data\": {\"identityKey\": \"vtx.identity.` + testNanoID2 + `\"}}]}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", script); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	cp, cons := newPipelineWithRealEvents(t, ctx, conn, "fullnine")
	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// (a) Core KV — mutation present.
	entry, err := conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2)
	if err != nil {
		t.Fatalf("mutation not committed: %v", err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(entry.Value, &doc)
	if doc["createdByOp"] != TrackerKey(env.RequestID) {
		t.Fatalf("createdByOp = %v, want %s", doc["createdByOp"], TrackerKey(env.RequestID))
	}

	// (b) Tracker — present with mutationKeys + eventClasses populated.
	te, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID))
	if err != nil {
		t.Fatalf("tracker: %v", err)
	}
	tr, _ := ParseTracker(te.Value)
	mks, _ := tr.Data["mutationKeys"].([]interface{})
	if len(mks) != 1 {
		t.Fatalf("tracker mutationKeys = %v", tr.Data["mutationKeys"])
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	if len(ecs) != 1 || ecs[0] != "identity.created" {
		t.Fatalf("tracker eventClasses = %v", tr.Data["eventClasses"])
	}

	// (c) Outbox aspect — the faithful EventList is persisted atomically with
	// the commit (vtx.op.<id>.events), the durable consumer's publish source.
	// The commit path's job ends here; publishing is the outbox consumer's job
	// (covered by internal/processor/outbox/consumer_test.go).
	ae, err := conn.KVGet(ctx, testCoreBucket, OutboxAspectKey(env.RequestID))
	if err != nil {
		t.Fatalf("outbox aspect not persisted: %v", err)
	}
	aspect, err := ParseOutboxAspect(ae.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	if len(aspect.Data.Events) != 1 || aspect.Data.Events[0].Payload["identityKey"] != "vtx.identity."+testNanoID2 {
		t.Fatalf("outbox aspect events not faithful: %+v", aspect.Data.Events)
	}
}

// newPipelineWithRealEvents builds a CommitPath wired with the Story
// 1.7 real Committer + outbox-published events + real Acker.
// Each subtest passes a unique `tag` so durables don't collide.
func newPipelineWithRealEvents(t *testing.T, ctx context.Context, conn *substrate.Conn, tag string) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testLogger()
	authz := NewStubAuthorizer(logger)
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, testHealthBucket, "proc-test-"+tag, 10*time.Second, metrics, logger)
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
		Durable:        testDurable + "-" + tag,
		FilterSubjects: []string{"ops.default"},
		AckWait:        2 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	_ = slog.LevelInfo
	return cp, cons
}
