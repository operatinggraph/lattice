// Object lifecycle integration tests — exercise the public Lattice surface a
// real Capability Package sees: seed the kernel, install rbac + identity +
// hygiene + objects-base through the Processor, then submit the object
// lifecycle ops and assert the committed Core-KV shape. They prove the object
// vertex is content-addressed + deduped, the .content aspect carries only
// reference metadata (the bytes never enter Core KV), a tombstoned object
// revives on re-attach (CC2), and the OCC-touch tracks the link set (§19).
package objectsbase_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	objectsbase "github.com/asolgan/lattice/packages/objects-base"
)

const (
	objStaffActorID  = "BBobjstaffHJKMNPQRST"
	objStaffActorKey = "vtx.identity." + objStaffActorID
	objStaffCapKey   = "cap.identity." + objStaffActorID

	testDigest = "SHA-256=GLnInPVtESTexampledigestAA"
)

func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    objStaffCapKey,
		Actor:                  objStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{objStaffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "AttachObject", Scope: "any"},
			{OperationType: "DetachObject", Scope: "any"},
			{OperationType: "TombstoneObject", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupObjectsEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, objectsbase.Package); err != nil {
		stop()
		t.Fatalf("install objects-base: %v", err)
	}
	stop()
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	return ctx, conn
}

func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, deleted bool) {
	t.Helper()
	doc := map[string]any{"class": "identity", "isDeleted": deleted, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed identity %s: %v", key, err)
	}
}

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) (map[string]any, uint64) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc, entry.Revision
}

func isDeleted(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	doc, _ := readDoc(t, ctx, conn, key)
	d, _ := doc["isDeleted"].(bool)
	return d
}

// liveLinksOf reads the object vertex's data.liveLinks scalar (the authoritative
// live-link count the objectLiveness lens decides orphan-ness on).
func liveLinksOf(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) int {
	t.Helper()
	doc, _ := readDoc(t, ctx, conn, key)
	d, _ := doc["data"].(map[string]any)
	v, _ := d["liveLinks"].(float64)
	return int(v)
}

func liveExists(ctx context.Context, conn *substrate.Conn, key string) bool {
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	d, _ := doc["isDeleted"].(bool)
	return !d
}

// submitObj publishes one op (with an explicit requestId) and drives one commit
// cycle, asserting the outcome. Used by the dedup / idempotency tests that need
// to control the requestId.
func submitObj(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, requestID, opType string, payload map[string]any, reads []string, want processor.MessageOutcome) {
	t.Helper()
	pb, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         objStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "object",
		Payload:       pb,
		ContextHint:   &processor.ContextHint{Reads: reads},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestObject_DuplicateRequestCollapses proves the idempotency contract: a
// re-submitted AttachObject with the SAME requestId collapses on the Contract #4
// tracker (OutcomeDuplicate) and does NOT re-commit — exactly-once even though
// the deterministic requestId makes a genuine retry recompute the same id.
func TestObject_DuplicateRequestCollapses(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objdup", Instance: "objdup-1"})

	id := "vtx.identity.AAuserHJKMNPQRSTUVW3"
	seedIdentity(t, ctx, conn, id, false)
	digest := "SHA-256=dupTESTdigestExampleAA"
	oid := substrate.SHA256NanoID("object:" + digest)
	objKey := "vtx.object." + oid
	link := "lnk.object." + oid + ".photoOf.identity.AAuserHJKMNPQRSTUVW3"
	payload := map[string]any{"digest": digest, "size": 10, "contentType": "image/png",
		"storeName": "s-dup", "targetKey": id, "linkName": "photoOf"}

	reqID := testutil.GenReqID("dupreq")
	submitObj(t, ctx, conn, cp, cons, reqID, "AttachObject", payload, []string{id}, processor.OutcomeAccepted)
	if !liveExists(ctx, conn, link) {
		t.Fatalf("first attach did not create %s", link)
	}
	_, rev1 := readDoc(t, ctx, conn, objKey)

	// Same requestId again → duplicate (short-circuit at step 2), no re-commit.
	submitObj(t, ctx, conn, cp, cons, reqID, "AttachObject", payload, []string{id}, processor.OutcomeDuplicate)
	_, rev2 := readDoc(t, ctx, conn, objKey)
	if rev2 != rev1 {
		t.Fatalf("duplicate must not re-commit (object rev %d -> %d)", rev1, rev2)
	}
}

// TestObject_LiveObjectRequiresContentInReads proves the m1 guard: digest
// collision detection is script-enforced, so an attach to a live object that
// omits the .content aspect from contextHint.reads is rejected (rather than
// silently skipping the collision check).
func TestObject_LiveObjectRequiresContentInReads(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objguard", Instance: "objguard-1"})

	id := "vtx.identity.AAuserHJKMNPQRSTUVW4"
	id2 := "vtx.identity.AAuserHJKMNPQRSTUVW5"
	seedIdentity(t, ctx, conn, id, false)
	seedIdentity(t, ctx, conn, id2, false)
	digest := "SHA-256=guardTESTdigestExampleA"
	oid := substrate.SHA256NanoID("object:" + digest)
	objKey := "vtx.object." + oid

	p1 := map[string]any{"digest": digest, "size": 5, "contentType": "image/png",
		"storeName": "s-g", "targetKey": id, "linkName": "photoOf"}
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("guard1"), "AttachObject", p1, []string{id}, processor.OutcomeAccepted)

	// Attach the same bytes to a different owner with the object now live, but
	// OMIT the .content aspect from reads → must reject (InvalidArgument).
	p2 := map[string]any{"digest": digest, "size": 5, "contentType": "image/png",
		"storeName": "s-g2", "targetKey": id2, "linkName": "photoOf"}
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("guard2"), "AttachObject", p2, []string{id2, objKey}, processor.OutcomeRejected)
}

