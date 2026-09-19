package loftspaceledger_test

// A reversal names the charge it corrects: CreditAccount's reversesRef leg
// (the atomic writer of the reverses link) and LinkReversal (the operator's
// after-the-fact writer of the same key), through the real Processor
// pipeline. Mirrors clinic-ledger's TestCreditAccount_ReversesRef* set,
// plus what this ledger adds: the face cap (ReversalExceedsCharge), the
// entry-type guards (NotADebit / NotACredit), the landlord's self-scoped
// leg admitted where the resident's is refused, and LinkReversal's own
// guards (AlreadyLinked, the stale mark it leaves on .arrears).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// submitTx submits one transaction-DDL op with an explicit payload, hint and
// authContext, asserts the outcome, and returns the reply (for a refusal's
// code) and the key the op would mint under this label.
func submitTx(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, opType, actorKey string, payload map[string]any,
	hint *processor.ContextHint, authCtx *processor.AuthContext, want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         actorKey,
		SubmittedAt:   "2026-09-13T10:00:00Z",
		Class:         "transaction",
		Payload:       body,
		ContextHint:   hint,
		AuthContext:   authCtx,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, "vtx.transaction." + nanoIDFromRequestID(reqID)
}

// reversalHint is the declaration the landlord console sends with a
// reversal: the account and the charge, plus the charge's own authorizedBy
// walk (reversal_target refuses the security deposit's charge off its
// clause). The charge's .entry and postedTo link are the DDL's own
// derive_reads' to supply.
func reversalHint(acctKey, debitKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads: []string{acctKey, debitKey},
		Enumerations: []processor.EnumerationHint{
			{Hub: debitKey, Relation: "authorizedBy", Direction: "out"},
		},
	}
}

// reversesLinkKey is the one literal shape both writers mint — pinned as a
// string so a drift in either the type segment or the relation name fails
// here, not in an outbound walk that silently rebuilds the wrong endpoint.
func reversesLinkKey(creditKey, debitKey string) string {
	return "lnk.transaction." + creditKey[len("vtx.transaction."):] + ".reverses.transaction." + debitKey[len("vtx.transaction."):]
}

// TestCreditAccount_ReversesRefWritesReversesLink: a CreditAccount carrying
// reversesRef writes the reverses link (credit tx -> the reversed debit tx)
// the arrears evaluation and the ledgerHistory lens read; a plain
// CreditAccount with no reversesRef writes no such link. The link key is
// pinned as a literal: lnk.transaction.<creditId>.reverses.transaction.<debitId>.
func TestCreditAccount_ReversesRefWritesReversesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reversesref")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFLEASE1HJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefacct0000000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefdebit000000001", acctKey, "2026-09-05T17:01:51Z", 205000)

	_, creditKey := submitTx(t, ctx, conn, cp, cons, "revrefcredit00000001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205000, "memo": "Reversal: rent billed after the lease end", "reversesRef": debitKey},
		reversalHint(acctKey, debitKey), nil, processor.OutcomeAccepted)

	creditID := creditKey[len("vtx.transaction."):]
	debitID := debitKey[len("vtx.transaction."):]
	want := "lnk.transaction." + creditID + ".reverses.transaction." + debitID
	if !keyExists(t, ctx, conn, want) {
		t.Fatalf("reverses link must exist at the literal key %s", want)
	}
	lnk := readDoc(t, ctx, conn, want)
	if got, _ := lnk["sourceVertex"].(string); got != creditKey {
		t.Fatalf("reverses link sourceVertex = %q, want the credit %s (the later-arriving vertex is the source)", got, creditKey)
	}
	if got, _ := lnk["targetVertex"].(string); got != debitKey {
		t.Fatalf("reverses link targetVertex = %q, want the debit %s", got, debitKey)
	}
	entry, _ := readDoc(t, ctx, conn, creditKey+".entry")["data"].(map[string]any)
	if got, _ := entry["type"].(string); got != "credit" {
		t.Fatalf("the reversal posts as a credit, got %q", got)
	}

	// A plain CreditAccount (no reversesRef) writes no reverses link.
	_, plainKey := submitTx(t, ctx, conn, cp, cons, "revrefplain000000001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 1000},
		&processor.ContextHint{Reads: []string{acctKey}}, nil, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, reversesLinkKey(plainKey, debitKey)) {
		t.Fatalf("a plain CreditAccount with no reversesRef must write no reverses link")
	}
}

