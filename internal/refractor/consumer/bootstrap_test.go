package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startJS launches an in-memory NATS server with JetStream and returns a
// connected JetStream context and the underlying NATS connection.
func startJS(t *testing.T) (jetstream.JetStream, *nats.Conn) {
	t.Helper()
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js, nc
}

func TestBootstrapper_ReadyOnEmptyStream(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	// Create an empty Core KV bucket (underlying stream exists but has no messages).
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-empty"})
	require.NoError(t, err)

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-empty"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-empty")
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-boot-empty", adjKV)

	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
		// success — empty stream signals ready immediately
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready on empty stream")
	}
}

// TestBootstrapper_ReadyAfterLargeBacklog guards the prefetch race: a non-empty
// backlog is prefetched into the consumer's client buffer (server NumPending → 0)
// well before the handler finishes building the adjacency index, so a
// NumPending-only "caught up" check would close Ready on a partial index. All
// edges target one node, so each adjacency.Build is a CAS on the same growing
// adj.<nodeId> entry — making the build slower than the prefetch and widening the
// window. With the ack-aware ConsumerCaughtUp check, Ready must not fire until
// every seeded edge is indexed.
func TestBootstrapper_ReadyAfterLargeBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-backlog"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-backlog"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-backlog")
	require.NoError(t, err)

	const backlog = 300
	for i := 0; i < backlog; i++ {
		evt := adjacency.CoreKVEvent{
			CoreKvKey:   fmt.Sprintf("core.e%d", i),
			EdgeID:      fmt.Sprintf("e%d", i),
			Name:        "HAS_PARTY",
			Direction:   "outbound",
			NodeID:      "nodeBig",
			OtherNodeID: fmt.Sprintf("other%d", i),
		}
		data, marshalErr := json.Marshal(evt)
		require.NoError(t, marshalErr)
		_, putErr := coreKV.Put(ctx, "edge."+evt.EdgeID, data)
		require.NoError(t, putErr)
	}

	b := consumer.NewBootstrapper(conn, "core-boot-backlog", adjKV)
	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready after large backlog")
	}

	// At the instant Ready fires, EVERY seeded edge must already be indexed —
	// Ready must not have been raised on a partially-built index. No node these
	// tests seed comes near the overflow threshold, so every read here is served
	// from the adjacency document and never needs a Core KV handle.
	edges, _, err := adjacency.Neighbors(ctx, adjKV, nil, "nodeBig")
	require.NoError(t, err)
	assert.Len(t, edges, backlog,
		"all %d edges must be indexed at the instant Ready fires (prefetch-race guard)", backlog)
}

func TestBootstrapper_ReadyAfterProcessingMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-msgs"})
	require.NoError(t, err)

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-msgs"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-msgs")
	require.NoError(t, err)

	// Write two edge events to Core KV before the bootstrapper starts.
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: "core.e1", EdgeID: "e1", Name: "HAS_PARTY", Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB"},
		{CoreKvKey: "core.e2", EdgeID: "e2", Name: "HAS_CONTACT", Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeC"},
	} {
		data, marshalErr := json.Marshal(evt)
		require.NoError(t, marshalErr)
		_, putErr := coreKV.Put(ctx, "edge."+evt.EdgeID, data)
		require.NoError(t, putErr)
	}

	b := consumer.NewBootstrapper(conn, "core-boot-msgs", adjKV)
	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready after messages")
	}

	// Both edges must appear in the adjacency index.
	edges, _, err := adjacency.Neighbors(ctx, adjKV, nil, "nodeA")
	require.NoError(t, err)
	assert.Len(t, edges, 2)

	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.EdgeID
	}
	assert.ElementsMatch(t, []string{"e1", "e2"}, ids)
}

