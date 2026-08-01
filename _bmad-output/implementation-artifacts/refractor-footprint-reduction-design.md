# Refractor footprint reduction — kill the amplifiers

**Status: ✅ Andrew-ratified (in-session, 2026-08-01)** — Andrew directed the campaign live
("drive this whole refactor project to completion") after reviewing the measured cost model.
The D2 delta-evaluation fire carries its own detailed design section (§D2) and is ratified
separately once written; everything else builds against this doc.

**For Andrew:** Refractor's memory/CPU footprint — and the substrate load it induces — is
almost entirely *amplification*, not data: an 11 MB graph producing 6 GB of JetStream exhaust
and saturating NATS during boot/rebuild. Five measured amplifiers, seven fires. Quick wins
(Fires 1–5) are behavior-preserving constant-factor cuts; D1/D2 are the structural fixes.
No contract changes. One open vendor question gates D1 (§D1).

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
| **1 — Delete the dead adjacency watch** | Remove `runAdjWatch`/`drainAdjWatch`/`handleAdjUpdate`/`handleAdjNode`, the `runAdjWatch` launch, `SkipsAdjWatchWrite` + guarded-skip arms, `adj_watch_internal_test.go`; ADR-16 supersession note in `docs/components/refractor.md` + bootstrapper comments; fix the stale "until the adjacency watch heals it" comment. Keep bucket + bootstrapper + `Neighbors` (the executor's index). | S | 📋 |
| **2 — Label-prefix seed scan** | `seedNodes` generic path uses `ListKeysPrefix("vtx.<label>.")` when the pattern has a label (keep the KindVertex shape filter — the prefix also matches aspect keys); unlabeled patterns keep the full scan. | S | 📋 |
| **3 — Relevance-gate plain vertex events** | In `handle`'s KindVertex branch: plain lenses (`actorEnumerator == nil`) ack-and-skip when `!plainReactsTo(label)`. Anchor-label tombstones still pass by construction; non-exhaustive label sets keep reproject-all; actor-aware pipelines untouched. | S | 📋 |
| **4 — Skip-if-identical unguarded writes** | `NatsKVAdapter.Upsert` unguarded path: Get + byte-compare, skip identical Puts (the sweep's precedent, made universal). Audit skips with the write. Guarded/personal paths untouched. Postgres `IS DISTINCT FROM` optional follow-up. | S–M | 📋 |
| **5 — One audit stream** | Single `REFRACTOR_AUDIT` stream over `lattice.refractor.audit.>` with MaxAge 7d + MaxBytes ceiling; per-process ensure; stop creating `AUDIT_<ruleID>`; delete the 95 existing (dev history, Andrew-approved direction). | S–M | 📋 |
| **6 — Reap durable on lens delete** | Lens tombstone currently strands `refractor-<ruleID>` durables forever (lifecycle step 9); delete the durable when the lens is removed. | S | 📋 |
| **D1 — Server-side filter subjects** | Per-lens durable `FilterSubjects` derived from referenced labels (`$KV.core-kv.vtx.<label>.>`, `lnk.<label>.>`, `lnk.*.*.*.<label>.>`); broad filter kept for non-exhaustive label sets. **Gate: vendor-verify multi-filter × DeliverLastPerSubject on the NATS 2.14 pin, and filter-edit vs recreate semantics on an existing durable.** | M | 📋 gated on vendor check |
| **D2 — Event-seeded delta evaluation** | Anchor-label event → single-anchor recompute; non-anchor referenced-type event → affected anchors via adjacency reverse walk, per-anchor recompute. `DiffRetraction` lenses exempt (unanchored by design). Must compose with `CompiledBranches` (multi-walk), the per-anchor presence-check retraction, and the DeliverLastPerSubject initial projection. **Detailed design lands as §D2 in this doc before build; Andrew ratifies in-session.** | L | 📋 design pending |

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
