// DurableJanitor — internal test package so tests can call sweep directly
// rather than waiting out the production cadence (90s grace + 30min tick),
// and reuse registry_probe_internal_test.go's fixture helpers.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

const janitorTestKVConfig = `{"bucket":"contract_view","key":["contract_id"]}`

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// makeConsumer creates a durable consumer on the Core KV bucket's backing
// stream, the same stream cmd/refractor puts every per-lens durable on.
func makeConsumer(ctx context.Context, t *testing.T, conn *substrate.Conn, name string) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "KV_core-kv", jetstream.ConsumerConfig{
		Durable:   name,
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
}

func consumerExists(ctx context.Context, t *testing.T, conn *substrate.Conn, name string) bool {
	t.Helper()
	_, err := conn.JetStream().Consumer(ctx, "KV_core-kv", name)
	return err == nil
}

// TestDurableJanitor_DeletesOnlyOrphanedLensDurables is the load-bearing
// test: the sweep must delete a durable whose lens is gone and nothing else
// on a stream it shares with other owners.
func TestDurableJanitor_DeletesOnlyOrphanedLensDurables(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	const liveID = "AbCdEfGhJkMnPqRsTuVw"
	const tombstonedID = "BcDeFgHjKmNpQrStUvWx"
	const absentID = "CdEfGhJkMnPqRsTuVwXy"
	const bootstrapID = "RfxBootstrap12345678"
	const registeredButUndeclaredID = "DeFgHjKmNpQrStUvWxYz"

	putLens(ctx, t, conn, bucket, liveID, false, janitorTestKVConfig)
	putLens(ctx, t, conn, bucket, tombstonedID, true, janitorTestKVConfig)
	// absentID is deliberately never written: a lens whose vertex aged out of
	// KV entirely is the case the tombstone reap can never see.

	makeConsumer(ctx, t, conn, "refractor-"+liveID)
	makeConsumer(ctx, t, conn, "refractor-"+tombstonedID)
	makeConsumer(ctx, t, conn, "refractor-"+absentID)
	makeConsumer(ctx, t, conn, "refractor-"+bootstrapID)
	makeConsumer(ctx, t, conn, "refractor-"+registeredButUndeclaredID)
	makeConsumer(ctx, t, conn, "refractor-adjacency")
	makeConsumer(ctx, t, conn, "refractor-lens-source-rfx-abc123-CVmVsDREP1FQ8a46Epzz")
	makeConsumer(ctx, t, conn, "chronicler-defs-chronicler-abc")

	registered := func() []string { return []string{registeredButUndeclaredID} }
	j := NewDurableJanitor(conn, bucket, registered, []string{bootstrapID}, quietLogger())
	deleted := j.sweep(ctx)

	require.ElementsMatch(t, []string{"refractor-" + tombstonedID, "refractor-" + absentID}, deleted,
		"only the two durables whose lens vertex positively says it is gone may be deleted")

	require.True(t, consumerExists(ctx, t, conn, "refractor-"+liveID),
		"a live lens's durable must survive")
	require.True(t, consumerExists(ctx, t, conn, "refractor-"+bootstrapID),
		"a lens that runs with no Core KV declaration (the env-gated bootstrap lens) must survive")
	require.True(t, consumerExists(ctx, t, conn, "refractor-"+registeredButUndeclaredID),
		"a running pipeline's durable must survive even with no vertex to vouch for it")
	require.True(t, consumerExists(ctx, t, conn, "refractor-adjacency"),
		"the adjacency bootstrapper's consumer shares the prefix and must survive")
	require.True(t, consumerExists(ctx, t, conn, "refractor-lens-source-rfx-abc123-CVmVsDREP1FQ8a46Epzz"),
		"the lens source's per-boot durable shares the prefix and must survive")
	require.True(t, consumerExists(ctx, t, conn, "chronicler-defs-chronicler-abc"),
		"another component's consumer must survive")

	require.False(t, consumerExists(ctx, t, conn, "refractor-"+tombstonedID))
	require.False(t, consumerExists(ctx, t, conn, "refractor-"+absentID))
}

// TestDurableJanitor_UnparseableVertexKeepsDurable pins the fail-closed
// direction on the one read that licenses a deletion: only a positive "this
// lens is gone" may delete, and an envelope that cannot be read is not one.
func TestDurableJanitor_UnparseableVertexKeepsDurable(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	const id = "AbCdEfGhJkMnPqRsTuVw"

	_, err := conn.KVPut(ctx, bucket, "vtx.meta."+id, []byte("{not json"))
	require.NoError(t, err)
	makeConsumer(ctx, t, conn, "refractor-"+id)

	j := NewDurableJanitor(conn, bucket, func() []string { return nil }, nil, quietLogger())

	require.Empty(t, j.sweep(ctx), "an unreadable vertex must delete nothing")
	require.True(t, consumerExists(ctx, t, conn, "refractor-"+id))
}

// TestDurableJanitor_ClasslessVertexKeepsDurable guards a case the
// set-difference shape would get wrong: a lens whose root envelope carries no
// `class` is invisible to the class-filtered enumerations elsewhere in this
// package, but it is not a deleted lens — its pipeline is driven by the
// `.spec` aspect, which never consults the root's class at all.
func TestDurableJanitor_ClasslessVertexKeepsDurable(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	const id = "AbCdEfGhJkMnPqRsTuVw"

	body, err := json.Marshal(map[string]any{"id": id, "isDeleted": false})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, "vtx.meta."+id, body)
	require.NoError(t, err)
	makeConsumer(ctx, t, conn, "refractor-"+id)

	j := NewDurableJanitor(conn, bucket, func() []string { return nil }, nil, quietLogger())

	require.Empty(t, j.sweep(ctx))
	require.True(t, consumerExists(ctx, t, conn, "refractor-"+id))
}

// TestDurableJanitor_EventStreamLensDurableSurvives guards the widening this
// judgment makes over RegistryProbe's narrower declared set: a
// Chronicler-owned eventStream lens is skipped by the Refractor and so is
// absent from declaredLensIDs — but it is a live lens, and anything named
// after it must not be deleted.
func TestDurableJanitor_EventStreamLensDurableSurvives(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	const eventStreamID = "AbCdEfGhJkMnPqRsTuVw"

	putEventStreamLens(ctx, t, conn, bucket, eventStreamID)
	makeConsumer(ctx, t, conn, "refractor-"+eventStreamID)

	probe := NewRegistryProbe(conn, bucket, func() []string { return nil }, quietLogger())
	declared, err := probe.declaredLensIDs(ctx)
	require.NoError(t, err)
	require.NotContains(t, declared, eventStreamID, "premise: the probe's narrow set excludes it")

	j := NewDurableJanitor(conn, bucket, func() []string { return nil }, nil, quietLogger())
	require.Empty(t, j.sweep(ctx))
	require.True(t, consumerExists(ctx, t, conn, "refractor-"+eventStreamID))
}
