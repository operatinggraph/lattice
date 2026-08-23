package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

const testTimeout = 5 * time.Second

// The subjects every test below signs in as. Bare 20-char NanoIDs from the
// limited alphabet (internal/substrate/nanoid.go).
const (
	memberA   = "aaaaaaaaaaaaaaaaaaaa"
	memberB   = "bbbbbbbbbbbbbbbbbbbb"
	staffSubj = "cccccccccccccccccccc"
	// staffWorkplace is the one location staffSubj `worksAt`; a session covered
	// by it is inside that staffer's reach and one covered by otherWorkplace is
	// not. The pair is what makes the workplace boundary discriminating rather
	// than merely present.
	staffWorkplace = "vtx.building.A9jnKK2bGwZNrfHHkLme"
	otherWorkplace = "vtx.building.T4pQmZbNxKrWvL8dHcyR"
	instrSubj      = "dddddddddddddddddddd"
	doctorSubj     = "eeeeeeeeeeeeeeeeeeee"
	// rootSubj holds the primordial operator role and NO workplace; twoHatSubj
	// works at both buildings.
	rootSubj   = "ffffffffffffffffffff"
	twoHatSubj = "gggggggggggggggggggg"
	// instrStaffSubj holds BOTH the instructor binding AND a workplace — the
	// roster's own union case: their own led class anywhere, plus every class
	// at the building they work.
	instrStaffSubj = "hhhhhhhhhhhhhhhhhhhh"
)

