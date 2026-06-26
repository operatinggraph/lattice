package loom_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/substrate"
)

// The onboarding e2e proves the userTask seam end-to-end (Story 8.2 AC#1-9):
// each userTask step submits a CreateTask (not the bound op), the flow waits on
// a live token.<taskKey>, the simulated user's bound op auto-completes the task
// → orchestration.taskCompleted(taskKey) → Loom correlates by taskKey and advances, to
// exhaustion + the loom.patternCompleted terminal — plus a long-wait restart
// before the user acts that resumes exactly once.
//
// The bound ops (SetName/SetPhone/SetAddress) are onboarding-flow fixture ops:
// the fake Processor simulates their auto-complete (the real commit-path
// auto-complete is internal/processor/autocomplete.go, exercised in the
// processor package). A bound-op submission carries authContext.task = the
// taskKey, exactly as a task-authorized op does (rp.Path == "task"), so the fake
// emits orchestration.taskCompleted(taskKey) — proving the integration the auto-complete needs.

// seedOpMeta writes a vtx.meta.<opId> op meta-vertex carrying data.operationType
// so Loom's pattern source indexes operationType → meta key (the userTask
// forOperation resolution).
func seedOpMeta(t *testing.T, ctx context.Context, conn *substrate.Conn, opID, operationType string) string {
	t.Helper()
	key := "vtx.meta." + opID
	body, _ := json.Marshal(map[string]any{
		"class": "meta",
		"data":  map[string]any{"operationType": operationType},
	})
	_, err := conn.KVPut(ctx, coreKVBucket, key, body)
	require.NoError(t, err)
	return key
}

// submitBoundOp simulates the user performing the step's bound op. It carries
// authContext.task = taskKey (the task-auth path); the fake Processor reads that
// and emits orchestration.taskCompleted(taskKey) as the commit-path auto-complete would.
func submitBoundOp(t *testing.T, ctx context.Context, conn *substrate.Conn, operation, taskKey, subjectKey string) {
	t.Helper()
	requestID := mustNanoID(t)
	payload, _ := json.Marshal(map[string]string{"subjectKey": subjectKey})
	env, _ := json.Marshal(map[string]any{
		"requestId":     requestID,
		"lane":          "system",
		"operationType": operation,
		"actor":         subjectKey,
		"submittedAt":   substrate.FormatTimestamp(time.Now()),
		"payload":       json.RawMessage(payload),
		"authContext":   map[string]string{"task": taskKey, "target": subjectKey},
	})

	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	msg := &nats.Msg{Subject: "ops.system", Data: env, Header: nats.Header{replyInboxHeader: []string{inbox}}}
	_, err = conn.JetStream().PublishMsg(ctx, msg)
	require.NoError(t, err)

	reply, err := sub.NextMsgWithContext(ctx)
	require.NoError(t, err)
	var r struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(reply.Data, &r))
	require.Contains(t, []string{"accepted", "duplicate"}, r.Status, "bound op must commit")
}

// onboardingPattern builds the [collectName, collectPhone, collectAddress]
// userTask pattern whose completion domain is orchestration (the
// orchestration.taskCompleted event).
func onboardingPattern(patternID string) loom.Pattern {
	return loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "identity",
		CompletionDomains: []string{"orchestration"},
		Steps: []loom.Step{
			{Kind: "userTask", Operation: "SetName"},
			{Kind: "userTask", Operation: "SetPhone"},
			{Kind: "userTask", Operation: "SetAddress"},
		},
	}
}

// waitTaskKey waits until the instance has parked on a live token whose key is a
// vtx.task.<id> (a userTask write-ahead), returning that taskKey. It asserts the
// cursor is the expected step and the token pointer is live (the wait).
func waitTaskKey(t *testing.T, ctx context.Context, conn *substrate.Conn, instanceID string, wantCursor int) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID); err == nil {
			var inst loom.Instance
			if json.Unmarshal(entry.Value, &inst) == nil &&
				inst.Cursor == wantCursor && inst.Status == "running" &&
				len(inst.PendingToken) > len("vtx.task.") && inst.PendingToken[:len("vtx.task.")] == "vtx.task." {
				// The durable reverse pointer must be live (the wait).
				if _, perr := conn.KVGet(ctx, loomStateBucket, "token."+inst.PendingToken); perr == nil {
					return inst.PendingToken
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("instance %q never parked a userTask token at cursor %d", instanceID, wantCursor)
	return ""
}

// newOnboardingProcessor returns a fake Processor that honours CreateTask (mint
// vtx.task.<suppliedTaskId> + tracker, NO completion event) and the bound ops
// (emit orchestration.taskCompleted(taskKey) on commit, simulating auto-complete).
func newOnboardingProcessor(conn *substrate.Conn, boundOps map[string]struct{}) *fakeProcessor {
	fp := &fakeProcessor{
		conn:      conn,
		logger:    testLogger(),
		rejectOps: map[string]struct{}{},
	}
	fp.taskOps = boundOps
	return fp
}

// TestOnboardingE2E_UserTaskFlow drives the onboarding pattern to completion via
// real StartLoomPattern + simulated user bound ops, proving AC#1-9 (ordered
// CreateTask per step, the wait, taskKey correlation, exhaustion terminal).
func TestOnboardingE2E_UserTaskFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	fp.run(ctx, t)

	// Op meta-vertices for the three bound ops (Loom resolves forOperation).
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, onboardingPattern(patternID))

	engine := newEngine(conn)
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	time.Sleep(700 * time.Millisecond) // pattern + op-meta CDC replay

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	steps := []string{"SetName", "SetPhone", "SetAddress"}
	for i, op := range steps {
		// The step submits CreateTask and WAITS on a live token.<taskKey>.
		taskKey := waitTaskKey(t, ctx, conn, instanceID, i)
		require.True(t, fp.taskCreated(taskKey), "step %d must submit CreateTask minting %s", i, taskKey)

		// The user performs the bound op → auto-complete → orchestration.taskCompleted(taskKey)
		// → Loom correlates and advances.
		submitBoundOp(t, ctx, conn, op, taskKey, subjectKey)
	}

	// Exhaustion → loom.patternCompleted.
	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternCompleted (OnboardingComplete) must be emitted")

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 3, inst.Cursor, "cursor must advance through all three userTask steps")

	// Exactly three CreateTask ops committed (one per step, no duplicates).
	require.Equal(t, 3, fp.createTaskCount(), "exactly one CreateTask per userTask step")
}

