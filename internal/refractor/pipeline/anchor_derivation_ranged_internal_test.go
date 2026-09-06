package pipeline

// The ranged (variable-length) step in the pattern-directed affected-anchor
// derivation — varlength-anchor-derivation-design.md §14's test table for
// Increment 1.
//
// Every fixture here is SYNTHETIC and hand-built, and §9 says why that is not a
// preference: the shipped corpus cannot exercise the worst defect a ranged step
// can carry. Sixteen of the seventeen ranged-hop instances in the corpus have an
// unlabeled far end, so nothing prunes; the seventeenth has both ends on the
// same expanded location set with intermediates that are all locations. A
// corpus-driven test would pass with the intermediate-prune defect in. So the
// fixtures below differ from the corpus deliberately — a ranged hop whose
// terminal label differs from its intermediates' types, a chain longer than the
// executor's own clamp, a graph shaped to make one adjacency read the difference
// between an answer and a fallback.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// generatedReadGrantProducerSpec returns the cypher pkgmgr emits for one of
// edge-manifest's generated read-grant producers, read off the package
// definition rather than copied. The Increment 2 lens is generated, so a copy
// here would go on pinning an emission nobody ships the moment the generator or
// the package's walk list changes.
func generatedReadGrantProducerSpec(t *testing.T, canonicalName string) string {
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

// rangedResidenceSpec is capabilityServiceAccess's positive arm reduced to its
// steppable skeleton: a fixed hop in, a ranged containment walk, a fixed hop
// out. Concrete labels rather than `:location*` so the shape under test is the
// RANGE and not the taxonomy expansion, which has its own tests
// (anchor_derivation_taxonomy_internal_test.go) and its own vector below (T5).
const rangedResidenceSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*0..]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

// fixedResidenceSpec is rangedResidenceSpec with the range removed, and it is
// what makes every ranged assertion below a claim about the RANGE rather than
// about the fixture: on the identical graph this pattern reaches only the first
// containment level, so a chain deeper than one hop derives nothing at all.
const fixedResidenceSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

// residenceChain wires alice into a containment chain `depth` links deep and
// hangs one service off the top of it, returning the fixture. Names are
// `l1`..`l<depth+1>`, so `l1` is the residence and the service is availableAt
// the last one.
func residenceChain(t *testing.T, adjKV *substrate.KV, depth int) *enumFixture {
	t.Helper()
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("l1", "location")
	f.edge("residesIn", "alice", "l1")
	for i := 1; i <= depth; i++ {
		f.vertex(fmt.Sprintf("l%d", i+1), "location")
		f.edge("containedIn", fmt.Sprintf("l%d", i), fmt.Sprintf("l%d", i+1))
	}
	f.vertex("svc", "service")
	f.edge("availableAt", "svc", fmt.Sprintf("l%d", depth+1))
	return f
}

// TestDeriveAnchors_RangedHopReachesAnchorsAcrossIt is T1: a pattern carrying a
// ranged hop is indexable, and the walk crosses that hop — a service availableAt
// the TOP of a three-deep containment chain derives the actor living at the
// bottom of it.
//
// The derived set is asserted equal to the ActorEnumerator BFS's, which is the
// answer this arm substitutes for. Equality is the right claim here and not a
// weaker "non-empty": the derivation's licence is that its set is a SUPERSET of
// the truly-affected anchors, and on this shape the BFS is that truth.
func TestDeriveAnchors_RangedHopReachesAnchorsAcrossIt(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := residenceChain(t, adjKV, 3)
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, rangedResidenceSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete,
		"a pattern carrying a ranged hop is indexable: %s", rs.anchorHops.Incomplete)

	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)

	bfs, err := p.actorEnumerator.Enumerate(ctx, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, bfs,
		"the shipped BFS reaches alice across the chain, so it is the truth to match")
	require.ElementsMatch(t, bfs, derived,
		"the derived set must equal what the walk replaces — a three-deep chain is crossed, not skipped")

	// The vector that makes the assertion above about the RANGE: the identical
	// graph under a single fixed containedIn hop stops one level up and derives
	// nothing, while the BFS still reaches alice. That gap is exactly what the
	// ranged step closes.
	fixedP := derivationPipeline(t, adjKV, fixedResidenceSpec)
	fixedDerived, ok, err := fixedP.deriveAnchorsForVertex(ctx, fixedP.ruleState(), f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, fixedDerived,
		"one fixed hop cannot span a three-deep chain — so the ranged answer above is the range's doing")
}

