package full

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// prefetchOff returns a copy of e whose evaluations point-read every node and
// aspect one key at a time. It is the un-batched path these tests measure the
// batched one against — the same rule, the same graph, one round trip per key.
func prefetchOff(e *Engine) *Engine {
	c := *e
	c.prefetchDisabled = true
	return &c
}

// runCounted evaluates body through the executor ExecuteWithStats builds, and
// hands the executor back so a test can read how many single-key Core KV reads
// the evaluation issued and what it staged.
func runCounted(
	t *testing.T,
	e *Engine,
	body string,
	params map[string]any,
	adjKV, coreKV *substrate.KV,
) (*executor, []ruleengine.ProjectionResult, ruleengine.EvalFootprint) {
	t.Helper()
	cr, err := e.Parse(body)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok, "expected *CompiledRule, got %T", cr)
	ex := e.newExecutor(context.Background(), compiled,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	results, footprint, err := ex.run(compiled)
	require.NoError(t, err)
	return ex, results, footprint
}

// seedHub writes a hub identity and n service vertices, each carrying one
// `providedTo` edge to the hub — the wide-actor shape a personal lens walks.
// Returns the hub's vertex key and the neighbours' keys in seeding order.
func seedHub(t *testing.T, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, n int) (string, []string) {
	t.Helper()
	hub := putVertex(t, reg, coreKV, "hub", "identity", nil)
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("inst%03d", i)
		keys = append(keys, putVertex(t, reg, coreKV, name, "service", map[string]any{"n": int64(i)}))
		putEdge(t, reg, adjKV, "providedTo", name, "hub")
	}
	return hub, keys
}

// anchorRows renders results as (anchor → whole value map) for comparison
// between two evaluations of one rule.
func anchorRows(results []ruleengine.ProjectionResult) map[string]map[string]any {
	rows := make(map[string]map[string]any, len(results))
	for _, r := range results {
		rows[fmt.Sprint(r.Values["anchor"])] = r.Values
	}
	return rows
}

// TestTraverseRel_PrefetchesFrontier_ZeroPointReads pins the hop's read
// discipline: a frontier of N admitted neighbours is read in ONE batch before
// any of it is bound, so binding those neighbours costs no single-key reads at
// all — and the rows are the ones the point-read path produces.
//
// The count is the proof, not a proxy for it: the same rule with batching off
// pays exactly one point read per neighbour, so the difference between the two
// runs IS the frontier.
func TestTraverseRel_PrefetchesFrontier_ZeroPointReads(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const neighbours = 50
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, neighbourKeys := seedHub(t, reg, adjKV, coreKV, neighbours)

	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service) RETURN inst.key AS anchor`
	params := map[string]any{"k": hub}

	on, onRows, onFootprint := runCounted(t, New(), body, params, adjKV, coreKV)
	off, offRows, _ := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Len(t, onRows, neighbours)
	require.Equal(t, anchorRows(offRows), anchorRows(onRows),
		"batching a hop's frontier must not change the rows it binds")

	require.Equal(t, neighbours, off.pointReads-on.pointReads,
		"every neighbour the point-read path fetches one at a time must come from the batch instead")
	require.Less(t, on.pointReads, neighbours,
		"a batched hop must not point-read its frontier at all (only the anchor is fetched by key)")
	require.Equal(t, 1, on.batchReads,
		"the frontier is ONE request, not one per key")
	require.Zero(t, off.batchReads)

	// Each neighbour is nonetheless part of the evaluation's read surface: the
	// batch stages, and binding the node promotes it into the memo the
	// footprint is built from.
	for _, key := range neighbourKeys {
		require.Contains(t, onFootprint.NodeRevisions, key)
		require.NotZero(t, onFootprint.NodeRevisions[key])
	}
}

// TestProjection_AspectPrefetch pins the projection twin: a RETURN that
// dereferences an aspect off every bound node reads those aspect bodies in one
// batch, an ABSENT aspect included — it resolves to null exactly as the
// point-read path resolves it, and its absence is memoized, so a key created
// mid-evaluation cannot change the answer half way down the rows.
func TestProjection_AspectPrefetch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const neighbours = 20
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, neighbourKeys := seedHub(t, reg, adjKV, coreKV, neighbours)
	// Every instance but the last carries the aspect the projection reads.
	for i := 0; i < neighbours-1; i++ {
		putAspect(t, reg, coreKV, fmt.Sprintf("inst%03d", i), "presentation",
			map[string]any{"name": fmt.Sprintf("service %d", i)})
	}
	absentAspect := neighbourKeys[neighbours-1] + ".presentation"

	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
	         RETURN inst.key AS anchor, inst.presentation.data.name AS name`
	params := map[string]any{"k": hub}

	on, onRows, onFootprint := runCounted(t, New(), body, params, adjKV, coreKV)
	off, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Equal(t, anchorRows(offRows), anchorRows(onRows),
		"batching the aspect reads must not change a single projected value")

	// The point-read path pays one read per neighbour AND one per aspect
	// dereference; the batched path pays neither.
	require.Equal(t, 2*neighbours, off.pointReads-on.pointReads)
	require.Less(t, on.pointReads, neighbours)

	byAnchor := anchorRows(onRows)
	require.Equal(t, "service 0", byAnchor[neighbourKeys[0]]["name"])
	require.Nil(t, byAnchor[neighbourKeys[neighbours-1]]["name"],
		"an absent aspect projects null")

	// The absent aspect key is memoized as absent — revision 0 in the
	// footprint, which is what a point read of a missing key records and what
	// the pipeline's validation compares against.
	require.Contains(t, onFootprint.NodeRevisions, absentAspect)
	require.Zero(t, onFootprint.NodeRevisions[absentAspect])
	require.Equal(t, offFootprint.NodeRevisions[absentAspect], onFootprint.NodeRevisions[absentAspect])
}

