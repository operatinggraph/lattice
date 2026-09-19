package loftspaceledger_test

// The late fee, end to end through the real Processor pipeline (decision 8 of
// docs/reviews/loftspace-ledger-reversal-and-late-fee-2026-09-18.md): the
// arrears evaluation bills the account's live purpose=lateFee clause on the
// commit that sends the reminder — a debit of the CLAUSE's own amount,
// authorizedBy it, in the same batch, with lateFeeAt stamped on .arrears —
// and on no other commit: a re-evaluation bills nothing more, an account with
// no fee clause (or only a superseded / other-purpose one) is billed nothing,
// lateFeeAt rides every carry sentAt rides and drops where sentAt drops, and
// paying the rent alone leaves the fee as the head of the SAME episode.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const lateFeeCents = 5000

// seedLateFeeClause seeds one clause charging acctKey — the shape
// semantic-contracts' mint_clause writes for the lease's fee term (purpose
// lateFee, period perArrearsEpisode) with the given .status state and, when
// tombstoned, the root SupersedeClause leaves behind.
func seedLateFeeClause(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey, clauseID, purpose string, amountCents int, state string, tombstoned bool) string {
	t.Helper()
	clauseKey := "vtx.clause." + clauseID
	if tombstoned {
		tombstoneVertex(t, ctx, conn, clauseKey, "clause")
	} else {
		seedVertex(t, ctx, conn, clauseKey, "clause", nil)
	}
	period := "perArrearsEpisode"
	if purpose != "lateFee" {
		period = "oneTime"
	}
	seedAspect(t, ctx, conn, clauseKey, "terms", "clauseTerms", map[string]any{
		"kind": "computational", "conditioned": false, "amountCents": amountCents, "period": period, "purpose": purpose,
	})
	seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", map[string]any{"state": state})
	seedLink(t, ctx, conn,
		"lnk.clause."+clauseID+".chargesTo.account."+acctKey[len("vtx.account."):],
		clauseKey, acctKey, "chargesTo", "chargesTo")
	return clauseKey
}

// feeTransactionFor is the transaction the evaluation with requestID minted
// — the one nanoid the account script draws — as "" when it minted none.
func feeTransactionFor(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) string {
	t.Helper()
	txKey := "vtx.transaction." + nanoIDFromRequestID(requestID)
	if !keyExists(t, ctx, conn, txKey) {
		return ""
	}
	return txKey
}

// requireFeeDebit asserts the fee the evaluation posted: a debit of the
// clause's OWN recorded amount (read back from its .terms — the producer,
// never the constant), due on receipt at the evaluation's instant, memo
// naming the rent's due day, posted to the account and authorizedBy the
// clause.
func requireFeeDebit(t *testing.T, ctx context.Context, conn *substrate.Conn, txKey, acctKey, clauseKey, evaluatedAt, rentDueDay string) {
	t.Helper()
	entry, _ := readDoc(t, ctx, conn, txKey+".entry")["data"].(map[string]any)
	if got, _ := entry["type"].(string); got != "debit" {
		t.Fatalf("the fee is a debit, got %q", got)
	}
	clauseCents, _ := readDoc(t, ctx, conn, clauseKey+".terms")["data"].(map[string]any)["amountCents"].(float64)
	if got, _ := entry["amountCents"].(float64); got != clauseCents || got == 0 {
		t.Fatalf("the fee's amountCents = %v, want the clause's own recorded %v", entry["amountCents"], clauseCents)
	}
	if got, _ := entry["postedAt"].(string); got != evaluatedAt {
		t.Fatalf("the fee's postedAt = %q, want the evaluation's own %q", got, evaluatedAt)
	}
	if got, _ := entry["dueAt"].(string); got != evaluatedAt {
		t.Fatalf("the fee's dueAt = %q, want due on receipt (%q) — never re-gridded as rent", got, evaluatedAt)
	}
	if got, _ := entry["memo"].(string); got != "Late fee — rent due "+rentDueDay {
		t.Fatalf("the fee's memo = %q, want %q", got, "Late fee — rent due "+rentDueDay)
	}
	txID := txKey[len("vtx.transaction."):]
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".postedTo.account."+acctKey[len("vtx.account."):]) {
		t.Fatalf("the fee must be postedTo the account")
	}
	if !keyExists(t, ctx, conn, "lnk.transaction."+txID+".authorizedBy.clause."+clauseKey[len("vtx.clause."):]) {
		t.Fatalf("the fee must be authorizedBy the fee clause — the statement's \"why was I charged this?\" chain")
	}
}

