package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- Pure helper: applyHydratedRevisions (Contract #3 §3.2 default) ---

func TestApplyHydratedRevisions(t *testing.T) {
	rev := func(v uint64) *uint64 { return &v }
	hydrated := map[string]VertexDoc{
		"vtx.identity.aaa.profile": {Revision: 7},
		"vtx.identity.bbb":         {Revision: 12},
	}

	t.Run("default update conditioned on hydrated revision", func(t *testing.T) {
		muts := []MutationOp{{Op: "update", Key: "vtx.identity.aaa.profile"}}
		defaulted := applyHydratedRevisions(muts, hydrated)
		if muts[0].ExpectedRevision == nil || *muts[0].ExpectedRevision != 7 {
			t.Fatalf("ExpectedRevision = %v, want 7", muts[0].ExpectedRevision)
		}
		if defaulted["vtx.identity.aaa.profile"] != 7 {
			t.Fatalf("defaulted should map the key to its conditioned revision 7, got %v", defaulted)
		}
	})

	t.Run("tombstone conditioned on hydrated revision", func(t *testing.T) {
		muts := []MutationOp{{Op: "tombstone", Key: "vtx.identity.bbb"}}
		defaulted := applyHydratedRevisions(muts, hydrated)
		if muts[0].ExpectedRevision == nil || *muts[0].ExpectedRevision != 12 {
			t.Fatalf("ExpectedRevision = %v, want 12", muts[0].ExpectedRevision)
		}
		if defaulted["vtx.identity.bbb"] != 12 {
			t.Fatalf("defaulted rev = %v, want 12", defaulted["vtx.identity.bbb"])
		}
	})

	t.Run("explicit expectedRevision never overridden", func(t *testing.T) {
		muts := []MutationOp{{Op: "update", Key: "vtx.identity.aaa.profile", ExpectedRevision: rev(3)}}
		defaulted := applyHydratedRevisions(muts, hydrated)
		if *muts[0].ExpectedRevision != 3 {
			t.Fatalf("explicit ExpectedRevision was overridden: %d", *muts[0].ExpectedRevision)
		}
		if _, ok := defaulted["vtx.identity.aaa.profile"]; ok {
			t.Fatalf("explicit-CAS key must NOT be marked defaulted (not retry-eligible)")
		}
	})

	t.Run("create is never conditioned", func(t *testing.T) {
		muts := []MutationOp{{Op: "create", Key: "vtx.identity.aaa.profile"}}
		defaulted := applyHydratedRevisions(muts, hydrated)
		if muts[0].ExpectedRevision != nil {
			t.Fatalf("create must stay unconditioned, got %v", muts[0].ExpectedRevision)
		}
		if len(defaulted) != 0 {
			t.Fatalf("create must not be defaulted")
		}
	})

	t.Run("update on a key not read at step 4 stays unconditioned", func(t *testing.T) {
		muts := []MutationOp{{Op: "update", Key: "vtx.identity.zzz.unread"}}
		defaulted := applyHydratedRevisions(muts, hydrated)
		if muts[0].ExpectedRevision != nil {
			t.Fatalf("an un-hydrated update has no step-4 revision; want unconditioned, got %v", muts[0].ExpectedRevision)
		}
		if len(defaulted) != 0 {
			t.Fatalf("un-hydrated key must not be defaulted")
		}
	})

	t.Run("empty hydrated set is a no-op", func(t *testing.T) {
		muts := []MutationOp{{Op: "update", Key: "vtx.identity.aaa.profile"}}
		defaulted := applyHydratedRevisions(muts, nil)
		if muts[0].ExpectedRevision != nil || defaulted != nil {
			t.Fatalf("nil hydrated must leave mutations untouched")
		}
	})
}

// --- Pure helper: conditionRevision (step 8's per-mutation batch condition) ---

func TestConditionRevision(t *testing.T) {
	t.Parallel()
	key := "vtx.identity." + testNanoID2 + ".profile"

	t.Run("explicit ExpectedRevision wins even when prior also has an entry", func(t *testing.T) {
		explicit := uint64(3)
		m := MutationOp{Op: "update", Key: key, ExpectedRevision: &explicit}
		prior := PriorDocs{key: priorDoc{Revision: 99, Found: true}}
		rev, ok, fallback := conditionRevision(m, prior)
		if !ok || fallback {
			t.Fatalf("ok=%v fallback=%v, want ok=true fallback=false", ok, fallback)
		}
		if rev != 3 {
			t.Fatalf("rev = %d, want the explicit revision 3, not prior's 99", rev)
		}
	})

	t.Run("nil ExpectedRevision + key present in prior → fallback-conditioned", func(t *testing.T) {
		m := MutationOp{Op: "update", Key: key}
		prior := PriorDocs{key: priorDoc{Revision: 42, Found: true}}
		rev, ok, fallback := conditionRevision(m, prior)
		if !ok || !fallback {
			t.Fatalf("ok=%v fallback=%v, want ok=true fallback=true", ok, fallback)
		}
		if rev != 42 {
			t.Fatalf("rev = %d, want prior's 42", rev)
		}
	})

	t.Run("nil ExpectedRevision + key absent from prior → unconditioned", func(t *testing.T) {
		m := MutationOp{Op: "update", Key: key}
		prior := PriorDocs{}
		if _, ok, _ := conditionRevision(m, prior); ok {
			t.Fatalf("a key with no prior document must be unconditioned")
		}
	})

	t.Run("nil ExpectedRevision + key present but not Found → unconditioned", func(t *testing.T) {
		m := MutationOp{Op: "update", Key: key}
		prior := PriorDocs{key: priorDoc{Found: false}}
		if _, ok, _ := conditionRevision(m, prior); ok {
			t.Fatalf("a prior entry with Found=false must be unconditioned")
		}
	})
}

// --- Structural conflict attribution: movedConditionedKeys (the production
// path: NATS does not name the failing key, so we re-read the conditioned
// keys) ---

func TestMovedConditionedKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)
	cp := newOCCPipeline(t, conn, nil, nil, &occFakeCommitter{}, &Metrics{})

	key := "vtx.identity." + testNanoID2 + ".profile"
	rev := occWriteKey(t, ctx, conn, key)

	t.Run("key still at conditioned revision → not moved", func(t *testing.T) {
		moved := cp.movedConditionedKeys(ctx, map[string]uint64{key: rev})
		if len(moved) != 0 {
			t.Fatalf("an unchanged key must not be reported moved, got %v", moved)
		}
	})

	t.Run("key bumped past conditioned revision → moved", func(t *testing.T) {
		moved := cp.movedConditionedKeys(ctx, map[string]uint64{key: rev - 1}) // assert a stale rev
		if len(moved) != 1 || moved[0] != key {
			t.Fatalf("a key whose revision advanced must be reported moved, got %v", moved)
		}
	})

	t.Run("hard-deleted key → moved (conditioned revision no longer valid)", func(t *testing.T) {
		gone := "vtx.identity." + testNanoID1 + ".gone"
		moved := cp.movedConditionedKeys(ctx, map[string]uint64{gone: 5})
		if len(moved) != 1 || moved[0] != gone {
			t.Fatalf("an absent key must be reported moved, got %v", moved)
		}
	})

	t.Run("empty defaulted set → nil", func(t *testing.T) {
		if moved := cp.movedConditionedKeys(ctx, nil); moved != nil {
			t.Fatalf("nil defaulted must yield nil, got %v", moved)
		}
	})
}

