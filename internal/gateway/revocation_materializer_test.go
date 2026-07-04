package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/gateway/revocation"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
)

// newRevocationTestConn starts an embedded NATS+JetStream server and returns a
// wrapped Conn with the core-events stream created (the token-revocation
// bucket is left to each test so the refuse-to-start case can omit it).
func newRevocationTestConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-events",
		Subjects: []string{"events.>"},
	})
	require.NoError(t, err)
	return conn, ctx
}

func createRevocationBucket(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: revocation.BucketName})
	require.NoError(t, err)
}

// publishRevocationEvent publishes a synthetic Event envelope (the same shape
// internal/processor/outbox publishes) directly to events.gateway.<name> —
// exercising the materializer's consumer without spinning up the real
// Processor pipeline (identity-domain's revocation_test.go proves the op
// emits this exact shape).
func publishRevocationEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, eventType string, payload map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"eventId":   "evt-" + eventType,
		"requestId": "req-" + eventType,
		"eventType": eventType,
		"domain":    "gateway",
		"payload":   payload,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, "events."+eventType, body, nil))
}

func TestStartRevocationMaterializer_RefusesWhenBucketMissing(t *testing.T) {
	conn, ctx := newRevocationTestConn(t)
	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)

	_, err := StartRevocationMaterializer(ctx, conn, hb, nil)
	if err == nil {
		t.Fatal("StartRevocationMaterializer: want error when token-revocation bucket is unprovisioned, got nil")
	}
}

func TestStartRevocationMaterializer_ColdStartDrainsPriorHistory(t *testing.T) {
	conn, ctx := newRevocationTestConn(t)
	createRevocationBucket(t, ctx, conn)

	targetActor := "vtx.identity.PriorHistoryActor"
	// Publish BEFORE the materializer attaches — proves the cold-start
	// catch-up (design §2.3) drains history committed before this boot, not
	// just events that arrive live.
	publishRevocationEvent(t, ctx, conn, "gateway.actorRevoked", map[string]any{
		"actor": targetActor, "at": "2026-07-03T00:00:00Z", "by": "vtx.identity.operator", "reason": "cold-start-proof",
	})

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartRevocationMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	entry, err := conn.KVGet(ctx, revocation.BucketName, targetActor)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	if got, _ := doc["by"].(string); got != "vtx.identity.operator" {
		t.Fatalf("revoked doc by = %q, want vtx.identity.operator", got)
	}
}

func TestRevocationMaterializer_LiveRevokeThenUnrevoke(t *testing.T) {
	conn, ctx := newRevocationTestConn(t)
	createRevocationBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartRevocationMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	targetActor := "vtx.identity.LiveFlowActor"
	publishRevocationEvent(t, ctx, conn, "gateway.actorRevoked", map[string]any{
		"actor": targetActor, "at": "2026-07-03T01:00:00Z", "by": "vtx.identity.operator",
	})

	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, revocation.BucketName, targetActor)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "revoked key never appeared")

	// §2.6: a successful fold must surface in the heartbeat's revocation
	// state as a non-zero synced sequence.
	require.Eventually(t, func() bool {
		return hb.revocationLastSeq.Load() > 0
	}, 5*time.Second, 20*time.Millisecond, "heartbeat revocation state never reflected the live revoke")

	publishRevocationEvent(t, ctx, conn, "gateway.actorUnrevoked", map[string]any{
		"actor": targetActor, "at": "2026-07-03T02:00:00Z", "by": "vtx.identity.operator",
	})

	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, revocation.BucketName, targetActor)
		return errors.Is(err, substrate.ErrKeyNotFound)
	}, 5*time.Second, 20*time.Millisecond, "unrevoked key never cleared")
}
