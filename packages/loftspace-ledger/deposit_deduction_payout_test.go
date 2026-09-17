package loftspaceledger_test

// RecordDepositDeduction and PayOutBalance through the real Processor
// pipeline: a landlord deduction taken off a charged, still-held deposit
// clause (the running total on loftspace-ledger's OWN .deductions aspect —
// never semantic-contracts' clauseStatus, the S9 hazard — no .arrears mark,
// every refusal), and paying an ended tenancy's credit balance out (the
// recomputed balance, no authorizedBy link, the .arrears mark, every
// refusal) — plus the derive_reads proof for each: a submitter that
// declares nothing still has the whole read set hydrated.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedChargedDepositClause seeds a purpose=deposit, oneTime, computational
// clause already charged (.status completed, DebitAccount's own shape) with
// its two deterministic custody links to leaseKey/acctKey — the state
// RecordDepositDeduction/ReturnDeposit dispatch on — with no op run.
// deductedCents, when > 0, seeds a running deduction total already recorded
// on the clause's OWN .deductions aspect (depositDeductions DDL) — never on
// .status, which semantic-contracts owns.
func seedChargedDepositClause(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, acctKey, clauseID string, deductedCents int) string {
	t.Helper()
	clauseKey := "vtx.clause." + clauseID
	seedVertex(t, ctx, conn, clauseKey, "clause", nil)
	seedAspect(t, ctx, conn, clauseKey, "terms", "clauseTerms",
		map[string]any{"kind": "computational", "conditioned": false, "amountCents": depositAmountCents, "period": "oneTime", "purpose": "deposit"})
	seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", map[string]any{"state": "completed", "completedAt": depositChargedAt})
	if deductedCents > 0 {
		seedAspect(t, ctx, conn, clauseKey, "deductions", "depositDeductions",
			map[string]any{"totalCents": deductedCents, "count": 1, "lastRecordedAt": depositChargedAt})
	}
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	acctID := strings.TrimPrefix(acctKey, "vtx.account.")
	seedLink(t, ctx, conn, "lnk.clause."+clauseID+".chargesTo.account."+acctID, clauseKey, acctKey, "chargesTo", "chargesTo")
	seedLink(t, ctx, conn, "lnk.clause."+clauseID+".governs.leaseapp."+leaseID, clauseKey, leaseKey, "governs", "governs")
	return clauseKey
}

// deductionPayload builds the RecordDepositDeduction JSON payload.
func deductionPayload(acctKey, clauseKey string, amountCents int, reason string) json.RawMessage {
	b, err := json.Marshal(map[string]any{"accountKey": acctKey, "clauseKey": clauseKey, "amountCents": amountCents, "reason": reason})
	if err != nil {
		panic(err)
	}
	return b
}

// submitRecordDepositDeduction drives one RecordDepositDeduction with the
// given actor/hint/authContext and returns the reply + the transaction key
// the request would mint.
func submitRecordDepositDeduction(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey, clauseKey string, amountCents int, reason string,
	hint *processor.ContextHint, authCtx *processor.AuthContext, want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordDepositDeduction",
		Actor:         actorKey,
		SubmittedAt:   depositReturnAt,
		Class:         "transaction",
		Payload:       deductionPayload(acctKey, clauseKey, amountCents, reason),
		ContextHint:   hint,
		AuthContext:   authCtx,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, "vtx.transaction." + nanoIDFromRequestID(reqID)
}

// deductionHint is the derive_reads shape (scripts.go's RecordDepositDeduction
// branch): the account root, the clause root, its .terms and .status as
// declared reads, .deductions as optionalRead (absent = never deducted) —
// the chargesTo link is the DDL's own derive_reads to supply.
func deductionHint(acctKey, clauseKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{acctKey, clauseKey, clauseKey + ".terms", clauseKey + ".status"},
		OptionalReads: []string{clauseKey + ".deductions"},
	}
}

func deductionsTotalCents(t *testing.T, ctx context.Context, conn *substrate.Conn, clauseKey string) float64 {
	t.Helper()
	deductions, _ := readDoc(t, ctx, conn, clauseKey+".deductions")["data"].(map[string]any)
	v, _ := deductions["totalCents"].(float64)
	return v
}

