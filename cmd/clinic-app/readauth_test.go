package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer abc.def.ghi", "abc.def.ghi"}, // case-insensitive scheme
		{"Bearer   spaced  ", "spaced"},
		{"Basic abc", ""},
		{"abc.def.ghi", ""},
		{"", ""},
		{"Bearer ", ""}, // scheme only, no token
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := bearerToken(r); got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " On "} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "x"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}

// TestSetupReadAuth_DevPosture proves the demo posture wires a verifier whose
// trust matches the minter — a token the signer mints verifies, and its subject
// round-trips to the RLS principal.
func TestSetupReadAuth_DevPosture(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	if authn == nil || signer == nil {
		t.Fatalf("dev posture must return non-nil authn (%v) and signer (%v)", authn, signer)
	}

	const sub = "Hj4kPmRtw9nbCxz5vQ2y"
	tok, _, err := signer.mint(sub)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	actor, err := authn.Authenticate(t.Context(), tok)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != sub {
		t.Errorf("subject = %q, want %q", actor.Subject, sub)
	}
	if actor.ActorID != "vtx.identity."+sub {
		t.Errorf("actorID = %q, want vtx.identity.%s", actor.ActorID, sub)
	}
}

// TestSetupReadAuth_NoPosture: neither env set ⇒ no authenticator (fail closed).
func TestSetupReadAuth_NoPosture(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "")
	t.Setenv("CLINIC_APP_JWT_PUBLIC_KEY", "")
	authn, signer, err := setupReadAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	if authn != nil || signer != nil {
		t.Fatalf("no posture must return nil authn/signer, got authn=%v signer=%v", authn, signer)
	}
}

// TestSetupReadAuth_BadPublicKey: a configured but unparseable key is a hard
// misconfiguration, not a silent deny-all.
func TestSetupReadAuth_BadPublicKey(t *testing.T) {
	t.Setenv("CLINIC_APP_DEV_AUTH", "")
	t.Setenv("CLINIC_APP_JWT_PUBLIC_KEY", "not a pem")
	if _, _, err := setupReadAuth(discardLogger(), true); err == nil {
		t.Fatal("expected an error for an unparseable public key")
	}
}

// devAuthServer builds a server with the demo auth posture for handler tests.
func devAuthServer(t *testing.T) *server {
	t.Helper()
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	return &server{logger: discardLogger(), authn: authn, devSigner: signer, natsTimeout: testTimeout}
}

func TestHandleMyAppointments_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleMyAppointments(rec, httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyAppointments_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleMyAppointments(rec, httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

func TestHandleMyAppointments_ForgedToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer not.a.valid.jwt")
	s.handleMyAppointments(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged token)", rec.Code)
	}
}

// TestHandleMyAppointments_ValidToken_PoolUnconfigured_502: a verified actor with
// no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyAppointments_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleMyAppointments(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
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
	actor, err := s.authn.Authenticate(t.Context(), resp.Token)
	if err != nil {
		t.Fatalf("authenticate minted token: %v", err)
	}
	if actor.Subject != "Hj4kPmRtw9nbCxz5vQ2y" {
		t.Errorf("subject = %q", actor.Subject)
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