// TestDeriveAnchors_RangedHopZeroLengthAdmitsTheStandingNode is T2. `*0..`
// binds its far position to the standing node itself, crossing no edge, and the
// walk has to make that admission or a service availableAt the actor's OWN
// residence derives nobody.
//
// The `*1..` vector is the positive proof that the depth-0 admission is what
// carries the answer: same graph, same lens, one changed bound, no anchor.
func TestDeriveAnchors_RangedHopZeroLengthAdmitsTheStandingNode(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("home", "location")
	f.vertex("svc", "service")
	f.edge("residesIn", "alice", "home")
	f.edge("availableAt", "svc", "home")
	ctx := context.Background()

	const zeroHop = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*0..2]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`
	const oneHopMinimum = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*1..2]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

	p := derivationPipeline(t, adjKV, zeroHop)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"`*0..2` admits the residence itself, so a service available there reaches its resident")

	minOne := derivationPipeline(t, adjKV, oneHopMinimum)
	require.True(t, minOne.ruleState().anchorHops.Complete)
	derived, ok, err = minOne.deriveAnchorsForVertex(ctx, minOne.ruleState(), f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, derived,
		"`*1..2` crosses at least one containedIn edge, and there is none — so the answer above IS the depth-0 admission")
}

// mixedTypeChainSpec is the fixture §9 says the corpus cannot supply: a ranged
// hop whose TERMINAL label (`building`) differs from every intermediate type it
// crosses (`room`, `floor`). A walk that applied the terminal label to
// intermediates would stop at the first one — dropping paths the executor walks,
// which drops anchors, which drops a revocation.
const mixedTypeChainSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*1..3]->(loc:building)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

// TestDeriveAnchors_RangedIntermediatesAreNotPrunedByTheTerminalLabel is T3.
//
// The two halves are a pair and neither means anything alone. First the label
// prune is shown to be a LIVE predicate on this very step — it rejects both
// intermediate types and accepts the terminal one — so the walk's success below
// cannot be explained by a prune that never fires. Then the walk is run and must
// reach the actor across three intermediates the prune would have rejected.
//
// §14 records this as a MUTATION proof, and the mutation is the one line that
// would introduce the defect: applying stepAdmitsFarEnd to the frontier
// extension in expandRanged, not only to its admission. Planted in a throwaway
// worktree, it turns this test red at the derived-set assertion.
// mixedTypeChain builds mixedTypeChainSpec's graph: alice lives in `home`, which
// sits inside a room, inside a floor, inside the building a service is
// availableAt. Only `home` and `b1` carry a type the pattern names; the two
// intermediates do not.
func mixedTypeChain(t *testing.T, adjKV *substrate.KV) *enumFixture {
	t.Helper()
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("home", "location")
	f.vertex("r1", "room")
	f.vertex("f1", "floor")
	f.vertex("b1", "building")
	f.vertex("svc", "service")
	f.edge("residesIn", "alice", "home")
	f.edge("containedIn", "home", "r1")
	f.edge("containedIn", "r1", "f1")
	f.edge("containedIn", "f1", "b1")
	f.edge("availableAt", "svc", "b1")
	return f
}

func TestDeriveAnchors_RangedIntermediatesAreNotPrunedByTheTerminalLabel(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := mixedTypeChain(t, adjKV)
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, mixedTypeChainSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)

	// Half one: the prune is live on this step and really would reject the
	// intermediates. Read off the RUNNING index rather than restated, so a
	// pattern edit that made the labels agree cannot leave this test passing
	// while proving nothing.
	var buildingPos = -1
	for i, l := range rs.anchorHops.Labels {
		if l == "building" {
			buildingPos = i
		}
	}
	require.GreaterOrEqual(t, buildingPos, 0, "the fixture's terminal position must be the labeled one")
	var backStep full.PatternStep
	for _, s := range rs.anchorHops.StepsFrom(buildingPos) {
		if s.Rel == "containedIn" {
			backStep = s
		}
	}
	require.Equal(t, "containedIn", backStep.Rel, "the ranged step back down the chain must exist")
	require.Equal(t, "location", backStep.ToLabel)
	require.Equal(t, 1, backStep.Min)
	require.Equal(t, 3, backStep.Max)
	require.False(t, stepAdmitsFarEnd(backStep, "room"),
		"the prune is live: a room is not admissible at this step's far end")
	require.False(t, stepAdmitsFarEnd(backStep, "floor"))
	require.True(t, stepAdmitsFarEnd(backStep, "location"),
		"and it does admit the terminal type, so it is a discriminating predicate rather than a closed door")

	// Half two: the walk crosses those same two intermediates anyway, because
	// the prune runs at ADMISSION only. traverseRel's nodeMatches sits in the
	// same place, which is why this is completeness with respect to the
	// executor rather than leniency.
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"the frontier extends through a room and a floor the terminal label rejects; pruning them drops the actor")

	// And the link event on the same edge, which is the shape a revocation
	// actually arrives as: an availableAt tombstone must reach the same actor.
	derived, ok, err = p.deriveAnchorsForLink(ctx, rs, f.link("availableAt", "svc", "service", "b1", "building"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, derived, f.key("alice", "identity"),
		"a revocation event must derive the actor it revokes from")
}

// clampChainSpec puts a ranged hop between two positions with DIFFERENT labels,
// so the walk's admission at each end is decided by a discriminating type and
// the reachable set is the chain itself rather than the chain's own symmetric
// closure. That is what makes a boundary at exactly maxVarLengthHops observable.
const clampChainSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:unit)-[:containedIn*0..]->(loc:location)
RETURN identity.key AS actorKey, loc.key AS lk
`

