package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// attachmentsKeyPrefix is the OutputKeyPattern prefix of the objects-base
// `objectAttachments` display lens. The Documents tab reads these rows out of
// the shared weaver-targets read model — never Core KV (P5). Loupe scans Core KV
// because it is the admin inspector; a vertical app reads projections.
const attachmentsKeyPrefix = "objectAttachments."

// inlineImageTypes are the content types streamed back inline so the browser can
// render a thumbnail; everything else (pdf, svg, html, unknown) is forced to a
// neutral octet-stream attachment so an uploaded active document can never run
// as same-origin script.
var inlineImageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
}

// attachmentRow is one projected `objectAttachments` row — the byte-plane
// metadata for a single object plus the owner keys it is linked to. owners is
// the list filter input for "documents for this applicant". Sensitive /
// GoverningIdentity / Encryption / Digest carry the crypto-shred envelope
// (object-store-crypto-shred-design.md §9 Fire 4 Increment 2) — all null/zero
// for a non-sensitive object, since the DDL never writes those keys for one.
type attachmentRow struct {
	EntityKey         string `json:"entityKey"`
	StoreName         string `json:"storeName"`
	ContentType       string `json:"contentType"`
	Size              int64  `json:"size"`
	Digest            string `json:"digest"`
	Sensitive         bool   `json:"sensitive"`
	GoverningIdentity string `json:"governingIdentity"`
	Encryption        struct {
		Algo       string `json:"algo"`
		Nonce      string `json:"nonce"`
		WrappedCEK string `json:"wrappedCEK"`
		KeyID      string `json:"keyId"`
	} `json:"encryption"`
	Owners []struct {
		OwnerKey string `json:"ownerKey"`
	} `json:"owners"`
}

