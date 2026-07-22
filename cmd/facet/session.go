package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
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
const loginOptionsPath = "/api/login-options"
const sessionRefreshPath = "/api/session/refresh"

// sessionRefreshGrace is how far past a session token's stated exp POST
// /api/session/refresh still honors it — the sliding-session renewal window
// (edge-browser-node-design.md's token-refresh-on-reconnect). The proactive refresh
// loop the browser-native shell runs (boot.mjs) renews well before expiry in
// the ordinary case, so this grace exists for the case that loop itself was
// delayed — a backgrounded tab whose timers the browser throttled, a laptop
// that slept — not as the normal renewal path.
const sessionRefreshGrace = 5 * time.Minute

// isSessionAuthExempt reports whether path is reachable with no session
// cookie at all — the login page, the endpoints that establish/clear one,
// and /api/claim (a not-yet-claimed identity has no session to present;
// that ceremony is how a fresh user gets into the system in the first
// place). /api/session/refresh belongs here too: it is reachable with an
// already-expired (within grace) cookie by design, so it does its own
// verification (handleSessionRefresh, against the grace-tolerant
// refreshAuthn) rather than requireSession's strict one. Mirrors
// cmd/loupe/readauth.go's isOperatorAuthExempt.
func isSessionAuthExempt(path string) bool {
	switch path {
	case loginPagePath, devLoginPath, logoutPath, whoamiPath, loginOptionsPath, sessionRefreshPath, "/api/claim":
		return true
	default:
		return false
	}
}

// demoPersona is one entry of FACET_DEMO_PERSONAS — the hosted-demo login
// posture (deploy/demo): a curated, seed-derived identity the login page
// offers as a one-tap sign-in card. While the list is non-empty these are
// also the ONLY subjects handleDevLogin will mint for, and /api/claim is
// disabled: the demo world's residents are fixed, so the open any-subject
// minter and the claim ceremony's write surface both stay unreachable from
// the proxied public listener.
type demoPersona struct {
	// ID is the persona's bare identity NanoID (a "vtx.identity." prefix is
	// tolerated and stripped at parse).
	ID string `json:"id"`
	// Label is the card's headline (e.g. the seeded resident's name).
	Label string `json:"label"`
	// Sub is an optional second line (e.g. "Resident · Unit 1").
	Sub string `json:"sub,omitempty"`
}

// parseDemoPersonas parses the FACET_DEMO_PERSONAS env value. Empty input is
// the non-demo posture (nil list). Every entry must carry a valid bare
// NanoID and a label — a malformed list fails startup rather than silently
// widening the fence to nothing.
func parseDemoPersonas(raw string) ([]demoPersona, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var personas []demoPersona
	if err := json.Unmarshal([]byte(raw), &personas); err != nil {
		return nil, fmt.Errorf("FACET_DEMO_PERSONAS: %w", err)
	}
	if len(personas) == 0 {
		return nil, fmt.Errorf("FACET_DEMO_PERSONAS: set but names no personas")
	}
	for i := range personas {
		personas[i].ID = strings.TrimPrefix(strings.TrimSpace(personas[i].ID), "vtx.identity.")
		if !substrate.IsValidNanoID(personas[i].ID) {
			return nil, fmt.Errorf("FACET_DEMO_PERSONAS[%d]: id must be a 20-character NanoID", i)
		}
		if strings.TrimSpace(personas[i].Label) == "" {
			return nil, fmt.Errorf("FACET_DEMO_PERSONAS[%d]: label is required", i)
		}
	}
	return personas, nil
}

// personaAllowed reports whether bareID may sign in under the current
// posture: an empty persona list allows any identity (the dev default), a
// non-empty one allows exactly its members.
func (s *server) personaAllowed(bareID string) bool {
	if len(s.personas) == 0 {
		return true
	}
	for _, p := range s.personas {
		if p.ID == bareID {
			return true
		}
	}
	return false
}

