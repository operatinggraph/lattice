// Package natsauth is the NATS auth-callout responder for the Edge sync
// plane (per-identity-nats-subscribe-acl-design.md, Fire 1 — "the callout
// boundary").
//
// The shipped #75 transport-auth posture (nats-account-write-restriction-
// design.md) is a flat NATS `authorization` block of 16 static, per-component
// NKey users, every one of which still holds `subscribe: [">"]` — v1
// explicitly declined subscribe lockdown. Every NATS connection that is NOT
// one of those 16 static users is now delegated to this responder via the
// server's `auth_callout` mechanism (fork A of the design): the server sends
// the CONNECT's options (including the client's bearer token, ADR-26) to
// this responder over the well-known request-reply subject
// ($SYS.REQ.USER.AUTH), and this responder answers with a signed, scoped,
// EXPIRING per-connection user JWT — or a signed denial.
//
// Handle verifies the bearer token with the SAME internal/gateway/auth
// Authenticator every other external Lattice surface uses (Contract #11 —
// "the shared verifier", now extended to the transport), resolves the
// credential to its claimed business identity via
// internal/gateway/credentialbinding (A→U, deny-safe on a miss), and issues
// a permission set that allows the identity to subscribe ONLY its own
// `lattice.sync.user.<U>` Personal-Lens delta subject (+ its own scoped
// JetStream consumer/ack/inbox namespace) — closing the one open EDGE.3 gate
// leg (edge-lattice-full-design.md §7).
//
// Every subject embedded in the issued permission set derives from the
// VERIFIED actor id, never from a client-asserted field (not the CONNECT
// username, not the resolved identity's own self-report) — the confinement
// invariant the design's adversarial pass names explicitly (§3.2, §12).
package natsauth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// AuthCalloutSubject is the NATS server's well-known auth-callout
// request-reply subject (ADR-26; pinned `server/auth_callout.go`
// `AuthCalloutSubject = "$SYS.REQ.USER.AUTH"`). Duplicated as a literal
// rather than importing nats-server's `server` package into a production
// binary for one constant.
const AuthCalloutSubject = "$SYS.REQ.USER.AUTH"

// AuthRequestXKeyHeader is the header name the NATS server (2.14 pin,
// `server/auth_callout.go` `AuthRequestXKeyHeader = "Nats-Server-Xkey"`)
// attaches to every callout request when the account's `auth_callout.xkey`
// is configured — the value is the server's own ephemeral per-instance
// public curve key, sealed against per §3.1a. Duplicated as a literal for
// the same reason AuthCalloutSubject is.
const AuthRequestXKeyHeader = "Nats-Server-Xkey"

// DefaultMaxAuthzTTL bounds the issued authorization's lifetime (design
// §3.5): the server disconnects the client at expiry, and reconnect re-runs
// the callout against the live revocation bucket — so this is also the
// worst-case revocation latency for an already-open subscription.
const DefaultMaxAuthzTTL = 15 * time.Minute

// MinAuthzTTL floor-clamps an operator-supplied TTL (env-tunable per the
// design's EDGE_SYNC_AUTHZ_TTL) so a misconfiguration cannot shrink the
// window to something that thrashes reconnects.
const MinAuthzTTL = time.Minute

// syncSubjectPrefix is the Personal Lens's sync-subject prefix
// (subjects.PersonalSync's "lattice.sync.user" convention,
// internal/refractor/subjects/subjects.go). Duplicated as a literal rather
// than imported to avoid pulling internal/refractor into the Gateway's
// dependency graph for one constant — the two are pinned together by the
// natsperm conformance vectors (§8), which would fail loudly on drift.
const syncSubjectPrefix = "lattice.sync.user"

// inboxPrefix mirrors cmd/edge's nats.CustomInboxPrefix("_INBOX.edge.<U>")
// (design §3.3) — the per-identity reply-inbox namespace.
const inboxPrefix = "_INBOX.edge"

