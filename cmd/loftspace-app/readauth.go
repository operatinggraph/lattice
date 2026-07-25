package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
)

// The read boundary (D1.3 Fire 3) — loftspace-app reads the protected
// lease-applications Postgres models as an AUTHENTICATED actor. Authentication
// is the shared browser-session kit (internal/appsession): an HttpOnly session
// cookie carries the same JWT the Gateway verifies on every write, the session
// middleware verifies it once per request, and the identity it resolves is the
// RLS principal. The app never holds authorization logic — Postgres RLS is the
// single authorization source.
//
// Two postures, selected by env (fail-closed: neither configured ⇒ no verifier
// ⇒ every session-gated request is 401 before a handler runs):
//
//   - DEMO (LOFTSPACE_APP_DEV_AUTH=1, loopback bind only): POST /api/dev-login
//     opens a session for the identity signing in, signed with the checked-in
//     dev key shared by the Gateway and every vertical app
//     (deploy/gateway-dev-key/, kid auth.DevKeyID — real-actor-write-auth-e2e-
//     design.md §3.2's shared-dev-IdP interim). Because the key is shared, that
//     token verifies at BOTH this app's read boundary and the Gateway's write
//     path — one dev identity, one token, both surfaces — which is what lets the
//     browser-direct FE (writes → Gateway, reads → app) act as a single actor.
//     The private key is dev-only and never accepted from outside a loopback
//     bind.
//   - PRODUCTION (LOFTSPACE_APP_JWT_PUBLIC_KEY + LOFTSPACE_APP_JWT_ISSUER): the
//     verifier trusts the external IdP's public key; nothing is minted here
//     (actor signing keys live outside the platform), so the login and refresh
//     endpoints report 404 and only an externally-issued token opens a session.

// authenticateRead returns the actor a protected read runs as: the identity the
// session middleware already resolved from this request's verified cookie. A
// credential that is bound to a business identity was resolved to that identity
// once, at login (internal/appsession's handleDevLogin), so the subject carried
// here is already the identity the RLS grants are keyed on.
func (s *server) authenticateRead(r *http.Request) (auth.VerifiedActor, error) {
	subject, ok := appsession.Identity(r.Context())
	// Defense in depth: the middleware refuses to install an empty identity,
	// but a protected read keys RLS off actor.Subject
	// (set_config('lattice.actor_id', …)). Refuse a blank principal here rather
	// than depend on the RLS policy to deny an empty actor.
	if !ok || strings.TrimSpace(subject) == "" {
		return auth.VerifiedActor{}, fmt.Errorf("no signed-in identity (sign in at %s)", appsession.LoginPagePath)
	}
	return auth.VerifiedActor{ActorID: auth.IdentityKeyPrefix + subject, Subject: subject}, nil
}
