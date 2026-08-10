package adjacency

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// The FNV-1a 64-bit parameters, used directly rather than through the hash
// interface where the fingerprint combines per-entry hashes by hand.
const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// Neighbors returns the edge list for nodeID, plus a fingerprint of the read
// it served — a caller building a per-evaluation footprint records the
// fingerprint and re-reads later to detect a mid-evaluation write to this
// node's edge list.
//
// The edges come from one of two places, and the fingerprint's meaning follows:
//
//   - Unmarked node (all but the rare hub): the node's adjacency document in
//     adjKV, with the document's KV revision as the fingerprint — 0 when the
//     document is absent. The edges and the revision are exactly the
//     document's own; the cost is one batched two-key request rather than one
//     point read, which measures at roughly the request floor either way (a
//     multi-key request p50 near 306 µs against a single Get's 153 µs — about
//     2×, and the same 306 µs two sequential Gets would cost while also being
//     non-atomic).
//   - Overflow-marked node: Core KV's link keyspace, enumerated directly, with
//     an order-independent hash over the matched links' (key, revision) pairs
//     as the fingerprint. The two fingerprint spaces are never meaningfully
//     confusable: a node that latches between a footprint's capture and its
//     validation yields a hash where a revision was recorded, which differs
//     with overwhelming probability (a 64-bit hash colliding with a small
//     document revision is not impossible, only vanishingly unlikely), and
//     reports drift — the conservative answer.
//
// coreKV may be nil only for a caller that never reaches a marked node (the
// read-free key-resolution executor). A marked node with no Core KV handle is
// an error, never a silently short edge list.
//
// Returns an empty (non-nil) slice if the node has no adjacency entry. ctx is
// propagated to the KV reads so the caller can cancel during shutdown.
func Neighbors(ctx context.Context, kv, coreKV *substrate.KV, nodeID string) ([]EdgeEntry, uint64, error) {
	st, err := readNodeState(ctx, kv, nodeID)
	if err != nil {
		return nil, 0, err
	}
	if st.marked {
		return neighborsFromCoreKV(ctx, coreKV, nodeID)
	}
	if !st.docFound {
		return []EdgeEntry{}, 0, nil
	}
	return st.doc.Edges, st.docRev, nil
}

// neighborsFromCoreKV builds a marked node's edge list from Core KV itself,
// the record the adjacency document is only ever a cache of.
//
// Both directions are read in ONE request, under two Contract #1 link-key
// filters: `lnk.*.<nodeID>.>` catches every link whose SOURCE is the node
// (segment 3), `lnk.*.*.*.*.<nodeID>` every link whose TARGET is (segment 6).
// Neither filter is a subject-subset of the other — the source form pins a
// literal where the target form wildcards, and its trailing `>` sits where the
// target form carries a literal — so they are a legal pair to hand to one
// request even though they intersect on self-links. The read is also
// commit-fresh, which the document is not: Core KV holds the link the moment
// the write commits, so a marked node never needs the pipelines' link
// pre-apply.
//
// The request drops the point-in-time guarantee (GetMultiNoSnapshot, not
// GetMulti). A node marked on the degree arm has more matched subjects than
// the batched fast path admits, so its read takes the consumer drain — and
// the stability-verified drain fails outright whenever any of those thousands
// of links moves between its two passes, which on a node busy enough to have
// overflowed is the ordinary case rather than the exception. Requiring that
// would reinstate, on the read side, the failure the latch exists to end.
// (A node marked on the BYTE arm, or one whose links were later retracted,
// can sit under the cap and take the atomic fast path after all; the weaker
// primitive is only weaker past the cap, so that case loses nothing.)
//
// It is also more atomicity than this read ever had: each edge is an
// independent fact, and an evaluation that depends on the set re-reads it and
// compares fingerprints (pipeline.footprintValid) rather than trusting one
// read's instant. Completeness is what matters here, and completeness is what
// the drain still guarantees — including retracting a link hard-deleted while
// the drain is in flight, so a revoked edge never reaches a walk.
//
// The result is sorted, so a marked node's edge order is stable across reads
// instead of following a map's iteration order.
func neighborsFromCoreKV(ctx context.Context, coreKV *substrate.KV, nodeID string) ([]EdgeEntry, uint64, error) {
	if coreKV == nil {
		return nil, 0, fmt.Errorf("adjacency: node %s is overflow-marked and needs a Core KV handle to read", nodeID)
	}

	outbound := substrate.LinkPrefix + ".*." + nodeID + ".>"
	inbound := substrate.LinkPrefix + ".*.*.*.*." + nodeID

	entries, err := coreKV.GetMultiNoSnapshot(ctx, []string{outbound, inbound})
	if err != nil {
		return nil, 0, fmt.Errorf("adjacency: enumerate links of %s: %w", nodeID, err)
	}

	// One unreadable key must not cost a hub its whole edge set: an aborted
	// read here is the frozen-wrong answer in another costume, and it would
	// repeat on EVERY read of that node, since the fallback re-parses every
	// body every time while the write path saw each body once. Both arms below
	// therefore skip and warn. The warnings are capped per read for the same
	// reason: a single bad key on a node of thousands would otherwise emit a
	// line per read, forever.
	skipped := newSkipLog(nodeID)

	edges := make([]EdgeEntry, 0, len(entries))
	for key, entry := range entries {
		srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(key)
		if !ok {
			// Reachable: NATS' `>` matches one token or many, so the outbound
			// filter admits any `lnk.<x>.<nodeID>.…` key of four segments or
			// more, not the six-segment shape alone.
			skipped.record("key is not a Contract #1 link key", key)
			continue
		}

		// A soft-tombstoned link is still a live KV entry (the primitive drops
		// only NATS-level delete markers), and it is how a retraction reaches
		// a marked node: the edge leaves this list while the key remains.
		var envelope struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if jsonErr := json.Unmarshal(entry.Value, &envelope); jsonErr != nil {
			skipped.record("envelope is not decodable JSON", key)
			continue
		}
		if envelope.IsDeleted {
			continue
		}

		// Which endpoint the node is decides the direction, from the same key
		// EventsForLink reads. A self-link is both endpoints and yields both
		// entries here — where the document path collapses them to one; see
		// EventsForLink's doc comment for why that divergence is the intended
		// direction.
		if srcID == nodeID {
			edges = append(edges, EdgeEntry{
				CoreKvKey:   key,
				EdgeID:      key,
				Name:        linkName,
				Direction:   "outbound",
				OtherNodeID: dstID,
				OtherType:   dstType,
			})
		}
		if dstID == nodeID {
			edges = append(edges, EdgeEntry{
				CoreKvKey:   key,
				EdgeID:      key,
				Name:        linkName,
				Direction:   "inbound",
				OtherNodeID: srcID,
				OtherType:   srcType,
			})
		}
	}
	skipped.flush()

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].EdgeID != edges[j].EdgeID {
			return edges[i].EdgeID < edges[j].EdgeID
		}
		return edges[i].Direction < edges[j].Direction
	})

	return edges, linkSetFingerprint(entries), nil
}

