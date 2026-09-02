# Refractor — the descriptor-hub walk, and the periodic load that never idles

**Status:** ✅ Winston-ratified — build-ready (2026-09-01). Implementation design; no frozen-contract change, no
architectural fork. Ratified by Winston under the 2026-08-20 split; the auth-plane enumeration change (§5.1) is
posture-changing and gets a cold adversarial review at close.
**Board row:** `backlog/lattice.md` — *[Refractor] Two Postgres-target lenses made zero net progress for 3 days
while KV-target siblings drain* (the row names two lenses; the census below finds 24).
**Principal's ask (verbatim, clause by clause):** *"Take this item: [Refractor] Two Postgres-target lenses made
zero net progress for 3 days while KV-target siblings drain."* · *"Use this opportunity to review which actions
produce unreasonably heavy load on Refractor and see what we can do differently."* · The Vertical PO's
observation: *"It's Refractor's per-lens periodic timers, not a backlog draining … O(lenses × entities) of KV
re-reading on a graph where nothing is changing"* and *"28 lens consumers are frozen, not slow … survived a
full restart."*

## 1. What was measured (2026-09-01, live stack, `bin/refractor` built 2026-08-30 23:25, up 41 h)

Re-derive every number here before trusting it — the commands are inline.

| Fact | Value | How |
|---|---|---|
| Lens consumers on `KV_core-kv` | 139 (126 `refractor-<ruleId>` pipelines) | `nats consumer report KV_core-kv` |
| Consumers with `Ack Pending = 1` and 10k–128k unprocessed | **24** | same report; the `1` is the pump's single in-flight message |
| Consumers at the stream head (ack floor = `last_seq` 347,933) | the rest | same |
| Core KV writes during a 30 s window | 0 | `stream info KV_core-kv` twice |
| Per-message handler cost on the 24 | 20 s – 190 s | consecutive `pipeline: processed` timestamps per `ruleId` in `refractor.log` |
| In-flight handler goroutines | 24, all inside `adjacency.Neighbors` / `neighborsFromCoreKV` / KV reads under `evalAspectFanOut` or the plain arms | `:6070/debug/pprof/goroutine?debug=2` |
| `vtx.meta.pFf8PviwpWugC6kepFf8` (the `service` type descriptor) | **degree 3,913, overflow-marked** (`adjmark.` present) | `nats kv get refractor-adjacency adjmark.pFf8…` |
| `vtx.identity.edu97ixj2CJB6auNi6L4` | degree 2,969, overflow-marked; `ocZv1PtnocWiy37gcwbn` 1,449 inbound `providedTo` | same |
| A `service` instance's adjacency doc | exactly `{instanceOf → meta, providedTo → identity}` | `nats kv get refractor-adjacency adj.<serviceId>` |
| Lenses the anchor derivation **refuses statically** | 15 personal (`edge*`) — "row depends on inputs outside its compiled pattern"; 2 untyped (`objectLiveness`, `objectAttachments`); 3 varlength/`WITH` (`capabilityServiceAccess`, `edgeManifestReadGrants`, `edgeManifestStaffReadGrants`) | `grep "anchor derivation cannot act" refractor.log` |
| Derivation tally, `capabilityServiceAccess` | acted 133,691 · fell back 9 → **caught up, lag 0** | `grep "anchor-derivation tally"` |
| Derivation tally, `leaseApplicationComplete` / `renewalComplete` | fell back 936 / 2,317 | same |
| Plain-lens derivation, `leaseApplicationsRead` | refused: *"its target adapter cannot read a row back"* (no audit ⇒ no standing healer) | `grep "plain anchor derivation cannot act"` |
| Plain-lens derivation, `landlordLeaseApplicationsRead` | refused: target-diff retraction | same |
| Sweep tick (`DefaultSweepInterval` 60 s, ~41 lenses) | `ListKeysFilter("vtx.<type>.*")` + `adapter.ListKeysPrefix` **every tick**, to pick 25 candidates; no idle short-circuit | `pipeline/sweep.go:811-846` |
| LagPoller tick (`MetricsInterval` 5 s × 126) | 2 consumer-info + 1 publish + Health-KV read-modify-write (+ peak-rows RMW) per lens per tick ≈ 150 req/s | `health/lag_poller.go:149-213` |
| Rebuild watcher (`RebuildPollInterval` 500 ms × 3 live) | consumer-info every 500 ms for 41 h — the three rebuilds cannot drain at 1 msg/min | `pipeline/rebuild.go:500-540` |
| `REFRACTOR_AUDIT` / `SYNC` streams | 1.9 M / 530 k msgs at the 512 MiB cap, 0 consumers | `stream info` — by design (NFR6 / Edge sync), not this item |

