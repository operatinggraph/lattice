# Vertical-app descriptor audit — where the UI is hardcoded in JS instead of described in the package

**2026-08-20, Winston (Andrew-directed session).** The question: operations are supposed to be
self-describing — full `OpMetaSpec` descriptors a client like Facet renders forms from, with no raw
NanoIDs shown to humans — so where do the vertical apps hardcode UI in JS instead, and what
standard/skill/lint binds this? Four read-only audit agents swept `cmd/{loftspace,clinic,cafe,wellness}-app`
(Go + web assets), cross-checked against each domain's `opmetas.go`.

## 1. Prior art — this was half-solved deliberately, supply side only

- **Supply (packages): SOLVED.** The Vertical Package Standard
  (`_bmad-output/implementation-artifacts/vertical-package-standard.md`, ratified 2026-07-24) made
  S1 — *every user-facing op is self-describing* — a blocking gate (`scripts/lint-package-standard.go`);
  convergence closed 2026-07-26 with zero S1 debt across all 29 packages. The client-ceremony design
  (2026-08-02) narrowed the not-describable class to genuine ceremonies via `derive_reads` +
  mint-and-reveal, with a closed `[no-op-meta: <code>]` exemption vocabulary.
- **Facet (the reference consumer): COVENANT-GATED.** `scripts/lint-facet-discovery.go` (CI STRICT)
  bans vertical vocabulary/op literals/key shapes from `cmd/facet`; forms render from the
  `manifest.op.*` descriptor rows. The display-name convention (ratified 2026-07-18, D4) added the
  renderer floor rule — *a bare NanoID is never a primary label* — and shipped it for Facet.
