# Weaver — the reconciler's work set is the projection, not the residue of past dispatches

> **🗄️ HELD — not ratified** (*Andrew, ratification session 2026-08-27; adjudicated by Winston in the
> same session*). The problem is real and the DD held up (every §2 citation re-verified at review;
> §17 stands) — but the **shape is refused**: no new enumerator on top of the per-target durables.
>
> **Why held.** Andrew's standing doctrine: a new mechanism built to patch a gap left by the previous
> mechanism is evidence the base design should be re-derived, not extended. Review DD confirmed the
> suspicion structurally: §11's alternatives **A** (periodic durable re-create) and **B** (Nak the
> declines) were each rejected on grounds the *other* one solves — B "cannot reach rows declined before
> the change" (A's re-create reaches exactly them); A is "unpaced" and cannot build the observed-column
> set (under standing Nak-retry the latch self-heals by level re-raise, dissolving the need). **The
> combination was never priced, and it is the substrate-native shape: JetStream is the enumerator and
> the retry engine.** No new durable, no cursor/cycle/budget state, no walk; steady-state cost is
> O(stuck rows) instead of the sweep's perpetual O(all rows)/5min.
>
> **The redesign is RATIFIED and supersedes this shape:**
> [weaver-decline-retry-substrate-native-design.md](weaver-decline-retry-substrate-native-design.md)
> (✅ Andrew, 2026-08-27) — it delivers the direction below **as corrected by Andrew the same
> day**: item 2's per-boot replay was too strong (a durable rebuild is a **manual, Loupe-invoked
> verb**, never a standing per-boot mechanism), and the decline taxonomy splits on where the fix
> can come from (data errors Ack with a standing issue; only config errors ride the Nak loop).
> This doc stays as the row-sweep record and the fallback — **revive trigger: the redesign's §2
> V7 KV history-1 pin fails.**
>
> **Replacement direction (named for the redesign):**
> 1. `handleRow`'s transient/data-error decline exits return **`NakWithDelay`** (per-class backoffs,
>    `MaxDeliver` unbounded — the substrate omits the bound at ≤0) instead of Ack.
> 2. Lane-1 durables adopt the **registry's own per-boot-nonce replay** (`registry.go:26-34` documents
>    the pattern as deliberate): every boot re-delivers the current row set — heals the already-stranded
>    population and makes `contraction.go:22-25`'s replay claim *true* instead of deleted.
> 3. `dispatchGap`'s `NumDelivered > 1` blanket re-fire branch **retires** — redelivery becomes routine,
>    so lease-gated `reclaim` becomes the sole re-fire authority (a simplification in itself).
> 4. The `gapConfig:` latch stays per-entity-cleared; an over-eager clear self-heals within one backoff
>    period because a still-open row's Nak loop re-raises it.
>
> **Phase 0 of the redesign (keystone, vendor-grounded per `docs/vendors.md`, NATS 2.14):** the exact
> semantics of pending/Nak'd redelivery state when per-subject compaction erases the message (KV
> overwrite of the key). If that semantics does not cooperate, **this design revives as the fallback**
> — its DD is done (two adversarial passes + the 2026-08-27 ratify-session spot-checks, all green).
>
> **Carried forward into the redesign:** the §8 severity question (standing re-raised issues have the
> same package-typo-pins-`unhealthy` property — argue it there, same recommendation stands); census C2
> (which decline class actually stranded the clinic 26 — the boot-race narrative was corrected at
> review: consumers are created from registry callbacks, so the arrival-direction race does not exist;
> `GapWithoutPlaybook` under an earlier package version is the leading hypothesis); the §13 fixture and
> mutation disciplines, which apply to the Nak shape's tests unchanged.
>
> The body below is the held design, kept as the record of the row-sweep shape and its grounding.

**Author:** Winston (Designer fire, 2026-08-27).
**Board row:** `[Weaver] The sweep enumerates state keys, not declared×projected work` — ★★★
(`_bmad-output/planning-artifacts/backlog/lattice.md`). **Size: M → L** on the fold (§14).
**Demand:** [lattice-designer-triage-2026-08-27.md §5](../../docs/reviews/lattice-designer-triage-2026-08-27.md).
**Blocks:** `verticals.md` — *29 of 63 appointments carry no site and `clinicSiteBackfill` closes the
gap for none*.

---

## 1. Problem + intent

Weaver's operating law is the **liveness invariant** (Contract #10 §10.8,
`docs/contracts/10-orchestration-weaver.md:225-232`): every `violating` row is eventually
**discharged**, **excluded**, or **escalated**, and *"the reconciler sweep … [is] jointly the
enforcement of this one invariant."*

The reconciler sweep is the only mechanism that revisits a row after its lane-1 delivery is acked. Its
pass is one call — `keys, err := e.conn.KVListKeys(ctx, e.cfg.WeaverStateBucket)`
(`reconciler.go:187`) — and every key in that bucket is written by a dispatch or an operator verb: a
§10.3 mark (`state.go:121`), a `__count` budget (`state.go:327`), an `__effect` window
(`state.go:533`), the `__control` marker (`state.go:751`). **The sweep's work set is the residue of
work already started.**

Lane 1 is the only path that ever starts one, and it **Acks** what it declines. `handleRow`
(`evaluator.go:21-201`) has seven Ack-and-return exits before any dispatch:

| # | line | Condition | Class |
|---|---|---|---|
| 1 | `:27` | key is not `<targetId>.<entityId>` | permanent |
| 2 | `:34` | target not in the registry at delivery time | **transient** — replay lag, install order |
| 3 | `:43` | row body does not parse | permanent while the data is wrong |
| 4 | `:93` | empty body — the entity-deletion tombstone | correct |
| 5 | `:115` | target carries the `__control` freeze | **transient** — until enable |
| 6 | `:120` | `violating` reads false, *including a non-bool column, which `boolColumn` surfaces as `RowDataError` and reads false* | permanent while the data is wrong |
| 7 | `:131` | `entityKey` echo missing | permanent while the projection is wrong |

(`:56` and `:104` are the two `NakWithDelay` exits — a `clearClosedMarks` and a `scheduleFreshness`
failure.)

A lane-1 Ack is **final**. The durable is `weaver-target-<targetId>` — stable-named, **no per-boot
nonce** (`engine.go:18`, `:414-425`) — with `DeliverLastPerSubject`, and `ConsumerSupervisor.Stop()`
preserves durables (`consumer_supervisor.go:385-411`). The registry source deliberately does the
opposite (`registry.go:441-445`), and `registry.go:26-34` records why:

> JetStream only honors DeliverPolicy when a durable is **first created** — `CreateOrUpdateConsumer`
> against an EXISTING durable of the same name resumes from its persisted ack floor regardless of the
> DeliverPolicy requested.

So a declined row is re-examined only if the lens re-projects it — and a row that is stuck *because*
nothing is remediating it is, by construction, the row that never changes. **A quiet violating row
gets exactly one evaluation, ever.**

Three filed symptoms, one root:

1. **★★★ — a never-dispatched gap is invisible forever.** Live evidence: 26 of 28 violating
   `clinicSiteBackfill` entities never dispatched once.
2. **★ — the `gapConfig:` health latch can only be retired per-entity.** Its sole
   column-stopped-being-reported clear is inside `clearClosedMarks` (`evaluator.go:884`), and that
   clear's **own doc comment names the missing capability** (`:874-882`): *"only a walk of the
   candidate set — which the sweep, holding one mark, does not have — observes a column leaving rather
   than one entity's gap ending."*
3. **Observability — the contraction monitor reads ~0 after every warm restart.**
   `contraction.go:22-25` claims lane-1's replay re-derives the count; that is false for a warm restart,
   by the very semantics `registry.go:26-34` documents one file over. The counts are in-memory
   (`contraction.go:26-31`), nothing rebuilds them, and `sample()` (`reconciler.go:246`) snapshots zero
   indefinitely — for exactly the standing-violation population the metric exists to expose.

**Intent.** Make the reconciler's work set what the invariant already promises. And note symptom 2's
comment: the codebase already identified *"a walk of the candidate set"* as the missing thing. This
design is that walk.

---

## 2. Grounding ledger

Pinned to the code that *does* the thing, never a comment that describes it — except rows 12 and 16,
where the comment **is** the artefact under review.