// TestObject_TombstoneEpochCAS_AbortsOnRelink is the #1 build-blocking GC
// invariant (§20): a re-link landing between orphan-detection and the tombstone
// commit must abort the reclaim, so the byte-janitor never deletes bytes a live
// link references. The lens projects the object's linkEpoch at orphan-detection;
// a re-link bumps it; TombstoneObject CASes the projected epoch against the
// current one and fails Stale on a mismatch.
func TestObject_TombstoneEpochCAS_AbortsOnRelink(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objcas", Instance: "objcas-1"})

	id1 := "vtx.identity.AAuserHJKMNPQRSTUVW6"
	id2 := "vtx.identity.AAuserHJKMNPQRSTUVW7"
	seedIdentity(t, ctx, conn, id1, false)
	seedIdentity(t, ctx, conn, id2, false)
	digest := "SHA-256=casTESTdigestExampleAAA"
	oid := substrate.SHA256NanoID("object:" + digest)
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"

	attach := func(label, target string, reads []string) {
		submitObj(t, ctx, conn, cp, cons, testutil.GenReqID(label), "AttachObject",
			map[string]any{"digest": digest, "size": 7, "contentType": "image/png",
				"storeName": "s-cas", "targetKey": target, "linkName": "photoOf"},
			reads, processor.OutcomeAccepted)
	}
	detach := func(label, target string, reads []string) {
		submitObj(t, ctx, conn, cp, cons, testutil.GenReqID(label), "DetachObject",
			map[string]any{"oid": oid, "targetKey": target, "linkName": "photoOf"},
			reads, processor.OutcomeAccepted)
	}
	link1 := "lnk.object." + oid + ".photoOf.identity.AAuserHJKMNPQRSTUVW6"
	epoch := func() int {
		doc, _ := readDoc(t, ctx, conn, objKey)
		d, _ := doc["data"].(map[string]any)
		v, _ := d["linkEpoch"].(float64)
		return int(v)
	}

	attach("cas1", id1, []string{id1}) // mint → epoch 1, liveLinks 1
	if epoch() != 1 {
		t.Fatalf("epoch = %d want 1", epoch())
	}
	// Orphan it (liveLinks → 0) so the lens would project it for reclaim at epoch 2.
	detach("casD1", id1, []string{link1, objKey})
	if epoch() != 2 || liveLinksOf(t, ctx, conn, objKey) != 0 {
		t.Fatalf("after detach: epoch=%d liveLinks=%d want 2/0", epoch(), liveLinksOf(t, ctx, conn, objKey))
	}
	// A re-link landing AFTER orphan-detection: re-attach revives the link, bumping
	// the epoch beyond what the lens saw (and lifting liveLinks back to 1).
	attach("casReattach", id1, []string{id1, objKey, contentKey, link1})
	if epoch() != 3 || liveLinksOf(t, ctx, conn, objKey) != 1 {
		t.Fatalf("after re-attach: epoch=%d liveLinks=%d want 3/1", epoch(), liveLinksOf(t, ctx, conn, objKey))
	}

	// TombstoneObject with the STALE orphan-detection epoch (2) → aborts Stale: the
	// re-link bumped the epoch past it, so the reclaim must not reap the live object.
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("tombStale"), "TombstoneObject",
		map[string]any{"oid": oid, "expectedEpoch": 2}, []string{objKey, contentKey}, processor.OutcomeRejected)
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("a stale-epoch tombstone must NOT reap the re-linked object")
	}

	// Re-orphan (liveLinks → 0, epoch → 4), then the matching-epoch tombstone of the
	// genuine orphan proceeds — the liveLinks>0 backstop is satisfied.
	detach("casD2", id1, []string{link1, objKey})
	if epoch() != 4 || liveLinksOf(t, ctx, conn, objKey) != 0 {
		t.Fatalf("after re-detach: epoch=%d liveLinks=%d want 4/0", epoch(), liveLinksOf(t, ctx, conn, objKey))
	}
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("tombOK"), "TombstoneObject",
		map[string]any{"oid": oid, "expectedEpoch": 4}, []string{objKey, contentKey}, processor.OutcomeAccepted)
	if !isDeleted(t, ctx, conn, objKey) {
		t.Fatalf("a current-epoch tombstone of an orphan (liveLinks 0) should soft-delete the object")
	}
}

