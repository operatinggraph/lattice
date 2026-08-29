package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
	frontdesk "github.com/operatinggraph/lattice/packages/front-desk"
)

const testTimeout = 5 * time.Second

// newTestConn spins up an embedded JetStream server carrying every bucket
// cafe-app's read handlers touch (weaver-targets + the cafe-ledger read
// models), so the handler-level tests below drive the REAL read path —
// KVListKeys/KVGet — rather than the pure computeXxx seam (mirrors
// cmd/loftspace-app/objects_crypto_test.go's sensitiveObjectFixture).
func newTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	ns := natsfixture.StartServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "cafe-app-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	for _, bucket := range []string{weaverTargetsBucket, cafeledger.LeaseAccountsBucket, cafeledger.LedgerHistoryBucket, cafedomain.MenuCatalogBucket, cafedomain.LeaseWorkplacesBucket,
		frontdesk.BookingsBucket, frontdesk.LeaseDetailsBucket, frontdesk.VisitsBucket} {
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
	// The read boundary compares a role entry against the primordial operator
	// role KEY, which main() resolves from the bootstrap ids at boot. Without
	// them that key is empty and a fixture minting an empty role would prove
	// nothing.
	testutil.EnsurePrimordials(t)
	if bootstrap.RoleOperatorKey == "" {
		t.Fatal("primordial ids loaded but the operator role key is empty")
	}
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
	seedLeaseAt(t, conn, leaseAppKey, bookerIdentity, staffWorkplace)
}