// TestCreditAccount_LandlordSelfScope_ReversesRefAccepted: the shipped
// console's shape — the managing landlord, self-scoped, reversing a charge
// on a lease of a unit they manage — lands with its link. The landlord is
// the creditor; the reversal is their own correction.
func TestCreditAccount_LandlordSelfScope_ReversesRefAccepted(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "revrefll")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBREVREFLLTENANTHJKM"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBREVREFLLLEASEHJKMN", tenantID)
	unitID := "BBREVREFLLUNJTHJKMNP"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefllacct00000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revreflldebit0000001", acctKey, "2026-09-05T17:01:51Z", 205000)

	_, creditKey := submitTx(t, ctx, conn, cp, cons, "revrefllcredit000001", "CreditAccount", ledgerLandlordKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205000, "reversesRef": debitKey},
		reversalHint(acctKey, debitKey),
		&processor.AuthContext{Target: ledgerLandlordKey}, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, reversesLinkKey(creditKey, debitKey)) {
		t.Fatalf("the landlord's self-scoped reversal must write the reverses link")
	}
}

// TestCreditAccount_UnknownReversesRefRejected rejects a CreditAccount whose
// reversesRef names a non-existent transaction (UnknownTransaction), and
// mints nothing.
func TestCreditAccount_UnknownReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownrevref")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFUNKLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefunkacct0000001", leaseKey)
	absent := "vtx.transaction.BBABSENTREVTXHJKMNPQ"

	reply, creditKey := submitTx(t, ctx, conn, cp, cons, "revrefunkcredit00001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 2500, "reversesRef": absent},
		&processor.ContextHint{Reads: []string{acctKey}}, nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "UnknownTransaction")
	if keyExists(t, ctx, conn, creditKey) {
		t.Fatal("a refused reversal must mint nothing")
	}
}

// TestDebitAccount_ReversesRefRejected rejects reversesRef on a DebitAccount
// — a reversal is a credit-only concept (InvalidArgument).
func TestDebitAccount_ReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitrevrefrej")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFDBTLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefdbtacct0000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefdbtdebit000001", acctKey, "2026-09-05T17:01:51Z", 205000)

	reply, _ := submitTx(t, ctx, conn, cp, cons, "revrefdbtdebit000002", "DebitAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 2500, "reversesRef": debitKey},
		&processor.ContextHint{Reads: []string{acctKey}}, nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "InvalidArgument")
}

// TestRecordCharge_ReversesRefRejected is the same refusal on the person's
// manual charge: LoftspaceRecordCharge is a debit, and a debit never
// reverses anything (InvalidArgument).
func TestRecordCharge_ReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "chargerevrefrej")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFCHGLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefchgacct0000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefchgdebit000001", acctKey, "2026-09-05T17:01:51Z", 205000)

	reply, _ := submitTx(t, ctx, conn, cp, cons, "revrefchgcharge00001", "LoftspaceRecordCharge", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 2500, "reversesRef": debitKey},
		&processor.ContextHint{Reads: []string{acctKey}}, nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "InvalidArgument")
}

// TestCreditAccount_ConsumerSelfScope_RejectedReversesRef: a resident
// paying down THEIR OWN account may not name the charge their payment
// reverses — the arrears head retires the named charge on the link's
// strength, and the tenant chooses neither the charge nor the amount of a
// correction. Refused AuthDenied before the balance walk, nothing minted.
func TestCreditAccount_ConsumerSelfScope_RejectedReversesRef(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfrevref")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBREVREFSELFLEASEHJK", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefselfacct000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefselfdebit00001", acctKey, "2026-09-05T17:01:51Z", 150000)

	reply, creditKey := submitTx(t, ctx, conn, cp, cons, "revrefselfcredit0001", "CreditAccount", ledgerSelfConsumerKey,
		map[string]any{"accountKey": acctKey, "amountCents": 150000, "reversesRef": debitKey},
		&processor.ContextHint{Reads: []string{acctKey, debitKey}},
		&processor.AuthContext{Target: ledgerSelfConsumerKey}, processor.OutcomeRejected)
	requireRefusal(t, reply, "AuthDenied")
	if keyExists(t, ctx, conn, creditKey) {
		t.Fatal("a refused resident reversal must mint nothing")
	}
}

