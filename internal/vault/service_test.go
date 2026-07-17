package vault_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/vault"
)

// startTestServer starts an in-memory JetStream-enabled NATS server and
// returns a connected *nats.Conn.
func startTestServer(t *testing.T) *nats.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "identity-1", env, []byte("123-45-6789"))
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Ciphertext:  ct,
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	ct, err := backend.Encrypt(context.Background(), "identity-1", env, []byte("pii"))
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "identity-1"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendDecrypt(t, nc, vault.DecryptRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Ciphertext:  ct,
	})

	require.NotEmpty(t, resp.Error)
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	cek := []byte("0123456789abcdef0123456789abcdef") // 32 bytes (a per-object CEK)
	wrapResp := sendWrapKey(t, nc, vault.WrapKeyRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Key:         cek,
	})
	require.Empty(t, wrapResp.Error)
	assert.NotEqual(t, cek, wrapResp.Ciphertext.CT, "wrapped CEK must not equal the plaintext CEK")

	unwrapResp := sendUnwrapKey(t, nc, vault.UnwrapKeyRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Wrapped:     wrapResp.Ciphertext,
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	cek := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := backend.WrapKey(context.Background(), "identity-1", env, cek)
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "identity-1"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendUnwrapKey(t, nc, vault.UnwrapKeyRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		Wrapped:     wrapped,
	})
	require.NotEmpty(t, resp.Error)
	assert.Empty(t, resp.Key)
}

func TestService_WrapKey_MissingKey_Rejected(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kek := make([]byte, 32)
	backend, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendWrapKey(t, nc, vault.WrapKeyRequest{IdentityKey: "identity-1", Envelope: env})
	require.NotEmpty(t, resp.Error)
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		AspectScope: "lease",
		TTLSeconds:  60,
	})
	require.Empty(t, resp.Error)
	require.NotEmpty(t, resp.Key)
	assert.True(t, resp.ExpiresAt.After(time.Now()), "ExpiresAt must be in the future")

	// The issued key is the same DEK Decrypt uses under the hood — an Edge
	// holding it can open a ciphertext delta locally with plain AES-GCM.
	directDEK, err := backend.IssueSessionKey(context.Background(), "identity-1", env, "lease", time.Minute)
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

	env, err := backend.CreateIdentityKey(context.Background(), "identity-1")
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(context.Background(), "identity-1"))

	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendIssueSessionKey(t, nc, vault.IssueSessionKeyRequest{
		IdentityKey: "identity-1",
		Envelope:    env,
		TTLSeconds:  60,
	})
	require.NotEmpty(t, resp.Error)
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