// seedLeaseAt is seedLease with the lease's covering locations named — the
// cafeLeaseWorkplaces row the staff read boundary intersects against. Passing
// no location at all seeds the row with an EMPTY covering set, the shape an
// unwired lease projects, which must deny every staffer rather than be read as
// unrestricted.
func seedLeaseAt(t *testing.T, conn *substrate.Conn, leaseAppKey, bookerIdentity string, coveringLocations ...string) {
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
	putJSON(t, conn, cafedomain.LeaseWorkplacesBucket, leaseAppKey, map[string]any{
		"leaseAppKey":       leaseAppKey,
		"coveringLocations": coveringLocations,
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

func TestHandleLeases_Staff_SeesCoveredLeases(t *testing.T) {
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
		t.Fatalf("staff's leases = %+v, want both leases (both sit at this staffer's workplace)", body.Leases)
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

func TestHandleTabs_Staff_SeesCoveredTabs(t *testing.T) {
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
		t.Fatalf("staff's tabs = %+v, want both tabs (both leases sit at this staffer's workplace)", body.Tabs)
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

func TestHandleResidents_Staff_SeesCoveredRoster(t *testing.T) {
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
		t.Fatalf("staff's roster = %+v, want both residents (both leases sit at this staffer's workplace)", body.Residents)
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

func TestHandleLedger_Staff_CoveredLease_OK(t *testing.T) {
	staff, resB := "AAAAAAAAAAAAAAAAAAAA", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	seedLease(t, s.conn, "vtx.leaseapp.bbb", resB)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.bbb", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (staff must read the ledger of a lease their workplace covers)", rec.Code, rec.Body.String())
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

// ---- /api/menu (session required; leaseAppKey confines the catalog to what that lease's Charge would accept) ----

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

// A self-order picker (leaseAppKey named) offers only what a Charge against
// that lease would actually accept — the offer side of cafe-domain's
// location_covers bound.
func TestHandleMenu_LeaseAppKey_OffersOnlyItemsChargeWouldAccept(t *testing.T) {
	resA := "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.aaa", resA, staffWorkplace)
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.covered", map[string]any{
		"menuItemKey": "vtx.menuitem.covered", "name": "Latte", "priceCents": 450, "servedAt": staffWorkplace,
	})
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.elsewhere", map[string]any{
		"menuItemKey": "vtx.menuitem.elsewhere", "name": "Croissant", "priceCents": 350, "servedAt": otherWorkplace,
	})

	rec := sessionGET(s, s.handleMenu, "/api/menu?leaseAppKey=vtx.leaseapp.aaa", cookieFor(resA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Menu []menuItemRow `json:"menu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Menu) != 1 || body.Menu[0].MenuItemKey != "vtx.menuitem.covered" {
		t.Fatalf("menu = %+v, want exactly the item served at this lease's covering location", body.Menu)
	}
}

func TestHandleMenu_LeaseAppKey_NotYourLease_403(t *testing.T) {
	resA, resB := "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.aaa", resA, staffWorkplace)
	seedLeaseAt(t, s.conn, "vtx.leaseapp.bbb", resB, staffWorkplace)

	rec := sessionGET(s, s.handleMenu, "/api/menu?leaseAppKey=vtx.leaseapp.bbb", cookieFor(resA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (resident A naming resident B's lease)", rec.Code)
	}
}

// TestHandleMenu_NoLeaseAppKey_StaffConfinedToWorkplace proves the
// front-desk Manage Menu grid (no leaseAppKey — loadManageMenu,
// cafe-app/web/app.js) no longer shows every property's catalog: a staffer
// wired to staffWorkplace sees only the item whose coveringLocations chain
// reaches it, never the item served at an unrelated building.
func TestHandleMenu_NoLeaseAppKey_StaffConfinedToWorkplace(t *testing.T) {
	staff := "AAAAAAAAAAAAAAAAAAAA"
	s, cookieFor := devSessionServer(t, fakeGatewayActor(t, map[string]bool{staff: true}))
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.covered", map[string]any{
		"menuItemKey": "vtx.menuitem.covered", "name": "Latte", "priceCents": 450,
		"servedAt": staffWorkplace, "coveringLocations": []string{staffWorkplace},
	})
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.elsewhere", map[string]any{
		"menuItemKey": "vtx.menuitem.elsewhere", "name": "Croissant", "priceCents": 350,
		"servedAt": otherWorkplace, "coveringLocations": []string{otherWorkplace},
	})

	rec := sessionGET(s, s.handleMenu, "/api/menu", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Menu []menuItemRow `json:"menu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Menu) != 1 || body.Menu[0].MenuItemKey != "vtx.menuitem.covered" {
		t.Fatalf("menu = %+v, want exactly the item this staffer's workplace covers", body.Menu)
	}
}

// TestHandleMenu_NoLeaseAppKey_RoleLessWorksAt_StillConfined: handleMenu's
// no-leaseAppKey branch gates confinement on isStaff (workplace alone), NOT
// isFrontDesk — unlike every PII/write surface, there is no refusal branch
// here at all, so narrowing the filter condition would have DROPPED the
// confinement instead of tightening it (admit==nil reads as "unfiltered",
// computeMenu). fakeGatewayActor auto-grants frontOfHouse to every staff
// subject, which would mask that regression, so this uses
// fakeGatewayActorRoles directly to prove a worksAt caller with NO role at
// all still gets the workplace-confined view, not the whole catalog.
func TestHandleMenu_NoLeaseAppKey_RoleLessWorksAt_StillConfined(t *testing.T) {
	staff := "AAAAAAAAAAAAAAAAAAAA"
	s, cookieFor := devSessionServer(t, fakeGatewayActorRoles(t,
		map[string][]string{staff: {staffWorkplace}}, nil, nil))
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.covered", map[string]any{
		"menuItemKey": "vtx.menuitem.covered", "name": "Latte", "priceCents": 450,
		"servedAt": staffWorkplace, "coveringLocations": []string{staffWorkplace},
	})
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.elsewhere", map[string]any{
		"menuItemKey": "vtx.menuitem.elsewhere", "name": "Croissant", "priceCents": 350,
		"servedAt": otherWorkplace, "coveringLocations": []string{otherWorkplace},
	})

	rec := sessionGET(s, s.handleMenu, "/api/menu", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Menu []menuItemRow `json:"menu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Menu) != 1 || body.Menu[0].MenuItemKey != "vtx.menuitem.covered" {
		t.Fatalf("menu = %+v, want exactly the item this staffer's workplace covers (not the whole catalog)", body.Menu)
	}
}

// TestHandleMenu_NoLeaseAppKey_OperatorSeesEveryItem proves the operator
// exemption: break-glass admin (the write side's actor_holds_operator
// exemption from require_workplace, ddls.go) can also see the whole catalog
// on the same grid a workplace-bound staffer only sees part of.
func TestHandleMenu_NoLeaseAppKey_OperatorSeesEveryItem(t *testing.T) {
	op := "AAAAAAAAAAAAAAAAAAAA"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{op: {staffWorkplace}}, map[string]bool{op: true}))
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.covered", map[string]any{
		"menuItemKey": "vtx.menuitem.covered", "name": "Latte", "priceCents": 450,
		"servedAt": staffWorkplace, "coveringLocations": []string{staffWorkplace},
	})
	putJSON(t, s.conn, cafedomain.MenuCatalogBucket, "vtx.menuitem.elsewhere", map[string]any{
		"menuItemKey": "vtx.menuitem.elsewhere", "name": "Croissant", "priceCents": 350,
		"servedAt": otherWorkplace, "coveringLocations": []string{otherWorkplace},
	})

	rec := sessionGET(s, s.handleMenu, "/api/menu", cookieFor(op))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Menu []menuItemRow `json:"menu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Menu) != 2 {
		t.Fatalf("menu = %+v, want both items unfiltered for the operator", body.Menu)
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

// ---- workplace confinement (facet-staff-worlds-design.md §9) ----
//
// Every test here pairs a POSITIVE and a NEGATIVE vector for the SAME staffer
// on the SAME endpoint, differing only in where the lease sits. A negative
// alone would pass against a boundary that denied everything, and a positive
// alone against one that admitted everything; only the pair proves the term is
// the workplace and not something else.

// staffAtOneBuilding seeds two leases — one at the staffer's workplace, one at
// a building they do not work at — and returns the server plus the cookie
// helper. Both leases are otherwise identical, so any difference in what the
// staffer can read is attributable to the covering set alone.
func staffAtOneBuilding(t *testing.T) (*server, func(string) *http.Cookie, string) {
	t.Helper()
	const staff, resA, resB = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{staff: {staffWorkplace}}, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.mine", resA, staffWorkplace)
	seedLeaseAt(t, s.conn, "vtx.leaseapp.theirs", resB, otherWorkplace)
	return s, cookieFor, staff
}

func TestHandleLeases_Staff_SeesOnlyLeasesTheirWorkplaceCovers(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)

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
	if len(body.Leases) != 1 || body.Leases[0].LeaseAppKey != "vtx.leaseapp.mine" {
		t.Fatalf("staff's leases = %+v, want exactly [vtx.leaseapp.mine] — the other building's lease must not be listed", body.Leases)
	}
}