func TestRecordDepositDeduction_AccumulatesRunningTotal(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "deductaccum")

	leaseKey := seedLease(t, ctx, conn, "BBDEDACCUMLEASEHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "deductaccumacctset01", leaseKey)
	clauseKey := seedChargedDepositClause(t, ctx, conn, leaseKey, acctKey, "BBDEDACCUMCLAUSEHJKM", 0)

	reply, txKey := submitRecordDepositDeduction(t, ctx, conn, cp, cons, "deductaccum000000001", ledgerActorKey,
		acctKey, clauseKey, 100000, "Carpet cleaning", deductionHint(acctKey, clauseKey), nil, processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("first deduction: %+v", reply.Error)
	}
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["type"].(string); got != "deduction" {
		t.Fatalf("entry.type = %q, want deduction", got)
	}
	if got, _ := entry["amountCents"].(float64); got != 100000 {
		t.Fatalf("entry.amountCents = %v, want 100000", entry["amountCents"])
	}
	if got, _ := entry["memo"].(string); got != "Carpet cleaning" {
		t.Fatalf("entry.memo = %q, want the reason", got)
	}
	acctID := acctKey[len("vtx.account."):]
	txID := txKey[len("vtx.transaction."):]
	clauseID := clauseKey[len("vtx.clause."):]
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".postedTo.account."+acctID) {
		t.Fatalf("postedTo link must exist")
	}
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".authorizedBy.clause."+clauseID) {
		t.Fatalf("authorizedBy link must exist — the chain of custody back to the deposit clause")
	}
	if got := deductionsTotalCents(t, ctx, conn, clauseKey); got != 100000 {
		t.Fatalf(".deductions.totalCents = %v, want 100000", got)
	}
	if keyExists(t, ctx, conn, acctKey+".arrears") {
		t.Fatalf("a deduction must mark no .arrears — it moves no FIFO")
	}

	// A second, concurrent-shaped deduction accumulates onto the running
	// total exactly (a bare update, never a pinned overwrite).
	_, txKey2 := submitRecordDepositDeduction(t, ctx, conn, cp, cons, "deductaccum000000002", ledgerActorKey,
		acctKey, clauseKey, 150000, "Wall repair", deductionHint(acctKey, clauseKey), nil, processor.OutcomeAccepted)
	if got := deductionsTotalCents(t, ctx, conn, clauseKey); got != 250000 {
		t.Fatalf(".deductions.totalCents after second deduction = %v, want 250000 (100000+150000)", got)
	}
	status, _ := readDoc(t, ctx, conn, clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("state after deduction = %q, want completed (unchanged until ReturnDeposit)", got)
	}
	if got, _ := status["completedAt"].(string); got != depositChargedAt {
		t.Fatalf("completedAt must be kept: got %q", got)
	}
	if txKey == txKey2 {
		t.Fatalf("each deduction must mint its own transaction")
	}

	// A third deduction that would exceed the deposit is refused, and the
	// running total is left untouched.
	reply3, _ := submitRecordDepositDeduction(t, ctx, conn, cp, cons, "deductaccum000000003", ledgerActorKey,
		acctKey, clauseKey, 1, "one more cent", deductionHint(acctKey, clauseKey), nil, processor.OutcomeRejected)
	requireRefusal(t, reply3, "DeductionExceedsDeposit")
	if got := deductionsTotalCents(t, ctx, conn, clauseKey); got != 250000 {
		t.Fatalf("a refused deduction must not change .deductions.totalCents, got %v", got)
	}
}

