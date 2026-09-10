package vault_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/vault"
)

// startTestServer starts an in-memory JetStream-enabled NATS server and
// returns a connected *nats.Conn.
func startTestServer(t *testing.T) *nats.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
	t.Cleanup(nc.Close)
	return nc
}

func sendDecrypt(t *testing.T, nc *nats.Conn, req vault.DecryptRequest) vault.DecryptResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.DecryptSubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.DecryptSubject)
	var resp vault.DecryptResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

func TestService_Decrypt_RoundTrip(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.ReveaLHoLderAAAAAAAA")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "vtx.identity.ReveaLHoLderAAAAAAAA", env, []byte("123-45-6789"))
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		KeyHolderKey: "vtx.identity.ReveaLHoLderAAAAAAAA",
		Envelope:     env,
		Ciphertext:   ct,
	})

	require.Empty(t, resp.Error)
	assert.Equal(t, []byte("123-45-6789"), resp.Plaintext)
}

func TestService_Decrypt_ShreddedIdentity_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.ShredHoLderAAAAAAAAA")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "vtx.identity.ShredHoLderAAAAAAAAA", env, []byte("pii"))
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "vtx.identity.ShredHoLderAAAAAAAAA"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		KeyHolderKey: "vtx.identity.ShredHoLderAAAAAAAAA",
		Envelope:     env,
		Ciphertext:   ct,
	})

	// The refusal must be the shred, not the holder-kind gate ahead of it: a
	// malformed identity key would also be refused, for the wrong reason.
	assert.Equal(t, vault.ErrKeyShredded.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

func TestService_Decrypt_MissingIdentityKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{})
	require.NotEmpty(t, resp.Error)
}

// TestService_Decrypt_NonIdentityHolder_Denied pins the Reveal rule (Contract
// #3 §3.10): the wholesale RPC carries no actor and no purpose, so a record
// sealed under any holder that is not an identity is refused with
// ErrRevealDenied before a key is touched — a shredded retention class answers
// the same way, so the refusal tells nothing about the class's key state, and
// a holder that is not even a vertex key is refused rather than admitted.
func TestService_Decrypt_NonIdentityHolder_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	for _, tc := range []struct {
		name   string
		holder string
		shred  bool
	}{
		{name: "live retention-class holder", holder: "vtx.retentionclass.RetentionCLassAAAAAA"},
		{name: "shredded retention-class holder", holder: "vtx.retentionclass.RetentionShredAAAAAA", shred: true},
		{name: "holder that is not a vertex key", holder: "identity-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := backend.CreateIdentityKey(ctx, tc.holder)
			require.NoError(t, err)
			ct, err := backend.Encrypt(ctx, tc.holder, env, []byte("retained"))
			require.NoError(t, err)
			if tc.shred {
				require.NoError(t, backend.ShredKey(ctx, tc.holder))
			}
			resp := sendDecrypt(t, nc, vault.DecryptRequest{KeyHolderKey: tc.holder, Envelope: env, Ciphertext: ct})
			assert.Equal(t, vault.ErrRevealDenied.Error(), resp.Error)
			assert.Empty(t, resp.Plaintext)
		})
	}
}

func TestService_StartNATSListener_DoubleStartRejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))
	require.Error(t, svc.StartNATSListener(ctx, nc))
}

func TestDecryptSubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.vault.decrypt", vault.DecryptSubject)
}

func TestWrapUnwrapKeySubjects_Exact(t *testing.T) {
	assert.Equal(t, "lattice.vault.wrapkey", vault.WrapKeySubject)
	assert.Equal(t, "lattice.vault.unwrapkey", vault.UnwrapKeySubject)
}

func sendWrapKey(t *testing.T, nc *nats.Conn, req vault.WrapKeyRequest) vault.WrapKeyResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.WrapKeySubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.WrapKeySubject)
	var resp vault.WrapKeyResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

func sendUnwrapKey(t *testing.T, nc *nats.Conn, req vault.UnwrapKeyRequest) vault.UnwrapKeyResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.UnwrapKeySubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.UnwrapKeySubject)
	var resp vault.UnwrapKeyResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