// TestOnboardingE2E_LongWaitRestartExactlyOnce proves AC#6/#9: an engine restart
// mid-flow, BEFORE the user acts on a step, does not break the flow — after
// restart the user submits the bound op and the cursor advances exactly once
// (no double CreateTask, no double advance), correlated on the durable
// token.<taskKey> pointer.
func TestOnboardingE2E_LongWaitRestartExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	fp.run(ctx, t)

	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, onboardingPattern(patternID))

	subjectKey := "vtx.identity." + mustNanoID(t)

	// --- Generation 1: start, drive to the first userTask wait, then crash
	// BEFORE the user acts. ---
	e1 := newEngine(conn)
	e1Ctx, e1Cancel := context.WithCancel(ctx)
	e1Done := make(chan struct{})
	go func() { defer close(e1Done); _ = e1.Start(e1Ctx) }()
	// Give the CDC pattern source a moment to load the installed pattern before
	// the trigger arrives so handleTrigger resolves it on first delivery rather
	// than Nak-ing for redelivery. waitTaskKey below is the real wait for the
	// userTask park; this is only an optimisation to skip a redelivery cycle.
	time.Sleep(700 * time.Millisecond)

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	taskKey0 := waitTaskKey(t, ctx, conn, instanceID, 0)
	require.True(t, fp.taskCreated(taskKey0), "step 0 CreateTask must have committed before crash")
	require.Equal(t, 1, fp.createTaskCount(), "exactly one CreateTask before the user acts")

	// Crash generation 1 with the user not having acted (token still live). Wait
	// for e1.Start to RETURN, not a fixed sleep: Start blocks on ctx.Done then runs
	// the synchronous supervisor.Stop (which cancels and JOINS every consumer pump,
	// `<-mc.done`) before returning, so a returned Start means gen-1 is fully
	// stopped with no consumer still mid-work. The async cancel + fixed sleep this
	// replaces was the double-process window the flake rode: under -p 4 load the
	// 500ms could elapse before gen-1's pumps drained, leaving gen-1 and gen-2 both
	// live on the same token. Joining the goroutine closes that window.
	e1Cancel()
	select {
	case <-e1Done:
	case <-time.After(15 * time.Second):
		t.Fatal("generation 1 engine did not stop within 15s after cancel")
	}

	// --- Generation 2: restart. The durable token.<taskKey> pointer is still
	// live; the per-domain consumer resumes from its ack floor. ---
	e2 := newEngine(conn)
	e2Ctx, e2Cancel := context.WithCancel(ctx)
	defer e2Cancel()
	go func() { _ = e2.Start(e2Ctx) }()

	// The instance must still be parked on the SAME taskKey — no re-submission, no
	// double CreateTask — across the restart. Assert the count NEVER exceeds 1 over
	// a window that spans gen-2's bring-up: gen-2 re-attaches its trigger consumer
	// (DeliverAll), the patternStarted event is redelivered, and handleTrigger must
	// recognise the still-pending userTask as a duplicate and Ack WITHOUT
	// re-submitting. A point-in-time Equal after a fixed settle-sleep raced this;
	// require.Never catches a re-submission whenever within the window it occurs.
	require.Never(t, func() bool { return fp.createTaskCount() > 1 },
		2*time.Second, 50*time.Millisecond,
		"restart must not re-submit CreateTask for a still-pending userTask")

	// NOW the user acts. The cursor advances against the durable pointer.
	submitBoundOp(t, ctx, conn, "SetName", taskKey0, subjectKey)

	// Drive the remaining two steps to completion.
	for i, op := range []string{"SetPhone", "SetAddress"} {
		taskKey := waitTaskKey(t, ctx, conn, instanceID, i+1)
		submitBoundOp(t, ctx, conn, op, taskKey, subjectKey)
	}

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 3, inst.Cursor)
	// Exactly three CreateTask across BOTH generations (step 0 once despite the
	// restart). The instance is terminal here, so this count is stable — the
	// end-to-end exactly-once witness.
	require.Equal(t, 3, fp.createTaskCount(), "long-wait restart must not double-submit any userTask")
}