// TestCreditAccount_ReversesRefOtherAccountRejected: reversesRef must name a
// charge posted to THIS account — a credit on account A naming a debit on
// account B is refused WrongAccount, off the derived postedTo link key.
func TestCreditAccount_ReversesRefOtherAccountRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "revrefotheracct")

	leaseA := seedLease(t, ctx, conn, "BBREVREFXTHLEASEAHJK")
	acctA := createAccount(t, ctx, conn, cp, cons, "revrefothaccta000001", leaseA)
	leaseB := seedLease(t, ctx, conn, "BBREVREFXTHLEASEBHJK")
	acctB := createAccount(t, ctx, conn, cp, cons, "revrefothacctb000001", leaseB)
	debitOnB := debitAt(t, ctx, conn, cp, cons, "revrefothdebitb00001", acctB, "2026-09-05T17:01:51Z", 205000)

	reply, _ := submitTx(t, ctx, conn, cp, cons, "revrefothcredita0001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctA, "amountCents": 2500, "reversesRef": debitOnB},
		&processor.ContextHint{Reads: []string{acctA, debitOnB}}, nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "WrongAccount")
}

// TestCreditAccount_ReversesRefExceedsCharge: the reversal is capped at the
// charge's own face (ReversalExceedsCharge), and a credit cannot be
// "reversed" (NotADebit) — the two shape guards on the named target.
func TestCreditAccount_ReversesRefExceedsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "revrefexceeds")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFEXCLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefexcacct0000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefexcdebit000001", acctKey, "2026-09-05T17:01:51Z", 205000)
	paymentKey := creditAt(t, ctx, conn, cp, cons, "revrefexcpay00000001", acctKey, "2026-09-06T09:00:00Z", 50000)

	reply, over := submitTx(t, ctx, conn, cp, cons, "revrefexccredit00001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205001, "reversesRef": debitKey},
		reversalHint(acctKey, debitKey), nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "ReversalExceedsCharge")
	if keyExists(t, ctx, conn, over) {
		t.Fatal("a refused reversal must mint nothing")
	}

	reply, _ = submitTx(t, ctx, conn, cp, cons, "revrefexccredit00002", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 1000, "reversesRef": paymentKey},
		&processor.ContextHint{Reads: []string{acctKey, paymentKey}}, nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "NotADebit")

	// Exactly the face is admitted: the cap is inclusive.
	_, exact := submitTx(t, ctx, conn, cp, cons, "revrefexccredit00003", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205000, "reversesRef": debitKey},
		reversalHint(acctKey, debitKey), nil, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, reversesLinkKey(exact, debitKey)) {
		t.Fatal("a reversal of exactly the face must land with its link")
	}
}

// seedDepositClause seeds the authorizing clause a security-deposit charge
// carries — a clause root, its .terms with purpose=deposit, and the
// authorizedBy link from the charge — the shape DebitAccount's
// clause-authorized posting leaves, with no op run.
func seedDepositClause(t *testing.T, ctx context.Context, conn *substrate.Conn, debitKey, clauseID string) {
	t.Helper()
	clauseKey := "vtx.clause." + clauseID
	seedVertex(t, ctx, conn, clauseKey, "clause", nil)
	seedAspect(t, ctx, conn, clauseKey, "terms", "clauseTerms", map[string]any{
		"kind": "computational", "conditioned": false, "amountCents": 250000, "period": "oneTime", "purpose": "deposit",
	})
	seedLink(t, ctx, conn,
		"lnk.transaction."+debitKey[len("vtx.transaction."):]+".authorizedBy.clause."+clauseID,
		debitKey, clauseKey, "authorizedBy", "authorizedBy")
}

