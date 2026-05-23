// Story 3.6 Starlark unit smoke tests (ported in 4.7 cleanup).
//
// The pre-4.7 form ran 5 separate DDL scripts (role / permission /
// holdsRole / grantsPermission / reportsTo) via the StarlarkRunner.
// The post-4.7 form has ONE `rbac` script with 10 op branches; each
// per-DDL test maps to a per-op-branch test against that single
// script.
//
// Coverage (one per operationType, parses + Contract #3 shape):
//   - TestStarlark_Rbac_CreateRole
//   - TestStarlark_Rbac_UpdateRole
//   - TestStarlark_Rbac_TombstoneRole
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

	"github.com/asolgan/lattice/internal/processor"

	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
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
	starlarkRoleID  = "JsktstRoleHJKMNPQRSV" // 20 chars
	starlarkPermID  = "JsktstPermHJKMNPQRSW" // 20 chars
)

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
	if len(result.Events) == 0 || result.Events[0].Class != "RoleCreated" {
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
	if len(result.Events) == 0 || result.Events[0].Class != "PermissionCreated" {
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
	if len(result.Events) == 0 || result.Events[0].Class != "RoleAssigned" {
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
	if len(result.Events) == 0 || result.Events[0].Class != "PermissionGranted" {
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
