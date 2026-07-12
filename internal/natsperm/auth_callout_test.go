package natsperm

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	gwauth "github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/gateway/natsauth"
	"github.com/asolgan/lattice/internal/substrate"
)

// calloutIssuerSeedPath is deploy/nkeys/auth-callout-issuer.nk — the same
// path cmd/gateway's NATS_AUTH_CALLOUT_ISSUER_SEED default names, committed
// alongside the other dev seeds by deploy/gen-dev-nkeys.
func calloutIssuerSeedPath(t *testing.T) string {
	return seedPath(t, "auth-callout-issuer")
}

// calloutXkeySeedPath is deploy/nkeys/auth-callout-xkey.nk — the same path
// cmd/gateway's NATS_AUTH_CALLOUT_XKEY_SEED default names.
func calloutXkeySeedPath(t *testing.T) string {
	return seedPath(t, "auth-callout-xkey")
}

// fakeRevocationChecker reports revoked for exactly one configured actor —
// enough to drive the responder's revocation vector without a live KV bucket.
type fakeRevocationChecker struct{ revokedActor string }

func (f fakeRevocationChecker) IsRevoked(_ context.Context, actorID string) (bool, error) {
	return actorID == f.revokedActor, nil
}

// startResponder wires a real internal/gateway/auth.Authenticator + a real
// natsauth.Responder onto the "gateway" component's own committed NKey
// connection (already an auth_users bypass member) and subscribes it to the
// server's auth-callout subject — the exact shape cmd/gateway wires in
// production, proven here against the REAL committed conf + seed, not a
// hand-built fixture (mirrors startServerFromConf's own rationale).
func startResponder(t *testing.T, url string, trustedKID string, pub crypto.PublicKey, revoked string) {
	t.Helper()
	verifier, err := gwauth.NewVerifier(gwauth.Config{
		Keys:    map[string]crypto.PublicKey{trustedKID: pub},
		KeyInfo: map[string]gwauth.KeyInfo{trustedKID: {Spec: gwauth.BindingSpec{Mode: gwauth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("build verifier: %v", err)
	}
	authn := gwauth.NewAuthenticator(verifier, fakeRevocationChecker{revokedActor: revoked})

	seed, err := readSeed(calloutIssuerSeedPath(t))
	if err != nil {
		t.Fatalf("read auth-callout issuer seed: %v", err)
	}
	issuer, err := nkeys.FromSeed(seed)
	if err != nil {
		t.Fatalf("parse auth-callout issuer seed: %v", err)
	}

	xkeySeed, err := readSeed(calloutXkeySeedPath(t))
	if err != nil {
		t.Fatalf("read auth-callout xkey seed: %v", err)
	}
	xkp, err := nkeys.FromCurveSeed(xkeySeed)
	if err != nil {
		t.Fatalf("parse auth-callout xkey seed: %v", err)
	}

	// A short TTL in-test so the revocation-driven expiry-disconnect vector
	// does not need to wait 15 real minutes.
	responder, err := natsauth.NewResponder(authn, noopResolver{}, issuer, natsauth.MinAuthzTTL)
	if err != nil {
		t.Fatalf("build responder: %v", err)
	}

	gw := connectAs(t, url, "gateway")
	sub, err := gw.NATS().Subscribe(natsauth.AuthCalloutSubject, func(msg *nats.Msg) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Mirrors cmd/gateway's subscription callback exactly (the real
		// committed conf now always configures auth_callout.xkey, so the
		// live server seals every request to this responder — a test
		// wiring that skipped the unseal/seal round trip would silently
		// stop proving anything against msg.Data).
		serverXkey := msg.Header.Get(natsauth.AuthRequestXKeyHeader)
		reqToken, err := natsauth.UnsealRequest(msg.Data, serverXkey, xkp)
		if err != nil {
			return
		}
		resp, err := responder.Handle(ctx, reqToken)
		if err != nil {
			return
		}
		sealed, err := natsauth.SealResponse(resp, serverXkey, xkp)
		if err != nil {
			return
		}
		_ = msg.Respond(sealed)
	})
	if err != nil {
		t.Fatalf("subscribe %s: %v", natsauth.AuthCalloutSubject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

// noopResolver is Fire 1's deny-safe default (no credential-bindings bucket
// wired in this conformance test) — every connection acts as its own raw
// verified actor, exactly cmd/gateway's noopIdentityResolver.
type noopResolver struct{}

func (noopResolver) Resolve(context.Context, string) (string, bool, error) { return "", false, nil }

func readSeed(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(raw), nil
}

func rsaKeypair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	return k, &k.PublicKey
}

// mintBearerToken signs a RS256 token with the given kid + subject + expiry
// — the same shape internal/gateway/auth.Verifier.Verify expects (ModeNanoID
// binding: sub passed through after IsValidNanoID).
func mintBearerToken(t *testing.T, priv *rsa.PrivateKey, kid, sub string, exp time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Subject:   sub,
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func nanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("generate nanoid: %v", err)
	}
	return id
}

// connectEdge dials as an untrusted (non-auth_users) connection presenting
// bearerToken — every real Edge connect's shape, delegated to the responder.
// Mirrors cmd/edge exactly, including nats.CustomInboxPrefix("_INBOX.edge.<identity>")
// (design §3.3) — WITHOUT it, nats.go's own default request-reply inbox
// (`_INBOX.<nuid>.*`, used internally by every JetStream API call) falls
// outside the issued grant and every JetStream operation on the connection
// fails closed as a side effect, not as the vector under test.
func connectEdge(t *testing.T, url, bearerToken, identity, deviceName string) (*nats.Conn, error) {
	t.Helper()
	opts := []nats.Option{nats.Token(bearerToken), nats.Name(deviceName), nats.Timeout(5 * time.Second)}
	if identity != "" {
		opts = append(opts, nats.CustomInboxPrefix("_INBOX.edge."+identity))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	t.Cleanup(nc.Close)
	return nc, nil
}

const syncStream = "SYNC"

func provisionSyncStream(t *testing.T, url string) {
	t.Helper()
	boot := connectAs(t, url, "bootstrap")
	provisionStream(t, boot, syncStream, []string{"lattice.sync.user.>"})
}

// TestAuthCallout_OwnSliceSubscribeAllowed is design §8 vector 1: a verified
// identity's connection can subscribe (via a filtered JetStream pull
// consumer) its OWN lattice.sync.user.<id> subject and receive a delivered
// message.
func TestAuthCallout_OwnSliceSubscribeAllowed(t *testing.T) {
	url := startServerFromConf(t)
	provisionSyncStream(t, url)

	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	identity := nanoID(t)
	tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
	nc, err := connectEdge(t, url, tok, identity, "device-1")
	if err != nil {
		t.Fatalf("edge connect: want success, got %v", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream context: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	durable := "edge-sync-" + identity + "-device-1"
	cons, err := js.CreateOrUpdateConsumer(ctx, syncStream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: "lattice.sync.user." + identity,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create own-slice consumer: want success, got %v", err)
	}

	// Publish a delta as refractor (the sanctioned publisher) and confirm the
	// consumer actually delivers it.
	ref := connectAs(t, url, "refractor")
	if err := ref.Publish(ctx, "lattice.sync.user."+identity, []byte("{}"), nil); err != nil {
		t.Fatalf("refractor publish delta: %v", err)
	}

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := 0
	for m := range msgs.Messages() {
		got++
		_ = m.Ack()
	}
	if got != 1 {
		t.Fatalf("delivered messages = %d, want 1", got)
	}
}

// TestAuthCallout_CrossIdentityDenied is design §8 vector 2: identity A's
// connection cannot subscribe B's raw subject, cannot create a consumer
// filtered on B's subject, and cannot pull from B's durable.
func TestAuthCallout_CrossIdentityDenied(t *testing.T) {
	url := startServerFromConf(t)
	provisionSyncStream(t, url)

	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	identityA := nanoID(t)
	identityB := nanoID(t)
	tokA := mintBearerToken(t, priv, "test-kid", identityA, time.Now().Add(time.Hour))
	ncA, err := connectEdge(t, url, tokA, identityA, "device-1")
	if err != nil {
		t.Fatalf("edge A connect: want success, got %v", err)
	}

	// Raw SUB on B's subject: NATS permission violations are async — publish
	// against A's own subject after subscribing and assert the message never
	// arrives on the (denied) B subscription within a bound.
	subB, err := ncA.SubscribeSync("lattice.sync.user." + identityB)
	if err != nil {
		t.Fatalf("SubscribeSync (server-side accepted, permission enforced on delivery): %v", err)
	}
	ref := connectAs(t, url, "refractor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ref.Publish(ctx, "lattice.sync.user."+identityB, []byte("{}"), nil); err != nil {
		t.Fatalf("refractor publish to B: %v", err)
	}
	if _, err := subB.NextMsg(2 * time.Second); err == nil {
		t.Fatal("A's denied subscription to B's subject received a message")
	}

	// CONSUMER.CREATE with B's filter: denied (unroutable — A holds no
	// permission naming B's filter in the pinned subject).
	jsA, err := jetstream.New(ncA)
	if err != nil {
		t.Fatalf("jetstream context: %v", err)
	}
	if _, err := jsA.CreateOrUpdateConsumer(ctx, syncStream, jetstream.ConsumerConfig{
		Durable:       "edge-sync-" + identityA + "-device-1",
		FilterSubject: "lattice.sync.user." + identityB,
	}); err == nil {
		t.Fatal("consumer create with B's filter: want denial, got success")
	}

	// CONSUMER.INFO / MSG.NEXT on B's own durable (created by B itself): A
	// cannot even look it up, let alone pull from it — both verbs are
	// exact-pinned to the connection's OWN durable name (§3.3).
	tokB := mintBearerToken(t, priv, "test-kid", identityB, time.Now().Add(time.Hour))
	ncB, err := connectEdge(t, url, tokB, identityB, "device-1")
	if err != nil {
		t.Fatalf("edge B connect: want success, got %v", err)
	}
	jsB, err := jetstream.New(ncB)
	if err != nil {
		t.Fatalf("jetstream context: %v", err)
	}
	durableB := "edge-sync-" + identityB + "-device-1"
	// A FRESH context — ctx's budget was already spent waiting out the
	// preceding denied CONSUMER.CREATE call above; reusing it here would
	// make B's legitimate, unrelated call fail on a starved deadline, not a
	// permission problem.
	createCtx, createCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer createCancel()
	if _, err := jsB.CreateOrUpdateConsumer(createCtx, syncStream, jetstream.ConsumerConfig{
		Durable:       durableB,
		FilterSubject: "lattice.sync.user." + identityB,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("B creates own consumer: %v", err)
	}
	infoCtx, infoCancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer infoCancel()
	if _, err := jsA.Consumer(infoCtx, syncStream, durableB); err == nil {
		t.Fatal("A resolved B's durable via CONSUMER.INFO — expected denial")
	}
}

// TestAuthCallout_FailClosed is design §8 vector 3 (the parts a unit test
// can exercise deterministically — malformed/expired/unknown-kid/empty
// tokens; "responder down" is covered by natsauth's own unit tests, not
// re-proven here against a live server to avoid an auth_timeout-bound test).
func TestAuthCallout_FailClosed(t *testing.T) {
	url := startServerFromConf(t)
	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"malformed", "not-a-jwt"},
		{"expired", mintBearerToken(t, priv, "test-kid", nanoID(t), time.Now().Add(-time.Minute))},
		{"unknown-kid", mintBearerToken(t, priv, "wrong-kid", nanoID(t), time.Now().Add(time.Hour))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := connectEdge(t, url, c.token, "", "device-1"); err == nil {
				t.Fatalf("%s token: want connect denial, got success", c.name)
			}
		})
	}
}

// TestAuthCallout_Revocation is design §8 vector 4 (new-connect half — the
// live-connection expiry-disconnect half is the responder's own MaxTTL unit
// test, natsauth.TestResponder_Handle_AuthorizationCappedAtMaxTTL): a
// revoked actor's identity cannot establish a new connection.
func TestAuthCallout_Revocation(t *testing.T) {
	url := startServerFromConf(t)
	priv, pub := rsaKeypair(t)
	identity := nanoID(t)
	startResponder(t, url, "test-kid", pub, gwauth.IdentityKeyPrefix+identity)

	tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
	if _, err := connectEdge(t, url, tok, identity, "device-1"); err == nil {
		t.Fatal("revoked identity: want connect denial, got success")
	}
}

// TestAuthCallout_DeltaForgeryDenied is design §8 vector 6: the edge
// connection may publish neither its own sync subject (delta forgery — only
// Refractor publishes there) nor core-operations.
func TestAuthCallout_DeltaForgeryDenied(t *testing.T) {
	url := startServerFromConf(t)
	provisionSyncStream(t, url)
	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	identity := nanoID(t)
	tok := mintBearerToken(t, priv, "test-kid", identity, time.Now().Add(time.Hour))
	nc, err := connectEdge(t, url, tok, identity, "device-1")
	if err != nil {
		t.Fatalf("edge connect: %v", err)
	}
	c, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer cancel()
	if err := c.Publish(ctx, "lattice.sync.user."+identity, []byte("forged"), nil); err == nil {
		t.Fatal("edge publish to its own sync subject: want denial, got success")
	}
	if err := c.Publish(ctx, "ops.default", []byte("forged"), nil); err == nil {
		t.Fatal("edge publish to core-operations: want denial, got success")
	}
}

// TestAuthCallout_InboxIsolation is design §8 vector 7: identity A may not
// subscribe identity B's reply-inbox namespace.
func TestAuthCallout_InboxIsolation(t *testing.T) {
	url := startServerFromConf(t)
	priv, pub := rsaKeypair(t)
	startResponder(t, url, "test-kid", pub, "")

	identityA := nanoID(t)
	identityB := nanoID(t)
	tokA := mintBearerToken(t, priv, "test-kid", identityA, time.Now().Add(time.Hour))
	ncA, err := connectEdge(t, url, tokA, identityA, "device-1")
	if err != nil {
		t.Fatalf("edge A connect: %v", err)
	}

	subA, err := ncA.SubscribeSync("_INBOX.edge." + identityB + ".>")
	if err != nil {
		t.Fatalf("SubscribeSync on B's inbox namespace (permission enforced on delivery): %v", err)
	}
	// processor holds a broad "_INBOX.>" publish grant (the op-submission
	// reply path) — the one existing component whose allow-list can even
	// reach an arbitrary inbox namespace, standing in for "any publisher".
	admin := connectAs(t, url, "processor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// PublishCore, not Publish: an inbox subject is core-NATS only (no
	// backing JetStream stream), so a JetStream-context publish would fail
	// with "no response from stream" regardless of permissions.
	if err := admin.PublishCore(ctx, "_INBOX.edge."+identityB+".probe", []byte("{}")); err != nil {
		t.Fatalf("publish into B's inbox namespace: %v", err)
	}
	if _, err := subA.NextMsg(2 * time.Second); err == nil {
		t.Fatal("A's denied subscription to B's inbox namespace received a message")
	}
}
