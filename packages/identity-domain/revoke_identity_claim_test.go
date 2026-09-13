// RevokeIdentityClaim integration tests for the identity-domain Capability
// Package — the operator undo for a claim made by the wrong person
// (docs/components/_packages.md, "Identity claim custody").
//
// Every rejection asserts the script's own outcome word rather than the bare
// "rejected" MessageOutcome, for the reason credential_reconcile_test.go
// records: authorization, hydration and payload errors all collapse to one
// outcome, so a word-less assertion cannot tell that the guard under test
// fired at all.
//
// Coverage:
//  1. TestRevokeIdentityClaim_Success — the rogue's credential is cut off,
//     the identity returns to unclaimed with the new secret armed, the old
//     secret no longer claims, the new one does (as a different credential),
//     and the consumer grant the revoke tombstoned is revived by that claim.
//  2. TestRevokeIdentityClaim_Unclaimed_WrongState — RotateClaimKey's job.
//  3. TestRevokeIdentityClaim_Provisioned_NotSecretClaimed — a
//     Gateway-provisioned credential identity never carried a .claimKey.
//  4. TestRevokeIdentityClaim_NonOperator_AuthDenied — the capability gate,
//     not the script, stops front-desk staff.
//  5. TestRevokeIdentityClaim_TombstonedBinding_NothingToRevoke.
//  6. TestRevokeIdentityClaim_UndeclaredSubmitter_Refused — an envelope with
//     no contextHint is refused with a named outcome, never accepted.
//  7. TestRevokeIdentityClaim_LinkPlaneMissing_PlaneInconsistent — an array
//     entry with no live boundTo link.
//  8. TestRevokeIdentityClaim_ForeignIndex_OwnerMismatch — a live index that
//     names another owner is never tombstoned blind.
//  9. TestRevokeIdentityClaim_Merged_Rejected.
//  10. TestRevokeIdentityClaim_Sealed_Erased — the erasure write-path gate.
//  11. TestRevokeIdentityClaim_ConcurrentBindingWriteConflicts — the
//     .credentialBinding tombstone carries the revision hydration observed,
//     so a credential linked between read and commit conflicts the batch.
//  12. TestRevokeIdentityClaim_UnarmedLinkKeyIsTombstonedAnyway — the
//     .linkKey tombstone is unconditional, and the bodiless tombstone it
//     leaves reads as "no secret" to CompleteCredentialLink and is written
//     over by a later InitiateCredentialLink.
//  13. TestRevokeIdentityClaim_LiveClaimKey_ClaimKeyDrift.
//  14. TestRevokeIdentityClaim_UnnamedLiveLink_PlaneInconsistent — the other
//     direction: a live inbound boundTo link the array does not name.
//
// `too-many-credentials` (a cursor still open after CLAIM_REVOKE_MAX_PAGES
// pages, >200 boundTo links live or spent on one identity) is NOT covered:
// the fixture cost is out of proportion to a bound the batch cap already
// sits well under, and the branch is a plain cursor check.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// revokeEnv submits as the operator-equivalent staff actor with the
// descriptor's three declared reads and its one declared walk (opmetas.go),
// the `{payload.identityKey}` hub resolved by hand the way a dispatcher
// would. Nothing else is declared: .claimKey, .linkKey, the consumer grant and
// the erasure gate keys are derive_reads' to supply, and a test that declared
// them by hand would pass with the derivation deleted.
func revokeEnv(reqID, actorKey, identityKey, newHash string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RevokeIdentityClaim",
		Actor:         actorKey,
		SubmittedAt:   "2026-09-12T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","claimKeyHash":"` + newHash + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{identityKey, identityKey + ".state", identityKey + ".credentialBinding"},
			Enumerations: []processor.EnumerationHint{
				{Hub: identityKey, Relation: "boundTo", Direction: "in"},
			},
		},
	}
}

