// The withdrawal of the one rbac op that rewrites a permission vertex's body,
// proven end to end on a fresh rbac-domain install. What is proven here is the
// OPERATION channel; `UpgradePackage`'s bootstrap DDL reaches the same bodies
// by a route no rbac grant governs, filed separately.
//
// `UpdatePermission` is the one rbac op that rewrites an existing permission
// vertex's `data` — its operationType, its scope, and (once the provenance
// stamp lands) its `data.origin`. Contract #6 §6.1 rule 1 requires that body to
// be write-once, so the package dispatches the op but grants it to nobody. The
// enforcement is absence: with no matching platformPermission, step 3 denies
// before the script is ever reached.
//
// Absence is exactly the kind of guarantee a broken fixture can fake, so the
// denial is never asserted on its own here — every case runs a positive vector
// through the same actor and pipeline first.
package rbacdomain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"

	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// rmSeededPermID is the permission vertex the UpdatePermission attempt targets.
// 20 chars, substrate.Alphabet only.
const (
	rmSeededPermID  = "RmPermXzBbCdEfGHJKMN"
	rmSeededPermKey = "vtx.permission." + rmSeededPermID
)

// TestRoleMgmt_UpdatePermissionUngranted proves the withdrawal is load-bearing
// at step 3, not just absent from a Go slice.
//
// The order is the whole test. The operator's cap doc is derived from the
// package's own PermissionSpecs, so a fixture that failed to reach the
// authorizer at all — a mis-seeded doc, a pipeline wired to the wrong bucket,
// an actor key typo — would deny EVERY op and the denial half would pass while
// proving nothing. CreateRole runs first as the same actor through the same
// pipeline: it can only succeed if the capability path is live, which is what
// makes the following denial attributable to the missing grant.
//
// The target permission vertex is seeded alive and well-formed, so the op is
// valid in every respect except authorization: if the grant were restored, this
// submission would commit rather than fail somewhere downstream.
func TestRoleMgmt_UpdatePermissionUngranted(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newRbacPipeline(t, ctx, conn, "updateperm")

	// Positive vector: a granted rbac op, same actor, same pipeline.
	createEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmUpPermPos00"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-08-14T10:00:00Z",
		Class:         "rbac",
		Payload:       json.RawMessage(`{"name":"WriteOncePositive","description":"proves the capability path is live"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, createEnv)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("positive vector CreateRole outcome = %v, want Accepted (error %+v) — the fixture never reached the auth check, so any denial below would be vacuous", outcome, reply.Error)
	}

	// A live, well-formed target: the op is valid but for the grant.
	permDoc := map[string]any{
		"class":     "permission",
		"isDeleted": false,
		"data":      map[string]any{"operationType": "CreateRole", "scope": "any"},
	}
	b, _ := json.Marshal(permDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, rmSeededPermKey, b); err != nil {
		t.Fatalf("seed permission vertex %s: %v", rmSeededPermKey, err)
	}

	updateEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmUpPermNeg00"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdatePermission",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-08-14T10:01:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"permKey":"` + rmSeededPermKey +
			`","operationType":"InstallPackage","scope":"any"}`),
		ContextHint: &processor.ContextHint{Reads: []string{rmSeededPermKey}},
	}
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, updateEnv)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("UpdatePermission outcome = %v, want Rejected — the operator must hold no grant for a body-rewriting op on the security plane", outcome)
	}
	if reply.Error == nil {
		t.Fatalf("rejected UpdatePermission carries no error")
	}
	if got := string(reply.Error.Code); got != string(processor.ErrCodeAuthDenied) {
		t.Errorf("rejection code = %q, want %q — a denial for any other reason would not prove the grant is gone", got, processor.ErrCodeAuthDenied)
	}
	if got := reply.Error.Message; !strings.Contains(got, "no matching platformPermission") {
		t.Errorf("rejection reason = %q, want it to name the absent platformPermission — this is the step-3 absence path, not a script guard", got)
	}

	// The residual: the body the op targeted is untouched.
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, rmSeededPermKey)
	if err != nil {
		t.Fatalf("seeded permission vertex missing after the denial: %v", err)
	}
	var after map[string]any
	if err := json.Unmarshal(entry.Value, &after); err != nil {
		t.Fatalf("unmarshal permission vertex: %v", err)
	}
	data, _ := after["data"].(map[string]any)
	if got, _ := data["operationType"].(string); got != "CreateRole" {
		t.Errorf("the permission vertex's operationType is now %q — a denied UpdatePermission rewrote the body it was supposed to be unable to touch", got)
	}
}

// TestPackage_UpdatePermissionDispatchedButUngranted pins the two halves apart.
// §5.2 of the grant-provenance design withdraws the GRANT and keeps the
// Starlark: an ungranted op is denied by absence at step 3, which is the same
// fail-closed the withheld shred verbs rely on, and deleting the branch would
// be permittedCommands churn for a behaviourally identical result. A later
// author tidying "dead" code has to fail this test to remove it.
func TestPackage_UpdatePermissionDispatchedButUngranted(t *testing.T) {
	var dispatched bool
	for _, cmd := range rbacdomain.Package.DDLs[0].PermittedCommands {
		if cmd == "UpdatePermission" {
			dispatched = true
		}
	}
	if !dispatched {
		t.Error("the rbac DDL no longer admits UpdatePermission — the design withdraws the GRANT and keeps the branch (an ungranted op is already denied at step 3 by absence)")
	}
	if !strings.Contains(rbacdomain.Package.DDLs[0].Script, `if ot == "UpdatePermission":`) {
		t.Error("the rbac script no longer implements the UpdatePermission branch — permittedCommands admits it, so removing the branch leaves an admitted command with no dispatch")
	}
	for _, p := range rbacdomain.Package.Permissions {
		if p.OperationType == "UpdatePermission" {
			t.Fatal("rbac-domain grants UpdatePermission — it rewrites a permission vertex's body, which Contract #6 §6.1 rule 1 requires to be write-once")
		}
	}
}
