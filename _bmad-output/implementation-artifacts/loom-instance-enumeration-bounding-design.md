# `loom-state` accretion — bound the ENUMERATION, keep the ledger

**Status: ✅ RATIFIED (split) by Andrew 2026-09-02 — "agreed - split it, cursor alone is fine once tombstones are swept".** The enumeration fire (Inc 1–3 as one Lattice fire, §11) is ready, sequenced AFTER the Loupe-lane row *Flows-tab liveness must not read absence from the engine list as orphaned* (§7.1). The retention fork is resolved: the cursor is permanent (accepted), and the delete tombstones — five sixths of the bucket — are swept (§8 alternative 9, filed as its own designer row). Contract #10 substrate: clause 2 committed at ratification with its closing sentence narrowed to the retained-record set; clause 1 lands with the fire, text of record in §7. Winston's adjudications: `runningInstanceReader` swaps to the filter; the `failed` index as designed; Phase 0 re-runs §5's censuses. Winston (Lattice Designer, unattended fire, 2026-09-01).

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
Loupe's Flows tab and `lattice loom list`) is already over its handler timeout on the live stack — and, at the
measured growth rate, stops answering **at all** in roughly ten days (§2a). So: point every enumeration at a
server-side subject filter that already exists in the substrate, add the one index that is missing (`failed`),
and let the terminal-complete residue be what it already is — durable dedup evidence that is **never
enumerated**.

**The fork I need you to rule on (§8).** The recommendation is *"do not prune; accept unbounded durable growth
in an engine's private KV, and name the ceiling."* That reverses the filed row's own prescription and it is a
platform-posture call, not a mechanism choice: measured on the live stack, a Loom instance leaves a permanent
trace of **~1,100 bytes across ~6 KV subjects** — the cursor is one of the six, and the other five are the
**delete tombstones** of the sub-keys Loom cleans up (§2). So a deployment running 10⁶ flows carries **~1.1 GB
and ~6×10⁶ subjects** in `loom-state` forever. §8 prices three alternatives (build the retraction primitive
now; prune unsoundly; bound the *tombstone* population) and §9 names the exact primitive and the exact trigger
that should revive it. I recommend **not building it now** — it needs a cross-component Weaver→Loom channel
and its only payoff today is bytes — but the ceiling is inside a plausible production regime, not only
hyperscale, so it is your call and not mine. One number belongs in that ruling: the deferred primitive
licenses pruning **the cursor only**, which is 12,339 of the bucket's 74,032 subjects — **17%**. The other
five sixths are reachable today, by alternative 9, without touching the dedup guard at all.

**A cross-lane sequencing constraint this fire created (§7, §11).** Inc 2's narrowing is not invisible to
Loupe. A history row that still reads `running` while the engine has moved on is badged `stale-history`
("the projection is behind"); once `complete` instances leave the list, that same row badges **`orphaned`**
("Loom has no record of it at all") for the whole lens catch-up window. So Inc 2 cannot land alone: a
second `cmd/loupe/**` row must land with or before it. Named in §7 and sequenced in §11.

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

The board row, **as it read when this fire opened**:

> **[Loom] Terminal instance cursors cannot be pruned — the dedup guard has no substitute.** `loom-state`
> accretes one `instance.<id>` forever. Cursor presence is the frozen-contract collapse point for Weaver's
> `triggerLoom` re-dispatch, whose horizon is unbounded, so no retention window suffices.
> `📐 needs designer pass · no-pattern: durable id-scoped re-trigger dedup tombstone`

**Disclosure: this fire then rewrote that row.** The live row in `backlog/lattice.md` now reads *"…and three
paths enumerate the whole bucket / Pruning stays out … the live harm is enumeration: `ListInstances` is past
`KVGetMulti`'s fast-path gate against a 5 s handler timeout"* — which is this design's conclusion, including
the prescription reversal §8 asks Andrew to rule on, written back over the demand that prompted it. The
pre-fire text above is the one to argue against; a ratification that reads only the board is reading this
design's summary of itself.

Every factual clause of the original row survives grounding. What does not survive is the implicit premise carried by
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

| Consumer | Cost shape today | Measured on the live stack, 2026-09-01 |
|---|---|---|
| `runningInstanceCounter.count` (heartbeat, every 10 s) | `KVListKeysPrefix(bucket, "instance.")` → server filter `instance.>` → **matches cursors AND pins**, and every deleted pin's **tombstone** with them | **24,678 subjects delivered per tick** — to count **1** running instance |
| `pinnedDomains` (via `reconcileConsumers`, `internal/loom/engine.go` — reached from the **complete** arm and the **fail** arm, so **once per terminal**) | `KVListKeys` (**whole bucket**) + client-side pin filter, then `KVGetMulti` over pins only | **74,032 subjects delivered**, ~1,000× per day on this stack |
| `listInstances` (`lattice.ctrl.loom.list`) | `KVListKeys` (**whole bucket**) + `KVGetMulti` over **every** instance record | 74,032 subjects, then 12,339 bodies — **already failing**; (a) |
| `getInstance` / `resolveToken` / `getPinnedPattern` | direct GET | O(1) — unaffected, forever |
| `cmd/loupe`'s `weaverArtifactLive` (the only reader outside `internal/loom`) | direct GET, **presence only** | O(1) — but **semantically wrong**; §2.1 |
| storage | `loom-state` is provisioned `max_msgs_per_subject=1`, `max_age=0`, `max_bytes=-1` (`internal/bootstrap/platform_buckets.go` → `ProvisionBuckets`) | 74,032 subjects · 74,032 msgs · **13,536,548 bytes** → **~1,097 B and ~6 subjects per instance**; unbounded, and **never silently discarded** |

**The unit of accretion is not the cursor — it is about six subjects.** Loom writes and then deletes four
ephemeral sub-keys per instance (the pattern pin, the token pointer, the deadline mark, and an outbox record
per step). In a `max_msgs_per_subject=1`, `max_age=0` stream a delete is not a removal: it leaves a permanent
DEL tombstone occupying its own subject in the stream's subject index, and **nothing in this tree ever sweeps
them** — `grep -rn PurgeDeletes --include='*.go'` returns zero production hits. The live breakdown:
`outbox.*` 24,677 · `instance.<id>` 12,339 · `instance.<id>.pattern` 12,339 (of which exactly **one** is a live
pin — one running instance) · `token.*` 12,339 · `deadline.*` 12,338. Every one of those subjects is
delivered to any whole-bucket listing, forever.

