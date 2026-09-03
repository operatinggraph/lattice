// The multi-walk arm of the affected-anchor derivation
// (personal-lens-derivation-licence-design.md §4.5).
//
// A lens that compiles to several branches evaluates N independent queries and
// merges their rows. The derivation's question — "which anchors can this event's
// row set have moved" — is therefore asked of the LENS and answered by every
// branch: one index per branch, one walk per branch from that branch's own
// seeds, and the UNION of what they reach. The union is a superset of each
// branch's superset, which is the invariant the whole unit is built on
// (auth-plane-projection-latency-design.md §4.7).
//
// Two properties make the union the right composition rather than an
// approximation of one:
//
//   - a branch that does not bind the changed element at all seeds nothing and
//     contributes nothing, which is correct precisely because the branches that
//     DO bind it are walked in the same pass; and
//   - mergeBranchRows can make one branch's row depend on a sibling's, but
//     executeBranches re-runs EVERY branch for every actor the derivation names,
//     so an anchor reached through one branch is reprojected against all of them
//     (TestBranchUnion_MergeRestsOnEveryBranchRerunPerActor pins that reliance).
package pipeline

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The conjuncts that refuse a branch-carrying lens's per-branch index set
// WHOLE, as the substring an operator reads in `pipeline: anchor derivation
// cannot act on this lens`. Each names its own conjunct: an unnamed refusal
// logs a blank line, and a blank line is swallowed by the refusal latch whose
// zero value is also the empty string (noteStaticDerivationRefusal).
//
// They are refusals of the LENS, not of one walk, because the derived set is a
// union: a walk whose graph cannot answer contributes an unknown, and a union
// with an unknown in it is not a superset of anything.
//
// Written for whatever carries branches rather than for today's three personal
// lenses. Nothing restricts a branches spec to a Personal lens — the installer's
// gate is `len(branches) > 1` and branchmerge.go's own doc contemplates a
// hand-authored one — so these hold a population the corpus does not yet have.
const (
	// DerivationBranchIncompleteRefusal: one walk's pattern graph stopped at a
	// shape it could not read, so its hop list is a floor rather than the truth
	// and the anchors it does not reach are not evidence of absence.
	DerivationBranchIncompleteRefusal = "one of the lens's walks has no complete pattern graph"

	// DerivationBranchUnresolvedExpansionRefusal: a `*` position with no
	// resolved concrete set makes the walk PRUNE every far end it cannot
	// confirm, and pruning under-approximates — the one direction this unit
	// refuses. Declined whole, exactly as the single-walk arm declines it.
	DerivationBranchUnresolvedExpansionRefusal = "one of the lens's walks carries the `*` taxonomy-expansion sigil with no resolved concrete set"

	// DerivationBranchAnchorDisagreementRefusal is the checkable form of "each
	// branch carries its own anchor, and one graph cannot speak for all of
	// them". Where the branches really do anchor on different labels, the
	// union's keys would name two kinds of vertex and the walk's single anchor
	// prefix could not render both. Where they agree — every walk-generated
	// personal lens, whose branches are all compiled from the same constant
	// head — there is nothing to refuse.
	DerivationBranchAnchorDisagreementRefusal = "the lens's walks do not all anchor on the same vertex label"

	// derivationBranchUnnamedRefusal is the belt to the named conjuncts above.
	// No arm of AnchorHopIndex leaves Complete false with an empty Incomplete,
	// so no branch can reach it from the installer today; a HopIndex assembled
	// directly can, and G15's lesson is that the empty string is not a reason.
	derivationBranchUnnamedRefusal = "one of the lens's walks declined without naming a conjunct"
)

// The two conjuncts both arms share, spelled once so the single-walk and
// multi-walk arms cannot report the same refusal differently.
const (
	// derivationAnchorLabelRefusal: the anchor position's label must be the type
	// the enumerator enumerates, or the keys the walk builds name a different
	// kind of vertex than the evaluation binds.
	derivationAnchorLabelRefusal = "the anchor position's label is not the enumerator's actor type"

	// derivationUnnamedIndexRefusal is the belt to every named conjunct: a
	// single-walk graph that is incomplete and carries no reason of its own, and
	// the reason switch's own last resort. Distinct from
	// DerivationNoBranchIndexRefusal, which speaks about a lens compiling to
	// several walks and would be a wrong sentence here.
	derivationUnnamedIndexRefusal = "the lens's pattern graph declined without naming a conjunct"
)

