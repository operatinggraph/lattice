package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

const testTimeout = 5 * time.Second

// TestMain points the dev-auth posture's shared-dev-key loader at the repo
// root (deploy/gateway-dev-key/), since a test binary's CWD is this package's
// directory, not the repo root the production default path assumes.
func TestMain(m *testing.M) {
	os.Setenv("LOFTSPACE_APP_DEV_PRIVATE_KEY_PATH", "../../deploy/gateway-dev-key/dev-private.pem")
	os.Setenv("LOFTSPACE_APP_DEV_PUBLIC_KEY_PATH", "../../deploy/gateway-dev-key/dev-public.pem")
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
	t.Setenv("LOFTSPACE_APP_DEV_AUTH", "1")
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
	r := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
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
	if _, err := s.authenticateRead(httptest.NewRequest(http.MethodGet, "/api/applications", nil)); err == nil {
		t.Fatal("expected an error with no session on the request")
	}
}

// TestAuthenticateRead_BlankIdentity_Errors is the defence in depth: a blank
// principal must never reach set_config('lattice.actor_id', …).
func TestAuthenticateRead_BlankIdentity_Errors(t *testing.T) {
	s := &server{logger: discardLogger(), natsTimeout: testTimeout}
	r := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	r = r.WithContext(appsession.WithSession(r.Context(), "   ", true))
	if _, err := s.authenticateRead(r); err == nil {
		t.Fatal("expected an error for a blank session identity")
	}
}

func TestHandleApplications_NoAuthPosture_401(t *testing.T) {
	s := noPostureServer(t)
	rec := sessionGET(s, s.handleApplications, "/api/applications", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleApplications_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	rec := sessionGET(s, s.handleApplications, "/api/applications", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no session cookie)", rec.Code)
	}
}

func TestHandleApplications_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.valid.jwt"}
	rec := sessionGET(s, s.handleApplications, "/api/applications", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (forged cookie)", rec.Code)
	}
}

// TestHandleApplications_ValidSession_PoolUnconfigured_502: a signed-in actor
// with no read-model pool gets a clean 502, never a nil-pointer panic.
func TestHandleApplications_ValidSession_PoolUnconfigured_502(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil) // session set, pgPool nil
	rec := sessionGET(s, s.handleApplications, "/api/applications", cookieFor("Hj4kPmRtw9nbCxz5vQ2y"))
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
		DevKeyPath: os.Getenv("LOFTSPACE_APP_DEV_PUBLIC_KEY_PATH"),
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

func TestDeriveLandlordFlags(t *testing.T) {
	approved := "approved"
	declined := "declined"
	cases := []struct {
		name              string
		decision          *string
		wantApproved      bool
		wantDeclined      bool
		wantDeclinedAlias bool
	}{
		{"nil", nil, false, false, false},
		{"approved", &approved, true, false, false},
		{"declined", &declined, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := protectedApplicationRow{LandlordDecision: c.decision}
			deriveLandlordFlags(&row)
			if row.LandlordApproved != c.wantApproved || row.LandlordDeclined != c.wantDeclined || row.Declined != c.wantDeclinedAlias {
				t.Errorf("flags = approved:%v declined:%v declinedAlias:%v", row.LandlordApproved, row.LandlordDeclined, row.Declined)
			}
		})
	}
}