// TestRecordDepositDeduction_UndeclaredSubmitter_StillDeducts is the
// derive_reads proof: a submitter that declares NOTHING still has the
// account, the clause, its .terms, .status and .deductions, and the
// chargesTo custody link hydrated by the DDL's own derivation.
func TestRecordDepositDeduction_UndeclaredSubmitter_StillDeducts(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "deductbare")

	leaseKey := seedLease(t, ctx, conn, "BBDEDBARELEASEHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "deductbareacctsetup1", leaseKey)
	clauseKey := seedChargedDepositClause(t, ctx, conn, leaseKey, acctKey, "BBDEDBARECLAUSEHJKMN", 0)

	reqID := testutil.GenReqID("deductbare000000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordDepositDeduction",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		Payload:       deductionPayload(acctKey, clauseKey, 50000, "Undeclared submit"),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := deductionsTotalCents(t, ctx, conn, clauseKey); got != 50000 {
		t.Fatalf("an undeclared submitter must still have the whole read set hydrated: .deductions.totalCents = %v, want 50000", got)
	}
}

// TestRecordDepositDeduction_LandlordSelfScope_Allowed proves a landlord
// holding no operator role can deduct from a deposit on a unit they manage.
func TestRecordDepositDeduction_LandlordSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "deductlandlordok")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBDEDLLTENANTHJKMNPQ"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBDEDLLLEASEHJKMNPQR", tenantID)
	unitID := "BBDEDLLUNJTHJKMNPQRS"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "deductllacctsetup001", leaseKey)
	clauseKey := seedChargedDepositClause(t, ctx, conn, leaseKey, acctKey, "BBDEDLLCLAUSEHJKMNPQ", 0)

	reply, _ := submitRecordDepositDeduction(t, ctx, conn, cp, cons, "deductllok0000000001", ledgerLandlordKey,
		acctKey, clauseKey, 50000, "Cleaning", deductionHint(acctKey, clauseKey), &processor.AuthContext{Target: ledgerLandlordKey}, processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("landlord deduction on a managed unit = %+v, want accepted", reply.Error)
	}
}

// TestRecordDepositDeduction_ResidentSelfScope_Rejected proves the resident
// standing self_scope_standing returns is refused outright for this op — a
// deduction is the landlord's act, never the tenant's own.
func TestRecordDepositDeduction_ResidentSelfScope_Rejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "deductresidentrej")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBDEDRESLEASEHJKMNPQ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "deductresacctsetup01", leaseKey)
	clauseKey := seedChargedDepositClause(t, ctx, conn, leaseKey, acctKey, "BBDEDRESCLAUSEHJKMNP", 0)

	reply, _ := submitRecordDepositDeduction(t, ctx, conn, cp, cons, "deductresrej00000001", ledgerSelfConsumerKey,
		acctKey, clauseKey, 50000, "not mine to deduct", deductionHint(acctKey, clauseKey), &processor.AuthContext{Target: ledgerSelfConsumerKey}, processor.OutcomeRejected)
	requireRefusal(t, reply, "AuthDenied")
}

