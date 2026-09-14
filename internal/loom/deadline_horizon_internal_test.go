package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The evidence horizon, and the one delivery that crosses it.
//
// The step-deadline probe reads "no op tracker, no outbox record" as
// rejected-or-lost. The tracker it reads carries opstatus.TrackerTTL (24h,
// Contract #4 §4.3) while the wait it backstops is unbounded, so that reading
// is sound only while the step is younger than the tracker's life. These tests
// pin both sides of that comparison and the note the inconclusive side writes.
//
// The delivery that reaches the far side is a marker minted LATE: the server
// writes a MaxAge marker when it PROCESSES an expiry, not when the TTL falls
// due, so a per-key TTL that fell due while nothing was serving is expired —
// with its MaxAge marker — on the recovery pass's first age check. That premise
// is pinned live, against the pinned server and its recovery path, by
// internal/substrate's
// TestKVMarkerProvenance_TTLPastDueAtRecoveryStillMintsAMaxAgeMarker (which
// also carries the nats-server v2.14.0 filestore.go cites for the three pieces
// that compose to it: expireMsgsOnRecover's early return under subject-delete
// markers, recoverTTLState's immediate age check, and expireMsgs draining every
// past-due entry through handleRemovalOrSdm).
//
// So after an outage longer than the tracker's remaining life, KV_loom-state's
// deadline marker and KV_core-kv's tracker expiry are processed in the same
// pass on the same clock: Loom's durable delivers the marker, and the tracker
// it would have read is already gone. What is left to pin here is what Loom
// does with that delivery, which is a comparison against a clock — so these
// tests inject the clock rather than staging a day-long outage.

// horizonContext is the test budget every case in this file runs under: the
// seeds drive real transitions against embedded NATS, and nothing here waits
// on a duration.
func horizonContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

// horizonEngine builds an engine over s whose clock the test moves, and returns
// the engine, the log sink and a pointer to the instant e.now() reports. The
// clock is read only from the test's own goroutine (these tests call
// handleDeadline directly rather than attaching the durable), so a plain
// variable is the whole seam.
func horizonEngine(s *stateStore) (*Engine, *bytes.Buffer, *time.Time) {
	buf := &bytes.Buffer{}
	e := newSweepEngine(s, sweepLogger(buf))
	at := time.Now()
	e.clock = func() time.Time { return at }
	return e, buf, &at
}

// requireLogged returns the one log record at the given level whose msg matches,
// failing when there is not exactly one. It is how these tests assert the §10.6
// alert carrier: an inconclusive verdict must ALERT, not merely decline to fail.
func requireLogged(t *testing.T, buf *bytes.Buffer, level, msg string) map[string]any {
	t.Helper()
	var hits []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "log line is not JSON: %s", line)
		if rec["level"] == level && rec["msg"] == msg {
			hits = append(hits, rec)
		}
	}
	require.Len(t, hits, 1, "want exactly one %s %q in:\n%s", level, msg, buf.String())
	return hits[0]
}

// deadlineMarkerFor is the server-minted expiry marker for an instance's arm,
// hand-built so a test delivers it when it chooses: an empty body on the
// deadline key carrying Nats-Marker-Reason: MaxAge, the only provenance the
// handler admits.
func deadlineMarkerFor(s *stateStore, instanceID string) (string, substrate.Message) {
	subjPrefix := "$KV." + s.bucket + "."
	return subjPrefix, substrate.Message{
		Subject: subjPrefix + deadlineKey(instanceID),
		Body:    nil,
		Header:  maxAgeHeader,
	}
}

// requireNoteStands reads the record and returns its standing inconclusive
// note, asserting the instance is still running on token — the whole point of
// the verdict.
func requireNoteStands(ctx context.Context, t *testing.T, s *stateStore, instanceID, token string) *probeNote {
	t.Helper()
	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.Equal(t, StatusRunning, inst.Status, "an inconclusive verdict must leave the instance running")
	require.Equal(t, token, inst.PendingToken, "an inconclusive verdict must leave the pending token in place")
	require.NotNil(t, inst.DeadlineProbe, "the inconclusive verdict must be noted on the record")
	return inst.DeadlineProbe
}

