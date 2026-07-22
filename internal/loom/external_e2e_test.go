package loom_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/loom"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The external e2e proves the externalTask seam end-to-end: an
// externalTask step submits its instanceOp (which mints a claim vertex of a
// NON-service, package-chosen type — invariant a, the engine names no type), the
// flow parks on the bare instance handle, the bridge's replyOp posts back
// carrying payload.externalRef = the handle, records the outcome as an ASPECT
// (invariant b / D5), and emits orchestration.externalTaskCompleted; Loom
// correlates by externalRef and advances ONLY on that completion event.
//
// The externalTask is symmetric to a userTask: the bounded creation-deadline
// backstops the instanceOp SUBMISSION only — committed instanceOp → disarm to an
// unbounded bridge wait; rejected/lost instanceOp → FailPattern (FR29);
// not-yet-relayed → re-arm. The deadline never advances the cursor.
//
// instanceOp/replyOp/the external.<adapter> event are TEST FIXTURES here (the
// real DDLs land in 14.4). The fakeProcessor in loom_e2e_test.go models them.

const (
	fixtureClaimType   = "widget" // NON-service claim-vertex type (invariant a)
	fixtureInstanceOp  = "CreateWidgetInstance"
	fixtureReplyOp     = "ResolveWidget"
	fixtureAdapter     = "widgetmaker"
	fixtureReplyAspect = "outcome"
	// The completion event is the uniform orchestration-domain signal the replyOp
	// DDL emits (§10.5/§10.6) — symmetric to a userTask's
	// orchestration.taskCompleted; Loom correlates it via payload.externalRef.
	fixtureReplyEvent = "orchestration.externalTaskCompleted"
)

// newExternalProcessor returns a fake Processor wired with the externalTask
// fixtures: instanceOp mints vtx.widget.<handle>, replyOp records the outcome
// aspect + emits orchestration.externalTaskCompleted(externalRef).
func newExternalProcessor(conn *substrate.Conn) *fakeProcessor {
	return &fakeProcessor{
		conn:        conn,
		logger:      testLogger(),
		rejectOps:   map[string]struct{}{},
		instanceOps: map[string]struct{}{fixtureInstanceOp: {}},
		replyOps:    map[string]struct{}{fixtureReplyOp: {}},
		claimType:   fixtureClaimType,
		replyAspect: fixtureReplyAspect,
		replyEvent:  fixtureReplyEvent,
	}
}

// externalPattern builds a single-step externalTask pattern over a widget
// subject completing on the orchestration domain — where the replyOp's
// orchestration.externalTaskCompleted signal lands (§10.5/§10.6), exactly like an
// all-userTask pattern.
func externalPattern(patternID string) loom.Pattern {
	return loom.Pattern{
		PatternID:         patternID,
		SubjectType:       fixtureClaimType,
		CompletionDomains: []string{"orchestration"},
		Steps: []loom.Step{{
			Kind:       "externalTask",
			Adapter:    fixtureAdapter,
			InstanceOp: fixtureInstanceOp,
			ReplyOp:    fixtureReplyOp,
			Params:     json.RawMessage(`{"shape":"round"}`),
		}},
	}
}

