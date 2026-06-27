# Agentic Operating Model — Swim-lane execution

> **Status:** ✅ **Winston-ratified — build-ready** (direction set by Andrew, 2026-06-26: *two* parallel
> streams). Evolves `agentic-ops-design.md` §5/§6.1.1. No frozen contract / no platform fork — operating
> machinery (skills + scheduled tasks + board layout), fully revertible.

## 1. Problems this fixes

1. **Blind on the credit/session budget.** A scheduled fire has **no way to query remaining tokens** (no
   usage tool; `/context` is interactive-only). So a single greedy Steward *guesses* when to stop — and a
   safe guess stops early → **a lot of unused tokens left on the table**.
2. **One advancer + only one lane hydrated → Lattice starves.** The Vertical PO keeps the **Verticals** lane
   full of ready demand, so the single Steward always finds vertical work and ships it. **Lattice features**
   and **component maintenance** have **no standing hydrator** (only the Steward's last-resort Inquiry, which
   the vertical queue crowds out) → they run dry, nothing is "ready", and **Lattice improvements stall** while
   the PO-fed verticals keep advancing.

## 2. Principles

- **Don't measure the budget — make every fire safe to end.** Each fire does a **bounded batch** (a few small
  items, or one increment of a big one), commits each unit green, and **exits**. It never tries to "drain the
  queue" or "use up the budget." Throughput = **cadence × parallel streams**; **the rate-limiter is the
  governor** — run dense, and when the window trips, fires fail cheaply and resume after reset. Push until the
  limiter says no (nothing on the table); never lose work (bounded + committed units).
- **Two parallel streams, split along the no-collision seam.** App-vertical work (packages + FE) and Lattice
  platform work touch **disjoint code areas**, so they run **concurrently** without colliding. Lattice work
  stays **serial within itself** (features + maintenance both live in `internal/*` — splitting them would
  collide), advancing by **round-robin across components**.
- **Demand is hydrated per lane — and the PO drives Lattice demand too.** A vertical never papers over a
  missing Lattice feature (see §4).

## 3. Streams & roles

| Stream | Code area (disjoint) | Advancer | Hydrator(s) |
|---|---|---|---|
| **1 — App Verticals** | `packages/<vertical>-*` (loftspace-domain, lease-signing, clinic-*), `cmd/<vertical>-app`, vertical lenses | **Vertical Steward** (`steward-verticals`) | **Vertical PO** (`vertical-po-discovery`) |
| **2 — Lattice** (features + component maintenance) | `internal/*` (processor, weaver, loom, refractor, substrate, bootstrap), `cmd/loupe`, core/base packages | **Lattice Steward** (`steward-autonomous`, repurposed) — **round-robin across components** | **Surveyor** (`platform-surveyor`, new) **+ PO-routed platform gaps** |

- Stream 1 is **mostly package + FE**: the PO defines the capability → Sally (UX) → FE Engineer builds → package/lens work via the owner pattern.
- Stream 2 **round-robins across components** (Core / Weaver / Loom / Refractor / Loupe + cross-cutting
  features) by component freshness × importance×readiness — the existing component-coverage rotation, now the
  Lattice stream's core selection rule. "Features" and "maintenance" are two *kinds* of item within the lane,
  not separate rotation axes.
- **Loupe is a Lattice-stream component** (`cmd/loupe` — disjoint from the vertical apps' `cmd/*-app`, so no
  cross-stream collision): it is the operator/inspector and the **P5 Core-KV-reading exception**, i.e.
  *platform* infra, not a product vertical. The Lattice Steward advances it like any component — the **owner**
  skill for backend / handlers / read-model / observability (`cmd/loupe/*.go`), and **UX-then-FE** (Sally → FE
  Engineer + in-browser verify) for operator-surface FE (`cmd/loupe/web`). **UX-then-FE is the FE *mechanism***,
  not a Verticals-only thing: the **FE Engineer serves both** Loupe and the vertical apps; the difference is
  only which steward invokes it.
- **Reliability/observability is not a lane** — it's a pre-emption check **both** advancers run first (red
  CI/gate/Health pre-empts that stream's normal pick), fed by Lamplighter + CI.

## 4. The no-paper-over rule (Andrew, 2026-06-26)

**The app-vertical stream must never substitute a workaround for a missing Lattice capability and call the
item done.** First, know what is *not* a Lattice gap: a missing **lens / read-model (DDL)** is **package work**
the vertical stream builds itself (Stream 1 is mostly package + FE) — add the lens to the owning package, never
read Core KV. A real **Lattice gap** is a missing **platform primitive** the package cannot provide: a cypher
**engine** capability (e.g. a new aggregator), a missing **op / kernel** mechanism, a **substrate** feature, an
**orchestration** capability (e.g. `@every`). When vertical work hits one of *those*:

1. **File it as Lattice-lane demand** in `lattice.md` (tagged with the requesting vertical + why), so the
   Surveyor/Lattice Steward picks it up as a real feature.
2. **Block or defer the vertical item on it** — or ship only the part that doesn't need the missing capability
   — and mark the vertical item `🚧 blocked-on: <lattice item>`.
3. **Never** hack the capability into the package/FE to fake completeness. A vertical builds **on** real
   Lattice capabilities; faking one hides the platform gap and stalls Lattice further.

