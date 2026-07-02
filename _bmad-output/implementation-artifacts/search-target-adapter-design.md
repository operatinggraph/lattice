# Search / Elasticsearch lens-target adapter — design

**Status: ✅ Andrew-ratified (2026-07-02) — SHELF design (build with the first real search consumer).**
Decisions: **vendor pin = OpenSearch** (Apache-2.0 server + client; vendors.md row staged at build/pin
time) · **Postgres-FTS blessed as the interim default** for the first small consumer (tsvector/pg_trgm on
the existing protected read models — RLS-compatible, zero new infra), OpenSearch reserved for genuine
global-scale faceted search · the public-only fail-closed guard stands (a search target rejects unless
explicitly `public: true`; protected search = a separate future D1-for-search design). Andrew will have
the Vertical PO file the first search demand.
· Designer: Winston (architect) · 2026-07-01
Backlog row: `planning-artifacts/backlog/lattice.md` → *Read-model / projection maturity* →
"Elasticsearch target adapter".

---

## For Andrew (the one-look)

**What it does (two lines).** Refractor's target-adapter SPI ships two stores — `nats_kv` and
`postgres`. The architecture (`lattice-architecture.md:78`) and the vault reserve a **third: a search
engine** for "global search and reporting" (the Personal-Lens *Global Search index*). This design is the
**third `Adapter` implementation** — a `SearchAdapter` that upserts/deletes one search document per
projected row, plugged into the existing `buildAdapter` switch, with the monotonic write guard mapped
**natively** onto the search engine's external-versioning (no CAS loop, no `WHERE seq >` clause).

**Two decisions for you — one true fork, one interim blessing.**

1. **FORK (vendor + license) — your call.** Which engine backs the third adapter?
   - **A — OpenSearch (RECOMMENDED).** Apache-2.0 server + Apache-2.0 `opensearch-go` client. ES-API
     compatible (forked ES 7.10). No license entanglement for a platform we may one day offer as a
     service.
   - **B — Elasticsearch.** Richer ecosystem, but the **server** is Elastic License 2.0 / SSPL since
     7.11 — a managed-service resale restriction. The `go-elasticsearch` client is Apache-2.0; the
     *server* license is the risk.
   - The adapter code is ~identical either way (same REST API, same external-versioning). The choice is
     a **vendor pin + license posture**, not an architecture fork. My recommendation: **A (OpenSearch)**;
     it costs us nothing on capability and removes the license question. Say the word and I record the pin
     in `docs/vendors.md` (staged for you, not committed).

