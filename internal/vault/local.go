package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"sync"
	"time"
)

// LocalAlg identifies the AEAD this backend uses for both DEK-wrapping and
// aspect-data encryption (design §2.5, grounded in NIST SP 800-38D — a
// per-identity DEK's message count stays far below the 96-bit-random-nonce
// birthday bound at this scale).
const LocalAlg = "AES-256-GCM"

// dekKeySize is the DEK and KEK size in bytes (AES-256).
const dekKeySize = 32

// dekCacheTTL bounds how long an unwrapped (plaintext) DEK is kept in
// memory. Steady-state Encrypt/Decrypt hit this cache and make zero calls to
// the KEK-unwrap path; a cache miss re-derives the DEK from the caller's
// Envelope. TTL is a hygiene bound, not a security boundary — the deny-list
// in shredKey is what makes a shred stick.
const dekCacheTTL = 5 * time.Minute

// LocalBackend is the envelope-encryption Vault backend (design §2.5, Path
// A — the ratified recommendation): a single master key-encryption key
// (KEK), sealed outside Core KV, wraps a random per-identity data-encryption
// key (DEK). Suited to the dev + trusted-tool (Loupe, 127.0.0.1) deployment
// posture; production KMS backends (HashiCorp Vault Transit, AWS/GCP KMS)
// implement the same Vault interface later.
type LocalBackend struct {
	kek        []byte
	kekVersion string

	mu       sync.Mutex
	dekCache map[string]cachedDEK // identityKey -> unwrapped DEK
	shredded map[string]time.Time // identityKey -> ShredKey call time
}

type cachedDEK struct {
	plaintext   []byte
	wrappedHash [sha256.Size]byte // identifies which Envelope.WrappedDEK this DEK was unwrapped from
	expiresAt   time.Time
}

var _ Vault = (*LocalBackend)(nil)

// NewLocalBackend constructs a LocalBackend sealing kek as the master KEK.
// kek must be exactly 32 bytes (AES-256). kekVersion labels the KEK in every
// Envelope this backend mints, so a future KEK rotation is detectable on
// read (rotation itself is not implemented — v1 uses one KEK for its
// lifetime, per design §2.5's deferred-rotation posture).
func NewLocalBackend(kek []byte, kekVersion string) (*LocalBackend, error) {
	if len(kek) != dekKeySize {
		return nil, fmt.Errorf("vault: master KEK must be %d bytes, got %d", dekKeySize, len(kek))
	}
	if kekVersion == "" {
		kekVersion = "v1"
	}
	sealed := make([]byte, len(kek))
	copy(sealed, kek)
	return &LocalBackend{
		kek:        sealed,
		kekVersion: kekVersion,
		dekCache:   make(map[string]cachedDEK),
		shredded:   make(map[string]time.Time),
	}, nil
}

// MasterKEKFromEnv reads and base64-decodes a 32-byte master KEK from the
// named environment variable (the sealed-in-config posture design §2.5
// describes for the local backend's dev + trusted-tool deployment).
func MasterKEKFromEnv(envVar string) ([]byte, error) {
	v := os.Getenv(envVar)
	if v == "" {
		return nil, fmt.Errorf("vault: %s not set", envVar)
	}
	kek, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("vault: %s is not valid base64: %w", envVar, err)
	}
	if len(kek) != dekKeySize {
		return nil, fmt.Errorf("vault: %s decodes to %d bytes, want %d", envVar, len(kek), dekKeySize)
	}
	return kek, nil
}

// GenerateMasterKEK returns a fresh random 32-byte KEK, base64-encoded for
// storage in a sealed config value (env var / file). Provisioning tooling
// (`make`-driven dev setup) uses this to mint a KEK once.
func GenerateMasterKEK() (string, error) {
	kek := make([]byte, dekKeySize)
	if _, err := rand.Read(kek); err != nil {
		return "", fmt.Errorf("vault: generate KEK: %w", err)
	}
	return base64.StdEncoding.EncodeToString(kek), nil
}

// CreateIdentityKey mints a random DEK, wraps it under the master KEK bound
// to identityKey as AEAD associated data, and returns the Envelope. It does
// not persist or cache anything by identityKey alone — the returned Envelope
// is the caller's only handle on the new key until the caller supplies it
// back to Encrypt/Decrypt.
func (b *LocalBackend) CreateIdentityKey(_ context.Context, identityKey string) (Envelope, error) {
	if identityKey == "" {
		return Envelope{}, fmt.Errorf("%w: empty identityKey", ErrInvalidEnvelope)
	}
	dek := make([]byte, dekKeySize)
	if _, err := rand.Read(dek); err != nil {
		return Envelope{}, fmt.Errorf("vault: generate DEK: %w", err)
	}
	wrapped, err := b.seal(b.kek, dek, []byte(identityKey))
	if err != nil {
		return Envelope{}, fmt.Errorf("vault: wrap DEK: %w", err)
	}
	return Envelope{
		WrappedDEK: wrapped,
		KeyID:      identityKey,
		KEKVersion: b.kekVersion,
		Alg:        LocalAlg,
		CreatedAt:  time.Now().UTC(),
		Shredded:   false,
	}, nil
}

