package appsession

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePublicOrigin_Accepts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"https://app.demo.example", "https://app.demo.example"},
		{"https://app.demo.example/", "https://app.demo.example"},
		{"https://app.demo.example:8443", "https://app.demo.example:8443"},
		{"https://app.demo.example:443", "https://app.demo.example"},
		{"HTTPS://App.Demo.Example", "https://app.demo.example"},
		{"https://app.demo.example.", "https://app.demo.example"},
		{"  https://app.demo.example  ", "https://app.demo.example"},
		{"https://[::1]:8443", "https://[::1]:8443"},
		{"https://app.demo.example:1", "https://app.demo.example:1"},
		{"https://app.demo.example:65535", "https://app.demo.example:65535"},
		// A colon with no digits is the empty port, which defaults like an
		// omitted one rather than failing.
		{"https://app.demo.example:", "https://app.demo.example"},
	} {
		got, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", tc.raw)
		require.NoError(t, err, "raw=%s", tc.raw)
		require.NotNil(t, got, "raw=%s", tc.raw)
		require.Equal(t, tc.want, got.String(), "raw=%s", tc.raw)
	}
}

func TestParsePublicOrigin_EmptyIsTheUndeclaredPosture(t *testing.T) {
	got, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "   ")
	require.NoError(t, err)
	require.Nil(t, got)
	require.Equal(t, "", got.String())
}

// TestParsePublicOrigin_RefusesToBoot: every malformed declaration must stop the
// process rather than read as undeclared — a typo that silently dropped it would
// take the cross-origin gate and the Secure cookie down together. Each case
// asserts the REASON, so a value rejected by the wrong rule cannot pass for
// having merely errored.
func TestParsePublicOrigin_RefusesToBoot(t *testing.T) {
	for _, tc := range []struct{ raw, because string }{
		{"http://app.demo.example", "must use the https scheme"},
		{"app.demo.example", "must use the https scheme"}, // no scheme at all reads as a path
		{"mailto:ops@demo.example", "is not an absolute origin"},
		{"https://", "has no host"},
		{"https://app.demo.example..", "has an empty label"},
		{"https://.app.demo.example", "has an empty label"},
		{"https://u:p@app.demo.ex", "must not carry userinfo"},
		{"https://app.demo.ex/admin", "must not carry a path"},
		{"https://app.demo.ex?x=1", "must not carry a query"},
		{"https://app.demo.ex?", "must not carry a query"}, // ForceQuery: a bare trailing ?
		{"https://app.demo.ex#frag", "must not carry a fragment"},
		{"https://app.demo.ex:0", "out-of-range port"},
		{"https://app.demo.ex:65536", "out-of-range port"},
		{"https://app.demo.ex:99999999999999999999", "out-of-range port"}, // all digits, overflows Atoi
		{"https://app.demo.example:abc", "is not a URL"},                  // net/url rejects the port itself
		{"https://::1:8443", "is not a URL"},                              // unbracketed IPv6
	} {
		got, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", tc.raw)
		require.Error(t, err, "raw=%s", tc.raw)
		require.Nil(t, got, "raw=%s", tc.raw)
		require.Contains(t, err.Error(), "APP_PUBLIC_ORIGIN", "the error must name the env var to fix")
		require.Contains(t, err.Error(), tc.because, "raw=%s was rejected for the wrong reason", tc.raw)
	}
}

func TestPublicOrigin_Matches(t *testing.T) {
	p, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://app.demo.example")
	require.NoError(t, err)

	for _, ok := range []string{
		"https://app.demo.example",
		"https://app.demo.example:443",
		"https://APP.DEMO.EXAMPLE",
		"https://app.demo.example.",
	} {
		require.True(t, p.matches(ok), "origin=%s", ok)
	}
	for _, bad := range []string{
		"",
		"null",
		"http://app.demo.example",       // scheme
		"https://app.demo.example:8443", // port
		"https://evil.example",
		"https://ſmart.example", // Unicode case folding must not widen the host
	} {
		require.False(t, p.matches(bad), "origin=%s", bad)
	}

	var undeclared *PublicOrigin
	require.False(t, undeclared.matches("https://app.demo.example"))
}

func TestPublicOrigin_MatchesIPv6AndUnicodeFolding(t *testing.T) {
	v6, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://[::1]:8443")
	require.NoError(t, err)
	require.True(t, v6.matches("https://[::1]:8443"))
	require.False(t, v6.matches("https://[::1]:8444"))
	require.False(t, v6.matches("https://[::2]:8443"))

	smart, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://smart.example")
	require.NoError(t, err)
	require.False(t, smart.matches("https://ſmart.example"))
}

