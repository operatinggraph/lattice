package adjacency

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
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