// maxSkipWarningsPerRead bounds how many individual unusable keys one
// fallback read names in the log. A node reaches the fallback only by holding
// thousands of links, and a key that cannot be read stays unreadable, so an
// uncapped line-per-key would reprint the same handful of names on every read
// of that node for as long as it lives. A few named examples locate the
// problem; the flushed total says how big it is.
const maxSkipWarningsPerRead = 3

// skipLog collects the keys a fallback read could not turn into edges, naming
// the first few and counting the rest.
type skipLog struct {
	nodeID string
	named  int
	total  int
}

func newSkipLog(nodeID string) *skipLog { return &skipLog{nodeID: nodeID} }

func (s *skipLog) record(reason, key string) {
	s.total++
	if s.named < maxSkipWarningsPerRead {
		s.named++
		slog.Warn("adjacency: skipping an unusable key while enumerating a marked node's links",
			"nodeId", s.nodeID, "key", key, "reason", reason)
	}
}

// flush reports the total once the read is done, so a cap on named keys never
// hides the scale of the problem.
func (s *skipLog) flush() {
	if s.total > s.named {
		slog.Warn("adjacency: more unusable keys were skipped than were named above",
			"nodeId", s.nodeID, "skipped", s.total, "named", s.named)
	}
}

// linkSetFingerprint condenses a marked node's matched link set into the
// uint64 a footprint records, as an order-independent hash over every matched
// (key, revision) pair.
//
// A hash rather than a maximum sequence, because the set is what has to be
// pinned, not its high-water mark: a hard-deleted link leaves the set without
// touching any surviving entry's revision, and no maximum over the survivors
// can see that. Order-independent, because the pairs arrive from a map. The
// hash is only ever compared for equality by the caller that recorded it
// (a footprint validates by re-reading and comparing), so it needs no
// ordering or monotonicity — only that it changes whenever the set does.
//
// Soft-tombstoned links are hashed in even though they are not returned as
// edges: a link flipping isDeleted moves its revision, which is drift the
// caller must see, and hashing it in reports that with no dependence on the
// flag's direction.
func linkSetFingerprint(entries map[string]*substrate.KVEntry) uint64 {
	// A commutative combine over per-pair hashes: order-independent by
	// construction, and seeded from FNV's offset basis rather than zero so an
	// empty set is distinguishable from the 0 that means "no adjacency
	// document" on the unmarked path.
	acc := uint64(fnvOffset64)
	for key, entry := range entries {
		h := fnv.New64a()
		_, _ = h.Write([]byte(key))
		// The separator byte keeps a key's bytes from running into the
		// revision's, so no (key, revision) pair can be spelled by another.
		var rev [9]byte
		binary.BigEndian.PutUint64(rev[1:], entry.Revision)
		_, _ = h.Write(rev[:])
		acc += h.Sum64()
	}
	// Fold in the cardinality so the combine cannot be fooled by a set whose
	// per-pair hashes happen to sum the same, and keep 0 out of the codomain
	// for the same reason the seed is not zero.
	acc = acc*fnvPrime64 + uint64(len(entries))
	if acc == 0 {
		acc = 1
	}
	return acc
}
