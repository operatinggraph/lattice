// CompleteCredentialLink's spent-`.linkKey` wire shape — the twin of
// claim_test.go's TestClaimIdentity_ReClaimAfterRealClaim_GenericError.
//
// `.linkKey` is a sensitive aspect-type (ddls.go, aspectType linkKey) that a
// successful CompleteCredentialLink tombstones, and the tombstone RETAINS the
// prior document. Every live dispatcher — cmd/loftspace-app/credentials_link.go
// and cmd/facet/credentials.go — declares that key under `optionalReads`, which
// hydrates through the same decryptSensitiveDoc call the required-`reads` arm
// uses (internal/processor/step4_hydrate.go). A read path that refuses a
// tombstoned sensitive doc therefore fails the whole operation in step 4, and
// the caller sees the internal-error code the Gateway renders as HTTP 500
// (internal/gateway/gateway.go rejectedStatusCode) instead of the generic
// rejection the script itself owes — which makes "the link secret was already
// spent" distinguishable from every other refusal cause (NFR-S6).
//
// Coverage:
//  1. TestCompleteCredentialLink_ReCompleteAfterRealLink_GenericError
package identitydomain_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// dispatcherCompleteLinkEnv builds the CompleteCredentialLink envelope exactly
// as the live dispatchers do: the target vertex and its state under `reads`,
// and `.linkKey` + `.credentialBinding` under `optionalReads`. The
// credentialindex dedup probe is absent on purpose — it is a class-(g) key
// identity-domain's own derive_reads computes from the actor.
//
// The `optionalReads` placement of `.linkKey` is the point: it is the arm that
// differs from the claim ceremony's required-`reads` declaration, and it is
// the arm production actually takes.
func dispatcherCompleteLinkEnv(reqID, actorKey, uKey, linkKeyPlaintext string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CompleteCredentialLink",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-11T10:02:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetIdentityKey":"` + uKey + `","linkKey":"` + linkKeyPlaintext + `"}`),
		AuthContext:   &processor.AuthContext{Target: actorKey},
		ContextHint: &processor.ContextHint{
			Reads:         []string{uKey, uKey + ".state"},
			OptionalReads: []string{uKey + ".linkKey", uKey + ".credentialBinding"},
		},
	}
}

// TestCompleteCredentialLink_ReCompleteAfterRealLink_GenericError drives the
// whole ceremony — claim, arm, complete, re-complete — because the tombstone
// the second completion trips over is written by the first completion itself,
// not by a fixture.
//
// The replay comes from A3, a credential bound to nothing: A2's own retry
// would stop at the credential-already-bound guard, which sits ABOVE the
// linkKey read in the script and so never exercises the spent-secret branch.
func TestCompleteCredentialLink_ReCompleteAfterRealLink_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	instance := claimInstance + "-cmpl-respend"
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:      "cmpl-respend",
		Instance:     instance,
		ClaimEmitter: processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, instance, testutil.TestLogger()),
	})
	testutil.SeedCapDoc(t, ctx, conn, thirdCredCapDoc())
	testutil.SeedCredentialActor(t, ctx, conn, thirdCredActorKey, consumerRoleKey(t))

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplRespend")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	plaintext := "link-secret-respend-once"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplRespendArm"), uKey, sha256HexOf(plaintext)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, dispatcherCompleteLinkEnv(
		testutil.GenReqID("CmplRespendA2"), secondCredActorKey, uKey, plaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Assert the hazard rather than assume it: the tombstone must still carry
	// the encrypted body at the very key the replay is about to declare. If
	// CompleteCredentialLink ever hard-deleted `.linkKey`, the replay below
	// would prove nothing about the tombstoned-sensitive-read path.
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, uKey+".linkKey")
	if err != nil {
		t.Fatalf("read %s.linkKey after the first completion: %v", uKey, err)
	}
	var spent map[string]any
	if err := json.Unmarshal(entry.Value, &spent); err != nil {
		t.Fatalf("unmarshal spent linkKey: %v", err)
	}
	if deleted, _ := spent["isDeleted"].(bool); !deleted {
		t.Fatalf("linkKey isDeleted = false after a successful completion; doc = %+v", spent)
	}
	if body, _ := spent["data"].(map[string]any); len(body) == 0 {
		t.Fatalf("linkKey tombstone carries no body; the retained-document hazard this test rides is gone: %+v", spent)
	}

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		dispatcherCompleteLinkEnv(testutil.GenReqID("CmplRespendA3"), thirdCredActorKey, uKey, plaintext))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("replayed completion outcome = %q, want rejected", outcome)
	}
	if reply != nil && reply.Error != nil && reply.Error.Code == processor.ErrCodeInternalError {
		t.Fatalf("replayed completion answered %q (%q) — a spent link secret must not fault the read path; "+
			"the Gateway renders that code as HTTP 500, which distinguishes an already-spent secret "+
			"from every other refusal cause", reply.Error.Code, reply.Error.Message)
	}
	assertGenericClaimRejection(t, reply)

	// The counter is what proves the script actually RAN and took its own
	// invalid-key branch. Without it a step-4 hydrate fault satisfies
	// "rejected" just as well, and the wire-shape assertions above would be
	// the only thing standing between this test and a vacuous pass.
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key"); !ok || count < 1 {
		t.Fatalf("claim-attempts.invalid-key = %d (found=%v) — the replay never reached the script's own gate", count, ok)
	}
}