// originRequest builds a request to host carrying whichever browser signals the
// case exercises ("" omits the header).
func originRequest(method, host, origin, fetchSite, fetchMode string) *http.Request {
	r := httptest.NewRequest(method, "/api/thing", strings.NewReader("{}"))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if fetchSite != "" {
		r.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	if fetchMode != "" {
		r.Header.Set("Sec-Fetch-Mode", fetchMode)
		// `navigate` alone does not mean top-level — it is sent for a nested
		// navigation too. Every caller passing it here means the ordinary
		// link/bookmark case, so the destination that makes it top-level rides
		// along; the nested shapes get their own test.
		if fetchMode == "navigate" {
			r.Header.Set("Sec-Fetch-Dest", "document")
		}
	}
	return r
}

// TestCrossOriginBlocked_CoHostedPortIsNotSameOrigin is the vector the gate
// exists for: cookies are host-scoped, not port-scoped, so a page served by a
// sibling app on another localhost port is same-SITE — SameSite=Strict lets its
// POST carry this app's session cookie.
func TestCrossOriginBlocked_CoHostedPortIsNotSameOrigin(t *testing.T) {
	m := newTestManager(t, nil)

	rec := httptest.NewRecorder()
	r := originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7799", "same-site", "cors")
	require.True(t, m.CrossOriginBlocked(rec, r))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// The same request from a browser too old to send Sec-Fetch-Site: the Origin
	// branch must catch it on the port alone, both hosts being loopback.
	rec = httptest.NewRecorder()
	r = originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7799", "", "")
	require.True(t, m.CrossOriginBlocked(rec, r))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "http://127.0.0.1:7799")
}

// TestCrossOriginBlocked_SubresourceGETIsBlocked pins the side-effect axis: a
// GET is not automatically safe (Facet's GET /api/feed mints the session
// identity's credential), and a bare <img src> to a sibling app is a same-site
// subresource that carries the cookie and sends NO Origin — so Fetch Metadata is
// the only thing that can tell it from a link the user clicked.
func TestCrossOriginBlocked_SubresourceGETIsBlocked(t *testing.T) {
	m := newTestManager(t, nil)
	for _, mode := range []string{"no-cors", "cors", "same-origin"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodGet, "127.0.0.1:7788", "", "same-site", mode)
		require.True(t, m.CrossOriginBlocked(rec, r), "Sec-Fetch-Mode=%s", mode)
		require.Equal(t, http.StatusForbidden, rec.Code)
	}
}

// TestCrossOriginBlocked_TopLevelNavigationPasses is the other half: an ordinary
// link, bookmark or redirect from one co-hosted app to another must keep
// working, and a navigation is the user's own act.
func TestCrossOriginBlocked_TopLevelNavigationPasses(t *testing.T) {
	m := newTestManager(t, nil)
	for _, site := range []string{"same-site", "cross-site"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodGet, "127.0.0.1:7788", "", site, "navigate")
		require.False(t, m.CrossOriginBlocked(rec, r), "Sec-Fetch-Site=%s", site)
	}

	// A navigating POST is a cross-site form submit, not a link — still refused.
	rec := httptest.NewRecorder()
	r := originRequest(http.MethodPost, "127.0.0.1:7788", "", "cross-site", "navigate")
	require.True(t, m.CrossOriginBlocked(rec, r))
}

func TestCrossOriginBlocked_FetchMetadataPolicy(t *testing.T) {
	m := newTestManager(t, nil)

	// Admitted: this app's own page, and a request with no initiator at all
	// (typed URL, bookmark) — which no page can forge.
	for _, site := range []string{"same-origin", "Same-Origin", "none"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7788", site, "cors")
		require.False(t, m.CrossOriginBlocked(rec, r), "Sec-Fetch-Site=%s", site)
	}

	for _, site := range []string{"same-site", "cross-site", "SAME-SITE-ish"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7788", site, "cors")
		require.True(t, m.CrossOriginBlocked(rec, r), "Sec-Fetch-Site=%s", site)
		require.Contains(t, rec.Body.String(), site)
	}
}

// TestCrossOriginBlocked_BothSignalsMustHold: each half of the AND, exercised on
// its own, so neither can be dropped without a test going red.
func TestCrossOriginBlocked_BothSignalsMustHold(t *testing.T) {
	m := newTestManager(t, nil)

	// Metadata says this app's own page; the Origin says otherwise.
	rec := httptest.NewRecorder()
	r := originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7799", "same-origin", "cors")
	require.True(t, m.CrossOriginBlocked(rec, r))
	require.Contains(t, rec.Body.String(), "Origin")

	// The Origin is this app's own; the metadata says a sibling initiated it.
	rec = httptest.NewRecorder()
	r = originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7788", "same-site", "cors")
	require.True(t, m.CrossOriginBlocked(rec, r))
	require.Contains(t, rec.Body.String(), "Sec-Fetch-Site")
}

