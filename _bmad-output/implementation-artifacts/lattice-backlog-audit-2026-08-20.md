# Lattice backlog audit — steward-filed rows (2026-08-20)

**For Andrew.** You asked whether the steward-filed rows accreting on `backlog/lattice.md` are genuinely
needed or token-burning, and whether simpler solutions exist. Winston (Designer lane) audited **30 rows**
(everything filed since the 2026-08-11 board, plus the still-open residual clusters) — each premise
**re-grounded in code**, not trusted from row/design-doc prose, over seven read-only sub-agents.

**Headline:** the work is **not fabricated** — every premise checked out as a real code path (three were
*partially* falsified; see below). But the backlog **is** inflated, by three mechanisms that are all
*filing-discipline* defects, not fake findings:

1. **Under-consolidation at filing.** One fire files N rows where the remaining work is ~N/3 units. The
   erasure fire filed **8 rows → 3 real units** (1 design, 1 verify-tooling task, ~3 batchable XS fixes,
   2 parks). The forgery pair (A+B) is **one** design. The §29 pair (D+E) is **one** S fire.
2. **Size / gate inflation.** Routine XS–S fixes filed as `📐 needs designer pass · M`. In every such case a
   **ratified pattern to extend already exists in the same file** — so §2.5's own test ("no ratified pattern
   to extend") says *steward builds it*, not *designer designs it*. "📐 M" is being used as the default
   label for "I found something and didn't fix it," which is exactly the do-less-file-more failure the
   2026-08-08 process review already ruled against.
3. **The residual-harvest norm has no net-flow brake.** Steward `SKILL.md §4` mandates filing every named
   residual with only two outs (Andrew / designer). Andrew's 2026-08-08 ruling — *"what a fire discovers,
   THIS RUN fixes"* — is not binding in practice: these fires kept filing. `📐`-tagged rows went **0 → 12**
   in nine days.

**Net effect of the corrections applied below:** the Lattice lane's `📐 needs-designer-pass` count drops
from **12 to 4** (only the genuinely design-shaped items remain), and ~11 open rows are merged, folded,
moved to their correct lane, or parked behind a real revive-trigger.

---

## Per-row verdicts

Legend: **KEEP** = correctly filed, leave it · **RESCOPE** = real but mis-sized/mis-gated, corrected in
place · **MERGE/FOLD** = same-root-cause as a sibling, collapsed · **MOVE** = misfiled cross-lane · **PARK**
= real but zero live driver, moved to the parking lot with a named revive-trigger.

### The genuine design items (KEEP — these earn the 📐)

