package pipeline

// Adjacency-watch (ADR-16) fail-safe edge-arm coverage: handleAdjUpdate's
// bad-key / not-found / tombstone arms (pipeline.go:1189) and the arms
// handleAdjNode was extracted to hold — parse-fail, edge, unmarshal-fail,
// evaluate-error, guarded-skip, write-error+continue (pipeline.go:1219).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// adjWriteSpyAdapter records every Upsert/Delete call (and can fail on
// selected calls) without touching any real store — the fake-adapter shape
// rebuild_force_truncate_internal_test.go's guardedTruncAdapter establishes
// for this package.
type adjWriteSpyAdapter struct {
	guarded bool
	// failUpsertCalls, when non-empty, is consumed in order: the n-th Upsert
	// call fails with the corresponding error (nil = succeed). Delete always
	// succeeds — no arm here needs a failing Delete.
	failUpsertCalls []error
	upsertCalls     int
	writes          []map[string]any // keys of every Upsert/Delete actually applied
}

func (a *adjWriteSpyAdapter) Upsert(_ context.Context, keys map[string]any, _ map[string]any, _ uint64) error {
	idx := a.upsertCalls
	a.upsertCalls++
	if idx < len(a.failUpsertCalls) && a.failUpsertCalls[idx] != nil {
		return a.failUpsertCalls[idx]
	}
	a.writes = append(a.writes, keys)
	return nil
}
func (a *adjWriteSpyAdapter) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	a.writes = append(a.writes, keys)
	return nil
}
func (a *adjWriteSpyAdapter) Probe(context.Context) error { return nil }
func (a *adjWriteSpyAdapter) Close() error                { return nil }
func (a *adjWriteSpyAdapter) Guarded() bool               { return a.guarded }

// singleRowSpec matches one vertex by key and returns one row per anchor —
// enough for handleAdjNode's evaluate/guarded/write arms to produce a
// non-vacuous result set without needing adjacency traversal.
const singleRowSpec = `
MATCH (n:widget {key: $actorKey})
RETURN n.key AS key, n.data.name AS name
`

// twoRowSpec is singleRowSpec's sibling that yields TWO independent rows for
// the same anchor — a required (non-OPTIONAL) MATCH fans out one row per
// matching child, so seeding two children yields two rows — used by the
// write-error+continue arm so "continue" is distinguishable from "stop after
// the first".
const twoRowSpec = `
MATCH (n:widget {key: $actorKey})
MATCH (n)<-[:ownedBy]-(child:widgetchild)
RETURN n.key AS key, child.key AS childKey
`

// compileAdjRule parses spec against the full engine and returns the engine +
// compiled rule, mirroring pipeline_test.go's compileFullRule for this
// internal-test file (that helper lives in the external _test package and
// isn't reachable here).
func compileAdjRule(t *testing.T, spec string) (*full.Engine, ruleengine.CompiledRule) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	return eng, cr
}

// widgetBody builds a Contract #1 vertex body for the widget test type.
func widgetBody(key string, data map[string]any) []byte {
	body := map[string]any{
		"key": key, "class": "widget", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": data,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err) // test fixture construction; a marshal failure here is a test bug
	}
	return raw
}

// ── Arm 1: bad key (no "adj." prefix) ──────────────────────────────────────

// TestHandleAdjUpdate_BadKey_NoReadNoWrite pins arm 1 (pipeline.go:1192): a
// key with no "adj." prefix returns immediately — no Core KV read, no write.
// A nil coreKV proves no read is attempted (a real read would nil-panic).
func TestHandleAdjUpdate_BadKey_NoReadNoWrite(t *testing.T) {
	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-badkey", adpt: ad}
	p.handleAdjUpdate(context.Background(), "notadj.vtx.widget.X")
	require.Empty(t, ad.writes, "a malformed adjacency key must never reach the write path")
}

// ── Arms 4-9: handleAdjNode, pure-unit (no Core KV Get needed) ─────────────

// TestHandleAdjNode_ParseFail pins arm 4 (pipeline.go:1220 pre-extraction —
// now the top of handleAdjNode): an un-parseable vertex key returns with no
// write.
func TestHandleAdjNode_ParseFail(t *testing.T) {
	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-parsefail", adpt: ad}
	p.handleAdjNode(context.Background(), "garbage-not-a-vertex-key", []byte(`{}`))
	require.Empty(t, ad.writes)
}

// TestHandleAdjNode_EdgeEvent pins arm 5: a body carrying a non-empty nodeId
// field is an edge event (the bootstrapper's job, not a pipeline's) and is
// skipped with no write.
func TestHandleAdjNode_EdgeEvent(t *testing.T) {
	const nodeKey = "vtx.widget.EdgeAAAAAAAAAAAAAAAA"
	_, _, ok := substrate.ParseVertexKey(nodeKey)
	require.True(t, ok, "test fixture must be a well-formed vertex key — otherwise this exercises arm 4, not arm 5")

	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-edge", adpt: ad}
	body := []byte(`{"nodeId":"n1","otherNodeId":"n2"}`)
	p.handleAdjNode(context.Background(), nodeKey, body)
	require.Empty(t, ad.writes, "an edge event must never reach the evaluate/write path")
}

