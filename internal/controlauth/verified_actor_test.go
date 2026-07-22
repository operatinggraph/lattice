package controlauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const testVerifierKID = "test-key-1"

func newTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	return k
}

func signTestToken(t *testing.T, priv *rsa.PrivateKey, kid string, claims jwt.RegisteredClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func validTestClaims(sub string) jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Subject:   sub,
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}
}

// fakeRevocationChecker is a controllable RevocationChecker (auth.go) for
// exercising ResolveActor's revocation branch without the real substrate-KV
// implementation.
type fakeRevocationChecker struct {
	revoked map[string]bool
	err     error
}

func (f *fakeRevocationChecker) IsRevoked(ctx context.Context, actorID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[actorID], nil
}

func TestResolveActor_NilVerifier_PassesThroughRawHeader(t *testing.T) {
	srv := startResolveEchoService(t, nil)
	defer srv.Stop()

	nc, err := nats.Connect(srv.natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	reply, err := nc.RequestMsg(NewActorRequestMsg("controlauth.test.resolve", "vtx.identity.OPERATOR"), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := string(reply.Data); got != "vtx.identity.OPERATOR" {
		t.Fatalf("got actor %q, want vtx.identity.OPERATOR (nil verifier = Fire 1 passthrough)", got)
	}
}

func TestResolveActor_NilVerifier_NoHeaderResolvesEmpty(t *testing.T) {
	srv := startResolveEchoService(t, nil)
	defer srv.Stop()

	nc, err := nats.Connect(srv.natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	reply, err := nc.RequestMsg(NewActorRequestMsg("controlauth.test.resolve", ""), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := string(reply.Data); got != "" {
		t.Fatalf("got actor %q, want empty", got)
	}
}

func TestResolveActor_VerifierConfigured_ValidTokenResolvesActorID(t *testing.T) {
	priv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	srv := startResolveEchoService(t, av)
	defer srv.Stop()
	nc, err := nats.Connect(srv.natsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	token := signTestToken(t, priv, testVerifierKID, validTestClaims("HWiis7G8Q9pqmc2nm5x1"))
	reply, err := nc.RequestMsg(NewActorRequestMsg("controlauth.test.resolve", token), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := string(reply.Data); got != "vtx.identity.HWiis7G8Q9pqmc2nm5x1" {
		t.Fatalf("got actor %q, want vtx.identity.HWiis7G8Q9pqmc2nm5x1", got)
	}
}

func TestResolveActor_VerifierConfigured_NoTokenDenies(t *testing.T) {
	priv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	req := &fakeMicroRequest{}
	_, err = ResolveActor(context.Background(), req, av)
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestResolveActor_VerifierConfigured_MalformedTokenDenies(t *testing.T) {
	priv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	req := &fakeMicroRequest{headers: nats.Header{HeaderActor: []string{"not-a-jwt"}}}
	_, err = ResolveActor(context.Background(), req, av)
	if err == nil {
		t.Fatal("expected an error for a malformed token, got nil")
	}
	if !errors.Is(err, auth.ErrMalformedToken) {
		t.Fatalf("err = %v, want wrapping auth.ErrMalformedToken", err)
	}
}

func TestResolveActor_VerifierConfigured_ExpiredTokenDenies(t *testing.T) {
	priv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	expired := jwt.RegisteredClaims{
		Subject:   "HWiis7G8Q9pqmc2nm5x1",
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}
	token := signTestToken(t, priv, testVerifierKID, expired)
	req := &fakeMicroRequest{headers: nats.Header{HeaderActor: []string{token}}}
	_, err = ResolveActor(context.Background(), req, av)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("err = %v, want wrapping auth.ErrTokenExpired", err)
	}
}

func TestResolveActor_VerifierConfigured_RevokedActorDenies(t *testing.T) {
	priv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	rev := &fakeRevocationChecker{revoked: map[string]bool{"vtx.identity.HWiis7G8Q9pqmc2nm5x1": true}}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, rev))

	token := signTestToken(t, priv, testVerifierKID, validTestClaims("HWiis7G8Q9pqmc2nm5x1"))
	req := &fakeMicroRequest{headers: nats.Header{HeaderActor: []string{token}}}
	_, err = ResolveActor(context.Background(), req, av)
	if !errors.Is(err, auth.ErrTokenRevoked) {
		t.Fatalf("err = %v, want wrapping auth.ErrTokenRevoked", err)
	}
}

func TestResolveActor_VerifierConfigured_UnknownSignerDenies(t *testing.T) {
	trustedPriv := newTestRSAKey(t)
	untrustedPriv := newTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{testVerifierKID: &trustedPriv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{testVerifierKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	av := NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	// Forged: signed by a key the verifier does not trust, but claiming the
	// trusted kid — proves ResolveActor denies on signature mismatch, not
	// just on an absent kid.
	token := signTestToken(t, untrustedPriv, testVerifierKID, validTestClaims("HWiis7G8Q9pqmc2nm5x1"))
	req := &fakeMicroRequest{headers: nats.Header{HeaderActor: []string{token}}}
	_, err = ResolveActor(context.Background(), req, av)
	if !errors.Is(err, auth.ErrInvalidSignature) {
		t.Fatalf("err = %v, want wrapping auth.ErrInvalidSignature", err)
	}
}

// fakeMicroRequest is a minimal micro.Request for exercising ResolveActor
// directly (no real NATS round-trip needed for the deny-path assertions —
// ActorFromRequest only reads req.Headers()).
type fakeMicroRequest struct {
	micro.Request
	headers nats.Header
}

func (f *fakeMicroRequest) Headers() micro.Headers {
	return micro.Headers(f.headers)
}

// resolveEchoService is startEchoService's Fire 2 sibling: replies with
// ResolveActor's result (or the error string, prefixed) instead of the raw
// ActorFromRequest value, proving ResolveActor's behavior over a real micro
// request/reply round-trip.
type resolveEchoService struct {
	natsURL string
	svc     micro.Service
	nc      *nats.Conn
}

func (e *resolveEchoService) Stop() {
	_ = e.svc.Stop()
	e.nc.Close()
}

func startResolveEchoService(t *testing.T, verifier *ActorVerifier) *resolveEchoService {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	svc, err := micro.AddService(nc, micro.Config{Name: "controlauth-resolve-test", Version: "0.0.1"})
	if err != nil {
		t.Fatalf("micro.AddService: %v", err)
	}
	if err := svc.AddEndpoint("resolve",
		micro.HandlerFunc(func(req micro.Request) {
			actor, err := ResolveActor(context.Background(), req, verifier)
			if err != nil {
				_ = req.Respond([]byte("ERR:" + err.Error()))
				return
			}
			_ = req.Respond([]byte(actor))
		}),
		micro.WithEndpointSubject("controlauth.test.resolve")); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}

	return &resolveEchoService{natsURL: url, svc: svc, nc: nc}
}
