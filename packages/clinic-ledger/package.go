// Package clinicledger is the Clinic patient payment ledger: a per-patient
// financial account that records charges (copays, invoice lines) and payments
// as an append-only transaction history, plus a CAS-maintained .balance
// aspect that caches the running total in O(1) for authorization checks.
//
// It declares:
//
//   - The `clinicaccount` vertex type (DDL `clinicaccount`) — ClinicCreateAccount
//     mints vtx.clinicaccount.<NanoID> (root data {} per D5) with its OWN
//     independently-minted NanoID (never reused from the patient — Core KV
//     NanoIDs are unique platform-wide identifiers, not scoped per vertex
//     type), linked to the patient via heldFor, plus a .balance aspect
//     ({balanceCents: 0}) that ClinicDebitAccount/ClinicCreditAccount keep in
//     lockstep with every posted entry. "At most one account per
//     patient" is enforced by the `clinicLedgerAccountGuard` aspect on the
//     patient instead of a shared/derived key.
//
//   - The `clinicLedgerAccountGuard` aspect type (DDL
//     `clinicLedgerAccountGuard`) — vtx.patient.<NanoID>.ledgerAccount =
//     {accountKey}, written once by ClinicCreateAccount alongside the account it
//     names; its deterministic, patient-anchored key is the uniqueness guard.
//
//   - The `clinictransaction` vertex type (DDL `clinictransaction`) —
//     ClinicDebitAccount (a charge: a copay, an invoice line) and
//     ClinicCreditAccount (a payment received, or reason:"waiver" a charge
//     forgiven) each mint vtx.clinictransaction.<NanoID> (root data {} per
//     D5) with a .entry aspect {type, amountCents, memo?, postedAt,
//     reason? (credit only)}, linked to the account via postedTo, and
//     update the account's .balance aspect by the signed amount — a bare
//     update, auto-conditioned on the step-4 hydrated revision rather than
//     an explicit expectedRevision of its own, which is what makes it
//     retry-eligible: a lost race re-hydrates and retries the whole op
//     rather than hard-conflicting. That conditioning depends on the key
//     being declared, and the transaction DDL's own `derive_reads` declares
//     it on every dispatch rather than trusting the submitter to (every
//     dispatcher declares it in optionalReads as well).
//     The append-only log stays the audit trail; the clinicLedgerHistory
//     lens still derives its own balance independently by summing entries.
//     A waiver reduces the balance identically to a payment but reason
//     keeps the two distinguishable in the history; only the
//     operator/frontOfHouse scope=any grant may waive — a self-scoped
//     patient credit is rejected. A self-scoped credit is also capped at
//     the account's outstanding .balance (nothing here witnesses that a
//     self-submitted payment happened); a staff credit or waiver is the
//     clinic's own decision and is not, so it may take the balance
//     negative. An account opened before the .balance aspect existed
//     carries none until a self-scoped payment replays its own history to
//     compute one — a charge, a staff payment and a waiver against such an
//     account post and leave it legacy.
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
//     ClinicDebitAccount{accountKey, amountCents, appointmentRef} once the
//     patient's account exists — ClinicDebitAccount's optional appointmentRef
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
	Version: "0.3.0",
	Description: "Clinic patient payment ledger: the clinicaccount vertex type (ClinicCreateAccount, independently-minted " +
		"id, one per patient via a .ledgerAccount guard aspect on the patient) + the clinictransaction vertex type " +
		"(ClinicDebitAccount/ClinicCreditAccount, append-only entries linked to the account via postedTo, ClinicDebitAccount " +
		"taking an optional appointmentRef back-ref, ClinicCreditAccount taking an optional reason to waive a charge instead " +
		"of recording cash collected and an optional reversesRef back-ref to the debit it reverses) + the " +
		"clinicLedgerHistory read-model lens (one row per transaction) + the " +
		"clinicPatientAccounts lens (patient -> account key lookup) + the clinicNoShowSettlement Weaver playbook " +
		"(lazily opens the account via ClinicCreateAccount, auto-charges the no-show fee, then auto-reverses that charge " +
		"via ClinicCreditAccount once a CorrectAppointmentStatus correction moves the appointment off noShow). All three " +
		"ops grant front-of-house staff alongside the operator, unconfined. ClinicCreditAccount ALSO grants a patient scope=self " +
		"(pay down their own balance, never waive it — reason:\"waiver\" is rejected server-side for a self-scoped " +
		"submit), ownership proven server-side and the amount capped at the account's maintained .balance. " +
		"Depends clinic-domain.",
	Depends:       []string{"clinic-domain"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
