package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxClaimBodyBytes bounds POST /api/claim's request body, mirroring
// cmd/loftspace-app/server.go's requireBody 1 MiB cap.
const maxClaimBodyBytes = 1 << 20

// claimRetryBackoffs is isTransientAuthLag's bounded backoff schedule,
// ported verbatim from cmd/loftspace-app/web/app.js's retryBackoffsMs
// (~3s total): the freshly-minted device credential's ProvisionConsumerIdentity
// auto-provision commits to Core KV synchronously, but the CapabilityAuthorizer
// reads an async-projected Capability Lens, so the very next request (this
// ClaimIdentity submit) can race ahead of that projection.
var claimRetryBackoffs = []time.Duration{
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
}

// isTransientAuthLag mirrors app.js's helper of the same name: a rejection
// is the known, architecturally-expected async-projection race — not a
// genuine, persistent denial — only for these two AuthDenied reasons.
func isTransientAuthLag(reply *processor.OperationReply) bool {
	if reply == nil || reply.Status != processor.ReplyStatusRejected || reply.Error == nil {
		return false
	}
	if reply.Error.Code != processor.ErrCodeAuthDenied {
		return false
	}
	reason, _ := reply.Error.Details["reason"].(string)
	return reason == "NoCapabilityEntry" || reason == "OperationNotPermitted"
}

// claimRequest is what the browser POSTs to /api/claim (facet-app-ux.md
// §3.7's claim/link entry, Fire 3). Facet mints its own throwaway device
// credential server-side — the browser never sees a Gateway URL or a bearer
// token, matching server.go's "the browser only ever talks to this
// process's own localhost HTTP surface" invariant.
type claimRequest struct {
	TargetIdentityKey string `json:"targetIdentityKey"`
	ClaimKey          string `json:"claimKey"`
}

// handleClaim implements POST /api/claim: mints a bare device credential and
// submits ClaimIdentity through the Gateway as that credential — the
// raw-credential carve-out (gateway-claim-flow-identity-provisioning-
// design.md §11.0), mirroring cmd/loftspace-app/web/app.js's
// runClaimCeremony wire shape verbatim (authContext.target == the minted
// credential's own key; payload.targetIdentityKey == the identity being
// claimed, which is the op's real subject — see identity-domain's
// ClaimIdentity DDL ExpectedOutcome) including its bounded
// isTransientAuthLag retry for the fresh credential's own capability-grant
// projection race.
func (s *server) handleClaim(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "claim is disabled (FACET_DEV_AUTH not set)")
		return
	}
	// Demo-persona posture (FACET_DEMO_PERSONAS): the world's residents are
	// fixed and pre-claimed, so the ceremony's write surface stays closed —
	// same fail-closed shape as the nil-signer gate above.
	if s.session.HasPersonaFence() {
		s.writeError(w, http.StatusNotFound, "claim is disabled (demo-persona deployment)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req claimRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClaimBodyBytes)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	targetKey := strings.TrimSpace(req.TargetIdentityKey)
	claimKey := strings.TrimSpace(req.ClaimKey)
	if targetKey == "" || claimKey == "" {
		s.writeError(w, http.StatusBadRequest, "targetIdentityKey and claimKey are both required")
		return
	}

	deviceBareID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate device credential: "+err.Error())
		return
	}
	token, _, err := s.devSigner.Mint(deviceBareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint device credential: "+err.Error())
		return
	}
	deviceKey := "vtx.identity." + deviceBareID

	payload, err := json.Marshal(map[string]any{"targetIdentityKey": targetKey, "claimKey": claimKey})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal claim payload: "+err.Error())
		return
	}
	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Class:         "identity",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: deviceKey},
		ContextHint:   &processor.ContextHint{Reads: []string{targetKey, targetKey + ".state", targetKey + ".claimKey"}},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	submitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: token}

	var reply *processor.OperationReply
	for attempt := 0; ; attempt++ {
		reply, err = submitter.Submit(ctx, env)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "claim failed: "+err.Error())
			return
		}
		if !isTransientAuthLag(reply) || attempt >= len(claimRetryBackoffs) {
			break
		}
		select {
		case <-time.After(claimRetryBackoffs[attempt]):
		case <-ctx.Done():
			s.writeError(w, http.StatusGatewayTimeout, "claim timed out waiting for the fresh device credential's capability grant to project")
			return
		}
	}
	if reply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if reply.Error != nil {
			msg = string(reply.Error.Code) + ": " + reply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "claim rejected: "+msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"claimedIdentityKey": targetKey,
		"credentialKey":      deviceKey,
	})
}
