package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes.
func TestMain(m *testing.M) {
	os.Setenv("LOUPE_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv("LOUPE_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer abc.def.ghi", "abc.def.ghi"}, // case-insensitive scheme
		{"Bearer   spaced  ", "spaced"},
		{"Basic abc", ""},
		{"abc.def.ghi", ""},
		{"", ""},
		{"Bearer ", ""}, // scheme only, no token
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerToken(r); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "x"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}

// TestSetupOperatorAuth_DevPosture proves the demo posture wires a verifier
// whose trust matches the minter — a token the signer mints verifies.
func TestSetupOperatorAuth_DevPosture(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "1")
	authn, signer, err := setupOperatorAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupOperatorAuth: %v", err)
	}
	if authn == nil || signer == nil {
		t.Fatalf("dev posture must return non-nil authn (%v) and signer (%v)", authn, signer)
	}

	const sub = "Hj4kPmRtw9nbCxz5vQ2y"
	tok, _, err := signer.mint(sub)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	actor, err := authn.Authenticate(t.Context(), tok)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != sub {
		t.Errorf("subject = %q, want %q", actor.Subject, sub)
	}
}

// TestSetupOperatorAuth_DevAuth_RefusesNonLoopback pins the defense-in-depth
// guard: dev-auth trusts any caller-asserted subject, so it must never be
// reachable off-host even if an operator misconfigures a non-loopback bind.
func TestSetupOperatorAuth_DevAuth_RefusesNonLoopback(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "1")
	if _, _, err := setupOperatorAuth(discardLogger(), false); err == nil {
		t.Fatal("expected an error enabling dev-auth on a non-loopback bind")
	}
}

// TestSetupOperatorAuth_NoPosture: neither env set ⇒ no authenticator (fail
// closed) — the correct default for a console whose operator login is not
// provisioned.
func TestSetupOperatorAuth_NoPosture(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "")
	t.Setenv("LOUPE_JWT_PUBLIC_KEY", "")
	authn, signer, err := setupOperatorAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupOperatorAuth: %v", err)
	}
	if authn != nil || signer != nil {
		t.Fatalf("no posture must return nil authn/signer, got authn=%v signer=%v", authn, signer)
	}
}

// TestSetupOperatorAuth_BadPublicKey: a configured but unparseable key is a
// hard misconfiguration, not a silent deny-all.
func TestSetupOperatorAuth_BadPublicKey(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "")
	t.Setenv("LOUPE_JWT_PUBLIC_KEY", "not a pem")
	if _, _, err := setupOperatorAuth(discardLogger(), true); err == nil {
		t.Fatal("expected an error for an unparseable public key")
	}
}

// devAuthServer builds a server with the demo auth posture wired, an operator
// actor key set, and a nil NATS conn — for gate/handler tests that don't need
// a live connection.
func devAuthServer(t *testing.T) *server {
	t.Helper()
	t.Setenv("LOUPE_DEV_AUTH", "1")
	authn, signer, err := setupOperatorAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupOperatorAuth: %v", err)
	}
	return &server{
		logger:           discardLogger(),
		authn:            authn,
		devSigner:        signer,
		operatorActorKey: "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		natsTimeout:      time.Second,
	}
}

func TestRequireOperator_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: time.Second} // authn nil
	called := false
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/systemmap", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run without a valid operator token")
	}
}

func TestRequireOperator_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	called := false
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/systemmap", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run without a bearer token")
	}
}

func TestRequireOperator_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run with a forged token")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

func TestRequireOperator_ValidToken_PassesThrough(t *testing.T) {
	s := devAuthServer(t)
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	called := false
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	gate.ServeHTTP(rec, r)
	if !called {
		t.Fatal("next handler must run with a valid operator token")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestRequireOperator_DevTokenPath_Bypasses proves the one deliberate
// exemption: the minting endpoint itself must be reachable without a token
// (a caller has none yet), even when no auth is configured elsewhere.
func TestRequireOperator_DevTokenPath_Bypasses(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: time.Second} // authn nil
	called := false
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil))
	if !called {
		t.Fatal("the dev-token path must bypass the operator gate")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleOperatorDevToken_Disabled_404(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: time.Second} // devSigner nil
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (dev-token disabled)", rec.Code)
	}
}

func TestHandleOperatorDevToken_WrongMethod_405(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleOperatorDevToken_CrossOrigin_403(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	r.Header.Set("Origin", "https://evil.example")
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-origin mint request blocked)", rec.Code)
	}
}