func TestHandleTabs_Staff_ForeignLeaseTabsNotListed(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	seedOpenTab(t, s.conn, "vtx.tab.mine", "vtx.leaseapp.mine")
	seedOpenTab(t, s.conn, "vtx.tab.theirs", "vtx.leaseapp.theirs")

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
	if len(body.Tabs) != 1 || body.Tabs[0].TabKey != "vtx.tab.mine" {
		t.Fatalf("staff's tabs = %+v, want exactly [vtx.tab.mine]", body.Tabs)
	}
}

// TestHandleTabs_Staff_NamedForeignLease_403 is the other half of the tab
// boundary: filtering the unfiltered list is not enough if naming the lease
// outright still answers. The refusal must also not read like the resident's
// ("that lease is not yours"), which would be false at the front desk.
func TestHandleTabs_Staff_NamedForeignLease_403(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	seedOpenTab(t, s.conn, "vtx.tab.theirs", "vtx.leaseapp.theirs")

	rec := sessionGET(s, s.handleTabs, "/api/tabs?leaseAppKey=vtx.leaseapp.theirs", cookieFor(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (a lease at a building this staffer does not work at); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not at a place you work") {
		t.Errorf("denial = %s, want the staff-shaped reason", rec.Body.String())
	}

	ok := sessionGET(s, s.handleTabs, "/api/tabs?leaseAppKey=vtx.leaseapp.mine", cookieFor(staff))
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the lease at their own workplace; body=%s", ok.Code, ok.Body.String())
	}
}

