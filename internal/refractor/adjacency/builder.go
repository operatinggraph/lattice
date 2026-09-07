package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

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
	// Bounded at MaxRemovalFloors per node, lowest sequences evicted first.
	Removals map[string]uint64 `json:"removals,omitempty"`
}

// MaxRemovalFloors bounds Removals per node.
//
// The cap is a DOCUMENT-SIZE requirement and nothing else. A node's whole edge
// list lives in one KV document; unbounded auxiliary state in it is how this
// index fails, which is what the overflow latch exists for, and the floor map
// has to be bounded for exactly that reason. No claim is made that an evicted
// floor is one whose racing event can no longer arrive: nothing here bounds
// that. A durable consumer sets no MaxDeliver (substrate.DurableConsumerConfig
// applies one only when it is > 0, so JetStream's default is unlimited), and a
// lens can sit at InitialPause: PauseInfra until an operator resumes it, so a
// message may be held or redelivered arbitrarily late.
//
// What an eviction costs is therefore stated positively: that one edge reverts
// to the behaviour this package had before the guard existed — a stale create
// can resurrect it. The guard is never worse than that baseline anywhere, only
// incomplete past the cap.
//
// 1024, not a smaller number, because the eviction key is a COUNT of removals
// on one node while the traffic that produces them is bursty: a single atomic
// batch carries up to 998 business mutations (atomic-batch-size-ceiling-design),
// so any cap at or below that lets ONE offboarding operation shed every floor
// recorded before it. 1024 clears that ceiling, and its cost is bounded: at the
// ~90 B a link-key floor marshals to, a full map is ≈ 92 KB against the 800 KiB
// overflowBytes budget — visible to the latch (which measures the marshalled
// document) and far from dominating it.
//
// It must stay >= 1: at 0 the eviction loop would drop the floor removeEdge
// just recorded, silently disabling the stale-create half of the guard.
//
// Exported because it is a property of the stored document, not a private
// tuning knob: a reader of an AdjValue can tell a complete floor set from a
// truncated one only against this number.
const MaxRemovalFloors = 1024

// declinedWrites counts the edge writes the ordering floor has refused since
// this process started.
//
// It exists because the refusal is otherwise invisible: upsertEdge declines by
// writing nothing and returning nil, which is byte-identical to a healthy
// no-op, so an operator asking "is anything being reordered under me" would
// have no way to tell a quiet stack from one where a lagging writer's every
// event is being dropped. The precedent this guard is modelled on — the grant
// writer's monotonic watermark — reports its declines to the caller
// (DeclinedByWatermark); this path has no caller-visible verdict to carry one,
// so the count is where that signal lives.
//
// It is a counter and not a health field on purpose: a rising count is normal
// on a lagging stack and means "the guard is doing its job", not "the component
// is unhealthy".
var declinedWrites atomic.Uint64