// TestHandleAdjNode_UnmarshalFail pins arm 6: a corrupt (non-JSON) body warns
// and returns — no write, no panic.
func TestHandleAdjNode_UnmarshalFail(t *testing.T) {
	const nodeKey = "vtx.widget.BadBodyAAAAAAAAAAAAA"
	_, _, ok := substrate.ParseVertexKey(nodeKey)
	require.True(t, ok, "test fixture must be a well-formed vertex key — otherwise this exercises arm 4, not arm 6")

	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-unmarshal", adpt: ad}
	require.NotPanics(t, func() {
		p.handleAdjNode(context.Background(), nodeKey, []byte("not json"))
	})
	require.Empty(t, ad.writes)
}

// TestHandleAdjNode_EvaluateError pins arm 7: a forced evaluate error
// (engineKind Full, nil fullCR — evaluateForEntryRaw's own explicit
// "p.fullEngine == nil || p.fullCR == nil" guard fires directly on this call
// path, since handleAdjNode calls evaluateForEntry) warns and returns — no
// write, no panic (including with a nil reporter, per the output_collision
// nil-reporter precedent).
func TestHandleAdjNode_EvaluateError(t *testing.T) {
	const nodeKey = "vtx.widget.EvaErrAAAAAAAAAAAAAA"
	_, _, ok := substrate.ParseVertexKey(nodeKey)
	require.True(t, ok, "test fixture must be a well-formed vertex key — otherwise this exercises arm 4, not arm 7")

	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{
		ruleID:     "rule-adj-evalerr",
		adpt:       ad,
		engineKind: ruleengine.EngineFull,
		fullEngine: nil,
		fullCR:     nil,
		reporter:   nil,
	}
	require.NotPanics(t, func() {
		p.handleAdjNode(context.Background(), nodeKey, widgetBody(nodeKey, map[string]any{"name": "x"}))
	})
	require.Empty(t, ad.writes)
}

// TestHandleAdjNode_GuardedSkip_NonVacuous pins arm 8 (pipeline.go:998): a
// guarded adapter's watermark may only advance on a stream-sequenced write,
// so the adjacency-watch path (seq 0, never stream-sequenced) must skip the
// write entirely — even when the re-evaluation DOES produce at least one
// result. Seeding a lens that yields zero rows would make the "no write"
// assertion vacuously true regardless of the guard; this test seeds ONE
// matching row so the guard is the only reason nothing is written.
func TestHandleAdjNode_GuardedSkip_NonVacuous(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()
	eng, cr := compileAdjRule(t, singleRowSpec)

	const widgetID = "AdjGuardWidgetAAAAAA"
	nodeKey := "vtx.widget." + widgetID
	writeCollisionVertex(t, coreKV, nodeKey, "widget", map[string]any{"name": "gadget"})

	ad := &adjWriteSpyAdapter{guarded: true}
	p := &Pipeline{
		ruleID:     "rule-adj-guarded",
		coreKV:     coreKV,
		adjKV:      adjKV,
		adpt:       ad,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}

	// Confirm the seeded lens is non-vacuous BEFORE asserting the guard: the
	// same evaluate path handleAdjNode drives must actually produce a row.
	entry := ruleengine.NodeEntry{CoreKVKey: nodeKey, NodeLabel: "widget",
		Properties: map[string]any{"lastModifiedAt": "2026-07-02T10:00:00Z", "data": map[string]any{"name": "gadget"}}}
	results, _, err := p.evaluateForEntry(ctx, entry)
	require.NoError(t, err)
	require.NotEmpty(t, results, "the guarded-skip assertion is meaningless unless the lens actually matches")

	p.handleAdjNode(ctx, nodeKey, widgetBody(nodeKey, map[string]any{"name": "gadget"}))
	require.Empty(t, ad.writes,
		"a guarded target must never be written by the adjacency-watch path, even when results are non-empty")
}