// documentView is the Documents-tab projection of one attached object: the oid
// (the stable address for view / detach), its display metadata, and the owner it
// is attached to within the applicant's scope. Sensitive tells the FE to
// request the decrypt-capable read (?decrypt=true) rather than the ciphertext
// default (object-store-crypto-shred-design.md §9 Fire 4 Increment 2).
type documentView struct {
	OID         string `json:"oid"`
	OwnerKey    string `json:"ownerKey"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Sensitive   bool   `json:"sensitive"`
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

// vtxType returns the <type> segment of a vtx.<type>.<id> key, or "" if key
// does not have that shape (including a blank type or id segment — a
// malformed key must never resolve to a real type).
func vtxType(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "vtx" || parts[1] == "" || parts[2] == "" {
		return ""
	}
	return parts[1]
}

// isPublicObjectOwner reports whether ownerKey is a unit — the ONLY owner type
// the Documents/objects surface serves without authentication (D1.5). Unit-owned
// objects are listing photos: publicly browsable, the same classification
// `/api/listings` already carries (marketing content, never PII). Every other
// owner type (identity, leaseapp) is applicant-scoped document content
// (proof-of-income, ID scans, the signed lease) and requires authenticateRead +
// entitlement below.
func isPublicObjectOwner(ownerKey string) bool {
	return vtxType(ownerKey) == "unit"
}

// entitledToObjectOwner reports whether actorID (a verified read-actor's bare
// identity NanoID) is entitled to read objects owned by ownerKey — an identity
// or leaseapp vtx key (isPublicObjectOwner has already cleared unit keys).
//
//   - identity owner: entitled iff ownerKey IS the actor's own identity (the
//     actor reading their own ID scan / proof-of-income upload).
//   - leaseapp owner: entitled iff the PROTECTED, RLS-scoped
//     read_lease_applications model resolves ownerKey for this actor — the
//     same applicant-only membership check handleApplications/
//     handleLeaseDocumentGet already condition their reads on (D1.3 Fire 2).
//
// A nil pgPool (Postgres read boundary not configured) fails closed: a
// leaseapp-owned object is simply not entitled, never silently allowed.
func (s *server) entitledToObjectOwner(ctx context.Context, actorID, ownerKey string) bool {
	switch vtxType(ownerKey) {
	case "identity":
		return ownerKey == "vtx.identity."+actorID
	case "leaseapp":
		if s.pgPool == nil {
			return false
		}
		_, ok, err := queryApplicationByKey(ctx, s.pgPool, actorID, ownerKey)
		return err == nil && ok
	default:
		return false
	}
}

// handleObjects routes /api/objects:
//
//	POST /api/objects                             → upload bytes (byte-plane only; the browser
//	                                                 submits AttachObject itself, browser-direct)
//	GET  /api/objects?owner=     (or ?applicant=)  → list objects scoped to an owner key
//	GET  /api/objects/<oid>                        → stream the bytes back
//
// AttachObject/DetachObject are submitted browser-direct through the Gateway's
// POST /v1/operations (real-actor-write-auth-e2e; #75 Fire 2b) — this app never
// asserts an actor for the anchor op, so it holds no route for either.
func (s *server) handleObjects(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/objects")
	rest = strings.Trim(rest, "/")
	switch {
	case r.Method == http.MethodPost && rest == "":
		s.handleObjectUpload(w, r)
	case r.Method == http.MethodGet && rest == "":
		s.handleObjectList(w, r)
	case r.Method == http.MethodGet && rest != "":
		s.handleObjectGet(w, r, rest)
	default:
		s.writeError(w, http.StatusBadRequest,
			"expected POST /api/objects, GET /api/objects?applicant=, or GET /api/objects/<oid>")
	}
}

// computeDocuments assembles the Documents rows from the `objectAttachments` lens
// read model: it keeps the lens-prefixed keys, decodes each row, and — when owners
// is non-empty — keeps only objects linked to one of those owner keys (the
// applicant's identity + each of their applications; the trusted-tool view scope).
// An empty owners set lists every owned object (the operator-style view). Rows
// sort by oid for a stable view.
func computeDocuments(keys []string, get kvGetter, owners []string) []documentView {
	scope := make(map[string]bool, len(owners))
	for _, o := range owners {
		if o != "" {
			scope[o] = true
		}
	}
	out := make([]documentView, 0)
	for _, k := range keys {
		if !strings.HasPrefix(k, attachmentsKeyPrefix) {
			continue
		}
		raw, ok := get(k)
		if !ok {
			continue
		}
		var row attachmentRow
		if json.Unmarshal(raw, &row) != nil || row.EntityKey == "" {
			continue
		}
		oid := strings.TrimPrefix(row.EntityKey, "vtx.object.")
		matched := ""
		for _, o := range row.Owners {
			if o.OwnerKey == "" {
				continue // the degenerate {ownerKey:null} artifact of a zero-link object
			}
			if len(scope) == 0 || scope[o.OwnerKey] {
				matched = o.OwnerKey
				break
			}
		}
		if matched == "" {
			continue // not in this applicant's scope (or fully detached)
		}
		out = append(out, documentView{
			OID: oid, OwnerKey: matched, ContentType: row.ContentType, Size: row.Size,
			Sensitive: row.Sensitive,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// handleObjectList implements GET /api/objects?owner= — the objects attached to
// one or more owner keys, served from the `objectAttachments` lens rows in the
// shared weaver-targets read model (NOT Core KV; P5). The owner key is generic: an
// applicant's leaseapp / identity (the Documents tab) OR a unit (listing photos).
// `owner` may repeat (`?owner=a&owner=b`) to union an applicant's identity + every
// application into one "all my documents" view. `applicant` is accepted as a
// backward-compatible single-owner alias.
//
// D1.5 — split by owner type (isPublicObjectOwner): a **unit** owner (listing
// photos) is served unauthenticated, mirroring `/api/listings`'s deliberate public
// classification. Every other owner (identity / leaseapp — applicant document
// content: proof-of-income, ID scans, the signed lease) requires
// authenticateRead + entitledToObjectOwner; a non-entitled or unauthenticated
// request simply drops that owner from the result (the same "can't tell
// not-mine from not-real" posture the RLS reads use), never erroring the whole
// call just because ONE requested owner isn't the caller's. An owner-less
// request ("list every object") is no longer served — the D1.5 shape closes
// exactly that unauthenticated full-dump vector — the caller must scope by at
// least one owner.
func (s *server) handleObjectList(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	var requested []string
	for _, o := range r.URL.Query()["owner"] {
		if o = strings.TrimSpace(o); o != "" {
			requested = append(requested, o)
		}
	}
	if len(requested) == 0 {
		if a := strings.TrimSpace(r.URL.Query().Get("applicant")); a != "" {
			requested = append(requested, a)
		}
	}

	allowed, status, msg := s.resolveAllowedObjectOwners(ctx, r, requested)
	if status != 0 {
		s.writeError(w, status, msg)
		return
	}
	if len(allowed) == 0 {
		// computeDocuments treats an EMPTY scope as "no filter — list everything"
		// (the wildcard shape the operator/manage-photos view relies on); every
		// requested owner having been rejected must NOT fall through to that
		// wildcard, or a caller could probe an unentitled protected owner and get
		// every object in the system back. Short-circuit to an empty result.
		s.writeJSON(w, http.StatusOK, map[string]any{"documents": []documentView{}, "count": 0})
		return
	}

	bucket := bootstrap.WeaverTargetsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is objects-base installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	docs := computeDocuments(keys, get, allowed)
	s.writeJSON(w, http.StatusOK, map[string]any{"documents": docs, "count": len(docs)})
}

// resolveAllowedObjectOwners applies D1.5 to a requested owner-key list (pure
// decision logic, no NATS read — independently testable): requiring at least
// one owner (closes the unauthenticated full-dump vector), splitting
// public/protected via isPublicObjectOwner, and — only when a protected owner
// was requested — authenticating the caller and keeping only the entitled
// subset (entitledToObjectOwner). status != 0 means the caller should write
// that status/msg and stop; status == 0 with an empty `allowed` means "return
// an empty result", not "list everything" (see the call site's comment).
func (s *server) resolveAllowedObjectOwners(ctx context.Context, r *http.Request, requested []string) (allowed []string, status int, msg string) {
	if len(requested) == 0 {
		return nil, http.StatusBadRequest, "at least one owner= (or applicant=) query param is required"
	}

	var publicOwners, protectedOwners []string
	for _, o := range requested {
		if isPublicObjectOwner(o) {
			publicOwners = append(publicOwners, o)
		} else {
			protectedOwners = append(protectedOwners, o)
		}
	}

	allowed = publicOwners
	if len(protectedOwners) > 0 {
		actor, err := s.authenticateRead(r)
		if err != nil {
			return nil, http.StatusUnauthorized, "authentication required: " + err.Error()
		}
		for _, o := range protectedOwners {
			if s.entitledToObjectOwner(ctx, actor.Subject, o) {
				allowed = append(allowed, o)
			}
		}
	}
	return allowed, 0, ""
}

// handleObjectUpload implements POST /api/objects — the byte-plane half only.
// It streams the file part to the core-objects store (cap enforced in
// substrate) and hands back the content-addressed oid plus everything the
// browser needs to submit AttachObject itself, browser-direct through the
// Gateway (#75 Fire 2b: this app never asserts an actor for the anchor op). A
// browser that uploads bytes but never follows up with AttachObject leaves an
// orphan; object-store-manager's never-attached reconcile (a low-cadence
// backstop, internal/objectmanager) reclaims it after its grace window, so no
// compensating delete is needed here.
//
// sensitive=true (object-store-crypto-shred-design.md §9 Fire 4 Increment 2)
// branches to the crypto-shred path: the byte-plane write still happens here
// (unchanged from #75 — only the AttachObject SUBMISSION moved browser-side),
// but the bytes are sealed under a per-object CEK before they ever reach
// core-objects, and the response carries the encryption envelope for the
// browser to fold into its AttachObject payload. governingIdentity must equal
// the caller's OWN authenticated identity: unlike Loupe (an admin console the
// Vault's wrap/unwrap RPC already trusts wholesale), an applicant browser
// session is not equivalently trusted, so this app enforces the same
// ownership check entitledToObjectOwner's identity branch already makes —
// self-only, so a legitimate upload through THIS endpoint can only ever wrap
// under the caller's own DEK.
//
// This does NOT close every confused-deputy path: AttachObject's own
// submission is browser-direct via the Gateway's shared "staff" trusted-tool
// credential (#75 Fire 2b — staffReadToken carries no real per-applicant
// subject, and operator already holds AttachObject scope:any), so a caller
// who already holds that credential could submit AttachObject directly,
// bypassing this handler, with a self-consistent-looking but Vault-unwrapped
// (forged) encryption block claiming an arbitrary governingIdentity — the
// SAME pre-existing trust boundary every other AttachObject field
// (targetKey/linkName/storeName) already has today, not something this
// increment introduces or could close alone (closing it needs a real
// consumer scope=self grant for AttachObject, mirroring
// CreateLeaseApplication's; flagged as a residual, not built here). Bounded:
// no plaintext ever leaks this way — Vault's AEAD tag rejects a forged
// wrappedCEK on decrypt — the exposure is a spurious governingIdentity
// attribution on an object the forger already controls, not a
// confidentiality breach.
func (s *server) handleObjectUpload(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.uploadCap+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}
	targetKey := strings.TrimSpace(r.FormValue("targetKey"))
	linkName := strings.TrimSpace(r.FormValue("linkName"))
	sensitive := strings.TrimSpace(r.FormValue("sensitive")) == "true"
	governingIdentity := strings.TrimSpace(r.FormValue("governingIdentity"))
	if targetKey == "" || linkName == "" {
		s.writeError(w, http.StatusBadRequest, "targetKey and linkName form fields are required")
		return
	}
	if _, err := objectLinkKey("x", targetKey, linkName); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if sensitive {
		if governingIdentity == "" {
			s.writeError(w, http.StatusBadRequest, "governingIdentity form field is required when sensitive=true")
			return
		}
		actor, err := s.authenticateRead(r)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
			return
		}
		if governingIdentity != "vtx.identity."+actor.Subject {
			s.writeError(w, http.StatusForbidden, "governingIdentity must be your own identity")
			return
		}
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

	ctx, cancel := s.reqContext(r)
	defer cancel()

	if sensitive {
		s.handleSensitiveObjectUpload(w, ctx, conn, file, contentType, governingIdentity)
		return
	}

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
	resp := map[string]any{
		"oid": oid, "digest": info.Digest, "storeName": storeName,
		"size": info.Size, "contentType": contentType,
	}
	if header.Filename != "" {
		resp["filename"] = header.Filename
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// handleObjectGet implements GET /api/objects/<oid>[?decrypt=true]. It
// resolves the storeName from the `objectAttachments` lens read model (NOT
// Core KV; P5) and streams the bytes (NATS verifies the digest as it
// streams). The Refractor is never in the byte path.
//
// A sensitive object's bytes are ciphertext at rest; the default response is
// that ciphertext, unreadable by construction — no additional read-path
// authorization needed beyond the ordinary owner entitlement below
// (object-store-crypto-shred-design.md §3.4/§9 Fire 4 Increment 2).
// `?decrypt=true` is the opt-in plaintext read, gated behind the SAME
// authorizeObjectGet entitlement every object read already requires (an
// identity-owned sensitive document is the applicant's own upload, so the
// existing self-ownership check is exactly the right gate — no separate
// crypto-specific authorization is needed).
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

	row, ok := s.resolveObject(ctx, conn, oid)
	if !ok {
		s.writeError(w, http.StatusNotFound, "object not found in the read model")
		return
	}
	var owners []string
	for _, o := range row.Owners {
		if o.OwnerKey != "" {
			owners = append(owners, o.OwnerKey)
		}
	}
	if authOK, status, msg := s.authorizeObjectGet(ctx, r, owners); !authOK {
		s.writeError(w, status, msg)
		return
	}

	if row.Sensitive && r.URL.Query().Get("decrypt") == "true" {
		s.handleSensitiveObjectDecrypt(w, ctx, conn, oid, row)
		return
	}

	rc, info, err := conn.ObjectGet(ctx, bootstrap.CoreObjectsBucket, row.StoreName)
	if err != nil {
		if errors.Is(err, substrate.ErrObjectNotFound) {
			s.writeError(w, http.StatusNotFound, "object bytes not found")
			return
		}
		s.writeError(w, http.StatusBadGateway, "read object bytes: "+err.Error())
		return
	}
	defer rc.Close()

	// Only the raster-image allow-list is served with its declared type + inline;
	// every other type (svg / html / pdf / unknown) is forced to a neutral
	// octet-stream attachment so an uploaded active document can never run as
	// same-origin script. The CSP is the belt.
	ct := row.ContentType
	disposition := "attachment"
	if inlineImageTypes[ct] {
		disposition = "inline"
	} else {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatUint(info.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// resolveObject finds an object's full `objectAttachments` row by oid (NOT
// Core KV; P5). It lists the lens keys and matches the row whose entityKey is
// vtx.object.<oid> — the same list-and-filter pattern the other vertical-app
// readers use.
func (s *server) resolveObject(ctx context.Context, conn *substrate.Conn, oid string) (row attachmentRow, ok bool) {
	bucket := bootstrap.WeaverTargetsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		return attachmentRow{}, false
	}
	want := "vtx.object." + oid
	for _, k := range keys {
		if !strings.HasPrefix(k, attachmentsKeyPrefix) {
			continue
		}
		entry, err := conn.KVGet(ctx, bucket, k)
		if err != nil {
			continue
		}
		var r attachmentRow
		if json.Unmarshal(entry.Value, &r) != nil {
			continue
		}
		if r.EntityKey == want && r.StoreName != "" {
			return r, true
		}
	}
	return attachmentRow{}, false
}

// authorizeObjectGet enforces D1.5 on a resolved object's owners before its
// bytes stream, mirroring resolveAllowedObjectOwners' per-owner-type logic
// exactly rather than a single any-owner-is-public shortcut: an object is
// public ONLY when it has ZERO protected (identity/leaseapp) owners — a unit
// owner never grants access to a DIFFERENT, protected owner also linked to
// the same object (a content-addressed object can carry more than one link;
// the fix closes the case where a unit link would otherwise leak a co-owned
// identity/leaseapp link with no auth at all). Otherwise it requires
// authenticateRead + entitledToObjectOwner against at least one protected
// owner. A missing/invalid token is 401 (nothing to hide — every protected
// object requires SOME credential); an authenticated-but-not-entitled
// request gets the same 404 a genuinely absent object would, so the caller
// cannot tell "not yours" from "doesn't exist" (the posture
// handleLeaseDocumentGet already established). No owners at all (the
// degenerate zero-link case) is never public and can never be entitled.
func (s *server) authorizeObjectGet(ctx context.Context, r *http.Request, owners []string) (ok bool, status int, msg string) {
	var protectedOwners []string
	for _, o := range owners {
		if !isPublicObjectOwner(o) {
			protectedOwners = append(protectedOwners, o)
		}
	}
	if len(owners) > 0 && len(protectedOwners) == 0 {
		return true, 0, ""
	}
	actor, err := s.authenticateRead(r)
	if err != nil {
		return false, http.StatusUnauthorized, "authentication required: " + err.Error()
	}
	for _, o := range protectedOwners {
		if s.entitledToObjectOwner(ctx, actor.Subject, o) {
			return true, 0, ""
		}
	}
	return false, http.StatusNotFound, "object not found in the read model"
}
