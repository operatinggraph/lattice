// Package gateway is the external write-path translator (design:
// gateway-external-trust-boundary-design.md, Fire 1). It terminates
// external HTTP requests, verifies the caller's IdP-signed JWT with the
// already-built internal/gateway/auth Authenticator, and STAMPS the verified
// actor into the operation envelope before publishing to core-operations —
// making env.Actor unforgeable for external actors (F2-A: the Processor
// trusts env.Actor because the NATS account-level write restriction
// (#75 Fire 2, live) lets only the Gateway's NATS user publish
// core-operations).
//
// Internal service actors (Loom / Weaver / Bridge / object-store-manager /
// admin tooling) keep their sanctioned direct-submit path and never go
// through this package.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/gateway/rolesanchors"
	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxBodyBytes bounds the request body the Gateway will read — an
// unauthenticated (pre-auth-check) or authenticated caller cannot force
// unbounded memory use. 1 MiB matches the NATS max_payload default a
// Core-KV mutation is bounded by anyway (Contract #3 §3.9.1).
const maxBodyBytes = 1 << 20

// submitFunc is the side effect of publishing an envelope to core-operations
// and waiting for the Processor's reply. The default implementation wraps
// output.SubmitOp against a live *substrate.Conn; tests inject a fake so the
// stamping/auth logic is provable without a NATS connection.
type submitFunc func(ctx context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error)

// opStatusFunc is the side effect of asking the Processor-hosted
// lattice.op.status RPC whether an operation landed (op-status-read-surface-
// design.md Fire 2). The default implementation requests over a live
// *substrate.Conn; tests inject a fake so handleOperationStatus is provable
// without a NATS connection.
type opStatusFunc func(ctx context.Context, requestID string) (*opstatus.Response, error)

// Server is the Gateway's HTTP handler set. Its only mutable state is
// Metrics, which is safe for concurrent use — the Server itself is safe for
// concurrent use.
type Server struct {
	authn      *auth.Authenticator
	submit     submitFunc
	opStatus   opStatusFunc
	logger     Logger
	reqTimeout time.Duration
	metrics    *Metrics

	// pgPool + readModels back the read-path front (Fire 3, ConfigureReadModels).
	// Both are nil until configured — a Server with neither still serves
	// /v1/operations exactly as before.
	pgPool     PgPool
	readModels map[string]ReadModel

	// gatewayActorKey + consumerRoleKey + provisioned back the first-
	// authenticated-touch auto-provisioning pre-flight (ConfigureProvisioning,
	// real-actor-write-auth-e2e-design.md Phase 1). gatewayActorKey empty
	// until configured — a Server with it unset still serves /v1/operations
	// exactly as before.
	gatewayActorKey string
	consumerRoleKey string
	provisioned     *provisionedCache

	// corsOrigins backs ConfigureCORS (real-actor-write-auth-e2e-design.md
	// §3.1, browser-direct topology): nil/empty until configured, in which
	// case /v1/operations serves exactly as before (no CORS headers) — a
	// browser calling cross-origin is refused by the browser itself, same as
	// today.
	corsOrigins map[string]struct{}

	// credentialBindings backs ConfigureCredentialBindings (the claim-flow
	// shared-seam amendment, R1): nil until configured, in which case every
	// request acts as its raw authenticated actor exactly as before this
	// mechanism existed.
	credentialBindings CredentialBindingResolver

	// identityIndexHint backs ConfigureIdentityIndexHint (the provision-time
	// identityindex probe, multi-credential-identity-linking-design.md §3.4):
	// nil until configured, in which case whoami `?probe=1` always omits
	// existingIdentityHint (best-effort — absence never denies anything).
	identityIndexHint IdentityIndexHintResolver

	// rolesAnchors backs ConfigureRolesAnchors (persona-worlds-design.md §10
	// Fire P1): nil until configured, in which case whoami omits both the
	// roles and anchors fields (best-effort — absence never denies anything).
	rolesAnchors RolesAnchorsResolver
}

