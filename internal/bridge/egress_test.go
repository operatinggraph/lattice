package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

// --- shared harness -----------------------------------------------------

func egressTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// testIdentityID mints a real NanoID — every identity key in these tests
// must be a well-formed vtx.identity.<NanoID> (ParseAspectKey validates the
// id segment against the fixed-length NanoID alphabet, so an arbitrary short
// string like "abc123" fails as malformed, not merely "not found").
func testIdentityID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	return id
}

func provisionEnvelopeBucket(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: envelopeLensBucket}); err != nil {
		t.Fatalf("provision %s: %v", envelopeLensBucket, err)
	}
}

// startTestVault wires a real LocalBackend + its NATS decrypt responder on
// conn, returning the backend so a test can call CreateIdentityKey/Encrypt/
// ShredKey directly against the SAME instance the RPC responder serves.
func startTestVault(t *testing.T, ctx context.Context, conn *substrate.Conn) *vault.LocalBackend {
	t.Helper()
	v, err := vault.NewLocalBackend([]byte("01234567890123456789012345678901"[:32]), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	svc := vault.NewService(v, slog.Default())
	if err := svc.StartNATSListener(ctx, conn.NATS()); err != nil {
		t.Fatalf("StartNATSListener: %v", err)
	}
	return v
}

// seedSensitiveAspect mints an identity key, encrypts plaintext under it,
// writes the (real, non-projected) envelope into the envelope-lens bucket,
// and returns the aspect's ref key + the $sensitiveRef-shaped ciphertext —
// exactly the marker shape orchestration-base's resolve_subject_params
// produces (design §3.2/§3.3).
func seedSensitiveAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, v *vault.LocalBackend, identityID, aspect string, plaintext map[string]any) (ref string, marker sensitiveRefMarker) {
	t.Helper()
	identityKey := "vtx.identity." + identityID
	ref = identityKey + "." + aspect

	env, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if _, err := conn.KVPut(ctx, envelopeLensBucket, identityKey, envBytes); err != nil {
		t.Fatalf("seed envelope: %v", err)
	}

	pt, err := json.Marshal(plaintext)
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	ct, err := v.Encrypt(ctx, identityKey, env, pt)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return ref, sensitiveRefMarker{Ref: ref, Ciphertext: ct}
}

func sensitiveRefParam(t *testing.T, marker sensitiveRefMarker, field string) json.RawMessage {
	t.Helper()
	marker.Field = field
	inner, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	out, err := json.Marshal(map[string]json.RawMessage{"$sensitiveRef": inner})
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	return out
}

// --- unwrapEgressParams / resolveSensitiveRef -----------------------------

