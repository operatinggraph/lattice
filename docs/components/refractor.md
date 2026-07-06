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
| `lens/` | `CoreKVSource` (durable consumer over `vtx.meta.>`, routes `meta.lens` class to loader); `Rule` type; `translateSpec` from `LensSpec` to `Rule`; engine selection via registry |
| `adapter/` | `Adapter` interface; `nats_kv` adapter; Postgres adapter; `nats_subject` adapter (Personal Lens transport); `PoolManager` for Postgres connection pooling |
| `adjacency/` | Adjacency KV read helpers |
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
| `keyshredded/` | Durable `events.privacy.keyShredded` listener that nullifies a shredded identity's projected rows (brainstorm #62 — the one sanctioned event-stream listener in Refractor's charter); records `RecordShredFinalization{projectionsNullified}` |
| `eventlens/` | Event-sourced lens projection over the `core-events` stream (Chronicler PROJECT mode). **Being extracted** to the standalone `cmd/chronicler` component (Fork C re-ratified 2026-07-06); see [chronicler.md](./chronicler.md) + backlog `chronicler-host-reconciliation` |

---

## In-contracts (what it consumes)

| Contract | Source | Notes |
|----------|--------|-------|
| **Core KV CDC events** | Durable JetStream consumer (`substrate.SubscribeKVChanges`) on the `KV_core-kv` backing stream | Both the all-mutations stream and the `vtx.meta.>` lens-def watch run on the same durable-consumer pattern; ack position persists across restarts so a restarted Refractor resumes rather than replaying from the start. |
| **Lens meta-vertices** | Core KV `vtx.meta.<NanoID>` with `class: meta.lens` and a `.spec` aspect | The `spec` aspect carries: `id`, `canonicalName`, `targetType`, `targetConfig`, `cypherRule`, `outputSchema`, `engine` (optional). `engine` must be `"full"` (or absent → full); any other value fails lens validation. |
| **Adjacency KV** | `refractor-adjacency` bucket | Refractor's internal inbound-link index, built by `consumer/bootstrap.go` from every `lnk.*` CDC event; two directional entries per edge. EdgeID == link key. The adjacency is the inbound-link lookup index for the cypher executor. |

---

## Out-contracts (what it produces)

