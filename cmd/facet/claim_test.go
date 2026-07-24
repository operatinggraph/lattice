package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/processor"
)

// TestClaim_ExemptFromSessionAtTheMux proves the exemption end-to-end through
// the real mux (registerRoutes → session.RequireSession), not just by calling
// handleClaim directly: a request with NO cookie and NO boot identity
// configured must still reach the claim handler rather than being redirected
// to /login or 401ed by RequireSession. Today's only other claim tests call
// srv.handleClaim in isolation, which cannot catch the exemption being
// dropped from ExtraExemptPaths.
func TestClaim_ExemptFromSessionAtTheMux(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), session: testSession(t, nil)}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(`{}`))
	mux.ServeHTTP(w, r)

	// No session at all resolves for this request (no cookie, no boot
	// identity), so RequireSession would otherwise redirect a browser
	// navigation or 401 an API call. Reaching handleClaim's own validation
	// (missing fields → 400) instead of either of those proves the exemption
	// held.
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NotEqual(t, http.StatusFound, w.Code)
	require.NotEqual(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "targetIdentityKey")
}

func TestHandleClaim_DisabledWithoutDevSigner(t *testing.T) {
	srv := &server{logger: slog.Default(), session: testSession(t, nil)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(`{}`))
	srv.handleClaim(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleClaim_RequiresBothFields(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), session: testSession(t, nil)}
	for _, body := range []string{`{}`, `{"targetIdentityKey":"vtx.identity.abc"}`, `{"claimKey":"secret"}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
		srv.handleClaim(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", body)
	}
}

func TestHandleClaim_MethodNotAllowed(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), session: testSession(t, nil)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/claim", nil)
	srv.handleClaim(w, r)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandleClaim_DisabledInDemoPersonaPosture(t *testing.T) {
	srv := &server{
		logger:    slog.Default(),
		devSigner: testDevSigner(t),
		session: testSession(t, func(c *appsession.Config) {
			c.Personas = []appsession.Persona{{ID: "aaaaaaaaaaaaaaaaaaaa", Label: "Riley"}}
		}),
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(`{"targetIdentityKey":"vtx.identity.abc","claimKey":"secret"}`))
	srv.handleClaim(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleClaim_SubmitsExpectedEnvelopeAndReturnsCredential(t *testing.T) {
	var gotAuth string
	var gotEnv struct {
		OperationType string                 `json:"operationType"`
		Class         string                 `json:"class"`
		Payload       json.RawMessage        `json:"payload"`
		Reads         []string               `json:"reads"`
		AuthContext   *processor.AuthContext `json:"authContext"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/operations", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotEnv))
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL, session: testSession(t, nil)}
	body := `{"targetIdentityKey":"vtx.identity.targetnano01","claimKey":"the-plaintext-secret"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
	srv.handleClaim(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, strings.HasPrefix(gotAuth, "Bearer "))
	require.Equal(t, "ClaimIdentity", gotEnv.OperationType)
	require.Equal(t, "identity", gotEnv.Class)
	require.NotNil(t, gotEnv.AuthContext)
	require.True(t, strings.HasPrefix(gotEnv.AuthContext.Target, "vtx.identity."))

	var payload struct {
		TargetIdentityKey string `json:"targetIdentityKey"`
		ClaimKey          string `json:"claimKey"`
	}
	require.NoError(t, json.Unmarshal(gotEnv.Payload, &payload))
	require.Equal(t, "vtx.identity.targetnano01", payload.TargetIdentityKey)
	require.Equal(t, "the-plaintext-secret", payload.ClaimKey)
	require.Contains(t, gotEnv.Reads, "vtx.identity.targetnano01")
	require.Contains(t, gotEnv.Reads, "vtx.identity.targetnano01.state")
	require.Contains(t, gotEnv.Reads, "vtx.identity.targetnano01.claimKey")

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "vtx.identity.targetnano01", resp["claimedIdentityKey"])
	require.True(t, strings.HasPrefix(resp["credentialKey"], "vtx.identity."))
	// The minted device credential must differ from the claimed target — a
	// throwaway credential, never the identity being claimed.
	require.NotEqual(t, resp["claimedIdentityKey"], resp["credentialKey"])
}

func TestHandleClaim_RetriesTransientAuthLagThenSucceeds(t *testing.T) {
	var calls int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			_ = json.NewEncoder(w).Encode(processor.OperationReply{
				Status: processor.ReplyStatusRejected,
				Error:  &processor.ReplyError{Code: processor.ErrCodeAuthDenied, Message: "no capability entry", Details: map[string]any{"reason": "NoCapabilityEntry"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL, session: testSession(t, nil)}

	orig := claimRetryBackoffs
	claimRetryBackoffs = []time.Duration{time.Millisecond}
	defer func() { claimRetryBackoffs = orig }()

	body := `{"targetIdentityKey":"vtx.identity.targetnano01","claimKey":"the-plaintext-secret"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
	srv.handleClaim(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 2, calls, "must retry once on a transient AuthDenied/NoCapabilityEntry rejection")
}

func TestHandleClaim_PersistentDenialDoesNotRetryForever(t *testing.T) {
	var calls int
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &processor.ReplyError{Code: processor.ErrCodeAuthDenied, Message: "no capability entry", Details: map[string]any{"reason": "NoCapabilityEntry"}},
		})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL, session: testSession(t, nil)}
	orig := claimRetryBackoffs
	claimRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { claimRetryBackoffs = orig }()

	body := `{"targetIdentityKey":"vtx.identity.targetnano01","claimKey":"the-plaintext-secret"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
	srv.handleClaim(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Equal(t, 3, calls, "one initial attempt plus exactly len(claimRetryBackoffs) retries, then give up")
}

func TestHandleClaim_GatewayRejectionSurfacesAsError(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &processor.ReplyError{Code: "ClaimKeyInvalid", Message: "claim key does not match"},
		})
	}))
	defer gw.Close()

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL, session: testSession(t, nil)}
	body := `{"targetIdentityKey":"vtx.identity.targetnano01","claimKey":"wrong-secret"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
	srv.handleClaim(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "ClaimKeyInvalid")
}
