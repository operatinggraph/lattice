// Two-Phase Identity Claim (FR2, FR5) integration tests for the
// identity-domain Capability Package.
//
// Tests chain a CreateUnclaimedIdentity op (arrange) with a
// ClaimIdentity op (act) so both ops are exercised together. All
// rejections collapse to ErrCodeClaimKeyInvalid per NFR-S6
// anti-enumeration; specific outcomes surface only via Health KV
// counters (`claim-attempts.<outcome>`).
//
// Coverage:
//  1. TestClaimIdentity_Success                            — full happy path
//  2. TestClaimIdentity_WrongKey_GenericError              — wrong plaintext
//  3. TestClaimIdentity_AlreadyClaimed_GenericError        — state=claimed
//  4. TestClaimIdentity_Flagged_GenericError               — state=flagged-for-review
//  5. TestClaimIdentity_Merged_GenericError                — state=merged
//  6. TestClaimIdentity_CredentialAlreadyBound_GenericError
//  7. TestClaimIdentity_FR5_GrandfatheredFlow              — historical import
//  8. TestClaimIdentity_FR5_ImmediateAccess                — second claim blocked
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const claimInstance = "icl-test"

// claimedConsumerGrantKey returns the lnk.identity.<id>.holdsRole.role.<id>
// key ClaimIdentity's R2 refinement grants the claimed identity
// (gateway-claim-flow-identity-provisioning-design.md §11.5).
func claimedConsumerGrantKey(t *testing.T, identityKey string) string {
	t.Helper()
	roleKey := consumerRoleKey(t)
	identityID := identityKey[len("vtx.identity."):]
	roleID := roleKey[len("vtx.role."):]
	return "lnk.identity." + identityID + ".holdsRole.role." + roleID
}

func newClaimPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testutil.TestLogger()
	emitter := processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, claimInstance+"-"+durable, logger)
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:      durable,
		Instance:     claimInstance + "-" + durable,
		ClaimEmitter: emitter,
	})
}

// createIdentityAndGetKeys runs CreateUnclaimedIdentity as staff and
// returns identityKey + plaintext claim key for use by a subsequent
// claim.
func createIdentityAndGetKeys(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID string) (identityKey, claimKey string) {
	t.Helper()
	identityID := identityIDFromRequestID(reqID)
	identityKey = "vtx.identity." + identityID

	// Option C: the client mints the claim secret and submits only its hash.
	claimKeyPlaintext := "claim-secret-for-" + reqID
	claimKeyHash := sha256HexOf(claimKeyPlaintext)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Test Identity","email":"test@claim.example","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("create: state = %q, want unclaimed", got)
	}
	return identityKey, claimKeyPlaintext
}

// readClaimHealthCounter reads a claim-attempts counter for the given outcome.
func readClaimHealthCounter(t *testing.T, ctx context.Context, conn *substrate.Conn, instance, outcome string) (count int64, found bool) {
	t.Helper()
	key := "health.processor." + instance + ".claim-attempts." + outcome
	entry, err := conn.KVGet(ctx, testutil.HarnessHealthBucket, key)
	if err != nil {
		return 0, false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return 0, false
	}
	c, _ := doc["count"].(float64)
	return int64(c), true
}

