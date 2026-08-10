package adjacency_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// nanoID pads a readable prefix out to the canonical 20-character NanoID
// length. Seed data has to carry real NanoIDs: a link key with a short id
// fails substrate.ParseLinkKey, and the fallback read would then silently
// drop the very edges a case is about.
func nanoID(t *testing.T, prefix string) string {
	t.Helper()
	id := prefix + strings.Repeat("a", substrate.NanoIDLength-len(prefix))
	require.True(t, substrate.IsValidNanoID(id), "seed id %q must be a canonical NanoID", id)
	return id
}

// seedLink writes a Contract #1 link envelope into Core KV and returns its key.
func seedLink(t *testing.T, coreKV *substrate.KV, srcType, srcID, relation, dstType, dstID string, deleted bool) string {
	t.Helper()
	key := substrate.LinkKey(srcType, srcID, relation, dstType, dstID)
	body, err := json.Marshal(map[string]any{"key": key, "isDeleted": deleted})
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, body)
	require.NoError(t, err)
	return key
}

// markNode sets a node's overflow latch the way another Refractor instance
// would: straight into KV, with nothing in this process's cache to find it by.
func markNode(t *testing.T, adjKV *substrate.KV, nodeID string) {
	t.Helper()
	body, err := json.Marshal(adjacency.AdjMark{Degree: 9999, Bytes: 999999})
	require.NoError(t, err)
	_, err = adjKV.Create(context.Background(), subjects.AdjMarkKey(nodeID), body)
	require.NoError(t, err)
}

// isMarked reports whether a node's overflow latch exists in KV.
func isMarked(t *testing.T, adjKV *substrate.KV, nodeID string) bool {
	t.Helper()
	_, err := adjKV.Get(context.Background(), subjects.AdjMarkKey(nodeID))
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
	return false
}

// documentEdges reads a node's adjacency document directly, bypassing
// Neighbors — the only way to see what the write path actually stored once
// the read path has started answering from Core KV instead.
func documentEdges(t *testing.T, adjKV *substrate.KV, nodeID string) []adjacency.EdgeEntry {
	t.Helper()
	entry, err := adjKV.Get(context.Background(), subjects.AdjKey(nodeID))
	if err != nil {
		require.ErrorIs(t, err, substrate.ErrKeyNotFound)
		return nil
	}
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	return doc.Edges
}

// linkEdgeEvent builds the outbound half of one Contract #1 link's indexing,
// as EventsForLink would. Seed edges have to carry real link keys: the latch
// refuses to fire on a document holding an edge the Core KV fallback could not
// rebuild, which is exactly an edge with no link key.
func linkEdgeEvent(t *testing.T, nodeID string, i int) adjacency.CoreKVEvent {
	t.Helper()
	peer := nanoID(t, peerPrefix(i))
	key := substrate.LinkKey("identity", nodeID, "holdsRole", "role", peer)
	return adjacency.CoreKVEvent{
		CoreKvKey: key, EdgeID: key, Name: "holdsRole",
		Direction: "outbound", NodeID: nodeID, OtherNodeID: peer, OtherType: "role",
	}
}

// buildEdges indexes n distinct outbound link edges on nodeID, returning their
// keys in seeding order.
func buildEdges(t *testing.T, adjKV *substrate.KV, nodeID string, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		evt := linkEdgeEvent(t, nodeID, i)
		keys = append(keys, evt.EdgeID)
		require.NoError(t, adjacency.Build(context.Background(), adjKV, evt))
	}
	return keys
}

func TestBuild_LatchesOnTheDegreeThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	adjKV, _ := startKVs(t)
	// Bytes deliberately out of reach: only the degree arm may fire here.
	defer adjacency.SetOverflowThresholds(3, 1<<20)()

	nodeID := nanoID(t, "nda")
	buildEdges(t, adjKV, nodeID, 3)
	require.False(t, isMarked(t, adjKV, nodeID), "a node at the threshold must not latch")
	assert.Len(t, documentEdges(t, adjKV, nodeID), 3)

	buildEdges(t, adjKV, nodeID, 4)
	assert.True(t, isMarked(t, adjKV, nodeID), "the fourth edge must carry the node past the threshold")
	assert.Empty(t, documentEdges(t, adjKV, nodeID), "a latched node's document must be emptied")
}

