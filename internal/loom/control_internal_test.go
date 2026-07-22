package loom

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func controlTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newControlTestConn spins up an in-process JetStream server and provisions the
// loom-state bucket (atomic-publish, TTL marker) the engine's state store writes.
// It returns a connection and a context bounded to the test.
func newControlTestConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "loom-state", LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	stream, err := js.Stream(ctx, "KV_loom-state")
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, cfg)
	require.NoError(t, err)
	return conn, ctx
}

// newControlEngine constructs an engine over conn with the control-test buckets.
// It does not Start the engine — the read methods (ListInstances/InspectInstance)
// scan loom-state directly, so no running consumers are needed.
func newControlEngine(conn *substrate.Conn) *Engine {
	return NewEngine(conn, Config{
		CoreKVBucket:    "core-kv",
		LoomStateBucket: "loom-state",
		EventsStream:    "core-events",
		ActorKey:        "vtx.identity.LoomCtrlActor123",
		Lane:            "system",
		Logger:          controlTestLogger(),
	})
}

// putInstance writes an instance record directly (bypassing createInstance, which
// always co-writes a pin) so a test can craft a running-without-pin or
// cursor-out-of-range record.
func putInstance(t *testing.T, ctx context.Context, conn *substrate.Conn, inst Instance) {
	t.Helper()
	body, err := json.Marshal(inst)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, "loom-state", instanceKey(inst.InstanceID), body)
	require.NoError(t, err)
}

// putPin writes an instance's pattern pin directly.
func putPin(t *testing.T, ctx context.Context, conn *substrate.Conn, instanceID string, p Pattern) {
	t.Helper()
	body, err := json.Marshal(&p)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, "loom-state", patternPinKey(instanceID), body)
	require.NoError(t, err)
}

