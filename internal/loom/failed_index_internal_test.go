package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// failedIndexPattern is the one-step systemOp pattern these tests drive: enough
// for a real create → submit → fail / advance sequence, so every marker asserted
// below was written by the production transition batch and not by a fixture.
func failedIndexPattern() Pattern {
	return Pattern{PatternID: "p1", SubjectType: "widget", MetaKey: "vtx.meta.p1", Steps: []Step{
		{Kind: StepKindSystemOp, Operation: "StepA"},
	}}
}

// newFailedIndexID mints a fresh valid 20-character NanoID (Contract #1
// alphabet) for an instance these tests seed, so no fixture depends on an id
// shape production cannot produce.
func newFailedIndexID(t *testing.T) string {
	t.Helper()
	id, err := keys.NewNanoID()
	require.NoError(t, err)
	return id
}

// requireFailedIndexed asserts instanceID carries a failed-index marker holding
// the empty-object body the index is written with.
func requireFailedIndexed(ctx context.Context, t *testing.T, s *stateStore, instanceID string) {
	t.Helper()
	entry, err := s.conn.KVGet(ctx, s.bucket, failedMarkerKey(instanceID))
	require.NoError(t, err, "%s must be indexed as failed", instanceID)
	require.JSONEq(t, failedMarkerBody, string(entry.Value))
}

// requireNotFailedIndexed asserts instanceID carries no failed-index marker —
// absent, or standing behind a removal marker, which read the same way.
func requireNotFailedIndexed(ctx context.Context, t *testing.T, s *stateStore, instanceID string) {
	t.Helper()
	_, err := s.conn.KVGet(ctx, s.bucket, failedMarkerKey(instanceID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "%s must not be indexed as failed", instanceID)
}

// driveToComplete runs instanceID through the production sequence to a real
// completion: create, submit step 0, then advance on that step's token, which
// runs off the end of the one-step pattern and completes the instance.
func driveToComplete(t *testing.T, ctx context.Context, e *Engine, pat *Pattern, instanceID string) {
	t.Helper()
	driveToRunning(t, ctx, e, pat, instanceID)
	require.NoError(t, e.advance(ctx, instanceID, deriveRequestID(instanceID, 0)))
	done, err := e.state.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusComplete, done.Status, "precondition: the instance completed for real")
}

// driveToRunning creates instanceID and parks it on its first step, the state a
// running instance is in between transitions.
func driveToRunning(t *testing.T, ctx context.Context, e *Engine, pat *Pattern, instanceID string) {
	t.Helper()
	inst := &Instance{
		InstanceID: instanceID, PatternRef: "vtx.meta." + pat.PatternID, SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, e.state.createInstance(ctx, inst, pat))
	require.NoError(t, e.submitStep(ctx, inst, pat, "", tokenCreateOnly))
}

// TestTransition_FailedArmIndexes_CompleteArmSettlesIt pins what each terminal
// arm does to the index. Both instances reach terminal through the production
// path, so the assertion is about the batch the engine really commits.
func TestTransition_FailedArmIndexes_CompleteArmSettlesIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	failed, done := newFailedIndexID(t), newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, failed)
	driveToComplete(t, ctx, e, &pat, done)

	requireFailedIndexed(ctx, t, e.state, failed)
	requireNotFailedIndexed(ctx, t, e.state, done)

	// The index is a membership claim only: the cursor stays the authority on
	// status, and the failed record is still readable by id.
	stored, err := e.state.getInstance(ctx, failed)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, stored.Status)
}

// TestTransition_CompleteArmHealsAStaleMarkerOnARunningInstance is the negative
// vector for the one window in which the index can disagree with its instance: a
// marker standing on a RUNNING cursor. Only a race can produce it — the backfill
// classifying an instance as failed, a redrive landing, then the backfill's PUT —
// so the test puts the marker there by hand and says so; nothing else may, and
// there is no production path that writes one.
//
// The complete arm is what heals it. Redrive refuses a non-failed instance, the
// failed arm only ever adds, and nothing sweeps the family — so without the
// removal on this arm the marker would outlive its instance and hold a completed
// flow in the operator's redrive queue forever.
func TestTransition_CompleteArmHealsAStaleMarkerOnARunningInstance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	driveToRunning(t, ctx, e, &pat, instanceID)
	_, err := conn.KVPut(ctx, "loom-state", failedMarkerKey(instanceID), []byte(failedMarkerBody))
	require.NoError(t, err)
	requireFailedIndexed(ctx, t, e.state, instanceID)

	require.NoError(t, e.advance(ctx, instanceID, deriveRequestID(instanceID, 0)))
	settled, err := e.state.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusComplete, settled.Status, "precondition: the instance completed for real")

	requireNotFailedIndexed(ctx, t, e.state, instanceID)
	requireExpiringPurgeMarker(ctx, t, e.state, failedMarkerKey(instanceID))

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Empty(t, got, "the healed instance is on neither family, so it is not listed")
}