| # | Fact | Citation |
|---|---|---|
| 1 | The sweep pass is one whole-bucket `KVListKeys(weaver-state)`; every leg routes from it | `reconciler.go:187`, `:196-231` |
| 2 | `listed` is that raw key set, consumed by one leg (`sweepCount` arm (c)) for one membership test | `reconciler.go:192-195`, `:553-557` |
| 3 | `weaver-targets` key = `<targetId>.<entityId>`, one dot; `targetId` forbids dots, `entityId` is a bare NanoID | `evaluator.go:1451-1461`, `registry.go:652-658` |
| 4 | `KVListKeysPrefix` / `KVListKeysFilter` are **server-side** subject-filtered lists over the KV stream | `substrate/kv.go:234-252`, `:282-317` |
| 5 | `KVListKeysFilter` pages **client-side**: it drains the whole filtered set, then sorts/de-dups/slices. Paging bounds the caller's downstream value reads, **not** the key enumeration | `substrate/kv.go:282-317`, `pageFilteredKeys` `:321-338` |
| 6 | `KVGetMulti` / `KVGetMultiNoSnapshot`: ≤1024 matched subjects take an identical atomic Direct-Get fast path; past it the former double-drains (fails whenever any key moves), the latter drains once | `substrate/kv_multi.go:192`, `:241` |
| 7 | Weaver **already** creates durable consumers on `KV_weaver-targets` and already `KVGet`s that bucket from `sweepCount` — both paths are proven live, not merely permitted. `protectedStreamDenies` denies STREAM verbs only | `engine.go:414-418`, `reconciler.go:552`, `natsperm/matrix.go:368-378`, `:464-474` |
| 8 | Weaver already batches a KV read via `KVGetMulti` (`seedDisabledTargets`). *`KVGetMultiNoSnapshot`'s only existing caller is Refractor's adjacency read* — §4.3 argues its use here on its own merits, not on precedent | `control.go:44-79`, `kv_multi.go:241` |
| 9 | `weaver-targets` removals are **hard** deletes (`EmptyDelete` → `kv.Delete`), surfaced as an empty-body CDC message; no in-body `isDeleted` in this bucket | `refractor/adapter/natskv.go:311-319`, `evaluator.go:37-38` |
| 10 | Below `handleRow`'s preamble the message is read for exactly two things: `msg.Sequence` (`:172`, `:270`, `:305`) and `msg.NumDelivered` (`:346`). **Verified exhaustively across every callee** — `clearClosedMarks`, `scheduleFreshness`, `openGapColumns`, `gapSuppressed`, `dispatchGap`, `planGap`, `admitGap`, `fireEpisode`, `escalateExhaustedGap`, `shadowCompare`, `staleMark`, `augurEscalation` — none takes the message, subject, consumer, or delivery context | census C3, §12 |
| 11 | `warmedUp()` gates every decision acting on *absent* registry evidence, and `sweepCount`'s arm-(n) **dispatch** | `reconciler.go:1253-1256`; gates at `:388`, `:398`, `:823`, `:835`, `:696-700` |
| 12 | `contraction.go:22-25`'s restart claim is **false for a warm restart** — stable durable name, `Stop()` preserves it, so `CreateOrUpdateConsumer` resumes from the ack floor | `contraction.go:22-25` (the claim); `engine.go:414-425` + `consumer_supervisor.go:385-411` (the refutation); `registry.go:26-34` (the same semantics, documented correctly) |
| 13 | `contractionStats` has three call sites — `observe`, `sample`, `snapshot`. Nothing gates a decision on it | `evaluator.go:66`, `reconciler.go:246`, `health.go:467-471` |
| 14 | `observe` is **transition-based** over a `known` set (`if was == violating { return }`), so a second caller cannot double-count | `contraction.go:49-64` |
| 15 | `issueKeyGapConfig` = `gapConfig:<targetID>.<col>`, an **in-memory `issueCache`** key. **Three** codes: `GapWithoutPlaybook` (`error`, `:229`), `UnresolvedReference` (`warning`, paced, `:577`), `PlaybookConfigError` (`error`, paced, `:585`). `planGap`'s `errData` arm raises a **different** family — `TemplateDataError` at `issueKeyTemplateEntity` (`:581`) | `evaluator.go:1553`, `:1599-1601`, `:229`, `:577`, `:581`, `:585` |
| 16 | The sole column-stopped-being-reported clear is `evaluator.go:884`, inside `clearClosedMarks`, and its doc comment (`:874-882`) states the reason it lives there: *no walk of the candidate set exists* | `evaluator.go:874-884` |
| 17 | `target.Gaps` is the orphan-delete authority for `__effect` **only**; mark/count orphaning is row-column driven, and `sweepCount` **declines** to delete a count whose column left the playbook | `reconciler.go:399` vs `:319`, `:583`, `:618-624` |
| 18 | Mark per-key TTL = `2 × lease` (**1 h** at the 30 m default); count TTL = `256 × lease` (**≈128 h**). `sweepCount` arm (n) fires only for `count == 0`; `reclaim` requires a mark | `state.go:44`, `:64`, `reconciler.go:691`, `:817` |
| 19 | Exhaustion requires `count >= capN, capN ≥ 1`; `getDispatchCount` returns 0 on not-found. **A row with no `__count` key can never be exhausted** | `evaluator.go:1252-1256`, `:1165`, `state.go:307-310` |
| 20 | `Revoke` deletes from `e.targets` (the engine's consumer-fingerprint map), **not** from `e.source` — the target stays registered and keeps appearing in `targetIDs()`; safety comes from the re-written `__control` marker | `control.go:203-217`, and its own comment at `:208-214` |
| 21 | The sweep durable sets `MaxAckPending: 1` and **no `AckWait`** ⇒ JetStream's 30 s default; `consumer_supervisor.go:605` overrides only when `spec.AckWait > 0`, and `consumer.go:149-160` documents that a handler exceeding AckWait *"is redelivered WHILE STILL RUNNING"* | `sweep_schedule.go:42-54`, `consumer_supervisor.go:605`, `consumer.go:149-160` |
| 22 | `warmPass` runs as an in-process goroutine at start (`engine.go:373`), outside the durable, and its own comment concedes it may overlap a fired pass | `reconciler.go:168-171`, `engine.go:373` |
| 23 | `admitGap` is called from `planGap` — *"the ONE seam both fresh-dispatch legs share"* — and its token bucket + pending queue are **per target and shared** across legs | `evaluator.go:527`, `docs/components/weaver.md:688-690` |
| 24 | `aggregateStatus` computes over the full issue set, so any `error` ⇒ `unhealthy`; `boundIssues` sorts `error` first by explicit design; `issueCache.issues` has **no cap** (only `paced` is pruned) | `health.go:~530`, `:616-623`, `:130-145`, `:253` |
| 25 | 26 production `meta.weaverTarget` specs across 13 packages; exactly one declares an `Admission` block (`leaseApplicationComplete`) | census C1, §12; `packages/lease-signing/targets.go:81-83` |
| 26 | `defaultSweepInterval = 1m` (`:21`), `defaultMarkLease = 30m` (`:17`), `defaultSweepOrphanWarmup = 5m` (`:25`) | `reconciler.go`; clamps at `engine.go:157-184` |

---

## 3. What the sweep can and cannot see today

| Leg | Iterates | Reaches a never-dispatched row? |
|---|---|---|
| `sweepMark` (`:273`) | mark keys | No — a mark **is** a past dispatch |
| `reclaim` (`:817`) | continuation of `sweepMark` arm (e) | No — requires a mark |
| `sweepCount` (`:532`) | `__count` keys | No — a count **is** a past dispatch |
| `sweepEffect` (`:365`) | `__effect` keys | No |
| `contraction.sample` (`:246`) | nothing (snapshots in-memory state) | No |

**Every leg is downstream of a dispatch.** And ledger row 18 exposes a second, sharper hole *inside*
the existing set: for ~127 hours a `__count` key can outlive its mark, and in that window
`sweepCount` declines (`count != 0`) while `reclaim` has no mark to work from. **A row with a live
count, no mark, and an open gap is owned by nobody today.** §4.4's skip predicate is written against
this fact; the first draft's predicate preserved the hole, which is why it changed.

Two corrections to the demand row as filed:

- **"Relax `sweepCount`'s `count.Count != 0`" is unimplementable as a fix.** It is the first of five
  gates on arm (n), and it distinguishes an operator re-arm (`ResetRetryBudget`, the only writer that
  persists a literal `0`) from a mid-chain gap whose pacing belongs to `reclaim`'s backoff ladder.
  Relaxing it mints a fresh `claimId` outside that ladder — the duplicate-userTask hazard the Weaver
  dossier's first entry records. And it changes nothing about the symptom: **there is no key to
  iterate.**
- **`target.Gaps` is not "the orphan authority on the `__effect` and mark families."** `__effect` only
  (ledger row 17).

---

## 4. The shape — a row sweep on its own schedule

### 4.1 Why its own durable, not another leg in `pass`

The first draft added a leg inside `sweeper.pass`. Both adversarial passes broke it on the same two
facts, and they are decisive:

- **`sweepSpec` sets no `AckWait`, so the handler runs under JetStream's 30 s default** (ledger row
  21). `handleSweepFired` calls `pass` synchronously. A leg doing 26 filtered lists, 26 batched
  multi-gets and up to N dispatches does not reliably fit in 30 s, and a handler that overruns *"is
  redelivered WHILE STILL RUNNING."*
- **`warmPass` runs outside the durable** (ledger row 22), so `MaxAckPending: 1` does not prevent
  overlap. The existing legs are safe under overlap because they hold no cross-pass state; the row
  sweep does (§5), and two interleaved `cursor` advances **skip a page** — which then hands
  `finishCycle` a partial observed set and retires an `error` latch whose column is open on the unread
  page. That is §4.6's own named worst case, reached by the sweep's documented concurrency rather than
  by a coding slip.

