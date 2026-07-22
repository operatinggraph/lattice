package control_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// fakeCoreKV is a minimal in-memory coreKVGetter test double — the
// "sessionkey" op's only Core KV need is a single piiKey point-read.
type fakeCoreKV struct {
	entries map[string]*substrate.KVEntry
}

func (f *fakeCoreKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	e, ok := f.entries[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return e, nil
}

// piiKeyEntry builds a fakeCoreKV entry in the same shape the Processor's
// commit path writes for vtx.identity.<id>.piiKey (mirrors
// internal/refractor/pipeline.SecureDecryptor.readPiiKeyEnvelope's expected
// doc shape: {"data": <vault.Envelope>}).
func piiKeyEntry(t *testing.T, envelope vault.Envelope) *substrate.KVEntry {
	t.Helper()
	value, err := json.Marshal(struct {
		Data vault.Envelope `json:"data"`
	}{Data: envelope})
	require.NoError(t, err)
	return &substrate.KVEntry{Value: value}
}

// fakeSessionVault records IssueSessionKey calls and returns a fixed
// (SessionKey, err) pair. Every other Vault method is unused by the
// "sessionkey" op and panics if ever called, so a test that hits them fails
// loudly rather than silently passing on the wrong path (mirrors
// internal/privacyworker's fakeVault).
type fakeSessionVault struct {
	key SessionKeyCall
	err error

	calledWith []SessionKeyCall
}

// SessionKeyCall records one IssueSessionKey invocation's arguments.
type SessionKeyCall struct {
	IdentityKey string
	Envelope    vault.Envelope
	AspectScope string
	TTL         time.Duration
}

func (f *fakeSessionVault) CreateIdentityKey(context.Context, string) (vault.Envelope, error) {
	panic("fakeSessionVault: CreateIdentityKey not used by the sessionkey op")
}

func (f *fakeSessionVault) Encrypt(context.Context, string, vault.Envelope, []byte) (vault.Ciphertext, error) {
	panic("fakeSessionVault: Encrypt not used by the sessionkey op")
}

func (f *fakeSessionVault) Decrypt(context.Context, string, vault.Envelope, vault.Ciphertext) ([]byte, error) {
	panic("fakeSessionVault: Decrypt not used by the sessionkey op")
}

func (f *fakeSessionVault) WrapKey(context.Context, string, vault.Envelope, []byte) (vault.Ciphertext, error) {
	panic("fakeSessionVault: WrapKey not used by the sessionkey op")
}

func (f *fakeSessionVault) UnwrapKey(context.Context, string, vault.Envelope, vault.Ciphertext) ([]byte, error) {
	panic("fakeSessionVault: UnwrapKey not used by the sessionkey op")
}

func (f *fakeSessionVault) ShredKey(context.Context, string) error {
	panic("fakeSessionVault: ShredKey not used by the sessionkey op")
}

func (f *fakeSessionVault) IssueSessionKey(_ context.Context, identityKey string, envelope vault.Envelope, aspectScope string, ttl time.Duration) (vault.SessionKey, error) {
	call := SessionKeyCall{IdentityKey: identityKey, Envelope: envelope, AspectScope: aspectScope, TTL: ttl}
	f.calledWith = append(f.calledWith, call)
	if f.err != nil {
		return vault.SessionKey{}, f.err
	}
	return vault.SessionKey{Key: []byte(f.key.IdentityKey + "-dek"), ExpiresAt: time.Unix(1000, 0).UTC()}, nil
}

func (f *fakeSessionVault) MAC(context.Context, string, []byte) ([]byte, error) {
	panic("fakeSessionVault: MAC not used by the sessionkey op")
}

func TestControl_PersonalSessionKey_NotConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "AAAAAAAAAAAAAAAAAAAA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "sessionkey"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.Nil(t, resp.PersonalSessionKey)
}

func TestControl_PersonalSessionKey_MissingIdentityID_Errors(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetCoreKV(&fakeCoreKV{})
	svc.SetVault(&fakeSessionVault{})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "sessionkey"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "identityId is required")
	assert.Nil(t, resp.PersonalSessionKey)
}

func TestControl_PersonalSessionKey_NoPiiKey_Errors(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetCoreKV(&fakeCoreKV{entries: map[string]*substrate.KVEntry{}})
	svc.SetVault(&fakeSessionVault{})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "AAAAAAAAAAAAAAAAAAAA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "sessionkey"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Contains(t, resp.Error, "no piiKey aspect")
	assert.Nil(t, resp.PersonalSessionKey)
}

