package adjacency_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The ordering guard. A Contract #1 link key IS its own EdgeID, and it is
// stable across a revoke → re-grant, so the same EdgeID names two different
// versions of the same relationship. Three writers index it off one Core KV
// stream with no ordering guarantee between them, and a handler can hold an
// already-fetched message for minutes while the world moves on — so a staler
// view of an edge can reach the index after a newer one, in both directions.
//
// Every case below is driven by explicit sequence arguments: the interleaving
// under test is the order the numbers name, never the order the calls happen to
// run in. And every event is built by EventsForLink over a real Contract #1
// link key, because that is the only shape any production writer of this index
// produces — an edge whose CoreKvKey is not a link key takes different paths
// through the latch and the Core KV fallback.

// nodeDocument reads a node's whole adjacency document and its KV revision.
// documentEdges' view stops at the edge list, and this guard needs both halves
// it does not show: the floors an absent edge leaves behind, and the revision,
// which is how a DECLINED write is told apart from a write of the identical
// document (the latter would churn the bucket and manufacture CAS conflicts for
// the other two writers).
func nodeDocument(t *testing.T, adjKV *substrate.KV, nodeID string) (adjacency.AdjValue, uint64) {
	t.Helper()
	entry, err := adjKV.Get(context.Background(), subjects.AdjKey(nodeID))
	if err != nil {
		require.ErrorIs(t, err, substrate.ErrKeyNotFound)
		return adjacency.AdjValue{}, 0
	}
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	return doc, entry.Revision
}

// orderingLink is one Contract #1 link and the node its outbound arm indexes
// under. Every ordering case works on that arm, so the link's own key is the
// EdgeID under test.
type orderingLink struct {
	key    string
	nodeID string
}

// newOrderingLink mints a link between two fresh vertices, in the exact shape
// EventsForLink is handed in production.
func newOrderingLink(t *testing.T, srcPrefix, dstPrefix string) orderingLink {
	t.Helper()
	srcID, dstID := nanoID(t, srcPrefix), nanoID(t, dstPrefix)
	return orderingLink{
		key:    substrate.LinkKey("identity", srcID, "holdsRole", "role", dstID),
		nodeID: srcID,
	}
}

// create / remove are the outbound halves of the directional pair
// EventsForLink builds for this link at sequence seq — the outbound half
// because that is the one indexed under l.nodeID.
func (l orderingLink) create(seq uint64) adjacency.CoreKVEvent {
	return outboundEvent(l.key, false, seq)
}

func (l orderingLink) remove(seq uint64) adjacency.CoreKVEvent {
	return outboundEvent(l.key, true, seq)
}

// outboundEvent is the source endpoint's half of indexing one link key, built
// the one way production builds it.
func outboundEvent(key string, deleted bool, seq uint64) adjacency.CoreKVEvent {
	srcType, srcID, name, dstType, dstID, ok := substrate.ParseLinkKey(key)
	if !ok {
		panic("ordering test: link key does not parse: " + key)
	}
	return adjacency.EventsForLink(key, srcType, srcID, name, dstType, dstID, deleted, seq)[0]
}

// distinctNanoID renders i as a canonical 20-character NanoID, distinct for
// every i. The digits run 1..9 rather than 0..9 because the Contract #1
// alphabet excludes the visually ambiguous 0 — a seed id that is not a valid
// NanoID is one production could never produce.
func distinctNanoID(t *testing.T, prefix string, i int) string {
	t.Helper()
	var digits []byte
	for n := i; ; n /= 9 {
		digits = append([]byte{byte('1' + n%9)}, digits...)
		if n < 9 {
			break
		}
	}
	return nanoID(t, prefix+string(digits))
}

