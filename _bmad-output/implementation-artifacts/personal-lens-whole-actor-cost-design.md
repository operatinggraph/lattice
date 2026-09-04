# Personal lens whole-actor recompute cost — per-evaluation gate scope, batched engine reads, pipelined publishes

**Status:** ✅ Winston-ratified — build-ready (2026-09-03). No architectural fork, no frozen-contract change
(Andrew, at selection: *"I don't anticipate arch fork, or contract change, so plan and build in one fire"*;
the 2026-08-20 ratification split routes a fork-free, contract-free design to Winston). **Board row:**
`[Refractor] edgeInstances is licensed and derives, yet pays ~15 s per pattern-bound event`
(`backlog/lattice.md`). **Parent:** `personal-lens-derivation-licence-design.md` §15.8, whose close pass
measured the residual and filed the row.

## For Andrew (one-look block)

- **The row's mechanism claim was wrong, and this design corrects it before building.** §15.8 attributed the
  10–22 s per event to *"the whole-actor recompute + keyset frame"* and filed it as needing a sub-actor
  incremental reprojection (a new derivation output + a per-row delete on the personal wire). A wall-clock
  sample of the live handler (§3 C4) puts **~63 %** of the time in the personal envelope's **per-row gate
  reads** — `capabilityread.IsReadable` (a point read, then a consumer-backed key **listing**, per row) and
  `personalinterest.IsRelevant` (a multi-get per row) — **~20 %** in two **synchronous JetStream publishes
  per row** (the upsert and its audit entry), and **~17 %** in the engine's sequential per-neighbour point
  reads. The recompute is whole-actor, and the actor is wide (up to 3,638 rows), but what costs is that every
  one of those rows pays 4–5 network round trips in series. The keyset frame is one publish.
- **The fix keeps the ratified whole-actor model and every retraction semantic unchanged.** Three
  increments, each semantics-preserving: (1) the envelope's two gates are answered from **one per-evaluation
  read per actor** instead of one per row; (2) the full engine **prefetches** a hop's frontier and a
  projection's aspect reads with the substrate's existing multi-get; (3) the personal adapter and the audit
  writer **pipeline** their publishes and await the acks once per batch. Expected: the heaviest actor's
  evaluation drops from ~22 s to well under a second; the §11 bar the parent design left unmet (≥ 1 msg/s on
  pattern-bound events) is met with margin, and every other path that evaluates a whole actor — hydrate on
  device attach, the grant-change reprojection, the personal sweeper — gets the same reduction.
- **What this does not do, named:** the actor's N rows are still re-published to the device per event.
  That is a wire/retention harm (§1.3), not the drain harm this row measures, and its fix *is* the new
  mechanism the row named — filed as its own designer row with the measurement (§12).

## 1. Problem

### 1.1 What was measured (live stack, 2026-09-03 13:02–14:00, Refractor at `7e2ef6b2`'s predecessor build)

`edgeInstances` (`ZdEvei26RWsXY16mZdEv`, personal, one walk `(identity)<-[:providedTo]-(inst:service)`,
`packages/edge-manifest/lenses.go:192-207`) drains its `KV_core-kv` consumer at ~0.5 msg/s with 71 k
unprocessed. Inter-completion gaps from the `pipeline: processed` log, by the event kind its pattern binds
(census C1, 1 500 messages):

| Event kind | n | median | p90 | max |
|---|---|---|---|---|
| `lnk.service.*.providedTo.identity.*` | 105 | 9.03 s | 22.0 s | 57.3 s |
| `vtx.service.*.outcome` | 105 | 8.86 s | 22.6 s | 49.7 s |
| `vtx.service.*` root | 105 | 8.90 s | 22.9 s | 30.7 s |
| `vtx.op.*` (pattern does not bind) | 1 078 | 0.00 s | 0.00 s | 0.03 s |

The derivation acts (`fellBack=0`, parent §15.8) and names one actor per event. That actor is wide: seven
identities carry **1,164–3,638** `providedTo` service instances each (C2), all with a live
`cap-read.edgeManifest` grant (C3: 3,644 for the widest), so every event re-projects 1,164–3,638 rows.

### 1.2 Where the time goes (C4 — 24 one-second goroutine samples of the `edgeInstances` handler)

| Phase | samples | what runs |
|---|---|---|
| `personalEnvelopeFn` → `capabilityread.IsReadable` | 11 | per row: `kv.Get` of the base key, then `ListKeysFilter` (a consumer-backed enumeration, `substrate/kv.go:282`), then `kv.Get` per listed key (`capabilityread.go:86-125`) |
| `personalEnvelopeFn` → `personalinterest.IsRelevant` | 4 | per row: `kv.GetMulti(identityID + ".>")` (`interest.go:291-317`) |
| engine `traverseRel` / `resolveProperty` → `readNode` | 4 | per neighbour and per aspect: one `KVGet` (`executor.go:852-895`, `rel_traverse.go:74`, `values.go:70`) |
| `writeAudit` → `AuditWriter.WriteAudit` → `Conn.Publish` | 4 | per committed row: a synchronous JetStream publish awaiting the store ack (`audit_writer.go:142`, `publish.go:62-76`) |
| `NatsSubjectAdapter.publish` → `Conn.Publish` | 1 | per row: the same synchronous publish (`natssubject.go:424-434`) |

A 30 s CPU profile of the process is 70 % `kevent`/`syscall`/`cond_wait`: the process is idle, waiting on
round trips. Nothing here is compute.

### 1.3 The harm this design does NOT retire (the follow-on's measurement)

Each event re-publishes every row of the actor. The two widest actors' SYNC subjects sit at the stream's
10,000-message per-subject cap (C5), i.e. under three events of history, and the stream holds 736 k deleted
messages against its 512 MiB cap. A device offline across three events must re-hydrate. That is the harm a
delta publication would retire; it is not what makes the consumer drain at 0.5 msg/s, and it is filed
separately (§12).

## 2. Grounding ledger (verified in code this fire)

| # | Claim | file:line |
|---|---|---|
| G1 | The personal envelope runs per RETURN row and calls `IsReadable` then `IsRelevant`, each a live read | `projection/personal.go:157-235` |
| G2 | `IsReadable` = base-key point read → `ListKeysFilter("cap-read.*.<actor>.<anchor>")` → point read per listed key; fail-closed | `capabilityread/capabilityread.go:86-148` |
| G3 | `ListKeysFilter` opens a `ListKeysFiltered` lister (an ephemeral consumer) and drains it per call | `substrate/kv.go:282-315` |
| G4 | `IsRelevant` = `GetMulti(identityID + ".>")` per call; no registration ⇒ admit | `personalinterest/interest.go:291-317` |
| G5 | The envelope is applied inside `executeFullForActorOnce`, after `executeBranches`, with the evaluation's `params` | `pipeline/evaluate.go:751-830` |
| G6 | `EnvelopeFn` has no scope argument; `SetEnvelopeFn`/`SetMultiEnvelopeFn` are alternatives and `envelopeFn != nil` is read as "envelope installed" by the seeding, collision-guard and footprint predicates | `pipeline/pipeline.go:714-732, 832-850`; `evaluate.go:257, 534-549, 880-884` |
| G7 | `traverseRel` fetches each admitted neighbour with `fetchNode` in series; `fetchNode` memoizes per evaluation, absent ⇒ nil memoized; the footprint is built from that memo (absent ⇒ revision 0) | `full/rel_traverse.go:58-100`, `full/executor.go:852-895, 500-530` |
| G8 | An aspect reference `node.<aspect>` is a point read of `<nodeKey>.<aspect>` through the same memo; a relationship binding's property is a point read of the link key | `full/values.go:23-80` |
| G9 | `KVGetMulti` / `KVGetMultiNoSnapshot`: ≤ 1,024 matched subjects on one atomic fast path; past it a consumer drain, verified (double) or single | `substrate/kv_multi.go:192-270` |
| G10 | The adjacency store already batches its two-key node-state read with `GetMulti`; its hub read uses `GetMultiNoSnapshot` because the set is independent facts re-validated by the footprint | `adjacency/overflow.go:148-170`, `kv_multi.go:212-240` |
| G11 | `Conn.Publish` is `js.PublishMsg` — one store-ack round trip per call; nats.go v1.52.0 exposes `PublishMsgAsync` returning a `PubAckFuture` | `substrate/publish.go:62-76`; `nats.go@v1.52.0/jetstream/publish.go:274` |
| G12 | The personal adapter's Upsert / Delete / keyset / marker all funnel through one `publish`; it holds no state; ordering is the stream's per-subject sequence | `adapter/natssubject.go:60-75, 320-352, 406-434` |
| G13 | `writeResults` writes rows in series, audits each committed one, then the caller emits frames; a personal target's failures are classified transient/infra ⇒ Nak | `pipeline/results.go:34-250, 443-460`; `evaluate.go:1288-1320` |
| G14 | `Hydrate` and `ReprojectPersonalActor` have their own serial write loops over the same adapter, then publish the frame | `pipeline/hydrate.go:80-125`, `reproject_personal.go:143-225` |
| G15 | Two ctx-carried test/behaviour hooks are the precedent for a per-call value the pipeline threads without a signature change | `pipeline/anchor_derivation_plain.go` (`isPlainDerivedAnchorReentry`), `full/executor.go` (`footprintCapturedHook`), `adjacency/readobserver.go` |
| G16 | The change-posture lint gate is a symbol→annotation table; a new live-read symbol not in the table is unguarded | `scripts/lint-conventions.go:3131-3150` |
| G17 | The Edge client applies `upsert` / `delete` / `keyset` / `hydrationComplete`; a frame prunes by lens attribution | `edge/sync/sync.go:1085-1140` |