// TestCrossOriginBlocked_NonBrowserClientPasses: a request with neither signal
// is not browser-driven, so it carries no ambient cookie of anyone else's.
func TestCrossOriginBlocked_NonBrowserClientPasses(t *testing.T) {
	m := newTestManager(t, nil)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		r := originRequest(method, "127.0.0.1:7788", "", "", "")
		require.False(t, m.CrossOriginBlocked(rec, r), "method=%s", method)
	}
}

func TestCrossOriginBlocked_EveryStateChangingMethodIsGated(t *testing.T) {
	m := newTestManager(t, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "FROB"} {
		rec := httptest.NewRecorder()
		r := originRequest(method, "127.0.0.1:7788", "http://127.0.0.1:7799", "", "")
		require.True(t, m.CrossOriginBlocked(rec, r), "method=%s", method)
	}
}

// TestCrossOriginBlocked_OriginAgreeingWithHostIsNotEnough pins the
// DNS-rebinding hardening: under rebinding both headers carry the attacker's
// name and agree by construction, so agreement alone must not open the gate.
func TestCrossOriginBlocked_OriginAgreeingWithHostIsNotEnough(t *testing.T) {
	m := newTestManager(t, nil)
	rec := httptest.NewRecorder()
	r := originRequest(http.MethodPost, "rebind.evil.example", "http://rebind.evil.example", "", "")
	require.True(t, m.CrossOriginBlocked(rec, r))

	// Declaring that host as the bind is the operator's explicit opt-in, and the
	// rooted (trailing-dot) form of a host addresses the same app.
	m = newTestManager(t, func(c *Config) { c.BindHost = "rebind.evil.example" })
	for _, host := range []string{"rebind.evil.example", "rebind.evil.example."} {
		rec = httptest.NewRecorder()
		r = originRequest(http.MethodPost, host, "http://"+host, "", "")
		require.False(t, m.CrossOriginBlocked(rec, r), "host=%s", host)
	}
}

// TestCrossOriginBlocked_LoopbackAcceptsTheRootedForm: localhost. and localhost
// are the same loopback page, so the gate must not 403 the rooted spelling.
func TestCrossOriginBlocked_LoopbackAcceptsTheRootedForm(t *testing.T) {
	m := newTestManager(t, nil)
	for _, host := range []string{"localhost:7788", "localhost.:7788", "127.0.0.1:7788", "[::1]:7788"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, host, "http://"+host, "", "")
		require.False(t, m.CrossOriginBlocked(rec, r), "host=%s", host)
	}
}

// TestCrossOriginBlocked_BothSchemeFormsOfHostAccepted pins the deployment shape
// a connection-derived scheme would break: a TLS-terminating proxy in front of a
// non-loopback bind, preserving Host, with no PublicOrigin declared. The
// browser's Origin says https while the proxied hop arrives as plain http, so
// pinning the scheme to r.TLS would 403 every write — including login — with no
// boot-time signal.
func TestCrossOriginBlocked_BothSchemeFormsOfHostAccepted(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.BindHost = "app.internal" })
	for _, origin := range []string{"http://app.internal", "https://app.internal"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "app.internal", origin, "", "")
		require.False(t, m.CrossOriginBlocked(rec, r), "origin=%s", origin)
	}

	// The hardening the branch actually rests on is untouched: a host that is
	// neither loopback nor the configured bind host is refused in either form,
	// even though Origin and Host agree by construction (DNS rebinding).
	for _, origin := range []string{"http://evil.example", "https://evil.example"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "evil.example", origin, "", "")
		require.True(t, m.CrossOriginBlocked(rec, r), "origin=%s", origin)
	}
}

