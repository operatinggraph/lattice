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
	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

const testTimeout = 5 * time.Second

// The subjects every test below signs in as. Bare 20-char NanoIDs from the
// limited alphabet (internal/substrate/nanoid.go).
const (
	memberA    = "aaaaaaaaaaaaaaaaaaaa"
	memberB    = "bbbbbbbbbbbbbbbbbbbb"
	staffSubj  = "cccccccccccccccccccc"
	instrSubj  = "dddddddddddddddddddd"
	doctorSubj = "eeeeeeeeeeeeeeeeeeee"
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
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

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
				Key:      "vtx.building.A9jnKK2bGwZNrfHHkLme",
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
		case memberB:
			// The degenerate unmatched-OPTIONAL-MATCH shape: a relation with
			// no key. It must confer nothing.
			anchors = append(anchors,
				appsession.ActorAnchor{Relation: "worksAt"},
				appsession.ActorAnchor{Relation: "identifiedBy"},
			)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actorId":         "vtx.identity." + claims.Subject,
			"resolvedActorId": "vtx.identity." + claims.Subject,
			"roles":           []string{},
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

// seedSession seeds one wellnessSessions row, led by instructorKey when
// non-empty.
func seedSession(t *testing.T, conn *substrate.Conn, sessionKey, name, instructorKey string) {
	t.Helper()
	row := map[string]any{
		"sessionKey": sessionKey,
		"name":       name,
		"startsAt":   "2026-08-01T10:00:00Z",
		"endsAt":     "2026-08-01T11:00:00Z",
		"capacity":   12.0,
		"studioKey":  "vtx.studio.kR8mPqZnXvBtL3wHdYcj",
		"studioName": "Riverside Movement Studio",
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
	for _, path := range []string{"/api/bookings", "/api/my-residency"} {
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

func TestHandleBookings_Roster_StaffSeesTheHouse(t *testing.T) {
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
			if got.isStaff != tc.wantStaff {
				t.Errorf("isStaff = %v, want %v", got.isStaff, tc.wantStaff)
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
	if got.isStaff {
		t.Errorf("isStaff = true on a failed resolve; the hat must never be granted by default")
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

// The exact vector this fire closed: /api/bookings?bookerKey=<anyone> used to
// hand any caller any resident's whole class history. The parameter is no
// longer read, and this pins that — a future author reintroducing one has a
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