// TestObject_ReplaceLeg_DecrementsOldObject pins the replaceObjectId leg's
// liveLinks accounting — the only counter-mutation path the other tests don't
// exercise. A "new photo" attach that replaces a prior object in the same
// (target, linkName) slot must tombstone the OLD object's link AND decrement its
// liveLinks, reaping it (liveLinks 0) iff that was its last link, while the new
// object lands live (liveLinks 1).
func TestObject_ReplaceLeg_DecrementsOldObject(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objrep", Instance: "objrep-1"})

	id1 := "vtx.identity.AAuserHJKMNPQRSTUVR1"
	seedIdentity(t, ctx, conn, id1, false)

	digestA := "SHA-256=replaceLegOldObjectAAAA"
	digestB := "SHA-256=replaceLegNewObjectBBBB"
	oidA := substrate.SHA256NanoID("object:" + digestA)
	oidB := substrate.SHA256NanoID("object:" + digestB)
	objA := "vtx.object." + oidA
	objB := "vtx.object." + oidB
	linkA := "lnk.object." + oidA + ".photoOf.identity.AAuserHJKMNPQRSTUVR1"
	linkB := "lnk.object." + oidB + ".photoOf.identity.AAuserHJKMNPQRSTUVR1"

	submit := func(label string, payload map[string]any, reads []string, want processor.MessageOutcome) {
		t.Helper()
		pb, _ := json.Marshal(payload)
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "AttachObject",
			Actor:         objStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "object",
			Payload:       pb,
			ContextHint:   &processor.ContextHint{Reads: reads},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, want)
	}

	// Attach object A (the old photo) → liveLinks 1.
	submit("attachA", map[string]any{
		"digest": digestA, "size": 10, "contentType": "image/jpeg",
		"storeName": "store-A", "targetKey": id1, "linkName": "photoOf"},
		[]string{id1}, processor.OutcomeAccepted)
	if ll := liveLinksOf(t, ctx, conn, objA); ll != 1 {
		t.Fatalf("object A liveLinks after attach = %d want 1", ll)
	}

	// Attach object B to the SAME (target, linkName) slot with replaceObjectId=A.
	// B lands live (liveLinks 1); A's link is tombstoned and A's liveLinks → 0.
	submit("attachBreplaceA", map[string]any{
		"digest": digestB, "size": 12, "contentType": "image/jpeg",
		"storeName": "store-B", "targetKey": id1, "linkName": "photoOf",
		"replaceObjectId": oidA},
		[]string{id1, objA, linkA}, processor.OutcomeAccepted)

	if !liveExists(ctx, conn, linkB) {
		t.Fatalf("new object B's link %s must be live", linkB)
	}
	if !isDeleted(t, ctx, conn, linkA) {
		t.Fatalf("old object A's link %s must be tombstoned by the replace leg", linkA)
	}
	if ll := liveLinksOf(t, ctx, conn, objB); ll != 1 {
		t.Fatalf("new object B liveLinks = %d want 1", ll)
	}
	if ll := liveLinksOf(t, ctx, conn, objA); ll != 0 {
		t.Fatalf("old object A liveLinks after replace = %d want 0 (now a reclaim candidate)", ll)
	}
}

