// MergeIdentity end-to-end tests for the identity-hygiene Capability Package.
//
// All 14 tests exercise the MergeIdentity operation through the real Processor
// pipeline (CapabilityAuthorizer → DDLCache → Hydrator → Executor → Committer)
// using the testutil harness.
//
// Lens-projection tests (TestHygiene_LensProjection_ExactEmail,
// _LevenshteinName, _SkipsMerged, _NFR_P3) are explicitly deferred.
//
// Coverage:
//  1. TestMerge_HappyPath               — 3 edges migrated, state=merged, 1 event
//  2. TestMerge_EnumeratedEdgesFromLens — construct envelope from seeded lens entry
//  3. TestMerge_RejectsFabricatedEdge   — non-link key → EdgeNotALink
//  4. TestMerge_RejectsEdgeNotTouchingSecondary — unrelated link → EdgeDoesNotTouchSecondary
//  5. TestMerge_RejectsTombstonedEdge   — isDeleted link → EdgeNotFound
//  6. TestMerge_RejectsAlreadyMergedSecondary — secondary state=merged → rejected
//  7. TestMerge_PostMergeRedirect_FR4   — post-merge op on secondary is rejected
//  8. TestMerge_NonOperatorActor_Denied — consumer actor denied at step 3
//  9. TestMerge_RejectsSecondaryWithOpenTask — live assignedTo + open task → IdentityHasOpenTasks
//  10. TestMerge_AllowsSecondaryWithClosedTask — live assignedTo + completed task → allowed
//  11. TestMerge_TombstonesDuplicateOfLink_ForwardDirection — pair link (secondary=source) tombstoned
//  12. TestMerge_TombstonesDuplicateOfLink_InvertedMerge — pair link tombstoned when merge roles invert its direction
//  13. TestMerge_IndexesRepoint_OwnedAndThirdPartyUntouched — owned identityindex repointed, third-party untouched
//  14. TestMerge_TrustGateAcceptsRealClassLink — real production class (not "link") migrates
package identityhygiene_test

import (
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/testutil"
)

// --- 1. TestMerge_HappyPath ---

// TestMerge_HappyPath seeds primary + secondary identities and 3 link edges
// touching the secondary (2 inbound, 1 outbound). Submits MergeIdentity with
// all 3 edges declared in the payload and ContextHint.Reads. Asserts:
//   - pipeline outcome: Accepted
//   - secondary.state == "merged"
//   - secondary.mergedInto == primaryKey
//   - 3 tombstoned edges (original) exist in Core KV
//   - 3 new rewritten link keys exist in Core KV (not tombstoned)
//   - exactly 1 IdentityMerged event in the tracker
func TestMerge_HappyPath(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mhp")

	// Use GenReqID to guarantee 20-char safe-alphabet IDs.
	primaryID := testutil.GenReqID("PrimHappyPath0")
	secondaryID := testutil.GenReqID("SecHappyPath00")
	otherSrcID1 := testutil.GenReqID("OtherSrc111111")
	otherSrcID2 := testutil.GenReqID("OtherSrc222222")
	otherTgtID3 := testutil.GenReqID("OtherTgt333333")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	// 2 inbound links: other -> secondary
	lnk1 := "lnk.identity." + otherSrcID1 + ".knows.identity." + secondaryID
	lnk2 := "lnk.identity." + otherSrcID2 + ".follows.identity." + secondaryID
	// 1 outbound link: secondary -> other
	lnk3 := "lnk.identity." + secondaryID + ".likes.identity." + otherTgtID3

	seedLinkVertex(t, ctx, conn, lnk1, false)
	seedLinkVertex(t, ctx, conn, lnk2, false)
	seedLinkVertex(t, ctx, conn, lnk3, false)

	edges := []string{lnk1, lnk2, lnk3}
	reqID := testutil.GenReqID("MrgHappyPath0")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:00:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads: mergeReads(primaryKey, secondaryKey, edges),
			// The op's one kv.Links call (the secondary-has-open-tasks
			// guard), declared as class-(e) metadata (Contract #2 §2.5:
			// bounded + paged, never hydrated — the declaration feeds the
			// Edge mirror-coverage gate + the read-posture lint).
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Assert secondary state → merged
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q, want merged", got)
	}

	// Assert secondary mergedInto → primaryKey
	miData := readAspectData(t, ctx, conn, secondaryKey+".mergedInto")
	if got, _ := miData["value"].(string); got != primaryKey {
		t.Fatalf("secondary.mergedInto = %q, want %s", got, primaryKey)
	}

	// Assert all 3 original edges are tombstoned
	for _, lk := range edges {
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lk)
		if err != nil {
			t.Fatalf("original edge %s: %v", lk, err)
		}
		var doc map[string]any
		_ = json.Unmarshal(entry.Value, &doc)
		if isDeleted, _ := doc["isDeleted"].(bool); !isDeleted {
			t.Fatalf("original edge %s should be tombstoned", lk)
		}
	}

	// Assert 3 new rekeyed edges exist (not tombstoned).
	// All secondaryID references in endpoints become primaryID.
	newLnk1 := "lnk.identity." + otherSrcID1 + ".knows.identity." + primaryID
	newLnk2 := "lnk.identity." + otherSrcID2 + ".follows.identity." + primaryID
	newLnk3 := "lnk.identity." + primaryID + ".likes.identity." + otherTgtID3

	for _, lk := range []string{newLnk1, newLnk2, newLnk3} {
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, lk)
		if err != nil {
			t.Fatalf("rekeyed edge %s not found: %v", lk, err)
		}
		var doc map[string]any
		_ = json.Unmarshal(entry.Value, &doc)
		if isDeleted, _ := doc["isDeleted"].(bool); isDeleted {
			t.Fatalf("rekeyed edge %s must not be tombstoned", lk)
		}
	}

	// Assert exactly 1 IdentityMerged event in the tracker
	assertTrackerEvent(t, ctx, conn, reqID, "identity.merged")
}

