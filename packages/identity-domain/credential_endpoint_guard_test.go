// The endpoint guard ClaimIdentity and CompleteCredentialLink apply to the
// credential they are submitted as. Contract #3 §3.5 leaves endpoint
// resolution to the script for a link the Processor will not resolve, and
// requires a script whose link "must guarantee" its endpoints to declare and
// validate them. These two must: the boundTo they emit is the sole input to
// identityCredentialBindingsRead, which anchors on the credential vertex and
// is presented as a person's COMPLETE list of sign-in methods — so a claim by
// an actor with no vertex commits a binding that projects no row at all, while
// the sibling credentials lens still lists the credential.
//
// Every test here removes the actor vertex setupTestEnv seeds, rather than
// inventing a fresh unprovisioned key: the point is that the SAME actor that
// claims successfully everywhere else in this package is refused without its
// vertex, so a passing suite cannot be hiding a guard that never fires.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// dropCredentialActor removes the credential actor's identity vertex, putting
// the actor in the state the Gateway's first-touch provisioning would have
// prevented — known to the auth plane (its cap doc stands), unknown to the
// graph.
func dropCredentialActor(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) {
	t.Helper()
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, actorKey); err != nil {
		t.Fatalf("delete credential actor %s: %v", actorKey, err)
	}
}

// tombstoneCredentialActor marks the credential actor's vertex deleted — a
// revoked credential. ProvisionConsumerIdentity deliberately no-ops on a
// tombstoned actor ("tombstoned stays tombstoned"), so this refusal is
// permanent, not a transient wiring gap.
func tombstoneCredentialActor(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) {
	t.Helper()
	doc := map[string]any{
		"key": actorKey, "class": "identity", "isDeleted": true,
		"data": map[string]any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned actor: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, actorKey, b); err != nil {
		t.Fatalf("tombstone credential actor %s: %v", actorKey, err)
	}
}

// assertNoBindingWritten pins what the guard is FOR: neither half of the
// credential→person fact may exist after a refused claim. Asserting only the
// outcome would stay green if the script rejected after emitting mutations.
func assertNoBindingWritten(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, identityKey string) {
	t.Helper()
	assertKeyAbsent(t, ctx, conn, credentialIndexKey(actorKey),
		"an unprovisioned credential got an index vertex")
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(actorKey, identityKey),
		"an unprovisioned credential became the source of a boundTo edge")
}

func TestClaimIdentity_UnprovisionedCredential_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "unprov-claim")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("UnprovClaim"))
	dropCredentialActor(t, ctx, conn, consumerActorKey)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("UnprovClaimGo"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-unprov-claim"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-not-provisioned"); !ok || count < 1 {
		t.Fatalf("claim-attempts.credential-not-provisioned = (%d, found=%v), want >=1 — the refusal must name the endpoint, not collapse into invalid-key",
			count, ok)
	}
	assertNoBindingWritten(t, ctx, conn, consumerActorKey, uKey)

	// The same claim, same secret, once the credential exists: this is what
	// falsifies the test. Without it a guard that rejected EVERY claim would
	// pass every assertion above.
	testutil.SeedCredentialActor(t, ctx, conn, consumerActorKey, consumerRoleKey(t))
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("UnprovClaimOk"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

func TestClaimIdentity_TombstonedCredential_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "revoked-claim")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("RevokedClaim"))
	tombstoneCredentialActor(t, ctx, conn, consumerActorKey)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("RevokedClaimGo"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-revoked-claim"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-not-provisioned"); !ok || count < 1 {
		t.Fatalf("claim-attempts.credential-not-provisioned = (%d, found=%v), want >=1 — a revoked credential must take the endpoint refusal, not a secret one",
			count, ok)
	}
	assertNoBindingWritten(t, ctx, conn, consumerActorKey, uKey)
}