// assertRevokeRejected pins WHICH guard fired. `want` is the outcome word the
// script's fail_revoke emits.
func assertRevokeRejected(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope, want string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil {
		t.Fatalf("rejected with no error detail; want ClaimRevokeRejected: %s", want)
	}
	if !strings.Contains(reply.Error.Message, "ClaimRevokeRejected: "+want) {
		t.Fatalf("rejected with %q, want the %q guard — a rejection from anywhere else means the guard under test never ran",
			reply.Error.Message, want)
	}
}

// seedSpentClaimedIdentity builds the shape a secret-claimed identity has
// after ClaimIdentity, directly: state as given, a tombstoned .claimKey, and a
// live .credentialBinding naming credentialActorKey — without the boundTo
// link or the credentialindex the real claim also writes. Used by the
// rejection tests whose guard fires before the credential planes are read.
func seedSpentClaimedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, state, mergedInto, credentialActorKey string) {
	t.Helper()
	seedDirectIdentity(t, ctx, conn, identityKey, state, mergedInto)
	seedSpentClaimKeyAspect(t, ctx, conn, identityKey, sha256HexOf("spent-secret-"+identityKey))
	seedSensitiveAspect(t, ctx, conn, identityKey, "credentialBinding", map[string]any{
		"actorKey": credentialActorKey, "boundAt": "2026-09-12T09:00:00Z",
		"credentials": []any{map[string]any{"actorKey": credentialActorKey, "boundAt": "2026-09-12T09:00:00Z"}},
	})
}

// tombstoneKV flips isDeleted on an existing Core KV document, preserving its
// body the way step 8's tombstone does.
func tombstoneKV(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	doc["isDeleted"] = true
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone %s: %v", key, err)
	}
}

// repointCredentialIndex rewrites a live credentialindex vertex's identityKey
// so it names a different owner — the shape a foreign or stale index has, which
// no shipped op produces for a binding whose link is still live.
func repointCredentialIndex(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, newOwnerKey string) {
	t.Helper()
	key := credentialIndexKey(credentialActorKey)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	data["identityKey"] = newOwnerKey
	doc["data"] = data
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("repoint %s: %v", key, err)
	}
}

