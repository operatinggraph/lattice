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
	observeRead(ctx, nodeID, nil, st.marked, true)
	if st.marked {
		return neighborsFromCoreKV(ctx, coreKV, nodeID, nil)
	}
	if !st.docFound {
		return []EdgeEntry{}, 0, nil
	}
	return st.doc.Edges, st.docRev, nil
}

// NeighborsScoped returns nodeID's edges read at the narrowest scope the
// node's own shape rewards, and reports WHICH scope that was so a caller
// memoizing the answer knows what key it may file it under.
//
// One node-state read decides the shape, and the two shapes answer
// differently:
//
//   - Unmarked node (all but the rare hub): the whole adjacency document,
//     unfiltered, with the document's KV revision as the fingerprint (0 when
//     the document is absent) and whole=true. Narrowing an unmarked node buys
//     nothing — the document is one key however many relations a caller
//     follows — and would cost one read per relation instead of one per node,
//     so rels is ignored here and the caller filters the answer it gets. The
//     edges and the fingerprint are exactly what Neighbors returns for the
//     same node, and comparable with it.
//   - Overflow-marked node: only rels' links, out of Core KV's link keyspace
//     under per-relation Contract #1 subject filters, with the scoped
//     fingerprint and whole=false.
//
// rels must name at least one relation. An empty set is an ERROR rather than
// an empty answer: on a marked node the difference between "this node has no
// links of the relations you asked for" and "you asked for nothing" is exactly
// the difference this function must never blur, and answering quietly would
// hand a caller a short edge list for the one node class where a short list is
// the failure the overflow latch exists to prevent. A caller that has genuinely
// proven it follows nothing wants NeighborsByRelation, whose contract is to
// read nothing and answer with no edges.
//
// whole is the discriminator a footprint depends on: a scoped fingerprint
// covers only what THIS read matched and is not comparable with a whole read's
// (see NeighborsByRelation), so a caller recording fingerprints records only
// the whole=true ones and pins a whole=false read by some other unit.
//
// coreKV may be nil only for a caller that never reaches a marked node; a
// marked node with no Core KV handle is an error, never a silently short edge
// list.
func NeighborsScoped(ctx context.Context, kv, coreKV *substrate.KV, nodeID string, rels map[string]struct{}) ([]EdgeEntry, uint64, bool, error) {
	if len(rels) == 0 {
		return nil, 0, false, fmt.Errorf("adjacency: NeighborsScoped %s: at least one relation is required", nodeID)
	}
	st, err := readNodeState(ctx, kv, nodeID)
	if err != nil {
		return nil, 0, false, err
	}
	observeRead(ctx, nodeID, rels, st.marked, !st.marked)
	if st.marked {
		edges, fingerprint, cerr := neighborsFromCoreKV(ctx, coreKV, nodeID, rels)
		return edges, fingerprint, false, cerr
	}
	if !st.docFound {
		return []EdgeEntry{}, 0, true, nil
	}
	return st.doc.Edges, st.docRev, true, nil
}

// NeighborsByRelation returns only the edges of nodeID named by rels. It is
// Neighbors narrowed to the relations a caller can prove it will follow, and it
// exists for the node Neighbors is worst at: an overflow-marked hub, whose read
// enumerates the node's whole link keyspace out of Core KV. A caller that will
// follow one relation of a hub's thousands pays for one relation's links here.
//
// An empty rels set returns no edges and reads nothing at all — a caller that
// has proven it follows nothing does not need the node.
//
// The two node shapes narrow in different places, and both answers are exact:
//
//   - Unmarked node: the same one batched read Neighbors takes, with the
//     document's edges filtered by name. The narrowing is in the answer, not in
//     the read — a document is one key either way.
//   - Overflow-marked node: per-relation Contract #1 subject filters, so Core
//     KV matches only the named relations' links. The in-memory name filter
//     still runs over what comes back, so the answer never depends on the
//     filters being the tightest expressible.
//
// The returned fingerprint covers only what THIS read matched, so it is not
// comparable with a fingerprint Neighbors produced for the same node: a
// footprint that captured one and validated with the other would report drift
// on every read. Callers that walk rather than capture (the actor enumerator)
// discard it.
func NeighborsByRelation(ctx context.Context, kv, coreKV *substrate.KV, nodeID string, rels map[string]struct{}) ([]EdgeEntry, uint64, error) {
	if len(rels) == 0 {
		return []EdgeEntry{}, 0, nil
	}
	edges, fingerprint, whole, err := NeighborsScoped(ctx, kv, coreKV, nodeID, rels)
	if err != nil {
		return nil, 0, err
	}
	// The marked arm narrowed the READ, so its answer is already exact; the
	// unmarked arm answers with the whole document, and the narrowing to rels
	// is this filter.
	if whole {
		return filterEdgesByRelation(edges, rels), fingerprint, nil
	}
	return edges, fingerprint, nil
}

// filterEdgesByRelation keeps the edges whose relation name rels holds,
// returning a non-nil slice so an empty answer reads the same way Neighbors'
// does.
func filterEdgesByRelation(edges []EdgeEntry, rels map[string]struct{}) []EdgeEntry {
	out := make([]EdgeEntry, 0, len(edges))
	for _, e := range edges {
		if _, ok := rels[e.Name]; ok {
			out = append(out, e)
		}
	}
	return out
}

