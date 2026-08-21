package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSharedFormModule_GatedAnonymouslyServesWithASession proves /shared/
// carries the same session gate as the rest of the SPA shell, and that a
// signed-in caller actually gets the real internal/descriptorform module
// back — the exact StripPrefix mount every one of the four staff apps uses,
// driven through the app's own registerRoutes wiring rather than a bare curl
// (RequireSession gates the whole inner mux, so an anonymous request 401s
// regardless of whether the mount itself is correct; only an authenticated
// request distinguishes a working mount from a 404 one).
func TestSharedFormModule_GatedAnonymouslyServesWithASession(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("GET /shared/form.mjs without a session = 200; the app shell must not serve to an anonymous caller")
	}

	r := httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil)
	r.AddCookie(cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shared/form.mjs with a session = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "export function renderOpForm") {
		t.Fatalf("did not serve the real module; body:\n%s", rec.Body.String())
	}
}