// TestPrefetch_FootprintIdentical is the read-surface pin: one rule evaluated
// with batching on and off yields byte-identical read-surface footprints, so
// the certificate the pipeline re-checks after an evaluation cannot depend on
// how the reads were issued. It covers the three shapes whose footprint entries
// are easiest to get wrong —
//
//   - an ABSENT aspect (memoized, revision 0, not merely omitted — a footprint
//     that dropped absences would re-execute forever against a key that is
//     supposed to stay missing);
//   - a TOMBSTONED neighbour (isDeleted, excluded from the rows by both paths
//     while still counting as read);
//   - an aspect a batch reads that the evaluation never dereferences (the ELSE
//     of a CASE whose WHEN always holds), which must stay OUT of the footprint.
//
// The last is why a batched entry is staged and promoted on use rather than
// memoized outright: a prefetch reads what a clause is ABOUT to want, and a
// clause with a branch does not want all of it.
func TestPrefetch_FootprintIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const neighbours = 12
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, neighbourKeys := seedHub(t, reg, adjKV, coreKV, neighbours)
	for i := 0; i < neighbours-2; i++ {
		putAspect(t, reg, coreKV, fmt.Sprintf("inst%03d", i), "presentation",
			map[string]any{"name": fmt.Sprintf("service %d", i)})
	}
	// One neighbour is soft-deleted: the walk crosses its edge and reads it,
	// and both paths must drop it from the rows.
	tombstoned := putVertex(t, reg, coreKV, "inst000", "service",
		map[string]any{"n": int64(0), "isDeleted": true})
	require.Equal(t, neighbourKeys[0], tombstoned)

	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
	         RETURN inst.key AS anchor,
	                inst.presentation.data.name AS name,
	                (CASE WHEN inst.key <> null THEN "bound" ELSE inst.audit.data.reason END) AS state`
	params := map[string]any{"k": hub}

	on, onRows, onFootprint := runCounted(t, New(), body, params, adjKV, coreKV)
	_, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Len(t, onRows, neighbours-1, "the tombstoned neighbour binds on neither path")
	require.Equal(t, anchorRows(offRows), anchorRows(onRows))
	require.True(t, reflect.DeepEqual(onFootprint, offFootprint),
		"batched reads must observe the same read surface, key for key and revision for revision:\nbatched=%#v\npoint  =%#v",
		onFootprint, offFootprint)
	require.NotContains(t, onFootprint.NodeRevisions, tombstoned+".presentation",
		"the tombstoned neighbour never binds, so its aspect is never dereferenced")

	// The CASE's ELSE is unreachable for every row, so no evaluation ever
	// dereferences inst.audit — yet the batch DID read it, and staging is what
	// keeps that read out of the certificate.
	staged := 0
	for _, key := range neighbourKeys {
		auditKey := key + ".audit"
		require.NotContains(t, onFootprint.NodeRevisions, auditKey,
			"a key the evaluation never dereferenced must not enter the read surface")
		if _, ok := on.prefetched[auditKey]; ok {
			staged++
		}
	}
	require.NotZero(t, staged, "the batch is expected to have read the CASE's untaken branch")
}

// TestPrefetchConstants_MatchTheServerCaps pins the request sizes absolutely.
// They are not tuning knobs: prefetchChunk is the substrate multi-get's
// atomic fast-path cap on MATCHED SUBJECTS, and a request over it leaves that
// path for a consumer drain. Shrinking it silently turns a batch back into
// one-request-per-handful while every read-count assertion in this file — which
// counts the point reads a batch REMOVED — stays satisfied, so the constant is
// pinned here and the request counts are asserted alongside the read counts.
func TestPrefetchConstants_MatchTheServerCaps(t *testing.T) {
	require.Equal(t, 1024, prefetchChunk,
		"the multi-get's atomic fast path admits 1,024 matched subjects")
	require.Equal(t, 16, prefetchChunkFloor,
		"the floor bounds the split-on-failure descent; below it a failure is not about size")
	require.Less(t, prefetchChunkFloor, prefetchChunk)
}

// TestPrefetchNodes_OverTheChunkCap runs a batch WIDER than the fast-path cap
// against a real bucket: every key is fetched across several chunks, every one
// of them is served to fetchNode with no single-key read, and an absent key in
// the same request stages the absence a point read of it would have recorded.
func TestPrefetchNodes_OverTheChunkCap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const present = prefetchChunk + 76 // two chunks: a full one and a remainder
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	keys := make([]string, 0, present)
	for i := 0; i < present; i++ {
		keys = append(keys, putVertex(t, reg, coreKV, fmt.Sprintf("wide%04d", i), "service",
			map[string]any{"n": int64(i)}))
	}
	absent := []string{
		"vtx.service." + c1NanoID("neverWritten1"),
		"vtx.service." + c1NanoID("neverWritten2"),
	}
	requested := append(append([]string{}, keys...), absent...)

	ex := newTestExecutor(nil, coreKV)
	require.NoError(t, ex.prefetchNodes(requested))
	require.Zero(t, ex.pointReads, "a batched read issues no single-key reads")
	require.Equal(t, 2, ex.batchReads,
		"a full fast-path chunk and a remainder is TWO requests, not one per key")

	for i, key := range keys {
		ref, err := ex.fetchNode(key)
		require.NoError(t, err)
		require.NotNil(t, ref, "key %d missing from the batch", i)
		require.Equal(t, key, ref.props["key"])
		require.Equal(t, float64(i), ref.props["n"])
		require.NotZero(t, ref.revision)
	}
	for _, key := range absent {
		ref, err := ex.fetchNode(key)
		require.NoError(t, err)
		require.Nil(t, ref)
	}
	require.Zero(t, ex.pointReads,
		"every requested key — present and absent — must be served from the batch")

	footprint := ex.footprint()
	require.Len(t, footprint.NodeRevisions, len(requested))
	for _, key := range absent {
		require.Zero(t, footprint.NodeRevisions[key])
	}
}

// TestPrefetchNodes_LeavesTheDegenerateCasesAlone pins what the batch declines
// to do: a read-free executor never reads, an already-memoized key is never
// re-requested, and a request that reduces to a single key is left to the point
// read — one key is one round trip whichever primitive issues it.
func TestPrefetchNodes_LeavesTheDegenerateCasesAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	first := putVertex(t, reg, coreKV, "one", "service", nil)
	second := putVertex(t, reg, coreKV, "two", "service", nil)

	// The read-free key-resolution executor (nil coreKV, nil memo).
	readFree := &executor{ctx: context.Background()}
	require.NoError(t, readFree.prefetchNodes([]string{first, second}))
	require.Empty(t, readFree.prefetched)

	ex := newTestExecutor(nil, coreKV)
	require.NoError(t, ex.prefetchNodes([]string{first}))
	require.Empty(t, ex.prefetched, "a single key is left to the point read")

	// A duplicated key reduces to one and is likewise left alone.
	require.NoError(t, ex.prefetchNodes([]string{first, first, first}))
	require.Empty(t, ex.prefetched)

	memoized, err := ex.fetchNode(first)
	require.NoError(t, err)
	require.NotNil(t, memoized)
	require.Equal(t, 1, ex.pointReads)

	// One key is memoized and one is not, so the batch has a single key left
	// to ask for and declines again.
	require.NoError(t, ex.prefetchNodes([]string{first, second}))
	require.Empty(t, ex.prefetched)
	require.Equal(t, 1, ex.pointReads)
}

// runCountedCtx is runCounted evaluating under a caller-supplied context, for a
// test that installs an adjacency read observer.
func runCountedCtx(
	t *testing.T,
	ctx context.Context,
	e *Engine,
	body string,
	params map[string]any,
	adjKV, coreKV *substrate.KV,
) (*executor, []ruleengine.ProjectionResult, ruleengine.EvalFootprint) {
	t.Helper()
	cr, err := e.Parse(body)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok, "expected *CompiledRule, got %T", cr)
	ex := e.newExecutor(ctx, compiled,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	results, footprint, err := ex.run(compiled)
	require.NoError(t, err)
	return ex, results, footprint
}

// runExpectingError evaluates body and returns the error it failed with, so two
// paths' failures can be compared rather than merely both being non-nil.
func runExpectingError(
	t *testing.T,
	e *Engine,
	body string,
	params map[string]any,
	adjKV, coreKV *substrate.KV,
) error {
	t.Helper()
	cr, err := e.Parse(body)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok, "expected *CompiledRule, got %T", cr)
	ex := e.newExecutor(context.Background(), compiled,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	_, _, runErr := ex.run(compiled)
	require.Error(t, runErr, "the evaluation was expected to fail")
	return runErr
}

// seedInstancesWithTemplates writes the hub/instance shape seedHub does and
// gives each instance its own `instanceOf` template — the second hop that runs
// once per ALREADY-BOUND row, and so once per row's adjacency read.
func seedInstancesWithTemplates(
	t *testing.T, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, n int,
) (string, []string) {
	t.Helper()
	hub, instances := seedHub(t, reg, adjKV, coreKV, n)
	for i := 0; i < n; i++ {
		inst := fmt.Sprintf("inst%03d", i)
		tpl := fmt.Sprintf("tpl%03d", i)
		putVertex(t, reg, coreKV, tpl, "service", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "instanceOf", inst, tpl)
	}
	return hub, instances
}

// TestApplyMatch_PrefetchesBoundFrontier_ZeroAdjacencyReads pins the second
// hop's read discipline. An `OPTIONAL MATCH` hanging off a variable already
// bound to N rows is walked per row, so without a batch it costs N node-state
// reads; the batch reads all N sources in one request before the loop begins,
// and the rows are the ones the per-node path produces.
func TestApplyMatch_PrefetchesBoundFrontier_ZeroAdjacencyReads(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const instances = 50
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, _ := seedInstancesWithTemplates(t, reg, adjKV, coreKV, instances)

	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
	         OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
	         RETURN inst.key AS anchor, tpl.key AS templateKey`
	params := map[string]any{"k": hub}

	on, onRows, _ := runCounted(t, New(), body, params, adjKV, coreKV)
	off, offRows, _ := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Len(t, onRows, instances)
	require.Equal(t, anchorRows(offRows), anchorRows(onRows),
		"batching a stage's bound sources must not change the rows it produces")

	require.Equal(t, instances, off.adjReads-on.adjReads,
		"every bound source the per-node path reads one at a time must come from the batch instead")
	require.Equal(t, 1, on.adjReads,
		"only the anchor's own hop is read per-node; the whole second stage is one batch")
	require.Equal(t, 2, on.batchReads,
		"two requests for the whole evaluation — the anchor hop's Core KV frontier, "+
			"and the stage's adjacency sources — not one per node")
	require.Zero(t, off.batchReads)
}

