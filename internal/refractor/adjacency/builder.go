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

	// Seq is the Core KV backing-stream sequence of the event that wrote this
	// entry, and therefore the ordering floor the entry defends: an event
	// carrying a lower sequence is a staler view of the same edge and is
	// refused (see upsertEdge). Zero for an entry written by a path that
	// carries no sequence, which is a floor no event can fall below — so an
	// unsequenced entry behaves exactly like an unguarded one.
	Seq uint64 `json:"seq,omitempty"`
}

// AdjValue is the JSON structure stored at key adj.<nodeId> in the Adjacency KV.
type AdjValue struct {
	Edges []EdgeEntry `json:"edges"`

	// Removals is the ordering floor of the edges this node does NOT hold:
	// EdgeID → the sequence of the removal that dropped it. A stored entry
	// carries its own floor in EdgeEntry.Seq; an absent one has nowhere to
	// carry it, and without this map a removal that already landed could not
	// refuse the stale create that follows it — the revoked edge would come
	// back. It is deliberately NOT a tombstone entry inside Edges: every reader
	// of Edges (Neighbors, NeighborsScoped, PrefetchNodes), the overflow degree
	// accounting and edgesWithoutLinkKeys all treat an entry as a live edge,
	// and none of them has to learn otherwise for the floor to work.
	//
	// Bounded at maxRemovalFloors per node, lowest sequences evicted first.
	Removals map[string]uint64 `json:"removals,omitempty"`
}

// maxRemovalFloors bounds Removals per node. A node's whole edge list lives in
// one KV document, so unbounded auxiliary state is how this index fails (that
// is what the overflow latch exists for); the floor map has to be bounded for
// the same reason, and it is bounded by evicting the LOWEST sequences first. A
// floor's only job is to refuse an event staler than itself, and the stalest
// floors are the ones whose racing events can no longer be in flight —
// redelivery is bounded by AckWait × MaxDeliver, and a lens pipeline's lag is
// bounded by its own consumer. Evicting one re-opens the stale-create window
// for that single edge, and only after 128 further removals on the same node.
const maxRemovalFloors = 128

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

	// Seq is the Core KV backing-stream sequence of the message this event was
	// derived from — for a NATS KV bucket the backing stream IS the bucket, so
	// the number is also the KV revision of the link key, per-key monotone by
	// construction and comparable across every writer of this index, because
	// all three consume the one Core KV stream.
	//
	// `json:"-"` is a correctness tag, not a cosmetic one. The legacy event
	// path unmarshals a CoreKVEvent straight out of a Core KV message BODY; a
	// wire-visible field would let that body name its own ordering floor and
	// so promote itself over the events it must lose to. The sequence is
	// transport-derived and stamped by the consumer after the unmarshal, which
	// makes body-chosen ordering unrepresentable.
	Seq uint64 `json:"-"`
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
//
// seq is the Core KV backing-stream sequence of the message the link envelope
// arrived on, and it is stamped on BOTH directional events: the two endpoints
// index the same link and must arbitrate it against the same number, or one
// arm of an edge could keep a version the other has already refused. Callers
// with no sequence to offer pass 0, which is the floor every event clears.
func EventsForLink(key, srcType, srcID, linkName, dstType, dstID string, isDeleted bool, seq uint64) []CoreKVEvent {
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
			Seq:         seq,
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
			Seq:         seq,
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
		Seq:         evt.Seq,
	}
	return upsertEdge(ctx, kv, evt.NodeID, edge, evt.IsDeleted)
}

