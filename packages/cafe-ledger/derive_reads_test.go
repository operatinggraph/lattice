// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// four transactionDDL ops (DebitAccount, CreditCafeAccount, RefundCafeCharge,
// PayoutCafeCredit) and the accountDDL's EvaluateCafeArrears — each envelope
// below declares no Reads and no OptionalReads, proving the script's own
// derive_reads(op) is what hydrates the account (its .balance/.arrears, and
// for RefundCafeCharge, reversesRef + its .entry) rather than a caller's own
// declaration. DebitAccount and EvaluateCafeArrears run confine=False/
// actor-restricted paths and so carry no ContextHint at all;
// CreditCafeAccount/PayoutCafeCredit/RefundCafeCharge call post_entry with
// confine=True, which unconditionally walks the actor's own holdsRole
// enumeration (workplace_exempt -> actor_holds_operator) — Contract #2's
// orthogonal class (e) channel, always caller-declared and never something
// derive_reads returns — so those three vectors declare it; RefundCafeCharge
// declares a second Enumeration too, reversed_charge's own unconditional
// postedTo-out proof that reversesRef belongs to this account.
package cafeledger_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestDebitAccount_UndeclaredSubmitter_PostsCharge: a bare DebitAccount posts
// the charge and updates .balance. derive_reads' own optionalReads carries the
// account root, so post_entry's vertex_alive(state, acct_key) sees the live
// account rather than misreading an undeclared root as absent.
func TestDebitAccount_UndeclaredSubmitter_PostsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEUNDCLLEASEHJK1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafenodeclacctcreate", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafedebitnodeclsubmt"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500,"memo":"Settled tab"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 500 {
		t.Fatalf("balance = %v, want 500 — the derivation must hydrate the account root for the charge to post at all", got)
	}
}

// TestCreditCafeAccount_UndeclaredSubmitter_PostsPayment: a CreditCafeAccount
// declaring only the mandatory operator-role enumeration (no Reads, no
// OptionalReads) against an account that already owes money still posts the
// payment and reduces .balance — the same undeclared-root failure mode
// DebitAccount's vector proves, on the credit leg.
func TestCreditCafeAccount_UndeclaredSubmitter_PostsPayment(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEUNDCLLEASEHJL1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafenodeclacctcredit", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafenodeclcreditchrg", acctKey, "2026-07-01T13:00:00Z", 1000)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafecreditnodeclsubm"),
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T14:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":400,"memo":"House tab payment"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 600 {
		t.Fatalf("balance = %v, want 600 (1000 charged - 400 paid) — the payment must actually post", got)
	}
}

// TestPayoutCafeCredit_UndeclaredSubmitter_PaysOutCredit: a PayoutCafeCredit
// declaring only the mandatory operator-role enumeration against an account
// holding credit still pays it out and brings .balance back toward zero. The
// credit is set up by paying a charge in full and then refunding it (the one
// way this ledger's balance goes negative — a plain payment is capped at what
// is owed, scripts.go), through the package's own fully-declared refundAs
// helper: only the vector under test declares just the enumeration.
func TestPayoutCafeCredit_UndeclaredSubmitter_PaysOutCredit(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEUNDCLLEASEHJM1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafenodeclacctpayout", leaseKey)
	chargeKey := debitAt(t, ctx, conn, cp, cons, "cafenodeclpayoutchrg", acctKey, "2026-07-01T13:00:00Z", 500)
	creditAt(t, ctx, conn, cp, cons, "cafenodeclpayoutpaid", acctKey, "2026-07-01T13:30:00Z", 500)
	// Balance is 0, cashCents 500. Refunding the (now paid) charge takes the
	// account 400 cents into credit.
	refundAs(t, ctx, conn, cp, cons, "cafenodeclpayoutrfnd", ledgerActorKey, acctKey, chargeKey, 400, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -400 {
		t.Fatalf("setup: balance = %v, want -400 (a 400-cent credit) before the payout vector runs", got)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafepayoutnodeclsubm"),
		Lane:          processor.LaneDefault,
		OperationType: "PayoutCafeCredit",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T14:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":300,"memo":"Paid from till"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != -100 {
		t.Fatalf("balance = %v, want -100 (-400 credit + 300 paid out) — the payout must actually post", got)
	}
}

// TestRefundCafeCharge_UndeclaredSubmitter_PostsRefund: a RefundCafeCharge
// declaring only the two Enumerations post_entry always walks for this op
// (the mandatory operator-role confinement probe, and reversed_charge's own
// postedTo-out proof that reversesRef is posted to this account) — no Reads,
// no OptionalReads — posts the refund and advances the charge's own
// refundedCents tally. derive_reads carries the account root/.balance/
// .arrears AND reversesRef/its .entry, so reversed_charge's reverses_key/
// entry_key state[...] checks see the live charge rather than misreading an
// undeclared key as absent, and the entry's refundedCents CAS is conditioned
// on the hydrated revision for this submitter exactly as it is for one that
// declared everything by hand.
func TestRefundCafeCharge_UndeclaredSubmitter_PostsRefund(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEUNDCLLEASEHJN1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafenodeclacctrefund", leaseKey)
	chargeKey := debitAt(t, ctx, conn, cp, cons, "cafenodeclrefundchrg", acctKey, "2026-07-01T13:00:00Z", 500)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("caferefundnodeclsubm"),
		Lane:          processor.LaneDefault,
		OperationType: "RefundCafeCharge",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T14:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","reversesRef":"` + chargeKey + `","amountCents":200,"memo":"Spilled"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: chargeKey, Relation: "postedTo", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 300 {
		t.Fatalf("balance = %v, want 300 (500 charged - 200 refunded) — the refund must actually post", got)
	}
	tally := chargeTally(t, ctx, conn, chargeKey)
	got, _ := tally["refundedCents"].(float64)
	if got != 200 {
		t.Fatalf("charge %s .entry.refundedCents = %v, want 200 — the derivation must hydrate reversesRef and its .entry for the CAS'd tally to advance at all", chargeKey, got)
	}
}

// TestEvaluateCafeArrears_UndeclaredSubmitter_AgesAccount: an
// EvaluateCafeArrears, submitted by Weaver's own dispatch actor declaring only
// the two Enumerations the evaluation always walks (postedTo, to replay the
// account's history, and heldFor, to resolve the notification's lease) and no
// Reads/OptionalReads, ages the account and writes .arrears. This DDL's own
// derive_reads carries the account root alongside .arrears, so vertex_alive
// sees the live account rather than misreading an undeclared root as absent.
func TestEvaluateCafeArrears_UndeclaredSubmitter_AgesAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEUNDCLLEASEHJP1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafenodeclacctarrear", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafenodeclarrearchrg", acctKey, "2026-07-01T00:00:00Z", 500)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafearrearsnodeclsub"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateCafeArrears",
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
}
