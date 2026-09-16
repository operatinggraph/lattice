// Package loftspaceledger is the Loftspace tenant payment ledger: a per-lease
// financial account that records charges (rent, fees, deposits) and payments
// as an append-only transaction history, never a mutable running total.
//
// It declares:
//
//   - The `account` vertex type (DDL `account`) — LoftspaceCreateAccount mints
//     vtx.account.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the lease — Core KV
//     NanoIDs are unique platform-wide identifiers, not scoped per vertex
//     type), linked to the leaseapp via heldFor. "At most one account per
//     lease" is enforced by the `ledgerAccountGuard` aspect on the leaseapp
//     instead of a shared/derived key.
//
//   - The `ledgerAccountGuard` aspect type (DDL `ledgerAccountGuard`) —
//     vtx.leaseapp.<NanoID>.ledgerAccount = {accountKey}, written once by
//     LoftspaceCreateAccount alongside the account it names; its deterministic,
//     lease-anchored key is the uniqueness guard.
//
//   - The `transaction` vertex type (DDL `transaction`) — DebitAccount (a
//     charge: rent, a late fee, a deposit), CreditAccount (a payment
//     received) and ReturnDeposit (a charged security deposit credited back
//     once the tenancy has ended) each mint vtx.transaction.<NanoID> (root data {} per D5) with a
//     .entry aspect {type, amountCents, memo?, postedAt, periodStart?,
//     periodEnd?, dueAt?} — the three optional stamps are a recurring charge's
//     own billing period and due date — linked to the account via postedTo.
//     The ledger is append-only: a balance is derived by summing
//     entries (the ledgerHistory lens), never stored as a mutable aspect — so
//     concurrent debits/credits never race a read-modify-write. DebitAccount's
//     optional clauseRef additionally writes the authorizedBy link (transaction
//     → clause) and updates the clause's .status — completed for a one-time
//     clause, or chargeValidUntil re-armed for a period="monthly" clause
//     (Fire V3) — the semantic-contracts Executable Paper package's canonical
//     directOp consumer. ReturnDeposit{leaseAppKey, clauseKey, accountKey}
//     is that package's leaseRentSettlement missing_depositReturn dispatch:
//     it reads the deposit clause's own .terms (purpose=deposit, the
//     amount) and .status (completed = charged), the lease's .tenancy
//     (endedAt), and the clause's deterministic chargesTo / governs links,
//     posts the credit authorizedBy the clause and marks its .status
//     returned under OCC; a clause already returned is a no-op.
//
//   - The `ledgerHistory` lens (§10.2-style read model, one row per
//     transaction) the payment-history FE reads (P5), projecting the
//     authorizing clause's prose and purpose beside each entry so a
//     statement holds the deposit apart from rent.
//
//   - The `leaseAccounts` lens (one row per lease, accountKey null until one
//     is opened) — the FE's only way to resolve a lease's account key, since
//     it can no longer be derived from leaseAppKey — projecting the account's
//     arrears due date, reminded-for date and reminder send instant beside it.
//
//   - The `loftspaceAccountArrears` aspect type (DDL `loftspaceAccountArrears`)
//     — vtx.account.<NanoID>.arrears = {evaluatedAt, dueAt?, remindAt?,
//     remindedFor?, sentAt?, stale?, historyTooLong?}, the account's
//     arrears-episode state. Minted and rewritten by EvaluateLoftspaceArrears;
//     every posted entry marks it stale (this ledger stores no balance, so an
//     entry cannot tell an episode opening from one continuing).
//
//   - The `loftspaceArrearsReminders` weaver-target lens + its §10.8 playbook
//     (targets.go) — the wellness-ledger arrears mechanism applied to this
//     ledger, which likewise stores no balance. missing_evaluation dispatches
//     directOp(EvaluateLoftspaceArrears), which ages the account with the same
//     plain FIFO the tenant's statement runs, stamps .arrears with the head's
//     OWN recorded due date (its postedAt when it recorded none) and the
//     reminder instant the five-day grace puts after it, and fires ONE
//     external.notification per arrears episode to the bridge's
//     "notification" adapter once that instant has passed.
//     `RecordLoftspaceArrearsReminderNotification` records the outcome as an
//     audit-only .arrearsNotification aspect (notifications.go).
//
// This is the ledger the semantic-contracts-executable-paper design builds to:
// vtx.account.<id> + Debit/CreditAccount + ledger entries linked back to
// their authorizing source (packages/semantic-contracts, Fire V1).
//
// Mirrors packages/clinic-ledger, with the account held for a lease instead
// of a patient (see implementation-artifacts/adjacency-shared-nanoid-collision-design.md
// for why the account carries its own independent NanoID rather than the
// lease's).
//
// Depends lease-signing (the leaseapp vertex type an account is heldFor) and
// orchestration-base (MarkExpired and the freshnessExpiry marker the arrears
// @at firing writes onto the account).
package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "loftspace-ledger",
	Version: "0.8.1",
	Description: "Loftspace tenant payment ledger: the account vertex type (LoftspaceCreateAccount, independently-minted " +
		"id, one per lease via a .ledgerAccount guard aspect on the leaseapp) + the transaction vertex type " +
		"(DebitAccount/CreditAccount, append-only entries linked to the account via postedTo; DebitAccount's " +
		"optional clauseRef writes the authorizedBy audit link + updates the clause status: completed one-time, " +
		"or chargeValidUntil re-armed if period=monthly, Fire V3; ReturnDeposit credits a charged purpose=deposit " +
		"clause back once the lease's tenancy has ended and marks it returned — Weaver's leaseRentSettlement " +
		"dispatch; LoftspaceRecordCharge is a person's manual " +
		"charge, never clause-authorized; it and CreditAccount also grant a consumer scope=self, " +
		"ownership-checked off the account's own heldFor topology — a resident paying down their own " +
		"balance, credit only and amount-capped at the account's own recomputed outstanding balance, or a " +
		"landlord recording a charge or a payment on a lease of a unit they manage, uncapped) + the " +
		"ledgerHistory read-model lens (one row per transaction) + the leaseAccounts lens (lease -> account " +
		"key lookup, plus the account's arrears due date and reminder timestamps). " +
		"Also ships the rent-arrears reminder: the account's .arrears episode aspect (minted by evaluation; every " +
		"posted entry marks it stale, since no balance is stored) + the loftspaceArrearsReminders weaver-target " +
		"convergence lens, whose §10.8 playbook dispatches EvaluateLoftspaceArrears — that op ages the account with " +
		"the same plain FIFO the tenant's statement runs, records the head's own recorded due date (its postedAt " +
		"when it recorded none) and the reminder instant five days after it, and fires ONE external.notification " +
		"per arrears episode to the bridge's \"notification\" adapter, keyed on (accountKey, dueAt, headKey). " +
		"RecordLoftspaceArrearsReminderNotification records the outcome. Depends lease-signing + orchestration-base.",
	Depends:       []string{"lease-signing", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
	// DebitAccount carries no op-meta: it is Weaver's clause-authorized
	// charge and the operator's CLI charge, with no shipped screen; the
	// person-facing "Record charge" descriptor is LoftspaceRecordCharge's.
	// An upgrade that drops an op-meta must declare its disposition; no
	// task is ever minted forOperation DebitAccount (CreateTask is
	// operator-only and no playbook targets it), so cancelling open
	// referents is a no-op declaration, not a work-destroying one.
	RetireCancelsOpenTasks: []string{"DebitAccount"},
}