// TestDeriveAnchors_OpenRangeWalksExactlyTheExecutorsClamp is T4's walk half:
// the clamp is applied PER HOP and is the executor's own, so an open `*0..`
// walks to exactly maxVarLengthHops and stops — not to nine, and not forever.
//
// The pair is the whole test. A chain node exactly maxVarLengthHops from the
// residence derives the actor; the very next one along does not. Either
// assertion alone would survive an off-by-one in the other direction.
func TestDeriveAnchors_OpenRangeWalksExactlyTheExecutorsClamp(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("home", "unit")
	f.edge("residesIn", "alice", "home")
	// A chain two links longer than the clamp, so where the walk stops is a
	// measurement rather than the end of the fixture.
	prev := "home"
	for i := 1; i <= DefaultActorMaxDepth+2; i++ {
		name := fmt.Sprintf("c%d", i)
		f.vertex(name, "location")
		f.edge("containedIn", prev, name)
		prev = name
	}
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, clampChainSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)

	// The boundary this test walks to is the INDEX's own stored bound, asserted
	// against DefaultActorMaxDepth — which documents itself as mirroring the
	// executor's variable-length cap. Without this the two could drift and the
	// fixture below would quietly measure the wrong boundary.
	for _, h := range rs.anchorHops.Hops {
		if h.Rel == "containedIn" {
			require.Equal(t, DefaultActorMaxDepth, h.Max,
				"the stored clamp and the enumerator's mirror of it must still agree")
		}
	}

	atClamp := fmt.Sprintf("c%d", DefaultActorMaxDepth)
	pastClamp := fmt.Sprintf("c%d", DefaultActorMaxDepth+1)

	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key(atClamp, "location"), "location")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"a node exactly maxVarLengthHops up the chain still reaches the residence, so the actor derives")

	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, f.key(pastClamp, "location"), "location")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, derived,
		"one link further and the residence is out of the executor's own reach, so no row of this actor's can change")
}

// twelveHopSpec is §4.2's second prohibition made concrete: residesIn (1 graph
// hop) + containedIn (up to maxVarLengthHops) + availableAt (1 more) is a
// TWELVE-graph-hop pattern. A whole-walk depth budget of ten — the shape the
// triage originally proposed — would under-approximate it.
const twelveHopSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:unit)-[:containedIn*0..]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

// TestDeriveAnchors_TwelveGraphHopPatternStillDerives is T4's per-hop half: the
// bound belongs to the ranged HOP, so a pattern that chains a fixed hop, a
// ten-hop range and another fixed hop spans twelve graph hops and still derives.
func TestDeriveAnchors_TwelveGraphHopPatternStillDerives(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("home", "unit")
	f.edge("residesIn", "alice", "home")
	prev := "home"
	for i := 1; i <= DefaultActorMaxDepth; i++ {
		name := fmt.Sprintf("c%d", i)
		f.vertex(name, "location")
		f.edge("containedIn", prev, name)
		prev = name
	}
	f.vertex("svc", "service")
	f.edge("availableAt", "svc", prev)
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, twelveHopSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)

	// The ranged hop carries its OWN bound; nothing about the two fixed hops on
	// either side of it reduces that bound.
	var ranged full.PatternHop
	for _, h := range rs.anchorHops.Hops {
		if h.Rel == "containedIn" {
			ranged = h
		}
	}
	require.Equal(t, "containedIn", ranged.Rel)
	require.Equal(t, DefaultActorMaxDepth, ranged.Max,
		"the range's own clamp, unreduced by the fixed hops flanking it")

	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"twelve graph hops: availableAt + ten containedIn + residesIn — a global depth-10 budget would lose the actor here")
}