// waitExternalHandle waits until the instance has parked on a live BARE handle
// token (an externalTask write-ahead: dot-free, NOT a vtx.task.<id>), returning
// the handle. It asserts the cursor and that the token pointer is live.
func waitExternalHandle(t *testing.T, ctx context.Context, conn *substrate.Conn, instanceID string, wantCursor int) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID); err == nil {
			var inst loom.Instance
			if json.Unmarshal(entry.Value, &inst) == nil &&
				inst.Cursor == wantCursor && inst.Status == "running" &&
				inst.PendingToken != "" && !strings.Contains(inst.PendingToken, ".") {
				// A bare handle is dot-free, so it is neither a vtx.task.<id> userTask
				// token nor any dotted key — the externalTask write-ahead handle.
				if _, perr := conn.KVGet(ctx, loomStateBucket, "token."+inst.PendingToken); perr == nil {
					return inst.PendingToken
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("instance %q never parked an externalTask handle at cursor %d", instanceID, wantCursor)
	return ""
}

// submitReplyOp models the BRIDGE posting the result back: it submits the
// replyOp op carrying payload.externalRef = the bare handle. The fake Processor
// records the outcome aspect on the claim vertex and emits the completion event.
func submitReplyOp(t *testing.T, ctx context.Context, conn *substrate.Conn, externalRef string, outcome map[string]any) {
	t.Helper()
	requestID := mustNanoID(t)
	payload, _ := json.Marshal(map[string]any{"externalRef": externalRef, "outcome": outcome})
	env, _ := json.Marshal(map[string]any{
		"requestId":     requestID,
		"lane":          "system",
		"operationType": fixtureReplyOp,
		"actor":         loomActorKey,
		"submittedAt":   substrate.FormatTimestamp(time.Now()),
		"payload":       json.RawMessage(payload),
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
	require.Contains(t, []string{"accepted", "duplicate"}, r.Status, "replyOp must commit")
}

// readVertexData reads a vertex's root `data` object (nil if absent).
func readVertexData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, coreKVBucket, key)
	if err != nil {
		return nil
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(entry.Value, &env))
	return env.Data
}

// TestExternalE2E_RunsToCompletion is the headline proof (invariant a + b +
// idempotency): an externalTask step parks on a bare handle, the instanceOp
// mints vtx.widget.<handle> (a NON-service type — the engine names no type), the
// bridge's replyOp records the outcome as an aspect and emits
// orchestration.externalTaskCompleted, Loom correlates by payload.externalRef and
// advances to completion. Advance happens ONLY on that completion event (after
// the unbounded post-commit bridge wait) — never on the deadline. D5 is
// gate-asserted: the outcome lives in an aspect, the claim-vertex root data is
// minimal. Idempotency: a redelivered completion does not re-advance.
func TestExternalE2E_RunsToCompletion(t *testing.T) {
	t.Parallel()
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

	fp := newExternalProcessor(conn)
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, externalPattern(patternID))

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx." + fixtureClaimType + "." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// Step 0 parks on a live BARE handle; the instanceOp committed (claim vertex
	// minted) exactly once.
	handle := waitExternalHandle(t, ctx, conn, instanceID, 0)
	require.False(t, strings.HasPrefix(handle, "vtx."), "Loom parks on the BARE handle, not a vtx.<type>.<id> key")
	claimKey := "vtx." + fixtureClaimType + "." + handle
	require.Eventually(t, func() bool { return readVertexData(t, ctx, conn, claimKey) != nil },
		10*time.Second, 100*time.Millisecond, "instanceOp must mint the claim vertex vtx.widget.<handle>")
	require.Equal(t, 1, fp.createdInstanceCount(), "instanceOp committed exactly once")

	// The bridge posts the result back carrying payload.externalRef = the handle.
	submitReplyOp(t, ctx, conn, handle, map[string]any{"signed": true, "ref": "X-123"})

	// Loom correlates by externalRef → advances → completes.
	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternCompleted must be emitted after the replyOp")
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor, "cursor must advance to exhaustion")

	// --- Invariant b (D5), gate-asserted: outcome in an ASPECT, root data minimal.
	aspectKey := claimKey + "." + fixtureReplyAspect
	aspectData := readVertexData(t, ctx, conn, aspectKey)
	require.NotNil(t, aspectData, "the replyOp outcome must live in an aspect (vtx.widget.<handle>.outcome)")
	require.Equal(t, true, aspectData["signed"], "outcome aspect must carry the outcome fields")
	require.Equal(t, "X-123", aspectData["ref"])

	rootData := readVertexData(t, ctx, conn, claimKey)
	require.NotContains(t, rootData, "signed", "claim-vertex root data must NOT carry the outcome fields (D5)")
	require.NotContains(t, rootData, "ref", "claim-vertex root data must NOT carry the outcome fields (D5)")

	// --- Idempotency: redeliver the replyOp completion event → no second advance.
	republishEvent(t, ctx, conn, fixtureReplyEvent, map[string]any{"externalRef": handle})
	time.Sleep(1 * time.Second)
	inst2 := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst2.Cursor, "a redelivered replyOp must not re-advance (pointer-presence guard)")
	require.Equal(t, 1, fp.createdInstanceCount(), "no second instanceOp under the redelivery")
}

