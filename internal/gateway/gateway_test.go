package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/processor"
)

// --- test JWT fixtures ---------------------------------------------------

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func signToken(t *testing.T, priv *rsa.PrivateKey, kid, sub string) string {
	t.Helper()
	c := jwt.RegisteredClaims{
		Subject:   sub,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// testAuthenticator builds a real auth.Authenticator over a single trusted
// test key — the same production Verify path the Gateway hands requests to,
// with no NATS dependency (revocation checker nil).
func testAuthenticator(t *testing.T, priv *rsa.PrivateKey, kid string) *auth.Authenticator {
	t.Helper()
	v, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{kid: &priv.PublicKey}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return auth.NewAuthenticator(v, nil)
}

// --- buildEnvelope --------------------------------------------------------

func TestBuildEnvelope_StampsVerifiedActor_IgnoresBodyActor(t *testing.T) {
	// operationRequest carries no `actor` field by construction — assert that
	// even a body containing a raw "actor" key never influences the built
	// envelope (the field simply isn't bound during unmarshal).
	var req operationRequest
	if err := json.Unmarshal([]byte(`{"operationType":"PingPlatform","actor":"vtx.identity.EVILACTOR00000000000"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	env, err := buildEnvelope(req, "vtx.identity.REALACTOR00000000000", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.Actor != "vtx.identity.REALACTOR00000000000" {
		t.Fatalf("Actor = %q, want the verified actor (forged body actor must never win)", env.Actor)
	}
}

func TestBuildEnvelope_RequiresOperationType(t *testing.T) {
	_, err := buildEnvelope(operationRequest{}, "vtx.identity.x", time.Now())
	if err == nil {
		t.Fatal("want error for missing operationType")
	}
}

func TestBuildEnvelope_RejectsInvalidLane(t *testing.T) {
	_, err := buildEnvelope(operationRequest{OperationType: "Ping", Lane: "bogus"}, "vtx.identity.x", time.Now())
	if err == nil {
		t.Fatal("want error for invalid lane")
	}
}

func TestBuildEnvelope_DefaultsLaneAndPayload(t *testing.T) {
	env, err := buildEnvelope(operationRequest{OperationType: "Ping"}, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.Lane != processor.LaneDefault {
		t.Fatalf("Lane = %q, want default", env.Lane)
	}
	if string(env.Payload) != "{}" {
		t.Fatalf("Payload = %q, want {}", env.Payload)
	}
}

func TestBuildEnvelope_GeneratesRequestIDWhenOmitted(t *testing.T) {
	env, err := buildEnvelope(operationRequest{OperationType: "Ping"}, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.RequestID == "" {
		t.Fatal("want a generated requestId")
	}
}

func TestBuildEnvelope_PreservesClientRequestID(t *testing.T) {
	env, err := buildEnvelope(operationRequest{OperationType: "Ping", RequestID: "client-supplied-id"}, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.RequestID != "client-supplied-id" {
		t.Fatalf("RequestID = %q, want client-supplied-id (Contract #4 idempotency forwards it verbatim)", env.RequestID)
	}
}

func TestBuildEnvelope_ForwardsAuthContextAndReads(t *testing.T) {
	req := operationRequest{
		OperationType: "Ping",
		AuthContext:   &processor.AuthContext{Service: "svc.x"},
		Reads:         []string{"vtx.a.1", "vtx.a.1", " ", "vtx.b.2"},
	}
	env, err := buildEnvelope(req, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.AuthContext == nil || env.AuthContext.Service != "svc.x" {
		t.Fatalf("AuthContext not forwarded: %+v", env.AuthContext)
	}
	if env.ContextHint == nil || len(env.ContextHint.Reads) != 2 {
		t.Fatalf("ContextHint.Reads = %+v, want deduped [vtx.a.1 vtx.b.2]", env.ContextHint)
	}
}

// TestBuildEnvelope_ForwardsOptionalReads proves a browser-direct client can
// declare Contract #2 §2.5 optionalReads (script-read-posture-design.md §13
// verticals-fire dispatcher wiring) — a class-(d) read-before-create/dedup —
// same dedup/trim treatment as Reads, both wire forms (bare + nested).
func TestBuildEnvelope_ForwardsOptionalReads(t *testing.T) {
	req := operationRequest{
		OperationType: "Ping",
		Reads:         []string{"vtx.a.1"},
		OptionalReads: []string{"vtx.a.1.guard", "vtx.a.1.guard", " "},
	}
	env, err := buildEnvelope(req, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env.ContextHint == nil || len(env.ContextHint.OptionalReads) != 1 || env.ContextHint.OptionalReads[0] != "vtx.a.1.guard" {
		t.Fatalf("ContextHint.OptionalReads = %+v, want deduped [vtx.a.1.guard]", env.ContextHint)
	}

	nested := operationRequest{
		OperationType: "Ping",
		ContextHint:   &operationRequestContext{Reads: []string{"vtx.a.1"}, OptionalReads: []string{"vtx.a.1.guard"}},
	}
	env2, err := buildEnvelope(nested, "vtx.identity.x", time.Now())
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	if env2.ContextHint == nil || len(env2.ContextHint.OptionalReads) != 1 || env2.ContextHint.OptionalReads[0] != "vtx.a.1.guard" {
		t.Fatalf("nested contextHint.optionalReads not forwarded: %+v", env2.ContextHint)
	}
}

// --- bearerToken -----------------------------------------------------------

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi", true},
		{"", "", false},
		{"Basic abc", "", false},
		{"Bearer ", "", false},
		{"bearer abc", "", false}, // case-sensitive scheme
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/v1/operations", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		got, ok := bearerToken(r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("bearerToken(%q) = (%q,%v), want (%q,%v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

// --- handleOperations end-to-end (fake submit, real Authenticator) --------

func newTestServer(t *testing.T, authn *auth.Authenticator, submit submitFunc) *Server {
	t.Helper()
	s := &Server{authn: authn, submit: submit, logger: nopLogger{}, reqTimeout: 5 * time.Second, metrics: &Metrics{}}
	return s
}

func doOperations(t *testing.T, s *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/operations", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestHandleOperations_ForgedActorNeverWins is the Gate-3 adversarial vector
// for the Gateway (design §6): a request body carries a forged `actor`
// claiming a different, more-privileged identity than the token's verified
// subject. Assert the envelope the Gateway actually publishes carries the
// VERIFIED actor — the forged value never reaches core-operations.
func TestHandleOperations_ForgedActorNeverWins(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "REALACTOR00000000000")

	var captured *processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = env
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)

	body := `{"operationType":"PingPlatform","actor":"vtx.identity.EVILADMIN0000000000"}`
	w := doOperations(t, s, token, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("submit was never called")
	}
	if captured.Actor != "vtx.identity.REALACTOR00000000000" {
		t.Fatalf("EXPOSED — env.Actor = %q, want the verified actor (forged body actor won)", captured.Actor)
	}
}

// --- ConfigureCredentialBindings / resolveActor ---------------------------

// fakeCredentialResolver is a fixed-answer CredentialBindingResolver: bound
// resolves rawActorID to identityKey; unbound reports bound=false; a
// non-nil err always wins.
type fakeCredentialResolver struct {
	identityKey string
	bound       bool
	err         error
}

func (f fakeCredentialResolver) Resolve(context.Context, string) (string, bool, error) {
	return f.identityKey, f.bound, f.err
}

// TestHandleOperations_CredentialBinding_ResolvesToClaimedIdentity proves a
// bound credential actor's op is stamped with the claimed business identity,
// not the raw credential (gateway-claim-flow-identity-provisioning-
// design.md §11.0/§11.5 R1).
func TestHandleOperations_CredentialBinding_ResolvesToClaimedIdentity(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "RAWCREDENTIAL00000000")

	var captured *processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = env
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureCredentialBindings(fakeCredentialResolver{identityKey: "vtx.identity.CLAIMEDBUSINESS0000", bound: true})

	w := doOperations(t, s, token, `{"operationType":"RenewLease"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured.Actor != "vtx.identity.CLAIMEDBUSINESS0000" {
		t.Fatalf("env.Actor = %q, want the resolved business identity", captured.Actor)
	}
}

// TestHandleOperations_CredentialBinding_ClaimIdentityCarveOut proves
// ClaimIdentity always sees the raw credential actor, never resolved — the
// one carve-out (§11.0): a resolved actor would let an already-bound person
// chain-claim a second identity.
func TestHandleOperations_CredentialBinding_ClaimIdentityCarveOut(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "RAWCREDENTIAL00000000")

	var captured *processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = env
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureCredentialBindings(fakeCredentialResolver{identityKey: "vtx.identity.CLAIMEDBUSINESS0000", bound: true})

	w := doOperations(t, s, token, `{"operationType":"ClaimIdentity"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured.Actor != "vtx.identity.RAWCREDENTIAL00000000" {
		t.Fatalf("ClaimIdentity env.Actor = %q, want the raw credential (carve-out bypassed)", captured.Actor)
	}
}

// TestHandleOperations_CredentialBinding_ResolveError_FallsBackToRawActor
// proves a resolver failure (e.g. KV unreachable) fails OPEN to the raw
// credential actor rather than denying the request — acting as the raw
// credential is the documented deny-safe fallback (the actor simply lacks
// the claimed identity's business-scoped grants; it never gains more than
// it's entitled to).
func TestHandleOperations_CredentialBinding_ResolveError_FallsBackToRawActor(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "RAWCREDENTIAL00000000")

	var captured *processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = env
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureCredentialBindings(fakeCredentialResolver{err: errors.New("kv unreachable")})

	w := doOperations(t, s, token, `{"operationType":"RenewLease"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if captured.Actor != "vtx.identity.RAWCREDENTIAL00000000" {
		t.Fatalf("env.Actor = %q, want the raw credential on resolver error", captured.Actor)
	}
}

// --- ConfigureProvisioning / auto-provisioning pre-flight ------------------

// TestHandleOperations_ProvisioningPreflight_FirstTouch: a fresh actor's
// first request triggers ProvisionConsumerIdentity under the configured
// gatewayActorKey BEFORE the caller's own op, carrying targetActorKey (the
// verified actor) + consumerRoleKey.
func TestHandleOperations_ProvisioningPreflight_FirstTouch(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "FRESHACTOR000000000A")

	var captured []*processor.OperationEnvelope
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = append(captured, env)
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureProvisioning("vtx.identity.GATEWAY00000000000A", "vtx.role.CONSUMER0000000000A")

	w := doOperations(t, s, token, `{"operationType":"PingPlatform"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(captured) != 2 {
		t.Fatalf("submit called %d times, want 2 (provision then the real op)", len(captured))
	}

	prov := captured[0]
	if prov.OperationType != "ProvisionConsumerIdentity" {
		t.Fatalf("first submit OperationType = %q, want ProvisionConsumerIdentity", prov.OperationType)
	}
	if prov.Actor != "vtx.identity.GATEWAY00000000000A" {
		t.Fatalf("first submit Actor = %q, want the Gateway's own actor", prov.Actor)
	}
	// read-posture (script-read-posture-design §13): targetActorKey rides
	// OptionalReads (absence-tolerant — legitimately absent on first touch,
	// never Reads which would fault HydrationMiss); consumerRoleKey is a
	// pinned, always-live role vertex and rides Reads.
	if prov.ContextHint == nil {
		t.Fatal("first submit ContextHint = nil, want Reads=[consumerRoleKey] OptionalReads=[targetActorKey]")
	}
	if got := prov.ContextHint.Reads; len(got) != 1 || got[0] != "vtx.role.CONSUMER0000000000A" {
		t.Fatalf("first submit ContextHint.Reads = %v, want [vtx.role.CONSUMER0000000000A]", got)
	}
	if got := prov.ContextHint.OptionalReads; len(got) != 1 || got[0] != "vtx.identity.FRESHACTOR000000000A" {
		t.Fatalf("first submit ContextHint.OptionalReads = %v, want [vtx.identity.FRESHACTOR000000000A]", got)
	}
	var payload struct {
		TargetActorKey  string `json:"targetActorKey"`
		ConsumerRoleKey string `json:"consumerRoleKey"`
	}
	if err := json.Unmarshal(prov.Payload, &payload); err != nil {
		t.Fatalf("unmarshal provisioning payload: %v", err)
	}
	if payload.TargetActorKey != "vtx.identity.FRESHACTOR000000000A" {
		t.Fatalf("targetActorKey = %q, want the verified actor", payload.TargetActorKey)
	}
	if payload.ConsumerRoleKey != "vtx.role.CONSUMER0000000000A" {
		t.Fatalf("consumerRoleKey = %q, want the configured role key", payload.ConsumerRoleKey)
	}

	real := captured[1]
	if real.OperationType != "PingPlatform" || real.Actor != "vtx.identity.FRESHACTOR000000000A" {
		t.Fatalf("second submit = %+v, want the caller's own op under the verified actor", real)
	}
}

// TestHandleOperations_ProvisioningPreflight_CacheHit: a second request from
// the same actor skips the provisioning submit entirely.
func TestHandleOperations_ProvisioningPreflight_CacheHit(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "REPEATACTOR00000000")

	var opTypes []string
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		opTypes = append(opTypes, env.OperationType)
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureProvisioning("vtx.identity.GATEWAY00000000000A", "vtx.role.CONSUMER0000000000A")

	doOperations(t, s, token, `{"operationType":"PingPlatform"}`)
	doOperations(t, s, token, `{"operationType":"PingPlatform"}`)

	if len(opTypes) != 3 {
		t.Fatalf("submit calls = %v, want [ProvisionConsumerIdentity PingPlatform PingPlatform] (3 total)", opTypes)
	}
	if opTypes[0] != "ProvisionConsumerIdentity" || opTypes[1] != "PingPlatform" || opTypes[2] != "PingPlatform" {
		t.Fatalf("submit calls = %v, want provisioning only once (first request)", opTypes)
	}
}

// TestHandleOperations_ProvisioningPreflight_ToleratesFailure: a provisioning
// submit error never blocks the caller's own op — the real op's own
// capability check is the authority, not this best-effort pre-flight.
func TestHandleOperations_ProvisioningPreflight_ToleratesFailure(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "FAILACTOR000000000A")

	calls := 0
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		calls++
		if env.OperationType == "ProvisionConsumerIdentity" {
			return nil, errors.New("simulated NATS timeout")
		}
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureProvisioning("vtx.identity.GATEWAY00000000000A", "vtx.role.CONSUMER0000000000A")

	w := doOperations(t, s, token, `{"operationType":"PingPlatform"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s (a provisioning failure must not fail the real op)", w.Code, w.Body.String())
	}
	if calls != 2 {
		t.Fatalf("submit called %d times, want 2 (provisioning attempted, then the real op still ran)", calls)
	}
}

func TestHandleOperations_Unauthenticated_401(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := newTestServer(t, authn, func(context.Context, *processor.OperationEnvelope) (*processor.OperationReply, error) {
		t.Fatal("submit must not be called for an unauthenticated request")
		return nil, nil
	})

	w := doOperations(t, s, "", `{"operationType":"Ping"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandleOperations_InvalidSignature_401(t *testing.T) {
	priv := newTestKey(t)
	other := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	// signed by a DIFFERENT key than the one the Authenticator trusts under kid "k1".
	badToken := signToken(t, other, "k1", "someone")

	s := newTestServer(t, authn, func(context.Context, *processor.OperationEnvelope) (*processor.OperationReply, error) {
		t.Fatal("submit must not be called for an invalid signature")
		return nil, nil
	})

	w := doOperations(t, s, badToken, `{"operationType":"Ping"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandleOperations_Revoked_403(t *testing.T) {
	priv := newTestKey(t)
	v, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{"k1": &priv.PublicKey}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	authn := auth.NewAuthenticator(v, alwaysRevoked{})
	token := signToken(t, priv, "k1", "someone")

	s := newTestServer(t, authn, func(context.Context, *processor.OperationEnvelope) (*processor.OperationReply, error) {
		t.Fatal("submit must not be called for a revoked actor")
		return nil, nil
	})

	w := doOperations(t, s, token, `{"operationType":"Ping"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

type alwaysRevoked struct{}

func (alwaysRevoked) IsRevoked(context.Context, string) (bool, error) { return true, nil }

func TestHandleOperations_RejectedReply_MapsAuthDeniedTo403(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "someone")

	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{
			RequestID: env.RequestID,
			Status:    processor.ReplyStatusRejected,
			Error:     &processor.ReplyError{Code: processor.ErrCodeAuthDenied, Message: "no capability"},
		}, nil
	}
	s := newTestServer(t, authn, fake)

	w := doOperations(t, s, token, `{"operationType":"Ping"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
}

func TestHandleOperations_GETNotAllowed(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := newTestServer(t, authn, nil)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/operations", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleOperations_MalformedBody_400(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "someone")
	s := newTestServer(t, authn, func(context.Context, *processor.OperationEnvelope) (*processor.OperationReply, error) {
		t.Fatal("submit must not be called for a malformed body")
		return nil, nil
	})

	w := doOperations(t, s, token, `not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- CORS (real-actor-write-auth-e2e-design.md §3.1, browser-direct) -------

func TestCORS_Unconfigured_NoHeaders(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "someone")
	s := newTestServer(t, authn, func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/operations", strings.NewReader(`{"operationType":"Noop"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "http://localhost:7788")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty (CORS not configured)", got)
	}
}

func TestCORS_Preflight_AllowedOrigin(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := newTestServer(t, authn, nil)
	s.ConfigureCORS([]string{"http://localhost:7788"})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodOptions, "/v1/operations", nil)
	r.Header.Set("Origin", "http://localhost:7788")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:7788" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("Access-Control-Allow-Methods missing on preflight response")
	}
}

// TestCORS_Preflight_DisallowedOrigin_NoHeaders is the fail-closed check: a
// preflight from an origin NOT in the allow-list gets no CORS headers, so the
// browser blocks the follow-up write — never a wildcard fallback.
func TestCORS_Preflight_DisallowedOrigin_NoHeaders(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := newTestServer(t, authn, nil)
	s.ConfigureCORS([]string{"http://localhost:7788"})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodOptions, "/v1/operations", nil)
	r.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for a disallowed origin", got)
	}
	// Vary: Origin must still be set on a denied-CORS response — a shared
	// cache in front of this endpoint must never serve one origin's response
	// (headered or not) to another.
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want %q even for a disallowed origin", got, "Origin")
	}
}

func TestCORS_ActualRequest_AllowedOriginGetsHeaders(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "someone")
	s := newTestServer(t, authn, func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	})
	s.ConfigureCORS([]string{"http://localhost:7788"})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/operations", strings.NewReader(`{"operationType":"Noop"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "http://localhost:7788")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:7788" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}
}