func TestObject_Lifecycle(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "obj", Instance: "obj-1"})

	id1 := "vtx.identity.AAuserHJKMNPQRSTUVW1"
	id2 := "vtx.identity.AAuserHJKMNPQRSTUVW2"
	seedIdentity(t, ctx, conn, id1, false)
	seedIdentity(t, ctx, conn, id2, false)

	oid := substrate.SHA256NanoID("object:" + testDigest)
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"
	link1 := "lnk.object." + oid + ".photoOf.identity.AAuserHJKMNPQRSTUVW1"
	link2 := "lnk.object." + oid + ".photoOf.identity.AAuserHJKMNPQRSTUVW2"

	submit := func(label, opType string, payload map[string]any, reads []string, want processor.MessageOutcome) {
		t.Helper()
		pb, _ := json.Marshal(payload)
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: opType,
			Actor:         objStaffActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "object",
			Payload:       pb,
			ContextHint:   &processor.ContextHint{Reads: reads},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, want)
	}

	attachPayload := func(target string) map[string]any {
		return map[string]any{
			"digest": testDigest, "size": 184213, "contentType": "image/jpeg",
			"storeName": "store-nanoid-1", "targetKey": target, "linkName": "photoOf",
			"filename": "me.jpg",
		}
	}

	// 1. AttachObject (absent) → mints object vertex + .content + link.
	submit("attach1", "AttachObject", attachPayload(id1), []string{id1}, processor.OutcomeAccepted)
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("object vertex %s not created", objKey)
	}
	if !liveExists(ctx, conn, link1) {
		t.Fatalf("link %s not created", link1)
	}
	objDoc, objRev1 := readDoc(t, ctx, conn, objKey)
	if cls, _ := objDoc["class"].(string); cls != "object" {
		t.Fatalf("object class = %q want object", cls)
	}
	// Root data carries exactly the two GC scalars (the documented exceptions to
	// D5's root-minimal rule): linkEpoch (the re-link CAS version, starts at 1)
	// and liveLinks (the authoritative live-link count, =1 after the first attach).
	if data, _ := objDoc["data"].(map[string]any); len(data) != 2 || data["linkEpoch"] == nil || data["liveLinks"] == nil {
		t.Fatalf("object root data must be {linkEpoch, liveLinks}, got %v", data)
	}
	if ll := liveLinksOf(t, ctx, conn, objKey); ll != 1 {
		t.Fatalf("liveLinks after first attach = %d want 1", ll)
	}

	// The .content aspect carries ONLY reference metadata — the bytes never
	// enter Core KV (the off-graph-blob invariant).
	contentDoc, _ := readDoc(t, ctx, conn, contentKey)
	cdata, _ := contentDoc["data"].(map[string]any)
	if cdata["digest"] != testDigest || cdata["storeName"] != "store-nanoid-1" || cdata["contentType"] != "image/jpeg" {
		t.Fatalf(".content metadata wrong: %v", cdata)
	}
	allowed := map[string]bool{"digest": true, "size": true, "contentType": true, "storeName": true}
	for k := range cdata {
		if !allowed[k] {
			t.Fatalf(".content carries an unexpected field %q (the bytes must never enter Core KV): %v", k, cdata)
		}
	}
	// filename is attachment-specific → on the LINK, not the shared object.
	linkDoc, _ := readDoc(t, ctx, conn, link1)
	if ldata, _ := linkDoc["data"].(map[string]any); ldata["filename"] != "me.jpg" {
		t.Fatalf("link must carry filename me.jpg, got %v", ldata)
	}

	// 2. AttachObject of identical bytes to a different owner → dedup: same
	//    object vertex (touched, revision bumped), only a new link.
	submit("attach2", "AttachObject", attachPayload(id2), []string{id2, objKey, contentKey}, processor.OutcomeAccepted)
	if !liveExists(ctx, conn, link2) {
		t.Fatalf("dedup link %s not created", link2)
	}
	_, objRev2 := readDoc(t, ctx, conn, objKey)
	if objRev2 <= objRev1 {
		t.Fatalf("dedup must OCC-touch the object vertex (rev %d -> %d)", objRev1, objRev2)
	}
	if ll := liveLinksOf(t, ctx, conn, objKey); ll != 2 {
		t.Fatalf("liveLinks after a dedup re-link to a 2nd owner = %d want 2", ll)
	}

	// 3. Reject: attach to a meta/system target (CC7 protected-target guard).
	submit("attachMeta", "AttachObject",
		map[string]any{"digest": testDigest, "size": 1, "contentType": "image/jpeg",
			"storeName": "s", "targetKey": bootstrap.MetaRootKey, "linkName": "photoOf"},
		[]string{bootstrap.MetaRootKey}, processor.OutcomeRejected)

	// 4. Reject: attach to an absent target (UnknownTarget). The absent key is
	//    NOT declared in reads (a declared-but-absent key is a hydration miss).
	submit("attachGhost", "AttachObject",
		map[string]any{"digest": testDigest, "size": 1, "contentType": "image/jpeg",
			"storeName": "s", "targetKey": "vtx.identity.ZZghostHJKMNPQRSTUV", "linkName": "photoOf"},
		[]string{}, processor.OutcomeRejected)

	// 5. DetachObject from owner 1 → link tombstoned, object still alive (touched).
	submit("detach1", "DetachObject",
		map[string]any{"oid": oid, "targetKey": id1, "linkName": "photoOf"},
		[]string{link1, objKey}, processor.OutcomeAccepted)
	if !isDeleted(t, ctx, conn, link1) {
		t.Fatalf("link %s should be tombstoned after detach", link1)
	}
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("object must stay alive while owner 2 still links it")
	}
	if ll := liveLinksOf(t, ctx, conn, objKey); ll != 1 {
		t.Fatalf("liveLinks after detaching one of two owners = %d want 1", ll)
	}

	// 5b. The liveLinks>0 backstop refuses to reap a still-linked object: owner 2
	//     still links it (liveLinks 1), so a TombstoneObject is rejected.
	submit("tombLive", "TombstoneObject",
		map[string]any{"oid": oid}, []string{objKey, contentKey}, processor.OutcomeRejected)
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("the liveLinks>0 backstop must refuse to reap a still-linked object")
	}

	// 5c. Detach owner 2 → liveLinks 0, now a genuine orphan.
	submit("detach2", "DetachObject",
		map[string]any{"oid": oid, "targetKey": id2, "linkName": "photoOf"},
		[]string{link2, objKey}, processor.OutcomeAccepted)
	if ll := liveLinksOf(t, ctx, conn, objKey); ll != 0 {
		t.Fatalf("liveLinks after detaching the last owner = %d want 0", ll)
	}

	// 6. TombstoneObject on the orphan → object vertex + .content soft-deleted, the
	//    object.tombstoned event (the v1b byte-reclaim trigger) is emitted.
	submit("tombstone", "TombstoneObject",
		map[string]any{"oid": oid}, []string{objKey, contentKey}, processor.OutcomeAccepted)
	if !isDeleted(t, ctx, conn, objKey) {
		t.Fatalf("object vertex should be tombstoned")
	}
	if !isDeleted(t, ctx, conn, contentKey) {
		t.Fatalf(".content aspect should be tombstoned")
	}

	// 7. Re-attach the SAME bytes after tombstone → revive (CC2): the object
	//    vertex + .content come back alive with the fresh upload; no data loss.
	revive := attachPayload(id1)
	revive["storeName"] = "store-nanoid-2-fresh"
	submit("revive", "AttachObject", revive,
		[]string{id1, objKey, contentKey, link1}, processor.OutcomeAccepted)
	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("object vertex must be revived")
	}
	if !liveExists(ctx, conn, contentKey) {
		t.Fatalf(".content must be revived")
	}
	revContent, _ := readDoc(t, ctx, conn, contentKey)
	if rdata, _ := revContent["data"].(map[string]any); rdata["storeName"] != "store-nanoid-2-fresh" {
		t.Fatalf("revive must re-point .content to the fresh upload, got %v", rdata)
	}
	if !liveExists(ctx, conn, link1) {
		t.Fatalf("re-attach must restore the link %s", link1)
	}
}

