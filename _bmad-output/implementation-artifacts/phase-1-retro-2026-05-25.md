# Phase 1 Retrospective — Lattice

**Date:** 2026-05-25
**Scope:** Epics 1–6 (Lattice Core, Refractor, Bootstrap/Kernel, Capability Packages, AI-Agent contract, CLI/Hello-Lattice)
**Facilitator:** Bob (Scrum Master)
**Participant:** Andrew (Project Lead)
**Format:** Phase-level (first retrospective; per-epic retros were skipped)

---

## Phase Summary

**Delivered:**
- **Epic 1 (Lattice Core):** Processor 6-step commit pipeline, Core KV bucket layout, idempotency tracker, hydration, JetStream consumer
- **Epic 2 (Refractor):** Lens projection engine, Cypher rule engine, Postgres + KV adapters, capability lens
- **Epic 3 (Capability / RBAC):** Capability lens with auth-freshness checks; rbac-domain capability package
- **Epic 4 (Identity + Kernel formalization):** identity-domain + identity-hygiene packages; **kernel meta-DDL (Story 4.7)** — formalized mid-phase
- **Epic 5 (AI-agent contract):** DDL self-description aspects (5.1), cold-start AI traversal (5.2), compensation aspect (5.3)
- **Epic 6 (CLI + Tutorial):** `lattice` CLI (6.1), Health KV schema docs (6.2), deployment isolation docs (6.3), hello-lattice reference impl (6.4)

**Gates:**
- Gate 2 (bypass) ✅
- Gate 3 (capability adversarial) ✅
- Gate 4 (rollback) ✅
- **Gate 5 (hello-lattice) — partial:** M1-M3 pass; M4-M6 skipped with documented Phase-1.5/2 architectural gaps

**Deviations:** 14 documented in MORPH-DEVIATIONS.md. Largest: capability package formalization mid-phase.

---

## What Went Well

### 1. Architecture held up (Andrew's top win)
The nine core decisions from brainstorming (CDC-not-events, KV data model, Capability=Lens, Health-KV plane, DDL location, Loom/Weaver layering, parser strategy, single-cell MVP, Materializer=Refractor) survived contact with code. No fundamental re-architecting required across six epics. Mid-phase additions (kernel meta-DDL, capability packages, compensation aspect) layered cleanly without invalidating prior decisions.

### 2. Capability packages crystallized domain extensibility (Andrew's co-top win)
What started as "RBAC will be built in" became "RBAC is a package, identity is a package, hygiene is a package, and packages install via a defined contract." This is a Phase-2 enabler — Loom and Weaver will consume packages the same way. Mid-phase formalization was the right call even though it cost late-phase consistency.

### 3. Sub-agent (Sonnet) pattern earned its keep
Introduced Epic 5, used heavily through Epic 6. Outer Opus context stayed manageable through epic-scale work. Pattern works because the briefs were self-contained and the produced code was reviewable.

### 4. `bmad-code-review` was high-signal where it ran
On Epics 5 and 6, CR surfaced real bugs and contract gaps every single time (parseMutations expectedRevision, meta.lens contract mismatch, book vertex `key` field, seedCapDocForAgent escape hatch). Strong evidence that running it on Epics 1-4 would have caught comparable issues earlier.

### 5. Gate tests as forcing function
Each phase gate (2/3/4/5) caught architectural gaps that unit tests didn't. Gate 5 specifically surfaced three Phase-1.5/2 latent bugs that would have ambushed Phase 2 (Loom/Weaver) silently.

---

## What Didn't Go Well

### 1. Skipped code review on Epics 1-4 (Andrew's top miss)
The most load-bearing code in the system — Processor commit path, Refractor, Core KV contracts, bootstrap — never received the adversarial CR pass. By the time CR became standard practice (Epic 5), Epics 1-4 had already shipped. This is the single biggest known risk going into Phase 2.

### 2. Substrate-direct package writes bypass the Processor (Andrew's co-top miss)
`lattice-pkg` writes DDL meta-vertices directly to Core KV instead of going through the Processor commit path. This violates the implicit invariant "every kernel state change goes through the Processor." Discovered late via M5/M6 because Processor DDL cache doesn't see substrate-direct installs and isn't invalidated on tombstone. **This is a contract bug, not just a cache bug.**

### 3. CI silently broken from Story 6.1 → 7 fix-up commits
"Story shipped" was treated as "PR merged + local test passed" — not "main CI green." CI degradation accumulated unobserved across four stories. When discovered, the failure stack hid earlier failures behind later ones. Cost: an unbudgeted debugging cycle plus loss of confidence in CI as a gate.

### 4. Documentation sprawl in `_bmad-output/`
All planning + implementation artifacts piled into one folder. By end of Phase 1 it was hard for both me (Winston) and Andrew to know which doc was authoritative for which contract. Late-phase migration to `/docs` started right but is incomplete; `data-contracts.md` is monolithic and needs sharding.

### 5. Token budgets tracked but not enforced
The per-story token tracker exists but no story hit a "halt at budget" trigger. Sessions ran to natural limits instead. Result: harder to predict cost ceilings for Phase 2 sub-agent fan-out.

### 6. Hello Lattice deferred its critical milestones
M4 (Refractor Postgres schema mgmt), M5 (DDL cache invalidation on substrate-direct install), M6 (DDL cache eviction on tombstone) all surfaced real architectural gaps, not test bugs. Gate 5 ships partial.

---

## Bug Hotspots Identified (Andrew's assessment)

CR sweep should bias toward:

1. **Refractor lens projection** — projection lag bounds, capability lens auth freshness reprojection (known gap), adapter contracts (especially Postgres adapter schema lifecycle), error recovery paths
2. **Bootstrap / kernel meta-DDL** — Story 4.7 was a late addition; meta lane, cache management, interactions between kernel state and capability packages

Lower bias on Processor commit path and Core KV bucket contracts — but still need a pass; they ship the most critical contracts in the system.

---

## Action Items for Phase 1.5

**Scope decision:** **CR + contracts hardening + docs migration** (Andrew's selection)

### A. Code review sweep (highest priority)
- **A1.** CR Epic 1 (Lattice Core: Processor, Core KV, idempotency, hydration)
- **A2.** CR Epic 2 (Refractor: projection engine, adapters, capability lens) — **bias hotspot**
- **A3.** CR Epic 3 (Capability lens, RBAC)
- **A4.** CR Epic 4 (Identity, kernel meta-DDL Story 4.7) — **bias hotspot**
- Each CR launched as a parallel sub-agent (Sonnet), Winston adjudicates findings, assigns fixes to sub-agents, commits when good.

### B. Known architectural gaps (Gate 5 surfaced)
- **B1.** Postgres adapter schema management — lens-spec-driven CREATE TABLE or migration hook
- **B2.** Processor DDL cache invalidation on substrate-direct package installs (or: route packages through Processor write path — preferred long-term)
- **B3.** Processor DDL cache eviction on TombstoneMetaVertex commit
- **B4.** Capability lens heartbeat reprojection (or projected-at refresh) to prevent AuthFreshnessExceeded for unchanged docs

### C. Contracts hardening
- **C1.** Lock OperationEnvelope, OperationReply, ContextHint shapes — write contract tests
- **C2.** Lock Core KV bucket layouts and key shapes per bucket — codify in /docs
- **C3.** Lock capability package install contract — substrate-direct write surface area (or eliminate it per B2)
- **C4.** Lock DDL self-description aspect set (Story 5.1 + 5.3 `.compensation`)

### D. Docs migration to `/docs`
- **D1.** Shard `data-contracts.md` by contract type (envelope, KV, capability, DDL, hint, reply) into `/docs/contracts/`
- **D2.** Migrate `lattice-architecture.md` to `/docs/architecture/` (sharded by component)
- **D3.** Migrate `prd.md` to `/docs/prd/` (sharded)
- **D4.** Generate `/docs/index.md` via `bmad-index-docs` for AI navigability
- **D5.** Produce LLM-optimized distillates of architecture + PRD via `bmad-distillator` for Winston's working memory
- **D6.** Keep `_bmad-output/` for workflow artifacts only (stories, retros, reports, briefs)

---

## Process Changes (committed by Andrew)

### P1. Winston-orchestrated story cycle, sub-agents don't commit
- Winston runs **CS → DS → CR** by spawning sub-agents
- Sub-agents produce artifacts and propose changes but **do not commit**
- Winston adjudicates CR notes
- Winston assigns fixes to sub-agents (DS round 2)
- Winston commits when everything is good
- Winston watches CI (potentially while spawning the next story's CS)

**Why this matters:** Keeps quality gate authority and commit authority in one place. CR can't be skipped. CI watch is part of the cycle, not an afterthought. Sub-agents stay focused on production.

### P2. New docs land in `/docs` by default
- Workflow artifacts (stories, retros, sprint plans, code-review reports) stay in `_bmad-output/`
- Everything else — contracts, architecture, ops guides, schema docs, tutorials — lands in `/docs`

---

## Significant Discoveries (require Phase 2 plan update)

🚨 **Substrate-direct package install contract is at odds with "Processor is the only write path"** — Phase 2 (Loom, Weaver) needs to decide: do they install via the Processor commit path, or do they also write substrate-direct? Either answer is workable, but the choice must be made before Loom design solidifies. Suggest deciding during Phase 1.5 contract hardening (item C3).

---

## Readiness for Phase 1.5

| Dimension | Status |
|---|---|
| Story completeness (Epics 1-6) | ✅ Formally done |
| CI on main | ✅ Green as of `e91af77` |
| Architectural docs accuracy | ⚠️ Needs migration + sharding; capability package contract under-documented |
| Code review coverage | ❌ Epics 1-4 unreviewed |
| Gate 5 | ⚠️ Partial (M4-M6 deferred) |
| Token tracker | ✅ Up to date |

**Verdict:** Ready to kick off Phase 1.5. Sequence:
1. CR sub-agents on Epics 1-4 (parallel)
2. Adjudicate + spawn fix sub-agents
3. Contracts hardening + Gate 5 gap fixes interleaved
4. Docs migration in parallel
5. `bmad-correct-course` to consolidate findings → Phase 1.5 epic + story plan
6. `bmad-check-implementation-readiness` before Phase 1.5 sprint
7. `bmad-edit-prd` + `bmad-create-epics-and-stories` for Phase 2

---

## Key Lessons (for the record)

1. **Code review isn't optional under token pressure.** It's where load-bearing bugs surface. Skipping it on Epics 1-4 created the single biggest risk going into Phase 2.
2. **CI green is part of "done."** Story is not shipped until main CI is green. Watch on every push.
3. **"Every state change goes through the Processor" is an invariant worth defending.** Substrate-direct shortcuts (lattice-pkg) seem expedient and cost later via cache-management debt.
4. **Gates are oracles.** When a gate test deferred milestones, the deferred items were real architectural gaps, not test setup issues. Trust the gate.
5. **Sub-agent + adjudication beats sub-agent + auto-commit.** Keep quality and commit authority in the orchestrator.

---

*Retrospective complete. Next workflow: parallel CR sub-agents on Epics 1-4, then `bmad-correct-course` to consolidate findings into Phase 1.5 plan.*