// TestRecordDepositDeduction_Refusals — each fact the deduction rides on,
// moved off the charged-and-held state one at a time.
func TestRecordDepositDeduction_Refusals(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "deductrefuse")

	mkFixture := func(label, leaseID, clauseID string, deductedCents int, terms map[string]any, status map[string]any) (string, string) {
		leaseKey := seedLease(t, ctx, conn, leaseID)
		acctKey := createAccount(t, ctx, conn, cp, cons, label, leaseKey)
		clauseKey := "vtx.clause." + clauseID
		seedVertex(t, ctx, conn, clauseKey, "clause", nil)
		if terms == nil {
			terms = map[string]any{"kind": "computational", "conditioned": false, "amountCents": depositAmountCents, "period": "oneTime", "purpose": "deposit"}
		}
		seedAspect(t, ctx, conn, clauseKey, "terms", "clauseTerms", terms)
		if status == nil {
			status = map[string]any{"state": "completed", "completedAt": depositChargedAt}
		}
		seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", status)
		if deductedCents > 0 {
			// The running total lives on THIS package's own .deductions
			// aspect (depositDeductions DDL) — never on .status.
			seedAspect(t, ctx, conn, clauseKey, "deductions", "depositDeductions",
				map[string]any{"totalCents": deductedCents, "count": 1, "lastRecordedAt": depositChargedAt})
		}
		acctID := acctKey[len("vtx.account."):]
		seedLink(t, ctx, conn, "lnk.clause."+clauseID+".chargesTo.account."+acctID, clauseKey, acctKey, "chargesTo", "chargesTo")
		return acctKey, clauseKey
	}

	otherAcctKey, _ := mkFixture("deductrefotheracct01", "BBDEDREFQTHERLEASEHJ", "BBDEDREFQTHERCLAUSHJ", 0, nil, nil)

	cases := []struct {
		name, label, leaseID, clauseID, code string
		amountCents                          int
		reason                               string
		deductedCents                        int
		terms, status                        map[string]any
		useAcct, useClause                   string
	}{
		{name: "unknown-account", label: "deductref000001", leaseID: "BBDEDREFLEASE1HJKMNP", clauseID: "BBDEDREFCLAUSE1HJKMN", code: "UnknownAccount",
			amountCents: 50000, reason: "x", useAcct: "vtx.account.BBDEDNQSUCHACCTHJKMN"},
		{name: "unknown-clause", label: "deductref000002", leaseID: "BBDEDREFLEASE2HJKMNP", clauseID: "BBDEDREFCLAUSE2HJKMN", code: "UnknownClause",
			amountCents: 50000, reason: "x", useClause: "vtx.clause.BBDEDNQSUCHCLAUSEHJK"},
		{name: "invalid-amount", label: "deductref000003", leaseID: "BBDEDREFLEASE3HJKMNP", clauseID: "BBDEDREFCLAUSE3HJKMN", code: "InvalidArgument",
			amountCents: 0, reason: "x"},
		{name: "missing-reason", label: "deductref000004", leaseID: "BBDEDREFLEASE4HJKMNP", clauseID: "BBDEDREFCLAUSE4HJKMN", code: "InvalidArgument",
			amountCents: 50000, reason: ""},
		{name: "reason-too-long", label: "deductref000005", leaseID: "BBDEDREFLEASE5HJKMNP", clauseID: "BBDEDREFCLAUSE5HJKMN", code: "InvalidArgument",
			amountCents: 50000, reason: strings.Repeat("x", 201)},
		{name: "not-a-deposit-monthly", label: "deductref000006", leaseID: "BBDEDREFLEASE6HJKMNP", clauseID: "BBDEDREFCLAUSE6HJKMN", code: "NotADeposit",
			amountCents: 50000, reason: "x", terms: map[string]any{"kind": "computational", "conditioned": false, "amountCents": depositAmountCents, "period": "monthly", "purpose": "deposit"}},
		{name: "not-a-deposit-judgment", label: "deductref000007", leaseID: "BBDEDREFLEASE7HJKMNP", clauseID: "BBDEDREFCLAUSE7HJKMN", code: "NotADeposit",
			amountCents: 50000, reason: "x", terms: map[string]any{"kind": "judgment", "conditioned": false, "amountCents": depositAmountCents, "period": "oneTime", "purpose": "deposit"}},
		{name: "not-a-deposit-no-purpose", label: "deductref000008", leaseID: "BBDEDREFLEASE8HJKMNP", clauseID: "BBDEDREFCLAUSE8HJKMN", code: "NotADeposit",
			amountCents: 50000, reason: "x", terms: map[string]any{"kind": "computational", "conditioned": false, "amountCents": depositAmountCents, "period": "oneTime"}},
		{name: "clause-charges-another-account", label: "deductref000009", leaseID: "BBDEDREFLEASE9HJKMNP", clauseID: "BBDEDREFCLAUSE9HJKMN", code: "ClauseAccountMismatch",
			amountCents: 50000, reason: "x", useAcct: otherAcctKey},
		{name: "deposit-not-held-active", label: "deductref000010", leaseID: "BBDEDREFLEASEAHJKMNP", clauseID: "BBDEDREFCLAUSEAHJKMN", code: "DepositNotHeld",
			amountCents: 50000, reason: "x", status: map[string]any{"state": "active"}},
		{name: "deposit-not-held-returned", label: "deductref000011", leaseID: "BBDEDREFLEASEBHJKMNP", clauseID: "BBDEDREFCLAUSEBHJKMN", code: "DepositNotHeld",
			amountCents: 50000, reason: "x", status: map[string]any{"state": "returned", "returnedAt": depositReturnAt}},
		{name: "deduction-exceeds-deposit", label: "deductref000012", leaseID: "BBDEDREFLEASECHJKMNP", clauseID: "BBDEDREFCLAUSECHJKMN", code: "DeductionExceedsDeposit",
			amountCents: 1, reason: "x", deductedCents: depositAmountCents},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The account-open label must differ from the submit label
			// (GenReqID derives the request id from it; reusing one would
			// collide the deduction submit with the account-open submit).
			acctKey, clauseKey := mkFixture(strings.Replace(tc.label, "deductref", "deductopen", 1), tc.leaseID, tc.clauseID, tc.deductedCents, tc.terms, tc.status)
			if tc.useAcct != "" {
				acctKey = tc.useAcct
			}
			if tc.useClause != "" {
				clauseKey = tc.useClause
			}
			reply, txKey := submitRecordDepositDeduction(t, ctx, conn, cp, cons, tc.label, ledgerActorKey,
				acctKey, clauseKey, tc.amountCents, tc.reason, nil, nil, processor.OutcomeRejected)
			requireRefusal(t, reply, tc.code)
			if keyExists(t, ctx, conn, txKey) {
				t.Fatalf("%s: a refused deduction must mint nothing", tc.name)
			}
		})
	}
}

