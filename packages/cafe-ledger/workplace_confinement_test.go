package cafeledger_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
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
			{OperationType: "PayoutCafeCredit", Scope: "any"},
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

// TestCreditWorkplace_CounterPaymentTabMustBeHeldByTheAccountsLease: the
// counter payment's tab must belong to the account it is credited to. The
// staffer is frontOfHouse at building A and credits account A — the
// confinement walk passes — but names a settled, paid tab of lease B. Without
// the heldFor tie, lease B's resident's cash would land as credit on lease
// A's account; the tie refuses it AuthDenied off the account's OWN heldFor
// lease, never the payload. The same staffer's counter payment for lease A's
// own tab posts first, so the refusal is the tie and not the workplace.
func TestCreditWorkplace_CounterPaymentTabMustBeHeldByTheAccountsLease(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditwctabtie")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := createAccount(t, ctx, conn, cp, cons, "cafewctieaccta000001", leaseA)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	tabA := seedSettledTab(t, ctx, conn, "BBCAFEWCTABAHJKMNPQR", leaseA, 1850, 1850, "settled")
	tabB := seedSettledTab(t, ctx, conn, "BBCAFEWCTABBHJKMNPQR", leaseB, 1850, 1850, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafewctiedebita00001", acctA, tabA, 1850)

	own, _ := counterPaymentEnv("cafewctiecredita0001", wcStaffKey, acctA, tabA, 1850, "payment")
	testutil.PublishOp(t, conn, own)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	other, _ := counterPaymentEnv("cafewctiecreditb0001", wcStaffKey, acctA, tabB, 1850, "payment")
	assertRejectedBecause(t, ctx, conn, cp, cons, other,
		"AuthDenied: tab "+tabB+" is not held by this account's lease")
	if got := balanceCents(t, ctx, conn, acctA); got != 0 {
		t.Fatalf("balance after the refused cross-lease counter payment = %v, want the untouched 0", got)
	}
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

// TestPayoutWorkplace_StaffConfinedToWorkplace is the confinement pair run for
// PayoutCafeCredit, and it is not redundant with the payment's or the refund's:
// the payout reaches post_entry's require_workplace site through its own
// execute() branch, so a payout branch that forgot confine=True would leave a
// front-desk staffer handing out cash against accounts at buildings across town
// while both sibling vectors stayed green. Each account is driven into credit
// as the operator first — the payout question under test is the workplace's,
// not the fixture's. The accepted vector runs first: a rejection-only test would
// pass against a guard that denied everyone.
func TestPayoutWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutworkplace")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := seedCreditAccount(t, ctx, conn, cp, cons, "cafepwaaaa", leaseA, 900)
	acctB := seedCreditAccount(t, ctx, conn, cp, cons, "cafepwbbbb", leaseB, 900)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	payoutAs(t, ctx, conn, cp, cons, "cafepwpayoutathome",
		wcStaffKey, acctA, 900, processor.OutcomeAccepted)
	payoutAs(t, ctx, conn, cp, cons, "cafepwpayoutaway",
		wcStaffKey, acctB, 900, processor.OutcomeRejected)
	if got := balanceCents(t, ctx, conn, acctB); got != -900 {
		t.Fatalf("the away account's credit = %v, want the untouched -900", got)
	}
}

// TestPayoutWorkplace_UnwiredStaffDeniedNotWidened covers the tombstone for the
// payout branch: a soft-deleted worksAt link hydrates as a DOCUMENT, not None,
// so unwiring a staffer must narrow their payout surface to nothing rather than
// widen it from one building to all of them.
func TestPayoutWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutunwired")

	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)
	acctA := seedCreditAccount(t, ctx, conn, cp, cons, "cafepwuwaa", leaseA, 900)
	acctB := seedCreditAccount(t, ctx, conn, cp, cons, "cafepwuwbb", leaseB, 900)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	tombstoneWorksAt(t, ctx, conn)

	payoutAs(t, ctx, conn, cp, cons, "cafepwuwpayouthome",
		wcStaffKey, acctA, 900, processor.OutcomeRejected)
	payoutAs(t, ctx, conn, cp, cons, "cafepwuwpayoutaway",
		wcStaffKey, acctB, 900, processor.OutcomeRejected)
}

// Self-clearing — the staff leg's other half. The workplace walk above proves
// STANDING: the caller works where the account's lease sits. A front-of-house
// staffer who also holds a lease at their own building passes it against their
// own account, and could forgive, refund or pay out what the house is owed to
// themselves. post_entry's require_not_own_account resolves the account's
// holder off its own heldFor lease and refuses the three clearing verbs when
// that holder is the actor.
const (
	scStaffAwayID  = "BBCAFELSCAWAYSTAFFHJ"
	scStaffAwayKey = "vtx.identity." + scStaffAwayID
	scUnitA2ID     = "BBCAFELSCUNTA2HJKMNP"
	scLeaseA2ID    = "BBCAFELSCLEASEA2HJKM"
	scResidentA2ID = "BBCAFELSCRESDA2HJKMN"
)

