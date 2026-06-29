# Addendum to the Augur design — the escalation dispatch path (resolving the Fire-1 (3b) blocker)

**Status: ✅ Lead-resolved (Winston) — build-ready; one design-of-record change FLAGGED FOR ANDREW (no contract change, no architectural fork).**
**Author:** Winston (Designer fire, 2026-06-29)
**Amends:** [`augur-design.md`](augur-design.md) §3.3 (the escalation read/dispatch path).
**Backlog row:** Lattice lane → *AI-native → The Augur* — unblocks the flagged **Fire 1 (3b)** (the Weaver
unplannable-escalation branch).

---

## For Andrew (one look)

**The blocker (Steward-flagged, Fire 1).** The Augur reasoning call needs three pieces of gap context —
`{targetId, entityId, gapColumn}` — to reach the `CreateAugurReasoningClaim` op that mints the claim vertex
and emits the bridge call. The ratified design §3.3 routed the call through
`triggerLoom → externalTask → bridge` and said those params are *"resolved from the row."* But the shipped
externalTask-param mechanism (Mechanism 2, `inferExternalTaskReads`, 65f4f4d) resolves **only
`subject.<aspect>.data.<field>` against the single subject vertex** — and `gapColumn`/`targetId` are
Weaver-lens-row context, **not** on any subject vertex. `StartLoomPattern` carries only
`{patternRef, subjectKey, instanceId, expectedRevision}` — there is **no channel** to thread the gap
coordinates through Loom to the instanceOp. So §3.3's "resolved from the row" was a **false premise** — the
row never reaches Loom. Fire 1 correctly stopped rather than build (3b) blind.

**The resolution — Option F (recommended): Weaver dispatches `CreateAugurReasoningClaim` as a `directOp`,
straight to the bridge; drop the Loom wrapper.** Grounding the bridge and the as-built augur op reveals the
Loom layer is **ceremony** for the single-step reasoning episode:

- The bridge (`internal/bridge/dispatch.go:17-19`) is **loom-agnostic by construction** — it consumes
  `events.external.>` (an *"ordinary business event"* emitted by **any** op's transactional outbox) and posts
  the named `replyOp` keyed on `instanceKey`. It never inspects or requires a Loom instance. Sync-only
  externalTasks (no `dispatchOp`) are already supported (`dispatch.go:43`).
- `CreateAugurReasoningClaim` (as built) **already** reads its gap context as literal params from `op.payload`
  and **already emits `external.augur` off its own outbox**. The Loom externalTask was only a vehicle to
  *submit* it with resolved params — and it is precisely the vehicle that can't supply non-subject params.
- Weaver's `actionDirectOp` (`internal/weaver/strategist.go:172-213`) **already** resolves a params map +
  reads from the lens row into an op payload. Weaver holds `{targetId, entityId, gapColumn}` in the row at
  dispatch — exactly what the op needs.

So the op is *already shaped for directOp dispatch*; the blocker exists **only** because the design inserted
Loom between them. Removing Loom hands the op its params directly. **No contract touch. No `StartLoomPattern`
change. No new platform primitive.** It *removes* surface (a loom pattern + an externalTask step) rather than
adding it, and preserves every safety property the design relied on (deterministic validator, FR58 visible
claim, bridge idempotency, Weaver anti-storm mark + reclaim recovery — all intact; see §4).

**The one thing to know (a design-of-record change, not a ratification gate).** This makes Augur the **first
op to drive the bridge from a non-Loom (Weaver-`directOp`) dispatch.** The bridge supports it by construction,
so this is **not** a frozen-contract change and **not** an architectural fork — it is a lead (Winston) design
call within already-blessed machinery, made in the grounded-deviation-flagged style the whole Augur Fire-1
build has used. I am proceeding so the Lattice lane is unblocked; **flagged for your awareness** in case you'd
rather keep Augur on the literal Loom path (that is Option B below — viable, but it buys a `§10.5/§10.9`
contract amendment and keeps a wrapper that orchestrates nothing). My recommendation is **F**.

---

## 1. The blocker, precisely

The flagged Fire-1 (3b) item: *the Weaver unplannable-escalation branch* (`internal/weaver/evaluator.go:148`,
`dispatchGap` `!ok`) must, when `target.Augur.Escalate` includes `unplannable`, dispatch the reasoning call.
The reasoning call's first effect is `CreateAugurReasoningClaim`, which requires
`params.{targetId, entityId, gapColumn, trigger}` (`packages/augur/ddls.go` `execute`).

The data flow that drops the context:

```
Weaver buildPlan (HAS targetID, entityID, gapColumn, full row)
   │   actionTriggerLoom → StartLoomPattern payload =
   │        { patternRef, subjectKey, instanceId, expectedRevision }      ◄── gap context DROPPED here
   ▼
StartLoomPattern op (§10.9) → loom.patternStarted event
   │   triggerBody = { instanceId, patternRef, subjectKey }               ◄── still no gap context
   ▼
Loom instance → externalTask step → params resolve via subject.<aspect>   ◄── subject vertex only; gapColumn
                                                                               is NOT on any vertex
   ▼
CreateAugurReasoningClaim instanceOp  — needs params.{targetId, entityId, gapColumn}  ✗ unavailable
```

`gapColumn` is the decisive one: it is a **(target, gap)** coordinate that exists only in Weaver's convergence
lens row, never as an aspect on the candidate or any other vertex. No amount of subject-templating can produce
it. The context must travel **from Weaver's dispatch**, not be re-derived downstream.

---

## 2. The resolution — Option F (recommended)

**Weaver dispatches `CreateAugurReasoningClaim` as a `directOp` whose params carry the trusted gap context;
the op mints the claim and emits `external.augur`; the bridge calls the model; `RecordProposal` (the bridge
`replyOp`, keyed on `instanceKey`) records the proposal. No Loom instance.**

```
unplannable gap on a violating row, target.augur escalates "unplannable"
  → Weaver CAS-creates the weaver-state mark <targetId>.<entityId>.<gapColumn>   (anti-storm — unchanged;
       one reasoning call per stuck gap per window)
  → Weaver fires a directOp (fireEpisode → act.submit — the SAME lane-1 path as every remediation):
        operationType: CreateAugurReasoningClaim
        payload: { instanceKey: <stable, claimId-seeded>, adapter: "augur", replyOp: "RecordProposal",
                   targetId:  row.<targetKeyColumn>,        # literal/row-template, resolved by Weaver
                   entityId:  row.<entityKeyColumn>,
                   gapColumn: "<the stuck gap>",            # a literal — Weaver knows it at dispatch
                   trigger:   "unplannable",
                   expectedRevision: <row revision> }
        reads: [ row.<targetKey>, row.<entityKey> ]         # the no-orphan alive checks
  → CreateAugurReasoningClaim commits: mints vtx.augurproposal.<handle> + .gap aspect (TRUSTED context) +
       forCandidate/forTarget links (no-orphan, FR58 "visible claim before the call"), and emits
       external.augur off its transactional outbox.   [op as built — only the payload read is flattened, §3]
  → the bridge's augur adapter reads the action catalog, hydrates context, calls the model (structured
       output), returns {action, params, rationale, confidence}.   [unchanged from §3.3 (a)-(d)]
  → the bridge posts replyOp RecordProposal (keyed on instanceKey = <handle>) → the Processor runs the §5
       deterministic validator against the TRUSTED entity_key read back from the claim → writes the proposal
       (pending | invalid).   [RecordProposal unchanged except it no longer emits externalTaskCompleted —
        there is no Loom instance to unpark]
```

The escalation is **still dispatched as a gap** — it inherits the anti-storm mark, OCC, lease, and
reconciler-sweep reclaim wholesale (Weaver's `directOp` runs through `fireEpisode` exactly like `assignTask`
/`triggerLoom`). A crashed or lost reasoning call is reclaimed at lease expiry and re-fired, idempotent on the
bridge `idempotencyKey = instanceKey` (≤1 billed model call/episode) and on `CreateAugurReasoningClaim`'s
create-only claim vertex (≤1 claim). The reasoning call is **synchronous** (design §3.3 — seconds), so the
bridge's sync `Adapter.Execute` path applies and no async/park lane is needed.

### 2.1 Why Loom earned nothing here

The reasoning episode is **single-step**: call the model → record the proposal (which then sits `pending` for
human review). There is no second step to advance to, so the loom instance's only capabilities — park/unpark
and cursor-rebuild — orchestrate **nothing**. The design's stated rationale for the bridge (§3.3 "Why the
bridge, not an in-process LLM client": reuse durable claim + idempotency + recovery, keep Weaver pure) is an
argument for **the bridge**, reached via an external-event emit — **not** for Loom specifically. F reaches the
bridge via Weaver's `directOp` emit and preserves that rationale exactly. Dispatch of an *approved* proposal
(design §3.4) was already routed through Weaver's `buildPlan` path, **not** the reasoning loom pattern — so
nothing downstream depended on a loom instance existing for the reasoning leg.

---

## 3. Build deltas (all worktree-only; Augur Fire 1 is unmerged)

Each is a small edit to the in-flight `augur-fire1` worktree — **no platform/contract code**:

1. **Weaver escalation branch (the unblocked 3b).** In `evaluator.go`'s unplannable path, when
   `target.Augur` escalates the trigger, build a **`directOp` GapAction** for `CreateAugurReasoningClaim` from
   the parsed `target.Augur` block (adapter, replyOp, the `targetId`/`entityId` row-column mappings,
   `gapColumn` literal = the stuck column, `trigger` literal). This dispatches through the existing
   `buildPlan(actionDirectOp) → fireEpisode → act.submit`. No new Weaver mechanism.
2. **Flatten `CreateAugurReasoningClaim`'s payload read.** As built it reads a nested
   `op.payload.params.{targetId,…}` (the shape a loom externalTask passed). Weaver's `directOp` supplies
   **flat** top-level params (`ga.Params` values are literal/`row.<col>` strings). Change the op to read
   `targetId`/`entityId`/`gapColumn`/`trigger` flat from `op.payload` (alongside `instanceKey`/`adapter`/
   `replyOp`, already flat). One DDL-script edit; the validator + claim-mint + outbox-emit are otherwise
   unchanged. (Equivalent alternative — extend Weaver's `directOp` `Params` to resolve a nested-object value —
   is rejected: a larger Weaver change for no gain when the op shape is the one thing free to adjust.)
3. **Drop the loom pattern + externalTask.** Remove the `augurReasoning` loom-pattern artifact and its
   externalTask step from the augur package (Fire-1 item (5) "the `augurReasoning` externalTask pattern" is
   **deleted from scope**, not built). The augur target-policy `AugurPolicy.Pattern` field (parsed at 545a695)
   becomes unused → replace it with the reasoning **op + adapter** the policy names (a small adjust to the
   *unmerged* target-policy parse/validate; `validateAugurPolicy` already in worktree). No `StartLoomPattern`,
   no `inferExternalTaskReads` involvement for augur.
4. **`RecordProposal`: drop the `orchestration.externalTaskCompleted` emit.** No loom instance to unpark.
   Correlation is unaffected — the bridge posts `RecordProposal` keyed on `instanceKey`, and RecordProposal
   reads the claim by `<handle>` and runs the §5 validator against the trusted `.gap` context, exactly as
   built. (The "reply-without-claim → rejected" adversarial still holds: a model reply can never fabricate a
   proposal.)
5. **Permissions/bootstrap.** Weaver's **service-actor** must hold a capability grant to submit
   `CreateAugurReasoningClaim` (in F, *Weaver* submits it directly — under the loom design the loom actuator's
   service-actor did). The augur package's `permissions.go` grants the op to Weaver's service-actor (the same
   `system`-lane service-actor that submits `assignTask`/`triggerLoom`/`directOp` remediations today — lane
   authorization already grants it `system`, ref the shipped Lane-authorization Fire 1). Wire it in the
   primordial-package install (Fire-1 item (4)).

**Net:** Fire 1 gets *smaller* — items (3b), (4), (5), (6) proceed; item (5)'s "augurReasoning externalTask
pattern" is deleted, not built; no `StartLoomPattern`/Loom change anywhere.

---

## 4. Safety properties — preserved (the review checklist)

| Property | Under the ratified Loom path | Under Option F | Preserved? |
|---|---|---|---|
| **Weaver never calls the model** | bridge does (via externalTask emit) | bridge does (via directOp emit) | ✅ |
| **Deterministic validator (§5) on TRUSTED context** | RecordProposal validates vs the claim's `.gap` | identical — RecordProposal unchanged | ✅ |
| **FR58 visible claim before the call** | instanceOp mints claim, then emits external | directOp mints claim, then emits external | ✅ |
| **≤1 billed model call / episode** | bridge `idempotencyKey = instanceKey` | identical — same instanceKey | ✅ |
| **≤1 claim / episode** | create-only claim vertex | identical | ✅ |
| **Anti-storm (one call per stuck gap/window)** | Weaver mark on the gap | identical — directOp runs through `fireEpisode` | ✅ |
| **Crash/loss recovery** | reconciler reclaim re-fires; bridge dedups | identical — directOp is reclaimed like any gap | ✅ |
| **Reply can't fabricate a proposal** | RecordProposal requires a live claim | identical | ✅ |
| **Human gate before any remediation** | proposal sits `pending`; §3.4 dispatch via buildPlan | identical — unchanged | ✅ |

The only behavioral subtraction is the loom park/unpark, which orchestrated nothing for a single-step episode.

---

## 5. Alternatives considered (the row's A/B/C + F)

- **A — per-(target, gap) literal-param loom patterns.** Rejected: `entityId` is per-candidate (per row), so
  even a literal pattern can't avoid threading *something* from the row; and patterns are installed package
  artifacts (can't be minted per gap), so this needs O(targets × gaps) patterns. Ugly and doesn't scale.
- **B — extend `StartLoomPattern` with a generic trigger-params pass-through (§10.5/§10.9 contract touch).**
  *Viable and clean as a general primitive* (any future Weaver→Loom escalation needing row/gap context would
  use it), and it keeps Augur on the literal Loom path. **Rejected for Augur** because the reasoning episode is
  single-step — the loom wrapper orchestrates nothing — so B buys a frozen-contract amendment (StartLoomPattern
  payload gains `params`; externalTask param resolution gains a `trigger.<field>` namespace frozen at instance
  start; §10.6 determinism re-argued) to keep a wrapper that earns nothing. If a *future* multi-step
  Weaver→Loom escalation appears, file B then as the general primitive — do not pre-build it for Augur (no
  consumer needs the loom orchestration here). **This is the fallback if Andrew prefers Augur stay on Loom.**
- **C — synthesize a reasoning-subject vertex carrying the gap context, use it as the loom subject.** Rejected:
  Weaver doesn't write Core KV (P2) — it would need a directOp to mint the subject *and then* a StartLoomPattern,
  i.e. two dispatches for one gap (breaks the one-episode mark model), and the synthesized subject **is** the
  claim vertex we already mint — circular. F is C with the redundant loom hop removed.
- **F — directOp → bridge, drop Loom (recommended).** Simplest extension of existing machinery (Weaver
  `directOp` + the loom-agnostic bridge + the already-built `CreateAugurReasoningClaim`); no contract touch;
  removes surface; faithful to the design's intent.

---

## 6. Contract surface & flags

- **No frozen-contract change.** Contracts #2 (envelope), #4 (idempotency), #6 (capability), #10
  (orchestration) are all **built to**, not changed. In particular **Contract #10 §10.5/§10.9 are NOT
  touched** (the Option-B amendment is *not* staged). The augur ops remain package DDL (ops are package data).
- **Design-of-record note (flagged for Andrew, lead-authority — not a ratification gate):** Augur becomes the
  **first non-Loom op to drive the bridge**. The bridge is loom-agnostic by construction (`dispatch.go:17-19`),
  so this is within blessed machinery. Recorded here so the augur design-of-record reflects `directOp → bridge`,
  superseding §3.3's `triggerLoom → externalTask → bridge` *for the reasoning leg* (the approved-proposal
  dispatch leg, §3.4, was already non-Loom).
- **Augur design §3.3 is amended by this addendum** (mechanism corrected; intent and §3.1/§3.2/§3.4/§3.5/§5
  unchanged).

---

## 7. For the Lattice Steward — Fire 1 continuation (unblocked)

Build order, each independently green:

1. **(3b′) augur op + policy reshape (worktree).** Flatten `CreateAugurReasoningClaim`'s payload read (§3.2);
   drop the loom pattern + externalTask + the `Pattern` policy field → reasoning op + adapter (§3.3); drop
   RecordProposal's `externalTaskCompleted` emit (§3.4). Reuse the existing validator + claim tests; add a unit
   asserting the flat-payload read.
2. **(3b″) Weaver escalation branch.** `evaluator.go` unplannable path builds the `directOp` GapAction from the
   parsed `target.Augur` block; dispatch through `buildPlan(actionDirectOp)`. Test: an unplannable gap on an
   augur-enabled target emits a `CreateAugurReasoningClaim` directOp carrying `{targetId, entityId, gapColumn,
   trigger}` from the row, marked anti-storm; a non-augur target does not.
3. **(4) Permissions + bootstrap install-list.** Grant Weaver's service-actor `CreateAugurReasoningClaim`
   (§3.5); add `augur` to the primordial install list + version bump; `make verify-package-augur` + ephemeral
   e2e (fresh bootstrap).
4. **(5) `augur-proposals` review lens** (unchanged from Fire 1's remaining scope).
5. **(6) e2e + adversarial loop** (FakeAugur: unplannable gap → `pending` proposal end-to-end through Weaver
   directOp → bridge → RecordProposal; a malicious proposal → `invalid`, never dispatches).
6. **(7) the full 3-layer adversarial review** at Fire-1 completion — focused on the §5 validator boundary +
   the directOp-emit/reply correlation (now the load-bearing seam in place of the reply-path split).

This addendum needs **no Andrew ratification to build** (no contract change, no fork); the design-of-record
note in §6 is flagged for his awareness and is reversible to Option B if he prefers the Loom path.