// --- Retry loop (commitPipeline) over fake steps + a real embedded conn ---

type occFakeHydrator struct{ state HydratedState }

func (h occFakeHydrator) Hydrate(_ context.Context, _ *OperationEnvelope) (HydratedState, error) {
	return h.state, nil
}

type occFakeExecutor struct{ result ScriptResult }

// Execute returns a FRESH copy of the mutation set each call, mirroring the real
// Starlark executor: a re-execution on retry re-derives mutations from scratch
// (no expectedRevision carried over from a prior attempt's defaulting).
func (e occFakeExecutor) Execute(_ context.Context, _ *OperationEnvelope, _ HydratedState) (ScriptResult, error) {
	muts := make([]MutationOp, len(e.result.Mutations))
	for i, m := range e.result.Mutations {
		muts[i] = m
		muts[i].ExpectedRevision = nil
		if m.ExpectedRevision != nil {
			rev := *m.ExpectedRevision
			muts[i].ExpectedRevision = &rev
		}
	}
	return ScriptResult{Mutations: muts, Events: e.result.Events, PrimaryKey: e.result.PrimaryKey}, nil
}

// occFakeCommitter fails the first failFor calls (or always) with a ConflictError
// that carries NO ConflictingKey — exactly what the real committer produces,
// since NATS omits the failing subject from an atomic-batch rejection. On each
// failing call it bumps bumpKey's Core-KV revision to simulate the concurrent
// winner, so the production structural-attribution path (movedConditionedKeys) sees
// the conditioned key has moved and drives the retry.
type occFakeCommitter struct {
	conn       *substrate.Conn
	calls      atomic.Uint64
	failFor    int
	alwaysFail bool
	bumpKey    string
	// viaFallback reports bumpKey on the simulated conflict's
	// FallbackConditionedKeys instead of relying on the §3.2-defaulted set — it
	// exercises the undeclared-key retry path (the key step 8 conditioned via
	// its own prior-document read, never hydrated at step 4) rather than the
	// applyHydratedRevisions path the other occFakeCommitter tests exercise.
	viaFallback bool
}

