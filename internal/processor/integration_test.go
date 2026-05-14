package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

const (
	testCoreBucket   = "core-kv"
	testHealthBucket = "health-kv"
	testStream       = "core-operations"
	testDurable      = "processor-main"
)

// startEmbeddedNATS spins up an in-process JetStream-enabled NATS server.
// Mirrors the substrate package's test harness.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
		_ = server.VERSION
	})
	return s.ClientURL()
}

// provisionHarness mirrors what `cmd/bootstrap` does: Core KV bucket
// with TTL + AllowAtomicPublish, Health KV bucket with TTL, and the
// core-operations stream.
func provisionHarness(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	for _, bucket := range []string{testCoreBucket, testHealthBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("create KV bucket %q: %v", bucket, err)
		}
	}
	// AllowAtomicPublish on Core KV.
	streamName := "KV_" + testCoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}

	// core-operations stream.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     testStream,
		Subjects: []string{"ops.*", "ops.meta.>"},
	})
	if err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}
}

// newPipeline builds a CommitPath + heartbeater + consumer ready to run.
func newPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, logger *slog.Logger) (*CommitPath, jetstream.Consumer, *Metrics) {
	t.Helper()
	authz := NewStubAuthorizer(logger)
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, testHealthBucket, "proc-test-"+testNanoID1, 10*time.Second, metrics, logger)
	committer := NewStubCommitter(conn, testCoreBucket, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  testCoreBucket,
		HealthKV:    testHealthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydrator(conn, testCoreBucket, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   &StubValidator{logger: logger},
		Committer:   committer,
		Events:      &StubEventPublisher{logger: logger},
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName: testStream,
		Durable:    testDurable,
		// Restrict to a test-specific subject so each subtest can run
		// against its own consumer-subject pair without cross-talk.
		FilterSubjects: []string{"ops.default"},
		AckWait:        2 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons, metrics
}

func publishEnvelope(t *testing.T, conn *substrate.Conn, env *OperationEnvelope) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	subject := "ops." + string(env.Lane)
	_, err = conn.JetStream().Publish(context.Background(), subject, b)
	if err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}
}

