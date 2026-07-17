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

// TestGatewaySubmitter_CredentialRejectionIsTyped covers the two codes the
// Gateway answers with when it refuses the CREDENTIAL rather than the
// operation (gateway.go's authFailureStatus: 401 authentication failed / 403
// token revoked). Both must carry ErrCredentialRejected so a caller can tell
// a permanently-dead credential from an ordinary transport blip it should
// keep retrying — the distinction the §4.4 sign-out flow rides on.
func TestGatewaySubmitter_CredentialRejectionIsTyped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"revoked token (403)", http.StatusForbidden, `{"error":"token revoked"}`},
		{"failed authentication (401)", http.StatusUnauthorized, `{"error":"authentication failed"}`},
		{"denial with no error body", http.StatusForbidden, `nonsense-not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			g := &GatewaySubmitter{URL: srv.URL, Token: "a-dead-token"}
			reply, err := g.Submit(context.Background(), testEnv("req1"))
			require.Nil(t, reply)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrCredentialRejected)
		})
	}
}

// TestGatewaySubmitter_NonAuthFailureIsNotCredentialRejection is the other
// half of the guard: a 500 or a malformed body is transient — retrying is
// exactly right — so it must NOT be mistaken for a dead credential and
// sign the user out.
func TestGatewaySubmitter_NonAuthFailureIsNotCredentialRejection(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusBadRequest} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}))
		g := &GatewaySubmitter{URL: srv.URL, Token: "a-valid-token"}
		_, err := g.Submit(context.Background(), testEnv("req1"))
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrCredentialRejected, "status=%d", status)
		srv.Close()
	}
}

// TestDrain_CredentialRejectionPropagatesAndKeepsIntentQueued proves the
// contract the sign-out flow depends on end to end: the sentinel survives
// Drain's error wrapping (errors.Is through %w), and the refused intent is
// NOT dequeued — a re-login with a fresh credential must still be able to
// drain it.
func TestDrain_CredentialRejectionPropagatesAndKeepsIntentQueued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"token revoked"}`))
	}))
	defer srv.Close()

	st, ov := openTestStack(t)
	ag := New(&GatewaySubmitter{URL: srv.URL, Token: "a-dead-token"}, st, ov, nil, Config{})
	require.NoError(t, ag.Enqueue(testEnv("req1"), nil))

	err := ag.Drain(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCredentialRejected)

	recs, err := st.ListIntents()
	require.NoError(t, err)
	require.Len(t, recs, 1, "a credential-refused intent must stay queued for a later re-login to drain")
}

// TestGatewaySubmitter_RejectedReplyWith403IsNotCredentialRejection pins a
// non-obvious ordering the whole revocation story rests on. The Gateway
// answers 403 for BOTH a refused credential AND an ordinary AuthDenied
// rejected REPLY — including the routine async capability-projection race
// that callers retry (isTransientAuthLag). Submit only tells them apart by
// parsing a full OperationReply FIRST and returning it before it ever looks
// at the status code. If that order flips, /api/claim's retry loop goes dead
// and the drain loop starts firing false revocations at users whose write
// merely raced a projection.
func TestGatewaySubmitter_RejectedReplyWith403IsNotCredentialRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error: &processor.ReplyError{
				Code:    processor.ErrCodeAuthDenied,
				Message: "no capability entry",
				Details: map[string]any{"reason": "NoCapabilityEntry"},
			},
		})
	}))
	defer srv.Close()

	g := &GatewaySubmitter{URL: srv.URL, Token: "a-perfectly-valid-token"}
	reply, err := g.Submit(context.Background(), testEnv("req1"))
	require.NoError(t, err, "a rejected reply is an outcome, not a transport/credential failure")
	require.NotNil(t, reply)
	require.Equal(t, processor.ReplyStatusRejected, reply.Status)
	require.Equal(t, processor.ErrCodeAuthDenied, reply.Error.Code)
}