func (c *occFakeCommitter) ReadPrior(_ context.Context, _ []MutationOp) (PriorDocs, error) {
	return PriorDocs{}, nil
}

func (c *occFakeCommitter) Commit(ctx context.Context, _ *OperationEnvelope, _ ScriptResult, _ Tracker, _ PriorDocs) (CommitAck, error) {
	n := int(c.calls.Add(1))
	if c.alwaysFail || n <= c.failFor {
		var fallback map[string]uint64
		if c.bumpKey != "" && c.conn != nil {
			entry, err := c.conn.KVGet(ctx, testCoreBucket, c.bumpKey)
			preBumpRev := uint64(0)
			if err == nil {
				preBumpRev = entry.Revision
			}
			_, _ = c.conn.KVPut(ctx, testCoreBucket, c.bumpKey,
				[]byte(fmt.Sprintf(`{"key":%q,"class":"identity","isDeleted":false,"data":{}}`, c.bumpKey)))
			if c.viaFallback {
				fallback = map[string]uint64{c.bumpKey: preBumpRev}
			}
		}
		return CommitAck{}, &ConflictError{Cause: substrate.ErrAtomicBatchRejected, FallbackConditionedKeys: fallback} // empty key, like prod
	}
	return CommitAck{Revisions: map[string]uint64{}}, nil
}

func occConn(t *testing.T) *substrate.Conn {
	t.Helper()
	return connectSubstrate(t, startEmbeddedNATS(t), "occ-test")
}

func occWriteKey(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) uint64 {
	t.Helper()
	rev, err := conn.KVPut(ctx, testCoreBucket, key,
		[]byte(fmt.Sprintf(`{"key":%q,"class":"identity","isDeleted":false,"data":{}}`, key)))
	if err != nil {
		t.Fatalf("seed key %s: %v", key, err)
	}
	return rev
}

func newOCCPipeline(t *testing.T, conn *substrate.Conn, hyd Hydrator, exec Executor, comm Committer, metrics *Metrics) *CommitPath {
	t.Helper()
	return newOCCPipelineAuth(t, conn, NewStubAuthorizer(testLogger()), hyd, exec, comm, metrics)
}

func newOCCPipelineAuth(t *testing.T, conn *substrate.Conn, authz Authorizer, hyd Hydrator, exec Executor, comm Committer, metrics *Metrics) *CommitPath {
	t.Helper()
	logger := testLogger()
	return NewCommitPath(Deps{
		Conn:               conn,
		CoreBucket:         testCoreBucket,
		HealthKV:           testHealthBucket,
		Authorizer:         authz,
		Hydrator:           hyd,
		Executor:           exec,
		Validator:          &StubValidator{logger: logger},
		Committer:          comm,
		Metrics:            metrics,
		Logger:             logger,
		CommitRetryBackoff: func(int) time.Duration { return 0 }, // deterministic + fast
	})
}

func driveOCC(t *testing.T, ctx context.Context, cp *CommitPath, requestID string) (MessageOutcome, substrate.Decision) {
	t.Helper()
	env := newTestEnvelope(requestID)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	msg := substrate.Message{
		Subject:      "ops.default",
		Body:         b,
		ReplySubject: "",
		Header:       func(string) string { return "" },
	}
	return cp.dispatch(ctx, msg)
}

