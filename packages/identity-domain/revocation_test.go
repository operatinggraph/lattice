// RevokeActor / UnrevokeActor integration tests for the identity-domain
// Capability Package (gateway-token-revocation-activation-design.md Fire 1).
//
// These prove the SHIPPED package through the real pipeline:
//   - both ops commit a tracker-only atomic batch (no business mutation) and
//     emit their gateway.actorRevoked / gateway.actorUnrevoked event with the
//     expected {actor,at,by,reason} payload — the shape the Gateway's own
//     materializer (internal/gateway.StartRevocationMaterializer) folds;
//   - a non-operator actor is denied (a platform kill-switch is not self-scoped);
//   - a missing/malformed actor is rejected before any event is built.
package identitydomain_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func newRevocationPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "rev-" + durable,
	})
}

// assertOutboxEvent reads the durable outbox aspect for reqID and returns the
// payload of the event with class wantClass, failing the test if absent.
func assertOutboxEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, wantClass string) map[string]interface{} {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		t.Fatalf("outbox aspect missing for %s: %v", reqID, err)
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	for _, ev := range aspect.Data.Events {
		if ev.EventType == wantClass {
			return ev.Payload
		}
	}
	t.Fatalf("no %s event in outbox aspect for %s", wantClass, reqID)
	return nil
}

func TestRevokeActor_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-success")

	reqID := testutil.GenReqID("RevokeSuccess")
	targetActor := "vtx.identity.CompromisedActorNPQR"
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"` + targetActor + `","reason":"reported-compromised"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Event-only: the actor's own vertex is untouched (RevokeActor writes no
	// Core-KV mutation at all — the operational kill-switch lives in the
	// Gateway's own materialized bucket, not the graph).
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, targetActor); err == nil {
		t.Fatalf("RevokeActor must not create/touch the actor vertex, but %s exists", targetActor)
	}

	payload := assertOutboxEvent(t, ctx, conn, reqID, "gateway.actorRevoked")
	if got, _ := payload["actor"].(string); got != targetActor {
		t.Fatalf("actorRevoked payload actor = %q, want %q", got, targetActor)
	}
	if got, _ := payload["by"].(string); got != staffActorKey {
		t.Fatalf("actorRevoked payload by = %q, want %q", got, staffActorKey)
	}
	if got, _ := payload["reason"].(string); got != "reported-compromised" {
		t.Fatalf("actorRevoked payload reason = %q, want %q", got, "reported-compromised")
	}
	if got, _ := payload["at"].(string); got == "" {
		t.Fatalf("actorRevoked payload at is empty")
	}
}

func TestRevokeActor_ReasonOptional(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-noreason")

	reqID := testutil.GenReqID("RevokeNoReason")
	targetActor := "vtx.identity.NoReasonActorNPQRSTU"
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"` + targetActor + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	payload := assertOutboxEvent(t, ctx, conn, reqID, "gateway.actorRevoked")
	if got, _ := payload["reason"].(string); got != "" {
		t.Fatalf("actorRevoked payload reason = %q, want empty", got)
	}
}

func TestUnrevokeActor_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-unrevoke-success")

	reqID := testutil.GenReqID("UnrevokeSuccess")
	targetActor := "vtx.identity.ReinstatedActorNPQRS"
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "UnrevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T11:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"` + targetActor + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	payload := assertOutboxEvent(t, ctx, conn, reqID, "gateway.actorUnrevoked")
	if got, _ := payload["actor"].(string); got != targetActor {
		t.Fatalf("actorUnrevoked payload actor = %q, want %q", got, targetActor)
	}
	if got, _ := payload["by"].(string); got != staffActorKey {
		t.Fatalf("actorUnrevoked payload by = %q, want %q", got, staffActorKey)
	}
}

// TestRevokeActor_NonOperatorDenied: a platform kill-switch is not
// self-scoped — the consumer fixture (ClaimIdentity scope=self only) has no
// RevokeActor grant and is denied at step 3.
func TestRevokeActor_NonOperatorDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-denied")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RevokeDenied"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"vtx.identity.SomeOtherActorNPQRS"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRevokeActor_MissingActorRejected: the Starlark validates actor is
// present and identity-shaped before building any event.
func TestRevokeActor_MissingActorRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-missing")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RevokeMissing"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"reason":"no actor supplied"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRevokeActor_NonIdentityActorRejected: actor must be a
// vtx.identity.<NanoID> key — the only shape the Gateway's read-path checker
// and the identity-domain's own actor space use.
func TestRevokeActor_NonIdentityActorRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-badshape")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RevokeBadShape"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"vtx.role.notAnIdentity"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRevokeActor_BadCharsetActorRejected: a correctly-prefixed actor whose id
// segment is outside the NanoID charset (or wrong length) must be rejected at
// the script — the gateway revocation materializer poison-pill fix: an actor
// id this loose would commit + publish, then permanently fail the
// materializer's KVPut, so the script is the fail-closed gate that keeps a
// poison key from ever reaching the outbox.
func TestRevokeActor_BadCharsetActorRejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newRevocationPipeline(t, ctx, conn, "rev-revoke-badcharset")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RevokeBadCharset"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeActor",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-03T10:00:00Z",
		Class:         "actorRevocation",
		Payload:       []byte(`{"actor":"vtx.identity.not_a_real_NanoID!!"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
