// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// transactionDDL's ClinicDebitAccount and ClinicCreditAccount and the
// accountDDL's EvaluateClinicArrears — each envelope below declares no reads
// at all, proving the script's own derive_reads(op) is what hydrates the
// account root (and its .balance / .arrears) rather than a caller's own
// declaration. Neither entry op runs a confinement walk (post_entry's own doc
// comment: no workplace check here, unlike cafe-ledger's staff-voice ops), so
// no Enumerations declaration is needed for those two; the evaluation always
// walks postedTo and heldFor, and only a read can be derived, so its vector
// declares exactly those two walks and nothing else.
package clinicledger_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestClinicDebitAccount_UndeclaredSubmitter_PostsCharge: a bare
// ClinicDebitAccount posts the charge and updates .balance. derive_reads' own
// optionalReads carries the account root, so post_entry's vertex_alive(state,
// acct_key) sees the live account rather than misreading an undeclared root
// as absent.
func TestClinicDebitAccount_UndeclaredSubmitter_PostsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "cldebitnodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "clndclpat00000001", "Nora Declan")
	acctKey := createAccount(t, ctx, conn, cp, cons, "clndclacct0000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clndcldebitsubmit01"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500,"memo":"No-show fee"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 500 {
		t.Fatalf("balance = %v, want 500 — the derivation must hydrate the account root for the charge to post at all", got)
	}
}

// TestClinicCreditAccount_UndeclaredSubmitter_PostsPayment: a bare
// ClinicCreditAccount against an account that already owes money still posts
// the payment and reduces .balance — the same undeclared-root failure mode
// ClinicDebitAccount's vector proves, on the credit leg.
func TestClinicCreditAccount_UndeclaredSubmitter_PostsPayment(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "clcreditnodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "clndclpat00000002", "Reed Sloane")
	acctKey := createAccount(t, ctx, conn, cp, cons, "clndclacct0000002", patientKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clndclcreditchrg001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000,"memo":"No-show fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}, OptionalReads: []string{acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clndclcreditsubmit1"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T14:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":400,"memo":"Payment"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 600 {
		t.Fatalf("balance = %v, want 600 (1000 charged - 400 paid) — the payment must actually post", got)
	}
}

// TestEvaluateClinicArrears_UndeclaredSubmitter_AgesAccount: an
// EvaluateClinicArrears, submitted by Weaver's own dispatch actor declaring
// only the two Enumerations the evaluation always walks (postedTo, to replay
// the account's history, and heldFor, to resolve the notification's patient)
// and no Reads/OptionalReads, ages the account and writes .arrears. This DDL's
// own derive_reads carries the account root alongside .arrears, so
// vertex_alive sees the live account rather than misreading an undeclared
// root as absent.
func TestEvaluateClinicArrears_UndeclaredSubmitter_AgesAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnodecl")

	patientKey := createPatient(t, ctx, conn, cp, cons, "clndclpat00000003", "Riley Chen")
	acctKey := createAccount(t, ctx, conn, cp, cons, "clndclacct0000003", patientKey)
	debitAt(t, ctx, conn, cp, cons, "clndclarrearschrg001", acctKey, "2026-07-01T00:00:00Z", 2500)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clarrearsnodeclsub01"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateClinicArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-01T00:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
				{Hub: acctKey, Relation: "heldFor", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatalf("no .arrears aspect written — the derivation must hydrate the account root for the evaluation to run at all")
	}
	if got, _ := data["remindedFor"].(string); got != dueFor(t, "2026-07-01T00:00:00Z") {
		t.Fatalf("remindedFor = %q, want %q — the evaluation must see and rewrite the hydrated aspect", got, dueFor(t, "2026-07-01T00:00:00Z"))
	}
}
