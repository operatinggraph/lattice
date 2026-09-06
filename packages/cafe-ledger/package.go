// Package cafeledger is the Café house-tab payment ledger: a per-lease
// financial account that records café charges (settled tabs), payments and
// refunds as a transaction history no posted entry's money fields are ever
// rewritten in.
//
// It declares:
//
//   - The `cafeaccount` vertex type (DDL `cafeaccount`) — CreateAccount
//     mints vtx.cafeaccount.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the lease — Core KV
//     NanoIDs are unique platform-wide identifiers, not scoped per vertex
//     type), linked to the leaseapp via heldFor. "At most one café account
//     per lease" is enforced by the `cafeLedgerAccountGuard` aspect on the
//     leaseapp instead of a shared/derived key.
//
//   - The `cafeLedgerAccountGuard` aspect type (DDL
//     `cafeLedgerAccountGuard`) — vtx.leaseapp.<NanoID>.cafeLedgerAccount =
//     {accountKey}, written once by CreateAccount alongside the account it
//     names. The local name is vertical-prefixed (cafeLedgerAccount, not
//     ledgerAccount): this same leaseapp already carries loftspace-ledger's
//     own .ledgerAccount guard aspect, so a bare local name would collide
//     key-for-key with it — the two ledgers anchor the same vertex type,
//     unlike loftspace-ledger/clinic-ledger which anchor different ones.
//
//   - The `cafetransaction` vertex type (DDL `cafetransaction`) —
//     DebitAccount (a charge: a settled café tab), CreditCafeAccount (a
//     payment received) and RefundCafeCharge (a charge given back) each mint
//     vtx.cafetransaction.<NanoID> (root data {} per D5) with a .entry aspect
//     {type, amountCents, memo?, postedAt}, linked to the account via
//     postedTo. The DISPLAYED balance is derived by summing entries (the
//     cafeLedgerHistory lens) and stays the display source of truth. That is
//     also why a refund is an ordinary credit entry plus a `reverses` link to
//     the charge it gives back, rather than a third entry type: the link
//     carries the correction's identity, so every balance consumer keeps
//     summing two kinds of entry and none of them has to learn a third. Two
//     tallies are maintained: `refundedCents` on a charge's own .entry aspect
//     — the refund ceiling, upserted under a compare-and-set on the revision
//     that aspect was hydrated at, so two refunds racing the same charge
//     serialize instead of jointly overrunning it — and the account's own
//     .balance aspect below.
//
//   - The `cafeAccountBalance` aspect type (DDL `cafeAccountBalance`) —
//     vtx.cafeaccount.<NanoID>.balance = {balanceCents}, minted at zero by
//     CreateAccount and moved by the signed amount by every posted entry, via
//     a bare update the Processor auto-conditions on the revision it hydrated
//     at, so concurrent entries serialize and retry rather than dropping one.
//     That conditioning depends on the key being declared, and the transaction
//     DDL's own `derive_reads` declares it on every dispatch rather than
//     trusting the submitter to (every dispatcher declares it in optionalReads
//     as well). It exists so CreditCafeAccount can cap a payment at what the
//     account actually owes without replaying a long house tab — the cap binds
//     every leg, resident scope=self and staff scope=any alike, since no
//     payment rail witnesses either. RefundCafeCharge maintains it but is not
//     bounded by it: its ceiling is the reversed charge's un-refunded
//     remainder, so giving back an already-paid charge takes the balance
//     negative. An account minted under 0.4.0's predecessors carries none until
//     a payment computes it from the account's own history; a charge or refund
//     against such an account posts and leaves it alone.
//
//   - The `cafeAccountArrears` aspect type (DDL `cafeAccountArrears`) —
//     vtx.cafeaccount.<NanoID>.arrears = {evaluatedAt, dueAt?, remindedFor?,
//     sentAt?, stale?}, the account's arrears-episode state. A charge against an
//     account that owed nothing opens an episode by recording the due date its
//     own postedAt implies; a payment that clears the balance ends it; a partial
//     payment marks it stale, because which charge is now oldest-and-open is a
//     function of the whole history rather than of the entry being posted.
//
//   - The `cafeArrearsReminders` weaver-target lens + its §10.8 playbook — this
//     package's first ORCHESTRATION. It arms Weaver's @at timer at the recorded
//     due date, opens one gap when that timer fires (or when the state is stale,
//     or was never evaluated at all) and dispatches
//     directOp(EvaluateCafeArrears), which ages the account with the same FIFO
//     the resident's own statement runs and, once per episode, fires the
//     external.notification the bridge turns into a real message.
//     `.arrears.remindedFor` names the due date already reminded for, so a
//     re-dispatched or redelivered evaluation recomputes the same head and sends
//     nothing. `RecordCafeArrearsReminderNotification` records the outcome as an
//     audit-only aspect (notifications.go) and does not gate the lens.
//
//   - The `cafeLedgerHistory` lens (one row per transaction, carrying the
//     reverses and settles hops) the house-tab history FE reads (P5).
//
//   - The `cafeLeaseAccounts` lens (one row per lease, accountKey null until
//     one is opened) — the FE's only way to resolve a lease's café account
//     key, since it can no longer be derived from leaseAppKey. It also carries
//     the account's arrears due date and reminder timestamp, which is what lets
//     the front-desk grid and the resident's statement say when a reminder went
//     out without a second read model.
//
// Mirrors packages/loftspace-ledger and packages/clinic-ledger, with the
// account held for the SAME leaseapp loftspace-ledger already anchors to
// (Increment 1 of the Café vertical, verticals.md — see
// implementation-artifacts/cafe-ledger-design.md for the guard-aspect
// local-name collision this introduces and how it's avoided). Both mint the
// account under its own independently-generated NanoID and enforce
// one-account-per-holder via a guard aspect on the holder vertex (see
// implementation-artifacts/adjacency-shared-nanoid-collision-design.md).
//
// Every canonicalName is vertical-prefixed (cafeaccount/cafetransaction/
// cafeLedgerHistory, not loftspace-ledger's bare account/transaction/
// ledgerHistory): a canonicalName is global across every installed package
// (internal/pkgmgr/installer.go checkCanonicalNameCollision).
//
// Depends lease-signing (the leaseapp vertex type an account is heldFor) and
// orchestration-base (MarkExpired and the freshnessExpiry marker the arrears
// @at firing writes onto the account).
package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "cafe-ledger",
	Version: "0.5.0",
	Description: "Café house-tab payment ledger: the cafeaccount vertex type (CreateAccount, independently-minted " +
		"id, one per lease via a .cafeLedgerAccount guard aspect on the leaseapp) + the cafetransaction vertex type " +
		"(DebitAccount/CreditCafeAccount/RefundCafeCharge, entries linked to the account via postedTo, each " +
		"keeping the account's .balance running-total aspect in lockstep) " +
		"+ the cafeLedgerHistory read-model lens (one row per transaction, carrying the reverses and settles hops) " +
		"+ the cafeLeaseAccounts lens (lease -> account key lookup). CreditCafeAccount ALSO grants a resident " +
		"scope=self (pay down their own house tab), ownership proven server-side and the amount capped at the " +
		"account's outstanding balance on every leg. RefundCafeCharge gives " +
		"back a posted charge as a credit anchored on that charge by a reverses link, bounded by a CAS-pinned " +
		"refundedCents tally on that charge's own entry rather than by the balance, staff-only at every scope. " +
		"Also ships the arrears reminder: the account's .arrears episode aspect (a charge against an account that " +
		"owed nothing records the due date its own postedAt implies; a payment that clears the balance ends the " +
		"episode; a partial one marks it stale) + the cafeArrearsReminders weaver-target convergence lens, whose " +
		"§10.8 playbook dispatches EvaluateCafeArrears — that op ages the account with the same FIFO the resident's " +
		"statement runs and fires ONE external.notification per arrears episode to the bridge's \"notification\" " +
		"adapter, keyed on (accountKey, dueAt). RecordCafeArrearsReminderNotification records the outcome. " +
		"Depends lease-signing + orchestration-base.",
	Depends:       []string{"lease-signing", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