// occUpdateState seeds `key` in Core KV and returns a hydrated state + result for
// a single default update on it, conditioned on the seeded revision.
func occUpdateState(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) (HydratedState, ScriptResult) {
	rev := occWriteKey(t, ctx, conn, key)
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{key: {Key: key, Revision: rev}}}}
	result := ScriptResult{Mutations: []MutationOp{{
		Op:       "update",
		Key:      key,
		Document: map[string]interface{}{"class": "identity", "isDeleted": false, "data": map[string]any{"name": "Andrew"}},
	}}}
	return state, result
}

func TestCommitPipeline_RetryAbsorbsBenignConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.identity." + testNanoID2 + ".profile"
	state, result := occUpdateState(t, ctx, conn, key)
	// Conflict once (bumping the conditioned key to simulate the winner), then succeed.
	committer := &occFakeCommitter{conn: conn, failFor: 1, bumpKey: key}
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn, occFakeHydrator{state}, occFakeExecutor{result}, committer, metrics)

	outcome, decision := driveOCC(t, ctx, cp, testNanoID1)
	if outcome != OutcomeAccepted || decision != substrate.Ack {
		t.Fatalf("outcome=%v decision=%v, want accepted/Ack", outcome, decision)
	}
	if got := committer.calls.Load(); got != 2 {
		t.Fatalf("committer calls = %d, want 2 (one conflict + one success)", got)
	}
	if metrics.CommitRetries.Load() != 1 {
		t.Fatalf("CommitRetries = %d, want 1", metrics.CommitRetries.Load())
	}
	if metrics.CommitRetryExhausted.Load() != 0 {
		t.Fatalf("CommitRetryExhausted = %d, want 0", metrics.CommitRetryExhausted.Load())
	}
	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
}

// TestCommitPipeline_RetryAbsorbsBenignConflict_UndeclaredKey mirrors
// TestCommitPipeline_RetryAbsorbsBenignConflict but for the undeclared-key
// class: the mutation's key is absent from step 4's Hydrated set (never
// declared in contextHint.reads — the shape of a key discovered only via
// kv.Links at execution time), so applyHydratedRevisions leaves it
// unconditioned and only step 8's own prior-document read can condition it.
// The conflict is reported via ConflictError.FallbackConditionedKeys, not the
// §3.2-defaulted set, and must be retried exactly like a defaulted key.
func TestCommitPipeline_RetryAbsorbsBenignConflict_UndeclaredKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.identity." + testNanoID2 + ".profile"
	occWriteKey(t, ctx, conn, key)
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{}}}
	result := ScriptResult{Mutations: []MutationOp{{
		Op:       "update",
		Key:      key,
		Document: map[string]interface{}{"class": "identity", "isDeleted": false, "data": map[string]any{"name": "Andrew"}},
	}}}
	// Conflict once (bumping the fallback-conditioned key, reported via
	// FallbackConditionedKeys rather than the §3.2-defaulted set), then succeed.
	committer := &occFakeCommitter{conn: conn, failFor: 1, bumpKey: key, viaFallback: true}
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn, occFakeHydrator{state}, occFakeExecutor{result}, committer, metrics)

	outcome, decision := driveOCC(t, ctx, cp, testNanoID1)
	if outcome != OutcomeAccepted || decision != substrate.Ack {
		t.Fatalf("outcome=%v decision=%v, want accepted/Ack", outcome, decision)
	}
	if got := committer.calls.Load(); got != 2 {
		t.Fatalf("committer calls = %d, want 2 (one conflict + one success)", got)
	}
	if metrics.CommitRetries.Load() != 1 {
		t.Fatalf("CommitRetries = %d, want 1", metrics.CommitRetries.Load())
	}
	if metrics.CommitRetryExhausted.Load() != 0 {
		t.Fatalf("CommitRetryExhausted = %d, want 0", metrics.CommitRetryExhausted.Load())
	}
	if metrics.OpsCommitted.Load() != 1 {
		t.Fatalf("OpsCommitted = %d, want 1", metrics.OpsCommitted.Load())
	}
}