// TestBuild_StaleRemovalCannotDeleteALiveEdge is the under-grant direction: a
// revocation delayed behind the re-grant that superseded it must not delete the
// live edge the re-grant created. Without the guard the removal matches on
// EdgeID alone, drops the entry, and the relationship is absent to every
// executor walk, the enumerator and the CDC prefix diff until something else
// happens to rewrite the node.
func TestBuild_StaleRemovalCannotDeleteALiveEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)
	link := newOrderingLink(t, "srm", "drm")

	require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))
	_, revAfterCreate := nodeDocument(t, adjKV, link.nodeID)

	require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(100)),
		"a refused write is declined, not failed — a redelivery would only present it again")

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
	require.NoError(t, err)
	require.Len(t, edges, 1, "the re-grant at 200 outranks the revocation at 100 — the edge survives")
	assert.Equal(t, link.key, edges[0].EdgeID)

	doc, rev := nodeDocument(t, adjKV, link.nodeID)
	assert.Empty(t, doc.Removals,
		"a declined removal records no floor: it never happened as far as this node is concerned")
	assert.Equal(t, revAfterCreate, rev,
		"a decline writes nothing at all — rewriting the identical document would churn the revision "+
			"on every stale redelivery and manufacture CAS conflicts for the other two writers")
}

// TestBuild_StaleCreateCannotResurrectARemovedEdge is the over-grant direction,
// and the more dangerous one: a revocation that does not stick. The removal
// leaves no entry behind, so the floor that refuses the delayed create has
// nowhere to live except beside the edge list — which is exactly why the
// document keeps removal floors at all.
func TestBuild_StaleCreateCannotResurrectARemovedEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)
	link := newOrderingLink(t, "scr", "dcr")

	require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(200)))
	_, revAfterRemoval := nodeDocument(t, adjKV, link.nodeID)

	require.NoError(t, adjacency.Build(ctx, adjKV, link.create(100)))

	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
	require.NoError(t, err)
	assert.Empty(t, edges, "the revocation at 200 outranks the create at 100 — the edge stays revoked")

	doc, rev := nodeDocument(t, adjKV, link.nodeID)
	assert.Equal(t, map[string]uint64{link.key: 200}, doc.Removals,
		"the removal's own sequence is the floor the stale create was measured against")
	assert.Equal(t, revAfterRemoval, rev, "the declined create wrote nothing")
}

// TestBuild_NewerRemovalAndNewerCreateStillApply is the positive vector for
// both arms. A guard that refuses everything would pass the two stale cases
// above and be worthless; what makes it a guard rather than a freeze is that an
// event ABOVE the floor still lands, on either arm.
func TestBuild_NewerRemovalAndNewerCreateStillApply(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	t.Run("a newer removal deletes an edge the floor does not protect", func(t *testing.T) {
		link := newOrderingLink(t, "snr", "dnr")
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))
		require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(300)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		assert.Empty(t, edges, "300 clears a floor of 200 — the revocation lands")
	})

	t.Run("a newer create supersedes the removal that preceded it", func(t *testing.T) {
		link := newOrderingLink(t, "snc", "dnc")
		require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(200)))

		fresh := link.create(300)
		fresh.Name = "regranted"
		require.NoError(t, adjacency.Build(ctx, adjKV, fresh))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		require.Len(t, edges, 1, "300 clears a floor of 200 — the re-grant lands")
		assert.Equal(t, "regranted", edges[0].Name)
	})

	t.Run("a redelivery of the message that set the floor is idempotent", func(t *testing.T) {
		link := newOrderingLink(t, "sdp", "ddp")
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		assert.Len(t, edges, 1, "equal sequences are one message twice — applying it again changes nothing")
	})
}

