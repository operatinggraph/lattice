# Refractor footprint reduction — kill the amplifiers

**Status: ✅ SHIPPED (all 8 fires on `main`, CI green, 2026-08-01)** — Andrew ratified and
directed the campaign in-session; §7 records the post-ship measurement. The one open build
item is §D2 **Phase 2** (reverse anchor enumeration), whose named trigger — neighbor-event
full recomputes dominating a backlog drain — was measured live at the closing restart (§7).

**For Andrew:** Refractor's memory/CPU footprint — and the substrate load it induces — is
almost entirely *amplification*, not data: an 11 MB graph producing 6 GB of JetStream exhaust
and saturating NATS during boot/rebuild. Five measured amplifiers, eight fires, no contract
changes.

---

## 1. Measured baseline (dev stack, 2026-08-01)

| Metric | Value |
|---|---|
| `KV_core-kv` (the entire graph) | 16,372 keys, **11.2 MB** |
| Refractor RSS | **540 MB** (siblings: ~10 MB) |
| NATS server | **5.75 GB RAM**, 104% CPU sustained / ~1400% burst, 84 slow consumers |
| JetStream | 142 streams, consumers **236 settling / 698 storm peak** |
| `AUDIT_*` streams | **95 streams, 20.4 M entries, 5.6 GB** (93% of store) in a 7-day window ≈ **33 projection writes/s sustained** |
| Restart incident (04:57) | ~50 lenses lagging, `core-kv context deadline exceeded`, host 10.4/12 GB swap |

95 lenses installed. Steady-state consumer floor ≈ 205 durables; the tide above it is
Refractor-generated ephemerals (per-pipeline adjacency watchers + watch-based `ListKeys`
listers — each in-flight full-bucket listing is a live server-side consumer).

## 2. The five amplifiers

1. **Unanchored evaluation = full-graph rescan.** Most business lenses are unanchored
   (`MATCH (l:leaseapp)` — no `{key: $actorKey}`). The generic seed path
   (`ruleengine/full/executor.go` `seedNodes`) lists the **whole bucket** and point-reads
   **every vertex of every type** to filter by label, memoized per-evaluation only.
   `ListKeysPrefix` already exists in substrate and is unused here.
2. **Vertex events are ungated.** `plainReactsTo` (referenced-label skip) gates only the
   aspect and link arms. A vertex-root event of *any* type triggers a full evaluation on
   *every* plain lens (`pipeline.go` `handle`, KindVertex branch).
3. **Every lens is a whole-stream durable.** Each pipeline consumes `$KV.core-kv.>`
   (`cmd/refractor/main.go` `startPipeline`); the server fans every Core KV write out ~97×,
   and boot/rebuild replays ~16k `DeliverLastPerSubject` events per lens.
4. **Writes are unconditional and every write is audited.** Unguarded `Upsert` is a blind
   `Put` (`adapter/natskv.go`); a full row set is rewritten per trigger; each write appends
   to a private per-lens 7-day `AUDIT_<ruleID>` stream. The 20.4 M entries are this, measured.
5. **The per-pipeline adjacency watch is dead machinery.** Every pipeline watches the whole
   `refractor-adjacency` bucket; the handler strips `adj.` and Gets Core KV with a **bare
   NanoID**, which has never been a Core KV key shape (Andrew confirms: not even in
   Materializer) — the re-evaluate arm is unreachable, so it's 95 standing consumers plus
   2×95 no-op Gets per link mutation. Both link arms already pre-apply the edge to adjacency
   before evaluating (`evaluate.go` fan-out; `pipeline.go` plain arm), so the ADR-16 ordering
   race the watch was meant to heal is closed at the source. Pure deletion.

## 3. Fires

