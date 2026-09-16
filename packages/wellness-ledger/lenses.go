package wellnessledger

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this
// member had": the billing-history FE reads THIS projected bucket (one entry
// per transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "wellness-ledger-history"

// MemberAccountsBucket is the NATS-KV read model the wellnessMemberAccounts
// lens projects into — one row per member IDENTITY that has ever booked
// (whether or not a ledger account has been opened yet), carrying the
// account's key when one exists. Since the account carries its own
// independently-minted NanoID (never derived from the identity's), the FE
// cannot compute an account key by string manipulation the way it once
// could — this lens is the P5 query surface for "does this member have a
// ledger account, and what is its key."
const MemberAccountsBucket = "wellness-member-accounts"

// NoShowSettlementTarget is the §10.8 TargetID == the wellnessNoShowSettlement
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const NoShowSettlementTarget = "wellnessNoShowSettlement"

// ClassPriceSettlementTarget is the §10.8 TargetID == the
// wellnessClassPriceSettlement lens's OutputKeyPattern prefix — the
// §10.2↔§10.8 binding Weaver reads. A separate target from
// NoShowSettlementTarget: a class price is owed regardless of attendance
// outcome (unconditional on booking .status), whereas the no-show fee gates
// on status='noShow' — two independent gaps over the same booking, converged
// by two independent settlesClassPrice/settles links so neither's count()
// sees the other's transaction.
const ClassPriceSettlementTarget = "wellnessClassPriceSettlement"

// RefundSettlementTarget is the §10.8 TargetID == the wellnessRefundSettlement
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
// Anchored on wellnessrefund (wellness-domain/ddls.go), not booking: the
// booking a refund traces back to is already tombstoned by the time the
// refund marker exists (CancelBooking mints both in the same mutation
// batch), so the gap must anchor on a vertex that survives — the exact
// reason the marker exists at all (see wellness-domain's refundVertexTypeDDL
// doc comment, ddls.go).
const RefundSettlementTarget = "wellnessRefundSettlement"

// ArrearsRemindersTarget is the §10.8 TargetID == the wellnessArrearsReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads, and
// the key the freshnessExpiry marker records this target's own fired timer
// under.
const ArrearsRemindersTarget = "wellnessArrearsReminders"

// arrearsOp is the Weaver-dispatched arrears evaluation (accountDDLScript).
const arrearsOp = "EvaluateWellnessArrears"