// CredentialBindingResolver is the credential→identity resolution surface
// the write path (handleOperations) and the Gateway's own read-model routes
// (read.go) consult after authentication
// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5 R1).
// internal/gateway/credentialbinding provides the substrate-backed
// implementation.
type CredentialBindingResolver interface {
	// Resolve looks up rawActorID's claimed business identity. bound=false
	// (no error) means unclaimed — the caller should act as rawActorID.
	Resolve(ctx context.Context, rawActorID string) (identityKey string, bound bool, err error)
}

// IdentityIndexHintResolver is the P5-clean read seam whoami's `?probe=1`
// path consults (multi-credential-identity-linking-design.md §3.4).
// internal/gateway/identityindexhint provides the substrate-backed
// implementation reading the identity-domain package's identityIndexHint
// lens bucket.
type IdentityIndexHintResolver interface {
	// Lookup answers whether a live identityindex vertex exists at indexKey
	// and which identity it points at. found=false (no error) means no hint.
	Lookup(ctx context.Context, indexKey string) (identityKey string, found bool, err error)
}

// RolesAnchorsResolver is the whoami roles[]/anchors[] read seam
// (persona-worlds-design.md §10 Fire P1). internal/gateway/rolesanchors
// provides the substrate-backed implementation reading the rbac-domain
// capabilityRoles projection and the identity-domain package's
// identityAnchors lens bucket.
type RolesAnchorsResolver interface {
	// Resolve reports actorKey's role-derived grant keys and residence/
	// workplace anchors, degrading to empty results on any absence or error.
	Resolve(ctx context.Context, actorKey string) (roles []string, anchors []rolesanchors.Anchor)
}

// provisionedCacheMaxEntries caps provisionedCache's memory: it holds one
// entry per distinct authenticated actor for the life of the process, with
// no TTL, so an unbounded map would be a slow leak on a long-lived,
// internet-facing Gateway. On overflow the whole set is cleared rather than
// evicted piecemeal (simpler than LRU; a false miss just re-runs the
// idempotent op, so a full-clear burst of re-provisioning is harmless).
const provisionedCacheMaxEntries = 100_000

// provisionedCache is a bounded, pure latency optimization: a false miss
// just re-runs the idempotent ProvisionConsumerIdentity op, so correctness
// never depends on it, and it starts empty on every restart by design (a
// cold Gateway harmlessly re-provisions already-provisioned actors once).
// Mirrors issueCache's convention (health.go).
type provisionedCache struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newProvisionedCache() *provisionedCache {
	return &provisionedCache{seen: make(map[string]struct{})}
}

func (c *provisionedCache) has(actorKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.seen[actorKey]
	return ok
}

func (c *provisionedCache) add(actorKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) >= provisionedCacheMaxEntries {
		c.seen = make(map[string]struct{})
	}
	c.seen[actorKey] = struct{}{}
}

// Logger is the minimal logging surface Server needs (satisfied by *slog.Logger).
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// nopLogger discards everything; used when no logger is supplied.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// defaultReqTimeout bounds how long a single /v1/operations call waits for
// the Processor's reply before returning 202 for async reconciliation
// (mirrors the bridge's async-reply posture, design §3.1).
const defaultReqTimeout = 8 * time.Second

// NewServer builds a Server that authenticates with authn and publishes
// operations over conn (the Gateway's own NATS identity — #75's
// "only the Gateway may publish core-operations" grant). authn and conn must
// be non-nil. metrics may be nil (a fresh, unshared counter set is used) —
// pass a shared *Metrics to have a Heartbeater report the same counters.
func NewServer(authn *auth.Authenticator, conn *substrate.Conn, metrics *Metrics, logger Logger) *Server {
	if logger == nil {
		logger = nopLogger{}
	}
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &Server{
		authn: authn,
		submit: func(ctx context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
			return output.SubmitOp(ctx, conn, env)
		},
		opStatus: func(ctx context.Context, requestID string) (*opstatus.Response, error) {
			return requestOpStatus(ctx, conn, requestID)
		},
		logger:     logger,
		reqTimeout: defaultReqTimeout,
		metrics:    metrics,
	}
}

