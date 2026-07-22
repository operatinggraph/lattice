package control_test

import (
	"context"
	"crypto"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
)

// identityBindingTestA/B are valid-shaped (20-char, canonical-alphabet)
// NanoIDs — auth.ModeNanoID rejects a subject that isn't one, so the
// mismatch/override assertions below must use real NanoID shapes to
// exercise the §3.4 binding logic itself, not incidental token rejection.
const (
	identityBindingTestA = "AAAAAAAAAAAAAAAAAAAA"
	identityBindingTestB = "BBBBBBBBBBBBBBBBBBBB"
)

// newIdentityBindingVerifier builds an ActorVerifier whose tokens resolve to
// vtx.identity.<sub> — the same construction actor_verifier_test.go uses.
func newIdentityBindingVerifier(t *testing.T) (*controlauth.ActorVerifier, func(sub string) string) {
	t.Helper()
	priv := newVerifierTestRSAKey(t)
	verifier, err := auth.NewVerifier(auth.Config{
		Keys:    map[string]crypto.PublicKey{verifierTestKID: &priv.PublicKey},
		KeyInfo: map[string]auth.KeyInfo{verifierTestKID: {Spec: auth.BindingSpec{Mode: auth.ModeNanoID}}},
	})
	require.NoError(t, err)
	av := controlauth.NewActorVerifier(auth.NewAuthenticator(verifier, nil))
	sign := func(sub string) string { return signVerifierTestToken(t, priv, sub) }
	return av, sign
}

func personalRequestMsg(t *testing.T, subject, token string, body control.ControlRequest) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	msg := &nats.Msg{Subject: subject, Data: data}
	if token != "" {
		msg.Header = nats.Header{}
		msg.Header.Set(controlauth.HeaderActor, token)
	}
	return msg
}

// TestControl_PersonalHydrate_VerifiedActorOverridesBodyIdentity is vector 8
// (per-identity-nats-subscribe-acl-design.md §8): personal.hydrate with a
// verified actor A and a body claiming B is served as A, never B — the
// payload field is display/debug, never authority, once an ActorVerifier is
// configured (§3.4).
func TestControl_PersonalHydrate_VerifiedActorOverridesBodyIdentity(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	h := &fakeHydrator{revision: 42}
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetPersonalHydrator(h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	tokenForA := sign(identityBindingTestA)
	msg := personalRequestMsg(t, control.ControlSubject("personal", "hydrate"), tokenForA,
		control.ControlRequest{IdentityID: identityBindingTestB})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "a body identityId that disagrees with the verified actor must be rejected")
	assert.Empty(t, h.calledWith, "the hydrator must never run against the unauthorized body identity")
}

// TestControl_PersonalHydrate_VerifiedActorFillsEmptyBodyIdentity proves the
// override applies even when the body omits identityId entirely — the
// verified actor is the sole source, not a fallback for a missing field.
func TestControl_PersonalHydrate_VerifiedActorFillsEmptyBodyIdentity(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	h := &fakeHydrator{revision: 42}
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetPersonalHydrator(h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	msg := personalRequestMsg(t, control.ControlSubject("personal", "hydrate"), sign(identityBindingTestA),
		control.ControlRequest{})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	assert.Equal(t, []string{identityBindingTestA}, h.calledWith)
}

// TestControl_PersonalRegister_VerifiedActorOverridesBodyIdentity proves the
// same binding on personal.register — the Interest Set write lands under
// the verified actor, not a client-declared identityId.
func TestControl_PersonalRegister_VerifiedActorOverridesBodyIdentity(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	kv := makeKV(t, nc, js, "refractor-test-personal-identity-binding-register")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetPersonalInterestKV(kv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	msg := personalRequestMsg(t, control.ControlSubject("personal", "register"), sign(identityBindingTestA),
		control.ControlRequest{IdentityID: identityBindingTestB, DeviceID: "deviceX", Types: []string{"lease"}})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.NotEmpty(t, resp.Error, "a body identityId that disagrees with the verified actor must be rejected")

	key, err := personalinterest.Key(identityBindingTestB, "deviceX")
	require.NoError(t, err)
	_, err = kv.Get(ctx, key)
	assert.Error(t, err, "the rejected register must not have written under the unauthorized body identity")

	keyA, err := personalinterest.Key(identityBindingTestA, "deviceX")
	require.NoError(t, err)
	_, err = kv.Get(ctx, keyA)
	assert.Error(t, err, "the rejected register must not have written under the verified actor either")
}

// TestControl_PersonalDeregister_VerifiedActorOverridesBodyIdentity proves
// the same binding on personal.deregister — a verified actor A cannot
// deregister a device under a client-declared identity B, and A's own
// registration is left untouched by the rejected call.
func TestControl_PersonalDeregister_VerifiedActorOverridesBodyIdentity(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	kv := makeKV(t, nc, js, "refractor-test-personal-identity-binding-deregister")
	require.NoError(t, personalinterest.Register(context.Background(), kv, identityBindingTestA, "deviceX", []string{"lease"}, nil, "2026-07-11T00:00:00Z"))

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetPersonalInterestKV(kv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	msg := personalRequestMsg(t, control.ControlSubject("personal", "deregister"), sign(identityBindingTestA),
		control.ControlRequest{IdentityID: identityBindingTestB, DeviceID: "deviceX"})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.NotEmpty(t, resp.Error, "a body identityId that disagrees with the verified actor must be rejected")

	keyA, err := personalinterest.Key(identityBindingTestA, "deviceX")
	require.NoError(t, err)
	_, err = kv.Get(context.Background(), keyA)
	assert.NoError(t, err, "the rejected deregister must leave the verified actor's own registration intact")
}

// TestControl_PersonalHydrate_NoVerifierPreservesSelfAssertedBody proves Fire
// 1 behavior is unchanged when no ActorVerifier is configured (dev/e2e
// fixtures): the body's identityId is trusted as-is.
func TestControl_PersonalHydrate_NoVerifierPreservesSelfAssertedBody(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	h := &fakeHydrator{revision: 7}
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityZ"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	assert.Equal(t, []string{"identityZ"}, h.calledWith)
}
