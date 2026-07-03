# clinic-ledger

The Clinic patient payment ledger (v0.1.0) — a per-patient financial account that records charges
(copays, invoice lines) and payments as an **append-only** transaction history; a balance is always
derived by summing entries, never stored as a mutable running total.

Depends: `clinic-domain` (the `patient` vertex type an account is `heldFor`). Install:
`lattice-pkg install packages/clinic-ledger` (after `clinic-domain`; or `make install-clinic` onto a
running stack).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (2) | `clinicaccount` (root `{}`, D5) · `clinictransaction` (root `{}`, D5, `.entry` aspect) |
| **Aspect types** (1) | `clinicLedgerAccountGuard` — `vtx.patient.<id>.ledgerAccount`, the per-patient create-only uniqueness guard |
| **Links** (2) | `heldFor` (account → patient) · `postedTo` (transaction → account) |
| **Operations** (3) | `CreateAccount` · `DebitAccount` · `CreditAccount` |
| **Projection lenses** (2) | `clinicLedgerHistory` (one row per transaction) → `clinic-ledger-history` · `clinicPatientAccounts` (patient → account key lookup) → `clinic-patient-accounts` (both `nats-kv`, `full` engine) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — the trusted
single-identity model, no new capability surface, identical to `loftspace-ledger`.

## Key shapes (Contract #1)

```
vtx.clinicaccount.<id>                 class=clinicaccount       root {} (D5 — balance is lens-derived)
vtx.clinictransaction.<id>             class=clinictransaction   root {} (D5)
vtx.clinictransaction.<id>.entry       class=entry               {type ∈ debit|credit, amountCents, memo?, postedAt}
vtx.patient.<id>.ledgerAccount         class=clinicLedgerAccountGuard  {accountKey}  (the uniqueness guard)

lnk.clinicaccount.<id>.heldFor.patient.<id>            (account → patient; account is the later-arriving vertex)
lnk.clinictransaction.<id>.postedTo.clinicaccount.<id> (transaction → account; transaction is the later-arriving vertex)
```

Vertical-prefixed (`clinicaccount`/`clinictransaction`, not `loftspace-ledger`'s bare
`account`/`transaction`): a `canonicalName` is global across every installed package
(`internal/pkgmgr/installer.go` `checkCanonicalNameCollision`), so the two ledger packages could not
otherwise both install onto one kernel.

## Independent account NanoID + guard aspect

`CreateAccount` mints the account under its **own independently-generated NanoID** — never reused
from the patient, since Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex
type. "At most one account per patient" is enforced by the deterministic create-only
`clinicLedgerAccountGuard` aspect on the **patient** (`patientKey + ".ledgerAccount"`) instead of a
shared/derived key: a second `CreateAccount` for the same patient conflicts on that already-existing
aspect key. This mirrors `loftspace-ledger`'s account/lease shape (the account held for a patient
instead of a lease — a patient may have many appointments/encounters, and billing tracks a single
running balance across all of them); see
[`adjacency-shared-nanoid-collision-design.md`](../../_bmad-output/implementation-artifacts/adjacency-shared-nanoid-collision-design.md)
for why the account carries its own id rather than the patient's.

## Append-only ledger

`DebitAccount`/`CreditAccount` each mint a fresh `vtx.clinictransaction.<id>` with a `.entry` aspect
and the `postedTo` link back to the account — no balance field is ever written or mutated; the
`clinicLedgerHistory` lens derives a balance by summing `amountCents` (positive for debit, negative
for credit) client-side, so concurrent debits/credits never race a read-modify-write.

## Where the ledger is surfaced

`clinicLedgerHistory` is the FE's billing-history read model (P5); `clinicPatientAccounts` is the
only way the FE resolves a patient's account key, since it is no longer derivable from `patientKey`
(the independent NanoID above) — a patient with no account yet still gets a row (`accountKey` null).

## Out of scope

- **A stored/cached balance** — deliberately never materialized; always summed from
  `clinicLedgerHistory`.
- **Refunds / voids as a distinct operation** — model as an offsetting `CreditAccount`/`DebitAccount`
  entry with an explanatory `memo` today; a dedicated reversal op is not yet needed.
