package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// Protection is a property of the WIRING, not of any handler: registerRoutes
// builds an inner mux and hands the whole thing to RequireSession. A per-handler
// test cannot see that — it wraps the handler under test in its own middleware
// and passes however registerRoutes is wired. These tests drive the real mux, so
// a route registered outside the guard, or wrongly declared auth-exempt, fails
// here and nowhere else.

// realMux builds the app's actual route table over a demo-posture session.
func realMux(t *testing.T) (*http.ServeMux, func(subject string) *http.Cookie) {
	t.Helper()
	s, cookieFor := devSessionServer(t, nil)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux, cookieFor
}

// TestRegisterRoutes_EveryAPIRouteIsSessionGated walks the app's whole /api
// surface and proves an unauthenticated caller is refused at each one. The list
// is spelled out rather than derived so that ADDING a route without gating it
// does not silently satisfy the test.
func TestRegisterRoutes_EveryAPIRouteIsSessionGated(t *testing.T) {
	mux, _ := realMux(t)
	gated := []string{
		"/api/listings",
		"/api/op-catalog",
		"/api/staff/identities",
		"/api/applications",
		"/api/credentials",
		"/api/credentials/link/complete",
		"/api/unit-applications",
		"/api/landlord/applications",
		"/api/portfolio-pulse",
		"/api/renewals",
		"/api/search",
		"/api/tasks",
		"/api/objects",
		"/api/objects/abc",
		"/api/lease-document",
		"/api/ledger",
		"/api/config",
	}
	for _, path := range gated {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401 (is it registered outside RequireSession?)", path, rec.Code)
		}
	}
}

// TestRegisterRoutes_StaticAssetsAreSessionGated: the SPA shell itself sits
// behind the session, so an anonymous browser meets the login page rather than
// an app that renders and then fails every call it makes.
func TestRegisterRoutes_StaticAssetsAreSessionGated(t *testing.T) {
	mux, _ := realMux(t)
	for _, path := range []string{"/", "/index.html", "/app.js", "/shared/form.mjs"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s without a session = 200; the app shell must not serve to an anonymous caller", path)
		}
	}
}

// TestRegisterRoutes_LoginSurfaceIsReachableAnonymously is the other half: the
// routes that EXIST to establish a session must not require one, or a signed-out
// browser has no way back in.
func TestRegisterRoutes_LoginSurfaceIsReachableAnonymously(t *testing.T) {
	mux, _ := realMux(t)
	for _, path := range []string{appsession.LoginPagePath, appsession.WhoamiPath, appsession.LoginOptionsPath} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("GET %s without a session = 401; the sign-in surface must be reachable anonymously", path)
		}
	}
}

// TestRegisterRoutes_CrossOriginWriteIsRefusedEvenWithACookie: the app is bound
// to a localhost port alongside its sibling apps, which makes their pages
// same-SITE to it — the session cookie is exactly what such a page's POST would
// ride in on. The cross-origin gate is wiring too (RequireSession, ahead of the
// exemptions), so a valid cookie must not save the request.
func TestRegisterRoutes_CrossOriginWriteIsRefusedEvenWithACookie(t *testing.T) {
	mux, cookieFor := realMux(t)
	const subject = "Hj4kPmRtw9nbCxz5vQ2y"

	for _, path := range []string{"/api/unit-applications", "/api/objects", appsession.DevLoginPath, appsession.LogoutPath} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		r.Host = "127.0.0.1:7788"
		r.Header.Set("Origin", "http://127.0.0.1:7799")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.AddCookie(cookieFor(subject))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s from a sibling localhost port = %d, want 403", path, rec.Code)
		}
	}

	// The app's own page must still get through, or the gate has taken the app
	// down instead of hardening it.
	r := httptest.NewRequest(http.MethodPost, "/api/unit-applications", strings.NewReader("{}"))
	r.Host = "127.0.0.1:7788"
	r.Header.Set("Origin", "http://127.0.0.1:7788")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(cookieFor(subject))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("POST /api/unit-applications from the app's own origin = 403; body=%s", rec.Body.String())
	}
}

// TestRegisterRoutes_SessionReachesTheHandler proves the gate opens for a real
// cookie — without this the 401s above could come from a mux that refuses
// everything.
func TestRegisterRoutes_SessionReachesTheHandler(t *testing.T) {
	mux, cookieFor := realMux(t)
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	r.AddCookie(cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config with a session = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "gatewayUrl") {
		t.Fatalf("handler did not run; body=%s", rec.Body.String())
	}
}

// TestRegisterRoutes_SharedFormModuleServes proves /shared/form.mjs resolves
// to the real embedded internal/descriptorform module through the app's own
// registerRoutes wiring — the exact StripPrefix mount every one of the four
// staff apps uses, driven here through the full mux + a real session cookie
// rather than a bare curl (RequireSession gates the whole inner mux, so an
// anonymous request 401s regardless of whether the mount itself is correct;
// only an authenticated request distinguishes a working mount from a 404 one).
func TestRegisterRoutes_SharedFormModuleServes(t *testing.T) {
	mux, cookieFor := realMux(t)
	r := httptest.NewRequest(http.MethodGet, "/shared/form.mjs", nil)
	r.AddCookie(cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shared/form.mjs with a session = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "export function renderOpForm") {
		t.Fatalf("did not serve the real module; body:\n%s", rec.Body.String())
	}
}
