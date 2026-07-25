package main

import (
	"net/http"
	"testing"
)

func TestHandleStaffIdentities_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleStaffIdentities, "/api/staff/identities", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleStaffIdentities_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleStaffIdentities, "/api/staff/identities", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

// TestHandleStaffIdentities_ValidSession_PoolUnconfigured_502: a signed-in
// actor with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleStaffIdentities_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleStaffIdentities, "/api/staff/identities", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