func TestRevokeIdentityClaim_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-succ")

	// The rogue claim: consumerActorKey claims the identity the desk minted.
	createReqID := testutil.GenReqID("RvkSuccCreate0")
	identityKey, oldPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)
	rogueClaimEnv := erasureClaimEnv(testutil.GenReqID("RvkSuccRogue00"), consumerActorKey, identityKey, oldPlaintext)
	testutil.PublishOp(t, conn, rogueClaimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	grantKey := consumerGrantLink(identityKey, consumerRoleKey(t))
	assertDocLive(t, ctx, conn, grantKey, "the claim grants consumer")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "the claim indexes the rogue credential")

	// The rogue also arms a link secret, the way they would to hand a second
	// credential in later. It must not outlive the claim.
	seedIdentityCapDoc(t, ctx, conn, identityKey, "InitiateCredentialLink")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("RvkSuccArm0000"), identityKey, sha256HexOf("rogue-link-secret")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertDocLive(t, ctx, conn, identityKey+".linkKey", "the rogue's link secret is armed")

	newPlaintext := "replacement-claim-secret-0001"
	newHash := sha256HexOf(newPlaintext)
	revokeReqID := testutil.GenReqID("RvkSuccRevoke0")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, revokeEnv(revokeReqID, staffActorKey, identityKey, newHash))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("revoke outcome = %q (%+v), want accepted", outcome, reply.Error)
	}
	if reply.PrimaryKey != identityKey {
		t.Fatalf("revoke reply primaryKey = %q, want %s", reply.PrimaryKey, identityKey)
	}

	// The identity is back to unclaimed, with the credential plane cut.
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state after revoke = %q, want unclaimed", got)
	}
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey), "the rogue credential must stop resolving")
	assertTombstonedBoundTo(t, ctx, conn, consumerActorKey, identityKey)
	assertTombstoned(t, ctx, conn, identityKey+".credentialBinding", "the credential set is revoked whole")
	assertTombstoned(t, ctx, conn, grantKey, "the consumer grant the claim conferred goes with it")
	assertTombstoned(t, ctx, conn, identityKey+".linkKey", "an armed link secret must not outlive the claim it belonged to")
	claimKey := readDecryptedAspectData(t, ctx, conn, identityKey, "claimKey")
	if got, _ := claimKey["hash"].(string); got != newHash {
		t.Fatalf("claimKey.hash after revoke = %q, want the replacement %q", got, newHash)
	}
	assertDocLive(t, ctx, conn, identityKey+".claimKey", "the replacement secret is armed, not left tombstoned")
	assertDocLive(t, ctx, conn, identityKey, "the identity vertex itself is untouched")
	assertDocLive(t, ctx, conn, identityKey+".name", "the person's PII is untouched")

	// The events: one identity.unbound per credential (the Gateway's
	// bucket-row delete) and one identity.claimRevoked naming the set.
	assertTrackerEvent(t, ctx, conn, revokeReqID, "identity.unbound")
	assertTrackerEvent(t, ctx, conn, revokeReqID, "identity.claimRevoked")
	unbound := assertOutboxEvent(t, ctx, conn, revokeReqID, "identity.unbound")
	if got, _ := unbound["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("identity.unbound.actorKey = %q, want the rogue credential %q", got, consumerActorKey)
	}
	if got, _ := unbound["identityKey"].(string); got != identityKey {
		t.Fatalf("identity.unbound.identityKey = %q, want %q", got, identityKey)
	}
	revoked := assertOutboxEvent(t, ctx, conn, revokeReqID, "identity.claimRevoked")
	creds, _ := revoked["credentials"].([]any)
	if len(creds) != 1 {
		t.Fatalf("identity.claimRevoked.credentials = %+v, want exactly the one rogue credential", creds)
	}
	if m, _ := creds[0].(map[string]any); m["actorKey"] != consumerActorKey || m["boundAt"] != rogueClaimEnv.SubmittedAt {
		t.Fatalf("identity.claimRevoked.credentials[0] = %+v, want {actorKey:%s boundAt:%s}", m, consumerActorKey, rogueClaimEnv.SubmittedAt)
	}

	// The rogue's old secret is spent: a claim with it is refused.
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("RvkSuccOldClm0"), consumerActorKey, identityKey, oldPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	stateAspect = readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("old-secret claim mutated state: %q, want unclaimed", got)
	}

	// The real person claims with the replacement secret, as a DIFFERENT
	// credential, and the claim's holdsRole upsert revives the tombstoned
	// consumer grant.
	seedIdentityCapDoc(t, ctx, conn, secondCredActorKey, "ClaimIdentity")
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("RvkSuccNewClm0"), secondCredActorKey, identityKey, newPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	stateAspect = readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state after the real claim = %q, want claimed", got)
	}
	assertDocLive(t, ctx, conn, grantKey, "ClaimIdentity's upsert revives the grant the revoke tombstoned")
	assertDocLive(t, ctx, conn, credentialIndexKey(secondCredActorKey), "the real credential is indexed")
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey), "the rogue credential stays cut off after the real claim")
	binding := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	bound, _ := binding["credentials"].([]any)
	if len(bound) != 1 {
		t.Fatalf("credentials after the real claim = %+v, want exactly the real credential", bound)
	}
	if m, _ := bound[0].(map[string]any); m["actorKey"] != secondCredActorKey {
		t.Fatalf("credentials[0] = %+v, want %s", m, secondCredActorKey)
	}
}

func TestRevokeIdentityClaim_Unclaimed_WrongState(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-unclaimed")

	identityKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("RvkUnclCreate0"))

	// .credentialBinding is a required read and an unclaimed identity has
	// none; the state guard fires before the script touches it, so the
	// envelope is the descriptor's — and a required-absent key only faults
	// when read.
	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkUnclDo00000"), staffActorKey, identityKey, sha256HexOf("whatever")), "wrong-state")

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed (untouched)", got)
	}
	assertDocLive(t, ctx, conn, identityKey+".claimKey", "an unclaimed identity's secret is RotateClaimKey's to replace, not this verb's")
}

