package loftspaceledger

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this lease
// had": the payment-history FE reads THIS projected bucket (one entry per
// transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "loftspace-ledger-history"

// LeaseAccountsBucket is the NATS-KV read model the leaseAccounts lens
// projects into — one row per LEASE (whether or not a ledger account has
// been opened yet), carrying the account's key when one exists. Since the
// account carries its own independently-minted NanoID (never derived from
// the lease's own), the FE cannot compute an account key by string
// manipulation the way it once could — this lens is the P5 query surface for
// "does this lease have a ledger account, and what is its key."
const LeaseAccountsBucket = "loftspace-lease-accounts"

// ArrearsRemindersTarget is the §10.8 TargetID == the loftspaceArrearsReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads, and
// the key the freshnessExpiry marker records this target's own fired timer
// under.
const ArrearsRemindersTarget = "loftspaceArrearsReminders"

// arrearsOp is the Weaver-dispatched arrears evaluation (accountDDLScript).
const arrearsOp = "EvaluateLoftspaceArrears"

// Lenses returns the package's Lens declarations: ledgerHistory (one row per
// posted transaction, flattening the .entry aspect + the account/lease it
// posted to into a query-optimized read-model row — the FE derives a running
// balance client-side by summing amountCents, positive for debit, negative
// for credit, over rows for a given leaseAppKey/accountKey; the ledger
// itself never stores a mutable running total), leaseAccounts (the lease
// -> account key lookup, since the account key is no longer derivable, plus
// the account's three arrears columns), and loftspaceArrearsReminders (the
// one-row-per-account arrears convergence lens whose playbook dispatches
// EvaluateLoftspaceArrears — the wellness-ledger arrears mechanism applied
// to this ledger).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "ledgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "leaseAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseAccountsBucket,
			Engine:        "full",
			Spec:          leaseAccountsSpec,
		},
		{
			CanonicalName:  ArrearsRemindersTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           arrearsRemindersSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "account",
				OutputKeyPattern: ArrearsRemindersTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_evaluation", "missing_replay_a", "missing_replay_b", "entityKey", "freshUntil", "dueAt", "remindAt", "remindedFor", "reminderSentAt", "stale", "historyTooLong", "historyBudget", "evaluatedAt", "replaying", "replayPages", "maxretries_evaluation"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// arrearsRemindersSpec is the one-row-per-account arrears convergence cypher —
// wellness-ledger's wellnessArrearsRemindersSpec applied to a rent account,
// with ONE difference: the timer arms at remindAt (the head's own recorded due
// date plus the package's grace), not at dueAt. Rent is due on its date and the
// statement counts "N days overdue" from dueAt; the reminder waits out the
// grace. The identity the reminder addresses is the op's to resolve from
// state, never a row column: a Params entry bound to an optional hop is a
// dispatch refusal on every row where the hop misses. freshUntil arms Weaver's
// @at temporal timer (internal/weaver/temporal.go) at a deadline, the fired
// timer's MarkExpired records that lapse under THIS target's own byTarget key
// on the account, and the recorded lapse — not a clock — is what opens the gap.
//
// The lifecycle of one arrears episode, on a ledger that stores no balance:
//
//   - An account nothing has ever evaluated projects evaluatedAt = null and is
//     violating from its first projection — which is exactly how every such
//     account, with charges or without, gets its first evaluation: one op
//     each, then quiet.
//   - EvaluateLoftspaceArrears replays the account's own history, and where a
//     charge is open stamps .arrears.dueAt = that charge's own recorded due
//     date (its postedAt when it recorded none) and .arrears.remindAt = dueAt
//     plus the grace (RECORDED time facts, written by the op). While no timer
//     has fired at that reminder instant the row projects freshUntil =
//     remindAt → Weaver arms an @at there. missing_evaluation is false.
//   - At remindAt the @at fires → MarkExpired's freshnessExpiry marker on this
//     account records the fired instant under this target's key AND
//     re-projects the row → the recorded lapse now reaches remindAt →
//     missing_evaluation flips true and freshUntil goes null (a one-shot
//     wake-up, not re-armed).
//   - Weaver dispatches directOp(EvaluateLoftspaceArrears) — driven by the
//     violating row, not by a timer. The op recomputes the FIFO head and
//     stamps .arrears.remindedFor = the due date it reminded for, alongside
//     the notification it fires → re-projection → remindedFor = dueAt →
//     missing_evaluation false, freshUntil null. Converged, and no second
//     reminder for this episode however many times the row is re-evaluated.
//   - EVERY posted entry marks .arrears.stale (post_entry cannot see a
//     balance, so it cannot tell a clearing payment from a partial one or a
//     new charge from one queued behind the head), which opens the gap
//     directly (no timer involved) so the evaluation recomputes. Its rewrite
//     drops stale; a recomputed remindAt later than the recorded lapse re-arms
//     freshUntil with no clearing write at all, and a history that nets to
//     nothing owed rewrites .arrears to {evaluatedAt} alone: no remindAt, so
//     no timer and no gap, and nothing of the finished episode survives to
//     make the NEXT charge look already reminded. Where the next charge posted
//     BEFORE that evaluation ran, the op finds an episode opener newer than
//     the recorded send and drops the send record itself, so the row re-arms
//     for the new episode with remindedFor absent.
//   - The marker is permanent (orchestration-base merges, never clears), and
//     a lapse recorded for an EARLIER episode can reach a new episode's
//     remindAt only when that remindAt already lies in the past — a new head
//     whose recorded due date plus the grace precedes an instant that has
//     already fired, which a late-posted recurring rent can produce. The gap
//     it then opens is a genuine one, not a spurious send: the op sends only
//     on remindAt <= evaluatedAt, and every evaluation runs after the lapse
//     it was opened by. The old marker can hurry a truly overdue episode's
//     first evaluation; it cannot make one send early.
//   - A history longer than one page of the op's postedTo enumeration is
//     replayed ACROSS dispatches: each page records its running aggregate,
//     the cursor to resume from and a phase that flips on every page on
//     .arrears.replay (the checkpoint), and only the page that exhausts the
//     enumeration computes the head and writes .arrears without it. While the
//     checkpoint stands the row is mid-replay: missing_evaluation is false
//     (its replay conjunct), freshUntil is null (no timer arms at a reminder
//     instant the replay has not confirmed), and exactly one of the two phase
//     gaps is open — missing_replay_a on phase a, missing_replay_b on phase b
//     — each a directOp(EvaluateLoftspaceArrears) on the playbook. A
//     checkpoint whose phase is neither value re-opens missing_evaluation
//     instead (the conjunct admits it), so a malformed checkpoint restarts the
//     evaluation rather than leaving the row with no gap at all. A page
//     written under phase a CLOSES missing_replay_a and OPENS
//     missing_replay_b, so the gap that dispatched a page is closed by that
//     page's own write (its mark and dispatch count cleared) and the next
//     page is dispatched by the other gap on the row's re-projection: a
//     level-triggered gap that stayed OPEN across successful dispatches would
//     instead hold its mark for the whole lease and accrue its dispatch count
//     toward the retry budget. The finalize page writes no replay → every gap
//     false. Every posted entry drops the checkpoint along with the stale
//     mark it always sets (post_entry has no balance to distinguish shapes
//     with, so it carries every entry the same way), so the phase gap closes,
//     missing_evaluation re-opens, and the next evaluation starts at page 1.
//     replaying and replayPages are the operator's view of the same state.
//   - An account whose transaction history outran the op's replay budget
//     carries historyTooLong with historyBudget, the entry count it
//     exhausted. The flag suppresses the timer outright — a remindAt a
//     degraded evaluation carried is not an instant to arm on, exactly as
//     stale is not — and suppresses the gap while the recorded budget is at
//     least the current one. That pairing is the point: the op cannot compute
//     a head for such an account, so a gap that stayed open would have Weaver
//     re-dispatch the same doomed evaluation on every window with nothing
//     sent and nothing said, and a timer armed at a remindAt no evaluation
//     could confirm would fire against a head nobody knows. Quiet, but
//     VISIBLE — the row stays in the weaver-targets bucket carrying the flag,
//     which is the operator's signal. The next posted entry drops the flag
//     (post_entry's carry) and sets stale, buying exactly one more attempt. A
//     flag recorded under a SMALLER budget than the current one — or under
//     none (historyBudget absent) — does not suppress the gap:
//     missing_evaluation's fourth arm opens it for exactly one evaluation
//     under the current budget, which either finalizes or re-records the flag
//     at the current budget, so a raised budget reaches the accounts the old
//     one parked, once.
//
// missing_evaluation's lapse arm carries no `remindAt <> null` conjunct. It
// would be dead: the arm's own byTarget >= remindAt comparison is already
// false on a null remindAt (a null operand makes the range test false, never
// true), so nothing reaches that arm without a recorded reminder instant.
// freshUntil KEEPS its null test — there the comparison it guards is negated,
// and NOT(false) is true. The historyBudget test leans on the same rule the
// other way: `historyBudget >= N` is false when the field is absent, so NOT
// of it is true, and an account flagged with no recorded budget reads as
// flagged under a smaller one.
//
// The lens reads NO clock. Both operands of every comparison are stored graph
// data, so the row is a pure function of the subgraph and two projections at
// different wall-clock instants over the same graph agree.
//
// One row per anchor, no walk at all. dueAt, remindAt, remindedFor,
// reminderSentAt, stale, historyTooLong, historyBudget, evaluatedAt,
// replaying and replayPages are INFORMATIONAL columns (operator
// observability); only entityKey + freshUntil + the four bools (violating and
// the three gaps) are load-bearing for Weaver's dispatch and temporal lanes,
// and maxretries_evaluation is the retry cap (retry_budget.go) — carried
// only on missing_evaluation; the two phase gaps use the engine's default
// retry budget, since each gap episode is exactly one dispatch (§ above).
//
// Built with fmt.Sprintf: %[1]s is the target id, from the constant the
// WeaverTargetSpec uses; %[3]d the current replay budget in entries
// (ArrearsPageLimit × ArrearsMaxPages, scripts.go) that a recorded
// historyBudget is measured against; %[4]s / %[5]s the two phase literals
// (ArrearsPhaseA / ArrearsPhaseB) the op writes; %[2]d the retry cap. The
// cypher has no negated relationship pattern at all, only scalar NOT
// comparisons.
var arrearsRemindersSpec = fmt.Sprintf(`MATCH (a:account {key: $actorKey})
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.arrears.data.dueAt AS dueAt,
  a.arrears.data.remindAt AS remindAt,
  a.arrears.data.remindedFor AS remindedFor,
  a.arrears.data.sentAt AS reminderSentAt,
  a.arrears.data.stale AS stale,
  a.arrears.data.historyTooLong AS historyTooLong,
  a.arrears.data.historyBudget AS historyBudget,
  a.arrears.data.evaluatedAt AS evaluatedAt,
  (a.arrears.data.replay <> null) AS replaying,
  a.arrears.data.replay.pages AS replayPages,
  CASE WHEN (a.arrears.data.remindAt <> null) AND (a.arrears.data.replay = null) AND (a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND NOT (a.arrears.data.stale = true) AND NOT (a.arrears.data.historyTooLong = true) AND NOT (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.remindAt) THEN a.arrears.data.remindAt ELSE null END AS freshUntil,
  (
    NOT ((a.arrears.data.historyTooLong = true) AND (a.arrears.data.historyBudget >= %[3]d))
    AND ((a.arrears.data.replay = null) OR (NOT (a.arrears.data.replay.phase = '%[4]s') AND NOT (a.arrears.data.replay.phase = '%[5]s')))
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.remindAt))
      OR ((a.arrears.data.historyTooLong = true) AND NOT (a.arrears.data.historyBudget >= %[3]d))
    )
  ) AS missing_evaluation,
  (a.arrears.data.replay.phase = '%[4]s') AS missing_replay_a,
  (a.arrears.data.replay.phase = '%[5]s') AS missing_replay_b,
  (
    (
      NOT ((a.arrears.data.historyTooLong = true) AND (a.arrears.data.historyBudget >= %[3]d))
      AND ((a.arrears.data.replay = null) OR (NOT (a.arrears.data.replay.phase = '%[4]s') AND NOT (a.arrears.data.replay.phase = '%[5]s')))
      AND (
        (a.arrears.data.evaluatedAt = null)
        OR (a.arrears.data.stale = true)
        OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.remindAt))
        OR ((a.arrears.data.historyTooLong = true) AND NOT (a.arrears.data.historyBudget >= %[3]d))
      )
    )
    OR (a.arrears.data.replay.phase = '%[4]s')
    OR (a.arrears.data.replay.phase = '%[5]s')
  ) AS violating,
  %[2]d AS maxretries_evaluation`, ArrearsRemindersTarget, maxArrearsEvaluationRetries, ArrearsPageLimit*ArrearsMaxPages, ArrearsPhaseA, ArrearsPhaseB)

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the lease so the FE can filter/group by leaseAppKey
// with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a transaction
// projects a row only when it is genuinely posted to a live account held for a
// live lease (the normal shape every DebitAccount/CreditAccount commit
// produces). The per-row key is the transaction key (the IntoKey default), so
// the read model is keyed by vtx.transaction.<id>; transactionKey repeats it in
// the body for the reader.
//
// periodStart / periodEnd / dueAt are the recurring charge's own recorded
// billing period and due date (DebitAccount stamps them on the .entry from
// the clause's anniversary grid); null on a payment or a one-time charge.
//
// The trailing OPTIONAL MATCH walks authorizedBy to a semantic-contracts clause
// (Fire V4 "why was I charged this?") — OPTIONAL because a plain human-
// submitted DebitAccount/CreditAccount carries no clauseRef, and this lens
// projects a row for every transaction regardless. clausePurpose is the
// clause's recorded .terms.purpose (null on every clause minted without one):
// the statement holds a purpose=deposit entry — the charge and, once the
// tenancy has ended, ReturnDeposit's credit, both authorizedBy the same
// clause — apart from rent by this column, never by the memo. No compile-time
// dependency on semantic-contracts: the cypher matches a vertex by class
// label at read time, same as any other package's lens matching a
// cross-package link.
const ledgerHistorySpec = `MATCH (t:transaction)
MATCH (t)-[:postedTo]->(a:account)
MATCH (a)-[:heldFor]->(l:leaseapp)
OPTIONAL MATCH (t)-[:authorizedBy]->(c:clause)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.kind AS kind,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  t.entry.data.periodStart AS periodStart,
  t.entry.data.periodEnd AS periodEnd,
  t.entry.data.dueAt AS dueAt,
  c.key AS clauseKey,
  c.prose.data.text AS clauseProse,
  c.terms.data.purpose AS clausePurpose`

// leaseAccountsSpec projects one row per lease — the anchor is the leaseapp
// (not the account), so a lease with no ledger account yet still gets a row
// (accountKey null), which is exactly the "has this lease opened an
// account" query the FE needs before its first-ever charge or payment.
// OPTIONAL MATCH: the heldFor hop legitimately has no match for a lease that
// has never had a charge/payment.
//
// The three arrears columns come off the account's own .arrears aspect and are
// INFORMATIONAL — this lens drives no convergence. They are here because the
// landlord ledger, the tenant statement and the portfolio list all need to say
// WHEN the rent fell due and WHEN a reminder went out, and this is already the
// per-lease row all three read; the alternative was a second bucket keyed by
// account for three scalars. They are null for a lease with no account, and
// for an account nothing has yet aged.
const leaseAccountsSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)<-[:heldFor]-(a:account)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  a.key AS accountKey,
  a.arrears.data.dueAt AS arrearsDueAt,
  a.arrears.data.remindedFor AS arrearsRemindedFor,
  a.arrears.data.sentAt AS arrearsReminderSentAt`