// ---- PayOutBalance ----

func payOutPayload(acctKey, leaseKey string) json.RawMessage {
	b, err := json.Marshal(map[string]any{"accountKey": acctKey, "leaseAppKey": leaseKey})
	if err != nil {
		panic(err)
	}
	return b
}

func submitPayOutBalance(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey, leaseKey string, hint *processor.ContextHint, authCtx *processor.AuthContext,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "PayOutBalance",
		Actor:         actorKey,
		SubmittedAt:   depositReturnAt,
		Class:         "transaction",
		Payload:       payOutPayload(acctKey, leaseKey),
		ContextHint:   hint,
		AuthContext:   authCtx,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, "vtx.transaction." + nanoIDFromRequestID(reqID)
}

func payOutHint(acctKey, leaseKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads: []string{acctKey, leaseKey, leaseKey + ".tenancy"},
	}
}

// seedEndedTenancy stamps a lease's .tenancy with the given endedAt ("" =
// not ended).
func seedEndedTenancy(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, endedAt string) {
	t.Helper()
	tenancy := map[string]any{"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z"}
	if endedAt != "" {
		tenancy["endedAt"] = endedAt
	}
	seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", tenancy)
}

func TestPayOutBalance_PaysOutCreditBalanceAndZeroesIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payout")

	leaseKey := seedLease(t, ctx, conn, "BBPAYQUTLEASEHJKMNPQ")
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBPAYQUTACCTHJKMNPQR", leaseKey)
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTTX1HJKMNPQRS", "debit", 150000, "2026-07-02T13:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTTX2HJKMNPQRS", "credit", 400000, "2027-07-02T09:00:00Z", "")
	// owed = 150000 - 400000 = -250000 (a 2500.00 credit balance)
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{"evaluatedAt": "2027-07-01T00:00:00Z"})

	reply, txKey := submitPayOutBalance(t, ctx, conn, cp, cons, "payout00000000000001", ledgerActorKey,
		acctKey, leaseKey, payOutHint(acctKey, leaseKey), nil, processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("payout: %+v", reply.Error)
	}
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["type"].(string); got != "debit" {
		t.Fatalf("entry.type = %q, want debit", got)
	}
	if got, _ := entry["kind"].(string); got != "payout" {
		t.Fatalf("entry.kind = %q, want payout", got)
	}
	if got, _ := entry["amountCents"].(float64); got != 250000 {
		t.Fatalf("entry.amountCents = %v, want 250000 (the credit balance's magnitude)", entry["amountCents"])
	}
	if got, _ := entry["memo"].(string); got != "Balance paid out to the tenant" {
		t.Fatalf("entry.memo = %q", got)
	}
	acctID := acctKey[len("vtx.account."):]
	txID := txKey[len("vtx.transaction."):]
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".postedTo.account."+acctID) {
		t.Fatalf("postedTo link must exist")
	}
	if keyExists(t, ctx, conn, "lnk.transaction."+txID+".authorizedBy.clause.anything") {
		t.Fatalf("a payout authorizes off no clause")
	}
	arrears := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := arrears["stale"].(bool); !stale {
		t.Fatalf("a payout debit must mark .arrears stale like every other debit: %+v", arrears)
	}

	// The balance is now zero: a second payout is refused NoCreditBalance.
	reply2, _ := submitPayOutBalance(t, ctx, conn, cp, cons, "payout00000000000002", ledgerActorKey,
		acctKey, leaseKey, payOutHint(acctKey, leaseKey), nil, processor.OutcomeRejected)
	requireRefusal(t, reply2, "NoCreditBalance")
}

