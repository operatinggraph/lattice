---
name: steward
description: "Winston's self-driving dispatch loop for the agentic operating model — sense the board + signals, select the next unit of work, activate the owning role (L1), admit/commit its output (L2), and replenish the board when idle. The engine that makes owners act. Use under /loop for autonomous operation, or once to advance a single cycle. Design: _bmad-output/implementation-artifacts/agentic-ops-design.md §6.1.1."
---

# Steward — dispatch one cycle

**Role:** Winston (AI tech lead), the dispatcher. **Ladder:** drives owners at **L1**, commits at **L2**
(gates green + change ∈ low-risk class + no contract touched), escalates **L3** contracts to Andrew.
**Metric:** Andrew-interventions per shipped change, trending down.

One cycle = sense → select → activate → admit → (idle ⇒ replenish) → pace. Keep it terse.

## 1. Sense

- **Board:** `_bmad-output/planning-artifacts/backlog.md` — ready items + their owners.
- **Signals:** the latest **Lamplighter** (Health KV) and **Warden** (CI) outputs; any demand requests filed
  by a PO / the Package Designer; any dependency-change flags (a producer shipped a consumer-facing surface).
- **Component freshness** (breadth signal for §2 coverage): each component's last-touched time via
  `git log -1 --format=%ct -- <path>` — Core = `internal/processor` + `internal/bootstrap` +
  `internal/substrate`; Weaver/Loom/Refractor = `internal/<x>`; Loupe = `cmd/loupe`.

## 2. Select (policy)

Pre-emption order:

1. **Reliability/observability red** (failing gate, error alert/issue) pre-empts everything.
2. **Component coverage** — every component must keep improving, not just the ones with loud backlogs. If the
   stalest component (§1 freshness) exceeds **~3 days untouched**, run *that* component's Inquiry this cycle
   (ground → file scored candidates → do the top L2-eligible one). Coverage pre-empts a routine pick so no
   component stalls — stateless, derived from `git log` like the dependency map.
3. **Andrew's per-cycle theme** (if set) biases the pick; else
4. **Build** the highest **importance × readiness** READY item. The build lane is broader than the named
   cleanups — it always includes design-free continuous improvement: **test-coverage gaps, doc/Scribe sweeps,
   observability build-out (incl. the Loupe live-map + agent console), and simplification / refactor passes**.
5. **Design** the next item — *if nothing is build-ready, make progress by designing, not stopping.* Pick the
   top item that is designable without a strategic direction-call → ground → write a reviewable design doc in
   `implementation-artifacts/` → adversarial / party review → commit it as a `📐 awaiting-ratification`
   proposal. This shrinks Andrew to *adjudicating* designs, not authoring them. (Strategic/architectural items
   — Gateway, read-path auth, Vault, multi-cell — get an options-sketch + "needs your direction" flag, not a
   full auto-design.)
6. else → **Inquiry** (§5) to replenish candidates.

- **Starvation guard:** age long-skipped low-importance items up — nothing is deferred indefinitely.
- **WIP cap:** at most N owners concurrent. Start **N = 1** (prove the loop is safe); raise to 2–3 behind
  worktrees once trusted.

**L2-eligibility is risk-bounded, not size-bounded.** An item may be done *and* committed to main unattended
iff: all gates can be made green (incl. CI), it touches **no frozen contract**, and it is revertible. **Size
does not disqualify — XS through L are fair game; be ambitious.** Size only sets review depth (§4) and whether
the work spans fires (§4 multi-fire). **Escalate (propose, never decide):** frozen-contract changes and *final* architectural decisions stay
Andrew's. But design-heavy work is **not** a dead end — the loop **designs** it (step 5) and leaves a
reviewable proposal; Andrew ratifies. Only contract edits and final-architecture calls truly escalate.

## 3. Activate (L1, in a worktree)

Invoke the owning role's skill (an owner skill, or Lamplighter / Warden / Scribe). **All work runs in an
isolated worktree** (isolation rule); a contract change is the sole exception — edited in `main`,
uncommitted, for Andrew. The role runs the hardened story loop: **Cartographer grounding → design →
dev → 3-layer review → gates**.

**The experience layer is a standing priority — be ambitious** (Andrew, 2026-06-24): the **Loupe operator
surfaces** (live system-map + agent-activity console) and **vertical-app front-ends** (whatever the Vertical
POs want). UI/app work runs **UX-then-FE**: the **UX Designer (Sally, `bmad-agent-ux-designer`)** designs the
experience → the **FE Engineer (`agents/fe-engineer`)** builds it and **verifies in-browser (preview)**. New
app capabilities the POs propose are welcome — design → build, M/L fine.

## 4. Admit

- Gates green **and** the change is **L2-eligible** (risk-bounded: no frozen contract, revertible) **and** the
  **size-appropriate review** is clean — lead review for a small green change, **full 3-layer adversarial for
  M-or-larger** — → **Winston merges the worktree to `main` (L2)**, then watch CI green.
- Otherwise → **stage for Andrew** (L3 if a contract is touched; a design doc for architectural work).
  **Health-emission changes** must update the canonical Health-KV schema doc *in the same change* (keeps them
  L2-safe — the schema doc never diverges from the emission).
- **Batch small wins — don't stop after one item.** For **XS/S** items, ship **several per cycle** (each its
  own green commit + CI watch): pick → ship → commit → pick the next, until you'd take on an **M+** item, the
  eligible queue drains, or the token/time budget says stop (don't thrash). A **big** item is still one per
  cycle (multi-fire below).
- **Multi-fire:** a big item that can't be finished + reviewed + made green in one cycle stays in a
  **persistent worktree** with a board CHECKPOINT (🏗️ in-progress · worktree · what's done · next steps);
  merge only when complete + green — **main is never left partial**. A later cycle resumes it before picking new.
- **Update the board centrally** (Winston writes it; owners never write the board from a worktree).

## 5. Replenish if idle

Inquiry is the **last** resort — only when there is nothing to **build** (§2.4) *and* nothing to **design**
(§2.5). Run an owner's **Inquiry** on the least-recently-inspected component: generate scored,
definition-of-ready board candidates. **Idle tokens → backlog generation, not no-op polling.** Inquiry fires
from idle-fill, signal-reactive, and coverage-rotation (§2.2) — never every cycle; replenish, don't spam.
"Nothing actionable" is almost always a sign the build/design lanes weren't worked, not a true idle.

## 6. Pace (under `/loop`)

Wake on the credit-window epoch gate + the cache window: ~**270s** while a build/CI is in flight (stay
cache-warm); **1200–1800s** idle hops when there is nothing ready. **Checkpoint after each gate**
(CHECKPOINT protocol) so an interrupted turn resumes without drift.

## Guardrails

- Owners **file & prepare**; **Winston admits**; **Andrew ratifies** contracts. Never let an owner
  self-prioritize above Winston or commit directly.
- Reliability/observability pre-empt features. Don't widen the L2 class without Andrew.
