package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/internal/gateway/auth"
)

const testTimeout = 5 * time.Second

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes.
func TestMain(m *testing.M) {
	os.Setenv("CLINIC_APP_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv("CLINIC_APP_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer abc.def.ghi", "abc.def.ghi"}, // case-insensitive scheme
		{"Bearer   spaced  ", "spaced"},
		{"Basic abc", ""},
		{"abc.def.ghi", ""},
		{"", ""},
		{"Bearer ", ""}, // scheme only, no token
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerToken(r); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "x"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}

// TestSetupReadAuth_DevPosture proves the demo posture wires a verifier whose
// trust matches the minter — a token the signer mints verifies, and its subject
// round-trips to the RLS principal.
func TestSetupReadAuth_DevPosture(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	if authn == nil || signer == nil {
		t.Fatalf("dev posture must return non-nil authn (%v) and signer (%v)", authn, signer)
	}

	const sub = "Hj4kPmRtw9nbCxz5vQ2y"
	tok, _, err := signer.mint(sub)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	actor, err := authn.Authenticate(t.Context(), tok)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != sub {
		t.Errorf("subject = %q, want %q", actor.Subject, sub)
	}
	if actor.ActorID != "vtx.identity."+sub {
		t.Errorf("actorID = %q, want vtx.identity.%s", actor.ActorID, sub)
	}
}

// TestSetupReadAuth_DevPosture_SharedKeyInteroperates proves the actual point
// of the shared-dev-IdP interim (real-actor-write-auth-e2e-design.md §3.2):
// a token minted here validates against an independently-built verifier that
// trusts nothing but the shared dev key — standing in for the Gateway's own
// trust set — and a token shaped like what `gateway dev-token` mints (no
// iss/aud claims, kid auth.DevKeyID, signed with the same private key)
// validates at this app's read boundary. One shared key, either direction.
func TestSetupReadAuth_DevPosture_SharedKeyInteroperates(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}

	const sub = "Hj4kPmRtw9nbCxz5vQ2y"

	// This app's minted token verifies against a Gateway-shaped trust set.
	gatewayKeys, gatewaySpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv("CLINIC_APP_DEV_PUBLIC_KEY_PATH"),
	}, nil)
	if err != nil {
		t.Fatalf("LoadTrustedKeys: %v", err)
	}
	gatewayVerifier, err := auth.NewVerifier(auth.Config{Keys: gatewayKeys, KeyInfo: auth.KeyInfoFromSpecs(gatewaySpecs)})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	tok, _, err := signer.mint(sub)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := gatewayVerifier.Verify(tok); err != nil {
		t.Errorf("app-minted token rejected by a Gateway-shaped verifier: %v", err)
	}

	// A `gateway dev-token`-shaped token (no iss/aud, same shared key)
	// verifies at this app's read boundary.
	privKey, err := auth.LoadDevSigningKey(os.Getenv("CLINIC_APP_DEV_PRIVATE_KEY_PATH"))
	if err != nil {
		t.Fatalf("LoadDevSigningKey: %v", err)
	}
	gatewayTok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Subject:   sub,
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
	})
	gatewayTok.Header["kid"] = auth.DevKeyID
	signed, err := gatewayTok.SignedString(privKey)
	if err != nil {
		t.Fatalf("sign gateway-shaped token: %v", err)
	}
	actor, err := authn.Authenticate(t.Context(), signed)
	if err != nil {
		t.Fatalf("app read boundary rejected a gateway-shaped token: %v", err)
	}
	if actor.Subject != sub {
		t.Errorf("subject = %q, want %q", actor.Subject, sub)
	}
}

// TestSetupReadAuth_NoPosture: neither env set ⇒ no authenticator (fail closed).
func TestSetupReadAuth_NoPosture(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "")
	t.Setenv("CLINIC_APP_JWT_PUBLIC_KEY", "")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	if authn != nil || signer != nil {
		t.Fatalf("no posture must return nil authn/signer, got authn=%v signer=%v", authn, signer)
	}
}

// TestSetupReadAuth_BadPublicKey: a configured but unparseable key is a hard
// misconfiguration, not a silent deny-all.
func TestSetupReadAuth_BadPublicKey(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "")
	t.Setenv("CLINIC_APP_JWT_PUBLIC_KEY", "not a pem")
	if _, _, err := setupReadAuth(discardLogger(), true, nil); err == nil {
		t.Fatal("expected an error for an unparseable public key")
	}
}

// fakeRevocationChecker is a fixed-answer auth.RevocationChecker.
type fakeRevocationChecker struct {
	revoked bool
	err     error
}

func (f fakeRevocationChecker) IsRevoked(context.Context, string) (bool, error) {
	return f.revoked, f.err
}

// TestSetupReadAuth_RevocationChecker_Wired proves setupReadAuth threads the
// revocation checker it's given into the Authenticator it builds (§12.1) — a
// revoked actor's otherwise-valid token is denied, not just verified.
func TestSetupReadAuth_RevocationChecker_Wired(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, fakeRevocationChecker{revoked: true})
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	tok, _, err := signer.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := authn.Authenticate(t.Context(), tok); !errors.Is(err, auth.ErrTokenRevoked) {
		t.Fatalf("Authenticate = %v, want ErrTokenRevoked", err)
	}
}

