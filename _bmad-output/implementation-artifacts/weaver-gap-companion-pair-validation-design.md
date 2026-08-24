# The `inflight_<g>` / `maxretries_<g>` companion pair — install-time validation, and the `privacy-base` declaration it reconciles against

**Status:** ✅ Winston-ratified — build-ready. Every decision here is implementation-level: an install
gate mirroring a rule already frozen in Contract #10, and one package's declaration corrected to the
shape that rule describes. No frozen-contract edit, no architectural fork. The *textual* ambiguity in
§10.3 that this gate reads around is already prepared separately as an L3 proposal branch
(`claude/contract-10-inflight-suppression-scope`) that nothing here depends on.

**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* →
"[Weaver/Pkgmgr] The `inflight_<g>`⇒`maxretries_<g>` MUST is unenforced; privacy-base ships the
uncapped-external case" (★★, M). Filed by the 2026-08-24 `inflight_<g>` fire
([design](inflight-suppression-vs-reclaim-contract-design.md) §7) as one consolidated row naming both
halves, with the note that the validator's shape depends on the `privacy-base` decision.

**Author:** Winston (Lattice Steward fire, 2026-08-24).

---

## 1. The two halves, exactly

**Half A — the MUST has no enforcement.** `docs/contracts/10-orchestration-substrate.md:248`:

> **A gap that declares `inflight_<g>` MUST declare `maxretries_<g>`:** the fresh-`claimId` path has no
> collapse to pace it, so the budget is its only bound.

Nothing checks it. `internal/pkgmgr/orchestrationguard.go`'s `validateWeaverTargets` validates the gap
key shape, the action table, per-action required fields, the reserved param, the planner fields, the
augur block and the admission block — and never looks at the companion columns.

**Half B — `privacy-base` ships the case the MUST forbids, and it costs the §10.8 promise.**
`packages/privacy-base/lenses.go:363-365` projects three **constant-`false`** `inflight_<g>` columns for
three **`directOp`** gaps (`missing_credentialResidue`, `missing_dedupResidue`, `missing_erasureSeal`)
and declares no `maxretries_<g>` for any of them. `directOp` is external-class outright
(`evaluator.go:406-409`), so this violates the MUST under *both* the literal and the scoped reading.

The cost is not the violation, it is what the violation buys. `gapSuppressed`
(`internal/weaver/evaluator.go:1027-1031`) declines the engine's `defaultDirectOpRetryBudget` for any row
that *declares* `inflight_<g>` — a lens with its own pacing is not the engine's to second-guess. A
**constant-false** marker suppresses nothing while switching that safety net **off**, so the three gaps
have no cap of any kind, `count >= capN` can never hold, and `escalateExhaustedGap`
(`evaluator.go:156`, `reconciler.go:508`) is structurally unreachable for them. Contract #10's

> falls back to the engine's default retry budget (3 dispatches), then raises the §10.8
> `GapBudgetExhausted` standing issue — **a loud stop, never a silent park**
> (`10-orchestration-weaver.md:69-72`)

is therefore unreachable on the erasure residue sweeps: a stuck erasure re-dispatches indefinitely,
backoff-paced by `uncappedExternal` (`reconciler.go:554`) and visible only as a `sweepReclaimsSuppressed`
counter, and no operator is ever told. On a compliance path, that is the wrong silence.

## 2. Why the package did it — the premise is real, the tool was wrong

`packages/privacy-base/targets.go:16-21`: both residue ops **"sweep one bounded page of their respective
residue per commit and re-open until the counts reach zero"**. `SWEEP_LIMIT = 64` live links per commit
(`purge_identity_dedup_footprint.go:182`, `unbind_identity_credentials.go:137`). The dispatch `__count`
is deleted **only on gap-close** or leg-release (`evaluator.go:811`, `evaluator.go:879`;
`10-orchestration-substrate.md:264`) — never on a reclaim and never on progress — so it climbs
monotonically across pages while the gap stays open.

So the package's premise is exactly right: the engine's default budget of **3** would suppress a paged
sweep after three pages and strand the erasure. Its own test says so
(`erasure_residue_lens_test.go:486-492`). What is wrong is the instrument. It reached for the *side
effect* of declaring `inflight_<g>` (opt out of the default budget) rather than for the cap itself, and
in doing so traded a budget that was merely **too small** for **no budget at all**.

