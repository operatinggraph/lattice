# Augur

**Component reference** | Audience: implementers + architects

> Decisions of record live in `_bmad-output/planning-artifacts/lattice-architecture.md` →
> "Phase 2 Architecture — Orchestration Core" (D3, D4, AI-native reasoning tier). The frozen surface
> is the additive `augur` block on the Weaver target definition, Contract #10
> ([`docs/contracts/10-orchestration-augur.md`](/docs/contracts/10-orchestration-augur.md)) — where
> this page and the contract diverge, the contract governs. Update this page in the same commit as the
> code; drift is a documentation bug.
>
> This page describes **what the Augur is**. A per-surface ledger of what is built vs. deferred lives
> in [Implementation status](#implementation-status) at the end.

The Augur is **the Weaver's AI-assisted reasoning tier (L3)** — the escalation boundary for
convergence gaps the deterministic path cannot plan. Weaver's own tiers handle every gap the package
author anticipated (L1 re-confirms the violation and de-dups in-flight work; L2 hydrates context,
classifies the gap, and selects a playbook action). When a gap has **no** deterministic procedure — an
`unplannable` gap, or one that has `exhausted` its retry budget — Weaver escalates it to the Augur
instead of failing closed. The Augur turns that gap into a bounded reasoning call, records the model's
answer as a reviewable **proposal**, and — once a human approves it — dispatches it back through the
Weaver's ordinary remediation path.

The engine ships **zero domain knowledge**. The model is constrained to the catalog of actions the
Weaver can already dispatch; the proposal is a Core-KV vertex; the mutation goes through the Processor
like any other. The Augur proposes — **deterministic validation and a human gate govern, and the
Processor stays the sole writer** (P2). It is the bounded first step of the "AI-authored capabilities"
vision: prove the *propose → validate → gate → apply* loop on a single remediation before trusting the
platform to author anything larger.

External idempotent I/O is **not** an Augur concern (as with the Weaver): the reasoning call itself is
a Loom `externalTask` executed by the **bridge** (`docs/components/bridge.md`) — the LLM lives behind
the same egress boundary as any other outbound call. The Augur detects-and-proposes; it never reaches
an external system itself.

---

## Relationship to the Weaver

The Augur is not a parallel engine — it is the Weaver's deepest evaluation tier, reached by escalation,
never by routing.

- **L1 / L2 — deterministic (Weaver).** Re-confirm the violation, hydrate, classify the gap, select a
  playbook action. These handle every enumerable, routine gap.
- **L3 — the Augur.** The fallback for the un-enumerable: a gap with no playbook becomes an ambiguous
  problem statement for the model to reason about, constrained to the dispatchable action catalog.

The escalation is dispatched **as a gap**, so it inherits the Weaver's anti-storm mark, OCC, lease, and
reconciler-sweep recovery unchanged — one reasoning call per stuck gap per anti-storm window, idempotent
on the bridge idempotency key. An **approved** proposal is projected as a synthesized target row and
picked up by the Weaver's normal lane-1 handler: downstream, it is indistinguishable from a playbook
entry, re-validated at dispatch time like any other action.

---

## The loop

**Capture (escalation → proposal).** A stuck gap dispatches a `triggerLoom` of the `augurReasoning`
`externalTask` pattern. The bridge's `augur` adapter calls the model (pluggable; default
`claude-opus-4-8`; a deterministic `FakeAugur` runs in CI) with the action catalog supplied as a
structured-output schema, constraining the reply to
`{ action ∈ {triggerLoom, assignTask, directOp}, params, rationale, confidence ∈ 0..1 }`. The reply is
validated at record time and stored as a `vtx.augurProposal` vertex — `review.state = pending` if valid,
`invalid` if not. A pending proposal surfaces in Loupe for a human. *Value delivered on its own: an
unplannable gap becomes a reasoned, auditable, human-reviewable proposal instead of a silent dead end.*

**Verdict → dispatch (proposal → remediation).** An operator approves via `ReviewProposal` (re-validated
on approve; fails closed to `invalid` if the catalog has drifted since). An approved proposal projects
as a synthesized target row carrying the proposed `{action, params}`; the Weaver's lane-1 handler fires
it through the existing `buildPlan → fireEpisode → act.submit` path — re-validated once more at dispatch
time — under a **proposal-scoped deterministic requestId** (collapse-only under sweep reclaim). The gap
closes when the remediation's downstream work lands, and the proposal flips to `dispatched`.

---

## The safety boundary

Deterministic validation runs at **three independent points** — the model cannot emit an out-of-scope
action that reaches Core KV:

1. **Schema constraint (reasoning time).** Structured output forces `action ∈ catalog` and `params`
   conforming to that action's DDL schema.
