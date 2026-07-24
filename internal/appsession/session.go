// Package appsession is the shared browser-session kit every Lattice
// front-end binary signs users in with: a login page, a demo-posture login
// endpoint fenced to a persona list, an HttpOnly session cookie carrying the
// same JWT the Gateway already verifies on every write, a sliding-session
// refresh endpoint, logout, and the middleware that resolves each request to
// a signed-in identity.
//
// The kit owns the CAPABILITY of signing in; each app owns the UX around it
// (its own login page bytes, its own extra exempt paths, its own post-logout
// cleanup). It never reads a platform bucket: the only outbound call is to
// the Gateway's own external /v1/actor door, so an adopting app gains no new
// data-plane dependency.
//
// Production posture is verify-only — with no Signer configured, the minting
// endpoints report 404 and only externally-issued tokens open a session. A
// real IdP plugs in here.
package appsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The session surface's routes. They are fixed rather than configurable:
// every app's login page and boot script addresses them by these literals.
const (
	LoginPagePath    = "/login"
	DevLoginPath     = "/api/dev-login"
	LogoutPath       = "/api/logout"
	WhoamiPath       = "/api/whoami"
	LoginOptionsPath = "/api/login-options"
	RefreshPath      = "/api/session/refresh"
)

// RefreshGrace is how far past a session token's stated exp POST
// /api/session/refresh still honors it — the sliding-session renewal window.
// The proactive refresh loop a browser shell runs renews well before expiry
// in the ordinary case, so this grace exists for the case that loop itself
// was delayed — a backgrounded tab whose timers the browser throttled, a
// laptop that slept — not as the normal renewal path.
const RefreshGrace = 5 * time.Minute

// maxBodyBytes bounds a session endpoint's request body.
const maxBodyBytes = 1 << 20

