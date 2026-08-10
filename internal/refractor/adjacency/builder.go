package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// EdgeEntry is one graph edge stored in the adjacency list for a node.
//
// OtherType is the Contract #1 vertex-type segment of the OTHER endpoint, and
// every producer in the tree sets it from the parsed link key. It lets an
// executor reconstruct the OTHER endpoint's full vertex key
// (vtx.<OtherType>.<OtherNodeID>) for a Core KV point read without scanning the
// bucket — and that reconstruction is the only way such a neighbour reaches a
// LABELED pattern node, since a pattern label is the Contract #1 key type and a
// bare NodeID parses as no type at all. An entry without it falls back to a
// NodeID-only lookup that can satisfy an unlabeled pattern only.
type EdgeEntry struct {
	CoreKvKey   string `json:"coreKvKey"`
	EdgeID      string `json:"edgeId"`
	Name        string `json:"name"`
	Direction   string `json:"direction"`
	OtherNodeID string `json:"otherNodeId"`
	OtherType   string `json:"otherType,omitempty"`
}

// AdjValue is the JSON structure stored at key adj.<nodeId> in the Adjacency KV.
type AdjValue struct {
	Edges []EdgeEntry `json:"edges"`
}

// DirectionMatches compares an EdgeEntry.Direction string ("outbound" /
// "inbound", the vocabulary this package's Build/Neighbors persist) against
// want, in the engine-neutral "out"/"in"/"both" vocabulary
// full.Direction.String() produces (ruleengine stays engine-neutral and must
// not import the full engine's Direction type — see
// ruleengine.EdgeSelector's doc comment). "out" wants outbound; "in" wants
// inbound; "both" wants either; any other value matches nothing (fail-closed
// on an unrecognised want, mirroring the executor's own former
// directionMatches switch default).
func DirectionMatches(edgeDir, want string) bool {
	switch want {
	case "out":
		return edgeDir == "outbound"
	case "in":
		return edgeDir == "inbound"
	case "both":
		return true
	}
	return false
}

// CoreKVEvent is the parsed payload of an incoming Core KV edge event.
//
// OtherType mirrors EdgeEntry.OtherType — see that comment. The
// adjacency builder propagates this field through to the persisted
// EdgeEntry verbatim.
type CoreKVEvent struct {
	CoreKvKey   string `json:"coreKvKey"`
	EdgeID      string `json:"edgeId"`
	Name        string `json:"name"`
	Direction   string `json:"direction"`
	NodeID      string `json:"nodeId"`      // the node to index under (determines the adj key)
	OtherNodeID string `json:"otherNodeId"` // the other endpoint (bare NodeID)
	OtherType   string `json:"otherType,omitempty"`
	IsDeleted   bool   `json:"isDeleted"`
}

// EventsForLink builds the two directional CoreKVEvents that indexing one
// Contract #1 link envelope produces: one outbound from the source endpoint,
// one inbound to the destination endpoint. key is the link's own KV key,
// which is globally unique per Contract #1 and therefore doubles as EdgeID
// for both directions — the same key, the same edge, seen from each end.
//
// A self-link (srcType/srcID equal to dstType/dstID) yields two events on the
// SAME node, and what the two paths then store diverges — knowingly:
//
//   - The document path collapses them to one entry. Both events carry the
//     same NodeID and the same EdgeID, and upsertEntry matches on EdgeID
//     alone, so the second event overwrites the first: an unmarked node stores
//     a single entry, direction "inbound", and its outbound view of its own
//     self-link is not reachable through the index.
//   - The Core KV fallback (neighborsFromCoreKV) emits both, because it
//     derives direction from the link key's endpoints rather than from an
//     entry list keyed by EdgeID.
//
// So latching a node CHANGES its self-link results, from one entry to two.
// That direction is the intended one — the fallback's answer is the correct
// one, and a consumer filtering on Direction (pipeline.ActorEnumerator's
// hierarchy hop, for instance) sees an outbound self-edge only once the node
// is marked. Collapsing the fallback to match the document would be
// propagating the defect, not preserving compatibility.
//
// This is the one constructor every Core KV link consumer in the tree
// (bootstrap, the link fan-out's pre-apply, and plain-link reprojection)
// builds its pair of events from — they see the same link key parsed the
// same way and must agree on what indexing it means.
func EventsForLink(key, srcType, srcID, linkName, dstType, dstID string, isDeleted bool) []CoreKVEvent {
	return []CoreKVEvent{
		{
			CoreKvKey:   key,
			EdgeID:      key,
			Name:        linkName,
			Direction:   "outbound",
			NodeID:      srcID,
			OtherNodeID: dstID,
			OtherType:   dstType,
			IsDeleted:   isDeleted,
		},
		{
			CoreKvKey:   key,
			EdgeID:      key,
			Name:        linkName,
			Direction:   "inbound",
			NodeID:      dstID,
			OtherNodeID: srcID,
			OtherType:   srcType,
			IsDeleted:   isDeleted,
		},
	}
}

