package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/substrate"
)

// buildTestVerifier builds an auth.Authenticator trusting pub under kid,
// bound ModeNanoID — the same binding mode the real dev key uses (session.go's
// setupSessionAuthn), so a token minted for a valid NanoID subject verifies
// exactly as it would against the checked-in shared dev key.
func buildTestVerifier(pub *rsa.PublicKey, kid string) (*auth.Authenticator, error) {
	v, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{kid: pub},
		KeyInfo: map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	if err != nil {
		return nil, err
	}
	return auth.NewAuthenticator(v, nil), nil
}

func testNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

func TestIsSessionAuthExempt(t *testing.T) {
	for _, p := range []string{loginPagePath, devLoginPath, logoutPath, whoamiPath, "/api/claim"} {
		require.True(t, isSessionAuthExempt(p), "path=%s", p)
	}
	for _, p := range []string{"/", "/api/feed", "/api/enqueue", "/app.js"} {
		require.False(t, isSessionAuthExempt(p), "path=%s", p)
	}
}

func TestResolveSessionIdentity_NoSignerNoBootFallsClosed(t *testing.T) {
	srv := &server{logger: slog.Default()}
	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	_, ok := srv.resolveSessionIdentity(r)
	require.False(t, ok)
}

func TestResolveSessionIdentity_BootFallbackWhenNoCookie(t *testing.T) {
	srv := &server{logger: slog.Default(), bootIdentityID: "bootid12345678901234"}
	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	id, ok := srv.resolveSessionIdentity(r)
	require.True(t, ok)
	require.Equal(t, "bootid12345678901234", id)
}

func TestResolveSessionIdentity_VerifiedCookieWinsOverBootFallback(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer := &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}
	authn, err := buildTestVerifier(&priv.PublicKey, "test")
	require.NoError(t, err)
	srv := &server{logger: slog.Default(), authn: authn, bootIdentityID: "bootid12345678901234"}

	loggedIn := testNanoID(t)
	token, _, err := signer.mint(loggedIn)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	id, ok := srv.resolveSessionIdentity(r)
	require.True(t, ok)
	require.Equal(t, loggedIn, id, "a verified session cookie must win over the boot-env fallback")
}

func TestHandleDevLogin_DisabledWithoutDevSigner(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, devLoginPath, strings.NewReader(`{"identityId":"x"}`))
	srv.handleDevLogin(w, r)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandleDevLogin_MethodNotAllowed(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	srv := &server{logger: slog.Default(), devSigner: &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, devLoginPath, nil)
	srv.handleDevLogin(w, r)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandleDevLogin_RequiresIdentityID(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	srv := &server{logger: slog.Default(), devSigner: &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, devLoginPath, strings.NewReader(`{}`))
	srv.handleDevLogin(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDevLogin_SetsSessionCookieAndVerifies(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer := &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}
	authn, err := buildTestVerifier(&priv.PublicKey, "test")
	require.NoError(t, err)
	srv := &server{logger: slog.Default(), devSigner: signer, authn: authn, loopback: true}

	target := testNanoID(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, devLoginPath, strings.NewReader(`{"identityId":"`+target+`"}`))
	srv.handleDevLogin(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	res := w.Result()
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	require.NotNil(t, cookie, "handleDevLogin must set the session cookie")
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)

	// The cookie the browser would send back on the next request must
	// resolve to the same identity through resolveSessionIdentity.
	r2 := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r2.AddCookie(cookie)
	id, ok := srv.resolveSessionIdentity(r2)
	require.True(t, ok)
	require.Equal(t, target, id)
}

func TestHandleDevLogin_AcceptsVtxIdentityPrefix(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer := &devSigner{priv: priv, kid: "test", ttl: devTokenTTL, now: time.Now}
	srv := &server{logger: slog.Default(), devSigner: signer, loopback: true}
	target := testNanoID(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, devLoginPath, strings.NewReader(`{"identityId":"vtx.identity.`+target+`"}`))
	srv.handleDevLogin(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"`+target+`"`)
}

func TestHandleWhoami_NoSession(t *testing.T) {
	srv := &server{logger: slog.Default()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, whoamiPath, nil)
	srv.handleWhoami(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"loggedIn":false}`, w.Body.String())
}

func TestHandleWhoami_BootFallback(t *testing.T) {
	srv := &server{logger: slog.Default(), bootIdentityID: "bootid12345678901234"}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, whoamiPath, nil)
	srv.handleWhoami(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"bootid12345678901234"`)
	require.Contains(t, w.Body.String(), `"loggedIn":true`)
}

func TestHandleLogout_ClearsCookie(t *testing.T) {
	srv := &server{logger: slog.Default(), loopback: true}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, logoutPath, nil)
	srv.handleLogout(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	res := w.Result()
	require.Len(t, res.Cookies(), 1)
	require.Equal(t, sessionCookieName, res.Cookies()[0].Name)
	require.Equal(t, -1, res.Cookies()[0].MaxAge)
}

func TestRequireSession_ExemptPathPassesThrough(t *testing.T) {
	srv := &server{logger: slog.Default()}
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, loginPagePath, nil)
	srv.requireSession(inner).ServeHTTP(w, r)
	require.True(t, called)
}

func TestRequireSession_NoIdentityAPICallGets401(t *testing.T) {
	srv := &server{logger: slog.Default()}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach handler") })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/enqueue", nil)
	srv.requireSession(inner).ServeHTTP(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireSession_NoIdentityBrowserNavRedirectsToLogin(t *testing.T) {
	srv := &server{logger: slog.Default()}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not reach handler") })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	srv.requireSession(inner).ServeHTTP(w, r)
	require.Equal(t, http.StatusFound, w.Code)
	require.Equal(t, loginPagePath, w.Header().Get("Location"))
}

func TestRequireSession_ResolvedIdentityReachesHandlerInContext(t *testing.T) {
	srv := &server{logger: slog.Default(), bootIdentityID: "bootid12345678901234"}
	var gotID string
	var gotOK bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, gotOK = sessionIdentity(r.Context())
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/feed", nil)
	srv.requireSession(inner).ServeHTTP(w, r)
	require.True(t, gotOK)
	require.Equal(t, "bootid12345678901234", gotID)
}

func TestSetupSessionAuthn_NilSignerYieldsNilAuthenticator(t *testing.T) {
	authn, err := setupSessionAuthn(slog.Default(), nil)
	require.NoError(t, err)
	require.Nil(t, authn)
}
