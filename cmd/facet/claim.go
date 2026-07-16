package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/asolgan/lattice/internal/edge/agent"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// maxClaimBodyBytes bounds POST /api/claim's request body, mirroring
// cmd/loftspace-app/server.go's requireBody 1 MiB cap.
const maxClaimBodyBytes = 1 << 20

// claimRetryBackoffs is isTransientAuthLag's bounded backoff schedule,
// ported verbatim from cmd/loftspace-app/web/app.js's retryBackoffsMs
// (~3s total): the freshly-minted device credential's ProvisionConsumerIdentity
// auto-provision commits to Core KV synchronously, but the CapabilityAuthorizer
// reads an async-projected Capability Lens, so the very next request (this
// ClaimIdentity submit) can race ahead of that projection.
var claimRetryBackoffs = []time.Duration{
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
}

// isTransientAuthLag mirrors app.js's helper of the same name: a rejection
// is the known, architecturally-expected async-projection race — not a
// genuine, persistent denial — only for these two AuthDenied reasons.
func isTransientAuthLag(reply *processor.OperationReply) bool {
	if reply == nil || reply.Status != processor.ReplyStatusRejected || reply.Error == nil {
		return false
	}
	if reply.Error.Code != processor.ErrCodeAuthDenied {
		return false
	}
	reason, _ := reply.Error.Details["reason"].(string)
	return reason == "NoCapabilityEntry" || reason == "OperationNotPermitted"
}

// edge-showcase-app-design.md §7.1 (Fire 3 build note): the claim ceremony's
// only demo-posture credential need is a throwaway device JWT to authenticate
// the ClaimIdentity raw-credential carve-out — the same shared-dev-key
// stand-in cmd/loftspace-app/readauth.go and cmd/clinic-app already use for
// their read boundaries, applied here instead to a write. Nil unless
// FACET_DEV_AUTH is enabled; a nil signer disables /api/claim (404), the
// same fail-closed default as the other apps' dev-auth posture.
type devSigner struct {
	priv *rsa.PrivateKey
	kid  string
	ttl  time.Duration
	now  func() time.Time
}

const devTokenTTL = 30 * time.Minute

func (d *devSigner) mint(subject string) (string, error) {
	now := d.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(d.ttl)),
	})
	tok.Header["kid"] = d.kid
	return tok.SignedString(d.priv)
}

// setupDevSigner mirrors readauth.go's DEMO posture (same shared dev key,
// same loopback defense-in-depth) minus its PRODUCTION verify-only branch:
// Facet has no read boundary to configure, and a deployed IdP issuing real
// tokens has no Facet-side counterpart to wire — the app never signs on an
// external IdP's behalf, same as readauth.go's package doc.
func setupDevSigner(logger *slog.Logger, loopback bool) (*devSigner, error) {
	if !isTruthy(os.Getenv("FACET_DEV_AUTH")) {
		return nil, nil
	}
	if !loopback {
		return nil, fmt.Errorf("FACET_DEV_AUTH is only permitted on a loopback bind (the in-process minter trusts any subject)")
	}
	priv, err := auth.LoadDevSigningKey(os.Getenv("FACET_DEV_PRIVATE_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("dev-auth: load shared dev signing key: %w", err)
	}
	logger.Warn("FACET_DEV_AUTH ENABLED: /api/claim mints demo JWTs in-process (NOT for production)")
	return &devSigner{priv: priv, kid: auth.DevKeyID, ttl: devTokenTTL, now: time.Now}, nil
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// isLoopbackHost mirrors cmd/loftspace-app/main.go's helper of the same
// name. An empty host (the bare ":7810" form) means all interfaces and is
// NOT loopback — fail safe.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return host
}

// claimRequest is what the browser POSTs to /api/claim (facet-app-ux.md
// §3.7's claim/link entry, Fire 3). Facet mints its own throwaway device
// credential server-side — the browser never sees a Gateway URL or a bearer
// token, matching server.go's "the browser only ever talks to this
// process's own localhost HTTP surface" invariant.
type claimRequest struct {
	TargetIdentityKey string `json:"targetIdentityKey"`
	ClaimKey          string `json:"claimKey"`
}

// handleClaim implements POST /api/claim: mints a bare device credential and
// submits ClaimIdentity through the Gateway as that credential — the
// raw-credential carve-out (gateway-claim-flow-identity-provisioning-
// design.md §11.0), mirroring cmd/loftspace-app/web/app.js's
// runClaimCeremony wire shape verbatim (authContext.target == the minted
// credential's own key; payload.targetIdentityKey == the identity being
// claimed, which is the op's real subject — see identity-domain's
// ClaimIdentity DDL ExpectedOutcome) including its bounded
// isTransientAuthLag retry for the fresh credential's own capability-grant
// projection race.
func (s *server) handleClaim(w http.ResponseWriter, r *http.Request) {
	if s.devSigner == nil {
		s.writeError(w, http.StatusNotFound, "claim is disabled (FACET_DEV_AUTH not set)")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req claimRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClaimBodyBytes)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
		return
	}
	targetKey := strings.TrimSpace(req.TargetIdentityKey)
	claimKey := strings.TrimSpace(req.ClaimKey)
	if targetKey == "" || claimKey == "" {
		s.writeError(w, http.StatusBadRequest, "targetIdentityKey and claimKey are both required")
		return
	}

	deviceBareID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate device credential: "+err.Error())
		return
	}
	token, err := s.devSigner.mint(deviceBareID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "mint device credential: "+err.Error())
		return
	}
	deviceKey := "vtx.identity." + deviceBareID

	payload, err := json.Marshal(map[string]any{"targetIdentityKey": targetKey, "claimKey": claimKey})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "marshal claim payload: "+err.Error())
		return
	}
	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "generate requestId: "+err.Error())
		return
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Class:         "identity",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: deviceKey},
		ContextHint:   &processor.ContextHint{Reads: []string{targetKey, targetKey + ".state", targetKey + ".claimKey"}},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	submitter := &agent.GatewaySubmitter{URL: s.gatewayURL, Token: token}

	var reply *processor.OperationReply
	for attempt := 0; ; attempt++ {
		reply, err = submitter.Submit(ctx, env)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "claim failed: "+err.Error())
			return
		}
		if !isTransientAuthLag(reply) || attempt >= len(claimRetryBackoffs) {
			break
		}
		select {
		case <-time.After(claimRetryBackoffs[attempt]):
		case <-ctx.Done():
			s.writeError(w, http.StatusGatewayTimeout, "claim timed out waiting for the fresh device credential's capability grant to project")
			return
		}
	}
	if reply.Status != processor.ReplyStatusAccepted {
		msg := "rejected"
		if reply.Error != nil {
			msg = string(reply.Error.Code) + ": " + reply.Error.Message
		}
		s.writeError(w, http.StatusUnprocessableEntity, "claim rejected: "+msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"claimedIdentityKey": targetKey,
		"credentialKey":      deviceKey,
	})
}
