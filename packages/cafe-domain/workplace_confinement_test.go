package cafedomain_test

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

// Workplace write confinement — facet-staff-worlds-design.md §3.5 / F4.
//
// A staff actor holds its vertical grants at scope=any, exactly as `operator`
// does; nothing in the capability plane distinguishes the two, because scope is
// only `any` or `self` (Contract #6) and a standing grant sets no authContext.
// Confinement is therefore enforced in the op script: a caller that cannot
// prove it is root may write only inside the location it worksAt.
//
// The topology every vector below builds:
//
//	vtx.building.<A>                      vtx.building.<B>
//	      ^ containedIn                         ^ containedIn
//	vtx.unit.<A>                          vtx.unit.<B>
//	      ^ appliesToUnit                       ^ appliesToUnit
//	vtx.leaseapp.<A>                      vtx.leaseapp.<B>
//
// The staff identity worksAt building A only.
const (
	wcStaffID  = "BBCAFEWCSTAFFHJKMNPQ"
	wcStaffKey = "vtx.identity." + wcStaffID
	wcStaffCap = "cap.identity." + wcStaffID

	wcBuildingAID = "BBCAFEWCBLDGAHJKMNPQ"
	wcBuildingBID = "BBCAFEWCBLDGBHJKMNPQ"
	wcUnitAID     = "BBCAFEWCUNTAHJKMNPQR"
	wcUnitBID     = "BBCAFEWCUNTBHJKMNPQR"
	wcLeaseAID    = "BBCAFEWCLEASEAHJKMNP"
	wcLeaseBID    = "BBCAFEWCLEASEBHJKMNP"

	wcBuildingAKey = "vtx.building." + wcBuildingAID
	wcBuildingBKey = "vtx.building." + wcBuildingBID
)

// wcStaffCapDoc grants the same scope=any tab surface the operator cap doc
// grants. That is the point: the capability plane cannot tell staff from root,
// so if confinement holds, it holds entirely inside the script.
func wcStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    wcStaffCap,
		Actor:                  wcStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wcStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "OpenTab", Scope: "any"},
			{OperationType: "Charge", Scope: "any"},
			{OperationType: "Settle", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

func wcWorksAtLink() string {
	return "lnk.identity." + wcStaffID + ".worksAt.building." + wcBuildingAID
}

// seedWorkplaceTopology builds the two-building world above and returns the two
// lease keys (A, B). The staff identity is wired worksAt building A only, and
// holds NO operator holdsRole link — it cannot prove root.
func seedWorkplaceTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) (string, string) {
	t.Helper()
	seedIdentity(t, ctx, conn, wcStaffID)
	seedVertex(t, ctx, conn, wcBuildingAKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingBKey, "location", map[string]any{})

	mk := func(unitID, leaseID, buildingKey string) string {
		unitKey := "vtx.unit." + unitID
		seedVertex(t, ctx, conn, unitKey, "location", map[string]any{})
		testutil.SeedLink(t, ctx, conn,
			"lnk.unit."+unitID+".containedIn.building."+buildingKey[len("vtx.building."):],
			"containedIn", unitKey, buildingKey)

		leaseKey := "vtx.leaseapp." + leaseID
		seedVertex(t, ctx, conn, leaseKey, "leaseapp", map[string]any{})
		testutil.SeedLink(t, ctx, conn,
			"lnk.leaseapp."+leaseID+".appliesToUnit.unit."+unitID,
			"appliesToUnit", leaseKey, unitKey)
		return leaseKey
	}
	leaseA := mk(wcUnitAID, wcLeaseAID, wcBuildingAKey)
	leaseB := mk(wcUnitBID, wcLeaseBID, wcBuildingBKey)

	testutil.SeedLink(t, ctx, conn, wcWorksAtLink(), "worksAt", wcStaffKey, wcBuildingAKey)
	return leaseA, leaseB
}

// tombstoneWorksAt soft-deletes the worksAt link the way UnwireWorksAt does —
// the document stays in Core KV with isDeleted:true. This is the case a
// `kv.Read(k) == None` guard silently passes, because a tombstone hydrates as a
// DOCUMENT, not None.
func tombstoneWorksAt(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	doc := map[string]any{
		"class": "worksAt", "isDeleted": true,
		"sourceVertex": wcStaffKey, "targetVertex": wcBuildingAKey,
		"localName": "worksAt", "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, wcWorksAtLink(), b); err != nil {
		t.Fatalf("tombstone worksAt: %v", err)
	}
}

// submitOpenTabAs submits OpenTab{leaseAppKey} as an arbitrary actor on the
// standing path (no authContext), declaring exactly what a staff caller would.
func submitOpenTabAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, leaseKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab"},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_OperatorUnconfined proves the guard leaves root alone: the
// operator actor holds no worksAt link at all and still writes at both
// buildings. A worksAt-derived exemption would produce this same result — the
// Unwired vector below is what tells the two designs apart.
func TestWorkplace_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wcoperator")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcopa000000000000001", leaseA, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab at building A = %v, want Accepted (root stays unconfined)", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcopb000000000000002", leaseB, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab at building B = %v, want Accepted (root stays unconfined)", got)
	}
}

// TestWorkplace_StaffConfinedToWorkplace is the F4 guarantee: one staff actor,
// one scope=any grant, accepted at the building it worksAt and rejected at the
// one it does not.
func TestWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcstaff")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcsta00000000000001", leaseA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff OpenTab at its OWN workplace = %v, want Accepted", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcstb00000000000002", leaseB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff OpenTab at ANOTHER building = %v, want Rejected — this is the multi-org gate", got)
	}
}

// TestWorkplace_UnwiredStaffDeniedNotWidened pins the design decision, and is
// the vector a `kv.Read(link) == None` guard fails.
//
// UnwireWorksAt tombstones rather than deletes, and a tombstone hydrates as a
// document — so `== None` reads it as "no workplace". Under a worksAt-derived
// exemption that means UNCONFINED: unwiring a staff member's workplace would
// widen their write surface from one building to every building. The exemption
// is role-derived precisely so this actor — who can no longer prove a workplace
// and cannot prove root either — is denied everywhere.
func TestWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcunwired")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	tombstoneWorksAt(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcuwa00000000000001", leaseA, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("unwired staff at its FORMER workplace = %v, want Rejected", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcuwb00000000000002", leaseB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("unwired staff at another building = %v, want Rejected — an unwire must NEVER widen the write surface", got)
	}
}

// TestWorkplace_UnlocatableTargetIsOperatorOnly pins the fail-closed default: a
// lease whose unit is wired into no building resolves to no location, cannot be
// confined, and is therefore root-only. Falling open here would make "remove
// the containedIn link" a confinement bypass.
func TestWorkplace_UnlocatableTargetIsOperatorOnly(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcorphan")
	seedWorkplaceTopology(t, ctx, conn)

	orphanLease := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEWCQRPHANLEASEH", wcStaffID)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcora00000000000001", orphanLease, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff OpenTab on an unlocatable lease = %v, want Rejected (fail closed)", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcorb00000000000002", orphanLease, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab on an unlocatable lease = %v, want Accepted", got)
	}
}
