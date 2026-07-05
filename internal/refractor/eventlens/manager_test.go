package eventlens

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// testHarness wires a real (embedded) guarded NatsKVAdapter + a Manager
// against it, for handle()-level convergence tests that don't need a real
// JetStream stream/consumer.
func testHarness(t *testing.T) (*Manager, *adapter.NatsKVAdapter, *substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "orchestration-history"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, "orchestration-history")
	require.NoError(t, err)

	nkv, err := adapter.New(kv, []string{"instance_id"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	nkv.SetGuarded(true)

	mgr, err := New(Config{
		Conn:         conn,
		EventsStream: "core-events",
		Subject:      "events.loom.>",
		Durable:      "refractor-test-lens",
		KeyField:     "instance_id",
		Project:      testProjection(),
		Adapter:      nkv,
	})
	require.NoError(t, err)
	return mgr, nkv, conn, ctx
}

func eventMsg(t *testing.T, eventType string, payload map[string]any, seq uint64) substrate.Message {
	t.Helper()
	body, err := json.Marshal(Event{EventType: eventType, Payload: payload, Timestamp: "2026-07-05T10:00:00Z"})
	require.NoError(t, err)
	return substrate.Message{Body: body, Sequence: seq}
}

func TestManagerHandle_StartedThenCompleted_Converges(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)

	d := mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-1", "patternRef": "onboarding-v1", "subjectKey": "identity.1",
	}, 1))
	require.Equal(t, substrate.Ack, d)

	d = mgr.handle(ctx, eventMsg(t, "loom.patternCompleted", map[string]any{"instanceId": "inst-1"}, 2))
	require.Equal(t, substrate.Ack, d)

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-1"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "complete", row["status"])
	require.Equal(t, "onboarding-v1", row["pattern_ref"], "pattern_ref set by the started event must survive the completed event's write")
	require.Equal(t, "2026-07-05T10:00:00Z", row["started_at"])
	require.Equal(t, "2026-07-05T10:00:00Z", row["ended_at"])
	require.Equal(t, float64(2), row["last_event_seq"])
}

func TestManagerHandle_Failed_SetsReasonAndFailedStatus(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)

	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-2", "patternRef": "p", "subjectKey": "s",
	}, 1)))
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternFailed", map[string]any{
		"instanceId": "inst-2", "reason": "vendor timeout",
	}, 2)))

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-2"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "failed", row["status"])
	require.Equal(t, "vendor timeout", row["failure_reason"])
}

// TestManagerHandle_OutOfOrderReplay_DoesNotClobberTerminal proves the design's
// §2.4 correctness core: a redelivered/replayed lower-seq patternStarted
// arriving AFTER a higher-seq patternCompleted must not resurrect "running" —
// the adapter's monotonic guard rejects the whole write.
func TestManagerHandle_OutOfOrderReplay_DoesNotClobberTerminal(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)

	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-3", "patternRef": "p", "subjectKey": "s",
	}, 10)))
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternCompleted", map[string]any{
		"instanceId": "inst-3",
	}, 20)))

	// A replayed (redelivered) started event at a LOWER seq than the stored
	// watermark — e.g. a rebuild/DeliverAll replay racing the live tail.
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-3", "patternRef": "p", "subjectKey": "s",
	}, 10)))

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-3"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "complete", row["status"], "a lower-seq replay must never resurrect a terminal row to running")
	require.Equal(t, float64(20), row["last_event_seq"])
}

func TestManagerHandle_UnmappedEventType_Terms(t *testing.T) {
	mgr, _, _, ctx := testHarness(t)
	d := mgr.handle(ctx, eventMsg(t, "loom.somethingElse", map[string]any{"instanceId": "inst-4"}, 1))
	require.Equal(t, substrate.Term, d, "a poison event (unmapped type) must Term, never nak-loop")
}

func TestManagerHandle_UnparseableBody_Terms(t *testing.T) {
	mgr, _, _, ctx := testHarness(t)
	d := mgr.handle(ctx, substrate.Message{Body: []byte("not json"), Sequence: 1})
	require.Equal(t, substrate.Term, d)
}

func TestManagerHandle_EmptyBody_Acks(t *testing.T) {
	mgr, _, _, ctx := testHarness(t)
	d := mgr.handle(ctx, substrate.Message{Body: nil, Sequence: 1})
	require.Equal(t, substrate.Ack, d)
}

// TestManagerHandle_ZeroSequence_RetriesInsteadOfSilentlyAcking proves a
// metadata-read failure (Sequence left at its zero value) is retried, not
// Acked with nothing written — an eventStream lens's event is the only copy
// of its contribution (unlike a coreKV lens, which can rebuild from Core-KV),
// so silently accepting a seq-0 delivery would permanently lose it.
func TestManagerHandle_ZeroSequence_RetriesInsteadOfSilentlyAcking(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)
	d := mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-zero-seq", "patternRef": "p", "subjectKey": "s",
	}, 0))
	require.Equal(t, substrate.NakWithDelay, d)

	_, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-zero-seq"})
	require.NoError(t, err)
	require.False(t, ok, "a seq-0 delivery must not be silently accepted as if written")
}

// TestManager_Run_EndToEnd is Fire 1's dark-primitive integration proof: a
// throwaway test lens over a synthetic core-events-shaped stream/subject — no
// production lens involved. Proves the full wire path (durable consumer
// creation, subject filter, DeliverAll backfill) converges a real published
// sequence, not just the handle() function in isolation.
func TestManager_Run_EndToEnd(t *testing.T) {
	mgr, nkv, conn, ctx := testHarness(t)

	_, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-events", Subjects: []string{"events.>"},
	})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	started, err := json.Marshal(Event{EventType: "loom.patternStarted", Payload: map[string]any{
		"instanceId": "inst-e2e", "patternRef": "onboarding-v1", "subjectKey": "identity.1",
	}, Timestamp: "2026-07-05T10:00:00Z"})
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, "events.loom.patternStarted", started, nil))

	completed, err := json.Marshal(Event{EventType: "loom.patternCompleted", Payload: map[string]any{
		"instanceId": "inst-e2e",
	}, Timestamp: "2026-07-05T10:01:00Z"})
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, "events.loom.patternCompleted", completed, nil))

	require.Eventually(t, func() bool {
		row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-e2e"})
		if err != nil || !ok {
			return false
		}
		return row["status"] == "complete"
	}, 5*time.Second, 50*time.Millisecond, "row must converge to complete via the real durable consumer")

	cancel()
	<-done
}
