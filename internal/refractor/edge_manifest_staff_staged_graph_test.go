// The staged-vs-unstaged proof for the lens the WITH-scope re-binding narrowing
// was built for — varlength-anchor-derivation-design.md's Increment 2.
//
// internal/refractor/ruleengine/full's
// TestAnchorHopIndex_GeneratedProducerIndexesToTheUnstagedGraph makes the same
// comparison on a TWO-walk fixture, which is what the generator's own golden
// emits. The lens the increment actually converts is a FIVE-walk producer whose
// stages re-open two different shared spines — `holdsRole` three times and
// `worksAt` + `containedIn*0..` twice — so it exercises the re-binding on a
// graph with ten positions, several duplicate hops, and a ranged hop, none of
// which the two-walk fixture reaches. This file holds it to the same claim, on
// the real emitted cypher rather than a fixture.
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
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// unstagedEdgeManifestStaffReadGrants is edgeManifestStaffReadGrants' five
// walks written as ONE scope. Clause order is chosen so the two forms create
// their pattern positions in the same order — the builder numbers a position at
// its first sighting, so a twin that introduced them differently would compare
// unequal for a reason that says nothing about staging.
//
// It is hand-written on purpose, and it is the only hand-written half: the
// staged side is read from the live package expansion below, so a generator or
// package change moves the staged side and this twin is what fails to agree.
const unstagedEdgeManifestStaffReadGrants = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)
OPTIONAL MATCH (role)<-[:queuedFor]-(task:task)
OPTIONAL MATCH (identity)-[:worksAt]->(work)
OPTIONAL MATCH (work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(studio:studio)
OPTIONAL MATCH (role)<-[:offeredTo]-(pane:meta)
OPTIONAL MATCH (work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo:workorder)
RETURN
  identity.key AS actorKey,
  op.key AS a,
  task.key AS b,
  studio.key AS c,
  pane.key AS d,
  wo.key AS e
`

// stagedReadGrantProducerSpec returns the cypher pkgmgr really emits for one of
// edge-manifest's generated read-grant producers, off the package definition
// rather than off a copy.
func stagedReadGrantProducerSpec(t *testing.T, canonicalName string) string {
	t.Helper()
	expanded, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "edge-manifest's read-grant walks must compose")
	for _, l := range expanded.Lenses {
		if l.CanonicalName == canonicalName {
			require.NotEmpty(t, l.Spec, "%s must carry a generated cypher", canonicalName)
			return l.Spec
		}
	}
	t.Fatalf("edge-manifest ships no lens named %q", canonicalName)
	return ""
}

func TestEdgeManifestStaffReadGrants_IndexesToItsUnstagedGraph(t *testing.T) {
	eng := full.New()
	index := func(spec string) full.HopIndex {
		t.Helper()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		return fullCR.AnchorHopIndex()
	}

	staged := index(stagedReadGrantProducerSpec(t, "edgeManifestStaffReadGrants"))
	require.Truef(t, staged.Complete,
		"the shipped five-walk producer must index — %q means the re-binding narrowing no longer reaches it", staged.Incomplete)

	unstaged := index(unstagedEdgeManifestStaffReadGrants)
	require.Truef(t, unstaged.Complete, "the unstaged twin must index, or it pins nothing: %s", unstaged.Incomplete)

	require.Equal(t, unstaged.Labels, staged.Labels, "a staging boundary must create no position of its own")
	require.Equal(t, unstaged.LabelExpand, staged.LabelExpand)
	require.Equal(t, unstaged.Anchor, staged.Anchor)
	require.Equal(t, unstaged.Dist, staged.Dist,
		"Dist is computed from Hops, and the re-opened spines add only hops that were already there")

	// The staged form re-walks `holdsRole` at three stages and the
	// worksAt/containedIn spine at two, so it must carry strictly more hop
	// RECORDS while carrying no hop the unstaged graph does not.
	require.Greater(t, len(staged.Hops), len(unstaged.Hops),
		"the shared spines really are re-emitted per stage, or this comparison pins nothing")
	require.ElementsMatch(t, uniquePatternHops(unstaged.Hops), uniquePatternHops(staged.Hops))

	// Seeds are what the pipeline acts on. Every relation either form can bind
	// is asked of both, including the ranged containment hop and the two
	// relations that reach a `meta` from different sides.
	for _, link := range []struct{ src, rel, dst string }{
		{"identity", "holdsRole", "role"},
		{"permission", "grantedBy", "role"},
		{"permission", "forOperation", "meta"},
		{"task", "queuedFor", "role"},
		{"identity", "worksAt", ""},
		{"", "containedIn", ""},
		{"studio", "locatedAt", ""},
		{"workorder", "locatedAt", ""},
		{"meta", "offeredTo", "role"},
	} {
		want := uniqueSeedSet(unstaged.AnchorSideSeeds(link.src, link.rel, link.dst))
		got := uniqueSeedSet(staged.AnchorSideSeeds(link.src, link.rel, link.dst))
		require.NotEmptyf(t, want, "the unstaged twin must seed `%s`, or this row pins nothing", link.rel)
		require.ElementsMatchf(t, want, got, "staging moved the seeds for `%s`", link.rel)
	}
}

// TestEdgeManifestStaffReadGrants_WithScopeVerdictConsumersArePinned pins the
// OTHER answers the WITH-scope verdict gates.
//
// `withScopeVerdict == ""` is not only the hop index's conjunct. It is also the
// precondition for keyColumnShape (anchor_delete.go) — and so for
// AnchorProjectionKey, AnchorDeleteResult, HasAnchorOnlyKeyColumns,
// ProjectsOneRowPerAnchor and PartitionsByAnchor — and for
// ExistenceDependsOnNeighbour (ast.go). Admitting a re-binding therefore moved
// this lens from "refused" to "answered" at all of them at once, and nothing
// downstream would have noticed: every consumer that reads them bails earlier
// for an actor-aggregate lens with an enumerator installed.
//
// So the answers are pinned here rather than left to be discovered. Each is
// asserted with the fact that makes it TRUE for this cypher, not merely with
// the verdict, so a future widening that makes one of them wrong reds here
// instead of shipping.
func TestEdgeManifestStaffReadGrants_WithScopeVerdictConsumersArePinned(t *testing.T) {
	eng := full.New()
	spec := stagedReadGrantProducerSpec(t, "edgeManifestStaffReadGrants")
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
	// re-opened chains change neither half of that.
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
	_, ok = eng.AnchorProjectionKey(cr, "vtx.role.AAAAAAAAAAAAAAAAAAAA", "role", nil)
	require.False(t, ok)

	// PartitionsByAnchor stays REFUSED, and on a conjunct the narrowing does not
	// touch: the anchor pattern is pinned by its own `{key: $actorKey}`
	// (anchorPatternIsKeyed), so there is no partition to take. This is the row
	// that keeps ruleinstall.go's behaviour unchanged for this lens.
	identifying, partitions := fullCR.PartitionsByAnchor()
	require.False(t, partitions)
	require.Nil(t, identifying)
}

func uniquePatternHops(hops []full.PatternHop) []full.PatternHop {
	seen := map[full.PatternHop]struct{}{}
	out := make([]full.PatternHop, 0, len(hops))
	for _, h := range hops {
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

func uniqueSeedSet(seeds []full.Seed) []full.Seed {
	seen := map[full.Seed]struct{}{}
	out := make([]full.Seed, 0, len(seeds))
	for _, s := range seeds {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