func TestClaimIdentity_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "succ")

	createReqID := testutil.GenReqID("ClmSuccCreate")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)

	claimReqID := testutil.GenReqID("ClmSuccClaim0")
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
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed", got)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentialBinding.actorKey = %q", got)
	}

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey")
	if err != nil {
		t.Fatalf("claimKey aspect missing: %v", err)
	}
	var claimKeyDoc map[string]any
	_ = json.Unmarshal(entry.Value, &claimKeyDoc)
	if isDeleted, _ := claimKeyDoc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("claimKey aspect should be tombstoned")
	}

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err != nil {
		t.Fatalf("credentialindex vertex not found at %s: %v", credIndexKey, err)
	}

	grantKey := claimedConsumerGrantKey(t, identityKey)
	grantEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, grantKey)
	if err != nil {
		t.Fatalf("R2: holdsRole grant not found at %s: %v", grantKey, err)
	}
	var grantDoc struct {
		SourceVertex string `json:"sourceVertex"`
		TargetVertex string `json:"targetVertex"`
	}
	if err := json.Unmarshal(grantEntry.Value, &grantDoc); err != nil {
		t.Fatalf("unmarshal holdsRole grant: %v", err)
	}
	if grantDoc.SourceVertex != identityKey || grantDoc.TargetVertex != consumerRoleKey(t) {
		t.Fatalf("R2: holdsRole grant source/target = %q/%q, want %q/%q",
			grantDoc.SourceVertex, grantDoc.TargetVertex, identityKey, consumerRoleKey(t))
	}

	assertTrackerEvent(t, ctx, conn, claimReqID, "identity.claimed")

	instance := claimInstance + "-succ"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "success")
	if !ok {
		t.Fatalf("claim-attempts.success not found for %s", instance)
	}
	if count < 1 {
		t.Fatalf("claim-attempts.success count = %d", count)
	}
}

func TestClaimIdentity_WrongKey_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "wrkey")

	createReqID := testutil.GenReqID("ClmWrKeyCreate")
	identityKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)

	claimReqID := testutil.GenReqID("ClmWrKeyClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"garbage-wrong-key-12345","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state mutated: %q, want unclaimed", got)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err == nil {
		t.Fatalf("credentialindex should NOT exist after wrong-key")
	}
	instance := claimInstance + "-wrkey"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key")
	if !ok {
		t.Fatalf("claim-attempts.invalid-key not found")
	}
	if count < 1 {
		t.Fatalf("invalid-key count = %d", count)
	}
}

func TestClaimIdentity_AlreadyClaimed_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "alrcl")

	identityID := testutil.GenReqID("PreClaimedIdnt")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "claimed", "")
	seedClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmAlrClClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-alrcl"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "wrong-state")
	if !ok {
		t.Fatalf("claim-attempts.wrong-state not found")
	}
	if count < 1 {
		t.Fatalf("wrong-state count = %d", count)
	}
}

func TestClaimIdentity_Flagged_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "flagged")

	identityID := testutil.GenReqID("FlaggedIdentit")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "flagged-for-review", "")
	seedClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmFlagClaim00")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-flagged"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "flagged")
	if !ok {
		t.Fatalf("claim-attempts.flagged not found")
	}
	if count < 1 {
		t.Fatalf("flagged count = %d", count)
	}
}

func TestClaimIdentity_Merged_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "merged")

	identityID := testutil.GenReqID("MergedIdentity")
	identityKey := "vtx.identity." + identityID
	mergedIntoKey := "vtx.identity." + testutil.GenReqID("MergedIntoIdnt")
	seedDirectIdentity(t, ctx, conn, identityKey, "merged", mergedIntoKey)
	seedClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmMergdClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
				identityKey + ".mergedInto",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	outcome := testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("NFR-S6: merged identity must surface as generic Rejected, got %q", outcome)
	}

	instance := claimInstance + "-merged"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "merged")
	if !ok {
		t.Fatalf("claim-attempts.merged not found")
	}
	if count < 1 {
		t.Fatalf("merged count = %d", count)
	}
	_ = time.Now
}