// TestRedriveInstance_PurgesTheFailedIndex pins the removal: a redriven instance
// is no longer awaiting a redrive, so it must leave the enumerated queue in the
// same batch that flips it back to running — and it must leave it the way every
// removal in this bucket does, as an expiring purge marker rather than a
// permanent DEL.
func TestRedriveInstance_PurgesTheFailedIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, instanceID)
	requireFailedIndexed(ctx, t, e.state, instanceID)

	require.NoError(t, e.RedriveInstance(ctx, instanceID))

	requireNotFailedIndexed(ctx, t, e.state, instanceID)
	requireExpiringPurgeMarker(ctx, t, e.state, failedMarkerKey(instanceID))

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, StatusRunning, got[0].Status, "a redriven instance lists as running, not as failed")
}

// TestFailedIndex_RefailAfterRedriveRewritesTheMarker is the PUT-not-CreateOnly
// proof, and the one test that would have caught dossier entry one on this key.
// The redrive purges the marker, which leaves a removal marker standing on that
// subject for tombstoneTTL; a CreateOnly write is refused by exactly such a
// marker ("subject must be empty"), so a second failure inside that minute would
// commit no index at all and the instance would be invisible to the redrive
// queue. The second failure runs immediately, well inside the marker's life.
func TestFailedIndex_RefailAfterRedriveRewritesTheMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	token := driveToFailedAtStepZero(t, ctx, e, &pat, instanceID)
	require.NoError(t, e.RedriveInstance(ctx, instanceID))

	// Precondition: the subject the second failure writes to carries the
	// redrive's removal marker, the state a create-only write cannot commit
	// against.
	requireExpiringPurgeMarker(ctx, t, e.state, failedMarkerKey(instanceID))

	resumed, err := e.state.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, resumed.Status)
	require.Equal(t, token, resumed.PendingToken, "the redrive re-derived the failed step's token")

	require.NoError(t, e.fail(ctx, resumed, token, "step 0 failed again after the redrive", 0))

	requireFailedIndexed(ctx, t, e.state, instanceID)
	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, StatusFailed, got[0].Status, "the re-failed instance is back in the redrive queue")
}

// TestListInstances_ActionableInstancesOnly pins the narrowed list against state
// left by real transitions: the running and failed instances are returned, sorted
// by instanceId, and the completed one is absent — while still being answerable
// by id, which is what makes the narrowing sound.
func TestListInstances_ActionableInstancesOnly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	running, failed, done := newFailedIndexID(t), newFailedIndexID(t), newFailedIndexID(t)
	driveToRunning(t, ctx, e, &pat, running)
	driveToFailedAtStepZero(t, ctx, e, &pat, failed)
	driveToComplete(t, ctx, e, &pat, done)

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2, "running ∪ failed; the completed instance is not enumerable")

	byID := map[string]InstanceSummary{}
	ids := make([]string, 0, len(got))
	for _, inst := range got {
		byID[inst.InstanceID] = inst
		ids = append(ids, inst.InstanceID)
	}
	require.NotContains(t, byID, done, "a completed instance must not be listed")
	require.Equal(t, StatusRunning, byID[running].Status)
	require.Equal(t, "vtx.meta.p1", byID[running].PatternRef)
	require.Equal(t, StatusFailed, byID[failed].Status)
	require.Equal(t, 1, byID[failed].RetryCount, "the failure stamped its retry count on the record")
	require.Less(t, ids[0], ids[1], "results are sorted by instanceId")

	// Absent from the list, present by id — the promise the narrowing rests on.
	detail, err := e.InspectInstance(ctx, done)
	require.NoError(t, err)
	require.True(t, detail.Terminal)
	require.Equal(t, StatusComplete, detail.Instance.Status)
}

