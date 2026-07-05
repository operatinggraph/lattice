package control_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/refractor/control"
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
// wire for Refractor: with an ActorVerifier configured, a signed actor JWT on
// the HeaderActor value resolves to the verified vtx.identity.<sub> before
// capability.Authorize runs.
func TestControl_ActorVerifier_ValidTokenResolvesVerifiedActor(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	priv := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey}})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	svc := control.NewService()
	rec := &recordingCapability{}
	svc.SetCapabilityChecker(rec)
	svc.SetActorVerifier(av)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	token := signVerifierTestToken(t, priv, "opNanoID1")
	_, err = nc.RequestMsg(controlauth.NewActorRequestMsg(subj, token), 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "vtx.identity.opNanoID1", rec.actor(), "capability.Authorize must see the VERIFIED actor id, never the raw token")
}

// TestControl_ActorVerifier_NoTokenDeniesBeforeCapabilityRead proves an
// anonymous request is denied once an ActorVerifier is configured, and that
// capability.Authorize never runs — verification precedes the capability
// read.
func TestControl_ActorVerifier_NoTokenDeniesBeforeCapabilityRead(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	priv := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey}})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))

	svc := control.NewService()
	rec := &recordingCapability{}
	svc.SetCapabilityChecker(rec)
	svc.SetActorVerifier(av)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	reply, err := nc.Request(subj, nil, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "an anonymous request must be denied once verified-actor mode is configured")
	assert.Equal(t, 0, rec.callCount(), "capability.Authorize must never run when actor verification fails")
}
