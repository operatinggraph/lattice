package pipeline

// The per-branch index set's own vectors: the refusals, the published pair's
// lifetime, and the shared budget (personal-lens-derivation-licence-design.md
// §4.5, §5, §10).
//
// The corpus differential proves the union derives the right anchors for the
// three lenses that ship. These are the cases the corpus does not have: a lens
// whose walks disagree, a walk whose graph cannot answer, a reload that shrinks
// a three-walk body to one, and a graph wide enough to spend the budget. Every
// one is authored, because the refusals are written for whatever carries
// branches rather than for today's three — nothing restricts a branches spec to
// a generated personal lens (branchmerge.go's own doc contemplates a
// hand-authored one).

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// notFullRule is a compiled rule from some other engine. The multi-walk arm has
// to refuse it by NAME rather than by building a zero graph for it, which is the
// shape that logs a blank reason.
type notFullRule struct{}

func (notFullRule) EngineName() string { return "not-full" }

func mustParse(t *testing.T, eng *full.Engine, spec string) ruleengine.CompiledRule {
	t.Helper()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	return cr
}

// The three shapes a branch-carrying lens can be refused on, plus the two the
// pattern graphs agree on.
const (
	branchSpecMayRead    = "MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)\nRETURN x.key AS anchor"
	branchSpecMayBook    = "MATCH (identity:identity {key: $actorKey})-[:mayBook]->(x:unit)\nRETURN x.key AS anchor"
	branchSpecUnreadable = "MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor"
	branchSpecOtherType  = "MATCH (org:org {key: $actorKey})-[:owns]->(x:unit)\nRETURN x.key AS anchor"
)

// TestBranchAnchorHopsRefusal_EveryConjunctNamesItself is the closed vocabulary's
// own proof: every conjunct that can refuse a per-branch index set produces a
// NAMED, distinct reason, and the admitting case produces none.
//
// Each negative vector is preceded by the positive one it differs from by a
// single branch, so a refusal that fired for the wrong reason — or an admitting
// predicate that stopped answering — fails here rather than being read as the
// conjunct under test.
func TestBranchAnchorHopsRefusal_EveryConjunctNamesItself(t *testing.T) {
	eng := full.New()
	good := []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
	}

	t.Run("positive vector: two walks that agree admit, with no reason at all", func(t *testing.T) {
		hops, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, good, nil)
		require.Empty(t, refusal)
		require.Len(t, hops, 2)
		for i, h := range hops {
			require.Truef(t, h.Complete, "walk %d", i)
			require.Equal(t, "identity", h.Labels[h.Anchor])
		}
	})

	t.Run("a walk whose graph cannot answer names the incomplete conjunct AND carries that walk's own reason", func(t *testing.T) {
		_, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, []ruleengine.CompiledRule{
			good[0], mustParse(t, eng, branchSpecUnreadable),
		}, nil)
		require.Contains(t, refusal, DerivationBranchIncompleteRefusal)
		require.Contains(t, refusal, "walk 1")
		require.Contains(t, refusal, "lower bound exceeds one hop",
			"the refused walk's OWN reason must survive: 'one of the walks declined' alone sends an operator nowhere")
	})

	t.Run("walks anchoring on different labels name the disagreement — the checkable form of the old exclusion's claim", func(t *testing.T) {
		_, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, []ruleengine.CompiledRule{
			good[0], mustParse(t, eng, branchSpecOtherType),
		}, nil)
		require.Contains(t, refusal, DerivationBranchAnchorDisagreementRefusal)
		require.Contains(t, refusal, "identity")
		require.Contains(t, refusal, "org")
	})

	t.Run("a walk carrying an unresolved `*` position names the position it refused on", func(t *testing.T) {
		hops, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, good, nil)
		require.Empty(t, refusal, "precondition: this set is otherwise fine")
		// The state a rule state published while the resolver could not answer
		// carries: a COMPLETE graph whose `*` position has no concrete set.
		unresolved := append([]full.HopIndex(nil), hops...)
		unresolved[1].LabelExpand = make([]bool, len(unresolved[1].Labels))
		unresolved[1].LabelExpand[unresolved[1].Anchor] = true
		unresolved[1].Expanded = nil
		require.GreaterOrEqual(t, unresolved[1].UnresolvedExpansionPosition(), 0)

		refusal = branchAnchorHopsRefusal(unresolved)
		require.Contains(t, refusal, DerivationBranchUnresolvedExpansionRefusal)
		require.Contains(t, refusal, "walk 1")
	})

	t.Run("a branch from another engine leaves no graph at all, and says so", func(t *testing.T) {
		_, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, []ruleengine.CompiledRule{
			good[0], notFullRule{},
		}, nil)
		require.Equal(t, DerivationNoBranchIndexRefusal, refusal)
	})

	t.Run("a non-full engine, and no branches at all, refuse the same way", func(t *testing.T) {
		_, refusal := deriveBranchAnchorHops("plain", good, nil)
		require.Equal(t, DerivationNoBranchIndexRefusal, refusal)
		_, refusal = deriveBranchAnchorHops(ruleengine.EngineFull, nil, nil)
		require.Equal(t, DerivationNoBranchIndexRefusal, refusal)
		require.Equal(t, DerivationNoBranchIndexRefusal, branchAnchorHopsRefusal(nil))
	})

	t.Run("a graph that declines without naming a conjunct is still named", func(t *testing.T) {
		// The belt to the named conjuncts' brace (G15's lesson): the zero
		// HopIndex is incomplete AND carries no reason, which is the shape that
		// logs a blank line and is then swallowed by the refusal latch. No arm
		// of AnchorHopIndex produces it today, so the vector is authored.
		refusal := branchAnchorHopsRefusal([]full.HopIndex{{}})
		require.Contains(t, refusal, DerivationBranchIncompleteRefusal)
		require.Contains(t, refusal, derivationBranchUnnamedRefusal)
		require.NotEmpty(t, refusal)
	})

	t.Run("every refusal is distinct, so the census vocabulary cannot collapse two conjuncts into one", func(t *testing.T) {
		seen := map[string]struct{}{}
		for _, r := range []string{
			DerivationNoBranchIndexRefusal,
			DerivationBranchIncompleteRefusal,
			DerivationBranchUnresolvedExpansionRefusal,
			DerivationBranchAnchorDisagreementRefusal,
			derivationBranchUnnamedRefusal,
			derivationUnnamedIndexRefusal,
			derivationAnchorLabelRefusal,
		} {
			require.NotEmpty(t, r)
			_, dup := seen[r]
			require.Falsef(t, dup, "two conjuncts share the reason %q", r)
			seen[r] = struct{}{}
		}
	})
}

