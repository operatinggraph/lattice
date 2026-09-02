package full

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// readRecorder collects the adjacency reads made under it, so a test can pin
// WHICH read a caller took rather than inferring it from a footprint's shape.
type readRecorder struct {
	mu   sync.Mutex
	seen []adjacency.ReadObservation
}

func (r *readRecorder) observe(obs adjacency.ReadObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The observation's Relations set belongs to the caller, so copy it before
	// keeping it past the call.
	if obs.Relations != nil {
		rels := map[string]struct{}{}
		for rel := range obs.Relations {
			rels[rel] = struct{}{}
		}
		obs.Relations = rels
	}
	r.seen = append(r.seen, obs)
}

// of returns every read recorded for one node.
func (r *readRecorder) of(nodeID string) []adjacency.ReadObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []adjacency.ReadObservation
	for _, obs := range r.seen {
		if obs.NodeID == nodeID {
			out = append(out, obs)
		}
	}
	return out
}

// observedCtx returns a context whose adjacency reads land in a fresh recorder.
func observedCtx() (context.Context, *readRecorder) {
	rec := &readRecorder{}
	return adjacency.WithReadObserver(context.Background(), rec.observe), rec
}

// hubReadScopeQuery walks one typed relation off a marked role node.
const hubReadScopeQuery = `MATCH (r:role {key: $k})<-[:queuedFor]-(t:task) RETURN t.key AS taskKey`

// TestExecutor_HubRead_TypedHop_FootprintsSelectorsOnly pins §9.1's rules 2
// and 3 end to end: a typed hop over an overflow-marked hub reads only that
// hop's relation, so the hub carries NO whole-read fingerprint and is pinned
// by its Matched set alone — a set holding exactly the hub's Core KV links of
// that relation, with the hub's OTHER relation nowhere in the footprint.
//
// The mode-off twin runs the same evaluation on the whole-node read and must
// carry the fingerprint, and both modes must bind the same rows: the scope
// changes what is READ and what is FOOTPRINTED, never what the pattern
// matches.
func TestExecutor_HubRead_TypedHop_FootprintsSelectorsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "task2", "task", nil)
	putVertex(t, reg, coreKV, "perm1", "permission", nil)

	roleID := reg.idByName["role"]
	markNodeOverflowed(t, adjKV, roleID)
	queued1 := putLink(t, reg, coreKV, "queuedFor", "task1", "role")
	queued2 := putLink(t, reg, coreKV, "queuedFor", "task2", "role")
	granted := putLink(t, reg, coreKV, "grantedBy", "perm1", "role")

	wantRows := []string{vtxKey(reg, "task1"), vtxKey(reg, "task2")}
	ec := ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}}

	scoped := New().WithHubReadScopeMode(HubReadScopeModeOn)
	cr, err := scoped.Parse(hubReadScopeQuery)
	require.NoError(t, err)

	ctx, rec := observedCtx()
	rows, fp, err := scoped.ExecuteWithFootprint(ctx, cr, ec, adjKV, coreKV)
	require.NoError(t, err)
	require.ElementsMatch(t, wantRows, rowKeys(t, rows, "taskKey"))

	// WHICH read happened, pinned directly rather than inferred from the
	// footprint's shape — several read paths can produce the same shape.
	reads := rec.of(roleID)
	require.Len(t, reads, 1, "the hub is read exactly once for the whole evaluation")
	require.Equal(t, map[string]struct{}{"queuedFor": {}}, reads[0].Relations,
		"the typed hop reads the hub at its own relation, not whole")
	require.True(t, reads[0].Marked)
	require.False(t, reads[0].Whole, "a marked node answering a scoped read does not answer whole")

	require.NotContains(t, fp.EdgeRevisions, roleID,
		"a relation-scoped fingerprint is not comparable with a whole read's, so it must never be recorded")
	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok, "the hub must be footprinted by the selector the hop consulted")
	require.False(t, sel.Fallback)

	// These are recordEdgeSelector's properties, not the scoped read's: a
	// typed hop records exactly the selector it consulted and the identities
	// that passed it, and would record the same set off a whole read. The
	// scope assertion is the observer above.
	require.Len(t, sel.Matched, 1, "a typed hop records exactly the one selector it consulted")
	matched := sel.Matched[ruleengine.EdgeSelector{RelType: "queuedFor", Direction: "in"}]
	require.Equal(t, map[string]struct{}{queued1: {}, queued2: {}}, matched,
		"and exactly the identities that passed it")
	require.NotContains(t, matched, granted)
	for selector := range sel.Matched {
		require.NotEqual(t, "grantedBy", selector.RelType,
			"a relation no hop followed is recorded by no selector")
	}

	// The way back: the same evaluation on the whole-node read.
	whole := New().WithHubReadScopeMode(HubReadScopeModeOff)
	wholeCR, err := whole.Parse(hubReadScopeQuery)
	require.NoError(t, err)

	wholeCtx, wholeRec := observedCtx()
	wholeRows, wholeFP, err := whole.ExecuteWithFootprint(wholeCtx, wholeCR, ec, adjKV, coreKV)
	require.NoError(t, err)
	require.ElementsMatch(t, wantRows, rowKeys(t, wholeRows, "taskKey"),
		"the scope must change what is read, never what the pattern binds")

	wholeReads := wholeRec.of(roleID)
	require.Len(t, wholeReads, 1)
	require.Nil(t, wholeReads[0].Relations, "with the scope off the same hop reads the hub whole")
	require.True(t, wholeReads[0].Whole)
	require.Contains(t, wholeFP.EdgeRevisions, roleID,
		"with the scope off the hub is read whole and carries its whole-read fingerprint")
	require.NotZero(t, wholeFP.EdgeRevisions[roleID])
	require.Equal(t, sel.Matched, wholeFP.EdgeSelectors[roleID].Matched,
		"and the Matched set the hop records is the same either way")
}

