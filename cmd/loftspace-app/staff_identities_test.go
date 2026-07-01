package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleStaffIdentities_NoAuthConfigured_401(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout} // authn nil
	rec := httptest.NewRecorder()
	s.handleStaffIdentities(rec, httptest.NewRequest(http.MethodGet, "/api/staff/identities", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleStaffIdentities_NoToken_401(t *testing.T) {
	s := devAuthServer(t)
	rec := httptest.NewRecorder()
	s.handleStaffIdentities(rec, httptest.NewRequest(http.MethodGet, "/api/staff/identities", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no bearer)", rec.Code)
	}
}

// TestHandleStaffIdentities_ValidToken_PoolUnconfigured_502: a verified actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleStaffIdentities_ValidToken_PoolUnconfigured_502(t *testing.T) {
	s := devAuthServer(t) // authn set, pgPool nil
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/staff/identities", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	s.handleStaffIdentities(rec, r)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