// TestCreditAccount_ReversesRefDepositRefused: the security deposit's
// charge is never reversed — a reversal would leave the deposit "held" on
// the statement (computeDepositSummary ignores a credit that names no
// deposit clause) while ReturnDeposit refunds it again at tenancy end. The
// op walks the charge's authorizedBy clause and refuses DepositNotReversible;
// a rent charge on the same account reverses as usual.
func TestCreditAccount_ReversesRefDepositRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "revrefdeposit")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFDEPLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefdepacct0000001", leaseKey)
	depositDebit := debitAt(t, ctx, conn, cp, cons, "revrefdepdebit000001", acctKey, "2026-09-02T09:00:00Z", 250000)
	seedDepositClause(t, ctx, conn, depositDebit, "BBREVREFDEPCLAUSEHJK")
	rentDebit := debitAt(t, ctx, conn, cp, cons, "revrefdepdebit000002", acctKey, "2026-09-05T17:01:51Z", 205000)

	reply, creditKey := submitTx(t, ctx, conn, cp, cons, "revrefdepcredit00001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 250000, "reversesRef": depositDebit},
		reversalHint(acctKey, depositDebit), nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "DepositNotReversible")
	if keyExists(t, ctx, conn, creditKey) {
		t.Fatal("a refused deposit reversal must mint nothing")
	}

	_, rentReversal := submitTx(t, ctx, conn, cp, cons, "revrefdepcredit00002", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205000, "reversesRef": rentDebit},
		reversalHint(acctKey, rentDebit), nil, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, reversesLinkKey(rentReversal, rentDebit)) {
		t.Fatal("the rent charge beside the deposit reverses as usual")
	}
}

// --- LinkReversal ----------------------------------------------------------

// linkReversalHint is the declaration a CLI operator would spell: the three
// payload roots. Every other key (.entry, .arrears, the postedTo links) is
// the DDL's own derive_reads' to supply.
func linkReversalHint(acctKey, creditKey, debitKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads: []string{acctKey, creditKey, debitKey},
		Enumerations: []processor.EnumerationHint{
			{Hub: creditKey, Relation: "reverses", Direction: "out"},
			{Hub: debitKey, Relation: "authorizedBy", Direction: "out"},
		},
	}
}

// TestLinkReversal_AcceptedWritesLinkAndMarksStale: a credit posted naming
// nothing (the legacy reversal — a plain CreditAccount with a memo) is tied
// to its charge by LinkReversal: the reverses link lands at the same literal
// key CreditAccount's atomic leg writes, and the account's recorded arrears
// state is marked stale with every other field carried — the enumerated
// set's meaning changed under the head, so the evaluation recomputes.
func TestLinkReversal_AcceptedWritesLinkAndMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "linkreversal")

	leaseKey := seedLease(t, ctx, conn, "BBLNKREVLEASE1HJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "lnkrevacct0000000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "lnkrevdebit000000001", acctKey, "2026-09-05T17:01:51Z", 205000)
	creditKey := creditAt(t, ctx, conn, cp, cons, "lnkrevcredit00000001", acctKey, "2026-09-13T09:00:00Z", 205000)
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-16T09:00:00Z",
		"dueAt":       "2026-09-05T17:01:51Z",
		"remindAt":    "2026-09-10T17:01:51Z",
		"remindedFor": "2026-09-05T17:01:51Z",
		"sentAt":      "2026-09-16T09:00:00Z",
	})

	_, _ = submitTx(t, ctx, conn, cp, cons, "lnkrevlink0000000001", "LinkReversal", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "creditKey": creditKey, "reversesRef": debitKey},
		linkReversalHint(acctKey, creditKey, debitKey), nil, processor.OutcomeAccepted)

	want := reversesLinkKey(creditKey, debitKey)
	if !keyExists(t, ctx, conn, want) {
		t.Fatalf("LinkReversal must write the reverses link at %s", want)
	}
	lnk := readDoc(t, ctx, conn, want)
	if got, _ := lnk["sourceVertex"].(string); got != creditKey {
		t.Fatalf("reverses link sourceVertex = %q, want the credit", got)
	}
	arrears := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := arrears["stale"].(bool); !stale {
		t.Fatalf("LinkReversal must mark the arrears state stale: %+v", arrears)
	}
	if got, _ := arrears["sentAt"].(string); got != "2026-09-16T09:00:00Z" {
		t.Fatalf("sentAt = %q — the stale mark carries the episode's send record, never rewrites it", got)
	}
	if got, _ := arrears["remindedFor"].(string); got != "2026-09-05T17:01:51Z" {
		t.Fatalf("remindedFor = %q — carried", got)
	}
	// A second LinkReversal on the same credit: the credit names a charge
	// now, whatever the target (AlreadyLinked), and the link is untouched.
	reply, _ := submitTx(t, ctx, conn, cp, cons, "lnkrevlink0000000002", "LinkReversal", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "creditKey": creditKey, "reversesRef": debitKey},
		linkReversalHint(acctKey, creditKey, debitKey), nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "AlreadyLinked")
}

