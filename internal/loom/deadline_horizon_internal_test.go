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
	require.Contains(t, note.Reason, substrate.FormatTimestamp(epoch),
		"the epoch the guard compared must be the STEP's — the token pointer's stamp, not the record's "+
			"(which the note write itself moves) and not the wall clock")
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
	e, logs, at := horizonEngine(s)
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

	// The reason is half the verdict. This is the one arm that still terminates
	// past the horizon, so the terminal it writes must name the break the
	// operator has to fix and not the arm's generic rejected-or-lost text — the
	// misleading-reason harm the guard exists to stop.
	terminal := requireLogged(t, logs, "WARN", "loom instance failed")
	require.Equal(t, "token pointer missing", terminal["reason"],
		"the terminal must name the invariant break, not the step's rejected-or-lost reason")
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
// The redrive row runs twice. Through RedriveInstance, which is the reachable
// path: an instance parked on an inconclusive verdict is redrivable, and the
// resume must not carry the verdict it just answered. And at the store, on a
// FAILED record carrying a note — a state no production path produces (every
// terminal runs through transition, which clears it) and which only a record
// written by an older binary could present, which is why the clear is settled in
// redrive's batch rather than assumed away.
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

	t.Run("RedriveInstance clears it: the operator answered what the probe could not", func(t *testing.T) {
		const instanceID = "instNoteRedrive1"
		_, pat, _ := seedOnStepZero(ctx, t, e, instanceID)
		registerPattern(e, pat)
		noted, _ := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe)

		require.NoError(t, e.RedriveInstance(ctx, instanceID),
			"a running instance carrying an inconclusive verdict is redrivable")
		after, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, after.Status)
		require.Equal(t, 0, after.Cursor, "the redrive resumes AT the parked cursor")
		require.Nil(t, after.DeadlineProbe, "a redriven instance carries no stale inconclusive verdict")
	})

	t.Run("the store's redrive clears it on a failed record too", func(t *testing.T) {
		const instanceID = "instNoteRedrive2"
		inst, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
		require.NoError(t, e.fail(ctx, inst, token0, "step 0 deadline exceeded; op rejected or lost", 0))
		noted, revision := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe, "seed: a failed record carrying a note, as an older binary could leave")

		noted.Status = StatusRunning
		require.NoError(t, s.redrive(ctx, noted, &pat, "", revision))
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

	// The property the guard's soundness rests on, and the reason the epoch is
	// read from the token pointer rather than from the record: the note the
	// first pass just wrote moved the RECORD's stamp, and the step's epoch must
	// not have moved with it. Were the epoch read from the record, this
	// redelivery would see a fresh epoch and FAIL a healthy parked instance —
	// the hazard that rules the record's timestamp out as a source.
	afterNote, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)
	require.Equal(t, epoch, afterNote,
		"the token pointer's stamp is the step's epoch: a note write must not move it")
	require.True(t, instanceRecordStamp(ctx, t, s, instanceID).After(afterNote),
		"precondition: the note DID move the record's own stamp, which is what makes the record unusable here")

	// The redelivery, one hour of engine clock later.
	*at = at.Add(time.Hour)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))
	second := requireNoteStands(ctx, t, s, instanceID, token)

	require.NotEqual(t, first.At, second.At, "the redelivery must re-note at the revision the first pass left")
	require.Equal(t, substrate.FormatTimestamp(*at), second.At)
}

// instanceRecordStamp is the substrate timestamp on the instance RECORD — the
// candidate epoch source the design rejects, read here so a test can assert
// which of the two stamps a verdict was judged against.
func instanceRecordStamp(ctx context.Context, t *testing.T, s *stateStore, instanceID string) time.Time {
	t.Helper()
	entry, err := s.conn.KVGet(ctx, s.bucket, instanceKey(instanceID))
	require.NoError(t, err)
	return entry.Timestamp
}