- **The vertical FEs: DELIBERATELY LEFT HARDCODED.** The edge-showcase design §1 documented the
  disease (loftspace's 16 op call sites + `COMPLETIONS` registry) and built Facet *beside* the apps;
  the Standard codified the tolerance ("a staff FE hardcodes its own dispatch"). No gate, no skill
  guidance, and no P5-legal descriptor source existed for a staff app — the op catalog is a
  per-actor *personal* lens (Facet's transport), and `loftspace app.js:76` names the gap: *"the
  generic DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan would
  violate P5 in a vertical app."*

## 2. The occurrence inventory (audited at `872a325b`)

| App | Submission sites | Distinct ops | Descriptor consumption | Hardcoded per-op UI | Client-rebuilt Core-KV keys |
|---|---|---|---|---|---|
| `cmd/loftspace-app` | 22 (21 JS + 1 Go) | 28 | title/description via my-tasks lens only | ~1,600 JS + ~130 HTML lines (~38%) | 13 six-segment `lnk.*` sites + ~12 aspect-suffix reads; 4 dedicated helpers |
| `cmd/clinic-app` | 19 | 21 | **zero** | ~4,500 of ~4,900 lines (>90%) | 7 sites, 2 of them **required** reads; JS mirror of the Starlark slot-grid discretization |
| `cmd/cafe-app` | 14 | 7 | **zero** | ~445 JS + 15 HTML lines (~40%) | 2 link-key helpers feeding 19 hand-enumerated read arrays |
| `cmd/wellness-app` | 15 | 15 | **zero** (3 comments cite descriptors as rationale) | ~980 lines (~41%) | 11 inline `lnk.*` splices + 4 helpers re-deriving seat/waitlist/slot-cell read sets |

**~70 submission sites, ~60 distinct ops, ~7,500 lines of per-op form/dispatch code, 0 lines
reading `InputSchema`/`FieldDescriptions`/`Dispatch`** — while the descriptors for nearly every one
of those ops already exist in the owning packages. Recurring sub-patterns, worst-first:

1. **Server logic re-implemented client-side to hand-declare reads.** Clinic mirrors the
   slot-grid discretization (`app.js:1937-1962`, its comment admits it); wellness re-derives
   seat/waitlist/slot-cell key sets; every app splices 6-segment link keys with an `idOf()`/
   `shortKey()` helper. A package-side change to any of these shapes silently breaks the app's
   `reads` declarations at runtime.
2. **Validation/enum constants duplicated from the DDL, not read from it** (wellness capacity ≤ 200,
   repeat ≤ 52; loftspace's `EMPLOYMENT_OPTIONS` "mirrors the SetApplicantProfile enum").
3. **Hand-mirrored authorization** (clinic's `applyHatGating` re-encodes the grant matrix for UI
   visibility) and hand-mapped rejection codes (7 substring checks → English).
4. **D4 floor-rule violations** (the "no NanoIDs" half): clinic renders a front-desk user's own bare
   NanoID as the signed-in header *by design* of its fallback chain, and echoes full `vtx.*` keys
   into success toasts (patient/provider/appointment created); loftspace prints truncated keys as
   always-on "reference codes" on application/task/renewal cards + full keys in 4 toasts; wellness
   asks staff to *type* `vtx.identity.…` to book a guest (placeholder input); café/wellness show
   truncated-key fallbacks when a roster/name lookup fails. The three open board rows (café roster
   502 → raw keys; wellness null-studio cards; loftspace unnamed renewals) are instances of this
   class being filed piecemeal.

## 3. The census correction the gate surfaced

S1's census treats `operator` as a **trusted-tool role** ("an op reachable only by them owes no
descriptor" — the admin tool hardcodes its own dispatch). The apps falsify that premise by
demonstration: **16 references to 15 operator-granted ops with real human forms and no descriptor,
no exemption** — `CreateProvider`, `SetProviderProfile`, `SetSiteProfile`, `AssignProviderSite`,
`RemoveProviderSite` (clinic-domain), `CreateLocation` (location-domain, reached from two apps),
`AssignUnitOwner`, `SetUnitAddress`, `SetListing` (loftspace-domain), `DebitAccount`
(loftspace-ledger), `SignRenewal` (lease-signing), `TombstoneStudio`, `CreateInstructor`
(wellness-domain), `AttachObject`, `DetachObject` (objects-base). A shipped staff form is proof a
person triggers the op — and the staff-worlds catalog (edge-manifest's held-role walk) cannot render
what nothing describes. These 15 are now the shrink-only `appOpDebt` baseline in the new gate, i.e.
the descriptor sweep's exact work-list.

## 4. Standards / skills / lints — what existed, what this session added

**Existed:** the Standard (S1–S10) + `lint-package-standard.go` (packages, blocking);
`lint-facet-discovery.go` + `lint-facet-renderer-drift.go` (cmd/facet only); `lint-web.go`
(derived-key ban in web assets); the display-name convention D1–D4 (ratified; enforced nowhere
outside Facet); `docs/components/_packages.md` authoring quick reference (no descriptor mention);
`agents/fe-engineer/SKILL.md` (zero descriptor guidance).

**Added this session:**

- **`scripts/lint-app-op-descriptors.go`** — the app seam of S1, blocking in CI over `cmd/*-app`
  non-test sources. R1: every submitted `operationType` literal must be registered (a package
  rename breaks CI, not a person's click). R2: every referenced op must carry a full descriptor or
  a client-side exemption; a machinery exemption (`engine-op`/`reply-op`/`lifecycle-op`) reached by
  app UI **fails** — the UI falsifies "no person dispatches this". The 15 undescribed ops above are
  the shrink-only baseline (stale entries fail too). Prints the per-app op-literal measure every
  run. All four rules mutation-tested (bogus literal, deleted baseline entry, phantom entry,
  engine-op reference).
- **`agents/fe-engineer/SKILL.md`** — new "Descriptor discipline" section: wire UI only to
  described ops (descriptor-first, package work before FE work); hand-built forms *transcribe* the
  descriptor/DDL with a citation, never invent bounds/enums/read-sets; D4 floor rule binds every
  renderer. Gate added to the role's gate list.
- **`docs/components/_packages.md`** — quick reference now names `opmetas.go` as a first-class
  authoring artifact and points at the Standard + both gates.
- **Board rows** (verticals lane): the 15-op descriptor sweep (ready — the baseline is the
  work-list); the descriptor-driven staff rendering item (op-catalog read model + shared renderer —
  needs design; the P5-legal successor to `COMPLETIONS`); the D4 floor-rule sweep across the four
  FEs (ready).

## 5. What is deliberately NOT gated yet

Hand-built forms per se. The sanctioned alternative for a staff app — an op-catalog read model
(plain lens over op-metas projecting the descriptor vocabulary to a KV bucket apps may read under
P5) plus a shared form renderer — does not exist, and a gate must not ban an idiom before the
alternative ships. When that design lands, its own fire carries the shrink-only migration baseline
(per-app hardcoded-op counts are already printed by the gate on every run, so the trend is visible
now). Field-level drift between a hand-built form and its descriptor's schema is likewise carried
by the skill + review layer until the renderer makes it structural.

## 6. Same-day outcome (adjudicated)

The sweep (§3's work-list) shipped **12 of 15** descriptors across five packages (version-bumped
in lockstep; all gates green, including the build-tagged lease-convergence suite run locally).
Three stayed in the `appOpDebt` baseline with grounded reasons rather than exemption codes —
`CreateLocation` (one op on three leaf DDLs; a static `Dispatch.Class` cannot express the class
choice), `AttachObject`/`DetachObject` (inputs are the byte-plane upload response; the fix is an
owner-anchored read surface + upload affordance, not a marker) — and the sweep exposed a gate
blind spot: unquoted JS object keys (`SignLease:` in loftspace's `COMPLETIONS`) were invisible to
the quoted-literal scan. The gate now carries a keyed-op detector; `SignLease` is the baseline's
fourth entry. All four residuals are owed by named increments of the ratified
[staff-descriptor-rendering-design.md](../../_bmad-output/implementation-artifacts/staff-descriptor-rendering-design.md)
(Winston-adjudicated under Andrew's 2026-08-20 delegation: no-fork/no-contract designs need no
Andrew approval), whose Inc 1 (op-catalog lens + loftspace pilot) is build-ready.
