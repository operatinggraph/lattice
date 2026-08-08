# Contract #10 — Orchestration Surfaces (Loom / Weaver)

**Status: Phase 2 — FROZEN (2026-06-02).** Authored in the Phase 2 architecture sprint
(2026-06-01); hardened DESIGN→frozen across the 2026-06-02 data-contracts session (Loom side +
all four Weaver sections). Rationale: `lattice-architecture.md` → "Phase 2 Architecture —
Orchestration Core". Component detail: `docs/components/{loom,weaver}.md`.

This contract defines the data shapes the orchestration engines introduce. All sections (§10.1–§10.9)
are frozen — implementation stories build to these shapes; changes require a contract revision, not an
in-flight redefinition. **Known deferred carries** (do NOT reopen the frozen shapes — they extend them
later): shared pure-Starlark guard evaluator (until the first Starlark guard is authored, §10.5);
platform `scope: specific` in step-3 (§10.8 `triggerLoom` external callers, Phase 3); `weaver-work`
durable bucket (lane-2 / Phase 3, §10.3).

---


## This contract is sharded (§10.x numbers unchanged)

Contract #10 grew large, so its body is split into the component parts below. **Every `§10.x` section
number is preserved** — this is a file reorganization, not a renumber; keep citing `§10.8`, `§10.2`,
etc., exactly as before.

| Part | Sections | Surface |
|---|---|---|
| [Loom](10-orchestration-loom.md) | §10.5 · §10.6 · §10.9 | Pattern definition; step completion & correlation (incl. the `externalTask`/bridge seam); pattern trigger & lifecycle ops |
| [Weaver](10-orchestration-weaver.md) | §10.2 · §10.8 | Target Lens output; target + playbook + planner extension |
| [Augur](10-orchestration-augur.md) | §10.8 (Augur) | The AI-reasoning escalation & dispatch tier, extracted from §10.8 |
| [Substrate](10-orchestration-substrate.md) | §10.1 · §10.3 · §10.4 · §10.7 | Task vertex; operational KV (`loom-state` / `weaver-state`); ADR-51 message scheduling; ephemeral task grants |

The **§10.2 ↔ §10.8 detection↔remediation binding** lives entirely within the Weaver part. The
external-I/O **bridge** has its own adapter/envelope contract in
[`docs/components/bridge.md`](../components/bridge.md) — Contract #10 covers only Loom's `externalTask`
step surface (§10.5 / §10.6). The shared **revision history** for all sections is at the foot of this
index.

---

## Revision history

The row-per-revision record — the session-level deliberation behind every amendment — lives in
[`docs/decisions/contract-10-revision-history.md`](../decisions/contract-10-revision-history.md).
Every row's normative outcome is folded into the shard bodies; the index below exists to date a clause.

| Date | Sessions / amendments |
|------|------|
| 2026-06-01 | Created (Phase 2 design). |
| 2026-06-02 | The data-contracts freeze session: Loom guard grammar + step shape + §10.6 completion/correlation; task-auth realignment; auto-complete; links-not-fields; §§10.2/10.3/10.4/10.8 frozen and the contract flipped DESIGN→FROZEN; (a1) cap-lens extraction. |
| 2026-06-03 | §10.1 speculative-aspect drop; scoped pre-implementation review (coherence + crash-safety pins). |
| 2026-06-06 | Loom amendment (`loom-state` reshaped, `completionDomains`, §10.9 NEW); Loom command outbox. |
| 2026-06-07 | Event-domain model (`orchestration`-domain completion; `payload.taskKey` correlation). |
| 2026-06-12 | Weaver amendments (mark TTL = 2×lease; per-target+entity schedule keying; `freshUntil`); pattern-definition pinning. |
| 2026-06-13 | `nudge` gains `operation` (retired 2026-06-18). |
| 2026-06-18 | External I/O Bridge package: `externalTask` NEW; `weaver-claims` + `nudge` RETIRED; `actorAggregate` targets; externalTask deadline/completion symmetry. |
| 2026-06-19 | `directOp` gains optional `reads?`. |
| 2026-07-04 | Weaver planner mandate (shadow/planned modes, `candidates`/`goal`, reserved `__control`/`__count`/`__effect` shapes). |
| 2026-07-05 | `surface` GapAction; `goalColumns`; goal-first rider + per-leg plan execution + per-gap `actions` catalog. |
