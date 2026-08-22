package main

import (
	"net/http"
	"testing"
)

// TestHandleIdentities_NoCookie_401 — the roster is session-gated before any
// query runs, so an unauthenticated caller can never reach the name model.
func TestHandleIdentities_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleIdentities, "/api/identities", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

// TestHandleIdentities_ValidSession_PoolUnconfigured_502 — a signed-in actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleIdentities_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // pgPool left nil
	rec := sessionGET(s, s.handleIdentities, "/api/identities", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
