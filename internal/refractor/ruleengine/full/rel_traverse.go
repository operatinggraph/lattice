package full

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// traverseRel expands one relationship hop (possibly variable-length).
func (ex *executor) traverseRel(b binding, from *nodeRef, rel RelPattern, to NodePattern) ([]binding, error) {
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
		var nextFrontier []frontier
		for _, f := range current {
			// Adjacency KV is indexed by bare NodeID, not full Contract #1
			// vertex keys. When f.node.key is a Contract #1 vtx key, extract
			// the NodeID; otherwise treat the key as a bare NodeID (test /
			// legacy Materializer fixture path).
			adjLookupID := f.node.key
			if _, nodeID, ok := substrate.ParseVertexKey(f.node.key); ok {
				adjLookupID = nodeID
			}
			edges, err := ex.fetchEdges(adjLookupID)
			if err != nil {
				return nil, fmt.Errorf("full engine: neighbors(%s): %w", adjLookupID, err)
			}
			ex.recordEdgeSelector(adjLookupID, rel, edges)
			for _, e := range edges {
				if rel.Type != "" && e.Name != rel.Type {
					continue
				}
				if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
					continue
				}
				// Reconstruct the OTHER endpoint's Core KV key. If the edge
				// carries OtherType (Contract #1 link convention), build the
				// full vtx key; otherwise the OtherNodeID itself is the
				// Core KV key (Materializer-style fixture path).
				otherCoreKey := e.OtherNodeID
				if e.OtherType != "" {
					otherCoreKey = substrate.VertexPrefix + "." + e.OtherType + "." + e.OtherNodeID
				}
				if _, seen := f.seen[otherCoreKey]; seen {
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
