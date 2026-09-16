package semanticcontracts_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The clause's purpose token through the real Processor: CreateClause
// records a valid token on .terms.purpose and in its event, refuses anything
// that is not one, stores no key when none is supplied, and SupersedeClause
// inherits it through mint_clause. Then the deposit's whole life on the
// ledger seam this package owns: CreateClause with purpose=deposit →
// DebitAccount (the clauseSatisfaction dispatch) → ReturnDeposit (the
// leaseRentSettlement dispatch, in its declared-reads shape) — which is what
// proves the clauseStatus DDL admits ReturnDeposit's cross-package write.

// clauseCreatedEvent reads the clause.created event data the request's
// outbox carries.
func clauseCreatedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(requestID))
	if err != nil {
		t.Fatalf("read outbox aspect for %s: %v", requestID, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect for %s: %v", requestID, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == "clause.created" {
			return e.Payload
		}
	}
	t.Fatalf("no clause.created event in the outbox of %s", requestID)
	return nil
}

func TestCreateClause_Purpose_RecordedOnTermsAndEvent(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausepurpose")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEPURPQSEHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpurpose01", leaseKey)

	cases := []struct{ name, label, purpose string }{
		{"deposit", "createclausepurpos1", "deposit"},
		{"mixed-case-and-digits", "createclausepurpos2", "petFee2"},
		{"thirty-two-chars", "createclausepurpos3", "a" + strings.Repeat("b", 31)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clauseKey := submitCreateClause(t, ctx, conn, cp, cons, tc.label, leaseKey, acctKey,
				`,"amountCents":250000,"purpose":"`+tc.purpose+`"`, processor.OutcomeAccepted)
			terms := readData(t, ctx, conn, clauseKey+".terms")
			if got, _ := terms["purpose"].(string); got != tc.purpose {
				t.Fatalf("terms.purpose = %q, want %q", got, tc.purpose)
			}
			if got, _ := terms["period"].(string); got != "oneTime" {
				t.Fatalf("terms.period = %q — the purpose token never changes the archetype", got)
			}
			ev := clauseCreatedEvent(t, ctx, conn, testutil.GenReqID(tc.label))
			if got, _ := ev["purpose"].(string); got != tc.purpose {
				t.Fatalf("clause.created event purpose = %q, want %q", got, tc.purpose)
			}
		})
	}
}

// TestCreateClause_NoPurpose_StoresNoKey — the positive vector for the
// absent shape every existing clause has: no purpose key on .terms and none
// in the event, never an empty string or a default.
func TestCreateClause_NoPurpose_StoresNoKey(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausenopurpose")

	leaseKey := seedLease(t, ctx, conn, "BBLEASENQPURPQSEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctnopurpos1", leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausenopurp1", leaseKey, acctKey,
		`,"amountCents":4500`, processor.OutcomeAccepted)
	terms := readData(t, ctx, conn, clauseKey+".terms")
	if v, ok := terms["purpose"]; ok {
		t.Fatalf("a clause minted without a purpose must carry no purpose key, got %v", v)
	}
	ev := clauseCreatedEvent(t, ctx, conn, testutil.GenReqID("createclausenopurp1"))
	if v, ok := ev["purpose"]; ok {
		t.Fatalf("clause.created must carry no purpose when none was supplied, got %v", v)
	}
	// An explicit null is the absent shape too.
	nullKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausenopurp2", leaseKey, acctKey,
		`,"amountCents":4500,"purpose":null`, processor.OutcomeAccepted)
	if v, ok := readData(t, ctx, conn, nullKey+".terms")["purpose"]; ok {
		t.Fatalf("purpose: null must record no purpose key, got %v", v)
	}
}

// TestCreateClause_Purpose_Refusals — anything that is not a
// ^[a-z][a-zA-Z0-9]{0,31}$ token is refused InvalidArgument and mints
// nothing: an upper-case or digit first character, a separator, the empty
// string, a 33-character token, a non-string.
func TestCreateClause_Purpose_Refusals(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "clausepurposerefuse")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEPURPREFHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpurpref01", leaseKey)

	cases := []struct{ name, label, extra string }{
		{"upper-first", "createclausepurprf1", `,"amountCents":4500,"purpose":"Deposit"`},
		{"digit-first", "createclausepurprf2", `,"amountCents":4500,"purpose":"1deposit"`},
		{"separator", "createclausepurprf3", `,"amountCents":4500,"purpose":"pet-fee"`},
		{"space", "createclausepurprf4", `,"amountCents":4500,"purpose":"pet fee"`},
		{"empty", "createclausepurprf5", `,"amountCents":4500,"purpose":""`},
		{"too-long", "createclausepurprf6", `,"amountCents":4500,"purpose":"a` + strings.Repeat("b", 32) + `"`},
		{"not-a-string", "createclausepurprf7", `,"amountCents":4500,"purpose":7`},
		{"non-ascii", "createclausepurprf8", `,"amountCents":4500,"purpose":"dépôt"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqID := testutil.GenReqID(tc.label)
			env := &processor.OperationEnvelope{
				RequestID:     reqID,
				Lane:          processor.LaneDefault,
				OperationType: "CreateClause",
				Actor:         scActorKey,
				SubmittedAt:   "2026-07-02T12:00:00Z",
				Class:         "clause",
				Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey + `","prose":"A fee."` + tc.extra + `}`),
				ContextHint:   &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
			}
			outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
			if outcome != processor.OutcomeRejected {
				t.Fatalf("%s: outcome = %v, want rejected", tc.name, outcome)
			}
			if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: purpose") {
				t.Fatalf("%s: want an InvalidArgument: purpose refusal, got %+v", tc.name, reply.Error)
			}
			if keyExists(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(reqID)) {
				t.Fatalf("%s: a refused CreateClause must mint nothing", tc.name)
			}
		})
	}
}