// TestListInstances_BothFamiliesNameOneInstanceListsItOnce pins the union: an
// instance named by the pin family AND the failed family is one row, not two. The
// state is reachable in production either way round (a redrive re-pins before its
// batch removes the marker on a replica reading in between); the pin here is
// restored by hand to hold it still.
func TestListInstances_BothFamiliesNameOneInstanceListsItOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, instanceID)
	requireFailedIndexed(ctx, t, e.state, instanceID)
	putPin(t, ctx, conn, instanceID, pat)

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1, "one instance named by both families is one row")
	require.Equal(t, instanceID, got[0].InstanceID)
	require.Equal(t, StatusFailed, got[0].Status, "the record decides what it reads as")
}

// TestListInstances_CompleteRecordLoggedByTheFamilyThatNamedIt pins the two
// meanings of an excluded complete record. Named by the PIN family, it is the
// benign resolve-then-read race — the instance completed in the window — and is
// Debug. Named by the FAILED family, the index disagrees with a record that will
// not change again, which is a stale marker and is Warn.
func TestListInstances_CompleteRecordLoggedByTheFamilyThatNamedIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("pin family: a race, at Debug", func(t *testing.T) {
		t.Parallel()
		conn, ctx := newControlTestConn(t)
		e := newControlEngine(conn)
		pat := failedIndexPattern()
		registerPattern(e, pat)

		instanceID := newFailedIndexID(t)
		driveToComplete(t, ctx, e, &pat, instanceID)
		// The pin the terminal batch removed, standing again: exactly what the
		// resolution sees when an instance completes under it.
		putPin(t, ctx, conn, instanceID, pat)

		var logged bytes.Buffer
		insts, err := e.state.listInstances(ctx, bufferLogger(&logged))
		require.NoError(t, err)
		require.Empty(t, insts)
		require.Contains(t, logged.String(), "completed under the read")
		require.NotContains(t, logged.String(), "level=WARN")
	})

	t.Run("failed family: a stale index, at Warn", func(t *testing.T) {
		t.Parallel()
		conn, ctx := newControlTestConn(t)
		e := newControlEngine(conn)
		pat := failedIndexPattern()
		registerPattern(e, pat)

		instanceID := newFailedIndexID(t)
		driveToComplete(t, ctx, e, &pat, instanceID)
		// A marker on a completed instance: unreachable in production now that
		// the complete arm settles the index, so it is written by hand.
		_, err := conn.KVPut(ctx, "loom-state", failedMarkerKey(instanceID), []byte(failedMarkerBody))
		require.NoError(t, err)

		var logged bytes.Buffer
		insts, err := e.state.listInstances(ctx, bufferLogger(&logged))
		require.NoError(t, err)
		require.Empty(t, insts, "the record's status decides, not the index that named it")
		require.Contains(t, logged.String(), "level=WARN")
		require.Contains(t, logged.String(), "failed index names an instance whose record reads complete")
	})
}

// TestListInstances_UnparseableRecordIsSkipped pins the skip-and-log posture of a
// poisoned record on the narrowed path: it is excluded, the rest of the list still
// answers, and the call does not fail.
func TestListInstances_UnparseableRecordIsSkipped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	running, failed := newFailedIndexID(t), newFailedIndexID(t)
	driveToRunning(t, ctx, e, &pat, running)
	driveToFailedAtStepZero(t, ctx, e, &pat, failed)
	_, err := conn.KVPut(ctx, "loom-state", instanceKey(failed), []byte("{not json"))
	require.NoError(t, err)

	var logged bytes.Buffer
	insts, err := e.state.listInstances(ctx, bufferLogger(&logged))
	require.NoError(t, err)
	require.Len(t, insts, 1, "one poisoned record must not blind the operator to the others")
	require.Equal(t, running, insts[0].InstanceID)
	require.Contains(t, logged.String(), "unparseable")
}

// TestListInstances_ResolutionErrorIsHard pins the fail-closed posture: the
// resolution is complete-or-error, so a failure cannot be reported as an empty
// queue.
func TestListInstances_ResolutionErrorIsHard(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	sentinel := errors.New("resolution refused")
	e.state.lister = failingLister{inner: conn, err: sentinel}

	got, err := e.ListInstances(ctx)
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, got, "a partial queue must never be returned")
}