// TestLinkReversal_AlreadyLinkedByTheAtomicLeg: a credit that named its
// charge when it posted (CreditAccount's reversesRef) is refused a second
// link — the two writers of the key are arbitrated by population, and this
// credit belongs to the other one.
func TestLinkReversal_AlreadyLinkedByTheAtomicLeg(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "linkreversaldup")

	leaseKey := seedLease(t, ctx, conn, "BBLNKREVLEASE2HJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "lnkrevdupacct0000001", leaseKey)
	debitA := debitAt(t, ctx, conn, cp, cons, "lnkrevdupdebita00001", acctKey, "2026-09-05T17:01:51Z", 205000)
	debitB := debitAt(t, ctx, conn, cp, cons, "lnkrevdupdebitb00001", acctKey, "2026-09-06T17:01:51Z", 205000)
	_, creditKey := submitTx(t, ctx, conn, cp, cons, "lnkrevdupcredit00001", "CreditAccount", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "amountCents": 205000, "reversesRef": debitA},
		reversalHint(acctKey, debitA), nil, processor.OutcomeAccepted)

	reply, _ := submitTx(t, ctx, conn, cp, cons, "lnkrevduplink0000001", "LinkReversal", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "creditKey": creditKey, "reversesRef": debitB},
		linkReversalHint(acctKey, creditKey, debitB), nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "AlreadyLinked")
	if keyExists(t, ctx, conn, reversesLinkKey(creditKey, debitB)) {
		t.Fatal("a refused LinkReversal must write no link")
	}
}

