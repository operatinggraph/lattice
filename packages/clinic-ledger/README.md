# clinic-ledger

The Clinic patient payment ledger (v0.1.0) — a per-patient financial account that records charges
(copays, invoice lines) and payments as an **append-only** transaction history. The account also
carries a maintained `.balance` aspect (`{balanceCents}`) — an O(1) authorization cache kept in
lockstep with every posted entry via an auto-conditioned, retry-eligible update (no explicit
`expectedRevision` of its own); the `clinicLedgerHistory` lens remains the display source of
truth, independently summing the full entry history.

Depends: `clinic-domain` (the `patient` vertex type an account is `heldFor`). Install:
`lattice-pkg install packages/clinic-ledger` (after `clinic-domain`; or `make install-clinic` onto a
running stack).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `clinicaccount` (root `{}`, D5, `.balance` aspect) · `clinictransaction` (root `{}`, D5, `.entry` aspect incl. a debit-only payer dimension) |
| **Aspect types** (2) | `clinicLedgerAccountGuard` — `vtx.patient.<id>.ledgerAccount`, the per-patient create-only uniqueness guard · `clinicAccountBalance` — `vtx.clinicaccount.<id>.balance`, the maintained running-total cache |
| **Links** (2) | `heldFor` (account → patient) · `postedTo` (transaction → account) |
| **Operations** (3) | `ClinicCreateAccount` · `ClinicDebitAccount` · `ClinicCreditAccount` |
| **Projection lenses** (2) | `clinicLedgerHistory` (one row per transaction) → `clinic-ledger-history` · `clinicPatientAccounts` (patient → account key lookup) → `clinic-patient-accounts` (both `nats-kv`, `full` engine) |

All three operations are granted to `operator` and `frontOfHouse` at `scope: any` (`permissions.go`),
unconfined — a patient carries no building to workplace-confine to. The front desk opens a patient's
ledger account, records a charge, and records a payment all directly from the browser.

## Key shapes (Contract #1)

```
vtx.clinicaccount.<id>                 class=clinicaccount       root {} (D5)
vtx.clinicaccount.<id>.balance         class=clinicAccountBalance  {balanceCents}  (O(1) cache; updated on every post)
vtx.clinictransaction.<id>             class=clinictransaction   root {} (D5)
vtx.clinictransaction.<id>.entry       class=entry               {type ∈ debit|credit, amountCents, memo?, postedAt,
                                                                   billedTo? ∈ self|insurance (debit only, default self),
                                                                   expectedReimbursementCents? (debit + billedTo=insurance only)}
vtx.patient.<id>.ledgerAccount         class=clinicLedgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.clinicaccount.<id>.heldFor.patient.<id>            (account → patient; account is the later-arriving vertex)
lnk.clinictransaction.<id>.postedTo.clinicaccount.<id> (transaction → account; transaction is the later-arriving vertex)
```

Vertical-prefixed (`clinicaccount`/`clinictransaction`, not `loftspace-ledger`'s bare
`account`/`transaction`): a `canonicalName` is global across every installed package
(`internal/pkgmgr/installer.go` `checkCanonicalNameCollision`), so the two ledger packages could not
otherwise both install onto one kernel.

## Independent account NanoID + guard aspect

`ClinicCreateAccount` mints the account under its **own independently-generated NanoID** — never reused
from the patient, since Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex
type. "At most one account per patient" is enforced by the deterministic create-only
`clinicLedgerAccountGuard` aspect on the **patient** (`patientKey + ".ledgerAccount"`) instead of a
shared/derived key: a second `ClinicCreateAccount` for the same patient conflicts on that already-existing
aspect key. This mirrors `loftspace-ledger`'s account/lease shape (the account held for a patient
instead of a lease — a patient may have many appointments/encounters, and billing tracks a single
running balance across all of them); see
[`adjacency-shared-nanoid-collision-design.md`](../../_bmad-output/implementation-artifacts/adjacency-shared-nanoid-collision-design.md)
for why the account carries its own id rather than the patient's.

## Append-only ledger + the maintained balance cache

`ClinicDebitAccount`/`ClinicCreditAccount` each mint a fresh `vtx.clinictransaction.<id>` with a
`.entry` aspect and the `postedTo` link back to the account. The append-only log stays the audit
trail — the `clinicLedgerHistory` lens still derives its own balance independently by summing
`amountCents` (positive for debit, negative for credit) — but each op also updates the account's own
`.balance` aspect by the signed amount, via a BARE update (no explicit `expectedRevision`). Because
`.balance` is a declared read, the Processor auto-conditions that update on the step-4 hydrated
revision and marks it retry-eligible: a lost race re-hydrates and retries the whole op (the bounded
internal commit-conflict retry) rather than hard-conflicting, so concurrent debits/credits never
race a silent read-modify-write. This is what lets a self-scoped `ClinicCreditAccount` verify "amount
owed" in O(1) instead of replaying the account's full transaction history — a heavy self-pay account
was blowing the Starlark wall budget on that replay before this cache existed.

## Payer dimension (billing, not a claims pipeline)

A `ClinicDebitAccount` charge optionally carries `billedTo` (`self` | `insurance`, defaults to `self`
when omitted) and, only when `billedTo` is `insurance`, `expectedReimbursementCents` (must be positive
and `<= amountCents`) — enough for a clinic to track what it billed insurance for vs. what it actually
collected via a `ClinicCreditAccount` payment. Both fields reject on `ClinicCreditAccount` (a payment
has nothing to bill). This is **not** X12 837/835 claims/clearinghouse integration — that
certified-EHR-scale lift is explicitly out of bounds for a reference vertical; the dimension only
bounds what a debit entry *claims* about its payer.

## Where the ledger is surfaced

`clinicLedgerHistory` is the FE's billing-history read model (P5); `clinicPatientAccounts` is the
only way the FE resolves a patient's account key, since it is no longer derivable from `patientKey`
(the independent NanoID above) — a patient with no account yet still gets a row (`accountKey` null).

## Out of scope

- **The `.balance` aspect as a display/query surface** — it is `post_entry`'s own internal
  authorization cache, never read outside this package; the FE and any auditor use
  `clinicLedgerHistory`, which stays the independently-derived source of truth.
- **Refunds / voids as a distinct operation** — model as an offsetting
  `ClinicCreditAccount`/`ClinicDebitAccount` entry with an explanatory `memo` today; a dedicated
  reversal op is not yet needed.