// TestListInstances_ResolvesBothFamiliesServerSide pins the selection the list
// hands the server. It is the revert-proof for the bound AND for the
// completeness: a key listing over the same filters (KVListKeysFilter) returns
// the same answer on a quiet bucket, so only the request itself shows which
// primitive was used.
func TestListInstances_ResolvesBothFamiliesServerSide(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	rec := &recordingLister{inner: conn}
	e.state.lister = rec
	pat := failedIndexPattern()
	registerPattern(e, pat)

	driveToRunning(t, ctx, e, &pat, newFailedIndexID(t))

	_, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Equal(t, [][]string{{patternPinFilter, failedMarkerFilter}}, rec.resolved,
		"one complete resolution over the two index families — not a key listing, and not the cursor family")
}

// --- Backfill ----------------------------------------------------------------

// unindexFailure removes an instance's failed-index marker the way an operator
// clearing state by hand would, leaving a cursor that reads `failed` with no
// marker beside it — the bucket state the backfill exists to settle. The subject
// is left carrying a DEL marker, which is not the same as never having been
// written: a plain PUT commits against either, which is exactly why the backfill
// (and transition's failed arm) never uses CreateOnly.
func unindexFailure(ctx context.Context, t *testing.T, conn *substrate.Conn, bucket, instanceID string) {
	t.Helper()
	require.NoError(t, conn.KVDelete(ctx, bucket, failedMarkerKey(instanceID)))
	_, err := conn.KVGet(ctx, bucket, failedMarkerKey(instanceID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "seed precondition: %s must be unindexed", instanceID)
}

// requireBackfillSentinel asserts the completion sentinel is present, which is
// what makes every later pass a single GET.
func requireBackfillSentinel(ctx context.Context, t *testing.T, s *stateStore) *substrate.KVEntry {
	t.Helper()
	entry, err := s.conn.KVGet(ctx, s.bucket, failedIndexBackfillSentinelKey)
	require.NoError(t, err, "the completed pass must record its sentinel")
	var sentinel failedIndexSentinel
	require.NoError(t, json.Unmarshal(entry.Value, &sentinel))
	require.NotEmpty(t, sentinel.CompletedAt)
	return entry
}

// TestBackfillFailedIndex_SettlesAnUnindexedFailure is the migration's one
// consequential assertion: a cursor reading `failed` with no marker beside it
// must become visible to the redrive queue, and nothing else may be indexed with
// it.
func TestBackfillFailedIndex_SettlesAnUnindexedFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	failedA, failedB := newFailedIndexID(t), newFailedIndexID(t)
	running, done := newFailedIndexID(t), newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, failedA)
	driveToFailedAtStepZero(t, ctx, e, &pat, failedB)
	driveToRunning(t, ctx, e, &pat, running)
	driveToComplete(t, ctx, e, &pat, done)
	unindexFailure(ctx, t, conn, "loom-state", failedA)
	unindexFailure(ctx, t, conn, "loom-state", failedB)

	// Precondition: the queue is missing both failures.
	before, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, before, 1, "precondition: only the running instance is enumerable")

	summary := e.backfillFailedIndex(ctx)
	require.NoError(t, summary.err)
	require.NoError(t, summary.cancelled)
	require.False(t, summary.skipped)
	require.Equal(t, 4, summary.scanned, "every cursor is classified")
	require.Equal(t, 2, summary.marked)
	require.Zero(t, summary.alreadyMarked)
	require.Zero(t, summary.remaining)

	requireFailedIndexed(ctx, t, e.state, failedA)
	requireFailedIndexed(ctx, t, e.state, failedB)
	requireNotFailedIndexed(ctx, t, e.state, running)
	requireNotFailedIndexed(ctx, t, e.state, done)
	requireBackfillSentinel(ctx, t, e.state)

	after, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, after, 3)
}

