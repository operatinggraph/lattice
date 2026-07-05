# LoftSpace D1.5 — the per-landlord RLS read as the rich decision surface

**Status:** ✅ built — fully shipped (2026-07-05; see §5 "Deferred-to-Vault items, now shipped"). Board
row CLOSED (was misread as `blocked-on Vault Fire 5` after Vault landed; verticals.md Done log, `7eb3330`).
Recommendation C, §4. The §4 fork is an internal
engineering tradeoff (duplicate a load-bearing aggregation vs. stay display-only) — not a trust-boundary /
topology / security-posture fork on the named strategic-fork list (Gateway, read-path auth D1, Vault,
multi-cell, HA-NATS), so it is Winston's to ratify per the steward's decide-don't-defer rule. C is
well-grounded (mirrors the applicant protected lens's deliberate display-only choice, avoids duplicating
`leaseApplicationCompleteSpec`, `$now`-in-protected-path feasibility already verified in §2) and ships real
value now with zero duplication risk; the readiness clone (option A) stays deferred to Vault, sharing that
gate with name/contact display (§3). **Lane:** Verticals. **Continues:** `read-path-authorization-d1-design.md`
§7.5 (which scopes D1.5 generically as "roll the remaining protected read models … pure extension"; the
*rich-decision* half it defers with "D1.5 may roll the gap state onto a protected model later",
`lease-signing/lenses.go` ~L549).

## For Andrew (one-look ratification block)

- **What it is.** Today the landlord has **two** surfaces: (a) the **trusted operator console**
  (`GET /api/unit-applications` → the Weaver convergence read model `weaver-targets`, sees **every**
  unit) which renders the rich decision card — qualification signals + applicant readiness + Approve /
  Decline; and (b) the **enforced per-landlord RLS read** (`GET /api/landlord/applications` → the
  protected Postgres model `read_landlord_lease_applications`, scoped by RLS to the units the signed-in
  landlord manages) which renders **only a scope-count banner**. D1.5's goal: make (b) the rich decision
  surface and retire (a). This is the landlord-side analog of the applicant D1.3-Fire-3 cutover.
- **The catch the PO row under-scoped.** The board filed this as "add 6 signal cols + wire
  `renderUnitCard`". It is **not** that small. A true cutover that retires the console needs the
  protected landlord lens to carry **applicant readiness** (`qualified` ≡ ssn ∧ fresh-bgcheck ∧ payment ∧
  signature) — because the FE gates the **Approve** button on `a.qualified` (`app.js`
  `renderApplicantRow`). Readiness is an **aggregation over service instances with a `$now` freshness
  predicate** — the exact machinery the convergence lens (`leaseApplicationCompleteSpec`) already runs.
- **The decision (one).** Carrying readiness in the landlord protected lens means **cloning the
  convergence lens's aggregation into a second lens** — a lens↔lens duplication of a *load-bearing core
  projection*. The applicant protected lens (`leaseApplicationsReadSpec`) **deliberately did not** clone
  it (display scalars only). §4 lays out the options; the recommendation is **(C) split the cutover**:
  ship the **non-readiness** rich surface now (profile signals + disposition — all non-sensitive, zero
  duplication), and **defer** the readiness clone + console retirement until **Vault** lands (which is
  also the gate for the name/contact display below — so the two deferrals share one gate).
- **The Vault boundary (no new gap — already filed).** The applicant **name** (`id.name`), **email**,
  **phone** are `sensitive=true` aspects (`identity-domain/ddls.go`); the patient `.demographics` are PHI
  (`clinic-domain`). **No lens may project a raw sensitive aspect** — the only sensitive read anywhere is
  `id.ssn.data.value` used as a *presence test* (`= null` → boolean), never a projected value. So the
  landlord RLS card can show **everything except the applicant's name** (it degrades to the NanoID label
  `renderApplicantRow` already falls back to). Surfacing name/email/phone to an authorized reader is the
  **display half of the existing `Vault + crypto-shredding` item** (lattice.md, ✅ ratified, 🚧 seq behind
  D1 — "transient-session-key decrypt"). The two contact-projection PO rows and this name display are all
  **blocked-on that one item** — not buildable as filed.