// The instructor entity instrSubj is identifiedBy-bound to, and the session
// it leads versus one it does not.
const (
	instructorKeyFixture = "vtx.instructor.hRk2mTvQxbYpLnW4dCgf"
	ledSessionKey        = "vtx.session.mQ7bXpKrLvTnZs2WdHyu"
	otherSessionKey      = "vtx.session.wY4nGtBcRkMpXzL8vJqd"
)

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes. Mirrors
// cmd/cafe-app/readauth_test.go's own TestMain.
func TestMain(m *testing.M) {
	os.Setenv(envPrefix+"_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv(envPrefix+"_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestConn spins up an embedded JetStream server carrying every bucket
// wellness-app's read handlers touch, so the handler-level tests below drive
// the REAL read path — KVListKeys/KVGet — rather than the pure computeXxx
// seam (mirrors cmd/cafe-app/authz_test.go).
func newTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	ns := natsfixture.StartServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "wellness-app-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	for _, bucket := range []string{
		weaverTargetsBucket,
		wellnessdomain.WellnessStudiosBucket,
		wellnessdomain.WellnessSessionsBucket,
		wellnessdomain.WellnessBookingsBucket,
		wellnessdomain.WellnessInstructorsBucket,
		wellnessdomain.WellnessMembersBucket,
		wellnessledger.LedgerHistoryBucket,
		wellnessledger.MemberAccountsBucket,
	} {
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

// fakeGatewayActor stands in for the Gateway's /v1/actor door that
// resolveSubjectHats calls, answering from the token's own subject.
func fakeGatewayActor(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/actor", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var claims jwt.RegisteredClaims
		if _, _, err := jwt.NewParser().ParseUnverified(tok, &claims); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Every anchor carries the key of the thing it points at. The key
		// matters: identityAnchors also emits a keyless {relation: "..."}
		// entry for an identity with no such binding at all, so a fixture
		// that omitted it would be shaped like the degenerate entry and
		// could not tell the two apart.
		anchors := []appsession.ActorAnchor{}
		switch claims.Subject {
		case staffSubj:
			anchors = append(anchors, appsession.ActorAnchor{
				Relation: "worksAt",
				Key:      staffWorkplace,
				Name:     "Riverside Building",
			})
		case instrSubj:
			anchors = append(anchors, appsession.ActorAnchor{
				Relation: "identifiedBy",
				Key:      instructorKeyFixture,
			})
		case doctorSubj:
			// A clinic provider binding: the same relation, a different
			// entity TYPE. It must not confer the wellness instructor hat.
			anchors = append(anchors, appsession.ActorAnchor{
				Relation: "identifiedBy",
				Key:      "vtx.provider.jT3mWqZbNxKrPvL7dHcy",
			})
		case twoHatSubj:
			anchors = append(anchors,
				appsession.ActorAnchor{Relation: "worksAt", Key: staffWorkplace},
				appsession.ActorAnchor{Relation: "worksAt", Key: otherWorkplace},
			)
		case instrStaffSubj:
			anchors = append(anchors,
				appsession.ActorAnchor{Relation: "worksAt", Key: staffWorkplace},
				appsession.ActorAnchor{Relation: "identifiedBy", Key: instructorKeyFixture},
			)
		case memberB:
			// The degenerate unmatched-OPTIONAL-MATCH shape: a relation with
			// no key. It must confer nothing.
			anchors = append(anchors,
				appsession.ActorAnchor{Relation: "worksAt"},
				appsession.ActorAnchor{Relation: "identifiedBy"},
			)
		}
		// The real Gateway reports roles as VERTEX KEYS, never canonical
		// names (internal/gateway/whoami.go forwards what rolesanchors
		// resolves), so the fixture has to speak keys — a fixture saying
		// "operator" would let a name comparison pass here and match nothing
		// against a live Gateway.
		roles := []string{"vtx.role.9vGfETkjxLSNuZzb9vGf"}
		if claims.Subject == rootSubj {
			roles = append(roles, bootstrap.RoleOperatorKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actorId":         "vtx.identity." + claims.Subject,
			"resolvedActorId": "vtx.identity." + claims.Subject,
			"roles":           roles,
			"anchors":         anchors,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// devSessionServer builds a server whose session surface is the real
// appsession kit in the demo posture (the shared dev key) — carrying the
// SHIPPED publicReadPaths exemption list, so the public-read tests assert the
// real value rather than a copy — over a real embedded JetStream server.
// Returns the helper that mints a session cookie for a bare identity id.
func devSessionServer(t *testing.T) (*server, func(subject string) *http.Cookie) {
	t.Helper()
	// The read boundary compares a role entry against the primordial operator
	// role KEY, which main() resolves from the bootstrap ids at boot. Without
	// them that key is empty and a fixture minting an empty role would prove
	// nothing.
	testutil.EnsurePrimordials(t)
	if bootstrap.RoleOperatorKey == "" {
		t.Fatal("primordial ids loaded but the operator role key is empty")
	}
	gatewayURL := fakeGatewayActor(t)
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
		AppName:          appName,
		EnvPrefix:        envPrefix,
		Logger:           discardLogger(),
		GatewayURL:       gatewayURL,
		Signer:           signer,
		Authn:            authn,
		RefreshAuthn:     refreshAuthn,
		Loopback:         true,
		LoginPage:        []byte("<html>login</html>"),
		ExtraExemptPaths: publicReadPaths,
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

// muxGET drives one GET through the app's WHOLE route table — the only way to
// exercise which paths the exemption list actually opens.
func muxGET(s *server, path string, c *http.Cookie) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// noLocations seeds a session no location covers. It is an EMPTY slice, not a
// nil one: seedSession reads nil as "say nothing about topology" and defaults
// to the staffer's own building, so the unwired case has to be stated.
var noLocations = []string{}

// seedSession seeds one wellnessSessions row, led by instructorKey when
// non-empty and covered by coveringLocations — defaulting to the one building
// staffSubj works at, so a caller that says nothing about topology gets the
// session the staff hat can legitimately reach. A session somewhere else, or
// nowhere at all, is seeded by naming its locations explicitly.
func seedSession(t *testing.T, conn *substrate.Conn, sessionKey, name, instructorKey string, coveringLocations ...string) {
	t.Helper()
	if coveringLocations == nil {
		coveringLocations = []string{staffWorkplace}
	}
	row := map[string]any{
		"sessionKey":        sessionKey,
		"name":              name,
		"startsAt":          "2026-08-01T10:00:00Z",
		"endsAt":            "2026-08-01T11:00:00Z",
		"capacity":          12.0,
		"studioKey":         "vtx.studio.kR8mPqZnXvBtL3wHdYcj",
		"studioName":        "Riverside Movement Studio",
		"coveringLocations": coveringLocations,
	}
	if instructorKey != "" {
		row["instructorKey"] = instructorKey
		row["instructorName"] = "Sam Okafor"
	}
	putJSON(t, conn, wellnessdomain.WellnessSessionsBucket, sessionKey, row)
}

// seedBooking seeds one wellnessBookings row for bookerSubject (a bare id).
func seedBooking(t *testing.T, conn *substrate.Conn, bookingKey, sessionKey, bookerSubject string) {
	t.Helper()
	putJSON(t, conn, wellnessdomain.WellnessBookingsBucket, bookingKey, map[string]any{
		"bookingKey":  bookingKey,
		"status":      "booked",
		"rate":        "standard",
		"sessionKey":  sessionKey,
		"sessionName": "Vinyasa Flow",
		"startsAt":    "2026-08-01T10:00:00Z",
		"endsAt":      "2026-08-01T11:00:00Z",
		"bookerKey":   "vtx.identity." + bookerSubject,
	})
}

func decodeBookings(t *testing.T, rec *httptest.ResponseRecorder) []bookingRow {
	t.Helper()
	var body struct {
		Bookings []bookingRow `json:"bookings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode bookings: %v (body=%s)", err, rec.Body.String())
	}
	return body.Bookings
}

// ---- the public-read tier (§7.3: "schedule stays public-read") ----

func TestPublicReads_AnswerWithNoSession(t *testing.T) {
	s, _ := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")
	putJSON(t, s.conn, wellnessdomain.WellnessStudiosBucket, "vtx.studio.kR8mPqZnXvBtL3wHdYcj", map[string]any{
		"studioKey": "vtx.studio.kR8mPqZnXvBtL3wHdYcj",
		"name":      "Riverside Movement Studio",
	})

	for _, path := range publicReadPaths {
		rec := muxGET(s, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s with no session = %d, want 200 (the class schedule is public-read)", path, rec.Code)
		}
	}
}

func TestPerUserReads_RefuseWithNoSession(t *testing.T) {
	s, _ := devSessionServer(t)
	for _, path := range []string{"/api/bookings", "/api/my-residency", "/api/ledger"} {
		rec := muxGET(s, path, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no session = %d, want 401", path, rec.Code)
		}
	}
}

// ---- /api/bookings: My Classes is the caller's own, and nobody else's ----

func TestHandleBookings_MemberSeesOnlyOwnBookings(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", ledSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings, "/api/bookings", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rows := decodeBookings(t, rec)
	if len(rows) != 1 {
		t.Fatalf("got %d bookings, want 1 (only memberA's own)", len(rows))
	}
	if rows[0].BookerKey != "vtx.identity."+memberA {
		t.Errorf("bookerKey = %q, want memberA's own", rows[0].BookerKey)
	}
}

// A member who has booked nothing must get an empty list, not the house. This
// is the positive control that proves the filter above is scoping rather than
// the fixture merely being small.
func TestHandleBookings_MemberWithNoBookingsSeesNone(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings", cookieFor(memberB))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 0 {
		t.Fatalf("got %d bookings for a member who booked nothing, want 0", len(rows))
	}
}

// ---- /api/bookings?sessionKey=: the roster is a staff / own-class surface ----

func TestHandleBookings_Roster_MemberForbidden(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", instructorKeyFixture)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(memberA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (a plain member may not read a roster); body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBookings_Roster_StaffSeesTheirWorkplace(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", instructorKeyFixture)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", ledSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 2 {
		t.Fatalf("staff read %d roster rows, want 2 (every seat, not just their own)", len(rows))
	}
}

// The discriminating pair for the staff hat, mirroring the instructor one
// above: the SAME staffer, the same call, differing only in whether the
// session sits at a location they work at. Without the workplace term this
// second call returned 200 and the whole roster.
func TestHandleBookings_Roster_StaffDeniedOutsideTheirWorkplace(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")
	seedSession(t, s.conn, otherSessionKey, "Class at another building", "", otherWorkplace)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", otherSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("own workplace: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec = sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+otherSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another building: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// A staffer wired to the BUILDING reads a session in a room inside it: the
// lens projects the whole containment chain, so the depth-0 room and its
// ancestors all count — the read-side of worksAt_covers walking upward.
func TestHandleBookings_Roster_StaffCoveredByAContainingLocation(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "",
		"vtx.location.rM3kQpZbNxTvW7dHcyLn", staffWorkplace)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a room inside the staffer's building; body=%s", rec.Code, rec.Body.String())
	}
}

// A session whose studio sits nowhere is covered by nobody. Staff must not
// read it: an unwired topology denies rather than falling open, the same
// answer require_workplace gives an empty location list.
func TestHandleBookings_Roster_UnlocatedSessionDeniesStaff(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Class nowhere", "", noLocations...)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a session no location covers; body=%s", rec.Code, rec.Body.String())
	}
}

// The discriminating pair for the instructor hat: the SAME actor, the same
// call, differing only in which session is named.
func TestHandleBookings_Roster_InstructorOwnClassOnly(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Evening Flow with Sam", instructorKeyFixture)
	seedSession(t, s.conn, otherSessionKey, "Somebody else's class", "")
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", otherSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(instrSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("own class: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 1 {
		t.Fatalf("instructor read %d rows of their own class's roster, want 1", len(rows))
	}

	rec = sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+otherSessionKey, cookieFor(instrSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("another instructor's class: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// An unprojected session cannot establish that anyone leads it, so the roster
// read fails closed rather than falling through to "no ledBy ⇒ allowed".
func TestHandleBookings_Roster_UnknownSessionRefusedForInstructor(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+otherSessionKey, cookieFor(instrSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a session no lens projects", rec.Code)
	}
}

// ---- /api/my-residency ----

func TestHandleMyResidency_ScopedToCaller(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	putJSON(t, s.conn, weaverTargetsBucket, leaseApplicationKeyPrefix+"vtx.leaseapp.aaa", map[string]any{
		"entityKey":        "vtx.leaseapp.aaa",
		"applicant":        "vtx.identity." + memberA,
		"landlordApproved": true,
	})
	putJSON(t, s.conn, weaverTargetsBucket, leaseApplicationKeyPrefix+"vtx.leaseapp.bbb", map[string]any{
		"entityKey":        "vtx.leaseapp.bbb",
		"applicant":        "vtx.identity." + memberB,
		"landlordApproved": true,
	})

	rec := sessionGET(s, s.handleMyResidency, "/api/my-residency", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Leases []residencyRow `json:"leases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Leases) != 1 || body.Leases[0].LeaseAppKey != "vtx.leaseapp.aaa" {
		t.Fatalf("got %+v, want only memberA's own lease", body.Leases)
	}
}

// ---- resolveSubjectHats: which anchors confer which hat ----

func TestResolveSubjectHats(t *testing.T) {
	s, cookieFor := devSessionServer(t)

	cases := []struct {
		name          string
		subject       string
		wantStaff     bool
		wantInstrutor string
	}{
		{"plain member holds neither hat", memberA, false, ""},
		{"a worksAt anchor confers staff", staffSubj, true, ""},
		{"an identifiedBy instructor binding confers the instructor hat", instrSubj, false, instructorKeyFixture},
		// The same relation bound to a clinic PROVIDER must not confer the
		// wellness instructor hat — the vertex type is what distinguishes
		// them on a multi-hat human (persona-worlds-design.md §3.4).
		{"an identifiedBy provider binding confers no instructor hat", doctorSubj, false, ""},
		// identityAnchors emits a keyless entry for every relation an
		// identity does NOT have; testing the relation alone would grant
		// both hats to everyone.
		{"keyless degenerate anchors confer nothing", memberB, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/bookings", nil)
			r.AddCookie(cookieFor(tc.subject))
			var got subjectHats
			var err error
			s.session.RequireSession(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				got, err = s.resolveSubjectHats(req)
			})).ServeHTTP(httptest.NewRecorder(), r)
			if err != nil {
				t.Fatalf("resolveSubjectHats: %v", err)
			}
			if got.isStaff() != tc.wantStaff {
				t.Errorf("isStaff() = %v, want %v", got.isStaff(), tc.wantStaff)
			}
			if got.instructorKey != tc.wantInstrutor {
				t.Errorf("instructorKey = %q, want %q", got.instructorKey, tc.wantInstrutor)
			}
			if got.identityKey() != "vtx.identity."+tc.subject {
				t.Errorf("identityKey() = %q, want the caller's own vertex key", got.identityKey())
			}
		})
	}
}

// A session the Gateway cannot answer for must fail CLOSED, never defaulting
// to the unfiltered staff answer.
func TestResolveSubjectHats_GatewayUnreachable_FailsClosed(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	s.gatewayURL = "http://127.0.0.1:1" // nothing listens here

	r := httptest.NewRequest(http.MethodGet, "/api/bookings", nil)
	r.AddCookie(cookieFor(staffSubj))
	var got subjectHats
	var err error
	s.session.RequireSession(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		got, err = s.resolveSubjectHats(req)
	})).ServeHTTP(httptest.NewRecorder(), r)
	if err == nil {
		t.Fatalf("resolveSubjectHats returned %+v, want an error when the Gateway is unreachable", got)
	}
	if got.isStaff() {
		t.Errorf("isStaff() = true on a failed resolve; the hat must never be granted by default")
	}
}

// The mirror of the above: a genuinely absent session IS a 401, so the FE's
// sign-in bounce still fires when it should.
func TestHandleBookings_NoSession_Is401(t *testing.T) {
	s, _ := devSessionServer(t)
	rec := sessionGET(s, s.handleBookings, "/api/bookings", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a request with no session at all", rec.Code)
	}
}

// The vector this closes: /api/bookings?bookerKey=<anyone> must not hand
// any caller any resident's whole class history. The parameter is not
// read, and this pins that — a future author reintroducing one has a
// failing test rather than a silent regression.
func TestHandleBookings_BookerKeyParamIsIgnored(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", ledSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings,
		"/api/bookings?bookerKey=vtx.identity."+memberB, cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rows := decodeBookings(t, rec)
	if len(rows) != 1 || rows[0].BookerKey != "vtx.identity."+memberA {
		t.Fatalf("naming another member in ?bookerKey= returned %+v; the session's own subject must decide", rows)
	}
}

// Staff read any roster, including a class nobody leads — the staff branch
// must answer BEFORE the ledBy check, not fall through it.
func TestHandleBookings_Roster_StaffReadsUnledSession(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, otherSessionKey, "Unled Class", "")
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", otherSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+otherSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (staff read any roster, led or not); body=%s", rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 1 {
		t.Fatalf("staff read %d rows of an unled session's roster, want 1", len(rows))
	}
}

