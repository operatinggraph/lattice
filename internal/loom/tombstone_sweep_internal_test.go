package loom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- fixtures ---------------------------------------------------------------

// sweepLogger returns a Debug-level logger over buf, so a test can read the
// pass's summary line back as JSON.
func sweepLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// newSweepEngine builds an engine over the state store's connection and bucket.
// It is never Started: the conversion pass reads and writes loom-state directly
// and needs no consumer attached.
func newSweepEngine(s *stateStore, logger *slog.Logger) *Engine {
	return NewEngine(s.conn, Config{
		LoomStateBucket: s.bucket,
		ActorKey:        "vtx.identity.LoomSweepActor123456",
		Logger:          logger,
	})
}

// sweepFixturePattern is the one-step pattern the seeded instances pin.
func sweepFixturePattern() Pattern {
	return Pattern{PatternID: "p1", SubjectType: "widget", MetaKey: "vtx.meta.p1", Steps: []Step{
		{Kind: StepKindSystemOp, Operation: "StepA"},
	}}
}

// seedRunningInstance drives a real create + one real transition, so the
// instance holds every key family at once: the cursor, the pattern pin, a step
// token, an outbox record and an armed deadline.
func seedRunningInstance(ctx context.Context, t *testing.T, s *stateStore, instanceID, token string) *Instance {
	t.Helper()
	pat := sweepFixturePattern()
	inst := &Instance{
		InstanceID: instanceID, PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, s.createInstance(ctx, inst, &pat))

	inst.PendingToken = token
	ob := &outboxRecord{RequestID: token, Operation: "StepA", Lane: "system", Actor: "vtx.identity.LoomSeedActor1234567"}
	// An hour, not a minute: a deadline that could expire inside the test's
	// own budget would move the subject's sequence, and the live-key
	// assertions compare exactly that.
	require.NoError(t, s.transition(ctx, inst, token, "", tokenCreateOnly, ob, time.Hour))
	for _, key := range []string{
		instanceKey(instanceID), patternPinKey(instanceID),
		tokenKey(token), outboxKey(token), deadlineKey(instanceID),
	} {
		_, err := s.conn.KVGet(ctx, s.bucket, key)
		require.NoError(t, err, "seed precondition: %s must hold a value", key)
	}
	return inst
}

// markTerminal flips a seeded instance's cursor record to a terminal status
// without running a transition, so the instance's ephemeral subjects stay
// exactly as the fixture arranged them and only the status the deadline
// family's guard reads changes.
func markTerminal(ctx context.Context, t *testing.T, s *stateStore, inst *Instance) {
	t.Helper()
	inst.Status = StatusComplete
	body, err := json.Marshal(inst)
	require.NoError(t, err)
	_, err = s.conn.KVPut(ctx, s.bucket, instanceKey(inst.InstanceID), body)
	require.NoError(t, err)
}

// seedTerminalInstance is seedRunningInstance with a terminal cursor — the
// shape every legacy marker in the live bucket stands on, since the removal
// that left it was written by the transition that ended the instance.
func seedTerminalInstance(ctx context.Context, t *testing.T, s *stateStore, instanceID, token string) *Instance {
	t.Helper()
	inst := seedRunningInstance(ctx, t, s, instanceID, token)
	markTerminal(ctx, t, s, inst)
	return inst
}

// legacyDelete removes a key the way a plain KV delete does: a permanent DELETE
// marker on a history-1 subject, which is the residue the conversion pass
// exists to clear. Production code cannot write this shape (the removal sites
// all purge with a TTL), so the fixture writes it directly.
func legacyDelete(ctx context.Context, t *testing.T, s *stateStore, keys ...string) {
	t.Helper()
	for _, key := range keys {
		require.NoError(t, s.conn.KVDelete(ctx, s.bucket, key))
		hdr, _ := markerOn(ctx, t, s, key)
		require.Equal(t, "DEL", hdr.Get("KV-Operation"),
			"seed precondition: %s must carry a permanent delete marker", key)
	}
}

