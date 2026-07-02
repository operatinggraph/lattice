package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
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