// stableNanoID returns a deterministic 20-char Contract #1 NanoID. We
// use this so the link-bridge test produces real valid keys.
func stableNanoIDForBootstrap(seedStr string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("bootstrap-test:" + seedStr) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

// TestBootstrapper_LinkEnvelopeBridge — Story 3.2b §1: a Contract #1 link
// envelope (key `lnk.<srcType>.<srcId>.<linkName>.<dstType>.<dstId>`) must
// produce TWO directional adjacency entries (outbound from src, inbound
// from dst) when seen by the bootstrapper.
func TestBootstrapper_LinkEnvelopeBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-link"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-link"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-link")
	require.NoError(t, err)

	// Seed one Contract #1 link envelope: identity → holdsRole → role.
	identityID := stableNanoIDForBootstrap("alice")
	roleID := stableNanoIDForBootstrap("editor")
	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)

	envelope := map[string]any{
		"key":          linkKey,
		"class":        "holdsRole",
		"isDeleted":    false,
		"sourceVertex": identityKey,
		"targetVertex": roleKey,
		"localName":    "holdsRole",
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, body)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-boot-link", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with link envelope")
	}

	// Source-side: identityID → outbound `holdsRole` → roleID.
	srcEdges, _, err := adjacency.Neighbors(ctx, adjKV, nil, identityID)
	require.NoError(t, err)
	require.Len(t, srcEdges, 1, "src adjacency must have one outbound edge")
	assert.Equal(t, "outbound", srcEdges[0].Direction)
	assert.Equal(t, "holdsRole", srcEdges[0].Name)
	assert.Equal(t, roleID, srcEdges[0].OtherNodeID)
	assert.Equal(t, "role", srcEdges[0].OtherType)
	assert.Equal(t, linkKey, srcEdges[0].EdgeID)

	// Dst-side: roleID → inbound `holdsRole` → identityID.
	dstEdges, _, err := adjacency.Neighbors(ctx, adjKV, nil, roleID)
	require.NoError(t, err)
	require.Len(t, dstEdges, 1, "dst adjacency must have one inbound edge")
	assert.Equal(t, "inbound", dstEdges[0].Direction)
	assert.Equal(t, identityID, dstEdges[0].OtherNodeID)
	assert.Equal(t, "identity", dstEdges[0].OtherType)
}

// TestBootstrapper_LinkEnvelopeBridge_Tombstone — Story 3.2b §1: an
// `isDeleted: true` link envelope must REMOVE both directional adjacency
// entries when seen by the bootstrapper.
func TestBootstrapper_LinkEnvelopeBridge_Tombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-linktomb"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-linktomb"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-linktomb")
	require.NoError(t, err)

	identityID := stableNanoIDForBootstrap("bob")
	roleID := stableNanoIDForBootstrap("viewer")
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)

	// Pre-seed the live edge.
	live := map[string]any{
		"key":       linkKey,
		"class":     "holdsRole",
		"isDeleted": false,
	}
	body, err := json.Marshal(live)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, body)
	require.NoError(t, err)

	// Then write the tombstone (overwrite). Both messages arrive in order
	// via the durable consumer.
	tomb := map[string]any{
		"key":       linkKey,
		"class":     "holdsRole",
		"isDeleted": true,
	}
	body, err = json.Marshal(tomb)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, body)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-boot-linktomb", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with link tombstone")
	}

	// Both directional edges must be removed after the tombstone is
	// processed. KV consumer DeliverAllPolicy + per-subject ordering
	// guarantees the tombstone follows the live event.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srcEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, identityID)
		dstEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, roleID)
		if len(srcEdges) == 0 && len(dstEdges) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	srcEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, identityID)
	dstEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, roleID)
	t.Fatalf("link tombstone did not remove both adjacency entries: src=%v dst=%v", srcEdges, dstEdges)
}

