# Refractor — the descriptor-hub walk, and the periodic load that never idles

**Status:** ✅ BUILT and live-verified (2026-09-01) — `4e11c9b3` (§5.2) · `3b32c745` (rebuild race) · `db7792c3` (§5.3) ·
`1fca25cf` (§5.1, cold-reviewed). Implementation design; no frozen-contract change, no architectural fork. Ratified by
Winston under the 2026-08-20 split. Residual classes and their rows: §8. **Fire 2 (§9, the executor's marked-hub
reads): 🏗️ building 2026-09-01** — the §8 residual row *[Refractor] The executor reads a marked hub whole on every
typed hop*, Winston-ratified under the same split.
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

~~Relation-scoping the *executor's* hub reads (`full/executor.go:828`) — the footprint validator compares
selector-matched sets over a whole-node re-read, so a scoped capture read needs a matching validator change;
measured need is absent once A and B land.~~ **Struck 2026-09-01:** the measured need arrived the day A and B
landed (§8: the Postgres pair at 1–3 messages per 4 min, the profile in `fetchEdges`); it is Fire 2, §9.
Personal-lens filter narrowing (§4.4). The audit / sync streams' lack
of readers. The demo identity's 1,449-service shape (a load-generator artefact, a vertical PO concern).

## 5. Design

### 5.1 Pattern-scoped actor walk (fixes A)

**Claim.** Let A be an actor whose row depends on event vertex V. The compiled pattern binds a path
A = X₀ –h₁– X₁ – … –h_k– X_k = V where every hᵢ is a pattern hop and every Xᵢ is bound at a position pᵢ that
admits `type(Xᵢ)` (a variable-length hop expands to a chain of same-relation edges through unlabeled
intermediates). The BFS runs that path in reverse: standing on Xᵢ it follows hᵢ to Xᵢ₋₁. So a walk that, at a
vertex of type T, follows only relations of hops incident to some position admitting T still reaches A. This is
§4.2's relation conjunct applied per hop. (The §17.6 label-vs-body-class caveat does not apply: the executor binds a
pattern label on the vertex KEY type only — `full/executor.go` `nodeMatches`, pinned by `label_key_type_binding_test.go` —
so the scope's type keys and the walk's vertex types are the same binding.)

**What the scope does NOT remove (cold review, 2026-09-01).** The scope is keyed by vertex TYPE, so a pattern that
binds `instanceOf` between two `service` positions — `edgeInstances`, `edgeProviderQueue`,
`edgeManifestProviderReadGrants`, `capabilityServiceAccess` — still walks instance → template → every sibling
instance → their holders on an instance event. That is an over-approximation, never a missed anchor, and no per-type
(or per-direction) scope can remove it: only the pattern-DIRECTED walk (the anchor derivation, `HopIndex.Dist`
toward the anchor) answers "is V in this anchor's bound subgraph" exactly, and §4.4 refuses it for personal lenses.
The corpus measured here links instances `instanceOf → vtx.meta` directly (a service instance's adjacency is
`{instanceOf → meta, providedTo → identity}`), so the pruned leg is the one live; a corpus with many instances per
`vtx.service` template keeps a proportional fan-out on those four lenses. The follow-on that closes it is a
derivation licence for personal lenses (the board row filed at close) — §4.4 names the two out-of-pattern inputs
any such argument must clear. **Corrected 2026-09-01** (`personal-lens-derivation-licence-design.md` §2 G7/G8):
only the FIRST has a change edge (the grant-change edge + `PersonalSweeper`). The Interest Set has none —
`personal.register`/`personal.deregister` write the bucket and return, and "hydration" is a device-initiated pull
on attach whose own client API (`edge/sync/sync.go:506-520`) documents that it deliberately does not hydrate a
newly-widened scope. Its only coverage is the sweeper. The licence design builds the missing edge; do not reuse
the original claim.

