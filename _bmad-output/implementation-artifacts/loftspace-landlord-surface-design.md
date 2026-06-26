# LoftSpace — landlord / property-manager surface

**Status:** 🏗️ **Data layer ✅ shipped (e6d4499); decision-op + FE ✅ Winston-ratified — build-ready.**
No frozen-contract change, no Andrew gate — all package-owned mechanisms (Capability-KV §06 op-logic +
package-owned lens BodyColumns), the same envelope as the just-shipped listing-status-on-approval and
duplicate-application-guard work. The decision-op revises the heavily-reviewed `leaseApplicationComplete`
convergence semantics, so it wants a **3-layer adversarial review** + the `test-lease-convergence` heavy
e2e updated on build — its own build fire (do **not** fold into a small green follow-up).

**Problem (PO-filed, live-observed 2026-06-26).** The vertical is **one-sided** — `loftspace-app` is
entirely applicant-facing. There is no landlord surface: (1) no way to post / manage a listing in-app
(units are minted via raw `CreateLocation`/`SetListing`/`SetUnitAddress`); (2) no per-unit applicant
view; (3) no human leasing **decision** — convergence == auto-approval, and the only "decline" path is
the `BRIDGE_FAKE_DECLINE` env toggle, not a review. A real leasing product has two sides; today the
landlord is invisible.

---

## Increment 1 — data layer (✅ DONE, e6d4499)

`GET /api/unit-applications` (loftspace-app) groups the existing `leaseApplicationComplete` convergence
rows by `unitKey`, joins applicant names from `applicantRoster`, and unions in zero-application units
from `availableListings`. **No new lens was needed** — the convergence lens already carries `unitKey` /
`applicant` / `applicantApproved` / `declined` per application; the "by-unit projection lens" the PO
filing guessed at was unnecessary. P5-clean (three lens read models, zero Core KV); pure `groupByUnit`
assembler unit-tested; live-verified against the running stack. This doc's remaining increments build on
it.

---

## Increment 2 — the landlord decision op (the crux)

### The crux: a human decision must *gate* the auto-lease, not race it

Today (listing-status-on-approval, shipped 2026-06-26) an application that fully auto-converges
(`applicantApproved`: ssn + fresh bgcheck + payment + signature) opens `missing_listingLeased`, and
Weaver dispatches `directOp(SetListingStatus status=leased)` — the unit leases **automatically**, with no
human in the loop. A landlord "decline" bolted on as an after-the-fact veto would **race** that flip:
convergence is fast, so the unit can lease before the landlord acts, and the veto arrives too late. A
correct leasing decision is therefore not a veto on an auto-lease — it must be the **gate** the lease
waits behind.

### Winston's resolution (ratified): split readiness from the leasing decision

Decouple the two questions the single `applicantApproved → lease` path conflates today:

1. **Applicant readiness (automatic, unchanged).** Auto-convergence still drives `applicantApproved` =
   "this applicant is qualified" (all four applicant gaps closed). No behavior change to the bgcheck /
   payment / PII / signature flow.
2. **The leasing decision (human, new).** The listing-flip now gates on an **explicit landlord
   approval**, not on `applicantApproved`. A qualified application sits in a new state —
   *qualified, awaiting landlord decision* — until the landlord approves (→ lease) or declines (→
   terminal declined). This is the human-in-the-loop the PO gap names, and it closes the race (nothing
   auto-leases).

This **revises** the just-shipped auto-flip (which fired on `applicantApproved`) into a landlord-gated
flip — the natural completion of that increment, not a contradiction. The disruption is the convergence
tests that assert an auto-lease now need a landlord-approval step (see Test plan).

### `DecideLeaseApplication` op (lease-signing)

- **Signature:** `DecideLeaseApplication{leaseAppKey, decision}`. `decision ∈ {approved, declined}`.
  `reads = [leaseAppKey]` (declared `contextHint.reads`).
- **Script (on the `leaseapp` vertexType DDL, beside `SignLease`/`WithdrawLeaseApplication`):**
  - validate `leaseAppKey` is a live `vtx.leaseapp` (vertex `isDeleted` false → else `UnknownLeaseApplication`);
  - validate `decision` enum (→ else `BadDecision`);
  - write a `.decision` aspect `{value: decision, decidedAt: <op-stamped UTC>}` on the leaseapp
    (unconditioned upsert, mirroring `SetAppointmentStatus` — a later decision overrides an earlier one,
    so a landlord can reverse a decline to an approve). No `kv.Read` of prior decision needed.
- **Permission:** `mk("DecideLeaseApplication")`, Scope `any`, `GrantsTo: ["operator"]` — the landlord
  acts as the trusted-tool operator (same model as every other loftspace-app write). Mirrors
  `SignLease`'s grant.
- **No frozen contract:** a new package op (Capability-KV §06 sanctions the op's own Starlark logic) +
  one package-owned aspect. Op name carries `operationType` for the my-tasks self-describe.

### `leaseApplicationComplete` convergence-lens changes

Add one OPTIONAL walk of the leaseapp's own `.decision` aspect (the anchor already binds `app`; no new
node walk — `.decision` is an aspect on `app`, read like `app.signature.data.signedAt`):

```
  app.decision.data.value AS landlordDecision        -- 'approved' | 'declined' | null
```