// TestLinkReversal_Refusals walks LinkReversal's remaining guards, each on
// its own fixture: the credit must be a credit (NotACredit), the target a
// debit (NotADebit), both posted to the payload account (WrongAccount), the
// credit within the charge's face (ReversalExceedsCharge), and both live
// (UnknownTransaction). None mints a link; none marks the arrears state.
func TestLinkReversal_Refusals(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "linkreversalref")

	leaseKey := seedLease(t, ctx, conn, "BBLNKREVLEASE3HJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "lnkrevrefacct0000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "lnkrevrefdebit000001", acctKey, "2026-09-05T17:01:51Z", 205000)
	smallDebit := debitAt(t, ctx, conn, cp, cons, "lnkrevrefdebit000002", acctKey, "2026-09-06T17:01:51Z", 1000)
	creditKey := creditAt(t, ctx, conn, cp, cons, "lnkrevrefcredit00001", acctKey, "2026-09-13T09:00:00Z", 205000)
	otherLease := seedLease(t, ctx, conn, "BBLNKREVLEASE4HJKMNP")
	otherAcct := createAccount(t, ctx, conn, cp, cons, "lnkrevrefacct0000002", otherLease)
	otherCredit := creditAt(t, ctx, conn, cp, cons, "lnkrevrefcredit00002", otherAcct, "2026-09-13T09:00:00Z", 1000)
	// A credit posted BEFORE the charge it would be tied to — the shape the
	// arrears walk reads as a plain payment, so the link is refused.
	earlyCredit := creditAt(t, ctx, conn, cp, cons, "lnkrevrefcredit00003", acctKey, "2026-09-01T09:00:00Z", 1000)
	// The security deposit's charge: a debit authorizedBy a purpose=deposit
	// clause, with a credit of its exact face posted after it.
	depositDebit := debitAt(t, ctx, conn, cp, cons, "lnkrevrefdebit000003", acctKey, "2026-09-02T09:00:00Z", 250000)
	seedDepositClause(t, ctx, conn, depositDebit, "BBLNKREVDEPCLAUSEHJK")
	depositCredit := creditAt(t, ctx, conn, cp, cons, "lnkrevrefcredit00004", acctKey, "2026-09-14T09:00:00Z", 250000)
	// Seeded AFTER every posted entry above (each marks the state stale), so
	// the no-mark assertion below reads the refusals alone.
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-16T09:00:00Z",
	})

	cases := []struct {
		name, label, code string
		credit, target    string
	}{
		{"precedes-charge", "lnkrevreflink0000008", "ReversalPrecedesCharge", earlyCredit, debitKey},
		{"deposit-charge", "lnkrevreflink0000009", "DepositNotReversible", depositCredit, depositDebit},
		{"not-a-credit", "lnkrevreflink0000001", "NotACredit", debitKey, smallDebit},
		{"not-a-debit", "lnkrevreflink0000002", "NotADebit", creditKey, otherCredit},
		{"credit-on-other-account", "lnkrevreflink0000003", "WrongAccount", otherCredit, debitKey},
		{"exceeds-face", "lnkrevreflink0000004", "ReversalExceedsCharge", creditKey, smallDebit},
		{"unknown-credit", "lnkrevreflink0000005", "UnknownTransaction", "vtx.transaction.BBABSENTLNKCRHJKMNPQ", debitKey},
		{"unknown-target", "lnkrevreflink0000006", "UnknownTransaction", creditKey, "vtx.transaction.BBABSENTLNKDBHJKMNPQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reads := []string{acctKey}
			if keyExists(t, ctx, conn, tc.credit) {
				reads = append(reads, tc.credit)
			}
			if keyExists(t, ctx, conn, tc.target) {
				reads = append(reads, tc.target)
			}
			hint := &processor.ContextHint{Reads: reads,
				Enumerations: []processor.EnumerationHint{
					{Hub: tc.credit, Relation: "reverses", Direction: "out"},
					{Hub: tc.target, Relation: "authorizedBy", Direction: "out"}}}
			reply, _ := submitTx(t, ctx, conn, cp, cons, tc.label, "LinkReversal", ledgerActorKey,
				map[string]any{"accountKey": acctKey, "creditKey": tc.credit, "reversesRef": tc.target},
				hint, nil, processor.OutcomeRejected)
			requireRefusal(t, reply, tc.code)
			if keyExists(t, ctx, conn, reversesLinkKey(tc.credit, tc.target)) {
				t.Fatalf("%s: a refused LinkReversal must write no link", tc.name)
			}
		})
	}
	if _, stale := arrearsData(t, ctx, conn, acctKey)["stale"]; stale {
		t.Fatal("a refused LinkReversal must not mark the arrears state stale")
	}
	// The target on another account is refused too (WrongAccount on the
	// debit's own postedTo link), with a credit that IS on this account.
	otherDebit := debitAt(t, ctx, conn, cp, cons, "lnkrevrefdebit000004", otherAcct, "2026-09-05T17:01:51Z", 205000)
	reply, _ := submitTx(t, ctx, conn, cp, cons, "lnkrevreflink0000007", "LinkReversal", ledgerActorKey,
		map[string]any{"accountKey": acctKey, "creditKey": creditKey, "reversesRef": otherDebit},
		linkReversalHint(acctKey, creditKey, otherDebit), nil, processor.OutcomeRejected)
	requireRefusal(t, reply, "WrongAccount")
}
