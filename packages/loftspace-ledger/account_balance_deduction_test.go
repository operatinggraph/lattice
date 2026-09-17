package loftspaceledger_test

// The negative vectors for Decision 1 ("every balance reader ignores a
// deduction") at the account_balance_cents layer: the resident's capped
// self-credit and PayOutBalance's own computed amount must both be
// unaffected by a "deduction" row sitting in the account's postedTo
// history — account_balance_cents' own explicit type test (debit/credit,
// nothing else) already excludes it, and these tests are what would go red
// if that exclusion were ever weakened to an else-branch that folded any
// non-debit type in as a credit (or vice versa).

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestCreditAccount_ResidentSelfCredit_IgnoresDeductionInHistory proves a
// deduction sitting in the account's own history moves neither direction of
// the resident's self-credit cap: a deduction wrongly read as a credit would
// zero out (or invert) what is owed and refuse a legitimate payment; wrongly
// read as a debit it would inflate the cap far past what is actually owed.
func TestCreditAccount_ResidentSelfCredit_IgnoresDeductionInHistory(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "residentcreditdeduct")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBRESDEDLEASEHJKMNPQ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "resdedacctsetup00001", leaseKey)

	// owed = 100000 (one debit). A deduction of 999999999 sitting in the same
	// postedTo history must not move the cap in either direction.
	seedEntryAt(t, ctx, conn, acctKey, "BBRESDEDDEBJTHJKMNPQ", "debit", 100000, "2026-07-01T00:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, "BBRESDEDDEDUCTHJKMNP", "deduction", 999999999, "2026-07-02T00:00:00Z", "")

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "resdedcreditok0000001", "CreditAccount", ledgerSelfConsumerKey, acctKey, 100000)
	if got != processor.OutcomeAccepted {
		t.Fatalf("self-credit of exactly what is owed (100000) = %v (%+v), want accepted — a deduction in history must not move the cap", got, reply.Error)
	}

	got2, reply2 := submitSelfEntry(t, ctx, conn, cp, cons, "resdedcreditover00001", "CreditAccount", ledgerSelfConsumerKey, acctKey, 1)
	if got2 != processor.OutcomeRejected {
		t.Fatalf("a self-credit once the balance is already paid down to zero = %v, want rejected", got2)
	}
	requireRefusal(t, reply2, "NoBalanceToPay")
}

// TestPayOutBalance_IgnoresDeductionInHistory proves the credit balance
// PayOutBalance computes and pays out is unaffected by a deduction row
// sitting in the same account's history.
func TestPayOutBalance_IgnoresDeductionInHistory(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutdeductignore")

	leaseKey := seedLease(t, ctx, conn, "BBPAYQUTDEDLEASEHJKM")
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBPAYQUTDEDACCTHJKMN", leaseKey)
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTDEDCREDJTHJK", "credit", 250000, "2027-07-02T09:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTDEDDEDUCTHJK", "deduction", 999999999, "2027-07-01T09:00:00Z", "")

	reply, txKey := submitPayOutBalance(t, ctx, conn, cp, cons, "payoutdedok000000001", ledgerActorKey,
		acctKey, leaseKey, payOutHint(acctKey, leaseKey), nil, processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("payout: %+v", reply.Error)
	}
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 250000 {
		t.Fatalf("payout amountCents = %v, want 250000 — the deduction's 999999999 in history must not move the computed balance", got)
	}
}
