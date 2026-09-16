// Package wellnessledger is the Wellness member payment ledger: a per-member
// financial account that records charges (no-show fees and class-price
// charges today; a pass/membership product is a future extension of the same
// primitive, out of scope here) and payments as an append-only transaction
// history, never a mutable running total.
//
// It declares:
//
//   - The `wellnessaccount` vertex type (DDL `wellnessaccount`) — WellnessCreateAccount
//     mints vtx.wellnessaccount.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the member's identity —
//     Core KV NanoIDs are unique platform-wide identifiers, not scoped per
//     vertex type), linked to the identity via heldFor. "At most one account
//     per member" is enforced by the `wellnessLedgerAccountGuard` aspect on
//     the identity instead of a shared/derived key.
//
//   - The `wellnessLedgerAccountGuard` aspect type (DDL
//     `wellnessLedgerAccountGuard`) — vtx.identity.<NanoID>.wellnessLedgerAccount
//     = {accountKey}, written once by WellnessCreateAccount alongside the account it
//     names; its deterministic, identity-anchored key is the uniqueness guard.
//
//   - The `wellnesstransaction` vertex type (DDL `wellnesstransaction`) —
//     WellnessDebitAccount (a charge: a no-show fee and/or a class-price charge) and
//     WellnessCreditAccount (a payment received, or reason:"waiver" a charge
//     forgiven) each mint vtx.wellnesstransaction.<NanoID>
//     (root data {} per D5) with a .entry aspect {type, amountCents, memo?, postedAt,
//     reason? (credit only)}, linked to the account via postedTo. WellnessDebitAccount
//     independently accepts bookingRef (no-show settlement, writes settles) and
//     priceBookingRef (class-price settlement, writes settlesClassPrice) — either,
//     both, or neither, two distinct relations so the two settlement gaps never
//     collide in a count(). The ledger is append-only: a balance is derived
//     by summing entries (the wellnessLedgerHistory lens), never stored as a
//     mutable aspect — so concurrent debits/credits never race a
//     read-modify-write. A waiver reduces the balance identically to a
//     payment but reason keeps the two distinguishable in the history; only
//     the operator/frontOfHouse scope=any grant may waive — a self-scoped
//     member credit is rejected.
//
//   - The `wellnessAccountArrears` aspect type (DDL `wellnessAccountArrears`) —
//     vtx.wellnessaccount.<NanoID>.arrears = {evaluatedAt, dueAt?, remindedFor?,
//     sentAt?, stale?, historyTooLong?}, the account's arrears-episode state.
//     Minted and rewritten by EvaluateWellnessArrears; every posted entry
//     marks it stale where it exists (with no stored balance an entry cannot
//     tell an episode opening from one continuing) and mints nothing where it
//     does not.
//
//   - The `wellnessLedgerHistory` lens (one row per transaction) the
//     billing-history FE reads (P5).
//
//   - The `wellnessMemberAccounts` lens (one row per member identity,
//     accountKey null until one is opened) — the FE's only way to resolve a
//     member's account key, since it can no longer be derived from
//     identityKey — projecting the account's arrears due date, reminded-for
//     date and reminder timestamp beside it for the statement and the desk.
//
//   - The `wellnessArrearsReminders` weaver-target lens + its §10.8 playbook
//     (targets.go) — clinic-ledger's resumable arrears mechanism applied to
//     this ledger: one row per account, freshUntil = the recorded dueAt arms
//     Weaver's @at, the fired timer's recorded lapse (and a stale mark, and a
//     never-evaluated account) opens missing_evaluation, and the playbook
//     dispatches directOp(EvaluateWellnessArrears), which replays the
//     account's postedTo history ONE PAGE PER DISPATCH — a history longer
//     than one page records a checkpoint on .arrears.replay and the lens's
//     missing_replay_a / missing_replay_b gaps chain the next page — ages
//     the account with the same FIFO the member's statement runs, stamps
//     .arrears, and
//     fires ONE external.notification per arrears episode to the bridge's
//     "notification" adapter, keyed on (accountKey, dueAt).
//     `RecordWellnessArrearsReminderNotification` records the outcome as an
//     idempotent overwrite (notifications.go).
//
//   - The `wellnessNoShowSettlement` actorAggregate lens + its Weaver playbook
//     (targets.go): a noShow booking carrying a noShowFeeCents (set by
//     wellness-domain's SetBookingAttendance) converges via a directOp
//     WellnessDebitAccount{accountKey, amountCents, bookingRef} once the booker's
//     account exists — WellnessDebitAccount's optional bookingRef writes the settles
//     audit link (transaction→booking) the lens reads to detect the gap is
//     closed. If the booker has no wellnessaccount yet, a directOp
//     WellnessCreateAccount{identityKey} opens one first, lazily, so a booker
//     who has never been billed before is never permanently stranded.
//     Mirrors clinic-ledger/clinic-domain's identical no-show-fee shape
//     (clinicNoShowSettlement), self-contained in this one package (no
//     new cross-package dependency, same rationale as clinic-ledger's own
//     placement — clinic-noshow-fee-design.md §"Package boundary").
//
//   - The `wellnessClassPriceSettlement` actorAggregate lens + its Weaver
//     playbook (targets.go): the OTHER wellness billing gap — a session
//     carrying a priceCents (set by wellness-domain's CreateSession) —
//     residentPriceCents instead, when the booking's own .status.rate is
//     resident and the session declares one, else priceCents same as a
//     standard booking (a CASE WHEN over the booking's rate, mirroring
//     orchestration-base's unroutedTasksSpec) —
//     converges via a directOp WellnessDebitAccount{accountKey, amountCents,
//     priceBookingRef} once the booker's account exists, UNCONDITIONAL on
//     attendance (a class price is owed for the seat, not for showing up).
//     Same lazy-open relay as wellnessNoShowSettlement: a directOp
//     WellnessCreateAccount{identityKey} opens the booker's account first when
//     it doesn't exist yet. WellnessDebitAccount's optional priceBookingRef
//     writes the settlesClassPrice audit link (transaction→booking, a
//     relation distinct from the no-show settlement's settles) the lens
//     reads to detect the gap is closed — so the two settlement gaps never
//     collide in a count() or double-charge each other.
//
// Mirrors packages/clinic-ledger, with the account held for the booker's
// identity directly rather than a domain-specific patient/lease vertex —
// wellness bookings carry no member-registration vertex of their own
// (CreateBooking's booker param is a bare vtx.identity), so the ledger anchors
// there instead. Both packages mint the account under its own
// independently-generated NanoID and enforce one-account-per-holder via a
// guard aspect on the holder vertex (see
// implementation-artifacts/adjacency-shared-nanoid-collision-design.md).
//
// Every canonicalName is vertical-prefixed (wellnessaccount/wellnesstransaction/
// wellnessLedgerHistory, not loftspace-ledger's bare account/transaction/
// ledgerHistory): a canonicalName is global across every installed package
// (internal/pkgmgr/installer.go checkCanonicalNameCollision).
//
// Depends wellness-domain (WellnessDebitAccount's optional bookingRef validates
// against wellness-domain's booking vertex type; the wellnessNoShowSettlement
// lens walks a booking's bookedBy link) and orchestration-base (MarkExpired
// and the freshnessExpiry marker the arrears @at firing writes onto the
// account).
package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "wellness-ledger",
	Version: "0.3.0",
	Description: "Wellness member payment ledger: the wellnessaccount vertex type (WellnessCreateAccount, independently-minted " +
		"id, one per member identity via a .wellnessLedgerAccount guard aspect on the identity) + the wellnesstransaction " +
		"vertex type (WellnessDebitAccount/WellnessCreditAccount, append-only entries linked to the account via postedTo, WellnessDebitAccount " +
		"independently taking optional bookingRef (no-show settlement, writes settles) and priceBookingRef (class-price " +
		"settlement, writes settlesClassPrice) back-refs; WellnessCreditAccount independently taking optional refundRef, " +
		"writes settlesRefund, and optional reason to waive a charge instead of recording cash collected) + the " +
		"wellnessLedgerHistory read-model lens (one row per " +
		"transaction) + the wellnessMemberAccounts lens (member identity -> account key lookup) + the " +
		"wellnessNoShowSettlement Weaver playbook (lazily opens the member's account via WellnessCreateAccount, then " +
		"auto-charges the no-show fee) + the wellnessClassPriceSettlement Weaver playbook (same lazy account-open relay, " +
		"then auto-charges the class price, unconditional on attendance) + the wellnessRefundSettlement Weaver playbook " +
		"(reverses a class-price charge already posted before its booking was cancelled, anchored on wellness-domain's " +
		"wellnessrefund marker vertex rather than the already-tombstoned booking). " +
		"Also ships the arrears reminder: the account's .arrears episode aspect (minted by evaluation; every posted " +
		"entry marks it stale, since no balance is stored) + the wellnessArrearsReminders weaver-target convergence " +
		"lens, whose §10.8 playbook dispatches EvaluateWellnessArrears — that op replays the account's postedTo " +
		"history one page per dispatch (a history longer than one page leaves a resumable .arrears.replay checkpoint " +
		"the lens's missing_replay_a/missing_replay_b gaps chain across dispatches), ages the account with the same " +
		"FIFO the member's statement runs and fires ONE external.notification per arrears episode to the bridge's " +
		"\"notification\" adapter, keyed on (accountKey, dueAt). RecordWellnessArrearsReminderNotification records " +
		"the outcome; wellnessMemberAccounts projects the due date and reminder timestamp for the statement and the " +
		"desk. Depends wellness-domain + orchestration-base.",
	Depends:       []string{"wellness-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