// TestExecutor_HubRead_UnmarkedNode_ReadsWholeOnce pins the other half of rule
// 2: an UNMARKED node is byte-identical to the unscoped read however many
// typed relations cross it. A document is one key, so narrowing it would cost
// one read per relation and save nothing — the first typed hop memoizes the
// whole document, every later hop is served from that memo, and the node
// carries a whole-read fingerprint with no hub entry anywhere.
func TestExecutor_HubRead_UnmarkedNode_ReadsWholeOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "perm1", "permission", nil)
	putEdge(t, reg, adjKV, "queuedFor", "task1", "role")
	putEdge(t, reg, adjKV, "grantedBy", "perm1", "role")

	roleID := reg.idByName["role"]
	ex := newScopedTestExecutor(adjKV, coreKV, HubReadScopeModeOn)

	queued, err := ex.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Len(t, queued, 2, "an unmarked node answers whole; the caller filters")

	granted, err := ex.fetchEdges(roleID, "grantedBy")
	require.NoError(t, err)
	require.Equal(t, queued, granted, "the second relation is served from the whole memo, not a second read")

	require.Len(t, ex.edges, 1, "one whole memo entry for the node")
	require.Contains(t, ex.edges, roleID)
	require.Empty(t, ex.hubEdges, "an unmarked node never takes the hub memo")

	fp := ex.footprint()
	require.Contains(t, fp.EdgeRevisions, roleID, "a whole read is footprinted by its fingerprint")
	require.NotZero(t, fp.EdgeRevisions[roleID])
}

// TestExecutor_HubRead_RepeatableReadPerRelation pins rule 2's repeatable-read
// key: a hub read is memoized under (node, relation), so the SAME typed hop
// crossing the same hub twice in one evaluation observes the list the
// evaluation already saw even though a link of that relation has since
// committed — and a DIFFERENT relation on the same hub is a different key and
// reads for itself.
func TestExecutor_HubRead_RepeatableReadPerRelation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "task2", "task", nil)
	putVertex(t, reg, coreKV, "perm1", "permission", nil)

	roleID := reg.idByName["role"]
	markNodeOverflowed(t, adjKV, roleID)
	queued1 := putLink(t, reg, coreKV, "queuedFor", "task1", "role")
	granted := putLink(t, reg, coreKV, "grantedBy", "perm1", "role")

	ex := newScopedTestExecutor(adjKV, coreKV, HubReadScopeModeOn)

	first, err := ex.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Equal(t, []string{queued1}, edgeIDs(first))

	// A commit lands mid-evaluation, on exactly the relation already read.
	putLink(t, reg, coreKV, "queuedFor", "task2", "role")

	second, err := ex.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Equal(t, first, second,
		"a second hop over the same (hub, relation) must observe the list the evaluation already saw")

	other, err := ex.fetchEdges(roleID, "grantedBy")
	require.NoError(t, err)
	require.Equal(t, []string{granted}, edgeIDs(other),
		"a different relation on the same hub is a different memo key and reads for itself")

	require.Empty(t, ex.edges, "a marked hub never enters the whole memo on a typed hop")
	require.Empty(t, ex.edgeRevisions, "and its scoped fingerprint is never recorded")
	require.Len(t, ex.hubEdges, 2, "one hub memo entry per (hub, relation) read")

	// A fresh evaluation observes the commit — the memo is per-evaluation.
	next := newScopedTestExecutor(adjKV, coreKV, HubReadScopeModeOn)
	fresh, err := next.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Len(t, fresh, 2, "the next evaluation must see the committed queuedFor link")
}

