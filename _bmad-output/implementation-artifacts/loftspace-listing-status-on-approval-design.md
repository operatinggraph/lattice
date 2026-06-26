# LoftSpace — listing-status transition on approval (Increment 2, part a)

**Status:** ✅ Winston-ratified — **build-ready** (no frozen-contract change, no Andrew gate; all
package-owned mechanisms). Build is its own fire (cross-package convergence + a new op →
**3-layer adversarial review**).

**Problem (PO-filed, live-observed 2026-06-26).** After an application **fully converges**
(`violating:false`, all applicant gaps closed = approved), the unit's listing stays
`status:"available"` and keeps showing in Browse&Apply — nothing flips a leased unit out of
availability. The vertical models the applicant workflow but never closes the loop on the *thing being
leased*. (Part b — the concurrent/duplicate-application guard — already shipped; this is part a.)

## Decision summary

Drive the transition by **convergence → `directOp`**, the proven precedent
(`objectLiveness→TombstoneObject`, `appointmentReminders→RecordAppointmentReminder`). Two package-local
pieces, no platform/contract surface:

1. **New `SetListingStatus(unit, status)` op in `loftspace-domain`** — a *status-only* transition.
   `SetListing` is a full-aspect unconditioned upsert requiring all economics (rentAmount, bedrooms,
   …), which a convergence row does not carry — so a status-only op that **preserves the existing
   `.listing`** is the clean mechanism, not a `SetListing` re-supply.
2. **`lease-signing`'s `leaseApplicationComplete` target drives it** — the convergence lens gains a
   gap `missing_listingLeased` + a `directOp(SetListingStatus, status=leased)` playbook entry. The
   dispatching target (lease-signing) and the op-owning package (loftspace-domain) differ — a
   **cross-package directOp**, which is fine: auth is the `operator`-role grant (below), and
   lease-signing already installs after loftspace-domain.

### Why a new op, not `SetListing` partial-update
`SetListing` requires the full economics payload (`ddls.go` step-6 gates rentAmount/bedrooms/
availableFrom/leaseTermMonths/status all required). A convergence directOp carries only what the lens
row projects (today `unitRent`). Re-projecting *every* listing field onto the lease row to re-supply
them is fragile and couples the lease lens to loftspace's full listing shape. A dedicated
`SetListingStatus` reads the live `.listing` (`kv.Read`, §2.5), rewrites **only** `status`, and
preserves the rest — minimal, idempotent, and self-contained.

## `SetListingStatus` op (loftspace-domain)

- **Signature:** `SetListingStatus{unit, status}`. `unit` = `vtx.unit.<NanoID>`; `status ∈
  {available, pending, leased}`. `reads = [unit]` (declared `contextHint.reads`).
- **Script (on the `loftspaceListing` vertexType DDL, beside `SetListing`):**
  - validate `unit` is a live `vtx.unit` (vertex `isDeleted` false);
  - `kv.Read(unit + ".listing")` — **require** the `.listing` aspect to exist (a unit with no
    listing is not transitionable: reject `NoListing`);
  - validate `status` enum;
  - **idempotent:** if the existing `.listing.status` already equals `status`, emit the aspect
    unchanged (a clean no-op upsert — at-least-once directOp re-dispatch must not thrash);
  - else rewrite the `.listing` aspect with `status` replaced, **all other fields preserved verbatim**
    from the kv.Read snapshot.
- **Permission:** add `mk("SetListingStatus")` to `loftspace-domain/permissions.go` (Scope `any`,
  `GrantsTo: ["operator"]`) — exactly mirrors `SetListing`/`SetUnitAddress`, so Weaver's service actor
  (holds `operator`) may submit it. This is the cross-package directOp auth, identical to how
  clinic-reminders grants `RecordAppointmentReminder` to `operator`.
- **No transition guard beyond enum + liveness.** available→leased, pending→leased, and the
  idempotent leased→leased all valid. A landlord-driven revert (leased→available) is allowed too —
  the op is a generic status setter; convergence only ever *drives* it to `leased`.
- **DDL / manifest / verify-package-loftspace-domain** updated (new op = +N assertions); the op is
  **non-sensitive**, mints no vertex (writes one aspect on an existing unit).

## `leaseApplicationComplete` lens + playbook (lease-signing)

The lens already `OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)` and reads `u.listing.data.rentAmount`.
Add:

- **`unitStatus ← u.listing.data.status`** — new informational column.
- **`applicantApproved`** (informational bool) `= NOT (missing_onboarding OR missing_bgcheck OR
  missing_payment OR missing_signature)` — the four *applicant* gaps all closed. **The FE reads this
  for its "approved" banner**, decoupling the applicant-facing "approved" read from `violating` (which
  now also covers the listing transition — see below). Avoids regressing the My-Applications UX.
- **`missing_listingLeased`** (gap) `= (unitKey <> null) AND applicantApproved AND (unitStatus <>
  'leased')`. Opens exactly when the application is approved and its unit is not yet leased.
- **`violating`** — **add `missing_listingLeased` to the OR.** This is load-bearing: Weaver **skips all
  dispatch when `violating=false`** (`evaluator.go:81`), so a directOp gap only fires while the row is
  violating. Folding the transition into `violating` is also semantically correct — the application's
  work is not *fully* done until the unit it is for is marked leased. The applicant FE keys "approved"
  off the new `applicantApproved` column, so the brief sub-second `violating`-true window between
  applicant-approval and listing-flip does not read as "still in review."
- **Playbook (lease-signing `WeaverTargets`):**
  ```
  "missing_listingLeased": { Action: "directOp", Operation: "SetListingStatus",
      Params: {"unit": "row.unitKey", "status": "leased"}, Reads: []string{"row.unitKey"} }
  ```
- **BodyColumns** extended with `unitStatus`, `applicantApproved`, `missing_listingLeased`.

## Edge cases (all self-resolving — no infinite loops)

- **Multi-applicant race.** Cross-applicant applications to one unit are allowed by design (the
  landlord chooses). The **first** to converge dispatches `SetListingStatus(leased)`. Every *other*
  application's `missing_listingLeased = (unitStatus <> 'leased')` is then **false** (the unit is
  leased) → no dispatch, no double-transition. The losers stay approved-but-unit-leased; surfacing
  "unit no longer available" to them is a **future landlord-surface concern** (noted, not built here).
- **Dead unit.** `appliesToUnit` filters `isDeleted`, so a tombstoned unit → `unitKey = null` →
  `missing_listingLeased = false` → no dispatch. Safe.
- **Freshness re-open after lease.** If a bgcheck's freshness lapses *after* the unit is leased, an
  applicant gap re-opens → `applicantApproved` false → `missing_listingLeased` false (unit already
  leased anyway). Nothing to undo. Correct.
- **At-least-once / mark-lease reclaim.** A re-dispatched `SetListingStatus` is an idempotent no-op
  once `status=leased`. The directOp carries `expectedRevision` (OCC); a concurrent operator
  `SetListing` would RevisionConflict and Weaver retries to convergence.

## What this is NOT (scope discipline)

- **No landlord decision op.** Convergence == approval is the existing auto-approve flywheel; a human
  approve/decline lives with the **landlord/property-manager surface** (separate ★★★ item). This wires
  the *unit-availability* consequence of the already-automatic approval.
- **No frozen-contract touch.** New op DDL + permission + lens BodyColumns + a directOp playbook entry
  are all package-owned. §10.2 violating-OR, §10.8 directOp, and §2.5 `kv.Read` are existing
  mechanisms. **No Andrew gate.**

## Build plan (next fire)

1. loftspace-domain: `SetListingStatus` script + DDL + permission + manifest + verify-package update.
2. lease-signing: lens columns (`unitStatus`, `applicantApproved`, `missing_listingLeased`, violating
   fold) + the directOp playbook entry + BodyColumns.
3. Tests: Processor-driven `SetListingStatus` (transition · preserve-economics · NoListing reject ·
   dead-unit reject · already-leased idempotent · bad-status reject); lease lens cypher
   (`missing_listingLeased` true/false, `applicantApproved`, multi-applicant unit-leased→no-gap);
   extend `test-lease-convergence` e2e to assert an approved application flips its unit to `leased`
   and it drops from `availableListings`.
4. **3-layer adversarial review** (cross-package convergence + new op): Blind Hunter, Edge Case Hunter,
   Acceptance Auditor. Gates: build/vet/golangci/STRICT-P5/gofmt + verify-package-loftspace-domain +
   verify-package-lease-signing (if present) + the e2e.

## Files (build)
`packages/loftspace-domain/{ddls,permissions,package,manifest}.go`,
`scripts/verify-package-loftspace-domain.go`,
`packages/lease-signing/{lenses,targets}.go`,
`cmd/loftspace-app/web/` (optional follow-on: read `unitStatus`/`applicantApproved` for the banner).
