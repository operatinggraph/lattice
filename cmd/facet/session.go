package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/gateway/auth"
)

// Inc 2 (edge-showcase-app-design.md §7.2): a login surface so a returning
// or second user can get INTO Facet at all — Fire 3 shipped only the claim
// ceremony (one-shot unclaimed→claimed), leaving identity an
// operator-injected boot env var with no way back in. This mirrors Loupe's
// shipped session-cookie pattern (cmd/loupe/readauth.go) — an HttpOnly,
// SameSite=Strict cookie carrying the exact same dev-minted JWT every other
// Facet write already trusts — generalized to loftspace's multi-subject
// minting (any identity, not one fixed operator), since Facet's whole point
// is "same binary, different identity, different app" (design §1).

const sessionCookieName = "facet_session"
const loginPagePath = "/login"
const devLoginPath = "/api/dev-login"
const logoutPath = "/api/logout"
const whoamiPath = "/api/whoami"

// isSessionAuthExempt reports whether path is reachable with no session
// cookie at all — the login page, the endpoints that establish/clear one,
// and /api/claim (a not-yet-claimed identity has no session to present;
// that ceremony is how a fresh user gets into the system in the first
// place). Mirrors cmd/loupe/readauth.go's isOperatorAuthExempt.
func isSessionAuthExempt(path string) bool {
	switch path {
	case loginPagePath, devLoginPath, logoutPath, whoamiPath, "/api/claim":
		return true
	default:
		return false
	}
}

func (s *server) setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   !s.loopback,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !s.loopback,
		SameSite: http.SameSiteStrictMode,
	})
}

func sessionCookieToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

// sessionIdentityContextKey is the request-context key requireSession stores
// the resolved identity id (bare NanoID) under.
type sessionIdentityContextKey struct{}

func sessionIdentity(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(sessionIdentityContextKey{}).(string)
	return id, ok && id != ""
}

// resolveSessionIdentity verifies the session cookie's token (when s.authn
// is configured) and returns the bare identity id it authenticates. Falls
// back to s.bootIdentityID — the boot-env EDGE_IDENTITY_ID, now an optional
// single-user fallback per design §7.2 — when no cookie verifies, so a
// deployment that never enables FACET_DEV_AUTH, or a browser that hasn't
// logged in yet, keeps working exactly as Fire 2/3 did: one process, one
// identity.
func (s *server) resolveSessionIdentity(r *http.Request) (string, bool) {
	if s.authn != nil {
		if tok := sessionCookieToken(r); tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if actor, err := s.authn.Authenticate(ctx, tok); err == nil && actor.Subject != "" {
				return actor.Subject, true
			}
		}
	}
	if s.bootIdentityID != "" {
		return s.bootIdentityID, true
	}
	return "", false
}

// requireSession wraps next so every request resolves to a signed-in
// identity — either a verified session cookie or the boot-env fallback —
// before reaching a handler that touches an engine. Unlike Loupe's
// requireOperator this never hard-denies a deployment with no login
// mechanism configured at all as a SECURITY posture: Facet has never had an
// authN gate of its own (EDGE.3's Gateway-side JWT verification is the real
// security boundary on every write; this cookie only selects WHICH
// already-entitled identity's engine a request lands on). It still 401s/
// redirects when literally nothing resolves, because a handler downstream
// has no engine to run against.
func (s *server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSessionAuthExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		identityID, ok := s.resolveSessionIdentity(r)
		if !ok {
			if isBrowserNavigation(r) {
				http.Redirect(w, r, loginPagePath, http.StatusFound)
				return
			}
			s.writeError(w, http.StatusUnauthorized, "login required (no session cookie and no boot identity configured)")
			return
		}
		ctx := context.WithValue(r.Context(), sessionIdentityContextKey{}, identityID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isBrowserNavigation mirrors cmd/loupe/readauth.go's helper of the same
// name: a top-level browser page load (address bar, link click, bookmark)
// rather than a script/API/curl call.
func isBrowserNavigation(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document")
}

// handleLoginPage implements GET /login — the standalone page a fresh
// browser with no session at all can always reach.
func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	body, err := webFS.ReadFile("web/login.html")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "login page: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(body); err != nil {
		s.logger.Error("facet: write login page", "error", err)
	}
}

// devLoginRequest is what the login page POSTs: the bare identity id to sign
// in as (e.g. a showcase persona's NanoID from `make seed-showcase`, or one
// POST /api/claim just returned). Facet has no directory to pick a name from
// (design's non-goal list: "no admin/cross-identity surfaces") — the id
// itself is the whole credential in this demo posture, same as the claim
// flow already hands the caller a raw key.
type devLoginRequest struct {
	IdentityID string `json:"identityId"`
}

// handleDevLogin implements POST /api/dev-login {identityId} — the demo-only
// login stand-in, mirroring cmd/loftspace-app's handleDevToken (any
// caller-supplied subject) plus cmd/loupe's cookie-setting
// (handleOperatorDevToken): mints a token for the requested identity and
// sets it as the session cookie. Available ONLY when FACET_DEV_AUTH is
// enabled — same gate, same reasoning as /api/claim: an in-process minter
// that trusts any caller-supplied subject must never be reachable off a
// loopback bind.
func (s *server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "login is disabled (FACET_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req devLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClaimBodyBytes)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	bareID := strings.TrimPrefix(strings.TrimSpace(req.IdentityID), "vtx.identity.")
	if bareID == "" {
		s.writeError(w, http.StatusBadRequest, "identityId is required")
		return
	}
	token, exp, err := s.devSigner.mint(bareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint session token: "+err.Error())
		return
	}
	s.setSessionCookie(w, token, exp)
	s.writeJSON(w, http.StatusOK, map[string]any{"identityId": bareID, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// handleLogout implements POST /api/logout — clears the session cookie.
// Exempted from requireSession so a stale or invalid cookie can always be
// cleared rather than trapping the browser.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	s.clearSessionCookie(w)
	s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleWhoami implements GET /api/whoami — the session's who-am-I UX
// (design §7.2's whoami beat; Facet has no shared cross-app actor-info
// endpoint to reuse, so this lands on its own /api/* surface alongside
// /api/feed, /api/enqueue, /api/claim). Never errors: an absent session just
// reports loggedIn:false, so the login page can safely probe it before any
// cookie exists.
func (s *server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	identityID, ok := s.resolveSessionIdentity(r)
	if !ok {
		s.writeJSON(w, http.StatusOK, map[string]bool{"loggedIn": false})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn":   true,
		"identityId": identityID,
	})
}

// setupSessionAuthn builds the verifier requireSession uses to check a
// session cookie's token — the same shared dev key signer signs with,
// loaded via the identical DevMode:true path setupDevSigner/cmd/loupe's
// setupOperatorAuth already use, so a token minted here or by /api/claim's
// throwaway credential both verify. Returns (nil, nil) when signer is nil —
// session verification is meaningless without a signer to have minted the
// cookie in the first place.
func setupSessionAuthn(logger *slog.Logger, signer *devSigner) (*auth.Authenticator, error) {
	if signer == nil {
		return nil, nil
	}
	trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv("FACET_DEV_PUBLIC_KEY_PATH"),
	}, func(msg string) { logger.Warn(msg) })
	if err != nil {
		return nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
	}
	verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: auth.KeyInfoFromSpecs(trustedSpecs)})
	if err != nil {
		return nil, fmt.Errorf("dev-auth: build session verifier: %w", err)
	}
	return auth.NewAuthenticator(verifier, nil), nil
}
