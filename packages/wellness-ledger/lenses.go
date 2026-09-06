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
// booking, which is already tombstoned by the time the marker exists).
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
	}
}

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
// className/classStartsAt are read off the matched booking's own .status
// snapshot (nsbk.status / cpbk.status — bookingStatusAspectTypeDDL,
// wellness-domain/ddls.go) or the matched refund's own .detail snapshot
// (rf.detail — refundDetailAspectTypeDDL, same file), never by walking
// forSession to the session: CreateBooking/JoinWaitlist snapshot the
// session's .schedule.name/.schedule.startsAt onto the booking at booking
// time, and CancelBooking copies that same snapshot onto a refund marker it
// mints, precisely because the session a charge or refund was for can later
// be TombstoneSession'd — Contract #1's isDeleted read-filtering means a
// forSession→session walk simply stops matching once that happens, dropping
// the class name from every transaction it ever charged (mirrors
// clinic-reminders' atSite link precedent, commit 4da005a0 — write the
// snapshot once, at op time, onto state that survives the tombstone, instead
// of re-deriving it from a vertex that might not). Projecting a real class
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
  coalesce(nsbk.status.data.className, cpbk.status.data.className, rf.detail.data.className) AS className,
  coalesce(nsbk.status.data.classStartsAt, cpbk.status.data.classStartsAt, rf.detail.data.classStartsAt) AS classStartsAt`

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
const memberAccountsSpec = `MATCH (id:identity)<-[:bookedBy]-(bk:booking)
WITH DISTINCT id
OPTIONAL MATCH (id)<-[:heldFor]-(a:wellnessaccount)
RETURN
  id.key AS key,
  id.key AS identityKey,
  a.key AS accountKey`
