package pipeline

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newTestKVs boots an embedded NATS server, creates each named JetStream KV
// bucket, and returns them opened in the order named.
func newTestKVs(t *testing.T, buckets ...string) []*substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	for _, bucket := range buckets {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err)
	}
	kvs := make([]*substrate.KV, len(buckets))
	for i, bucket := range buckets {
		kvs[i], err = conn.OpenKV(ctx, bucket)
		require.NoError(t, err)
	}
	return kvs
}
