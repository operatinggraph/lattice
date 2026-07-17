package vault_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestLocalBackend_WrapUnwrapKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)

	cek := make([]byte, 32)
	_, err = rand.Read(cek)
	require.NoError(t, err)

	wrapped, err := b.WrapKey(ctx, "identity-1", env, cek)
	require.NoError(t, err)
	assert.NotEqual(t, cek, wrapped.CT, "wrapped CEK must not equal the plaintext CEK")
	assert.Equal(t, "identity-1", wrapped.KeyID)

	got, err := b.UnwrapKey(ctx, "identity-1", env, wrapped)
	require.NoError(t, err)
	assert.Equal(t, cek, got)
}

func TestLocalBackend_UnwrapKey_WrongIdentity_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	envA, err := b.CreateIdentityKey(ctx, "identity-A")
	require.NoError(t, err)
	envB, err := b.CreateIdentityKey(ctx, "identity-B")
	require.NoError(t, err)

	cek := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := b.WrapKey(ctx, "identity-A", envA, cek)
	require.NoError(t, err)

	_, err = b.UnwrapKey(ctx, "identity-B", envB, wrapped)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestLocalBackend_UnwrapKey_AfterShred_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	cek := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := b.WrapKey(ctx, "identity-1", env, cek)
	require.NoError(t, err)

	require.NoError(t, b.ShredKey(ctx, "identity-1"))

	_, err = b.UnwrapKey(ctx, "identity-1", env, wrapped)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)
}

// TestLocalBackend_IssueSessionKey_ReturnsTheSameDEKAsDecrypt proves the
// transient session key an Edge decrypts with locally is exactly the DEK
// Encrypt/Decrypt use — so it can open any ciphertext delta for that
// identity without a second key-derivation scheme to keep in sync.
func TestLocalBackend_IssueSessionKey_ReturnsTheSameDEKAsDecrypt(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	plaintext := []byte("sensitive delta payload")
	ct, err := b.Encrypt(ctx, "identity-1", env, plaintext)
	require.NoError(t, err)

	sk, err := b.IssueSessionKey(ctx, "identity-1", env, "lease", time.Minute)
	require.NoError(t, err)
	require.NotEmpty(t, sk.Key)
	assert.True(t, sk.ExpiresAt.After(time.Now()))

	// Decrypt the ciphertext under the issued session key exactly as an Edge
	// node would (internal/edge/vault) — no call back to the Vault needed.
	opened, err := vault.OpenWithSessionKey(sk.Key, "identity-1", ct)
	require.NoError(t, err)
	assert.Equal(t, plaintext, opened)
}

func TestOpenWithSessionKey_WrongIdentityKeyAAD_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("payload"))
	require.NoError(t, err)
	sk, err := b.IssueSessionKey(ctx, "identity-1", env, "", time.Minute)
	require.NoError(t, err)

	_, err = vault.OpenWithSessionKey(sk.Key, "identity-2", ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestOpenWithSessionKey_WrongSessionKey_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("payload"))
	require.NoError(t, err)

	wrongKey := make([]byte, 32)
	_, err = rand.Read(wrongKey)
	require.NoError(t, err)

	_, err = vault.OpenWithSessionKey(wrongKey, "identity-1", ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestOpenWithSessionKey_TamperedCiphertext_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("payload"))
	require.NoError(t, err)
	sk, err := b.IssueSessionKey(ctx, "identity-1", env, "", time.Minute)
	require.NoError(t, err)

	ct.CT[0] ^= 0xFF
	_, err = vault.OpenWithSessionKey(sk.Key, "identity-1", ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

func TestOpenWithSessionKey_MalformedNonce_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	ct, err := b.Encrypt(ctx, "identity-1", env, []byte("payload"))
	require.NoError(t, err)
	sk, err := b.IssueSessionKey(ctx, "identity-1", env, "", time.Minute)
	require.NoError(t, err)

	ct.Nonce = ct.Nonce[:len(ct.Nonce)-1]
	_, err = vault.OpenWithSessionKey(sk.Key, "identity-1", ct)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrDecryptFailed)
}

// TestLocalBackend_IssueSessionKey_AfterShred_Denied is Gate-3 vector 5
// (personal-secure-lens-design.md §5): a shredded identity's session key
// issuance must fail exactly like Decrypt/UnwrapKey.
func TestLocalBackend_IssueSessionKey_AfterShred_Denied(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)
	require.NoError(t, b.ShredKey(ctx, "identity-1"))

	_, err = b.IssueSessionKey(ctx, "identity-1", env, "lease", time.Minute)
	require.Error(t, err)
	assert.ErrorIs(t, err, vault.ErrKeyShredded)
}