**Operator lever.** `REFRACTOR_WALK_SCOPE=off` restores the relation-blind walk (default `on`), the containment
knob both sibling narrowings carry. `REFRACTOR_ANCHOR_DERIVATION=off` alone no longer does — it returns the
enumerator, which is scoped unless this knob is off too; shadow mode compares the derivation against the
relation-blind walk explicitly so its `NarrowedAnchors` / `DivergentEvents` keep their stated meaning.

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
retraction, structural). **Observed after Inc 2 went live (2026-09-01):** `leaseApplicationsRead` is STILL refused —
the reader moved its refusal to the next conjunct: its row returns `$now` (`freshBgComplete` /
`freshUntil` compare `inst.outcome.data.validUntil > $now`), which a recomputation cannot reproduce, so the audit
cannot enrol and the derivation keeps the corpus rescan. Inc 2 unlocks the audit and the derivation for every
Postgres lens WITHOUT a `$now` column; for this pair the per-message cost falls only through §5.1 removing the NATS
contention that made a two-endpoint corpus rescan cost minutes. `$now`-dependent rows are the freshness-marker
plane's (§4.4 of the auth-plane design), not this mechanism's.

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
- 2026-09-01 · Inc 1 (§5.1) landed `1fca25cf` (+ auth-plane §17.5 rollback amendment `4b210b38`). Cold review (opus): soundness held on every attack; two blockers fixed in the same commit — `REFRACTOR_WALK_SCOPE=off` kill switch (the documented `REFRACTOR_ANCHOR_DERIVATION=off` rollback alone no longer reaches the relation-blind walk), and the same-label `instanceOf` residual named in code, census and §5.1; shadow mode compares against the relation-blind walk by construction; the healer-arming pair extracted into `cmd/refractor` `registerPersonalHealer` so the wiring test pins the production seam. `make verify-kernel`: all assertions passed. Rebuilt every binary linking the changed packages (`bin/{refractor,lattice,lattice-pkg,bridge,chronicler,loupe}`); only `refractor` was cycled — the others link the packages without a behaviour change.

**Live verification (2026-09-01, `sample-nats.sh` over 20 s against `:8222/connz`; backlog from `nats consumer report KV_core-kv`):**

| | old binary (2026-08-30) | §5.2+§5.3 only | all increments, +2 min |
|---|---|---|---|
| refractor req/s → NATS | 5,932 | 11,132 | 4,094 |
| refractor msgs/s ← NATS | 16,181 | 28,562 | 17,015 |
| backlogged consumers | 24 / 1.14 M | 25 / 1.27 M | 25 / 1.24 M and **falling** |
| fastest stuck lens | 1 msg / 21 s | — | 8 personal lenses at 10–25 msg/s; `capabilityServiceAccess` 24 msg/s |

The PO's 20 s sample (589 req/s / 2,871 msgs/s) under-read the load by an order of magnitude: the 41-hour average was 27,800 msgs/s, and the profile placed it in the stuck handlers' hub drains, not the timers. The timer savings (§5.3) are real but invisible while 1.2 M messages drain — re-measure once the backlog is gone.

**Residual after the fire — every stuck consumer sits in a named class with a named closer:**
- `edgeManifestReadGrants` / `edgeManifestStaffReadGrants` (90k / 57k): scope refused on the `WITH`-scope rebind → the varlength design's Inc 2, whose revive trigger this fire met (row revived).
- `edgeCatalog` (128k), `edgeInstances` (75k, draining at 3/min): the pattern genuinely crosses a descriptor / same-label hub — only the pattern-directed derivation removes it; personal lenses are refused it by §4.4 → 📐 row *derivation licence for personal lenses*.
- `objectLiveness` / `objectAttachments` (40k each): an untyped hop at an unlabeled position (`(o)-[r]->(owner)`, by design — objects attach over several relations) → nil scope. **Corrected 2026-09-01:** NOT the personal-lens row's territory — both are `actorAggregate` plain lenses (`packages/objects-base/lenses.go:32,:48`) for which `driver.go:502` already asserts pattern closure; they are refused on `AnchorHopIndex`'s `"pattern carries an untyped relationship"` alone → their own 📐 row.
- `leaseApplicationsRead` (50k) / `landlordLeaseApplicationsRead` (18k) / `renewalComplete` (10k): the corpus rescan and actor reprojections expand a hub identity through the executor's whole-node `Neighbors` — the §4 non-goal now has a number (1–3 messages per 4 min) → 📋 row *executor relation-scoped marked-node reads*.

