# HA NATS clustering (substrate + compute availability) — design

**Status: ✅ Andrew-ratified (2026-06-29)** — fork = A (Processor active/passive, leader-elected); R3 + NATS-KV-CAS lease; no frozen-contract change; **whole build shelved behind the first production-availability driver (including Fire 1 — full-shelve, zero urgency)**. See the *Ratified* block. Author: Winston (Designer fire, 2026-06-29).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Scale-out → HA NATS clustering* (★ now / ★★ prod, M–L).
Grounds in `lattice-architecture.md` (**P2** sole-writer / serialization point, **P3** "NATS is sufficient…
two capabilities require early validation against expected scale", **P6**/NFR-SC2 cell-agnostic keys, the
KV Bucket Taxonomy §69, the quantitative targets §158–163), `docs/operations/deployment-isolation.md` (which
explicitly scopes this: *"Phase 1 uses a single NATS server rather than a cluster… High-availability NATS
clustering is a Phase 3+ concern"* — NFR-R6; and *"One NATS server **or NATS cluster** per cell"* — so HA is
multi-cell's per-cell availability prerequisite), the brainstorming inventory (sharding/cells stream 8,
cross-cutting/platform), the component docs that flag single-instance as a Phase-3 concern
(`weaver.md:178`, `loom.md:277` — *already* multi-instance-safe, `refractor` "system-wide liveness
bottleneck" §91), the substrate provisioning seam (`internal/bootstrap/primordial.go:80–214`,
`internal/substrate/stream.go`, `cmd/refractor/main.go:216`), and the NATS upstream
(`docs/vendors.md` pin **2.14**; JetStream clustering docs; **ADR-50 Atomic Batched Publishes**, confirmed
this fire to support replicated streams). A self-adversarial pass ran as part of this fire; its findings are
folded into §6–§8. **No frozen-contract change** (see §4).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Today a Lattice deployment is a **single NATS server with every stream and KV
bucket at Replicas=1** (`docker-compose.yml: nats:2.14-alpine -js`; no `Replicas` set anywhere — see
`primordial.go:96–204`), so losing that one node — or its disk — is a **total outage and a loss of the Core-KV
ledger itself**. This design makes a single deployment survive a node loss and a process crash on **two
planes**: (1) the **substrate** — a 3-node NATS cluster with **R3 replicas** on every stream/KV/object store
(quorum survives one node down); (2) the **compute tier** — a per-component HA posture (most components are
*already* HA-ready via durable consumers + bucket-coordinated state; the **sole-writer Processor** gets a
leader-elected active/standby posture that preserves P2 exactly).

**The one architectural fork (designed through — your call).** **The Processor's HA posture:**
**A — active/passive, leader-elected (RECOMMENDED)** vs **B — active/active (shared durables + OCC +
cross-instance DDL-cache coherence).** I recommend **A**: it preserves P2's serialization point and the
*synchronous* DDL-cache-invalidation-with-meta-commit invariant (`lattice-architecture.md:63`) **byte-for-byte**,
with failover bounded by a lease TTL (seconds). B buys write *throughput* we do not need — the locked target is
**10–100 ops/sec** (§158) and a single active Processor over-serves that by orders of magnitude — at the cost of
a genuinely new correctness surface (every instance must watch the meta-lane CDC to invalidate its DDL cache, and
"no concurrent DDL changes by design" becomes eventually-consistent across instances). Full trade-off in §6.1.
Secondary, non-fork knobs with recommendations: **R3 vs R5** (rec R3, parameterized), **leader-election
mechanism** (rec a NATS-KV-CAS lease — no new external dependency vs etcd/k8s; §3.3).