// opStatusTimeout bounds the Gateway's wait on the lattice.op.status RPC,
// independent of the responder's own handler timeout — a wedged or
// unreachable Processor must not hang the HTTP request indefinitely. Mirrors
// internal/bridge's resultAlreadyLandedTimeout (op-status-read-surface-
// design.md Fire 1).
const opStatusTimeout = 5 * time.Second

// requestOpStatus asks the Processor-hosted lattice.op.status RPC whether
// requestID landed (op-status-read-surface-design.md Fire 2), mirroring
// internal/bridge/dispatch.go's resultAlreadyLanded.
func requestOpStatus(ctx context.Context, conn *substrate.Conn, requestID string) (*opstatus.Response, error) {
	reqBody, err := json.Marshal(opstatus.Request{RequestID: requestID})
	if err != nil {
		return nil, fmt.Errorf("gateway: marshal op-status request: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, opStatusTimeout)
	defer cancel()
	msg, err := conn.NATS().RequestWithContext(rctx, opstatus.Subject, reqBody)
	if err != nil {
		return nil, fmt.Errorf("gateway: request op-status %q: %w", requestID, err)
	}
	var resp opstatus.Response
	if uerr := json.Unmarshal(msg.Data, &resp); uerr != nil {
		return nil, fmt.Errorf("gateway: parse op-status reply for %q: %w", requestID, uerr)
	}
	return &resp, nil
}

// ConfigureProvisioning enables the first-authenticated-touch auto-
// provisioning pre-flight (real-actor-write-auth-e2e-design.md Phase 1,
// gateway-claim-flow-identity-provisioning-design.md §3.4): before
// submitting the caller's own op, handleOperations submits
// ProvisionConsumerIdentity under gatewayActorKey for any verified actor not
// yet seen. gatewayActorKey is bootstrap.GatewayIdentityKey; consumerRoleKey
// is "vtx.role."+pkgmgr.RoleID("identity-domain","consumer") — the caller
// resolves both so this package stays free of a pkgmgr/bootstrap import.
// Call before RegisterRoutes. Unconfigured (either argument empty), the
// pre-flight is skipped — mirrors ConfigureReadModels' additive-capability
// pattern; a Server with neither still serves /v1/operations exactly as
// before.
func (s *Server) ConfigureProvisioning(gatewayActorKey, consumerRoleKey string) {
	s.gatewayActorKey = gatewayActorKey
	s.consumerRoleKey = consumerRoleKey
	s.provisioned = newProvisionedCache()
}

// ConfigureCredentialBindings enables credential→identity resolution on both
// the write path (handleOperations) and the Gateway's own read-model routes
// (read.go): after authentication, a raw credential actor (A) that has
// claimed a business identity (U) acts as U instead — every subsequent
// mutation/read anchors on the identity the business links point at, not the
// bare credential (gateway-claim-flow-identity-provisioning-design.md
// §11.0). Unconfigured (nil resolver, the default), every actor acts as
// itself exactly as before this method existed.
func (s *Server) ConfigureCredentialBindings(r CredentialBindingResolver) {
	s.credentialBindings = r
}

// ConfigureIdentityIndexHint wires the identity-index-hint read seam whoami's
// `?probe=1` path consults (multi-credential-identity-linking-design.md
// §3.4). Unconfigured (nil resolver, the default), `?probe=1` always omits
// existingIdentityHint from the response.
func (s *Server) ConfigureIdentityIndexHint(r IdentityIndexHintResolver) {
	s.identityIndexHint = r
}

// ConfigureRolesAnchors wires the whoami roles[]/anchors[] read seam
// (persona-worlds-design.md §10 Fire P1). Unconfigured (nil resolver, the
// default), the response omits both fields.
func (s *Server) ConfigureRolesAnchors(r RolesAnchorsResolver) {
	s.rolesAnchors = r
}

// claimIdentityOperationType is the one carve-out resolveActor's caller must
// apply: ClaimIdentity binds a credential to a business identity by hashing
// op.actor, so it must see the raw credential — a resolved actor would let
// an already-bound person chain-claim a second identity
// (gateway-claim-flow-identity-provisioning-design.md §11.0 "one carve-out").
const claimIdentityOperationType = "ClaimIdentity"

// completeCredentialLinkOperationType extends the raw-credential carve-out
// to the second credential-binding op: CompleteCredentialLink hashes
// op.actor exactly like ClaimIdentity does (the new credential A2 proving
// control of a link secret), so it must see the raw A2, never a resolved
// identity (multi-credential-identity-linking-design.md §5, Contract #11
// §11.4's carve-out generalizes from ClaimIdentity alone to this pair).
const completeCredentialLinkOperationType = "CompleteCredentialLink"

// rawCredentialCarveOut is the set of operation types resolveActor's caller
// must skip (submit under the raw authenticated actor, never the resolved
// business identity) — every op whose script hashes op.actor to derive or
// check a credentialindex key.
var rawCredentialCarveOut = map[string]struct{}{
	claimIdentityOperationType:          {},
	completeCredentialLinkOperationType: {},
}

// resolveActor consults the credential-bindings resolver (if configured) to
// turn a raw credential actor into its claimed business identity. An
// unconfigured resolver, a miss (no binding yet — the CDC-lag window between
// a live claim and this Gateway's local bucket observing it), or a lookup
// error all resolve to rawActorID unchanged: acting as the raw credential is
// the documented deny-safe fallback — a person who hasn't claimed, or whose
// claim hasn't propagated yet, only ever sees/writes their own data, never
// more than they're entitled to.
func (s *Server) resolveActor(ctx context.Context, rawActorID string) string {
	if s.credentialBindings == nil {
		return rawActorID
	}
	identityKey, bound, err := s.credentialBindings.Resolve(ctx, rawActorID)
	if err != nil {
		s.logger.Error("gateway: credential-binding resolve failed", "actor", rawActorID, "error", err)
		return rawActorID
	}
	if !bound {
		return rawActorID
	}
	return identityKey
}

// ConfigureCORS enables CORS handling on POST /v1/operations for the given
// exact set of allowed Origin values (real-actor-write-auth-e2e-design.md
// §3.1): the browser-direct topology has the browser call the Gateway
// cross-origin from the vertical app's own origin, so the preflight OPTIONS
// request and the actual response must carry Access-Control-Allow-* headers
// naming that origin. origins is matched by exact string equality (scheme +
// host + port) — never a wildcard: a bearer-token API should not train
// callers to expect Access-Control-Allow-Origin: *. Unconfigured (nil/empty),
// CORS stays off and a cross-origin browser call is refused by the browser
// itself, exactly as before this method existed.
func (s *Server) ConfigureCORS(origins []string) {
	s.corsOrigins = make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			s.corsOrigins[o] = struct{}{}
		}
	}
}

