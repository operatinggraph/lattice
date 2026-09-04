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
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
)

// Every café lens but one is plain NATS-KV (P5) — the read boundary for those
// is SESSION-scoped instead (persona-worlds-design.md Fire W4 §3): a resident
// may see only rows for leases whose bookerKey is their own signed-in
// identity, and a `worksAt` staffer sees the leases their workplace covers —
// the house they work in, not every house (facet-staff-worlds-design.md §9).
// Every read handler resolves the caller through authenticateRead or
// resolveSubjectHats, mirroring cmd/clinic-app's own authenticateRead read
// boundary. The one exception is /api/identities (identities.go), the
// protected cafeIdentitiesRead Postgres model, RLS-scoped rather than
// filtered here.

const actorFetchTimeout = 10 * time.Second

// operatorRoleKey is the primordial root role's VERTEX KEY. /v1/actor reports
// roles as keys, not canonical names, and the operator id is loaded at runtime
// from the bootstrap JSON (bootstrap.Load in main), so this cannot be a
// compile-time constant — the same reason cafe-domain's `actor_holds_operator`
// resolves the role from the graph rather than from a substituted literal.
func operatorRoleKey() string { return bootstrap.RoleOperatorKey }

// frontOfHouseRoleKey is identity-domain's `frontOfHouse` role's VERTEX KEY —
// a pure, install-independent derivation (unlike the operator key, which
// needs a bootstrap read), so it can be a plain function rather than a value
// threaded in from main. This is the SAME conjunct cafe-domain's own op
// grants (`GrantsTo: [operator, frontOfHouse]`) and service-location's
// `cap-read.staff` grant lens require — a `worksAt`-only caller with no
// frontOfHouse role holds zero POS grants and resolves zero other
// residents' names, so treating them as front-desk staff here would offer a
// surface they cannot actually use (verticals-designer-triage-2026-08-27.md
// §7).
func frontOfHouseRoleKey() string {
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")
}

// authenticateRead returns the bare identity the session middleware already
// resolved from this request's verified cookie. Defense in depth: the
// middleware refuses to install an empty identity, but a blank principal is
// refused here too rather than depending on that alone.
func (s *server) authenticateRead(r *http.Request) (string, error) {
	subject, ok := appsession.Identity(r.Context())
	if !ok || strings.TrimSpace(subject) == "" {
		return "", fmt.Errorf("no signed-in identity (sign in at %s)", appsession.LoginPagePath)
	}
	return subject, nil
}

// subjectHats is the caller's read-boundary role, resolved from the signed-in
// session. workplaces holds the location keys the caller `worksAt` — a staffer
// reads the leases those locations cover and no others, including on the
// front-desk staff surface; a caller with none is a resident, scoped to their
// own lease's rows.
type subjectHats struct {
	identityID string
	workplaces []string
	// isOperator marks the primordial root role. The write side exempts it
	// from workplace confinement (`require_workplace`'s actor_holds_operator,
	// cafe-domain's ddls.go), so the read side does too: break-glass admin
	// that can settle a tab at any building must be able to see it.
	isOperator bool
	// frontOfHouse marks the `frontOfHouse` role, the conjunct isFrontDesk
	// composes with a workplace. A `backOfHouse`-only or role-less worksAt
	// caller leaves this false.
	frontOfHouse bool
}

// isStaff reports whether the caller carries any workplace at all — a
// structural fact, "does this identity work here", NOT an authorization
// answer. PII-adjacent front-desk surfaces (POS, roster, another lease's
// ledger) gate on `isFrontDesk`, below. The Manage Menu grid still gates on
// this directly (handleMenu) — that surface confines a workplace's own
// (non-sensitive) catalog rather than refusing outright, so a worksAt-only
// backOfHouse caller sees a workplace-scoped read, not the whole catalog,
// and every write it could attempt still 403s at the op grant regardless.
func (h subjectHats) isStaff() bool { return len(h.workplaces) > 0 }

// isFrontDesk reports whether the caller may use the café's staff surfaces —
// the app-side mirror of the write side's own definition (permissions.go's
// `GrantsTo: [operator, frontOfHouse]` and service-location's `cap-read.staff`
// grant lens, both requiring `worksAt` AND `holdsRole frontOfHouse`): a
// worksAt-only caller holds neither a POS grant nor a PII-read grant, so
// isStaff() alone let a role-less or backOfHouse-only worksAt caller into a
// front-desk surface that could only ever show them unresolved names and
// 403 every write (verticals-designer-triage-2026-08-27.md §7).
func (h subjectHats) isFrontDesk() bool { return h.isStaff() && h.frontOfHouse }