func TestHandleLedger_Staff_ForeignLease_403(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.theirs", cookieFor(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (another building's ledger); body=%s", rec.Code, rec.Body.String())
	}
	ok := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.mine", cookieFor(staff))
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for their own building's ledger; body=%s", ok.Code, ok.Body.String())
	}
}

func TestHandleResidents_Staff_SeesOnlyCoveredApplicants(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)

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
	if len(body.Residents) != 1 || body.Residents[0].LeaseAppKey != "vtx.leaseapp.mine" {
		t.Fatalf("staff's roster = %+v, want only the applicant at their own building", body.Residents)
	}
}

// TestHandleFrontDeskLeaseDetails_Staff_SeesOnlyCoveredLeases proves the grid
// itself narrows, not merely the pickers feeding it. The row carries the
// unit's address and rent, so an unconfined grid published one building's
// tenancy terms to another building's front desk.
func TestHandleFrontDeskLeaseDetails_Staff_SeesOnlyCoveredLeases(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	for _, lease := range []string{"vtx.leaseapp.mine", "vtx.leaseapp.theirs"} {
		putJSON(t, s.conn, frontdesk.LeaseDetailsBucket, lease, map[string]any{
			"leaseAppKey": lease, "unitAddress": "1 Main St", "unitRent": 2400.0,
		})
	}

	rec := sessionGET(s, s.handleFrontDeskLeaseDetails, "/api/frontdesk-lease-details", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		LeaseDetails []leaseDetailRow `json:"leaseDetails"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.LeaseDetails) != 1 || body.LeaseDetails[0].LeaseAppKey != "vtx.leaseapp.mine" {
		t.Fatalf("front-desk grid = %+v, want only the lease at their own building", body.LeaseDetails)
	}
}

// TestHandleFrontDeskBookings_Staff_SeesOnlyCoveredLeases: the booked-class
// badge is a cross-vertical join (a wellness booking surfaced on a café grid),
// so it is the row most likely to carry another building's resident.
func TestHandleFrontDeskBookings_Staff_SeesOnlyCoveredLeases(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	for _, lease := range []string{"vtx.leaseapp.mine", "vtx.leaseapp.theirs"} {
		putJSON(t, s.conn, frontdesk.BookingsBucket, lease, map[string]any{
			"bookingKey": "vtx.booking." + lease, "leaseAppKey": lease, "sessionName": "Vinyasa Flow",
		})
	}

	rec := sessionGET(s, s.handleFrontDeskBookings, "/api/frontdesk-bookings", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Bookings []bookingRow `json:"bookings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Bookings) != 1 || body.Bookings[0].LeaseAppKey != "vtx.leaseapp.mine" {
		t.Fatalf("front-desk bookings = %+v, want only the lease at their own building", body.Bookings)
	}
}

