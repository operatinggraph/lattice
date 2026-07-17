// Package vault implements per-identity key custody for crypto-shredding
// sensitive aspects (Contract #3 §3.10/§3.11): a Vault wraps/unwraps a
// per-identity data-encryption key (DEK) and encrypts/decrypts data under
// it. Key material never lands in Core KV — only the wrapped (ciphertext)
// DEK does, as the identity's piiKey aspect. ShredKey makes that wrapped DEK
// permanently unusable, rendering every ciphertext it protects — in live KV
// and in JetStream history — unrecoverable.
//
// The interface is deliberately stateless with respect to key custody: the
// caller (the Processor, at commit-path steps 4/6.5) supplies the Envelope
// it already holds from the identity's piiKey aspect, and the Vault never
// needs its own durable store of wrapped DEKs — Core KV's piiKey is the
// single source of truth for that ciphertext. This keeps a Vault backend
// swappable (local envelope encryption today, a production KMS later)
// without introducing a second, potentially divergent, copy of the wrapped
// key material.
package vault

import (
	"context"
	"errors"
	"time"
)

// Envelope is the wrapped-DEK reference persisted in an identity's
// vtx.identity.<id>.piiKey aspect (design §2.1, Contract #3 §3.10). WrappedDEK
// is ciphertext — the DEK wrapped under the backend's master key (KEK) — never
// plaintext key material.
type Envelope struct {
	WrappedDEK []byte    `json:"wrappedDEK"`
	KeyID      string    `json:"keyId"`
	KEKVersion string    `json:"kekVersion"`
	Alg        string    `json:"alg"`
	CreatedAt  time.Time `json:"createdAt"`
	Shredded   bool      `json:"shredded"`
}

// Ciphertext is the encrypted form of a sensitive aspect's data, stored in
// Core KV in place of the plaintext (Contract #3 §3.10).
type Ciphertext struct {
	CT    []byte `json:"ct"`
	Nonce []byte `json:"nonce"`
	KeyID string `json:"keyId"`
}

