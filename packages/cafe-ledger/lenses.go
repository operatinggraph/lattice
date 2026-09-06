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
				BodyColumns:      []string{"violating", "missing_evaluation", "entityKey", "freshUntil", "leaseAppKey", "dueAt", "remindedFor", "reminderSentAt", "stale", "historyTooLong", "evaluatedAt"},
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
// DebitAccount/CreditCafeAccount/RefundCafeCharge commit produces). The
// per-row key is the transaction key (the IntoKey default), so the read model
// is keyed by vtx.cafetransaction.<id>; transactionKey repeats it in the body
// for the reader.
//
// The last two hops ARE optional, and both are anchor-rooted out-hops (no new
// anchor, still partitionable by the transaction):
//
//   - reverses, present only on a refund, names the charge being given back.
//     It is what lets a statement say "this line is a correction of that one"
//     instead of rendering a refund identically to cash the resident handed
//     over — the entry itself is an ordinary credit, deliberately, so every
//     balance consumer sums it unchanged.
//   - settles, present only on a charge posted by the cafeTabSettlement
//     playbook, names the tab it settled. It is what tells a reader which
//     debits are refundable café charges at all: a hand-posted debit with no
//     tab behind it has no counter transaction to correct.
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
//   - An account whose transaction history outran the op's replay budget carries
//     historyTooLong, and it suppresses BOTH the gap and the timer. That pairing
//     is the point: the op cannot compute a head for such an account, so a gap
//     that stayed open would have Weaver re-dispatch the same doomed evaluation
//     on every window with nothing sent and nothing said, and a timer armed at a
//     dueAt no evaluation could confirm would fire against a head nobody knows.
//     Quiet, but VISIBLE — the row stays in the weaver-targets bucket carrying
//     the flag, which is the operator's signal. The next posted entry drops the
//     flag (post_entry's carry) and sets stale, buying exactly one more attempt.
//
// missing_evaluation's third arm carries no `dueAt <> null` conjunct. It would be
// dead: the arm's own byTarget >= dueAt comparison is already false on a null
// dueAt (a null operand makes the range test false, never true), so nothing
// reaches that arm without a recorded due date. freshUntil KEEPS its null test —
// there the comparison it guards is negated, and NOT(false) is true.
//
// The lens reads NO clock. Both operands of every comparison are stored graph
// data, so the row is a pure function of the subgraph and two projections at
// different wall-clock instants over the same graph agree.
//
// One row per anchor: heldFor is 0..1 (CreateAccount writes exactly one, guarded
// create-only by the lease's .cafeLedgerAccount aspect), so the OPTIONAL walk
// cannot fan out — and it is OPTIONAL, so an account with no lease still
// projects a row, with a null leaseAppKey. leaseAppKey, dueAt, remindedFor,
// reminderSentAt, stale, historyTooLong and evaluatedAt are INFORMATIONAL
// columns (the playbook's leaseAppKey param and operator observability); only
// entityKey + freshUntil + the two bools are load-bearing for Weaver's dispatch
// and temporal lanes.
//
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors' static
// reach; its advisory asks for a hand check for a narrowing range bound inside
// a NEGATED pattern, and there is none — the cypher has no negated relationship
// pattern at all, only scalar NOT comparisons.
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
  a.arrears.data.evaluatedAt AS evaluatedAt,
  CASE WHEN (a.arrears.data.dueAt <> null) AND (a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND NOT (a.arrears.data.stale = true) AND NOT (a.arrears.data.historyTooLong = true) AND NOT (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt) THEN a.arrears.data.dueAt ELSE null END AS freshUntil,
  (
    NOT (a.arrears.data.historyTooLong = true)
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
    )
  ) AS missing_evaluation,
  (
    NOT (a.arrears.data.historyTooLong = true)
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
    )
  ) AS violating`, CafeArrearsRemindersTarget)