// TestPatternComprehension_PrefetchesBoundSources is the same discipline for a
// pattern comprehension, whose pattern is walked once per row inside the
// projection rather than as a clause of its own.
func TestPatternComprehension_PrefetchesBoundSources(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const instances = 20
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, instanceKeys := seedInstancesWithTemplates(t, reg, adjKV, coreKV, instances)

	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
	         RETURN inst.key AS anchor, [(inst)-[:instanceOf]->(tpl:service) | tpl.key] AS templates`
	params := map[string]any{"k": hub}

	on, onRows, _ := runCounted(t, New(), body, params, adjKV, coreKV)
	off, offRows, _ := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Equal(t, anchorRows(offRows), anchorRows(onRows))
	require.Equal(t, instances, off.adjReads-on.adjReads)
	require.Equal(t, 1, on.adjReads)

	// The comprehension really did project each instance's template.
	byAnchor := anchorRows(onRows)
	templates, ok := byAnchor[instanceKeys[0]]["templates"].([]any)
	require.True(t, ok, "the comprehension projects a list")
	require.Len(t, templates, 1)
}

// seedFootprintShapes builds the three node shapes whose adjacency footprint
// entries are easiest for a batch to get wrong, and returns the hub key with
// the marked instance's NodeID.
//
//   - a TOMBSTONED instance, which never binds and so is never a source;
//   - an overflow-MARKED instance, whose edges live in Core KV's link keyspace
//     and which the batch must decline to answer for;
//   - `gadget` vertices with NO adjacency document at all, whose hop must
//     footprint an absence at fingerprint 0 rather than omit the node.
func seedFootprintShapes(
	t *testing.T, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, instances, gadgets int,
) (string, string) {
	t.Helper()
	hub, _ := seedInstancesWithTemplates(t, reg, adjKV, coreKV, instances)

	putVertex(t, reg, coreKV, "inst000", "service", map[string]any{"n": int64(0), "isDeleted": true})

	markedID := reg.idByName["inst001"]
	markNodeOverflowed(t, adjKV, markedID)
	putLink(t, reg, coreKV, "instanceOf", "inst001", "tpl001")

	for i := 0; i < gadgets; i++ {
		putVertex(t, reg, coreKV, fmt.Sprintf("gadget%03d", i), "gadget", map[string]any{"n": int64(i)})
	}
	return hub, markedID
}

// footprintShapesBody walks all three shapes in one evaluation: the instance
// stage (tombstoned + marked), and a gadget stage whose sources carry no
// adjacency document.
const footprintShapesBody = `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
MATCH (g:gadget)
OPTIONAL MATCH (g)-[:pairsWith]->(peer:service)
RETURN inst.key AS anchor, tpl.key AS templateKey, g.key AS gadgetKey, peer.key AS peerKey`

// TestPrefetchEdges_FootprintIdentical is the adjacency read-surface pin: one
// rule evaluated with batching on and off yields byte-identical footprints —
// EdgeRevisions and EdgeSelectors included — over a tombstoned source, an
// overflow-marked source the batch must decline, and sources with no adjacency
// document at all.
func TestPrefetchEdges_FootprintIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const instances, gadgets = 6, 3
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, markedID := seedFootprintShapes(t, reg, adjKV, coreKV, instances, gadgets)
	params := map[string]any{"k": hub}

	// The posture is named rather than inherited: the marked node's scoped read
	// is the arm this test is about.
	eng := New().WithHubReadScopeMode(HubReadScopeModeOn)
	on, onRows, onFootprint := runCounted(t, eng, footprintShapesBody, params, adjKV, coreKV)
	_, offRows, offFootprint := runCounted(t, prefetchOff(eng), footprintShapesBody, params, adjKV, coreKV)

	require.NotEmpty(t, onRows)
	require.Equal(t, len(offRows), len(onRows))
	require.Equal(t, anchorRows(offRows), anchorRows(onRows))
	require.True(t, reflect.DeepEqual(onFootprint, offFootprint),
		"batched node-state reads must observe the same read surface:\nbatched=%#v\npoint  =%#v",
		onFootprint, offFootprint)

	// The marked node keeps the scoped read's footprint shape: a selector, and
	// deliberately NO whole-read fingerprint to compare against.
	require.Contains(t, onFootprint.EdgeSelectors, markedID)
	require.NotContains(t, onFootprint.EdgeRevisions, markedID,
		"a marked node must stay on the scoped read, which the batch cannot answer for")
	require.Contains(t, onFootprint.EdgeRevisions, reg.idByName["gadget000"])
	require.Zero(t, onFootprint.EdgeRevisions[reg.idByName["gadget000"]],
		"a node with no adjacency document footprints an absence, not an omission")
	require.NotContains(t, onFootprint.EdgeRevisions, reg.idByName["inst000"],
		"a tombstoned vertex never binds, so it is never a source")

	// The gadget stage really did batch: three sources, one request.
	require.Less(t, on.adjReads, 1+instances+gadgets)
}

// TestPrefetchEdges_ObserverParity pins that batching does not change WHICH
// adjacency reads a caller is told about, nor in what order.
//
// The discriminating clause is the one whose first node is bound but LABELLED
// out: `matchPath` rejects every row before it hops, so the per-node path reads
// nothing there. A batch that reported its reads when it issued them would
// announce those nodes anyway — which is why an entry is reported when it is
// PROMOTED, at the point the per-node read would have happened.
func TestPrefetchEdges_ObserverParity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const instances, gadgets = 6, 3
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, _ := seedFootprintShapes(t, reg, adjKV, coreKV, instances, gadgets)
	params := map[string]any{"k": hub}

	// The labelled-out stage comes FIRST, while no instance's adjacency is
	// memoized yet, so the batch really does stage every one of them before
	// matchPath rejects the rows without hopping.
	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
OPTIONAL MATCH (inst:widget)-[:pairsWith]->(never:service)
OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
RETURN inst.key AS anchor, tpl.key AS templateKey, never.key AS neverKey`

	observe := func(e *Engine) []adjacency.ReadObservation {
		var seen []adjacency.ReadObservation
		ctx := adjacency.WithReadObserver(context.Background(), func(o adjacency.ReadObservation) {
			seen = append(seen, o)
		})
		runCountedCtx(t, ctx, e, body, params, adjKV, coreKV)
		return seen
	}

	eng := New().WithHubReadScopeMode(HubReadScopeModeOn)
	batched := observe(eng)
	perNode := observe(prefetchOff(eng))

	require.NotEmpty(t, perNode)
	require.Equal(t, perNode, batched,
		"a batch must report exactly the reads the per-node path reports, in the same order")

	// The staging really happened on the rejected stage: the whole instance
	// set was read in one batch, so the promoting stage issues no read at all.
	batchedEx, _, _ := runCounted(t, eng, body, params, adjKV, coreKV)
	require.Equal(t, 2, batchedEx.adjReads,
		"the anchor hop and the overflow-MARKED instance are the only per-node reads left; "+
			"every unmarked instance was staged by the rejected stage's batch")
	for _, o := range batched {
		require.NotEqual(t, reg.idByName["inst000"], o.NodeID,
			"a tombstoned vertex is never read")
	}
}

