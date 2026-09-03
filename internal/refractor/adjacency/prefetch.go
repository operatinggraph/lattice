package adjacency

import (
	"context"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// prefetchSubjectCap is the substrate multi-get's atomic fast-path cap
// (substrate.KVGetMulti): at or under it the whole response is computed under
// the stream's read lock in one round trip, and past it the primitive falls
// back to draining a consumer.
const prefetchSubjectCap = 1024

// prefetchNodesPerRequest is how many nodes one batched request covers.
//
// Each node contributes TWO subjects — its document and its overflow mark — and
// the pair must come from ONE instant, for exactly the reason readNodeState
// batches them: a reader that saw the document the latch has just emptied
// WITHOUT the mark that explains it would present an empty edge list as the
// node's authoritative edge set. Halving the subject cap is what keeps both of
// a node's keys inside a single request, and so inside a single instant.
// Different nodes need no such relationship: each one's state is an
// independent fact.
const prefetchNodesPerRequest = prefetchSubjectCap / 2

// prefetchNodeFloor is the request size below which a failure is the caller's
// error rather than another split. The response byte ceiling no subject count
// can predict (substrate.ChunkedMultiGet) is what the splitting exists for, and
// eight nodes of documents each near the overflow threshold is already well
// under it — past this point a failure is about something other than size.
const prefetchNodeFloor = 8

// Prefetched is one node's batched adjacency answer — what the whole-node read
// of that node would have returned, or the fact that the node is
// overflow-marked and cannot be answered this way.
type Prefetched struct {
	// Edges is the node's adjacency document's edge list, exactly as
	// Neighbors and a whole NeighborsScoped return it: an empty slice when the
	// node has no document at all. It is empty and meaningless when Marked.
	//
	// The answer does not depend on any relation scope. An unmarked node's
	// read is its whole document however many relations the caller follows
	// (NeighborsScoped ignores its rels on that arm and leaves the filtering to
	// the caller), so one staged entry serves a typed hop and an untyped one
	// alike.
	Edges []EdgeEntry
	// Fingerprint is the document's KV revision, 0 when the node has no
	// document — the same value and the same meaning a whole read reports, and
	// so directly comparable with it.
	Fingerprint uint64
	// Marked reports the node's overflow latch. A marked node carries NO edges
	// here: its edges live in Core KV's link keyspace, which this batch never
	// touches, so the caller must read such a node through NeighborsScoped /
	// Neighbors as it always has.
	Marked bool
}

// PrefetchNodes reads the adjacency state of many nodes in chunked requests —
// the batched form of readNodeState, for a caller about to walk from a whole
// set of already-known nodes and otherwise facing one round trip per node. It
// returns the answers by node id together with how many REQUESTS it issued,
// which is the quantity that says a batch is a batch.
//
// The requests drop the point-in-time guarantee across nodes
// (GetMultiNoSnapshot). Each node's state is an independent fact, and the
// caller that batches this way re-validates its work by comparing a read-surface
// footprint afterwards rather than by trusting one instant — the same argument
// the overflow-marked hub read makes for the same primitive. What must be
// atomic, and is, is each node's own document/mark PAIR: the NODE is the unit
// this splits by, so no split can tear one.
//
// This function reports NOTHING to a context read observer. The read it serves
// is not taken until the caller USES the staged answer, and a batch necessarily
// covers nodes a walk may never reach — so the caller owes ObserveWholeRead at
// the point it consumes an entry, which is where the per-node read would have
// observed. That keeps the observation stream identical either way.
//
// Two node classes are deliberately ABSENT from the answer, and a caller must
// treat an absent node as un-prefetched and read it per-node:
//
//   - an id that cannot form a NATS subject token, which would panic the key
//     builders — batching must not turn a walk that would never have reached
//     such a node into a failure;
//   - a node whose document does not decode. The per-node read is left to hit
//     it and fail exactly where it failed before any batching, so a corrupt
//     document cannot fail an evaluation that never needed that node.
func PrefetchNodes(ctx context.Context, kv *substrate.KV, nodeIDs []string) (map[string]Prefetched, int, error) {
	out := make(map[string]Prefetched, len(nodeIDs))
	if kv == nil || len(nodeIDs) == 0 {
		return out, 0, nil
	}

	want := make([]string, 0, len(nodeIDs))
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		if !subjects.ValidToken(id) {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		want = append(want, id)
	}

	requests := 0
	read := func(ctx context.Context, ids []string) (map[string]*substrate.KVEntry, error) {
		keys := make([]string, 0, 2*len(ids))
		for _, id := range ids {
			keys = append(keys, subjects.AdjKey(id), subjects.AdjMarkKey(id))
		}
		requests++
		return kv.GetMultiNoSnapshot(ctx, keys)
	}
	visit := func(ids []string, entries map[string]*substrate.KVEntry) error {
		for _, id := range ids {
			_, marked := entries[subjects.AdjMarkKey(id)]
			st, err := decodeNodeState(id, entries[subjects.AdjKey(id)], marked)
			if err != nil {
				slog.Warn("adjacency: prefetched document did not decode; leaving the node to its own read",
					"nodeId", id, "error", err)
				continue
			}
			if st.marked {
				out[id] = Prefetched{Marked: true}
				continue
			}
			if !st.docFound {
				out[id] = Prefetched{Edges: []EdgeEntry{}}
				continue
			}
			out[id] = Prefetched{Edges: st.doc.Edges, Fingerprint: st.docRev}
		}
		return nil
	}

	if err := substrate.ChunkedMultiGet(
		ctx, want, prefetchNodesPerRequest, prefetchNodeFloor, read, visit,
	); err != nil {
		return nil, requests, err
	}
	return out, requests, nil
}

// ObserveWholeRead reports one whole-node read to ctx's read observer — the
// report a caller serving a node from PrefetchNodes owes at the moment it
// consumes the staged answer, in place of the one Neighbors / NeighborsScoped
// would have made. rels is the relation scope the caller asked at, nil for a
// whole-node read.
//
// A staged answer is only ever an unmarked node's whole document, so the
// observation it stands in for is always Marked=false, Whole=true.
func ObserveWholeRead(ctx context.Context, nodeID string, rels map[string]struct{}) {
	observeRead(ctx, nodeID, rels, false, true)
}
