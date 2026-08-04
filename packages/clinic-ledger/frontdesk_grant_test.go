package clinicledger_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
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
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
