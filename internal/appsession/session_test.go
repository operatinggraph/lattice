package appsession

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const testAppName = "testapp"
const testCookieName = testAppName + "_session"

// buildTestVerifier builds an auth.Authenticator trusting pub under kid,
// bound ModeNanoID — the same binding mode the real dev key uses
// (NewAuthenticators), so a token minted for a valid NanoID subject verifies
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

// buildTestVerifierWithSkew mirrors buildTestVerifier with a caller-chosen
// ClockSkew — handleRefresh's grace-window tests need a WIDER skew than the
// ordinary strict verifier to prove the refresh endpoint tolerates what
// RequireSession's own check would reject.
func buildTestVerifierWithSkew(pub *rsa.PublicKey, kid string, skew time.Duration) (*auth.Authenticator, error) {
	v, err := auth.NewVerifier(auth.Config{
		Keys:      map[string]crypto.PublicKey{kid: pub},
		KeyInfo:   map[string]auth.KeyInfo{kid: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
		ClockSkew: skew,
	})
	if err != nil {
		return nil, err
	}
	return auth.NewAuthenticator(v, nil), nil
}

// testSigner builds a Signer with a throwaway key — never the checked-in
// shared dev key.
func testSigner(t *testing.T) *Signer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return NewSigner(priv, "test", DevTokenTTL, time.Now)
}

func testNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// newTestManager builds a Manager over cfg's overrides, filling in the
// required fields every test needs.
func newTestManager(t *testing.T, mutate func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		AppName:   testAppName,
		Logger:    slog.Default(),
		LoginPage: []byte("<html>login</html>"),
		Loopback:  true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	if cfg.Signer != nil && cfg.GatewayURL == "" {
		// A Gateway that reports no distinct binding, so login resolution is
		// a no-op unless a test wires its own stub.
		cfg.GatewayURL = newActorStub(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"actorId": "vtx.identity.x", "resolvedActorId": "vtx.identity.x"})
		})
	}
	m, err := New(cfg)
	require.NoError(t, err)
	return m
}

// newActorStub serves a stand-in Gateway /v1/actor and returns its base URL.
func newActorStub(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/actor", r.URL.Path)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestNew_RequiresAppNameAndLoginPage(t *testing.T) {
	_, err := New(Config{LoginPage: []byte("x")})
	require.Error(t, err)
	_, err = New(Config{AppName: testAppName})
	require.Error(t, err)
	// A configured minter with no Gateway to resolve against is a
	// misconfiguration, not a degraded mode: login would fail-open on every
	// sign-in and silently never resolve a linked credential.
	_, err = New(Config{AppName: testAppName, LoginPage: []byte("x"), Signer: testSigner(t)})
	require.Error(t, err)

	m, err := New(Config{AppName: testAppName, LoginPage: []byte("x")})
	require.NoError(t, err)
	require.Equal(t, testCookieName, m.CookieName())
}

func TestIsAuthExempt(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.ExtraExemptPaths = []string{"/api/claim"} })
	for _, p := range []string{LoginPagePath, DevLoginPath, LogoutPath, WhoamiPath, LoginOptionsPath, RefreshPath, "/api/claim"} {
		require.True(t, m.IsAuthExempt(p), "path=%s", p)
	}
	for _, p := range []string{"/", "/api/feed", "/api/enqueue", "/app.js"} {
		require.False(t, m.IsAuthExempt(p), "path=%s", p)
	}
}

func TestResolve_NoSignerNoFallbackFallsClosed(t *testing.T) {
	m := newTestManager(t, nil)
	r := httptest.NewRequest(http.MethodGet, WhoamiPath, nil)
	_, ok := m.resolve(r)
	require.False(t, ok)
}

func TestResolve_FallbackWhenNoCookie(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.FallbackIdentityID = "bootid12345678901234" })
	r := httptest.NewRequest(http.MethodGet, WhoamiPath, nil)
	si, ok := m.resolve(r)
	require.True(t, ok)
	require.Equal(t, "bootid12345678901234", si.identityID)
	require.False(t, si.viaCookie, "the boot fallback is not a proven cookie session")
}

func TestResolve_VerifiedCookieWinsOverFallback(t *testing.T) {
	signer := testSigner(t)
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) {
		c.Authn = authn
		c.FallbackIdentityID = "bootid12345678901234"
	})

	loggedIn := testNanoID(t)
	token, _, err := signer.Mint(loggedIn)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, WhoamiPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: token})
	si, ok := m.resolve(r)
	require.True(t, ok)
	require.Equal(t, loggedIn, si.identityID, "a verified session cookie must win over the boot-env fallback")
	require.True(t, si.viaCookie)
}