func TestCommitPipeline_ExhaustionSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.identity." + testNanoID2 + ".profile"
	state, result := occUpdateState(t, ctx, conn, key)
	committer := &occFakeCommitter{conn: conn, alwaysFail: true, bumpKey: key}
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn, occFakeHydrator{state}, occFakeExecutor{result}, committer, metrics)

	outcome, decision := driveOCC(t, ctx, cp, testNanoID1)
	if outcome != OutcomeRejected || decision != substrate.Term {
		t.Fatalf("outcome=%v decision=%v, want rejected/Term", outcome, decision)
	}
	if got := committer.calls.Load(); got != uint64(defaultMaxCommitAttempts) {
		t.Fatalf("committer calls = %d, want %d (the full attempt budget)", got, defaultMaxCommitAttempts)
	}
	if metrics.CommitRetries.Load() != uint64(defaultMaxCommitAttempts-1) {
		t.Fatalf("CommitRetries = %d, want %d", metrics.CommitRetries.Load(), defaultMaxCommitAttempts-1)
	}
	if metrics.CommitRetryExhausted.Load() != 1 {
		t.Fatalf("CommitRetryExhausted = %d, want 1", metrics.CommitRetryExhausted.Load())
	}
	if metrics.OpsCommitted.Load() != 0 {
		t.Fatalf("OpsCommitted = %d, want 0", metrics.OpsCommitted.Load())
	}
}

func TestCommitPipeline_CreateOnceCollisionSurfacesWithoutRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	// A create mutation is never defaulted → no conditioned key moves → the
	// conflict is a uniqueness/domain reject, surfaced immediately (no retry).
	key := "vtx.identityindex." + testNanoID2
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{}}}
	result := ScriptResult{Mutations: []MutationOp{{
		Op:       "create",
		Key:      key,
		Document: map[string]interface{}{"class": "identityindex", "data": map[string]any{"contactType": "phone"}},
	}}}
	committer := &occFakeCommitter{conn: conn, alwaysFail: true} // no bumpKey: nothing moves
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn, occFakeHydrator{state}, occFakeExecutor{result}, committer, metrics)

	outcome, _ := driveOCC(t, ctx, cp, testNanoID1)
	if outcome != OutcomeRejected {
		t.Fatalf("outcome=%v, want rejected", outcome)
	}
	if got := committer.calls.Load(); got != 1 {
		t.Fatalf("committer calls = %d, want 1 (no retry on a create-once collision)", got)
	}
	if metrics.CommitRetries.Load() != 0 || metrics.CommitRetryExhausted.Load() != 0 {
		t.Fatalf("create-once collision must not touch the retry counters: retries=%d exhausted=%d",
			metrics.CommitRetries.Load(), metrics.CommitRetryExhausted.Load())
	}
}

func TestCommitPipeline_ExplicitRevisionConflictNotRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	// An update carrying an explicit expectedRevision (a deliberate caller CAS, e.g.
	// a compensating op) is NOT defaulted, so even though the key exists and could
	// move, it is not in the defaulted set → the conflict surfaces unchanged.
	key := "vtx.identity." + testNanoID2 + ".profile"
	occWriteKey(t, ctx, conn, key)
	explicit := uint64(99)
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{key: {Key: key, Revision: 7}}}}
	result := ScriptResult{Mutations: []MutationOp{{
		Op:               "update",
		Key:              key,
		ExpectedRevision: &explicit,
		Document:         map[string]interface{}{"class": "identity", "isDeleted": false, "data": map[string]any{}},
	}}}
	committer := &occFakeCommitter{conn: conn, alwaysFail: true, bumpKey: key}
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn, occFakeHydrator{state}, occFakeExecutor{result}, committer, metrics)

	outcome, _ := driveOCC(t, ctx, cp, testNanoID1)
	if outcome != OutcomeRejected {
		t.Fatalf("outcome=%v, want rejected", outcome)
	}
	if got := committer.calls.Load(); got != 1 {
		t.Fatalf("committer calls = %d, want 1 (explicit-CAS conflict is not retried)", got)
	}
}

// countingAuthorizer wraps an Authorizer to count Authorize calls — the Gate-3
// "internal retry cannot bypass auth" property: auth runs exactly once across N
// commit retries (the retry re-executes the same already-authorized envelope).
type countingAuthorizer struct {
	inner Authorizer
	calls atomic.Uint64
}

func (a *countingAuthorizer) Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	a.calls.Add(1)
	return a.inner.Authorize(ctx, env)
}

