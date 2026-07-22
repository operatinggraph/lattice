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

// Tests for Contract #2 §2.5 `contextHint.optionalReads` — the absence-tolerant
// declared read (class (d), read-before-create / dedup):
//   - present  → hydrated exactly like a `reads` key (cache hit at step 4);
//   - absent   → recorded known-absent (NEVER HydrationMiss); kv.Read serves
//     None from the step-4 snapshot with no live GET;
//   - a `reads` key keeps its fail-closed semantics, even when duplicated in
//     optionalReads;
//   - a create conditioned on the step-4-observed absence that loses the race
//     is absorbed by the commit retry loop (re-hydrate → present → re-branch).

func TestHydrate_OptionalReads_PresentIsHydrated(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	key := "vtx.identity." + testNanoID2
	doc := []byte(`{"class":"identity","isDeleted":false,"data":{"name":"Andrew"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{OptionalReads: []string{key}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sc := state.Context
	if _, ok := sc.Hydrated[key]; !ok {
		t.Fatalf("present optionalReads key not hydrated: %+v", sc.Hydrated)
	}
	if sc.Hydrated[key].Revision == 0 {
		t.Fatalf("hydrated optionalReads key carries no revision (OCC handle lost)")
	}
	if _, absent := sc.KnownAbsent[key]; absent {
		t.Fatalf("present key must not be recorded known-absent")
	}
}

func TestHydrate_OptionalReads_AbsentIsKnownAbsent(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())

	missing := "vtx.task.NeverCreatedNeverSeen00"
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{OptionalReads: []string{missing}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("absent optionalReads key must not fault, got: %v", err)
	}
	sc := state.Context
	if _, ok := sc.Hydrated[missing]; ok {
		t.Fatalf("absent key must not appear in Hydrated")
	}
	if _, absent := sc.KnownAbsent[missing]; !absent {
		t.Fatalf("absent optionalReads key not recorded known-absent: %+v", sc.KnownAbsent)
	}
}

// TestHydrate_OptionalReads_ReadsStayFailClosed — the §2.5 authoring rule is
// structural: a key in `reads` faults on absence even if a (redundant)
// optionalReads entry also names it. optionalReads can never soften `reads`.
func TestHydrate_OptionalReads_ReadsStayFailClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := NewHydrator(conn, testCoreBucket, testLogger())
	_ = conn

	missing := "vtx.identity.MissingMissingMissing"
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{
		Reads:         []string{missing},
		OptionalReads: []string{missing},
	}

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) || hErr.Code != "HydrationMiss" {
		t.Fatalf("reads key duplicated in optionalReads must still HydrationMiss, got: %v", err)
	}
}

// TestKVRead_KnownAbsent_NoneWithNoLiveGET — a known-absent key is served None
// from the step-4 snapshot: the lazy KVReader is never consulted (the fake
// records its calls), so the read is replay-stable and costs no round-trip.
func TestKVRead_KnownAbsent_NoneWithNoLiveGET(t *testing.T) {
	key := "vtx.task.DeclaredButAbsent0000"
	reader := &fakeKVReader{docs: map[string]*VertexDoc{
		// The reader WOULD return a doc — proving that a hit here would be
		// observable. The known-absent snapshot must win instead.
		key: {Key: key, Class: "task"},
	}}
	sc := ScriptContext{
		KnownAbsent: map[string]struct{}{key: {}},
		KVReader:    reader,
	}
	res, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+key+`")
    cls = "none" if v == None else "present"
    return {"mutations": [], "events": [{"class": cls, "data": {}}]}
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "none" {
		t.Fatalf("known-absent key must read as None, got %+v", res.Events)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("known-absent read must not touch the live reader, calls: %v", reader.calls)
	}
}

// TestCommitPipeline_AbsentConditionedCreateRetries — the (A′) attribution: a
// create off a known-absent optionalReads key that conflicts while the key has
// materialized is a benign declared-dedup race → absorbed by the in-process
// retry (unlike an undeclared create-once collision, which
// TestCommitPipeline_CreateOnceCollisionSurfacesWithoutRetry pins as
// surfaced-not-retried).
func TestCommitPipeline_AbsentConditionedCreateRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	key := "vtx.task." + testNanoID2
	state := HydratedState{Context: ScriptContext{
		Hydrated:    map[string]VertexDoc{},
		KnownAbsent: map[string]struct{}{key: {}},
	}}
	result := ScriptResult{Mutations: []MutationOp{{
		Op:       "create",
		Key:      key,
		Document: map[string]interface{}{"class": "task", "data": map[string]any{"status": "open"}},
	}}}
	// Conflict once; the fake writes bumpKey on failure, materializing the
	// concurrent winner exactly as a lost CreateOnly race would observe it.
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
}