// sensitiveAttachPayload builds an AttachObject payload for a sensitive object
// whose bytes are already client-side ciphertext — the DDL never sees key
// material, only the caller-supplied envelope (Contract #3 §3.11).
func sensitiveAttachPayload(digest, target, keyID string) map[string]any {
	return map[string]any{
		"digest": digest, "size": 4096, "contentType": "application/pdf",
		"storeName": "sensitive-store-1", "targetKey": target, "linkName": "signedLeaseOf",
		"sensitive": true, "governingIdentity": keyID,
		"encryption": map[string]any{
			"algo": "AES-256-GCM", "nonce": "test-nonce-b64", "wrappedCEK": "test-wrapped-cek-b64",
			"keyId": keyID,
		},
	}
}

// TestObject_SensitiveAttach_IdentitySaltedOid proves §3.3/§3.11's identity
// salting: a sensitive object's oid folds in governingIdentity (== keyId), and
// the .content aspect carries sensitive:true + the caller's envelope verbatim
// alongside the ordinary reference metadata — recorded, not decrypted or
// re-derived (the Processor/DDL never touches key material, §4.3).
func TestObject_SensitiveAttach_IdentitySaltedOid(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsens", Instance: "objsens-1"})

	applicant := "vtx.identity." + testutil.GenReqID("sensapplicant1")
	seedIdentity(t, ctx, conn, applicant, false)

	digest := "SHA-256=sensitiveTESTdigestExampleA"
	oid := substrate.SHA256NanoID("object:" + applicant + ":" + digest)
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"

	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensattach1"), "AttachObject",
		sensitiveAttachPayload(digest, applicant, applicant), []string{applicant}, processor.OutcomeAccepted)

	if !liveExists(ctx, conn, objKey) {
		t.Fatalf("identity-salted object vertex %s not created", objKey)
	}
	contentDoc, _ := readDoc(t, ctx, conn, contentKey)
	cdata, _ := contentDoc["data"].(map[string]any)
	if sensitive, _ := cdata["sensitive"].(bool); !sensitive {
		t.Fatalf(".content.sensitive must be true, got %v", cdata["sensitive"])
	}
	enc, _ := cdata["encryption"].(map[string]any)
	if enc == nil {
		t.Fatalf(".content.encryption missing, got %v", cdata)
	}
	if enc["algo"] != "AES-256-GCM" || enc["nonce"] != "test-nonce-b64" ||
		enc["wrappedCEK"] != "test-wrapped-cek-b64" || enc["keyId"] != applicant {
		t.Fatalf(".content.encryption envelope wrong: %v", enc)
	}
	if cdata["digest"] != digest {
		t.Fatalf(".content.digest must stay the caller-supplied (plaintext) digest, got %v", cdata["digest"])
	}
}

