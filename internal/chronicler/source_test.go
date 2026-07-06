package chronicler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// hostHarness wires a real (embedded) NATS/JetStream server with core-kv +
// core-events streams and a Host watching vtx.meta.> for eventStream lens
// definitions.
func hostHarness(t *testing.T) (*Host, *substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "orchestration-history"})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-events", Subjects: []string{"events.>"},
	})
	require.NoError(t, err)

	host := NewHost(HostConfig{
		Conn:         conn,
		CoreKVBucket: "core-kv",
		EventsStream: "core-events",
		Instance:     "test",
	})
	return host, conn, ctx
}

// eventStreamLensSpec builds the exact `vtx.meta.<id>.spec` aspect body an
// installed eventStream lens carries — the same wire shape
// internal/refractor/lens.LensSpec (pre-extraction) and pkgmgr.LensSpec
// (install-time authoring) both mirror.
func eventStreamLensSpec(canonicalName, subject, bucket, keyColumn string) map[string]any {
	return map[string]any{
		"canonicalName": canonicalName,
		"targetType":    "nats_kv",
		"targetConfig": map[string]any{
			"bucket": bucket,
			"key":    []string{keyColumn},
		},
		"source": map[string]any{
			"kind":     "eventStream",
			"subjects": []string{subject},
			"project": map[string]any{
				"key": "payload.instanceId",
				"columns": map[string]any{
					keyColumn: "payload.instanceId",
					"status": map[string]any{
						"from": "eventType",
						"map":  map[string]string{"loom.patternStarted": "running", "loom.patternCompleted": "complete"},
					},
				},
			},
		},
	}
}

func putVertex(t *testing.T, conn *substrate.Conn, ctx context.Context, key, class string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"key": key, "class": class, "data": map[string]any{}})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, "core-kv", key, body)
	require.NoError(t, err)
}

func putSpecAspect(t *testing.T, conn *substrate.Conn, ctx context.Context, vertexKey string, spec map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"class": "spec", "data": spec})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, "core-kv", vertexKey+".spec", body)
	require.NoError(t, err)
}

// TestHost_DiscoversEventStreamLens_MaterializesRealEvent proves the whole
// new discovery path end to end: a `meta.lens` vertex + an eventStream spec
// aspect land in Core KV → Host starts a Manager → a real published
// core-events message converges a row in the declared NATS-KV bucket.
func TestHost_DiscoversEventStreamLens_MaterializesRealEvent(t *testing.T) {
	host, conn, ctx := hostHarness(t)
	require.NoError(t, host.Start(ctx))

	lensID := "AAdiscoverLensAAAAAA"
	vertexKey := "vtx.meta." + lensID
	putVertex(t, conn, ctx, vertexKey, "meta.lens")
	putSpecAspect(t, conn, ctx, vertexKey, eventStreamLensSpec("chroniclerTestLens", "events.loom.>", "orchestration-history", "instance_id"))

	require.Eventually(t, func() bool {
		count, _ := host.Active()
		return count == 1
	}, 5*time.Second, 20*time.Millisecond, "the eventStream definition must be discovered and started")

	ev, err := json.Marshal(Event{EventType: "loom.patternStarted", Payload: map[string]any{"instanceId": "inst-discover"}, Timestamp: "2026-07-06T00:00:00Z"})
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, "events.loom.patternStarted", ev, nil))

	kv, err := conn.OpenKV(ctx, "orchestration-history")
	require.NoError(t, err)
	reader, err := adapter.New(kv, []string{"instance_id"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, ok, err := reader.GetRow(ctx, map[string]any{"instance_id": "inst-discover"})
		return err == nil && ok && row["status"] == "running"
	}, 5*time.Second, 20*time.Millisecond, "the real event must materialize a row via the discovered definition")
}

// TestHost_CoreKvLens_IsIgnored proves Chronicler skips a `coreKv`-kind (or
// absent-source) lens silently — that stays Refractor's concern, unchanged
// by this extraction.
func TestHost_CoreKvLens_IsIgnored(t *testing.T) {
	host, conn, ctx := hostHarness(t)
	require.NoError(t, host.Start(ctx))

	lensID := "AAcoreKvLensAAAAAAAA"
	vertexKey := "vtx.meta." + lensID
	putVertex(t, conn, ctx, vertexKey, "meta.lens")
	putSpecAspect(t, conn, ctx, vertexKey, map[string]any{
		"canonicalName": "someCoreKvLens",
		"cypherRule":    "MATCH (i:identity) RETURN i.key AS key",
		"targetType":    "nats_kv",
		"targetConfig":  map[string]any{"bucket": "some-bucket", "key": []string{"key"}},
	})

	// Give the discovery loop time to process; it must NOT start a manager.
	time.Sleep(200 * time.Millisecond)
	count, _ := host.Active()
	require.Equal(t, 0, count, "a coreKv lens must never be picked up by Chronicler")
}

// TestHost_RemovingDefinition_StopsItsManager proves a vertex delete tears
// down the running Manager (mirrors Refractor's CoreKVSource removal path).
func TestHost_RemovingDefinition_StopsItsManager(t *testing.T) {
	host, conn, ctx := hostHarness(t)
	require.NoError(t, host.Start(ctx))

	lensID := "AAremoveLensAAAAAAAA"
	vertexKey := "vtx.meta." + lensID
	putVertex(t, conn, ctx, vertexKey, "meta.lens")
	putSpecAspect(t, conn, ctx, vertexKey, eventStreamLensSpec("chroniclerRemoveLens", "events.remove.>", "orchestration-history", "instance_id"))

	require.Eventually(t, func() bool {
		count, _ := host.Active()
		return count == 1
	}, 5*time.Second, 20*time.Millisecond)

	require.NoError(t, conn.KVDelete(ctx, "core-kv", vertexKey))

	require.Eventually(t, func() bool {
		count, _ := host.Active()
		return count == 0
	}, 5*time.Second, 20*time.Millisecond, "deleting the vertex must stop the running manager")
}

// TestHost_VertexReclassifiedAwayFromLens_StopsItsManager proves a vertex
// UPDATE (not delete) that changes its class away from meta.lens also tears
// down the running Manager — a reclassification is a genuine, if rare,
// lens-lifecycle operation, and leaving the old Manager running would keep it
// consuming its subject and writing its target bucket indefinitely after
// Core KV no longer considers the vertex a lens at all.
func TestHost_VertexReclassifiedAwayFromLens_StopsItsManager(t *testing.T) {
	host, conn, ctx := hostHarness(t)
	require.NoError(t, host.Start(ctx))

	lensID, err := substrate.NewNanoID()
	require.NoError(t, err)
	vertexKey := "vtx.meta." + lensID
	putVertex(t, conn, ctx, vertexKey, "meta.lens")
	putSpecAspect(t, conn, ctx, vertexKey, eventStreamLensSpec("chroniclerReclassifyLens", "events.reclassify.>", "orchestration-history", "instance_id"))

	require.Eventually(t, func() bool {
		count, _ := host.Active()
		return count == 1
	}, 5*time.Second, 20*time.Millisecond)

	// The vertex is updated (not deleted) to a different, non-lens class.
	putVertex(t, conn, ctx, vertexKey, "meta.ddl.aspectType")

	require.Eventually(t, func() bool {
		count, _ := host.Active()
		return count == 0
	}, 5*time.Second, 20*time.Millisecond, "reclassifying the vertex away from meta.lens must stop the running manager")
}
