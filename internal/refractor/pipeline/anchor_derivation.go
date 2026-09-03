// Pattern-directed affected-anchor derivation — the data half of
// auth-plane-projection-latency-design.md §4.7. The structural half is
// full.HopIndex; this walks the live adjacency graph under its direction,
// seeded by the CHANGED ELEMENT rather than by a neighbouring vertex, so a
// link mutation costs a handful of relation-filtered adjacency reads instead of
// an undirected depth-10 BFS plus a cypher execution per actor it happened to
// touch.
//
// The invariant is a SUPERSET and it is directional: under-approximation on the
// auth plane is a stale grant, and an over-grant when the missed event was a
// revocation. Every shape the derivation cannot resolve returns ok == false and
// the caller keeps the shipped ActorEnumerator BFS, unchanged — so a derivation
// bug degrades to current behaviour rather than to silence.
package pipeline

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultDerivationReadCap caps the adjacency documents one derivation may
// read. It is a FALLBACK trigger, not a truncation: a walk that hits it returns
// ok == false and the caller runs the BFS. Truncating instead would silently
// return a subset, which is the one failure this whole unit exists to avoid.
//
// The bound is generous relative to what the shapes in §16.4 cost (a handful of
// reads each) and small relative to the BFS it replaces, whose own actor cap is
// 10_000.
const DefaultDerivationReadCap = 2_000

// DerivationRangedWorkFactor multiplies the read cap into the ranged closures'
// work budget: the adjacency ENTRIES they may iterate, as against the documents
// they may read. The two differ because edgesOf memoises, so a re-entered
// closure re-walks cached edge lists at no read cost at all.
//
// It is a multiple rather than its own constant so one operator knob still
// governs the walk's whole cost envelope: SetAnchorDerivationReadCap moves both
// together. The factor is the average degree above which iterating is no longer
// cheap relative to the read that fetched the list — generous against the
// shapes the derivation is built for (a containment chain's handful of edges
// per vertex) and far below the measured pathology (86,050 entries for 1,023
// reads on a binary containment tree).
const DerivationRangedWorkFactor = 16

// SetAnchorDerivationReadCap overrides the adjacency read budget one derivation
// may spend — per EVENT, shared by every branch of a multi-walk lens's union
// (derivationBudget), not per walk. n <= 0 restores the default. It exists so an
// operator can bound the derivation's cost on a live lens without a redeploy,
// and so a test can reach the fallback without building a graph of thousands of
// vertices.
func (p *Pipeline) SetAnchorDerivationReadCap(n int) {
	p.derivReadCap.Store(int64(n))
}

func (p *Pipeline) derivationReadCap() int {
	if n := p.derivReadCap.Load(); n > 0 {
		return int(n)
	}
	return DefaultDerivationReadCap
}

// deriveAnchorsForVertex returns the anchor keys whose projection a mutation of
// (vertexType, vertexKey) can change. ok == false means the derivation declined
// and the caller must fall back.
func (p *Pipeline) deriveAnchorsForVertex(ctx context.Context, rs ruleState, vertexKey, vertexType string) ([]string, bool, error) {
	idxs, ready := p.derivationIndexes(rs)
	if !ready {
		return nil, false, nil
	}
	_, id, parsed := substrate.ParseVertexKey(vertexKey)
	if !parsed {
		return nil, false, nil
	}
	// Seeded per pattern graph, because a position index means nothing outside
	// the graph that numbered it: a multi-walk lens's branches bind the changed
	// type at different positions, and a branch that does not bind it at all
	// seeds nothing.
	return p.walkBranches(ctx, idxs, func(idx full.HopIndex) []seededNode {
		var seeds []seededNode
		for _, pos := range idx.PositionsBinding(vertexType) {
			seeds = append(seeds, seededNode{pos: pos, id: id})
		}
		return seeds
	})
}

// deriveAnchorsForAspect is deriveAnchorsForVertex seeded by the aspect's
// PARENT vertex: an aspect mutation changes what the parent's node properties
// render, and the pattern binds the parent, never the aspect key itself.
func (p *Pipeline) deriveAnchorsForAspect(ctx context.Context, rs ruleState, aspectKey string) ([]string, bool, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(aspectKey)
	if !ok {
		return nil, false, nil
	}
	return p.deriveAnchorsForVertex(ctx, rs, parentVtx, parentType)
}