// requireListedTombstone asserts key is enumerated as a delete marker under
// filter, and returns the marker the listing saw.
func requireListedTombstone(ctx context.Context, t *testing.T, s *stateStore, filter, key string) substrate.KVTombstone {
	t.Helper()
	markers, err := s.conn.KVListTombstones(ctx, s.bucket, filter)
	require.NoError(t, err)
	for _, m := range markers {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("%s was not listed as a delete marker under %q (got %v)", key, filter, markers)
	return substrate.KVTombstone{}
}

// familyResult returns the pass's result for one filter.
func familyResult(t *testing.T, s legacySweepSummary, filter string) legacySweepFamilyResult {
	t.Helper()
	for _, f := range s.families {
		if f.filter == filter {
			return f
		}
	}
	t.Fatalf("family %q was not visited (visited %d families)", filter, len(s.families))
	return legacySweepFamilyResult{}
}

// streamLastSeq is the loom-state backing stream's last sequence — the probe
// for "the pass published nothing".
func streamLastSeq(ctx context.Context, t *testing.T, s *stateStore) uint64 {
	t.Helper()
	stream, err := s.conn.JetStream().Stream(ctx, "KV_"+s.bucket)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	return info.State.LastSeq
}

// --- the pass ---------------------------------------------------------------

// TestSweepLegacyTombstones_ConvertsEveryFamilyAndSparesLiveKeys is the pass's
// central proof: every permanent delete marker across the four ephemeral
// families becomes an expiring purge marker and leaves the tombstone listing,
// while nothing a live instance holds — and no instance cursor in any state —
// is touched.
func TestSweepLegacyTombstones_ConvertsEveryFamilyAndSparesLiveKeys(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))

	// A terminated instance whose ephemeral keys were removed the legacy way:
	// one key in each of the four families, its cursor left in place.
	const legacyID, legacyToken = "instLegacy1", "tokLegacy1"
	seedTerminalInstance(ctx, t, s, legacyID, legacyToken)
	legacyKeys := []string{
		patternPinKey(legacyID), tokenKey(legacyToken),
		outboxKey(legacyToken), deadlineKey(legacyID),
	}
	legacyDelete(ctx, t, s, legacyKeys...)

	// A running instance: every one of its keys must survive untouched.
	const liveID, liveToken = "instLive1", "tokLive1"
	seedRunningInstance(ctx, t, s, liveID, liveToken)
	liveKeys := []string{
		instanceKey(liveID), patternPinKey(liveID),
		tokenKey(liveToken), outboxKey(liveToken), deadlineKey(liveID),
	}
	liveSeq := map[string]uint64{}
	liveBody := map[string][]byte{}
	for _, key := range liveKeys {
		_, seq := markerOn(ctx, t, s, key)
		liveSeq[key] = seq
		entry, err := s.conn.KVGet(ctx, s.bucket, key)
		require.NoError(t, err)
		liveBody[key] = entry.Value
	}

	// A cursor subject carrying a delete marker of its own. The pass never
	// enumerates the cursor family, so no state a cursor can be in is
	// reachable by it — this marker must still be exactly what it was.
	const strayCursorID = "instStrayCur1"
	_, err := s.conn.KVPut(ctx, s.bucket, instanceKey(strayCursorID), []byte(`{"instanceId":"instStrayCur1"}`))
	require.NoError(t, err)
	legacyDelete(ctx, t, s, instanceKey(strayCursorID))
	_, strayCursorSeq := markerOn(ctx, t, s, instanceKey(strayCursorID))

	summary := e.sweepLegacyTombstones(ctx)
	require.NoError(t, summary.err, "the pass must run to the end of every family")
	require.Len(t, summary.families, 4)
	for _, fam := range e.legacyTombstoneFamilies() {
		res := familyResult(t, summary, fam.filter)
		require.Equal(t, 1, res.listed, "%s: exactly the one legacy marker is listed", fam.filter)
		require.Equal(t, 1, res.converted, "%s", fam.filter)
		require.Zero(t, res.skippedMismatch, "%s", fam.filter)
		require.Zero(t, res.skippedRunning, "%s", fam.filter)
	}

	// Every converted subject now carries the expiring purge marker and is out
	// of the tombstone listing. The subject's departure from the stream is the
	// server's own TTL behaviour, pinned by the substrate's purge-TTL test —
	// what this pass owns is the marker it leaves.
	for _, key := range legacyKeys {
		requireExpiringPurgeMarker(ctx, t, s, key)
	}

	// The terminated instance's cursor still reads.
	got, err := s.getInstance(ctx, legacyID)
	require.NoError(t, err)
	require.NotNil(t, got, "the cursor is the one permanent subject and the pass must not reach it")

	// Nothing the running instance holds moved.
	for _, key := range liveKeys {
		_, seq := markerOn(ctx, t, s, key)
		require.Equal(t, liveSeq[key], seq, "%s: a live key must not be republished", key)
		entry, err := s.conn.KVGet(ctx, s.bucket, key)
		require.NoError(t, err, "%s must still read as a value", key)
		require.Equal(t, liveBody[key], entry.Value, "%s: body changed", key)
	}

	// The stray cursor marker is untouched — still a DELETE, same revision.
	hdr, seq := markerOn(ctx, t, s, instanceKey(strayCursorID))
	require.Equal(t, "DEL", hdr.Get("KV-Operation"), "the cursor family is never enumerated")
	require.Equal(t, strayCursorSeq, seq)

	// The summary line: one Info record naming each family's counts.
	rec := lastSweepRecord(t, &logs)
	require.Equal(t, "INFO", rec["level"])
	require.Equal(t, "loom: legacy tombstone conversion pass", rec["msg"])
	for _, fam := range e.legacyTombstoneFamilies() {
		require.Equal(t, "listed=1 converted=1 skippedMismatch=0 skippedRunning=0", rec[fam.filter], "family %s", fam.filter)
	}
	require.NotEmpty(t, rec["elapsed"])
	require.NotContains(t, rec, "stoppedAt")
}

