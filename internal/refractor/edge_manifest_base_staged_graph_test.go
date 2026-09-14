// The staged-vs-unstaged proof for the BASE edge-manifest read-grant producer,
// the widest lens the WITH-scope re-binding narrowing admits.
//
// edge_manifest_staff_staged_graph_test.go makes the same comparison on the
// five-walk staff producer. This one is the nine-walk base producer, and it is
// a strictly harder instance of the same claim: it re-opens ONE shared spine —
// `residesIn` + `containedIn*0..` — at five separate stages, and re-binds `tpl`
// across a staging boundary to hang `permitsOperation` off a service the
// previous stage already bound. Fourteen positions, thirteen relations, five
// re-opens of one ranged spine.
//
// The claim, stated as a comparison because prose cannot be run: the staged
// producer's pattern graph is the graph the same walks produce with NO boundary
// between them, plus duplicate hop records. Same positions, same labels, same
// anchor, same distances, and the same seed set for every relation either form
// can bind. If a staged re-open ever stopped landing on the position that
// already existed, this test sees a new position or a moved distance rather
// than a silently narrower derived anchor set.
package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// unstagedEdgeManifestReadGrants is edgeManifestReadGrants' nine walks written
// as ONE scope. Clause order is chosen so the two forms create their pattern
// positions in the same order — the builder numbers a position at its first
// sighting, so a twin that introduced them differently would compare unequal
// for a reason that says nothing about staging. Each clause the staged form
// re-emits per stage appears here exactly once; that collapse is the whole
// difference between the two sides.
//
// It is hand-written on purpose, and it is the only hand-written half: the
// staged side is read from the live package expansion, so a generator or
// package change moves the staged side and this twin is what fails to agree.
const unstagedEdgeManifestReadGrants = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
OPTIONAL MATCH (container)<-[:locatedAt]-(studio:studio)
OPTIONAL MATCH (studio)<-[:atStudio]-(sess:session)
OPTIONAL MATCH (container)<-[:practicesAt]-(prov:provider)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
OPTIONAL MATCH (identity)<-[:applicationFor]-(la:leaseapp)
OPTIONAL MATCH (la)<-[:openFor]-(tab:tab)
OPTIONAL MATCH (container)<-[:servedAt]-(item:menuitem)
RETURN
  identity.key AS actorKey,
  tpl.key AS a,
  op.key AS b,
  task.key AS c,
  inst.key AS d,
  sess.key AS e,
  prov.key AS f,
  bk.key AS g,
  tab.key AS h,
  item.key AS i
