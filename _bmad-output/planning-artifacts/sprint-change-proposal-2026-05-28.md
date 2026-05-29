# Sprint Change Proposal — Phase 1.5 Hardening

**Date:** 2026-05-28
**Author:** Winston (Architect) facilitating as Scrum Master
**Project Lead:** Andrew
**Scope classification:** **Moderate** (backlog reorganization — new Phase 1.5 block + 7 stories; no PRD pivot)

---

## Section 1 — Issue Summary

Phase 1 (Epics 1–6) shipped all planned stories, but two systemic gaps were identified post-completion:

1. **Code review coverage was incomplete.** The `bmad-code-review` adversarial pass ran only on Epics 5–6. Epics 1–4 — the load-bearing Lattice Core (Processor, Core KV/substrate, Refractor, Bootstrap/Kernel) and the capability-package framework — never received the pass.

2. **Gate 5 (Hello Lattice) shipped partial.** Milestones M4–M6 were deferred behind three real architectural gaps: Postgres adapter schema management (M4), DDL cache staleness vs. substrate-direct package installs (M5), and DDL cache eviction on tombstone (M6).

**Discovery:** Phase 1 retrospective (`phase-1-retro-2026-05-28.md`) + a full six-component code-review sweep run during Phase 1.5.

**Evidence:** Six CR reports under `_bmad-output/implementation-artifacts/phase-1.5-cr-*.md`. Aggregate raw findings: **4 P0, 19 P1, 31 P2**, plus ~190 history-comment cleanups. The inline-fixable subset (the large majority) is already shipped across commits `3cb5d1c`, `8552ca2`, `6ccbe2e`, `59e91cb`, `46f3353`, `64921cc`, `e522966` and follow-ups — all CI-green. Five findings too large for inline fix were carved into dedicated stories.

---

## Section 2 — Impact Analysis

### Roadmap impact
- **Epics 1–6:** unchanged in scope; all CR fixes already merged. No re-opening.
- **Phase 1.5 (hardening block):** introduced as a labeled interstitial between Phase 1 and Phase 2 — remediation of shipped work, not new feature capability. Carries the five carved stories + two consolidation stories (7 total, IDs 1.5.1–1.5.7).
- **Phase 2 (Loom, Weaver, reference app):** unblocked once Phase 1.5 completes and Gate 5 goes fully green.

### Architecture impact
- **Substrate-direct install pattern → eliminated.** Andrew's decision: capability-package installs route through the Processor (ops.meta lane). This upholds the "every kernel state change goes through the Processor" invariant and closes M5 at the root. This is the single largest architectural change of Phase 1.5 and the one most relevant to Loom's design.
- **Write-path contract surface hardens:** `AtomicBatch`/`PublishBatch` gain context; `AtomicBatch` returns per-key revisions; `OperationReply.Revisions` becomes real (unblocks the Story 5.3 compensation contract).
- **Kernel meta-DDL gains symmetry:** `UpdateMetaVertex` becomes a full-aspect update; `TombstoneMetaVertex` cascades to aspects + triggers cache eviction.
- **Capability auth freshness** becomes deterministic + heartbeat-reprojected.

### Artifact conflicts
- `epics.md`: add a **Phase 1.5 Hardening** block after Epic 6 (before the Phase 2+ deferred ledger). Several Phase-1.5 items were pre-noted in the existing "Phase 2+ Deferred Architectural Capabilities" ledger — move the now-in-scope ones (DDL cache, Refractor key-shape overlap) into the Phase 1.5 block.
- `prd.md`: **no change** — no requirement added/removed; this is hardening of shipped FRs.
- `data-contracts.md`: already sharded to `/docs/contracts/`; story 1.5.7 adds conformance tests that lock those shapes.
- No UX impact.

### Technical impact
- Touches Processor, Substrate, Bootstrap, Refractor, pkgmgr. No new external dependencies.
- Gate 5 marker flips from `partial` to `passed` at end of Phase 1.5.

---

## Section 3 — Recommended Approach

**Direct adjustment** — add a Phase 1.5 hardening block to the existing plan. No rollback, no MVP reduction. Rationale: Phase 1 functionality is sound; this is targeted hardening of contracts and closure of the three known gaps. The work is well-bounded by the CR reports.

---

## Section 4 — Detailed Change Proposals

### Phase 1.5 — Hardening Block