func TestClaimIdentity_CredentialAlreadyBound_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "credbnd")

	credIndexKey := credentialIndexKey(consumerActorKey)
	priorIdentityKey := "vtx.identity." + testutil.GenReqID("PriorBoundIdnt")
	credIdxDoc := map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data": map[string]any{
			"actorKey":    consumerActorKey,
			"identityKey": priorIdentityKey,
			"boundAt":     "2026-05-22T09:00:00Z",
		},
	}
	b, _ := json.Marshal(credIdxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, credIndexKey, b); err != nil {
		t.Fatalf("seed credentialindex: %v", err)
	}

	identityID := testutil.GenReqID("SecndIdentity0")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "unclaimed", "")
	claimKeyHash := sha256HexOf("the-real-key-12345678901234567890")
	seedClaimKeyAspect(t, ctx, conn, identityKey, claimKeyHash)

	claimReqID := testutil.GenReqID("ClmCredBndClm0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"the-real-key-12345678901234567890","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
				credIndexKey,
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-credbnd"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-already-bound")
	if !ok {
		t.Fatalf("claim-attempts.credential-already-bound not found")
	}
	if count < 1 {
		t.Fatalf("credential-already-bound count = %d", count)
	}
}

func TestClaimIdentity_FR5_GrandfatheredFlow(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "fr5gf")

	identityID := testutil.GenReqID("FR5GrandFathrd")
	identityKey := "vtx.identity." + identityID

	grandPlaintext := "grandfathered-claim-key-1234567"
	grandHash := sha256HexOf(grandPlaintext)

	// Historical-import shape: minimal vertex + state aspect + claimKey aspect.
	vtxDoc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"createdAt": "2024-01-01T00:00:00Z",
		"createdBy": "system.legacy-import",
		"data":      map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, vb); err != nil {
		t.Fatalf("seed grandfathered vertex: %v", err)
	}
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": "unclaimed"},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect: %v", err)
	}
	seedClaimKeyAspect(t, ctx, conn, identityKey, grandHash)

	credIndexKey := credentialIndexKey(consumerActorKey)
	claimReqID := testutil.GenReqID("ClmFR5GFClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + grandPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("FR5 GF: state = %q, want claimed", got)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("FR5 GF: credentialBinding.actorKey = %q", got)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err != nil {
		t.Fatalf("FR5 GF: credentialindex missing: %v", err)
	}

	// Confirm provenance: createdBy on the grandfathered vertex is the
	// legacy-import marker (proves the createdByOp on the .credentialBinding
	// + .state mutations is the new ClaimIdentity op's tracker — not the
	// grandfathered vertex's).
	vtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey)
	if err != nil {
		t.Fatalf("read identity vertex: %v", err)
	}
	var rawVtx map[string]any
	_ = json.Unmarshal(vtxEntry.Value, &rawVtx)
	if got, _ := rawVtx["createdBy"].(string); got != "system.legacy-import" {
		t.Fatalf("FR5: expected createdBy=system.legacy-import, got %q", got)
	}
}

func TestClaimIdentity_FR5_ImmediateAccess(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "fr5ia")

	createReqID := testutil.GenReqID("FR5IACreate00")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)
	claimReqID1 := testutil.GenReqID("FR5IAClaim0001")
	claimEnv1 := &processor.OperationEnvelope{
		RequestID:     claimReqID1,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("FR5 IA: first claim state = %q", got)
	}

	// Second claim against a different unclaimed identity by same consumer:
	// must fail with credential-already-bound.
	identity2ID := testutil.GenReqID("FR5IAIdent2000")
	identity2Key := "vtx.identity." + identity2ID
	secondPlaintext := "fr5-second-claim-key-12345678901"
	secondHash := sha256HexOf(secondPlaintext)
	seedDirectIdentity(t, ctx, conn, identity2Key, "unclaimed", "")
	seedClaimKeyAspect(t, ctx, conn, identity2Key, secondHash)

	claimReqID2 := testutil.GenReqID("FR5IAClaim0002")
	claimEnv2 := &processor.OperationEnvelope{
		RequestID:     claimReqID2,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:02:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + secondPlaintext + `","targetIdentityKey":"` + identity2Key + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identity2Key,
				identity2Key + ".state",
				identity2Key + ".claimKey",
				credIndexKey,
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-fr5ia"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-already-bound")
	if !ok {
		t.Fatalf("FR5 IA: credential-already-bound not found")
	}
	if count < 1 {
		t.Fatalf("FR5 IA: credential-already-bound count = %d", count)
	}
}