// TestStaff_ContainingLocationCovers is the app-side half of the containment
// walk: given a covering set that holds BOTH the lease's own unit and the
// building above it — the shape the lens projects — a staffer wired to the
// building is admitted, not only one wired to the exact unit. That the LENS
// actually walks containedIn to produce that set is pinned separately, and by
// construction, in cafe-domain's TestCafeLeaseWorkplaces_CoveringLocations;
// this test seeds the row and so cannot prove it.
func TestStaff_ContainingLocationCovers(t *testing.T) {
	const staff, res = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	const unitKey = "vtx.unit.K3mWqZbNxKrPvL7dHcyR"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{staff: {staffWorkplace}}, nil))
	// The unit's own key AND the building above it, the shape the lens's
	// `containedIn*0..` comprehension projects.
	seedLeaseAt(t, s.conn, "vtx.leaseapp.mine", res, unitKey, staffWorkplace)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.mine", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the staffer works at a location CONTAINING the lease's unit); body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestStaff_UnwiredLeaseDenied: a lease whose covering set is empty — its unit
// has no containment and the lease may have no unit at all — is covered by
// nobody. This is the vector that would otherwise wave a whole class of rows
// through, since an empty set is the shape the lens projects for anything the
// topology has not reached yet.
func TestStaff_UnwiredLeaseDenied(t *testing.T) {
	const staff, res = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{staff: {staffWorkplace}}, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.nowhere", res)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.nowhere", cookieFor(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (an unwired lease is covered by nobody); body=%s", rec.Code, rec.Body.String())
	}
}

// TestStaff_LeaseWithNoWorkplaceRowDenied is the STALE-PROJECTION vector: a
// lease that has a leaseApplicationComplete row but no cafeLeaseWorkplaces row
// at all — exactly what a stack projects between installing cafe-domain 0.8.0
// and the Refractor catching up. An absent row must deny like an empty one,
// rather than falling through to visible.
func TestStaff_LeaseWithNoWorkplaceRowDenied(t *testing.T) {
	const staff, res = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{staff: {staffWorkplace}}, nil))
	putJSON(t, s.conn, weaverTargetsBucket, leaseApplicationKeyPrefix+"vtx.leaseapp.stale", map[string]any{
		"entityKey": "vtx.leaseapp.stale", "applicant": "vtx.identity." + res, "landlordApproved": true,
	})

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.stale", cookieFor(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (no covering projection yet ⇒ covered by nobody); body=%s", rec.Code, rec.Body.String())
	}
}