// TestRuleState_BranchAnchorHopsSurvivePublication pins the hand-maintained
// round trip through the Pipeline's own fields.
//
// A ruleState field added without a line in BOTH publishRuleState and
// ruleState() reads as its zero value on every event with nothing failing
// anywhere — and for this PAIR the zero values are "no graphs" and "nothing
// refused them", which read together as a union over no walks at all: an empty
// anchor set returned as a real answer. So the omission is fail-open, and the
// trip is pinned rather than the two lists trusted to stay in step.
func TestRuleState_BranchAnchorHopsSurvivePublication(t *testing.T) {
	eng := full.New()
	p := &Pipeline{ruleID: "round-trip"}
	require.NoError(t, p.UseFullEngineBranches(eng, mustParse(t, eng, branchSpecMayRead), []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
	}))

	rs := p.ruleState()
	require.Len(t, rs.anchorHopsPerBranch, 2,
		"the graphs must survive the publication; nil here is a union over no walks")
	require.Empty(t, rs.anchorHopsPerBranchRefusal)
	require.True(t, rs.anchorHopsPerBranch[0].Complete)
	require.True(t, rs.anchorHopsPerBranch[1].Complete)

	// And the refusal half survives too — pinned on a set that IS refused, since
	// an empty string proves nothing about a field that carries empty strings.
	refused := &Pipeline{ruleID: "round-trip-refused"}
	require.NoError(t, refused.UseFullEngineBranches(eng, mustParse(t, eng, branchSpecMayRead), []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecUnreadable),
	}))
	rs = refused.ruleState()
	require.Contains(t, rs.anchorHopsPerBranchRefusal, DerivationBranchIncompleteRefusal)
	require.Empty(t, rs.anchorHopsPerBranch, "a refused set publishes no graphs")
}