// TestHandleAdjNode_WriteError_RecordsAndContinues pins arm 9
// (pipeline.go:1012): a write error on one result must not abort the loop —
// remaining results are independent and adapter writes are idempotent — and
// the error is recorded to Health KV (not a pause: adj-watch events carry no
// stream sequence and are not JetStream-replayable). Two results are seeded
// so "continue" (attempt the second) is distinguishable from "return after
// the first error".
func TestHandleAdjNode_WriteError_RecordsAndContinues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()
	eng, cr := compileAdjRule(t, twoRowSpec)

	const widgetID = "AdjWriteErrWidgetAAA"
	const child1ID = "AdjWriteErrKid1AAAAA"
	const child2ID = "AdjWriteErrKid2AAAAA"
	nodeKey := "vtx.widget." + widgetID
	writeCollisionVertex(t, coreKV, nodeKey, "widget", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.widgetchild."+child1ID, "widgetchild", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.widgetchild."+child2ID, "widgetchild", map[string]any{})
	buildCollisionEdge(t, adjKV, "ownedBy", "widgetchild", child1ID, "widget", widgetID)
	buildCollisionEdge(t, adjKV, "ownedBy", "widgetchild", child2ID, "widget", widgetID)

	reporter := health.New(healthKV, "rule-adj-writeerr")
	ad := &adjWriteSpyAdapter{failUpsertCalls: []error{errors.New("boom: first write fails")}}
	p := &Pipeline{
		ruleID:     "rule-adj-writeerr",
		coreKV:     coreKV,
		adjKV:      adjKV,
		adpt:       ad,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
		reporter:   reporter,
	}

	entry := ruleengine.NodeEntry{CoreKVKey: nodeKey, NodeLabel: "widget",
		Properties: map[string]any{"lastModifiedAt": "2026-07-02T10:00:00Z"}}
	preResults, _, err := p.evaluateForEntry(ctx, entry)
	require.NoError(t, err)
	require.Len(t, preResults, 2, "the write-error+continue assertion needs two independent results")

	p.handleAdjNode(ctx, nodeKey, widgetBody(nodeKey, map[string]any{}))

	require.Equal(t, 2, ad.upsertCalls, "the second result must still be attempted after the first write fails")
	require.Len(t, ad.writes, 1, "exactly one of the two writes succeeded")

	entryStatus, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(1), entryStatus.ErrorCount, "the write failure must be recorded to Health KV")
	require.NotNil(t, entryStatus.LastError)
	require.Contains(t, *entryStatus.LastError, "boom: first write fails")
}

// ── Arms 2-3: handleAdjUpdate's Get-driven arms, NATS-backed ───────────────

// TestHandleAdjUpdate_NotFound_Skips pins arm 2 (pipeline.go:1201): a fresh
// "adj.<absent>" key over an empty CORE bucket skips — the node hasn't
// arrived yet; the stream consumer will project it when it does.
func TestHandleAdjUpdate_NotFound_Skips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-notfound", coreKV: coreKV, adjKV: adjKV, adpt: ad}

	p.handleAdjUpdate(context.Background(), "adj.vtx.widget.AbsentAAAAAAAAAAAAAA")
	require.Empty(t, ad.writes)
}

// TestHandleAdjUpdate_Tombstone_Skips pins arm 3 (pipeline.go:1215): seeding
// the node then writing Contract #1's empty-body tombstone (a live KV entry
// with a zero-length value — NOT a hard KVDelete, which would instead make
// the subsequent Get return ErrKeyNotFound and take arm 2) must skip —
// deletion is the stream consumer's job, not the adjacency watch's.
func TestHandleAdjUpdate_Tombstone_Skips(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const widgetID = "AdjTombstoneWidgetAA"
	nodeKey := "vtx.widget." + widgetID
	writeCollisionVertex(t, coreKV, nodeKey, "widget", map[string]any{"name": "gadget"})
	_, err := coreKV.Put(ctx, nodeKey, nil)
	require.NoError(t, err)

	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{ruleID: "rule-adj-tombstone", coreKV: coreKV, adjKV: adjKV, adpt: ad}

	p.handleAdjUpdate(ctx, "adj."+nodeKey)
	require.Empty(t, ad.writes)
}

// TestHandleAdjUpdate_LiveNode_DrivesHandleAdjNode proves the full seam end
// to end: a live (non-tombstoned) node drives handleAdjUpdate's prefix-strip
// and Get all the way into handleAdjNode's parse-evaluate-write body — the
// extraction's risk mitigation (design §7): the NATS-backed arm tests must
// still exercise handleAdjUpdate as a whole, not just the extracted half.
func TestHandleAdjUpdate_LiveNode_DrivesHandleAdjNode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()
	eng, cr := compileAdjRule(t, singleRowSpec)

	const widgetID = "AdjLiveNodeWidgetAAA"
	nodeKey := "vtx.widget." + widgetID
	writeCollisionVertex(t, coreKV, nodeKey, "widget", map[string]any{"name": "gadget"})

	ad := &adjWriteSpyAdapter{}
	p := &Pipeline{
		ruleID:     "rule-adj-livenode",
		coreKV:     coreKV,
		adjKV:      adjKV,
		adpt:       ad,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}

	p.handleAdjUpdate(ctx, "adj."+nodeKey)
	require.Len(t, ad.writes, 1, "a live node must drive the full seam through to a write")
	require.Equal(t, map[string]any{"key": nodeKey}, ad.writes[0])
}