// oneStepExternalTaskPattern is a single-step externalTask pattern: the step
// kind whose deadline bounds the instanceOp SUBMISSION, probed through the
// instanceOp's own derived requestId while the pending token is the bare
// instance handle.
func oneStepExternalTaskPattern() Pattern {
	return Pattern{PatternID: "pext", SubjectType: "widget", MetaKey: "vtx.meta.pext", Steps: []Step{
		{Kind: StepKindExternalTask, Adapter: "backgroundCheck", InstanceOp: "CreateCheck", ReplyOp: "ResolveCheck"},
	}}
}

// seedStepZeroOfPattern drives a real createInstance + a real submitStep for
// pattern's step 0, so the instance is parked on exactly the token, outbox
// record and deadline arm that step kind writes in production, and then leaves
// the probe's rejected-or-lost preconditions in place: the outbox record cleared
// the way the relay clears it on publish-ack, and the arm removed so the marker
// the test delivers is the current one.
func seedStepZeroOfPattern(ctx context.Context, t *testing.T, e *Engine, pat Pattern, instanceID string) string {
	t.Helper()
	inst := &Instance{
		InstanceID: instanceID, PatternRef: pat.MetaKey, SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, e.state.createInstance(ctx, inst, &pat))
	require.NoError(t, e.submitStep(ctx, inst, &pat, "", tokenCreateOnly))
	require.NotEmpty(t, inst.PendingToken)
	clearOutbox(ctx, t, e.state, deriveRequestID(instanceID, 0))
	removeDeadlineArm(ctx, t, e.state, instanceID)
	return inst.PendingToken
}

// TestOnDeadline_TheHorizonGuardsEveryStepKindsRejectedOrLostVerdict pins the
// guard on the two arms the userTask cases above do not reach.
//
// All three step kinds read the same two absences and all three read them about
// the step the instance is parked on, so all three route their rejected-or-lost
// verdict through one helper — but a verdict that still calls probeFail directly
// is indistinguishable from a guarded one on the fresh-epoch side, which is the
// side every other test in the package exercises. Each case here parks its own
// step kind past the horizon, through that kind's real submitStep, and requires
// the inconclusive verdict.
//
// The tokens differ by kind (a systemOp's IS the op requestId; an
// externalTask's is the bare instance handle while its tracker and outbox are
// keyed by the instanceOp's derived requestId), which is the other thing these
// cases pin: the epoch is read from the PENDING token's pointer in every arm,
// not from whatever key that arm probes.
func TestOnDeadline_TheHorizonGuardsEveryStepKindsRejectedOrLostVerdict(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, logs, at := horizonEngine(s)
	startNotCommittedResponder(t, s.conn)

	for _, tc := range []struct {
		name       string
		instanceID string
		pattern    Pattern
		wantReason string
	}{
		{"systemOp", "instHorizonSysOp1", twoStepSystemPattern(), "op rejected or lost"},
		{"externalTask", "instHorizonExt1", oneStepExternalTaskPattern(), "instanceOp rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := seedStepZeroOfPattern(ctx, t, e, tc.pattern, tc.instanceID)
			epoch, err := s.tokenEpoch(ctx, token)
			require.NoError(t, err)

			*at = epoch.Add(opstatus.TrackerTTL + time.Hour)
			subjPrefix, marker := deadlineMarkerFor(s, tc.instanceID)
			require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))

			note := requireNoteStands(ctx, t, s, tc.instanceID, token)
			require.Contains(t, note.Reason, "INCONCLUSIVE")
			require.Contains(t, note.Reason, tc.wantReason,
				"the note carries this arm's own verdict text")
			require.Contains(t, note.Reason, substrate.FormatTimestamp(epoch),
				"this arm's epoch must be its PENDING token's pointer stamp")

			armed, err := s.deadlineArmed(ctx, tc.instanceID)
			require.NoError(t, err)
			require.False(t, armed, "an inconclusive verdict re-arms nothing")
		})
	}

	// Neither arm may terminate an instance past the horizon — the fail path is
	// what a reverted call site would take, and it is loud in the sink.
	require.NotContains(t, logs.String(), `"msg":"loom instance failed"`,
		"no arm may terminate an instance whose own evidence has aged out")
}