// handleLoginOptions implements GET /api/login-options — the login page's
// probe for the demo-persona posture. Always answers (an empty list means
// "no personas configured; show the free-form dev sign-in"), so the page
// needs no other configuration channel.
func (s *server) handleLoginOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	personas := s.personas
	if personas == nil {
		personas = []demoPersona{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"personas": personas})
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
// the resolved session under.
type sessionIdentityContextKey struct{}

// sessionInfo is what requireSession resolved for a request: which identity,
// and whether a real session COOKIE authenticated it as opposed to the
// boot-env single-user fallback. The distinction is load-bearing — a
// fallback session proves nothing about who the caller is, so it must not
// reach a per-user surface (see handleCredentials).
type sessionInfo struct {
	identityID string
	viaCookie  bool
}

func sessionIdentity(ctx context.Context) (string, bool) {
	si, ok := ctx.Value(sessionIdentityContextKey{}).(sessionInfo)
	return si.identityID, ok && si.identityID != ""
}

// sessionViaCookie reports whether this request's identity was proven by a
// verified session cookie rather than inherited from the boot fallback.
func sessionViaCookie(ctx context.Context) bool {
	si, ok := ctx.Value(sessionIdentityContextKey{}).(sessionInfo)
	return ok && si.viaCookie
}

// resolveSessionIdentity verifies the session cookie's token (when s.authn
// is configured) and returns the bare identity id it authenticates. Falls
// back to s.bootIdentityID — the boot-env EDGE_IDENTITY_ID, now an optional
// single-user fallback per design §7.2 — when no cookie verifies, so a
// deployment that never enables FACET_DEV_AUTH, or a browser that hasn't
// logged in yet, keeps working exactly as Fire 2/3 did: one process, one
// identity.
func (s *server) resolveSessionIdentity(r *http.Request) (sessionInfo, bool) {
	if s.authn != nil {
		if tok := sessionCookieToken(r); tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if actor, err := s.authn.Authenticate(ctx, tok); err == nil && actor.Subject != "" {
				return sessionInfo{identityID: actor.Subject, viaCookie: true}, true
			}
			// A cookie that is PRESENT but does not verify fails CLOSED. It
			// must never fall through to the boot identity: a session whose
			// token merely expired (30m TTL) would silently become someone
			// else, and the UI would keep claiming to be the signed-in user
			// while acting as the boot identity — an UnlinkCredential then
			// strips the WRONG identity's sign-in method. Absent cookie is
			// the only case the fallback answers.
			return sessionInfo{}, false
		}
	}
	if s.bootIdentityID != "" {
		return sessionInfo{identityID: s.bootIdentityID}, true
	}
	return sessionInfo{}, false
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
		si, ok := s.resolveSessionIdentity(r)
		if !ok {
			if isBrowserNavigation(r) {
				http.Redirect(w, r, loginPagePath, http.StatusFound)
				return
			}
			s.writeError(w, http.StatusUnauthorized, "login required (no valid session cookie and no boot identity configured)")
			return
		}
		ctx := context.WithValue(r.Context(), sessionIdentityContextKey{}, si)
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
	// The minter signs ANY subject it is handed, and that subject goes on to
	// name a file under FACET_STORE_DIR (engine.go) — including one this
	// process later DELETES on sign-out (engineManager.Purge). An id is a
	// Contract #1 NanoID or it is not an identity; refusing anything else
	// here keeps a caller-supplied string from ever reaching a path.
	if !substrate.IsValidNanoID(bareID) {
		s.writeError(w, http.StatusBadRequest, "identityId must be a 20-character NanoID")
		return
	}
	if !s.personaAllowed(bareID) {
		s.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
		return
	}
	token, exp, err := s.devSigner.mint(bareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint session token: "+err.Error())
		return
	}

	// Resolve the credential to the identity it is BOUND to (design §4.1
	// step 2's whoami beat: GET /v1/actor → {actorId, resolvedActorId}).
	// Signing in with a second, linked sign-in method — exactly what Inc 3's
	// own /api/credentials/link mints — must open THAT identity's world, not
	// the bare credential's empty one: otherwise "manage sign-in methods"
	// links a credential that cannot sign in, and the Gateway would resolve
	// the write path to U while this session's engine still synced A2's
	// slice. Resolution happens once, here at login, rather than per
	// request: the cookie carries the RESOLVED identity's own token, so the
	// engine's NATS connection and every Gateway write already authenticate
	// as U with no further round trip.
	resolved, rerr := s.resolveActorIdentity(r.Context(), token)
	if rerr != nil {
		// Fail OPEN to the raw credential — the documented deny-safe
		// fallback (mirrors cmd/loftspace-app/readauth.go's resolve and the
		// Gateway's own resolveActor): an unresolved binding grants nothing
		// extra, it just signs in as the credential itself.
		s.logger.Error("facet: credential-binding resolve failed; signing in as the raw credential", "actor", bareID, "error", rerr)
	} else if resolved != "" && resolved != bareID {
		// The persona fence applies to the identity the session actually
		// opens, not just the credential typed in — a linked credential that
		// resolves outside the persona list must not become a side door.
		if !s.personaAllowed(resolved) {
			s.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
			return
		}
		token, exp, err = s.devSigner.mint(resolved)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "mint resolved session token: "+err.Error())
			return
		}
		s.logger.Info("facet: signed in via a bound credential", "credential", bareID, "identityId", resolved)
		bareID = resolved
	}

	s.setSessionCookie(w, token, exp)
	s.writeJSON(w, http.StatusOK, map[string]any{"identityId": bareID, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// actorResponse is the Gateway's GET /v1/actor (whoami) body — the shipped
// multi-credential Fire 2 surface the design names as Facet's hard
// dependency for credential→identity resolution (§4.1 step 2; gap G10).
type actorResponse struct {
	ActorID         string `json:"actorId"`
	ResolvedActorID string `json:"resolvedActorId"`
}

// resolveActorIdentity asks the Gateway which identity token's credential is
// bound to, returning the bare resolved identity id — empty when the Gateway
// reports no distinct binding (an identity signing in as itself). Facet
// deliberately does NOT read the credential-bindings KV bucket the way
// cmd/loftspace-app does: design §4.5 binds this app to "never reads
// platform buckets"; its only platform surface is the Gateway's own
// external door.
func (s *server) resolveActorIdentity(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.gatewayURL+"/v1/actor", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway whoami: HTTP %d", resp.StatusCode)
	}
	var body actorResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxClaimBodyBytes)).Decode(&body); err != nil {
		return "", err
	}
	if body.ResolvedActorID == "" || body.ResolvedActorID == body.ActorID {
		return "", nil
	}
	return strings.TrimPrefix(body.ResolvedActorID, "vtx.identity."), nil
}