New / revised RETURN columns (all package-owned BodyColumns):

- `landlordDecision` (string, informational) — the raw decision the FE renders.
- `landlordApproved` ≡ `(landlordDecision = 'approved')`.
- `landlordDeclined` ≡ `(landlordDecision = 'declined')`.
- **`missing_listingLeased`** — **revise** its gate: replace the `applicantApproved` term with
  `landlordApproved` (keep the unit-exists / has-listing / not-already-leased terms). So the flip fires
  iff a qualified application is **landlord-approved** and its unit is leasable. A landlord-declined or
  undecided application never flips the listing.
- **`declined`** — fold in `landlordDeclined`: `declined ≡ (bgcheck-declined) OR (payment-declined) OR
  landlordDeclined`. A landlord decline is a terminal disposition the applicant FE renders the same red
  "declined" banner for. (Still NOT in `violating` — declined ⊂ violating already, and a declined app
  has open gaps.)
- **`applicantApproved`** — unchanged definition (the four applicant gaps), but its **meaning shifts** to
  "qualified, pending landlord decision." Consumers (the applicant FE "complete" banner) must move their
  "complete/leased" signal to `landlordApproved && unitStatus='leased'` and show "qualified — awaiting
  landlord review" while `applicantApproved && !landlordApproved && !landlordDeclined` (see Increment 3).
- **`violating`** — gains the revised `missing_listingLeased` term automatically (it already ORs it);
  Weaver keeps dispatching the flip only while violating, so a qualified-but-undecided app stays
  violating (correct — its work is not done until the landlord decides) but dispatches **no** flip
  (missing_listingLeased false until landlordApproved). The userTask / externalTask gaps for an
  undecided-but-qualified app are all closed, so no spurious dispatch.

**Race / multi-applicant:** unchanged from the shipped flip — the first landlord-approved application
leases the unit; every other application's `(unitStatus <> 'leased')` then goes false → no second flip.
The landlord approving two applicants for one unit is a landlord error the unit-lease idempotency
absorbs (second flip is a clean no-op); a by-unit "already leased" hint in the FE discourages it.

---

## Increment 3 — the landlord FE (UX→FE fire)

A new **Landlord** view in loftspace-app (a top-level mode toggle, or a sibling app — UX decides),
reading `GET /api/unit-applications`. Run the UX-then-FE routine (Sally → FE Engineer → in-browser
verify against a fresh `make up-loftspace`). Surfaces:

1. **My units** — one card per unit (address, rent, status), application count, from `/api/unit-applications`
   (zero-application units included). A **"Post a listing"** form mints a unit over the existing ops
   (`CreateLocation` → `SetListing` → `SetUnitAddress`) via `/api/op` — the landlord analog of the
   applicant "New applicant" modal.
2. **Applicants per unit** — expand a unit → applicant cards (name, disposition badge:
   qualified / in-review / declined / approved, signed flag). For a **qualified** applicant
   (`approved:true` from the data layer = `applicantApproved`, not yet landlord-decided), show
   **Approve** / **Decline** buttons → `DecideLeaseApplication`. Approve on one applicant leases the
   unit (the flip) and the others read "unit leased."
3. **Applicant-side copy update** — the applicant My Applications "complete" banner moves from
   `applicantApproved` to `landlordApproved` + leased; between them it reads "Qualified — awaiting
   landlord review." (Small companion FE edit, same fire as the lens change so the applicant view stays
   truthful.)

---

## Test plan

- **Processor-driven** (`lease_signing_test.go`): `TestDecideLeaseApplication` — approved writes
  `.decision`, declined writes `.decision`, reverse decline→approve, bad enum → `BadDecision`,
  tombstoned leaseapp → `UnknownLeaseApplication`.
- **Lens cypher** (`lens_cypher_test.go`, real engine): `landlordApproved` opens `missing_listingLeased`
  only when qualified + approved + leasable; `landlordDeclined` → `declined` true + no flip; undecided
  qualified → no flip, not declined; existing applicant-gap cases unchanged.
- **Heavy e2e** (`test-lease-convergence`): **update** every convergence test that asserts the
  listing flips — insert a `DecideLeaseApplication(approved)` step after applicant-readiness; add
  `TestLeaseConvergence_NoLeaseUntilLandlordApproves` (qualified app, unit stays `available` until the
  decision) and `…_LandlordDeclineIsTerminal`.
- **verify-package-loftspace-domain** unchanged (the op is lease-signing's); bump lease-signing
  0.3.0 → 0.4.0 (manifest + Package synced).

## Review plan

3-layer adversarial (`bmad-code-review`): Blind Hunter (diff-only), Edge Case Hunter (the
qualified/undecided/declined/approved state matrix + the multi-applicant lease race), Acceptance
Auditor (vs. this doc + Contract #10 §10.2 — confirm no frozen-contract touch, the convergence row stays
one-per-anchor, `violating` semantics preserved). Convergence-plane change → full rigor, not lead-only.

## Why no Andrew gate

New package op (Capability-KV §06 op-logic) + package-owned lens columns + a revised package-owned
predicate. No frozen contract, no new trust surface (operator-role grant, same as every loftspace-app
write), no platform scan seam. Implementation-level throughout → Winston-ratified, build-ready.
