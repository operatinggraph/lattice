package vault_test

// The decryptRef responder resolves the key holder from the ciphertext's own
// keyId. The ref names WHICH record is being opened and is bound into the MAC;
// it does not select the key. These tests seal under a holder the ref does not
// name, so a responder still deriving the holder from the ref cannot pass them.

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/vault"
)

func startRefService(t *testing.T) (*vault.LocalBackend, *nats.Conn, context.Context) {
	t.Helper()
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	backend, err := vault.NewLocalBackend(make([]byte, 32), "v1")
	require.NoError(t, err)
	svc := vault.NewService(backend, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))
	return backend, nc, ctx
}

// A record anchored on one identity and sealed under a retention-class holder
// decrypts. The ref's anchor holds no key at all, so this passes only if the
// responder read the holder off the ciphertext.
func TestService_DecryptRef_ResolvesTheHolderFromTheCiphertext(t *testing.T) {
	backend, nc, ctx := startRefService(t)

	const holder = "vtx.retentionclass.RetCLassRefAAAAAAAAA"
	ref := "vtx.appointment." + testRefIdentityKey + ".encounter"
	const requestID = "req-holder-1"

	env, err := backend.CreateIdentityKey(ctx, holder)
	require.NoError(t, err)
	ct, err := backend.Encrypt(ctx, holder, env, []byte("chart note"))
	require.NoError(t, err)
	mac, err := backend.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(ref, requestID, ct))
	require.NoError(t, err)

	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: ref, RequestID: requestID, Envelope: env, Ciphertext: ct, MAC: mac,
	})
	require.Empty(t, resp.Error)
	assert.Equal(t, []byte("chart note"), resp.Plaintext)
}

// A ciphertext naming no usable holder is refused before any decrypt is
// attempted, even though the ref itself is a well-formed identity-anchored
// aspect key whose identity has a live DEK.
func TestService_DecryptRef_MalformedKeyIDNeverFallsBackToTheRef(t *testing.T) {
	backend, nc, ctx := startRefService(t)

	identityKey := "vtx.identity." + testRefIdentityKey
	ref := identityKey + ".ssn"
	const requestID = "req-holder-2"

	env, err := backend.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)
	ct, err := backend.Encrypt(ctx, identityKey, env, []byte("123-45-6789"))
	require.NoError(t, err)
	ct.KeyID = "not-a-vertex-key"
	mac, err := backend.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(ref, requestID, ct))
	require.NoError(t, err)

	resp := sendDecryptRef(t, nc, vault.DecryptRefRequest{
		Ref: ref, RequestID: requestID, Envelope: env, Ciphertext: ct, MAC: mac,
	})
	assert.Equal(t, "vault: ciphertext names no usable key holder", resp.Error, "the refusal must come from holder resolution, not from a later decrypt failure")
	assert.Empty(t, resp.Plaintext)
}