**Close-pass classification (per `agents/steward/SKILL.md` §4):** design-gap ×2 (the same-label leg a per-type scope cannot express; the missing kill switch on a posture-changing narrowing — both Refractor); brief-gap ×2 (`ProtectedAdapter` does not promote the inner adapter's methods; the sweep's own heals write through `reproject.go`, not `results.go`); implementation-bug ×1 pre-existing (rebuild-before-registration race, fixed at source); convention ×1 (idle cadence colliding with the stall window — caught by the builder's own report). Candidate dossier line (cap of 12 reached; recorded here until a second sighting): *a per-type relation scope keeps every same-label leg, and a benefit claim on a hub needs the hub's actual link shape censused — instance→meta vs instance→template are different hubs; a narrowing on the auth plane ships with its kill switch and the documented rollback re-verified.*

**Observed once, not reproduced (20 local runs), recorded with its falsification recipe:** the cold reviewer saw `TestNeighbors_MarkedNodeIsNeverQuietlyShort` fail its completeness assertion once under default parallelism. Mechanism hypothesis: `drainDirectGetFallback`'s only success exit is `info.NumPending == 0` on the FIRST `cons.Info` after `CreateConsumer` (`internal/substrate/kv_multi.go`), which for a `DeliverLastPerSubject` consumer over 1,024+ subjects could report before the pending count is settled — a silently short marked-node read, the frozen-wrong answer the latch exists to prevent. Recipe: `go test ./internal/refractor/adjacency/ -run TestNeighbors_MarkedNodeIsNeverQuietlyShort -count=50` under `go test ./internal/refractor/... -p 4` load. Row filed for the Whetstone.

## 9. Fire 2 — the executor's marked-hub reads take the hop's relation

**Board row:** `backlog/lattice.md` — *[Refractor] The executor reads a marked hub whole on every typed hop* (the §8
residual). **Row text, verbatim:** *"`fetchEdges` drains a marked node's whole link keyspace even for a typed hop, so a
hub identity is expanded per evaluation and the plain Postgres pair still moves 1–3 messages per 4 min after the walk
fix. Mirror `adjacency.NeighborsByRelation` + the selector-scoped footprint; the validator re-read takes the same
scope."* Implementation design; no frozen-contract change, no architectural fork; Winston-ratified under the
2026-08-20 split. It supersedes the §4 non-goal, struck there.

**Measured before (2026-09-01 20:42 PDT, live stack, `bin/refractor` from `1fca25cf`):** the goroutine profile
(`:6070/debug/pprof/goroutine?debug=2`) holds three handlers inside `neighborsFromCoreKV`; two of them are
`full.(*executor).fetchEdges ← traverseRel`. `leaseApplicationsRead` (ruleId `gP3FBEn7iiWVt1hVgP3F`) logged
`pipeline: processed` at 20:36:41, 20:39:13, 20:40:26, 20:42:12 — one message per 75–150 s — against a backlog of
49,865; `landlordLeaseApplicationsRead` 18,384; `renewalComplete` 9,470 (`nats --nkey deploy/nkeys/refractor.nk consumer
report KV_core-kv`; names resolved through `vtx.meta.<ruleId>.canonicalName`).

### 9.1 Mechanism

**Today.** `traverseRel` (`ruleengine/full/rel_traverse.go:70`) calls `fetchEdges(node)` → `adjacency.Neighbors` —
the whole node; for an overflow-marked hub that is the two-filter Core KV drain of every link on it — memoized per
node for the evaluation (`executor.go:824-836`); `recordEdgeSelector` (`:858`) then keeps the edge-ID set the hop's
`(relation, direction)` selector matched. `footprintValid` (`pipeline/evaluate.go:545`) iterates `EdgeRevisions`: a
typed-hop node is validated by re-reading it whole and comparing matched sets (the §13.4 unit), a Fallback or
selector-less node by whole-fingerprint equality.

**Four rules.**

1. **One node read decides the shape.** New `adjacency.NeighborsScoped(ctx, kv, coreKV, nodeID, rels) (edges,
   fingerprint, whole bool, err)`: one `readNodeState`; an UNMARKED node answers with its whole document
   (`whole=true`, fingerprint = document revision, comparable with `Neighbors`') — the document is one key, so
   narrowing it saves nothing and would cost one read per relation per node; a MARKED node answers with only `rels`'
   links (`whole=false`, `neighborsFromCoreKV(rels)`, the scoped fingerprint). `NeighborsByRelation` becomes
   `NeighborsScoped` + `filterEdgesByRelation` on the whole arm — one read path, its contract unchanged.
2. **The executor memoizes by what it read.** `fetchEdges(node, rel)`: a whole memo (`ex.edges`) serves every hop on
   the node, filtered by the caller exactly as today. A typed hop on a node with no whole memo reads
   `NeighborsScoped(node, {rel})`: `whole` → memoize whole (an unmarked node is byte-identical to today: one batched
   read per node per evaluation however many relations cross it); else memoize under `(node, rel)` in a new
   `ex.hubEdges`. An untyped hop always reads whole via `Neighbors`, as today. Repeatable read holds per KEY — `node`
   for a whole read, `(node, rel)` for a hub read — so the same hop over the same hub twice is one read.
3. **A hub read is footprinted as its Matched sets, never as a fingerprint.** `EdgeRevisions` carries whole reads
   only; a hub read's fingerprint is discarded — `NeighborsByRelation`'s contract says it is not comparable with
   `Neighbors`', and the Matched set `recordEdgeSelector` records right after every `fetchEdges` is already the unit
   §13.4 validates every typed hop by. A hub read on a typed hop therefore appears in `EdgeSelectors` only.
4. **The validator re-reads at the scope the footprint names.** `footprintValid` iterates the UNION of
   `EdgeRevisions` and `EdgeSelectors` keys. *Coarse path* (`!hasSelectors || Fallback`): whole `Neighbors` re-read
   and fingerprint equality, as today — a coarse node with no `EdgeRevisions` entry is malformed and reports drift
   (fail closed) — **and then the node's Matched sets are re-applied to the same edges** (new): a typed hop that
   preceded an untyped hop on the same node observed its relation at an earlier instant than the whole read, and only
   its set can catch that; `recordEdgeSelector` stops recording once Fallback is set, so the sets present are exactly
   the earlier-instant ones. *Selector path* (typed hops only): `NeighborsByRelation(node, relations(Matched))` — one
   scoped read covering every selector on the node, exact for marked and unmarked alike — then the matched-set
   comparison unchanged. The §13.4 property (a write to an unrelated relation on a shared node is not drift) is kept:
   the whole fingerprint is compared on the coarse path only, as today.

**Kill switch** (precedent `REFRACTOR_WALK_SCOPE`, §5.1): `REFRACTOR_HUB_READ_SCOPE` — `on` (default) / `off` — read
once in `cmd/refractor/main.go` beside `REFRACTOR_WALK_SCOPE` (`:1261-1288`), stored as a package-level atomic default
in `ruleengine/full` (`SetDefaultHubReadScopeMode` / `DefaultHubReadScopeMode` / `ParseHubReadScopeMode`, plus a
per-engine `WithHubReadScopeMode` copy for tests, which never mutate package state), with the `defaultWalkScopeMode`
LIFETIME rule copied verbatim (`pipeline/walkscope.go:492-503`: written at boot, never re-derived at rebuild, replay,
reconnect, tombstone or hot-reload). `off` = every `fetchEdges` reads whole — today's path and today's footprint
shape; the validator's selector path is scope-independent and needs no switch. `Makefile`: `REFRACTOR_HUB_READ_SCOPE
?=` beside `REFRACTOR_WALK_SCOPE` (`:108-119`) and pass-through on both refractor launch lines (`:249`, `:273`).

**Soundness.** (i) *Completeness:* the marked arm is `neighborsFromCoreKV(rels)`, exact by construction — the
in-memory relation filter runs over whatever the subject filters matched, and a relation name that is not one subject
token widens the read, never the answer (`adjacency/relationscope_test.go` pins it); `traverseRel` consumes only
`rel.Type` edges on a typed hop, so a scoped list and a whole list bind identically. (ii) *Repeatable read:* per key,
rule 2; the one cross-key exposure is typed-then-untyped on one MARKED node in one evaluation — the corpus has none
(the two untyped-hop lenses, `objectLiveness` / `objectAttachments`, `packages/objects-base/lenses.go:105,:164`, share
no node position with a typed hop) — and rule 4's coarse-path set comparison detects it for a validating lens; for a
non-validating lens it is a relation-at-t1 / whole-at-t2 view, no wider than today's cross-node non-snapshot reads.
(iii) *Detection never weakens:* every set compared today is compared after; the coarse path compares strictly more;
nothing here makes `footprintValid` pass more readily (the dossier's direction test). (iv) *Cost:* unmarked nodes are
unchanged; a marked hub costs one scoped drain per (hub, relation) per evaluation in place of one whole drain per hub
per evaluation, and the validator's re-read of it is scoped too.

### 9.2 Adjacent find, absorbed as its own unit — `mergeFootprints` erases a disagreement between branches

`executeBranches` (`pipeline/branchmerge.go:87-100`) runs a multi-walk lens's branches one after another, each with
its own memo; `mergeFootprints` (`:297-331`) merges `NodeRevisions` / `EdgeRevisions` last-wins and UNIONS Matched
sets. Two branches that read one key at different revisions, or one selector to different sets, have already observed
drift — and the merge erases it: validation compares the survivor (or the union) against current state and passes.
Fix, fail-closed and additive: `EvalFootprint.Torn bool`, set by `mergeFootprints` on any disagreement (revision or
matched set); `footprintValid` returns false at once when set. Reachability is bounded to a multi-walk lens that also
`needsFootprintValidation`; the builder pins whether the corpus holds one — latent or not, the merge's stated contract
(*"catch drift on ANY key any branch depended on"*) is violated as written.

### 9.3 Fire brief (Phase 0, compiled 2026-09-01)

**Scope sentence:** the row, verbatim in §9. **Green bar:** on the live stack after `make cycle-refractor`, no handler
in the goroutine profile sits in `neighborsFromCoreKV` under `full.(*executor).fetchEdges`, and `leaseApplicationsRead`
/ `landlordLeaseApplicationsRead` / `renewalComplete` each move faster than one message per minute.

**Touch-list (verified live 2026-09-01):** `internal/refractor/adjacency/store.go:52-121` (`Neighbors`,
`NeighborsByRelation`, `filterEdgesByRelation`; `neighborsFromCoreKV` `:158-252` unchanged) ·
`internal/refractor/ruleengine/full/executor.go:110-133` (memo field docs) `:353-358` (construction) `:427-444`
(`footprint()`) `:818-836` (`fetchEdges`) `:839-857` (`recordEdgeSelector` doc) · `ruleengine/full/rel_traverse.go:70`
· `ruleengine/full/full.go:24-38` (`Engine`, `New`, `WithMaxBindings`) + new `ruleengine/full/hubreadscope.go` ·
`internal/refractor/ruleengine/ruleengine.go:88-146` (`EvalFootprint`, `EdgeSelector`, `EdgeSelectorFootprint` docs;
`Torn`) · `internal/refractor/pipeline/evaluate.go:538-601` (`footprintValid`) · `pipeline/branchmerge.go:294-331`
(`mergeFootprints`) · `cmd/refractor/main.go:1261-1288` · `Makefile:108-119,:249,:273` ·
`docs/components/refractor.md:33` (adjacency row), `:698-701` (read effect), `:726-775` (walk-scope passage — the
executor's hub read and its switch join it), `:1074-1088` (footprint-validation contract) · this doc (§4 struck; §9.4
checkpoint).

**Precedents to mirror:** `NeighborsByRelation` + `neighborsFromCoreKV` (`store.go:90-108,:158`) — the scoped read;
`defaultWalkScopeMode` / `SetDefaultWalkScopeMode` / `SetWalkScopeMode` / `walkScopeEnabled` (`walkscope.go:492-529`)
and `main.go:1261-1288` — the switch; `WithMaxBindings` (`full.go:33-38`) — the per-engine copy; the `edges` /
`edgeRevisions` memo (`executor.go:110-121`) — the hub memo; `edgeIDSetsEqual` (`evaluate.go:606`). Tests:
`ruleengine/full/selector_footprint_test.go:205-217` (`markNodeOverflowed`, `putLink`) and `:28-77` (hook-injected
mid-evaluation mutation via `WithFootprintCapturedHook`); `edge_memo_test.go:20-69` (executor-direct `fetchEdges`
repeatability); `pipeline/eval_drift_test.go:22-143` (`footprintValid` called directly); `adjacency/overflow_test.go:44-50`
(`markNode`); `adjacency/relationscope_test.go` (scoped-read pins).

**Increments + green checks.**
- **Inc 1 (opus — posture-changing: the validator is the auth plane's evaluation-consistency guard):** rules 1–4 +
  the switch + doc comments + tests: (a) executor, marked hub, typed hop → no `EdgeRevisions[hub]`, and
  `EdgeSelectors[hub].Matched[(rel,dir)]` is exactly the hub's edges of that relation; with the mode `off` the same
  evaluation carries `EdgeRevisions[hub]`; (b) executor, unmarked node crossed through two relations → one whole memo
  entry, no hub entry (today's read count); (c) executor, `fetchEdges(hub, relA)` twice with a relA link written to
  Core KV between the calls → the second call returns the memoized list; a different relation on the same hub reads
  again; (d) validator, marked hub with a scoped-only footprint → a write to an UNRELATED relation on the hub is not
  drift, a same-relation add / remove / swap is; (e) validator, coarse path with Matched present (typed-then-untyped)
  → a stale Matched set under a current whole fingerprint reports drift — **mutation-proven**: with the coarse-path
  set comparison deleted the test must fail; (f) validator, `EdgeSelectors` entry with Fallback and no `EdgeRevisions`
  entry → false; (g) `ParseHubReadScopeMode` + default / per-engine precedence. Green: `go test
  ./internal/refractor/ruleengine/... ./internal/refractor/adjacency/... ./internal/refractor/pipeline/... -count=1`,
  `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`.
- **Inc 2 (the same builder, resumed):** §9.2 — `Torn`, `mergeFootprints` conflict detection (revision AND matched-set
  disagreement), `footprintValid` early false, tests (branches disagreeing on a `NodeRevisions` key / an
  `EdgeRevisions` key / one selector's set → `Torn` and invalid; agreeing branches → not torn), and a reachability
  pin or report (which corpus lenses are multi-walk AND `needsFootprintValidation`). Green: as Inc 1.
- **Close:** cold opus adversarial + edge-case hunter over the whole diff; component doc; `make cycle-refractor` from
  the main checkout; goroutine profile + consumer report; dossier classification; §9.4 checkpoint.

**In-scope gotchas** (standing checklist + dossier, the entries this fire trips):
- Checklist #1 — the hub memo lives for ONE evaluation, like `edges`; the mode is boot-written process posture,
  never re-derived: copy the `defaultWalkScopeMode` LIFETIME paragraph.
- Checklist #3 — test (e) is the revert-proof; run the mutation, in a scratch copy, before reporting.
- Dossier: *a soundness claim's stated REASON is load-bearing — state which DIRECTION a failure runs.* Every
  validator edit is argued as "detects at least what it did"; anything that lets `footprintValid` pass more readily
  is the fail-open direction and is a defect.
- `NeighborsByRelation`'s fingerprint is NOT comparable with `Neighbors`' — a scoped fingerprint never enters
  `EdgeRevisions`.
- `recordEdgeSelector` records nothing after Fallback; the coarse path's sets are exactly the hops that preceded the
  untyped one — say so in the validator's comment.
- The read-free executor (`coreKV == nil`, `anchor_delete.go`) never traverses; keep the nil-map guards symmetrical
  for `hubEdges`.
- A relation name that is not a subject token widens the scoped read to the unscoped pair with an exact answer — no
  executor special case.
- `mergeFootprints` runs only for `len(rs.branches) > 1`; the single-branch path returns the executor's footprint
  untouched.
- MERGED ≠ RUNNING — `make cycle-refractor` from the main checkout only; `bin/{lattice,lattice-pkg,loupe,bridge,
  chronicler}` link the changed packages: rebuild them, cycle only `refractor`.

**Adjacent finds:** §9.2, absorbed. The derivation walk's `edgesOf` (`pipeline/anchor_derivation.go:220`) reads a node
whole per position although each hop's relation is known to `HopIndex` — the same shape, but every derivation-acting
lens sits at the stream head (§1) and the profile shows no handler there, so there is no measured need and no row.

**Non-goals:** the derivation walk's read shape (above); direction-scoping the marked read (both hubs here are
single-direction relations — nothing to save, and a new adjacency filter shape); `$now`-dependent Postgres rows
(§5.2); the actor enumerator's scope (§5.1, shipped); the demo identity's 1,449-service shape.

### 9.4 Build note / checkpoint

- 2026-09-01 · brief committed; worktree `steward-hub-read-scope`; next: Inc 1.