// TestDeadlineRejectedOrLost_ReadsTheStepsEpochAndNotTheRecords pins WHICH
// stamp the guard compares, on the one arrangement where the two candidates
// disagree.
//
// The instance record's own timestamp is the obvious alternative and is wrong
// for a reason this test stages: the note the verdict writes moves that stamp,
// so a redelivered marker would read a fresh epoch and FAIL a healthy parked
// instance — the exact harm the guard exists to stop, reintroduced by the
// substitution. The token pointer's stamp is the step's epoch by construction:
// the step's own transition wrote it and nothing since has touched it.
//
// The clock is parked at exactly one horizon past the POINTER's stamp. The
// pointer's age is then exactly the horizon (inclusive: absence stops being
// evidence AT the horizon, not after it), while the record — moved later by the
// first note — is younger than the horizon, so a record-reading guard would
// take the fail branch. Only one of the two readings leaves the instance
// running.
func TestDeadlineRejectedOrLost_ReadsTheStepsEpochAndNotTheRecords(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, at := horizonEngine(s)
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instEpochSource1"
	token := seedParkedUserTask(ctx, t, s, instanceID, time.Hour)
	removeDeadlineArm(ctx, t, s, instanceID)

	epoch, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)

	// A first verdict, which writes the note — and so moves the record's stamp
	// away from the step's epoch.
	*at = epoch.Add(opstatus.TrackerTTL + time.Hour)
	subjPrefix, marker := deadlineMarkerFor(s, instanceID)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))
	requireNoteStands(ctx, t, s, instanceID, token)

	stamp := instanceRecordStamp(ctx, t, s, instanceID)
	require.True(t, stamp.After(epoch),
		"precondition: the note moved the record's stamp past the step's epoch")
	unchanged, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)
	require.Equal(t, epoch, unchanged, "the step's epoch must not move when the record is written")

	// The separating instant: exactly one horizon past the step's epoch, which
	// is INSIDE the horizon as measured from the record.
	*at = epoch.Add(opstatus.TrackerTTL)
	require.True(t, at.Sub(stamp) < opstatus.TrackerTTL,
		"precondition: judged against the record, this instant is inside the horizon")
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))

	still := requireNoteStands(ctx, t, s, instanceID, token)
	require.Equal(t, substrate.FormatTimestamp(*at), still.At,
		"the re-note is stamped at the instant the verdict was reached")
	require.Contains(t, still.Reason, substrate.FormatTimestamp(epoch),
		"and it names the step's epoch, which is the stamp the comparison used")
}

// TestTokenEpoch_RestartsWhenARedriveResumesTheStep pins the state-table row
// that keeps a redriven instance out of the inconclusive verdict: the resumed
// step's pointer is re-put, so its epoch is the redrive's instant and not the
// original submission's.
//
// Without that reset a redrive of a day-old step would be re-noted by the very
// next marker — the operator's action would change nothing — and a redrive is
// the only verb that reaches a parked instance at all.
func TestTokenEpoch_RestartsWhenARedriveResumesTheStep(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	const instanceID = "instEpochRedrive1"
	inst, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
	registerPattern(e, pat)
	before, err := s.tokenEpoch(ctx, token0)
	require.NoError(t, err)

	// A real terminal, then a real redrive: the resumed step re-derives the very
	// token the failure removed.
	require.NoError(t, e.fail(ctx, inst, token0, "step 0 deadline exceeded; op rejected or lost", 0))
	_, err = s.tokenEpoch(ctx, token0)
	require.ErrorIs(t, err, errTokenPointerMissing, "precondition: the terminal removed the pointer")

	require.NoError(t, e.RedriveInstance(ctx, instanceID))
	resumed, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, token0, resumed.PendingToken, "the redrive re-derives the same step token")

	after, err := s.tokenEpoch(ctx, token0)
	require.NoError(t, err)
	require.True(t, after.After(before),
		"the redrive's re-put restarts the step's epoch (before=%s after=%s)", before, after)
}