// TestBranchAnchorHops_ReloadReplacesThemWholesale is the lifetime §5 states: a
// publication replaces the pair wholesale, so a lens reloaded from three walks
// down to one leaves NO per-branch graph armed — and the reverse arms them.
//
// The hazard is specific: the fields are only written on their own arm, so a
// reload that took the other arm and left the previous body's graphs standing
// would have the derivation walking a pattern the lens no longer projects.
func TestBranchAnchorHops_ReloadReplacesThemWholesale(t *testing.T) {
	eng := full.New()
	three := []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
		mustParse(t, eng, "MATCH (identity:identity {key: $actorKey})-[:mayVisit]->(x:unit)\nRETURN x.key AS anchor"),
	}
	p := &Pipeline{ruleID: "reload"}
	p.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	require.NoError(t, p.UseFullEngineBranches(eng, three[0], three))
	require.Len(t, p.ruleState().anchorHopsPerBranch, 3)
	idxs, ready := p.derivationIndexes(p.ruleState())
	require.True(t, ready)
	require.Len(t, idxs, 3)

	// Three walks down to one: the single-walk graph arms, and nothing of the
	// previous body survives.
	require.NoError(t, p.UseFullEngineBranches(eng, three[0], nil))
	rs := p.ruleState()
	require.Empty(t, rs.anchorHopsPerBranch, "a reload must not leave a previous body's graphs armed")
	require.Empty(t, rs.anchorHopsPerBranchRefusal)
	require.Empty(t, rs.branches)
	require.True(t, rs.anchorHops.Complete, "and the single-walk arm takes over")
	idxs, ready = p.derivationIndexes(rs)
	require.True(t, ready)
	require.Len(t, idxs, 1, "one walk, one graph — the union is over exactly what the lens now projects")

	// And back up again, to a body whose walks no longer agree: the graphs are
	// replaced by the refusal, not left as the previous body's usable set.
	require.NoError(t, p.UseFullEngineBranches(eng, three[0], []ruleengine.CompiledRule{
		three[0], mustParse(t, eng, branchSpecUnreadable),
	}))
	rs = p.ruleState()
	require.Empty(t, rs.anchorHopsPerBranch)
	require.Contains(t, rs.anchorHopsPerBranchRefusal, DerivationBranchIncompleteRefusal)
	_, ready = p.derivationIndexes(rs)
	require.False(t, ready, "a refused set must not derive; the caller keeps the enumerator")
}

// TestMultiWalkDerivationRefusal_FailsClosedOnThePair is the fail-open guard the
// round trip's zero values invite: graphs missing with nothing refusing them is
// not "nothing to refuse", it is a union over no walks.
func TestMultiWalkDerivationRefusal_FailsClosedOnThePair(t *testing.T) {
	eng := full.New()
	p := &Pipeline{ruleID: "blank-pair"}
	p.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	branches := []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
	}
	require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))

	rs := p.ruleState()
	require.Empty(t, p.multiWalkDerivationRefusal(rs), "precondition: this lens is otherwise derivable")

	rs.anchorHopsPerBranch = nil
	require.Equal(t, DerivationNoBranchIndexRefusal, p.multiWalkDerivationRefusal(rs))
	_, ready := p.derivationIndexes(rs)
	require.False(t, ready)

	// The other half of the pair, and the conjunct only a live read can answer.
	rs = p.ruleState()
	noEnumerator := &Pipeline{ruleID: "no-enumerator"}
	require.NoError(t, noEnumerator.UseFullEngineBranches(eng, branches[0], branches))
	require.Equal(t, derivationAnchorLabelRefusal,
		noEnumerator.multiWalkDerivationRefusal(noEnumerator.ruleState()))
	_, ready = noEnumerator.derivationIndexes(rs)
	require.False(t, ready)
}

// TestMultiWalkDerivationRefusal_AnchorLabelMustBeTheEnumeratorsActorType covers
// the conjunct an "is an enumerator installed at all" vector cannot reach.
//
// The walk renders every anchor it reaches as `vtx.<Labels[Anchor]>.<id>`, so a
// lens whose walks anchor on L running on a pipeline that enumerates A != L
// would hand the caller keys naming a kind of vertex the evaluation never binds
// — and NONE of the real A anchors. That is an under-approximation, the one
// direction this unit refuses, and it is silent: every other conjunct clears.
//
// The nil-enumerator case and this one share a refusal string but not a cause,
// and only this one exercises the comparison.
func TestMultiWalkDerivationRefusal_AnchorLabelMustBeTheEnumeratorsActorType(t *testing.T) {
	eng := full.New()
	branches := []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
	}
	p := &Pipeline{ruleID: "wrong-actor-type"}
	require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))

	// Positive vector first: the same lens, the same graphs, an enumerator over
	// the type its walks DO anchor on.
	p.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	require.Empty(t, p.multiWalkDerivationRefusal(p.ruleState()))
	idxs, ready := p.derivationIndexes(p.ruleState())
	require.True(t, ready)
	require.Len(t, idxs, 2)

	// And the same lens under an enumerator over a different type: every branch
	// conjunct still clears, so this is the comparison and nothing else.
	p.SetActorEnumerator(NewActorEnumerator(nil, nil, "org"))
	rs := p.ruleState()
	require.Empty(t, rs.anchorHopsPerBranchRefusal,
		"precondition: the graphs themselves are fine — the disagreement is with the ENUMERATOR")
	require.Equal(t, "identity", rs.anchorHopsPerBranch[0].Labels[rs.anchorHopsPerBranch[0].Anchor])
	require.Equal(t, derivationAnchorLabelRefusal, p.multiWalkDerivationRefusal(rs))
	require.Equal(t, derivationAnchorLabelRefusal, p.derivationIndexRefusal(rs))
	_, ready = p.derivationIndexes(rs)
	require.False(t, ready, "a lens whose anchors are not the enumerated type must derive nothing")
}