// Encrypt implements Vault.
func (b *LocalBackend) Encrypt(_ context.Context, identityKey string, envelope Envelope, plaintext []byte) (Ciphertext, error) {
	dek, err := b.checkAndDeriveDEK(identityKey, envelope)
	if err != nil {
		return Ciphertext{}, err
	}
	nonce, ct, err := b.sealWithNonce(dek, plaintext, []byte(identityKey))
	if err != nil {
		return Ciphertext{}, fmt.Errorf("vault: encrypt: %w", err)
	}
	return Ciphertext{CT: ct, Nonce: nonce, KeyID: envelope.KeyID}, nil
}

// Decrypt implements Vault.
func (b *LocalBackend) Decrypt(_ context.Context, identityKey string, envelope Envelope, ct Ciphertext) ([]byte, error) {
	dek, err := b.checkAndDeriveDEK(identityKey, envelope)
	if err != nil {
		return nil, err
	}
	plaintext, err := b.openWithNonce(dek, ct.Nonce, ct.CT, []byte(identityKey))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	return plaintext, nil
}

// ShredKey implements Vault. It is idempotent: shredding an identity that
// was never created, or is already shredded, still succeeds. Marking
// shredded and evicting the DEK cache happen under the same lock
// checkAndDeriveDEK holds for its shredded-check + derive, so a ShredKey
// call can never interleave inside an in-flight Encrypt/Decrypt (no TOCTOU
// window where a decrypt "wins the race" against a concurrent shred).
func (b *LocalBackend) ShredKey(_ context.Context, identityKey string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shredded[identityKey] = time.Now().UTC()
	delete(b.dekCache, identityKey)
	return nil
}

// checkAndDeriveDEK returns the plaintext DEK for identityKey, or
// ErrKeyShredded / ErrInvalidEnvelope. The shredded-check, DEK-cache lookup,
// KEK-unwrap, and cache-write all run under one held lock — this is the
// single critical section that makes ShredKey a real barrier: it cannot run
// between this method's shredded-check and its use of the derived DEK, so a
// decrypt-in-flight at the moment of a concurrent shred either completes
// entirely before the shred is recorded or observes it and fails, never
// both-partially. The cache is further validated against a hash of
// envelope.WrappedDEK (not just identityKey) so a caller supplying a
// different Envelope for the same identity within the TTL window — a stale
// copy, or a future rotated key — re-derives rather than silently reusing
// the wrong DEK. Unwrapping is CPU-bound, in-memory AES-GCM (no I/O), so
// holding the lock across it does not risk blocking on an external call.
func (b *LocalBackend) checkAndDeriveDEK(identityKey string, envelope Envelope) ([]byte, error) {
	if identityKey == "" {
		return nil, fmt.Errorf("%w: empty identityKey", ErrInvalidEnvelope)
	}
	if len(envelope.WrappedDEK) == 0 {
		return nil, fmt.Errorf("%w: empty WrappedDEK", ErrInvalidEnvelope)
	}
	wrappedHash := sha256.Sum256(envelope.WrappedDEK)

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.shredded[identityKey]; ok || envelope.Shredded {
		return nil, ErrKeyShredded
	}
	if cached, ok := b.dekCache[identityKey]; ok && cached.wrappedHash == wrappedHash && time.Now().Before(cached.expiresAt) {
		return cached.plaintext, nil
	}
	if envelope.KEKVersion != b.kekVersion {
		return nil, fmt.Errorf("%w: envelope KEK version %q, backend has %q", ErrInvalidEnvelope, envelope.KEKVersion, b.kekVersion)
	}
	dek, err := b.open(b.kek, envelope.WrappedDEK, []byte(identityKey))
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap DEK: %v", ErrInvalidEnvelope, err)
	}

	b.dekCache[identityKey] = cachedDEK{plaintext: dek, wrappedHash: wrappedHash, expiresAt: time.Now().Add(dekCacheTTL)}
	return dek, nil
}

// seal is sealWithNonce with a fresh random nonce, returning nonce||ciphertext
// concatenated (self-describing: AES-GCM's standard nonce size is fixed).
func (b *LocalBackend) seal(key, plaintext, aad []byte) ([]byte, error) {
	nonce, ct, err := b.sealWithNonce(key, plaintext, aad)
	if err != nil {
		return nil, err
	}
	return append(nonce, ct...), nil
}

// sealWithNonce AEAD-seals plaintext under key with a fresh random nonce,
// returning the nonce and ciphertext separately.
func (b *LocalBackend) sealWithNonce(key, plaintext, aad []byte) (nonce, ct []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

// open reverses seal: sealed is nonce||ciphertext.
func (b *LocalBackend) open(key, sealed, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(sealed) < ns {
		return nil, fmt.Errorf("sealed value shorter than nonce size")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	return gcm.Open(nil, nonce, ct, aad)
}

// openWithNonce AEAD-opens ct under key using an explicit, externally
// supplied nonce (the shape a Ciphertext arrives in off the wire / out of
// Core KV). It validates nonce is exactly the AEAD's expected size before
// calling gcm.Open — Go's stdlib GCM implementation panics on a
// wrong-length nonce rather than returning an error, and ct.Nonce here may
// be attacker/caller-controlled (e.g. a malformed lattice.vault.decrypt RPC
// payload), so this check is what turns a malformed request into an
// ordinary error instead of taking down the process.
func (b *LocalBackend) openWithNonce(key, nonce, ct, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce length: got %d, want %d", len(nonce), gcm.NonceSize())
	}
	return gcm.Open(nil, nonce, ct, aad)
}

// newGCM constructs an AES-GCM AEAD from key (must be dekKeySize bytes).
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