// lastSweepRecord decodes the last conversion-pass log record written to buf.
func lastSweepRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "log line %q", line)
		if rec["msg"] == "loom: legacy tombstone conversion pass" {
			last = rec
		}
	}
	require.NotNil(t, last, "no conversion-pass summary line was logged")
	return last
}

// TestConvertFamily_SkipsReCreatedKey pins the outcome the whole pass rests on:
// a key re-created between the listing and the publish is skipped, not purged.
// The conversion is conditioned on the revision the listing saw, so the live
// value it now carries survives untouched.
func TestConvertFamily_SkipsReCreatedKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))

	const instanceID = "instRepin1"
	seedRunningInstance(ctx, t, s, instanceID, "tokRepin1")
	pinKey := patternPinKey(instanceID)
	legacyDelete(ctx, t, s, pinKey)

	fam := e.legacyTombstoneFamilies()[0]
	marker := requireListedTombstone(ctx, t, s, fam.filter, pinKey)

	// A redrive re-pins the pattern after the listing and before the publish.
	pinBody, err := json.Marshal(sweepFixturePattern())
	require.NoError(t, err)
	rePinRev, err := s.conn.KVPut(ctx, s.bucket, pinKey, pinBody)
	require.NoError(t, err)

	res := e.convertFamily(ctx, fam, []substrate.KVTombstone{marker})
	require.NoError(t, res.err)
	require.Equal(t, 1, res.listed)
	require.Zero(t, res.converted, "the marker the listing saw is gone; nothing to convert")
	require.Equal(t, 1, res.skippedMismatch)

	entry, err := s.conn.KVGet(ctx, s.bucket, pinKey)
	require.NoError(t, err, "the re-created pin must survive the pass")
	require.Equal(t, pinBody, entry.Value)
	require.Equal(t, rePinRev, entry.Revision, "the live value must not be republished or removed")
}

