// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for
// AttachObject and DetachObject — each envelope below declares NO ContextHint
// at all, proving the script's own derive_reads(op) is what hydrates the
// target root (AttachObject) or the link key + object vertex (DetachObject)
// rather than a caller's own declaration. Neither op runs a confinement walk,
// so no Enumerations declaration is needed either.
//
// The package's own submitObj helper (object_lifecycle_test.go) always builds
// its ContextHint from a `reads []string` PARAMETER — even a caller passing
// nil there is invisible to a static literal check, since the composite
// itself reads a variable, not the literal nil. These two vectors build the
// envelope directly so the bare shape is visible at the literal.
package objectsbase_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestAttachObject_UndeclaredSubmitter_AttachesObject: a bare AttachObject
// mints the object and its link. derive_reads' own optionalReads carries the
// target root, so alive(state, target_key) sees the live target rather than
// misreading an undeclared root as absent.
func TestAttachObject_UndeclaredSubmitter_AttachesObject(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objattachnodecl", Instance: "objattachnodecl-1"})

	id := "vtx.identity.AAuserNDECLXHJKMNPQR"
	seedIdentity(t, ctx, conn, id, false)
	digest := "SHA-256=nodeclTESTdigestExampleA"
	oid := substrate.SHA256NanoID("object:" + digest)
	objKey := "vtx.object." + oid
	link := "lnk.object." + oid + ".photoOf.identity.AAuserNDECLXHJKMNPQR"

	payload, _ := json.Marshal(map[string]any{
		"digest": digest, "size": 10, "contentType": "image/png",
		"storeName": "s-nodecl", "targetKey": id, "linkName": "photoOf",
	})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("objattnodeclsubmit1"),
		Lane:          processor.LaneDefault,
		OperationType: "AttachObject",
		Actor:         objStaffActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "object",
		Payload:       payload,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if !liveExists(ctx, conn, link) {
		t.Fatalf("attach did not create %s — the derivation must hydrate the target root for the attach to run at all", link)
	}
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("attach did not create %s", objKey)
	}
}

// TestDetachObject_UndeclaredSubmitter_DetachesObject: a bare DetachObject
// still tombstones the link and drops the object's liveLinks count.
// derive_reads already computes the link key (fail-closed reads) and the
// object vertex (absence-tolerant optionalReads) from oid/targetKey/linkName,
// so this vector needs no script fix — only the literal-envelope shape the
// package's own parameterized submitObj helper cannot express statically.
func TestDetachObject_UndeclaredSubmitter_DetachesObject(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objdetachnodecl", Instance: "objdetachnodecl-1"})

	id := "vtx.identity.AAuserNDECLYHJKMNPQS"
	seedIdentity(t, ctx, conn, id, false)
	digest := "SHA-256=nodecl2TESTdigestExample"
	oid := substrate.SHA256NanoID("object:" + digest)
	objKey := "vtx.object." + oid
	link := "lnk.object." + oid + ".photoOf.identity.AAuserNDECLYHJKMNPQS"

	attachPayload, _ := json.Marshal(map[string]any{
		"digest": digest, "size": 10, "contentType": "image/png",
		"storeName": "s-nodecl2", "targetKey": id, "linkName": "photoOf",
	})
	attachEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("objdetnodeclattach1"),
		Lane:          processor.LaneDefault,
		OperationType: "AttachObject",
		Actor:         objStaffActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "object",
		Payload:       attachPayload,
		ContextHint:   &processor.ContextHint{Reads: []string{id}},
	}
	testutil.PublishOp(t, conn, attachEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if !liveExists(ctx, conn, link) {
		t.Fatalf("setup attach did not create %s", link)
	}

	detachPayload, _ := json.Marshal(map[string]any{"oid": oid, "targetKey": id, "linkName": "photoOf"})
	detachEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("objdetnodeclsubmit1"),
		Lane:          processor.LaneDefault,
		OperationType: "DetachObject",
		Actor:         objStaffActorKey,
		SubmittedAt:   "2026-07-01T12:01:00Z",
		Class:         "object",
		Payload:       detachPayload,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, detachEnv)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if !isDeleted(t, ctx, conn, link) {
		t.Fatalf("link %s should be tombstoned after a bare detach", link)
	}
	if ll := liveLinksOf(t, ctx, conn, objKey); ll != 0 {
		t.Fatalf("liveLinks after bare detach = %d want 0", ll)
	}
}