| Row | Grounding | Verdict |
|---|---|---|
| **[bootstrap] `UpgradePackage` create-arm forges a package-origin permission/role vertex** (★★★ M) | TRUE and worse than filed: `rejectPermissionRoleRewrites` skips `create`; a bare 3-seg `vtx.permission.*` matches neither shape-check in `rejectPackageScopeViolations`; self-declared `origin:"package"` defeats `WouldRefuseReservedGrant`. Trigger is **not root** — `consoleOperator` (Loupe's control-plane identity, deliberately non-root) holds `UpgradePackage@meta`. No XS exists (server-side Definition registry is import-cycle-blocked). | **KEEP** — the one true ★★★ designer item; now also owns the grantedBy-revival gap. |
| **[Edge] first-paint gate has no identity for the hydrate cycle** (★★ M) | Verified end-to-end; the client-side fix was **built and refuted** on 4 defects incl. a permanent session-hang; §6 names three unresolved design needs. Live user-visible harm today (Fire 2 unmasked it). | **KEEP** — design earned by build-and-refute evidence. |
| **[Processor] sensitive resolution trusts self-reported `class`** (★★ M) | TRUE; a cheap derive-and-compare is *actively wrong* (`"profile"` legitimately maps to 4 classes by anchor type; `DDLSpec` has no localName field). Real fix = a new declared per-DDL localName + install-time uniqueness + corpus backfill. From Winston's own security review. | **KEEP** — correctly M; the design decision is the declared-field semantics. |
| **[Pkgmgr] no live-vs-declared reconciliation for permission vertices** (★ S) | TRUE narrowly; complementary to the forgery fix (catches self-consistent forgeries + honest drift). | **KEEP** — correctly S/📋. |

### Mis-sized / mis-gated (RESCOPE — steward builds, no designer pass)

| Row | Why it is not a designer pass | Correction |
|---|---|---|
| **[natsperm] `$JS.ACK.>` ack-forgery** (was ★ M 📐) | The 6-consumer deny registry the fix needs **already shipped** (`83891a8c`, MSG.NEXT steal). The fix is one ACK subject appended to the same `Deny()` loop; v1 ack-format is live so per-consumer scoping works today. | **📋 XS.** Mirror the shipped registry line. |
| **[Refractor] two miscompiled clause shapes** (was ★★ M 📐 "clause-semantics fork, corpus-wide blast radius") | Census confirms **0 live lenses** (all 31 LensSpec files + generated lenses, which emit only `OPTIONAL MATCH`/named `WITH` — immune by construction). `WITH *` = a ~3–5-line `v.fail()` mirror (idiom used **4×** in the same file); the MATCH shape = ~30–60-line visitor pass with an adjacent precedent (`withScopeReject`). Nothing is forky. | **📋 XS–S.** Refuse-at-parse. The "fork" framing *is* the inflation. |
| **[identity-domain] claim-rejection latency channel** (was ★★ M 📐 "constant-time rejection") | NFR-S6 (grounded, `epics/index.md:129`) bars exposing permission-graph **structure** — it says nothing about **timing**. The ★★ rests on a self-imposed standard. Delta is ~tens of µs under NATS/GC jitter — lab-measurable, not plausibly remote; leaks only claimed/unclaimed for an already-known key. | **📋 XS–S ★.** A fixed floor on the one endpoint's rejection paths, not a new pattern class. |
| **[privacy-base] pre-narrowing shredded subject earns a clean attestation** (was ★★ S–M 📐 "undesigned primitive") | "Undesigned primitive" is overstated: `cmd/lattice/identity/reconcile.go:153` already prefix-scans `credentialindex` out-of-write-path. Producer deleted `54b3c8c7`, so the population is **capped, not growing**. | **📋 S.** A small CLI tool + one narrow tombstone op (gated on owner `piiKey.shredded` + dead `boundTo`), precedent named. Do before the first real erasure. |
| **[Processor] §2.5 floor skips `{me.*}`/`{entity.*}` templates** (★★ M 📐) | Multi-anchor undecidability is **real** for concrete-value resolution — but a shape-matching floor (unresolvable segment = bounded single-segment wildcard) sidesteps anchor semantics entirely. | **Kept 📐, rescoped:** first act is a shape-match spike; it may collapse M→S with no `{me.*}` decision needed. |

### Same-root-cause (MERGE / FOLD)

| Rows | Finding | Correction |
|---|---|---|
| **[rbac] grantedBy revival** → folds into **[bootstrap] forgery** | Design doc's own §15 concludes both need the *same* server-side Definition-vs-mutation check; an XS was tried and falsified (`OperationType` can't split attack from legitimate re-add). | **FOLD** into the forgery design's scope; remove the standalone row. |
| **[Pkgmgr] §29 dropped-column (R2)** + **§29 rename (R3)** | Same file (`upgrade.go diffManifest`), same failure family; Fire 28 is the ratified carve-out precedent; the author-declares-intent annotation mirrors the shipped read-posture default-deny. | **MERGE** to one **📋 S** row (steward-buildable, no designer pass). |
| **[privacy-base] never-run-e2e** + **no `verify-package-*` target** | The first is a verify-script task (precedent: `verify-claim-ceremony.go`); the second is a mechanical `verify-package-identity.go` copy. | **MERGE** to one **📋 S** "privacy-base verification tooling" row. |

### Misfiled cross-lane (MOVE)

| Row | Finding | Correction |
|---|---|---|
| **[wellness-ledger] `wellnessMemberAccounts` has no retraction transport** | Premise **half-falsified**: the "three siblings declare `DiffRetraction`" is **false** (grep: `DiffRetraction` appears nowhere in wellness-ledger). The real precedent is `loftspace-domain/lenses.go:100-113` (identical shape, `DiffRetraction:true`). The "known mass-Delete hazard" gating it **does not apply** (anchored-hazard is auto-guarded at activation; shared-bucket hazard N/A — the bucket is single-lens). | **MOVE** to `verticals.md` as an **XS** package edit (mirror loftspace). It is neither Lattice-lane nor design-gated. |
| **[Loupe] `protected` config field reads malformed as "not protected"** | Real but **display-only** confirmed (both consumers are UI badges, never a gate); no writer emits malformed today. `cmd/loupe/**` belongs on the Loupe lane per the board's own header. | **MOVE** to `loupe.md` as **XS**. |

### Real but no live driver (PARK — parking lot, named revive-trigger)

