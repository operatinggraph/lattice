// Package controlauth is the shared home for control-plane request
// authentication + authorization (control-plane-capability-authz-design.md).
// Fire 1a added the actor-on-the-wire header (header.go); Fire 1b added the
// capability checker (checker.go). Fire 2 (this file) lifts the actor from
// self-asserted to verified: the same signed-JWT seam the read path uses
// (internal/gateway/auth), reused here rather than re-derived — "same JWT,
// same trust model" (design §3.4(d)).
package controlauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/micro"

	"github.com/asolgan/lattice/internal/gateway/auth"
)

// ErrNoToken is returned by ResolveActor when an ActorVerifier is configured
// but the request carries no HeaderActor value to verify.
var ErrNoToken = errors.New("controlauth: no actor token asserted")

// ActorVerifier lifts the control plane's HeaderActor value from a
// self-asserted actor key to a signed JWT: the header carries the token, not
// the bare key, and ResolveActor verifies it (signature, time, issuer,
// audience) and checks the revocation kill-switch before any capability read
// runs. Built once per control-service process from the same trust root the
// Gateway's read path uses.
type ActorVerifier struct {
	authn *auth.Authenticator
}

// NewActorVerifier wraps an already-built *auth.Authenticator (Verifier +
// optional revocation checker) as an ActorVerifier. A nil argument is not
// meaningful — callers wire a *ActorVerifier onto a Service only when
// verification is actually configured; an unconfigured deployment passes a
// nil *ActorVerifier to ResolveActor instead (Fire 1 behavior).
func NewActorVerifier(authn *auth.Authenticator) *ActorVerifier {
	return &ActorVerifier{authn: authn}
}

// ResolveActor returns the control-plane actor id authorized for req.
//
// verifier == nil preserves Fire 1: the HeaderActor value is trusted as
// asserted (ActorFromRequest, no verification) — the deployment has not
// configured a JWT trust root, so nothing about existing behavior changes
// (no flag-day lockout, mirrors the design's R2 mitigation).
//
// verifier != nil treats the HeaderActor value as a signed JWT: it is
// verified and checked against the revocation kill-switch (D1's
// internal/gateway/auth.Authenticator) BEFORE any capability read runs. Any
// failure — missing token, bad signature, expired, revoked — denies with a
// non-nil error and an empty actor; the caller must treat that identically
// to a capability-check denial and never fall back to the raw header value.
func ResolveActor(ctx context.Context, req micro.Request, verifier *ActorVerifier) (string, error) {
	if verifier == nil {
		return ActorFromRequest(req), nil
	}
	token := ActorFromRequest(req)
	if token == "" {
		return "", ErrNoToken
	}
	actor, err := verifier.authn.Authenticate(ctx, token)
	if err != nil {
		return "", fmt.Errorf("controlauth: verify actor token: %w", err)
	}
	return actor.ActorID, nil
}
