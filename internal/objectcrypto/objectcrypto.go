// Package objectcrypto holds the generic AEAD content-encryption and
// Vault wrap/unwrap-RPC helpers behind the object-store crypto-shred design
// (object-store-crypto-shred-design.md §3.1) — factored out of
// cmd/loupe/objects_crypto.go (Fire 2) so a second sensitive-object uploader
// (Fire 4: a vertical app) does not duplicate this security-sensitive code.
// Bulk object bytes never pass through the Vault — only the small per-object
// Content Encryption Key (CEK) does, wrapped under the governing identity's
// DEK via WrapKey/UnwrapKey.
package objectcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// CEKSize is the per-object Content Encryption Key size (AES-256).
const CEKSize = 32

// ContentEncryptionAlgo is the AEAD a sensitive object's bulk bytes are
// sealed under (object-store-crypto-shred-design.md §3.1) — the same
// algorithm the Vault itself uses for envelope-wrapping the CEK.
const ContentEncryptionAlgo = "AES-256-GCM"

// Digest formats data's SHA-256 in the same "SHA-256=<base64url>" form
// NATS's object store computes and verifies (internal/substrate/object.go),
// so a sensitive object's plaintext digest is directly comparable to a
// non-sensitive object's NATS-computed one. A sensitive upload cannot use
// NATS's own digest (it streams ciphertext, not plaintext) so this
// reimplements the same format over caller-supplied bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "SHA-256=" + base64.URLEncoding.EncodeToString(sum[:])
}

// Seal AEAD-encrypts plaintext under key with a fresh random nonce, mirroring
// internal/vault's LocalBackend.sealWithNonce (same AEAD construction, kept
// as a dependency-free copy since bulk object bytes must never pass through
// the Vault — only the CEK does). aad is bound into the GCM tag but not
// encrypted — the caller passes the object's oid so a `.content` document
// splice (copying one object's ciphertext/envelope onto another's oid, same
// governing identity) fails Open's tag check instead of silently decrypting
// under the wrong object's identity.
func Seal(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

// Open reverses Seal: AEAD-decrypts ciphertext under key using the stored
// nonce, verifying the GCM tag against aad (must be the same value Seal was
// called with, i.e. the object's oid).
func Open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce length: got %d, want %d", len(nonce), gcm.NonceSize())
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// GenerateCEK returns a fresh random per-object Content Encryption Key.
func GenerateCEK() ([]byte, error) {
	cek := make([]byte, CEKSize)
	if _, err := rand.Read(cek); err != nil {
		return nil, fmt.Errorf("generate CEK: %w", err)
	}
	return cek, nil
}

// EncodeWrappedCEK packs a wrap-RPC Ciphertext into the single string
// `.content.encryption.wrappedCEK` field expects: base64(nonce) + ":" +
// base64(ct). KeyID is not encoded here — it is redundant with the
// sibling `encryption.keyId` field the DDL already requires.
func EncodeWrappedCEK(ct vault.Ciphertext) string {
	return base64.StdEncoding.EncodeToString(ct.Nonce) + ":" + base64.StdEncoding.EncodeToString(ct.CT)
}

// DecodeWrappedCEK reverses EncodeWrappedCEK.
func DecodeWrappedCEK(s string) (vault.Ciphertext, error) {
	nonceB64, ctB64, ok := strings.Cut(s, ":")
	if !ok {
		return vault.Ciphertext{}, errors.New("malformed wrappedCEK: expected nonce:ciphertext")
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return vault.Ciphertext{}, fmt.Errorf("wrappedCEK nonce: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return vault.Ciphertext{}, fmt.Errorf("wrappedCEK ciphertext: %w", err)
	}
	return vault.Ciphertext{Nonce: nonce, CT: ct}, nil
}

// WrapKey calls the Vault's WrapKeySubject RPC (object-store-crypto-shred-
// design.md §3.1): wrap key (a per-object CEK) under identityKey's DEK. The
// bulk bytes never reach the Vault — only this small key does. env is the
// identity's piiKey Envelope; callers fetch it their own P5-compliant way
// (Loupe via its inspector-only direct Core-KV read, a vertical app via a
// lens projection) — this package is agnostic to that source.
func WrapKey(ctx context.Context, conn *substrate.Conn, identityKey string, env vault.Envelope, key []byte) (vault.Ciphertext, error) {
	reqBody, err := json.Marshal(vault.WrapKeyRequest{IdentityKey: identityKey, Envelope: env, Key: key})
	if err != nil {
		return vault.Ciphertext{}, fmt.Errorf("marshal wrapKey request: %w", err)
	}
	msg, err := conn.NATS().RequestWithContext(ctx, vault.WrapKeySubject, reqBody)
	if err != nil {
		return vault.Ciphertext{}, fmt.Errorf("vault wrapKey RPC: %w", err)
	}
	var resp vault.WrapKeyResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return vault.Ciphertext{}, fmt.Errorf("parse vault wrapKey reply: %w", err)
	}
	if resp.Error != "" {
		return vault.Ciphertext{}, errors.New(resp.Error)
	}
	return resp.Ciphertext, nil
}

// UnwrapKey calls the Vault's UnwrapKeySubject RPC — the read-side
// counterpart of WrapKey.
func UnwrapKey(ctx context.Context, conn *substrate.Conn, identityKey string, env vault.Envelope, wrapped vault.Ciphertext) ([]byte, error) {
	reqBody, err := json.Marshal(vault.UnwrapKeyRequest{IdentityKey: identityKey, Envelope: env, Wrapped: wrapped})
	if err != nil {
		return nil, fmt.Errorf("marshal unwrapKey request: %w", err)
	}
	msg, err := conn.NATS().RequestWithContext(ctx, vault.UnwrapKeySubject, reqBody)
	if err != nil {
		return nil, fmt.Errorf("vault unwrapKey RPC: %w", err)
	}
	var resp vault.UnwrapKeyResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("parse vault unwrapKey reply: %w", err)
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Key, nil
}