Three things fall out, and each redirects the design.

**(a) The live defect is `ListInstances`, it is already broken, and it hard-fails on a date.** The
matched-subject fast-path gate is **exactly 1,024**, enforced server-side (`nats-server` v2.14.2
`server/stream.go`'s `maxAllowedResponses`, passed to `store.MultiLastSeqs`, `ErrTooManyResults` → `413`;
`docs/vendors.md` records it). `listInstances` hands `KVGetMulti` **12,339** keys — 12× past the gate — so
every call routes to `kvGetMultiFallback` → `drainDirectGetFallback`, whose documented worst case is in the
hundreds of seconds against `internal/loom/control/service.go`'s **5 s** `handlerTimeout`. The consumer is not
hypothetical: `cmd/loupe/flows.go` requests `lattice.ctrl.loom.list` on **every render of the Flows tab**.

The timeout is not the end of it. `drainDirectGetFallback` passes **all** subjects in a single
`CreateConsumer` request with no chunking (`internal/substrate/kv_multi.go`), and the live `max_payload` is
1,048,576 bytes. At ≈47 B per subject entry, today's 12,339 subjects is ≈580 KB. Past roughly **22,000
instances the create-consumer request exceeds `max_payload` and `ListInstances` fails outright** — not slowly,
not partially. At the measured ≈1,000 instances/day that is **about ten days from 2026-09-01**. This is the
item's real clock, and it is why Inc 2 is the fire's headline.

**(b) The residue is not write-only — one consumer probes it, and misreads it.** §2.1.

**(c) Storage is real but still not the lever, and shrinking the record is not a fix.** The cursor body is
**195 bytes** measured (`Instance` is 7 scalar fields, exactly as claimed) — but the instance costs ~1,097 B,
so **the cursor is under a fifth of its own footprint** and the tombstones are the rest. A "dedup tombstone"
that replaces the cursor therefore saves a fraction of a fifth, has the **same key cardinality**, and changes
no consumer's complexity class. The bucket has no byte cap, so there is no discard cliff quietly eating dedup
evidence — the ledger is safe where it sits. **The row's prescribed primitive optimizes the one dimension that
does not hurt, and it is aimed at the smaller half of even that dimension.**

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
matches cursors as well as pins, and every deleted pin's tombstone along with them.

- `runningInstanceCounter.count` → filter **`instance.*.pattern`**. The suffix filter that runs client-side
  today moves to the server: **24,678 subjects per tick → 12,339**.
- `pinnedDomains` → the same filter, replacing its whole-bucket `KVListKeys`: **74,032 → 12,339**, once per
  terminal transition.

**State the benefit as measured, not as a complexity class.** The tempting claim is that the per-tick transfer
drops "from O(all instances) to O(running)". It is wrong, and the mechanism says why: in
`nats.go` v1.52.0, `ignoreDeletes` is applied **client-side**, in the watcher callback — the consumer is
`DeliverLastPerSubject` + `HeadersOnly`, and `ListKeysFiltered` is exactly
`WatchFiltered(..., IgnoreDeletes(), MetaOnly())`. So the server delivers one header-only message **per
matching subject, tombstones included**, and the client throws the delete markers away after they cross the
wire. `instance.*.pattern` matches 12,339 subjects of which **one** is a live pin. The honest numbers are
therefore **2× on the heartbeat and 6× on `pinnedDomains`**, with the residue still growing by one subject per
instance ever created. That is a large, cheap, immediate win on the two paths that run thousands of times a
day — and it is a **constant factor, not a bound**. The bound belongs to Inc 2 and to §9's ceiling.

Both are pure substitutions: the pin key set is unchanged, and `pinnedDomains`' documented error posture
(unparseable pin ⇒ skip; transient read error ⇒ hard error) is unchanged. The mid-token `*` is settled, not
novel: `KVListKeysFilter`'s own doc comment gives it as the target-bounded link-enumeration idiom, it has
production callers today (`internal/processor/starlark_kv.go`, `internal/pkgmgr/opmetaretirement.go`,
`cmd/lattice/candidates/candidates.go`) and a passing mid-`*` test in `internal/substrate`. The `instanceId`
token it spans is guarded to a NanoID at `internal/loom/engine.go`'s entry, so it can never contain a `.` that
would split it into two tokens.

`runningInstanceReader`'s narrow one-method interface (deliberately typed so a body fetch on that path is a
compile error) **swaps** `KVListKeysPrefix` for `KVListKeysFilter` — it does not gain a second method. Keeping
both would leave the whole-keyspace call reachable from exactly the path this increment is removing it from;
the one-method property is the guard, and it only guards what it is the only member of.

### Inc 2 — a `failed` index, and a bounded `ListInstances`

**Why an index and not a smarter query.** The key shape already indexes exactly one status: `running`, as the
presence of `instance.<id>.pattern` (`isPatternPinKey`). It cannot separate `complete` from `failed` — both
live only inside the cursor *body*, which is precisely the thing a listing must not fetch. So the enumerable
set today is `running` or `everything`, and the operator-actionable set is `running ∪ failed`. The missing
half has to become a key.

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
instanceIds → **one** `KVGetMulti` over that set. The result set is `running ∪ failed`.

**The `KVGetMulti` bound survives the tombstone finding, and it is the one that matters.** The lister hands
back **live keys** — the delete markers are dropped client-side before any key reaches the caller — so the
get-multi runs over `|running| + |failed|`, an operator-actionable quantity, not over the subjects that were
delivered to get there. The 1,024 fast-path gate is therefore unreachable in any healthy deployment, and the
`max_payload` wall of §2a moves out of reach with it. If the gate *is* reached, that means ≥1,024 un-redriven
failures, which is itself the alarm. **Inc 2 fixes the failure this item is really about.**

