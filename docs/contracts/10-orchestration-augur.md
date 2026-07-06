# Contract #10 (Augur) — AI-reasoning escalation & dispatch

> **A part of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)**, extracted from
> **§10.8** (Weaver target + playbook — [10-orchestration-weaver.md](10-orchestration-weaver.md)), where
> the `augur` block shape and the `proposedOp` action row are declared as its Weaver-side hooks. The
> Augur is the L3 AI boundary with a human review gate; full design in
> `_bmad-output/implementation-artifacts/augur-design.md`. These subsections retain their §10.8 lineage.

### Augur escalation (✅ Andrew-ratified 2026-06-27)

> **The Augur** is the **AI-assisted reasoning tier** (the L3 evaluator tier, `docs/components/weaver.md`)
> — the named feature that implements L3. **Additive, opt-in, default-absent.** A `meta.weaverTarget` MAY
> carry an `augur` block. With **no `augur` block** the target behaves **exactly as the frozen contract** —
> an unplannable gap (a `missing_*: true` column with no `gaps[col]` entry) fails closed (config error →
> alert, above). The `augur` block redirects that dead-end to the Augur: Weaver dispatches a `triggerLoom`
> of the `augur.pattern` reasoning `externalTask` (a new `augur` **bridge adapter** — package/bridge data,
> the `external` domain is ordinary per §10.5; Weaver never calls the model directly), the model (default
> `claude-opus-4-8`) proposes a remediation **constrained to the installed action catalog** — via Anthropic
> **structured outputs** (`output_config.format`) / strict tool use, so it cannot emit an out-of-catalog
> action — and the `replyOp` records it as a `vtx.augurProposal` vertex (package DDL) **pending human
> review**. The AI **proposes**; a deterministic validator + a human gate **govern**; the Processor stays
> the sole writer (P2). Full design: `_bmad-output/implementation-artifacts/augur-design.md`.
>
> ```
> "augur": {
>   "escalate": ["unplannable" | "exhausted", ...],  // which stuck-gap triggers escalate (default: none)
>   "pattern":  "<reasoning externalTask pattern ref>",
>   "model":    "<optional adapter model override; default claude-opus-4-8>",
>   "autoApply": {                                    // OPTIONAL — DESIGNED, not enabled until Andrew ratifies
>     "actions": ["<low-risk action allow-list>"],    //   the autonomy boundary. A proposal in this allow-list
>     "minConfidence": 0.0..1.0                        //   + ≥ minConfidence + passing deterministic validation
>   }                                                  //   may skip the human gate; ABSENT = human-in-the-loop
> }
> ```
>
> **Install-time validation** (same class as the `gaps`-key + `targetId`-uniqueness checks): `augur.escalate`
> values ∈ `{unplannable, exhausted}`; `augur.pattern` resolves to an installed `meta.loomPattern` whose body
> is an `externalTask`; `augur.autoApply.actions` ⊆ `{triggerLoom, assignTask, directOp}`. **Affected
> consumers:** the Weaver engine (the escalation branch) + package authors (the `augur` package).
> The `gaps`/templating/action-table shapes below are **unchanged**.

### Augur dispatch (Fire 2b — approved proposal → remediation)

> The escalation above turns a stuck gap into a `vtx.augurproposal` vertex pending human review; **dispatch**
> is how an `approved` proposal becomes a real remediation. The `augur` package ships a primordial
> **`augurDispatch` convergence target** (a `meta.weaverTarget` + the `augurDispatchPending` lens) that
> projects one §10.2 row per proposal into `weaver-targets` under the `augurDispatch.` prefix with
> **`violating = (review.state == "approved")`** and the proposed action/params + the TRUSTED candidate as
> param columns. So an approved proposal is picked up by Weaver's **existing lane-1 machinery** (watch / mark /
> lease / sweep) — no new pickup path. Its single gap `missing_dispatch` maps to the **`proposedOp`** action
> (action table above): Weaver materialises the row-carried `{action, params}` into the existing `buildPlan`
> after the **dispatch-time deterministic re-validation** (the design §5 *third* leg — action vocabulary +
> live-registry resolution + default-deny scope to the trusted candidate + Weaver-authority), then dispatches
> a **two-op** episode: the proposed remediation op (carrying a **proposal-scoped deterministic requestId**, so
> a sweep re-dispatch collapses on the Contract #4 tracker — at-most-once), and **`RecordProposalDispatch`**
> (package op) flipping `review.state approved → dispatched | invalid` + stamping `dispatchedAt`. The flip
> reprojects the row `violating = false` → the mark clears (level-reconciled) → no re-dispatch; because
> correctness rests on the deterministic requestId, the flip is liveness (stop the churn), and a
> genuinely-lost remediation leaves the **original** target violating → it re-escalates (a fresh proposal
> supersedes). A proposal that fails dispatch-time re-validation flips `invalid` (auditable) and dispatches
> nothing. Dispatch is **human-in-the-loop** (a proposal dispatches only after `ReviewProposal{approve}`);
> the `autoApply` autonomy boundary (Fire 3) is unchanged. **Affected consumers:** the Weaver engine (the
> `proposedOp` branch + the 2-op fire) + the `augur` package (the `augurDispatch` target/lens + the
> `RecordProposalDispatch` op). Full design:
> `_bmad-output/implementation-artifacts/augur-dispatch-pickup-design.md`.