So the row sweep gets **its own `@every` schedule and durable**, mirroring `sweep_schedule.go`'s
shipped pattern exactly (`ScheduleEvery` → `schedule.weaver.rowsweep` → `.fired` → the
`weaver-row-sweep` durable), with:

- an explicit **`AckWait`**, set to `2 × RowSweepInterval` and floored well above the pass's bound —
  the ledger-row-21 hazard is the one this consumer must not inherit;
- **`MaxAckPending: 1`**, so one replica never self-overlaps;
- **`RowSweepInterval`**, default **5 minutes** — deliberately slower than the mark sweep's 1 minute,
  because the cost profile is nothing like the existing legs' (§7);
- **no warm-pass equivalent.** A cold start's first row pass arrives one interval later; the mark
  sweep's warm pass exists because a lease can expire during a restart, and nothing analogous applies
  here.

Within one process the leg is therefore serial by construction, and §5's state needs no lock beyond
the guard against a cross-replica read (which is per-process state anyway — see §5's concurrency
paragraph).

### 4.2 The walk

```
for each targetID in e.source.targetIDs():
    if !warmedUp()            : skip                      # registry replay lag
    if isTargetDisabled(tid)  : drop leg state; skip       # freeze AND revoke (ledger row 20)

    marks := markKeySet(KVListKeysPrefix(weaver-state, tid+"."))   # STATE AS MEMORY
    keys, next := KVListKeysFilter(weaver-targets, tid+".>", cursor[tid], pageSize)
    entries    := KVGetMultiNoSnapshot(weaver-targets, keys)

    # WALK A — bookkeeping. Whole page, above every guard, unconditional.
    for key in keys: observed[tid] |= openMissingColumns(entries[key])
                     cycle[tid]    |= violatingIf(entries[key])

    # WALK B — dispatch. Budgeted per target.
    for key in keys:
        if anyMarkFor(marks, tid, entityId): continue      # sweepMark/reclaim own it
        if budget[tid] == 0                : continue      # deferred, not lost
        evaluateRow(..., src = rowFromSweep)

    cursor[tid] = next                                     # ALWAYS advances
    if next == "": finishCycle(tid)                        # §4.5, §4.6
```

Five decisions, each an application of a ledger row rather than a preference.

**(a) Enumerate registered targets, and skip disabled ones outright.** `targetIDs()` is the set
`contraction.sample` already walks. A `__control`-frozen target can dispatch nothing, so walking it
buys nothing — and per ledger row 20 a **revoked** target *stays registered*, so without this skip the
leg would pay a filtered list plus a multi-get for a revoked target's entire row population every pass,
forever, with zero possible output. Skipping both is one predicate. §6 row 8's win survives: on
`Enable` the marker clears and the very next row pass walks the target and dispatches — which is
precisely what `Enable` cannot do today.

**(b) `KVListKeysFilter` with the `<targetId>.>` filter.** Server-side filtered to one target, and it
returns the cursor the paging needs. **The paging bounds the value reads and the dispatch burst, not
the key list** — the list is drained whole (ledger row 5). §7 prices that rather than eliding it.

**(c) `KVGetMultiNoSnapshot`, page clamped ≤1024.** At or under the cap both entry points take the
identical atomic fast path, so the choice is inert at the default; it matters only if an operator raises
the page size, where `KVGetMulti`'s double-drain *"fails, hard, whenever ANY matched key moves between
the passes"* — the normal condition on a busy target. The leg's read set is exactly the shape that doc
comment sanctions: **independent facts, each valid alone**, every one re-gated by the mark CAS-create
before anything dispatches. A stale-revision read cannot cause a wrong dispatch; it can only lose a race
it was never authoritative in. (Ledger row 8: this is the argument, not a precedent claim.)

**(d) State as memory, at mark granularity.** One `KVListKeysPrefix(weaver-state, tid+".")` per target
per pass — bounded by that target's in-flight episodes and budgets, not by its rows — from which the leg
builds the set of keys `splitMarkKey` accepts. **The skip is mark presence for this entity at any gap
column, and nothing else.** The first draft skipped on *any* weaver-state key under the entity prefix;
that was wrong three ways and each was blocking: a live `__count` with no mark is owned by nobody
(ledger row 18) so the skip preserved the hole it exists to close; the granularity mismatched (an entity
with a stale count at gap A would be skipped for a newly-violating gap B); and `__effect` keys carry no
`<entityId>` segment at all (`<tid>.__effect.<col>.<ref>`), so that clause described an impossible
membership. Mark presence is the thing that actually means *"an episode is in flight and `reclaim` owns
it."*

**(e) The cursor always advances; the dispatch budget never blocks it.** Walk A and Walk B are separate
for exactly this reason. If deferral held the cursor back, a target with a chronic backlog would never
reach `next == ""`, so `finishCycle` would never run — the contraction rebuild would never install, the
`gapConfig:` retirement would never fire, and `sample()` would skip the target permanently. That is
symptom 3 *inverted* by its own fix, on precisely the large diverging targets the metric exists for. A
row whose dispatch was deferred is simply re-encountered next cycle, within the published horizon.

### 4.3 Re-use, with the seam named

The Weaver dossier is unambiguous: *"A NEW dispatch seam inherits that classifier and the pacing built
on it, not just the gates above it."* So the leg reaches dispatch through lane 1's own code —
`handleRow`'s body below the preamble is extracted into

```go
type rowSource int   // rowFromLane1 | rowFromSweep

func (e *Engine) evaluateRow(ctx context.Context, target *Target, targetID, entityID string,
    row map[string]any, sequence uint64, firstDelivery bool, src rowSource) substrate.Decision
```

**and the extraction is deliberately not "verbatim".** The first draft claimed it was; both reviewers
falsified that, and the honest answer is a named seam with a table rather than a claim:

| Step in `handleRow`'s body | lane 1 | row sweep | why |
|---|---|---|---|
| `clearClosedMarks` `:55` | runs | **skipped** | `sweepMark` `:319` and `sweepCount` `:583` already reconcile marks/counts against the row every mark-sweep pass; re-running it would also re-enter the per-entity `gapConfig:` clear §4.5 replaces — and its failure path is `NakWithDelay` (`:56`), which the leg has no message to Nak |
| `contraction.observe` `:66` | runs | **skipped** — Walk A accumulates cycle-scoped instead | a mid-page `observe` would feed `current` from a partial cycle (§4.6) |
| `entityKey` raise/clear `:84-90` | runs | runs | level-driven both ways |
| `scheduleFreshness` `:103` | runs | runs, **`freshUntil`-carrying rows only** | a row lane 1 never delivered never armed its `@at` timer — the temporal lane's instance of the same root |
| `isTargetDisabled` `:114` | returns Ack | unreachable — §4.2(a) skipped the target | |
| `openGapColumns` → dispatch `:135+` | runs | runs | the whole point: same classifier, same suppression gate, same `admitGap`, same `surface` arm, same augur escalation, same mark CAS-create |

`sequence = entry.Revision` — `dispatchGap`'s own comment states the identity (*"the backing-stream
sequence IS the KV revision"*, `:265-268`), and the zero-sequence guard at `:270` is inert for a live KV
entry. `firstDelivery = true`, **always**: `msg.NumDelivered != 1` is what makes `dispatchGap` blanket
re-fire *every* in-flight gap on the row (`:207-215`), which is `reclaim`'s job, gated on an expired
lease. With no mark, `found = false`, so the fresh-`claimId` stale branch (`:643-689`) is unreachable and
a CAS-create losing to a concurrent lane-1 dispatch drops correctly (`:702-705`).

What the seam buys is that the **dispatch decision** — the part that can mint a duplicate task or a
duplicate vendor call — is the same code, reached from a second enumerator. T7 is narrowed to that claim
(§13), because that is the claim that is true.

### 4.4 One new issue the leg raises that lane 1 does not

`handleRow` logs and Acks an unparseable body (`:43`) without raising anything. For a *delivered* row
that is defensible — the log names it and a re-projection will fix it. For a row the leg re-reads every
cycle, silence is the defect being fixed, so §6 row 2 raises `RowDataError` at `issueKeyDataEntity`.
This is a genuine behavioural addition, named here rather than hidden inside "identical by
construction", and it is bounded by §8's per-target overflow cap.

### 4.5 The observed-column set, and the clear that moves

The `gapConfig:` latch is target-scoped and its only column-close clear runs per entity — and that
clear's own doc comment says why: *no walk of the candidate set exists* (ledger row 16). This design is
that walk, so **`evaluator.go:884`'s `issues.clear(issueKeyGapConfig(...))` moves out of
`clearClosedMarks` and into the leg's `finishCycle`.** The removal is in scope and is load-bearing: left
in place, lane 1 would still retire the target-scoped latch the instant *any one* entity's column
closes, firing far more often than the new clear and rendering it near-inert — symptom 2 would not be
closed. `retireClosedGapIssues` (per-entity, `:883`) stays exactly where it is.