**What it does not fix, stated with the same candour as Inc 1.** The two listing *legs* pay the same tombstone
tax: `instance.*.pattern` delivers one subject per instance ever created, and `instance.*.failed` one per
instance that has ever failed — a redrive's marker DELETE leaves its own permanent subject exactly as the pin's
does. Both are still cheap (header-only, and the failed population is small by construction), and both are
still bounded by §9's ceiling rather than by this increment. Alternative 9 in §8 is the lever that would bound
them; it is deliberately not in this fire.

**The semantic change, stated plainly:** `complete` instances stop appearing in `lattice.ctrl.loom.list`. That
is visible in two places, and both are this fire's responsibility to land: `cmd/lattice/loom list`'s help text
(*"List Loom instances (running + retained terminals)"*) changes in the same commit, and Loupe's Flows badge
must stop reading list-absence as `orphaned` — §7.

### Inc 3 — say what the residue is, in the places that bind

- **Contract** (§7): the staged `10-orchestration-substrate.md` edit.
- **`docs/components/loom.md`**: the `loom-state` keyspace section gains the never-enumerated (not
  "write-only" — §2.1) ledger posture, the measured **~1,097 B and ~6 subjects per instance** with the
  tombstone mechanism that produces the six, the ceiling, and a pointer to §9's deferred retraction primitive
  — so the next author finds the reasoning where the keyspace is documented rather than re-deriving it.
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

**The marker adds a seventh subject to any instance that ever fails, permanently.** Its removal by `redrive`
is a delete like every other in this bucket: the subject stays, holding a tombstone, and no sweep exists
(§2). That is a real cost of this increment and it is accepted knowingly — the failed population is small by
construction (an un-redriven failure is an operator work item, not a steady-state class), and the alternative
is leaving the failed set unenumerable, which is the capability loss §6 exists to prevent. It is also the
increment that makes alternative 9's absence *visible* rather than merely inherited.

## 5. Executable censuses

Every count this design leans on, as the command that derives it. Phase-0 of the build re-runs these against
merged `main`; a disagreement is a scope change, not a rounding error.

| Claim | Command | Expected — and the result at 2026-09-01 22:44 where it has been run |
|---|---|---|
| Only three paths enumerate `loom-state` | `grep -rn "KVListKeys\|KVListKeysPrefix\|KVListKeysFilter" --include='*.go' internal/loom/ \| grep -v _test` | 3 sites: `health.go` (count), `state.go` `pinnedDomains`, `state.go` `listInstances`. **Confirmed** |
| Exactly two terminal write sites, both via `transition` | `grep -rn "StatusComplete\|StatusFailed" --include='*.go' internal/loom/ \| grep -v _test` | assignments only at `engine.go`'s complete/failed arms; `control.go`'s are comparisons. **Confirmed** |
| `redrive` is the only `failed → running` path | `grep -rn "func (s \*stateStore) redrive\|RedriveInstance" --include='*.go' internal/loom/ \| grep -v _test` | one state method, one engine entry point. **Confirmed** |
| `ListInstances` has exactly two consumers | `grep -rn "lattice.ctrl.loom.list" --include='*.go' cmd/ internal/ \| grep -v _test` | `cmd/loupe/{control,flows}.go` and `cmd/lattice/loom/loom.go`. **Confirmed** |
| Does any consumer of `ListInstances` read `complete`? | read `cmd/loupe/flows.go`'s `flowLiveness` / `loomInstanceStatuses`, then `cmd/lattice/loom/loom.go`'s `list` | **REFUTED.** `flowLiveness` tests *absence* before status — `!engineHas ⇒ orphaned`, terminal ⇒ `stale-history` — so dropping `complete` converts one verdict into the other. §7.1 owns the remedy |
| How often does a *whole-bucket* enumeration run? | read `reconcileConsumers`' callers in `internal/loom/engine.go` | reached from the complete arm **and** the fail arm ⇒ **once per terminal instance**, ~1,000×/day here, each a 74,032-subject delivery |
| `loom-state`'s stream limits | `nats stream info KV_loom-state`, and read `PlatformBuckets()` + `ProvisionBuckets` in `internal/bootstrap/` | `max_msgs_per_subject=1`, `max_age=0`, `max_bytes=-1`; `PerKeyTTL: true` in `platform_buckets.go`, so the live stream carries `allow_msg_ttl=true` and `subject_delete_marker_ttl=1s`. **Confirmed** |
| Nothing sweeps delete tombstones | `grep -rn PurgeDeletes --include='*.go' .` | **zero production hits** — the mechanism behind §2's ~6-subjects-per-instance figure |
| Producers of `StartLoomPattern` (who can supply a stable `instanceId`) | `grep -rn "StartLoomPattern" --include='*.go' internal/ cmd/ packages/ \| grep -v _test` | 3 production producers — Weaver's `triggerLoom` (always, claimId-seeded); `cmd/lattice/loom start --instance-id` (operator opt-in, else a fresh requestId); Loupe's vault-erase trigger (never). **Only Weaver re-invokes automatically**, so only its path can re-trigger an existing cursor |
| Readers of `instance.<id>` outside `internal/loom` | `grep -rn "LoomStateBucket" --include='*.go' cmd/ internal/ \| grep -v _test` | exactly one — `cmd/loupe/weaver.go`'s `weaverArtifactLive` (§2.1). No Chronicler reader, no package `kv.Read` |
| **The sizing input** — live keys, subjects, bytes | `nats kv ls loom-state \| wc -l`; `nats stream info KV_loom-state`; `nats stream subjects KV_loom-state` | **12,341 live keys · 74,032 subjects · 13,536,548 bytes**, of which cursors 12,339, pin subjects 12,339 (**1** live), `token.*` 12,339, `deadline.*` 12,338, `outbox.*` 24,677. Growth ≈ **1,000 instances/day** ⇒ ≈6,000 subjects/day |