func TestHandleOperatorDevToken_WrongVertexType_500(t *testing.T) {
	s := devAuthServer(t)
	s.operatorActorKey = "vtx.meta.Hj4kPmRtw9nbCxz5vQ2y" // not a vtx.identity.<id> key
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (operator actor key is not a vtx.identity key)", rec.Code)
	}
}

func TestHandleOperatorDevToken_NoOperatorActor_502(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "1")
	authn, signer, err := setupOperatorAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupOperatorAuth: %v", err)
	}
	s := &server{logger: discardLogger(), authn: authn, devSigner: signer, natsTimeout: time.Second} // operatorActorKey empty
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no operator actor configured)", rec.Code)
	}
}

func TestHandleOperatorDevToken_Mint_RoundTrips(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" || resp.ExpiresAt == "" {
		t.Fatalf("empty token/expiresAt: %+v", resp)
	}
	actor, err := s.authn.Authenticate(t.Context(), resp.Token)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Errorf("subject = %q, want the bare operator-actor NanoID", actor.Subject)
	}
}

// TestFullMux_GatedEndToEnd proves the actual production wiring — mux wrapped
// by requireOperator, exactly as main.go builds it — denies an unauthenticated
// request to the static UI and to an API route, and admits one bearing a
// dev-minted operator token.
func TestFullMux_GatedEndToEnd(t *testing.T) {
	s := devAuthServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	gated := s.requireOperator(mux)

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated static UI request: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/systemmap", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API request: status = %d, want 401", rec.Code)
	}

	mintRec := httptest.NewRecorder()
	gated.ServeHTTP(mintRec, httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil))
	if mintRec.Code != http.StatusOK {
		t.Fatalf("dev-token mint through the gated mux: status = %d, want 200", mintRec.Code)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mintRec.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode mint response: %v", err)
	}

	rec = httptest.NewRecorder()
	authed := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	authed.Header.Set("Authorization", "Bearer "+minted.Token)
	gated.ServeHTTP(rec, authed)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("authenticated API request still 401'd: body=%s", rec.Body.String())
	}
}

func TestIsOperatorAuthExempt(t *testing.T) {
	exempt := []string{operatorDevTokenPath, operatorSessionPath, operatorLogoutPath, loginPagePath}
	for _, p := range exempt {
		if !isOperatorAuthExempt(p) {
			t.Errorf("isOperatorAuthExempt(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/", "/api/systemmap", "/js/main.js", "/login/"} {
		if isOperatorAuthExempt(p) {
			t.Errorf("isOperatorAuthExempt(%q) = true, want false", p)
		}
	}
}

func TestIsBrowserNavigation(t *testing.T) {
	cases := []struct {
		method string
		dest   string
		want   bool
	}{
		{http.MethodGet, "document", true},
		{http.MethodGet, "Document", true}, // header values are case-insensitive
		{http.MethodGet, "empty", false},   // a fetch()/XHR call, not a navigation
		{http.MethodGet, "", false},        // curl/httptest send no Sec-Fetch-Dest
		{http.MethodPost, "document", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/", nil)
		if c.dest != "" {
			r.Header.Set("Sec-Fetch-Dest", c.dest)
		}
		if got := isBrowserNavigation(r); got != c.want {
			t.Errorf("isBrowserNavigation(method=%s, dest=%q) = %v, want %v", c.method, c.dest, got, c.want)
		}
	}
}

// TestRequireOperator_SessionCookie_PassesThrough proves the cookie transport
// authenticates identically to the Authorization header — the browser-usable
// half of the same credential (a plain page load cannot carry a custom
// header, but the browser attaches a cookie automatically).
func TestRequireOperator_SessionCookie_PassesThrough(t *testing.T) {
	s := devAuthServer(t)
	tok, exp, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	called := false
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookieName, Value: tok, Expires: exp})
	gate.ServeHTTP(rec, r)
	if !called {
		t.Fatal("next handler must run with a valid session cookie")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRequireOperator_ForgedCookie_401(t *testing.T) {
	s := devAuthServer(t)
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run with a forged session cookie")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookieName, Value: "not.a.valid.jwt"})
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestRequireOperator_BrowserNavigation_RedirectsToLogin proves the actual
// fix: an unauthenticated top-level page load bounces to /login instead of
// surfacing a bare JSON error a browser can do nothing useful with.
func TestRequireOperator_BrowserNavigation_RedirectsToLogin(t *testing.T) {
	s := devAuthServer(t)
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run without a credential")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (redirect to /login)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != loginPagePath {
		t.Errorf("Location = %q, want %q", loc, loginPagePath)
	}
}

// TestRequireOperator_NonNavigationRequest_Still401 pins that the redirect is
// scoped to real browser navigations — a fetch()/XHR call (or curl, or the
// existing test suite) must keep seeing the plain JSON 401 it already parses.
func TestRequireOperator_NonNavigationRequest_Still401(t *testing.T) {
	s := devAuthServer(t)
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run without a credential")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Sec-Fetch-Dest", "empty") // a fetch()/XHR call, not a navigation
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (non-navigation request)", rec.Code)
	}
}

// TestHandleOperatorDevToken_Mint_SetsSessionCookie proves the dev-login
// button's whole trip: mint sets a browser-usable, loopback-appropriate
// cookie (not Secure on loopback http, or a real browser would drop it) in
// addition to the existing JSON token response.
func TestHandleOperatorDevToken_Mint_SetsSessionCookie(t *testing.T) {
	s := devAuthServer(t)
	s.bindHost = "127.0.0.1" // mirrors main.go's real loopback default
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	s.handleOperatorDevToken(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == operatorSessionCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("dev-token mint must set the operator session cookie")
	}
	if !found.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if found.Secure {
		t.Error("session cookie must not be Secure on a loopback bind (plain http://127.0.0.1 dev use)")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", found.SameSite)
	}
	if _, err := s.authn.Authenticate(t.Context(), found.Value); err != nil {
		t.Errorf("cookie value must be the verifiable minted token: %v", err)
	}
}

func TestHandleLoginPage_ServesHTML(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: time.Second}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, loginPagePath, nil)
	s.handleLoginPage(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if !strings.Contains(rec.Body.String(), "Loupe") {
		t.Error("login page body does not look like the login page")
	}
}

