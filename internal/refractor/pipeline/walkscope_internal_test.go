package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// descriptorHubSpec is the shape the whole increment exists for: a lens that
// reaches a service through `providedTo` and never through the type descriptor.
// A service instance's other edge, `instanceOf`, points at a `vtx.meta` hub
// carrying one link per instance of that type.
const descriptorHubSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:providedTo]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS serviceKey
`

// hubFixture wires one service to its holder and to a type descriptor, and
// hangs peerCount OTHER services off that same descriptor, each with a holder
// of its own. It is the live topology in miniature: the relation-blind walk
// crosses the descriptor and reaches every holder in the graph.
func hubFixture(t *testing.T, peerCount int) (*enumFixture, *substrate.KV, []string) {
	t.Helper()
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	f.vertex("meta", "meta")
	f.vertex("svc", "service")
	f.vertex("holder", "identity")
	f.edge("instanceOf", "svc", "meta")
	f.edge("providedTo", "svc", "holder")

	peers := make([]string, 0, peerCount)
	for i := 0; i < peerCount; i++ {
		svcName := "peerSvc" + string(rune('A'+i))
		holderName := "peerHolder" + string(rune('A'+i))
		f.vertex(svcName, "service")
		peerKey := f.vertex(holderName, "identity")
		f.edge("instanceOf", svcName, "meta")
		f.edge("providedTo", svcName, holderName)
		peers = append(peers, peerKey)
	}
	return f, adjKV, peers
}

// TestEnumerateScoped_DescriptorHubIsNotCrossed is the defect and its fix in one
// case. Relation-blind, an event on ONE service reaches every holder in the
// graph by way of the type descriptor; under the lens's own pattern scope it
// reaches exactly the one holder that service is provided to.
func TestEnumerateScoped_DescriptorHubIsNotCrossed(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()
	enum := NewActorEnumerator(adjKV, nil, "identity")
	svc := f.key("svc", "service")

	blind, err := enum.Enumerate(ctx, svc, "service")
	require.NoError(t, err)
	require.ElementsMatch(t, append([]string{f.key("holder", "identity")}, peers...), blind,
		"the relation-blind walk expands the descriptor and reaches every instance's holder")

	scope := &walkScope{
		byType:   map[string]map[string]struct{}{"service": {"providedTo": {}}},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	scoped, err := enum.enumerateScoped(ctx, svc, "service", scope)
	require.NoError(t, err)
	require.Equal(t, []string{f.key("holder", "identity")}, scoped,
		"the pattern never passes through the descriptor, so the walk must not either")
}

// TestEnumerateScoped_DerivedScopeIsTheOneThatPrunes runs the same graph through
// the DERIVATION rather than a hand-built scope, so the pin is on what a lens
// really earns from its own cypher.
func TestEnumerateScoped_DerivedScopeIsTheOneThatPrunes(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()

	p := derivationPipeline(t, adjKV, descriptorHubSpec)
	// §4.2's second conjunct: the scope gives up the incidental heal, so it is
	// withheld until a standing healer will repair the row. A sweep plan is the
	// auth/business plane's arm of it.
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	rs := p.ruleState()
	require.NotNil(t, rs.walkScope, "the pattern half must derive: %s", rs.walkScopeRefusal)

	byType, anyType, scoped := p.WalkScope()
	require.True(t, scoped)
	require.Equal(t, map[string][]string{
		"identity": {"providedTo"},
		"service":  {"providedTo"},
	}, byType)
	require.Empty(t, anyType)

	anchors, err := p.enumerateAnchors(ctx, rs, f.key("svc", "service"), "service")
	require.NoError(t, err)
	require.Equal(t, []string{f.key("holder", "identity")}, anchors)

	for _, peer := range peers {
		require.NotContains(t, anchors, peer)
	}
}

// TestEnumerateScoped_HierarchyHopStillReachesTheManager keeps the one addition
// to the stop-at-an-actor rule: the fixed reportsTo hop runs off every found
// actor, and it runs through a relation-scoped read rather than the whole node.
func TestEnumerateScoped_HierarchyHopStillReachesTheManager(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.vertex("noise", "location")
	f.edge("assignedTo", "report", "task")
	f.edge("reportsTo", "report", "manager")
	// An edge on the report the scope does not name, so the hierarchy read has
	// something it must skip rather than merely nothing to find.
	f.edge("locatedAt", "report", "noise")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	scope := &walkScope{
		byType:   map[string]map[string]struct{}{"task": {"assignedTo": {}}, "identity": {"assignedTo": {}}},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	actors, err := enum.enumerateScoped(context.Background(), task, "task", scope)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		f.key("report", "identity"),
		f.key("manager", "identity"),
	}, actors, "reportsTo is the enumerator's own fixed hop, not a scoped relation")
}

// TestEnumerateScoped_NilScopeIsTodaysWalk is the fail-closed half: the shipped
// co-holder answer is what a nil scope still produces, byte for byte.
func TestEnumerateScoped_NilScopeIsTodaysWalk(t *testing.T) {
	p, f, adjKV := rolesFixture(t)
	ctx := context.Background()
	alice := f.key("alice", "identity")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	blind, err := enum.enumerateScoped(ctx, alice, "identity", nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		alice, f.key("bob", "identity"), f.key("carol", "identity"),
	}, blind)

	// The same answer the shipped walk gives through the pipeline's own seam.
	walked, err := p.enumerateAnchors(ctx, p.ruleState(), alice, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, blind, walked)
}

// TestEnumerateScoped_ScopeDoesNotCostTheCoHolders guards the direction that
// hurts. capabilityRoles really does depend on every co-holder of a role, and
// its own derived scope names holdsRole and grantedBy — so scoping must leave
// that answer exactly as wide as the relation-blind walk's.
func TestEnumerateScoped_ScopeDoesNotCostTheCoHolders(t *testing.T) {
	p, f, adjKV := rolesFixture(t)
	ctx := context.Background()
	rs := p.ruleState()
	require.NotNil(t, rs.walkScope, "capabilityRoles' pattern graph must scope: %s", rs.walkScopeRefusal)

	admin := f.key("admin", "role")
	blind, err := NewActorEnumerator(adjKV, nil, "identity").Enumerate(ctx, admin, "role")
	require.NoError(t, err)

	scoped, err := p.enumerateAnchors(ctx, rs, admin, "role")
	require.NoError(t, err)
	require.ElementsMatch(t, blind, scoped)
	require.ElementsMatch(t, []string{
		f.key("alice", "identity"), f.key("bob", "identity"), f.key("carol", "identity"),
	}, scoped)
}

// TestWalkScope_MultiWalkUnionsEveryBranch is the unsoundness the derivation
// avoids by not reading ruleState.anchorHops: that field is single-walk, so a
// multi-walk lens whose scope came from one branch would prune the other's
// relations and never reproject the anchors it reaches.
func TestWalkScope_MultiWalkUnionsEveryBranch(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	eng := full.New()

	branchA, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, role.key AS rk
`)
	require.NoError(t, err)
	branchB, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:providedTo]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS sk
