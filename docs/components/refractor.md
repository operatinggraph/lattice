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
| `adjacency/` | Adjacency KV read/write: `Build` (CAS upsert/remove, with the per-node overflow latch), `Neighbors` (the document read, or the Core-KV fallback read for an overflow-marked node), `EventsForLink` (the directional-event-pair constructor every link consumer shares) |
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
| `keyshredded/` | Durable `events.privacy.keyShredded` listener that nullifies a shredded identity's projected rows (brainstorm #62 — the one sanctioned event-stream listener in Refractor's charter); records `RecordShredFinalization{projectionsNullified}` under the `identity.system.privacy` service actor, declaring the `piiKey` aspect **and that actor's own vertex** in `ContextHint.Reads` — the script reads the latter to refuse an attestation written by any other actor |

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
| **Capability KV** (Contract #6 §6.2) | `capability-kv` bucket | Produced by the bootstrap-seeded Capability Lens; key pattern `cap.<actorType>.<id>` (e.g. `cap.identity.<actorId>`), derived by stripping the `vtx.` prefix from the actor vertex key. Consumed by Processor's step-3 `CapabilityAuthorizer`. |
| **Per-lens target KV buckets** | Bucket name per `LensSpec.targetConfig.bucket` | e.g. `duplicate-candidates` (the identity-hygiene package's Duplicate Candidates Lens). Created on demand if not pre-provisioned. |
| **Postgres rows** | Target table per `LensSpec.targetConfig.table` | For SQL-target lenses. The adapter is **thin**: it upserts one column **per RETURN field** (`INSERT … (book_id, title) … ON CONFLICT DO UPDATE`) and issues no DDL. The target table is **provisioned out-of-band** (a migration), with columns matching the lens RETURN (key columns + projected fields). Delete projection is **mode-dependent** (`targetConfig.deleteMode`, default `hard`): the default hard delete issues `DELETE FROM` and needs only the key + projected columns; `deleteMode: soft` issues `UPDATE … SET is_deleted=true, deleted_at=NOW()` and **requires** the `is_deleted` / `deleted_at` columns. The Refractor does not create or alter the table. |
| **Health KV signals** (Contract #5) | Health KV `health.refractor.<instance>.lens.<canonicalName>` | Per-lens latency snapshots (p95, p99, mean, count from `LatencyRingBuffer`); consumer lag; per-instance heartbeat every 10s. |
| **Audit subjects** | `lattice.refractor.audit.<lensId>` | One `AuditEntry` per projection. Every lens's audit subject lands on the single consolidated `REFRACTOR_AUDIT` JetStream stream (subject filter `lattice.refractor.audit.>`, 7-day MaxAge, 512 MiB MaxBytes) — one stream for the whole deployment, not one per lens. |
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
  retract. See the design doc for the Edge-side consumption half (client-side `Sources` attribution +
  `frameHW` guard + dead-lens prune), not yet built.
- **Stream provisioning.** The adapter JIT-provisions the backing stream via `substrate.EnsureStream`
  (mirrors the `nats_kv` case's JIT bucket creation) rather than a bootstrap pre-provision, and
  **unions** the lens's `subjectPrefix` wildcard into the stream's existing `Subjects` rather than
  overwriting them — the `SYNC` stream is meant to carry one platform-wide prefix, but this keeps a
  second lens sharing the same stream name safe regardless.
- **Guard posture: unguarded.** A subject publish is a fire-and-forget-shaped append (though the
  underlying JetStream publish is a confirmed round-trip, not a literal fire-and-forget); ordering
  is the stream's per-subject sequence, and the recipient dedups/reorders by envelope `revision`.
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
- **Hydration Hook (Fire PL.4, `internal/refractor/pipeline.Pipeline.Hydrate`).** The cold-start
  catch-up path for a device that missed the SYNC stream's retention window (or is starting for the
  first time): the control-plane RPC `lattice.ctrl.refractor.personal.hydrate` — request body
  `{identityId, deviceId?}`, response `{personalHydrate: {hydrated: true, revision, lenses}}` —
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
  hydration snapshot regress a fresher incremental delta that raced it. When `deviceId` is given and
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
  — a permissive `USING(true)` policy is rejected, not just any SELECT policy). Failures are
  plain (recoverable) errors so the lens auto-resumes once the operator provisions the table.
- **`VerifyGrantTable()`** (on `PostgresGrantWriter`) is the same read-only check for the shared
  **`actor_read_grants`** table — it asserts the expected columns + types so the seq-guarded
  writes and every protected policy's membership subquery have the shape they depend on. The
  grant table is the read-auth source of truth, not a protected business table, so it is not
  itself RLS-locked — only its shape is verified.
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

**Continuous re-verification.** Because the `Probe` is on the periodic supervisor heartbeat, a
posture turned off *after* activation (e.g. `ALTER TABLE … NO FORCE ROW LEVEL SECURITY`)
re-pauses the lens within a heartbeat — stronger than create-once provisioning, which never
re-checks drift.

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

#### `OPTIONAL MATCH … WHERE` null-restore semantics

When an `OPTIONAL MATCH` pattern matches real neighbors but a `WHERE` then excludes
**every** one of them, `applyMatch` preserves the anchor row with the optional
pattern variables bound null — the correct Cypher OPTIONAL MATCH semantics, for every
cypher. The null fallback is constructed from the source binding (`nullBindNewVars`,
shared with `matchPatterns`'s no-match branch), not recovered from the expansion set:
when the pattern matched only real neighbors, the expansion set holds no null row to
recover, so an anchor whose sole neighbor is WHERE-filtered must be null-restored
from the source. This is what makes a dedicated family-filtered `OPTIONAL MATCH …
WHERE` (e.g. the lease lens's `freshUntil` bgcheck match) safe: a no-fresh-match anchor
projects with the optional column null instead of dropping the row (a dropped
convergence row reads to Weaver as an entity deletion).

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
**non-anchor variable is rejected structurally**), so a **neighbor-keyed composite**
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
first and only live `DiffRetraction` lens; a convergence
(`violating`-flag) lens never opts in, so its never-retract contract is untouched.

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
8. **Lens spec update** → `CoreKVSource.updateCB` fires; `ClassifyUpdate` determines the reload: an `INTO`-only change hot-reloads the adapter in place (`IntoOnly`), while a `MATCH` (query) change requires a full pipeline rebuild (`MatchChange`). Both kinds first run one **refusal set** (`cmd/refractor/reload.go`): a change to the `Output` descriptor, `grantTable`, `protected`, `secureColumns`, or — for a guarded lens — its write surface, cannot be carried by either swap. A refused update is recorded as an error on the lens's health entry and the lens keeps serving its **activated** spec; it is not paused, because a lens running its activated spec is doing the right thing. **A refusal only reaches the operator who caused it if something tells them:** lens IDs are version-independent, so a package upgrade edits a spec in place and `lattice-pkg apply` commits successfully. `internal/refractor/reloadpin` restates the spec-derived half of the refusal set over the stored document, and the installer runs it at diff time so an apply that lands a non-hot-reloadable lens edit reports `ReactivationRequired` naming the lens and the remedy. Refractor stays the authority — `reloadpin` only predicts, so drift costs a missing warning rather than a wrong refusal, and `TestPinnedFieldsMatchTheRefusalSet` pins the two together
9. **Lens tombstone** (parent vertex deleted or `.spec` deleted) → `CoreKVSource` purges its spec-tracking maps, logs the removal, and invokes its removal callback (mirroring the load/update callbacks — fired only for a genuine tombstone, never for an in-place spec update). `cmd/refractor` wires that callback to the same `control.Deleter` the operator `delete` control op uses (`internal/refractor/control`'s `RegisterDeleter`): the durable consumer is removed from the `KV_core-kv` stream (`pipeline.Pipeline.RemoveConsumer`, durable removed **before** the pipeline's run context is cancelled — see that method's doc for why the reverse order silently strands the durable), the pipeline is stopped, and its health KV entry is deleted. A sibling lens's durable and registration are untouched. Adjacency entries are left in place (tombstone re-projection is a Phase 3 carry)

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
| Read effect | `Neighbors` reads a node's document and its mark in one batched `KVGetMulti` call, so a node latching mid-read can never present the just-emptied document as a complete answer. An unmarked node's read is that same two-key batched request, returning the document's KV revision as the fingerprint. A marked node's read enumerates Core KV's `lnk.*` keyspace directly for both directions, drops soft-tombstoned links, and returns a fingerprint hashed over the matched `(key, revision)` set in place of a document revision |
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
lens-projection-liveness-design.md §15). Every other lens kind is excluded structurally —
the driver simply never installs a `SweepPlan` for it: a plain lens retracts through
filter/diff retraction and the Personal Lens has its own Hydrate.

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
  still draining is exempt however long it runs; one that has not yet reported a count is
  unknown, not wedged. While a rebuild is in flight the metric carries `rebuildOutstanding`
  and `rebuildProgressAt` (dropped once it finishes, so a stale final count is never published
  as a stuck one). A poll that keeps erroring records no progress deliberately — that retry is
  unbounded, so an error that never clears must read as wedged. A
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
reaches the envelope/adapter. If nothing moved,
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
| **Per-lens projection liveness (all lenses)** | `Pipeline.Progress()` (in-process `lastAppliedSeq`/`lastProjectedAt`) → `health.Reporter.SetProjectionProgress` (5s cycle) → `LatticeHeartbeater.LensProvider` → threshold eval | `<lensId>` entry — `lastProjectedAt`/`projectionLag`; `health.refractor.<instance>` — `metrics.lensLiveness.<canonicalName>` `{status, projectionLag, lastProjectedAt, alert, unreadable}` (always emitted) + a Contract #5 §5.5 `issues[]` entry and degraded `status` when anomalous | The generalized sibling of the Capability-Lens backstop above, widened to **every** non-auth-plane (business) lens (lens-projection-liveness-design.md). `lastProjectedAt` (advances only on a real target write, so a caught-up-but-no-op consumer leaves it frozen even while `lastAppliedSeq` moves) gives an operator a real freshness clock; the same raise-after-N / clear-band debounce as the cap path auto-alerts a wedged consumer via `LensProjectionLagging`, and a paused business lens raises `LensProjectionPaused` — both `severity: warning` (⇒ `status: degraded`), **never** `error`/`unhealthy`: a single frozen business lens is a real outage for that vertical but must not fail the whole Refractor instance. A lens whose liveness inputs cannot be read is reported `status: "unknown"` / `alert: "unreadable"` with `projectionLag: null` and raises `LensProjectionUnreadable`, never dropped from the map — an absent lens is indistinguishable from one that was never installed. An actor-aggregate business lens also carries its convergence sweep's verdicts here — `LensCoverageDivergence`, `LensRepairFailing`, `LensRepairBlocked`, `LensAuditUnverified`, `LensSweepStalled` — mirroring the `Capability*` sweep codes but **always `severity: warning`**, at every streak length, for the same reason pause and lag are. Auth-plane lenses are excluded (the Capability-Lens path above stays canonical for them) — separate debounce/issue and sweep-staleness state, zero regression surface on that security-critical path. |
| Eval-drift retries/requeues | `pipeline.executeFullForActor` → `Reporter.RecordEvalDriftRetry` / `RecordEvalDriftRequeue` | `<lensId>` entry — `evalDriftRetries`/`evalDriftRequeues` | Footprint-validation counters (evaluation-consistency-design.md §4.6/§13.3): only an auth-plane actor-aggregate lens whose cypher emits a multi-binding conjunct unit pays this cost — a single-binding auth-plane lens (e.g. `capabilityRoles`) is exempt by construction and never attempts a drift-retry on its own legitimate churn. `evalDriftRetries` counts an inline re-execution the drift-detection triggered; `evalDriftRequeues` counts an evaluation whose read surface still diverged after that retry and was requeued as `failure.ErrEvalDrift` rather than landing a possibly-torn row. Both are cumulative and expected to stay near zero — a nonzero rate under sustained load is the signal that sizes Fire 2's per-row footprint scope. |
| Audit | `health.AuditWriter` | `lattice.refractor.audit.<lensId>` | Per-projection audit append. |

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

- **A meta sweep multiplies `Rebuild`** — a rebuild is a durable delete-recreate per lens, so any fan-out over
  the lens set floods the server's consumer management and starves every other lens behind it; a bound held
  per PATH leaves the sum unbounded, and a bound released mid-handover covers less than it claims (this is a
  concurrency/fairness bound — **not** memory; §17.19 retired that attribution). Minted:
  dynamic-type-taxonomy §17.8. Check, claim by claim: every path that can BURST (reload scheduler, `rebuild`
  op) runs on one shared `internal/refractor/rebuildgate.Gate` —
  `TestTaxonomyChanged_FanOutStaysWithinTheConcurrencyBound`,
  `TestRebuildGate_TaxonomyAndControlPathsShareOneBound`; the slot spans the handover *in the shipped
  rebuild* — `TestRebuild_HoldsUntilTheConsumerPumpHasReopened` (fails if `Pipeline.Rebuild` reverts to a
  non-waiting reset); the barrier itself, including overlapping waiters —
  `TestSupervisor_ResetAwaitReopen_{ReturnsOnlyAfterThePumpReopens,OverlappingWaitersAreBothReleased}`. The
  barrier is best-effort by design (an open already in flight can release a waiter early; consequence is a
  slot released early). Two things sit outside the bound BY CHOICE, not as holes: the replay DRAIN, and the
  synchronous `RebuildRule` arm — at most one exists process-wide because there is one `classkeyshredded`
  manager on one inline durable handler (*not* `rebuildSerial`, which excludes only those callers from each
  other), while its drain-wait budget is tens of minutes. Worst case is the bound plus one, and that one can
  overlap a gated rebuild of the same lens.
- **Turning on a behaviour an existing predicate gated hands it exactly the complement — and any safety
  property that rode on that predicate is absent there.** A shrink-truncate flag reached the lenses
  `ApplyTruncateScope` returns early for, i.e. the ones whose purge is unconfined, so it aimed a whole-bucket
  wipe at shared `capability-kv`. Minted: dynamic-type-taxonomy B0 (cold pass, reproduced). Check: for a new
  flag, name the population it newly affects and the invariant the old gate was silently supplying.
- **New pipeline state without a declared lifetime** (registry / latch / armed flag) — reset, carry, and
  order it at replay, reconnect, tombstone, and retry, or the review will. Minted: dynamic-type-taxonomy
  item 4 (nineteen findings, this class load-bearing). Check: the designer's state-lifetime table +
  standing checklist #1.
- **Site censuses derived from key shapes undercount** — `nodeMatches` also admits a vertex whose body
  `class`/`label` matches, so derive label/equality censuses from the matcher, not the key grammar. Minted:
  dynamic-type-taxonomy §5.1 census correction (four → six). Check: executable census, re-run at Phase 0.
- **An expansion sigil is fail-CLOSED in a positive pattern and fail-OPEN in a negated one** — constraining
  the binder inside `NOT (...)` removes exclusions, i.e. grants. A `*` label on an auth lens's exclusion walk
  turns a partial taxonomy expansion into an over-grant, and the two arms of the same lens then fail in
  opposite directions. Minted: dynamic-type-taxonomy B1 (`capabilityServiceAccess`'s `exLoc`, which mints
  `cap.svc.<actor>`; reproduced as a failing test before removal). Check: per-lens string pin today
  (`service-location/package_test.go`); a `lint-lens-anchors` "sigil inside a negated pattern" rule on the
  second sighting.
- **A label narrows the binder, not necessarily the consumer filter** — `ReferencedLabels` clears
  exhaustiveness for *any* variable-length hop (`ruleengine/full/labels.go:135-138`) and `ConsumerFilter`
  takes the broad arm before cap arithmetic, so a lens carrying a `*0..` walk is broad whatever its labels.
  Minted: dynamic-type-taxonomy §9.4, whose headline "converts an unnarrowable lens into a narrowed one"
  survived three increments before anyone measured it. Check:
  `label_derivation_corpus_census_test.go` pins `(labels, exhaustive, filterMode)` per shipped lens.
- **Lens lag is not read-model incompleteness** — anti-join / field-diff against the source before designing
  any drain or backfill. Minted: capability-projection reconciliation. Check: none yet.
- **An upsert-only reprojection retracts nothing whose key drops out** — on the security plane that is an
  over-grant. Minted: negative/retraction design pass (designer SKILL §2). Check: none yet (the retraction
  primitive is its own backlog item).
- **A `WITH` boundary drops unprojected variables** — any set derived from a query (labels, carried state)
  must model the drop or it re-seeds / excuses wrongly. Minted: lens-label-key-type binding design (two
  reviewers broke the same increment in opposite directions). Check: none yet.
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
  only, never to conclude absence — or it is deleted, which is what shipped.