// TestMultiWorkplaceStaff_ReadsBoth: a staffer wired to two buildings reads
// both, so the intersection is a UNION over their workplaces and not a
// first-match.
func TestMultiWorkplaceStaff_ReadsBoth(t *testing.T) {
	const staff, resA, resB = "AAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{staff: {staffWorkplace, otherWorkplace}}, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.north", resA, staffWorkplace)
	seedLeaseAt(t, s.conn, "vtx.leaseapp.south", resB, otherWorkplace)

	rec := sessionGET(s, s.handleLeases, "/api/leases", cookieFor(staff))
	var body struct {
		Leases []leaseRow `json:"leases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leases) != 2 {
		t.Fatalf("two-building staffer's leases = %+v, want both", body.Leases)
	}
}

// TestOperator_ReadsEveryLease: the primordial operator holds NO workplace and
// still reads any lease — the exemption require_workplace gives root on the
// write side (facet-staff-worlds-design.md §9). It is asserted against
// bootstrap.RoleOperatorKey because /v1/actor forwards role VERTEX KEYS; a
// fixture answering the canonical name "operator" would pass here while
// matching nothing against a live Gateway.
func TestOperator_ReadsEveryLease(t *testing.T) {
	const root, res = "FFFFFFFFFFFFFFFFFFFF", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{}, map[string]bool{root: true}))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.theirs", res, otherWorkplace)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.theirs", cookieFor(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an operator holds no workplace and still reads any lease); body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestNonFrontOfHouseRole_IsNotExempt: a worksAt caller holding some OTHER
// role — neither the primordial operator nor frontOfHouse — reaches the
// front desk at NEITHER lease, including the one at their own workplace.
// Before isFrontDesk (readauth.go), a bare worksAt anchor was sufficient
// regardless of role, which is exactly the gap
// verticals-designer-triage-2026-08-27.md §7 closes: the write side has
// always required `GrantsTo: [operator, frontOfHouse]`, so a worksAt-only
// or arbitrary-role staffer held zero POS grants already — this proves the
// read side now agrees.
func TestNonFrontOfHouseRole_IsNotExempt(t *testing.T) {
	const other, res = "EEEEEEEEEEEEEEEEEEEE", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActorRoles(t,
		map[string][]string{other: {staffWorkplace}},
		map[string]bool{},
		map[string][]string{other: {nonOperatorRoleKey}}))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.theirs", res, otherWorkplace)
	seedLeaseAt(t, s.conn, "vtx.leaseapp.mine", res, staffWorkplace)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.theirs", cookieFor(other))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (a role that is not frontOfHouse confers no exemption); body=%s",
			rec.Code, rec.Body.String())
	}
	// Same caller, same role, a lease at their OWN workplace: without
	// frontOfHouse this is refused too — worksAt alone is topology, not
	// authority (permissions.go).
	ok := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.mine", cookieFor(other))
	if ok.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (worksAt without frontOfHouse admits nothing, even at their own workplace); body=%s",
			ok.Code, ok.Body.String())
	}
}

// TestRoleLessWorksAt_IsNotFrontDesk is the exact shape
// verticals-designer-triage-2026-08-27.md §7 named: a `worksAt` caller with
// NO role at all (the 3-of-10 seeded identities the row calls "plausibly
// intentional backOfHouse personas") reaches the front desk at nobody's
// lease, including their own workplace's.
func TestRoleLessWorksAt_IsNotFrontDesk(t *testing.T) {
	const other, res = "GGGGGGGGGGGGGGGGGGGG", "HHHHHHHHHHHHHHHHHHHH"
	s, cookieFor := devSessionServer(t, fakeGatewayActorRoles(t,
		map[string][]string{other: {staffWorkplace}}, nil, nil))
	seedLeaseAt(t, s.conn, "vtx.leaseapp.mine", res, staffWorkplace)

	rec := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.mine", cookieFor(other))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (worksAt with no role at all is not front-desk staff); body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestOperator_ReachesTheFrontDesk: the front desk gates on being staff, and
// an operator holds no workplace, so without the explicit exemption root would
// be locked out of the surface it is meant to be able to inspect.
func TestOperator_ReachesTheFrontDesk(t *testing.T) {
	const root = "FFFFFFFFFFFFFFFFFFFF"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{}, map[string]bool{root: true}))

	putJSON(t, s.conn, frontdesk.BookingsBucket, "vtx.leaseapp.theirs", map[string]any{
		"bookingKey": "vtx.booking.x", "leaseAppKey": "vtx.leaseapp.theirs", "sessionName": "Vinyasa Flow",
	})

	rec := sessionGET(s, s.handleFrontDeskBookings, "/api/frontdesk-bookings", cookieFor(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an operator reaches the front desk); body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Bookings []bookingRow `json:"bookings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Reaching the surface is not the same as being served its rows: without
	// this the test would pass against an operator handed an empty grid.
	if len(body.Bookings) != 1 {
		t.Fatalf("operator's front-desk grid = %+v, want the row at a building they do not work at", body.Bookings)
	}
}