// covers reports whether any location the caller works at appears in a lease's
// projected covering set — the read-side mirror of cafe-domain's
// `worksAt_covers`, which walks a unit's containedIn chain upward testing the
// actor's worksAt link at each level (facet-staff-worlds-design.md §9). The
// cafeLeaseWorkplaces lens materializes that chain per lease, so the same
// answer falls out of a set intersection. Fails CLOSED on both empty sides: a
// caller with no workplace covers nothing, and a lease whose topology is
// unwired is covered by nobody — the denial require_workplace gives an empty
// location list.
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
		return subjectHats{}, fmt.Errorf("no session token to resolve a role from (sign in at %s)", appsession.LoginPagePath)
	}
	actor, err := s.fetchActorAnchors(r.Context(), token)
	if err != nil {
		return subjectHats{}, fmt.Errorf("resolve session role from the Gateway: %w", err)
	}
	hats := subjectHats{identityID: identityID}
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
		// The key must be present, not just the relation. identityAnchors
		// stamps `relation` as a literal constant on every collected entry,
		// so an identity with NO workplace still yields a {key:null,
		// relation:"worksAt"} entry from the unmatched OPTIONAL MATCH
		// (packages/identity-domain/lenses.go:168). That entry is dropped
		// upstream by the lens's RealnessFilter, but a relation-only test
		// here would make this app's entire staff boundary depend on a
		// filter declared in a different package; requiring the key is what
		// makes the boundary self-contained.
		if a.Relation == "worksAt" && strings.TrimSpace(a.Key) != "" {
			hats.workplaces = append(hats.workplaces, strings.TrimSpace(a.Key))
		}
	}
	return hats, nil
}