// deriveBranchAnchorHops builds one anchor pattern graph per compiled branch and
// reports the conjunct that refuses the set whole, "" when every branch can
// answer and they agree about the anchor.
//
// It mirrors deriveWalkScope, two paragraphs below its call site in
// useFullEngineBranches, in both derivation and refusal shape: over EVERY
// branch, one unreadable branch refusing the lens, and the taxonomy expansion
// threaded in exactly as the single-walk arm threads it (a no-op when the map is
// nil, i.e. no `*` anywhere in this query).
//
// What it does NOT decide is whether the anchor label is the enumerator's actor
// type. That is not a property of the compiled rule: activation installs the
// ActorEnumerator after the rule is published, so a verdict taken here would
// read every pipeline as having none. It is asked live, in derivationIndexes.
func deriveBranchAnchorHops(engineKind string, rules []ruleengine.CompiledRule, expansion map[string]map[string]struct{}) ([]full.HopIndex, string) {
	if engineKind != ruleengine.EngineFull {
		return nil, DerivationNoBranchIndexRefusal
	}
	if len(rules) == 0 {
		return nil, DerivationNoBranchIndexRefusal
	}
	hops := make([]full.HopIndex, 0, len(rules))
	for _, c := range rules {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			return nil, DerivationNoBranchIndexRefusal
		}
		hops = append(hops, fullCR.AnchorHopIndex().WithLabelExpansion(expansion))
	}
	if refusal := branchAnchorHopsRefusal(hops); refusal != "" {
		return nil, refusal
	}
	return hops, ""
}

// branchAnchorHopsRefusal names the conjunct that makes a per-branch index set
// unusable, "" when it can answer. Split out from the builder so the refusal is
// one predicate over a set of graphs however the graphs were obtained — the
// installer resolves them from compiled branches, and the corpus census resolves
// them the same way through BranchDerivationRefusal.
func branchAnchorHopsRefusal(hops []full.HopIndex) string {
	if len(hops) == 0 {
		return DerivationNoBranchIndexRefusal
	}
	for i, h := range hops {
		if h.Complete {
			continue
		}
		reason := h.Incomplete
		if reason == "" {
			reason = derivationBranchUnnamedRefusal
		}
		return fmt.Sprintf("%s: walk %d: %s", DerivationBranchIncompleteRefusal, i, reason)
	}
	for i, h := range hops {
		if pos := h.UnresolvedExpansionPosition(); pos >= 0 {
			return fmt.Sprintf("%s: walk %d, pattern position %d — the walk would prune far ends it cannot confirm, which under-approximates",
				DerivationBranchUnresolvedExpansionRefusal, i, pos)
		}
	}
	first := hops[0].Labels[hops[0].Anchor]
	for i, h := range hops[1:] {
		if l := h.Labels[h.Anchor]; l != first {
			return fmt.Sprintf("%s: walk 0 anchors on %q, walk %d on %q",
				DerivationBranchAnchorDisagreementRefusal, first, i+1, l)
		}
	}
	return ""
}

// BranchDerivationRefusal reports why the affected-anchor derivation can hold no
// usable per-branch index set for a lens compiled to these branches, "" when it
// can.
//
// It exists for the corpus censuses, which pin what the derivation will READ for
// a lens rather than what each cypher's own graph says in isolation. They call
// the shipped predicate through this door instead of restating it, so a conjunct
// added above is pinned for free and neither side can answer the question
// differently.
//
// expanded is the taxonomy label expansion, and it is a PARAMETER rather than a
// nil the door supplies for itself because the installer threads one
// (useFullEngineBranches resolves it from the branch set's ExpansionLabels
// before building these graphs) and a census that passed nil would be answering
// about a different lens: a `*` position reads UNRESOLVED with no expansion and
// resolves with one, which is the difference between the whole lens being
// refused and it deriving. A caller with no expansion to offer passes nil, and
// gets the reading a pipeline whose resolver could not answer would get.
func BranchDerivationRefusal(branches []ruleengine.CompiledRule, expanded map[string]map[string]struct{}) string {
	_, refusal := deriveBranchAnchorHops(ruleengine.EngineFull, branches, expanded)
	return refusal
}

