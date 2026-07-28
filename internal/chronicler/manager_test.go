package chronicler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// testProjection mirrors packages/orchestration-base's production
// loomFlowHistorySource lens byte-for-byte, including the ClearOn wiring on
// ended_at/failure_reason (loupe-flows-edge-depth-ux.md §1.2).
func testProjection() *EventProjection {
	return &EventProjection{
		Key: "payload.instanceId",
		Columns: map[string]ColumnMapping{
			"instance_id": {Path: "payload.instanceId"},
			"pattern_ref": {Path: "payload.patternRef"},
			"status": {
				From: "eventType",
				Map: map[string]string{
					"loom.patternStarted":   "running",
					"loom.patternCompleted": "complete",
					"loom.patternFailed":    "failed",
				},
			},
			"failure_reason": {Path: "payload.reason", ClearOn: []string{"loom.patternStarted"}},
			"started_at":     {When: []string{"loom.patternStarted"}, Value: "timestamp"},
			"ended_at": {
				When:    []string{"loom.patternCompleted", "loom.patternFailed"},
				Value:   "timestamp",
				ClearOn: []string{"loom.patternStarted"},
			},
			"last_event_seq": {Path: "message.sequence"},
		},
	}
}

// testHarness wires a real (embedded) guarded NatsKVAdapter + a Manager
// against it, for handle()-level convergence tests that don't need a real
// JetStream stream/consumer.
func testHarness(t *testing.T) (*Manager, *adapter.NatsKVAdapter, *substrate.Conn, context.Context) {
	t.Helper()
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
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

	mgr, err := NewManager(ManagerConfig{
		Conn:         conn,
		EventsStream: "core-events",
		Subject:      "events.loom.>",
		Durable:      "chronicler-test-lens",
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

func TestManagerHandle_OutOfOrderReplay_DoesNotClobberTerminal(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)

	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-3", "patternRef": "p", "subjectKey": "s",
	}, 10)))
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternCompleted", map[string]any{
		"instanceId": "inst-3",
	}, 20)))
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-3", "patternRef": "p", "subjectKey": "s",
	}, 10)))

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-3"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "complete", row["status"], "a lower-seq replay must never resurrect a terminal row to running")
	require.Equal(t, float64(20), row["last_event_seq"])
}

// TestManagerHandle_RedispatchClearsTerminalColumns pins the
// loupe-flows-edge-depth-ux.md §1.2 fix: Weaver's stable instanceId means a
// re-dispatch's patternStarted collapses onto an already-terminal instance.
// Without ClearOn, ended_at/failure_reason from the PRIOR run would carry
// forward onto the new running row (ended_at before started_at, a stale
// failure_reason on a healthy flow). The re-dispatch must drop both.
func TestManagerHandle_RedispatchClearsTerminalColumns(t *testing.T) {
	mgr, nkv, _, ctx := testHarness(t)

	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-redispatch", "patternRef": "p", "subjectKey": "s",
	}, 1)))
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternFailed", map[string]any{
		"instanceId": "inst-redispatch", "reason": "step timed out",
	}, 2)))

	row, ok, err := nkv.GetRow(ctx, map[string]any{"instance_id": "inst-redispatch"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "failed", row["status"])
	require.Equal(t, "step timed out", row["failure_reason"])
	require.Equal(t, "2026-07-05T10:00:00Z", row["ended_at"])

	// Re-dispatch: a fresh patternStarted for the same (stable) instanceId.
	require.Equal(t, substrate.Ack, mgr.handle(ctx, eventMsg(t, "loom.patternStarted", map[string]any{
		"instanceId": "inst-redispatch", "patternRef": "p", "subjectKey": "s",
	}, 3)))

	row, ok, err = nkv.GetRow(ctx, map[string]any{"instance_id": "inst-redispatch"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "running", row["status"])
	require.Nil(t, row["ended_at"], "the prior run's ended_at must not survive a re-dispatch onto the same instance")
	require.Nil(t, row["failure_reason"], "the prior run's failure_reason must not survive a re-dispatch onto the same instance")
	require.Equal(t, "p", row["pattern_ref"], "columns with no ClearOn keep carrying forward as before")
}

func TestManagerHandle_UnmappedEventType_Terms(t *testing.T) {
	mgr, _, _, ctx := testHarness(t)
	d := mgr.handle(ctx, eventMsg(t, "loom.somethingElse", map[string]any{"instanceId": "inst-4"}, 1))
	require.Equal(t, substrate.Term, d, "a poison event (unmapped type) must Term, never nak-loop")
}

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

// TestManager_Run_EndToEnd proves the full wire path (durable consumer
// creation, subject filter, DeliverAll backfill) converges a real published
// sequence, not just the handle() function in isolation — the same proof
// internal/refractor/eventlens's Fire 1 pinned, now against this package's
// port.
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
