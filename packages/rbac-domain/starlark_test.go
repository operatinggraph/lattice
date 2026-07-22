// Starlark unit smoke tests for the rbac-domain DDL script.
//
// One `rbac` script handles 10 op branches; each test exercises one
// op branch against that single script.
//
// Coverage (one per operationType, parses + Contract #3 shape):
//   - TestStarlark_Rbac_CreateRole
//   - TestStarlark_Rbac_UpdateRole
//   - TestStarlark_Rbac_TombstoneRole
//   - TestStarlark_Rbac_TombstoneRole_RejectsOpenQueuedTask   (Contract #10 §10.1 no-orphan guard)
//   - TestStarlark_Rbac_TombstoneRole_AllowsClosedQueuedTask  (live link, terminal task -- must allow)
//   - TestStarlark_Rbac_TombstoneRole_SkipsTombstonedQueuedLink
//   - TestStarlark_Rbac_CreatePermission
//   - TestStarlark_Rbac_AssignRole          (holdsRole link key shape)
//   - TestStarlark_Rbac_RevokeRole
//   - TestStarlark_Rbac_GrantPermission     (grantedBy link key shape)
//   - TestStarlark_Rbac_Parses              (rbac compiles for every op)
//
// `reportsTo` is NOT in the rbac-domain package. The pre-4.7
// TestStarlark_ReportsTo_AssignReportingChain test is ported as
// TestStarlark_ReportsTo_Skipped — a documented skip that surfaces the
// pending relocation work. See packages/rbac-domain/reports_to_test.go.
package rbacdomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"

	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// rbacScript returns the single rbac DDL Starlark source.
func rbacScript() string {
	for _, d := range rbacdomain.Package.DDLs {
		if d.CanonicalName == "rbac" {
			return d.Script
		}
	}
	panic("rbac DDL not found in rbac-domain package")
}

// Test NanoIDs for Starlark unit tests.
const (
	starlarkActorID = "JsktstActHJKMNPQRSTU" // 20 chars
	starlarkRoleID  = "JsktstRozeHJKMNPQRSV" // 20 chars (no 'l' -- must pass substrate.IsValidNanoID for kv.Links)
	starlarkPermID  = "JsktstPermHJKMNPQRSW" // 20 chars
)

// emptyLinkLister is a processor.ScriptLinkLister returning no links for any
// filter — the default kv.Links seam for rbac unit tests that don't exercise
// TombstoneRole's open-task guard (Contract #10 §10.1).
type emptyLinkLister struct{}

func (emptyLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return nil, "", nil
}

// makeRbacScriptContext builds a minimal ScriptContext for an rbac
// script unit test.
func makeRbacScriptContext(opType, payloadJSON string, hydrated map[string]processor.VertexDoc) processor.ScriptContext {
	return processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ2y",
			Lane:          processor.LaneDefault,
			OperationType: opType,
			Actor:         "vtx.identity." + starlarkActorID,
			SubmittedAt:   "2026-05-22T10:00:00Z",
			Payload:       json.RawMessage(payloadJSON),
		},
		Hydrated:     hydrated,
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: rbacScript(),
		ScriptClass:  "rbac",
		LinkLister:   emptyLinkLister{},
	}
}

// assertContract3Shape verifies the result conforms to Contract #3.
func assertContract3Shape(t *testing.T, result processor.ScriptResult, wantMutations bool) {
	t.Helper()
	if wantMutations && len(result.Mutations) == 0 {
		t.Fatalf("expected at least one mutation, got none")
	}
	for i, m := range result.Mutations {
		if m.Op != "create" && m.Op != "update" && m.Op != "tombstone" {
			t.Fatalf("mutations[%d].op = %q", i, m.Op)
		}
		if m.Key == "" {
			t.Fatalf("mutations[%d].key empty", i)
		}
	}
}

// aliveVertex constructs a hydrated VertexDoc that vertex_alive() will
// treat as live.
func aliveVertex(key, class string) processor.VertexDoc {
	return processor.VertexDoc{Key: key, Class: class, IsDeleted: false, Data: map[string]interface{}{}}
}