`)
	require.NoError(t, err)

	p := &Pipeline{ruleID: "multiWalkLens", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
	// A multi-walk lens is a Personal lens, so its healer is the personal
	// plane's, not a sweep plan (it never receives one).
	p.SetPersonalPlaneHealer(true)
	require.NoError(t, p.UseFullEngineBranches(eng, branchA, []ruleengine.CompiledRule{branchA, branchB}))

	byType, anyType, scoped := p.WalkScope()
	require.True(t, scoped, "both branches index completely: %s", p.WalkScopeRefusal())
	require.Empty(t, anyType)
	require.Equal(t, map[string][]string{
		"identity": {"holdsRole", "providedTo"},
		"role":     {"holdsRole"},
		"service":  {"providedTo"},
	}, byType, "the identity position carries BOTH branches' relations")
}

// TestWalkScope_OneUnreadableBranchRefusesTheWholeScope pins the absorbing
// element: a branch whose pattern graph cannot be read says nothing about which
// relations it traverses, so no branch's scope may be applied.
func TestWalkScope_OneUnreadableBranchRefusesTheWholeScope(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	eng := full.New()

	typed, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, role.key AS rk
`)
	require.NoError(t, err)
	unreadable, err := eng.Parse(unindexableRolesSpec)
	require.NoError(t, err)

	p := &Pipeline{ruleID: "mixedLens", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
	p.SetPersonalPlaneHealer(true)
	require.NoError(t, p.UseFullEngineBranches(eng, typed, []ruleengine.CompiledRule{typed, unreadable}))

	_, _, scoped := p.WalkScope()
	require.False(t, scoped)
	require.Equal(t, walkScopeRefusalIncompleteIndex, p.WalkScopeRefusal(),
		"the healer is installed, so the refusal reported is the PATTERN one")
}