// Lenses returns the package's Lens declarations: wellnessLedgerHistory (one
// row per posted transaction, flattening the .entry aspect + the account/
// identity it posted to into a query-optimized read-model row — the FE
// derives a running balance client-side by summing amountCents, positive for
// debit, negative for credit, over rows for a given identityKey/accountKey;
// the ledger itself never stores a mutable running total), wellnessMemberAccounts
// (the member -> account key lookup, since the account key is no longer
// derivable), wellnessNoShowSettlement (the missing_account/missing_charge
// convergence lens targets.go's WeaverTargets dispatches
// WellnessCreateAccount/WellnessDebitAccount over), and
// wellnessClassPriceSettlement (the missing_account/missing_price_charge
// convergence lens — the OTHER wellness billing gap, a class's booking price,
// converged unconditionally on attendance rather than gated on a noShow), and
// wellnessRefundSettlement (the missing_refund convergence lens — reverses a
// class-price charge already posted before its booking was cancelled,
// anchored on wellness-domain's wellnessrefund marker vertex rather than the
// booking, which is already tombstoned by the time the marker exists), and wellnessArrearsReminders
// (the one-row-per-account arrears convergence lens whose playbook
// dispatches EvaluateWellnessArrears — the cafeArrearsReminders mechanism
// applied to this ledger).
// Prefixed like the package's DDLs (ddls.go): a Lens canonicalName is global
// across every installed package, and loftspace-ledger already owns the bare
// `ledgerHistory` name.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "wellnessLedgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "wellnessMemberAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        MemberAccountsBucket,
			Engine:        "full",
			Spec:          memberAccountsSpec,
		},
		{
			CanonicalName:  NoShowSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           noShowSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: NoShowSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_account", "missing_charge", "entityKey", "bookingKey", "identityKey", "accountKey", "feeCents", "status", "memo", "maxretries_charge"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  ClassPriceSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           classPriceSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "booking",
				OutputKeyPattern: ClassPriceSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_account", "missing_price_charge", "entityKey", "bookingKey", "identityKey", "accountKey", "priceCents", "sessionName", "maxretries_price_charge"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  RefundSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           refundSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "wellnessrefund",
				OutputKeyPattern: RefundSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_refund", "entityKey", "refundKey", "accountKey", "amountCents", "memo", "maxretries_refund"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
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
				AnchorType:       "wellnessaccount",
				OutputKeyPattern: ArrearsRemindersTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_evaluation", "missing_replay_a", "missing_replay_b", "entityKey", "freshUntil", "dueAt", "remindedFor", "reminderSentAt", "stale", "historyTooLong", "historyBudget", "evaluatedAt", "replaying", "replayPages", "maxretries_evaluation"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// arrearsRemindersSpec is the one-row-per-account arrears convergence cypher —
// cafe-ledger's cafeArrearsRemindersSpec applied to a wellnessaccount, minus
// the heldFor hop (the identity the reminder addresses is the op's to resolve
// from state, never a row column: a Params entry bound to an optional hop is a
// dispatch refusal on every row where the hop misses). freshUntil arms
// Weaver's @at temporal timer (internal/weaver/temporal.go) at a deadline, the
// fired timer's MarkExpired records that lapse under THIS target's own
// byTarget key on the account, and the recorded lapse — not a clock — is what
// opens the gap.
//
// The lifecycle of one arrears episode, on a ledger that stores no balance:
//
//   - An account nothing has ever evaluated projects evaluatedAt = null and is
//     violating from its first projection — which is exactly how every such
//     account, with charges or without, gets its first evaluation: one op
//     each, then quiet.
//   - EvaluateWellnessArrears replays the account's own history, and where a
//     charge is open stamps .arrears.dueAt = that charge's postedAt + the
//     ledger's net term (a RECORDED time fact, written by the op). While no
//     timer has fired at that deadline the row projects freshUntil = dueAt →
//     Weaver arms an @at there. missing_evaluation is false.
//   - At dueAt the @at fires → MarkExpired's freshnessExpiry marker on this
//     account records the fired instant under this target's key AND
//     re-projects the row → the recorded lapse now reaches dueAt →
//     missing_evaluation flips true and freshUntil goes null (a one-shot
//     wake-up, not re-armed).
//   - Weaver dispatches directOp(EvaluateWellnessArrears) — driven by the
//     violating row, not by a timer. The op recomputes the FIFO head and
//     stamps .arrears.remindedFor = the due date it reminded for, alongside
//     the notification it fires → re-projection → remindedFor = dueAt →
//     missing_evaluation false, freshUntil null. Converged, and no second
//     reminder for this episode however many times the row is re-evaluated.
//   - EVERY posted entry marks .arrears.stale (post_entry cannot see a
//     balance, so it cannot tell a clearing payment from a partial one or a
//     new charge from one queued behind the head), which opens the gap
//     directly (no timer involved) so the evaluation recomputes. Its rewrite
//     drops stale; a recomputed dueAt later than the recorded lapse re-arms
//     freshUntil with no clearing write at all, and a history that nets to
//     nothing owed rewrites .arrears to {evaluatedAt} alone: no dueAt, so no
//     timer and no gap, and nothing of the finished episode survives to make
//     the NEXT charge look already reminded. Where the next charge posted
//     BEFORE that evaluation ran, the op finds an episode opener newer than
//     the recorded send and drops the send record itself, so the row re-arms
//     for the new episode with remindedFor absent.
//   - A new episode's dueAt is necessarily later than any instant already
//     recorded in the marker (its charge posts after the last episode's
//     ended, and both add the same term), so the permanent marker never
//     poisons it.
//   - A history longer than one page of the op's postedTo enumeration is
//     replayed ACROSS dispatches: each page records its running aggregate,
//     the cursor to resume from and a phase that flips on every page on
//     .arrears.replay (the checkpoint), and only the page that exhausts the
//     enumeration computes the head and writes .arrears without it. While the
//     checkpoint stands the row is mid-replay: missing_evaluation is false
//     (its replay conjunct), freshUntil is null (no timer arms at a due date
//     the replay has not confirmed), and exactly one of the two phase gaps is
//     open — missing_replay_a on phase a, missing_replay_b on phase b — each
//     a directOp(EvaluateWellnessArrears) on the playbook. A checkpoint whose
//     phase is neither value re-opens missing_evaluation instead (the
//     conjunct admits it), so a malformed checkpoint restarts the evaluation
//     rather than leaving the row with no gap at all. A page written under
//     phase a CLOSES missing_replay_a and OPENS missing_replay_b, so the gap
//     that dispatched a page is closed by that page's own write (its mark
//     and dispatch count cleared) and the next page is dispatched by the
//     other gap on the row's re-projection: a level-triggered gap that
//     stayed OPEN across successful dispatches would instead hold its mark
//     for the whole lease and accrue its dispatch count toward the retry
//     budget. The finalize page writes no replay → every gap false. A posted
//     entry mid-replay drops the checkpoint and sets stale (post_entry's
//     carry), so the phase gap closes, missing_evaluation re-opens, and the
//     next evaluation starts at page 1. replaying and replayPages are the
//     operator's view of the same state.
//   - An account whose transaction history outran the op's replay budget
//     carries historyTooLong with historyBudget, the entry count it
//     exhausted. The flag suppresses the timer outright — a dueAt a degraded
//     evaluation carried is not a date to arm on, exactly as stale is not —
//     and suppresses the gap while the recorded budget is at least the
//     current one. That pairing is the point: the op cannot compute a head
//     for such an account, so a gap that stayed open would have Weaver
//     re-dispatch the same doomed evaluation on every window with nothing
//     sent and nothing said, and a timer armed at a dueAt no evaluation
//     could confirm would fire against a head nobody knows. Quiet, but
//     VISIBLE — the row stays in the weaver-targets bucket carrying the
//     flag, which is the operator's signal. The next posted entry drops the
//     flag (post_entry's carry) and sets stale, buying exactly one more
//     attempt. A flag recorded under a SMALLER budget than the current one —
//     or under none (historyBudget absent) — does not suppress the gap:
//     missing_evaluation's fourth arm opens it for exactly one evaluation
//     under the current budget, which either finalizes or re-records the
//     flag at the current budget, so a raised budget reaches the accounts
//     the old one parked, once.
//
// missing_evaluation's lapse arm carries no `dueAt <> null` conjunct. It
// would be dead: the arm's own byTarget >= dueAt comparison is already false
// on a null dueAt (a null operand makes the range test false, never true), so
// nothing reaches that arm without a recorded due date. freshUntil KEEPS its
// null test — there the comparison it guards is negated, and NOT(false) is
// true. The historyBudget test leans on the same rule the other way:
// `historyBudget >= N` is false when the field is absent, so NOT of it is
// true, and an account flagged with no recorded budget reads as flagged
// under a smaller one.
//
// The lens reads NO clock. Both operands of every comparison are stored graph
// data, so the row is a pure function of the subgraph and two projections at
// different wall-clock instants over the same graph agree.
//
// One row per anchor, no walk at all. dueAt, remindedFor, reminderSentAt,
// stale, historyTooLong, historyBudget, evaluatedAt, replaying and
// replayPages are INFORMATIONAL columns (operator observability); only
// entityKey + freshUntil + the four bools (violating and the three gaps) are
// load-bearing for Weaver's dispatch and temporal lanes, and
// maxretries_evaluation is the retry cap the other three targets in this
// package declare the same way (retry_budget.go).
//
// Built with fmt.Sprintf: %[1]s is the target id, from the constant the
// WeaverTargetSpec uses; %[2]d the retry cap (maxArrearsEvaluationRetries,
// retry_budget.go); %[3]d the current replay budget in entries
// (ArrearsPageLimit × ArrearsMaxPages, scripts.go) that a recorded
// historyBudget is measured against; %[4]s / %[5]s the two phase literals
// (ArrearsPhaseA / ArrearsPhaseB) the op writes. The cypher has no negated
// relationship pattern at all, only scalar NOT comparisons.
var arrearsRemindersSpec = fmt.Sprintf(`MATCH (a:wellnessaccount {key: $actorKey})
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
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
    NOT ((a.arrears.data.historyTooLong = true) AND (a.arrears.data.historyBudget >= %[3]d))
    AND ((a.arrears.data.replay = null) OR (NOT (a.arrears.data.replay.phase = '%[4]s') AND NOT (a.arrears.data.replay.phase = '%[5]s')))
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
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
        OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
        OR ((a.arrears.data.historyTooLong = true) AND NOT (a.arrears.data.historyBudget >= %[3]d))
      )
    )
    OR (a.arrears.data.replay.phase = '%[4]s')
    OR (a.arrears.data.replay.phase = '%[5]s')
  ) AS violating,
  %[2]d AS maxretries_evaluation`, ArrearsRemindersTarget, maxArrearsEvaluationRetries, ArrearsPageLimit*ArrearsMaxPages, ArrearsPhaseA, ArrearsPhaseB)

// noShowSettlementSpec is the one-row-per-booking convergence cypher: a
// noShow booking carrying a positive noShowFeeCents needs its charge posted
// onto the booker's wellness-ledger account, once — two independent gaps,
// mirroring clinic-ledger's identical clinicNoShowSettlement shape:
//
//   - `missing_account` — the booking is a noShow, carries a fee, and the
//     booker has no wellnessaccount yet (accountKey null). Weaver dispatches
//     WellnessCreateAccount{identityKey} (targets.go), opening the account
//     lazily on first no-show rather than requiring it pre-exist.
//   - `missing_charge` — the booking is a noShow, carries a fee, the booker
//     has a ledger account, and no wellnesstransaction `settles` this
//     booking yet (count(tx.key) collapses the fan to a single existence
//     check — the objectLiveness/clauseSatisfaction idiom, same as
//     clinic-ledger's clinicNoShowSettlement). Weaver dispatches
//     WellnessDebitAccount{accountKey, amountCents, bookingRef} (targets.go) — the
//     bookingRef extension writes the settles audit link this OPTIONAL
//     MATCH walks, so once posted the gap converges and stays converged
//     (noShow carries forward once SetBookingAttendance sets it, and a
//     re-mark to attended is the mirror correction — see targets.go's doc
//     comment on that edge).
//
// Once missing_account converges (WellnessCreateAccount writes the identity's
// .wellnessLedgerAccount guard aspect), the next lens tick reads the now-real
// accountKey and missing_charge takes over — the same lazy account-open relay
// clinicNoShowSettlement uses. A booking with no noShowFeeCents (a noShow set
// before this lens existed) never violates either gap — a non-goal for v1,
// not a gap this lens is meant to converge.
// noShowSettlementSpec is built once at package init: the retry cap
// (maxChargeRetries) bakes into the constant maxretries_charge column, the
// §10.2 "the policy lives in the cypher" convention lease-signing's
// leaseApplicationCompleteSpec established. The cypher carries no literal '%'.
var noShowSettlementSpec = fmt.Sprintf(`MATCH (bk:booking {key: $actorKey})
MATCH (bk)-[:bookedBy]->(id:identity)
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
OPTIONAL MATCH (bk)<-[:settles]-(tx:wellnesstransaction)
WITH
  bk.key AS entityKey,
  bk.status.data.value AS status,
  bk.status.data.noShowFeeCents AS feeCents,
  id.key AS identityKey,
  a.key AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS bookingKey,
  identityKey,
  accountKey,
  feeCents,
  status,
  'No-show fee' AS memo,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey = null)) AS missing_account,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  (
    ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey = null))
    OR ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0))
  ) AS violating,
  %d AS maxretries_charge
`, maxChargeRetries)

// classPriceSettlementSpec is the one-row-per-booking convergence cypher for
// the OTHER wellness billing gap: a session carrying a positive priceCents
// needs its class-price charge posted onto the booker's wellness-ledger
// account, once — regardless of attendance outcome, but only once the
// booking actually holds a seat. Both gaps below carry a `status = 'booked'`
// clause mirroring noShowSettlementSpec's `status = 'noShow'` gate: a
// `waitlisted` booking holds no seat yet (find_promotion_candidate,
// wellness-domain/ddls.go, is what flips it to booked when one frees up), so
// it must not be charged — charging on write would bill a class the booker
// may never attend, with no promotion-independent event to trigger a refund.
// Two independent gaps, mirroring noShowSettlementSpec's own
// missing_account/missing_charge split:
//
//   - `missing_account` — the booking is booked, its session carries an
//     effective price > 0, and the booker has no wellnessaccount yet
//     (accountKey null). Weaver dispatches WellnessCreateAccount{identityKey}
//     (targets.go), opening the account lazily on first priced booking
//     rather than requiring it pre-exist.
//
//   - `missing_price_charge` — the booking is booked, its session carries an
//     effective price > 0, the booker has a ledger account, and no
//     wellnesstransaction `settlesClassPrice` this booking yet
//     (count(tx.key) collapses the fan to a single existence check, the same
//     objectLiveness/clauseSatisfaction idiom noShowSettlementSpec uses).
//     Weaver dispatches WellnessDebitAccount{accountKey, amountCents,
//     priceBookingRef} (targets.go) — the priceBookingRef extension writes
//     the settlesClassPrice audit link this OPTIONAL MATCH walks (a DISTINCT
//     relation from settles, so this lens's count() never sees a no-show-fee
//     transaction and vice versa), so once posted the gap converges and
//     stays converged.
//
//   - `priceCents` — the EFFECTIVE price this booking owes, not the session's
//     raw priceCents: a booking whose own .status.rate is "resident" charges
//     the session's residentPriceCents when the session declares one, else
//     falls back to priceCents exactly like a standard booking (verticals.md
//     "a verified resident is charged the same class price as a walk-in" —
//     CreateBooking stamps rate at booking time; the session's
//     residentPriceCents is CreateSession/ReassignSession-owned, see
//     wellness-domain/ddls.go). The CASE WHEN idiom mirrors
//     orchestration-base's unroutedTasksSpec (lenses.go).
//
// Once missing_account converges (WellnessCreateAccount writes the identity's
// .wellnessLedgerAccount guard aspect), the next lens tick reads the now-real
// accountKey and missing_price_charge takes over — the same lazy account-open
// relay noShowSettlementSpec uses.
//
// `MATCH (bk:booking {key: $actorKey})` alone (no isDeleted clause) is enough
// to exclude a cancelled/soft-deleted booking — the full engine's Core-KV
// reads filter isDeleted per Contract #1 (executor.go), so a tombstoned
// booking simply stops matching, mirroring noShowSettlementSpec's identical
// MATCH. A booking whose effective price is null/0 (no priceCents on the
// session, or a free class) never violates either gap — a non-goal for v1,
// not a gap this lens is meant to converge.
// classPriceSettlementSpec is built once at package init: the retry cap
// (maxPriceChargeRetries) bakes into the constant maxretries_price_charge column,
// the same §10.2 "the policy lives in the cypher" convention
// noShowSettlementSpec follows. The cypher carries no literal '%'.
var classPriceSettlementSpec = fmt.Sprintf(`MATCH (bk:booking {key: $actorKey})
MATCH (bk)-[:forSession]->(se:session)
MATCH (bk)-[:bookedBy]->(id:identity)
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
OPTIONAL MATCH (bk)<-[:settlesClassPrice]-(tx:wellnesstransaction)
WITH
  bk.key AS entityKey,
  bk.status.data.value AS status,
  (CASE WHEN (bk.status.data.rate = 'resident') AND (se.schedule.data.residentPriceCents <> null) THEN se.schedule.data.residentPriceCents ELSE se.schedule.data.priceCents END) AS priceCents,
  se.schedule.data.name AS sessionName,
  id.key AS identityKey,
  a.key AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS bookingKey,
  identityKey,
  accountKey,
  priceCents,
  sessionName,
  status,
  ((status = 'booked') AND (priceCents <> null) AND (priceCents > 0) AND (accountKey = null)) AS missing_account,
  ((status = 'booked') AND (priceCents <> null) AND (priceCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_price_charge,
  (
    ((status = 'booked') AND (priceCents <> null) AND (priceCents > 0) AND (accountKey = null))
    OR ((status = 'booked') AND (priceCents <> null) AND (priceCents > 0) AND (accountKey <> null) AND (txCount = 0))
  ) AS violating,
  %d AS maxretries_price_charge
`, maxPriceChargeRetries)

// refundSettlementSpec is the one-row-per-wellnessrefund convergence cypher
// for the REFUND gap: a wellnessrefund marker (wellness-domain's
// CancelBooking/ReleaseOrphanedBooking, ddls.go — minted when a booking
// already carries a posted settlesClassPrice charge, a posted no-show-fee
// charge, or both) needs its credit posted back onto the account it names,
// once. memo projects the marker's OWN detail.memo ("Class price refund" or
// "No-show fee refund", set by whichever mint site wrote this marker)
// verbatim rather than a hardcoded literal — one marker type now reverses
// two different charge shapes, so the credit line must say which.
//
//   - `missing_refund` — the marker carries a live accountKey and a positive
//     amountCents (always true for a well-formed marker — CancelBooking only
//     mints one after resolving both), and no wellnesstransaction
//     `settlesRefund` this marker yet (count(tx.key) collapses the fan to a
//     single existence check, the same objectLiveness/clauseSatisfaction
//     idiom classPriceSettlementSpec/noShowSettlementSpec use). Weaver
//     dispatches WellnessCreditAccount{accountKey, amountCents, refundRef}
//     (targets.go) — the refundRef extension writes the settlesRefund audit
//     link this OPTIONAL MATCH walks, so once posted the gap converges and
//     stays converged (a wellnessrefund is minted at most once per cancelled
//     booking, so there is no later re-violation to guard against).
//
// Anchored on wellnessrefund, not booking — the whole reason this marker
// exists is that the booking it traces back to is ALREADY tombstoned by the
// time it is minted (Contract #1 isDeleted read-filtering), so no lens could
// ever anchor this gap on the booking itself.
// refundSettlementSpec is built once at package init: the retry cap
// (maxRefundRetries) bakes into the constant maxretries_refund column, the
// same §10.2 "the policy lives in the cypher" convention the other two specs
// above follow. The cypher carries no literal '%'.
var refundSettlementSpec = fmt.Sprintf(`MATCH (rf:wellnessrefund {key: $actorKey})
OPTIONAL MATCH (rf)<-[:settlesRefund]-(tx:wellnesstransaction)
WITH
  rf.key AS entityKey,
  rf.detail.data.accountKey AS accountKey,
  rf.detail.data.amountCents AS amountCents,
  coalesce(rf.detail.data.memo, 'Refund') AS refundMemo,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS refundKey,
  accountKey,
  amountCents,
  refundMemo AS memo,
  ((accountKey <> null) AND (amountCents <> null) AND (amountCents > 0) AND (txCount = 0)) AS missing_refund,
  ((accountKey <> null) AND (amountCents <> null) AND (amountCents > 0) AND (txCount = 0)) AS violating,
  %d AS maxretries_refund
`, maxRefundRetries)

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the identity so the FE can filter/group by
// identityKey with no extra hop. Every MATCH through identity is REQUIRED
// (not OPTIONAL): a transaction projects a row only when it is genuinely
// posted to a live account held for a live identity (the normal shape every
// WellnessDebitAccount/WellnessCreditAccount commit produces). The per-row
// key is the transaction key (the IntoKey default), so the read model is
// keyed by vtx.wellnesstransaction.<id>; transactionKey repeats it in the
// body for the reader.
//
// The trailing OPTIONAL MATCHes cover the THREE relations a transaction may
// settle: `settles` (a no-show fee, noShowSettlementSpec above),
// `settlesClassPrice` (a class-price charge, classPriceSettlementSpec above),
// and `settlesRefund` (a class-price refund credit, refundSettlementSpec
// above) — most transactions (a front-desk payment) settle none of the
// three, so nsbk/cpbk/rf simply stay unmatched. At most one of the three is
// ever bound for a given transaction (a wellnesstransaction carries exactly
// one settlement relation, written once atomically at mint time by
// WellnessDebitAccount/WellnessCreditAccount), so `coalesce` across the
// three is never picking between competing live values, only skipping the
// unmatched ones — the same composition primitive pkgmgr Walks uses to fold
// several optionally-bound copies of one variable back to a single name
// (internal/refractor/ruleengine/full/expr_eval.go).
//
// Two more OPTIONAL MATCHes walk `reverses`, the relation a wellnessrefund
// marker carries to the charge it gives back (written unconditionally at
// every mint site — CancelBooking (:4845), SetBookingAttendance (:5097),
// and ReleaseOrphanedBooking's class-price and no-show branches (:5235,
// :5287), wellness-domain/ddls.go): `(rf)-[:reverses]->(rtx)` off THIS row's own
// settlesRefund-linked marker projects reversesKey when this row IS the
// refund credit, and `(t)<-[:reverses]-(rrf)` off this row's transaction
// directly projects rrf when this row IS the reversed charge — the two
// hops read opposite directions off the same relation because a single row
// can be either end of it, never both.
//
// className/classStartsAt are read off the matched booking's own .status
// snapshot (nsbk.status / cpbk.status — bookingStatusAspectTypeDDL,
// wellness-domain/ddls.go), the matched refund's own .detail snapshot
// (rf.detail — refundDetailAspectTypeDDL, same file), or — last —
// the reversing marker's own .detail snapshot (rrf.detail): never by
// walking forSession to the session. CreateBooking/JoinWaitlist snapshot the
// session's .schedule.name/.schedule.startsAt onto the booking at booking
// time, and CancelBooking copies that same snapshot onto a refund marker it
// mints, precisely because the session a charge or refund was for can later
// be TombstoneSession'd — Contract #1's isDeleted read-filtering means a
// forSession→session walk simply stops matching once that happens, dropping
// the class name from every transaction it ever charged (mirrors
// clinic-reminders' atSite link precedent, commit 4da005a0 — write the
// snapshot once, at op time, onto state that survives the tombstone, instead
// of re-deriving it from a vertex that might not). A charge whose OWN
// settles/settlesClassPrice booking has since been tombstoned still needs
// its class name for the reader, and the marker that reverses it carries
// the same snapshot the marker's own credit row reads — rrf.detail is the
// last fallback precisely because it is the only one of the three that
// names a charge rather than the refund itself. Projecting a real class
// name — not just a date, since wellness has one to give a reader (clinic's
// appointment has none) — is what lets a member's billing history tell two
// otherwise identical "No-show fee" lines apart by which class each one
// billed.
const ledgerHistorySpec = `MATCH (t:wellnesstransaction)
MATCH (t)-[:postedTo]->(a:wellnessaccount)
MATCH (a)-[:heldFor]->(id:identity)
OPTIONAL MATCH (t)-[:settles]->(nsbk:booking)
OPTIONAL MATCH (t)-[:settlesClassPrice]->(cpbk:booking)
OPTIONAL MATCH (t)-[:settlesRefund]->(rf:wellnessrefund)
OPTIONAL MATCH (rf)-[:reverses]->(rtx:wellnesstransaction)
OPTIONAL MATCH (t)<-[:reverses]-(rrf:wellnessrefund)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  id.key AS identityKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  t.entry.data.reason AS reason,
  coalesce(nsbk.key, cpbk.key) AS bookingKey,
  coalesce(nsbk.status.data.className, cpbk.status.data.className, rf.detail.data.className, rrf.detail.data.className) AS className,
  coalesce(nsbk.status.data.classStartsAt, cpbk.status.data.classStartsAt, rf.detail.data.classStartsAt, rrf.detail.data.classStartsAt) AS classStartsAt,
  rtx.key AS reversesKey`

// memberAccountsSpec projects one row per identity that has ever BOOKED —
// anchored on the identity itself, with the inbound bookedBy walk from
// booking used only as an existence test (DISTINCT'd — a member with many
// bookings still gets exactly one row) — not one row per platform identity,
// which would scan every LoftSpace tenant / Clinic patient / Café resident
// regardless of whether they ever touched Wellness. A member with no ledger
// account yet still gets a row (accountKey null), which is exactly the "has
// this member opened an account" query the FE needs before its first-ever
// charge or payment. OPTIONAL MATCH: the heldFor hop legitimately has no
// match for a member who has never had a charge/payment.
//
// Anchored on `id:identity`, not `bk:booking`: the row this lens ever emits is
// keyed on id.key, not on the key of any matched booking, so a lens anchored
// on booking partitions by nothing the engine can seed on — every event
// forced a whole-corpus rescan plus a whole-bucket diff (DiffRetraction), and
// no Refractor conjunct could ever admit a per-anchor retraction
// (anchor-partitioned-plain-lens-retraction-design.md §8 row 2). Re-anchoring
// on the identity makes id.key the anchor's OWN key, so the output rows
// PARTITION by anchor (full.CompiledRule.ProjectsOneRowPerAnchor): the engine
// seeds per identity on a bookedBy event and retracts a member's row when
// their last booking's existence test stops matching, with no whole-bucket
// diff. The required inbound MATCH — walking bookedBy backward from the
// anchor rather than forward from booking — mirrors rbac-domain's
// capabilityRoleIndexSpec (`MATCH (role:role)<-[:grantedBy]-(perm:permission)`,
// lenses.go).
//
// The key shape this lens writes is UNCHANGED for its consumer
// (cmd/wellness-app/ledger.go's KVGet(MemberAccountsBucket, identityKey)):
// id.key was always the row's key, and still is — only which vertex the
// engine anchors the evaluation on moved.
//
// The three arrears columns come off the account's own .arrears aspect and
// are INFORMATIONAL — this lens drives no convergence. They are here because
// the front-desk arrears grid and the member's statement both need to say
// WHEN a balance fell due and WHEN a reminder went out, and this is already
// the per-member row both read. arrearsReminderSentAt is the op's recorded
// SEND INTENT (.arrears.sentAt, stamped on the commit that emitted the outbox
// event); the adapter's delivery outcome is .arrearsNotification, which this
// lens does not project. They are null for a member with no account, and for
// an account nothing has yet evaluated.
const memberAccountsSpec = `MATCH (id:identity)<-[:bookedBy]-(bk:booking)
WITH DISTINCT id
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
RETURN
  id.key AS key,
  id.key AS identityKey,
  a.key AS accountKey,
  a.arrears.data.dueAt AS arrearsDueAt,
  a.arrears.data.remindedFor AS arrearsRemindedFor,
  a.arrears.data.sentAt AS arrearsReminderSentAt`