func TestStarlark_Rbac_CreateRole(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	sc := makeRbacScriptContext("CreateRole", `{"name":"TestRole","description":"smoke"}`, nil)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	if !strings.HasPrefix(result.Mutations[0].Key, "vtx.role.") {
		t.Fatalf("mutations[0].key = %q, want vtx.role.*", result.Mutations[0].Key)
	}
	if len(result.Events) == 0 || result.Events[0].Class != "rbac.roleCreated" {
		t.Fatalf("expected RoleCreated, got %+v", result.Events)
	}
}

func TestStarlark_Rbac_UpdateRole(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	roleKey := "vtx.role." + starlarkRoleID
	hydrated := map[string]processor.VertexDoc{
		roleKey: aliveVertex(roleKey, "role"),
	}
	sc := makeRbacScriptContext("UpdateRole",
		`{"roleKey":"`+roleKey+`","description":"updated"}`, hydrated)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	if result.Mutations[0].Op != "update" {
		t.Fatalf("op = %q, want update", result.Mutations[0].Op)
	}
}

func TestStarlark_Rbac_TombstoneRole(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	roleKey := "vtx.role." + starlarkRoleID
	hydrated := map[string]processor.VertexDoc{
		roleKey: aliveVertex(roleKey, "role"),
	}
	sc := makeRbacScriptContext("TombstoneRole",
		`{"roleKey":"`+roleKey+`"}`, hydrated)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	if result.Mutations[0].Op != "tombstone" {
		t.Fatalf("op = %q", result.Mutations[0].Op)
	}
}

// staticLinkLister returns a fixed page of links for every kv.Links call --
// sufficient for these tests, which enumerate at most one page.
type staticLinkLister struct {
	links []processor.LinkDoc
}

func (s staticLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return s.links, "", nil
}

// staticKVReader serves a fixed set of vertex docs for kv.Read -- stands in
// for the task vertex a TombstoneRole open-task-guard candidate link points
// at (Contract #10 §10.1).
type staticKVReader struct {
	docs map[string]processor.VertexDoc
}

func (s staticKVReader) ReadVertex(_ context.Context, key string) (*processor.VertexDoc, error) {
	if d, ok := s.docs[key]; ok {
		return &d, nil
	}
	return nil, nil
}

const starlarkQueuedTaskID = "JsktstTaskHJKMNPQRST" // 20 chars

// TestStarlark_Rbac_TombstoneRole_RejectsOpenQueuedTask: a role holding a
// live queuedFor link to a still-open task must be rejected
// (RoleHasOpenTasks), not silently orphaned (Contract #10 §10.1).
func TestStarlark_Rbac_TombstoneRole_RejectsOpenQueuedTask(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	roleKey := "vtx.role." + starlarkRoleID
	taskKey := "vtx.task." + starlarkQueuedTaskID
	queuedLnk := "lnk.task." + starlarkQueuedTaskID + ".queuedFor.role." + starlarkRoleID

	sc := makeRbacScriptContext("TombstoneRole", `{"roleKey":"`+roleKey+`"}`,
		map[string]processor.VertexDoc{roleKey: aliveVertex(roleKey, "role")})
	sc.LinkLister = staticLinkLister{links: []processor.LinkDoc{
		{Key: queuedLnk, Class: "queuedFor", SourceVertex: taskKey, TargetVertex: roleKey},
	}}
	sc.KVReader = staticKVReader{docs: map[string]processor.VertexDoc{
		taskKey: {Key: taskKey, Class: "task", Data: map[string]any{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}},
	}}

	_, err := runner.Run(context.Background(), sc)
	if err == nil || !strings.Contains(err.Error(), "RoleHasOpenTasks") {
		t.Fatalf("role with an open queued task: want RoleHasOpenTasks rejection, got %v", err)
	}
}

