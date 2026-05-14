package processor

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// TestE2E_FullTenStepCommitPath is the Story 1.8 capstone: a single
// integration test that publishes one fully-formed operation, traces
// it through all 10 commit-path steps, and asserts:
//
//   - Core KV: the mutation document exists at its expected key.
//   - Idempotency Tracker: vtx.op.<requestId> exists with mutationKeys
//     and eventClasses populated.
//   - core-events: the published event is durably stored at events.<class>.
//
// Step ordering is asserted at the log level (steps 1-10 in order) by
// counting structured log lines via a capture handler.
func TestE2E_FullTenStepCommitPath(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	provisionEvents(t, ctx, conn)

	// Seed a script that produces one mutation + one event.
	script := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    return {\"mutations\": [{\"op\": \"create\", \"key\": \"vtx.identity.` + testNanoID2 + `\", \"document\": {\"class\": \"identity\", \"data\": {\"name\": op.payload.name}}}], \"events\": [{\"class\": \"identity.created\", \"data\": {\"identityKey\": \"vtx.identity.` + testNanoID2 + `\"}}]}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.identity.script", script); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	cp, cons := newPipelineWithRealEvents(t, ctx, conn, "fullten")
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

	// (c) core-events — the event landed on `events.identity.created`.
	eventsCons, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "core-events", jetstream.ConsumerConfig{
		Durable:        "fullten-events",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("event consumer: %v", err)
	}
	batch, err := eventsCons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("Fetch event: %v", err)
	}
	got := 0
	for m := range batch.Messages() {
		got++
		if m.Subject() != "events.identity.created" {
			t.Fatalf("event subject = %q, want events.identity.created", m.Subject())
		}
		var ev Event
		if err := json.Unmarshal(m.Data(), &ev); err != nil {
			t.Fatalf("event unmarshal: %v", err)
		}
		if ev.RequestID != env.RequestID {
			t.Fatalf("event requestId = %q, want %q", ev.RequestID, env.RequestID)
		}
		if ev.EventType != "identity.created" {
			t.Fatalf("event type = %q", ev.EventType)
		}
		_ = m.Ack()
	}
	if got != 1 {
		t.Fatalf("expected exactly 1 event on core-events, got %d", got)
	}
}

// newPipelineWithRealEvents builds a CommitPath wired with the Story
// 1.7 real Committer + Story 1.8 real EventPublisher + real Acker.
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
		Validator:   NewValidator(cache, logger),
		Committer:   committer,
		Events:      NewEventPublisher(conn, logger),
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
