package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// whoamiResponse is the GET /v1/actor body
// (multi-credential-identity-linking-design.md §3.5). Under Contract #11
// opaque-mode binding a browser cannot compute its own derived ActorID, so
// without this endpoint no FE can fill authContext.target for any
// self-scoped op (ClaimIdentity, InitiateCredentialLink,
// CompleteCredentialLink) or declare the credentialindex dedup read.
type whoamiResponse struct {
	ActorID              string `json:"actorId"`
	ResolvedActorID      string `json:"resolvedActorId"`
	CredentialIndexKey   string `json:"credentialIndexKey"`
	ExistingIdentityHint bool   `json:"existingIdentityHint,omitempty"`
}

// handleWhoami implements GET /v1/actor. Runs the same authenticate →
// provision-if-needed → resolve pipeline handleOperations runs on every
// write — the natural "first authenticated call" for a fresh FE session —
// and reports the verified raw actor, its resolved business identity (if
// any credential binding exists), and the credentialindex key the caller
// would declare on a ClaimIdentity/CompleteCredentialLink dedup read.
// Read-only at the platform level: the only write it can trigger is the
// shipped idempotent ProvisionConsumerIdentity op (P2-clean).
//
// `?probe=1` additionally computes existingIdentityHint
// (multi-credential-identity-linking-design.md §3.4): a direct, P5-clean
// read against the identity-domain package's identityIndexHint lens bucket
// (internal/gateway/identityindexhint) — never through an operation reply,
// since Contract #2 §2.7's closed `response` schema permits only
// `primaryKey` and cannot carry read-derived data. Scoped to emails the
// caller provably controls: the hash is computed exclusively from the
// token's own verified `email`/`email_verified` claims, never from
// client-supplied input, so the probe cannot become an arbitrary-email
// existence oracle.
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

	resp := whoamiResponse{
		ActorID:            actor.ActorID,
		ResolvedActorID:    resolvedActor,
		CredentialIndexKey: "vtx.credentialindex." + substrate.SHA256NanoID(actor.ActorID),
	}

	if r.URL.Query().Get("probe") == "1" {
		resp.ExistingIdentityHint = s.probeExistingIdentityHint(ctx, actor)
	}

	writeJSON(w, http.StatusOK, resp)
}

// probeExistingIdentityHint answers §3.4's "an account matching your
// verified email may already exist" question. false covers every
// legitimately absent case — no configured resolver, no verified email
// claim, no matching index vertex, or a hit that resolves to the caller's
// own actor key — never a hydration fault or an error surfaced to the
// caller; a probe is a soft UX hint, not a security-relevant read.
func (s *Server) probeExistingIdentityHint(ctx context.Context, actor auth.VerifiedActor) bool {
	if s.identityIndexHint == nil || actor.VerifiedEmail == "" {
		return false
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(actor.VerifiedEmail))
	if normalizedEmail == "" {
		return false
	}
	indexKey := "vtx.identityindex." + substrate.SHA256NanoID("email:"+normalizedEmail)
	identityKey, found, err := s.identityIndexHint.Lookup(ctx, indexKey)
	if err != nil {
		s.logger.Warn("gateway: identity-index-hint lookup failed", "actor", actor.ActorID, "error", err)
		return false
	}
	return found && identityKey != actor.ActorID
}
