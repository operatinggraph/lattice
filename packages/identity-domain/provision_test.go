// ProvisionConsumerIdentity (real-actor-write-auth-e2e-design.md Phase 1)
// integration tests for the identity-domain Capability Package.
//
// Pipeline: real Capability authorizer, real DDL cache, real Hydrator +
// Executor + Committer — identical to create_test.go's harness.
//
// Coverage:
//  1. TestProvisionConsumerIdentity_FreshActor_Success — creates the
//     identity + .state=claimed + holdsRole->consumer link
//  2. TestProvisionConsumerIdentity_AlreadyProvisioned_Idempotent — second
//     call for the same actor is a no-op, same response
//  3. TestProvisionConsumerIdentity_MalformedTargetActorKey_Rejected
//  4. TestProvisionConsumerIdentity_UnknownConsumerRoleKey_Rejected
//  5. TestProvisionConsumerIdentity_NonGatewayActor_Denied — the fail-closed
//     window before the Gateway's identityProvisioner grant is wired
//  6. TestProvisionConsumerIdentity_OtherLiveRoleKey_Rejected — a live but
//     WRONG role (operator) must never be grantable through this op
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const freshActorKey = "vtx.identity.JfrshActHJKMNPQRSTUV"

func newProvisionPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ipc-" + durable,
	})
}

func consumerRoleKey(t *testing.T) string {
	t.Helper()
	return "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")
}

func provisionPayload(t *testing.T, targetActorKey, roleKey string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"targetActorKey": targetActorKey, "consumerRoleKey": roleKey})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestProvisionConsumerIdentity_FreshActor_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-success")
	roleKey := consumerRoleKey(t)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCISuccess"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, roleKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, freshActorKey); err != nil {
		t.Fatalf("identity vertex not found: %v", err)
	}
	stateAspect := readAspectData(t, ctx, conn, freshActorKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed", got)
	}

	roleID := roleKey[len("vtx.role."):]
	linkKey := "lnk.identity.JfrshActHJKMNPQRSTUV.holdsRole.role." + roleID
	linkEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("holdsRole link not found: %v", err)
	}
	var linkDoc struct {
		SourceVertex string `json:"sourceVertex"`
		TargetVertex string `json:"targetVertex"`
	}
	if err := json.Unmarshal(linkEntry.Value, &linkDoc); err != nil {
		t.Fatalf("unmarshal link: %v", err)
	}
	if linkDoc.SourceVertex != freshActorKey || linkDoc.TargetVertex != roleKey {
		t.Fatalf("link source/target = %q/%q, want %q/%q", linkDoc.SourceVertex, linkDoc.TargetVertex, freshActorKey, roleKey)
	}

	assertTrackerEvent(t, ctx, conn, env.RequestID, "identity.provisioned")
}

func TestProvisionConsumerIdentity_AlreadyProvisioned_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-idem")
	roleKey := consumerRoleKey(t)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIIdem1"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, roleKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIIdem2"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:05:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, roleKey),
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	roleID := roleKey[len("vtx.role."):]
	linkKey := "lnk.identity.JfrshActHJKMNPQRSTUV.holdsRole.role." + roleID
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey); err != nil {
		t.Fatalf("holdsRole link missing after re-provision: %v", err)
	}
}

func TestProvisionConsumerIdentity_MalformedTargetActorKey_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-malformed")
	roleKey := consumerRoleKey(t)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIMalformed"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, "vtx.identity.not-a-nanoid", roleKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestProvisionConsumerIdentity_UnknownConsumerRoleKey_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-badrole")

	bogusRoleKey := "vtx.role.NoSuchRoleHJKMNPQRSTUV"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIBadRole"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, bogusRoleKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestProvisionConsumerIdentity_NonGatewayActor_Denied proves the fail-closed
// window described in gateway-claim-flow-identity-provisioning-design.md
// §3.3/§7: an actor without the identityProvisioner (or operator) grant is
// denied at step 3 — mirrored here by the consumer fixture, which holds no
// such grant.
func TestProvisionConsumerIdentity_NonGatewayActor_Denied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-nongateway")
	roleKey := consumerRoleKey(t)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCINonGateway"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, roleKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestProvisionConsumerIdentity_WithIdpProvenance_WritesIdpBindingAspect
// proves the Increment 2 delta (external-actor-authn-binding-design.md §3.3
// / §9): when the Gateway's pre-flight passes idpIssuer/idpSubject (an
// opaque-mode token's raw provenance), the script writes a sensitive
// .idpBinding aspect verbatim alongside the usual fresh-actor mutations.
func TestProvisionConsumerIdentity_WithIdpProvenance_WritesIdpBindingAspect(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-idp")
	roleKey := consumerRoleKey(t)

	const iss = "https://accounts.google.com"
	const sub = "110169484474386276334"
	payload, err := json.Marshal(map[string]string{
		"targetActorKey": freshActorKey, "consumerRoleKey": roleKey,
		"idpIssuer": iss, "idpSubject": sub,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIIdp"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-10T10:00:00Z",
		Class:         "identity",
		Payload:       payload,
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	idpBinding := readDecryptedAspectData(t, ctx, conn, freshActorKey, "idpBinding")
	if got, _ := idpBinding["iss"].(string); got != iss {
		t.Fatalf("idpBinding.iss = %q, want %q", got, iss)
	}
	if got, _ := idpBinding["sub"].(string); got != sub {
		t.Fatalf("idpBinding.sub = %q, want %q", got, sub)
	}
}

// TestProvisionConsumerIdentity_MismatchedIdpFields_Rejected proves the pair
// travels together — idpIssuer without idpSubject (or vice versa) is a
// wiring fault, not a partial-provenance case, and must be rejected before
// any mutation lands.
func TestProvisionConsumerIdentity_MismatchedIdpFields_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-idp-mismatch")
	roleKey := consumerRoleKey(t)

	payload, err := json.Marshal(map[string]string{
		"targetActorKey": freshActorKey, "consumerRoleKey": roleKey,
		"idpIssuer": "https://accounts.google.com", // idpSubject deliberately omitted
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIIdpMismatch"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-10T10:00:00Z",
		Class:         "identity",
		Payload:       payload,
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, freshActorKey); err == nil {
		t.Fatalf("identity vertex must NOT be created when idpIssuer/idpSubject are mismatched")
	}
}

// TestProvisionConsumerIdentity_OtherLiveRoleKey_Rejected is the security
// regression for the pinned-role fix: a live, real role that is NOT
// consumer (here, operator) must be rejected — not silently granted — even
// though it passes a bare existence/liveness check. Without pinning
// consumerRoleKey to the package's own consumer role, this op could be used
// to provision a first-touch actor straight into operator (root-equivalent).
func TestProvisionConsumerIdentity_OtherLiveRoleKey_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-wrongrole")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("PCIWrongRole"),
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   "2026-07-06T10:00:00Z",
		Class:         "identity",
		Payload:       provisionPayload(t, freshActorKey, bootstrap.RoleOperatorKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, freshActorKey); err == nil {
		t.Fatalf("identity vertex must NOT be created when consumerRoleKey names the wrong role")
	}
}
