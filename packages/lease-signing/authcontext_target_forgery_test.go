// Workplace-confinement forgery vector. `authContext.target` is a client field
// the Gateway forwards verbatim and step 3 never inspects on the scope=any
// path, so a guard that exempts a caller from confinement on target PRESENCE is
// forgeable by any staff member holding a standing grant.
//
// DecideLeaseApplication is the op that matters here: it is landlord/staff-only
// (no scope=self path), so unlike Create/Withdraw/SetApplicantProfile it
// carries no applicant-self ownership proof to independently stop a forged
// target — the workplace guard is the only thing confining it.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	lfStaffID  = "BBLeaseFrgStaffHJKMN"
	lfStaffKey = "vtx.identity." + lfStaffID
	lfStaffCap = "cap.identity." + lfStaffID

	lfBuildingAID = "BBLeaseFrgBdgAHJKMNP"
	lfBuildingBID = "BBLeaseFrgBdgBHJKMNP"

	lfBuildingAKey = "vtx.building." + lfBuildingAID
	lfBuildingBKey = "vtx.building." + lfBuildingBID
)

// lfStaffCapDoc grants the same scope=any DecideLeaseApplication the operator
// holds — the capability plane cannot tell staff from root, so if confinement
// holds it holds entirely inside the script.
func lfStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    lfStaffCap,
		Actor:                  lfStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{lfStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "DecideLeaseApplication", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// lfDecideAs submits DecideLeaseApplication{decision: declined} as an arbitrary
// actor. A decline needs no signature floor, and the confinement gate binds it
// exactly as tightly as an approve. forgedTarget, when non-empty, becomes
// authContext.target with no task.
func lfDecideAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, leaseAppKey, unitKey, actorKey, forgedTarget string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DecideLeaseApplication",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         "leaseapp",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseAppKey +
			`","decision":"declined","unit":"` + unitKey + `"}`),
		ContextHint: decideReadsFor(leaseAppKey, unitKey),
	}
	if forgedTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: forgedTarget}
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_ForgedAuthContextTargetStaysConfined: a leasing agent wired to
// building A must not decide applications for units in building B by
// fabricating an authContext.target.
func TestWorkplace_ForgedAuthContextTargetStaysConfined(t *testing.T) {
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "lsforge")
	testutil.SeedCapDoc(t, ctx, conn, lfStaffCapDoc())

	seedVertex(t, ctx, conn, lfStaffKey, "identity", map[string]any{})
	seedVertex(t, ctx, conn, lfBuildingAKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, lfBuildingBKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+lfStaffID+".worksAt.building."+lfBuildingAID,
		"worksAt", lfStaffKey, lfBuildingAKey)
	testutil.SeedHoldsRole(t, ctx, conn, lfStaffKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "frontOfHouse"))

	// One application per building. The guard resolves the unit from the
	// application's OWN appliesToUnit link, then walks containedIn to a
	// location, so each unit is wired into its building.
	applicantA := seedApplicant(t, ctx, conn, "BBLeaseFrgAppcntAHJK")
	applicantB := seedApplicant(t, ctx, conn, "BBLeaseFrgAppcntBHJK")
	appA := createApplication(t, ctx, conn, cp, cons, applicantA)
	appB := createApplication(t, ctx, conn, cp, cons, applicantB)
	unitA, unitB := unitKeyFor(applicantA), unitKeyFor(applicantB)
	lfSeedUnitIn(t, ctx, conn, unitA, lfBuildingAKey, lfBuildingAID)
	lfSeedUnitIn(t, ctx, conn, unitB, lfBuildingBKey, lfBuildingBID)

	// POSITIVE SIBLING: the agent decides an application at their OWN building
	// — so a Rejected below is the confinement guard talking, not a broken path.
	if got := lfDecideAs(t, ctx, conn, cp, cons, "lsfga00000000000001",
		appA, unitA, lfStaffKey, ""); got != processor.OutcomeAccepted {
		t.Fatalf("agent DecideLeaseApplication at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got)
	}
	// Unforged control: without a target they are confined to building A.
	if got := lfDecideAs(t, ctx, conn, cp, cons, "lsfgb00000000000002",
		appB, unitB, lfStaffKey, ""); got != processor.OutcomeRejected {
		t.Fatalf("agent DecideLeaseApplication at ANOTHER building = %v, want Rejected", got)
	}
	// THE FORGERY: the same denied call plus a fabricated target.
	if got := lfDecideAs(t, ctx, conn, cp, cons, "lsfgc00000000000003",
		appB, unitB, lfStaffKey, lfStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("agent DecideLeaseApplication at ANOTHER building with a FORGED authContext.target = %v, "+
			"want Rejected — target presence must not exempt a scope=any caller from workplace confinement", got)
	}
	// Denied before any mutation: a decision is TERMINAL, so a decision written
	// here would be unrecoverable, not merely wrong.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, appB+".decision"); err == nil {
		t.Errorf("the forged-target decide wrote %s.decision; it must be denied before any mutation", appB)
	}
}

func lfSeedUnitIn(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey, buildingKey, buildingID string) {
	t.Helper()
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.unit."+unitID+".containedIn.building."+buildingID,
		"containedIn", unitKey, buildingKey)
}