// TestOnDeadline_AbsenceIsEvidenceOnlyInsideTheEvidencesLifetime is the guard's
// central proof, and it runs both verdicts through ONE instance and ONE marker
// so nothing but the age of the evidence differs between them.
//
// The instance is a parked userTask — the population the harm names, since its
// wait is unbounded — seeded through a real createInstance + transition so its
// token pointer carries the epoch a real step's does. Its arm is removed (the
// currency test) and the op-status responder answers "not committed", so both
// passes reach the rejected-or-lost verdict with no tracker and no outbox
// record: the two absences the probe used to read as "rejected".
//
// Pass one runs with the engine's clock a day and an hour past the step's
// epoch. The tracker would have expired by then whether or not the op
// committed, so the absence distinguishes nothing: the verdict must be
// inconclusive — a Warn alert, a note on the record, an Ack, and an instance
// still running on its token with its step untouched.
//
// Pass two delivers the same marker with the clock back at the real now, where
// the epoch is seconds old and an absent tracker really does mean the op never
// committed. The verdict must be the terminal — which is also what proves pass
// one pinned the comparison rather than a probe that had simply stopped
// failing.
func TestOnDeadline_AbsenceIsEvidenceOnlyInsideTheEvidencesLifetime(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, logs, at := horizonEngine(s)
	responder := startNotCommittedResponder(t, s.conn)

	const instanceID = "instHorizonAged1"
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)

	epoch, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), epoch, time.Minute,
		"precondition: the seeded step's epoch is the server's stamp on its token pointer, written just now")

	// Pass one: the engine's clock a day past the step's epoch.
	*at = epoch.Add(opstatus.TrackerTTL + time.Hour)
	subjPrefix, marker := deadlineMarkerFor(s, instanceID)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker),
		"an inconclusive verdict acks: the marker lives one second, so a Nak asks for a redelivery that cannot come")

	note := requireNoteStands(ctx, t, s, instanceID, token)
	require.Contains(t, note.Reason, "INCONCLUSIVE")
	require.Contains(t, note.Reason, "CreateTask rejected",
		"the note carries the verdict the probe would have written, so both readings are named")
	noteAt, perr := time.Parse(time.RFC3339Nano, note.At)
	require.NoError(t, perr)
	require.Equal(t, at.UTC(), noteAt.UTC(), "the note is stamped from the engine's clock")

	alert := requireLogged(t, logs, "WARN", "loom: step deadline verdict inconclusive; instance left running")
	require.Equal(t, instanceID, alert["instanceId"])
	require.Equal(t, opstatus.TrackerTTL.String(), alert["horizon"])

	require.Positive(t, responder.hits.Load(), "the probe must still spend its evidence reads before judging them stale")
	_, err = s.conn.KVGet(ctx, s.bucket, tokenKey(token))
	require.NoError(t, err, "the step's token pointer must survive an inconclusive verdict")
	armed, err := s.deadlineArmed(ctx, instanceID)
	require.NoError(t, err)
	require.False(t, armed, "an inconclusive verdict does not re-arm: the wait it guards is unbounded")

	// Pass two: the same marker, the same instance, the real clock. The step's
	// epoch is now inside the horizon, so the two absences are evidence again.
	*at = time.Now()
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))

	failed, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status,
		"inside the horizon an absent tracker IS the rejected-or-lost verdict")
	require.Nil(t, failed.DeadlineProbe,
		"the terminal supersedes the note, settled in the batch that flips the status")
}