The replacement: Walk A unions `openMissingColumns(row)` — every `missing_*` key reading `true` — into
`observed[tid]`. At `finishCycle`, for every live `gapConfig:<tid>.<col>` whose `col` is not in the
completed cycle's observed set, `issues.clear(...)`.

**Walk A is where it belongs, and that placement is the fix for a real defect.** In `handleRow`,
`openGapColumns` sits *below* the `entityKey` guard (`:131`) and below `isTargetDisabled` (`:114`). If
the union were computed inside `evaluateRow`, then a target whose only rows holding column X open are
rows missing their `entityKey` echo would yield `observed = {}` for X, and `finishCycle` would clear an
`error` latch while the column is open on every row — and that population is *exactly* the reported
clinic shape. The dossier's rule is the general statement: **every RETIRE belongs above every "cannot
act" GUARD.** Walk A reads the raw body and is above all of them.

Why the predicate retires all three codes soundly:

| Code | Raiser | Reachable with the column open on zero rows? |
|---|---|---|
| `GapWithoutPlaybook` | `dispatchGap` `:229` | No — raised only while evaluating a row whose column is open |
| `UnresolvedReference` | `planGap` `:577` | No — `planGap` is reached only from an open column |
| `PlaybookConfigError` | `planGap` `:585` | No — same |

(`planGap`'s `errData` arm raises `TemplateDataError` at `issueKeyTemplateEntity`, `:581` — a
**different** family, per-entity, with its own clear discipline. It is not governed by this retirement
and §6 row 13 names it separately.)

**Be honest about the character of that guarantee: it holds because all three *shipped* raisers happen
to be open-column-gated, not because anything structurally forces a raiser at this key to be.** A fourth
raiser added later would silently invalidate the target-scoped clear with a success signal on the
retirement. That is why census C4 ships as a **pinning test** (T8): the invariant is real but
unenforced, and the gate that enforces it ships in this design.

Three constraints on the clear, all load-bearing:

1. **Only at cycle completion, never mid-page** — a partial observed set drops a live `error` issue.
2. **After the cycle's own evaluations**, so a column this cycle re-raised is in the observed set.
3. **Only when the cycle was COMPLETE and CLEAN.** A list error, a `KVGetMultiNoSnapshot` failure, or
   any unparseable body in the cycle marks it *incomplete*: `finishCycle` advances the cursor and rebuilds
   nothing, and the retirement is skipped. §15's first draft covered only a list error; a failed
   multi-get and a corrupt body are separate, equally sufficient routes to an incomplete observed set,
   and all three now take the same fail-safe branch — a latch survives longer, never retires early.

### 4.6 The contraction rebuild

`observe` is transition-based and idempotent (ledger row 14), but a *partial* page must never feed
`current`. So Walk A accumulates `cycle[tid]`, and at a **complete, clean** `finishCycle` the target's
partition of `known` and its `current[tid]` are **replaced** — a rebuild, not an incremental blend.
`sample()` skips a target with no completed cycle since process start, so the trajectory ring never
holds a sample drawn from an unbuilt count. Lane 1's `observe` continues on every delivery and keeps the
count current *between* cycles; the rebuild is the periodic ground-truth correction the doc comment
always claimed a replay provided. **`contraction.go:22-25`'s restart sentence is deleted and replaced in
the same increment** — an affirmatively wrong comment about the mechanism that backstops it is not left
standing next to a design that depends on the correction.

---

## 5. State-lifetime table

| State | Created | Reset | Carried | Ordered | Restart | Disabled / revoked target | Target unregistered |
|---|---|---|---|---|---|---|---|
| `cursor[tid]` | first row pass reaching the target after warm-up | to `""` at `finishCycle` | across passes within a cycle | lexicographic, strict-`>` (`kv.go:321-338`) | in-memory ⇒ lost ⇒ cycle restarts from the top; a re-scan is idempotent and the mark CAS-create re-gates every dispatch | **evicted** by §4.2(a)'s skip | evicted when the id leaves `targetIDs()` |
| `cycle[tid]` | at cycle start | rebuilt each cycle | within a cycle | set | lost ⇒ no sample until the next complete cycle | evicted | evicted |
| `observed[tid]` | at cycle start | rebuilt each cycle | within a cycle | set | lost ⇒ no `gapConfig:` retirement until the next complete cycle — **fail-safe** | evicted | evicted |
| `cycleClean[tid]` | true at cycle start | per cycle; cleared by any list/multi-get/parse failure (§4.5 constraint 3) | within a cycle | n/a | lost ⇒ next cycle re-decides | evicted | evicted |
| `cycleComplete[tid]` | false at process start | set at the first clean `finishCycle`; never cleared | process lifetime | n/a | false ⇒ `sample()` skips, so no zero-sample is recorded | evicted | evicted |
| `budget[tid]` | at pass start, per target | every pass | not carried | consumed in page order | n/a | n/a | n/a |

**Eviction is explicit, not implied.** Every map is swept at the top of each row pass against the
current `targetIDs()` minus the disabled set; the first draft asserted entries were "dropped" with no
mechanism, and got the `Revoke` semantics wrong besides (ledger row 20). Without the sweep these leak
across target churn.

**Concurrency.** §4.1 gives the leg its own durable with `MaxAckPending: 1` and no warm-pass
counterpart, so **within one process there is exactly one row pass at a time** — the interleaved-cursor
hazard that broke the first draft is designed out rather than locked around. Across replicas each
process holds its own cursor and cycles independently: correct, not a bug, since the mark CAS-create is
the anti-storm authority and a duplicate visit costs a read and loses a race. Two consequences stated
rather than discovered — the dispatch budget is **per replica per target**, and the contraction rebuild
and `gapConfig:` retirement are per-instance, which they already are (each replica publishes its own
`health.weaver.<instance>`, and `contractionStats` is documented as *"rows this engine instance has
observed"*). Weaver is single-instance in every shipped deployment.

**No durable key family is added** — no `weaver-state` shape, no Contract #10 §10.3 reserved-key entry.
Loss of any of this degrades to "re-scan from the top", never to a wrong verdict.

---

## 6. Per-row outcome table

Derived from `handleRow`'s actual control flow (§1's exit table plus the gap loop), not enumerated by
hand. Third column is the outcome, decided per row.