// TestSweepLegacyTombstones_SecondPassIsANoOp pins the level-triggered posture:
// once the delete markers are converted, a further pass lists nothing, publishes
// nothing, and says nothing above Debug.
func TestSweepLegacyTombstones_SecondPassIsANoOp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))

	const instanceID, token = "instTwice1", "tokTwice1"
	seedTerminalInstance(ctx, t, s, instanceID, token)
	legacyDelete(ctx, t, s, patternPinKey(instanceID), tokenKey(token),
		outboxKey(token), deadlineKey(instanceID))

	first := e.convertLegacyTombstones(ctx)
	require.NoError(t, first.err)
	require.Equal(t, 4, first.totalListed())

	before := streamLastSeq(ctx, t, s)
	logs.Reset()
	second := e.sweepLegacyTombstones(ctx)
	require.NoError(t, second.err)
	require.Zero(t, second.totalListed(), "the converted markers are purges; the pass lists deletes only")
	for _, f := range second.families {
		require.Zero(t, f.converted, "%s", f.filter)
		require.Zero(t, f.skippedMismatch, "%s", f.filter)
		require.Zero(t, f.skippedRunning, "%s", f.filter)
	}
	require.Equal(t, before, streamLastSeq(ctx, t, s), "a no-op pass must publish nothing")
	require.Equal(t, "DEBUG", lastSweepRecord(t, &logs)["level"], "an empty pass stays quiet")
}

// TestLegacyTombstoneFamilies_TableShape pins the family table itself: which
// filters the pass visits, in what order, which are paced — the two whose
// subjects a DeliverAll durable consumes — and which carries a per-key guard.
// Exactly deadline.> does, because it is the one family whose conversions wake
// a handler that can act destructively on a live instance. The cursor family
// appears nowhere.
func TestLegacyTombstoneFamilies_TableShape(t *testing.T) {
	t.Parallel()

	// The table's shape does not depend on any connection: the guard is a
	// closure over the engine, never called here.
	e := NewEngine(nil, Config{LoomStateBucket: "loom-state", ActorKey: "vtx.identity.LoomSweepActor123456"})

	want := []struct {
		filter   string
		paced    bool
		hasGuard bool
	}{
		{"instance.*.pattern", false, false},
		{"token.>", false, false},
		{"outbox.>", true, false},
		{"deadline.>", true, true},
	}
	got := e.legacyTombstoneFamilies()
	require.Len(t, got, len(want))
	for i, w := range want {
		require.Equal(t, w.filter, got[i].filter, "family %d", i)
		require.Equal(t, w.paced, got[i].paced,
			"%s: the families no durable filters run first and unpaced", w.filter)
		require.Equal(t, w.hasGuard, got[i].guard != nil,
			"%s: only deadline.> may carry a per-key guard", w.filter)
	}

	for _, fam := range got {
		require.False(t, strings.HasPrefix(fam.filter, instancePrefix) && !strings.Contains(fam.filter, patternPinSuffix),
			"%s could address an instance cursor", fam.filter)
	}
}

// TestConvertFamily_StopsAtAFailedPublish pins the third outcome: a publish
// error that is neither a revision conflict nor a cancellation ends the family
// where it stands, recording the key, and leaves the rest of the listing for
// the next start.
func TestConvertFamily_StopsAtAFailedPublish(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID, token = "instStop1", "tokStop1"
	seedTerminalInstance(ctx, t, s, instanceID, token)
	legacyDelete(ctx, t, s, tokenKey(token))

	fam := e.legacyTombstoneFamilies()[1]
	marker := requireListedTombstone(ctx, t, s, fam.filter, tokenKey(token))

	// A genuine publish failure, not a cancellation: an engine aimed at a
	// bucket that was never provisioned. Its conversion cannot land, and the
	// error is not a revision conflict.
	broken := newSweepEngine(newStateStore(s.conn, "loom-state-never-provisioned"), sweepLogger(&bytes.Buffer{}))
	res := broken.convertFamily(ctx, fam, []substrate.KVTombstone{marker})
	require.Error(t, res.err)
	require.Nil(t, res.cancelled, "a publish failure is not a cancellation")
	require.Equal(t, marker.Key, res.stoppedAtKey)
	require.Zero(t, res.converted)
	require.Zero(t, res.skippedMismatch, "a publish failure is not a mismatch")

	hdr, _ := markerOn(ctx, t, s, tokenKey(token))
	require.Equal(t, "DEL", hdr.Get("KV-Operation"), "the unconverted marker stays as it was")
}

