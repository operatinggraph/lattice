package cafeledger

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// CafeArrearsRemindersTarget is the §10.8 TargetID == the cafeArrearsReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads, and
// the key the freshnessExpiry marker records this target's own fired timer
// under.
const CafeArrearsRemindersTarget = "cafeArrearsReminders"

// arrearsOp is the Weaver-dispatched arrears evaluation (accountDDLScript).
const arrearsOp = "EvaluateCafeArrears"

// LedgerHistoryBucket is the NATS-KV read model the cafeLedgerHistory lens
// projects into. It is the **P5 query surface** for "what café charges/
// payments has this lease had": the house-tab-history FE reads THIS
// projected bucket (one entry per transaction, keyed by the transaction
// key), never Core KV (lattice-architecture.md P5 — lenses are the only
// application query surface). The Refractor auto-creates the bucket on lens
// load.
const LedgerHistoryBucket = "cafe-ledger-history"

// LeaseAccountsBucket is the NATS-KV read model the cafeLeaseAccounts lens
// projects into — one row per LEASE (whether or not a café account has been
// opened yet), carrying the account's key when one exists. Since the
// account carries its own independently-minted NanoID (never derived from
// the lease's), the FE cannot compute an account key by string manipulation
// — this lens is the P5 query surface for "does this lease have a café
// account, and what is its key."
const LeaseAccountsBucket = "cafe-lease-accounts"