// TestBootstrapper_LinkHardDeleteRetracts covers the SECOND retraction
// transport a link key carries: a NATS KV hard delete (KVDelete/Purge, an
// empty-bodied message) rather than an `isDeleted: true` envelope overwrite.
// internal/refractor/refractor_capability_linkfanout_e2e_test.go's (c') case
// exercises exactly this vector against the capability pipeline
// (coreKV.Delete on a grantedBy link key); this is the same vector against
// the dedicated adjacency Bootstrapper, which must classify the key as a
// link BEFORE its generic empty-body-is-a-tombstone check, or the hard
// delete never reaches the bridge that retracts it.
//
// The Bootstrapper starts BEFORE either write, and the live Put and the
// Delete are two separate LIVE deliveries to the running consumer — not two
// writes seeded ahead of a fresh consumer's startup. That distinction is
// load-bearing: this bucket's History defaults to 1 (one revision per
// subject), so a Put immediately followed by a Delete, both seeded before a
// consumer starts, compacts the live message out of the stream before a
// fresh DeliverAllPolicy consumer ever sees it — the assertion "no edges
// remain" would then hold whether or not the hard-delete classification
// fix works, because the edge was never indexed in the first place. Driving
// both writes through the already-running consumer instead proves the edge
// is genuinely present before the delete, and genuinely gone after it.
func TestBootstrapper_LinkHardDeleteRetracts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-linkharddel"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-linkharddel"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-linkharddel")
	require.NoError(t, err)

	identityID := stableNanoIDForBootstrap("carol")
	roleID := stableNanoIDForBootstrap("editor2")
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)

	b := consumer.NewBootstrapper(conn, "core-boot-linkharddel", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready on an empty stream")
	}

	live := map[string]any{"key": linkKey, "class": "holdsRole", "isDeleted": false}
	body, err := json.Marshal(live)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, body)
	require.NoError(t, err)

	bothEdgesPresent := func() bool {
		srcEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, identityID)
		dstEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, roleID)
		return len(srcEdges) == 1 && len(dstEdges) == 1
	}
	deadline := time.Now().Add(5 * time.Second)
	for !bothEdgesPresent() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, bothEdgesPresent(), "the live link must be indexed before the hard delete can prove anything")

	// The hard delete: an empty-bodied NATS KV tombstone, not a JSON
	// isDeleted:true overwrite.
	require.NoError(t, coreKV.Delete(ctx, linkKey))

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srcEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, identityID)
		dstEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, roleID)
		if len(srcEdges) == 0 && len(dstEdges) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	srcEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, identityID)
	dstEdges, _, _ := adjacency.Neighbors(ctx, adjKV, nil, roleID)
	t.Fatalf("a hard-deleted link key did not remove both adjacency entries: src=%v dst=%v", srcEdges, dstEdges)
}

func TestBootstrapper_SkipsNonEdgeEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-noedge"})
	require.NoError(t, err)

	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-noedge"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-noedge")
	require.NoError(t, err)

	// Write a node-only entry (no NodeID in adjacency sense — empty nodeId field).
	data, err := json.Marshal(map[string]any{"someField": "value"})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "node.n1", data)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-boot-noedge", adjKV)
	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with non-edge entries")
	}
}