// requireFeeBilledFor pins the literal billedFor link key the fee posting
// writes: fee → the head charge, the fee the later-arriving vertex and so
// the source, its type segments the transaction's on both ends (an outbound
// walk rebuilds the far endpoint from the key's own type segment).
func requireFeeBilledFor(t *testing.T, ctx context.Context, conn *substrate.Conn, feeKey, headKey string) {
	t.Helper()
	want := "lnk.transaction." + feeKey[len("vtx.transaction."):] + ".billedFor.transaction." + headKey[len("vtx.transaction."):]
	if !keyExists(t, ctx, conn, want) {
		t.Fatalf("the fee must be billedFor the head charge: %s", want)
	}
	doc := readDoc(t, ctx, conn, want)
	if got, _ := doc["sourceVertex"].(string); got != feeKey {
		t.Fatalf("billedFor.sourceVertex = %q, want the fee %q", got, feeKey)
	}
	if got, _ := doc["targetVertex"].(string); got != headKey {
		t.Fatalf("billedFor.targetVertex = %q, want the head charge %q", got, headKey)
	}
}

// evaluationEvent returns the first event of the given class the
// evaluation's outbox carries, or nil when it emitted none.
func evaluationEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) map[string]any {
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
		if e.EventType == class {
			return e.Payload
		}
	}
	return nil
}

// evaluationDebitEvent returns the account.debited event the evaluation's
// outbox carries — the fee posting's — or nil when it emitted none.
func evaluationDebitEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) map[string]any {
	t.Helper()
	return evaluationEvent(t, ctx, conn, requestID, "account.debited")
}

// TestArrears_LateFeeBilledOnTheSendCommit — the account carries a live
// purpose=lateFee clause; the evaluation that sends the reminder posts the
// fee in the same commit and stamps lateFeeAt. The re-dispatch finds sentAt
// recorded, sends nothing and bills nothing more; lateFeeAt is carried.
func TestArrears_LateFeeBilledOnTheSendCommit(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfee")

	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBARREARSFEELEASEHJK", "BBARREARSFEETNTHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrfeeacct00000001", leaseKey)
	clauseKey := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFEECLAUSEHJ", "lateFee", lateFeeCents, "active", false)
	headKey := debitAt(t, ctx, conn, cp, cons, "bbarrfeedebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrfeeeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("fixture: the head is past its grace, so the reminder must go out")
	}
	// The tenant is told the balance this commit LEAVES — rent plus the fee
	// billed beside it — and the fee itself, so the message can say
	// "including a late fee of $50".
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["balanceCents"].(float64); got != 240000+lateFeeCents {
		t.Fatalf("params.balanceCents = %v, want %d (the rent plus the fee billed in this commit)", params["balanceCents"], 240000+lateFeeCents)
	}
	if got, _ := params["lateFeeCents"].(float64); got != lateFeeCents {
		t.Fatalf("params.lateFeeCents = %v, want %d", params["lateFeeCents"], lateFeeCents)
	}
	evaluated := evaluationEvent(t, ctx, conn, reqID, "account.arrearsEvaluated")
	if got, _ := evaluated["balanceCents"].(float64); got != 240000+lateFeeCents {
		t.Fatalf("account.arrearsEvaluated.balanceCents = %v, want the fee included", evaluated["balanceCents"])
	}
	if got, _ := evaluated["lateFeeCents"].(float64); got != lateFeeCents {
		t.Fatalf("account.arrearsEvaluated.lateFeeCents = %v", evaluated["lateFeeCents"])
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt = %q", got)
	}
	if got, _ := data["lateFeeAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("lateFeeAt = %q, want the send commit's own instant", got)
	}
	feeKey := feeTransactionFor(t, ctx, conn, reqID)
	if feeKey == "" {
		t.Fatal("the send commit must post the late fee")
	}
	requireFeeDebit(t, ctx, conn, feeKey, acctKey, clauseKey, "2026-09-12T09:00:00Z", "2026-09-01")
	requireFeeBilledFor(t, ctx, conn, feeKey, headKey)
	ev := evaluationDebitEvent(t, ctx, conn, reqID)
	if ev == nil {
		t.Fatal("the fee posting emits account.debited, as every posted debit does")
	}
	if got, _ := ev["transactionKey"].(string); got != feeKey {
		t.Fatalf("account.debited.transactionKey = %q, want %q", got, feeKey)
	}
	if got, _ := ev["clauseKey"].(string); got != clauseKey {
		t.Fatalf("account.debited.clauseKey = %q, want %q", got, clauseKey)
	}
	if got, _ := ev["billedForKey"].(string); got != headKey {
		t.Fatalf("account.debited.billedForKey = %q, want the head %q", got, headKey)
	}
	if got, _ := ev["amountCents"].(float64); got != lateFeeCents {
		t.Fatalf("account.debited.amountCents = %v", ev["amountCents"])
	}

	// The re-dispatch: sentAt stands, so nothing is sent and nothing billed.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrfeeeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-13T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID2) != nil {
		t.Fatal("a re-dispatched evaluation must send nothing for an episode already reminded for")
	}
	if got := feeTransactionFor(t, ctx, conn, reqID2); got != "" {
		t.Fatalf("a re-dispatched evaluation must bill nothing more, got %s", got)
	}
	if ev := evaluationDebitEvent(t, ctx, conn, reqID2); ev != nil {
		t.Fatalf("a re-dispatched evaluation must emit no account.debited, got %+v", ev)
	}
	again := arrearsData(t, ctx, conn, acctKey)
	if got, _ := again["lateFeeAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("lateFeeAt = %q, want the original carried forward, not re-stamped", got)
	}
}

