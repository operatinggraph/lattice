# Refractor

**Component reference** | Audience: implementers + architects

---

## Overview

Refractor projects Core KV state into derived KV buckets and Postgres tables
via continuously-running Lens definitions. Lenses are openCypher queries (full
engine) that read from Core KV + Adjacency KV and write to per-lens target
adapters. This is the **read side** of Lattice: Processor writes to Core KV,
lenses derive queryable projections. Refractor does not write to Core KV — it
produces Capability KV, per-lens target buckets, Postgres rows, Health KV
signals, and audit/metrics subjects.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/refractor/` | All projection engine sub-packages (17 packages) |
| `cmd/refractor/` | Binary entry point; wires engine, consumer, pipeline, adapter, control, health |

Key sub-packages:

| Sub-package | Role |
|-------------|------|
| `pipeline/` | `Pipeline` struct; drives per-lens CDC-event → evaluate → adapt loop; `LatencyRingBuffer` (128-sample window) |
| `lens/` | `CoreKVSource` (durable consumer over `vtx.meta.>` and `lnk.meta.*.subtypeOf.>`, routes `meta.lens` class to the lens loader and accumulates the dynamic type taxonomy from vertexType/canonicalName/subtypeOf events — dynamic-type-taxonomy-design.md §6.1); `Rule` type; `translateSpec` from `LensSpec` to `Rule`; engine selection via registry |
| `adapter/` | `Adapter` interface; `nats_kv` adapter; Postgres adapter; `nats_subject` adapter (Personal Lens transport); `PoolManager` for Postgres connection pooling |
| `adjacency/` | Adjacency KV read/write: `Build` (CAS upsert/remove, with the per-node overflow latch), `Neighbors` (the document read, or the Core-KV fallback read for an overflow-marked node), `NeighborsScoped` / `NeighborsByRelation` (the same read narrowed to named relations, and the flag saying whether the node answered whole), `EventsForLink` (the directional-event-pair constructor every link consumer shares) |
| `consumer/` | `Bootstrapper` (builds the adjacency index from link CDC events). Per-lens durable JetStream consumers are owned by each `pipeline.Pipeline` via `substrate.ConsumerSupervisor` (see Lens lifecycle step 5). |
| `control/` | `Service` — control plane on the NATS `micro.Service` framework; endpoints at `lattice.ctrl.refractor.<lensId>.<op>` |
| `health/` | `LatticeHeartbeater`; `Reporter`; `AuditWriter` (subjects `lattice.refractor.audit.<lensId>`); `LagPoller` (subjects `lattice.refractor.metrics.<lensId>`) |
| `ruleengine/` | Registry + engine interface; `full/` (openCypher via ANTLR4) + `full/cypher/` (generated lexer/parser) |
| `failure/` | Failure-tier classification; retry / DLQ routing |
| `subjects/` | Centralizes all subject name construction (`lattice.refractor.*`, `lattice.ctrl.refractor.*`) |
| `config/` | Configuration types |
| `capabilityenv/` | Wraps executor RETURN rows into Contract #6 §6.2 Capability KV envelopes |
| `projection/` | Actor-aggregate projection-plan compiler (plan-as-data: Execution + Output, `EnvelopeFn`/`BuildKey`, §6.3 freshness, personal-lens install) — the Epic-12 capability projection driver |
| `capabilityread/` | Reads D1's `cap-read.*` read-path Capability KV to answer "may this actor read this anchor?" — the Personal Lens's fail-closed read-grant gate (§6.14, Fire PL.3) |
| `personalinterest/` | Per-device Interest Set in the `personal-lens-interest` bucket — a bandwidth relevance filter, never a security control (Fire PL.2) |
| `keyshredded/` | Durable `events.privacy.keyShredded` listener that nullifies a shredded identity's projected rows (brainstorm #62 — the one sanctioned event-stream listener in Refractor's charter); its guarded-key nullifications stamp `projectionSeq = MaxInt64`, Contract #6 §6.2's terminal always-wins token. Records `RecordShredFinalization{projectionsNullified}` under the `identity.system.privacy` service actor, declaring the `piiKey` aspect **and that actor's own vertex** in `ContextHint.Reads` — the script reads the latter to refuse an attestation written by any other actor |

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Core KV CDC events** | Durable JetStream consumer (`substrate.SubscribeKVChanges`) on the `KV_core-kv` backing stream | Both the all-mutations stream and the lens-def/taxonomy watch (`vtx.meta.>` + `lnk.meta.*.subtypeOf.>` on one consumer, dynamic-type-taxonomy-design.md §6.1) run on the same durable-consumer pattern; ack position persists across restarts so a restarted Refractor resumes rather than replaying from the start. |
| **Lens meta-vertices** | Core KV `vtx.meta.<NanoID>` with `class: meta.lens` and a `.spec` aspect | The `spec` aspect carries: `id`, `canonicalName`, `targetType`, `targetConfig`, `cypherRule`, `outputSchema`, `engine` (optional). `engine` must be `"full"` (or absent → full); any other value fails lens validation. |
| **Adjacency KV** | `refractor-adjacency` bucket | Refractor's internal per-node edge index, built by `consumer/bootstrap.go` from every `lnk.*` CDC event; one document per node (`adj.<nodeId>`) holding every edge that touches it, one entry per direction. EdgeID == link key. A node whose edge count or document size crosses a threshold latches and is read via a direct Core KV enumeration instead of its document — see "Refractor adjacency KV" below. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Capability KV** (Contract #6 §6.2) | `capability-kv` bucket | Two disjoint producers. The bootstrap-seeded Capability Lens writes the anchor `cap.<actorType>.<id>` (e.g. `cap.identity.<actorId>`), derived by stripping the `vtx.` prefix from the actor vertex key, for identities holding the primordial `operator` role; rbac-domain's `capabilityRoles` lens writes `cap.roles.<actorType>.<id>` for every actor from its role/permission topology. Neither is a subset of the other. Consumed by Processor's step-3 `CapabilityAuthorizer`, which reads the anchor only for an actor in the graph-derived system-actor set and `cap.roles.*` alone for everyone else (Contract #6 §6.1). |
| **Per-lens target KV buckets** | Bucket name per `LensSpec.targetConfig.bucket` | e.g. `duplicate-candidates` (the identity-hygiene package's Duplicate Candidates Lens). Created on demand if not pre-provisioned. |
| **Postgres rows** | Target table per `LensSpec.targetConfig.table` | For SQL-target lenses. The adapter is **thin**: it upserts one column **per RETURN field** (`INSERT … (book_id, title) … ON CONFLICT DO UPDATE`) and issues no DDL. The target table is **provisioned out-of-band** (a migration), with columns matching the lens RETURN (key columns + projected fields). Delete projection is **mode-dependent** (`targetConfig.deleteMode`, default `hard`): the default hard delete issues `DELETE FROM` and needs only the key + projected columns; `deleteMode: soft` issues `UPDATE … SET is_deleted=true, deleted_at=NOW()` and **requires** the `is_deleted` / `deleted_at` columns. The Refractor does not create or alter the table. |
| **Health KV signals** (Contract #5) | Health KV `health.refractor.<instance>.lens.<canonicalName>` | Per-lens latency snapshots (p95, p99, mean, count from `LatencyRingBuffer`); consumer lag; per-instance heartbeat every 10s. |
| **Audit subjects** | `lattice.refractor.audit.<lensId>` | One `AuditEntry` per **committed** projection — a row that actually landed in the target along the CDC write path (the pipeline's write step, plus the retry queue that finishes what it could not). A write the ordering guard declined and one dropped for want of an ordering token each append nothing, as does a NATS-KV row an unguarded write skips for being byte-identical to what is stored (a read-before-write only that adapter does — unguarded Postgres takes `ON CONFLICT DO UPDATE` and reports a row changed whatever its value); reconciliation accounts for its heals as health verdicts, not here. Every lens's audit subject lands on the single consolidated `REFRACTOR_AUDIT` JetStream stream (subject filter `lattice.refractor.audit.>`, 7-day MaxAge, 512 MiB MaxBytes) — one stream for the whole deployment, not one per lens. A write step's entries are pipelined through their own publish pipeline, flushed on the way out and separate from the one carrying the rows: a lost audit entry is logged and forgiven, never a reason to redeliver. |
| **Metrics subjects** | `lattice.refractor.metrics.<lensId>` | Consumer lag on `LagPoller` interval. |
| **Control plane** | `micro.Service` endpoints at `lattice.ctrl.refractor.<lensId>.<op>` | Handles JSON control requests (`health`, `validate`, `rebuild`, `pause`, `resume`, `delete`, `register`, `deregister`, `hydrate`, `sessionkey`, `syncgap`) via the NATS Services framework; capability-gated (FR30). The five identity-bound Personal-Lens ops (`register`/`deregister`/`hydrate`/`sessionkey`/`syncgap`) additionally bind `identityId` to the verified actor server-side. |
| **Personal Lens delta envelopes** | Per-recipient NATS subject `<targetConfig.subjectPrefix>.<actor>` (e.g. `lattice.sync.user.<identityId>`) on the backing JetStream stream `targetConfig.stream` | Produced by a `targetType: "nats_subject"` lens (`adapter/natssubject.go`). See below. |

### Personal Lens transport (`nats_subject` target)

The **Personal Lens** turns Refractor from a shared read-model warehouse into a per-identity
*filtered delta stream* — the cloud-side half of the Edge Lattice
(`personal-secure-lens-design.md`). The transport runs under a trusted-single-identity posture; a
cross-vertex fan-out + Interest Set sits on top, and D1's read-path Capability KV is the correctness
boundary (below). The recipient is either RETURNed directly by the lens's own cypher (the **PL.1
shape**) or injected by the fan-out envelope (the **PL.2 shape**) — never both.

- **`TargetNATSSubjectConfig`** (`lens/corekv_source.go`): `{ "subjectPrefix": "lattice.sync.user",
  "stream": "SYNC", "personal": false, "key": ["__actor", ...businessKeys] }`. `key` must include
  `adapter.PersonalActorKeyField` (`"__actor"`) exactly once. When `personal` is absent/false
  (PL.1 shape), the lens's own RETURN aliases `__actor` to the recipient identity's key directly —
  no fan-out. When `personal: true` (PL.2 shape,
  `projection.IsPersonalLens`/`projection.InstallPersonalLens`), `__actor` is **not** a RETURN
  alias: the pipeline installs an `ActorEnumerator` (`actorType: "identity"`) and re-executes the
  cypher once per enumerated recipient with `$actorKey` bound, injecting that recipient into
  `keys["__actor"]` — `key` still declares only the lens's own **business** columns (identical to
  `IntoConfig.Key` minus `"__actor"`); a personal cypher's neighbor-anchor column must always alias
  to `anchor` (a $actorKey-scoped traversal that matches no neighbor yields one degenerate
  all-null row, recognized and skipped by an empty `anchor`, same as the actor-aggregate
  envelope's realness check).
- **Subject resolution.** The adapter is driven per row, not per bucket: `keys["__actor"]`
  resolves the delivery subject (`subjects.PersonalSync(subjectPrefix, actor)` →
  `<subjectPrefix>.<actor>`); the remaining key fields build the envelope's `key` (mirrors
  `NatsKVAdapter.buildKey`). A non-string or subject-unsafe `__actor` value fails the write with an
  error rather than reaching a panic — the value is untrusted, cypher-projected business data.
- **Delta envelope** (`{op, key, anchor, kind, class, revision, projectionSeq, encrypted, data, lens}`):
  `op` is `"upsert"`, `"delete"`, `"keyset"` (Retraction Fire R1, below), or the Fire PL.4 terminal
  `"hydrationComplete"` marker (key/data omitted); `anchor`/`kind`/`class` are optional envelope
  metadata a lens's RETURN clause supplies as reserved row-column names (promoted out of `data`, so
  they never appear twice); `data` is the remaining projected row (nil/omitted for a delete or an
  all-metadata row). `encrypted` is always `false` through PL.2 — Vault ciphertext passthrough is
  Fire 5. `lens` (an `"upsert"`/`"keyset"` field) is the producing lens's rule ID, stamped from the
  adapter's construction-time `ruleID` — the attribution a same-key multi-lens overlap needs (below).
- **Keyset frame (Retraction Fire R1, `personal-lens-retraction-design.md`).** Personal Lens rows
  never retracted before this fire, live or on cold hydrate — a row that stopped matching a lens's
  cypher just lingered on the device forever. R1 closes the gap from the server side: after every
  successful evaluate+write, the pipeline publishes one **additional** `{op: "keyset", lens,
  keys: [...], revision, projectionSeq}` frame per enumerated actor, naming that lens's **complete,
  authoritative** business-key set for the actor as of `revision` — an actor whose evaluation
  surfaced zero surviving rows (D1 deny, Interest Set miss, a missing/tombstoned actor) gets an
  **empty** frame, which is the last-row-retraction signal. Frames are emitted only when the whole
  batch cleanly applied (no retry-enqueue, no terminal DLQ) and are additive on the wire — a client
  that doesn't understand `"keyset"` acks and ignores it (the existing unknown-`op` fallback), so R1
  ships with zero risk to a client that hasn't adopted the Edge-side diff (a later, separate fire).
  The identity-tombstone shortcut and `reprojectActors`' missing-actor branch (previously a
  cap-shaped `Delete` this adapter rejects — `"__actor" absent from keys"`, redelivered indefinitely
  since a personal pipeline configures no retry queue) now emit **no** result for a personal target;
  the caller's empty frame is what retracts instead, closing that redelivery-loop defect
  structurally. `Hydrate` (below) publishes its own keyset frame — at `highWater`, after its bulk
  upserts and before the terminal marker — so a cold reconnect prunes exactly like a live retraction.
  Emission is scoped to the `reprojectActors` code path (fan-out + Hydrate); a personal actor's own
  vertex mutating itself outside a fan-out (e.g. an identity property edit) re-evaluates directly and
  never emits a frame — it also never produces a `Delete` for a personal pipeline (filter/diff
  retraction is gated off for actor-aware lenses), so there is nothing that branch would need to
  retract. The Edge-side consumption half is in `internal/edge`: per-key lens `Sources` attribution and
  the `frameHW` resurrection guard in `store/bolt.go` (`ApplyUpsert` drops an unattributed delta below
  the lens's frame high-water; `ApplyKeySet` advances that high-water and prunes via `collectAttributed`),
  and the dead-lens prune in `sync/sync.go` (`PruneDeadLensAttributions`, run after a completed hydrate).
- **Publication scope (`personal-lens-delta-publication-design.md` §4).** A row is published iff the
  publisher's `pipeline.PublishScope` admits it; **every** surviving row is framed, so a row the scope
  withholds is one the device already holds and the frame is what keeps the client from pruning it.
  `ScopeAll` publishes everything — a `Hydrate`, an Interest Set change, the healer's daily content
  cycle, and the **zero value**, so a caller that sets no scope publishes the whole actor. `ScopeNone`
  publishes the frame alone — the standing healer's ordinary pass. `ScopeAnchors(A)` publishes the rows
  whose `anchor` alias names a NanoID in `A`, bounded at 64 anchors before it widens to `ScopeAll` — a
  D1 grant change, which moves the inclusion of exactly the rows anchored at the anchor whose grant
  flipped and nothing else's content. `ScopeVertices(V)` publishes the rows whose **provenance** — the
  vertex keys their evaluation read, recorded per row by the engine — meets `V`, the vertices one CDC
  event touched: the event vertex, an aspect's parent vertex, a link's two endpoints, or the actor's
  own key. A row that recorded no provenance is published, so a path recording none reproduces the
  whole-actor publication rather than silencing the device. Three properties of the compiled rule
  refuse the scope and publish the whole actor: a row reading `$now` or `$projectedAt` changes with no
  vertex changing; a pattern position carrying the `*` label sigil binds through a taxonomy closure no
  row's provenance names, so a type joining or leaving it would move rows the scope withheld; and a
  scan-seeded anchor puts its whole type on every row's provenance, which admits everything anyway —
  a degeneracy, decided once instead of per row. A personal lens declaring a retry queue is refused at
  install — a retried write replays at its original, lower ordering token, which a later frame makes
  the client drop. A frame published by a **signalled** reprojection or a hydrate counts as output and
  advances the lens's freshness clock; the frame a live CDC event publishes does not, and neither does
  the standing healer's frames-only pass — both go out whatever the rows did, so stamping either would
  turn that clock into a heartbeat and hide a lens withholding every row.
  A cold `Hydrate` publishes the whole actor at a high-water captured before it evaluates, so two
  guards keep a scoped live publish from racing ahead of it: the write loop publishes an actor whole
  while a hydrate holds that actor's publish slot, and a hydrate waits for the event already in flight
  to leave the handler before capturing.
  `ScopeSilent` publishes **nothing** — no row, no `Delete`, and, uniquely, no frame — and is what the
  CDC write loop scopes every event to while a personal lens's own **rebuild** is replaying (§4.5). A
  rebuild re-delivers every Core KV entry at its original revision, and every message that replay would
  send sits below the frame high-water a connected device already holds, so the device drops all of it;
  the messages are still acked, so the ordering token advances and the rescan drains normally. The flag
  is read once per event at the scope producer, so an event's rows and its frame are decided by one
  observation and a rebuild finishing mid-event cannot half-publish it. Business and auth-plane
  rebuilds are untouched — their replay is what repairs a stored read model.
  One consumer of the rebuild pays for that silence and is refused rather than served: a
  **retention-class key destruction** delivers the erasure by rebuilding every lens declaring the
  holder's type, and a personal lens's replay would upsert no null over anything. Such a target is
  enumerated, **labelled and refused loudly**, and takes the destruction's attestation with it — a
  rebuild that reported clean there would record a completed erasure over plaintext still held on the
  SYNC stream and on every device.
- **Stream provisioning.** The adapter JIT-provisions the backing stream via `substrate.EnsureStream`
  (mirrors the `nats_kv` case's JIT bucket creation) rather than a bootstrap pre-provision, and
  **unions** the lens's `subjectPrefix` wildcard into the stream's existing `Subjects` rather than
  overwriting them — the `SYNC` stream is meant to carry one platform-wide prefix, but this keeps a
  second lens sharing the same stream name safe regardless.
- **Composition census.** `make sync-census` (`scripts/sync-census.go`) is a read-only, live re-run of the `SYNC` stream's composition (stream-wide or per-subject via `-subject`) that reports `personal-lens-delta-publication-design.md` §10 "T7"'s acceptance numbers.
- **Guard posture: unguarded.** A subject publish is a fire-and-forget-shaped append (though the
  underlying JetStream publish is a confirmed round-trip, not a literal fire-and-forget); ordering
  is the stream's per-subject sequence, and the recipient dedups/reorders by envelope `revision`.
- **Pipelined publishes.** A write loop over a whole actor — the CDC write step, a cold `Hydrate`, a
  grant-change reprojection — publishes its rows through one `substrate.PublishPipeline` and awaits
  the store acks once at the end, instead of paying a round trip per row (a wide actor projects
  thousands). The pipeline rides the write loop's own context, so the adapter stays stateless and
  each concurrent caller owns its own; ordering on the wire is unchanged (the connection's send
  order), and the pipeline is always flushed **before** the keyset frame, which is what keeps the
  frame's "the rows I describe have applied" contract exact. A failed flush is a write failure of
  the whole loop: the frame is withheld and the message redelivers, because a publish pipeline
  carries no atomicity — unlike `Conn.PublishBatch`'s all-or-nothing batch it can leave earlier rows
  stored, and only redelivery over idempotent upserts restores the rest. The flush is the **only**
  place a pipelined publish failure is reported: an ack that resolves mid-loop belongs to an earlier
  row, so surfacing it there would charge one row's failure to another and dispose of the wrong one.
  Two bounds keep the mechanism honest — an unacknowledged publish resolves as a timeout rather than
  hanging a flush forever, and the connection-wide ceiling on outstanding acks is sized above
  `personal lenses × pipelines × window` so a wide write step cannot exhaust the budget every lens on
  the process shares. Concretely: the ceiling (`substrate.PublishAsyncMaxPending`, 8,192) is shared
  per connection: the 15 personal lenses' own CDC write steps reserve 3,840 of it (15 × 2 pipelines —
  row and audit — × the 128-wide default window), and the uncapped dimension is concurrent
  whole-actor `Hydrate`/reprojection calls, which open one pipeline apiece — about 34 of those
  running at once reach the ceiling, fail-closed (the stalled publish surfaces at that call's own
  `Flush`; a hydrate errors and the device re-attaches). Nothing is recorded until the flush returns
  clean: the audit entry claiming a row's hash and the lens's freshness clock both advance only for
  rows the flush stands behind.
- **Interest Set (Fire PL.2, `internal/refractor/personalinterest`).** A per-device relevance
  filter — a **bandwidth optimization, never a security control**: no registered device for a
  recipient (or a device that declares no `types`/`anchors`) admits everything; a declared filter
  admits a delta whose `kind` is in `types` or whose `anchor` is in `anchors`; any one of an
  identity's devices matching admits it (they share one subject). Stored in the Refractor-owned
  `personal-lens-interest` KV bucket (`bootstrap.PersonalLensInterestKV`), keyed
  `<identityId>.<deviceId>`, body `{types, anchors, registeredAt, revisionCursor}`. Managed by the
  control-plane RPCs `lattice.ctrl.refractor.personal.register` /
  `lattice.ctrl.refractor.personal.deregister` (`"personal"` is a fixed pseudo-lensId, not a real
  lens) — request body `{identityId, deviceId, types?, anchors?}`, response
  `{personalRegister: {registered: true}}` / `{personalDeregister: {deregistered: true}}`.
- **Interest Set change edge.** `IsRelevant` is read live at evaluation time, so a rewritten
  registration changes what a personal lens publishes with no Core-KV event on the lens's own
  subgraph. All **four** writers therefore announce, after their write lands: `personal.register`,
  `personal.deregister`, `health.InterestReconciler`'s orphan reap, and — less obviously —
  `personal.hydrate` when its revision-cursor write CREATES the registration
  (`personalinterest.SetRevisionCursor`'s `kv.Create` arm), since a row with no types and no anchors
  is what `IsRelevant` reads as admit-everything. Hydrating an already-registered device only
  updates a cursor, touches no filter, and announces nothing. Each takes a bare
  `func(identityID string)` wired by `cmd/refractor` — `control` imports `health`, so a shared sink
  type is unusable by both — and the closure enqueues onto the grant-change `Reprojector`'s existing
  coalescing dirty set, which already owns the bound, the drop accounting and the registry-ready
  hold. `nil` announces nothing, which is what a harness running no reprojector gets. The direction
  that matters is the NARROWING one: a device that stops asking for a type must stop receiving its
  keys, and the authoritative keyset frame is what prunes them. The reader side is gated — the
  `interest-change-posture` check in `scripts/lint-conventions.go` default-denies any
  `personalinterest.IsRelevant` call site that does not declare how it learns its answer changed,
  through the same symbol→annotation table that governs `capabilityread.IsReadable`.
- **D1 read-grant security gate (Fire PL.3, `internal/refractor/capabilityread`).** The
  correctness boundary: before publishing, `IsReadable(actorType, actorID, anchorID)` GETs the
  actor's base `cap-read.<actor>` slice plus every domain-specific `cap-read.<domain>.<actor>`
  slice (discovered via a wildcarded key-listing filter, since domain names are package-owned and
  not enumerable statically) and unions their `readableAnchors[]` (Contract #6 §6.14). Fail-closed
  throughout: no contributing slice, every slice soft-tombstoned (`isDeleted:true`, §6.8), or the
  anchor absent from all of them — deny. Runs in `personalEnvelopeFn` *before* the Interest Set
  relevance filter and wins over it (a device declaring an anchor relevant does not override a
  missing read grant). Threaded into `InstallPersonalLens` as `capKV`; `nil` disables the gate — a
  trusted/test-only posture, never a production default (`cmd/refractor/main.go` always opens a
  real `capability-kv` handle).
- **Per-evaluation gate scope (`personal-lens-whole-actor-cost-design.md` §4.1).** Both gates are
  answered from state read **once per actor evaluation**, not once per row: the pipeline's
  `EnvelopeScopeFn` — installed beside the envelope by `InstallPersonalLens`, run after the walk and
  only when the evaluation produced rows — reads the actor's whole readable-anchor set
  (`capabilityread.ReadableAnchors`) and the identity's registrations
  (`personalinterest.Registrations`), and the envelope answers each row with `AnchorSet.Admits` /
  `personalinterest.RelevantIn`: the same predicates over the same keys, so neither gate's answer
  moves. That state reaches the envelope through a **copy** of the evaluation's parameters — the
  engine's own parameters are never touched, so no `$name` can bind to it — and its lifetime is
  exactly that evaluation, never cached across events or actors, because a grant set outliving its
  evaluation would keep honouring a revoked grant. An envelope handed no scope reads live per row,
  unchanged. Both whole-actor readers sit in the same `grant-change-posture` /
  `interest-change-posture` table as their per-anchor siblings, under the identical default-deny.
  Two details follow from reading a whole actor rather than one anchor, both fail-closed: an
  **unparseable** `cap-read` body denies that anchor and logs at Warn instead of erroring (erroring
  would let one corrupt key wedge every evaluation of an actor holding thousands of good ones,
  identically on every redelivery), and the scope's reads are bounded by a **15 s timeout** — a wide
  actor's grant set is past the multi-get's 1,024-subject fast path, where the no-snapshot variant
  resolves each filter to its subjects with a STREAM.INFO subject filter (computed under the
  stream lock; a key listing is explicitly not the resolution — it is count-bounded and can end
  short) and reads them in ≤ 1,024-key atomic requests; the bound is what keeps a pathological
  read from stalling the lens's consumer silently, and exceeding it is a loud evaluation error
  the Nak path already handles.
- **D1 read-grant change edge + personal convergence sweep
  (`internal/refractor/grantchange`).** That gate makes a personal row a function of *two*
  inputs — the lens's own Core-KV subgraph, which drives it through CDC, and the `cap-read`
  projection, which is written by four *other* pipelines and drives nothing. So a grant landing
  or being revoked changed no row until unrelated traffic happened to re-drive that actor. Two
  mechanisms close that, the same pair every other Refractor plane runs. **The edge (latency
  path):** the read-grant producer's guarded write already holds both the stored and the
  outgoing body, so it — and only it — can tell a grant *liveness transition* from the
  watermark rewrite the guard performs on every evaluation; on a transition it inverts the
  written key back to its actor **and the one anchor whose grant moved**
  (`OutputDescriptor.AnchorEntryFromKey`, fail-closed; the entry token is empty for a key that names the
  actor alone, read as "the whole actor moved") and hands both to
  the process-level `grantchange.Reprojector`, which coalesces per actor and re-drives that one
  actor across every registered personal lens via `Pipeline.ReprojectPersonalActor` — `Hydrate`
  without the terminal `hydrationComplete` marker, with the frame revision captured *after*
  reprojection so the retraction can actually retract. The drain holds a signal, rather than
  consuming it, until two things hold: the in-process lens registry is complete against Core KV,
  **and** every registered personal pipeline reports an ordering token (`Progress().LastAppliedSeq
  != 0`). The second conjunct exists because `ReprojectPersonalActor` refuses with
  `ErrNoOrderingToken` while a consumer's ack floor is unseeded, and the drain consumes each signal
  exactly once without re-enqueueing a failure — so a device changing its interest inside the
  post-restart window would otherwise lose its retraction to the sweeper. Both conjuncts share one
  hold, one two-minute bound and one latch; past the bound the drain proceeds and raises the
  degradation on every registered lens's health entry under the kind that names the conjunct that
  held — `registry-incomplete` for a lens that never activated, `ordering-token-unseeded` for a
  consumer that has applied no event. **Operator note:** on a fresh stack the FIRST grant- or
  interest-change signal after boot can wait out the whole two-minute bound and then raise
  `ordering-token-unseeded`, because a personal consumer that has applied no event yet genuinely
  cannot publish a frame. Nothing is lost — the signal is held, not dropped — and the hold latches
  open once; a fault that recurs after the first boot window is a stalled durable, not this. `Truncate` (a rebuild,
  or the truncating rebuild a *narrowing* MATCH reload owes automatically) announces every purged
  key the same way, since it never reaches the guard. So does the identity key-shred path, on **both** its arms
  — `Pipeline.DeleteAllForActor` for a perEntry target and `Pipeline.Delete` (behind
  `Control.NullifyRow`) for a doc-mode one. Which announcement each makes turns on
  `adapter.GrantTransitionDeriver`, **not** on `OutcomeDeleter`: satisfying the outcome interface
  says only that a retraction reports whether it landed, and both `GrantWriterAdapter` and
  `PostgresAdapter` satisfy it while leaving `Transition` at zero, so keying on it would announce
  nothing for every key while reading like a closed hole. A liveness-deriving adapter retracts
  through `DeleteWithOutcome` and announces per revoked key; any other announces **once per actor**,
  with the actor key the shred call already holds — coarser than per key, strictly safe, and the
  reprojection it drives is per actor anyway. **The producer set is closed by three checks over two
  facts,** because the D1 gate discovers its rows by a wildcard listing and so reads *any*
  `cap-read.` key as a live grant. (1) **What a lens DECLARES:** `projection.CapReadWriterRefusal`
  refuses, at registration, a lens whose §6.13 output key pattern claims the namespace without
  qualifying as a read-grant producer (wrong projection kind, no `entryKeyColumn`, not auth-plane,
  or a key pattern whose inverse cannot name the actor). (2) **What a lens WRITES:** a lens with no
  descriptor declares no key space at all — it renders its key by joining RETURN column values — so
  a plain `nats_kv` lens on the capability bucket returning the literal `'cap-read.billing'` into
  its first key column would mint a live five-token grant no declaration-level check can see.
  `NatsKVAdapter` therefore refuses any write whose *rendered* key claims the namespace unless the
  rule licensed it, fail-closed and terminal, raising `unsanctioned-grant-key` on the lens's own
  health entry once per lens. The licence is bound by `projection.ApplyReadGrantLicence` inside
  `cmd/refractor`'s `buildAdapter` — keyed on `IsReadGrantProducer`, never on "has a sink" — because
  that is the single point every adapter passes through, **activation and the replacement an
  INTO-only hot reload swaps in alike**: a package reinstall with an unchanged cypher classifies as
  INTO-only, so binding at the installer would silently unlicense a producer on `lattice pkg
  install`. The health reporter is bound by the *pipeline* (`New` and `HotReloadInto`), which
  outlives its adapters and therefore also owns the once-per-lens dedup. The two write paths reach
  the guard differently: `upsert`/`deleteRow` render through `buildKey` and are refused there, while
  `truncate` never renders a key — it lists and purges — so an unlicensed adapter's truncate
  **skips** every `cap-read.` key instead, which is what stops a descriptor-less plain lens (whose
  rebuild is unscoped, since `ApplyTruncateScope` derives a prefix only for actor-aggregate lenses)
  from purging every producer's grants under cover of a rebuild. `writeResults` recognizes the
  refusal *ahead of* its `FailClosed` arm: the misconfiguration is permanent, so redelivering would
  spin the lens forever, and the guard refuses that lens's writes in both directions so acking masks
  nothing. (3) **Before either runs:** `scripts/lint-cap-read-producers.go` calls the same predicate
  as (1) over `internal/bootstrap` and `packages/**`, and separately refuses a descriptor-less
  auth-plane lens whose cypher names the namespace; its `packages/**` arm is preventive only, since
  the generated producers are composed at install time rather than written as source literals. A
  qualifying producer installed with no sink offered stays installable and says so loudly — that
  posture is fail-SLOW (its consumers converge on the standing healer), never fail-open.
  **The backstop (convergence path):**
  `grantchange.PersonalSweeper` — one walk shared by every personal lens, over the `identity`
  population from Core KV, `DefaultPersonalSweepBatch` (5) identities per
  `DefaultPersonalSweepInterval` (60s), population cached per cycle rather than re-listed per
  tick, cursor and last-closed-cycle unpersisted so a restart re-verifies from the top. It
  exists because the edge is in-process and best-effort by construction: a crash between the
  producer's write and the drain loses the signal, the coalescing set is bounded, a lens that
  registers late never hears a transition that landed first, and in a partitioned-lens HA
  deployment the two halves can sit in different processes. Prevention best-effort,
  detect-and-recover authoritative. It needs no orphan direction — the authoritative keyset
  frame is the stray-killer, evaluated on the device — which is exactly why the sweep the
  actor-aggregate plane runs does not fit here. **It runs on two cadences.** Every pass publishes the
  authoritative keyset frame, which is the product of both inclusion gates and so re-asks exactly what
  the healer is here to re-ask (`ScopeNone`). A whole CYCLE republishes rows as well (`ScopeAll`) — the
  bounded content backstop for a device that stays connected and never re-hydrates — **at least once
  per `PersonalContentHealInterval` (24 h) and at most once per cycle**, because rows are only
  republished by a whole cycle: a cycle carries content when the cycle *after* it would close past that
  window, so a deployment whose cycle is longer than the interval (population / batch × tick interval
  ≥ 24 h) makes **every** cycle a content cycle and saves nothing. The cycle's kind is latched where a
  cycle starts, at the population re-list, and holds for every batch of that cycle; the first cycle
  after boot is a content cycle, and its start is logged with the elapsed time since the last one, the
  projected cycle length, and the population/batch/interval it was computed from. **Every owned end of
  a personal lens's rebuild window** asks for one as well (`RequestContentCycle`, injected onto the
  pipeline the way the grant-change sink is): the lens published nothing for the length of that window,
  so the content cycle is what hands every device the rebuilt shape once, at a live revision, within a
  cycle rather than within a day. The ask answers the **silence, not the success** — a rebuild
  abandoned at its consumer reset was silent for exactly as long as one that drained — while a
  superseded finisher stays quiet, because the newer rescan's own end is what will ask. One request
  buys exactly one cycle — it is consumed by the same latch, so a package install that rebuilds fifteen
  lenses costs one content cycle between them, and a lens whose target is a stored read model asks for
  none at all (its own replay is what repairs it). Both mechanisms report on each personal lens's
  own health entry: faults through `RecordGrantReprojectIssue` (dropped signals, a failed
  reprojection), and the sweep's cursor / last completed cycle / drain queue depth through
  `personalSweep*` (personal-lens-grant-change-trigger-design.md). The sweep also publishes what its
  last pass **achieved**, as `personalSweepVerdict` in a closed vocabulary — `clean`, `never-passed`,
  `failed`, `population-unreadable`, `instance-count-unreadable`, `instance-count-impossible`,
  `multiple-instances`, and `stale`, the one token the sweep never produces because a stalled sweeper
  reports nothing at all (`health.LagPoller` writes it instead). That field
  is not decoration: the cursor and the cycle stamp advance on a pass in which every reprojection
  failed, because the per-lens failure path logs, raises the fault and continues — so a reader that
  took progress for health would read healthy through the exact condition the healer exists to
  detect. `Run` performs one pass immediately and only then ticks, or the whole plane would spend a
  full interval after every restart with no verdict at all.
- **The personal derivation licence (`personal-lens-derivation-licence-design.md` §4.4).** The
  affected-anchor derivation refused every personal lens outright, because a personal row is a
  function of *two* inputs the compiled pattern does not bind — the D1 read gate and the Interest
  Set — so a derived anchor set that correctly excludes an anchor whose pattern did not change still
  skips a row whose grant did. That refusal was correct while those inputs were silent. Now that each
  has a change edge, `pipeline.personalDerivationLicence` states what it actually required, as six
  fail-closed conjuncts:

  | # | Conjunct | Where it comes from |
  |---|---|---|
  | 0 | this pipeline is a personal lens | asserted by the host at `registerPersonalHealer` |
  | 1 | the D1 read gate is wired, a grant-change reprojector exists in this process, and **every** cap-read producer installed here has a sink | the first two asserted at the same call; the sink half is a live accessor onto `projection.ReadGrantProducersWithoutSink()` |
  | 2 | **all four** Interest Set writers announce, or the lens has no interest filter | a live accessor over `control.Service.InterestChangeSinkInstalled()` (register/deregister/hydrate) **and** `health.InterestReconcilersWithoutSink() == 0` (the orphan reap) |
  | 3 | the personal-plane healer's **last pass verdict** is clean, recent, and from a pass that **began after this lens registered** | read live off `PersonalSweeper.Verdict()` |
  | 4 | the compiled rule references neither `$now` nor `$projectedAt`, exhaustively provable | the compiled rule |
  | 5 | exactly one live Refractor instance — not zero, not more — and the count itself readable | the same pass verdict |

  Conjuncts 0 and the wiring halves of 1 are boot-time facts only `cmd/refractor` holds, and their
  zero value is refusal: a host that asserts nothing narrows nothing. **The two census halves of
  conjuncts 1 and 2 are accessors, not booleans**, and that is load-bearing: a cap-read producer can
  install after a personal lens registered (a hot lens install), and `cmd/refractor` builds the
  `InterestReconciler` *inside* the very activation arm that registers the first personal lens — a
  value sampled at registration would answer about a process that no longer exists, in the fail-open
  direction. A **nil** accessor refuses, because "nobody wired a way to check" and "checked, and it
  is fine" are different answers. Conjuncts 3 and 5 are likewise read **live** at every gate
  evaluation and never snapshotted onto the rule state, for the reason `standingHealerInstalled` is
  read live — both halves of the wiring are installed after the rule is published. Conjunct 3's
  registration clause exists because a lens joining an already-swept plane would otherwise inherit a
  clean verdict from a pass that never drove it: the healer's guarantee is per-lens, so the evidence
  for it is too. Refusal strings are stable (no interpolated durations), because the refusal note
  latches on the string. `patternClosedOutput` stays **false** for a personal lens: it is a claim
  about the lens read by two predicates with different tolerances and different rollback shapes, and
  this narrowing is entitled to change one of them. The knob back is the existing
  `REFRACTOR_ANCHOR_DERIVATION=off`; no new operator surface.

  **Which conjuncts can actually refuse a shipped deployment, stated plainly.** Conjunct 0 and
  conjunct 1 are *structurally vacuous* in the process `cmd/refractor` builds: the personal arm of
  the install switch is what asserts the class, `requireReadGate` is true so a nil capability handle
  refuses registration long before the licence is asked, the reprojector is constructed
  unconditionally, and `startPipeline` always offers the grant sink — so the producer census is empty
  by construction. They are conjuncts because a *different* host (a harness, an embedder, a future
  wiring change) can falsify them, and a licence built only of conjuncts nobody can falsify asserts
  nothing about the class it governs. What can refuse a real deployment is conjunct 2 (transiently,
  while an `InterestReconciler` exists but its sink is not yet wired), conjunct 3 (the healer's own
  verdict — the common case, and the availability cliff R6 names), conjunct 4 (only once an author
  writes a clock into a personal cypher), and conjunct 5 (the deployment's cardinality).

  **A count of ZERO refuses, and is its own verdict token.** The process running the census is itself
  a live Refractor, so a census that finds none has contradicted itself: zero means the census is
  broken — the Health bucket purged or re-provisioned under a running process, heartbeat writes
  failing while listings succeed, a permission change, a key-shape drift — not that the deployment is
  empty. The direction is what makes it a defect rather than a curiosity: two instances whose
  heartbeats are not landing read exactly this on *both* of them, so a readable zero would license
  the narrowing on both while the edge reaches neither. It is refused twice, once where the count is
  read (an empty listing is reported UNREADABLE) and once in the licence, so an edit to either cannot
  reopen it alone.

  **A multi-walk lens derives over EVERY walk, and its anchor set is their union.** A lens that
  compiles to several branches evaluates N independent queries and merges their rows, so the
  derivation's question is asked of the *lens*: one `AnchorHopIndex` per branch, published on the
  rule state as a pair with the conjunct that refused it, one walk per branch from that branch's own
  seeds, and the union of what they reach. The union is a superset of each branch's superset. Two
  properties carry it: a branch that does not bind the changed element seeds nothing — a real answer
  only because every branch that does bind it is walked in the same pass — and `executeBranches`
  re-runs every branch for every derived actor, so the merge cannot make a sibling's contribution go
  unrecomputed. Both are pinned by name in `internal/refractor/pipeline`'s branch tests, and the
  superset itself by a differential test over the real corpus, per branch and unioned.

  The **refusals are of the lens, not of one walk**, because a branch whose graph cannot answer
  contributes an unknown and a union carrying an unknown is a superset of nothing: any walk
  incomplete (`DerivationBranchIncompleteRefusal`, carrying that walk's own reason), any walk with an
  unresolved `*` position (`DerivationBranchUnresolvedExpansionRefusal`), walks that do not all
  anchor on the same label (`DerivationBranchAnchorDisagreementRefusal` — the checkable form of "each
  branch carries its own anchor, and one graph cannot speak for all of them"), or no per-branch graph
  at all
  (`DerivationNoBranchIndexRefusal`). They are written for whatever carries branches rather than for
  today's three lenses: nothing restricts a branches spec to a generated personal lens. All four are
  latent on the shipped corpus, so the census default-denies the vocabulary and a separate vector
  runs the predicate over branch sets that do reach each one.

  **One budget per event, not per walk.** The adjacency read cap, the ranged-work budget and the
  neighbour memo are shared across the branches of one derivation and die with it: three walks over a
  lens's shared vertices read those documents once between them, and a lens too wide for the cap
  declines ONCE — the whole lens, back to the enumerator — rather than paying N times to decline N
  times. The budget must not outlive the event, or one wide event would leave the lens declining for
  ever; both halves are pinned by the cap at which the lens declines.

  `seedAnchorLabels` and the plain arm's `rootHops` stay single-walk on the same arm: a seed label
  that must speak for one evaluation is a different question from an anchor set that is a union.

  **The `health` control RPC answers BOTH halves per lens** — `personalDerivation` carries
  `{licensed, refusal, indexReady, indexRefusal}`, derived live from one rule-state snapshot. They are
  two questions with two different fixes: the licence is about the host's wiring and the plane's
  healer, the index about the cypher, and a fully licensed lens whose walks cannot answer runs the
  enumerator with a clean licence. A reader that took `licensed` for the whole answer would call that
  lens narrowed. Both refusals are the same sentences the `anchor derivation cannot act on this lens`
  log line carries, off one shared predicate, so the gate, the log and the RPC cannot disagree about
  which conjunct refused.
  A **granted** licence logs once, at Info, naming the verdict it was granted on — because the payoff
  is claimed as "the refusal is gone", and an absence of log lines is indistinguishable from a lens
  that stopped receiving events. **A stalled healer surfaces on the entry**, too: the sweep cannot
  report its own silence, so `health.LagPoller` — the one *other* periodic per-lens writer, on its
  own clock — escalates a stored `personalSweepVerdict` to `stale` once the healer's last pass has
  aged past the licence's window. Two writers of one field are safe here by an ownership rule rather
  than by luck: the poller writes only `stale` and the sweep never does, so each value has exactly
  one producer and a recovered healer's own verdict is simply the later write. The healer conjunct of `derivationIndexForAct` now reads
  `standingHealerInstalled()` rather than `p.sweeper` alone: a Personal Lens never receives a
  `SweepPlan`, so the old reader was the one consumer of "has this lens a standing healer" that could
  never see the personal arm. (`oneKeyAnswerSound` is a **third** reader of that same question and is
  deliberately left alone — converging it would arm a different narrowing with no licence review of
  its own.)
- **R4 — the licence revokes itself above one instance, by design.** The grant-change edge is an
  in-process function call, declared as such by `grantchange.GrantChangeEdgeSpansDeployment`. On a
  second Refractor a producer on one instance announces to no personal lens on another, while every
  wiring conjunct above stays true on both — a fail-open at exactly the transition. Two mechanisms
  close it, and they are a pair rather than alternatives. At **runtime**, conjunct 5 counts live
  instances from a Health-KV listing of `health.refractor.*`, once per sweep pass and never per
  event, failing closed on an unreadable count *and on an empty one*. That count is a **backstop with
  a bounded window**, because its two staleness directions are not symmetric: a crashed instance's
  unexpired heartbeat over-counts and refuses (pessimisation, safe — and pinned as correct by a test,
  so a later freshness filter has to argue with it), while a newly started instance that has not yet
  written its first heartbeat under-counts and the licence stays on meanwhile. **The window is not
  the heartbeat interval**: the count is re-derived once per *sweep* pass, so the exposure runs until
  the first sweep pass that begins after the second instance's first heartbeat lands — up to one
  sweep interval on top of the heartbeat, not one heartbeat. At **build time**,
  `scripts/lint-refractor-single-instance.go` refuses the affordance itself — a replica/scale count
  on a refractor service, two background launches in one Makefile recipe or a loop around one, a new
  JetStream queue group under `cmd/refractor` or `internal/refractor`, an instance-identity or
  cardinality env knob, or a `--scale refractor=N` command — while that constant is false. It matches
  a service by name, image or command, and globs override files, because a replica count lands in
  `docker-compose.override.yml` without touching the file under review. Its reach gaps are printed on
  every clean run rather than left implied: orchestration outside this repo (k8s/Helm/Nomad/systemd/
  ECS/Procfile — none ships today), an operator starting the binary twice or running
  `cycle-refractor` without stopping what it replaces, a scale typed at a shell rather than
  committed, a service composed under a name/image/command it does not recognize, and the fact that
  Refractor's lens consumers are pull consumers on a shared durable name, so two processes already
  split a lens's stream with no new declaration anywhere. **A durable, deployment-wide grant-change signal
  (`personal-lens-derivation-licence-design.md` §8 alternative #6) is the precondition of
  re-licensing a personal lens on a multi-instance deployment** — not an optimisation to be done
  later, and flipping the constant without it removes both mechanisms at once.
- **Hydration Hook (Fire PL.4, `internal/refractor/pipeline.Pipeline.Hydrate`).** The cold-start
  catch-up path for a device that missed the SYNC stream's retention window (or is starting for the
  first time): the control-plane RPC `lattice.ctrl.refractor.personal.hydrate` — request body
  `{identityId, deviceId?}`, response
  `{personalHydrate: {hydrated: true, revision, lenses, syncStartSeq}}` —
  re-executes the personal cypher for that one identity via the same `reprojectActors` machinery the
  live cross-vertex fan-out uses (§ above), publishes every resulting row as a normal upsert/delete
  through the active adapter, then (via the adapter's optional `KeySetPublisher` interface) its own
  keyset frame at the same revision (Retraction Fire R1, above), then (via the optional
  `HydrationMarkerPublisher` interface) a terminal `{op: "hydrationComplete", revision,
  projectionSeq}` marker to the identity's subject. `lenses` is the set of registered personal
  hydrators that ran — the Edge client drops any stored attribution for a lens **not** in this set
  after a completed hydrate, healing a decommissioned/re-minted lens's otherwise permanently-stranded
  keys (no live emitter would ever retract them any other way). `revision` is the pipeline's own CDC
  forward-progress
  (`Progress().LastAppliedSeq`) captured *before* reprojection runs, so any live incremental delta
  applied concurrently with or after the hydrate call necessarily carries a revision at or above
  this snapshot's — the Edge's last-writer-wins-by-revision resolution can never let a bulk
  hydration snapshot regress a fresher incremental delta that raced it. `syncStartSeq` is a
  **different counter** and the two must not be conflated: it is the **SYNC stream's** last sequence,
  read once from a freshly fetched `StreamInfo` before the hydrator fan-out (the seam
  `Service.SetSyncLastSeq`, wired in `cmd/refractor/main.go` beside `SetSyncFirstSeq`), whereas
  `revision` is a **CDC** projection sequence. The burst that follows is a projection of the world as
  of that SYNC sequence, so a cold or gapped Edge node starts its durable consumer at
  `syncStartSeq + 1` and never reads the retained history the burst already accounts for
  (`edge-cold-signin-delivery-position-design.md` §3.2/§3.4). The field is fail-soft: an unset seam or
  a read error returns `0` and hydration still succeeds — a node reading `0` asserts no start position
  and takes the subject's whole retained history, the over-deliver direction. When `deviceId` is given and
  the Interest Set KV is configured, the resulting revision is best-effort recorded into that
  device's Interest Set doc (`revisionCursor`, preserving its existing `types`/`anchors` filter) —
  bookkeeping only; the Edge itself decides warm-vs-cold hydration from its own local cursor, not
  from this field. Wired in `cmd/refractor/main.go` via `controlSvc.RegisterPersonalHydrator(r.ID, p)`
  alongside `InstallPersonalLens` — a deployment installs one Personal Lens pipeline per
  `nats_subject` rule (edge-manifest alone ships ten), so this is a per-ruleID registry like
  `RegisterReprojector`: the "hydrate" op fans out to every registered pipeline for the requesting
  identity and reports the max revision across all of them.
- **Deferred to later PL fires** (`personal-secure-lens-design.md` §7): Vault-ciphertext +
  transient-key composition (PL.5, 🚧 gated on Vault Phase A).

### Protected read-model provisioning (read-path authorization, D1.3)

A **protected** read model lives in Postgres under **row-level security** so a reader sees only
the rows it is authorized for (Contract #6 §6.14). Like every other Postgres target, the table
is **provisioned out-of-band** — Refractor issues **no DDL**. The difference from a plain table
is the security plane: a missing/disabled RLS posture produces **no write error** (writes to an
unlocked table succeed; the table is just world-readable on the *read* path), so the ordinary
"pause on write error" net would fail-**open**. Refractor closes that gap by **actively
verifying the RLS posture at activation and pausing the lens fail-closed** if it is absent —
the **verify-and-pause** model. There is now **one** principle for all Postgres provisioning
(out-of-band), and FORCE RLS stays structural by being *verified*, not *created*.

The read-path primitives live in `adapter/rls.go`:

- **`VerifyProtectedTable(pool, table, keyCols, body)`** is the read-only posture check (no DDL,
  no writes — only system-catalog reads) that a protected lens runs as its `Probe` while
  infra-paused at activation. It gates, in priority order: the table exists, is an ordinary
  table, and has row-level security **both `ENABLE`d and `FORCE`d** — the security-critical bit
  (FORCE *without* ENABLE leaves the table world-readable; with both on, a missing/wrong policy
  **denies all rows** — §6.14 H3, fail-closed never leak); the expected columns are present with
  the platform types (`authz_anchors` is exactly `text[]`, `projection_seq` is `bigint`, every
  key + body column present); and the deterministically-named **`FOR SELECT` set-membership
  policy** is present and intact (its `USING` references `authz_anchors` against the grant table
  — a permissive `USING(true)` policy is rejected, not just any SELECT policy); and a **unique
  index exactly covering the `ON CONFLICT` key columns** exists. Failures are
  plain (recoverable) errors so the lens auto-resumes once the operator provisions the table.
- **`VerifyGrantTable()`** (on `PostgresGrantWriter`) is the same read-only check for the shared
  **`actor_read_grants`** table — it asserts the expected columns + types, and the unique index
  covering `(actor_id, anchor_id, grant_source)`, so the seq-guarded
  writes and every protected policy's membership subquery have the shape they depend on. The
  grant table is the read-auth source of truth, not a protected business table, so it is not
  itself RLS-locked — only its shape is verified.
- **The arbiter-index check is not shape pedantry.** Every write on both paths is an
  `INSERT … ON CONFLICT`, and Postgres infers its arbiter from the unique indexes whose key
  columns match the conflict target *exactly* as a set — so a table re-provisioned without its
  primary key raises `42P10` on every single write. `42P10` is in the structural set, so the lens
  pauses dark rather than Naking forever while its health entry reads `active`. The emitted DDL
  always creates the matching `PRIMARY KEY`; what this catches is a table re-provisioned by hand
  from a subset of it.
- **`BuildProtectedTableDDL(table, keyCols, body)`** / **`BuildGrantTableDDL()`** generate the
  exact DDL each table expects (key + body columns plus the platform `authz_anchors text[]` /
  `projection_seq bigint`; `ENABLE` **and** `FORCE ROW LEVEL SECURITY`; the `FOR SELECT`
  set-membership policy — a row is visible iff the current actor,
  `current_setting('lattice.actor_id', true)`, NULL-safe → deny when unset, holds a **live**
  grant for **any** of the row's `authz_anchors`). They are **no longer executed at activation**
  — they are the single source of truth the verifier checks against *and* the operator runbook
  (below) emits.
- **`PostgresGrantWriter`** maintains the grant table's contents (it no longer provisions it).
  `UpsertGrant` / `RevokeGrant` enforce the §6.14 **monotonic-seq guard** (a write takes effect
  only when `projectionSeq` strictly exceeds the stored one, per
  `(actor_id, anchor_id, grant_source)`), so a stale CDC replay can neither downgrade a fresh
  grant nor **resurrect a revoked one** (H4). `grant_source` (the contributing lens's canonical
  name) keeps producers disjoint — a revoke from one package never wipes another's coexisting
  grant. RLS then unions across all sources natively via the policy.

**Activation wiring.** A postgres lens spec declares the read-path posture in its
`targetConfig`:

- **`protected: true`** + a `columns: [{name, type}]` list → the lens registers with
  `InitialPause: PauseInfra` (the substrate seam that makes a consumer **probe before its first
  drain**) and wraps the Postgres adapter in a **`ProtectedAdapter`** whose `Probe` is
  `VerifyProtectedTable`. So the lens starts infra-paused, verifies the out-of-band posture, and
  **projects nothing into a table that is not locked down**; once the operator provisions it the
  next probe passes and the lens **auto-resumes** (no operator Resume, no Refractor restart). The
  adapter also encodes the `authz_anchors` (and any declared `text[]`) column as a Postgres array
  (the full engine emits a list as `[]any`, which the base adapter would otherwise coerce to
  JSONB). A protected lens's Delete is **always** the seq-guarded soft tombstone
  (`UPDATE … SET is_deleted=true, deleted_at=NOW(), projection_seq=$N`, conditioned on the incoming seq
  exceeding the stored one — `adapter/postgres.go`) regardless of the declared `deleteMode`, and the
  generated policy filters `NOT is_deleted` before evaluating membership (Contract #6 §6.14).
- **`grantTable: true`** → the lens projects to `actor_read_grants` through the seq-guarded
  **`GrantWriterAdapter`** (table + composite key `actor_id, anchor_id, grant_source` default
  from the platform; the lens need only RETURN those three), and likewise starts infra-paused
  behind `VerifyGrantTable`. Its `Delete` path tombstones via `RevokeGrant`; it intentionally
  does **not** support truncate (the table is shared across every `grant_source`).
- **`public: true`** → the auditable opt-out; no RLS, provisioned out-of-band like any plain
  SQL-target lens. A lens may not be both `protected` and `public`.

**Re-verification after activation.** The `Probe` runs while a lens is *paused*, not while it is
draining, so a posture turned off after activation (e.g. `ALTER TABLE … NO FORCE ROW LEVEL
SECURITY`, a dropped column, a re-provision that lost the primary key) is caught by the **write**
that then fails: `42P01` / `42703` / `42P10` classify structural and pause the lens. That is still
stronger than create-once provisioning, which never re-checks drift at all — but the detection is
write-triggered, so a lens with no traffic can hold a drifted posture until its next projection.

Both protected and grant lenses then set `substrate.ConsumerSpec.StructuralProbe`, which lets that
structural pause **re-run its own probe and resume when the posture verifies clean** — the same
fail-closed gate, with no operator required, bounded by a three-attempt relapse latch and announced
on the heartbeat — as `CapabilityLensStructuralPauseAutoRecovered` for a **grant** lens (every one
is auth-plane by `projection.IsAuthPlane`) and `LensStructuralPauseAutoRecovered` for a protected
business lens. See
[refractor-failure-tiers.md](./refractor-failure-tiers.md) for the tier semantics, the exclusions
(the plain Postgres adapter's `pool.Ping` probe cannot adjudicate its own condition), and the latch.

**Operator runbook (out-of-band provisioning).** The DDL is emitted, never hand-written:

- **`lattice lens emit-ddl`** prints the exact `Build*TableDDL` for every installed
  protected/grant lens (read-only against Core KV; grant table first, then each protected
  table), to apply against the read-model database as a migration.
- **`make provision-readpath`** applies that same DDL to the dev Postgres (idempotent —
  `CREATE TABLE IF NOT EXISTS` / `DROP`-then-`CREATE POLICY`); it is wired into `make up-full`
  and `make up-loftspace` so the local stack is one command. Run it **after** install so the
  lens specs exist in Core KV; a no-op when no protected/grant lens is installed.

**Status:** verify-and-pause provisioning, the grant writer, the two read-path adapters, the
`InitialPause` substrate seam, and the operator runbook all ship; the first protected business
read model (`read_lease_applications` + `read_landlord_lease_applications`) and its
`cap-read.*` grant lenses are live in the LoftSpace vertical (`make up-loftspace`), read through
the non-superuser SELECT-only `loftspace_app` role so RLS is enforced. The H3 deny-all, H4
no-resurrect, the verify-and-pause posture checks, and an end-to-end seam proof run against a
real Postgres under `POSTGRES_TEST_DSN`.

---

## Rule engine

Refractor has one engine implementation, the full openCypher engine.
Selection logic lives in `internal/refractor/ruleengine/`
(`Registry.SelectForLens`) — every lens declares `engine: "full"` (or leaves
it absent, which resolves to `"full"`); any other value fails lens
validation. The full openCypher engine is the only rule engine Refractor runs.

### Full engine (`ruleengine/full/`)

- openCypher parser via `antlr4-go/antlr/v4 v4.13.1` runtime
- Grammar vendored from `jtejido/go-opencypher`
- `full.Engine.Parse` — lexes + parses via generated `cypher.CypherLexer` / `cypher.CypherParser`; walks AST with `newASTVisitor`; returns `*CompiledRule`
- `full.Engine.ExecuteWith` — evaluates the compiled query against Core KV + Adjacency KV; produces projection rows
- **Canonical engine for new lenses.** The bootstrap-seeded Capability Lens uses `engine: "full"`.
- **Wiring**: `cmd/refractor/main.go` constructs `full.New()` and registers it; `startPipeline` routes based on `r.ResolvedEngine == ruleengine.EngineFull`

#### Batched reads: a hop's frontier, a projection's aspects, a stage's bound sources

An evaluation reads a relationship hop's whole admitted frontier, and the aspect (or link) bodies a
projecting clause is about to dereference off its rows, through the substrate's multi-get
(`GetMultiNoSnapshot`, exact keys, chunked at the primitive's atomic fast-path cap) rather than one
point read per key — so a wide actor costs a round trip per chunk instead of one per neighbour and
one per column per row. A batched entry is **staged**, and enters the evaluation's node memo — and so
its read-surface footprint — only when the evaluation actually dereferences that key: a clause with a
branch (a `CASE` arm not taken, a short-circuited `AND`) does not use everything the batch read, and
the certificate the pipeline re-checks stays exactly the set of keys the evaluation used. Absent and
soft-deleted keys decode to the same nil handle a point read of them yields, at revision 0.

The **adjacency** side batches the same way: a stage that hops from nodes it has already bound — an
`OPTIONAL MATCH` or a pattern comprehension hanging off a variable bound to many rows, which is
otherwise one node-state read per row — reads every source's document-and-mark pair in one chunked
request, sized so both of a node's keys stay inside one instant. A staged answer is likewise promoted
on use: through the same `memoizeWhole` composition, recording the same fingerprint, and reporting the
same read to a context read observer, at the point the per-node read would have happened. An
overflow-marked node is never answered from a batch — its edges live in Core KV's link keyspace, and
it keeps the relation-scoped read. A stage the branch-decomposition analysis split takes the same batch
per deferred branch, over every base row, before the fold loop that would otherwise expand each branch
one row at a time; a later clause of such a branch hops from a head the expansion itself binds, so it
stays per row.

#### The pattern graph steps a ranged hop

`AnchorHopIndex`/`ScanRootHopIndex` (`hopindex.go`) index a **variable-length** relationship
rather than refusing it: `PatternHop`/`PatternStep` carry the hop's `[Min, Max]` range, clamped at
index-build time by the same `maxVarLengthHops` the executor's own `traverseRel` applies
(`executor.go`, `rel_traverse.go`). The derivation walk
(`pipeline.walkToAnchors`) answers such a step with a bounded frontier expansion — the zero-hop
admission when `Min == 0`, a closure-local cycle guard, and the far-end label prune applied **only at
admission**, never to intermediates, because the executor filters intermediates by nothing either.
Every read still goes through the walk's one `edgesOf` closure, so `DefaultDerivationReadCap` bounds
it and a breach returns `ok == false` and runs the shipped `ActorEnumerator` BFS: no new budget, no
truncation.

The soundness statement is narrower than it looks and is what makes the shared clamp load-bearing:
**the derivation is complete with respect to what the executor will evaluate, not with respect to the
graph.** An anchor whose path crosses more than `maxVarLengthHops` of a ranged hop cannot produce a
row, because the executor's walk stops there too.

A ranged hop's distance is an interval, so it contributes no `HopIndex.Dist`. `Dist` is computed over
binding hops, and any position the anchor can reach across a ranged **binding** hop takes the
incomparable `-1` sentinel — `AnchorSideSeeds` then seeds **both** endpoints, which only widens the
derived set. An over-stated distance would be the unsound direction: `consider` drops the endpoint
whose distance is larger.

#### `OPTIONAL MATCH … WHERE` null-restore semantics

When an `OPTIONAL MATCH` pattern matches real neighbors but a `WHERE` then excludes
**every** one of them, `applyMatch` preserves the anchor row with the optional
pattern variables bound null — the correct Cypher OPTIONAL MATCH semantics, for every
cypher. The null fallback is constructed from the source binding (`nullBindNewVars`,
shared with `matchPatterns`'s no-match branch), not recovered from the expansion set:
when the pattern matched only real neighbors, the expansion set holds no null row to
recover, so an anchor whose sole neighbor is WHERE-filtered must be null-restored
from the source. This is what makes a dedicated family-filtered `OPTIONAL MATCH …
WHERE` safe: a no-match anchor projects with the optional column null instead of
dropping the row (a dropped convergence row reads to Weaver as an entity deletion).
The lease vertical is the worked example of the *other* way to stay safe — its
background-check freshness discrimination lives inside a `count`/`max` `CASE` on one
unfiltered `providedTo` fan, so there is no filtered optional to null-restore at all,
and the deadline it compares against is a stored marker rather than `$now`.

#### Anchor-tombstone retraction (plain projection lenses)

The full engine is upsert-only: `ExecuteWith` re-derives a lens's rows by re-scanning
Core KV (it ignores the CDC event's payload), and `fetchNode` filters a soft-deleted
vertex, so a tombstoned **anchor** yields **zero rows** — but the engine never emits a
*Delete*, so the row the anchor previously projected would linger in the lens target
forever. The pipeline closes this: when a CDC event is a **root tombstone of the lens's
anchor** (`isDeleted` true, event vertex type == the first `MATCH` node's label), it emits
a Delete keyed by the anchor's output columns (`full.Engine.AnchorDeleteResult` derives the
key from the AST). It resolves **every** declared key column **read-free** against the
tombstoned anchor — `<anchor>.key`, a root-body field, or a pure function over them (e.g.
`nanoIdFromKey(identity.key)`) — so a **composite-key** lens retracts the exact row it
projected; a column that would need a Core-KV read (an aspect access on a now-deleted vertex)
is unresolvable and the event falls through to a re-execute (never a wrong or partial Delete).
This mirrors the **actor-aware capability path's** tombstone shortcut, which already
retracts; it is the non-actor twin of that retraction path.

**Multi-column projection keys.** For a **plain** projection lens the full engine builds the
**complete** key map from the lens's declared key columns (`Rule.Into.Key`, threaded onto
`full.CompiledRule.KeyColumns` at activation) — so a composite-key
lens such as the D1 `capabilityReadGrants` **GrantTable** producer (keyed on
`actor_id, anchor_id, grant_source`) hands the `GrantWriterAdapter` every key column it
requires and actually populates `actor_read_grants`. Each declared key column must be a
`RETURN` alias, validated **fail-closed at activation** (a mis-declared key fails the lens,
not silently drops a column at write time). The **same** complete key is built on the
anchor-tombstone Delete path above, so the grant lens's self-grant is `RevokeGrant`'d when its
identity is tombstoned (the §6.14 seq-guarded soft-tombstone). A single-key lens is unchanged
(one column = the sole `RETURN` key). **Envelope lenses** (actor-aggregate `cap.<actor>` / the
operation-role index) are *not* threaded: their projection key is synthesized by the envelope
at write time, not taken from the `RETURN` columns.

A tombstone of a **secondary** (non-anchor) node — e.g. a deleted patient on an
appointment lens — is *not* a retraction: it re-executes so dependent fields refresh (the
appointment row survives with `patientName` null).

#### Plain-lens aspect/link reprojection + filter-retraction

A plain (non-actor-aware) **full-engine** lens **reprojects on aspect/link-only
mutations**: a `KindAspect` CDC event re-executes seeded from the aspect's **owner
vertex** (`evalPlainAspectReprojection` — the plain analog of the capability path's
`evalAspectFanOut`; a Secure Lens's piiKey shred scrubs projected plaintext through this
same arm), and a `KindLink` event re-executes seeded from **both endpoint vertices**
(`evalPlainLinkReprojection`, results deduplicated across the two seeds). So an edited
listing price or a renamed provider is promptly fresh in its read model, instead of
incidentally fresh on the next unrelated vertex-root event.

**A pattern label is the vertex key type.** `MATCH (u:unit)` binds a vertex whose key
parses as `vtx.unit.<id>` — nothing else. Fine-grained classification lives in the body's
`class` field (Contract #1 §1) and is matched as a property predicate,
`MATCH (l {class: "location"})`, which works in seed and traversal position alike. The
distinction is load-bearing rather than stylistic: the labeled seed scan, event seeding,
anchor retraction, and the narrowing derivation below all read a label as the key type,
so a binder resolving it any other way would narrow those on a set the executor does not
honor. The property form is the wider one — being unlabeled costs `exhaustive`, and an
**anchor** position cannot use it at all, since anchor event-seeding and tombstone
retraction both require a label.

**Type-relevance skip (the amplification bound).** The re-execute runs only when the
event's owner/endpoint vertex **type** is in the lens's referenced-label set
(`full.CompiledRule.ReferencedLabels` — every node label its MATCH patterns, pattern
expressions, and comprehensions can bind): a `meta` aspect mutation cannot change a
`MATCH (u:unit)` lens's rows, so the lens acks it without scanning. A query whose label
set is not exhaustive (an unlabeled node pattern, or a variable-length relationship
whose intermediate hops bind arbitrary types) disables the skip and reprojects on
every event — conservative, never a missed refresh.

A label makes that set exhaustive only where it actually **constrains** what survives.
A label in a **required** `MATCH` does, in both directions — a required match on an
already-bound variable drops the bindings that fail it, so it prunes an earlier
whole-bucket seed. A label in an **OPTIONAL** `MATCH` does so from that clause onward
only: the path binds as a unit or null-binds its new variables, and it never prunes a
binding made earlier. A label inside a `WHERE` or a pattern comprehension constrains
**nothing** downstream — those bindings are discarded, so a later `MATCH` on the same
name is a fresh whole-bucket seed. Both scopes reset at a `WITH`, which rebuilds every
binding from its projection items alone.

On top of that freshness transport sits **filter-retraction**: after any plain
(no actor enumerator, no envelope) full-engine re-execute, a presence check derives
the event anchor's projection key
read-free (`full.Engine.AnchorProjectionKey` — the same derivation
`AnchorDeleteResult` delegates to) and, when the anchor is **absent from the re-derived
row set** — its `WHERE` predicate flipped, a keyed aspect was tombstoned, a required
link was removed — emits a Delete on that key. The safety keystone: the derivation
succeeds **only** for a one-row-per-anchor, anchor-keyed lens (every key column
resolves read-free from the anchor binding alone; a key column referencing a
**non-anchor variable is rejected structurally**). A `WITH` between the RETURN and the
pattern does not by itself refuse: the structural half (`anchorProjectionShape`) first takes
the memoized `WITH`-scope verdict (`withscope.go` — a renamed pattern variable, a
dropped-then-re-read name, or an unmodelled clause refuses) and then **resolves each key
column back through the `WITH` aliases** to an expression over pattern variables
(`withalias.go`, default-deny on any node it does not model), so `nanoIdFromKey(entityKey)`
behind `WITH app.key AS entityKey` is judged as `nanoIdFromKey(app.key)`. Corpus verdicts are
pinned per lens by `plain_with_alias_closure_census_test.go` (bucket F = the `WITH`-bearing lenses
that are closed: `leaseApplicationsRead`, `renewalsRead`, `clinicPatientsRead`). A **neighbor-keyed composite**
lens (e.g. `read_landlord_lease_applications`, keyed `(app_id, landlord_id)`) falls
through to the previous linger behaviour — never a wrong or partial Delete. A
never-matched anchor emits an idempotent Delete against an absent key — a no-op on a
NATS-KV/Postgres row target (pinned by test); on a **GrantTable** target the
`RevokeGrant` write deliberately inserts a seq-stamped tombstone row for a
never-granted key (deny-direction, ≤1 row per actor — it also makes a `protected`
flag flipping false promptly revoke the wildcard grant).
Convergence (`violating`-flag) lenses on a **plain** (non-actorAggregate) full-engine
path are unaffected by design and now **enforced at activation**: a lens projecting
into the shared `weaver-targets` bucket may carry no filtering (non-optional) `WHERE`
— `(*full.CompiledRule).ValidateNoFilteringWhereForConvergence`, called from
`startPipeline` alongside the `DiffRetraction` guard, fails the lens closed rather than
let its presence-check retraction emit a Delete Weaver would misread as "entity gone"
instead of "stopped violating." **actorAggregate convergence lenses are exempt** — the
presence check only ever runs for `p.actorEnumerator == nil && p.envelopeFn == nil`, so
it structurally cannot fire for an envelope lens regardless of `WHERE`; their zero-match
case instead retracts through one of two envelope-level transports, chosen by where the
filtering `WHERE` sits. A lens whose anchor MATCH always succeeds and only an
OPTIONAL-MATCH secondary pattern is conditional (`RealnessFilter` set) retracts through
the per-row envelope callback once the row's realness columns come back empty
(`projection/driver.go`'s `EnvelopeFn`) — proven live by `unroutedTasks`
(`packages/orchestration-base/lenses.go`). A doc-mode `emptyBehavior: delete|softDelete`
lens whose filtering `WHERE` sits on the anchor match itself — so the cypher returns no
row at all once it stops matching — retracts through the zero-row-retraction transport
(`pipeline.Pipeline.zeroRowRetraction`, armed by `projection.InstallActorAggregate`) —
proven live by `orphanedTaskGrants` (`packages/orchestration-base/lenses.go`). Either
way, a missing row and a live `violating: false` row are handled identically by
Weaver's evaluator.

**Neighbor-driven / multi-row retraction (target-diff, opt-in).** A neighbor-keyed
composite lens whose presence check structurally falls through (above) can opt into
`DiffRetraction` (a lens-definition flag, `pkgmgr.LensSpec.DiffRetraction` →
`lens.IntoConfig.DiffRetraction` → `pipeline.SetDiffRetraction`, threaded like every
other per-lens component — never canonical-name-keyed): when the presence check's
`ok` comes back false, the pipeline instead reads the target's **full live key set**
via `adapter.KeyLister.ListKeys` and diffs it against the re-execute's **full**
freshly-computed row set, emitting a Delete for every key the target still carries but
the fresh computation no longer produces. This is exact — not an approximation scoped
to whichever vertex happened to trigger the event — because a `DiffRetraction` lens's
query is **unanchored** (no `{key: $actorKey}` anywhere): the re-execute already
recomputes the complete current truth on every trigger regardless of which vertex fired
it, so comparing full-target-state to full-fresh-state is correct by construction, and
sidesteps the ambiguity a per-vertex-scoped diff would hit (an `identity` endpoint can be
either the applicant or the managing landlord role in `read_landlord_lease_applications`,
with no single stable id to scope a prefix-list by).
`(*full.CompiledRule).ValidateUnanchoredForDiffRetraction` is the activation-time
backstop: a lens that references `$actorKey` anywhere fails to activate rather than
mass-retracting every other live anchor's rows on its first event — the diff's
soundness rests entirely on that invariant. `read_landlord_lease_applications`
(`(app_id, landlord_id)`, D1.3 Increment 2, Vault 5b's manages-unassign consumer) is the
reference `DiffRetraction` lens; eight more declare it today (`grep -rn "DiffRetraction: true"
packages/*/lenses.go`); a lens whose key resolves to its anchor — through a `WITH` or
not — needs none of it for the retraction and takes the anchor Delete; a Secure plain lens
keeps the declaration as a continuous per-event healer the detect-only audit does not offer
(`clinicPatientsRead`). A convergence
(`violating`-flag) lens never opts in, so its never-retract contract is untouched.

**Neighbour-driven retraction (the plain arm's anchor derivation).** The other transport for a
drop-out no event names — a neighbour vertex tombstoned, a link two hops out removed, a
neighbour's aspect flipping a `WHERE` — is the plain arm's own affected-anchor derivation
(`anchor_derivation_plain.go`). In `act` mode on a lens its **licence** admits, a neighbour
event is answered by one seeded evaluation per derived anchor instead of the whole-corpus
rescan, and each of those evaluations can emit the anchor Delete the rescan structurally
cannot: an upsert-only rescan names no key that dropped out, and the filter-retraction
presence check derives its key from the EVENT vertex, which on a neighbour event is not an
anchor. The licence is fail-closed and read per event, never snapshotted: not the auth plane,
an enrolled and unsuppressed divergence auditor whose last verdict is recent, a full-engine
rule, a target that can read a row back, no `$now` / `$projectedAt`, and per-anchor closure
(`ProjectsOneRowPerAnchor`).

**A Secure Lens is licensed on exactly those conjuncts** — the decryptor is not one of them.
What makes that sound is a wiring invariant rather than a predicate: the re-entrant evaluation
goes through `evaluatePlainFromVertexRaw`, which **never decrypts**, so the outer
`evaluateForEntry` wrapper the whole event already runs inside stays the single choke point and
a derived anchor's secure columns are decrypted exactly once. A re-entry that decrypted on its
own account would hand that wrapper a decrypted string where a ciphertext envelope map is
declared — Terminal, and the column is redacted to `null`, a stored row silently missing its
PII rather than an error (`TestSecureDecryptor_DecryptCallsPerEvaluation` is what holds it).

A derived set larger than `DefaultPlainDerivedAnchorCap` (64) is a **fall-back, not a
truncation**: the event takes the rescan, so the retraction is not made and the row is left for
the audit's `retained` class. That is published, because a transport that is silently off reads
exactly like one with nothing to do — `derivationArmed` (the transport's own live posture),
`derivationFellBack` (fall-backs since the process started, every cause) and
`derivationOverCapSize` (the last refused derived set's size, carried only once it has fired)
ride the lens's liveness entry, and are absent entirely for a lens whose derivation is
**ineligible** — a fixed property of its shape (`act` mode plus the derivation index's own
conjuncts), independent of the licence. An ELIGIBLE lens the licence currently turns back still
publishes all three, `derivationArmed: false` among them: a static licence refusal is never
counted as a fall-back, so `derivationFellBack` keeps whatever it already accrued and
`derivationArmed` — not absence — is what says the transport is declared but currently off.

**A business-plane lens that needs one of those two transports must carry one, refused at
activation otherwise.** `CompiledRule.ExistenceDependsOnNeighbour` classifies the shape: a
non-`OPTIONAL` `MATCH` reaching a variable the anchor does not bind, or a `WHERE` reading one —
directly, or through a `WITH` alias it resolves back to its source bindings — makes the row's
EXISTENCE a neighbour's to decide, so a neighbour event can drop it and no anchor event names
it. Such a lens must satisfy T1 (`Pipeline.PlainDerivationStaticallyEligible`, which is the
derivation licence's own static prefix — one function, so the gate cannot admit a lens the
licence will never license) or T2 (target-diff retraction on a target it owns). A classifier
answer that is not exhaustive is a refusal, never a pass. The lens does not activate: dark is
the safe end, because a lens silently keeping orphaned rows on a Protected table serves them
under stale authorization anchors, and after an erasure serves plaintext no re-projection
reaches. The reason lands on the lens's health entry (`lastError`), which is the only account a
lens with no heartbeat status can leave.

The gate is scoped to the **business plane** — the same boundary the divergence audit draws,
and for the same reason: an auth-plane verdict belongs to the plane that has a code, a severity
ladder and an escalation for it, and the derivation licence refuses that plane outright, so T1
is not available there to satisfy the rule with. The auth-plane lenses that carry no transport
are named debt rather than an absence, pinned by name in the retraction-transport corpus census
(`internal/refractor/plain_retraction_transport_corpus_census_test.go`), which is also where an
author meets the business-plane rule before a runtime refusal does.

`retractionTransport` publishes the verdict per lens — `derivation`, `diffRetraction`,
`diffRetraction-prefix`, or `derivation (audit disarmed)` — for a lens whose rows a neighbour
can drop, and nothing for one whose rows cannot. Two further values describe an obligation NOT
met: `none` (its rows depend on a neighbour and nothing retracts a drop-out) and `unclassified`
(whether they depend on one could not be derived from its query shape, which every gate reads
as a refusal rather than as "they do not"). Both raise `LensRetractionTransportMissing`
(`error`) and the per-lens `alert` `retraction-transport-missing`, and neither should ever
appear: activation refuses both shapes and so does every hot-reload path that could install
one, so the alert is the backstop for a lens that reached the registry past all of it. The
field is **business-plane only** — the heartbeat's lens provider filters the auth plane out
before reading it, because an auth-plane lens publishes `CapabilityLensStatus`, which has no
member for this and whose per-row verdicts belong to the convergence sweep and the
`Capability*` codes. **The disarmed reading is the deployment's
own**: `SetAuditEnabled(false)` makes the audit refuse every lens corpus-wide, so the
derivation licence's first conjunct can never hold and a T1 lens's declared transport is
carrying nothing. The heartbeat raises `LensRetractionTransportDisarmed` (`warning`) once,
listing what the switch voided, and moves no lens's own `alert`: one operator decision reported
as N lens faults would bury the decision.

**T2 on a target the lens SHARES is scoped, or refused.** `NatsKVAdapter.ListKeys` enumerates
the whole bucket and its key mapping filters only by segment count — a single-column key is
kept verbatim — so an unscoped diff on a shared bucket reads every sibling's key as a row this
lens no longer produces and Deletes it. **The scoping is the only protection a listed key gets**:
the diff Deletes every key the fresh row set does not contain, and a sibling's key is never in
that set, so nothing downstream of the listing can spare it. Activation decides sharing from
the lens **registry**, the only place that knows it (`pkgmgr` validates one package at a time
and cannot see a bucket another package shares), on three rules:

- **Scoping is unconditional.** Any `DiffRetraction` lens with a derivable
  `OutputDescriptor.KeyPrefix` is scoped to it at activation, siblings or not. Deriving it only
  for a bucket that already holds one leaves a diff lens that loaded *first* listing the whole
  bucket for the life of the process — nothing re-scopes a running pipeline when the sibling
  arrives.
- **A live unscoped diff refuses the newcomer.** What a sibling is asked is what its diff
  *lists* — the prefix installed on its running pipeline — never what its declaration would
  admit. The sibling is running, so refusing the arriving lens is the only disposition
  available.
- **Prefixes must be provably disjoint.** `KeyPrefix` admits `cap.` for a lens whose keys are
  `cap.roles.*`, so two *nesting* prefixes are one diff listing the other's rows. On a bucket a
  diff reads at all, every lens must declare a key space, and no two may nest; a lens that
  declares none leaves the disjointness unprovable, which is refused the same way.

Those three together are what make the verdict independent of load order — not the check being
"asked of every lens", which on its own decides nothing about a diff that is already unscoped.
The same rule is re-asked on the two hot-reload paths that can reach it: an `INTO` edit moving
the lens onto another bucket, and a `MATCH` edit (whose swap cannot re-scope a running diff, so
a target needing a different scoping is refused rather than applied under the old one).

**A MATCH edit re-enters the transport gate.** A cypher edit can add a required neighbour to a
lens whose rows nothing could orphan before — exactly what activation refuses — and the swap
that carries it is the one path no activation follows. The gate is re-asked of the rule the
pipeline now evaluates, and a refusal puts the previously admitted rule back: the lens goes on
running the shape it was admitted with, and the reason lands on its health entry like every
other hot-reload refusal. `diffRetraction` itself is **not** hot-reloadable in either direction
— the flag and its scoping bind before the lens runs, so an edit to it is refused with the
re-activation remedy rather than accepted and silently not applied.

### Property model (how lens cypher reads a node)

A vertex's Core KV body carries the **envelope** (`key`, `class`, provenance,
`isDeleted`) and, by exception, a small `data` object for types that keep
business data on the vertex root (e.g. permissions: `perm.data.operationType`).
**Business data otherwise lives in aspects** — separate Core KV keys
`vtx.<type>.<id>.<localName>` whose body nests the value under `data`
(`canonicalName` → `data.value`, `description` → `data.text`). Vertices exist
mostly to walk links; aspects hold the data.

Lens cypher reads these **explicitly**, and the full engine's property resolver
(`executor.go` `resolveProperty`) disambiguates by presence in the root body:

| Cypher | Resolves to |
|---|---|
| `node.key`, `node.class` | vertex envelope (root body) |
| `node.data.<field>` | the vertex root `data` object (permissions only, by exception) |
| `node.<aspect>.data.<field>` | point-reads the aspect key `vtx.<type>.<id>.<aspect>` and navigates its body — e.g. `role.canonicalName.data.value` |

A name **present** in the root body returns that value; a name **absent** from
the root body is treated as an aspect reference and point-read (not a scan).
Only the first hop off a vertex resolves an aspect; the returned aspect body is
a plain map, so `.data.<field>` is ordinary map navigation. Authoring rule:
write the path the data actually lives at — `perm.data.operationType` (root),
`role.canonicalName.data.value` (aspect).

### Property model (how lens cypher reads a relationship)

A pattern may name the relationship it walks — `-[r:boundTo]->`, `-[r]->` — and
the walk binds `r` to the **link** it crossed, built from the adjacency entry it
already holds. Three things project off it:

| Cypher | Resolves to | Cost |
|---|---|---|
| `type(r)` | the relation name — the `<relation>` segment of the link key, i.e. the link's localName (an untyped `-[r]->` reports whichever relation the walk actually crossed) | free — already in the adjacency entry |
| `r.key` | the full Contract #1 6-segment link key `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>` | free — same |
| `r.data.<field>` | the link's own payload (`AttachObject`'s `filename`; `duplicateOf`'s `criteria`) | **one Core-KV point-read per dereferenced edge**, memoized per evaluation |

The payload read enters the evaluation footprint under the **link key**, so a
concurrent write to a projected link is caught by the same read-surface
validation a vertex or aspect read is. It adds no edge selector and no
`Fallback`: the adjacency read surface is the hop's, and a projection does not
change the hop. `WHERE r.data.<field> = …` is therefore admissible — it is a row
filter applied after the expansion, not an edge filter the walk honours, so
`recordEdgeSelector` still certifies the same footprint.

**The two halves come from different stores, and they can disagree.** `type(r)`
and `r.key` are read out of the ADJACENCY document; `r.data.*` is a point-read
of Core KV. Adjacency lags Core KV, so in the window after a link is deleted the
walk still crosses the edge (identity facts present) while the payload resolves
null — `objectAttachments` projects `{ownerKey, linkName, filename: null}` for a
just-detached document until the adjacency catch-up removes the edge, and the
row disappears then. Reading the link envelope for the identity halves would
close the window and destroy the property that makes them free; a consumer that
cannot tolerate the tear should treat a null payload on a present entry as
"in flight", not as "no filename".

**Naming a relationship changes what a row IS — even when the variable is never
read.** Two links between the same endpoint pair (an object attached to one
owner under two slots) are two rows, where an anonymous hop yields one row per
endpoint. So adding a name to an existing `-[]->` is a cosmetically-null edit
that multiplies every aggregate in the lens: a `count()` doubles, a `collect()`
gains duplicate entries, and a grouping key that never mentioned the
relationship still partitions the same rows. Check the aggregates before adding
a name. A relationship variable reused in a later clause means the same link,
exactly as a reused node variable means the same node; an `OPTIONAL MATCH` that
finds nothing binds it to null, so all three forms are null and never an error.

Everything else about a relationship is refused at **parse**, because it would
otherwise project a column of silent nulls with no diagnostic
(`ruleengine/full/relbinding.go`):

- a relationship variable on a **variable-length hop** (`-[r:x*1..3]->`,
  `-[r:x*0..]->`) — an expansion of several hops crosses no single relationship,
  and a zero-hop one crosses none at all;
- a **dereference other than `.key` or `.data`** (`r.localName`, `r.class`) —
  the key is already projectable and the rest of a link's envelope is provenance
  no lens reads, so it is refused rather than served;
- a **bare use of the variable** — `RETURN r`, `count(r)`, `collect(r)`, `r` in
  a grouping key. A relationship is bound for its identity and its payload, not
  as a value: rendered it is an empty object, and counted it changes what the
  aggregate means;
- a **reference to a name a `WITH` stopped carrying**, which is unbound there
  and would read as null for the rest of the query.

The refusal is judged over every position the executor evaluates an expression
from — a clause's `WHERE`, its projection items, and its patterns' inline
property maps — and it follows the BINDING rather than the name, so a `WITH`
that carries a relationship under another name carries the rule with it.
`resolveProperty` applies the same projectable-property rule at evaluation and
ERRORS on anything else, so a shape that reaches it by any other route fails
loudly instead of serving a link's envelope.

### Engine selection algorithm

1. `LensSpec.engine` field is inspected at spec load time (in `translateSpec`)
2. `ruleengine.Registry.SelectForLens` is called with the spec's `RuleEngine` string:
   - `"full"` or `""` (absent) → full engine
   - any other value → `SelectionError` ("unknown engine")
3. `Rule.ResolvedEngine` is set to `"full"`; `Rule.CompiledRule` holds the compiled AST
4. At runtime, `startPipeline` calls `UseFullEngine` to wire the pipeline's evaluate path

### Levenshtein UDFs (full engine)

The full engine's cypher executor (`ruleengine/full/executor.go`) provides two
pure, deterministic, side-effect-free string UDFs:

- `levenshteinDist(a, b) -> int` — classical Wagner-Fischer edit distance, O(N²)
- `levenshteinRatio(a, b) -> float` — normalized similarity in [0, 1]

The identity-hygiene Duplicate Candidates Lens uses them to score
near-duplicate identities.

---

## Lens lifecycle

1. **Lens definition arrives** via Core KV mutation on `vtx.meta.<NanoID>` (vertex with `class: meta.lens`) + a `.spec` aspect
2. **`CoreKVSource`** consumes `vtx.meta.>` and `lnk.meta.*.subtypeOf.>` via one durable consumer; routes vertex entries with class `meta.lens` to the spec parser, and separately accumulates `meta.ddl.vertexType`/`canonicalName`/`subtypeOf` events into the dynamic type taxonomy (dynamic-type-taxonomy-design.md §6.1). CDC ordering is not guaranteed — if the `.spec` aspect arrives before its parent vertex, it is buffered in `pendingSpecs` until the parent vertex's class is observed
3. **`translateSpec`** converts `LensSpec` → `Rule`; engine selection via `Registry.SelectForLens`; `CompiledRule` populated
4. **`startPipeline`** (in `cmd/refractor/main.go`) constructs the adapter (opens the target KV bucket / Postgres table), wires a `pipeline.Pipeline`, installs a `LatencyRingBuffer`, launches a `health.Reporter`
5. **The pipeline's `substrate.ConsumerSupervisor`** (built in `pipeline.Pipeline.RunOn`, configured from `cmd/refractor/main.go`) creates a durable JetStream consumer (durable name `refractor-<ruleID>`) on the `KV_core-kv` backing stream filtered to the lens's source-key prefix
6. **Each CDC event** → `pipeline.Pipeline.HandleMessage` → engine evaluates → projection row(s) emitted → `EnvelopeFn` wraps row → adapter writes to target
7. **Latency** tracked in `pipeline.LatencyRingBuffer` (128-sample ring buffer, thread-safe). Per-mutation health signals via `LatticeHeartbeater.LensLatencyProvider`
8. **Lens spec update** → `CoreKVSource.updateCB` fires; `ClassifyUpdate` determines the reload: an `INTO`-only change hot-reloads the adapter in place (`IntoOnly`), while a `MATCH` (query) change requires a full pipeline rebuild (`MatchChange`). Both kinds first run one **refusal set** (`cmd/refractor/reload.go`): `grantTable`, `protected`, `secureColumns`, a lens's authorization plane, or — for a guarded lens — its write surface. Those are refused because each leaves something a restart does not put right (grant rows no producer addresses afterwards, an RLS posture no swap re-verified, a decrypt set deciding which ciphertexts open), so how to reconcile it is an **operator's** call, not a package upgrade's — which is why the reason names an operator action. A refused update is recorded as an error on the lens's health entry and the lens keeps serving its **activated** spec; it is not paused, because a lens running its activated spec is doing the right thing.

    A change to the **`Output` descriptor** — or to `projectionKind`, which decides whether that descriptor is installed at all — is not in that set. The envelope, delete-key derivation, sweep plan and guard predicate are installed only by activation, so Refractor **re-activates the lens in place** (`reloader.reactivate`), and the rows the edit leaves behind are reconcilable without a human. In order:

    1. **Pre-flight**, so a malformed edit leaves the OLD lens running instead of stopping it for an activation that was always going to refuse: **both** the running lens's target and the edited spec's must be `nats_kv` — the Output descriptor's only home, since the §6.2 write guard, the per-entry `PrefixKeyLister` listing and the `RowReader` read-back are all NATS-KV capabilities — and the descriptor must compile and satisfy the cap-read producer closure. Requiring the OLD target too is what keeps the purge in item 3 off a target it must never clear: a protected Postgres adapter answers yes to every question the purge asks (`NewProtectedAdapter` forces the §6.2 guard, so the purge is *forced* whatever the caller requested; it implements `Truncater`; and with no key prefix it counts as owning its table outright), so a `projectionKind` flip **out of** `actorAggregate` — which skips every new-rule descriptor check — would otherwise truncate a live RLS table.
    2. **Deactivate** through the same `remover`/`pipelineDeleter` a tombstone drives — and **abort** if it reports a failure, because the durable removal fails *before* the run context is cancelled, so a failed teardown leaves the old pump alive and a replacement would double-write the lens. `Run` having returned is then asserted, not assumed.
    3. **Purge** the target when the replay cannot overwrite what is stored — the key shape moved, or the new `emptyBehavior` is `skip` — and unconditionally when the target is **guarded**, whose watermark declines an equal-seq replay. A non-`nats_kv` target never reaches this step at all: item 1 refuses the whole re-activation rather than leaving the purge to a later flag, because a guarded target's purge is forced and no downstream decision could stop it. It also requires the target to be truncatable, and stays within what the lens **owns**: `projection.ApplyTruncateScope` binds both the key prefix *and* the descriptor's own `OwnsKey` test, because one lens's prefix contains another's (`cap.` covers `cap.ephemeral.`, `cap.svc.`, `cap.roles.`, `cap.role-by-operation.`) and a prefix-only purge on the shared capability bucket would take four sibling producers' rows with it. `OwnsKey` is `AnchorFromKey` plus one shape: a perEntry lens's own pre-flip legacy parent document, which it wrote itself and which no sweep claims. A purge that fails is recorded and the activation still runs.

        The one direction the purge cannot serve is a descriptor **arriving** over a plain lens. The purge runs on the OLD adapter, and `ApplyTruncateScope` derives a key prefix only for an actor-aggregate rule — so a plain lens's adapter is unconfined, the purge is declined, and the rows it wrote stay where they are (the same outcome a tombstone or a restart leaves). Scoping off the new descriptor would not reach them either: they carry a key shape it does not claim. Refractor logs a Warn naming the consequence.
    4. **Activate** from the new spec. The fresh durable starts at `DeliverLastPerSubject`, so the whole current corpus is re-projected in the new shape rather than only events arriving from now on.
    5. **Post-check**: a lens that is neither registered nor queued for a taxonomy retry is dark, and is recorded as an error *and* an `infra` pause. The pause is the state — without it `RecordError` re-creates the entry the teardown deleted at its `active` default, so a lens with nothing running would read healthy. `infra` rather than `structural` because of how each is SERVED: the supervisor's pump runs a probe loop for an infra pause and `waitWhilePaused` for a structural one, so a structural pause would be held until an operator issued `resume` and a restart would no longer recover the lens. The remedy the entry names — restart Refractor, or fix the spec and reinstall — then works: the next activation restores the pause, probes the target, passes, resumes, and `SetActive` clears the diagnosis with it.

    **A refusal only reaches the operator who caused it if something tells them:** lens IDs are version-independent, so a package upgrade edits a spec in place and `lattice-pkg apply` commits successfully. `internal/refractor/reloadpin` restates the spec-derived half of the refusal set over the stored document — `grantTable`, `protected`, `secureColumns`, and deliberately not `output`, which Refractor applies itself — and the installer runs it at diff time so an apply that lands a non-hot-reloadable lens edit reports `ReactivationRequired` naming the lens and the remedy. Refractor stays the authority — `reloadpin` only predicts, so drift costs a missing warning rather than a wrong refusal, and `TestPinnedFieldsMatchTheRefusalSet` pins the two together

9. **Lens tombstone** (parent vertex deleted or `.spec` deleted) → `CoreKVSource` purges its spec-tracking maps, logs the removal, and invokes its removal callback. `CoreKVSource` itself fires that callback only for a genuine tombstone, never for an in-place spec update (which drives `updateCB` instead) — but the removal MECHANISM has a second trigger: step 8's re-activation drives the same `remover` (through its `stop` half, which returns the teardown's outcome instead of logging it), so a tombstone and a re-activation tear a lens down identically. The log line names which (`trigger`). `cmd/refractor` wires that callback to the same `control.Deleter` the operator `delete` control op uses (`internal/refractor/control`'s `RegisterDeleter`): the durable consumer is removed from the `KV_core-kv` stream (`pipeline.Pipeline.RemoveConsumer`, durable removed **before** the pipeline's run context is cancelled — see that method's doc for why the reverse order silently strands the durable), the pipeline is stopped, and its health KV entry is deleted. A sibling lens's durable and registration are untouched. Adjacency entries are left in place (tombstone re-projection is a Phase 3 carry)

---

## Refractor adjacency KV

The `refractor-adjacency` bucket is Refractor's internal secondary index for
graph traversal. It is built and maintained exclusively by Refractor; no other
component writes to it.

| Property | Value |
|----------|-------|
| Bucket name | `refractor-adjacency` |
| Builder | `consumer/bootstrap.go` (`Bootstrapper.Run`) — processes every `lnk.*` CDC event via `adjacency.EventsForLink` + `adjacency.Build` |
| Entry shape | One document per node: `adj.<nodeId>` → `{"edges": [EdgeEntry, ...]}`. Each edge on a node is one `EdgeEntry` (`Direction`, `Name`, `OtherNodeID`, `OtherType`, `EdgeID`); a link contributes one entry to each of its two endpoints' documents — an outbound entry on the source node's document, an inbound entry on the destination's |
| EdgeID | == link key; consistent across adjacency + Core KV |
| Purpose | Inbound/outbound-link lookup index for the cypher executor (graph traversal without a global scan) |

Within Refractor the adjacency index is consumed directly by the cypher
executor for inbound- and outbound-link enumeration without a global `lnk.*`
scan.

### The overflow latch

Every edge on a node lives in that one node's document, so a node whose
degree grows without bound eventually produces a document the NATS payload
ceiling refuses to accept: the write fails permanently, the failing message
redelivers forever, and every read of that node keeps returning the last
document that did fit — a frozen, silently wrong edge set. The overflow
latch (`internal/refractor/adjacency/overflow.go`) is the structural answer.

| Property | Value |
|----------|-------|
| Mark key | `adjmark.<nodeId>`, same bucket, a distinct key prefix from `adj.<nodeId>` so an older binary that has never heard of the latch cannot see it, let alone unmark a node by rewriting the document |
| Thresholds | `3,072` edges **or** an `800 KiB` marshaled document, whichever comes first — entries are variable-length, so neither threshold bounds the other |
| Latch effect | The `Build` call that carries a node past either threshold creates the mark (create-tolerating-conflict, so the several concurrent writers of one node's edges converge on one mark instead of racing), best-effort empties the node's document to reclaim the space, and becomes a no-op for every later event on that node — an added edge is not absorbed and a retracted one is not removed, because Core KV is now the node's authoritative record |
| Read effect | `Neighbors` reads a node's document and its mark in one batched `KVGetMulti` call, so a node latching mid-read can never present the just-emptied document as a complete answer. An unmarked node's read is that same two-key batched request, returning the document's KV revision as the fingerprint. A marked node's read enumerates Core KV's `lnk.*` keyspace directly for both directions, drops soft-tombstoned links, and returns a fingerprint hashed over the matched `(key, revision)` set in place of a document revision. A caller that can name the relations it will follow takes `NeighborsScoped` instead: one node-state read decides the shape, an unmarked node still answers whole (a document is one key however many relations are asked for) and a marked node's Core KV enumeration is filtered to those relations by subject. The returned `whole` flag says which happened, because a scoped fingerprint covers only what that read matched and is never comparable with a whole read's |
| Freshness | A marked node's read is commit-fresh: Core KV holds a link the instant its write commits, so a marked node needs no help from the pipelines' link pre-apply (below) to see an edge that triggered the read. An unmarked node's document can still lag its own triggering CDC event by one write, which is exactly what the link pre-apply closes |
| Cost | An unmarked node's read is a two-key batched request (the document plus the mark), roughly 2× a plain single-key `Get` (a multi-key request measures near 306 µs against a single `Get`'s 153 µs) — the same cost two sequential `Get`s would pay while also being non-atomic, so batching is the cheaper of those two ways to consult a mark on the read path, not a free one. A marked node's read is a further, larger Core KV enumeration: slower still, but complete, where a jammed write and a frozen, wrong edge set are not |
| Lifetime | Created the first time a node's `Build` carries it past either threshold; never lifted — a node whose degree later shrinks keeps paying the fallback read rather than risk reintroducing the jam on its next growth spurt. Durable across restarts and reconnects (it is ordinary KV state); re-latched deterministically by the Bootstrapper's replay, which re-crosses the same threshold on the same edges; wiped only when the bucket itself is wiped |

### Link fan-out on the capability pipeline

Most lenses only project on **vertex** CDC events; `pipeline.processMsg`
ack-and-skips link and aspect events. The capability pipeline is the exception:
it has an `ActorEnumerator` installed, and a pure link mutation (e.g.
`holdsRole`, `grantedBy`) changes an actor's authorization with **no
accompanying vertex change**. So on the actor-aware pipeline a `KindLink` CDC
event — create *and* tombstone (revocation) — drives a fan-out reprojection
(`evaluateLinkFanOut`): the link key is parsed into its two endpoint vertices,
affected actors are enumerated from **both** endpoints (union), and each is
reprojected through the same per-actor machinery as the vertex fan-out
(`reprojectActors`).

Because the dedicated adjacency `Bootstrapper` and the capability pipeline both
observe the same link event with no cross-consumer ordering guarantee, the
pipeline first **idempotently applies the link to the adjacency KV itself**
(via `adjacency.Build`, mirroring the bootstrapper's two directional events,
keyed by the link key as EdgeID) before enumerating. This guarantees the
reprojection cypher sees a consistent edge set and never races ahead of the
edge that triggered it; the bootstrapper's later `Build` for the same edge is a
no-op. A link whose endpoints reach no actor (e.g. a `book → author` link)
enumerates to the empty set and is a correct no-op.

#### The walk is scoped to the lens's own pattern relations

The enumeration is not an undirected crawl of everything adjacent. Standing on a vertex of
type `T`, the walk follows only the relations of pattern hops incident to a pattern position
that admits `T` (`pipeline.walkScope`, derived per rule publication from **every** compiled
branch's `AnchorHopIndex`; refractor-hub-walk-and-periodic-load-design.md §5.1). The
argument is the pattern's: an actor whose row depends on the event vertex is joined to it by
a path of pattern hops through positions admitting each intermediate's type, and the walk
runs that path in reverse.

Without it the walk crosses Contract #1's `instanceOf` descriptor edge. A type descriptor
holds one link per instance of that type, so one event on one instance expanded every
instance of that type — and then every actor attached to any of them — turning a single
service write into a whole-tenant reprojection through an overflow-marked hub.

Two consequences worth stating:

- Where the relation set at a vertex type is finite, the adjacency read is scoped to it
  (`adjacency.NeighborsByRelation`), so an overflow-marked hub's Core KV enumeration is
  filtered by relation rather than drained whole. An **empty** set — a vertex type no
  pattern position admits — costs no read at all. The fixed `reportsTo` hierarchy hop takes
  that same scoped read unconditionally, since it never followed anything else.
- The walk therefore no longer reprojects an actor merely because an out-of-pattern
  neighbour changed. That incidental reprojection used to heal a row lost out of band, so
  the scope carries auth-plane-projection-latency-design.md §4.2's second conjunct as well:
  **a lens gives up the accident only where a standing healer will repair the row**. Two
  healers count, one per plane — the convergence sweep below (`p.sweeper`, installed by
  `InstallActorAggregate`'s enrolment gate, which may refuse warn-only) and the personal
  plane's `grantchange.PersonalSweeper` plus the D1 grant-change edge (recorded on the
  pipeline by `cmd/refractor` at its `grantReprojector.RegisterPersonal` call, since a
  Personal Lens never receives a `SweepPlan`). An actor-aware lens with neither keeps the
  relation-blind walk. The healer half is read per event, not snapshotted, because both
  arms are installed after the rule is published.

Every shape the scope cannot resolve keeps the relation-blind walk unchanged: no standing
healer, a non-full engine, a branch that is not a compiled full rule, a pattern graph that
is `Incomplete`, an unresolved `*` expansion, or an untyped relationship at an unlabeled
position — the install-level refusals reported ahead of the cypher-level ones, since they
hold for the life of the wiring. The affected-anchor derivation reads that same pattern
graph and does *not* refuse an untyped relationship — it walks the hop as a wildcard,
admitting every relation — so on such a lens the two arms disagree by design: the scope
narrows a walk, the derivation replaces one. The per-lens verdicts are pinned by the corpus census
(`internal/refractor/actor_walk_scope_corpus_census_test.go`), which supplies the same
install production does and records which healer arm each lens landed on; the act-mode
tally line carries `walkScoped` so an operator can see which posture a lens is running.

**The way back is `REFRACTOR_WALK_SCOPE=off`**, which puts every lens on the relation-blind
walk and reports `disabled by operator` through `WalkScopeRefusal()`, the tally and the
census alike. It is a third switch, separate from the two beside it:
`REFRACTOR_ANCHOR_DERIVATION=off` restores the *enumerator*, which is itself scoped, and
`REFRACTOR_ACTOR_PEER_ANCHORS` governs only events on a lens's own actor type — so a full
rollback to the pre-scope walk needs this knob as well. Like the others it bounds the next
event and heals nothing already stale; that is `lattice lens rebuild`'s job or the sweep's.

**The derivation is the arm this scope is the fallback for, and the personal plane now reaches it.**
`derivationIndexForAct` refused every personal lens on two conjuncts — `patternClosedOutput`, which
no personal lens sets, and a healer test that read `p.sweeper` alone, which a Personal Lens never
receives. Both are now answered: the healer conjunct reads `standingHealerInstalled()` (one arm per
plane), and the pattern-closure conjunct is a disjunction with the personal narrowing licence
described in the Personal Lens section above. A licensed personal lens derives its affected anchors
from the compiled pattern instead of walking outward from the event; every one it refuses keeps the
scoped enumerator exactly as it is. The per-lens static verdicts are pinned by the corpus census
(`internal/refractor/personal_derivation_corpus_census_test.go`), which pins the declaration and the
cypher-level conjuncts — the process-level ones cannot be censused and are held by
`TestPersonalDerivationLicence_Conjuncts`.

One residual the scope does **not** remove: `byType` is keyed by vertex type, so a lens
whose own pattern binds `instanceOf` between two `service` positions — `edgeInstances`,
`edgeProviderQueue`, `edgeManifestProviderReadGrants`, `capabilityServiceAccess` — still
follows that relation at every service, and so still expands the type descriptor. No
per-type scope can separate "instance → its template" from "template → its other instances"
when both ends are the same type; the position-directed affected-anchor derivation is what
does, and it is the arm this scope is the fallback for.

#### The full engine's own traversal reads a marked hub by the hop's relation

The scope above narrows which relations the *enumeration* follows before an evaluation
runs. Inside the evaluation, the full engine's executor applies the same idea to its own
reads: a **typed** relationship hop standing on an overflow-marked node reads that node at
the hop's relation (`adjacency.NeighborsScoped`) instead of draining the hub's whole Core KV
link keyspace, so a lens that follows one relation of a hub's thousands pays for one
relation. An **unmarked** node still answers whole and is memoized whole — its document is
one key however many relations cross it — so nothing about the ordinary node changes. An
untyped hop reads whole either way, since it consumes every edge regardless of type.

A hub read is memoized per `(hub, relation)` for the life of the evaluation, so repeatable
read holds per key exactly as the whole memo holds it per node. It is footprinted as its
**Matched sets** and never as a fingerprint: a scoped fingerprint is not comparable with a
whole read's, so such a node carries no `EdgeRevisions` entry and the validator re-reads it
at the same relation scope (below).

The relation is the memo key, and it stays the key even once the same node is read whole —
by an untyped hop, or because the node stopped being marked mid-evaluation. Such a read is
**composed** against the relations already pinned: each keeps the edges the hop that first
crossed it saw, and only the rest of the node comes from the later read, so every hop's view
of a relation is the first hop's view of that relation whatever order the hops arrive in.
Because a scoped read answers in both directions, the composition footprints each relation
it substituted under a **both-direction selector** of its own — the pin validation re-derives
to catch a link arriving on a direction no hop walked, which the whole-read fingerprint,
taken at the later instant, cannot see.

**`REFRACTOR_HUB_READ_SCOPE=off`** restores the whole read — every typed hop drains a marked
hub again, and its footprint carries the whole-read fingerprint. Like the switches beside it,
it is a containment lever for an operator who believes a lens is missing edges, not a posture
to deploy in, and it bounds the next event rather than healing a row already stale.

### Convergence sweep

Both mechanisms above order and correct writes that **happen**. Neither can conjure a
write that never did: a grant landing while the pipeline is not consuming — a restart
window, or the multi-ten-minute drain after a source stream is recreated — leaves
`cap.*.<actor>` physically absent, surviving restarts, with nothing re-driving it. Zero
consumer lag is not converged truth.

The same hole exists off the auth plane, and arrives by a second route: adding a walk to a
lens reprojects nothing already stored, so an actor's row refreshes only when a CDC event
next happens to touch it.

So every **actor-aggregate** lens runs a periodic self-audit beside its pump
(`pipeline.RunSweep`, `internal/refractor/pipeline/sweep.go`;
capability-projection-reconciliation-design.md §3.2, generalized by
lens-projection-liveness-design.md §15). No other lens kind gets a `SweepPlan` — a plain
lens retracts through filter/diff retraction — but the **Personal Lens** now has a
convergence walk of its own, run by a different mechanism for a reason the enrolment gate
below makes plain: it cannot enumerate its target's keys, so it can never take the orphan
direction the `SweepPlan` sweep is built around. Its keyset frame supplies that direction
from the other end instead (`grantchange.PersonalSweeper` — see the D1 read-grant change
edge above).

Enrolment is gated on the lens being able to **name its own rows**, on three counts. A
target is shared — one `weaver-targets` bucket holds a dozen lenses' rows — so the sweep
enumerates only keys under the lens's own key-pattern prefix (`OutputDescriptor.KeyPrefix`,
which becomes a NATS subject filter, so it must end on a segment boundary and carry no
wildcard token); the pattern must **round-trip** (`BuildKey` → `AnchorFromKey` → the same
anchor), since the grammar permits shapes where it does not and the orphan direction then
claims nothing while looking exactly like a lens with nothing to claim; and the target
adapter must implement `adapter.PrefixKeyLister`. A lens failing any of the three gets **no
sweep at all** rather than an unscoped listing of a target it shares — the alternative is
not a degraded sweep but one that faults every tick, reporting a lens as unrepairable
rather than as unswept. `metrics.lensLiveness.<lens>.sweepEnrolled` carries that decision,
so a lens running without its only stale-row detector is visible rather than merely logged
at activation.

Scoping *narrows*: the prefix is the same literal `AnchorFromKey` matches first, so it can
only omit keys the ownership test would have rejected — and since one lens's prefix can be
another's ancestor (`cap.` and `cap.roles.`), that exact ownership test still runs on
everything the listing returns.

An auth-plane lens sweeps every 60s; a business lens every 5 minutes
(`projection.BusinessSweepInterval`). The asymmetry is the one the health path already
states: an unhealed capability document is an authorization failure, a stale business read
model is one vertical's outage. It also keeps the cell's steady-state reprojection load at
roughly what the three auth-plane lenses already cost, rather than multiplying it by the
lens count. Each lens's first tick is offset by a hash of its rule ID, so the lens set does
not enumerate and reproject in one burst per interval; the offset is derived rather than
drawn so a lens keeps its slot across restarts.

The coverage-prefilter listing (the two key listings below) is itself cached across ticks: it
is only re-taken when the pipeline's applied Core KV sequence or its own write count against
the target has moved since the last one, with a forced re-list every 30th pass as insurance.
On an idle cell where a full round-robin lap of the deep verify has healed nothing and found
no divergence, the deep verify itself then runs only every 5th tick (`pipeline.IdleSweepBackoffEvery`)
instead of every tick — any movement in either signal reverts a lens to every tick immediately.
A skipped tick never advances the sweep's own liveness clock, only a real pass does, so the
back-off cadence is pinned well inside `CapabilitySweepStalled`/`LensSweepStalled`'s staleness
window (`health.DefaultCapabilitySweepStallCycles`, 10 sweep intervals by default): a real pass
still recurs every 5 minutes at the 60s auth-plane interval against a 10-minute stall window, and
every 25 minutes against 50 at the 5-minute `BusinessSweepInterval`.

- A **coverage prefilter** compares two key listings, the lens's anchor-type vertices in
  Core KV against the target's live keys, and points at both directions: an anchor with no
  target key (the lost first projection) and a target key whose anchor is gone (an
  over-grant). A tombstoned anchor legitimately has no key and is excluded, or the
  accumulated tombstone set would starve the walk below.
- A **bounded round-robin deep verify** (default 25 anchors per 60s tick, both
  overridable) re-executes the projection from a persisted cursor. This is the only
  detector for a row that is present but *stale* — the over-grant the prefilter cannot
  see, since a revoked actor keeps both its vertex and its key.
- **Neither prefilter direction is a decisive divergence, so both are priority hints that
  earn their share of the batch.**
  - "An anchor has no row" is definite only for a lens that projects one row per anchor. For
    a lens whose match *filters* it is the steady state — true on the auth plane itself,
    where an identity holding no unexpired task grant correctly has no
    `capabilityEphemeral` row and one holding no role correctly has no `capabilityRoles` row.
  - "A target key has no live anchor" is not proof of a row to retract either. Core KV
    deletes are logical, so a **departed identity keeps its vertex key** and stays in the
    anchor listing; its stale row is therefore *not* an orphan, and the deep verify is what
    detects that over-grant. The orphan hint's real triggers are a physically purged anchor,
    a row written for an anchor that never existed, and a transiently short anchor listing.
- **Each hint rotates from its own cursor, and a reserved share alone is not enough.** A hint
  whose candidate set stays populated forever must also *reach every member of it*, or an
  individual divergence starves while the direction's slot count looks perfectly healthy.
  This bites hardest on the orphan hint: an auth-plane target is always guarded, so
  retracting a row writes a **soft tombstone** that remains a live NATS-KV key and keeps
  appearing in the target listing while reading as absent. A retracted orphan therefore never
  leaves the set, and a hint that always walked its sorted head would re-verify the same
  already-retracted keys every tick while a genuinely stale row further down the order was
  never reached. Neither hint cursor is persisted (the deep verify's is — it carries the
  completeness guarantee), so the rotation is **restart-scoped**: a cell that redeploys faster
  than a full rotation re-walks from the head.
- **A hint that heals nothing is capped at its floor.** After
  `hintMissesBeforeFloor` *consecutive* passes in which a hint selected candidates and none of
  them wrote, it keeps only a reserved floor and the slots pass to the other directions. Two
  passes rather than one because a single unproductive pass is a transient: either key listing
  can come back short, and a row landing mid-pass reads as missing and then as correct. A pass
  whose candidates *errored* is no evidence in either direction, so an erroring lens is never
  mistaken for one whose premise is false; one write clears the record outright; and the full
  share is restored every `hintRetestPasses` regardless, so a record formed from a transient
  cannot hold for the life of the process. Each transition is logged, since a demoted detector
  is otherwise invisible.
- **Each direction holds a reserved share the others cannot consume**, because two of them are
  the *only* detector for what they see: the deep verify for a present-but-stale row, and the
  orphan hint for a row whose anchor is gone (the deep verify walks anchors, and an orphan's
  anchor is by definition not among them). A direction that can be starved indefinitely is a
  detector that can be silently switched off. Each share is sized to the work that exists, so
  a direction with nothing to do hands its slots on: a lens with no orphans leaves the
  coverage hint the whole prefilter, and a listing with no anchors leaves the orphan hint the
  whole batch. Below three slots a batch cannot seat three directions at all — the default is
  25 and nothing in production overrides it.
- Repair reuses the `reproject` verb's path exactly (§3.1), so reconciliation has one
  write path and one ordering token: the pipeline's last-applied stream sequence, captured
  before evaluation, which always loses to a later real CDC event under the §6.2 guard.
  Skip-if-identical makes a converged pass cost **zero writes**.
- Each heal increments `metrics.capabilityLens.<name>.reconciled` and raises
  `CapabilityCoverageDivergence` (warning; `error` once two consecutive passes each heal
  something). Healing is deliberately loud — a nonzero rate is the signal to go find the
  delivery gap.
- A repair the sweep could **not** write raises `CapabilityRepairFailing` instead — nothing
  on one failing pass (the immediate retry usually clears it), `warning` at two consecutive,
  `error` at three; `metrics.capabilityLens.<name>.failingActors` is the gauge and `alert`
  reads `repair-failing`. The two verdicts are separate because a failed repair heals
  nothing, so keyed on the heal count alone an unwritable row clears the divergent streak
  and leaves an active, caught-up lens reading as converged. A failing anchor retries next
  pass, then backs off by doubling to a sixteen-pass ceiling, yielding its batch slot to the
  next divergent anchor — the backoff suppresses the retry work, never the signal. An actor
  that leaves both listings is reaped, so a departed anchor cannot pin the alert open.
  Pass-level faults (unreadable survey, a tick abandoned before it verified anything) raise
  it too.
- The sweep suppresses itself while a rebuild is in flight (a rebuild is a superset), while
  the lens is paused (operator intent wins), and — fail-closed — whenever its own health
  entry is unreadable. A suppressed tick reaches no verdict, so it records the *reason*
  (`metrics.capabilityLens.<name>.sweepSuppression`) and deliberately leaves the liveness
  clock (`sweepLastPassAt`) aging: every verdict above describes the last pass that ran, so
  a sweep held indefinitely republishes a converged one forever. Past **10 sweep intervals**
  with no verdict the heartbeat raises `CapabilitySweepStalled` and `alert` reads
  `sweep-stalled`: `error` at once when no *fresh* suppression reason explains it (the sweep
  should be ticking and is not — a reason older than ~2 intervals describes a tick that
  already ended, so it explains nothing), `warning` escalating to `error` at 3× the window
  when a cause is named, and `warning` while the lens is `rebuilding` — a rebuild supersedes
  the sweep, and its own duration is not this detector's verdict. A rebuild that has stopped
  **draining** does escalate to `error`: `watchRebuildCompletion` records the un-drained count
  and when it last *decreased*, and a rebuild that has not drained a message within the same
  staleness window is stuck rather than slow, superseding the sweep with nothing. A rebuild
  still draining is exempt however long it runs. `rebuildProgressAt` is stamped when the
  rebuild's window OPENS, so a started rebuild is never "unknown": a zero value means only
  that this process has started no rebuild on the lens. While a rebuild is in flight the metric
  carries `rebuildOutstanding` and `rebuildProgressAt` (dropped once it finishes, so a stale
  final count is never published as a stuck one). A poll that keeps erroring records no
  progress deliberately — that retry is unbounded, so an error that never clears ages from the
  window's open and reads as wedged, which is what keeps a personal lens's silent rebuild
  window observable. A
  paused lens is exempt — already an error in its own right — and the exemption re-baselines
  the clock, so a resume does not read as stalled for the length of the pause. The cursor and
  heal count persist on the lens's existing health entry, so a restart resumes rather than
  restarts the walk.
- A lens whose liveness inputs cannot be read is reported as `status: "unknown"` /
  `alert: "unreadable"` with `consumerLag: null`, and raises `CapabilityLensUnreadable`
  (warning — the observation path failed, not necessarily the lens). It is never dropped from
  the snapshot: an auth-plane lens absent from `metrics.capabilityLens` is indistinguishable
  from one that was never installed. Its sweep verdicts still apply, since those come from the
  in-process sweeper rather than the health entry.

### The plain-lens divergence audit — a correctness verdict that never repairs

The sweep above covers actor-aggregate lenses, and its detection is a **byproduct of its
repair**: `healed` counts writes that landed, so a divergence with no repair transport
produces no write, no heal, and a cleared streak. The plain corpus had neither half — no
sweep, and therefore no per-row correctness signal at all: `lastProjectedAt`,
`projectionLag` and `alert` answer *"is this lens still moving?"*, and nothing answered
*"is what it already wrote still true?"*. A frozen row rendered green.

So an enrolled **plain** lens runs a periodic divergence audit beside its pump
(`pipeline.RunAudit`, `internal/refractor/pipeline/audit.go`;
lens-projection-divergence-audit-design.md §4.3). Every 15 minutes
(`DefaultAuditInterval`) it lists a bounded batch of its anchors from the persisted cursor
(`DefaultAuditBatch`, 10), re-runs the lens's own **seeded** evaluation for each — the D2
Phase 1 primitive that makes a per-anchor recompute cost one anchor's walk instead of a
corpus-wide scan — and compares each result against the stored row with
`rowsComparableMasked`, canonical JSON with the volatile envelope fields stripped, the same
definition of "same row" the sweep's `rowsEquivalent` / `Reproject`'s `classifyDivergence`
use, with two further exclusions per anchor: the row's own key columns, always (a freshly
computed row carries every RETURN alias, key columns included, while
`adapter.RowReader.GetRow`'s contract excludes them — comparing them would report a mismatch
no recomputation could ever resolve), and, for a Secure Lens, its declared secure columns
(secure-plain-lens-retraction-and-audit-design.md §4.1) — see below. `rowsEquivalent` itself
is untouched and stays the sweep's own comparator; a mask is never threaded there.
**It never writes to the
target.** Repair on a shared, unguarded plain target was rejected (§8.1): there is no
ordering token to keep a repair subordinate to a racing CDC event, a seeded evaluation
cannot prove it owns the keyspace it would write into, and coupling this detector to a
writer would rebuild the exact structure whose collapse produced the twelve-day silence.
The remediation path is the operator's control-plane `reproject` RPC and `Rebuild`.

Enrolment is fail-closed, and — unlike the
sweep's, which is decided once at install — **every one is re-checked at the top of every
pass**: they are all mutable pipeline state, so a hot reload could otherwise leave a lens
auditing under a shape it no longer has. A failed re-check self-suppresses with the reason
rather than auditing. The conjuncts, in order: **not on the auth plane** (`projection.IsAuthPlane` — `nats_kv` into
`capability-kv`, or a Postgres grant table), which is the only thing keeping a *plain-kind*
capability-bucket lens out (the actor-aggregate and `RowReader` conjuncts below cover every
auth-plane lens shipped today, but neither covers that shape, and a per-row verdict on the
authorization read model belongs to the plane that has an escalation ladder for it); a single
derivable anchor pattern
(`seedAnchorLabels`, exactly one label — a multi-walk lens has N anchors and one seed
cannot speak for all of them, and a `*` taxonomy anchor resolves to a subtype *set* one
key-type listing cannot enumerate); no actor enumerator and no envelope (an actor-aware
evaluation's anchor is the actor, so seeding it evaluates the wrong entity, and those
lenses are the sweep's); an `adapter.RowReader` target (without read-back there is nothing
to compare against, and an audit that cannot compare would report clean); and no `$now` /
`$projectedAt` reference — `$now` is wall-clock and
`$projectedAt` derives from the *event* vertex's provenance, a neighbour of the anchor on
the plain CDC path, so a seeded recompute supplying the anchor's own props can never
reproduce either and the lens would read divergent forever. That last conjunct reads
`full.CompiledRule.ReferencesParam`, whose **non-exhaustive answer is a refusal, never a
pass**: `(referenced=false, exhaustive=false)` means the walk could not rule the parameter
out, and treating that as an absence is the read-the-declaration-not-the-matcher mistake
the flag exists to prevent.

**The comparison excludes two classes of column before the mask is even reached.** The row's
own key columns, always: a freshly computed row carries every RETURN alias, key columns
included, while `GetRow`'s contract excludes them. And every column the STORED row carries that
the computed one does not — `PostgresAdapter.GetRow` reads `SELECT *` deliberately (a reader
with its own column list would go stale against the writer's), so a table column no RETURN
alias produces, a migration leftover most often, comes back with the row. The exclusion is
sound because the computed key set is exactly the alias set: the executor assigns every RETURN
alias unconditionally, so an alias that evaluated to null is present-with-null and never
missing, and a stored-only key can therefore only be a column the lens does not project.
Compared instead, such a column reads `stale` on every pass forever on a lens whose projection
is exactly right — a divergence no recomputation resolves and no operator action clears. The
exclusion is one-directional: a column the COMPUTED row carries and the stored one does not is a
real divergence and stays compared.

A **Secure Lens** enrols like any other plain lens: its recompute never calls the decryptor
(`evaluateForEntry` and the two actor fan-out handlers are the only callers), so its
computed row always carries the raw ciphertext envelope for a declared secure column while
the stored row carries the decrypted plaintext (or null). Rather than refuse the lens, the
comparison excludes those columns — `auditMaskedColumns` on the lens's liveness entry names
them, published as `[]` (never `null`) for an enrolled lens with none, and absent for a
refused one, the same rule `divergentRows` follows. Under the mask `missing` and `retained`
are exact — they are presence and key questions, not content ones — and `stale` is exact
over every **other** column; a masked column is simply unverified, never assumed equal or
diverged. A **`DiffRetraction`** lens also enrols: `executeFullForAudit` never calls
`applyDiffRetraction` (that function's only caller is the plain CDC path the audit does not
run), so its seeded evaluation is read exactly like any other plain lens's. Its
should-not-exist direction (`retained`) is narrower, though — it depends on
`AnchorProjectionKey`'s own read-free derivation, which most of this corpus's
`DiffRetraction` lenses decline (a non-partitioning key, most often), so those enrol with
`missing`/`stale` only; an absent `retained` on one of them reads as "not detected in this
direction," never as "clean."

A refused lens publishes `auditEnrolled: false` plus `auditRefusal`, runs no pass, and can
never read as audit-stalled — *not audited* must stay distinguishable from *audited,
clean*. Most of the corpus refuses by design, and the number is **observed rather than
asserted**: the Refractor logs an enrolment census at startup (enrolled, refused, and the
dominant reason) so a refusal that turns out to dominate is a grounded follow-on instead of
a guess.

Divergences are reported per class in `metrics.lensLiveness.<lens>.divergentRows` —
`missing`, `stale`, `retained` — carrying only the classes that fired, so a direction that
has silently stopped detecting reads as absent rather than as zero; `divergentTotal` is the
sum the `LensProjectionDiverged` issue and the `diverged` alert key on, at `warning`
severity at every magnitude. An anchor the audit could conclude nothing about is counted in
`auditUnverified` and is **neither clean nor divergent**. Coverage is stated rather than
implied: a pass covers one batch, `auditCycleCompletedAt` says when the walk last closed
over the whole lens, `auditListingSize` how large that lens's anchor type is, and
`auditCoverageBasis: "key-type"` names the real boundary — the executor also admits a vertex
whose *body* `class`/`label` equals the pattern label, and such an anchor is never
enumerated and never audited. That is under-coverage, never a wrong verdict, and it is
published rather than assumed away. Coverage claims are earned, not stamped: `auditCycleCompletedAt` and the
`auditCycle*` totals beside it are recorded only when a cycle actually compared anchors, so a
lens with none never earns a completion and its clean number stays visibly unsubstantiated;
and `auditCycleDivergentTotal` is what stops a finding self-erasing on the next pass, since a
pass that did not re-examine the divergent anchor reports zero.

Suppression (rebuild in flight, paused lens, unreadable
health entry, failed enrolment re-check) records its reason and leaves `auditLastPassAt`
ageing, so a held audit cannot republish a clean verdict forever. Both mid-pass conditions are
re-derived AFTER the batch as well as before it: a rule swap landing inside a pass makes every
comparison in hand a comparison against a retired rule (the sweep withholds its write for this;
the audit, having none, withholds the verdict), and a rebuild or pause starting mid-batch
leaves the remaining anchors compared against a target being truncated underneath them, which
would read as `missing` divergences that were never divergent. A hot-reload that MOVES the
lens's anchor is adopted rather than suppressed — the walk restarts under the new label with a
reset cursor — because `InstallAudit` runs once at activation and nothing re-invokes it, so
suppressing there would hold until the process restarted. past **10 audit
intervals** with no verdict the heartbeat raises `LensAuditStalled` and `alert` reads
`audit-stalled`. The cursor and the last completed cycle persist on the lens's own health
entry (`health.Reporter.SetAuditProgress` — the audit's only write anywhere), so a redeploy
resumes the walk instead of re-auditing the head forever while never reaching the tail.

---

## Capability KV envelope (Contract #6 §6.2)

Built by `internal/refractor/capabilityenv/`. The envelope wraps the cypher
RETURN row into the canonical Capability KV shape.

### Field set

| Field | Value |
|-------|-------|
| `key` | `cap.<actorType>.<id>` (constructed from the actor vertex key `vtx.<actorType>.<id>` via `capabilityKey`, which strips the `vtx.` prefix) |
| `actor` | Vertex key of the actor (`vtx.identity.<id>`) |
| `version` | `"1.0"` |
| `projectedAt` | RFC3339 **provenance** timestamp: the anchor actor vertex's `lastModifiedAt` (the committing op's timestamp per Contract #1 §1.3), bound into the cypher as `$projectedAt`. It is deterministic ("as-of input state") — replay/rebuild over the same vertex yields an identical value (no wall-clock churn). It is consumed only by monitoring + the Processor auth trace; the Processor does not compare it against any freshness ceiling. |
| `projectedFromRevisions` | Map of `{actorKey: revision, lensDefKey: revision}`; recorded as projection provenance and surfaced in the Processor auth trace (planes 2+3). Not a freshness gate. |
| `lanes` | `["default"]` (multi-lane projection is Phase 3) |
| `platformPermissions` | Array from cypher RETURN; `[]` if absent |
| `serviceAccess` | Array from cypher RETURN; `[]` if absent |
| `ephemeralGrants` | Array from cypher RETURN; `[]` if absent |
| `roles` | Array from cypher RETURN; `[]` if absent |

The `capabilityKey` derivation is actor-type-agnostic: any `vtx.<type>.<id>`
actor key projects to `cap.<type>.<id>`, so non-identity actor types (e.g.
service actors) are supported without code change.

### Read-set certificate vs. `projectedFromRevisions`

`projectedFromRevisions` (above) is **post-hoc output provenance**: it scrapes
graph-shaped key strings out of the already-computed RETURN row and re-reads
their revisions *after* evaluation, for monitoring and the Processor auth
trace — a third instant over output keys, not a correctness mechanism.

A separate, unrelated certificate exists purely inside one evaluation: the
full engine's `ExecuteWithFootprint` (`internal/refractor/ruleengine/full`)
returns, alongside the RETURN rows, every Core KV key and adjacency node the
evaluation actually read, each paired with the revision observed. For an
actor-aggregate lens whose target is auth-plane (`projection.IsAuthPlane`)
AND whose compiled cypher emits at least one multi-binding conjunct unit
(`projection.hasMultiBindingConjunctUnit`, evaluation-consistency-design.md
§13.3 — a single-binding auth-plane lens like `capabilityRoles` is exempt by
construction, since a torn *set* of individually-real entries is ordinary
bounded staleness, not a fabricated combination), `pipeline.executeFullForActor`
re-reads that footprint immediately after evaluation and before the row
reaches the envelope/adapter.

Each adjacency node is re-read at the scope the footprint names. A node crossed
only by typed hops is re-read at exactly those relations and compared by matched
edge identities, so a write to an unrelated relation on a shared hub is not
drift — for a marked hub read at the hop's relation those identities are the
node's whole footprint, since no comparable fingerprint exists. A node an
untyped hop crossed is compared by whole fingerprint **and** by the matched sets
of any typed hops that preceded that hop on it, and by the both-direction pin
recorded for any relation folded into that whole read: those observed their
relation at an earlier instant than the whole read, and only re-deriving them
catches a write that landed in between. A node on that coarse path with no
fingerprint at all, or on the selector path with no selector at all, is
malformed and reports drift rather than passing unchecked.

One footprint can cover several evaluations. A multi-walk lens runs each branch
separately, with its own read memo, and the branch footprints are merged; two
branches that read one key at different revisions, or one node's selector to
different edge identities, have already watched a write land between them. The
merged certificate carries that as **torn**, and a torn footprint is rejected
without any re-read: the row already blends two instants, the merged maps hold
one value per key, and a re-read of a graph that has since settled would compare
equal and pass. If nothing moved,
the row proceeds unchanged. If something moved, the evaluation re-executes
once against current state and validates again; if it still diverges
(sustained churn), the pipeline returns a typed transient failure
(`failure.ErrEvalDrift`) instead of writing a row that could blend two
different real instants — never a torn document, and never a silently empty
result set.

Consumer contract: business lens rows are convergent, not point-in-time — a
consumer branching on a cross-key column pair must tolerate transient blends
or go through the Processor; auth-plane envelope rows are footprint-validated.

---

## Rebuild & truncate semantics

`Pipeline.Rebuild(ctx, truncate)` resets a lens's durable consumer so the lens
re-projects from the start of its source stream. The optional truncate step
clears the target store first.

| Adapter / mode | `truncate` requested | Behavior |
|----------------|----------------------|----------|
| NATS-KV, unguarded | `false` | No truncate; the stream replay overwrites each key last-writer-wins. |
| NATS-KV, unguarded | `true` | `Truncate` purges the lens's keys, then the stream replays into the emptied key space. (`Truncate` does what the flag promised — it is not a silent skip.) |
| NATS-KV, **guarded** | `false` or `true` | **Truncate is forced.** A guarded bucket's monotonic `projectionSeq` watermarks would reject the historical lower-seq replays against the live high-seq watermarks, leaving rejected-write holes. The pipeline detects guardedness via `Guarded()` (it never learns lens canonical names), purges the lens's keys — clearing the watermarks with the data — and logs at info that truncate was forced. The stream then replays from empty, the highest-seq write wins, and the steady state is identical to a from-scratch projection (Contract #6 §6.2). |
| Postgres (no `Truncater`) | `true` | Truncate is skipped; the replay still repairs absent rows — see **Repairing a diverged shared target** below. |

**Truncate is scoped to the keys the lens owns.** A lens whose output key pattern
yields a literal prefix (`OutputDescriptor.KeyPrefix` — the same literal
`AnchorFromKey` matches first) purges only keys under that prefix. Several lenses
project into one bucket — the auth plane's `capability` is the live case, and
`weaver-targets` carries 14 — so an unscoped purge there would be a platform-wide
authorization wipe healed only at sweep pace. A lens with no derivable prefix
purges its whole bucket, which is what a **dedicated** target needs to reach the
empty high-water state the guarded replay writes into. The scoping is bound to
the *rule* (`projection.ApplyTruncateScope`), not to one adapter instance, so a
replacement adapter built by an INTO-only hot reload carries it too — exactly how
the §6.2 guard is bound, and for the same reason.

**The sweep's verdict is not inferred from its repair.** `Reproject` returns an explicit
`Verdict` per anchor — `converged`, `healed`, `blocked`, or `unverified` — whose **zero
value is `unverified`**, so a branch added later that forgets to conclude reports "I do not
know" rather than "converged". The verdict is explicit rather than inferred from what the
sweep managed to write: an outcome the repair path has no transport for produces no write,
and a signal derived from writes reads that as convergence — clearing the divergent streak
and rendering, in Health KV, byte-identically to a converged lens. Without the explicit
verdict a lens reads *healthier the more thoroughly it is broken*. `blocked` is the case where the §6.2 ordering guard declines the write against an
equal-or-fresher watermark: nothing errors, the write reports success, and the row does not
change. One call can carry many results for one anchor, so the anchor's verdict is the
**worst** any result reached (`blocked` > `unverified` > `healed` > `converged`).

**`Reproject` also refuses to write under a superseded rule**, the same policy
`writeResults` applies on the CDC path, checked after evaluation and before the write loop.
A MATCH hot-reload swaps the rule synchronously and truncates on a *new* goroutine, so
without the check a still-running sweep pass lands an old-rule row into the emptied target
— where the absent-key branch takes it unconditionally — stamped at the consumer **head**,
which outranks every sequence the rebuild is about to replay. The rebuild is then locked out
of the key by the write it was racing, and if the MATCH edit was a narrowing or a revocation
the frozen row is the pre-edit permission set. The sweep's disposition is to abandon the
whole pass and charge **no** actor a failure strike: no actor is at fault, and a strike would
push it into backoff and delay the genuine post-rebuild heal.

`NatsKVAdapter.Truncate` purges each key (`Purge` per key) rather than deleting:
a purge drops prior revisions and leaves a delete marker, so a subsequent `Get`
returns `ErrKeyNotFound` and a guarded rebuild's first replay takes the
absent→`Create` path with no stale watermark in the way. The force keeps the
projection-write guard **on** across the rebuild — it is never bypassed, so the
monotonic ordering still holds: a stale retry-queue write carries its original
(lower) `projectionSeq` and is superseded by the higher-seq replay of the current
state. The post-rebuild **steady state therefore equals a from-scratch
projection** regardless of how a concurrent retry interleaves with the
truncate/replay — the guarantee is on the converged state, not on instantaneous
consistency mid-rebuild (while a guarded bucket is being rebuilt its keys are
transiently absent, which step-3 denies fail-closed). The retry queue is **not**
separately quiesced during the rebuild because it does not need to be: the guard
makes a racing stale write lose on its own.

### Repairing a diverged shared target

A **grant lens** cannot truncate at all: `actor_read_grants` is shared by every
producer, so `GrantWriterAdapter` deliberately implements no `Truncater` — one
lens's rebuild must never `TRUNCATE` a table it co-owns. That reads as leaving an
operator who restored or partially wiped the table with nothing that re-derives
the missing rows. **It does not.**

`Rebuild(ctx, false)` is the repair. The §6.14 monotonic guard lives entirely in
the `ON CONFLICT … DO UPDATE … WHERE EXCLUDED.projection_seq > stored` arm, so:

- a row that is **absent** — the out-of-band restore or partial wipe — hits no
  conflict and is re-created by the plain `INSERT`, at whatever sequence the
  replay carries;
- a row still **present** replays against its own stored sequence and is a no-op,
  because the comparison is strictly-greater;
- a grant legitimately **revoked** at a higher sequence stays revoked, because the
  stale replay loses the same comparison.

The replay carries the real stream sequence (`ProjectionSeq = msg.Sequence`), not
a reconciliation seq-0, so it is not subject to the absent-row seq-0 refusal that
governs sweep writes. The repair is therefore fail-closed by construction: it can
restore what was lost but cannot over-grant. Pinned by
`TestRLS_RebuildReplayRederivesRowsLostOutOfBand_Integration`.

The same reasoning covers any guarded target without a `Truncater`, which is why
the pipeline logs that case at **info** describing both halves, rather than
warning that rows "survive the rebuild" — that phrasing describes only the
no-op half and discourages the action that actually repairs the divergence.

---

## Capability-Lens health (operational backstop)

The Processor has no per-operation projection-freshness gate. The accepted
bounded-staleness window is backstopped *operationally* by the Refractor's
per-lens health, and (in future) by Gateway token revocation for hard
identity/session revocation. This subsection documents **what the
Capability-Lens pipeline emits today**.

A Capability Lens is any lens projecting into the `capability-kv` bucket
(`projection.IsAuthPlane` — e.g. `capabilityRoles`, `capabilityRoleIndex`). It is
wired through the generic per-lens health path **plus** a Capability-Lens-aware
liveness/lag threshold on the instance heartbeat (see the last row). The signals
it emits:

| Signal | Source | Key / subject | Semantics |
|--------|--------|---------------|-----------|
| Per-lens status | `health.Reporter` | Health KV, keyed by the lens `ruleID` | `active` / `paused` / `rebuilding`, plus `errorCount`, `activeSequence`, `pauseReason`, `lastError`. Updated on lifecycle transitions and `RecordError`. |
| Consumer lag | `health.LagPoller` → `Reporter.SetConsumerLag` | `lattice.refractor.metrics.<lensId>` + the `consumerLag` field on the per-lens health entry | `NumPending` on the lens consumer, polled on an interval. |
| Per-lens latency | `pipeline.LatencyRingBuffer` → `LatticeHeartbeater.LensLatencyProvider` | `health.refractor.<instance>` heartbeat — inline `metrics.lensLatency` (keyed by `canonicalName`) | p95 / p99 / mean / count of per-event projection latency (NFR-P3 instrument). |
| Instance heartbeat | `LatticeHeartbeater` | `health.refractor.<instance>` | 10s heartbeat with TTL purge (NFR-O1). |
| **Capability-Lens liveness alert** | `LatticeHeartbeater.CapabilityLensProvider` → threshold eval | `health.refractor.<instance>` — `metrics.capabilityLens.<canonicalName>` `{status, consumerLag, alert}` (always emitted) + a Contract #5 §5.5 `issues[]` entry and degraded/unhealthy `status` when anomalous | A **paused** capability lens raises `CapabilityLensPaused` (`severity: error` ⇒ `status: unhealthy`): the authz read-model is frozen. An **active** lens with `consumerLag` over the threshold (default 100, deployment-overridable) raises `CapabilityLensLagging` (`severity: warning` ⇒ `status: degraded`) — **debounced**: it raises only after the lens stays over threshold for N consecutive heartbeats (default 3 ≈ 30s sustained) and clears once lag falls to/below a lower clear-threshold band, so a one-cycle spike does not flap. `rebuilding` and within-threshold are `ok`. The issue's `since` persists across heartbeats and the issue is dropped when it resolves. An actor-aggregate capability lens also carries its convergence sweep's verdicts here — `CapabilityCoverageDivergence`, `CapabilityRepairFailing`, `CapabilityRepairBlocked`, `CapabilityAuditUnverified`, `CapabilitySweepStalled` — where, unlike the business-lens family, a sustained streak escalates to `error` (⇒ `status: unhealthy`): a wrong auth-plane row is a live permission set the graph no longer grants. `CapabilityRepairBlocked` names a divergence the §6.2 ordering guard refused to let the sweep repair (the write returned success having changed nothing, so `RepairFailing` stays silent), and `CapabilityAuditUnverified` an anchor the sweep could reach no verdict on at all. Read-only — it observes the lens reporter + supervised consumer; no authz path, Core KV, or projection is touched. |
| **Per-lens projection liveness (all lenses)** | `Pipeline.Progress()` (in-process `lastAppliedSeq`/`lastProjectedAt`) → `health.Reporter.SetProjectionProgress` (30s cycle; the Health-KV write itself is skipped when lag, `lastProjectedAt`, `lagProgressAt`, `ackPending` and `ackFloorProgressAt` are all unchanged from the last write — the metrics publish stays unconditional every cycle) → `LatticeHeartbeater.LensProvider` → threshold eval | `<lensId>` entry — `lastProjectedAt`/`projectionLag`; `health.refractor.<instance>` — `metrics.lensLiveness.<canonicalName>` `{status, projectionLag, lastProjectedAt, alert, unreadable}` (always emitted) + a Contract #5 §5.5 `issues[]` entry and degraded `status` when anomalous | The generalized sibling of the Capability-Lens backstop above, widened to **every** non-auth-plane (business) lens (lens-projection-liveness-design.md). `lastProjectedAt` (advances only on real output — a landed row write, a `Hydrate`, or a **signalled** personal reprojection's keyset frame, which is the whole answer when the admitted row set is empty — so a caught-up-but-no-op consumer leaves it frozen even while `lastAppliedSeq` moves; the standing personal healer's frames-only pass and a live CDC event's frame deliberately do **not** stamp it — a healer turning over every personal lens every pass, or an event framing its actors whatever its rows did, would keep `LensProjectionStalled` from ever firing) gives an operator a real freshness clock; the same raise-after-N / clear-band debounce as the cap path auto-alerts a wedged consumer via `LensProjectionLagging`, and a paused business lens raises `LensProjectionPaused` — both `severity: warning` (⇒ `status: degraded`), **never** `error`/`unhealthy`: a single frozen business lens is a real outage for that vertical but must not fail the whole Refractor instance. A lens whose liveness inputs cannot be read is reported `status: "unknown"` / `alert: "unreadable"` with `projectionLag: null` and raises `LensProjectionUnreadable`, never dropped from the map — an absent lens is indistinguishable from one that was never installed. An actor-aggregate business lens also carries its convergence sweep's verdicts here — `LensCoverageDivergence`, `LensRepairFailing`, `LensRepairBlocked`, `LensAuditUnverified`, `LensSweepStalled` — mirroring the `Capability*` sweep codes but **always `severity: warning`**, at every streak length, for the same reason pause and lag are. Auth-plane lenses are excluded (the Capability-Lens path above stays canonical for them) — separate debounce/issue and sweep-staleness state, zero regression surface on that security-critical path. An enrolled **plain** lens carries a second, independent detector here: the divergence audit's `LensProjectionDiverged` (the recomputation disagrees with the stored row, per-class in `divergentRows`, and **nothing was repaired** — the audit never writes) and `LensAuditStalled` (the audit itself has stopped reaching verdicts), both `warning`. `auditEnrolled`/`auditRefusal` are published for every lens, so a lens with no correctness detector is visible rather than reading like one that keeps finding nothing. |
| Eval-drift retries/requeues | `pipeline.executeFullForActor` → `Reporter.RecordEvalDriftRetry` / `RecordEvalDriftRequeue` | `<lensId>` entry — `evalDriftRetries`/`evalDriftRequeues` | Footprint-validation counters (evaluation-consistency-design.md §4.6/§13.3): only an auth-plane actor-aggregate lens whose cypher emits a multi-binding conjunct unit pays this cost — a single-binding auth-plane lens (e.g. `capabilityRoles`) is exempt by construction and never attempts a drift-retry on its own legitimate churn. `evalDriftRetries` counts an inline re-execution the drift-detection triggered; `evalDriftRequeues` counts an evaluation whose read surface still diverged after that retry and was requeued as `failure.ErrEvalDrift` rather than landing a possibly-torn row. Both are cumulative and expected to stay near zero — a nonzero rate under sustained load is the signal that sizes Fire 2's per-row footprint scope. |
| Audit | `health.AuditWriter` | `lattice.refractor.audit.<lensId>` | Per-**committed**-projection audit append (see the Audit subjects row): a write that stored no row appends no entry. |

This is the automated backstop for the Processor's absent per-op freshness gate:
a dead or lagging Capability projector now degrades the Refractor heartbeat with a
distinct, machine-readable issue the **Lamplighter** classifies and surfaces,
rather than requiring an operator to read generic signals and apply judgment.

**Residual follow-up (not a gap in the alert itself):** the Gateway
token-revocation **hard** control — a paused/lagging capability lens degrades
health but cannot itself force-revoke a stale token — remains future work, landing
with the Gateway / read-path authorization (D1). (Loupe's `componentLiveness` fuses
heartbeat freshness with the §5.4 `status` and the worst §5.5 `issues[]` severity on
its component cards and system-map nodes; the lag alert debounces — raising only
after several consecutive over-threshold heartbeats, with a lower clear-threshold
band — so a one-cycle spike does not flap the heartbeat.)

---

## Principles (binding)

- **Lenses are the read path**: reads never go through the write path. The operation reply carries only commit-trace identifiers (`primaryKey`, `revisions`) — it is never a query channel (there is no arbitrary `detail` map, and the constraint is enforced in code).
- **Every Core KV mutation must be observable** via at least one lens projection (NFR-P3 ≤500ms end-to-end latency target). The `LatencyRingBuffer` p99 is the primary instrument.
- **Lens output is overwrite-by-reprojection**: fabricated or stale KV writes in a lens target are corrected on the next reprojection event. This is the fabricated-KV-write defense. Substrate-level write restriction on the lens target buckets (per-component NKey publish permissions) is 🔭 Designed — the ratified NATS account write-restriction hardening (credential seam shipped, enforcement pending).
- **Lens definitions live in Core KV vertices**, not in source code. The platform discovers them via the `vtx.meta.>` CDC stream. Seeding a new lens requires a `CreateMetaVertex` operation through the Processor write path.
- **openCypher full engine is canonical**; it is the only rule engine Refractor runs.

---

## What's deferred

| Feature | Phase | Notes |
|---------|-------|-------|
| Personal Lens / Secure Lens | Fires 1–4 (PL.1 transport, PL.2 fan-out + Interest Set, PL.3 D1 security gate, PL.4 Hydration Hook) shipped; Retraction R1 (server-side keyset frames) shipped, R2 (Edge-side consumption) pending; PL.5 pending | Per-identity security-filtered projection. PL.1's `nats_subject` target adapter + PL.2's `ActorEnumerator` fan-out/Interest Set + PL.3's `capabilityread`-backed D1 gate + PL.4's `personal.hydrate` cold-bulk-projection RPC + Retraction R1's per-actor keyset frame (above) ship dark; the NATS `SUB` boundary (Fork 3, subscribe-ACL) and the `personal.{register,deregister,hydrate}` request-body identity binding are now closed (per-identity-nats-subscribe-acl-design.md Fires 1–2) — full untrusted-multi-identity exposure still waits on that design's Fire 3 (Edge design EDGE.3 handoff); Vault ciphertext + transient-key composition (PL.5) remains |
| Multi-cell lens routing | Phase 3 | Current pipeline is single-cell |
| Cross-instance latency aggregation | Phase 3 | Current `LatencyRingBuffer` is per-instance; no cluster-level rollup |
| Substrate-level write restriction on lens target buckets | 🔭 Designed (ratified 2026-06-27) | Today the defense against fabricated lens-target writes is overwrite-by-reprojection only; the **NATS account write-restriction** design scopes per-component NKey publish permissions so only Refractor writes the lens/auth buckets (credential seam shipped; enforcement pending) |

---

## Review keeps catching (dossier)

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
  **Fifth sighting (Secure plain lens retraction+audit, 2026-09-05): a conjunct's stated reason can name a code
  path the conjunct's own consumer never takes.** Two refusals cited "plaintext" against an evaluation path
  (`executeFullForAudit`) that never calls the decryptor, a third cited the diff against a path that never
  diffs, and the licence's was a real plumbing seam described as a soundness bound. Check: for every
  enrolment/licence conjunct, name the function the consumer actually calls and grep it for the thing the
  reason fears; a reason that names a different function is a claim about the wrong consumer.
  **Third sighting (expiry-as-a-recorded-fact, 2026-09-02): a lifted refusal reveals the conjunct behind it,
  and a GRANTED licence logs nothing.** The design's "`$now` is the last conjunct refusing
  `leaseApplicationsRead`" was true of the log line and false of the licence: once the audit enrolled and
  reached a verdict the licence refused at `ProjectsOneRowPerAnchor` — a shape fact no clock edit could
  move — and the only evidence either way was a refusal line whose absence proves nothing. Check: a payoff
  claimed as "refusal X gone" is proved by the licence's POSITIVE verdict (enrolment log / audit verdict /
  a tally that acts), read live after the fix; and a design lifting conjunct N reads conjuncts N+1..end
  against the lens before it promises the payoff. **Third sighting, one level up (personal-lens whole-actor cost, 2026-09-03): a review that REPLACES the mechanism refutes the measurement that justified the old one.** The read-cost probe timed a key listing; the listing was then refuted (a count-bounded stop condition drops a hot key from two agreeing enumerations) and replaced by a STREAM.INFO resolution — and the design kept citing the listing's numbers until the close pass. Check: when a mechanism is swapped at build, re-run the census/probe on the SHIPPED one before the row closes; numbers about deleted code are not evidence. **Fourth sighting (personal-lens delta publication Inc 1, 2026-09-04): a design's negative claim about a CONSUMER ("nothing alarms on that clock") is a coverage claim — `LensProjectionStalled` reads `lastProjectedAt`, and stamping it on the healer's frames-only pass would have turned the divergence signal into a healer heartbeat on all 15 personal lenses.** Check: before a design says nothing reads X, grep X's readers, and list each one's fail direction. **Fifth sighting (untyped-hop anchor derivation, 2026-09-04): a refusal lifted to a BOUND moves the guard from the predicate into prose, and the prose named an enforcer that does not enforce it.** §4.2 stated the wildcard hop's soundness bound (position count and hop incidence) and pointed at the relation-COVERAGE census as what keeps the corpus inside it; a cold reviewer ran a four-position wildcard graph through that census and it passed. Check: when a design replaces "refuse X" with "X is sound only under C", name the assertion that fails when C is violated, on C's own dimension, and run C's violating shape through it before the claim ships (`TestCorpusAnchorHopIndex_WildcardHopGraphsStayInsideTheBound`).
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
- **A widened operation silently drops the bound or budget its narrow predecessor carried.** A per-row read
  inherited nats.go's default API timeout; the whole-actor scope read on the consumer's deadline-less ctx had
  none (R7). A synchronous publish was bounded at 5 s; its async replacement could wedge a flush forever (R8).
  A resolution's legs were unbounded on the same ctx. And a window sized per pipeline against a
  per-CONNECTION async budget (R9). Minted: personal-lens whole-actor cost (2026-09-03) — the same defect
  three times, found by three cold reviewers. Check: every new substrate primitive or batch states, in its
  doc, what bounds it when the ctx carries no deadline and against which SHARED budget it is sized (name the
  denominator); a new exported ctx-taking substrate func without that sentence is the smell. **Second
  sighting, and the widened thing is a FLAG rather than an operation:** `rebuildInFlight`'s comment priced
  being briefly wrong as bounded *because the sweep is a healer and the attestation reads `drained`* — true
  for its three readers, false for the fourth, a personal lens that publishes nothing while it is set, where
  the same brief wrongness is a resumed flood of messages every device drops. That half is now mechanized by
  `scripts/lint-flag-consumer-census.go`, which holds each registered flag's reader set (file + function) and
  fails an undeclared reader as well as a declared one that has stopped reading, so adding a reader means
  re-reading the bound. The substrate half stays unmechanized; promotion candidate: a `lint-conventions` rule
  over `internal/substrate`'s exported ctx-taking funcs. (Displaced the
  check-less *"an upsert-only reprojection retracts nothing whose key drops out"* note — its subject is the
  delta-publication row on the board, not a review check.) Sighted again (personal-lens delta Inc 4, 2026-09-05) in a new form: a process-wide FLAG gaining a reader — `rebuildInFlight` acquired publication silence while `releaseRebuildSignal`'s doc still priced the concurrent-abandon race as "the sweep is a healer"; the stale bound hid in the flag's own comment. Promoted: `scripts/lint-flag-consumer-census.go` fails when a registered flag gains a reader the registry does not declare — the fix path is to declare it and re-read every bound the flag's comment asserts.
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
  answer is nothing" must behave alike. **Second sighting, the SET-read form (personal-lens whole-actor cost, 2026-09-03): a corrupt member of a set read fails the whole set where the per-item read it replaced failed only the item that needed it** — one unparseable `cap-read` body wedged an actor's entire personal plane into redelivery (R6); a corrupt never-dereferenced Core-KV key failed the evaluation (R6 mirrored). Check — MECHANIZED as a mandated test shape: any batch/set reader ships a corrupt-member test proving the failure stays scoped to the member (`TestReadableAnchors_MalformedBodyDeniesOnlyThatAnchor`, `TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed`).
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
  fail-open by construction; enumerate the shapes that provably do not, and assume the rest carry it. RUNNING-vs-DECLARED form (2026-09-05): a class label computed from the running object (`publishesToDevices()` reading the adapter) answered false for a nil pipeline on the class-key erasure's attestation path — the admitting answer — while the declared fact (`IsPersonalLens`) sat unused; a boolean gating a fail-closed refusal asserts both sources and pins the vector where the running one is unavailable.
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
  before asserting, name what the fixture ARRANGED that production does not, and arrange the other way. **Fourth and fifth sightings (personal-lens whole-actor cost, 2026-09-03), with the generalization the fire adds: pin the mechanism's COST absolutely, not only its result.** Every zero-read assertion counted per-key reads, so a shrunken chunk constant (1,024 → 4) passed both packages green with the whole payoff gone; before-frame tests were carried by send order alone with the flush short-circuited; an engine-params isolation test could not fail. Check: a batching or pipelining mechanism ships a request-count pin (`batchReads`, `Pending() == 0` at the seam) and an absolute pin on its constants, each revert-proven. **Sixth and seventh sightings (personal-lens delta publication Inc 2, 2026-09-04), on the HARNESS and on the MULTIPLIER:** a log attribute carrying a struct with only a `String()` rendered as `{}` under the JSON handler production installs, and the unit test was green under the text handler it had installed instead; and a fold memo keyed to the node the caller ASKS for never served the shared head production reaches per output row, while the allocation pin ran on a two-row fixture — the one shape where rows × chain is invisible — and called it the worst case. Check: the harness half is **MECHANIZED** — `scripts/lint-slog-values.go` refuses an slog attribute value of a module struct type that implements none of `slog.LogValuer` / `json.Marshaler` / `encoding.TextMarshaler`; for the multiplier half, a cost pin names which dimension multiplies the mechanism's work and runs on the fixture that maximizes THAT one, in the production call order (never by hand-calling the arm the memo was written for). ABSENCE form (T8, 2026-09-05): an assertion that nothing was published ships a positive control that the mechanism was exercised (the replay delivered, the applied sequence advanced), or a narrowed filter passes it vacuously.
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
  **Corollary (Secure plain lens retraction+audit, 2026-09-05): a transport with a cap has a fallback, and
  the fallback needs a health field.** `recordDerivationFellBack` reached no published surface, so a
  transport that fell back on every event read identical to one that never had to — a transport that can
  be silently off. Check: for every bounded mechanism, name the branch taken past the bound and the health
  key that counts it; a shadow-path tally with no publisher is a zero indistinguishable from not measured.

- **An operator-driven repair promoted to an AUTOMATIC one carries every tolerance the operator path had, and each must be re-derived.** Minted: lens-output-reactivation (2026-09-03), where an Output edit began re-activating the lens with the rebuild's purge ahead of the replay. Three tolerances rode in, all found by the cold reviewer, none by the brief: (a) a listing SCOPE is not an ownership set — `KeyPrefix`'s own doc says a prefix admits siblings (`cap.` contains `cap.roles.`), and `truncateKeys` purged the listing whole, so a `bodyColumns` edit to the kernel `capability` lens would have wiped four sibling producers' rows; (b) a healer's clear keyed to nothing clears every writer's latch — the clean-registration `ClearLastError` erased the purge-failure diagnosis seconds after it was raised; (c) a flag consulted on the wrong side of a force rule guards nothing — `requested` gated the purge while `resolveTruncate` forces one for any guarded adapter, so a protected Postgres table was still purgeable. Check: ownership is exact (`OutputDescriptor.OwnsKey`, bound by `ApplyTruncateScope`, pinned by `TestTruncateScope_KernelCapabilityLensPurgesOnlyTheKeysItsOwnInverseClaims`), a clear names what it owns (`Reporter.ClearLastErrorIf`), and a refusal is by construction (`reactivationPreflight`) — and a design that automates a repair lists the operator path's tolerances (scope, clears, teardown result, target family) as premises to falsify.