// TestBackfillFailedIndex_SettlesACursorWithNoPinAndNoMarkerSubject covers the
// honest pre-index INPUT: a cursor reading `failed` whose marker subject was never
// written at all, and which carries no pin either (its terminal removed it). No
// production path can produce that state now, so the record is written directly —
// for the backfill's input, unlike for its output, a hand-written cursor IS the
// state of record.
func TestBackfillFailedIndex_SettlesACursorWithNoPinAndNoMarkerSubject(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	instanceID := newFailedIndexID(t)
	putInstance(t, ctx, conn, Instance{
		InstanceID: instanceID, PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusFailed, RetryCount: 1,
	})
	_, err := conn.KVGet(ctx, "loom-state", failedMarkerKey(instanceID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "precondition: the marker subject was never written")

	summary := e.backfillFailedIndex(ctx)
	require.NoError(t, summary.err)
	require.Equal(t, 1, summary.marked)
	requireFailedIndexed(ctx, t, e.state, instanceID)

	got, err := e.ListInstances(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, instanceID, got[0].InstanceID)
}

// TestBackfillFailedIndex_SecondPassIsOneGet pins the sentinel gate: a pass after
// a completed one reads nothing but the sentinel, writes nothing, and leaves the
// marker it wrote at the revision it wrote it at.
func TestBackfillFailedIndex_SecondPassIsOneGet(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, instanceID)
	unindexFailure(ctx, t, conn, "loom-state", instanceID)

	counting := &countingFailedIndexStore{inner: conn}
	e.failedIndex = counting

	first := e.backfillFailedIndex(ctx)
	require.NoError(t, first.err)
	require.Equal(t, 1, first.marked)
	requireBackfillSentinel(ctx, t, e.state)
	markerEntry, err := conn.KVGet(ctx, "loom-state", failedMarkerKey(instanceID))
	require.NoError(t, err)

	require.Equal(t, [][]string{{failedMarkerFilter}, {instanceCursorFilter}}, counting.resolved,
		"the first pass resolves the marker family and the cursor family, once each")
	resolvedAfterFirst, putsAfterFirst := len(counting.resolved), counting.puts

	second := e.backfillFailedIndex(ctx)
	require.NoError(t, second.err)
	require.True(t, second.skipped, "the sentinel gates the pass")
	require.Zero(t, second.scanned)
	require.Zero(t, second.marked)
	require.Len(t, counting.resolved, resolvedAfterFirst, "a skipped pass reads no family at all")
	require.Equal(t, putsAfterFirst, counting.puts, "a skipped pass writes nothing")
	require.Equal(t, 2, counting.gets, "one sentinel GET per pass, and nothing else")

	after, err := conn.KVGet(ctx, "loom-state", failedMarkerKey(instanceID))
	require.NoError(t, err)
	require.Equal(t, markerEntry.Revision, after.Revision,
		"a skipped pass must not rewrite the marker it wrote")
}

// countingFailedIndexStore wraps a real connection and counts what the backfill
// asks of it, so a pass that should do nothing can be shown to have done nothing
// rather than merely to have changed nothing.
type countingFailedIndexStore struct {
	inner    failedIndexStore
	gets     int
	puts     int
	resolved [][]string
}

func (c *countingFailedIndexStore) KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error) {
	c.gets++
	return c.inner.KVGet(ctx, bucket, key)
}

func (c *countingFailedIndexStore) KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	c.resolved = append(c.resolved, append([]string(nil), keys...))
	return c.inner.KVGetMultiNoSnapshot(ctx, bucket, keys)
}

func (c *countingFailedIndexStore) KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	c.puts++
	return c.inner.KVPut(ctx, bucket, key, value)
}

// cancellingFailedIndexStore ends the pass's context after its first marker
// write, so an interruption lands exactly where a partial pass really stops:
// between two writes, with work left owing.
type cancellingFailedIndexStore struct {
	inner  failedIndexStore
	cancel context.CancelFunc
	puts   int
}

func (c *cancellingFailedIndexStore) KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error) {
	return c.inner.KVGet(ctx, bucket, key)
}

func (c *cancellingFailedIndexStore) KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	return c.inner.KVGetMultiNoSnapshot(ctx, bucket, keys)
}

func (c *cancellingFailedIndexStore) KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	rev, err := c.inner.KVPut(ctx, bucket, key, value)
	c.puts++
	if c.puts == 1 {
		c.cancel()
	}
	return rev, err
}