// TestConvertFamily_ClassifiesCancellationApartFromFailure pins the other end
// of that split. The engine's context ending is not a failure: no key was
// attempted, so none is named, and the markers left are simply the next
// start's work. The check runs on an UNPACED family, so it is the per-marker
// check being pinned and not the pacing select.
func TestConvertFamily_ClassifiesCancellationApartFromFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID, token = "instCancel1", "tokCancel1"
	seedTerminalInstance(ctx, t, s, instanceID, token)
	legacyDelete(ctx, t, s, tokenKey(token))

	fam := e.legacyTombstoneFamilies()[1]
	require.False(t, fam.paced, "this test is about the unpaced per-marker check")
	marker := requireListedTombstone(ctx, t, s, fam.filter, tokenKey(token))

	before := streamLastSeq(ctx, t, s)
	dead, cancelDead := context.WithCancel(ctx)
	cancelDead()
	res := e.convertFamily(dead, fam, []substrate.KVTombstone{marker})
	require.ErrorIs(t, res.cancelled, context.Canceled)
	require.NoError(t, res.err, "a cancellation is not a failure")
	require.Empty(t, res.stoppedAtKey, "no key was attempted, so none may be named")
	require.Zero(t, res.converted)
	require.Zero(t, res.skippedMismatch)
	require.Equal(t, before, streamLastSeq(ctx, t, s), "a cancelled family must publish nothing")

	hdr, _ := markerOn(ctx, t, s, tokenKey(token))
	require.Equal(t, "DEL", hdr.Get("KV-Operation"), "the unconverted marker stays as it was")
}

// TestSweepLegacyTombstones_StopsAtTheFirstFailureAndTheNextPassFinishes pins
// the pass-level half of the same rule — a failure ends the pass, so the
// families after it are not visited at all — and that convergence is by the
// next pass, which finds them still listed.
func TestSweepLegacyTombstones_StopsAtTheFirstFailureAndTheNextPassFinishes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	var logs bytes.Buffer
	e := newSweepEngine(s, sweepLogger(&logs))

	const instanceID, token = "instResume1", "tokResume1"
	seedTerminalInstance(ctx, t, s, instanceID, token)
	legacyDelete(ctx, t, s, patternPinKey(instanceID), tokenKey(token),
		outboxKey(token), deadlineKey(instanceID))

	dead, cancelDead := context.WithCancel(ctx)
	cancelDead()
	stopped := e.sweepLegacyTombstones(dead)
	require.Error(t, stopped.err)
	require.Equal(t, e.legacyTombstoneFamilies()[0].filter, stopped.stoppedAtFamily)
	require.Len(t, stopped.families, 1, "the families after the failure are never visited")

	rec := lastSweepRecord(t, &logs)
	require.Equal(t, "INFO", rec["level"])
	require.Contains(t, rec["stoppedAt"], e.legacyTombstoneFamilies()[0].filter)

	// The next pass, on a live context, converts everything the stopped one left.
	resumed := e.convertLegacyTombstones(ctx)
	require.NoError(t, resumed.err)
	require.Equal(t, 4, resumed.totalListed())
	for _, key := range []string{
		patternPinKey(instanceID), tokenKey(token), outboxKey(token), deadlineKey(instanceID),
	} {
		requireExpiringPurgeMarker(ctx, t, s, key)
	}
}