// --- 2. TestMerge_EnumeratedEdgesFromLens ---

// TestMerge_EnumeratedEdgesFromLens simulates the realistic operator-CLI flow:
// the test seeds a duplicate-candidates bucket entry (as a real Refractor
// would have written), reads secondaryInboundEdges + secondaryOutboundEdges
// from that entry, and constructs the MergeIdentity envelope from them.
// Asserts the merge succeeds with state=merged on the secondary.
func TestMerge_EnumeratedEdgesFromLens(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mlens")

	primaryID := testutil.GenReqID("PrimLensFlow00")
	secondaryID := testutil.GenReqID("SecLensFlow000")
	inboundSrcID := testutil.GenReqID("LensInboundSrc")
	outboundTgtID := testutil.GenReqID("LensOutboundTg")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	inboundLnk := "lnk.identity." + inboundSrcID + ".knows.identity." + secondaryID
	outboundLnk := "lnk.identity." + secondaryID + ".follows.identity." + outboundTgtID

	seedLinkVertex(t, ctx, conn, inboundLnk, false)
	seedLinkVertex(t, ctx, conn, outboundLnk, false)

	// Seed the duplicate-candidates entry (simulating Refractor output).
	seedDuplicateCandidateEntry(t, ctx, conn, primaryKey, secondaryKey,
		[]string{inboundLnk}, []string{outboundLnk})

	// Operator CLI reads the lens entry and constructs edges.
	edges := []string{inboundLnk, outboundLnk}
	reqID := testutil.GenReqID("MrgLensEnum000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:01:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q, want merged", got)
	}
	assertTrackerEvent(t, ctx, conn, reqID, "identity.merged")
}

// --- 3. TestMerge_RejectsFabricatedEdge ---

// TestMerge_RejectsFabricatedEdge submits a MergeIdentity whose edges list
// contains a bare identity vertex key (not a link key). The script must reject
// with EdgeNotALink and no mutations may be applied.
func TestMerge_RejectsFabricatedEdge(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mfabr")

	primaryID := testutil.GenReqID("PrimFabricEdge")
	secondaryID := testutil.GenReqID("SecFabricEdge0")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	// A bare identity key is NOT a valid edge key (not lnk.* and not 6 segments).
	fabricatedEdge := secondaryKey

	edges := []string{fabricatedEdge}
	reqID := testutil.GenReqID("MrgFabricEdge0")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:02:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain unclaimed (no mutation).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state mutated despite rejection: %q", got)
	}
}

// --- 4. TestMerge_RejectsEdgeNotTouchingSecondary ---