> **Goal:** Lattice Core (Processor, Core KV), Refractor, Bootstrap/Kernel, and the capability-package framework are contract-solid and adversarially reviewed. The substrate-direct install pattern is eliminated. Gate 5 (Hello Lattice) passes fully. Contract shapes are frozen behind a conformance suite. This is the prerequisite for Phase 2 (Loom, Weaver, reference app).
>
> **Gates delivered:** Gate 5 full pass (M1–M6); contract conformance suite green.

#### Story 1.5.1 — Substrate write-path contracts
`AtomicBatch`/`PublishBatch` accept `context.Context`; `AtomicBatch` returns per-key revisions; Processor wires `OperationReply.Revisions`; Bootstrap drops its hardcoded 30s timeout. **Unblocks** the Story 5.3 compensation contract (revisions feed compensating-op templates).
**Sources:** Core KV F-001, Processor P2-001. **Foundational — sequence first.**

#### Story 1.5.2 — DDL tombstone coherence (M6) *(absorbs B3)*
`TombstoneMetaVertex` cascades tombstones to all aspect keys (Bootstrap); Processor `loadMetaVertex` honors `isDeleted` and evicts the cache entry on tombstone commit.
**Sources:** Bootstrap F-007, Processor P2-002, Gate 5 B3. **Sequence after 1.5.3** (shared `meta_ddl.go`).

#### Story 1.5.3 — UpdateMetaVertex expansion
Extend `UpdateMetaVertex` to accept optional `script`, `permittedCommands`, `inputSchema`, `outputSchema`, `fieldDescription`, `examples`; mutate only fields present; preserve `metaKey` identity. Enables hot-fix of DDL bugs without tombstone+recreate.
**Sources:** Bootstrap F-002.

