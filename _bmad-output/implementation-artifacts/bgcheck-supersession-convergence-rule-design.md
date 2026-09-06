# Background-check supersession is a convergence rule — the older completed check retires through Weaver, never through the reply op

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-09-06 · Winston · one cold adversarial pass
run and folded (§13)
**Board row:** `[lease-signing] Supersede a background check automatically when its successor completes`
(lattice.md, ★★ / M) · **Parent:** [bgcheck-runaway-and-broad-filter-design.md](bgcheck-runaway-and-broad-filter-design.md)
§2 row D, §5 "(D) the durable rule", §6 (the op + the Andrew-authorized purge, shipped `689eb0c0`).

---

## For Andrew

**What it does, in two lines.** A completed background check that has a later completed sibling on the same
applicant, minted by this same package, retires itself: a new lens projects exactly those pairs, and Weaver's
ordinary `directOp` convergence submits the op Fire 2 already shipped. No new platform mechanism; package work in
`packages/lease-signing`, built from the Lattice lane like its parent.

**Why it is here and not Winston-adjudicated.** The row reserved one question for you — *is a superseded check's
history a record?* — and I have answered it rather than left it open; you ratify or overturn the answer. There is
**no architectural fork and no frozen-contract edit** (§8: the design builds to Contract #2 §2.5 class (g) and
Contract #10 §10.8 `directOp`, both existing).

1. **Product decision — a superseded check's history is a record at rest, not a live one (§4.4).** The op
   tombstones with the bare `op:tombstone` form, which storage applies body-preserving: the class, the `.outcome`
   with its `completedAt`/`validUntil`, and both links stay readable in Core KV (Loupe / audit / a future
   retention pass), and a `lease.serviceInstanceSuperseded{instanceKey, supersededBy, subjectKey}` provenance
   event rides `core-events`. What changes is only what *live* read models see — the readiness aggregate, the
   freshness timer, `renewalComplete`'s `bgcheckValidUntil`. **Recommendation: ratify.** The alternative (a
   marker the aggregate skips) keeps every superseded instance on the fan's read path (§10 row 7), which is the
   cost the row exists to remove. Fire 2's purge already applied this posture to 12,245 checks with your
   authorization; this makes it the standing rule, one check at a time.
2. **Mechanism decision I made, recorded for one-look review — the op's actor guard flips from "refuses Loom and
   Weaver" to "refuses Loom".** Fire 2's cold review refused both engines because *"a platform engine never
   supersedes a check"*. This design makes Weaver the legitimate durable submitter; every guard the op evaluates
   (ownership of the older check, family, subject, recency, aliveness) is proven from Processor-hydrated keys,
   never from the caller, so a forged payload buys nothing (§6). Loom stays refused: it mints instances and
   never retires them. One residual the flip re-arms is stated in §6 and is dormant today: an **AI-authored**
   weaverTarget could also drive this op, because lease-signing is not a platform-protected package — bounded to
   "retire a genuinely superseded owned check at a moment of its choosing", and behind `BRIDGE_CAPABILITY_AUTHOR`.
3. **Observation, not a decision this design needs (§11.4).** With Fire 1's 30-day window, `missing_bgcheck`
   stays armed for every application whose unit is **not yet leased, or** whose landlord decision is approved
   (`lenses.go:959`, both disjuncts), so each such applicant is re-checked at the vendor every 30 days for as
   long as the application row exists. Supersession keeps the live instance set at one per applicant; it does
   not touch that vendor cost. A LoftSpace product question for the PO lane; named here, not filed.

---

## 1. The row, clause by clause

