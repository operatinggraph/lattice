package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
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

// TestRevocationWriteFailed_InvalidKeyTerminatesAndReportsIssue proves the
// poison-pill fix: a KVPut/KVDelete failure classified as an invalid-key
// error must Term (never redeliver) and surface a Health issue, instead of
// Nak-ing forever (the original bug — no MaxDeliver on this consumer, so an
// unputtable key would have stalled kill-switch sync indefinitely).
func TestRevocationWriteFailed_InvalidKeyTerminatesAndReportsIssue(t *testing.T) {
	hb := NewHeartbeater(nil, "health-kv", "gw-test", &Metrics{}, nil)

	decision, err := revocationWriteFailed(hb, slog.Default(), "revoke", "vtx.identity.bad key!", fmt.Errorf("kv put: %w", jetstream.ErrInvalidKey))
	require.Equal(t, substrate.Term, decision)
	require.Error(t, err)

	issues := hb.issues.snapshot()
	require.Len(t, issues, 1)
	require.Equal(t, "revocation.unputtableKey", issues[0].Code)
	require.Equal(t, severityError, issues[0].Severity)
}

// TestRevocationWriteFailed_TransientErrorRetries proves the fix is scoped:
// an ordinary transient failure (e.g. the server briefly unreachable) still
// Naks for at-least-once redelivery — only a genuinely unwritable key is
// terminated.
func TestRevocationWriteFailed_TransientErrorRetries(t *testing.T) {
	hb := NewHeartbeater(nil, "health-kv", "gw-test", &Metrics{}, nil)

	decision, err := revocationWriteFailed(hb, slog.Default(), "revoke", "vtx.identity.SomeValidActorNPQRSTU", errors.New("kv put: deadline exceeded"))
	require.Equal(t, substrate.Nak, decision)
	require.Error(t, err)
	require.Empty(t, hb.issues.snapshot())
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

// TestRevocationMaterializer_PoisonKeyDroppedNotStuck proves the poison-pill
// fix through the real materializer + a real embedded NATS server (not just
// the revocationWriteFailed unit test): an actor key NATS-KV genuinely
// refuses (a space, outside its `^[-/_=.a-zA-Z0-9]+$` key charset) must be
// dropped — never written, and critically never blocking the consumer from
// processing the next, valid event. Before this fix the same failure Nak'd
// forever with no MaxDeliver, permanently stalling kill-switch sync.
func TestRevocationMaterializer_PoisonKeyDroppedNotStuck(t *testing.T) {
	conn, ctx := newRevocationTestConn(t)
	createRevocationBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartRevocationMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	poisonActor := "vtx.identity.bad actor key"
	publishRevocationEvent(t, ctx, conn, "gateway.actorRevoked", map[string]any{
		"actor": poisonActor, "at": "2026-07-03T03:00:00Z", "by": "vtx.identity.operator",
	})

	require.Eventually(t, func() bool {
		issues := hb.issues.snapshot()
		for _, is := range issues {
			if is.Code == "revocation.unputtableKey" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "poison-key Health issue never surfaced")

	// The poison key must never have been written (Term drops it before any
	// retry could succeed against a still-refusing key). KVGet on an
	// invalid-charset key itself errors (not ErrKeyNotFound — NATS rejects the
	// lookup, not just the value), so absence is checked via the bucket's key
	// listing instead.
	keys, err := conn.KVListKeys(ctx, revocation.BucketName)
	require.NoError(t, err)
	require.NotContains(t, keys, poisonActor, "poison key must never be written")

	// The consumer must still be live and processing — a subsequent, valid
	// event folds normally rather than the pump being stuck behind the
	// poisoned one.
	validActor := "vtx.identity.NextValidActorNPQRSTU"
	publishRevocationEvent(t, ctx, conn, "gateway.actorRevoked", map[string]any{
		"actor": validActor, "at": "2026-07-03T03:00:01Z", "by": "vtx.identity.operator",
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, revocation.BucketName, validActor)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "consumer stuck behind the poison key — next valid event never folded")
}