## 3. Decisions (Winston, implementation-level)

### 3.1 `privacy-base` declares a cap sized to its own paging, and drops the inert marker

The three gaps **drop `inflight_<g>`** and **declare `maxretries_<g>`**.

Dropping the marker is behaviour-identical on every path that reads it: `boolColumn` reads an absent
column to the same `false` the constant projected, so `gapSuppressed`'s inflight term
(`evaluator.go:1022`) and `staleMark`'s verdict (`evaluator.go:363-380`) are unchanged. What changes is
only that the row stops *claiming* an in-flight window it never has — a synchronous commit has none —
and stops using that claim to decline a budget.

**The cap is derived from the op's own reach, not invented.** Each sweep pass reads at most
`MAX_LINK_PAGES × LINK_PAGE_LIMIT = 64 × 256 = 16 384` links and tombstones at most `SWEEP_LIMIT = 64`
live ones, so draining the widest fan-out a single pass can even *reach* takes `16384 / 64 = 256`
dispatches. **`maxretries_credentialResidue` and `maxretries_dedupResidue` are 256**: beyond that the op
has itself run out of reach (its own documented stall, `purge_identity_dedup_footprint.go:253,274`), so
256 is the point past which more dispatches cannot be progress. A real person's credential and dedup
fan-out is single- or double-digit; the cap is ~3 orders of magnitude above any plausible subject and
will only ever be reached by a sweep that is not draining.

**`maxretries_erasureSeal` is 16.** The terminal gap does not page — it opens only once the other four
close, and its op re-verifies five arms and writes one attestation inside a single commit
(`targets.go:44-49`). It needs slack only for a concurrent write that legitimately re-opens residue
after the seal opened, not for paging, so a cap three orders of magnitude smaller alerts far sooner on a
seal that keeps failing. 16 is a judgement, stated as one: large enough that a racing writer cannot
exhaust it, small enough that a genuinely stuck seal surfaces in the same operational session.

**What the caps restore.** `escalateExhaustedGap` becomes reachable, so the §10.8 promise holds: a stuck
erasure now stops **loudly** — `GapBudgetExhausted` (warning, entity-scoped, `evaluator.go:1104`), or an
Augur `exhausted` escalation where a target configures one — instead of grinding on unseen. The
erasure's incompleteness stays independently visible in the residue lens's `violating` and in the two
`surface` gaps regardless, so the loud stop adds an alert without removing a signal.

**A side effect, named:** with a usable cap declared, `hasUsableRetryCap` is true, so `uncappedExternal`
(`reconciler.go:554`) is false for these gaps and the exponential reclaim backoff no longer paces them.
That is the intended direction — the pacing was the stopgap that comment calls "until then" — and a
paging sweep reclaiming at mark-lease expiry is the sweep doing its work, not churn.

### 3.2 The validator refuses only what it can decide statically: `directOp` and `proposedOp`

The gate: **for a gap whose action is statically external-class, a declared `inflight_<g>` requires a
declared `maxretries_<g>`.** Declaration is read from the feeding lens's `Output.BodyColumns` — the row
body is what the engine reads, so a column absent from `BodyColumns` is a column the Weaver never sees.

**Why only two of the five actions.** The validator must agree with the engine's classifier
(`externalDispatchGap`, `evaluator.go:406-429`) or it is worse than no gate:

| action | classifier | statically decidable? |
|---|---|---|
| `directOp` | external outright (`:407`) | **yes** |
| `proposedOp` | external outright (`:407`) | **yes** |
| `triggerLoom` | external **iff** the pattern's every step is non-parking (`externalEligibleSteps`) | **no** |
| `assignTask` | never external (`:426`) | n/a — nothing to check |
| `surface` | never external (`:426`) | n/a — dispatches nothing |

