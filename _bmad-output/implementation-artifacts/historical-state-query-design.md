# Historical State Query (FR51) — design

**Status: 🅿️ Design reviewed & kept — BUILD DEFERRED (parking lot, very far back).**
Andrew, 2026-06-27: **defer the whole thing** (not even the cheap Layer-1a audit surface now) — low
near-term value (single-user experiment; the point-in-time-reconstruction consumer is thin) and a
standing storage cost not worth paying yet. The **design is kept on the shelf**, ready to revive when a
concrete need arises (an incident postmortem, a vertical that needs time-travel, a real audit requirement).
Fork-1 (reconstruction source) is **A — committed-delta archive** by the design's recommendation (B has
the auth-circularity flaw) — to be reconfirmed if/when un-parked.
**Component:** Refractor (read side) · reuses the Processor's deterministic mutation core
**Backlog row:** Lattice lane → *Parking lot — very low priority (far, far back)*
**Author:** Winston (Designer fire, 2026-06-27)

---

## For Andrew (ratify in one look)

**What it does, in two lines.** Gives operators two things the platform can't do today: **(1)** a durable, time-range-queryable **archive of the immutable record** (who requested what, when; and every committed graph delta) that outlives the 7-day / History-1 live retention; and **(2)** **point-in-time reconstruction** — "show me the graph (or any lens projection) *as it was at time T*" — by replaying the archived record into a throwaway sandbox and projecting the operator's chosen lens into an ephemeral target they query. It is the operator-facing realization of brainstorm #37 ("backfill/replay engine against the historical ledger") and the PRD "unlimited retention path" (prd.md:387–391), and it consumes seams the frozen contracts already reserved for it.

