package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/operatinggraph/lattice/internal/appsession"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/identityceremony"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Linking a new sign-in method. What used to sit beside it here — the
// bound-credential LIST and the unlink submission — is gone, because both
// became descriptor-driven: the signInMethods pane declares the Protected
// table and its dispatch target, and UnlinkCredential's op descriptor
// declares how to submit against a row. This file kept them only while a
// bound credential had no projected row for a descriptor to name.
//
// Linking cannot follow yet, and its exemption says why: the ceremony arms a
// secret that has to reach a SECOND device and come back on a different op,
// which the descriptor vocabulary has no way to express. So it stays what it
// has been — ONE self-contained backend call running the
// Initiate/CompleteCredentialLink pair server-side against a throwaway device
// credential, mirroring claim.go, and honouring Facet's "the browser talks to
// no one but this Go host" invariant.

// pgxBeginner is the subset of *pgxpool.Pool a Protected read uses. The pane
// executor is its remaining consumer (pane.go).
type pgxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// mintLinkSecret generates the client-minted-secret idiom claim.go's
// ClaimIdentity carve-out and the InitiateCredentialLink/CompleteCredentialLink
// pair both use: a random plaintext Lattice never sees, only its sha256 hash.
func mintLinkSecret() (plaintext, hashHex string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plaintext = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plaintext))
	return plaintext, hex.EncodeToString(sum[:]), nil
}

// handleCredentialsLink implements POST /api/credentials/link — runs
// InitiateCredentialLink (as the session identity U) then CompleteCredentialLink
// (as a freshly minted throwaway device credential A2) back to back, mirroring
// the established linkNewCredential ceremony exactly, just server-side:
// Facet's browser never gets a Gateway URL or bearer token of its own
// (server.go's own invariant — same reasoning as claim.go).
func (s *server) handleCredentialsLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "linking is disabled (FACET_DEV_AUTH not set)")
		return
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	// Mutating a credential set is per-user by definition — the boot-env
	// fallback proves no identity, so it never reaches this (same reasoning
	// as handleCredentials).
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to manage sign-in methods")
		return
	}
	uKey := "vtx.identity." + identityID

	secret, hashHex, err := mintLinkSecret()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate link secret: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	uToken, _, err := s.devSigner.Mint(identityID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint session credential: "+err.Error())
		return
	}
	initiatePayload, err := json.Marshal(map[string]any{"linkKeyHash": hashHex})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal initiate payload: "+err.Error())
		return
	}
	initiateID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}
	initiateEnv := &processor.OperationEnvelope{
		RequestID:     initiateID,
		Lane:          processor.LaneDefault,
		OperationType: "InitiateCredentialLink",
		Class:         "identity",
		Payload:       initiatePayload,
		AuthContext:   &processor.AuthContext{Target: uKey},
		ContextHint:   identityceremony.InitiateCredentialLinkContextHint(uKey),
	}
	initiateSubmitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: uToken}
	initiateReply, err := initiateSubmitter.Submit(ctx, initiateEnv)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "initiate link failed: "+err.Error())
		return
	}
	if initiateReply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if initiateReply.Error != nil {
			msg = string(initiateReply.Error.Code) + ": " + initiateReply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "initiate link rejected: "+msg)
		return
	}

	deviceBareID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate device credential: "+err.Error())
		return
	}
	a2Key := "vtx.identity." + deviceBareID
	a2Token, _, err := s.devSigner.Mint(deviceBareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint device credential: "+err.Error())
		return
	}
	completePayload, err := json.Marshal(map[string]any{"targetIdentityKey": uKey, "linkKey": secret})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal complete payload: "+err.Error())
		return
	}
	completeID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}
	completeEnv := &processor.OperationEnvelope{
		RequestID:     completeID,
		Lane:          processor.LaneDefault,
		OperationType: "CompleteCredentialLink",
		Class:         "identity",
		Payload:       completePayload,
		AuthContext:   &processor.AuthContext{Target: a2Key},
		// authContext.target is the fresh credential A2, not uKey, so step 3's
		// scope=self gate says nothing about which identity the payload names.
		// The declared disposition is what keeps this ceremony's "no such
		// identity" indistinguishable from its "wrong secret" (NFR-S6) — see
		// identityceremony.CompleteCredentialLinkContextHint.
		ContextHint: identityceremony.CompleteCredentialLinkContextHint(uKey),
	}
	completeSubmitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: a2Token}

	var completeReply *processor.OperationReply
	for attempt := 0; ; attempt++ {
		completeReply, err = completeSubmitter.Submit(ctx, completeEnv)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "complete link failed: "+err.Error())
			return
		}
		if !isTransientAuthLag(completeReply) || attempt >= len(claimRetryBackoffs) {
			break
		}
		select {
		case <-time.After(claimRetryBackoffs[attempt]):
		case <-ctx.Done():
			s.writeError(w, http.StatusGatewayTimeout, "link timed out waiting for the fresh device credential's capability grant to project")
			return
		}
	}
	if completeReply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if completeReply.Error != nil {
			msg = string(completeReply.Error.Code) + ": " + completeReply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "complete link rejected: "+msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"linkedCredentialKey": a2Key})
}
