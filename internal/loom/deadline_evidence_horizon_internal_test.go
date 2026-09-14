package loom

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- fixtures ---------------------------------------------------------------

// pastTheHorizon returns a clock reading far enough ahead that any epoch
// stamped during the test is older than the evidence horizon. It is the only
// way to stand a test on the far side of a 24 h bound: the server stamps the
// token pointer's timestamp, so the epoch cannot be backdated, and waiting a
// day out is not a test.
func pastTheHorizon() func() time.Time {
	return func() time.Time { return time.Now().Add(opstatus.TrackerTTL + time.Minute) }
}

// seedExpiredUserTaskArm parks an instance on a userTask step and then removes
// its arm, which is the state the probe is only ever entered in: the expiry
// emptied the subject, so deadlineArmed reads absent and the marker the test
// hands the handler is this instance's current one.
func seedExpiredUserTaskArm(ctx context.Context, t *testing.T, s *stateStore, instanceID string) string {
	t.Helper()
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)
	return token
}

// deliverExpiry hands the engine the one message its probe admits — a
// server-shaped MaxAge marker on the instance's deadline subject — and asserts
// the decision is an Ack. A Nak here would mean the handler asked for a
// redelivery of a marker that lives one second.
func deliverExpiry(ctx context.Context, t *testing.T, e *Engine, s *stateStore, instanceID string) {
	t.Helper()
	subjPrefix := "$KV." + s.bucket + "."
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, substrate.Message{
		Subject: subjPrefix + deadlineKey(instanceID),
		Body:    nil,
		Header:  maxAgeHeader,
	}), "a deadline verdict is always acked: the marker it rode lives one second")
}

// requireNoted reads the instance back and asserts it is still running on token
// with an inconclusive-verdict note carrying reason. It returns the revision the
// note stands at, which is what the CAS ordering rows compare.
func requireNoted(ctx context.Context, t *testing.T, s *stateStore, instanceID, token, reason string) uint64 {
	t.Helper()
	inst, revision, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.Equal(t, StatusRunning, inst.Status,
		"an inconclusive verdict leaves the instance running: its human task may still be live")
	require.Equal(t, token, inst.PendingToken, "the pending token is untouched by a refused verdict")
	require.NotNil(t, inst.DeadlineProbe, "the refusal must be recorded on the record, not only logged")
	require.Equal(t, reason, inst.DeadlineProbe.Reason,
		"the note carries the verdict the probe would have written, verbatim")
	require.NotEmpty(t, inst.DeadlineProbe.At)
	_, perr := time.Parse(time.RFC3339, inst.DeadlineProbe.At)
	require.NoError(t, perr, "the note's timestamp is RFC3339, the encoding every loom-state timestamp uses")
	return revision
}

// --- T1/T2: the horizon itself ----------------------------------------------

// TestProbeRejectedOrLost_EvidencePastItsOwnLifetimeIsNoVerdict is the guard's
// central proof and the row's harm, inverted.
//
// The setup is the late-minted delivery exactly: an instance parked on a live
// userTask whose CreateTask committed long ago, whose op tracker has since aged
// out of its 24 h life, and whose deadline marker only now reaches the watcher.
// Every read the probe makes says "no tracker, no outbox" — the same two
// absences a genuinely rejected op leaves — and today that is a terminal.
//
// The one thing that separates them is the AGE of those absences, which the
// probe takes from the pending step's token pointer, and which only an injected
// clock can put on the far side of a day (the server stamps the pointer; a test
// cannot backdate it and must not sleep a day). Past the horizon the probe must
// refuse: warn naming both readings and the operator verb, record the refusal on
// the record at the revision it read, ack, and leave the instance running on its
// token so the human task it backstops can still complete.
//
// MUTATION (T2): delete the `age < opstatus.TrackerTTL` comparison in
// probeRejectedOrLost — i.e. make it always probeFail — and this test reds on
// the status assertion (failed, not running) inside requireNoted. Verified by
// hand while building.
func TestProbeRejectedOrLost_EvidencePastItsOwnLifetimeIsNoVerdict(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))
	e.clock = pastTheHorizon()
	responder := startNotCommittedResponder(t, s.conn)

	const instanceID = "instHorizon1"
	token := seedExpiredUserTaskArm(ctx, t, s, instanceID)

	deliverExpiry(ctx, t, e, s, instanceID)

	require.Positive(t, responder.hits.Load(),
		"the guard sits AFTER the evidence reads, not instead of them: a refusal still asks")
	requireNoted(ctx, t, s, instanceID, token, "step 0 CreateTask rejected")

	out := logs.String()
	require.Contains(t, out, "deadline verdict inconclusive",
		"the refusal is alerted — §10.6 forbids a silent wedge")
	require.Contains(t, out, "no runtime can tell the two apart once the tracker is gone",
		"the alert must name BOTH readings the evidence can no longer distinguish")
	require.Contains(t, out, "lattice loom redrive",
		"an alert that names no operator verb leaves the instance parked with no way out")
}

