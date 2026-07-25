package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// linkPOST drives one POST at the credential-link ceremony through the real
// session middleware, so an absent or unverifiable cookie is judged by exactly
// the code that guards the endpoint in production.
func linkPOST(s *server, body string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/credentials/link/complete", strings.NewReader(body))
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.session.RequireSession(http.HandlerFunc(s.handleCompleteCredentialLink)).ServeHTTP(rec, r)
	return rec
}

// TestCompleteCredentialLink_TargetsOnlyTheSession is the load-bearing
// property: the identity a credential gets bound to comes from the verified
// session, never the request body. A caller that names someone else's identity
// gets its OWN identity linked, not theirs.
func TestCompleteCredentialLink_TargetsOnlyTheSession(t *testing.T) {
	const caller = "Hj4kPmRtw9nbCxz5vQ2y"
	const victim = "PQipBmNwsvkcQeoT37Az"

	var gotTarget string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotTarget = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"accepted","primaryKey":"vtx.identity.` + caller + `"}`))
	}))
	defer gw.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gw.URL })
	s.signer = mustSigner(t)

	rec := linkPOST(s, `{"linkKey":"secret","targetIdentityKey":"vtx.identity.`+victim+`"}`, cookieFor(caller))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotTarget, "vtx.identity."+caller) {
		t.Fatalf("the op must target the SESSION identity %q, got payload %s", caller, gotTarget)
	}
	if strings.Contains(gotTarget, victim) {
		t.Fatalf("a caller-supplied targetIdentityKey must never reach the op; payload %s", gotTarget)
	}
}

// TestCompleteCredentialLink_MintsItsOwnCredential proves the credential being
// bound is server-generated: the browser neither names nor sees a subject, and
// the returned credential key is not the session's own identity.
func TestCompleteCredentialLink_MintsItsOwnCredential(t *testing.T) {
	const caller = "Hj4kPmRtw9nbCxz5vQ2y"
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer gw.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gw.URL })
	s.signer = mustSigner(t)

	rec := linkPOST(s, `{"linkKey":"secret"}`, cookieFor(caller))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "credentialKey") {
		t.Fatalf("response must name the minted credential, got %s", body)
	}
	if strings.Contains(body, caller) {
		t.Fatalf("the minted credential must be a FRESH subject, not the session identity; got %s", body)
	}
}

// TestCompleteCredentialLink_NoCookie_401: no session ⇒ no identity to link a
// credential onto, so the ceremony never runs.
func TestCompleteCredentialLink_NoCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	s.signer = mustSigner(t)
	if rec := linkPOST(s, `{"linkKey":"secret"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestCompleteCredentialLink_ForgedCookie_401: a cookie that does not verify is
// refused rather than falling back to any identity.
func TestCompleteCredentialLink_ForgedCookie_401(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	s.signer = mustSigner(t)
	forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.jwt"}
	if rec := linkPOST(s, `{"linkKey":"secret"}`, forged); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestCompleteCredentialLink_BootFallbackIdentity_403 is the confinement that
// does not rest on a deployment happening to omit a boot identity: a session
// resolved from the boot fallback rather than a cookie proved nothing about
// who the caller is, so it must not be able to bind a credential onto the
// process's own identity.
func TestCompleteCredentialLink_BootFallbackIdentity_403(t *testing.T) {
	s, _ := devSessionServer(t, nil)
	s.signer = mustSigner(t)

	r := httptest.NewRequest(http.MethodPost, "/api/credentials/link/complete", strings.NewReader(`{"linkKey":"secret"}`))
	// viaCookie=false is exactly what the middleware installs for a boot-env
	// fallback identity.
	r = r.WithContext(appsession.WithSession(r.Context(), "Hj4kPmRtw9nbCxz5vQ2y", false))
	rec := httptest.NewRecorder()
	s.handleCompleteCredentialLink(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (a boot-env identity must not link credentials)", rec.Code)
	}
}

// TestCompleteCredentialLink_NoSigner_404: the production verify-only posture
// mints nothing, so the ceremony reports no such surface rather than half-running.
func TestCompleteCredentialLink_NoSigner_404(t *testing.T) {
	s, cookieFor := devSessionServer(t, nil)
	s.signer = nil
	if rec := linkPOST(s, `{"linkKey":"secret"}`, cookieFor("Hj4kPmRtw9nbCxz5vQ2y")); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestCompleteCredentialLink_DemoPersonaPosture_404: a hosted demo's people are
// fixed and pre-claimed, so the ceremony's write surface stays closed.
func TestCompleteCredentialLink_DemoPersonaPosture_404(t *testing.T) {
	const caller = "Hj4kPmRtw9nbCxz5vQ2y"
	s, cookieFor := devSessionServer(t, nil)
	s.signer = mustSigner(t)
	fenced, err := appsession.New(appsession.Config{
		AppName:   appName,
		EnvPrefix: envPrefix,
		Logger:    discardLogger(),
		Loopback:  true,
		LoginPage: []byte("<html>login</html>"),
		Personas:  []appsession.Persona{{ID: caller, Label: "Demo"}},
	})
	if err != nil {
		t.Fatalf("appsession.New: %v", err)
	}
	cookie := cookieFor(caller)
	s.session = fenced
	r := httptest.NewRequest(http.MethodPost, "/api/credentials/link/complete", strings.NewReader(`{"linkKey":"secret"}`))
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.handleCompleteCredentialLink(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (persona-fenced deployment)", rec.Code)
	}
}

// TestCompleteCredentialLink_WrongMethod_405 and _MissingLinkKey_400 pin the
// request shape: there is exactly one verb and the secret is mandatory.
func TestCompleteCredentialLink_WrongMethod_405(t *testing.T) {
	const caller = "Hj4kPmRtw9nbCxz5vQ2y"
	s, cookieFor := devSessionServer(t, nil)
	s.signer = mustSigner(t)
	r := httptest.NewRequest(http.MethodGet, "/api/credentials/link/complete", nil)
	r.AddCookie(cookieFor(caller))
	rec := httptest.NewRecorder()
	s.session.RequireSession(http.HandlerFunc(s.handleCompleteCredentialLink)).ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestCompleteCredentialLink_MissingLinkKey_400(t *testing.T) {
	const caller = "Hj4kPmRtw9nbCxz5vQ2y"
	s, cookieFor := devSessionServer(t, nil)
	s.signer = mustSigner(t)
	if rec := linkPOST(s, `{"linkKey":"  "}`, cookieFor(caller)); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// mustSigner builds the demo-posture signer the ceremony mints its throwaway
// device credential with.
func mustSigner(t *testing.T) *appsession.Signer {
	t.Helper()
	t.Setenv("LOFTSPACE_APP_DEV_AUTH", "1")
	signer, err := appsession.NewDevSigner(discardLogger(), envPrefix, true)
	if err != nil {
		t.Fatalf("NewDevSigner: %v", err)
	}
	return signer
}