func TestService_WrapUnwrapKey_RoundTrip(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.BLobHoLderAAAAAAAAAA")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	cek := []byte("0123456789abcdef0123456789abcdef") // 32 bytes (a per-object CEK)
	wrapResp := sendWrapKey(t, nc, vault.WrapKeyRequest{
		KeyHolderKey: "vtx.identity.BLobHoLderAAAAAAAAAA",
		Envelope:     env,
		Key:          cek,
	})
	require.Empty(t, wrapResp.Error)
	assert.NotEqual(t, cek, wrapResp.Ciphertext.CT, "wrapped CEK must not equal the plaintext CEK")

	unwrapResp := sendUnwrapKey(t, nc, vault.UnwrapKeyRequest{
		KeyHolderKey: "vtx.identity.BLobHoLderAAAAAAAAAA",
		Envelope:     env,
		Wrapped:      wrapResp.Ciphertext,
	})
	require.Empty(t, unwrapResp.Error)
	assert.Equal(t, cek, unwrapResp.Key)
}

func TestService_UnwrapKey_ShreddedIdentity_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.BLobHoLderAAAAAAAAAA")
	require.NoError(t, err)
	cek := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := backend.WrapKey(context.Background(), "vtx.identity.BLobHoLderAAAAAAAAAA", env, cek)
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "vtx.identity.BLobHoLderAAAAAAAAAA"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendUnwrapKey(t, nc, vault.UnwrapKeyRequest{
		KeyHolderKey: "vtx.identity.BLobHoLderAAAAAAAAAA",
		Envelope:     env,
		Wrapped:      wrapped,
	})
	// The refusal must be the shred, not the holder-kind gate ahead of it.
	assert.Equal(t, vault.ErrKeyShredded.Error(), resp.Error)
	assert.Empty(t, resp.Key)
}

func TestService_WrapKey_MissingKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.BLobHoLderAAAAAAAAAA")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendWrapKey(t, nc, vault.WrapKeyRequest{KeyHolderKey: "vtx.identity.BLobHoLderAAAAAAAAAA", Envelope: env})
	require.NotEmpty(t, resp.Error)
}

// TestService_WrapUnwrapKey_NonIdentityHolder_Denied pins the object plane's
// holder rule (Contract #3 §3.11) at both RPCs — and, for unwrap, the bypass it
// closes: an unwrap is a decrypt, so a sensitive aspect's ciphertext sealed
// under a retention-class holder, presented to UnwrapKeySubject under that
// holder, must be refused exactly as DecryptSubject refuses it.
func TestService_WrapUnwrapKey_NonIdentityHolder_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	const holder = "vtx.retentionclass.RetentionCLassAAAAAA"
	env, err := backend.CreateIdentityKey(ctx, holder)
	require.NoError(t, err)

	wrapResp := sendWrapKey(t, nc, vault.WrapKeyRequest{KeyHolderKey: holder, Envelope: env, Key: []byte("0123456789abcdef0123456789abcdef")})
	assert.Equal(t, vault.ErrHolderNotIdentity.Error(), wrapResp.Error)
	assert.Empty(t, wrapResp.Ciphertext.CT)

	retained, err := backend.Encrypt(ctx, holder, env, []byte("chart note"))
	require.NoError(t, err)
	unwrapResp := sendUnwrapKey(t, nc, vault.UnwrapKeyRequest{KeyHolderKey: holder, Envelope: env, Wrapped: retained})
	assert.Equal(t, vault.ErrHolderNotIdentity.Error(), unwrapResp.Error)
	assert.Empty(t, unwrapResp.Key)
}

func TestService_WrapKey_MissingIdentityKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendWrapKey(t, nc, vault.WrapKeyRequest{Key: []byte("k")})
	require.NotEmpty(t, resp.Error)
}

func TestIssueSessionKeySubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.vault.issuesessionkey", vault.IssueSessionKeySubject)
}

func sendIssueSessionKey(t *testing.T, nc *nats.Conn, req vault.IssueSessionKeyRequest) vault.IssueSessionKeyResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.IssueSessionKeySubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.IssueSessionKeySubject)
	var resp vault.IssueSessionKeyResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

