package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// The cross-origin gate is a property of the WIRING: registerRoutes builds an
// inner mux and hands the whole thing to RequireSession, which runs the gate
// ahead of everything else — including the routes that are exempt from needing
// a session. Facet is the publicly-served app and the one whose route table
// varies at boot (registerBrowserEngineRoutes), so the pin belongs on the real
// mux rather than on a handler.
func originGateMux(t *testing.T) *http.ServeMux {
	t.Helper()
	srv := &server{logger: slog.Default(), devSigner: testDevSigner(t), session: testSession(t, nil)}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	return mux
}

// TestRegisterRoutes_CrossOriginSubresourceGETIsRefused is the side-effect axis:
// GET /api/feed acquires the session identity's sync engine, which MINTS that
// identity's credential. A bare <img src> or EventSource on a sibling app's page
// is a same-site subresource — it carries the cookie and sends no Origin at all,
// so Fetch Metadata is the only thing that separates it from a link the user
// clicked.
func TestRegisterRoutes_CrossOriginSubresourceGETIsRefused(t *testing.T) {
	mux := originGateMux(t)
	for _, mode := range []string{"no-cors", "cors"} {
		r := httptest.NewRequest(http.MethodGet, "/api/feed", nil)
		r.Host = "127.0.0.1:7810"
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.Header.Set("Sec-Fetch-Mode", mode)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET /api/feed as a sibling app's %s subresource = %d, want 403", mode, rec.Code)
		}
	}
}

func TestRegisterRoutes_CrossOriginWriteIsRefused(t *testing.T) {
	mux := originGateMux(t)
	for _, path := range []string{"/api/enqueue", "/api/credentials/unlink", appsession.DevLoginPath, appsession.LogoutPath} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		r.Host = "127.0.0.1:7810"
		r.Header.Set("Origin", "http://127.0.0.1:7788")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s from a sibling localhost port = %d, want 403", path, rec.Code)
		}
	}
}

// TestRegisterRoutes_OwnPageAndNavigationStillWork: the gate must harden the app,
// not take it down. Facet's own SSE feed and its own writes reach the session
// layer, and a plain navigation to the login page opens from anywhere.
func TestRegisterRoutes_OwnPageAndNavigationStillWork(t *testing.T) {
	mux := originGateMux(t)

	r := httptest.NewRequest(http.MethodGet, "/api/feed", nil)
	r.Host = "127.0.0.1:7810"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Errorf("GET /api/feed from the app's own page = 403; body=%s", rec.Body.String())
	}

	r = httptest.NewRequest(http.MethodPost, "/api/enqueue", strings.NewReader("{}"))
	r.Host = "127.0.0.1:7810"
	r.Header.Set("Origin", "http://127.0.0.1:7810")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden {
		t.Errorf("POST /api/enqueue from the app's own page = 403; body=%s", rec.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, appsession.LoginPagePath, nil)
	r.Host = "127.0.0.1:7810"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("cross-site navigation to the login page = %d, want 200", rec.Code)
	}
}
