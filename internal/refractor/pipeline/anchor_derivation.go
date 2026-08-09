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

// SetAnchorDerivationReadCap overrides the per-walk adjacency read budget.
// n <= 0 restores the default. It exists so an operator can bound the
// derivation's cost on a live lens without a redeploy, and so a test can reach
// the fallback without building a graph of thousands of vertices.
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
	idx, ready := p.derivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	_, id, parsed := substrate.ParseVertexKey(vertexKey)
	if !parsed {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, pos := range idx.PositionsBinding(vertexType) {
		seeds = append(seeds, seededNode{pos: pos, id: id})
	}
	return p.walkToAnchors(ctx, idx, seeds)
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
// hop, so no anchor's output can change.
func (p *Pipeline) deriveAnchorsForLink(ctx context.Context, rs ruleState, linkKey string) ([]string, bool, error) {
	idx, ready := p.derivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	srcType, srcID, rel, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, s := range idx.AnchorSideSeeds(srcType, rel, dstType) {
		id := dstID
		if s.SrcIsAnchorSide {
			id = srcID
		}
		seeds = append(seeds, seededNode{pos: s.Pos, id: id})
	}
	return p.walkToAnchors(ctx, idx, seeds)
}

// derivationIndex returns the compiled pattern graph to walk under, and whether
// this pipeline may use one at all. Both conjuncts beyond HopIndex.Complete are
// about this pipeline rather than the query:
//
//   - an ActorEnumerator must be installed, because the derived set is only
//     ever compared with — or substituted for — its answer; and
//   - the anchor position's own label must be the enumerator's actor type, or
//     the keys this walk builds would name a different kind of vertex than the
//     one the evaluation binds.
func (p *Pipeline) derivationIndex(rs ruleState) (full.HopIndex, bool) {
	if p.actorEnumerator == nil {
		return full.HopIndex{}, false
	}
	if !rs.anchorHops.Complete {
		return full.HopIndex{}, false
	}
	if l := rs.anchorHops.Labels[rs.anchorHops.Anchor]; l != p.actorEnumerator.actorType {
		return full.HopIndex{}, false
	}
	return rs.anchorHops, true
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
func (p *Pipeline) walkToAnchors(ctx context.Context, idx full.HopIndex, seeds []seededNode) ([]string, bool, error) {
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

	// One adjacency document per vertex per walk, however many positions that
	// vertex is reached at.
	neighbours := map[string][]adjacency.EdgeEntry{}
	readCap := p.derivationReadCap()
	reads := 0
	edgesOf := func(id string) ([]adjacency.EdgeEntry, error) {
		if edges, cached := neighbours[id]; cached {
			return edges, nil
		}
		if reads >= readCap {
			return nil, errDerivationTooWide
		}
		reads++
		edges, _, err := adjacency.Neighbors(ctx, p.adjKV, id)
		if err != nil {
			return nil, fmt.Errorf("pipeline: anchor derivation: neighbours of %q: %w", id, err)
		}
		neighbours[id] = edges
		return edges, nil
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
			for _, e := range edges {
				if e.Name != step.Rel || !adjacency.DirectionMatches(e.Direction, step.EdgeDir) {
					continue
				}
				// An edge entry with no OtherType is a legacy typeless edge:
				// the far end's type is unknown, so the pattern's label cannot
				// rule it out and the walk keeps it. A `*` far end prunes by
				// set membership against its taxonomy-resolved downward
				// closure instead of string equality — and, symmetrically to
				// full/executor.go's nodeMatches, an unresolved expansion
				// (step.ToExpanded == nil) fails closed by pruning, never by
				// falling back to ToLabel's bare (and possibly abstract, so
				// meaningless as a key type) string.
				if step.ToLabelExpand {
					if e.OtherType != "" {
						if step.ToExpanded == nil {
							continue
						}
						if _, hit := step.ToExpanded[e.OtherType]; !hit {
							continue
						}
					}
				} else if step.ToLabel != "" && e.OtherType != "" && e.OtherType != step.ToLabel {
					continue
				}
				next := seededNode{pos: step.ToPos, id: e.OtherNodeID}
				if _, seen := visited[next]; seen {
					continue
				}
				visited[next] = struct{}{}
				queue = append(queue, next)
			}
		}
	}

	out := make([]string, 0, len(anchors))
	for k := range anchors {
		out = append(out, k)
	}
	return out, true, nil
}

// errDerivationTooWide is the sentinel for the read cap. It never escapes
// walkToAnchors — it is translated into ok == false, i.e. "fall back".
var errDerivationTooWide = fmt.Errorf("pipeline: anchor derivation: adjacency read cap reached")