// TestService_IssueSessionKey_ReturnsTheDEK proves the Personal Lens Fire 5
// happy path (personal-secure-lens-design.md §3.6): the Edge asks the cloud
// for a transient session key and gets back the same DEK Decrypt/UnwrapKey
// use, so it can open ciphertext deltas locally.
func TestService_IssueSessionKey_ReturnsTheDEK(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.SessionHoLderAAAAAAA")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{
		KeyHolderKey: "vtx.identity.SessionHoLderAAAAAAA",
		Envelope:     env,
		AspectScope:  "lease",
		TTLSeconds:   60,
	})
	require.Empty(t, resp.Error)
	require.NotEmpty(t, resp.Key)
	assert.True(t, resp.ExpiresAt.After(time.Now()), "ExpiresAt must be in the future")

	// The issued key is the same DEK Decrypt uses under the hood — an Edge
	// holding it can open a ciphertext delta locally with plain AES-GCM.
	directDEK, err := backend.IssueSessionKey(context.Background(), "vtx.identity.SessionHoLderAAAAAAA", env, "lease", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, directDEK.Key, resp.Key)
}

// TestService_IssueSessionKey_ShreddedIdentity_Denied is Gate-3 vector 5
// (personal-secure-lens-design.md §5): once an identity is shredded, the
// Vault must refuse to mint any further session key for it — the Edge can
// never freshly decrypt that identity's ciphertext deltas again.
func TestService_IssueSessionKey_ShreddedIdentity_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	env, err := backend.CreateIdentityKey(context.Background(), "vtx.identity.SessionHoLderAAAAAAA")
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "vtx.identity.SessionHoLderAAAAAAA"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{
		KeyHolderKey: "vtx.identity.SessionHoLderAAAAAAA",
		Envelope:     env,
		TTLSeconds:   60,
	})
	// The refusal must be the shred, not the holder-kind gate ahead of it.
	assert.Equal(t, vault.ErrKeyShredded.Error(), resp.Error)
	assert.Empty(t, resp.Key)
}

// TestService_IssueSessionKey_NonIdentityHolder_Denied: only an identity has
// a personal-lens session to hand its DEK to; a retention-class holder is
// refused before any key is touched.
func TestService_IssueSessionKey_NonIdentityHolder_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	const holder = "vtx.retentionclass.RetentionCLassAAAAAA"
	env, err := backend.CreateIdentityKey(ctx, holder)
	require.NoError(t, err)
	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{KeyHolderKey: holder, Envelope: env, AspectScope: "lease", TTLSeconds: 60})
	assert.Equal(t, vault.ErrHolderNotIdentity.Error(), resp.Error)
	assert.Empty(t, resp.Key)
}

func TestService_IssueSessionKey_MissingIdentityKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{})
	require.NotEmpty(t, resp.Error)
}

func TestDecryptRefSubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.vault.decryptref", vault.DecryptRefSubject)
}

func sendDecryptRef(t *testing.T, nc *nats.Conn, req vault.DecryptRefRequest) vault.DecryptRefResponse {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	reply, err := nc.Request(vault.DecryptRefSubject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", vault.DecryptRefSubject)
	var resp vault.DecryptRefResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

// mintRefFixture wires a backend + service and mints a valid, MAC'd
// sensitive-ref for identityKey — the "everything genuine" starting point
// each reject-table test mutates one field away from.
type mintRefFixture struct {
	backend   *vault.LocalBackend
	nc        *nats.Conn
	ref       string
	requestID string
	envelope  vault.Envelope
	ct        vault.Ciphertext
	mac       []byte
}

// testRefIdentityKey is a valid 20-char limited-alphabet NanoID (CLAUDE.md's
// seed-data convention) so substrate.ParseAspectKey accepts the fixture's
// synthesized aspect key.
const testRefIdentityKey = "Hj4kPmRtw9nbCxz5vQ2y"

func mintValidRef(t *testing.T, ctx context.Context, nc *nats.Conn, backend *vault.LocalBackend, plaintext []byte) mintRefFixture {
	t.Helper()
	identityKey := "vtx.identity." + testRefIdentityKey
	ref := identityKey + ".ssn"
	requestID := "req-1"

	env, err := backend.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)
	ct, err := backend.Encrypt(ctx, identityKey, env, plaintext)
	require.NoError(t, err)
	mac, err := backend.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(ref, requestID, ct))
	require.NoError(t, err)

	return mintRefFixture{backend: backend, nc: nc, ref: ref, requestID: requestID, envelope: env, ct: ct, mac: mac}
}

