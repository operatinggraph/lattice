package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// The cross-origin gate is a property of the WIRING: registerRoutes builds an
// inner mux and hands the whole thing to RequireSession, which runs the gate
// ahead of everything else. clinic-app is bound to a localhost port alongside
// its sibling apps, so their pages are same-SITE to it and their POSTs would
// otherwise carry this app's session cookie.

// realMux builds the app's actual route table over a session manager with no
// verifier — enough to prove WHERE the gate sits, since a 403 can then only
// come from the gate (an ungated request to these paths lands on a 401 or the
// handler's own answer).
func realMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	noPostureServer(t).registerRoutes(mux)
	return mux
}

func TestRegisterRoutes_CrossOriginWriteIsRefused(t *testing.T) {
	mux := realMux(t)
	// The session endpoints are exempt from NEEDING a session, not from proving
	// their origin: a forced login or logout is a state change too.
	for _, path := range []string{appsession.DevLoginPath, appsession.LogoutPath, appsession.RefreshPath} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		r.Host = "127.0.0.1:7799"
		r.Header.Set("Origin", "http://127.0.0.1:7788")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s from a sibling localhost port = %d, want 403", path, rec.Code)
		}
	}
}

func TestRegisterRoutes_CrossOriginSubresourceReadIsRefused(t *testing.T) {
	mux := realMux(t)
	r := httptest.NewRequest(http.MethodGet, "/api/appointments", nil)
	r.Host = "127.0.0.1:7799"
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Mode", "no-cors")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/appointments as a sibling app's subresource = %d, want 403", rec.Code)
	}
}

// TestRegisterRoutes_TheAppsOwnPageAndPlainNavigationStillWork is the other
// half: the gate must harden the app, not take it down. A same-origin write
// reaches the session layer (401 without a cookie, never 403), and a plain
// browser navigation to the login page opens from anywhere.
func TestRegisterRoutes_TheAppsOwnPageAndPlainNavigationStillWork(t *testing.T) {
	mux := realMux(t)

	r := httptest.NewRequest(http.MethodPost, appsession.RefreshPath, strings.NewReader("{}"))
	r.Host = "127.0.0.1:7799"
	r.Header.Set("Origin", "http://127.0.0.1:7799")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Errorf("POST %s from the app's own page = 403; body=%s", appsession.RefreshPath, rec.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, appsession.LoginPagePath, nil)
	r.Host = "127.0.0.1:7799"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("cross-site navigation to the login page = %d, want 200", rec.Code)
	}
}