// TestHubReadScopeMode_ParseAndPrecedence pins the switch itself: the parser
// round-trips the two modes and rejects anything else rather than guessing,
// the package default resolves Unset to On, and an engine's own override wins
// over the package default WITHOUT mutating it — the form every other test
// here uses so package state is never touched.
func TestHubReadScopeMode_ParseAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want HubReadScopeMode
	}{
		{"on", HubReadScopeModeOn},
		{"off", HubReadScopeModeOff},
	} {
		got, err := ParseHubReadScopeMode(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
		require.Equal(t, tc.in, got.String(), "String must round-trip what Parse accepts")
	}
	require.Equal(t, "unset", HubReadScopeModeUnset.String())

	for _, bad := range []string{"", "ON", "true", "1", "disabled", " on"} {
		got, err := ParseHubReadScopeMode(bad)
		require.Errorf(t, err, "%q must be rejected, never guessed at", bad)
		require.Equal(t, HubReadScopeModeUnset, got)
	}

	// The package default is asserted, so pin it to its unset state first and
	// put back whatever was there. Unset is the initial state, so no other
	// test can be relying on a different one.
	restore := HubReadScopeMode(defaultHubReadScopeMode.Load())
	t.Cleanup(func() { SetDefaultHubReadScopeMode(restore) })
	SetDefaultHubReadScopeMode(HubReadScopeModeUnset)

	require.Equal(t, HubReadScopeModeOn, DefaultHubReadScopeMode(),
		"an unset package default resolves to on")

	base := New()
	require.True(t, base.hubReadScopeEnabled(), "an engine with no override takes the default")

	off := base.WithHubReadScopeMode(HubReadScopeModeOff)
	require.False(t, off.hubReadScopeEnabled())
	require.True(t, base.hubReadScopeEnabled(), "the override returns a copy; the receiver is unchanged")
	require.Equal(t, HubReadScopeModeOn, DefaultHubReadScopeMode(),
		"and a per-engine override never mutates package state")

	require.True(t, off.WithHubReadScopeMode(HubReadScopeModeUnset).hubReadScopeEnabled(),
		"Unset returns an engine to the package default")
}

// rowKeys pulls one string column out of a result set.
func rowKeys(t *testing.T, rows []ruleengine.ProjectionResult, column string) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		v, ok := row.Values[column].(string)
		require.Truef(t, ok, "column %q is not a string in %v", column, row.Values)
		out = append(out, v)
	}
	return out
}

// edgeIDs renders an edge list as its edge identities, the unit a footprint
// and these tests compare by.
func edgeIDs(edges []adjacency.EdgeEntry) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.EdgeID)
	}
	return out
}