func TestBuild_LatchesOnTheByteThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	adjKV, _ := startKVs(t)
	// Degree deliberately out of reach: only the byte arm may fire here. The
	// two arms are independent because entries are variable-length — a handful
	// of long entries outweighs a great many short ones.
	defer adjacency.SetOverflowThresholds(1<<20, 128)()

	nodeID := nanoID(t, "ndb")
	buildEdges(t, adjKV, nodeID, 4)

	assert.True(t, isMarked(t, adjKV, nodeID), "a small edge count must still latch on size")
	assert.Empty(t, documentEdges(t, adjKV, nodeID))
}

func TestBuild_MarkedNodeAbsorbsNoFurtherWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)
	defer adjacency.SetOverflowThresholds(2, 1<<20)()

	nodeID := nanoID(t, "ndc")
	seeded := buildEdges(t, adjKV, nodeID, 3)
	require.True(t, isMarked(t, adjKV, nodeID))

	// Upsert path: a new edge is not absorbed.
	require.NoError(t, adjacency.Build(ctx, adjKV, linkEdgeEvent(t, nodeID, 99)))
	assert.Empty(t, documentEdges(t, adjKV, nodeID), "the upsert path must not write to a marked node")

	// Removal path: a retraction is not absorbed either. The retraction still
	// happens — it happens in Core KV, which is what a marked node is read
	// from — so re-materializing the document here would be the bug.
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		EdgeID: seeded[0], NodeID: nodeID, IsDeleted: true,
	}))
	assert.Empty(t, documentEdges(t, adjKV, nodeID), "the removal path must not write to a marked node")
	assert.True(t, isMarked(t, adjKV, nodeID), "the latch is never lifted")
}

func TestBuild_HonorsAMarkItNeverWroteItself(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	// The mark is set the way a second Refractor instance would set it: this
	// process never latched the node itself, so only the batched read of the
	// document and the mark together can account for the no-op below.
	nodeID := nanoID(t, "ndd")
	markNode(t, adjKV, nodeID)

	require.NoError(t, adjacency.Build(ctx, adjKV, linkEdgeEvent(t, nodeID, 0)))
	assert.Nil(t, documentEdges(t, adjKV, nodeID), "a mark this process never wrote must suppress its writes too")
}

func TestBuild_LatchIsIdempotentAcrossConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	adjKV, _ := startKVs(t)
	defer adjacency.SetOverflowThresholds(3, 1<<20)()

	// Three writers index one node's edges concurrently in production (the
	// bootstrap consumer, the lens fan-out's link pre-apply, plain-link
	// reprojection), so all three can cross the threshold on the same burst.
	nodeID := nanoID(t, "nde")
	const writers = 3
	const perWriter = 4

	// Every event is built HERE, on the test goroutine. The builder asserts on
	// its seed ids, and testing.T's failure calls are only valid from the
	// goroutine running the test — a worker calling one would abandon its
	// caller mid-stack and hang the run instead of reporting.
	events := make([][]adjacency.CoreKVEvent, writers)
	for w := range events {
		events[w] = make([]adjacency.CoreKVEvent, perWriter)
		for i := range events[w] {
			events[w][i] = linkEdgeEvent(t, nodeID, w*perWriter+i)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for _, evt := range events[w] {
				if err := adjacency.Build(context.Background(), adjKV, evt); err != nil {
					errs[w] = err
					return
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	for w, err := range errs {
		require.NoError(t, err, "writer %d must not see the latch as a failure", w)
	}
	assert.True(t, isMarked(t, adjKV, nodeID))
	assert.Empty(t, documentEdges(t, adjKV, nodeID))
}

func TestNeighbors_UnmarkedNodeMatchesTheDocumentExactly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// An absent document: an empty non-nil slice at revision 0.
	edges, rev, err := adjacency.Neighbors(ctx, adjKV, coreKV, "nodeAbsent")
	require.NoError(t, err)
	require.NotNil(t, edges)
	assert.Empty(t, edges)
	assert.Zero(t, rev)

	// A present document: exactly its stored edges, at exactly its KV
	// revision — the guarantee that adding the latch changes nothing for the
	// nodes that never reach it.
	nodeID := nanoID(t, "ndf")
	seeded := buildEdges(t, adjKV, nodeID, 3)

	entry, err := adjKV.Get(ctx, subjects.AdjKey(nodeID))
	require.NoError(t, err)
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))

	edges, rev, err = adjacency.Neighbors(ctx, adjKV, coreKV, nodeID)
	require.NoError(t, err)
	assert.Equal(t, doc.Edges, edges)
	assert.Equal(t, entry.Revision, rev)

	// And a document emptied by retraction stays distinguishable from an
	// absent one: it keeps a real revision.
	for _, key := range seeded {
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			EdgeID: key, NodeID: nodeID, IsDeleted: true,
		}))
	}
	edges, rev, err = adjacency.Neighbors(ctx, adjKV, coreKV, nodeID)
	require.NoError(t, err)
	assert.Empty(t, edges)
	assert.NotZero(t, rev, "an emptied document is not an absent one")
}