// TestPayOutBalance_UndeclaredSubmitter_StillPaysOut is the derive_reads
// proof for PayOutBalance's branch: a submitter that declares NOTHING still
// has the account and its .arrears, the lease and its .tenancy, and the
// deterministic heldFor custody link hydrated.
func TestPayOutBalance_UndeclaredSubmitter_StillPaysOut(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutbare")

	leaseKey := seedLease(t, ctx, conn, "BBPAYQUTBARELEASEHJK")
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	acctKey := seedAccountHeldFor(t, ctx, conn, "BBPAYQUTBAREACCTHJKM", leaseKey)
	seedEntryAt(t, ctx, conn, acctKey, "BBPAYQUTBARETXHJKMNP", "credit", 100000, "2027-07-02T09:00:00Z", "")

	reqID := testutil.GenReqID("payoutbare0000000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "PayOutBalance",
		Actor:         ledgerActorKey,
		SubmittedAt:   depositReturnAt,
		// Class is explicit, not inferred: PayOutBalance is legitimately
		// admitted by TWO vertexType DDLs now (account, for its own bare
		// idempotency-anchor update of the account root; transaction, its
		// real dispatch), so the ddl cache's single-DDL class-inference
		// convenience no longer applies to it — every real caller declares
		// class explicitly anyway (submitPayOutBalance does). Declaring it
		// here changes nothing about what this test proves: derive_reads
		// still supplies the WHOLE read set for a submitter that declares no
		// reads/optionalReads at all.
		Class:   "transaction",
		Payload: payOutPayload(acctKey, leaseKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	txKey := "vtx.transaction." + nanoIDFromRequestID(reqID)
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 100000 {
		t.Fatalf("an undeclared submitter must still have the whole read set hydrated: amountCents = %v, want 100000", got)
	}
}

// TestPayOutBalance_LandlordSelfScope_Allowed proves a landlord holding no
// operator role can pay out an ended tenancy's credit balance on a unit
// they manage.
func TestPayOutBalance_LandlordSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutlandlordok")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBPAYQUTLLTENANTHJKM"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBPAYQUTLLLEASEHJKMN", tenantID)
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	unitID := "BBPAYQUTLLUNJTHJKMNP"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "payoutllacctsetup001", leaseKey)
	submitSelfEntry(t, ctx, conn, cp, cons, "payoutllcredit000001", "CreditAccount", ledgerLandlordKey, acctKey, 100000)

	reply, _ := submitPayOutBalance(t, ctx, conn, cp, cons, "payoutllok00000000001", ledgerLandlordKey,
		acctKey, leaseKey, payOutHint(acctKey, leaseKey), &processor.AuthContext{Target: ledgerLandlordKey}, processor.OutcomeAccepted)
	if reply.Error != nil {
		t.Fatalf("landlord payout on a managed unit = %+v, want accepted", reply.Error)
	}
}