// TestPersonalDerivationStatus_LicensedAndTheIndexRefusedAreSeparateAnswers pins
// the operator surface's two halves against a lens that is in exactly the state
// an inference from one of them would get wrong: fully licensed, and acting on
// nothing because its walks do not agree about the anchor.
func TestPersonalDerivationStatus_LicensedAndTheIndexRefusedAreSeparateAnswers(t *testing.T) {
	eng := full.New()
	p := &Pipeline{ruleID: "licensed-index-refused", engineKind: ruleengine.EngineFull}
	require.NoError(t, p.UseFullEngineBranches(eng, mustParse(t, eng, branchSpecMayRead), []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecOtherType),
	}))
	p.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	p.SetPersonalPlaneHealer(true)
	p.SetPersonalDerivationLicence(licensedWiring(), cleanVerdict)

	licensed, refusal, indexReady, indexRefusal := p.PersonalDerivationStatus()
	require.True(t, licensed, "refusal: %s", refusal)
	require.Empty(t, refusal)
	require.False(t, indexReady,
		"the licence speaks about the process and the plane; the index speaks about the cypher, and this one's walks disagree")
	require.Contains(t, indexRefusal, DerivationBranchAnchorDisagreementRefusal)

	// The positive counterpart, so "licensed and acting" is distinguishable from
	// "licensed and refused" rather than the field simply always reading false.
	acting := &Pipeline{ruleID: "licensed-index-ready", engineKind: ruleengine.EngineFull}
	require.NoError(t, acting.UseFullEngineBranches(eng, mustParse(t, eng, branchSpecMayRead), []ruleengine.CompiledRule{
		mustParse(t, eng, branchSpecMayRead),
		mustParse(t, eng, branchSpecMayBook),
	}))
	acting.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
	acting.SetPersonalPlaneHealer(true)
	acting.SetPersonalDerivationLicence(licensedWiring(), cleanVerdict)

	licensed, refusal, indexReady, indexRefusal = acting.PersonalDerivationStatus()
	require.True(t, licensed, "refusal: %s", refusal)
	require.True(t, indexReady)
	require.Empty(t, indexRefusal)
}

// The budget fixture: three walks reaching the anchor over chains of one, two
// and three vertices, sharing their first vertex. The counts are what the test
// asserts on, so they are stated here rather than derived by the reader:
//
//	walk 0: folder                    -> 1 adjacency read
//	walk 1: folder, shelf             -> 1 new
//	walk 2: folder, room, team        -> 2 new
//
// Four DISTINCT documents between them, six if each walk kept its own memo, and
// three for the widest single walk. Those three numbers are what separate one
// shared budget from three private ones.
const (
	budgetWalk0 = "MATCH (identity:identity {key: $actorKey})-[:watches]->(f:folder)\nRETURN f.key AS anchor"
	budgetWalk1 = "MATCH (identity:identity {key: $actorKey})-[:owns]->(s:shelf)<-[:onShelf]-(f:folder)\nRETURN f.key AS anchor"
	budgetWalk2 = "MATCH (identity:identity {key: $actorKey})-[:leads]->(t:team)-[:usesRoom]->(r:room)<-[:inRoom]-(f:folder)\nRETURN f.key AS anchor"
)