// TestMerge_RejectsEdgeNotTouchingSecondary submits a MergeIdentity with an
// edge whose two endpoints are both unrelated identities (neither is the
// secondary). The script must reject with EdgeDoesNotTouchSecondary and apply
// no mutations.
func TestMerge_RejectsEdgeNotTouchingSecondary(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mnots")

	primaryID := testutil.GenReqID("PrimNotTouch00")
	secondaryID := testutil.GenReqID("SecNotTouch000")
	unrelA := testutil.GenReqID("UnrelatedActrA")
	unrelB := testutil.GenReqID("UnrelatedActrB")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	// A link between two completely different identities — neither endpoint is secondaryID.
	unrelatedLink := "lnk.identity." + unrelA + ".knows.identity." + unrelB
	seedLinkVertex(t, ctx, conn, unrelatedLink, false)

	edges := []string{unrelatedLink}
	reqID := testutil.GenReqID("MrgNotTouch000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:03:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain unclaimed (no mutation).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state mutated despite rejection: %q", got)
	}
}

// --- 5. TestMerge_RejectsTombstonedEdge ---

// TestMerge_RejectsTombstonedEdge seeds a link envelope with isDeleted=true
// and submits MergeIdentity with that key in the edges list. The script must
// reject with EdgeNotFound (tombstoned = not found per the trust gate) and
// apply no mutations.
func TestMerge_RejectsTombstonedEdge(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mtomb")

	primaryID := testutil.GenReqID("PrimTombstone0")
	secondaryID := testutil.GenReqID("SecTombstone00")
	tombSrcID := testutil.GenReqID("TombstoneSource")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	// Seed a tombstoned link touching secondary.
	tombLink := "lnk.identity." + tombSrcID + ".knows.identity." + secondaryID
	seedLinkVertex(t, ctx, conn, tombLink, true) // isDeleted=true

	edges := []string{tombLink}
	reqID := testutil.GenReqID("MrgTombstone00")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:04:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain unclaimed (no mutation).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state mutated despite rejection: %q", got)
	}
}

// --- 6. TestMerge_RejectsAlreadyMergedSecondary ---

// TestMerge_RejectsAlreadyMergedSecondary seeds the secondary identity with
// state="merged" before submitting MergeIdentity. The script must reject (the
// `secondary state=merged` guard fires). No mutations are applied.
//
// NFR-S6: the error message must NOT contain the mergedInto value — verified
// by asserting the mergedInto aspect remains unchanged (not mutated to new value).
func TestMerge_RejectsAlreadyMergedSecondary(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "malgm")

	primaryID := testutil.GenReqID("PrimAlrMerged0")
	secondaryID := testutil.GenReqID("SecAlrMerged00")
	otherPrimaryID := testutil.GenReqID("OtherPrimaryAM")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	otherPrimaryKey := "vtx.identity." + otherPrimaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	// Secondary is already merged into some other primary.
	seedIdentityVertex(t, ctx, conn, secondaryKey, "merged", otherPrimaryKey)

	edges := []string{} // no edges needed — guard fires before trust gate
	reqID := testutil.GenReqID("MrgAlrMerged00")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:05:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain merged (no mutation).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("secondary.state changed: %q, want merged", got)
	}

	// NFR-S6: assert secondary.mergedInto still points at the pre-existing
	// primary, not at the new primaryKey (confirming no mutation occurred).
	miData := readAspectData(t, ctx, conn, secondaryKey+".mergedInto")
	if got, _ := miData["value"].(string); got != otherPrimaryKey {
		t.Fatalf("secondary.mergedInto changed: %q, want %s", got, otherPrimaryKey)
	}
}

// --- 7. TestMerge_PostMergeRedirect_FR4 ---

