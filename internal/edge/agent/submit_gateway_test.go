package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/processor"
)

func TestGatewaySubmitter_AcceptedReplyRoundTrips(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/operations", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		var req gatewayOperationRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		_ = json.NewEncoder(w).Encode(processor.OperationReply{
			RequestID: req.RequestID, Status: processor.ReplyStatusAccepted, Decision: "committed",
		})
	}))
	defer srv.Close()

	g := &GatewaySubmitter{URL: srv.URL, Token: "a-valid-token"}
	reply, err := g.Submit(context.Background(), testEnv("req1"))
	require.NoError(t, err)
	require.Equal(t, processor.ReplyStatusAccepted, reply.Status)
	require.Equal(t, "Bearer a-valid-token", gotAuth)
}

func TestGatewaySubmitter_NoTokenErrorsWithoutCallingGateway(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	g := &GatewaySubmitter{URL: srv.URL}
	_, err := g.Submit(context.Background(), testEnv("req1"))
	require.Error(t, err)
	require.False(t, called, "submit must not call the gateway with no credential")
}

// TestGatewaySubmitter_RevokedTokenDenied is the unit-level half of the
// Gate-3 vector ("a revoked JWT ... cannot submit an intent"): a 401/403
// from the Gateway must surface as a plain Go error, never a fabricated
// OperationReply, so Drain leaves the intent queued rather than treating a
// denial as a terminal outcome. See edge_gateway_submit_gate3_test.go
// (internal/gateway package) for the full proof against a real Authenticator.
func TestGatewaySubmitter_RevokedTokenDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "actor revoked"})
	}))
	defer srv.Close()

	g := &GatewaySubmitter{URL: srv.URL, Token: "a-revoked-token"}
	reply, err := g.Submit(context.Background(), testEnv("req1"))
	require.Error(t, err)
	require.Nil(t, reply)
	require.Contains(t, err.Error(), "actor revoked")
}