// Build processes a CoreKVEvent and updates adj.<NodeID> in kv using CAS-with-retry.
// ctx is propagated to all KV calls so the caller can cancel during shutdown.
//
// A node whose edge list has overflowed (see overflow.go) has no document to
// maintain: Build latches the node the first time it crosses a threshold and
// is a no-op for every event on it afterwards, in both directions — an added
// edge is not absorbed and a retracted one is not removed, because the
// authoritative record of both is Core KV, which Neighbors reads directly for
// such a node.
func Build(ctx context.Context, kv *substrate.KV, evt CoreKVEvent) error {
	edge := EdgeEntry{
		CoreKvKey:   evt.CoreKvKey,
		EdgeID:      evt.EdgeID,
		Name:        evt.Name,
		Direction:   evt.Direction,
		OtherNodeID: evt.OtherNodeID,
		OtherType:   evt.OtherType,
	}
	return upsertEdge(ctx, kv, evt.NodeID, edge, evt.IsDeleted)
}

func upsertEdge(ctx context.Context, kv *substrate.KV, nodeID string, edge EdgeEntry, remove bool) error {
	key := subjects.AdjKey(nodeID)

	for {
		st, err := readNodeState(ctx, kv, nodeID)
		if err != nil {
			return err
		}
		if st.marked {
			return nil
		}

		current := st.doc
		if remove {
			current.Edges = removeEdge(current.Edges, edge.EdgeID)
		} else {
			current.Edges = upsertEntry(current.Edges, edge)
		}

		data, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("adjacency: marshal %s: %w", key, err)
		}

		if int64(len(current.Edges)) > overflowDegree.Load() || int64(len(data)) > overflowBytes.Load() {
			if unreconstructable := edgesWithoutLinkKeys(current.Edges); unreconstructable > 0 {
				// Latching hands this node's reads to an enumeration of Core
				// KV's LINK keyspace, which can only reconstruct edges that
				// have a link key. The legacy event path indexes edges from
				// any Core KV message carrying a nodeId, and those have none —
				// latching would delete them from every future read, with no
				// mark ever lifted to bring them back. Keeping the document is
				// the lesser harm: it eventually jams, loudly and visibly,
				// instead of silently shrinking the graph.
				//
				// The warning repeats for every event past the threshold on
				// such a node, and that repetition is the point: this is a
				// configuration the fallback cannot serve, and it wants an
				// operator, not a single line lost in a boot log.
				slog.Warn("adjacency: node is past the overflow threshold but cannot be latched — it holds edges with no Contract #1 link key, which the Core KV fallback cannot reconstruct",
					"nodeId", nodeID, "degree", len(current.Edges), "bytes", len(data),
					"edgesWithoutLinkKeys", unreconstructable)
			} else {
				return latch(ctx, kv, nodeID, len(current.Edges), len(data))
			}
		}

		if !st.docFound {
			_, err = kv.Create(ctx, key, data)
			if err == nil {
				return nil
			}
			if errors.Is(err, substrate.ErrRevisionConflict) {
				continue
			}
			return fmt.Errorf("adjacency: create %s: %w", key, err)
		}

		_, err = kv.Update(ctx, key, data, st.docRev)
		if err == nil {
			return nil
		}
		if errors.Is(err, substrate.ErrRevisionConflict) {
			continue
		}
		return fmt.Errorf("adjacency: update %s: %w", key, err)
	}
}

