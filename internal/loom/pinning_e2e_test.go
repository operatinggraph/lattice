package loom_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/substrate"
)

// opRecorder collects the operationType of every op published to ops.system, in
// publish order, via a plain NATS subscription (a JetStream publish is also a
// core NATS publish). stepOps filters to the Step* fixture ops so lifecycle ops
// (StartLoomPattern/CompletePattern/CreateTask) don't clutter the assertion.
type opRecorder struct {
	mu  sync.Mutex
	ops []string
}

func newOpRecorder(t *testing.T, conn *substrate.Conn) *opRecorder {
	t.Helper()
	r := &opRecorder{}
	sub, err := conn.NATS().Subscribe("ops.system", func(msg *nats.Msg) {
		var env struct {
			OperationType string `json:"operationType"`
		}
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		r.mu.Lock()
		r.ops = append(r.ops, env.OperationType)
		r.mu.Unlock()
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return r
}

func (r *opRecorder) stepOps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, op := range r.ops {
		if strings.HasPrefix(op, "Step") {
			out = append(out, op)
		}
	}
	return out
}

// waitStepOps polls until the recorded Step* sequence equals want (publish
// order is deterministic here: each instance is driven to completion before the
// next is triggered).
func (r *opRecorder) waitStepOps(t *testing.T, want []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got := r.stepOps()
		if len(got) >= len(want) {
			require.Equal(t, want, got)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, want, r.stepOps())
}

// TestPinningE2E_MidFlightPatternEditRunsPinnedDefinition proves the binding
// policy: the definition binds at instance start. An in-flight instance whose
// pattern is updated mid-flight (steps inserted AND reordered AND a guard
// changed) completes under its PINNED definition — the exact old step sequence
// executes — while a NEW instance triggered after the update runs the NEW
// definition (including the changed guard's skip).
func TestPinningE2E_MidFlightPatternEditRunsPinnedDefinition(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	gate := make(chan struct{})
	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		gate:      gate,
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)
	rec := newOpRecorder(t, conn)

	patternID := mustNanoID(t)
	// OLD definition: StepA, then StepB guarded on name being ABSENT (true for a
	// bare subject → StepB runs), then StepC.
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps: []loom.Step{
			{Kind: "systemOp", Operation: "StepA"},
			{Kind: "systemOp", Operation: "StepB", Guard: json.RawMessage(`{"absent":"subject.profile.data.name"}`)},
			{Kind: "systemOp", Operation: "StepC"},
		},
	})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	// --- In-flight instance under the OLD definition, parked on StepA (the gate
	// holds its completion event). ---
	subject1 := "vtx.identity." + mustNanoID(t)
	instance1 := submitStartLoomPattern(t, ctx, conn, patternID, subject1)
	waitPending(t, ctx, conn, instance1)

	// The pin exists while the instance is live.
	_, err = conn.KVGet(ctx, loomStateBucket, "instance."+instance1+".pattern")
	require.NoError(t, err, "pattern pin must be written atomically with the instance")

	// --- Mid-flight pattern edit: insert StepX at the head, reorder StepC ahead
	// of StepA, and INVERT StepB's guard (absent → present: false for a bare
	// subject → StepB is now skipped). ---
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps: []loom.Step{
			{Kind: "systemOp", Operation: "StepX"},
			{Kind: "systemOp", Operation: "StepC"},
			{Kind: "systemOp", Operation: "StepB", Guard: json.RawMessage(`{"present":"subject.profile.data.name"}`)},
			{Kind: "systemOp", Operation: "StepA"},
		},
	})
	time.Sleep(700 * time.Millisecond) // let the CDC update register

	// Release StepA's completion: the in-flight instance must advance under its
	// PINNED definition — StepB (old guard true) then StepC — never StepX.
	close(gate)
	inst1 := waitInstanceStatus(t, ctx, conn, instance1, "complete")
	require.Equal(t, 3, inst1.Cursor, "pinned definition has 3 steps")
	rec.waitStepOps(t, []string{"StepA", "StepB", "StepC"})

	// The terminal batch deleted the pin.
	_, err = conn.KVGet(ctx, loomStateBucket, "instance."+instance1+".pattern")
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "terminal batch must delete the pattern pin")

	// --- A NEW instance runs the NEW definition: StepX, StepC, StepB skipped
	// (changed guard), StepA. ---
	subject2 := "vtx.identity." + mustNanoID(t)
	instance2 := submitStartLoomPattern(t, ctx, conn, patternID, subject2)
	inst2 := waitInstanceStatus(t, ctx, conn, instance2, "complete")
	require.Equal(t, 4, inst2.Cursor, "new definition has 4 steps")
	rec.waitStepOps(t, []string{
		"StepA", "StepB", "StepC", // instance 1 under the pinned (old) definition
		"StepX", "StepC", "StepA", // instance 2 under the new definition; StepB guard-skipped
	})
}

