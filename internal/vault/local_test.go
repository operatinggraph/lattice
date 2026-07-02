package vault_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/vault"
)

func newTestBackend(t *testing.T) *vault.LocalBackend {
	t.Helper()
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	b, err := vault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	return b
}

func TestLocalBackend_EncryptDecryptRoundTrip(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	assert.Equal(t, "identity-1", env.KeyID)
	assert.Equal(t, vault.LocalAlg, env.Alg)
	assert.False(t, env.Shredded)
	assert.NotEmpty(t, env.WrappedDEK)

	plaintext := []byte("123-45-6789")
	ct, err := b.Encrypt(ctx, "identity-1", env, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ct.CT, "ciphertext must not equal plaintext")
	assert.Equal(t, "identity-1", ct.KeyID)

	got, err := b.Decrypt(ctx, "identity-1", env, ct)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestLocalBackend_DistinctIdentitiesGetDistinctKeys(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	envA, err := b.CreateIdentityKey(ctx, "identity-A")
	require.NoError(t, err)
	envB, err := b.CreateIdentityKey(ctx, "identity-B")
	require.NoError(t, err)

	plaintext := []byte("same plaintext")
	ctA, err := b.Encrypt(ctx, "identity-A", envA, plaintext)
	require.NoError(t, err)

	// identity-B's envelope cannot decrypt identity-A's ciphertext.
	_, err = b.Decrypt(ctx, "identity-B", envB, ctA)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestLocalBackend_MismatchedIdentityKeyForEnvelope_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	envA, err := b.CreateIdentityKey(ctx, "identity-A")
	require.NoError(t, err)

	// identity-A's Envelope was wrapped with identity-A bound as AEAD
	// associated data. Presenting it under a different identityKey (a
	// caller bug, a confused/replayed request) must not silently unwrap —
	// identityKey is part of the trust boundary, not just a cache label.
	_, err = b.Encrypt(ctx, "identity-B", envA, []byte("pii"))
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrInvalidEnvelope)
}

func TestLocalBackend_Decrypt_MalformedNonceLength_ReturnsErrorNotPanic(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("pii"))
	require.NoError(t, err)

	for _, nonce := range [][]byte{nil, {}, []byte("short"), make([]byte, 64)} {
		badCT := ct
		badCT.Nonce = nonce
		assert.NotPanics(t, func() {
			_, err := b.Decrypt(ctx, "identity-1", env, badCT)
			require.Error(t, err)
		})
	}
}

func TestLocalBackend_ConcurrentEncryptDecryptShred_NoRace(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("pii"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = b.Decrypt(ctx, "identity-1", env, ct)
		}()
		go func() {
			defer wg.Done()
			_ = b.ShredKey(ctx, "identity-1")
		}()
	}
	wg.Wait()

	// Once all goroutines have settled, the identity is shredded and stays
	// that way — no goroutine's in-flight derive/cache-write can resurrect
	// access after ShredKey has run.
	_, err = b.Decrypt(ctx, "identity-1", env, ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)
}

func TestLocalBackend_NonceUniquePerEncryption(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)

	ct1, err := b.Encrypt(ctx, "identity-1", env, []byte("same value"))
	require.NoError(t, err)
	ct2, err := b.Encrypt(ctx, "identity-1", env, []byte("same value"))
	require.NoError(t, err)

	assert.NotEqual(t, ct1.Nonce, ct2.Nonce, "each encryption must use a fresh nonce")
	assert.NotEqual(t, ct1.CT, ct2.CT, "identical plaintext must not produce identical ciphertext")
}

func TestLocalBackend_ShredKey_DecryptFailsPermanently(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("ssn"))
	require.NoError(t, err)

	require.NoError(t, b.ShredKey(ctx, "identity-1"))

	_, err = b.Decrypt(ctx, "identity-1", env, ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)

	// Re-supplying a fresh (unshredded) Envelope for the same identity still
	// fails — ShredKey is keyed on identityKey, not on the Envelope value.
	freshEnv := env
	freshEnv.Shredded = false
	_, err = b.Decrypt(ctx, "identity-1", freshEnv, ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)

	// Encrypt is equally denied post-shred.
	_, err = b.Encrypt(ctx, "identity-1", env, []byte("more pii"))
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)
}

func TestLocalBackend_ShredKey_IdempotentOnUnknownIdentity(t *testing.T) {
	b := newTestBackend(t)
	// Shredding an identity that never had a key created is not an error.
	assert.NoError(t, b.ShredKey(context.Background(), "never-created"))
	assert.NoError(t, b.ShredKey(context.Background(), "never-created"))
}