// TestBackfillFailedIndex_InterruptedBetweenWritesConverges pins the convergence
// posture where it actually matters: the pass is ended AFTER it has written one
// marker and before it has written the rest. It must report what it left, write no
// sentinel — so nothing gates the retry — and the next start must finish the work.
func TestBackfillFailedIndex_InterruptedBetweenWritesConverges(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)
	pat := failedIndexPattern()
	registerPattern(e, pat)

	const failures = 3
	ids := make([]string, 0, failures)
	for i := 0; i < failures; i++ {
		id := newFailedIndexID(t)
		driveToFailedAtStepZero(t, ctx, e, &pat, id)
		unindexFailure(ctx, t, conn, "loom-state", id)
		ids = append(ids, id)
	}

	passCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.failedIndex = &cancellingFailedIndexStore{inner: conn, cancel: cancel}

	interrupted := e.backfillFailedIndex(passCtx)
	require.Error(t, interrupted.cancelled, "the pass reports the cancellation, not a failure")
	require.NoError(t, interrupted.err)
	require.Equal(t, 1, interrupted.marked, "one marker landed before the interruption")
	require.Equal(t, failures-1, interrupted.remaining, "the rest are the next start's work")
	_, err := conn.KVGet(ctx, "loom-state", failedIndexBackfillSentinelKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"a partial pass must not gate the retry")

	indexed := 0
	for _, id := range ids {
		if _, err := conn.KVGet(ctx, "loom-state", failedMarkerKey(id)); err == nil {
			indexed++
		}
	}
	require.Equal(t, 1, indexed, "exactly the one write that landed")

	e.failedIndex = conn
	resumed := e.backfillFailedIndex(ctx)
	require.NoError(t, resumed.err)
	require.Equal(t, failures-1, resumed.marked, "the next start finishes the work")
	require.Equal(t, 1, resumed.alreadyMarked, "and skips what the interrupted pass wrote")
	for _, id := range ids {
		requireFailedIndexed(ctx, t, e.state, id)
	}
	requireBackfillSentinel(ctx, t, e.state)
}

// --- Start wiring ------------------------------------------------------------

// blockingFailedIndexStore holds the pass at its first read until released, so a
// test can observe that Start joins it rather than outliving it.
type blockingFailedIndexStore struct {
	inner   failedIndexStore
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (b *blockingFailedIndexStore) KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error) {
	if !b.once {
		b.once = true
		close(b.entered)
		<-b.release
	}
	return b.inner.KVGet(ctx, bucket, key)
}

func (b *blockingFailedIndexStore) KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	return b.inner.KVGetMultiNoSnapshot(ctx, bucket, keys)
}

func (b *blockingFailedIndexStore) KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	return b.inner.KVPut(ctx, bucket, key, value)
}

// TestEngineStart_SettlesTheFailedIndex pins the wiring on a bucket carrying the
// state the pass exists for: a real failure whose marker has been cleared. The
// pass runs off Start, and Start still returns cleanly on cancellation.
func TestEngineStart_SettlesTheFailedIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newSweepEngineBuckets(ctx, t)
	e := NewEngine(s.conn, Config{
		LoomStateBucket: s.bucket,
		CoreKVBucket:    "core-kv",
		EventsStream:    "core-events",
		ActorKey:        "vtx.identity.LoomSweepActor123456",
		Lane:            "system",
		Logger:          sweepLogger(&bytes.Buffer{}),
	})
	pat := failedIndexPattern()
	registerPattern(e, pat)

	instanceID := newFailedIndexID(t)
	driveToFailedAtStepZero(t, ctx, e, &pat, instanceID)
	unindexFailure(ctx, t, s.conn, s.bucket, instanceID)

	engCtx, engCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- e.Start(engCtx) }()

	require.Eventually(t, func() bool {
		_, err := s.conn.KVGet(ctx, s.bucket, failedMarkerKey(instanceID))
		return err == nil
	}, 30*time.Second, 100*time.Millisecond, "Start must settle the failed index")

	engCancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("engine did not stop")
	}
}

// TestEngineStart_JoinsTheFailedIndexPassBeforeReturning pins the join itself:
// with the pass held at its first read, a cancelled Start must NOT return until
// the pass does — otherwise the pass's reads, writes and log line land after its
// caller believes the engine is down.
func TestEngineStart_JoinsTheFailedIndexPassBeforeReturning(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newSweepEngineBuckets(ctx, t)
	blocking := &blockingFailedIndexStore{
		inner:   s.conn,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	e := NewEngine(s.conn, Config{
		LoomStateBucket: s.bucket,
		ActorKey:        "vtx.identity.LoomSweepActor123456",
		Logger:          sweepLogger(&bytes.Buffer{}),
	})
	e.failedIndex = blocking

	engCtx, engCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- e.Start(engCtx) }()

	select {
	case <-blocking.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the backfill pass never started")
	}
	engCancel()

	select {
	case err := <-errCh:
		t.Fatalf("Start returned while the pass was still running (err=%v)", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(blocking.release)
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after the pass was released")
	}
}