// TestPrefetchEdges_LeavesTheDegenerateCasesAlone pins what the adjacency batch
// declines: an executor with no adjacency handle, a request that reduces to a
// single node, and a node already answered at a relation scope.
func TestPrefetchEdges_LeavesTheDegenerateCasesAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedHub(t, reg, adjKV, coreKV, 3)
	first, second := reg.idByName["inst000"], reg.idByName["inst001"]

	readFree := &executor{ctx: context.Background()}
	require.NoError(t, readFree.prefetchEdges([]string{first, second}, "providedTo"))
	require.Empty(t, readFree.prefetchedEdges)

	ex := newTestExecutor(adjKV, coreKV)
	require.NoError(t, ex.prefetchEdges([]string{first}, "providedTo"))
	require.Empty(t, ex.prefetchedEdges, "a single node is left to the per-node read")

	require.NoError(t, ex.prefetchEdges([]string{first, first}, "providedTo"))
	require.Empty(t, ex.prefetchedEdges, "a duplicated node reduces to one")

	require.NoError(t, ex.prefetchEdges([]string{first, second}, "providedTo"))
	require.Len(t, ex.prefetchedEdges, 2)
	require.Zero(t, ex.adjReads, "a batch is not a per-node read")

	edges, err := ex.fetchEdges(first, "providedTo")
	require.NoError(t, err)
	require.Len(t, edges, 1, "the staged answer is the node's whole document")
	require.Zero(t, ex.adjReads, "promoting a staged entry issues no read")
	require.Contains(t, ex.edges, first)
	require.NotZero(t, ex.edgeRevisions[first])
}

