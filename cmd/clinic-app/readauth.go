package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// credentialBindingResolver is the credential→identity resolution surface
// authenticateRead consults so a claimed credential (A) reads as its
// business identity (U) — the shared seam the browser-direct topology
// requires (real-actor-write-auth-e2e-design.md §5,
// gateway-claim-flow-identity-provisioning-design.md §11.0): writes resolve
// at the Gateway, reads land on this app's boundary, and both must resolve
// the same claimed binding. internal/gateway/credentialbinding.Resolver
// satisfies this; a nil resolver (the default) leaves every actor acting as
// itself, unchanged from before this seam existed.
type credentialBindingResolver interface {
	Resolve(ctx context.Context, actorID string) (identityKey string, bound bool, err error)
}

// The read boundary (D1.5, porting D1.3's loftspace-app pattern into a second
// vertical) — clinic-app reads the protected clinicAppointmentsRead Postgres
// model as an AUTHENTICATED actor. Authentication is the shared verify-only
// seam (internal/gateway/auth): the app verifies an IdP JWT and uses the
// verified subject as the RLS principal. The app never holds authorization
// logic — Postgres RLS is the single authorization source.
//
// Two postures, selected by env (fail-closed: neither set ⇒ no authenticator ⇒
// every protected read is 401):
//
//   - DEMO (CLINIC_APP_DEV_AUTH=1): the trusted loopback tool signs with the
//     checked-in dev key shared by the Gateway and every vertical app
//     (deploy/gateway-dev-key/, kid auth.DevKeyID — real-actor-write-auth-e2e-
//     design.md §3.2's shared-dev-IdP interim), and exposes POST /api/dev-token
//     to mint a short-lived JWT for the selected patient identity. Because the
//     key is shared, a token minted here (or by `gateway dev-token`) verifies at
//     BOTH this app's read boundary and the Gateway's write path — one dev
//     identity, one token, both surfaces — which is what lets the browser-direct
//     FE (writes → Gateway, reads → app) present a single Bearer token. This is
//     the explicit demo stand-in for a real IdP login (design Option C); the
//     private key is dev-only and never accepted from outside a loopback bind.
//   - PRODUCTION (CLINIC_APP_JWT_PUBLIC_KEY set): the Verifier trusts the
//     real external IdP's public key(s); no minting happens here (the app never
//     signs — actor signing keys live outside the platform). The FE presents
//     real Bearer tokens (the deferred login flow).

const devTokenTTL = 30 * time.Minute

// devSigner mints short-lived JWTs for the demo posture. It is nil unless
// CLINIC_APP_DEV_AUTH is enabled. It signs with the shared dev key (no
// issuer/audience claims), so the resulting token verifies both here and at
// the Gateway and any other vertical app running the same shared-dev-IdP
// posture — see the package doc.
type devSigner struct {
	priv *rsa.PrivateKey
	kid  string
	ttl  time.Duration
	now  func() time.Time
}

// mint returns a signed RS256 token whose `sub` is the bare identity id the RLS
// principal keys on. subject must be the bare NanoID (the <id> of
// vtx.identity.<id>), matching the grant table's actor_id column.
func (d *devSigner) mint(subject string) (string, time.Time, error) {
	now := d.now()
	exp := now.Add(d.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	})
	tok.Header["kid"] = d.kid
	signed, err := tok.SignedString(d.priv)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// setupReadAuth builds the read-boundary authenticator from the environment. It
// returns (nil, nil, nil) when no posture is configured — a nil authenticator
// makes every protected read fail closed with 401, which is the correct default
// for an app whose read boundary is not provisioned.
func setupReadAuth(logger *slog.Logger, loopback bool, revocationChecker auth.RevocationChecker) (*auth.Authenticator, *devSigner, error) {
	if isTruthy(os.Getenv("CLINIC_APP_DEV_AUTH")) {
		// Defense in depth: the dev minter trusts any caller-supplied subject, so it
		// must never be reachable off-host. Refuse to enable it on a non-loopback
		// bind even though the operator asked — a misconfigured non-local bind with
		// dev-auth would be an open identity-impersonation surface.
		if !loopback {
			return nil, nil, fmt.Errorf("CLINIC_APP_DEV_AUTH is only permitted on a loopback bind (the in-process minter trusts any subject); use CLINIC_APP_JWT_PUBLIC_KEY for a non-local deployment")
		}
		if strings.TrimSpace(os.Getenv("CLINIC_APP_JWT_PUBLIC_KEY")) != "" {
			logger.Warn("both CLINIC_APP_DEV_AUTH and CLINIC_APP_JWT_PUBLIC_KEY are set; dev-auth wins and the configured IdP public key is IGNORED")
		}
		priv, err := auth.LoadDevSigningKey(os.Getenv("CLINIC_APP_DEV_PRIVATE_KEY_PATH"))
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: load shared dev signing key: %w", err)
		}
		trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
			DevMode:    true,
			DevKeyPath: os.Getenv("CLINIC_APP_DEV_PUBLIC_KEY_PATH"),
		}, func(msg string) { logger.Warn(msg) })
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
		}
		verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: auth.KeyInfoFromSpecs(trustedSpecs)})
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: build verifier: %w", err)
		}
		logger.Warn("DEV-AUTH ENABLED: minting demo JWTs in-process (NOT for production); the read boundary trusts the shared dev key")
		signer := &devSigner{
			priv: priv,
			kid:  auth.DevKeyID,
			ttl:  devTokenTTL,
			now:  time.Now,
		}
		return auth.NewAuthenticator(verifier, revocationChecker), signer, nil
	}

	pemKey := os.Getenv("CLINIC_APP_JWT_PUBLIC_KEY")
	if strings.TrimSpace(pemKey) == "" {
		return nil, nil, nil
	}
	pub, err := parsePublicKeyPEM(pemKey)
	if err != nil {
		return nil, nil, fmt.Errorf("CLINIC_APP_JWT_PUBLIC_KEY: %w", err)
	}
	issuer := os.Getenv("CLINIC_APP_JWT_ISSUER")
	if strings.TrimSpace(issuer) == "" {
		return nil, nil, fmt.Errorf("CLINIC_APP_JWT_ISSUER is required alongside CLINIC_APP_JWT_PUBLIC_KEY " +
			"(a configured external IdP source MUST pin an expected iss — Contract #11 §3.2)")
	}
	kid := envOrDefault("CLINIC_APP_JWT_KID", "idp-key-1")
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     map[string]crypto.PublicKey{kid: pub},
		KeyInfo:  map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeOpaque, Issuer: issuer}}},
		Audience: os.Getenv("CLINIC_APP_JWT_AUDIENCE"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build verifier: %w", err)
	}
	logger.Info("read boundary configured with external IdP public key", "kid", kid)
	return auth.NewAuthenticator(verifier, revocationChecker), nil, nil
}

