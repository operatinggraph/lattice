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
)

// cafe-app has no protected Postgres read boundary (every café lens is plain
// NATS-KV, P5) — the read boundary here is SESSION-scoped instead
// (persona-worlds-design.md Fire W4 §3): a resident may see only rows for
// leases whose bookerKey is their own signed-in identity, and a `worksAt`
// staffer sees the house, including the front-desk staff surface. Every read
// handler resolves the caller through authenticateRead or resolveSubjectHats,
// mirroring cmd/clinic-app's own authenticateRead read boundary.

const actorFetchTimeout = 10 * time.Second

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

// subjectHats is the caller's read-boundary role, resolved from the
// signed-in session: isStaff marks a `worksAt` anchor (sees the house,
// including the front-desk staff surface); otherwise the caller is a
// resident, scoped to their own lease's rows.
type subjectHats struct {
	identityID string
	isStaff    bool
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
	anchors, err := s.fetchActorAnchors(r.Context(), token)
	if err != nil {
		return subjectHats{}, fmt.Errorf("resolve session role from the Gateway: %w", err)
	}
	staff := false
	for _, a := range anchors {
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
			staff = true
			break
		}
	}
	return subjectHats{identityID: identityID, isStaff: staff}, nil
}

// actorAnchorsResponse decodes the Gateway's GET /v1/actor body far enough to
// read the anchors array; the response also carries actorId/roles, which
// this read boundary does not need.
type actorAnchorsResponse struct {
	Anchors []appsession.ActorAnchor `json:"anchors"`
}

// fetchActorAnchors asks the Gateway which residence/workplace anchors
// token's actor carries.
func (s *server) fetchActorAnchors(ctx context.Context, token string) ([]appsession.ActorAnchor, error) {
	ctx, cancel := context.WithTimeout(ctx, actorFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.gatewayURL+"/v1/actor", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway /v1/actor: HTTP %d", resp.StatusCode)
	}
	var body actorAnchorsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Anchors, nil
}