// republishEvent re-emits a completion event onto core-events (models an
// at-least-once redelivery of an already-consumed reply).
func republishEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, class string, payload map[string]any) {
	t.Helper()
	ev := map[string]any{
		"eventId":   mustNanoID(t),
		"requestId": mustNanoID(t),
		"eventType": class,
		"payload":   payload,
		"timestamp": substrate.FormatTimestamp(time.Now()),
	}
	eb, _ := json.Marshal(ev)
	_, err := conn.JetStream().Publish(ctx, "events."+class, eb)
	require.NoError(t, err)
}

// TestExternalE2E_RejectedInstanceOpFails proves the FR29 backstop: a rejected
// instanceOp mints no claim vertex, no tracker, no event — so the handle would
// park forever. The bounded deadline fires, the probe (keyed off the
// instanceOp's OWN requestId) finds no tracker + no outbox → the instance fails
// (and announces loom.patternFailed). Never a silent wedge.
func TestExternalE2E_RejectedInstanceOpFails(t *testing.T) {
	t.Parallel()
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

	fp := newExternalProcessor(conn)
	fp.rejectOps[fixtureInstanceOp] = struct{}{} // reject precedes the mint → no claim vertex
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, externalPattern(patternID))

	// A short creation-deadline so the rejected instanceOp is detected off-stream
	// fast (the externalTask arms CreateTaskTimeout, like a userTask).
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn, func(c *loom.Config) { c.CreateTaskTimeout = 2 * time.Second })
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx." + fixtureClaimType + "." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "failed")
	require.Equal(t, "failed", inst.Status)
	require.Equal(t, 0, fp.createdInstanceCount(), "rejected instanceOp mints no claim vertex")

	_, err = failedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternFailed must be emitted for a rejected instanceOp")
}

// TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait is the load-bearing
// proof that an externalTask is symmetric to a userTask, NOT a systemOp: the
// instanceOp COMMITS (claim vertex + tracker minted) but the reply NEVER arrives.
// The bounded creation-deadline fires, the probe finds the instanceOp's tracker
// present (keyed off its OWN requestId, NOT the parked handle) and DISARMS the
// deadline — the bridge wait is now unbounded. The instance must stay running at
// the SAME cursor (NOT advanced — advancing on a committed-but-unreplied
// instanceOp would skip the external result — and NOT failed); the cursor moves
// only when orchestration.externalTaskCompleted arrives.
func TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	// A committed-but-unreplied instanceOp must NOT advance to completion off the
	// deadline probe (that would emit patternCompleted) — subscribe so the test
	// can assert it does NOT happen.
	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)
	failedSub, err := nc.SubscribeSync("events.loom.patternFailed")
	require.NoError(t, err)

	// The instanceOp commits (mints the claim vertex + tracker) but NO replyOp is
	// ever submitted — so no completion event arrives. The deadline+probe must
	// DISARM off the instanceOp's own tracker and leave the instance running.
	fp := newExternalProcessor(conn)
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, externalPattern(patternID))

	// A short creation-deadline so the committed instanceOp is probed fast.
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn, func(c *loom.Config) { c.CreateTaskTimeout = 2 * time.Second })
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx." + fixtureClaimType + "." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// The instanceOp commits (claim vertex minted) — but we NEVER submit a
	// replyOp, so no completion event arrives.
	handle := waitExternalHandle(t, ctx, conn, instanceID, 0)
	claimKey := "vtx." + fixtureClaimType + "." + handle
	require.Eventually(t, func() bool { return readVertexData(t, ctx, conn, claimKey) != nil },
		10*time.Second, 100*time.Millisecond, "instanceOp must commit (claim vertex minted)")

	// Let several creation-deadlines fire (CreateTaskTimeout=2s). Each probe must
	// find the tracker present and DISARM — never advance, never fail. The
	// instance must stay running at the same cursor, unbounded.
	time.Sleep(5 * time.Second)
	entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID)
	require.NoError(t, err)
	var parked loom.Instance
	require.NoError(t, json.Unmarshal(entry.Value, &parked))
	require.Equal(t, "running", parked.Status, "a committed-but-unreplied instanceOp must DISARM to an unbounded wait, not advance or fail")
	require.Equal(t, 0, parked.Cursor, "the cursor must NOT advance off the deadline (advancing would skip the external result)")
	require.Equal(t, handle, parked.PendingToken, "the instance stays parked on the same handle for the bridge reply")

	// The deadline was disarmed (no completion, no failure announced).
	_, err = completedSub.NextMsg(500 * time.Millisecond)
	require.Error(t, err, "a committed-but-unreplied instanceOp must NOT advance to completion off the deadline")
	_, err = failedSub.NextMsg(500 * time.Millisecond)
	require.Error(t, err, "a committed-but-unreplied instanceOp must NOT fail off the deadline")

	// The bridge eventually replies → the flow advances ONLY now (proving the wait
	// was unbounded, not lost). The handle's token pointer is still live.
	submitReplyOp(t, ctx, conn, handle, map[string]any{"signed": true})
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor, "advance happens only on orchestration.externalTaskCompleted, after the unbounded wait")
}

// TestExternalE2E_NotYetRelayedRearms proves the re-arm branch: with the relay
// paused, the instanceOp's outbox record is still present (not yet delivered) and
// no tracker exists. The deadline fires; the probe finds the outbox present and
// RE-ARMS (does not fail). When the relay resumes, the instanceOp commits and
// the flow runs to completion.
func TestExternalE2E_NotYetRelayedRearms(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	fp := newExternalProcessor(conn)
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, externalPattern(patternID))

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	engine, engErr := startEngine(t, engCtx, conn, func(c *loom.Config) { c.CreateTaskTimeout = 2 * time.Second })
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	// Pause the outbox relay so the instanceOp record sits in loom-state
	// undelivered: the creation-deadline will fire with the outbox present + no
	// tracker → the probe must RE-ARM, not fail.
	engine.PauseForTest(ctx, "loom-outbox-relay")

	subjectKey := "vtx." + fixtureClaimType + "." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// The instance parks on the handle with the instanceOp still in the outbox.
	handle := waitExternalHandle(t, ctx, conn, instanceID, 0)
	require.NotEmpty(t, handle)

	// Let at least one creation-deadline fire while the relay is paused
	// (CreateTaskTimeout=2s). The instance MUST still be running (re-armed), not failed.
	time.Sleep(3 * time.Second)
	entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID)
	require.NoError(t, err)
	var parked loom.Instance
	require.NoError(t, json.Unmarshal(entry.Value, &parked))
	require.Equal(t, "running", parked.Status, "a not-yet-relayed instanceOp must re-arm, not fail")

	// Resume the relay; the instanceOp now commits and the flow can complete via a
	// replyOp.
	engine.ResumeForTest(ctx, "loom-outbox-relay")
	require.Eventually(t, func() bool {
		return readVertexData(t, ctx, conn, "vtx."+fixtureClaimType+"."+handle) != nil
	}, 15*time.Second, 150*time.Millisecond, "after the relay resumes the instanceOp commits")

	submitReplyOp(t, ctx, conn, handle, map[string]any{"result": "ok"})
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor)
}