// My Classes must keep serving through a Gateway outage: the answer needs
// only the session's own subject, so it must not consult /v1/actor at all.
// Only the roster branch depends on the hats, and only it may fail closed.
func TestHandleBookings_OwnClassesSurviveGatewayOutage(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	s.gatewayURL = "http://127.0.0.1:1" // nothing listens here

	rec := sessionGET(s, s.handleBookings, "/api/bookings", cookieFor(memberA))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — own bookings need no Gateway call; body=%s", rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 1 {
		t.Fatalf("got %d own bookings during a Gateway outage, want 1", len(rows))
	}
}

// The complement: a roster read during the same outage still fails closed.
func TestHandleBookings_RosterFailsClosedDuringGatewayOutage(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Evening Flow", instructorKeyFixture)
	s.gatewayURL = "http://127.0.0.1:1"

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(instrSubj))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (hats unresolvable ⇒ no roster, and not a 403 accusation)", rec.Code)
	}
	// The detail can name internal hosts/URLs; it must not reach the client.
	if strings.Contains(rec.Body.String(), "127.0.0.1:1") {
		t.Errorf("response leaks the upstream address: %s", rec.Body.String())
	}
}

// ---- /api/instructors ----

func TestHandleInstructors_RequiresASession(t *testing.T) {
	s, _ := devSessionServer(t)
	rec := muxGET(s, "/api/instructors", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — the instructor directory is not part of the public schedule", rec.Code)
	}
}

