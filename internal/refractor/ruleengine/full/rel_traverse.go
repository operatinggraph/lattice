package full

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// adjacencyNodeID maps a Core KV vertex key to the bare NodeID Adjacency KV is
// indexed by. A key that is not a Contract #1 vertex key is itself the NodeID
// (the test / legacy Materializer fixture path). It is the one derivation, so
// the batch that prefetches a set of nodes and the hop that reads one of them
// cannot come to disagree about which node was meant.
func adjacencyNodeID(key string) string {
	if _, nodeID, ok := substrate.ParseVertexKey(key); ok {
		return nodeID
	}
	return key
}

// otherEndKey reconstructs the Core KV key of the endpoint e leads to. An edge
// carrying OtherType follows the Contract #1 link convention, so the endpoint's
// full vtx key is built from it; otherwise OtherNodeID is itself the Core KV
// key (the test / legacy Materializer fixture path).
func otherEndKey(e adjacency.EdgeEntry) string {
	if e.OtherType != "" {
		return substrate.VertexPrefix + "." + e.OtherType + "." + e.OtherNodeID
	}
	return e.OtherNodeID
}

// hopCrosses reports whether a hop of rel from a node whose path has already
// visited `seen` crosses edge e to the endpoint at otherKey: the relation
// matches (an untyped hop crosses every relation), the direction matches, and
// the endpoint is one this path has not already visited.
//
// It is the single admission rule for the hop, so the batch that prefetches the
// frontier and the loop that binds it cannot come to differ about which edges
// this hop consumes.
func hopCrosses(e adjacency.EdgeEntry, rel RelPattern, seen map[string]struct{}, otherKey string) bool {
	if rel.Type != "" && e.Name != rel.Type {
		return false
	}
	if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
		return false
	}
	_, visited := seen[otherKey]
	return !visited
}

