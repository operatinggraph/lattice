package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestComputeDocuments_ScopesToOwnerAndReshapes proves the Documents assembler
// keeps only objectAttachments-prefixed rows, scopes them to the applicant's
// owner key, drops the degenerate {ownerKey:null} artifact, and reshapes each
// surviving row to its oid + display metadata.
func TestComputeDocuments_ScopesToOwnerAndReshapes(t *testing.T) {
	const leaseapp = "vtx.leaseapp.app1"
	entries := map[string]string{
		// mine — a pdf attached to my leaseapp
		"objectAttachments.aaa": `{"entityKey":"vtx.object.aaa","storeName":"s1","contentType":"application/pdf","size":4096,"owners":[{"ownerKey":"vtx.leaseapp.app1"}]}`,
		// someone else's — different owner, must be excluded when scoped
		"objectAttachments.bbb": `{"entityKey":"vtx.object.bbb","storeName":"s2","contentType":"image/png","size":100,"owners":[{"ownerKey":"vtx.leaseapp.other"}]}`,
		// fully detached — only the null artifact, must be excluded
		"objectAttachments.ccc": `{"entityKey":"vtx.object.ccc","storeName":"s3","contentType":"image/png","size":50,"owners":[{"ownerKey":null}]}`,
		// not a lens row — a leaseApplicationComplete projection sharing the bucket
		"leaseApplicationComplete.zzz": `{"entityKey":"vtx.leaseapp.app1"}`,
		// undecodable — skipped, never panics
		"objectAttachments.ddd": `{`,
	}

	docs := computeDocuments(keysOf(entries), fakeKV(entries), []string{leaseapp})
	if len(docs) != 1 {
		t.Fatalf("want exactly my 1 document, got %d: %+v", len(docs), docs)
	}
	d := docs[0]
	if d.OID != "aaa" {
		t.Errorf("oid: want aaa (stripped of the vtx.object. prefix), got %q", d.OID)
	}
	if d.OwnerKey != leaseapp {
		t.Errorf("ownerKey: want %q, got %q", leaseapp, d.OwnerKey)
	}
	if d.ContentType != "application/pdf" || d.Size != 4096 {
		t.Errorf("metadata mismatch: %+v", d)
	}
}

// TestComputeDocuments_UnscopedListsAll proves an empty applicant lists every
// document that has at least one real owner (the operator-style view).
func TestComputeDocuments_UnscopedListsAll(t *testing.T) {
	entries := map[string]string{
		"objectAttachments.aaa": `{"entityKey":"vtx.object.aaa","storeName":"s1","contentType":"application/pdf","size":1,"owners":[{"ownerKey":"vtx.leaseapp.app1"}]}`,
		"objectAttachments.bbb": `{"entityKey":"vtx.object.bbb","storeName":"s2","contentType":"image/png","size":2,"owners":[{"ownerKey":"vtx.identity.id2"}]}`,
		// no real owner — excluded even when unscoped
		"objectAttachments.ccc": `{"entityKey":"vtx.object.ccc","storeName":"s3","owners":[{"ownerKey":null}]}`,
	}
	docs := computeDocuments(keysOf(entries), fakeKV(entries), nil)
	if len(docs) != 2 {
		t.Fatalf("want 2 owned documents, got %d: %+v", len(docs), docs)
	}
	// sorted by oid for a stable view
	if docs[0].OID != "aaa" || docs[1].OID != "bbb" {
		t.Errorf("want oid-sorted [aaa bbb], got [%s %s]", docs[0].OID, docs[1].OID)
	}
}

// TestComputeDocuments_UnionsMultipleOwners proves the "all my documents" view:
// a set of owner keys (the applicant's identity + each application) unions every
// document linked to any of them, while documents owned only by an out-of-scope
// key stay excluded — and each surviving row reports the in-scope owner it matched.
func TestComputeDocuments_UnionsMultipleOwners(t *testing.T) {
	const identity = "vtx.identity.me"
	const app1 = "vtx.leaseapp.app1"
	const app2 = "vtx.leaseapp.app2"
	entries := map[string]string{
		// mine — one on my identity, one on each of my two applications
		"objectAttachments.aaa": `{"entityKey":"vtx.object.aaa","storeName":"s1","contentType":"image/png","size":1,"owners":[{"ownerKey":"vtx.identity.me"}]}`,
		"objectAttachments.bbb": `{"entityKey":"vtx.object.bbb","storeName":"s2","contentType":"application/pdf","size":2,"owners":[{"ownerKey":"vtx.leaseapp.app1"}]}`,
		"objectAttachments.ccc": `{"entityKey":"vtx.object.ccc","storeName":"s3","contentType":"application/pdf","size":3,"owners":[{"ownerKey":"vtx.leaseapp.app2"}]}`,
		// someone else's application — excluded from my union
		"objectAttachments.ddd": `{"entityKey":"vtx.object.ddd","storeName":"s4","contentType":"image/png","size":4,"owners":[{"ownerKey":"vtx.leaseapp.other"}]}`,
	}

	docs := computeDocuments(keysOf(entries), fakeKV(entries), []string{identity, app1, app2})
	if len(docs) != 3 {
		t.Fatalf("want my 3 documents unioned across identity + 2 apps, got %d: %+v", len(docs), docs)
	}
	want := map[string]string{"aaa": identity, "bbb": app1, "ccc": app2}
	for _, d := range docs {
		if want[d.OID] != d.OwnerKey {
			t.Errorf("doc %s: want owner %q, got %q", d.OID, want[d.OID], d.OwnerKey)
		}
	}
}