func TestNeighbors_MarkedNodeReadsCoreKV(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "hub")
	roleID := nanoID(t, "rrr")
	taskID := nanoID(t, "task")

	outKey := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", roleID, false)
	inKey := seedLink(t, coreKV, "task", taskID, "assignedTo", "identity", hub, false)
	// A link on neither endpoint of the hub must not leak into its read.
	seedLink(t, coreKV, "task", taskID, "forRole", "role", roleID, false)

	markNode(t, adjKV, hub)

	edges, fingerprint, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, 2)
	assert.NotZero(t, fingerprint)

	byID := map[string]adjacency.EdgeEntry{}
	for _, e := range edges {
		byID[e.EdgeID] = e
	}

	out := byID[outKey]
	assert.Equal(t, outKey, out.CoreKvKey)
	assert.Equal(t, "outbound", out.Direction)
	assert.Equal(t, "holdsRole", out.Name)
	assert.Equal(t, roleID, out.OtherNodeID)
	assert.Equal(t, "role", out.OtherType)

	in := byID[inKey]
	assert.Equal(t, inKey, in.CoreKvKey)
	assert.Equal(t, "inbound", in.Direction)
	assert.Equal(t, "assignedTo", in.Name)
	assert.Equal(t, taskID, in.OtherNodeID)
	assert.Equal(t, "task", in.OtherType)
}

func TestNeighbors_MarkedNodeSelfLinkYieldsBothDirections(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "sfx")
	key := seedLink(t, coreKV, "identity", hub, "supervises", "identity", hub, false)
	markNode(t, adjKV, hub)

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, 2, "a self-link is both an outbound and an inbound edge of the same node")

	dirs := []string{edges[0].Direction, edges[1].Direction}
	assert.ElementsMatch(t, []string{"inbound", "outbound"}, dirs)
	for _, e := range edges {
		assert.Equal(t, key, e.EdgeID)
		assert.Equal(t, hub, e.OtherNodeID)
		assert.Equal(t, "identity", e.OtherType)
	}
}

func TestNeighbors_MarkedNodeDropsSoftTombstonedLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "tomb")
	roleID := nanoID(t, "rrr")
	liveKey := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", roleID, false)
	deadRole := nanoID(t, "gone")
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", deadRole, false)
	markNode(t, adjKV, hub)

	edges, before, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, 2)

	// Retracting a link on a marked node is a soft tombstone in Core KV — the
	// write path indexes nothing, so this read is the only transport the
	// retraction has.
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", deadRole, true)

	edges, after, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, liveKey, edges[0].EdgeID)
	assert.NotEqual(t, before, after, "a retraction must move the fingerprint")
}

