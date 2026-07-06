package main

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

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

// cekSize is the per-object Content Encryption Key size (AES-256).
const cekSize = 32

// contentEncryptionAlgo is the AEAD this handler uses for a sensitive
// object's bulk bytes (object-store-crypto-shred-design.md §3.1) — the same
// algorithm the Vault itself uses for envelope-wrapping the CEK.
const contentEncryptionAlgo = "AES-256-GCM"

// sha256Digest formats data's SHA-256 in the same "SHA-256=<base64url>" form
// NATS's object store computes and verifies (internal/substrate/object.go),
// so a sensitive object's plaintext digest is directly comparable to a
// non-sensitive object's NATS-computed one. A sensitive upload cannot use
// NATS's own digest (it streams ciphertext, not plaintext) so this
// reimplements the same format over caller-supplied bytes.
func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "SHA-256=" + base64.URLEncoding.EncodeToString(sum[:])
}

// sealAESGCM AEAD-encrypts plaintext under key with a fresh random nonce,
// mirroring internal/vault's LocalBackend.sealWithNonce (same AEAD
// construction, kept as a local, dependency-free copy since bulk object
// bytes must never pass through the Vault — only the CEK does). aad is bound
// into the GCM tag but not encrypted — the caller passes the object's oid so
// a `.content` document splice (copying one object's ciphertext/envelope
// onto another's oid, same governing identity) fails Open's tag check
// instead of silently decrypting under the wrong object's identity.
func sealAESGCM(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
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

// openAESGCM reverses sealAESGCM: AEAD-decrypts ciphertext under key using
// the stored nonce, verifying the GCM tag against aad (must be the same
// value sealAESGCM was called with, i.e. the object's oid).
func openAESGCM(key, nonce, ciphertext, aad []byte) ([]byte, error) {
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

// generateCEK returns a fresh random per-object Content Encryption Key.
func generateCEK() ([]byte, error) {
	cek := make([]byte, cekSize)
	if _, err := rand.Read(cek); err != nil {
		return nil, fmt.Errorf("generate CEK: %w", err)
	}
	return cek, nil
}

// encodeWrappedCEK packs a wrap-RPC Ciphertext into the single string
// `.content.encryption.wrappedCEK` field expects: base64(nonce) + ":" +
// base64(ct). KeyID is not encoded here — it is redundant with the
// sibling `encryption.keyId` field the DDL already requires.
func encodeWrappedCEK(ct vault.Ciphertext) string {
	return base64.StdEncoding.EncodeToString(ct.Nonce) + ":" + base64.StdEncoding.EncodeToString(ct.CT)
}

// decodeWrappedCEK reverses encodeWrappedCEK.
func decodeWrappedCEK(s string) (vault.Ciphertext, error) {
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

// fetchPiiKeyEnvelope reads identityKey's wrapped-DEK Envelope off its
// piiKey aspect — the same Core-KV read handleVaultDecrypt already does
// before calling the Vault decrypt RPC, factored out since the wrap/unwrap
// RPCs need the identical Envelope.
func fetchPiiKeyEnvelope(ctx context.Context, conn *substrate.Conn, identityKey string) (vault.Envelope, error) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, identityKey+".piiKey")
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return vault.Envelope{}, fmt.Errorf("%s has no piiKey — Vault.CreateIdentityKey must run first (e.g. via a sensitive aspect write)", identityKey)
		}
		return vault.Envelope{}, fmt.Errorf("get %s.piiKey: %w", identityKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return vault.Envelope{}, fmt.Errorf("parse %s.piiKey: %w", identityKey, err)
	}
	return doc.Data, nil
}

// vaultWrapKey calls the Vault's WrapKeySubject RPC (object-store-crypto-
// shred-design.md §3.1): wrap key (a per-object CEK) under identityKey's DEK.
// The bulk bytes never reach the Vault — only this small key does.
func vaultWrapKey(ctx context.Context, conn *substrate.Conn, identityKey string, env vault.Envelope, key []byte) (vault.Ciphertext, error) {
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

// vaultUnwrapKey calls the Vault's UnwrapKeySubject RPC — the read-side
// counterpart of vaultWrapKey.
func vaultUnwrapKey(ctx context.Context, conn *substrate.Conn, identityKey string, env vault.Envelope, wrapped vault.Ciphertext) ([]byte, error) {
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