// TestProbeRejectedOrLost_FreshEvidenceStillFails is T1's positive vector on the
// same seed: with the wall clock, the token pointer was written seconds ago, the
// absences are current, and the verdict is the terminal it has always been.
// Without this, a probe that had simply stopped failing anything would pass the
// test above.
func TestProbeRejectedOrLost_FreshEvidenceStillFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instFreshEpoch1"
	seedExpiredUserTaskArm(ctx, t, s, instanceID)

	deliverExpiry(ctx, t, e, s, instanceID)

	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, inst.Status,
		"absences read inside the evidence's own lifetime are still a verdict")
	require.Nil(t, inst.DeadlineProbe, "a decided instance carries no inconclusive note")
}

// TestProbeRejectedOrLost_MissingTokenPointerIsAnInvariantBreak pins the third
// arm. A running instance's token.<pendingToken> is written in the same batch as
// the record that names it, so its absence is not "old evidence" — it is a break,
// and a break is evidence in itself. The horizon guard must not swallow it into
// a note: it fails the instance the way a missing pattern pin does.
func TestProbeRejectedOrLost_MissingTokenPointerIsAnInvariantBreak(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))
	e.clock = pastTheHorizon()
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instNoPointer1"
	token := seedExpiredUserTaskArm(ctx, t, s, instanceID)
	require.NoError(t, s.deleteToken(ctx, token), "the break the arm exists for")

	deliverExpiry(ctx, t, e, s, instanceID)

	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, inst.Status,
		"an absent token pointer is an invariant break, not aged-out evidence")
	require.Nil(t, inst.DeadlineProbe)
	require.Contains(t, logs.String(), "token pointer missing",
		"the terminal must name the break, not the deadline")
}

// TestTokenEpoch_PropagatesGenuineGetFailure pins the error side of the epoch
// read's ErrKeyNotFound fork, the way TestDeadlineArmed_PropagatesGenuineGetFailure
// pins the currency read's. Not-found here means an invariant break and fails an
// instance; a real substrate failure must therefore stay a retryable error rather
// than collapse into that verdict.
func TestTokenEpoch_PropagatesGenuineGetFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newStateStore(newLoomConn(t), "loom-state-never-provisioned")
	epoch, err := s.tokenEpoch(ctx, "someToken")
	require.Error(t, err)
	require.NotErrorIs(t, err, errTokenPointerMissing,
		"a bucket that does not exist is not a missing pointer")
	require.True(t, epoch.IsZero())
}

// --- T4: the note's lifetime ------------------------------------------------