// multiWalkDerivationRefusal is the whole verdict for a branch-carrying lens:
// the conjuncts the publication already decided, plus the one that can only be
// asked live. "" means the derivation may act on this lens's per-branch set.
//
// One predicate, two consumers — derivationIndexes decides with it and
// noteStaticDerivationRefusal reports with it — because an index gated from one
// place and explained from another drifts, and the copy that drifts is the one
// an operator reads.
func (p *Pipeline) multiWalkDerivationRefusal(rs ruleState) string {
	if rs.anchorHopsPerBranchRefusal != "" {
		return rs.anchorHopsPerBranchRefusal
	}
	// Belt to the refusal string's brace. The pair is published together and a
	// refused set is nil, so an empty refusal beside an empty set can only be a
	// field that lost its line in the ruleState round trip — where the zero
	// value of the refusal is the ADMITTING answer and the zero value of the
	// set is a union over no walks at all, which returns an empty anchor set as
	// a real answer. Fail closed on the pair, not on either half.
	if len(rs.anchorHopsPerBranch) == 0 {
		return DerivationNoBranchIndexRefusal
	}
	// The anchor labels agree by the conjunct deriveBranchAnchorHops already
	// applied, so walk 0 speaks for all of them.
	h := rs.anchorHopsPerBranch[0]
	if p.actorEnumerator == nil || h.Labels[h.Anchor] != p.actorEnumerator.actorType {
		return derivationAnchorLabelRefusal
	}
	return ""
}

// derivationBudget is one EVENT's whole walk allowance, shared by every branch
// the union walks.
//
// It exists because the per-branch union would otherwise multiply the cost it is
// supposed to bound: reads, work and the neighbour memo were per-call locals in
// walkToAnchors, so three branches paid three times the adjacency reads and
// missed the memo three times over on the vertices their patterns share — and a
// wide lens declined three times instead of once.
//
// Its lifetime is exactly one derivation call, i.e. one CDC event: it is created
// by the deriveAnchorsFor* entry point and dies with it. It must not outlive
// that, or one wide event would poison the next — a budget carried across events
// would leave a lens permanently declining on a cap some earlier event spent.
type derivationBudget struct {
	// neighbours memoises one adjacency document per vertex per EVENT, across
	// branches. Keyed by vertex id, which means the same thing in every branch
	// — unlike a pattern position, which does not, and which is why `visited`
	// stays per-walk.
	neighbours map[string][]adjacency.EdgeEntry
	// reads and readCap bound the adjacency documents this event may read.
	reads   int
	readCap int
	// work and workBudget bound the adjacency ENTRIES the ranged closures may
	// iterate, which the memoising read cap cannot see (walkToAnchors' own doc
	// gives the arithmetic).
	work       int
	workBudget int
	// rangedReads tallies the reads the ranged closures performed, reported once
	// for the whole event by the entry point that owns this budget.
	rangedReads int
}

func (p *Pipeline) newDerivationBudget() *derivationBudget {
	readCap := p.derivationReadCap()
	return &derivationBudget{
		neighbours: map[string][]adjacency.EdgeEntry{},
		readCap:    readCap,
		workBudget: readCap * DerivationRangedWorkFactor,
	}
}

// walkBranches runs one walk per index from that index's own seeds and returns
// the UNION of the anchors they reach, under one shared budget.
//
// A single branch declining declines the whole lens: its contribution is
// unknown, and a union missing an unknown is not a superset. That is also what
// makes the shared budget honest — the lens declines ONCE, on the first breach,
// rather than paying N times to decline N times.
func (p *Pipeline) walkBranches(ctx context.Context, idxs []full.HopIndex, seedsFor func(full.HopIndex) []seededNode) ([]string, bool, error) {
	if len(idxs) == 0 {
		// Unreachable from the pipeline — derivationIndexes refuses an empty
		// set — and fail-closed rather than "a union over no walks is empty",
		// which would be an empty anchor set returned as a real answer.
		return nil, false, nil
	}
	budget := p.newDerivationBudget()
	defer func() {
		if budget.rangedReads > 0 {
			p.recordDerivationRangedReads(budget.rangedReads)
		}
	}()

	anchors := map[string]struct{}{}
	for _, idx := range idxs {
		seeds := seedsFor(idx)
		if len(seeds) == 0 {
			// This branch does not bind the changed element anywhere, so no
			// anchor of its own can have moved through it. Correct only because
			// the branches that DO bind it are walked in this same pass — the
			// union is what makes an empty contribution a real answer rather
			// than a hole.
			continue
		}
		reached, ok, err := p.walkToAnchors(ctx, idx, seeds, budget)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		for _, a := range reached {
			anchors[a] = struct{}{}
		}
	}
	out := make([]string, 0, len(anchors))
	for k := range anchors {
		out = append(out, k)
	}
	return out, true, nil
}

// walkOneIndex is walkBranches over a single pattern graph — one walk, one
// budget, the same disposition. It is what the plain arm runs, whose scan-root
// graph is single-walk by its own conjunct (plainDerivationIndex).
func (p *Pipeline) walkOneIndex(ctx context.Context, idx full.HopIndex, seeds []seededNode) ([]string, bool, error) {
	return p.walkBranches(ctx, []full.HopIndex{idx}, func(full.HopIndex) []seededNode { return seeds })
}