// SessionKey is the transient decryption key IssueSessionKey mints for an
// Edge node's local, in-memory decrypt of ciphertext deltas (Personal Lens
// Fire 5, personal-secure-lens-design.md §3.6 — "Transient Decryption",
// Edge Lattice.md §5). Key is the same per-identity DEK Decrypt/UnwrapKey use
// (there is one DEK per identity, not one per aspect); ExpiresAt is a
// hygiene bound the caller enforces locally, not the security boundary — the
// real revocation guarantee is ShredKey, checked fresh on every issuance.
type SessionKey struct {
	Key       []byte    `json:"key"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Sentinel errors a Vault backend returns. Callers (the Processor's
// commit-path hooks, the decrypt RPC responder) match on these with
// errors.Is rather than backend-specific error values.
var (
	// ErrKeyShredded is returned by Encrypt/Decrypt once ShredKey has been
	// called for the identity (or the caller-supplied Envelope already
	// carries Shredded=true) — the crypto-shred guarantee: no further
	// plaintext, in either direction, once shredded.
	ErrKeyShredded = errors.New("vault: identity key shredded")
	// ErrInvalidEnvelope is returned when a caller-supplied Envelope cannot
	// have been produced by CreateIdentityKey (malformed WrappedDEK, unknown
	// KEKVersion, …).
	ErrInvalidEnvelope = errors.New("vault: invalid envelope")
	// ErrDecryptFailed is returned when authenticated decryption fails (bad
	// key, tampered ciphertext, or — for an AEAD — a wrong nonce/AAD).
	ErrDecryptFailed = errors.New("vault: decrypt failed")
	// ErrRefUnverified is returned by the ref-verified decrypt RPC responder
	// (DecryptRefSubject) when a sensitive-ref marker's MAC is absent or does
	// not match Vault.MAC's recomputation — a fabricated or corrupted ref,
	// never decrypted (design sensitive-ref-mac-provenance-design.md §3.3).
	// Echoed over the wire and matched via errors.Is, exactly like
	// ErrKeyShredded.
	ErrRefUnverified = errors.New("vault: ref unverified")
)

// Vault is the key-custody + crypto interface the Processor's commit-path
// hooks and the trusted-tool decrypt RPC depend on. Implementations must
// never expose plaintext DEK material outside the backend.
type Vault interface {
	// CreateIdentityKey mints a new per-identity DEK, wraps it under the
	// backend's master key, and returns the wrapped-DEK Envelope for the
	// caller to persist as the identity's piiKey aspect. Called lazily, once
	// per identity, on its first sensitive-aspect write.
	CreateIdentityKey(ctx context.Context, identityKey string) (Envelope, error)

	// Encrypt seals plaintext under the DEK referenced by envelope,
	// returning the ciphertext envelope stored in place of the aspect's
	// plaintext data. identityKey is cryptographically bound into the
	// operation (AEAD associated data) — envelope alone is not sufficient;
	// a caller presenting the right Envelope under the wrong identityKey
	// fails with ErrInvalidEnvelope rather than silently succeeding under
	// the wrong identity. Fails with ErrKeyShredded if the identity's key
	// has been shredded.
	Encrypt(ctx context.Context, identityKey string, envelope Envelope, plaintext []byte) (Ciphertext, error)

	// Decrypt opens ct under the DEK referenced by envelope, returning the
	// original plaintext. Like Encrypt, identityKey must match the identity
	// envelope was minted for (AEAD-bound) or the call fails with
	// ErrInvalidEnvelope. Fails with ErrKeyShredded if the identity's key
	// has been shredded, or ErrDecryptFailed on any authentication failure.
	Decrypt(ctx context.Context, identityKey string, envelope Envelope, ct Ciphertext) ([]byte, error)

	// ShredKey irreversibly revokes the backend's ability to decrypt under
	// identityKey's DEK — the crypto-shred right-to-erasure primitive. After
	// ShredKey returns, every subsequent Encrypt/Decrypt for identityKey
	// fails with ErrKeyShredded, regardless of the Envelope supplied.
	//
	// For the local envelope-encryption backend this revokes the backend's
	// own willingness to unwrap (a deny-list), since its master KEK is
	// shared across identities; a production KMS backend additionally
	// destroys the per-identity KMS key version, which is true cryptographic
	// destruction. ShredKey is idempotent — shredding an already-shredded
	// (or never-created) identity key is not an error.
	ShredKey(ctx context.Context, identityKey string) error

	// WrapKey seals a small secret — e.g. a per-object Content Encryption Key
	// (Contract #3 §3.11) — under the DEK referenced by envelope, binding
	// identityKey exactly as Encrypt does. It is the same envelope operation
	// Encrypt already implements, exposed under a name that reads as
	// key-custody (wrapping a key) rather than aspect-data encryption at the
	// call site — a caller wrapping a CEK never touches Encrypt directly.
	// Fails with ErrKeyShredded / ErrInvalidEnvelope on the same conditions
	// as Encrypt.
	WrapKey(ctx context.Context, identityKey string, envelope Envelope, key []byte) (Ciphertext, error)

	// UnwrapKey reverses WrapKey, returning the original key bytes. Fails
	// with ErrKeyShredded / ErrInvalidEnvelope / ErrDecryptFailed on the same
	// conditions as Decrypt.
	UnwrapKey(ctx context.Context, identityKey string, envelope Envelope, wrapped Ciphertext) ([]byte, error)

	// IssueSessionKey mints a SessionKey for an Edge node to decrypt
	// identityKey's ciphertext deltas locally (Personal Lens Fire 5). Like
	// Decrypt/UnwrapKey it derives from envelope and fails with
	// ErrKeyShredded once the identity's key has been shredded — so a
	// shredded identity's already-delivered ciphertext deltas can never be
	// freshly decrypted again ("remote shredding renders all local copies
	// permanent gibberish", Edge Lattice.md §5). aspectScope is carried for
	// audit/API-shape only: there is one DEK per identity, not one per
	// aspect, so it does not select a different key. Fails with
	// ErrInvalidEnvelope on the same conditions as Decrypt.
	IssueSessionKey(ctx context.Context, identityKey string, envelope Envelope, aspectScope string, ttl time.Duration) (SessionKey, error)

	// MAC computes a keyed MAC over data under a purpose-scoped key derived
	// from the backend's platform secret (design
	// sensitive-ref-mac-provenance-design.md §3.1) — the primitive behind a
	// sensitive-ref marker's Processor-authentication (§3.2/§3.3). Purpose
	// strings are frozen constants (e.g. RefMACPurpose); distinct purposes
	// yield independent keys. Platform-scoped, not per-identity —
	// deliberately NOT gated by ShredKey's deny-list: a shred must kill
	// decryption (Decrypt does), not the ability to recognize a
	// Processor-minted marker. Verification is recompute-and-compare by the
	// caller; there is no separate Verify method.
	MAC(ctx context.Context, purpose string, data []byte) ([]byte, error)
}