// handleStaffHats implements GET /api/staff-hats: the one FE-visible bit of
// the caller's server-resolved read-boundary role — whether they hold the
// frontOfHouse hat resolveSubjectHats already computes for every read. The
// nav gates staff-only tabs (POS/Front Desk/Manage Menu) on this instead of
// the raw worksAt anchor alone, so a worksAt-only caller with no
// frontOfHouse role sees those tabs hidden rather than hitting the same 403
// the write side (and every other read handler) already gives them
// (isFrontDesk, above). Fails closed on any resolveSubjectHats error: no
// body is written that a caller could mistake for "frontOfHouse: false"
// still meaning "resolved and confirmed false".
func (s *server) handleStaffHats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
	hats, err := s.resolveSubjectHats(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
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

// fetchActorAnchors asks the Gateway which residence/workplace anchors and
// roles token's actor carries.
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

// leaseVisibility is the set of leases one caller may see rows for. Every café
// staff and resident read keys on leaseAppKey — tabs, leases, residents,
// ledger, and all three front-desk grid rows — so ONE resolved set confines
// all of them, and each handler asks the same question of it.
//
// The zero value admits nothing, which is what makes this fail closed by
// construction: an unresolved or errored visibility cannot be mistaken for an
// unrestricted one. `unrestricted` is set on exactly one path (the operator),
// never as a fallback.
type leaseVisibility struct {
	unrestricted bool
	leases       map[string]bool
}

// admits reports whether this caller may see rows for leaseAppKey.
func (v leaseVisibility) admits(leaseAppKey string) bool {
	return v.unrestricted || v.leases[leaseAppKey]
}

// visibleLeases resolves the caller's lease visibility: an operator sees every
// lease (the exemption require_workplace gives root on the write side), and
// everyone else sees the UNION of the leases they hold as a resident and — if
// they carry a `worksAt` anchor — the leases their workplace covers
// (facet-staff-worlds-design.md §9). One rule read at one place rather than a
// staff test repeated at seven handlers, which is how the workplace term came
// to be missing from all of them at once.
//
// The union is load-bearing, not incidental. A café staffer who also LIVES in
// the building is one person with two hats, and the hats are complementary,
// not alternatives — the write side says so in as many words
// (require_workplace's own comment, cafe-domain's ddls.go: a scope=self caller
// is bound by their op's ownership probe, a scope=any staff caller by the
// workplace guard, "each binds the path the other cannot see"). Resolving only
// the staff half would hide a staffer's OWN house tab from them the moment
// they take a job at a different building.
func (s *server) visibleLeases(ctx context.Context, hats subjectHats) (leaseVisibility, error) {
	if hats.isOperator {
		return leaseVisibility{unrestricted: true}, nil
	}
	leases, err := s.residentOwnLeases(ctx, hats.identityID)
	if err != nil {
		return leaseVisibility{}, fmt.Errorf("resolve resident's own leases: %w", err)
	}
	if hats.isFrontDesk() {
		covered, unattributable, err := s.staffCoveredLeases(ctx, hats)
		if err != nil {
			return leaseVisibility{}, err
		}
		for key := range covered {
			leases[key] = true
		}
		for key := range unattributable {
			leases[key] = true
		}
	}
	return leaseVisibility{leases: leases}, nil
}

// notYourLease is the denial for naming a lease outside the caller's
// visibility. A front-desk staffer and a resident are refused for different
// reasons, and the message says which — "not yours" is misleading at the
// front desk, where the lease genuinely is somebody's, just not at this
// staffer's building. Neither message names the lease's actual location:
// that a lease exists somewhere is already implied by asking for it, but
// where is not.
func notYourLease(hats subjectHats) string {
	if hats.isFrontDesk() {
		return "that lease is not at a place you work"
	}
	return "that lease is not yours"
}

// leaseWorkplaceProjection is one row of the cafe-domain `cafeLeaseWorkplaces`
// lens — the locations that cover a lease. MissingLocation distinguishes an
// empty CoveringLocations caused by a data gap (the appliesToUnit target is
// gone or never wired) from the ordinary "no workplace reaches this lease"
// answer — see leaseWorkplacesSpec (cafe-domain/lenses.go).
type leaseWorkplaceProjection struct {
	LeaseAppKey       string   `json:"leaseAppKey"`
	MissingLocation   bool     `json:"missingLocation"`
	CoveringLocations []string `json:"coveringLocations"`
}

// staffCoveredLeases returns two sets from one pass over cafeLeaseWorkplaces:
// covered — the leases this staffer's SPECIFIC workplace reaches, the staff
// analog of residentOwnLeases (residents.go), resolved once per request and
// then intersected with whatever rows the handler was going to return; and
// unattributable — every lease flagged MissingLocation, visible to EVERY
// front-desk staffer regardless of workplace because no specific one can be
// blamed for the gap (11 café leases hit this when the 2026-08-23 duplicate-
// listing reap tombstoned their units out from under a live lease, before the
// reap script's live-tenancy guard existed, `9a3a7807`). This is a READ-only
// accommodation: require_workplace's write-side walk independently refuses a
// Charge/Settle against the same dead unit no matter what `unattributable`
// admits, so it never grants a collection capability, only the visibility to
// notice the debt and escalate it (an operator, already unrestricted, is who
// actually fixes the underlying data). Every café staff read keys on
// leaseAppKey, so these two sets, resolved once here, confine all of them.
//
// Fails CLOSED throughout. A caller with no workplace gets an empty covered
// set, not a pass. A lease whose row is missing entirely — a projection
// written before this lens existed, or one that has not converged yet — is
// simply absent from both sets and therefore denied, rather than defaulting
// to visible. Only the bucket-missing case is distinguished, and it is
// reported as an error rather than as an empty answer, so a stack where
// cafe-domain 0.11.30 has not been installed fails loudly instead of
// silently blanking every staff view.
func (s *server) staffCoveredLeases(ctx context.Context, hats subjectHats) (covered, unattributable map[string]bool, err error) {
	covered, unattributable = map[string]bool{}, map[string]bool{}
	if !hats.isFrontDesk() {
		return covered, unattributable, nil
	}
	bucket := cafedomain.LeaseWorkplacesBucket
	keys, err := s.conn.KVListKeys(ctx, bucket)
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w (is cafe-domain 0.11.30 installed and the Refractor projecting?)", bucket, err)
	}
	get := s.kvGetter(ctx, bucket)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var p leaseWorkplaceProjection
		if json.Unmarshal(raw, &p) != nil || p.LeaseAppKey == "" {
			continue
		}
		if p.MissingLocation {
			unattributable[p.LeaseAppKey] = true
			continue
		}
		if hats.covers(p.CoveringLocations) {
			covered[p.LeaseAppKey] = true
		}
	}
	return covered, unattributable, nil
}
