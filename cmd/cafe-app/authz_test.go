package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
)

const testTimeout = 5 * time.Second

// newTestConn spins up an embedded JetStream server carrying every bucket
// cafe-app's read handlers touch (weaver-targets + the cafe-ledger read
// models), so the handler-level tests below drive the REAL read path —
// KVListKeys/KVGet — rather than the pure computeXxx seam (mirrors
// cmd/loftspace-app/objects_crypto_test.go's sensitiveObjectFixture).
func newTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "cafe-app-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	for _, bucket := range []string{weaverTargetsBucket, cafeledger.LeaseAccountsBucket, cafeledger.LedgerHistoryBucket, cafedomain.MenuCatalogBucket} {
		if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatalf("create %s bucket: %v", bucket, err)
		}
	}
	return conn
}

// putJSON seeds one lens row.
func putJSON(t *testing.T, conn *substrate.Conn, bucket, key string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s/%s: %v", bucket, key, err)
	}
	if _, err := conn.KVPut(context.Background(), bucket, key, b); err != nil {
		t.Fatalf("KVPut %s/%s: %v", bucket, key, err)
	}
}

// devSessionServer builds a server whose session surface is the real
// appsession kit in the demo posture (the shared dev key) and whose NATS
// connection is a real embedded JetStream server, returning the helper that
// mints a session cookie for a bare identity id. gatewayURL points at a
// fakeGatewayActor double standing in for the Gateway's /v1/actor door
// resolveSubjectHats calls.
func devSessionServer(t *testing.T, gatewayURL string) (*server, func(subject string) *http.Cookie) {
	t.Helper()
	t.Setenv(envPrefix+"_DEV_AUTH", "1")
	signer, err := appsession.NewDevSigner(discardLogger(), envPrefix, true)
	if err != nil {
		t.Fatalf("NewDevSigner: %v", err)
	}
	authn, refreshAuthn, err := appsession.NewAuthenticators(discardLogger(), envPrefix, signer, nil)
	if err != nil {
		t.Fatalf("NewAuthenticators: %v", err)
	}
	session, err := appsession.New(appsession.Config{
		AppName:      appName,
		EnvPrefix:    envPrefix,
		Logger:       discardLogger(),
		GatewayURL:   gatewayURL,
		Signer:       signer,
		Authn:        authn,
		RefreshAuthn: refreshAuthn,
		Loopback:     true,
		LoginPage:    []byte("<html>login</html>"),
	})
	if err != nil {
		t.Fatalf("appsession.New: %v", err)
	}
	s := &server{
		logger:      discardLogger(),
		authn:       authn,
		session:     session,
		natsTimeout: testTimeout,
		gatewayURL:  gatewayURL,
		conn:        newTestConn(t),
	}
	return s, func(subject string) *http.Cookie {
		t.Helper()
		tok, exp, err := signer.Mint(subject)
		if err != nil {
			t.Fatalf("mint %s: %v", subject, err)
		}
		return &http.Cookie{Name: session.CookieName(), Value: tok, Expires: exp}
	}
}

// sessionGET drives one GET through the real session middleware onto h.
func sessionGET(s *server, h http.HandlerFunc, path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.session.RequireSession(h).ServeHTTP(rec, r)
	return rec
}

// seedLease seeds one leaseApplicationComplete row (residents.go's
// computeResidents) plus a cafeLeaseAccounts row (leases.go's computeLeases)
// for leaseAppKey, applied for by bookerIdentity (a bare NanoID).
func seedLease(t *testing.T, conn *substrate.Conn, leaseAppKey, bookerIdentity string) {
	t.Helper()
	putJSON(t, conn, weaverTargetsBucket, leaseApplicationKeyPrefix+leaseAppKey, map[string]any{
		"entityKey":        leaseAppKey,
		"applicant":        "vtx.identity." + bookerIdentity,
		"landlordApproved": true,
	})
	putJSON(t, conn, cafeledger.LeaseAccountsBucket, leaseAppKey, map[string]any{
		"leaseAppKey": leaseAppKey,
		"accountKey":  "",
	})
}

