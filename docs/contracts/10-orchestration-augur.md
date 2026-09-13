# Contract #10 (Augur) — AI-reasoning escalation & dispatch

> **A shard of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)**, extracted from
> **§10.8** ([10-orchestration-weaver.md](10-orchestration-weaver.md), which declares the `augur` block
> and the `proposedOp` action as its Weaver-side hooks). Design + mechanism detail:
> `_bmad-output/implementation-artifacts/augur-design.md`, `docs/components/augur.md`.

### Augur escalation (✅ Andrew-ratified 2026-06-27)

**Additive, opt-in, default-absent.** A `meta.weaverTarget` MAY carry an `augur` block; with **no
`augur` block** the target behaves exactly as the frozen contract — an unplannable gap (a
`missing_*: true` column with no `gaps[col]` entry) fails closed (config error → alert). The block
redirects that dead-end to the AI-reasoning tier:

- Weaver dispatches the reasoning **op directly** (a `directOp` — `augur.op`, default
  `CreateAugurReasoningClaim`) through the `augur.adapter` **bridge adapter** (default `augur`; the
  `external` domain is ordinary per §10.5 — Weaver never calls the model directly).
- The model proposes a remediation **constrained to the installed action catalog** (structured
  outputs — the adapter cannot emit an out-of-catalog action; mechanism: `docs/components/augur.md`).
- The `augur.replyOp` (default `RecordProposal`) records it as a `vtx.augurproposal` vertex (package
  DDL) **pending human review**. The AI **proposes**; a deterministic validator + a human gate
  **govern**; the Processor stays the sole writer (P2).

```
"augur": {
  "escalate": ["unplannable" | "exhausted", ...],  // which stuck-gap triggers escalate (default: none)
  "op":       "<reasoning op; default CreateAugurReasoningClaim>",   // dispatched as a directOp
  "adapter":  "<bridge adapter; default augur>",
  "replyOp":  "<records the proposal; default RecordProposal>",
  "model":    "<optional adapter model override>",
  "autoApply": {                                    // OPTIONAL — DESIGNED, not enabled until Andrew ratifies
    "actions": ["<low-risk action allow-list>"],    //   the autonomy boundary. A proposal in this allow-list
    "minConfidence": 0.0..1.0                        //   + ≥ minConfidence + passing deterministic validation
  }                                                  //   may skip the human gate; ABSENT = human-in-the-loop
}
```

**Install-time validation** (same class as the `gaps`-key + `targetId`-uniqueness checks):
`augur.escalate` values ∈ `{unplannable, exhausted}`; `augur.op` / `augur.adapter` / `augur.replyOp` /
`augur.model` (all optional overrides) are single NATS-token strings; `augur.autoApply.actions` ⊆
`{triggerLoom, assignTask, directOp}`. The §10.8 `gaps`/templating/action-table shapes are unchanged
(weaver shard).

### Augur dispatch (approved proposal → remediation)

Escalation turns a stuck gap into a `vtx.augurproposal` pending human review; **dispatch** is how an
`approved` proposal becomes a real remediation, riding Weaver's existing lane-1 machinery (no new
pickup path):

- The `augur` package ships a primordial **`augurDispatch` convergence target** (a `meta.weaverTarget`
  + the `augurDispatchPending` lens) projecting one §10.2 row per proposal under the `augurDispatch.`
  prefix with **`violating = (review.state == "approved")`** and the proposed action/params + the
  TRUSTED candidate as param columns.
- Its single gap `missing_dispatch` maps to the **`proposedOp`** action (§10.8): Weaver materialises
  the row-carried `{action, params}` into an ordinary dispatch after the **dispatch-time
  deterministic re-validation** (action vocabulary · live-registry resolution · default-deny scope to
  the trusted candidate · Weaver-authority), then dispatches a **two-op** episode: the proposed
  remediation op (carrying a **proposal-scoped deterministic requestId**, so a sweep re-dispatch
  collapses on the Contract #4 tracker — at-most-once) and **`RecordProposalDispatch`** (package op)
  flipping `review.state approved → dispatched | invalid` + stamping `dispatchedAt`.
- The flip reprojects `violating = false` → the mark clears (level-reconciled) → no re-dispatch.
  Correctness rests on the deterministic requestId; **the flip is liveness** (stop the churn). A
  genuinely-lost remediation leaves the **original** target violating → it re-escalates (a fresh
  proposal supersedes). A proposal failing re-validation flips `invalid` (auditable) and dispatches
  nothing.
- Dispatch is **human-in-the-loop** (a proposal dispatches only after `ReviewProposal{approve}`); the
  `autoApply` autonomy boundary is unchanged (Andrew-gated).

### Plan-shaped proposals + the promotion proposal (the planner mandate's Augur floor, §10.8)

- **A proposal MAY be plan-shaped**: the structured output carries an ordered `steps` list (each
  `{action, params}` from the same escalation vocabulary; **at most 8**), recorded on `.proposed` as
  `steps` with `action`/`params` mirroring `steps[0]`; a single-action proposal is the one-step case.
  The record-time and approval-time validation legs run **per step** (vocabulary · default-deny scope to
  the escalated candidate); one failing step stores the whole proposal `invalid`, naming the step.
- **An approved plan dispatches leg by leg on the `augurDispatch` target**: `.review.leg` counts the
  legs dispatched; the row projects `dispatchLeg`, Weaver materialises `steps[dispatchLeg]` after the
  dispatch-time re-validation, fires it under a **leg-scoped** proposal requestId, and the
  `RecordProposalDispatch` flip (carrying `leg`; refused when it does not equal `review.leg`) advances
  the counter — the proposal stays `approved` while legs remain and flips `dispatched` on the last. A
  leg that fails re-validation flips the whole proposal `invalid`; no partial plan continues. Legs are
  **ordered** — the next is published only after the previous leg's flip re-projected the row — and
  each dispatches **at most once**; a leg's success is not verified by the dispatch (a rejected leg
  leaves the origin gap violating, which re-escalates and a fresh proposal supersedes).
- **A promotion proposal** is Weaver-authored, never model-authored: when a `mode:"planned"` gap's
  `__effect` window (§10.3) for one actionRef holds a full window with every episode closed, Weaver
  submits **`RecordPromotionProposal`** (package op, Weaver-actor only) once per (target, gap,
  actionRef) — a `vtx.augurproposal` with `.gap.trigger = "promotion"`, its candidate the target's
  own meta vertex, `.proposed.action = "promotePlaybook"` carrying `{targetId, gapColumn, actionRef,
  window, closed}` — into the same review queue. It is **never dispatched**: the `augurDispatch` row
  projects `violating = false` for it in every state; an approval records a human-ratified
  recommendation that the package author promote the chain to a static playbook entry.