// seedSecondLeaseAtBuildingA adds a second unit + lease at building A, held by
// an ordinary resident (scResidentA2ID) rather than any staffer, so a vector
// can prove the refusal is about the ACTOR's own lease and not about the
// building.
func seedSecondLeaseAtBuildingA(t *testing.T, ctx context.Context, conn *substrate.Conn) string {
	t.Helper()
	unitKey := "vtx.unit." + scUnitA2ID
	seedVertex(t, ctx, conn, unitKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.unit."+scUnitA2ID+".containedIn.building."+wcBuildingAID,
		"containedIn", unitKey, wcBuildingAKey)
	seedIdentity(t, ctx, conn, scResidentA2ID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, scLeaseA2ID, scResidentA2ID)
	testutil.SeedLink(t, ctx, conn,
		"lnk.leaseapp."+scLeaseA2ID+".appliesToUnit.unit."+scUnitA2ID,
		"appliesToUnit", leaseKey, unitKey)
	return leaseKey
}

// holdLease records that identityID is the applicant of leaseKey — the
// applicationFor link the self leg's ownership proof and the staff leg's
// self-clearing refusal both read.
func holdLease(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, identityID string) {
	t.Helper()
	leaseID := leaseKey[len("vtx.leaseapp."):]
	seedLink(t, ctx, conn,
		"lnk.leaseapp."+leaseID+".applicationFor.identity."+identityID,
		leaseKey, "vtx.identity."+identityID, "applicationFor", "applicationFor")
}

// waiverEnvFor is creditEnvFor with reason: "waiver" — the staff-voice
// write-off, declaring exactly what the descriptor declares.
func waiverEnvFor(label, actorKey, acctKey string, amountCents int) *processor.OperationEnvelope {
	env, _ := creditEnvFor(label, actorKey, acctKey, amountCents)
	env.Payload = json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
		strconv.Itoa(amountCents) + `,"reason":"waiver","memo":"Written off"}`)
	return env
}

const (
	selfClearingForgive = "SelfClearing: a staffer may not forgive their own account"
	selfClearingRefund  = "SelfClearing: a staffer may not refund their own account"
	selfClearingPayout  = "SelfClearing: a staffer may not pay out their own account"
)

// seedOwnChargedAccount builds the self-clearing topology: the front-of-house
// staffer worksAt building A AND holds lease A, whose account carries one
// operator-posted charge of 1850. Returns the account and the charge.
func seedOwnChargedAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, prefix string) (string, string) {
	t.Helper()
	leaseA, _ := seedWorkplaceTopology(t, ctx, conn)
	holdLease(t, ctx, conn, leaseA, wcStaffID)
	acctOwn := createAccount(t, ctx, conn, cp, cons, prefix+"acct", leaseA)
	chargeOwn := postDebit(t, ctx, conn, cp, cons, prefix+"debit", acctOwn, 1850, "Settled tab")
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	return acctOwn, chargeOwn
}

// TestSelfClearing_WaiverRefusedOnOwnAccount: the lease-holding staffer's
// write-off of their own account is refused, and the counter payment of the
// same balance is accepted straight after — a payment is money coming IN, not
// a clearing verb. The refusal is read from the reply: the workplace walk
// passes here (home building), so an outcome-only assertion could not tell
// this rule from confinement. One test per verb, so each refusal is
// revert-provable on its own rather than shadowed by the one before it.
func TestSelfClearing_WaiverRefusedOnOwnAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingwaiver")
	acctOwn, _ := seedOwnChargedAccount(t, ctx, conn, cp, cons, "cafescwv")

	assertRejectedBecause(t, ctx, conn, cp, cons,
		waiverEnvFor("cafescwvwaiver000001", wcStaffKey, acctOwn, 1850), selfClearingForgive)
	if got := balanceCents(t, ctx, conn, acctOwn); got != 1850 {
		t.Fatalf("balance after the refused write-off = %v, want the untouched 1850", got)
	}
	creditAs(t, ctx, conn, cp, cons, "cafescwvpayment00001",
		wcStaffKey, acctOwn, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctOwn); got != 0 {
		t.Fatalf("balance after the staffer's own counter payment = %v, want 0", got)
	}
}

// TestSelfClearing_RefundRefusedOnOwnAccount: the same staffer, having paid
// their own charge in full (so the refund would otherwise be within the
// charge and the cash floor), is refused refunding it; the operator refunds
// it in their place.
func TestSelfClearing_RefundRefusedOnOwnAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingrefund")
	acctOwn, chargeOwn := seedOwnChargedAccount(t, ctx, conn, cp, cons, "cafescrf")
	creditAs(t, ctx, conn, cp, cons, "cafescrfpayment00001",
		wcStaffKey, acctOwn, "", processor.OutcomeAccepted)

	refund, _ := refundEnv("cafescrfrefund000001", wcStaffKey, acctOwn, chargeOwn, 1850, "")
	assertRejectedBecause(t, ctx, conn, cp, cons, refund, selfClearingRefund)
	if got := balanceCents(t, ctx, conn, acctOwn); got != 0 {
		t.Fatalf("balance after the refused refund = %v, want the untouched 0", got)
	}
	refundAs(t, ctx, conn, cp, cons, "cafescrfoprefund0001",
		ledgerActorKey, acctOwn, chargeOwn, 1850, "", processor.OutcomeAccepted)
}

