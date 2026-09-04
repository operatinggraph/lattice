package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- fixtures ---------------------------------------------------------------

// userTaskFixturePattern is a one-step userTask pattern: the step kind whose
// deadline bounds only the CREATION, so a parked instance sits on an unbounded
// human wait — the population the row's harm names.
func userTaskFixturePattern() Pattern {
	return Pattern{PatternID: "p1", SubjectType: "widget", MetaKey: "vtx.meta.p1", Steps: []Step{
		{Kind: StepKindUserTask, Operation: "CreateTask"},
	}}
}

// seedParkedUserTask drives a real createInstance + transition so the instance
// is parked on a userTask token with a real armed deadline, exactly as a live
// step-0 userTask is. deadlineTTL chooses whether that arm is left to expire
// (the genuine signal) or is long enough that only the test removes it.
//
// No outbox record is written: the userTask probe reads the CreateTask op's own
// derived requestId, never the parked token, so an absent outbox there is what
// puts the probe on its fail branch — the vector this file needs.
func seedParkedUserTask(ctx context.Context, t *testing.T, s *stateStore, instanceID string, deadlineTTL time.Duration) string {
	t.Helper()
	pat := userTaskFixturePattern()
	inst := &Instance{
		InstanceID: instanceID, PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, s.createInstance(ctx, inst, &pat))

	token := userTaskTokenPrefix + instanceID
	inst.PendingToken = token
	require.NoError(t, s.transition(ctx, inst, token, "", tokenCreateOnly, nil, deadlineTTL, 0))
	_, err := s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID))
	require.NoError(t, err, "seed precondition: %s must be armed", deadlineKey(instanceID))
	return token
}

// notCommittedResponder answers every lattice.op.status request with the
// verdict a rejected-or-lost op produces: found:false, committed:false. It is
// the evidence the probe reads, and its hit count says whether the probe ran at
// all.
type notCommittedResponder struct {
	hits atomic.Int64
}

func startNotCommittedResponder(t *testing.T, conn *substrate.Conn) *notCommittedResponder {
	t.Helper()
	r := &notCommittedResponder{}
	sub, err := conn.NATS().Subscribe(opstatus.Subject, func(msg *nats.Msg) {
		r.hits.Add(1)
		body, merr := json.Marshal(opstatus.Response{Found: false, Committed: false})
		require.NoError(t, merr)
		require.NoError(t, msg.Respond(body))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return r
}

// attachDeadlineWatcher adds the real loom-deadline durable — the production
// spec, the supervised pump, the real handler — and nothing else. Start's other
// consumers are irrelevant to the classifier and would drag the pattern source,
// the heartbeater and the sweep pass into a test about one durable.
func attachDeadlineWatcher(ctx context.Context, t *testing.T, e *Engine) {
	t.Helper()
	require.NoError(t, e.supervisor.Add(ctx, e.deadlineSpec()))
	t.Cleanup(e.supervisor.Stop)
}

// awaitDeadlineDrain blocks until the loom-deadline durable has delivered and
// acked everything the stream holds for it, then asserts nothing was ever
// redelivered. It is the no-sleep barrier for "the watcher has seen every
// marker": NumPending counts what is still to come and NumAckPending what is in
// a handler right now, so both at zero means no verdict is still in flight.
//
// The redelivery count is a separate assertion rather than part of the
// predicate, so a Nak reports itself instead of burning the whole budget: the
// handler Naks only when a probe errors, which on a message it should never
// have read is the loudest form of the failure this file hunts.
func awaitDeadlineDrain(ctx context.Context, t *testing.T, s *stateStore, why string) {
	t.Helper()
	stream, err := s.conn.JetStream().Stream(ctx, "KV_"+s.bucket)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		cons, cerr := stream.Consumer(ctx, deadlineDurable)
		if cerr != nil {
			return false
		}
		info, ierr := cons.Info(ctx)
		if ierr != nil {
			return false
		}
		return info.NumPending == 0 && info.NumAckPending == 0
	}, 60*time.Second, 100*time.Millisecond, why)

	cons, err := stream.Consumer(ctx, deadlineDurable)
	require.NoError(t, err)
	info, err := cons.Info(ctx)
	require.NoError(t, err)
	require.Zero(t, info.NumRedelivered,
		"the watcher must never Nak a deadline message: %s", why)
}

