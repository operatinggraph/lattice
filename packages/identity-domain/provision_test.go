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
//  3. TestProvisionConsumerIdentity_ExistingIdentity_GrantsConsumer — an
//     identity another path minted (a seeded / bound persona) gains the grant
//     without being re-provisioned
//  4. TestProvisionConsumerIdentity_RevokedGrant_StaysRevoked — the grant is
//     absent-only, so a RevokeRole holds against the pre-flight
//  5. TestProvisionConsumerIdentity_TombstonedIdentity_GrantsNothing
//  6. TestProvisionConsumerIdentity_MalformedTargetActorKey_Rejected
//  7. TestProvisionConsumerIdentity_UnknownConsumerRoleKey_Rejected
//  8. TestProvisionConsumerIdentity_NonGatewayActor_Denied — the fail-closed
//     window before the Gateway's identityProvisioner grant is wired
//  9. TestProvisionConsumerIdentity_OtherLiveRoleKey_Rejected — a live but
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

// boundPersonaKey stands for the shape every bound-entity persona arrives in
// (persona-worlds W0/W5): an identity some other path minted — seeded by
// seed-showcase, bound by BindProviderIdentity / BindInstructorIdentity /
// BindServiceProviderIdentity — holding its entity role and no consumer grant.
const boundPersonaKey = "vtx.identity.JbndPrsnHJKMNPQRSTU9"

// providerRoleKey is the generic entity-archetype role all three bind-ops grant
// (clinic BindProviderIdentity, wellness BindInstructorIdentity, service-domain
// BindServiceProviderIdentity) — the only role a bound persona arrives holding.
var providerRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")

// consumerGrantLink is the deterministic Contract #1 §1.1 key the Gateway
// declares and the script tests. Built here independently of the production
// helper so a change to either side has to be made deliberately in both.
func consumerGrantLink(actorKey, roleKey string) string {
	return "lnk.identity." + actorKey[len("vtx.identity."):] +
		".holdsRole.role." + roleKey[len("vtx.role."):]
}

// provisionEnvelope mirrors internal/gateway's provisionActorIfNeeded,
// declaring both class-(d) optional reads: the identity vertex (absent on the
// fresh-actor path) and the grant link (absent until granted). The link
// declaration is what lets the script tell "already granted" from "grant
// missing"; a dispatcher that omits it hydrates the link as absent and drives
// an already-granted actor into a create it can never commit.
func provisionEnvelope(t *testing.T, requestID, targetActorKey, roleKey, submittedAt string) *processor.OperationEnvelope {
	t.Helper()
	return &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         gatewayActorKey,
		SubmittedAt:   submittedAt,
		Class:         "identity",
		Payload:       provisionPayload(t, targetActorKey, roleKey),
		ContextHint: &processor.ContextHint{
			Reads:         []string{roleKey},
			OptionalReads: []string{targetActorKey, consumerGrantLink(targetActorKey, roleKey)},
		},
	}
}

// seedTombstonedGrant writes the document a committed RevokeRole leaves at the
// grant link: present, isDeleted. The op must read this as "revoked", not as
// "absent" — the harness stands in for the rbac-domain op because the
// identity-domain operator cap doc grants no rbac permissions.
func seedTombstonedGrant(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey, actorKey, roleKey string) {
	t.Helper()
	doc := map[string]any{
		"class": "holdsRole", "isDeleted": true,
		"sourceVertex": actorKey, "targetVertex": roleKey,
		"localName": "holdsRole", "data": map[string]any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned grant: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, b); err != nil {
		t.Fatalf("seed tombstoned grant %s: %v", linkKey, err)
	}
}

func grantIsDeleted(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", linkKey, err)
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", linkKey, err)
	}
	return doc.IsDeleted
}

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

	env := provisionEnvelope(t, testutil.GenReqID("PCISuccess"), freshActorKey, roleKey, "2026-07-06T10:00:00Z")
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

	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("PCIIdem1"), freshActorKey, roleKey, "2026-07-06T10:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("PCIIdem2"), freshActorKey, roleKey, "2026-07-06T10:05:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	linkKey := consumerGrantLink(freshActorKey, roleKey)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey); err != nil {
		t.Fatalf("holdsRole link missing after re-provision: %v", err)
	}
	if grantIsDeleted(t, ctx, conn, linkKey) {
		t.Fatalf("re-provision must leave the live grant untouched, found it tombstoned")
	}
}