| Artifact | Destination | Notes |
|----------|-------------|-------|
| **Capability KV** (Contract #6 §6.2) | `capability-kv` bucket | Produced by the bootstrap-seeded Capability Lens; key pattern `cap.<actorType>.<id>` (e.g. `cap.identity.<actorId>`), derived by stripping the `vtx.` prefix from the actor vertex key. Consumed by Processor's step-3 `CapabilityAuthorizer`. |
| **Per-lens target KV buckets** | Bucket name per `LensSpec.targetConfig.bucket` | e.g. `duplicate-candidates` (the identity-hygiene package's Duplicate Candidates Lens). Created on demand if not pre-provisioned. |
| **Postgres rows** | Target table per `LensSpec.targetConfig.table` | For SQL-target lenses. The adapter is **thin**: it upserts one column **per RETURN field** (`INSERT … (book_id, title) … ON CONFLICT DO UPDATE`) and issues no DDL. The target table is **provisioned out-of-band** (a migration), with columns matching the lens RETURN (key columns + projected fields). Delete projection is **mode-dependent** (`targetConfig.deleteMode`, default `hard`): the default hard delete issues `DELETE FROM` and needs only the key + projected columns; `deleteMode: soft` issues `UPDATE … SET is_deleted=true, deleted_at=NOW()` and **requires** the `is_deleted` / `deleted_at` columns. The Refractor does not create or alter the table. |
| **Health KV signals** (Contract #5) | Health KV `health.refractor.<instance>.lens.<canonicalName>` | Per-lens latency snapshots (p95, p99, mean, count from `LatencyRingBuffer`); consumer lag; per-instance heartbeat every 10s. |
| **Audit subjects** | `lattice.refractor.audit.<lensId>` | One `AuditEntry` per projection. |
| **Metrics subjects** | `lattice.refractor.metrics.<lensId>` | Consumer lag on `LagPoller` interval. |
| **Control plane** | `micro.Service` endpoints at `lattice.ctrl.refractor.<lensId>.<op>` | Handles JSON control requests (`validate`, `rebuild`, `pause`, `resume`, `delete`, `register`, `deregister`, health) via the NATS Services framework; capability-gated (FR30). |
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
- **Delta envelope** (`{op, key, anchor, kind, class, revision, projectionSeq, encrypted, data}`):
  `op` is `"upsert"` or `"delete"`; `anchor`/`kind`/`class` are optional envelope metadata a lens's
  RETURN clause supplies as reserved row-column names (promoted out of `data`, so they never appear
  twice); `data` is the remaining projected row (nil/omitted for a delete or an all-metadata row).
  `encrypted` is always `false` through PL.2 — Vault ciphertext passthrough is Fire 5.
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
- **Deferred to later PL fires** (`personal-secure-lens-design.md` §7): the Hydration Hook (PL.4)
  and Vault-ciphertext + transient-key composition (PL.5, 🚧 gated on Vault Phase A).

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

> **Tombstone column (staged §6.14 clarification).** The grant table carries an `is_deleted`
> boolean the contract's illustrative four-column schema omits. It is **required**: §6.14
> mandates that a delete "applies only when its incoming `projectionSeq` exceeds the stored
> one" and that a stale replay "cannot resurrect a revoked grant" — both need the revoked
> row's seq **retained**, which a hard `DELETE` discards. Revocation is therefore a
> seq-guarded **soft tombstone**; the RLS policy and the membership lookup filter
> `NOT is_deleted`. This reuses the existing Postgres soft-delete convention.

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
  JSONB). A protected lens **may not** use `deleteMode: soft` — the RLS table has no `is_deleted`
  column and the §6.14 policy does not filter it, so soft delete is rejected at spec load.
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

**Type-relevance skip (the amplification bound).** The re-execute runs only when the
event's owner/endpoint vertex **type** is in the lens's referenced-label set
(`full.CompiledRule.ReferencedLabels` — every node label its MATCH patterns, pattern
expressions, and comprehensions can bind): a `meta` aspect mutation cannot change a
`MATCH (u:unit)` lens's rows, so the lens acks it without scanning. A query whose label
set is not exhaustive (an unlabeled node pattern, or a variable-length relationship
whose intermediate hops bind arbitrary types) disables the skip and reprojects on
every event — conservative, never a missed refresh.

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
Convergence (`violating`-flag) lenses are unaffected: they anchor-match every
candidate unconditionally, so the presence check never fires for them — an authoring
invariant (a future convergence lens with a filtering `WHERE` would retract rows
Weaver misreads as deletions; keep convergence predicates in the `violating` flag).

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
2. **`CoreKVSource`** consumes `vtx.meta.>` via the durable consumer; routes entries with class `meta.lens` to the spec parser. CDC ordering is not guaranteed — if the `.spec` aspect arrives before its parent vertex, it is buffered in `pendingSpecs` until the parent vertex's class is observed
3. **`translateSpec`** converts `LensSpec` → `Rule`; engine selection via `Registry.SelectForLens`; `CompiledRule` populated
4. **`startPipeline`** (in `cmd/refractor/main.go`) constructs the adapter (opens the target KV bucket / Postgres table), wires a `pipeline.Pipeline`, installs a `LatencyRingBuffer`, launches a `health.Reporter`
5. **The pipeline's `substrate.ConsumerSupervisor`** (built in `pipeline.Pipeline.RunOn`, configured from `cmd/refractor/main.go`) creates a durable JetStream consumer (durable name `refractor-<ruleID>`) on the `KV_core-kv` backing stream filtered to the lens's source-key prefix
6. **Each CDC event** → `pipeline.Pipeline.HandleMessage` → engine evaluates → projection row(s) emitted → `EnvelopeFn` wraps row → adapter writes to target
7. **Latency** tracked in `pipeline.LatencyRingBuffer` (128-sample ring buffer, thread-safe). Per-mutation health signals via `LatticeHeartbeater.LensLatencyProvider`
8. **Lens spec update** → `CoreKVSource.updateCB` fires; `ClassifyUpdate` determines the reload: an `INTO`-only change hot-reloads the adapter in place (`IntoOnly`), while a `MATCH` (query) change requires a full pipeline rebuild (`MatchChange`)
9. **Lens tombstone** (parent vertex deleted or `.spec` deleted) → `CoreKVSource` purges its spec-tracking maps and logs the removal; the running pipeline's durable consumer is **not** torn down by the tombstone itself (an operator `delete` control op, or a process restart, removes it). Adjacency entries are left in place (tombstone re-projection is a Phase 3 carry)

---

## Refractor adjacency KV

The `refractor-adjacency` bucket is Refractor's internal secondary index for
graph traversal. It is built and maintained exclusively by Refractor; no other
component writes to it.

| Property | Value |
|----------|-------|
| Bucket name | `refractor-adjacency` |
| Builder | `consumer/bootstrap.go` (`Bootstrapper.Run`) — processes every `lnk.*` CDC event |
| Entry shape | Two directional entries per link: `adj.<type1>.<id1>.<linkName>` → list of `<type2>.<id2>` EdgeIDs |
| EdgeID | == link key; consistent across adjacency + Core KV |
| Purpose | Inbound-link lookup index for the cypher executor (graph traversal without a global scan) |

Within Refractor the adjacency is consumed directly by the cypher executor for
inbound-link enumeration without a global `lnk.*` scan.

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

---

## Rebuild & truncate semantics

`Pipeline.Rebuild(ctx, truncate)` resets a lens's durable consumer so the lens
re-projects from the start of its source stream. The optional truncate step
clears the target store first.

| Adapter / mode | `truncate` requested | Behavior |
|----------------|----------------------|----------|
| NATS-KV, unguarded | `false` | No truncate; the stream replay overwrites each key last-writer-wins. |
| NATS-KV, unguarded | `true` | `Truncate` purges every key in the bucket, then the stream replays into the empty bucket. (`Truncate` does what the flag promised — it is not a silent skip.) |
| NATS-KV, **guarded** | `false` or `true` | **Truncate is forced.** A guarded bucket's monotonic `projectionSeq` watermarks would reject the historical lower-seq replays against the live high-seq watermarks, leaving rejected-write holes. The pipeline detects guardedness via `Guarded()` (it never learns lens canonical names), purges the bucket — clearing the watermarks with the data — and logs at info that truncate was forced. The stream then replays from empty, the highest-seq write wins, and the steady state is identical to a from-scratch projection (Contract #6 §6.2). |
| Postgres (no `Truncater`) | `true` | Truncate is skipped with a warn (the adapter does not implement `Truncater`). |

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
| **Capability-Lens liveness alert** | `LatticeHeartbeater.CapabilityLensProvider` → threshold eval | `health.refractor.<instance>` — `metrics.capabilityLens.<canonicalName>` `{status, consumerLag, alert}` (always emitted) + a Contract #5 §5.5 `issues[]` entry and degraded/unhealthy `status` when anomalous | A **paused** capability lens raises `CapabilityLensPaused` (`severity: error` ⇒ `status: unhealthy`): the authz read-model is frozen. An **active** lens with `consumerLag` over the threshold (default 100, deployment-overridable) raises `CapabilityLensLagging` (`severity: warning` ⇒ `status: degraded`) — **debounced**: it raises only after the lens stays over threshold for N consecutive heartbeats (default 3 ≈ 30s sustained) and clears once lag falls to/below a lower clear-threshold band, so a one-cycle spike does not flap. `rebuilding` and within-threshold are `ok`. The issue's `since` persists across heartbeats and the issue is dropped when it resolves. Read-only — it observes the lens reporter + supervised consumer; no authz path, Core KV, or projection is touched. |
| **Per-lens projection liveness (all lenses)** | `Pipeline.Progress()` (in-process `lastAppliedSeq`/`lastProjectedAt`) → `health.Reporter.SetProjectionProgress` (5s cycle) → `LatticeHeartbeater.LensProvider` → threshold eval | `<lensId>` entry — `lastProjectedAt`/`projectionLag`; `health.refractor.<instance>` — `metrics.lensLiveness.<canonicalName>` `{status, projectionLag, lastProjectedAt, alert}` (always emitted) + a Contract #5 §5.5 `issues[]` entry and degraded `status` when anomalous | The generalized sibling of the Capability-Lens backstop above, widened to **every** non-auth-plane (business) lens (lens-projection-liveness-design.md). `lastProjectedAt` (advances only on a real target write, so a caught-up-but-no-op consumer leaves it frozen even while `lastAppliedSeq` moves) gives an operator a real freshness clock; the same raise-after-N / clear-band debounce as the cap path auto-alerts a wedged consumer via `LensProjectionLagging`, and a paused business lens raises `LensProjectionPaused` — both `severity: warning` (⇒ `status: degraded`), **never** `error`/`unhealthy`: a single frozen business lens is a real outage for that vertical but must not fail the whole Refractor instance. Auth-plane lenses are excluded (the Capability-Lens path above stays canonical for them) — separate debounce/issue state, zero regression surface on that security-critical path. |
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
| Personal Lens / Secure Lens | Fires 1–3 (PL.1 transport, PL.2 fan-out + Interest Set, PL.3 D1 security gate) shipped; PL.4–PL.5 pending | Per-identity security-filtered projection. PL.1's `nats_subject` target adapter + PL.2's `ActorEnumerator` fan-out/Interest Set + PL.3's `capabilityread`-backed D1 gate (above) ship dark, still behind the trusted-single-identity carve-out for the NATS `SUB` boundary itself (Fork 3, subscribe-ACL); Hydration (PL.4) and Vault ciphertext (PL.5) remain |
| Multi-cell lens routing | Phase 3 | Current pipeline is single-cell |
| Cross-instance latency aggregation | Phase 3 | Current `LatencyRingBuffer` is per-instance; no cluster-level rollup |
| Link-envelope tombstone re-projection | Phase 3 | Currently adjacency entries are left in place on tombstone; re-projection on tombstone is not triggered |
| Substrate-level write restriction on lens target buckets | 🔭 Designed (ratified 2026-06-27) | Today the defense against fabricated lens-target writes is overwrite-by-reprojection only; the **NATS account write-restriction** design scopes per-component NKey publish permissions so only Refractor writes the lens/auth buckets (credential seam shipped; enforcement pending) |