// TestStarlark_Rbac_TombstoneRole_AllowsClosedQueuedTask: CompleteTask /
// CancelTask never tombstone the queuedFor/assignedTo link (orchestration-base
// leaves it live post-transition), so link liveness alone must NOT block a
// tombstone -- only a still-"open" task blocks. A live link to a completed
// task must allow TombstoneRole to proceed.
func TestStarlark_Rbac_TombstoneRole_AllowsClosedQueuedTask(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	roleKey := "vtx.role." + starlarkRoleID
	taskKey := "vtx.task." + starlarkQueuedTaskID
	queuedLnk := "lnk.task." + starlarkQueuedTaskID + ".queuedFor.role." + starlarkRoleID

	sc := makeRbacScriptContext("TombstoneRole", `{"roleKey":"`+roleKey+`"}`,
		map[string]processor.VertexDoc{roleKey: aliveVertex(roleKey, "role")})
	sc.LinkLister = staticLinkLister{links: []processor.LinkDoc{
		{Key: queuedLnk, Class: "queuedFor", SourceVertex: taskKey, TargetVertex: roleKey},
	}}
	sc.KVReader = staticKVReader{docs: map[string]processor.VertexDoc{
		taskKey: {Key: taskKey, Class: "task", Data: map[string]any{"status": "complete", "expiresAt": "2030-01-01T00:00:00Z"}},
	}}

	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("role with only a completed queued task: unexpected rejection: %v", err)
	}
	assertContract3Shape(t, result, true)
	if result.Mutations[0].Op != "tombstone" {
		t.Fatalf("op = %q, want tombstone", result.Mutations[0].Op)
	}
}

// TestStarlark_Rbac_TombstoneRole_SkipsTombstonedQueuedLink: a tombstoned
// (isDeleted) queuedFor link is fast-skipped without even a kv.Read of its
// target -- a role whose only queuedFor link is already gone tombstones
// cleanly.
func TestStarlark_Rbac_TombstoneRole_SkipsTombstonedQueuedLink(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	roleKey := "vtx.role." + starlarkRoleID
	taskKey := "vtx.task." + starlarkQueuedTaskID
	queuedLnk := "lnk.task." + starlarkQueuedTaskID + ".queuedFor.role." + starlarkRoleID

	sc := makeRbacScriptContext("TombstoneRole", `{"roleKey":"`+roleKey+`"}`,
		map[string]processor.VertexDoc{roleKey: aliveVertex(roleKey, "role")})
	sc.LinkLister = staticLinkLister{links: []processor.LinkDoc{
		{Key: queuedLnk, Class: "queuedFor", IsDeleted: true, SourceVertex: taskKey, TargetVertex: roleKey},
	}}
	// No KVReader wired: if the script tried to kv.Read the tombstoned
	// link's source it would error (nil reader), so success here proves the
	// fast-skip.

	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("role with only a tombstoned queuedFor link: unexpected rejection: %v", err)
	}
	assertContract3Shape(t, result, true)
}

func TestStarlark_Rbac_CreatePermission(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	sc := makeRbacScriptContext("CreatePermission",
		`{"operationType":"CreateRole","scope":"any","note":"test"}`, nil)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	if !strings.HasPrefix(result.Mutations[0].Key, "vtx.permission.") {
		t.Fatalf("mutations[0].key = %q, want vtx.permission.*", result.Mutations[0].Key)
	}
	if len(result.Events) == 0 || result.Events[0].Class != "rbac.permissionCreated" {
		t.Fatalf("expected PermissionCreated, got %+v", result.Events)
	}
}

// TestStarlark_Rbac_AssignRole asserts the post-4.7 holdsRole link
// key shape (actor=younger, role=older).
func TestStarlark_Rbac_AssignRole(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	actorKey := "vtx.identity." + starlarkActorID
	roleKey := "vtx.role." + starlarkRoleID
	hydrated := map[string]processor.VertexDoc{
		actorKey: aliveVertex(actorKey, "identity"),
		roleKey:  aliveVertex(roleKey, "role"),
	}
	sc := makeRbacScriptContext("AssignRole",
		`{"actorKey":"`+actorKey+`","roleKey":"`+roleKey+`"}`, hydrated)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	wantKey := "lnk.identity." + starlarkActorID + ".holdsRole.role." + starlarkRoleID
	if result.Mutations[0].Key != wantKey {
		t.Fatalf("mutations[0].key = %q, want %q", result.Mutations[0].Key, wantKey)
	}
	if len(result.Events) == 0 || result.Events[0].Class != "rbac.roleAssigned" {
		t.Fatalf("expected RoleAssigned, got %+v", result.Events)
	}
}

