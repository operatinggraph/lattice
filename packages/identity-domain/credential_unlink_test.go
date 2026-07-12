// UnlinkCredential (Fire 4, multi-credential-identity-linking-design.md §8)
// integration tests for the identity-domain Capability Package.
//
// Coverage:
//  1. TestUnlinkCredential_Success
//  2. TestUnlinkCredential_LastCredential_Rejected
//  3. TestUnlinkCredential_NeverLinked_Rejected
//  4. TestUnlinkCredential_UnlinkThenRelink_RoundTrip
//  5. TestUnlinkCredential_PromotesSingularOnRemovalOfOriginalHolder
//  6. TestUnlinkCredential_MergedIdentity_Rejected
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func unlinkEnv(reqID, uKey, credentialActorKey string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "UnlinkCredential",
		Actor:         uKey,
		SubmittedAt:   "2026-07-12T10:00:00Z",
		Class:         "identity",
		Payload:       []byte(`{"credentialActorKey":"` + credentialActorKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: uKey},
		ContextHint: &processor.ContextHint{
			Reads:         []string{uKey, uKey + ".state"},
			OptionalReads: []string{uKey + ".credentialBinding"},
		},
	}
}

// linkSecondCredential runs InitiateCredentialLink (as U) + CompleteCredentialLink
// (as the given raw credential actor) end to end, binding a second credential
// to uKey. Mirrors claimFreshIdentity's role for the second-credential half
// of the flow.
func linkSecondCredential(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, uKey, credActorKey, label, plaintext string) {
	t.Helper()
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID(label+"Arm"), uKey, sha256HexOf(plaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := completeLinkEnv(testutil.GenReqID(label+"Cmpl"), credActorKey, uKey, plaintext)
	env.ContextHint.OptionalReads = append(env.ContextHint.OptionalReads, credentialIndexKey(credActorKey))
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

func TestUnlinkCredential_Success(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-succ")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkSucc")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkSuccLink", "link-secret-unlnk-succ")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	unlinkReqID := testutil.GenReqID("UnlnkSuccDone")
	testutil.PublishOp(t, conn, unlinkEnv(unlinkReqID, uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A2's credentialindex must be tombstoned.
	a2IndexKey := credentialIndexKey(secondCredActorKey)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, a2IndexKey)
	if err != nil {
		t.Fatalf("credentialindex(A2) KVGet: %v", err)
	}
	var idxDoc map[string]any
	if err := json.Unmarshal(entry.Value, &idxDoc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if isDeleted, _ := idxDoc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("credentialindex(A2) should be tombstoned after unlink")
	}

	// U.credentialBinding.credentials must contain only A1 (consumerActorKey).
	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 1 {
		t.Fatalf("credentials array len = %d, want 1 (only A1 remaining): %+v", len(creds), creds)
	}
	m, _ := creds[0].(map[string]interface{})
	if got, _ := m["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentials[0].actorKey = %q, want %q", got, consumerActorKey)
	}

	assertTrackerEvent(t, ctx, conn, unlinkReqID, "identity.unbound")
}

func TestUnlinkCredential_LastCredential_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-last")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkLast")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	// U has exactly one credential (A1 = consumerActorKey, from ClaimIdentity).
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("UnlnkLastDone"), uKey, consumerActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// A1's credentialindex must survive untouched.
	a1IndexKey := credentialIndexKey(consumerActorKey)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, a1IndexKey)
	if err != nil {
		t.Fatalf("credentialindex(A1) KVGet: %v", err)
	}
	var idxDoc map[string]any
	if err := json.Unmarshal(entry.Value, &idxDoc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if isDeleted, _ := idxDoc["isDeleted"].(bool); isDeleted {
		t.Fatalf("credentialindex(A1) should NOT be tombstoned — last-credential unlink must be rejected")
	}

	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 1 {
		t.Fatalf("credentials array len = %d, want 1 (untouched): %+v", len(creds), creds)
	}
}

func TestUnlinkCredential_NeverLinked_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-never")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkNever")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkNeverLink", "link-secret-unlnk-never")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	// thirdCredActorKey was never bound to U at all.
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("UnlnkNeverDone"), uKey, thirdCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// Both real credentials (A1, A2) must survive untouched.
	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 2 {
		t.Fatalf("credentials array len = %d, want 2 (untouched): %+v", len(creds), creds)
	}
}