// TestDeadlineProbeNote_ClearedAtEveryBoundaryThatMovesTheInstance pins the
// note's whole lifetime past its creation. It is a statement about ONE parked
// step's evidence, so it must not survive anything that ends that step: an
// advance writes the next step's token, a fail decides the instance, and a
// redrive is the operator's answer to whatever the note said. Each clear rides
// the batch that moves the instance — a derived fact settled by a second write
// could outlive the state it describes.
func TestDeadlineProbeNote_ClearedAtEveryBoundaryThatMovesTheInstance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	t.Run("advance clears it", func(t *testing.T) {
		const instanceID = "instClearAdvance1"
		_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)
		noteInconclusive(ctx, t, s, instanceID, "step 0 deadline exceeded; op rejected or lost")

		require.NoError(t, e.advance(ctx, instanceID, token0))

		advanced, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, 1, advanced.Cursor, "precondition: the instance moved to step 1")
		require.Nil(t, advanced.DeadlineProbe,
			"the note described step 0's evidence; step 1 is a new wait")
	})

	t.Run("fail clears it", func(t *testing.T) {
		const instanceID = "instClearFail1"
		_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)
		noteInconclusive(ctx, t, s, instanceID, "step 0 deadline exceeded; op rejected or lost")

		noted, revision, err := s.getInstanceAtRevision(ctx, instanceID)
		require.NoError(t, err)
		require.NoError(t, e.fail(ctx, noted, token0, "step 0 deadline exceeded; op rejected or lost", revision))

		failed, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusFailed, failed.Status)
		require.Nil(t, failed.DeadlineProbe, "a decided instance carries no refusal")
	})

	t.Run("redrive clears it", func(t *testing.T) {
		const instanceID = "instClearRedrive1"
		inst, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
		read, revision, err := s.getInstanceAtRevision(ctx, instanceID)
		require.NoError(t, err)
		require.NoError(t, e.fail(ctx, read, token0, "step 0 deadline exceeded; op rejected or lost", revision))

		// A note on a failed record is not a state the probe can produce (it
		// refuses only on a running instance), so it is written here through the
		// real writer to isolate redrive's own clear from fail's.
		noteInconclusive(ctx, t, s, instanceID, "step 0 deadline exceeded; op rejected or lost")

		failed, failedRevision, err := s.getInstanceAtRevision(ctx, instanceID)
		require.NoError(t, err)
		failed.Status = StatusRunning
		require.NoError(t, s.redrive(ctx, failed, &pat, failedRevision))

		redriven, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, redriven.Status)
		require.Nil(t, redriven.DeadlineProbe, "a redrive is the answer to whatever the note said")
		require.Equal(t, inst.InstanceID, redriven.InstanceID)
	})
}

// noteInconclusive writes an inconclusive-verdict note on the instance through
// the production writer, at whatever revision the record currently carries.
func noteInconclusive(ctx context.Context, t *testing.T, s *stateStore, instanceID, reason string) {
	t.Helper()
	inst, revision, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NoError(t, s.noteDeadlineProbe(ctx, inst, reason, time.Now(), revision))
	reread, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, reread.DeadlineProbe, "seed precondition: the note must be on the record")
}

// TestNoteDeadlineProbe_TouchesTheRecordAndNothingElse pins why the note is a
// single-key CAS put rather than a transition. transition with no deadline TTL
// purges deadline.<instanceId>, and on this path that key is ALREADY expired —
// an unconditioned purge of an absent key is accepted by the server, so it would
// mint a marker on an empty subject and wake the probe again on evidence of its
// own making. The refusal must leave the deadline subject exactly as the expiry
// left it.
func TestNoteDeadlineProbe_TouchesTheRecordAndNothingElse(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	e.clock = pastTheHorizon()
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instNoStrayMarker1"
	token := seedExpiredUserTaskArm(ctx, t, s, instanceID)

	stream, err := s.conn.JetStream().Stream(ctx, "KV_"+s.bucket)
	require.NoError(t, err)
	before, err := stream.Info(ctx)
	require.NoError(t, err)
	deadlineSeqBefore := before.State.LastSeq

	deliverExpiry(ctx, t, e, s, instanceID)
	requireNoted(ctx, t, s, instanceID, token, "step 0 CreateTask rejected")

	// The instance record is the only subject written: one message on the
	// stream, and no new message on the deadline subject at all.
	after, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, deadlineSeqBefore+1, after.State.LastSeq,
		"an inconclusive verdict writes the record and nothing else")
	_, err = s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"the expired deadline subject must be left as the expiry left it, never re-marked")
}

// --- T5: the operator surface -----------------------------------------------

// TestInspectInstance_CarriesTheInconclusiveNote pins the note's whole reason for
// being durable: an operator asking after a parked instance must be told the
// engine refused a verdict on it. A note the record holds and the summary drops
// is a fact nobody can act on.
func TestInspectInstance_CarriesTheInconclusiveNote(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	e.clock = pastTheHorizon()
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instInspectNote1"
	seedExpiredUserTaskArm(ctx, t, s, instanceID)
	deliverExpiry(ctx, t, e, s, instanceID)

	detail, err := e.InspectInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, detail.Instance.Status)
	require.False(t, detail.Terminal)
	require.NotNil(t, detail.Instance.DeadlineProbe, "the summary must carry the note the record holds")
	require.Equal(t, "step 0 CreateTask rejected", detail.Instance.DeadlineProbe.Reason)

	listed, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].DeadlineProbe, "the actionable-instance listing carries it too")
}

