package loom_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/loom"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// wipeLoomState simulates the §10.6 "disaster recovery" precondition: the entire
// loom-state durable plane is LOST (no instance cursor, no token pointer, no
// outbox/deadline marker survives). It lists and deletes every key in the
// loom-state bucket — "lost entirely" with nothing readable surviving — leaving
// the bucket itself in place so a fresh engine generation provisions against it
// unchanged. (Q3 ruling: no bucket-wipe primitive exists; a list+delete test
// helper is in-scope as test support.)
func wipeLoomState(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, loomStateBucket)
	require.NoError(t, err)
	for _, k := range keys {
		require.NoError(t, conn.KVDelete(ctx, loomStateBucket, k))
	}
}

// seedAspect writes a Core KV aspect envelope <subjectKey>.<aspect> with the
// given data map, the shape the Processor write path mints (mirrors seedOpMeta's
// conn.KVPut envelope shape). data == nil writes a body without a data envelope.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, subjectKey, aspect string, data map[string]any) {
	t.Helper()
	body := map[string]any{"class": "aspect"}
	if data != nil {
		body["data"] = data
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey+"."+aspect, b)
	require.NoError(t, err)
}

// guardedOnboardingPattern is the §10.5 worked-example fixture
// (docs/contracts/10-orchestration-surfaces.md:350-361): SetName/SetPhone guarded
// on the profile aspect's name/phone fields, with a guardless final SetAddress
// (which exercises §10.6 invariant 2 — a guardless step has no guard-replay
// signal and is reached, never skipped).
func guardedOnboardingPattern(patternID string) loom.Pattern {
	return loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "identity",
		CompletionDomains: []string{"orchestration"},
		Steps: []loom.Step{
			{Kind: "userTask", Operation: "SetName", Guard: json.RawMessage(`{"absent":"subject.profile.data.name"}`)},
			{Kind: "userTask", Operation: "SetPhone", Guard: json.RawMessage(`{"absent":"subject.profile.data.phone"}`)},
			{Kind: "userTask", Operation: "SetAddress"},
		},
	}
}

