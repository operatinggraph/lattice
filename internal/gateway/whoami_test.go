package gateway

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// claimsWithEmail mirrors internal/gateway/auth's unexported idpClaims —
// same JSON shape (email/email_verified alongside the registered claims),
// redeclared here since the two packages can't share an unexported type.
type claimsWithEmail struct {
	jwt.RegisteredClaims
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
}

func signTokenWithEmail(t *testing.T, priv *rsa.PrivateKey, kid, sub, email string, verified bool) string {
	t.Helper()
	c := claimsWithEmail{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sub,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Email:         email,
		EmailVerified: verified,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token with email: %v", err)
	}
	return s
}

// fakeIdentityIndexHintResolver is a fixed-answer IdentityIndexHintResolver
// (mirrors fakeCredentialResolver): found resolves indexKey to identityKey;
// a non-nil err always wins.
type fakeIdentityIndexHintResolver struct {
	identityKey string
	found       bool
	err         error
	gotKey      string
}

func (f *fakeIdentityIndexHintResolver) Lookup(_ context.Context, indexKey string) (string, bool, error) {
	f.gotKey = indexKey
	return f.identityKey, f.found, f.err
}

func doWhoamiProbe(t *testing.T, s *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/actor?probe=1", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func doWhoami(t *testing.T, s *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/actor", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestHandleWhoami_Unauthenticated_Rejected proves GET /v1/actor is gated by
// the same bearer-token authentication as every other Gateway surface — no
// anonymous actor-discovery oracle.
func TestHandleWhoami_Unauthenticated_Rejected(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := newTestServer(t, authn, nil)

	w := doWhoami(t, s, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestHandleWhoami_UnboundActor_ReportsRawActor proves an unclaimed
// credential's resolvedActorId equals its own actorId (no credential
// binding configured — every actor acts as itself, exactly as
// handleOperations' resolveActor does).
func TestHandleWhoami_UnboundActor_ReportsRawActor(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "NcxqoP292Z4a7uPKftM6")
	s := newTestServer(t, authn, nil)

	w := doWhoami(t, s, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantActor := "vtx.identity.NcxqoP292Z4a7uPKftM6"
	if resp.ActorID != wantActor {
		t.Fatalf("actorId = %q, want %q", resp.ActorID, wantActor)
	}
	if resp.ResolvedActorID != wantActor {
		t.Fatalf("resolvedActorId = %q, want %q (unbound falls back to raw actor)", resp.ResolvedActorID, wantActor)
	}
	wantIndexKey := "vtx.credentialindex." + substrate.SHA256NanoID(wantActor)
	if resp.CredentialIndexKey != wantIndexKey {
		t.Fatalf("credentialIndexKey = %q, want %q", resp.CredentialIndexKey, wantIndexKey)
	}
}

// TestHandleWhoami_BoundActor_ReportsResolvedIdentity proves a claimed
// credential's resolvedActorId is the business identity the credential-
// bindings resolver reports — the same seam handleOperations consults
// (multi-credential-identity-linking-design.md §3.5).
func TestHandleWhoami_BoundActor_ReportsResolvedIdentity(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "NcxqoP292Z4a7uPKftM6")
	s := newTestServer(t, authn, nil)
	s.ConfigureCredentialBindings(fakeCredentialResolver{identityKey: "vtx.identity.CLAIMEDBUSINESS0000", bound: true})

	w := doWhoami(t, s, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ActorID != "vtx.identity.NcxqoP292Z4a7uPKftM6" {
		t.Fatalf("actorId = %q, want the raw credential", resp.ActorID)
	}
	if resp.ResolvedActorID != "vtx.identity.CLAIMEDBUSINESS0000" {
		t.Fatalf("resolvedActorId = %q, want the resolved business identity", resp.ResolvedActorID)
	}
}

// TestHandleWhoami_PostMethod_Rejected proves the endpoint is GET-only.
func TestHandleWhoami_PostMethod_Rejected(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "NcxqoP292Z4a7uPKftM6")
	s := newTestServer(t, authn, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/actor", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestHandleWhoami_TriggersProvisioningPreflight proves whoami runs the same
// first-authenticated-touch auto-provisioning pre-flight handleOperations
// does — "the natural first authenticated call for a fresh FE session"
// (§3.5) — when ConfigureProvisioning is set.
func TestHandleWhoami_TriggersProvisioningPreflight(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "nXFwju1FgWbCTmAdPZkF")

	var captured []*processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = append(captured, env)
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureProvisioning("vtx.identity.GATEWAYSYSTEM000000", "vtx.role.consumerROLE00000000")

	w := doWhoami(t, s, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(captured) != 1 {
		t.Fatalf("submit called %d times, want 1 (the provisioning pre-flight)", len(captured))
	}
	if captured[0].OperationType != "ProvisionConsumerIdentity" {
		t.Fatalf("OperationType = %q, want ProvisionConsumerIdentity", captured[0].OperationType)
	}
	if captured[0].Actor != "vtx.identity.GATEWAYSYSTEM000000" {
		t.Fatalf("Actor = %q, want the gateway's own actor", captured[0].Actor)
	}
}

// TestHandleWhoami_Probe_HintFound proves `?probe=1` with a verified email
// matching a DIFFERENT identity's identityindex hit surfaces
// existingIdentityHint=true, and that the looked-up key is the same
// sha256NanoID("email:"+normalizedEmail) construction the write-path dedup
// scripts use (§3.4), lowercased+trimmed exactly like ddls.go's own
// `raw_email.strip().lower()`.
func TestHandleWhoami_Probe_HintFound(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "  Person@Example.Test  ", true)
	s := newTestServer(t, authn, nil)
	resolver := &fakeIdentityIndexHintResolver{identityKey: "vtx.identity.STAFFCREATED00000001", found: true}
	s.ConfigureIdentityIndexHint(resolver)

	w := doWhoamiProbe(t, s, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = false, want true (email matches a different identity)")
	}
	wantIndexKey := "vtx.identityindex." + substrate.SHA256NanoID("email:person@example.test")
	if resolver.gotKey != wantIndexKey {
		t.Fatalf("looked up %q, want %q", resolver.gotKey, wantIndexKey)
	}
}

// TestHandleWhoami_Probe_NoHit proves a configured resolver reporting no
// match yields existingIdentityHint=false.
func TestHandleWhoami_Probe_NoHit(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "person@example.test", true)
	s := newTestServer(t, authn, nil)
	s.ConfigureIdentityIndexHint(&fakeIdentityIndexHintResolver{found: false})

	w := doWhoamiProbe(t, s, token)
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = true, want false (no index hit)")
	}
}

// TestHandleWhoami_Probe_HitIsOwnActor proves a hit that resolves to the
// caller's OWN actor key never sets the hint — §3.4's "identityKey !=
// target_actor_key" condition, the case where the caller IS the indexed
// identity, not a duplicate.
func TestHandleWhoami_Probe_HitIsOwnActor(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "person@example.test", true)
	s := newTestServer(t, authn, nil)
	s.ConfigureIdentityIndexHint(&fakeIdentityIndexHintResolver{
		identityKey: "vtx.identity.NcxqoP292Z4a7uPKftM6", found: true,
	})

	w := doWhoamiProbe(t, s, token)
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = true, want false (the hit IS the caller's own actor)")
	}
}