// TestCrossOriginBlocked_NestedNavigationRefused is the clickjacking vector that
// reading Sec-Fetch-Mode alone admits: `navigate` covers a nested navigation, so
// an <iframe src> on a co-hosted app's page is same-site, carries this app's
// session cookie (SameSite=Strict is port-agnostic), authenticates, and renders
// the signed-in app inside a hostile frame.
func TestCrossOriginBlocked_NestedNavigationRefused(t *testing.T) {
	m := newTestManager(t, nil)
	for _, dest := range []string{"iframe", "frame", "object", "embed"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodGet, "127.0.0.1:7788", "", "same-site", "navigate")
		r.Header.Set("Sec-Fetch-Dest", dest)
		require.True(t, m.CrossOriginBlocked(rec, r), "Sec-Fetch-Dest=%s", dest)
	}

	// A navigation that cannot prove it is top-level is refused too.
	rec := httptest.NewRecorder()
	r := originRequest(http.MethodGet, "127.0.0.1:7788", "", "same-site", "navigate")
	r.Header.Del("Sec-Fetch-Dest")
	require.True(t, m.CrossOriginBlocked(rec, r))
}

// TestCrossOriginBlocked_FetchMetadataHeaderShape pins that the gate keys on the
// header KEY's presence, not on a non-empty value: a GET carries no Origin, so
// an empty Sec-Fetch-Site falling through to the Origin check would pass
// unconditionally.
func TestCrossOriginBlocked_FetchMetadataHeaderShape(t *testing.T) {
	m := newTestManager(t, nil)

	// Present but empty.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/thing", nil)
	r.Host = "127.0.0.1:7788"
	r.Header["Sec-Fetch-Site"] = []string{""}
	require.True(t, m.CrossOriginBlocked(rec, r))

	// Repeated — Header.Get would silently take the admitting first value.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/thing", nil)
	r.Host = "127.0.0.1:7788"
	r.Header["Sec-Fetch-Site"] = []string{"same-origin", "cross-site"}
	require.True(t, m.CrossOriginBlocked(rec, r))

	// An unknown or future token fails closed.
	rec = httptest.NewRecorder()
	r = originRequest(http.MethodGet, "127.0.0.1:7788", "", "same-party", "cors")
	require.True(t, m.CrossOriginBlocked(rec, r))
}

// TestOriginGate_UsableWithoutAManager is the point of the extraction: an app
// with its own auth model (the Loupe console) gets the identical decision
// without constructing a session Manager it would never register.
func TestOriginGate_UsableWithoutAManager(t *testing.T) {
	declared, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://app.demo.example")
	require.NoError(t, err)
	gate := OriginGate{PublicOrigin: declared, BindHost: "127.0.0.1"}

	// Refused, with a reason the caller renders in its own error shape.
	reason, blocked := gate.Blocked(originRequest(http.MethodPost, "127.0.0.1:7788", "http://evil.example", "", ""))
	require.True(t, blocked)
	require.Contains(t, reason, "evil.example")

	// Admitted: the app's own page, and the declared public origin.
	for _, r := range []*http.Request{
		originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7788", "same-origin", "cors"),
		originRequest(http.MethodPost, "127.0.0.1:7788", "https://app.demo.example", "same-origin", "cors"),
	} {
		reason, blocked := gate.Blocked(r)
		require.False(t, blocked)
		require.Empty(t, reason)
	}

	// The Manager wrapper renders the same decision as the kit's 403.
	m := newTestManager(t, func(c *Config) { c.PublicOrigin = declared })
	rec := httptest.NewRecorder()
	require.True(t, m.CrossOriginBlocked(rec, originRequest(http.MethodPost, "127.0.0.1:7788", "http://evil.example", "", "")))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "cross-origin request blocked")
}

func TestCrossOriginBlocked_NullOriginFailsClosed(t *testing.T) {
	m := newTestManager(t, nil)
	for _, origin := range []string{"null", "http://"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "", origin, "", "")
		require.True(t, m.CrossOriginBlocked(rec, r), "origin=%s", origin)
	}
}

// TestCrossOriginBlocked_DeclaredPublicOriginPasses is the proxied deployment:
// the browser's Origin names the public site while the bind stays loopback, so
// without the declaration every state-changing request would 403.
func TestCrossOriginBlocked_DeclaredPublicOriginPasses(t *testing.T) {
	declared, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://app.demo.example")
	require.NoError(t, err)
	m := newTestManager(t, func(c *Config) { c.PublicOrigin = declared })

	// The shape the hosted demo runs: TLS terminated at the proxy, Host passed
	// through, the app itself still bound to loopback.
	for _, host := range []string{"app.demo.example", "127.0.0.1:7788"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, host, "https://app.demo.example", "same-origin", "cors")
		require.False(t, m.CrossOriginBlocked(rec, r), "host=%s", host)
	}

	// A different site reaching the same proxied app is still blocked, and the
	// declaration does not license a sibling port on the public host either.
	for _, origin := range []string{"https://evil.example", "https://app.demo.example:8443"} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "127.0.0.1:7788", origin, "", "")
		require.True(t, m.CrossOriginBlocked(rec, r), "origin=%s", origin)
	}
}