// TestAdjacencyPrefetch_MatchesThePerNodeAnswer pins the batch against the
// primitive it stands in for, node shape by node shape, at the adjacency
// package's own boundary.
func TestAdjacencyPrefetch_MatchesThePerNodeAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedHub(t, reg, adjKV, coreKV, 3)
	putVertex(t, reg, coreKV, "lonely", "service", nil)

	hubID := reg.idByName["hub"]
	instID := reg.idByName["inst000"]
	lonelyID := reg.idByName["lonely"]
	markedID := reg.idByName["inst001"]
	markNodeOverflowed(t, adjKV, markedID)

	got, requests, err := adjacency.PrefetchNodes(context.Background(), adjKV,
		[]string{hubID, instID, lonelyID, markedID, "", "not.a.token"})
	require.NoError(t, err)
	require.Equal(t, 1, requests, "four nodes are one request")
	require.Len(t, got, 4, "an id that cannot be a subject token is skipped, never a panic")

	for _, id := range []string{hubID, instID, lonelyID} {
		wantEdges, wantFingerprint, err := adjacency.Neighbors(context.Background(), adjKV, coreKV, id)
		require.NoError(t, err)
		require.False(t, got[id].Marked)
		require.Equal(t, wantEdges, got[id].Edges, "node %s", id)
		require.Equal(t, wantFingerprint, got[id].Fingerprint, "node %s", id)
	}
	require.True(t, got[markedID].Marked)
	require.Empty(t, got[markedID].Edges, "a marked node's edges are not this batch's to answer")
	require.Zero(t, got[markedID].Fingerprint)

	// The absent-document node is an empty list at fingerprint 0, which is what
	// a footprint records as "this node had no edges", not "unread".
	require.Empty(t, got[lonelyID].Edges)
	require.NotNil(t, got[lonelyID].Edges)
	require.Zero(t, got[lonelyID].Fingerprint)
}