func TestLocalBackend_EnvelopeMarkedShredded_DecryptDenied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("pii"))
	require.NoError(t, err)

	// A caller (e.g. the Processor, having read piiKey.shredded=true from
	// Core KV) supplies an Envelope with Shredded=true without the backend
	// ever having seen ShredKey — must still be denied (defense-in-depth
	// against the backend's own deny-list being stale/unpersisted).
	shreddedEnv := env
	shreddedEnv.Shredded = true
	_, err = b.Decrypt(ctx, "identity-1", shreddedEnv, ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)
}

func TestLocalBackend_DifferentEnvelopeSameIdentity_DoesNotReuseStaleCache(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	envOld, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ctOld, err := b.Encrypt(ctx, "identity-1", envOld, []byte("old dek plaintext"))
	require.NoError(t, err) // primes the DEK cache for identity-1 under envOld

	// A second Envelope for the SAME identityKey but a DIFFERENT WrappedDEK
	// (simulating a caller mistake or a future key-rotation scenario) must
	// derive its own DEK rather than reuse whatever is cached for envOld.
	envNew, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	require.NotEqual(t, envOld.WrappedDEK, envNew.WrappedDEK)

	ctNew, err := b.Encrypt(ctx, "identity-1", envNew, []byte("new dek plaintext"))
	require.NoError(t, err)

	// Each ciphertext only decrypts under its own Envelope.
	gotOld, err := b.Decrypt(ctx, "identity-1", envOld, ctOld)
	require.NoError(t, err)
	assert.Equal(t, []byte("old dek plaintext"), gotOld)

	gotNew, err := b.Decrypt(ctx, "identity-1", envNew, ctNew)
	require.NoError(t, err)
	assert.Equal(t, []byte("new dek plaintext"), gotNew)

	// Cross-envelope decrypt fails — proves the cache didn't leak envOld's
	// DEK into an envNew-keyed decrypt or vice versa.
	_, err = b.Decrypt(ctx, "identity-1", envNew, ctOld)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestLocalBackend_WrongKEKVersion_Rejected(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)

	env.KEKVersion = "v-does-not-exist"
	_, err = b.Encrypt(ctx, "identity-1", env, []byte("pii"))
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrInvalidEnvelope)
}

func TestLocalBackend_EmptyWrappedDEK_Rejected(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	_, err := b.Encrypt(ctx, "identity-1", vault.Envelope{KEKVersion: "v1"}, []byte("pii"))
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrInvalidEnvelope)
}

func TestLocalBackend_TamperedCiphertext_DecryptFails(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("pii"))
	require.NoError(t, err)

	tampered := ct
	tampered.CT = append([]byte(nil), ct.CT...)
	tampered.CT[0] ^= 0xFF

	_, err = b.Decrypt(ctx, "identity-1", env, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestNewLocalBackend_RejectsWrongKEKLength(t *testing.T) {
	_, err := vault.NewLocalBackend([]byte("too-short"), "v1")
	require.Error(t, err)
}

func TestMasterKEKFromEnv(t *testing.T) {
	kek, err := vault.GenerateMasterKEK()
	require.NoError(t, err)
	t.Setenv("TEST_VAULT_KEK", kek)

	got, err := vault.MasterKEKFromEnv("TEST_VAULT_KEK")
	require.NoError(t, err)
	assert.Len(t, got, 32)
}

func TestMasterKEKFromEnv_Missing(t *testing.T) {
	_, err := vault.MasterKEKFromEnv("TEST_VAULT_KEK_DOES_NOT_EXIST")
	require.Error(t, err)
}

func TestMasterKEKFromEnv_InvalidLength(t *testing.T) {
	t.Setenv("TEST_VAULT_KEK_SHORT", "dG9vc2hvcnQ=") // base64("tooshort")
	_, err := vault.MasterKEKFromEnv("TEST_VAULT_KEK_SHORT")
	require.Error(t, err)
}

func TestMasterKEKFromEnv_InvalidBase64(t *testing.T) {
	t.Setenv("TEST_VAULT_KEK_BAD", "not-valid-base64!!")
	_, err := vault.MasterKEKFromEnv("TEST_VAULT_KEK_BAD")
	require.Error(t, err)
}

func TestLocalBackend_CreateIdentityKey_EmptyIdentityRejected(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.CreateIdentityKey(context.Background(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, vault.ErrInvalidEnvelope))
}