// DeclinedWrites reports how many edge writes this process's ordering floor has
// refused. Monotone for the life of the process; a delta over an interval is
// the quantity worth reading.
func DeclinedWrites() uint64 { return declinedWrites.Load() }

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
	// derived from. What makes it usable as an ordering key is the total order
	// of that ONE stream: every writer of this index consumes it, so any two
	// events carrying a sequence are comparable whichever writer produced them.
	//
	// On the link arm the number is additionally the KV revision of the link
	// key itself, because the event's EdgeID IS the key the message arrived on.
	// That is a stronger property than comparability and the consumer is
	// responsible for preserving it: a floor for edge L must only ever be set
	// from a message on subject L, or an unrelated key's position could pin a
	// floor L's own future revisions can never reach. The legacy event path
	// takes its EdgeID from the message BODY, so it stamps a sequence only when
	// that EdgeID is the message's own key.
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
// arrived on, and it is stamped on BOTH directional events so that the two
// endpoints arbitrate the same link against the same NUMBER. That is all the
// stamp buys, and the distinction matters: the same number does not guarantee
// the same VERDICT, because each endpoint measures it against the floors ITS
// OWN node document holds, and a busy node can have evicted a floor a quiet one
// still keeps (see MaxRemovalFloors). The asymmetry that leaves — an edge
// present at one end and absent at the other — is a stated residual of this
// index, not something the stamp closes.
//
// A caller with no sequence passes 0. That is not "the floor every event
// clears": an event carrying 0 applies only where the floor is still 0, and is
// declined against any real floor (see upsertEdge).
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
// DECLINED, not failed: nothing at all is written — not even a rewrite of the
// identical document, which would churn the node's KV revision on every stale
// redelivery and manufacture CAS conflicts for the other two writers — and the
// caller acks, because a redelivery would present the same stale event again.
// A decline is counted (DeclinedWrites) and logged at Debug rather than passed
// back, because on a lagging stack it is the expected outcome, not a fault.
//
// The comparison is >=, and the direction is load-bearing — but its guarantee
// is narrower than "nothing changes for an unsequenced caller". What holds
// exactly: an event carrying no sequence meets a floor of 0 and applies, so an
// edge NO sequenced writer has touched indexes as it always did, and so does
// every document written before entries carried a sequence. What does NOT hold:
// once any writer of an edge is sequenced, an unsequenced writer of that same
// edge is declined, because 0 is below that edge's floor. Mixed writers of one
// edge therefore lose the unsequenced one — an acceptable outcome only because
// every production writer of a link is sequenced, and the alternative (>) would
// make every unsequenced path stop applying everywhere and shrink the index
// silently. Equal sequences are one message redelivered, and re-applying it is
// idempotent.
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
		if floor := edgeFloor(current, edge.EdgeID); edge.Seq < floor {
			declinedWrites.Add(1)
			slog.Debug("adjacency: declined an edge write staler than the floor the index already holds",
				"nodeId", nodeID, "edgeId", edge.EdgeID, "evtSeq", edge.Seq, "floor", floor,
				"remove", remove)
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
// Under this code the two can never both be present for one EdgeID: an applied
// upsert deletes the floor and an applied removal deletes the entry, so exactly
// one of them (or neither) answers. The maximum is therefore defensive rather
// than a rule with a reachable case behind it — it is written as a maximum so
// that a document holding both, however it came to, resolves to the more recent
// of the two rather than to whichever branch happened to be checked first. No
// binary produces such a document today: EdgeEntry.Seq and AdjValue.Removals
// are one and the same addition, so a binary without the floors has no entry
// sequences either and every floor it can present reads as 0.
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
//
// It returns an AdjValue but is NOT a pure function of one: it overwrites doc's
// edge slice in place and deletes from the map doc names, so every alias of
// either sees the change. That is safe only because the sole caller passes a
// document it has just decoded and abandons after the call, re-reading a fresh
// one on each CAS pass. It is worth stating because a decoded document's edge
// slice does escape elsewhere in this package — prefetch hands st.doc.Edges
// straight to the executor's memo maps — so a future caller that shared one
// with a reader would corrupt it silently.
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
//
// Like upsertEntry it mutates in place behind its return value — it compacts
// doc's edge slice through doc.Edges[:0] and writes into the map doc names — so
// the same caller discipline applies: pass a freshly decoded document, and do
// not share one with a reader.
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

// evictStalestFloors trims removals to MaxRemovalFloors by dropping the lowest
// sequences — the floors whose edges have been gone longest, which is the least
// arbitrary order available and not a claim that their racing events have
// expired (see MaxRemovalFloors). Ties break on the EdgeID so the choice is
// deterministic: two writers reaching the cap on the same document must shed
// the same floors, or the surviving set would depend on map iteration order.
//
// The victim is tracked with an explicit `found` flag rather than by treating a
// zero-valued id as "nothing chosen yet". An EdgeID is an arbitrary string and
// the empty one is a legal map key, so a sentinel of "" would both make the
// first candidate's selection depend on iteration order and leave a floor
// stored under "" permanently unevictable.
func evictStalestFloors(removals map[string]uint64) {
	for len(removals) > MaxRemovalFloors {
		var stalestID string
		var stalestSeq uint64
		found := false
		for id, seq := range removals {
			if !found || seq < stalestSeq || (seq == stalestSeq && id < stalestID) {
				stalestID, stalestSeq, found = id, seq, true
			}
		}
		delete(removals, stalestID)
	}
}
