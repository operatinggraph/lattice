package loftspaceledger_test

// The race between RecordDepositDeduction and ReturnDeposit. RecordDepositDeduction
// reads the clause's .status and writes this package's own .deductions;
// ReturnDeposit reads .deductions and writes .status. With no shared written
// key the commit path's per-key OCC could not serialize them — a
// boundary-time deduction would land on an already-returned clause, or a
// return would credit a total a concurrent deduction was mid-flight on — so
// ReturnDeposit also writes .deductions (scripts.go), the shared
// serialization anchor. These tests drive both ops CONCURRENTLY — both
// hydrated before either commits — the packages/cafe-ledger
// driveConcurrently idiom, since a serial DriveOne never puts two ops on the
// same hydrated revision. On this host the deduction commits first in every
// run (the return's retry re-hydrates and nets); the return-wins
// interleaving (the deduction's retry re-hydrates and refuses
// DepositNotHeld) follows from the same conditioned keys but is not forced
// by this harness.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// driveConcurrently fetches n pending operations and runs them through the
// commit path SIMULTANEOUSLY, returning their outcomes in FETCH order (which
// tracks publish order for a single durable consumer, as every call site
// here relies on). Only overlapping hydrations put two ops on the same
// revision of a shared key — the race a live front desk and a live
// leaseRentSettlement dispatch can actually produce.
func driveConcurrently(t *testing.T, ctx context.Context, cp *processor.CommitPath,
	cons jetstream.Consumer, n int) []processor.MessageOutcome {
	t.Helper()
	batch, err := cons.Fetch(n, jetstream.FetchMaxWait(10*time.Second))
	if err != nil {
		t.Fatalf("Fetch(%d): %v", n, err)
	}
	var msgs []jetstream.Msg
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("Fetch batch error: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("fetched %d messages, want %d", len(msgs), n)
	}

	outcomes := make([]processor.MessageOutcome, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i, m := range msgs {
		wg.Add(1)
		go func(i int, m jetstream.Msg) {
			defer wg.Done()
			<-release
			outcomes[i] = cp.HandleMessage(ctx, m)
		}(i, m)
	}
	close(release)
	wg.Wait()
	return outcomes
}

// TestRecordDepositDeductionReturnDeposit_Race proves the two ops serialize
// through their shared .deductions key however they interleave: whichever
// wins the race, the other re-hydrates and re-executes against the fresh
// result rather than committing independently against a stale read.
//
//   - RecordDepositDeduction wins: it creates .deductions. ReturnDeposit's
//     own create of .deductions (it hydrated the key absent too) conflicts,
//     re-hydrates, and re-executes as a bare UPDATE against the now-recorded
//     total — the credit it posts is the NET, never the full deposit.
//   - ReturnDeposit wins: it creates .deductions = {totalCents: 0} and moves
//     .status to returned, crediting the FULL deposit (nothing was deducted
//     yet). RecordDepositDeduction's own create of .deductions conflicts,
//     re-hydrates, and re-executes against the now-"returned" .status —
//     DepositNotHeld refuses it. The deduction never lands on a returned
//     clause, and the tenant is never both credited in full AND charged a
//     deduction that would exceed what was actually held.
//
// Either way, the invariant this test proves is a conservation law: the
// credit posted plus whatever landed on .deductions always equals the
// clause's own full amountCents — never more (double-spend), never less
// (money vanishing).
func TestRecordDepositDeductionReturnDeposit_Race(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "depositdeductreturnrace")

	f := seedDepositFixture(t, ctx, conn, cp, cons, "bbracedepacct0000001", "BBRACEDEPLEASEHJKMNP", "BBRACEDEPCLAUSEHJKMN")

	const deductAmount = 60000
	deductReqID := testutil.GenReqID("bbracededuct0000001")
	deductEnv := &processor.OperationEnvelope{
		RequestID:     deductReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordDepositDeduction",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		Class:         "transaction",
		Payload:       deductionPayload(f.acctKey, f.clauseKey, deductAmount, "Carpet cleaning"),
		ContextHint:   deductionHint(f.acctKey, f.clauseKey),
	}
	returnReqID := testutil.GenReqID("bbracereturn0000001")
	returnEnv := &processor.OperationEnvelope{
		RequestID:     returnReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReturnDeposit",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		Class:         "transaction",
		Payload:       returnDepositPayload(f.leaseKey, f.clauseKey, f.acctKey),
		ContextHint:   returnDepositHint(f),
	}
	testutil.PublishOp(t, conn, deductEnv)
	testutil.PublishOp(t, conn, returnEnv)

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	deductionOutcome, returnOutcome := outcomes[0], outcomes[1]

	if returnOutcome != processor.OutcomeAccepted {
		t.Fatalf("ReturnDeposit outcome = %v, want accepted — nothing about a concurrent deduction touches its own preconditions", returnOutcome)
	}
	if deductionOutcome != processor.OutcomeAccepted && deductionOutcome != processor.OutcomeRejected {
		t.Fatalf("RecordDepositDeduction outcome = %v, want accepted or rejected", deductionOutcome)
	}

	deductionTxKey := "vtx.transaction." + nanoIDFromRequestID(deductReqID)
	returnTxKey := "vtx.transaction." + nanoIDFromRequestID(returnReqID)

	deductionsTotal := deductionsTotalCents(t, ctx, conn, f.clauseKey)
	returnEntry, _ := readDoc(t, ctx, conn, returnTxKey+".entry")["data"].(map[string]any)
	creditedCents, _ := returnEntry["amountCents"].(float64)

	if deductionOutcome == processor.OutcomeAccepted {
		if !keyExists(t, ctx, conn, deductionTxKey) {
			t.Fatalf("RecordDepositDeduction was accepted but posted no transaction")
		}
		if deductionsTotal != deductAmount {
			t.Fatalf(".deductions.totalCents = %v, want %d (the accepted deduction)", deductionsTotal, deductAmount)
		}
	} else {
		if keyExists(t, ctx, conn, deductionTxKey) {
			t.Fatalf("RecordDepositDeduction was rejected but a transaction exists anyway — a refused submit must mint nothing")
		}
		if deductionsTotal != 0 {
			t.Fatalf(".deductions.totalCents = %v, want 0 — the rejected deduction never landed", deductionsTotal)
		}
		status, _ := readDoc(t, ctx, conn, f.clauseKey+".status")["data"].(map[string]any)
		if got, _ := status["state"].(string); got != "returned" {
			t.Fatalf("a rejected deduction must mean the return landed FIRST, so the clause must read returned; got %q", got)
		}
	}

	if creditedCents+deductionsTotal != depositAmountCents {
		t.Fatalf("conservation broken: credited (%v) + deducted (%v) = %v, want the clause's own amountCents %d",
			creditedCents, deductionsTotal, creditedCents+deductionsTotal, depositAmountCents)
	}
}