func TestControl_PersonalSessionKey_Success_ReturnsKey(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	envelope := vault.Envelope{KeyID: "kek-1", Alg: "aes-gcm"}
	identityKey := "vtx.identity.AAAAAAAAAAAAAAAAAAAA"
	kv := &fakeCoreKV{entries: map[string]*substrate.KVEntry{
		identityKey + ".piiKey": piiKeyEntry(t, envelope),
	}}
	fv := &fakeSessionVault{key: SessionKeyCall{IdentityKey: identityKey}}

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetCoreKV(kv)
	svc.SetVault(fv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "AAAAAAAAAAAAAAAAAAAA", AspectScope: "ssn", TTLSeconds: 60})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "sessionkey"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalSessionKey)
	assert.Equal(t, []byte(identityKey+"-dek"), resp.PersonalSessionKey.Key)
	assert.Equal(t, time.Unix(1000, 0).UTC(), resp.PersonalSessionKey.ExpiresAt)

	require.Len(t, fv.calledWith, 1)
	assert.Equal(t, identityKey, fv.calledWith[0].IdentityKey)
	assert.Equal(t, envelope, fv.calledWith[0].Envelope)
	assert.Equal(t, "ssn", fv.calledWith[0].AspectScope)
	assert.Equal(t, 60*time.Second, fv.calledWith[0].TTL)
}

func TestControl_PersonalSessionKey_Shredded_SurfacesError(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	identityKey := "vtx.identity.AAAAAAAAAAAAAAAAAAAA"
	kv := &fakeCoreKV{entries: map[string]*substrate.KVEntry{
		identityKey + ".piiKey": piiKeyEntry(t, vault.Envelope{Shredded: true}),
	}}
	fv := &fakeSessionVault{err: vault.ErrKeyShredded}

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetCoreKV(kv)
	svc.SetVault(fv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "AAAAAAAAAAAAAAAAAAAA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "sessionkey"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Equal(t, vault.ErrKeyShredded.Error(), resp.Error)
	assert.Nil(t, resp.PersonalSessionKey, "a shredded identity's ciphertext deltas must never decrypt — no key returned")
}

// TestControl_PersonalSessionKey_VerifiedActorOverridesBodyIdentity is the
// EDGE.4 twin of TestControl_PersonalHydrate_VerifiedActorOverridesBodyIdentity
// (per-identity-nats-subscribe-acl-design.md §3.4 vector 8): the highest-value
// proof for this op — a verified actor A can never mint a session key (and so
// never the raw DEK) for another identity B by naming B in the request body.
func TestControl_PersonalSessionKey_VerifiedActorOverridesBodyIdentity(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	identityKeyA := "vtx.identity." + identityBindingTestA
	kv := &fakeCoreKV{entries: map[string]*substrate.KVEntry{
		identityKeyA + ".piiKey": piiKeyEntry(t, vault.Envelope{KeyID: "kek-1"}),
	}}
	fv := &fakeSessionVault{}

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetCoreKV(kv)
	svc.SetVault(fv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	tokenForA := sign(identityBindingTestA)
	msg := personalRequestMsg(t, control.ControlSubject("personal", "sessionkey"), tokenForA,
		control.ControlRequest{IdentityID: identityBindingTestB})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "a body identityId that disagrees with the verified actor must be rejected")
	assert.Nil(t, resp.PersonalSessionKey)
	assert.Empty(t, fv.calledWith, "the vault must never be asked to mint a session key for the unauthorized body identity")
}

// TestControl_PersonalSessionKey_VerifiedActorFillsEmptyBodyIdentity proves
// the override applies even when the body omits identityId — the verified
// actor is the sole source, and the caller correctly receives its own key.
func TestControl_PersonalSessionKey_VerifiedActorFillsEmptyBodyIdentity(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	av, sign := newIdentityBindingVerifier(t)

	identityKeyA := "vtx.identity." + identityBindingTestA
	kv := &fakeCoreKV{entries: map[string]*substrate.KVEntry{
		identityKeyA + ".piiKey": piiKeyEntry(t, vault.Envelope{KeyID: "kek-1"}),
	}}
	fv := &fakeSessionVault{key: SessionKeyCall{IdentityKey: identityKeyA}}

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetActorVerifier(av)
	svc.SetCoreKV(kv)
	svc.SetVault(fv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	msg := personalRequestMsg(t, control.ControlSubject("personal", "sessionkey"), sign(identityBindingTestA),
		control.ControlRequest{})
	reply, err := nc.RequestMsg(msg, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalSessionKey)
	require.Len(t, fv.calledWith, 1)
	assert.Equal(t, identityKeyA, fv.calledWith[0].IdentityKey)
}