| # | Row state | Leg action | Outcome | vs. today |
|---|---|---|---|---|
| 1 | Key absent from the multi-get map (deleted mid-cycle) | skip; mark the cycle **unclean** | nothing | same (hard delete, ledger row 9) |
| 2 | Body does not parse | skip dispatch; raise `RowDataError`; cycle **unclean** | **audible, standing** | today: logged once at delivery, then silence (§4.4) |
| 3 | Empty body (tombstone) | skip | nothing | same |
| 4 | A **mark** exists for this entity at any gap | Walk A records it; Walk B takes no dispatch decision | `sweepMark`/`reclaim` own it | same |
| 5 | `violating` false | recorded; `freshUntil` armed if present | nothing owed | same |
| 6 | `violating` non-bool | `boolColumn` raises `RowDataError`, reads false ⇒ row 5 | **audible, standing** | today: latched at one delivery, **lost at restart and never re-derived** |
| 7 | `violating` true, `entityKey` echo missing | skip dispatch; `RowDataError` | **audible, standing** | same as row 6 |
| 8 | Target `__control`-frozen or revoked | target skipped entirely (§4.2(a)) | correctly excluded — the invariant's "excluded" branch | **fixed at enable**: today `Enable` cannot redeliver acked rows, so a freeze is permanent for a quiet row |
| 9 | `violating` true, open column not in `target.Gaps`, no augur `unplannable` | `GapWithoutPlaybook` | **audible, standing** — severity per §8 | today: raised at one delivery, lost at restart |
| 10 | `violating` true, action `surface` | per-entity `Surface` issue | escalated | same |
| 11 | `violating` true, suppressed by `inflight_<g>` | skip | in flight | same |
| 12 | `violating` true, a live `__count` ≥ cap but **no mark** (ledger row 18's 127-hour window) | `escalateExhaustedGap` | escalated | **fixed** — `sweepCount` declines (`count != 0`) and `reclaim` has no mark, so today **no leg reaches this row at all** |
| 13 | `violating` true, plan resolution fails | `UnresolvedReference` / `PlaybookConfigError` (target-scoped) or `TemplateDataError` (per-entity, `:581`) | **audible, standing; retried next cycle** | today: one Nak cycle, then silence |
| 14 | `violating` true, `admitGap` denies | skip; no mark, no issue | paced | same — but see §7.4 |
| 15 | `violating` true, dispatchable, budget available | mark CAS-create → dispatch | **discharged or escalated** | **fixed — this is the clinic case** |
| 16 | Row 15 but the target's pass budget is spent | skip dispatch; cursor **still advances** | deferred to the next cycle, within the published horizon | new pacing (§7.4) |

Row 12 is worth reading twice: the first draft claimed exhaustion was "fixed for a row with **no**
`__count` key", which ledger row 19 proves impossible — exhaustion *requires* a count. The real
population is the opposite one, and it is only reachable because §4.2(d) narrowed the skip to mark
presence.

Rows 2, 6, 7, 9 and 13 are the honest limit: **the leg does not make an unprojectable row dispatch.** It
converts *silence* into a standing, level-driven Health issue re-raised every cycle for as long as the
fact holds — which is what the invariant's "escalated" branch means, and what an operator needs. A row
that is violating and un-actionable is a target-authoring bug; the engine's obligation is to say so
continuously, not once. Note the precise delta: today these are not "raised once then silent" but
**latched until cleared or until restart, and after a restart never re-derived for a quiet row**
(`health.go:127`, `:130-145`) — which is the stronger version of the argument.

---

## 7. Cost, pacing, and the measurement premises

### 7.1 What one row pass actually costs

The first draft priced this as *"one filtered list + one batched multi-get"* per target. That is wrong
on the dominant term, and both reviewers caught it. Per **non-skipped violating** row, per **open gap**,
before any dispatch, `evaluateRow` performs two serialized KV round-trips:

- `gapSuppressed` → `getDispatchCount` → `KVGet` (`evaluator.go:1210`, `state.go:306`)
- `dispatchGap` → `e.marks.get` → `KVGet` (`evaluator.go:281`)

| Term | Per target per pass |
|---|---|
| Filtered list on `weaver-targets` (+ its ordered-consumer create/teardown) | 1 |
| Prefix list on `weaver-state` (state-as-memory, §4.2(d)) | 1 |
| Batched Direct-Get | 1, ≤1024 subjects ⇒ always the atomic fast path |
| **Serialized `KVGet`s** | **2 × (violating, markless rows in the page) × (open gaps per row)** |
| Direct-Get response bytes | ≤1024 × row size; at 2 KB/row = 2 MB against a 64 MiB `MaxPending` ceiling |

For the clinic target: 28 violating rows × 1 gap × 2 = **56 round-trips**, plus 3 — not 3. That is
still trivial, but the shape matters: a 512-row page of violating rows is **>1,000 serialized
round-trips**, which is exactly why the leg does not belong inside the 1-minute mark sweep under a 30 s
`AckWait` (§4.1), and why the dispatch budget bounds `evaluateRow` calls (§7.4) rather than only
dispatches.

**The key list is the term that grows, and paging does not bound it** (ledger row 5). At today's corpus
this is kilobytes. The stated ceiling is O(target rows) key strings per pass. The named mitigation —
snapshot each target's key list once per cycle instead of per pass — is **deliberately not built now**:
it adds retained state with a lifetime to get wrong, for a corpus that does not yet exist. Its trigger is
in §14.

### 7.2 The coverage horizon

```
full-cycle horizon(target) = ceil(rows / pageSize) × RowSweepInterval
```

At `pageSize = 512`, `RowSweepInterval = 5m`: a 63-row target cycles every pass (5 min); 10,000 rows
every ~100 min; 100,000 rows every ~16 h. Compare `defaultMarkLease = 30m`: a *dispatched* episode is
reclaimed within one lease; a *never-dispatched* row is reached within one cycle. Both bounded, which is
all the invariant asks. **The formula holds because §4.2(e) decouples the cursor from the budget** — in
the first draft it did not, and the horizon was fiction for exactly the targets that needed it.
`sweepRowCycleHorizon` publishes it per target.

### 7.3 The dispatch budget is per target, with a reserved share

`SweepRowDispatchBudget` bounds the `evaluateRow` calls the row sweep originates **per target per
pass** (default 16; ≤0 ⇒ default). Per target, not global: with a global budget and `targetIDs()`
returning Go map-iteration order (`registry.go:1503-1509`), which targets got the allowance would be
random per pass, and a chronic backlog on one target would starve every target later in that pass's
order. Refractor's own convergence sweep already ships the answer and states the principle
(`pipeline/sweep.go:~848-860`): *"Each direction holds a reserved share the others cannot consume… A
direction that could be silently switched off."* Contract #10 §10.8 names *"admission fairness"* as a
limb of the liveness invariant; a global unshared budget is that question, answered wrongly.

When a target's budget is spent the leg stops taking dispatch decisions for that target this pass;
Walk A still completes, the cursor still advances, and the deferred count is logged and metered
(`sweepRowDeferred`). **No silent cap.**

### 7.4 What the leg does — and does not — leave alone

The first draft claimed *"an existing episode's pacing is never changed by a leg that did not create
it."* **That is false and is withdrawn.** `admitGap` is called from `planGap`, the one seam both
fresh-dispatch legs share, and its token bucket and pending queue are **per target and shared** (ledger
row 23). Every leg-originated `admitGap` consumes a token and a pending slot that lane 1 or `reclaim`
would otherwise take, so a lane-1 dispatch *can* now be deferred because the row sweep drained the
budget.

Priced honestly: exactly one shipped target declares an `Admission` block —
`leaseApplicationComplete` (ledger row 25) — so today's exposure is one target, and admission's own
contract is that a deferral is *"ordinary pacing, not a fault: no mark, no episode, no Health issue"*, so
the interference costs latency, never correctness. The right long-term shape is for the leg's share to
come *out of* the target's declared admission budget rather than beside it; that is sequenced behind a
named trigger (§14) rather than built for a population of one.

What the leg genuinely does not touch: `reclaim`, `sweepCount`'s re-arm, and lane-1 dispatch are outside
`SweepRowDispatchBudget` — it governs only calls the leg originates, so an existing episode's *mark and
lease* discipline is untouched.

### 7.5 The arithmetic against the reported symptom

The target was read, not assumed (`packages/clinic-domain/targets.go:17-40`): one gap, `missing_site` →
`directOp BackfillAppointmentSite`, `Params: {appointmentKey: row.entityKey}`. It is **not** a `surface`
gap (row 10 n/a), it **has** a playbook entry (row 9 n/a), and it declares no `inflight_site`, so it
takes `defaultDirectOpRetryBudget = 3` and `GapBudgetExhausted` stays reachable. An adversarial pass
traced the population end-to-end through every conjunct of §6's ladder against `lenses.go:101-127` and
`:767-776` and found it passes each one: `violating` is a real bool (`site.key = null`), `entityKey` is
non-empty, no `Admission`/`Augur`/`Mode` block, `buildPlan` resolves `row.entityKey`, `found = false` ⇒
CAS-create ⇒ dispatch. The honest-limit rows do not apply to this population.

At `pageSize = 512`, `SweepRowDispatchBudget = 16`, `RowSweepInterval = 5m`: 28 rows fit in one page, one
cycle per pass, and the 26 drain over **two passes (~10 minutes)** on the budget. Census C2 re-derives
the population at build time rather than trusting this paragraph — the "26 of 28" figure is inherited
from the triage's live read and is a hypothesis about a state that may have moved.

---

## 8. The one decision for Andrew — severity, and the cache bound

§6 makes five decline classes **standing**. Two of them are `error`-severity
(`GapWithoutPlaybook` `:229`, `PlaybookConfigError` `:585`), and `aggregateStatus` computes over the
full issue set, so any `error` ⇒ `status: "unhealthy"` (ledger row 24).

Today the practical blast radius is small precisely *because* of the defect this design fixes: the raise
needs a lane-1 delivery, and the in-memory cache empties on restart, so a `GapWithoutPlaybook` on a quiet
row self-clears and cannot be re-raised. **This design converts that into a permanent,
restart-surviving `unhealthy`.** Contract #5 §5.2 defines `unhealthy` as *"cannot fulfil its primary
responsibility"*, and Weaver dispatching normally for 25 of 26 targets while one package's playbook is
missing a `gaps` entry is not that. The codebase draws the line itself, for a sibling fault, at
`evaluator.go:123-131`: *"a Contract #5 §5.2 `warning` (degraded), never an `error` (unhealthy = cannot
fulfil the responsibility)."*

**Recommendation: demote `GapWithoutPlaybook` and `PlaybookConfigError` to `warning` at their raise
sites**, so the severity is one value for both callers (two severities at one issue key would flap). It
is a deliberate, shipped severity and the change reaches lane 1 as well as the leg, which is why it is
Andrew's call rather than mine. The alternative — keep `error` and accept that a package-authoring typo
pins the component `unhealthy` until someone fixes the package — is defensible if `unhealthy` is meant
to be that strict; it destroys any operator alert wired to `status != healthy`.

**The cache bound is mine, and ships either way.** `issueCache.issues` has no cap; only `paced` is pruned
(ledger row 24). `issueKeyDataEntity` is per-(target, entity, column) and the leg raises it for **every
projected row**, not just delivered ones, and `snapshot()` sorts the whole key set on every heartbeat. A
100k-row target with a systemic projection bug means a 100k-entry map re-sorted every 10 s. So the leg's
per-entity raises are **capped per target** with an overflow entry naming the suppressed count —
mirroring `installer.go`'s `sampleWithOverflow`, the pattern the dossier already cites for the
*document*. T12 pins the **cache**, not only the emitted document; the first draft tested only the
latter.

