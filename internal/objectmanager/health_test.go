package objectmanager

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/healthkv"
)

// emitHeartbeat writes with a TTL derived from heartbeatEvery ×
// healthkv.DefaultTTLMultiplier (Contract #5 §5.6) so a crashed instance's key
// self-expires instead of orphaning forever. Real NATS expiry mechanics are
// proven once at the substrate layer and by the Processor heartbeater's
// end-to-end TTL test; this proves the write succeeds against a TTL-enabled
// bucket and pins the derived value.
func TestEmitHeartbeat_WritesWithDerivedTTL(t *testing.T) {
	if got, want := heartbeatEvery*healthkv.DefaultTTLMultiplier, 100*time.Second; got != want {
		t.Fatalf("derived heartbeat TTL = %v, want %v", got, want)
	}

	conn, ctx := testConn(t)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second, // enables AllowMsgTTL so KVPutWithTTL works in tests
	}); err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}

	m := New(Config{
		Conn:           conn,
		CoreKVBucket:   "core-kv",
		ObjectsBucket:  "core-objects",
		EventsStream:   "core-events",
		ReconcileGrace: time.Hour,
		HealthKVBucket: "health-kv",
		Instance:       "objmgr-ttl-test",
	})
	m.emitHeartbeat(ctx)

	if _, err := conn.KVGet(ctx, "health-kv", "health.object-store-manager.objmgr-ttl-test"); err != nil {
		t.Fatalf("heartbeat key missing right after emit: %v", err)
	}
}