// latch marks nodeID as overflowed and gives up on its document.
//
// The mark is created, not put: three writers index one node's edges
// concurrently (the bootstrap consumer, the lens fan-out's link pre-apply, and
// plain-link reprojection), so all three can reach this point on the same
// event burst. Create-tolerating-conflict makes that idempotent — whichever
// writer arrives first defines the mark, and the others adopt it — where a
// create-or-fail would surface a spurious error the callers translate into a
// redelivery, and an unconditional put would churn the key on every writer.
//
// Emptying the document is best-effort and deliberately not part of the
// latch's success: the mark alone decides how the node is read, so a failure
// here costs no correctness. It is worth attempting because that body is
// exactly what a node too large to rewrite cannot shed, and replacing it
// returns its ~1 MiB to the bucket. But the attempt is made ONCE and never
// retried: no later Build re-enters this function (they all return at the
// mark), and nothing sweeps the bucket, so a Put that fails here leaves the
// oversize body parked until the bucket is next rebuilt. That is a space
// cost only. An older binary that later overwrites the emptied body changes
// nothing either, since no reader consults a marked node's document.
func latch(ctx context.Context, kv *substrate.KV, nodeID string, degree, size int) error {
	markKey := subjects.AdjMarkKey(nodeID)

	body, err := json.Marshal(AdjMark{Degree: degree, Bytes: size})
	if err != nil {
		return fmt.Errorf("adjacency: marshal %s: %w", markKey, err)
	}
	if _, err := kv.Create(ctx, markKey, body); err != nil && !errors.Is(err, substrate.ErrRevisionConflict) {
		return fmt.Errorf("adjacency: create %s: %w", markKey, err)
	}

	slog.Warn("adjacency: node exceeded the overflow threshold — its edge reads now enumerate Core KV",
		"nodeId", nodeID, "degree", degree, "bytes", size,
		"degreeThreshold", overflowDegree.Load(), "bytesThreshold", overflowBytes.Load())

	empty, err := json.Marshal(AdjValue{Edges: []EdgeEntry{}})
	if err != nil {
		return fmt.Errorf("adjacency: marshal %s: %w", subjects.AdjKey(nodeID), err)
	}
	if _, err := kv.Put(ctx, subjects.AdjKey(nodeID), empty); err != nil {
		slog.Warn("adjacency: could not empty the document of an overflow-marked node",
			"err", err, "nodeId", nodeID)
	}
	return nil
}

// edgesWithoutLinkKeys counts the entries whose CoreKvKey is not a Contract #1
// link key — the edges a Core KV link enumeration could never rebuild, and so
// the ones that would disappear if this node were latched.
func edgesWithoutLinkKeys(edges []EdgeEntry) int {
	n := 0
	for _, e := range edges {
		if substrate.ClassifyKey(e.CoreKvKey) != substrate.KindLink {
			n++
		}
	}
	return n
}

// upsertEntry adds edge to the list or replaces the existing entry with the same EdgeID.
func upsertEntry(edges []EdgeEntry, edge EdgeEntry) []EdgeEntry {
	for i, e := range edges {
		if e.EdgeID == edge.EdgeID {
			edges[i] = edge
			return edges
		}
	}
	return append(edges, edge)
}

// removeEdge returns a slice with the entry matching edgeID removed.
func removeEdge(edges []EdgeEntry, edgeID string) []EdgeEntry {
	out := edges[:0]
	for _, e := range edges {
		if e.EdgeID != edgeID {
			out = append(out, e)
		}
	}
	return out
}