The **Vertical PO** is the primary driver of this demand: exercising the app surfaces what the platform
actually lacks (grounded, not speculative). (Boundary vs **P5**: "no lens projects this field" is *not* a
Lattice gap — adding the lens is **package work** the vertical does itself; only a missing platform *primitive*
routes to the Lattice lane.)

## 5. Backlog tracking — per-lane files

`_bmad-output/planning-artifacts/backlog/`:

- **`README.md`** — index + scales legend + the cross-lane rules (§4, prioritization) + the **Done/shipped
  history** (the prior Progress board, retained). Light, rarely written.
- **`verticals.md`** — Stream 1: ready / in-flight / done-log + the PO discovery notes. Written by the
  Vertical Steward + the PO only.
- **`lattice.md`** — Stream 2: **Lattice features** (cross-cutting platform capabilities — the deferred
  backlog) + **Component maintenance** (grouped by component) + ready / in-flight / done-log. Written by the
  Lattice Steward + the Surveyor only (+ PO-routed gaps).

Each advancer + its hydrator write **only their lane file** → the single-file board-collision git races
disappear. Item status vocabulary: **📋 ready · 🏗️ in-flight (worktree) · ✅ done (commit) · 📐
design-proposed · 🚧 blocked (Andrew-gated, or blocked-on another item)**. A lane is **starving** when its
ready-count → 0 (its hydrator runs). *North-star (deferred): model the backlog in the graph so Loupe shows it
self-truthing — big lift; per-lane markdown now.*

## 6. Fire shape (budget-blind, bounded)

Each advancer fire:
1. **Sense** its lane file + signals (Lamplighter/CI; for Lattice, component freshness).
2. **Pre-empt** on reliability/observability red.
3. **Select** — Verticals: top importance×readiness ready item; Lattice: round-robin component × importance×readiness. Resume any in-flight (🏗️) item first.
4. **Advance** a **bounded batch** — several XS/S/M, or one increment of a big (L+) item — each its own green
   commit; **then exit.** No "drain", no "budget" guessing.
5. **Multi-fire** for big items: persistent worktree + a 🏗️ CHECKPOINT; merge only when complete + green.

Cadence: both advancers fire densely + staggered; the two hydrators on their own cadence. Tune **up** until the
limiter occasionally trips — that trip is the signal the window is fully used.

## 7. Isolation (parallel-safe)

**Roles are skills, followed inline — not spawned.** An advancer **follows** the owner / fe-engineer /
lamplighter **playbook inline as Winston** (Skill tool, or read `agents/<role>/SKILL.md`); it does **not**
Agent-spawn them (only generic agent types are registered — a spawn fails, and a cold agent just re-derives
what the advancer already has loaded). The advancer *is* Winston: it builds and admits; there is no separate
hand-up.

**Code in a worktree, docs in `main`.** **CODE** runs in an isolated **git worktree** the fire creates (commit
+ push to `main`, no PR) — **not the main checkout**: `go build ./...` / `golangci-lint` / `go test` in a
*shared* checkout would compile the other stream's uncommitted in-progress code and fail spuriously.
**Documents — the backlog / lane files, design docs, and contracts — are edited directly in `main`** (never a
worktree); **contract** edits stay **uncommitted** for Andrew. Always: **scoped `git add <paths>`** (never
`-A` / `commit -a`), **`git pull --rebase`** before push. The two streams touch **disjoint code** (worktrees)
and **disjoint lane files** (main), so concurrent commits rebase cleanly almost always.

**Shared core stack vs. your own app binary.** `make down` (the CORE STACK — NATS + processor / refractor /
weaver / loom / bridge / objmgr / Loupe) is forbidden if you didn't start it (shared by every fire + Andrew).
**But the single binary you just rebuilt — `bin/<vertical>-app` (:7788 / :7799) or `bin/loupe` — is yours to
cycle** to serve + verify new (`go:embed`'d) assets: reuse the running core stack, `pkill` the stale binary,
rebuild, relaunch it in the **background**, verify in-browser, leave it running. (A changed lens / DDL won't
hot-reload under a live stack — the **F-004** gap — so verify those via tests + the ephemeral-stack e2e
targets.) A *stale running binary serves the OLD assets* — restarting your own binary is how you verify, and is
**not** `make down`.

## 8. Rollout

- **Phase 1.5 (now):** 2 advancers (Vertical + Lattice) · Surveyor hydrator · per-lane board · PO routes
  Lattice demand · the no-paper-over rule · bounded-batch fires.
- **Phase 2 (later, only if Lattice throughput lags):** split the Lattice stream into parallel
  feature/maintenance advancers — **deferred**, because they'd collide on `internal/*`.

## 9. The fleet (scheduled tasks)

| Task | Role | Cadence (initial) |
|---|---|---|
| `steward-autonomous` *(repurposed)* | **Lattice Steward** (advance Stream 2) | dense (~hourly) |
| `steward-verticals` *(new)* | **Vertical Steward** (advance Stream 1) | dense (~hourly), offset from Lattice |
| `platform-surveyor` *(new)* | **Surveyor** (hydrate `lattice.md`) | a few ×/day |
| `vertical-po-discovery` *(updated)* | **Vertical PO** (hydrate `verticals.md` + route Lattice gaps) | a few ×/day |
