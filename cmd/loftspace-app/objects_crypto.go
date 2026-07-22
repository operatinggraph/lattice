package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/objectcrypto"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

// fetchPiiKeyEnvelope reads identityKey's wrapped-DEK Envelope off the
// privacy-base `piiKeyEnvelope` lens (bucket privacybase.PiiKeyEnvelopeBucket)
// — the P5-compliant read seam a vertical app uses in place of Loupe's direct
// Core-KV read (the P5 inspector exception only Loupe and the platform
// binaries get; object-store-crypto-shred-design.md §9 Fire 4 Increment 1).
// The lens is a flat "nats-kv" projection keyed by the identity vertex key
// verbatim (internal/pkgmgr's IntoKey default), so the KV value unmarshals
// directly onto vault.Envelope. CreatedAt stays unprojected/zero (a display
// nicety no consumer needs) but Shredded IS projected (sensitive-param-egress
// Fire 2 fixed this — a stale unprojected value would defeat a restart-proof
// shred check for any consumer besides the Vault process's own in-memory
// registry): the Vault backend's shred check is
// `b.shredded[identityKey] || envelope.Shredded` (internal/vault/local.go), an
// OR, so this call's Envelope.Shredded is real, CDC-refreshed durable truth —
// a second, restart-proof gate alongside the backend's own record.
func fetchPiiKeyEnvelope(ctx context.Context, conn *substrate.Conn, identityKey string) (vault.Envelope, error) {
	entry, err := conn.KVGet(ctx, privacybase.PiiKeyEnvelopeBucket, identityKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return vault.Envelope{}, fmt.Errorf("%s has no piiKey envelope projected — Vault.CreateIdentityKey must run first (e.g. via a sensitive aspect write)", identityKey)
		}
		return vault.Envelope{}, fmt.Errorf("get %s piiKeyEnvelope: %w", identityKey, err)
	}
	var env vault.Envelope
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return vault.Envelope{}, fmt.Errorf("parse %s piiKeyEnvelope: %w", identityKey, err)
	}
	return env, nil
}

// handleSensitiveObjectUpload implements the sensitive branch of POST
// /api/objects (object-store-crypto-shred-design.md §9 Fire 4 Increment 2):
// the byte-plane write stays server-side (unchanged from the ordinary path;
// #75 Fire 2b only moved the AttachObject op submission browser-side), but
// the plaintext is read fully, encrypted under a fresh per-object CEK, and
// only the CEK — never the bulk bytes — passes through the Vault (wrapped
// under governingIdentity's DEK, fetched via the P5-compliant lens above).
// Ciphertext, not plaintext, is what streams to core-objects; the response
// carries the encryption envelope for the browser to fold into the
// AttachObject payload it submits itself — this handler never submits that
// op (mirrors cmd/loupe/objects.go's handleSensitiveObjectUpload, adapted for
// the browser-submits-AttachObject shape).
func (s *server) handleSensitiveObjectUpload(w http.ResponseWriter, ctx context.Context, conn *substrate.Conn, file multipart.File, contentType, governingIdentity string) {
	plaintext, err := io.ReadAll(io.LimitReader(file, s.uploadCap+1))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "read upload: "+err.Error())
		return
	}
	if int64(len(plaintext)) > s.uploadCap {
		s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds the %d-byte cap", s.uploadCap))
		return
	}

	// The oid is derived from the PLAINTEXT digest, salted with the governing
	// identity (object-store-crypto-shred-design.md §3.3) so a sensitive
	// object is never cross-identity content-addressed. Computed before
	// sealing so it can bind the ciphertext as AEAD associated data.
	plaintextDigest := objectcrypto.Digest(plaintext)
	oid := substrate.SHA256NanoID("object:" + governingIdentity + ":" + plaintextDigest)

	env, err := fetchPiiKeyEnvelope(ctx, conn, governingIdentity)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	cek, err := objectcrypto.GenerateCEK()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	contentNonce, ciphertext, err := objectcrypto.Seal(cek, plaintext, []byte(oid))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encrypt: "+err.Error())
		return
	}
	wrapped, err := objectcrypto.WrapKey(ctx, conn, governingIdentity, env, cek)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "wrap CEK: "+err.Error())
		return
	}

	storeName, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "generate store name: "+err.Error())
		return
	}
	info, err := conn.ObjectPut(ctx, bootstrap.CoreObjectsBucket, storeName, bytes.NewReader(ciphertext), int64(len(ciphertext)))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "store object bytes: "+err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"oid": oid, "digest": plaintextDigest, "storeName": storeName,
		"size": info.Size, "contentType": contentType,
		"sensitive": true, "governingIdentity": governingIdentity,
		"encryption": map[string]any{
			"algo":       objectcrypto.ContentEncryptionAlgo,
			"nonce":      base64.StdEncoding.EncodeToString(contentNonce),
			"wrappedCEK": objectcrypto.EncodeWrappedCEK(wrapped),
			"keyId":      governingIdentity,
		},
	})
}

// handleSensitiveObjectDecrypt implements the `?decrypt=true` opt-in read for
// a sensitive object (object-store-crypto-shred-design.md §3.4/§9 Fire 4
// Increment 2): unwrap the CEK via the Vault (the piiKey envelope fetched
// through the same P5-compliant lens the upload path uses), decrypt the
// ciphertext locally, verify the GCM tag and the plaintext digest, then serve
// the plaintext under the same anti-XSS disposition/CSP posture as the
// default (ciphertext) path. Caller must have already authorized the read
// (authorizeObjectGet) — this only performs the crypto.
func (s *server) handleSensitiveObjectDecrypt(w http.ResponseWriter, ctx context.Context, conn *substrate.Conn, oid string, row attachmentRow) {
	wrapped, err := objectcrypto.DecodeWrappedCEK(row.Encryption.WrappedCEK)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decode wrappedCEK: "+err.Error())
		return
	}
	env, err := fetchPiiKeyEnvelope(ctx, conn, row.GoverningIdentity)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	cek, err := objectcrypto.UnwrapKey(ctx, conn, row.GoverningIdentity, env, wrapped)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "unwrap CEK: "+err.Error())
		return
	}
	contentNonce, err := base64.StdEncoding.DecodeString(row.Encryption.Nonce)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decode content nonce: "+err.Error())
		return
	}

	rc, _, err := conn.ObjectGet(ctx, bootstrap.CoreObjectsBucket, row.StoreName)
	if err != nil {
		if errors.Is(err, substrate.ErrObjectNotFound) {
			s.writeError(w, http.StatusNotFound, "object bytes not found")
			return
		}
		s.writeError(w, http.StatusBadGateway, "read object bytes: "+err.Error())
		return
	}
	ciphertext, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "read object bytes: "+err.Error())
		return
	}

	plaintext, err := objectcrypto.Open(cek, contentNonce, ciphertext, []byte(oid))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decrypt failed: tampered or corrupt ciphertext")
		return
	}
	if row.Digest != "" && objectcrypto.Digest(plaintext) != row.Digest {
		s.writeError(w, http.StatusBadGateway, "decrypt succeeded but plaintext digest mismatch — integrity check failed")
		return
	}

	ct := row.ContentType
	disposition := "attachment"
	if inlineImageTypes[ct] {
		disposition = "inline"
	} else {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(plaintext)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(plaintext)
}
