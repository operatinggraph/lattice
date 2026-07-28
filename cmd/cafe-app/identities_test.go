package main

import (
	"net/http"
	"testing"
)

func TestHandleIdentities_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, "http://127.0.0.1:1") // nothing listens here; unused by this test
	rec := sessionGET(s, s.handleIdentities, "/api/identities", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

// TestHandleIdentities_ValidSession_PoolUnconfigured_502: a signed-in actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleIdentities_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, "http://127.0.0.1:1") // session set, pgPool nil
	rec := sessionGET(s, s.handleIdentities, "/api/identities", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