`

func TestEdgeManifestReadGrants_IndexesToItsUnstagedGraph(t *testing.T) {
	eng := full.New()
	index := func(spec string) full.HopIndex {
		t.Helper()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		return fullCR.AnchorHopIndex()
	}

	staged := index(stagedReadGrantProducerSpec(t, "edgeManifestReadGrants"))
	require.Truef(t, staged.Complete,
		"the shipped nine-walk producer must index — %q means the re-binding narrowing no longer reaches it", staged.Incomplete)

	unstaged := index(unstagedEdgeManifestReadGrants)
	require.Truef(t, unstaged.Complete, "the unstaged twin must index, or it pins nothing: %s", unstaged.Incomplete)

	require.Equal(t, unstaged.Labels, staged.Labels, "a staging boundary must create no position of its own")
	require.Equal(t, unstaged.LabelExpand, staged.LabelExpand)
	require.Equal(t, unstaged.Anchor, staged.Anchor)
	require.Equal(t, unstaged.Dist, staged.Dist,
		"Dist is computed from Hops, and the re-opened spine adds only hops that were already there")

	// The staged form re-walks the residence spine at five stages and re-binds
	// `tpl` across a boundary, so it must carry strictly more hop RECORDS while
	// carrying no hop the unstaged graph does not.
	require.Greater(t, len(staged.Hops), len(unstaged.Hops),
		"the shared spine really is re-emitted per stage, or this comparison pins nothing")
	require.ElementsMatch(t, uniquePatternHops(unstaged.Hops), uniquePatternHops(staged.Hops))

	// Seeds are what the pipeline acts on. Every relation either form can bind
	// is asked of both, including the ranged containment hop and the
	// `permitsOperation` hop that hangs off the re-bound `tpl`.
	for _, link := range []struct{ src, rel, dst string }{
		{"identity", "residesIn", ""},
		{"", "containedIn", ""},
		{"service", "availableAt", ""},
		{"service", "permitsOperation", "meta"},
		{"task", "assignedTo", "identity"},
		{"service", "providedTo", "identity"},
		{"studio", "locatedAt", ""},
		{"session", "atStudio", "studio"},
		{"provider", "practicesAt", ""},
		{"booking", "bookedBy", "identity"},
		{"leaseapp", "applicationFor", "identity"},
		{"tab", "openFor", "leaseapp"},
		{"menuitem", "servedAt", ""},
	} {
		want := uniqueSeedSet(unstaged.AnchorSideSeeds(link.src, link.rel, link.dst))
		got := uniqueSeedSet(staged.AnchorSideSeeds(link.src, link.rel, link.dst))
		require.NotEmptyf(t, want, "the unstaged twin must seed `%s`, or this row pins nothing", link.rel)
		require.ElementsMatchf(t, want, got, "staging moved the seeds for `%s`", link.rel)
	}
}

// TestEdgeManifestReadGrants_WithScopeVerdictConsumersArePinned pins the OTHER
// answers the WITH-scope verdict gates.
//
// `withScopeVerdict == ""` is not only the hop index's conjunct. It is also the
// precondition for keyColumnShape (anchor_delete.go) — and so for
// AnchorProjectionKey, AnchorDeleteResult, HasAnchorOnlyKeyColumns,
// ProjectsOneRowPerAnchor and PartitionsByAnchor — and for
// ExistenceDependsOnNeighbour (ast.go). Admitting a re-binding therefore moves
// this lens from "refused" to "answered" at all of them at once, and nothing
// downstream would notice: every consumer that reads them bails earlier for an
// actor-aggregate lens with an enumerator installed.
//
// So the answers are pinned here rather than left to be discovered. Each is
// asserted with the fact that makes it TRUE for this cypher, not merely with
// the verdict, so a future widening that makes one of them wrong reds here
// instead of shipping.
func TestEdgeManifestReadGrants_WithScopeVerdictConsumersArePinned(t *testing.T) {
	eng := full.New()
	spec := stagedReadGrantProducerSpec(t, "edgeManifestReadGrants")
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)

	// The precondition. Without it every assertion below holds for the boring
	// reason — the verdict refused — and pins nothing.
	require.Truef(t, fullCR.AnchorHopIndex().Complete,
		"these answers are only reachable because the WITH scope is admitted: %s", fullCR.AnchorHopIndex().Incomplete)

	// ExistenceDependsOnNeighbour: no row here can be dropped by a neighbour.
	// The producer opens on ONE required MATCH — the anchor itself — and every
	// other clause is an OPTIONAL MATCH, whose failed pattern restores nulls
	// rather than removing the row; no clause carries a WHERE at all. So the
	// answer is exhaustive and negative on the cypher's own structure, and the
	// re-opened spine changes neither half of that.
	depends, reasons, exhaustive := fullCR.ExistenceDependsOnNeighbour()
	require.True(t, exhaustive, "the WITH scope is admitted, so the walk can answer")
	require.False(t, depends, "reasons: %v", reasons)
	require.Empty(t, reasons)

	// The key-column shape. The RETURN's first item is `identity.key AS
	// actorKey` and the lens threads no Into.Key, so the legacy first-item path
	// is the production path: one key column, resolving through every boundary
	// to the ANCHOR's own binding — `identity` is carried under its own name by
	// every staging WITH and re-bound by none of them.
	require.True(t, fullCR.HasAnchorOnlyKeyColumns())
	require.True(t, fullCR.ProjectsOneRowPerAnchor())

	const anchorKey = "vtx.identity.AAAAAAAAAAAAAAAAAAAA"
	keys, ok := eng.AnchorProjectionKey(cr, anchorKey, "identity", nil)
	require.True(t, ok, "the anchor's key column must resolve read-free from the anchor binding")
	require.Equal(t, map[string]any{"actorKey": anchorKey}, keys)

	// A different vertex type is not this rule's anchor, so no key is derivable
	// for it — the half of the ok contract that keeps a widened structural
	// answer from becoming a Delete on somebody else's row.
	_, ok = eng.AnchorProjectionKey(cr, "vtx.service.AAAAAAAAAAAAAAAAAAAA", "tpl", nil)
	require.False(t, ok)

	// PartitionsByAnchor stays REFUSED, and on a conjunct the narrowing does not
	// touch: the anchor pattern is pinned by its own `{key: $actorKey}`
	// (anchorPatternIsKeyed), so there is no partition to take. This is the row
	// that keeps ruleinstall.go's behaviour unchanged for this lens.
	identifying, partitions := fullCR.PartitionsByAnchor()
	require.False(t, partitions)
	require.Nil(t, identifying)
}
