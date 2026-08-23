package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/gateway/rolesanchors"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// whoamiResponse is the GET /v1/actor body
// (multi-credential-identity-linking-design.md §3.5). Under Contract #11
// opaque-mode binding a browser cannot compute its own derived ActorID, so
// without this endpoint no FE can fill authContext.target for any
// self-scoped op (ClaimIdentity, InitiateCredentialLink,
// CompleteCredentialLink) or declare the credentialindex dedup read. Roles
// and Anchors report the resolved actor's role-derived grant keys and
// residence/workplace anchors (persona-worlds-design.md §10) — both omitted
// (omitempty) when no resolver is configured or the resolver reports
// nothing.
type whoamiResponse struct {
	ActorID              string                `json:"actorId"`
	ResolvedActorID      string                `json:"resolvedActorId"`
	CredentialIndexKey   string                `json:"credentialIndexKey"`
	ExistingIdentityHint bool                  `json:"existingIdentityHint,omitempty"`
	Roles                []string              `json:"roles,omitempty"`
	Anchors              []rolesanchors.Anchor `json:"anchors,omitempty"`
}

// handleWhoami implements GET /v1/actor. Runs the same authenticate →
// provision-if-needed → resolve pipeline handleOperations runs on every
// write — the natural "first authenticated call" for a fresh FE session —
// and reports the verified raw actor, its resolved business identity (if
// any credential binding exists), and the credentialindex key the caller
// would declare on a ClaimIdentity/CompleteCredentialLink dedup read. When a
// roles/anchors resolver is configured, the response additionally carries
// the resolved actor's role-derived grant keys and residence/workplace
// anchors (persona-worlds-design.md §10). Read-only at the platform level:
// the only write it can trigger is the shipped idempotent
// ProvisionConsumerIdentity op (P2-clean).
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
//
// Handles its own CORS/preflight (mirrors handleOperationStatus): a browser
// GET carrying `Authorization` triggers a preflight OPTIONS regardless of
// method, so this route answers the preflight before the GET-only guard —
// the natural browser-direct caller is an FE rendering the whoami hats
// (persona-worlds-design.md §10), which fails preflight without this.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if len(s.corsOrigins) > 0 {
		w.Header().Set("Vary", "Origin")
	}
	if origin := r.Header.Get("Origin"); s.allowedOrigin(origin) {
		writeCORSHeaders(w, origin, "GET, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
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

	// Deliberately best-effort here, unlike the write path: whoami is an
	// identity probe that commits nothing, so a failed pre-flight costs the
	// caller an unresolved actor, not a link written against an endpoint that
	// does not exist. The failure is already logged inside; a 503 on a
	// read-only probe would be worse than the answer it replaces.
	_ = s.provisionActorIfNeeded(ctx, actor.ActorID, actor.Issuer, actor.RawSubject)

	resolvedActor := s.resolveActor(ctx, actor.ActorID)

	resp := whoamiResponse{
		ActorID:            actor.ActorID,
		ResolvedActorID:    resolvedActor,
		CredentialIndexKey: CredentialIndexKey(actor.ActorID),
	}

	if s.rolesAnchors != nil {
		resp.Roles, resp.Anchors = s.rolesAnchors.Resolve(ctx, resolvedActor)
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
	indexKey, ok := EmailIdentityIndexKey(actor.VerifiedEmail)
	if !ok {
		return false
	}
	identityKey, found, err := s.identityIndexHint.Lookup(ctx, indexKey)
	if err != nil {
		s.logger.Warn("gateway: identity-index-hint lookup failed", "actor", actor.ActorID, "error", err)
		return false
	}
	return found && identityKey != actor.ActorID
}

// CredentialIndexKey returns the credentialindex vertex key that records which
// business identity an authenticated actor's credential is bound to. The
// gateway reports it on GET /v1/actor so a browser — which under Contract #11
// opaque-mode binding cannot compute its own ActorID, let alone a key derived
// from it — can declare the dedup read on ClaimIdentity /
// CompleteCredentialLink.
//
// The actor key is already a Contract #1 key, so it is hashed as-is: no
// normalization, matching identity-domain's credential_index_key.
func CredentialIndexKey(actorKey string) string {
	// derived-key: the credentialindex vertex key for an actor, the same value
	// identity-domain's `credential_index_key(actor_key)` computes
	// (packages/identity-domain/ddls.go). The gateway cannot defer to the
	// package: the package's version is Starlark SOURCE TEXT that only the
	// Processor can execute, and this key must be in a whoami response body
	// before any operation is submitted — there is nothing to defer TO at that
	// point in the request. TestCredentialIndexKeyAgreesWithIdentityDomain
	// (packages/identity-domain/gateway_agreement_test.go) drives a real
	// ceremony through the real Starlark and fails if the two stop matching.
	return "vtx.credentialindex." + substrate.SHA256NanoID(actorKey)
}

// EmailIdentityIndexKey normalizes a verified email claim the way
// identity-domain normalizes a contact and returns the identityindex vertex key
// an identity carrying that email is indexed under. ok is false when the claim
// normalizes to nothing, which is the same answer the package's normalizer
// gives (None) and is never a key.
//
// The caller must only ever pass an email the AUTH PLANE verified, never
// client-supplied input: the key is an existence oracle for whoever can choose
// its input.
func EmailIdentityIndexKey(rawEmail string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(rawEmail))
	if normalized == "" {
		return "", false
	}
	// derived-key: the identityindex vertex key for a normalized email, the
	// same value identity-domain's `identity_index_key("email",
	// normalize_email(raw))` computes (packages/identity-domain/ddls.go). The
	// gateway cannot defer to the package: the probe is a direct P5 lens read
	// answered inside GET /v1/actor with no operation in flight, and the
	// package's normalizer is Starlark source text with no Go entry point to
	// call. The normalization is duplicated here in Go, so
	// TestEmailIdentityIndexKeyAgreesWithIdentityDomain
	// (packages/identity-domain/gateway_agreement_test.go) drives raw inputs
	// through the real Starlark and fails the moment either side's trimming,
	// lowercasing or prefix changes.
	return "vtx.identityindex." + substrate.SHA256NanoID("email:"+normalized), true
}
