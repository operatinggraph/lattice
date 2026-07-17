package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LocalAlg identifies the AEAD this backend uses for both DEK-wrapping and
// aspect-data encryption (design §2.5, grounded in NIST SP 800-38D — a
// per-identity DEK's message count stays far below the 96-bit-random-nonce
// birthday bound at this scale).
const LocalAlg = "AES-256-GCM"

// dekKeySize is the DEK and KEK size in bytes (AES-256).
const dekKeySize = 32

// macKeyPurposePrefix domain-separates a purpose-derived MAC key from the
// KEK's direct use elsewhere (AES-GCM wrapping) — the KEK never directly MACs
// data (design §2, NIST SP 800-108 KDF-in-HMAC form).
const macKeyPurposePrefix = "lattice/mac/"

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
	macKeys  map[string][]byte    // purpose -> derived MAC key (memoized, platform-scoped)

	// Operational counters for the Vault's own Health-KV heartbeat group
	// (health.vault.<instance>, emitted by the Processor that hosts this
	// backend). Atomic so the hot Encrypt/Decrypt path bumps a metric without
	// contending on b.mu.
	encryptCalls atomic.Uint64
	decryptCalls atomic.Uint64
	shredCalls   atomic.Uint64
}

// LocalBackendName identifies this backend in the health.vault heartbeat's
// `backend` field (Contract #5 §5.4 Vault baseline). An operator reads it to
// know a shred's guarantee strength: on this local envelope backend ShredKey is
// a deny-list *refusal* (the shared master KEK cannot be per-identity
// destroyed — see ShredKey), whereas a production KMS backend destroys the
// per-identity key version, which is true cryptographic erasure.
const LocalBackendName = "local-envelope"

// Stats is a point-in-time snapshot of a LocalBackend's operational counters,
// surfaced by the Processor's health.vault heartbeat. Call counts are
// cumulative since process start; DEKCacheSize/ShreddedCount are current
// gauges.
//
// DEKCacheSize is the count of *unwrapped* DEKs currently held in the TTL cache
// (the active decrypt/encrypt working set) — deliberately NOT a custody-set
// total: this backend holds no durable list of per-identity keys (each wrapped
// DEK lives in Core KV as the identity's piiKey aspect, not in the Vault), so
// there is no cheap, honest "total keys held" for it to report.
type Stats struct {
	Backend       string // LocalBackendName
	EncryptCalls  uint64 // cumulative Encrypt calls
	DecryptCalls  uint64 // cumulative Decrypt calls (Contract #5 §5.4 vault_calls_total)
	ShredCalls    uint64 // cumulative ShredKey calls (keyshredded_handled_total, Vault side)
	DEKCacheSize  int    // unwrapped DEKs currently cached (gauge; TTL-bounded working set)
	ShreddedCount int    // identities on the in-memory shred deny-list (gauge)
}