// TestDeriveAnchors_RangedReadCapFallsBackRatherThanTruncating is T6, and it
// guards the one mutation that silently breaks the superset invariant: swallow
// the read cap inside the ranged closure and return the anchors found so far
// with ok == true.
//
// The budget is what is moved, never the graph, so the test states the property
// instead of approximating it with thousands of vertices.
//
// The fixture is the MIXED-TYPE chain rather than a plain one, and that choice
// is load-bearing. On a same-typed chain every node the closure reads is also
// admitted, so the outer step loop reads it again and the outer loop's own cap
// check refuses whatever the closure did — which would mask the defect this test
// exists to catch. Here the room and the floor are read by the closure's frontier
// and by nothing else, so a budget that runs out on one of them can only be
// noticed inside the closure.
//
// The walk costs five adjacency documents: the service, the building, the floor,
// the room, and the residence. Five is exactly enough; four runs out in the outer
// step loop; three runs out inside the ranged closure, before it has admitted
// anything at all. All three answers are asserted, because "wherever the budget
// runs out, the answer is a refusal" is the actual contract.
//
// §14 records this as a MUTATION proof. The mutation is in expandRanged's read:
// return the partial frontier instead of propagating errDerivationTooWide.
// Planted in a throwaway worktree it turns the cap-of-three assertion red,
// because the walk then answers with an EMPTY set and ok == true — and an empty
// set on a complete index is read by the caller as a licensed skip, here of
// alice's own revocation.
func TestDeriveAnchors_RangedReadCapFallsBackRatherThanTruncating(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := mixedTypeChain(t, adjKV)
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, mixedTypeChainSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)
	svcKey := f.key("svc", "service")
	aliceKey := f.key("alice", "identity")

	p.SetAnchorDerivationReadCap(5)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, svcKey, "service")
	require.NoError(t, err)
	require.True(t, ok, "five adjacency documents are enough for this walk")
	require.ElementsMatch(t, []string{aliceKey}, derived)

	p.SetAnchorDerivationReadCap(4)
	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, svcKey, "service")
	require.NoError(t, err)
	require.False(t, ok, "a budget exhausted in the outer step loop refuses")
	require.Nil(t, derived)

	p.SetAnchorDerivationReadCap(3)
	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, svcKey, "service")
	require.NoError(t, err)
	require.False(t, ok,
		"an exhausted budget inside the ranged closure must fall back too, never return the partial set")
	require.Nil(t, derived,
		"a refusal carries no anchors at all — a truncated set is indistinguishable from a complete one")

	// What the refusal buys, which is the reason it must be a refusal: ok ==
	// false routes the caller to the BFS, and the BFS still finds the anchor the
	// truncated walk had not yet reached. Truncating would have returned the
	// empty set with ok == true, and an empty set on a complete index is read as
	// a licensed skip — here, of alice's own revocation.
	bfs, err := p.actorEnumerator.Enumerate(ctx, svcKey, "service")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{aliceKey}, bfs,
		"the fallback answer is complete, so falling back loses nothing a truncation would have dropped")

	// The counter that makes the cap's firing rate legible has to move on this
	// exit too — the read-cap exit is precisely the one worth seeing.
	require.Positive(t, p.AnchorDerivationShadow().RangedClosureReads,
		"a walk that exits through the read cap still reports what its ranged closures read")
}

// TestDeriveAnchors_RangedWalkHoldsNothingAcrossEvents is T11. The design's
// claim is that the increment introduces NO state — no registry, no cache, no
// latch — and that absence is itself the claim, so it is pinned rather than
// assumed. The ranged closure's frontier guard is keyed by (vertex, hop) and
// dies with the call; the walk's own visited set dies with the walk.
//
// Two disjoint residence graphs live in ONE adjacency bucket and one pipeline
// answers for both, in sequence. Anything the walk retained would show up as the
// first graph's actor appearing in the second graph's answer.
func TestDeriveAnchors_RangedWalkHoldsNothingAcrossEvents(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	// Graph A: alice, two containment levels, one service.
	f.vertex("alice", "identity")
	f.vertex("a1", "location")
	f.vertex("a2", "location")
	f.vertex("svcA", "service")
	f.edge("residesIn", "alice", "a1")
	f.edge("containedIn", "a1", "a2")
	f.edge("availableAt", "svcA", "a2")
	// Graph B: bob, disjoint from A in every vertex and every edge.
	f.vertex("bob", "identity")
	f.vertex("b1", "location")
	f.vertex("b2", "location")
	f.vertex("svcB", "service")
	f.edge("residesIn", "bob", "b1")
	f.edge("containedIn", "b1", "b2")
	f.edge("availableAt", "svcB", "b2")

	ctx := context.Background()
	p := derivationPipeline(t, adjKV, rangedResidenceSpec)
	rs := p.ruleState()
	aliceKey, bobKey := f.key("alice", "identity"), f.key("bob", "identity")

	derive := func(svc string) []string {
		t.Helper()
		got, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key(svc, "service"), "service")
		require.NoError(t, err)
		require.True(t, ok)
		return got
	}

	first := derive("svcA")
	require.ElementsMatch(t, []string{aliceKey}, first)

	second := derive("svcB")
	require.ElementsMatch(t, []string{bobKey}, second,
		"the second event's answer carries nothing from the first walk's frontier")
	require.NotContains(t, second, aliceKey)

	// And back again, on the same pipeline: a walk that accumulated would grow,
	// so the repeat must be byte-identical to the first answer rather than
	// merely non-empty.
	require.ElementsMatch(t, first, derive("svcA"),
		"re-running the first event after the second reproduces the first answer exactly")
}

// shippedCapabilityServiceAccess is the live cypher of the one anchored lens
// Increment 1 converts (packages/service-location/lenses.go's
// capabilityServiceAccessSpec), copied verbatim. It carries TWO ranged
// containment walks: the positive one that grants a service, and the exclusion
// one inside `NOT (...)` that revokes it.
const shippedCapabilityServiceAccess = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location*)-[:containedIn*0..]->(loc:location*)<-[:availableAt]-(svc:service)
WHERE NOT (svc)-[:instanceOf]->(svcTpl:service)
  AND NOT (loc0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    service: svc.key,
    resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) WHERE op.data.operationType <> null | {operationType: op.data.operationType}]
  }) AS serviceAccess