The other three health sub-questions resolve without a decision: **flapping `since`** cannot occur
(`setSince` `:155-165` preserves an existing stamp, and §4.5's clear only fires when no row holds the
column open); **eviction of the explaining issue** cannot occur (`boundIssues` `:616-623` sorts `error`
first by explicit design, the leg's target-scoped families are bounded by 26 targets × columns, and the
flood families are `warning`).

---

## 9. Reconciliation with the existing mental model

**"Isn't that what the sweep is?"** The sweep is the *reclaim* mechanism; every leg is keyed on evidence
a dispatch produced (§3). Nothing was broken — the enumeration was never the projection.

**"Doesn't lane 1's replay cover this on restart?"** No: that is the belief `contraction.go:22-25`
encodes and `registry.go:26-34` refutes one file over (ledger row 12). Only a *cold* JetStream state
replays.

**"Doesn't `Enable` resume a frozen target's remediation?"** `Enable` Resumes pumping; it does not
redeliver acked messages. And `Disable` writes the marker *before* pausing, so any delivery in that
window takes `handleRow`'s `isTargetDisabled` Ack and is gone. The component doc's *"On enable,
remediation resumes for whatever is still violating"* is true only for rows that re-project afterwards;
the row sweep makes it unconditional, and the sentence is corrected in the same increment.

**"Does this duplicate Refractor's convergence sweep?"** No — and the precedent must be scoped
precisely, because the first draft over-claimed it. Refractor's sweep *does* prefix-list
`weaver-targets` (`pipeline/sweep.go:835`), but `SetSweepPlan` is installed only from
`InstallActorAggregate` (`projection/driver.go:517-530`), so only `unroutedTasks`,
`staleAssignedTasks` and `orphanedTaskGrants` are enrolled; `clinicSiteBackfill` and every plain
convergence lens are not. The primitive and the permission are shipped precedent; the enrolment is not.
§11 F prices the alternative of extending that enrolment.

**"New state — do we keep that state already?"** Six in-memory, cycle- or pass-scoped structures (§5),
mirroring the sanctioned in-memory class the engine already names: `shadowStats`, `contractionStats`,
`oscillationStats`, `admission`, and the registry's own CDC-rebuilt cache. No durable key family.

**"Is lane 1 now redundant?"** No. Lane 1 is the **prompt** path (dispatch within milliseconds of
projection); the row sweep is the **complete** path (every violation reached within one cycle). That is
the same prompt/backstop split §10.3 already draws between the reconciler and the mark's per-key TTL,
one level up.

---

## 10. Contract surface — no change, and why the first draft's edit was withdrawn

The first draft staged a frozen-contract edit adding, to §10.8's liveness bullet, a sentence promising
that the reconciler's work set is the target's currently-projected rows. **It is withdrawn**, on three
grounds that the adversarial pass put better than the draft had:

- **The promise already exists.** The frozen bullet reads *"No violating row may sit indefinitely with
  nothing owed on it: **every** `violating` row is eventually discharged, excluded, or escalated,"* and
  closes with *"A target shape under which a gap can stay open forever without escalating is a
  target-authoring bug, not an engine tolerance."* A never-dispatched violating row **is** a `violating`
  row. An implementation that does not honor a frozen promise is a bug to fix, not a contract to amend
  — the same shape as `weaver-exhausted-gap-durable-stop-design.md`.
- **It named a mechanism.** "The reconciler's *work set*" and "the engine state past dispatches left
  behind" describe how the engine enumerates. No package author can observe a work set; they observe
  dispatches and Health issues. Contracts are public contracts of a private codebase.
- **A pure refactor would falsify it.** Replace the leg with a periodic durable re-create (§11 A) and the
  observable promise is intact while the clause is false. A contract sentence a refactor can falsify is
  implementation detail — the skill's own test, applied to my own draft.

**One adjacency, adjudicated rather than blanket-denied.** `docs/contracts/10-orchestration-weaver.md:95`
freezes: *"Absent (every target before this fire) is unbounded — byte-identical dispatch, no row
read."* §7.3 introduces an engine-level dispatch budget that paces targets declaring no `admission`
block. My reading is that the clause scopes to the **admission mechanism's** effect — a target without
the block gets nothing from admission, and its lane-1 dispatch path is byte-identical to before
admission shipped, which this design does not touch. The budget governs only dispatches originated by a
path that did not exist when the clause was frozen. **No edit; the adjudication is recorded here rather
than asserted as "no other section changes."** §10.2's row shape and §10.3's reserved-key table are
untouched (§5: no durable key family).

**Component doc** (`docs/components/weaver.md`, not frozen, committed with the build): the Pipeline
section gains the row sweep; the Contraction-monitor restart sentence is rewritten (§4.6); the
Control-plane `Enable` sentence is qualified (§9).

---

## 11. Alternatives considered

**A. Periodic lane-1 durable re-create** — re-create each target's lane-1 durable (or attach a
short-lived `DeliverLastPerSubject` ephemeral) on a cadence, so JetStream itself re-enumerates. *This is
the strongest alternative and the first draft never named it* — it rejected only the per-**boot** nonce
variant, on a property (boot-triggering) that only that variant has.

| | Row sweep (recommended) | Periodic durable re-create |
|---|---|---|
| Enumeration code | a leg + 6 cycle-scoped structures | none — JetStream is the enumerator |
| `evaluateRow` extraction (§4.3) | required | **not required** — same handler, same path |
| Cross-pass state | 6 structures (§5) | none |
| Contraction rebuild | cycle-scoped install (§4.6) | free — `observe` runs on every delivery |
| Pacing | per-target budget (§7.3) | **unpaced burst** — every row of every target re-delivered at once |
| Observed-column set (§4.5) | free — Walk A already holds it | still needs separate machinery |
| Skipping owned rows | mark-set check (§4.2(d)) | none — re-delivers rows with live episodes, re-entering `dispatchGap` |

*Rejected, on two grounds and not on shape.* The unpaced burst is the decisive one: a re-created durable
delivers **every** row of the target simultaneously with no budget, no cursor, and no reserved share —
the failure §7.3 exists to prevent, minus the mechanism to prevent it. Second, it still cannot build the
target-wide observed-column set symptom 2 needs, so §4.5 would need its own walk anyway — at which point
the walk exists and the durable churn is redundant. The advantages are real and this is a close call;
what settles it is that the row sweep's extra machinery *is* the pacing and the observed set, not
overhead beside them.

**B. Nak the transient declines instead of Acking them.** *Rejected.* Edge-triggered repair of a level
fact, failing four ways: it cannot reach a row declined **before** the change (the entire existing
population — the demand); a genuinely-removed target hot-loops with no escalation; the redelivery
re-enters `dispatchGap` with `NumDelivered > 1`, which is the blanket in-flight re-fire branch
(`:207-215`) — a duplicate-dispatch path, not a retry; and it does nothing for exits 1/3/6/7. Worth
naming the *relationship*: with the row sweep in place the Acks become **correct** — they prevent
hot-looping and the level sweep is the retry. That is why this design does not touch them.

**C. A durable "declined" marker written on each decline**, so the existing enumeration covers it.
*Rejected.* Edge-triggered state about a level fact: a decline whose marker write fails is silently lost,
in the under-remediation direction. It cannot cover exit 2 (no registered target to key the marker on)
nor a row never delivered. It adds a durable key family with a lifetime to get wrong. *Could a variant
beat it?* A marker written by the **lens** would be level-triggered — but that is "project a column
saying you are violating", which the row already does. **The row is the marker.** (Noting the objection
landing back: §5's in-memory choice avoided the *durability*, and the first draft still got the
lifetime wrong — `Revoke`, eviction. Hence §5's explicit eviction sweep.)

**D. Rewrite the N consumers** — fix `clinicSiteBackfill`'s cypher so its rows re-project. *Rejected on
demand breadth, which is the test this alternative is entitled to.* 26 production targets across 13
packages (census C1), every one exposed to the identical hole; and the triage already found a
`violating`-predicate change *"would silently mask it"* — re-projecting clears the symptom by making
lane 1 deliver again while leaving the next decline just as permanent. Not a single-digit census, and the
demand-side fix does not fix the demand.

**E. Refractor emits only violating rows** (negative/filter-retraction projection). *Not an alternative
— a composable future optimization*, and the deferred scale-time capability
`docs/components/weaver.md` §Targets-as-Lenses already names. It shrinks §7.1's list term; a violating
row still needs an enumerator, which is the hole.

