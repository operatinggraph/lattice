// Package cafeledger is the Café house-tab payment ledger: a per-lease
// financial account that records café charges (settled tabs) and payments as
// an append-only transaction history, never a mutable running total.
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
//     DebitAccount (a charge: a settled café tab) and CreditAccount (a
//     payment received) each mint vtx.cafetransaction.<NanoID> (root data {}
//     per D5) with a .entry aspect {type, amountCents, memo?, postedAt},
//     linked to the account via postedTo. The ledger is append-only: a
//     balance is derived by summing entries (the cafeLedgerHistory lens),
//     never stored as a mutable aspect — so concurrent debits/credits never
//     race a read-modify-write.
//
//   - The `cafeLedgerHistory` lens (one row per transaction) the house-tab
//     history FE reads (P5).
//
//   - The `cafeLeaseAccounts` lens (one row per lease, accountKey null until
//     one is opened) — the FE's only way to resolve a lease's café account
//     key, since it can no longer be derived from leaseAppKey.
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
// Depends lease-signing (the leaseapp vertex type an account is heldFor).
package cafeledger

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "cafe-ledger",
	Version: "0.1.0",
	Description: "Café house-tab payment ledger: the cafeaccount vertex type (CreateAccount, independently-minted " +
		"id, one per lease via a .cafeLedgerAccount guard aspect on the leaseapp) + the cafetransaction vertex type " +
		"(DebitAccount/CreditAccount, append-only entries linked to the account via postedTo) + the " +
		"cafeLedgerHistory read-model lens (one row per transaction) + the cafeLeaseAccounts lens (lease -> account " +
		"key lookup). Depends lease-signing.",
	Depends:     []string{"lease-signing"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
