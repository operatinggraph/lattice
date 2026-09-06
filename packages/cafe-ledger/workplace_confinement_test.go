package cafeledger_test

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

// Workplace write confinement for CreditCafeAccount — persona-worlds-design.md,
// the café confined-ledger-credit brief.
//
// A payment is the one ledger entry a human runs: someone hands money over at
// the counter and the front desk records it. So CreditCafeAccount grants
// frontOfHouse, and a front-desk actor holds that grant at scope=any exactly as
// `operator` does — scope is only `any` or `self` (Contract #6) and a standing
// grant sets no authContext, so the capability plane cannot tell the two apart.
// Confinement lives in the op script instead: a caller that cannot prove it is
// root may credit only accounts whose lease sits somewhere it worksAt.
//
// CreditCafeAccount names an ACCOUNT, not a location, so the guard resolves one
// through two platform-written hops — neither of them a payload field:
//
//	vtx.building.<A>                      vtx.building.<B>
//	      ^ containedIn                         ^ containedIn
//	vtx.unit.<A>                          vtx.unit.<B>
//	      ^ appliesToUnit                       ^ appliesToUnit
//	vtx.leaseapp.<A>                      vtx.leaseapp.<B>
//	      ^ heldFor                             ^ heldFor
//	vtx.cafeaccount.<A>                   vtx.cafeaccount.<B>
//
// The staff identity worksAt building A only.
const (
	wcStaffID  = "BBCAFELWCSTAFFHJKMNP"
	wcStaffKey = "vtx.identity." + wcStaffID
	wcStaffCap = "cap.identity." + wcStaffID

	wcBuildingAID = "BBCAFELWCBLDGAHJKMNP"
	wcBuildingBID = "BBCAFELWCBLDGBHJKMNP"
	wcUnitAID     = "BBCAFELWCUNTAHJKMNPQ"
	wcUnitBID     = "BBCAFELWCUNTBHJKMNPQ"
	wcLeaseAID    = "BBCAFELWCLEASEAHJKMN"
	wcLeaseBID    = "BBCAFELWCLEASEBHJKMN"

	wcBuildingAKey = "vtx.building." + wcBuildingAID
	wcBuildingBKey = "vtx.building." + wcBuildingBID
)

// wcStaffCapDoc grants the same scope=any CreditCafeAccount the operator cap doc
// grants. That is the point: if confinement holds, it holds entirely inside the
// script, because nothing here distinguishes this actor from root.
func wcStaffCapDoc() *processor.CapabilityDoc { return staffCapDocFor(wcStaffKey) }

// staffCapDocFor builds that front-desk cap doc for an arbitrary identity, so a
// vector can seed a second staffer wired to a different level of the topology.
func staffCapDocFor(actorKey string) *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorKey[len("vtx.identity."):],
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreditCafeAccount", Scope: "any"},
			{OperationType: "RefundCafeCharge", Scope: "any"},
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
// LEASE keys (A, B); the caller mints each lease's account through the real
// CreateAccount op, so the heldFor hop under test is the one the package
// actually writes. The staff identity is wired worksAt building A only and
// holds NO operator holdsRole link — it cannot prove root.
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
// the document stays in Core KV with isDeleted:true, which is precisely what a
// `kv.Read(k) == None` test would sail past, because a tombstone hydrates as a
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