// TestObject_SensitiveAttach_CrossIdentity_NoDedup proves the §4.1 decision:
// two identities uploading byte-identical sensitive content get DISTINCT
// object vertices (identity-salted oid) — no shared-ownership PII linkage
// leak, unlike the non-sensitive content-addressed path.
func TestObject_SensitiveAttach_CrossIdentity_NoDedup(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensdedup", Instance: "objsensdedup-1"})

	identityA := "vtx.identity." + testutil.GenReqID("sensidentityA")
	identityB := "vtx.identity." + testutil.GenReqID("sensidentityB")
	seedIdentity(t, ctx, conn, identityA, false)
	seedIdentity(t, ctx, conn, identityB, false)

	digest := "SHA-256=sharedPlaintextDigestExample"
	oidA := substrate.SHA256NanoID("object:" + identityA + ":" + digest)
	oidB := substrate.SHA256NanoID("object:" + identityB + ":" + digest)
	if oidA == oidB {
		t.Fatalf("distinct identities must salt to distinct oids")
	}

	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensdedupA"), "AttachObject",
		sensitiveAttachPayload(digest, identityA, identityA), []string{identityA}, processor.OutcomeAccepted)
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensdedupB"), "AttachObject",
		sensitiveAttachPayload(digest, identityB, identityB), []string{identityB}, processor.OutcomeAccepted)

	if !liveExists(ctx, conn, "vtx.object."+oidA) {
		t.Fatalf("identity A's object vertex not created")
	}
	if !liveExists(ctx, conn, "vtx.object."+oidB) {
		t.Fatalf("identity B's object vertex not created")
	}
}