// handleLogout implements POST /api/logout — clears the session cookie.
// Exempted from requireSession so a stale or invalid cookie can always be
// cleared rather than trapping the browser.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// Purge the signed-in identity's local mirror (§4.4). Resolve from the
	// COOKIE only, never the boot fallback: a boot-env deployment's identity
	// is the process's own, not this browser's to erase.
	if s.authn != nil {
		if tok := sessionCookieToken(r); tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			if actor, err := s.authn.Authenticate(ctx, tok); err == nil && actor.Subject != "" && actor.Subject != s.bootIdentityID {
				if err := s.engines.Purge(actor.Subject); err != nil {
					// The cookie still clears — a mirror we failed to delete
					// must not trap the user in a session they asked to
					// leave. Loud, because it IS the §4.4 residual.
					s.logger.Error("facet: purge local mirror on sign-out failed", "identityId", actor.Subject, "error", err)
				}
			}
			cancel()
		}
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
	si, ok := s.resolveSessionIdentity(r)
	if !ok {
		s.writeJSON(w, http.StatusOK, map[string]bool{"loggedIn": false})
		return
	}
	// canSignOut distinguishes a real cookie session from the boot-env
	// single-user fallback, which no cookie authenticates and therefore no
	// logout can end. Without it the two collapse and a boot deployment
	// traps the browser in a loop: sign-out clears a cookie nobody used,
	// whoami still answers loggedIn (via the fallback), and the login page
	// bounces straight back into the app.
	s.writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn":   true,
		"identityId": si.identityID,
		"canSignOut": si.viaCookie,
	})
}

