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

	"github.com/operatinggraph/lattice/internal/gateway/credentialbinding"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newCredentialBindingsTestConn starts an embedded NATS+JetStream server and
// returns a wrapped Conn with the core-events stream created (the
// credential-bindings bucket is left to each test so the refuse-to-start
// case can omit it).
func newCredentialBindingsTestConn(t *testing.T) (*substrate.Conn, context.Context) {
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

func createCredentialBindingsBucket(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: credentialbinding.BucketName})
	require.NoError(t, err)
}

// publishIdentityEvent publishes a synthetic Event envelope (the same shape
// internal/processor/outbox publishes) directly to events.identity.<name> —
// exercising the materializer's consumer without spinning up the real
// Processor pipeline (identity-domain's claim_test.go proves ClaimIdentity
// emits this exact shape).
func publishIdentityEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, eventType string, payload map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"eventId":   "evt-" + eventType,
		"requestId": "req-" + eventType,
		"eventType": eventType,
		"domain":    "identity",
		"payload":   payload,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, "events."+eventType, body, nil))
}

func TestStartCredentialBindingsMaterializer_RefusesWhenBucketMissing(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)

	_, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	if err == nil {
		t.Fatal("StartCredentialBindingsMaterializer: want error when credential-bindings bucket is unprovisioned, got nil")
	}
}

func TestStartCredentialBindingsMaterializer_ColdStartDrainsPriorHistory(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	actorKey := "vtx.identity.PriorHistoryActr"
	identityKey := "vtx.identity.PriorHistoryIdnt"
	// Publish BEFORE the materializer attaches — proves the cold-start
	// catch-up drains history committed before this boot, not just events
	// that arrive live.
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	entry, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	if got, _ := doc["identityKey"].(string); got != identityKey {
		t.Fatalf("bound doc identityKey = %q, want %q", got, identityKey)
	}
}

func TestCredentialBindingsMaterializer_LiveClaim(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	actorKey := "vtx.identity.LiveFlowActorXY"
	identityKey := "vtx.identity.LiveFlowIdntXY"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})

	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "claimed binding never appeared")
}

// TestCredentialBindingsMaterializer_LiveRebound proves identity.rebound
// (packages/identity-hygiene/ddls.go's MergeIdentity, multi-credential-
// identity-linking-design.md §3.3) folds into the bucket exactly like
// identity.claimed.
func TestCredentialBindingsMaterializer_LiveRebound(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	actorKey := "vtx.identity.ReboundActorXYZ"
	primaryKey := "vtx.identity.ReboundPrimryXY"
	secondaryKey := "vtx.identity.ReboundSecndryX"
	publishIdentityEvent(t, ctx, conn, "identity.rebound", map[string]any{
		"actorKey": actorKey, "identityKey": primaryKey, "previousIdentityKey": secondaryKey,
	})

	require.Eventually(t, func() bool {
		entry, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		if err != nil {
			return false
		}
		var doc map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &doc))
		got, _ := doc["identityKey"].(string)
		return got == primaryKey
	}, 5*time.Second, 20*time.Millisecond, "rebound binding never appeared")
}

// TestCredentialBindingsMaterializer_ReboundAfterClaimConverges proves a
// rebound after a claim folds last and wins — a merge repointing a credential
// that was already bound via a prior claim must resolve to the merge's
// primary, not the original claim target (design §3.3, "A rebound after a
// claim folds last and wins").
func TestCredentialBindingsMaterializer_ReboundAfterClaimConverges(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	actorKey := "vtx.identity.ChainClaimActrX"
	secondaryKey := "vtx.identity.ChainClaimSecY"
	primaryKey := "vtx.identity.ChainClaimPriZ"

	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": secondaryKey, "actorKey": actorKey,
	})
	require.Eventually(t, func() bool {
		entry, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		if err != nil {
			return false
		}
		var doc map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &doc))
		got, _ := doc["identityKey"].(string)
		return got == secondaryKey
	}, 5*time.Second, 20*time.Millisecond, "claimed binding never appeared")

	publishIdentityEvent(t, ctx, conn, "identity.rebound", map[string]any{
		"actorKey": actorKey, "identityKey": primaryKey, "previousIdentityKey": secondaryKey,
	})
	require.Eventually(t, func() bool {
		entry, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		if err != nil {
			return false
		}
		var doc map[string]any
		require.NoError(t, json.Unmarshal(entry.Value, &doc))
		got, _ := doc["identityKey"].(string)
		return got == primaryKey
	}, 5*time.Second, 20*time.Millisecond, "rebound never converged over the prior claim")
}

