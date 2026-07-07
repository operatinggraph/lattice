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
	"strconv"
	"strings"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	// defaultUploadCap bounds a single object upload (OBJECTS_MAX_UPLOAD_BYTES).
	defaultUploadCap = 25 << 20 // 25 MiB

	attachReqNamespace = "object:attach:"
	detachReqNamespace = "object:detach:"

	// attachRetries bounds the re-read-and-retry loop on a concurrent
	// same-object RevisionConflict (CC5 convergence).
	attachRetries = 4
)

// inlineImageTypes are the content types Loupe streams back with
// Content-Disposition: inline so the Files tab can render them. Everything else
// (svg, html, pdf, octet-stream, …) is served as an attachment — an uploaded
// active document must never execute as same-origin script in the admin UI.
var inlineImageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
}

// join0 joins fields with the NUL byte (which cannot appear in any field) — the
// deriveID idiom (CC6) so disjoint field tuples never collide for a requestId.
func join0(parts ...string) string { return strings.Join(parts, "\x00") }

// objectDisposition decides how an object's bytes are served back through the
// admin UI. Only the raster-image allow-list keeps its declared content type and
// renders inline; every other type (svg / html / pdf / unknown) is forced to a
// neutral octet-stream attachment so an uploaded active document can never
// execute as same-origin script. Returns the content type to serve and the
// Content-Disposition value — the anti-XSS boundary, paired with the
// nosniff + sandbox CSP belt in handleObjectGet.
func objectDisposition(contentType string) (servedType, disposition string) {
	if inlineImageTypes[contentType] {
		return contentType, "inline"
	}
	return "application/octet-stream", "attachment"
}

// objectLinkKey reconstructs lnk.object.<oid>.<linkName>.<tgtType>.<tgtId> from
// the object id and the full vtx.<type>.<id> target — deterministic, no scan.
func objectLinkKey(oid, targetKey, linkName string) (string, error) {
	parts := strings.Split(targetKey, ".")
	if len(parts) != 3 || parts[0] != "vtx" {
		return "", fmt.Errorf("targetKey must be vtx.<type>.<id>: %q", targetKey)
	}
	return "lnk.object." + oid + "." + linkName + "." + parts[1] + "." + parts[2], nil
}

// handleObjects routes /api/objects:
//
//	POST   /api/objects                              → upload bytes + AttachObject
//	GET    /api/objects/<oid>                        → stream the bytes back
//	DELETE /api/objects/<oid>?targetKey=&linkName=   → DetachObject
func (s *server) handleObjects(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/objects")
	rest = strings.Trim(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "":
		if s.crossOriginBlocked(w, r) {
			return
		}
		s.handleObjectUpload(w, r)
	case r.Method == http.MethodGet && rest != "":
		s.handleObjectGet(w, r, rest)
	case r.Method == http.MethodDelete && rest != "":
		if s.crossOriginBlocked(w, r) {
			return
		}
		s.handleObjectDetach(w, r, rest)
	default:
		s.writeError(w, http.StatusBadRequest,
			"expected POST /api/objects, GET /api/objects/<oid>, or DELETE /api/objects/<oid>?targetKey=&linkName=")
	}
}