| Row | Why park | Revive-trigger |
|---|---|---|
| **[Loom] externalTask subject-only egress** (★★ M) | The one consumer (verticals: *"the executed lease still doesn't name its tenant"*) has a **demand-side fix**: snapshot the applicant name onto the LEASEAPP as a sensitive aspect at `SignLease` (a non-`external.*` op, unrestricted per §3.6) and read it via the **already-shipped** `subject.<aspect>` path — zero Loom/Processor change. The platform primitive serves one field in one package. | A **second** link-hop-egress consumer. |
| **[Weaver] paged re-dispatch throttled to one mark lease** (★★ M–L) | Census kills the trigger: median erasure subject has 2–3 links vs `SWEEP_LIMIT = 64` — pagination never engages; **zero** real erasures have ever run. | A real erasure observed taking >1 pass. |
| **[privacy-base] stuck sweep re-dispatches with no escalation** (★★ S–M) | Named consumer (§12 step-4 operator surface) is explicitly **unbuilt**; Loupe's generic Weaver view already lists stuck gaps worst-first with lease timestamps (free partial mitigation). | A real stuck erasure. |
| **[Processor] root actor set is a boot snapshot** (★ S–M) | Latency/consistency only (self-graded), fail-closed; the only trigger is a manual `AssignRole` by an existing operator — never happened live. | A live-grant workflow that cannot tolerate a restart. Ops note: restart the 4 binaries after granting a new operator. |
| **[Refractor] rule-engine memoized reads vs bulk `KVGetMulti`** (★ S) + **[Weaver] `sweeper.pass` joint-snapshot** (★ S) | Both name the enumeration-corpus sweep as consumer — that project **shipped and closed** `717312ca`, deferring these two **by name**; zero remaining demand, no profiling cited. | An observed read-count/latency signal on the specific site. |
| **[Tooling] `expectedRevision` pin has no red test** (★ XS–S) | Premise **partially falsified**: the generic CAS conflict **is** tested (`step8_commit_test.go:71`); only the op+race *composition* is not, and no in-process interleaving hook exists — a presence-test would be theater. | An in-process interleaving harness exists. |

### Correctly filed small fixes (KEEP — but should have been fixed in-fire or batched)

`[Docs] kernel-seeded prose` (XS; code sites fixable now, the Contract #6 §6.1 line is an uncommitted edit
for Andrew) · `[Pkgmgr] packageName byte-exact` (XS–S, compare-only) · `[Tooling] verify-claim-ceremony 5s
SLA` (XS — note: the row's own "poll to convergence" prescription is wrong; the outer 30 s ctx truncates it) ·
`[Loom/Weaver] class-(e) enumeration declaration` (XS, nothing degrades at runtime) · `[Weaver] Health issue
entity segment` (XS, in-memory cache, no migration) · `[Refractor] dispositionEvalErr no CatPrivacyCritical
arm` (XS, mirror the shipped+tested `classifyForSupervisor` arm) · `[identity-domain] credentialindex residue`
(XS, copy the in-code caveat into the DDL's public Description) · `[Pkgmgr] live-vs-declared reconciler` (S,
kept above).

These eight are ~100–170 lines total. As **one batched hygiene fire** they cost a fraction of eight separate
briefs/reviews/close-outs — and several were in-scope for the fire that found them (Andrew's "THIS RUN
fixes" ruling).

---

## The three partially-falsified premises (filed findings that overstated)

1. **wellness-ledger** "three siblings declare `DiffRetraction`" — they do not; the blocker rationale
   (mass-Delete hazard) does not apply. Turned a mirror-a-shipped-primitive XS into a design-gated ★★ S.
2. **expectedRevision** "unverified by any test" — the generic mechanism *is* tested; only the composition is not.
3. **pre-narrowing attestation** "needs an undesigned enumeration primitive" — a prefix-scan precedent already
   exists out-of-write-path; the concrete (capped) population is closable without a new primitive.

Each overstatement pushed a cheaper item up a size/gate tier — the same inflation vector as (2) above.

---

## What is left for Andrew (not applied)

- **Contract #6 §6.1** still calls the root actor set "kernel-seeded" (three code-comment sites + the contract
  line). The **code comments** are fixable in the hygiene batch; the **frozen-contract line** is staged as an
  **uncommitted edit** per the contract-change rule — it is not committed here.
- The two **genuine design KEEPs still awaiting a designer pass** — forgery (now owning grantedBy-revival) and
  sensitive-`class` — are correctly `📐`; the Edge first-paint KEEP too. The floor row is `📐` pending its
  shape-match spike. Those four are the *real* designer queue.

## Skill-level fix (Designer + Steward improvement loop)

Two structural checks proposed so the inflation cannot recur (see the closing summary): a **consolidation
gate at filing** (residuals sharing a root cause file as one row) and an **honest-gate check** (a `📐 needs
designer pass` label must name the *specific absent ratified pattern* — if a precedent exists in the touched
file, it is a steward `📋`, not a designer `📐`).