// TestProvisionConsumerIdentity_ExistingIdentity_GrantsConsumer is the
// bound-persona case: the identity vertex exists (minted by seed-showcase and
// bound to a clinic provider / wellness instructor / service provider) and
// holds its entity role alone, so before this grant lands the persona can
// reach none of the twenty consumer scope=self ops — it cannot book its own
// appointment or apply for a lease in the building it works in.
//
// The persona is deliberately left at state=unclaimed, which is where
// CreateUnclaimedIdentity leaves it and where every seeded persona stays: a
// grant gated on claim state would exclude exactly the identities this exists
// to serve.
func TestProvisionConsumerIdentity_ExistingIdentity_GrantsConsumer(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-backfill")
	roleKey := consumerRoleKey(t)

	seedIdentityVertex(t, ctx, conn, boundPersonaKey, "unclaimed", "")
	testutil.SeedHoldsRole(t, ctx, conn, boundPersonaKey, providerRoleKey)

	linkKey := consumerGrantLink(boundPersonaKey, roleKey)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey); err == nil {
		t.Fatalf("precondition: the bound persona must hold no consumer grant")
	}

	env := provisionEnvelope(t, testutil.GenReqID("PCIBackfill"), boundPersonaKey, roleKey, "2026-07-27T10:00:00Z")
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("consumer grant not created for the existing identity: %v", err)
	}
	var linkDoc struct {
		Class        string `json:"class"`
		IsDeleted    bool   `json:"isDeleted"`
		SourceVertex string `json:"sourceVertex"`
		TargetVertex string `json:"targetVertex"`
	}
	if err := json.Unmarshal(entry.Value, &linkDoc); err != nil {
		t.Fatalf("unmarshal grant link: %v", err)
	}
	if linkDoc.Class != "holdsRole" || linkDoc.IsDeleted {
		t.Fatalf("grant link class/isDeleted = %q/%v, want holdsRole/false", linkDoc.Class, linkDoc.IsDeleted)
	}
	if linkDoc.SourceVertex != boundPersonaKey || linkDoc.TargetVertex != roleKey {
		t.Fatalf("grant source/target = %q/%q, want %q/%q",
			linkDoc.SourceVertex, linkDoc.TargetVertex, boundPersonaKey, roleKey)
	}

	// The entity role it already held must survive: this completes a persona's
	// grants, it does not re-provision the identity.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, consumerGrantLink(boundPersonaKey, providerRoleKey)); err != nil {
		t.Fatalf("pre-existing entity role grant lost: %v", err)
	}
	stateAspect := readAspectData(t, ctx, conn, boundPersonaKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed (the grant must not re-provision the identity)", got)
	}

	assertTrackerEvent(t, ctx, conn, env.RequestID, "identity.consumerGranted")
}

// TestProvisionConsumerIdentity_RevokedGrant_StaysRevoked is the reason this op
// grants absent-only instead of reusing AssignRole's revive branch. The caller
// is an automatic per-request pre-flight, so a revive would mean a RevokeRole on
// consumer is undone by the revoked actor's very next authenticated request —
// a revocation that silently does not hold.
func TestProvisionConsumerIdentity_RevokedGrant_StaysRevoked(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-revoked")
	roleKey := consumerRoleKey(t)

	seedIdentityVertex(t, ctx, conn, boundPersonaKey, "claimed", "")
	linkKey := consumerGrantLink(boundPersonaKey, roleKey)
	seedTombstonedGrant(t, ctx, conn, linkKey, boundPersonaKey, roleKey)

	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("PCIRevoked"), boundPersonaKey, roleKey, "2026-07-27T11:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if !grantIsDeleted(t, ctx, conn, linkKey) {
		t.Fatalf("a revoked consumer grant was revived by the provisioning pre-flight")
	}
}

// TestProvisionConsumerIdentity_AlreadyGranted_IgnoresRoleVertexHealth pins the
// reason the role-vertex read sits inside the granting branches rather than at
// the top of the op: this runs on every authenticated request, so the path a
// returning actor takes must not be able to fail on the health of state it does
// not need. With the role vertex tombstoned out from under it, an actor who
// already holds the grant still gets a clean no-op.
func TestProvisionConsumerIdentity_AlreadyGranted_IgnoresRoleVertexHealth(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-deadrole")
	roleKey := consumerRoleKey(t)

	seedIdentityVertex(t, ctx, conn, boundPersonaKey, "claimed", "")
	testutil.SeedHoldsRole(t, ctx, conn, boundPersonaKey, roleKey)

	deadRole := map[string]any{"class": "role", "isDeleted": true, "data": map[string]any{}}
	b, err := json.Marshal(deadRole)
	if err != nil {
		t.Fatalf("marshal tombstoned role: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, roleKey, b); err != nil {
		t.Fatalf("tombstone the consumer role vertex: %v", err)
	}

	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("PCIDeadRole"), boundPersonaKey, roleKey, "2026-07-27T13:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if grantIsDeleted(t, ctx, conn, consumerGrantLink(boundPersonaKey, roleKey)) {
		t.Fatalf("the existing grant must be left alone")
	}
}

// TestProvisionConsumerIdentity_TombstonedIdentity_GrantsNothing — a dead
// vertex confers nothing and must not acquire a role on its way out.
func TestProvisionConsumerIdentity_TombstonedIdentity_GrantsNothing(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newProvisionPipeline(t, ctx, conn, "pci-dead")
	roleKey := consumerRoleKey(t)

	deadDoc := map[string]any{"class": "identity", "isDeleted": true, "data": map[string]any{}}
	b, err := json.Marshal(deadDoc)
	if err != nil {
		t.Fatalf("marshal tombstoned identity: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, boundPersonaKey, b); err != nil {
		t.Fatalf("seed tombstoned identity: %v", err)
	}

	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("PCIDead"), boundPersonaKey, roleKey, "2026-07-27T12:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, consumerGrantLink(boundPersonaKey, roleKey)); err == nil {
		t.Fatalf("a tombstoned identity acquired a consumer grant")
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{roleKey},
			OptionalReads: []string{freshActorKey, consumerGrantLink(freshActorKey, roleKey)},
		},
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{roleKey},
			OptionalReads: []string{freshActorKey, consumerGrantLink(freshActorKey, roleKey)},
		},
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
