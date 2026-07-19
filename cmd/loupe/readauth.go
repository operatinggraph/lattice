package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/substrate"
)

// The console's front door (loupe-operator-auth-lift-design.md §3.1). Loupe
// was auth-less: whoever reached its listen address was trusted as admin.
// This gate requires a verified operator Bearer JWT for every request — the
// static UI and every /api/* route — and answers a no-token request with a
// 401, matching the design's fail-closed intent. It is an authN gate, not an
// RLS filter: an authenticated operator still sees the whole graph (the
// inspector's job); the gate only answers "is anyone allowed in the door at
// all". Reads are otherwise unchanged; op-submissions still stamp
// operatorActorKey/operatorActorToken as they do today (the Gateway-relay
// half of the lift is a later increment).
//
// Two postures, selected by env (mirrors cmd/loftspace-app/readauth.go):
//
//   - DEMO (LOUPE_DEV_AUTH=1): loopback-only, signs with the shared checked-in
//     dev key (deploy/gateway-dev-key/) that the Gateway and every vertical app
//     already trust. POST /api/operator/dev-token mints a short-lived token for
//     Loupe's own configured operator identity (operatorActorKey) — unlike the
//     verticals' per-applicant minting, Loupe has exactly one operator subject,
//     so the endpoint takes no request body.
//   - PRODUCTION (LOUPE_JWT_PUBLIC_KEY set): trusts a real external IdP's
//     public key; Loupe never signs. An operator obtains a real Bearer token
//     out-of-band (a redirect-based IdP login stays deferred) and hands it to
//     /login's manual-token form, which exchanges it for the session cookie
//     below.
//
// A plain browser navigation cannot carry a custom Authorization header, so a
// gate covering "every request" needs a second credential transport for the
// UI itself: an HttpOnly, SameSite=Strict session cookie
// (operatorSessionCookieName), set once a token verifies (by the dev-token
// mint or by POST /api/operator/session) and accepted by authenticateConsole
// as a fallback when no bearer header is present. /login is the one exempted
// static page — self-contained, no external JS/CSS, so no further exemptions
// are needed — a fresh unauthenticated browser can always reach to start that
// exchange. requireOperator additionally redirects an unauthenticated
// top-level browser navigation (Sec-Fetch-Dest: document) to /login instead
// of answering it with a bare JSON 401, which only a programmatic caller
// (curl, the API, tests) ever sees.
const operatorDevTokenTTL = 30 * time.Minute

// operatorDevTokenPath is exempted from requireOperator — a caller must be
// able to reach the minting endpoint before it holds a token. The handler
// itself still fails closed (404) unless LOUPE_DEV_AUTH is set, and
// setupOperatorAuth refuses to enable dev-auth off a loopback bind, so this
// exemption never opens a production surface.
const operatorDevTokenPath = "/api/operator/dev-token"

// operatorSessionPath exchanges an already-verified Bearer token (a real IdP
// token obtained out-of-band, or one minted by dev-token) for the browser
// session cookie. Exempted for the same reason as operatorDevTokenPath — a
// caller with no cookie yet must be able to reach it. It runs the exact
// verification authenticateConsole does, so it cannot be used to bypass auth,
// only to move an already-valid token onto the cookie transport.
const operatorSessionPath = "/api/operator/session"

// operatorLogoutPath clears the session cookie. Exempted so a stale or
// invalid cookie can always be cleared rather than trapping the browser.
const operatorLogoutPath = "/api/operator/logout"

// loginPagePath serves the standalone login page — the one static route
// requireOperator never gates, so a fresh browser with no credential at all
// always has a way in.
const loginPagePath = "/login"

// operatorSessionCookieName holds the operator's verified Bearer token for
// browser use — exactly the same JWT authenticateConsole verifies from an
// Authorization header, just a second transport for the identical
// credential, not a weaker one. HttpOnly (JS never reads it) + SameSite=Strict
// (never attached to a cross-site request, so it cannot be CSRF-ridden) +
// Secure on any non-loopback bind (setOperatorSessionCookie).
const operatorSessionCookieName = "loupe_operator_session"

// devSigner mints short-lived JWTs for the demo posture, signing with the
// shared dev key so the token verifies both here and at the Gateway/any other
// shared-dev-IdP posture (real-actor-write-auth-e2e-design.md §3.2).
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