// traverseRel expands one relationship hop (possibly variable-length).
func (ex *executor) traverseRel(b binding, from *nodeRef, rel RelPattern, to NodePattern) ([]binding, error) {
	// Every candidate this hop fetches — the ones it binds, the ones it finds
	// tombstoned, and the ones the label, the property predicates or the
	// already-visited rule reject — was fetched to expand b, so all of them
	// land on b's chain and are inherited by every row the hop produces. A
	// ranged hop's later frontiers record there too: a candidate rejected at
	// hop 2 belongs to each row descending from this one head.
	prevHead := ex.provHead
	ex.provHead = provChain(b)
	defer func() { ex.provHead = prevHead }()

	minHops := rel.MinHops
	maxHops := rel.MaxHops
	if maxHops < 0 || maxHops > maxVarLengthHops {
		maxHops = maxVarLengthHops
	}
	if minHops < 0 {
		minHops = 0
	}

	type frontier struct {
		node *nodeRef
		seen map[string]struct{}
	}
	starts := []frontier{{node: from, seen: map[string]struct{}{from.key: {}}}}

	// A hit is one admitted endpoint together with the edge the walk crossed to
	// reach it — the relationship a named rel variable binds. edge is nil for
	// the zero-hop admission of `from`, which crosses no edge at all.
	type hit struct {
		node *nodeRef
		edge *adjacency.EdgeEntry
	}

	var matched []hit
	// Hop 0 means "from itself" — admit if minHops==0 and to filters allow.
	admit := func(ref *nodeRef) (bool, error) {
		if !ex.nodeMatches(ref, to) {
			return false, nil
		}
		ok, err := ex.propsAllMatch(b, ref, to)
		if err != nil {
			return false, err
		}
		return ok, nil
	}

	if minHops == 0 {
		ok, err := admit(from)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, hit{node: from})
		}
	}

	current := starts
	for hop := 1; hop <= maxHops; hop++ {
		// Every node this hop steps FROM is known before the loop, so their
		// adjacency is one batch rather than one node-state read apiece. It
		// matters from hop 2 on: hop 1 steps from a single node, and a ranged
		// hop's later frontiers are as wide as the walk has fanned out.
		frontierNodes := make([]string, 0, len(current))
		for _, f := range current {
			frontierNodes = append(frontierNodes, adjacencyNodeID(f.node.key))
		}
		if err := ex.prefetchEdges(frontierNodes, rel.Type); err != nil {
			return nil, err
		}
		var nextFrontier []frontier
		for _, f := range current {
			adjLookupID := adjacencyNodeID(f.node.key)
			// The hop reads this frontier node's adjacency, so a link written
			// or tombstoned on it moves what the walk crosses. It is recorded
			// by the node's own Core KV key: the adjacency NodeID carries no
			// vertex type and so names no vertex on its own.
			ex.recordProv(f.node.key)
			edges, err := ex.fetchEdges(adjLookupID, rel.Type)
			if err != nil {
				return nil, fmt.Errorf("full engine: neighbors(%s): %w", adjLookupID, err)
			}
			ex.recordEdgeSelector(adjLookupID, rel, edges)
			// The whole frontier this hop admits is read in ONE batch before
			// any of it is bound, so a node carrying thousands of admitted
			// edges costs a round trip per chunk rather than one per
			// neighbour. The binding loop below is unchanged — its fetchNode
			// calls are served by what the batch staged.
			frontierKeys := make([]string, 0, len(edges))
			for _, e := range edges {
				if other := otherEndKey(e); hopCrosses(e, rel, f.seen, other) {
					frontierKeys = append(frontierKeys, other)
				}
			}
			if err := ex.prefetchNodes(frontierKeys); err != nil {
				return nil, err
			}
			for _, e := range edges {
				otherCoreKey := otherEndKey(e)
				if !hopCrosses(e, rel, f.seen, otherCoreKey) {
					continue
				}
				neighbor, err := ex.fetchNode(otherCoreKey)
				if err != nil {
					return nil, err
				}
				if neighbor == nil {
					continue
				}
				if hop >= minHops {
					ok, err := admit(neighbor)
					if err != nil {
						return nil, err
					}
					if ok {
						edge := e
						matched = append(matched, hit{node: neighbor, edge: &edge})
					}
				}
				// Extend frontier for next hop.
				ns := make(map[string]struct{}, len(f.seen)+1)
				for k := range f.seen {
					ns[k] = struct{}{}
				}
				ns[neighbor.key] = struct{}{}
				nextFrontier = append(nextFrontier, frontier{node: neighbor, seen: ns})
			}
		}
		current = nextFrontier
		if len(current) == 0 {
			break
		}
	}

	// Deduplicate matched — the same target is reachable via multiple paths.
	//
	// What a row IS decides what makes two of them the same. Where the pattern
	// names no relationship variable, a row is the endpoint alone, so every
	// path to one endpoint collapses to a single row. Where it names one, the
	// relationship is part of the row too: two distinct links to the same
	// neighbour are two different bindings of that variable, and collapsing
	// them by endpoint would drop one of the links the pattern matched.
	seen := map[string]bool{}
	var unique []hit
	for _, m := range matched {
		dedupeKey := m.node.key
		if rel.Variable != "" && m.edge != nil {
			dedupeKey += "\x00" + m.edge.CoreKvKey
		}
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		unique = append(unique, m)
	}

	out := make([]binding, 0, len(unique))
	for _, m := range unique {
		n := m.node
		// If the destination variable is already bound in this binding, the
		// traversal must arrive at the same node (constrained-target case,
		// e.g. `(report)<-[:reportsTo]-(identity)` where identity is already
		// bound from a prior clause).
		if to.Variable != "" {
			if existing, ok := b[to.Variable]; ok {
				ex, _ := existing.(*nodeRef)
				if ex == nil || ex.key != n.key {
					continue
				}
			}
		}
		var relRef *nodeRef
		if rel.Variable != "" {
			// An expansion that crossed no edge (the zero-hop admission of
			// `from`) has no relationship to bind, so a pattern naming one
			// yields no row for it. Validate refuses a rel variable on a
			// variable-length hop, which is the only pattern that reaches
			// here that way.
			if m.edge == nil {
				continue
			}
			// An adjacency entry is not guaranteed to carry a Contract #1 link
			// key: the legacy event path indexes an edge off any Core KV
			// message carrying a nodeId, taking coreKvKey and name verbatim
			// from the body (adjacency's own overflow latch refuses to engage
			// on a node holding such edges for the same reason). Binding one
			// would project an empty or malformed key, and a payload
			// dereference would point-read it and fail the whole evaluation.
			// The expansion is dropped instead, exactly as a zero-hop one is.
			if substrate.ClassifyKey(m.edge.CoreKvKey) != substrate.KindLink {
				continue
			}
			// The relationship binding is built from the adjacency entry the
			// walk already holds: the link key and the relation name, no read.
			relRef = &nodeRef{key: m.edge.CoreKvKey, rel: m.edge.Name}
			// The destination variable's constrained-target rule, applied to
			// the relationship: a rel variable already bound in this binding
			// must resolve to the same link, or this expansion is not the one
			// the pattern names.
			if existing, ok := b[rel.Variable]; ok {
				bound, _ := existing.(*nodeRef)
				if bound == nil || bound.key != relRef.key {
					continue
				}
			}
		}
		nb := cloneBinding(b)
		if to.Variable != "" {
			nb[to.Variable] = n
		}
		if rel.Variable != "" {
			nb[rel.Variable] = relRef
		}
		out = append(out, nb)
	}
	return out, nil
}