**The 24 backlogged lenses are exactly the lenses whose events still run the relation-blind `ActorEnumerator`
BFS (derivation refused or fallen back) plus the two plain Postgres lenses whose derivation is refused.** Every
derivation-acting lens is at the stream head. The consumers are not frozen: each is mid-message, kept alive by
`keepAckAlive`, at minutes per message against a 10k–128k backlog — which is indistinguishable from frozen over a
40 s window and survives a restart because the cost is per message, not per consumer.

## 2. Root causes

**A — the actor walk crosses the type-descriptor hub.** `ActorEnumerator.Enumerate` (`pipeline/actor_enumerator.go:134`)
is an undirected, relation-blind BFS that expands every non-actor vertex. A `service` instance's only non-actor
edge is `instanceOf → vtx.meta.<serviceType>`, a hub with one edge per instance in the graph. One event on one
service therefore reads the hub (an overflow-marked node: a full Core KV link-keyspace drain through an ephemeral
consumer, twice), expands all 3,913 instances, reads each one's adjacency, finds every customer identity in the
system as an "affected actor", runs the hierarchy hop on each (another hub drain for the two hub identities),
and reprojects them all. The walk was written for `role → permission → identity` topologies; Contract #1's
`instanceOf` descriptor links make every instance of a type two hops from every other.

**B — the plain Postgres lenses re-scan the corpus per neighbour event.** `leaseApplicationsRead`'s plain-arm
derivation is refused because the Postgres adapter implements no `RowReader`, so the divergence audit cannot
enrol, so the derivation has no standing healer (`anchor_derivation_plain.go:327`, `audit.go:968`). Each of its
neighbour events (`service`, `identity`, `unit`, `augurproposal`, `object`) runs the unseeded whole-corpus
evaluation; that corpus contains a demo identity with 1,449 `providedTo` services, which the readiness
`OPTIONAL MATCH`es expand. `landlordLeaseApplicationsRead` is refused on target-diff retraction (a structural
§5 refusal; it inherits the same rescan).

**C — the periodic loops list the world on every tick and never notice the graph is idle.** The sweep's
`survey` re-lists both sides of its comparison every 60 s per lens to choose 25 anchors; the lag poller
rewrites an unchanged Health-KV entry every 5 s per lens; a rebuild watcher polls at 500 ms indefinitely. On a
quiet graph this is the floor the PO measured, and it is pure re-derivation of values that cannot have changed.

## 3. Alternatives

| Alternative | Verdict |
|---|---|
| **Delete the BFS** — derivation only | Rejected. The derivation refuses every personal lens by ratified design (auth-plane-projection-latency §4.4 names two out-of-pattern inputs) and falls back per event on shapes it cannot resolve; the BFS is the fallback that keeps those lenses correct. |
| **Delete the sweep / lag poller** | Rejected. The sweep is the standing healer §4.2's narrowing depends on; `lagProgressAt` is the stall detector the PO's finding was read from. They must idle, not vanish. |
| Raise the walk's depth / actor caps | Irrelevant — neither cap is reached; the cost is breadth at one hub. |
| Drop `instanceOf` edges from adjacency at build time | Rejected. `edgeProviderQueue` traverses `instanceOf` (service → template) and the executor shares the store; the descriptor hop is a legitimate edge, just never a pattern *path*. |
| Never expand `vtx.meta.*` in the walk | Sound today (no pattern passes *through* a meta position) but a per-type special case with a census-dependent argument; subsumed by the general rule below at the same cost. |
| Narrow personal lenses' consumer filter | Out of scope — §4.4 is a ratified refusal; not needed once the walk is pattern-scoped (an irrelevant event costs one parent read and an empty walk). |
| Rewrite the 24 lenses | Not applicable: the lenses are correct; the walk and the refused derivation are the defects. |
| **Pattern-scoped walk + scoped hub reads + Postgres `RowReader` + idle-aware loops** | **Chosen.** Each is substrate-native, fail-closed to today's behaviour, and independently revertible. |

