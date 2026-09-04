package full

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// provAllocNeighbours is the widest actor's shape, rounded: the live stack's
// widest personal subject holds 3,638 `providedTo` instances of which the
// sample found none live, and its lens projects two rows.
const provAllocNeighbours = 4000

// provWideCandidates / provWideRows are the OTHER extreme of the same corpus:
// `edgeCatalog`'s shape, where one actor's walk fetches a couple of thousand
// candidates and the projection carries roughly a hundred rows off the one
// head chain they share. It is the fold's worst case as the narrow fixture is
// the chain's — every one of those rows names the whole candidate set.
const (
	provWideCandidates = 2000
	provWideRows       = 100
)

// evalMatchReturn runs one MATCH + one RETURN query from a caller-supplied
// root binding, through the same executor methods run() drives. The root is
// the mechanism's own switch: a root carrying no chain gives every clone
// nothing to descend from and every read nowhere to record, so the two arms
// differ in the provenance machinery and in nothing else. Any other clause
// shape fails rather than being silently skipped, so the measurement can never
// drift into covering less of the query than it claims.
func evalMatchReturn(
	t *testing.T, ex *executor, compiled *CompiledRule, root binding,
) []ruleengine.ProjectionResult {
	t.Helper()
	bindings := []binding{root}
	var out []ruleengine.ProjectionResult
	for _, clause := range compiled.Query.Clauses {
		switch c := clause.(type) {
		case *Match:
			next, err := ex.applyMatch(bindings, c)
			require.NoError(t, err)
			bindings = next
		case *Return:
			res, err := ex.applyReturn(bindings, c)
			require.NoError(t, err)
			out = res
		default:
			t.Fatalf("this measurement covers MATCH + RETURN only, got %T", clause)
		}
	}
	return out
}

// putProvidedToHub seeds a "providedTo" edge from every name in names to hub.
// Each neighbour's own outbound document holds exactly one entry, so those
// still go through the incremental adjacency.Build one edge at a time — O(1)
// each. Hub's inbound document is the one every one of those edges lands on,
// so building it the same incremental way re-marshals and rewrites hub's
// whole, ever-growing document on every call: O(N) work N times over. This
// collects every inbound EdgeEntry first and writes hub's document once,
// through the package's own AdjValue encoding — the same shape upsertEdge
// itself marshals — so the seed stays linear in len(names) instead of
// quadratic.
func putProvidedToHub(t testing.TB, reg *fixtureRegistry, adjKV *substrate.KV, names []string, hubName string) {
	t.Helper()
	ctx := context.Background()
	hubID := reg.idByName[hubName]
	hubType := reg.typeByID[hubID]
	require.NotEmpty(t, hubID, "fixture: %q not registered", hubName)

	inbound := make([]adjacency.EdgeEntry, 0, len(names))
	for _, name := range names {
		fromID := reg.idByName[name]
		fromType := reg.typeByID[fromID]
		require.NotEmpty(t, fromID, "fixture: %q not registered", name)

		edgeID := "providedTo_" + fromID + "_" + hubID
		coreKvKey := "lnk." + fromType + "." + fromID + ".providedTo." + hubType + "." + hubID

		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: coreKvKey, EdgeID: edgeID, Name: "providedTo",
			Direction: "outbound", NodeID: fromID, OtherNodeID: hubID, OtherType: hubType,
		}))
		inbound = append(inbound, adjacency.EdgeEntry{
			CoreKvKey: coreKvKey, EdgeID: edgeID, Name: "providedTo",
			Direction: "inbound", OtherNodeID: fromID, OtherType: fromType,
		})
	}

	data, err := json.Marshal(adjacency.AdjValue{Edges: inbound})
	require.NoError(t, err)
	_, err = adjKV.Create(ctx, subjects.AdjKey(hubID), data)
	require.NoError(t, err)
}

// TestProvenance_AllocationWithinTwiceTheBaseline pins the cost §4.1 states:
// one pointer per clone, one slice append per fetch, one fold per output row.
// Both extremes of the corpus are measured against the same budget, because
// each is the worst case of a different half of the mechanism:
//
//   - 4,000 tombstoned neighbours and 2 live ones is where the RECORD is at
//     its longest and the output at its narrowest — the candidate set is held
//     once and folded twice.
//   - 2,000 candidates and 100 rows is where the FOLD costs most: every row
//     names the whole candidate set, so the head chain is what the memo has to
//     keep from being rebuilt a hundred times over.
//
// The baseline is the same evaluation over the same graph with a root binding
// that carries no chain: identical code, identical reads, the mechanism inert.
func TestProvenance_AllocationWithinTwiceTheBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("long chain, two rows", func(t *testing.T) {
		measureProvAllocation(t, provAllocNeighbours, 2)
	})
	t.Run("wide head, many rows", func(t *testing.T) {
		measureProvAllocation(t, provWideCandidates, provWideRows)
	})
}