// allowedOrigin reports whether origin is in the configured CORS allow-list.
func (s *Server) allowedOrigin(origin string) bool {
	if origin == "" || len(s.corsOrigins) == 0 {
		return false
	}
	_, ok := s.corsOrigins[origin]
	return ok
}

// writeCORSHeaders sets the Access-Control-Allow-* headers for a request from
// origin (already confirmed allowed by the caller) on the given methods.
// Vary: Origin marks the response as origin-dependent so a shared cache never
// serves it cross-origin.
func writeCORSHeaders(w http.ResponseWriter, origin, methods string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", methods)
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	h.Set("Access-Control-Max-Age", "600")
}

// RegisterRoutes mounts the Gateway's HTTP surface on mux — the write-path
// keystone plus one GET /v1/<name> route per read-model configured via
// ConfigureReadModels (call it before RegisterRoutes; routes are mounted
// once, from whatever is configured at this call).
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/operations", s.handleOperations)
	mux.HandleFunc("/v1/operations/{requestId}", s.handleOperationStatus)
	mux.HandleFunc("/v1/actor", s.handleWhoami)
	for name, model := range s.readModels {
		if !ValidReadModelName(name) {
			s.logger.Error("gateway: skipping invalid read-model name", "name", name)
			continue
		}
		mux.HandleFunc("/v1/"+name, s.handleReadModel(name, model))
	}
}

