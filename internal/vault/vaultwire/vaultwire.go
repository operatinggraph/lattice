// Package vaultwire holds the Vault wire types and the pure-crypto open that a
// caller needs when it holds a session key but no Vault backend — the Edge
// Vault client above all (edge-lattice-full-design.md §3.6). It depends on
// nothing but the standard library.
//
// It exists because internal/vault bundles its NATS-served backend (service.go)
// beside these types, so importing a Ciphertext struct links a NATS client. A
// browser-hosted Edge engine must not carry one (edge-browser-node-design.md
// §3.2): the transport it is allowed is the Gateway door, and a linked NATS
// client is both dead weight in the artifact and a bypass waiting to be wired.
// internal/vault re-exports every name here, so platform call sites read as
// vault vocabulary and do not import this package directly.
package vaultwire

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
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

// NewGCM constructs an AES-GCM AEAD from key (must be a valid AES key length).
func NewGCM(key []byte) (cipher.AEAD, error) {
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
	gcm, err := NewGCM(sessionKey)
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