## 4. Non-goals

Relation-scoping the *executor's* hub reads (`full/executor.go:828`) — the footprint validator compares
selector-matched sets over a whole-node re-read, so a scoped capture read needs a matching validator change;
measured need is absent once A and B land. Personal-lens filter narrowing (§4.4). The audit / sync streams' lack
of readers. The demo identity's 1,449-service shape (a load-generator artefact, a vertical PO concern).

## 5. Design

### 5.1 Pattern-scoped actor walk (fixes A)

**Claim.** Let A be an actor whose row depends on event vertex V. The compiled pattern binds a path
A = X₀ –h₁– X₁ – … –h_k– X_k = V where every hᵢ is a pattern hop and every Xᵢ is bound at a position pᵢ that
admits `type(Xᵢ)` (a variable-length hop expands to a chain of same-relation edges through unlabeled
intermediates). The BFS runs that path in reverse: standing on Xᵢ it follows hᵢ to Xᵢ₋₁. So a walk that, at a
vertex of type T, follows only relations of hops incident to some position admitting T still reaches A. This is
§4.2's relation conjunct applied per hop, and it carries §17.6's label-vs-body-class caveat unchanged (already
filed).

**Mechanism.** A `walkScope` published on `ruleState` next to `reprojectRelations` (`pipeline/ruleinstall.go:150-200,
:365-385`), derived from **every branch's** `AnchorHopIndex()` (not `rs.anchorHops`, which is single-walk only —
`ruleinstall.go:405-445`):

- for each hop `h` and each of its two positions `p`: if `Labels[p] == ""` or `p` is a `*` position with no
  resolved expansion → `anyType ∪= {h.Rel}`; else for each type the position admits → `byType[type] ∪= {h.Rel}`.
  An untyped hop (`h.Rel == ""`) makes the position's types wildcard (`allRels`).