**The sizing row was run, and it moved the item.** The prior fire's ~9,260 records is now 12,339 cursors — a
33% drift in three days — and, more importantly, it was the **wrong unit**: the bucket holds 74,032 subjects,
six per instance, because nothing sweeps a delete. Phase 0 of the build re-runs every row above against merged
`main` and against the stack *of the day it runs*; the two figures that decide scope are the cursor count
(against §2a's ~22,000 hard wall) and the subject count (against the 100,000 `JSMaxSubjectDetails` cap on
`StreamInfo` subject details in `nats-server` 2.14.2 — `loom-state` is at 74,032 and climbing ~6,000/day, so
the operator's own inspection surface degrades first, around **2026-09-05**).

## 6. Migration

**Inc 1 needs none** — a listing filter change, no state written.

**Inc 2's marker is absent for every instance that failed before it ships.** Those instances disappear from
`ListInstances` the moment the list stops enumerating cursors — a silent capability loss on exactly the
surface an operator uses to find what to redrive. So Inc 2 ships **with a bounded one-shot backfill**, run
once off the engine's startup path (not on it), gated to run only while it has work:

- page `instance.*` (excluding sub-keys) via `KVListKeysFilter` with an explicit `limit`, fetch each page's
  bodies, and PUT `instance.<id>.failed` for every record whose `Status == StatusFailed`;
- **`limit` stays below 1,024**, so each page's body fetch takes `KVGetMulti`'s server-side fast path rather
  than the consumer-drain fallback that §2a is about. The one enumeration this design deliberately keeps
  large must not be the one that re-creates the failure it removes;
- idempotent and convergent — a partial pass is fine, the next process start continues it;
- one summary log line (scanned / marked / remaining); no health issue, no new control verb.

**Its measured cost, at the numbers this design was re-grounded on.** The `instance.*` listing leg delivers
**12,339 subjects** — one per instance ever created, and no tombstones, because the cursor is the one key Loom
never deletes — followed by 12,339 record bodies in ~13 fast-path pages. (A whole-bucket variant of the same
scan would deliver all 74,032 subjects; the filter is what keeps it to the cursors.) That is ~33% more than
the ~9,260 the increment was originally sized against, and it grows by ~1,000/day until the fire lands.

This is deliberately the **capability-granting** shape, not a verdict-bearing one: it only makes failed
instances *visible*, it deletes nothing, and it has no decision to get wrong. Its enumeration is of the same
family as the scan this design removes from the steady state — paid once, at a start the operator is already
performing to deploy the fix, and never again.

## 7. Contract surface

**Building to, unchanged:** Contract #4 §4.3 (the 24 h tracker horizon — §1.2 is a consequence of it, not a
request to change it); `10-orchestration-loom.md` §10.9 (the dedup sentence stays literally true — the cursor
is still present whenever a re-trigger can arrive); `10-orchestration-substrate.md`'s `triggerLoom` clause and
`instance.<instanceId>` *Named constructs* entry (unchanged; this design is what makes them permanently safe).

**One edit — `docs/contracts/10-orchestration-substrate.md`, the `loom-state` section.** It adds an observable
promise that is currently unstated, in two clauses that land at different times (Andrew, 2026-09-02):

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

**Clause 2 is committed at ratification** — it records a decision that is true today (the record already
persists indefinitely) — with its closing sentence narrowed to *"a design that bounds the retained-record set
must first bound the horizon over which a trigger can be re-emitted"*, so that sweeping delete tombstones (§8,
alternative 9, now the direction) is not foreclosed by the letter of a clause about records. **Clause 1 lands
with the fire's commit**, not at ratification: it narrows what the control plane answers — behaviour the
runtime does not yet have — and a promise the runtime does not yet keep is not committed ahead of its build
(Andrew, 2026-09-01). It is held out of the tree until then; its text of record:

> *The control plane answers for the instances an operator can still act on — those running, and those
> failed and awaiting redrive. A completed instance is not enumerable there; completed-flow history is a
> read-model concern, served by the projection of the `loom.*` lifecycle events, never by asking the engine.
> An instance addressed by id is always answerable while its record exists, whatever its state.*

### 7.1 Affected consumers of the narrowing

**`cmd/lattice/loom list`** — its output shrinks, and its help text (`Short: "List Loom instances (running +
retained terminals)"`) becomes false the moment Inc 2 lands. It changes in the same commit as Inc 2.

**`cmd/loupe`'s Flows tab — affected, and this is the consequence that most shapes the fire.** It is natural to
record Loupe as unaffected: it reads `orchestration-history` as authoritative and the control plane as
enrichment. The second half of that is wrong. `cmd/loupe/flows.go`'s `flowLiveness` badges a history row that
still reads `running` by branching on **absence before status**:

> `if !engineHas { return livenessOrphaned }` — *"Loom has no record of the instance at all: the terminal
> event was lost or the engine died mid-flight."*
> `if loomTerminal(engineStatus) { return livenessStaleHistory }` — *"The flow is not stuck; the projection
> is behind."*

Under Inc 2 a completed instance is **absent** from `ListInstances`. So the ordinary case — a flow completes,
the history row still reads `running` for the length of the lens catch-up window — flips from `stale-history`
to **`orphaned`**. Those two badges tell an operator opposite things: one says *wait a moment*, the other says
*something was lost*. The file already warns against exactly this class in its own comments — *"The engine's
STATUS decides this, not the row's presence in the instance list"*, and, on the decoder, *"the list carries
FINISHED instances too, so a decoder that kept only the ids could not tell a running flow from a remembered
one"*. Inc 2 removes the evidence those comments rest on. The **`failed` half is unharmed**: failed instances
stay in the list by construction, which is what the new index is for.

**The remedy, and whose lane it is.** The badge must stop deriving liveness from list *membership* and instead
address each still-running row **by id** — which the contract edit's own closing sentence makes sound (*"An
instance addressed by id is always answerable while its record exists, whatever its state"*), with
`InspectInstance` as the verb. That is `cmd/loupe/**` — **Stream 3, its own lane and its own build lock** — so
a Lattice fire must not build it. Filed as a **second** Loupe row, distinct from §2.1's presence-probe defect:

> **[Loupe] Flows-tab liveness must not read absence from the list as orphaned**

**Sequencing, binding rather than advisory:** that row must land **with or before** Inc 2. §11 carries the
constraint. This is the one place where this design cannot ship its own correctness inside one lane.

## 8. Alternatives