func TestRevokeIdentityClaim_Provisioned_NotSecretClaimed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-provisioned")

	// A real Gateway first-touch provisioning: claimed, consumer-granted, and
	// never carried a .claimKey — it IS its own credential.
	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("RvkProvPCI0000"), freshActorKey, consumerRoleKey(t), "2026-09-12T09:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkProvDo00000"), staffActorKey, freshActorKey, sha256HexOf("whatever")), "not-secret-claimed")

	stateAspect := readAspectData(t, ctx, conn, freshActorKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed (untouched)", got)
	}
	if kvExists(t, ctx, conn, freshActorKey+".claimKey") {
		t.Fatalf("a claim secret was armed on a credential identity — that is the vertex that signs in, not a person's login to reset")
	}
	assertDocLive(t, ctx, conn, consumerGrantLink(freshActorKey, consumerRoleKey(t)), "the provisioned actor keeps its grant")
}

// TestRevokeIdentityClaim_NonOperator_AuthDenied proves the grant is what
// authorizes this, not the shape: frontDeskActorKey holds the same scope=any
// posture for its own ops and is exactly the role the doctrine says this verb
// must sit above.
func TestRevokeIdentityClaim_NonOperator_AuthDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-auth")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkAuth")

	// AuthDenied specifically, not merely "rejected": the envelope is
	// otherwise the accepted shape, so a rejection carrying a SCRIPT refusal
	// would mean the script ran for an actor that should never have reached
	// it.
	env := revokeEnv(testutil.GenReqID("RvkAuthDeny000"), frontDeskActorKey, identityKey, sha256HexOf("whatever"))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("rejected with %+v, want code %s — front-desk staff must be stopped by the capability gate, not by a script refusal",
			reply.Error, processor.ErrCodeAuthDenied)
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "a denied submit must change nothing")
	assertDocLive(t, ctx, conn, identityKey+".credentialBinding", "a denied submit must change nothing")

	// Positive control: the identical envelope from the operator is accepted,
	// so the denial was the grant and not the shape.
	testutil.PublishOp(t, conn, revokeEnv(testutil.GenReqID("RvkAuthAllow00"), staffActorKey, identityKey, sha256HexOf("whatever")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey), "the operator's revoke cuts the credential")
}

func TestRevokeIdentityClaim_TombstonedBinding_NothingToRevoke(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-nobinding")

	identityKey := "vtx.identity." + testutil.GenReqID("RvkNoBind")
	seedSpentClaimedIdentity(t, ctx, conn, identityKey, "claimed", "", consumerActorKey)
	tombstoneKV(t, ctx, conn, identityKey+".credentialBinding")

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkNoBindDo000"), staffActorKey, identityKey, sha256HexOf("whatever")), "nothing-to-revoke")

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed (untouched)", got)
	}
	assertTombstoned(t, ctx, conn, identityKey+".claimKey", "no replacement secret is armed on a refusal")
}

// TestRevokeIdentityClaim_UndeclaredSubmitter_Refused: an envelope declaring
// nothing at all. The guard's OCC rests on whoever writes its read
// declaration, so the property that has to hold is that an under-declaring
// submitter is REFUSED — never accepted with a partial view of the identity.
// With no reads hydrated the vertex reads as absent and the script names that
// outcome; nothing is written.
func TestRevokeIdentityClaim_UndeclaredSubmitter_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-undeclared")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkUndecl")

	env := revokeEnv(testutil.GenReqID("RvkUndeclDo000"), staffActorKey, identityKey, sha256HexOf("whatever"))
	env.ContextHint = nil
	assertRevokeRejected(t, ctx, conn, cp, cons, env, "no-target")

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed — an undeclared submit must change nothing", got)
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "an undeclared submit must change nothing")
	assertDocLive(t, ctx, conn, boundToLinkKey(consumerActorKey, identityKey), "an undeclared submit must change nothing")
}