- **Fail-closed to nil scope** (today's relation-blind walk) when: the engine is not full, any branch is not a
  `*full.CompiledRule`, any branch's index is `!Complete`, or `UnresolvedExpansionPosition() >= 0`.
- `enumerateAnchors` (`actor_enumerator.go:257`) hands the scope to `Enumerate`; in the BFS loop an edge is
  followed iff `scope == nil || scope.allows(cur.nodeType, edge.Name)`. The actor-stop rule, the depth and
  actor-set caps, the event-vertex self-add, and the explicit `reportsTo` hierarchy hop are unchanged.
- **Reads become relation-scoped where it pays:** `adjacency.NeighborsByRelation(ctx, kv, coreKV, nodeID, rels)`
  — unmarked node: the document, filtered; marked node: `GetMultiNoSnapshot` under `lnk.*.<id>.<rel>.>` +
  `lnk.*.*.<rel>.*.<id>` per relation (the same two-filter shape `neighborsFromCoreKV` uses, `adjacency/store.go:52-130`).
  The walk uses it whenever the scope at the current vertex is a finite set; the hierarchy hop uses it with
  `{reportsTo}` always (it never needed the whole hub).
- Health: the per-lens entry gains nothing new; the derivation tally line gains `walkScoped: bool`.

**Corpus census (standing rule, `docs/components/refractor.md` dossier):** `TestCorpusActorWalkScope` over
`forEachCorpusCypher`, pinning per actor-aware cypher (actorAggregate + personal) whether the scope is active and
a sorted digest of `byType`/`anyType`, with a population floor. Every lens the scope refuses names the refusing
conjunct. A `nil → scoped` move on a lens you edited is the direction that needs the §5.1 argument re-read.

### 5.2 Postgres `RowReader` (fixes B)

`PostgresAdapter.GetRow(ctx, keys)` mirroring `NatsKVAdapter.GetRow` (`adapter/natskv.go:592`): `SELECT` the
declared columns by the `IntoKey` columns; `ok=false` on no row; a guarded/protected adapter answers through the
same connection the writer uses (the writer role is not an RLS subject — verify in `adapter/rls.go` before
relying on it, and add the test if it is not already pinned). With a reader, `AuditPlan` enrols
(`audit.go:968` no longer refuses), the plain derivation's licence admits the lens, and a neighbour event
seeds the one affected application instead of the corpus. `landlordLeaseApplicationsRead` stays refused (diff
retraction, structural) — its cost drops only through the smaller graph reads of §5.1's siblings; record it.

### 5.3 Idle-aware periodic loops (fixes C)

- **Sweep survey cache** (`sweep.go:811`): `survey()` is skipped and the previous `(anchors, targets)` reused
  when the pipeline's applied sequence (`recordAppliedSeq`, `pipeline.go:547`) and a new per-pipeline
  `projectionWrites` counter (incremented at both adapter write sites in `results.go:74,88,237,246`) are both
  unchanged since the last survey — the anchor set can only move on a Core KV write this consumer has applied
  (the anchor label is always in a narrowed filter), the target set only through this pipeline's own writes.
  A full re-list still runs every 30th pass as insurance against an external target mutation.
- **Idle back-off:** when both counters are unchanged *and* the last complete cursor cycle healed nothing, the
  deep verify runs on every 10th tick; any change resets to every tick. Suppression (`sweep.go:423`) and the
  liveness clock semantics are untouched: a skipped idle tick records nothing, exactly like a suppressed one.
- **LagPoller:** `MetricsInterval` 5 s → 30 s; `SetProjectionProgress` skipped when every value it would write
  equals the last write; the metrics publish stays per tick. The Health-KV schema gains no field
  (`docs/observability/health-kv-schema.md` unchanged); `docs/components/refractor.md` rows for the sweep/lag
  cadence are updated in the same commit.
- **Rebuild watcher:** poll interval doubles from 500 ms up to 5 s while `outstanding` is unchanged, resets on a
  decrease. Tests that override `RebuildPollInterval` keep working (they set it below the floor).

## 6. Fire plan — one fire, four increments, each landing on `main` when green

| Inc | Content | Builder | Review |
|---|---|---|---|
| 1 | §5.1 walk scope + `NeighborsByRelation` + corpus census | opus (posture-changing) | cold opus adversarial |
| 2 | §5.2 Postgres `GetRow` + audit-enrolment test | sonnet | lead |
| 3 | §5.3 sweep cache + idle back-off + lag poller + rebuild back-off | sonnet | lead |
| close | cumulative adversarial pass over the whole diff; dossier classification | — | cold opus |

Landing shape: **each increment on `main` independently** — each is fail-closed to prior behaviour and reverts
alone. Gates per increment: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, every `scripts/lint-*.go`, `go test ./internal/refractor/... ./internal/substrate/...`,
plus the build-tagged harnesses the touched interfaces reach.

**Live verification (MERGED ≠ RUNNING):** `make cycle-refractor` from the main checkout, then: the goroutine
profile shows no handler inside `neighborsFromCoreKV` for a descriptor hub; `nats consumer report KV_core-kv`
shows the 24 backlogs falling at ≥ 1 msg/s each; the three `rebuilding` lenses reach `active`; NATS connection
rate for `refractor` (msgs/s in) drops by the sweep-listing share on an idle graph.

## 7. Fire brief (Phase 0, compiled 2026-09-01)

**Scope sentence:** the principal's ask, §0 above — the frozen-lens row, and the load review, in one fire.
**Touch-list (verified live):** `internal/refractor/pipeline/actor_enumerator.go:134-249,257` ·
`pipeline/ruleinstall.go:150-200,365-385,405-445` · `pipeline/rulestate.go:25-26,94-95,134-135` ·
`pipeline/anchor_derivation_shadow.go` (tally line) · `internal/refractor/adjacency/store.go:52-130` +
`overflow.go:148` · `adapter/postgres.go` (+ `adapter.go:102` `RowReader`) · `pipeline/audit.go:968` (no edit;
the gate it lifts) · `pipeline/sweep.go:441-470,811-846` · `pipeline/results.go:74,88,237,246` ·
`pipeline/pipeline.go:547` · `health/lag_poller.go:18,149-213` · `health/reporter.go:757` ·
`pipeline/rebuild.go:500-540` · `pipeline/pipeline.go:58` · `cmd/refractor/main.go:1789` ·
`internal/refractor/actor_onekey_corpus_census_test.go` (census precedent, `forEachCorpusCypher` at
`label_derivation_corpus_census_test.go:551`) · `docs/components/refractor.md:36,68,768,782,1137`.
**Precedents:** `linkRelationReactsTo` (`rulestate.go:228`) for the relation-set posture; `neighborsFromCoreKV`
for the two-filter marked read; `NatsKVAdapter.GetRow`; the `*_corpus_census_test.go` family.
**Increment order + green checks:** as §6; Inc 1 additionally `go test ./internal/refractor/ -run TestCorpus -count=1`.
**Gotchas (dossier + this grounding):** never derive a per-lens analysis from cypher text — run the real
`HopIndex` through `forEachCorpusCypher`; `rs.anchorHops` is single-walk — a multi-walk lens's scope must union
every branch or it is unsound; a marked node's `GetMultiNoSnapshot` returns soft-tombstoned links — filter
`isDeleted` as `neighborsFromCoreKV` does; the sweep's `record()` must not run for a skipped idle tick (it
would stamp a converged pass that verified nothing — the abandoned-pass rule at `sweep.go:493`); `MetricsInterval`
is overridden by `pipeline_test.go:1164` — keep it a var; a `keepAckAlive` message under the old binary stays
in flight across the cycle — expect one redelivery per backlogged lens.
**Adjacent finds:** none needing a row — the two zero-reader streams are by design; the demo identity's shape is
a load-generator artefact (noted for the Vertical PO in the commit).
**Non-goals:** §4.