// Config wires one app's session surface.
type Config struct {
	// AppName names the app in log lines and derives the session cookie's
	// name (<AppName>_session). Required.
	AppName string
	// EnvPrefix names the dev-auth env var (<EnvPrefix>_DEV_AUTH) the login
	// page's operator-facing "disabled" messages cite — the same prefix the
	// app passed to NewDevSigner. Required when Signer/RefreshAuthn can be
	// nil in production (i.e. whenever dev auth is a real possibility for
	// this app), so an operator staring at web/login.html's rendered error
	// sees exactly the env var to set, not a prefix-less generic message.
	EnvPrefix string
	Logger    *slog.Logger
	// GatewayURL is the base URL of the Gateway whose /v1/actor answers
	// which identity a credential is bound to. Required when Signer is set.
	GatewayURL string
	// Signer mints session tokens (demo posture). Nil ⇒ login and refresh
	// report 404 and no cookie can be issued in-process.
	Signer *Signer
	// Authn verifies a session cookie on every request; RefreshAuthn is its
	// grace-tolerant sibling backing only the refresh endpoint. Both nil
	// under the same condition as Signer — see NewAuthenticators.
	Authn        *auth.Authenticator
	RefreshAuthn *auth.Authenticator
	// Loopback gates the cookie's Secure flag: a loopback bind is served
	// over plain http, where a Secure cookie would never be sent back.
	Loopback bool
	// FallbackIdentityID, when set, is the single-user boot identity a
	// request with NO cookie at all resolves to — the posture an app runs in
	// before anyone logs in. A cookie that is present but does not verify
	// never falls back to it (see resolve).
	FallbackIdentityID string
	// Personas, when non-empty, fences sign-in to exactly these identities.
	Personas []Persona
	// LoginPage is the HTML the login route serves. Required.
	LoginPage []byte
	// ExtraExemptPaths are app-specific routes reachable with no session at
	// all — an app's own claim ceremony, for instance, which by definition
	// runs before its caller has an identity to present.
	ExtraExemptPaths []string
	// OnSignOut, when set, runs at logout for the identity a verified cookie
	// named, unless that identity is FallbackIdentityID (the process's own,
	// not this browser's to tear down). Its error is logged, never
	// surfaced — a cleanup that failed must not trap the user in a session
	// they asked to leave.
	OnSignOut func(identityID string) error
	// HTTPClient calls the Gateway; nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Manager serves one app's session surface.
type Manager struct {
	cfg        Config
	cookieName string
	exempt     map[string]bool
	httpClient *http.Client
}

// New validates cfg and builds the session surface.
func New(cfg Config) (*Manager, error) {
	if strings.TrimSpace(cfg.AppName) == "" {
		return nil, errors.New("appsession: AppName is required")
	}
	if len(cfg.LoginPage) == 0 {
		return nil, errors.New("appsession: LoginPage is required")
	}
	if cfg.Signer != nil && strings.TrimSpace(cfg.GatewayURL) == "" {
		return nil, errors.New("appsession: GatewayURL is required when a Signer is configured")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	m := &Manager{
		cfg:        cfg,
		cookieName: cfg.AppName + "_session",
		exempt:     map[string]bool{},
		httpClient: cfg.HTTPClient,
	}
	if m.httpClient == nil {
		m.httpClient = http.DefaultClient
	}
	// The login page, the endpoints that establish or clear a session, and
	// whatever the app named. Refresh belongs here too: it is reachable with
	// an already-expired (within grace) cookie by design, so it does its own
	// verification against RefreshAuthn rather than the strict one.
	for _, p := range []string{LoginPagePath, DevLoginPath, LogoutPath, WhoamiPath, LoginOptionsPath, RefreshPath} {
		m.exempt[p] = true
	}
	for _, p := range cfg.ExtraExemptPaths {
		m.exempt[p] = true
	}
	return m, nil
}

// RegisterRoutes binds the session surface onto mux.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(LoginPagePath, m.handleLoginPage)
	mux.HandleFunc(LoginOptionsPath, m.handleLoginOptions)
	mux.HandleFunc(DevLoginPath, m.handleDevLogin)
	mux.HandleFunc(LogoutPath, m.handleLogout)
	mux.HandleFunc(WhoamiPath, m.handleWhoami)
	mux.HandleFunc(RefreshPath, m.handleRefresh)
}

// CookieName is the app's session cookie name.
func (m *Manager) CookieName() string { return m.cookieName }

// HasPersonaFence reports whether the process runs the demo-persona posture,
// which an app's own open-ended ceremonies close behind.
func (m *Manager) HasPersonaFence() bool { return len(m.cfg.Personas) > 0 }

// IsAuthExempt reports whether path is reachable with no session at all.
func (m *Manager) IsAuthExempt(path string) bool { return m.exempt[path] }

// CookieToken returns the raw session token the request carries, if any.
func (m *Manager) CookieToken(r *http.Request) string {
	c, err := r.Cookie(m.cookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

// contextKey is the request-context key RequireSession stores the resolved
// session under.
type contextKey struct{}

// info is what RequireSession resolved for a request: which identity, and
// whether a real session COOKIE authenticated it as opposed to the
// single-user boot fallback. The distinction is load-bearing — a fallback
// session proves nothing about who the caller is, so it must not reach a
// per-user surface.
type info struct {
	identityID string
	viaCookie  bool
}

// Identity returns the signed-in identity RequireSession resolved.
func Identity(ctx context.Context) (string, bool) {
	si, ok := ctx.Value(contextKey{}).(info)
	return si.identityID, ok && si.identityID != ""
}

// ViaCookie reports whether this request's identity was proven by a verified
// session cookie rather than inherited from the boot fallback.
func ViaCookie(ctx context.Context) bool {
	si, ok := ctx.Value(contextKey{}).(info)
	return ok && si.viaCookie
}

// WithSession returns ctx carrying a resolved session — what RequireSession
// installs before any handler runs.
func WithSession(ctx context.Context, identityID string, viaCookie bool) context.Context {
	return context.WithValue(ctx, contextKey{}, info{identityID: identityID, viaCookie: viaCookie})
}

// resolve verifies the session cookie's token (when a verifier is
// configured) and returns the bare identity id it authenticates, falling back
// to FallbackIdentityID when no cookie is present at all — so a deployment
// that never enables dev auth, or a browser that hasn't logged in yet, keeps
// working as one process, one identity.
func (m *Manager) resolve(r *http.Request) (info, bool) {
	if m.cfg.Authn != nil {
		if tok := m.CookieToken(r); tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if actor, err := m.cfg.Authn.Authenticate(ctx, tok); err == nil && actor.Subject != "" {
				return info{identityID: actor.Subject, viaCookie: true}, true
			}
			// A cookie that is PRESENT but does not verify fails CLOSED. It
			// must never fall through to the boot identity: a session whose
			// token merely expired would silently become someone else, and
			// the UI would keep claiming to be the signed-in user while
			// acting as the boot identity — an unlink then strips the WRONG
			// identity's sign-in method. Absent cookie is the only case the
			// fallback answers.
			return info{}, false
		}
	}
	if m.cfg.FallbackIdentityID != "" {
		return info{identityID: m.cfg.FallbackIdentityID}, true
	}
	return info{}, false
}

// RequireSession wraps next so every request resolves to a signed-in
// identity — either a verified session cookie or the boot fallback — before
// reaching a handler that acts on someone's behalf. This is not the security
// boundary: the Gateway verifies every write independently, and this cookie
// only selects WHICH already-entitled identity a request runs as. It still
// 401s or redirects when nothing resolves, because a handler downstream has
// no identity to act for.
func (m *Manager) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.IsAuthExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		si, ok := m.resolve(r)
		if !ok {
			if isBrowserNavigation(r) {
				http.Redirect(w, r, LoginPagePath, http.StatusFound)
				return
			}
			m.writeError(w, http.StatusUnauthorized, "login required (no valid session cookie and no boot identity configured)")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), si.identityID, si.viaCookie)))
	})
}