func TestHandleInstructors_ListsNamedInstructors(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	putJSON(t, s.conn, wellnessdomain.WellnessInstructorsBucket, instructorKeyFixture, map[string]any{
		"instructorKey": instructorKeyFixture,
		"displayName":   "Sam Okafor",
		"studioKey":     "vtx.studio.kR8mPqZnXvBtL3wHdYcj",
	})

	rec := sessionGET(s, s.handleInstructors, "/api/instructors", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Instructors []instructorRow `json:"instructors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Instructors) != 1 || body.Instructors[0].DisplayName != "Sam Okafor" {
		t.Fatalf("got %+v, want the one named instructor", body.Instructors)
	}
}

// publicReadPaths is the whole public surface; an empty slice would make
// TestPublicReads_AnswerWithNoSession pass vacuously.
func TestPublicReadPaths_IsExactlyTheSchedule(t *testing.T) {
	want := map[string]bool{"/api/studios": true, "/api/sessions": true}
	if len(publicReadPaths) != len(want) {
		t.Fatalf("publicReadPaths = %v, want exactly the two schedule reads", publicReadPaths)
	}
	for _, p := range publicReadPaths {
		if !want[p] {
			t.Errorf("%s is exempt from the session gate; only the class schedule may be", p)
		}
	}
}

// An operator reads any roster, workplace or not — the read-side half of the
// exemption require_workplace gives root on the write side. Without it the
// break-glass admin that can schedule a class anywhere could not see who is in
// it.
func TestHandleBookings_Roster_OperatorReadsAnyWorkplace(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, otherSessionKey, "Class at another building", "", otherWorkplace)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", otherSessionKey, memberB)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+otherSessionKey, cookieFor(rootSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an operator holds no workplace and still reads any roster); body=%s",
			rec.Code, rec.Body.String())
	}
	if rows := decodeBookings(t, rec); len(rows) != 1 {
		t.Fatalf("operator read %d roster rows, want 1", len(rows))
	}
}

// A staffer wired to two buildings reads both, and still nothing outside them.
func TestHandleBookings_Roster_MultipleWorkplaces(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Class at home", "")
	seedSession(t, s.conn, otherSessionKey, "Class at the other building", "", otherWorkplace)
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)
	seedBooking(t, s.conn, "vtx.booking.bT5qWnZxKvMpL2wHdGcy", otherSessionKey, memberB)

	for _, key := range []string{ledSessionKey, otherSessionKey} {
		rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+key, cookieFor(twoHatSubj))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 for a staffer working at both; body=%s", key, rec.Code, rec.Body.String())
		}
	}
	// The same two-workplace staffer is still refused a third building.
	third := "vtx.session.pK9rTmZbNxWvL4dHcyQs"
	putJSON(t, s.conn, wellnessdomain.WellnessSessionsBucket, third, map[string]any{
		"sessionKey":        third,
		"name":              "Class at a building they do not work at",
		"coveringLocations": []string{"vtx.building.Z7mKqPbNxTvW3dHcyRLn"},
	})
	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+third, cookieFor(twoHatSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("third building: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// A row projected by an OLDER version of the lens carries no coveringLocations
// key at all. That decodes to a nil slice and must DENY — the stale-projection
// case is the one where a fail-open would hand every roster to any staffer.
func TestHandleBookings_Roster_RowWithoutCoveringLocationsDenies(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	putJSON(t, s.conn, wellnessdomain.WellnessSessionsBucket, ledSessionKey, map[string]any{
		"sessionKey": ledSessionKey,
		"name":       "Projected before the workplace term existed",
		"startsAt":   "2026-08-01T10:00:00Z",
		"endsAt":     "2026-08-01T11:00:00Z",
		"capacity":   12.0,
		"studioKey":  "vtx.studio.kR8mPqZnXvBtL3wHdYcj",
	})
	seedBooking(t, s.conn, "vtx.booking.aR3nKpXvZmBtL7wHdYcj", ledSessionKey, memberA)

	rec := sessionGET(s, s.handleBookings, "/api/bookings?sessionKey="+ledSessionKey, cookieFor(staffSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a row carrying no covering set; body=%s", rec.Code, rec.Body.String())
	}
}

// ---- /api/members: the front desk's picker, scoped to where they work ----

// seedMember seeds one wellnessMembers row for bookerSubject (a bare id),
// covered by coveringLocations — defaulting to the one building staffSubj
// works at, so a caller that says nothing about topology gets a member the
// staff hat can legitimately reach. Mirrors seedSession's defaulting.
func seedMember(t *testing.T, conn *substrate.Conn, leaseAppKey, bookerSubject string, coveringLocations ...string) {
	t.Helper()
	seedMemberDecided(t, conn, leaseAppKey, bookerSubject, "", coveringLocations...)
}

// seedMemberDecided is seedMember with the landlord's verdict stated. An empty
// decision is the undecided application the lens projects as null.
func seedMemberDecided(t *testing.T, conn *substrate.Conn, leaseAppKey, bookerSubject, decision string, coveringLocations ...string) {
	t.Helper()
	if coveringLocations == nil {
		coveringLocations = []string{staffWorkplace}
	}
	row := map[string]any{
		"leaseAppKey":       leaseAppKey,
		"bookerKey":         "vtx.identity." + bookerSubject,
		"coveringLocations": coveringLocations,
	}
	if decision != "" {
		row["landlordDecision"] = decision
	}
	putJSON(t, conn, wellnessdomain.WellnessMembersBucket, leaseAppKey, row)
}

const (
	leaseHere      = "vtx.leaseapp.pK4mRtZbXvNqL7wHdYcj"
	leaseElsewhere = "vtx.leaseapp.zT8nWpKrBvMqX3LdHcyg"
)

func decodeMembers(t *testing.T, rec *httptest.ResponseRecorder) []memberRow {
	t.Helper()
	var body struct {
		Members []memberRow `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode members: %v (body=%s)", err, rec.Body.String())
	}
	return body.Members
}