// TestSupersedeClause_InheritsPurpose — the replacement clause is minted
// through mint_clause, so the purpose the amendment supplies is recorded on
// it the same way.
func TestSupersedeClause_InheritsPurpose(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedepurpose")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPPURPHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsuppurp01", leaseKey)
	oldClauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausesuppurp", leaseKey, acctKey,
		`,"amountCents":250000,"purpose":"deposit"`, processor.OutcomeAccepted)

	supersedeReqID := testutil.GenReqID("supersedepurpose001")
	env := &processor.OperationEnvelope{
		RequestID:     supersedeReqID,
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + oldClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"Security deposit (amended to $3,000).","amountCents":300000,"purpose":"deposit"}`),
		ContextHint: &processor.ContextHint{Reads: []string{oldClauseKey, leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	newTerms := readData(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(supersedeReqID)+".terms")
	if got, _ := newTerms["purpose"].(string); got != "deposit" {
		t.Fatalf("the replacement clause's terms.purpose = %q, want deposit", got)
	}
}

// TestDeposit_ChargedThenReturned_ThroughBothPackages — the deposit clause's
// whole life on the seam: CreateClause with purpose=deposit, DebitAccount as
// clauseSatisfaction dispatches it (the clause completes), then
// ReturnDeposit as leaseRentSettlement's missing_depositReturn dispatches it
// — the declared-reads shape targets.go routes, with the two custody links
// left to loftspace-ledger's own derive_reads — once the lease's .tenancy
// records endedAt. Both packages are installed here, so the clauseStatus
// DDL's permittedCommands gate is live: the returned write commits only
// because that DDL admits ReturnDeposit.
func TestDeposit_ChargedThenReturned_ThroughBothPackages(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "depositlife")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEDEPLYFEHJKMNP")
	seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z", "endedAt": "2027-07-01T00:00:00Z",
	})
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctdeplife01", leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausedeplife", leaseKey, acctKey,
		`,"amountCents":250000,"period":"oneTime","purpose":"deposit"`, processor.OutcomeAccepted)

	// Before the charge: a return is refused — the deposit is still active.
	early := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("returndepositearly01"),
		Lane:          processor.LaneDefault,
		OperationType: "ReturnDeposit",
		Actor:         scActorKey,
		SubmittedAt:   "2027-07-02T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","clauseKey":"` + clauseKey + `","accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{acctKey, leaseKey + ".tenancy", clauseKey, clauseKey + ".terms", clauseKey + ".status"},
			OptionalReads: []string{acctKey + ".arrears"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, early)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "DepositNotCharged") {
		t.Fatalf("a return before the charge must be refused DepositNotCharged, got %v / %+v", outcome, reply.Error)
	}

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitdeposit00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":250000,"clauseRef":"` + clauseKey + `","period":"oneTime"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey, clauseKey + ".terms"}, OptionalReads: []string{clauseKey + ".status"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := readData(t, ctx, conn, clauseKey+".status")["state"].(string); got != "completed" {
		t.Fatalf("after the charge the clause is completed, got %q", got)
	}

	returnReqID := testutil.GenReqID("returndeposit0000001")
	returnEnv := &processor.OperationEnvelope{
		RequestID:     returnReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReturnDeposit",
		Actor:         scActorKey,
		SubmittedAt:   "2027-07-03T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","clauseKey":"` + clauseKey + `","accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{acctKey, leaseKey + ".tenancy", clauseKey, clauseKey + ".terms", clauseKey + ".status"},
			OptionalReads: []string{acctKey + ".arrears"},
		},
	}
	testutil.PublishOp(t, conn, returnEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	txKey := "vtx.transaction." + nanoIDFromRequestID(returnReqID)
	entry := readData(t, ctx, conn, txKey+".entry")
	if got, _ := entry["type"].(string); got != "credit" {
		t.Fatalf("the return posts a credit, got %q", got)
	}
	if got, _ := entry["amountCents"].(float64); got != 250000 {
		t.Fatalf("the return credits the clause's own amount, got %v", entry["amountCents"])
	}
	if !keyExists(t, ctx, conn, "lnk.transaction."+txKey[len("vtx.transaction."):]+".authorizedBy.clause."+clauseKey[len("vtx.clause."):]) {
		t.Fatalf("the return credit must be authorizedBy the deposit clause")
	}
	status := readData(t, ctx, conn, clauseKey+".status")
	if got, _ := status["state"].(string); got != "returned" {
		t.Fatalf("after the return the clause is returned, got %q", got)
	}
	if _, ok := status["completedAt"]; !ok {
		t.Fatalf("the return keeps DebitAccount's completedAt: %v", status)
	}
	if got, _ := status["returnedAt"].(string); got != "2027-07-03T09:00:00Z" {
		t.Fatalf("returnedAt = %q", got)
	}
}
