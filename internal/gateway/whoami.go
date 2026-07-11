package gateway

import (
	"context"
	"net/http"

	"github.com/asolgan/lattice/internal/substrate"
)

// whoamiResponse is the GET /v1/actor body
// (multi-credential-identity-linking-design.md §3.5). Under Contract #11
// opaque-mode binding a browser cannot compute its own derived ActorID, so
// without this endpoint no FE can fill authContext.target for any
// self-scoped op (ClaimIdentity, InitiateCredentialLink,
// CompleteCredentialLink) or declare the credentialindex dedup read.
type whoamiResponse struct {
	ActorID            string `json:"actorId"`
	ResolvedActorID    string `json:"resolvedActorId"`
	CredentialIndexKey string `json:"credentialIndexKey"`
}

// handleWhoami implements GET /v1/actor. Runs the same authenticate →
// provision-if-needed → resolve pipeline handleOperations runs on every
// write — the natural "first authenticated call" for a fresh FE session —
// and reports the verified raw actor, its resolved business identity (if
// any credential binding exists), and the credentialindex key the caller
// would declare on a ClaimIdentity/CompleteCredentialLink dedup read.
// Read-only at the platform level: the only write it can trigger is the
// shipped idempotent ProvisionConsumerIdentity op (P2-clean).
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	s.metrics.readsTotal.Add(1)

	token, ok := bearerToken(r)
	if !ok {
		s.metrics.authFailuresTotal.Add(1)
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization: Bearer header")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.reqTimeout)
	defer cancel()

	actor, err := s.authn.Authenticate(ctx, token)
	if err != nil {
		s.metrics.authFailuresTotal.Add(1)
		status, msg := mapAuthError(err)
		writeError(w, status, msg)
		return
	}

	s.provisionActorIfNeeded(ctx, actor.ActorID, actor.Issuer, actor.RawSubject)

	resolvedActor := s.resolveActor(ctx, actor.ActorID)

	writeJSON(w, http.StatusOK, whoamiResponse{
		ActorID:            actor.ActorID,
		ResolvedActorID:    resolvedActor,
		CredentialIndexKey: "vtx.credentialindex." + substrate.SHA256NanoID(actor.ActorID),
	})
}