// TestEngineStart_RunsTheConversionPass pins the launch against the three
// bucket populations a real start meets at once: a terminated instance's
// legacy residue in all four families, which converts; a live running
// instance whose keys are all values, which the pass never addresses; and a
// running instance whose deadline was disarmed long ago and whose disarm left
// a legacy DEL marker, which the pass must leave exactly as it is — converting
// it would re-fire the deadline probe on a live human wait.
func TestEngineStart_RunsTheConversionPass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newSweepEngineBuckets(ctx, t)

	const doneID, doneToken = "instStart1", "tokStart1"
	seedTerminalInstance(ctx, t, s, doneID, doneToken)
	residue := []string{
		patternPinKey(doneID), tokenKey(doneToken), outboxKey(doneToken), deadlineKey(doneID),
	}
	legacyDelete(ctx, t, s, residue...)

	const liveID, liveToken = "instStartLive1", "tokStartLive1"
	seedRunningInstance(ctx, t, s, liveID, liveToken)

	const waitID, waitToken = "instStartWait1", "tokStartWait1"
	seedRunningInstance(ctx, t, s, waitID, waitToken)
	require.NoError(t, s.disarmDeadline(ctx, waitID))
	legacyDelete(ctx, t, s, deadlineKey(waitID))
	_, disarmedSeq := markerOn(ctx, t, s, deadlineKey(waitID))

	e := NewEngine(s.conn, Config{
		LoomStateBucket: s.bucket,
		ActorKey:        "vtx.identity.LoomSweepActor123456",
		Logger:          sweepLogger(&bytes.Buffer{}),
	})
	engCtx, engCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- e.Start(engCtx) }()

	require.Eventually(t, func() bool {
		for _, key := range residue {
			markers, err := s.conn.KVListTombstones(ctx, s.bucket, key)
			if err != nil || len(markers) > 0 {
				return false
			}
		}
		return true
	}, 30*time.Second, 100*time.Millisecond, "Start must run the conversion pass")

	for _, key := range residue {
		requireExpiringPurgeMarker(ctx, t, s, key)
	}

	// The disarmed instance's marker is untouched, at the same revision.
	hdr, seq := markerOn(ctx, t, s, deadlineKey(waitID))
	require.Equal(t, "DEL", hdr.Get("KV-Operation"),
		"a running instance's deadline marker must not be converted")
	require.Equal(t, disarmedSeq, seq, "nothing may be republished on that subject")

	// And both running instances are still running, still holding their step.
	for _, id := range []struct{ instanceID, token string }{{liveID, liveToken}, {waitID, waitToken}} {
		inst, err := s.getInstance(ctx, id.instanceID)
		require.NoError(t, err)
		require.NotNil(t, inst, "%s", id.instanceID)
		require.Equal(t, StatusRunning, inst.Status, "%s must still be running", id.instanceID)
		require.Equal(t, id.token, inst.PendingToken, "%s must still hold its pending token", id.instanceID)
	}
	for _, key := range []string{
		patternPinKey(liveID), tokenKey(liveToken), outboxKey(liveToken), deadlineKey(liveID),
		patternPinKey(waitID), tokenKey(waitToken), outboxKey(waitToken),
	} {
		_, err := s.conn.KVGet(ctx, s.bucket, key)
		require.NoError(t, err, "%s must still read as a value", key)
	}

	engCancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("engine did not stop")
	}
}

