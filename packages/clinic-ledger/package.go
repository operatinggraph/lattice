// Package clinicledger is the Clinic patient payment ledger: a per-patient
// financial account that records charges (copays, invoice lines) and payments
// as an append-only transaction history, never a mutable running total.
//
// It declares:
//
//   - The `clinicaccount` vertex type (DDL `clinicaccount`) — CreateAccount
//     mints vtx.clinicaccount.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the patient — Core KV
//     NanoIDs are unique platform-wide identifiers, not scoped per vertex
//     type), linked to the patient via heldFor. "At most one account per
//     patient" is enforced by the `clinicLedgerAccountGuard` aspect on the
//     patient instead of a shared/derived key.
//
//   - The `clinicLedgerAccountGuard` aspect type (DDL
//     `clinicLedgerAccountGuard`) — vtx.patient.<NanoID>.ledgerAccount =
//     {accountKey}, written once by CreateAccount alongside the account it
//     names; its deterministic, patient-anchored key is the uniqueness guard.
//
//   - The `clinictransaction` vertex type (DDL `clinictransaction`) —
//     DebitAccount (a charge: a copay, an invoice line) and CreditAccount (a
//     payment received) each mint vtx.clinictransaction.<NanoID> (root data {}
//     per D5) with a .entry aspect {type, amountCents, memo?, postedAt},
//     linked to the account via postedTo. The ledger is append-only: a
//     balance is derived by summing entries (the clinicLedgerHistory lens),
//     never stored as a mutable aspect — so concurrent debits/credits never
//     race a read-modify-write.
//
//   - The `clinicLedgerHistory` lens (one row per transaction) the
//     billing-history FE reads (P5).
//
//   - The `clinicPatientAccounts` lens (one row per patient, accountKey null
//     until one is opened) — the FE's only way to resolve a patient's account
//     key, since it can no longer be derived from patientKey.
//
//   - The `clinicNoShowSettlement` actorAggregate lens + its Weaver playbook
//     (targets.go): a noShow appointment carrying a noShowFeeCents (set by
//     clinic-domain's SetAppointmentStatus) converges via a directOp
//     DebitAccount{accountKey, amountCents, appointmentRef} once the
//     patient's account exists — DebitAccount's optional appointmentRef
//     writes the settles audit link (transaction→appointment) the lens
//     reads to detect the gap is closed. Mirrors cafe-domain/cafe-ledger's
//     missing_charge shape, but self-contained in this one package (no new
//     cross-package dependency — see
//     implementation-artifacts/clinic-noshow-fee-design.md).
//
// Mirrors packages/loftspace-ledger, with the account held for a patient
// instead of a lease — a patient may have many appointments/encounters, and
// billing tracks a single running balance across all of them. Both packages
// mint the account under its own independently-generated NanoID and enforce
// one-account-per-holder via a guard aspect on the holder vertex (see
// implementation-artifacts/adjacency-shared-nanoid-collision-design.md).
//
// Every canonicalName is vertical-prefixed (clinicaccount/clinictransaction/
// clinicLedgerHistory, not loftspace-ledger's bare account/transaction/
// ledgerHistory): a canonicalName is global across every installed package
// (internal/pkgmgr/installer.go checkCanonicalNameCollision), so the two
// ledger packages could not otherwise both install onto one kernel.
//
// Depends clinic-domain (the patient vertex type an account is heldFor).
package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "clinic-ledger",
	Version: "0.2.0",
	Description: "Clinic patient payment ledger: the clinicaccount vertex type (CreateAccount, independently-minted " +
		"id, one per patient via a .ledgerAccount guard aspect on the patient) + the clinictransaction vertex type " +
		"(DebitAccount/CreditAccount, append-only entries linked to the account via postedTo, DebitAccount taking an " +
		"optional appointmentRef back-ref) + the clinicLedgerHistory read-model lens (one row per transaction) + the " +
		"clinicPatientAccounts lens (patient -> account key lookup) + the clinicNoShowSettlement Weaver playbook " +
		"(no-show fee auto-charge). Depends clinic-domain.",
	Depends:       []string{"clinic-domain"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
}