// setupOperatorAuth builds the console's authenticator from the environment.
// It returns (nil, nil, nil) when no posture is configured — a nil
// authenticator makes requireOperator fail every request closed with 401,
// the correct default for a console whose operator login is not provisioned.
func setupOperatorAuth(logger *slog.Logger, loopback bool) (*auth.Authenticator, *devSigner, error) {
	if isTruthy(os.Getenv("LOUPE_DEV_AUTH")) {
		// Defense in depth: the dev minter signs for whatever subject Loupe is
		// configured with, so it must never be reachable off-host — a
		// misconfigured non-local bind with dev-auth would let any network
		// caller mint itself an operator token.
		if !loopback {
			return nil, nil, fmt.Errorf("LOUPE_DEV_AUTH is only permitted on a loopback bind; use LOUPE_JWT_PUBLIC_KEY for a non-local deployment")
		}
		if strings.TrimSpace(os.Getenv("LOUPE_JWT_PUBLIC_KEY")) != "" {
			logger.Warn("both LOUPE_DEV_AUTH and LOUPE_JWT_PUBLIC_KEY are set; dev-auth wins and the configured IdP public key is IGNORED")
		}
		priv, err := auth.LoadDevSigningKey(os.Getenv("LOUPE_DEV_PRIVATE_KEY_PATH"))
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: load shared dev signing key: %w", err)
		}
		trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
			DevMode:    true,
			DevKeyPath: os.Getenv("LOUPE_DEV_PUBLIC_KEY_PATH"),
		}, func(msg string) { logger.Warn(msg) })
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
		}
		verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: auth.KeyInfoFromSpecs(trustedSpecs)})
		if err != nil {
			return nil, nil, fmt.Errorf("dev-auth: build verifier: %w", err)
		}
		logger.Warn("DEV-AUTH ENABLED: Loupe mints its own operator token in-process (NOT for production); the console trusts the shared dev key")
		signer := &devSigner{
			priv: priv,
			kid:  auth.DevKeyID,
			ttl:  operatorDevTokenTTL,
			now:  time.Now,
		}
		// No revocation checker: the demo posture has no revocation bucket wired
		// (NewAuthenticator permits a nil RevocationChecker — verification only).
		return auth.NewAuthenticator(verifier, nil), signer, nil
	}

	pemKey := os.Getenv("LOUPE_JWT_PUBLIC_KEY")
	if strings.TrimSpace(pemKey) == "" {
		return nil, nil, nil
	}
	pub, err := parseOperatorPublicKeyPEM(pemKey)
	if err != nil {
		return nil, nil, fmt.Errorf("LOUPE_JWT_PUBLIC_KEY: %w", err)
	}
	issuer := os.Getenv("LOUPE_JWT_ISSUER")
	if strings.TrimSpace(issuer) == "" {
		return nil, nil, fmt.Errorf("LOUPE_JWT_ISSUER is required alongside LOUPE_JWT_PUBLIC_KEY " +
			"(a configured external IdP source MUST pin an expected iss — Contract #11 §3.2)")
	}
	kid := envOrDefault("LOUPE_JWT_KID", "idp-key-1")
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:     map[string]crypto.PublicKey{kid: pub},
		KeyInfo:  map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeOpaque, Issuer: issuer}}},
		Audience: os.Getenv("LOUPE_JWT_AUDIENCE"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("build verifier: %w", err)
	}
	logger.Info("operator console configured with external IdP public key", "kid", kid)
	return auth.NewAuthenticator(verifier, nil), nil, nil
}