func TestCommitPipeline_AuthRunsOnceAcrossRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.identity." + testNanoID2 + ".profile"
	state, result := occUpdateState(t, ctx, conn, key)
	authz := &countingAuthorizer{inner: NewStubAuthorizer(testLogger())}
	committer := &occFakeCommitter{conn: conn, failFor: 1, bumpKey: key} // one retry
	cp := newOCCPipelineAuth(t, conn, authz, occFakeHydrator{state}, occFakeExecutor{result}, committer, &Metrics{})

	if outcome, _ := driveOCC(t, ctx, cp, testNanoID1); outcome != OutcomeAccepted {
		t.Fatalf("outcome=%v, want accepted", outcome)
	}
	if committer.calls.Load() != 2 {
		t.Fatalf("expected a retry (2 commit calls), got %d", committer.calls.Load())
	}
	if got := authz.calls.Load(); got != 1 {
		t.Fatalf("Authorize called %d times across the retry; want exactly 1 (no re-auth on retry)", got)
	}
}

// TestRealCommitter_Section32Conditioning demonstrates Part (A) end-to-end
// against the REAL committer + real NATS CAS: an update conditioned on the
// hydrated revision succeeds; the same revision, asserted after the root moved,
// is rejected by the substrate (the lost-update guard §3.2 closes).
func TestRealCommitter_Section32Conditioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)
	committer := NewCommitter(conn, testCoreBucket, nil, testLogger(), time.Now)

	root := "vtx.identity." + testNanoID2
	env := newTestEnvelope(testNanoID1)

	// Create the root.
	createRes := ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: root,
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "A"}},
	}}}
	ack, err := committer.Commit(ctx, env, createRes, NewTracker(env, time.Now()), nil)
	if err != nil {
		t.Fatalf("create commit: %v", err)
	}
	rev := ack.Revisions[root]
	if rev == 0 {
		t.Fatalf("no revision derived for %s", root)
	}

	// A conditioned update on the current revision succeeds.
	env2 := newTestEnvelope(testNanoID2)
	r := rev
	updRes := ScriptResult{Mutations: []MutationOp{{
		Op: "update", Key: root, ExpectedRevision: &r,
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "B"}},
	}}}
	if _, err := committer.Commit(ctx, env2, updRes, NewTracker(env2, time.Now()), nil); err != nil {
		t.Fatalf("conditioned update on current revision should succeed: %v", err)
	}

	// The SAME (now stale) revision is rejected — the lost-update guard fires.
	env3 := newTestEnvelope(testCoreBucket + "-stale")
	staleRes := ScriptResult{Mutations: []MutationOp{{
		Op: "update", Key: root, ExpectedRevision: &r, // r is now stale (root moved to rev+1)
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "C"}},
	}}}
	_, err = committer.Commit(ctx, env3, staleRes, NewTracker(env3, time.Now()), nil)
	var confErr *ConflictError
	if err == nil || !errors.As(err, &confErr) {
		t.Fatalf("a stale conditioned update must be rejected as a ConflictError, got %v", err)
	}
}