// TestHandleOperatorSession_ValidToken_SetsCookie proves the manual-token
// exchange: an already-verified Bearer token (pasted from a real IdP, or
// minted separately) becomes a usable session cookie. It cannot mint or widen
// anything — verifyOperatorToken is the same check every transport shares.
func TestHandleOperatorSession_ValidToken_SetsCookie(t *testing.T) {
	s := devAuthServer(t)
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`{"token":"`+tok+`"}`))
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == operatorSessionCookieName {
			found = c
		}
	}
	if found == nil || found.Value != tok {
		t.Fatalf("session cookie not set to the presented token: %+v", found)
	}
}

func TestHandleOperatorSession_InvalidToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`{"token":"not.a.valid.jwt"}`))
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (invalid token)", rec.Code)
	}
}

func TestHandleOperatorSession_EmptyToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`{"token":""}`))
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (empty token)", rec.Code)
	}
}

func TestHandleOperatorSession_BadJSON_400(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`not json`))
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (malformed JSON)", rec.Code)
	}
}

func TestHandleOperatorSession_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: time.Second} // authn nil
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`{"token":"anything"}`))
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no auth posture configured)", rec.Code)
	}
}

func TestHandleOperatorSession_WrongMethod_405(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, operatorSessionPath, nil)
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleOperatorSession_CrossOrigin_403(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorSessionPath, strings.NewReader(`{"token":"x"}`))
	r.Header.Set("Origin", "https://evil.example")
	s.handleOperatorSession(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-origin session request blocked)", rec.Code)
	}
}

func TestHandleOperatorLogout_ClearsCookie(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorLogoutPath, nil)
	s.handleOperatorLogout(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == operatorSessionCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("logout must set the session cookie (cleared)")
	}
	if found.MaxAge >= 0 || found.Value != "" {
		t.Errorf("logout cookie not cleared: MaxAge=%d Value=%q", found.MaxAge, found.Value)
	}
}

func TestHandleOperatorLogout_WrongMethod_405(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, operatorLogoutPath, nil)
	s.handleOperatorLogout(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleOperatorLogout_CrossOrigin_403(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, operatorLogoutPath, nil)
	r.Header.Set("Origin", "https://evil.example")
	s.handleOperatorLogout(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-origin logout request blocked)", rec.Code)
	}
}

