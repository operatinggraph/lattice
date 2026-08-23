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
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1", nil); err != nil {
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
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1", nil); err != nil {
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
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req1", nil); err != nil {
		t.Fatalf("decryptSensitiveDoc: %v", err)
	}
	if _, ok := tracker.plaintextKeys[doc.Key]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded — neither cyclic vertex's class ever resolves to a registered DDL", tracker.plaintextKeys, doc.Key)
	}
}

// TestFire1Payoff_EightSiblingReadsShareTemplateNode_MemoCollapsesRoundTrips
// is Fire 1's live e2e payoff proof, against REAL embedded Core KV — no fake
// reader anywhere in this test. It reproduces the design's own §4 shape: an
// "instance → template → type" 2-hop chain (the deepest real domain shape,
// per maxInstanceOfHops' doc comment), where EIGHT sibling documents each
// have their own distinct root but instanceOf the SAME template vertex,
// which itself instanceOfs the registered sensitive type DDL — exactly
// "eight transaction .entry aspects" sharing the type vertex at hop 2.
//
// It measures real NATS requests issued (conn.NATS().Stats().OutMsgs), not
// wall-clock: on the fast embedded-NATS fixture this test runs against,
// round-trip latency is sub-millisecond regardless of Fire 1, so a
// wall-clock assertion here would pass whether or not the collapse actually
// fired (confirmed by hand: disabling the memo left elapsed time
// unchanged). Request COUNT is what the design's measured 9-14× reduction
// (§4) is actually about, and it is what a mutation of the memo consult
// changes even on a fast fixture. Asserts the WITH-memo run issues strictly
// fewer requests than an otherwise-identical WITHOUT-memo run.
//
// This is Fire 1's OWN closing proof — that the platform mechanism no
// longer costs one live read per sibling for the shared node. Verifying it
// end-to-end against the actual four vertical ledgers' self-credit
// operations is the Verticals stream's own follow-on once their self-scope
// grants land (packages/*-ledger, tracked in verticals.md) — building or
// merging those belongs to that follow-on, not to this platform mechanism's
// proof.
func TestFire1Payoff_EightSiblingReadsShareTemplateNode_MemoCollapsesRoundTrips(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// The type DDL (hop 2's terminal): a vertexType DDL, sensitive.
	typeMetaID := mustNanoID(t)
	typeDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"sensitiveTxnType","permittedCommands":["Noop"],"sensitive":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+typeMetaID, typeDoc); err != nil {
		t.Fatalf("seed type DDL: %v", err)
	}
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	const siblingCount = 8
	// run seeds a fresh template (shared by all siblings) + 8 fresh sibling
	// roots, each with its own instanceOf link to that template, and returns
	// the docs to decrypt. Fresh IDs per run so the two runs (memo / no
	// memo) never share Core KV state.
	run := func(memo *ddlResolutionMemo) []*VertexDoc {
		templateID := mustNanoID(t)
		if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.widget."+templateID,
			[]byte(`{"class":"txnTemplate","isDeleted":false,"data":{}}`)); err != nil {
			t.Fatalf("seed template vertex: %v", err)
		}
		seedCommittedLink(t, ctx, conn,
			fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", templateID, typeMetaID), false)

		docs := make([]*VertexDoc, siblingCount)
		for i := 0; i < siblingCount; i++ {
			rootID := mustNanoID(t)
			seedCommittedLink(t, ctx, conn,
				fmt.Sprintf("lnk.identity.%s.instanceOf.widget.%s", rootID, templateID), false)
			docs[i] = &VertexDoc{
				Key:   "vtx.identity." + rootID + ".entry",
				Class: "txnEntry.v3", // unregistered — forces the 2-hop walk
				Data:  map[string]interface{}{"amount": i + 1},
			}
		}
		tracker := &sensitiveReadTracker{}
		for _, doc := range docs {
			if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, nil, doc, false, tracker, "req-payoff", memo); err != nil {
				t.Fatalf("decryptSensitiveDoc(%s): %v", doc.Key, err)
			}
			if _, ok := tracker.plaintextKeys[doc.Key]; !ok {
				t.Fatalf("plaintextKeys missing %q — chain-resolved sensitivity must still be tracked", doc.Key)
			}
		}
		return docs
	}

	before := conn.NATS().Stats().OutMsgs
	run(nil) // no memo — every sibling's hop-2 (the shared template) re-reads Core KV
	withoutMemo := conn.NATS().Stats().OutMsgs - before

	before = conn.NATS().Stats().OutMsgs
	run(&ddlResolutionMemo{}) // fresh memo per execution, exactly as ScriptContext provides
	withMemo := conn.NATS().Stats().OutMsgs - before

	if withMemo >= withoutMemo {
		t.Fatalf("requests with memo = %d, without = %d — want strictly fewer with the memo warm (7 of 8 walks reaching the shared template node should skip Core KV entirely)", withMemo, withoutMemo)
	}
	t.Logf("NATS requests: without memo = %d, with memo = %d (saved %d)", withoutMemo, withMemo, withoutMemo-withMemo)
}
