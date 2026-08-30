package main

import (
	"net/http"
	"testing"
)

func TestHandleIdentities_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t)
	rec := sessionGET(s, s.handleIdentities, "/api/identities", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

// TestHandleIdentities_ValidSession_PoolUnconfigured_502: a signed-in actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleIdentities_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t) // session set, pgPool nil
	rec := sessionGET(s, s.handleIdentities, "/api/identities", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// TestHandleIdentities_QParam_PoolUnconfigured_502: the same clean 502 with a
// `?q=` search term on the request — the new front-desk typeahead query
// parameter is read before the pool-nil check trips, so it must never panic
// or change the pool-unconfigured error path.
func TestHandleIdentities_QParam_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t) // session set, pgPool nil
	rec := sessionGET(s, s.handleIdentities, "/api/identities?q=ali", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