## 8. Build note / checkpoint

- 2026-09-01 · Inc 2 (§5.2 Postgres `GetRow`) landed `4e11c9b3`. Brief gap: `ProtectedAdapter` wraps the inner adapter as a named field, so the reader had to be re-declared on the wrapper for `leaseApplicationsRead` to satisfy `RowReader` at all — the builder caught it; the corpus census verifies which lenses now enrol.
- 2026-09-01 · CI on the Inc 2 push failed in `TestNarrowedFilter_RebuildRecomputesLabelSet` (untouched by Inc 2): Run creates the durable server-side before the supervisor manages it, and a rebuild in that window is told "not managed". Fixed at the source `3b32c745` — the rebuild takes Pause/Resume's `awaitStarted` guard while the consumer is unmanaged — with a mutation-proven regression test.
- 2026-09-01 · Inc 3 (§5.3) landed `db7792c3`. Deviation from §5.3: the idle deep verify runs every 5th tick, not every 10th — the 10th-tick cadence collided with the heartbeater's 10-interval sweep-stall alert (an idle lens would have flapped `sweep-stalled` forever); `pipeline.IdleSweepBackoffEvery` is pinned at half `health.DefaultCapabilitySweepStallCycles` by a cross-package test. The projection-write counter covers every adapter write site, not only `results.go`'s (the sweep's own heals write through `reproject.go`).
- Inc 1 (§5.1) built with §4.2's standing-healer conjunct (sweep plan, or the personal plane's `PersonalSweeper` + grant-change edge); cold adversarial review in flight.
