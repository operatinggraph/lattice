// Workplace-confinement forgery vectors: `authContext.target` is a client
// field the Gateway forwards verbatim, so a guard that exempts a caller from
// confinement on its mere presence is forgeable by anyone holding a scope=any
// grant. These vectors pin the two ways that goes wrong for maintenance:
// fabricating a target outright, and substituting a different resource than the
// one a legitimate task grant was scoped to.
//
// Each negative is paired with its positive sibling — a negative that passes
// because the op failed for an unrelated reason proves nothing.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// mdSubmitResolveForged submits ResolveWorkOrder with a caller-chosen
// authContext: `target` set with NO task, which is what a scope=any holder can
// fabricate. Step 3 authorizes it on the standing grant without ever inspecting
// target, so the script — not the capability plane — is what must catch it.
func mdSubmitResolveForged(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, actorKey, workOrderKey, notes, forgedTarget string) processor.MessageOutcome {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"workOrderKey": workOrderKey, "notes": notes})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ResolveWorkOrder",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T11:30:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads:         []string{workOrderKey},
			OptionalReads: []string{workOrderKey + ".resolution"},
		},
		AuthContext: &processor.AuthContext{Target: forgedTarget},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestResolveWorkOrder_ForgedTargetStaysConfined: a tech holding a standing
// scope=any resolve grant cannot buy their way out of workplace confinement by
// fabricating an authContext.target. Step 3 authorizes scope=any without
// looking at target, so the target is validated by nothing and the guard must
// ignore it.
func TestResolveWorkOrder_ForgedTargetStaysConfined(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdforge")
	mdSeedWorld(t, ctx, conn)

	// A work order at building B — outside the tech's workplace.
	const woID = "BBMANTWQRKGHJKMNPQRS"
	woKey := "vtx.workorder." + woID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdfrg00000000000001",
		mdActorKey, mdBuildingBKey, "Lift is out at B", woID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue at building B = %v, want Accepted", got)
	}
	doc := mdTechCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)

	// POSITIVE SIBLING FIRST: the same actor, same grant, on a work order at
	// the building they DO work at, succeeds. Without this the negative below
	// could be passing because the whole path is broken.
	const okID = "BBMANTWQRKJHJKMNPQRS"
	okKey := "vtx.workorder." + okID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdfrg00000000000002",
		mdActorKey, mdUnitAKey, "Boiler at A", okID); got != processor.OutcomeAccepted {
		t.Fatalf("operator ReportIssue at unit A = %v, want Accepted", got)
	}
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdfrg00000000000003",
		mdTechKey, okKey, "Fixed, my building.", ""); got != processor.OutcomeAccepted {
		t.Fatalf("tech standing-path resolve INSIDE their workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negative below proves nothing)", got)
	}

	// THE FORGERY: a fabricated non-empty target, which the pre-fix guard
	// accepted as an exemption from confinement.
	if got := mdSubmitResolveForged(t, ctx, conn, cp, cons, "mdfrg00000000000004",
		mdTechKey, woKey, "Not my building.", woKey); got != processor.OutcomeRejected {
		t.Fatalf("tech resolve at ANOTHER building with a FORGED authContext.target = %v, want Rejected — "+
			"a scope=any caller must not exempt themselves from workplace confinement by setting a target", got)
	}
	// Fail-closed for the right reason: nothing was written.
	if mdKeyLive(ctx, conn, woKey+".resolution") {
		t.Error("the forged-target resolve wrote a resolution; it must be denied before any mutation")
	}
}