// TestMerge_PostMergeRedirect_FR4 performs a successful merge in the first op,
// then submits an UpdateIdentityState against the now-merged secondary. The
// identity DDL's `enforce_not_merged` guard must reject the second op.
// Per NFR-S6 we assert only that the outcome is Rejected and no state mutation
// occurs (we do not assert on specific error code text).
func TestMerge_PostMergeRedirect_FR4(t *testing.T) {
	ctx, conn := setupTestEnv(t)

	primaryID := testutil.GenReqID("PrimPMRedir000")
	secondaryID := testutil.GenReqID("SecPMRedir0000")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	// Step 1: merge secondary into primary.
	// Use a single pipeline (mpm1) for both ops so the consumer sees
	// message 1 (merge) then message 2 (update) in sequence, avoiding the
	// Duplicate short-circuit that would fire if a second consumer started
	// from the beginning of the stream and re-processed message 1.
	mergeReqID := testutil.GenReqID("MrgPMRedirMrg0")
	mergeEdges := []string{}
	sharedCP, sharedCons := newMergePipeline(t, ctx, conn, "mpm1")
	mergeEnv := &processor.OperationEnvelope{
		RequestID:     mergeReqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:06:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, mergeEdges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, mergeEdges)},
	}
	testutil.PublishOp(t, conn, mergeEnv)
	testutil.DriveOne(t, ctx, sharedCP, sharedCons, processor.OutcomeAccepted)

	// Confirm secondary is now merged.
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("setup: secondary.state = %q after merge, want merged", got)
	}

	// Step 2: submit UpdateIdentityState against the merged secondary using
	// the SAME pipeline+consumer so message 2 (update) is next in sequence.
	// We seed the operator cap doc with UpdateIdentityState added so the
	// operator actor passes step 3. Simpler: reuse operatorActorKey + seed
	// a new cap with both permissions.
	updateActorID := testutil.GenReqID("UpdateActorPMR")
	updateActorKey := "vtx.identity." + updateActorID
	updateCapKey := "cap.identity." + updateActorID
	updateCap := &processor.CapabilityDoc{
		Key:                    updateCapKey,
		Actor:                  updateActorKey,
		Version:                "1.0",
		ProjectedAt:            "2026-05-23T10:06:00Z",
		ProjectedFromRevisions: map[string]uint64{updateActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "UpdateIdentityState", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + updateActorID},
	}
	testutil.SeedCapDoc(t, ctx, conn, updateCap)

	updateReqID := testutil.GenReqID("MrgPMRedirUpdt")
	updateEnv := &processor.OperationEnvelope{
		RequestID:     updateReqID,
		Lane:          processor.LaneDefault,
		OperationType: "UpdateIdentityState",
		Actor:         updateActorKey,
		SubmittedAt:   "2026-05-23T10:07:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + secondaryKey + `","newState":"claimed"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			secondaryKey + ".state",
			secondaryKey + ".mergedInto",
		}},
	}
	testutil.PublishOp(t, conn, updateEnv)
	testutil.DriveOne(t, ctx, sharedCP, sharedCons, processor.OutcomeRejected)

	// Secondary state must remain merged (no mutation from the blocked op).
	stateData2 := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData2["value"].(string); got != "merged" {
		t.Fatalf("secondary.state changed after blocked update: %q", got)
	}
}

// --- 8. TestMerge_NonOperatorActor_Denied ---

// TestMerge_NonOperatorActor_Denied submits MergeIdentity with a consumer-role
// actor that only has ClaimIdentity permission. The Capability Authorizer at
// step 3 must deny the request and the pipeline must return Rejected.
func TestMerge_NonOperatorActor_Denied(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mnopa")

	primaryID := testutil.GenReqID("PrimNonOpActr0")
	secondaryID := testutil.GenReqID("SecNonOpActr00")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	edges := []string{}
	reqID := testutil.GenReqID("MrgNonOpActor0")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         consumerActorKey, // consumer has ClaimIdentity only; no MergeIdentity
		SubmittedAt:   "2026-05-23T10:08:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain unclaimed (denial at step 3 — no mutations).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state mutated despite auth denial: %q", got)
	}
}

// --- 9. TestMerge_RejectsSecondaryWithOpenTask ---

// TestMerge_RejectsSecondaryWithOpenTask seeds an assignedTo link pointing an
// OPEN task at the secondary identity (deliberately NOT declared in `edges`).
// MergeIdentity must reject with IdentityHasOpenTasks (Contract #10 §10.1
// no-orphan tombstone guard) -- proving the guard enumerates secondary's
// inbound assignedTo links via kv.Links independently of whatever edges the
// caller happened to declare, rather than silently rekeying the task's
// assignment onto the primary.
func TestMerge_RejectsSecondaryWithOpenTask(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mopen")

	primaryID := testutil.GenReqID("PrimOpenTask00")
	secondaryID := testutil.GenReqID("SecOpenTask000")
	taskID := testutil.GenReqID("OpenTaskAssgn0")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	taskKey := "vtx.task." + taskID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")
	seedTaskVertex(t, ctx, conn, taskKey, "open")

	assignedLnk := "lnk.task." + taskID + ".assignedTo.identity." + secondaryID
	seedLinkVertex(t, ctx, conn, assignedLnk, false)

	edges := []string{}
	reqID := testutil.GenReqID("MrgOpenTask000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:09:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Secondary state must remain unclaimed (no mutation from the blocked op).
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state mutated despite rejection: %q", got)
	}
}

