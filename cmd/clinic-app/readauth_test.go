package main

import (
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

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const testTimeout = 5 * time.Second

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes.
func TestMain(m *testing.M) {
	os.Setenv("CLINIC_APP_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv("CLINIC_APP_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
	os.Exit(m.Run())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// devSessionServer builds a server whose session surface is the real
// appsession kit in the demo posture (the shared dev key), and returns the
// helper that mints a session cookie for a bare identity id. Tests drive their
// requests through the manager's own middleware, so an absent, forged, or
// expired cookie is judged by exactly the code that guards the endpoint in
// production.
func devSessionServer(t *testing.T, mutate func(*server)) (*server, func(subject string) *http.Cookie) {
	t.Helper()
	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	signer, err := appsession.NewDevSigner(discardLogger(), envPrefix, true)
	if err != nil {
		t.Fatalf("NewDevSigner: %v", err)
	}
	authn, refreshAuthn, err := appsession.NewAuthenticators(discardLogger(), envPrefix, signer, nil)
	if err != nil {
		t.Fatalf("NewAuthenticators: %v", err)
	}
	session, err := appsession.New(appsession.Config{
		AppName:   appName,
		EnvPrefix: envPrefix,
		Logger:    discardLogger(),
		// Never dialled: these tests mint cookies directly instead of driving
		// POST /api/dev-login, whose Gateway round trip is the kit's own
		// covered surface.
		GatewayURL:   "http://gateway.invalid",
		Signer:       signer,
		Authn:        authn,
		RefreshAuthn: refreshAuthn,
		Loopback:     true,
		LoginPage:    []byte("<html>login</html>"),
	})
	if err != nil {
		t.Fatalf("appsession.New: %v", err)
	}
	s := &server{logger: discardLogger(), authn: authn, session: session, natsTimeout: testTimeout}
	if mutate != nil {
		mutate(s)
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

// noPostureServer builds a server whose session manager has no verifier at all
// — the unprovisioned deployment, where every session-gated request must fail
// closed.
func noPostureServer(t *testing.T) *server {
	t.Helper()
	session, err := appsession.New(appsession.Config{
		AppName:   appName,
		EnvPrefix: envPrefix,
		Logger:    discardLogger(),
		Loopback:  true,
		LoginPage: []byte("<html>login</html>"),
	})
	if err != nil {
		t.Fatalf("appsession.New: %v", err)
	}
	return &server{logger: discardLogger(), session: session, natsTimeout: testTimeout}
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

// TestAuthenticateRead_SessionIdentityIsTheRLSPrincipal: the identity the
// session resolved becomes the actor a protected read runs as, in both the
// bare-subject and full-key shapes the RLS call sites consume.
func TestAuthenticateRead_SessionIdentityIsTheRLSPrincipal(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	actor, err := s.authenticateRead(r.WithContext(appsession.WithSession(r.Context(), id, true)))
	if err != nil {
		t.Fatalf("authenticateRead: %v", err)
	}
	if actor.Subject != id {
		t.Errorf("subject = %q, want %q", actor.Subject, id)
	}
	if actor.ActorID != auth.IdentityKeyPrefix+id {
		t.Errorf("actorID = %q, want %s%s", actor.ActorID, auth.IdentityKeyPrefix, id)
	}
}

// TestAuthenticateRead_NoSession_Errors: no resolved identity ⇒ no principal
// to key RLS on, so the read is refused rather than running as nobody.
func TestAuthenticateRead_NoSession_Errors(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	if _, err := s.authenticateRead(httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)); err == nil {
		t.Fatal("expected an error with no session on the request")
	}
}

// TestAuthenticateRead_BlankIdentity_Errors is the defence in depth: a blank
// principal must never reach set_config('lattice.actor_id', …).
func TestAuthenticateRead_BlankIdentity_Errors(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	r := httptest.NewRequest(http.MethodGet, "/api/my-appointments", nil)
	r = r.WithContext(appsession.WithSession(r.Context(), "   ", true))
	if _, err := s.authenticateRead(r); err == nil {
		t.Fatal("expected an error for a blank session identity")
	}
}

func TestHandleMyAppointments_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleMyAppointments, "/api/my-appointments", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyAppointments_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleMyAppointments, "/api/my-appointments", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleMyAppointments_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleMyAppointments, "/api/my-appointments", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestHandleMyAppointments_ValidSession_PoolUnconfigured_502: a signed-in
// actor with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyAppointments_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleMyAppointments, "/api/my-appointments", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// TestHandleMyProviderSchedule_* mirror TestHandleMyAppointments_* — the same
// session-then-RLS boundary, just the provider-anchored sibling endpoint
// (D1.5 Increment 2).

func TestHandleMyProviderSchedule_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleMyProviderSchedule, "/api/my-schedule", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyProviderSchedule_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleMyProviderSchedule, "/api/my-schedule", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleMyProviderSchedule_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleMyProviderSchedule, "/api/my-schedule", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestHandleMyProviderSchedule_ValidSession_PoolUnconfigured_502: a signed-in
// actor with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyProviderSchedule_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleMyProviderSchedule, "/api/my-schedule", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// TestHandleMyEncounters_* mirror TestHandleMyAppointments_*/
// TestHandleMyProviderSchedule_* — the same session-then-RLS boundary, for the
// clinicEncountersRead-backed sibling endpoint. The RLS scoping itself (which
// actor gets which rows) is covered separately, against a real Postgres, by
// TestEncountersReadBoundary_RLS_Enforcement (encounters_rls_test.go); these
// run without Postgres, so they are what `go test ./cmd/clinic-app/` actually
// exercises locally.

func TestHandleMyEncounters_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleMyEncounters, "/api/my-encounters", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleMyEncounters_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleMyEncounters, "/api/my-encounters", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleMyEncounters_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleMyEncounters, "/api/my-encounters", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestHandleMyEncounters_ValidSession_PoolUnconfigured_502: a signed-in actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleMyEncounters_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleMyEncounters, "/api/my-encounters", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pool unconfigured)", rec.Code)
	}
}

// TestSessionCookieInteroperatesWithTheSharedDevKey proves the actual point of
// the shared-dev-IdP interim (real-actor-write-auth-e2e-design.md §3.2): the
// token this app's session cookie carries verifies against an independently
// built verifier that trusts nothing but the shared dev key — standing in for
// the Gateway's own trust set. One shared key, so the browser-direct FE
// (writes → Gateway, reads → app) acts as a single actor.
func TestSessionCookieInteroperatesWithTheSharedDevKey(t *testing.T) {
	_, cookieFor := devSessionServer(t, nil)

	gatewayKeys, gatewaySpecs, err := auth.LoadTrustedKeys(auth.KeySourceConfig{
		DevMode:    true,
		DevKeyPath: os.Getenv("CLINIC_APP_DEV_PUBLIC_KEY_PATH"),
	}, nil)
	if err != nil {
		t.Fatalf("LoadTrustedKeys: %v", err)
	}
	gatewayVerifier, err := auth.NewVerifier(auth.Config{Keys: gatewayKeys, KeyInfo: auth.KeyInfoFromSpecs(gatewaySpecs)})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	const sub = "Hj4kPmRtw9nbCxz5vQ2y"
	actor, err := gatewayVerifier.Verify(cookieFor(sub).Value)
	if err != nil {
		t.Fatalf("session token rejected by a Gateway-shaped verifier: %v", err)
	}
	if actor.Subject != sub {
		t.Errorf("subject = %q, want %q", actor.Subject, sub)
	}
}

// staffWorkplace is the clinic building a plain staff subject `worksAt`.
// Mirrors cmd/cafe-app's readauth_test.go fixture shape.
const staffWorkplace = "vtx.building.A9jnKK2bGwZNrfHHkLme"

// fakeGatewayActorWorkplaces stands in for the Gateway's external
// GET /v1/actor door that resolveSubjectHats calls: it decodes the bearer's
// JWT subject (unverified — a trusted test double, not a security boundary,
// standing in for the Gateway which has already verified the token) and
// reports the named `worksAt` anchors and operator role per subject. Mirrors
// cmd/cafe-app's and cmd/wellness-app's identical fixture.
//
// Every subject with at least one workplace ALSO gets the `frontOfHouse`
// role: clinic-app's front-desk roster gates on isFrontDesk (worksAt AND
// frontOfHouse, readauth.go — mirroring clinic-domain's own
// `GrantsTo: [operator, frontOfHouse]`), so every current caller of this
// helper means "front-desk staff". A worksAt-only, role-less caller is its
// own case, TestRoleLessWorksAt_IsNotFrontDesk.
func fakeGatewayActorWorkplaces(t *testing.T, workplaces map[string][]string, operators map[string]bool) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/actor", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var claims jwt.RegisteredClaims
		if _, _, err := jwt.NewParser().ParseUnverified(tok, &claims); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		anchors := []appsession.ActorAnchor{}
		for _, key := range workplaces[claims.Subject] {
			anchors = append(anchors, appsession.ActorAnchor{Relation: "worksAt", Key: key, Name: "Clinic Building"})
		}
		var roles []string
		if operators[claims.Subject] {
			roles = append(roles, bootstrap.RoleOperatorKey)
		}
		if len(workplaces[claims.Subject]) > 0 {
			roles = append(roles, frontOfHouseRoleKey())
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

// TestResolveSubjectHats_GatewayUnreachable_FailsClosed: the Gateway call
// resolveSubjectHats depends on to learn the caller's anchors is down ⇒
// refused outright, never defaulting to the unfiltered "staff" answer.
func TestResolveSubjectHats_GatewayUnreachable_FailsClosed(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = "http://127.0.0.1:1" }) // nothing listens here
	r := httptest.NewRequest(http.MethodGet, "/api/residents", nil)
	r.AddCookie(cookieFor(id))
	rec := httptest.NewRecorder()
	ran := false
	s.session.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		if _, err := s.resolveSubjectHats(r); err == nil {
			t.Error("expected resolveSubjectHats to fail closed when the Gateway is unreachable")
		}
	})).ServeHTTP(rec, r)
	if !ran {
		t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
	}
}

