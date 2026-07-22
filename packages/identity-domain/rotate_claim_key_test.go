// RotateClaimKey (R4, gateway-claim-flow-identity-provisioning-design.md
// §11.5) integration tests for the identity-domain Capability Package.
//
// Coverage:
//  1. TestRotateClaimKey_Success              — old secret invalid, new one claims
//  2. TestRotateClaimKey_RejectsClaimed        — state=claimed fails closed
//  3. TestRotateClaimKey_RejectsMalformedHash  — non-hex/short hash rejected
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func newRotatePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "irk-" + durable,
	})
}

// TestRotateClaimKey_Success: staff creates an identity, "loses" the secret,
// rotates the claim key, and confirms the OLD plaintext no longer claims
// while the NEW one does.
func TestRotateClaimKey_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRotatePipeline(t, ctx, conn, "succ")

	createReqID := testutil.GenReqID("RCKSuccCreate0")
	identityKey, oldPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	newPlaintext := "re-issued-claim-secret-0001"
	newHash := sha256HexOf(newPlaintext)
	rotateReqID := testutil.GenReqID("RCKSuccRotate0")
	rotateEnv := &processor.OperationEnvelope{
		RequestID:     rotateReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RotateClaimKey",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:30Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","claimKeyHash":"` + newHash + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, rotateEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The old plaintext must no longer work.
	oldClaimReqID := testutil.GenReqID("RCKSuccOldClm0")
	oldClaimEnv := &processor.OperationEnvelope{
		RequestID:     oldClaimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + oldPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, oldClaimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("old claim mutated state: %q, want unclaimed", got)
	}

	// The new plaintext claims successfully.
	newClaimReqID := testutil.GenReqID("RCKSuccNewClm0")
	newClaimEnv := &processor.OperationEnvelope{
		RequestID:     newClaimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:02:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + newPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, newClaimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect = readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state after rotated-key claim = %q, want claimed", got)
	}
}

// TestRotateClaimKey_RejectsClaimed: a claimed identity's secret can't be
// rotated — it has no secret left to rotate (the aspect is tombstoned).
func TestRotateClaimKey_RejectsClaimed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRotatePipeline(t, ctx, conn, "alrcl")

	identityID := testutil.GenReqID("RCKAlrClIdent0")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "claimed", "")

	rotateReqID := testutil.GenReqID("RCKAlrClRotat0")
	rotateEnv := &processor.OperationEnvelope{
		RequestID:     rotateReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RotateClaimKey",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:30Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","claimKeyHash":"` + sha256HexOf("whatever") + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
			},
			OptionalReads: []string{
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, rotateEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRotateClaimKey_RejectsMalformedHash: a non-hex/short hash is rejected
// before any mutation, mirroring CreateUnclaimedIdentity's own validation.
func TestRotateClaimKey_RejectsMalformedHash(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRotatePipeline(t, ctx, conn, "badhash")

	createReqID := testutil.GenReqID("RCKBadHCreate0")
	identityKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	rotateReqID := testutil.GenReqID("RCKBadHRotate0")
	rotateEnv := &processor.OperationEnvelope{
		RequestID:     rotateReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RotateClaimKey",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:30Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","claimKeyHash":"not-hex"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, rotateEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("malformed rotate mutated state: %q, want unclaimed", got)
	}
}
