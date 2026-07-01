package main

import (
	"crypto"
	"crypto/rand"
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

	"github.com/asolgan/lattice/internal/gateway/auth"
)

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
//   - DEMO (CLINIC_APP_DEV_AUTH=1): the trusted loopback tool generates an
//     EPHEMERAL in-process RSA keypair at startup, trusts its own public half in
//     the Verifier, and exposes POST /api/dev-token to mint a short-lived JWT for
//     the selected patient identity. This is the explicit demo stand-in for the
//     deferred Gateway/IdP login (design Option C) — it lets the browser FE keep
//     working on the loopback demo while exercising the SAME verified-JWT → RLS
//     path the production boundary uses. The signing key never persists and is
//     never accepted from outside the process.
//   - PRODUCTION (CLINIC_APP_JWT_PUBLIC_KEY set): the Verifier trusts the
//     real external IdP's public key(s); no minting happens here (the app never
//     signs — actor signing keys live outside the platform). The FE presents
//     real Bearer tokens (the deferred login flow).

const (
	devAuthKID      = "clinic-dev"
	devAuthIssuer   = "clinic-app-dev"
	devAuthAudience = "lattice-read"
	devTokenTTL     = 30 * time.Minute
)

// devSigner mints short-lived JWTs for the demo posture. It is nil unless
// CLINIC_APP_DEV_AUTH is enabled.
type devSigner struct {
	priv     *rsa.PrivateKey
	kid      string
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// mint returns a signed RS256 token whose `sub` is the bare identity id the RLS
// principal keys on. subject must be the bare NanoID (the <id> of
// vtx.identity.<id>), matching the grant table's actor_id column.
func (d *devSigner) mint(subject string) (string, time.Time, error) {
	now := d.now()
	exp := now.Add(d.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Subject:   subject,
		Issuer:    d.issuer,
		Audience:  jwt.ClaimStrings{d.audience},
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
func setupReadAuth(logger *slog.Logger, loopback bool) (*auth.Authenticator, *devSigner, error) {
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
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: generate ephemeral key: %w", err)
		}
		verifier, err := auth.NewVerifier(auth.Config{
			Keys:     map[string]crypto.PublicKey{devAuthKID: &priv.PublicKey},
			Issuer:   devAuthIssuer,
			Audience: devAuthAudience,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: build verifier: %w", err)
		}
		logger.Warn("DEV-AUTH ENABLED: minting demo JWTs in-process (NOT for production); the read boundary trusts an ephemeral key")
		signer := &devSigner{
			priv:     priv,
			kid:      devAuthKID,
			issuer:   devAuthIssuer,
			audience: devAuthAudience,
			ttl:      devTokenTTL,
			now:      time.Now,
		}
		// Revocation is the D1.2 kill-switch; the demo has no revocation bucket, so
		// pass nil (the Authenticator permits a nil checker — verification only).
		return auth.NewAuthenticator(verifier, nil), signer, nil
	}

	pemKey := os.Getenv("CLINIC_APP_JWT_PUBLIC_KEY")
	if strings.TrimSpace(pemKey) == "" {
		return nil, nil, nil
	}
	pub, err := parsePublicKeyPEM(pemKey)
	if err != nil {
		return nil, nil, fmt.Errorf("CLINIC_APP_JWT_PUBLIC_KEY: %w", err)
	}
	kid := envOrDefault("CLINIC_APP_JWT_KID", "idp-key-1")
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     map[string]crypto.PublicKey{kid: pub},
		Issuer:   os.Getenv("CLINIC_APP_JWT_ISSUER"),
		Audience: os.Getenv("CLINIC_APP_JWT_AUDIENCE"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build verifier: %w", err)
	}
	logger.Info("read boundary configured with external IdP public key", "kid", kid)
	return auth.NewAuthenticator(verifier, nil), nil, nil
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

// isTruthy reports whether an env value enables a flag (1/true/yes, any case).
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