// TestArrears_NoLateFeeClause_NoFee — an account with no fee clause is
// reminded and billed nothing: no transaction, no lateFeeAt key at all (its
// absence is the fact "this episode was never charged a fee", not an empty
// value). The same for a clause that is not a fee (the deposit) and for a
// fee clause that is no longer active (superseded, its root tombstoned).
func TestArrears_NoLateFeeClause_NoFee(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnofee")

	cases := []struct {
		name, leaseID, acctLabel, debitLabel, evalLabel string
		seed                                            func(acctKey string)
	}{
		{"no-clause", "BBARREARSNQFEELEASEH", "bbarrnofeeacct000001", "bbarrnofeedebit00001", "bbarrnofeeeval000001", func(string) {}},
		{"deposit-clause", "BBARREARSDPFEELEASEH", "bbarrdpfeeacct000001", "bbarrdpfeedebit00001", "bbarrdpfeeeval000001", func(acctKey string) {
			seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSDPFEECLAUSE", "deposit", 250000, "completed", false)
		}},
		{"superseded-fee-clause", "BBARREARSSPFEELEASEH", "bbarrspfeeacct000001", "bbarrspfeedebit00001", "bbarrspfeeeval000001", func(acctKey string) {
			seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSSPFEECLAUSE", "lateFee", lateFeeCents, "superseded", true)
		}},
		{"active-status-dead-root", "BBARREARSDRFEELEASEH", "bbarrdrfeeacct000001", "bbarrdrfeedebit00001", "bbarrdrfeeeval000001", func(acctKey string) {
			seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSDRFEECLAUSE", "lateFee", lateFeeCents, "active", true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaseKey := seedLease(t, ctx, conn, tc.leaseID)
			acctKey := createAccount(t, ctx, conn, cp, cons, tc.acctLabel, leaseKey)
			tc.seed(acctKey)
			debitAt(t, ctx, conn, cp, cons, tc.debitLabel, acctKey, "2026-09-01T09:00:00Z", 240000)

			_, reqID := evaluateArrears(t, ctx, conn, cp, cons, tc.evalLabel,
				bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
			if arrearsNotification(t, ctx, conn, reqID) == nil {
				t.Fatal("fixture: the reminder must go out")
			}
			if got := feeTransactionFor(t, ctx, conn, reqID); got != "" {
				t.Fatalf("no live fee clause charges this account, yet a fee was posted: %s", got)
			}
			data := arrearsData(t, ctx, conn, acctKey)
			if v, ok := data["lateFeeAt"]; ok {
				t.Fatalf("lateFeeAt must be absent when no fee was billed, got %v", v)
			}
			if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
				t.Fatalf("the send itself is unaffected: sentAt = %q", got)
			}
		})
	}
}

