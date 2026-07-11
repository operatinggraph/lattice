// Multi-credential link flow (Fire 2,
// multi-credential-identity-linking-design.md §3.2) integration tests for
// the identity-domain Capability Package.
//
// Coverage:
//  1. TestInitiateCredentialLink_Success
//  2. TestInitiateCredentialLink_Unclaimed_Rejected
//  3. TestInitiateCredentialLink_Merged_Rejected
//  4. TestInitiateCredentialLink_ReArm_Overwrites
//  5. TestCompleteCredentialLink_Success
//  6. TestCompleteCredentialLink_WrongKey_Rejected
//  7. TestCompleteCredentialLink_SpentKey_Rejected
//  8. TestCompleteCredentialLink_AlreadyBoundCredential_Rejected
//  9. TestCompleteCredentialLink_UnclaimedTarget_Rejected
//  10. TestCompleteCredentialLink_ScenarioB_CreatesAspect
//  11. TestClaimIdentity_WritesCredentialsArray
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

// thirdCredActorKey is a third raw credential (A3), distinct from
// consumerActorKey (A1/U's claimer) and secondCredActorKey (A2) — needed by
// the spent-key test, which must attempt a link completion from a credential
// that was never the one bound moments earlier.
const (
	thirdCredActorID  = "JthrdCrdHJKMNPQRSTUV"
	thirdCredActorKey = "vtx.identity." + thirdCredActorID
	thirdCredCapKey   = "cap.identity." + thirdCredActorID
)

func thirdCredCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    thirdCredCapKey,
		Actor:                  thirdCredActorKey,
		Version:                "1.0",
		ProjectedFromRevisions: map[string]uint64{thirdCredActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{{OperationType: "CompleteCredentialLink", Scope: "self"}},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	}
}

func newLinkPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "icl-" + durable,
	})
}

// seedIdentityCapDoc grants opType (scope=self) to a dynamically-generated
// identity key (e.g. U, minted mid-test by a real claim/provision op) —
// production derives this from the Capability Lens projecting the
// identity's holdsRole grant; tests seed it directly, mirroring
// consumerCapDoc/secondCredCapDoc.
func seedIdentityCapDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, opType string) {
	t.Helper()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap." + identityKey[len("vtx."):],
		Actor:                  identityKey,
		Version:                "1.0",
		ProjectedFromRevisions: map[string]uint64{identityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{{OperationType: opType, Scope: "self"}},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  []string{"vtx.role.consumer"},
	})
}

// claimFreshIdentity runs CreateUnclaimedIdentity (as staff) + ClaimIdentity
// (as consumerActorKey/A1) end to end, returning the resulting claimed
// identity's key U. Mirrors createIdentityAndGetKeys + the claim half of
// TestClaimIdentity_Success.
func claimFreshIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label string) (identityKey string) {
	t.Helper()
	createReqID := testutil.GenReqID(label + "Cr")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	claimReqID := testutil.GenReqID(label + "Cl")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{identityKey, identityKey + ".state", identityKey + ".claimKey"},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return identityKey
}

func initiateLinkEnv(reqID, uKey, linkKeyHash string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "InitiateCredentialLink",
		Actor:         uKey,
		SubmittedAt:   "2026-07-11T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"linkKeyHash":"` + linkKeyHash + `"}`),
		AuthContext:   &processor.AuthContext{Target: uKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{uKey, uKey + ".state"},
		},
	}
}

