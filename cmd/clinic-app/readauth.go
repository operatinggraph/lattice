package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// The read boundary (D1.5, porting D1.3's loftspace-app pattern into a second
// vertical) — clinic-app reads the protected clinicAppointmentsRead Postgres
// model as an AUTHENTICATED actor. Authentication is the shared browser-session
// kit (internal/appsession): an HttpOnly session cookie carries the same JWT the
// Gateway verifies on every write, the session middleware verifies it once per
// request, and the identity it resolves is the RLS principal. The app never
// holds authorization logic — Postgres RLS is the single authorization source.
//
// Two postures, selected by env (fail-closed: neither configured ⇒ no verifier
// ⇒ every session-gated request is 401 before a handler runs):
//
//   - DEMO (CLINIC_APP_DEV_AUTH=1, loopback bind only): POST /api/dev-login
//     opens a session for the chosen identity, signed with the checked-in dev
//     key shared by the Gateway and every vertical app (deploy/gateway-dev-key/,
//     kid auth.DevKeyID — real-actor-write-auth-e2e-design.md §3.2's shared-dev-
//     IdP interim). Because the key is shared, that token verifies at BOTH this
//     app's read boundary and the Gateway's write path — one dev identity, one
//     token, both surfaces — which is what lets the browser-direct FE (writes →
//     Gateway, reads → app) act as a single actor. The private key is dev-only
//     and never accepted from outside a loopback bind.
//   - PRODUCTION (CLINIC_APP_JWT_PUBLIC_KEY + CLINIC_APP_JWT_ISSUER): the
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

const actorFetchTimeout = 10 * time.Second

// subjectHats is the caller's read-boundary role for the one clinic-app read
// that has no RLS-protected query of its own (residents.go's leaseApplicationComplete
// lookup): workplaces holds every clinic building the caller worksAt — the
// same anchor clinic-domain's write-side confinement keys on
// (worksAt_covers, packages/clinic-domain/ddls.go) — and isOperator marks the
// primordial root role, exempted the same way CreateAppointment's own
// confinement check exempts it.
type subjectHats struct {
	identityID string
	workplaces []string
	isOperator bool
	// frontOfHouse marks the `frontOfHouse` role, the conjunct isFrontDesk
	// composes with a workplace — see isFrontDesk's own comment.
	frontOfHouse bool
}

// isStaff reports whether the caller carries any clinic workplace at all —
// a structural fact, NOT an authorization answer; the front-desk roster
// gates on isFrontDesk, below.
func (h subjectHats) isStaff() bool { return len(h.workplaces) > 0 }

// isFrontDesk reports whether the caller may use clinic-app's front-desk
// surfaces — the app-side mirror of the write side's own definition
// (clinic-domain/permissions.go's `GrantsTo: [operator, frontOfHouse]` and
// service-location's `cap-read.staff` grant lens, both requiring `worksAt`
// AND `holdsRole frontOfHouse`): a worksAt-only caller with no frontOfHouse
// role holds neither an op grant nor a PII-read grant
// (verticals-designer-triage-2026-08-27.md §7).
func (h subjectHats) isFrontDesk() bool { return h.isStaff() && h.frontOfHouse }

func operatorRoleKey() string { return bootstrap.RoleOperatorKey }

// frontOfHouseRoleKey is identity-domain's `frontOfHouse` role's VERTEX
// KEY — a pure, install-independent derivation, mirroring cmd/cafe-app's
// own frontOfHouseRoleKey.
func frontOfHouseRoleKey() string {
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")
}

// resolveSubjectHats authenticates the caller and asks the Gateway's own
// external /v1/actor door which anchors and roles the session's token
// carries — mirrors cmd/cafe-app's and cmd/wellness-app's identical
// resolveSubjectHats, the established precedent for scoping a read that,
// unlike the Postgres RLS protected models above, has no actor-scoped query
// of its own. Fails CLOSED: any error resolving the identity, the session's
// raw token, or the Gateway call is refused outright — never defaulting to
// the unfiltered "staff" answer.
func (s *server) resolveSubjectHats(r *http.Request) (subjectHats, error) {
	subject, ok := appsession.Identity(r.Context())
	if !ok || strings.TrimSpace(subject) == "" {
		return subjectHats{}, fmt.Errorf("no signed-in identity (sign in at %s)", appsession.LoginPagePath)
	}
	token := s.session.CookieToken(r)
	if token == "" {
		return subjectHats{}, fmt.Errorf("no session token to resolve a role from (sign in at %s)", appsession.LoginPagePath)
	}
	actor, err := s.fetchActorAnchors(r.Context(), token)
	if err != nil {
		return subjectHats{}, fmt.Errorf("resolve session role from the Gateway: %w", err)
	}
	hats := subjectHats{identityID: subject}
	// The operator key is empty until bootstrap.Load has run; comparing
	// against it then would exempt every role-less caller, so a blank key
	// matches nothing rather than everything.
	if opKey := operatorRoleKey(); strings.TrimSpace(opKey) != "" {
		for _, role := range actor.Roles {
			if strings.TrimSpace(role) == opKey {
				hats.isOperator = true
			}
		}
	}
	if fohKey := frontOfHouseRoleKey(); strings.TrimSpace(fohKey) != "" {
		for _, role := range actor.Roles {
			if strings.TrimSpace(role) == fohKey {
				hats.frontOfHouse = true
			}
		}
	}
	for _, a := range actor.Anchors {
		if a.Relation == "worksAt" && strings.TrimSpace(a.Key) != "" {
			hats.workplaces = append(hats.workplaces, strings.TrimSpace(a.Key))
		}
	}
	return hats, nil
}

// actorAnchorsResponse decodes the Gateway's GET /v1/actor body far enough to
// read the anchors and roles arrays; the response also carries actorId,
// which this read boundary does not need.
type actorAnchorsResponse struct {
	Anchors []appsession.ActorAnchor `json:"anchors"`
	Roles   []string                 `json:"roles"`
}

// fetchActorAnchors asks the Gateway which workplace anchors and roles
// token's actor carries.
func (s *server) fetchActorAnchors(ctx context.Context, token string) (actorAnchorsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, actorFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.gatewayURL+"/v1/actor", nil)
	if err != nil {
		return actorAnchorsResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return actorAnchorsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return actorAnchorsResponse{}, fmt.Errorf("gateway /v1/actor: HTTP %d", resp.StatusCode)
	}
	var body actorAnchorsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return actorAnchorsResponse{}, err
	}
	return body, nil
}
