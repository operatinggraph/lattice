# `loom-state` accretion — bound the ENUMERATION, keep the ledger

**Status: 📐 awaiting-Andrew (ratification).** Winston (Lattice Designer, unattended fire, 2026-09-01).

**Supersedes the pruning direction** of
[`loom-terminal-instance-retention-design.md`](loom-terminal-instance-retention-design.md) — whose §0 already
withdrew its own Inc 1/Inc 3 and re-filed the item as a designer pass naming an absent primitive
("durable id-scoped re-trigger dedup tombstone"). **This design declines to build that primitive**, on the
grounding below, and answers the item a different way.

---

## For Andrew

**What it does, in two lines.** The Loom instance cursor accretes forever and cannot be pruned — that part of
the row is right and this design does not fight it. What grounding shows is that the accretion's live harm is
not storage at all: three code paths **enumerate the whole bucket**, and one of them (`ListInstances`, behind
Loupe's Flows tab and `lattice loom list`) is already over its handler timeout on the live stack. So: point every
enumeration at a server-side subject filter that already exists in the substrate, add the one index that is
missing (`failed`), and let the terminal-complete residue be what it already is — durable dedup evidence that
is **never enumerated**.

**The fork I need you to rule on (§8).** The recommendation is *"do not prune; accept unbounded durable growth
in an engine's private KV, and name the ceiling."* That reverses the filed row's own prescription and it is a
platform-posture call, not a mechanism choice: it says a Loom instance leaves a permanent ~300-byte trace, so a
deployment running 10⁶ flows carries ~300 MB and 10⁶ KV subjects in `loom-state` forever. §8 prices the two
alternatives (build the retraction primitive now; prune unsoundly) and §9 names the exact primitive and the
exact trigger that should revive it. I recommend **not building it now** — it needs a cross-component
Weaver→Loom channel and its only payoff today is bytes — but the ceiling is inside a plausible production
regime, not only hyperscale, so it is your call and not mine.

**A live defect this fire found, outside the Lattice lane (§2.1).** Loupe's Weaver-detail view decides a
flow artifact is `Live` by a bare presence GET on the instance cursor, never reading its `Status` — so every
Weaver-triggered flow that completed or failed renders as **live, forever**. That is the *other* consequence
of "the cursor persists after terminal", it is invisible to the enumeration story, and it is `cmd/loupe/**`
(Stream 3). Filed as its own row on `backlog/loupe.md`; not built here.

**Frozen-contract change (§7).** One edit to `docs/contracts/10-orchestration-substrate.md`, in the
`loom-state` section, staged **UNCOMMITTED** in `main`. It states, as an observable promise, what the control
plane answers for (running + failed) and that the terminal-complete cursor is deliberately non-enumerable
durable dedup evidence. Today the code over-delivers relative to §10.9's own "which flows are running"
sentence, so the edit **narrows a promise nobody wrote down** rather than removing a stated one — but a
narrowing of an operator-visible surface is yours to sign, and it is the clause the next retention design must
not silently contradict.

---

## 1. The row, and the one sentence of it that grounding moved

The board row reads:

> **[Loom] Terminal instance cursors cannot be pruned — the dedup guard has no substitute.** `loom-state`
> accretes one `instance.<id>` forever. Cursor presence is the frozen-contract collapse point for Weaver's
> `triggerLoom` re-dispatch, whose horizon is unbounded, so no retention window suffices.
> `📐 needs designer pass · no-pattern: durable id-scoped re-trigger dedup tombstone`

Every factual clause of that row survives grounding. What does not survive is the implicit premise carried by
the `no-pattern:` prescription — that the *remedy* is a substitute dedup record. A tombstone that replaces the
cursor has the **same key cardinality** as the cursor; it bounds bytes, not keys, and keys are what hurts.
Re-deriving the need (below) points somewhere else entirely.

### 1.1 The horizon claim is confirmed, and now narrowed to one class

The retention design's §0 falsification is correct and the code says so precisely.

- `internal/weaver/actuator.go` derives **three disjoint identities** for one dispatch episode:
  `deriveEpisodeRequestID` is seeded on the **mark revision** (so it changes on every reclaim),
  while `deriveStableTaskID` and `deriveStableInstanceID` are seeded on the **`claimId`**, minted once at the
  mark's CAS-create and preserved across every reclaim. So a reclaim genuinely commits a **new op** (new
  `requestId` ⇒ no Contract #4 collapse) that emits a **new `patternStarted`** carrying the **same**
  `instanceId`.
- `internal/weaver/strategist.go` supplies that stable `instanceId` on every `actionTriggerLoom` plan.
- The collapse is frozen text: `docs/contracts/10-orchestration-substrate.md`'s `triggerLoom` clause, and the
  `instance.<instanceId>` entry in the same file's *Named constructs* block — *"The record persists after
  terminal: its presence is the dedup evidence that collapses a re-emitted trigger for the same instance."*

**Narrowing the row's "unbounded" to the class that actually produces it.** `internal/weaver/evaluator.go`'s
`externalDispatchGap` classifier splits `triggerLoom` by the pattern's own step kinds, and the two halves have
opposite reclaim semantics:

| Gap class | Reclaim's claimId | Consequence for `loom-state` |
|---|---|---|
| **External** (`directOp`/`proposedOp`; `triggerLoom` of an **externalTask-only** pattern) | a **fresh** claimId is minted (`evaluator.go`, the stale-mark branch) | a fresh `instanceId` ⇒ a genuinely new instance. Bounded by `maxretries_<g>`, and `GapBudgetExhausted` is a loud durable stop. **Not a source of re-triggers against an existing cursor.** |
| **userTask** (`assignTask`; `triggerLoom` of a pattern that parks on a human) | preserved **verbatim** | the same `instanceId` re-emitted on every lease expiry, for as long as the gap column stays open — **unbounded**. |

I had expected to find a contract violation here (a stable `instanceId` supplied to an externalTask-only
pattern would silently collapse a reclaim that §10.8 says is *intended* to be a new vendor call). The code
refutes it: the fresh-claimId branch exists and its comment names the exact hazard — *"Reusing the old claimId
here would seed the fresh triggerLoom dispatch with the SAME already-terminal Loom-instance identity."*
Recorded because a reader of §10.8 alone would draw my wrong conclusion.

**"Unbounded" is a package-authoring CHOICE whose only alternative is worse — the more useful framing for a
ratification.** A `maxretries_<g>` column declared on a row *does* bind any action, `triggerLoom` included
(`internal/weaver/evaluator.go`; the engine's `defaultDirectOpRetryBudget` fallback is gated to `directOp`
alone, so a userTask gap is uncapped only because nobody declared one). But if such a gap ever exhausts its
budget, the one un-park verb refuses it: `reArmDeclines` (`internal/weaver/control.go`) returns *"the sweep
never re-arms a collapse-only gap — a fresh episode would mint a new claimId and duplicate it"*, pinned by
`TestResetRetryBudget_RefusesACollapseOnlyGap`. A capped collapse-only gap therefore has **no operator remedy
at all**. `packages/lease-signing` made exactly this trade in the open: it *removed* the
`maxretries_onboarding`/`_signature` caps it once carried, because that cap was "create-once-FOREVER". So the
horizon is unbounded **deliberately**, the alternative policy is a permanent wedge, and no Loom-side design
should assume a future cap will bound it.

Steady-state pacing, for the scale arithmetic below: a collapse-only reclaim backs off exponentially to
`defaultReclaimBackoffCap = 24h` (`internal/weaver/reconciler.go`), so one open userTask gap costs at most one
wasted op + event per day — and, because it collapses, **zero** new `loom-state` keys.

So the unbounded producer is exactly: **a userTask-parking pattern triggered by a Weaver gap that stays open.**
And for that class the re-dispatch is a **provable no-op** — `internal/loom/engine.go`'s trigger handler finds
the cursor, sees terminal-or-running-with-a-step-in-flight, and `Ack`s. It has produced exactly one outcome,
every time, since the platform shipped.

### 1.2 The tracker cannot take the load — the 24h horizon is contract, not accident

The obvious "simplify the base" move is to make the collapse happen **upstream**, at the Processor, by seeding
`triggerLoom`'s `requestId` on the `claimId` too — there is even shipped precedent for exactly that shape in
the same file (`deriveProposalDispatchRequestID`, deliberately *not* mark-revision-seeded so a sweep reclaim
collapses on the tracker). **It does not work, and Contract #4 says why in as many words:** trackers carry a
**24-hour per-key TTL** (§4.3), and §4.3 closes with the sentence this whole item lives under —

> *"A flow that needs idempotency beyond the horizon (e.g. a pattern that sleeps for weeks) layers its own
> dedup on top of (or alongside) the tracker."*

The `loom-state` cursor **is** that layered dedup. A userTask gap open for longer than a day — the normal case
— would find the tracker expired and re-dispatch for real. Recorded as a refuted alternative (§8) rather than
left for the next fire to re-derive.

## 2. Re-deriving the need: what does the accretion actually cost?

Per the `no-pattern:` discipline — the prescription names the primitive *a particular solution shape* would
need; re-derive the need first. The need is whatever the unbounded key set makes worse. So: enumerate every
consumer of the key set, and price each.

| Consumer | Cost shape today | State |
|---|---|---|
| `runningInstanceCounter.count` (heartbeat, every 10 s) | `KVListKeysPrefix(bucket, "instance.")` → server filter `instance.>` → **matches cursors AND pins**, so every instance key is transferred, keys-only, per tick | **partially fixed** — the body fetch is gone (2026-08-29), the whole-keyspace transfer is not |
| `pinnedDomains` (every consumer reconcile) | `KVListKeys` (**whole bucket**) + client-side pin filter, then `KVGetMulti` over pins only | O(all keys) |
| `listInstances` (`lattice.ctrl.loom.list`) | `KVListKeys` (**whole bucket**) + `KVGetMulti` over **every** instance record | O(all keys) **and** O(all bodies) |
| `getInstance` / `resolveToken` / `getPinnedPattern` | direct GET | O(1) — unaffected, forever |
| `cmd/loupe`'s `weaverArtifactLive` (the only reader outside `internal/loom`) | direct GET, **presence only** | O(1) — but **semantically wrong**; §2.1 |
| storage | ~200 B of JSON (`Instance` is 7 scalar fields) + message overhead ≈ **~300 B/instance**; `loom-state` is provisioned with **no `MaxBytes` and no `MaxAge`** (`internal/bootstrap/primordial.go`'s `ProvisionBuckets`, over `PlatformBuckets()`) | unbounded, and **never silently discarded** |

Three things fall out, and each redirects the design.

**(a) The live defect is `ListInstances`, and it is already broken.** At the measured ~9,260 records it is past
`KVGetMulti`'s ~1,024 matched-subject fast-path gate (`internal/substrate/kv_multi.go`), which drops to an
ephemeral-consumer drain with a documented worst case in the hundreds of seconds — against
`internal/loom/control/service.go`'s **5 s** handler timeout. The consumer is not hypothetical:
`cmd/loupe/flows.go` requests `lattice.ctrl.loom.list` on **every render of the Flows tab**.

**(b) The residue is not write-only — one consumer probes it, and misreads it.** §2.1.

**(c) Storage is not the problem, and shrinking the record is not a fix.** The record is already ~200 bytes of
JSON; a "dedup tombstone" saves perhaps 150 of them and changes no consumer's complexity class. The bucket has
no byte cap, so there is no discard cliff quietly eating dedup evidence — the ledger is safe where it sits.
**The row's prescribed primitive optimizes the one dimension that does not hurt.**

### 2.1 The presence-probe defect (found here, owned elsewhere)

`cmd/loupe/weaver.go`'s `weaverArtifactLive` answers *"is this gap's artifact still open?"* for the Weaver
detail view. Its `"flow"` arm is a bare `KVGet` on the instance cursor returning `err == nil` — **presence,
not liveness**. Because the cursor persists after terminal *by design*, every Weaver-triggered flow that ever
completed or failed reports `Live: true` for the life of the deployment. The sibling `"task"` arm has the same
shape and is likely wrong for the same reason (a task vertex is *soft*-tombstoned in Core KV, so it is also
still present) — that arm should be read, not assumed.

Three consequences, and only the third is work:

1. It **falsifies the tempting sentence** *"the terminal residue is a write-only ledger nothing reads."*
   Something reads it, by direct GET, and gets the wrong answer. The correct statement — the one §7's contract
   clause uses — is that the residue is never **enumerated**; a presence probe against it must be
   status-aware.
2. It is an **independent argument against pruning**, not for it: a status-aware probe on a pruned cursor
   still cannot distinguish "aged out" from "never ran" — alternative 4's circularity, in a second consumer.
3. It is `cmd/loupe/**` — **Stream 3, its own lane and its own build lock.** A Lattice fire must not build it.
   Filed as a capped row on `backlog/loupe.md` (★★ / XS) pointing here. The fix is to decode the record and
   test `Status == running`, and to re-derive the `"task"` arm against the soft-tombstone envelope rather than
   copying the flow arm's shape.

## 3. The shape

**Keep the cursor exactly as it is. Make every enumeration server-side and bounded to the actionable set.**

Nothing about the terminal batch, the cursor's lifetime, or the dedup guard changes. Three code paths change,
and one new index key appears.

### Inc 1 — point the two pin listings at a real filter *(the primitive already exists)*

`substrate.KVListKeysFilter(ctx, bucket, filter, cursor, limit)` is already shipped
(`internal/substrate/kv.go`): an **arbitrary** NATS subject filter pushed to the server (nats.go's
`ListKeysFiltered` → `WatchFiltered` prefixes each filter with `$KV.<bucket>.` and passes it as the consumer's
filter subject — its own comment sanctions wildcard patterns: *"Could be a pattern so don't check for
validity"*), with lexicographic paging and load-bearing de-dup. It is the general form of `KVListKeysPrefix`,
which can only express a trailing `prefix>` — which is precisely why the heartbeat's `instance.` prefix
matches cursors as well as pins.

- `runningInstanceCounter.count` → filter **`instance.*.pattern`**. The suffix filter that runs client-side
  today moves to the server, and the per-tick transfer drops from O(all instances) to O(running).
- `pinnedDomains` → the same filter, replacing its whole-bucket `KVListKeys`.

Both are pure substitutions: the pin key set is unchanged, and `pinnedDomains`' documented error posture
(unparseable pin ⇒ skip; transient read error ⇒ hard error) is unchanged. Mid-token `*` is the same
server-side-bounded idiom `KVListKeysFilter`'s own doc comment already gives for target-bounded link
enumeration.

`runningInstanceReader`'s narrow one-method interface (deliberately typed so a body fetch on that path is a
compile error) gains the one method and keeps that property.

### Inc 2 — a `failed` index, and a bounded `ListInstances`

There are exactly **two** terminal write sites (`internal/loom/engine.go`'s complete and failed arms) and both
funnel through `stateStore.transition`, whose single `inst.Status != StatusRunning` branch already deletes the
pattern pin. Add, in that same branch and the same `AtomicBatch`:

- `Status == StatusFailed` → **PUT** `instance.<id>.failed` (an empty-object body; the key's presence is the
  whole signal).
- `Status == StatusComplete` → nothing. A complete instance is not actionable and not enumerable.

`stateStore.redrive` — the one sanctioned `failed → running` transition — **deletes** the marker in its
existing CAS-guarded batch, beside the re-pin it already writes.

**PUT, never `CreateOnly`.** A redrive deletes the marker, leaving a permanent delete marker on that subject;
a `CreateOnly` re-write after a second failure could never commit. This is dossier entry #1 in
`docs/components/loom.md` — the defect that broke `RedriveInstance` in production — applied at authoring time
rather than found at review.

`listInstances` then becomes: two filtered listings (`instance.*.pattern` ∪ `instance.*.failed`) → derive the
instanceIds → **one** `KVGetMulti` over that set. The result set is `running ∪ failed`. Both legs are bounded
by an operator-actionable quantity, so the 1,024 fast-path gate is unreachable in any healthy deployment — and
if it *is* reached, that means ≥1,024 un-redriven failures, which is itself the alarm.

**The semantic change, stated plainly:** `complete` instances stop appearing in `lattice.ctrl.loom.list`.

### Inc 3 — say what the residue is, in the places that bind

- **Contract** (§7): the staged `10-orchestration-substrate.md` edit.
- **`docs/components/loom.md`**: the `loom-state` keyspace section gains the write-only-ledger posture, the
  ~300 B/instance figure, the ceiling, and a pointer to §9's deferred retraction primitive — so the next
  author finds the reasoning where the keyspace is documented rather than re-deriving it.
- **`docs/observability/health-kv-schema.md`** needs no change: `runningInstances`' documented derivation was
  already corrected to the pin index in the 2026-08-29 fire, and Inc 1 changes only how the pins are listed.

## 4. State-lifetime table — `instance.<id>.failed`

The one new stateful thing in this design. Every boundary the neighbouring pin already honours:

| Boundary | `instance.<id>.pattern` (existing) | `instance.<id>.failed` (new) |
|---|---|---|
| **Created** | `createInstance`, `CreateOnly`, atomic with the cursor | `transition`, **plain PUT**, atomic with the terminal flip, failed arm only |
| **Removed** | `transition`'s terminal branch (both arms) | `redrive`'s CAS batch |
| **Re-created after removal** | never (a redrive re-pins with a plain PUT, guarded by the cursor CAS) | yes — a redriven instance that fails again re-PUTs it. This is why it is not `CreateOnly` |
| **Crash between batch and anything else** | impossible — one `AtomicBatch` | impossible — same batch |
| **Concurrent redrive** | loser's whole batch rejected on the cursor CAS | same batch, same CAS, same rejection |
| **Trigger redelivery / replay** | untouched (the trigger path never reaches `transition` for an existing instance) | untouched |
| **TTL / expiry** | none | **none** — a failed instance is an operator work item and must not age out of the queue silently |
| **Upgrade from a tree without it** | n/a | **absent for every pre-existing failed instance.** §6 owns this |
| **Bucket wipe (`make down`)** | gone with everything | gone with everything |

**The failed marker is an index, not a fact.** The cursor's `Status` field stays authoritative; the marker
only makes the failed set enumerable. A marker that disagrees with its cursor (crash-impossible, but
reachable by hand-editing) is resolved in favour of the cursor: `listInstances` reads the record it fetches,
so a stale marker costs one extra `KVGetMulti` entry and nothing else, and a *missing* marker hides a failed
instance from the list — which §6's backfill exists to prevent and §10 prices as the design's main risk.

## 5. Executable censuses

Every count this design leans on, as the command that derives it. Phase-0 of the build re-runs these against
merged `main`; a disagreement is a scope change, not a rounding error.

| Claim | Command | Expected |
|---|---|---|
| Only three paths enumerate `loom-state` | `grep -rn "KVListKeys\|KVListKeysPrefix\|KVListKeysFilter" --include='*.go' internal/loom/ \| grep -v _test` | 3 sites: `health.go` (count), `state.go` `pinnedDomains`, `state.go` `listInstances` |
| Exactly two terminal write sites, both via `transition` | `grep -rn "StatusComplete\|StatusFailed" --include='*.go' internal/loom/ \| grep -v _test` | assignments only at `engine.go`'s complete/failed arms; `control.go`'s are comparisons |
| `redrive` is the only `failed → running` path | `grep -rn "func (s \*stateStore) redrive\|RedriveInstance" --include='*.go' internal/loom/ \| grep -v _test` | one state method, one engine entry point |
| `ListInstances` has exactly two consumers | `grep -rn "lattice.ctrl.loom.list" --include='*.go' cmd/ internal/ \| grep -v _test` | `cmd/loupe/{control,flows}.go` and `cmd/lattice/loom/loom.go` |
| No consumer of `ListInstances` needs `complete` | read `cmd/loupe/flows.go`'s `computeFlows` / `loomInstanceStatuses` | the control read is **enrichment only** — §9.1 |
| `loom-state` has no byte or age cap | read `PlatformBuckets()` + `ProvisionBuckets` in `internal/bootstrap/` | `KeyValueConfig` sets Bucket/Description/`LimitMarkerTTL` only |
| Producers of `StartLoomPattern` (who can supply a stable `instanceId`) | `grep -rn "StartLoomPattern" --include='*.go' internal/ cmd/ packages/ \| grep -v _test` | 3 production producers — Weaver's `triggerLoom` (always, claimId-seeded); `cmd/lattice/loom start --instance-id` (operator opt-in, else a fresh requestId); Loupe's vault-erase trigger (never). **Only Weaver re-invokes automatically**, so only its path can re-trigger an existing cursor |
| Readers of `instance.<id>` outside `internal/loom` | `grep -rn "LoomStateBucket" --include='*.go' cmd/ internal/ \| grep -v _test` | exactly one — `cmd/loupe/weaver.go`'s `weaverArtifactLive` (§2.1). No Chronicler reader, no package `kv.Read` |
| The live record count (the sizing input) | `nats kv ls loom-state \| wc -l` against the live stack, or the engine's own count | ~9,260 at 2026-08-29; **re-measure — this is the number the whole item is sized on** |

The last row is the one to actually run: this design's *urgency* rests on a figure measured by a prior fire.
It is cheap to re-derive and it decides whether Inc 2 is the fire's headline or its footnote.

## 6. Migration

**Inc 1 needs none** — a listing filter change, no state written.

**Inc 2's marker is absent for every instance that failed before it ships.** Those instances disappear from
`ListInstances` the moment the list stops enumerating cursors — a silent capability loss on exactly the
surface an operator uses to find what to redrive. So Inc 2 ships **with a bounded one-shot backfill**, run
once off the engine's startup path (not on it), gated to run only while it has work:

- page `instance.*` (excluding sub-keys) via `KVListKeysFilter` with an explicit `limit`, fetch each page's
  bodies, and PUT `instance.<id>.failed` for every record whose `Status == StatusFailed`;
- idempotent and convergent — a partial pass is fine, the next process start continues it;
- one summary log line (scanned / marked / remaining); no health issue, no new control verb.

This is deliberately the **capability-granting** shape, not a verdict-bearing one: it only makes failed
instances *visible*, it deletes nothing, and it has no decision to get wrong. Its enumeration is the exact
whole-bucket scan this design removes from the steady state — paid once, at a start the operator is already
performing to deploy the fix, and never again.

## 7. Contract surface

**Building to, unchanged:** Contract #4 §4.3 (the 24 h tracker horizon — §1.2 is a consequence of it, not a
request to change it); `10-orchestration-loom.md` §10.9 (the dedup sentence stays literally true — the cursor
is still present whenever a re-trigger can arrive); `10-orchestration-substrate.md`'s `triggerLoom` clause and
`instance.<instanceId>` *Named constructs* entry (unchanged; this design is what makes them permanently safe).

**One edit, staged UNCOMMITTED in `main`** — `docs/contracts/10-orchestration-substrate.md`, the `loom-state`
section. It adds an observable promise that is currently unstated, in two clauses:

1. **What the control plane answers for** — instances that are running or awaiting operator action (failed),
   never the completed history. Completed-flow history is a read-model concern.
2. **What a terminal-complete cursor is** — durable, permanent, deliberately non-enumerable dedup evidence.

Clause 1 is a **narrowing of an unwritten behaviour**, not the removal of a stated one: §10.9 already scopes
control-plane queryability to *"which flows are running"*, and the current implementation over-delivers. It is
still yours to sign because it is operator-visible.

Clause 2 is what makes §9's deferred work legible: it says the growth is a *decision*, so a future retention
design has to argue against a promise rather than assume an oversight. Written at promise altitude — no key
layouts, no file names, no mechanism; the layouts stay in `docs/components/loom.md`, as the sibling clause
already directs.

**Affected consumers of the narrowing:** `cmd/loupe` (unaffected — §9.1) and `cmd/lattice/loom`'s `list`
subcommand (its output shrinks; its help text says so).

## 8. Alternatives

**Row 1, as the discipline requires: do not have the thing.**

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Delete `ListInstances`.** The expensive consumer is the whole live defect; if nobody needs it, the fix is −1 RPC and no new index. | **Rejected — a real consumer, and it is the operator's only discovery path for redrive.** `RedriveInstance` takes an `instanceId` the operator must first *find*, and `orchestration-history` (the read model) projects `events.loom.>` — which records a `patternFailed`, but the engine's *current* `failed` status after a redrive attempt is Loom's alone. Deleting the list makes redrive undiscoverable. Kept, and bounded instead. |
| **2** | **Build the row's prescribed dedup tombstone** — delete the cursor at terminal, write a minimal id-scoped record in its place. | **Rejected — same key cardinality, so it fixes bytes and not one consumer's complexity class** (§2). It also *worsens* the cursor's own hazards: an explicit cursor DELETE leaves a permanent marker (dossier #1) which `createInstance`'s `CreateOnly` cursor write then refuses forever, converting a harmless duplicate-trigger Ack into an unbounded Nak loop. |
| **3** | **Collapse the re-dispatch upstream** — seed `triggerLoom`'s `requestId` on the `claimId` so the reclaim dies at the Contract #4 tracker, restoring §10.9's `instanceId == requestId` model and making `R > MaxAge` sufficient. | **Rejected — the tracker's horizon is 24 h by contract** (§1.2), and a userTask gap open longer than a day is the normal case. Recorded because it is the shape a reader will reach for, it has shipped precedent in the same file, and it is wrong for a reason that only Contract #4 §4.3 states. |
| **4** | **A `collapseOnExisting` flag on `StartLoomPattern`** — a reclaim declares itself, so cursor *absence* means "pruned" rather than "never ran" and terminal cursors become freely prunable. | **Rejected — it deletes a live self-heal.** Today a first dispatch whose op never commits is healed by the next reclaim: no cursor ⇒ Loom creates the instance. Under the flag the gap wedges silently and forever. Making the flag sound requires distinguishing "pruned" from "never ran" — which *is* the evidence being deleted. Circular. |
| **5** | **Weaver stops re-dispatching a collapse-intent action once its dispatch is known-landed** (a `dispatched` stamp on the mark, the `RecordProposalDispatch` shape), bounding the horizon at the source so §9's pruning becomes sound. | **The right long-term shape, deferred — §9.** Publish-success is not commit-success, so a sound stamp needs a commit-confirmation channel Weaver does not have; and its payoff today is bytes. Not rejected — sequenced. |
| **6** | **Serve `ListInstances` from the pin index alone** (the 2026-08-27 triage's resolved shape). | **Rejected, as the prior design also found:** pins are running-only, so failed instances vanish from the redrive discovery path. Inc 2 is that shape *plus* the missing half. |
| **7** | **Shrink the terminal cursor's body** (drop `PatternRef`/`SubjectKey`/`PendingToken` at terminal). | **Rejected — measurement.** The record is 7 scalar fields, ~200 B; the saving is ~150 B/instance and no consumer gets cheaper. It also breaks `ListInstances`' ability to show *what* failed, on the one surface that needs it. |
| **8** | **A `MaxAge` or `MaxBytes` on the `loom-state` bucket.** | **Rejected, emphatically — it is the unsound prune with no author.** JetStream discard would silently delete dedup evidence with no code path aware of it, and the outcome is §1's correctness catastrophe (a historical pattern re-run from step 0 with its committed side effects re-executed). The current no-cap provisioning is correct and this design's §7 clause 2 is partly there to stop someone "fixing" it. |

**Priced in combination** (the standing rule that a rejection may not lean on another rejection's absence):
3+5 together — an upstream collapse *plus* a bounded horizon — would make pruning sound without any Loom-side
dedup substitute. That combination is exactly §9, and its blocker is the commit-confirmation channel, not
either half's individual objection. 2+8 (tombstone plus a bucket cap) is the shape a future author is most
likely to reach for and is the most dangerous: a cap over *any* dedup record, tombstone or cursor, is
unsound for the same reason.

## 9. The ceiling, and the primitive that would raise it — named, not built

**The ceiling.** ~300 B and one KV subject per Loom instance, forever. At 10⁶ instances that is ~300 MB of
file storage and 10⁶ subjects in the KV stream's in-memory subject index — an operational cost, not a
failure. At 10⁸ it is fatal. So the honest statement is that the ledger is free at today's scale, cheap at
10⁶, and needs the work below before 10⁷. That is **inside a plausible production regime**, not only the
multi-cell/hyperscale one, which is why this is §"For Andrew" and not a footnote.

**Why nothing bounds it today.** A terminal cursor is prunable exactly when **no live Weaver mark can still
derive its `instanceId`** — and that set is small and knowable: an episode's `instanceId` is a pure function
of `(targetId, entityId, gapColumn, claimId)`, and when the gap closes the mark is deleted and that `claimId`
can never be re-derived (a reopen mints a new one). So the reachable set is bounded by **open userTask gaps**.
**Weaver knows it exactly; Loom cannot compute it.** Every sound pruning design therefore needs the same
missing thing, which is why alternatives 3, 4 and 5 are three faces of one gap:

> **A dispatch-landed confirmation on the Weaver side** — the evidence that lets a collapse-intent reclaim
> stop re-dispatching, so the re-trigger horizon becomes bounded and a retention window becomes sufficient.

Note where that lands: it is a **Weaver** primitive, not the Loom-side "durable id-scoped re-trigger dedup
tombstone" the row prescribes. The row named the primitive its assumed solution shape would need; the sound
one is on the other side of the seam.

**Why not now (the dead-scaffolding test).** *Does building it realize value before its consumer exists?* No.
Its only payoff is bytes, at a scale no deployment is near; it needs a cross-component channel Weaver does not
have (publish-success ≠ commit-success); and every intermediate form is either unsound (alternative 4) or
inert. **Ratify the direction, sequence the build.**

**Revive trigger (write it on the row):** a deployment whose `loom-state` subject count is an *observed*
operational cost — a measured NATS memory or startup-time regression attributable to the bucket — **or** a
`ListInstances`/redrive path that needs completed instances back. Not a date, and not "when we get to 10⁶".

## 10. Risks

| Risk | Disposition |
|---|---|
| **The `failed` backfill misses instances, so a failed flow is invisible to redrive** | The design's single most consequential failure mode, and the reason §6 is an owned increment rather than a note. Pinned by a test that seeds failed instances through a **real** `createInstance` + `transition` (never `putInstance` — dossier #2), runs the backfill, and asserts the list. |
| Dropping `complete` from `ListInstances` breaks a consumer | Census (§5) says two consumers; Loupe reads the control plane as enrichment only (§9.1) and the CLI's output shrinks. Pinned by a test asserting a completed instance is absent and a failed one present. |
| A mid-token `*` filter behaves differently than the prefix form on the pinned server | nats.go v1.52.0 → `WatchFiltered` prefixes with `$KV.<bucket>.` and passes the pattern as the consumer filter, explicitly permitting wildcards; nats-server v2.14.0. Proven by test against the embedded fixture, not by reading — the same posture the prior fire took on TTL semantics. |
| `KVListKeysFilter` collects all matches in memory before paging | True, and correct here: the server-side filter is what bounds the set (running/failed), so the collected set is already small. The `limit` is used in §6's backfill, where the matched set is deliberately the large one. |
| A poisoned/unparseable record blinds the list | Unchanged posture — `listInstances` already skips-and-logs a per-key unmarshal failure and hard-fails a genuine read error. Inc 2 must preserve both. |
| The 9,260 figure is stale | §5's last census row; re-measured in Phase 0. |

## 11. Decomposition for the Steward

Three increments, each independently shippable and green. **Posture-changing: Inc 2** (a new durable index key
on orchestration state, and an operator-visible narrowing) → full three-layer adversarial pass, cold
reviewers. **Inc 1 and Inc 3 are mechanical** → lead review. One cumulative adversarial pass over the whole
diff at close. (Depth is the Steward's sizing per `agents/steward/SKILL.md` §4; this is the recommendation,
not a floor.)

**Inc 1 — filtered pin listings.** `health.go`'s counter and `state.go`'s `pinnedDomains` onto
`KVListKeysFilter` with `instance.*.pattern`.
*Owns:* a fixture test proving the filter returns pins and **not** cursors when both exist (the mutation test
is that the current `instance.>` prefix fails it); a `pinnedDomains` test that its error posture is unchanged.

**Inc 2 — the `failed` index + bounded `ListInstances` + the backfill.**
*Owns:* the marker written on the failed arm and not the complete arm, seeded through a **real terminal
transition**; the redrive delete; a **re-fail after redrive** test (the PUT-not-`CreateOnly` proof — this is
the one that would have caught dossier #1); `ListInstances` returns running ∪ failed and excludes complete;
the backfill's idempotence and partial-pass convergence; a consumer test that `cmd/lattice/loom list` renders
the narrowed set.

**Inc 3 — docs + the contract edit.** `docs/components/loom.md`'s keyspace section; the `10-orchestration-substrate.md`
clause (**only if Andrew ratifies it — otherwise this increment ships the component doc alone**); a new
dossier entry for the enumeration lesson (*"a `prefix>` filter that matches a key's sub-keys is not the
index you think it is — the heartbeat's `instance.` prefix matched every cursor for as long as the pin-index
fix has been shipped"*).
*Owns:* no test; `lint-board` + `lint-conventions` are its gates.

## 12. Gates

`go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go` ·
every other `scripts/lint-*.go` · `go run ./scripts/lint-board.go` · `make verify-kernel` ·
`go test ./internal/loom/... ./internal/substrate/... ./internal/bootstrap/...` · full `go test ./...` with
`POSTGRES_TEST_DSN` set.

Build-tagged harnesses: `runningInstanceReader` gains a method, so every fake implementing it must be
enumerated — `grep -rl "^//go:build " --include=*_test.go internal/` and run the tagged targets the loom
interfaces reach. No `packages/` content changes, so no manifest version bump.

---

### Appendix — grounding ledger

Every claim above is pinned to code that *does* the thing, never to a comment that describes it.

| Fact | Where |
|---|---|
| Cursor has no delete site; terminal deletes only pin/token/deadline | `internal/loom/state.go` — `transition`'s terminal branch |
| Two terminal write sites, one choke point | `internal/loom/engine.go` complete/failed arms → `transition` |
| Trigger dedup: present ⇒ Ack (or resume step 0 if it crashed before submission) | `internal/loom/engine.go`, the `getInstance` branch |
| `createInstance` writes cursor + pin, both `CreateOnly`, one batch | `internal/loom/state.go` |
| `redrive` guards on a **cursor CAS**, re-pins with a plain PUT | `internal/loom/state.go` — and its own comment on why `CreateOnly` cannot work |
| Counter already uses a **server-side** prefix list, but the prefix is `instance.` | `internal/loom/health.go` — `count` |
| `pinnedDomains` and `listInstances` use whole-bucket `KVListKeys` | `internal/loom/state.go` |
| `KVGetMulti`'s ~1,024 matched-subject fast-path gate | `internal/substrate/kv_multi.go` |
| `KVListKeysFilter` exists, arbitrary filter, paged, de-duped | `internal/substrate/kv.go` |
| `WatchFiltered` prefixes `$KV.<bucket>.` and permits patterns | nats.go v1.52.0 `jetstream/kv.go` (pin: `go.mod`; server v2.14.0) |
| `loom-state` provisioned with no `MaxBytes`/`MaxAge` | `internal/bootstrap/platform_buckets.go` + `primordial.go`'s `ProvisionBuckets` |
| Tracker TTL is 24 h, and the "layer your own dedup" sentence | `docs/contracts/04-idempotency-tracker.md` §4.3 |
| Episode requestId is mark-revision-seeded; task/instance ids are claimId-seeded | `internal/weaver/actuator.go` — the four `derive*` helpers |
| External reclaim mints a **fresh** claimId | `internal/weaver/evaluator.go`, the stale-mark branch |
| `externalDispatchGap` classifies `triggerLoom` by pattern step kinds | `internal/weaver/evaluator.go` |
| Loupe reads `orchestration-history` as authoritative, `loom.list` as enrichment | `cmd/loupe/flows.go` — `handleFlows` |
| Control handler timeout 5 s | `internal/loom/control/service.go` |
| `weaverArtifactLive`'s flow arm returns `err == nil` on a bare presence GET | `cmd/loupe/weaver.go` |
| A collapse-only gap's exhausted budget has no re-arm path | `internal/weaver/control.go` — `reArmDeclines`, pinned by `TestResetRetryBudget_RefusesACollapseOnlyGap` |
| The engine's retry-budget fallback is `directOp`-only | `internal/weaver/evaluator.go` — `defaultDirectOpRetryBudget`'s doc |
| lease-signing deliberately removed its userTask `maxretries` caps | `packages/lease-signing/lenses.go`, `targets.go` |
| Reclaim backoff caps at 24 h | `internal/weaver/reconciler.go` — `defaultReclaimBackoffCap` |
| `instanceId` is optional on the op; absent ⇒ the op's own `requestId` | `packages/orchestration-base/loom_lifecycle.go` |
