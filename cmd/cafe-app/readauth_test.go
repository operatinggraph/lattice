package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes. Mirrors
// cmd/clinic-app/readauth_test.go's own TestMain.
func TestMain(m *testing.M) {
	os.Setenv(envPrefix+"_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv(envPrefix+"_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
	os.Exit(m.Run())
}

// staffWorkplace is the location a plain staff subject `worksAt`;
// otherWorkplace is a second building nobody in the default fixture works at.
// The pair is what makes the workplace boundary DISCRIMINATING rather than
// merely present — a test seeding only staffWorkplace would pass just as well
// against a boundary that admitted every lease.
const (
	staffWorkplace = "vtx.building.A9jnKK2bGwZNrfHHkLme"
	otherWorkplace = "vtx.building.T4pQmZbNxKrWvL8dHcyR"
)

// fakeGatewayActor stands in for the Gateway's external GET /v1/actor door
// that resolveSubjectHats calls: it decodes the bearer's JWT subject
// (unverified — a trusted test double, not a security boundary, standing in
// for the Gateway which has already verified the token) and reports a
// `worksAt` anchor at staffWorkplace for exactly the staff subjects named —
// plus the `frontOfHouse` role each of them (fakeGatewayActorWorkplaces),
// since every caller of this helper means "front-desk staff", the only kind
// of worksAt-carrying identity this app's staff surfaces actually admit
// (isFrontDesk, readauth.go). A worksAt-only, role-less caller is its own
// case, `TestNonFrontOfHouseRole_IsNotExempt`.
// Returns the fake server's base URL, to set as server.gatewayURL.
func fakeGatewayActor(t *testing.T, staffSubjects map[string]bool) string {
	t.Helper()
	workplaces := map[string][]string{}
	for subj := range staffSubjects {
		workplaces[subj] = []string{staffWorkplace}
	}
	return fakeGatewayActorWorkplaces(t, workplaces, nil)
}

// nonOperatorRoleKey is a role vertex key that is NOT the primordial operator
// — the shape a consoleOperator or frontOfHouse holder carries. A test that
// only ever emitted the operator key could not tell "exempts the operator"
// from "exempts anyone holding any role".
const nonOperatorRoleKey = "vtx.role.T4pQmZbNxKrWvL8dHcyR"

// fakeGatewayActorWorkplaces is the same double with the workplace anchors
// named per subject, plus the set of subjects holding the primordial operator
// role. Roles are reported as VERTEX KEYS, never canonical names, because that
// is what the real Gateway forwards (internal/gateway/whoami.go returns what
// rolesanchors resolved) — a fixture answering "operator" would let a
// name comparison pass here while matching nothing against a live Gateway.
//
// Every subject with at least one workplace ALSO gets the `frontOfHouse`
// role: this app's staff surfaces gate on isFrontDesk (worksAt AND
// frontOfHouse, readauth.go — mirroring the write side's own
// `GrantsTo: [operator, frontOfHouse]`), so a worksAt-only fixture would
// only ever exercise the 403 path every caller of this helper is instead
// using to assert 200. A test wanting a worksAt-only, role-less caller
// (there is exactly one: `TestNonFrontOfHouseRole_IsNotExempt`) calls
// fakeGatewayActorRoles directly instead.
func fakeGatewayActorWorkplaces(t *testing.T, workplaces map[string][]string, operators map[string]bool) string {
	roles := map[string][]string{}
	for subj, locs := range workplaces {
		if len(locs) > 0 {
			roles[subj] = []string{frontOfHouseRoleKey()}
		}
	}
	return fakeGatewayActorRoles(t, workplaces, operators, roles)
}

// fakeGatewayActorRoles is the fullest form: workplace anchors, the operator
// exemption, and arbitrary extra role keys per subject.
func fakeGatewayActorRoles(t *testing.T, workplaces map[string][]string, operators map[string]bool, extraRoles map[string][]string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/actor", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var claims jwt.RegisteredClaims
		if _, _, err := jwt.NewParser().ParseUnverified(tok, &claims); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// A real workplace anchor carries the building it points at. The
		// key matters: identityAnchors also emits a keyless {relation:
		// "worksAt"} entry for an identity with no workplace at all, so a
		// fixture that omitted the key would be shaped like the degenerate
		// entry and could not tell the two apart.
		anchors := []appsession.ActorAnchor{}
		for _, key := range workplaces[claims.Subject] {
			anchors = append(anchors, appsession.ActorAnchor{
				Relation: "worksAt", Key: key, Name: "Riverside Building",
			})
		}
		roles := append([]string{}, extraRoles[claims.Subject]...)
		if operators[claims.Subject] {
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

// TestAuthenticateRead_SessionIdentityIsTheSubject: the identity the session
// resolved is exactly what authenticateRead returns.
func TestAuthenticateRead_SessionIdentityIsTheSubject(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	r := httptest.NewRequest(http.MethodGet, "/api/leases", nil)
	subject, err := s.authenticateRead(r.WithContext(appsession.WithSession(r.Context(), id, true)))
	if err != nil {
		t.Fatalf("authenticateRead: %v", err)
	}
	if subject != id {
		t.Errorf("subject = %q, want %q", subject, id)
	}
}

// TestAuthenticateRead_NoSession_Errors: no resolved identity ⇒ refused
// rather than treated as an unfiltered read run as nobody.
func TestAuthenticateRead_NoSession_Errors(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	if _, err := s.authenticateRead(httptest.NewRequest(http.MethodGet, "/api/leases", nil)); err == nil {
		t.Fatal("expected an error with no session on the request")
	}
}

// TestAuthenticateRead_BlankIdentity_Errors is the defence in depth: a blank
// principal must never reach a scoping decision.
func TestAuthenticateRead_BlankIdentity_Errors(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	r := httptest.NewRequest(http.MethodGet, "/api/leases", nil)
	r = r.WithContext(appsession.WithSession(r.Context(), "   ", true))
	if _, err := s.authenticateRead(r); err == nil {
		t.Fatal("expected an error for a blank session identity")
	}
}

// TestResolveSubjectHats_GatewayUnreachable_FailsClosed: the Gateway call
// resolveSubjectHats depends on to learn the caller's anchors is down ⇒
// refused outright, never defaulting to "staff" (the unfiltered answer).
func TestResolveSubjectHats_GatewayUnreachable_FailsClosed(t *testing.T) {
	const id = "Hj4kPmRtw9nbCxz5vQ2y"
	s, cookieFor := devSessionServer(t, "http://127.0.0.1:1") // nothing listens here
	r := httptest.NewRequest(http.MethodGet, "/api/leases", nil)
	r.AddCookie(cookieFor(id))
	rec := httptest.NewRecorder()
	// The assertion lives inside the middleware's next-handler, so it only
	// runs if the session resolved. Without this flag a RequireSession that
	// refused the request first would skip the body and pass vacuously.
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

// TestResolveSubjectHats_KeylessWorksAtAnchorIsNotStaff: identityAnchors
// stamps `relation` as a literal on every collected entry, so an identity
// with no workplace still produces a {key:null, relation:"worksAt"} entry
// from the unmatched OPTIONAL MATCH. A caller carrying only that entry is a
// resident, and must not be read as staff.
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

	s, cookieFor := devSessionServer(t, srv.URL)
	r := httptest.NewRequest(http.MethodGet, "/api/leases", nil)
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