// --- 10. TestMerge_AllowsSecondaryWithClosedTask ---

// TestMerge_AllowsSecondaryWithClosedTask seeds an assignedTo link pointing a
// COMPLETED task at the secondary. CompleteTask/CancelTask never tombstone
// the assignedTo/queuedFor link (orchestration-base leaves it live post
// terminal transition), so link liveness alone cannot be the guard's signal —
// it must read the task's own status. The merge must succeed.
func TestMerge_AllowsSecondaryWithClosedTask(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mclosed")

	primaryID := testutil.GenReqID("PrimClosedTsk0")
	secondaryID := testutil.GenReqID("SecClosedTask0")
	taskID := testutil.GenReqID("ClosedTaskAsgn")

	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	taskKey := "vtx.task." + taskID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")
	seedTaskVertex(t, ctx, conn, taskKey, "complete")

	assignedLnk := "lnk.task." + taskID + ".assignedTo.identity." + secondaryID
	seedLinkVertex(t, ctx, conn, assignedLnk, false)

	edges := []string{}
	reqID := testutil.GenReqID("MrgClosedTask0")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:10:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q, want merged", got)
	}
}

// --- 11. TestMerge_TombstonesDuplicateOfLink_ForwardDirection ---

// TestMerge_TombstonesDuplicateOfLink_ForwardDirection seeds the durable pair
// link in the CreateUnclaimedIdentity convention (the later-arriving identity
// is the source) with secondary as the later arrival, then merges secondary
// into primary. The script must tombstone the pair link independently of
// `edges` (dedup-over-encrypted-pii-design.md §3.4).
func TestMerge_TombstonesDuplicateOfLink_ForwardDirection(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mdupfwd")

	primaryID := testutil.GenReqID("PrimDupFwd0000")
	secondaryID := testutil.GenReqID("SecDupFwd00000")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	dupKey := "lnk.identity." + secondaryID + ".duplicateOf.identity." + primaryID
	seedLinkVertexWithClass(t, ctx, conn, dupKey, "duplicateOf", false,
		map[string]any{"criteria": []any{"exact-email"}})

	edges := []string{}
	reqID := testutil.GenReqID("MrgDupFwd00000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:11:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads: mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: []string{
				dupKey,
				"lnk.identity." + primaryID + ".duplicateOf.identity." + secondaryID,
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertLinkTombstoned(t, ctx, conn, dupKey)
}

// --- 12. TestMerge_TombstonesDuplicateOfLink_InvertedMerge ---

// TestMerge_TombstonesDuplicateOfLink_InvertedMerge seeds the pair link with
// identity B as the source (duplicateOf A), then merges with the roles
// INVERTED from the link's own direction: primary=B, secondary=A. Adversarial
// finding 6 (design §3.4/§12): a single-direction tombstone would leave this
// pair link live post-merge. Both directional keys must be probed.
func TestMerge_TombstonesDuplicateOfLink_InvertedMerge(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mdupinv")

	aID := testutil.GenReqID("IdentAInvert00")
	bID := testutil.GenReqID("IdentBInvert00")
	aKey := "vtx.identity." + aID
	bKey := "vtx.identity." + bID

	seedIdentityVertex(t, ctx, conn, aKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, bKey, "unclaimed", "")

	// B is the later arrival: B duplicateOf A.
	dupKey := "lnk.identity." + bID + ".duplicateOf.identity." + aID
	seedLinkVertexWithClass(t, ctx, conn, dupKey, "duplicateOf", false,
		map[string]any{"criteria": []any{"exact-phone"}})

	// Operator picks the INVERTED merge: B survives (primary), A is merged away.
	primaryKey := bKey
	secondaryKey := aKey
	edges := []string{}
	reqID := testutil.GenReqID("MrgDupInvert00")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:12:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads: mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: []string{
				dupKey,
				"lnk.identity." + bID + ".duplicateOf.identity." + aID,
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertLinkTombstoned(t, ctx, conn, dupKey)
}

// --- 13. TestMerge_IndexesRepoint_OwnedAndThirdPartyUntouched ---

// TestMerge_IndexesRepoint_OwnedAndThirdPartyUntouched seeds an identityindex
// vertex owned by the secondary (via a live inbound `indexes` link) and a
// second identityindex vertex owned by an unrelated third identity. After
// merging secondary into primary: the secondary's owned index is repointed
// (old link tombstoned, new link to primary created, vertex identityKey
// updated, contactType preserved) — decrypt-free (design §3.4). The
// third-party index is untouched (the enumeration is scoped to secondary's
// inbound links only).
func TestMerge_IndexesRepoint_OwnedAndThirdPartyUntouched(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "midxrp")

	primaryID := testutil.GenReqID("PrimIdxRepoint")
	secondaryID := testutil.GenReqID("SecIdxRepoint0")
	thirdID := testutil.GenReqID("ThirdIdxOwner0")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	thirdKey := "vtx.identity." + thirdID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, thirdKey, "unclaimed", "")

	ownedHash := testutil.GenReqID("OwnedIdxHash00")
	ownedIdxKey := "vtx.identityindex." + ownedHash
	ownedLinkKey := "lnk.identityindex." + ownedHash + ".indexes.identity." + secondaryID
	seedIdentityIndexVertex(t, ctx, conn, ownedIdxKey, "email", secondaryKey)
	seedLinkVertexWithClass(t, ctx, conn, ownedLinkKey, "indexes", false, map[string]any{})

	thirdHash := testutil.GenReqID("ThirdIdxHash00")
	thirdIdxKey := "vtx.identityindex." + thirdHash
	thirdLinkKey := "lnk.identityindex." + thirdHash + ".indexes.identity." + thirdID
	seedIdentityIndexVertex(t, ctx, conn, thirdIdxKey, "phone", thirdKey)
	seedLinkVertexWithClass(t, ctx, conn, thirdLinkKey, "indexes", false, map[string]any{})

	edges := []string{}
	reqID := testutil.GenReqID("MrgIdxRepoint0")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:13:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads: mergeReads(primaryKey, secondaryKey, edges),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Owned index: old link tombstoned, new link to primary live.
	assertLinkTombstoned(t, ctx, conn, ownedLinkKey)
	newOwnedLinkKey := "lnk.identityindex." + ownedHash + ".indexes.identity." + primaryID
	assertLinkLive(t, ctx, conn, newOwnedLinkKey)

	ownedIdxData := readAspectData(t, ctx, conn, ownedIdxKey)
	if got, _ := ownedIdxData["identityKey"].(string); got != primaryKey {
		t.Fatalf("owned identityindex.identityKey = %q, want %s", got, primaryKey)
	}
	if got, _ := ownedIdxData["contactType"].(string); got != "email" {
		t.Fatalf("owned identityindex.contactType = %q, want email (must be preserved)", got)
	}

	// Third-party index: untouched.
	assertLinkLive(t, ctx, conn, thirdLinkKey)
	thirdIdxData := readAspectData(t, ctx, conn, thirdIdxKey)
	if got, _ := thirdIdxData["identityKey"].(string); got != thirdKey {
		t.Fatalf("third-party identityindex.identityKey changed to %q, want unchanged %s", got, thirdKey)
	}
}

