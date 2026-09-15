# clinic-ledger

The Clinic patient payment ledger (v0.5.0) — a per-patient financial account that records charges
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
| **Links** (5) | `heldFor` (account → patient) · `postedTo` (transaction → account) · `settles` (transaction → appointment: the line IS the visit's fee) · `forVisit` (transaction → appointment: the line is FOR the visit) · `reverses` (credit → the charge it gives back) |
| **Operations** (3) | `ClinicCreateAccount` · `ClinicDebitAccount` · `ClinicCreditAccount` |
| **Projection lenses** (2) | `clinicLedgerHistory` (one row per transaction, carrying the visit it names and the charge it reverses) → `clinic-ledger-history` · `clinicPatientAccounts` (patient → account key lookup) → `clinic-patient-accounts` (both `nats-kv`, `full` engine) |
| **Weaver target** (1) | `clinicNoShowSettlement` — charges the fee an appointment's status carries once, opens the account first if needed, and reverses a charge whose appointment is later corrected to a fee-less status |

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
lnk.clinictransaction.<id>.settles.appointment.<id>    (debit → appointment; ClinicDebitAccount appointmentRef)
lnk.clinictransaction.<id>.forVisit.appointment.<id>   (debit → appointment; ClinicDebitAccount visitRef)
lnk.clinictransaction.<id>.reverses.clinictransaction.<id> (credit → the reversed debit; ClinicCreditAccount reversesRef)
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

Being a *declared* read is the whole basis of that conditioning, and `contextHint` is
submitter-supplied and never enforced — a client that simply omitted the key would get a live read
and an **unconditioned** update, and two concurrent entries would each write their own total over the
other. So the transaction DDL declares the key itself: `derive_reads(op)` (Contract #2 §2.5 class
(g)) returns `<accountKey>.balance` under `optionalReads` at the head of step 4 for every dispatch of
both ops. The dispatchers' own static declarations (`opmetas.go`, `targets.go`) document the read
set; `derive_reads` guarantees it.

### The amount cap, and who it binds

A **self-scoped** (patient) `ClinicCreditAccount` may never exceed the account's outstanding
`.balance`, and is refused outright when nothing is owed — nothing on this platform witnesses that a
self-submitted payment actually happened, so the amount is as much the attack surface as the account
named. Both refusals spell the amounts as dollars and carry no entity key: they are toasted verbatim
at the patient. A **staff** credit or waiver is not capped — it records a decision the clinic made,
and the reversal `clinicNoShowSettlement` dispatches gives back a charge that may already have been
paid, so both may legitimately take the balance negative.

The ownership proof runs before the balance is read, which is what keeps a scope=self holder from
naming a stranger's account and making the server walk that account's history.

### Accounts opened before `.balance` existed

`ClinicCreateAccount` mints the aspect, so the legacy set is closed. An account in it carries no
`.balance` at all — hence `optionalReads` rather than `reads`, since a required read would
HydrationMiss-reject every entry against it. Only a **self-pay** pays the one-time bounded replay
(10 pages of 50 `postedTo` entries) that computes such an account's balance, because a self-pay is
the only leg whose cap needs the number; exceeding that budget refuses the payment rather than
seeding a partial sum. A charge, a staff payment and a waiver against a legacy account post normally
and write no `.balance`, so the account stays legacy until a self-pay first touches it — and that
replay sums the whole history, those later entries included. That also keeps the unattended
`clinicNoShowSettlement` dispatches off the replay path entirely.

A *tombstoned* `.balance` is a different absence from a missing one: a create against a tombstone is
refused (Contract #3 §3.3), so only a genuinely absent key is minted and a tombstoned one is revived
by the update. A document of any other class under that key is refused (`InvalidState`) rather than
read as a number.

## Payer dimension (billing, not a claims pipeline)

A `ClinicDebitAccount` charge optionally carries `billedTo` (`self` | `insurance`, defaults to `self`
when omitted) and, only when `billedTo` is `insurance`, `expectedReimbursementCents` (must be positive
and `<= amountCents`) — enough for a clinic to track what it billed insurance for vs. what it actually
collected via a `ClinicCreditAccount` payment. Both fields reject on `ClinicCreditAccount` (a payment
has nothing to bill). This is **not** X12 837/835 claims/clearinghouse integration — that
certified-EHR-scale lift is explicitly out of bounds for a reference vertical; the dimension only
bounds what a debit entry *claims* about its payer.

## A charge names its visit — two relations, never both

A `ClinicDebitAccount` can name an appointment in one of two ways, and the op refuses both at once
(`InvalidArgument`):

- **`appointmentRef` → `settles`**: the line **is** the fee the appointment's current status carries
  (a no-show fee, a late-cancellation fee). This is the shape `clinicNoShowSettlement`'s `missing_charge`
  gap dispatches. The op reads the appointment's `.status` and refuses **`NoFeeToSettle`** unless it
  carries `noShowFeeCents > 0` — `missing_reversal` reads a fee-less status beside a live `settles` link as a
  correction that owes a reversal, so a `settles` link minted against a fee-less appointment would be credited
  straight back. The `.status` key is declared by the script's own `derive_reads` (an `optionalRead`, beside
  `.balance`) whenever the payload carries a well-formed appointment key, so the guard reads a hydrated,
  OCC-conditioned document whatever the submitter declared; `CreateAppointment` always writes `.status`, so
  absence means no fee, never "not yet loaded". A Weaver dispatch that races a status correction is refused
  rather than charged-then-reversed.
- **`visitRef` → `forVisit`**: the line is **for** the visit — a desk copay, a procedure charge. Validated
  alive (`UnknownAppointment`) and as this account's patient's own appointment (`WrongPatient` — the patient
  comes from the account's `heldFor` walk, which the descriptor declares, never the payload); rejected on a
  `ClinicCreditAccount`, as `appointmentRef` is. No convergence lens reads `forVisit`, so
  a copay on a no-show appointment neither counts as its fee nor opens a reversal; only `clinicLedgerHistory`
  projects it. The FE's charge form fills it from the visit the desk picks (the descriptor carries
  `visitRef` and declares `{payload.visitRef}`; an absent optional field's template is dropped, so a plain
  charge declares nothing extra).

A `ClinicCreditAccount` names the charge it gives back through **`reversesRef` → `reverses`** — the
`missing_reversal` dispatch does, and so does a desk waiver that picks the charge it forgives (the descriptor
carries `reversesRef`; a self-scoped patient payment is refused `AuthDenied` if it sends one, since the link
would disarm the reversal a later correction owes the patient). The charge must be posted to the same account
(`WrongAccount`) — the `postedTo` link key is deterministic from the two payload keys, so `derive_reads` declares
it as an `optionalRead` for every `ClinicCreditAccount` carrying a well-formed `reversesRef`. A hand waiver that names a no-show fee
therefore carries the `reverses` link `missing_reversal` counts, so a later status correction does not credit
the fee a second time.

`clinicLedgerHistory` projects, per line: `appointmentKey` / `visitStartsAt` (coalesced across `settles` and
`forVisit` — a line has at most one visit), `settlesFee` (`true` when the line is that visit's fee, `false`
otherwise) and `reversesKey` (the charge a credit reverses, or null) — the column a statement retires the named
debit on before ageing the rest FIFO.

## Where the ledger is surfaced

`clinicLedgerHistory` is the FE's billing-history read model (P5); `clinicPatientAccounts` is the
only way the FE resolves a patient's account key, since it is no longer derivable from `patientKey`
(the independent NanoID above) — a patient with no account yet still gets a row (`accountKey` null).

## Out of scope

- **The `.balance` aspect as a display/query surface** — it is `post_entry`'s own internal
  authorization cache, never read outside this package; the FE and any auditor use
  `clinicLedgerHistory`, which stays the independently-derived source of truth.
- **A standalone refund op** — a reversal is an ordinary `ClinicCreditAccount` carrying `reversesRef`
  (the `reverses` link names the charge it gives back), or a `reason: "waiver"` credit when the debt is
  forgiven rather than repaid; `clinicNoShowSettlement`'s `missing_reversal` gap posts the former itself.