// TestPinningE2E_UpdatedAwayDomainSurvivesUntilDrain proves the reconcile
// union: when an in-flight instance's pattern is updated so its completion
// domain is no longer referenced by any current pattern, the loom-<domain>
// consumer survives (the live instance's pinned pattern keeps it in the desired
// set), the instance still completes under its pinned definition, and once it
// completes (pin deleted) the terminal-triggered reconcile tears the drained
// domain consumer down.
func TestPinningE2E_UpdatedAwayDomainSurvivesUntilDrain(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	gate := make(chan struct{})
	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		gate:      gate,
		eventFor:  func(string) string { return "lease.stepDone" },
	}
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "lease",
		CompletionDomains: []string{"lease"},
		Steps:             []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx.lease." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	waitPending(t, ctx, conn, instanceID)
	requireConsumer(t, ctx, conn, "loom-lease", true)

	// Update the pattern away from the lease domain: no current pattern
	// references it any more — only the in-flight instance's pin does.
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "lease",
		CompletionDomains: []string{"identity"},
		Steps:             []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})
	time.Sleep(700 * time.Millisecond) // let the CDC update + reconcile run

	requireConsumer(t, ctx, conn, "loom-lease", true)    // union keeps it for the live instance
	requireConsumer(t, ctx, conn, "loom-identity", true) // new definition's domain added

	// Release the completion: it arrives on events.lease.> — deliverable only
	// because loom-lease survived — and the instance completes under its pin.
	close(gate)
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor)

	// Pin deleted at terminal; the terminal-triggered reconcile drains the
	// now-unreferenced lease consumer.
	_, err = conn.KVGet(ctx, loomStateBucket, "instance."+instanceID+".pattern")
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
	waitConsumerGone(t, ctx, conn, "loom-lease")
	requireConsumer(t, ctx, conn, "loom-identity", true) // still referenced by the current definition
}

// TestPinningE2E_RedeliveredTriggerResumesStepZero proves the wedge recovery on
// the trigger path: an instance whose createInstance batch committed (instance +
// pin) but whose handler crashed before submitting step 0 (status=running,
// pendingToken empty) is RESUMED by a redelivered trigger — step 0 runs from the
// PINNED pattern and the instance completes. The pattern is deliberately NOT
// installed in the live source, which also proves (a) the startup-seed
// reconcile attaches the pinned domain's consumer with zero loaded patterns and
// (b) the resume resolves steps from the pin, never the live source.
func TestPinningE2E_RedeliveredTriggerResumesStepZero(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	instanceID := mustNanoID(t)
	patternID := mustNanoID(t)
	patternRef := "vtx.meta." + patternID
	subjectKey := "vtx.identity." + mustNanoID(t)
	pattern := loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	}

	// Manufacture the crash residue: the createInstance batch's two keys, with
	// no pending token (the step-0 transition never committed).
	instBody, err := json.Marshal(loom.Instance{
		InstanceID: instanceID,
		PatternRef: patternRef,
		SubjectKey: subjectKey,
		Status:     "running",
	})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, loomStateBucket, "instance."+instanceID, instBody)
	require.NoError(t, err)
	pinBody, err := json.Marshal(pattern)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, loomStateBucket, "instance."+instanceID+".pattern", pinBody)
	require.NoError(t, err)

	engine := newEngine(conn)
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	// The startup seed must attach loom-identity from the pin alone — no live
	// pattern exists to fire a source callback.
	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-identity")
	}), "startup-seed reconcile must attach the pinned domain's consumer")

	// Redeliver the trigger (the at-least-once delivery the crashed handler
	// Nak'd / never acked).
	ev, err := json.Marshal(map[string]any{
		"eventId":   mustNanoID(t),
		"requestId": instanceID,
		"eventType": "loom.patternStarted",
		"payload": map[string]any{
			"instanceId": instanceID,
			"patternRef": patternRef,
			"subjectKey": subjectKey,
		},
		"timestamp": substrate.FormatTimestamp(time.Now()),
	})
	require.NoError(t, err)
	_, err = conn.JetStream().Publish(ctx, "events.loom.patternStarted", ev)
	require.NoError(t, err)

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor, "the resumed step 0 ran to exhaustion")
	_, err = conn.KVGet(ctx, loomStateBucket, "instance."+instanceID+".pattern")
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "terminal batch must delete the pin")
}

