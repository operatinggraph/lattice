package loftspaceledger_test

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

// Workplace write confinement for LoftspaceCreateAccount (verticals.md —
// "CreateAccount can't be called from any of the 4 ledger apps' browsers").
//
// Unlike clinic-ledger's/wellness-ledger's identical create op — granted
// frontOfHouse UNCONFINED because a patient/member carries no building — a
// leaseapp sits at a unit, so LoftspaceCreateAccount's frontOfHouse grant is
// workplace-confined in scripts.go, mirroring cafe-ledger's
// CreditCafeAccount guard (workplace_confinement_test.go) one hop shorter:
// LoftspaceCreateAccount names the LEASE directly (leaseAppKey), so the
// resolver walks appliesToUnit only — no heldFor hop, since the account does
// not exist yet at create time.
//
//	vtx.building.<A>            vtx.building.<B>
//	      ^ containedIn               ^ containedIn
//	vtx.unit.<A>                vtx.unit.<B>
//	      ^ appliesToUnit             ^ appliesToUnit
//	vtx.leaseapp.<A>             vtx.leaseapp.<B>
//
// The staff identity worksAt building A only.
const (
	wcStaffID  = "BBLLWCSTAFFHJKMNPQRS"
	wcStaffKey = "vtx.identity." + wcStaffID

	wcBuildingAID = "BBLLWCBLDGAHJKMNPQRS"
	wcBuildingBID = "BBLLWCBLDGBHJKMNPQRS"
	wcUnitAID     = "BBLLWCUNTAHJKMNPQRST"
	wcUnitBID     = "BBLLWCUNTBHJKMNPQRST"
	wcLeaseAID    = "BBLLWCLEASEAHJKMNPQR"
	wcLeaseBID    = "BBLLWCLEASEBHJKMNPQR"

	wcBuildingAKey = "vtx.building." + wcBuildingAID
	wcBuildingBKey = "vtx.building." + wcBuildingBID
)

// wcStaffCapDoc grants the same scope=any LoftspaceCreateAccount the operator
// cap doc grants. That is the point: if confinement holds, it holds entirely
// inside the script, because nothing here distinguishes this actor from root.
func wcStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + wcStaffID,
		Actor:                  wcStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wcStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "LoftspaceCreateAccount", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

func wcWorksAtLink() string {
	return "lnk.identity." + wcStaffID + ".worksAt.building." + wcBuildingAID
}

// seedWorkplaceTopology builds the two-building world above and returns the
// two LEASE keys (A, B). The staff identity is wired worksAt building A only
// and holds NO operator holdsRole link — it cannot prove root.
func seedWorkplaceTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) (string, string) {
	t.Helper()
	seedVertex(t, ctx, conn, wcStaffKey, "identity", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingAKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingBKey, "location", map[string]any{})

	mk := func(unitID, leaseID, buildingID string) string {
		unitKey := "vtx.unit." + unitID
		seedVertex(t, ctx, conn, unitKey, "location", map[string]any{})
		testutil.SeedLink(t, ctx, conn,
			"lnk.unit."+unitID+".containedIn.building."+buildingID,
			"containedIn", unitKey, "vtx.building."+buildingID)

		leaseKey := seedLease(t, ctx, conn, leaseID)
		testutil.SeedLink(t, ctx, conn,
			"lnk.leaseapp."+leaseID+".appliesToUnit.unit."+unitID,
			"appliesToUnit", leaseKey, unitKey)
		return leaseKey
	}
	leaseA := mk(wcUnitAID, wcLeaseAID, wcBuildingAID)
	leaseB := mk(wcUnitBID, wcLeaseBID, wcBuildingBID)

	testutil.SeedLink(t, ctx, conn, wcWorksAtLink(), "worksAt", wcStaffKey, wcBuildingAKey)
	return leaseA, leaseB
}

// tombstoneWorksAt soft-deletes the worksAt link the way UnwireWorksAt does —
// the document stays in Core KV with isDeleted:true, which is precisely what
// a `kv.Read(k) == None` test would sail past, because a tombstone hydrates
// as a DOCUMENT, not None.
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

// createAccountAs submits LoftspaceCreateAccount against leaseKey as the
// given actor and asserts the outcome.
func createAccountAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, leaseKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceCreateAccount",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestCreateAccountWorkplace_StaffConfinedToWorkplace is the pair that
// matters: the SAME front-desk actor, holding one scope=any grant, is
// accepted opening the account for the lease at the building it worksAt and
// rejected opening one across town. The accepted vector runs first on
// purpose — a rejection-only test would pass just as well against a guard
// that denied everyone.
func TestCreateAccountWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "createworkplace")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	createAccountAs(t, ctx, conn, cp, cons, "llwccreateathome001",
		wcStaffKey, leaseA, processor.OutcomeAccepted)
	createAccountAs(t, ctx, conn, cp, cons, "llwccreateaway00001",
		wcStaffKey, leaseB, processor.OutcomeRejected)
}

// TestCreateAccountWorkplace_OperatorUnconfined proves the guard exempts
// root by the holdsRole LINK, not by anything the caller supplies: the
// operator actor worksAt nowhere, and opens accounts at both buildings.
func TestCreateAccountWorkplace_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "createoperator")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	createAccountAs(t, ctx, conn, cp, cons, "llwcopcreateaaa0001",
		ledgerActorKey, leaseA, processor.OutcomeAccepted)
	createAccountAs(t, ctx, conn, cp, cons, "llwcopcreatebbb0001",
		ledgerActorKey, leaseB, processor.OutcomeAccepted)
}

// TestCreateAccountWorkplace_UnwiredStaffDeniedNotWidened covers the
// tombstone: a soft-deleted worksAt link hydrates as a DOCUMENT, not None,
// so a guard that only tested `kv.Read(k) == None` would read an unwired
// staffer as wired. Unwiring must narrow the write surface to nothing, never
// widen it from one building to all of them.
func TestCreateAccountWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "createunwired")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	tombstoneWorksAt(t, ctx, conn)

	createAccountAs(t, ctx, conn, cp, cons, "llwcunwiredhome0001",
		wcStaffKey, leaseA, processor.OutcomeRejected)
	createAccountAs(t, ctx, conn, cp, cons, "llwcunwiredaway0001",
		wcStaffKey, leaseB, processor.OutcomeRejected)
}

// TestCreateAccountWorkplace_UnlocatableLeaseIsOperatorOnly pins the
// fail-closed end of the resolver. A lease that names no unit resolves to an
// empty candidate list, and require_workplace treats that as a denial for
// anyone but an operator — an unwired topology must not fall open.
func TestCreateAccountWorkplace_UnlocatableLeaseIsOperatorOnly(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "createunlocatable")

	seedWorkplaceTopology(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	orphanLease := seedLease(t, ctx, conn, "BBLLWCRPHANLEASEHJKM")

	createAccountAs(t, ctx, conn, cp, cons, "llwcorphanstaff0001",
		wcStaffKey, orphanLease, processor.OutcomeRejected)
	createAccountAs(t, ctx, conn, cp, cons, "llwcorphanroot00001",
		ledgerActorKey, orphanLease, processor.OutcomeAccepted)
}
