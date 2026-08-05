package wellnessledger_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// Front-desk unconfined grant for WellnessCreateAccount, WellnessDebitAccount,
// and WellnessCreditAccount (verticals.md — "A member sees a balance nobody
// can settle"): wellness-app's Roster view ships a Record charge/payment form
// to the front desk, and all three ops now grant frontOfHouse to match. A
// wellnessaccount is anchored on a member identity, which carries no
// building, so there is nothing left to workplace-confine — mirrors
// clinic-ledger's identical ClinicCreateAccount/ClinicDebitAccount/
// ClinicCreditAccount fix, cafe-ledger's CreditCafeAccount, and
// clinic-domain's TestFrontDesk_RegisterPatientUnconfined.
const (
	ledFDActorID  = "WLLEDFDACTRHJKMNPQRS"
	ledFDActorKey = "vtx.identity." + ledFDActorID
	ledFDCapKey   = "cap.identity." + ledFDActorID

	ledNoGrantActorID  = "WLLEDNGRANTHJKMNPQRS"
	ledNoGrantActorKey = "vtx.identity." + ledNoGrantActorID

	// ledConsumerID stands in for identity-domain's real `consumer` role
	// grant flow (mirrors wellness-domain's domainConsumerID) — a real
	// member's own identity, self-opening their OWN ledger account.
	ledConsumerID  = "WLLEDCQNSUMERHJKMNPQ"
	ledConsumerKey = "vtx.identity." + ledConsumerID
	ledConsumerCap = "cap.identity." + ledConsumerID
)

// ledConsumerCapDoc grants the consumer role's scope=self WellnessCreateAccount
// permission — the real-actor-write-auth-e2e self-service caller, mirrors
// wellness-domain's domainConsumerCapDoc.
func ledConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledConsumerCap,
		Actor:                  ledConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "WellnessCreateAccount", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

func ledFDCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledFDCapKey,
		Actor:                  ledFDActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledFDActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "WellnessCreateAccount", Scope: "any"},
			{OperationType: "WellnessDebitAccount", Scope: "any"},
			{OperationType: "WellnessCreditAccount", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestFrontDesk_CreateAccountUnconfined is the Inc guarantee: a front-desk
// actor holding only the CreateAccount grant (no operator role) can open a
// member's ledger account — the call wellness-app's My Classes balance panel
// needs before it can show anything but "no charges yet".
func TestFrontDesk_CreateAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledger")

	identityKey := seedIdentity(t, ctx, conn, "WLFD23456789ABCDT1AB")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdledgeracct0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk CreateAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_CreateAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_CreateAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdenied")

	identityKey := seedIdentity(t, ctx, conn, "WLFD23456789ABCDT2AB")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdlnograntacct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("CreateAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestFrontDesk_WellnessDebitAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the WellnessDebitAccount grant (no operator
// role) can record a charge against a member's ledger account — the call
// wellness-app's Roster billing panel needs so the desk can post a fee, not
// just open the account.
func TestFrontDesk_WellnessDebitAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdebit")

	identityKey := seedIdentity(t, ctx, conn, "WLFDDBT23456789ABCDE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fddebitacct0000001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fddebittx000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk WellnessDebitAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_WellnessDebitAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_WellnessDebitAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdebitdenied")

	identityKey := seedIdentity(t, ctx, conn, "WLNGDBT23456789ABCDE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdnogrntdebitacc001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdnogrntdebittx0001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("WellnessDebitAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestFrontDesk_WellnessCreditAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the WellnessCreditAccount grant (no operator
// role) can record a payment against a member's ledger account — the call
// that settles a member's balance over the counter, e.g. a no-show fee they
// cannot record themselves.
func TestFrontDesk_WellnessCreditAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgercredit")

	identityKey := seedIdentity(t, ctx, conn, "WLFDCRD23456789ABCDE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdcreditacct000001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdcredittx00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk WellnessCreditAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_WellnessCreditAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_WellnessCreditAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgercreditdenied")

	identityKey := seedIdentity(t, ctx, conn, "WLNGCRD23456789ABCDE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdnogrntcreditacc01", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdnogrntcredittx001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("WellnessCreditAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestConsumer_CreateAccountSelfScopeAllowed proves a real member, holding
// only the consumer scope=self grant, can open THEIR OWN ledger account —
// most bookings are self-service (wellness-domain's CreateBooking self-scope
// precedent), so the account has to open at the same moment without a
// front-desk actor in the loop.
func TestConsumer_CreateAccountSelfScopeAllowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfledgerok")

	seedIdentity(t, ctx, conn, ledConsumerID)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("selfledgeracct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledConsumerKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + ledConsumerKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{ledConsumerKey}},
		AuthContext:   &processor.AuthContext{Target: ledConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("self-service WellnessCreateAccount outcome = %v, want Accepted", got)
	}
}

// TestConsumer_CreateAccountSelfScopeRejectedForOther proves the Starlark
// guard closes the gap step 3 leaves open: step 3's scope=self only checks
// authContext.target == actor, never looks at payload.identityKey. A
// consumer satisfying that check but naming a DIFFERENT identity must be
// rejected — self-service never lets one member open another's account.
func TestConsumer_CreateAccountSelfScopeRejectedForOther(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfledgerother")

	seedIdentity(t, ctx, conn, ledConsumerID)
	otherIdentityKey := seedIdentity(t, ctx, conn, "WLLEDCQNSOTHERHJKMNP")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("selfledgeracct00002"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledConsumerKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + otherIdentityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{otherIdentityKey}},
		AuthContext:   &processor.AuthContext{Target: ledConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("self-service WellnessCreateAccount for another identity outcome = %v, want Rejected (AuthDenied)", got)
	}
}