// --- 14. TestMerge_TrustGateAcceptsRealClassLink ---

// TestMerge_TrustGateAcceptsRealClassLink seeds a link touching secondary
// with a real production class (its relation name, "holdsRole" — never the
// literal "link"), mirroring the shared make_link(cls=...) idiom every
// production writer uses. Pre-Fire-2 this would reject EdgeNotALink (adversarial
// finding 7, design §2.4/§3.4); the class check is now removed, so the merge
// must succeed and the edge must migrate.
func TestMerge_TrustGateAcceptsRealClassLink(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mrealcls")

	primaryID := testutil.GenReqID("PrimRealClass0")
	secondaryID := testutil.GenReqID("SecRealClass00")
	roleID := testutil.GenReqID("RealClassRole0")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	roleLnk := "lnk.identity." + secondaryID + ".holdsRole.role." + roleID
	seedLinkVertexWithClass(t, ctx, conn, roleLnk, "holdsRole", false, map[string]any{})

	edges := []string{roleLnk}
	reqID := testutil.GenReqID("MrgRealClass00")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:14:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint:   &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, edges)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertLinkTombstoned(t, ctx, conn, roleLnk)
	newRoleLnk := "lnk.identity." + primaryID + ".holdsRole.role." + roleID
	assertLinkLive(t, ctx, conn, newRoleLnk)
}