// TestListInstances_SeededState proves ListInstances returns running instances
// and retained terminals, filters out the .pattern pin sub-keys, and sorts by
// instanceId.
func TestListInstances_SeededState(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	putInstance(t, ctx, conn, Instance{InstanceID: "aaa1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 0, Status: StatusRunning})
	putInstance(t, ctx, conn, Instance{InstanceID: "ccc3", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w3", Cursor: 2, Status: StatusComplete, RetryCount: 0})
	putInstance(t, ctx, conn, Instance{InstanceID: "bbb2", PatternRef: "vtx.meta.p2", SubjectKey: "vtx.widget.w2", Cursor: 1, Status: StatusFailed, RetryCount: 1})
	// A pin sub-key under a live instance — must be filtered out (not counted as
	// an instance record).
	putPin(t, ctx, conn, "aaa1", Pattern{PatternID: "p1", SubjectType: "widget", Steps: []Step{{Kind: StepKindSystemOp, Operation: "StepA"}}})

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3, "running + retained terminals, pin sub-key excluded")

	// Sorted by instanceId.
	require.Equal(t, "aaa1", got[0].InstanceID)
	require.Equal(t, "bbb2", got[1].InstanceID)
	require.Equal(t, "ccc3", got[2].InstanceID)

	require.Equal(t, StatusRunning, got[0].Status)
	require.Equal(t, StatusFailed, got[1].Status)
	require.Equal(t, 1, got[1].RetryCount)
	require.Equal(t, StatusComplete, got[2].Status)
	require.Equal(t, "vtx.meta.p2", got[1].PatternRef)
	require.Equal(t, 1, got[1].Cursor)
}

// TestListInstances_Empty proves an empty loom-state yields an empty list, not an
// error.
func TestListInstances_Empty(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestInspectInstance_Running proves a running instance resolves its current step
// from the pinned pattern at the cursor.
func TestInspectInstance_Running(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	pat := Pattern{PatternID: "p1", SubjectType: "widget", Steps: []Step{
		{Kind: StepKindSystemOp, Operation: "StepA"},
		{Kind: StepKindUserTask, Operation: "StepB"},
	}}
	putInstance(t, ctx, conn, Instance{InstanceID: "run1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 1, Status: StatusRunning})
	putPin(t, ctx, conn, "run1", pat)

	d, err := e.InspectInstance(ctx, "run1")
	require.NoError(t, err)
	require.False(t, d.Terminal)
	require.NotNil(t, d.CurrentStep)
	require.Equal(t, StepKindUserTask, d.CurrentStep.Kind)
	require.Equal(t, "StepB", d.CurrentStep.Operation)
	require.Equal(t, 1, d.Instance.Cursor)
}

// TestInspectInstance_Terminal proves a terminal instance reports terminal=true
// with a nil current step and does NOT require a pin (the pin is deleted at
// terminal).
func TestInspectInstance_Terminal(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	// Terminal record with NO pin (mirrors production: the pin is deleted in the
	// terminal batch). Inspect must not error on the absent pin.
	putInstance(t, ctx, conn, Instance{InstanceID: "done1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 2, Status: StatusComplete})

	d, err := e.InspectInstance(ctx, "done1")
	require.NoError(t, err)
	require.True(t, d.Terminal)
	require.Nil(t, d.CurrentStep)
	require.Equal(t, StatusComplete, d.Instance.Status)
}

// TestInspectInstance_MissingPinOnRunning proves a running instance whose pin is
// genuinely absent (and stays running on re-read) surfaces the invariant-break
// error rather than panicking.
func TestInspectInstance_MissingPinOnRunning(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	// Running record, NO pin written — the read-tearing re-read still sees running,
	// so this is the genuine invariant break.
	putInstance(t, ctx, conn, Instance{InstanceID: "nopin1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 0, Status: StatusRunning})

	_, err := e.InspectInstance(ctx, "nopin1")
	require.Error(t, err)
	require.ErrorIs(t, err, errPatternPinMissing)
}

// TestInspectInstance_CursorOutOfRange proves a running instance whose cursor
// indexes at or past the pinned pattern's step count surfaces a typed error
// rather than an index-out-of-range panic. Both the far-past case (cursor > len)
// and the EXACT boundary (cursor == len, the terminal-cursor position a running
// record must never sit at) are corrupt and must be reported, not panicked.
func TestInspectInstance_CursorOutOfRange(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		steps  int
		cursor int
	}{
		{"farPastEnd", 1, 5},    // cursor 5, 1 step — well off the end
		{"exactBoundary", 2, 2}, // cursor == len(steps): the terminal-cursor index
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			conn, ctx := newControlTestConn(t)
			e := newControlEngine(conn)

			steps := make([]Step, tc.steps)
			for i := range steps {
				steps[i] = Step{Kind: StepKindSystemOp, Operation: "Step"}
			}
			pat := Pattern{PatternID: "p1", SubjectType: "widget", Steps: steps}
			putInstance(t, ctx, conn, Instance{InstanceID: tc.name, PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: tc.cursor, Status: StatusRunning})
			putPin(t, ctx, conn, tc.name, pat)

			_, err := e.InspectInstance(ctx, tc.name)
			require.Error(t, err)
			require.Contains(t, err.Error(), "out of range")
		})
	}
}

// TestInspectResolved_RereadDeletedClearsStatus proves the read-tearing branch:
// when the pin is absent for a record the caller believed running, but the
// re-read finds the record gone entirely (reread == nil), inspect reports
// Terminal=true and does NOT echo the now-untrue "running" status — it returns
// the empty-string sentinel rather than an inconsistent terminal:true /
// status:running pair. Driven by passing inspectResolved a running summary for an
// instanceId that is absent from the store: no pin (errPatternPinMissing) and the
// re-read returns nil.
func TestInspectResolved_RereadDeletedClearsStatus(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	// inst is a believed-running snapshot whose record does not exist in the store
	// — modelling the cursor record having been deleted between the original read
	// and the pin read. No pin is written, so getPinnedPattern returns
	// errPatternPinMissing; the re-read getInstance returns nil.
	inst := &Instance{InstanceID: "vanished1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 0, Status: StatusRunning}

	d, err := e.inspectResolved(ctx, inst)
	require.NoError(t, err)
	require.True(t, d.Terminal)
	require.Nil(t, d.CurrentStep)
	require.Equal(t, "", d.Instance.Status, "a vanished record must not echo the stale running status")
	require.Equal(t, "vanished1", d.Instance.InstanceID, "identity fields are still carried")
}

// TestInspectInstance_NotFound proves a missing instance is a not-found error.
func TestInspectInstance_NotFound(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	_, err := e.InspectInstance(ctx, "ghost")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestPauseResumeConsumer_ValidateThenToggle proves the engine-layer guards
// (C3): an unknown name is rejected, the relay/deadline consumers are rejected by
// pause but resume is unrestricted, and a managed completion/trigger consumer
// pauses and resumes. The three fixed consumers register at engine Start.
func TestPauseResumeConsumer_ValidateThenToggle(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	// The fixed consumers attach to core-events and the loom-state backing stream;
	// provision both so Start can add them.
	js := conn.JetStream()
	_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv", LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "health-kv"})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-events", Subjects: []string{"events.>"},
		Retention: jetstream.LimitsPolicy, MaxAge: time.Hour,
	})
	require.NoError(t, err)

	e := NewEngine(conn, Config{
		CoreKVBucket:    "core-kv",
		LoomStateBucket: "loom-state",
		EventsStream:    "core-events",
		HealthKVBucket:  "health-kv",
		ActorKey:        "vtx.identity.LoomCtrlActor123",
		Lane:            "system",
		HeartbeatEvery:  time.Hour, // suppress heartbeat churn in this test
		Logger:          controlTestLogger(),
	})

	startCtx, cancelStart := context.WithCancel(ctx)
	startErr := make(chan error, 1)
	go func() { startErr <- e.Start(startCtx) }()
	t.Cleanup(func() {
		cancelStart()
		select {
		case <-startErr:
		case <-time.After(10 * time.Second):
			t.Error("engine did not shut down within 10s")
		}
	})

	// Wait for the three fixed consumers to register in the supervisor.
	require.True(t, waitForCond(5*time.Second, func() bool {
		return e.supervisor.IsManaged(triggerDurable) &&
			e.supervisor.IsManaged(relayDurable) &&
			e.supervisor.IsManaged(deadlineDurable)
	}), "fixed consumers should register at Start")

	// Unknown name → not-managed error (pause AND resume).
	_, err = e.PauseConsumer(ctx, "loom-nope")
	require.ErrorIs(t, err, errConsumerNotManaged)
	err = e.ResumeConsumer(ctx, "loom-nope")
	require.ErrorIs(t, err, errConsumerNotManaged)

	// Relay + deadline are dispatch/crash-safety critical → pause forbidden.
	_, err = e.PauseConsumer(ctx, relayDurable)
	require.ErrorIs(t, err, errConsumerPauseForbidden)
	_, err = e.PauseConsumer(ctx, deadlineDurable)
	require.ErrorIs(t, err, errConsumerPauseForbidden)

	// Resume IS unrestricted over managed names — including relay/deadline (it can
	// recover an out-of-band pause). No error.
	require.NoError(t, e.ResumeConsumer(ctx, relayDurable))
	require.NoError(t, e.ResumeConsumer(ctx, deadlineDurable))

	// A managed completion/trigger consumer pauses then resumes cleanly. The
	// trigger pause carries only the persist-across-restart note — pausing the
	// trigger stops new instances starting but stalls nothing already in flight.
	note, err := e.PauseConsumer(ctx, triggerDurable)
	require.NoError(t, err)
	require.Contains(t, note, pauseRestartNote)
	require.NotContains(t, note, pauseDomainStallNote)
	require.NoError(t, e.ResumeConsumer(ctx, triggerDurable))

	// A per-domain completion consumer (loom-<domain>) pauses with BOTH the
	// persist-across-restart note and the in-flight-stall warning: with this
	// consumer paused, completions for the domain are no longer drained, so any
	// in-flight instance awaiting that domain holds until resume.
	require.NoError(t, e.supervisor.Add(ctx, e.domainSpec("widget")))
	require.True(t, e.supervisor.IsManaged("loom-widget"), "domain consumer should register")
	dnote, err := e.PauseConsumer(ctx, "loom-widget")
	require.NoError(t, err)
	require.Contains(t, dnote, pauseRestartNote)
	require.Contains(t, dnote, pauseDomainStallNote)
	require.NoError(t, e.ResumeConsumer(ctx, "loom-widget"))

	// ListConsumers reports ALL managed consumers (including relay/deadline) — the
	// read-only op is not restricted.
	cs, err := e.ListConsumers(ctx)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, c := range cs {
		names[c.Name] = true
	}
	require.True(t, names[triggerDurable])
	require.True(t, names[relayDurable])
	require.True(t, names[deadlineDurable])
}

// waitForCond polls cond until true or the deadline.
func waitForCond(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