// TestRevokeIdentityClaim_LinkPlaneMissing_PlaneInconsistent: the credentials
// array names a credential whose boundTo link is gone — the pre-link shape
// ReconcileCredentialBinding converges. This verb never guesses which plane is
// right, so it refuses whole.
func TestRevokeIdentityClaim_LinkPlaneMissing_PlaneInconsistent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-plane")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkPlane")
	dropBoundToLink(t, ctx, conn, consumerActorKey, identityKey)

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkPlaneDo0000"), staffActorKey, identityKey, sha256HexOf("whatever")), "plane-inconsistent")

	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "a refusal must leave the index standing for the reconcile")
	assertDocLive(t, ctx, conn, identityKey+".credentialBinding", "a refusal must leave the binding standing")
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed (untouched)", got)
	}
}

// TestRevokeIdentityClaim_ForeignIndex_OwnerMismatch: the index is the
// authority on which identity a credential resolves to, so a live index naming
// another owner is not this identity's binding to cut. UnlinkCredential
// tombstones the index blind; this verb does not.
func TestRevokeIdentityClaim_ForeignIndex_OwnerMismatch(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-owner")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkOwner")
	otherKey := "vtx.identity." + testutil.GenReqID("RvkOwnerOther")
	repointCredentialIndex(t, ctx, conn, consumerActorKey, otherKey)

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkOwnerDo0000"), staffActorKey, identityKey, sha256HexOf("whatever")), "owner-mismatch")

	idx := readAspectData(t, ctx, conn, credentialIndexKey(consumerActorKey))
	if got, _ := idx["identityKey"].(string); got != otherKey {
		t.Fatalf("credentialindex.identityKey = %q, want the foreign owner %q left untouched", got, otherKey)
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "another owner's index is never tombstoned by this verb")
	assertDocLive(t, ctx, conn, boundToLinkKey(consumerActorKey, identityKey), "a refusal leaves the link standing")
}

func TestRevokeIdentityClaim_Merged_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-merged")

	identityKey := "vtx.identity." + testutil.GenReqID("RvkMerged")
	seedSpentClaimedIdentity(t, ctx, conn, identityKey, "merged", "vtx.identity.SurvivorVtxNPQRSTUVW", consumerActorKey)

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkMergedDo000"), staffActorKey, identityKey, sha256HexOf("whatever")), "merged")

	assertDocLive(t, ctx, conn, identityKey+".credentialBinding", "a merged-away identity's binding is frozen")
	assertTombstoned(t, ctx, conn, identityKey+".claimKey", "no replacement secret is armed on a merged identity")
}

// TestRevokeIdentityClaim_Sealed_Erased: a subject sealed for erasure may
// acquire no fresh erasable representation, and an armed claim secret is one.
// The credential-plane sweep for an erased subject is
// UnbindIdentityCredentials' job.
func TestRevokeIdentityClaim_Sealed_Erased(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-erased")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkErased")
	sealForErasure(t, ctx, conn, identityKey)

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkErasedDo000"), staffActorKey, identityKey, sha256HexOf("whatever")), "erased")

	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "the erasure plane owns this subject's credential sweep")
	assertTombstoned(t, ctx, conn, identityKey+".claimKey", "no replacement secret is armed on a sealed subject")
}