// The discriminating pair for the whole surface: the SAME staffer, the same
// call, two members differing only in which building their lease sits at.
// Without the workplace term this returned both.
func TestHandleMembers_StaffSeesOnlyTheirWorkplace(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)
	seedMember(t, s.conn, leaseElsewhere, memberB, otherWorkplace)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	members := decodeMembers(t, rec)
	if len(members) != 1 {
		t.Fatalf("got %d members, want 1 (the one at this staffer's building); %+v", len(members), members)
	}
	if members[0].BookerKey != "vtx.identity."+memberA || members[0].LeaseAppKey != leaseHere {
		t.Fatalf("got %+v, want memberA on %s", members[0], leaseHere)
	}
}

// A staffer wired to the BUILDING reaches a member whose lease is on a unit
// inside it: the lens projects the whole containment chain, so the depth-0
// unit and its ancestors all count.
func TestHandleMembers_StaffCoveredByAContainingLocation(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA, "vtx.unit.qR7mKpXvZnBtL4wHdYcj", staffWorkplace)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 1 {
		t.Fatalf("got %d members, want 1 — a containing location covers the unit below it; %+v", len(got), got)
	}
}

// A lease whose unit is unwired is covered by nobody. It must not be offered:
// an empty covering set denies rather than falling open, the same answer
// require_workplace gives an empty location list.
func TestHandleMembers_UnlocatedLeaseOfferedToNobody(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA, noLocations...)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 0 {
		t.Fatalf("got %d members, want 0 — an unwired lease covers nobody; %+v", len(got), got)
	}
}

