package adjacency_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startKVs brings up an embedded NATS server with the two buckets this
// package spans: the adjacency index it writes, and the Core KV bucket an
// overflow-marked node's edges are read out of.
func startKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	for _, bucket := range []string{"adjacency-test", "core-kv-test"} {
		_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err)
	}

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(context.Background(), "adjacency-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(context.Background(), "core-kv-test")
	require.NoError(t, err)

	return adjKV, coreKV
}

func TestBuild_SingleEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv, coreKV := startKVs(t)

	evt := adjacency.CoreKVEvent{
		CoreKvKey:   "core.agreement-1",
		EdgeID:      "e1",
		Name:        "HAS_PARTY",
		Direction:   "outbound",
		NodeID:      "nodeA",
		OtherNodeID: "nodeB",
	}
	require.NoError(t, adjacency.Build(ctx, kv, evt))

	edges, _, err := adjacency.Neighbors(ctx, kv, coreKV, "nodeA")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "e1", edges[0].EdgeID)
	assert.Equal(t, "core.agreement-1", edges[0].CoreKvKey)
	assert.Equal(t, "HAS_PARTY", edges[0].Name)
	assert.Equal(t, "outbound", edges[0].Direction)
	assert.Equal(t, "nodeB", edges[0].OtherNodeID)
}

func TestBuild_TwoEdgesSameNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.agreement-1", EdgeID: "e1", Name: "HAS_PARTY",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB",
	}))
	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.agreement-1", EdgeID: "e2", Name: "HAS_CONTACT",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeC",
	}))

	edges, _, err := adjacency.Neighbors(ctx, kv, coreKV, "nodeA")
	require.NoError(t, err)
	assert.Len(t, edges, 2)

	ids := []string{edges[0].EdgeID, edges[1].EdgeID}
	assert.ElementsMatch(t, []string{"e1", "e2"}, ids)
}

func TestBuild_UpsertReplacesExistingEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.old", EdgeID: "e1", Name: "OLD_TYPE",
		Direction: "inbound", NodeID: "nodeA", OtherNodeID: "nodeB",
	}))
	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.new", EdgeID: "e1", Name: "NEW_TYPE",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeC",
	}))

	edges, _, err := adjacency.Neighbors(ctx, kv, coreKV, "nodeA")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "e1", edges[0].EdgeID)
	assert.Equal(t, "NEW_TYPE", edges[0].Name)
	assert.Equal(t, "outbound", edges[0].Direction)
	assert.Equal(t, "nodeC", edges[0].OtherNodeID)
}

func TestBuild_DeleteEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.agreement-1", EdgeID: "e1", Name: "HAS_PARTY",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB",
	}))
	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.agreement-1", EdgeID: "e2", Name: "HAS_CONTACT",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeC",
	}))

	// Delete e1
	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		EdgeID: "e1", NodeID: "nodeA", IsDeleted: true,
	}))

	edges, _, err := adjacency.Neighbors(ctx, kv, coreKV, "nodeA")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "e2", edges[0].EdgeID)
}

func TestBuild_DeleteNonexistentEdge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv, coreKV := startKVs(t)

	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.agreement-1", EdgeID: "e1", Name: "HAS_PARTY",
		Direction: "outbound", NodeID: "nodeA", OtherNodeID: "nodeB",
	}))

	// Delete an edge that doesn't exist — should not error
	require.NoError(t, adjacency.Build(ctx, kv, adjacency.CoreKVEvent{
		EdgeID: "nonexistent", NodeID: "nodeA", IsDeleted: true,
	}))

	// e1 must still be present
	edges, _, err := adjacency.Neighbors(ctx, kv, coreKV, "nodeA")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "e1", edges[0].EdgeID)
}
