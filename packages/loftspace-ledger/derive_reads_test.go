// derive_reads bare-submitter vectors (Contract #2 §2.5 class (g)) for the
// keys a reversal's guards read: CreditAccount's reversesRef target — its
// root, its .entry and the postedTo link that proves it is a charge on the
// payload account — and LinkReversal's whole read set. Each envelope below
// declares no Reads and no OptionalReads at all, so an accepted reversal
// proves the transaction DDL's own derive_reads(op) is what hydrated the
// target (a script that reads a key the submitter never declared and the
// derivation never supplied sees it absent and refuses UnknownTransaction —
// the vector would fail on the outcome), and a refused cross-account
// reversal proves the derived link's ABSENCE is the honest verdict. The
// read-drift guard armed on the pipeline is what makes a bare vector a
// proof: a lazy fallback read would fail the test at the read.
//
// The entry ops run no confinement walk (post_entry's own doc comment);
// the one walk a reversal always runs — the named charge's own authorizedBy
// link, for DepositNotReversible — hangs off a payload key, so its vector
// declares exactly that enumeration and nothing else (only a read can be
// derived). LinkReversal adds the credit's own reverses walk for
// AlreadyLinked, declared the same way.
package loftspaceledger_test

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestCreditAccount_UndeclaredSubmitter_ReversesRefStillProven: a bare
// CreditAccount naming the charge it reverses lands with its link — the
// derivation hydrated the debit's root, its .entry (the face the credit is
// capped at) and its postedTo link — and the same bare shape naming a
// charge on ANOTHER account is refused WrongAccount, off the derived link's
// absence, never off a lazy read.
func TestCreditAccount_UndeclaredSubmitter_ReversesRefStillProven(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "revrefnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBREVREFNDLEASE1HJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "revrefndacct00000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "revrefnddebit0000001", acctKey, "2026-09-05T17:01:51Z", 205000)

	creditReqID := testutil.GenReqID("revrefndcredit000001")
	env := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-09-13T10:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":205000,"reversesRef":"` + debitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: debitKey, Relation: "authorizedBy", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v) — the derivation must hydrate the named charge for the reversal to post at all", outcome, reply.Error)
	}
	creditKey := "vtx.transaction." + nanoIDFromRequestID(creditReqID)
	if !keyExists(t, ctx, conn, reversesLinkKey(creditKey, debitKey)) {
		t.Fatal("the bare reversal must land with its reverses link")
	}

	otherLease := seedLease(t, ctx, conn, "BBREVREFNDLEASE2HJKM")
	otherAcct := createAccount(t, ctx, conn, cp, cons, "revrefndacct00000002", otherLease)
	crossEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("revrefndcredit000002"),
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-09-13T10:05:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + otherAcct + `","amountCents":1000,"reversesRef":"` + debitKey + `"}`),
	}
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, crossEnv)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a bare cross-account reversal must be refused, got %v", outcome)
	}
	requireRefusal(t, reply, "WrongAccount")
}

// TestLinkReversal_UndeclaredSubmitter_StillLinks: a LinkReversal that
// declares only the reverses walk (no Reads, no OptionalReads) still ties
// the credit to its charge and marks the account's arrears state stale on a
// conditioned key — the account root and its .arrears, both transactions
// with their .entry aspects, and both postedTo links all come off the DDL's
// own derive_reads.
func TestLinkReversal_UndeclaredSubmitter_StillLinks(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "lnkrevnodecl")

	leaseKey := seedLease(t, ctx, conn, "BBLNKREVNDLEASE1HJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "lnkrevndacct00000001", leaseKey)
	debitKey := debitAt(t, ctx, conn, cp, cons, "lnkrevnddebit0000001", acctKey, "2026-09-05T17:01:51Z", 205000)
	creditKey := creditAt(t, ctx, conn, cp, cons, "lnkrevndcredit000001", acctKey, "2026-09-13T09:00:00Z", 205000)
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-16T09:00:00Z",
		"dueAt":       "2026-09-05T17:01:51Z",
		"remindAt":    "2026-09-10T17:01:51Z",
	})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("lnkrevndlink00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "LinkReversal",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-09-18T10:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","creditKey":"` + creditKey + `","reversesRef":"` + debitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: creditKey, Relation: "reverses", Direction: "out"},
				{Hub: debitKey, Relation: "authorizedBy", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v) — the derivation must hydrate both transactions and the account", outcome, reply.Error)
	}
	if !keyExists(t, ctx, conn, reversesLinkKey(creditKey, debitKey)) {
		t.Fatal("the bare LinkReversal must land its link")
	}
	arrears := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := arrears["stale"].(bool); !stale {
		t.Fatalf("the stale mark must land on the derived .arrears key: %+v", arrears)
	}
	if got, _ := arrears["dueAt"].(string); got != "2026-09-05T17:01:51Z" {
		t.Fatalf("dueAt = %q — the derived hydration is what lets the mark carry the record", got)
	}
}