// TestIsPublicObjectOwner proves the D1.5 owner-type classification: only a
// unit key (listing photos) is public; identity/leaseapp keys (applicant
// document content) and anything malformed are NOT — the fail-closed default
// for an owner type nobody has classified.
func TestIsPublicObjectOwner(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"vtx.unit.u1", true},
		{"vtx.identity.id1", false},
		{"vtx.leaseapp.app1", false},
		{"vtx.somethingnew.x1", false}, // unclassified type — fail closed, not public
		{"not-a-vtx-key", false},
	}
	for _, c := range cases {
		if got := isPublicObjectOwner(c.key); got != c.want {
			t.Errorf("isPublicObjectOwner(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

// TestEntitledToObjectOwner_Identity proves the identity branch (no Postgres
// needed): an actor is entitled to their OWN identity-owned objects only.
func TestEntitledToObjectOwner_Identity(t *testing.T) {
	s := &server{logger: discardLogger()}
	ctx := context.Background()
	if !s.entitledToObjectOwner(ctx, "alice", "vtx.identity.alice") {
		t.Error("alice must be entitled to her own identity-owned objects")
	}
	if s.entitledToObjectOwner(ctx, "alice", "vtx.identity.bob") {
		t.Error("alice must NOT be entitled to bob's identity-owned objects")
	}
}

// TestEntitledToObjectOwner_LeaseappFailsClosedWithoutPostgres proves a
// leaseapp-owned object is NEVER entitled when the Postgres read boundary
// isn't configured — fail closed, never silently allowed.
func TestEntitledToObjectOwner_LeaseappFailsClosedWithoutPostgres(t *testing.T) {
	s := &server{logger: discardLogger(), pgPool: nil}
	if s.entitledToObjectOwner(context.Background(), "alice", "vtx.leaseapp.app1") {
		t.Error("a leaseapp owner must fail closed when pgPool is nil")
	}
}

// TestEntitledToObjectOwner_UnknownTypeDenied proves an owner key of an
// unrecognized vtx type is never entitled (fail-closed default arm).
func TestEntitledToObjectOwner_UnknownTypeDenied(t *testing.T) {
	s := &server{logger: discardLogger()}
	if s.entitledToObjectOwner(context.Background(), "alice", "vtx.somethingnew.x1") {
		t.Error("an unclassified owner type must never be entitled")
	}
}

// TestResolveAllowedObjectOwners_RequiresAtLeastOneOwner proves the D1.5
// close of the "list every object" unauthenticated full-dump vector: an
// owner-less request is rejected, not defaulted to "list everything".
func TestResolveAllowedObjectOwners_RequiresAtLeastOneOwner(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects", nil)
	_, status, _ := s.resolveAllowedObjectOwners(context.Background(), r, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

// TestResolveAllowedObjectOwners_PublicOwnerNeedsNoAuth proves a unit owner
// (listing photos) is allowed through with no Authorization header at all —
// mirrors /api/listings' deliberate public classification.
func TestResolveAllowedObjectOwners_PublicOwnerNeedsNoAuth(t *testing.T) {
	s := &server{logger: discardLogger()} // authn is nil — any protected owner would 401
	r := httptest.NewRequest(http.MethodGet, "/api/objects?owner=vtx.unit.u1", nil)
	allowed, status, msg := s.resolveAllowedObjectOwners(context.Background(), r, []string{"vtx.unit.u1"})
	if status != 0 {
		t.Fatalf("status = %d (%s), want 0 (no auth required for a unit owner)", status, msg)
	}
	if len(allowed) != 1 || allowed[0] != "vtx.unit.u1" {
		t.Fatalf("allowed = %v, want [vtx.unit.u1]", allowed)
	}
}

// TestResolveAllowedObjectOwners_ProtectedOwnerRequiresAuth proves a
// protected (identity/leaseapp) owner 401s when the caller presents no
// Bearer token — the read boundary not configured (authn nil) fails closed
// through the same authenticateRead path every other D1 read uses.
func TestResolveAllowedObjectOwners_ProtectedOwnerRequiresAuth(t *testing.T) {
	s := &server{logger: discardLogger()} // no authn configured
	r := httptest.NewRequest(http.MethodGet, "/api/objects?owner=vtx.identity.alice", nil)
	_, status, _ := s.resolveAllowedObjectOwners(context.Background(), r, []string{"vtx.identity.alice"})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestResolveAllowedObjectOwners_MixedPublicAndProtectedStillRequiresAuth
// proves that requesting a public owner alongside a protected one still
// gates the whole call on authentication (a defensive default the current FE
// never actually exercises — it never mixes unit keys with identity/leaseapp
// keys in one call — but must fail closed if it ever did).
func TestResolveAllowedObjectOwners_MixedPublicAndProtectedStillRequiresAuth(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects?owner=vtx.unit.u1&owner=vtx.identity.alice", nil)
	_, status, _ := s.resolveAllowedObjectOwners(context.Background(), r,
		[]string{"vtx.unit.u1", "vtx.identity.alice"})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (protected owner present, no auth)", status)
	}
}

// TestAuthorizeObjectGet_PublicOwnerBypassesAuth proves a unit-owned object
// (a listing photo) streams with NO Authorization header.
func TestAuthorizeObjectGet_PublicOwnerBypassesAuth(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
	ok, status, _ := s.authorizeObjectGet(context.Background(), r, []string{"vtx.unit.u1"})
	if !ok || status != 0 {
		t.Fatalf("ok=%v status=%d, want ok=true status=0", ok, status)
	}
}

// TestAuthorizeObjectGet_ProtectedOwnerUnauthenticatedIs401 proves an
// identity/leaseapp-owned object with no token presented is a 401 — matching
// the established convention (handleApplications, handleLandlordApplications,
// handleLeaseDocumentGet, resolveAllowedObjectOwners): 401 means "no/invalid
// credentials," nothing to hide since every protected object requires SOME
// token; 404 is reserved for authenticated-but-not-entitled below.
func TestAuthorizeObjectGet_ProtectedOwnerUnauthenticatedIs401(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
	ok, status, _ := s.authorizeObjectGet(context.Background(), r, []string{"vtx.identity.alice"})
	if ok || status != http.StatusUnauthorized {
		t.Fatalf("ok=%v status=%d, want ok=false status=401", ok, status)
	}
}

// TestAuthorizeObjectGet_NoOwnersIsNeverPublic proves the degenerate
// zero-link object (no owners at all) is never treated as public — it always
// requires auth, and with no authenticator configured that's a 401 (same as
// any other protected object with no token presented).
func TestAuthorizeObjectGet_NoOwnersIsNeverPublic(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
	ok, status, _ := s.authorizeObjectGet(context.Background(), r, nil)
	if ok || status != http.StatusUnauthorized {
		t.Fatalf("ok=%v status=%d, want ok=false status=401", ok, status)
	}
}

// TestAuthorizeObjectGet_MixedOwners_UnitDoesNotLeakProtectedSibling is the
// Blind Hunter regression: a single object can carry MORE THAN ONE owner link
// (e.g. content-addressed bytes independently attached to a public unit AND a
// protected identity/leaseapp). A unit owner must NEVER shortcut access to the
// whole object — the object is public only when it has ZERO protected owners;
// with a protected owner present, an unauthenticated caller must still be
// refused (401), not served the bytes via the unit link.
func TestAuthorizeObjectGet_MixedOwners_UnitDoesNotLeakProtectedSibling(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
	ok, status, _ := s.authorizeObjectGet(context.Background(), r, []string{"vtx.unit.u1", "vtx.identity.victim"})
	if ok || status != http.StatusUnauthorized {
		t.Fatalf("ok=%v status=%d, want ok=false status=401 — a unit owner must not leak a co-owned protected owner", ok, status)
	}
}

// TestAuthorizeObjectGet_AllPublicOwnersNeedsNoAuth proves an object whose
// owners are ALL public (no protected owner at all) still bypasses auth
// entirely — the fix above narrows the shortcut to "zero protected owners,"
// not "zero unit owners," so a multi-unit-owned photo must still work
// unauthenticated.
func TestAuthorizeObjectGet_AllPublicOwnersNeedsNoAuth(t *testing.T) {
	s := &server{logger: discardLogger()}
	r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
	ok, status, _ := s.authorizeObjectGet(context.Background(), r, []string{"vtx.unit.u1", "vtx.unit.u2"})
	if !ok || status != 0 {
		t.Fatalf("ok=%v status=%d, want ok=true status=0", ok, status)
	}
}

// TestVtxType_RejectsEmptySegments proves a malformed key with a blank type
// or id segment (e.g. "vtx.unit." with no id) never resolves to a real type
// — closing the gap where such a key would otherwise be classified public.
func TestVtxType_RejectsEmptySegments(t *testing.T) {
	cases := []string{"vtx.unit.", "vtx..u1", "vtx.."}
	for _, key := range cases {
		if got := vtxType(key); got != "" {
			t.Errorf("vtxType(%q) = %q, want \"\" (malformed segment)", key, got)
		}
		if isPublicObjectOwner(key) {
			t.Errorf("isPublicObjectOwner(%q) = true, want false (malformed key must never be public)", key)
		}
	}
}