// TestClaimIdentity_UnprovisionedCredential_RefusedBeforeTheSecretIsSpent pins
// that a refused claim does not consume the target's claim secret: the guard
// must leave the identity claimable by the same secret once the credential is
// provisioned, or a transient provisioning gap would burn the person's only
// way in.
func TestClaimIdentity_UnprovisionedCredential_RefusedBeforeTheSecretIsSpent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "unprov-secret")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("UnprovSecret"))
	dropCredentialActor(t, ctx, conn, consumerActorKey)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("UnprovSecretNo"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, uKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("target state = %q, want unclaimed — a refused claim must not advance the identity", got)
	}

	testutil.SeedCredentialActor(t, ctx, conn, consumerActorKey, consumerRoleKey(t))
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("UnprovSecretYes"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

func TestCompleteCredentialLink_UnprovisionedCredential_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unprov-link")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnprovLink")
	dropCredentialActor(t, ctx, conn, secondCredActorKey)

	linkSecret := "link-secret-unprovisioned"
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("UnprovLinkArm"), uKey, sha256HexOf(linkSecret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("UnprovLinkGo"), secondCredActorKey, uKey, linkSecret))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	assertNoBindingWritten(t, ctx, conn, secondCredActorKey, uKey)

	// The secret survives a refusal (it is tombstoned on success only), so the
	// same one proves the guard is what refused — not a spent or wrong secret.
	testutil.SeedCredentialActor(t, ctx, conn, secondCredActorKey, consumerRoleKey(t))
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("UnprovLinkOk"), secondCredActorKey, uKey, linkSecret))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, secondCredActorKey, uKey)
}

func TestCompleteCredentialLink_TombstonedCredential_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	// The CLAIM pipeline, not the link one: only it carries a ClaimAttemptEmitter,
	// and the outcome word is the whole point of this test — every rejection on
	// this path collapses to the same generic wire code, so Health KV is the
	// only place the reason is legible.
	cp, cons := newClaimPipeline(t, ctx, conn, "revoked-link")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RevokedLink")
	tombstoneCredentialActor(t, ctx, conn, secondCredActorKey)

	linkSecret := "link-secret-revoked"
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("RevokedLinkArm"), uKey, sha256HexOf(linkSecret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("RevokedLinkGo"), secondCredActorKey, uKey, linkSecret))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	assertNoBindingWritten(t, ctx, conn, secondCredActorKey, uKey)

	// Named, not merely refused: without this the test cannot tell the endpoint
	// guard from a spent secret or a wrong state, all of which look identical
	// on the wire.
	instance := claimInstance + "-revoked-link"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-not-provisioned"); !ok || count < 1 {
		t.Fatalf("claim-attempts.credential-not-provisioned = (%d, found=%v), want >=1", count, ok)
	}
}

// TestDeriveReads_ClaimOpsDeclareTheActorVertex proves the guard is fed by the
// package's own derivation and not by a declaration the submitter remembered:
// the envelope below declares only the target's three keys, exactly as the
// erasure-gate envelopes do, and the guard still sees the actor vertex.
//
// Without the derivation the actor key hydrates as absent and EVERY claim —
// provisioned or not — takes the refusal, which is the failure this asserts
// against: the accepted claim here is only reachable if derive_reads named it.
func TestDeriveReads_ClaimOpsDeclareTheActorVertex(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "derive-actor")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("DeriveActor"))

	env := erasureClaimEnv(testutil.GenReqID("DeriveActorGo"), consumerActorKey, uKey, claimKeyPlaintext)
	for _, k := range env.ContextHint.Reads {
		if k == consumerActorKey {
			t.Fatal("the fixture declares the actor vertex by hand; this test would stay green with derive_reads deleted")
		}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestReconcileCredentialBinding_UnprovisionedCredential_Rejected covers the
// third writer of the boundTo edge, and the one with the strongest reason to
// need the guard: what this op exists to converge is the pre-link corpus —
// bindings made before the boundTo type existed — which is exactly the
// population whose credential vertex may never have been minted. Its authority
// is the credentialindex, which carries the association in its own body and can
// therefore name a credential that has no vertex at all, so without the guard
// the one operator-run repair path would manufacture precisely the dangling
// edges the bind paths refuse.
func TestReconcileCredentialBinding_UnprovisionedCredential_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unprov-recon")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnprovRecon")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	dropCredentialActor(t, ctx, conn, consumerActorKey)

	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("UnprovReconNo"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(consumerActorKey, uKey),
		"the repair path published an edge whose source vertex does not exist")

	// Provisioned, the same repair succeeds — so the refusal above is the
	// endpoint guard and not this op declining every input.
	testutil.SeedCredentialActor(t, ctx, conn, consumerActorKey, consumerRoleKey(t))
	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("UnprovReconOk"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}
