package semanticcontracts_test

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

// submitSupersede drives one SupersedeClause over oldClauseKey with the given
// extra payload fields, declaring the amended clause, its .terms (the read
// the inheriting shape requires), its .status (the active check's read), the
// lease and the account.
func submitSupersede(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, oldClauseKey, leaseKey, acctKey, extra string, want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + oldClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"Amended."` + extra + `}`),
		ContextHint: &processor.ContextHint{Reads: []string{oldClauseKey, oldClauseKey + ".terms", oldClauseKey + ".status", leaseKey, acctKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, "vtx.clause." + nanoIDFromRequestID(reqID)
}

// TestSupersedeClause_PurposeInheritedWhenOmitted — an amendment that names
// no purpose keeps the amended clause's token: the replacement of a deposit
// clause is still the deposit clause, so leaseRentSettlement's
// missing_deposit stays shut and no second deposit is minted. The amended
// clause's .terms is the declared read the inheritance is served from; a
// dispatch that omits it is refused rather than allowed to untag.
func TestSupersedeClause_PurposeInheritedWhenOmitted(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedeinherit")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPPURPHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsuppurp01", leaseKey)
	oldClauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausesuppurp", leaseKey, acctKey,
		`,"amountCents":250000,"purpose":"deposit"`, processor.OutcomeAccepted)

	_, newKey := submitSupersede(t, ctx, conn, cp, cons, "supersedeinherit001", oldClauseKey, leaseKey, acctKey,
		`,"amountCents":300000`, processor.OutcomeAccepted)
	newTerms := readData(t, ctx, conn, newKey+".terms")
	if got, _ := newTerms["purpose"].(string); got != "deposit" {
		t.Fatalf("the replacement's terms.purpose = %q, want the inherited deposit", got)
	}
	if got, _ := newTerms["amountCents"].(float64); got != 300000 {
		t.Fatalf("the replacement's amountCents = %v, want the amended 300000", newTerms["amountCents"])
	}

	// A second amendment that omits both the purpose AND the .terms read is
	// refused: the inheritance has nothing to read from, and dropping the
	// token silently is exactly what it exists to prevent.
	undeclaredReqID := testutil.GenReqID("supersedeinherit002")
	undeclared := &processor.OperationEnvelope{
		RequestID:     undeclaredReqID,
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T15:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + newKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"Amended again.","amountCents":310000}`),
		ContextHint: &processor.ContextHint{Reads: []string{newKey, newKey + ".status", leaseKey, acctKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, undeclared)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("an amendment with no purpose and no .terms read must be refused InvalidState, got %v / %+v", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(undeclaredReqID)) {
		t.Fatalf("a refused amendment mints nothing")
	}
}

// TestSupersedeClause_PurposeNeverChanges — an amendment keeps the amended
// clause's purpose: a payload naming the SAME token is accepted; one naming
// a different token — re-tagging a deposit as a fee, tagging an untagged
// clause deposit — is refused InvalidArgument and mints nothing. A clause
// with a different purpose is a new clause, never an amendment.
func TestSupersedeClause_PurposeNeverChanges(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedepurposefixed")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPQVRHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsupovr001", leaseKey)
	depositKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausesupovr1", leaseKey, acctKey,
		`,"amountCents":250000,"purpose":"deposit"`, processor.OutcomeAccepted)
	plainKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausesupovr2", leaseKey, acctKey,
		`,"amountCents":4500`, processor.OutcomeAccepted)

	// deposit → fee: refused, naming the recorded token.
	reply, refused := submitSupersede(t, ctx, conn, cp, cons, "supersedeoverride01", depositKey, leaseKey, acctKey,
		`,"amountCents":300000,"purpose":"lateFee"`, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: purpose: a superseding clause keeps the amended clause's purpose (deposit)") {
		t.Fatalf("re-tagging a deposit must be refused naming the recorded token, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, refused) {
		t.Fatalf("a refused amendment mints nothing")
	}
	if !keyExists(t, ctx, conn, depositKey) {
		t.Fatalf("a refused amendment leaves the amended clause live")
	}

	// none → deposit: refused, naming "none".
	reply, refused = submitSupersede(t, ctx, conn, cp, cons, "supersedeoverride02", plainKey, leaseKey, acctKey,
		`,"amountCents":4600,"purpose":"deposit"`, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "keeps the amended clause's purpose (none)") {
		t.Fatalf("tagging an untagged clause must be refused naming none, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, refused) {
		t.Fatalf("a refused amendment mints nothing")
	}

	// deposit → deposit, explicit: accepted, the token carried.
	_, newKey := submitSupersede(t, ctx, conn, cp, cons, "supersedeoverride03", depositKey, leaseKey, acctKey,
		`,"amountCents":300000,"purpose":"deposit"`, processor.OutcomeAccepted)
	if got, _ := readData(t, ctx, conn, newKey+".terms")["purpose"].(string); got != "deposit" {
		t.Fatalf("an explicit same-token amendment carries the token, got %q", got)
	}

	// none → none: a plain clause amended without a purpose stays untagged.
	_, plainNew := submitSupersede(t, ctx, conn, cp, cons, "supersedeoverride04", plainKey, leaseKey, acctKey,
		`,"amountCents":4600`, processor.OutcomeAccepted)
	if v, ok := readData(t, ctx, conn, plainNew+".terms")["purpose"]; ok {
		t.Fatalf("a clause with no token stays untagged through an amendment, got %v", v)
	}

	// The deposit shape is closed at mint on this path too: amending a
	// deposit to a monthly clause is refused.
	reply, refused = submitSupersede(t, ctx, conn, cp, cons, "supersedeoverride05", newKey, leaseKey, acctKey,
		`,"amountCents":300000,"period":"monthly"`, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: purpose: deposit is a oneTime computational clause") {
		t.Fatalf("an inherited deposit token on a monthly amendment must be refused, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, refused) {
		t.Fatalf("a refused amendment mints nothing")
	}
}

// TestCreateClause_DepositShape_Refusals — purpose=deposit is one shape: a
// oneTime computational clause. A monthly or a judgment "deposit" is refused
// at mint, so neither the lens nor ReturnDeposit ever meets one this package
// wrote.
func TestCreateClause_DepositShape_Refusals(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "depositshape")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEDEPSHAPEHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctdepshape1", leaseKey)
	inspectorKey := seedIdentity(t, ctx, conn, "BBDEPSHAPEYNSPHJKMNP")

	cases := []struct{ name, label, payload string }{
		{"monthly", "createclausedepshp1", `{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey + `","prose":"Deposit.","amountCents":250000,"period":"monthly","purpose":"deposit"}`},
		{"judgment", "createclausedepshp2", `{"leaseAppKey":"` + leaseKey + `","kind":"judgment","inspectorKey":"` + inspectorKey + `","prose":"Deposit.","purpose":"deposit"}`},
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
				Payload:       json.RawMessage(tc.payload),
				ContextHint:   &processor.ContextHint{Reads: []string{leaseKey, acctKey, inspectorKey}},
			}
			outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
			if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: purpose") {
				t.Fatalf("%s: want an InvalidArgument: purpose refusal, got %v / %+v", tc.name, outcome, reply.Error)
			}
			if keyExists(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(reqID)) {
				t.Fatalf("%s: a refused CreateClause must mint nothing", tc.name)
			}
		})
	}
}