## 3. Executable censuses (commands + the numbers pinned this fire)

```sh
# C1 — per-event-kind inter-completion gaps for edgeInstances (last 1 500 processed lines)
grep '"pipeline: processed"' refractor.log | grep ZdEvei26RWsXY16mZdEv | tail -1500 | <bucket by entityId shape; median/p90/max>
#   ⇒ providedTo-link 9.03 s / 22.0 s / 57.3 s · service.outcome 8.86 / 22.6 / 49.7 · service-root 8.90 / 22.9 / 30.7 · op 0.00 / 0.00 / 0.03
# C2 — providedTo instances per identity
nats --server=localhost:4222 --nkey=$PWD/deploy/nkeys/lattice.nk kv ls core-kv \
  | grep -E '^lnk\.service\.[^.]+\.providedTo\.identity\.' | sed -E 's/^.*\.identity\.//' | sort | uniq -c | sort -rn | head
#   ⇒ 3638 · 1891 · 1627 · 1450 · 1327 · 1169 · 1164 · then ≤ 2 (12,288 links, 175 identities, 7 instanceOf service→service)
# C3 — live read grants for the widest actor
nats ... kv ls capability-kv | grep -cE '^cap-read\.edgeManifest\.identity\.edu97ixj2CJB6auNi6L4\.'   # ⇒ 3644 (12,636 in the domain)
# C4 — where the handler's wall clock goes: 24 × (curl 127.0.0.1:6070/debug/pprof/goroutine?debug=2; sleep 1),
#      the goroutine whose (*Pipeline).handle frame carries the edgeInstances consumer's stream sequence
#   ⇒ IsReadable 11 (listing 7, point read 4) · IsRelevant 4 · engine readNode 4 · audit publish 4 · adapter publish 1
# C5 — SYNC per-subject depth for the two widest actors
nats ... stream subjects SYNC | grep -E 'edu97ixj2CJB6auNi6L4|dzst9ZB6Q8Jhw4m9hHVG'   # ⇒ 10,000 · 10,000 (the per-subject cap)
```

### 3.1 Read-cost probe (C6, live, 16:45 — read-only, the widest actor; run twice each)

| Read | cost |
|---|---|
| the two `cap-read` wildcards for the actor, 3,671 matched (past the cap ⇒ consumer drain) | **3.0–3.4 s** |
| the same set as a keys-only `ListKeysFilter` ×2 | 0.18 s |
| the same 3,671 keys as exact-key multi-gets in ≤ 1,024 chunks | ~0.04 s (one 1,000-key request: 10–14 ms) |
| exact keys at the boundary: 1,024 ⇒ 10 ms; **1,025 ⇒ 1.2–1.8 s** (the drain) | the cap is exactly 1,024 |
| the marked identity hub's `providedTo` links (3,638, two wildcard filters ⇒ drain) | 0.8–2.7 s |
| the same as a keys-only listing | 0.47 s |

So a whole-actor evaluation of the widest actor was paying two consumer drains — Inc 1's own grant-set read
and the hub's link read — at ~1 ms per key, ≈ 96 % of the ~4 s that remained after Inc 1+2 (24-sample
profile of the live handler: 13 in `ReadableAnchors`, 10 in `neighborsFromCoreKV`, 1 in `Registrations`,
0 in the publishes). **Inc 4 (found at build):** `KVGetMultiNoSnapshot`'s past-the-cap path becomes
list-then-get — wildcards expanded to exact keys through the listing, then fetched in ≤ 1,024-key atomic
requests — so every `NoSnapshot` caller past the cap (the grant set, the marked hub, any executor prefetch
that overflows) pays ~0.1–0.2 ms per key instead of ~1 ms. The snapshot-verified `KVGetMulti` keeps its
double drain: its guarantee is the comparison, which list-then-get cannot offer.

