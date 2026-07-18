package main

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/substrate"
)

// cafe-app has no protected read boundary (every café lens is plain NATS-KV,
// P5) — the auth concern is minting Bearer tokens the Gateway's write path
// verifies (real-actor-write-auth-e2e-design.md §3.1's shared-dev-IdP
// posture, mirroring loftspace-app/clinic-app/wellness-app's own
// handleStaffDevToken). Charge stays grantsTo:[operator] scope:any (a single
// staff token covers it), but OpenTab/Settle additionally grant `consumer`
// scope=self (packages/cafe-domain/permissions.go), so a resident acting as
// themselves mints a token for their own identity via handleDevToken.

const devTokenTTL = 30 * time.Minute

// devSigner mints short-lived JWTs for the demo posture. It is nil unless
// CAFE_APP_DEV_AUTH is enabled. It signs with the shared dev key, so the
// resulting token verifies at the Gateway (and any other vertical app
// running the same shared-dev-IdP posture).
type devSigner struct {
	priv *rsa.PrivateKey
	kid  string
	ttl  time.Duration
	now  func() time.Time
}

// mint returns a signed RS256 token whose `sub` is the bare identity id.
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

// setupDevSigner builds the staff dev-token minter from the environment. It
// returns (nil, nil) when CAFE_APP_DEV_AUTH is not set.
func setupDevSigner(logger *slog.Logger, loopback bool) (*devSigner, error) {
	if !isTruthy(os.Getenv("CAFE_APP_DEV_AUTH")) {
		return nil, nil
	}
	// Defense in depth: the dev minter must never be reachable off-host.
	if !loopback {
		return nil, fmt.Errorf("CAFE_APP_DEV_AUTH is only permitted on a loopback bind (the in-process minter trusts the fixed admin actor)")
	}
	priv, err := auth.LoadDevSigningKey(os.Getenv("CAFE_APP_DEV_PRIVATE_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("dev-auth: load shared dev signing key: %w", err)
	}
	logger.Warn("DEV-AUTH ENABLED: minting demo staff JWTs in-process (NOT for production)")
	return &devSigner{priv: priv, kid: auth.DevKeyID, ttl: devTokenTTL, now: time.Now}, nil
}

// handleStaffDevToken implements POST /api/staff/dev-token (no body) — mints
// for a FIXED subject (this app's own admin actor), mirroring
// loftspace-app/clinic-app's handleStaffDevToken. Available only when
// dev-auth is enabled.
func (s *server) handleStaffDevToken(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "dev-token minting is disabled (CAFE_APP_DEV_AUTH not set)")
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

// handleDevToken implements POST /api/dev-token {subject} → {token, expiresAt} —
// the demo-only self-service login stand-in, mirroring cmd/wellness-app's
// handleDevToken. subject is the bare identity id (no "vtx.identity." prefix)
// of the resident's OWN identity — OpenTab/Settle's consumer scope=self grant
// requires authContext.target to name that identity, and cafe-domain's own
// Starlark script separately confirms it is the tab's lease's applicant
// (packages/cafe-domain/ddls.go). Available only when dev-auth is enabled.
func (s *server) handleDevToken(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "dev-token minting is disabled (CAFE_APP_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