// A row projected by an OLDER version of the lens carries no coveringLocations
// key at all. That decodes to a nil slice and must DENY — the stale-projection
// case is where a fail-open would publish the whole directory.
func TestHandleMembers_RowWithoutCoveringLocationsDenies(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	putJSON(t, s.conn, wellnessdomain.WellnessMembersBucket, leaseHere, map[string]any{
		"leaseAppKey": leaseHere,
		"bookerKey":   "vtx.identity." + memberA,
	})

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 0 {
		t.Fatalf("got %d members, want 0 for a row carrying no covering set; %+v", len(got), got)
	}
}

// A directory of who else lives here is not a member's to read. This is the
// boundary the deleted /api/residents lacked.
func TestHandleMembers_MemberForbidden(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(memberA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a member reads no directory, not even one they appear in; body=%s",
			rec.Code, rec.Body.String())
	}
}

// Leading a class confers no directory: an instructor holds no CreateBooking
// grant, so the picker that feeds it is not theirs either.
func TestHandleMembers_InstructorForbidden(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(instrSubj))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// The operator holds no workplace at all, and reads every member — the
// break-glass exemption require_workplace gives root on the write side.
func TestHandleMembers_OperatorSeesEveryMember(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)
	seedMember(t, s.conn, leaseElsewhere, memberB, otherWorkplace)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(rootSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 2 {
		t.Fatalf("got %d members, want 2 — root is exempt from confinement, not from the read; %+v", len(got), got)
	}
}