// TestService_DecryptRef_RoundTrip proves the genuine happy path: a
// correctly minted marker verifies and decrypts.
func TestService_DecryptRef_RoundTrip(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("123-45-6789"))
	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: f.ref, RequestID: f.requestID, Envelope: f.envelope, Ciphertext: f.ct, MAC: f.mac,
	})
	require.Empty(t, resp.Error)
	assert.Equal(t, []byte("123-45-6789"), resp.Plaintext)
}

// TestService_DecryptRef_MissingMAC_Denied: an absent MAC — a pre-MAC or
// deliberately stripped marker — is refused before any decrypt attempt
// (design §3.4's bridge-side "require mac" fail-closed, mirrored here at the
// Vault-side verify boundary too).
func TestService_DecryptRef_MissingMAC_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("pii"))
	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: f.ref, RequestID: f.requestID, Envelope: f.envelope, Ciphertext: f.ct,
	})
	require.Equal(t, vault.ErrRefUnverified.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_ForgedRef_Denied: a fabricated marker naming a
// second identity's aspect — the exact attack this design closes — fails
// MAC verification because the MAC binds `ref` itself.
func TestService_DecryptRef_ForgedRef_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("pii"))
	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: "vtx.identity.St6mP3qBn4rT8wYxK7Vc.ssn", RequestID: f.requestID, Envelope: f.envelope, Ciphertext: f.ct, MAC: f.mac,
	})
	require.Equal(t, vault.ErrRefUnverified.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_SplicedCiphertext_Denied: a marker's ciphertext
// swapped for a different one (harvested from another aspect) fails
// verification — the MAC binds `ciphertext`, not just `ref`.
func TestService_DecryptRef_SplicedCiphertext_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("pii"))
	otherCT, err := backend.Encrypt(ctx, "vtx.identity."+testRefIdentityKey, f.envelope, []byte("different plaintext"))
	require.NoError(t, err)

	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: f.ref, RequestID: f.requestID, Envelope: f.envelope, Ciphertext: otherCT, MAC: f.mac,
	})
	require.Equal(t, vault.ErrRefUnverified.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_WrongRequestID_Denied: a marker replayed under a
// different requestId than the one it was minted for fails verification —
// the splice-resistance binding (design §3.2), even though ref+ciphertext
// are both genuine.
func TestService_DecryptRef_WrongRequestID_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("pii"))
	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: f.ref, RequestID: "a-different-request-id", Envelope: f.envelope, Ciphertext: f.ct, MAC: f.mac,
	})
	require.Equal(t, vault.ErrRefUnverified.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_MalformedRef_Denied: a ref that isn't a
// well-formed identity-anchored aspect key is rejected before any MAC or
// decrypt work — mirrors the bridge's own resolveSensitiveRef guard.
func TestService_DecryptRef_MalformedRef_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{Ref: "not-a-well-formed-key"})
	require.NotEmpty(t, resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_ShreddedAfterValidMAC_Denied proves the order the
// design pins explicitly (§8, Fire 1): a shredded identity is refused even
// with a genuinely valid MAC — MAC verification narrows who can ATTEMPT a
// decrypt, it does not bypass the shred gate underneath.
func TestService_DecryptRef_ShreddedAfterValidMAC_Denied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("pii"))
	require.NoError(t, backend.ShredKey(ctx, "vtx.identity."+testRefIdentityKey))

	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: f.ref, RequestID: f.requestID, Envelope: f.envelope, Ciphertext: f.ct, MAC: f.mac,
	})
	require.Equal(t, vault.ErrKeyShredded.Error(), resp.Error)
	assert.Empty(t, resp.Plaintext)
}

// TestService_DecryptRef_SameRequestIDRedelivery_Verifies proves legitimate
// same-event redelivery (idempotent retry of the exact same tuple) still
// verifies — the binding rejects a DIFFERENT requestId, not a repeated one.
func TestService_DecryptRef_SameRequestIDRedelivery_Verifies(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	f := mintValidRef(t, ctx, nc, backend, []byte("123-45-6789"))
	req := vault.DecryptRefRequest{Ref: f.ref, RequestID: f.requestID, Envelope: f.envelope, Ciphertext: f.ct, MAC: f.mac}

	first := sendDecryptRef(t, nc, req)
	require.Empty(t, first.Error)
	second := sendDecryptRef(t, nc, req)
	require.Empty(t, second.Error)
	assert.Equal(t, first.Plaintext, second.Plaintext)
}