// TestRedriveInstance_AcceptsAnInstanceParkedOnAnInconclusiveVerdict pins the
// operator verb the inconclusive verdict needs.
//
// A parked instance's deadline expired and nothing re-arms, so on the reading
// where the step's op was genuinely rejected no completion will ever come: it is
// stalled exactly as a failed instance is, and a redrive is what resumes it. The
// verdict's note is the gate — this case seeds it through the probe itself, not
// by hand, so the state redrive accepts is the state the engine produces.
//
// What the resume must leave behind: a running instance on a freshly re-put
// token with a live arm and its op back in the outbox, and no note — the
// operator has answered what the probe could not.
func TestRedriveInstance_AcceptsAnInstanceParkedOnAnInconclusiveVerdict(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, at := horizonEngine(s)
	startNotCommittedResponder(t, s.conn)

	const instanceID = "instRedriveParked1"
	pat := twoStepSystemPattern()
	token := seedStepZeroOfPattern(ctx, t, e, pat, instanceID)
	registerPattern(e, pat)

	epoch, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)
	*at = epoch.Add(opstatus.TrackerTTL + time.Hour)
	subjPrefix, marker := deadlineMarkerFor(s, instanceID)
	require.Equal(t, substrate.Ack, e.handleDeadline(ctx, subjPrefix, marker))
	requireNoteStands(ctx, t, s, instanceID, token)

	require.NoError(t, e.RedriveInstance(ctx, instanceID))

	resumed, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, resumed.Status)
	require.Equal(t, 0, resumed.Cursor, "the resume is AT the parked cursor, never a restart")
	require.Equal(t, token, resumed.PendingToken, "the resumed step re-derives its own token")
	require.Nil(t, resumed.DeadlineProbe, "the resume settles the verdict it answered")

	resumedEpoch, err := s.tokenEpoch(ctx, token)
	require.NoError(t, err)
	require.True(t, resumedEpoch.After(epoch), "the resumed step's epoch restarts, so the probe reads it as fresh")
	_, err = s.conn.KVGet(ctx, s.bucket, outboxKey(deriveRequestID(instanceID, 0)))
	require.NoError(t, err, "the resumed step's op is back in the outbox for the relay")
	armed, err := s.deadlineArmed(ctx, instanceID)
	require.NoError(t, err)
	require.True(t, armed, "the resumed step arms its own deadline again")
}

// TestRedriveInstance_RefusesARunningInstanceWithNoInconclusiveVerdict keeps the
// gate's original purpose: a running instance with no standing verdict is
// progressing and still backstopped by a live deadline, so a redrive would
// re-submit a step whose op may be seconds from committing. The refusal is the
// same typed error a complete instance gets, and it names what the record
// actually holds.
func TestRedriveInstance_RefusesARunningInstanceWithNoInconclusiveVerdict(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	const instanceID = "instRedriveRunning1"
	_, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
	registerPattern(e, pat)

	err := e.RedriveInstance(ctx, instanceID)
	require.ErrorIs(t, err, errInstanceNotFailed,
		"a plain running instance is progressing: the note is the whole gate")
	require.Contains(t, err.Error(), "status=running")
	require.Contains(t, err.Error(), "deadlineProbe=false")

	untouched, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, token0, untouched.PendingToken, "a refused redrive writes nothing")
	require.Equal(t, 0, untouched.Cursor)
}

// TestRedriveInstance_ACompletionUnderTheRedriveWins pins the race guard on the
// arm that has a live token to race with.
//
// Both halves are the same fact from two sides. At the store: the redrive's
// batch is conditioned on the revision the call read, so a completion that
// lands first bumps the revision and the redrive is rejected WHOLE — the
// instance keeps the progress it just made rather than being re-submitted over
// it. And at the control surface: that same completion cleared the note in its
// advance, so the operator's next attempt is refused by the gate itself, which
// is the honest answer — there is nothing stalled to redrive any more.
func TestRedriveInstance_ACompletionUnderTheRedriveWins(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	const instanceID = "instRedriveRace1"
	_, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
	registerPattern(e, pat)
	noted, revision := noteOnRecord(ctx, t, s, instanceID)
	require.NotNil(t, noted.DeadlineProbe)

	// The completion lands under the redrive: a real advance off the parked
	// step, which bumps the record's revision.
	require.NoError(t, e.advance(ctx, instanceID, token0))
	advanced, revisionAfter, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, 1, advanced.Cursor, "precondition: the completion advanced the instance")
	require.NotEqual(t, revision, revisionAfter)

	// The redrive's own batch, at the revision it read: refused whole.
	noted.Status = StatusRunning
	require.Error(t, s.redrive(ctx, noted, &pat, token0, revision),
		"a completion landing first must refuse the redrive, not be overwritten by it")
	survived, revisionNow, err := s.getInstanceAtRevision(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, revisionAfter, revisionNow, "the refused batch must have written nothing")
	require.Equal(t, 1, survived.Cursor)
	require.Nil(t, survived.DeadlineProbe, "the advance settled the note on its way past the step")

	// And the gate refuses the retry: the advance cleared what made it
	// redrivable.
	require.ErrorIs(t, e.RedriveInstance(ctx, instanceID), errInstanceNotFailed,
		"an instance that has since advanced is not stalled")
}