// sessionRefreshResponse is what POST /api/session/refresh returns: the
// fresh bearer token, in the raw. Browser-native mode's boot.mjs needs the
// literal value — it drives nats.js and the wasm host's Gateway submitter
// directly, the same trust window.__EDGE_BOOT__'s initial injection already
// extends the browser (bootConfigForSession) — alongside re-setting the
// HttpOnly cookie so a plain page navigation stays signed in too.
type sessionRefreshResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// handleSessionRefresh implements POST /api/session/refresh — the
// sliding-session renewal endpoint (edge-browser-node-design.md's
// token-refresh-on-reconnect). It verifies the current session
// cookie with s.refreshAuthn's sessionRefreshGrace tolerance (wider than
// every other session-gated request's strict default), then mints a fresh
// token for the SAME identity and re-sets the cookie. It deliberately does
// NOT re-run handleDevLogin's credential-binding resolution — that resolves
// WHICH identity a login opens, a decision a refresh of an already-open
// session never revisits.
func (s *server) handleSessionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.refreshAuthn == nil {
		s.writeError(w, http.StatusNotFound, "refresh is disabled (FACET_DEV_AUTH not set)")
		return
	}
	tok := sessionCookieToken(r)
	if tok == "" {
		s.writeError(w, http.StatusUnauthorized, "no session to refresh")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	actor, err := s.refreshAuthn.Authenticate(ctx, tok)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "session cannot be refreshed; sign in again")
		return
	}
	if !s.personaAllowed(actor.Subject) {
		// The fence can only have narrowed since login (an operator edits
		// FACET_DEMO_PERSONAS and restarts, which re-reads it at boot) —
		// never let a stale session's refresh widen what it can still do.
		s.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
		return
	}
	token, exp, err := s.devSigner.mint(actor.Subject)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint refreshed session token: "+err.Error())
		return
	}
	s.setSessionCookie(w, token, exp)
	s.writeJSON(w, http.StatusOK, sessionRefreshResponse{Token: token, ExpiresAt: exp.UTC().Format(time.RFC3339)})
}

// setupSessionAuthn builds the two verifiers session cookies use — the same
// shared dev key signer signs with, loaded via the identical DevMode:true
// path setupDevSigner/cmd/loupe's setupOperatorAuth already use, so a token
// minted here, by /api/claim's throwaway credential, or by
// handleSessionRefresh, all verify against both. strict enforces
// auth.DefaultClockSkew and backs requireSession's ordinary per-request
// check; refresh additionally tolerates sessionRefreshGrace past a token's
// exp and backs ONLY handleSessionRefresh (see its doc). Both are
// (nil, nil, nil) when signer is nil — session verification is meaningless
// without a signer to have minted the cookie in the first place.
func setupSessionAuthn(logger *slog.Logger, signer *devSigner) (strict, refresh *auth.Authenticator, err error) {
	if signer == nil {
		return nil, nil, nil
	}
	trustedKeys, trustedSpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv("FACET_DEV_PUBLIC_KEY_PATH"),
	}, func(msg string) { logger.Warn(msg) })
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: load shared dev trust key: %w", err)
	}
	keyInfo := auth.KeyInfoFromSpecs(trustedSpecs)
	verifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session verifier: %w", err)
	}
	refreshVerifier, err := auth.NewVerifier(auth.Config{Keys: trustedKeys, KeyInfo: keyInfo, ClockSkew: sessionRefreshGrace})
	if err != nil {
		return nil, nil, fmt.Errorf("dev-auth: build session refresh verifier: %w", err)
	}
	return auth.NewAuthenticator(verifier, nil), auth.NewAuthenticator(refreshVerifier, nil), nil
}