// A staffer at two buildings reads the members of both — the union, not the
// first match.
func TestHandleMembers_TwoHatStafferSeesBothBuildings(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)
	seedMember(t, s.conn, leaseElsewhere, memberB, otherWorkplace)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(twoHatSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 2 {
		t.Fatalf("got %d members, want 2 — both workplaces count; %+v", len(got), got)
	}
}

// The picker is a per-user read, so it needs a session like every other one.
func TestHandleMembers_RefusesWithNoSession(t *testing.T) {
	s, _ := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)

	rec := muxGET(s, "/api/members", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — /api/members must not be on the public-read exemption list; body=%s",
			rec.Code, rec.Body.String())
	}
}

// The covering set is the server's confinement input, not the client's. It must
// not travel: publishing it would hand a staffer the building topology that
// decided the answer, and it is what the picker is deliberately silent about.
// Asserted on the RAW body, since decodeMembers would silently drop the field
// and let a regression pass green.
func TestHandleMembers_WithholdsCoveringLocationsFromTheClient(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMember(t, s.conn, leaseHere, memberA)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "coveringLocations") || strings.Contains(body, staffWorkplace) {
		t.Fatalf("the response carries the confinement input; body=%s", body)
	}
	// The landlord's verdict is the reader's filter, not the picker's business:
	// whether a neighbour's lease was approved is nothing a front desk needs to
	// book them into a yoga class.
	if strings.Contains(body, "landlordDecision") {
		t.Fatalf("the response carries the landlord's verdict; body=%s", body)
	}
}

// The discriminating pair for the refusal drop: two members at the SAME
// building, differing only in the landlord's verdict. A refused applicant keeps
// a live lease and a live applicationFor link, so without this the front desk
// would be handed the identity of somebody that building turned down and told
// they were a member.
func TestHandleMembers_DeclinedApplicantIsNotOffered(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMemberDecided(t, s.conn, leaseHere, memberA, "declined")
	seedMemberDecided(t, s.conn, leaseElsewhere, memberB, "approved")

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	members := decodeMembers(t, rec)
	if len(members) != 1 || members[0].BookerKey != "vtx.identity."+memberB {
		t.Fatalf("got %+v, want only the approved member — a refusal is not a membership", members)
	}
}

// An application still awaiting a landlord belongs to somebody living in the
// building, and is exactly who the front desk books in. Only a REFUSAL
// disqualifies; the resident RATE is CreateBooking's separate, stricter
// question, answered from the lease's own .tenancy.
func TestHandleMembers_UndecidedApplicationStaysInTheDirectory(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMemberDecided(t, s.conn, leaseHere, memberA, "")

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 1 {
		t.Fatalf("got %d members, want 1 — an undecided lease is not a refusal; %+v", len(got), got)
	}
}