// TestHandleWhoami_Probe_NoVerifiedEmail_SkipsLookup proves a probe with no
// verified email claim never performs a lookup — an unverified or absent
// email must not become a probe surface.
func TestHandleWhoami_Probe_NoVerifiedEmail_SkipsLookup(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "person@example.test", false)
	s := newTestServer(t, authn, nil)
	resolver := &fakeIdentityIndexHintResolver{identityKey: "vtx.identity.STAFFCREATED00000001", found: true}
	s.ConfigureIdentityIndexHint(resolver)

	w := doWhoamiProbe(t, s, token)
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = true, want false (email_verified=false)")
	}
	if resolver.gotKey != "" {
		t.Fatalf("resolver was consulted (key %q), want no lookup for an unverified email", resolver.gotKey)
	}
}

// TestHandleWhoami_NoProbeParam_OmitsHint proves a plain (no `?probe=1`)
// whoami call never consults the resolver, even when one is configured and
// would report a hit — the probe stays opt-in per call.
func TestHandleWhoami_NoProbeParam_OmitsHint(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "person@example.test", true)
	s := newTestServer(t, authn, nil)
	resolver := &fakeIdentityIndexHintResolver{identityKey: "vtx.identity.STAFFCREATED00000001", found: true}
	s.ConfigureIdentityIndexHint(resolver)

	w := doWhoami(t, s, token)
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = true, want false (no ?probe=1)")
	}
	if resolver.gotKey != "" {
		t.Fatalf("resolver was consulted (key %q), want no lookup without ?probe=1", resolver.gotKey)
	}
}

// TestHandleWhoami_Probe_NoResolverConfigured proves `?probe=1` degrades
// gracefully (no panic, hint omitted) when ConfigureIdentityIndexHint was
// never called — the same additive/best-effort posture as an unconfigured
// CredentialBindingResolver.
func TestHandleWhoami_Probe_NoResolverConfigured(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signTokenWithEmail(t, priv, "k1", "NcxqoP292Z4a7uPKftM6", "person@example.test", true)
	s := newTestServer(t, authn, nil)

	w := doWhoamiProbe(t, s, token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp whoamiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ExistingIdentityHint {
		t.Fatal("existingIdentityHint = true, want false (no resolver configured)")
	}
}