// TestGuardEval_PinnedAbsenceSemantics table-tests evalGuard against a real
// Core KV (the package norm is NATS-backed; short-skips). It pins the §10.5
// absence semantics: null/missing/soft-deleted/empty-after-trim are ABSENT;
// "0"/false/0 are PRESENT — never "falsy"; equals is type-aware and an absent
// path never equals anything.
func TestGuardEval_PinnedAbsenceSemantics(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	subjectKey := "vtx.identity." + mustNanoID(t)

	// Root vertex: data fields covering present/absent variants.
	rootBody, _ := json.Marshal(map[string]any{
		"class": "identity",
		"data": map[string]any{
			"name":      "Ada",
			"nullField": nil,
			"emptyStr":  "",
			"blankStr":  "   ",
			"zeroStr":   "0",
			"falseBool": false,
			"zeroNum":   0,
			"status":    "active",
			"count":     3,
		},
	})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	// A live profile aspect with name present, phone empty-after-trim.
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada", "phone": "  "})
	// A soft-deleted aspect: every field under it is absent.
	tombBody, _ := json.Marshal(map[string]any{"class": "aspect", "isDeleted": true, "data": map[string]any{"name": "Ghost"}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey+".tomb", tombBody)
	require.NoError(t, err)

	eval := func(t *testing.T, raw string) bool {
		t.Helper()
		g, perr := loom.ParseGuardForTest(raw)
		require.NoError(t, perr)
		got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
		require.NoError(t, eerr)
		return got
	}

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		// absent atom — true when path absent
		{"present field not absent", `{"absent":"subject.data.name"}`, false},
		{"missing field absent", `{"absent":"subject.data.missing"}`, true},
		{"null field absent", `{"absent":"subject.data.nullField"}`, true},
		{"empty string absent", `{"absent":"subject.data.emptyStr"}`, true},
		{"blank (trim) string absent", `{"absent":"subject.data.blankStr"}`, true},
		{`"0" string present`, `{"absent":"subject.data.zeroStr"}`, false},
		{"false bool present", `{"absent":"subject.data.falseBool"}`, false},
		{"zero number present", `{"absent":"subject.data.zeroNum"}`, false},
		{"missing root vertex field absent", `{"absent":"subject.data.nope"}`, true},
		// aspect paths
		{"aspect name present", `{"present":"subject.profile.data.name"}`, true},
		{"aspect phone empty absent", `{"absent":"subject.profile.data.phone"}`, true},
		{"missing aspect absent", `{"absent":"subject.nosuch.data.x"}`, true},
		{"soft-deleted aspect field absent", `{"absent":"subject.tomb.data.name"}`, true},
		{"soft-deleted aspect not present", `{"present":"subject.tomb.data.name"}`, false},
		// present atom
		{"present on present field", `{"present":"subject.data.name"}`, true},
		{"present on absent field", `{"present":"subject.data.missing"}`, false},
		// equals
		{"equals string match", `{"equals":{"path":"subject.data.status","value":"active"}}`, true},
		{"equals string mismatch", `{"equals":{"path":"subject.data.status","value":"closed"}}`, false},
		{"equals number match", `{"equals":{"path":"subject.data.count","value":3}}`, true},
		{"equals number mismatch", `{"equals":{"path":"subject.data.count","value":4}}`, false},
		{"equals zero number", `{"equals":{"path":"subject.data.zeroNum","value":0}}`, true},
		{`equals "0" string`, `{"equals":{"path":"subject.data.zeroStr","value":"0"}}`, true},
		{"equals false bool", `{"equals":{"path":"subject.data.falseBool","value":false}}`, true},
		{"equals absent path never equals null", `{"equals":{"path":"subject.data.missing","value":null}}`, false},
		{"equals absent path never equals empty", `{"equals":{"path":"subject.data.missing","value":""}}`, false},
		{"equals empty-trim absent never equals", `{"equals":{"path":"subject.data.blankStr","value":"   "}}`, false},
		// composition
		{"allOf both true", `{"allOf":[{"present":"subject.data.name"},{"absent":"subject.data.missing"}]}`, true},
		{"allOf one false", `{"allOf":[{"present":"subject.data.name"},{"present":"subject.data.missing"}]}`, false},
		{"anyOf one true", `{"anyOf":[{"present":"subject.data.missing"},{"present":"subject.data.name"}]}`, true},
		{"anyOf all false", `{"anyOf":[{"present":"subject.data.missing"},{"absent":"subject.data.name"}]}`, false},
		{"not inverts", `{"not":{"present":"subject.data.name"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, eval(t, tc.raw))
		})
	}
}

// TestGuardE2E_FalseGuardSkipsStep proves AC#2 end-to-end: a false guard skips
// its step (no CreateTask, no token), and a run of consecutive false guards
// skips multiple steps in one transition. An all-false pattern completes on the
// trigger with NO task at all.
func TestGuardE2E_FalseGuardSkipsStep(t *testing.T) {
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

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	fp.run(ctx, t)
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, guardedOnboardingPattern(patternID))

	subjectKey := "vtx.identity." + mustNanoID(t)
	// Seed name AND phone present → step 0 and step 1 guards both false; the run
	// must skip both in a single transition and land on the guardless SetAddress.
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada", "phone": "555-1234"})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// Lands directly on cursor 2 (SetAddress) — steps 0 and 1 skipped.
	taskKey := waitTaskKey(t, ctx, conn, instanceID, 2)
	waitTaskCreated(t, fp, taskKey, "SetAddress CreateTask must be the only task")
	require.Equal(t, 1, fp.createTaskCount(), "two false guards skip both steps with no CreateTask")

	// Complete the guardless final step → patternCompleted.
	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)
	submitBoundOp(t, ctx, conn, "SetAddress", taskKey, subjectKey)
	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "pattern completes after the single un-skipped step")

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 3, inst.Cursor)
	require.Equal(t, 1, fp.createTaskCount(), "exactly one CreateTask (SetAddress) across the whole run")
}

// TestGuardE2E_AllGuardsFalseCompletesOnTrigger proves the AC#2 tail: when every
// guarded step is skipped and there is no guardless tail, the pattern completes
// immediately on the trigger with NO task or op submitted.
func TestGuardE2E_AllGuardsFalseCompletesOnTrigger(t *testing.T) {
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

	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)

	fp := newOnboardingProcessor(conn, map[string]struct{}{"SetName": {}, "SetPhone": {}})
	fp.run(ctx, t)
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")

	// A two-step pattern, BOTH guarded, no guardless tail.
	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "identity",
		CompletionDomains: []string{"orchestration"},
		Steps: []loom.Step{
			{Kind: "userTask", Operation: "SetName", Guard: json.RawMessage(`{"absent":"subject.profile.data.name"}`)},
			{Kind: "userTask", Operation: "SetPhone", Guard: json.RawMessage(`{"absent":"subject.profile.data.phone"}`)},
		},
	})

	subjectKey := "vtx.identity." + mustNanoID(t)
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada", "phone": "555-1234"})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "all-guards-false pattern completes on trigger")

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 2, inst.Cursor, "completed-on-trigger persists cursor == len(steps)")
	require.Equal(t, 0, fp.createTaskCount(), "no CreateTask for an all-skipped pattern")
}

// TestGuardE2E_DisasterRecoveryCursorRebuild proves AC#6 (the §10.5-fixture
// disaster-recovery test, honoring §10.6 invariant 2). It runs a guarded
// onboarding flow to mid-pattern, WIPES loom-state entirely, re-triggers, and
// asserts identical EFFECTIVE resumption — the fresh instance guard-replays
// against the (still-populated) subject, lands on the same step a continuing
// instance would, never re-runs the already-skipped step 0, and honors the
// guardless final step via the token rule.
func TestGuardE2E_DisasterRecoveryCursorRebuild(t *testing.T) {
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

	boundOps := map[string]struct{}{"SetName": {}, "SetPhone": {}, "SetAddress": {}}
	fp := newOnboardingProcessor(conn, boundOps)
	fp.run(ctx, t)
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetPhone")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetAddress")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, guardedOnboardingPattern(patternID))

	subjectKey := "vtx.identity." + mustNanoID(t)
	// name already present → step 0's guard is false from the start.
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada"})

	// --- Generation 1: start; step 0 (name present) skipped; step 1 (phone
	// absent) runs and parks on its userTask token. ---
	e1Ctx, e1Cancel := context.WithCancel(ctx)
	_, e1Err := startEngine(t, e1Ctx, conn)
	waitForReady(t, 5*time.Second, e1Err, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "gen-1 trigger consumer never registered")

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	taskKey1 := waitTaskKey(t, ctx, conn, instanceID, 1)
	waitTaskCreated(t, fp, taskKey1, "step 1 (SetPhone) CreateTask minted")
	require.Equal(t, 1, fp.createTaskCount(), "only SetPhone created — SetName was guard-skipped (no task)")

	// --- Disaster: lose loom-state entirely (cursor, token, outbox all gone). ---
	e1Cancel()
	joinEngine(t, e1Err)
	wipeLoomState(t, ctx, conn)

	// --- Generation 2: fresh engine over the same conn/buckets; re-submit the
	// SAME StartLoomPattern. A fresh instanceId is expected (loom-state was the
	// lost cursor's key); the test asserts EFFECTIVE resumption position, not
	// literal instanceId continuity (Winston Q1 ruling). ---
	e2Ctx, e2Cancel := context.WithCancel(ctx)
	defer e2Cancel()
	_, e2Err := startEngine(t, e2Ctx, conn)
	waitForReady(t, 5*time.Second, e2Err, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "gen-2 trigger consumer never registered")

	instanceID2 := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	require.NotEqual(t, instanceID, instanceID2, "lost loom-state ⇒ a new instance id")

	// The re-driven instance guard-replays: name still present → step 0 skipped
	// again (NO second SetName task); phone still absent → lands on step 1.
	taskKey1b := waitTaskKey(t, ctx, conn, instanceID2, 1)
	waitTaskCreated(t, fp, taskKey1b, "re-driven instance lands on SetPhone (step 1), not SetName (step 0)")

	// CreateTask accounting: gen-1 SetPhone (1) + gen-2's own SetPhone (1) = 2.
	// Crucially NO SetName task was ever created in either generation — step 0's
	// false guard skipped it both times (no double-submit of the skipped step).
	require.Equal(t, 2, fp.createTaskCount(),
		"only the two SetPhone tasks (gen1 + gen2's own first run); SetName never created")

	// --- Drive gen-2 to completion through the guardless final step (§10.6
	// invariant 2 — SetAddress completes solely via its token). ---
	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)
	submitBoundOp(t, ctx, conn, "SetPhone", taskKey1b, subjectKey)
	taskKey2 := waitTaskKey(t, ctx, conn, instanceID2, 2)
	submitBoundOp(t, ctx, conn, "SetAddress", taskKey2, subjectKey)
	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "guardless final step (SetAddress) honored via token rule → patternCompleted")

	inst := waitInstanceStatus(t, ctx, conn, instanceID2, "complete")
	require.Equal(t, 3, inst.Cursor)
}

// TestGuardE2E_DisasterRecoveryGuardlessStepRerun pins the §10.6 "documented
// bound" (Contract #10 ~line 242) for a guardless step's recovery window: after
// total loom-state loss, a guardless step that already committed in gen-1
// re-runs under gen-2's fresh instanceId (requestIds derive from instanceID, so
// Contract #4's dedup tracker cannot see across generations — each generation's
// requestId is its own first attempt).
//
// Pattern: [{userTask SetName, guard: absent name}, {systemOp Sync, guardless}].
// Gen-1: name already present → step 0 skipped (guard false, no CreateTask);
// step 1 (Sync, guardless) runs to commitment and the instance completes. The
// entire loom-state plane is then wiped and gen-2 re-submits the SAME
// StartLoomPattern: guard replay again skips step 0 (name still present, no
// double-submit of the skipped step) and lands on step 1, which re-runs Sync
// under gen-2's own (different) requestId — a SECOND systemOp commit. The
// duplicate is bounded and operator-visible, never looping: exactly 2 Sync
// commits across the two generations, both instances complete, SetName is
// never created in either generation.
func TestGuardE2E_DisasterRecoveryGuardlessStepRerun(t *testing.T) {
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
	seedOpMeta(t, ctx, conn, mustNanoID(t), "SetName")
	seedOpMeta(t, ctx, conn, mustNanoID(t), "Sync")

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "identity",
		CompletionDomains: []string{"identity", "orchestration"},
		Steps: []loom.Step{
			{Kind: "userTask", Operation: "SetName", Guard: json.RawMessage(`{"absent":"subject.profile.data.name"}`)},
			{Kind: "systemOp", Operation: "Sync"},
		},
	})

	subjectKey := "vtx.identity." + mustNanoID(t)
	// name already present → step 0's guard is false from the start, in both
	// generations.
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada"})

	// --- Generation 1: step 0 (name present) skipped; step 1 (Sync, guardless,
	// systemOp) runs to commitment and the pattern completes. ---
	e1Ctx, e1Cancel := context.WithCancel(ctx)
	_, e1Err := startEngine(t, e1Ctx, conn)
	waitForReady(t, 5*time.Second, e1Err, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "gen-1 trigger consumer never registered")

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	inst1 := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 2, inst1.Cursor, "gen-1 completes after the single guardless Sync step")
	require.Equal(t, int64(1), atomic.LoadInt64(&fp.submitted), "gen-1's Sync op commits exactly once")
	require.Equal(t, 0, fp.createTaskCount(), "SetName never created — its false guard skips it")

	// --- Disaster: lose loom-state entirely (cursor, token, outbox, op tracker
	// pointers — everything). ---
	e1Cancel()
	joinEngine(t, e1Err)
	wipeLoomState(t, ctx, conn)

	// --- Generation 2: fresh engine; re-submit the SAME StartLoomPattern. Guard
	// replay again skips step 0 (name still present) and lands on step 1, whose
	// requestId is derived from gen-2's (new) instanceId — a SECOND, distinct
	// Sync commit (Contract #4 cannot dedup across generations, §10.6 documented
	// bound). ---
	e2Ctx, e2Cancel := context.WithCancel(ctx)
	defer e2Cancel()
	_, e2Err := startEngine(t, e2Ctx, conn)
	waitForReady(t, 5*time.Second, e2Err, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "gen-2 trigger consumer never registered")

	instanceID2 := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)
	require.NotEqual(t, instanceID, instanceID2, "lost loom-state ⇒ a new instance id")

	inst2 := waitInstanceStatus(t, ctx, conn, instanceID2, "complete")
	require.Equal(t, 2, inst2.Cursor, "gen-2 completes after re-running the guardless Sync step")

	// Exactly 2 Sync commits across BOTH generations — the bounded, documented
	// duplicate, not an unbounded loop. SetName is STILL never created.
	require.Equal(t, int64(2), atomic.LoadInt64(&fp.submitted),
		"the guardless step re-commits exactly once more in gen-2 — a bounded duplicate, not a loop")
	require.Equal(t, 0, fp.createTaskCount(), "SetName never created in either generation")
}