// TestPayOutBalance_ResidentSelfScope_Rejected proves the resident standing
// is refused outright — a resident may not pay their own balance out.
func TestPayOutBalance_ResidentSelfScope_Rejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutresidentrej")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBPAYQUTRESLEASEHJKM", ledgerSelfConsumerID)
	seedEndedTenancy(t, ctx, conn, leaseKey, tenancyEndedAt)
	acctKey := createAccount(t, ctx, conn, cp, cons, "payoutresacctsetup01", leaseKey)
	submitSelfEntry(t, ctx, conn, cp, cons, "payoutrescredit00001", "CreditAccount", ledgerSelfConsumerKey, acctKey, 100000)

	reply, _ := submitPayOutBalance(t, ctx, conn, cp, cons, "payoutresrej00000001", ledgerSelfConsumerKey,
		acctKey, leaseKey, payOutHint(acctKey, leaseKey), &processor.AuthContext{Target: ledgerSelfConsumerKey}, processor.OutcomeRejected)
	requireRefusal(t, reply, "AuthDenied")
}

// TestPayOutBalance_Refusals — each fact the payout rides on, moved off the
// ended-with-a-credit-balance state one at a time.
func TestPayOutBalance_Refusals(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutrefuse")

	otherLease := seedLease(t, ctx, conn, "BBPAYQUTQTHERLEASEHJ")

	cases := []struct {
		name, label, leaseID, acctID, code string
		endedAt                            string
		creditCents                        int
		useAcct, useLease                  string
	}{
		{name: "unknown-account", label: "payoutref00000000001", leaseID: "BBPAYQUTREFLEASE1HJK", acctID: "BBPAYQUTREFACCT1HJKM",
			endedAt: tenancyEndedAt, creditCents: 100000, code: "UnknownAccount", useAcct: "vtx.account.BBPAYQUTNQSUCHACCTHJ"},
		{name: "unknown-lease", label: "payoutref00000000002", leaseID: "BBPAYQUTREFLEASE2HJK", acctID: "BBPAYQUTREFACCT2HJKM",
			endedAt: tenancyEndedAt, creditCents: 100000, code: "UnknownLeaseApplication", useLease: "vtx.leaseapp.BBPAYQUTNQSUCHLEASEH"},
		{name: "account-lease-mismatch", label: "payoutref00000000003", leaseID: "BBPAYQUTREFLEASE3HJK", acctID: "BBPAYQUTREFACCT3HJKM",
			endedAt: tenancyEndedAt, creditCents: 100000, code: "AccountLeaseMismatch", useLease: otherLease},
		{name: "tenancy-not-ended", label: "payoutref00000000004", leaseID: "BBPAYQUTREFLEASE4HJK", acctID: "BBPAYQUTREFACCT4HJKM",
			endedAt: "", creditCents: 100000, code: "TenancyNotEnded"},
		{name: "no-credit-balance-zero", label: "payoutref00000000005", leaseID: "BBPAYQUTREFLEASE5HJK", acctID: "BBPAYQUTREFACCT5HJKM",
			endedAt: tenancyEndedAt, creditCents: 0, code: "NoCreditBalance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaseKey := seedLease(t, ctx, conn, tc.leaseID)
			seedEndedTenancy(t, ctx, conn, leaseKey, tc.endedAt)
			acctKey := seedAccountHeldFor(t, ctx, conn, tc.acctID, leaseKey)
			if tc.creditCents > 0 {
				seedEntryAt(t, ctx, conn, acctKey, tc.acctID[:18]+"TX", "credit", tc.creditCents, "2027-07-02T09:00:00Z", "")
			}
			useAcct, useLease := acctKey, leaseKey
			if tc.useAcct != "" {
				useAcct = tc.useAcct
			}
			if tc.useLease != "" {
				useLease = tc.useLease
			}
			reply, txKey := submitPayOutBalance(t, ctx, conn, cp, cons, tc.label, ledgerActorKey,
				useAcct, useLease, nil, nil, processor.OutcomeRejected)
			requireRefusal(t, reply, tc.code)
			if keyExists(t, ctx, conn, txKey) {
				t.Fatalf("%s: a refused payout must mint nothing", tc.name)
			}
		})
	}
}
