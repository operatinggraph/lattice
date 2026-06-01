# Story 1.5.10 — Transactional Event Outbox

**Status:** SPEC — adjudicated. Pre-Phase-2 hardening. **Gates Phase 2** (Loom/Weaver).
**Module surface:** `internal/processor`, `internal/bootstrap`, new `internal/processor/outbox` consumer, `cmd/processor`.

---

## 0. ADJUDICATION — FINAL (Winston). DS builds to THIS; it overrides any conflicting fork recommendation below.

Decisions locked with Andrew (2026-06-01):

- **Fork A — persistence location: dedicated aspect `vtx.op.<id>.events`.** Written as an additional `BatchOp` in the SAME step-8 `substrate.AtomicBatch` (single-bucket Core KV), so the EventList is captured atomically iff the commit succeeds. Body carries the FULL faithful `EventList` (each event's `eventId`, `class`, `targetKey`, `payload`, `timestamp` — exactly what `BuildEventList` produced), not just class names. Contract #1 aspect envelope (`vertexKey`, `localName: "events"`, `class: "events"` or similar; value under `data`).

- **TTL / durability — the outbox record MUST outlive the 24h dedup tracker.** The 24h tracker horizon is a **per-key TTL** (`Nats-TTL` header on the tracker `BatchOp`, `step8_commit.go:174`), NOT a bucket-level expiry; the Core KV bucket has no `MaxAge` (durable — confirmed `primordial.go:73-99`; the 7-day `MaxAge` is on `core-events` only). Therefore: **write the `vtx.op.<id>.events` aspect BatchOp with NO `Nats-TTL` header** → it persists durably and outlives the tracker (which expires at 24h). The aspect is removed by the consumer after confirmed publish (below). An unpublished record persists indefinitely until delivered — events are never dropped by a >24h outage.

- **Fork B — publish path: OUTBOX-ONLY. Remove synchronous step-9 entirely.** All events publish via the durable outbox consumer. Rationale: the <500ms p99 ceiling is CDC-to-projection (Refractor → Capability Lens) and auth is CDC-reactive — it does NOT consume `core-events`, so the added CDC hop costs latency only on Phase 2 Loom/Weaver orchestration, not auth. Contract #4 §4.4 already declares events post-commit, best-effort, at-least-once, consumer-idempotent — which sanctions async publish. Single path eliminates the double-publish race.

- **Fork C — republish guard: consumer-idempotency + delete-after-publish (no `published` marker).** Because B is outbox-only there is exactly ONE publisher. The durable consumer's offset is the primary progress guard; after a confirmed `PublishBatch` the consumer **tombstones `vtx.op.<id>.events`** (cleanup that also makes a full seq-0 replay republish-safe). Events remain at-least-once; no exactly-once claim. No extra per-op marker write.

- **Redelivery path is DELETED, not rewritten.** With outbox-only, a redelivered op message that hits step-2 dedup simply **acks** — the events were already persisted in the original atomic batch and the outbox consumer owns publishing. Remove `RebuildEventListFromClasses`, `maybeRepublishEvents` and its call sites, and any `eventsPublishedAt`/`markEventsPublished` reconstruction semantics. After this story NO code path reconstructs events from KV keys.

- **Consumer shape:** durable consumer (e.g. `processor-outbox`) on `KV_<CoreKVBucket>`, `FilterSubject` matching the events-aspect key shape (`$KV.<bucket>.vtx.op.*.events`), `DeliverAllPolicy` + `AckExplicitPolicy`. On each delivery: skip empty/PURGE/tombstone bodies (ack); else unmarshal the EventList, `substrate.PublishBatch` to `core-events`, then tombstone the aspect, then ack. Nak on publish failure (redelivery retries). Wire it in `cmd/processor/main.go` alongside `cp.Run`. Mirror the durable-consumer/reconnect discipline of `internal/refractor/consumer/bootstrap.go`.

- **Open item to confirm during DS (not blocking):** verify the events-aspect key (`localName: "events"`) does not collide with any existing op-tracker aspect, and that writing an aspect whose parent vertex carries a 24h TTL while the aspect itself has none is honored independently by NATS KV (it is — each KV key is an independent stream subject/message; per-key TTL is per-message). If DS finds otherwise, escalate to Winston before proceeding.

The sections below are the CS agent's grounding analysis; where §3's fork recommendations differ from §0, §0 wins.

---

## 1. Context & problem

`core-events` are **not** CDC. They are intentional, declared-schema business events that Phase 2 Loom and Weaver depend on; the stream is a typed contract (Contract #3 §3.4: "Events with no registered DDL are rejected — unlike vertex/aspect/link writes, events MUST be declared… consumers (Loom, Weaver) depend on schema knowledge").

The script returns the authoritative `EventList` (Contract #3 §3.1, `{mutations, events}`). Today the commit path builds the real `EventList` once at step 8 (`BuildEventList`, `internal/processor/step7_events.go:41`) and publishes it synchronously at step 9 (`internal/processor/step9_publish.go:115`, `EventPublisherImpl.Publish` → `substrate.PublishBatch`). That primary-path list is faithful.

The defect is on **redelivery**. The op tracker (`vtx.op.<requestId>`, Contract #4) persists only `data.eventClasses` (the class-name list) and `data.mutationKeys` — see `internal/processor/step8_commit.go:121-122` and Contract #4 §4.2. It does **not** persist the events themselves (their `eventId`, `payload`, `targetKey`, `timestamp`). So when the Processor crashes between step 8 (durable commit) and step 9 (publish), redelivery hits the step-2 dedup short-circuit (`internal/processor/commit_path.go:151`, `internal/processor/step2_dedup.go:38`) and calls `maybeRepublishEvents` (`commit_path.go:520`), which invokes **`RebuildEventListFromClasses`** (`internal/processor/step9_publish.go:20`).

`RebuildEventListFromClasses` **fabricates** events from the persisted class names + mutation keys:
- fresh `eventId` per event (`step9_publish.go:27` — the original IDs are gone),
- **empty `payload`** (`step9_publish.go:40` — `map[string]interface{}{}`),
- `targetKey` guessed positionally from `mutationKeys[i]` (`step9_publish.go:31-34`).

The doc comment is explicit: "this is a best-effort re-publish rather than an exact replay" (`step9_publish.go:18-19`). A reconstruction from KV keys is **not equal** to what the Starlark script actually returned — the payload (the thing Loom/Weaver act on) is lost entirely. This is wrong for a declared-schema event contract and is a hard prerequisite to fix before Phase 2 (epics.md:296,298; `lattice-architecture.md:186`).

The fix: a **transactional outbox**. Persist the real script-returned `EventList` as part of the step-8 atomic batch (on the op tracker), and have a durable consumer publish from that persisted record to `core-events`, acking only on confirmed publish. Redelivery republishes the **real** events, never a reconstruction.

---

## 2. Goal / non-goals

**Goal**
- Persist the faithful `EventList` (full events, including `eventId`/`payload`/`targetKey`/`timestamp`) atomically with the commit, on the op tracker, within the existing step-8 `substrate.AtomicBatch`.
- A durable consumer reads persisted op-tracker outbox records and publishes the real events to `core-events`, acking only after confirmed publish.
- Redelivery / crash-between-commit-and-publish republishes the **real** events (byte-identical to what the script returned + the IDs minted at commit), not a reconstruction.
- Delete `RebuildEventListFromClasses` and the best-effort reconstruction path entirely.

**Non-goals**
- No change to the Starlark return contract (Contract #3 §3.1) or event DDL validation (step 7 / `vtx.meta.event.<name>`).
- No change to event subject scheme (`events.<class>`, `step9_publish.go:195`) or the `core-events` stream's `events.>` / 7-day `MaxAge` config beyond what the outbox consumer requires.
- No exactly-once delivery to consumers. Events remain **at-least-once**; Loom/Weaver remain idempotent (Contract #3 §3.4 convention + `lattice-architecture.md:184`). This story fixes *fidelity*, not *cardinality*.
- No Loom/Weaver work.

---

## 3. Design

### 3.1 Fork A — Where the EventList is persisted → **RECOMMEND: dedicated aspect `vtx.op.<id>.events`** (Andrew should ratify)

Two options:
- **A1 — aspect** `vtx.op.<requestId>.events`: a separate Core KV entry (independently CDC-visible), written as an additional `BatchOp` in the same step-8 atomic batch.
- **A2 — tracker root `data` field**: e.g. `tracker.Data["events"] = <full EventList>`, alongside the existing `eventClasses`/`mutationKeys`.

**Recommendation: A1 (dedicated aspect).** Rationale grounded in code/contracts:
- **Convention.** Contract #1's envelope/aspect model: business data lives in aspects, not bloated on the root `data`. The op tracker is platform infrastructure (not a business vertex), but the outbox *is* a distinct, sizeable, separately-consumed datum — exactly the shape an aspect models. A1 keeps the tracker root lean (it stays the dedup linchpin, Contract #4 §4.1) while the events ride a sibling key.
- **Consumer read shape.** The outbox consumer filters the Core KV stream for op-tracker outbox writes. With A1 the consumer's `FilterSubject` can target the events aspect precisely (`$KV.<bucket>.vtx.op.*.events`), so it never has to parse every tracker write — mirrors how the refractor adjacency consumer filters (`internal/refractor/consumer/bootstrap.go:64-69`). With A2 the consumer must read every `vtx.op.*` write and dig events out of `data`.
- **Payload size.** Events carry full payloads (Contract #3 §3.4). Bundling them into the tracker root grows the dedup record that step 2 reads on the hot path (`step2_dedup.go:38` KVGets the tracker on *every* op). A1 keeps step-2 reads small.
- **Atomicity preserved.** A1 is still one extra `BatchOp` in the same single-bucket `substrate.AtomicBatch` (`step8_commit.go:139-175`), so it commits atomically with mutations + tracker — no new transaction.

Cost of A1: one extra Core KV entry per event-emitting op, and it needs its own TTL (set equal to `TrackerTTL`, 24h, `tracker.go:19`) so it expires in lockstep with the tracker. Ops with zero events write no events aspect (skip the `BatchOp`).

> **Andrew must ratify A.** The alternative (A2) is simpler to wire but couples event payload size to the dedup hot path. Recommendation stands on the read-shape + hot-path argument.

**Outbox aspect shape (A1):**
```json
{
  "key": "vtx.op.<requestId>.events",
  "class": "op-outbox",
  "isDeleted": false,
  "vertexKey": "vtx.op.<requestId>",
  "localName": "events",
  "createdAt": "…", "createdBy": "…", "createdByOp": "vtx.op.<requestId>",
  "lastModifiedAt": "…", "lastModifiedBy": "…", "lastModifiedByOp": "vtx.op.<requestId>",
  "data": {
    "requestId": "<requestId>",
    "events": [ { "eventId": "...", "requestId": "...", "eventType": "...",
                 "targetKey": "...", "payload": { ... }, "timestamp": "..." } ],
    "published": false
  }
}
```
`data.events` is the exact `EventList` from `BuildEventList` (the `Event` JSON shape, `step7_events.go:15-22`). `published` is the fork-C marker (see §3.3).

### 3.2 Fork B — Does synchronous step 9 stay? → **RECOMMEND: Option 1 (keep step 9 as fast-path, outbox consumer as durable backstop)** (Andrew should ratify)

- **Option 1 — keep step 9 synchronous, add outbox consumer as backstop.** Steady-state events publish in-commit (low latency); the durable consumer only does real work when step 9 didn't complete (crash) or on redelivery. Two publish paths to keep consistent.
- **Option 2 — remove step 9; all publishing flows through the outbox consumer.** One path, simplest correctness; every event incurs commit→CDC→consumer→publish latency.

**Recommendation: Option 1.** Rationale grounded in the locked NFRs:
- The architecture locks **CDC-to-projection lag < 500ms p99**, and "auth correctness depends on this ceiling" (`lattice-architecture.md:159`); capability auth depends on event-driven reprojection latency. Routing *every* event through an extra CDC hop (commit → Core KV CDC → outbox consumer → `core-events` → Loom/Weaver/Refractor) adds a hop to the steady-state latency budget that Option 1 avoids for the common case.
- Step 9 already exists, is tested, and is atomic per-publish (`step9_publish.go`, `substrate.PublishBatch`). Keeping it is low-risk; the outbox consumer becomes a *correctness backstop* that only fires when step 9 didn't confirm.
- The two-paths-consistency risk is contained by fork C's `published` marker (§3.3): both paths set the same marker and both no-op once it's set, so they cannot double-publish in steady state and they converge on crash.

The decision Andrew must weigh: Option 2 is genuinely simpler (one path, no "did step 9 run?" reasoning) at the cost of adding the outbox CDC hop to the *steady-state* event latency for everything Phase 2 consumes. Given the <500ms auth-coupled ceiling, **CS recommends Option 1 but flags this as a real architectural choice for Andrew.**

**Consequence for the commit path under Option 1:** step 9 stays (`commit_path.go:333-343`). On step-9 success it sets the `published` marker (replacing the current `markEventsPublished` `eventsPublishedAt` write). On step-9 failure it still naks for redelivery — but the *backstop* is now the outbox consumer, not `maybeRepublishEvents`. `maybeRepublishEvents` + `RebuildEventListFromClasses` are **deleted** (§8).

### 3.3 Fork C — Republish guard → **RECOMMEND: explicit `published` marker on the outbox aspect** (Andrew should ratify)

- **C1 — rely on durable-consumer offset + Loom/Weaver idempotency only** (events at-least-once by contract).
- **C2 — write an explicit `published` marker back to the outbox aspect after confirmed publish** (stronger dedup; one extra KV write per op).

**Recommendation: C2.** Rationale:
- With Option 1 (fork B) there are *two* producers into `core-events` (step-9 fast path and the outbox consumer). A shared, authoritative `published` flag on the outbox aspect is what makes them mutually exclusive in steady state: step 9 sets it on success; the outbox consumer skips any aspect already marked `published:true` and only publishes (then marks) the unmarked ones. Without it, every committed op would be published twice in the happy path (once by step 9, once by the consumer) — tolerable by contract but wasteful and noisy for Phase 2.
- Contract #4 §4.4 and `lattice-architecture.md:184` already promise consumers must tolerate duplicates, so C2 is an *optimization for correctness-clarity*, not a new guarantee. We do not claim exactly-once.
- Cost: one extra unconditional KV write per event-emitting op (the marker update), analogous to today's `markEventsPublished` KVPut (`commit_path.go:576-589`) — net-neutral vs. the path being deleted.

The marker is a tolerated-best-effort write: if the marker write fails after a confirmed publish, the worst case is a duplicate publish on redelivery, which consumers already tolerate. **Crash/marker-loss never causes event loss or fabrication — only an at-most-one duplicate.** That is the whole point of the outbox over `RebuildEventListFromClasses`.

> **Andrew must ratify C.** If he prefers minimal KV writes and is comfortable with the happy-path double-publish (Option 1 + C1), drop the marker and lean entirely on consumer idempotency. CS recommends C2 because it makes the two-producer model clean.

### 3.4 The durable outbox consumer

New package `internal/processor/outbox` (mirrors `internal/refractor/consumer`):
- **Source stream:** the Core KV stream `KV_<coreBucket>` (`subjects.CoreKVStream`, `subjects.go:45`).
- **FilterSubject:** `$KV.<coreBucket>.vtx.op.*.events` — only outbox aspects (A1 enables this precise filter). Confirm NATS token wildcard semantics for the `requestId` segment; fall back to `$KV.<coreBucket>.vtx.op.>` + key-shape gate (`localName == "events"`) if `*` doesn't span the id cleanly.
- **Durable name:** `processor-outbox`.
- **DeliverPolicy:** `DeliverAllPolicy`. **AckPolicy:** `AckExplicitPolicy`. (Same config shape as `bootstrap.go:64-69`.)
- **Per-message handling:**
  1. Recover the Core KV key from the subject (strip `$KV.<bucket>.`, as `bootstrap.go:153`).
  2. Empty body (KV tombstone/PURGE marker, incl. the 24h TTL `MaxAge` purge, Contract #4 §4.3) → `Ack` + skip (`bootstrap.go:144-150`).
  3. Unmarshal the outbox aspect. If `data.published == true` → `Ack` + skip (already published by step-9 fast path or a prior consumer pass).
  4. Else decode `data.events` into `EventList`, publish via the **same** `EventPublisherImpl.Publish` (or `substrate.PublishBatch`) used by step 9, with identical subjects/headers so event IDs and payloads are byte-faithful.
  5. On publish success → write `published:true` back to the outbox aspect (unconditional KVPut, best-effort, fork C) → `Ack`.
  6. On publish failure → `Nak` (JetStream redelivers; events stay at-least-once).
  7. On unparseable aspect → `Term` (poison message; structurally cannot be a valid outbox record) — but log loudly, since an unparseable outbox record is an event-loss risk and should alert.

**Wiring:** launch alongside the commit path in `cmd/processor/main.go` (today launches `hb.Run` + `cp.Run`, `cmd/processor/main.go:114-138`) as another goroutine: `go outboxConsumer.Run(ctx)`. Construct it from the same `*substrate.Conn` + coreBucket. (Decide: co-located in the Processor binary, mirroring how refractor co-locates its bootstrap consumer.)

### 3.5 Crash-recovery semantics

- **Crash after step 8, before step 9:** tracker + outbox aspect are durably committed (same atomic batch). The op message is unacked → JetStream redelivers; step 2 dedup short-circuits (no re-execution, no re-mutation — mutations are revision-conditioned no-ops anyway). **Independently**, the outbox consumer sees the committed outbox aspect (`published:false`), publishes the **real** events, and marks `published:true`. Redelivery's dedup path no longer republishes (it does nothing about events now — that's the consumer's job). Result: the real events reach `core-events` exactly via the persisted record. No reconstruction, no fabricated payloads.
- **Crash after step 9 publish, before marker write:** outbox consumer later publishes again (marker still false) → one duplicate, consumers idempotent. No loss.
- **Consumer restart:** `DeliverAllPolicy` + durable offset; it re-reads from its last ack point. Any `published:false` outbox aspect within retention gets (re)published; `published:true` ones are skipped. Lag is bounded by the consumer's pending count (observable as in `bootstrap.go:128-134`).
- **Ordering:** events for a *single* operation publish as one ordered `substrate.PublishBatch` (Nats-Batch-Sequence 1..N, `batch.go:188-200,275`), preserving intra-op order as today. Cross-operation order is **not** guaranteed (it isn't today either) — Phase 2 consumers must not assume global event order.

---

## 4. Acceptance criteria (testable)

1. **Atomic persistence.** A committed event-emitting op writes the op tracker AND a `vtx.op.<id>.events` outbox aspect containing the **full** `EventList` (each event's `eventId`, `eventType`, `targetKey`, **non-empty `payload`** when the script supplied one, `timestamp`) in a single atomic batch. An op with zero events writes no outbox aspect.
2. **Fidelity on redelivery (the core AC).** Simulate a crash between step 8 and step 9 (commit durable, publish not run). After recovery, the events delivered to `core-events` are **byte-identical** to the script's returned `EventList` (same `eventId`s minted at commit, same `payload`s) — **NOT** a reconstruction. A test that asserts a non-empty payload survives redelivery MUST pass (it cannot today: `RebuildEventListFromClasses` yields `payload:{}`).
3. **No reconstruction code remains.** `RebuildEventListFromClasses` and `maybeRepublishEvents` are deleted; `grep -r RebuildEventListFromClasses internal/` is empty (excluding this spec).
4. **Outbox consumer publishes from the persisted record.** With step 9 disabled/failing, the durable consumer alone delivers the real events to `core-events` and acks only after confirmed publish; a forced publish failure results in a Nak + redelivery (events not lost).
5. **No double-publish in steady state (fork C).** Happy path: events appear on `core-events` exactly once; the outbox consumer skips the aspect because `published:true`.
6. **Idempotent re-run / duplicate tolerance.** Replaying the same op (or restarting the consumer) never fabricates events and never loses events; at most one duplicate per event, consistent with Contract #3 §3.4 / `lattice-architecture.md:184`.
7. **TTL coherence.** The outbox aspect carries `TTL = TrackerTTL` (24h) and expires in lockstep with the tracker; its PURGE marker is acked + skipped by the consumer (no error, no republish).
8. **Existing event-publish contract intact.** Subjects remain `events.<class>` (`step9_publish.go:195`); step-7 event DDL validation against `vtx.meta.event.<name>` is unchanged; conformance suite (`internal/processor/conformance_test.go`) stays green.

---

## 5. Test plan

**Unit (`internal/processor`, `internal/processor/outbox`)**
- `Commit` builds the outbox `BatchOp` from the real `EventList`; assert aspect key/shape/TTL and that `data.events` round-trips the full `Event` structs (extend `step8_commit_test.go`).
- Zero-events op writes no outbox `BatchOp`.
- Outbox consumer handler: `published:true` → skip+ack; `published:false` → publish (mock `EventPublisher`/Conn) → marker write → ack; publish error → nak; empty body → ack+skip; unparseable → term.
- Delete `step9_publish_test.go` cases that exercise `RebuildEventListFromClasses`; replace with outbox-fidelity unit tests.

**Integration (faithful, embedded NATS — mirror `internal/processor/integration_test.go`, which spins up `nats-server/v2/test` + `nats-server/v2/server`)**
- End-to-end happy path: submit an op with a non-empty event payload → assert one delivery on `core-events` with the exact payload; assert outbox aspect `published:true`.
- **Crash-between-commit-and-publish:** wire a pipeline whose step-9 publisher is forced to fail (or is absent), commit durably, then run the outbox consumer against the live stream → assert the real (non-empty-payload, original-`eventId`) events arrive on `core-events`. This is the AC-2 regression that the current code fails.
- Consumer restart / redelivery: stop+restart the consumer, assert no duplicate beyond at-most-one and no loss.
- TTL purge: shorten TTL in a test, assert the PURGE marker is acked and triggers no republish.
- Use the project's faithful CI preamble/embedded-server style already present in `integration_test.go` (real JetStream, atomic batch, `AllowAtomicPublish` on `core-events`, `bootstrap.provisionStreams`).

---

## 6. Files to change / add

**Add**
- `internal/processor/outbox/consumer.go` — durable outbox consumer (mirror `internal/refractor/consumer/bootstrap.go`): create-or-update durable `processor-outbox` on `KV_<coreBucket>`, filter `vtx.op.*.events`, publish via shared publisher, marker write, ack discipline.
- `internal/processor/outbox/consumer_test.go` — handler unit tests.
- `internal/processor/outbox_aspect.go` (or fold into `tracker.go`) — `OutboxAspect` type, `OutboxAspectKey(requestId) = "vtx.op."+requestId+".events"`, marshal/parse, `Class = "op-outbox"`.

**Change**
- `internal/processor/step8_commit.go` — in `Commit`, after `BuildEventList`, append a `BatchOp` for the outbox aspect (when `len(events) > 0`) with `CreateOnly:true`, `TTL: TrackerTTL`. Keep `eventClasses`/`mutationKeys` on the tracker for traceability (Contract #4 §4.2 still wants them) — only the *publish source* moves to the aspect.
- `internal/processor/commit_path.go` — replace `markEventsPublished` (eventsPublishedAt) with the fork-C `published` marker write on the outbox aspect; **delete** `maybeRepublishEvents` and its two call sites (`commit_path.go:151`, `:294`). Step-9 success now marks the outbox aspect published.
- `internal/processor/step9_publish.go` — **delete** `RebuildEventListFromClasses` (and `PublicationError` stays). Keep `EventPublisherImpl.Publish` / `EventSubject` (shared by step 9 and the consumer).
- `cmd/processor/main.go` — construct and `go outboxConsumer.Run(ctx)` alongside `cp.Run` (`:114-138`).
- `internal/bootstrap/primordial.go` — if a new `op-outbox` event/aspect needs no DDL (it's platform infra, like the tracker which has no DDL), confirm no bootstrap change is needed; the `core-events` stream config (`:144-151`) is unchanged. **Verify** the outbox consumer's durable can be created against `KV_<coreBucket>` (the stream already exists; refractor does the same).

**Docs (→ `/docs`, by Winston, not in this story's code change)**
- Contract #4: add §4.2 note that the publish-source `EventList` lives on `vtx.op.<id>.events` (outbox), distinct from `data.eventClasses` (traceability only).
- `lattice-architecture.md` Commit Path step 9 / "Transactional outbox" block: reflect the consumer + marker, drop the "re-derive" language.

---

## 7. Risks & open questions (need Andrew's call)

- **Fork A (persistence location)** — aspect vs. tracker-root field. CS recommends the aspect. *Ratify.*
- **Fork B (keep step 9?)** — Option 1 (fast-path + backstop) vs. Option 2 (single path through outbox). CS recommends Option 1 on the <500ms auth-coupled latency ceiling, but Option 2 is meaningfully simpler. **This is the consequential call** — it decides whether steady-state Phase-2 event latency carries an extra CDC hop. *Ratify.*
- **Fork C (republish guard)** — explicit `published` marker vs. consumer-idempotency-only. CS recommends the marker (clean two-producer model). *Ratify; tied to B — if B=Option 2, C can be C1 since there's only one producer.*
- **Subject wildcard** — confirm `$KV.<bucket>.vtx.op.*.events` matches every `requestId` (NanoID has no dots, so `*` should span it). If not, fall back to `vtx.op.>` + key-shape gate. (Implementation detail, not Andrew's call.)
- **Consumer co-location** — run the outbox consumer in the Processor binary (recommended, mirrors refractor) vs. a separate process. Affects deployment/isolation (`6-3-deployment-isolation-specification.md` may have an opinion).
- **PURGE-marker edge** — confirm the 24h TTL PURGE on the outbox aspect (and an *unpublished* aspect that expires before the consumer runs) is acceptable: if an op commits, the Processor and consumer are both down for >24h, the outbox aspect PURGEs and those events are never delivered. This matches the existing 24h idempotency horizon (Contract #4 §4.3) but is worth an explicit acknowledgement that the outbox's durability is bounded by the same 24h TTL. *Andrew: is 24h acceptable for the outbox, or should the outbox aspect outlive the dedup tracker?*

---

## 8. What this removes

- **`RebuildEventListFromClasses`** (`internal/processor/step9_publish.go:14-45`) — the best-effort reconstruction that fabricates fresh `eventId`s and empty payloads. Deleted entirely.
- **`maybeRepublishEvents`** (`internal/processor/commit_path.go:520-570`) and its two call sites (`commit_path.go:151` in the step-2 dedup short-circuit, `commit_path.go:294` in the concurrent-redelivery branch). The outbox consumer replaces this responsibility.
- **`markEventsPublished` / `eventsPublishedAt`** semantics (`commit_path.go:572-589`) — superseded by the fork-C `published` marker on the outbox aspect.
- The corresponding `RebuildEventListFromClasses` test coverage in `step9_publish_test.go`.

After this story, **there is no code path that reconstructs events from KV keys.** Every event published to `core-events` — primary path or redelivery — originates from the script-returned `EventList` persisted in the step-8 atomic batch.