// TestResolve_PresentButInvalidCookieFailsClosed pins the asymmetry the whole
// posture rests on: an ABSENT cookie may fall back to the boot identity, a
// PRESENT-but-unverifiable one may not — otherwise an expired session
// silently becomes someone else while the UI still claims the signed-in user.
func TestResolve_PresentButInvalidCookieFailsClosed(t *testing.T) {
	signer := testSigner(t)
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) {
		c.Authn = authn
		c.FallbackIdentityID = "bootid12345678901234"
	})

	r := httptest.NewRequest(http.MethodGet, WhoamiPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: "not-a-jwt"})
	_, ok := m.resolve(r)
	require.False(t, ok, "a present-but-invalid cookie must never fall through to the boot identity")
}

func TestHandleDevLogin_DisabledWithoutSigner(t *testing.T) {
	m := newTestManager(t, nil)
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"x"}`)))
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleDevLogin_DisabledMessageNamesTheEnvVar pins the exact operator-
// facing string: web/login.html renders body.error verbatim, so losing the
// env var name here strips the operator's only hint of what to set.
func TestHandleDevLogin_DisabledMessageNamesTheEnvVar(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.EnvPrefix = "FACET" })
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"x"}`)))
	require.Equal(t, http.StatusNotFound, w.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Equal(t, "login is disabled (FACET_DEV_AUTH not set)", body["error"])
}

func TestHandleDevLogin_MethodNotAllowed(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.Signer = testSigner(t) })
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodGet, DevLoginPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandleDevLogin_RequiresIdentityID(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.Signer = testSigner(t) })
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDevLogin_RejectsNonNanoIDSubject(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.Signer = testSigner(t) })
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"../../etc/passwd"}`)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleDevLogin_SetsSessionCookieAndVerifies(t *testing.T) {
	signer := testSigner(t)
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) {
		c.Signer = signer
		c.Authn = authn
	})

	target := testNanoID(t)
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+target+`"}`)))
	require.Equal(t, http.StatusOK, w.Code)

	cookie := cookieNamed(w.Result(), testCookieName)
	require.NotNil(t, cookie, "handleDevLogin must set the session cookie")
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)

	// The cookie the browser would send back on the next request must resolve
	// to the same identity.
	r2 := httptest.NewRequest(http.MethodGet, WhoamiPath, nil)
	r2.AddCookie(cookie)
	si, ok := m.resolve(r2)
	require.True(t, ok)
	require.Equal(t, target, si.identityID)
}

func TestHandleDevLogin_AcceptsVtxIdentityPrefix(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.Signer = testSigner(t) })
	target := testNanoID(t)
	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"vtx.identity.`+target+`"}`)))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"`+target+`"`)
}

func TestHandleDevLogin_PersonaFence(t *testing.T) {
	allowed, outsider := testNanoID(t), testNanoID(t)
	m := newTestManager(t, func(c *Config) {
		c.Signer = testSigner(t)
		c.Personas = []Persona{{ID: allowed, Label: "Riley"}}
	})

	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+outsider+`"}`)))
	require.Equal(t, http.StatusForbidden, w.Code, "a valid NanoID outside the persona list must be refused")

	w = httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+allowed+`"}`)))
	require.Equal(t, http.StatusOK, w.Code, "a listed persona must sign in")
	require.NotNil(t, cookieNamed(w.Result(), testCookieName), "persona sign-in must still set the session cookie")
}

// TestHandleDevLogin_ResolvesBoundCredential covers the login-time
// credential→identity beat: signing in with a linked sign-in method must open
// the identity it is BOUND to, not the bare credential's own empty world.
func TestHandleDevLogin_ResolvesBoundCredential(t *testing.T) {
	credential, bound := testNanoID(t), testNanoID(t)
	signer := testSigner(t)
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) {
		c.Signer = signer
		c.Authn = authn
		c.GatewayURL = newActorStub(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"actorId":         "vtx.identity." + credential,
				"resolvedActorId": "vtx.identity." + bound,
			})
		})
	})

	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+credential+`"}`)))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"`+bound+`"`)

	// The cookie must carry the RESOLVED identity's own token, so downstream
	// calls authenticate as that identity with no further round trip.
	cookie := cookieNamed(w.Result(), testCookieName)
	require.NotNil(t, cookie)
	actor, err := authn.Authenticate(context.Background(), cookie.Value)
	require.NoError(t, err)
	require.Equal(t, bound, actor.Subject)
}

// TestHandleDevLogin_ResolvedIdentityIsFenced closes the side door: a linked
// credential whose bound identity sits outside the persona list must not sign
// in just because the credential itself was listed.
func TestHandleDevLogin_ResolvedIdentityIsFenced(t *testing.T) {
	credential, bound := testNanoID(t), testNanoID(t)
	m := newTestManager(t, func(c *Config) {
		c.Signer = testSigner(t)
		c.Personas = []Persona{{ID: credential, Label: "Riley"}}
		c.GatewayURL = newActorStub(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"actorId":         "vtx.identity." + credential,
				"resolvedActorId": "vtx.identity." + bound,
			})
		})
	})

	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+credential+`"}`)))
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Nil(t, cookieNamed(w.Result(), testCookieName))
}