// handleObjectUpload implements POST /api/objects. It streams the file part to
// the core-objects store (cap enforced in substrate), derives the
// content-addressed oid, then submits AttachObject. Bytes first, then graph: a
// failed op leaves only collectable bytes, never a partial graph.
func (s *server) handleObjectUpload(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}

	// Bound the whole request so form parsing can't exhaust memory; the stream
	// cap inside ObjectPut is the authoritative per-blob limit.
	r.Body = http.MaxBytesReader(w, r.Body, s.uploadCap+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}
	targetKey := strings.TrimSpace(r.FormValue("targetKey"))
	linkName := strings.TrimSpace(r.FormValue("linkName"))
	replaceObjectID := strings.TrimSpace(r.FormValue("replaceObjectId"))
	sensitive := strings.TrimSpace(r.FormValue("sensitive")) == "true"
	governingIdentity := strings.TrimSpace(r.FormValue("governingIdentity"))
	if targetKey == "" || linkName == "" {
		s.writeError(w, http.StatusBadRequest, "targetKey and linkName form fields are required")
		return
	}
	if sensitive && governingIdentity == "" {
		s.writeError(w, http.StatusBadRequest, "governingIdentity form field is required when sensitive=true")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "a 'file' part is required: "+err.Error())
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(contentType) > 255 {
		contentType = contentType[:255]
	}

	ctx, cancel := s.gatewaySubmitContext(r)
	defer cancel()

	if sensitive {
		s.handleSensitiveObjectUpload(w, ctx, conn, file, header, contentType, targetKey, linkName, replaceObjectID, governingIdentity)
		return
	}

	// 1. Stream bytes under a provisional store name. The digest is computed by
	//    NATS during the Put and returned after.
	storeName, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "generate store name: "+err.Error())
		return
	}
	info, err := conn.ObjectPut(ctx, bootstrap.CoreObjectsBucket, storeName, file, s.uploadCap)
	if err != nil {
		if errors.Is(err, substrate.ErrObjectTooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload exceeds the %d-byte cap", s.uploadCap))
			return
		}
		s.writeError(w, http.StatusBadGateway, "store object bytes: "+err.Error())
		return
	}
	oid := substrate.SHA256NanoID("object:" + info.Digest)
	objKey := "vtx.object." + oid

	payload := map[string]any{
		"digest": info.Digest, "size": info.Size, "contentType": contentType,
		"storeName": storeName, "targetKey": targetKey, "linkName": linkName,
	}
	if header.Filename != "" {
		payload["filename"] = header.Filename
	}
	if replaceObjectID != "" {
		payload["replaceObjectId"] = replaceObjectID
	}

	reply, err := s.submitAttach(ctx, conn, oid, info.Digest, targetKey, linkName, replaceObjectID, payload)
	if err != nil {
		// The op never landed → our just-uploaded bytes are an orphan; reclaim.
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
		s.writeError(w, http.StatusBadGateway, "submit AttachObject: "+err.Error())
		return
	}
	if reply.Status == processor.ReplyStatusRejected {
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
		s.writeJSON(w, http.StatusBadRequest, reply)
		return
	}

	// Dedup self-heal (§10 byte-layer convergence): if the committed object
	// points at a DIFFERENT storeName, identical bytes already existed and ours
	// is a duplicate — reclaim it so it does not orphan.
	if canonical := s.objectStoreName(ctx, conn, objKey); canonical != "" && canonical != storeName {
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
	}

	// A duplicate (collapsed on the 24h tracker) carries no primaryKey, but the
	// link is committed — surface its deterministic key so the client can
	// address it.
	primaryKey := reply.PrimaryKey
	if primaryKey == "" {
		if lk, e := objectLinkKey(oid, targetKey, linkName); e == nil {
			primaryKey = lk
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"oid": oid, "primaryKey": primaryKey,
		"digest": info.Digest, "size": info.Size,
	})
}