// TestExecutor_HubRead_WholeReadComposesOverPinnedRelations pins §9.1 rule 2's
// memo-key invariant: the memo key is the RELATION, and a whole read is the
// union of every relation's read rather than a value that may overwrite one.
//
// A typed hop pins relation A on a marked hub at t1. An untyped hop then reads
// the same hub WHOLE at t2, and that read's own view of A is a later instant
// than the one an earlier hop already bound rows from. Composing the whole read
// against the pinned relation is what keeps the evaluation's view of A at t1
// — without it the whole entry would shadow the hub memo and a third hop on A
// would see t2, which is exactly the two-instants-in-one-evaluation defect the
// memo exists to close.
func TestExecutor_HubRead_WholeReadComposesOverPinnedRelations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "task2", "task", nil)
	putVertex(t, reg, coreKV, "perm1", "permission", nil)

	roleID := reg.idByName["role"]
	markNodeOverflowed(t, adjKV, roleID)
	queued1 := putLink(t, reg, coreKV, "queuedFor", "task1", "role")
	granted := putLink(t, reg, coreKV, "grantedBy", "perm1", "role")

	ex := newScopedTestExecutor(adjKV, coreKV, HubReadScopeModeOn)

	// t1 — a typed hop pins queuedFor.
	pinned, err := ex.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Equal(t, []string{queued1}, edgeIDs(pinned))

	// A second queuedFor link commits between the two hops.
	putLink(t, reg, coreKV, "queuedFor", "task2", "role")

	// t2 — an untyped hop reads the hub whole. Its queuedFor subset must be
	// the t1 answer, and every other relation must be present as the whole
	// read found it.
	whole, err := ex.fetchEdges(roleID, "")
	require.NoError(t, err)
	require.Equal(t, []string{queued1}, edgeIDs(edgesOfRelation(whole, "queuedFor")),
		"the whole read must not overwrite a relation this evaluation already pinned")
	require.Equal(t, []string{granted}, edgeIDs(edgesOfRelation(whole, "grantedBy")),
		"a relation nothing pinned comes through as the whole read found it")
	require.True(t, sort.SliceIsSorted(whole, func(i, j int) bool {
		if whole[i].EdgeID != whole[j].EdgeID {
			return whole[i].EdgeID < whole[j].EdgeID
		}
		return whole[i].Direction < whole[j].Direction
	}), "the composed list must be in the order one read would have produced")

	// A third hop on the pinned relation is served the same answer as the
	// first, whichever memo now holds it.
	again, err := ex.fetchEdges(roleID, "queuedFor")
	require.NoError(t, err)
	require.Equal(t, []string{queued1}, edgeIDs(edgesOfRelation(again, "queuedFor")),
		"every hop's view of a relation must equal the FIRST hop's view of that relation")

	require.Contains(t, ex.edgeRevisions, roleID,
		"the whole read still records ITS fingerprint — the composed relation is pinned by its Matched set instead")
}

// TestExecutor_HubRead_TypedThenUntypedHop_Footprints is the evaluation-level
// twin: the footprint shape the validator's coarse path is tested against must
// actually come out of the executor, not only out of a hand-built literal. A
// typed hop followed by an untyped hop on one marked hub produces a Fallback
// entry that still carries the earlier typed hop's Matched set, alongside the
// untyped hop's whole-read fingerprint — which is precisely the pair
// pipeline.footprintValid's coarse path compares.
func TestExecutor_HubRead_TypedThenUntypedHop_Footprints(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "role", "role", nil)
	putVertex(t, reg, coreKV, "task1", "task", nil)
	putVertex(t, reg, coreKV, "perm1", "permission", nil)

	roleID := reg.idByName["role"]
	markNodeOverflowed(t, adjKV, roleID)
	queued1 := putLink(t, reg, coreKV, "queuedFor", "task1", "role")
	putLink(t, reg, coreKV, "grantedBy", "perm1", "role")

	eng := New().WithHubReadScopeMode(HubReadScopeModeOn)
	cr, err := eng.Parse(
		`MATCH (r:role {key: $k})<-[:queuedFor]-(t:task) ` +
			`OPTIONAL MATCH (r)<--(x) ` +
			`RETURN t.key AS taskKey, x.key AS anyKey`)
	require.NoError(t, err)

	ctx, rec := observedCtx()
	_, fp, err := eng.ExecuteWithFootprint(ctx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "role")}},
		adjKV, coreKV)
	require.NoError(t, err)

	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok)
	require.True(t, sel.Fallback, "the untyped hop consumes every edge, so the node falls back")
	require.Len(t, sel.Matched, 1,
		"recordEdgeSelector stops at Fallback, so the sets present are exactly the typed hops that preceded it")
	require.Equal(t, map[string]struct{}{queued1: {}},
		sel.Matched[ruleengine.EdgeSelector{RelType: "queuedFor", Direction: "in"}])
	require.Contains(t, fp.EdgeRevisions, roleID,
		"the untyped hop read the hub whole, so a comparable fingerprint IS recorded")

	// Two reads of the hub, in that order: the typed hop's scoped one, then
	// the untyped hop's whole one.
	reads := rec.of(roleID)
	require.Len(t, reads, 2)
	require.Equal(t, map[string]struct{}{"queuedFor": {}}, reads[0].Relations)
	require.Nil(t, reads[1].Relations)
}

// edgesOfRelation returns the subset of edges carrying one relation name.
func edgesOfRelation(edges []adjacency.EdgeEntry, name string) []adjacency.EdgeEntry {
	out := make([]adjacency.EdgeEntry, 0, len(edges))
	for _, e := range edges {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}