**Row 1, as the discipline requires: do not have the thing.**

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Delete `ListInstances`.** The expensive consumer is the whole live defect; if nobody needs it, the fix is −1 RPC and no new index. | **Rejected — a real consumer, and it is the operator's only discovery path for redrive.** `RedriveInstance` takes an `instanceId` the operator must first *find*, and `orchestration-history` (the read model) projects `events.loom.>` — which records a `patternFailed`, but the engine's *current* `failed` status after a redrive attempt is Loom's alone. Worse: the read model is keyed `IntoKey: ["instance_id"]`, so it holds **one row per instance id, not per run**, and its `ended_at` / `failure_reason` columns carry `ClearOn: ["loom.patternStarted"]` — a redrive **erases the failure it would have to substitute for**. Deleting the list makes redrive undiscoverable. Kept, and bounded instead. |
| **2** | **Build the row's prescribed dedup tombstone** — delete the cursor at terminal, write a minimal id-scoped record in its place. | **Rejected — same key cardinality, so it fixes bytes and not one consumer's complexity class** (§2). It also *worsens* the cursor's own hazards: an explicit cursor DELETE leaves a permanent marker (dossier #1) which `createInstance`'s `CreateOnly` cursor write then refuses forever, converting a harmless duplicate-trigger Ack into an unbounded Nak loop. |
| **3** | **Collapse the re-dispatch upstream** — seed `triggerLoom`'s `requestId` on the `claimId` so the reclaim dies at the Contract #4 tracker, restoring §10.9's `instanceId == requestId` model and making `R > MaxAge` sufficient. | **Rejected — the tracker's horizon is 24 h by contract** (§1.2), and a userTask gap open longer than a day is the normal case. Recorded because it is the shape a reader will reach for, it has shipped precedent in the same file, and it is wrong for a reason that only Contract #4 §4.3 states. |
| **4** | **A `collapseOnExisting` flag on `StartLoomPattern`** — a reclaim declares itself, so cursor *absence* means "pruned" rather than "never ran" and terminal cursors become freely prunable. | **Rejected — it deletes a live self-heal.** Today a first dispatch whose op never commits is healed by the next reclaim: no cursor ⇒ Loom creates the instance. Under the flag the gap wedges silently and forever. Making the flag sound requires distinguishing "pruned" from "never ran" — which *is* the evidence being deleted. Circular. |
| **5** | **Weaver stops re-dispatching a collapse-intent action once its dispatch is known-landed** (a `dispatched` stamp on the mark, the `RecordProposalDispatch` shape), bounding the horizon at the source so §9's pruning becomes sound. | **The right long-term shape, deferred — §9.** Publish-success is not commit-success, so a sound stamp needs a commit-confirmation channel Weaver does not have; and its payoff today is bytes. Not rejected — sequenced. |
| **6** | **Serve `ListInstances` from the pin index alone** (the 2026-08-27 triage's resolved shape). | **Rejected, as the prior design also found:** pins are running-only, so failed instances vanish from the redrive discovery path. Inc 2 is that shape *plus* the missing half. |
| **7** | **Shrink the terminal cursor's body** (drop `PatternRef`/`SubjectKey`/`PendingToken` at terminal). | **Rejected — measurement.** The record is 7 scalar fields and **195 B measured**; the saving is ~150 B against an instance footprint of ~1,097 B — under 14% of the bytes, **none** of the subjects, and no consumer gets cheaper. It also breaks `ListInstances`' ability to show *what* failed, on the one surface that needs it. |
| **8** | **A `MaxAge` or `MaxBytes` on the `loom-state` bucket.** | **Rejected, emphatically — it is the unsound prune with no author.** JetStream discard would silently delete dedup evidence with no code path aware of it, and the outcome is §1's correctness catastrophe (a historical pattern re-run from step 0 with its committed side effects re-executed). A limit discriminates by age and size; it has no notion of what a message *means*. The current no-cap provisioning is correct and this design's §7 clause 2 is partly there to stop someone "fixing" it. Contrast **9**, which is the sound form of the impulse behind this row. |
| **9** | **Bound the *tombstone* population, not the record set** — sweep the DEL markers the ephemeral sub-keys leave behind (`PurgeDeletes`), or write those deletions as TTL'd **purges** so the markers expire on their own. | **Not rejected — the largest unclaimed win in the item, and deliberately out of this fire.** It reaches **five sixths** of the growth (61,693 of 74,032 subjects today) and it is sound *by construction*, not by argument. Two honest costs, both real; below. |

**Alternative 9 in full, because an enumeration-shaped reading of this item skips the family entirely** — and
skipping it poses §9's ceiling against the wrong denominator.

- **Why it is sound by construction.** `nats.go` v1.52.0's `PurgeDeletes` collects only entries whose
  `Operation()` is `KeyValueDelete` or `KeyValuePurge`. Loom **never deletes `instance.<id>`** — `transition`
  deletes the pin and leaves the cursor standing. So no tombstone sweep, at any age threshold, can remove a
  cursor: the dedup guard is untouchable by this mechanism. **That is a claim alternative 8 cannot make**, and
  it is the whole difference between the two rows.
- **The TTL variant is supported, and the bucket is already provisioned for it.** A TTL'd *purge* is
  expressible (`kv.go`: `if o.ttl > 0 && o.purge` attaches `WithMsgTTL`) while a TTL'd plain *delete* returns
  `ErrTTLOnDeleteNotSupported` — so the deletion has to be re-shaped as a purge, not merely given a TTL.
  `loom-state` is provisioned `PerKeyTTL: true` (`internal/bootstrap/platform_buckets.go`) and the live stream
  carries `allow_msg_ttl=true` with `subject_delete_marker_ttl=1s`.
- **Cost 1 — the stream-admin route is denied to Loom itself.** `internal/natsperm/matrix.go`'s
  `protectedStreamDenies` puts `$JS.API.STREAM.PURGE.<stream>` on every non-bootstrap component's deny list,
  **including the bucket's own owner**. A sweep therefore cannot be a thing the engine does on a timer; it is
  bootstrap's, or an explicit operator verb's. Owning a bucket is not admin over its stream, and a design that
  assumes otherwise fails at the permission matrix, not in review.
- **Cost 2 — the publish-shaped route needs a substrate addition first.** `internal/substrate/batch.go`'s
  `BatchOp` carries `TTL` and `Delete` but **no `Purge`**, and every Loom deletion rides `AtomicBatch`. So the
  TTL'd-purge variant is a substrate change before it is a Loom change — small, but it is platform surface and
  it belongs to whoever answers §9's ceiling, not to the fire that fixes the enumeration.

**Priced in combination** (the standing rule that a rejection may not lean on another rejection's absence):
3+5 together — an upstream collapse *plus* a bounded horizon — would make pruning sound without any Loom-side
dedup substitute. That combination is exactly §9, and its blocker is the commit-confirmation channel, not
either half's individual objection. **5+9 is the pairing that actually closes the ceiling**: 5 licenses
removing the cursor (17% of the subjects), 9 removes the tombstones (83%), and only together do they bound the
keyspace — which is why §9's ceiling must be read against 9's absence, not only 5's. **9 alone is the only row
that improves the ceiling without any cross-component channel at all.** 2+8 (tombstone plus a bucket cap) is
the shape a future author is most likely to reach for and is the most dangerous: a cap over *any* dedup
record, tombstone or cursor, is unsound for the same reason — and 9 is not a weaker version of it but a
different discriminator, operation rather than age.

## 9. The ceiling, and the primitive that would raise it — named, not built

**Decision (Andrew, 2026-09-02): *"cursor alone is fine once tombstones are swept."*** The fork below is resolved
in two halves. The permanent per-instance trace is accepted — as the **195-byte cursor**, not the ~1.1 KB the
bucket carries today — and the five sixths that are delete tombstones are to be swept: §8's alternative 9 is
the direction, filed as its own designer row on the lattice board, with the substrate purge primitive and the
owner-denied stream-admin trap as its design questions. The Weaver-side retraction primitive below stays named
and not built, on the revive trigger as written; the ceiling it would raise is the cursor ledger alone, ~200 B
and one subject per instance.

**The ceiling.** ~1,097 B and **~6 KV subjects** per Loom instance, forever — the cursor plus the five delete
tombstones §2 measures. At 10⁶ instances that is **~1.1 GB** of file storage and **~6×10⁶ subjects** in the KV
stream's in-memory subject index — an operational cost, not a failure. At 10⁸ it is fatal. But two *dated*
bounds arrive long before either: `ListInstances`' `max_payload` wall at ~22,000 instances (§2a), and
`JSMaxSubjectDetails = 100_000` in `nats-server` 2.14.2, which caps the subject detail `StreamInfo` returns —
the operator's own inspection surface, reached at ~16,700 instances. Inc 2 removes the first. **Nothing in
this fire touches the second**, and on this stack it is days away. So the honest statement is that the ledger
is cheap at 10⁶ and needs the work below before 10⁷, while the *subject* count is already the number an
operator trips over. That is **inside a plausible production regime**, not only the multi-cell/hyperscale one,
which is why this is §"For Andrew" and not a footnote.

**And the primitive named below addresses one sixth of it.** Pruning every terminal cursor removes 12,339 of
74,032 subjects — **17%**. The other five sixths are tombstones, which no retention design touches and which
§8's alternative 9 reaches today, without any cross-component channel. Ratifying *"do not prune"* is therefore
a materially smaller concession than a cursor-only reading of the growth makes it look: what is being declined
is a sixth of the growth, and the five sixths stay addressable by a different, sounder mechanism. The fork is
real — its stake is simply smaller, and its alternative nearer, than the subject arithmetic first suggests.

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
The two dated bounds above are explicitly **not** this trigger: the `max_payload` wall is Inc 2's to remove
and the `JSMaxSubjectDetails` cap is alternative 9's. Neither needs this primitive, and neither should be
allowed to smuggle it in.

## 10. Risks

| Risk | Disposition |
|---|---|
| **The `failed` backfill misses instances, so a failed flow is invisible to redrive** | The design's single most consequential failure mode, and the reason §6 is an owned increment rather than a note. Pinned by a test that seeds failed instances through a **real** `createInstance` + `transition` (never `putInstance` — dossier #2), runs the backfill, and asserts the list. |
| **Dropping `complete` from `ListInstances` breaks a consumer** | **Materialized, not hypothetical.** Loupe's Flows badge reads list *absence* as `orphaned`, so the narrowing converts every ordinary `stale-history` row into a lost-flow alarm (§7.1). The remedy is a per-running-row `InspectInstance` and it is Stream 3's; the constraint is that its row lands **with or before** Inc 2. The CLI's output shrinks and its help text changes in the same commit. Pinned on the Lattice side by a test asserting a completed instance is absent and a failed one present — but that test does **not** cover the risk; the Loupe row does. |
| `KVListKeysFilter` collects all matches in memory before paging | True, and still correct here — but say which set. The collected **key** set is bounded by the server-side filter (running ∪ failed, small). The **delivered** set is not: it is one header-only message per matching subject, tombstones included (§3 Inc 1). Memory follows the keys; wire cost follows the subjects. The `limit` is used in §6's backfill, where the key set is deliberately the large one. |
| A poisoned/unparseable record blinds the list | Unchanged posture — `listInstances` already skips-and-logs a per-key unmarshal failure and hard-fails a genuine read error. Inc 2 must preserve both. |
| **`ListInstances` stops answering entirely, on a date** | ~22,000 instances puts `drainDirectGetFallback`'s unchunked `CreateConsumer` request past the 1,048,576-byte `max_payload` (§2a) — ≈10 days from 2026-09-01 at the measured ~1,000/day. This is the risk that sets the item's urgency, and Inc 2 is what removes it. If the fire slips past that date the failure mode changes from "slow" to "gone", and the redrive discovery path goes with it. |
| The operator's own stream inspection degrades before any of this | `JSMaxSubjectDetails = 100_000` caps `StreamInfo`'s subject details; `loom-state` is at 74,032 and climbing ~6,000/day, so `nats stream subjects` stops being complete around **2026-09-05**. Nothing in this fire addresses it — §8's alternative 9 is the lever, and this is the concrete reason it is filed rather than dropped. |
| The sizing figures go stale fast, and did | The prior fire's ~9,260 drifted 33% in three days *and* counted the wrong unit (§5). Every figure here is dated 2026-09-01 22:44; Phase 0 re-runs the censuses and a disagreement is a scope change. |

### Ratification pass (2026-09-02)

Due diligence against the live stack (`KV_loom-state`, NATS 2.14.2, measured 2026-09-01 22:44) and against
pinned source moved the following. Each is folded into the body above; the list exists only so a reader who
saw this design before ratification can find what moved, and so the next author knows which claims were
*tested* rather than reasoned.

1. **Sizing — wrong unit, not just a stale number.** ~300 B and one subject per instance became **~1,097 B
   and ~6 subjects**. Nothing sweeps a delete, so each ephemeral sub-key leaves a permanent tombstone subject.
   §2, §9.
2. **Inc 1's payoff claim refuted.** `ignoreDeletes` is client-side, so tombstones cross the wire. The benefit
   is **2× on the heartbeat, 6× on `pinnedDomains`** — a constant factor, not a complexity class. §3.
3. **Loupe is affected by Inc 2**, converting `stale-history` into `orphaned`. A second Loupe row and a
   binding cross-lane sequencing constraint. §7.1, §10, §11.
4. **`ListInstances` hard-fails on a date** — the unchunked `CreateConsumer` request crosses `max_payload` at
   ~22,000 instances — rather than merely timing out. §2a. This is what makes the item urgent.
5. **Alternative 9 added**: bound the tombstone population. It reaches the five sixths of the growth that §9's
   deferred primitive cannot, and it is sound by construction rather than by argument. §8.
6. **Retired:** the mid-token `*` filter risk — the idiom has three production callers and a passing test, so
   it is grounding, not a risk. **Retargeted:** three citations of a §9.1 that never existed — all three
   carried the Loupe claim item 3 refutes, and now point at §7.1.
7. **The three increments collapse into one fire.** §11.
8. **Re-confirmed unchanged** (listed so they are not re-derived): the three enumeration paths; the two
   terminal write sites and their single choke point; the `failed` marker's lifetime at every boundary; the
   redrive CAS and its `PUT`-not-`CreateOnly` justification; dedup by presence with a status read only for
   crash-resume; the `orchestration-history` projection's existence and its live 12,339 rows; and that §7's
   contract edit is still uncommitted in `main`.

## 11. Decomposition for the Steward

**This is ONE Lattice fire.** Inc 1–3 below are its *parts*, not separately-shippable fires: they touch the
same two functions in `internal/loom/state.go` (`pinnedDomains`, `listInstances`), the same terminal branch in
`transition`, and the same test file — and Inc 3 is documentation. Splitting them buys nothing and costs three
reviews of one shared diff. **Posture-changing** (a
new durable index key on orchestration state, plus an operator-visible narrowing) → full three-layer
adversarial pass with cold reviewers over the whole diff at close. (Depth is the Steward's sizing per
`agents/steward/SKILL.md` §4; this is the recommendation, not a floor.)

**Phase 0 — re-run §5's censuses against merged `main` and against the stack of the day.** Not a formality:
the sizing row already disagreed with the prior fire's figure by 33% and by a factor of six on the unit that
matters. The two numbers that can change scope are the cursor count (against §2a's ~22,000 wall) and the
subject count (against the 100,000 `JSMaxSubjectDetails` cap). Server pin throughout: **NATS 2.14** (the stack
runs 2.14.2; `docker-compose.yml` pins `nats:2.14-alpine`).

**Inc 1 — filtered pin listings.** `health.go`'s counter and `state.go`'s `pinnedDomains` onto
`KVListKeysFilter` with `instance.*.pattern`. `runningInstanceReader` **swaps** its one method rather than
gaining a second, so the whole-keyspace call stops being reachable from that path at compile time.
*Owns:* a fixture test proving the filter returns pins and **not** cursors when both exist (the mutation test
is that the current `instance.>` prefix fails it); a `pinnedDomains` test that its error posture is unchanged.

**Inc 2 — the `failed` index + bounded `ListInstances` + the backfill.**
*Owns:* the marker written on the failed arm and not the complete arm, seeded through a **real terminal
transition**; the redrive delete; a **re-fail after redrive** test (the PUT-not-`CreateOnly` proof — this is
the one that would have caught dossier #1); `ListInstances` returns running ∪ failed and excludes complete;
the backfill's idempotence, partial-pass convergence, and its sub-1,024 page limit; a consumer test that
`cmd/lattice/loom list` renders the narrowed set, with its help text corrected in the same commit.
*Sized against:* a one-shot backfill scan of ~12,339 cursor subjects and as many bodies — ~33% more than the
increment was first sized for, and growing ~1,000/day until it lands.

**Inc 3 — docs + the contract edit.** `docs/components/loom.md`'s keyspace section; the `10-orchestration-substrate.md`
clause (**only if Andrew ratifies it — otherwise this part ships the component doc alone**); **two** new
dossier entries — the enumeration lesson (*"a `prefix>` filter that matches a key's sub-keys is not the index
you think it is — the heartbeat's `instance.` prefix matched every cursor for as long as the pin-index fix has
been shipped"*) and the tombstone lesson (*"in a `max_msgs_per_subject=1` bucket a delete is not a removal: it
leaves a permanent subject that every listing still pays for, and nothing in this tree sweeps them"*).
*Owns:* no test; `lint-board` + `lint-conventions` are its gates.

**Cross-lane constraint, binding on Inc 2.** The Loupe row *[Loupe] Flows-tab liveness must not read absence
from the list as orphaned* (§7.1) must land **with or before** this fire. It is Stream 3's build lock, so the
Lattice fire cannot carry it: the Steward either confirms it has landed or holds Inc 2 — Inc 1 and Inc 3 are
unconstrained and can ship while it is pending. §2.1's separate presence-probe row is **not** a blocker for
either; it is a pre-existing defect, not one this design creates.

## 12. Gates

`go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go` ·
every other `scripts/lint-*.go` · `go run ./scripts/lint-board.go` · `make verify-kernel` ·
`go test ./internal/loom/... ./internal/substrate/... ./internal/bootstrap/...` · full `go test ./...` with
`POSTGRES_TEST_DSN` set.

Build-tagged harnesses: `runningInstanceReader` **swaps** its method (`KVListKeysPrefix` →
`KVListKeysFilter`), so every fake implementing it stops satisfying the interface until updated — and a
build-tagged fake fails to *compile* while `go test ./...` reports green. Enumerate them
(`grep -rl "^//go:build " --include=*_test.go internal/`) and run the tagged targets the loom interfaces
reach. No `packages/` content changes, so no manifest version bump.

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
| The matched-subject fast-path gate is **exactly 1,024**, and it is the **server's** | `nats-server` 2.14.2 `server/stream.go` — `maxAllowedResponses`, passed to `store.MultiLastSeqs`, `ErrTooManyResults` → `413`; recorded in `docs/vendors.md` |
| The fallback drain passes **all** subjects in one unchunked `CreateConsumer` | `internal/substrate/kv_multi.go` — `drainDirectGetFallback`; live `max_payload` 1,048,576 B ⇒ the ~22,000-instance wall |
| `KVListKeysFilter` exists, arbitrary filter, paged, de-duped; mid-token `*` is its documented idiom | `internal/substrate/kv.go`, with production callers in `internal/processor/starlark_kv.go`, `internal/pkgmgr/opmetaretirement.go`, `cmd/lattice/candidates/candidates.go` and a passing mid-`*` test in `internal/substrate` |
| `instanceId` is guarded to a NanoID, so it can never split the `*` token | `internal/loom/engine.go` — the trigger entry |
| `WatchFiltered` prefixes `$KV.<bucket>.` and permits patterns | nats.go v1.52.0 `jetstream/kv.go` (pin: `go.mod`; server **NATS 2.14**, running 2.14.2, `docker-compose.yml` pins `nats:2.14-alpine`) |
| **`ignoreDeletes` is applied client-side**; the consumer is `DeliverLastPerSubject` + `HeadersOnly`, and `ListKeysFiltered` = `WatchFiltered(..., IgnoreDeletes(), MetaOnly())` | nats.go v1.52.0 `jetstream/kv.go` — why tombstones still cross the wire |
| `loom-state` provisioned with no `MaxBytes`/`MaxAge`, `max_msgs_per_subject=1`, and `PerKeyTTL: true` | `internal/bootstrap/platform_buckets.go` + `primordial.go`'s `ProvisionBuckets`; live stream carries `allow_msg_ttl=true`, `subject_delete_marker_ttl=1s` |
| Measured bucket state, 2026-09-01 22:44 | 74,032 subjects · 74,032 msgs · 13,536,548 B; 12,341 live keys; 12,339 cursors; **1** live pin; cursor body 195 B |
| **Nothing sweeps delete tombstones** | `grep -rn PurgeDeletes --include='*.go' .` → zero production hits |
| `PurgeDeletes` collects only `KeyValueDelete`/`KeyValuePurge` entries, so it can never remove a cursor | nats.go v1.52.0 `jetstream/kv.go` — the soundness of §8's alternative 9 |
| A TTL'd **purge** is expressible; a TTL'd plain **delete** returns `ErrTTLOnDeleteNotSupported` | nats.go v1.52.0 `jetstream/kv.go` — the `o.ttl > 0 && o.purge` branch |
| `$JS.API.STREAM.PURGE.<stream>` is denied to every non-bootstrap component, **including the bucket's owner** | `internal/natsperm/matrix.go` — `protectedStreamDenies` |
| `BatchOp` carries `TTL` and `Delete` but **no `Purge`**, and Loom's deletions ride `AtomicBatch` | `internal/substrate/batch.go` |
| `StreamInfo` subject details cap at 100,000 | `nats-server` 2.14.2 `jetstream_api.go` — `JSMaxSubjectDetails` |
| Tracker TTL is 24 h, and the "layer your own dedup" sentence | `docs/contracts/04-idempotency-tracker.md` §4.3 |
| Episode requestId is mark-revision-seeded; task/instance ids are claimId-seeded | `internal/weaver/actuator.go` — the four `derive*` helpers |
| External reclaim mints a **fresh** claimId | `internal/weaver/evaluator.go`, the stale-mark branch |
| `externalDispatchGap` classifies `triggerLoom` by pattern step kinds | `internal/weaver/evaluator.go` |
| Loupe's liveness badge branches on list **absence before status** (`!engineHas ⇒ orphaned`), so the narrowing changes its verdict | `cmd/loupe/flows.go` — `flowLiveness`, and `loomInstanceStatuses`' own doc comment on why the status is "half the answer, not decoration" |
| `cmd/lattice/loom list`'s help text promises *"running + retained terminals"* | `cmd/lattice/loom/loom.go` — `newListCommand` |
| `orchestration-history` exists and is live — but is one row **per instance id, not per run** | `packages/orchestration-base/lenses.go` — lens `loomFlowHistory` → bucket `orchestration-history`, `IntoKey: ["instance_id"]`, an eventStream over `events.loom.>`; `ended_at` / `failure_reason` carry `ClearOn: ["loom.patternStarted"]`. 12,339 live rows, read by `cmd/loupe/flows.go` |
| Control handler timeout 5 s, applied to the list verb | `internal/loom/control/service.go` — `handlerTimeout`, at `opList` |
| A whole-bucket enumeration runs once **per terminal instance** | `internal/loom/engine.go` — `reconcileConsumers`, reached from the complete arm and the fail arm |
| `weaverArtifactLive`'s flow arm returns `err == nil` on a bare presence GET | `cmd/loupe/weaver.go` |
| A collapse-only gap's exhausted budget has no re-arm path | `internal/weaver/control.go` — `reArmDeclines`, pinned by `TestResetRetryBudget_RefusesACollapseOnlyGap` |
| The engine's retry-budget fallback is `directOp`-only | `internal/weaver/evaluator.go` — `defaultDirectOpRetryBudget`'s doc |
| lease-signing deliberately removed its userTask `maxretries` caps | `packages/lease-signing/lenses.go`, `targets.go` |
| Reclaim backoff caps at 24 h | `internal/weaver/reconciler.go` — `defaultReclaimBackoffCap` |
| `instanceId` is optional on the op; absent ⇒ the op's own `requestId` | `packages/orchestration-base/loom_lifecycle.go` |
