package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

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