// isBrowserNavigation reports a top-level browser page load (address bar,
// link click, bookmark) rather than a script/API/curl call.
func isBrowserNavigation(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document")
}

func (m *Manager) personaAllowed(bareID string) bool {
	if len(m.cfg.Personas) == 0 {
		return true
	}
	for _, p := range m.cfg.Personas {
		if p.ID == bareID {
			return true
		}
	}
	return false
}

// handleLoginPage serves the standalone page a fresh browser with no session
// at all can always reach.
func (m *Manager) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(m.cfg.LoginPage); err != nil {
		m.cfg.Logger.Error(m.cfg.AppName+": write login page", "error", err)
	}
}

// handleLoginOptions implements GET /api/login-options — the login page's
// probe for the demo-persona posture. Always answers (an empty list means
// "no personas configured; show the free-form dev sign-in"), so the page
// needs no other configuration channel.
func (m *Manager) handleLoginOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		m.writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	personas := m.cfg.Personas
	if personas == nil {
		personas = []Persona{}
	}
	m.writeJSON(w, http.StatusOK, map[string]any{"personas": personas})
}

func (m *Manager) setCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   !m.cfg.Loopback,
		SameSite: http.SameSiteStrictMode,
	})
}

func (m *Manager) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   !m.cfg.Loopback,
		SameSite: http.SameSiteStrictMode,
	})
}

// devLoginRequest is what the login page POSTs: the bare identity id to sign
// in as (a seeded persona's NanoID, or one a claim ceremony just returned).
type devLoginRequest struct {
	IdentityID string `json:"identityId"`
}