// TestRealCommitter_FallbackConditioning demonstrates the step-8 fallback path
// end-to-end against the REAL committer + real NATS CAS. It is deliberately
// NOT a plain success check: an unconditioned write also succeeds, so success
// alone cannot distinguish "conditioned on the prior-read revision" from "not
// conditioned at all" (deleting the fallback wiring entirely would still leave
// a bare success assertion green). Instead this forces a genuine
// ErrAtomicBatchRejected — a create-once collision on an UNRELATED key in the
// same batch, deterministic, no goroutine racing needed — and asserts the
// returned ConflictError.FallbackConditionedKeys reports the EXACT revision
// step 8's own prior-document read (readPriorDocuments) conditioned the
// untouched update mutation on.
func TestRealCommitter_FallbackConditioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)
	committer := NewCommitter(conn, testCoreBucket, nil, testLogger(), time.Now)

	keyA := "vtx.identity." + testNanoID1
	keyB := "vtx.identity." + testNanoID2

	// Seed B via a real commit, capturing the revision it lands at.
	envSeedB := newTestEnvelope(testNanoID2 + "-seed-b")
	createB := ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: keyB,
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "B"}},
	}}}
	ackB, err := committer.Commit(ctx, envSeedB, createB, NewTracker(envSeedB, time.Now()), nil)
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}
	revB := ackB.Revisions[keyB]
	if revB == 0 {
		t.Fatalf("no revision derived for %s", keyB)
	}

	// Seed A too, so a later CreateOnly on A is guaranteed to collide.
	envSeedA := newTestEnvelope(testNanoID1 + "-seed-a")
	createA := ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: keyA,
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "A"}},
	}}}
	if _, err := committer.Commit(ctx, envSeedA, createA, NewTracker(envSeedA, time.Now()), nil); err != nil {
		t.Fatalf("seed A: %v", err)
	}

	// One batch: (i) a create on the now-existing A — CreateOnly fails this op
	// deterministically, rejecting the WHOLE atomic batch — and (ii) an update
	// on B with NO ExpectedRevision (never hydrated at step 4, so the §3.2
	// default never fires). Nothing has touched B since the seed, so step 8's
	// fallback conditioning must have asserted exactly revB.
	envConflict := newTestEnvelope(testNanoID1 + "-conflict")
	conflictRes := ScriptResult{Mutations: []MutationOp{
		{Op: "create", Key: keyA, Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "A-again"}}},
		{Op: "update", Key: keyB, Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "B-updated"}}},
	}}
	_, err = committer.Commit(ctx, envConflict, conflictRes, NewTracker(envConflict, time.Now()), nil)
	var confErr *ConflictError
	if err == nil || !errors.As(err, &confErr) {
		t.Fatalf("a create-once collision on A must reject the whole batch as a ConflictError, got %v", err)
	}
	if got := confErr.FallbackConditionedKeys[keyB]; got != revB {
		t.Fatalf("FallbackConditionedKeys[%s] = %d, want %d (the revision B was actually fallback-conditioned on)", keyB, got, revB)
	}
}

func TestRecordCommitConflict_HealthKV(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	instance := "proc-test-" + testNanoID1
	emitter := NewCommitConflictEmitter(conn, testHealthBucket, instance, testLogger())
	emitter.RecordCommitConflict(ctx, CommitConflictInfo{
		ConflictingKey: "vtx.identity.aaa.profile", Lane: "default", OperationType: "UpdateProfile", Exhausted: false,
	})
	emitter.RecordCommitConflict(ctx, CommitConflictInfo{
		ConflictingKey: "vtx.identity.aaa.profile", Lane: "system", OperationType: "directOp", Exhausted: true,
	})

	key := "health.processor." + instance + ".commit-conflicts"
	doc := readHealthDoc(t, ctx, conn, testHealthBucket, key)
	if doc["count"] != float64(2) {
		t.Fatalf("count = %v, want 2 (rolling counter)", doc["count"])
	}
	if doc["conflictingKey"] != "vtx.identity.aaa.profile" {
		t.Fatalf("conflictingKey = %v", doc["conflictingKey"])
	}
	// Last writer wins ("currently happening", not an audit log).
	if doc["lane"] != "system" || doc["operationType"] != "directOp" || doc["exhausted"] != true {
		t.Fatalf("expected last-write-wins context, got lane=%v op=%v exhausted=%v",
			doc["lane"], doc["operationType"], doc["exhausted"])
	}
}

func TestNewCommitConflictEmitter_NoopWhenUnwired(t *testing.T) {
	e := NewCommitConflictEmitter(nil, "", "", testLogger())
	if _, ok := e.(noopCommitConflictEmitter); !ok {
		t.Fatalf("nil conn must yield the noop emitter, got %T", e)
	}
	e.RecordCommitConflict(context.Background(), CommitConflictInfo{ConflictingKey: "k"}) // must not panic
}