// TestOnDeadline_AMissingTokenPointerIsAnInvariantBreakNotAgeing pins the one
// row of the state table that stays a terminal past the horizon.
//
// The pointer is written in the step's own transition batch and removed only by
// the batch that leaves the step, so for a running instance its absence is an
// invariant break — the same class as a missing pattern pin, and evidence in
// itself. It must not be collapsed into "the evidence is stale": that would
// park an instance whose own index is broken, forever and silently. The clock
// is set past the horizon here so nothing but the sentinel can produce the
// terminal.
func TestOnDeadline_AMissingTokenPointerIsAnInvariantBreakNotAgeing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, at := horizonEngine(s)
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instHorizonNoPtr1"
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)
	require.NoError(t, s.deleteToken(ctx, token), "seed: the pointer the step's batch wrote is gone")
	_, err := s.tokenEpoch(ctx, token)
	require.ErrorIs(t, err, errTokenPointerMissing, "seed precondition: the pointer is absent")

	*at = time.Now().Add(opstatus.TrackerTTL + time.Hour)
	subjPrefix, marker := deadlineMarkerFor(s, instanceID)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))

	failed, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status,
		"a missing token pointer is an invariant break, never a stale-evidence verdict")
	require.Nil(t, failed.DeadlineProbe)
}

// TestDeadlineProbeNote_IsSettledByEveryPathThatLeavesTheStep pins the note's
// lifetime: it describes ONE step's evidence, so every path that leaves that
// step must clear it in the same write that changes the state, never as a
// second write that could land independently.
//
// The three paths are the three the design names. advance writes a new token —
// a new step, whose evidence is its own. fail (and complete) takes the status
// out of running — a verdict that supersedes an inconclusive one. redrive
// resumes on the operator's decision, which answers what the probe could not.
//
// The redrive row is belt and braces by construction and is exercised at the
// store: no production path leaves a note on a FAILED record (every terminal
// runs through transition, which clears it), and RedriveInstance accepts only a
// failed instance — so the state the clear guards is reachable only from a
// record written by an older binary, which is exactly why the clear is settled
// in redrive's batch rather than assumed away.
func TestDeadlineProbeNote_IsSettledByEveryPathThatLeavesTheStep(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	t.Run("advance clears it: the new token is a new step", func(t *testing.T) {
		const instanceID = "instNoteAdvance1"
		_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)
		noted, revision := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe)

		require.NoError(t, e.advance(ctx, instanceID, token0))
		after, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, 1, after.Cursor, "precondition: the advance moved to step 1")
		require.NotEqual(t, token0, after.PendingToken)
		require.Nil(t, after.DeadlineProbe, "step 0's note must not describe step 1")
		require.NotZero(t, revision)
	})

	t.Run("fail clears it: the terminal supersedes it", func(t *testing.T) {
		const instanceID = "instNoteFail1"
		_, _, token0 := seedOnStepZero(ctx, t, e, instanceID)
		noted, _ := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe)

		require.NoError(t, e.fail(ctx, noted, token0, "step 0 deadline exceeded; op rejected or lost", 0))
		after, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusFailed, after.Status)
		require.Nil(t, after.DeadlineProbe, "a terminal instance carries no pending-step note")
	})

	t.Run("redrive clears it: the operator answered what the probe could not", func(t *testing.T) {
		const instanceID = "instNoteRedrive1"
		inst, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
		require.NoError(t, e.fail(ctx, inst, token0, "step 0 deadline exceeded; op rejected or lost", 0))
		noted, revision := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe, "seed: a failed record carrying a note, as an older binary could leave")

		noted.Status = StatusRunning
		require.NoError(t, s.redrive(ctx, noted, &pat, revision))
		after, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, after.Status)
		require.Nil(t, after.DeadlineProbe, "a redriven instance carries no stale inconclusive verdict")
	})
}

// noteOnRecord writes a real inconclusive note onto an instance's record
// through the production store method, at the revision the record currently
// carries, and returns the record it wrote plus that revision.
func noteOnRecord(ctx context.Context, t *testing.T, s *stateStore, instanceID string) (*Instance, uint64) {
	t.Helper()
	inst, revision, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	require.NoError(t, s.noteDeadlineProbe(ctx, inst, "step 0 deadline exceeded; op rejected or lost: INCONCLUSIVE", time.Now(), revision))
	reread, rev, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, reread.DeadlineProbe, "seed precondition: the note is on the record")
	return reread, rev
}