// handleSensitiveObjectUpload implements the sensitive branch of POST
// /api/objects (object-store-crypto-shred-design.md §3.1/§3.5 Fire 2): the
// plaintext is read fully into memory (an AEAD seals a whole buffer, not a
// stream), encrypted client-side under a fresh per-object CEK, and only the
// CEK — never the bulk bytes — passes through the Vault (wrapped under
// governingIdentity's DEK). Ciphertext, not plaintext, is what streams to
// core-objects; the oid is identity-salted (§3.3) so a sensitive object is
// never cross-identity content-addressed.
func (s *server) handleSensitiveObjectUpload(w http.ResponseWriter, ctx context.Context, conn *substrate.Conn, file multipart.File, header *multipart.FileHeader, contentType, targetKey, linkName, replaceObjectID, governingIdentity string) {
	plaintext, err := io.ReadAll(io.LimitReader(file, s.uploadCap+1))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "read upload: "+err.Error())
		return
	}
	if int64(len(plaintext)) > s.uploadCap {
		s.writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds the %d-byte cap", s.uploadCap))
		return
	}

	// The oid is derived from the PLAINTEXT digest (post-decrypt integrity
	// claim), salted with the governing identity (§3.3) — NATS's own digest
	// (computed over whatever is streamed) would be the ciphertext's, which is
	// useless for that claim, so it is never used on this branch. Computed
	// before sealing so the oid can bind the content ciphertext as AEAD
	// associated data (below) — otherwise a `.content` document splice could
	// graft one object's ciphertext/envelope onto another's oid within the
	// same governing identity and still decrypt.
	plaintextDigest := sha256Digest(plaintext)
	oid := substrate.SHA256NanoID("object:" + governingIdentity + ":" + plaintextDigest)
	objKey := "vtx.object." + oid

	env, err := fetchPiiKeyEnvelope(ctx, conn, governingIdentity)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	cek, err := generateCEK()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	contentNonce, ciphertext, err := sealAESGCM(cek, plaintext, []byte(oid))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "encrypt: "+err.Error())
		return
	}
	wrapped, err := vaultWrapKey(ctx, conn, governingIdentity, env, cek)
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

	payload := map[string]any{
		"digest": plaintextDigest, "size": info.Size, "contentType": contentType,
		"storeName": storeName, "targetKey": targetKey, "linkName": linkName,
		"sensitive": true, "governingIdentity": governingIdentity,
		"encryption": map[string]any{
			"algo":       contentEncryptionAlgo,
			"nonce":      base64.StdEncoding.EncodeToString(contentNonce),
			"wrappedCEK": encodeWrappedCEK(wrapped),
			"keyId":      governingIdentity,
		},
	}
	if header.Filename != "" {
		payload["filename"] = header.Filename
	}
	if replaceObjectID != "" {
		payload["replaceObjectId"] = replaceObjectID
	}

	reply, err := s.submitAttach(ctx, conn, oid, plaintextDigest, targetKey, linkName, replaceObjectID, payload)
	if err != nil {
		// The op never landed → our just-uploaded bytes are an orphan; reclaim.
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
		s.writeError(w, http.StatusBadGateway, "submit AttachObject: "+err.Error())
		return
	}
	if reply.Status == processor.ReplyStatusRejected {
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
		s.writeJSON(w, http.StatusBadRequest, reply)
		return
	}

	// Dedup self-heal (§10 byte-layer convergence): if the committed object
	// points at a DIFFERENT storeName, an identical upload by this SAME
	// identity already exists (sensitive dedup is within-identity only, §3.3)
	// and ours is a duplicate — reclaim it so it does not orphan.
	if canonical := s.objectStoreName(ctx, conn, objKey); canonical != "" && canonical != storeName {
		_ = conn.ObjectDelete(ctx, bootstrap.CoreObjectsBucket, storeName)
	}

	primaryKey := reply.PrimaryKey
	if primaryKey == "" {
		if lk, e := objectLinkKey(oid, targetKey, linkName); e == nil {
			primaryKey = lk
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"oid": oid, "primaryKey": primaryKey,
		"digest": plaintextDigest, "size": info.Size,
	})
}

// submitAttach derives the deterministic requestId + the conditional read set
// and submits AttachObject, retrying on a concurrent same-object
// RevisionConflict (CC5). A declared-but-absent read key is a fatal hydration
// miss, so only keys that currently exist are declared.
func (s *server) submitAttach(ctx context.Context, conn *substrate.Conn, oid, digest, targetKey, linkName, replaceObjectID string, payload map[string]any) (*processor.OperationReply, error) {
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"
	linkKey, err := objectLinkKey(oid, targetKey, linkName)
	if err != nil {
		return nil, err
	}

	token := operatorToken(ctx)
	var lastReply *processor.OperationReply
	for attempt := 0; attempt < attachRetries; attempt++ {
		reads := []string{targetKey} // always — it must be a live vertex
		linkRev, linkTombstoned, linkPresent := s.keyState(ctx, conn, linkKey)
		if s.keyPresent(ctx, conn, objKey) {
			reads = append(reads, objKey)
		}
		if s.keyPresent(ctx, conn, contentKey) {
			reads = append(reads, contentKey)
		}
		if linkPresent {
			reads = append(reads, linkKey)
		}
		if replaceObjectID != "" {
			if oldLink, e := objectLinkKey(replaceObjectID, targetKey, linkName); e == nil && s.keyPresent(ctx, conn, oldLink) {
				reads = append(reads, oldLink)
			}
			if oldObj := "vtx.object." + replaceObjectID; s.keyPresent(ctx, conn, oldObj) {
				reads = append(reads, oldObj)
			}
		}

		// Deterministic requestId (CC6): content-derived so retries collapse on
		// the Contract #4 tracker. A re-attach over a tombstoned link is a
		// distinct user intent → salt with the tombstone revision so it is not
		// deduped against the original attach within the 24h horizon.
		seed := join0(digest, targetKey, linkName, replaceObjectID)
		if linkTombstoned {
			seed = join0(seed, strconv.FormatUint(linkRev, 10))
		}
		requestID := substrate.DeriveNanoID(attachReqNamespace, seed)

		greq := gatewayOperationRequest{
			RequestID:     requestID,
			Lane:          string(processor.LaneDefault),
			OperationType: "AttachObject",
			Class:         "object",
			Payload:       mustJSON(payload),
			Reads:         reads,
		}
		reply, err := submitOpViaGateway(ctx, s.gatewayURL, token, greq)
		if err != nil {
			return nil, err
		}
		lastReply = reply
		if reply.Status == processor.ReplyStatusRejected && reply.Error != nil &&
			reply.Error.Code == processor.ErrCodeRevisionConflict {
			continue // concurrent same-object change; re-read + retry
		}
		return reply, nil
	}
	return lastReply, nil
}