// TestSelfClearing_PayoutRefusedOnOwnAccount: the same staffer's account is
// driven into credit the only way the ledger offers (charge, paid, refunded by
// the operator), and the staffer is refused paying that credit out to
// themselves.
func TestSelfClearing_PayoutRefusedOnOwnAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingpayout")
	acctOwn, chargeOwn := seedOwnChargedAccount(t, ctx, conn, cp, cons, "cafescpo")
	creditAs(t, ctx, conn, cp, cons, "cafescpopayment00001",
		wcStaffKey, acctOwn, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "cafescpooprefund0001",
		ledgerActorKey, acctOwn, chargeOwn, 1850, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctOwn); got != -1850 {
		t.Fatalf("balance after the operator's refund = %v, want -1850 (in credit)", got)
	}

	payout, _ := payoutEnv("cafescpopayout000001", wcStaffKey, acctOwn, payoutPayload(acctOwn, 1850), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, payout, selfClearingPayout)
	if got := balanceCents(t, ctx, conn, acctOwn); got != -1850 {
		t.Fatalf("balance after the refused payout = %v, want the untouched -1850", got)
	}
}

// TestSelfClearing_StaffClearsAnotherResidentsAccount is the accepting half
// the refusals above are measured against: the SAME lease-holding staffer,
// at the SAME building, writes off another resident's account. A guard that
// refused every write-off at a building where the staffer lives would pass
// the vector above and fail this one.
func TestSelfClearing_StaffClearsAnotherResidentsAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingother")

	leaseA, _ := seedWorkplaceTopology(t, ctx, conn)
	holdLease(t, ctx, conn, leaseA, wcStaffID)
	leaseA2 := seedSecondLeaseAtBuildingA(t, ctx, conn)
	acctOther := seedChargedAccount(t, ctx, conn, cp, cons, "cafescotheracct00001", "cafescotherdebit0001", leaseA2, 1850)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())

	testutil.PublishOp(t, conn, waiverEnvFor("cafescotherwaiver001", wcStaffKey, acctOther, 1850))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctOther); got != 0 {
		t.Fatalf("balance after the write-off of another resident's account = %v, want 0", got)
	}
}

// TestSelfClearing_OperatorNotExempt pins the one place root is not root: the
// operator worksAt nowhere and clears at every building, but an operator who
// holds a lease is a resident of it, and the house's money is no more theirs to
// forgive to themselves than a staffer's. The rule is about whose money it is,
// not whose standing.
func TestSelfClearing_OperatorNotExempt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingop")

	_, leaseB := seedWorkplaceTopology(t, ctx, conn)
	holdLease(t, ctx, conn, leaseB, ledgerActorID)
	acctB := seedChargedAccount(t, ctx, conn, cp, cons, "cafescopacct00000001", "cafescopdebit0000001", leaseB, 1850)

	assertRejectedBecause(t, ctx, conn, cp, cons,
		waiverEnvFor("cafescopwaiver000001", ledgerActorKey, acctB, 1850), selfClearingForgive)
	creditAs(t, ctx, conn, cp, cons, "cafescoppayment00001",
		ledgerActorKey, acctB, "", processor.OutcomeAccepted)
}

// TestSelfClearing_ConfinementRefusesFirst pins the order of the two staff-leg
// checks: a staffer who holds lease A but worksAt building B is refused the
// write-off of their own account on CONFINEMENT, and the refusal never names
// SelfClearing — standing is settled before ownership is asked, so the
// ownership walk is never spent on a caller with no standing at all.
func TestSelfClearing_ConfinementRefusesFirst(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfclearingaway")

	leaseA, _ := seedWorkplaceTopology(t, ctx, conn)
	seedVertex(t, ctx, conn, scStaffAwayKey, "identity", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+scStaffAwayID+".worksAt.building."+wcBuildingBID,
		"worksAt", scStaffAwayKey, wcBuildingBKey)
	holdLease(t, ctx, conn, leaseA, scStaffAwayID)
	acctA := seedChargedAccount(t, ctx, conn, cp, cons, "cafescawayacct000001", "cafescawaydebit00001", leaseA, 1850)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDocFor(scStaffAwayKey))

	env := waiverEnvFor("cafescawaywaiver0001", scStaffAwayKey, acctA, 1850)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "does not worksAt") {
		t.Fatalf("rejected with %+v, want the confinement refusal", reply.Error)
	}
	if strings.Contains(reply.Error.Message, "SelfClearing") {
		t.Fatalf("rejected with %+v, want confinement to refuse before the self-clearing walk runs", reply.Error)
	}
}