func newTestEnvelope(requestID string) *OperationEnvelope {
	return &OperationEnvelope{
		RequestID:     requestID,
		Lane:          LaneDefault,
		OperationType: "CreateIdentity",
		Actor:         "vtx.identity." + testNanoID2,
		SubmittedAt:   "2026-05-13T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Andrew"}`),
	}
}

// seedNoopScript writes a vtx.meta.<class> DDL meta-vertex and a
// vtx.meta.<class>.script aspect containing a no-op Starlark script.
// Used by integration tests that just need step 4+5 to pass through.
func seedNoopScript(t *testing.T, ctx context.Context, conn *substrate.Conn, class string) {
	t.Helper()
	ddlKey := "vtx.meta." + class
	scriptKey := ddlKey + ".script"
	ddlDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"` + class + `","permittedCommands":["CreateIdentity"]}}`)
	scriptDoc := []byte(`{"class":"meta.script","isDeleted":false,"data":{"source":"def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, ddlKey, ddlDoc); err != nil {
		t.Fatalf("seed DDL %s: %v", ddlKey, err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, scriptKey, scriptDoc); err != nil {
		t.Fatalf("seed script %s: %v", scriptKey, err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// driveOne runs the consumer loop until f signals done or ctx expires.
func driveOne(t *testing.T, ctx context.Context, cp *CommitPath, cons jetstream.Consumer, want MessageOutcome) MessageOutcome {
	t.Helper()
	got := make(chan MessageOutcome, 1)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		outcome := cp.HandleMessage(ctx, m)
		select {
		case got <- outcome:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()
	select {
	case outcome := <-got:
		if want != "" && outcome != want {
			t.Fatalf("outcome mismatch: got %q want %q", outcome, want)
		}
		// Drain a brief moment to let ack flush.
		time.Sleep(100 * time.Millisecond)
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for outcome (want %q)", want)
		return ""
	}
}

func setupTestPipeline(t *testing.T) (context.Context, *substrate.Conn, *CommitPath, jetstream.Consumer, *Metrics) {
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "processor-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionHarness(t, ctx, conn)
	seedNoopScript(t, ctx, conn, "identity")
	cp, cons, metrics := newPipeline(t, ctx, conn, testLogger())
	return ctx, conn, cp, cons, metrics
}

// AC1: first delivery accepted — envelope consumed, dedup checks empty,
// auth allows, stubbed steps run, tracker is written, message acked.
func TestIntegration_FirstDeliveryAccepted(t *testing.T) {
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Tracker must exist with isDeleted=false.
	entry, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID))
	if err != nil {
		t.Fatalf("tracker not present after accept: %v", err)
	}
	tr, err := ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	if tr.IsDeleted {
		t.Fatalf("tracker should not be tombstoned")
	}
	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
	if metrics.OpsDuplicates.Load() != 0 {
		t.Fatalf("OpsDuplicates = %d, want 0", metrics.OpsDuplicates.Load())
	}
}

// AC2: duplicate short-circuited at step 2.
func TestIntegration_DuplicateShortCircuited(t *testing.T) {
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)
	env := newTestEnvelope(testNanoID1)

	// Pre-seed the tracker so the next delivery is a dedup hit.
	tr := NewTracker(env, time.Now())
	val, _ := tr.Marshal()
	if _, err := conn.KVCreate(ctx, testCoreBucket, tr.Key, val); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeDuplicate)

	if metrics.OpsDuplicates.Load() != 1 {
		t.Fatalf("OpsDuplicates = %d, want 1", metrics.OpsDuplicates.Load())
	}
	if metrics.OpsCommitted.Load() != 0 {
		t.Fatalf("OpsCommitted = %d, want 0 (the tracker existed before delivery)", metrics.OpsCommitted.Load())
	}
}

// AC3: malformed envelope nack-with-term.
func TestIntegration_MalformedNackTerminated(t *testing.T) {
	ctx, conn, cp, cons, metrics := setupTestPipeline(t)

	// Publish a raw payload that is not a valid envelope.
	bad := []byte(`{"requestId":"` + testNanoID1 + `","lane":"banana"}`)
	if _, err := conn.JetStream().Publish(ctx, "ops.default", bad); err != nil {
		t.Fatalf("publish bad: %v", err)
	}
	driveOne(t, ctx, cp, cons, OutcomeMalformed)
	if metrics.OpsMalformed.Load() != 1 {
		t.Fatalf("OpsMalformed = %d, want 1", metrics.OpsMalformed.Load())
	}

	// Health KV should carry the malformed-operation marker for the
	// recoverable requestId.
	healthKey := "health.processor.proc-test-" + testNanoID1 + ".malformed-operation." + testNanoID1
	if _, err := conn.KVGet(ctx, testHealthBucket, healthKey); err != nil {
		t.Fatalf("expected malformed-operation marker at %q, got %v", healthKey, err)
	}

	// Confirm the message did NOT redeliver: pull info from the
	// consumer and verify ack pending is zero after term.
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending != 0 {
		t.Fatalf("NumAckPending after term = %d, want 0", info.NumAckPending)
	}
	if info.NumPending != 0 {
		t.Fatalf("NumPending after term = %d, want 0", info.NumPending)
	}
}

// AC4: tracker-write-failure retry-safe — simulate commit failure by
// pre-creating the tracker key but the consumer call sees no tracker
// (race-window). The StubCommitter's AtomicBatch will be rejected
// (CreateOnly conflict); the commit path then re-probes and finds the
// tracker, acks, and emits a duplicate reply. Net: no double-commit.
func TestIntegration_TrackerWriteConflictIsAckedAsDuplicate(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	logger := testLogger()

	// Build a pipeline with a special committer that races: it pre-seeds
	// the tracker before its own AtomicBatch call, mimicking another
	// instance having committed first.
	authz := NewStubAuthorizer(logger)
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, testHealthBucket, "proc-test-"+testNanoID2, 10*time.Second, metrics, logger)
	racy := &racyCommitter{
		inner: NewStubCommitter(conn, testCoreBucket, logger, time.Now),
		conn:  conn,
	}
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  testCoreBucket,
		HealthKV:    testHealthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydrator(conn, testCoreBucket, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   &StubValidator{logger: logger},
		Committer:   racy,
		Events:      &StubEventPublisher{logger: logger},
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     testStream,
		Durable:        testDurable + "-racy",
		FilterSubjects: []string{"ops.default"},
		AckWait:        2 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}

	env := newTestEnvelope(testNanoID2)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeDuplicate)

	if racy.calls.Load() != 1 {
		t.Fatalf("racy committer should have been invoked exactly once, got %d", racy.calls.Load())
	}
	if metrics.OpsDuplicates.Load() != 1 {
		t.Fatalf("OpsDuplicates = %d, want 1", metrics.OpsDuplicates.Load())
	}

	// And the tracker is present exactly once (not double-written).
	if _, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID)); err != nil {
		t.Fatalf("tracker should exist after duplicate path: %v", err)
	}
}

// racyCommitter pre-seeds the tracker before delegating to the real
// committer, so the AtomicBatch CreateOnly conflict fires. Simulates a
// crash-after-commit / concurrent-redelivery race.
type racyCommitter struct {
	inner Committer
	conn  *substrate.Conn
	calls atomic.Uint64
}

func (r *racyCommitter) Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker) (CommitAck, error) {
	r.calls.Add(1)
	val, _ := tracker.Marshal()
	if _, err := r.conn.KVCreate(ctx, testCoreBucket, tracker.Key, val); err != nil && !errors.Is(err, substrate.ErrRevisionConflict) {
		return CommitAck{}, fmt.Errorf("racy pre-seed: %w", err)
	}
	return r.inner.Commit(ctx, env, result, tracker)
}

// ReplyShape: unit-level assertion that BuildAcceptedReply produces the
// Contract #2 §2.4 wire shape with the Story-1.5 `accepted-stub` decision
// marker. End-to-end request-reply through JetStream is deferred to
// Story 1.7 (handoff brief decision #11 — stub reply form only in 1.5).
func TestReplyShape_AcceptedStub(t *testing.T) {
	r := BuildAcceptedReply(testNanoID1, time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC))
	if r.Status != ReplyStatusAccepted {
		t.Fatalf("Status = %q", r.Status)
	}
	if r.Decision != "accepted-stub" {
		t.Fatalf("Decision = %q, want accepted-stub", r.Decision)
	}
	if r.OpTrackerKey != "vtx.op."+testNanoID1 {
		t.Fatalf("OpTrackerKey = %q", r.OpTrackerKey)
	}
	b, err := MarshalReply(r)
	if err != nil {
		t.Fatalf("MarshalReply: %v", err)
	}
	var back OperationReply
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if back.Decision != "accepted-stub" {
		t.Fatalf("round-trip Decision = %q", back.Decision)
	}
}

func TestReplyShape_Duplicate(t *testing.T) {
	env := newTestEnvelope(testNanoID1)
	tr := NewTracker(env, time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC))
	r := BuildDuplicateReply(testNanoID1, &tr)
	if r.Status != ReplyStatusDuplicate {
		t.Fatalf("Status = %q", r.Status)
	}
	if r.OriginalCommittedAt == "" {
		t.Fatalf("OriginalCommittedAt should be set from tracker")
	}
}

func TestReplyShape_Rejected(t *testing.T) {
	r := BuildRejectedReply(testNanoID1, ErrCodeAuthDenied, "stub denied", nil)
	if r.Status != ReplyStatusRejected || r.Error == nil || r.Error.Code != ErrCodeAuthDenied {
		t.Fatalf("rejected reply wrong: %+v", r)
	}
}
