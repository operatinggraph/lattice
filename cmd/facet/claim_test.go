package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/processor"
)

// testDevSigner builds a devSigner with a throwaway key (never the checked-in
// shared dev key — this test never verifies the JWT, only that handleClaim
// builds the right envelope and maps the fake Gateway's replies correctly;
// the ClaimIdentity mechanics themselves are proven once, against a real
// Processor, by packages/identity-domain/claim_test.go).
func testDevSigner(t *testing.T) *devSigner {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}
}

func TestHandleClaim_DisabledWithoutDevSigner(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(`{}`))
	srv.handleClaim(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleClaim_RequiresBothFields(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t)}
	for _, body := range []string{`{}`, `{"targetIdentityKey":"vtx.identity.abc"}`, `{"claimKey":"secret"}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
		srv.handleClaim(w, r)
		require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", body)
	}
}

func TestHandleClaim_MethodNotAllowed(t *testing.T) {
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/claim", nil)
	srv.handleClaim(w, r)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
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

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
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

func TestSetupDevSigner_DisabledByDefault(t *testing.T) {
	signer, err := setupDevSigner(slog.Default(), true)
	require.NoError(t, err)
	require.Nil(t, signer)
}

func TestSetupDevSigner_RefusesNonLoopbackBind(t *testing.T) {
	t.Setenv("FACET_DEV_AUTH", "1")
	signer, err := setupDevSigner(slog.Default(), false)
	require.Error(t, err)
	require.Nil(t, signer)
	require.Contains(t, err.Error(), "loopback")
}

func TestIsLoopbackHost(t *testing.T) {
	require.True(t, isLoopbackHost("127.0.0.1"))
	require.True(t, isLoopbackHost("localhost"))
	require.True(t, isLoopbackHost("::1"))
	require.False(t, isLoopbackHost("0.0.0.0"))
	require.False(t, isLoopbackHost("10.0.0.5"))
	require.False(t, isLoopbackHost(""))
	require.Equal(t, "127.0.0.1", hostOf("127.0.0.1:7810"))
	require.Equal(t, "", hostOf(":7810"))
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

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}

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

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
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

	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), gatewayURL: gw.URL}
	body := `{"targetIdentityKey":"vtx.identity.targetnano01","claimKey":"wrong-secret"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/claim", strings.NewReader(body))
	srv.handleClaim(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.Contains(t, w.Body.String(), "ClaimKeyInvalid")
}