// requireStillParked asserts an instance is running on the token it parked on
// — the outcome a removal marker must leave untouched.
func requireStillParked(ctx context.Context, t *testing.T, s *stateStore, instanceID, token string) {
	t.Helper()
	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.Equal(t, StatusRunning, inst.Status,
		"%s must still be running: a removal of its deadline key is not an expiry", instanceID)
	require.Equal(t, token, inst.PendingToken, "%s must still hold its pending token", instanceID)
}

// --- the classifier ---------------------------------------------------------

// TestHandleDeadline_ActsOnTheExpiryAndNotOnARemoval is the classifier's
// central proof, run through the real loom-deadline durable against both
// answers at once.
//
// The negative: two parked userTask instances whose deadline keys are REMOVED —
// one the legacy way (a permanent DEL, an older binary's idiom) and one the way
// every Loom removal writes today (a purge carrying tombstoneTTL). Both deliver
// an empty body to the watcher; neither is an expiry, and the probe's evidence
// (a 24 h Contract #4 tracker, an outbox record the relay already cleared) says
// nothing about a step that was never due. Both instances must still be
// running, parked on their tokens.
//
// The positive vector, same seed and same responder: a third instance whose arm
// is left to EXPIRE. Its marker is the one signal the watcher exists for, and
// it must fail the instance — otherwise the negative above would pass on a
// handler that had simply stopped working.
func TestHandleDeadline_ActsOnTheExpiryAndNotOnARemoval(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	responder := startNotCommittedResponder(t, s.conn)
	attachDeadlineWatcher(ctx, t, e)

	const legacyID, purgedID, expiredID = "instLegacyDel1", "instPurged1", "instExpired1"
	legacyToken := seedParkedUserTask(ctx, t, s, legacyID, time.Hour)
	purgedToken := seedParkedUserTask(ctx, t, s, purgedID, time.Hour)
	seedParkedUserTask(ctx, t, s, expiredID, time.Second)

	// The two removal shapes, delivered live to the attached watcher.
	legacyDelete(ctx, t, s, deadlineKey(legacyID))
	removeDeadlineArm(ctx, t, s, purgedID)

	// The positive vector settles first — its arm expires on its own second.
	require.Eventually(t, func() bool {
		inst, err := s.getInstance(ctx, expiredID)
		return err == nil && inst != nil && inst.Status == StatusFailed
	}, 60*time.Second, 100*time.Millisecond,
		"an expired arm must fail its instance — the signal the watcher exists for")

	awaitDeadlineDrain(ctx, t, s, "the watcher must consume every deadline message")

	requireStillParked(ctx, t, s, legacyID, legacyToken)
	requireStillParked(ctx, t, s, purgedID, purgedToken)

	// The removals cost no evidence read at all: the one probe that ran is the
	// expiry's. Its userTask branch asks the tracker once and stops at the
	// absent outbox.
	require.Equal(t, int64(1), responder.hits.Load(),
		"only the expiry may reach the probe; a removal is acked without a read")
}

// TestHandleDeadline_RebuiltDurableReplaysRemovalsHarmlessly is the filed
// harm end to end: a loom-deadline durable rebuilt from DeliverAll replays
// every removal marker loom-state still carries, including those of instances
// that are parked and healthy.
//
// The responder is deliberately ABSENT. That is the sharpest form of the
// assertion available: if any replayed removal reached the probe, the
// lattice.op.status RPC would time out, the handler would Nak, and the marker
// would be redelivered forever — so a drained durable with every instance still
// running proves no probe ran, not merely that none of them wrote.
func TestHandleDeadline_RebuiltDurableReplaysRemovalsHarmlessly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const parked = 5
	tokens := make(map[string]string, parked)
	for i := range parked {
		id := fmt.Sprintf("instReplay%d", i)
		tokens[id] = seedParkedUserTask(ctx, t, s, id, time.Hour)
		removeDeadlineArm(ctx, t, s, id)
	}

	attachDeadlineWatcher(ctx, t, e)
	awaitDeadlineDrain(ctx, t, s, "the first attach must drain the seeded markers")

	// The rebuild: delete and re-create the durable, then wait for the reopened
	// workers rather than for a duration.
	require.NoError(t, e.supervisor.ResetAwaitReopen(ctx, deadlineDurable, 30*time.Second))
	awaitDeadlineDrain(ctx, t, s, "the rebuilt durable must replay and drain every marker")

	for id, token := range tokens {
		requireStillParked(ctx, t, s, id, token)
	}
}