// TestHandleDevLogin_ResolveFailureFailsOpenToTheCredential pins the
// deny-safe fallback: an unreachable or erroring Gateway signs the caller in
// as the raw credential (which grants nothing extra) rather than refusing.
func TestHandleDevLogin_ResolveFailureFailsOpenToTheCredential(t *testing.T) {
	credential := testNanoID(t)
	m := newTestManager(t, func(c *Config) {
		c.Signer = testSigner(t)
		c.GatewayURL = newActorStub(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	})

	w := httptest.NewRecorder()
	m.handleDevLogin(w, httptest.NewRequest(http.MethodPost, DevLoginPath, strings.NewReader(`{"identityId":"`+credential+`"}`)))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"`+credential+`"`)
	require.NotNil(t, cookieNamed(w.Result(), testCookieName))
}

func TestHandleWhoami_NoSession(t *testing.T) {
	m := newTestManager(t, nil)
	w := httptest.NewRecorder()
	m.handleWhoami(w, httptest.NewRequest(http.MethodGet, WhoamiPath, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"loggedIn":false}`, w.Body.String())
}

func TestHandleWhoami_Fallback(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.FallbackIdentityID = "bootid12345678901234" })
	w := httptest.NewRecorder()
	m.handleWhoami(w, httptest.NewRequest(http.MethodGet, WhoamiPath, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"loggedIn":true,"identityId":"bootid12345678901234","canSignOut":false}`, w.Body.String())
}

func TestHandleLogout_ClearsCookie(t *testing.T) {
	m := newTestManager(t, nil)
	w := httptest.NewRecorder()
	m.handleLogout(w, httptest.NewRequest(http.MethodPost, LogoutPath, nil))
	require.Equal(t, http.StatusOK, w.Code)
	res := w.Result()
	require.Len(t, res.Cookies(), 1)
	require.Equal(t, testCookieName, res.Cookies()[0].Name)
	require.Equal(t, -1, res.Cookies()[0].MaxAge)
}

// TestHandleLogout_SignOutHookRunsForTheCookieIdentityOnly pins both halves of
// the cleanup rule: a verified cookie's identity is torn down, the process's
// own boot identity never is.
func TestHandleLogout_SignOutHookRunsForTheCookieIdentityOnly(t *testing.T) {
	signer := testSigner(t)
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	boot := testNanoID(t)
	var signedOut []string
	m := newTestManager(t, func(c *Config) {
		c.Signer = signer
		c.Authn = authn
		c.FallbackIdentityID = boot
		c.OnSignOut = func(id string) error { signedOut = append(signedOut, id); return nil }
	})

	user := testNanoID(t)
	userToken, _, err := signer.Mint(user)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: userToken})
	m.handleLogout(httptest.NewRecorder(), r)
	require.Equal(t, []string{user}, signedOut)

	bootToken, _, err := signer.Mint(boot)
	require.NoError(t, err)
	r = httptest.NewRequest(http.MethodPost, LogoutPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: bootToken})
	m.handleLogout(httptest.NewRecorder(), r)
	require.Equal(t, []string{user}, signedOut, "the boot identity is the process's own, not this browser's to tear down")
}

func TestRequireSession_ExemptPathPassesThrough(t *testing.T) {
	m := newTestManager(t, nil)
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	m.RequireSession(inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, LoginPagePath, nil))
	require.True(t, called)
}

func TestRequireSession_NoIdentityAPICallGets401(t *testing.T) {
	m := newTestManager(t, nil)
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") })
	w := httptest.NewRecorder()
	m.RequireSession(inner).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/enqueue", nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireSession_NoIdentityBrowserNavRedirectsToLogin(t *testing.T) {
	m := newTestManager(t, nil)
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not reach handler") })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	m.RequireSession(inner).ServeHTTP(w, r)
	require.Equal(t, http.StatusFound, w.Code)
	require.Equal(t, LoginPagePath, w.Header().Get("Location"))
}

func TestRequireSession_ResolvedIdentityReachesHandlerInContext(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.FallbackIdentityID = "bootid12345678901234" })
	var gotID string
	var gotOK, gotViaCookie bool
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotID, gotOK = Identity(r.Context())
		gotViaCookie = ViaCookie(r.Context())
	})
	m.RequireSession(inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/feed", nil))
	require.True(t, gotOK)
	require.Equal(t, "bootid12345678901234", gotID)
	require.False(t, gotViaCookie)
}

func TestHandleLoginPage(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.LoginPage = []byte("<html>sign in</html>") })
	w := httptest.NewRecorder()
	m.handleLoginPage(w, httptest.NewRequest(http.MethodGet, LoginPagePath, nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "<html>sign in</html>", w.Body.String())
}

func TestHandleLoginOptions(t *testing.T) {
	t.Run("no personas yields an empty list", func(t *testing.T) {
		m := newTestManager(t, nil)
		w := httptest.NewRecorder()
		m.handleLoginOptions(w, httptest.NewRequest(http.MethodGet, LoginOptionsPath, nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.JSONEq(t, `{"personas":[]}`, w.Body.String())
		require.False(t, m.HasPersonaFence())
	})
	t.Run("personas are returned verbatim", func(t *testing.T) {
		id := testNanoID(t)
		m := newTestManager(t, func(c *Config) { c.Personas = []Persona{{ID: id, Label: "Riley", Sub: "Unit 1"}} })
		w := httptest.NewRecorder()
		m.handleLoginOptions(w, httptest.NewRequest(http.MethodGet, LoginOptionsPath, nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.JSONEq(t, `{"personas":[{"id":"`+id+`","label":"Riley","sub":"Unit 1"}]}`, w.Body.String())
		require.True(t, m.HasPersonaFence())
	})
}

func TestNewAuthenticators_NilSignerYieldsNilAuthenticators(t *testing.T) {
	authn, refreshAuthn, err := NewAuthenticators(slog.Default(), "TESTAPP", nil)
	require.NoError(t, err)
	require.Nil(t, authn)
	require.Nil(t, refreshAuthn)
}

func TestParsePersonas(t *testing.T) {
	id1, id2 := testNanoID(t), testNanoID(t)
	const envVar = "TESTAPP_DEMO_PERSONAS"

	t.Run("unset means no posture", func(t *testing.T) {
		personas, err := ParsePersonas(envVar, "")
		require.NoError(t, err)
		require.Nil(t, personas)
	})
	t.Run("valid list, vtx prefix stripped", func(t *testing.T) {
		personas, err := ParsePersonas(envVar,
			`[{"id":"vtx.identity.`+id1+`","label":"Riley","sub":"Unit 1"},{"id":"`+id2+`","label":"Sam"}]`)
		require.NoError(t, err)
		require.Len(t, personas, 2)
		require.Equal(t, id1, personas[0].ID)
		require.Equal(t, id2, personas[1].ID)
	})
	t.Run("set but empty is an error", func(t *testing.T) {
		_, err := ParsePersonas(envVar, `[]`)
		require.Error(t, err)
	})
	t.Run("non-NanoID id is an error", func(t *testing.T) {
		_, err := ParsePersonas(envVar, `[{"id":"../../etc/passwd","label":"x"}]`)
		require.Error(t, err)
	})
	t.Run("missing label is an error", func(t *testing.T) {
		_, err := ParsePersonas(envVar, `[{"id":"`+id1+`"}]`)
		require.Error(t, err)
	})
}

// refreshTestManager builds a Manager whose Authn/RefreshAuthn pair mirrors
// NewAuthenticators' real split (same key, strict vs. grace skew) — production
// wires both from one signer; these tests do the same so a grace-window
// assertion compares the two verifiers NewAuthenticators would actually build,
// not a hand-tuned stand-in.
func refreshTestManager(t *testing.T, signer *Signer, mutate func(*Config)) *Manager {
	t.Helper()
	authn, err := buildTestVerifier(&signer.priv.PublicKey, signer.kid)
	require.NoError(t, err)
	refreshAuthn, err := buildTestVerifierWithSkew(&signer.priv.PublicKey, signer.kid, RefreshGrace)
	require.NoError(t, err)
	return newTestManager(t, func(c *Config) {
		c.Signer = signer
		c.Authn = authn
		c.RefreshAuthn = refreshAuthn
		if mutate != nil {
			mutate(c)
		}
	})
}

// mintAt mints identityID's token as if the signer's clock read `when` —
// gives a test precise control over a token's iat/exp instead of racing
// time.Now()'s one-second JWT resolution. RS256 is a deterministic signature
// scheme, so two mints of the SAME subject within the same wall-clock second
// produce a byte-identical token — a real, harmless property, not a bug, but
// one a test proving "this call minted something new" must route around.
func mintAt(t *testing.T, signer *Signer, identityID string, when time.Time) string {
	t.Helper()
	realNow := signer.now
	signer.now = func() time.Time { return when }
	defer func() { signer.now = realNow }()
	token, _, err := signer.Mint(identityID)
	require.NoError(t, err)
	return token
}

// mintExpiredBy mints identityID's token as if it were minted expiredBy ago
// relative to now — i.e. the returned token's exp already lies expiredBy
// behind wall-clock time.
func mintExpiredBy(t *testing.T, signer *Signer, identityID string, expiredBy time.Duration) string {
	t.Helper()
	return mintAt(t, signer, identityID, time.Now().Add(-signer.ttl).Add(-expiredBy))
}

func TestHandleRefresh_DisabledWithoutSigner(t *testing.T) {
	m := newTestManager(t, nil)
	w := httptest.NewRecorder()
	m.handleRefresh(w, httptest.NewRequest(http.MethodPost, RefreshPath, nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleRefresh_DisabledMessageNamesTheEnvVar mirrors the dev-login
// pinned message: the same web/login.html renders this error verbatim too.
func TestHandleRefresh_DisabledMessageNamesTheEnvVar(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.EnvPrefix = "FACET" })
	w := httptest.NewRecorder()
	m.handleRefresh(w, httptest.NewRequest(http.MethodPost, RefreshPath, nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Equal(t, "refresh is disabled (FACET_DEV_AUTH not set)", body["error"])
}

// TestHandleRefresh_RefreshAuthnWithoutSignerDoesNotPanic proves the gate
// checks BOTH fields. An adopter can wire RefreshAuthn (verify-only, the
// kit's documented production posture) while leaving Signer nil — refresh
// must still 404, not nil-deref calling Mint on a nil *Signer.
func TestHandleRefresh_RefreshAuthnWithoutSignerDoesNotPanic(t *testing.T) {
	signer := testSigner(t)
	refreshAuthn, err := buildTestVerifierWithSkew(&signer.priv.PublicKey, signer.kid, RefreshGrace)
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) {
		c.RefreshAuthn = refreshAuthn
		c.EnvPrefix = "FACET"
	})
	tok, _, err := signer.Mint(testNanoID(t))
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: tok})
	require.NotPanics(t, func() { m.handleRefresh(w, r) })
	require.Equal(t, http.StatusNotFound, w.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Equal(t, "refresh is disabled (FACET_DEV_AUTH not set)", body["error"])
}

func TestHandleRefresh_MethodNotAllowed(t *testing.T) {
	m := refreshTestManager(t, testSigner(t), nil)
	w := httptest.NewRecorder()
	m.handleRefresh(w, httptest.NewRequest(http.MethodGet, RefreshPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandleRefresh_NoCookieUnauthorized(t *testing.T) {
	m := refreshTestManager(t, testSigner(t), nil)
	w := httptest.NewRecorder()
	m.handleRefresh(w, httptest.NewRequest(http.MethodPost, RefreshPath, nil))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleRefresh_InvalidCookieRejected(t *testing.T) {
	m := refreshTestManager(t, testSigner(t), nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: "not-a-jwt"})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestHandleRefresh_ValidCookieRotatesTokenAndCookie is the ordinary case: a
// fresh, unexpired session refreshes cleanly — a new token, a new Set-Cookie,
// both for the SAME identity, and the fresh cookie verifies against the
// ordinary strict authn exactly like a login-minted one would.
func TestHandleRefresh_ValidCookieRotatesTokenAndCookie(t *testing.T) {
	signer := testSigner(t)
	m := refreshTestManager(t, signer, nil)
	identity := testNanoID(t)
	// Backdated a few seconds so its iat provably differs from the refresh's
	// own mint below — RS256 is deterministic, so two mints within the same
	// wall-clock second are byte-identical (see mintAt's doc).
	oldToken := mintAt(t, signer, identity, time.Now().Add(-5*time.Second))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: oldToken})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var body refreshResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.NotEmpty(t, body.Token)
	require.NotEqual(t, oldToken, body.Token, "a refresh must mint a NEW token, not echo the old one")
	require.NotEmpty(t, body.ExpiresAt)

	cookie := cookieNamed(w.Result(), testCookieName)
	require.NotNil(t, cookie, "handleRefresh must re-set the session cookie")
	require.Equal(t, body.Token, cookie.Value, "the response token and the cookie must be the SAME fresh credential")

	// The fresh token verifies against the ordinary strict authenticator, for
	// the SAME identity — a page reload right after a refresh must not bounce
	// to the login page.
	actor, err := m.cfg.Authn.Authenticate(context.Background(), body.Token)
	require.NoError(t, err)
	require.Equal(t, identity, actor.Subject)
}

// TestHandleRefresh_GraceWindowAcceptsRecentlyExpiredToken proves the
// sliding-session tolerance: a cookie that strict verification would already
// reject (past auth.DefaultClockSkew) still refreshes when it's within
// RefreshGrace — the tab-backgrounded/laptop-asleep case the endpoint exists
// for.
func TestHandleRefresh_GraceWindowAcceptsRecentlyExpiredToken(t *testing.T) {
	signer := testSigner(t)
	m := refreshTestManager(t, signer, nil)
	identity := testNanoID(t)
	expired := mintExpiredBy(t, signer, identity, 2*time.Minute) // < RefreshGrace (5m), > strict skew (60s)

	// The strict authenticator (every other session-gated request) already
	// refuses this token — proving the grace test below is actually testing a
	// WIDER tolerance, not one that would pass anyway.
	_, err := m.cfg.Authn.Authenticate(context.Background(), expired)
	require.Error(t, err, "a 2-minute-expired token must already fail strict verification")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: expired})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusOK, w.Code, "a token expired within the grace window must still refresh")

	var body refreshResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	actor, err := m.cfg.Authn.Authenticate(context.Background(), body.Token)
	require.NoError(t, err)
	require.Equal(t, identity, actor.Subject)
}

// TestHandleRefresh_BeyondGraceWindowRejected proves the grace window is
// bounded, not unlimited: a session dead long enough has no refresh path and
// must fall back to the login page.
func TestHandleRefresh_BeyondGraceWindowRejected(t *testing.T) {
	signer := testSigner(t)
	m := refreshTestManager(t, signer, nil)
	expired := mintExpiredBy(t, signer, testNanoID(t), RefreshGrace+time.Minute)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: expired})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestHandleRefresh_PersonaFence mirrors TestHandleDevLogin_PersonaFence for
// the refresh path: a session for an identity the CURRENT persona list no
// longer names must not silently keep renewing.
func TestHandleRefresh_PersonaFence(t *testing.T) {
	signer := testSigner(t)
	allowed, outsider := testNanoID(t), testNanoID(t)
	m := refreshTestManager(t, signer, func(c *Config) {
		c.Personas = []Persona{{ID: allowed, Label: "Riley"}}
	})

	outsiderToken, _, err := signer.Mint(outsider)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: outsiderToken})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusForbidden, w.Code)

	allowedToken, _, err := signer.Mint(allowed)
	require.NoError(t, err)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: testCookieName, Value: allowedToken})
	m.handleRefresh(w, r)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestRegisterRoutes_BindsTheSessionSurface(t *testing.T) {
	m := newTestManager(t, nil)
	mux := http.NewServeMux()
	m.RegisterRoutes(mux)
	for _, p := range []string{LoginPagePath, LoginOptionsPath, DevLoginPath, LogoutPath, WhoamiPath, RefreshPath} {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, p, nil))
		require.Equal(t, p, pattern, "route %s must be bound", p)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		require.True(t, IsLoopbackHost(h), "host=%s", h)
	}
	for _, h := range []string{"", "0.0.0.0", "10.0.0.4", "example.com"} {
		require.False(t, IsLoopbackHost(h), "host=%s", h)
	}
	require.Equal(t, "127.0.0.1", HostOf("127.0.0.1:7810"))
	require.Equal(t, "", HostOf(":7810"), "a bare port binds all interfaces and must not read as loopback")
}

func TestTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		require.True(t, Truthy(v), "value=%q", v)
	}
	for _, v := range []string{"", "0", "false", "no", "off"} {
		require.False(t, Truthy(v), "value=%q", v)
	}
}
