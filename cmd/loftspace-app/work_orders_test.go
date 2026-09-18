package main

import (
	"net/http"
	"testing"
)

// No-Postgres unit coverage for the landlord work-orders reader: the
// fail-closed auth/pool paths, mirroring portfolio_test.go /
// landlord_applications_test.go.

func TestHandleLandlordWorkOrders_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleLandlordWorkOrders, "/api/landlord/work-orders", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleLandlordWorkOrders_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleLandlordWorkOrders, "/api/landlord/work-orders", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleLandlordWorkOrders_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleLandlordWorkOrders, "/api/landlord/work-orders", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// A signed-in actor with no read-model pool gets a clean 502, never a
// nil-pointer panic (mirrors handlePortfolioPulse / handleLandlordApplications).
func TestHandleLandlordWorkOrders_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleLandlordWorkOrders, "/api/landlord/work-orders", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// The same fail-closed auth/pool coverage for the reporter's own read
// (handleMyWorkOrders, GET /api/my/work-orders — decision 7).

func TestHandleMyWorkOrders_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleMyWorkOrders, "/api/my/work-orders", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyWorkOrders_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleMyWorkOrders, "/api/my/work-orders", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleMyWorkOrders_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleMyWorkOrders, "/api/my/work-orders", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

func TestHandleMyWorkOrders_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleMyWorkOrders, "/api/my/work-orders", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}
