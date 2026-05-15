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
	edges, err := adjacency.Neighbors(adjKV, "nodeA")
	require.NoError(t, err)
	assert.Len(t, edges, 2)

	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.EdgeID
	}
	assert.ElementsMatch(t, []string{"e1", "e2"}, ids)
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