// deriveAnchorsForLink returns the anchor keys a link create or tombstone can
// affect, seeded at the ANCHOR-SIDE endpoint of every pattern hop the link can
// bind (§4.7's 3a). The far endpoint's other edges are never traversed, which
// is what turns `capabilityRoles`' holdsRole event from sixty executions into
// one.
//
// An empty derived set on a complete index is a real answer — the link binds no
// hop, so no anchor's output can change. The same reading is what makes the
// multi-walk union sound at its own boundary: AnchorSideSeeds is exact only for
// the pattern positions the changed link BINDS, so a branch whose pattern never
// mentions the link seeds nothing and contributes nothing — correct precisely
// because every branch that does mention it is walked in the same pass and
// unioned in.
func (p *Pipeline) deriveAnchorsForLink(ctx context.Context, rs ruleState, linkKey string) ([]string, bool, error) {
	idxs, ready := p.derivationIndexes(rs)
	if !ready {
		return nil, false, nil
	}
	srcType, srcID, rel, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		return nil, false, nil
	}
	return p.walkBranches(ctx, idxs, func(idx full.HopIndex) []seededNode {
		var seeds []seededNode
		for _, s := range idx.AnchorSideSeeds(srcType, rel, dstType) {
			id := dstID
			if s.SrcIsAnchorSide {
				id = srcID
			}
			seeds = append(seeds, seededNode{pos: s.Pos, id: id})
		}
		return seeds
	})
}

// derivationIndexes returns the compiled pattern graph(s) to walk under, and
// whether this pipeline may use them at all. One for an ordinary lens; one per
// branch for a lens that compiles to several walks, whose derived set is the
// UNION of what they reach (anchor_derivation_branches.go).
//
// Two of the conjuncts beyond HopIndex.Complete are about this pipeline rather
// than the query:
//
//   - an ActorEnumerator must be installed, because the derived set is only
//     ever compared with — or substituted for — its answer; and
//   - the anchor position's own label must be the enumerator's actor type, or
//     the keys this walk builds would name a different kind of vertex than the
//     one the evaluation binds.
//
// The remaining one is about the taxonomy: a `*` position with no resolved
// expansion makes the walk PRUNE every far end it cannot confirm
// (UnresolvedExpansionPosition's doc), and an empty derived set on a Complete
// index is read as a real answer with no BFS behind it. So the index is
// declined whole, exactly like every other shape it cannot resolve.
//
// The multi-walk arm asks the same questions of every branch and refuses the
// LENS when any of them declines, because the answer is a union: a branch whose
// graph cannot answer contributes an unknown, and a union carrying an unknown is
// a superset of nothing. Its conjuncts live in multiWalkDerivationRefusal, which
// noteStaticDerivationRefusal reports from, so the gate and the operator log
// cannot disagree about which one refused.
func (p *Pipeline) derivationIndexes(rs ruleState) ([]full.HopIndex, bool) {
	if p.derivationIndexRefusal(rs) != "" {
		return nil, false
	}
	if len(rs.branches) > 1 {
		return rs.anchorHopsPerBranch, true
	}
	return []full.HopIndex{rs.anchorHops}, true
}

// derivationIndexRefusal names the conjunct that leaves this pipeline without a
// usable pattern graph, "" when it has one. It is the gate (derivationIndexes),
// the operator log line (noteStaticDerivationRefusal) and the control-plane
// health answer (PersonalDerivationStatus) reading ONE predicate: an index gated
// from one place and explained from another drifts, and the copy that drifts is
// the one an operator acts on.
//
// Every branch of it names itself. The empty string is reserved for "the index
// answers" — it is never a reason, because the refusal latch's own zero value is
// also "" and a blank reason is swallowed rather than printed.
func (p *Pipeline) derivationIndexRefusal(rs ruleState) string {
	if len(rs.branches) > 1 {
		return p.multiWalkDerivationRefusal(rs)
	}
	if !rs.anchorHops.Complete {
		if reason := rs.anchorHops.Incomplete; reason != "" {
			return reason
		}
		// A single-walk rule carrying no graph at all. No arm of AnchorHopIndex
		// leaves Complete false with an empty reason, so this is the belt to
		// those conjuncts' brace: an unnamed refusal logs a blank line, which is
		// visible and fixable, rather than nothing at all.
		return derivationUnnamedIndexRefusal
	}
	if pos := rs.anchorHops.UnresolvedExpansionPosition(); pos >= 0 {
		return fmt.Sprintf("pattern position %d carries the `*` taxonomy-expansion sigil with no resolved concrete set — the walk would prune far ends it cannot confirm, which under-approximates", pos)
	}
	// Last because it is the one conjunct that is not a property of the compiled
	// rule: the ActorEnumerator is installed after the rule is published, so a
	// pipeline that has not finished installing reads exactly like one whose
	// anchor names the wrong kind of vertex, and both must refuse.
	if p.actorEnumerator == nil || rs.anchorHops.Labels[rs.anchorHops.Anchor] != p.actorEnumerator.actorType {
		return derivationAnchorLabelRefusal
	}
	return ""
}

