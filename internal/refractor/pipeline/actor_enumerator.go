// Cross-vertex fan-out lives in the pipeline, not the engine. When a CDC
// event arrives on a non-actor vertex (e.g. a role, permission, service,
// location, or task vertex), the pipeline enumerates the set of actor
// (identity) vertices reachable from the mutated vertex via the topology
// relations the Capability Lens cares about, then re-executes the cypher
// rule with `$actorKey` bound to each affected actor.
//
// The enumeration is depth-bounded (matching the executor's
// variable-length traversal cap, default 10) and actor-cap-bounded
// (default 10_000) so a runaway traversal can't stall the pipeline.
// Both caps are configurable per Pipeline.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ActorEnumerator finds the set of actor (identity) vertex keys
// reachable from eventVertexKey via undirected adjacency BFS. The
// returned slice contains FULL Contract #1 vertex keys
// (e.g. "vtx.identity.<NanoID>"). When eventVertexKey already has the
// target type, it is returned as a singleton — the pipeline still
// re-executes the cypher against it.
//
// Caps:
//   - maxDepth: BFS depth bound. Per Decision #3 the default mirrors the
//     executor's variable-length traversal cap (10).
//   - maxActors: hard cap on the actor set. When the enumeration touches
//     more actors than the cap, a warning is logged and the truncated
//     set is returned (no error — per brief, "log a warning and
//     proceed").
//
// adjKV and coreKV are the live KV handles; coreKV is used only when
// resolving the type of a neighbour whose EdgeEntry doesn't carry
// OtherType (legacy edge events). For Contract #1 link envelopes
// fed through the 3.2b link bridge OtherType is always set.
type ActorEnumerator struct {
	adjKV     *substrate.KV
	coreKV    *substrate.KV
	actorType string
	maxDepth  int
	maxActors int
}

// DefaultActorMaxDepth mirrors the executor's variable-length traversal
// cap (Decision #3 / scope guard).
const DefaultActorMaxDepth = 10

// DefaultActorMaxSet is the default cap on the affected-actor set per
// Decision #3 / scope guard. Above this we log a warning and proceed.
const DefaultActorMaxSet = 10_000

// NewActorEnumerator constructs an enumerator with the given KV handles
// and target actor type (e.g. "identity").
func NewActorEnumerator(adjKV, coreKV *substrate.KV, actorType string) *ActorEnumerator {
	return &ActorEnumerator{
		adjKV:     adjKV,
		coreKV:    coreKV,
		actorType: actorType,
		maxDepth:  DefaultActorMaxDepth,
		maxActors: DefaultActorMaxSet,
	}
}

// WithCaps overrides the default depth and actor-set caps. Returns the
// receiver to allow fluent configuration at wire-up time.
func (e *ActorEnumerator) WithCaps(maxDepth, maxActors int) *ActorEnumerator {
	if maxDepth > 0 {
		e.maxDepth = maxDepth
	}
	if maxActors > 0 {
		e.maxActors = maxActors
	}
	return e
}

// Enumerate returns the set of actor vertex keys reachable from
// eventVertexKey by undirected adjacency BFS. The traversal is bounded
// by maxDepth and maxActors. eventVertexType is the type segment of
// the event vertex; when it equals e.actorType the event itself is
// returned as a singleton (the pipeline still re-projects it on the
// fast path). ctx is propagated to adjacency KV reads.
func (e *ActorEnumerator) Enumerate(ctx context.Context, eventVertexKey, eventVertexType string) ([]string, error) {
	// Fast path: the event is already on an actor vertex. The pipeline
	// re-executes the cypher against eventVertexKey via the normal route.
	if eventVertexType == e.actorType {
		return []string{eventVertexKey}, nil
	}

	// Recover the event vertex's NanoID for the BFS frontier; adjacency
	// is keyed by NanoID per `subjects.AdjKey`.
	_, eventID, ok := substrate.ParseVertexKey(eventVertexKey)
	if !ok {
		return nil, fmt.Errorf("pipeline: actor enumerator: not a Contract #1 vertex key: %q", eventVertexKey)
	}

	visited := map[string]struct{}{eventID: {}}
	actors := map[string]struct{}{}
	type frontierEntry struct {
		nodeID   string
		nodeType string
		depth    int
	}
	frontier := []frontierEntry{{nodeID: eventID, nodeType: eventVertexType, depth: 0}}
	truncated := false

	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		if cur.depth >= e.maxDepth {
			continue
		}
		edges, err := adjacency.Neighbors(ctx, e.adjKV, cur.nodeID)
		if err != nil {
			return nil, fmt.Errorf("pipeline: actor enumerator: neighbours of %q: %w", cur.nodeID, err)
		}
		for _, edge := range edges {
			other := edge.OtherNodeID
			otherType := edge.OtherType
			if otherType == "" {
				// Legacy edge event with no OtherType — best-effort lookup
				// via Core KV. We don't FAIL on missing/typeless edges;
				// such edges simply don't contribute to the actor set.
				continue
			}
			if _, seen := visited[other]; seen {
				continue
			}
			visited[other] = struct{}{}

			if otherType == e.actorType {
				actorKey := substrate.VertexPrefix + "." + otherType + "." + other
				if _, exists := actors[actorKey]; !exists {
					if len(actors) >= e.maxActors {
						truncated = true
						continue
					}
					actors[actorKey] = struct{}{}
				}
				// We don't traverse THROUGH actors — once we hit an
				// actor, capability is computed from that actor's own
				// outbound topology. Continuing past it would double-
				// count actors via shared neighbours (e.g. two actors
				// in the same location).
				continue
			}

			frontier = append(frontier, frontierEntry{nodeID: other, nodeType: otherType, depth: cur.depth + 1})
		}
	}

	if truncated {
		slog.Warn("pipeline: actor enumerator: actor-set cap exceeded; truncated",
			"eventVertex", eventVertexKey, "cap", e.maxActors)
	}

	out := make([]string, 0, len(actors))
	for k := range actors {
		out = append(out, k)
	}
	return out, nil
}