// A resubmit of a requestId whose PRIOR incarnation's outbox aspect was
// tombstoned by the outbox consumer (post-publish KV delete → a DEL marker
// still occupying the subject) must commit. Contract #4 §4.3: after the 24h
// tracker TTL, resubmission is a legitimate fresh execution — a conditioned
// outbox write would collide with the marker and permanently brick every
// deterministic-requestId reuse (e.g. same-version `lattice-pkg install
// --force` refreshes).
func TestRealCommitter_ResubmitAfterOutboxTombstone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)
	committer := NewCommitter(conn, testCoreBucket, nil, testLogger(), time.Now)

	rid := testNanoID1
	env := newTestEnvelope(rid)
	outboxKey := TrackerKey(rid) + ".events"

	// Simulate the prior incarnation's residue: the tracker has TTL'd out
	// (never written here), and the consumer's tombstone left a DEL marker
	// on the outbox subject.
	if _, err := conn.KVPut(ctx, testCoreBucket, outboxKey, []byte(`{"stale":"prior incarnation"}`)); err != nil {
		t.Fatalf("seed stale outbox aspect: %v", err)
	}
	if err := conn.KVDelete(ctx, testCoreBucket, outboxKey); err != nil {
		t.Fatalf("tombstone outbox aspect: %v", err)
	}

	res := ScriptResult{
		Mutations: []MutationOp{{
			Op: "create", Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "A"}},
		}},
		Events: []EventSpec{{Class: "identity.created", Data: map[string]any{"k": "v"}}},
	}
	if _, err := committer.Commit(ctx, env, res, NewTracker(env, time.Now()), nil); err != nil {
		t.Fatalf("resubmit over outbox tombstone residue must commit, got: %v", err)
	}

	// The fresh outbox aspect must be readable (the DEL marker superseded).
	if _, err := conn.KVGet(ctx, testCoreBucket, outboxKey); err != nil {
		t.Fatalf("outbox aspect not present after commit: %v", err)
	}
}

// An operator-tombstoned tracker (in-body isDeleted: true — Contract #4 §4.4's
// tombstone-then-resubmit retry signal) still occupies its subject, so the
// re-execution's tracker write must supersede it by revision rather than
// attempt a create-only write that can never succeed there.
func TestRealCommitter_ResubmitSupersedesTombstonedTracker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)
	committer := NewCommitter(conn, testCoreBucket, nil, testLogger(), time.Now)

	rid := testNanoID2
	env := newTestEnvelope(rid)

	// Seed the operator-tombstoned tracker.
	dead := NewTracker(env, time.Now())
	dead.IsDeleted = true
	deadVal, err := dead.Marshal()
	if err != nil {
		t.Fatalf("marshal tombstoned tracker: %v", err)
	}
	rev, err := conn.KVPut(ctx, testCoreBucket, dead.Key, deadVal)
	if err != nil {
		t.Fatalf("seed tombstoned tracker: %v", err)
	}

	// Without the supersede revision the create-only write must conflict —
	// the guard this test locks in.
	res := ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: "vtx.identity." + testNanoID1,
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "B"}},
	}}}
	if _, err := committer.Commit(ctx, env, res, NewTracker(env, time.Now()), nil); err == nil {
		t.Fatalf("create-only tracker write over a live tombstoned value should conflict")
	}

	// With SupersedesRevision (what step 2 threads through) it commits.
	fresh := NewTracker(env, time.Now())
	fresh.SupersedesRevision = &rev
	res2 := ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: "vtx.identity." + testNanoID1 + ".profile",
		Document: map[string]interface{}{"class": "identity", "data": map[string]any{"name": "C"}},
	}}}
	if _, err := committer.Commit(ctx, env, res2, fresh, nil); err != nil {
		t.Fatalf("supersede-conditioned tracker write must commit, got: %v", err)
	}

	// The live tracker replaced the tombstoned one.
	entry, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(rid))
	if err != nil {
		t.Fatalf("tracker read-back: %v", err)
	}
	tr, err := ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	if tr.IsDeleted {
		t.Fatalf("tracker still tombstoned after superseding commit")
	}
}

// CheckDedup surfaces the tombstoned tracker's revision so the commit path
// can thread it to the step-8 write (DedupResult.TombstonedRevision).
func TestCheckDedup_TombstonedTrackerCarriesRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	rid := testNanoID1
	env := newTestEnvelope(rid)
	dead := NewTracker(env, time.Now())
	dead.IsDeleted = true
	deadVal, err := dead.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rev, err := conn.KVPut(ctx, testCoreBucket, dead.Key, deadVal)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := CheckDedup(ctx, conn, testCoreBucket, rid)
	if err != nil {
		t.Fatalf("CheckDedup: %v", err)
	}
	if got.Outcome != DedupNotFound {
		t.Fatalf("Outcome = %v, want DedupNotFound (§4.4 retry signal)", got.Outcome)
	}
	if got.TombstonedRevision == nil || *got.TombstonedRevision != rev {
		t.Fatalf("TombstonedRevision = %v, want %d", got.TombstonedRevision, rev)
	}
}
