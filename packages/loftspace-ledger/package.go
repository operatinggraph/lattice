// Package loftspaceledger is the Loftspace tenant payment ledger: a per-lease
// financial account that records charges (rent, fees, deposits) and payments
// as an append-only transaction history, never a mutable running total.
//
// It declares:
//
//   - The `account` vertex type (DDL `account`) — CreateAccount mints
//     vtx.account.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the lease — Core KV
//     NanoIDs are unique platform-wide identifiers, not scoped per vertex
//     type), linked to the leaseapp via heldFor. "At most one account per
//     lease" is enforced by the `ledgerAccountGuard` aspect on the leaseapp
//     instead of a shared/derived key.
//
//   - The `ledgerAccountGuard` aspect type (DDL `ledgerAccountGuard`) —
//     vtx.leaseapp.<NanoID>.ledgerAccount = {accountKey}, written once by
//     CreateAccount alongside the account it names; its deterministic,
//     lease-anchored key is the uniqueness guard.
//
//   - The `transaction` vertex type (DDL `transaction`) — DebitAccount (a
//     charge: rent, a late fee, a deposit) and CreditAccount (a payment
//     received) each mint vtx.transaction.<NanoID> (root data {} per D5) with a
//     .entry aspect {type, amountCents, memo?, postedAt}, linked to the account
//     via postedTo. The ledger is append-only: a balance is derived by summing
//     entries (the ledgerHistory lens), never stored as a mutable aspect — so
//     concurrent debits/credits never race a read-modify-write. DebitAccount's
//     optional clauseRef additionally writes the authorizedBy link (transaction
//     → clause) and marks the clause completed — the bespoke-contracts
//     Executable Paper package's canonical directOp consumer.
//
//   - The `ledgerHistory` lens (§10.2-style read model, one row per
//     transaction) the payment-history FE reads (P5).
//
//   - The `leaseAccounts` lens (one row per lease, accountKey null until one
//     is opened) — the FE's only way to resolve a lease's account key, since
//     it can no longer be derived from leaseAppKey.
//
// This is the ledger the bespoke-contracts-executable-paper design builds to:
// vtx.account.<id> + Debit/CreditAccount + ledger entries linked back to
// their authorizing source (packages/bespoke-contracts, Fire V1).
//
// Mirrors packages/clinic-ledger, with the account held for a lease instead
// of a patient (see implementation-artifacts/adjacency-shared-nanoid-collision-design.md
// for why the account carries its own independent NanoID rather than the
// lease's).
//
// Depends lease-signing (the leaseapp vertex type an account is heldFor).
package loftspaceledger

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "loftspace-ledger",
	Version: "0.2.0",
	Description: "Loftspace tenant payment ledger: the account vertex type (CreateAccount, independently-minted " +
		"id, one per lease via a .ledgerAccount guard aspect on the leaseapp) + the transaction vertex type " +
		"(DebitAccount/CreditAccount, append-only entries linked to the account via postedTo; DebitAccount's " +
		"optional clauseRef writes the authorizedBy audit link + marks the clause completed) + the ledgerHistory " +
		"read-model lens (one row per transaction) + the leaseAccounts lens (lease -> account key lookup). " +
		"Depends lease-signing.",
	Depends:     []string{"lease-signing"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
