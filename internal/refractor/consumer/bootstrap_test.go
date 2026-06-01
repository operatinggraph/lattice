package consumer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/substrate"
)

// startJS launches an in-memory NATS server with JetStream and returns a
// connected JetStream context and the underlying NATS connection.
func startJS(t *testing.T) (jetstream.JetStream, *nats.Conn) {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

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

	js, _ := startJS(t)

	// Create an empty Core KV bucket (underlying stream exists but has no messages).
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-empty"})
	require.NoError(t, err)

	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-empty"})
	require.NoError(t, err)

	b := consumer.NewBootstrapper(js, "core-boot-empty", adjKV)

	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
		// success — empty stream signals ready immediately
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready on empty stream")
	}
}

func TestBootstrapper_ReadyAfterProcessingMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-msgs"})
	require.NoError(t, err)

	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-msgs"})
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

	b := consumer.NewBootstrapper(js, "core-boot-msgs", adjKV)
	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready after messages")
	}

	// Both edges must appear in the adjacency index.
	edges, err := adjacency.Neighbors(ctx, adjKV, "nodeA")
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

	js, _ := startJS(t)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-link"})
	require.NoError(t, err)
	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-link"})
	require.NoError(t, err)

	// Seed one Contract #1 link envelope: identity → holdsRole → role.
	identityID := stableNanoIDForBootstrap("alice")
	roleID := stableNanoIDForBootstrap("editor")
	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)

	envelope := map[string]any{
		"key":           linkKey,
		"class":         "holdsRole",
		"isDeleted":     false,
		"sourceVertex": identityKey,
		"targetVertex":   roleKey,
		"localName":     "holdsRole",
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, body)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(js, "core-boot-link", adjKV)
	go func() { _ = b.Run(ctx) }()
	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with link envelope")
	}

	// Source-side: identityID → outbound `holdsRole` → roleID.
	srcEdges, err := adjacency.Neighbors(ctx, adjKV, identityID)
	require.NoError(t, err)
	require.Len(t, srcEdges, 1, "src adjacency must have one outbound edge")
	assert.Equal(t, "outbound", srcEdges[0].Direction)
	assert.Equal(t, "holdsRole", srcEdges[0].Name)
	assert.Equal(t, roleID, srcEdges[0].OtherNodeID)
	assert.Equal(t, "role", srcEdges[0].OtherType)
	assert.Equal(t, linkKey, srcEdges[0].EdgeID)

	// Dst-side: roleID → inbound `holdsRole` → identityID.
	dstEdges, err := adjacency.Neighbors(ctx, adjKV, roleID)
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

	js, _ := startJS(t)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-linktomb"})
	require.NoError(t, err)
	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-linktomb"})
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

	b := consumer.NewBootstrapper(js, "core-boot-linktomb", adjKV)
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
		srcEdges, _ := adjacency.Neighbors(ctx, adjKV, identityID)
		dstEdges, _ := adjacency.Neighbors(ctx, adjKV, roleID)
		if len(srcEdges) == 0 && len(dstEdges) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	srcEdges, _ := adjacency.Neighbors(ctx, adjKV, identityID)
	dstEdges, _ := adjacency.Neighbors(ctx, adjKV, roleID)
	t.Fatalf("link tombstone did not remove both adjacency entries: src=%v dst=%v", srcEdges, dstEdges)
}

func TestBootstrapper_SkipsNonEdgeEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)

	coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-boot-noedge"})
	require.NoError(t, err)

	adjKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-boot-noedge"})
	require.NoError(t, err)

	// Write a node-only entry (no NodeID in adjacency sense — empty nodeId field).
	data, err := json.Marshal(map[string]any{"someField": "value"})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, "node.n1", data)
	require.NoError(t, err)

	b := consumer.NewBootstrapper(js, "core-boot-noedge", adjKV)
	go func() { _ = b.Run(ctx) }()

	select {
	case <-b.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap Ready with non-edge entries")
	}
}