// TestPinningE2E_RedeliveredTriggerMissingPinFails proves resumeStepZero's own
// pattern-pin-missing branch: unlike TestPinningE2E_MissingPinFailsInstance
// (which destroys an already-pinned instance's pin mid-flight, hit via
// advance), this manufactures a redelivered patternStarted trigger whose
// instance record exists (status=running, pendingToken empty — the
// createInstance-committed-but-step-0-never-submitted crash residue) but
// whose pin was NEVER written at all. resumeStepZero must fail the instance
// (operator-visible terminal), not Nak-loop forever.
func TestPinningE2E_RedeliveredTriggerMissingPinFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	instanceID := mustNanoID(t)
	patternID := mustNanoID(t)
	patternRef := "vtx.meta." + patternID
	subjectKey := "vtx.identity." + mustNanoID(t)

	// Manufacture the crash residue with NO pin written — the createInstance
	// batch itself never committed (or was rolled back out-of-band), leaving
	// only the instance record.
	instBody, err := json.Marshal(loom.Instance{
		InstanceID: instanceID,
		PatternRef: patternRef,
		SubjectKey: subjectKey,
		Status:     "running",
	})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, loomStateBucket, "instance."+instanceID, instBody)
	require.NoError(t, err)

	engine := newEngine(conn)
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	ev, err := json.Marshal(map[string]any{
		"eventId":   mustNanoID(t),
		"requestId": instanceID,
		"eventType": "loom.patternStarted",
		"payload": map[string]any{
			"instanceId": instanceID,
			"patternRef": patternRef,
			"subjectKey": subjectKey,
		},
		"timestamp": substrate.FormatTimestamp(time.Now()),
	})
	require.NoError(t, err)
	_, err = conn.JetStream().Publish(ctx, "events.loom.patternStarted", ev)
	require.NoError(t, err)

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "failed")
	require.Equal(t, "", inst.PendingToken, "the failed terminal clears the pending token")
}

// TestPinningE2E_MissingPinFailsInstance proves the poison-wedge guard: a
// RUNNING instance whose pin is deleted out-of-band (an invariant break no
// retry can repair) is failed — an operator-visible terminal — instead of
// Nak-looping its completion forever.
func TestPinningE2E_MissingPinFailsInstance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	gate := make(chan struct{})
	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		gate:      gate,
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps: []loom.Step{
			{Kind: "systemOp", Operation: "StepA"},
			{Kind: "systemOp", Operation: "StepB"},
		},
	})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	waitPending(t, ctx, conn, instanceID)

	// Destroy the pin out-of-band while StepA's completion is gated.
	require.NoError(t, conn.KVDelete(ctx, loomStateBucket, "instance."+instanceID+".pattern"))

	// Release StepA's completion: advance finds the pin gone and must fail the
	// instance (terminal), not redeliver forever.
	close(gate)
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "failed")
	require.Equal(t, "", inst.PendingToken, "the failed terminal clears the pending token")
}

// requireConsumer asserts the presence (or absence) of a durable consumer on
// the core-events stream.
func requireConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, name string, want bool) {
	t.Helper()
	_, err := conn.JetStream().Consumer(ctx, eventsStream, name)
	if want {
		require.NoError(t, err, "consumer %s must exist", name)
		return
	}
	require.Error(t, err, "consumer %s must not exist", name)
}

// waitConsumerGone polls until the durable consumer is deleted. Only the
// JetStream not-found condition counts as gone — a transient lookup error keeps
// polling, so it cannot false-pass the teardown assertion.
func waitConsumerGone(t *testing.T, ctx context.Context, conn *substrate.Conn, name string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := conn.JetStream().Consumer(ctx, eventsStream, name); errors.Is(err, jetstream.ErrConsumerNotFound) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("consumer %q was never torn down after its last live instance completed", name)
}