// handleDevLogin implements POST /api/dev-login {identityId} — the demo-only
// login stand-in: mints a token for the requested identity and sets it as the
// session cookie. Available ONLY when dev auth is enabled, because an
// in-process minter that trusts any caller-supplied subject must never be
// reachable off a loopback bind.
func (m *Manager) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if m.cfg.Signer == nil {
		m.writeError(w, http.StatusNotFound, "login is disabled ("+m.cfg.EnvPrefix+"_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		m.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req devLoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		m.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	bareID := strings.TrimPrefix(strings.TrimSpace(req.IdentityID), "vtx.identity.")
	if bareID == "" {
		m.writeError(w, http.StatusBadRequest, "identityId is required")
		return
	}
	// The minter signs ANY subject it is handed, and that subject goes on to
	// name per-identity local state an app may later delete on sign-out. An
	// id is a Contract #1 NanoID or it is not an identity; refusing anything
	// else here keeps a caller-supplied string from ever reaching a path.
	if !substrate.IsValidNanoID(bareID) {
		m.writeError(w, http.StatusBadRequest, "identityId must be a 20-character NanoID")
		return
	}
	if !m.personaAllowed(bareID) {
		m.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
		return
	}
	token, exp, err := m.cfg.Signer.Mint(bareID)
	if err != nil {
		m.writeError(w, http.StatusInternalServerError, "mint session token: "+err.Error())
		return
	}

	// Resolve the credential to the identity it is BOUND to (the Gateway's
	// whoami beat). Signing in with a second, linked sign-in method must open
	// THAT identity's world, not the bare credential's empty one — otherwise
	// linking a credential produces one that cannot sign in. Resolution
	// happens once, here at login: the cookie then carries the RESOLVED
	// identity's own token, so every downstream call already authenticates as
	// that identity with no further round trip.
	resolved, rerr := m.resolveActorIdentity(r.Context(), token)
	if rerr != nil {
		// Fail OPEN to the raw credential — the documented deny-safe
		// fallback: an unresolved binding grants nothing extra, it just signs
		// in as the credential itself.
		m.cfg.Logger.Error(m.cfg.AppName+": credential-binding resolve failed; signing in as the raw credential", "actor", bareID, "error", rerr)
	} else if resolved != "" && resolved != bareID {
		// The persona fence applies to the identity the session actually
		// opens, not just the credential typed in — a linked credential that
		// resolves outside the persona list must not become a side door.
		if !m.personaAllowed(resolved) {
			m.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
			return
		}
		token, exp, err = m.cfg.Signer.Mint(resolved)
		if err != nil {
			m.writeError(w, http.StatusInternalServerError, "mint resolved session token: "+err.Error())
			return
		}
		m.cfg.Logger.Info(m.cfg.AppName+": signed in via a bound credential", "credential", bareID, "identityId", resolved)
		bareID = resolved
	}

	m.setCookie(w, token, exp)
	m.writeJSON(w, http.StatusOK, map[string]any{"identityId": bareID, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// actorResponse is the Gateway's GET /v1/actor (whoami) body, decoded for the
// one field login needs.
type actorResponse struct {
	ActorID         string `json:"actorId"`
	ResolvedActorID string `json:"resolvedActorId"`
}

// resolveActorIdentity asks the Gateway which identity a token's credential
// is bound to, returning the bare resolved identity id — empty when the
// Gateway reports no distinct binding (an identity signing in as itself). The
// kit deliberately does not read the credential-bindings bucket directly: the
// Gateway's own external door is its only platform surface.
func (m *Manager) resolveActorIdentity(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.GatewayURL+"/v1/actor", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway whoami: HTTP %d", resp.StatusCode)
	}
	var body actorResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return "", err
	}
	if body.ResolvedActorID == "" || body.ResolvedActorID == body.ActorID {
		return "", nil
	}
	return strings.TrimPrefix(body.ResolvedActorID, "vtx.identity."), nil
}

// handleLogout implements POST /api/logout — clears the session cookie.
// Exempt from RequireSession so a stale or invalid cookie can always be
// cleared rather than trapping the browser.
func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		m.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// Run the app's sign-out cleanup for the COOKIE's identity only, never
	// the boot fallback: a boot-env deployment's identity is the process's
	// own, not this browser's to erase.
	if m.cfg.OnSignOut != nil && m.cfg.Authn != nil {
		if tok := m.CookieToken(r); tok != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			if actor, err := m.cfg.Authn.Authenticate(ctx, tok); err == nil && actor.Subject != "" && actor.Subject != m.cfg.FallbackIdentityID {
				if err := m.cfg.OnSignOut(actor.Subject); err != nil {
					// The cookie still clears — cleanup we failed to run must
					// not trap the user in a session they asked to leave.
					m.cfg.Logger.Error(m.cfg.AppName+": sign-out cleanup failed", "identityId", actor.Subject, "error", err)
				}
			}
			cancel()
		}
	}
	m.clearCookie(w)
	m.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleWhoami implements GET /api/whoami — the session's who-am-I UX. Never
// errors: an absent session just reports loggedIn:false, so the login page
// can safely probe it before any cookie exists.
func (m *Manager) handleWhoami(w http.ResponseWriter, r *http.Request) {
	si, ok := m.resolve(r)
	if !ok {
		m.writeJSON(w, http.StatusOK, map[string]bool{"loggedIn": false})
		return
	}
	// canSignOut distinguishes a real cookie session from the boot-env
	// single-user fallback, which no cookie authenticates and therefore no
	// logout can end. Without it the two collapse and a boot deployment traps
	// the browser in a loop: sign-out clears a cookie nobody used, whoami
	// still answers loggedIn (via the fallback), and the login page bounces
	// straight back into the app.
	m.writeJSON(w, http.StatusOK, map[string]any{
		"loggedIn":   true,
		"identityId": si.identityID,
		"canSignOut": si.viaCookie,
	})
}

// refreshResponse is what POST /api/session/refresh returns: the fresh bearer
// token, in the raw. A browser-native shell needs the literal value — it
// drives its own NATS connection and Gateway submitter — alongside the
// re-set HttpOnly cookie so a plain page navigation stays signed in too.
type refreshResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// handleRefresh implements POST /api/session/refresh — the sliding-session
// renewal endpoint. It verifies the current cookie with RefreshGrace
// tolerance (wider than every other session-gated request's strict default),
// then mints a fresh token for the SAME identity and re-sets the cookie. It
// deliberately does NOT re-run login's credential-binding resolution — that
// resolves WHICH identity a login opens, a decision a refresh of an
// already-open session never revisits.
func (m *Manager) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		m.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// Gate on BOTH: an adopter can wire RefreshAuthn (verify-only, the
	// documented production posture) with no Signer at all, and the mint
	// below would nil-deref a nil *Signer if this only checked RefreshAuthn.
	if m.cfg.RefreshAuthn == nil || m.cfg.Signer == nil {
		m.writeError(w, http.StatusNotFound, "refresh is disabled ("+m.cfg.EnvPrefix+"_DEV_AUTH not set)")
		return
	}
	tok := m.CookieToken(r)
	if tok == "" {
		m.writeError(w, http.StatusUnauthorized, "no session to refresh")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	actor, err := m.cfg.RefreshAuthn.Authenticate(ctx, tok)
	if err != nil {
		m.writeError(w, http.StatusUnauthorized, "session cannot be refreshed; sign in again")
		return
	}
	if !m.personaAllowed(actor.Subject) {
		// The fence can only have narrowed since login (an operator edits the
		// persona list and restarts, which re-reads it at boot) — never let a
		// stale session's refresh widen what it can still do.
		m.writeError(w, http.StatusForbidden, "this deployment only signs in the listed demo personas")
		return
	}
	token, exp, err := m.cfg.Signer.Mint(actor.Subject)
	if err != nil {
		m.writeError(w, http.StatusInternalServerError, "mint refreshed session token: "+err.Error())
		return
	}
	m.setCookie(w, token, exp)
	m.writeJSON(w, http.StatusOK, refreshResponse{Token: token, ExpiresAt: exp.UTC().Format(time.RFC3339)})
}

func (m *Manager) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		m.cfg.Logger.Error(m.cfg.AppName+": encode response failed", "err", err)
	}
}

func (m *Manager) writeError(w http.ResponseWriter, status int, msg string) {
	m.writeJSON(w, status, map[string]string{"error": msg})
}