// TestResolveSubjectHats_WorksAtConfersStaff proves the discriminating case:
// a subject with a `worksAt` anchor is staff, one with none is not.
func TestResolveSubjectHats_WorksAtConfersStaff(t *testing.T) {
	const staff, patient = "Hj4kPmRtw9nbCxz5vQ2y", "Kx8mNqTwZ4bRvL2yDcHf"
	gwURL := fakeGatewayActorWorkplaces(t, map[string][]string{staff: {staffWorkplace}}, nil)
	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gwURL })

	check := func(subject string, wantStaff bool) {
		r := httptest.NewRequest(http.MethodGet, "/api/residents", nil)
		r.AddCookie(cookieFor(subject))
		rec := httptest.NewRecorder()
		ran := false
		s.session.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ran = true
			hats, err := s.resolveSubjectHats(r)
			if err != nil {
				t.Fatalf("resolveSubjectHats(%s): %v", subject, err)
			}
			if hats.isStaff() != wantStaff {
				t.Errorf("subject %s: isStaff() = %v, want %v", subject, hats.isStaff(), wantStaff)
			}
		})).ServeHTTP(rec, r)
		if !ran {
			t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
		}
	}
	check(staff, true)
	check(patient, false)
}

// TestResolveSubjectHats_KeylessWorksAtAnchorIsNotStaff: identityAnchors
// stamps `relation` as a literal on every collected entry, so an identity
// with no workplace still produces a {key:null, relation:"worksAt"} entry
// from the unmatched OPTIONAL MATCH. A caller carrying only that entry is a
// patient, and must not be read as staff. Mirrors cmd/cafe-app's identical
// test.
func TestResolveSubjectHats_KeylessWorksAtAnchorIsNotStaff(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/actor", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actorId":         "vtx.identity." + id,
			"resolvedActorId": "vtx.identity." + id,
			"roles":           []string{},
			"anchors":         []appsession.ActorAnchor{{Relation: "worksAt"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = srv.URL })
	r := httptest.NewRequest(http.MethodGet, "/api/residents", nil)
	r.AddCookie(cookieFor(id))
	rec := httptest.NewRecorder()
	ran := false
	s.session.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			t.Fatalf("resolveSubjectHats: %v", err)
		}
		if hats.isStaff() {
			t.Error("a keyless worksAt anchor must not confer the staff hat")
		}
	})).ServeHTTP(rec, r)
	if !ran {
		t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
	}
}

