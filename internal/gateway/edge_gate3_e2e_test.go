package gateway

import (
	"context"
	"crypto"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/agent"
	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/processor"
)

// edge-lattice-full-design.md §5's Gate-3 vector for the Edge's write path:
// "a revoked JWT (D1 revocation) cannot submit an intent". Fire 3 of
// per-identity-nats-subscribe-acl-design.md already proved live NATS
// subscription revocation against the real dev stack
// (scripts/verify-edge-revocation-e2e.go); that script cannot cover
// submission because, until this fire, internal/edge/agent submitted
// directly to core-operations, bypassing the Gateway (and its
// Authenticator) entirely. This is the submission-side proof: a real
// gateway.Server, wrapping a real auth.Authenticator (the identical
// production Verify+revocation-check path), fronting an actual
// internal/edge/agent.Agent configured with a GatewaySubmitter — not a
// fake HTTP handler standing in for the Gateway.

func newEdgeGate3Server(t *testing.T, authn *auth.Authenticator, submit submitFunc) *httptest.Server {
	t.Helper()
	s := newTestServer(t, authn, submit)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newEdgeAgentStack(t *testing.T, gatewayURL, token string) (*agent.Agent, *overlay.Overlay, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ov := overlay.New(st)
	sub := &agent.GatewaySubmitter{URL: gatewayURL, Token: token}
	return agent.New(sub, st, ov, nil, agent.Config{}), ov, st
}

const edgeGate3Key = "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"

func edgeGate3Env(requestID string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "UpdateLease",
		Payload:       json.RawMessage(`{}`),
	}
}

// TestEdgeGate3_ValidTokenSubmitsThroughGateway proves the accept path: a
// live, unrevoked EDGE_TOKEN reaches submit (here a fake stub standing in
// for the Processor — the Gateway's own auth+stamping behavior is the
// object under test) and the reply flows back through Drain.
func TestEdgeGate3_ValidTokenSubmitsThroughGateway(t *testing.T) {
	priv := newTestKey(t)
	var captured *processor.OperationEnvelope
	srv := newEdgeGate3Server(t, testAuthenticator(t, priv, "k1"), func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		captured = env
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	})

	token := signToken(t, priv, "k1", "bViQrN4kLpXe8tGmZoAy")
	ag, ov, st := newEdgeAgentStack(t, srv.URL, token)
	require.NoError(t, ov.Apply(edgeGate3Key, "req1", []byte(`{"rent":150}`), false))
	require.NoError(t, ag.Enqueue(edgeGate3Env("req1"), []string{edgeGate3Key}))

	require.NoError(t, ag.Drain(context.Background()))

	require.NotNil(t, captured, "the gateway must have relayed the intent to submit")
	require.Equal(t, "vtx.identity.bViQrN4kLpXe8tGmZoAy", captured.Actor,
		"the Gateway must stamp the verified subject, never a client-asserted actor")

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents, "an accepted intent must be dequeued")
}

// TestEdgeGate3_RevokedTokenNeverSubmits proves the deny path: a revoked
// actor's EDGE_TOKEN is denied by the Gateway's Authenticator BEFORE any
// envelope reaches submit — Drain must surface a transport-level error
// (not a fabricated reply) and leave the intent queued, exactly like being
// offline, so the node never silently discards the user's edit.
func TestEdgeGate3_RevokedTokenNeverSubmits(t *testing.T) {
	priv := newTestKey(t)
	v, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{"k1": &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{"k1": {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	require.NoError(t, err)
	authn := auth.NewAuthenticator(v, alwaysRevoked{})

	called := false
	srv := newEdgeGate3Server(t, authn, func(context.Context, *processor.OperationEnvelope) (*processor.OperationReply, error) {
		called = true
		return nil, nil
	})

	token := signToken(t, priv, "k1", "bWkNv6oRpDzJhTyLqXeS")
	ag, ov, st := newEdgeAgentStack(t, srv.URL, token)
	require.NoError(t, ov.Apply(edgeGate3Key, "req1", []byte(`{"rent":150}`), false))
	require.NoError(t, ag.Enqueue(edgeGate3Env("req1"), []string{edgeGate3Key}))

	drainErr := ag.Drain(context.Background())
	require.Error(t, drainErr, "a revoked actor's intent must not silently succeed or vanish")
	require.False(t, called, "the Processor-side submit stub must never be reached for a revoked actor")

	intents, listErr := st.ListIntents()
	require.NoError(t, listErr)
	require.Len(t, intents, 1, "the denied intent must remain queued, not discarded")
}
