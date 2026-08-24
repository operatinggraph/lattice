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

Dropping the marker is inert for these three gaps, but **not** for the reason a first reading suggests,
and the difference is worth stating because it is where this could have gone wrong.

`gapSuppressed`'s inflight term (`evaluator.go:1022`) does read absent and `false` alike, via
`boolColumn`. **`staleMark` does not** (`evaluator.go:366-368`): it tests *declaredness* first and
returns `false` outright for an undeclared column, where a declared-`false` marker on an external gap
returns `true`. So dropping the column flips `confirmedConcluded` from `true` to `false` for these gaps.
Three terms consume it, and each is inert here:

- `collapseOnlyReclaim` (`reconciler.go:53-56`) excludes `actionDirectOp` outright, so `collapseOnly` is
  `false` under either value — these gaps are never backed off on that term.
- `uncappedExternal` (`reconciler.go:558`) needs `confirmedConcluded && !hasUsableRetryCap`. Both
  conjuncts flip against each other here and the term settles `false` either way once the cap lands —
  which also returns `markTTL` to its default backstop, correct now that no backoff window has to be
  outlived.
- The **`claimId` choice** (`reconciler.go:636-644`) is the one that would have mattered, and it does not
  reach a `directOp`. A fresh-vs-preserved `claimId` is load-bearing only where it seeds a stable
  artifact identity — `deriveStableInstanceID` for `triggerLoom`, `deriveStableTaskID` for `assignTask`
  (`strategist.go:185,240`). `directOp`'s plan takes `payload: func(string) map[string]any { return
  params }` (`strategist.go:307`) and leaves `plan.requestID` nil, so the Contract #4 idempotency key is
  `deriveEpisodeRequestID(targetID, entityID, col, markRevision)` (`evaluator.go:715`) — **mark-revision
  scoped, not `claimId` scoped**. The reclaim's in-place `replace` mints a new revision, so every reclaim
  is a genuinely new op regardless. A paged sweep keeps re-dispatching real work, which is the whole
  requirement.

- **Lane-1's stale reclaim** (`evaluator.go:329`) is a **fourth** consumer, in the other dispatch leg,
  and it is the one this design's first cut missed outright. `stale := found && !leaseLive(...) &&
  e.staleMark(...)` gates `fireEpisode`'s `found && stale` branch (`evaluator.go:565-616`): with the
  marker declared, a CDC delivery onto an expired mark reclaimed it in lane-1; without it, a first
  delivery is the anti-storm drop and a redelivery re-fires on the unchanged `markRev`, which the
  Contract #4 tracker collapses. **So lane-1 now contributes no re-dispatch for these gaps and recovery
  rests entirely on the sweep.** Accepted, not overlooked: the sweep's 1-minute cadence
  (`reconciler.go:21`) already drives every page, and losing lane-1's reclaim is offset by the
  `uncappedExternal` backoff no longer pacing these gaps — the two removals roughly cancel at a
  per-page cadence of about one mark lease.

What changes, then, is only that the row stops *claiming* an in-flight window it never has — a
synchronous commit has none — and stops using that claim to decline a budget.

**The cap is derived from the sweeps' own reach, summed over the arms the gap covers.** Each op sweeps
**one arm per commit**, in a fixed order, and each arm carries its own read window: a pass reads at most
`MAX_PAGES × PAGE_LIMIT = 64 × 256 = 16 384` links and tombstones at most `SWEEP_LIMIT = 64` live ones.
A drained arm does **not** fail — `collect_live_sweep` returns `[]` on cursor exhaustion and the op moves
to the next arm — so dispatches past a single arm's ceiling are still progress, and the bound is
`Σ_arms(MAX_PAGES × PAGE_LIMIT) / SWEEP_LIMIT`:

- `missing_credentialResidue` — `UnbindIdentityCredentials`, **2 arms** (`boundTo` in, then out;
  `packages/identity-domain/unbind_identity_credentials.go:399,404`) → **512**.
- `missing_dedupResidue` — `PurgeIdentityDedupFootprint`, **3 arms** (`indexes` in, `duplicateOf` out,
  `duplicateOf` in; `packages/privacy-base/purge_identity_dedup_footprint.go:403,408,410`) → **768**.

A real person's credential and dedup fan-out is single- or double-digit; these caps stand orders of
magnitude above any plausible subject and are reachable only by a sweep that is not draining.

**`maxretries_erasureSeal` is 768, matching the widest sibling.** The terminal gap does not page — one
verify plus one attestation per commit (`targets.go:44-49`) — so it has no reach of its own to derive
from, and the sizing is a judgement stated as one. It is deliberately **not** small: because exhaustion
is not self-clearing (§3.3), a short fuse would permanently park a live erasure over a merely transient
cause. Sizing every gap in the target alike means none parks while another could still be converging.

**What the caps restore.** `escalateExhaustedGap` becomes reachable, so the §10.8 promise holds: a stuck
erasure now stops **loudly** — `GapBudgetExhausted` (warning, entity-scoped, `evaluator.go:1104`), or an
Augur `exhausted` escalation where a target configures one — instead of grinding on unseen. The
erasure's incompleteness stays visible in the residue lens's `violating` column. It is **not** covered by
the target's two `surface` gaps: `missing_vaultDestruction` and `missing_projectionNullify` are different
conditions, both closed in every stranding scenario, so once a residue gap exhausts, `GapBudgetExhausted`
is the only *active* signal — `violating` is a KV column with no alerting path.

**A side effect, named:** with a usable cap declared, `hasUsableRetryCap` is true, so `uncappedExternal`
(`reconciler.go:554`) is false for these gaps and the exponential reclaim backoff no longer paces them.
That is the intended direction — the pacing was the stopgap that comment calls "until then" — and a
paging sweep reclaiming at mark-lease expiry is the sweep doing its work, not churn.

### 3.2 The validator refuses only `directOp` — the one action where the harm is real and static

The gate: **for a gap whose action is statically external-class, a declared `inflight_<g>` requires a
declared `maxretries_<g>`.** Declaration is read from the **union of the feeding lens's
`Output.BodyColumns` and `Output.StaticEmptyColumns`** — the projection driver writes body columns and
then `envelope[col] = []any{}` for every static-empty one (`internal/refractor/projection/driver.go:70-73,
127-130`), and Weaver unmarshals that envelope verbatim, so both lists reach the row. `BodyColumns` alone
is *not* the authority: a marker declared only in `StaticEmptyColumns` still makes `gapSuppressed`'s
`_, declaresInflight := row[...]` true. Reading the cypher instead would be unsound in the other
direction — an alias no descriptor lists never reaches the row at all.

**Why one action of the five.** The validator must agree with the engine's classifier
(`externalDispatchGap`, `evaluator.go:406-429`) or it is worse than no gate — but agreement is necessary,
not sufficient:

| action | classifier | statically decidable? | in the gate |
|---|---|---|---|
| `directOp` | external outright (`:407`) | **yes** | **yes** |
| `proposedOp` | external outright (`:407`) | yes | **no — see below** |
| `triggerLoom` | external **iff** the pattern's every step is non-parking (`externalEligibleSteps`) | **no** | no |
| `assignTask` | never external (`:426`) | n/a — nothing to check | no |
| `surface` | never external (`:426`) | n/a — dispatches nothing | no |

**`proposedOp` is statically decidable and still excluded**, because refusing it would refuse the
better-behaved of two identical outcomes. `gapSuppressed`'s default-budget fallback is `directOp`-only
(`evaluator.go:1032-1041`), so a `proposedOp` gap is equally uncapped whether it declares the marker or
neither companion — and the shipped `augur` target (`packages/augur/targets.go:21`) is the admitted half
of that pair. The engine also contradicts itself here: `externalDispatchGap` calls `proposedOp` external
while `collapseOnlyReclaim` (`reconciler.go:53-55`) puts it in the collapse-only set. A gate inheriting
one of two disagreeing sites is inheriting the disagreement, so the gate stays where the harm is
unambiguous. The pin test therefore asserts *containment* — `directOp` is in the engine's unconditional
clause, the gate's set is a subset of it, and every excluded member carries a recorded reason — so a new
external action added to the engine fails the pin until someone decides whether §10.3 bites for it.

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

### 3.3 Exhaustion is a park — the cost, accepted rather than denied

The `targets.go` text this fire rewrote argued *against* declaring a cap, and its mechanism claim was
correct and remains correct: `escalateExhaustedGap` "never touches this gap's own mark"
(`evaluator.go:1093-1095`), so after exhaustion the mark TTL-expires, the sweep — which enumerates
**marks**, not rows (`reconciler.go:58-67`) — loses the entity, the lens projects no `freshUntil` so no
timer re-delivers the row, and nothing writes the subject (that is what "stuck" means) so no CDC delivery
arrives either. Meanwhile the `__count` key persists and clears only on gap-close, which the suppression
itself prevents. The loop is self-sealing, and because the standing issue lives in the in-process
`issueCache` (`health.go:82-95`), a Weaver restart after exhaustion leaves no alert, no dispatch, and a
still-suppressed gap.

**That argument is answered, not deleted.** It is a reason to size the caps so that reaching one means
*definitively not draining* — which §3.1 does, at orders of magnitude above any plausible subject — not a
reason to prefer the prior state, where a stuck erasure re-dispatched forever and §10.8's escalation was
structurally **unreachable**. A bounded park that alerts once beats an unbounded grind that never alerts.
The operator recovery is real and now documented rather than folklore: clear
`<targetId>.<entityId>.<gapColumn>.__count` in `weaver-state` once the cause is fixed.

**What is genuinely missing is engine-level and filed, not papered over:** a durable, re-armable
representation of an exhausted gap, with a defined un-park path. That is a pre-existing property of
`GapBudgetExhausted` for every capped gap in every package — this fire does not introduce it, it puts a
compliance path behind it — and it has no ratified pattern to extend, so it is filed on `lattice.md` as
the one designer-pass row this item produces. **Not** an Augur `exhausted` block, which was considered
and rejected: escalation *clears* the standing issue before dispatching `CreateAugurReasoningClaim`, an op
only the `augur` package's DDL admits, so in a deployment without that package it would trade a warning
for a failed dispatch and couple a compliance package to another package's installation.

### 3.4 Blast radius — verified, not estimated

A census of every `inflight_`/`maxretries_` declaration in the repo (three scouts, re-verified against
the files) puts **six** shipped `inflight_*` body columns on the board — but only five of them are
companions. lease-signing also declares `inflight_docGen` (`lenses.go:51`), which pairs with no
`missing_docGen` gap: the doc-generation gaps are `missing_leaseDoc` (`triggerLoom`) and
`missing_leaseDocAttach` (`directOp`, no marker). A column with no gap of the same name is not a
companion and the gate never reaches it. The five that are:

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

---

## 7. Close — what the reviews found, classified

Three cold reviewers (gate false-refusal · runtime behaviour · test integrity), none of them an
implementer. **No blocking finding.** The gate survived 25 of 30 mutations on the first pass; both
mandated test shapes — the `validateAll`-driving member test and the `go/ast` cross-package constant pin
— were independently confirmed real and loud. What the reviews did break was **this design**, not the
code built from it. Classified per component:

**Design gaps (all mine, all fixed in the same fire):**

- *A derived constant derived from one iteration of a loop.* The caps came from a single arm's read reach
  and under-sized `credentialResidue` 2× and `dedupResidue` 3× (§3.1). The op yields to the next arm on a
  drained cursor rather than failing, so the original "a 257th dispatch cannot be progress" was false.
- *A consumer set asserted complete without grepping for it.* `staleMark`'s declaredness test has a fourth
  consumer in lane-1 (§3.1), missed entirely; the first cut also cited `boolColumn` for an inertness that
  `boolColumn` does not provide.
- *A declaration surface named by the design rather than derived from the writer.* `Output.BodyColumns` is
  not the row body — `StaticEmptyColumns` reaches it too, and a marker declared there slipped the gate
  completely (§3.2).
- *A gate inheriting one side of an engine disagreement.* `proposedOp` was included because
  `externalDispatchGap` calls it external, while `collapseOnlyReclaim` classes it collapse-only and the
  default-budget fallback never applies to it — so the refusal would have hit the better-behaved of two
  identical shapes (§3.2).
- *A cost deleted instead of answered.* The prior `targets.go` paragraph arguing against a cap was correct
  about its mechanism; §3.3 now answers it and names the operator recovery.

**Test-integrity gaps (fixed):** the cap values were asserted against themselves — a reviewer set the
sweep cap to 7 and to 999 with the whole suite green — now pinned against the Starlark page constants they
derive from, with the arm count counted from the source; four tests dereferenced a possibly-nil error, so
any regression that disarmed the gate panicked and masked the rest of the binary; and the commonest
*accept* case had no fixture, so inverting the gate into an install-blocking false refusal survived green.

**Implementation bugs: none.** Both builders' code was sound; every MAJOR traced to a ratified claim here.

**Discovery, fixed this run (§4's "what a fire discovers, this run fixes"):** `wellness-ledger` projected
`maxretries_price` for gap `missing_price_charge` — the engine derives the name from the gap key, found
nothing, and silently fell back to the default budget of 3, which happened to equal the intended cap. Dead
since it shipped; corrected, with the class routed to the `_packages` dossier.

**Filed:** one row, the §4 out-#2 designer pass named in §3.3. No residual rows, no deferral labels.

**Lessons routed:** three entries to `docs/components/_packages.md` — the dead companion column, the
per-arm cap derivation, and absence-vs-declared-false. `weaver.md` (12) and `pkgmgr.md` (13) are at and
over the 12-entry cap, so their share of this fire's lessons lives in the gate's own comments and here
rather than displacing an entry that still earns its slot.