// seedOpenTab seeds one open cafeTabSettlement row for leaseAppKey.
func seedOpenTab(t *testing.T, conn *substrate.Conn, tabKey, leaseAppKey string) {
	t.Helper()
	putJSON(t, conn, weaverTargetsBucket, "cafeTabSettlement."+tabKey, map[string]any{
		"tabKey":      tabKey,
		"leaseAppKey": leaseAppKey,
		"totalCents":  500.0,
		"status":      "open",
		"openedAt":    "2026-07-20T10:00:00Z",
	})
}

// ---- /api/leases ----

func TestHandleLeases_Unauthenticated_401(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	rec := sessionGET(s, s.handleLeases, "/api/leases", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleLeases_Resident_SeesOnlyOwnLease(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleLeases, "/api/leases", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Leases []leaseRow `json:"leases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leases) != 1 || body.Leases[0].LeaseAppKey != "vtx.leaseapp.aaa" {
		t.Fatalf("resident A's leases = %+v, want exactly [vtx.leaseapp.aaa] (never resident B's)", body.Leases)
	}
}

func TestHandleLeases_Staff_SeesTheHouse(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleLeases, "/api/leases", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Leases []leaseRow `json:"leases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leases) != 2 {
		t.Fatalf("staff's leases = %+v, want both leases (the house)", body.Leases)
	}
}

// ---- /api/tabs ----

func TestHandleTabs_Unauthenticated_401(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	rec := sessionGET(s, s.handleTabs, "/api/tabs", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleTabs_Resident_SeesOnlyOwnLeaseTab(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)
	seedOpenTab(t, s.conn, "vtx.tab.a1", "vtx.leaseapp.aaa")
	seedOpenTab(t, s.conn, "vtx.tab.b1", "vtx.leaseapp.bbb")

	rec := sessionGET(s, s.handleTabs, "/api/tabs", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Tabs []tabRow `json:"tabs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tabs) != 1 || body.Tabs[0].TabKey != "vtx.tab.a1" {
		t.Fatalf("resident A's tabs = %+v, want exactly [vtx.tab.a1] (never resident B's)", body.Tabs)
	}
}

func TestHandleTabs_Resident_ExplicitOtherLease_403(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)
	seedOpenTab(t, s.conn, "vtx.tab.b1", "vtx.leaseapp.bbb")

	rec := sessionGET(s, s.handleTabs, "/api/tabs?leaseAppKey=vtx.leaseapp.bbb", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (resident A naming resident B's lease)", rec.Code)
	}
}

// The positive counterpart to the 403 above: naming a lease EXPLICITLY is
// how the resident view fetches its own tab, so the check has to
// discriminate on whose lease it is. Without this, inverting the ownership
// test would still pass the 403 case while refusing every legitimate read.
func TestHandleTabs_Resident_ExplicitOwnLease_200(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)
	seedOpenTab(t, s.conn, "vtx.tab.a1", "vtx.leaseapp.aaa")

	rec := sessionGET(s, s.handleTabs, "/api/tabs?leaseAppKey=vtx.leaseapp.aaa", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (resident A naming their OWN lease)", rec.Code)
	}
	var body struct {
		Tabs []tabRow `json:"tabs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tabs) != 1 || body.Tabs[0].TabKey != "vtx.tab.a1" {
		t.Fatalf("resident A's own-lease tabs = %+v, want exactly [vtx.tab.a1]", body.Tabs)
	}
}

func TestHandleTabs_Staff_SeesTheHouse(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)
	seedOpenTab(t, s.conn, "vtx.tab.a1", "vtx.leaseapp.aaa")
	seedOpenTab(t, s.conn, "vtx.tab.b1", "vtx.leaseapp.bbb")

	rec := sessionGET(s, s.handleTabs, "/api/tabs", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Tabs []tabRow `json:"tabs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tabs) != 2 {
		t.Fatalf("staff's tabs = %+v, want both tabs (the house)", body.Tabs)
	}
}

// ---- /api/residents ----

func TestHandleResidents_Resident_SeesOnlyOwnRow(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Residents []residentRow `json:"residents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Residents) != 1 || body.Residents[0].BookerKey != "vtx.identity."+resA {
		t.Fatalf("resident A's roster = %+v, want exactly their own row", body.Residents)
	}
}

func TestHandleResidents_Staff_SeesTheRoster(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Residents []residentRow `json:"residents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Residents) != 2 {
		t.Fatalf("staff's roster = %+v, want both residents", body.Residents)
	}
}

// ---- /api/ledger ----

func TestHandleLedger_Resident_OwnLease_OK(t *testing.T) {
	staff, resA := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.aaa", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleLedger_Resident_OtherLease_403(t *testing.T) {
	staff, resA, resB := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.aaa", resA)
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.bbb", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (resident A reading resident B's ledger)", rec.Code)
	}
}

func TestHandleLedger_Staff_AnyLease_OK(t *testing.T) {
	staff, resB := "AAAAAAAAAAAAAAAAAAAA", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.bbb", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (staff must read any lease's ledger)", rec.Code, rec.Body.String())
	}
}

// ---- /api/frontdesk-* (staff-only surface) ----

func TestHandleFrontDeskBookings_Unauthenticated_401(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	rec := sessionGET(s, s.handleFrontDeskBookings, "/api/frontdesk-bookings", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleFrontDeskBookings_Resident_403(t *testing.T) {
	staff, resA := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	rec := sessionGET(s, s.handleFrontDeskBookings, "/api/frontdesk-bookings", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (front desk is staff-only)", rec.Code)
	}
}

func TestHandleFrontDeskBookings_Staff_200(t *testing.T) {
	staff := "AAAAAAAAAAAAAAAAAAAA"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	rec := sessionGET(s, s.handleFrontDeskBookings, "/api/frontdesk-bookings", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s, want 200 for staff", rec.Code, rec.Body.String())
	}
}

func TestHandleFrontDeskLeaseDetails_Resident_403(t *testing.T) {
	staff, resA := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	rec := sessionGET(s, s.handleFrontDeskLeaseDetails, "/api/frontdesk-lease-details", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (front desk is staff-only)", rec.Code)
	}
}

func TestHandleFrontDeskVisits_Resident_403(t *testing.T) {
	staff, resA := "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	rec := sessionGET(s, s.handleFrontDeskVisits, "/api/frontdesk-visits", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (front desk is staff-only)", rec.Code)
	}
}

// ---- /api/menu (public catalog: session required, no per-subject filtering) ----

func TestHandleMenu_Unauthenticated_401(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	rec := sessionGET(s, s.handleMenu, "/api/menu", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMenu_AnySignedInSubject_200(t *testing.T) {
	resA := "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, nil))
	rec := sessionGET(s, s.handleMenu, "/api/menu", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s, want 200 (menu is a public catalog once signed in)", rec.Code, rec.Body.String())
	}
}

// ---- whole-mux wiring: RequireSession covers every read (persona-worlds-design.md Fire W4 §3) ----

func TestRegisterRoutes_UncredentialedAPIRead_401(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	for _, path := range []string{
		"/api/leases", "/api/tabs", "/api/ledger?leaseAppKey=vtx.leaseapp.x",
		"/api/residents", "/api/menu",
		"/api/frontdesk-bookings", "/api/frontdesk-lease-details", "/api/frontdesk-visits",
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 uncredentialed", path, rec.Code)
		}
	}
}

func TestRegisterRoutes_LoginPageServedWithNoSession(t *testing.T) {
	s, _ := devSessionServer(t, fakeGatewayActor(t, nil))
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login uncredentialed: status = %d, want 200 (the kit's own routes must not be behind RequireSession)", rec.Code)
	}
}