// TestBuild_UnsequencedEventsBehaveExactlyAsBefore pins the compatibility
// direction of the >= comparison AND its limit, because the two are one
// property seen from either side. An event carrying no sequence meets a floor
// of 0 and applies, so an edge no sequenced writer has touched indexes as it
// always did. But 0 is below every REAL floor, so the moment one writer of an
// edge is sequenced, an unsequenced writer of that same edge is declined —
// which is why the mixed case is pinned here rather than left to be discovered
// by a caller who read only the first half of that sentence.
func TestBuild_UnsequencedEventsBehaveExactlyAsBefore(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, coreKV := startKVs(t)

	t.Run("an edge no sequenced writer has touched indexes as it always did", func(t *testing.T) {
		link := newOrderingLink(t, "sun", "dun")
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(0)))
		require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(0)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		assert.Empty(t, edges, "an unsequenced removal is a pure drop, as it has always been")

		doc, _ := nodeDocument(t, adjKV, link.nodeID)
		assert.Empty(t, doc.Removals,
			"an event that names no position in the stream makes no ordering claim, so it records no floor")

		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(0)))
		edges, _, err = adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		assert.Len(t, edges, 1, "and nothing the removal left behind can block the next unsequenced create")
	})

	t.Run("but an unsequenced writer loses to a sequenced one on the same edge", func(t *testing.T) {
		link := newOrderingLink(t, "smx", "dmx")
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))
		require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(0)))

		edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		require.Len(t, edges, 1,
			"0 is below a floor of 200: the unsequenced removal is declined, not applied")

		require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(300)))
		require.NoError(t, adjacency.Build(ctx, adjKV, link.create(0)))
		edges, _, err = adjacency.Neighbors(ctx, adjKV, coreKV, link.nodeID)
		require.NoError(t, err)
		assert.Empty(t, edges, "and the same holds on the other arm, against a removal floor")
	})
}

// TestBuild_UpsertClearsTheRemovalFloor pins that a floor does not outlive the
// entry that supersedes it. Once the edge is back, the entry's own sequence is
// what defends it; keeping the removal's number too would leave one edge
// guarded by two floors that drift apart on the next write.
func TestBuild_UpsertClearsTheRemovalFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)
	link := newOrderingLink(t, "suc", "duc")

	require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(200)))
	doc, _ := nodeDocument(t, adjKV, link.nodeID)
	require.Equal(t, map[string]uint64{link.key: 200}, doc.Removals)

	require.NoError(t, adjacency.Build(ctx, adjKV, link.create(300)))

	doc, _ = nodeDocument(t, adjKV, link.nodeID)
	assert.Empty(t, doc.Removals, "the applied upsert takes the floor over into its own entry")
	require.Len(t, doc.Edges, 1)
	assert.Equal(t, uint64(300), doc.Edges[0].Seq, "and that entry now carries it")
}

// TestBuild_RemovalFloorsAreCappedAndDropTheStalest pins the bound end to end.
// The cap is a document-size requirement — a node's whole edge list lives in
// one KV document, and unbounded auxiliary state in it is how this index fails.
// Which floors go is the visible half, and it is the lowest sequences: the
// edges gone longest, which is the least arbitrary order available.
func TestBuild_RemovalFloorsAreCappedAndDropTheStalest(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)

	// One node, many links leaving it — the shape a bulk offboarding produces.
	srcID := nanoID(t, "cap")
	const overflow = 32
	total := adjacency.MaxRemovalFloors + overflow

	keys := make([]string, total)
	for i := range keys {
		keys[i] = substrate.LinkKey("identity", srcID, "holdsRole", "role", distinctNanoID(t, "d", i))
		require.NoError(t, adjacency.Build(ctx, adjKV, outboundEvent(keys[i], true, uint64(i+1))))
	}

	doc, _ := nodeDocument(t, adjKV, srcID)
	require.Len(t, doc.Removals, adjacency.MaxRemovalFloors,
		"the map is bounded whatever the removal traffic")

	want := map[string]uint64{}
	for i := overflow; i < total; i++ {
		want[keys[i]] = uint64(i + 1)
	}
	assert.Equal(t, want, doc.Removals, "the survivors are the highest sequences — the freshest floors")

	// A floor arriving already staler than everything held is the one the cap
	// sheds, so a late removal cannot evict a fresher floor by displacing it.
	lateKey := substrate.LinkKey("identity", srcID, "holdsRole", "role", nanoID(t, "zzz"))
	require.NoError(t, adjacency.Build(ctx, adjKV, outboundEvent(lateKey, true, 5)))

	doc, _ = nodeDocument(t, adjKV, srcID)
	assert.Equal(t, want, doc.Removals,
		"the stalest floor is dropped even when it is the one just recorded")
}