// creditAs submits CreditCafeAccount against acctKey as the given actor and asserts
// the outcome. authContextTarget is the raw client-supplied hint; the harness
// never validates it, which is exactly the forgery vector one vector below
// exercises.
//
// Every account it is pointed at must already owe at least the 1850 it pays:
// post_entry caps a payment at the account's own outstanding balance, so a
// credit against a never-charged account is refused whatever the workplace
// says, and an "accepted" vector on one would prove nothing about confinement.
// seedChargedAccount below is how each vector gets there.
func creditAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey, authContextTarget string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"House tab payment"}`),
		ContextHint:   staffCreditHint(actorKey, acctKey),
	}
	if authContextTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authContextTarget}
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// seedChargedAccount opens the account for leaseKey and posts one operator
// charge of amountCents to it, so a confinement vector's payment has a balance
// to pay down. The charge goes in as the operator, which DebitAccount grants at
// scope=any and never confines — the workplace question under test is the
// PAYMENT's, not the charge's.
func seedChargedAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, acctLabel, debitLabel, leaseKey string, amountCents int) string {
	t.Helper()
	acctKey := createAccount(t, ctx, conn, cp, cons, acctLabel, leaseKey)
	postDebit(t, ctx, conn, cp, cons, debitLabel, acctKey, amountCents, "Settled tab")
	return acctKey
}

// TestCreditWorkplace_StaffConfinedToWorkplace is the pair that matters: the
// SAME front-desk actor, holding one scope=any grant, is accepted crediting the
// account at the building it worksAt and rejected crediting the one across
// town. The accepted vector runs first on purpose — a rejection-only test would
// pass just as well against a guard that denied everyone.
func TestCreditWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditworkplace")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcacctaaaa000001", "cafewcdebitaaaa00001", leaseA, 1850)
	acctB := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcacctbbbb000001", "cafewcdebitbbbb00001", leaseB, 1850)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	creditAs(t, ctx, conn, cp, cons, "cafewccreditathome01",
		wcStaffKey, acctA, "", processor.OutcomeAccepted)
	creditAs(t, ctx, conn, cp, cons, "cafewccreditaway0001",
		wcStaffKey, acctB, "", processor.OutcomeRejected)
}

// TestCreditWorkplace_CoversDeeperContainment exercises the containment LOOP
// rather than its first iteration. Every other vector resolves in one hop
// (unit → building), which would pass just as well against a guard that never
// looped at all. Here a floor sits between the unit and the building the
// staffer worksAt, so authorizing requires walking two levels — and the
// zero-hop end is pinned in the same test by a second staffer wired to the
// unit itself, the case worksAt_covers' own comment claims works.
func TestCreditWorkplace_CoversDeeperContainment(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditdeep")

	seedWorkplaceTopology(t, ctx, conn)

	const floorID = "BBCAFELWCFLR3HJKMNPQ"
	const deepUnitID = "BBCAFELWCDEEPUNTHJKM"
	const deepLeaseID = "BBCAFELWCDEEPLEASHJK"

	floorKey := "vtx.floor." + floorID
	deepUnitKey := "vtx.unit." + deepUnitID
	seedVertex(t, ctx, conn, floorKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, deepUnitKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.floor."+floorID+".containedIn.building."+wcBuildingAID,
		"containedIn", floorKey, wcBuildingAKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.unit."+deepUnitID+".containedIn.floor."+floorID,
		"containedIn", deepUnitKey, floorKey)

	deepLease := seedLease(t, ctx, conn, deepLeaseID)
	testutil.SeedLink(t, ctx, conn,
		"lnk.leaseapp."+deepLeaseID+".appliesToUnit.unit."+deepUnitID,
		"appliesToUnit", deepLease, deepUnitKey)
	// 3700 charged: this vector spends two 1850 payments against one account.
	deepAcct := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcdeepacct000001", "cafewcdeepdebit00001", deepLease, 3700)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	// Two levels up: unit → floor → building, where the worksAt link lives.
	creditAs(t, ctx, conn, cp, cons, "cafewcdeepbuilding01",
		wcStaffKey, deepAcct, "", processor.OutcomeAccepted)

	// Zero hops: a second staffer wired to the exact unit, which the walk
	// tests before it climbs at all.
	const unitStaffID = "BBCAFELWCUNTSTAFFHJK"
	unitStaffKey := "vtx.identity." + unitStaffID
	seedVertex(t, ctx, conn, unitStaffKey, "identity", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+unitStaffID+".worksAt.unit."+deepUnitID,
		"worksAt", unitStaffKey, deepUnitKey)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDocFor(unitStaffKey))

	creditAs(t, ctx, conn, cp, cons, "cafewcdeepunitstaff1",
		unitStaffKey, deepAcct, "", processor.OutcomeAccepted)
}

// TestCreditWorkplace_OperatorUnconfined proves the guard exempts root by the
// holdsRole LINK, not by anything the caller supplies: the operator actor
// worksAt nowhere, and credits both buildings.
func TestCreditWorkplace_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditoperator")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcopacctaaa00001", "cafewcopdebitaaa0001", leaseA, 1850)
	acctB := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcopacctbbb00001", "cafewcopdebitbbb0001", leaseB, 1850)

	creditAs(t, ctx, conn, cp, cons, "cafewcopcreditaaa001",
		ledgerActorKey, acctA, "", processor.OutcomeAccepted)
	creditAs(t, ctx, conn, cp, cons, "cafewcopcreditbbb001",
		ledgerActorKey, acctB, "", processor.OutcomeAccepted)
}

// TestCreditWorkplace_ForgedAuthContextTargetStaysConfined pins the reason
// require_workplace exempts on op.authTargetValidated and never on
// authContextTarget being non-empty. The raw target is a client-supplied hint
// any scope=any holder can set to whatever it likes; if its mere presence
// exempted, every staff member could opt out of confinement by naming itself.
func TestCreditWorkplace_ForgedAuthContextTargetStaysConfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditforged")

	_, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctB := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcfgacctbbb00001", "cafewcfgdebitbbb0001", leaseB, 1850)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	creditAs(t, ctx, conn, cp, cons, "cafewcforgedtarget01",
		wcStaffKey, acctB, wcStaffKey, processor.OutcomeRejected)
}

// TestCreditWorkplace_UnwiredStaffDeniedNotWidened covers the tombstone: a
// soft-deleted worksAt link hydrates as a DOCUMENT, not None, so a guard that
// only tested `kv.Read(k) == None` would read an unwired staffer as wired.
// Unwiring must narrow the write surface to nothing, never widen it from one
// building to all of them.
func TestCreditWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditunwired")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcuwacctaaa00001", "cafewcuwdebitaaa0001", leaseA, 1850)
	acctB := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcuwacctbbb00001", "cafewcuwdebitbbb0001", leaseB, 1850)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	tombstoneWorksAt(t, ctx, conn)

	creditAs(t, ctx, conn, cp, cons, "cafewcunwiredhome001",
		wcStaffKey, acctA, "", processor.OutcomeRejected)
	creditAs(t, ctx, conn, cp, cons, "cafewcunwiredaway001",
		wcStaffKey, acctB, "", processor.OutcomeRejected)
}

// TestRefundWorkplace_StaffConfinedToWorkplace is CreditCafeAccount's
// confinement pair run for RefundCafeCharge, and it is not redundant with it:
// the two ops reach post_entry's require_workplace site through separate
// execute() branches, so a refund branch that forgot confine=True would leave
// a front-desk staffer refunding charges at buildings across town while the
// payment vector above stayed green. The accepted vector runs first — a
// rejection-only test would pass against a guard that denied everyone.
func TestRefundWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundworkplace")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := createAccount(t, ctx, conn, cp, cons, "caferwacctaaaa000001", leaseA)
	acctB := createAccount(t, ctx, conn, cp, cons, "caferwacctbbbb000001", leaseB)
	chargeA := postDebit(t, ctx, conn, cp, cons, "caferwdebitaaaa00001", acctA, 900, "Settled tab")
	chargeB := postDebit(t, ctx, conn, cp, cons, "caferwdebitbbbb00001", acctB, 900, "Settled tab")
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	refundAs(t, ctx, conn, cp, cons, "caferwrefundathome01",
		wcStaffKey, acctA, chargeA, 900, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferwrefundaway0001",
		wcStaffKey, acctB, chargeB, 900, "", processor.OutcomeRejected)
}

// TestRefundWorkplace_UnwiredStaffDeniedNotWidened covers the tombstone for the
// refund branch: a soft-deleted worksAt link hydrates as a DOCUMENT, not None,
// so unwiring a staffer must narrow their refund surface to nothing rather than
// widen it from one building to all of them.
func TestRefundWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundunwired")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := createAccount(t, ctx, conn, cp, cons, "caferwuwacctaaa00001", leaseA)
	acctB := createAccount(t, ctx, conn, cp, cons, "caferwuwacctbbb00001", leaseB)
	chargeA := postDebit(t, ctx, conn, cp, cons, "caferwuwdebitaaa0001", acctA, 900, "Settled tab")
	chargeB := postDebit(t, ctx, conn, cp, cons, "caferwuwdebitbbb0001", acctB, 900, "Settled tab")
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	tombstoneWorksAt(t, ctx, conn)

	refundAs(t, ctx, conn, cp, cons, "caferwuwrefundhome01",
		wcStaffKey, acctA, chargeA, 900, "", processor.OutcomeRejected)
	refundAs(t, ctx, conn, cp, cons, "caferwuwrefundaway01",
		wcStaffKey, acctB, chargeB, 900, "", processor.OutcomeRejected)
}

// TestRefundWorkplace_OperatorUnconfined proves the refund branch exempts root
// by the holdsRole LINK, the same way the payment branch does: the operator
// actor worksAt nowhere and refunds at both buildings.
func TestRefundWorkplace_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundoperator")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := createAccount(t, ctx, conn, cp, cons, "caferwopacctaaa00001", leaseA)
	acctB := createAccount(t, ctx, conn, cp, cons, "caferwopacctbbb00001", leaseB)
	chargeA := postDebit(t, ctx, conn, cp, cons, "caferwopdebitaaa0001", acctA, 900, "Settled tab")
	chargeB := postDebit(t, ctx, conn, cp, cons, "caferwopdebitbbb0001", acctB, 900, "Settled tab")

	refundAs(t, ctx, conn, cp, cons, "caferwoprefundaaa001",
		ledgerActorKey, acctA, chargeA, 900, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferwoprefundbbb001",
		ledgerActorKey, acctB, chargeB, 900, "", processor.OutcomeAccepted)
}

// TestCreditWorkplace_UnlocatableAccountIsOperatorOnly pins the fail-closed
// end of the resolver. An account whose lease names no unit resolves to an
// empty candidate list, and require_workplace treats that as a denial for
// anyone but an operator — an unwired topology must not fall open.
func TestCreditWorkplace_UnlocatableAccountIsOperatorOnly(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditunlocatable")

	seedWorkplaceTopology(t, ctx, conn)
	orphanLease := seedLease(t, ctx, conn, "BBCAFELWCRPHANLEASEZ")
	orphanAcct := seedChargedAccount(t, ctx, conn, cp, cons, "cafewcorphanacct0001", "cafewcorphandebit001", orphanLease, 1850)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	creditAs(t, ctx, conn, cp, cons, "cafewcorphanstaff001",
		wcStaffKey, orphanAcct, "", processor.OutcomeRejected)
	creditAs(t, ctx, conn, cp, cons, "cafewcorphanroot0001",
		ledgerActorKey, orphanAcct, "", processor.OutcomeAccepted)
}
