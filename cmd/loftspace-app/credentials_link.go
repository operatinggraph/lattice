package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Linking an Nth sign-in method (multi-credential-identity-linking-design.md
// §3.2) is a two-step ceremony: the signed-in identity arms a link secret
// (InitiateCredentialLink, submitted browser-direct under its own session
// token like every other write), then a BRAND-NEW credential proves that
// secret (CompleteCredentialLink) — because the whole point is to bind a
// credential the identity is not currently holding.
//
// That second step is the one thing the browser cannot do for itself: it would
// need a token for a subject that is not the signed-in user, and this app holds
// no such minting surface (persona-worlds-design.md §6.1). So the ceremony runs
// SERVER-SIDE, mirroring cmd/facet/claim.go: the app mints a throwaway device
// credential of its own choosing and submits CompleteCredentialLink as that
// credential. The browser never sees a bearer token and never names a subject —
// it supplies only the link secret it just armed, and the identity being linked
// to is read from the session, never from the request body.

// maxLinkBodyBytes bounds POST /api/credentials/link/complete's request body —
// the same 1 MiB cap every JSON surface in this app accepts.
const maxLinkBodyBytes = 1 << 20

// linkRetryBackoffs is the bounded backoff for the fresh device credential's
// own capability-grant projection race (~3s total): the Gateway's
// provisionActorIfNeeded commits the credential's consumer-role grant to Core
// KV synchronously, but the CapabilityAuthorizer reads an asynchronously
// projected Capability Lens, so this immediately-following submit can arrive
// ahead of it.
var linkRetryBackoffs = []time.Duration{
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
}

// isTransientAuthLag reports whether a rejection is that known, expected
// projection race rather than a genuine, persistent denial.
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

// linkCompleteRequest is what the browser POSTs: the plaintext link secret it
// armed with InitiateCredentialLink. It carries NO identity — this endpoint
// takes its target from the session, so nothing a caller sends here can aim the
// link at another account. That is defense in depth on THIS surface, not a
// platform boundary: the CompleteCredentialLink script authorizes on the
// linkKey hash alone, so possession of a live secret is what ultimately
// protects the identity, and any consumer-role actor can submit the op
// browser-direct with an arbitrary targetIdentityKey.
type linkCompleteRequest struct {
	LinkKey string `json:"linkKey"`
}

// handleCompleteCredentialLink implements POST /api/credentials/link/complete.
func (s *server) handleCompleteCredentialLink(w http.ResponseWriter, r *http.Request) {
	if s.signer == nil {
		s.writeError(w, http.StatusNotFound, "linking a sign-in method is disabled (LOFTSPACE_APP_DEV_AUTH not set)")
		return
	}
	// Demo-persona posture: the world's people are fixed and pre-claimed, so the
	// ceremony's write surface stays closed — the same fail-closed shape as the
	// nil-signer gate above.
	if s.session.HasPersonaFence() {
		s.writeError(w, http.StatusNotFound, "linking a sign-in method is disabled (demo-persona deployment)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok || strings.TrimSpace(identityID) == "" {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	// Binding a new credential onto an identity is only ever the act of someone
	// who PROVED they are that identity. A boot-env fallback identity proves
	// nothing, so it must never reach this ceremony — otherwise any caller that
	// can reach the process could link a credential they control onto the
	// process's own identity.
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to link a sign-in method")
		return
	}
	var req linkCompleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLinkBodyBytes)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	linkKey := strings.TrimSpace(req.LinkKey)
	if linkKey == "" {
		s.writeError(w, http.StatusBadRequest, "linkKey is required")
		return
	}
	targetKey := "vtx.identity." + identityID

	deviceBareID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate device credential: "+err.Error())
		return
	}
	token, _, err := s.signer.Mint(deviceBareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint device credential: "+err.Error())
		return
	}
	deviceKey := "vtx.identity." + deviceBareID

	payload, err := json.Marshal(map[string]any{"targetIdentityKey": targetKey, "linkKey": linkKey})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal link payload: "+err.Error())
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
		OperationType: "CompleteCredentialLink",
		Class:         "identity",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: deviceKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{targetKey, targetKey + ".state"},
			// Absence-tolerant: an identity linking its FIRST extra credential
			// has no .credentialBinding yet. The credentialindex dedup probe is
			// deliberately NOT declared here — it is a class-(g) key that
			// identity-domain's own derive_reads computes from the actor
			// (Contract #2 §2.5), so declaring it would mean re-deriving, in a
			// second language, a key the package already produces.
			OptionalReads: []string{
				targetKey + ".linkKey",
				targetKey + ".credentialBinding",
			},
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	submitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: token}

	var reply *processor.OperationReply
	for attempt := 0; ; attempt++ {
		reply, err = submitter.Submit(ctx, env)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "linking failed: "+err.Error())
			return
		}
		if !isTransientAuthLag(reply) || attempt >= len(linkRetryBackoffs) {
			break
		}
		select {
		case <-time.After(linkRetryBackoffs[attempt]):
		case <-ctx.Done():
			s.writeError(w, http.StatusGatewayTimeout, "linking timed out waiting for the fresh credential's capability grant to project")
			return
		}
	}
	if reply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if reply.Error != nil {
			msg = string(reply.Error.Code) + ": " + reply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "linking rejected: "+msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"credentialKey": deviceKey})
}
