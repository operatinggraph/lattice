# The Augur — Fire 2b: approved-proposal dispatch pickup (closing the loop) — design

**Status: 📐 awaiting-Andrew (ratification).**
**Component:** Weaver (convergence engine) + the `augur` capability package · builds on the ratified parent design `augur-design.md`
**Backlog row:** Lattice lane → *AI-native → the Augur (L3 evaluator)* → **Fire 2b**
**Author:** Winston (Designer fire, 2026-06-30)
**Parent:** [`augur-design.md`](augur-design.md) (✅ Andrew-ratified 2026-06-27; Fire 1 + Fire 2a shipped)

---

## For Andrew (ratify in one look)

**What it does, in two lines.** Fire 2a shipped `ReviewProposal` — a human can flip an Augur proposal
`pending → approved`. But nothing *dispatches* an approved proposal: the parent design's §3.4 said it
"projects as a synthesized §10.2 dispatch row and fires through Weaver's existing machinery," which the
backlog flagged as **under-resolved** — the `augur-proposals` lens is a *read-model*, not a weaver-target;
the proposed action is *data* but the §10.8 playbook is *static config* (and `directOp` deliberately
**rejects** a row-templated op type); and the `dispatched`-flip / re-dispatch-suppression was hand-waved.
**This design resolves all three** with the smallest extension of shipped machinery: a primordial
**`augurDispatch` convergence target** the `augur` package ships (so Weaver's *existing* lane-1
watch/mark/lease/sweep picks approved proposals up like any other gap), a new **opt-in §10.8 `proposedOp`
action** that materialises the row-carried `{action, params}` into the *existing* `buildPlan` after a
**dispatch-time re-validation** (the parent design's §5 third leg), and a **proposal-scoped deterministic
requestId** that makes every re-dispatch *collapse-only* — so the `RecordProposalDispatch` state-flip is a
best-effort cleanup, never a correctness dependency.

**Frozen-contract change: ONE — Contract #10 §10.8 (staged UNCOMMITTED in `main`).** Additive + opt-in:
(1) a new **`proposedOp`** row in the §10.8 action table, and (2) an **"Augur dispatch"** paragraph in the
existing "Augur escalation" subsection documenting the `augurDispatch` target + the dispatch-time
re-validation + the dispatched-flip. No existing target uses `proposedOp` (default-absent); the
`gaps`/templating/`augur`-block shapes are **untouched**. The diff in
`docs/contracts/10-orchestration-surfaces.md` **is** the proposal. **Affected consumers:** the Weaver
engine (the `proposedOp` action branch + the 2-op dispatch) and the `augur` package (the `augurDispatch`
target + lens + the `RecordProposalDispatch` op). Everything else — the new convergence lens, the dispatch
target registration, the flip op, the new Health metrics — is **package DDL / author-discretion**, no
contract change.

**No new architectural fork.** The one fork the parent design carries — the **autonomy boundary**
(`augur.autoApply`, Fire 3) — is **untouched and still gated on your sign-off**. Fire 2b is
*human-in-the-loop* dispatch only: a proposal dispatches **only** after a human `approve`. (Fire 2b is in
fact the precondition that makes Fire 3 even *possible* — `autoApply` just lets the engine write the
`approved` state the human writes here. So ratifying 2b does not pre-decide 3.)

**The one thing worth your eye.** The dispatch is intrinsically a *two-op* act — perform the proposed
remediation **and** record the proposal `dispatched` — and Weaver dispatches *fire-and-forget* (no commit
confirmation), so the two cannot be atomic. The design makes that safe the same way Weaver already makes
collapse-only userTask reclaims safe (§10.3): the proposed op carries a **deterministic, proposal-scoped**
requestId, so a re-dispatch **collapses on the Contract #4 tracker** → at-most-one effect regardless of
whether the flip landed; the flip merely stops the wasteful churn (paced by the *existing* collapse-only
backoff) and stamps `dispatchedAt`; and the **original convergence target is the ultimate backstop** (a
genuinely-lost remediation leaves the real gap violating → it re-escalates → a fresh proposal supersedes).
This is consistent with Weaver's whole fire-and-forget + level-reconcile + "the source row is truth"
philosophy — not a new safety model. §4.4 walks the failure matrix.

Everything else is resolved in the body. Nothing here blocks the **Lattice Steward** except your
ratification of the §10.8 edit.

---

## 1. Problem & intent

The Augur loop is: **detect a stuck gap → reason (Claude, via the bridge) → record a proposal → human
reviews → dispatch the approved remediation → the gap closes.** Fires 1–2a built everything up to the
human verdict:

- **Fire 1** (`adaf7be`) — escalation + capture. A target's `augur` block (§10.8, committed `2917e0f`)
  redirects an unplannable gap to `CreateAugurReasoningClaim` (a Weaver **directOp**, "Option F" — no Loom
  wrapper, `internal/weaver/strategist.go:augurEscalation`), which mints the `vtx.augurproposal.<handle>`
  claim vertex write-ahead and emits `external.augur`; the bridge calls the model; the `RecordProposal`
  replyOp stores a `pending` (or `invalid`) proposal under the **record-time §5 validator**
  (`packages/augur/ddls.go`).
- **Fire 2a** (`3dbd049`) — `ReviewProposal`, the human verdict: an operator flips `pending → approved |
  rejected`, **re-validating** on approve (`revalidate_for_approval`, the §5 *static* re-check).

**The gap Fire 2b closes (the loop's last hop).** An `approved` proposal carries, on the
`vtx.augurproposal.<handle>` vertex:

```
.gap        { targetId, entityId, gapColumn, trigger }   # TRUSTED escalation context (instanceOp-minted)
.proposed   { action, params }                           # the model's remediation (action ∈ {triggerLoom, assignTask, directOp})
.review     { state: "approved", reviewedAt, dispatchedAt: "" }
```

…and **nothing dispatches it.** The `packages/augur/package.go` doc already records the boundary verbatim:
*"The approved-proposal dispatch pickup is Fire 2b, Weaver-side."* The parent design's §3.4 sketched the
intent ("project as a §10.2 row, fire through the existing machinery, re-validated") but left three
mechanisms unresolved, which the backlog row called out and the Lattice Steward (2026-06-30) refused to
cold-start:

1. **Pickup transport.** The `augurProposals` lens (`packages/augur/lenses.go`) projects into its **own**
   read-model bucket `augur-proposals` for Loupe; it is **not** a `meta.weaverTarget` and Weaver's lane-1
   watches only `weaver-targets` (`<targetId>.>`). There is **no channel** by which an approved proposal
   reaches Weaver's dispatch path.
2. **Dynamic action vs. static playbook.** The proposed `action` + `params` are **data per proposal**, but
   the §10.8 playbook is **static config keyed by gap column**, and `buildPlan`'s `directOp` branch
   *explicitly rejects a row-templated operation* (`strategist.go:176` — "directOp operation must be a
   literal operationType"). A standard playbook entry **cannot** dispatch a row-carried op.
3. **The dispatched-flip + re-dispatch suppression.** §3.4 said "the proposed op's own commit (or a tiny
   `DispatchProposal` flip) advances `review.state → dispatched`" but did not resolve **which**, and a
   convergence lens **cannot observe an arbitrary op's effect** (a cypher lens can't compute a derived
   requestId or check a dynamic tracker key), so "the gap closes when the remediation lands and the lens
   re-projects" — true for ordinary gaps — does **not** transport here. Without resolution, an approved
   proposal re-dispatches on every sweep.

**Intent (unchanged from the parent).** AI **proposes**; a deterministic validator + a human gate
**govern**; the Processor stays the sole writer (P2); an approved AI remediation must be **indistinguishable
downstream from a hand-authored playbook entry** — same OCC, same anti-storm mark, same idempotency.

---

## 2. Grounding — the machinery this MUST reuse (mirror, don't reinvent)

The design's whole discipline is to reach the loop's last hop with the **smallest** new surface. Confirmed
against the code:

| Fire 2b needs | Already ships | Reused how |
|---|---|---|
| A way for Weaver to *see* approved proposals | **Lane-1 KV-CDC watch over `weaver-targets`** + the §10.8 `meta.weaverTarget` registry (`internal/weaver`, "Lane 1" ✅) | A new **`augurDispatch` convergence target** (package `meta.weaverTarget`) whose lens projects approved proposals as standard §10.2 rows into `weaver-targets` under the `augurDispatch.` prefix. **Zero new pickup path** — it is one more registered target. |
| Anti-storm / OCC / lease / crash-recovery for the dispatch | **`weaver-state` mark (CAS-create = OCC) + 2× lease TTL + reconciler sweep** (§10.3 ✅) | The dispatch is just another gap (`augurDispatch.<proposalId>.missing_dispatch`) — inherits all of it unchanged. |
| To resolve + fire the proposed `{triggerLoom \| assignTask \| directOp}` | **`buildPlan` + `fireEpisode` + `act.submit`** (`strategist.go` / `evaluator.go` ✅) — the three actions the model proposes ARE Weaver's three actions | The `proposedOp` action **materialises a `GapAction` from the row** and calls the **existing** `buildPlan`. No new action *semantics* — only a new *source* for the action (the row, not the static playbook). |
| At-most-once dispatch under sweep re-fire | **Deterministic, stable artifact id ⇒ collapse-only** (§10.3 — the userTask `claimId`-seeded `instanceId`/`taskId`; the Contract #4 `vtx.op.<requestId>` tracker) | The proposed op carries a **proposal-scoped deterministic requestId** (`derive(proposalHandle)`), generalising the collapse-only property to all three actions. A reclaim re-derives the same id → tracker collapse → no second effect. |
| Pacing the wasteful re-fire of a collapse-only dispatch | **`backoffInterval` for collapse-only userTask reclaims** (`reconciler.go`, `sweepReclaimsSuppressed` ✅) | The `proposedOp` dispatch is a collapse-only class → qualifies for the **same** backoff. |
| Recording the verdict transition as an op (P2) | **The op core + the augur DDL state machine** (`packages/augur/ddls.go` ✅) | One new package op, `RecordProposalDispatch`, flips `approved → dispatched \| invalid` — a sibling of `ReviewProposal`, same pending/approved-only guard idiom. |
| Operators see what dispatched | **The `augurProposals` read-model lens + Loupe** (P5 ✅) | The lens already projects `.review.dispatchedAt`; no change needed (the flip fills it in). |

**Invariants honored (checked):** **P2** — every mutation is an op (the proposed op, the flip); Weaver's
only direct write stays `weaver-state`. **P5** — operators read via the read-model lens; the `augurDispatch`
rows live in Weaver-internal `weaver-targets`, off the read-path (§10.2 "no read-path authz anchor here").
**P1** — the proposal is a Core-KV vertex; the dispatch episode is operational (the `weaver-state` mark).
**The engine ships zero domain knowledge** — the `augurDispatch` target + lens are **package** data, so
the generic Weaver engine never learns the `augurproposal` type (the `proposedOp` action is generic: it
materialises *whatever* action the row carries). **Contract #1 key-shapes** — no new key shapes; the
`augurDispatch.<proposalId>` row key is the standard §10.2 `<targetId>.<entityId>` (entityId = the proposal
handle NanoID).

---

## 3. The shape

### 3.1 Pickup — the `augurDispatch` convergence target (package DDL)

The `augur` package gains a **second lens** and its **first `meta.weaverTarget`**:

```
lens:    augurDispatchPending   (engine: full, adapter: nats-kv, bucket: weaver-targets)
target:  meta.weaverTarget { targetId: "augurDispatch", lensRef: "augurDispatchPending",
           gaps: { "missing_dispatch": { "action": "proposedOp" } } }
```

The lens projects **one row per proposal** into the shared primordial `weaver-targets` bucket under the
`augurDispatch.` prefix (the §10.2 contract-contribution pattern; `targetId` install-validated unique):

```
bucket: weaver-targets
key:    augurDispatch.<proposalHandle>            # entityId = the proposal NanoID handle (§10.2 entity-ID rule)
value:  {
          "entityKey":       "vtx.augurproposal.<handle>",   # §10.2 echo — the row IS about the proposal
          "violating":       true,                           # = (reviewState == "approved")
          "missing_dispatch": true,                          # the single gap column = violating
          "proposedAction":  "directOp",                     # param column — pr.proposed.data.action
          "proposedParams":  { ... },                        # param column — pr.proposed.data.params (JSON map, verbatim)
          "candidateKey":    "vtx.leaseapp.<id>",            # param column — pr.gap.data.entityId (TRUSTED)
          "targetMetaKey":   "vtx.meta.<weaverTargetId>",    # param column — pr.gap.data.targetId (TRUSTED)
          "originGap":        "missing_approval",            # param column — pr.gap.data.gapColumn (audit)
          "projectedAt":     "..."
        }
```

Key decisions (each grounded):

- **`violating = (reviewState == "approved")`, and it is the *only* dispatching state.** A `pending` /
  `rejected` / `invalid` / `dispatched` / `superseded` proposal — and a claim still in flight (null
  `reviewState`) — projects `violating = false`. **Default-deny / fail-closed:** an absent or unrecognised
  `review.state` → `false` → no dispatch (mirrors the parent design's §5 default-deny; a forgotten/garbled
  field never dispatches). This is the same null-safe `node.<aspect>.data.<field>` discipline the existing
  `augurProposals` lens already uses for in-flight claims (`lenses.go`).
- **Row-per-proposal, not row-only-when-violating** (§10.2, the settled "avoid Refractor retraction"
  choice). The row key `augurDispatch.<proposalHandle>` is **1:1 with the proposal vertex**, so a state flip
  `approved → dispatched` is a **single-row column overwrite**, *not* a row-set shrink — `violating` retracts
  to `false` via the ordinary §10.2 upsert, with **no negative/filter-retraction primitive required** (this
  design is independent of the in-flight negative-retraction work; see §6). The row is deleted only on true
  proposal tombstone (`IsDeleted`). *(This is the explicit "retraction-has-a-transport" check: the changed
  projection here is a single-row overwrite, so overwrite-by-reprojection genuinely retracts it.)*
- **`proposedParams` projects verbatim as a JSON map column** — exactly as the existing `augurProposals`
  lens already projects it (`lenses.go:81`), the same shape `clinicProviders` uses for non-scalar columns.
  A §10.2 param column is free-form, so this needs no contract change.
- **The candidate / target keys come from the TRUSTED `.gap` aspect** (instanceOp-minted, model-independent
  — the parent design's load-bearing safety split), **not** from `proposed.params`. The proposal vertex
  carries everything; the lens is **flat** (no link walk), one row per proposal — the same clean shape the
  `augurProposals` read-model lens uses.

### 3.2 Dispatch — the `proposedOp` action (§10.8, additive)

`missing_dispatch` maps to a **new opt-in action `proposedOp`**. Unlike the three existing actions whose
op + params are *playbook-static*, `proposedOp` sources them from the **row**. Its handler (Weaver Go,
beside `buildPlan`) does:

1. **Read** `proposedAction`, `proposedParams`, `candidateKey`, `targetMetaKey` from the row.
2. **Dispatch-time re-validation — the parent design's §5 *third* leg** (the live-catalog drift check
   that the record-time + approve-time legs explicitly deferred to here, `ddls.go:revalidate_for_approval`
   comment: *"the live-catalog drift re-check is the dispatch-time leg, Weaver-side"*):
   - **action ∈ {`triggerLoom`, `assignTask`, `directOp`}** (the allowed escalation vocabulary).
   - **Scope containment, default-deny:** every `vtx.*`-shaped value in `proposedParams` **equals
     `candidateKey`** (re-running the parent design's `scope_verdict` discipline in Go — defense in depth;
     `candidateKey` is trusted from `.gap`, never from `proposed`). A foreign vertex key → invalid.
   - **The op is in Weaver's authority allow-list** — a static set the engine knows from the augur
     permission grant (belt-and-suspenders; the Processor's capability check is the final independent
     backstop — `proposedOp` grants the model **no new authority**, only the right to *arrange* Weaver's
     existing service-actor authority).
3. **Materialise a `GapAction`** from `{proposedAction, proposedParams}` and call the **existing**
   `buildPlan` (with `candidateKey` as `entityKey`). `buildPlan` resolves the pattern/op refs **against the
   live registry** (`source.patternMetaKey` / `source.opMetaKey`), so a stale proposal whose op was
   uninstalled since approval surfaces as a `planError` → **invalid** (`errConfig`/`errData`) or a bounded
   transient retry (`errTransient` — replay lag), exactly as for any other gap. This *is* the live-catalog
   drift check, reusing `buildPlan`'s existing resolution.
4. **Outcome:**
   - **valid →** dispatch the materialised plan (§3.3) with a **proposal-scoped deterministic requestId**,
     then record `dispatched`.
   - **invalid →** dispatch **no** remediation; submit `RecordProposalDispatch{handle, outcome:"invalid",
     reason}` so the row flips non-violating (the operator sees the auditable reason) + a Health issue.

> **Why a new action, not a relaxed `directOp`.** `directOp` *deliberately* rejects a row-templated op type
> (`strategist.go:176`) because a lens naming an arbitrary op under Weaver's authority is a
> capability-escape surface — the exact surface the §5 validator exists to close. Relaxing that rule
> *generally* would weaken every ordinary playbook. `proposedOp` confines the dynamic-op capability to the
> **one** primordial target whose rows come **only** from the §5-validated approved-proposal projection, and
> every dispatch re-runs the §5 validator. This is the *simplest extension that does not bend a deliberate
> convention for unrelated callers* (the parent design's red-flag reflex applied: the "directOp must be
> literal" constraint is real and worth keeping — so add a gated sibling, don't relax it).

> **Param vocabulary (resolved, not deferred).** The action catalog the model reasons over (parent §3.3(a))
> and the `proposedParams` it returns use the **§10.8 GapAction vocabulary** — `triggerLoom {pattern,
> subject}`, `assignTask {operation, assignee, target}`, `directOp {operation, params, reads}` — so
> materialisation into `buildPlan` is a direct field map. Because the §5 scope check is **default-deny on
> every `vtx`-key**, the proposable surface is intrinsically **candidate-scoped**: a `triggerLoom` whose
> `subject` is the candidate and whose `pattern` is a canonicalName (not a vertex key); a `directOp` on the
> candidate; an `assignTask` whose `target` is the candidate (an `assignee` naming a *different* identity is
> a foreign key → rejected — so an approval-routing proposal must target the candidate, e.g. a role-queue
> resolved package-side, not a foreign identity literal). This is a *deliberate* containment, not a defect;
> widening the proposable surface (foreign assignees, cross-entity actions) is a future, separately-ratified
> step and **out of Fire 2b scope**. The dispatch faithfully runs only what already passed §5.

### 3.3 The two-op dispatch + the dispatched-flip (the resolved §3.4 mechanism)

A successful dispatch is intrinsically **two ops** — *do the remediation* and *record the proposal
dispatched* — and they cannot be atomic (Weaver dispatches fire-and-forget, no commit confirmation; and an
arbitrary catalog op's DDL cannot also flip a proposal it knows nothing about). The resolution makes the
**proposed op idempotent on its own**, so the flip is cleanup, not correctness:

```
augurDispatch.<h> row: violating=true, missing_dispatch=true, proposedAction/Params/candidateKey
  → Weaver lane-1: CAS-create weaver-state mark augurDispatch.<h>.missing_dispatch  (anti-storm OCC)
  → proposedOp handler: §5 dispatch-validate → materialise GapAction → buildPlan
  → fire (the proposedOp variant of act.submit), in publish order:
      (a) the PROPOSED op, requestId = deriveProposalDispatchRequestID(h)   # PROPOSAL-scoped, deterministic
      (b) RecordProposalDispatch{ externalRef: h, outcome: "dispatched" }   # flips approved → dispatched
  → (a) commits its remediation (a leaseapp op / a Loom start / a task) — its Contract #4 tracker vtx.op.<derive(h)> now exists
  → (b) commits → review.state = dispatched, dispatchedAt = op.submittedAt, emits augur.proposalDispatched
  → CDC: proposal.review changed → augurDispatchPending lens reprojects: violating=false, missing_dispatch=false
  → Weaver level-reconciled mark-clearing (watch update + sweep) deletes the mark → NO re-dispatch
```

**Why proposal-scoped (not episode-scoped) requestId for (a).** Weaver's ordinary dispatch derives the op
requestId from the *mark revision* (`deriveEpisodeRequestID`, `evaluator.go:306`) — episode-scoped, so a
sweep *reclaim* (a fresh mark revision) re-fires with a **new** requestId. For an idempotent userTask that
is fine (the stable artifact id collapses it downstream); for an arbitrary directOp it would **double-apply**.
So `proposedOp` derives the proposed op's requestId from the **proposal handle** (stable across reclaims),
generalising the §10.3 collapse-only property to all three actions: a reclaim re-derives the **same**
requestId → **collapses on the Contract #4 `vtx.op.<requestId>` tracker** → **at-most-one** remediation
effect, whether or not the flip ever landed. (For materialised `triggerLoom`/`assignTask`, the stable
`instanceId`/`taskId` is additionally seeded from the proposal handle, so the consumer-side collapse holds
too — belt-and-suspenders.)

**`RecordProposalDispatch` (new augur package op)** — a sibling of `ReviewProposal`: payload
`{ externalRef: <handle>, outcome: "dispatched" | "invalid", reason? }`. Guard: transitions **only** from
`review.state == "approved"` (read-and-check, the same pending-only idiom Fire 2a uses); a redelivery or a
second flip from `dispatched`/`invalid` is a **no-op** (collapsed earlier by the Contract #4 requestId
tracker, and guarded again by the approved-only check). Stamps `dispatchedAt = op.submittedAt`
(replay-stable, no sandbox clock). Emits `augur.proposalDispatched{proposalKey, outcome}`. Granted to
`operator` (Weaver holds it via `holdsRole → operator`, exactly like the other three augur ops).

### 3.4 Re-dispatch suppression — the full failure matrix (this is the part §3.4 hand-waved)

| Event | What happens | Net effect |
|---|---|---|
| **Happy path** | (a) + (b) commit → `review.state=dispatched` → lens reprojects `violating=false` → mark cleared on the next watch update / sweep | remediation done once, no re-dispatch ✅ |
| **(b) lost** (flip publish failed / rejected) | row stays `violating`; sweep reclaims at lease expiry → re-fires (a) [**collapses** on `vtx.op.<derive(h)>` — no 2nd effect] + (b) [retried, eventually lands] | self-heals; bounded waste **paced by the existing collapse-only `backoffInterval`** + capped at the 24h tracker horizon ✅ |
| **(a) lost** (remediation publish failed) | publish-fail → **Nak → redelivery re-publishes the same requestId** (idempotent), exactly as today's Weaver dispatch | remediation eventually lands ✅ |
| **(a) rejected at Processor** (true catalog drift past `buildPlan`, or a capability denial) | no remediation, no tracker; if (b) still landed, proposal=`dispatched` but the **original** target (e.g. `leaseApplicationComplete`) is still violating → it **re-escalates** → a fresh proposal **supersedes** the stale one | self-heals via the original target — the accepted bound, mirroring §10.4 "a rejected `MarkExpired` waits for the next CDC touch" ✅ |
| **Sweep reclaim while genuinely in flight** | (a) re-fired → collapses on the tracker; (b) re-fired → no-op (already dispatched or approved→dispatched idempotent) | at-most-once ✅ |
| **Proposal re-approved after dispatch** | impossible — `ReviewProposal` guards **pending-only**; a `dispatched` proposal is terminal | n/a ✅ |

**The load-bearing property:** correctness (at-most-one remediation) rests on the **deterministic
proposal-scoped requestId** alone; the flip + the lens reprojection are *liveness* (stop the churn, record
the stamp), and the **original convergence target is the final backstop** for a genuinely-lost remediation.
No mechanism depends on observing the arbitrary proposed op's effect — which is exactly why the parent
§3.4's "the gap closes when the remediation lands and the lens re-projects" did *not* transport, and why
this design routes closure through the proposal's own `review.state` instead.

---

## 4. Reconciliation with the existing mental model (pre-empt "but didn't we…?")

- **"Didn't §3.4 already say this projects as a §10.2 row and reuses the machinery?"** It stated the
  *intent* but not the *mechanism*, and the three under-resolved points (read-model ≠ weaver-target;
  static playbook ≠ dynamic action; the lens can't observe an arbitrary op) each break the naive reading.
  This design supplies the missing transports: a **dedicated convergence target** (not the read-model
  lens), a **gated dynamic action** (not a relaxed directOp), and **closure via the proposal's own
  `review.state`** (not via observing the remediation).
- **"Does this duplicate or contradict an established pattern?"** No — it is the **maximal reuse** of lane-1
  dispatch. The only genuinely-new engine code is the `proposedOp` action branch (materialise + §5
  re-validate) and the `proposedOp` 2-op fire variant; everything else (watch, mark, lease, sweep,
  backoff, `buildPlan`, `fireEpisode`) is reused verbatim. The new package surface (one lens, one target,
  one op) mirrors `lease-signing`'s target + the existing three augur ops.
- **"Does it introduce new state — do we already keep it?"** No new operational state class: the dispatch
  uses a standard `weaver-state` mark; the proposal vertex already reserves `.review.dispatchedAt` (Fire 1
  wrote it as `""`). The augurDispatch rows are standard `weaver-targets` entries.
- **"Does it touch a parallel in-flight design?"** Checked the other `📐`/`🏗️` Lattice designs. The
  **negative/filter-retraction** design is **not** a dependency: §3.1 shows the augurDispatch rows are
  single-row-overwrite (1:1 proposal↔row), so `violating` retracts via ordinary upsert; it neither needs
  nor blocks the retraction primitive. No overlap with `kv.Links` (no reverse-link enumeration), the
  `hard-delete` verb (proposals soft-tombstone fine), or `script-read-posture` (the §5 dispatch validator
  runs in Weaver Go, not a Starlark live read).
- **"Is this default-open anywhere?"** No. Every gate fails closed: `violating` defaults `false` on any
  non-`approved`/absent state; the §5 scope check is default-deny on every `vtx`-key; an unresolvable op →
  `invalid`, never dispatch; the Processor capability check is the final backstop.

---

## 5. Contract surface

**One frozen-contract change — Contract #10 §10.8 — staged UNCOMMITTED in `main` (the diff is the
proposal).** Two additive edits, both opt-in / default-absent:

1. **A `proposedOp` row in the §10.8 action table:**

   | `action` | params | effect |
   |----------|--------|--------|
   | `proposedOp` | *(none — sourced from the row)* | Dispatch the **row-carried** `proposedAction` + `proposedParams` (materialised into a `GapAction`) after the **dispatch-time §5 re-validation** (action ∈ catalog · live-registry resolution · default-deny scope to the row's trusted candidate · op ∈ Weaver authority). The proposed op carries a **proposal-scoped deterministic requestId** (collapse-only re-dispatch). Used **only** by the `augur` package's primordial `augurDispatch` target; a target wiring `proposedOp` to a row whose source is not a §5-validated proposal is a package bug. |

2. **An "Augur dispatch (Fire 2b)" paragraph appended to the existing "Augur escalation" subsection**,
   documenting: the `augurDispatch` convergence target the `augur` package ships; the
   `violating = (reviewState == "approved")` projection; the two-op dispatch + the `RecordProposalDispatch`
   `approved → dispatched | invalid` flip; and the deterministic-requestId / original-target-backstop
   suppression model.

The §10.2 row shape, the templating rule, the `gaps`-key convention, the `augur` block, and the existing
action table rows are **untouched**. This is the same additive-extension class as the 13.1 `directOp.reads`
amendment (`2026-06-19`). **Affected consumers:** the Weaver engine (the new action branch + 2-op fire) and
the `augur` package (the new target/lens/op).

**No other contract change.** The `augurDispatch` `meta.weaverTarget`, the `augurDispatchPending` lens, the
`RecordProposalDispatch` op + permission, and the proposal `dispatched` state transition are **package
DDL**; the new Weaver Health metrics (`proposalsDispatched`, `proposalsDispatchInvalid`) are
**author-discretion** under Contract #5 §5.4.

---

## 6. Migration / compatibility & test strategy

**Migration.** Purely additive. The `augur` package bumps to `0.3.0` (one new lens, one new
`meta.weaverTarget`, one new op + permission); bootstrap re-installs it like any package edit
(`make refresh-package`-class; a *newly-added* target needs the Weaver registry to pick it up, which the
existing `meta.weaverTarget` CDC source does on install). No data migration; no existing target changes
behavior; a proposal recorded before Fire 2b simply becomes dispatchable once it is `approved`.

**Test strategy** (the fire ships green; mirrors the existing Weaver + augur e2e style):

- **Unit (Weaver Go)** — the `proposedOp` materialisation + dispatch-time §5 validator table: each accept
  class (a candidate-scoped directOp / triggerLoom / assignTask) → a built plan; each reject class (foreign
  `vtx`-key in params → invalid; unknown action → invalid; uninstalled op → invalid/transient; op outside
  Weaver authority → invalid) → `RecordProposalDispatch{invalid}` + no remediation submit. Determinism of
  `deriveProposalDispatchRequestID` (a reclaim re-derives the same id).
- **Unit (augur DDL)** — `RecordProposalDispatch`: `approved → dispatched` and `approved → invalid`
  transitions; the **approved-only guard** (a flip from `pending`/`dispatched`/`invalid`/`rejected` is
  rejected/no-op); `dispatchedAt` stamping; the `augur.proposalDispatched` event.
- **E2e (ephemeral stack, the real Processor + bridge + Weaver)** — the existing `FakeAugur` adapter
  (deterministic, no model call) drives: a target with an `augur` block hits an unplannable gap → a
  `pending` proposal → `ReviewProposal{approve}` → the `augurDispatch` target **dispatches** the proposed
  op → the **original** gap closes and the proposal flips `dispatched`. A second e2e: an approved proposal
  whose proposed op was uninstalled between approve and dispatch → flips `invalid`, dispatches nothing.
- **Adversarial (the Gate-3-style "DEFENDED" assertion for the dispatch surface)** — a `FakeAugur` proposal
  that somehow reached `approved` carrying a `directOp` on a **different** entity (a foreign `vtx`-key) is
  caught at the dispatch-time §5 scope check → `invalid`, **never** dispatches; an op outside Weaver's
  authority is caught at the validator **and** the Processor capability check. A **sweep-reclaim** mid-flight
  re-fires and the proposed op **collapses** on its deterministic tracker — exactly one effect (the
  `TestWeaverE2E_MidFlightKill` pattern, applied to the dispatch episode).

**Review.** This is a cross-cutting AI-surface + security-plane (capability-escape) fire. The parent design
ran a focused self-adversarial pass; for the **build**, the Steward should run the **full 3-layer review**
with explicit attention to (1) the dispatch-time §5 validator's default-deny completeness (every `vtx`-key
path), (2) the deterministic-requestId collapse under reclaim, and (3) the approved-only flip guard. *(This
design's own pre-build gate is this 3-layer-at-build requirement; there is no deferred Designer-lane
adversarial pass left dangling — the self-adversarial failure matrix §3.4 + §6 is recorded as run.)*

---

## 7. Decomposition for the Steward — ONE fire, internal build order

Fire 2b is **coupled and ships as one fire** (the lens, target, action, and flip are interdependent —
splitting them produces dead scaffolding: a target with no `proposedOp` action dead-ends at
`GapWithoutPlaybook`; a dispatch with no flip re-dispatch-storms). Build it in this internal order, each
step compiling green, the e2e proving the whole:

1. **`RecordProposalDispatch`** (augur DDL + permission) — the `approved → dispatched | invalid` flip op +
   its unit tests. (Independently testable; no consumer yet — but it lands *with* the rest in one fire, not
   as a standalone green increment.)
2. **`augurDispatchPending` lens + the `augurDispatch` `meta.weaverTarget`** (augur package) — projects
   approved proposals into `weaver-targets`. Install-validate `targetId` uniqueness.
3. **The `proposedOp` action** (Weaver: `GapActionSpec`/`GapAction` accept `proposedOp`; the action branch
   materialises a `GapAction` from the row + runs the dispatch-time §5 validator; `deriveProposalDispatch
   RequestID`; the 2-op `proposedOp` fire variant in the actuator) + the §10.8 contract edit.
4. **E2e + adversarial** (§6) on the ephemeral stack with `FakeAugur`; new Health metrics.
5. **Docs** — `docs/components/weaver.md` (the L3/Augur dispatch leg; the `proposedOp` action) +
   `packages/augur` doc (retire the "dispatch pickup is Fire 2b" boundary note → "shipped").

**Optional follow-ons (out of this fire):** the `exhausted` escalation trigger (already in the `augur.
escalate` enum — extend the Fire-1 escalation branch); **Fire 3** (`augur.autoApply`) stays **gated on
Andrew** — Fire 2b is its precondition (autoApply writes the `approved` state a human writes here), but
ratifying 2b does **not** enable 3.

---

## 8. Risks & alternatives

| Risk | Mitigation |
|---|---|
| **A malicious/stale approved proposal dispatches a harmful op.** | The dispatch-time §5 validator (action ∈ catalog · default-deny scope to the trusted candidate · op ∈ Weaver authority) + the Processor capability backstop. `proposedOp` grants the model **no new authority**. Adversarial test proves DEFENDED. |
| **Double-dispatch on sweep reclaim.** | The proposal-scoped deterministic requestId → Contract #4 tracker collapse → at-most-one effect, independent of the flip. (The §3.4 failure matrix.) |
| **The flip is lost → re-dispatch churn.** | Collapse-only (no second effect) + paced by the **existing** `backoffInterval` + capped at the 24h tracker horizon; self-heals when the flip lands. |
| **A genuinely-lost remediation leaves a `dispatched` proposal with no effect.** | The **original convergence target** stays violating → re-escalates → a fresh proposal supersedes — the accepted bound, mirroring §10.4's rejected-`MarkExpired` posture. |
| **Coupling the generic engine to the augur type.** | Avoided — the `augurDispatch` target + lens are **package** data; `proposedOp` is a **generic** action (materialises *whatever* row-carried action), so the engine never learns `augurproposal`. |

**Alternatives considered:**
- **Relax `directOp` to allow a row-templated op type** — rejected (§3.2): bends a deliberate
  capability-escape guard for *all* playbooks; `proposedOp` confines the dynamic-op surface to the one
  §5-gated target.
- **A new Weaver pickup path watching the `augur-proposals` read-model bucket directly** — rejected: a
  parallel watch/mark/recovery pipeline duplicates lane-1; the `augurDispatch` weaver-target reuses it
  wholesale.
- **Fold the flip into the proposed op's commit / drive closure from the remediation's effect** — rejected
  (§3.4): an arbitrary catalog op can't flip a proposal it knows nothing about, and a cypher lens can't
  observe a dynamic op tracker; closure must route through the proposal's own `review.state`.
- **Auto-register `augurDispatch` inside the engine when any `augur` target exists** — rejected: couples the
  zero-domain-knowledge engine to the augur package; ship the target as package data instead.
- **A Loom 2-step pattern (proposed op → flip)** — rejected: Loom pattern steps are static (§10.5); the
  proposed op type is data, so the dynamic-op problem recurs at the step level. The single-step directOp +
  a follow-on flip is the honest shape (the parent design's "an orchestration wrapper that orchestrates
  nothing is ceremony" reflex).

---

## 9. Open questions — resolved

- **How does Weaver *see* an approved proposal?** → A primordial **`augurDispatch` convergence target**
  (package `meta.weaverTarget` + the `augurDispatchPending` lens) projecting approved proposals as standard
  §10.2 rows into `weaver-targets`. Not the read-model lens; not a new pickup path. (§3.1)
- **How is a row-carried dynamic op dispatched, given `directOp` rejects a templated op type?** → A new
  opt-in §10.8 **`proposedOp`** action that materialises the row's `{action, params}` into the existing
  `buildPlan` after the dispatch-time §5 re-validation. (§3.2)
- **What flips `review.state → dispatched`, and how is re-dispatch suppressed?** → A **`RecordProposal
  Dispatch`** op (approved → dispatched | invalid), submitted as the second of a 2-op fire; the **proposal-
  scoped deterministic requestId** on the proposed op makes the whole thing collapse-only, so the flip is
  liveness, not correctness, and the original target is the backstop. (§3.3, §3.4)
- **Where does the dispatch-time (third) §5 validation run?** → In the Weaver `proposedOp` handler (Go),
  re-checking action vocabulary + default-deny scope + Weaver authority, with `buildPlan`'s live-registry
  resolution as the catalog-drift check. (§3.2)
- **Does this need the negative/filter-retraction primitive?** → No — the augurDispatch rows are
  single-row-overwrite (1:1 with the proposal), so `violating` retracts via ordinary §10.2 upsert. (§3.1, §4)
- **Does ratifying 2b pre-decide the autonomy boundary (Fire 3)?** → No — 2b is human-in-the-loop only;
  Fire 3's `autoApply` stays gated on Andrew. (For-Andrew block, §7)

---

## 10. What lands where

| Path | Change |
|---|---|
| `docs/contracts/10-orchestration-surfaces.md` §10.8 | **(UNCOMMITTED)** the `proposedOp` action-table row + the "Augur dispatch (Fire 2b)" paragraph |
| `packages/augur/lenses.go` | the `augurDispatchPending` convergence lens (→ `weaver-targets`, `augurDispatch.` prefix) |
| `packages/augur/targets.go` *(new)* + `package.go` | the `augurDispatch` `meta.weaverTarget` (`missing_dispatch → proposedOp`); bump to `0.3.0` |
| `packages/augur/ddls.go` + `permissions.go` | the `RecordProposalDispatch` op (approved → dispatched \| invalid) + its operator grant |
| `internal/weaver/strategist.go` (`GapAction`, `buildPlan`) | the `proposedOp` action: materialise a `GapAction` from the row + the dispatch-time §5 validator |
| `internal/weaver/evaluator.go` (actuator) | `deriveProposalDispatchRequestID`; the 2-op `proposedOp` fire variant (proposed op + `RecordProposalDispatch`) |
| `cmd/loupe/` *(optional)* | surface `dispatched` / `dispatchedAt` (the lens already projects it — likely free) |
| `docs/components/weaver.md` + `packages/augur` doc | the dispatch leg; retire the "dispatch pickup is Fire 2b" boundary note |
| tests | the validator unit table; the augur-DDL flip table; the `FakeAugur` dispatch e2e (approve→dispatch→gap-close) + the invalid-on-drift e2e; the adversarial scope-escape + sweep-reclaim DEFENDED |

---

*Designer fire — Winston. This design completes and resolves the parent Augur design's §3.4; it awaits
Andrew's ratification of the §10.8 edit (staged uncommitted) before the Lattice Steward builds Fire 2b as
one fire. The autonomy boundary (Fire 3) remains separately gated on Andrew.*
