package loom_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/loom"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestGuardEval_Starlark_EvalTable exercises evalGuard's {reads, starlark}
// branch (design doc §4 test bullet 3: eval): a range predicate, a
// cross-field predicate, absence parity with the declarative grammar for a
// soft-deleted aspect, and the read-set bound (an aspect NOT in `reads`
// reads as None, never an error — design doc §2.2).
func TestGuardEval_Starlark_EvalTable(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{
		"class": "identity",
		"data":  map[string]any{"age": 21},
	})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	seedAspect(t, ctx, conn, subjectKey, "lease", map[string]any{
		"startDate": "2026-01-01T00:00:00Z",
		"endDate":   "2026-12-31T00:00:00Z",
	})
	// A soft-deleted aspect: subject.tomb must project as None (absence
	// parity with the declarative grammar).
	tombBody, _ := json.Marshal(map[string]any{"class": "aspect", "isDeleted": true, "data": map[string]any{"x": 1}})
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
		{
			"range predicate true",
			`{"starlark":"def guard(subject): return subject.data.age >= 18"}`,
			true,
		},
		{
			"range predicate false",
			`{"starlark":"def guard(subject): return subject.data.age >= 65"}`,
			false,
		},
		{
			"cross-field predicate true",
			`{"reads":["lease"],"starlark":"def guard(subject): return time.rfc3339_utc(subject.lease.data.startDate) < time.rfc3339_utc(subject.lease.data.endDate)"}`,
			true,
		},
		{
			"cross-field predicate false",
			`{"reads":["lease"],"starlark":"def guard(subject): return time.rfc3339_utc(subject.lease.data.endDate) < time.rfc3339_utc(subject.lease.data.startDate)"}`,
			false,
		},
		{
			"soft-deleted aspect projects None",
			`{"reads":["tomb"],"starlark":"def guard(subject): return subject.tomb == None"}`,
			true,
		},
		{
			"undeclared aspect (not in reads) projects None",
			`{"starlark":"def guard(subject): return subject.lease == None"}`,
			true,
		},
		{
			"missing data field reads as None via dot access",
			`{"starlark":"def guard(subject): return subject.data.nope == None"}`,
			true,
		},
		{
			"data dict subscript access works",
			`{"starlark":"def guard(subject): return subject.data[\"age\"] == 21"}`,
			true,
		},
		{
			"data dict .get() method works",
			`{"starlark":"def guard(subject): return subject.data.get(\"age\", 0) == 21"}`,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, eval(t, tc.raw))
		})
	}
}

// TestGuardEval_Starlark_SubjectNeverNoneEvenWithAbsentRoot proves `subject`
// itself is NEVER None, even when the root vertex was never created — only
// an aspect (subject.<name>) collapses to None on absence. This mirrors the
// declarative grammar exactly: `subject.data.<field>` reads absent without
// requiring the root vertex to exist (guard_eval.go's resolve() treats a
// missing root like a missing aspect at the FIELD level, never at the whole-
// subject level), and matches the canonical example (Contract §10.5:
// `def guard(subject): return subject.profile.data.age >= 18`) which never
// defensively checks `subject != None` first.
func TestGuardEval_Starlark_SubjectNeverNoneEvenWithAbsentRoot(t *testing.T) {
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

	// The root vertex is deliberately NEVER written — only its aspect.
	subjectKey := "vtx.identity." + mustNanoID(t)
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada"})

	g, perr := loom.ParseGuardForTest(
		`{"reads":["profile"],"starlark":"def guard(subject): return subject != None and subject.data.get(\"missing\") == None and subject.profile.data.get(\"name\") == \"Ada\""}`)
	require.NoError(t, perr)
	got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
	require.NoError(t, eerr)
	require.True(t, got, "subject must stay non-None with an empty .data even though the root vertex was never created")
}