// TestUnlinkCredential_UnlinkThenRelink_RoundTrip proves the design's §9
// Fire 4 round-trip requirement: after A2 is unlinked from U, A2 can
// complete a fresh link back to U. This pins the credential_index_mutation
// revive fix (§8 build-note) — without it, CompleteCredentialLink's blind
// "create" on A2's now-tombstoned credentialindex vertex would RevisionConflict
// forever, permanently locking A2 out of ever relinking anywhere.
func TestUnlinkCredential_UnlinkThenRelink_RoundTrip(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-relink")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkRelink")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkRelinkLink1", "link-secret-relink-1")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("UnlnkRelinkDone"), uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A2 relinks to U with a fresh secret.
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkRelinkLink2", "link-secret-relink-2")

	a2IndexKey := credentialIndexKey(secondCredActorKey)
	indexData := readAspectData(t, ctx, conn, a2IndexKey)
	if got, _ := indexData["identityKey"].(string); got != uKey {
		t.Fatalf("credentialindex(A2).identityKey = %q, want %q after relink", got, uKey)
	}

	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 2 {
		t.Fatalf("credentials array len = %d, want 2 (A1 + relinked A2): %+v", len(creds), creds)
	}
	seen := map[string]bool{}
	for _, c := range creds {
		m, _ := c.(map[string]interface{})
		actorKey, _ := m["actorKey"].(string)
		seen[actorKey] = true
	}
	if !seen[consumerActorKey] || !seen[secondCredActorKey] {
		t.Fatalf("credentials array = %+v, want both %q and %q", creds, consumerActorKey, secondCredActorKey)
	}
}

// TestUnlinkCredential_PromotesSingularOnRemovalOfOriginalHolder proves the
// singular actorKey/boundAt promotion branch (ddls.go's UnlinkCredential,
// the credentials[0] != removed case is already covered by
// TestUnlinkCredential_Success; this pins credentials[0] == removed): when
// the credential removed is the one currently holding the singular
// actorKey/boundAt fields (A1, the original ClaimIdentity holder) and >=2
// credentials remain, the singular fields must promote to the new first
// entry rather than go stale.
func TestUnlinkCredential_PromotesSingularOnRemovalOfOriginalHolder(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-promote")
	testutil.SeedCapDoc(t, ctx, conn, thirdCredCapDoc())

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkPromote")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkPromoteLinkA2", "link-secret-promote-a2")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, thirdCredActorKey, "UnlnkPromoteLinkA3", "link-secret-promote-a3")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	// A1 (consumerActorKey) is the original singular holder from ClaimIdentity.
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("UnlnkPromoteDone"), uKey, consumerActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != secondCredActorKey {
		t.Fatalf("credentialBinding.actorKey after removing the original singular holder = %q, want promoted to %q", got, secondCredActorKey)
	}
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 2 {
		t.Fatalf("credentials array len = %d, want 2 (A2 + A3): %+v", len(creds), creds)
	}
	seen := map[string]bool{}
	for _, c := range creds {
		m, _ := c.(map[string]interface{})
		actorKey, _ := m["actorKey"].(string)
		seen[actorKey] = true
	}
	if seen[consumerActorKey] {
		t.Fatalf("credentials array still contains removed A1: %+v", creds)
	}
	if !seen[secondCredActorKey] || !seen[thirdCredActorKey] {
		t.Fatalf("credentials array = %+v, want both %q and %q", creds, secondCredActorKey, thirdCredActorKey)
	}
}

// TestUnlinkCredential_MergedIdentity_Rejected proves the enforce_not_merged
// guard applies to UnlinkCredential exactly like every other self-scoped op
// in this package (InitiateCredentialLink, CompleteCredentialLink): a merged
// identity can no longer mutate its own credentialBinding.
func TestUnlinkCredential_MergedIdentity_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-merged")

	identityID := testutil.GenReqID("UnlnkMerged")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "merged", "vtx.identity.SurvivorVtxNPQRSTUVW")
	seedSensitiveAspect(t, ctx, conn, identityKey, "credentialBinding", map[string]any{
		"actorKey": consumerActorKey, "boundAt": "2026-05-22T10:01:00Z",
		"credentials": []any{map[string]any{"actorKey": consumerActorKey, "boundAt": "2026-05-22T10:01:00Z"}},
	})
	seedIdentityCapDoc(t, ctx, conn, identityKey, "UnlinkCredential")

	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("UnlnkMergedDone"), identityKey, consumerActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The credentialBinding aspect must survive untouched — a merged
	// identity's binding is frozen.
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentialBinding.actorKey = %q, want untouched %q — merged identity's UnlinkCredential must be rejected", got, consumerActorKey)
	}

	// The credentialindex must never have been created/tombstoned.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(consumerActorKey)); err == nil {
		t.Fatalf("credentialindex should not exist — merged identity's UnlinkCredential must be rejected before any mutation")
	}
}
