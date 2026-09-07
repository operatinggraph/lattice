package adjacency

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestLatch_ToleratesAnAlreadyCreatedMark pins the create-tolerating-conflict
// half of the latch's idempotency, which no Build-level test can reach
// deterministically: two writers that both read a node as unmarked and both
// decide to latch it race on the mark's creation, and the loser must treat the
// winner's mark as success. Calling latch twice reproduces exactly that
// losing writer's view — a create-or-fail here would surface as an error the
// consumer translates into an endless redelivery of the same event.
func TestLatch_ToleratesAnAlreadyCreatedMark(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv := startLatchKV(t)

	const nodeID = "nodeLatch"
	require.NoError(t, latch(ctx, kv, nodeID, 10, 1000))
	require.NoError(t, latch(ctx, kv, nodeID, 11, 1100),
		"a second writer arriving after the mark exists must not fail")

	_, err := kv.Get(ctx, subjects.AdjMarkKey(nodeID))
	require.NoError(t, err)
}

func startLatchKV(t *testing.T) *substrate.KV {
	t.Helper()
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "adjacency-latch-test"})
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	kv, err := conn.OpenKV(context.Background(), "adjacency-latch-test")
	require.NoError(t, err)

	return kv
}

// TestLatch_ClearsTheRemovalFloorsWithTheEdges pins the half of the latch's
// replacement document that is otherwise only incidental: the body it writes
// zero-values Removals as well as Edges, so a latched node keeps no ordering
// floors either.
//
// It matters because the two halves are kept for opposite reasons and a future
// edit could easily preserve one. A marked node's reads enumerate Core KV,
// which is authoritative and needs no floor, so a floor left behind would be
// state nothing consults — and, since the mark is never lifted, state nothing
// ever will.
func TestLatch_ClearsTheRemovalFloorsWithTheEdges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	kv := startLatchKV(t)

	const nodeID = "nodeFloorLatch"
	seeded, err := json.Marshal(AdjValue{
		Edges:    []EdgeEntry{{CoreKvKey: "lnk.a.b.c.d.e", EdgeID: "lnk.a.b.c.d.e", Seq: 7}},
		Removals: map[string]uint64{"lnk.gone.1": 3, "lnk.gone.2": 5},
	})
	require.NoError(t, err)
	_, err = kv.Put(ctx, subjects.AdjKey(nodeID), seeded)
	require.NoError(t, err)

	require.NoError(t, latch(ctx, kv, nodeID, 1, len(seeded)))

	entry, err := kv.Get(ctx, subjects.AdjKey(nodeID))
	require.NoError(t, err)
	var doc AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	assert.Empty(t, doc.Edges, "the latch gives up the edge list")
	assert.Empty(t, doc.Removals, "and the floors that guarded the edges it no longer holds")
}