// --- currency and the write window ------------------------------------------

// twoStepSystemPattern is a two-step systemOp pattern, so a real advance off
// step 0 lands on a step that arms a deadline of its own.
func twoStepSystemPattern() Pattern {
	return Pattern{PatternID: "p2", SubjectType: "widget", MetaKey: "vtx.meta.p2", Steps: []Step{
		{Kind: StepKindSystemOp, Operation: "StepA"},
		{Kind: StepKindSystemOp, Operation: "StepB"},
	}}
}

// seedOnStepZero drives a real createInstance + a real submitStep, so the
// instance is parked on step 0's derived token with the deadline that step
// armed and the outbox record it wrote.
func seedOnStepZero(ctx context.Context, t *testing.T, e *Engine, instanceID string) (*Instance, Pattern, string) {
	t.Helper()
	pat := twoStepSystemPattern()
	inst := &Instance{
		InstanceID: instanceID, PatternRef: "vtx.meta.p2", SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, e.state.createInstance(ctx, inst, &pat))
	require.NoError(t, e.submitStep(ctx, inst, &pat, "", tokenCreateOnly))
	require.Equal(t, deriveRequestID(instanceID, 0), inst.PendingToken)
	return inst, pat, inst.PendingToken
}

// clearOutbox removes an outbox record the way the relay does on publish-ack,
// which is what puts a probe on its rejected-or-lost branch rather than its
// not-yet-relayed one.
func clearOutbox(ctx context.Context, t *testing.T, s *stateStore, requestID string) {
	t.Helper()
	_, err := s.conn.KVGet(ctx, s.bucket, outboxKey(requestID))
	require.NoError(t, err, "seed precondition: outbox.%s must exist", requestID)
	require.NoError(t, s.conn.KVPurgeWithTTL(ctx, s.bucket, outboxKey(requestID), tombstoneTTL, 0))
}

// TestOnDeadline_APresentDeadlineKeyMeansALaterStepRearmed pins the currency
// guard, on the race it exists for: the marker for step N is emitted, N's
// completion advances the instance to N+1 — which arms a deadline of its own —
// and only then does the pump deliver. Nothing about that marker describes
// N+1, whose op was submitted milliseconds ago and has neither a tracker nor an
// outbox record left.
//
// A marker's own emission empties the subject, so a VALUE on deadline.<id> at
// probe time can only have been written after it. That makes presence the
// currency test, and it is read before the tracker RPC is spent: the probe must
// return having asked nothing and written nothing, leaving N+1's arm live.
//
// The second half is the positive vector on the same instance: remove the arm
// and deliver the same marker again, and the probe runs to its verdict.
func TestOnDeadline_APresentDeadlineKeyMeansALaterStepRearmed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	responder := startNotCommittedResponder(t, s.conn)

	const instanceID = "instCurrency1"
	_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)

	// The advance that beats the delivery: a real advance off step 0's token,
	// landing on step 1 and arming its deadline.
	require.NoError(t, e.advance(ctx, instanceID, token0))
	advanced, armRevision, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, 1, advanced.Cursor, "precondition: the instance advanced to step 1")
	token1 := advanced.PendingToken
	require.Equal(t, deriveRequestID(instanceID, 1), token1)
	armed, err := e.state.deadlineArmed(ctx, instanceID)
	require.NoError(t, err)
	require.True(t, armed, "precondition: step 1's submission armed the deadline")

	// Step 1 is relayed and rejected, so nothing but the currency guard stands
	// between the stale marker and a terminal.
	clearOutbox(ctx, t, s, token1)

	subjPrefix := "$KV." + s.bucket + "."
	staleMarker := substrate.Message{
		Subject: subjPrefix + deadlineKey(instanceID),
		Body:    nil,
		Header:  maxAgeHeader,
	}
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, staleMarker))

	still, revAfter, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, still.Status, "step N's marker must not fail an instance on step N+1")
	require.Equal(t, 1, still.Cursor)
	require.Equal(t, token1, still.PendingToken)
	require.Equal(t, armRevision, revAfter, "the record must not be written at all")
	armed, err = e.state.deadlineArmed(ctx, instanceID)
	require.NoError(t, err)
	require.True(t, armed, "step 1's live arm must survive the stale marker")
	require.Zero(t, responder.hits.Load(),
		"a stale marker must be dropped before the tracker RPC is spent")

	// The positive vector: with the arm gone, the same marker IS this
	// instance's expiry, and the probe runs to its verdict.
	removeDeadlineArm(ctx, t, s, instanceID)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, staleMarker))

	failed, err := e.state.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status,
		"with no arm standing, the marker is the current step's expiry and the probe must act")
	require.Positive(t, responder.hits.Load(), "the probe must have read its evidence")
}

