// RBAC integration tests for the rbac-domain Capability Package.
//
// One `rbac` DDL handles 10 op branches with holdsRole + grantedBy
// link key shapes. Tests submit ops and assert outcomes through the
// production commit pipeline + Capability KV.
//
// Coverage:
//  1. TestRoleMgmt_CreateRole          — vtx.role.<X> + canonicalName + description
//  2. TestRoleMgmt_AssignRole          — holdsRole link key shape
//  3. TestRoleMgmt_RevokeRole          — holdsRole link tombstone
//  4. TestRoleMgmt_UnauthorizedDenied  — consumer cap doc -> Rejected
//  5. TestRoleMgmt_AuditViaCapKV       — projected platformPermissions
package rbacdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func newRbacPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "rm-" + durable,
	})
}

// seedRoleVertex pre-seeds a role vertex (alive) so AssignRole /
// RevokeRole / GrantPermission tests have a target to point at. The
// post-4.7 rbac DDL gates AssignRole/RevokeRole on the role vertex
// being alive — without this fixture seed, AssignRole would fail
// with UnknownRole.
func seedRoleVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, roleKey string) {
	t.Helper()
	doc := map[string]any{
		"class":     "role",
		"isDeleted": false,
		"data":      map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, roleKey, b); err != nil {
		t.Fatalf("seed role vertex %s: %v", roleKey, err)
	}
}

// seedIdentityVertex pre-seeds an identity vertex so the operator's
// own identity (referenced in AssignRole) is "alive" per the rbac
// DDL's vertex_alive guard.
func seedIdentityVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	doc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"data":      map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, b); err != nil {
		t.Fatalf("seed identity vertex %s: %v", identityKey, err)
	}
}

// TestRoleMgmt_CreateRole submits CreateRole as operator; asserts the
// role vertex + canonicalName + description aspects are written.
func TestRoleMgmt_CreateRole(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newRbacPipeline(t, ctx, conn, "create")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmCrRole00000"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "rbac",
		Payload:       json.RawMessage(`{"name":"TestRole","description":"A test role"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	te, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(env.RequestID))
	if err != nil {
		t.Fatalf("tracker not found: %v", err)
	}
	tr, err := processor.ParseTracker(te.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}

	mks, _ := tr.Data["mutationKeys"].([]interface{})
	if len(mks) < 3 {
		t.Fatalf("expected >= 3 mutationKeys (role + canonicalName + description), got %v", mks)
	}
	var roleKey string
	for _, mk := range mks {
		if s, ok := mk.(string); ok && strings.HasPrefix(s, "vtx.role.") && !strings.Contains(s, ".canonicalName") && !strings.Contains(s, ".description") {
			roleKey = s
		}
	}
	if roleKey == "" {
		t.Fatalf("no vtx.role.* vertex in mutationKeys: %v", mks)
	}
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, roleKey)
	if err != nil {
		t.Fatalf("role vertex missing: %v", err)
	}
	var roleDoc map[string]any
	_ = json.Unmarshal(entry.Value, &roleDoc)
	if roleDoc["class"] != "role" {
		t.Fatalf("role class = %v", roleDoc["class"])
	}

	// canonicalName aspect must be present with "TestRole".
	cnEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, roleKey+".canonicalName")
	if err != nil {
		t.Fatalf("canonicalName aspect missing: %v", err)
	}
	var cnDoc map[string]any
	_ = json.Unmarshal(cnEntry.Value, &cnDoc)
	cnData, _ := cnDoc["data"].(map[string]any)
	if got, _ := cnData["value"].(string); got != "TestRole" {
		t.Fatalf("canonicalName = %q", got)
	}

	ecs, _ := tr.Data["eventClasses"].([]interface{})
	found := false
	for _, ec := range ecs {
		if ec == "RoleCreated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RoleCreated not in eventClasses: %v", ecs)
	}
}

// TestRoleMgmt_AssignRole creates an actor identity + a role vertex,
// then submits AssignRole and asserts the holdsRole link is written
// with the post-4.7 key shape.
func TestRoleMgmt_AssignRole(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newRbacPipeline(t, ctx, conn, "assign")

	seedIdentityVertex(t, ctx, conn, rmOperatorActorKey)
	seedRoleVertex(t, ctx, conn, rmTargetRoleKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmAsRole00000"),
		Lane:          processor.LaneDefault,
		OperationType: "AssignRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"actorKey":"` + rmOperatorActorKey +
			`","roleKey":"` + rmTargetRoleKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			rmOperatorActorKey,
			rmTargetRoleKey,
		}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	expectedLnk := "lnk.identity." + rmOperatorActorID + ".holdsRole.role." + rmTargetRoleID
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, expectedLnk)
	if err != nil {
		t.Fatalf("holdsRole link missing at %s: %v", expectedLnk, err)
	}
	var lnkDoc map[string]any
	_ = json.Unmarshal(entry.Value, &lnkDoc)
	if lnkDoc["class"] != "holdsRole" {
		t.Fatalf("link class = %v", lnkDoc["class"])
	}
	if isDeleted, _ := lnkDoc["isDeleted"].(bool); isDeleted {
		t.Fatalf("holdsRole link should not be deleted")
	}
}