func completeLinkEnv(reqID, a2Key, uKey, linkKeyPlaintext string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CompleteCredentialLink",
		Actor:         a2Key,
		SubmittedAt:   "2026-07-11T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetIdentityKey":"` + uKey + `","linkKey":"` + linkKeyPlaintext + `"}`),
		AuthContext:   &processor.AuthContext{Target: a2Key},
		ContextHint: &processor.ContextHint{
			Reads:         []string{uKey, uKey + ".state"},
			OptionalReads: []string{uKey + ".linkKey", uKey + ".credentialBinding", credentialIndexKey(a2Key)},
		},
	}
}

func TestInitiateCredentialLink_Success(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "init-succ")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "InitSucc")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	linkHash := sha256HexOf("link-secret-init-succ")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("InitSuccArm"), uKey, linkHash))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkData := readDecryptedAspectData(t, ctx, conn, uKey, "linkKey")
	if got, _ := linkData["hash"].(string); got != linkHash {
		t.Fatalf("linkKey.hash = %q, want %q", got, linkHash)
	}
	if got, _ := linkData["algo"].(string); got != "sha256" {
		t.Fatalf("linkKey.algo = %q, want sha256", got)
	}
}

func TestInitiateCredentialLink_Unclaimed_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "init-unclaimed")

	identityID := testutil.GenReqID("InitUnclaimed")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "unclaimed", "")
	seedIdentityCapDoc(t, ctx, conn, identityKey, "InitiateCredentialLink")

	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("InitUnclArm"), identityKey, sha256HexOf("whatever")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".linkKey"); err == nil {
		t.Fatalf("linkKey should NOT exist after rejection on an unclaimed identity")
	}
}

func TestInitiateCredentialLink_Merged_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "init-merged")

	identityID := testutil.GenReqID("InitMerged")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "merged", "vtx.identity.SurvivorVtxNPQRSTUVW")
	seedIdentityCapDoc(t, ctx, conn, identityKey, "InitiateCredentialLink")

	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("InitMrgArm"), identityKey, sha256HexOf("whatever")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestInitiateCredentialLink_ReArm_Overwrites(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "init-rearm")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "InitRearm")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	hash1 := sha256HexOf("link-secret-1")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("InitRearm1"), uKey, hash1))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	hash2 := sha256HexOf("link-secret-2")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("InitRearm2"), uKey, hash2))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkData := readDecryptedAspectData(t, ctx, conn, uKey, "linkKey")
	if got, _ := linkData["hash"].(string); got != hash2 {
		t.Fatalf("linkKey.hash = %q, want the re-armed %q (overwritten, not the first %q)", got, hash2, hash1)
	}
}

func TestCompleteCredentialLink_Success(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-succ")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplSucc")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	plaintext := "link-secret-cmpl-succ"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplSuccArm"), uKey, sha256HexOf(plaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	completeReqID := testutil.GenReqID("CmplSuccDone")
	testutil.PublishOp(t, conn, completeLinkEnv(completeReqID, secondCredActorKey, uKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	a2IndexKey := credentialIndexKey(secondCredActorKey)
	indexData := readAspectData(t, ctx, conn, a2IndexKey)
	if got, _ := indexData["identityKey"].(string); got != uKey {
		t.Fatalf("credentialindex(A2).identityKey = %q, want %q", got, uKey)
	}

	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 2 {
		t.Fatalf("credentials array len = %d, want 2 (A1 from ClaimIdentity + A2 from CompleteCredentialLink): %+v", len(creds), creds)
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

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, uKey+".linkKey")
	if err != nil {
		t.Fatalf("linkKey aspect missing: %v", err)
	}
	var linkKeyDoc map[string]any
	_ = json.Unmarshal(entry.Value, &linkKeyDoc)
	if isDeleted, _ := linkKeyDoc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("linkKey aspect should be tombstoned")
	}

	assertTrackerEvent(t, ctx, conn, completeReqID, "identity.claimed")
}

func TestCompleteCredentialLink_WrongKey_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-wrong")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplWrong")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplWrongArm"), uKey, sha256HexOf("the-real-secret")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplWrongDone"), secondCredActorKey, uKey, "a-guessed-wrong-secret"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey)); err == nil {
		t.Fatalf("credentialindex(A2) should NOT exist after a wrong-key rejection")
	}
}

func TestCompleteCredentialLink_SpentKey_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-spent")
	testutil.SeedCapDoc(t, ctx, conn, thirdCredCapDoc())

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplSpent")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	plaintext := "link-secret-spend-once"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplSpentArm"), uKey, sha256HexOf(plaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A2 spends the secret legitimately.
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplSpentA2"), secondCredActorKey, uKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A3 (never bound to anything) tries to replay the now-tombstoned secret.
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplSpentA3"), thirdCredActorKey, uKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(thirdCredActorKey)); err == nil {
		t.Fatalf("credentialindex(A3) should NOT exist after a spent-key rejection")
	}
}

func TestCompleteCredentialLink_AlreadyBoundCredential_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-bound")

	u1Key := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplBoundU1")
	seedIdentityCapDoc(t, ctx, conn, u1Key, "InitiateCredentialLink")
	plaintext1 := "link-secret-u1"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplBoundU1Arm"), u1Key, sha256HexOf(plaintext1)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplBoundU1Done"), secondCredActorKey, u1Key, plaintext1))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// U2: a second, unrelated claimed identity carrying its own armed link
	// secret. A2 (already bound to U1) must not be able to bind to it too.
	u2ID := testutil.GenReqID("CmplBoundU2")
	u2Key := "vtx.identity." + u2ID
	seedDirectIdentity(t, ctx, conn, u2Key, "claimed", "")
	plaintext2 := "link-secret-u2"
	seedSensitiveAspect(t, ctx, conn, u2Key, "linkKey", map[string]any{"hash": sha256HexOf(plaintext2), "algo": "sha256"})

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplBoundU2Done"), secondCredActorKey, u2Key, plaintext2))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, u2Key+".credentialBinding"); err == nil {
		t.Fatalf("U2.credentialBinding should NOT exist after an already-bound-credential rejection")
	}
}

func TestCompleteCredentialLink_UnclaimedTarget_Rejected(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-unclaimed")

	identityID := testutil.GenReqID("CmplUnclaimed")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "unclaimed", "")
	plaintext := "link-secret-unclaimed-target"
	seedSensitiveAspect(t, ctx, conn, identityKey, "linkKey", map[string]any{"hash": sha256HexOf(plaintext), "algo": "sha256"})

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplUnclDone"), secondCredActorKey, identityKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey)); err == nil {
		t.Fatalf("credentialindex(A2) should NOT exist after an unclaimed-target rejection")
	}
}

// TestCompleteCredentialLink_ScenarioB_CreatesAspect proves the
// design's §3.2 "complete-on-Scenario-B identity creates the aspect" case:
// ProvisionConsumerIdentity's bare, already-claimed identity has NO
// credentialBinding aspect (it is self-bound by construction), so
// CompleteCredentialLink's binding_absent branch must create one fresh
// rather than assume it always exists.
func TestCompleteCredentialLink_ScenarioB_CreatesAspect(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-scenb")

	scenarioBKey := "vtx.identity.JscnBActHJKMNPQRSTUV"
	roleKey := consumerRoleKey(t)
	provisionEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CmplScenBProv"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-11T09:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, scenarioBKey, roleKey),
	}
	testutil.PublishOp(t, conn, provisionEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, scenarioBKey+".credentialBinding"); err == nil {
		t.Fatalf("Scenario-B identity should have NO credentialBinding aspect before CompleteCredentialLink")
	}

	seedIdentityCapDoc(t, ctx, conn, scenarioBKey, "InitiateCredentialLink")
	plaintext := "link-secret-scenario-b"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplScenBArm"), scenarioBKey, sha256HexOf(plaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplScenBDone"), secondCredActorKey, scenarioBKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	bindData := readDecryptedAspectData(t, ctx, conn, scenarioBKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != secondCredActorKey {
		t.Fatalf("credentialBinding.actorKey = %q, want %q", got, secondCredActorKey)
	}
	creds, _ := bindData["credentials"].([]interface{})
	if len(creds) != 1 {
		t.Fatalf("credentials array len = %d, want 1: %+v", len(creds), creds)
	}
	m, _ := creds[0].(map[string]interface{})
	if got, _ := m["actorKey"].(string); got != secondCredActorKey {
		t.Fatalf("credentials[0].actorKey = %q, want %q", got, secondCredActorKey)
	}
}

// TestCompleteCredentialLink_GenericError_NoEnumeration proves NFR-S6 holds
// for CompleteCredentialLink the same way TestClaimIdentity_*_GenericError
// proves it for ClaimIdentity: the Processor's classifyScriptError/
// classifyStepError reclassification only special-cases a
// "ClaimKeyInvalid: <outcome>" fail() message, so CompleteCredentialLink's
// fail_link() deliberately reuses that exact prefix (rather than a distinct
// "LinkKeyInvalid" code, which would need its own frozen Contract #2 §2.6
// entry) — every failure reason surfaces only via the
// claim-attempts.<outcome> Health KV counter, never in the caller-visible
// reply. This pins the wiring: before this fix, a "LinkKeyInvalid: "-
// prefixed fail() message fell through classifyScriptError's generic branch
// unreclassified, so the outcome-specific detail (here "invalid-key") leaked
// into the caller-visible reply's error.details.message instead of being
// stripped and routed to Health KV.
func TestCompleteCredentialLink_GenericError_NoEnumeration(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	logger := testutil.TestLogger()
	instance := claimInstance + "-cmpl-generr"
	emitter := processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, instance, logger)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:      "cmpl-generr",
		Instance:     instance,
		ClaimEmitter: emitter,
	})

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplGenErr")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplGenErrArm"), uKey, sha256HexOf("the-real-secret")))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// A wrong secret against a real, armed target is the "invalid-key"
	// outcome — one of five distinct CompleteCredentialLink failure reasons
	// NFR-S6 requires be indistinguishable on the wire.
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("CmplGenErrDone"), secondCredActorKey, uKey, "a-guessed-wrong-secret"))
	outcome := testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want Rejected", outcome)
	}

	count, found := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key")
	if !found {
		t.Fatalf("claim-attempts.invalid-key not found — CompleteCredentialLink's " +
			"fail_link() message was not reclassified: the specific outcome never " +
			"reached Health KV, meaning it stayed in the caller-visible reply instead " +
			"(NFR-S6 violation)")
	}
	if count < 1 {
		t.Fatalf("claim-attempts.invalid-key count = %d, want >= 1", count)
	}
}

// TestClaimIdentity_WritesCredentialsArray pins §3.1: ClaimIdentity now
// writes the N-credential array (not just the singular actorKey/boundAt
// fields) so a subsequent CompleteCredentialLink has something to append to.
func TestClaimIdentity_WritesCredentialsArray(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "claim-array")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ClaimArray")

	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, ok := bindData["credentials"].([]interface{})
	if !ok || len(creds) != 1 {
		t.Fatalf("credentials array = %+v, want a 1-element array", bindData["credentials"])
	}
	m, _ := creds[0].(map[string]interface{})
	if got, _ := m["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentials[0].actorKey = %q, want %q", got, consumerActorKey)
	}
}
