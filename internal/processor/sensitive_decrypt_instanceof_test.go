package processor

import (
	"fmt"
	"testing"
)

// TestDecryptSensitiveDoc_InstanceOfChainedSensitiveClass_MarksPlaintext: a
// document's OWN class ("ssn.v2") is not directly registered, but its
// identity's LIVE committed instanceOf chain resolves — via the same
// ddlResolver step 6 and step 6.5 share — to a vertex classed "ssn", a
// registered Sensitive DDL. Before wiring decrypt-on-read through the shared
// resolver, decryptSensitiveDoc's raw DDLs.Lookup("ssn.v2") missed and never
// recorded the key as readable plaintext, leaving step 6's external-egress
// guard blind to it. v is nil (no Vault wired) so this exercises only the
// resolution + tracker recording, not the decrypt itself.
func TestDecryptSensitiveDoc_InstanceOfChainedSensitiveClass_MarksPlaintext(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	identityID := testNanoID2
	targetID := tplID
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.identity.%s.instanceOf.widget.%s", identityID, targetID), false)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.widget."+targetID,
		[]byte(`{"class":"ssn","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed target vertex: %v", err)
	}

	doc := &VertexDoc{
		Key:   "vtx.identity." + identityID + ".ssn2",
		Class: "ssn.v2",
		Data:  map[string]interface{}{"value": "123-45-6789"},
	}
	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1"); err != nil {
		t.Fatalf("decryptSensitiveDoc: %v", err)
	}
	if _, ok := tracker.plaintextKeys[doc.Key]; !ok {
		t.Fatalf("plaintextKeys = %+v, want %q recorded — chain-resolved sensitivity must be tracked", tracker.plaintextKeys, doc.Key)
	}
}

// TestDecryptSensitiveDoc_UnresolvableClass_NeverMarksPlaintext: a document
// whose class resolves neither by exact lookup nor via any instanceOf chain
// (no live link at all) is left untouched — the permissive default, not a
// false positive.
func TestDecryptSensitiveDoc_UnresolvableClass_NeverMarksPlaintext(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	doc := &VertexDoc{
		Key:   "vtx.identity." + testNanoID2 + ".unregistered",
		Class: "totally.unregistered",
		Data:  map[string]interface{}{"value": "plain"},
	}
	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1"); err != nil {
		t.Fatalf("decryptSensitiveDoc: %v", err)
	}
	if _, ok := tracker.plaintextKeys[doc.Key]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded — an unresolvable class must stay permissive, not sensitive", tracker.plaintextKeys, doc.Key)
	}
}

// TestDecryptSensitiveDoc_MutualInstanceOfCycle_DoesNotRecurse: a chain
// terminal (vtx.widget.A) whose OWN class is unregistered and which itself
// instanceOfs back to a second vertex (vtx.widget.B) that instanceOfs back to
// A — a 2-cycle neither vertex's exact class resolves. classOf's live layer
// must read each vertex's class via a plain, non-decrypting read (§ doc
// comment on ddlResolver.classReader): if it instead recursed back into
// decryptSensitiveDoc/resolveGoverningDDL for the chain terminal, this cycle
// would recurse without bound and crash the process with a stack overflow —
// a fatal, uncatchable runtime error, so this test's very ability to run to
// completion (not hang or crash the binary) is the assertion.
func TestDecryptSensitiveDoc_MutualInstanceOfCycle_DoesNotRecurse(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	identityID := testNanoID2
	idA, idB := tplID, instID
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.identity.%s.instanceOf.widget.%s", identityID, idA), false)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", idA, idB), false)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", idB, idA), false)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.widget."+idA,
		[]byte(`{"class":"cyclic.a","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed vertex A: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.widget."+idB,
		[]byte(`{"class":"cyclic.b","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed vertex B: %v", err)
	}

	doc := &VertexDoc{
		Key:   "vtx.identity." + identityID + ".unresolvable2",
		Class: "unresolvable.chain",
		Data:  map[string]interface{}{"value": "plain"},
	}
	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1"); err != nil {
		t.Fatalf("decryptSensitiveDoc: %v", err)
	}
	if _, ok := tracker.plaintextKeys[doc.Key]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded — neither cyclic vertex's class ever resolves to a registered DDL", tracker.plaintextKeys, doc.Key)
	}
}