// TestRoleMgmt_RevokeRole assigns then revokes; asserts the link is
// tombstoned (isDeleted=true).
func TestRoleMgmt_RevokeRole(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newRbacPipeline(t, ctx, conn, "revoke")

	seedIdentityVertex(t, ctx, conn, rmOperatorActorKey)
	seedRoleVertex(t, ctx, conn, rmTargetRoleKey)

	expectedLnk := "lnk.identity." + rmOperatorActorID + ".holdsRole.role." + rmTargetRoleID

	assignEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmAsRoleRv000"),
		Lane:          processor.LaneDefault,
		OperationType: "AssignRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-05-22T10:02:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"actorKey":"` + rmOperatorActorKey +
			`","roleKey":"` + rmTargetRoleKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			rmOperatorActorKey, rmTargetRoleKey,
		}},
	}
	testutil.PublishOp(t, conn, assignEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	revokeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmRvRole00000"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-05-22T10:03:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"actorKey":"` + rmOperatorActorKey +
			`","roleKey":"` + rmTargetRoleKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{expectedLnk}},
	}
	testutil.PublishOp(t, conn, revokeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, expectedLnk)
	if err != nil {
		t.Fatalf("holdsRole link missing after revoke: %v", err)
	}
	var lnkDoc map[string]any
	_ = json.Unmarshal(entry.Value, &lnkDoc)
	if isDeleted, _ := lnkDoc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("holdsRole link should be tombstoned after RevokeRole; got isDeleted=%v", isDeleted)
	}
	_ = time.Now
}

// TestRoleMgmt_UnauthorizedDenied submits CreateRole as the consumer
// actor (no rbac permissions). Expects OutcomeRejected; no vtx.role.*
// keys should be written beyond the pre-seeded fixture.
func TestRoleMgmt_UnauthorizedDenied(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newRbacPipeline(t, ctx, conn, "unauth")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmUnRole00000"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         rmConsumerActorKey,
		SubmittedAt:   "2026-05-22T10:04:00Z",
		Class:         "rbac",
		Payload:       json.RawMessage(`{"name":"NotAllowed","description":"consumer should not be able to create roles"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// No vtx.role.* keys beyond the operator role (seeded by kernel) +
	// any package-seeded roles should exist with the canonicalName
	// "NotAllowed".
	keys, _ := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	for _, k := range keys {
		if !strings.HasSuffix(k, ".canonicalName") {
			continue
		}
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, k)
		if err != nil {
			continue
		}
		var doc map[string]any
		_ = json.Unmarshal(entry.Value, &doc)
		data, _ := doc["data"].(map[string]any)
		if got, _ := data["value"].(string); got == "NotAllowed" {
			t.Fatalf("unexpected NotAllowed role committed despite denial: key=%s", k)
		}
	}
}

// TestRoleMgmt_AuditViaCapKV validates the operator cap doc carries
// the 10 rbac permissions. The setup helpers seed the doc directly;
// in production the same shape comes from the Capability Lens
// projection over the rbac-domain installed package.
func TestRoleMgmt_AuditViaCapKV(t *testing.T) {
	ctx, conn := setupTestEnv(t)

	js := conn.JetStream()
	capKV, err := js.KeyValue(ctx, testutil.HarnessCapBucket)
	if err != nil {
		t.Fatalf("open capability-kv: %v", err)
	}
	entry, err := capKV.Get(ctx, rmOperatorCapKey)
	if err != nil {
		t.Fatalf("get operator cap entry: %v", err)
	}
	var doc processor.CapabilityDoc
	if err := json.Unmarshal(entry.Value(), &doc); err != nil {
		t.Fatalf("unmarshal cap doc: %v", err)
	}
	expected := []string{
		"CreateRole", "UpdateRole", "TombstoneRole",
		"CreatePermission", "UpdatePermission", "TombstonePermission",
		"AssignRole", "RevokeRole",
		"GrantPermission", "RevokePermission",
	}
	permMap := map[string]string{}
	for _, p := range doc.PlatformPermissions {
		permMap[p.OperationType] = p.Scope
	}
	for _, op := range expected {
		scope, ok := permMap[op]
		if !ok {
			t.Errorf("platformPermissions missing %q", op)
			continue
		}
		if scope != "any" {
			t.Errorf("platformPermissions[%q].scope = %q, want any", op, scope)
		}
	}
}