func TestNeighbors_MarkedNodeFingerprintTracksTheLinkSet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "fing")
	firstRole := nanoID(t, "rfa")
	secondRole := nanoID(t, "rsb")
	firstKey := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", firstRole, false)
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", secondRole, false)
	markNode(t, adjKV, hub)

	_, base, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)

	_, again, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Equal(t, base, again, "an unchanged link set must fingerprint identically")

	// A new link.
	thirdRole := nanoID(t, "rtc")
	seedLink(t, coreKV, "identity", hub, "holdsRole", "role", thirdRole, false)
	_, grown, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.NotEqual(t, base, grown)

	// A link leaving the keyspace outright. firstKey is the OLDEST of the
	// three, so the highest revision in the matched set is unchanged by its
	// removal — the case a max-sequence fingerprint cannot see, and the reason
	// this one hashes the whole (key, revision) set instead.
	beforeMax := maxRevision(t, ctx, coreKV, hub)
	require.NoError(t, coreKV.Delete(ctx, firstKey))
	edges, shrunk, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, 2)
	require.Equal(t, beforeMax, maxRevision(t, ctx, coreKV, hub),
		"the seed must actually exercise the blind spot: the surviving maximum must not move")
	assert.NotEqual(t, grown, shrunk, "a link dropping out must move the fingerprint")
}

// maxRevision returns the highest KV revision among the links touching nodeID
// — the value a max-sequence fingerprint would have reported.
func maxRevision(t *testing.T, ctx context.Context, coreKV *substrate.KV, nodeID string) uint64 {
	t.Helper()
	entries, err := coreKV.GetMulti(ctx, []string{
		substrate.LinkPrefix + ".*." + nodeID + ".>",
		substrate.LinkPrefix + ".*.*.*.*." + nodeID,
	})
	require.NoError(t, err)
	var max uint64
	for _, e := range entries {
		if e.Revision > max {
			max = e.Revision
		}
	}
	return max
}

func TestNeighbors_MarkedNodeWithoutCoreKVFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	hub := nanoID(t, "nokv")
	markNode(t, adjKV, hub)

	_, _, err := adjacency.Neighbors(ctx, adjKV, nil, hub)
	require.Error(t, err, "a marked node with no Core KV handle must error, never report an empty edge set")
}

func TestNeighbors_LatchedNodeServesEveryEdgeItStoppedIndexing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)
	defer adjacency.SetOverflowThresholds(2, 1<<20)()

	// The end-to-end shape: links land in Core KV, the write path indexes them
	// until the node latches, and from then on the read path answers from Core
	// KV — including the edges that arrived after the latch and were never
	// indexed at all.
	hub := nanoID(t, "e2e")
	var want []string
	for i := 0; i < 5; i++ {
		roleID := nanoID(t, fmt.Sprintf("rr%d", i+1))
		key := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", roleID, false)
		want = append(want, key)
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: key, EdgeID: key, Name: "holdsRole", Direction: "outbound",
			NodeID: hub, OtherNodeID: roleID, OtherType: "role",
		}))
	}

	require.True(t, isMarked(t, adjKV, hub))
	require.Empty(t, documentEdges(t, adjKV, hub), "the document must hold none of them")

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	got := make([]string, len(edges))
	for i, e := range edges {
		got[i] = e.EdgeID
	}
	assert.ElementsMatch(t, want, got)
}

func TestNeighbors_MarkedNodeReadsPastTheFastPathSubjectCap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// The node this whole mechanism exists for has ~3,900 links, so its read
	// never takes the batched fast path: past 1,024 matched subjects the
	// substrate primitive switches to a stability-verified consumer drain,
	// which filters by consumer subject rather than by direct-get and REFUSES
	// a filter pair where one subject subsumes the other. This case is the
	// proof that the outbound and inbound filters this package hands it are a
	// legal pair — and that the marked read is complete at that size.
	hub := nanoID(t, "cap")
	const links = 1100
	want := make([]string, 0, links)
	for i := 0; i < links; i++ {
		peer := nanoID(t, peerPrefix(i))
		if i%2 == 0 {
			want = append(want, seedLink(t, coreKV, "identity", hub, "holdsRole", "role", peer, false))
			continue
		}
		want = append(want, seedLink(t, coreKV, "task", peer, "assignedTo", "identity", hub, false))
	}
	markNode(t, adjKV, hub)

	edges, fingerprint, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, edges, links)
	assert.NotZero(t, fingerprint)

	got := make([]string, len(edges))
	for i, e := range edges {
		got[i] = e.EdgeID
	}
	assert.ElementsMatch(t, want, got)
}