**F. Enrol plain convergence lenses in Refractor's own sweep**, letting its re-projection `Put` re-emit
the row into lane 1 — zero Weaver change. *Rejected on cost placement.* It would re-project **every row
of every convergence target** on Refractor's sweep cadence, burning the Refractor evaluation budget —
which is the scarcest resource in the platform right now and the subject of the board's co-top ★★★ row
(a varlength-anchor rebuild has the auth plane's sweeps suppressed 15h+ and 19 lenses lagging). Spending
that budget to solve a Weaver liveness gap is the wrong trade, and it also relocates responsibility for
the §10.8 invariant into a component that does not own it. Worth naming because it is the only option
requiring no Weaver change at all.

**G. Detect-and-report only** — walk rows, raise issues, never dispatch. *Rejected.* Delivers none of the
demand (the clinic row needs the dispatch), and "a human now owns it" is one of three outcomes, not all
three. The dispatch path is reached through the same `evaluateRow` lane 1 uses, so declining to use it
would be caution about code that is already load-bearing.

---

## 12. Executable censuses

**C1 — production weaver targets** (sizes the per-pass target count, §7.1).
```
grep -rn 'TargetID:' --include='*.go' packages/ | grep -v _test | wc -l
```
*Run this fire: **26**, across 13 files.* Unit: `TargetID:` field assignments in non-test package
sources — declarations, not files, not registered instances. Cross-check by distinct value, not by
matching line:
```
grep -rn 'TargetID:' --include='*.go' packages/ | grep -v _test | sed 's/.*TargetID: *//' | sort -u
```
*Run this fire: 9 string literals + 17 exported constants.* A count from `grep -c weaverTarget` returns
44 and is the wrong unit — it counts mentions.

**C2 — the demand population.** Against a running stack: `weaver-targets` keys under
`clinicSiteBackfill.`, of which how many have `violating == true`, versus `weaver-state` keys under the
same prefix. The premise is that the third number is far below the second. **Must be re-derived at build
time** — the "26 of 28" figure is inherited from the triage's live read.

**C3 — `msg` reachability below `handleRow`'s preamble** (pins §4.3's seam).
```
sed -n '/^func (e \*Engine) handleRow/,/^}/p' internal/weaver/evaluator.go | grep -n 'msg\.'
sed -n '/^func (e \*Engine) dispatchGap/,/^}/p' internal/weaver/evaluator.go | grep -n 'msg\.'
```
*Run this fire, and independently verified across all twelve callees by an adversarial pass:*
`msg.Subject`/`msg.Body` in the preamble only; `msg.Sequence` (`:172`, `:270`, `:305`) and
`msg.NumDelivered` (`:346`) thereafter — nothing else. **Ships as a pinning test** (T7): a third field
appearing here is a scope line item, not a detail.