// TestLocalBackend_IssueSessionKey_TTLClamped proves a non-positive or
// over-ceiling ttl clamps to maxSessionKeyTTL rather than erroring or
// minting an unbounded-lifetime key.
func TestLocalBackend_IssueSessionKey_TTLClamped(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	env, err := b.CreateIdentityKey(ctx, "identity-1")
	require.NoError(t, err)

	skZero, err := b.IssueSessionKey(ctx, "identity-1", env, "", 0)
	require.NoError(t, err)
	assert.True(t, skZero.ExpiresAt.After(time.Now().Add(59*time.Minute)), "ttl<=0 must clamp to the 1h ceiling, not mint a zero-lifetime key")

	skOver, err := b.IssueSessionKey(ctx, "identity-1", env, "", 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, skOver.ExpiresAt.Before(time.Now().Add(2*time.Hour)), "an over-ceiling ttl must clamp down to the 1h ceiling")
}

// TestLocalBackend_MAC_Deterministic proves MAC is a pure function of
// (purpose, data) — unlike Encrypt's random nonce, a verifier must be able to
// recompute and compare (design sensitive-ref-mac-provenance-design.md §3.1).
func TestLocalBackend_MAC_Deterministic(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	mac1, err := b.MAC(ctx, vault.RefMACPurpose, []byte("same data"))
	require.NoError(t, err)
	mac2, err := b.MAC(ctx, vault.RefMACPurpose, []byte("same data"))
	require.NoError(t, err)
	assert.Equal(t, mac1, mac2, "MAC must be deterministic for the same purpose+data")
	assert.NotEmpty(t, mac1)
}

// TestLocalBackend_MAC_PurposeSeparation proves distinct purposes yield
// independent keys — the same data MACs differently under a different
// purpose (design §3.1's domain-separation requirement).
func TestLocalBackend_MAC_PurposeSeparation(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	macA, err := b.MAC(ctx, "purpose-a", []byte("data"))
	require.NoError(t, err)
	macB, err := b.MAC(ctx, "purpose-b", []byte("data"))
	require.NoError(t, err)
	assert.NotEqual(t, macA, macB, "distinct purposes must derive independent keys")
}

// TestLocalBackend_MAC_DifferentDataDifferentMAC proves MAC is sensitive to
// its input — changing data under the same purpose changes the MAC (the
// basic integrity property a verifier's hmac.Equal comparison relies on).
func TestLocalBackend_MAC_DifferentDataDifferentMAC(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	mac1, err := b.MAC(ctx, vault.RefMACPurpose, []byte("data one"))
	require.NoError(t, err)
	mac2, err := b.MAC(ctx, vault.RefMACPurpose, []byte("data two"))
	require.NoError(t, err)
	assert.NotEqual(t, mac1, mac2)
}

// TestLocalBackend_MAC_DistinctBackendsDistinctMACs proves the MAC key
// derives from the backend's own KEK, not a fixed constant — two backends
// with different KEKs produce different MACs for identical purpose+data.
func TestLocalBackend_MAC_DistinctBackendsDistinctMACs(t *testing.T) {
	ctx := context.Background()
	b1 := newTestBackend(t)
	b2 := newTestBackend(t)

	mac1, err := b1.MAC(ctx, vault.RefMACPurpose, []byte("data"))
	require.NoError(t, err)
	mac2, err := b2.MAC(ctx, vault.RefMACPurpose, []byte("data"))
	require.NoError(t, err)
	assert.NotEqual(t, mac1, mac2, "distinct KEKs must derive distinct MAC keys")
}

// TestLocalBackend_MAC_EmptyPurposeRejected guards against silently deriving
// a MAC key under an empty purpose label, which would collapse
// domain-separation for any caller that forgot to pass one.
func TestLocalBackend_MAC_EmptyPurposeRejected(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	_, err := b.MAC(ctx, "", []byte("data"))
	require.Error(t, err)
}

// TestLocalBackend_MAC_NotGatedByShred proves MAC is platform-scoped, not
// per-identity: it stays callable after ShredKey — a shred must kill
// decryption, not the ability to recognize a Processor-minted marker (design
// §3.1's explicit "deliberately NOT gated by the shredded-set").
func TestLocalBackend_MAC_NotGatedByShred(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)

	require.NoError(t, b.ShredKey(ctx, "identity-1"))
	mac, err := b.MAC(ctx, vault.RefMACPurpose, []byte("vtx.identity.identity-1.ssn"))
	require.NoError(t, err, "MAC must remain callable after an identity is shredded")
	assert.NotEmpty(t, mac)
}

// TestRefMACInput_FieldBoundariesUnambiguous proves the length-prefixed
// encoding cannot be confused by shifting bytes across a field boundary —
// e.g. "ab"+"c" must MAC differently from "a"+"bc" (design §3.2's "never
// JSON" rationale: an unprefixed concatenation would be ambiguous here).
func TestRefMACInput_FieldBoundariesUnambiguous(t *testing.T) {
	in1 := vault.RefMACInput("ab", "c", vault.Ciphertext{})
	in2 := vault.RefMACInput("a", "bc", vault.Ciphertext{})
	assert.NotEqual(t, in1, in2)
}
