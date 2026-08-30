package main

import (
	"context"
	"encoding/json"
	"errors"
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

// Every wellness lens but one is plain NATS-KV (P5) — the read boundary for
// those is SESSION-scoped instead (persona-worlds-design.md Fire W3 §3). It is
// tiered by data rather than by habit: the class SCHEDULE (/api/studios,
// /api/sessions) is public-read, carrying no person-identifying column about
// the people its rows are about, while every per-user read resolves the
// caller through authenticateRead or resolveSubjectHats — a member sees only
// their own bookings, and a roster is a staff or bound-instructor surface.
// Mirrors cmd/cafe-app's own read boundary, with the instructor hat added.
// The one exception is /api/identities (identities.go), the protected
// wellnessIdentitiesRead Postgres model, RLS-scoped rather than filtered
// here.

const actorFetchTimeout = 10 * time.Second

// instructorKeyPrefix is the vertex-type prefix of a wellness instructor
// entity. An `identifiedBy` anchor names whatever entity a login is bound to —
// a clinic provider and a wellness instructor are both bindings on the same
// relation — so the TYPE is what tells them apart on a multi-hat human
// (packages/identity-domain/lenses.go's identityAnchors collects
// {key: bound.key, relation: 'identifiedBy'}).
const instructorKeyPrefix = "vtx.instructor."

// operatorRoleKey is the primordial root role's VERTEX KEY. /v1/actor reports
// roles as keys, not canonical names, and the operator id is loaded at runtime
// from the bootstrap JSON (bootstrap.Load in main), so this cannot be a
// compile-time constant — the same reason wellness-domain's
// `actor_holds_operator` resolves the role from the graph rather than from a
// substituted literal.
func operatorRoleKey() string { return bootstrap.RoleOperatorKey }

// frontOfHouseRoleKey is identity-domain's `frontOfHouse` role's VERTEX
// KEY — a pure, install-independent derivation, mirroring cmd/cafe-app's
// own frontOfHouseRoleKey.
func frontOfHouseRoleKey() string {
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")
}

// errNoSession marks the caller having no usable session, as distinct from
// this app being unable to resolve one. The two must not be conflated at the
// handler: a 401 tells the FE the session is over and it navigates to /login
// (app.js's isAuthLapse → onSessionLapsed), so reporting a Gateway outage as
// 401 would sign a perfectly valid session out.
var errNoSession = errors.New("no signed-in identity")

// authenticateRead returns the bare identity the session middleware already
// resolved from this request's verified cookie. Defense in depth: the
// middleware refuses to install an empty identity, but a blank principal is
// refused here too rather than depending on that alone — and the identity
// must have been proven by a real COOKIE, never inherited from a boot
// fallback, which proves nothing about who the caller is. This app configures
// no FallbackIdentityID, so requiring ViaCookie is what keeps that true here
// rather than resting on a setting in main.go.
func (s *server) authenticateRead(r *http.Request) (string, error) {
	subject, ok := appsession.Identity(r.Context())
	if !ok || strings.TrimSpace(subject) == "" {
		return "", fmt.Errorf("%w (sign in at %s)", errNoSession, appsession.LoginPagePath)
	}
	if !appsession.ViaCookie(r.Context()) {
		return "", fmt.Errorf("%w: a per-user read needs a real session (sign in at %s)", errNoSession, appsession.LoginPagePath)
	}
	return subject, nil
}

// subjectHats is the caller's read-boundary role, resolved from the signed-in
// session. workplaces holds the location keys the caller `worksAt` — a
// front-desk staffer reads the rosters those locations cover and no others,
// the workplace being one half of isFrontDesk rather than the whole answer;
// instructorKey is the vtx.instructor entity an `identifiedBy` anchor binds
// this login to, empty for everyone else — an instructor sees the roster of
// the sessions they lead and no others. Neither is set for a plain member, who
// is scoped to their own bookings.
type subjectHats struct {
	identityID    string
	workplaces    []string
	instructorKey string
	// isOperator marks the primordial root role. The write side exempts it
	// from workplace confinement (`actor_holds_operator`, wellness-domain's
	// ddls.go), so the read side does too: break-glass admin that can schedule
	// a class at any building must be able to see who is in it.
	isOperator bool
	// frontOfHouse marks the `frontOfHouse` role, the conjunct isFrontDesk
	// composes with a workplace — see isFrontDesk's own comment. Orthogonal
	// to instructorKey: an instructor's own-class roster access needs no
	// front-desk role at all.
	frontOfHouse bool
}

// isStaff reports whether the caller carries any workplace at all — a
// structural fact, NOT an authorization answer; every front-desk-only
// surface (roster, member directory, another member's ledger) gates on
// isFrontDesk, below, OR'd with instructorKey where a bound instructor also
// has standing access to their own sessions.
func (h subjectHats) isStaff() bool { return len(h.workplaces) > 0 }

// isFrontDesk reports whether the caller may use wellness-app's front-desk
// surfaces — the app-side mirror of the write side's own definition
// (wellness-domain/permissions.go's `GrantsTo: [operator, frontOfHouse]`
// rows and service-location's `cap-read.staff` grant lens, both requiring
// `worksAt` AND `holdsRole frontOfHouse`): a worksAt-only caller with no
// frontOfHouse role holds neither an op grant nor a PII-read grant
// (verticals-designer-triage-2026-08-27.md §7).
func (h subjectHats) isFrontDesk() bool { return h.isStaff() && h.frontOfHouse }

// covers reports whether any location the caller works at appears in a row's
// projected covering set — the read-side mirror of wellness-domain's
// `worksAt_covers`, which walks a location's containedIn chain upward testing
// the actor's worksAt link at each level (facet-staff-worlds-design.md §9).
// The lens materializes that chain per row, so the same answer falls out of a
// set intersection. Fails CLOSED on both empty sides: a caller with no
// workplace covers nothing, and a row whose topology is unwired is covered by
// nobody — the denial require_workplace gives an empty location list.
//
// Necessary, never sufficient. Like isStaff it is a structural fact, not an
// authorization answer: reach at a workplace is the FRONT DESK's, so every
// surface that answers workplace-wide — the roster (mayReadRoster,
// bookings.go), its picker (computeRosterSessions, sessions.go), the member
// directory and another member's ledger — conjoins this with isFrontDesk. A
// caller admitted to those surfaces on the instructor hat instead is scoped by
// their instructor-key match, whatever workplace they may separately hold.
func (h subjectHats) covers(coveringLocations []string) bool {
	for _, loc := range coveringLocations {
		// Compare the TRIMMED value, not merely test it for emptiness: the
		// workplace keys were trimmed when they were collected, so testing one
		// form and comparing the other would silently refuse a padded key.
		loc = strings.TrimSpace(loc)
		if loc == "" {
			continue
		}
		for _, work := range h.workplaces {
			if work == loc {
				return true
			}
		}
	}
	return false
}

// identityKey is the caller's full identity VERTEX key. The session resolves
// a bare identity id, while every lens projects the 4-segment vertex key
// (wellnessBookings' bookerKey, leaseApplicationComplete's applicant), so
// scoping a read to the caller compares against this.
func (h subjectHats) identityKey() string {
	return auth.IdentityKeyPrefix + h.identityID
}

// resolveSubjectHats authenticates the caller and asks the Gateway's own
// external /v1/actor door which anchors the session's token carries — the
// same call the kit's own whoami makes for the FE (internal/appsession's
// unexported fetchActorHats), re-issued here because this Go handler, not the
// browser, is the one deciding what a read may return. Fails CLOSED: any
// error resolving the identity, the session's raw token, or the Gateway call
// is refused outright — never defaulting to the unfiltered "staff" answer.
func (s *server) resolveSubjectHats(r *http.Request) (subjectHats, error) {
	identityID, err := s.authenticateRead(r)
	if err != nil {
		return subjectHats{}, err
	}
	token := s.session.CookieToken(r)
	if token == "" {
		return subjectHats{}, fmt.Errorf("%w: no session token to resolve a role from (sign in at %s)", errNoSession, appsession.LoginPagePath)
	}
	actor, err := s.fetchActorAnchors(r.Context(), token)
	if err != nil {
		return subjectHats{}, fmt.Errorf("resolve session role from the Gateway: %w", err)
	}
	hats := subjectHats{identityID: identityID}
	// An unloaded bootstrap leaves the operator key empty. Comparing against it
	// would make every blank role entry root, so an unresolvable operator id
	// grants the exemption to nobody rather than to everybody.
	if opKey := operatorRoleKey(); opKey != "" {
		for _, role := range actor.Roles {
			if strings.TrimSpace(role) == opKey {
				hats.isOperator = true
			}
		}
	}
	if fohKey := frontOfHouseRoleKey(); fohKey != "" {
		for _, role := range actor.Roles {
			if strings.TrimSpace(role) == fohKey {
				hats.frontOfHouse = true
			}
		}
	}
	for _, a := range actor.Anchors {
		// The key must be present, not just the relation. identityAnchors
		// stamps `relation` as a literal constant on every collected entry,
		// so an identity with NO workplace still yields a {key:null,
		// relation:"worksAt"} entry from the unmatched OPTIONAL MATCH
		// (packages/identity-domain/lenses.go:168). That entry is dropped
		// upstream by the lens's RealnessFilter, but a relation-only test
		// here would make this app's staff and instructor boundaries depend
		// on a filter declared in a different package; requiring the key is
		// what makes them self-contained.
		key := strings.TrimSpace(a.Key)
		if key == "" {
			continue
		}
		switch {
		case a.Relation == "worksAt":
			hats.workplaces = append(hats.workplaces, key)
		case a.Relation == "identifiedBy" && strings.HasPrefix(key, instructorKeyPrefix):
			hats.instructorKey = key
		}
	}
	return hats, nil
}

// writeAuthError reports a failed authenticateRead/resolveSubjectHats to the
// client, distinguishing the two kinds. Only a genuinely absent session is a
// 401 — the FE treats that as "your session is over" and navigates to
// /login. A Gateway this app could not reach is a 502: the caller's session
// is fine, so signing them out would be both wrong and a poor experience.
// Either way the caller is refused; the status says whose problem it is.
func (s *server) writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoSession) {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	s.logger.Error("resolve the caller's read hats", "error", err)
	s.writeError(w, http.StatusBadGateway, "could not confirm your access with the Gateway; try again")
}

// handleStaffHats implements GET /api/staff-hats: the one FE-visible bit of
// the caller's server-resolved read-boundary role — whether they hold the
// frontOfHouse hat resolveSubjectHats already computes. Mirrors cmd/cafe-app's
// identical handler; see its comment. The nav gates staff-only surfaces on
// this instead of the raw worksAt anchor alone, so a worksAt-only caller with
// no frontOfHouse role sees them hidden rather than hitting the same 403 the
// write side already gives them (isFrontDesk, above). Uses writeAuthError,
// like every other resolveSubjectHats caller in this app, so a Gateway outage
// reports 502 rather than signing a valid session out.
func (s *server) handleStaffHats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"frontOfHouse": hats.frontOfHouse})
}

// actorAnchorsResponse decodes the Gateway's GET /v1/actor body far enough to
// read the anchors and roles arrays; the response also carries actorId, which
// this read boundary does not need.
type actorAnchorsResponse struct {
	Anchors []appsession.ActorAnchor `json:"anchors"`
	Roles   []string                 `json:"roles"`
}

// fetchActorAnchors asks the Gateway which residence/workplace/binding
// anchors and roles token's actor carries.
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