// TestProbeFail_RefusesATerminalWrittenOverAnAdvancedRecord pins the write
// window the currency guard cannot see: the probe reads the record for step N,
// spends a tracker RPC and an outbox read, and the completion lands in exactly
// that gap. Without the condition, fail overwrites an instance that has already
// moved to N+1 — flipping a healthy record to failed and purging N+1's live arm
// in the same batch, a silent wedge if N+1 is then rejected.
//
// The refusal is the answer, not an error: the caller returns nil, so the
// marker is acked and no redelivery is asked for (a MaxAge marker lives one
// second; there would be nothing to redeliver).
//
// The second call is the positive vector — with the revision the record
// actually carries, the same verdict commits — so a probeFail that had simply
// stopped writing could not pass this.
func TestProbeFail_RefusesATerminalWrittenOverAnAdvancedRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID = "instWriteWindow1"
	_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)

	// What the probe read before it went off to gather evidence.
	readAtR, revisionR, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, 0, readAtR.Cursor)

	// The completion lands under the probe: a real advance to step 1, which
	// bumps the record's revision and arms a deadline of its own.
	require.NoError(t, e.advance(ctx, instanceID, token0))
	advanced, revisionAfter, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, 1, advanced.Cursor)
	require.NotEqual(t, revisionR, revisionAfter, "precondition: the advance moved the revision")
	token1 := advanced.PendingToken

	// The probe's verdict, carrying the revision it read at. Refused, and the
	// refusal is reported as a drop.
	require.NoError(t, e.probeFail(ctx, readAtR, token0,
		"step 0 deadline exceeded; op rejected or lost", revisionR),
		"a refused condition is the probe's answer, not an error")

	survived, revNow, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, survived.Status, "the advanced record must survive the stale verdict")
	require.Equal(t, 1, survived.Cursor)
	require.Equal(t, token1, survived.PendingToken)
	require.Equal(t, revisionAfter, revNow, "the refused batch must have written nothing")
	armed, err := e.state.deadlineArmed(ctx, instanceID)
	require.NoError(t, err)
	require.True(t, armed, "step 1's live arm must not be purged by a refused terminal")

	// The positive vector: the same verdict against the revision the record
	// actually carries commits.
	fresh, freshRevision, err := e.state.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NoError(t, e.probeFail(ctx, fresh, token1,
		"step 1 deadline exceeded; op rejected or lost", freshRevision))

	failed, err := e.state.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status, "a current-revision verdict must commit")
}

// TestDeadlineArmed_PropagatesGenuineGetFailure pins the error side of the
// currency read's ErrKeyNotFound fork. Absence is a load-bearing answer here —
// it means "the arm that expired is still the one this instance is on", and the
// probe goes on to spend its evidence reads and write a terminal off it. A real
// substrate failure (here: the bucket was never provisioned) must therefore be
// returned, not collapsed into that answer; onDeadline turns the error into a
// Nak and writes nothing.
func TestDeadlineArmed_PropagatesGenuineGetFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newStateStore(newLoomConn(t), "loom-state-never-provisioned")
	armed, err := s.deadlineArmed(ctx, "any-instance")
	require.Error(t, err, "a genuine read failure must not read as an absent arm")
	require.False(t, armed)
}