// The operator exemption is from CONFINEMENT, not from the refusal drop: root
// sees every building, not people it was never true to call members.
func TestHandleMembers_OperatorAlsoSeesNoDeclinedApplicant(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedMemberDecided(t, s.conn, leaseHere, memberA, "declined")
	seedMemberDecided(t, s.conn, leaseElsewhere, memberB, "", otherWorkplace)

	rec := sessionGET(s, s.handleMembers, "/api/members", cookieFor(rootSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeMembers(t, rec); len(got) != 1 || got[0].BookerKey != "vtx.identity."+memberB {
		t.Fatalf("got %+v, want only the undecided member at the other building", got)
	}
}

// ---- /api/roster-sessions: the roster's own picker, scoped to where a staffer works or an instructor leads ----

func decodeRosterSessions(t *testing.T, rec *httptest.ResponseRecorder) []sessionRow {
	t.Helper()
	var body struct {
		Sessions []sessionRow `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sessions: %v (body=%s)", err, rec.Body.String())
	}
	return body.Sessions
}

// The discriminating pair for the whole surface: the SAME staffer, the same
// call, two sessions differing only in which building they run at. Before
// this endpoint existed, the roster's session picker was the public,
// building-wide /api/sessions — this is exactly the leak filed to
// verticals.md ("Wellness roster picker offers classes at other buildings").
func TestHandleRosterSessions_StaffSeesOnlyTheirWorkplace(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")
	seedSession(t, s.conn, otherSessionKey, "Class at another building", "", otherWorkplace)

	rec := sessionGET(s, s.handleRosterSessions, "/api/roster-sessions", cookieFor(staffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	sessions := decodeRosterSessions(t, rec)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (the one at this staffer's building); %+v", len(sessions), sessions)
	}
	if sessions[0].SessionKey != ledSessionKey {
		t.Fatalf("got %+v, want %s", sessions[0], ledSessionKey)
	}
}

// A bound instructor holds no workplace at all, so covers() answers nothing
// for either session — but the class they lead is theirs to read regardless,
// mirroring mayReadRoster's own dual test (bookings.go), and the one they do
// not lead stays refused even though neither session is covered by any
// workplace.
func TestHandleRosterSessions_InstructorSeesOnlyTheirOwnLedSession(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Evening Flow with Sam", instructorKeyFixture, noLocations...)
	seedSession(t, s.conn, otherSessionKey, "Somebody else's class", "", noLocations...)

	rec := sessionGET(s, s.handleRosterSessions, "/api/roster-sessions", cookieFor(instrSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	sessions := decodeRosterSessions(t, rec)
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (the one this instructor leads); %+v", len(sessions), sessions)
	}
	if sessions[0].SessionKey != ledSessionKey {
		t.Fatalf("got %+v, want %s", sessions[0], ledSessionKey)
	}
}

// A human who both works a building AND leads a class elsewhere sees the
// UNION of the two answers, not just one: their own led session at a
// building they do NOT work, plus a house class at the building they DO work
// (which they do not lead), and nothing from a third building neither reaches.
func TestHandleRosterSessions_TwoHatSeesTheUnion(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Evening Flow with Sam", instructorKeyFixture, otherWorkplace)
	seedSession(t, s.conn, otherSessionKey, "House class", "", staffWorkplace)
	unreached := "vtx.session.pN6mVtXrKbLqZ9wHdYcs"
	seedSession(t, s.conn, unreached, "Nowhere this caller reaches", "", otherWorkplace)

	rec := sessionGET(s, s.handleRosterSessions, "/api/roster-sessions", cookieFor(instrStaffSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := map[string]bool{}
	for _, se := range decodeRosterSessions(t, rec) {
		got[se.SessionKey] = true
	}
	if len(got) != 2 || !got[ledSessionKey] || !got[otherSessionKey] {
		t.Fatalf("got %+v, want exactly the led session and the worked-building session", got)
	}
}

// A plain member holds neither hat. The roster is not theirs to read, same
// boundary handleMembers draws for the member directory.
func TestHandleRosterSessions_MemberForbidden(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")

	rec := sessionGET(s, s.handleRosterSessions, "/api/roster-sessions", cookieFor(memberA))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// The operator holds no workplace and leads nothing, and reads every
// session anyway — the break-glass exemption require_workplace gives root on
// the write side.
func TestHandleRosterSessions_OperatorSeesEverySession(t *testing.T) {
	s, cookieFor := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")
	seedSession(t, s.conn, otherSessionKey, "Class at another building", "", otherWorkplace)

	rec := sessionGET(s, s.handleRosterSessions, "/api/roster-sessions", cookieFor(rootSubj))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeRosterSessions(t, rec); len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 — root sees every building; %+v", len(got), got)
	}
}

// The picker is a per-user read, so it needs a session like every other one.
func TestHandleRosterSessions_RefusesWithNoSession(t *testing.T) {
	s, _ := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")

	rec := muxGET(s, "/api/roster-sessions", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — /api/roster-sessions must not be on the public-read exemption list; body=%s",
			rec.Code, rec.Body.String())
	}
}

// /api/sessions itself must stay exactly as public and building-wide as
// before: a resident browsing the schedule grid still needs every session
// everywhere, which is the whole reason the roster needed its OWN endpoint
// rather than narrowing this one.
func TestHandleSessions_StaysPublicAndUnscoped(t *testing.T) {
	s, _ := devSessionServer(t)
	seedSession(t, s.conn, ledSessionKey, "Vinyasa Flow", "")
	seedSession(t, s.conn, otherSessionKey, "Class at another building", "", otherWorkplace)

	rec := muxGET(s, "/api/sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — /api/sessions stays public-read; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []sessionRow `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode sessions: %v (body=%s)", err, rec.Body.String())
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2 — the schedule grid stays building-wide; %+v", len(body.Sessions), body.Sessions)
	}
}