// TestFullMux_BrowserLoginFlow_EndToEnd proves the actual fix end-to-end: a
// browser with no credential at all can reach /login, an unauthenticated
// top-level navigation to / bounces there automatically, and the dev-login
// mint's cookie — used alone, with NO Authorization header, exactly what a
// real browser can do — passes the same gated mux GET / goes through.
func TestFullMux_BrowserLoginFlow_EndToEnd(t *testing.T) {
	s := devAuthServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	gated := s.requireOperator(mux)

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, loginPagePath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login unauthenticated: status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	nav := httptest.NewRequest(http.MethodGet, "/", nil)
	nav.Header.Set("Sec-Fetch-Dest", "document")
	gated.ServeHTTP(rec, nav)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != loginPagePath {
		t.Fatalf("unauthenticated navigation to /: status = %d, Location = %q, want 302 to %s",
			rec.Code, rec.Header().Get("Location"), loginPagePath)
	}

	mintRec := httptest.NewRecorder()
	gated.ServeHTTP(mintRec, httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil))
	if mintRec.Code != http.StatusOK {
		t.Fatalf("dev-token mint through the gated mux: status = %d, want 200", mintRec.Code)
	}
	var cookie *http.Cookie
	for _, c := range mintRec.Result().Cookies() {
		if c.Name == operatorSessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("dev-token mint through the gated mux must set the session cookie")
	}

	rec = httptest.NewRecorder()
	authed := httptest.NewRequest(http.MethodGet, "/", nil)
	authed.AddCookie(cookie)
	gated.ServeHTTP(rec, authed)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated static UI request: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestVerifyOperatorToken_WrongSubject_Denied pins the login gate to a NAMED
// operator (design intent, §1/§3.1): a validly-signed, non-expired,
// non-empty-subject token that simply names a DIFFERENT identity than the
// one configured as the operator must still be denied. Without this check, a
// token minted for any subject by anything trusting the same key/IdP (e.g.
// another app sharing the dev key, real-actor-write-auth-e2e-design.md §3.2)
// would open the console.
func TestVerifyOperatorToken_WrongSubject_Denied(t *testing.T) {
	s := devAuthServer(t) // operatorActorKey = vtx.identity.Hj4kPmRtw9nbCxz5vQ2y
	tok, _, err := s.devSigner.mint("SomeOtherIdentityNotTheOperator")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := s.verifyOperatorToken(t.Context(), tok); err == nil {
		t.Fatal("expected an error for a validly-signed token naming a different identity")
	}
}

// TestRequireOperator_ValidTokenWrongSubject_401 is the gate-level version of
// the above — the actual attack surface a browser or curl could hit.
func TestRequireOperator_ValidTokenWrongSubject_401(t *testing.T) {
	s := devAuthServer(t)
	tok, _, err := s.devSigner.mint("SomeOtherIdentityNotTheOperator")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run for a token naming a different identity than the configured operator")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (token names a different identity than the configured operator)", rec.Code)
	}
}

// TestVerifyOperatorToken_NoOperatorConfigured_Denied proves a validly-signed
// token cannot pass when Loupe has no configured operator identity at all
// (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded) —
// there is no "anyone" to fall back to.
func TestVerifyOperatorToken_NoOperatorConfigured_Denied(t *testing.T) {
	t.Setenv("LOUPE_DEV_AUTH", "1")
	authn, signer, err := setupOperatorAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupOperatorAuth: %v", err)
	}
	s := &server{logger: discardLogger(), authn: authn, devSigner: signer, natsTimeout: time.Second} // operatorActorKey empty
	tok, _, err := signer.mint("anyone")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := s.verifyOperatorToken(t.Context(), tok); err == nil {
		t.Fatal("expected an error when no operator actor is configured, even with a validly-signed token")
	}
}

// TestAuthenticateConsole_BadHeaderGoodCookie_FallsBack proves the
// precedence fix: a present-but-invalid Authorization header (a stale
// devtools-replayed header, a misbehaving extension) must not mask an
// otherwise-valid session cookie.
func TestAuthenticateConsole_BadHeaderGoodCookie_FallsBack(t *testing.T) {
	s := devAuthServer(t)
	tok, exp, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	r.AddCookie(&http.Cookie{Name: operatorSessionCookieName, Value: tok, Expires: exp})
	actor, gotTok, err := s.authenticateConsole(r)
	if err != nil {
		t.Fatalf("authenticateConsole: %v (a bad header must not mask a good cookie)", err)
	}
	if actor.Subject != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Errorf("subject = %q, want the cookie's subject", actor.Subject)
	}
	if gotTok != tok {
		t.Errorf("returned token = %q, want the cookie's token (what a relay must forward)", gotTok)
	}
}

// TestAuthenticateConsole_GoodHeaderWins proves the header is still tried
// first when both transports carry a valid credential — no behavior change
// for the existing header-only callers (API clients, curl, tests).
func TestAuthenticateConsole_GoodHeaderWins(t *testing.T) {
	s := devAuthServer(t)
	tok, exp, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.AddCookie(&http.Cookie{Name: operatorSessionCookieName, Value: "not.a.valid.jwt", Expires: exp})
	_, gotTok, err := s.authenticateConsole(r)
	if err != nil {
		t.Fatalf("authenticateConsole: %v (a valid header must succeed regardless of a bad cookie)", err)
	}
	if gotTok != tok {
		t.Errorf("returned token = %q, want the header's token", gotTok)
	}
}