// operationRequest is the POST /v1/operations body. There is no `actor`
// field by construction — the client cannot set the trusted actor even if it
// sends one in the raw JSON (unknown fields are dropped by json.Unmarshal);
// the verified actor is the ONLY source of env.Actor (design §3.1, A2).
type operationRequest struct {
	RequestID     string                      `json:"requestId,omitempty"`
	Lane          string                      `json:"lane,omitempty"`
	OperationType string                      `json:"operationType"`
	Class         string                      `json:"class,omitempty"`
	Payload       json.RawMessage             `json:"payload,omitempty"`
	Reads         []string                    `json:"reads,omitempty"`
	OptionalReads []string                    `json:"optionalReads,omitempty"`
	Enumerations  []processor.EnumerationHint `json:"enumerations,omitempty"`
	AuthContext   *processor.AuthContext      `json:"authContext,omitempty"`
	ContextHint   *operationRequestContext    `json:"contextHint,omitempty"`
}

// operationRequestContext lets a client declare Contract #2 §2.5 reads
// either as a bare `reads`/`optionalReads`/`enumerations` array or nested
// under `contextHint.{reads,optionalReads,enumerations}` — both forms are
// accepted so a caller mirroring the OperationEnvelope wire shape works
// unmodified.
type operationRequestContext struct {
	Reads         []string                    `json:"reads,omitempty"`
	OptionalReads []string                    `json:"optionalReads,omitempty"`
	Enumerations  []processor.EnumerationHint `json:"enumerations,omitempty"`
}

// errorResponse is the JSON body of a non-2xx reply.
type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleOperations implements POST /v1/operations — the write-path keystone.
// Bearer-authenticates the caller, strips any client-supplied actor, stamps
// the verified actor, and publishes the resulting envelope.
func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	if len(s.corsOrigins) > 0 {
		// Vary: Origin regardless of the allow/deny outcome — a shared cache in
		// front of this endpoint must never serve one origin's CORS-headered
		// response to another.
		w.Header().Set("Vary", "Origin")
	}
	if origin := r.Header.Get("Origin"); s.allowedOrigin(origin) {
		writeCORSHeaders(w, origin, "POST, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	s.metrics.requestsTotal.Add(1)

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

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
		return
	}

	var req operationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	resolvedActor := actor.ActorID
	if _, rawCredential := rawCredentialCarveOut[req.OperationType]; !rawCredential {
		resolvedActor = s.resolveActor(ctx, actor.ActorID)
	}

	env, err := buildEnvelope(req, resolvedActor, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	reply, err := s.submit(ctx, env)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// The Processor may still commit; the caller polls Core KV for
			// read-your-own-writes using requestId (mirrors the bridge's
			// async-reply posture, design §3.1).
			writeJSON(w, http.StatusAccepted, map[string]string{"requestId": env.RequestID})
			return
		}
		s.logger.Error("gateway: submit op failed", "requestId", env.RequestID, "error", err)
		writeError(w, http.StatusBadGateway, "submit operation: "+err.Error())
		return
	}
	s.metrics.opsSubmittedTotal.Add(1)

	writeJSON(w, replyStatusCode(reply), reply)
}

