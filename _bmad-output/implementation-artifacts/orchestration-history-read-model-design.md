# THE CHRONICLER — event-ledger materializer (durable orchestration history + ledger archival)

**Status: ✅ Andrew-ratified (2026-07-02) — Fork C: a NEW COMPONENT (neither Fork A/Refractor nor Fork B).**

> ## RATIFICATION REWORK (2026-07-02) — supersedes the Fork-A/B framing below
>
> Every body mention of "extend Refractor / LensSpec / the pipeline" is **superseded**: the event→row
> projection model, convergent-target semantics, package-owned definitions, and P5 read path all carry
> over **verbatim** — only the HOST changes. Grounds:
> - **The decisive fact:** Refractor's founding charter EXCLUDES event streams (*"Consumes … Core KV
>   change feed via NATS KV watcher — NOT `lattice-events`"*, brainstorm Stream-2 boundary :573). Fork A
>   would cross that line inside the auth-plane-critical binary — and everything it reused (adapter SPI,
>   `ConsumerSupervisor`, the `healthkv.Reporter`) is importable libraries needing no co-residence.
> - **The Chronicler:** Refractor projects convergent PRESENT STATE from Core-KV CDC; the Chronicler
>   materializes APPEND-ONLY HISTORY from platform streams, never evaluated (no cypher, no adjacency).
>   Two modes: **(1) PROJECT** an event stream into a convergent history read model; **(2) ARCHIVE** a
>   ledger stream verbatim into unlimited storage — the object-store plane, sequenced segments + a
>   manifest carrying first/last-seq + prev-segment chain, ack-only-after-durable-write. **No
>   auto-trim** of the live stream in v1 (trimming behind the archived watermark is operator policy,
>   separate from archiver correctness). Dev-posture honesty: ephemeral dev stacks wipe archives; the
>   driver is the PRD durability/audit promise + FR51's substrate, and the mechanism being proven.
> - **Fires (Andrew's ordering):** **F1** the component + mode 1 + `loomFlowHistory`
>   (orchestration-base ships the definition — package data; small binary in the bridge/objmgr weight
>   class assembled from `ConsumerSupervisor` + the adapter SPI + `healthkv.Reporter`) · **F2 Weaver
>   history** (`events.weaver.>`) · **F3 archive mode** + the core-operations intent-ledger archive
>   (the PRD "unlimited retention path", prd.md:387-391). Display rides **Loupe 2.0 F6** (its board
>   already lists this design as the durable-history cross-feed) — no `cmd/loupe` work here.
> - **Re-homing:** FR51's intent-ledger + committed-delta archives re-home to the Chronicler on revive
>   (retiring its "dedicated lens adapter" contortion — the Chronicler tails the Core-KV stream as a
>   raw sequential feed, touching none of Refractor's evaluate machinery); #68 op-trace is a future
>   Chronicler definition; #37 backfill/replay stays Refractor's (CDC world).
> - **No contract change** (§10.9 is permissive — "remains an option"; components are not
>   contract-enumerated — the objmgr/gateway precedent).

> ## PRE-BUILD REGROUNDING (2026-07-05, Steward) — four corrections before Fire 1
>
> Grounded against current code (not assumed) ahead of building. All four affect the shape below —
> read this before the body.
>
> 1. **Problem statement (§1.1) overstates the gap.** `internal/loom/control.go` (`ListInstances` /
>    `InspectInstance`) and `internal/loom/state.go`'s `transition` show the terminal batch deletes
>    **only the pattern pin** (`instance.<id>.pattern`) — the **instance cursor
>    (`instance.<id>`) persists** with `Status` flipped in place, and the control plane's `list`/`inspect`
>    **already surfaces retained terminals today**. "The moment a flow completes or fails it is gone" is
>    **false**. The real, narrower gap: `InstanceSummary`/`Instance` carry no failure **reason** (the
>    `loom.patternFailed{reason}` event's `reason` is never persisted to the cursor) and no
>    **timestamps** (`started_at`/`ended_at`) — so "why did it fail" and "throughput over a window" are
>    still unanswerable — plus `loom-state` is an **operational RPC bucket, not a P5 lens target**, so
>    Loupe has no read-model path to it (Loupe's Core-KV exception doesn't reach `loom-state` either — it
>    isn't Core KV). The Chronicler's value is real, just narrower: reason + timestamps + a queryable P5
>    read model + a bound on `loom-state`'s unbounded terminal-record growth — not "resurrecting vanished
>    flows."
> 2. **§2.2's projection example uses the wrong field names.** `internal/processor/step7_events.go`'s
>    `Event` struct publishes `{eventId, requestId, eventType, domain, targetKey, payload, timestamp}` —
>    the DDL script's `data` dict (`packages/orchestration-base/loom_lifecycle.go`) becomes the **`payload`**
>    field, not `data`. So `"data.instanceId"` below must read **`"payload.instanceId"`**, and
>    `"envelope.committedAt"` must read **`"timestamp"`** (a top-level `Event` field — there is no
>    `committedAt` or nested `payload.timestamp`). **`last_event_seq` is not a JSON envelope
>    field at all** — it comes from the JetStream delivery metadata the pipeline's consumer handler
>    already receives (`substrate.Message.Sequence`, populated from `msg.Metadata().Sequence.Stream` —
>    `internal/substrate/consumer.go:newMessage`), the same value Refractor's coreKv pipeline already
>    reads off the wire. The mapper config should name it `"message.sequence"` (or similar transport-level
>    key), not `"envelope.streamSeq"` — it is plumbed in by the pipeline runtime, never parsed out of the
>    event body.
> 3. **F2 (Weaver history, per the ratification-rework banner below) names a producer that doesn't
>    exist.** Grep confirms **no `.go` file publishes to `events.weaver.*`** or any `core-events` subject
>    from `internal/weaver` — Weaver's only outbound publish today is `ops.<lane>` (op submission to the
>    Processor; `internal/weaver/actuator.go`). §7 below already scopes Weaver parity as a **contingent
>    follow-on** ("hand a consumer an unbuilt producer" — memory `feedback_designer_chain_grounding`); the
>    banner's F2 ordering must be read through that lens: F2 is **blocked on Weaver first emitting a
>    lifecycle event stream** (a separate, unscoped design/build), not a same-fire deliverable. Treat the
>    banner's "F2 Weaver history" as **F2 (contingent, unscoped producer)** until that producer exists;
>    F1's `eventStream` primitive + `loomFlowHistory` consumer (Loom, which already emits `loom.*` events)
>    is the buildable core.
> 4. **F3 (archive mode) needs a dedicated Object Store bucket, not a new GC exemption mechanism.**
>    `internal/objectmanager/manager.go`'s reconcile sweep is scoped to exactly one configured bucket —
>    `m.cfg.ObjectsBucket`, bound to `bootstrap.CoreObjectsBucket = "core-objects"`
>    (`cmd/object-store-manager/main.go`) — it never scans the whole Object Store namespace. So archive
>    segments are GC-safe **by construction** as long as they land in a **separate, dedicated bucket**
>    (e.g. a new primordial `chronicler-archive` Object Store, provisioned in `internal/bootstrap/primordial.go`
>    exactly like `core-objects` is) rather than sharing `core-objects` — no new exemption/fencing code in
>    objmgr is needed. Note this for F3's design when that fire is scoped; it does not affect F1/F2.
>
> **FIRE 2 AS-BUILT (2026-07-05, Steward) — row key simplified from the originally-drafted
> `flow.<instanceId>` to a bare `<instanceId>` value** (§2.6/§3 below already reflect this). The shipped
> `loomFlowHistory` lens (`packages/orchestration-base/lenses.go`) keys rows by the bare instance NanoID.
> Reason: the `eventStream` primitive's `EventProjection.Key` (§2.2) is a plain dot-path
> resolution — one of the three `ColumnMapping` shapes (bare path / `{from,map}` / `{when,value}`), none
> of which can concatenate a literal string prefix onto a resolved value. Producing a real `flow.` prefix
> would mean extending the Fire-1 primitive with new templating machinery (a `KeyPrefix`-shaped field) for
> a single-purpose bucket that, as of this fire, only `loomFlowHistory` ever writes to — no other entity
> shares `orchestration-history`, so the prefix buys no disambiguation today. Revisit only if a second
> `eventStream` lens is ever pointed at this same bucket (not currently planned; Weaver history, §7, would
> get its own dedicated bucket like this one did). No `KeyColumn`/`Output.OutputKeyPattern` mechanism was
> used either — that machinery is actor-aggregate-only (§6.13), which this lens does not opt into.

· Designer fire 2026-06-30 (Winston); reworked at ratification 2026-07-02 · Lattice lane
**Backlog row:** "Loom / Weaver control-API surfacing" (`backlog/lattice.md` → Refinements & ops) — ★ · M

---

## For Andrew (read this first)

**What it does, in two lines.** Loom flows today vanish from queryability the instant they finish:
`loom-state` holds only *live* cursors (the pin + cursor are deleted in the terminal batch), and the
control plane reads `loom-state`, so "which flows completed / why did this one fail / how many onboarding
flows ran today" is unanswerable. This design adds a **durable historical read model** of every flow's
lifecycle — built the architecturally-blessed way: a **new Refractor lens that sources the `loom.*` event
stream** (not Core-KV CDC) and projects one convergent history row per instance into a read-model bucket
Loupe reads over the **P5 read path**, exactly like every other read model.

**The one design decision I made for you to confirm (a fork).** A durable orchestration read model has two
real shapes. I designed both through and **recommend A**:

- **Fork A — a reusable `eventStream` lens-source primitive (RECOMMENDED).** Teach Refractor to source a
  lens from an *event stream* (`events.loom.>`) instead of Core-KV CDC, projecting the event payload into a
  convergent target row. Loupe reads the target via P5.
  - **Pro:** It is the path the frozen contract *already names* — Contract #10 §10.9: *"A Refractor lens
    over the `loom.*` event stream remains an option for a durable read model if one is later wanted."* It
    needs **NO frozen-contract change**. It is **P5-pure** (read models are lenses; Loupe reads the lens
    target, not an RPC). And it is **reusable** — the platform currently *cannot* build a read model from an
    event stream (only from Core-KV CDC); this primitive unlocks Weaver history, an audit/trace read model
    (brainstorm #68 "the `vtx.op` IS the trace"), and the historical-ledger backfill direction (#37).
  - **Con:** It is a genuinely new Refractor projection *mode* — it diverges from the established "re-execute
    cypher over Core-KV" model (an event lens has no Core-KV vertex to `MATCH`; the event payload *is* the
    data). That divergence is the cost, and it is why I am surfacing it rather than just building to it.

- **Fork B — a control-plane history hack (the cheaper alternative I rejected).** Loom writes a
  `history.<instanceId>` record into `loom-state` (TTL'd) at terminal, before deleting the cursor; the
  control plane gains a `history` read; Loupe reads it through the existing control-proxy.
  - **Pro:** Smaller; 1–2 fires; faithful to the *current* "control plane reads operational state" precedent.
  - **Con:** It **requires a frozen-contract edit to §10.3** (the `loom-state` key list is FROZEN and
    enumerated to exactly five shapes; adding `history.<id>` is a contract change) and it **bends §10.9's
    ratified "instance is operational-only, queryability via the control plane" intent** further into a
    durable store. It is **RPC-fronted, not P5** (Loupe reads via control-proxy, not the read path). And it
    is **not reusable** — it buys Loom history only.

**My recommendation: Fork A.** It is contract-*blessed* rather than contract-*breaking*, P5-aligned, and
buys a reusable primitive for a ★ feature's price — the divergence is real but well-fenced (the source kind
gates the pipeline mode; an event lens carries no cypher). **There is no uncommitted frozen-contract edit in
this fire** — Fork A needs none; I left the contracts untouched. (If you prefer Fork B, say so and I will
prepare the §10.3 edit and re-decompose.)

**No other architectural fork** (no Gateway / read-path-auth / Vault / multi-cell / HA-NATS surface is
touched). The Steward builds this **only after ✅ Andrew-ratified**.

---

## 1. Problem & intent

### 1.1 The gap (grounded — corrected 2026-07-05, see the regrounding banner above)

Loom is the deterministic procedure engine. A flow ("instance") is **operational-only**: per the frozen
Contract #10 §10.9 and P1, it has **no Core-KV vertex** — its sole durable home is the `loom-state` cursor
(`instance.<instanceId>` + the pinned `instance.<instanceId>.pattern`). The terminal batch
(`CompletePattern`/`FailPattern`) **deletes only the pattern pin** (`internal/loom/state.go`'s
`transition`) — the **instance cursor persists**, its `Status` flipped in place — so that
`instance.*.pattern` listing yields exactly the *live* set (it drives the §10.9 per-domain consumer
reconcile) while `instance.<id>` listing (`ListInstances`) yields running instances **and retained
terminals alike**.

So the control plane (`internal/loom/control`, `lattice.ctrl.loom.list/consumers/inspect`) does **not**
lose terminal flows — `ListInstances`/`InspectInstance` already return them with their final `Status`. The
real, narrower gap is what the retained cursor **doesn't carry** and **who can read it**:

- *Why did this onboarding flow fail?* — `Instance` has no `Reason` field; `loom.patternFailed{reason}` is
  emitted onto `core-events` and then **never persisted anywhere queryable** — the reason exists for one
  event's lifetime only.
- *Which flows completed in the last day? How long did they take?* — `Instance` has no `started_at`/
  `ended_at`; there is no timestamp on the cursor at all, so throughput/duration queries are unanswerable
  even though the record survives.
- *Can Loupe show this?* — `loom-state` is Loom's private **operational RPC** bucket (P2 — only Loom
  writes it, read via the control-plane micro-service), not a **P5 lens read-model target**. Loupe has no
  path to it (its Core-KV exception doesn't extend to `loom-state`, which isn't Core KV either) —
  filtering/sorting/paging over flow history needs a real read model, not an RPC round-trip.
- *Is `loom-state` growing without bound?* — retained terminal cursors are **never pruned**; the
  operational bucket accretes forever. Splitting history into a dedicated, purpose-built read model is
  also how `loom-state` itself stays bounded to live + recent instances (a follow-on, not scoped here).

The shipped control plane already covers **operator pause/resume** (the other half of the backlog row);
this design covers the remaining half: **a durable, P5-readable `loom.*` history read model carrying
reason + timestamps** — not resurrecting flows that were never actually deleted.

### 1.2 Intent & vision lineage

This is the P1-respecting realization of a long-standing intent. The early Loom vault note
(`Obsidian Vault/Lattice/Loom/The Loom.md`) imagined an `asp.loom.instance.state` aspect on a Core-KV
*Instance Vertex* recording "a history of transitions … a perfect audit trail of the workflow's history."
The frozen architecture **replaced** that Core-KV instance vertex with operational-only `loom-state` (P1) —
which is correct for the *live* cursor but discards the *audit trail*. A durable read model **restores the
audit-trail intent without resurrecting the forbidden Core-KV vertex**: the history lives in a *lens target*
(a derived read model), reconstructed from the durable `loom.*` lifecycle events on `core-events`.

It also lands three brainstorm items on the same primitive:

- **#96 closed-loop Weaver auditor** (reads Health-KV, issues remediation) — a flow-history read model is a
  prerequisite observability surface for the on-platform auditor / FR54 anomaly detection.
- **#68 "operation-id → trace span correlation — the `vtx.op` IS the trace"** — an event-stream lens is the
  general mechanism for projecting an event log into a queryable trace/audit read model.
- **#37 backfill/replay engine for new lenses against the historical ledger** — the same "project an event
  stream into a durable target" shape.

### 1.3 Why the existing Refractor model does not already do this

Refractor lenses **re-derive state by re-executing cypher over Core-KV** on each CDC event — the executor
*ignores the event payload* and re-scans Core-KV (refractor.md "Anchor-tombstone retraction":
*"`ExecuteWith` re-derives a lens's rows by re-scanning Core KV (it ignores the CDC event's payload)"*).
That model **cannot** project loom flows: there is **no Core-KV vertex to `MATCH`** (P1) — the flow exists
only as a sequence of events. The event payload *is* the only data. So a durable loom read model needs a
**fundamentally different projection mode**: *event-sourced* (the event body maps to a row) rather than
*state-sourced* (re-derive the current row from Core-KV). That mode is the `eventStream` primitive.

---

## 2. The shape

### 2.1 Read path (P5) and write path (P2)

- **Write path (P2 — unchanged).** Loom's lifecycle ops (`StartLoomPattern`/`CompletePattern`/`FailPattern`,
  `packages/orchestration-base/loom_lifecycle.go`) already commit through the **Processor** and emit
  `loom.patternStarted/Completed/Failed` via the standard `vtx.op.<requestId>.events` outbox aspect onto
  `core-events`. **Nothing on the write path changes.** Refractor remains a pure consumer; the Processor
  stays the sole Core-KV writer. The read-model target is written by Refractor, like every other lens target.
- **Read path (P5).** The history read model is a **read-model target bucket** (`orchestration-history`),
  written by Refractor, read by Loupe via `KVGet`/`KVListKeys` — **identical** to how `cmd/loftspace-app`
  reads `weaver-targets` (the P5 precedent; CLAUDE.md P5 / memory `feedback_p5_lens_read_path`). Loupe is the
  inspector but here it does not even need its Core-KV exception — it reads a lens target the ordinary way.

### 2.2 The `eventStream` lens source (the new primitive)

`LensSpec` gains an optional **`source`** descriptor (default preserves every existing lens unchanged):

```jsonc
// existing lenses: source absent ⇒ {kind:"coreKv"} — re-execute cypher over Core-KV CDC (today's behavior)
// new event lenses:
"source": {
  "kind": "eventStream",
  "subjects": ["events.loom.>"],          // the durable JetStream subjects to consume (core-events)
  "project": {                             // declarative event-body → row mapping (NO cypher)
    "key":   "payload.instanceId",         // the target row key (one row per instance)
    "columns": {
      "instance_id":    "payload.instanceId",
      "pattern_ref":    "payload.patternRef",
      "subject_key":    "payload.subjectKey",
      "status":         { "from": "eventType", "map": {              // eventType → status enum
                          "loom.patternStarted": "running",
                          "loom.patternCompleted": "complete",
                          "loom.patternFailed":  "failed" } },
      "failure_reason": "payload.reason",
      "started_at":     { "when": "loom.patternStarted", "value": "timestamp" },   // top-level Event field, NOT payload.timestamp
      "ended_at":       { "when": ["loom.patternCompleted","loom.patternFailed"], "value": "timestamp" },
      "last_event_seq": "message.sequence"  // JetStream delivery metadata, NOT an event-body field —
                                             // plumbed in by the pipeline runtime (substrate.Message.Sequence);
                                             // monotonic convergence guard (see §2.4)
    }
  }
}
```

**Why declarative-mapping, not cypher.** An event lens has no graph to walk — there is no `MATCH`, no
Adjacency, no Core-KV read. Forcing cypher here would be a category error (and a Core-KV-read temptation that
violates the engine boundary). The mapping is a pure, total function `event → row` validated at lens-load
time (the same fail-closed doctrine as guard-grammar parsing, loom.md §guard grammar): an unknown source
`kind`, a cypher body on an `eventStream` lens, or a mapping referencing a non-existent envelope field is a
**load-time reject**, never a silent runtime fallthrough.

This mirrors the established **"recognize and reject wholesale at parse time"** doctrine (loom guard parser;
Refractor `translateSpec`) and the **dark-primitive-then-consumer** fire split of `kv.Links` (Fire 1 shipped
the primitive with no consumer; Fire 2 added the first consumer) — see §8.

### 2.3 The pipeline mode

`startPipeline` (`cmd/refractor/main.go`) branches on `source.kind`:

- **`coreKv` (default)** — today's path: a `substrate.ConsumerSupervisor` durable on the `KV_core-kv`
  backing stream, filtered to the lens's source-key prefix; each event re-executes cypher; upsert/Delete via
  the existing engine + adapter. **Completely unchanged.**
- **`eventStream`** — a `substrate.ConsumerSupervisor` durable on the **`core-events`** stream filtered to
  `source.subjects`; each event runs the declarative `project` mapping → one row; the **same target adapters**
  (`nats_kv`, Postgres) write it. No cypher engine, no Adjacency, no Core-KV read is constructed for an event
  lens. The latency ring buffer, health Reporter, audit/metrics, and control-plane `list`/`rebuild` wiring
  are all reused unchanged (they operate on the pipeline, not the engine).

**Boundary preserved.** `internal/refractor` already consumes JetStream via `substrate/*` only
(memory: the Refractor substrate migration — no raw `nats.go`/`jetstream` in non-test `internal/refractor`).
The event-stream consumer uses the **same substrate surface** that the lens-def `CoreKVSource` and pipeline
consumers already use (`substrate.ConsumerSupervisor` / `RunDurableConsumer`), so no new substrate primitive
is required — the `core-events` stream already exists and is already consumed by Loom/Weaver/the bridge.

### 2.4 Convergence & idempotency (the correctness core)

A durable read model must converge under at-least-once delivery, redelivery, and a from-scratch replay
(`DeliverAll` on rebuild). Two independent events touch one row (Started, then Completed/Failed) and may
arrive **out of order on a replay**. The guard is the **monotonic `last_event_seq`** — the `core-events`
stream sequence of the projecting event:

- The upsert applies **iff the incoming `last_event_seq` strictly exceeds the stored one** for that row key.
  A replayed earlier `patternStarted` (lower seq) **cannot** clobber a later `patternCompleted` (higher
  seq) → terminal status is stable. A redelivered duplicate (equal seq) is a no-op.
- This **reuses the existing `projectionSeq` monotonic-guard doctrine** verbatim (Refractor's guarded
  buckets / Contract #6 §6.14 `actor_read_grants` seq guard, refractor.md "Protected read-model
  provisioning"). The event lens declares its target **guarded** so the adapter applies the seq guard; on a
  guarded-bucket rebuild, truncate is forced (refractor.md "Rebuild & truncate semantics"), which is exactly
  right (see §2.5).
- **`started_at`/`ended_at` are write-once-by-condition:** `started_at` is set only by the `patternStarted`
  event, `ended_at` only by a terminal event. Because the seq guard already orders writes, a row that
  receives Completed-before-Started (replay) still ends with both columns correct once both events have been
  applied in seq order (the higher-seq terminal sets `ended_at` and `status`; the lower-seq Started sets
  `started_at` only if it is the highest-seq writer of that column — implemented as a per-column
  conditional upsert, the same shape as the grant table's column-scoped writes).

### 2.5 Two honestly-bounded limitations (not bugs — operating envelope)

1. **`core-events` has `MaxAge=7d`** (loom.md "Disaster recovery"). The durable target **accumulates beyond
   7 days in steady state** (each event is projected once, as it flows). But a **from-scratch rebuild**
   (`DeliverAll`) can only see the last 7 days of events — history older than 7d **cannot be reconstructed
   by a rebuild**. Mitigation + doctrine: **the history target is the durable record; it is not
   truncate-rebuilt in normal operations.** A rebuild is explicitly a "last-7d reconstruction" operation,
   documented on the lens. (If long-horizon durable history is ever required, the clean extension is a
   dedicated longer-retention `loom.*` event stream or a Postgres target with archival — noted as a
   follow-on, not built now. This is the same trade the platform already accepts for any event-sourced view.)
2. **An orphaned `running` row** (a flow whose terminal event is lost, or whose engine died mid-flight)
   persists as `running` forever. This is **observational value, not a leak**: a `running` history row with
   no matching live `loom-state` cursor and no terminal event is precisely the "stuck flow" signal the
   Lamplighter / #96 auditor wants. The read model surfaces it; it does not pretend it completed. (Loupe's
   Flows tab can cross-reference the live control-plane `list` to badge a history `running` row as
   "live" vs "orphaned".)

### 2.6 The read model — `loomFlowHistory` lens (package data)

A `meta.lens` definition (package data — **not** engine code; Decision #10), installed in
`orchestration-base` (the package that already owns the `task` DDL + the loom lifecycle DDL):

| Field | Value |
|---|---|
| `class` | `meta.lens` |
| `engine` | n/a (event lens — no cypher engine) |
| `source` | `{kind: "eventStream", subjects: ["events.loom.>"], project: {…}}` (§2.2) |
| `targetType` | `nats_kv` (v1 — see §6 alternatives for Postgres) |
| `targetConfig.bucket` | `orchestration-history` (joins the primordial bucket create list, like `weaver-targets` §10.2 / `loom-state` §10.3 — a **code** change in `internal/bootstrap/primordial.go`, not a contract change) |
| `targetConfig.guarded` | `true` (the §2.4 seq guard) |
| row key | bare `<instanceId>` (as-built; see the Fire 2 as-built note above — the primitive's `Key` has no prefix-templating) |
| row columns | `instance_id, pattern_ref, subject_key, status, started_at, ended_at, failure_reason, last_event_seq` |

### 2.7 Loupe surfacing (P5)

A Loupe **"Flows"** tab (`cmd/loupe`), built UX-then-FE per the Loupe model (memory:
`reference_run_full_stack` + the fe-engineer/Sally pairing). The Go handler reads `orchestration-history` via
`KVListKeys` + `KVGet` — **copy `cmd/loupe/corekv.go`'s list/get shape and `cmd/loftspace-app`'s
read-model-bucket read** (the P5 precedent), filter/sort in-handler (by `status`, `pattern_ref`, time
range). It is read-only; no new control-plane op. Cross-reference the live `lattice.ctrl.loom.list` to badge
`running` rows live-vs-orphaned (§2.5.2). Live-verified against `make up-full` (Loupe :7777).

---

## 3. Naming & key-shape conformance (Contract #1)

- The read-model **row key** (the bare `<instanceId>`, as-built — see the Fire 2 note above) lives in a
  **lens target bucket** (`orchestration-history`) — a derived read model, **not** Core KV — so it is *not*
  governed by the `vtx.`/`lnk.`/`asp.` key-shape rules (those govern Core KV; lens targets use their own row
  keys, exactly as `weaver-targets` rows are keyed by `<targetId>.<entityId>.<gapColumn>` per §10.2, not a
  `vtx.` shape). No Contract #1 surface.
- No new vertices/aspects/links are introduced (the flow has no Core-KV footprint — that is the whole point).
  The lens-def meta-vertex is an ordinary `vtx.meta.<NanoID>` + `.spec` aspect (4-segment), seeded by a
  `CreateMetaVertex` op like every other lens (refractor.md "Lens definitions live in Core KV vertices").

---

## 4. Contract surface (change-vs-build-to)

**No frozen-contract change** (Fork A). Precisely:

- **Contract #10 §10.9** is **built to, not changed** — it *already* sanctions "a Refractor lens over the
  `loom.*` event stream … for a durable read model." This design is the realization of that sentence.
- **Contract #10 §10.3** (frozen `loom-state` key list) is **untouched** — Fork A adds **no** `loom-state`
  key (that is Fork B's cost). `loom-state` stays operational-only.
- **Contract #10 §10.2** (Weaver target lens *output* shape) is **not** the model here — this is a loom
  event lens, a distinct read model. No §10.2 edit.
- **The `loom.*` event payloads** (`loom_lifecycle.go`) are **consumed as-is** — `{instanceId, patternRef,
  subjectKey, requestId}` / `{instanceId}` / `{instanceId, reason}`, plus the standard event envelope
  (`committedAt`, stream sequence). **No op/DDL change**, so no Contract #2/#3 surface.
- **`LensSpec`** (the `source`/`project` descriptor) is described in **refractor.md** (a component doc,
  updated in-commit with the code) — it is **not a frozen contract**, so extending it is build-to work.

The only doc edits are **build-time component-doc updates** (refractor.md: the `eventStream` source + the
event-projection pipeline mode; loom.md: replace the "remains an option" note with "served by the
`loomFlowHistory` lens"), made by the Steward in the implementing commit — **not** part of this Designer
fire, and **not** frozen contracts.

---

## 5. Migration & test strategy

- **Zero migration of existing lenses.** `source` absent ⇒ `{kind:"coreKv"}`; every shipped lens is byte-for-
  byte unchanged. The new bucket is created on first install (primordial create list).
- **Backfill on install.** When the `loomFlowHistory` lens is installed, its `DeliverAll` start replays the
  last 7d of `loom.*` events into the empty bucket — instant history for recent flows (§2.5.1 bounds it).
- **Test pyramid:**
  - *Unit* — the `project` mapping (`event → row`) total-function tests: each class → status; the seq-guard
    monotonicity (replayed lower-seq Started does not clobber a Completed); load-time rejects (cypher on an
    event lens; unknown `kind`; bad envelope field). Pure, no NATS.
  - *Integration (embedded NATS 2.14, `jsstore.Dir(t)` per memory `project_ci_test_parallelism`)* — publish
    `patternStarted`→`patternCompleted` onto `core-events`; assert the `orchestration-history` row converges
    `running`→`complete` with correct `started_at`/`ended_at`; publish out-of-order on a rebuild and assert
    convergence; publish `patternFailed{reason}` and assert `failed` + `failure_reason`.
  - *e2e* — `make up-full`: trigger an onboarding flow, complete it, assert Loupe's Flows tab shows it
    `complete`; trigger + let one fail, assert `failed{reason}`. Live-verify (headless curl first, then
    Loupe :7777 per memory `feedback_loupe_inbrowser_verify_unattended`).
- **Gates** (CLAUDE.md): `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
  `make test-bypass` (Gate 2 BLOCKED), `make test-capability-adversarial` (Gate 3 DEFENDED), the refractor +
  orchestration-base + loupe `go test` packages, and `make verify-package-orchestration-base` (DDL/lens
  changed — memory `feedback_verify_package_gate_gap`).

---

## 6. Risks, alternatives, and adversarial findings

**Self-conducted adversarial pass** (the design is cross-cutting — a new Refractor projection mode — so per
the Designer SKILL it gets an adversarial review; folded in below):

| # | Risk / challenge | Resolution |
|---|---|---|
| R1 | **New projection mode dilutes the "lenses re-derive state from Core-KV" model.** | Fenced by the `source.kind` gate: an event lens is a *distinct, validated* shape (no cypher, no Adjacency, no Core-KV read). The two modes never mix in one lens (load-time reject). The divergence is real and is the explicit fork flagged for Andrew (§For-Andrew) — not smuggled in. |
| R2 | **Out-of-order / replayed events corrupt a row.** | Monotonic `last_event_seq` guard (§2.4), reusing the shipped `projectionSeq` doctrine. A replayed lower-seq event is rejected; convergence is on the highest-seq writer per column. Tested explicitly. |
| R3 | **7d source retention ⇒ rebuild loses old history.** | Honestly bounded (§2.5.1): the target is the durable record, not truncate-rebuilt in normal ops; a rebuild is a documented last-7d operation. Long-horizon history is a noted follow-on (longer-retention stream / Postgres archival), not built now — avoids over-engineering a ★ feature. |
| R4 | **Orphaned `running` rows accumulate.** | Reframed as the intended "stuck flow" signal (§2.5.2), cross-referenced against the live control plane in Loupe. Observational, not a leak. |
| R5 | **P5 violation risk** (Loupe reaching into operational state). | Avoided by construction: Loupe reads the `orchestration-history` *lens target* via `KVGet`/`KVListKeys`, the P5 precedent (`loftspace-app`/`weaver-targets`). It does not read `loom-state`. |
| R6 | **Engine-boundary creep** (an event lens tempted to `kv.get` for richer columns). | Forbidden by the mode: the event payload is the only data source; the `project` mapping cannot reference Core-KV. If a column truly needs Core-KV state, that is a *state* lens (`coreKv`), not an event lens — a different tool. (Consistent with memory `feedback_no_new_engine_corekv_reads`: no new engine Core-KV reads.) |
| R7 | **Parallel in-flight design overlap.** | Checked (memory `feedback_designer_parallel_overlap_and_retraction`): no other `📐`/`🏗️` design touches Refractor lens sourcing or loom history. The "Negative/filter-retraction" and "Link-triggered reprojection" designs are *coreKv*-mode retraction semantics — orthogonal. No overlap. |
| R8 | **Retraction needs a transport** (the upsert-only trap). | A flow row is never *retracted* — it transitions `running`→terminal and persists by design (it is history). There is no "dropped composite key → over-grant" failure mode here (that risk is for grant/auth lenses). The seq guard is the only ordering transport needed. |

**Alternatives considered and rejected:**

- **Fork B (control-plane history in `loom-state`)** — §For-Andrew: needs a §10.3 frozen edit, bends §10.9,
  RPC-fronted (not P5), not reusable. Documented as the cheaper option if Andrew prefers it.
- **A Core-KV instance vertex + ordinary state lens** — **violates P1** (§10.9 "NO Core-KV instance vertex",
  binding). Rejected outright.
- **Loom writes the read model directly to its own bucket** — would make Loom a second writer of a queryable
  read model and re-implement projection/seq-guarding inside the engine; Refractor is the platform's
  projection component. Rejected (keep the engine minimal, Decision #10).
- **Postgres target for v1** — richer SQL queries, but Loupe's read pattern is KV-first and the v1 demand
  (list + filter recent flows) is served by a KV bucket. Postgres is the natural follow-on when rich
  time-range/aggregate queries are wanted (§7 Fire 4). KV v1 is the smaller green increment.

---

## 7. Weaver parity (scoped honestly)

The backlog row reads "Loom / **Weaver** control-API surfacing." The **primitive is general** (Fire 1
serves any `events.<domain>.>` stream). But a Weaver history read model is **thinner and contingent**:
Weaver is *convergence*, not *instances* — it does not have flows that start/complete/fail; it has standing
target violations + reclaim actions, and its current event surface is mostly *it acting* (submitting
`triggerLoom` ops), not a self-describing `weaver.*` lifecycle stream. Designing a Weaver history projection
now would **hand a consumer an unbuilt producer** (memory `feedback_designer_chain_grounding` — never assume
an unbuilt/unverified producer). So Weaver parity is a **documented follow-on (Fire 4), contingent on Weaver
first emitting a lifecycle/action event stream** — out of scope for the core value, which is entirely
Loom-flow history. The general primitive means that follow-on is "add a lens def," not "add machinery."

---

## 8. Fire-by-fire decomposition (for the Steward — each independently shippable + green)

> Build order; each fire lands green on its own. Mirrors the `kv.Links` dark-primitive-then-consumer split.

- **Fire 1 — the `eventStream` lens-source primitive (Refractor).** Extend `LensSpec` with the
  `source`/`project` descriptor + the `eventStream` pipeline mode in `cmd/refractor/main.go` `startPipeline`
  (branch on `source.kind`; default `coreKv` unchanged). Add the declarative `event → row` mapper + load-time
  validation (reject cypher-on-event-lens / unknown kind / bad field) + the seq guard wiring on the target.
  **Green:** unit tests for the mapper + guard + rejects; an integration test with a *throwaway* test event
  lens over a synthetic subject (no real consumer yet — dark primitive). No production lens ships. **Ship.**
- **Fire 2 — the `loomFlowHistory` lens + `orchestration-history` bucket (package data).** Add the
  `meta.lens` def to `orchestration-base` (event lens over `events.loom.>`, §2.6) and add
  `orchestration-history` to the primordial bucket create list. **Green:** the integration + e2e convergence
  tests (§5) — start→complete→`complete`; fail→`failed{reason}`; out-of-order replay converges. The first
  real consumer of Fire 1. **Ship.** (Whole backlog row's "durable read model" half delivered here; Loupe is
  the surfacing polish.)
- **Fire 3 — Loupe "Flows" tab (UX-then-FE).** Sally designs the tab; the fe-engineer builds the Go handler
  (P5 read of `orchestration-history`, copy `corekv.go`) + the FE, with the live-vs-orphaned badge
  cross-referencing `lattice.ctrl.loom.list`. **Green:** handler unit tests + live-verify against
  `make up-full`. **Ship.**
- **Fire 4 (optional follow-on, NOT required for core value).** Either (a) a Postgres `orchestration-history`
  target for rich time-range/aggregate queries, or (b) Weaver parity **iff** Weaver first emits a lifecycle
  event stream (§7). Build only on a concrete pull.

---

## 9. Summary

A durable orchestration history read model, built the contract-blessed way: a **reusable Refractor
`eventStream` lens-source primitive** projecting `events.loom.>` into an `orchestration-history` read model
that Loupe reads over the P5 read path. It restores the long-intended "audit trail of the workflow's
history" without resurrecting the P1-forbidden Core-KV instance vertex, needs **no frozen-contract change**,
reuses the shipped `projectionSeq` convergence doctrine, and unlocks downstream observability (#96 auditor,
#68 trace read model). **The one decision for Andrew: ratify Fork A (the event-lens primitive) over Fork B
(the control-plane-history hack).** Recommendation: **A.**

---

*Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>*