// TestStaffWhoAlsoLives_SeesTheirOwnLeaseToo: one person, two hats. A café
// staffer who works at one building and RENTS at another must still see their
// own house tab — the write side says the ownership probe and the workplace
// guard are complementary, not alternatives (require_workplace's own comment,
// cafe-domain's ddls.go), and the read side has to agree. Resolving only the
// staff half would take a staffer's own lease away from them the day they took
// a job across town.
func TestStaffWhoAlsoLives_SeesTheirOwnLeaseToo(t *testing.T) {
	const dana, neighbour = "DDDDDDDDDDDDDDDDDDDD", "BBBBBBBBBBBBBBBBBBBB"
	s, cookieFor := devSessionServer(t, fakeGatewayActorWorkplaces(t,
		map[string][]string{dana: {staffWorkplace}}, nil))
	// Dana's own lease is at the building she does NOT work at.
	seedLeaseAt(t, s.conn, "vtx.leaseapp.danahome", dana, otherWorkplace)
	// A lease at her workplace, held by somebody else — the staff half.
	seedLeaseAt(t, s.conn, "vtx.leaseapp.atwork", neighbour, staffWorkplace)
	// And a lease that is neither: not hers, not at her workplace.
	seedLeaseAt(t, s.conn, "vtx.leaseapp.stranger", neighbour, otherWorkplace)

	own := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.danahome", cookieFor(dana))
	if own.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a staffer must still read their OWN lease's ledger; body=%s",
			own.Code, own.Body.String())
	}
	atWork := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.atwork", cookieFor(dana))
	if atWork.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a lease at her workplace; body=%s", atWork.Code, atWork.Body.String())
	}
	// The union must not become "everything": a lease that is neither hers nor
	// at her workplace is still refused.
	stranger := sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey=vtx.leaseapp.stranger", cookieFor(dana))
	if stranger.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — the union of two hats is not every lease; body=%s",
			stranger.Code, stranger.Body.String())
	}

	rec := sessionGET(s, s.handleLeases, "/api/leases", cookieFor(dana))
	var body struct {
		Leases []leaseRow `json:"leases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leases) != 2 {
		t.Fatalf("two-hat caller's leases = %+v, want exactly her own and her workplace's", body.Leases)
	}
}

func TestHandleFrontDeskVisits_Staff_SeesOnlyCoveredLeases(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	for _, lease := range []string{"vtx.leaseapp.mine", "vtx.leaseapp.theirs"} {
		putJSON(t, s.conn, frontdesk.VisitsBucket, lease, map[string]any{
			"appointmentKey": "vtx.appointment." + lease, "leaseAppKey": lease,
			"startsAt": "2026-07-27T09:00:00Z", "endsAt": "2026-07-27T09:30:00Z",
		})
	}

	rec := sessionGET(s, s.handleFrontDeskVisits, "/api/frontdesk-visits", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Visits []visitRow `json:"visits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Visits) != 1 || body.Visits[0].LeaseAppKey != "vtx.leaseapp.mine" {
		t.Fatalf("front-desk visits = %+v, want only the lease at their own building", body.Visits)
	}
}

// TestFrontDesk_MissingWorkplacesBucket_502 pins the deliberate asymmetry in
// the front desk's best-effort posture. A missing FRONT-DESK bucket is
// tolerated — that package is an optional cross-vertical join, so the grid
// renders without its badges. A missing WORKPLACES bucket is not: it is the
// confinement source, and answering an unconfined-looking empty grid would
// read as "nobody is here today" while actually meaning "this app cannot tell
// who you may see." It fails loudly instead.
func TestFrontDesk_MissingWorkplacesBucket_502(t *testing.T) {
	s, cookieFor, staff := staffAtOneBuilding(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := s.conn.JetStream().DeleteKeyValue(ctx, cafedomain.LeaseWorkplacesBucket); err != nil {
		t.Fatalf("delete %s: %v", cafedomain.LeaseWorkplacesBucket, err)
	}

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
	}{
		{"bookings", s.handleFrontDeskBookings, "/api/frontdesk-bookings"},
		{"lease-details", s.handleFrontDeskLeaseDetails, "/api/frontdesk-lease-details"},
		{"visits", s.handleFrontDeskVisits, "/api/frontdesk-visits"},
		{"tabs", s.handleTabs, "/api/tabs"},
		{"leases", s.handleLeases, "/api/leases"},
	} {
		rec := sessionGET(s, tc.h, tc.path, cookieFor(staff))
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502 — no confinement source must never read as an answer; body=%s",
				tc.name, rec.Code, rec.Body.String())
		}
	}
}
