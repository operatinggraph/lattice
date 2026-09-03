package adjacency

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// A node's whole edge list lives in one KV document (AdjKey), so a node with
// enough edges eventually produces a document NATS refuses to accept: the
// write fails permanently, the failing message is redelivered forever, and
// every read of that node returns the last document that did fit — a frozen,
// silently wrong edge set. The overflow latch is the structural answer. Once a
// node's edge list crosses either threshold below, Build writes a mark
// (subjects.AdjMarkKey) and stops maintaining that node's document; Neighbors
// sees the mark and serves the node's edges by enumerating Core KV's link
// keyspace, which is the authoritative record the document was only ever a
// cache of. Marked reads are slower and complete; unmarked reads are untouched.
//
// BOTH thresholds latch, because neither bounds the other: entries are
// variable-length (a link key, a relation name, and two type segments), so a
// degree well under the count threshold can still marshal past the byte
// threshold, and a document of tiny entries can pass the count threshold long
// before it is anywhere near the size that jams.
const (
	// defaultOverflowDegree is the edge count past which a node latches.
	// 3,072 entries at the observed ~268 B/entry is ≈ 823 KB — comfortably
	// clear of the ~1 MiB payload ceiling that jams the write, and far above
	// any node whose edges a projection realistically walks.
	defaultOverflowDegree = 3072

	// defaultOverflowBytes is the marshaled-document size past which a node
	// latches, whatever its degree.
	defaultOverflowBytes = 800 << 10
)

// The active thresholds. Production always runs the constants above; a test
// that must cross the latch without seeding thousands of edges lowers them
// through SetOverflowThresholds.
//
// They are atomics because Build reads them on every event from every one of
// its concurrent writers while a test writes them, and a plain global would
// make that an ordinary data race — one a -race build reports and, absent the
// race detector, one the compiler is free to make surprising. The load costs
// nothing beside the KV round trip it sits in front of.
var (
	overflowDegree atomic.Int64
	overflowBytes  atomic.Int64
)

func init() {
	overflowDegree.Store(defaultOverflowDegree)
	overflowBytes.Store(defaultOverflowBytes)
}

// SetOverflowThresholds lowers the overflow-latch thresholds and returns a
// function restoring the production constants — the seam a test crosses the
// latch through, since seeding a real 3,072-edge node costs thousands of CAS
// round trips per case.
func SetOverflowThresholds(degree, maxBytes int) (restore func()) {
	prevDegree, prevBytes := overflowDegree.Load(), overflowBytes.Load()
	overflowDegree.Store(int64(degree))
	overflowBytes.Store(int64(maxBytes))
	return func() {
		overflowDegree.Store(prevDegree)
		overflowBytes.Store(prevBytes)
	}
}

// AdjMark is the JSON body stored at subjects.AdjMarkKey. Nothing reads its
// fields: the key's presence is the entire signal, and every consumer treats
// the body as opaque. It records the observation that tripped the latch purely
// so an operator finding the key can tell how the node got there.
type AdjMark struct {
	Degree int `json:"degree"`
	Bytes  int `json:"bytes"`
}

// The mark's lifetime, boundary by boundary. It is deliberately the ONLY
// record of a node's overflow state: nothing in this process caches it, so
// there is no second copy that can outlive the key and answer for it.
//
//   - Created: by latch, at the first Build that carries a node past either
//     threshold. Created tolerating an existing key, so the three concurrent
//     writers of one node's edges converge on one mark rather than one failing.
//   - Reset: never, by any code path. A node whose degree later shrinks keeps
//     its mark and keeps paying the fallback read; marks are rare, and
//     un-marking would reintroduce the jam on the next growth.
//   - Carried: across restarts and reconnects, because it is durable KV state
//     and nothing else. A Refractor that reconnects to a NATS it was
//     disconnected from re-reads the mark on the very next Build or Neighbors,
//     so it observes whatever the bucket now says — including that the mark is
//     gone.
//   - Ordered: every read of the mark is batched with the read of the document
//     it governs (readNodeState), so no caller can observe an emptied document
//     without the mark that explains it, in either direction.
//   - Replay: the Bootstrapper's rebuild re-crosses the same thresholds on the
//     same edges and re-latches deterministically. Where the mark survived, the
//     rebuild short-circuits on it instead.
//   - Tombstone: an edge retraction on a marked node writes nothing (there is
//     no document to maintain); the retraction reaches readers through the
//     fallback read, which sees the link leave Core KV.
//   - Bucket wipe: wiping or recreating the adjacency bucket drops the mark
//     along with the documents, and the next Build rebuilds the document from
//     zero and re-latches when it crosses the threshold again. This works
//     WITHOUT a restart, which matters because the Refractor is built to
//     survive a NATS outage and reconnect rather than exit: a process holding
//     its own memory of the mark would go on suppressing writes for a node the
//     bucket no longer knows about, and serve an empty edge set as
//     authoritative forever.
//   - Mixed binaries: a binary without the latch cannot see the mark key at
//     all, so it goes on rewriting the emptied document and failing on an
//     oversize one. It can never un-mark the node, because the mark is not
//     part of the document it writes — which is the whole reason the mark is
//     its own key rather than a field in the document.

// nodeState is one atomic observation of a node's adjacency storage: its
// document, and whether its overflow latch is set.
type nodeState struct {
	// doc is the decoded document; the zero value when docFound is false.
	doc AdjValue
	// docRev is the document's KV revision, 0 when no document exists —
	// the value Neighbors reports for an unmarked node.
	docRev uint64
	// docFound distinguishes "no document" from "a document holding no
	// edges": the two differ in the revision a footprint records.
	docFound bool
	// marked reports the presence of the node's overflow mark.
	marked bool
}

// readNodeState reads a node's document and its overflow mark in ONE batched
// KV request, and is the only way either path in this package learns whether a
// node is marked.
//
// The batching is a correctness requirement, not an optimization. Two
// sequential reads leave a window in which a node latches between them: the
// document read returns the body the latch has just emptied, the mark read
// still misses the mark, and the caller presents an empty edge list as this
// node's authoritative edge set — the silent-wrong answer the latch exists to
// prevent. KVGetMulti computes both keys under the stream's read lock, so the
// pair is a single point in time and the empty document is never observed
// without the mark that explains it.
func readNodeState(ctx context.Context, kv *substrate.KV, nodeID string) (nodeState, error) {
	docKey := subjects.AdjKey(nodeID)
	markKey := subjects.AdjMarkKey(nodeID)

	entries, err := kv.GetMulti(ctx, []string{docKey, markKey})
	if err != nil {
		return nodeState{}, fmt.Errorf("adjacency: get %s: %w", docKey, err)
	}
	_, marked := entries[markKey]
	return decodeNodeState(nodeID, entries[docKey], marked)
}

// decodeNodeState renders one node's document/mark pair into its state — the
// single decoder behind the per-node read and the batched one, so a node
// yields the same state, and the same error, however its keys were fetched.
//
// A nil docEntry is a node with no document at all, which is not the same as a
// document holding no edges: the two differ in the revision a footprint
// records, and docFound is what tells them apart.
func decodeNodeState(nodeID string, docEntry *substrate.KVEntry, markPresent bool) (nodeState, error) {
	st := nodeState{marked: markPresent}
	if docEntry == nil {
		return st, nil
	}
	st.docFound = true
	st.docRev = docEntry.Revision
	if err := json.Unmarshal(docEntry.Value, &st.doc); err != nil {
		return nodeState{}, fmt.Errorf("adjacency: unmarshal %s: %w", subjects.AdjKey(nodeID), err)
	}
	return st, nil
}