// TestArrears_LateFeeAtCarriedAndDroppedWithSentAt — the lifetime table:
// carried by a posted entry's stale mark (post_entry), carried by the
// evaluation over a partial payment (the episode continues), carried when
// the rent alone is paid and the fee becomes the head (no second reminder,
// no second fee), dropped when the balance clears and the episode ends, and
// dropped at the boundary when a fresh charge opened the next episode
// before any evaluation ran — where that episode is billed its own fee.
func TestArrears_LateFeeAtCarriedAndDroppedWithSentAt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfeelife")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSFLFLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrflfacct00000001", leaseKey)
	clauseKey := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFLFCLAUSEHJ", "lateFee", lateFeeCents, "active", false)
	debitAt(t, ctx, conn, cp, cons, "bbarrflfdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflfeval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if feeTransactionFor(t, ctx, conn, reqID) == "" {
		t.Fatal("fixture: the send commit bills the fee")
	}

	// A partial payment: post_entry marks stale and carries lateFeeAt.
	creditAt(t, ctx, conn, cp, cons, "bbarrflfpay000000001", acctKey, "2026-09-13T09:00:00Z", 100000)
	afterPay := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := afterPay["stale"].(bool); !stale {
		t.Fatal("fixture: the payment marks the state stale")
	}
	if got, _ := afterPay["lateFeeAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("post_entry must carry lateFeeAt as it carries sentAt, got %q", got)
	}

	// The evaluation over the moved head: the episode continues, both carried,
	// nothing sent, nothing billed.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflfeval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID2) != nil || feeTransactionFor(t, ctx, conn, reqID2) != "" {
		t.Fatal("a partial payment continues the episode: no second reminder, no second fee")
	}
	moved := arrearsData(t, ctx, conn, acctKey)
	if got, _ := moved["lateFeeAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("lateFeeAt carried across the evaluation, got %q", got)
	}

	// The rent paid off exactly: the fee (posted Sep 12, due on receipt) is now
	// the head; its own grace ran out Sep 17, and still nothing goes out — the
	// queue never emptied, so this is the same episode.
	creditAt(t, ctx, conn, cp, cons, "bbarrflfpay000000002", acctKey, "2026-09-15T09:00:00Z", 140000)
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflfeval00000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-18T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID3) != nil || feeTransactionFor(t, ctx, conn, reqID3) != "" {
		t.Fatal("the fee becoming the head is the same episode: no second reminder, no second fee")
	}
	feeHead := arrearsData(t, ctx, conn, acctKey)
	if got, _ := feeHead["dueAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("the head is now the fee, due on receipt: dueAt = %q", got)
	}
	if got, _ := feeHead["lateFeeAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("lateFeeAt carried with the fee as head, got %q", got)
	}
	if got, _ := feeHead["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt carried with the fee as head, got %q", got)
	}

	// The fee paid too: the balance clears and the evaluation ends the episode
	// — {evaluatedAt} alone, lateFeeAt dropped with sentAt.
	creditAt(t, ctx, conn, cp, cons, "bbarrflfpay000000003", acctKey, "2026-09-19T09:00:00Z", lateFeeCents)
	evaluateArrears(t, ctx, conn, cp, cons, "bbarrflfeval00000004",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-20T09:00:00Z", processor.OutcomeAccepted)
	over := arrearsData(t, ctx, conn, acctKey)
	for _, k := range []string{"dueAt", "sentAt", "lateFeeAt", "remindedFor"} {
		if v, ok := over[k]; ok {
			t.Fatalf("an ended episode drops %s, got %v", k, v)
		}
	}

	// The boundary: a new charge and a send record that predates it. Seed the
	// old episode's send record back (the shape an evaluation left before the
	// payment-to-zero and the new charge both posted ahead of it), then
	// evaluate past the new head's grace: the stale record is dropped as a
	// finished episode's, and THIS episode is reminded for and billed its own
	// fee at its own instant.
	debitAt(t, ctx, conn, cp, cons, "bbarrflfdebit0000002", acctKey, "2026-10-01T09:00:00Z", 240000)
	seedAspect(t, ctx, conn, acctKey, "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-12T09:00:00Z", "dueAt": "2026-09-01T09:00:00Z", "remindAt": "2026-09-06T09:00:00Z",
		"remindedFor": "2026-09-01T09:00:00Z", "sentAt": "2026-09-12T09:00:00Z", "lateFeeAt": "2026-09-12T09:00:00Z", "stale": true,
	})
	_, reqID5 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflfeval00000005",
		bootstrap.WeaverIdentityKey, acctKey, "2026-10-12T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID5) == nil {
		t.Fatal("the new episode is reminded for on its own merits")
	}
	fresh := arrearsData(t, ctx, conn, acctKey)
	if got, _ := fresh["lateFeeAt"].(string); got != "2026-10-12T09:00:00Z" {
		t.Fatalf("the stale lateFeeAt is dropped at the boundary and the new episode's stamped: got %q", got)
	}
	newFee := feeTransactionFor(t, ctx, conn, reqID5)
	if newFee == "" {
		t.Fatal("the new episode is billed its own fee")
	}
	requireFeeDebit(t, ctx, conn, newFee, acctKey, clauseKey, "2026-10-12T09:00:00Z", "2026-10-01")
}