// devAuthServer builds a server with the demo auth posture for handler tests.
func devAuthServer(t *testing.T) *server {
	t.Helper()
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	return &server{logger: discardLogger(), authn: authn, devSigner: signer, natsTimeout: testTimeout}
}

// fakeCredentialResolver is a fixed-answer credentialBindingResolver: bound
// resolves rawActorID to identityKey; unbound reports bound=false; a
// non-nil err always wins. Mirrors internal/gateway's own test fake.
type fakeCredentialResolver struct {
	identityKey string
	bound       bool
	err         error
}

func (f fakeCredentialResolver) Resolve(context.Context, string) (string, bool, error) {
	return f.identityKey, f.bound, f.err
}

// TestAuthenticateRead_CredentialBinding_ResolvesToClaimedIdentity proves a
// bound credential actor reads as the claimed business identity, not the raw
// credential — the read-boundary half of the shared seam
// (real-actor-write-auth-e2e-design.md §5).
func TestAuthenticateRead_CredentialBinding_ResolvesToClaimedIdentity(t *testing.T) {
	s := devAuthServer(t)
	s.credBindings = fakeCredentialResolver{identityKey: "vtx.identity.Bz9wLqXmPr4tKvNhYc3d", bound: true}
	tok, _, err := s.devSigner.mint("Rk3mNpQwZx8bFhTj2Ycd")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	actor, err := s.authenticateRead(r)
	if err != nil {
		t.Fatalf("authenticateRead: %v", err)
	}
	if actor.Subject != "Bz9wLqXmPr4tKvNhYc3d" {
		t.Errorf("subject = %q, want the resolved business identity", actor.Subject)
	}
}

// TestAuthenticateRead_CredentialBinding_Unbound_ActsAsRawActor proves an
// unclaimed credential (no binding yet) reads as itself — the documented
// deny-safe fallback, also covering the CDC-lag window.
func TestAuthenticateRead_CredentialBinding_Unbound_ActsAsRawActor(t *testing.T) {
	s := devAuthServer(t)
	s.credBindings = fakeCredentialResolver{bound: false}
	tok, _, err := s.devSigner.mint("Rk3mNpQwZx8bFhTj2Ycd")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	actor, err := s.authenticateRead(r)
	if err != nil {
		t.Fatalf("authenticateRead: %v", err)
	}
	if actor.Subject != "Rk3mNpQwZx8bFhTj2Ycd" {
		t.Errorf("subject = %q, want the raw credential actor unchanged", actor.Subject)
	}
}

// TestAuthenticateRead_CredentialBinding_ResolveError_FallsBackToRawActor
// proves a resolver failure (e.g. KV unreachable) fails OPEN to the raw
// credential rather than denying the read — mirrors the Gateway's
// resolveActor: acting as the raw credential never grants more than the
// actor is entitled to.
func TestAuthenticateRead_CredentialBinding_ResolveError_FallsBackToRawActor(t *testing.T) {
	s := devAuthServer(t)
	s.credBindings = fakeCredentialResolver{err: errors.New("kv unreachable")}
	tok, _, err := s.devSigner.mint("Rk3mNpQwZx8bFhTj2Ycd")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	actor, err := s.authenticateRead(r)
	if err != nil {
		t.Fatalf("authenticateRead: %v", err)
	}
	if actor.Subject != "Rk3mNpQwZx8bFhTj2Ycd" {
		t.Errorf("subject = %q, want the raw credential actor unchanged (fail open)", actor.Subject)
	}
}

func TestHandleMyAppointments_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleMyAppointments(rec, httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyAppointments_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleMyAppointments(rec, httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandleMyAppointments_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handleMyAppointments(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// TestHandleMyAppointments_ValidToken_PoolUnconfigured_502: a verified actor with
// no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyAppointments_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleMyAppointments(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// TestHandleMyProviderSchedule_* mirror TestHandleMyAppointments_* — the same
// verify-then-RLS boundary, just the provider-anchored sibling endpoint
// (D1.5 Increment 2).

func TestHandleMyProviderSchedule_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleMyProviderSchedule(rec, httptest.NewRequest(http.MethodGet, "/api/my-schedule", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyProviderSchedule_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleMyProviderSchedule(rec, httptest.NewRequest(http.MethodGet, "/api/my-schedule", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandleMyProviderSchedule_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-schedule", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handleMyProviderSchedule(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// TestHandleMyProviderSchedule_ValidToken_PoolUnconfigured_502: a verified
// actor with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyProviderSchedule_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-schedule", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleMyProviderSchedule(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

func TestHandleDevToken_Disabled_404(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // devSigner nil
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"x"}`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (dev-token disabled)", rec.Code)
	}
}

func TestHandleDevToken_Mint_RoundTrips(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"Hj4kPmRtw9nbCxz5vQ2y"}`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" || resp.ExpiresAt == "" {
		t.Fatalf("empty token/expiresAt: %+v", resp)
	}
	actor, err := s.authn.Authenticate(t.Context(), resp.Token)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Errorf("subject = %q", actor.Subject)
	}
}

func TestHandleDevToken_EmptySubject_400(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"  "}`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