// controlRPCs grants the design §3.3 control-RPC subjects — Fire 2
// (per-identity-nats-subscribe-acl-design.md, ratified 2026-07-10) landed
// the §3.4 server-side identity-binding override
// (internal/refractor/control/service.go's dispatchEndpoint binds
// body.IdentityID to the verified actor for register/deregister/hydrate/
// sessionkey, rejecting a mismatching client-asserted value) but left this
// transport grant closed — hydrate in particular was additionally
// unreachable by ANY actor until the matching internal/controlauth ops-table
// entry and packages/control-authz manifest grant were added alongside this
// list. sessionkey (edge-lattice-full-design.md §3.6, EDGE.4) joined the
// same §3.4 binding and the same three-places-in-lockstep pattern.
//
// Both halves must land together: the transport grant here (which subjects
// a connection may even reach) and the capability-plane grant in
// packages/control-authz/manifest.yaml (ctrl.refractor.{register,deregister,
// hydrate,sessionkey} scope=any → consumer) are independently necessary and
// jointly sufficient. Granting only this list without the capability-plane
// grant is a no-op (the subject is reachable but CapabilityKVChecker.Authorize
// still denies every actor). Granting only the capability-plane entry without
// opening these subjects reopens the original gap in the other direction:
// nothing ever lets the connection reach the subject to exercise the grant
// it holds. The §3.4 binding is what makes granting scope=any broadly (to
// every identity via the consumer role) safe — it unconditionally confines
// each op's effect to the caller's own identity regardless of capability
// scope.
var controlRPCs = []string{
	"lattice.ctrl.refractor.personal.register",
	"lattice.ctrl.refractor.personal.deregister",
	"lattice.ctrl.refractor.personal.hydrate",
	"lattice.ctrl.refractor.personal.sessionkey",
	"lattice.ctrl.refractor.personal.syncgap",
}

// Authenticator is the verify+revocation seam. *auth.Authenticator
// (internal/gateway/auth) satisfies it; the interface keeps this package
// unit-testable without a live substrate.
type Authenticator interface {
	Authenticate(ctx context.Context, tokenString string) (auth.VerifiedActor, error)
}

// IdentityResolver is the credential→business-identity resolution seam.
// *credentialbinding.Resolver (internal/gateway/credentialbinding) satisfies
// it.
type IdentityResolver interface {
	Resolve(ctx context.Context, actorID string) (identityKey string, bound bool, err error)
}

// Responder issues per-connection NATS user JWTs for delegated (untrusted)
// CONNECTs — design §3.2. Safe for concurrent use: every field is either
// immutable after construction or itself concurrency-safe (Authenticator /
// IdentityResolver), so one Responder serves a queue-grouped subscription
// across every gateway instance (design §3.2 "Multi-instance").
type Responder struct {
	authn   Authenticator
	resolve IdentityResolver
	issuer  nkeys.KeyPair
	maxTTL  time.Duration
	now     func() time.Time
}

// NewResponder builds a Responder. issuer must be an account key pair whose
// PUBLIC key matches the server config's `auth_callout.issuer` — construction
// fails closed if issuer cannot produce a valid public ACCOUNT key (a
// caller-contract violation: the wrong key kind would sign JWTs the server
// can never validate, so surfacing it at construction beats a runtime-only
// failure). maxTTL <= 0 uses DefaultMaxAuthzTTL; a positive value under
// MinAuthzTTL is floor-clamped, never rejected.
func NewResponder(authn Authenticator, resolve IdentityResolver, issuer nkeys.KeyPair, maxTTL time.Duration) (*Responder, error) {
	if authn == nil {
		return nil, fmt.Errorf("natsauth: authn is required")
	}
	if resolve == nil {
		return nil, fmt.Errorf("natsauth: resolve is required")
	}
	if issuer == nil {
		return nil, fmt.Errorf("natsauth: issuer key pair is required")
	}
	pub, err := issuer.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("natsauth: issuer public key: %w", err)
	}
	if !nkeys.IsValidPublicAccountKey(pub) {
		return nil, fmt.Errorf("natsauth: issuer must be an account key pair, got a key of a different kind (%q)", pub)
	}
	switch {
	case maxTTL <= 0:
		maxTTL = DefaultMaxAuthzTTL
	case maxTTL < MinAuthzTTL:
		maxTTL = MinAuthzTTL
	}
	return &Responder{authn: authn, resolve: resolve, issuer: issuer, maxTTL: maxTTL, now: time.Now}, nil
}

// Handle answers one auth-callout request (the decoded `$SYS.REQ.USER.AUTH`
// message payload) and returns the signed response token to publish back —
// the transport binding (subscribing, unwrapping xkey sealing, publishing
// the reply) lives in the caller (cmd/gateway), keeping this function pure
// request-in/token-out and unit-testable without a live NATS connection.
//
// The returned token is ALWAYS a validly-signed AuthorizationResponseClaims,
// even for a denial: a denial sets `.Error` (never a bare/omitted response),
// so every deny is auditable server-side ($SYS auth-error events) instead of
// looking like a callout timeout. The only non-nil error return is a
// request-decode or response-encode failure — a transport/protocol fault,
// not a model of "this connection is unauthorized" (that is always a
// successfully-encoded denial token).
func (r *Responder) Handle(ctx context.Context, requestToken string) (string, error) {
	req, err := jwt.DecodeAuthorizationRequestClaims(requestToken)
	if err != nil {
		return "", fmt.Errorf("natsauth: decode authorization request: %w", err)
	}

	resp := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	resp.Audience = req.Server.ID

	userJWT, err := r.authorize(ctx, req)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Jwt = userJWT
	}

	token, err := resp.Encode(r.issuer)
	if err != nil {
		return "", fmt.Errorf("natsauth: encode authorization response: %w", err)
	}
	return token, nil
}