func TestUnwrapEgressParams_HappyPath(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	provisionEnvelopeBucket(t, ctx, conn)
	v := startTestVault(t, ctx, conn)
	e := &Engine{conn: conn, logger: slog.Default()}

	_, marker := seedSensitiveAspect(t, ctx, conn, v, testIdentityID(t), "name", map[string]any{"value": "Alice Smith"})
	raw, _ := json.Marshal(map[string]json.RawMessage{
		"name":   sensitiveRefParam(t, marker, "value"),
		"family": json.RawMessage(`"backgroundCheck"`),
	})

	out, ferr := e.unwrapEgressParams(ctx, raw, 1)
	if ferr != nil {
		t.Fatalf("unwrapEgressParams: %v", ferr)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["name"] != "Alice Smith" {
		t.Errorf("name = %q, want plaintext %q", got["name"], "Alice Smith")
	}
	if got["family"] != "backgroundCheck" {
		t.Errorf("family = %q, want passthrough %q", got["family"], "backgroundCheck")
	}
}

func TestUnwrapEgressParams_NonMarkerPassthrough(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	e := &Engine{conn: conn, logger: slog.Default()}

	raw := json.RawMessage(`{"family":"backgroundCheck","count":3,"nested":{"a":1}}`)
	out, ferr := e.unwrapEgressParams(ctx, raw, 1)
	if ferr != nil {
		t.Fatalf("unwrapEgressParams: %v", ferr)
	}
	if string(out) != string(raw) {
		t.Errorf("non-marker params must pass through byte-identical: got %s, want %s", out, raw)
	}
}

func TestResolveSensitiveRef_Permanent(t *testing.T) {
	ctx := context.Background()

	t.Run("malformed ref", func(t *testing.T) {
		conn := egressTestConn(t)
		e := &Engine{conn: conn, logger: slog.Default()}
		_, ferr := e.resolveSensitiveRef(ctx, sensitiveRefMarker{Ref: "not-a-key", Field: "value"}, 1)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("malformed ref: want permanent failure, got %v", ferr)
		}
	})

	t.Run("non-identity ref", func(t *testing.T) {
		conn := egressTestConn(t)
		e := &Engine{conn: conn, logger: slog.Default()}
		_, ferr := e.resolveSensitiveRef(ctx, sensitiveRefMarker{Ref: "vtx.leaseapp.abc.status", Field: "value"}, 1)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("non-identity ref: want permanent failure, got %v", ferr)
		}
	})

	t.Run("no field", func(t *testing.T) {
		conn := egressTestConn(t)
		e := &Engine{conn: conn, logger: slog.Default()}
		_, ferr := e.resolveSensitiveRef(ctx, sensitiveRefMarker{Ref: "vtx.identity.abc.ssn"}, 1)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("empty field: want permanent failure, got %v", ferr)
		}
	})

	t.Run("shredded identity", func(t *testing.T) {
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		v := startTestVault(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		id := testIdentityID(t)
		_, marker := seedSensitiveAspect(t, ctx, conn, v, id, "ssn", map[string]any{"value": "123-45-6789"})
		if err := v.ShredKey(ctx, "vtx.identity."+id); err != nil {
			t.Fatalf("ShredKey: %v", err)
		}
		marker.Field = "value"
		_, ferr := e.resolveSensitiveRef(ctx, marker, 1)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("shredded identity: want permanent failure, got %v", ferr)
		}
	})

	t.Run("absent field", func(t *testing.T) {
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		v := startTestVault(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		_, marker := seedSensitiveAspect(t, ctx, conn, v, testIdentityID(t), "ssn", map[string]any{"value": "123-45-6789"})
		marker.Field = "notAField"
		_, ferr := e.resolveSensitiveRef(ctx, marker, 1)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("absent field: want permanent failure, got %v", ferr)
		}
	})

	t.Run("absent envelope row after attempts exhausted", func(t *testing.T) {
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		id := testIdentityID(t)
		marker := sensitiveRefMarker{Ref: "vtx.identity." + id + ".ssn", Field: "value",
			Ciphertext: vault.Ciphertext{CT: []byte("x"), Nonce: []byte("y"), KeyID: "vtx.identity." + id}}
		_, ferr := e.resolveSensitiveRef(ctx, marker, maxEgressUnwrapAttempts)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("absent envelope row past the retry budget: want permanent failure, got %v", ferr)
		}
	})

	t.Run("unparseable envelope row after attempts exhausted", func(t *testing.T) {
		// A non-ErrKeyNotFound fetchLiveEnvelope failure (a corrupt row, a
		// persistent bucket error) must ALSO escalate past the retry budget —
		// not just the absent-row arm above. An unconditional transient return
		// for this class would Nak forever (adversarial review finding, this
		// fire: FR29 "converge, never park" violated).
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		id := testIdentityID(t)
		identityKey := "vtx.identity." + id
		if _, err := conn.KVPut(ctx, envelopeLensBucket, identityKey, []byte("not json")); err != nil {
			t.Fatalf("seed unparseable envelope: %v", err)
		}
		marker := sensitiveRefMarker{Ref: identityKey + ".ssn", Field: "value",
			Ciphertext: vault.Ciphertext{CT: []byte("x"), Nonce: []byte("y"), KeyID: identityKey}}
		_, ferr := e.resolveSensitiveRef(ctx, marker, maxEgressUnwrapAttempts)
		if ferr == nil || ferr.class != egressPermanent {
			t.Fatalf("unparseable envelope row past the retry budget: want permanent failure, got %v", ferr)
		}
	})
}

func TestResolveSensitiveRef_Transient(t *testing.T) {
	ctx := context.Background()

	t.Run("envelope row not yet projected", func(t *testing.T) {
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		id := testIdentityID(t)
		marker := sensitiveRefMarker{Ref: "vtx.identity." + id + ".ssn", Field: "value",
			Ciphertext: vault.Ciphertext{CT: []byte("x"), Nonce: []byte("y"), KeyID: "vtx.identity." + id}}
		_, ferr := e.resolveSensitiveRef(ctx, marker, 1)
		if ferr == nil || ferr.class != egressTransient {
			t.Fatalf("envelope not yet projected (attempt 1 of %d): want transient failure, got %v", maxEgressUnwrapAttempts, ferr)
		}
	})

	t.Run("unparseable envelope row below the retry budget", func(t *testing.T) {
		conn := egressTestConn(t)
		provisionEnvelopeBucket(t, ctx, conn)
		e := &Engine{conn: conn, logger: slog.Default()}

		id := testIdentityID(t)
		identityKey := "vtx.identity." + id
		if _, err := conn.KVPut(ctx, envelopeLensBucket, identityKey, []byte("not json")); err != nil {
			t.Fatalf("seed unparseable envelope: %v", err)
		}
		marker := sensitiveRefMarker{Ref: identityKey + ".ssn", Field: "value",
			Ciphertext: vault.Ciphertext{CT: []byte("x"), Nonce: []byte("y"), KeyID: identityKey}}
		_, ferr := e.resolveSensitiveRef(ctx, marker, 1)
		if ferr == nil || ferr.class != egressTransient {
			t.Fatalf("unparseable envelope (attempt 1 of %d): want transient failure, got %v", maxEgressUnwrapAttempts, ferr)
		}
	})
}