// parsePublicKeyPEM decodes a PEM-encoded RSA or ECDSA public key (PKIX).
func parsePublicKeyPEM(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	return pub, nil
}

// bearerToken extracts the token from an `Authorization: Bearer <token>` header,
// or "" when absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// authenticateRead verifies the request's Bearer JWT and returns the verified
// actor. It returns an error (→ 401) when no authenticator is configured, no
// token is presented, or verification/revocation fails — fail closed throughout.
func (s *server) authenticateRead(r *http.Request) (auth.VerifiedActor, error) {
	if s.authn == nil {
		return auth.VerifiedActor{}, fmt.Errorf("read boundary not configured (set CLINIC_APP_DEV_AUTH or CLINIC_APP_JWT_PUBLIC_KEY)")
	}
	tok := bearerToken(r)
	if tok == "" {
		return auth.VerifiedActor{}, fmt.Errorf("missing bearer token")
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()
	actor, err := s.authn.Authenticate(ctx, tok)
	if err != nil {
		return auth.VerifiedActor{}, err
	}
	// Defense in depth: the verifier requires a `sub` claim, but a protected read
	// keys RLS off actor.Subject (set_config('lattice.actor_id', …)). Refuse an
	// empty/blank subject here rather than depend on the RLS policy to deny an
	// empty actor — a missing principal must never reach the read path.
	if strings.TrimSpace(actor.Subject) == "" {
		return auth.VerifiedActor{}, fmt.Errorf("token has no subject")
	}
	if s.credBindings != nil {
		if identityKey, bound, rerr := s.credBindings.Resolve(ctx, actor.ActorID); rerr != nil {
			// Fail OPEN to the raw credential — the documented deny-safe
			// fallback (matches the Gateway's resolveActor): an unresolved
			// binding denies nothing on its own, it just means the actor
			// sees/writes as itself, never more than it's entitled to.
			s.logger.Error("clinic-app: credential-binding resolve failed", "actor", actor.ActorID, "error", rerr)
		} else if bound {
			actor.Subject = strings.TrimPrefix(identityKey, auth.IdentityKeyPrefix)
		}
	}
	return actor, nil
}

// handleDevToken implements POST /api/dev-token {subject} → {token, expiresAt} —
// the demo-only login stand-in. It is available ONLY when dev-auth is enabled;
// otherwise it returns 404 so production deployments expose no minting surface.
func (s *server) handleDevToken(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "dev-token minting is disabled (CLINIC_APP_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	body, err := requireBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		s.writeError(w, http.StatusBadRequest, "subject (the bare identity id) is required")
		return
	}
	token, exp, err := s.devSigner.mint(subject)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint token: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresAt": exp.UTC().Format(time.RFC3339),
	})
}

// handleStaffDevToken implements POST /api/staff/dev-token (no body) — the
// demo-only login stand-in for the clinic-wide STAFF view (D1.5, the staff
// wildcard increment). Unlike handleDevToken it mints for a FIXED subject
// (the app's own admin actor, s.adminActor — the same root-equivalent
// identity clinic-app already connects to NATS as for writes), never a
// caller-supplied one: the client never needs to know, or be trusted to
// name, which identity holds the wildcard grant. Available ONLY when
// dev-auth is enabled, mirroring handleDevToken; a production deployment
// wires a real staff login instead (deferred Gateway, out of D1 scope).
func (s *server) handleStaffDevToken(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "dev-token minting is disabled (CLINIC_APP_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway, "admin actor not loaded (bootstrap file missing or unreadable)")
		return
	}
	_, subject, ok := substrate.ParseVertexKey(s.adminActor)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "admin actor key is malformed")
		return
	}
	token, exp, err := s.devSigner.mint(subject)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint token: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresAt": exp.UTC().Format(time.RFC3339),
	})
}

// isTruthy reports whether an env value enables a flag (1/true/yes, any case).
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