// TestInspectInstance_CarriesTheInconclusiveNoteAcrossAFreshEngine pins the two
// remaining rows of the note's lifetime: it is CARRIED on the record (so a
// restart does not lose it — a fresh engine over the same bucket reads it), and
// it reaches the operator through the control surface's own summary rather than
// living only in a log line.
func TestInspectInstance_CarriesTheInconclusiveNoteAcrossAFreshEngine(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	const instanceID = "instNoteInspect1"
	seedOnStepZero(ctx, t, e, instanceID)
	noted, _ := noteOnRecord(ctx, t, s, instanceID)

	// A fresh engine and a fresh store over the same bucket: what a restarted
	// Loom sees is the record, and nothing else.
	restarted := newSweepEngine(newStateStore(s.conn, s.bucket), sweepLogger(&bytes.Buffer{}))
	detail, err := restarted.InspectInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, detail.Instance.Status)
	require.NotNil(t, detail.Instance.DeadlineProbe,
		"InspectInstance must carry the note: it is why the instance is parked rather than failed")
	require.Equal(t, noted.DeadlineProbe.Reason, detail.Instance.DeadlineProbe.Reason)
	require.Equal(t, noted.DeadlineProbe.At, detail.Instance.DeadlineProbe.At)

	listed, err := restarted.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].DeadlineProbe, "the listing's summary carries the same field")
}

// TestNoteDeadlineProbe_TheSecondReplicasCASIsRefused pins the ordering: the
// note is written at the revision the probe READ the record at, so two replicas
// woken by one late marker write one note.
//
// The loser's refusal is an answer, not an error — the same drop probeFail
// takes, and for the same reason: a revision bump under a running probe is
// another actor's write, and the losing note describes a record that no longer
// exists in that state. The handler must Ack.
func TestNoteDeadlineProbe_TheSecondReplicasCASIsRefused(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, logs, at := horizonEngine(s)

	const instanceID = "instNoteRace1"
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)

	// Replica A read the record at revision R and is still gathering evidence.
	replicaA, revisionR, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)

	// Replica B, one round trip ahead, writes its note first.
	replicaB, revisionB, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, revisionR, revisionB, "precondition: both replicas read the same revision")
	require.NoError(t, s.noteDeadlineProbe(ctx, replicaB, "replica B's verdict", time.Now(), revisionB))
	_, revisionAfterB, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.NotEqual(t, revisionR, revisionAfterB, "precondition: B's note moved the revision")

	// Replica A's verdict, carrying the revision it read at: refused, dropped,
	// acked.
	*at = time.Now().Add(opstatus.TrackerTTL + time.Hour)
	require.NoError(t, e.deadlineRejectedOrLost(ctx, replicaA, token, "replica A's verdict", revisionR),
		"a refused CAS is the probe's answer, not an error")

	survived, revisionNow, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, revisionAfterB, revisionNow, "the refused note must have written nothing")
	require.Equal(t, "replica B's verdict", survived.DeadlineProbe.Reason)
	require.Equal(t, StatusRunning, survived.Status)
	requireLogged(t, logs, "INFO", "loom: instance moved on under the probe; inconclusive deadline note dropped")
}

// TestOnDeadline_ARedeliveredMarkerRenotesAndFailsNothing pins the redelivery
// row: a late marker delivered twice reaches the same inconclusive verdict
// twice, and the second pass re-notes at the revision the first one left rather
// than failing the instance or erroring.
//
// The write is idempotent in effect and bounded in count by the marker's own
// lifetime, so re-noting is the cheapest correct answer — but it must be a
// re-note, which is what the moved stamp proves.
func TestOnDeadline_ARedeliveredMarkerRenotesAndFailsNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, at := horizonEngine(s)
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instNoteRedeliver1"
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)
	epoch, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)

	subjPrefix, marker := deadlineMarkerFor(s, instanceID)

	*at = epoch.Add(opstatus.TrackerTTL + time.Hour)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))
	first := requireNoteStands(ctx, t, s, instanceID, token)

	// The redelivery, one hour of engine clock later.
	*at = at.Add(time.Hour)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))
	second := requireNoteStands(ctx, t, s, instanceID, token)

	require.NotEqual(t, first.At, second.At, "the redelivery must re-note at the revision the first pass left")
	require.Equal(t, substrate.FormatTimestamp(*at), second.At)
}