// TestRevokeIdentityClaim_ConcurrentBindingWriteConflicts proves the pin the
// .credentialBinding tombstone carries. The whole judgement — which
// credentials to cut, whether the two planes agree — rests on the binding as
// hydration read it, and a CompleteCredentialLink landing between that read
// and the commit appends an entry the batch never saw. Unpinned, the tombstone
// would erase that credential with nothing left to notice; pinned, the batch
// conflicts and nothing is written.
//
// The competing write is a direct Core KV re-encrypt of the array rather than a
// real CompleteCredentialLink: what the CAS compares is a revision, so any
// writer of that key reproduces it, and driving a second op through a commit
// path already mid-commit is not something the harness can do.
func TestRevokeIdentityClaim_ConcurrentBindingWriteConflicts(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)

	// One pipeline drives the fixture ops and the raced one: the hook fires
	// only on the first RevokeIdentityClaim, and a second consumer on the
	// same lane would replay the fixture ops as step-2 duplicates first.
	var identityKey string
	raced := make(chan struct{})
	raceCP, raceCons := newRacingPipeline(t, ctx, conn, racingPipelineConfig{
		Durable:       "rvk-race",
		FilterSubject: "ops.default",
		OperationType: "RevokeIdentityClaim",
	}, func() {
		data := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
		creds, _ := data["credentials"].([]any)
		data["credentials"] = append(creds, map[string]any{
			"actorKey": secondCredActorKey,
			"boundAt":  "2026-09-12T09:59:00Z",
		})
		seedSensitiveAspect(t, ctx, conn, identityKey, "credentialBinding", data)
		close(raced)
	})
	identityKey = claimFreshIdentity(t, ctx, conn, raceCP, raceCons, "RvkRace")

	env := revokeEnv(testutil.GenReqID("RvkRaceDo00000"), staffActorKey, identityKey, sha256HexOf("whatever"))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, raceCP, raceCons, env)
	<-raced

	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a write landing on the binding between this revoke's read and "+
			"its commit must conflict the batch, not be tombstoned by a decision that never saw it", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("rejected with %+v, want %s — the binding tombstone must carry the revision it was read at",
			reply.Error, processor.ErrCodeRevisionConflict)
	}

	// A conflicted batch commits nothing: the racing writer's entry survives,
	// the rogue credential still resolves, and the identity is still claimed.
	after := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	creds, _ := after["credentials"].([]any)
	if len(creds) != 2 {
		t.Fatalf("credentials after the conflict = %+v, want both entries — the conflicted batch must have written nothing", creds)
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "a conflict must roll back the credentialindex tombstone")
	assertDocLive(t, ctx, conn, boundToLinkKey(consumerActorKey, identityKey), "a conflict must roll back the link tombstone")
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state after the conflict = %q, want claimed", got)
	}
}