// TestObject_SensitiveAttach_KeyIdMismatch_Rejected proves the fail-closed
// consistency check between the two caller-supplied identity references — a
// mismatched encryption.keyId/governingIdentity pair is rejected rather than
// silently trusting one over the other.
func TestObject_SensitiveAttach_KeyIdMismatch_Rejected(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensmismatch", Instance: "objsensmismatch-1"})

	applicant := "vtx.identity." + testutil.GenReqID("sensmismatch1")
	other := "vtx.identity." + testutil.GenReqID("sensmismatch2")
	seedIdentity(t, ctx, conn, applicant, false)

	digest := "SHA-256=mismatchTESTdigestExampleAA"
	payload := sensitiveAttachPayload(digest, applicant, applicant)
	payload["encryption"].(map[string]any)["keyId"] = other // governingIdentity=applicant, encryption.keyId=other

	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensmismatch"), "AttachObject",
		payload, []string{applicant}, processor.OutcomeRejected)
}

// TestObject_SensitiveAttach_MissingEncryption_Rejected proves sensitive:true
// requires the full envelope — a client that forgets to wrap the CEK must not
// silently mint an unencrypted "sensitive" object.
func TestObject_SensitiveAttach_MissingEncryption_Rejected(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensnoenc", Instance: "objsensnoenc-1"})

	applicant := "vtx.identity." + testutil.GenReqID("sensnoencrypt1")
	seedIdentity(t, ctx, conn, applicant, false)

	payload := map[string]any{
		"digest": "SHA-256=noEncTESTdigestExampleAAA", "size": 10, "contentType": "application/pdf",
		"storeName": "s-noenc", "targetKey": applicant, "linkName": "signedLeaseOf",
		"sensitive": true, "governingIdentity": applicant,
	}
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensnoenc"), "AttachObject",
		payload, []string{applicant}, processor.OutcomeRejected)
}

// TestObject_SensitiveAttach_OidGoldenValue pins the identity-salted oid
// FORMULA itself (not merely "the DDL agrees with Go's own SHA256NanoID"): a
// hardcoded literal NanoID for a fixed (keyId, digest) pair, so a drift in
// either the Starlark script's salt string or Go's SHA256NanoID would be
// caught even if both drifted identically (a self-referential compare would
// not catch that).
func TestObject_SensitiveAttach_OidGoldenValue(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensgolden", Instance: "objsensgolden-1"})

	const (
		goldenKeyID  = "vtx.identity.goCdenidentity1STUVW"
		goldenDigest = "SHA-256=goldenTESTdigestExampleAAA"
		goldenOid    = "6MfkrmTEYN5xJsnuiDZT"
	)
	seedIdentity(t, ctx, conn, goldenKeyID, false)

	if got := substrate.SHA256NanoID("object:" + goldenKeyID + ":" + goldenDigest); got != goldenOid {
		t.Fatalf("golden oid drifted: SHA256NanoID(\"object:\"+keyId+\":\"+digest) = %q, want %q", got, goldenOid)
	}

	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensgolden"), "AttachObject",
		sensitiveAttachPayload(goldenDigest, goldenKeyID, goldenKeyID), []string{goldenKeyID}, processor.OutcomeAccepted)

	if !liveExists(ctx, conn, "vtx.object."+goldenOid) {
		t.Fatalf("object vertex at the golden oid vtx.object.%s not created — the DDL's identity-salt formula drifted", goldenOid)
	}
}