// TestExistencePredicate_PrefetchesBoundSources is the same discipline for an
// existence predicate — a bare path in a WHERE, and its `NOT (path)` form.
// Both walk the pattern once per row through matchPath, so both otherwise cost
// one node-state read per row, and both filter on the answer, which is what
// makes the row set the thing to compare.
//
// The predicate sits in a WITH's WHERE, where the rows it runs over are already
// projected and so already known — the position a batch can be taken from at
// all. A MATCH's own WHERE binds its subject in the same clause and is filtered
// per row inside that clause's loop, so its sources are not known before the
// loop and it keeps the per-node read.
func TestExistencePredicate_PrefetchesBoundSources(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const instances, withTemplate = 20, 12
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, _ := seedHub(t, reg, adjKV, coreKV, instances)
	// Only the first `withTemplate` instances carry a template, so the
	// predicate genuinely partitions the rows in both directions.
	for i := 0; i < withTemplate; i++ {
		inst, tpl := fmt.Sprintf("inst%03d", i), fmt.Sprintf("tpl%03d", i)
		putVertex(t, reg, coreKV, tpl, "service", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "instanceOf", inst, tpl)
	}
	params := map[string]any{"k": hub}

	for _, tc := range []struct {
		name      string
		predicate string
		wantRows  int
	}{
		{"exists", `(inst)-[:instanceOf]->(:service)`, withTemplate},
		{"negated", `NOT (inst)-[:instanceOf]->(:service)`, instances - withTemplate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
			         WITH inst
			         WHERE ` + tc.predicate + `
			         RETURN inst.key AS anchor`

			on, onRows, _ := runCounted(t, New(), body, params, adjKV, coreKV)
			off, offRows, _ := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

			require.Len(t, onRows, tc.wantRows, "the predicate must actually filter")
			require.Equal(t, anchorRows(offRows), anchorRows(onRows),
				"batching the predicate's sources must not change which rows survive it")

			require.Equal(t, instances, off.adjReads-on.adjReads,
				"every row's source the per-node path reads one at a time must come from the batch instead")
			require.Equal(t, 1, on.adjReads,
				"only the anchor's own hop is read per-node; the whole predicate is one batch")
		})
	}
}

// seedDecomposedStage writes `orders` order vertices, each with its own
// `instanceOf` template and `pairsWith` peer, plus `gadgets` gadget vertices
// each with a `pairsWith` peer of its own — two branch subjects, neither of
// whose adjacency any earlier clause reads, since both are seeded by scan.
func seedDecomposedStage(t *testing.T, reg *fixtureRegistry, adjKV, coreKV *substrate.KV, orders, gadgets int) {
	t.Helper()
	for i := 0; i < orders; i++ {
		order := fmt.Sprintf("order%03d", i)
		tpl := fmt.Sprintf("otpl%03d", i)
		peer := fmt.Sprintf("opeer%03d", i)
		putVertex(t, reg, coreKV, order, "order", map[string]any{"n": int64(i)})
		putVertex(t, reg, coreKV, tpl, "service", map[string]any{"n": int64(i)})
		putVertex(t, reg, coreKV, peer, "service", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "instanceOf", order, tpl)
		putEdge(t, reg, adjKV, "pairsWith", order, peer)
	}
	for i := 0; i < gadgets; i++ {
		gadget := fmt.Sprintf("gwidget%03d", i)
		peer := fmt.Sprintf("gpeer%03d", i)
		putVertex(t, reg, coreKV, gadget, "gadget", map[string]any{"n": int64(i)})
		putVertex(t, reg, coreKV, peer, "service", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "pairsWith", gadget, peer)
	}
}

// TestDecomposedStage_PrefetchesDeferredBranchSources pins the last per-row
// adjacency read a wide actor pays: a stage the branch-decomposition analysis
// splits expands each deferred branch ONE base row at a time, so nothing
// inside applyMatch can batch there. The batch is taken per branch, over every
// base row, before the fold loop begins.
//
// Both arms are seeded by SCAN, so no earlier clause has read either branch
// subject's adjacency and every adjacency read in the evaluation is the fold
// arm's own — which is what makes the count attributable.
func TestDecomposedStage_PrefetchesDeferredBranchSources(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const orders, gadgets = 20, 3
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedDecomposedStage(t, reg, adjKV, coreKV, orders, gadgets)

	t.Run("siblings off one bound subject", func(t *testing.T) {
		body := `MATCH (inst:order)
		         OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
		         OPTIONAL MATCH (inst)-[:pairsWith]->(peer:service)
		         RETURN inst.key AS anchor,
		                collect(DISTINCT tpl.key) AS templates,
		                collect(DISTINCT peer.key) AS peers`
		require.Len(t, lastPlan(t, body).deferred, 2,
			"the analysis must actually defer both branches, or this measures the product path")

		on, onRows, onFootprint := runCounted(t, New(), body, nil, adjKV, coreKV)
		off, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, nil, adjKV, coreKV)

		require.Len(t, onRows, orders)
		require.Equal(t, anchorRows(offRows), anchorRows(onRows),
			"batching a deferred branch's sources must not change a single aggregate")
		require.True(t, reflect.DeepEqual(onFootprint, offFootprint),
			"batched sources must observe the same read surface:\nbatched=%#v\npoint  =%#v",
			onFootprint, offFootprint)

		// Both branches hop from the SAME subject, so the node memo already
		// serves the second branch from the first branch's read: the reads the
		// per-row path pays are one per DISTINCT source, and the batch retires
		// all of them.
		require.Equal(t, orders, off.adjReads,
			"the per-row path reads each base row's subject once")
		require.Zero(t, on.adjReads,
			"one batch per deferred branch leaves no per-node adjacency read at all")
		require.Equal(t, orders, off.adjReads-on.adjReads)
		require.Equal(t, 1, on.batchReads,
			"both branches hop from the same subjects, so the second branch finds them all staged "+
				"and the whole stage costs ONE request")
	})

	t.Run("branches off two bound subjects", func(t *testing.T) {
		body := `MATCH (inst:order)
		         MATCH (g:gadget)
		         OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
		         OPTIONAL MATCH (g)-[:pairsWith]->(gpeer:service)
		         RETURN inst.key AS anchor, g.key AS gadgetKey,
		                collect(DISTINCT tpl.key) AS templates,
		                collect(DISTINCT gpeer.key) AS gadgetPeers`
		require.Len(t, lastPlan(t, body).deferred, 2)

		on, onRows, onFootprint := runCounted(t, New(), body, nil, adjKV, coreKV)
		off, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, nil, adjKV, coreKV)

		require.Len(t, onRows, orders*gadgets, "the two scans cross-product into the base rows")
		require.Equal(t, len(offRows), len(onRows))
		require.True(t, reflect.DeepEqual(offRows, onRows),
			"batching must not change a row of the decomposed stage")
		require.True(t, reflect.DeepEqual(onFootprint, offFootprint))

		// Each branch owns its own subject, so the per-row path pays for both
		// populations and each one is a batch of its own.
		require.Equal(t, orders+gadgets, off.adjReads)
		require.Zero(t, on.adjReads)
		require.Equal(t, 2, on.batchReads, "one request per deferred branch")
	})
}

// putCorrupt writes a body that is not JSON at key, standing in for the
// transitional or damaged entry a batch can pull in while reading ahead.
func putCorrupt(t *testing.T, kv *substrate.KV, key string) {
	t.Helper()
	_, err := kv.Put(context.Background(), key, []byte(`{"data": `))
	require.NoError(t, err)
}

// TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed pins that reading ahead cannot
// widen what an evaluation FAILS on. A batch necessarily covers keys a clause
// may not use, so a body that does not decode is logged and staged as nothing:
// the point read then fires if and only if the evaluation dereferences that
// key, and errors exactly where it did before any batching.
//
// Without that, a corrupt aspect on an untaken CASE arm would fail the whole
// evaluation — which the pipeline disposes as a redelivery, so the actor would
// never project again while a value it never reads stays broken.
func TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const neighbours = 6
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, neighbourKeys := seedHub(t, reg, adjKV, coreKV, neighbours)
	for _, key := range neighbourKeys {
		putCorrupt(t, coreKV, key+".audit")
	}
	params := map[string]any{"k": hub}

	t.Run("untaken arm", func(t *testing.T) {
		body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
		         RETURN inst.key AS anchor,
		                (CASE WHEN inst.key <> null THEN "bound" ELSE inst.audit.data.reason END) AS state`

		on, onRows, onFootprint := runCounted(t, New(), body, params, adjKV, coreKV)
		_, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

		require.Len(t, onRows, neighbours)
		require.Equal(t, anchorRows(offRows), anchorRows(onRows),
			"a corrupt body the evaluation never reads must not change a single row")
		require.True(t, reflect.DeepEqual(onFootprint, offFootprint))
		for _, key := range neighbourKeys {
			require.NotContains(t, on.prefetched, key+".audit",
				"a body that did not decode is staged as nothing, not as an absence")
		}
	})

	t.Run("taken arm", func(t *testing.T) {
		body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service)
		         RETURN inst.key AS anchor, inst.audit.data.reason AS reason`
		onErr := runExpectingError(t, New(), body, params, adjKV, coreKV)
		offErr := runExpectingError(t, prefetchOff(New()), body, params, adjKV, coreKV)
		require.Equal(t, offErr.Error(), onErr.Error(),
			"a corrupt body the evaluation DOES read must fail exactly as it failed un-batched")
		require.Contains(t, onErr.Error(), "unmarshal")
	})
}

// TestPrefetchEdges_CorruptDocumentFailsOnlyWhereItIsUsed is the adjacency twin:
// a node whose adjacency document does not decode is left out of the batch, so
// a walk that never hops from it succeeds and a walk that does fails exactly as
// it failed per-node.
func TestPrefetchEdges_CorruptDocumentFailsOnlyWhereItIsUsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const orders, gadgets = 6, 3
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedDecomposedStage(t, reg, adjKV, coreKV, orders, gadgets)
	// One gadget's adjacency document is damaged. It is batched with the other
	// gadgets whenever a stage hops from them.
	corruptGadget := reg.idByName["gwidget000"]
	putCorrupt(t, adjKV, subjects.AdjKey(corruptGadget))

	t.Run("never reached", func(t *testing.T) {
		// The gadget stage is labelled out, so no row ever hops from a gadget
		// — but the batch still reads every one of them.
		body := `MATCH (inst:order)
		         MATCH (g:gadget)
		         OPTIONAL MATCH (g:widget)-[:pairsWith]->(gpeer:service)
		         OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
		         RETURN inst.key AS anchor, g.key AS gadgetKey,
		                collect(DISTINCT tpl.key) AS templates,
		                collect(DISTINCT gpeer.key) AS gadgetPeers`

		on, onRows, onFootprint := runCounted(t, New(), body, nil, adjKV, coreKV)
		_, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, nil, adjKV, coreKV)

		require.NotEmpty(t, onRows)
		require.True(t, reflect.DeepEqual(offRows, onRows),
			"a corrupt document on a node the walk never hops from must not change a row")
		require.True(t, reflect.DeepEqual(onFootprint, offFootprint))
		require.NotContains(t, on.prefetchedEdges, corruptGadget,
			"a document that did not decode is staged as nothing")
	})

	t.Run("reached", func(t *testing.T) {
		body := `MATCH (g:gadget)
		         OPTIONAL MATCH (g)-[:pairsWith]->(gpeer:service)
		         RETURN g.key AS anchor, collect(DISTINCT gpeer.key) AS peers`
		onErr := runExpectingError(t, New(), body, nil, adjKV, coreKV)
		offErr := runExpectingError(t, prefetchOff(New()), body, nil, adjKV, coreKV)
		require.Equal(t, offErr.Error(), onErr.Error(),
			"a corrupt document the walk DOES hop from must fail exactly as it failed per-node")
		require.Contains(t, onErr.Error(), "unmarshal")
	})
}

// TestTraverseRel_PrefetchesEachHopsFrontier pins the ranged hop: a `*1..2`
// expansion steps from every node the previous hop reached, and those nodes are
// known before the loop, so hop 2's adjacency is one request rather than one
// read per frontier node.
func TestTraverseRel_PrefetchesEachHopsFrontier(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const mids = 50
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	root := putVertex(t, reg, coreKV, "root", "location", nil)
	for i := 0; i < mids; i++ {
		mid, leaf := fmt.Sprintf("mid%03d", i), fmt.Sprintf("leaf%03d", i)
		putVertex(t, reg, coreKV, mid, "location", map[string]any{"n": int64(i)})
		putVertex(t, reg, coreKV, leaf, "location", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "containedIn", mid, "root")
		putEdge(t, reg, adjKV, "containedIn", leaf, mid)
	}

	body := `MATCH (r:location {key: $k})<-[:containedIn*1..2]-(d:location) RETURN d.key AS anchor`
	params := map[string]any{"k": root}

	on, onRows, onFootprint := runCounted(t, New(), body, params, adjKV, coreKV)
	off, offRows, offFootprint := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)

	require.Len(t, onRows, 2*mids, "every mid and every leaf is reached")
	require.Equal(t, anchorRows(offRows), anchorRows(onRows))
	require.True(t, reflect.DeepEqual(onFootprint, offFootprint),
		"batching a ranged hop's frontier must observe the same read surface")

	// Hop 1 steps from the root alone (one node, left to the per-node read);
	// hop 2 steps from all `mids`, which is where the batch is.
	require.Equal(t, mids, off.adjReads-on.adjReads)
	require.Equal(t, 1, on.adjReads, "only the root's own read is left per-node")
	require.Equal(t, 2, on.batchReads,
		"two requests: hop 1's Core KV frontier, and hop 2's adjacency over every node it reached")
	require.Zero(t, off.batchReads)
}

// TestParsePrefetchMode pins the parser: it round-trips the two modes and
// rejects anything else rather than guessing, and Unset reports "unset".
func TestParsePrefetchMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want PrefetchMode
	}{
		{"on", PrefetchModeOn},
		{"off", PrefetchModeOff},
	} {
		got, err := ParsePrefetchMode(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
		require.Equal(t, tc.in, got.String(), "String must round-trip what Parse accepts")
	}
	require.Equal(t, "unset", PrefetchModeUnset.String())

	for _, bad := range []string{"", "ON", "true", "1", "disabled", " on"} {
		got, err := ParsePrefetchMode(bad)
		require.Errorf(t, err, "%q must be rejected, never guessed at", bad)
		require.Equal(t, PrefetchModeUnset, got)
	}
}

// TestDefaultPrefetchMode_Off_TakesPointReadPath pins the package-wide lever
// (REFRACTOR_ENGINE_PREFETCH): a plain New() engine — no per-engine override,
// exactly the form every production call site uses — batches under an unset
// or On default, and takes the point-read path the moment the default is Off,
// indistinguishable from the test-only prefetchOff helper's own override.
func TestDefaultPrefetchMode_Off_TakesPointReadPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	// The package default is asserted, so pin it to its unset state first and
	// put back whatever was there — the same form
	// TestHubReadScopeMode_ParseAndPrecedence uses, so package state is never
	// left disturbed for another test.
	restore := PrefetchMode(defaultPrefetchMode.Load())
	t.Cleanup(func() { SetDefaultPrefetchMode(restore) })
	SetDefaultPrefetchMode(PrefetchModeUnset)

	require.Equal(t, PrefetchModeOn, DefaultPrefetchMode(), "an unset package default resolves to on")
	require.False(t, New().prefetchModeDisabled(), "an engine with no override batches under the default")

	const neighbours = 20
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	hub, _ := seedHub(t, reg, adjKV, coreKV, neighbours)
	body := `MATCH (i:identity {key: $k})<-[:providedTo]-(inst:service) RETURN inst.key AS anchor`
	params := map[string]any{"k": hub}

	// The default still On: a plain New() engine batches exactly like the
	// earlier prefetch tests already pin.
	on, onRows, _ := runCounted(t, New(), body, params, adjKV, coreKV)
	require.Len(t, onRows, neighbours)
	require.Equal(t, 1, on.batchReads)

	// Flip the default off. A brand-new engine — still no per-engine
	// override — now takes the point-read path.
	SetDefaultPrefetchMode(PrefetchModeOff)
	require.True(t, New().prefetchModeDisabled(), "an engine with no override now takes the default's off")

	off, offRows, _ := runCounted(t, New(), body, params, adjKV, coreKV)
	require.Equal(t, anchorRows(offRows), anchorRows(onRows),
		"the package default must change how the read happens, never what the pattern binds")
	require.Zero(t, off.batchReads, "the default's off must reach a plain New() engine with no override")
	require.Equal(t, neighbours, off.pointReads-on.pointReads,
		"every neighbour must now cost its own point read once the default is off, matching prefetchOff's path")

	// prefetchOff's per-engine override must still force the point-read path
	// regardless of the package default.
	SetDefaultPrefetchMode(PrefetchModeOn)
	stillOff, stillOffRows, _ := runCounted(t, prefetchOff(New()), body, params, adjKV, coreKV)
	require.Equal(t, anchorRows(offRows), anchorRows(stillOffRows))
	require.Zero(t, stillOff.batchReads)
	require.Equal(t, off.pointReads, stillOff.pointReads,
		"the per-engine override must cost exactly what the package default's off costs")
}
