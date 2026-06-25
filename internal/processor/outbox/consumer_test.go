package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

const testBucket = "core-kv"

// startEmbeddedNATS spins up an in-process JetStream-enabled NATS server.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
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

// setup provisions Core KV (atomic publish) + core-events and returns a conn.
func setup(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := startEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "outbox-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	js := conn.JetStream()

	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         testBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create core-kv: %v", err)
	}
	stream, err := js.Stream(ctx, "KV_"+testBucket)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable atomic publish: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               "core-events",
		Subjects:           []string{"events.>"},
		Retention:          jetstream.LimitsPolicy,
		MaxAge:             7 * 24 * time.Hour,
		AllowAtomicPublish: true,
	}); err != nil {
		t.Fatalf("provision core-events: %v", err)
	}
	return ctx, conn
}

// persistOutbox writes an outbox aspect directly (modeling the step-8 atomic
// batch having committed the events without the consumer yet publishing them —
// i.e. crash-between-commit-and-publish).
func persistOutbox(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string, events processor.EventList) {
	t.Helper()
	aspect := processor.NewOutboxAspect(requestID, "vtx.identity.actor", processor.TrackerKey(requestID),
		substrate.FormatTimestamp(time.Now()), events)
	b, err := aspect.Marshal()
	if err != nil {
		t.Fatalf("marshal aspect: %v", err)
	}
	if _, err := conn.KVPut(ctx, testBucket, aspect.Key, b); err != nil {
		t.Fatalf("put aspect: %v", err)
	}
}

// drainEvents counts and returns the messages observed on core-events.
func drainEvents(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) [][]byte {
	t.Helper()
	cons, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "core-events", jetstream.ConsumerConfig{
		Durable:        durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("event consumer: %v", err)
	}
	batch, _ := cons.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
	var out [][]byte
	for m := range batch.Messages() {
		out = append(out, m.Data())
		_ = m.Ack()
	}
	return out
}

// runConsumerUntil runs the consumer in a goroutine and cancels once cond
// holds (polled) or the deadline elapses.
func runConsumerUntil(t *testing.T, ctx context.Context, conn *substrate.Conn, cond func() bool) {
	t.Helper()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := New(conn, testBucket, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(cctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
}

// TestOutbox_CrashBetweenCommitAndPublish_RepublishesRealEvents is the AC-2
// regression: a committed-but-unpublished outbox aspect is published faithfully
// — byte-identical payload, original eventId — NOT a reconstruction.
func TestOutbox_CrashBetweenCommitAndPublish_RepublishesRealEvents(t *testing.T) {
	ctx, conn := setup(t)

	want := processor.Event{
		EventID:   "Hj4kPmRtw9nbCxz5vQ2y",
		RequestID: "Aj4kPmRtw9nbCxz5vQ2y",
		EventType: "identity.created",
		TargetKey: "vtx.identity.Bj4kPmRtw9nbCxz5vQ2y",
		Payload:   map[string]interface{}{"identityKey": "vtx.identity.Bj4kPmRtw9nbCxz5vQ2y", "name": "Andrew"},
		Timestamp: substrate.FormatTimestamp(time.Now()),
	}
	wantJSON, _ := json.Marshal(want)
	persistOutbox(t, ctx, conn, want.RequestID, processor.EventList{want})

	// Aspect present BEFORE publish.
	if _, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(want.RequestID)); err != nil {
		t.Fatalf("aspect should exist before publish: %v", err)
	}

	runConsumerUntil(t, ctx, conn, func() bool {
		_, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(want.RequestID))
		return errors.Is(err, substrate.ErrKeyNotFound)
	})

	got := drainEvents(t, ctx, conn, "crash-events")
	if len(got) < 1 {
		t.Fatalf("got %d events on core-events, want >= 1 (at-least-once)", len(got))
	}
	// Every delivered copy must be the REAL persisted event (byte-identical) —
	// proving faithful republish, not a reconstruction. Duplicates are permitted
	// (events are at-least-once per Contract #4 §4.4) but must be exact copies,
	// never a fabricated/empty-payload reconstruction.
	for i, g := range got {
		if string(g) != string(wantJSON) {
			t.Fatalf("event %d not byte-identical:\n got=%s\nwant=%s", i, g, wantJSON)
		}
	}

	// Aspect GONE after publish (tombstoned).
	if _, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(want.RequestID)); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("aspect should be tombstoned after publish, got err=%v", err)
	}
}

// TestOutbox_NoDoublePublish: a second consumer pass over the now-tombstoned
// aspect publishes nothing more.
func TestOutbox_NoDoublePublish(t *testing.T) {
	ctx, conn := setup(t)
	rid := "Cj4kPmRtw9nbCxz5vQ2y"
	ev := processor.Event{
		EventID:   "Dj4kPmRtw9nbCxz5vQ2y",
		RequestID: rid,
		EventType: "identity.created",
		Payload:   map[string]interface{}{"k": "v"},
		Timestamp: substrate.FormatTimestamp(time.Now()),
	}
	persistOutbox(t, ctx, conn, rid, processor.EventList{ev})

	runConsumerUntil(t, ctx, conn, func() bool {
		_, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(rid))
		return errors.Is(err, substrate.ErrKeyNotFound)
	})
	// First pass published the event at-least-once.
	got1 := drainEvents(t, ctx, conn, "nodouble-events")
	if len(got1) < 1 {
		t.Fatalf("first pass: got %d events, want >= 1", len(got1))
	}
	// Second pass: the aspect is tombstoned; nothing new must be published. The
	// same durable resumes from its offset, so a second drain sees ONLY events
	// published since got1 — which must be zero.
	runConsumerUntil(t, ctx, conn, func() bool { return false })
	got2 := drainEvents(t, ctx, conn, "nodouble-events")
	if len(got2) != 0 {
		t.Fatalf("second pass over tombstoned aspect published %d new events, want 0", len(got2))
	}
}

// TestOutbox_EmptyBodySkipped: a tombstone/PURGE marker (empty body) is acked
// and skipped without error.
func TestOutbox_EmptyBodySkipped(t *testing.T) {
	ctx, conn := setup(t)
	rid := "Ej4kPmRtw9nbCxz5vQ2y"
	persistOutbox(t, ctx, conn, rid, processor.EventList{{
		EventID: "Fj4kPmRtw9nbCxz5vQ2y", RequestID: rid, EventType: "x.y",
		Payload: map[string]interface{}{}, Timestamp: substrate.FormatTimestamp(time.Now()),
	}})
	// Delete it first → the consumer sees the tombstone (empty body) and the put.
	if err := conn.KVDelete(ctx, testBucket, processor.OutboxAspectKey(rid)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// The consumer must drain both the put and the tombstone without panicking.
	runConsumerUntil(t, ctx, conn, func() bool { return false })
	// No assertion on event count here beyond "no crash"; the aspect was
	// deleted before publish so at most one event may have raced through.
}