// recordingHandler is a slog.Handler that closes started when the engine
// announces itself — the point past which Start has launched the conversion
// pass — and records whether anything was logged after sealed was set, which
// is the probe for "Start returned while the pass was still running".
type recordingHandler struct {
	inner   slog.Handler
	started chan struct{}
	once    sync.Once
	sealed  atomic.Bool
	after   atomic.Bool
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.sealed.Load() {
		h.after.Store(true)
	}
	if r.Message == "loom engine started" {
		h.once.Do(func() { close(h.started) })
	}
	return h.inner.Handle(ctx, r)
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// TestEngineStart_JoinsTheConversionPassBeforeReturning pins that Start does
// not outlive the goroutine it launched. A cancellation arriving while the
// pass is mid-listing must leave nothing behind that logs, publishes or reads
// after Start's caller believes the engine is down — so the summary line, the
// last thing the pass does, must land before Start returns.
func TestEngineStart_JoinsTheConversionPassBeforeReturning(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s := newSweepEngineBuckets(ctx, t)

	// A few hundred markers, so the pass has real work in flight when the
	// cancellation lands rather than finishing before Start reaches its wait.
	const markers = 400
	for i := 0; i < markers; i++ {
		key := tokenPrefix + fmt.Sprintf("tokJoin%04d", i)
		_, err := s.conn.KVPut(ctx, s.bucket, key, []byte(`{"instanceId":"instJoin1"}`))
		require.NoError(t, err)
		require.NoError(t, s.conn.KVDelete(ctx, s.bucket, key))
	}

	rec := &recordingHandler{
		inner:   slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}),
		started: make(chan struct{}),
	}
	e := NewEngine(s.conn, Config{
		LoomStateBucket: s.bucket,
		ActorKey:        "vtx.identity.LoomSweepActor123456",
		Logger:          slog.New(rec),
	})

	engCtx, engCancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- e.Start(engCtx) }()

	// Cancel once the engine has announced itself, which Start does directly
	// after launching the pass — so the cancellation lands on a pass that is
	// running, not on the consumer setup ahead of it.
	select {
	case <-rec.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the engine never started")
	}
	engCancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
	rec.sealed.Store(true)

	// Nothing the engine owns may log from here on. The pass logs its summary
	// unconditionally, so a goroutine still running would be seen.
	require.Never(t, func() bool { return rec.after.Load() }, 2*time.Second, 100*time.Millisecond,
		"the conversion pass logged after Start returned, so Start did not join it")
}

// --- the deadline family's guard --------------------------------------------

// TestConvertFamily_SkipsARunningInstancesDeadlineMarker is the guard's central
// proof, on the shape that produces it in production: a userTask instance whose
// bounded creation deadline was DISARMED — through the real disarm path — while
// the instance stays running, parked on an unbounded human wait. Its deadline
// subject carries a marker; converting it would deliver a fresh empty-body
// message to the deadline durable, whose probe on a running instance looks for
// a Contract #4 tracker that expired 24h after the op committed, finds nothing,
// and fails the instance. The pass must leave the marker exactly as it is.
func TestConvertFamily_SkipsARunningInstancesDeadlineMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID, token = "instWaiting1", "tokWaiting1"
	seedRunningInstance(ctx, t, s, instanceID, token)

	// The real disarm, then the legacy DEL shape on top of the marker it left:
	// this package's own removals purge with a TTL, so the residue the pass
	// exists to clear cannot be produced by production code any more.
	require.NoError(t, s.disarmDeadline(ctx, instanceID))
	legacyDelete(ctx, t, s, deadlineKey(instanceID))
	_, before := markerOn(ctx, t, s, deadlineKey(instanceID))

	fam := e.legacyTombstoneFamilies()[3]
	require.Equal(t, deadlinePrefix+">", fam.filter)
	marker := requireListedTombstone(ctx, t, s, fam.filter, deadlineKey(instanceID))

	res := e.convertFamily(ctx, fam, []substrate.KVTombstone{marker})
	require.NoError(t, res.err)
	require.Nil(t, res.cancelled)
	require.Equal(t, 1, res.listed)
	require.Zero(t, res.converted, "a running instance's deadline marker must not be converted")
	require.Zero(t, res.skippedMismatch, "the guard's refusal is not a revision mismatch")
	require.Equal(t, 1, res.skippedRunning)

	hdr, after := markerOn(ctx, t, s, deadlineKey(instanceID))
	require.Equal(t, "DEL", hdr.Get("KV-Operation"), "the marker must be exactly what it was")
	require.Equal(t, before, after, "nothing may be published on that subject")

	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusRunning, inst.Status, "the instance must still be running")
	require.Equal(t, token, inst.PendingToken, "its pending token must be untouched")
}