// TestWalkScope_UnreadableCypherRefusesTheScope pins the refusal on a real
// cypher. A pattern graph the completeness predicate declines says nothing about
// which relations it traverses, so the whole index is unreadable and the walk
// stays relation-blind.
func TestWalkScope_UnreadableCypherRefusesTheScope(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, unindexableRolesSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})

	byType, anyType, scoped := p.WalkScope()
	require.False(t, scoped)
	require.Nil(t, byType)
	require.Nil(t, anyType)
	require.Equal(t, walkScopeRefusalIncompleteIndex, p.WalkScopeRefusal(),
		"the healer is installed, so the refusal reported is the PATTERN one")
}

// TestWalkScope_UntypedHopAtLabeledEndsScopesPerType is the same question asked
// of a wildcard hop, and the answer differs: the relation is unknowable, but the
// TYPES its two labeled positions admit are not, so exactly those types follow
// every relation and no other type follows any. Reached from a real cypher —
// AnchorHopIndex now records an untyped relationship as a wildcard rather than
// refusing it.
func TestWalkScope_UntypedHopAtLabeledEndsScopesPerType(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, untypedRolesSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})

	_, _, scoped := p.WalkScope()
	require.True(t, scoped)
	require.Empty(t, p.WalkScopeRefusal())

	s := p.ruleState().walkScope
	require.NotNil(t, s)
	require.True(t, s.allows("identity", "anythingAtAll"))
	require.True(t, s.allows("role", "anythingAtAll"))
	require.False(t, s.allows("service", "anythingAtAll"),
		"a type neither position admits still follows nothing")
}