## 1. Problem & intent

The landlord decides leases. The decision surface — competing applicants ranked, each applicant's
qualification signals + readiness + a signed indicator, an Approve / Decline action gated so a landlord
cannot approve an unqualified applicant — currently lives **only** on the trusted operator console, which
sees **all** units (no per-landlord scope). The enforced per-landlord boundary exists (D1.3 Inc 3) but is
demonstrated only by a banner. D1.5 closes the gap: the **enforced** view becomes the **rich** view, so a
landlord's decisioning happens through their RLS scope, not a trusted all-units console.

## 2. Grounding (the pieces that already exist — do not redesign them)

- **The protected landlord read model.** `landlordLeaseApplicationsReadSpec`
  (`lease-signing/lenses.go`) — full-engine, Postgres adapter, `Protected`. Anchors every leaseapp,
  requires `applicationFor`→identity, `appliesToUnit`→unit, and the inbound `(u)<-[:manages]-(landlord)`
  walk; keys `(app_id, landlord_id)`; `authz_anchors = [landlord NanoID]`. Carries display scalars +
  `landlord_decision` + terms. **No** qualification / readiness columns today.
- **The convergence lens** `leaseApplicationCompleteSpec` (same file, ~L440–533) is the **source of
  truth** for qualification + readiness, projecting to `weaver-targets` keyed by leaseapp. It derives the
  7 profile signals (plain `app.profile.data.*` scalar hops — *not* sensitive) and readiness via a
  `providedTo` service-instance fan with a `$now` freshness CASE (`validUntil > $now`).
- **`$now` IS available in the protected projection path (verified).** The protected landlord lens is
  full-engine; the node-triggered full-engine path runs `executeFullForActor`
  (`refractor/pipeline/evaluate.go:113`), which sets `params["now"] = now.Format(time.RFC3339)`
  (`:191`). So a freshness-aware aggregation **works** in the protected Postgres lens — readiness is
  *technically* projectable there. This was the open feasibility question; it is answered yes.
- **The FE.** `loadLandlord` reads the console for the card grid + calls `loadLandlordRLS` (the banner).
  `renderUnitCard`→`renderApplicantRow`→`renderQualification` consume `status`, `qualified`, `signed`,
  the 7 signals, `leaseAppKey`, `landlordDeclined`, `declineReason`, `applicantName`. **Approve/Decline
  render iff `a.qualified && !unitLeased`.** `applicantName` already falls back to `shortKey(applicant)`.

## 3. What is and isn't sensitive (the projection boundary)

| Field | Source | Sensitive? | In landlord RLS lens? |
|---|---|---|---|
| `incomeToRentMet`, `employmentVerified`, `referenceCount`, `hasCoApplicant`, `hasGuarantor`, `guarantorIncomeToRentMet`, `profileSubmitted` | `app.profile.data.*` (derived booleans/ints) | **No** | ✅ buildable now |
| `landlord_decision`, `signed_at`, terms, unit scalars | aspects | **No** | ✅ already present |
| `qualified` (readiness) | aggregation over service instances + `$now` | **No** (a boolean) | ⚠️ needs the §4 clone |
| applicant **name** | `id.name` | **Yes** (`sensitive=true`) | 🚧 blocked-on Vault |
| applicant **email / phone** | `id.email` / `id.phone` | **Yes** (`sensitive=true`) | 🚧 blocked-on Vault |

The raw financials behind the signals (annual income, employer, guarantor income) are already deliberately
unprojected and stay so — the deferred Vault plane owns raw-financial display.

## 4. The one decision — how readiness reaches the landlord protected lens

`qualified` is the gate on the **Approve** action, so a console-retiring cutover must carry it. Options:

- **(A) Clone the aggregation** into `landlordLeaseApplicationsReadSpec` (the `providedTo` fan + `$now`
  freshness + the readiness derivation), re-anchored on landlord. *Pro:* one fire, fully RLS-enforced
  decisioning. *Con:* duplicates a **load-bearing core projection** in two lenses — every future change to
  readiness logic must land in both, the precise op/lens-divergence hazard the codebase has avoided so far
  (the applicant protected lens chose display-only on purpose). A shared cypher-fragment generator
  mitigates but does not remove it.