// measureProvAllocation evaluates one MATCH + RETURN over a hub with `dead`
// tombstoned candidates and `live` bound ones, with the provenance machinery
// on and off, and fails when recording costs more than twice the evaluation
// that records nothing.
func measureProvAllocation(t *testing.T, dead, live int) {
	t.Helper()
	// The shape is measured on the adjacency DOCUMENT path. A node this wide
	// latches past the production thresholds and serves its edges out of Core
	// KV's link keyspace instead, which putEdge does not write — so the
	// thresholds are raised for the life of this test rather than the fixture
	// being narrowed to something the widest actor is not.
	defer adjacency.SetOverflowThresholds(1<<20, 8<<20)()

	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	hub := putVertex(t, reg, coreKV, "hub", "identity", nil)
	neighbourNames := make([]string, 0, dead+live)
	for i := 0; i < dead; i++ {
		name := fmt.Sprintf("dead%04d", i)
		putVertex(t, reg, coreKV, name, "service", map[string]any{"isDeleted": true})
		neighbourNames = append(neighbourNames, name)
	}
	for i := 0; i < live; i++ {
		name := fmt.Sprintf("live%04d", i)
		putVertex(t, reg, coreKV, name, "service", map[string]any{"n": int64(i)})
		neighbourNames = append(neighbourNames, name)
	}
	putProvidedToHub(t, reg, adjKV, neighbourNames, "hub")

	eng := New()
	cr, err := eng.Parse(
		`MATCH (i:identity {key: $actorKey})<-[:providedTo]-(inst:service)
		 RETURN inst.key AS anchor, i.key AS actor`)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)
	ec := ruleengine.EventContext{Parameters: map[string]any{"actorKey": hub}}

	// One evaluation of each arm first: it warms whatever the substrate client
	// allocates once, and it is what asserts the two arms agree on the rows and
	// that the recording arm actually recorded the candidate set.
	withProv := evalMatchReturn(t,
		eng.newExecutor(context.Background(), compiled, ec, adjKV, coreKV),
		compiled, binding{provBindingKey: provRoot()})
	baseRows := evalMatchReturn(t,
		eng.newExecutor(context.Background(), compiled, ec, adjKV, coreKV),
		compiled, binding{})

	require.Len(t, withProv, live)
	require.Len(t, baseRows, live)
	for i := range withProv {
		require.Equal(t, withProv[i].Values, baseRows[i].Values)
		require.Nil(t, baseRows[i].Provenance)
		require.Len(t, withProv[i].Provenance, dead+live+1,
			"every candidate, every live instance and the hub")
	}

	measure := func(root func() binding) uint64 {
		const runs = 3
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < runs; i++ {
			ex := eng.newExecutor(context.Background(), compiled, ec, adjKV, coreKV)
			evalMatchReturn(t, ex, compiled, root())
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	baseline := measure(func() binding { return binding{} })
	recorded := measure(func() binding { return binding{provBindingKey: provRoot()} })

	require.Positive(t, baseline)
	t.Logf("allocated bytes over 3 evaluations: baseline %d, recording %d (%.2fx)",
		baseline, recorded, float64(recorded)/float64(baseline))
	require.LessOrEqual(t, recorded, 2*baseline,
		"recording provenance allocated %d bytes against a baseline of %d — more than the 2x §4.1 budgets",
		recorded, baseline)
}

// TestProvenance_FoldIsMemoizedPerNode pins the flatten's memo in the order
// production asks for it: applyReturn folds one leaf per output row, an output
// row's leaf is shared by nothing, and what those leaves have in common is
// their ancestry — the head chain carrying the walk's whole candidate set. So
// the fold has to enter that chain once for the projection rather than once
// per row, which a memo kept only for the node the caller named would not do.
//
// The counter is the pin. Folding one row enters its leaf, the clone it was
// projected from, the head and the root; every row after it enters its own two
// and reads the rest back — so the whole projection is 2 per row and a
// constant, against 4 per row for a fold that re-walked.
func TestProvenance_FoldIsMemoizedPerNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	const rows = 50
	hub := putVertex(t, reg, coreKV, "hub", "identity", nil)
	neighbourNames := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		name := fmt.Sprintf("live%04d", i)
		putVertex(t, reg, coreKV, name, "service", map[string]any{"n": int64(i)})
		neighbourNames = append(neighbourNames, name)
	}
	putProvidedToHub(t, reg, adjKV, neighbourNames, "hub")

	eng := New()
	cr, err := eng.Parse(
		`MATCH (i:identity {key: $actorKey})<-[:providedTo]-(inst:service)
		 RETURN inst.key AS anchor, i.key AS actor`)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)

	ex := eng.newExecutor(context.Background(), compiled,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": hub}}, adjKV, coreKV)
	out := evalMatchReturn(t, ex, compiled, binding{provBindingKey: provRoot()})
	require.Len(t, out, rows)
	for _, r := range out {
		require.Len(t, r.Provenance, rows+1, "every candidate and the hub")
	}

	require.LessOrEqual(t, ex.provFoldVisits, 2*rows+4,
		"the head every row descends from was entered more than once: %d node visits for %d rows",
		ex.provFoldVisits, rows)
	require.Greater(t, len(ex.provFolded), 2*rows,
		"a fold that memoized only the node it was asked for holds one entry per row")
}
