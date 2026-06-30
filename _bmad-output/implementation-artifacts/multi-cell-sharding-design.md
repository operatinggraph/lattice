# Multi-cell / sharding (horizontal scale-out via cells) — design

**Status: ✅ Andrew-ratified (2026-06-29)** — fork = C (cell = bucket; a cluster hosts 1+ cells) with the A-flavored intra-cluster default; secondaries as recommended (N:1 tenant default, Loom saga for cross-cell atomicity, cell-local lenses); Contract #2 §2.6 `CellMoved` ratified + committed; **whole build shelved** behind Gateway + HA-NATS + a real scale driver. **One correction folded in (see §3.6 + the new "Global-identity" extension flag): the "never 1:N" rule was an over-claim** — a hyperscale tenant (WeWork/Flow class) must span cells, which surfaces the global-identity problem; that handling (cross-cell *and* cross-region/residency) is a **named open extension**, not part of this core. See the *Ratified* block. Author: Winston (Designer fire, 2026-06-29).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Scale-out → Multi-cell / sharding* (★ now / ★★★ at
scale, XL). Grounds in `lattice-architecture.md` (**P2** sole-writer / serialization, **P5** lens read-path,
**P6**/NFR-SC2 cell-agnostic keys, the KV Bucket Taxonomy §69–95, Stream-8 Cells/Sharding §436–438, the
quantitative targets §158, the deferred-stream framing §32/§56), the **ratified deployment model**
`docs/operations/deployment-isolation.md` §66–109 (the per-cell topology + Gateway routing + cell-agnostic-key
invariant — *the canonical target this design realizes*), the **vault source** `Obsidian Vault/Lattice/Sharding/
Cell.md` (the Cellular Sharding Strategy + the four-phase Migration Dance + the Dual-Target shadow-write — the
original architectural sketch), the brainstorming inventory §160–173 (items **77–89**) + §199–214 (the
adversarial cross-cell items **#109, #110, #114, #121, #124**), the **sibling ratified-pending HA-NATS design**
`ha-nats-clustering-design.md` (the per-cell *availability* substrate this *scale* layer composes on), the
**awaiting-Andrew Gateway design** `gateway-external-trust-boundary-design.md` (the routing host), Contract #1
§1.1 (key shapes — unchanged), Contract #2 §2.6 (the error enumeration — one staged addition), and the substrate
seams (`internal/substrate/{keys,kv,batch,conn}.go`, `internal/bootstrap/primordial.go`, the Processor commit
path). A self-adversarial pass ran as part of this fire; its findings are folded into §6–§8.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Lets one logical Lattice grow past a single Core-KV bucket's ceiling by
splitting the graph into **cells** (a cell = one Core-KV bucket holding a co-located sub-graph), with a
**Global Adjacency Index** (`vertex → cell`) the Gateway routes on, **Bridge Links** for cross-cell edges, and a
zero-data-loss **live-migration dance** to rebalance a hot cell — **all without touching the Core-KV key shapes**
(NFR-SC2 is a locked invariant: cell identity lives in the bucket name + the index, never in a key).

**The one architectural fork (designed through — your call). Cell granularity + the migration mechanism that
flows from it:**
- **A — cells are buckets on a *shared* HA cluster** (the vault Cell.md model). Migration = the elegant
  **atomic dual-target shadow-write** (both buckets live in one cluster, so one atomic batch spans them →
  zero-data-loss, *no write-freeze*). Cheap to add a cell; a single Processor/Refractor fleet serves all cells.
  *Cost:* one cluster's JetStream meta-group + node resources bound total scale; a cluster outage takes all its
  cells (shared blast radius).
- **B — cells are fully isolated NATS deployments** (the ratified `deployment-isolation.md` model: per-cell
  NATS/Refractor/Capability-KV). Maximal isolation + blast-radius containment + true node-level horizontal
  scale. *Cost:* atomic dual-write **can't cross clusters**, so live migration becomes a **saga**
  (freeze-hydrate-verify-cutover) with a brief per-subgraph write-pause.
- **C — RECOMMENDED synthesis: a cell IS a bucket; a NATS cluster hosts one-or-more cells.** Routine
  load-spreading uses **intra-cluster cells + the atomic dual-write migration** (A's elegance, the common case);
  **cross-cluster cells (B, saga migration)** are reserved for blast-radius isolation / scaling past one
  cluster. The Gateway's index maps `vertex → cell → cluster-endpoint`; the migration mechanism is *chosen by
  whether source and target cells share a cluster*. This is a strict superset of both A and B and reconciles the
  vault sketch with the ratified deployment doc. **The decision for you:** which posture is the *default* — A's
  intra-cluster-cells (simpler ops, lower isolation) or B's isolated-deployments (stronger isolation, saga
  migration). I recommend **C with the A-flavored intra-cluster default**, escalating to B per-tenant on demand
  (§6.1).

**Three secondary decisions (designed through, recs given, not forks):** **multi-tenant mapping** (#124) — rec
**N:1 default** (many tenants per cell), **1:1** as an isolation upgrade, **never 1:N** (a tenant's atomically-
related subgraph must be co-located) (§3.6); **cross-cell atomic mutations** (#121) — rec **Saga via a Tracker
vertex + compensation** (the anchor principle keeps *most* atomicity within-cell) (§3.5); **cross-cell lens
completeness** (#110) — rec **cell-local lenses only** (the ratified posture: no cross-cell projection fan-out;
remote endpoints project as opaque `{cell, key}` refs, assembled at the Gateway/app layer) — which **dissolves**
the watermark/vector-clock problem rather than solving it (§3.4).

**Frozen-contract change: ONE small staged addition.** Contract **#2 §2.6** gains a reserved error code
**`CellMoved`** — the `410 Gone` analog (brainstorm #86): the only **client-observable** new surface, returned by
a drained cell that receives a stray write for a vertex it has handed off, telling the caller to refresh the
index and re-route. Everything else is **build-to** or **deployment/operational config**: key shapes are
**unchanged** (NFR-SC2 — *the* enabling invariant; this design's biggest payoff is that bridge links need **no
rewrite** on migration because they embed no cell id); the Global Adjacency Index + migration shadow-state are
**P1 operational state** (outside Core KV, like Weaver's mark-lease); the dual-target batch is internal Processor
behavior (Contract #2 step-8 commit semantics unchanged); per-cell laning/replication is deployment config (the
HA-NATS / write-restriction precedent). The §2.6 edit is **staged UNCOMMITTED in `main`** (the diff is the
proposal); the rest of the design is built against it.

**Build sequencing (honest — ratify-now / build-on-driver).** This is an **XL, build-deferred** design with
**hard prerequisites that are themselves not yet built**: the **Gateway** (the routing host — awaiting-Andrew)
and **HA-NATS clustering** (the per-cell availability substrate — awaiting-Andrew). At the locked target of
**10–100 ops/sec / ~500 members / ≤100K keys** (§158) a single cell is **orders of magnitude** within the
single-bucket ceiling — so there is **no current scale driver** and **no pressure to ship dead scaffolding**. The
recommendation: **ratify the design + the §2.6 edit, and shelve the build behind a real
scale/blast-radius driver** (like Vault/D1/HA were ratified-but-deferred), **except Fire 1** (the cell-addressing
substrate seam — no-behavior-change at one cell, de-risks the whole stack) which passes the dead-scaffolding test
on its own (§7).

**Ratified (Andrew, 2026-06-29).** **Fork = C** (cell = bucket; a cluster hosts 1+ cells), **A-flavored
intra-cluster default** (escalate a tenant to an isolated cross-cluster cell on demand). **Secondaries as
recommended:** N:1 tenant default / 1:1 on demand, Loom saga + compensation for cross-cell atomicity, cell-local
lenses. **Contract #2 §2.6 `CellMoved` ratified + committed.** **Build: ALL fires shelved** behind the two hard
prerequisites (Gateway + HA-NATS — both ratified this session, neither built) + a real scale driver — full-shelve
including the de-risking Fire 1 (consistent with HA; zero scale urgency at 10–100 ops/sec / ≤100K keys).

**Correction Andrew surfaced — "never 1:N" over-claims; the GLOBAL-IDENTITY problem is a named open extension
(two axes), NOT part of this core.** §3.6's "never split a tenant" only holds for a tenant that *fits* in a cell;
the honest rule is **"never split an *atomically-related subgraph*."** A **hyperscale tenant** (WeWork — coworking
real-estate, hundreds of properties; Flow — branded residential — both with **membership global across the
tenant**) **must** span cells, making the tenant-global identity a high-fan-in cross-cell hub the anchor principle
can't co-locate. Handled as a dedicated follow-on (standalone backlog item), two axes:
- **Axis 1 — cross-cell (within a deployment).** Leading candidate **shadow vertices** (the Edge Lattice concept:
  a read-only *anchor* for a remote entity) — canonical identity in a home cell, PII-light pseudonymous shadows
  in the cells where the member acts, so common member ops stay cell-local atomic and the home cell isn't a read
  hotspot. **The real content is the consistency contract, not the mechanism:** auth goes eventually-consistent
  across cells (D1's accepted class, window widened); candidate split = capabilities lag per-cell, **revocations
  on a global pseudonymous-keyed fast-path.** Not assumed correct (alternatives: dedicated identity tier;
  shard-by-identity — both weaker; shadows ≈ "cache the identity tier into its consuming cells").
- **Axis 2 — cross-region + data residency (going global).** Path forward **validated by this core**:
  `cell-registry` gains `region/jurisdiction` → **cell = residency zone**; **NFR-SC2 makes residency relocation
  key-free** (the migration dance to a different-region cell); **cell-local lenses are the residency-safe read
  posture** (no implicit cross-region PII projection); the pseudonymous index is globally-replicable. **Residency
  reshapes the shadow into a strictly PII-stripped anchor** (canonical PII stays home; never auto-replicate PII
  across borders; cross-border PII access is explicit/consented/logged). **Placement fail-closed on residency**
  (residency-tagged vertex → only a matching cell; absent tag → most-restrictive). **Hard edge — air-gapped
  sovereign jurisdictions** (PIPL/Russia, where even pointers may not cross) = a **federation** case (model C
  cross-cluster at its limit: per-jurisdiction federated identities, not global-identity-plus-shadow). Net: going
  global **adds a residency dimension to placement + reshapes the shadow — it does not fork the core model.**

---

## 1. Problem + intent

**Today the whole graph lives in one Core-KV bucket on one NATS deployment.** `internal/bootstrap/primordial.go`
provisions exactly one `core-kv` bucket; every vertex/aspect/link key the platform writes lands there. P6/§217
states the safety case plainly: *"Single-cell MVP is safe because the data model is cell-agnostic … Safety
depends on the expected MVP data volume fitting within NATS KV single-bucket scalability limits (validated …: up
to 100K keys at MVP)."* That ceiling is fine for the locked demand profile — but **scale-out is the named
deferred Stream 8** (§32/§436), and the architecture has *always* promised that crossing the ceiling is **a
routing/replication concern layered underneath, not a data-model change** (P6).

**Intent:** realize that promise. Make a single logical Lattice **span N cells** so total graph size, write
throughput, and blast radius scale horizontally — **without** changing any Core-KV key (NFR-SC2 is locked),
**without** changing the commit/auth/DDL semantics any component already relies on, and by **reusing** what is
already cell-ready (topology-agnostic keys; per-cell-isolated Refractor; the Gateway as the routing seam; the
HA-NATS replication layer) rather than reinventing it. The marquee architectural payoff: because keys embed no
cell id, **a vertex can move cells, and a cross-cell link can point at it, with zero key rewriting** — the index
flip is the *only* state that changes.

This is the **horizontal-scale** layer. Its sibling **HA-NATS clustering** (ratified-pending) is the
**vertical-resilience** layer *within* a cell. They compose: a production cell is an HA cluster; multi-cell adds
more cells. This design depends on HA for per-cell availability and on the **Gateway** for routing; it does not
re-solve either.

## 2. Reconciliation with the existing mental model (didn't we already…?)

- **"Isn't this already designed in `deployment-isolation.md`?"** *That doc states the **target topology**
  (per-cell NATS/Core-KV/Refractor/Capability-KV; Gateway routing; cell-agnostic keys) but not the **mechanism**:*
  how a vertex is *placed* in a cell, how a cell is *added*, how a hot cell is *rebalanced live without data
  loss*, how a cross-cell link is *stored and resolved*, and what *new code* (if any) each component needs. This
  design fills that gap and reconciles the doc with the older vault Cell.md sketch (which had the migration dance
  but predated the per-cell-deployment framing). **Where they conflict — "cell = bucket" (vault) vs "cell =
  deployment" (doc) — §3 resolves it with model C** (a cell is a bucket; a deployment hosts one-or-more cells).

- **"Doesn't NFR-SC2 already make this free?"** *It makes it **possible**, not **free**.* NFR-SC2 (keys embed no
  cell id) is the load-bearing invariant that means **no key migration tooling** and **no bridge-link rewrite** —
  the single hardest part of graph sharding (relocating a node and re-pointing its edges) collapses to *flip one
  index entry*. But you still need the index, the router, the bridge-link writer, the migration coordinator, and
  the cross-cell saga. NFR-SC2 removes the *data-model* work; this design is the *routing/replication/migration*
  work P6 said would remain.

- **"Does this duplicate HA-NATS clustering?"** *No (verified against the sibling design).* HA = replicate **one
  cell's** data across nodes (survive a node loss). Multi-cell = spread **the graph across cells** (survive the
  single-bucket ceiling; contain blast radius). HA is *within* a cell; multi-cell is *across* cells. The HA
  design itself names this row as its horizontal sibling ("each cell runs as an HA cluster"). They share the
  substrate replica seam (Fire 1 here reuses HA's `NATS_REPLICAS` plumbing) and the leader-election lease family
  (the migration coordinator is a lease-holder, §3.3.1).

- **"Does the Gateway already do this?"** *The Gateway design builds the **HTTP→NATS verify-and-stamp
  translator**; routing-by-cell is the **next layer on the same host.*** This design adds the **Global Adjacency
  Index read + cell-routing** to the Gateway (the deployment-isolation doc explicitly assigns topology
  resolution to the Gateway: *"the Gateway resolves topology; the components do not"*). So the Processor stays
  **cell-unaware in steady state** — the only Processor change is the *migration* dual-write window (§3.3).

- **"Does it introduce new persistent state, and is it shaped like state we already keep?"** Two new operational
  artifacts: the **Global Adjacency Index** (`vertex → cell`, a replicated KV) and the per-migration
  **shadow-state** (`MIGRATING / target_cell`). Both are **P1 operational state** (single-purpose bookkeeping
  outside Core KV) and the *same family* the platform already runs (Health KV, Weaver `weaver-state`, the
  Adjacency KV). No new Core-KV vertex/aspect/link; P2/P5 untouched.

## 3. The shape

A cell is a **Core-KV bucket** holding a co-located sub-graph (model C, §6.1). Six pieces: placement, the index,
the router, bridge links + cell-local lenses, cross-cell mutations, and the migration dance.

### 3.1 The cell + the Anchor Principle (placement)

- **Cell = one Core-KV bucket** (`core-kv` for cell 0 — byte-identical to today; `core-kv-<cellId>` for added
  cells; `cellId` is a short NanoID, never in a *key*). A bucket may share a NATS cluster with sibling cells
  (intra-cluster) or be its own cluster (cross-cluster) — §6.1.
- **Anchor Principle (brainstorm #78, vault §1):** a root vertex, **all its aspects, and all its outgoing links**
  are co-located in the same cell. This is what makes the Processor's **atomic batch** (Contract #2 step-8) still
  atomic — the batch never crosses a bucket boundary in steady state, because a vertex and everything it owns
  live together. **Enforced** by the placement rule: an op's writes are scoped to the target vertex's cell (the
  router, §3.3, resolves the cell before dispatch; the Processor writes only that cell's bucket).
- **Placement on create:** a *new root* vertex is assigned a cell by the Gateway's **placement policy**
  (default: the actor's tenant's cell — §3.6; or least-loaded-cell for tenantless platform vertices). The
  assignment is recorded in the index (§3.2) *as part of* the create — see the create ordering in §3.7. A
  *dependent* vertex (one whose DDL co-creates it under a root) inherits the root's cell (anchor principle).

### 3.2 The Global Adjacency Index (`vertex → cell`)

- **Shape:** a small, **replicated** operational KV bucket `cell-index` (R3, P1 operational state) mapping
  `<vertexKey> → {cell, status, targetCell?}` where `status ∈ {live, migrating}`. A companion static
  `cell-registry` maps `cell → {clusterEndpoint, tenant?, status}`. Both are **operational state**, not Core KV
  (no vertex/aspect/link; not a lens).
- **Why a flat per-vertex map, not a hash function:** sharding a graph by `hash(key)` would *shatter*
  relationships (vault §opening) — a vertex and its links could land in different shards, breaking the anchor
  principle and atomicity. An explicit `vertex → cell` map lets placement honor locality (a tenant's subgraph
  co-located) and lets a vertex *move* without a key change. The index is the price of graph-aware sharding; it
  is small (one short entry per **root** vertex — dependents inherit, so it is far smaller than the key count)
  and replicated for availability.
- **Read path:** the **Gateway** reads `cell-index` to route (§3.3). The Processor reads it only during a
  migration window (to know it must dual-write). Refractor never reads it (it is bound to its own cell's bucket).
  **P5 holds** — the index is operational routing state, not a business query surface.

### 3.3 The cell-aware operation router (brainstorm #89) — at the Gateway

Routing lives at the **Gateway** (the ratified topology-resolver), keeping the Processor cell-unaware in steady
state:

1. The Gateway receives a stamped op (post verify-and-stamp, per the Gateway design). It extracts the op's
   **target root vertex** (the entity the op mutates — already present in the envelope's hydration target).
2. It reads `cell-index[targetRoot]` → `cell` (a create with no existing target uses the **placement policy**,
   §3.1, and writes the index entry as part of the create flow, §3.7).
3. It publishes the op to **that cell's `core-operations` stream** (per-cell stream → the Processor fleet for
   that cell consumes it). The op envelope is **unchanged** (NFR-SC2: no cell field).
4. **Stray-write rejection (`CellMoved`, §3.3.2):** if a cell receives an op for a vertex it has **drained**
   (handed to another cell), its Processor rejects with `CellMoved{newCell}` so the Gateway/client refreshes the
   index and re-routes. This is the only path where the Processor is cell-aware outside migration.

**Per-cell laning (#114) is automatic:** each cell has its *own* `core-operations` stream and its own per-lane
durable consumers (the shipped per-lane-consumer work). A slow `urgent` op in cell A cannot head-of-line-block
cell B — they are different streams. The §114 "per-cell sub-laning" gap is **dissolved by cell = own-stream**,
not solved with extra partitioning.

#### 3.3.1 The migration coordinator (a leased singleton)

Migration is driven by a **coordinator** holding the HA-design's leader-election lease (`leader.cellmigration`)
— exactly the TTL+CAS lease family the platform already runs (Weaver mark-lease, Loom deadline, the HA Processor
lease). One coordinator runs the four-phase dance (§3.7) for a subgraph; the lease guarantees a single driver.

#### 3.3.2 `CellMoved` — the one client-observable new surface

When a cell drains a vertex (§3.7 Phase D), it keeps a short-lived **tombstone-redirect** in its local cell-index
view. A stray write (a client/Gateway with a stale cached route) is rejected `CellMoved{newCell}` rather than
silently accepted (which would lose the write) or blindly forwarded (the Processor is not a router). The caller
refreshes `cell-index` and re-routes. **This is the `410 Gone` of brainstorm #86, surfaced as a Contract #2 §2.6
error code** (the one staged contract edit, §4).

### 3.4 Bridge Links + cell-local lenses (#80, #109, #110, #88)

- **Bridge Link writer (#80):** a link whose two endpoints live in **different** cells (e.g. a Resident in cell A
  `heldBy` a Lease in cell B) is written as a **Bridge Link — stored in BOTH cells** (the source cell and the
  target cell), with the **identical** `lnk.<typeA>.<idA>.<rel>.<typeB>.<idB>` key in each (NFR-SC2 — the key
  embeds no cell id). Each cell's local traversal sees "an edge to a vertex that isn't local"; it resolves the
  remote endpoint's location via `cell-index`, never via a cached cell id on the link.
- **Why #109 dissolves (the NFR-SC2 payoff):** the brainstorm worried *"when a vertex moves cells, all incoming
  bridge links from other cells point to a stale Cell_ID — who rewrites them?"* **Answer: nobody — there is no
  stale Cell_ID to rewrite.** A bridge link stores no cell id; it stores the canonical (cell-agnostic) key. The
  *only* state that changes on migration is `cell-index[vertex]`, which every bridge-link resolution already
  reads. **This is the single biggest reason NFR-SC2 was locked**, and it is the design's cleanest win.
- **Cell-local lenses (#110, the ratified posture):** **Refractor is strictly per-cell** (deployment-isolation
  §107) — each Refractor projects only its own cell's CDC stream; **there is no cross-cell projection fan-out.** A
  lens that would traverse a bridge link projects the remote endpoint as an **opaque `{cell, key}` reference**
  (a flattened ref column), **not** by following the edge into another cell. Cross-cell queries are assembled at
  the **Gateway/application layer** by reading the relevant cells' lens targets and stitching. **This dissolves
  #110** (the "is my cross-cell view complete? out-of-order events across cells = wrong intermediate state"
  watermark/vector-clock problem) by **constraining lenses to be cell-complete by construction** — a cell-local
  lens is always complete over its own cell, and the cross-cell join is an explicit, eventually-consistent
  assembly, not an implicit traversal. *(Rejected alternative: cross-cell lenses with per-cell watermarks or a
  per-lens vector clock — far more machinery for a join the app layer can do explicitly; §6.3.)*
- **Refractor cross-cell dedup (#88):** needed **only** during an *intra-cluster* migration window, when **one**
  Refractor subscribes to both the source and target cell buckets and sees two CDC events for the same vertex
  (one per bucket). It dedups by **Revision ID** (the vault §5 mechanism). In the cross-cluster (per-cell-
  Refractor) case there is no shared Refractor, so no cross-cell dedup is needed — each cell's Refractor sees only
  its own bucket.

### 3.5 Cross-cell atomic business mutations (#121) — Saga, not batch

An atomic batch **cannot span cells** (different streams/clusters; the anchor principle exists precisely so it
never needs to in steady state). For the **rare** genuinely-cross-cell atomic business operation — transferring
state between two roots in different cells (asset A→B, a lease re-assigned across tenants) — the answer is a
**Saga via a Tracker vertex**, orchestrated by **Loom** (the existing sequential-workflow engine):

- The transfer is modeled as a `vtx.transfer.<id>` Tracker (in the *initiating* cell) with a Loom pattern:
  *debit-source (atomic, cell A) → credit-target (atomic, cell B) → mark-complete*; a failure after the debit
  triggers the **compensation** step (credit-source-back). Each step is a within-cell atomic op; the Tracker
  records saga state for idempotent recovery (the same shape Loom already uses for external-I/O claims).
- **The anchor principle minimizes how often this is needed:** because a vertex + its aspects + outgoing links
  are co-located, *most* business atomicity (the lease-application convergence, the clinic booking guard) stays
  **within one cell** and uses the ordinary atomic batch unchanged. Cross-cell sagas are the exception, reserved
  for true inter-subgraph transfers. **Recommendation: accept eventual-consistency-with-compensation for
  cross-cell transfers** (the only alternative — a distributed 2PC across cells — reintroduces the cross-cell
  lock the cellular model exists to avoid; §6.3).

### 3.6 Multi-tenant cell mapping (#124)

- **Recommendation: N:1 default, 1:1 on demand.** Many tenants share a cell (good utilization) until a tenant is
  large/sensitive/noisy enough to earn its **own** cell (the 1:1 isolation upgrade — a live migration of that
  tenant's subgraph into a fresh cell, §3.7). The **placement policy** (§3.1) keys on tenant: a new root lands in
  its tenant's cell.
- **The precise rule is "never split an *atomically-related subgraph*," NOT "never split a tenant"** (correction,
  Andrew 2026-06-29 — the earlier "never 1:N" was an over-claim). For a tenant that *fits* in a cell the two
  coincide. But a **hyperscale tenant** (WeWork / Flow class — too big for one cell, with **identity global across
  the tenant**) **must** span cells (1:N at the tenant level), which is sound **as long as each atomically-related
  subgraph stays whole**. The hard part it surfaces is the **tenant-global identity** (a high-fan-in vertex
  referenced from many cells, which the anchor principle cannot co-locate) — handled as a **named open extension,
  two axes (cross-cell shadows + cross-region/residency), in a dedicated follow-on** (see the Ratified block + the
  standalone backlog item), **not** in this core design.
- This mapping is **cell-registry metadata** (`cell → tenant?`), operational state, not Core KV. It composes with
  the Gateway's per-tenant routing and (later) D1's per-tenant authz.

### 3.7 The migration dance (live rebalance, no data loss) — vault §3

When a cell crosses a load threshold (or a tenant earns a 1:1 cell), the coordinator (§3.3.1) moves a subgraph
from `C_src` to `C_dst` in four phases (vault Cell.md §3, made concrete):

- **Phase A — Shadow State (initiate).** Coordinator sets `cell-index[root] = {cell: C_src, status: migrating,
  targetCell: C_dst}` for each root in the subgraph. Routing still points at `C_src`; the flag tells the
  `C_src` Processor to **dual-write**.
- **Phase B — Dual-target shadow-write (vault §2/§4).** While the bulk copy runs, **live** ops to a migrating
  vertex are executed **once** (one Starlark run) and committed as a **Dual-Target MutationBatch**: the write to
  **`C_src` keeps the OCC `expectedRevision` check** (C_src stays the "current truth"); the write to **`C_dst` is
  a blind write** (no revision check — C_dst is just being kept warm). **Intra-cluster** (C): both buckets share a
  cluster → this is **one atomic batch spanning both streams** (the Processor already spans `core-kv` +
  `loom-state` in one batch — verified, ADR-50), so the dual-write is **atomic and zero-loss**. **Cross-cluster**
  (B): the atomic batch can't span clusters → the dual-write degrades to **commit-to-C_src-then-async-mirror**,
  with the **Hydrator + merkle-verify (Phase C) closing any gap** before cutover (the saga-style fallback, §6.1).
- **Phase C — Bulk sync + convergence-verify.** A background **Hydrator** copies the historical (untouched-by-
  live-traffic) aspects/links from `C_src` to `C_dst`. **Weaver verifies** convergence with a **merkle-style
  subgraph comparison** (brainstorm #84 — Weaver is the convergence engine; this is a convergence target:
  *"C_dst matches C_src"*). Only on a verified match does the coordinator authorize cutover.
- **Phase D — Switchover + drain.** Coordinator **atomically flips** `cell-index[root] = {cell: C_dst, status:
  live}` (the index CAS is the commit point of the whole migration). `C_src` **stops accepting** new writes for
  those vertices and serves **`CellMoved{C_dst}`** to any stray write (§3.3.2). After the **idempotency horizon**
  (Contract #4 §4.3 tracker TTL — so no in-flight retry re-executes against the drained copy), the **prune** step
  deletes the moved subgraph from `C_src`.

**Create ordering (the index-vs-data race):** a new root's `cell-index` entry is written **before** its Core-KV
data is committed isn't required — instead, the **Gateway's placement decision is recorded in the index as the
first step of the create flow**, and a create whose index entry is missing falls back to the default-cell read
(idempotent: the create commits to the placed cell, and the index is back-filled on first route). The index is
**eventually consistent with placement**, never authoritative *over* the data — the cell's bucket is the truth
for *what* a vertex is; the index is the truth for *where* it lives.

## 4. Contract surface

**One small staged frozen-contract edit; everything else is build-to or deployment config.**

- **Contract #1 §1.1 (key shapes): NO change — and this is the keystone.** NFR-SC2 is a **locked invariant**
  (deployment-isolation §80): no cell-prefixed/deployment-prefixed Core-KV keys, ever. Cell identity lives in the
  **bucket name** (substrate addressing) + the **`cell-index`** (operational state), never in a key. This is what
  makes bridge links rewrite-free (§3.4) and migration key-free (§3.7). **Staging anything in Contract #1 would
  be a regression.**
- **Contract #2 §2.6 (error enumeration): ONE addition — `CellMoved` — STAGED UNCOMMITTED.** The drained-cell
  stray-write reject (§3.3.2, brainstorm #86) is a new **client-observable** structured reject, and §2.6 is the
  closed enumeration of such codes (*"the enumeration is extensible — Phase 2+ may add codes; existing codes are
  immutable contract"*). The edit reserves `CellMoved` (returned by a cell that has drained the target vertex;
  `details.newCell` lets the caller refresh + re-route). **Affected consumers:** the Gateway/router (acts on it —
  refresh + retry) and any direct client. It does not alter any existing code or commit step. *The diff is the
  proposal; staged in `main`, not committed (Andrew ratifies + commits).*
- **Contract #2 step-8 atomic batch: NO change.** The Dual-Target MutationBatch is **internal Processor behavior**
  using the *same* atomic-batch primitive (which already spans multiple streams; ADR-50 supports replicated +
  multi-stream batches). The intra-cluster dual-write is atomic; the cross-cluster fallback is explicitly
  non-atomic-with-verify (§3.7 Phase B/C). Commit *semantics* are preserved.
- **Contract #4 §4.3 (idempotency tracker TTL): NO change.** The migration prune (Phase D) **reuses** the
  idempotency horizon as the safe-to-delete fence — a build-to use of an existing seam.
- **The Global Adjacency Index, cell-registry, shadow-state, migration coordinator lease: P1 operational state.**
  Not Core KV, not a lens, not a contract (the Health-KV / Weaver-state / Adjacency-KV precedent).
- **Doc touch-ups the Steward applies at build (docs, not contracts):** `deployment-isolation.md` (flip the
  "Verification Path (future)" + the topology sections from *spec-for-future* to *designed/shipped*, add the
  index + migration mechanism); `lattice-architecture.md` Stream-8 status; a "cell posture" note in
  `processor.md` / `refractor.md` / a new `docs/components/gateway.md` routing section. `/docs` edits, staged by
  the Steward in build fires — **not** part of this design's commit.

## 5. Migration / compatibility

- **Single-cell is the zero-config default, byte-identical to today.** Cell 0 = the `core-kv` bucket; with one
  cell, the router always resolves to it, the index has the default fallback, no bridge links exist, no migration
  runs. **At one cell, nothing changes** — the entire multi-cell layer is inert (the dead-scaffolding-free
  property of Fire 1).
- **Adding a cell** is an ops action (provision `core-kv-<id>` + register it) + a placement-policy update; new
  roots route to it. **No existing data moves** unless a migration is run.
- **Rebalancing** (moving a hot subgraph) is the §3.7 dance — online, no key rewrite (NFR-SC2), no downtime
  beyond the bounded Phase-D index flip + the per-stray-write `CellMoved` refresh.
- **Rollback / cell removal:** migrate a cell's subgraphs out, then decommission the bucket — symmetric to add.
- **No key-migration tooling, ever** (NFR-SC2) — the property the architecture locked Phase 1 to guarantee
  exactly this.

## 6. Risks + alternatives

### 6.1 The fork — cell granularity + migration mechanism (A vs B vs **C**)

Restated from the For-Andrew block with the trade matrix:

| | **A — bucket on shared cluster** | **B — isolated deployment** | **C — RECOMMENDED (superset)** |
|---|---|---|---|
| Cell unit | Core-KV bucket | Whole NATS+Refractor+CapKV deployment | Bucket; a deployment hosts 1+ cells |
| Migration | **Atomic dual-write** (one cluster) | **Saga** (freeze-hydrate-verify-cutover) | Atomic intra-cluster; saga cross-cluster |
| Isolation / blast radius | Shared cluster (weaker) | Per-cell (strongest) | Tunable per tenant |
| Horizontal node-scale | Bounded by one cluster | Unbounded (add deployments) | Unbounded (add clusters) |
| Ops complexity | Lower | Higher | Medium (both modes) |
| Matches | vault Cell.md | deployment-isolation.md | both |

**Recommendation C with an A-flavored intra-cluster default:** start tenants in shared-cluster cells (cheap,
atomic migration), and **escalate a tenant to a dedicated cross-cluster cell on an isolation/scale driver** (a
1:1 migration that happens to also cross clusters → saga path). This reconciles the vault elegance with the
ratified isolation doc and avoids a premature all-isolated posture that pays B's ops cost before any tenant needs
it. **The fork for Andrew:** ratify C, and pick the *default* — A-flavored (my rec: simpler ops, atomic
migration, escalate-on-demand) vs B-flavored (stronger default isolation, saga migration from day one). I
recommend A-flavored-default; both are within C.

### 6.2 Hard prerequisites (sequencing honesty)

Multi-cell **cannot ship before** its two prerequisites, both **awaiting-Andrew**: the **Gateway** (the routing
host — without it there is nowhere to put the cell router) and **HA-NATS clustering** (per-cell availability — a
single-node cell is a SPOF the scale story can't accept). Plus a **real scale or blast-radius driver** (none
exists at 10–100 ops/sec / ≤100K keys). This is why the design is **ratify-now / build-on-driver** with only
Fire 1 buildable now. *This is the [[feedback_designer_chain_grounding]] discipline applied: the dependency
chain (Gateway → HA → multi-cell) is sequenced one-way, and no fire hands a consumer to an unbuilt producer —
Fire 1 is a pure no-op seam, the rest waits behind ratified+built prerequisites.*

### 6.3 Other risks + rejected alternatives

- **Cross-cell lens completeness (#110).** *Rejected:* cross-cell lenses with per-cell watermarks / per-lens
  vector clocks — correct but heavy, and it pushes distributed-consistency reasoning into the projection layer.
  *Chosen:* cell-local lenses + opaque remote refs + app-layer assembly (§3.4) — the ratified posture; the join
  is explicit and eventually-consistent, which is acceptable for a read view.
- **Cross-cell atomicity (#121).** *Rejected:* distributed 2PC across cells — reintroduces the cross-cell lock the
  cellular model exists to avoid, and NATS atomic batch can't span clusters anyway. *Chosen:* Loom saga +
  compensation (§3.5); the anchor principle keeps it rare.
- **Index hot-spotting.** The `cell-index` is read on **every** routed op (at the Gateway). *Mitigation:* it is a
  replicated KV with a Gateway-side cache (short TTL); a stale cache is **self-correcting** via `CellMoved`
  (§3.3.2) — a stale route costs one refresh, never a lost write. The index holds **one entry per root** (not per
  key), so it is far smaller than the graph.
- **Migration coordinator is a singleton.** It holds the HA lease (§3.3.1); a coordinator crash mid-migration is
  recoverable from the shadow-state in `cell-index` (the dance is idempotent per phase — re-hydrate, re-verify,
  re-flip). A stalled migration **degrades** (the subgraph stays on C_src, dual-writing) but never corrupts.
- **Atomic-batch-spans-two-buckets assumption (load-bearing for intra-cluster dual-write).** Verified: the
  Processor's step-8 commit already batches `core-kv` + `loom-state` (HA design §2; ADR-50). *Risk:* if a future
  NATS pin narrows atomic batches to a single stream, the intra-cluster dual-write degrades to the cross-cluster
  saga path (graceful, already designed). Re-validate on the pinned NATS in Fire 4.
- **Bridge-link orphan on migration race (#109 residual).** Because bridge links store no cell id, there is
  nothing to rewrite — but a bridge link to a vertex *mid-migration* must resolve via `cell-index` and may briefly
  see `status: migrating` (both copies valid; read either, prefer C_src until cutover). Resolution rule documented
  in §3.4; no race window produces a wrong answer because C_src remains truth until the atomic flip.

### 6.4 Self-adversarial pass (folded in)

- *"NFR-SC2 says cells are 'free' — is this design overweight?"* — No: NFR-SC2 removes the **data-model** work
  (no key rewrite); the index/router/bridge-writer/migration/saga is the **routing/replication** work P6
  explicitly said remains (§2). The design is exactly the residue NFR-SC2 *didn't* eliminate.
- *"The atomic dual-write only works intra-cluster — does that secretly force model A?"* — No: model C makes the
  migration mechanism a **function of whether the cells share a cluster** (atomic if yes, saga if no), so both
  postures are supported and the operator chooses per migration (§3.7 Phase B).
- *"Doesn't `CellMoved` leak topology to clients (the doc says the Gateway hides topology)?"* — It surfaces a
  *cell id to refresh-on*, not the cluster endpoint; the Gateway still resolves `cell → endpoint`. And it fires
  **only** in the brief stale-route window during a migration drain — steady-state clients never see it.
- *"Is the index a new SPOF?"* — It is R3 replicated (survives a node) and Gateway-cached; a transient index read
  failure fails the route closed (reject + retry), never a misroute. OCC + `CellMoved` are the correctness
  guarantees, the index is the optimization (mirrors the HA design's lease-vs-OCC reasoning).
- *"Does multi-cell break D1 read-path auth / Capability KV?"* — Capability KV is **per-cell** (deployment-
  isolation §97: each cell's Refractor populates its cell's Capability KV from its cell's Core KV). An actor's
  capability projects in the cell(s) where its identity + grants live; cross-cell authz composes via the same
  per-cell projection. D1 RLS is per-cell-Postgres. **Stated as a composition point**, not solved here (D1 is its
  own ratified track); a cross-cell authz audit is a Fire-5 concern.

## 7. Decomposition for the Steward (fire-by-fire)

Each fire is independently shippable + green. **Honest sequencing:** **Fire 1 is buildable now** (no-behavior-
change seam, de-risks); **Fires 2–6 sequence behind the Gateway + HA-NATS prerequisites + a real scale driver** —
ratify the design + the §2.6 edit, shelve the build (like Vault/D1/HA).

- **Fire 1 — cell-addressing substrate seam (buildable now; no behavior change; full review).** Parameterize the
  Core-KV bucket name behind a `coreKVBucket(cellId)` resolver (default `cellId=""` → `core-kv`, byte-identical);
  thread it where the bucket name is hard-coded (`primordial.go`, the Processor commit path, Refractor's CDC
  subscribe, Loupe's corekv reads). Add a **multi-bucket test fixture** (two `core-kv-<id>` buckets in one
  embedded NATS) proving a vertex written to cell A is invisible in cell B and vice-versa. *No behavior change at
  one cell; the seam + fixture are exercised + reusable → not dead scaffolding.* Reuses HA's `NATS_REPLICAS`
  plumbing for per-cell replicas.
- **Fire 2 — Global Adjacency Index + Gateway cell-router (behind Gateway + HA).** The `cell-index` /
  `cell-registry` replicated KVs; the placement policy; the Gateway routing read + per-cell `core-operations`
  publish; the `CellMoved` reject + the stray-write path. Tests: place → route → cross-cell route → stale-route
  `CellMoved` → refresh.
- **Fire 3 — Bridge Link writer + cell-local lens refs (behind Fire 2).** The dual-cell bridge-link write; the
  index-resolved remote-endpoint read; Refractor projecting a remote endpoint as an opaque `{cell, key}` ref;
  intra-cluster cross-cell **Revision-ID dedup** (#88). Tests: a cross-cell link resolves both directions; a lens
  emits the opaque ref, not a traversal.
- **Fire 4 — the migration dance (behind Fire 3; full 3-layer, P2 plane).** The coordinator (on the HA lease);
  Phase A shadow-state; the **Dual-Target MutationBatch** (atomic intra-cluster / mirror+verify cross-cluster);
  the **Hydrator**; **Weaver merkle convergence-verify**; the **atomic index flip**; the `410`/`CellMoved` drain;
  the idempotency-horizon prune. Re-validate atomic-batch-spans-two-buckets on the pinned NATS. Tests: live
  migration with concurrent writes → zero loss → C_dst == C_src → C_src drained → prune; coordinator-crash mid-
  dance → idempotent resume.
- **Fire 5 — cross-cell saga + multi-tenant mapping + cross-cell authz audit (behind Fire 4).** The
  Loom-orchestrated Tracker-vertex transfer + compensation (§3.5); the placement-policy tenant mapping (§3.6); a
  cross-cell Capability/D1 composition audit (§6.4). Tests: a cross-cell transfer with a mid-saga failure →
  compensation; a 1:1 tenant-isolation migration.
- **Fire 6 — production multi-cell topology + scale/chaos acceptance (the "turn it on" fire).** A multi-cell
  compose/k8s example; the add-cell + migrate ops runbooks; a **multi-cell convergence e2e** + a **cell-loss
  chaos test** + a **scale test crossing the single-bucket ceiling** as the NFR acceptance gate; the `/docs`
  touch-ups (§4).

## 8. Open questions — resolved

- **Cell granularity / migration mechanism?** → **Model C** (bucket; deployment hosts 1+ cells; atomic intra-
  cluster / saga cross-cluster), A-flavored default. **The fork — Andrew picks the default posture.** §3.1/§6.1.
- **Cell identity in keys?** → **Never** (NFR-SC2 locked); bucket name + `cell-index` only. §3.2/§4.
- **Where does routing live?** → the **Gateway** (Processor cell-unaware except the migration window). §3.3.
- **How does a vertex get placed?** → Gateway placement policy (tenant's cell / least-loaded), recorded in the
  index. §3.1/§3.7.
- **Cross-cell links?** → Bridge Links stored in both cells, identical cell-agnostic key, resolved via the index;
  **#109 dissolves** (nothing to rewrite). §3.4.
- **Cross-cell lens completeness (#110)?** → cell-local lenses + opaque remote refs + app-layer assembly; the
  watermark/vector-clock approach **rejected**. §3.4/§6.3.
- **Cross-cell atomic mutations (#121)?** → Loom **saga + compensation** via a Tracker vertex; anchor principle
  keeps it rare. §3.5.
- **Multi-tenant mapping (#124)?** → **N:1 default, 1:1 on demand, never 1:N.** §3.6.
- **Per-cell sub-laning (#114)?** → **dissolved** by cell = own `core-operations` stream + the shipped per-lane
  consumers. §3.3.
- **New contract?** → one §2.6 code (`CellMoved`), staged uncommitted; keys + commit semantics unchanged. §4.
- **Build now or shelve?** → ratify now; **Fire 1 buildable now** (de-risking); Fires 2–6 behind Gateway +
  HA-NATS + a real scale driver. §For-Andrew / §6.2 / §7.

---

### Recommended pre-build gate

For **Fire 4** (the migration dance — the one new-coordination, P2-plane, zero-data-loss increment), run a
**`bmad-party-mode` adversarial pass on the dual-write / index-flip / drain boundary** (the shadow-write
atomicity vs the step-8 OCC, the index-flip-as-commit-point, the stray-write `CellMoved` window) before building,
and record it as run — mirroring the pre-build passes the HA / D1 / per-lane designs self-flagged. The rest of
the surface (the addressing seam, the index/router, bridge links) is covered by the integration + multi-bucket
fixture tests.