// TestArrears_FeeTermSetAfterTheReminder_BillsFromTheNextEpisode — a fee
// clause minted after this episode's reminder went out is not billed
// retroactively: the re-evaluation carries sentAt and reaches no send
// commit, so no fee posts and lateFeeAt stays absent, until the next episode.
func TestArrears_FeeTermSetAfterTheReminder_BillsFromTheNextEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfeelate")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSFLTLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrfltacct00000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "bbarrfltdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)
	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflteval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID) == nil || feeTransactionFor(t, ctx, conn, reqID) != "" {
		t.Fatal("fixture: reminded, no fee clause yet, nothing billed")
	}

	seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFLTCLAUSEHJ", "lateFee", lateFeeCents, "active", false)
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "bbarrflteval00000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-20T09:00:00Z", processor.OutcomeAccepted)
	if got := feeTransactionFor(t, ctx, conn, reqID2); got != "" {
		t.Fatalf("a fee term set after the reminder went out must not bill this episode, got %s", got)
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if v, ok := data["lateFeeAt"]; ok {
		t.Fatalf("lateFeeAt must stay absent — this episode was never billed a fee, got %v", v)
	}
	if got, _ := data["sentAt"].(string); got != "2026-09-12T09:00:00Z" {
		t.Fatalf("sentAt carried, got %q", got)
	}
}