// TestOnboardingE2E_RejectedCreateTaskFails proves the userTask creation-deadline
// backstop (the §10.6 deadline+probe applied to task creation): a userTask whose
// CreateTask is REJECTED by the Processor mints no task vertex, no tracker, and
// no completion event — so the token would park forever. Instead the bounded
// creation-deadline fires, the probe finds no task vertex / tracker / outbox, and
// the instance ends status=failed (and announces loom.patternFailed). It does NOT
// hang. This is the load-bearing new assertion.
func TestOnboardingE2E_RejectedCreateTaskFails(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	failedSub, err := nc.SubscribeSync("events.loom.patternFailed")
	require.NoError(t, err)

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	// The Processor rejects CreateTask (e.g. the subject identity is dead/absent →
	// CreateTask's no-orphan validation rejects it): no tracker, no task vertex, no
	// event. The reject check precedes the CreateTask mint, so the task is never
	// created.
	fp.rejectOps["CreateTask"] = struct{}{}
	fp.run(ctx, t)

	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, onboardingPattern(patternID))

	// A short creation-deadline so the rejected CreateTask is detected off-stream
	// without waiting the 60s default.
	engine := newEngine(conn, func(c *loom.Config) { c.CreateTaskTimeout = 2 * time.Second })
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	time.Sleep(700 * time.Millisecond)

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// The instance must FAIL (not hang) once the creation-deadline probe finds no
	// task vertex / tracker / outbox.
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "failed")
	require.Equal(t, "failed", inst.Status)
	require.Equal(t, 0, fp.createTaskCount(), "rejected CreateTask mints no task")

	_, err = failedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternFailed must be emitted for a rejected CreateTask")
}

// TestOnboardingE2E_CreatedTaskDisarmsForUnboundedWait proves the other half of
// the deadline+probe: a userTask whose CreateTask COMMITS but whose human does
// NOT act for longer than CreateTaskTimeout must not be false-failed. The bounded
// creation-deadline fires, the probe finds the task vertex present, DISARMS the
// deadline, and the instance stays running. When the user finally acts — well
// after the creation-deadline — the cursor still advances exactly once. This
// proves the unbounded human wait survives the bounded creation-deadline.
func TestOnboardingE2E_CreatedTaskDisarmsForUnboundedWait(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	fp.run(ctx, t)

	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, onboardingPattern(patternID))

	// A short creation-deadline; the human (this test) deliberately waits LONGER
	// than it before acting, so the deadline fires while the task already exists.
	createDeadline := 2 * time.Second
	engine := newEngine(conn, func(c *loom.Config) { c.CreateTaskTimeout = createDeadline })
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	time.Sleep(700 * time.Millisecond)

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// Step 0 parks on a live userTask token; CreateTask has committed (task vertex
	// minted).
	taskKey0 := waitTaskKey(t, ctx, conn, instanceID, 0)
	require.True(t, fp.taskCreated(taskKey0), "step 0 CreateTask must commit (task vertex minted)")

	// Wait WELL PAST the creation-deadline without acting. The deadline fires, the
	// probe sees the task vertex, disarms, and the instance stays running at
	// cursor 0 on the same live token — never false-failed.
	time.Sleep(3 * createDeadline)
	inst, err := getInstance(ctx, conn, instanceID)
	require.NoError(t, err)
	require.Equal(t, "running", inst.Status, "a created-but-unacted userTask must NOT be failed by the creation-deadline")
	require.Equal(t, 0, inst.Cursor, "cursor must not advance while the human has not acted")
	require.Equal(t, taskKey0, inst.PendingToken, "the same userTask token is still pending (unbounded wait)")
	_, perr := conn.KVGet(ctx, loomStateBucket, "token."+taskKey0)
	require.NoError(t, perr, "the durable token pointer is still live")

	// NOW the user finally acts (long after the creation-deadline). The cursor
	// advances exactly once off the durable pointer.
	submitBoundOp(t, ctx, conn, "SetName", taskKey0, subjectKey)

	// Drive the remaining steps to completion to confirm the flow is intact and
	// advanced exactly once at step 0.
	for i, op := range []string{"SetPhone", "SetAddress"} {
		taskKey := waitTaskKey(t, ctx, conn, instanceID, i+1)
		submitBoundOp(t, ctx, conn, op, taskKey, subjectKey)
	}
	done := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 3, done.Cursor)
	require.Equal(t, 3, fp.createTaskCount(), "exactly one CreateTask per step despite the fired-then-disarmed creation-deadline")
}

// getInstance reads the persisted instance record (test helper).
func getInstance(ctx context.Context, conn *substrate.Conn, instanceID string) (*loom.Instance, error) {
	entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID)
	if err != nil {
		return nil, err
	}
	var inst loom.Instance
	if err := json.Unmarshal(entry.Value, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}