// Stats returns a snapshot of this backend's operational counters for the
// health.vault heartbeat. Cheap: the call counters are atomic reads; the lock
// is held only for the two map-length reads.
func (b *LocalBackend) Stats() Stats {
	b.mu.Lock()
	cacheSize := len(b.dekCache)
	shredCount := len(b.shredded)
	b.mu.Unlock()
	return Stats{
		Backend:       LocalBackendName,
		EncryptCalls:  b.encryptCalls.Load(),
		DecryptCalls:  b.decryptCalls.Load(),
		ShredCalls:    b.shredCalls.Load(),
		DEKCacheSize:  cacheSize,
		ShreddedCount: shredCount,
	}
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
		macKeys:    make(map[string][]byte),
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

// MasterKEKFromFile reads and base64-decodes a 32-byte master KEK from a
// file (trailing whitespace trimmed) — the seed-file convention this
// codebase uses for every other component credential (deploy/nkeys/*.nk),
// mirrored here for the master KEK's dev + trusted-tool posture.
func MasterKEKFromFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vault: read KEK file %s: %w", path, err)
	}
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("vault: KEK file %s is not valid base64: %w", path, err)
	}
	if len(kek) != dekKeySize {
		return nil, fmt.Errorf("vault: KEK file %s decodes to %d bytes, want %d", path, len(kek), dekKeySize)
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
//
// Refuses to mint for an identity already on the in-memory shredded deny-list
// (ErrKeyShredded). This is defense-in-depth, not the durability guarantee —
// within a single process the very next Encrypt call would reject a freshly
// minted key anyway via checkAndDeriveDEK's own b.shredded check, so this
// check exists to fail fast/obviously rather than mint a DEK that could never
// be used, and to keep the "never mint for a shredded identity" invariant
// true at every entry point, not just the one that happens to be reachable
// today.
func (b *LocalBackend) CreateIdentityKey(_ context.Context, identityKey string) (Envelope, error) {
	if identityKey == "" {
		return Envelope{}, fmt.Errorf("%w: empty identityKey", ErrInvalidEnvelope)
	}
	b.mu.Lock()
	_, alreadyShredded := b.shredded[identityKey]
	b.mu.Unlock()
	if alreadyShredded {
		return Envelope{}, ErrKeyShredded
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
	b.encryptCalls.Add(1)
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
	b.decryptCalls.Add(1)
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
	b.shredCalls.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shredded[identityKey] = time.Now().UTC()
	delete(b.dekCache, identityKey)
	return nil
}

// WrapKey implements Vault. It is Encrypt under a key-custody name — see the
// interface doc for why the two methods share an implementation.
func (b *LocalBackend) WrapKey(ctx context.Context, identityKey string, envelope Envelope, key []byte) (Ciphertext, error) {
	return b.Encrypt(ctx, identityKey, envelope, key)
}

// UnwrapKey implements Vault. It is Decrypt under a key-custody name — see
// the interface doc for why the two methods share an implementation.
func (b *LocalBackend) UnwrapKey(ctx context.Context, identityKey string, envelope Envelope, wrapped Ciphertext) ([]byte, error) {
	return b.Decrypt(ctx, identityKey, envelope, wrapped)
}

// maxSessionKeyTTL bounds an IssueSessionKey request — a hygiene ceiling on
// "short-lived" (design §3.6), not a security boundary: the boundary is
// checkAndDeriveDEK's shred check below, run fresh on every call.
const maxSessionKeyTTL = time.Hour

// IssueSessionKey implements Vault. It returns identityKey's DEK — the same
// key Encrypt/Decrypt use — via the identical shredded-check + derive path,
// so a shredded identity is denied exactly as Decrypt would be. ttl <= 0 or
// > maxSessionKeyTTL clamps to maxSessionKeyTTL rather than erroring.
func (b *LocalBackend) IssueSessionKey(_ context.Context, identityKey string, envelope Envelope, _ string, ttl time.Duration) (SessionKey, error) {
	dek, err := b.checkAndDeriveDEK(identityKey, envelope)
	if err != nil {
		return SessionKey{}, err
	}
	if ttl <= 0 || ttl > maxSessionKeyTTL {
		ttl = maxSessionKeyTTL
	}
	key := make([]byte, len(dek))
	copy(key, dek)
	return SessionKey{Key: key, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

// MAC implements Vault. It computes HMAC-SHA256(macKey(purpose), data) where
// macKey(purpose) is itself HMAC-SHA256(kek, macKeyPurposePrefix+purpose) —
// a single-block NIST SP 800-108 KDF-in-HMAC derivation, domain-separated
// from the KEK's direct AES-GCM use (design §2). Deterministic (same
// purpose+data always yields the same MAC, unlike Encrypt's random nonce) —
// that determinism is exactly what lets a verifier recompute-and-compare.
// Platform-scoped: unlike Encrypt/Decrypt/IssueSessionKey, MAC consults
// neither the DEK cache nor the shredded deny-list — a shred must kill
// decryption, not the ability to recognize a Processor-minted marker.
func (b *LocalBackend) MAC(_ context.Context, purpose string, data []byte) ([]byte, error) {
	if purpose == "" {
		return nil, fmt.Errorf("vault: MAC purpose required")
	}
	key := b.macKey(purpose)
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

// macKey returns purpose's derived MAC key, deriving and memoizing it on
// first use. Held under b.mu like every other piece of this backend's
// mutable state, but the derivation itself is cheap in-memory HMAC (no I/O),
// so the lock is held only briefly. Lazily initializes macKeys so a
// LocalBackend assembled via a struct literal that skips NewLocalBackend
// (a nil macKeys map) derives correctly instead of panicking on the
// map-assignment below.
func (b *LocalBackend) macKey(purpose string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key, ok := b.macKeys[purpose]; ok {
		return key
	}
	if b.macKeys == nil {
		b.macKeys = make(map[string][]byte)
	}
	mac := hmac.New(sha256.New, b.kek)
	mac.Write([]byte(macKeyPurposePrefix + purpose))
	key := mac.Sum(nil)
	b.macKeys[purpose] = key
	return key
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
//
// The shredded check runs BEFORE the WrappedDEK-empty check (not after): a
// caller may present a durable placeholder envelope for an identity that was
// shredded before it ever had a real key (empty WrappedDEK, Shredded=true —
// packages/privacy-base's ShredIdentityKey DDL writes exactly this shape when
// no piiKey existed yet, so the shred survives a process restart even though
// b.shredded is in-memory-only). Such an envelope must report ErrKeyShredded,
// not ErrInvalidEnvelope — the caller is asking "can I use this identity's
// key," and the honest answer is "no, it was shredded," not "your envelope is
// malformed."
func (b *LocalBackend) checkAndDeriveDEK(identityKey string, envelope Envelope) ([]byte, error) {
	if identityKey == "" {
		return nil, fmt.Errorf("%w: empty identityKey", ErrInvalidEnvelope)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.shredded[identityKey]; ok || envelope.Shredded {
		return nil, ErrKeyShredded
	}

	if len(envelope.WrappedDEK) == 0 {
		return nil, fmt.Errorf("%w: empty WrappedDEK", ErrInvalidEnvelope)
	}
	wrappedHash := sha256.Sum256(envelope.WrappedDEK)

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

// OpenWithSessionKey AEAD-opens ct under sessionKey — a SessionKey.Key
// returned by IssueSessionKey — binding identityKey as associated data
// exactly as LocalBackend.Decrypt does server-side. It is the local-decrypt
// counterpart for a caller (the Edge Vault Proxy client, edge-lattice-full-
// design.md §3.6, EDGE.4) that holds a session key but no live Vault
// backend. Fails with ErrDecryptFailed on any authentication failure (wrong
// key, tampered ciphertext, wrong identityKey, or a malformed nonce length).
func OpenWithSessionKey(sessionKey []byte, identityKey string, ct Ciphertext) ([]byte, error) {
	gcm, err := newGCM(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	if len(ct.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: invalid nonce length: got %d, want %d", ErrDecryptFailed, len(ct.Nonce), gcm.NonceSize())
	}
	plaintext, err := gcm.Open(nil, ct.Nonce, ct.CT, []byte(identityKey))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	return plaintext, nil
}