// UnsealRequest decrypts a sealed auth-callout request payload (design
// §3.1a: "with xkey set the payload is sealed to the service's curve key").
// xkp is this responder's own curve keypair (the private half of the conf's
// `auth_callout.xkey`); serverXkey is the sending server's ephemeral public
// xkey, read by the caller from the AuthRequestXKeyHeader (the transport
// binding — subscribing, header extraction, publishing the reply — lives in
// the caller, cmd/gateway, per Handle's own doc comment). Day-one xkey is
// mandatory (design §7: "enabled from day one, not a deferred hardening
// pass"), so an empty serverXkey is refused rather than falling back to
// treating data as plaintext — a missing header on a connection this
// responder's own conf always seals to is a protocol anomaly, not a
// legitimate unencrypted caller.
func UnsealRequest(data []byte, serverXkey string, xkp nkeys.KeyPair) (string, error) {
	if serverXkey == "" {
		return "", fmt.Errorf("natsauth: missing %s header on a callout request (xkey is mandatory)", AuthRequestXKeyHeader)
	}
	plain, err := xkp.Open(data, serverXkey)
	if err != nil {
		return "", fmt.Errorf("natsauth: open sealed request: %w", err)
	}
	return string(plain), nil
}

// SealResponse encrypts a response token back to the server's ephemeral
// public xkey — the reply-path half of the same sealed exchange
// UnsealRequest opens, using the SAME serverXkey UnsealRequest extracted
// from the request (the server decrypts a non-JWT-prefixed reply with its
// own keypair against the account's configured xkey, pinned
// `server/auth_callout.go`'s decodeResponse).
func SealResponse(token, serverXkey string, xkp nkeys.KeyPair) ([]byte, error) {
	sealed, err := xkp.Seal([]byte(token), serverXkey)
	if err != nil {
		return nil, fmt.Errorf("natsauth: seal response: %w", err)
	}
	return sealed, nil
}

// authorize runs the verify → resolve → template → sign pipeline. A non-nil
// error is always a deny reason (never a transport fault — Handle wraps this
// as the response's .Error), so every branch here fails closed by
// construction: the zero value of "no permissions granted" is never reached
// by accident, only by an explicit early return.
func (r *Responder) authorize(ctx context.Context, req *jwt.AuthorizationRequestClaims) (string, error) {
	token := req.ConnectOptions.Token
	if token == "" {
		return "", fmt.Errorf("no bearer token presented")
	}

	actor, err := r.authn.Authenticate(ctx, token)
	if err != nil {
		// Deliberately generic: the caller-visible deny reason never echoes
		// the verifier's specific sentinel (expired vs. unknown-kid vs.
		// revoked) — same posture as every other Lattice authentication
		// boundary, which does not hand an unauthenticated caller a signal
		// to iterate against.
		return "", fmt.Errorf("authentication failed")
	}

	identityFull := actor.ActorID
	if bound, boundKey, rerr := r.resolveIdentity(ctx, actor.ActorID); rerr == nil && bound {
		identityFull = boundKey
	}

	identityID := strings.TrimPrefix(identityFull, auth.IdentityKeyPrefix)
	if identityID == identityFull || identityID == "" {
		// Defense in depth (design §3.2's "re-asserts validateToken before
		// templating"): the verifier and the resolver both only ever
		// produce vtx.identity.<id> keys by construction, but a future
		// resolver bug (or a resolved binding to a non-identity vertex)
		// must fail closed here rather than template a malformed subject.
		return "", fmt.Errorf("resolved identity is not a well-formed identity key")
	}
	// NanoID-alphabet membership, not just the generic subject-safe check
	// (an adversarial pass finding, MEDIUM): the confinement argument that
	// a fabricated device name can only rename a durable INSIDE the
	// identity's own family (design §3.3) holds only because identityID can
	// never itself contain '-' — true for both live verifier paths
	// (SHA256NanoID / IsValidNanoID already enforce the alphabet), but the
	// RESOLVED (credential-bindings) path sources identityID from an
	// externally-materialized document that validateSubjectToken's generic
	// character class (rejects only `.`/`*`/`>`/whitespace) would not catch
	// if it ever carried a non-canonical value. Asserting the actual
	// Contract #1 alphabet here — not just "subject-safe" — is what makes
	// the durable-collision guarantee true at the layer that relies on it.
	if !substrate.IsValidNanoID(identityID) {
		return "", fmt.Errorf("resolved identity id is not a valid NanoID")
	}

	deviceID := strings.TrimSpace(req.ClientInformation.Name)
	if err := validateSubjectToken(deviceID); err != nil {
		return "", fmt.Errorf("device id is not subject-safe: %w", err)
	}

	perms := PermissionsFor(identityID, deviceID)

	expiry := r.maxTTL
	if !actor.ExpiresAt.IsZero() {
		if untilTokenExp := actor.ExpiresAt.Sub(r.now()); untilTokenExp < expiry {
			expiry = untilTokenExp
		}
	}
	if expiry <= 0 {
		return "", fmt.Errorf("token is at or past expiry")
	}

	uc := jwt.NewUserClaims(req.UserNkey)
	uc.Audience = "$G"
	uc.Permissions = *perms
	uc.Expires = r.now().Add(expiry).Unix()

	userJWT, err := uc.Encode(r.issuer)
	if err != nil {
		return "", fmt.Errorf("natsauth: encode user jwt: %w", err)
	}
	return userJWT, nil
}