func TestDetectSensitiveRef(t *testing.T) {
	if _, ok, ferr := detectSensitiveRef(json.RawMessage(`"backgroundCheck"`)); ok || ferr != nil {
		t.Errorf("plain string: want not-a-marker, got ok=%v ferr=%v", ok, ferr)
	}
	if _, ok, ferr := detectSensitiveRef(json.RawMessage(`42`)); ok || ferr != nil {
		t.Errorf("number: want not-a-marker, got ok=%v ferr=%v", ok, ferr)
	}
	if _, ok, ferr := detectSensitiveRef(json.RawMessage(`{"family":"x"}`)); ok || ferr != nil {
		t.Errorf("ordinary object: want not-a-marker, got ok=%v ferr=%v", ok, ferr)
	}
	_, ok, ferr := detectSensitiveRef(json.RawMessage(`{"$sensitiveRef":{"ref":123}}`))
	if ok || ferr == nil {
		t.Errorf("malformed inner marker: want a permanent parse failure, got ok=%v ferr=%v", ok, ferr)
	}
	marker, ok, ferr := detectSensitiveRef(json.RawMessage(`{"$sensitiveRef":{"ref":"vtx.identity.x.ssn","field":"value","ciphertext":{"ct":"YQ==","nonce":"Yg==","keyId":"k"}}}`))
	if !ok || ferr != nil {
		t.Fatalf("well-formed marker: want ok=true, got ok=%v ferr=%v", ok, ferr)
	}
	if marker.Ref != "vtx.identity.x.ssn" || marker.Field != "value" {
		t.Errorf("marker = %+v, want ref/field populated", marker)
	}
}

// --- handleExternal integration: the permanent/transient Decision + the
// terminal failed replyOp path ------------------------------------------

const egressHandlerLane = "system"

func provisionOpsAndCoreKV(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"}); err != nil {
		t.Fatalf("provision core-kv: %v", err)
	}
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "health-kv"}); err != nil {
		t.Fatalf("provision health-kv: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "core-operations", Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("provision core-operations: %v", err)
	}
}

func newTestEngineWithVault(t *testing.T, ctx context.Context, conn *substrate.Conn) *Engine {
	t.Helper()
	provisionOpsAndCoreKV(t, ctx, conn)
	provisionEnvelopeBucket(t, ctx, conn)
	off := false
	e := NewEngine(conn, Config{
		HealthKVBucket:   "health-kv",
		ActorKey:         "vtx.identity.bridgeTestActor",
		Lane:             egressHandlerLane,
		SkipOnRedelivery: &off,
		Logger:           slog.Default(),
	})
	return e
}