func parseOperatorPublicKeyPEM(pemStr string) (crypto.PublicKey, error) {
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

// bearerToken extracts the token from an `Authorization: Bearer <token>`
// header, or "" when absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// sessionCookieToken extracts the operator token from the session cookie, or
// "" when absent. The browser attaches this automatically on every
// same-origin request — including a plain top-level navigation, which cannot
// carry a custom header — once /login or a dev-token mint has set it.
func sessionCookieToken(r *http.Request) string {
	c, err := r.Cookie(operatorSessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

// setOperatorSessionCookie stores a verified operator token as the browser
// session credential, expiring alongside the token itself.
func (s *server) setOperatorSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     operatorSessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearOperatorSessionCookie removes the session cookie (logout, or a client
// reacting to a 401 mid-session).
func (s *server) clearOperatorSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     operatorSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// isOperatorAuthExempt reports whether path is reachable with no credential
// at all — the login page and the endpoints that establish or clear one.
func isOperatorAuthExempt(path string) bool {
	switch path {
	case operatorDevTokenPath, operatorSessionPath, operatorLogoutPath, loginPagePath:
		return true
	default:
		return false
	}
}

// isBrowserNavigation reports whether r is a top-level browser page load
// (address bar, link click, bookmark) rather than a script/API/curl call.
// Modern browsers set Sec-Fetch-Dest: document on every such request; a
// fetch()/XHR call sends "empty" or "cors", and curl/httptest send neither —
// so this never fires for the existing plain-JSON-401 callers or tests.
func isBrowserNavigation(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document")
}

// operatorTokenContextKey is the request-context key requireOperator stores
// the winning credential's raw token under, so a handler can relay the exact
// same Bearer token to the Gateway (op-submissions relay,
// loupe-operator-auth-lift-design.md §3.2) without re-deriving it from the
// header/cookie itself.
type operatorTokenContextKey struct{}

// operatorToken retrieves the current request's verified operator Bearer
// token from ctx (or "" outside a requireOperator-gated request, or on the
// exempted paths that never authenticate). Safe to call from anywhere ctx
// descends from a gated request's context, however many layers deep.
func operatorToken(ctx context.Context) string {
	tok, _ := ctx.Value(operatorTokenContextKey{}).(string)
	return tok
}

// requireOperator wraps next so every request — the static UI and every
// /api/* route alike — must carry a valid operator credential (a Bearer
// header or the session cookie), except the login page and the credential-
// exchange endpoints (a caller must be able to reach them before it holds
// anything). Fails closed: no authenticator configured, no credential
// presented, or verification failing all deny, never a silent pass. An
// unauthenticated top-level browser navigation is redirected to the login
// page instead of answered with a bare JSON 401; a programmatic caller (curl,
// the API, tests — no Sec-Fetch-Dest: document) still sees the plain 401. On
// success, the winning raw token is attached to the request context
// (operatorToken) so a handler can relay it onward.
func (s *server) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOperatorAuthExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		_, tok, err := s.authenticateConsole(r)
		if err != nil {
			if isBrowserNavigation(r) {
				http.Redirect(w, r, loginPagePath, http.StatusFound)
				return
			}
			s.writeError(w, http.StatusUnauthorized, "operator login required: "+err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), operatorTokenContextKey{}, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticateConsole verifies the request's operator credential and returns
// the verified actor alongside the exact raw token that verified it. It
// tries the Bearer header first; if that's absent OR fails to verify (a
// stray or expired header must not mask an otherwise-good session — e.g. a
// leftover devtools-replayed header alongside a live cookie), it falls back
// to the session cookie. Returns an error only when neither transport yields
// a verified operator — fail closed throughout.
func (s *server) authenticateConsole(r *http.Request) (auth.VerifiedActor, string, error) {
	ctx, cancel := s.reqContext(r)
	defer cancel()
	if tok := bearerToken(r); tok != "" {
		if actor, err := s.verifyOperatorToken(ctx, tok); err == nil {
			return actor, tok, nil
		}
	}
	tok := sessionCookieToken(r)
	actor, err := s.verifyOperatorToken(ctx, tok)
	return actor, tok, err
}

// verifyOperatorToken is the one verification path every credential
// transport shares (header, cookie, or a token presented to
// /api/operator/session) — fail closed: no authenticator configured, an
// empty token, a verification failure, a missing subject, no configured
// operator, or a token naming a different identity all deny. The last check
// is what makes this a NAMED operator's login (design intent, §1/§3.1) rather
// than "anyone holding any token the trusted key/IdP will sign" — without it,
// a token minted for an unrelated subject (e.g. by another app trusting the
// same shared dev key, real-actor-write-auth-e2e-design.md §3.2, or issued by
// a real IdP to some other user entirely) would open the console.
func (s *server) verifyOperatorToken(ctx context.Context, tok string) (auth.VerifiedActor, error) {
	if s.authn == nil {
		return auth.VerifiedActor{}, fmt.Errorf("console auth not configured (set LOUPE_DEV_AUTH or LOUPE_JWT_PUBLIC_KEY)")
	}
	if tok == "" {
		return auth.VerifiedActor{}, fmt.Errorf("missing bearer token")
	}
	actor, err := s.authn.Authenticate(ctx, tok)
	if err != nil {
		return auth.VerifiedActor{}, err
	}
	if strings.TrimSpace(actor.Subject) == "" {
		return auth.VerifiedActor{}, fmt.Errorf("token has no subject")
	}
	if s.operatorActorKey == "" {
		return auth.VerifiedActor{}, fmt.Errorf("no operator actor configured (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded)")
	}
	if actor.ActorID != s.operatorActorKey {
		return auth.VerifiedActor{}, fmt.Errorf("token does not authenticate the configured operator identity")
	}
	return actor, nil
}

// handleOperatorDevToken implements POST /api/operator/dev-token (no body) —
// the demo-only login stand-in. It mints for a FIXED subject (the configured
// operatorActorKey), never a caller-supplied one: unlike the verticals'
// per-applicant minting, Loupe has exactly one operator identity, so the
// client never needs to name it. Available ONLY when dev-auth is enabled; a
// production deployment wires a real operator IdP login instead.
func (s *server) handleOperatorDevToken(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "dev-token minting is disabled (LOUPE_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// crossOriginBlocked here too: mint is a state-changing action (issues a
	// live credential) like every other endpoint that checks it, even though
	// it takes no body — a hostile page's blind cross-origin POST is refused
	// before touching operatorActorKey, not just left to CORS to block the
	// response.
	if s.crossOriginBlocked(w, r) {
		return
	}
	if s.operatorActorKey == "" {
		s.writeError(w, http.StatusBadGateway, "no operator actor configured (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded)")
		return
	}
	vertexType, subject, ok := substrate.ParseVertexKey(s.operatorActorKey)
	if !ok || vertexType != "identity" {
		s.writeError(w, http.StatusInternalServerError, "operator actor key is malformed (must be a vtx.identity.<id> key)")
		return
	}
	token, exp, err := s.devSigner.mint(subject)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint token: "+err.Error())
		return
	}
	// Also set the session cookie so a browser that just minted this token can
	// immediately navigate to / (a plain navigation cannot carry the JSON
	// response's token as a header) — /login's dev-login button relies on this.
	s.setOperatorSessionCookie(w, token, exp)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresAt": exp.UTC().Format(time.RFC3339),
	})
}

// handleOperatorSession implements POST /api/operator/session
// {"token":"<jwt>"} → sets the session cookie when the token verifies, via
// the exact same check authenticateConsole runs. It cannot mint or widen a
// credential — only move an already-valid Bearer token (a real IdP token
// obtained out-of-band, or one minted by dev-token) onto the cookie transport
// a plain browser navigation can carry. Reachable with no existing credential
// (a caller has none yet), like the dev-token mint.
func (s *server) handleOperatorSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
		return
	}
	// Bounded like every other body-reading handler in this package
	// (handleOp, handleVaultDecrypt, the package installers) — this endpoint
	// is reachable with no credential at all, so an unbounded read would be a
	// pre-auth memory-exhaustion vector. A JWT is a few KB at most.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	tok := strings.TrimSpace(req.Token)
	ctx, cancel := s.reqContext(r)
	defer cancel()
	actor, err := s.verifyOperatorToken(ctx, tok)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
		return
	}
	s.setOperatorSessionCookie(w, tok, actor.ExpiresAt)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleOperatorLogout implements POST /api/operator/logout — clears the
// session cookie. Exempted from requireOperator so an expired or invalid
// cookie can always be cleared rather than trapping the browser at /login.
func (s *server) handleOperatorLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
		return
	}
	s.clearOperatorSessionCookie(w)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLoginPage implements GET /login — the standalone, self-contained
// (no external JS/CSS, so no further requireOperator exemptions are needed)
// page a fresh browser with no credential at all can always reach.
func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	body, err := webFS.ReadFile("web/login.html")
	if err != nil {
		// embed guarantees this file exists at build time; unreachable outside
		// a programmer error (mirrors the fs.Sub panic in registerRoutes).
		s.writeError(w, http.StatusInternalServerError, "login page: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(body); err != nil {
		s.logger.Error("write login page", "error", err)
	}
}

// isTruthy reports whether an env value enables a flag (1/true/yes, any case).
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
