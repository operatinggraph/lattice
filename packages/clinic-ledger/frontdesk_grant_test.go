package clinicledger_test

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
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
)

// Front-desk unconfined grant for ClinicCreateAccount, ClinicDebitAccount,
// and ClinicCreditAccount (verticals.md — "The clinic's Billing panel
// AuthDenies every hat that can reach it"): clinic-app's Billing panel ships
// a Record charge/payment form to the front desk alongside the patient, and
// all three ops now grant frontOfHouse to match. A clinicaccount is anchored
// on a patient, which carries no building, so there is nothing left to
// workplace-confine — mirrors wellness-ledger's WellnessCreateAccount fix,
// cafe-ledger's CreditCafeAccount, and clinic-domain's
// TestFrontDesk_RegisterPatientUnconfined.
const (
	ledFDActorID  = "CLLEDFDACTRHJKMNPQRS"
	ledFDActorKey = "vtx.identity." + ledFDActorID
	ledFDCapKey   = "cap.identity." + ledFDActorID

	ledNoGrantActorID  = "CLLEDNGRANTHJKMNPQRS"
	ledNoGrantActorKey = "vtx.identity." + ledNoGrantActorID
)

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
			{OperationType: "ClinicCreateAccount", Scope: "any"},
			{OperationType: "ClinicDebitAccount", Scope: "any"},
			{OperationType: "ClinicCreditAccount", Scope: "any"},
			{OperationType: "CreatePatient", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestFrontDesk_ClinicCreateAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the ClinicCreateAccount grant (no operator
// role) can open a patient's ledger account — the call clinic-app's billing
// view needs before it can show anything but "no account yet".
func TestFrontDesk_ClinicCreateAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledger")

	patientReqID := testutil.GenReqID("fdledgerpatient00001")
	patientEnv := &processor.OperationEnvelope{
		RequestID:     patientReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreatePatient",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "patient",
		Payload:       json.RawMessage(`{"fullName":"Front Desk Test Patient"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: testutil.DeclaredEnumerations("CreatePatient", ledFDActorKey, clinicdomain.OpMetas())},
	}
	testutil.PublishOp(t, conn, patientEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + nanoIDFromRequestID(patientReqID)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdledgeracct0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk ClinicCreateAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_ClinicCreateAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_ClinicCreateAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdenied")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdlnograntpatient001", "Denied Patient")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdlnograntacct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("ClinicCreateAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestFrontDesk_ClinicDebitAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the ClinicDebitAccount grant (no operator
// role) can record a charge against a patient's ledger account — the call
// clinic-app's Billing panel needs so the desk can post a copay or invoice
// line, not just open the account.
func TestFrontDesk_ClinicDebitAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdebit")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fddebitpatient00001", "Front Desk Debit Patient")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fddebitacct0000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fddebittx000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk ClinicDebitAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_ClinicDebitAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_ClinicDebitAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdebitdenied")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdnogrntdebitpat001", "Denied Debit Patient")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdnogrntdebitacc001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdnogrntdebittx0001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("ClinicDebitAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestFrontDesk_ClinicCreditAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the ClinicCreditAccount grant (no operator
// role) can record a payment against a patient's ledger account — the call
// that settles a patient's balance over the counter, e.g. a no-show fee she
// cannot record herself.
func TestFrontDesk_ClinicCreditAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgercredit")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdcreditpatient0001", "Front Desk Credit Patient")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdcreditacct000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdcredittx00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk ClinicCreditAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_ClinicCreditAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_ClinicCreditAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgercreditdenied")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdnogrntcreditpat01", "Denied Credit Patient")
	acctKey := createAccount(t, ctx, conn, cp, cons, "fdnogrntcreditacc01", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdnogrntcredittx001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("ClinicCreditAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// Self-clearing — the staff leg's other half. The frontOfHouse grant proves
// STANDING and nothing about ownership, so a staffer who is also a patient
// passes it against their own account and could forgive what the clinic is
// owed to themselves. post_entry's require_not_own_account resolves the
// account's holder off its own heldFor patient's identifiedBy link and
// refuses a waiver (and a reversesRef reversal) when that holder is the actor.
const ledFDOwnPatientID = "CLLEDFDSELFPATNTHJKM"

// seedOwnChargedAccount opens an account for a patient identified by the
// front-desk actor and posts one operator charge of 2500 to it.
func seedOwnChargedAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, prefix string) string {
	t.Helper()
	patientKey := seedPatientWithIdentity(t, ctx, conn, ledFDOwnPatientID, ledFDActorID)
	acctKey := createAccount(t, ctx, conn, cp, cons, prefix+"acct", patientKey)
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(prefix + "debit"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return acctKey
}

// staffCreditEnv is one staff-voice ClinicCreditAccount against acctKey with
// the given payload fields spliced in after accountKey.
func staffCreditEnv(label, actorKey, acctKey, fields string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `",` + fields + `}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
}

// TestFrontDesk_SelfClearing_WaiverRefusedOnOwnAccount: the front-desk actor,
// identified as the account's patient, is refused writing their own balance
// off; a payment of the same balance from the same actor is accepted straight
// after — a payment is money coming in, not a clearing verb. The refusal is
// read from the reply, since every denial collapses to "rejected".
func TestFrontDesk_SelfClearing_WaiverRefusedOnOwnAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdselfclearing")
	acctKey := seedOwnChargedAccount(t, ctx, conn, cp, cons, "fdscown")

	assertRejectedBecause(t, ctx, conn, cp, cons,
		staffCreditEnv("fdscownwaiver0000001", ledFDActorKey, acctKey, `"amountCents":2500,"reason":"waiver"`),
		"SelfClearing: a staffer may not forgive their own account")
	if got := balanceCents(t, ctx, conn, acctKey); got != 2500 {
		t.Fatalf("balance after the refused write-off = %v, want the untouched 2500", got)
	}
	testutil.PublishOp(t, conn,
		staffCreditEnv("fdscownpayment000001", ledFDActorKey, acctKey, `"amountCents":2500,"memo":"Front desk payment"`))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestFrontDesk_SelfClearing_OtherPatientAccepted is the accepting half the
// refusal is measured against: the SAME front-desk actor writes off another
// patient's account — a guard that refused every front-desk waiver would pass
// the vector above and fail this one.
func TestFrontDesk_SelfClearing_OtherPatientAccepted(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdselfclearingother")
	seedOwnChargedAccount(t, ctx, conn, cp, cons, "fdscoth")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdscotherpatient001", "Some Other Patient")
	otherAcct := createAccount(t, ctx, conn, cp, cons, "fdscotheracct000001", patientKey)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdscotherdebit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + otherAcct + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{otherAcct, otherAcct + ".balance"}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn,
		staffCreditEnv("fdscotherwaiver00001", ledFDActorKey, otherAcct, `"amountCents":2500,"reason":"waiver"`))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, otherAcct); got != 0 {
		t.Fatalf("balance after the write-off of another patient's account = %v, want 0", got)
	}
}

// postOtherPatientCharge opens an account for a fresh patient (no
// identifiedBy link) and posts one operator charge of 2500 to it, returning
// the account and the charge.
func postOtherPatientCharge(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, prefix string) (string, string) {
	t.Helper()
	patientKey := createPatient(t, ctx, conn, cp, cons, prefix+"patient", "Some Other Patient")
	acctKey := createAccount(t, ctx, conn, cp, cons, prefix+"acct", patientKey)
	reqID := testutil.GenReqID(prefix + "debit")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"No-show fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return acctKey, "vtx.clinictransaction." + nanoIDFromRequestID(reqID)
}

// reversalEnv is a staff-voice ClinicCreditAccount that names the charge it
// reverses (reason "payment" — the reversesRef alone selects the check), with
// the postedTo link the script proves the charge is this account's declared
// as the descriptor does.
func reversalEnv(label, acctKey, chargeKey string) *processor.OperationEnvelope {
	env := staffCreditEnv(label, ledFDActorKey, acctKey, `"amountCents":2500,"reversesRef":"`+chargeKey+`"`)
	env.ContextHint.Reads = append(env.ContextHint.Reads, chargeKey)
	env.ContextHint.OptionalReads = []string{"lnk.clinictransaction." + chargeKey[len("vtx.clinictransaction."):] + ".postedTo.clinicaccount." + acctKey[len("vtx.clinicaccount."):]}
	return env
}

// TestFrontDesk_SelfClearing_ReversalRefusedOnOwnAccount pins the reversesRef
// leg on its own: the front-desk actor, identified as the account's patient,
// is refused reversing a charge on their own account even with the default
// "payment" reason — a reversal gives the charge back, so it is a clearing
// verb whatever the reason says — and the same reversal on another patient's
// account is accepted.
func TestFrontDesk_SelfClearing_ReversalRefusedOnOwnAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdselfclearingrev")

	patientKey := seedPatientWithIdentity(t, ctx, conn, ledFDOwnPatientID, ledFDActorID)
	ownAcct := createAccount(t, ctx, conn, cp, cons, "fdscrevownacct000001", patientKey)
	ownReq := testutil.GenReqID("fdscrevowndebit00001")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     ownReq,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + ownAcct + `","amountCents":2500,"memo":"No-show fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{ownAcct, ownAcct + ".balance"}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	ownCharge := "vtx.clinictransaction." + nanoIDFromRequestID(ownReq)

	assertRejectedBecause(t, ctx, conn, cp, cons,
		reversalEnv("fdscrevownrev0000001", ownAcct, ownCharge),
		"SelfClearing: a staffer may not reverse a charge on their own account")
	if got := balanceCents(t, ctx, conn, ownAcct); got != 2500 {
		t.Fatalf("balance after the refused reversal = %v, want the untouched 2500", got)
	}

	otherAcct, otherCharge := postOtherPatientCharge(t, ctx, conn, cp, cons, "fdscrevoth")
	testutil.PublishOp(t, conn, reversalEnv("fdscrevotherrev00001", otherAcct, otherCharge))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, otherAcct); got != 0 {
		t.Fatalf("balance after reversing another patient's charge = %v, want 0", got)
	}
}
