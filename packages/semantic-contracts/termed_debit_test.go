package semanticcontracts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The termed-clause due-date rule of loftspace-ledger's DebitAccount, driven
// through the real Processor: a monthly clause whose .terms carry
// validFrom/validUntil has its due dates on the calendar-month anniversary
// grid from validFrom — never postedAt + the recurring window — and completes
// when the next due reaches validUntil.

// createTermedClause mints a monthly clause with a term through CreateClause.
func createTermedClause(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, acctKey, validFrom, validUntil string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","accountKey":"` + acctKey +
			`","prose":"Monthly rent per the signed lease agreement.","amountCents":240000,"period":"monthly","validFrom":"` +
			validFrom + `","validUntil":"` + validUntil + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseAppKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.clause." + nanoIDFromRequestID(reqID)
}

// setClauseDue writes the clause's recorded due date directly, standing in
// for an earlier DebitAccount (or a legacy 720h-cadence stamp) so a single
// test can start from any point of the term.
func setClauseDue(t *testing.T, ctx context.Context, conn *substrate.Conn, clauseKey, due string) {
	t.Helper()
	doc := map[string]any{"class": "clauseStatus", "isDeleted": false, "vertexKey": clauseKey, "localName": "status",
		"data": map[string]any{"state": "active", "chargeValidUntil": due}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, clauseKey+".status", b); err != nil {
		t.Fatalf("seed status %s: %v", clauseKey, err)
	}
}

// debitTermed submits the clauseSatisfaction playbook's DebitAccount shape —
// clauseRef + period, Reads on the account / clause / .terms, .status as the
// absence-tolerant read — and returns the committed .status data.
func debitTermed(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, acctKey, clauseKey, submittedAt string, want processor.MessageOutcome) map[string]any {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         scActorKey,
		SubmittedAt:   submittedAt,
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":240000,"clauseRef":"` + clauseKey + `","period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{acctKey, clauseKey, clauseKey + ".terms"},
			OptionalReads: []string{clauseKey + ".status"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	if want != processor.OutcomeAccepted {
		return nil
	}
	statusDoc := readDoc(t, ctx, conn, clauseKey+".status")
	data, _ := statusDoc["data"].(map[string]any)
	return data
}

// The first charge of a termed clause bills period 0 and records the next
// due one calendar month after validFrom — posted 40 days late, the due
// date is still the anniversary, not postedAt + 30 days.
func TestDebitAccount_TermedClause_FirstChargeDueOnTheAnniversary(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit1")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb01", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb01", leaseKey, acctKey, "2026-01-31T00:00:00Z", "2027-01-31T00:00:00Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000001", acctKey, clauseKey, "2026-03-12T09:00:00Z", processor.OutcomeAccepted)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-02-28T00:00:00Z" {
		t.Fatalf("first due = %q, want 2026-02-28T00:00:00Z (validFrom + 1 month, clamped)", got)
	}
	if got, _ := status["state"].(string); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}
}

// The next due is computed from validFrom, not from the previous due: Jan 31
// -> Feb 28 -> Mar 31, never Mar 28.
func TestDebitAccount_TermedClause_NextDueNeverDrifts(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit2")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK2")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb02", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb02", leaseKey, acctKey, "2026-01-31T00:00:00Z", "2027-01-31T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-02-28T00:00:00Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000002", acctKey, clauseKey, "2026-02-28T00:00:05Z", processor.OutcomeAccepted)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-03-31T00:00:00Z" {
		t.Fatalf("second due = %q, want 2026-03-31T00:00:00Z", got)
	}
}

// A recorded due off the anniversary grid (a legacy 720h-cadence stamp) bills
// the period containing it and re-arms on the grid.
func TestDebitAccount_TermedClause_OffGridDueBillsItsContainingPeriod(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit3")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK3")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb03", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb03", leaseKey, acctKey, "2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-10-02T17:18:27Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000003", acctKey, clauseKey, "2026-10-02T17:18:30Z", processor.OutcomeAccepted)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-10-08T00:00:00Z" {
		t.Fatalf("due after an off-grid 10-02 = %q, want 2026-10-08T00:00:00Z (the anniversary after the containing period's start)", got)
	}
}

// A recorded due before validFrom (every prior charge was pre-term) bills
// period 0.
func TestDebitAccount_TermedClause_DueBeforeValidFromBillsPeriodZero(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit4")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK4")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb04", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb04", leaseKey, acctKey, "2026-11-01T00:00:00Z", "2027-11-01T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-09-30T00:00:00Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000004", acctKey, clauseKey, "2026-11-01T00:00:01Z", processor.OutcomeAccepted)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-12-01T00:00:00Z" {
		t.Fatalf("due = %q, want 2026-12-01T00:00:00Z (period 0 billed)", got)
	}
}

// The final period's charge completes the clause: the next due equals
// validUntil and the status flips to completed.
func TestDebitAccount_TermedClause_FinalPeriodCompletes(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit5")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK5")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb05", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb05", leaseKey, acctKey, "2025-09-06T17:01:31Z", "2026-09-06T17:01:31Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-08-06T17:01:31Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000005", acctKey, clauseKey, "2026-08-06T17:01:40Z", processor.OutcomeAccepted)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-09-06T17:01:31Z" {
		t.Fatalf("due after the final charge = %q, want validUntil 2026-09-06T17:01:31Z", got)
	}
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("state = %q, want completed (the term is fully billed)", got)
	}
	if _, ok := status["completedAt"]; !ok {
		t.Fatalf("completedAt must be stamped, got %v", status)
	}
}

// A due at or past validUntil bills nothing: TermExhausted, no transaction.
func TestDebitAccount_TermedClause_PastValidUntilRefused(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit6")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK6")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb06", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb06", leaseKey, acctKey, "2025-09-06T17:01:31Z", "2026-09-06T17:01:31Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-09-06T17:01:31Z")

	debitTermed(t, ctx, conn, cp, cons, "debittermed000000006", acctKey, clauseKey, "2026-09-06T17:02:00Z", processor.OutcomeRejected)
	if keyExists(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(testutil.GenReqID("debittermed000000006"))) {
		t.Fatal("a refused charge must mint no transaction")
	}
}

// A termed clause's charge dispatched WITHOUT declaring .status is refused:
// a due date read from nothing would rewind the clause to period 0 and the
// lens would then re-bill every period since. The clause's .status exists
// (CreateClause wrote it); only the declaration is missing.
func TestDebitAccount_TermedClause_UndeclaredStatusRefused(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit7")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK7")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb07", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb07", leaseKey, acctKey, "2026-01-31T00:00:00Z", "2027-01-31T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-06-30T00:00:00Z")

	reqID := testutil.GenReqID("debittermed000000007")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         scActorKey,
		SubmittedAt:   "2026-06-30T00:00:05Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":240000,"clauseRef":"` + clauseKey + `","period":"monthly"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey, clauseKey + ".terms"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(reqID)) {
		t.Fatal("a refused charge must mint no transaction")
	}
	status, _ := readDoc(t, ctx, conn, clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-06-30T00:00:00Z" {
		t.Fatalf("the recorded due must be untouched, got %q", got)
	}
}

// An untermed clause keeps accepting the bare declaration (its due is never
// read), so the pre-term dispatch shape still bills it.
func TestDebitAccount_UntermedClause_UndeclaredStatusStillAccepted(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit8")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK8")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb08", leaseKey)
	reqID := testutil.GenReqID("createclauseuntrm08")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"Monthly smart-home fee.","amountCents":240000,"period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(reqID)

	debit := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debittermed000000008"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":240000,"clauseRef":"` + clauseKey + `","period":"monthly"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey, clauseKey + ".terms"}},
	}
	testutil.PublishOp(t, conn, debit)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	status, _ := readDoc(t, ctx, conn, clauseKey+".status")["data"].(map[string]any)
	if got, _ := status["chargeValidUntil"].(string); got != "2026-08-01T13:00:00Z" {
		t.Fatalf("untermed due = %q, want postedAt + 720h = 2026-08-01T13:00:00Z", got)
	}
}

// termedEntry reads the .entry data of the transaction a debitTermed label
// minted (the transaction key derives from the request id the label seeds).
func termedEntry(t *testing.T, ctx context.Context, conn *substrate.Conn, label string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(testutil.GenReqID(label))+".entry")
	data, _ := doc["data"].(map[string]any)
	return data
}

// A termed clause's charge records the period it bills and its due date on
// the entry itself: the period is [the recorded due, the next anniversary),
// and it falls due at the period's start — never at postedAt.
func TestDebitAccount_TermedClause_EntryRecordsItsPeriodAndDueDate(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit9")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHJK9")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb09", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb09", leaseKey, acctKey, "2026-09-06T00:00:00Z", "2027-09-06T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-10-06T00:00:00Z")

	debitTermed(t, ctx, conn, cp, cons, "debittermed000000009", acctKey, clauseKey, "2026-10-09T23:49:30Z", processor.OutcomeAccepted)
	entry := termedEntry(t, ctx, conn, "debittermed000000009")
	if got, _ := entry["periodStart"].(string); got != "2026-10-06T00:00:00Z" {
		t.Fatalf("entry.periodStart = %q, want the recorded due 2026-10-06T00:00:00Z", got)
	}
	if got, _ := entry["periodEnd"].(string); got != "2026-11-06T00:00:00Z" {
		t.Fatalf("entry.periodEnd = %q, want the next anniversary 2026-11-06T00:00:00Z", got)
	}
	if got, _ := entry["dueAt"].(string); got != "2026-10-06T00:00:00Z" {
		t.Fatalf("entry.dueAt = %q, want the period's start, not postedAt", got)
	}
	if got, _ := entry["postedAt"].(string); got != "2026-10-09T23:49:30Z" {
		t.Fatalf("entry.postedAt = %q", got)
	}
}

// The final period's end is the term's own end, not the anniversary past it:
// a term that is not a whole number of months bills [its last anniversary,
// validUntil).
func TestDebitAccount_TermedClause_FinalPeriodEndsAtValidUntil(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "termeddebit10")
	leaseKey := seedLease(t, ctx, conn, "BBLEASETERMEDDEBHKTN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createaccttermdeb10", leaseKey)
	clauseKey := createTermedClause(t, ctx, conn, cp, cons, "createclausetermdb10", leaseKey, acctKey, "2026-01-01T00:00:00Z", "2026-03-15T00:00:00Z")
	setClauseDue(t, ctx, conn, clauseKey, "2026-03-01T00:00:00Z")

	status := debitTermed(t, ctx, conn, cp, cons, "debittermed000000010", acctKey, clauseKey, "2026-03-01T00:00:05Z", processor.OutcomeAccepted)
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("state = %q, want completed", got)
	}
	entry := termedEntry(t, ctx, conn, "debittermed000000010")
	if got, _ := entry["periodStart"].(string); got != "2026-03-01T00:00:00Z" {
		t.Fatalf("entry.periodStart = %q", got)
	}
	if got, _ := entry["periodEnd"].(string); got != "2026-03-15T00:00:00Z" {
		t.Fatalf("entry.periodEnd = %q, want validUntil (the term ends there), not the April anniversary", got)
	}
}

// An untermed monthly clause's charge falls due at its recorded lapse when
// one is hydrated and already reached (the lens opened the gap there), while
// its period still runs from the posting — the legacy cadence.
func TestDebitAccount_UntermedClause_EntryDueAtTheRecordedLapse(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "untermeddebit1")
	leaseKey := seedLease(t, ctx, conn, "BBLEASEUNTERMDEBHJK1")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctuntermdb01", leaseKey)
	clauseReqID := testutil.GenReqID("createclauseuntrmdb1")
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     clauseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         scActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"Monthly smart-home fee.","amountCents":240000,"period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(clauseReqID)
	setClauseDue(t, ctx, conn, clauseKey, "2026-08-01T12:00:00Z")

	debitTermed(t, ctx, conn, cp, cons, "debituntermed0000001", acctKey, clauseKey, "2026-08-03T09:00:00Z", processor.OutcomeAccepted)
	entry := termedEntry(t, ctx, conn, "debituntermed0000001")
	if got, _ := entry["dueAt"].(string); got != "2026-08-01T12:00:00Z" {
		t.Fatalf("entry.dueAt = %q, want the recorded lapse 2026-08-01T12:00:00Z", got)
	}
	if got, _ := entry["periodStart"].(string); got != "2026-08-03T09:00:00Z" {
		t.Fatalf("entry.periodStart = %q, want postedAt (the untermed period runs from the posting)", got)
	}
}