**Frozen-contract change: NONE.** Replica count and cluster topology are **deployment/substrate config** — the
same category the ratified NATS-account-write-restriction design placed NATS account/permission config in. The
**atomic-batch commit semantics (Contract #2 step-8, Contract #4 trackers) are unchanged** — **ADR-50**
explicitly supports replicated streams (and notes a replicated batch "can actually be faster"), so the Processor
step-8 commit and the Loom command-outbox batch work under R3 with identical semantics. The leader-election lease
is **operational state** (P1: single-component bookkeeping, outside Core KV). The only Health-KV addition (an
`role: active|standby` metric) is **§5.4 author-discretion** (the `lane_lag` precedent), not a Contract #5
change. There is **no uncommitted contract edit** to review for this design.

**Build sequencing (honest — this is a ratify-now / build-on-driver design).** NFR-R6 *explicitly accepts*
single-server for dev/portfolio, so HA has **no urgency for the current posture** and there is no pressure to
ship dead scaffolding. The recommendation is to **ratify the design and shelve the build behind the first
production-availability driver** — exactly as Vault / D1-Personal-Lens were ratified-but-build-deferred. The one
exception that *is* worth building now is **Fire 1** (the parameterized replica seam + a clustered embedded-NATS
test fixture): it is no-behavior-change at the R1 default, it *proves* R3 + atomic-batch-under-quorum works on
our pin, and it de-risks everything downstream — so it passes the dead-scaffolding test on its own (§7).

**Ratified (Andrew, 2026-06-29).** **Fork = A** (Processor active/passive, leader-elected) — OCC stays the
correctness guarantee, the lease is liveness only. **R3 default** (parameterized to R5); **NATS-KV-CAS lease**
(no new external dependency). **No frozen-contract change.** **Build: ALL fires shelved behind the first
production-availability driver — including Fire 1.** Andrew chose full-shelve over the de-risking Fire-1-now,
given NFR-R6 explicitly accepts single-server and there is **zero current urgency**. The design is ratified and
ready; **nothing builds until a prod-HA driver exists.** **Pre-build obligation (Designer-lane):** the
`bmad-party-mode` failover/split-brain pass (see the end of this doc) runs before **Fire 2** is build-ready —
discharged when the driver triggers the build, not now.

---

## 1. Problem + intent

**Today, a single node is a single point of total failure — and of data loss.** Every JetStream stream
(`core-operations`, `core-events`, `core-schedules`) and every KV bucket (`core-kv`, `health-kv`,
`capability-kv`, `weaver-state`, `loom-state`, `weaver-targets`, `refractor-adjacency`) plus the
`core-objects` Object Store is created with the JetStream default **Replicas=1** (`primordial.go` sets no
`Replicas` field; `docker-compose.yml` runs one `nats:2.14-alpine`). R1 means a single copy of the data: a node
crash stops the platform, and a **disk failure loses the Core-KV ledger** — the immutable record P2 is built to
produce. There is no redundancy, no failover, and no read availability during a restart.

The architecture has always scoped this as deferred-but-coming. **P3** flags that "NATS is sufficient for all
core plane needs" *but* names durable-consumer-count and atomic-batch-ceiling as the two scale dimensions to
validate. **NFR-R6** accepts single-server for dev/demo and names **"High-availability NATS clustering is a
Phase 3+ concern"** (`deployment-isolation.md`). And the multi-cell scale-out path in that same doc says each
cell is *"One NATS server **or NATS cluster** per cell"* — so **HA clustering is the per-cell availability
substrate that multi-cell (the XL sibling row) builds on**. This row is the resilience layer; multi-cell is the
horizontal-scale layer above it.

**Intent:** make a single Lattice deployment **tolerate one node loss with no data loss and bounded write
downtime**, without changing the data model (P6/NFR-SC2 keys are already topology-agnostic), without changing any
frozen contract, and by **reusing each component's existing HA-readiness** rather than reinventing coordination.

## 2. Reconciliation with the existing mental model (didn't we already…?)

- **"Isn't most of the compute tier already HA-safe?"** *Yes — and that is the design's biggest lever.*
  **Loom is already multi-instance-safe** by construction (`loom.md:277`: completion correlation is a direct
  `token.<token>` GET via the bucket, "no in-memory index, no startup rebuild barrier"; durable per-domain
  consumers resume from last ack). **Refractor**'s per-lens durable consumers (the shipped
  `substrate.ConsumerSupervisor`) load-balance across instances. **Bridge** de-dups on the claim-vertex +
  `idempotencyKey`. So for these, "HA" is *run N replicas pointed at the cluster* — **no code**. The design's job
  is to (a) make the **substrate** redundant and (b) handle the **one** component that is genuinely a
  single-writer — the Processor — plus the **one** residual singleton — Weaver's reconciler sweep.

