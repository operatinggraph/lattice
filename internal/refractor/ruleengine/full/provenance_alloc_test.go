package full

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// provAllocNeighbours is the widest actor's shape, rounded: the live stack's
// widest personal subject holds 3,638 `providedTo` instances of which the
// sample found none live, and its lens projects two rows.
const provAllocNeighbours = 4000

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

// TestProvenance_AllocationWithinTwiceTheBaseline pins the cost §4.1 states:
// one pointer per clone, one slice append per fetch, one fold per output row.
// The fixture is the widest actor's shape — 4,000 tombstoned neighbours and 2
// live ones, all of them candidates one head fetches — which is where the
// chain is at its longest and its output at its narrowest, so the ratio here
// is the worst the corpus offers.
//
// The baseline is the same evaluation over the same graph with a root binding
// that carries no chain: identical code, identical reads, the mechanism inert.
func TestProvenance_AllocationWithinTwiceTheBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	// The shape is measured on the adjacency DOCUMENT path. A node this wide
	// latches past the production thresholds and serves its edges out of Core
	// KV's link keyspace instead, which putEdge does not write — so the
	// thresholds are raised for the life of this test rather than the fixture
	// being narrowed to something the widest actor is not.
	defer adjacency.SetOverflowThresholds(1<<20, 8<<20)()

	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	hub := putVertex(t, reg, coreKV, "hub", "identity", nil)
	for i := 0; i < provAllocNeighbours; i++ {
		name := fmt.Sprintf("dead%04d", i)
		putVertex(t, reg, coreKV, name, "service", map[string]any{"isDeleted": true})
		putEdge(t, reg, adjKV, "providedTo", name, "hub")
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("live%02d", i)
		putVertex(t, reg, coreKV, name, "service", map[string]any{"n": int64(i)})
		putEdge(t, reg, adjKV, "providedTo", name, "hub")
	}

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

	require.Len(t, withProv, 2)
	require.Len(t, baseRows, 2)
	for i := range withProv {
		require.Equal(t, withProv[i].Values, baseRows[i].Values)
		require.Nil(t, baseRows[i].Provenance)
		require.Len(t, withProv[i].Provenance, provAllocNeighbours+3,
			"every candidate, the two live instances and the hub")
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

// TestProvenance_FoldIsMemoizedPerNode pins the flatten's memo: a chain shared
// by many rows is folded once, so a wide head's candidate set is not walked
// per output row. The second ask for the same node is served from the memo
// and returns the identical slice.
func TestProvenance_FoldIsMemoizedPerNode(t *testing.T) {
	ex := &executor{provFolded: map[*provNode][]string{}}
	head := &provNode{keys: []string{
		substrate.VertexKey("identity", c1NanoID("alice")),
		substrate.VertexKey("org", c1NanoID("acme")),
	}}
	row := &provNode{parent: head, keys: []string{substrate.VertexKey("city", c1NanoID("paris"))}}

	first := ex.provVertexKeys(row)
	require.Len(t, first, 3)
	require.Len(t, ex.provFolded, 1)

	// Folding the shared head afterwards must agree with what the row's own
	// walk found for it, and must not re-walk a memoized answer.
	require.Len(t, ex.provVertexKeys(head), 2)
	second := ex.provVertexKeys(row)
	require.Equal(t, fmt.Sprintf("%p", first), fmt.Sprintf("%p", second),
		"a second fold of one node must be the memoized slice, not a fresh walk")
}