The board row (filed by the parent's Fire 1 build note, 2026-09-03):

> *"Supersede a background check automatically when its successor completes — The primitive exists
> (`TombstoneSupersededLeaseServiceInstance`, operator-driven, ownership-checked); the durable rule needs the
> reply op to find the prior instance without a live enumeration, and a product answer on check history."*
> `📐 needs designer pass · no-pattern: prior-instance discovery at reply time without a live enumeration`

And the parent's own words (§5 verbatim): *"(D) the durable rule — a check superseded by its successor is
tombstoned by the reply op, one live instance per subject per pattern — is filed 📐 on the Lattice lane;
`SupersedeClause` (semantic-contracts) is the shape precedent, and whether a superseded check's history is a
record is the product question the design must answer."*

| Clause | Verdict |
|---|---|
| "the primitive exists … operator-driven, ownership-checked" | Confirmed: `packages/lease-signing/scripts.go:195-339` (seven dispatcher-declared reads; refuses `primordialActor["loom"]` and `["weaver"]`, `:215-216`). **Ownership is proven for the older instance only** — the successor is checked alive / same class / completed / later / same subject, never for its `instanceOf` (`:257-317`); §4.3 records this as a shipped residual and the lens closes it on Weaver's path. |
| "the durable rule needs the **reply op** to find the prior instance" | **Refuted as a premise (§3).** The reply op cannot know the *subject*: the bridge posts `{externalRef, status, result}` only (`internal/bridge/dispatch.go:245-249`), attaches no `contextHint.reads` unless `replyOpReads` lists the op (`:85-98` — two Augur/capability ops, not this one), and the op is deliberately read-free (`scripts.go:344-362`). "At reply time" names a moment, not a mechanism. |
| "without a live enumeration" | Honoured. The only enumeration that would find a prior instance is the subject's inbound `providedTo` fan — the population that reached 3,637 on one identity (§7.1) — and every batched shape of it was refuted on the pinned substrate ([kv-links-listing-leg-collapse-design.md](kv-links-listing-leg-collapse-design.md) §4). No enumeration anywhere here: the fact is projected by a lens (P5); every key the op reads is declared or derived (§4.3). |
| "one live instance per subject per pattern" | Delivered for the **completed background-check population minted by this package** (§5). Failed and in-flight instances are not superseded — the op refuses them (`scripts.go:279-285`) and a failed verdict is the record `declined_bgcheck` reads (`lenses.go:967`). Payment instances are out of scope by construction: `missing_payment` closes permanently on the first completed payment (`payComplete > 0`), so they never accumulate. |
| "`SupersedeClause` is the shape precedent" | Not the shape here. `SupersedeClause` mints a replacement inside one op that knows both keys because the caller names the clause. Here the successor is minted by Loom's instanceOp with no knowledge of the predecessor, and both complete asynchronously through a read-free bridge reply. The precedent that fits is **convergence lens + `directOp`** — `staleUserTasks → CancelTask` (`targets.go:170-180`). |
| "a product answer on check history" | Answered above ("For Andrew" 1) and in §4.4. |

## 2. Grounding ledger (every claim cites the line that does the thing)

| Fact | Where |
|---|---|
| The instance is minted by Loom's externalTask instanceOp with `{instanceKey, subjectKey, adapter, replyOp, params}`; root data `{}`; `instanceOf` link to this DDL's meta; `providedTo` link to the applicant. | `scripts.go:112-193`; `patterns.go` (`backgroundCheck`, subject `identity`) |
| Weaver's `triggerLoom` hands Loom `{patternRef, subjectKey, instanceId}` — no prior-instance knowledge reaches the pattern. | `internal/weaver/strategist.go:198-210` |
| The reply op is bridge-submitted, payload-only, read-free; stamps `.outcome{status, completedAt, validUntil}` create-only; `completedAt = time.rfc3339_utc(op.submittedAt)`. | `scripts.go:344-472`; `internal/bridge/dispatch.go:85-98, 245-249` |
| **`rfc3339_utc` formats with `time.RFC3339` — whole seconds.** Two replies committing in one second stamp equal `completedAt`. | `internal/starlarksandbox/modules.go:81` |
| The readiness fan reads **every** `providedTo` instance of the applicant, no WHERE, and counts fresh completed bgchecks; `renewalComplete` re-derives the same fan. | `lenses.go:815-818`; `renewal_lenses.go:266-279` |
| `backgroundCheckFreshness` is instance-anchored, one hop, `EmptyBehavior: delete`; its target declares **no gaps** so its timer leg runs on every delivery. | `lenses.go:853-866, 150-162`; `targets.go:182-200` |
| **A fired `@at` reads the row back; a retracted row is a soft-tombstone body, so the "absent row" drop is never taken; `currentFreshUntil` finds no `freshUntil` and the firing proceeds to submit `MarkExpired`.** | `internal/weaver/temporal.go:260-302, 342-358`; `internal/refractor/pipeline/sweep.go:290` |
| The shipped op: seven REQUIRED reads; guards in order; bare tombstones of root + two links; event `lease.serviceInstanceSuperseded`. Recency guard is `not (succ_completed_at > inst_completed_at)` — a tie refuses. | `scripts.go:195-339` (`:287-296`); descriptor `ddls.go:596-725` |
| The op's grant is `operator` / `Scope:"any"`; Weaver and Loom hold `operator`. | `permissions.go:86-90`; `internal/bootstrap/primordial.go:488-494` |
| `derive_reads(op)` — Contract #2 §2.5 class (g): pure, payload-only, merged at the head of step 4; `ddl` and `state` are **failing** bindings there. A derived key the envelope also declares keeps the envelope's disposition. | `internal/processor/derive_reads.go:83-125, 170-180, 314-316, 539-541`; precedents `clinic-domain/ddls.go:2660`, `objects-base`, `wellness-domain`, `identity-domain`, `cafe-ledger`, `identity-hygiene` |
| An update/tombstone of a key hydrated at step 4 is conditioned on that revision (Contract #3 §3.2). | `internal/processor/commit_path.go:455-460, 662-672` |
| Weaver `directOp`: `params` are `row.<column>` templates plus an injected `expectedRevision`; `reads`/`optionalReads` are `row.<column>` or `row.<column>.<aspect>`; any string column value passes install (`orchestrationguard.go:383-385`) and load (`registry.go:840-846`); `Class` names the DDL. | `internal/weaver/strategist.go:295-360, 879-937`; precedent `targets.go:170-180` |
| A `directOp` gap with neither companion column falls back to a 3-**dispatch** budget then raises `GapBudgetExhausted`; Weaver publishes fire-and-forget and consumes no reply, so the issue carries no rejection reason — the requestId is logged at submit. | `docs/contracts/10-orchestration-weaver.md:69-76`; `internal/weaver/actuator.go:113-117`; `evaluator.go:659, 745, 2023` |
| Marks and the per-(entity, column) count are level-cleared on any delivery whose `missing_<col>` is not true, and swept by the deletion leg on a tombstone; `deleteDispatchCount` on close. | `internal/weaver/evaluator.go:61-70, 109, 1282` |
| **An actorAggregate lens receives an aspect CDC event as a fan-out unless its patterns cannot bind the aspect's parent type** — binding `meta` makes every `vtx.meta.*` aspect write a seed; a package install/upgrade rewrites each DDL meta's aspects. | `internal/refractor/pipeline/dispatch.go:78-86`; `internal/pkgmgr/build.go:126-146` |
| A tombstoned anchor retracts its read-model row as a tombstone **body** under the same key (not a KV DEL). | `internal/refractor/pipeline/audit.go:774-799`; `sweep.go:290`; observed §7.2 |
| The full engine projects `r.key` off a bound relationship variable at no read cost; **a relationship variable inside an aggregate is refused** (`max(r.key)`); `max`/`min` order strings; `(x <> null)` is the null test; a later `MATCH` may re-bind an earlier node variable. | `ruleengine/full/relbinding.go:7-24, 454, 495, 512-519`; `aggregate.go:103-106`; **spikes §7.3** |
| The DDL meta vertex carries `.canonicalName` with `data.value`; install occupancy is by **key**, not by canonical name, so two packages may declare the same DDL name. | `internal/pkgmgr/build.go:126-134`; `installer.go:1342-1360` |
| AI-authored weaverTargets are barred only from ops declared by `platformProtectedPackages`; lease-signing is not in the list. | `internal/pkgmgr/authored_dispatch_scope.go:20-32`; `capabilityapply.go:258-268` |
| Anchor derivation across a 2-hop walk through `identity` has a shipped, census-pinned precedent (`renewalComplete`). | `internal/refractor/actor_walk_scope_corpus_census_test.go:161`; `anchor_hopindex_corpus_census_test.go:148` |

## 3. Re-deriving the need — the `no-pattern:` was solution-shaped

The row prescribes *"prior-instance discovery at reply time"*: an event-correlation shape. It needs the subject
(the reply op does not have it), an index from subject to prior instance (a pointer aspect, two writers, a CAS —
§10 row 4), and it couples FR58's once-only terminal write to a second entity's tombstone (a guard failure on the
predecessor would reject the batch that completes the externalTask and park Loom's token on a check the vendor
already answered).

The fact the rule acts on is **level-triggered and already in the graph**: *instance A is completed, and some
instance B on the same subject, of the same class, owned by the same type authority, is completed later (or in the
same second with a greater key).* That is a pure function of the subgraph around A — what a lens projects (P5) —
and Weaver's convergence loop is the platform's named invariant-enforcer for enumerate-then-write shapes
(Contract #2 §2.5 (e): *"the invariant-enforcer is Weaver detect+recover"*). Nothing about the fact needs the
completion *event*; a coalesced or re-ordered delivery changes nothing because the row is re-derived from the
level. The missing primitive dissolves: a lens, a target, and two small edits to the shipped op.

## 4. The shape

### 4.1 Lens `supersededBackgroundChecks` (`packages/lease-signing/lenses.go`)

Instance-anchored `actorAggregate` in the `backgroundCheckFreshness` shape (`{key: $actorKey}`, `EmptyBehavior:
delete`, `weaver-targets`), projecting **only violating rows** — an anchor with no qualifying successor yields
zero rows, so no standing row exists per live check:

```cypher
MATCH (inst:service {key: $actorKey})-[own:instanceOf]->(m:meta)
  WHERE inst.class = 'service.backgroundCheck.instance'
    AND inst.outcome.data.status = 'completed'
    AND m.canonicalName.data.value = 'leaseServiceInstance'
MATCH (inst)-[:providedTo]->(id:identity)<-[:providedTo]-(newer:service)-[:instanceOf]->(m)
  WHERE newer.class = 'service.backgroundCheck.instance'
    AND newer.outcome.data.status = 'completed'
    AND ((newer.outcome.data.completedAt > inst.outcome.data.completedAt)
      OR ((newer.outcome.data.completedAt = inst.outcome.data.completedAt) AND (newer.key > inst.key)))
WITH inst.key AS entityKey, own.key AS instanceOfLink, id.key AS subjectKey, max(newer.key) AS supersededBy
RETURN entityKey AS actorKey, entityKey, subjectKey, instanceOfLink, supersededBy,
  True AS missing_retirement, True AS violating
```

- **Anchor predicate = the op's precondition on `instanceKey`**: completed, bgcheck class, owned by a
  `leaseServiceInstance` type authority. (Residual, §11.3: the anchor's meta is chosen by *name*; install does not
  refuse a same-named DDL in another package, so such a package's instances would anchor here and the op would
  refuse them `NotOwned` — loud, per row, `GapBudgetExhausted`. No such package exists; the successor side below
  is pinned by identity regardless.)
- **Successor predicate = the op's precondition on `supersededBy`, plus one it lacks**: same class, completed,
  later — with the tie broken on key, and **re-bound to the anchor's own meta `(m)`**, so a same-shaped instance
  from another type authority never supersedes ours (spike §7.3 c). The op today does not prove the successor's
  ownership (§4.3 c); on Weaver's path the lens does.
- **Ordering.** `completedAt` is `rfc3339_utc` from one op — fixed-width, so string order is time order — at
  **second** granularity, so equality is reachable (two replies for one applicant in one second: Weaver's
  admission paces `backgroundCheck` at 2/s, `targets.go:111`). The tie-break `(completedAt equal AND newer.key >
  inst.key)` is total and deterministic; §4.3 (b) puts the identical rule in the op. Without it a tie leaves two
  live checks forever with no row, no gap, no signal — the reviewer's finding 3.
- **`supersededBy` = `max(newer.key)`**: a deterministic member of the qualifying set; *any* member satisfies the
  op. The column is documented as *a* later owned completed sibling, not "the newest".
- **`instanceOfLink` = `own.key`** — the one key the op needs that `derive_reads` cannot compute (§4.3); the
  engine builds it from the adjacency entry the walk already holds (`relbinding.go:7-9`). The successor's
  `instanceOf` key cannot be projected the same way — a relationship variable inside `max(…)` is refused
  (spike §7.3 c) — which is why the successor's ownership is enforced by the pattern, not handed to the op.
- Output descriptor mirrors `staleUserTasks` (`lenses.go:122-136`): `AnchorType: "service"`,
  `OutputKeyPattern: "supersededBackgroundChecks.{actorSuffix}"`, `BodyColumns: [violating, missing_retirement,
  entityKey, subjectKey, supersededBy, instanceOfLink]`, `EmptyBehavior: "delete"`, `KeyColumn: "entityId"`. No
  `freshUntil`, no companions — the engine's 3-dispatch default is the right budget: every rejection this row can
  produce is a lens⇔op inconsistency worth a standing `GapBudgetExhausted`.
- **Cost class, two triggers.** (i) A `service`/`identity` write reprojects the anchor and, through the 2-hop
  derivation, its siblings: one `instanceOf` hop, one `providedTo` hop, one fan of the subject's *live* instances
  (steady state one to two — this rule keeps it there; today the seven keepers' fans of ≤2, §7.1).
  (ii) **Binding `meta` makes every `vtx.meta.*` aspect write a fan-out seed** (`dispatch.go:83` skips by parent
  type only): a package install/upgrade rewrites its DDL metas' aspects, and the fan from the
  `leaseServiceInstance` meta walks its **live** inbound `instanceOf` edges (adjacency returns none for a
  tombstoned link — Fire 2 tombstoned the links, so the 12,245 retired instances are not reached) — i.e. every
  live owned instance, once per rewritten aspect. Bounded by the live population this rule bounds; priced in
  §11.1. Every other meta vertex has no `instanceOf` inbound, so its writes cost one adjacency lookup.
  `backgroundCheckFreshness` binds only `service` and has no such trigger. Labels are exhaustive (`service`,
  `meta`, `identity`; relations `instanceOf`, `providedTo`), so the consumer filter derives narrowed.

### 4.2 Target `supersededBackgroundChecks` (`targets.go`)

```go
{
    TargetID:    "supersededBackgroundChecks",
    Description: "A completed background check that a later completed check on the same applicant, minted by this package, has superseded is retired, so every live view aggregates only over the current check. OPERATOR NOTE: a GapBudgetExhausted here means the lens and TombstoneSupersededLeaseServiceInstance disagree about a pair; Weaver logs the requestId at submit — read the rejection off the Contract #4 tracker (vtx.op.<requestId>) or the Processor log, fix, then reset-budget.",
    LensRef:     "supersededBackgroundChecks",
    Gaps: map[string]pkgmgr.GapActionSpec{
        "missing_retirement": {
            Action:    "directOp",
            Operation: "TombstoneSupersededLeaseServiceInstance",
            Class:     "leaseServiceInstance",
            Params:    map[string]string{"instanceKey": "row.entityKey", "supersededBy": "row.supersededBy", "subjectKey": "row.subjectKey"},
            Reads:     []string{"row.instanceOfLink"},
        },
    },
}
```

`Reads` carries exactly the key `derive_reads` cannot; the six others arrive by derivation (§4.3). Weaver injects
`expectedRevision` into `params` as for every `directOp` (`strategist.go:298`); the op's `InputSchema` is open
and the script reads only its three named fields — inert here, exactly as for `CancelTask`.

### 4.3 The op — three edits (`scripts.go`, `ddls.go`, `permissions.go`, version bump)

**(a) `derive_reads(op)` — the six payload-shaped keys become class (g).** Pure arithmetic on
`{instanceKey, supersededBy, subjectKey}`, the same `parts_of` derivation `execute` performs. `optional_string`
is not defined in this script today (it lives in the reply script and orchestration-base); define it locally:

```python
def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    return v.strip()

def derive_reads(op):
    # Contract #2 §2.5 class (g). Every key below is a pure function of the
    # payload -- the same derivation execute() performs -- so no dispatcher
    # restates it. The seventh read (the instanceOf ownership link) needs
    # ddl[...].metaKey, which this pre-pass deliberately cannot reach: it stays
    # the dispatcher's declaration (Weaver: row.instanceOfLink; an operator:
    # the key the descriptor spells out).
    if op.operationType != "TombstoneSupersededLeaseServiceInstance":
        return {}
    p = op.payload
    inst = optional_string(p, "instanceKey"); succ = optional_string(p, "supersededBy"); subj = optional_string(p, "subjectKey")
    if inst == None or succ == None or subj == None or inst == "" or succ == "" or subj == "":
        return {}   # execute()'s required_string raises the real InvalidArgument
    ip = inst.split("."); sp = succ.split("."); jp = subj.split(".")
    if len(ip) != 3 or len(sp) != 3 or len(jp) != 3:
        return {}
    return {"reads": [inst, succ, inst + ".outcome", succ + ".outcome",
                      "lnk.service." + ip[2] + ".providedTo.identity." + jp[2],
                      "lnk.service." + sp[2] + ".providedTo.identity." + jp[2]]}
```

A malformed payload derives nothing and lets `execute` raise its own `InvalidArgument` (objects-base / clinic
precedent). A derived key a dispatcher *also* declares keeps the envelope's disposition, so the existing test
harness that declares all seven stays valid. The six `# read-posture: (a) declared reads at … dispatch`
annotations become `# read-posture: (a) reads — derived server-side by this script's own derive_reads(op)` (the
clinic form, `ddls.go:2601`); the seventh keeps its dispatcher wording.

**(b) The recency guard gains the tie-break** the lens applies: `succ_completed_at > inst_completed_at`, **or**
equal and `superseded_by > instance_key` (`scripts.go:295-296`). The two rules must be textually the same
predicate; the pinned lens test's tie vectors (§11.2) are the drift detector.

**(c) The actor guard admits Weaver.** `scripts.go:215-216` becomes: refuse `op.actor == primordialActor["loom"]`
only, with the comment rewritten (Weaver is the durable submitter through the §4.2 target; an operator or trusted
tool stays admitted; Loom mints and never retires). `TestTombstoneSupersededLeaseServiceInstance_PlatformEngineDenied`
splits into Loom-refused and Weaver-accepted vectors. **Shipped residual, unchanged by this design:** the op does
not prove the *successor's* ownership (`scripts.go:257-317`) — an operator could name a same-class instance from
another type authority as `supersededBy`. Weaver never can (the lens re-binds `(m)`); closing it for the operator
path needs an eighth declared read the lens cannot project (§4.1), so it stays a documented operator-path residual
in the descriptor text rather than a mechanism.

Descriptor `Description`/`Examples` (`ddls.go:596-725`) and the permission `Note` (`permissions.go:88`) are
rewritten to the new contract (one dispatcher-declared read; Weaver-or-operator submitter; the tie rule; the
successor-ownership residual). The stale comment at `scripts.go:1281` ("Template-less (no instanceOf)" — the op
mints `instance_of_lnk` at `:163`) is corrected in the same touch. `manifest.yaml` + `package.go`:
`0.31.27 → 0.31.28` (`lint-package-version`).

### 4.4 Read path, write path, orchestration, precedents

- **Read (P5):** Weaver reads the lens's `weaver-targets` rows; nothing reads Core KV.
- **Write (P2):** one op, unchanged in its mutations, submitted through `core-operations` by Weaver's service actor
  under the existing `operator` grant.
- **Orchestration:** a Weaver convergence target with a `directOp` gap — level-triggered, idempotent by
  reprojection, budgeted, loud on exhaustion. No Loom pattern, no `@at`, no reply-op change.
- **History posture:** body-preserving tombstone at rest + `lease.serviceInstanceSuperseded` on `core-events`;
  invisible to every live lens. Loupe's Core KV inspector still reads the retired body.

## 5. State-lifetime table (rows; the OUTCOME column is the test-vector list for §11.2)

| # | State of instance A (anchor) and its subject S | Row for A | Dispatch | Outcome |
|---|---|---|---|---|
| 1 | A in flight (no `.outcome`) | none | — | untouched |
| 2 | A completed; S has no other owned completed bgcheck | **none** (second MATCH binds nothing → zero rows) | — | A is the current check; no standing row, no delivery |
| 3 | A completed at T1; B (owned, same class) completed at T2 > T1 | row, `supersededBy: B` | op(A, B, S) | A's root + 2 links tombstoned; A's freshness row and this row retract; readiness/renewal fans read B only. **Timer residue:** if A's `@at` was still armed (B completed *before* A's window lapsed — an operator re-run or a tie; on the ordinary path A lapsed first, which is what minted B, so the timer is already spent), it fires up to 30 days later, finds the freshness row's tombstone body (`temporal.go:262` is not taken), submits `MarkExpired` against A's tombstoned root, which the marker DDL's required root read refuses — **one rejected, fire-and-forget op, no retry**. Priced as acceptable; the platform-side fix (clear the schedule on a tombstone delivery) is not this package's to build and has no other consumer. |
| 4 | The mirror of 3 — B's own row | none (nothing later than B) | — | B stays |
| 5 | A completed; B **failed** later | none | — | A stays current; `declined_bgcheck` is `bgFailed>0 AND freshBgComplete=0`, a fresh A keeps the application converged; a lapsed A re-opens `missing_bgcheck` as today |
| 6 | A **failed**; B completed later | none (anchor: completed only) | — | A persists as the declined record; the op would refuse it anyway |
| 7 | A completed; B completed later but `service.payment.instance` | none (class conjunct) | — | never cross-family — the op's `WrongClass` twin. Payment is out of scope entirely: `missing_payment` closes permanently on the first completed payment, so payments never accumulate |
| 8 | A completed but its `instanceOf` targets a foreign meta or `service.<templateId>` | none (`m.canonicalName` / label `meta`) | — | never a row the op would refuse `NotOwned` (residual: a same-*named* foreign DDL, §11.3) |
| 8b | A completed; a later same-class instance F exists but its `instanceOf` targets a **different** meta | none (`(newer)-[:instanceOf]->(m)` re-binds the anchor's meta) | — | a foreign check never retires ours (spike §7.3 c) |
| 9 | A completed at T1; B and C both later | `supersededBy: max(B.key, C.key)` | op(A, that one) | valid either way |
| 9b | A and B completed in the **same second** | exactly one of them projects a row: the one with the smaller key, `supersededBy` = the greater | op(smaller, greater, S) | one survivor; the op's mirrored tie-break accepts |
| 10 | Row 3 projected; **B is tombstoned** (superseded by C) before A's dispatch commits | stale row names B | op → `UnknownInstance` (B's root hydrates as a tombstone; `vertex_alive` false) | dispatch count 1/3; B's tombstone is A's 2-hop neighbour → A reprojects with `supersededBy: C` → next dispatch succeeds → row deleted → marks and count swept (`evaluator.go:69, 109`) |
| 11 | Row 3 projected; **A is tombstoned concurrently** (an operator ran the op by hand) | — | op → `UnknownInstance` | A's tombstone retracts the row; count swept |
| 12 | Row 3 projected; A's root **updated** between step 4 and step 8 | — | `RevisionConflict` on the tombstone (`commit_path.go:455`) → re-hydrate → retry | at most one committed tombstone |
| 13 | **Never-written**: the seven keepers left by Fire 2 and every instance minted since | none today (§7.1: no keeper has a live owned completed sibling) | — | zero dispatches at install; the first retirements arrive with the first 30-day re-checks |
| 14 | Package reinstall / lens rebuild / Refractor restart | rows re-derived from the level; the meta-aspect rewrite reprojects every live owned instance once per aspect (§4.1 ii) | as rows say | idempotent |
| 15 | Weaver restart mid-episode | row re-delivered (level); marks in Weaver state | re-dispatch collapses on the mark or re-submits within the default budget | the op is idempotent on a retired A (row 11) |
| 16 | S's identity tombstoned / erased | `(inst)-[:providedTo]->(id)` finds no live hop → no row | — | nothing retires; as today |
| 17 | Target disabled by an operator | rows keep projecting; nothing dispatches | — | superseded checks accumulate again; the OPERATOR NOTE says so; re-enable resumes from the level |

**No new stateful mechanism**: the row is a projection; the episode marks are Weaver's existing
per-(target, entity, column) state with their existing lifetimes.

## 6. Concurrency and security envelope

- **Ordering.** Every key the op writes was hydrated at step 4 from the declared/derived set, so the tombstones are
  conditioned on the revisions read (Contract #3 §3.2). Two dispatchers racing on one pair serialise; the loser
  re-hydrates a tombstone and rejects `UnknownInstance`.
- **View freshness.** Weaver evaluates the *row*, a projection of an earlier level; the op re-evaluates every
  conjunct on the Processor's OCC snapshot. A stale row can only produce a rejection, never a wrong tombstone.
- **Actor.** Weaver submits as its service actor (`internal/weaver/actuator.go:96`), authorized by the existing
  `operator`/`any` grant. The op derives every trust-bearing key from the payload and from `ddl[…].metaKey`, never
  from a caller-supplied id: a forged `subjectKey` derives a `providedTo` key that never existed → `HydrationMiss`;
  a foreign `instanceKey` fails the ownership read; `supersededBy` must be alive, same class, same subject, later.
  Admitting Weaver changes who may *ask*; it does not change what can be proven. Permission envelope unchanged.
- **What the removed refusal was carrying (reviewer finding 4).** Today the Weaver refusal is the only thing
  stopping an **AI-authored** weaverTarget (an `ai-target-*` capability artifact with its own lens) from driving
  this op: `authored_dispatch_scope.go` bars authored targets only from ops declared by `platformProtectedPackages`,
  and lease-signing is not one. Post-flip residual: an authored target could retire a *genuinely superseded, owned*
  check on any applicant at a time of its choosing — every conjunct is still proven by the op, so the reach is
  timing, not content. Dormant (`BRIDGE_CAPABILITY_AUTHOR` is not `real`; the admission-model row is 🗄️ shelved).
  Recorded, not mechanised: adding lease-signing to the protected list would also bar authored targets from
  `SetListingStatus`/`AttachObject`, a wider decision than this row's.
- **Single Weaver instance today**; two would race the same row and serialise at the Processor (row 12).

## 7. Executable censuses (run 2026-09-06 on the shared dev stack; re-run at Phase 0)

### 7.1 Population per applicant — Core KV keys (tombstones included) and the read model

```sh
nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk kv ls core-kv > /tmp/k.txt
grep -cE '^vtx\.service\.[^.]+$' /tmp/k.txt                                   # 12303 service roots (live + tombstoned bodies)
grep -cE '^lnk\.service\.[^.]+\.instanceOf\.meta\.' /tmp/k.txt                 # 12296 lease-signing-owned
grep -cE '^lnk\.service\.[^.]+\.instanceOf\.service\.' /tmp/k.txt              # 7 service-domain (foreign shape, row 8)
grep -E '^lnk\.service\.[^.]+\.providedTo\.identity\.' /tmp/k.txt | awk -F. '{print $NF}' | sort | uniq -c | sort -rn | head -8
#   3638 edu97ixj2CJB6auNi6L4 · 1891 dzst9ZB6Q8Jhw4m9hHVG · 1627 LQ28Dp37vajbdTerZvij · 1450 ocZv1PtnocWiy37gcwbn
#   1327 FZJzSE5MdsKpm3eUTi2F · 1169 mBLYTedU9KkJ92CiY2vp · 1164 MQsmTTAgNkngkdEjQz9L · 2 gk12KRqMMVwjVwfxsb5c
```

`kv ls` lists every subject with a value, and a Lattice tombstone is a body — so these are the *purged* counts.
Sample of 12 roots (every 1,000th): **11 `service.backgroundCheck.instance` `isDeleted: true`, 1
`service.laundry.template` live** — consistent with Fire 2's 12,245 tombstones and 7 keepers. **Expected at
Phase 0:** the new lens projects **zero** rows (no keeper has a live owned completed sibling). **Falsify:** any
row at install means a keeper has one that Fire 2's planner missed — retire it through the target and record it.

### 7.2 The read model, and what a retraction leaves behind

```sh
nats … kv ls weaver-targets | grep -c '^backgroundCheckFreshness\.'   # 12252 keys
# every ~1200th row read raw: 10 × {isDeleted:true, projectedAt, projectionSeq}, 1 live {actor, entityKey, freshUntil:null, …}
```

A retracted row is a tombstone **body** under the same key, so the bucket's subject count does not shrink with
the population, and a fired `@at` that reads the row back finds a body, not `ErrKeyNotFound` (§5 row 3). Recorded
so nobody reads 12,252 as "the purge did not retract". The violating-rows-only shape (§4.1) adds no standing
subjects of its own; each retirement leaves one more tombstone body here, as every `EmptyBehavior: delete` lens does.

### 7.3 Engine spikes — the lens compiles and projects exactly the state table

Throwaway tests in `internal/refractor/ruleengine/full/` (fixture: identity S; instances all `providedTo` S;
outcomes as aspects; one or two meta vertices with `.canonicalName`), each run through `parseExec` and deleted:

- **(a) draft shape** (OPTIONAL MATCH fan, `max(CASE …)`, `own.key`): A→B, B→null, failed/in-flight/payment
  anchors → no row. PASS.
- **(b) violating-rows-only + tie-break** (non-optional second MATCH, `(a > b) OR (a = b AND key > key)`,
  `max(newer.key)`): expectations derived from the fixture — A→B; B→ *its* later sibling; the same-second pair
  T1/T2 → exactly one row, smaller key names the greater; failed / in-flight / payment → no row. PASS. It also
  **found finding 12 itself**: a same-class instance under a *different* meta superseded B because the draft's
  successor had no ownership conjunct.
- **(c) owned successor**: `(newer)-[:instanceOf]->(m)` (re-binding the anchor's meta) and the equivalent
  `(m2:meta) WHERE m2.key = m.key` both PASS — the foreign-meta, same-*named* successor no longer qualifies, and B
  projects no row. Projecting the successor's link key as `max(sown.key)` is **refused by the engine**
  (*"relationship variable … used as a value"*, `relbinding.go:454`), which is why §4.3 (c) leaves the operator-path
  residual rather than adding an eighth read.

Provenance of each row listed the anchor, the meta, the subject and every sibling — the reprojection set the hop
index must reach. **Inc 2 ships (b)+(c) as one pinned test** (§11.2).

### 7.4 Corpus pins the new lens joins (read the verdict off each test's failure, never guess)

`internal/refractor/{label_derivation,actor_onekey,actor_walk_scope,anchor_hopindex,grouping_reduction}_corpus_census_test.go`,
`branch_decomposition_corpus_census_pins_test.go`; gates `lint-gap-column-declaration` (`missing_retirement` ⊆
gaps), `lint-lens-anchors`, `lint-conventions` (read-posture annotations), `lint-package-version`,
`verify-package-lease-signing` (DDL→ops unchanged). **Relationship-variable projection has no corpus precedent**
(`grep -rnE '\[[a-zA-Z]+:[a-zA-Z]+\]' packages/*/lenses*.go` → only a comment in identity-domain); the engine
pins it (`rel_projection_test.go`), and the branch-decomposition / grouping censuses will classify `own.key` for
the first time — their verdicts are the build's to record, not a reason to avoid the shape.

## 8. Contract surface — none

- **Builds to Contract #2 §2.5 class (g)** (`derive_reads` — "the one class of declarable key a submitter cannot
  express: a key derived from the payload by the package's own semantics").
- **Builds to Contract #10 §10.8 `directOp`** (`reads?` = "bare vertex keys, each a literal or `row.<column>`"). A
  link key in `reads` is already what Fire 2's purge and the op's tests declare, and both Weaver gates pass it
  (§2). If Andrew reads "bare vertex keys" as excluding link keys, the touch-up is one word in §10.8 and a doc
  correction, not a behaviour change — flagged, not staged.
- Contract #1 key shapes unchanged; no new aspect, link, or vertex type.

## 9. Reconciliation with the mental model

- *Didn't we already handle this?* Fire 2 shipped the **op** and ran it once by hand. Nothing submits it durably —
  the parent's §6 says so ("Not in this fire: the reply-op rule").
- *Doesn't `backgroundCheckFreshness` already anchor on the instance?* Yes, and its target is gap-less **on
  purpose** (`targets.go:182-190`): the timer leg runs before any gap column is read, and its OPERATOR NOTE
  documents that disabling it fails freshness *open*. A retirement gap on that target would make one operator
  switch govern two unrelated failure modes and put the sibling fan into every freshness reprojection. A second
  lens over the same anchors costs one more hop-indexed pipeline and keeps each target's OPERATOR NOTE true.
- *Does this add state we keep elsewhere?* No. The "current check" is not recorded anywhere; every reader derives
  it (`freshBgComplete`, `bgcheckValidUntil`) and now this lens does the same.
- *Design-of-record?* The parent's §2 row D and §5 "(D)"; the Weaver dossier's "classify by shape" rule is honoured.

## 10. Alternatives

| # | Option | Priced | Verdict |
|---|---|---|---|
| 1 | **Do not have this thing.** Leave superseded checks live. Growth after Fire 1: +1 instance per **applicant with an application on a not-yet-leased unit or an approved decision** per 30 days (`lenses.go:959`; `maxretries_bgcheck` never bounds it because the count is deleted on every close, `evaluator.go:1282`) — on today's dev stack that is every applicant with an open or approved application, ~12 instances per such applicant per year. The readiness fan and `renewalComplete`'s aggregate grow linearly per applicant; Loom's `instance.<id>` cursor accumulates the same count (its own ratified row). Fire 2's purge would need re-running by hand roughly yearly. | No code; one operator ritual; a slowly re-forming version of the parent's harm at ~1/300th of the runaway rate. | Rejected — the demand is nameable (three readers on the fan; `leaseApplicationComplete` is the lens the parent could not drain), the fix is ~140 lines of package content, and it removes a standing manual purge. |
| 2 | **The reply op supersedes inline** (the row's prescription). | Needs the subject (not in the bridge payload), a subject→prior index (row 4), a `replyOpReads` entry, and couples the FR58 terminal write to a second entity's guards: a predecessor rejection would reject the completion and park Loom's token. | Rejected — wrong layer (§3). |
| 3 | **The instanceOp supersedes at claim time.** | The predecessor is equally unknown at claim, and the old check must stay valid until the new one *completes*. | Rejected. |
| 4 | **A "current check" pointer aspect on the identity**, written by the reply op. | A new aspect DDL on another package's vertex type; the reply op must first discover its subject (a class-(e) degree-1 `kv.Links`), two writers per subject need a CAS epoch; recorded state that can drift from the derived truth every other reader uses. | Rejected — more state, a live read on the bridge path, a second source of truth. |
| 5 | **Extend `backgroundCheckFreshness`** with the fan + a `missing_retirement` gap. | Fewer lines. Costs: the freshness reprojection gains the sibling fan; the gap-less target whose OPERATOR NOTE is load-bearing becomes a dispatching one. | Rejected narrowly (§9). Revisit at a third instance-anchored concern. |
| 6 | **Seven row-projected keys, no `derive_reads`.** | Refuted by spike (c): the successor's link keys cannot be projected through an aggregate; the shape is not expressible. | Rejected — and class (g) exists for exactly this, with six package precedents. |
| 7 | **Mark, don't tombstone** — a `.supersededBy` aspect the readiness fan skips. | Keeps history *live*; but a marked instance is still read by every fan before it is filtered — the cost the row exists to remove stays; every reader's CASE must learn the marker. | Rejected on cost; the product answer (history at rest) is what makes tombstoning correct. |
| 8 | **Enumerate in the op** — a class-(e) `kv.Links` on the subject's inbound `providedTo`. | The unbounded fan under the 250 ms wall, refuted for the confinement walk on every batched transport. | Rejected — the row's own "without a live enumeration". |
| 9 | **A platform primitive** ("prior-instance discovery" on Loom/externalTask). | Single consumer; every branch reduces to "a lens already expresses the fact". | Rejected — the mechanism dissolves (§3). |
| 10 | **The draft's standing-row shape** (OPTIONAL MATCH; a row per live completed check with `supersededBy: null`). | One permanent `weaver-targets` subject per live check beside `backgroundCheckFreshness`'s, delivered to Weaver's per-delivery bookkeeping (`evaluator.go:61-113`) for a row that can never dispatch. | Rejected for §4.1's violating-rows-only shape (reviewer finding 6; spike (b)). |
| 11 | **Disarm the freshness `@at` at retirement** (clear the schedule on a tombstone delivery, `temporal.go`). | A Weaver change for one rare, harmless rejected op (§5 row 3). | Rejected — no second consumer; recorded as the residue. |

**Dead-scaffolding test:** both increments realise value on the day they land (row 13: zero dispatches at install;
the first retirements arrive with the first 30-day re-checks Fire 1 guarantees). **Rejections in combination:**
rows 2 and 4 were priced together; row 5's objection was run against the recommendation (the new target's
OPERATOR NOTE names its single failure mode); row 6's absence was re-verified by spike, not assumed.

## 11. Migration, tests, risks

### 11.1 Migration / adoption
`make reinstall-package PKG=packages/lease-signing` on a running stack: the lens hot-reloads, the target
registers, the DDL script upgrades in place (`0.31.28`). No wipe, no restart, no backfill. **Cost of the install
itself:** the DDL metas' aspect rewrites (`build.go:126-146`) seed the new lens's fan from the
`leaseServiceInstance` meta over its live `instanceOf` inbound edges — every live owned instance (seven today),
once per rewritten aspect, ~4 aspects → ~30 two-hop evaluations; the 12,245 tombstoned instances are unreachable
(their `instanceOf` links are tombstoned). At a real population of N live checks the same install costs ~4N
evaluations, each a few ms. Every subsequent package upgrade pays the same; acceptable, and bounded by the
population this rule bounds.

### 11.2 Test strategy — every prescribed test is owned by an increment
- **Inc 1 (op):** `tombstone_superseded_instance_test.go` — (i) a submission declaring **only** the `instanceOf`
  link succeeds (derive_reads supplies six); (ii) declaring all seven still succeeds (weakest-wins merge); (iii) a
  malformed payload derives nothing and rejects `InvalidArgument` from `execute`; (iv) Weaver's actor accepted,
  Loom's refused (split of `_PlatformEngineDenied`); (v) **tie vectors**: equal `completedAt` with
  `supersededBy > instanceKey` accepted, the reverse refused `NotSuperseded`; (vi) every existing negative vector
  unchanged.
- **Inc 2 (lens + target):** `lens_cypher_test.go` — §7.3 (b)+(c) as one pinned test over the real fixture (rows
  1–9b of §5 including 8b's foreign-meta successor under a same-named meta); the six corpus census pins re-read;
  `lint-gap-column-declaration`; an ephemeral-stack e2e (`lease_signing_test.go` shape): seed A completed T1 and B
  completed T2 through the real ops → Weaver retires A → A's `supersededBackgroundChecks` and
  `backgroundCheckFreshness` rows are tombstone bodies, B's freshness row stands, `leaseApplicationComplete`'s
  `freshBgComplete` is 1. **Mutation tests:** drop the `completedAt >` conjunct from the lens and row 4 must fail
  (B would name A); drop the `-[:instanceOf]->(m)` re-bind and row 8b must fail; drop the tie-break and row 9b must
  fail (neither projects).
- **Live close (MERGED ≠ RUNNING):** rows in `weaver-targets` under `supersededBackgroundChecks.` (zero expected,
  §7.1); a hand-seeded pair on a test applicant retired within one Weaver delivery; `health.weaver` carries no
  `GapBudgetExhausted` for the target.

### 11.3 Risks
- **Relationship-variable projection is new to the corpus.** Engine-pinned and spiked three ways; the census
  verdicts are unknown until run. Inc 2's Phase 0 runs the six pins first and records the verdicts in the build note.
- **Lens⇔op predicate drift** (a future edit to one guard, not the other) shows up as `GapBudgetExhausted` per
  affected row — loud, per entity, operator-resettable, but **reason-less** (Weaver consumes no reply): the
  OPERATOR NOTE points at the requestId + Contract #4 tracker. The pinned test's rows 5–9b are the drift detectors.
- **A same-named `leaseServiceInstance` DDL in another package** would anchor its instances here and pair them
  with each other; the op refuses each `NotOwned` → `GapBudgetExhausted` per row. No such package exists; install
  does not refuse the name. Named, not mechanised.
- **`max` picks a non-newest successor** (row 9): harmless for the op; the column is documented accordingly.
- **The `@at` residue** (row 3): one rejected fire-and-forget `MarkExpired` per early-superseded check, ≤30 days
  after retirement. Priced acceptable; §10 row 11.

### 11.4 Named, not designed here
The standing 30-day re-check on every open-on-unleased-unit or approved application ("For Andrew" 3; the shipped
op's successor-ownership residual (§4.3 c); and the Loom `instance.<id>` cursor's own accumulation
(`loom-instance-enumeration-bounding-design.md`, ratified, sequenced) are adjacent and untouched.

## 12. Decomposition for the Steward — one fire, two increments (size M → honest S+S)

| Inc | Content | Posture | Green |
|---|---|---|---|
| **1** | `scripts.go`: `derive_reads` + local `optional_string`, tie-break, actor guard, annotations, the `:1281` comment; `ddls.go` descriptor text + Examples; `permissions.go` Note; version `0.31.28`; tests §11.2 (i–vi) | **Posture-changing** (an engine actor admitted to a tombstoning op; §6 is the argument, incl. the authored-target residual) — the Steward sizes review depth | `go test ./packages/lease-signing/ -run TombstoneSuperseded -count=1`, `lint-conventions`, `lint-package-version`, `verify-package-lease-signing` |
| **2** | `lenses.go` lens + output descriptor; `targets.go` target; census pins; pinned lens test + mutation tests; e2e; live close | Package content | `go test ./packages/lease-signing/ ./internal/refractor/ -run 'Census|Lens|Cypher|Superseded' -count=1`, `lint-gap-column-declaration`, `lint-lens-anchors`, `lint-board`; `make reinstall-package` + the §11.2 live close |

Inc 2 depends on Inc 1 (the target's one-key `Reads` needs the derivation; the lens's tie rule needs the op's).
Ship in that order, one worktree.

## 13. Checklist walk and review record

§2.3 walked item by item against the draft before the adversarial pass. Items that changed the draft: **A — "the
demand is a hypothesis"** (the reply-op premise refuted from `dispatch.go`); **B — "name the transport"**
(`derive_reads` binds `ddl` failing, found by opening `derive_reads.go:170-180`); **C — "run the census you
write"** (§7.1's raw counts were tombstones; the body sample corrected the headline); **D — "write the state table
before the predicate"** (rows 8 and 10 added the `m.canonicalName` conjunct and the stale-row path); **F — "a
`no-pattern:` prescription is solution-shaped"** (§3).

**Adversarial pass (2026-09-06, one cold reviewer, read-only, briefed to falsify §3, §5, §6, §10 row 1) — 11
findings, all verified against code by this fire before folding:**

| # | Finding | Disposition |
|---|---|---|
| 1 | MAJOR — a retracted row is a soft-tombstone body; the fired `@at` never takes the "absent" branch and submits `MarkExpired` against a tombstoned root | **Confirmed** (`temporal.go:260-302`); row 3 rewritten with the residue priced; §10 row 11 |
| 2 | MAJOR — the growth population is "not-yet-leased OR approved", not "approved"; `maxretries_bgcheck` never bounds it | **Confirmed** (`lenses.go:959`; `evaluator.go:1282`); "For Andrew" 3 and §10 row 1 restated |
| 3 | MAJOR — `completedAt` is second-granular; a tie leaves two live checks forever, silently | **Confirmed** (`modules.go:81`); tie-break on key in both lens and op (§4.1, §4.3 b), row 9b, spike (b), test (v) |
| 4 | MAJOR — the Weaver refusal was the only bar on an AI-authored target driving this op | **Confirmed** (`authored_dispatch_scope.go:20-32`, `capabilityapply.go:258`); stated in §6 and "For Andrew" 2; dormant |
| 5 | MAJOR — binding `meta` makes every meta aspect write a fan-out seed; an install reprojects every live owned instance | **Confirmed** (`dispatch.go:78-86`); priced in §4.1 (ii) and §11.1; bounded by the live population |
| 6 | MAJOR — the draft left a standing row per live check | **Adopted**: violating-rows-only shape (§4.1), spike (b); §10 row 10 |
| 7 | MINOR — `canonicalName` pins a name, not the meta identity; install does not refuse duplicates | **Confirmed** (`installer.go:1342-1360` is key occupancy); anchor-side residual §11.3; successor side pinned by identity (spike c) |
| 8 | MINOR — `optional_string` undefined in this script | **Fixed** (§4.3 a defines it locally) |
| 9 | MINOR — the OPERATOR NOTE promised a rejection reason Weaver does not carry | **Fixed** (§4.2 note points at the requestId + Contract #4 tracker) |
| 10 | MINOR — "the completed population" overclaimed; no payment row | **Fixed** (§1; row 7) |
| 11 | NOTE — stale comment `scripts.go:1281` | **Folded** into Inc 1's touch list |
| 12 | *(found by spike (b) in this fire)* — the draft's successor had no ownership conjunct; a same-class foreign-DDL instance superseded an owned one | **Fixed**: `(newer)-[:instanceOf]->(m)` (spike c); the op's matching residual on the operator path recorded (§4.3 c) |

**Held under attack** (the reviewer's own words, kept): the reply op cannot know the subject; `derive_reads`
cannot reach `ddl`; weakest-wins merge; a link key in `directOp` `Reads` passes both gates; `expectedRevision`
is inert; the stale-successor path rejects `UnknownInstance`, not `HydrationMiss`; the foreign-meta exclusion by
label binds on the key type segment.