// neighborsFromCoreKV builds a marked node's edge list from Core KV itself,
// the record the adjacency document is only ever a cache of. A non-empty rels
// narrows the read (and the answer) to those relation names; nil reads the
// whole node.
//
// Both directions are read in ONE request, under Contract #1 link-key filters:
// `lnk.*.<nodeID>.>` catches every link whose SOURCE is the node (segment 3),
// `lnk.*.*.*.*.<nodeID>` every link whose TARGET is (segment 6). Neither filter
// is a subject-subset of the other — the source form pins a literal where the
// target form wildcards, and its trailing `>` sits where the target form
// carries a literal — so they are a legal pair to hand to one request even
// though they intersect on self-links. A scoped read pins the relation segment
// in both forms and repeats the pair per relation, which keeps that property
// (two filters naming different relations are disjoint at segment 4). The read
// is also commit-fresh, which the document is not: Core KV holds the link the
// moment the write commits, so a marked node never needs the pipelines' link
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
func neighborsFromCoreKV(ctx context.Context, coreKV *substrate.KV, nodeID string, rels map[string]struct{}) ([]EdgeEntry, uint64, error) {
	if coreKV == nil {
		return nil, 0, fmt.Errorf("adjacency: node %s is overflow-marked and needs a Core KV handle to read", nodeID)
	}

	entries, err := coreKV.GetMultiNoSnapshot(ctx, linkFiltersFor(nodeID, rels))
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

	// A scoped read's fingerprint must cover exactly the entries its ANSWER was
	// derived from, or the two stop meaning the same thing: linkFiltersFor
	// widens to the unscoped pair for a relation name that cannot be spelled as
	// one subject token, and hashing everything those filters matched would pin
	// relations the caller never asked for — reporting drift on a write the
	// answer could not have depended on, and making one relation's fingerprint
	// depend on whether some OTHER relation's name happened to be spellable.
	// So a scoped read hashes only what passes the in-memory relation filter.
	// An unscoped read (rels == nil) hashes every entry the filters matched,
	// including keys this loop could not parse into an edge: nothing is out of
	// scope there, so a change to any of them is a change to this node.
	hashed := entries
	if rels != nil {
		hashed = make(map[string]*substrate.KVEntry, len(entries))
	}

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

		// The scope again, in memory. The subject filters above already withhold
		// the other relations, so this normally rejects nothing — but the
		// filters are a READ narrowing (linkFiltersFor widens them for a
		// relation name that cannot be spelled as one subject token) and this is
		// what makes the ANSWER exact whatever they matched.
		if rels != nil {
			if _, wanted := rels[linkName]; !wanted {
				continue
			}
			// In scope, so in the fingerprint — before the tombstone test
			// below, since a link flipping isDeleted is drift this read's
			// caller must see (see linkSetFingerprint).
			hashed[key] = entry
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
	SortEdges(edges)

	return edges, linkSetFingerprint(hashed), nil
}

// SortEdges orders an edge list in place by EdgeID, then by Direction — the
// total order a MARKED node's edges are returned in, since they come out of a
// map and would otherwise follow its iteration order. An unmarked node's edges
// are not sorted: they come back in document order, which is the write path's
// and is already stable.
//
// A caller assembling one list out of several reads of a marked node applies
// this to put the result in the order a single read of that node would have
// produced.
func SortEdges(edges []EdgeEntry) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].EdgeID != edges[j].EdgeID {
			return edges[i].EdgeID < edges[j].EdgeID
		}
		return edges[i].Direction < edges[j].Direction
	})
}

// linkFiltersFor returns the Contract #1 subject filters that match every link
// incident to nodeID, narrowed to rels when it is non-empty.
//
// The unscoped pair is `lnk.*.<nodeID>.>` (the node as source) and
// `lnk.*.*.*.*.<nodeID>` (the node as target). The scoped form pins the
// relation segment in each: `lnk.*.<nodeID>.<rel>.>` and
// `lnk.*.*.<rel>.*.<nodeID>`, one pair per relation, sorted so the request is
// deterministic.
//
// A relation name that is not a single subject token cannot be pinned at all —
// a `.` would split into two segments and a wildcard would match links this
// caller never asked for — so the whole read falls back to the unscoped pair.
// That is the widening direction: the caller's in-memory filter still cuts the
// answer to rels, and only the read is bigger than it needed to be.
func linkFiltersFor(nodeID string, rels map[string]struct{}) []string {
	unscoped := []string{
		substrate.LinkPrefix + ".*." + nodeID + ".>",
		substrate.LinkPrefix + ".*.*.*.*." + nodeID,
	}
	if len(rels) == 0 {
		return unscoped
	}
	names := make([]string, 0, len(rels))
	for rel := range rels {
		if !isSubjectToken(rel) {
			return unscoped
		}
		names = append(names, rel)
	}
	sort.Strings(names)
	out := make([]string, 0, 2*len(names))
	for _, rel := range names {
		out = append(out,
			substrate.LinkPrefix+".*."+nodeID+"."+rel+".>",
			substrate.LinkPrefix+".*.*."+rel+".*."+nodeID)
	}
	return out
}

// isSubjectToken reports whether s can stand as one literal NATS subject
// segment: non-empty, and free of the separator and both wildcards.
func isSubjectToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case '.', '*', '>', ' ', '\t', '\n', '\r':
			return false
		}
	}
	return true
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
