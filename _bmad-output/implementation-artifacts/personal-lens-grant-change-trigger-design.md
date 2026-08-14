# The personal plane's D1 gate gets a trigger — and the convergence sweep it never had

**Status: ✅ RATIFIED — Andrew, 2026-08-13 · low priority** (build after higher-priority ratified work).
Build as **one fire, internal order Inc 1 → Inc 2** (fewer-larger-fires; §11's internal cut available if
the worktree gets heavy); Inc 1 is posture-changing → full review pass. No contract change. DD folded at
ratification: the orphan-expiry precondition for §4.3's future narrowing is now **shipped** (`b9bf84ef`).
§13 adversarial pass ✅ RUN + findings FOLDED (2026-08-11) — four blocking, all closed in place.
Author: Winston (Designer fire, 2026-08-11).
**Component:** Refractor — `internal/refractor/{adapter,pipeline,projection,capabilityread}` + `cmd/refractor` wiring.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → Component maintenance →
*[Refractor] A personal row goes stale when its own D1 grant flips* (★★, M).
**Extends:** [personal-secure-lens-design.md](personal-secure-lens-design.md) (PL.1–PL.5 shipped) and
[personal-lens-retraction-design.md](personal-lens-retraction-design.md) (R1+R2 shipped) — this supplies the
**trigger** those two designs left to chance, and it is buildable only *because* R1 shipped the frame.
**Frozen-contract change: NONE.** See §7.

---

## For Andrew (one-look ratification block)

**What it does (two lines).** A Personal Lens decides every row against the D1 read-grant projection
(`cap-read.*` in `capability-kv`) — a projection produced by a *different* Refractor pipeline, read live, with
no change notification and no ordering between the two. So a grant that lands or is revoked changes nothing
until some unrelated Core-KV event happens to re-drive that actor. This design gives that dependency an edge:
the read-grant producer announces a grant **liveness transition**, and the personal pipelines re-evaluate that
one actor and publish a fresh authoritative keyset frame. A second increment enrols the personal plane in the
**convergence sweep** it has never had, as the standing healer behind that edge.

**The one thing to understand before ratifying.** The retraction *machinery* is already complete and shipped —
a D1-denied row is dropped inside the same evaluation call that assembles the frame's key list
(`evaluate.go:639-641` `continue`, before the append at `:657`; `emitPersonalFrames` builds only from the
surviving `results`, `evaluate.go:1126-1136`), and the client prunes on the frame (`edge/sync/sync.go:737-751`).
**Only the decision to re-evaluate is missing.** That is why this is an M, not a rewrite: the fix is one
notification edge plus a sweeper, not new retraction semantics.

**No architectural fork** (no Gateway / Vault / multi-cell / HA-NATS surface). Three calls I made that deserve
your eye:

1. **Producer-side in-process signal, not a `capability-kv` watcher.** I costed the watcher: the permission
   matrix permits it (`natsperm/matrix.go:63-71`, `:162-177`, `:460-464`) and `SubscribeKVChanges` is
   bucket-generic (`substrate/subscribe.go:169-206`), so it would have worked. I rejected it on **precision**:
   the guarded write path deliberately re-writes an unchanged body on every evaluation to advance the
   watermark (`adapter/natskv.go:175-189` — the comment states why it must never gain a content skip), so a
   watcher sees churn it cannot distinguish from a real grant flip, and the auth-plane sweep alone would then
   drive **up to 4 × 25 swept actors/minute × 15 personal lenses ≈ 1,500 pointless cypher evaluations a
   minute**, forever (`sweep.go:32` per producer × Census 2 × Census 1).
   `guardedWrite` already reads the stored entry's bytes for its CAS, so the **producer** is the only place
   that can tell a transition from a rewrite. §8.1 has the full comparison; §4.2 has the one write path that
   escapes it (`Truncate`) and what that costs.
2. **The sweep is the durability story, not a persisted queue.** The in-process signal is best-effort by
   construction (a crash between the grant write and the drain loses it). Rather than invent durable
   Refractor-owned state to protect it, Increment 2 enrols the personal plane in the sweep every other
   actor-aggregate lens already has. Prevention best-effort, detect-and-recover authoritative.
3. **A personal sweep is newly *possible*, and the reason is worth a sentence.** No decision ever excluded the
   personal plane from sweeping: `InstallPersonalLens` simply never calls `SetSweepPlan`
   (`projection/personal.go:106-135` vs `projection/driver.go:435`), and `sweepEnrolment` is never even
   evaluated for it (`driver.go:425` is its only call site, inside `InstallActorAggregate`, which a personal
   lens never reaches). Had it been asked, it would have refused on its third conjunct — the target adapter
   cannot enumerate keys (`driver.go:319-322`) — which is the sweep's **orphan** direction. The **keyset frame
   supplies that direction from the other end**: an authoritative frame retracts everything absent from it,
   client-side, with no target enumeration at all. So the gap is structural and unexamined, and the one reason
   that *would* have justified it stopped being true when R1 shipped.

**The adversarial pass earned its keep (§13).** It returned four blocking findings against my draft — the
actor is not in scope where I said it was, the *revocation* direction had no outcome channel out of the
adapter at all, the registry I proposed reusing is a one-method interface behind a deliberate architecture
boundary, and `Truncate` bypasses the whole mechanism on a path a **narrowing cypher edit triggers
automatically**. All four are folded. The fifth reshaped §4.1: because the client drops or exempts a frame
whose revision under-claims, copying `Hydrate`'s capture-before posture would have shipped a retraction that
cannot retract. Increment 1 is a solid M as a result, not a small one.

**What I am NOT proposing.** No change to what `IsReadable` admits, no change to the frame wire format, no
change to the D1 contract, and no widening of `Reproject` (§4.1 adds a sibling entry point; the
`ErrNotActorAggregate` refusal at `reproject.go:291-302` stays exactly as written and for exactly its stated
reason).

---

## 1. Problem + intent

### 1.1 The dependency nobody drew

`personalEnvelopeFn` gates every personal row on `capabilityread.IsReadable`
(`projection/personal.go:172-184`), which reads `cap-read.<…>.<actorType>.<actorId>.<anchorId>` keys out of
the `capability-kv` bucket (`capabilityread/capabilityread.go:73-109`). Those keys are written by four
*other* Refractor pipelines (§3, Census 2). So a Personal Lens's output is a function of two inputs:

- its own Core-KV subgraph — which **does** drive it, through CDC and the actor-enumerator fan-out; and
- the D1 read-grant projection — which drives **nothing**.

Refractor's whole reaction model is one shared Core-KV CDC subscription (`lens/corekv_source.go:721`). A lens
output bucket is a write target, never an input anything subscribes to. The personal plane is the one place
where a projection is *read as a decision input* by another projection, and that read has no change edge.

### 1.2 Both directions are broken, and the growth direction is a race rather than a gap

**Shrink (revocation) — the over-grant direction.** A revoked grant tombstones the actor's `cap-read` entry
(`multiEntryRetractions`, `evaluate.go:748`). Nothing re-evaluates the personal lens, so the device keeps the
row until an unrelated event touches that actor's subgraph, or the device cold-reattaches. The window is not
"bounded by CDC lag" — it is **bounded by unrelated traffic**, i.e. unbounded.

**Growth.** Symmetrically, a newly-granted anchor produces no row until something else re-drives the actor.

But for the subset of personal lenses whose own cypher walks the *same* relation the grant producer walks —
`edgeTasks` and `edgeCatalog` both carry a `domainStaff` role walk (Census 3) — the CDC event **does** reach
both pipelines. They then run **concurrently with no ordering**: if the personal pipeline evaluates before the
producer's `cap-read` write lands, `IsReadable` denies, the row is skipped, and nothing re-runs. This is worse
than a missing trigger, because it looks like it should work and fails intermittently.

That failure is not hypothetical. It was root-caused live on the showcase stack and written down twice:
`facet-staff-worlds-design.md` §166 records a genuinely fresh task never reaching the tech's mirror with the
grant document confirmed present, and `personal-lens-retraction-design.md` §9 records the same mechanism as an
**accepted risk** — *"retraction lands on the revoking event only if the cap-read producer projected first;
otherwise on the next enumerating event or hydrate."* This design retires that acceptance.

### 1.3 What an operator can do today

Three remedies exist, all wrong-shaped for a grant flip:

| Remedy | Where | Why it does not answer this |
|---|---|---|
| `personal.hydrate` | `control/service.go:1313` | Identity-bound to the caller (`service.go:929-941`) — only the device itself can ask, and only on attach |
| `personal.requesthydration` | `control/service.go:1449-1475` | An operator *can* force it, but it is consumed only at the device's **next attach** (`edge/sync/sync.go:444-452`) — an attached device is unreachable |
| Full lens `rebuild` | `control/service.go:1051` | Replays the entire Core-KV CDC history for the lens, for every actor, to fix one actor's grant |

The honest summary: the only mechanism that repairs one actor's grant staleness today is a full historical
replay of a lens.

### 1.4 Intent

Make the personal plane's second input behave like its first: a change to it re-drives the affected actor
promptly, and a standing sweep converges whatever the prompt path missed. Nothing about the security decision
changes — `IsReadable` is the boundary and stays the boundary; it simply stops being asked at arbitrary times.

---

## 2. Grounding ledger (verified in code this fire)