// TestCreateClause_AmountCents_WholeCents — a flat amountCents that arrives
// as a float within a millionth of an integer (leaseRentSettlement's
// dollars×100 conversion) is recorded as that integer; a fractional cent is
// refused.
func TestCreateClause_AmountCents_WholeCents(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "wholecents")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEWHQLECENTSHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctwholecent", leaseKey)

	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausewholec1", leaseKey, acctKey,
		`,"amountCents":150000.00000000001`, processor.OutcomeAccepted)
	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["amountCents"].(float64); got != 150000 {
		t.Fatalf("amountCents = %v, want the integer 150000", terms["amountCents"])
	}
	low := submitCreateClause(t, ctx, conn, cp, cons, "createclausewholec2", leaseKey, acctKey,
		`,"amountCents":149999.9999999`, processor.OutcomeAccepted)
	if got, _ := readData(t, ctx, conn, low+".terms")["amountCents"].(float64); got != 150000 {
		t.Fatalf("a float just under the integer rounds to it, got %v", got)
	}

	reqID := testutil.GenReqID("createclausewholec3")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey + `","prose":"A fee.","amountCents":1234.5}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "whole number of cents") {
		t.Fatalf("a fractional cent must be refused, got %v / %+v", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(reqID)) {
		t.Fatalf("a refused CreateClause must mint nothing")
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

// TestSupersedeClause_NonActiveClause_Refused — only an active clause is
// superseded: a completed (charged) and a returned deposit clause are each
// refused ClauseNotActive with nothing minted and the status untouched, and
// a dispatch that never declared .status is refused InvalidState rather than
// allowed to amend a clause whose state it cannot see. The active case is
// the positive vector every other supersede test in this package runs.
func TestSupersedeClause_NonActiveClause_Refused(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedenonactive")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPNQNACTHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsupnonac1", leaseKey)

	cases := []struct{ name, mintLabel, label, state string }{
		{"completed", "createclausesupna01", "supersedenonactive1", "completed"},
		{"returned", "createclausesupna02", "supersedenonactive2", "returned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clauseKey := submitCreateClause(t, ctx, conn, cp, cons, tc.mintLabel, leaseKey, acctKey,
				`,"amountCents":250000,"purpose":"deposit"`, processor.OutcomeAccepted)
			seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", map[string]any{"state": tc.state, "completedAt": "2026-07-02T13:00:00Z"})
			reply, newKey := submitSupersede(t, ctx, conn, cp, cons, tc.label, clauseKey, leaseKey, acctKey,
				`,"amountCents":300000`, processor.OutcomeRejected)
			if reply.Error == nil || !strings.Contains(reply.Error.Message, "ClauseNotActive") {
				t.Fatalf("%s: want ClauseNotActive, got %+v", tc.name, reply.Error)
			}
			if keyExists(t, ctx, conn, newKey) {
				t.Fatalf("%s: a refused amendment mints nothing", tc.name)
			}
			if got, _ := readData(t, ctx, conn, clauseKey+".status")["state"].(string); got != tc.state {
				t.Fatalf("%s: the refused amendment must leave the status %s, got %q", tc.name, tc.state, got)
			}
			if !keyExists(t, ctx, conn, clauseKey) {
				t.Fatalf("%s: the clause root must stay live", tc.name)
			}
		})
	}

	// .status undeclared: refused InvalidState, never amended blind.
	activeKey := submitCreateClause(t, ctx, conn, cp, cons, "createclausesupna03", leaseKey, acctKey,
		`,"amountCents":4500`, processor.OutcomeAccepted)
	reqID := testutil.GenReqID("supersedenonactive3")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + activeKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"Amended.","amountCents":4600}`),
		ContextHint: &processor.ContextHint{Reads: []string{activeKey, activeKey + ".terms", leaseKey, acctKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("an amendment that never declared .status must be refused InvalidState, got %v / %+v", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, "vtx.clause."+nanoIDFromRequestID(reqID)) {
		t.Fatalf("a refused amendment mints nothing")
	}
}
