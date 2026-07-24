package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/operatinggraph/lattice/internal/appsession"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// edge-showcase-app-design.md §7.2 Inc 3: the Me screen's "manage sign-in
// methods" entry — mirrors cmd/loftspace-app/credentials.go's identityCredentialsRead
// Protected-lens read verbatim (same lens, same RLS-anchored query shape),
// adapted to Facet's session model (the caller is always the session
// identity, never a client-supplied actorId) and to Facet's "browser talks
// to no one but this Go host" invariant: unlike loftspace's browser-direct
// Initiate/CompleteCredentialLink pair, linking here is ONE self-contained
// backend call that runs both ops server-side, mirroring claim.go's own
// mint-a-throwaway-device-credential shape for the second (proving) leg.

// pgxBeginner is the subset of *pgxpool.Pool the protected read uses.
type pgxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// credentialEntry is one bound credential — {actorKey, boundAt}, the shape
// identity-domain's credentialBinding aspect stores per entry.
type credentialEntry struct {
	ActorKey string `json:"actorKey"`
	BoundAt  string `json:"boundAt"`
}

// credentialBindingData is the decrypted shape of the identityCredentialsRead
// Secure Lens's `binding` column. A pre-Fire-2 record with no `credentials`
// array falls back to the singular actorKey/boundAt fields.
type credentialBindingData struct {
	ActorKey    string            `json:"actorKey"`
	BoundAt     string            `json:"boundAt"`
	Credentials []credentialEntry `json:"credentials"`
}

func (d credentialBindingData) entries() []credentialEntry {
	if len(d.Credentials) > 0 {
		return d.Credentials
	}
	if d.ActorKey == "" {
		return nil
	}
	return []credentialEntry{{ActorKey: d.ActorKey, BoundAt: d.BoundAt}}
}

// selectIdentityCredentialsSQL mirrors cmd/loftspace-app/credentials.go's
// query verbatim — no auth WHERE, RLS (the identity's own NanoID as
// authz_anchor) scopes it to the caller's txn-local lattice.actor_id.
const selectIdentityCredentialsSQL = `
SELECT entity_key, binding
FROM read_identity_credentials
WHERE entity_key = $1`

// queryIdentityCredentials runs the protected read for one identity inside a
// per-request transaction with a txn-local actor session variable. actorID
// must be the caller's own bare identity NanoID — the only identity_id RLS
// ever lets it see.
func queryIdentityCredentials(ctx context.Context, pool pgxBeginner, actorID string) ([]credentialEntry, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, false, err
	}

	var entityKey string
	var bindingJSON []byte
	err = tx.QueryRow(ctx, selectIdentityCredentialsSQL, "vtx.identity."+actorID).Scan(&entityKey, &bindingJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, tx.Commit(ctx)
		}
		return nil, false, err
	}

	var data credentialBindingData
	if err := json.Unmarshal(bindingJSON, &data); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return data.entries(), true, nil
}

// handleCredentials implements GET /api/credentials — the session identity's
// own "which sign-in methods are linked to me" list, served from the
// PROTECTED identityCredentialsRead Secure Lens. Only ever the session's own
// row (RLS); no client-supplied identity param.
func (s *server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	// credentialBinding is a SENSITIVE aspect (Contract #3 §3.10), so this
	// surface serves only a caller who PROVED which identity they are. The
	// boot-env fallback proves nothing — it hands the process's identity to
	// anyone who connects — and a deployment that binds off-loopback with a
	// boot identity would otherwise expose an identity's bound credentials
	// to any network caller. RLS would still confine the row to the boot
	// identity; it cannot tell that the caller isn't that identity.
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to manage sign-in methods")
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set FACET_PG_DSN and ensure Postgres + the identity-domain protected lens are up)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	entries, found, err := queryIdentityCredentials(ctx, s.pgPool, identityID)
	if err != nil {
		s.logger.Error("facet: read protected identity credentials", "identityId", identityID, "err", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected identity-credentials model")
		return
	}
	if !found {
		entries = []credentialEntry{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"credentials": entries, "count": len(entries)})
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
// cmd/loftspace-app/web/app.js's linkNewCredential exactly, just server-side:
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
		ContextHint:   &processor.ContextHint{Reads: []string{uKey, uKey + ".state"}},
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
		ContextHint: &processor.ContextHint{
			Reads: []string{uKey, uKey + ".state"},
			OptionalReads: []string{
				uKey + ".linkKey", uKey + ".credentialBinding",
				"vtx.credentialindex." + substrate.SHA256NanoID(a2Key),
			},
		},
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

// unlinkCredentialRequest is what the browser POSTs to /api/credentials/unlink.
type unlinkCredentialRequest struct {
	CredentialActorKey string `json:"credentialActorKey"`
}

// handleCredentialsUnlink implements POST /api/credentials/unlink — submits
// UnlinkCredential as the session identity U (self-scope). The platform
// itself refuses removing the last remaining credential.
func (s *server) handleCredentialsUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "unlinking is disabled (FACET_DEV_AUTH not set)")
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
	var req unlinkCredentialRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClaimBodyBytes)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	credentialActorKey := strings.TrimSpace(req.CredentialActorKey)
	if credentialActorKey == "" {
		s.writeError(w, http.StatusBadRequest, "credentialActorKey is required")
		return
	}
	uKey := "vtx.identity." + identityID

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	token, _, err := s.devSigner.Mint(identityID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint session credential: "+err.Error())
		return
	}
	payload, err := json.Marshal(map[string]any{"credentialActorKey": credentialActorKey})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal unlink payload: "+err.Error())
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
		OperationType: "UnlinkCredential",
		Class:         "identity",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: uKey},
		ContextHint: &processor.ContextHint{
			Reads:         []string{uKey, uKey + ".state"},
			OptionalReads: []string{uKey + ".credentialBinding"},
		},
	}
	submitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: token}
	reply, err := submitter.Submit(ctx, env)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "unlink failed: "+err.Error())
		return
	}
	if reply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if reply.Error != nil {
			msg = string(reply.Error.Code) + ": " + reply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "unlink rejected: "+msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