// TestCredentialBindingsMaterializer_LiveUnbound proves identity.unbound
// (packages/identity-domain/ddls.go's UnlinkCredential, multi-credential-
// identity-linking-design.md §8) folds as the plane's one explicit
// bucket-key DELETE — the one row-set shrink never covered by
// overwrite-by-reprojection (Contract #11 §11.4).
func TestCredentialBindingsMaterializer_LiveUnbound(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	actorKey := "vtx.identity.UnboundActorXYZ"
	identityKey := "vtx.identity.UnboundIdentXYZ"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "claimed binding never appeared")

	publishIdentityEvent(t, ctx, conn, "identity.unbound", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		return errors.Is(err, substrate.ErrKeyNotFound)
	}, 5*time.Second, 20*time.Millisecond, "unbound binding was never deleted")
}

// TestCredentialBindingsMaterializer_UnboundRedeliveryIsIdempotent proves a
// redelivered identity.unbound targeting an already-deleted key is a no-op
// (KVDelete's ErrKeyNotFound guard, mirroring revocationMaterializer's own
// redelivered-delete idempotency), not a poison-pill pause — a subsequent
// unrelated claim must still fold normally, proving the consumer never got
// stuck.
func TestCredentialBindingsMaterializer_UnboundRedeliveryIsIdempotent(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	actorKey := "vtx.identity.RedelUnboundActor"
	identityKey := "vtx.identity.RedelUnboundIdent"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "claimed binding never appeared")

	publishIdentityEvent(t, ctx, conn, "identity.unbound", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
		return errors.Is(err, substrate.ErrKeyNotFound)
	}, 5*time.Second, 20*time.Millisecond, "unbound binding was never deleted")

	// Redeliver the same unbind (at-least-once semantics) against the
	// now-already-deleted key.
	publishIdentityEvent(t, ctx, conn, "identity.unbound", map[string]any{
		"identityKey": identityKey, "actorKey": actorKey,
	})

	// A subsequent, unrelated claim must still fold — proves the redelivered
	// unbind didn't wedge or pause the consumer.
	nextActor := "vtx.identity.AfterRedelActor"
	nextIdentity := "vtx.identity.AfterRedelIdent"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": nextIdentity, "actorKey": nextActor,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, nextActor)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "consumer stuck behind the redelivered unbind — next claim never folded")

	_, err = conn.KVGet(ctx, credentialbinding.BucketName, actorKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "redelivered unbind must not resurrect the deleted key")
}

func TestCredentialBindingsMaterializer_IgnoresSiblingEvent(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	// identity.provisioned carries no identityKey/actorKey binding pair —
	// FilterSubject scopes delivery to events.identity.>, so this sibling
	// event legitimately arrives here too and must be ignored, not written.
	provisionedActor := "vtx.identity.ProvOnlyActorX"
	publishIdentityEvent(t, ctx, conn, "identity.provisioned", map[string]any{
		"identityKey": provisionedActor,
	})

	// A subsequent claim must still fold normally — proves the ignored
	// sibling event didn't wedge the consumer.
	claimedActor := "vtx.identity.AfterSiblingAct"
	claimedIdentity := "vtx.identity.AfterSiblingIdt"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": claimedIdentity, "actorKey": claimedActor,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, claimedActor)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "consumer stuck behind the ignored sibling event")

	_, err = conn.KVGet(ctx, credentialbinding.BucketName, provisionedActor)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "identity.provisioned must never be written as a binding")
}

func TestCredentialBindingsMaterializer_PoisonKeyDroppedNotStuck(t *testing.T) {
	conn, ctx := newCredentialBindingsTestConn(t)
	createCredentialBindingsBucket(t, ctx, conn)

	hb := NewHeartbeater(conn, "health-kv", "gw-test", &Metrics{}, nil)
	sup, err := StartCredentialBindingsMaterializer(ctx, conn, hb, nil)
	require.NoError(t, err)
	t.Cleanup(sup.Stop)

	poisonActor := "vtx.identity.bad actor key"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": "vtx.identity.SomePoisonTargt", "actorKey": poisonActor,
	})

	require.Eventually(t, func() bool {
		issues := hb.issues.snapshot()
		for _, is := range issues {
			if is.Code == "credentialBindings.unputtableKey" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "poison-key Health issue never surfaced")

	keys, err := conn.KVListKeys(ctx, credentialbinding.BucketName)
	require.NoError(t, err)
	require.NotContains(t, keys, poisonActor, "poison key must never be written")

	validActor := "vtx.identity.NextValidActorXY"
	publishIdentityEvent(t, ctx, conn, "identity.claimed", map[string]any{
		"identityKey": "vtx.identity.NextValidIdentY", "actorKey": validActor,
	})
	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, credentialbinding.BucketName, validActor)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "consumer stuck behind the poison key — next valid event never folded")
}