Every row cites the code that **does** the thing, never a comment that describes it, except where the row is
explicitly about a comment's claim.

| # | Fact | Where | Bearing |
|---|---|---|---|
| G1 | The D1 gate runs inside the envelope fn and can only skip, never delete | `projection/personal.go:172-184`, `:182` | The gate's only expression is "this row does not exist for you" |
| G2 | A D1-skipped row is `continue`d out of `results` **before** the append that would put it in the frame | `pipeline/evaluate.go:639-641`, append at `:657` | The frame is already correct the moment an evaluation runs — **the missing piece is only the evaluation** |
| G3 | `emitPersonalFrames` builds `byActor` from `results` and ranges over `enumeratedActors`, so a zero-surviving-row actor gets an **empty** frame | `pipeline/evaluate.go:1118-1147` | Full retraction, including "you may now read nothing", is already expressible |
| G4 | The client prunes every stored key for the lens absent from the frame | `edge/sync/sync.go:737-751`; `edge/store/bolt.go:197`, `idb.go:289` | The retraction transport is complete end to end (R2 shipped) |
| G5 | `Reproject` refuses a personal pipeline by asserting the adapter is a `KeySetPublisher` | `pipeline/reproject.go:291-302` | An **entry-point** refusal about a read-back-diff model, not a statement that per-actor re-evaluation is impossible |
| G6 | `Hydrate` re-evaluates exactly one actor off the consumer goroutine, taking its own rule snapshot | `pipeline/hydrate.go:35-95`, snapshot at `:56-58` | The per-actor entry point this design needs **already exists**, minus the marker; and it establishes the concurrency posture to mirror |
| G7 | `Hydrate` captures `highWater` **before** reprojection so a racing live delta cannot be regressed | `pipeline/hydrate.go:36` + doc `:20-27` | The revision posture the new entry point must copy (§4.1) |
| G8 | A personal pipeline never gets a `SweepPlan`: `InstallPersonalLens` simply never calls `SetSweepPlan` | `projection/personal.go:106-135` vs `projection/driver.go:435` | The exclusion is structural, not a name list — and not a policy statement |
| G9 | `sweepEnrolment` is called from **one** site — inside `InstallActorAggregate`, which a personal lens never reaches — so it is never *evaluated* for a personal lens; had it been, the third conjunct (adapter cannot enumerate keys) would refuse | `projection/driver.go:425` (sole call site), `:309-324`, `:319-322` | The exclusion is an **unexamined structural gap**, not a recorded decision. The only reason that *would* have justified it is the orphan direction, which the frame now supplies from the client side |
| G9a | `Sweeper.survey` gets its anchor population from **Core KV**, not from the target | `pipeline/sweep.go:740-745` (`VertexPrefix + AnchorType + ".*"` → `coreKV.ListKeysFilter`) | A personal sweep's candidate list is genuinely available — this is what makes Increment 2 possible at all |
| G9b | …but `survey` hard-fails without a target key lister, and `candidates` is built mostly out of the target listing | `pipeline/sweep.go:759-767` (`errSweepNoKeyLister`), `:786-930` | `Sweeper` itself is **not reusable**; only its Core-KV population walk and round-robin cursor transfer (§4.3) |
| G10 | The auth plane sweeps at 60 s / 25 anchors per tick, **per pipeline**; a 10k-actor cell re-verifies in ≈7 h | `pipeline/sweep.go:18-33`; the plan is per-lens, `projection/driver.go:435` | The batch/interval discipline Increment 2 mirrors, and — multiplied by Census 2's four producers — the number that makes a naive watcher unaffordable |
| G11 | The **guarded** upsert path deliberately writes even when the row content is unchanged, to advance the watermark | `adapter/natskv.go:169-190`, esp. the comment at `:175-184` | A `cap-read` KV message does **not** imply a grant changed — the decisive fact against the watcher (§8.1) |
| G12 | The **unguarded** path does content-skip, explicitly to avoid re-notifying a target bucket's watchers | `adapter/natskv.go:195-208` | The platform already reasons about "a write that notifies"; the guarded path simply cannot use it |
| G13 | `guardedWrite` reads the stored entry's **bytes** before writing, but the only thing that parses them is `storedProjectionSeq` — the stored `isDeleted` flag is read nowhere on the write path today | `adapter/natskv.go:334`, `:349` → `:388-405` | The producer is the only place both bodies are in hand — but deriving *liveness* from them is a **new parse**, not a free read (§11 Inc 1.1) |
| G13a | `Truncate` purges keys directly, never through `guardedWrite` | `adapter/natskv.go:533-546`, `truncateKeys` `:548-563` | A truncating rebuild of a producer is a **bulk revocation with no transition signal** — §4.2's fourth arm exists for it |
| G13b | A truncating rebuild is reachable automatically, not only by an operator: a MATCH hot-reload that *narrows* a lens owes one | `pipeline/pipeline.go:2669`, `:2707`; `cmd/refractor/reload.go:462-473`; `control/service.go:1051` | A narrowing edit to a producer's own cypher **is** a revocation-shaped change, and it takes the un-signalled path |
| G13c | `writeResults` asserts `OutcomeUpserter` only; the plain `Adapter.Delete` discards its outcome | `pipeline/pipeline.go:3372`, `:3382`; `adapter/natskv.go:230-233` | The **revocation** direction has no channel out of the adapter today — §11 Inc 1.2 adds `OutcomeDeleter` or Increment 1 ships a half-dead trigger |
| G13d | The guarded upsert path synthesises `Wrote: true` unconditionally, by design | `adapter/natskv.go:175-190` | The transition verdict must be a field **independent of `Wrote`**, never derived from it |
| G14 | `SubscribeKVChanges` is bucket-generic (`streamName = "KV_"+bucket`) and creates **only a consumer**, never a stream | `substrate/subscribe.go:169`, `:202` | A watcher was technically available — this row exists so §8.1's rejection is on merits, not on a false impossibility |
| G15 | `protectedStreamDenies` denies stream-admin verbs only; consumer ops and subscribe stay allowed for refractor | `natsperm/matrix.go:63-71`, `:162-177`, `:460-464` | Same as G14: the permission envelope is not the reason |
| G16 | Exactly **one** executable call site of `capabilityread.IsReadable` exists | `projection/personal.go:177` (census C1, §3) | The convention this design establishes has one subject today — which is why the gate is XS and why it must exist before there are two |

---

## 3. Executable censuses

Each ships as the command that derives it, so the build's Phase-0 re-runs it mechanically. **Two of these
numbers were wrong on the first derivation this fire; both wrong forms are recorded so nobody re-derives them
the same way.**

**Census 1 — Personal Lenses (the per-actor fan-out factor). Expected: 15.**

```bash
grep -c '^[[:space:]]*Personal: *true,' packages/edge-manifest/lenses.go
```

> The naive `grep -rn "Personal: *true" --include="*.go" packages/ | wc -l` returns **19** — it counts three
> doc-comment mentions (`lenses.go:43,53,444`) and the package test. The unit is *lens declarations*, not
> matching lines. `grep -c "CanonicalName:" packages/edge-manifest/lenses.go` = 15 corroborates it a second
> way. All 15 live in one file, in one package (`edge-manifest`); no other package declares a Personal Lens.

**Census 2 — `cap-read` producers. Expected: 4 (1 hand-authored + 3 generated).**

```bash
grep -rn 'OutputKeyPattern: *"cap-read' --include='*.go' internal/ packages/ | grep -v _test.go
grep -c '{Name: domain' packages/edge-manifest/lenses.go   # generated producers = declared domains
```

> `internal/bootstrap/lenses.go:220` is the kernel base self-grant (`cap-read.{actorSuffix}`).
> `internal/pkgmgr/anchorwalk.go:498` is the **generator**: one producer per declared read-grant domain,
> emitted at `ExpandReadGrantWalks()` time. `edge-manifest` declares three domains
> (`lenses.go:393-397`: `domainStaff`, `domainBase`, `domainProvider`). **No producer's Go literal exists in
> `packages/**` at all** — a source-only grep over `packages/` finds zero and would conclude there are none.

