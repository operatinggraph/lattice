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

// fakeGatewayActor stands in for the Gateway's external GET /v1/actor door
// that resolveSubjectHats calls: it decodes the bearer's JWT subject
// (unverified — a trusted test double, not a security boundary, standing in
// for the Gateway which has already verified the token) and reports a
// `worksAt` anchor for exactly the staff subjects named. Returns the fake
// server's base URL, to set as server.gatewayURL.
func fakeGatewayActor(t *testing.T, staffSubjects map[string]bool) string {
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
		if staffSubjects[claims.Subject] {
			anchors = append(anchors, appsession.ActorAnchor{
				Relation: "worksAt",
				Key:      "vtx.building.A9jnKK2bGwZNrfHHkLme",
				Name:     "Riverside Building",
			})
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
		if hats.isStaff {
			t.Error("a keyless worksAt anchor must not confer the staff hat")
		}
	})).ServeHTTP(rec, r)
	if !ran {
		t.Fatal("the assertion never ran — the session did not resolve, so nothing was actually tested")
	}
}