`triggerLoom` is out of scope deliberately, on two independent grounds: its pattern reference may be a
`row.<col>` template that only a row resolves (`:410`), and even a literal reference needs the pattern's
indexed step kinds, which a cross-package reference does not put in the batch. The engine itself
classifies an unknown pattern as **not** external — the fail-safe direction — and an install gate that
guessed the other way would produce a false refusal, the worst failure mode a gate has. The runtime
`uncappedExternal` backoff remains the backstop for that shape; no shipped package occupies it
(lease-signing's two `triggerLoom` external gaps, `bgcheck` and `payment`, both declare caps).

**Static ≠ runtime, and that is why the runtime backstop stays.** The gate reads *declarations*;
`gapSuppressed` reads *values*. A lens may declare `maxretries_<g>` in `BodyColumns` and project null or
zero into the row, which reads to the uncapped side. The gate raises the floor on authoring; it does not
replace `uncappedExternal`, and this fire removes nothing from the runtime path.

**Where it lives.** `internal/pkgmgr/orchestrationguard.go`, inside `validateWeaverTargets`, which is
already a method on `Definition` and so already holds `def.Lenses`. The companion prefixes and the
external-action set are **re-stated** in the guard rather than imported from `internal/weaver` —
the file's established convention, and its stated reason: the installer must not import the engines
(see its `escalateUnplannable`/`escalateExhausted` and Loom-step-kind blocks).

**Fail-open boundaries, each deliberate and tested:** a `LensRef` that resolves to no lens in this
batch (a NanoID pointing at an already-installed lens from another package) and a lens with a nil
`Output` are both skipped — a gate cannot validate a declaration it cannot see, and refusing on absence
would fail every cross-package target.

### 3.3 Blast radius — verified, not estimated

A census of every `inflight_`/`maxretries_` declaration in the repo (three scouts, re-verified against
the files) puts exactly five shipped `inflight_<g>` declarations on the board:

| package · lens | gap | action | `inflight_` | `maxretries_` | gate verdict |
|---|---|---|---|---|---|
| lease-signing · `leaseApplicationComplete` | `missing_bgcheck` | `triggerLoom` | yes (expr) | yes | out of scope; compliant anyway |
| lease-signing · `leaseApplicationComplete` | `missing_payment` | `triggerLoom` | yes (expr) | yes | out of scope; compliant anyway |
| lease-signing · `leaseApplicationComplete` | `missing_onboarding` | `triggerLoom` | yes (expr) | no | out of scope (parks on a human) |
| lease-signing · `leaseApplicationComplete` | `missing_signature` | `assignTask` | yes (expr) | no | never external — legal |
| privacy-base · `identityErasureResidue` | ×3 residue gaps | `directOp` | yes (**const false**) | no | **refused — fixed by §3.1** |

So the gate's entire blast radius is the three gaps §3.1 corrects, in the same fire. Nothing else
shipped changes verdict, and lease-signing's two human-paced declarations — the ones the 2026-08-24 fire
established are contract-legal and load-bearing — stay legal by construction, not by exemption.

## 4. Increments

1. **`privacy-base` declaration** (§3.1) — lens cypher + `BodyColumns` + the lens/target doc blocks; the
   two tests that pin the old shape rewritten to pin the new one.
   Green: `go test ./packages/privacy-base/...`.
2. **The gate** (§3.2) — `validateGapCompanionPair` in `orchestrationguard.go`, wired into
   `validateWeaverTargets`; tests mirroring `orchestrationguard_test.go`'s shape, including the
   deliberate non-refusals (`triggerLoom`, `assignTask`, unresolvable `LensRef`, nil `Output`).
   Green: `go test ./internal/pkgmgr/...`.
3. **Reconcile what this falsifies** — `reconciler.go:538-550`'s "Install-time validation of the
   companion pair is the real fix (filed…)" is now shipped, and its `privacy-base` counterexample no
   longer exists; the `uncappedExternal` pacing stays and its doc says why (§3.2's static≠runtime).
   `inflight-suppression-vs-reclaim-contract-design.md` §7's filed row closes. Dossier +
   board.
   Green: full gates with Postgres up.

## 5. Non-goals

`gapSuppressed`, `staleMark` and `externalDispatchGap` are unchanged — this fire adds an authoring gate
and corrects one package, and touches no dispatch decision. No `triggerLoom` static classification
(§3.2). No change to `GapBudgetExhausted`'s severity. No frozen-contract edit: the §10.3 scoping
proposal stays on its own branch for Andrew.

---

## 6. Fire brief (build note, 2026-08-24)

**1. Scope sentence.** Enforce Contract #10 §10.3's `inflight_<g>`⇒`maxretries_<g>` companion pair at
install time for the two statically-external gap actions, and correct the one shipped package that
occupies the case it forbids — restoring §10.8's "loud stop, never a silent park" on the erasure residue
sweeps. Green bar: §4's three increments, then the full gates with Postgres up.

**2. Verified touch-list** (`file:line` checked live at `1da97cd`).

| Site | What |
|---|---|
| `packages/privacy-base/lenses.go:363-365` | the three `false AS inflight_<g>` → three derived `maxretries_<g>` |
| `packages/privacy-base/lenses.go:117` | `BodyColumns` — swap the three `inflight_` entries for the `maxretries_` ones |
| `packages/privacy-base/targets.go:16-21` | the gap doc block — say the cap and its derivation, not the marker |
| `packages/privacy-base/erasure_residue_lens_test.go:483-506` | `..._DeclaresUncappedSweepGaps` — its whole premise inverts; rewrite to pin the caps + the absent marker |
| `packages/privacy-base/erasure_residue_lens_test.go:584` | `..._IsShapedAsAConvergenceLens`'s BodyColumns list |
| `internal/pkgmgr/orchestrationguard.go:81-140` | `validateWeaverTargets` — the new per-gap call site |
| `internal/pkgmgr/orchestrationguard.go:14-30` | where the re-stated engine constants live (the file's own convention) |
| `internal/pkgmgr/orchestrationguard_test.go:8+` | the test shape to mirror |
| `internal/weaver/reconciler.go:538-550` | the "filed / until then, pace it" comment — falsified by this fire |
| `docs/components/weaver.md`, `docs/components/pkgmgr.md` | dossier |
| `inflight-suppression-vs-reclaim-contract-design.md` §7 | its filed row closes here |

**3. Precedents to mirror.** `validateAugurSpec` / the Loom-step-kind block in the same file — the
established way the installer enforces an engine rule *without importing the engine*, by re-stating the
vocabulary next to a comment saying so. `resolveLensRef` (`build.go:572`) for canonicalName→lens
resolution. `orchestrationguard_test.go`'s valid-case-plus-comprehensive-negatives shape.

**4. Increment order + runnable green checks** — §4 above.

**5. In-scope gotchas.** Dossier entries this fire trips, verbatim from the owning components:
- *pkgmgr — every member of `validateAll` carries at least one test that drives `def.validateAll()` —
  not the rule — over a fixture legal in all other respects, asserting wording only that member emits, so
  a short-circuit from an earlier validator cannot pass for it.* **Mandated shape; the new gate gets one.**
- *pkgmgr — a refusal's stated remedy must not be a move that defeats the gate.* The refusal here must
  advise declaring the cap, never dropping `inflight_<g>` to dodge the check — dropping it is legal and
  correct only because it restores the engine default, so the message must say which.
- *weaver — a gap class is decided by the dispatch's SHAPE, never by its action name.* The gate reads
  `Action` only for the two values the classifier itself decides without a row; it must not extend that to
  `triggerLoom` (§3.2).
- *weaver — a restated cross-package constant needs a test that pins it.* The re-stated companion prefixes
  and external-action set need a test tying them to `internal/weaver`'s, or they drift unsafely.
- *weaver — an `error`-severity Health issue must not fire on a self-healing condition.* Read here as: a
  refusal must not fire on a shape that is merely unresolvable-in-this-batch (§3.2's fail-open boundaries).
- Standing checklist: **every census is a premise.** This brief's census already overturned a scout claim
  (a `TestReconciler_InflightWithoutRetryCapIsError` it reported at `reconciler_internal_test.go:887` does
  not exist) — re-derive before relying on any count. And: **the clone is shallow** — no history-derived
  negatives.

**6. Adjacent finds.** None out of scope. The `triggerLoom` static-classification limit is a *stated
boundary* of the new gate (§3.2), not a discovery: the engine already fails safe there, the
`uncappedExternal` runtime backoff backstops it, and no shipped package occupies the shape — so it files
no row.

**7. Non-goals.** §5 above.