// returnDepositPayload builds the ReturnDeposit JSON payload — mirrors
// submitReturnDeposit's inline literal (return_deposit_test.go), factored
// out so the race test can publish without driving.
func returnDepositPayload(leaseKey, clauseKey, acctKey string) []byte {
	return []byte(`{"leaseAppKey":"` + leaseKey + `","clauseKey":"` + clauseKey + `","accountKey":"` + acctKey + `"}`)
}

// TestPayOutBalance_Race_OnlyOneWins proves the account root's own bare
// update (scripts.go's pay_out_balance, make_vtx_update) is what stops two
// concurrent PayOutBalance submissions on a never-evaluated account (no
// .arrears aspect exists yet to serialize through — arrears_stale_mark
// mints nothing where absent) from both paying the SAME computed credit
// balance out: without a shared conditioned key, each independently mints
// its own transaction and neither would ever conflict with the other.
func TestPayOutBalance_Race_OnlyOneWins(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutrace")

	leaseKey := seedLease(t, ctx, conn, "BBPAYQUTRACELEASEHJK")
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBPAYQUTRACEACCTHJKM", leaseKey)
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTRACETXAHJKMN", "credit", 100000, "2027-07-02T09:00:00Z", "")

	envA := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("bbpayoutracea0000001"), Lane: processor.LaneDefault,
		OperationType: "PayOutBalance", Actor: ledgerActorKey, SubmittedAt: depositReturnAt, Class: "transaction",
		Payload: payOutPayload(acctKey, leaseKey), ContextHint: payOutHint(acctKey, leaseKey),
	}
	envB := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("bbpayoutraceb0000001"), Lane: processor.LaneDefault,
		OperationType: "PayOutBalance", Actor: ledgerActorKey, SubmittedAt: depositReturnAt, Class: "transaction",
		Payload: payOutPayload(acctKey, leaseKey), ContextHint: payOutHint(acctKey, leaseKey),
	}
	testutil.PublishOp(t, conn, envA)
	testutil.PublishOp(t, conn, envB)

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	accepted := 0
	var winnerReqID string
	for i, o := range outcomes {
		if o == processor.OutcomeAccepted {
			accepted++
			if i == 0 {
				winnerReqID = envA.RequestID
			} else {
				winnerReqID = envB.RequestID
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("outcomes = %v, want exactly one accepted — two payouts of the same credit balance may never both post", outcomes)
	}

	winnerTxKey := "vtx.transaction." + nanoIDFromRequestID(winnerReqID)
	entry, _ := readDoc(t, ctx, conn, winnerTxKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 100000 {
		t.Fatalf("the one accepted payout's amountCents = %v, want 100000 (the seeded credit, paid out exactly once)", got)
	}
	loserReqID := envA.RequestID
	if winnerReqID == envA.RequestID {
		loserReqID = envB.RequestID
	}
	if keyExists(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(loserReqID)) {
		t.Fatalf("the rejected payout must mint no transaction")
	}
}

// TestCreditAccount_ResidentSelfCredit_Race proves the account root's own
// bare update (post_entry, make_vtx_update, resident branch only) is what
// stops two concurrent capped self-credits from BOTH landing: each is
// individually valid against the balance read at ITS OWN hydration, but a
// credit balance produced by two independently-capped self-credits is
// exactly the balance PayOutBalance would then pay out — cash a resident
// could never actually deposit twice.
func TestCreditAccount_ResidentSelfCredit_Race(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "residentcreditrace")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBRESCREDRACELEASEHJ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "resscredraceacctset1", leaseKey)
	// A landlord-recorded charge establishes the 100000 owed both self-credits
	// below race to pay down — each individually valid at its own (stale)
	// hydration (the CreditAccount_ConsumerSelfScope_Allowed precedent,
	// ledger_test.go).
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("resscredracedebit001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "transaction",
		Payload:       []byte(`{"accountKey":"` + acctKey + `","amountCents":100000,"memo":"July rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	creditPayload := func() []byte {
		return []byte(`{"accountKey":"` + acctKey + `","amountCents":100000}`)
	}
	envA := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("resscredracea0000001"), Lane: processor.LaneDefault,
		OperationType: "CreditAccount", Actor: ledgerSelfConsumerKey, SubmittedAt: "2026-07-08T09:00:00Z", Class: "transaction",
		Payload: creditPayload(), ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext: &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	envB := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("resscredraceb0000001"), Lane: processor.LaneDefault,
		OperationType: "CreditAccount", Actor: ledgerSelfConsumerKey, SubmittedAt: "2026-07-08T09:00:01Z", Class: "transaction",
		Payload: creditPayload(), ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext: &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, envA)
	testutil.PublishOp(t, conn, envB)

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	accepted := 0
	var winnerReqID string
	for i, o := range outcomes {
		if o == processor.OutcomeAccepted {
			accepted++
			if i == 0 {
				winnerReqID = envA.RequestID
			} else {
				winnerReqID = envB.RequestID
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("outcomes = %v, want exactly one accepted — two capped self-credits of the whole balance may never both post", outcomes)
	}

	winnerTxKey := "vtx.transaction." + nanoIDFromRequestID(winnerReqID)
	entry, _ := readDoc(t, ctx, conn, winnerTxKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 100000 {
		t.Fatalf("the one accepted self-credit's amountCents = %v, want 100000 (the whole balance, paid down exactly once)", got)
	}
	loserReqID := envA.RequestID
	if winnerReqID == envA.RequestID {
		loserReqID = envB.RequestID
	}
	if keyExists(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(loserReqID)) {
		t.Fatalf("the rejected self-credit must mint no transaction — never a negative (cashable) balance")
	}
}