// handleOperationStatus implements GET /v1/operations/{requestId} — turns the
// write path's 202 fallback (a Processor reply timeout) into a real
// read-your-own-writes poll for browser actors, backed by the
// lattice.op.status RPC (op-status-read-surface-design.md Fire 2). Every
// authenticated actor may query any requestId — the RPC exposes op metadata
// (status/class/timestamps), never payloads, so its blast radius is a
// traffic oracle at worst (design §5, accepted for Fire 1; per-actor scoping
// is a named follow-on, not required here).
//
// Handles its own CORS/preflight (mirrors handleOperations): a browser GET
// carrying `Authorization` triggers a preflight OPTIONS regardless of
// method, so this route is registered method-agnostic and dispatches here.
func (s *Server) handleOperationStatus(w http.ResponseWriter, r *http.Request) {
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
	s.metrics.requestsTotal.Add(1)

	token, ok := bearerToken(r)
	if !ok {
		s.metrics.authFailuresTotal.Add(1)
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization: Bearer header")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.reqTimeout)
	defer cancel()

	if _, err := s.authn.Authenticate(ctx, token); err != nil {
		s.metrics.authFailuresTotal.Add(1)
		status, msg := mapAuthError(err)
		writeError(w, status, msg)
		return
	}

	requestID := r.PathValue("requestId")
	if !isBareRequestID(requestID) {
		writeError(w, http.StatusBadRequest, "requestId must be a bare id")
		return
	}

	resp, err := s.opStatus(ctx, requestID)
	if err != nil {
		s.logger.Error("gateway: op-status RPC failed", "requestId", requestID, "error", err)
		writeError(w, http.StatusBadGateway, "op status lookup failed")
		return
	}
	if resp.Error != "" {
		writeError(w, http.StatusBadGateway, resp.Error)
		return
	}
	if !resp.Found {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// isBareRequestID mirrors internal/opstatus's own isBareID guard (this
// package's path segment is caller-controlled exactly like the RPC's own
// requestId payload) — reject before spending a round trip to the Processor
// on input the responder would refuse anyway.
func isBareRequestID(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, ".*> \t\n")
}

// provisionActorIfNeeded submits ProvisionConsumerIdentity under the
// Gateway's own actor for a verified actor not yet in the in-memory
// provisioned set. A no-op when ConfigureProvisioning was never called.
// Tolerates any submit error or non-accepted reply — this is a best-effort
// pre-flight, not the source of truth on capability; the caller's own op
// (submitted right after, under its real, unforgeable actor) re-checks
// capability independently and is denied on its own merits if provisioning
// truly never lands. Failing here would make the whole request depend on an
// op whose only job is convenience, so a failure just means "try again next
// request" (logged, not surfaced to the HTTP caller).
// idpIssuer/idpSubject are the verifier's raw provenance (VerifiedActor.Issuer/
// .RawSubject, Contract #11 §3.3) — non-empty only for an opaque-mode token
// (a real external IdP); a nanoid-mode dev token carries no iss claim, so
// both are empty and the payload omits them (ProvisionConsumerIdentity treats
// the pair as optional). When present, the script writes them onto a fresh
// identity as the .idpBinding aspect — the audit answer to "which IdP account
// is this identity?"
func (s *Server) provisionActorIfNeeded(ctx context.Context, actorID, idpIssuer, idpSubject string) {
	if s.gatewayActorKey == "" || s.consumerRoleKey == "" || s.provisioned.has(actorID) {
		return
	}
	payloadFields := map[string]string{
		"targetActorKey":  actorID,
		"consumerRoleKey": s.consumerRoleKey,
	}
	if idpIssuer != "" {
		payloadFields["idpIssuer"] = idpIssuer
		payloadFields["idpSubject"] = idpSubject
	}
	payload, err := json.Marshal(payloadFields)
	if err != nil {
		s.logger.Error("gateway: marshal provisioning payload", "actor", actorID, "error", err)
		return
	}
	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.logger.Error("gateway: generate provisioning requestId", "actor", actorID, "error", err)
		return
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         s.gatewayActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "identity",
		Payload:       payload,
		// read-posture: targetActorKey is class (d) — legitimately does not
		// exist yet on the fresh-actor path, so it rides OptionalReads
		// (absence-tolerant), never Reads (which faults HydrationMiss on the
		// legitimately-absent key and would wedge every first-touch
		// request). consumerRoleKey is class (a) — a pinned, always-live
		// role vertex; its absence is a wiring fault.
		ContextHint: &processor.ContextHint{
			Reads:         []string{s.consumerRoleKey},
			OptionalReads: []string{actorID},
		},
	}
	reply, err := s.submit(ctx, env)
	if err != nil {
		s.logger.Warn("gateway: auto-provision consumer identity: submit failed, will retry next request",
			"actor", actorID, "error", err)
		return
	}
	if reply.Status != processor.ReplyStatusAccepted && reply.Status != processor.ReplyStatusDuplicate {
		s.logger.Warn("gateway: auto-provision consumer identity: not accepted, will retry next request",
			"actor", actorID, "status", reply.Status)
		return
	}
	s.provisioned.add(actorID)
}

// bearerToken extracts the token from a well-formed `Authorization: Bearer
// <token>` header. Missing, empty, or any other scheme fails closed.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}

// mapAuthError maps an auth.Authenticator failure to an HTTP status + a
// caller-safe message. Every branch denies; the distinction is purely for
// caller diagnostics (a revoked token is a stronger signal than an ordinary
// verification failure — design §6). The message is a FIXED string, never
// err.Error(): auth.Verifier's current sentinel set carries no token/key
// internals, but passing the raw error through would silently start leaking
// them the moment a future change to that package wraps a lower-level error —
// a caller-safe message is a property of this boundary, not a property this
// package should have to keep re-verifying against auth's internals.
func mapAuthError(err error) (int, string) {
	if errors.Is(err, auth.ErrTokenRevoked) {
		return http.StatusForbidden, "token revoked"
	}
	return http.StatusUnauthorized, "authentication failed"
}

// buildEnvelope turns an operationRequest into a processor.OperationEnvelope,
// stamping actorID (the VERIFIED actor — never anything from req) as
// env.Actor. requestId is generated when the client omits one.
func buildEnvelope(req operationRequest, actorID string, now time.Time) (*processor.OperationEnvelope, error) {
	if strings.TrimSpace(req.OperationType) == "" {
		return nil, fmt.Errorf("operationType is required")
	}
	lane := processor.Lane(req.Lane)
	if req.Lane == "" {
		lane = processor.LaneDefault
	}
	if !laneValid(lane) {
		return nil, fmt.Errorf("lane %q is not a recognized enum value (default|meta|urgent|system)", req.Lane)
	}
	payload := req.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("payload is not valid JSON")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		var err error
		requestID, err = substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("generate requestId: %w", err)
		}
	}

	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          lane,
		OperationType: req.OperationType,
		Actor:         actorID,
		SubmittedAt:   now.UTC().Format(time.RFC3339),
		Class:         req.Class,
		Payload:       payload,
		AuthContext:   req.AuthContext,
	}
	reads := cleanKeys(req.Reads, contextHintReads(req))
	optionalReads := cleanKeys(req.OptionalReads, contextHintOptionalReads(req))
	enumerations := req.Enumerations
	if len(enumerations) == 0 {
		enumerations = contextHintEnumerations(req)
	}
	if len(reads) > 0 || len(optionalReads) > 0 || len(enumerations) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads, OptionalReads: optionalReads, Enumerations: enumerations}
	}
	return env, nil
}

