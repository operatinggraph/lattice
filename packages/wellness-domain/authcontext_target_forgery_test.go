// Workplace-confinement forgery vector. `authContext.target` is a client field
// the Gateway forwards verbatim and step 3 never inspects on the scope=any
// path, so a guard that exempts a caller from confinement on target PRESENCE is
// forgeable by any staff member holding a standing grant.
//
// CreateSession is the op that matters here: it is staff-created by design (no
// scope=self path exists), so unlike CreateBooking/CancelBooking it carries no
// ownership proof to independently stop a forged target — the workplace guard
// is the only thing confining it.
package wellnessdomain_test

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
	wfStaffID  = "BBWELLFQRGESTAFFHJKM"
	wfStaffKey = "vtx.identity." + wfStaffID
	wfStaffCap = "cap.identity." + wfStaffID

	wfBuildingAID = "BBWELLFQRGEBLDGAHJKM"
	wfBuildingBID = "BBWELLFQRGEBLDGBHJKM"

	wfBuildingAKey = "vtx.building." + wfBuildingAID
	wfBuildingBKey = "vtx.building." + wfBuildingBID
)

// wfStaffCapDoc grants the same scope=any CreateSession the operator holds —
// the point being that the capability plane cannot tell staff from root, so if
// confinement holds it holds entirely inside the script.
func wfStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    wfStaffCap,
		Actor:                  wfStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wfStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateSession", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// wfCreateSessionAs submits CreateSession as an arbitrary actor. forgedTarget,
// when non-empty, becomes authContext.target with no task.
func wfCreateSessionAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, studioKey, actorKey, forgedTarget, startsAt, endsAt string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{
		"studio": studioKey, "name": "Vinyasa", "startsAt": startsAt, "endsAt": endsAt, "capacity": 4,
	})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "session",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{studioKey},
			OptionalReads: wdSlotClaimKeys(t, studioKey, startsAt, endsAt),
		},
	}
	if forgedTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: forgedTarget}
	}
	testutil.PublishOp(t, conn, env)
	return "vtx.session." + nanoIDFromRequestID(reqID), testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_ForgedAuthContextTargetStaysConfined: a staff member wired to
// building A must not create sessions at a studio in building B by fabricating
// an authContext.target. Exempting on target presence would hand every staff
// member an opt-out from the multi-org gate.
func TestWorkplace_ForgedAuthContextTargetStaysConfined(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdforge")
	testutil.SeedCapDoc(t, ctx, conn, wfStaffCapDoc())

	seedVertex(t, ctx, conn, wfStaffKey, "identity", map[string]any{})
	seedVertex(t, ctx, conn, wfBuildingAKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wfBuildingBKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+wfStaffID+".worksAt.building."+wfBuildingAID,
		"worksAt", wfStaffKey, wfBuildingAKey)
	testutil.SeedHoldsRole(t, ctx, conn, wfStaffKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "frontOfHouse"))

	// A studio in each building; the location comes from the studio's own
	// locatedAt link, which is what the guard resolves.
	studioA := createStudio(t, ctx, conn, cp, cons, "wdfgstudioa000000001", "Studio A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdfgstudiob000000001", "Studio B")
	wfSeedStudioAt(t, ctx, conn, studioA, wfBuildingAKey, wfBuildingAID)
	wfSeedStudioAt(t, ctx, conn, studioB, wfBuildingBKey, wfBuildingBID)

	// POSITIVE SIBLING: the staff member creates a session at their OWN
	// building — so a Rejected below is the confinement guard talking and not
	// a broken path.
	if _, got := wfCreateSessionAs(t, ctx, conn, cp, cons, "wdfga000000000000001",
		studioA, wfStaffKey, "", "2026-08-01T09:00:00Z", "2026-08-01T10:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateSession at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got)
	}
	// Unforged control: without a target they are confined to building A.
	if _, got := wfCreateSessionAs(t, ctx, conn, cp, cons, "wdfgb000000000000001",
		studioB, wfStaffKey, "", "2026-08-01T11:00:00Z", "2026-08-01T12:00:00Z"); got != processor.OutcomeRejected {
		t.Fatalf("staff CreateSession at ANOTHER building = %v, want Rejected", got)
	}
	// THE FORGERY: the same denied call plus a fabricated target.
	forgedKey, got := wfCreateSessionAs(t, ctx, conn, cp, cons, "wdfgc000000000000001",
		studioB, wfStaffKey, wfStaffKey, "2026-08-01T13:00:00Z", "2026-08-01T14:00:00Z")
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateSession at ANOTHER building with a FORGED authContext.target = %v, want Rejected — "+
			"target presence must not exempt a scope=any caller from workplace confinement", got)
	}
	// Denied before any mutation, not merely reported as denied.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, forgedKey); err == nil {
		t.Errorf("the forged-target CreateSession wrote %s; it must be denied before any mutation", forgedKey)
	}
}

func wfSeedStudioAt(t *testing.T, ctx context.Context, conn *substrate.Conn, studioKey, locationKey, locationID string) {
	t.Helper()
	_, studioID, _ := substrate.ParseVertexKey(studioKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.studio."+studioID+".locatedAt.building."+locationID,
		"locatedAt", studioKey, locationKey)
}