// peerPrefix renders a counter as a NanoID-alphabet-safe prefix. The Contract
// #1 alphabet drops the visually ambiguous characters, "0" among them, so a
// plain decimal counter is not a legal id fragment; mapping "0" to "z" keeps
// the rendering injective (same length, one character mapped one-to-one) and
// so keeps every generated peer distinct.
func peerPrefix(i int) string {
	return strings.ReplaceAll(fmt.Sprintf("p%d", i+1), "0", "z")
}

func TestBuild_RefusesToLatchEdgesTheFallbackCannotRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)
	defer adjacency.SetOverflowThresholds(2, 1<<20)()

	// The legacy event path indexes an edge from any Core KV message carrying
	// a nodeId, and such an edge has no link key. Latching the node would hand
	// its reads to an enumeration of the LINK keyspace, which cannot see that
	// edge — and the mark is never lifted, so the edge would be gone for good.
	nodeID := nanoID(t, "ndg")
	buildEdges(t, adjKV, nodeID, 2)
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "core.legacy", EdgeID: "legacy", Name: "rel",
		Direction: "outbound", NodeID: nodeID, OtherNodeID: "other",
	}))

	// Three edges against a threshold of two: the node is past the line and
	// must still not latch.
	require.False(t, isMarked(t, adjKV, nodeID), "a node holding a non-link edge must not latch")
	assert.Len(t, documentEdges(t, adjKV, nodeID), 3, "and must keep serving that edge from its document")

	// Once the unreconstructable edge is retracted the node latches normally,
	// so the guard blocks the hazard rather than the mechanism.
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		EdgeID: "legacy", NodeID: nodeID, IsDeleted: true,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, linkEdgeEvent(t, nodeID, 7)))
	assert.True(t, isMarked(t, adjKV, nodeID))
}

func TestBuild_SelfLinkCollapsesInTheDocumentAndSplitsOnceMarked(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// Both halves of a self-link carry the same NodeID and the same EdgeID, and
	// upsertEntry matches on EdgeID alone, so the document keeps only the one
	// written last. The fallback derives direction from the key's endpoints and
	// keeps both. Latching therefore CHANGES this node's self-link answer, from
	// one entry to two — knowingly, in the direction of the correct one.
	hub := nanoID(t, "sfd")
	key := seedLink(t, coreKV, "identity", hub, "supervises", "identity", hub, false)
	for _, evt := range adjacency.EventsForLink(key, "identity", hub, "supervises", "identity", hub, false) {
		require.NoError(t, adjacency.Build(ctx, adjKV, evt))
	}

	stored := documentEdges(t, adjKV, hub)
	require.Len(t, stored, 1, "the document collapses a self-link to one entry")
	assert.Equal(t, "inbound", stored[0].Direction, "the inbound half is written second and wins")

	unmarked, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, unmarked, 1)

	markNode(t, adjKV, hub)
	marked, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.Len(t, marked, 2, "the fallback restores the outbound view the document lost")
	assert.ElementsMatch(t, []string{"inbound", "outbound"},
		[]string{marked[0].Direction, marked[1].Direction})
}

