package adjacency_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
)

func TestNeighbors_NodeWithNoEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	kv := startAdjKV(t)

	edges, err := adjacency.Neighbors(kv, "unknown-node")
	require.NoError(t, err)
	assert.NotNil(t, edges, "must return non-nil slice")
	assert.Empty(t, edges)
}

func TestNeighbors_NodeWithEdges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	kv := startAdjKV(t)

	require.NoError(t, adjacency.Build(kv, adjacency.CoreKVEvent{
		CoreKvKey: "core.x", EdgeID: "e1", Name: "REL",
		Direction: "outbound", NodeID: "nodeX", OtherNodeID: "nodeY",
	}))

	edges, err := adjacency.Neighbors(kv, "nodeX")
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "e1", edges[0].EdgeID)
}