// seededNode is a starting point for the walk: a vertex id, bound at a
// particular pattern position.
type seededNode struct {
	pos int
	id  string
}

// walkToAnchors runs the joint (position, vertex) breadth-first walk from the
// seeds to the anchor position, and returns the anchor vertex keys it reaches.
//
// Two properties make this a superset of the truly-affected anchors:
//
//   - it follows EVERY hop incident to the position it stands at, not one
//     chosen chain, so a pattern path it could take is never skipped; and
//   - it prunes an edge only on evidence the pattern itself carries — the
//     relation name, the arrow's direction, and the far end's label. An edge
//     whose OtherType the adjacency entry does not record is kept, because
//     "cannot confirm the label" must widen the set, not narrow it.
//
// The label comparison matches a pattern label against the vertex KEY TYPE,
// which is what a label means everywhere: bare equality for an ordinary
// position, or membership in the taxonomy-resolved downward closure
// (HopIndex.admitsType) for a `*` position — the same reading full/executor.
// go's nodeMatches applies (dynamic-type-taxonomy-design.md §5.1). So a
// vertex this walk prunes on the far end's label is one the executor could
// not have bound either, and the pruning stays inside the superset property
// above.
//
// It does NOT expand from a node reached at the anchor position. That is sound
// because the anchor position is pinned by `{key: $actorKey}`: within one
// evaluation it binds exactly one vertex, so a realized path from some OTHER
// anchor to the changed element cannot pass through this one.
//
// The budget is the EVENT's, not this walk's: the reads, the ranged work and the
// neighbour memo are shared with every sibling branch walked for the same event
// (derivationBudget). One walk exhausting it declines the whole lens, which is
// the point — a wide lens declines once rather than once per branch, and the
// vertices two branches share are read once between them.
func (p *Pipeline) walkToAnchors(ctx context.Context, idx full.HopIndex, seeds []seededNode, budget *derivationBudget) ([]string, bool, error) {
	// Labels[Anchor] is always a bare (non-`*`) label here: AnchorHopIndex
	// refuses Complete when the anchor position itself carries LabelExpand
	// (hopindex.go), because a realized anchor vertex's own concrete type is
	// never learned by this walk — seededNode carries only a bare NanoID,
	// not the type an edge's OtherType recorded on the hop that reached it —
	// so one literal key prefix could not be right for an expanded anchor's
	// several possible concrete types. Concretely: derivationIndex requires
	// Complete, so this function never runs against such a query at all.
	anchorPrefix := substrate.VertexPrefix + "." + idx.Labels[idx.Anchor] + "."

	anchors := map[string]struct{}{}
	visited := make(map[seededNode]struct{}, len(seeds))
	queue := make([]seededNode, 0, len(seeds))
	for _, s := range seeds {
		if _, dup := visited[s]; dup {
			continue
		}
		visited[s] = struct{}{}
		queue = append(queue, s)
	}

	// One adjacency document per vertex per EVENT, however many positions that
	// vertex is reached at and however many branches reach it.
	edgesOf := func(id string) ([]adjacency.EdgeEntry, error) {
		if edges, cached := budget.neighbours[id]; cached {
			return edges, nil
		}
		if budget.reads >= budget.readCap {
			return nil, errDerivationTooWide
		}
		budget.reads++
		edges, _, err := adjacency.Neighbors(ctx, p.adjKV, p.coreKV, id)
		if err != nil {
			return nil, fmt.Errorf("pipeline: anchor derivation: neighbours of %q: %w", id, err)
		}
		budget.neighbours[id] = edges
		return edges, nil
	}

	// admit is the single entry point into the walk's frontier, so the global
	// (position, vertex) guard is applied identically by the fixed-hop move and
	// by the ranged closure.
	admit := func(n seededNode) {
		if _, seen := visited[n]; seen {
			return
		}
		visited[n] = struct{}{}
		queue = append(queue, n)
	}

	// budget.rangedReads is the adjacency documents the ranged closures below
	// read, tallied for the whole EVENT and reported once by the entry point
	// that owns the budget, however the walks exit — including the read-cap
	// exit, which is the firing rate worth seeing. budget.work counts the
	// adjacency entries the ranged closures iterate, which is what the memoising
	// read cap cannot see (its doc above).

	// expandRanged walks one ranged step's bounded frontier and returns the
	// adjacency documents it read. Standing on cur.id at cur.pos, it admits at
	// step.ToPos every node the executor's own frontier (full.traverseRel)
	// could reach across step.Rel/step.EdgeDir in between step.Min and
	// step.Max hops, plus cur.id itself when step.Min is 0.
	//
	// Three properties are load-bearing, each because its opposite would
	// UNDER-approximate — the one direction this unit refuses:
	//
	//   - The far-end label prune runs at ADMISSION ONLY. Intermediates extend
	//     the frontier on the relation and direction filter alone, exactly as
	//     traverseRel's nodeMatches runs only where it admits. A ranged hop's
	//     intermediates bind arbitrary types, so pruning them by the TERMINAL
	//     position's label drops paths the executor walks, i.e. drops anchors,
	//     i.e. drops a revocation.
	//   - The bound is step.Max, which the index already clamped PER RANGED HOP
	//     to the executor's own maxVarLengthHops. There is no whole-walk depth
	//     budget: a pattern chaining a fixed hop, a ten-hop range and another
	//     fixed hop is twelve graph hops, and a global depth cap would
	//     under-approximate it.
	//   - It reads only through edgesOf, so DefaultDerivationReadCap governs its
	//     I/O, and a breach returns errDerivationTooWide, which the caller turns
	//     into ok == false and the enumerator BFS. Never a truncation.
	//
	// The read cap bounds I/O and NOT work, and the difference is measurable:
	// edgesOf memoises, so a walk that re-enters this closure once per admitted
	// node re-iterates cached edge lists for free as far as the read cap is
	// concerned. On a 1,023-vertex containment tree that is 4,092 expansions and
	// 86,050 edge visits to derive one anchor — twelve times what the BFS this
	// replaces costs — and on a wide layered graph it reaches seconds of CPU at
	// 40% of the read cap. workBudget therefore bounds the edge entries the
	// ranged closures may iterate, and it is a FALLBACK trigger on exactly the
	// read cap's terms: a breach raises errDerivationTooWide, so the walk
	// degrades to the enumerator rather than returning a smaller set. It can
	// only ever cause MORE fallback, never a narrower answer.
	//
	// This closure is NOT equivalent to traverseRel and does not try to be.
	// traverseRel carries a per-path `seen` and enumerates paths; this walk
	// carries a global `visited` keyed by (position, vertex) and enumerates
	// reachability, so what it derives is a SUPERSET of the anchors the
	// executor's paths can reach. The superset is what the invariant needs.
	//
	// The frontier's own guard is keyed by (vertex, hop) and dies with the
	// call — no state outlives the walk. The hop belongs in the key only where
	// a step's lower bound exceeds one: such a step can reach a node both below
	// Min and at or above it, and a guard keyed by vertex alone would let the
	// first sighting suppress the admissible one. At Min <= 1 — every shape
	// AnchorHopIndex admits, since it refuses a higher lower bound — admission
	// precedes the guard and the two keys produce the same set, so the hop is
	// dropped and the guard costs one entry per vertex instead of Max.
	type frontierNode struct {
		id  string
		hop int
	}
	expandRanged := func(cur seededNode, step full.PatternStep) (int, error) {
		readsBefore := budget.reads
		// A step with no hop to take crosses no edge. StepsFrom never builds
		// one, but this closure also serves a caller assembling a HopIndex
		// directly, where silently admitting only the standing node would be an
		// under-approximation rather than a no-op.
		if step.Max < 1 {
			return 0, nil
		}
		hopInKey := step.Min > 1
		// The zero-hop admission: `*0..` binds the far position to the
		// standing node itself, crossing no edge (rel_traverse.go's minHops ==
		// 0 arm). No adjacency entry names this node's type, so the far-end
		// prune sees an unknown type and keeps it — "cannot confirm the label"
		// widens the set, never narrows it.
		if step.Min == 0 && stepAdmitsFarEnd(step, "") {
			admit(seededNode{pos: step.ToPos, id: cur.id})
		}
		seen := map[frontierNode]struct{}{{id: cur.id}: {}}
		frontier := []string{cur.id}
		for hop := 1; hop <= step.Max && len(frontier) > 0; hop++ {
			var next []string
			for _, id := range frontier {
				edges, err := edgesOf(id)
				if err != nil {
					return budget.reads - readsBefore, err
				}
				budget.work += len(edges)
				if budget.work > budget.workBudget {
					return budget.reads - readsBefore, errDerivationTooWide
				}
				for _, e := range edges {
					if !edgeTakesStep(step, e) {
						continue
					}
					if hop >= step.Min && stepAdmitsFarEnd(step, e.OtherType) {
						admit(seededNode{pos: step.ToPos, id: e.OtherNodeID})
					}
					fn := frontierNode{id: e.OtherNodeID}
					if hopInKey {
						fn.hop = hop
					}
					if _, dup := seen[fn]; dup {
						continue
					}
					seen[fn] = struct{}{}
					next = append(next, e.OtherNodeID)
				}
			}
			frontier = next
		}
		return budget.reads - readsBefore, nil
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.pos == idx.Anchor {
			anchors[anchorPrefix+cur.id] = struct{}{}
			continue
		}

		steps := idx.StepsFrom(cur.pos)
		if len(steps) == 0 {
			continue
		}
		edges, err := edgesOf(cur.id)
		if err != nil {
			if err == errDerivationTooWide {
				return nil, false, nil
			}
			return nil, false, err
		}
		for _, step := range steps {
			if step.Min == 1 && step.Max == 1 {
				for _, e := range edges {
					if !edgeTakesStep(step, e) || !stepAdmitsFarEnd(step, e.OtherType) {
						continue
					}
					admit(seededNode{pos: step.ToPos, id: e.OtherNodeID})
				}
				continue
			}
			n, err := expandRanged(cur, step)
			budget.rangedReads += n
			if err != nil {
				if err == errDerivationTooWide {
					return nil, false, nil
				}
				return nil, false, err
			}
		}
	}

	out := make([]string, 0, len(anchors))
	for k := range anchors {
		out = append(out, k)
	}
	return out, true, nil
}