**C4 — `gapConfig:` raisers** (pins §4.5's retirement argument).
```
grep -n 'issueKeyGapConfig' internal/weaver/evaluator.go
```
*Run this fire: **10** hits — 3 raises (`:229`, `:577`, `:585`), 4 clears (`:236`, `:523`, `:549`,
`:884`), the constructor (`:1599`), and **two doc-comment mentions (`:1506`, `:1593`) the pinning test
must exclude** or T8 fails on day one.* Post-fire the clear at `:884` moves to `finishCycle` (§4.5), so
the pin's expected shape changes with the increment that moves it.

**C5 — engine-config registration sites** (the "grep the existing three" reflex, for the new `Config`
fields and the new schedule).
```
grep -rn 'SweepOrphanWarmup' --include='*.go' . | grep -v '/tools/'
grep -rn 'sweepConsumerName\|sweepScheduleSubject\|armSweepSchedule' --include='*.go' .
```
Every hit is a scope line item: the `Config` field + doc comment, the default constant, the clamp, the
`newSweeper` wiring, the schedule arm, the consumer spec, the health sink, and every `cmd/weaver` and
test construction site.

---

## 13. Test strategy

| # | Proves | Shape | Inc |
|---|---|---|---|
| T1 | A violating row with **no mark** is dispatched by the row sweep | seed `weaver-targets` directly (never through lane 1), run one pass, assert a mark is CAS-created and the op submitted | 1 |
| T2 | A row **with a mark** is not touched by Walk B | seed row + mark, run a pass, assert exactly one dispatch and no second `claimId` | 1 |
| T3 | **A row with a live `__count` and NO mark IS dispatched/escalated** (ledger row 18's window; §6 row 12) | seed row + count at cap, no mark; assert `escalateExhaustedGap`, and a control at count<cap asserting dispatch | 1 |
| T4 | Paging covers every row across passes — no gap, no repeat | 3 × `pageSize` rows, `ceil(3)` passes, union of evaluated keys == the row set | 1 |
| T5 | The budget defers **without** blocking cycle completion | `budget+5` dispatchable rows on one target: pass 1 ⇒ exactly `budget` dispatches **and** `finishCycle` still runs; pass 2 ⇒ the remaining 5 | 1 |
| T6 | The budget is per target — a backlogged target does not starve another | two targets, one with a large backlog; assert the second dispatches every pass regardless of map order | 1 |
| T7 | The dispatch decision is identical from both callers (C3 pinned) | the same row through lane 1 and through the leg yields the same mark, `claimId` shape and dispatch issue set — **narrowed to the dispatch decision**; the `clearClosedMarks` / `contraction` / `freshness` / parse arms differ by design (§4.3) and are asserted per-mode | 1 |
| T8 | Every `gapConfig:` raiser is open-column-gated (C4 pinned, doc-comment hits excluded) | source-level pin on the raise-site set + a behavioural vector per code | 1 |
| T9 | The target-scoped clear retires a stranded latch, and **does not** retire one still open on an unvisited page or in an unclean cycle | two entities across two pages, one still open ⇒ latch survives mid-cycle; a forced multi-get failure ⇒ latch survives; assert on `since`, not membership | 1 |
| T10 | Removing `evaluator.go:884` does not strand a per-entity retirement | `retireClosedGapIssues` still fires from `clearClosedMarks`; the target latch now survives one entity's close and dies at cycle end | 1 |
| T11 | Contraction is rebuilt at a clean cycle completion and **not** sampled before it | restart with `known` empty ⇒ `sample` records nothing until the first clean `finishCycle`, then the true count | 2 |
| T12 | The **issue cache** stays bounded, not only the emitted document | N rows all raising `RowDataError` ⇒ per-target cap honored, overflow entry names the suppressed count, and the emitted document respects the existing severity-selected cap | 1 |
| T13 | The decline classes are audible and level-driven | one vector each for §6 rows 2, 6, 7, 9, 13: raised, survives a second cycle with `since` unchanged, clears on repair | 1 |
| T14 | A disabled/revoked target is skipped and its leg state evicted | freeze ⇒ no list, no multi-get, maps empty; enable ⇒ next pass dispatches **with no re-projection** | 1 |
| T15 | A revoked-but-registered target costs nothing (ledger row 20) | `Revoke`, then N passes; assert zero `weaver-targets` reads for it | 1 |
| T16 | The row sweep's own durable carries an explicit `AckWait` above its bound, and one replica never self-overlaps | spec-level assertion + a pass held artificially long, asserting no concurrent second pass | 1 |
| T17 | A `freshUntil` row never delivered by lane 1 gets its timer armed | seed directly, pass, assert the `@at` publish | 2 |
| T18 | e2e on the ephemeral stack: a target installed **after** its rows are projected converges | extend the existing convergence harness | 1 |

**Fixture discipline the Weaver dossier requires:** every fixture helper this leg uses must have one
vector that **omits each optional column** (`inflight_<g>`, `maxretries_<g>`, `freshUntil`, `priority`,
`entityKey`) — the dossier records two separate items where an always-supplied optional input made a
whole leg's defects untestable by construction. Fixture `targetId`s stay under 20 characters
(`lint-conventions` reads a 20-char `…ID` value as a NanoID).

**Mutation discipline:** revert each claimed line alone; where the claim is about *where* a block sits —
Walk A above the guards (§4.5), the cursor advance decoupled from the budget (§4.2(e)), the clear after
the cycle's own evaluations — the proof is a **move**, not a revert.

**T17 has no production consumer in the demand population:** `clinicSiteBackfill`'s lens emits no
`freshUntil` (`packages/clinic-domain/lenses.go:122`, `:767-776`). The temporal arm is built for the
targets that do project it, and is correctly in the later increment.

---

## 14. Decomposition for the Steward

**Size correction: the board row says M; after this fold it is L.** The first draft's M was priced
before the own-durable schedule, the per-target budget, the state-eviction sweep, the moved clear, and
the cache bound. Recorded here rather than discovered at build time; the board row is updated to match.

Two increments, not three — the first draft's Inc 1/Inc 2 split was **not available**. `evaluateRow`
reaches `dispatchGap` and `planGap`, which raise `issueKeyGapConfig` unconditionally, so §6 rows 9 and 13
become standing in whichever increment ships the leg. Shipping the leg without §4.5's replacement clear
would raise target-scoped latches on the sweep cadence with `evaluator.go:884` still in place — i.e. a
half-audible leg whose retirement is the wrong one. The leg and its retirement are one increment.

**Increment 1 — the row sweep** (the liveness fix; unblocks the clinic row; **posture-changing — this is
the one that warrants the full review pass**).
The `@every` schedule + `weaver-row-sweep` durable with its explicit `AckWait` (§4.1). `evaluateRow` with
its `rowSource` seam (§4.3, C3, T7). The walk: registered-target enumeration with the disabled skip and
state eviction, the state-as-memory mark set, the cursor, the paged list, the `KVGetMultiNoSnapshot`
page, Walk A above the guards, Walk B budgeted per target (§4.2). The `gapConfig:` retirement and the
move of `evaluator.go:884` (§4.5). §6 rows 2/6/7/9/13 audible, with §8's per-target cache cap. Config:
`RowSweepInterval`, `SweepRowPageSize` (default 512, clamped ≤1024), `SweepRowDispatchBudget` (default
16), plus every site census C5 returns. Metrics: `sweepRowsEvaluated`, `sweepRowDispatches`,
`sweepRowDeferred`, `sweepRowCycleHorizon`, `sweepRowCyclesClean`. Tests T1–T10, T12–T16, T18.
Component-doc Pipeline + `Enable` corrections. **Realizes value alone.**

**Increment 2 — the contraction rebuild and the temporal arm** (§4.6, §4.3's freshness row).
Cycle-scoped violating set, the atomic install at a clean `finishCycle`, `sample()` skipping an unbuilt
target, the `contraction.go:22-25` rewrite, and `scheduleFreshness` for `freshUntil` rows. Tests T11,
T17. Observability plus the temporal-lane hole; correctly last.

**If Andrew takes the severity demotion (§8)** it lands in Increment 1, with a test asserting one
severity per issue key across both callers.

**Not in scope, with named triggers** (the dead-scaffolding test — neither realizes value before its
trigger exists):
- *Per-cycle key-list snapshot* (§7.1's mitigation) — trigger: a target whose per-pass filtered list is
  measurable against `RowSweepInterval`.
- *Drawing the leg's dispatch share from the target's declared `admission` budget* rather than a separate
  engine budget (§7.4) — trigger: a **second** target declaring an `Admission` block, or a reported
  lane-1 deferral attributable to leg-originated admission traffic. Building it for a population of one
  is machinery ahead of demand.

---

## 15. Risks

| Risk | Direction | Mitigation |
|---|---|---|
| The leg dispatches a backlog an operator did not expect | over-dispatch | §7.3's per-target budget; every dispatch still passes the mark CAS-create, `gapSuppressed`, `admitGap`, and the userTask/external classifier, reached through the *same* function lane 1 uses. Nothing dispatches that lane 1 would not have dispatched at delivery. |
| A stale-revision row dispatches against a closed gap | wrong dispatch | The mark CAS-create is the anti-storm authority and the op carries the row's revision as its OCC condition; a stale revision loses, it does not double-act. This is what licenses `KVGetMultiNoSnapshot` (§4.2(c)). |
| The leg races `reclaim` on the same gap | duplicate episode | §4.2(d)'s mark-presence skip removes any row with a live mark from Walk B; `firstDelivery = true` additionally keeps it out of the blanket re-fire branch. |
| The `gapConfig:` clear retires a live latch | silent loss of an `error` issue | §4.5's three constraints — cycle-complete, after the cycle's evaluations, and **clean** (any list, multi-get or parse failure marks the cycle unclean and skips the retirement). Walk A above the guards closes the entityKey/frozen-target routes. T9 pins both directions on `since`. |
| A fourth `gapConfig:` raiser is added later and is not open-column-gated | the retirement silently over-retires | Named honestly in §4.5 as an incidental guarantee; T8's pinning test is the gate, shipped in the same increment. |
| Leg-originated `admitGap` defers a lane-1 dispatch | latency, never correctness | Stated in §7.4 rather than denied; one shipped target is exposed; admission's own contract makes a deferral a non-fault. The proper share-splitting is sequenced behind a named trigger. |
| The per-pass list grows with the corpus | cost | §7.1 states it as a ceiling; `sweepRowCycleHorizon` makes it observable; the mitigation is designed and sequenced (§14). |
| A partial read is mistaken for a complete one | under-coverage, or a wrong retirement | Both list primitives discard a ctx-expired partial and return an error (`kv.go:208-214`, `:246-252`, `:299-303`); a multi-get failure and a parse failure take the same unclean-cycle branch (§4.5 constraint 3). |
| The extraction changes lane-1 behaviour | regression on the hot path | The `rowSource` seam makes every per-caller difference explicit (§4.3's table); T7 pins the shared dispatch decision and asserts the differing arms per mode; C3 pins the message surface. |
| A revoked target costs reads forever | unbounded standing cost | §4.2(a) skips disabled targets; T15 pins zero reads. |

---

## 16. Dossier entries this design is built against

From `docs/components/weaver.md` § *Review keeps catching* — what a fire brief copies into part 5, and
where this design answers each:

- *A gap class is decided by the dispatch's SHAPE; a NEW seam inherits the classifier and its pacing.* →
  §4.3: no new seam; `evaluateRow` is the shared function, `firstDelivery` pinned true, the per-caller
  differences tabulated rather than claimed away.
- *A Health issue key is a LATCH: scope it to the fact it states, and split it only with every clear
  re-paired. Before adding a CLEAR, enumerate every OTHER leg that raises at that key.* → §4.5's raiser
  table, C4's pin, and the explicit **move** of `:884` rather than a second clear beside it.
- *A per-entity Health issue is unbounded, and the heartbeat is ONE KV value.* → §8's per-target cache
  cap with an overflow entry, and T12 pinning the cache as well as the document.
- *A fact ends by more routes than the one you are editing — enumerate the LEGS.* → §4.3's
  `clearClosedMarks` decision and §4.5's constraint that the target-scoped clear runs after the cycle's
  own evaluations.
- *A presence assertion cannot pin a clear whose caller re-raises in the same pass — the STAMP is the
  observable.* → T9 and T13 assert on `since`.
- *A shared test fixture that always supplies an OPTIONAL input pins only the supplied case.* → §13's
  fixture-discipline paragraph.
- *A leg's arms are a lattice: every RETIRE belongs above every "cannot act" GUARD.* → §4.5: Walk A reads
  the raw body above `isTargetDisabled` and above the `entityKey` guard. The first draft violated this by
  computing the observed set inside `evaluateRow`, and it was a blocking finding.
- *An `error`-severity Health issue must not fire on a self-healing condition.* → §8, which is the whole
  reason the severity question goes to Andrew.
- *Prove each changed line by reverting THAT LINE — and where the claim is about WHERE a block sits, the
  mutation is a MOVE.* → §13's mutation-discipline paragraph names the three ordering claims.

---

## 17. What the adversarial passes verified as SOUND

Recorded so the surviving argument is legible rather than assumed, and so a re-review can start from the
open questions rather than re-deriving the settled ones. Two independent passes, both read-only.

- **§1/§3's root cause.** `reconciler.go:187` is a whole-bucket `KVListKeys(weaver-state)`; every leg is
  downstream of a dispatch (verified across `:273`, `:365`, `:532`, `:817`).
- **Both corrections to the demand row.** `sweepCount`'s `count != 0` is the first of five gates on arm
  (n) and relaxing it mints a fresh `claimId` outside `reclaim`'s ladder; `target.Gaps` is the orphan
  authority for `__effect` alone.
- **Census C3, exhaustively.** All twelve callees below `handleRow`'s preamble take derived scalars only;
  none takes the message, subject, consumer, or delivery context.
- **`firstDelivery = true` is correct and safe**, for the stated reason: `found = false` makes the
  fresh-`claimId` branch unreachable and the CAS-create-loses drop handles a lane-1 race.
- **The zero-sequence guard is inert for the leg** and correctly left as written.
- **`contraction.observe` idempotence** (`if was == violating { return }`); **the three call sites**;
  **`contraction.go:22-25` is affirmatively wrong** as claimed.
- **`KVListKeysFilter` pages client-side** — the correction to the demand row's "paging designed in"
  stands.
- **The `KVGetMultiNoSnapshot` choice**, and §7.1's byte-margin arithmetic against the 64 MiB ceiling.
- **Permissions, and stronger than first stated**: Weaver already creates durable consumers on
  `KV_weaver-targets` and already `KVGet`s it from `sweepCount`, so both paths are proven live.
- **Hard-delete tombstones** in `weaver-targets`, so no soft-tombstone filtering is owed.
- **§6 row 6** — `boolColumn` raises `RowDataError` for a non-bool and returns false.
- **§6 row 8 is a genuine, correctly-reasoned win** — `Enable` resumes pumping without redelivering.
- **§7.5's clinic payoff**, traced end-to-end through every conjunct of §6's ladder against the target
  and its lens. The honest-limit rows do not apply to that population.
- **Alternative B's rejection**, including the observation that the Acks become correct once a level
  sweep exists.
- **Alternative D's rejection on demand breadth** — 26 targets, 13 packages.
- **§5's "no durable key family, no §10.3 reserved-key entry"**, and that §10.2/§10.3 are untouched.
- **`boundIssues` cannot evict the explaining issue** — severity-first by explicit design.