// TestBootstrapper_OverflowLatchEndToEnd proves the whole Shape B mechanism
// through the real Bootstrapper — not adjacency.Build/Neighbors called
// directly, but the durable consumer's own link-envelope bridge driving a
// node past a (test-lowered) overflow threshold during its startup backlog
// drain. It covers every clause of §15.5's regression list: the mark and the
// emptied document after the latch, the fallback read serving every edge
// (including the one whose Build call was the latch itself), a retraction
// landing on the fallback after the latch, and an unmarked node's ordinary
// document path staying untouched by any of it.
func TestBootstrapper_OverflowLatchEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	defer adjacency.SetOverflowThresholds(8, 1<<20)()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-overflow"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-overflow"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-boot-overflow")
	require.NoError(t, err)
	coreKVHandle, err := conn.OpenKV(ctx, "core-boot-overflow")
	require.NoError(t, err)

	hub := stableNanoIDForBootstrap("hub")

	// Seed one more outbound link from hub than the lowered degree=8
	// threshold tolerates, all before the bootstrapper starts — its own
	// startup backlog drain is what crosses the latch.
	const linkCount = 9
	wantEdges := make([]string, linkCount)
	for i := 0; i < linkCount; i++ {
		partner := stableNanoIDForBootstrap(fmt.Sprintf("overflowPartner%d", i))
		linkKey := substrate.LinkKey("identity", hub, "hasFriend", "identity", partner)
		body, marshalErr := json.Marshal(map[string]any{"key": linkKey, "isDeleted": false})
		require.NoError(t, marshalErr)
		_, putErr := coreKV.Put(ctx, linkKey, body)
		require.NoError(t, putErr)
		wantEdges[i] = linkKey
	}

	// A second, unrelated node whose degree never approaches the threshold:
	// the control case proving the latch existing elsewhere changes nothing
	// about how an ordinary node is stored.
	loneID := stableNanoIDForBootstrap("overflowLone")
	lonePartner := stableNanoIDForBootstrap("overflowLonePartner")
	loneLinkKey := substrate.LinkKey("identity", loneID, "hasFriend", "identity", lonePartner)
	loneBody, err := json.Marshal(map[string]any{"key": loneLinkKey, "isDeleted": false})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, loneLinkKey, loneBody)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-boot-overflow", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with an overflowing hub")
	}

	// The mark exists, and the document the latch emptied is a live,
	// zero-edge document — not deleted.
	_, err = adjKV.Get(ctx, subjects.AdjMarkKey(hub))
	require.NoError(t, err, "the hub must be overflow-marked")

	docEntry, err := adjKV.Get(ctx, subjects.AdjKey(hub))
	require.NoError(t, err, "the latch empties the document, it does not delete it")
	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(docEntry.Value, &doc))
	assert.Empty(t, doc.Edges, "a latched node's document must hold no edges")

	// Neighbors serves every edge — all nine, including the one whose Build
	// call tripped the latch and was therefore never itself indexed into the
	// document — via the Core KV fallback.
	edges, _, err := adjacency.Neighbors(ctx, adjKV, coreKVHandle, hub)
	require.NoError(t, err)
	gotEdges := make([]string, len(edges))
	for i, e := range edges {
		gotEdges[i] = e.EdgeID
	}
	assert.ElementsMatch(t, wantEdges, gotEdges,
		"the fallback read must serve every edge the document stopped tracking")

	// Soft-tombstoning one link removes it from the fallback read. No wait
	// is needed: a marked node's write path is a no-op (Build never absorbs
	// the retraction into the document), so Core KV is the retraction's only
	// transport, and neighborsFromCoreKV reads it live at call time.
	tombKey := wantEdges[0]
	tombBody, err := json.Marshal(map[string]any{"key": tombKey, "isDeleted": true})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, tombKey, tombBody)
	require.NoError(t, err)

	edgesAfter, _, err := adjacency.Neighbors(ctx, adjKV, coreKVHandle, hub)
	require.NoError(t, err)
	require.Len(t, edgesAfter, linkCount-1)
	gotAfter := make([]string, len(edgesAfter))
	for i, e := range edgesAfter {
		gotAfter[i] = e.EdgeID
	}
	assert.NotContains(t, gotAfter, tombKey, "a soft-tombstoned link must leave the fallback read")

	// The unmarked node carries no mark and keeps its ordinary document.
	_, err = adjKV.Get(ctx, subjects.AdjMarkKey(loneID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "an unmarked node must carry no overflow mark")

	loneEdges, loneRev, err := adjacency.Neighbors(ctx, adjKV, coreKVHandle, loneID)
	require.NoError(t, err)
	require.Len(t, loneEdges, 1)
	assert.Equal(t, loneLinkKey, loneEdges[0].EdgeID)
	assert.NotZero(t, loneRev, "an unmarked node's fingerprint is its document revision")
}