// TestRedriveInstance_AbandonsTheParkedStepsPointerInItsOwnBatch pins the one
// op that makes redriving a RUNNING instance safe, and the reason it is
// conditional.
//
// A parked instance's token pointer is live — unlike a failed one's, which its
// terminal removed — so a completion for that step can arrive at any moment,
// including between the redrive's CAS batch and the resumed step's submission.
// Resolving it there would advance the cursor under a re-submission of the same
// step, leaving the record on a step whose successor has already run. The batch
// therefore gives the pointer up: handleCompletion's resolveToken then finds no
// live token for such a completion and drops it, which is the honest
// consequence of an operator declaring the step unanswerable.
//
// The second case is why the op is conditional rather than unconditional. A
// failed instance has no pointer to abandon, and purging a subject that holds
// nothing MINTS a marker on it — the growth hazard deleteToken documents — so
// that path passes no token and its batch stays three writes wide.
func TestRedriveInstance_AbandonsTheParkedStepsPointerInItsOwnBatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := horizonContext()
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e, _, _ := horizonEngine(s)

	t.Run("a parked step's pointer is given up, so a late completion resolves nothing", func(t *testing.T) {
		const instanceID = "instAbandonParked1"
		_, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
		noted, revision := noteOnRecord(ctx, t, s, instanceID)
		require.NotNil(t, noted.DeadlineProbe)
		_, ok, err := s.resolveToken(ctx, token0)
		require.NoError(t, err)
		require.True(t, ok, "precondition: a parked step's pointer is live")

		// The CAS half of RedriveInstance, run alone so the intermediate state
		// it leaves is observable.
		before := streamWrites(ctx, t, s.conn)
		noted.Status = StatusRunning
		noted.PendingToken = ""
		require.NoError(t, s.redrive(ctx, noted, &pat, token0, revision))
		require.Equal(t, uint64(4), streamWrites(ctx, t, s.conn)-before,
			"record, pin, failed index and the abandoned pointer — one batch, four writes")

		_, ok, err = s.resolveToken(ctx, token0)
		require.NoError(t, err)
		require.False(t, ok,
			"the pointer a late completion would correlate on must be gone before the step is re-submitted")
		inWindow, err := s.getInstance(ctx, instanceID)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, inWindow.Status)
		require.Empty(t, inWindow.PendingToken, "the window is running-with-no-pending-token")
	})

	t.Run("a failed step has no pointer to abandon, and none is minted", func(t *testing.T) {
		const instanceID = "instAbandonFailed1"
		inst, pat, token0 := seedOnStepZero(ctx, t, e, instanceID)
		require.NoError(t, e.fail(ctx, inst, token0, "step 0 deadline exceeded; op rejected or lost", 0))
		failed, revision, err := s.getInstanceAtRevision(ctx, instanceID)
		require.NoError(t, err)

		before := streamWrites(ctx, t, s.conn)
		failed.Status = StatusRunning
		require.NoError(t, s.redrive(ctx, failed, &pat, failed.PendingToken, revision))
		require.Equal(t, uint64(3), streamWrites(ctx, t, s.conn)-before,
			"record, pin and failed index only: an absent pointer is not purged, so no marker is minted on it")
	})
}
