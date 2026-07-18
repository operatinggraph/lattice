package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes. Mirrors
// cmd/clinic-app/readauth_test.go's own TestMain.
func TestMain(m *testing.M) {
	os.Setenv("CAFE_APP_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Exit(m.Run())
}

// devAuthServer builds a server with the demo dev-auth posture for handler tests.
func devAuthServer(t *testing.T) *server {
	t.Helper()
	t.Setenv("CAFE_APP_DEV_AUTH", "1")
	signer, err := setupDevSigner(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupDevSigner: %v", err)
	}
	return &server{logger: discardLogger(), devSigner: signer, natsTimeout: testTimeout}
}

func TestHandleDevToken_Disabled_404(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // devSigner nil
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"x"}`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (dev-token disabled)", rec.Code)
	}
}

func TestHandleDevToken_Mint_RoundTrips(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"Hj4kPmRtw9nbCxz5vQ2y"}`))
	s.handleDevToken(rec, r)
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
}

func TestHandleDevToken_EmptySubject_400(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`{"subject":"  "}`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDevToken_WrongMethod_405(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/dev-token", nil)
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleDevToken_InvalidJSON_400(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/dev-token", strings.NewReader(`not json`))
	s.handleDevToken(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