// TestBuild_DeclinedWritesAreCounted pins the only outward sign a decline
// leaves. The refusal writes nothing and returns nil — byte-identical to a
// healthy no-op — so without this counter an operator could not tell a quiet
// stack from one where a lagging writer's every event is being dropped. The
// precedent this guard is modelled on reports its declines to the caller
// (adapter.DeclinedByWatermark); this path has no verdict to carry one.
func TestBuild_DeclinedWritesAreCounted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	adjKV, _ := startKVs(t)
	link := newOrderingLink(t, "sdc", "ddc")

	before := adjacency.DeclinedWrites()

	require.NoError(t, adjacency.Build(ctx, adjKV, link.create(200)))
	assert.Equal(t, before, adjacency.DeclinedWrites(), "an applied write is not a decline")

	require.NoError(t, adjacency.Build(ctx, adjKV, link.remove(100)))
	require.NoError(t, adjacency.Build(ctx, adjKV, link.create(100)))
	assert.Equal(t, before+2, adjacency.DeclinedWrites(),
		"one count per refused write, on either arm")
}

// TestBuild_ConcurrentWritersKeepEachOthersFloors pins the property the floor's
// lifetime rests on: floors are carried inside the node document and re-read on
// EVERY CAS pass, so a writer that loses a revision race rebuilds its change on
// what the winner wrote instead of overwriting it.
//
// Reading the node's state once outside the retry loop would pass every
// sequential case in this file and lose floors here — which is the whole reason
// this vector exists. The assertion is deterministic (the converged state is
// the same however the interleaving falls); what varies run to run is only how
// many revision conflicts the writers actually hit, and one node under six
// concurrent writers hits plenty.
func TestBuild_ConcurrentWritersKeepEachOthersFloors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	adjKV, _ := startKVs(t)

	srcID := nanoID(t, "cnc")
	const writers = 6
	const perWriter = 25

	// Every event is built HERE, on the test goroutine: the id helpers assert
	// through testing.T, whose failure calls are only valid from the goroutine
	// running the test.
	type edgeCase struct {
		key              string
		createSeq, rmSeq uint64
	}
	work := make([][]edgeCase, writers)
	want := map[string]uint64{}
	for w := range work {
		work[w] = make([]edgeCase, perWriter)
		for i := range work[w] {
			n := w*perWriter + i
			dstID := distinctNanoID(t, "c", n)
			key := substrate.LinkKey("identity", srcID, "holdsRole", "role", dstID)
			// Disjoint, ascending sequence ranges per writer — a shared stream
			// hands no two messages the same position.
			work[w][i] = edgeCase{key: key, createSeq: uint64(2*n + 1), rmSeq: uint64(2*n + 2)}
			want[key] = uint64(2*n + 2)
		}
	}
	require.LessOrEqual(t, len(want), adjacency.MaxRemovalFloors,
		"this vector must stay under the cap, or eviction — not clobbering — would explain a short map")

	events := make([][]adjacency.CoreKVEvent, writers)
	for w := range work {
		for _, c := range work[w] {
			events[w] = append(events[w],
				outboundEvent(c.key, false, c.createSeq),
				outboundEvent(c.key, true, c.rmSeq))
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for w := range writers {
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
		require.NoError(t, err, "writer %d", w)
	}

	doc, _ := nodeDocument(t, adjKV, srcID)
	assert.Empty(t, doc.Edges, "every edge was created and then removed")
	assert.Equal(t, want, doc.Removals,
		"every writer's floors survive every other writer's CAS retries")
}
