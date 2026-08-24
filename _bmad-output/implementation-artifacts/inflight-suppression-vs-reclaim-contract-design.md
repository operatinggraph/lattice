# `inflight_<g>` — one contract for suppression and for reclaim

**Status:** ✅ SHIPPED `3a35bde` (2026-08-24) — Winston-ratified, implementation-level throughout. Every open question here is
implementation-level: the engine change makes the code's *diagnosis* match the frozen contract it
already obeys. The one *textual* ambiguity in §10.3 is prepared separately as an L3 contract proposal
(§6) that main does not depend on. No architectural fork.

**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* →
"[Weaver] `gapSuppressed`/`staleMark` disagree on a human-task gap declaring `inflight_<g>`"
(★★, S–M). Consolidates two `verticals.md` rows (§7).

**Author:** Winston (Lattice Steward fire, 2026-08-24). The row carried `📐 needs designer pass ·
no-pattern: one suppression-vs-reclaim contract`. **The designer gate does not hold:** both readers
live in one file, both rules are already written in frozen contract text, and the fix is to make the
code *say* what the contract says. That is execution, not a new mechanism (`agents/steward/SKILL.md`
§2.5's test).

---

## 1. The disagreement, exactly

Two readers consult the same lens-projected column, `inflight_<g>`, and reach opposite verdicts about
what declaring it *means*.

- **`gapSuppressed`** (`internal/weaver/evaluator.go:1028`) reads it for **every** gap class. True ⇒
  the gap is not (re-)dispatched, on **both** legs — lane-1 (`evaluator.go:142`) and the sweep's
  reclaim (`reconciler.go:488`). No class test anywhere in the path.
- **`staleMark`** (`evaluator.go:363-385`) reads the same column and, when the gap is **not**
  external-dispatch, calls the declaration *"a lens-authoring bug"* and raises an **`error`**-severity
  `InflightActionMismatch` Health issue (`evaluator.go:378`).

Both are reached for the same row. `gapSuppressed` runs first on the sweep leg, so `staleMark` is
consulted only once the marker reads **false** — precisely when the gap is legitimately
re-dispatchable. The result is a standing `error` for a correctly-authored package on every sweep
pass: live since 2026-08-21 on `lease-signing`'s `missing_onboarding` and `missing_signature`.

## 2. Which reader is wrong — grounded in the frozen text

**`gapSuppressed` is right, and `staleMark`'s diagnosis is wrong.**

`docs/contracts/10-orchestration-weaver.md:63-66` defines the column with no class restriction:

> **dispatch-suppression companions `inflight_<g>` / `maxretries_<g>`** (optional, engine-recognized,
> per-gap) — a Lens **may** project, per gap `g`, an `inflight_<g>` bool (a remediation is already in
> flight → suppress re-dispatch)…

Suppression is the column's *definition*, available to any gap. `docs/contracts/10-orchestration-substrate.md:236-248`
then uses the same column for a **second, narrower** purpose — gating the **external-gap
stale-reconcile**, the reclaim that mints a **fresh `claimId`** ("re-call a dead vendor / mint a fresh
service instance"). A non-external gap is governed instead by §10.3's claimId-**preserved-verbatim**
rule, so the marker simply carries no reclaim authority there.

Those are two different questions, and `staleMark` answers only the second one. Answering "no" to the
second is **the contract working as written** — not evidence the lens is misauthored. The package
agrees, in its own words (`packages/lease-signing/lenses.go:526-538`):

> `inflight_onboarding` / `inflight_signature` — the same contract for the two HUMAN-paced gaps…
> Without the companion these two never stop re-dispatching, because only a person can close them:
> the mark lease expires, the sweep reclaims, and Weaver re-fires a remediation whose task is already
> sitting open… every reclaim still books an `__effect` dispatch into a window that cannot record a
> close until the person acts, which surfaces downstream as a **false `LensEffectMismatch`**.

So the declaration is deliberate, contract-sanctioned, and load-bearing — it is what stops the other
standing alert.

## 3. The one contract, stated

> **`inflight_<g>` means exactly one thing: suppress (re-)dispatch.** It is available to any gap and
> honored on both dispatch legs.
>
> It **additionally** licenses the **external-gap stale-reconcile** — a reclaim that mints a fresh
> `claimId` — **only** where the gap's dispatch is external, read from the pattern's own step kinds
> (never from the action name). For any other gap the marker suppresses and nothing more; the
> `claimId` stays preserved verbatim.

Both halves are already frozen text; only the code's *diagnosis* diverges from them.

## 4. What changes

**Behavior on every existing row is unchanged.** `staleMark` still returns false for a non-external
gap (marker ignored, `claimId` preserved verbatim); `gapSuppressed` is untouched. What changes is what
the engine *says*: a non-external gap declaring `inflight_<g>` is a §10.2 suppression declaration, so
`staleMark` logs it at Debug and raises nothing.

`InflightActionMismatch` is raised nowhere afterwards, so the class, its key helper
(`issueKeyInflightMismatch`), its prefix constant (`issuePrefixInflightMismatch`), and its
`issueKeyTargetPrefixes` entry are deleted outright. The `issueCache` is a process-local map
(`internal/weaver/health.go:76-84`) with no persistence, so any standing entry drains on the Weaver
restart the deploy performs — no migration.

**Why not also enforce §10.3's MUST here.** §10.3 says *"A gap that declares `inflight_<g>` MUST
declare `maxretries_<g>`"*, and it is enforced nowhere. Adding a runtime check for it was the
tempting second half of this fire — and the live census (§below) is exactly why it does **not**
belong in it: `privacy-base`'s three `identityErasureComplete` residue gaps are `directOp`
(external-classed), declare a **constant-false** `inflight_<g>`, and carry no cap. A new
`error`/`warning` that fired on the MUST would light up on a shipped package the moment it landed —
the identical failure mode this fire exists to retire (a Health issue blaming a package before its
shape is resolved). The MUST's enforcement is genuinely entangled with whether that `privacy-base`
declaration is even correct, which is a package/compliance question, not a Weaver diagnostic. It is
filed as one consolidated discovery (§7), and this fire instead **corrects the false in-code claim**
that surfaced it.

**Census (re-run live at `79b1232`).** Every gap declaring a Weaver-visible `inflight_<g>`:
`lease-signing` `bgcheck`/`payment` (external, both capped at 3 — `retry_budget.go`),
`onboarding`/`signature` (human, no cap required, the item's subject), and `privacy-base`'s three
`identityErasureComplete` residue gaps (`directOp`, constant `false`, no cap). `lease-signing`'s
`inflight_docGen` is **not** a companion of any gap — the prefix-swap convention would name it
`inflight_leaseDoc`, which no column uses; it is an FE-facing column the gap formula consumes
(`lenses.go:631-635`), and this census corrects a scout that listed it as a fourth companion. The
`privacy-base` trio is the counterexample to `reconciler.go:546`'s standing claim (§7).

## 5. Increment order + green checks

1. **`staleMark`: the model, and the false error.** Rewrite the function's stated contract to §3; drop
   the `InflightActionMismatch` raise (keep the Debug log for the transient/unindexed case); delete
   `issueKeyInflightMismatch`, `issuePrefixInflightMismatch`, and the `issueKeyTargetPrefixes` entry.
   Rewrite `TestStaleMark_ExternalDispatchClassifier`, `TestStaleMark_MismatchAlertSelfHeals`, and
   `TestSweep_InflightActionMismatchIgnoredForUserTaskGap` to assert the surviving guarantee — marker
   ignored, `claimId` preserved verbatim, **and no issue raised** — each raise/clear deletion proven by
   reverting that line and watching the (now-inverted) assertion.
   Green: `go test ./internal/weaver/... -count=1`.
2. **The false in-code claim, corrected.** `reconciler.go:538-550`'s comment asserts *"No shipped
   package does this"* of an uncapped external `inflight_<g>`; `privacy-base` does exactly this. Correct
   the comment to name the live instance and point at the filed row (§7) — the falsified-claim-amended-
   in-place rule applied to a code comment.
   Green: `go build ./...`.
3. **Docs in lockstep.** `docs/observability/health-kv-schema.md:908` drops the retired
   `InflightActionMismatch` row (the class is emission surface — the schema doc moves in the same
   change); `docs/components/weaver.md` dossier gains this fire's class; `async-reply-design.md` §5's
   "keep the `InflightActionMismatch` alert for the case it was written for" is falsified — amend where
   it stands.
   Green: `STRICT=1 go run ./scripts/lint-conventions.go`, `go run scripts/lint-board.go`.

Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./... -p 4` with
`POSTGRES_TEST_DSN` set (`agents/steward/REMOTE.md` §3 — without it the gated tests skip and the suite
is falsely green).

## 6. The contract proposal (L3 — prepare + flag, main does not depend on it)

§10.3's MUST reads **unqualified** — *"A gap that declares `inflight_<g>` MUST declare
`maxretries_<g>`"* — while its own justification ("the fresh-`claimId` path has no collapse to pace it,
so the budget is its only bound") holds only for the external class. Read literally it condemns
`lease-signing`'s two human gaps, whose `retry_budget.go` documents at length why a cap there is the
*bug* that was removed. The engine has always read it in the scoped sense; the text has not said so.

Prepared as a branch commit per `agents/steward/REMOTE.md` §2 —
**`claude/contract-10-inflight-suppression-scope`**, branch-vs-main diff IS the proposal, ratify =
merge. It scopes the MUST to the external class and states §3's suppression-only meaning for every
other gap. **Nothing in §5 depends on it:** this fire only *retires* a raise; it adds no enforcement of
the MUST under either reading.

## 7. Neighbors + discoveries

**Closed / corrected:**
- `verticals.md` "Weaver ignores lease-signing's `inflight_onboarding`/`inflight_signature`
  suppression" — its premise is **false and is corrected, not carried**: suppression is honored by
  `gapSuppressed` on both legs regardless of class (`TestHandleRow_InflightSuppressesDispatch`,
  `TestSweep_InflightGapNotReclaimed`). `staleMark` governs only the reclaim's `claimId` choice and
  never gated suppression.
- `verticals.md` "Weaver's dispatch loop redispatches 5 gap/action pairs … `LensEffectMismatch`" — the
  suppression companion is the package's own stated fix for exactly this. Statically the mechanism is
  sound; whether the standing alert clears is a **live** observation this container cannot make, so the
  row is re-pointed at that observation rather than closed blind.

**Filed (one consolidated discovery, one out).** §10.3's MUST has no enforcement, and `privacy-base`'s
three erasure residue gaps ship the exact uncapped-external `inflight_<g>` pattern it forbids — falsifying
`reconciler.go`'s "no shipped package does this". The two are one root: **an `inflight_<g>` /
`maxretries_<g>` companion-pair validator, and the `privacy-base` declaration it must first be reconciled
against.** The consequence, traced and confirmed by the cold review: because `gapSuppressed` declines
the engine's `defaultDirectOpRetryBudget` for any row that *declares* `inflight_<g>`
(`evaluator.go:1027`), a **constant-false** marker suppresses nothing while switching OFF the
`directOp` safety net — so those three erasure gaps have no cap, and `escalateExhaustedGap` can never
fire for them: §10.8's "budget exhaustion... never a silent park" promise is unreachable on the erasure
residue sweeps, which re-dispatch indefinitely (backoff-paced by `uncappedExternal`, never escalated).
That is almost certainly not what the package intended. Whether the fix is a cap (escalate a stuck
erasure after N), dropping the inert marker (letting the `directOp` default budget apply), or a
deliberate retry-forever is a package/compliance call — the erasure path's fail-loud-vs-keep-trying
tradeoff — and the validator's shape depends on it, which is why this fire does not decide it
unattended. Install-time gap-spec
validation has a precedent in `internal/pkgmgr`, so the mechanism is a steward `📋`, not a designer pass —
but the `privacy-base` decision is its prerequisite. Filed as one row on `lattice.md`.

---

## 8. Fire brief (build note, 2026-08-24)

**1. Scope sentence.** Make the engine's diagnosis of `inflight_<g>` match the one contract it already
obeys: retire the `error` (`InflightActionMismatch`) it raises against a contract-legal declaration on a
non-external gap, and correct the falsified in-code claim that surfaced the adjacent debt. Green bar:
§5's three increments, then the full gates with Postgres up.

**2. Verified touch-list** (`file:line` checked live at `79b1232`).

| Site | What |
|---|---|
| `internal/weaver/evaluator.go:333-362` | `staleMark` doc block — the stated model is what is wrong; rewrite to §3 |
| `internal/weaver/evaluator.go:371-384` | the classifier arms: drop the `error` raise + its clear; keep the transient Debug log; `staleMark` still returns `!external || markerFalse` verdict |
| `internal/weaver/evaluator.go:1224-1255` | prefix consts + `issueKeyTargetPrefixes` — remove the `inflightMismatch:` family |
| `internal/weaver/evaluator.go:1295-1296` | `issueKeyInflightMismatch` — delete |
| `internal/weaver/reconciler.go:538-550` | the "No shipped package does this" claim — false; correct + name `privacy-base` + filed row |
| `internal/weaver/evaluator_internal_test.go:554,618` | classifier + self-heal tests → assert no issue raised, `claimId` preserved |
| `internal/weaver/reconciler_internal_test.go:755,795` | sweep tests → assert no issue raised, `claimId` preserved |
| `docs/observability/health-kv-schema.md:908` | drop the retired class row — lockstep with the emission |
| `docs/components/weaver.md` (dossier) | the class this fire mints |
| `async-reply-design.md` §5 | "keep the `InflightActionMismatch` alert" — falsified, amend in place |

**3. Precedents to mirror.** The raise/clear deletion is the inverse of the pair being removed
(`evaluator.go:378-383`) — the *shape* was right, its *subject* was wrong, so the surviving classifier
keeps its structure minus the raise. The falsified-claim-in-place correction mirrors the design-doc-body
rule (`agents/steward/SKILL.md` §4) applied to a code comment. Test rewrites keep `seedPatternSpec`
(dossier: a hand-seeded `patternMeta` pins the fallback, not the name).

**4. Increment order + runnable green checks** — §5 above.

**5. In-scope gotchas.**
- **Health-emission lockstep** (`agents/steward/SKILL.md` §4): the schema doc changes in the same commit
  as the emission deletion.
- Dossier entries this fire trips, verbatim from `docs/components/weaver.md`:
  - *A gap class is decided by the dispatch's SHAPE, never by its action name* — the classifier this fire
    keeps; do not reintroduce an action-name test.
  - *An `error`-severity Health issue must not fire on a self-healing condition* — this fire's subject is
    the sharper case: an `error` firing on **no condition at all**.
  - *A Health issue key is a LATCH: scope it to the fact it states, and split it only with every clear
    re-paired* — the retired family leaves through **both** its clear site and `issueKeyTargetPrefixes`;
    prove no live path still expects it.
  - *A target leaves the registry by more routes than the teardown verb* — the retired prefix leaves
    `issueKeyTargetPrefixes`; confirm nothing else keys on it.
  - *Prove each changed line by reverting THAT LINE, not the feature* — each deleted raise/clear gets its
    own revert proof against the inverted assertion.
  - *A test that hand-seeds an engine's internal registry map pins the FALLBACK, not its name* — the sweep
    tests being rewritten seed `patternMeta`; keep `seedPatternSpec`.
- Standing checklist: **every census is a premise** — this brief's own census already overturned a
  scout's table (`inflight_docGen` is not a companion; `privacy-base` *is* the uncapped-external instance
  the reconciler comment denies) — re-run before relying on any count.

**6. Adjacent finds.** The §10.3 MUST-enforcement + the `privacy-base` counterexample — **filed as one
consolidated row** (§7), not built here, because the enforcement's shape depends on a `privacy-base`
package/compliance decision this Weaver fire should not swallow. The falsified reconciler comment that
names it is corrected *in this fire* (Increment 2). No other out-of-scope finds.

**7. Non-goals.** `gapSuppressed` (correct as shipped — not touched). The external-vs-human classifier
itself (shipped, correct — only its *alert* is removed). Any package edit, `privacy-base` included
(filed). A new runtime enforcement of §10.3's MUST (filed — would fire on `privacy-base` today). The
`LensEffectMismatch` row's live confirmation, which needs a running stack this container lacks.
</content>

---

## 9. Shipped (build note, 2026-08-24, `3a35bde`)

Built as designed, three increments, no deviations from §5. `staleMark`'s verdict and `gapSuppressed`'s
two-leg suppression are byte-for-byte unchanged; only the raise, its key helper
(`issueKeyInflightMismatch`), its prefix constant, and its `issueKeyTargetPrefixes` entry are gone. The
removed `e.issues.clear` on the external arm was dead the moment the raise went — no writer remains for
that family, and `issueCache` is process-local, so a pre-deploy entry vanishes on restart.

**Proofs.** Each deletion revert-proven: re-adding a raise in the non-external branch reds five subtests
of `TestStaleMark_ExternalDispatchClassifier` plus `TestStaleMark_ClassifierFollowsRegistryReplay`, so the
new no-issue assertions pin the deletion rather than passing vacuously. `TestSweep_InflightMarkerPreservesClaimIdForUserTaskGap`
holds the load-bearing guarantee (`claimId` verbatim) beside its mirror
`TestSweep_ExternalTaskOnlyPatternReclaimsWithFreshClaimId` (external path still mints fresh), so the
claimId behavior is pinned on both sides, not just the absent alert. Gates: `go build ./...`, `make vet`,
`golangci-lint`, `go test ./internal/weaver/...`, `make test-lease-convergence` (all three
`TestAsyncConvergence_*`), `lint-conventions`, `lint-board`.

**Cold adversarial review — clean.** Six attack lines, all SAFE: the verdict is unchanged for every input
class on both legs; `gapSuppressed` is independent and class-agnostic (`evaluator.go:1022`, reached at
`evaluator.go:142` / `reconciler.go:488` *before* `staleMark`); no dangling reference to the deleted
symbols in non-test code; the shared-cache assertions are stricter, not weaker; the `privacy-base`
trace confirmed (directOp ⇒ external, `hasUsableRetryCap` false ⇒ `uncappedExternal`, pacing path) with
**no runtime change** — those gaps take the external arm, where only the dead clear was removed.

**Review classification (the item's whole diff).** One class, design-gap: *a Health issue whose subject is
a contract-legal declaration*. Routed to `docs/components/weaver.md`'s dossier (12th entry). Not yet a
second sighting, so no lint gate is minted — the check named in the entry is the reviewer's question.

**MERGED ≠ RUNNING.** This container has no live stack (`agents/steward/REMOTE.md` §3), so the standing
`InflightActionMismatch` entries on Andrew's Weaver drain when that binary is rebuilt from `main` and
cycled — `bin/weaver` and `bin/lattice` both link `internal/weaver`. Until then the fix is merged and
CI-green but not observed live; the `verticals.md` `LensEffectMismatch` row's confirmation waits on the
same cycle.