- **"Isn't Weaver's singleton already being removed?"** *Yes.* Weaver's only non-HA-safe element is the
  reconciler sweep (`time.Ticker`, `weaver.md:178` "single-instance; multi-instance fan-out is a Phase 3
  concern"). The **ratified `op-vertex pruner + @every` recurring-schedules design** moves that sweep to a
  durable `@every` schedule (single-fire-across-replicas). So **Weaver HA composes on an already-ratified
  feature** — this design depends on it rather than re-solving it, and offers an active/passive interim for the
  window before `@every` lands.

- **"Does this duplicate multi-cell?"** *No.* Multi-cell is **horizontal scale** (route operations across
  isolated cells; the Gateway resolves topology). HA clustering is **vertical resilience within one
  cell/deployment** (replicate the one cell's data across nodes). They compose: a production multi-cell
  deployment runs *each cell* as an HA cluster. This row is the prerequisite, not a competitor.

- **"Does it introduce new state — and do we already keep that state somewhere?"** One new operational
  primitive: a **leader-election lease** (`leader.<role>` key with TTL + revision-CAS renew). This is **P1
  operational state** (single-component bookkeeping, outside Core KV), and it is the *same shape* as state the
  platform already keeps — Weaver's mark-lease (`ClaimedAt` + TTL in weaver-state) and Loom's `deadline.<id>`
  TTL. No new Core-KV vertex/aspect/link; no read-model; P5/P2 untouched.

- **"Does the commit path even survive replication?"** *Yes, verified this fire against upstream.* The Processor
  step-8 commit is a NATS **atomic batch** (`AllowAtomicPublish` on `KV_core-kv` + `KV_loom-state`,
  `primordial.go:120`). **ADR-50 (Atomic Batched Publishes)** documents support for **replicated and
  non-replicated streams**, with per-message consistency checks committed under the stream's Raft group — and
  Synadia notes a replicated batch "can actually be faster." So R3 changes the *durability/latency* of the
  commit, not its *semantics*.

## 3. The shape

Two planes. Plane 1 makes the data redundant; Plane 2 makes the compute redundant.

### 3.1 Plane 1 — NATS substrate HA (the foundation)

- **Topology:** a **3-node NATS cluster** — each server with a unique `server_name`, a `cluster {}` route mesh,
  and JetStream enabled. JetStream forms three tiers of Raft groups (meta group for the JS API + placement; one
  group per stream; one per consumer). Quorum = ⌊N/2⌋+1, so **R3 survives one node down**; during a stream
  leader election there is a brief write-pause ("no leader → the stream will not accept messages"), bounded to
  sub-second–seconds — the **RTO** for writes. Clients connect with the **full server list**
  (`nats.Servers([...])` / `NATS_URL` as a comma list) so a node loss is a transparent client reconnect, not an
  outage.

- **Replicas everywhere it matters:** set `Replicas: <N>` on **every** provisioned asset:
  - Streams: `core-operations`, `core-events`, `core-schedules` (`provisionStreams`, `primordial.go:171`) +
    the substrate-blessed `EnsureStream` (`stream.go:36` — Refractor DLQ/audit streams).
  - KV buckets: all seven in the `primordial.go:80` loop (`KeyValueConfig.Replicas`) + the Refractor
    target-bucket create (`cmd/refractor/main.go:216`).
  - Object Store: `core-objects` (`ObjectStoreConfig.Replicas`, `primordial.go:131`).
  - The KV→stream coupling holds: a KV bucket's replicas propagate to its backing `KV_<bucket>` stream, and
    `enableAtomicPublish`'s `UpdateStream` (`primordial.go:150`) preserves the replica count it reads back.

- **Parameterized, default-1, fail-safe at small clusters:** the replica count is **deployment config**
  (`NATS_REPLICAS`, default **1**). Default-1 keeps the **embedded test harness, CI, and `make up`
  byte-identical** (a single-node server cannot host R3 — `CreateOrUpdate` with `Replicas>nodes` errors). A
  startup guard clamps `Replicas` to the live server count when fewer (so a degraded 2-node cluster still boots),
  logging the downgrade — fail-*safe*, never fail-*silent-at-R1-in-prod* (a prod profile asserts `Replicas≥3`).

### 3.2 Plane 2 — Lattice compute HA (per-component posture)

| Component | Today | HA posture | Why / mechanism |
|---|---|---|---|
| **Processor** (sole Core-KV writer, P2) | single instance, per-lane durables (Fire 2 shipped) | **active/passive, leader-elected** | Only the lease-holder pumps the per-lane consumers + owns the synchronous DDL-cache. Standby(s) hot; acquire the lease on expiry → resume pumping. Preserves P2 serialization + synchronous DDL-cache invalidation exactly. (§6.1 fork.) |
| **Loom** | already multi-instance-safe (`loom.md:277`) | **active/active** | Run N replicas; durable per-domain consumers load-balance; token/cursor state in `loom-state` (R3). **No code change.** |
| **Weaver** | single-instance (sweep = `time.Ticker`) | **active/active** once the sweep is `@every` (ratified); **active/passive** interim | CDC convergence consumer is durable (load-balances); dispatch idempotency via `claimId` + mark-lease (`weaver-state`, R3). The lone singleton (the sweep) is removed by the ratified `@every` design — compose on it. Interim: hold the sweep behind the same lease as the Processor. |
| **Refractor** | per-lens durable consumers (`ConsumerSupervisor`) | **active/active** | Durable consumers per lens load-balance across instances; resume from last ack. The **system-wide liveness bottleneck** (§91) — HA matters most here, and it is already structurally ready. **No code change** beyond the replica seam. Postgres-target HA is a *separate* integration-dependency posture (out of scope; §54 "integration dependencies… each needs its own availability posture"). |
| **Bridge** | single instance | **active/active** | Claim-vertex + `idempotencyKey` de-dup (FR58). **No code change.** |
| **object-store-manager** | single GC sweeper | **active/passive / singleton** | Epoch-CAS GC is correctness-safe but contention-wasteful with N sweepers — hold it behind the lease (one active GC), or leave it a deliberate singleton (a stalled GC degrades, never corrupts — Loop A+B converge on next run). |
| **Loupe / vertical apps** | single instance | **active/active** (stateless) | P5 read-only over lens targets / Core-KV inspector; run behind a load balancer pointed at the server list. **No code change.** |

The pattern: **only the Processor needs new coordination code.** Everything else is either already HA-safe
(durable consumers / bucket-coordinated idempotency) or rides the same lease the Processor introduces.

### 3.3 The one new primitive — a substrate leader-election lease

A small, tested `substrate` primitive (the only genuinely new code on the critical path):

- **Shape:** a KV-CAS lease — key `leader.<role>` (e.g. `leader.processor`) on a small operational KV bucket
  (`platform-leases`, R3, per-key TTL). The active instance writes its instance-id with a TTL and **renews via
  revision-CAS** at ⅓-TTL cadence; standby instances **watch** the key and **CAS-acquire** when it expires or is
  released. Lease loss (renew CAS fails / TTL lapses) → the holder **stops pumping immediately** (fail-closed:
  a partitioned ex-leader that can't renew must not keep writing). This is **operational state** (P1), the
  **same TTL+CAS family** the platform already uses (Weaver `ClaimedAt`, Loom `deadline.<id>`).
- **Why NATS-KV-CAS, not etcd/k8s-lease:** **no new external dependency** — the lease lives on the substrate
  the platform already runs (and now replicates). etcd/Consul adds an operational dependency; a k8s `Lease`
  object couples leader-election to one orchestrator. A NATS-KV lease works identically in compose, k8s, or bare
  metal. (Rejected alternatives in §6.2.)
- **Consumers:** Processor (mandatory), object-store-manager, Weaver-interim. The active/active components do
  **not** touch it.
- **Split-brain safety:** the lease alone is *liveness*, not *safety* — the **real** anti-double-write guard
  stays the step-8 **OCC** (`expectedRevision`). Even if two Processors briefly believe they hold the lease (a
  pathological renew race), every commit is revision-conditioned, so one wins and the other's batch is rejected
  and redelivered — **no double-commit is structurally possible** regardless of the lease. The lease optimizes
  for *one* active writer (no wasted contention); OCC *guarantees* correctness. This belt-and-braces is the
  reason A is safe (§6.1).

## 4. Contract surface

**No frozen-contract change.** Itemized:

- **Replica count + cluster topology** = deployment/substrate config (env + `nats-server.conf` / compose / k8s).
  Same category the ratified `nats-account-write-restriction-design.md` used ("NATS account/permission config is
  deployment/substrate; no frozen-contract change"). Nothing in `docs/contracts/*` prescribes a replica count.
- **Atomic batch (Contract #2 step-8 commit; Contract #4 §4.3 trackers):** semantics **unchanged** under R3
  (ADR-50 supports replicated streams). The contracts describe *behavior*, which is preserved; R3 changes only
  durability/latency. No edit.
- **Per-key TTL (Contract #4 §4.3 / ADR-48), `@at`/`@every` schedules (Contract #10 §10.4 / ADR-51):** stream
  features that replicate with the stream — no contract impact.
- **Health KV (Contract #5):** active/passive components add an author-discretion `role: active|standby` +
  `leaderSince` to their §5.4 metrics — the documented author-discretion channel (`lane_lag` precedent). **No
  §5 change.**
- **Doc touch-ups the Steward applies at build (docs, not contracts):** `deployment-isolation.md` (the
  "Phase 3+ concern" / "Verification Path (future)" sections flip to designed/shipped + gain the cluster
  topology); `lattice-architecture.md` NFR-R6 status; a short "HA posture" note in each `docs/components/*.md`.
  These are `/docs` edits, staged by the Steward in the build fires — **not** part of this design's commit.

There is therefore **no uncommitted contract edit** accompanying this design — ratification is a pure design
sign-off.

## 5. Migration / compatibility

- **Backward-compatible by default.** `NATS_REPLICAS=1` reproduces today exactly; the embedded harness and CI
  stay single-node and untouched. HA is opt-in.
- **R1 → R3 on a live deployment** is an **ops runbook, not a data migration** (keys are topology-agnostic,
  P6/NFR-SC2): (1) stand up a 3-node cluster (rolling-add nodes to the existing server, JetStream meta-group
  forms); (2) `UpdateStream`/`UpdateKeyValue` to `Replicas: 3` per asset (NATS scales replicas in place once
  ≥3 peers exist); (3) roll the compute tier — bring up standby Processor(s) + extra Loom/Refractor/Weaver
  replicas pointed at the server list. No key rewrite, no dual-write, no downtime beyond per-asset
  re-replication catch-up.
- **Rollback** is symmetric (`Replicas: 1`, drop to one node) — trivial because the data model never changed.

## 6. Risks + alternatives

### 6.1 The fork — Processor HA posture

- **A. Active/passive, leader-elected (RECOMMENDED).** One active instance; standby(s) hot. *Pros:* preserves
  P2's serialization point and the **synchronous** DDL-cache-invalidation-with-meta-commit invariant
  (`lattice-architecture.md:63` — "Processor cache invalidation is synchronous with meta-lane commit. No
  concurrent DDL changes by design") **with zero change**; OCC already guarantees no double-commit even during a
  failover race (§3.3); failover RTO = lease TTL (seconds). *Cons:* one idle standby; a bounded write-pause on
  failover. *Quantified:* at the locked **10–100 ops/sec** write target (§158), a *single* active Processor is
  already orders of magnitude over-provisioned — there is **no throughput case** for active/active.
- **B. Active/active (shared durables + OCC + cross-instance DDL-cache coherence).** Multiple Processors bind
  the same per-lane durables; JetStream load-balances; OCC resolves write-write races. *Rejected.* It buys
  throughput we provably don't need, at the cost of a **new correctness surface**: each instance must **watch
  the meta-lane CDC to invalidate its own DDL cache**, so "no concurrent DDL changes by design" weakens to
  *eventually-consistent-across-instances* — a non-leader could briefly auth/validate an op against a stale DDL
  in the window between a meta-commit and its CDC propagation. *Re-ask "could a variant of B beat A?"* — a
  hybrid (active/active for default/system/urgent lanes, single-active for `meta`) still leaves the **non-meta
  lanes reading the DDL cache**, so cross-instance cache coherence is still required: no net simplification. **A
  wins decisively** — same correctness, far less machinery, ample headroom.

### 6.2 Secondary decisions (recommendations, not forks)

- **R3 vs R5.** R3 (tolerates 1 loss) is right for the single-operator demand profile (§158–163, ~500 members);
  R5 (tolerates 2) is an ops knob for larger fleets. **Parameterize `NATS_REPLICAS`; default 3 in the HA
  profile**, 1 in dev.
- **Lease mechanism.** NATS-KV-CAS (rec, §3.3) vs etcd/Consul (new external dep) vs k8s `Lease` (orchestrator
  lock-in). NATS-KV-CAS reuses the substrate + the platform's existing TTL+CAS idiom.
- **Embedded-cluster test fixture.** Use the `nats-server` test helpers' clustered embedded mode (the same
  `jsstore.Dir(t)` discipline that keeps parallel CI fixtures isolated — `project_ci_test_parallelism`) so R3 is
  exercisable **without Docker**, in `go test`.

### 6.3 Risks

- **Commit latency under quorum.** R3 adds a quorum round-trip to each step-8 commit. ADR-50 says a *replicated
  batch can be faster* (pipelined), but the **Stream-0 atomic-batch-ceiling spike** (§146, §210) must be
  **re-run under R3** to confirm the NFR-P3 ≤500ms end-to-end budget holds. *Mitigation:* a Fire-1 integration
  benchmark; the write target is tiny, so headroom is expected.
- **Durable-consumer count under clustering (P3).** Each consumer is its own Raft group; at hundreds of lenses
  this is the second scale dimension P3 named. *Mitigation:* validate in the clustered fixture; orthogonal to
  this design (it bounds *scale*, not *availability*) but surfaced here because clustering is when it bites.
- **Refractor = system-wide liveness bottleneck (§91).** HA helps (load-balanced durable consumers survive a
  node), but a poison-lens still head-of-line-blocks its own consumer — HA is not a substitute for the existing
  4-tier failure classification / DLQ. *Stated, not solved here.*
- **Object-store-manager singleton.** A held-lease GC is the conservative choice; a stalled GC degrades (blobs
  linger) but never corrupts (epoch-CAS) — acceptable, documented.

### 6.4 Self-adversarial pass (folded in)

- *"The lease is a new SPOF."* — No: the lease lives on R3 KV (survives a node); and OCC, not the lease, is the
  correctness guarantee (§3.3). A lease-service hiccup costs *liveness* (a failover blip), never *safety*.
- *"Default-1 in prod is a silent footgun"* — addressed by the prod-profile `Replicas≥3` assertion + the
  clamp-with-loud-log (§3.1); fail-safe, never fail-silent.
- *"Active/active Loom/Refractor could double-process."* — No: durable consumers deliver each message once
  across the consumer group; idempotency (token-pointer / projection upsert) covers redelivery. This is the
  *existing* guarantee, merely now spanning instances.
- *"R1→R3 needs a data migration."* — No (P6/NFR-SC2): keys are topology-agnostic; `UpdateStream` scales
  replicas in place.

## 7. Decomposition for the Steward (fire-by-fire)

Each fire is independently shippable + green. **Honest sequencing (ratified, Andrew 2026-06-29): ALL fires —
including Fire 1 — are shelved behind the first production-availability driver** (Andrew chose full-shelve given
zero urgency / NFR-R6 accepts single-server). The design is ratified and ready; Fire 1 (the no-behavior-change
replica seam + clustered fixture) is the **first fire to build when the driver arrives**, not a build-now
de-risk. (The original "Fire 1 buildable now" recommendation is retained below as the design's reasoning, but the
ratified decision is full-shelve.)

- **Fire 1 — substrate replica seam + clustered test fixture (buildable now; de-risking; full review).**
  Thread `NATS_REPLICAS` (default 1) into every stream/KV/object creation (`primordial.go`, `stream.go`,
  `cmd/refractor/main.go:216`); add the live-server-count clamp + prod-profile assertion. Add a **clustered
  embedded-NATS test fixture** and integration tests: R3 round-trip, **atomic-batch-under-R3**, a node-kill →
  quorum-survives → writes-resume, and the re-run of the Stream-0 batch-ceiling spike under R3. *No behavior
  change at the R1 default; not dead scaffolding (the fixture + seam are exercised + reusable).*
- **Fire 2 — the leader-election lease + Processor active/passive (ships together; full 3-layer, P2 plane).**
  `substrate.Lease` (acquire/renew/CAS/expire/release, fail-closed on renew-loss) **plus** its first consumer:
  the Processor pumps the per-lane durables only while holding `leader.processor`; standby hot-takeover; the
  `role`/`leaderSince` Health-KV metric. Ships the primitive **with** its consumer — no inert primitive. Tests:
  lease failover (active dies → standby pumps, **no double-commit** asserted via OCC), partition (ex-leader
  stops on renew-loss).
- **Fire 3 — the remaining singletons on the lease (thorough-lead + concurrency pass).**
  object-store-manager GC behind `leader.objstore`; Weaver-interim sweep behind `leader.weaver` (retired once
  `@every` lands — depend on the ratified recurring-schedules design). The active/active components
  (Loom/Refractor/Bridge/Loupe) need **no code** — covered by Fire 4's topology + docs.
- **Fire 4 — the production HA topology + acceptance (the "turn it on" fire; chaos e2e).**
  A 3-node `docker-compose.ha.yml` / k8s example; the R1→R3 ops runbook (`docs/operations/`); the clustered
  convergence e2e + a **node-kill chaos test** as the **NFR-R6 acceptance gate**; the `/docs` touch-ups (§4).

## 8. Open questions — resolved

- **Cluster size?** → R3 default (parameterized to R5). §6.2.
- **Lease mechanism?** → NATS-KV-CAS, no external dependency. §3.3 / §6.2.
- **Processor active/active or active/passive?** → active/passive (the fork; A). §6.1. *(Andrew's ratification
  call — recommendation made, designed through.)*
- **Does the commit path survive R3?** → yes, ADR-50; re-validate latency in Fire 1. §2 / §6.3.
- **Weaver singleton?** → composes on the ratified `@every` design; active/passive interim. §2 / §3.2.
- **New contract?** → none. §4.
- **Build now or shelve?** → ratify now; Fire 1 buildable now (de-risking); Fires 2–4 behind the first prod-HA
  driver. §For-Andrew / §7.

---

### Recommended pre-build gate

For Fire 2 (the lease + Processor failover — the one new-coordination, P2-plane increment), run a
**`bmad-party-mode` adversarial pass on the failover/split-brain boundary** (the lease-renew race vs the
step-8 OCC guarantee) before building, and record it as run — mirroring the pre-build passes the D1 / per-lane /
FR28 designs self-flagged. The rest of the surface (replica config, active/active run-N-replicas) is
mechanical and covered by the integration/chaos tests.