// TestNew_DeclaredOriginWithDevAuthNeedsAPersonaFence: a declared public origin
// means the app is proxied to the internet, and a Signer is the minter that
// signs whatever subject a caller names — together and unfenced they hand every
// identity in the graph to every visitor. The bind-address check that otherwise
// keeps the minter local cannot see this shape: the bind IS loopback.
func TestNew_DeclaredOriginWithDevAuthNeedsAPersonaFence(t *testing.T) {
	declared, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://app.demo.example")
	require.NoError(t, err)
	signer := testSigner(t)

	_, err = New(Config{
		AppName:      testAppName,
		EnvPrefix:    "APP",
		LoginPage:    []byte("<html>login</html>"),
		GatewayURL:   "http://gateway.invalid",
		Signer:       signer,
		PublicOrigin: declared,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "APP_DEMO_PERSONAS")

	// Fenced, it is the hosted-demo posture and boots.
	_, err = New(Config{
		AppName:      testAppName,
		EnvPrefix:    "APP",
		LoginPage:    []byte("<html>login</html>"),
		GatewayURL:   "http://gateway.invalid",
		Signer:       signer,
		PublicOrigin: declared,
		Personas:     []Persona{{ID: testNanoID(t), Label: "Demo"}},
	})
	require.NoError(t, err)

	// Verify-only production (no minter) needs no fence: nothing in the process
	// can issue a token for a subject a caller names.
	_, err = New(Config{
		AppName:      testAppName,
		EnvPrefix:    "APP",
		LoginPage:    []byte("<html>login</html>"),
		PublicOrigin: declared,
	})
	require.NoError(t, err)
}

// TestRequireSession_CrossOriginPostNeverReachesTheHandler proves the gate sits
// at the choke point every adopting app routes through, not at each handler.
func TestRequireSession_CrossOriginPostNeverReachesTheHandler(t *testing.T) {
	m := newTestManager(t, func(c *Config) { c.FallbackIdentityID = testNanoID(t) })
	reached := false
	h := m.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7799", "same-site", "cors"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, reached, "a cross-origin write must not reach the app's handler")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7788", "same-origin", "cors"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, reached)
}

// TestRequireSession_ExemptSessionEndpointsAreStillOriginGated: the session
// endpoints are exempt from NEEDING a session, not from proving their origin —
// a forced login or logout is a state change too.
func TestRequireSession_ExemptSessionEndpointsAreStillOriginGated(t *testing.T) {
	m := newTestManager(t, nil)
	reached := false
	h := m.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{DevLoginPath, LogoutPath, RefreshPath} {
		rec := httptest.NewRecorder()
		r := originRequest(http.MethodPost, "127.0.0.1:7788", "http://127.0.0.1:7799", "same-site", "cors")
		r.URL.Path = path
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusForbidden, rec.Code, "path=%s", path)
		require.False(t, reached, "path=%s", path)
	}

	// The login page is where a signed-out browser lands from anywhere, so a
	// cross-site navigation to it must still open.
	rec := httptest.NewRecorder()
	r := originRequest(http.MethodGet, "127.0.0.1:7788", "", "cross-site", "navigate")
	r.URL.Path = LoginPagePath
	h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, reached)
}

// TestCookieSecure_DeclaredOriginForcesSecure closes the twin fail-OPEN of the
// same proxy shape: a loopback bind behind TLS termination reads as "not
// public", which would ship the session cookie without Secure over HTTPS.
func TestCookieSecure_DeclaredOriginForcesSecure(t *testing.T) {
	declared, err := ParsePublicOrigin("APP_PUBLIC_ORIGIN", "https://app.demo.example")
	require.NoError(t, err)

	loopback := newTestManager(t, nil)
	require.False(t, loopback.cookieSecure())

	proxied := newTestManager(t, func(c *Config) { c.PublicOrigin = declared })
	require.True(t, proxied.cookieSecure())

	exposed := newTestManager(t, func(c *Config) { c.Loopback = false })
	require.True(t, exposed.cookieSecure())

	// Both cookie writers carry the derivation, so a logout on the proxied site
	// clears the same cookie the browser holds.
	rec := httptest.NewRecorder()
	proxied.setCookie(rec, "tok", time.Now().Add(time.Minute))
	proxied.clearCookie(rec)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 2)
	for _, c := range cookies {
		require.True(t, c.Secure, "name=%s", c.Name)
		require.True(t, c.HttpOnly)
		require.Equal(t, http.SameSiteStrictMode, c.SameSite)
	}
}