*(Amended at build, 2026-09-03 — found by a real test failure, `TestNeighbors_MarkedNodeIsNeverQuietlyShort`.)*
**A single listing is not a sound resolution.** nats.go's `ListKeysFiltered` is a `WatchFiltered(…,
IgnoreDeletes(), MetaOnly())` whose "initial values received" marker fires on `received >= initPending ||
delta == 0` with `initPending` captured at consumer creation (nats.go v1.52.0 `jetstream/kv.go`) — the same
count-bounded stop condition `drainDirectGetFallback`'s own doc identifies as unsound on a history-1 stream:
a rewrite during the enumeration erases the message the count counted and appends a new one later, the
counts balance, and the enumeration ends with keys undelivered. On the capability plane a short listing is a
silently disappearing grant. So the resolution enumerates each filter **twice and requires the key sets to
agree** (bounded retries, else a loud error), which is cheap because a rewrite never changes the key set:
the widest actor's listing cost is ~0.36 s, against the 3.0–3.4 s drain. The stop condition is now a
load-bearing vendor behaviour and has its row in `docs/vendors.md`.

## 4. The shape

### 4.1 Increment 1 — the envelope's gates are answered once per actor evaluation

**`capabilityread.ReadableAnchors(ctx, kv, actorType, actorID) (*AnchorSet, error)`** — one
`GetMultiNoSnapshot` over the two filters `cap-read.<actorSuffix>.*` and `cap-read.*.<actorSuffix>.*`,
decoding each body's `isDeleted`, yielding the set of anchor IDs with at least one live key. Its membership
answer for every anchor is **by construction identical to `IsReadable`** (base key live OR any domain key
live; tombstoned ⇒ not admitted; absent ⇒ not admitted) and the equivalence is pinned by a property test
over random grant layouts. Same metacharacter refusals as `IsReadable`. `NoSnapshot` is the correct posture,
not a shortcut: today's per-row reads already blend instants across rows, the grant-change edge re-drives
the actor when a grant moves, and the widest actor's key set (3,675) is past the fast-path cap where the
verified double drain fails under any concurrent write (G9/G10's argument, reused verbatim).

**`personalinterest.Registrations(ctx, kv, identityID) ([]Registration, error)`** — the existing
`GetMulti(identityID + ".>")` once; **`RelevantIn(regs, anchorType, anchorID) bool`** — the pure
per-row predicate `IsRelevant` already computes, factored so `IsRelevant` becomes `RelevantIn(Registrations(...))`.

**The scope hook.** `pipeline.EnvelopeScopeFn func(ctx context.Context, params map[string]any) (map[string]any, error)`
installed by `SetEnvelopeScope`. `executeFullForActorOnce` calls it **after** `executeBranches` and only
when the engine returned rows; it merges the returned entries into a **copy** of `params` that is handed to
the envelope only — the engine's own `Parameters` map is untouched, so no `$name` can ever observe them.
`personalEnvelopeFn` reads the anchor set and the registrations from those entries (keys are unexported
constants in `projection`) and falls back to today's per-row `IsReadable` / `IsRelevant` when absent — so
every existing envelope test still passes and the scope path is proved by tests that make the two answers
*disagree* (scope admits, KV denies ⇒ published; scope denies, KV admits ⇒ skipped). `envelopeFn != nil`
readers (G6) are untouched: the scoped envelope is still installed through `SetEnvelopeFn`.

**The gate is extended, not bypassed.** `ReadableAnchors` and `Registrations` join `changePostureRules`
under the same annotations (`grant-change-posture`, `interest-change-posture`) with the identical
default-deny; the new call sites in `personal.go` carry the identical `(subscribed)` declarations.

**Ordering of reads within an evaluation.** The scope is taken after the graph walk, so it is never staler
than the rows it gates; a grant landing between the two is at worst the same window today's per-row read
has, and the edge re-drives it.

### 4.2 Increment 2 — the full engine reads a frontier and a projection's aspects in batches

- **`traverseRel`**: after a frontier node's edge list is filtered (type, direction, unseen), the other-end
  keys not yet in `ex.nodes` are fetched with **one chunked `GetMultiNoSnapshot`** (exact keys, ≤ 1,024
  per chunk, so every chunk stays on the atomic fast path) and memoized through the same decoder
  `readNode` uses — `isDeleted` ⇒ nil, absent ⇒ nil, revision from the entry — so the loop's `fetchNode`
  calls hit the memo and the footprint is **byte-identical** to the point-read footprint (pinned by a test
  that runs one rule both ways and compares `EvalFootprint`).
- **Projection prefetch**: before `projectItems` (both arms) and `applyWith`'s WHERE evaluate a stage's
  bindings, an expression walk (`walkExprAll`) collects every first-hop `PropertyAccess{VariableRef v, key}`;
  for each binding whose `v` is a live node with `key` absent from its root body and `key != "key"`, the
  aspect key `<nodeKey>.<key>` is collected (a relationship binding contributes its link key); the set minus
  what is memoized is fetched in one chunked multi-get and memoized. `resolveProperty` is unchanged and
  keeps serving from the memo. The read-free executor (`coreKV == nil`) prefetches nothing.
- **Proof shape**: a test-only point-read counter on the executor; N neighbours ⇒ 0 point reads after
  prefetch, and with the prefetch disabled ⇒ N (the revert-proof, run in the builder's own worktree).
- *(Amended at build, 2026-09-03.)* **Prefetched values are STAGED, not memoized**: `prefetchNodes` fills a
  separate staging map and `fetchNode` promotes an entry into the memo on its first dereference. Direct
  memoization would have broken the byte-identical-footprint promise above — `walkExprAll` collects keys
  from expression positions the evaluation may never evaluate (`evalExpr` short-circuits `AND`/`OR`, `CASE`
  arms and `coalesce`; the corpus has 16 CASE arms and 45 non-first `AND` operands dereferencing an aspect),
  so a memoized-but-never-read key would enter the footprint at a revision no point read ever observed.
  Staging makes the footprint identical by construction for every rule, not only the ones a test picks.
- *(Amended at build, 2026-09-03 — Inc 2b, found by Inc 2's own census.)* **A stage's bound-source frontier
  reads its adjacency in one batch too.** `applyMatch` expands each binding from its bound first node, and
  `traverseRel`'s `fetchEdges` costs one adjacency node-state read per source node, so
  `OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)` over 3,638 bound rows was 3,638 serial round trips
  the Core-KV prefetch never touched. `adjacency` gains a chunked multi-node state read that answers exactly
  what `NeighborsScoped`/`Neighbors` answer for an UNMARKED node (doc edges + doc revision; absent ⇒ empty at
  0) and returns a marked hub as *marked, no edges* so it keeps its per-node scoped path; the executor stages
  those answers and `fetchEdges` promotes them through `memoizeWhole` at first use, so composition with pinned
  relations, the `edgeRevisions` record and the read-observer observations are the per-node path's. Sources
  are collected per `applyMatch` pattern (bound first node), per pattern comprehension and per existence
  pattern (`PatternExpr`, incl. its negated form) in a projection or WHERE. Two details as built: the
  read-observer observation fires at **promotion**, not at batch time — `matchPath` gates a bound head on
  its label/property predicates before it hops, so a batch-time observation would announce nodes the walk
  never reaches; and the batch is chunked **by node at 512** so a node's document and its overflow mark are
  always read in one request (the pairing `readNodeState` relies on to never see an emptied document
  without the mark that explains it).
- **Branch-decomposed stages (Inc 2c, absorbed at build).** A `stagePlan` — an aggregating `WITH`/`RETURN`
  whose sibling OPTIONAL MATCH branches are deferred into the fold — expands each branch per base row inside
  the fold loop, so the stage-level prefetch above never sees the branch's variables. The corpus census
  (`branch_decomposition_corpus_census_pins_test.go`) pins the decomposed set: one personal lens
  (`edgeIdentity`, 8 deferred groups), `myTasks`, the three `edgeManifest*ReadGrants` producers, the
  `capability*` aggregates and a dozen vertical read models. A deferred branch's **first node is bound in
  the base row**, so its adjacency source batch is applied across all base rows before the fold, exactly
  as `applyMatch` does; what stays per row is the aspect dereference of a node the branch only binds during
  its own expansion (the branches are deliberately not co-resident — that is what decomposition bounds).
  Trigger for going further: a decomposed stage whose deferred branch dereferences aspects over a wide
  actor; the fix is a bounded per-branch expansion window, which changes the memory bound decomposition
  exists to hold and so needs its own design.
- *(Amended at build, 2026-09-03 — Inc 2 cold review.)* A batch is bounded by **count and by size**: a chunk
  whose aggregate value size trips the connection's 64 MiB response ceiling is split in half and retried down
  to a floor — and ONLY on that signature (`substrate.ErrDirectGetAttemptsExhausted`, the deterministic
  over-size case); any other error propagates unchanged on the first request, so a transport stall is not
  amplified by the descent — so a wide frontier of large documents degrades to more requests, never to a
  wedged evaluation;
  a body or adjacency document that fails to decode is **not staged** (Warn), so the point read fires iff the
  evaluation dereferences it and fails exactly where it did before batching (R6's rule, mirrored); the
  request count per batch is pinned by a `batchReads` counter so a shrunken chunk constant cannot pass CI
  with the payoff gone; and `traverseRel`'s multi-hop frontier batches its adjacency at the top of each hop.
- **Not covered, named:** a `MATCH`/`OPTIONAL MATCH` clause's OWN `WHERE` whose existence pattern's subject
  is bound by that same clause (`packages/service-location/lenses.go:170-171`, `capabilityServiceAccess`:
  `… <-[:availableAt]-(svc:service) WHERE NOT (svc)-[:instanceOf]->(svcTpl:service) AND …`). The predicate
  runs per row inside `applyMatch`'s loop before the clause's own sources exist, so batching it means
  hoisting the WHERE out of that loop — a restructure, one corpus instance, an actor-aggregate lens whose
  per-actor row count is small. Its `[(svc)-[:permitsOperation]->(op) …]` comprehension IS batched.

### 4.3 Increment 3 — publishes are pipelined, acks awaited once per batch

- **`substrate.PublishPipeline`** (`publish_pipeline.go`; *named at build so it cannot be read as the
  pre-existing atomic `Conn.PublishBatch`, which is all-or-nothing — this one is ordered async publishes with
  no atomicity*): `Conn.NewPublishPipeline(window int)`; `Add(ctx, subject,
  data)` issues `js.PublishMsgAsync` and, when `window` futures are in flight, awaits the oldest; `Flush(ctx)`
  awaits every outstanding future and returns the **first** error. Ordering is the connection's send order
  (unchanged from the synchronous path). The window (256) keeps a wide batch under nats.go's 4,000
  outstanding-async-publish default with headroom for the other pipelines sharing the connection.
- **The pipeline rides the ctx** (G15's precedent): `adapter.WithPublishPipeline(ctx, p)`; `NatsSubjectAdapter.publish`
  adds to a pipeline found on the ctx and otherwise publishes synchronously as today. No adapter interface
  changes, no adapter state, and the adapter's concurrent callers (consumer goroutine, hydrate, reprojection)
  each own their batch.
- **Callers**: `writeResults`, `Hydrate`, `ReprojectPersonalActor` open a batch around their write loop and
  **flush before the frame** (the frame's "only after the batch cleanly applied" contract, unchanged). A
  flush error is a write error of the batch: `writeResults` Naks (today's category for a personal publish
  failure), the other two return it.
- **Audit**: `AuditWriter` gains the same async shape with its own pipeline (never shared with the row
  pipeline — an audit failure must not fail a row batch); `writeResults` flushes it at the end and logs
  errors, exactly today's best-effort posture. *(Amended at build, 2026-09-03 — Inc 3 cold review.)* The
  loop's audit tuples and the projected-count / freshness-clock updates are **buffered until the row
  pipeline's flush succeeds**: an audit entry asserts a row that landed, and a pipelined write is not known to
  have landed until its ack. The window is sized against the **connection's** async budget, not per pipeline
  (one `substrate.Conn` per process; sixteen personal lenses × two pipelines): `WithPublishAsyncMaxPending`
  raised to 8,192 and the default window 128, pinned by a corpus-count test.

### 4.4 Non-goals

- Delta publication (sub-actor seeded evaluation + per-row delete, or a publication memo) — §12.
- Any change to `IsReadable`'s or `IsRelevant`'s own semantics, the D1 key shapes, or the frame model.
- The `KV_core-kv` adapters (guarded, per-row revisions): they keep synchronous writes.
- `edgeManifestReadGrants` (106 k backlog — the `WITH`-scope row) and `leaseApplicationComplete` (Surveyor).

## 5. New state, and its lifetime

| State | Created | Reset | Carried | Ordered |
|---|---|---|---|---|
| per-evaluation gate scope (`*AnchorSet`, `[]Registration`) in the envelope's params copy | after `executeBranches`, per `executeFullForActorOnce` call | dropped with the call — never outlives one evaluation, never cached across actors or events | not carried | one read per actor evaluation, taken after the walk |
| executor staging maps `prefetched` (Core-KV bodies) and `prefetchedEdges` (adjacency answers) — *amended at build, 2026-09-03* | per evaluation, filled by each batch | an entry is **deleted on promotion** into the memo (first dereference), so the map holds only not-yet-used answers; the whole map dies with the executor | not carried | bounded by the keys one evaluation's expressions and frontiers name — for the widest actor a few thousand decoded bodies at most; a key the evaluation never uses never reaches the memo or the footprint |
| executor node/edge memo entries promoted from staging | per evaluation, same memos as today | with the executor | not carried | identical revisions/absences/observations to point reads (pinned on/off) |
| `PublishPipeline` futures | per write loop, on the ctx | flushed before the frame (the single error seam — `Add` never surfaces another message's failure); every future is bounded by the connection's 5 s async ack timeout, so an ack that never arrives surfaces at `Flush` as a timeout, never a wedge | not carried | connection send order; nats.go's no-responders retry can re-send one message ~250 ms later, which `Flush` still awaits before the frame |
| executor point-read counter | test builds only | — | — | — |

No registry, no cache across evaluations, no new latch.

## 6. Reconciliation with the existing mental model

*"The frame is whole-actor by ratified design, so the recompute must be whole-actor."* — True and kept.
This design changes how much a whole-actor evaluation costs, not what it computes or publishes.
*"Isn't the per-row `IsReadable` the security boundary?"* — It stays the boundary; the same predicate is
evaluated over the same keys, read once per actor instead of once per row, under the same change edge.
*"Why not the sub-actor path the row asked for?"* — Because it would leave hydrate, the grant-change
reprojection and the sweeper at 10–22 s per wide actor (they are whole-actor by necessity), and because the
measured cost is not where the row placed it.

## 7. Contract surface

None. No `docs/contracts/*` names the personal envelope's read strategy, the engine's read batching, or the
audit stream's publish mode. Docs owed in-fire: `docs/components/refractor.md`'s personal-lens transport and
rule-engine rows (batched reads, pipelined publishes, the per-actor gate scope).

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 0 | **Do nothing.** | Rejected: 71 k backlog at ~0.5 msg/s, and the cost is per event for every wide actor; the demo box's "my orders" lags 10–60 s per order event. |
| 1 | **Sub-actor incremental reprojection** (the row's prescription). | Not this fire: it fixes only the CDC path and needs a new derivation output plus a per-row delete on the personal wire; the measured cost is in the per-row gates and publishes, which it would not touch for the whole-actor paths. Re-filed with the wire harm (§12). |
| 2 | **Rewrite the lens** (bound "my orders" by time). | Rejected: a correct lens (v1 scope-down is deliberate, `edge-manifest.md`) and the platform must handle a 3,638-row actor; also leaves every other personal lens on the same per-row cost. |
| 3 | **Cache the grant set across evaluations.** | Rejected: a security memo with a lifetime beyond the evaluation is a stale-admit hazard; the per-evaluation scope has no lifetime to reason about. |
| 4 | **Parallelise the per-row reads with a worker pool** instead of a scope. | Rejected: N listings in parallel is N ephemeral consumers on the server; one read is strictly less work and simpler. |
| 5 | **Raise `KVGetMulti`'s use to the snapshot-verified variant for the grant set.** | Rejected: past 1,024 keys the double drain fails under any concurrent write (G9), which for the actors that matter is the normal condition; today's per-row posture blends instants anyway. |

## 9. Risks

| # | Risk | Direction | Mitigation |
|---|---|---|---|
| R1 | The anchor set and `IsReadable` disagree on some layout (tombstone, domain key only, base key only). | Over-grant if the set admits what the per-row read would deny. | Property test over random layouts asserts equality for every anchor; the set is built from the same key shapes and the same tombstone rule. |
| R2 | Prefetch seeds the memo with a value a point read would not have produced (a null body, a tombstone). | Wrong row or wrong footprint. | One decoder for both paths; footprint-equality pin on/off; absent keys memoized nil exactly as today. |
| R3 | A batched publish fails after earlier ones in the batch landed. | Partial batch on the wire. | Same as today's mid-loop failure: Nak ⇒ full redelivery; upserts are idempotent; the frame is withheld. |
| R4 | The async window starves another pipeline of nats.go's outstanding-publish budget. | Latency elsewhere. | Window 256 ≪ 4,000; `Add` awaits the oldest future rather than queueing without bound. |
| R5 | A listing over `cap-read.*.<actor>.*` returns a sibling actor's keys. | Over-grant. | The actor suffix is a literal token pair in both filters; `TestIsReadable_DoesNotLeakAcrossActors` is mirrored for the set. |
| R6 | *(added at build, 2026-09-03 — Inc 1 cold review)* The set reads every one of the actor's grant bodies, so one corrupt body would have failed the whole actor's evaluation on every redelivery, where the per-row read touched only the gated anchor's key. | Fail-closed, but a redelivery wedge for that actor's whole personal plane. | An unparseable body is logged at Warn and its anchor is simply not admitted — fail-closed per anchor; the asymmetry with `IsReadable` (which errors) is pinned in the equivalence test. |
| R8 | *(added at build, 2026-09-03 — Inc 3 cold review)* A pipelined publish whose ack never arrives (a JetStream leader dying after accepting the request) would block the flush forever on the deadline-less consumer ctx, wedging the lens with the message neither acked nor naked, where the synchronous publish was bounded at nats.go's 5 s default. | Availability, one lens. | `jetstream.WithPublishAsyncTimeout(5 s)` on the connection's JetStream handle mirrors the sync default and resolves abandoned futures; the timeout surfaces at `Flush` as a write error ⇒ Nak ⇒ redelivery. Pinned by a `NoAck`-stream test. |
| R9 | *(added at build, 2026-09-03 — Inc 3 verification)* The connection's async-publish ceiling (8,192) is shared by every pipeline in the process; the write steps of the 15 personal lenses reserve 3,840, and nothing caps concurrent hydrate RPCs or reprojections, so ~34 simultaneous whole-actor calls (a fleet re-attach after a Refractor restart) exhaust it. | Availability at the moment of a mass re-attach, fail-closed: a stalled `PublishMsgAsync` is recorded and surfaces at `Flush` — the hydrate returns an error and the device re-attaches, a row batch Naks; never a wedge, never an Ack. | The census test states the real term (concurrent whole-actor calls × window) and the figure; a concurrency cap on hydrate RPCs is the control plane's own design if a fleet ever reaches that scale. |
| R7 | *(added at build, 2026-09-03 — Inc 1 cold review)* The widest actors' grant sets exceed the multi-get's 1,024 fast-path cap, so production always takes the consumer drain, whose contract requires a caller deadline; the consumer ctx has none. | A starved drain stalls the personal consumer up to 80 s per event. | The scope read is bounded by a 15 s timeout inside `personalEnvelopeScope`; a timeout is a loud evaluation error the Nak path handles. The drain path itself is now pinned by a past-the-cap test over both key shapes with a second actor present. |

## 10. Test strategy

- `capabilityread`: `TestReadableAnchors_EqualsIsReadableOverRandomLayouts` (property), `_DoesNotLeakAcrossActors`,
  `_TombstonedExcluded`, `_RejectsMetacharacters`, `_KVFailurePropagates`. `personalinterest`:
  `TestRelevantIn_MatchesIsRelevant` table + `Registrations_ScopedToIdentity`.
- `projection`: `TestPersonalEnvelope_ScopeWinsOverKV` (both disagreement directions), `_FallsBackWithoutScope`.
- `pipeline`: `TestExecuteFullForActor_TakesScopeOncePerActor` (a counting scope fn: N rows ⇒ 1 call; 0 rows ⇒ 0),
  `TestWriteResults_FlushesBatchBeforeFrame`, `_FlushFailureNaks`; hydrate/reproject batch tests.
- `full`: `TestTraverseRel_PrefetchZeroesPointReads`, `TestProjection_AspectPrefetch`, `TestPrefetch_FootprintIdentical`.
- `substrate`: `TestPublishPipeline_OrderPreserved`, `_FlushReturnsFirstError`, `_WindowBoundsInFlight`, the
  never-acked (`NoAck` stream) timeout. `adapter`: ctx-pipeline on/off. `health`: audit pipeline flush logs,
  never fails the caller.
- `scripts/lint-conventions.go`: self-tests for the two new symbols (undeclared ⇒ denied).
- **Live acceptance** (after `make cycle-refractor` from `main`): C1 re-run — pattern-bound kinds at **p50 < 1 s,
  p90 < 2 s** (from 8.9 s / 22 s); the consumer drains ≥ 1 msg/s until empty; hydrate of the widest actor
  timed from Loupe/CLI.

### 10.1 Live measurements (MERGED ≠ RUNNING — each increment cycled from `main` and re-measured)

| Build live | `providedTo`-link median / p90 | `service.outcome` | `service` root | consumer |
|---|---|---|---|---|
| baseline (C1, 13:02–14:00) | 9.03 s / 22.0 s | 8.86 s / 22.6 s | 8.90 s / 22.9 s | 71 k → 116 k (growing) |
| Inc 1 (`0712aa14`, cycled 16:06, read 16:06–16:16, 646 msgs) | **4.72 s / 7.85 s** | — (none in window) | 0.00 s (the window's roots were the supersession purge's soft-deletes) | 116 k → 29 k in 10 min |
| Inc 1+2 (`03bcfca1`, cycled 16:23, read 16:23–16:33, 651 msgs; licensed at 16:25) | **3.99 s / 7.25 s** | — | 0.00 s | 29 k → 28 k (the window was 86 % `providedTo` events) |

## 11. Decomposition for the Steward

One fire, four increments (Inc 2 carries the 2b/2c adjacency and deferred-branch batches found at build;
Inc 4 — the substrate's `NoSnapshot` past-the-cap path — was found by the live measurement after Inc 1+2
landed, §3.1), landed on `main` in order (each independently green and semantics-preserving —
the *land each increment* shape; the invariant that keeps main correct across boundaries is that every
increment's fallback is today's path). Build tier: **opus** for all three (a security-plane read memo, an
engine read-consistency change, an ack-discipline change); cold **opus** review per increment sized to its
diff, plus one cumulative close pass.

## 12. Board + doc actions — APPLIED

- The row flips `📐 → 🏗️` pointing here; size M → L.
- New designer row: **`[Refractor] a personal lens republishes its whole actor per event`** — the §1.3
  harm, `no-pattern: delta publication for a personal lens` (the frame is whole-actor by ratified design;
  candidates: sub-actor seeded evaluation + per-row delete, or a per-(lens, actor) publication memo), with C5.
- Parent design §15.8's attribution sentence is struck in place (ratification-banner-rewrites-body, at build).

## 13. Fire brief (build note, 2026-09-03)

### 13.1 Scope sentence (verbatim)

Board row: *"`edgeInstances` is licensed and derives, yet pays ~15 s per pattern-bound event — the derivation
names one actor per service/`providedTo`/`.outcome` event; the whole-actor recompute + keyset frame then
costs 10–22 s, so it drains at 0.13 msg/s (96 k backlog) while `edgeCatalog` drains the same events at
25 msg/s."* Ratified scope (§4): **(1)** the personal envelope's two gates answered once per actor
evaluation; **(2)** the full engine's frontier and aspect reads batched through the substrate multi-get;
**(3)** the personal adapter's and audit writer's publishes pipelined with acks awaited per batch. **Green
bar:** every gate green; C1 re-measured live at p50 < 1 s / p90 < 2 s on pattern-bound kinds; the
`edgeInstances` consumer draining ≥ 1 msg/s.

### 13.2 Verified touch-list (checked live at `476fb2a7`, 2026-09-03)

| Increment | File | Anchor | Edit |
|---|---|---|---|
| 1 | `internal/refractor/capabilityread/capabilityread.go` | `:57-62` key builders, `:86-148` `IsReadable`/`checkPerAnchorKey` | add `AnchorSet` + `ReadableAnchors` (two filters, `GetMultiNoSnapshot`, same tombstone rule, same metacharacter refusals); `IsReadable` unchanged |
| 1 | `internal/refractor/personalinterest/interest.go` | `:291-317` `IsRelevant` | add `Registration` (exported view of `registrationDoc`'s types/anchors), `Registrations`, `RelevantIn`; `IsRelevant` = `RelevantIn(Registrations(...))` |
| 1 | `internal/refractor/projection/personal.go` | `:106-135` `InstallPersonalLens`, `:157-235` `personalEnvelopeFn` | install `SetEnvelopeScope(personalEnvelopeScope(interestKV, capKV))`; envelope reads the scope entries first, per-row fallback kept with its existing annotations |
| 1 | `internal/refractor/pipeline/pipeline.go` | `:714-732` `EnvelopeFn` docs, `:832-850` setters | `EnvelopeScopeFn` type + `SetEnvelopeScope` (nil = none); doc the params-copy contract |
| 1 | `internal/refractor/pipeline/evaluate.go` | `:751-830` `executeFullForActorOnce` | after `executeBranches`, if rows and a scope fn: call it, merge into a copy of `params` used only by the envelope loop |
| 1 | `scripts/lint-conventions.go` | `:3131-3150` `changePostureRules`, `:3686-3720` self-tests | two new rows (`ReadableAnchors` → `grant-change-posture`, `Registrations` → `interest-change-posture`) + self-tests |
| 2 | `internal/refractor/ruleengine/full/executor.go` | `:852-895` `fetchNode`/`readNode`, `:1147-1190` `applyWith`, `:1208-1330` `projectItems`, `:1619` `walkExprAll` | factor `decodeNode(key, entry)`; add `prefetchNodes(keys)` (chunk ≤ 1,024, `GetMultiNoSnapshot`, absent ⇒ nil memo) and `prefetchAspects(bindings, exprs)`; call before the item loops and the WITH WHERE |
| 2 | `internal/refractor/ruleengine/full/rel_traverse.go` | `:58-100` the per-edge `fetchNode` loop | collect the frontier node's unmemoized other-end keys, `prefetchNodes`, then the existing loop |
| 2 | `internal/refractor/ruleengine/full/values.go` | `:23-80` `resolveProperty` | unchanged — the aspect-key shape (`nr.key + "." + key`, link key for a rel binding) is what the prefetch mirrors |
| 2b | `internal/refractor/adjacency/store.go`, `overflow.go` | `:52-145` `Neighbors`/`NeighborsScoped`, `:148-170` `readNodeState` | add the chunked multi-node state read (unmarked ⇒ whole answer, marked ⇒ marked/no edges, observer parity) |
| 2b | `internal/refractor/ruleengine/full/executor.go` | `:534-600` `applyMatch`, `:930-975` `fetchEdges`/`memoizeWhole` | stage per-source adjacency answers; promote via `memoizeWhole` on first `fetchEdges`; collect sources per bound-first-node pattern and per pattern comprehension |
| 2 (review) | `internal/substrate/kv_multi_chunked.go` (new) | beside `kv_multi.go` `KVGetMultiNoSnapshot` | `ChunkedMultiGet(ctx, items, chunk, floor, read, visit)`: count-chunked, halves a failing request down to the floor, item = the unit a split may never tear (a node's doc + mark); both prefetchers use it |
| 2 (review) | `internal/refractor/adjacency/overflow.go`, `internal/refractor/subjects/subjects.go` | `readNodeState`, `validateToken` | one `decodeNodeState` behind the per-node read and the batch; `ValidToken` exported so the token rule has one definition |
| 3 | `internal/substrate/publish_pipeline.go` (new) + `publish.go` | `:62-76` `Publish` | `PublishPipeline` (`NewPublishPipeline(window)`, `Add`, `Flush`) over `js.PublishMsgAsync` (nats.go v1.52.0 `jetstream/publish.go:274`) |
| 3 | `internal/refractor/adapter/natssubject.go` + `publishpipeline.go` (new) | `:424-434` `publish` | ctx-carried pipeline: `WithPublishPipeline(ctx, p)` / `publishPipelineFrom(ctx)`, `PublishPipelineOpener`; sync path when none |
| 3 | `internal/refractor/pipeline/results.go` | `:34-250` `writeResults`, `:443-460` `writeAudit` | open row batch + audit batch before the loop; flush the row batch before returning the decision the frame emission reads; audit flush logged |
| 3 | `internal/refractor/pipeline/hydrate.go`, `reproject_personal.go` | `:80-125`, `:143-225` | same batch around the write loop, flushed before `PublishKeySet` |
| 3 | `internal/refractor/health/audit_writer.go` | `:138-156` `WriteAudit` | `WriteAuditPipelined(ctx, pipe, ...)` sibling (`WriteAudit` delegates with nil); own pipeline, never the row pipeline |
| docs | `docs/components/refractor.md` | personal-lens transport rows (`:72-135`), rule-engine section (`:563+`) | the three mechanisms, one line each |

### 13.3 Precedents to mirror

- Multi-get of exact keys with revisions: `adjacency/overflow.go:148-170` (`readNodeState`); the
  `NoSnapshot` posture argument: `substrate/kv_multi.go:212-240`.
- ctx-carried per-call value with an unexported key: `adjacency/readobserver.go:1-40`; the pipeline's
  `isPlainDerivedAnchorReentry` (`anchor_derivation_plain.go`).
- Memo-with-nil-for-absent + footprint from memo: `full/executor.go:852-868, 500-530`.
- Fail-closed key readers with natsfixture tests: `capabilityread_test.go:47-190` (`TestIsReadable_*`).
- Adapter optional-capability shapes: `adapter/adapter.go:139-160` (`HydrationMarkerPublisher`, `KeySetPublisher`).
- The posture gate's table-driven rows + self-tests: `scripts/lint-conventions.go:3131-3150, 3686-3720`.
- Greenfield, with reason: `PublishBatch` — the substrate has no async publish today (G11); it wraps the
  pinned client's own future API rather than inventing a queue.

### 13.4 Increment order + green checks

1. **Inc 1** — `go test ./internal/refractor/capabilityread/ ./internal/refractor/personalinterest/ ./internal/refractor/projection/ ./internal/refractor/pipeline/ -count=1 -p 4` and `STRICT=1 go run ./scripts/lint-conventions.go` (the two new gate rows self-tested).
2. **Inc 2** — `go test ./internal/refractor/ruleengine/full/ ./internal/refractor/pipeline/ -count=1 -p 4`; the footprint-equality pin and the point-read-counter revert-proof both green; `go test ./internal/refractor/ -run 'Corpus' -count=1` (no per-lens verdict moves).
3. **Inc 3** — `go test ./internal/substrate/ ./internal/refractor/adapter/ ./internal/refractor/health/ ./internal/refractor/pipeline/ -count=1 -p 4`; the personal e2e set `go test ./internal/refractor/ -run 'PersonalLens|EdgeManifest' -count=1`.
4. **Whole fire** — `go build ./... && make vet && golangci-lint run ./... && make verify-kernel`; every `scripts/lint-*.go`; `go test ./... -p 4` (a wide default changed: the executor's read path); build-tagged harnesses reachable by the touched interfaces — none add methods, but run `make test-control-plane-authz` and the convergence tags since `pipeline` changed.
5. **Live** — from the main checkout `make cycle-refractor`; re-run C1 after ≥ 20 pattern-bound events; consumer info on `refractor-ZdEvei26RWsXY16mZdEv`.

### 13.5 In-scope gotchas

- **The scope is security-plane state with a lifetime of one evaluation** — never stash it on the pipeline,
  the envelope closure, or a package var; the equivalence property test is the acceptance for Inc 1.
- **A gate edit beside a widening must be no weaker**: the two new symbols join the lint table with the
  identical default-deny; the fallback call sites keep their annotations.
- **Chunk at ≤ 1,024 exact keys** so no prefetch ever leaves the multi-get fast path (G9); a wildcard never
  enters the executor's prefetch (exact keys only) — the grant-set filters are the only wildcards, and they
  are deliberately `NoSnapshot`.
- **Absent ⇒ nil memo, revision 0**: prefetch must record the same absence a point read would, or the
  footprint changes shape and `footprintValid` re-executes forever on auth-plane lenses.
- **Flush before the frame, and never share the audit batch with the row batch.**
- **`context.Background()` in the envelope** (`personal.go:200, 221`) is pre-existing; the scope fn takes the
  evaluation ctx — do not widen the fix to the fallback reads in this fire.
- **`packages/` untouched** ⇒ no version bump; `internal/*` only.
- **Standing checklist** (`agents/fire-brief-template.md`): (1) lifetime table — §5; (2) every census a
  premise — §3 re-run at build; (3) revert-proof every increment in the builder's own worktree — the
  point-read counter, the scope-disagreement tests, the flush-failure Nak; (4) removal — nothing is removed,
  the per-row paths remain as fallbacks; (5) one key one writer — no new keys; (6) precedent may carry
  debt — `readNodeState` uses the verified `GetMulti` on two keys, which is right there and wrong for
  thousands (G9); do not copy the variant, copy the shape.
- **Touched component dossier — `docs/components/refractor.md` "Review keeps catching"**, copied verbatim
  in §13.8 below.

### 13.6 Adjacent finds

- **Delta publication for a personal lens** (§1.3) — filed now as a designer row (§12); the wire harm is
  measured, the pattern is genuinely absent.
- **The envelope's fallback reads ignore the evaluation ctx** (`context.Background()`) — pre-existing, one
  line each; absorbed into Inc 1 only if the builder's diff already touches those lines; otherwise left,
  because the scope path makes them cold.
- `leaseApplicationComplete` 152 k at 0 msg/s (parent §15.8) — Surveyor territory, unchanged.

### 13.7 Non-goals (drift fence)

§4.4 verbatim: no delta publication; no change to `IsReadable`/`IsRelevant` semantics or D1 key shapes or the
frame model; `KV_core-kv` adapters stay synchronous; `edgeManifestReadGrants` and `leaseApplicationComplete`
untouched. **Scope-diff gate, run:** every §13.2 row traces to §4's three increments; the row's own
prescription (sub-actor reprojection) is *narrowed out* with its harm re-filed, never substituted for by an
adjacent mechanism; declared dependencies — the multi-get primitive and nats.go's async publish — verified
both ways (G9, G11); no unlisted dependency is load-bearing for the green bar.

### 13.8 Refractor dossier (copied verbatim from `docs/components/refractor.md`)

#### Review keeps catching (dossier)

The component's recurring review-finding classes — fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`), the item-close review appends new ones (`agents/steward/SKILL.md` §4).
**Capped at 12 one-liners**; an entry RETIRES when a lint/test gate mechanizes it (name the gate, strike
the entry).

Retired so far — the gate is the record, so the prose is gone: *a projection read as a decision input by
another projection needs its own change edge* (`scripts/lint-conventions.go`'s blocking `IsReadable(` gate,
which default-denies a read call site carrying no `grant-change-posture` annotation), *site censuses derived from key
shapes undercount* (`label_derivation_corpus_census_test.go`, `grouping_reduction_corpus_census_test.go`),
*turning on a behaviour an existing predicate gated hands it the complement*
(`TestCorpusAnchorHopIndex_CompleteIndexHoldsEveryReferencedRelation`), *a label narrows the binder, not
necessarily the consumer filter* (`label_derivation_corpus_census_test.go`'s per-lens
`(labels, exhaustive, filterMode)` pin), *a new health `Entry` field ships with no carry-forward line, so the
next status transition silently zeroes it* (`health/entry_carry_forward_completeness_test.go` — reflects over
`Entry`, drives all three wholesale writers, and fails by field AND writer name unless the field is carried
forward or allow-listed as writer-owned with a reason), *a hand-maintained struct round trip's omitted field is fail-OPEN because the zero value is the admitting answer* (`TestRuleState_RoundTripCarriesEveryField`, which discovers the field universe from `rulestate.go` at test time and fails by field name unless each field is read into the snapshot and written back through the same `Pipeline` field).

**Standing rule, not a finding class:** a new per-lens analysis **ships its corpus census in the same
fire**, reusing `forEachCorpusCypher` rather than sweeping its own way — enumerate every parseable corpus
rule body through the *real* analysis (never a grep of cypher text, and never a reimplementation of the
predicate, which would agree with a broken gate), pin the per-lens verdict, and assert the population is
exactly these names with a floor on the count so an empty enumeration cannot read as a table of unchanged
rows. **The same rule binds a design's soundness argument:** a claim of the form *"the corpus has N / none
of shape X"* cites the executable pin that holds it (or ships one) — never a count read off a grep. Seen
twice (the hub-walk fire's benefit claim on a hub whose link shape was never censused; the hub-read-scope
fire's "two untyped-hop lenses" when the pinned census held three across two tables), so the count is the
wrong kind of claim: state the mechanism-level invariant and point at the pin.

- **A removal verdict's premises are the whole mechanism — check the PROBED ARTIFACT, not the precedent's
  shape.** Two ways this fire nearly shipped a reconciler that deleted live state. (a) **A probe artifact
  its own owner deletes-then-recreates is transiently absent for a perfectly live subject.** The Edge
  reconciler mirrored `DurableJanitor` structurally and inherited its single-read verdict — but
  `lensIsGone` reads a `vtx.meta.<id>` that is *never* transiently absent, while every Edge attach opens
  with an unconditional `DeleteStreamConsumer` (JetStream refuses a changed `DeliverPolicy`/`OptStartSeq`),
  so `ErrConsumerNotFound` is true for one RTT on every connect. The grace that would have covered it was
  anchored to a `registeredAt` nothing refreshed. A single-read verdict is sound ONLY over an artifact that
  is never transiently absent; establish that property before copying the shape. (b) **A verdict scoped to
  one dimension, over a store keyed without it, must fail closed when that dimension goes ambiguous — and
  must never be more permissive than the INSPECTOR rendering the same data.** One global
  `personal-lens-interest` bucket, one reconciler per SYNC stream: two streams and each deletes the other's
  devices wholesale. `cmd/loupe` already refuses to render a fleet verdict on that exact ambiguity; the
  deleter proceeded. Minted: edge-sync-orphan-expiry, (a) and (b) found independently by two cold reviewers.
  Check: `TestInterestReconciler_*` two-strike table + `TestSyncStreamWitness_ObserveAndAmbiguity`.
  (Displaced *"a meta sweep multiplies `Rebuild`"*, retired per this dossier's own rule — fully mechanized
  by `TestTaxonomyChanged_FanOutStaysWithinTheConcurrencyBound`,
  `TestRebuildGate_TaxonomyAndControlPathsShareOneBound`, `TestRebuild_HoldsUntilTheConsumerPumpHasReopened`
  and `TestSupervisor_ResetAwaitReopen_{ReturnsOnlyAfterThePumpReopens,OverlappingWaitersAreBothReleased}`,
  which are now the record.)
- **A soundness claim's stated REASON is load-bearing, and a reason measurement can falsify is worse than
  none** — §4.4 justified "evaluate, don't render" by "a shrunken footprint turns a match into a spurious
  drift retry." Backwards: `footprintValid` re-reads only what the footprint NAMES, so a smaller footprint
  validates fewer keys and silently PASSES — lost drift detection, fail-open. The constraint was right and the
  argument for it was refutable by anyone who measured retries, which is how a correct guardrail gets deleted
  by a later fire (§9.6 defers a generalization whose reads do reach Core KV). Minted: grouping-key close pass,
  found by the capability-plane reviewer, not the author. Check: for any "don't do X or Y breaks" constraint,
  read Y's consumer and state which DIRECTION the failure runs; if removing X makes a check pass more readily
  rather than fail, say so. **Second sighting, and it is the mirror image: refuting a refusal's REASON does
  not establish that the whole refusal was wrong.** `AnchorHopIndex` refused every variable-length hop
  because "the intermediate nodes cannot be stepped hop-by-hop" — refutable, and refuted, by the engine's own
  `traverseRel`. But the shape had a *real* boundary nobody had derived: `AnchorSideSeeds` seeds the changed
  link's two endpoints, which is exact only while that link binds its pattern positions, and across a ranged
  hop the changed link is an intermediate edge, so a lower bound above two drops anchors (thirteen graphs,
  found by a cold reviewer's sweep, not by the design). The refuted reason had been standing in for a
  correct one. Check: when you lift a refusal, do not stop at falsifying its stated reason — re-derive the
  boundary from the CONSUMERS the refusal was protecting, and expect the true limit to sit somewhere inside
  the old one. Corollary from the same fire: a refuted reason lives in more documents than the one you are
  building, so grep it — this one was normative text in three sibling designs, one of them the parent.
  **Third sighting (expiry-as-a-recorded-fact, 2026-09-02): a lifted refusal reveals the conjunct behind it,
  and a GRANTED licence logs nothing.** The design's "`$now` is the last conjunct refusing
  `leaseApplicationsRead`" was true of the log line and false of the licence: once the audit enrolled and
  reached a verdict the licence refused at `ProjectsOneRowPerAnchor` — a shape fact no clock edit could
  move — and the only evidence either way was a refusal line whose absence proves nothing. Check: a payoff
  claimed as "refusal X gone" is proved by the licence's POSITIVE verdict (enrolment log / audit verdict /
  a tally that acts), read live after the fix; and a design lifting conjunct N reads conjuncts N+1..end
  against the lens before it promises the payoff.
- **An expansion sigil is fail-CLOSED in a positive pattern and fail-OPEN in a negated one** — constraining
  the binder inside `NOT (...)` removes exclusions, i.e. grants. A `*` label on an auth lens's exclusion walk
  turns a partial taxonomy expansion into an over-grant, and the two arms of the same lens then fail in
  opposite directions. Minted: dynamic-type-taxonomy B1 (`capabilityServiceAccess`'s `exLoc`, which mints
  `cap.svc.<actor>`; reproduced as a failing test before removal). **Second sighting: the RANGE BOUND, one
  level up from the label** — once the pattern graph steps a bounded ranged hop, "bound your `*0..` to gain
  indexing" is an attractive package edit that is fail-closed on a positive arm (a too-shallow bound drops a
  service) and fail-OPEN on a negated one (it drops an exclusion, granting access). Same edit, opposite
  directions. Check: that half is **MECHANIZED** — `scripts/lint-lens-anchors.go` refuses a finite upper
  bound below the engine's own `maxVarLengthHops` clamp inside a negated extent, and runs its own
  positive-and-negative vectors on every invocation because the corpus ships no violating lens for it to
  catch. The **sigil** half still has only the per-lens string pin (`service-location/package_test.go`) — the
  entry retires when that one is mechanized too. Generalize before writing either: ask which direction the
  edit fails in on each arm, not whether it is "tighter".
- **A two-layer seam can be green at each layer and broken across it — the interposed step is where it dies**
  — a restored structural pause's diagnosis was stashed by the health sink and read back at the announcement,
  and both halves had passing tests: the substrate side drove `Load → probe → announce`, the Refractor side
  drove `Load → SetActive → Record`. Neither included the step that actually runs between them —
  `runPump`'s `InitialPause` re-seed calling `SetPaused(infra, "")`, which discarded the stash — so the
  operator got a self-heal with no cause on the single likeliest recovery path. The substrate test even
  *pinned that step* in its own lifecycle assertion without either side recognising it as the eraser. Minted:
  structural-pause Inc 2, found independently by two cold reviewers and by neither layer's author. Check: for
  any value handed across a component boundary, write the seam test with the **real** intervening sequence —
  enumerate what the other side does between the write and the read, and interpose it. Pinned by
  `TestHealthSink_RestoredStructuralCauseSurvivesTheReseededInfraGate`. (Displaced *"lens lag is not
  read-model incompleteness"*, which the capability-projection-reconciliation design still carries.)
- **An upsert-only reprojection retracts nothing whose key drops out** — on the security plane that is an
  over-grant. Minted: negative/retraction design pass (designer SKILL §2). Check: none yet (the retraction
  primitive is its own backlog item).
- **A fail-closed posture proved on the DELIVERY axis is not proved on the PROJECTION axis** — "unresolvable ⇒
  widen the filter" reads as safe and is, for delivery; the same unresolved answer also published an empty
  matcher, so the lens went to zero rows and a retracting lens to a mass Delete. Minted: dynamic-type-taxonomy
  close pass (one class, three findings). Check: for each uncertain state, name every consumer the value feeds
  and state the fail direction of each — a broad filter compensates only the consumers downstream of delivery.
- **One latch guarding two states that commit at different times** — a change-detection baseline written after
  an async rebuild while the gate it describes is published before it, so an A→B→A sequence takes the
  "unchanged" fast path against a baseline that never matched the gate. Minted: dynamic-type-taxonomy inc 4
  (found at the item's close, not at the increment's). Check: for any "has this changed?" comparison, prove the
  compared baseline and the acted-on state share a commit point.
- **An index whose entries are read from one place and gated from another must agree about absence** — the
  adjacency overflow latch cached "this node is marked" in process memory while the reader answered from KV,
  so a bucket wiped under a live process (the engine survives a NATS outage rather than restarting with it)
  left the writer no-oping every rebuild and the reader returning an EMPTY edge set as authoritative, with no
  error and no log line. Minted: adjacency Shape B close review (the state table had named the boundary and
  answered it with an environmental assertion). Check: a cache of durable state is consulted for PRESENCE
  only, never to conclude absence — or it is deleted, which is what shipped. **Second sighting, in memory
  rather than across a cache boundary: a present-but-EMPTY set and a missing one are the same answer, and two
  readers disagreed about that.** `HopIndex.Expanded` is consulted by `admitsType` per edge (an empty set
  admits no type, which PRUNES) and gated once per rule state by `UnresolvedExpansionPosition` (which tested
  `== nil`, so an empty set read as *resolved*). A `*` label resolving to nothing is a real, warned-about
  state, so the derivation accepted the index, built zero seeds, and returned an empty derived set with
  `ok == true` — read by the caller as "no anchor changes" on the lens that mints `cap.svc.<actor>`. Minted:
  varlength-anchor-derivation Inc 1, found by a cold reviewer; the design's own risk table had predicted it
  and the decomposition never turned that row into a task. Check — **MECHANIZED as a mandated test shape**:
  every absence gate over a resolved-set field asserts BOTH vectors, resolved and empty, against the same
  index (`TestAnchorHopIndex_EmptyExpansionIsUnresolved`), and the empty one is proven by reverting the
  predicate. Standing rule for the reader: `len(x) == 0`, not `x == nil`, wherever "no answer" and "the
  answer is nothing" must behave alike.
- **An authoring gate and its runtime resolver must agree, or the gate is advisory.** A parse-time refusal
  named the projectable surface of a relationship binding while `resolveProperty`'s arm resolved *whatever
  reached it*, so any shape the parse walk did not model served the value anyway: `WITH coalesce(r, r) AS rr`
  and `CASE … THEN r` both hand the binding on (the scope walk recognised only a bare variable item), and a
  `MATCH`'s **own** inline property map was never walked, so `MATCH (y {key: r.localName})` used a real
  link-envelope field as a vertex key. Minted: relationship-data-projection close pass (all three
  *executed* by a cold reviewer, not reasoned). Check: for any authoring-time refusal, name the runtime
  point the refused value would flow through and enforce the same predicate there too — one shared
  function, so relaxing it moves both. **The walk-completeness half is now MECHANIZED —
  `full/variable_refs_completeness_test.go`**: it discovers every `Expr` implementation and every
  expression-bearing field from the package source at test time and probes each position, so a new type *or a
  new field* fails until the walk handles it. Built after the same property-map blind spot reappeared in
  `collectPatternVariableRefs` (independent-branch decomposition close pass), where it made a fail-closed
  premise return `unknown=false` on a short reference set and a grant list gained an entry. A type-level
  `default:` arm cannot catch an unwalked FIELD on a type the walk already recognises — that is where both
  bugs lived. A scope walk that enumerates the shapes that CARRY a binding is
  fail-open by construction; enumerate the shapes that provably do not, and assume the rest carry it.
- **A fixture that establishes the favourable ORDER or ARM is an argument, not a test.** Minted on the
  ARM: the design's self-declared most-important test — a data-only link update must move the projected
  row — was written on a fixture whose pipeline installs no actor enumerator, so it exercised
  `evalPlainLinkReprojection` while the feature's only consumer declares `actorAggregate` and runs
  `evalLinkFanOut`; the mechanism was proved on an arm nothing ships (relationship-data-projection close
  pass, found independently by two reviewers). **Second sighting, on ORDER:**
  `TestPersonalSweep_RunSweepsImmediately` registered the lens before starting `Run`, while `cmd/refractor`
  starts `Run` before any lens activates — so `Sweep`'s empty-registry early return recorded no verdict,
  the first one waited a whole 60 s interval with every personal lens on the relation-blind enumerator, and
  an immediate pass that never ran was green (personal-lens licence close pass). **Third, on the BARRIER —
  a consumer that has SETTLED has not necessarily finished its handler:** `NumPending == 0` drops when a
  delivery is prefetched into the client buffer, not when the write it causes has landed, so a
  purge-then-observe test races an in-flight reprojection and the row reappears on its own (auth-plane 4c,
  surfaced at `-count=3`, not at `-count=1`). Check: read the consumer lens's `ProjectionKind` and assert
  the fixture takes the same branch of `handle`'s `KindLink` case; write the test in `cmd/`'s actual
  startup sequence, or assert the ordering in `cmd/` directly; and barrier on the EFFECT — poll until the
  row's own revision advances past the last pre-purge write — never on pending alone. The generalization:
  before asserting, name what the fixture ARRANGED that production does not, and arrange the other way.
- **A zero or empty reading that cannot be distinguished from "not measured" must read UNREADABLE, and a
  census owes a reached-ness counter.** Minted: personal-lens licence, four sightings in one item — an
  empty `health.refractor.*` listing would have licensed a two-instance deployment (a live Refractor that
  finds no Refractor has contradicted itself); an empty `HopIndex.Incomplete` swallowed by a latch whose
  zero value is `""`, so the operator log printed a blank reason; `staticRefusalSet`, which exists only to
  separate "no reason reported yet" from a reported empty one; and `InterestReconcilersConstructed()`,
  where zero constructed reconcilers and zero *reached* would have read the same. Check: for every
  count/verdict a consumer refuses on, ask what it reads when the MEASUREMENT is broken; if that equals
  the empty-subject reading, return unreadable rather than the number, and expose a reached-ness counter
  so a census can tell "nothing matched" from "nothing ran".
  `TestPersonalSweepVerdict_VocabularyIsClosed` pins the vocabulary half (a verdict summary is
  default-denied, so a new state cannot land as an unnamed empty string).

- **An operator-driven repair promoted to an AUTOMATIC one carries every tolerance the operator path had, and each must be re-derived.** Minted: lens-output-reactivation (2026-09-03), where an Output edit began re-activating the lens with the rebuild's purge ahead of the replay. Three tolerances rode in, all found by the cold reviewer, none by the brief: (a) a listing SCOPE is not an ownership set — `KeyPrefix`'s own doc says a prefix admits siblings (`cap.` contains `cap.roles.`), and `truncateKeys` purged the listing whole, so a `bodyColumns` edit to the kernel `capability` lens would have wiped four sibling producers' rows; (b) a healer's clear keyed to nothing clears every writer's latch — the clean-registration `ClearLastError` erased the purge-failure diagnosis seconds after it was raised; (c) a flag consulted on the wrong side of a force rule guards nothing — `requested` gated the purge while `resolveTruncate` forces one for any guarded adapter, so a protected Postgres table was still purgeable. Check: ownership is exact (`OutputDescriptor.OwnsKey`, bound by `ApplyTruncateScope`, pinned by `TestTruncateScope_KernelCapabilityLensPurgesOnlyTheKeysItsOwnInverseClaims`), a clear names what it owns (`Reporter.ClearLastErrorIf`), and a refusal is by construction (`reactivationPreflight`) — and a design that automates a repair lists the operator path's tolerances (scope, clears, teardown result, target family) as premises to falsify.