**Census 3 — the pairing (which producer can admit which lens's rows).** Structural: `validateWalklessPersonalLens`
(`internal/pkgmgr/anchorwalk.go:221-240`) fails the package build closed for a Personal Lens that is neither
self-anchored nor carries `Walks`, and an undeclared `GrantDomain` fails `parseWalks` (`:126-135`). **An
installed Personal Lens with no admitting producer is not a reachable state.** Consequence for this design:
every one of the 15 is D1-gated in production, so every one is exposed to the staleness — there is no subset to
scope to.

**Census 4 — `IsReadable` call sites. Expected: 1.**

```bash
grep -rn 'capabilityread\.IsReadable(' --include='*.go' internal/ cmd/ | grep -v _test.go
```

> The trailing `(` matters: without it the grep returns 5, of which 4 are doc comments
> (`pkgmgr/anchorwalk.go:12,606`, `pipeline/evaluate.go:735`, `pipeline/actor_enumerator.go:301`). This is the
> census the §10.1 lint gate pins.

---

## 4. The shape

Two increments. Increment 1 is the latency path; Increment 2 is the convergence path. They are the same two
mechanisms every other Refractor plane already runs — this plane has only ever had one of them, and its one
mechanism is blind to one of its two inputs.

### 4.1 The primitive both increments need — `ReprojectPersonalActor`

A new exported entry point on `Pipeline`, a **sibling** of `Hydrate`, not a widening of `Reproject`:

```go
// ReprojectPersonalActor re-evaluates this personal pipeline for one actor and
// publishes the resulting deltas plus an authoritative keyset frame. It is
// Hydrate without the terminal hydrationComplete marker.
func (p *Pipeline) ReprojectPersonalActor(ctx context.Context, identityID string) error
```

Mirroring `Hydrate` (G6) clause by clause:

| Clause | Hydrate | ReprojectPersonalActor | Why |
|---|---|---|---|
| Revision | `highWater := p.Progress().LastAppliedSeq` **before** reprojection (`hydrate.go:36`) | **after** reprojection, immediately before the publish — see §4.1.1 | Copying `Hydrate` here would make the retraction **unable to retract**. §4.1.1 |
| Rule snapshot | `p.ruleState()` (`hydrate.go:56-58`) | identical | Both run off the consumer goroutine |
| Missing actor | errors "no such identity" (`hydrate.go:47-51`) | **publishes the empty frame** | A tombstoned identity is the expected companion of a grant retraction; the empty frame is the retraction signal (G3). Erroring would drop the one case that most needs retracting |
| Keyset frame | `PublishKeySet` at `highWater` (`hydrate.go:83-87`) | identical | The retraction transport (G4) |
| Terminal marker | `PublishHydrationComplete` (`hydrate.go:88-92`) | **omitted** | That marker gates the client's first paint; emitting it mid-session would release a gate that is not being gated |

**Non-goal:** `Reproject` is untouched. Its refusal (G5) is about a read-back-diff reconciliation model that
does not fit an append-only target — still true, still the right refusal.

#### 4.1.1 Why the revision is captured AFTER, and the lock that makes it monotone

This is the one place the design deliberately departs from `Hydrate`, and the adversarial pass is what forced
it. The client applies a frame through two guards, both in `edge/store/bolt.go`:

- `:208-210` — a frame whose `revision` is **below** the last applied frame's high-water for that lens is
  **dropped whole**.
- `:291-294` (`collectAttributed`) — within an applied frame, any key whose stored attribution revision
  **exceeds** the frame's revision is **exempt from pruning**.

`Hydrate` captures `highWater` before reprojecting so it *under*-claims — correct for a bulk cold snapshot,
because the worst case is a row that arrives again anyway. For a **retraction** frame both guards fail in the
**over-grant** direction: the revoked key is either never examined or the whole frame is dropped, and the
stale row survives. A grant-triggered frame that under-claims by construction is a retraction that cannot
retract. So `ReprojectPersonalActor` captures the revision **after** `reprojectActors` returns.

The cost of capturing after is the mirror-image failure — over-claiming could prune a row a concurrent live
evaluation wrote at a higher sequence but has not yet framed. That direction is **under-display**, it is
recovered by the very next event or sweep pass, and it is the correct side to fail on for a security filter.

**And the three publishers get serialized.** Nothing today orders the Increment-1 drain worker, the
Increment-2 sweeper, and a live `personal.hydrate` against each other: three goroutines can publish three
frames for the same (lens, actor) at three independently-captured revisions, and the `frameHW` guard then
keeps whichever *arrived* with the highest number rather than whichever is freshest.
`ReprojectPersonalActor` therefore holds a **keyed (lens, actor) mutex across evaluate → write → publish**,
with the revision captured inside it. Frames from the three reprojection paths are then totally ordered and
monotone per (lens, actor). Live CDC frames still publish from `writeResults` outside that lock, but they are
emitted from the single consumer goroutine at `msg.Sequence`, so they are already monotone and strictly
fresher — "freshest wins" is then the correct outcome of the guard rather than an accident of arrival order.

**The residual, stated honestly.** A CDC evaluation that *started* before the producer's tombstone landed and
ACKs after our frame will publish a higher-revision, wrongly-admitting frame and win. `lastAppliedSeq`
advances only on ack (`pipeline.go:458-461`), so that in-flight evaluation is invisible to our snapshot, and
the window is bounded by however long that message takes to complete and ack — which CAS contention
(`natskv.go:328-362`, up to 8 attempts) or a retry-queue backoff can stretch well past one evaluation. It is
**not** "one evaluation duration", and T5 asserts it under contention rather than on the fast path. The sweep
converges it.

### 4.2 Increment 1 — the notification edge

**The signal.** `guardedWrite` (G13) already holds both the stored entry and the incoming body. It gains a
liveness verdict in its existing `guardVerdict`/outcome return:

| Stored | Incoming | Verdict / exit | Transition? |
|---|---|---|---|
| absent | live | `Create` (`natskv.go:335-343`) | **yes** — grant lands |
| live | tombstoned (`isDeleted:true`) | `Update` (`:354-361`) | **yes** — grant revoked |
| tombstoned | live | `Update` | **yes** — re-grant |
| live | live | `Update` | no — watermark advance only (G11) |
| tombstoned | tombstoned | `Update` | no |
| **absent** | **tombstoned** | `Create` of a tombstone body — reachable, since `deleteRow` always routes through `guardedWrite(delete=true)` (`:255`) | **no** — nothing was ever granted |
| any | `storedSeq >= incomingSeq` → `guardDeclinedByWatermark` (`:348-352`) | no write happened | no |
| any | `incomingSeq == 0` → `guardDroppedNoToken` (`:322-326`) | **returns before any `Get`** — no stored body exists to compare | no, and it must be classified as *unknown*, not as "no change" |
| any | any **error** exit (`:331`, `:341`, `:346`, `:359`, `:366`) | all return `guardCommitted` | no — classification is **err-first**, or every failure path reads as a committed non-transition |

Only the three `yes` rows produce a signal. Two implementation notes the table encodes: the verdict must be a
field **independent of `Wrote`** (G13d — the guarded path synthesises `Wrote: true` unconditionally, by
design), and deriving liveness requires **parsing the stored body's `isDeleted`**, which nothing on the write
path does today (G13 — `storedProjectionSeq` extracts the watermark and nothing else). That is the whole reason this is producer-side (§8.1): in steady
state the auth-plane sweep re-verifies 25 actors a minute (G10) and finds drift **rarely**, so the transition
rate collapses to the true grant-flip rate while a watcher would see all 25.

**The routing — via the inversion the sweep already owns.** My first draft claimed the write loop "already
knows the `actorKey`". It does not, and the adversarial pass proved it: `multiEntryRetractions` knows the
actor (`evaluate.go:768`) but returns `[]ruleengine.EvalResult`, whose four fields carry no actor
(`ruleengine/eval_result.go:16-38`); `reprojectActors` then flattens every actor's results into one slice
(`evaluate.go:1069`, `:1087`); and the write itself happens in `writeResults` (`pipeline.go:3360`, loop at
`:3379-3389`), where per-result actor attribution no longer exists. The file says so about its own sibling
problem at `pipeline.go:3477-3483`.

The mechanism that **does** exist is `OutputDescriptor.AnchorFromKey` (`projection/output.go:340-359`) — the
target-key → anchor-vertex-key inversion the `Sweeper` already uses to claim an orphan row (`sweep.go:619`,
`:882`). The signal routes through it: given the key just written, `AnchorFromKey` yields the actor. It is
fail-closed by construction — `ok=false` means "this lens does not own that key", so no signal, and the sweep
covers it.

This is **not** the `anchorType` routing §4.2 rejects below: `AnchorFromKey` is a declared, per-lens structural
inversion of the lens's own key pattern, whose failure mode is a *missing* signal; `anchorType` is audit-only
body metadata whose failure mode is a signal delivered to the wrong place. (`EntryEnvelopeFn` also writes the
actor into the entry body at `driver.go:242`, which would serve the upsert direction — but not a purge or a
tombstone-of-an-absent-key, so it cannot be the uniform answer.)

**The classification.** A pipeline forwards the signal only when its own compiled plan says it is a read-grant
producer: `plan.AuthPlane` **and** per-entry (`EntryKeyColumn != ""`) **and** its `OutputDescriptor.KeyPrefix`
begins with the prefix constant `capabilityread` itself uses to build its reader filter
(`capabilityread.go:44-49`). Exporting that one constant and having both sides consume it is the point: the
producer classification and the reader's key construction cannot drift apart, because they are the same
literal. Derived from plan data, never a canonical-name list — the same posture `SetGuarded`/`SetSweepPlan`
already take.

**The sink.** `Pipeline` gains an optional injected sink:

```go
type GrantChangeSink interface{ GrantChanged(actorKey string) }
```

installed by `projection/driver.go` for exactly the lenses the classification admits. Absent sink ⇒ no signal
⇒ Increment 2's sweep is the only healer. That is fail-**slow**, never fail-open: a forgotten sink costs
latency, never a grant that is honoured after revocation, because the gate itself is unchanged.

**The fourth arm — a truncating rebuild is a bulk revocation with no `guardedWrite` at all.** `Truncate` lists
its keys and `Purge`s them directly (`natskv.go:533-546` → `truncateKeys` `:548-563`), so nothing in the table
above ever runs for a purged grant. That path is not exotic: a `rebuild(truncate=true)` on a producer
(`pipeline.go:2669`, `:2707`) is the operator remedy §1.3 already names, **and** a MATCH hot-reload that
*narrows* a producer's own cypher owes a truncating rebuild automatically (`reload.go:462-473`) — which is
precisely a revocation-shaped change. Left unhandled, every grant a narrowing deploy revokes would fall
through to Increment 2's cycle time.

So `Truncate` on an auth-plane per-entry lens signals too: `truncateKeys` already returns the key list it
purged, so each key inverts through the same `AnchorFromKey` and every affected actor is enqueued. One
mechanism, four write paths, no special case in the consumer.

**The consumer.** One process-level reprojector holding a coalescing dirty-actor set, drained by a single
worker on a short ticker, calling `ReprojectPersonalActor` on **every registered personal pipeline** for the
actor. My first draft said it could reuse the hydrator registry the `personal.hydrate` op iterates and that
"nothing needs a second list". Both were wrong: `control.Hydrator` is a **one-method** interface
(`control/service.go:161-163`) and `personalHydratorByRuleID` is unexported behind `s.mu` with no iterator
(`:312`, `:471-482`), deliberately, so that `internal/control` need not import `internal/pipeline`
(`:165-173`). **The reprojector owns its own registry**, populated from `cmd/refractor` at the same site that
already registers the hydrator (`cmd/refractor/main.go:1401`), where the concrete `*pipeline.Pipeline` is in
hand. It is a second list, it is Refractor-internal, and it crosses no control-plane boundary — which is the
cheaper half of the two shapes the codebase already offers (the other being `control.Reprojector` +
`reprojectorByRuleID`, `service.go:165-173`).

**Why the whole actor, and every lens.** A grant transition names one anchor, and each personal lens projects
a known anchor type (Census 1) — so routing by the entry's `anchorType` would cut the fan-out 15×. Rejected:
`anchorType` is normatively **audit-only** metadata (Contract #6 §6.14, *"never part of the membership
match"*), and a wrong or absent value would route a retraction to no lens at all — a silent over-grant, the
worst direction, in exchange for CPU. Reproject the actor on all 15; the cost of one reaction is exactly the
cost of one `personal.hydrate`, which the platform already pays on every device attach.

### 4.3 Increment 2 — the personal convergence sweep

A `PersonalSweeper`: one shared ticker that walks the `identity` population in bounded batches and calls
`ReprojectPersonalActor` for each on every registered personal pipeline.

**It is a new sweeper, not a reuse — say so plainly.** `Sweeper.survey` hard-fails without a target key lister
(`sweep.go:759-767`, `errSweepNoKeyLister`), and `NatsSubjectAdapter` is not one; `candidates`
(`sweep.go:786-930`) is built almost entirely out of the target listing — covered/orphan grouping, three
reserved quota directions with their own cursors, per-actor backoff, the verdict sets. **Exactly two things
transfer**: the Core-KV anchor population walk (`sweep.go:740-745` — `VertexPrefix + AnchorType + ".*"` through
`coreKV.ListKeysFilter`, G9a) and the deep-verify round-robin cursor. Everything else is written fresh. That
is the honest size; "mirroring the discipline" would have read as reuse.

Two deliberate differences from what the auth plane runs:

- **No orphan direction, and none needed.** The existing sweep enumerates target keys to find strays
  (`sweepEnrolment`'s third conjunct, G9). The authoritative frame *is* the stray-killer, evaluated on the
  device: every key not in the frame is pruned (G4). This is precisely why the exclusion at G8/G9 is no longer
  binding — and why it was correct before R1 shipped.
- **One sweeper for all personal pipelines, not one per lens.** Fifteen independent tickers would multiply the
  identity walk fifteen-fold for one shared population.

**Defaults, and the arithmetic behind them.** `PersonalSweepInterval = 60s`, `PersonalSweepBatch = 5`
identities per tick, both deployment-overridable like `DefaultSweepInterval`/`DefaultSweepBatch`. That is
5 × 15 = **75 cypher evaluations per minute** (≈1.25/s), the same order as the auth-plane sweep's own
25 × 4 = 100/min. Full cycle = *N*/5 minutes: showcase-scale (dozens of identities) in minutes, a 10k-identity
cell in ≈33 h. **That cycle time bounds only the post-crash / un-signalled worst case**, because Increment 1
handles every transition the process observes; it is not the revocation latency.

**The enumeration is a second cost, and the batch knob does not bound it.** `survey`'s population listing is
`ListKeysFilter(..., limit 0)` — deliberately the *whole* population, unpaged (`sweep.go:741-745`). At the 10k
scale this section sizes, that is a 10k-key listing every tick on top of the 75 evaluations. Increment 2
therefore **caches the population between full cycles** and re-lists once per cycle rather than once per tick
(the cursor already implies a stable ordering over one cycle), and a mid-cycle identity that the cache misses
is picked up on the next — a delay the fast path already covers. Re-listing per tick is the shape to avoid,
not the shape to inherit.

**Population.** The `identity` vertex population, which is what `AnchorType` means for the existing sweep.
A tighter bound exists — only identities with a live device matter — but the Interest Set registration is
explicitly optional (an unregistered device still receives: `docs/components/refractor.md`, Interest Set), so
registration is not a sound census. The sound one is the live SYNC durable set, made trustworthy by
[edge-sync-orphan-expiry-design.md](edge-sync-orphan-expiry-design.md)'s `InactiveThreshold` — **shipped**
(`b9bf84ef`, 2026-08-12), so the precondition is already met. **Named future narrowing with a named
trigger:** when a cell's identity population materially exceeds its device population, narrow the sweep to
the live SYNC durable set. Not built now — the batch knob already bounds the cost, and the narrowing buys
nothing until that ratio bites.

---

## 5. New state, and its lifetime

Naming a data structure where a rule belongs is how an increment ships an unsound scope. Every field below
gets all four answers.

| State | Created | Reset | Carried across | Ordered relative to |
|---|---|---|---|---|
| **Dirty-actor set** (Inc 1, process-level `map[string]struct{}`) | At Refractor boot, with the reprojector | Emptied per drain tick, per actor, as each actor is taken | **Not** carried across a crash/restart — by design (§8.2); the sweep is the recovery | Insert-before-ack is irrelevant: the signal is in-process and synchronous with the producer's write, so an entry can only be lost with the process |
| **Set bound** (default 10,000 actors) | Same | On overflow, the *new* entry is dropped and a Health issue is raised (`refractor` component, Contract #5 §5.5) | n/a | Overflow must be **loud**: it means the sweep is the only healer for an unknown set, which an operator must be able to see |
| **Drain worker** | Boot | n/a | Stops on ctx cancel; in-flight actor abandoned | Serial: one actor at a time, all 15 pipelines sequentially, mirroring `Hydrate`'s per-identity loop. No new concurrency against the consumer goroutine beyond what `Hydrate` already establishes (G6) |
| **Sweep cursor** (Inc 2, position in the identity population) | Boot, at the start of the population | Wraps at the end of a full cycle | **Not** persisted — a restart re-starts the cycle from the beginning | A restart therefore re-verifies from the top, which is the safe direction (re-work, never a skipped segment). Mirrors `Sweeper`'s own in-memory cursor |
| **Sweep cycle counter / last-full-cycle timestamp** | Boot | On wrap | n/a | Published to Health KV so "the backstop is running" is observable, not assumed |
| **Cached identity population** (Inc 2, §4.3) | First tick of a cycle | Re-listed at each cycle wrap | Not across restart | A mid-cycle identity missed by the cache is picked up next cycle; the fast path covers it meanwhile |
| **Per-(lens, actor) publish mutex** (§4.1.1) | Lazily, keyed, in the reprojector | Released at the end of each evaluate → write → publish | Not across restart (no cross-process claim) | Held **across** the revision capture, so the three reprojection publishers (drain worker, sweeper, `Hydrate`) cannot interleave frames for one (lens, actor). Live CDC frames stay outside it and remain monotone by consumer ordering |
| **Per-actor drain error** | — | — | — | On a `ReprojectPersonalActor` error (e.g. a `capability-kv` read fault surfacing through `IsReadable` → `personal.go:177-180` → the whole-evaluation abort at `evaluate.go:638-652`): log at Warn, raise the same Health signal the overflow row uses, **continue to the remaining pipelines for that actor**, and do **not** re-enqueue — a persistent fault would otherwise spin the drain. The actor falls to the sweep, and the Health signal is what makes that visible rather than silent |

**Rule-swap ordering.** `ReprojectPersonalActor` takes its own `ruleState()` snapshot (G6). A rule replaced
mid-drain means the actor is reprojected under whichever snapshot it took; the next transition or sweep pass
corrects it — the identical posture `Hydrate` runs under, deliberately, and no weaker.

**Concurrency, checked rather than assumed.** Every piece of per-pipeline mutable state a second concurrent
per-actor reprojection would touch is already behind its own lock — `ruleState()`/`ruleMu`
(`pipeline.go:1114-1116`), `currentAdapter()`/`adapterMu` (`:2088-2092`), `Progress()`/`progressMu`
(`:477-481`), `peakRowsBuf` (`peakrows.go:57`), `latencyBuf` (`latency.go:23`), the derivation shadow
(`anchor_derivation_shadow.go:89`) — and `full.Engine` is stateless (`ruleengine/full/full.go:24-26`). Worth
stating plainly, though: **`Hydrate` is not itself a precedent for *concurrent* execution.** Each control op
is one `micro` endpoint on one subscription (`control/service.go:849-861`), so two hydrates for one pipeline
have never overlapped in production. Increment 1 is the first caller that can genuinely run beside the
consumer goroutine and beside another reprojection, which is why the field-by-field check above is in the
design rather than left to the build, and why §4.1.1's keyed mutex exists.

---

## 6. Reconciliation with the existing mental model

**"Didn't we already fix personal retraction?"** R1/R2 fixed the *transport* — a row that stops matching the
cypher is now retracted by frame diff. This is a different producer of the same symptom: the row still matches
the cypher; the **security filter's input** changed underneath it. R1's own §9 lists this case as an accepted
risk (§1.2), which is exactly the boundary between the two designs.

**"Doesn't the sweep already cover the auth plane?"** It covers the *producers* — it re-verifies that
`cap-read` rows are right. It has never covered the *consumer*, because the consumer was never enrolled (G8).
Convergence of an input is not convergence of the thing that reads it.

**"Doesn't this duplicate the plain-lens standing healer?"**
[plain-lens-neighbour-anchor-derivation-design.md](plain-lens-neighbour-anchor-derivation-design.md)
(📐 awaiting-Andrew) reasons about the same class — a narrowing/gating that removes an accidental heal needs a
standing healer — for the **plain** pipeline, and concludes a plain lens *cannot* have the ratified one
(`Reproject` is envelope-only; that design's §5.2 builds an enrolled Auditor instead). The two do not collide:
different pipeline family, different mechanism, and the resolution here (the frame supplies the orphan
direction) is structurally unavailable to a plain lens, whose target has no client-side reconciler. **If both
ratify, the Steward should build them in either order and share the batch/interval constants rather than
minting a third pair.**

**"Does this introduce new state we already keep somewhere?"** The dirty-actor set is genuinely new (§5). The
sweep cursor is a second instance of a pattern `Sweeper` already owns, and should reuse its shape.

**"Is a projection reading another projection an architecture violation?"** No, and this design does not
introduce it — `capabilityread` has read `capability-kv` since Fire PL.3, and the D1 design ratified that
shape. P5 constrains **applications**; Refractor is the projector, and `capability-kv` is a Refractor-owned
target. What was missing was the *change edge* on a dependency that already existed. No feedback loop is
created: personal lenses write to `nats_subject` only, so a grant-triggered reprojection can never produce
another grant transition.

**"Is this the read-path mirror of something on the write path?"** Yes, and the asymmetry is the point: the
Processor reads Capability KV **synchronously at commit** (`step3_auth_capability.go`), so a write-path
revocation takes effect on the next operation with no notification needed. The personal plane is **push**, so
it needs one. That difference is why a gap could sit here unnoticed while the write path was fine.

---

## 7. Contract surface

**No frozen-contract change.** Checked, section by section:

- **Contract #6 §6.14** defines the `cap-read.*` key space, the per-anchor entry shape, and the membership
  join. This design changes none of them; it adds a Refractor-internal reaction to a write §6.14 already
  specifies. §6.14's normative "`anchorType` is audit-only, never part of the membership match" is *honoured*
  — §4.2 explicitly refuses to route on it.
- **Contract #6 §6.2** (projectionSeq guard) is untouched: the transition verdict is derived from the write
  `guardedWrite` was already going to make, and a declined write produces no signal.
- **Contract #5 §5.5** is the existing alert convention the overflow/sweep-health surfaces use; no new clause.

Three contract edits are already staged uncommitted in `main` for other designs (`#1`, `#2`, `#6 §6.1`). This
design deliberately adds no fourth to that shared tree.

---

## 8. Alternatives considered

### 8.1 A `capability-kv` watcher (the shape I started with, and why it lost)

Refractor opens `SubscribeKVChanges(ctx, "capability-kv", []string{"cap-read."}, …)` and reacts to key changes.

**It would work.** The permission envelope allows it (G15 — `protectedStreamDenies` covers stream-admin verbs
only; refractor's subscribe grant is the unrestricted default), the primitive is bucket-generic and creates
only a consumer, never a stream (G14), and there is a topological precedent: **Weaver already consumes a
Refractor lens output bucket's backing stream** (`$KV.weaver-targets.<targetId>.>`, `weaver/engine.go:312`,
`:413`). I record all three because the rejection must be on merits, not on a false impossibility.

**Why it loses, quantified.** The guarded write path deliberately re-writes an unchanged body to advance the
watermark, and the comment at `natskv.go:175-184` states that it must never gain the content-skip the
unguarded path has (G11/G12). So a watcher cannot distinguish a grant flip from a watermark advance. The
auth-plane sweep alone re-projects **up to 25 actors a minute per producer lens** (G10 — the batch is
per-pipeline), and there are four producers (Census 2), so up to 100 distinct actors a minute. At 15 personal
lenses per actor (Census 1) that is **up to ≈1,500 pointless cypher evaluations a minute**, permanently — a
15× multiplier on an already-multiplied cost, arriving as an *accidental* coupling to another lens family's
cadence rather than a tunable healer. The transition filter is the fix, and the transition is only derivable where both bodies are
in hand: `guardedWrite` (G13).

**Two variants also rejected.** A watcher with an in-memory per-key digest cache recovers precision at a
memory cost proportional to total grant cardinality (actors × granted anchors), which is the one quantity the
per-anchor key design exists to keep off the heap. A watcher that reprojects only on `isDeleted:true` bodies
cannot tell a fresh tombstone from a re-write of an old one, and misses the growth direction entirely.

**What the watcher would have bought — durability — is bought in §8.2 instead, more cheaply.**

### 8.2 A durable signal stream (`lattice.refractor.capread.changed.>`)

Producer-side transition detection, published to a JetStream subject and consumed by a durable, so a crash
between the write and the reprojection is recovered exactly. Refractor may publish `lattice.refractor.>`
(`natsperm/matrix.go:175`), so this is available.

**Rejected: it buys exact recovery for one narrow window at the cost of a new platform stream** — bootstrap
provisioning, retention and byte limits, a new health surface, and a new thing to expire (the board already
carries an unbounded-SYNC-stream row). Increment 2's sweep covers the same window as a *general* healer that
also covers the rollout population, a missing sink, an overflowed set, and any future consumer of the D1
projection. One mechanism, more coverage, no new substrate object. **Prevention best-effort, detect-and-recover
authoritative** — the platform's own posture.

**Revisit trigger, named:** if a measured cycle time makes the post-crash window unacceptable for a real
deployment, this is the increment to add, and it slots in behind the same `GrantChangeSink` interface with no
change to §4.1 or §4.3.

### 8.3 Express read-authorization inside each personal lens's cypher

Make the grant part of the walked Core-KV subgraph, so ordinary CDC drives it and no cross-projection
dependency exists at all.

**Rejected:** it would duplicate package-owned grant logic into all 15 consumer lenses and directly contradicts
§6.14's contract-contribution decomposition (*"each vertical owns its own read-grant lens"*, which is exactly
why `IsReadable` must discover domains with a wildcard filter rather than a fixed list,
`capabilityread.go:6-11`). It also would not fix the race (§1.2): both pipelines would still evaluate
concurrently against the same event with no ordering.

### 8.4 Have the server nudge the device to re-hydrate

Publish a "your grants changed, re-hydrate" message on the identity's SYNC subject; the client calls
`personal.hydrate`.

**Rejected on three counts.** It costs a *full* hydrate (all lenses, all rows, plus the terminal marker) where
a targeted reprojection suffices; it depends on a client-side change, so an un-upgraded or ignoring client
silently keeps a revoked row — a security mechanism whose enforcement lives on the device is not one; and it
does nothing for a device that is offline when the nudge is published. §4.1 is strictly a subset of the work
and lands entirely server-side. (Note the platform already has the *durable* form of this idea —
`personal.requesthydration`, §1.3 — and its limitation is exactly the "only at next attach" one.)

### 8.5 Do only Increment 2 (the sweep), no notification edge

**Rejected on the growth direction's UX, not on correctness.** A newly-granted staff member would wait up to a
full sweep cycle to see their queue — hours on a large cell. The claim beat this whole feature family exists to
serve (`facet-staff-worlds-design.md`) is a real-time interaction. The sweep is the right *backstop* and the
wrong *path*.

### 8.6 Do only Increment 1 (the edge), no sweep

**Rejected:** an in-process best-effort signal with no standing healer is exactly the shape the platform keeps
being corrected on. It also leaves the pre-existing stale population at rollout with nothing to converge it.

---

## 9. Risks

- **A retraction frame the client's own guards discard.** The largest thing the adversarial pass found, and it
  reshaped §4.1: capture-before would have made every grant-triggered frame under-claim by construction, and
  both client guards (`bolt.go:208-210`, `:291-294`) then fail in the **over-grant** direction. Resolved by
  capturing after and serializing the reprojection publishers (§4.1.1). The residual — an in-flight CDC
  evaluation that started pre-tombstone and acks after us — is real, is **not** bounded by one evaluation
  duration (ack-gated, stretchable by CAS contention or retry backoff), and is asserted under contention by
  T5, not on the fast path.
- **Fan-out cost on a mass grant change.** A package upgrade that re-derives every actor's grants produces a
  transition per actor, each costing 15 evaluations. Bounded by the coalescing set and its drain rate, and
  visible: the drain-queue depth is a published gauge. This is the number the build must **measure**, not
  assume (§10, M1).
- **Overflow silently degrading to sweep-only.** Refused by construction: overflow raises a Health issue (§5).
- **Multi-instance Refractor (HA future).** The signal is in-process, so in a partitioned-lens HA deployment a
  producer and a personal pipeline could land on different instances and the edge would not cross. The sweep
  still covers it. Inherited by the ratified HA-NATS design as a named obligation — the same treatment
  `personal-lens-retraction-design.md` §9 gave frames.
- **A future second consumer of `IsReadable` forgetting to enrol.** This is the failure mode being fixed, and
  a migration does not bind the next author — so the gate ships with the design (§10.1 / §11 Inc 1).

---

## 10. Test strategy

| # | Proves | Shape |
|---|---|---|
| T1 | The transition matrix (§4.2) — all five rows, plus declined-by-watermark | Unit, `internal/refractor/adapter`, against the scripted `kvStore` fake the CAS branches already use (`natskv.go:53-56`) |
| T2 | Only a `cap-read.`-prefixed auth-plane per-entry lens gets a sink; a `cap.roles.*` write-plane producer gets none | Unit, `internal/refractor/projection` — classification derived from plan data |
| T3 | `ReprojectPersonalActor` on a **missing** identity publishes the empty frame rather than erroring (the §4.1 divergence from `Hydrate`) | Unit, `internal/refractor/pipeline` |
| T4 | **The headline, both directions, with no other event:** grant lands ⇒ the row appears; grant revoked ⇒ the next frame omits the key and the client prunes | e2e on the ephemeral stack, `internal/refractor` — a real `cap-read` write, no Core-KV event on the personal lens's own subgraph. Must be **mutation-tested**: disable the sink and confirm T4 fails, or it proves nothing |
| T5 | **The revision posture, both directions.** (a) A grant-triggered retraction frame is *applied* rather than dropped by `frameHW`, and its target key is *not* exempted by `collectAttributed` — i.e. the revocation actually prunes; (b) a genuinely fresher live delta still wins. Asserted **under CAS contention / retry backoff**, not on the fast path | Unit against the store's guards (`edge/store/bolt.go`) + e2e on frame `revision` ordering |
| T5b | Two reprojection publishers racing one (lens, actor) cannot interleave frames — the keyed mutex holds across the revision capture | Unit, `internal/refractor` — drain worker vs sweeper vs `Hydrate` |
| T6 | With the sink disabled, a flipped grant converges within one sweep cycle | e2e with `PersonalSweepBatch`/`Interval` shrunk (the same override discipline `PruneStaleDurableAge` uses) |
| T7 | The §10.1 gate fires on a new unannotated `IsReadable` call site | `scripts/lint-conventions.go` self-test |
| T8 | A **truncating rebuild** of a producer enqueues every actor whose keys it purged (§4.2's fourth arm) — the `matchShrank` path included | Unit + e2e; the `matchShrank` case drives it through `reload.go`'s narrowing arm |
| T9 | **Invariant:** a `KeySetPublisher`-targeted pipeline never produces a `Delete`-shaped `EvalResult` | Unit. Today this holds only by scattered convention (`evaluate.go:216-222`, `:1046-1052`; `driver.go:400` arms zero-row retraction only for actor-aggregate). If it were ever broken, `ApplyDelete` (`edge/store/bolt.go:174-193`) clears **every** lens's attribution for the key — one lens's tombstone wiping a sibling's live grant on-device, the exact shape this design exists to prevent. Pin it while the reasoning is fresh |
| T10 | A `ReprojectPersonalActor` error for one pipeline does not abort the actor's remaining pipelines, and raises the Health signal (§5) | Unit |
| M1 | **Measurement, not a test:** drain-queue depth and reactions/minute on the showcase stack over one auth-plane sweep cycle, recorded in this doc as a §measurement table at build | Live observation, Increment 1 close |

**Owner note:** every test above is owned by a named increment in §11 — none is unowned.

### 10.1 The gate that binds the next author

`scripts/lint-conventions.go` gains a check that **default-denies** any executable call site of
`capabilityread.IsReadable` that does not carry a declaration comment:

```go
// grant-change-posture: (subscribed | swept | none-justified: <reason>)
```

Mirroring the shipped `# read-posture:` convention (`scripts/lint-conventions.go:262`, `readPosture`): the
gate does not *classify* — it makes the author declare, and forgetting fails closed. Census 4 (§3) shows
exactly one call site today, so the migration leaves **zero** debt and the gate therefore ships **blocking**.
That is a deliberate departure from its model, which landed warn-first (`lint-conventions.go:613-614`) —
warn-first was right there because it met a corpus of existing debt, and wrong here because a warn over a
clean tree is exactly the fingers-crossed state the fire exists to end. It is ~30 lines and it is the only
thing that stops the second consumer of the D1 projection from reproducing this bug — which is precisely how
this bug got here.

---

## 11. Decomposition for the Steward

**Increment 1 — the notification edge (M). Posture-changing: yes (security plane) ⇒ full review depth.**

1. `guardedWrite` parses the stored body's `isDeleted` (new — G13) and returns the liveness verdict on the
   `guardVerdict`, **err-first** and **independent of `Wrote`** (G13d). **T1** covers all nine rows of §4.2's
   table, including `guardDroppedNoToken` and the five error exits.
2. `writeResults` asserts an outcome-carrying deleter alongside `OutcomeUpserter` (`pipeline.go:3372`,
   `:3382`) so the **revocation** direction has a channel at all (G13c). Without this step Increment 1 ships a
   working grant-lands trigger and a dead grant-revoked one.
3. `GrantChangeSink` on `Pipeline`; the write path routes the written key through
   `OutputDescriptor.AnchorFromKey` (§4.2) to get the actor. Fail-closed on `ok=false`.
4. `Truncate` on an auth-plane per-entry lens enqueues every purged key's actor through the same inversion
   (§4.2's fourth arm). **T8** — including the automatic `matchShrank` narrowing-rebuild path.
5. `capabilityread` exports the `cap-read.` prefix constant; `driver.go` classifies producers from plan data
   and installs the sink. **T2.**
6. `Pipeline.ReprojectPersonalActor` (§4.1), revision captured **after** the reprojection, under the keyed
   (lens, actor) mutex (§4.1.1). **T3**, **T5**, **T5b**.
7. The reprojector: its **own** per-ruleID registry of personal pipelines, populated from
   `cmd/refractor/main.go:1401` (§4.2 — not the control-plane hydrator registry); coalescing set (§5), drain
   worker, per-actor error posture. **T10.**
8. The `lint-conventions` gate (§10.1). **T7.**
9. **T4** (mutation-tested) + **T9**; **M1** recorded at close.

*Independently shippable and green:* yes — it closes the observed failure on its own; Increment 2 adds
convergence, not correctness of the fast path.

*Sizing note after the adversarial pass:* steps 2, 4 and 7 are all work the first draft did not have, because
it assumed a delete outcome existed, that `Truncate` went through the guard, and that a registry could be
reused. Increment 1 is a **solid M**, not a small one; if the Steward wants a smaller first fire, the clean
cut is steps 1–3 + 5–6 + 9 (the guarded-write arm end to end) with step 4 as its own follow-on — but **not**
step 2, which is load-bearing for the revocation half.

**Increment 2 — the personal convergence sweep (M). Posture-changing: no ⇒ the Steward's ordinary sizing.**

1. `PersonalSweeper` over the identity population, batch/interval constants, one instance for all personal
   pipelines, population cached per cycle rather than re-listed per tick (§4.3). It reuses `Sweeper`'s
   Core-KV population walk and round-robin cursor and **nothing else** — `survey`/`candidates` do not transfer
   (G9b).
2. Health surface: sweep cursor, last-full-cycle timestamp, drain-queue depth, overflow issue. Every one of
   these needs a **reader** — wire them into the refractor heartbeat, not just the struct.
3. `docs/components/refractor.md`: the D1 dependency edge, the personal sweep, and the sweep-exclusion prose at
   G8/G9 rewritten (it currently reads as a permanent property).
4. **T6.**
5. The `Review keeps catching` dossier entry for Refractor: *"a projection read as a decision input by another
   projection, with no change edge."*

**Sequencing:** 1 → 2. Increment 1 is not dead scaffolding — its consumers are the 15 shipped personal lenses
and the live Facet client (`ApplyKeySet`, G4), both in production today.

---

## 12. Open questions — resolved

| Question | Resolution |
|---|---|
| Watcher or producer-side signal? | Producer-side (§8.1) — the watcher cannot see a transition, and the sweep's rewrite rate makes the imprecision permanent, not occasional |
| Durability of the signal? | The sweep, not a persisted queue or a new stream (§8.2), with a named revisit trigger |
| Route by `anchorType` to cut the 15× fan-out? | No (§4.2) — audit-only metadata, and a mis-route is a silent over-grant |
| Widen `Reproject` or add a sibling? | Sibling (§4.1); `Reproject`'s refusal is about a reconciliation model that genuinely does not fit |
| Emit `hydrationComplete` on a grant-triggered reprojection? | No (§4.1) — it gates first paint |
| Sweep the identity population or the device population? | Identity population now; device population named as a future narrowing gated on the orphan-expiry design landing (§4.3) |
| Blocking lint gate or warn-first? | Blocking (§10.1) — the migration leaves zero debt |
| Capture the frame revision before or after reprojection? | **After**, under a keyed (lens, actor) mutex (§4.1.1) — capture-before makes a retraction unable to retract |
| Reuse the control-plane hydrator registry? | No (§4.2) — one-method interface, unexported, deliberate architecture boundary; the reprojector owns its own list |
| Route the signal by parsing the key, the entry body, or `anchorType`? | `OutputDescriptor.AnchorFromKey` (§4.2) — the sweep's own inversion, fail-closed, uniform across upsert / tombstone / purge |
| Does `Truncate` need its own arm? | Yes (§4.2) — it bypasses `guardedWrite` entirely, and a narrowing MATCH reload drives it automatically |

---

## 13. The adversarial pass — RUN, and what it changed

Two independent reviewers (mechanism-soundness and security/over-grant lenses), read-only against the code,
2026-08-11. **This section records the gate as discharged; the design is not build-ready without it.**

**Confirmed sound and left alone** (worth knowing, because the design's cheapness rests on them): the D1 gate
cannot be bypassed by the new entry point — `reprojectActors` applies the same `personalEnvelopeFn` closure
whatever the caller (`evaluate.go:638-656`, `personal.go:129`); an **empty** keyset frame really does retract
everything for that lens, with no early-return anywhere in the chain (`sync.go:737-751` →
`edge/store/bolt.go:197-229`); per-lens attribution means one lens's frame cannot prune a sibling's key
(`bolt.go:284-322`); the missing-actor branch already produces no result for a personal target
(`evaluate.go:1046-1052`) and `PublishKeySet` accepts an empty frame explicitly (`natssubject.go:301-304`);
and the population walk Increment 2 needs is genuinely Core-KV-sourced (`sweep.go:740-745`).

**Four blocking findings, all folded above, none of which the draft would have survived:**

| # | What the draft claimed | What the code said | Where it now lives |
|---|---|---|---|
| B1 | The write loop already knows the `actorKey` | `EvalResult` carries no actor and `reprojectActors` flattens across actors | §4.2 routes via `AnchorFromKey` |
| B2 | `UpsertOutcome`/`DeleteOutcome` carry the verdict | `writeResults` asserts `OutcomeUpserter` only; `Delete`'s outcome is discarded — the **revocation** half had no channel | §11 Inc 1.2, G13c |
| B3 | Reuse the hydrator registry; "nothing needs a second list" | One-method unexported interface behind a deliberate architecture boundary | §4.2, §11 Inc 1.7 |
| B4 | (unstated) — `Truncate` was invisible to the design | Purges bypass `guardedWrite`; a *narrowing* MATCH reload drives a truncating rebuild automatically | §4.2's fourth arm, G13a/G13b, T8 |

**And the one that reshaped the mechanism rather than the plumbing:** the client's `frameHW` and
`collectAttributed` guards mean a frame that under-claims its revision cannot retract — so copying `Hydrate`'s
capture-**before** posture would have shipped a revocation path that silently fails in the over-grant
direction, in both increments. §4.1.1 is entirely a product of that finding.

**The lesson, generalized** (for `agents/designer/SKILL.md` and the Refractor dossier): *a design that reuses a
publish/notify path for a **retraction** inherits that path's freshness posture — and an under-claiming
revision is safe for a snapshot and fatal for a retraction.* The same words ("publish the authoritative
frame") describe two different jobs, exactly like the RLS-anchor and "guard" precedent-transfer failures
before it. Ask what the *reader* does with the number, not what the writer meant by it.

---

## 14. Increment 1 fire brief (build note, 2026-08-14)

Phase-0 scout re-verified every G1–G16 citation and all four censuses live against `main` (1 day post-
ratification): all four censuses match exactly (15 / 4 / structural / 1), no proposed symbol
(`GrantChangeSink`, `ReprojectPersonalActor`, `PersonalSweeper`) is pre-built, and `guardVerdict` is
pre-existing infrastructure the design extends, not new. A handful of line numbers drifted (routine edits to
`evaluate.go`/`driver.go`/`pipeline.go`/`main.go` since ratification) — corrected below; no cited *fact* was
false.

**1. Scope sentence (verbatim, §11 Inc 1).** *"Increment 1 — the notification edge (M). Posture-changing: yes
(security plane) ⇒ full review depth."* Intent (§1.4): a `cap-read` grant transition re-drives the affected
actor's personal pipelines promptly; `IsReadable` stays the boundary, it simply stops being asked only at
arbitrary CDC-driven times.

**2. Verified touch-list (corrected `file:line`, checked live this fire):**

| File | What | Corrected anchor |
|---|---|---|
| `internal/refractor/adapter/natskv.go` | `guardedWrite` gains the liveness-transition verdict | `:316-363` (unchanged) — insert the stored/incoming `isDeleted` parse after the `Get` at `:329`, before the create/decline/update branches |
| … | `storedProjectionSeq` — model for the new `storedIsDeleted`-shaped parse | `:388-412` (unchanged) |
| … | `Truncate` / `truncateKeys` — the fourth signal arm | `:533-546` / `:548-563` (unchanged) |
| `internal/refractor/adapter/adapter.go` | `OutcomeUpserter` / `OutcomeDeleter` interfaces — **already defined**, `DeleteWithOutcome` **already implemented** on `NatsKVAdapter` (`natskv.go:23-24`, `:240-242`) | `:174-176`, `:221-223` |
| `internal/refractor/pipeline/pipeline.go` | `writeResults` — delete arm calls plain `adpt.Delete` (discards outcome); `wrote` stays hard-`true` for every delete | `:3428` (fn), delete arm `:3448-3450` (was cited `:3372/:3382`, drifted) |
| `internal/refractor/pipeline/hydrate.go` | `Hydrate` — the mirror pattern for `ReprojectPersonalActor` | `:33-95` (was `:35-95`, off by 2) |
| `internal/refractor/pipeline/evaluate.go` | Personal lens D1-skip is the **single-`envelopeFn`** arm (personal lenses use `SetEnvelopeFn`, not `SetMultiEnvelopeFn`) — skip-continue `:712-713`, append `:729-732`; `emitPersonalFrames` | `:712-732` (was cited `:639-641/657`, wrong arm — multiEnvelopeFn's arm is `:696-708`, not personal's); `emitPersonalFrames` fn at `:1190-1219` (was `:1118-1147`) |
| … | called from | `pipeline.go:3576` (`p.emitPersonalFrames(ctx, adpt, enumeratedActors, results, msg.Sequence)`) |
| `internal/refractor/capabilityread/capabilityread.go` | `perAnchorBaseKey`/`perAnchorDomainFilter` build `"cap-read."` inline — **no exported prefix constant exists yet**; step 5 must add one | `:44-49` |
| `internal/refractor/projection/driver.go` | `InstallActorAggregate` — `authPlane := plan.AuthPlane` (`:356-357`) is the classification site; `sweepEnrolment` def `:309` (was `:425`), sole call site `:426` (was `:425`) | `:336-386` install body |
| `internal/refractor/projection/personal.go` | `InstallPersonalLens` (never calls `SetSweepPlan`); `personalEnvelopeFn`'s D1 gate | `:106-135`; gate `:157-184`, `IsReadable` call `:177` (Census 4 confirmed: only call site) |
| `internal/refractor/projection/output.go` | `AnchorFromKey` (recovers the owning actor's full Contract #1 vertex key, e.g. `vtx.identity.<id>` — NOT the bare NanoID `ReprojectPersonalActor` takes; caller must `substrate.ParseVertexKey` it) / `KeyPrefix` | `:359-389` / `:287-299` |
| `cmd/refractor/main.go` | `controlSvc.RegisterPersonalHydrator(r.ID, p)` — the sibling site where the reprojector's own per-ruleID registry populates | `:1556` (was cited `:1401`, drifted) |
| `cmd/refractor/reload.go` | `matchShrank` narrowing detection feeds `markTaxonomyRebuildPending` | `:502`, `:572` (was `:462-473`); the `Truncate` call itself is `pipeline.go:2775` inside `resolveTruncate`/rebuild (`:2720`, `:2758`) (was `:2669/2707`) |
| `internal/refractor/health/reporter.go` | Health-signal precedent for the overflow/error issue and (Inc 2) sweep progress | `SetSweepProgress` `:753-780` is the Inc 2 precedent; `RecordError`/`SetFilterState` (`:411-505`) are the precedent shape for a new per-actor-drain-error / overflow signal — **no existing "raise an issue" call in Refractor's own Reporter today; this is a new method on the same Reporter, not a new subsystem** |
| new file (greenfield) | `GrantChangeSink`, `ReprojectPersonalActor`, the reprojector (registry, coalescing dirty-actor set, drain worker, keyed `(lens,actor)` mutex) | no existing file owns this; mirrors `hydrate.go`'s shape + `sweep.go`'s ticker/cursor shape (Inc 2 only for the latter) |

**3. Precedents to mirror.** `ReprojectPersonalActor` mirrors `Hydrate` clause-by-clause per §4.1's table
(revision-after is the one deliberate divergence, §4.1.1). The drain worker's per-actor error posture (log +
Health signal + continue, no re-enqueue) mirrors `writeResults`' own `CatTransient`/`CatTerminal` handling
shape (`pipeline.go:3428+`) without reusing its retry queue. The registry-not-reusing-hydrator-registry
decision is already grounded (G-noted in §4.2): `control.Hydrator` is one-method/unexported by deliberate
boundary (`service.go:161-163`, `:312`).

**4. Increment order (design §11 Inc 1, steps 1→9), green check per step:**

1. `guardedWrite` liveness verdict (parse stored + incoming `isDeleted`, err-first, independent of `Wrote`) →
   **T1**: `go test ./internal/refractor/adapter/... -run TestGuardedWrite` (new/extended), all 9 table rows.
2. `writeResults` gains an `OutcomeDeleter` consultation alongside `OutcomeUpserter` — **this is the same
   code the already-filed board row "[Refractor] The CDC write path audits a retraction the ordering guard
   declined" (`lattice.md` Component maintenance) needs fixed; fold it in now** (`wrote` for the delete arm
   becomes `outcome.Wrote` instead of hard-`true`) → close that row in the same commit, don't file it
   separately.
3. `GrantChangeSink` interface + `AnchorFromKey`-routed write-path signal, installed by `driver.go`'s
   classification → **T2**.
4. `Truncate` fourth arm (purged keys → `AnchorFromKey` → enqueue) → **T8**, incl. the automatic
   `matchShrank` path (`reload.go:502`).
5. `capabilityread` exports the `"cap-read."` prefix constant; `driver.go` classifies from `plan.AuthPlane &&
   desc.EntryKeyColumn != "" && strings.HasPrefix(prefix, capabilityread.KeyPrefix)` → **T2** (full).
6. `Pipeline.ReprojectPersonalActor` (§4.1), revision captured **after** reprojection, under the keyed
   `(lens, actor)` mutex (§4.1.1) → **T3, T5, T5b**.
7. The reprojector: own per-ruleID registry populated from `cmd/refractor/main.go:1556`'s call site;
   coalescing set + bound (§5) + Health overflow signal; drain worker; per-actor error posture → **T10**.
8. `lint-conventions` gate — default-deny an unannotated `capabilityread.IsReadable(` call site (Census 4
   pins today's single site) → **T7**: `go run ./scripts/lint-conventions.go` self-test.
9. **T4** (mutation-tested: disable the sink, confirm T4 fails) + **T9** (invariant: no `Delete`-shaped
   `EvalResult` reaches a `KeySetPublisher` target); **M1** (drain-queue depth / reactions-per-minute,
   recorded live at close).

**5. In-scope gotchas.** Standing checklist (all six apply): **(1)** new state → the §5 lifetime table is
already written in the design, build to it verbatim (dirty-actor set, bound, drain worker, publish mutex —
none skip the crash/restart row). **(2)** every census is a premise — Census 1–4 re-pinned above; re-run
Census 4's grep again right before step 8's gate lands (a second `IsReadable` site added mid-fire would
change the gate's "zero debt" claim). **(3)** T4 must be mutation-tested (disable the sink, confirm failure)
— do not accept a T4 that passes for the wrong reason. **(4)** removal/replacement — N/A, this is additive.
**(5)** one deterministic key, one writer — the keyed `(lens,actor)` mutex is exactly this: three publishers
(drain worker, sweeper, `Hydrate`) must serialize through it, never write around it. **(6)** precedent may
carry debt — `Hydrate` is *not* itself a precedent for concurrent execution against the same pipeline (§5
states this explicitly); verify every piece of per-pipeline state the design's own concurrency table lists
is actually lock-protected before assuming `Hydrate`'s shape is safe to run concurrently. Refractor's
"Review keeps catching" dossier is currently **empty** (all entries retired into mechanized gates) — nothing
to copy forward; this fire's own close pass may seed a new entry per §11 Inc 2 step 5.

**6. Adjacent finds.** One, and it is **absorbed into this run** (not filed): the `writeResults` delete-arm
audit bug (item 2 above) — same function, same lines, fixed by the same `OutcomeDeleter` consultation this
increment needs anyway. No other adjacent finds surfaced during grounding.

**7. Non-goals (design §"What I am NOT proposing", restated as the drift fence).** No change to what
`IsReadable` admits. No change to the frame wire format. No change to the D1 contract. No widening of
`Reproject` (`reproject.go:291-302` stays byte-for-byte). No `anchorType`-based routing (audit-only,
Contract #6 §6.14). No persisted/durable signal queue (§8.2 — the sweep is the durability story, and that is
Increment 2, not this one).

**Scope-diff gate: PASS.** Every touch-list entry traces to §11 Inc 1's nine steps; no substitution, no
widening. The one declared dependency (Increment 2's sweep as the durability backstop) is correctly *not*
load-bearing for Increment 1's own green bar (T1–T10, M1) — Increment 1 is independently shippable per
§11's own "yes" answer, confirmed by re-reading the code: nothing in the touch-list above requires
`PersonalSweeper` to exist first.

---

## 15. Increment 1 — SHIPPED (checkpoint, 2026-08-14)

**Merged to `main` at `b69487ef`** (9 commits, `136645e6..b69487ef`, +3762/−49 across 25 files). Landing
shape: **land each increment on `main` independently** (not hold-the-worktree) — Increment 1 is its own
green bar per §11's "independently shippable: yes" answer, so no persistent worktree carries forward; a
resumed fire for Increment 2 opens a fresh one per the usual convention.

**Review depth actually run** (full 3-layer, as the design's own header requires for a posture-changing
increment): three independent cold reviews (Blind Hunter / Edge-Case Hunter / Acceptance Auditor) against the
9-step build, zero CRITICAL findings, 4 MAJOR + 5 MINOR closed in one fix-round commit (`a18918e6`), a fourth
cold verification pass on that fix commit found one genuine (latent) regression in the new lint-gate's
import-qualifier resolver plus two doc issues, closed in a final commit (`b69487ef`). All four review passes'
full text and per-finding verdicts live in the fire's sub-agent transcripts, not duplicated here — this
section records outcomes and residuals only.

**What shipped, beyond §11's 9 steps (adjacent fold-ins, all reviewed and closed):**
- Closed board row *"[Refractor] The CDC write path audits a retraction the ordering guard declined"* — the
  same `OutcomeDeleter` consultation step 2 needed anyway. `wrote` for a KV-target delete now reflects
  `DeleteWithOutcome`'s real outcome for every adapter, not only guarded auth-plane ones — judged more
  correct by two independent reviewers; no test regressed.
- `Reproject` (the operator RPC / sweep deep-verify path) also signals the grant-change edge — not one of
  the original 9 steps, added because a healer that repairs a real grant flip without notifying the
  personal plane would leave a consumer unaware the healer acted. Verified volume-safe (gated on the
  transition, not `Wrote`) and verified not to touch `Reproject`'s `KeySetPublisher` refusal.
- `DeregisterPersonal`, wired to both pipeline-removal triggers — without it the drain would raise Health
  faults against a deleted lens forever.

**Known residuals, characterized precisely (corrected from an earlier, too-narrow description in a build
note that called one of these "sub-second" — it is not):**
1. **The registry-completeness gate's window is "Core-KV declaration → `RegisterPersonal`", the whole of
   `startPipeline`'s adapter/engine wiring in between — not a sub-second gap.** A lens hot-added or replaced
   after boot gets no protection for that window; a signal landing in it is lost with no healer until
   Increment 2's sweep ships. Practical impact is smaller than the window sounds, since a newly-installed
   personal lens does its own initial projection off current grants — but the window itself is real and is
   now documented in `registryIsReady`'s own doc comment, not only here.
2. **The registry-readiness gate reconciles corpus-globally (`ReconcileNow`), not narrowed to the lenses
   this reprojector actually drives** (the retention-class consumer's analogous gate deliberately narrows via
   `ReconcileNowForHolderType`; this one doesn't, because no narrowing primitive exists for "the lenses a
   personal reprojection needs"). Consequence, stated plainly in-code: any single unrelated lens that never
   registers makes readiness false forever, so `RegistryHoldMax` (2 min, now Health-signalled when it fires)
   is load-bearing rather than theoretical — **every process restart potentially eats a 2-minute hold on the
   first grant-change signal**, not only in a genuinely-still-loading registry. Named fix if this bites in
   practice: narrow the reconcile to the lenses this reprojector drives, mirroring the retention-class
   pattern.
3. **M1 (drain-queue depth / reactions-per-minute) is NOT recorded.** §11 Inc 1 step 9 and §14 part 4 step 9
   both require this as a live measurement over one auth-plane sweep cycle on a running stack — this fire
   built entirely against `go test` + embedded/ephemeral NATS per its own scope (no `make up`), so the number
   was never derivable here. **Needs a live showcase-stack observation before this line can be struck** —
   whichever fire builds Increment 2 (naturally exercising a live stack for its own e2e) should record it
   then, or a standalone measurement pass if Increment 2 is delayed.

**Increment 2 (the personal convergence sweep) is UNBUILT — this is where the next fire on this item
resumes.** §11's own decomposition (5 steps: `PersonalSweeper`, Health surface wiring, `docs/components/
refractor.md` update, T6, the dossier entry below) still applies unchanged; nothing in Increment 1's build
invalidated any part of it. No frozen-contract change, no architectural fork — ordinary Steward sizing
(§11: "Posture-changing: no").

**Refractor "Review keeps catching" dossier entry, added now** (`docs/components/refractor.md`, §11 Inc 2
step 5's entry, seeded early since Increment 1 is what actually closes the specific defect class): *"a
projection read as a decision input by another projection, with no change edge — check: does every producer
this lens depends on for AUTHORIZATION (not just anchor data) have a `grant-change-posture` (or equivalent)
declaration at its read call site, enforced by a blocking lint gate?"*