`

// serviceAccessWithoutExclusion is shippedCapabilityServiceAccess with the
// exclusion conjunct removed, and nothing else. It exists to make the test
// below a claim about the NEGATED arm rather than about the fixture.
const serviceAccessWithoutExclusion = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location*)-[:containedIn*0..]->(loc:location*)<-[:availableAt]-(svc:service)
WHERE NOT (svc)-[:instanceOf]->(svcTpl:service)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    service: svc.key,
    resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) WHERE op.data.operationType <> null | {operationType: op.data.operationType}]
  }) AS serviceAccess
`

// TestDeriveAnchors_NegatedRangedArmDerivesTheActor is T5, and its polarity is
// the reason it is not just another ranged-hop vector. capabilityServiceAccess
// writes `cap.svc.<actor>`, a live authorization surface. Its exclusion walk —
// `NOT (loc0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)` — is what
// REVOKES a service at a place an operator marked unavailable. A derivation that
// misses an event on that walk leaves an excluded service granted, which is an
// over-grant rather than a stale read.
//
// `exLoc` is deliberately UNLABELLED in the shipped lens (its own comment says
// why: a label on a negated position removes exclusions, i.e. grants access), so
// it is the only position a vertex outside the location taxonomy can bind. The
// fixture uses exactly that: a `zone` sits on the residence's containment chain
// and carries the unavailableAt, so it is reachable ONLY through the exclusion.
func TestDeriveAnchors_NegatedRangedArmDerivesTheActor(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("home", "unit")
	f.vertex("tower", "building")
	f.vertex("zone", "zone")
	f.vertex("laundry", "service")
	f.edge("residesIn", "alice", "home")
	f.edge("containedIn", "home", "tower")
	f.edge("containedIn", "home", "zone")
	f.edge("availableAt", "laundry", "tower")
	f.edge("unavailableAt", "laundry", "zone")
	ctx := context.Background()

	p := derivationPipelineWithTaxonomy(t, adjKV, shippedCapabilityServiceAccess, armedLocationResolver())
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete,
		"the shipped lens must index now that its ranged hops are steppable: %s", rs.anchorHops.Incomplete)
	require.Equal(t, -1, rs.anchorHops.UnresolvedExpansionPosition(),
		"the `:location*` positions must be resolved, or the walk would be declined before it started")

	// The fixture's premise, asserted rather than assumed: a `zone` is outside
	// the location taxonomy, so it binds no POSITIVE, labelled position at all.
	// The only positions admitting it are the unlabelled ones — `exLoc` inside
	// the exclusion, and the comprehension's `op`, which the fixture wires no
	// permitsOperation edge to and which is therefore a dead end for this walk.
	zonePositions := rs.anchorHops.PositionsBinding("zone")
	require.NotEmpty(t, zonePositions)
	for _, pos := range zonePositions {
		require.Empty(t, rs.anchorHops.Labels[pos],
			"a zone may bind only the deliberately unlabelled positions, or 'reachable only through the "+
				"exclusion' is not what is being tested")
	}

	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("zone", "zone"), "zone")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"an event on the exclusion's own node must derive the actor — missing it leaves an excluded service granted")

	// The link event is the shape an exclusion actually arrives as: wiring or
	// tombstoning the unavailableAt itself.
	derived, ok, err = p.deriveAnchorsForLink(ctx, rs,
		f.link("unavailableAt", "laundry", "service", "zone", "zone"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, derived, f.key("alice", "identity"),
		"the unavailableAt link event carries the revocation, so it must reach the actor it revokes from")

	// The vector that makes all of the above about the NEGATED arm: with the
	// exclusion conjunct removed and nothing else changed, the same zone event on
	// the same graph reaches no actor at all.
	noExcl := derivationPipelineWithTaxonomy(t, adjKV, serviceAccessWithoutExclusion, armedLocationResolver())
	require.True(t, noExcl.ruleState().anchorHops.Complete, "%s", noExcl.ruleState().anchorHops.Incomplete)
	derived, ok, err = noExcl.deriveAnchorsForVertex(ctx, noExcl.ruleState(), f.key("zone", "zone"), "zone")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, derived,
		"without the exclusion arm the zone reaches nobody, so the derivations above are the exclusion walk's doing")
}