// objectContentDoc is the .content aspect shape this handler reads —
// carrying the sensitive-object encryption envelope alongside the
// unconditionally-present fields (object-store-crypto-shred-design.md §3.2).
type objectContentDoc struct {
	IsDeleted bool `json:"isDeleted"`
	Data      struct {
		StoreName         string `json:"storeName"`
		ContentType       string `json:"contentType"`
		Digest            string `json:"digest"`
		Sensitive         bool   `json:"sensitive"`
		GoverningIdentity string `json:"governingIdentity"`
		Encryption        struct {
			Algo       string `json:"algo"`
			Nonce      string `json:"nonce"`
			WrappedCEK string `json:"wrappedCEK"`
			KeyID      string `json:"keyId"`
		} `json:"encryption"`
	} `json:"data"`
}

// handleObjectGet implements GET /api/objects/<oid>[?decrypt=true]. It
// resolves the storeName from the object's .content aspect and streams the
// bytes (NATS verifies the digest as it streams). The Refractor is never in
// the byte path.
//
// A sensitive object's bytes are ciphertext at rest; the default response is
// that ciphertext, unreadable by construction — no read-path authorization
// needed (object-store-crypto-shred-design.md §3.4). `?decrypt=true` is the
// opt-in trusted-tool plaintext read: Loupe unwraps the CEK via the Vault and
// decrypts locally. Non-sensitive objects ignore the parameter (already
// plaintext).
func (s *server) handleObjectGet(w http.ResponseWriter, r *http.Request, oid string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if !substrate.IsValidNanoID(oid) {
		s.writeError(w, http.StatusBadRequest, "invalid object id")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, "vtx.object."+oid+".content")
	if err != nil {
		s.writeError(w, http.StatusNotFound, "object not found")
		return
	}
	var doc objectContentDoc
	if json.Unmarshal(entry.Value, &doc) != nil || doc.Data.StoreName == "" {
		s.writeError(w, http.StatusBadGateway, "object metadata unreadable")
		return
	}
	if doc.IsDeleted {
		s.writeError(w, http.StatusNotFound, "object is deleted")
		return
	}

	if doc.Data.Sensitive && r.URL.Query().Get("decrypt") == "true" {
		s.handleSensitiveObjectDecrypt(w, ctx, conn, oid, doc)
		return
	}

	rc, info, err := conn.ObjectGet(ctx, bootstrap.CoreObjectsBucket, doc.Data.StoreName)
	if err != nil {
		if errors.Is(err, substrate.ErrObjectNotFound) {
			s.writeError(w, http.StatusNotFound, "object bytes not found")
			return
		}
		s.writeError(w, http.StatusBadGateway, "read object bytes: "+err.Error())
		return
	}
	defer rc.Close()

	// The CSP is the belt to objectDisposition's braces: even if a byte stream
	// is loaded as a sub-resource, nothing may run from it.
	ct, disposition := objectDisposition(doc.Data.ContentType)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatUint(info.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// handleSensitiveObjectDecrypt implements the `?decrypt=true` opt-in read for
// a sensitive object: unwrap the CEK via the Vault, decrypt the ciphertext
// locally, verify the GCM tag and the plaintext digest, then serve the
// plaintext under the same anti-XSS disposition/CSP posture as the default
// path (object-store-crypto-shred-design.md §3.4).
func (s *server) handleSensitiveObjectDecrypt(w http.ResponseWriter, ctx context.Context, conn *substrate.Conn, oid string, doc objectContentDoc) {
	wrapped, err := decodeWrappedCEK(doc.Data.Encryption.WrappedCEK)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decode wrappedCEK: "+err.Error())
		return
	}
	env, err := fetchPiiKeyEnvelope(ctx, conn, doc.Data.GoverningIdentity)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	cek, err := vaultUnwrapKey(ctx, conn, doc.Data.GoverningIdentity, env, wrapped)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "unwrap CEK: "+err.Error())
		return
	}
	contentNonce, err := base64.StdEncoding.DecodeString(doc.Data.Encryption.Nonce)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decode content nonce: "+err.Error())
		return
	}

	rc, _, err := conn.ObjectGet(ctx, bootstrap.CoreObjectsBucket, doc.Data.StoreName)
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

	plaintext, err := openAESGCM(cek, contentNonce, ciphertext, []byte(oid))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "decrypt failed: tampered or corrupt ciphertext")
		return
	}
	if sha256Digest(plaintext) != doc.Data.Digest {
		s.writeError(w, http.StatusBadGateway, "decrypt succeeded but plaintext digest mismatch — integrity check failed")
		return
	}

	ct, disposition := objectDisposition(doc.Data.ContentType)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(plaintext)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(plaintext)
}