// Lenses returns the package's Lens declarations: cafeLedgerHistory (one row
// per posted transaction, flattening the .entry aspect + the account/lease
// it posted to into a query-optimized read-model row — the FE derives a
// running balance client-side by summing amountCents, positive for debit,
// negative for credit, over rows for a given leaseAppKey/accountKey — this
// independent sum is the DISPLAY source of truth, never the account's own
// .balance authorization cache, which no lens reads) and
// cafeLeaseAccounts (the lease -> account key lookup, since the account key
// is no longer derivable). Prefixed like the package's DDLs (ddls.go): a
// Lens canonicalName is global across every installed package, and
// loftspace-ledger already owns the bare `ledgerHistory` / `leaseAccounts`
// names.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "cafeLedgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "cafeLeaseAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseAccountsBucket,
			Engine:        "full",
			Spec:          leaseAccountsSpec,
		},
		{
			CanonicalName:  CafeArrearsRemindersTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           cafeArrearsRemindersSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "cafeaccount",
				OutputKeyPattern: CafeArrearsRemindersTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_evaluation", "missing_replay_a", "missing_replay_b", "entityKey", "freshUntil", "leaseAppKey", "dueAt", "remindedFor", "reminderSentAt", "stale", "historyTooLong", "historyBudget", "evaluatedAt", "replaying", "replayPages"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// ledgerHistorySpec projects one row per transaction, walking postedTo to
// the account and heldFor to the leaseapp so the FE can filter/group by
// leaseAppKey with no extra hop. Those hops are REQUIRED (not OPTIONAL): a
// transaction projects a row only when it is genuinely posted to a live
// account held for a live lease (the normal shape every
// DebitAccount/CreditCafeAccount/RefundCafeCharge/PayoutCafeCredit commit
// produces). The per-row key is the transaction key (the IntoKey default), so
// the read model is keyed by vtx.cafetransaction.<id>; transactionKey repeats
// it in the body for the reader.
//
// reason is what tells a statement what kind of line it is reading: a credit
// was cash collected (payment), forgiven (waiver) or given back (refund); a
// debit was a charge (no reason) or cash paid out (payout). The balance sums
// type alone — reason never changes arithmetic, only the words beside it.
//
// The last two hops ARE optional, and both are anchor-rooted out-hops (no new
// anchor, still partitionable by the transaction):
//
//   - reverses, present only on a refund, names the charge being given back.
//     It is what lets a statement say "this line is a correction of THAT one"
//     — reason says the line is a refund, the hop says of what — and the entry
//     itself is an ordinary credit, deliberately, so every balance consumer
//     sums it unchanged.
//   - settles, present only on an entry posted by the cafeTabSettlement
//     playbook, names the tab it settled — the charge, and the counter
//     payment recorded at settle (a credit carrying the same hop). It is what
//     tells a reader which debits are refundable café charges at all: a
//     hand-posted debit with no tab behind it has no counter transaction to
//     correct — and which credit paid for which tab's lines.
const ledgerHistorySpec = `MATCH (t:cafetransaction)
MATCH (t)-[:postedTo]->(a:cafeaccount)
MATCH (a)-[:heldFor]->(l:leaseapp)
OPTIONAL MATCH (t)-[:reverses]->(rt:cafetransaction)
OPTIONAL MATCH (t)-[:settles]->(tb:tab)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  t.entry.data.reason AS reason,
  rt.key AS reversesKey,
  tb.key AS tabKey`

// leaseAccountsSpec projects one row per lease — the anchor is the lease
// (not the account), so a lease with no café account yet still gets a row
// (accountKey null), which is exactly the "has this lease opened a café
// account" query the FE needs before its first-ever charge or payment.
// OPTIONAL MATCH: the heldFor hop legitimately has no match for a lease that
// has never had a café charge/payment.
//
// The three arrears columns come off the account's own .arrears aspect and are
// INFORMATIONAL — this lens drives no convergence. They are here because the
// front-desk arrears grid and the resident's statement both need to say WHEN a
// reminder went out, and this is already the per-lease row both read; the
// alternative was a second bucket keyed by account for three scalars. They are
// null for a lease with no account, and for an account nothing has yet aged.
const leaseAccountsSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)<-[:heldFor]-(a:cafeaccount)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  a.key AS accountKey,
  a.arrears.data.dueAt AS arrearsDueAt,
  a.arrears.data.remindedFor AS arrearsRemindedFor,
  a.arrears.data.sentAt AS arrearsReminderSentAt`

// cafeArrearsRemindersSpec is the one-row-per-account arrears convergence
// cypher. It is the wellnessBookingReminders mechanism (lenses.go there) applied
// to a house tab: freshUntil arms Weaver's @at temporal timer
// (internal/weaver/temporal.go) at a deadline, the fired timer's MarkExpired
// records that lapse under THIS target's own byTarget key on the account, and
// the recorded lapse — not a clock — is what opens the gap.
//
// The lifecycle of one arrears episode:
//
//   - A charge posted to an account that owed nothing stamps
//     .arrears.dueAt = that charge's postedAt + the ledger's net term
//     (post_entry, scripts.go — a RECORDED time fact, written by the op). While
//     no timer has fired at that deadline the row projects freshUntil = dueAt →
//     Weaver arms an @at there. missing_evaluation is false.
//   - At dueAt the @at fires → MarkExpired's freshnessExpiry marker on this
//     account records the fired instant under this target's key AND re-projects
//     the row → the recorded lapse now reaches dueAt → missing_evaluation flips
//     true and freshUntil goes null (a one-shot wake-up, not re-armed).
//   - Weaver dispatches directOp(EvaluateCafeArrears) — driven by the violating
//     row, not by a timer. The op recomputes the FIFO head over the account's own
//     history and stamps .arrears.remindedFor = the due date it reminded for,
//     alongside the notification it fires → re-projection → remindedFor = dueAt
//     → missing_evaluation false, freshUntil null. Converged, and no second
//     reminder for this episode however many times the row is re-evaluated.
//   - A partial payment can move the FIFO head to a LATER charge, which no
//     single entry can compute; post_entry marks .arrears.stale instead, which
//     opens the gap directly (no timer involved) so the evaluation recomputes.
//     Its rewrite drops stale, and the recomputed dueAt — later than the one the
//     lapse was recorded at — re-arms freshUntil with no clearing write at all.
//   - A payment that clears the balance rewrites .arrears to {evaluatedAt}
//     alone: no dueAt, so no timer and no gap. The NEXT charge opens a fresh
//     episode whose dueAt is necessarily later than any instant already
//     recorded (its own postedAt is later than the last episode's, and both add
//     the same term), so the permanent marker never poisons it.
//   - An account nothing has ever evaluated projects evaluatedAt = null and is
//     violating from its first projection — which is exactly how the accounts
//     standing at install get their first evaluation, one op each, then quiet.
//   - A history longer than one page of the op's postedTo enumeration is
//     replayed ACROSS dispatches: each page records its running aggregate, the
//     cursor to resume from and a phase that flips on every page on
//     .arrears.replay (the checkpoint), and only the page that exhausts the
//     enumeration computes the head and writes .arrears without it. While the
//     checkpoint stands the row is mid-replay: missing_evaluation is false
//     (its replay conjunct), freshUntil is null (no timer arms at a due date
//     the replay has not confirmed), and exactly one of the two phase gaps is
//     open — missing_replay_a on phase a, missing_replay_b on phase b — each
//     a directOp(EvaluateCafeArrears) on the playbook. A checkpoint whose
//     phase is neither value re-opens missing_evaluation instead (the
//     conjunct admits it), so a malformed checkpoint restarts the evaluation
//     rather than leaving the row with no gap at all. A page written
//     under phase a CLOSES missing_replay_a and OPENS missing_replay_b, so the
//     gap that dispatched a page is closed by that page's own write (its mark
//     and dispatch count cleared) and the next page is dispatched by the other
//     gap on the row's re-projection: a level-triggered gap that stayed OPEN
//     across successful dispatches would instead hold its mark for the whole
//     lease and accrue its dispatch count toward the retry budget. The finalize
//     page writes no replay → every gap false. A posted entry mid-replay drops
//     the checkpoint and sets stale (post_entry's carry), so the phase gap
//     closes, missing_evaluation re-opens, and the next evaluation starts at
//     page 1. replaying and replayPages are the operator's view of the same
//     state.
//   - An account whose transaction history outran the op's replay budget carries
//     historyTooLong with historyBudget, the entry count it exhausted. The flag
//     suppresses the timer outright — a dueAt a degraded evaluation carried is
//     not a date to arm on, exactly as stale is not — and suppresses the gap
//     while the recorded budget is at least the current one. That pairing is
//     the point: the op cannot compute a head for such an account, so a gap
//     that stayed open would have Weaver re-dispatch the same doomed evaluation
//     on every window with nothing sent and nothing said, and a timer armed at
//     a dueAt no evaluation could confirm would fire against a head nobody
//     knows. Quiet, but VISIBLE — the row stays in the weaver-targets bucket
//     carrying the flag, which is the operator's signal. The next posted entry
//     drops the flag (post_entry's carry) and sets stale, buying exactly one
//     more attempt. A flag recorded under a SMALLER budget than the current one
//     — or under none (historyBudget absent) — does not suppress the gap:
//     missing_evaluation's fourth arm opens it for exactly one evaluation under
//     the current budget, which either finalizes or re-records the flag at the
//     current budget, so a raised budget reaches the accounts the old one
//     parked, once.
//
// missing_evaluation's lapse arm carries no `dueAt <> null` conjunct. It would
// be dead: the arm's own byTarget >= dueAt comparison is already false on a null
// dueAt (a null operand makes the range test false, never true), so nothing
// reaches that arm without a recorded due date. freshUntil KEEPS its null test —
// there the comparison it guards is negated, and NOT(false) is true. The
// historyBudget test leans on the same rule the other way: `historyBudget >= N`
// is false when the field is absent, so NOT of it is true, and an account
// flagged with no recorded budget reads as flagged under a smaller one.
//
// The lens reads NO clock. Both operands of every comparison are stored graph
// data, so the row is a pure function of the subgraph and two projections at
// different wall-clock instants over the same graph agree.
//
// One row per anchor: heldFor is 0..1 (CreateAccount writes exactly one, guarded
// create-only by the lease's .cafeLedgerAccount aspect), so the OPTIONAL walk
// cannot fan out — and it is OPTIONAL, so an account with no lease still
// projects a row, with a null leaseAppKey. leaseAppKey, dueAt, remindedFor,
// reminderSentAt, stale, historyTooLong, historyBudget, evaluatedAt, replaying
// and replayPages are INFORMATIONAL columns (the playbook's leaseAppKey param
// and operator observability); only entityKey + freshUntil + the four bools
// (violating and the three gaps) are load-bearing for Weaver's dispatch and
// temporal lanes.
//
// Built with fmt.Sprintf: %[1]s is the target id, from the constant the
// WeaverTargetSpec uses; %[2]d the current replay budget in entries
// (ArrearsPageLimit × ArrearsMaxPages, scripts.go) that a recorded
// historyBudget is measured against; %[3]s / %[4]s the two phase literals
// (ArrearsPhaseA / ArrearsPhaseB) the op writes.
//
// The Sprintf puts this Spec out of lint-lens-anchors' static reach; its
// advisory asks for a hand check for a narrowing range bound inside a NEGATED
// pattern, and there is none — the cypher has no negated relationship pattern
// at all, only scalar NOT comparisons.
var cafeArrearsRemindersSpec = fmt.Sprintf(`MATCH (a:cafeaccount {key: $actorKey})
OPTIONAL MATCH (a)-[:heldFor]->(l:leaseapp)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  l.key AS leaseAppKey,
  a.arrears.data.dueAt AS dueAt,
  a.arrears.data.remindedFor AS remindedFor,
  a.arrears.data.sentAt AS reminderSentAt,
  a.arrears.data.stale AS stale,
  a.arrears.data.historyTooLong AS historyTooLong,
  a.arrears.data.historyBudget AS historyBudget,
  a.arrears.data.evaluatedAt AS evaluatedAt,
  (a.arrears.data.replay <> null) AS replaying,
  a.arrears.data.replay.pages AS replayPages,
  CASE WHEN (a.arrears.data.dueAt <> null) AND (a.arrears.data.replay = null) AND (a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND NOT (a.arrears.data.stale = true) AND NOT (a.arrears.data.historyTooLong = true) AND NOT (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt) THEN a.arrears.data.dueAt ELSE null END AS freshUntil,
  (
    NOT ((a.arrears.data.historyTooLong = true) AND (a.arrears.data.historyBudget >= %[2]d))
    AND ((a.arrears.data.replay = null) OR (NOT (a.arrears.data.replay.phase = '%[3]s') AND NOT (a.arrears.data.replay.phase = '%[4]s')))
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
      OR ((a.arrears.data.historyTooLong = true) AND NOT (a.arrears.data.historyBudget >= %[2]d))
    )
  ) AS missing_evaluation,
  (a.arrears.data.replay.phase = '%[3]s') AS missing_replay_a,
  (a.arrears.data.replay.phase = '%[4]s') AS missing_replay_b,
  (
    (
      NOT ((a.arrears.data.historyTooLong = true) AND (a.arrears.data.historyBudget >= %[2]d))
      AND ((a.arrears.data.replay = null) OR (NOT (a.arrears.data.replay.phase = '%[3]s') AND NOT (a.arrears.data.replay.phase = '%[4]s')))
      AND (
        (a.arrears.data.evaluatedAt = null)
        OR (a.arrears.data.stale = true)
        OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
        OR ((a.arrears.data.historyTooLong = true) AND NOT (a.arrears.data.historyBudget >= %[2]d))
      )
    )
    OR (a.arrears.data.replay.phase = '%[3]s')
    OR (a.arrears.data.replay.phase = '%[4]s')
  ) AS violating`, CafeArrearsRemindersTarget, ArrearsPageLimit*ArrearsMaxPages, ArrearsPhaseA, ArrearsPhaseB)