// edgeTakesStep reports whether an adjacency entry is the relationship this
// step moves along: the pattern's relation name, read in the direction
// edgeDirFor resolved for the end the walk is standing on.
func edgeTakesStep(step full.PatternStep, e adjacency.EdgeEntry) bool {
	return e.Name == step.Rel && adjacency.DirectionMatches(e.Direction, step.EdgeDir)
}

// stepAdmitsFarEnd applies the pattern's own evidence about the node being
// admitted at step.ToPos: otherType is that node's vertex KEY TYPE as the
// adjacency entry recorded it, or "" where nothing recorded it.
//
// An empty otherType is a legacy typeless edge (or the zero-hop admission,
// which crosses no edge at all): the type is unknown, so the pattern's label
// cannot rule the node out and the walk keeps it. "Cannot confirm the label"
// must widen the set, not narrow it.
//
// A `*` far end prunes by membership in its taxonomy-resolved downward closure
// instead of by string equality. An unresolved expansion (ToExpanded == nil)
// prunes, and pruning is the UNSOUND direction for this walk — which is why
// derivationIndex declines an index carrying such a position before the walk
// ever starts, leaving that arm unreachable from the pipeline. It stays as
// written for a caller that builds a HopIndex directly, where pruning is still
// better than falling back to ToLabel's bare (and possibly abstract, so
// meaningless as a key type) string.
//
// Both the fixed single-hop move and the ranged closure's ADMISSION read this
// one function. The prune is written once deliberately: two copies drift, and
// the copy that drifts toward pruning more is the copy that drops an anchor.
func stepAdmitsFarEnd(step full.PatternStep, otherType string) bool {
	if step.ToLabelExpand {
		if otherType == "" {
			return true
		}
		if step.ToExpanded == nil {
			return false
		}
		_, hit := step.ToExpanded[otherType]
		return hit
	}
	return step.ToLabel == "" || otherType == "" || otherType == step.ToLabel
}

// errDerivationTooWide is the sentinel for the read cap. It never escapes
// walkToAnchors — it is translated into ok == false, i.e. "fall back".
var errDerivationTooWide = fmt.Errorf("pipeline: anchor derivation: adjacency read cap reached")
