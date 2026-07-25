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
	for _, path := range []string{"/", "/index.html", "/app.js"} {
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