// TestArrears_LateFeeIsTheClauseAmount_NotTheLeaseTerm — the fee's amount is
// the CLAUSE's recorded amountCents (the producer the authorizedBy chain
// names), so an amended term the settlement lens has not yet re-minted bills
// the clause as it stands: the debit equals the clause, whatever number sits
// anywhere else.
func TestArrears_LateFeeIsTheClauseAmount_NotTheLeaseTerm(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfeeamt")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSFAMLEASEHJK")
	seedAspect(t, ctx, conn, leaseKey, "lateFee", "leaseLateFee", map[string]any{"amountCents": 9999, "recordedAt": "2026-09-10T00:00:00Z"})
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrfamacct00000001", leaseKey)
	clauseKey := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFAMCLAUSEHJ", "lateFee", 7500, "active", false)
	debitAt(t, ctx, conn, cp, cons, "bbarrfamdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrfameval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	feeKey := feeTransactionFor(t, ctx, conn, reqID)
	if feeKey == "" {
		t.Fatal("the send commit bills the fee")
	}
	requireFeeDebit(t, ctx, conn, feeKey, acctKey, clauseKey, "2026-09-12T09:00:00Z", "2026-09-01")
	entry, _ := readDoc(t, ctx, conn, feeKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 7500 {
		t.Fatalf("the fee bills the clause's 7500, never the lease term's 9999: got %v", got)
	}
	if got, _ := entry["memo"].(string); !strings.HasPrefix(got, "Late fee") {
		t.Fatalf("memo = %q", got)
	}
}

// TestArrears_TwoActiveLateFeeClauses_GreatestKeyBills — an operator
// hand-mint beside the lens's own clause: the evaluation bills the clause
// with the GREATEST key, the same max(c.key) the settlement lens's
// lateFeeClauseKey names, so the op and the lens agree on which clause the
// fee is authorizedBy.
func TestArrears_TwoActiveLateFeeClauses_GreatestKeyBills(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfeetwo")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSFTWLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrftwacct00000001", leaseKey)
	// "BBARREARSFTWCLAUSEab" sorts after "BBARREARSFTWCLAUSEAB" — the
	// greater key carries the 7500 fee, the lesser the 5000.
	lesser := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFTWCLAUSEAB", "lateFee", 5000, "active", false)
	greater := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSFTWCLAUSEab", "lateFee", 7500, "active", false)
	if !(greater > lesser) {
		t.Fatalf("fixture: %s must sort after %s", greater, lesser)
	}
	debitAt(t, ctx, conn, cp, cons, "bbarrftwdebit0000001", acctKey, "2026-09-01T09:00:00Z", 240000)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "bbarrftweval00000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-12T09:00:00Z", processor.OutcomeAccepted)
	feeKey := feeTransactionFor(t, ctx, conn, reqID)
	if feeKey == "" {
		t.Fatal("the send commit bills a fee")
	}
	requireFeeDebit(t, ctx, conn, feeKey, acctKey, greater, "2026-09-12T09:00:00Z", "2026-09-01")
	entry, _ := readDoc(t, ctx, conn, feeKey+".entry")["data"].(map[string]any)
	if got, _ := entry["amountCents"].(float64); got != 7500 {
		t.Fatalf("the greater key's 7500 bills, got %v", got)
	}
	if keyExists(t, ctx, conn, "lnk.transaction."+feeKey[len("vtx.transaction."):]+".authorizedBy.clause."+lesser[len("vtx.clause."):]) {
		t.Fatal("one fee, one authorizing clause — never both")
	}
}

// TestDebitAccount_LateFeeClauseRefRefused — a hand-submitted
// DebitAccount{clauseRef} against a purpose=lateFee clause is refused
// InvalidArgument: the fee is billed by the arrears evaluation on the send
// commit, never charged directly, so nothing is posted and the clause's
// .status is untouched.
func TestDebitAccount_LateFeeClauseRefRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitlatefee")

	leaseKey := seedLease(t, ctx, conn, "BBARREARSDLFLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "bbarrdlfacct00000001", leaseKey)
	clauseKey := seedLateFeeClause(t, ctx, conn, acctKey, "BBARREARSDLFCLAUSEHJ", "lateFee", lateFeeCents, "active", false)

	reqID := testutil.GenReqID("bbarrdlfdebit0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-09-12T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(lateFeeCents) + `,"clauseRef":"` + clauseKey + `","period":"perArrearsEpisode"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey, clauseKey + ".terms"}, OptionalReads: []string{clauseKey + ".status"}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: clauseRef: a late-fee clause is billed by the arrears evaluation, never charged directly") {
		t.Fatalf("want the late-fee clauseRef refusal, got %v / %+v", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, "vtx.transaction."+nanoIDFromRequestID(reqID)) {
		t.Fatal("a refused charge posts nothing")
	}
	if got, _ := readDoc(t, ctx, conn, clauseKey+".status")["data"].(map[string]any)["state"].(string); got != "active" {
		t.Fatalf("the fee clause stays active, got %q", got)
	}
}