// TestResolveWorkOrder_TaskGrantCannotSubstituteAnotherWorkOrder is the
// resource-bind regression: a task grant is scopedTo ONE work order, but the
// resolved work order comes from `payload.workOrderKey` — an independent client
// field. Holding a legitimate grant for one work order must not resolve a
// different one at a building the claimant does not work at.
func TestResolveWorkOrder_TaskGrantCannotSubstituteAnotherWorkOrder(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdsubst")
	mdSeedWorld(t, ctx, conn)

	// BOTH work orders sit at building B — a building the tech does NOT work
	// at. That is deliberate: it makes the task grant the ONLY thing that can
	// admit the positive below, so the vector isolates the validated-target
	// exemption instead of passing on a worksAt walk that would have succeeded
	// anyway.
	const woAID = "BBMANTWQRKKHJKMNPQRS"
	woAKey := "vtx.workorder." + woAID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdsub00000000000001",
		mdActorKey, mdBuildingBKey, "Boiler at B", woAID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue for WO-A = %v, want Accepted", got)
	}
	const woBID = "BBMANTWQRKMHJKMNPQRS"
	woBKey := "vtx.workorder." + woBID
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdsub00000000000002",
		mdActorKey, mdBuildingBKey, "Lift at B", woBID); got != processor.OutcomeAccepted {
		t.Fatalf("ReportIssue for WO-B = %v, want Accepted", got)
	}

	mdSeedQueuedTask(t, ctx, conn, woAKey)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())
	// The grant is scopedTo WO-A and nothing else.
	testutil.SeedCapDoc(t, ctx, conn, mdTechTaskGrantDoc(woAKey))

	// THE SUBSTITUTION: authContext names WO-A (so step 3 matches the grant and
	// the target IS validated), while the payload names WO-B.
	b, _ := json.Marshal(map[string]any{"workOrderKey": woBKey, "notes": "Resolved the wrong one."})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("mdsub00000000000003"),
		Lane:          processor.LaneDefault,
		OperationType: "ResolveWorkOrder",
		Actor:         mdTechKey,
		SubmittedAt:   "2026-07-21T11:30:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads:         []string{woBKey},
			OptionalReads: []string{woBKey + ".resolution"},
		},
		AuthContext: &processor.AuthContext{Task: mdTaskKey, Target: woAKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("task grant for WO-A resolving WO-B at another building = %v, want Rejected — "+
			"a validated target only exempts the claimant for the work order it names", got)
	}
	if mdKeyLive(ctx, conn, woBKey+".resolution") {
		t.Error("the substituted work order was resolved; the resource bind must deny before any mutation")
	}

	// POSITIVE SIBLING: the SAME grant resolving the work order it actually
	// names still succeeds. This is the regression the whole primitive exists
	// to protect — the task claimant's legitimate path is target != actor, and
	// because WO-A is at a building the tech does NOT work at, the ONLY thing
	// that can admit this is the validated-target exemption. It therefore also
	// pins the commit path's stamping end-to-end: with the bit never set, this
	// falls to the worksAt walk and is denied.
	if got := mdSubmitResolve(t, ctx, conn, cp, cons, "mdsub00000000000004",
		mdTechKey, woAKey, "Replaced the pressure valve.", mdTaskKey); got != processor.OutcomeAccepted {
		t.Fatalf("task claimant resolving the work order their grant NAMES = %v, want Accepted — "+
			"the resource bind must not deny the legitimate task path", got)
	}
}

// TestReportIssue_ForgedTargetStaysConfined: ReportIssue has no self or task
// path at all, so a validated target is never legitimately true there and a
// fabricated one must not exempt a tech from reporting only where they work.
func TestReportIssue_ForgedTargetStaysConfined(t *testing.T) {
	ctx, conn := setupMaintenanceEnv(t)
	cp, cons := mdPipeline(t, ctx, conn, "mdrptforge")
	mdSeedWorld(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, mdTechCapDoc())

	// POSITIVE SIBLING: the tech reports at their own building.
	const okID = "BBMANTWQRKNHJKMNPQRS"
	if got := mdSubmitReportIssue(t, ctx, conn, cp, cons, "mdrpf00000000000001",
		mdTechKey, mdUnitAKey, "Boiler at my building", okID); got != processor.OutcomeAccepted {
		t.Fatalf("tech ReportIssue INSIDE their workplace = %v, want Accepted", got)
	}

	// THE FORGERY: same tech, a building they do not work at, with a
	// fabricated target.
	const badID = "BBMANTWQRKPHJKMNPQRS"
	b, _ := json.Marshal(map[string]any{
		"summary": "Not my building", "location": mdBuildingBKey, "workOrderId": badID,
	})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("mdrpf00000000000002"),
		Lane:          processor.LaneDefault,
		OperationType: "ReportIssue",
		Actor:         mdTechKey,
		SubmittedAt:   "2026-07-21T09:00:00Z",
		Class:         "workOrder",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{mdBuildingBKey}},
		AuthContext:   &processor.AuthContext{Target: mdBuildingBKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("tech ReportIssue at ANOTHER building with a FORGED target = %v, want Rejected", got)
	}
	if mdKeyLive(ctx, conn, "vtx.workorder."+badID) {
		t.Error("the forged-target report created a work order; it must be denied before any mutation")
	}
}