// TestObject_SensitiveAttach_NonBoolSensitive_Rejected proves the fail-closed
// fix for the security-classification flag: a non-bool `sensitive` (e.g. a
// caller/serialization bug sending the string "true") must be rejected, never
// silently coerced to false (which would mint a plaintext, non-identity-salted
// object with no error).
func TestObject_SensitiveAttach_NonBoolSensitive_Rejected(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensnonbool", Instance: "objsensnonbool-1"})

	applicant := "vtx.identity." + testutil.GenReqID("sensnonbool1")
	seedIdentity(t, ctx, conn, applicant, false)

	payload := map[string]any{
		"digest": "SHA-256=nonBoolTESTdigestExampleA", "size": 10, "contentType": "application/pdf",
		"storeName": "s-nonbool", "targetKey": applicant, "linkName": "signedLeaseOf",
		"sensitive": "true", "governingIdentity": applicant,
	}
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensnonbool"), "AttachObject",
		payload, []string{applicant}, processor.OutcomeRejected)
}

// TestObject_SensitiveAttach_GoverningIdentityMeta_Rejected mirrors the
// existing targetKey CC7 guard: governingIdentity must never be a meta/system
// vertex either.
func TestObject_SensitiveAttach_GoverningIdentityMeta_Rejected(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensmeta", Instance: "objsensmeta-1"})

	applicant := "vtx.identity." + testutil.GenReqID("sensmetatarget1")
	metaID := "vtx.meta." + testutil.GenReqID("sensmetagoverns1")
	seedIdentity(t, ctx, conn, applicant, false)

	payload := sensitiveAttachPayload("SHA-256=metaTESTdigestExampleAAAA", applicant, metaID)
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("sensmeta"), "AttachObject",
		payload, []string{applicant}, processor.OutcomeRejected)
}

// TestObject_SensitiveAttach_DedupKeepsFirstEnvelope proves the live→dedup
// branch's existing behavior (unchanged by this fire, extended coherently to
// sensitive objects): a SECOND owner attaching to the same (governingIdentity,
// digest) — an identity-salted dedup, mirroring TestObject_Lifecycle's
// cross-owner dedup step — does NOT overwrite .content.encryption with the
// new call's envelope, exactly mirroring how storeName also stays pinned to
// the first upload. The first envelope is the one that decrypts the bytes
// actually stored under the first storeName; a second upload's (different,
// since CEK is random per upload) envelope would reference bytes never
// referenced by any live link.
func TestObject_SensitiveAttach_DedupKeepsFirstEnvelope(t *testing.T) {
	ctx, conn := setupObjectsEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "objsensdedupenv", Instance: "objsensdedupenv-1"})

	governingIdentity := "vtx.identity." + testutil.GenReqID("sensdedupenvgov1")
	owner1 := "vtx.identity." + testutil.GenReqID("sensdedupenvown1")
	owner2 := "vtx.identity." + testutil.GenReqID("sensdedupenvown2")
	seedIdentity(t, ctx, conn, governingIdentity, false)
	seedIdentity(t, ctx, conn, owner1, false)
	seedIdentity(t, ctx, conn, owner2, false)

	digest := "SHA-256=dedupEnvTESTdigestExampleA"
	oid := substrate.SHA256NanoID("object:" + governingIdentity + ":" + digest)
	objKey := "vtx.object." + oid
	contentKey := objKey + ".content"

	first := sensitiveAttachPayload(digest, owner1, governingIdentity)
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("dedupenv1"), "AttachObject",
		first, []string{owner1}, processor.OutcomeAccepted)

	// A second owner attaching the SAME (governingIdentity, digest) dedups to
	// the same identity-salted oid — but with a DIFFERENT envelope, as a fresh
	// client-side re-wrap would produce (random CEK/nonce per upload). The
	// stored .content.encryption must stay the FIRST one.
	second := sensitiveAttachPayload(digest, owner2, governingIdentity)
	second["encryption"].(map[string]any)["nonce"] = "second-nonce-b64"
	second["encryption"].(map[string]any)["wrappedCEK"] = "second-wrapped-cek-b64"
	submitObj(t, ctx, conn, cp, cons, testutil.GenReqID("dedupenv2"), "AttachObject",
		second, []string{owner2, objKey, contentKey}, processor.OutcomeAccepted)

	contentDoc, _ := readDoc(t, ctx, conn, contentKey)
	cdata, _ := contentDoc["data"].(map[string]any)
	enc, _ := cdata["encryption"].(map[string]any)
	if enc["nonce"] != "test-nonce-b64" || enc["wrappedCEK"] != "test-wrapped-cek-b64" {
		t.Fatalf(".content.encryption must stay the FIRST upload's envelope on dedup, got %v", enc)
	}
}