// TestResolveSubjectHats_WorksAtNoFrontOfHouse_IsNotFrontDesk is the exact
// shape verticals-designer-triage-2026-08-27.md §7 named: a `worksAt`
// caller holding no `frontOfHouse` role — clinic-domain's write side has
// always required `GrantsTo: [operator, frontOfHouse]`, so this caller
// already held zero op grants — is staff (isStaff, a structural fact) but
// NOT front-desk (isFrontDesk), the predicate the roster gates on.
func TestResolveSubjectHats_WorksAtNoFrontOfHouse_IsNotFrontDesk(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/actor", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actorId":         "vtx.identity." + id,
			"resolvedActorId": "vtx.identity." + id,
			"roles":           []string{},
			"anchors":         []appsession.ActorAnchor{{Relation: "worksAt", Key: staffWorkplace, Name: "Clinic Building"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = srv.URL })
	r := httptest.NewRequest(http.MethodGet, "/api/residents", nil)
	r.AddCookie(cookieFor(id))
	rec := httptest.NewRecorder()
	ran := false
	s.session.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			t.Fatalf("resolveSubjectHats: %v", err)
		}
		if !hats.isStaff() {
			t.Error("a worksAt anchor must still confer the (structural) staff hat")
		}
		if hats.isFrontDesk() {
			t.Error("worksAt without frontOfHouse must not confer the front-desk hat")
		}
	})).ServeHTTP(rec, r)
	if !ran {
		t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
	}
}

// TestResolveSubjectHats_OperatorRoleExempted: the primordial operator role,
// with NO workplace anchor at all, still resolves as staff — the same
// exemption CreateAppointment's own confinement check gives on the write side.
func TestResolveSubjectHats_OperatorRoleExempted(t *testing.T) {
	testutil.EnsurePrimordials(t)
	if bootstrap.RoleOperatorKey == "" {
		t.Fatal("primordial ids loaded but the operator role key is empty")
	}
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	gwURL := fakeGatewayActorWorkplaces(t, nil, map[string]bool{id: true})
	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gwURL })
	r := httptest.NewRequest(http.MethodGet, "/api/residents", nil)
	r.AddCookie(cookieFor(id))
	rec := httptest.NewRecorder()
	ran := false
	s.session.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		hats, err := s.resolveSubjectHats(r)
		if err != nil {
			t.Fatalf("resolveSubjectHats: %v", err)
		}
		if !hats.isOperator {
			t.Error("expected the operator role to set isOperator")
		}
		if hats.isStaff() {
			t.Error("the operator has no workplace anchor, so isStaff() must be false — isOperator is the separate exemption")
		}
	})).ServeHTTP(rec, r)
	if !ran {
		t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
	}
}