// upsertEdge applies one edge event to a node's document under a per-edge
// ordering floor.
//
// The floor exists because a Contract #1 link key IS its own EdgeID: it is
// stable across a revoke → re-grant, so the identity that makes the index
// writable is exactly the identity that makes it reorderable. Three writers
// index the same link with no cross-consumer ordering guarantee (the dedicated
// bootstrapper, the actor-aware link fan-out's pre-apply, and plain-link
// reprojection), and a Nak'd message is redelivered behind the messages that
// were behind it. Both stale directions are reachable and they fail opposite
// ways: a stale removal deletes a live edge (under-grant), and a stale create
// resurrects a revoked one (over-grant).
//
// So an event applies only if its sequence is at least the floor its EdgeID
// already carries — the stored entry's own Seq, or, for an edge this node no
// longer holds, the sequence of the removal that dropped it. A refused event is
// DECLINED, not failed: nothing is written and the caller acks, because a
// redelivery would present the same stale event again.
//
// The comparison is >=, and the direction is load-bearing. An event with no
// sequence carries 0, meets a floor of 0, and applies — so every path that does
// not carry a sequence, and every document written before entries carried one,
// behaves exactly as an unguarded index would. With > instead, every
// unsequenced event would stop applying and the index would silently shrink.
// The guard therefore engages only between two events that both name a real
// position in the Core KV stream, which is the only population it can order.
// Equal sequences are one message redelivered, and applying it is idempotent.
//
// The floor is recomputed on every CAS pass, from the document that pass read:
// a concurrent writer's newer entry (or newer removal floor) is part of the
// state this event must clear, and re-reading it is what keeps two writers
// racing on one node from each deciding against a stale copy.
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
		if edge.Seq < edgeFloor(current, edge.EdgeID) {
			return nil
		}
		if remove {
			current = removeEdge(current, edge.EdgeID, edge.Seq)
		} else {
			current = upsertEntry(current, edge)
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
// returns its ~1 MiB to the bucket. The replacement document carries neither
// edges nor removal floors: a marked node's reads enumerate Core KV, which is
// authoritative and needs no ordering guard, so a floor kept past the latch
// would only be state nothing consults. But the attempt is made ONCE and never
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

// edgeFloor is the ordering floor an event must reach to touch edgeID on this
// node: the sequence of the entry the node holds for it, or of the removal that
// dropped it, whichever is higher.
//
// Both are consulted rather than one or the other because the two records
// answer different questions and only their maximum answers "how recent is what
// I already know about this edge". An entry and a floor coexist only
// transiently — an applied upsert clears the floor (see upsertEntry) — but a
// document written by a binary that does not keep floors can present an entry
// with no floor, and taking the maximum makes that case degrade to the entry's
// own sequence rather than to nothing.
func edgeFloor(doc AdjValue, edgeID string) uint64 {
	floor := doc.Removals[edgeID]
	for _, e := range doc.Edges {
		if e.EdgeID == edgeID {
			if e.Seq > floor {
				floor = e.Seq
			}
			break
		}
	}
	return floor
}

// upsertEntry adds edge to the list or replaces the existing entry with the
// same EdgeID, and drops any removal floor that EdgeID carried: the entry now
// holds the floor in its own Seq, and keeping both would have one edge defended
// by two numbers that can disagree.
func upsertEntry(doc AdjValue, edge EdgeEntry) AdjValue {
	delete(doc.Removals, edge.EdgeID)

	for i, e := range doc.Edges {
		if e.EdgeID == edge.EdgeID {
			doc.Edges[i] = edge
			return doc
		}
	}
	doc.Edges = append(doc.Edges, edge)
	return doc
}

// removeEdge drops the entry matching edgeID and records seq as that EdgeID's
// removal floor, so the create this removal supersedes cannot resurrect the
// edge if it arrives afterwards.
//
// A removal with no sequence (seq == 0) records nothing and stays a pure drop:
// an event that names no position in the stream makes no ordering claim, and a
// floor of 0 is indistinguishable from no floor at all. That confines every
// byte of floor state to the sequenced paths.
func removeEdge(doc AdjValue, edgeID string, seq uint64) AdjValue {
	out := doc.Edges[:0]
	for _, e := range doc.Edges {
		if e.EdgeID != edgeID {
			out = append(out, e)
		}
	}
	doc.Edges = out

	if seq == 0 {
		return doc
	}
	if doc.Removals == nil {
		doc.Removals = make(map[string]uint64, 1)
	}
	doc.Removals[edgeID] = seq
	evictStalestFloors(doc.Removals)
	return doc
}

// evictStalestFloors trims removals to maxRemovalFloors by dropping the lowest
// sequences, which are the floors whose racing events can no longer be in
// flight. Ties break on the EdgeID so the eviction is deterministic — two
// writers reaching the cap on the same document must shed the same floors, or
// the surviving set would depend on map iteration order.
func evictStalestFloors(removals map[string]uint64) {
	for len(removals) > maxRemovalFloors {
		stalestID, stalestSeq := "", uint64(0)
		for id, seq := range removals {
			if stalestID == "" || seq < stalestSeq || (seq == stalestSeq && id < stalestID) {
				stalestID, stalestSeq = id, seq
			}
		}
		delete(removals, stalestID)
	}
}