func TestBuild_RebuildsAfterTheBucketIsWipedUnderALiveProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)
	defer adjacency.SetOverflowThresholds(2, 1<<20)()

	// The Refractor is built to survive a NATS outage and reconnect rather than
	// exit, so a bucket wiped or recreated underneath it is observed by a
	// process that never restarted. Nothing may remember the mark across that:
	// a process answering from its own memory would suppress every write for a
	// node the bucket no longer knows about, and then serve an empty edge set
	// as authoritative, permanently and silently.
	nodeID := nanoID(t, "ndw")
	buildEdges(t, adjKV, nodeID, 3)
	require.True(t, isMarked(t, adjKV, nodeID))

	require.NoError(t, adjKV.Purge(ctx, subjects.AdjMarkKey(nodeID)))
	require.NoError(t, adjKV.Purge(ctx, subjects.AdjKey(nodeID)))

	evt := linkEdgeEvent(t, nodeID, 0)
	require.NoError(t, adjacency.Build(ctx, adjKV, evt))

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, nodeID)
	require.NoError(t, err)
	require.Len(t, edges, 1, "the index must rebuild from zero, not stay suppressed by a remembered mark")
	assert.Equal(t, evt.EdgeID, edges[0].EdgeID)
}

// seedMarkedHub marks a node and seeds it past the batched fast path's
// 1,024-subject cap, so every read of it takes the consumer drain — the shape
// the one node this mechanism exists for actually has.
func seedMarkedHub(t *testing.T, adjKV, coreKV *substrate.KV, prefix string, links int) (hub string, keys []string) {
	t.Helper()
	hub = nanoID(t, prefix)
	keys = make([]string, 0, links)
	for i := 0; i < links; i++ {
		keys = append(keys, seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, peerPrefix(i)), false))
	}
	markNode(t, adjKV, hub)
	return hub, keys
}

// rewriteLinks rewrites links in a loop until stopped, pausing between writes.
// It signals once the first write has landed so a reader can be sure its work
// overlaps the churn rather than racing it to the start line. stop is
// idempotent and also runs at test cleanup, so a case may stop the writer
// early to take a quiescent reading without leaking it on an earlier failure.
func rewriteLinks(t *testing.T, coreKV *substrate.KV, keys []string, gap time.Duration) (started <-chan struct{}, stop func()) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"isDeleted": false})
	require.NoError(t, err)

	done := make(chan struct{})
	first := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		signalled := false
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			if _, err := coreKV.Put(context.Background(), keys[i%len(keys)], body); err != nil {
				return
			}
			if !signalled {
				close(first)
				signalled = true
			}
			if gap > 0 {
				timer := time.NewTimer(gap)
				select {
				case <-done:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(done)
			wg.Wait()
		})
	}
	t.Cleanup(stop)
	return first, stop
}

func edgeIDs(edges []adjacency.EdgeEntry) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.EdgeID
	}
	return out
}

func TestNeighbors_MarkedNodeStaysReadableWhileItTakesWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// The node that overflowed is by construction the one taking writes
	// fastest, and every read of it drains a consumer. Demanding that nothing
	// move during that drain fails the read on the very node it exists for, so
	// the read asks for completeness and not simultaneity. Writes land
	// throughout this case at a rate a busy hub plausibly sustains, and every
	// read must come back whole.
	hub, want := seedMarkedHub(t, adjKV, coreKV, "bsy", 1100)

	started, stop := rewriteLinks(t, coreKV, want, 2*time.Millisecond)
	defer stop()
	<-started

	for attempt := 0; attempt < 3; attempt++ {
		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
		require.NoError(t, err, "a read overlapping live writes must not fail")
		require.ElementsMatch(t, want, edgeIDs(edges), "and must still be complete")
	}
}