func contextHintReads(req operationRequest) []string {
	if req.ContextHint == nil {
		return nil
	}
	return req.ContextHint.Reads
}

func contextHintOptionalReads(req operationRequest) []string {
	if req.ContextHint == nil {
		return nil
	}
	return req.ContextHint.OptionalReads
}

func contextHintEnumerations(req operationRequest) []processor.EnumerationHint {
	if req.ContextHint == nil {
		return nil
	}
	return req.ContextHint.Enumerations
}

// cleanKeys accepts either wire form (bare array or the nested contextHint
// array), trims, dedups, and drops empties.
func cleanKeys(bare, nested []string) []string {
	raw := bare
	if len(raw) == 0 {
		raw = nested
	}
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func laneValid(lane processor.Lane) bool {
	switch lane {
	case processor.LaneDefault, processor.LaneMeta, processor.LaneUrgent, processor.LaneSystem:
		return true
	}
	return false
}

// replyStatusCode maps a Contract #2 §2.4 OperationReply to an HTTP status.
// accepted/duplicate are both a successful outcome from the caller's
// perspective (an operation with this requestId is now committed); rejected
// maps by error code so an auth failure reads 4xx and an infra failure 5xx.
func replyStatusCode(reply *processor.OperationReply) int {
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return http.StatusOK
	case processor.ReplyStatusRejected:
		return rejectedStatusCode(reply)
	default:
		return http.StatusOK
	}
}

func rejectedStatusCode(reply *processor.OperationReply) int {
	if reply.Error == nil {
		return http.StatusBadRequest
	}
	switch reply.Error.Code {
	case processor.ErrCodeAuthDenied, processor.ErrCodeLaneUnauthorized, processor.ErrCodeAuthContextMismatch:
		return http.StatusForbidden
	case processor.ErrCodeInternalError, processor.ErrCodeAuthInfrastructureFailure:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}