2. **Interim blessing (not a fork).** The first real search consumer may be *small* (e.g. "search
   LoftSpace listings by text," "find a patient by name"). For that, **Postgres full-text search**
   (`tsvector` + GIN, `pg_trgm`) **reuses the shipped `PostgresAdapter` + pool + D1/RLS with zero new
   infra** — no OpenSearch cluster in docker-compose/CI. It is *not* a substitute for global-scale
   faceted relevance search, but it is the right first step for a single small vertical. Do you want the
   Postgres-FTS interim blessed as the default first move, with OpenSearch reserved for genuine
   global-search scale? (I recommend **yes** — it defers a heavyweight dependency until scale demands it.)

**No frozen-contract change.** The lens **target-type enum** (`nats_kv` | `postgres`) is validated in
**code** (`internal/refractor/lens/schema.go:212`, `corekv_source.go:440`), **not** frozen in any
`docs/contracts/*` section. Contract #6's `projectionSeq` guard (§6.2) is scoped to the **Capability-KV
auth plane**; a *business* search lens is not capability-plane, so mapping the guard onto external
versioning touches **no contract**. I read Contracts #1/#2/#6/#10 — none enumerates the adapter set or
prescribes a store's write mechanics. Nothing to edit. (§7 shows the search.)

**Build is deferred — this is a shelf design.** There is **no search consumer today** (no vertical PO
has filed a search feature; no brainstorm idea; no ES/OpenSearch dependency in `go.mod`). Per the
dead-scaffolding discipline, the adapter is inert until a lens uses it, so the **build waits for a real
search consumer**; ratify the *design* and shelf it. When a vertical files "search X," Fires 1–3 build
**with** that consumer (§9). The output you're ratifying is "the shape is settled and sequenced," not
"start building."

**One fail-closed guard I'm baking in (not a fork, but flag-worthy).** A search engine has **no
Postgres-RLS equivalent** — it cannot enforce D1 per-row read authorization at the store. So a
PII/protected search index would be an unauthorizable data sink. The design makes an **ES/OpenSearch
target reject at lens validation unless it is explicitly `public: true`** (non-PII, global-discovery
fields only) — omission **denies**, mirroring the write-path "no entry = no access" §6.8 and my own D1
fix (protected-by-default). Per-actor *protected* search is a **separate future design** (D1-for-search:
query-time actor-filter injection at a Gateway, or the Personal-Lens per-actor stream), explicitly out
of scope here.

---

## 1. Problem + intent

Lattice's read side is the **Refractor**: lenses (`vtx.meta.<NanoID>` with `class: meta.lens`) project
Core-KV state into **per-lens target stores** that applications query (P5 — apps read lens projections,
never Core KV). Two target stores ship today, both implementing the `adapter.Adapter` SPI:

- **`nats_kv`** (`internal/refractor/adapter/natskv.go`) — a KV bucket per lens; the default read-model
  store.
- **`postgres`** (`adapter/postgres.go`) — a SQL table per lens; relational queries + the D1 RLS
  read-path.

The architecture reserves a **third target store — a search engine** — and names it repeatedly:

- `_bmad-output/planning-artifacts/lattice-architecture.md:78` — *"Lens target stores | Refractor |
  Application queries | Postgres, **Elasticsearch**, per-user NATS streams — external to NATS KV."*
- Obsidian vault `Lattice System Spec.md:145` — *"project data into optimized stores (Postgres,
  Elasticsearch, etc.)"*
- `Lens and Refractor/The Refractor.md:42` — *"Static Projections: … Postgres or **Elasticsearch for
  global search and reporting**."*
- `Edge Lattice/Personal Lens.md:9` — *"a **'Global Search' Elasticsearch index**"* as a shared master
  projection.

The **intent** is a store optimized for what neither KV nor a relational table does well: **full-text
search, relevance ranking, faceting, and reporting aggregations over a global, multi-tenant projection**
(discover a listing by free text, find an identity by fuzzy name, a reporting dashboard). Core KV is
optimized for structure + consistency and is "inefficient for complex querying" (System Spec) — the
Refractor exists precisely to project *out* of it into query-optimized stores; search is the missing one.

**Why now / why a design (not a build).** The adapter is a **vision-anchored end-state**, but it has
**no live consumer**. The Designer's job is to keep a ratify-ready shape on the shelf so that when a
vertical PO files search demand, the Steward builds the adapter *with* the consumer instead of
cold-designing it. This doc settles the shape, the vendor fork, the guard mapping, and the fail-closed
auth posture — and sequences the build behind the real consumer.

## 2. The established pattern this MIRRORS

The whole point is to **extend the existing SPI, not invent parallel machinery**. The `Adapter` interface
(`adapter/adapter.go`) is deliberately small:

```go
type Adapter interface {
    Upsert(ctx, keys map[string]any, row map[string]any, projectionSeq uint64) error
    Delete(ctx, keys map[string]any, projectionSeq uint64) error
    Probe(ctx) error   // liveness → failure.Classify → pause/resume
    Close() error
}
type Truncater interface{ Truncate(ctx) error }  // optional: rebuild-truncate (FR29)
```

Everything downstream already routes through it: `pipeline.Pipeline.HandleMessage` → engine evaluates →
`EvalResult{Keys, Row, ProjectionSeq}` → `EnvelopeFn` → `adapter.Upsert/Delete`. A **new store is one new
`Adapter` implementation + one `case` in the `buildAdapter` switch** (`cmd/refractor/main.go:210`) + the
config/schema plumbing for its target fields. No pipeline, engine, consumer, or contract change.

The two shipped adapters set the precedents the `SearchAdapter` mirrors point-for-point:

| Concern | `nats_kv` | `postgres` | **`search` (this design)** |
|---|---|---|---|
| Doc/row identity | `buildKey` joins `keyOrder` with `.` | `ON CONFLICT (keyOrder)` | `_id` = `keyOrder` joined with `.` (mirror `buildKey`) |
| Upsert | `kv.Put(key, json(row))` | `INSERT … ON CONFLICT DO UPDATE` | `PUT /<index>/_doc/<_id>` body = row |
| Delete (hard) | `kv.Delete(key)`; absent = no-op | `DELETE FROM … WHERE key` | `DELETE /<index>/_doc/<_id>`; 404 = no-op |
| **Write guard** | CAS loop on stored `projectionSeq` | `WHERE EXCLUDED.seq > table.seq` | **external versioning** (`version=projectionSeq`, `version_type=external`) — engine rejects lower-seq with **409**, swallowed as no-op |
| Probe | `kv.Status` | `pool.Ping` | cluster health + `HEAD /<index>` exists |
| Truncate (FR29) | purge every key | `TRUNCATE TABLE` | delete-by-query `match_all` (or recreate index) |
| Provisioning | bucket created on demand | table out-of-band (no DDL) | **index + mapping out-of-band** (no DDL); Probe verify-and-pause |
| Connection sharing | one KV handle | `PoolManager` per DSN | `ClientManager` per endpoint (mirror `PoolManager`) |

## 3. The shape

### 3.1 Data model — one search document per projected row

A lens's `EvalResult` yields `Keys` (composite key fields) + `Row` (projected non-key fields). The
adapter maps this to one search document:

- **`_id`** = the `keyOrder` values joined with `.`, byte-identical to `NatsKVAdapter.buildKey`
  (Contract #1 uses `.` as the segment separator throughout). Deterministic → idempotent upsert.
- **document `_source`** = the `Row` map, JSON as-is. Field types come from the **index mapping**
  (provisioned out-of-band), so a `text` field is analyzed for full-text search, a `keyword` field for
  exact-match/faceting — the lens author declares the mapping, the adapter is thin (mirrors "Postgres
  adapter issues no DDL").
- **index name** = `targetConfig.index` (mirror `bucket` / `table`).

No new vertex/aspect/link kinds — a search lens is an ordinary `meta.lens` vertex whose `targetType` is
`"opensearch"`. P1/P2 untouched: Core KV is unchanged; the Processor is still the sole Core-KV writer;
the search index is a *downstream derived view*, exactly like the Postgres table.

### 3.2 Read path (P5) — apps query the index, not Core KV

A search-backed app queries the **index** via the engine's search API (`GET /<index>/_search`) — the same
posture as a Postgres-backed app running `SELECT`. The index **is** the lens projection; reading it
honors P5. Loupe-the-inspector is unaffected (it reads Core KV directly, its sanctioned exception).

**Eventual consistency is fine and matches lens semantics.** Search visibility lags by the engine's
refresh interval (default 1s). Lenses are already eventually consistent (freshness windows, convergence
polling — Contract #2 §157 "client polls the Lens for query convergence"). The adapter writes with
`refresh=false` (async refresh) for throughput; a consumer that needs read-your-write can request
`refresh=wait_for` per-query. No new consistency contract.

### 3.3 Write path (P2) — unchanged; the guard maps natively

The write path is untouched: Processor → Core KV → CDC → Refractor pipeline → `adapter.Upsert/Delete`.
The **monotonic projection-write guard** (the reason a lower-seq replay must not clobber a fresher row)
maps onto the search engine's **external versioning**, confirmed against the upstream reference
(Elastic `docs-index_.html`; OpenSearch shares the API as an ES 7.10 fork):

> `version_type=external` — *"Only index the document if the specified version is **strictly higher**
> than the version of the stored document or if there is no existing document."* A lower-or-equal version
> is rejected with a **409 version conflict**.

So a guarded `SearchAdapter.Upsert` issues `PUT /<index>/_doc/<_id>?version=<projectionSeq>&version_type=external`,
and the engine itself rejects a stale replay — **swallowed as an idempotent no-op** (the search analogue
of natskv's `storedSeq >= incoming → return nil`). This is **strictly better than both shipped guards**:
no read-before-write CAS loop (natskv's `guardCASMaxAttempts`), no `WHERE seq >` SQL clause (postgres) —
the ordering token rides the same write, one round-trip, enforced server-side.

**Guard posture.** Because external versioning is free (no extra round-trip), a search adapter runs it
**always-on** rather than opt-in — every search write carries `projectionSeq` as its external version.
This is a small, honest improvement over the shipped adapters (where the guard is opt-in because it costs
a CAS loop / an extra column); here it costs nothing, so there is no reason to leave replay-ordering
unprotected. A `Delete` with external versioning likewise carries the seq, so a delete cannot be undone
by a stale re-index. (A hard delete removes the version vector; the tombstone-vs-hard-delete nuance is
§8, risk R4.)

### 3.4 Guarded/unguarded and DeleteMode

- **DeleteMode.** `hard` (default) = `DELETE /_doc/<_id>` (404 swallowed, idempotent — NFR2). `soft` =
  index a tombstone doc `{isDeleted:true, projectedAt, projectionSeq}` (mirror natskv soft delete) for
  forensic search targets. Same `ParseDeleteMode` seam as the other adapters.
- **No `GrantWriter` / `ProtectedAdapter` analogue in v1.** Those are the D1 read-path machinery, which
  is Postgres-RLS-specific. A search target is `public`-only in v1 (§8 R1); protected search is a future
  design.

### 3.5 Connection management — `ClientManager` (mirror `PoolManager`)

`adapter.PoolManager` hands out one shared `pgxpool.Pool` per DSN so connection count stays bounded
across lenses targeting the same DB (ADR-9). The `SearchAdapter` mirrors this with a **`ClientManager`**
handing out one shared HTTP client per endpoint. Credentials (API key / basic auth) come from the
**environment**, never YAML (mirror the DSN-from-config, secret-from-env posture) — the lens's
`targetConfig.endpoint` names the cluster; the credential is resolved from env at wiring time.

### 3.6 Probe + provisioning (verify-and-pause)

Mirror the Postgres protected-lens posture: the **index + mapping are provisioned out-of-band** (an
operator migration / the `emit-ddl` control op could later emit the mapping JSON, but v1 issues no DDL —
exactly like the Postgres table). The adapter's `Probe`:

1. cluster reachable (`GET /`), and
2. the target index exists (`HEAD /<index>`) with the expected mapping posture.

On failure `Probe` returns an error the pipeline classifies (§3.7) → the lens **starts infra-paused**
and only projects once the index is present and correct. Refractor never projects into a nonexistent or
malformed index (mirror "Refractor projects nothing into a table that is not locked down").

### 3.7 Failure classification (mirror the four tiers)

`failure.Classify` routes adapter errors into `CatTransient` (Nak/redeliver) · `CatTerminal` (DLQ) ·
`CatInfra` (pause) · `CatStructural` (pause). The `SearchAdapter` maps HTTP outcomes:

| Engine response | Category | Rationale |
|---|---|---|
| connection refused / 503 / cluster red | `CatInfra` (`failure.Infrastructure`) | store temporarily down → pause + Probe-retry |
| 404 index missing | `CatStructural` | permanent misconfig → pause until provisioned |
| **409 version conflict** | **swallow → nil** | stale-replay no-op (the guard firing — not an error) |
| 400 mapper_parsing_exception | `CatTerminal` (`failure.Terminal`) | bad data for the mapping → DLQ, never retriable |
| 429 too_many_requests / timeout | `CatTransient` (default) | backpressure → Nak/redeliver |

## 4. Config + schema surface (code, not contract)

Additions, all in code (no `docs/contracts/*` change — §7):

1. **`lens/schema.go` `IntoConfig`** — new fields `Endpoint string`, `Index string` (search-only), and
   validation: `case "opensearch"` requires `endpoint` + `index`; **rejects the lens unless
   `public:true`** (§8 R1 fail-closed).
2. **`lens/schema.go` target enum** (`schema.go:212`) + **`corekv_source.go` translate**
   (`corekv_source.go:440`, error string at `:532`) + `readpath_translate` — extend
   `nats_kv|postgres` → `nats_kv|postgres|opensearch`.
3. **`cmd/refractor/main.go` `buildAdapter`** (`:210`) — new `case "opensearch"` acquiring a client from
   the `ClientManager` and building a `SearchAdapter`.
4. **`emit_ddl.go`** — the read-path DDL emitter gates on `TargetType != "postgres"` (`:74`) and so
   **already skips** search targets; no change needed (search issues no SQL DDL). A future *mapping*
   emitter is out of scope.

## 5. Orchestration

**None.** A search lens is an ordinary Refractor lens driven by the existing CDC pipeline — no Loom
pattern, no Weaver convergence lens, no `@at`/`@every`, no `directOp`. The only "orchestration" is the
existing pipeline's infra-pause/resume loop, which the new `Probe` plugs into unchanged.

## 6. Reconciliation with the existing mental model

- **"Didn't we already handle this?"** No — only two of the three reserved target stores ship. Search is
  the named-but-unbuilt third (`lattice-architecture.md:78`). This is the gap, not a duplicate.
- **"Does this duplicate/contradict an established pattern?"** It **mirrors** the adapter SPI exactly (§2)
  — one more `Adapter` implementation behind the same `buildAdapter` switch. It contradicts nothing; it
  adds no parallel machinery. The guard maps onto the *same* `projectionSeq` concept the other two use.
- **"Does this introduce new state — do we keep it somewhere already?"** The only new state is the search
  index itself — a **derived read model**, the same category as the Postgres table and the KV bucket
  (P1: operational/derived state lives outside Core KV; Core KV is untouched). No new Core-KV vertices,
  aspects, or links.
- **"Is the guard a new invention?"** No — it's the **existing** `projectionSeq` monotonic guard, mapped
  onto a native engine feature instead of a hand-rolled CAS loop. Less machinery, not more.

## 7. Contract surface — the search (no change)

I read the frozen contracts for anything that (a) enumerates the adapter/target set or (b) prescribes a
target store's write mechanics:

- **Contract #1** (addressing) — key shapes; `_id` uses the `.` separator per §1.1. No adapter list.
- **Contract #2** (operation envelope) — §157 mentions "lens-target store write / client polls the Lens
  for convergence" generically; enumerates no target types. `kv.Links` §217 is unrelated.
- **Contract #6** (capability KV) — the `projectionSeq` guard (§6.2, §6.14) is **scoped to the
  Capability-KV auth plane** (an auth-plane lens target). A *business* search lens is not capability-plane
  and does not touch §6. Mapping the guard onto external versioning is an *implementation* choice for a
  business lens, not a §6.2 amendment.
- **Contract #10** (orchestration) — §98/§109 reference the `nats_kv` adapter for close-task tombstones;
  no general adapter enumeration.

The target-type set lives in **code** (`lens/schema.go`, `corekv_source.go`) and the **architecture doc**
(a planning artifact, freely editable by the planning lead — not a frozen contract). **Nothing under
`docs/contracts/*` needs to change.** The only *staged-for-Andrew* edit is the **`docs/vendors.md` row**
once the vendor fork is decided (§ For Andrew #1) — and that's a doc, not a frozen contract; I leave it
unstaged until you pick A or B.

## 8. Risks + alternatives

**R1 — No RLS ⇒ a PII search index is an unauthorizable sink (the sharp edge).** A search engine has no
per-row `FORCE ROW LEVEL SECURITY`. If a lens projected PII to a shared index, any query would see it.
**Mitigation (baked in, fail-closed):** a search target **rejects at lens validation unless
`public:true`** — search lenses carry non-PII, global-discovery fields only (names/titles for
discovery), matching the vault's "Global Search index." Omission **denies**. Per-actor *protected* search
is a separate future design (query-time actor-filter injection at a Gateway, or the Personal-Lens
per-actor `lattice.sync.user.<id>` stream). This mirrors the write-path "no entry = no access" (§6.8) and
the D1 protected-by-default fix.

**R2 — New heavyweight infra (cluster in docker-compose + CI).** A real OpenSearch node in CI is a cost
(memory, startup, a `wait-for-ready`). **Mitigation:** Fire 1 unit-tests the adapter against a fake /
`dockertest` node with no standing CI service; the standing CI service lands in **Fire 2 with the real
consumer** — never paid before a lens uses it (dead-scaffolding discipline). The Postgres-FTS interim (§
For Andrew #2) sidesteps the cluster entirely for a first small consumer.

**R3 — Vendor license (Elastic License 2.0 / SSPL).** Covered by the fork; recommendation is **OpenSearch
(Apache-2.0)** to remove the question. Confirmed the API surface (external versioning, bulk, delete-by-
query) is shared — the adapter code is vendor-neutral against the REST API; only the client library +
`docker-compose` image differ.

**R4 — Hard delete drops the external-version vector.** A hard `DELETE` removes the document *and* its
version, so a *later* stale re-index (lower seq) of the same `_id` would be **accepted** (no stored
version to lose to). This is the search twin of the natskv note that "a guarded delete is always a soft
tombstone so the high-water mark survives physical absence." **Mitigation:** a **guarded** search adapter
(always-on, §3.3) uses a **soft tombstone** for deletes regardless of `deleteMode` — index
`{isDeleted:true, projectionSeq}` with external versioning — so the watermark survives. Hard physical
delete is available only on an *explicitly unguarded* search lens (rare; a non-ordered discovery index).
This is a deliberate, precedent-matching choice, not an oversight.

**R5 — Mapping drift.** The index mapping is provisioned out-of-band; a lens `RETURN` that adds a field
the mapping lacks either dynamic-maps (default) or errors. **Mitigation:** Probe verifies mapping posture
(mirror the Postgres protected-table verify); a mapping migration is an operator action, exactly like an
`ALTER TABLE`. Dynamic mapping is acceptable for a discovery index.

**Alternatives considered.**

- **A1 — Reuse Postgres full-text search (`tsvector`/GIN, `pg_trgm`) instead of a search engine.** This is
  the *simplest extension of what exists* — no new adapter at all, just a Postgres lens with a `tsvector`
  column + a GIN index (provisioned out-of-band). It inherits D1/RLS for free. **Verdict: the right first
  move for a small vertical, blessed as the interim (§ For Andrew #2) — but not the end-state.** The
  architecture explicitly names a *search engine* for *global* search + reporting; at global multi-tenant
  scale Postgres FTS does not match relevance tuning, faceting/aggregations, or horizontal search scale.
  So: Postgres-FTS interim → OpenSearch at scale. I re-asked "could a variant of A1 *beat* the adapter?"
  — for a single small consumer, yes (and it's blessed); for the reserved global-search end-state, no.
- **A2 — A per-user NATS-stream search (Personal Lens).** That's a *different* reserved target (per-user
  streams), for per-actor filtered views, not global search. Complementary, not a substitute — and it's
  its own ratified design. Out of scope.
- **A3 — Build the adapter now (inert), consumer later.** Rejected by the **dead-scaffolding test**: the
  adapter realizes no value until a lens uses it, and its correctness (mapping, guard, auth posture) is
  best proven *against a real consumer's* index. Ratify the design, sequence the build behind the
  consumer.

## 9. Fire-by-fire decomposition (build gated behind a real search consumer)

**Gate:** Fires 1–3 build **when the first search consumer is filed** (a vertical PO's "search X" +
its public global-discovery lens). Until then the design sits ratified on the shelf. The consumer's
lens is the co-shipped proof that each fire is exercised end-to-end (no dead scaffolding).

- **Fire 1 — the adapter + wiring, unit-tested.** `adapter.SearchAdapter` (Upsert/Delete/Probe/Close,
  always-on external-version guard, DeleteMode) + `ClientManager` + `buildAdapter` `case "opensearch"` +
  `schema.go`/`corekv_source.go` target-enum + the `public:true`-required validation (R1). Tests against
  a fake / `dockertest` node — **no standing CI service yet**. Ships green with the target enum extended.
- **Fire 2 — CI/compose + e2e with the consumer.** OpenSearch service in `docker-compose.yml` + a CI job
  (or a build-tagged e2e like the Postgres RLS tests, gated on an env var so the default `unit` job stays
  fast). The consumer's public search lens projects end-to-end through the real Refractor into a real
  index; an ephemeral-stack e2e asserts a document is searchable + a lower-seq replay is 409-rejected.
- **Fire 3 — Truncater + bulk + mapping-emit (optional hardening).** `Truncate` (delete-by-query /
  recreate) for rebuild (FR29); batch the pipeline's per-row writes into a Bulk request under load; an
  optional `emit-ddl`-style mapping emitter so the index mapping is package-declared rather than a hand
  migration.
- **Fire 4 — Protected/per-actor search (SEPARATE FUTURE DESIGN, not this doc).** D1-for-search: enforce
  read authorization for a filtered per-actor search view. A real fork (Gateway query-time actor-filter
  vs Personal-Lens stream) — designed only when a protected-search consumer exists. Named here so the
  boundary is explicit; **not** in this design's scope.

## 10. Test strategy

- **Unit (Fire 1):** `_id` construction (mirror `buildKey` byte-for-byte), Upsert/Delete request shape,
  external-version param on writes, 409→swallow, 404→no-op delete, failure-classification mapping,
  `public:true`-required validation. Fake HTTP transport or `dockertest`.
- **e2e (Fire 2):** ephemeral stack + real OpenSearch — project a public discovery lens, assert
  searchable, assert a replayed lower-seq CDC message does not clobber a fresher document (409 guard),
  assert infra-pause when the index is absent then resume when provisioned (verify-and-pause).
- **Gates:** the standard `go build ./... · make vet · golangci-lint · make verify-kernel · Gate 2/Gate 3
  · go test` — plus the search e2e behind its env-gated job so the default `unit` job stays fast (mirror
  the Postgres-RLS `POSTGRES_TEST_DSN` gating).

## 11. Adversarial review (self-conducted, folded in)

Per the L-feature review discipline, a focused adversarial pass on the sharp edges (findings already
folded above):

- **"Overwrite-by-reprojection retracts a stale doc" — verified false-for-key-drop, safe here.** A
  search doc is a **single-row overwrite** keyed by `_id` (the row's fields change → the upsert overwrites
  it; a Delete removes it) — *not* a composite-row-set shrink. So the retraction-transport trap (an
  upsert-only reprojection that never retracts a dropped composite key → over-grant) **does not apply**:
  each projected row is its own `_id`, retracted by the anchor-tombstone Delete the pipeline already
  emits. (Confirmed against the negative/filter-retraction design — the plain-lens linger is a *general*
  Refractor pipeline gap, orthogonal to the adapter, and inherited identically by all three stores.)
- **Guard on an unguarded store = a real reorder window — closed by always-on.** The postgres/plain LWW
  target has a documented reorder window (the guard is opt-in there because it costs a CAS/column). On
  search the guard costs **nothing** (rides the write as the external version), so I made it **always-on**
  — no unguarded-search reorder window exists by construction. (This is the "name the write guard
  precisely per adapter" reflex: search = external-version-guarded, always.)
- **Fail-closed default check.** A forgotten `public` marker does **not** silently expose PII: the lens is
  **rejected** at validation. Omission denies (R1). Verified against my own D1 default-open mistake.
- **Vendor claim grounded at the pin-to-be.** External-versioning semantics cited from the upstream
  reference (Elastic `docs-index_.html`; OpenSearch shares the ES-7.10 API), not training-prior. The exact
  client pin + any version-gated behavior get re-confirmed against the *chosen* vendor's docs at Fire 1
  and recorded in `docs/vendors.md` — the pin is deliberately deferred to the fork decision, not asserted
  here.
- **No parallel in-flight design collides.** Grepped the other `📐 awaiting-Andrew` / `🏗️` designs: none
  touches the `Adapter` SPI or `buildAdapter` for a new store. The negative/filter-retraction and
  link-triggered-reprojection designs touch the *pipeline* fan-out, not the adapter seam — orthogonal.

---

### Summary for the board

`📐 awaiting-Andrew · [design](../../implementation-artifacts/search-target-adapter-design.md)` — third
lens-target adapter (OpenSearch recommended); no contract change; build gated behind a real search
consumer; vendor fork + Postgres-FTS-interim blessing for Andrew.