// resolveIdentity wraps IdentityResolver.Resolve so a lookup error degrades
// to "act as the raw credential" (bound=false) rather than denying the
// connection outright — the same deny-safe fallback
// internal/gateway/gateway.go's resolveActor already applies on the HTTP
// write path (a CDC-lag miss must not lock a freshly-authenticated user out
// of their own, not-yet-claimed sync subject).
func (r *Responder) resolveIdentity(ctx context.Context, actorID string) (bound bool, identityKey string, err error) {
	identityKey, bound, err = r.resolve.Resolve(ctx, actorID)
	if err != nil {
		return false, "", err
	}
	return bound, identityKey, nil
}

// PermissionsFor builds the design §3.3 per-connection permission template —
// pure and unit-testable as data, independent of the request/response JWT
// plumbing. identityID and deviceID must already be validated subject-safe
// tokens (validateSubjectToken); PermissionsFor does not re-validate (the
// one caller, authorize, always validates first) so a malformed id here is a
// caller-contract bug, not a runtime input to guard again.
func PermissionsFor(identityID, deviceID string) *jwt.Permissions {
	durable := fmt.Sprintf("edge-sync-%s-%s", identityID, deviceID)
	syncSubject := syncSubjectPrefix + "." + identityID

	p := &jwt.Permissions{}
	p.Sub.Allow.Add(
		syncSubject,
		inboxPrefix+"."+identityID+".>",
	)
	p.Pub.Allow.Add(
		fmt.Sprintf("$JS.API.CONSUMER.CREATE.SYNC.%s.%s", durable, syncSubject),
		"$JS.API.CONSUMER.MSG.NEXT.SYNC."+durable,
		"$JS.API.CONSUMER.INFO.SYNC."+durable,
		"$JS.API.CONSUMER.DELETE.SYNC."+durable,
		"$JS.ACK.SYNC."+durable+".>",
	)
	p.Pub.Allow.Add(controlRPCs...)
	return p
}

// maxSubjectTokenLen bounds validateSubjectToken's input (defense in depth,
// an adversarial-pass finding, LOW): deviceID is fully CONNECT-client-
// controlled with no length check upstream (nats.go's CustomInboxPrefix
// itself imposes none), and it is spliced unbounded into every durable /
// consumer / ack subject PermissionsFor issues. Not independently exploitable
// (the identity segment stays the sole trust boundary — §3.3), but an
// unbounded value has no reason to be accepted. 200 bytes comfortably covers
// any real device identifier while staying well under NATS's default
// max_control_line (~4KB, the incidental bound this previously relied on
// alone).
const maxSubjectTokenLen = 200

// validateSubjectToken rejects an empty value, one exceeding
// maxSubjectTokenLen, or one containing a NATS subject-reserved character
// (`.`, `*`, `>`) or whitespace — the same character class
// subjects.validateToken enforces (internal/refractor/subjects/subjects.go),
// reimplemented here as an error-returning check rather than a panic: unlike
// that package's callers (a lensID/nodeID — a static, platform-chosen
// string), the value here (the CONNECT-supplied device name) must fail one
// connection's authorization, never crash the responder. The identity id
// itself is checked separately and more strictly (substrate.IsValidNanoID,
// in authorize) — this function is deviceID's check only.
func validateSubjectToken(s string) error {
	if s == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(s) > maxSubjectTokenLen {
		return fmt.Errorf("exceeds %d bytes", maxSubjectTokenLen)
	}
	if strings.ContainsAny(s, ".*> \t\n\r") {
		return fmt.Errorf("contains a character reserved in a NATS subject token: %q", s)
	}
	return nil
}