// handleObjectDetach implements DELETE /api/objects/<oid>?targetKey=&linkName=.
func (s *server) handleObjectDetach(w http.ResponseWriter, r *http.Request, oid string) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if !substrate.IsValidNanoID(oid) {
		s.writeError(w, http.StatusBadRequest, "invalid object id")
		return
	}
	targetKey := strings.TrimSpace(r.URL.Query().Get("targetKey"))
	linkName := strings.TrimSpace(r.URL.Query().Get("linkName"))
	if targetKey == "" || linkName == "" {
		s.writeError(w, http.StatusBadRequest, "targetKey and linkName query params are required")
		return
	}
	linkKey, err := objectLinkKey(oid, targetKey, linkName)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	objKey := "vtx.object." + oid

	ctx, cancel := s.gatewaySubmitContext(r)
	defer cancel()

	reads := []string{}
	linkRev, _, linkPresent := s.keyState(ctx, conn, linkKey)
	if linkPresent {
		reads = append(reads, linkKey)
	}
	if s.keyPresent(ctx, conn, objKey) {
		reads = append(reads, objKey)
	}

	// Salt the requestId with the link's current revision so a retry collapses
	// while a re-detach of a since-revived link is a distinct intent (CC6).
	requestID := substrate.DeriveNanoID(detachReqNamespace,
		join0(oid, targetKey, linkName, strconv.FormatUint(linkRev, 10)))
	greq := gatewayOperationRequest{
		RequestID:     requestID,
		Lane:          string(processor.LaneDefault),
		OperationType: "DetachObject",
		Class:         "object",
		Payload:       mustJSON(map[string]any{"oid": oid, "targetKey": targetKey, "linkName": linkName}),
		Reads:         reads,
	}
	reply, err := submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), greq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "submit DetachObject: "+err.Error())
		return
	}
	status := http.StatusOK
	if reply.Status == processor.ReplyStatusRejected {
		status = http.StatusBadRequest
	}
	s.writeJSON(w, status, reply)
}

// keyPresent reports whether key has a Core KV entry (alive OR soft-tombstoned —
// KVGet returns logically-deleted entries by design). Used to decide whether a
// key is safe to declare in ContextHint.Reads.
func (s *server) keyPresent(ctx context.Context, conn *substrate.Conn, key string) bool {
	_, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	return err == nil
}

// keyState returns (revision, isTombstoned, present) for a Core KV key.
func (s *server) keyState(ctx context.Context, conn *substrate.Conn, key string) (revision uint64, tombstoned bool, present bool) {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, key)
	if err != nil {
		return 0, false, false
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	_ = json.Unmarshal(entry.Value, &doc)
	return entry.Revision, doc.IsDeleted, true
}

// objectStoreName returns the storeName recorded on vtx.object.<oid>.content,
// or "" if the object/content is absent.
func (s *server) objectStoreName(ctx context.Context, conn *substrate.Conn, objKey string) string {
	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, objKey+".content")
	if err != nil {
		return ""
	}
	var doc struct {
		Data struct {
			StoreName string `json:"storeName"`
		} `json:"data"`
	}
	if json.Unmarshal(entry.Value, &doc) != nil {
		return ""
	}
	return doc.Data.StoreName
}