// awaitReplyOp reads the core-operations stream's DURABLE history (an
// ephemeral DeliverAll consumer, mirroring bridge_e2e_test's fakeProcessor)
// for the first opEnvelope whose operationType matches replyOp. It is created
// AFTER handleExternal already published — a plain core NATS subscription
// would miss a message published before it existed; the JetStream stream
// retains it regardless of ordering.
func awaitReplyOp(t *testing.T, ctx context.Context, conn *substrate.Conn, replyOp string, timeout time.Duration) opEnvelope {
	t.Helper()
	cons, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "core-operations", jetstream.ConsumerConfig{
		FilterSubject: "ops." + egressHandlerLane,
		AckPolicy:     jetstream.AckNonePolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatalf("create ops consumer: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := cons.Fetch(8, jetstream.FetchMaxWait(500*time.Millisecond))
		if err != nil {
			continue
		}
		for msg := range msgs.Messages() {
			var env opEnvelope
			if json.Unmarshal(msg.Data(), &env) == nil && env.OperationType == replyOp {
				return env
			}
		}
	}
	t.Fatalf("no %s replyOp observed within %s", replyOp, timeout)
	return opEnvelope{}
}

func externalEventBody(t *testing.T, ev externalEvent) []byte {
	t.Helper()
	body, err := json.Marshal(eventBody{Payload: ev})
	if err != nil {
		t.Fatalf("marshal event body: %v", err)
	}
	return body
}

func TestHandleExternal_EgressPermanentFailure_PostsFailedReplyOp(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	v := startTestVault(t, ctx, conn)
	e := newTestEngineWithVault(t, ctx, conn)
	if err := e.RegisterAdapter("stripe", AdapterFunc(func(context.Context, Request) (Dispatch, error) {
		t.Fatal("adapter must never be called on a permanent egress failure")
		return Dispatch{}, nil
	})); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	id := testIdentityID(t)
	_, marker := seedSensitiveAspect(t, ctx, conn, v, id, "ssn", map[string]any{"value": "123-45-6789"})
	if err := v.ShredKey(ctx, "vtx.identity."+id); err != nil {
		t.Fatalf("ShredKey: %v", err)
	}
	params, _ := json.Marshal(map[string]json.RawMessage{"ssn": sensitiveRefParam(t, marker, "value")})
	ev := externalEvent{InstanceKey: "perm-handle-1", Adapter: "stripe", ReplyOp: "ResolveCharge", Params: params}
	msg := substrate.Message{Body: externalEventBody(t, ev), NumDelivered: 1}

	decision := e.handleExternal(ctx, msg)
	if decision != substrate.Ack {
		t.Fatalf("Decision = %v, want Ack (a permanent egress failure converges, never parks)", decision)
	}

	env := awaitReplyOp(t, ctx, conn, "ResolveCharge", 5*time.Second)
	var payload struct {
		ExternalRef string `json:"externalRef"`
		Status      string `json:"status"`
		Result      string `json:"result"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal replyOp payload: %v", err)
	}
	if payload.Status != string(OutcomeFailed) {
		t.Errorf("status = %q, want %q", payload.Status, OutcomeFailed)
	}
	if payload.ExternalRef != "perm-handle-1" {
		t.Errorf("externalRef = %q, want %q", payload.ExternalRef, "perm-handle-1")
	}
}

func TestHandleExternal_EgressTransientFailure_NaksThenEscalates(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	e := newTestEngineWithVault(t, ctx, conn)
	adapterCalled := false
	if err := e.RegisterAdapter("stripe", AdapterFunc(func(context.Context, Request) (Dispatch, error) {
		adapterCalled = true
		return Dispatch{Disposition: Resolved, Result: Result{Status: OutcomeCompleted}}, nil
	})); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	transID := testIdentityID(t)
	marker := sensitiveRefMarker{Ref: "vtx.identity." + transID + ".ssn", Field: "value",
		Ciphertext: vault.Ciphertext{CT: []byte("x"), Nonce: []byte("y"), KeyID: "vtx.identity." + transID}}
	inner, _ := json.Marshal(marker)
	params, _ := json.Marshal(map[string]json.RawMessage{"ssn": json.RawMessage(`{"$sensitiveRef":` + string(inner) + `}`)})
	ev := externalEvent{InstanceKey: "trans-handle-1", Adapter: "stripe", ReplyOp: "ResolveCharge", Params: params}

	// Below the retry budget: NakWithDelay, adapter never called.
	msg := substrate.Message{Body: externalEventBody(t, ev), NumDelivered: 1}
	if decision := e.handleExternal(ctx, msg); decision != substrate.NakWithDelay {
		t.Fatalf("Decision (attempt 1) = %v, want NakWithDelay", decision)
	}
	if adapterCalled {
		t.Fatal("adapter must never be called while the envelope is unresolved")
	}

	// At/past the retry budget: escalates to permanent — a terminal failed
	// replyOp posts and the pattern converges (never parks forever).
	msg2 := substrate.Message{Body: externalEventBody(t, ev), NumDelivered: maxEgressUnwrapAttempts}
	if decision := e.handleExternal(ctx, msg2); decision != substrate.Ack {
		t.Fatalf("Decision (attempt %d) = %v, want Ack (escalated to permanent)", maxEgressUnwrapAttempts, decision)
	}
	env := awaitReplyOp(t, ctx, conn, "ResolveCharge", 5*time.Second)
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal replyOp payload: %v", err)
	}
	if payload.Status != string(OutcomeFailed) {
		t.Errorf("status = %q, want %q (escalated)", payload.Status, OutcomeFailed)
	}
	if adapterCalled {
		t.Fatal("adapter must never be called on an escalated egress failure")
	}
}

// TestFetchLiveEnvelope_ShredThenVaultRestart_DecryptStillRefuses pins the
// restart-/replay-proof shred gate (design §3.2/§3.5's B1 fix): a shred marks
// the identity's piiKey aspect shredded=true (privacy-base's
// ShredIdentityKey), the envelope-lens projects that field (sensitive-param-
// egress Fire 2's own lens fix — piiKeyEnvelopeSpec now RETURNs shredded), and
// a FRESH LocalBackend (simulating a Vault-process restart — the in-memory
// shredded-set is gone) still refuses to decrypt because the caller-supplied
// envelope itself carries Shredded=true. Without the lens fix this arm would
// be the B1 regression reopened: a restarted Vault with no lens signal would
// decrypt a shredded identity's PII again.
func TestFetchLiveEnvelope_ShredThenVaultRestart_DecryptStillRefuses(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	provisionEnvelopeBucket(t, ctx, conn)

	id := testIdentityID(t)
	identityKey := "vtx.identity." + id
	kek := []byte("01234567890123456789012345678901"[:32])

	// The pre-restart Vault: mint the key, encrypt real PII under it.
	v1, err := vault.NewLocalBackend(kek, "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	env, err := v1.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	ct, err := v1.Encrypt(ctx, identityKey, env, []byte(`{"value":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Mirror ShredIdentityKeyDDL for an identity that already had a real key
	// (packages/privacy-base/shred_identity_key.go): shredded flips true,
	// wrappedDEK is NOT zeroed — the lens row must carry shredded=true for the
	// restart gate to hold. This is what the fixed piiKeyEnvelopeSpec projects.
	env.Shredded = true
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal shredded envelope: %v", err)
	}
	if _, err := conn.KVPut(ctx, envelopeLensBucket, identityKey, envBytes); err != nil {
		t.Fatalf("seed shredded envelope: %v", err)
	}

	// The "restart": a brand-new backend instance, same KEK, EMPTY in-memory
	// shredded-set — v1's ShredKey call (if any) is gone. Only the lens's
	// projected Shredded=true stands between this decrypt and the plaintext.
	v2, err := vault.NewLocalBackend(kek, "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend (post-restart): %v", err)
	}
	svc := vault.NewService(v2, slog.Default())
	if err := svc.StartNATSListener(ctx, conn.NATS()); err != nil {
		t.Fatalf("StartNATSListener: %v", err)
	}

	e := &Engine{conn: conn, logger: slog.Default()}
	marker := sensitiveRefMarker{Ref: identityKey + ".ssn", Field: "value", Ciphertext: ct}
	_, ferr := e.resolveSensitiveRef(ctx, marker, 1)
	if ferr == nil || ferr.class != egressPermanent {
		t.Fatalf("post-restart decrypt of a shredded identity: want permanent failure, got %v", ferr)
	}
}

func TestHandleExternal_EgressHappyPath_AdapterReceivesPlaintext(t *testing.T) {
	ctx := context.Background()
	conn := egressTestConn(t)
	v := startTestVault(t, ctx, conn)
	e := newTestEngineWithVault(t, ctx, conn)

	var gotParams map[string]string
	if err := e.RegisterAdapter("stripe", AdapterFunc(func(_ context.Context, req Request) (Dispatch, error) {
		gotParams = req.Params
		return Dispatch{Disposition: Resolved, Result: Result{Status: OutcomeCompleted}}, nil
	})); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	_, marker := seedSensitiveAspect(t, ctx, conn, v, testIdentityID(t), "ssn", map[string]any{"value": "123-45-6789"})
	params, _ := json.Marshal(map[string]json.RawMessage{
		"ssn":    sensitiveRefParam(t, marker, "value"),
		"family": json.RawMessage(`"backgroundCheck"`),
	})
	ev := externalEvent{InstanceKey: "happy-handle-1", Adapter: "stripe", ReplyOp: "ResolveCharge", Params: params}
	msg := substrate.Message{Body: externalEventBody(t, ev), NumDelivered: 1}

	if decision := e.handleExternal(ctx, msg); decision != substrate.Ack {
		t.Fatalf("Decision = %v, want Ack", decision)
	}
	if gotParams["ssn"] != "123-45-6789" {
		t.Errorf("adapter Params[ssn] = %q, want plaintext %q", gotParams["ssn"], "123-45-6789")
	}
	if gotParams["family"] != "backgroundCheck" {
		t.Errorf("adapter Params[family] = %q, want passthrough %q", gotParams["family"], "backgroundCheck")
	}

	// The durable events.external.> plane's ciphertext-only assertion is the
	// e2e's job (design §7); here we only assert the adapter (the last-mile
	// vendor call) received plaintext and the dispatch converged.
	awaitReplyOp(t, ctx, conn, "ResolveCharge", 5*time.Second)
}