// TestDeriveWalkScope_UntypedHopAtAnUnlabeledPositionIsWildcard states the
// conjunct on its own: an untyped hop at a position binding any type leaves
// nothing to scope, so the whole scope is refused rather than narrowed. The
// pattern graph is hand-built so the index carries this shape and nothing else;
// objectAttachments is the shipped cypher that lands on it, pinned by
// actor_walk_scope_corpus_census_test.go.
func TestDeriveWalkScope_UntypedHopAtAnUnlabeledPositionIsWildcard(t *testing.T) {
	ix := full.HopIndex{
		Labels:   []string{"identity", ""},
		Hops:     []full.PatternHop{{Rel: "", From: 0, To: 1, Dir: full.DirOut, Min: 1, Max: 1, Binding: true}},
		Anchor:   0,
		Complete: true,
	}
	s := &walkScope{
		byType:   map[string]map[string]struct{}{},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	require.Equal(t, walkScopeRefusalUntypedHopUnlabeled, s.addIndex(ix),
		"an untyped hop at an unlabeled position scopes nothing")
}

// TestDeriveWalkScope_UntypedHopAtALabeledPositionIsPerTypeWildcard is the same
// conjunct one step narrower: the relation is unknowable, but the TYPES the
// position admits are not, so only those types follow every relation.
func TestDeriveWalkScope_UntypedHopAtALabeledPositionIsPerTypeWildcard(t *testing.T) {
	ix := full.HopIndex{
		Labels:   []string{"identity", "role"},
		Hops:     []full.PatternHop{{Rel: "", From: 0, To: 1, Dir: full.DirOut, Min: 1, Max: 1, Binding: true}},
		Anchor:   0,
		Complete: true,
	}
	s := &walkScope{
		byType:   map[string]map[string]struct{}{},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	require.Empty(t, s.addIndex(ix))
	require.True(t, s.allows("identity", "anything"))
	require.True(t, s.allows("role", "anything"))
	require.False(t, s.allows("service", "anything"),
		"a type neither position admits still follows nothing")

	_, finite := s.relationsAt("identity")
	require.False(t, finite, "a wildcard type has no relation set, so its node is read whole")
}

// TestWalkScope_RangedHopIsFollowableAtEveryType pins the intermediate rule: a
// variable-length hop expands through unlabeled intermediates, so the walk
// stands on vertices of a type no position names while crossing it.
func TestWalkScope_RangedHopIsFollowableAtEveryType(t *testing.T) {
	ix := full.HopIndex{
		Labels:   []string{"identity", "location"},
		Hops:     []full.PatternHop{{Rel: "containedIn", From: 1, To: 0, Dir: full.DirOut, Min: 0, Max: 7, Binding: true}},
		Anchor:   0,
		Complete: true,
	}
	s := &walkScope{
		byType:   map[string]map[string]struct{}{},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	require.Empty(t, s.addIndex(ix))
	require.True(t, s.allows("identity", "containedIn"))
	require.True(t, s.allows("location", "containedIn"))
	require.True(t, s.allows("unit", "containedIn"),
		"an intermediate of the expansion binds no position and must still be crossed")
	require.False(t, s.allows("unit", "holdsRole"))
}

// TestWalkScope_UnknownTypeFollowsNothing states the pruning rule directly: a
// vertex type no pattern position admits cannot sit on any path from an anchor
// to the event vertex, so the walk crosses nothing from it — and asks the store
// for nothing either.
func TestWalkScope_UnknownTypeFollowsNothing(t *testing.T) {
	s := &walkScope{
		byType:   map[string]map[string]struct{}{"service": {"providedTo": {}}},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	require.False(t, s.allows("meta", "instanceOf"))
	rels, finite := s.relationsAt("meta")
	require.True(t, finite)
	require.Empty(t, rels, "an empty finite set is what makes the descriptor read free")
}

// TestWalkScope_NilScopeAllowsEverything is the fail-closed contract every
// refusal lands on.
func TestWalkScope_NilScopeAllowsEverything(t *testing.T) {
	var s *walkScope
	require.True(t, s.allows("anything", "atAll"))
	_, finite := s.relationsAt("anything")
	require.False(t, finite)
}

// TestDeriveWalkScope_NonFullEngineRefuses covers the entry conjunct: without
// the full engine there is no pattern graph to read at all.
func TestDeriveWalkScope_NonFullEngineRefuses(t *testing.T) {
	scope, refusal := deriveWalkScope("simple", nil, nil)
	require.Nil(t, scope)
	require.Equal(t, walkScopeRefusalNotFullEngine, refusal)

	scope, refusal = deriveWalkScope(ruleengine.EngineFull, nil, nil)
	require.Nil(t, scope)
	require.Equal(t, walkScopeRefusalNoRule, refusal)
}

// TestWalkScope_NoStandingHealerRefusesTheScope is §4.2's second conjunct, on
// the pipeline whose pattern half is beyond dispute — the same descriptorHubSpec
// that scopes cleanly a few tests above. Scoping the walk gives up the
// incidental reprojection that today heals a row lost out of band, and §4.2
// licenses giving up that accident only where a standing healer replaces it.
//
// The three states are asserted on ONE pipeline in sequence, so nothing but the
// healer moves between them.
func TestWalkScope_NoStandingHealerRefusesTheScope(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()
	p := derivationPipeline(t, adjKV, descriptorHubSpec)
	svc := f.key("svc", "service")

	// The pattern half derives; the lens still walks relation-blind.
	require.NotNil(t, p.ruleState().walkScope,
		"the pattern half is not what refuses here")
	_, _, scoped := p.WalkScope()
	require.False(t, scoped)
	require.Equal(t, walkScopeRefusalNoHealer, p.WalkScopeRefusal())

	blind, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.ElementsMatch(t, append([]string{f.key("holder", "identity")}, peers...), blind,
		"an unhealed lens keeps the accident: the descriptor hub is still crossed")

	// The auth/business plane's arm.
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	_, _, scoped = p.WalkScope()
	require.True(t, scoped)
	require.Empty(t, p.WalkScopeRefusal())

	narrowed, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.Equal(t, []string{f.key("holder", "identity")}, narrowed,
		"the sweep is what licenses giving the accident up")
}

// TestWalkScope_PersonalPlaneHealerAlsoLicensesTheScope is the second arm, and
// it is the one that carries the shipped `edge*` corpus: a Personal Lens never
// receives a SweepPlan, so on the sweeper alone every one of them would keep the
// relation-blind walk. Its healer is grantchange.PersonalSweeper plus the D1
// grant-change edge, and the host records that registration on the pipeline.
func TestWalkScope_PersonalPlaneHealerAlsoLicensesTheScope(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()
	p := derivationPipeline(t, adjKV, descriptorHubSpec)
	svc := f.key("svc", "service")

	require.Nil(t, p.Sweeper(), "a Personal Lens never gets a sweep plan")
	_, _, scoped := p.WalkScope()
	require.False(t, scoped)
	require.Equal(t, walkScopeRefusalNoHealer, p.WalkScopeRefusal())
	blind, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.Len(t, blind, len(peers)+1)

	p.SetPersonalPlaneHealer(true)
	_, _, scoped = p.WalkScope()
	require.True(t, scoped, "the personal plane's healer is a standing healer too")
	require.Empty(t, p.WalkScopeRefusal())

	narrowed, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.Equal(t, []string{f.key("holder", "identity")}, narrowed)
}

// TestWalkScope_HealerRefusalIsReportedAheadOfThePatternOne pins the ordering.
// The healer conjunct is a property of the lens's INSTALL and holds for the life
// of it, so reporting a cypher-level refusal for a lens that would be
// relation-blind anyway would send a reader to edit the wrong thing. It mirrors
// oneKeyAnswerSound, which tests p.sweeper before its own pattern half.
func TestWalkScope_HealerRefusalIsReportedAheadOfThePatternOne(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, unindexableRolesSpec)

	require.Nil(t, p.ruleState().walkScope, "this cypher's pattern half refuses too")
	require.Equal(t, walkScopeRefusalIncompleteIndex, p.ruleState().walkScopeRefusal)
	require.Equal(t, walkScopeRefusalNoHealer, p.WalkScopeRefusal(),
		"with both conjuncts failing, the install-level one is what an operator is told")

	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	require.Equal(t, walkScopeRefusalIncompleteIndex, p.WalkScopeRefusal(),
		"once the healer stands, the pattern refusal is what remains")
}

// TestWalkScope_TallyReportsTheEffectivePosture keeps the operator-visible flag
// on the same predicate the walk runs, healer conjunct included — a lens
// reporting walkScoped while running the relation-blind walk is the one way this
// line could mislead.
func TestWalkScope_TallyReportsTheEffectivePosture(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, descriptorHubSpec)

	require.False(t, p.walkIsScoped(p.ruleState()), "no healer, no scope, no claim of one")
	p.SetPersonalPlaneHealer(true)
	require.True(t, p.walkIsScoped(p.ruleState()))
}

// TestDeriveWalkScope_MalformedIndexRefusesRatherThanSkipping is the conjunct
// item 4 of the cold review added: a hop naming a position the label slice does
// not hold cannot be read, and SKIPPING it would silently drop the relations it
// contributes — a relation missing from the scope is an edge the walk stops
// crossing, which is the narrowing direction. It has its own refusal so the
// reason an operator reads names the real cause rather than the untyped-hop one.
func TestDeriveWalkScope_MalformedIndexRefusesRatherThanSkipping(t *testing.T) {
	ix := full.HopIndex{
		Labels:   []string{"identity"},
		Hops:     []full.PatternHop{{Rel: "holdsRole", From: 0, To: 4, Dir: full.DirOut, Min: 1, Max: 1, Binding: true}},
		Anchor:   0,
		Complete: true,
	}
	s := &walkScope{
		byType:   map[string]map[string]struct{}{},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	require.Equal(t, walkScopeRefusalMalformedIndex, s.addIndex(ix))
	require.Empty(t, s.byType, "nothing is folded in from an index that could not be read")
}

// TestWalkScope_NoRulePublishedNamesItsReason closes the vocabulary's last hole:
// a pipeline that never activated has a nil scope and, without this, no reason
// at all — and "not scoped, no reason given" reads as a bug in the derivation
// rather than as the truth about the pipeline.
func TestWalkScope_NoRulePublishedNamesItsReason(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := &Pipeline{ruleID: "neverActivated", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
	p.SetPersonalPlaneHealer(true)

	_, _, scoped := p.WalkScope()
	require.False(t, scoped)
	require.Equal(t, walkScopeRefusalNoRulePublished, p.WalkScopeRefusal())
}

// TestWalkScopeMode_OffRestoresTheRelationBlindWalk is the operator's way back.
// The scope's blast radius is bounded by an argument rather than by anything the
// code checks, so — exactly as PeerAnchorMode does for §18.1's widening — there
// has to be a lever that puts every lens back on the walk it had, and it has to
// say so through every surface an operator reads.
func TestWalkScopeMode_OffRestoresTheRelationBlindWalk(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()
	p := derivationPipeline(t, adjKV, descriptorHubSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	svc := f.key("svc", "service")

	_, _, scoped := p.WalkScope()
	require.True(t, scoped, "the lens scopes with the knob at its default")
	narrowed, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.Equal(t, []string{f.key("holder", "identity")}, narrowed)

	p.SetWalkScopeMode(WalkScopeModeOff)
	_, _, scoped = p.WalkScope()
	require.False(t, scoped)
	require.Equal(t, walkScopeRefusalOperatorOff, p.WalkScopeRefusal(),
		"the surfaces must name the knob, not a healer or a cypher")
	require.False(t, p.walkIsScoped(p.ruleState()), "and the tally must not claim a scope")

	blind, err := p.enumerateAnchors(ctx, p.ruleState(), svc, "service")
	require.NoError(t, err)
	require.ElementsMatch(t, append([]string{f.key("holder", "identity")}, peers...), blind,
		"off is the relation-blind walk, descriptor hub and all")

	p.SetWalkScopeMode(WalkScopeModeUnset)
	_, _, scoped = p.WalkScope()
	require.True(t, scoped, "Unset returns the pipeline to the package default")
}

// TestWalkScopeMode_OperatorRefusalOutranksEveryOther pins the ordering an
// operator depends on: someone who turned the scope off must be told THAT, not
// told about a healer they did not touch or a cypher they did not write.
func TestWalkScopeMode_OperatorRefusalOutranksEveryOther(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, unindexableRolesSpec)
	require.Equal(t, walkScopeRefusalNoHealer, p.WalkScopeRefusal(),
		"no healer and an unreadable cypher: the install-level reason wins")

	p.SetWalkScopeMode(WalkScopeModeOff)
	require.Equal(t, walkScopeRefusalOperatorOff, p.WalkScopeRefusal(),
		"and the operator's own lever outranks even that")
}

// TestParseWalkScopeMode_RejectsRatherThanGuessing keeps a typo from silently
// putting the whole process back on the relation-blind walk.
func TestParseWalkScopeMode_RejectsRatherThanGuessing(t *testing.T) {
	on, err := ParseWalkScopeMode("on")
	require.NoError(t, err)
	require.Equal(t, WalkScopeModeOn, on)
	off, err := ParseWalkScopeMode("off")
	require.NoError(t, err)
	require.Equal(t, WalkScopeModeOff, off)
	for _, bad := range []string{"", "ON", "true", "scoped", "0"} {
		m, err := ParseWalkScopeMode(bad)
		require.Errorf(t, err, "%q must be rejected", bad)
		require.Equal(t, WalkScopeModeUnset, m)
	}
	require.Equal(t, "on", WalkScopeModeOn.String())
	require.Equal(t, "off", WalkScopeModeOff.String())
	require.Equal(t, "unset", WalkScopeModeUnset.String())
	require.Equal(t, WalkScopeModeOn, DefaultWalkScopeMode(), "the built-in is on")
}

// TestShadowMode_BaselineIsTheRelationBlindWalk pins what the shadow tally
// measures against. Both of its counters are defined against the WIDEST answer
// the pipeline can give — NarrowedAnchors is "anchors the derivation spared",
// DivergentEvents is "the derivation reached one the trusted superset did not" —
// so a baseline that was itself pattern-scoped would understate the first and
// fire the second on anchors the scope pruned. `act` and `off` keep the scoped
// walk, which is what the pipeline really runs; only `shadow` widens.
func TestShadowMode_BaselineIsTheRelationBlindWalk(t *testing.T) {
	f, adjKV, peers := hubFixture(t, 3)
	ctx := context.Background()
	p := derivationPipeline(t, adjKV, descriptorHubSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	rs := p.ruleState()
	svc := f.key("svc", "service")
	holder := f.key("holder", "identity")

	// A derivation that always declines, so what comes back is the enumerator's
	// answer on every arm and the two modes differ by the walk alone.
	declines := func() ([]string, bool, error) { return nil, false, nil }
	enumerate := func(scoped bool) ([]string, error) {
		return p.enumerateAnchorsWalk(ctx, rs, svc, "service", scoped)
	}

	p.SetAnchorDerivationMode(DerivationModeOff)
	acted, err := p.affectedAnchors(ctx, rs, svc, declines, enumerate)
	require.NoError(t, err)
	require.Equal(t, []string{holder}, acted,
		"off runs the walk the pipeline really runs, which is scoped")

	p.SetAnchorDerivationMode(DerivationModeShadow)
	baseline, err := p.affectedAnchors(ctx, rs, svc, declines, enumerate)
	require.NoError(t, err)
	require.ElementsMatch(t, append([]string{holder}, peers...), baseline,
		"shadow measures against the relation-blind walk, and acts on that wider answer")
}

// TestWalkScope_RangedWildcardHopRefusesWhileAFixedOneScopes holds the two
// wildcard shapes side by side, because the scope's answer to them differs and
// only the pair proves the refusal is the ranged hop's doing.
//
// A FIXED wildcard hop between two labeled endpoints is scopeable: the relation
// is unknowable, but the hop stands on exactly the types those two positions
// admit, so those types — and no others — follow every relation.
//
// A RANGED one is not. Its expansion stands on the unlabeled intermediates
// between the two bound positions, crossing a relation the hop does not name at
// a type no position names, so folding only the endpoints' types into wildcard
// would scope the walk to less than it must cross. That is the narrowing
// direction, and the whole index is refused instead.
func TestWalkScope_RangedWildcardHopRefusesWhileAFixedOneScopes(t *testing.T) {
	newScope := func() *walkScope {
		return &walkScope{
			byType:   map[string]map[string]struct{}{},
			anyType:  map[string]struct{}{},
			wildcard: map[string]struct{}{},
		}
	}
	labels := []string{"identity", "role"}

	fixed := newScope()
	require.Empty(t, fixed.addIndex(full.HopIndex{
		Labels:   labels,
		Hops:     []full.PatternHop{{Rel: "", From: 0, To: 1, Dir: full.DirOut, Min: 1, Max: 1, Binding: true}},
		Anchor:   0,
		Complete: true,
	}), "a fixed wildcard hop between labeled endpoints is scopeable")
	require.True(t, fixed.allows("identity", "anything"))
	require.True(t, fixed.allows("role", "anything"))
	require.False(t, fixed.allows("unit", "anything"),
		"a type neither endpoint admits is not on the hop and follows nothing")

	ranged := newScope()
	require.Equal(t, walkScopeRefusalRangedWildcardHop, ranged.addIndex(full.HopIndex{
		Labels:   labels,
		Hops:     []full.PatternHop{{Rel: "", From: 0, To: 1, Dir: full.DirOut, Min: 1, Max: 3, Binding: true}},
		Anchor:   0,
		Complete: true,
	}), "a ranged wildcard hop's intermediates cross any relation at any type, so the scope refuses it under its own reason")
	require.False(t, ranged.allows("identity", "anything"),
		"a refused index leaves the scope untouched rather than half-built")
}