// raceCommitter injects the same-commit race the §3.1 invariant is about: on
// its first Commit call it writes `key` to Core KV (the concurrent winner
// landing between step 4 and step 8), then delegates to the real Committer —
// whose CreateOnly condition on that key now fails.
type raceCommitter struct {
	inner Committer
	conn  *substrate.Conn
	key   string
	value []byte
	calls atomic.Uint64
}

func (r *raceCommitter) Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker) (CommitAck, error) {
	if r.calls.Add(1) == 1 {
		if _, err := r.conn.KVPut(ctx, testCoreBucket, r.key, r.value); err != nil {
			return CommitAck{}, fmt.Errorf("raceCommitter: inject winner: %w", err)
		}
	}
	return r.inner.Commit(ctx, env, result, tracker)
}

// TestOptionalReads_SameCommitRace_E2E is the design's load-bearing AC (§3.1
// party #8), end-to-end over the REAL Hydrator, Starlark executor, and
// Committer: a read-before-create script declares its dedup key in
// optionalReads; a concurrent create wins between step 4 and step 8; the
// CreateOnly backstop rejects the batch; the retry re-hydrates (key now
// present, served from the snapshot), the script re-branches no-op, and the
// operation commits cleanly WITHOUT overwriting the winner.
func TestOptionalReads_SameCommitRace_E2E(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := occConn(t)
	provisionHarness(t, ctx, conn)

	dedupKey := "vtx.task." + testNanoID2

	// Seed the class DDL + the read-before-create script (shadow-key path).
	script := `
def execute(state, op):
    key = "` + dedupKey + `"
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        return {"mutations": [], "events": []}
    return {"mutations": [{"op": "create", "key": key,
                           "document": {"class": "task", "isDeleted": False,
                                        "data": {"origin": "loser"}}}],
            "events": []}
`
	ddlDoc := `{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"task","permittedCommands":["CreateIdentity"]}}`
	scriptDoc, err := json.Marshal(map[string]any{
		"class": "meta.script", "isDeleted": false,
		"data": map[string]any{"source": script},
	})
	if err != nil {
		t.Fatalf("marshal script doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.task", []byte(ddlDoc)); err != nil {
		t.Fatalf("seed DDL: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.task.script", scriptDoc); err != nil {
		t.Fatalf("seed script: %v", err)
	}

	winnerDoc := []byte(`{"key":"` + dedupKey + `","class":"task","isDeleted":false,"data":{"origin":"winner"}}`)
	committer := &raceCommitter{
		inner: NewCommitter(conn, testCoreBucket, nil, testLogger(), time.Now),
		conn:  conn,
		key:   dedupKey,
		value: winnerDoc,
	}
	metrics := &Metrics{}
	cp := newOCCPipeline(t, conn,
		NewHydrator(conn, testCoreBucket, testLogger()),
		NewExecutor(NewStarlarkRunner(0, 0), testLogger()),
		committer, metrics)

	env := newTestEnvelope(testNanoID1)
	env.Class = "task"
	env.ContextHint = &ContextHint{OptionalReads: []string{dedupKey}}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	outcome, decision := cp.dispatch(ctx, substrate.Message{
		Subject: "ops.default", Body: b, Header: func(string) string { return "" },
	})
	if outcome != OutcomeAccepted || decision != substrate.Ack {
		t.Fatalf("outcome=%v decision=%v, want accepted/Ack (race absorbed)", outcome, decision)
	}
	if got := committer.calls.Load(); got != 2 {
		t.Fatalf("committer calls = %d, want 2 (lost race + no-op recommit)", got)
	}
	if metrics.CommitRetries.Load() != 1 {
		t.Fatalf("CommitRetries = %d, want 1", metrics.CommitRetries.Load())
	}

	// The winner's document survives byte-for-byte: the retry re-branched
	// no-op instead of overwriting.
	entry, err := conn.KVGet(ctx, testCoreBucket, dedupKey)
	if err != nil {
		t.Fatalf("read %s: %v", dedupKey, err)
	}
	var got struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &got); err != nil {
		t.Fatalf("parse committed doc: %v", err)
	}
	if got.Data["origin"] != "winner" {
		t.Fatalf("winner's doc was overwritten: %v", got.Data)
	}
}