2. **Record-time validation (`RecordProposal`).** Before `pending` storage: the action is one of
   `{triggerLoom, assignTask, directOp}`; the referenced pattern/operation resolves in the live
   registry; every param resolves; and the proposal's candidate **equals the escalated
   `(targetId, entityId)`** — no scope escape.
3. **Dispatch-time re-validation.** Action vocabulary + live-registry resolution + default-deny scope to
   the trusted candidate, immediately before `act.submit`. Any failure → `invalid`.

Behind all three sits the **Processor's own capability check** — the final, independent backstop that
governs every operation regardless of origin. Under the shipping configuration (human-in-the-loop
always), the blast radius of a bad proposal is **zero**: it cannot dispatch without approval, and it
grants no new authority.

---

## Data model

A proposal is business/meta state, so it is a Core-KV vertex (P1) — auditable and queryable, never
hidden operational state:

```
vtx.augurProposal.<NanoID>
  .gap        { targetId, entityId, gapColumn, trigger }
  .proposed   { action, params }
  .rationale  { text }
  .confidence { score }                 # 0..1, self-reported by the model
  .provenance { model, promptHash, catalogHash, reasonedAt }
  .review     { state, reviewedAt, dispatchedAt }
              # state ∈ { pending, approved, rejected, dispatched, invalid, superseded }

lnk.augurProposal.<id>.forCandidate.<type>.<entityId>
lnk.augurProposal.<id>.forTarget.meta.<weaverTargetId>
lnk.augurProposal.<id>.reviewedBy.identity.<reviewerId>   # stamped on approve / reject
```

Operators read proposals through the `augur-proposals` lens read-model (P5); Loupe, the inspector
exception, may read the vertex directly.

---

## Contract

The frozen surface is an **additive, opt-in `augur` block** on the Weaver target definition
(`meta.weaverTarget`), Contract #10:

```
"augur": {
  "escalate":  ["unplannable" | "exhausted", ...],   # which stuck-gap triggers reach L3
  "pattern":   "<reasoning externalTask pattern>",    # default: augurReasoning
  "model":     "<model override>",                    # default: claude-opus-4-8
  "autoApply": {                                      # designed, Andrew-gated (currently absent)
    "actions": ["triggerLoom", "assignTask", ...],   # low-risk allow-list
    "minConfidence": 0.0..1.0
  }
}
```

A target with **no `augur` block** behaves exactly as before — it fails closed on an unplannable gap.
The rest is package data: the `augur` package declares the proposal vertex DDL, the four operations
(`CreateAugurReasoningClaim`, `RecordProposal`, `ReviewProposal`, `RecordProposalDispatch`), the
`augur-proposals` read lens, the `augurDispatch` convergence target, and the reasoning pattern. The
bridge `augur` adapter is bridge-registry config. No kernel change.

---

## Principles that apply

- **P1** — the proposal is meta state → a Core-KV vertex; the in-flight reasoning *call* is operational
  (a bridge claim + the escalation mark in `weaver-state`).
- **P2** — the Processor is the sole Core-KV writer; the Augur only proposes. The Weaver writes
  `weaver-state` (its escalation mark) exactly as for any gap.
- **P5** — operators read proposals via the `augur-proposals` lens; Loupe is the one inspector exception.
- **Reasoning is egress** — the model call lives in the bridge, dispatched as a Loom `externalTask`;
  the Augur never opens a socket itself.

---

## Implementation status

| Surface | Status |
|---------|--------|
| Escalation branch (`unplannable` gap → L3 reasoning) | ✅ Built |
| Proposal vertex DDL + record-time deterministic validation | ✅ Built |
| Reasoning capture — `augurReasoning` pattern + bridge `augur` adapter (model + `FakeAugur` for CI) | ✅ Built |
| Human verdict — `ReviewProposal`, re-validated on approve | ✅ Built |
| Approved-proposal dispatch — `augurDispatch` target + dispatch-time re-validation + `proposedOp` | ✅ Built |
| Proposal-scoped deterministic requestId (collapse-only under reclaim) | ✅ Built |
| Autonomy dial (`augur.autoApply` allow-list + confidence gate) | 🔒 Designed, parsed + validated, **Andrew-gated** — human-in-the-loop ships until ratified |
| `exhausted`-trigger escalation (spent retry budget → L3) | ✅ Built — shares `augurEscalation` with `unplannable`; wired in `lease-signing` (screening gaps) |

**What ships today:** a stuck, unplannable gap becomes a reasoned, human-reviewed proposal that, once
approved, dispatches through the existing Weaver machinery. **Zero autonomous mutation** under the
default configuration — human approval is always required before anything is written. The autonomy dial
is built-but-disabled by design; enabling it is Andrew's call once the boundary is ratified.

---

*For the full design reasoning, the safety analysis, and the fire-by-fire decomposition, see the Augur
design docs under `_bmad-output/implementation-artifacts/` (`augur-design.md`, ratified 2026-06-27).*