#### Story 1.5.4 — Capability auth freshness coherence (B4)
Deterministic `projectedAt` (input-derived clock — Andrew's choice) + heartbeat reprojection of unchanged capability docs. Closes `AuthFreshnessExceeded` false-positives and replay churn.
**Sources:** Refractor F-009 + GAP-001, Gate 5 B4.

#### Story 1.5.5 — Route capability package installs through Processor (M5) *(absorbs B2, C3)*
Installer submits `CreateMetaVertex`/`CreateRole`/etc. ops through the Processor (ops.meta lane) instead of substrate-direct writes. Processor sees installs in-line; DDL cache stays coherent (closes M5). Defines the formal package-install contract (C3). Reshapes/obsoletes cap-pkg CR F-001 (PreInstall orphans), F-002 (concurrent-install TOCTOU), F-004 (version upgrade — now a Processor-routed sequence), F-011 (tombstone OCC).
**Sources:** Cap-pkg CR substrate-direct surface, Gate 5 B2, retro C3, Andrew's "route through Processor" decision. **Largest story; depends on 1.5.1.**

#### Story 1.5.6 — Re-enable Hello Lattice M4–M6 + flip Gate 5 to full pass *(absorbs B1)*
Un-skip M4–M6 in `hellolattice_test.go`; verify B1 (Postgres adapter handles lenses with named non-key columns, not just `row_data` JSONB); flip the gate5 Health-KV marker to `passed: true`.
**Sources:** Gate 5 deferred milestones, B1 verification. **Depends on 1.5.2, 1.5.4, 1.5.5.**

#### Story 1.5.7 — Contract conformance suite + freeze *(absorbs C1, C2, C4)*
Conformance tests locking: OperationEnvelope / OperationReply / ContextHint shapes (C1), Core KV bucket layouts + key shapes (C2), and the DDL self-description aspect set (C4 — 7 Story-5.1 aspects + `.compensation`). Frozen shapes live in `/docs/contracts/`.
**Sources:** retro C1/C2/C4. **Depends on 1.5.1 (reply shape) and 1.5.3 (aspect mutability) landing first.**

### Sequencing (dependency-ordered)

```
Wave A:   1.5.1   1.5.4        1.5.3 → 1.5.2
          (parallel)           (sequenced — shared meta_ddl.go)
Wave B:   1.5.5            (needs 1.5.1)
Wave C:   1.5.7            (needs 1.5.1 + 1.5.3)
Wave D:   1.5.6            (needs 1.5.2 + 1.5.4 + 1.5.5) ← flips Gate 5 green
```

### epics.md edits
- Add a **Phase 1.5 — Hardening Block** section after Epic 6 (before the Phase 2+ deferred ledger), listing the 7 stories above.
- In the **Phase 2+ Deferred Architectural Capabilities** ledger: mark the DDL-cache and Refractor key-shape items now-in-scope under Phase 1.5; leave genuinely-Phase-2 items (historical replay FR51, read-path authz, ES adapter, NATS Services migration) as deferred.

---

## Section 5 — Implementation Handoff

**Scope:** Moderate → backlog reorganization, then standard story cycle.

**Process (Andrew's Phase 1.5 rule):** Winston runs CS → DS → CR via sub-agents that do **not** commit; Winston adjudicates CR notes, assigns fixes, commits when green, watches CI. New docs land in `/docs`.

**Next workflow steps:**
1. `bmad-create-story` for each of 1.5.1–1.5.7 (Wave A first).
2. Dev + CR cycle per story.
3. `bmad-check-implementation-readiness` before declaring Phase 1.5 done.
4. Deferred docs migration (prd/architecture/epics → `/docs` + distillates) once sprint planning settles — requires a `bmm/config.yaml` `planning_artifacts` re-point.

**Phase 2 readiness criteria (success definition for Phase 1.5):**
- All 7 stories done, each with a CR pass.
- Gate 5 Health-KV marker = `passed: true` (M1–M6).
- Contract conformance suite green in CI.
- Substrate-direct install pattern removed (grep-clean).
- → **Then** Phase 2 (Loom, Weaver, reference app) begins on a hardened, contract-frozen, fully-reviewed Core.

---

## Appendix — CR findings disposition ledger

| Source finding | Severity | Disposition |
|---|---|---|
| Refractor F-001 XOR=OR | P0 | Shipped `8552ca2` |
| Refractor F-002 silenced bootstrapper | P0 | Shipped `8552ca2` |
| Refractor F-003..F-008 | P1 | Shipped `8552ca2` |
| Refractor F-009 projectedAt | P1 | **Story 1.5.4** |
| Refractor F-010..F-015, nits | P2 | Shipped `8552ca2` |
| Bootstrap F-001 crash-window dup | P1 | Shipped `3cb5d1c` |
| Bootstrap F-002 UpdateMetaVertex | P1 | **Story 1.5.3** |
| Bootstrap F-003..F-006, F-008 | P2 | Shipped `3cb5d1c` |
| Bootstrap F-007 tombstone cascade | P2 | **Story 1.5.2** |
| Core KV F-001 AtomicBatch ctx | P1 | **Story 1.5.1** |
| Core KV F-002 conn TOCTOU, F-003 export | P1 | Shipped `6ccbe2e` |
| Core KV F-004..F-008, nits | P2 | Shipped `6ccbe2e` |
| Processor P1-001 event-id dup | P1 | Shipped `59e91cb` |
| Processor P1-002 dedup re-publish | P1 | Shipped `59e91cb` |
| Processor P1-003 filter subjects | P1 | Shipped `59e91cb` (+ test fix `a89c04b`) |
| Processor P1-004 heartbeat | P1 | Shipped `59e91cb` |
| Processor P2-001 reply revisions | P2 | **Story 1.5.1** |
| Processor P2-002 cache no-evict | P2 | **Story 1.5.2** |
| Processor P2-003..P2-006, nits | P2 | Shipped `59e91cb` |
| AI-agent F-001 script/permittedCommands | P0 | Shipped `46f3353` |
| AI-agent F-002..F-008, nits | P1/P2 | Shipped `46f3353` (+ lint fix `5f91783`) |
| Cap-pkg F-001 PreInstall orphans | P0 | **Story 1.5.5** (reshaped) |
| Cap-pkg F-002 concurrent install | P1 | **Story 1.5.5** (reshaped) |
| Cap-pkg F-003 cold-cell error | P1 | Shipped `64921cc` |
| Cap-pkg F-004 version upgrade | P1 | **Story 1.5.5** (reshaped) |
| Cap-pkg F-005..F-010, nits | P1/P2 | Shipped `64921cc` |
| Cap-pkg F-011 tombstone OCC | P2 | **Story 1.5.5** (reshaped) |
| Gate 5 B1 Postgres schema | — | **Story 1.5.6** (verify; largely resolved) |
| Gate 5 B2 cache invalidation | — | **Story 1.5.5** |
| Gate 5 B3 cache eviction | — | **Story 1.5.2** |
| Gate 5 B4 auth freshness | — | **Story 1.5.4** |
| Contracts C1 envelope/reply/hint | — | **Story 1.5.7** |
| Contracts C2 KV shapes | — | **Story 1.5.7** (+ docs sharded `e522966`) |
| Contracts C3 install contract | — | **Story 1.5.5** |
| Contracts C4 DDL aspect set | — | **Story 1.5.7** |