func TestNeighbors_MarkedNodeIsNeverQuietlyShort(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// The invariant that has to hold at ANY write rate, including one no real
	// workload produces: a read either returns every edge or fails loudly. A
	// quietly short answer is the failure this whole mechanism exists to
	// prevent — on the capability plane a missing edge is a grant that
	// silently disappears — so a writer fast enough to outrun the drain must
	// cost availability, never correctness.
	hub, want := seedMarkedHub(t, adjKV, coreKV, "sat", 1100)

	started, stop := rewriteLinks(t, coreKV, want, 0)
	<-started

	for attempt := 0; attempt < 5; attempt++ {
		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
		if err != nil {
			// A writer this fast can outrun the drain, and losing that race
			// is the sanctioned outcome: availability, not correctness.
			continue
		}
		require.ElementsMatch(t, want, edgeIDs(edges),
			"a read that reports success must be complete")
	}

	// Every attempt above is allowed to fail, so on its own the loop can pass
	// while asserting nothing. This is the positive vector that makes it mean
	// something: with the writer stopped, a read of the same node MUST succeed
	// and MUST be complete. A mechanism broken badly enough to fail here would
	// have made every attempt above vacuous.
	stop()
	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err, "a quiescent read of the same node must succeed")
	require.ElementsMatch(t, want, edgeIDs(edges), "and must be complete")
}

func TestNeighbors_MarkedNodeSkipsUnusableKeysInsteadOfFailing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	hub := nanoID(t, "bad")
	good := seedLink(t, coreKV, "identity", hub, "holdsRole", "role", nanoID(t, "rrr"), false)

	// A key the outbound filter admits but ParseLinkKey rejects. NATS' `>`
	// matches one token or many, so `lnk.*.<hub>.>` catches this four-segment
	// key even though no link key is ever that shape.
	_, err := coreKV.Put(ctx, substrate.LinkPrefix+".identity."+hub+".stray", []byte(`{"isDeleted":false}`))
	require.NoError(t, err)

	// A well-formed key whose body is not JSON at all.
	_, err = coreKV.Put(ctx, substrate.LinkKey("identity", hub, "holdsRole", "role", nanoID(t, "rzz")), []byte("not json"))
	require.NoError(t, err)

	markNode(t, adjKV, hub)

	// Aborting on either of these would cost the hub its entire edge set on
	// EVERY read — the fallback re-parses every body every time, so one bad
	// entry would be permanent, which is the frozen-wrong answer again.
	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, hub)
	require.NoError(t, err, "one unusable key must not fail the whole read")
	require.Len(t, edges, 1)
	assert.Equal(t, good, edges[0].EdgeID)
}

func TestNeighbors_MarkedNodeRetractsALinkHardDeletedMidDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	// A marked node's read drains a consumer over several rounds, accumulating
	// entries as it goes, and a link can be hard-deleted while that is in
	// flight: an earlier round collects it as live, and the tombstone arrives
	// in a later one. If the later round merely skips the tombstone instead of
	// retracting the entry, the read hands back a revoked link as live — an
	// over-grant on the capability plane, and one that survives the footprint
	// check, because two consecutive reads can agree on a fingerprint that
	// includes the same deleted link.
	//
	// The window is unreachable from outside: a single-round drain always sees
	// the tombstone as its subject's last message. The hook below opens it by
	// committing the delete between one round and the next.
	hub, want := seedMarkedHub(t, adjKV, coreKV, "mdd", 1100)
	doomed := want[0]
	survivors := want[1:]

	deleted := false
	hookCtx := substrate.WithKVDrainRoundHook(ctx, func(round int) {
		if deleted {
			return
		}
		deleted = true
		// Hard delete, not a soft tombstone: this is the NATS-level marker
		// the drain has to act on, and the one the primitive strips.
		require.NoError(t, coreKV.Delete(context.Background(), doomed))
		// More writes so the drain has a further round to deliver it in.
		body, err := json.Marshal(map[string]any{"isDeleted": false})
		require.NoError(t, err)
		for _, key := range survivors[:50] {
			_, perr := coreKV.Put(context.Background(), key, body)
			require.NoError(t, perr)
		}
	})

	edges, _, err := adjacency.Neighbors(hookCtx, adjKV, coreKV, hub)
	require.NoError(t, err)
	require.True(t, deleted, "the mid-drain delete must actually have run")

	got := edgeIDs(edges)
	require.NotContains(t, got, doomed, "a link hard-deleted mid-drain must not come back as live")
	require.ElementsMatch(t, survivors, got, "and every surviving link must still be there")
}