// --- T6/T7: the CAS ordering ------------------------------------------------

// TestNoteDeadlineProbe_SecondReplicasRefusedCASIsTheAnswer pins the two-replica
// row. Both replicas read the record at the same revision and both reach the
// same refusal; the first note moves the revision, so the second's CAS is
// refused. That refusal is the answer — the record already says what the loser
// would have written — and it must be reported as an Ack, never as an error that
// Naks a marker living one second.
func TestNoteDeadlineProbe_SecondReplicasRefusedCASIsTheAnswer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))
	e.clock = pastTheHorizon()
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instTwoReplicas1"
	token := seedExpiredUserTaskArm(ctx, t, s, instanceID)
	const reason = "step 0 CreateTask rejected"

	// What both replicas read before they went off to gather evidence.
	replicaA, revision, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	replicaB, revisionB, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, revision, revisionB, "precondition: both replicas read the same revision")

	require.NoError(t, e.probeRejectedOrLost(ctx, replicaA, reason, revision))
	noteRevision := requireNoted(ctx, t, s, instanceID, token, reason)
	require.NotEqual(t, revision, noteRevision, "precondition: the winner moved the revision")

	require.NoError(t, e.probeRejectedOrLost(ctx, replicaB, reason, revisionB),
		"a refused condition is the loser's answer, not an error")

	_, revNow, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, noteRevision, revNow, "the refused note must have written nothing")
	require.Contains(t, logs.String(), "inconclusive note dropped",
		"the loser's drop is reported, not silent")
}

// TestProbeRejectedOrLost_RedeliveredMarkerRenotesAndFailsNothing pins the
// redelivery row. A marker redelivered while the instance is still parked past
// the horizon reaches the same refusal, and must re-note at the record's CURRENT
// revision rather than fail on a stale one. The note is idempotent in content and
// bounded in frequency by the marker window; what must never happen is the second
// pass deciding what the first refused to.
func TestProbeRejectedOrLost_RedeliveredMarkerRenotesAndFailsNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	e.clock = pastTheHorizon()
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instRenotedMarker1"
	const reason = "step 0 CreateTask rejected"
	token := seedExpiredUserTaskArm(ctx, t, s, instanceID)

	deliverExpiry(ctx, t, e, s, instanceID)
	firstRevision := requireNoted(ctx, t, s, instanceID, token, reason)

	deliverExpiry(ctx, t, e, s, instanceID)
	secondRevision := requireNoted(ctx, t, s, instanceID, token, reason)

	require.Equal(t, firstRevision+1, secondRevision,
		"the redelivered refusal re-notes at the revision the record now carries")
}

// TestProbeRejectedOrLost_HorizonIsTheTrackerLifetime pins WHICH bound the guard
// compares against. The horizon is not a Loom taste: it is the life of the very
// evidence the probe read, so it must be opstatus.TrackerTTL — the constant the
// Processor stamps every tracker with — and not a local copy that could drift
// from it. A clock one minute INSIDE the horizon still fails; one minute past it
// refuses.
func TestProbeRejectedOrLost_HorizonIsTheTrackerLifetime(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))
	startNotCommittedResponder(t, s.conn)

	for _, tc := range []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"a minute inside the tracker's life is still a verdict", opstatus.TrackerTTL - time.Minute, StatusFailed},
		{"a minute past it is not", opstatus.TrackerTTL + time.Minute, StatusRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instanceID := "instHz" + strings.ReplaceAll(tc.want, " ", "")
			seedExpiredUserTaskArm(ctx, t, s, instanceID)
			at := time.Now().Add(tc.offset)
			e.clock = func() time.Time { return at }

			deliverExpiry(ctx, t, e, s, instanceID)

			inst, err := s.getInstance(ctx, instanceID)
			require.NoError(t, err)
			require.Equal(t, tc.want, inst.Status)
		})
	}
}