// TestRevokeIdentityClaim_UnarmedLinkKeyIsTombstonedAnyway pins the
// unconditional .linkKey tombstone: an identity that never armed a link
// secret still gets the tombstone (a secret armed between the revoke's read
// and its commit would otherwise survive), the bodiless tombstone reads as no
// secret to CompleteCredentialLink rather than faulting at hydration, and a
// later InitiateCredentialLink writes over it.
func TestRevokeIdentityClaim_UnarmedLinkKeyIsTombstonedAnyway(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-linkkey")
	testutil.SeedCapDoc(t, ctx, conn, thirdCredCapDoc())
	testutil.SeedCredentialActor(t, ctx, conn, thirdCredActorKey, consumerRoleKey(t))

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkLinkKey")
	if kvExists(t, ctx, conn, identityKey+".linkKey") {
		t.Fatalf("fixture: a fresh claim must not have armed a link secret")
	}

	newPlaintext := "replacement-claim-secret-0002"
	testutil.PublishOp(t, conn, revokeEnv(testutil.GenReqID("RvkLinkKeyDo00"), staffActorKey, identityKey, sha256HexOf(newPlaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, identityKey+".linkKey", "the tombstone is written whether or not a secret was armed")

	// The real person claims; the identity is claimed again with the
	// bodiless tombstone still at .linkKey.
	seedIdentityCapDoc(t, ctx, conn, secondCredActorKey, "ClaimIdentity")
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("RvkLinkKeyClm0"), secondCredActorKey, identityKey, newPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// CompleteCredentialLink against the bodiless tombstone is a clean
	// refusal (no secret to match), not a hydration fault.
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		completeLinkEnv(testutil.GenReqID("RvkLinkKeyNoSc"), thirdCredActorKey, identityKey, "anything"))
	if outcome != processor.OutcomeRejected || reply.Error == nil || reply.Error.Code != "ClaimKeyInvalid" {
		t.Fatalf("CompleteCredentialLink over a bodiless .linkKey tombstone: outcome=%q err=%+v, want the generic ClaimKeyInvalid refusal", outcome, reply.Error)
	}
	if kvExists(t, ctx, conn, credentialIndexKey(thirdCredActorKey)) {
		t.Fatalf("a refused link must not index the credential")
	}

	// A fresh InitiateCredentialLink writes over the tombstone, and the link
	// completes.
	linkSecondCredential(t, ctx, conn, cp, cons, identityKey, thirdCredActorKey, "RvkLinkKeyRe", "link-secret-after-revoke")
	assertDocLive(t, ctx, conn, credentialIndexKey(thirdCredActorKey), "the re-armed secret binds the credential")
	assertTombstoned(t, ctx, conn, identityKey+".linkKey", "CompleteCredentialLink spends the re-armed secret")
}

// TestRevokeIdentityClaim_LiveClaimKey_ClaimKeyDrift: a claimed identity
// whose .claimKey is still LIVE is drift — the claim that set the state should
// have spent it — and is refused rather than guessed at.
func TestRevokeIdentityClaim_LiveClaimKey_ClaimKeyDrift(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-drift")

	identityKey := "vtx.identity." + testutil.GenReqID("RvkDrift")
	seedDirectIdentity(t, ctx, conn, identityKey, "claimed", "")
	seedClaimKeyAspect(t, ctx, conn, identityKey, sha256HexOf("never-spent"))
	seedSensitiveAspect(t, ctx, conn, identityKey, "credentialBinding", map[string]any{
		"actorKey": consumerActorKey, "boundAt": "2026-09-12T09:00:00Z",
		"credentials": []any{map[string]any{"actorKey": consumerActorKey, "boundAt": "2026-09-12T09:00:00Z"}},
	})

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkDriftDo0000"), staffActorKey, identityKey, sha256HexOf("whatever")), "claim-key-drift")

	claimKey := readDecryptedAspectData(t, ctx, conn, identityKey, "claimKey")
	if got, _ := claimKey["hash"].(string); got != sha256HexOf("never-spent") {
		t.Fatalf("claimKey.hash = %q, want the drifted secret left untouched", got)
	}
	assertDocLive(t, ctx, conn, identityKey+".credentialBinding", "a refusal leaves the binding standing")
}

// TestRevokeIdentityClaim_UnnamedLiveLink_PlaneInconsistent: the other
// direction of the exact-agreement rule — a live inbound boundTo link whose
// source the credentials array does not name.
func TestRevokeIdentityClaim_UnnamedLiveLink_PlaneInconsistent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-plane2")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkPlane2")
	// A second live link the array never recorded: the shape a boundTo
	// writer that skipped the array would leave.
	linkDoc := map[string]any{
		"class": "boundTo", "isDeleted": false,
		"sourceVertex": secondCredActorKey, "targetVertex": identityKey,
		"localName": "boundTo", "data": map[string]any{"boundAt": "2026-09-12T09:30:00Z"},
	}
	b, _ := json.Marshal(linkDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, boundToLinkKey(secondCredActorKey, identityKey), b); err != nil {
		t.Fatalf("seed unnamed boundTo link: %v", err)
	}

	assertRevokeRejected(t, ctx, conn, cp, cons,
		revokeEnv(testutil.GenReqID("RvkPlane2Do000"), staffActorKey, identityKey, sha256HexOf("whatever")), "plane-inconsistent")

	assertDocLive(t, ctx, conn, boundToLinkKey(secondCredActorKey, identityKey), "a refusal leaves the unnamed link standing")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey), "a refusal leaves the named credential's index standing")
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed (untouched)", got)
	}
}