**Frozen-contract change: NONE.** The design builds *to* reserved seams, it does not change them:
- Contract #2 §2.3 already reserves the **`replay` lane** ("for the Replay tool's operations during disaster recovery").
- Contract #4 §4.3 already reserves the tracker **`"replaying"`** status.
- Contract #3 §(deterministic-replay, NFR-E4) already guarantees Starlark re-execution is byte-identical (NanoIDs/timestamps seeded from the op envelope).
No `docs/contracts/*` edit is staged. (One *adjacent* operational decision — long-retention of the archive vs. the live stream's bounded retention — is a bootstrap/config + new-target matter, not a frozen-contract clause; see §6.)

**One architectural fork for your call (designed through, recommendation given).**
**Reconstruction source — what do we replay to rebuild state-at-T?**
- **Option A — Committed-delta archive (RECOMMENDED).** An always-on archive captures every *committed* Core KV delta (put/tombstone, with commit-seq + timestamp) into a long-retention target. Reconstruction replays the archived deltas `[snapshot..T]` into a sandbox KV — **no re-execution, no re-authorization, faithful by construction** (it records outcomes, not intents). Cost: standing storage for the delta archive (the PRD's blessed "stream mirroring / dedicated lens adapter" path).
- **Option B — Op-ledger re-execution.** Replay the unbounded `core-operations` intent ledger through the deterministic mutation core. No new always-on storage (reuses the existing ledger), but reconstruction must reproduce each op's **commit/auth outcome** — and capability authorization is *circular* under replay (auth reads a lens projection that is itself being reconstructed), and the commit outcome isn't durably recorded past the 24 h tracker TTL. Correct only with extra machinery.

**Recommendation: A.** The delta archive sidesteps the auth-circularity and commit-outcome problems entirely and reuses the existing adapter + Rebuild machinery; Option B's intent ledger is still independently valuable as the **audit/"who-requested-what" surface** (Layer 1a below), so the design keeps *both archives* but reconstructs from deltas. Your call is whether the standing storage cost of a full-history delta archive is acceptable now, or whether point-in-time reconstruction should wait while only the (cheaper) intent-audit surface ships first.

Everything else is resolved in the body. Nothing here blocks the **Lattice Steward** except your ratification + the fork decision.

---

## 1. Problem & intent

**FR51 (PRD):** *"Operators can query historical operational state across a configurable time range."*
**Implementation intent (epics/index.md:335, authoritative):** *"Historical state query support (FR51) — replay any operation sequence into a temporary Lens target across a configurable time range. Phase 1 has the substrate (immutable `core-operations` stream, NFR-R5) but no operator-facing replay machinery."*
**Gap analysis (refractor-gap-analysis.md §3.1):** *"Substrate exists. No operator-facing replay-into-temporary-Lens machinery exists in Refractor. Epic-sized."*
**Vision (brainstorm #37):** *"Backfill/replay engine for new Lenses against the historical ledger."*
**Vault (Observability and Control.md):** *"Rebuild/Replay: a command sent to a Refractor Lens to wipe its target projection and replay the ledger from Sequence: 0."*

Today the platform is **amnesiac about its own past**:

| Store | What it holds | Retention | Time-travel? |
|---|---|---|---|
| Core KV (`KV_core-kv`) | the live graph | **History: 1** (latest revision per key) | **No** — old revisions are compacted away |
| `core-events` | the Processor's business-event outbox | **MaxAge 7 days** | only last 7 days, and these are *domain* events, not raw deltas |
| `core-operations` | the immutable op-intent ledger (FR11) | **unbounded** (no MaxAge/MaxBytes set) | full intent history, but intents ≠ state |

So an operator can answer *"what is true now"* (lens projections) but not *"what was true on March 3"* or *"what did the rent roll look like before the April rate change,"* and the audit ledger, while complete, is only queryable as a raw NATS stream, not by time range / actor / type. FR51 closes both gaps.

**Two distinct operator needs, one immutable record:**
1. **Audit / ledger query** — *"every operation between T1 and T2, by actor X, of type Y."* (FR11 + the audit-trail promise, prd.md:387.) Served by projecting the **intent ledger** to a queryable, long-retention target.
2. **Point-in-time state** — *"the graph / this lens's projection **as it was at T**."* Served by **reconstructing** state-at-T and projecting a lens over it.

> **Wording reconciliation.** FR51 says "operational state." `lattice-architecture.md` P1 reserves "operational state" for Health/Weaver/Loom/Adjacency (outside Core KV). The repeated *implementation* framing (epics/index, gap-analysis, vault) is unambiguously **graph/operation-record replay**, so this design targets that. Historical **health/metrics** over time is FR52's observability-store concern; the delta-archive pattern here extends to it cleanly and is scoped as a thin follow-on (§7, Increment 6), not the core feature.

---

## 2. Why this is well-grounded (the seams are already laid)

This feature is unusual: the architecture **pre-reserved** almost everything it needs.

- **Determinism is a frozen guarantee.** Contract #3: *"the Processor seeds the generator with the operation's `requestId` to ensure replay determinism (re-executing the same operation produces the same generated IDs)"* and *"the architectural boundary that makes Starlark execution deterministic and replayable (NFR-E4)."* The committing-op **timestamp** is likewise op-carried, not a wall-clock read — Contract #6 §6.2 / refractor.md:224: `projectedAt` is *"the committing op's `lastModifiedAt` … Same input → same value across replay/rebuild (no wall-clock churn)."* External results re-enter the ledger as **subsequent ops** (the bridge's replyOp), so they are captured too. ⇒ **A full in-order replay is reproducible by construction.**
- **The `replay` lane exists.** Contract #2 §2.3: a dedicated lane *"for the Replay tool's operations during disaster recovery; keeps replays from competing with live traffic."* FR51 is the first real consumer of the anticipated *Replay tool*.
- **The `replaying` tracker status exists.** Contract #4 §4.3 reserves it.
- **Replay-from-the-ledger is already the Rebuild mental model.** `Pipeline.Rebuild(ctx, truncate)` (refractor.md §"Rebuild & truncate semantics") *already* resets a lens's durable consumer to re-project **from the start of its source stream**. FR51 adds two things to that precedent: a **time bound** on the replay, and a **separate (sandbox) source + ephemeral target** so the live projection is never disturbed.
- **The retention path is blessed.** prd.md:391: *"A dedicated Lens adapter — or NATS-level stream mirroring — can write the full `core-operations` ledger to BigQuery for unlimited retention… The Lens adapter framework already supports external targets… no architectural changes."*

The design therefore **extends existing patterns** (Refractor adapters + Rebuild + the deterministic core) rather than inventing a subsystem.

---

## 3. The shape

Two layers. Layer 1 (archives) is ship-now and delivers most operator value on its own; Layer 2 (reconstruction) is the ambitious increment built on Layer 1's delta archive.

### 3.1 Layer 1 — archive lenses (the durable, queryable record)

Two Refractor target adapters, each an *identity-ish* projection of an immutable stream into a long-retention store. Both are pure **read-side** work (P5: operators read the projection; P2: no Core KV write — these only *consume* CDC and write to their own targets, exactly like every other lens target).

**1a. Intent-ledger audit projection** (serves need #1).
A lens/consumer over **`core-operations`** that writes each operation envelope to a queryable, long-retention target (Postgres now; BigQuery/ES later via the existing adapter SPI). Columns: `op_seq` (stream sequence), `request_id`, `submitted_at`, `actor`, `operation_type`, `lane`, `payload` (jsonb), `committed` (bool — see note), `mutation_keys` (from the tracker while live). Operators (via Loupe / SQL) then answer "all ops by actor X between T1–T2 of type Y" directly.
*Note on `committed`:* the live tracker (Contract #4) carries `mutationKeys` + commit status but is 24 h-TTL; the audit projection captures it **while live** (the consumer joins the op with its tracker at projection time) and persists it durably, so the long-term audit row records whether each intent committed without re-deriving it later.

**1b. Committed-delta archive** (the substrate for Layer 2; serves reconstruction).
A consumer over the **Core KV CDC stream** (`KV_core-kv` backing stream — the same source Refractor lenses already consume via `substrate.SubscribeKVChanges`) that **appends** every delta to a long-retention, **append-only** delta store keyed by `(core_key, commit_seq)` with the commit timestamp and the operation `mutation` (`create` / `update` / `tombstone`) + value bytes. Unlike a normal lens target (which is last-writer-wins, History-1), this target is **never compacted** — it is the time-ordered history of every change.
- **Bring-up backfill:** at first start the archive seeds itself from a one-shot replay of current Core KV (today's state = the genesis snapshot); from then on it captures every delta as it flows. (CDC is History-1, so the archive cannot recover deltas that predate its own bring-up — documented; for a fresh deployment it captures everything from genesis.)
- **Storage shape:** append-only table (`core_kv_delta(core_key, commit_seq PRIMARY ordering, committed_at, mutation, value jsonb, value_revision)`) or a NATS stream mirror with long MaxAge. Postgres recommended for queryability + snapshot joins (§3.3).

### 3.2 Layer 2 — point-in-time reconstruction (the time-travel surface)

Given a **(lens, T)** request, reconstruct state-as-of-T and project the lens into an ephemeral target the operator reads. Driven as an **operational job** (P1: operational state lives outside Core KV), via the Refractor control plane.

**Mechanism (Option A, recommended):**

1. **Request** arrives on the Refractor control plane: `lattice.ctrl.refractor.replay.start` with `{lensId | adHocCypher, asOf: T  (or window [T1,T2]), targetTtl}`. (Reuses the `micro.Service` control surface — refractor.md §control; same pattern as the Loom/Weaver control planes shipped for Loupe.)
2. **Provision** an isolated **sandbox Core KV bucket** (`replay-sandbox.<jobId>`) and an **ephemeral target bucket** (`replay-target.<jobId>`), both TTL-bounded.
3. **Reconstruct** state-at-T into the sandbox by replaying the **committed-delta archive** `[nearest snapshot ≤ T .. last delta with committed_at ≤ T]` — a straight, ordered apply of recorded deltas (create/update → put; tombstone → delete). **No Processor, no Starlark, no auth** — the archive already holds outcomes. This is the crux of why Option A is simpler and correct.
4. **Project** the chosen lens **once** over the sandbox bucket → the ephemeral target. This is exactly `Pipeline.Rebuild` semantics but pointed at the sandbox source + ephemeral target (a one-shot, bounded replay rather than a continuous consumer).
5. **Operator queries** the ephemeral target via the normal read path (Loupe reads the KV/Postgres target; P5 ✓).
6. **GC**: the sandbox + ephemeral target are dropped on `targetTtl` expiry or `lattice.ctrl.refractor.replay.release`. Job state (status, progress, source range, target name) lives in a Refractor-owned **`refractor-replay-jobs`** bucket (operational state, P1) — never Core KV.

**Job-state shape** (`refractor-replay-jobs.<jobId>`, operational KV): `{jobId, lensId, asOf, status: pending|reconstructing|projecting|ready|released|failed, sourceRange, targetBucket, createdAt, ttlExpiresAt, error?}`. Surfaced by the control plane's `replay.list` / `replay.status` endpoints (so Loupe can show "historical query jobs in flight," and the Lamplighter can flag a wedged/failed reconstruction).

### 3.3 Snapshots (performance increment)

Replaying genesis→T for a large ledger is O(history). Mitigation, classic event-sourcing: the delta archive periodically materializes a **snapshot** (full key/value set as of a snapshot commit-seq) into a snapshot table keyed by `snapshot_seq`. Reconstruction then loads the nearest snapshot ≤ T and replays only the deltas after it. Snapshots are an *optimization* of Layer 2, shipped as a later increment — correctness holds without them (replay from genesis).

### 3.4 Read path / write path / naming (invariant check)

- **P5 (reads = lenses).** Every operator-facing surface is a **projection target** read via `KVGet`/`KVListKeys` (or SQL on the Postgres target): the audit projection (1a), the delta archive query (1b), and the ephemeral reconstruction target (Layer 2). No operator reads Core KV. The replay *job* status is an operational read (control plane / Health), not a graph read.
- **P2 (writes = ops).** Nothing in this design writes Core KV. The archives consume CDC and write *their own* targets; reconstruction writes only **sandbox/ephemeral** buckets. The sole interaction with the write path is *reading* the immutable `core-operations`/CDC record. ✓
- **P1 (operational state outside Core KV).** Replay jobs + snapshots + archives are operational/derived state in Refractor-owned buckets/tables, never Core KV. ✓
- **Contract #1 key-shapes.** This feature introduces **no new graph vertices/aspects/links** — it reconstructs *existing* keys (`vtx.*`, `lnk.*` preserve their 4-/6-segment shapes through replay verbatim, since the archive stores the literal Core KV keys). The new namespaces are operational bucket/target names (`replay-sandbox.<jobId>`, `replay-target.<jobId>`, `refractor-replay-jobs`, `core-kv-delta-archive`) — not graph keys, so Contract #1's vertex/link grammar doesn't apply, but they follow the existing `<domain>-<purpose>` bucket convention (`weaver-targets`, `refractor-adjacency`).

---

## 4. Contract surface

| Contract | Touch | Why |
|---|---|---|
| **#2 Operation Envelope §2.3** (`replay` lane) | **build-to (reserved)** | Reconstruction's archive replay does *not* re-submit ops, so the live `replay` lane is only relevant if Option B were chosen, or for the DR Replay tool. Either way: no change — the lane already exists. |
| **#4 Idempotency Tracker §4.3** (`"replaying"` status) | **build-to (reserved)** | Layer 1a reads the tracker's commit status to stamp `committed`; the reserved `replaying` status is available if a future op-replay path needs it. No change. |
| **#3 Mutation/Event-list** (NFR-E4 determinism) | **build-to (relied on)** | The determinism guarantee is what makes *any* replay faithful; the chosen Option A doesn't even need to re-execute, so it relies on the *recorded* deltas rather than re-derivation. No change. |
| **#5 Health KV** | **build-to** | Replay-job liveness + a wedged/failed-reconstruction issue are emitted on the Refractor heartbeat using the existing §5.4/§5.5 `status`/`issues[]` channel (same pattern as the capability-lens liveness alert). No change. |
| Refractor control surface (`lattice.ctrl.refractor.*`) | **out-contract, additive** | New `replay.{start,status,list,release}` endpoints — Refractor's own out-contract, not a frozen `docs/contracts/*` clause. |

**Net: no frozen-contract edit is staged.** This is a feature that fits the platform's reserved seams.

---

## 5. Migration / compatibility

- **Purely additive.** No existing key, lens, op, or contract changes. Adding the archive lenses is a `CreateMetaVertex` lens-seed (Layer 1) + new adapter code; the reconstruction path is new control endpoints + a sandbox/ephemeral lifecycle. Nothing existing breaks if these are absent.
- **Archive bring-up is the only ordering caveat** (§3.1b): the delta archive can only capture history from its own start; a deployment that wants genesis-complete history must enable the archive at (or seed from) genesis. Document in the Refractor doc + the install notes.
- **Existing Rebuild is unaffected** — Layer 2 *reuses* the Rebuild replay logic but never points it at a live target, so live lenses and the live truncate/guard semantics (Contract #6 §6.2 watermarks) are untouched.

---

## 6. Risks, alternatives & adversarial pass

A focused self-adversarial pass (Blind-Hunter / Edge-Case lenses), findings folded in:

1. **Auth circularity under op-replay (Option B).** *Caught in design.* Re-executing ops requires re-authorization, but capability is a *lens projection* being reconstructed in the same pass — circular, and the original commit outcome isn't durable past 24 h. **Resolved by choosing Option A** (replay recorded *deltas*, not intents — outcomes are already baked in). This is the single strongest reason for the recommendation.
2. **Non-determinism.** *Bounded.* Even Option B is deterministic per Contract #3 NFR-E4 (seeded NanoIDs, op-carried timestamps, external results re-entering as ops). Option A doesn't depend on it at all. No residual risk.
3. **DDL/script version skew.** *Handled by full in-order replay.* A faithful reconstruction must use the schema/script that was live at the op's time. Because DDL meta-vertices are themselves created/updated via ops/deltas in the same ordered record, an in-order replay rebuilds DDL-as-of-T *interleaved with data-as-of-T* — self-consistent. (Option A: the meta-vertex deltas are in the archive too, so the sandbox holds the correct DDL; though Option A doesn't *execute* DDL, the reconstructed graph already includes the meta-vertices' historical state.)
4. **History-1 means no recovery of pre-archive deltas.** *Accepted + documented.* The CDC stream is compacted; the delta archive can't reach back before its own bring-up. Mitigation: enable at genesis for full coverage; for existing deployments, "history starts at archive bring-up" is stated plainly (no silent gap).
5. **Storage cost of an always-on full-history archive.** *The fork's real cost.* Mitigated by: tiering to cheap external targets (Postgres → BigQuery/object store), snapshot+compaction of the *delta* archive beyond a retention horizon, and making the archive **optional** (a deployment that only wants the cheaper intent-audit surface enables 1a, not 1b). Flagged to Andrew as the fork's trade-off.
6. **Replay job resource blast radius.** A genesis→T reconstruction on a huge ledger could be heavy. Mitigations: snapshots (§3.3), the `replay` lane / off-peak scheduling for the DR variant, per-job TTL + concurrency cap on `refractor-replay-jobs`, and the job runs in its own sandbox so it can't degrade live projections.
7. **Sensitive data in archives (Vault interaction).** *Cross-design note.* The Vault/crypto-shredding design ([`vault-crypto-shredding-design.md`](vault-crypto-shredding-design.md)) makes `sensitive:true` aspects **ciphertext-at-rest, including in the immutable ledger** (Contract #3:198). The delta archive therefore stores **ciphertext** for sensitive aspects automatically — a shredded key renders historical reconstructions of that aspect unreadable too, which is the *correct* right-to-be-forgotten behavior. No extra work, but called out so the two designs stay coherent when both land.

**Alternatives considered & rejected:**
- *Bump Core KV `History` to N / use NATS KV revision history.* Rejected: per-key revision count ≠ a global as-of-T snapshot, and unbounded N is the same storage problem without the queryability or snapshot story.
- *Reconstruct by re-running a full sandbox Processor (auth + DDL + outbox suppressed).* Rejected as the *primary* path (Option B) for the auth-circularity reason above; retained conceptually for the DR Replay tool where re-execution from intents is the point.
- *Lengthen `core-events` MaxAge and reconstruct from business events.* Rejected: business events are *domain* events, not a complete raw-KV delta log, so they can't faithfully rebuild arbitrary keys.

---

## 7. Decomposition for the Lattice Steward (fire-by-fire)

Each increment is independently shippable + green. Layer 1 delivers value before Layer 2 exists.

- **Increment 1 — Intent-ledger audit projection (1a).** New consumer/adapter over `core-operations` → Postgres `operation_ledger` table (out-of-band DDL, per the Postgres adapter convention). Joins the live tracker for `committed`/`mutation_keys`. Operator value immediately: time-range/actor/type audit queries (FR11 + much of FR51). *Size: S–M. No contract change.*
- **Increment 2 — Committed-delta archive (1b).** Append-only delta consumer over the `KV_core-kv` CDC → `core_kv_delta` (Postgres) with bring-up backfill from current KV. Verified: replay `[genesis..now]` of the archive reproduces current Core KV byte-for-byte. *Size: M. The substrate for Layer 2.*
- **Increment 3 — Reconstruction coordinator + control plane.** `lattice.ctrl.refractor.replay.{start,status,list,release}`; sandbox + ephemeral bucket lifecycle; the ordered delta-apply into the sandbox; `refractor-replay-jobs` operational state; reuse `Pipeline.Rebuild` against the sandbox source → ephemeral target. e2e: reconstruct a lens-as-of-T and assert the projection equals the known historical state. *Size: M–L. The headline FR51 capability.*
- **Increment 4 — Operator surface in Loupe.** A "Historical query" panel: pick a lens + time, kick a replay job, watch status, browse the ephemeral target, release. (Loupe is the inspector — the only app allowed the trusted read path.) *Size: S–M (FE).*
- **Increment 5 — Snapshots + delta-archive compaction.** Periodic snapshots (§3.3) so reconstruction starts from the nearest snapshot; horizon-based compaction of the raw delta archive behind snapshots. *Size: M. Pure optimization.*
- **Increment 6 — (optional, FR52 companion) Health/metrics history.** Extend the archive pattern to Health KV CDC for historical *operational* metrics over time, feeding the Lamplighter's trend analysis. *Size: S–M. Adjacent; only if Andrew wants the FR51 "operational state" reading served literally.*

**Dependency order:** 1 ⟂ 2 (parallel) → 3 (needs 2) → 4 (needs 3) → 5 (optimizes 3) ; 6 independent.

---

## 8. Test strategy

- **Unit.** Delta-apply reducer (create/update/tombstone → KV state) table tests; archive consumer idempotency (replayed CDC message ⇒ no duplicate delta row, keyed by `(core_key, commit_seq)`); reconstruction bound selection (`asOf T` → correct delta range; snapshot selection).
- **Determinism/faithfulness e2e (the keystone).** On the ephemeral stack: drive a known op sequence with timestamps, capture the delta archive, then **reconstruct `[genesis..now]` into a sandbox and assert it equals live Core KV byte-for-byte** (the same guarantee Rebuild's steady-state-equals-from-scratch test asserts, refractor.md §Rebuild). Then reconstruct as-of an intermediate T and assert the chosen lens's projection equals the projection computed live at T.
- **Lifecycle e2e.** `replay.start` → `ready` → operator reads the ephemeral target → `release` GCs sandbox + target; TTL expiry GCs an abandoned job; a failed reconstruction surfaces a Refractor Health `issues[]` entry.
- **Gates.** `go build ./...`, `make vet`, `golangci-lint`, `make verify-kernel`, plus a new `make test-historical-query` ephemeral-stack target mirroring `test-object-gc` / `test-lease-convergence`.

---

## 9. Open questions — resolved

| Question | Resolution |
|---|---|
| Replay source: op-ledger vs delta vs KV-history? | **Committed-delta archive** (Option A) for reconstruction; **op-intent archive** retained for audit. (The fork itself — accept the standing storage cost — is Andrew's, §For-Andrew.) |
| Re-execute the Processor, or apply recorded deltas? | **Apply recorded deltas** — no re-execution, no auth circularity. (Re-execution is reserved for the DR Replay tool, a separate concern.) |
| Where does the historical query *result* live? | An **ephemeral lens target** (KV or Postgres), read via the normal P5 read path; GC'd by TTL/release. |
| Where does replay-job state live? | A Refractor-owned operational bucket `refractor-replay-jobs` (P1) — never Core KV. |
| Bounded vs unbounded live ledger retention? | Keep the **live** `core-operations`/CDC bounded; durability is the **archive's** job (PRD's blessed mirror-to-long-retention path). No contract clause governs this — it's bootstrap/config + a new target. |
| New graph vertices for a "query"? | **No.** Reconstruction rebuilds existing keys; the query is an operational job, not a graph mutation. Keeps the write path (P2) and Contract #1 grammar untouched. |
| Sensitive-aspect handling in the archive? | Inherits the Vault design: archives store **ciphertext** for `sensitive:true` aspects; a shredded key correctly renders historical reconstructions unreadable (§6.7). |
| "Operational state" (FR51 wording) vs graph state? | Core design targets **graph/operation-record** replay (the implementation intent); historical **health/metrics** is the FR52-adjacent Increment 6. |

---

*Designer fire output: a complete, reviewable design + board update. No code, no contract commit. Awaiting Andrew's ratification (and the §For-Andrew fork decision) before the Lattice Steward builds it.*