- **(B) Re-target / fan the convergence lens** to also emit a landlord-anchored protected Postgres row.
  *Rejected:* a lens projects to **one** target; the convergence read model is NATS-KV (`weaver-targets`,
  carrying the dispatch/gap machinery Weaver consumes), the landlord view is Postgres-RLS. There is no
  clean "one cypher, two targets", and entangling the dispatch read-model with the RLS read-model is worse
  than the duplication in (A).
- **(C) Split the cutover (RECOMMENDED).** Ship the **non-readiness** rich RLS surface now — the 7 profile
  signals + disposition + signed, all non-sensitive, **zero duplication** — as an *informational,
  enforced-scope* view. Keep **Approve/Decline on the trusted console** for the trusted-tool posture (a
  single-identity dev tool; the server-side guards already prevent an unqualified approve from leasing:
  `DecideLeaseApplication` requires `signed`, the convergence lens gates the actual lease on full
  readiness). **Defer** the readiness clone **and** console retirement to the **Vault** milestone — which
  is *already* the gate for the name/email/phone display (§3). One gate, two deferrals, no premature
  duplication of a core projection, and the simplest extension of the existing pattern.

**Recommendation: (C).** It respects the deliberate display-only choice of the applicant protected lens,
avoids duplicating the convergence aggregation before there's a forcing reason, keeps the Vault-gated bits
together, and still delivers a real, RLS-enforced, signal-rich landlord view now. Revisit (A) when Vault
lands and the name display + full RLS-enforced decisioning ship together as the genuine console retirement.

## 5. Build increments (under recommendation C)

1. **Lens.** Add the 7 profile-signal columns (`app.profile.data.*` + `(profileSubmittedAt <> null) AS
   profileSubmitted`) to `landlordLeaseApplicationsReadSpec` and to its `BodyColumns`. Pure scalar hops —
   no new MATCH, no aggregation, no `$now`. (`make verify-package-loftspace`; extend
   `landlord_protected_lens_test.go` / `lens_cypher_test.go`.)
2. **Handler.** Extend `protectedLandlordRow` + `selectLandlordApplicationsSQL` + the `Scan` in
   `cmd/loftspace-app/landlord_applications.go`; carry the signals into `landlordUnitGroup.Applications`.
   Derive the coarse `status` from `landlord_decision` + `signed_at` + `unit_status` (already documented as
   "coarse" there); **no** `qualified` / Approve gating on this surface (per C).
3. **FE.** Upgrade `#landlord-rls` from a banner into a rich RLS-scoped read view that reuses
   `renderQualification` against the protected rows (an *informational* card list under the console — keep
   the console grid; this is additive and **non-regressing**). The Approve/Decline path stays on the
   console. In-browser verify on `:7788`.
4. **Board.** Done-log the shipped increment; the readiness-clone + name display + console retirement stay
   on the row as **🚧 blocked-on Vault**.

**Deferred-to-Vault items, now shipped:** applicant name/email/phone display (Vault Fire 5b-ii,
`a710c7a`); option (A) readiness clone (Vault Fire 5b-ii-b, `13ffb75`); full RLS-enforced
decisioning + console retirement (Vault Fire 5b-ii-c, `7eb3330`) — see the checkpoints in
[vault-crypto-shredding-design.md](vault-crypto-shredding-design.md). This design is fully built.

## 6. Risks

- **Two near-duplicate landlord views (console + RLS list) until Vault.** Acceptable for a single-identity
  dev tool and explicitly framed as the enforcement-boundary view; the console retires at the Vault
  milestone. Mitigation: label the RLS view as the enforced-scope view, not a second console.
- **Divergence if (A) is taken later.** Whoever builds (A) must extract a shared cypher fragment for the
  readiness derivation (one generator feeding both lenses), not hand-copy it. Noted here so the future
  fire does not re-introduce the hazard.