// The four plain lenses Increment 1 converts, copied verbatim from their
// packages so a cypher edit that changes what the derivation can do to them
// shows up here rather than as a silent behaviour change in a running stack.
const (
	// packages/cafe-domain/lenses.go — cafeLeaseWorkplaces
	shippedCafeLeaseWorkplaces = `MATCH (l:leaseapp)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  [(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

	// packages/cafe-domain/lenses.go — menuCatalog
	shippedMenuCatalog = `MATCH (m:menuitem)
OPTIONAL MATCH (m)-[:servedAt]->(loc)
RETURN
  m.key AS key,
  m.key AS menuItemKey,
  m.price.data.name AS name,
  m.price.data.priceCents AS priceCents,
  loc.key AS servedAt,
  (loc.key = null) AS missingLocation,
  [(m)-[:servedAt]->(sloc)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

	// packages/wellness-domain/lenses.go — wellnessMembers
	shippedWellnessMembers = `MATCH (l:leaseapp)
MATCH (l)-[:applicationFor]->(id:identity)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  id.key AS bookerKey,
  l.decision.data.value AS landlordDecision,
  [(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

	// packages/wellness-domain/lenses.go — wellnessSessions
	shippedWellnessSessions = `MATCH (se:session)
OPTIONAL MATCH (se)-[:atStudio]->(s:studio)
OPTIONAL MATCH (se)-[:ledBy]->(i:instructor)
RETURN
  se.key AS key,
  se.key AS sessionKey,
  se.schedule.data.name AS name,
  se.schedule.data.startsAt AS startsAt,
  se.schedule.data.endsAt AS endsAt,
  se.schedule.data.capacity AS capacity,
  se.schedule.data.priceCents AS priceCents,
  se.schedule.data.residentPriceCents AS residentPriceCents,
  s.key AS studioKey,
  s.profile.data.name AS studioName,
  i.key AS instructorKey,
  i.profile.data.displayName AS instructorName,
  [(s)-[:locatedAt]->(pl)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations,
  (s.key = null) AS missingStudio`
)

// convertingPlainLenses is that same four, with the anchor type each one's
// scan root binds.
var convertingPlainLenses = []struct {
	name     string
	rootType string
	spec     string
}{
	{"cafeLeaseWorkplaces", "leaseapp", shippedCafeLeaseWorkplaces},
	{"menuCatalog", "menuitem", shippedMenuCatalog},
	{"wellnessMembers", "leaseapp", shippedWellnessMembers},
	{"wellnessSessions", "session", shippedWellnessSessions},
}

// TestSeedMultiPosition_FlipsForEveryConvertingPlainLens is T7's first half,
// asserted per lens rather than over a loop's aggregate so a future cypher edit
// names which lens moved.
//
// Why each of the four is on that path is the same story every time, and it is
// worth stating rather than leaving to the verdict: each carries its containment
// walk inside a RETURN pattern comprehension whose positions are UNLABELED, and
// an unlabeled position admits any type. So on a scan-root index that can answer
// for these lenses at all, the lens's own anchor type binds at those
// comprehension positions as well as at the root — and a single engine-level
// seed, which only ever narrows the ANCHOR position, is not the whole answer.
// seedMultiPosition is the predicate that notices.
func TestSeedMultiPosition_FlipsForEveryConvertingPlainLens(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	for _, l := range convertingPlainLenses {
		t.Run(l.name, func(t *testing.T) {
			p := plainDerivationPipeline(t, adjKV, l.spec)
			rs := p.ruleState()
			require.True(t, rs.rootHops.Complete,
				"the ranged hop must be indexed or this lens has no scan-root index at all: %s", rs.rootHops.Incomplete)
			idx, ready := p.plainDerivationIndex(rs)
			require.True(t, ready)

			positions := idx.PositionsBinding(l.rootType)
			require.Greater(t, len(positions), 1,
				"%s binds %s at %v — the comprehension's unlabeled positions admit it too", l.name, l.rootType, positions)
			require.Contains(t, positions, idx.Anchor, "the scan root is one of them")

			require.True(t, p.seedMultiPosition(rs, l.rootType),
				"%s must take the multi-position seed path", l.name)
		})
	}

	// The contrast that keeps the four assertions above from being a property of
	// every lens: a plain lens whose anchor label binds only the anchor stays on
	// the narrow single-seed path.
	t.Run("a single-position plain lens does not flip", func(t *testing.T) {
		p := plainDerivationPipeline(t, adjKV, providerSpec)
		require.False(t, p.seedMultiPosition(p.ruleState(), "provider"))
	})
}

// TestSeedMultiPosition_MultiPositionPathEqualsTheSingleSeedResult is T7's
// second half, on the lens whose shape makes the claim checkable end to end.
//
// The multi-position path exists because a single engine-level seed can miss
// rows where the event vertex sits at the OTHER position. On these four lenses
// that other position is a comprehension one, so the WALK's answer for an
// anchor-typed event collapses back to the event vertex itself — and the derived
// evaluation must therefore produce exactly what today's narrow single seed
// produces. A widening here would reproject siblings on every anchor event.
//
// The sibling lease is what makes the equality non-trivial: an unseeded
// whole-corpus rescan would return both rows, so "equal to the single seed"
// is a real constraint rather than the only possible answer.
func TestSeedMultiPosition_MultiPositionPathEqualsTheSingleSeedResult(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	p, coreKV, adjKV, _ := newRetractionPipeline(t, shippedCafeLeaseWorkplaces, []string{"key"})
	ctx := context.Background()

	const (
		leaseA = "RangedLeaseAAAAAAAAA"
		leaseB = "RangedLeaseBBBBBBBBB"
		unitA  = "RangedUnitAAAAAAAAAA"
		unitB  = "RangedUnitBBBBBBBBBB"
		tower  = "RangedTowerAAAAAAAAA"
	)
	leaseAKey := "vtx.leaseapp." + leaseA
	leaseAProps := writeAndReturnVertex(t, coreKV, leaseAKey, "leaseapp", nil)
	writeAndReturnVertex(t, coreKV, "vtx.leaseapp."+leaseB, "leaseapp", nil)
	writeAndReturnVertex(t, coreKV, "vtx.unit."+unitA, "unit", nil)
	writeAndReturnVertex(t, coreKV, "vtx.unit."+unitB, "unit", nil)
	writeAndReturnVertex(t, coreKV, "vtx.building."+tower, "building", nil)
	buildCollisionEdge(t, adjKV, "appliesToUnit", "leaseapp", leaseA, "unit", unitA)
	buildCollisionEdge(t, adjKV, "appliesToUnit", "leaseapp", leaseB, "unit", unitB)
	buildCollisionEdge(t, adjKV, "containedIn", "unit", unitA, "building", tower)
	buildCollisionEdge(t, adjKV, "containedIn", "unit", unitB, "building", tower)

	rs := p.ruleState()
	require.True(t, p.seedMultiPosition(rs, "leaseapp"),
		"this lens is on the multi-position path, or the equality below says nothing about it")

	derived, ok, err := p.deriveAnchorsForPlainVertex(ctx, rs, leaseAKey, "leaseapp")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{leaseAKey}, derived,
		"the extra positions are comprehension positions, so the derived set collapses to the event vertex itself")

	got, err := p.evaluatePlainDerivedAnchors(ctx, rs, derived, "leaseapp")
	require.NoError(t, err)
	want, err := p.executeFullForActor(ctx, rs, leaseAKey, leaseAProps, leaseAKey)
	require.NoError(t, err)
	require.Equal(t, want, got,
		"the K-seeded derived evaluation must produce exactly what today's narrow single seed produces")

	// Non-vacuity: the sibling lease really is in the corpus, so an answer that
	// widened would have shown it.
	require.Len(t, want, 1, "the single seed answers for lease A alone")
	unseeded, err := p.executeFullForActor(ctx, rs, leaseAKey, leaseAProps, "")
	require.NoError(t, err)
	require.Len(t, unseeded, 2, "an unseeded rescan really does return both leases")
}

// TestActorTypeBindsAnchorOnly_NeverArmsForAConvertedLens is T8. The one-key
// answer is the NARROW answer: it lets an event on a vertex of the actor type
// be answered with that vertex alone, skipping the walk. On a converted lens
// that would be an under-approximation, and this pins it closed per lens so a
// later cypher edit that would arm it fails here rather than in a tenant.
//
// A positive vector runs first. The predicate can and does return true — for
// capabilityRoles, whose identity label binds only the anchor — so a blanket
// false below would be caught rather than mistaken for a proof.
func TestActorTypeBindsAnchorOnly_NeverArmsForAConvertedLens(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	stagedStaffReadGrantsSpec := generatedReadGrantProducerSpec(t, "edgeManifestStaffReadGrants")

	t.Run("positive vector: a single-position actor lens does arm", func(t *testing.T) {
		p := derivationPipeline(t, adjKV, rolesSpec)
		require.True(t, ActorTypeBindsAnchorOnly(p.ruleState().anchorHops, "identity"))
	})

	t.Run("capabilityServiceAccess", func(t *testing.T) {
		p := derivationPipelineWithTaxonomy(t, adjKV, shippedCapabilityServiceAccess, armedLocationResolver())
		ix := p.ruleState().anchorHops
		require.True(t, ix.Complete, "%s", ix.Incomplete)
		require.Equal(t, -1, ix.UnresolvedExpansionPosition(),
			"the pin has to be earned on a fully resolved index, or it holds for the wrong reason")

		// The governing fact, named rather than left to the verdict: the actor
		// type binds at the unlabelled positions too — `exLoc` inside the
		// exclusion and `op` inside the comprehension — so the anchor is not the
		// only place an identity event can land.
		positions := ix.PositionsBinding("identity")
		require.Greater(t, len(positions), 1, "identity binds at %v, anchor=%d", positions, ix.Anchor)
		for _, pos := range positions {
			if pos == ix.Anchor {
				continue
			}
			require.Empty(t, ix.Labels[pos], "the extra positions are the unlabelled ones")
		}
		require.False(t, ActorTypeBindsAnchorOnly(ix, "identity"))
	})

	t.Run("edgeManifestStaffReadGrants", func(t *testing.T) {
		p := derivationPipeline(t, adjKV, stagedStaffReadGrantsSpec)
		ix := p.ruleState().anchorHops
		require.True(t, ix.Complete, "%s", ix.Incomplete)
		require.Equal(t, -1, ix.UnresolvedExpansionPosition(),
			"the pin has to be earned on a fully resolved index, or it holds for the wrong reason")

		// The governing fact, named rather than left to the verdict. This
		// producer carries two UNLABELLED positions — `work` and `place`, the
		// ends of the worksAt + containedIn*0.. spine the generator emits
		// without types — and an unlabelled position admits ANY vertex type,
		// the identity actor type included. So an identity event can land at
		// three positions, not one, and answering it with that one key would
		// drop every other actor whose row renders the same vertex.
		positions := ix.PositionsBinding("identity")
		require.Len(t, positions, 3, "identity binds at %v, anchor=%d", positions, ix.Anchor)
		for _, pos := range positions {
			if pos == ix.Anchor {
				continue
			}
			require.Empty(t, ix.Labels[pos], "the two extra positions are the unlabelled spine ends")
		}
		require.False(t, ActorTypeBindsAnchorOnly(ix, "identity"))
	})

	for _, l := range convertingPlainLenses {
		t.Run(l.name, func(t *testing.T) {
			p := plainDerivationPipeline(t, adjKV, l.spec)
			ix := p.ruleState().anchorHops
			// A plain lens carries no `{key: $actorKey}` position at all, so the
			// ANCHOR index is incomplete however steppable its hops are. That
			// is the governing conjunct, and it is asserted so the pin below
			// cannot silently start holding for a different reason.
			require.False(t, ix.Complete)
			require.NotEmpty(t, ix.Incomplete, "the refusal must name itself")
			require.False(t, ActorTypeBindsAnchorOnly(ix, l.rootType))
		})
	}
}

// wideFanChainSpec is the shape that separates the walk's two budgets: a ranged
// containment hop over vertices whose adjacency documents are WIDE. The
// derivation reads one document per vertex however many entries it holds, so a
// graph whose vertices carry many edges the step does not follow costs almost
// nothing in reads and a great deal in iteration.
const wideFanChainSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0:location)-[:containedIn*0..]->(loc:location)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`

// TestDeriveAnchors_RangedWorkBudgetFallsBackRatherThanBurning pins the budget
// the read cap structurally cannot enforce.
//
// edgesOf memoises, so re-entering the ranged closure re-iterates cached edge
// lists at NO read cost: reads bound the distinct vertices a walk touches, never
// the work it does on them. A cold review measured the gap — 1,023 reads against
// 86,050 edge visits on a containment tree, twelve times the cost of the BFS the
// derivation exists to undercut, and seconds of CPU at 40% of the read cap.
//
// The fixture is built so the two budgets cannot be confused. Each location on
// the chain carries a hundred `nearby` edges, and `nearby` is not the step's
// relation: edgeTakesStep filters those entries, so they are never admitted and
// their far ends are never READ — but every one of them is ITERATED. The walk
// therefore does ~100x the work per read, and a read cap set far above the
// vertex count still leaves the work budget as the only thing that can refuse.
// (An earlier fixture used same-relation siblings, which ARE admitted and then
// read; the read cap fired first and the test passed with the work budget
// disabled — pinning nothing.)
//
// The budget's contract is exactly the read cap's, which is what makes it safe
// to add one: a breach is a FALLBACK, never a truncation.
func TestDeriveAnchors_RangedWorkBudgetFallsBackRatherThanBurning(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	ctx := context.Background()

	const chainLen, fanPerLevel = 4, 100

	f.vertex("alice", "identity")
	f.vertex("l0", "location")
	f.edge("residesIn", "alice", "l0")
	for lvl := 0; lvl < chainLen; lvl++ {
		cur := fmt.Sprintf("l%d", lvl)
		next := fmt.Sprintf("l%d", lvl+1)
		f.vertex(next, "location")
		f.edge("containedIn", cur, next)
		for i := 0; i < fanPerLevel; i++ {
			side := fmt.Sprintf("near%d_%d", lvl, i)
			f.vertex(side, "location")
			f.edge("nearby", cur, side)
		}
	}
	f.vertex("svc", "service")
	f.edge("availableAt", "svc", fmt.Sprintf("l%d", chainLen))

	p := derivationPipeline(t, adjKV, wideFanChainSpec)
	rs := p.ruleState()
	require.True(t, rs.anchorHops.Complete, "%s", rs.anchorHops.Incomplete)
	svcKey := f.key("svc", "service")
	aliceKey := f.key("alice", "identity")

	// The positive vector first, or the refusal below pins nothing: with room to
	// work, this walk answers, and answers correctly.
	p.SetAnchorDerivationReadCap(4_000)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, svcKey, "service")
	require.NoError(t, err)
	require.True(t, ok, "a generous budget must let this walk finish")
	require.ElementsMatch(t, []string{aliceKey}, derived)
	readsWhenAnswering := p.AnchorDerivationShadow().RangedClosureReads

	// Now a cap whose WORK allowance the walk exceeds while its READ allowance
	// is untouched. The chain is a handful of vertices; the cap is an order of
	// magnitude above that, so nothing here can be a read refusal.
	p.SetAnchorDerivationReadCap(60)
	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, svcKey, "service")
	require.NoError(t, err)
	require.False(t, ok, "a walk that exceeds its work budget must refuse, not burn")
	require.Nil(t, derived, "a refusal carries no anchors — a partial set reads as a complete one")
	require.Less(t, p.AnchorDerivationShadow().RangedClosureReads-readsWhenAnswering, int64(60),
		"the refusal must come from the work budget, not from the read cap")

	// And the refusal loses nothing, which is why refusing is the right answer:
	// the enumerator the caller falls back to still finds alice.
	bfs, err := p.actorEnumerator.Enumerate(ctx, svcKey, "service")
	require.NoError(t, err)
	require.Contains(t, bfs, aliceKey,
		"the fallback answer is complete, so the budget costs cost and never correctness")
}