// TestConvertFamily_ConvertsATerminalInstancesDeadlineMarker is the guard's
// positive vector, without which the test above would pass on a guard that
// refused everything: a terminal instance's marker is the ordinary case and
// converts, because its probe returns at the status check.
func TestConvertFamily_ConvertsATerminalInstancesDeadlineMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID, token = "instDone1", "tokDone1"
	seedTerminalInstance(ctx, t, s, instanceID, token)
	legacyDelete(ctx, t, s, deadlineKey(instanceID))

	fam := e.legacyTombstoneFamilies()[3]
	marker := requireListedTombstone(ctx, t, s, fam.filter, deadlineKey(instanceID))

	res := e.convertFamily(ctx, fam, []substrate.KVTombstone{marker})
	require.NoError(t, res.err)
	require.Equal(t, 1, res.converted)
	require.Zero(t, res.skippedRunning)
	requireExpiringPurgeMarker(ctx, t, s, deadlineKey(instanceID))
}

// TestConvertFamily_SkipsADeadlineMarkerWithNoInstanceRecord pins the third
// verdict the guard can reach: a marker whose instance record is gone cannot
// be classified, and the pass never converts what it cannot classify.
func TestConvertFamily_SkipsADeadlineMarkerWithNoInstanceRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const orphanID = "instOrphan1"
	_, err := s.conn.KVPut(ctx, s.bucket, deadlineKey(orphanID), []byte(`{"setAt":"x"}`))
	require.NoError(t, err)
	legacyDelete(ctx, t, s, deadlineKey(orphanID))
	_, before := markerOn(ctx, t, s, deadlineKey(orphanID))

	fam := e.legacyTombstoneFamilies()[3]
	marker := requireListedTombstone(ctx, t, s, fam.filter, deadlineKey(orphanID))

	res := e.convertFamily(ctx, fam, []substrate.KVTombstone{marker})
	require.NoError(t, res.err)
	require.Zero(t, res.converted)
	require.Equal(t, 1, res.skippedRunning)
	_, after := markerOn(ctx, t, s, deadlineKey(orphanID))
	require.Equal(t, before, after)
}

// TestHandleDeadline_TerminalInstanceIsASilentNoOp pins the other half of the
// guard's argument, at the handler: the cost of converting a TERMINAL
// instance's marker really is one read. The delivery the conversion produces
// reaches handleDeadline as an empty body, the probe returns at the status
// check, and the handler acks having published nothing at all.
func TestHandleDeadline_TerminalInstanceIsASilentNoOp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	e := newSweepEngine(s, sweepLogger(&bytes.Buffer{}))

	const instanceID, token = "instAcked1", "tokAcked1"
	seedTerminalInstance(ctx, t, s, instanceID, token)

	subjPrefix := "$KV." + s.bucket + "."
	before := streamLastSeq(ctx, t, s)
	decision := e.handleDeadline(ctx, subjPrefix, substrate.Message{
		Subject: subjPrefix + deadlineKey(instanceID),
		Body:    nil,
	})
	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, before, streamLastSeq(ctx, t, s),
		"a terminal instance's deadline probe must publish nothing")

	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	require.Equal(t, StatusComplete, inst.Status)
}

// newSweepEngineBuckets provisions everything Engine.Start attaches to: the
// pattern source's core-kv, the trigger consumer's core-events, the
// heartbeater's health-kv, and loom-state itself with the atomic-publish and
// marker-TTL posture bootstrap gives the real bucket.
func newSweepEngineBuckets(ctx context.Context, t *testing.T) *stateStore {
	t.Helper()
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	js := conn.JetStream()
	for _, bucket := range []string{"core-kv", "loom-state", "health-kv"} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: time.Second})
		require.NoError(t, err)
	}
	stream, err := js.Stream(ctx, "KV_loom-state")
	require.NoError(t, err)
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, cfg)
	require.NoError(t, err)

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-events", Subjects: []string{"events.>"},
		Retention: jetstream.LimitsPolicy, MaxAge: time.Hour,
	})
	require.NoError(t, err)

	return newStateStore(conn, "loom-state")
}