| Fire | Scope | Size | State |
|---|---|---|---|
| **1 — Delete the dead adjacency watch** | Remove `runAdjWatch`/`drainAdjWatch`/`handleAdjUpdate`/`handleAdjNode`, the `runAdjWatch` launch, `SkipsAdjWatchWrite` + guarded-skip arms, `adj_watch_internal_test.go`; ADR-16 supersession note in `docs/components/refractor.md` + bootstrapper comments; fix the stale "until the adjacency watch heals it" comment. Keep bucket + bootstrapper + `Neighbors` (the executor's index). | S | ✅ shipped |
| **2 — Label-prefix seed scan** | `seedNodes` generic path uses `ListKeysPrefix("vtx.<label>.")` when the pattern has a label (keep the KindVertex shape filter — the prefix also matches aspect keys); unlabeled patterns keep the full scan. | S | ✅ shipped |
| **3 — Relevance-gate plain vertex events** | In `handle`'s KindVertex branch: plain lenses (`actorEnumerator == nil`) ack-and-skip when `!plainReactsTo(label)`. Anchor-label tombstones still pass by construction; non-exhaustive label sets keep reproject-all; actor-aware pipelines untouched. | S | ✅ shipped |
| **4 — Skip-if-identical unguarded writes** | `NatsKVAdapter.Upsert` unguarded path: Get + byte-compare, skip identical Puts (the sweep's precedent, made universal). Audit skips with the write. Guarded/personal paths untouched. Postgres `IS DISTINCT FROM` optional follow-up. | S–M | ✅ shipped |
| **5 — One audit stream** | Single `REFRACTOR_AUDIT` stream over `lattice.refractor.audit.>` with MaxAge 7d + MaxBytes ceiling; per-process ensure; stop creating `AUDIT_<ruleID>`; delete the 95 existing (dev history, Andrew-approved direction). | S–M | ✅ shipped |
| **6 — Reap durable on lens delete** | Lens tombstone currently strands `refractor-<ruleID>` durables forever (lifecycle step 9); delete the durable when the lens is removed. | S | ✅ shipped |
| **D1 — Server-side filter subjects** | Per-lens durable `FilterSubjects` derived from referenced labels (`$KV.core-kv.vtx.<label>.>`, `lnk.<label>.>`, `lnk.*.*.*.<label>.>`); broad filter kept for non-exhaustive label sets. **Vendor gate CLEARED against the pinned `nats-server v2.14.0` source:** last-per-subject validation admits plural `FilterSubjects` (`server/consumer.go:912-920`), pending is per-filter (`NumPendingMulti`, `:5534-5538`), and filters are editable on a live consumer (`:2577+`) — with the caveat that an update never resets the cursor, so a **widened** label set must ride `Pipeline.Rebuild` (consumer reset); narrowing needs nothing. Overlapping filters on one consumer are rejected — dedupe labels. Needs `substrate.ConsumerSpec.FilterSubjects []string`. | M | ✅ shipped |
| **D2 — Event-seeded delta evaluation** | §D2 below. Phase 1: anchor-labeled events recompute one anchor. Phase 2 (measured need only): reverse anchor enumeration for neighbor events. | M (P1) | ✅ Phase 1 shipped · Phase 2 trigger MEASURED (§7) |

Deliberately **not** in scope: a single shared demux consumer replacing per-lens durables
(the per-lens ack floor *is* the per-lens resume/rebuild mechanism — load-bearing); any
lens-authoring convention change (D2 seeds in the pipeline, not in package cypher); the
leaked `edge-sync-*` durables (filed separately on the board — `cmd/facet`, not Refractor).

## 4. Sequencing & verification

Wave 1: Fires 1 + 2 (disjoint files) → Wave 2: Fires 3 + 5 → Wave 3: Fires 4 + 6 → measure →
D1 (vendor check first) → D2 (design → ratify → build). Sub-agents build in fresh worktrees
and never commit; Winston reviews, commits direct to `main`, watches CI per fire.

Gates per fire: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
scoped `go test ./internal/refractor/...` — plus the **full `-p 4` suite** for Fires 2/3/4
(they change evaluation/write behavior for every lens — wide blast radius). Note the host is
currently memory-pressured; treat embedded-NATS handshake timeouts in untouched packages per
the CLAUDE.md triage rule (one scoped re-run, never loosen).

Measurement (task-tracked): re-pull `jsz`/`docker stats`/RSS after each wave; final proof is
a deliberate Refractor restart on the calm stack — drain time, consumer count, RSS, and the
absence of the deadline-exceeded storm.

## §D2 — Event-seeded delta evaluation (detailed design)

**Grounded premises** (verified in code): `ExecuteWithFootprint` consumes only `ec.Parameters`
— `EventContext.NodeKey` seeds nothing today, so an unanchored query full-scans on every
evaluation. Multi-walk branch merging (`branchmerge.go` `executeBranches`) is a Personal-lens
mechanism — those pipelines are already per-actor. So delta evaluation concerns exactly the
**plain, single-branch, non-DiffRetraction full-engine lenses** — the unanchored business-lens
corpus that dominates the census.

**Eligibility** (computed once at activation, alongside `plainReactsTo`): plain pipeline
(`actorEnumerator == nil`, no envelope fns), single compiled branch, `DiffRetraction` off,
and the compiled rule exposes its anchor label (the first MATCH node's label — the same
derivation `AnchorDeleteResult`/`AnchorProjectionKey` already use). Ineligible lenses behave
byte-identically to today.

**Phase 1 — anchor-event seeding (build now):**

- `ruleengine.EventContext` gains `SeedAnchor string` (the event vertex key). Inside the
  executor, the **first MATCH clause's anchor pattern** consumes it: when set, the pattern is
  labeled with the seed's own type, and the pattern carries no `key` property of its own, the
  candidate set is `{fetchNode(SeedAnchor)}` instead of the `seedNodes` scan. OPTIONAL MATCH
  and later clauses are untouched.
- Pipeline threading, per arm: the vertex arm sets `SeedAnchor = entry.CoreKVKey` when the
  event label equals the anchor label; the aspect arm seeds the **owner** vertex on the same
  condition; the link arm seeds the endpoint(s) whose type equals the anchor label and falls
  back to a full recompute for a non-anchor endpoint (two-endpoint dedup unchanged).
- A **neighbor** (non-anchor referenced-label) event takes today's full recompute — freshness
  semantics are exactly preserved; only anchor-labeled events narrow. Every mutation of an
  anchor arrives as that anchor's own event, so per-anchor recompute composes with per-key
  CDC delivery, including the DeliverLastPerSubject initial projection (per-key events become
  per-anchor evaluations).
- Retraction compatibility: the filter-retraction presence check is already scoped to the
  event anchor (`AnchorProjectionKey` + `resultsContainKeys`), so a seeded single-anchor
  result set answers it identically. `DiffRetraction` lenses are ineligible by definition.
  Multi-row-per-anchor lenses still produce their full per-anchor row set (seeding constrains
  the anchor binding, not pattern expansion).
- Write-side effect: results carry only the event anchor's rows — sibling anchors' rows are
  no longer rewritten at all (today they are rewritten identically on every trigger; Fire 4
  merely suppressed the redundant Put — Phase 1 removes the redundant *evaluation*).

**Phase 2 — reverse anchor enumeration (build only on measured need):** for a neighbor event,
derive affected anchors by walking the compiled pattern's relationship chain **reversed** from
the event label to the anchor label via `adjacency.Neighbors` (direction flipped, link-name
filtered per hop), then run per-anchor seeded recomputes. Derivation is attempted only for
linear chains (the `composeDataLensSpec` shape); var-length links, pattern comprehensions, or
multiple paths to the label fall back to full recompute — the same conservative posture as
`ReferencedLabels` exhaustiveness. The named trigger for building Phase 2: post-Phase-1
measurement showing neighbor-event full recomputes still dominating boot replay or steady
CPU (the plain-lens analog of the capability `ActorEnumerator`, derived from the AST instead
of hand-configured).

**Test obligations:** per-arm units plus an e2e proving (a) an anchor event rewrites only its
own row — sibling row revisions unchanged; (b) a neighbor event still refreshes dependent
rows; (c) a WHERE-flip on a seeded anchor still retracts; (d) a DiffRetraction lens's behavior
is byte-identical.

## 5. Reconciliation with the existing mental model

- *Didn't the type-relevance skip already bound amplification?* Only on the aspect/link arms;
  the vertex arm (the common case) and the adjacency watch never got the gate (Fire 3 / Fire 1).
- *Isn't DeliverLastPerSubject replay the intended initial-projection mechanism?* Yes — and it
  stays. Fires 2/3 (and later D1/D2) shrink what each replayed event costs, not the mechanism.
- *Does anything rely on identical-value re-writes?* Weaver re-evaluates to the same verdict;
  personal-lens transport is excluded (revision-ordered deltas); Loupe reads on demand. Fire 4
  verifies this claim against each adapter consumer as part of its review.
- *Is the adjacency watch load-bearing anywhere?* No — its productive arm is structurally
  unreachable (bare-NanoID Get), and both link arms pre-apply adjacency before evaluating.
  Freshness is carried by the stream-path arms + sweeps + rebuilds.

## 7. Post-ship measurement (closing restart, 2026-08-01 01:19 PT)

Binary rebuilt at all 8 fires; deliberate restart on the live dev stack (host under 12.7/13.3 GB
swap throughout — every number below is a floor, not a ceiling).

| Metric | Storm baseline | Post-ship |
|---|---|---|
| NATS RAM / CPU | 5.75 GB / 104% sustained, ~1400% burst | ~1.0 GB / draining-burst only |
| Slow consumers | 84 | **0** |
| JetStream streams | 142–147 | **50** (98 legacy `AUDIT_*` retired at boot) |
| JetStream store | 6.0 GB | 0.37 GB |
| Audit layout | 95 uncapped streams, 20.4 M msgs/7d | one `REFRACTOR_AUDIT`, 7d + 512 MiB cap |
| Boot to all 95 lenses started | ~50 lenses lagging, `context deadline exceeded`, no convergence | **22 s**, zero errors |
| Adjacency-watch consumers | ~95 standing + churn | 0 (mechanism deleted) |

The restart inherited a **~115k-message storm-era backlog** the old binary had made no visible
progress on (2.8% CPU while holding it). Within minutes the new binary had **54 of 100 lens
consumers fully drained**; the remainder split three ways, none a campaign regression:

- **6 pipelines infra-paused** (verify-and-pause probes failing on this host — environmental;
  their frozen pending is by design and surfaces on the existing health/pause plane).
- **One heavy `weaver-targets` convergence lens** crawling with AckWait redeliveries: its
  per-event evaluations are neighbor-shaped full recomputes, slower than AckWait under swap.
  **This is §D2 Phase 2's named trigger, measured** — reverse anchor enumeration is what
  removes this class. Phase 2 is now demand-justified (board row).
- A working band draining normally near the stream head.

Residual notes (no fires filed): `capabilityRoleIndex` projects raw `$projectedAt`, so Fire 4's
identical-skip rarely helps that one lens (documented at its envelope); a plain lens whose
anchor-derived key is not unique per anchor could cross-delete on the seeded WHERE-flip path —
the identical hazard already exists on the shipped anchor-tombstone path, no live lens has the
shape, and closing it needs a key-uniqueness notion the platform lacks today.
