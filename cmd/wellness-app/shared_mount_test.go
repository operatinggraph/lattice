package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// TestSharedFormModule_GatedAnonymouslyServesWithASession proves /shared/
// carries the same session gate as the rest of the SPA shell, and that a
// signed-in caller actually gets the real internal/descriptorform module
// back — the exact StripPrefix mount every one of the four staff apps uses,
// driven through the app's own registerRoutes wiring rather than a bare curl
// (RequireSession gates the whole inner mux, so an anonymous request 401s
// regardless of whether the mount itself is correct; only an authenticated
// request distinguishes a working mount from a 404 one). Builds its own
// session surface directly (no embedded NATS): static-asset serving never
// touches s.conn.
func TestSharedFormModule_GatedAnonymouslyServesWithASession(t *testing.T) {
	t.Setenv(envPrefix+"_DEV_AUTH", "1")
	signer, err := appsession.NewDevSigner(discardLogger(), envPrefix, true)
	if err != nil {
		t.Fatalf("NewDevSigner: %v", err)
	}
	authn, refreshAuthn, err := appsession.NewAuthenticators(discardLogger(), envPrefix, signer, nil)
	if err != nil {
		t.Fatalf("NewAuthenticators: %v", err)
	}
	session, err := appsession.New(appsession.Config{
		AppName:      appName,
		EnvPrefix:    envPrefix,
		Logger:       discardLogger(),
		GatewayURL:   "http://gateway.invalid",
		Signer:       signer,
		Authn:        authn,
		RefreshAuthn: refreshAuthn,
		Loopback:     true,
		LoginPage:    []byte("<html>login</html>"),
	})
	if err != nil {
		t.Fatalf("appsession.New: %v", err)
	}
	s := &server{logger: discardLogger(), authn: authn, session: session, natsTimeout: testTimeout}
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("GET /shared/form.mjs without a session = 200; the app shell must not serve to an anonymous caller")
	}

	r := httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil)
	tok, exp, err := signer.Mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: session.CookieName(), Value: tok, Expires: exp})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shared/form.mjs with a session = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "export function renderOpForm") {
		t.Fatalf("did not serve the real module; body:\n%s", rec.Body.String())
	}
}
