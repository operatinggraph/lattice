package loom

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// counterTestPattern is a minimal one-step pattern, enough to satisfy the
// pattern pin createInstance/transition co-write the counter tests exercise
// (mirrors retentionPattern in retention_internal_test.go).
func counterTestPattern() *Pattern {
	return &Pattern{
		PatternID:   "counter-pattern",
		SubjectType: "widget",
		Steps:       []Step{{Kind: StepKindSystemOp, Operation: "StepA"}},
	}
}

// newCounterTestInstance builds a running Instance with a fresh valid NanoID
// (Contract #1 alphabet), for tests that seed several independently-identified
// instances at once.
func newCounterTestInstance(t *testing.T) *Instance {
	t.Helper()
	id, err := keys.NewNanoID()
	require.NoError(t, err)
	return &Instance{
		InstanceID: id,
		PatternRef: "vtx.meta.CounterPattern00001",
		SubjectKey: "vtx.widget." + id,
		Status:     StatusRunning,
	}
}

// The core assertion: the pin-index count tracks the real running-instance
// set across a lifecycle driven through the REAL transition (not a hand-built
// fixture), including instances that leave the running set.
func TestRunningInstanceCounter_LifecycleAcrossRealTransitions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	r := &runningInstanceCounter{conn: conn, bucket: "loom-state", interval: defaultHeartbeatEvery}

	const n = 5
	insts := make([]*Instance, n)
	for i := 0; i < n; i++ {
		inst := newCounterTestInstance(t)
		require.NoError(t, s.createInstance(ctx, inst, counterTestPattern()))
		insts[i] = inst
	}

	got, err := r.count(ctx)
	require.NoError(t, err)
	require.Equal(t, n, got, "every created instance is running and pinned")

	// Drive two instances to terminal through the real transition — the pin
	// delete this depends on rides the same AtomicBatch a production terminal
	// takes.
	insts[0].Status = StatusComplete
	require.NoError(t, s.transition(ctx, insts[0], "", "", nil, 0))
	insts[1].Status = StatusFailed
	require.NoError(t, s.transition(ctx, insts[1], "", "", nil, 0))

	got, err = r.count(ctx)
	require.NoError(t, err)
	require.Equal(t, n-2, got, "terminal instances drop out of the count")
}

// A terminal instance's cursor record stays readable (retained for the
// retention window, per Inc 1) but must not be counted as running — precisely
// what the old body-scan got right by decoding Status, and what the pin-index
// count must also get right despite never looking at the body.
func TestRunningInstanceCounter_TerminalRecordRetainedButNotCounted(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	r := &runningInstanceCounter{conn: conn, bucket: "loom-state", interval: defaultHeartbeatEvery}

	inst := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, inst, counterTestPattern()))

	inst.Status = StatusComplete
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))

	got, err := r.count(ctx)
	require.NoError(t, err)
	require.Zero(t, got, "a terminal instance must not be counted as running")

	stored, err := s.getInstance(ctx, inst.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, stored, "the cursor record is retained for the retention window, not deleted at terminal")
	require.Equal(t, StatusComplete, stored.Status)
}

// A poisoned/unparseable instance BODY must not affect the count: the
// pin-index path never decodes a body at all, so it is immune by
// construction rather than by a skip-on-error branch.
func TestRunningInstanceCounter_PoisonedBodyDoesNotAffectCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	r := &runningInstanceCounter{conn: conn, bucket: "loom-state", interval: defaultHeartbeatEvery}

	good := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, good, counterTestPattern()))

	poisoned := newCounterTestInstance(t)
	require.NoError(t, s.createInstance(ctx, poisoned, counterTestPattern()))
	// Corrupt the instance BODY only — its pin (the thing count() actually
	// reads) is untouched, exactly as production leaves a poisoned cursor.
	_, err := conn.KVPut(ctx, "loom-state", instanceKey(poisoned.InstanceID), []byte("{not json"))
	require.NoError(t, err)

	got, err := r.count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, got, "a poisoned instance body must not affect the pin-based count")
}

// fakeInstanceReader implements runningInstanceReader with nothing but
// KVListKeysPrefix. It has no KVGetMulti method at all, so if count() were
// ever changed to fetch a record body, this file would fail to COMPILE
// rather than merely fail an assertion — the strongest form of "no body
// fetch" proof available for this call.
type fakeInstanceReader struct {
	keys []string
}

func (f fakeInstanceReader) KVListKeysPrefix(_ context.Context, _, _ string) ([]string, error) {
	return f.keys, nil
}

// Proves count() derives its result from nothing but the pin-suffixed keys a
// KVListKeysPrefix call returns — no other method is available to it.
func TestRunningInstanceCounter_NoBodyFetchStructural(t *testing.T) {
	t.Parallel()
	sampleKeys := []string{
		"instance.aaaaaaaaaaaaaaaaaaaa.pattern",
		"instance.aaaaaaaaaaaaaaaaaaaa",
		"instance.bbbbbbbbbbbbbbbbbbbb.pattern",
		"instance.bbbbbbbbbbbbbbbbbbbb",
		"instance.cccccccccccccccccccc", // terminal: pin already deleted
	}
	r := &runningInstanceCounter{conn: fakeInstanceReader{keys: sampleKeys}, bucket: "loom-state", interval: defaultHeartbeatEvery}

	got, err := r.count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, got, "only the two pin keys count as running")
}

// blockingInstanceReader blocks on KVListKeysPrefix until its ctx is done, so
// a test can observe exactly which deadline governs the call.
type blockingInstanceReader struct{}

func (blockingInstanceReader) KVListKeysPrefix(ctx context.Context, _, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// count() must bound its KV call to countDeadline(interval), not inherit the
// caller's context unbounded — proven by handing it an outer context with a
// generous 3s safety-net deadline (so a regression fails fast instead of
// hanging the suite) and confirming the call actually returns on the much
// shorter derived deadline.
func TestRunningInstanceCounter_DeadlineApplied(t *testing.T) {
	t.Parallel()
	outerCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	r := &runningInstanceCounter{conn: blockingInstanceReader{}, bucket: "loom-state", interval: 20 * time.Millisecond}

	start := time.Now()
	_, err := r.count(outerCtx)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, time.Second,
		"count must return on its own derived deadline (~10ms here), not the outer 3s context")
}

// Pins the countDeadline derivation: half the interval, capped, and a
// non-positive interval falls back to the cap rather than a zero deadline.
func TestCountDeadline_Derivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{"half of a modest interval", 10 * time.Second, 5 * time.Second},
		{"large interval clamps to the cap", time.Hour, 5 * time.Second},
		{"small interval halves without clamping", 40 * time.Millisecond, 20 * time.Millisecond},
		{"zero interval falls back to the cap", 0, 5 * time.Second},
		{"negative interval falls back to the cap", -time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, countDeadline(tc.interval))
		})
	}
}