func newBudgetFixture(t *testing.T) (*Pipeline, string) {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	branches := []ruleengine.CompiledRule{
		mustParse(t, eng, budgetWalk0),
		mustParse(t, eng, budgetWalk1),
		mustParse(t, eng, budgetWalk2),
	}
	p := &Pipeline{
		ruleID: "budget", coreKVBucket: "CORE", coreKV: coreKV, adjKV: adjKV,
		engineKind: ruleengine.EngineFull,
	}
	require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))

	ids := map[string]string{}
	for _, n := range []string{"identity", "folder", "shelf", "room", "team"} {
		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		ids[n] = id
	}
	// The adjacency write evaluateLinkFanOut performs, keyed by a Contract #1
	// six-segment link key: a fixture writing some other shape would be
	// exercising the walk against edges the pipeline cannot produce.
	link := func(rel, fromType, from, toType, to string) {
		linkKey := fmt.Sprintf("lnk.%s.%s.%s.%s.%s", fromType, ids[from], rel, toType, ids[to])
		for _, evt := range []adjacency.CoreKVEvent{
			{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "outbound",
				NodeID: ids[from], OtherNodeID: ids[to], OtherType: toType},
			{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "inbound",
				NodeID: ids[to], OtherNodeID: ids[from], OtherType: fromType},
		} {
			require.NoError(t, adjacency.Build(context.Background(), adjKV, evt))
		}
	}
	link("watches", "identity", "identity", "folder", "folder")
	link("owns", "identity", "identity", "shelf", "shelf")
	link("onShelf", "folder", "folder", "shelf", "shelf")
	link("leads", "identity", "identity", "team", "team")
	link("usesRoom", "team", "team", "room", "room")
	link("inRoom", "folder", "folder", "room", "room")

	return p, substrate.VertexKey("folder", ids["folder"])
}

// TestBranchDerivation_OneSharedBudgetAcrossTheWalks pins the budget's shape by
// the cap at which the lens declines, which is the only place the three possible
// implementations differ:
//
//   - three private budgets would decline at 3 (the widest single walk) and
//     succeed at 4 — but would also succeed at 3, where the shared one cannot;
//   - one shared counter with per-walk memos would need 6;
//   - one shared counter AND one shared memo needs 4, which is what it is.
//
// So "declines at 3, succeeds at 4" is the pair of assertions that admits only
// the third, and it is what makes "a wide lens declines ONCE rather than N
// times" a property rather than a claim.
func TestBranchDerivation_OneSharedBudgetAcrossTheWalks(t *testing.T) {
	p, folderKey := newBudgetFixture(t)
	ctx := context.Background()

	p.SetAnchorDerivationReadCap(4)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
	require.NoError(t, err)
	require.True(t, ok,
		"four documents is what the three walks read BETWEEN them; needing more means the neighbour memo is not shared")
	require.Len(t, derived, 1, "and the union still names the one identity all three walks reach")

	p.SetAnchorDerivationReadCap(3)
	_, ok, err = p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
	require.NoError(t, err)
	require.False(t, ok,
		"the widest single walk fits in three reads, so a per-walk budget would answer here — one shared budget must decline the LENS")

	// And that decline is a FALL-BACK, not an error and not a truncation: no
	// error to report, and no partial anchor set for the caller to act on — the
	// caller keeps the enumerator, which is the only safe reading of a walk that
	// ran out of budget.
	derived, ok, err = p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
	require.NoError(t, err, "a spent budget is not an error; it is the shape that says 'use the enumerator'")
	require.False(t, ok)
	require.Empty(t, derived, "a truncated set returned as an answer is the one failure this unit exists to avoid")

	// n <= 0 restores the default cap, which is generous enough for this graph.
	p.SetAnchorDerivationReadCap(0)
	derived, ok, err = p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, derived, 1)
}

// TestBranchDerivation_BudgetDoesNotOutliveOneEvent is the lifetime half (§5).
// A budget that survived its call would leave a lens that once declined
// declining for ever — one wide event poisoning every later one.
func TestBranchDerivation_BudgetDoesNotOutliveOneEvent(t *testing.T) {
	p, folderKey := newBudgetFixture(t)
	ctx := context.Background()

	p.SetAnchorDerivationReadCap(3)
	_, ok, err := p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
	require.NoError(t, err)
	require.False(t, ok, "precondition: this event really does spend the whole budget")

	p.SetAnchorDerivationReadCap(4)
	for i := 0; i < 3; i++ {
		derived, ok, err := p.deriveAnchorsForVertex(ctx, p.ruleState(), folderKey, "folder")
		require.NoError(t, err)
		require.Truef(t, ok, "event %d must start from a full budget", i)
		require.Len(t, derived, 1)
	}
}