// TestStarlark_Rbac_RevokeRole asserts the holdsRole link is tombstoned.
func TestStarlark_Rbac_RevokeRole(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	actorKey := "vtx.identity." + starlarkActorID
	roleKey := "vtx.role." + starlarkRoleID
	linkKey := "lnk.identity." + starlarkActorID + ".holdsRole.role." + starlarkRoleID
	hydrated := map[string]processor.VertexDoc{
		linkKey: {Key: linkKey, Class: "holdsRole", IsDeleted: false, Data: map[string]interface{}{}},
	}
	sc := makeRbacScriptContext("RevokeRole",
		`{"actorKey":"`+actorKey+`","roleKey":"`+roleKey+`"}`, hydrated)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	if result.Mutations[0].Op != "tombstone" {
		t.Fatalf("op = %q", result.Mutations[0].Op)
	}
	if result.Mutations[0].Key != linkKey {
		t.Fatalf("mutations[0].key = %q, want %q", result.Mutations[0].Key, linkKey)
	}
}

// TestStarlark_Rbac_GrantPermission asserts the grantedBy link key
// shape (post-4.7 rename of grantsPermission).
func TestStarlark_Rbac_GrantPermission(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	permKey := "vtx.permission." + starlarkPermID
	roleKey := "vtx.role." + starlarkRoleID
	hydrated := map[string]processor.VertexDoc{
		permKey: aliveVertex(permKey, "permission"),
		roleKey: aliveVertex(roleKey, "role"),
	}
	sc := makeRbacScriptContext("GrantPermission",
		`{"permKey":"`+permKey+`","roleKey":"`+roleKey+`"}`, hydrated)
	result, err := runner.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertContract3Shape(t, result, true)
	wantKey := "lnk.permission." + starlarkPermID + ".grantedBy.role." + starlarkRoleID
	if result.Mutations[0].Key != wantKey {
		t.Fatalf("mutations[0].key = %q, want %q", result.Mutations[0].Key, wantKey)
	}
	if len(result.Events) == 0 || result.Events[0].Class != "rbac.permissionGranted" {
		t.Fatalf("expected PermissionGranted, got %+v", result.Events)
	}
}

// TestStarlark_Rbac_Parses: compile-only check for every op branch
// (post-4.7 successor to TestStarlark_AllScriptsParse).
func TestStarlark_Rbac_Parses(t *testing.T) {
	cases := []struct {
		opType  string
		payload string
		seed    func() map[string]processor.VertexDoc
	}{
		{"CreateRole", `{"name":"x"}`, func() map[string]processor.VertexDoc { return nil }},
		{"CreatePermission", `{"operationType":"X","scope":"any"}`, func() map[string]processor.VertexDoc { return nil }},
		{"AssignRole",
			`{"actorKey":"vtx.identity.` + starlarkActorID + `","roleKey":"vtx.role.` + starlarkRoleID + `"}`,
			func() map[string]processor.VertexDoc {
				return map[string]processor.VertexDoc{
					"vtx.identity." + starlarkActorID: aliveVertex("vtx.identity."+starlarkActorID, "identity"),
					"vtx.role." + starlarkRoleID:      aliveVertex("vtx.role."+starlarkRoleID, "role"),
				}
			}},
		{"GrantPermission",
			`{"permKey":"vtx.permission.` + starlarkPermID + `","roleKey":"vtx.role.` + starlarkRoleID + `"}`,
			func() map[string]processor.VertexDoc {
				return map[string]processor.VertexDoc{
					"vtx.permission." + starlarkPermID: aliveVertex("vtx.permission."+starlarkPermID, "permission"),
					"vtx.role." + starlarkRoleID:       aliveVertex("vtx.role."+starlarkRoleID, "role"),
				}
			}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.opType, func(t *testing.T) {
			runner := processor.NewStarlarkRunner(0, 0)
			sc := makeRbacScriptContext(tc.opType, tc.payload, tc.seed())
			if _, err := runner.Run(context.Background(), sc); err != nil {
				t.Fatalf("rbac script Run(%s): %v", tc.opType, err)
			}
		})
	}
}
