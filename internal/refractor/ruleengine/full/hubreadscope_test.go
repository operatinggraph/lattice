package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

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

	rows, fp, err := scoped.ExecuteWithFootprint(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err)
	require.ElementsMatch(t, wantRows, rowKeys(t, rows, "taskKey"))

	require.NotContains(t, fp.EdgeRevisions, roleID,
		"a relation-scoped fingerprint is not comparable with a whole read's, so it must never be recorded")
	sel, ok := fp.EdgeSelectors[roleID]
	require.True(t, ok, "the hub must be footprinted by the selector the hop consulted")
	require.False(t, sel.Fallback)
	require.Len(t, sel.Matched, 1, "exactly one selector was consulted on the hub")

	matched := sel.Matched[ruleengine.EdgeSelector{RelType: "queuedFor", Direction: "in"}]
	require.Equal(t, map[string]struct{}{queued1: {}, queued2: {}}, matched,
		"the Matched set must be exactly the hub's queuedFor links")
	require.NotContains(t, matched, granted)
	for selector := range sel.Matched {
		require.NotEqual(t, "grantedBy", selector.RelType,
			"the relation the pattern never follows must not appear in the footprint at all")
	}

	// The way back: the same evaluation on the whole-node read.
	whole := New().WithHubReadScopeMode(HubReadScopeModeOff)
	wholeCR, err := whole.Parse(hubReadScopeQuery)
	require.NoError(t, err)

	wholeRows, wholeFP, err := whole.ExecuteWithFootprint(context.Background(), wholeCR, ec, adjKV, coreKV)
	require.NoError(t, err)
	require.ElementsMatch(t, wantRows, rowKeys(t, wholeRows, "taskKey"),
		"the scope must change what is read, never what the pattern binds")
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
	ex := newTestExecutor(adjKV, coreKV)
	require.True(t, ex.hubReadScope, "the default posture is on")

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

	ex := newTestExecutor(adjKV, coreKV)

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
	next := newTestExecutor(adjKV, coreKV)
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
