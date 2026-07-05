package control_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/weaver/control"
)

const verifierTestKID = "test-key-1"

func newVerifierTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return k
}

func signVerifierTestToken(t *testing.T, priv *rsa.PrivateKey, sub string) string {
	t.Helper()
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   sub,
		IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = verifierTestKID
	s, err := tok.SignedString(priv)
	require.NoError(t, err)
	return s
}

// TestControl_ActorVerifier_ValidTokenResolvesVerifiedActor proves Fire 2's
// end-to-end wire: with an ActorVerifier configured, a signed actor JWT on
// the HeaderActor value resolves to the verified vtx.identity.<sub> — not
// the raw token string — before capability.Authorize runs.
func TestControl_ActorVerifier_ValidTokenResolvesVerifiedActor(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	priv := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey}})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	svc.SetActorVerifier(av)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	token := signVerifierTestToken(t, priv, "opNanoID1")
	reply, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.ListSubject(), token), 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Equal(t, "vtx.identity.opNanoID1", rec.actor(), "capability.Authorize must see the VERIFIED actor id, never the raw token")
	assert.Empty(t, resp.Error)
}

// TestControl_ActorVerifier_NoTokenDeniesBeforeCapabilityRead proves an
// anonymous request (no HeaderActor value) is denied once an ActorVerifier
// is configured, and — critically — that capability.Authorize is never
// invoked: verification runs strictly before the capability read (the
// design's explicit ordering).
func TestControl_ActorVerifier_NoTokenDeniesBeforeCapabilityRead(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	priv := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey}})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	svc.SetActorVerifier(av)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ListSubject())
	assert.NotEmpty(t, resp.Error, "an anonymous request must be denied once verified-actor mode is configured")
	assert.Equal(t, "", rec.actor(), "capability.Authorize must never run when actor verification fails")
}

// TestControl_ActorVerifier_ForgedTokenDenies proves a token signed by a key
// the verifier does not trust is denied, not silently accepted as the raw
// header value (guards against a regression to Fire 1's self-asserted
// behavior once a verifier IS configured).
func TestControl_ActorVerifier_ForgedTokenDenies(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	priv := newVerifierTestRSAKey(t)
	untrusted := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey}})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	svc.SetActorVerifier(av)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	forged := signVerifierTestToken(t, untrusted, "attacker")
	reply, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.ListSubject(), forged), 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.False(t, strings.Contains(resp.Error, "attacker"), "a forged token must never resolve to any actor id")
	assert.Equal(t, "", rec.actor())
}