// TestGuardEval_Starlark_NonBoolReturn asserts a guard that returns a
// non-bool is an evaluation error, never silently coerced.
func TestGuardEval_Starlark_NonBoolReturn(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{"class": "identity", "data": map[string]any{}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	g, perr := loom.ParseGuardForTest(`{"starlark":"def guard(subject): return 1"}`)
	require.NoError(t, perr)
	_, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
	require.Error(t, eerr)
	require.Contains(t, eerr.Error(), "bool")
}

// TestGuardEval_Starlark_WallBudgetTrips asserts a pathological guard (an
// infinite loop) fails fast via the wall/step budget rather than stalling
// the engine's transition loop indefinitely.
func TestGuardEval_Starlark_WallBudgetTrips(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{"class": "identity", "data": map[string]any{}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	g, perr := loom.ParseGuardForTest(`{"starlark":"def guard(subject):\n    x = 0\n    for i in range(100000000):\n        x += i\n    return True"}`)
	require.NoError(t, perr)
	start := time.Now()
	_, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
	elapsed := time.Since(start)
	require.Error(t, eerr)
	require.Less(t, elapsed, 5*time.Second, "guard eval should fail fast via the wall/step budget, not hang")
}

// TestGuardEval_Starlark_FrozenSubjectImmutable proves the frozen `subject`
// value physically cannot be mutated by the predicate (design doc §2.2's
// "the value is Freeze()d" claim, §7's adversarial-pass immutability proof).
func TestGuardEval_Starlark_FrozenSubjectImmutable(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{"class": "identity", "data": map[string]any{"age": 21}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	// subject.data's underlying dict does not expose Starlark's `x[k]=v` item-
	// assignment syntax at all (starlarkDataDict implements no HasSetKey), so
	// that alone wouldn't prove Freeze() is doing anything. `.update(...)` IS a
	// real dict-mutating builtin method (passed through to the underlying
	// *starlark.Dict) — it fails ONLY because Freeze() marked the hash table
	// frozen, which is the actual property under test.
	g, perr := loom.ParseGuardForTest(`{"starlark":"def guard(subject):\n    subject.data.update({\"age\": 99})\n    return True"}`)
	require.NoError(t, perr)
	_, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
	require.Error(t, eerr)
	require.True(t, strings.Contains(eerr.Error(), "frozen"),
		"want a frozen-hash-table mutation-rejected error, got %v", eerr)
}

// TestGuardEval_Starlark_Determinism proves the load-bearing property (design
// doc §4 test bullet 4): the SAME subject snapshot evaluated twice yields the
// SAME bool — the property the §10.6 cursor-rebuild invariant depends on.
func TestGuardEval_Starlark_Determinism(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{"class": "identity", "data": map[string]any{"age": 30}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)

	g, perr := loom.ParseGuardForTest(`{"starlark":"def guard(subject): return subject.data.age >= 18"}`)
	require.NoError(t, perr)

	var results []bool
	for i := 0; i < 5; i++ {
		got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
		require.NoError(t, eerr)
		results = append(results, got)
	}
	for _, r := range results {
		require.Equal(t, results[0], r, "same subject snapshot must evaluate to the same bool every time")
	}
}

// starlarkGuardedOnboardingPattern is guardedOnboardingPattern's twin with
// step 0's guard rewritten from the declarative `{"absent": ...}` atom to an
// equivalent Starlark predicate — used to prove the §10.6 cursor-rebuild
// invariant holds identically through the Starlark evaluation path (design
// doc §4 test bullet 4's replay-golden proof).
func starlarkGuardedOnboardingPattern(patternID string) loom.Pattern {
	return loom.Pattern{
		PatternID:         patternID,
		SubjectType:       "identity",
		CompletionDomains: []string{"orchestration"},
		Steps: []loom.Step{
			{Kind: "userTask", Operation: "SetName", Guard: json.RawMessage(
				`{"reads":["profile"],"starlark":"def guard(subject): return subject.profile == None or subject.profile.data.get('name') == None"}`)},
			{Kind: "userTask", Operation: "SetPhone", Guard: json.RawMessage(`{"absent":"subject.profile.data.phone"}`)},
			{Kind: "userTask", Operation: "SetAddress"},
		},
	}
}

// TestGuardE2E_DisasterRecoveryCursorRebuild_StarlarkGuard is
// TestGuardE2E_DisasterRecoveryCursorRebuild's twin with a Starlark guard on
// step 0 instead of a declarative one — the executable proof (design doc §4
// test bullet 4) that a Starlark guard is recovery-idempotent by
// construction exactly like the declarative grammar: total loom-state loss
// followed by a re-driven instance replays the Starlark guard from cursor 0
// against the unchanged subject and lands on the SAME effective step
// (SetPhone), never re-issuing SetName's task.
func TestGuardE2E_DisasterRecoveryCursorRebuild_StarlarkGuard(t *testing.T) {
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
	installPattern(t, ctx, conn, patternID, starlarkGuardedOnboardingPattern(patternID))

	subjectKey := "vtx.identity." + mustNanoID(t)
	// name already present → step 0's starlark guard is false from the start.
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
	// SAME StartLoomPattern. ---
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
	// false starlark guard skipped it both times (no double-submit of the
	// skipped step).
	require.Equal(t, 2, fp.createTaskCount(),
		"only the two SetPhone tasks (gen1 + gen2's own first run); SetName never created")

	// --- Drive gen-2 to completion through the guardless final step. ---
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

// TestGuardEval_Starlark_HydrationDedup proves the §guardResolver
// one-snapshot-per-key property (design doc §4 test bullet 5) holds through
// the Starlark path: a composite guard mixing a declarative atom and a
// Starlark atom over the SAME aspect see the identical snapshot within one
// evaluation — both branches share the one *guardResolver evalGuard
// constructs for the whole tree (guard_eval.go's evalStarlark calls r.envelope,
// the same memoized method the declarative atoms use).
func TestGuardEval_Starlark_HydrationDedup(t *testing.T) {
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
	rootBody, _ := json.Marshal(map[string]any{"class": "identity", "data": map[string]any{}})
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada"})

	g, perr := loom.ParseGuardForTest(
		`{"allOf":[{"present":"subject.profile.data.name"},` +
			`{"reads":["profile"],"starlark":"def guard(subject): return subject.profile.data.name == \"Ada\""}]}`)
	require.NoError(t, perr)
	got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
	require.NoError(t, eerr)
	require.True(t, got, "both branches of the composite guard must see the same profile snapshot")
}
