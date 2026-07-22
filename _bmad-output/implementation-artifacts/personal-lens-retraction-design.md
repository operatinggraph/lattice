# Personal Lens retraction — authoritative keyset frames (reconcile-at-the-edge)

**Status: 📐 awaiting-Andrew (ratification)** · Author: Winston (Designer fire, 2026-07-22) ·
**Component:** Refractor (`internal/refractor/{pipeline,adapter,projection}`) + Edge client (`internal/edge/{sync,store}`, `cmd/facet`, `internal/edge/browser`)
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → Component maintenance → *[Refractor] Personal Lens rows never retract* (★★, M).
**Demand:** the [staff-worlds F5 Inc-2 residual](facet-staff-worlds-design.md) (§6) — live-proven on the showcase stack: a completed work-order task stayed on the tech's mirror un-retracted, and the verticals board's ★★★ staff-worlds row is **blocked today** on exactly this ("claim beat blocked-on mirror-delivery"; the delivery half shipped `5c5cb236`, this is the retraction half).
**Extends:** [personal-secure-lens-design.md](personal-secure-lens-design.md) (✅ ratified; PL.1–PL.5 shipped) — this closes the retraction gap that design never covered.
**Frozen-contract change: NONE** (the delta envelope + frames are component-level surfaces per that design's ratified §4; `docs/components/refractor.md` + edge docs updated at build).

---

## For Andrew (one-look ratification block)

**What it does (two lines).** Every per-actor evaluation a Personal Lens already runs (live fan-out and `personal.hydrate` — the same `reprojectActors` code path) additionally publishes one small **keyset frame**: "here is the complete, authoritative set of keys this lens currently projects for you, as of revision R." The Edge client — the stateful party — diffs its mirror against the frame and prunes what dropped out; the Refractor stays exactly as stateless as the vault demands (*"the Refractor doesn't have to remember what it sent to User A"*, Personal Lens.md §4.1).

**The one thing to understand before ratifying.** All three shipped retraction mechanisms (filter-retraction, `applyDiffRetraction`, the auth-plane sweep) diff a fresh evaluation against a **durable target-side key ledger** (KV bucket / Postgres via `KeyLister`). The personal target is an append-only subject stream — its "target state" is the **device mirror**. So the diff cannot run server-side without inventing new server state; this design ships the authoritative set to where the state already durably lives and runs the diff there. No new server state, no new wire verb for the client to trust beyond "this list is current."

**No architectural fork** (no Gateway / D1 / Vault / multi-cell / HA-NATS surface). Two posture calls I made that deserve your eye:

1. **Statelessness over a server-side key ledger** — the rejected Alternative A would give precise per-key deletes at the cost of an O(identities × rows) mutable Refractor-owned ledger that can itself diverge and needs its own reconciliation. The frame costs bytes-on-the-wire per reprojection instead (bounded: keys only, and the live path already re-publishes the full row set as upserts per event — §3.1). I recommend the vault's shape.
2. **Migration wipes the mirror once** — the client store schema gains per-key lens attribution; on version mismatch the mirror is purged and cold-hydrated (the store's own documented posture: the mirror is disposable, the cloud is truth). One extra hydration per device at rollout; no data loss (mirror is a cache).

**Also fixed in scope (latent defect found grounding this):** an identity-vertex tombstone today emits a Delete with a **capability-shaped key** on personal pipelines (`actorDeleteKeyFor` fallback, `evaluate.go:116-123, 446-458`) which `NatsSubjectAdapter.Delete` rejects (`__actor` absent). The error classifies **transient** (`failure.Classify` default) and personal lenses configure no retry queue, so the event **redelivers with backoff indefinitely on every personal pipeline** (ten on the showcase stack) — permanent per-rule redelivery churn, not even a DLQ. The frame mechanism replaces it with an empty keyset per lens (mirror-clearing), closing the defect structurally.

---

## 1. Problem + intent

A Personal Lens row that stops matching is never retracted, live or via cold `Hydrate`:

- **Live:** a CDC event routes through `evaluateFanOut`/`evaluateLinkFanOut`/`evaluateAspectFanOut` → `reprojectActors` → `executeFullForActor`, and a row that dropped out of the result set is simply **absent** — `personalEnvelopeFn` can only `ErrSkipProjection` (D1 deny, interest miss, degenerate anchor: `projection/personal.go:137-165`), never delete. The plain-lens retraction block is explicitly gated off for actor-enumerator pipelines (`evaluate.go:158`).
- **Cold:** `Pipeline.Hydrate` re-runs the same `reprojectActors` and publishes upserts + `hydrationComplete{revision}` — the client (`internal/edge/sync/sync.go`) merges, never prunes; `hydrationComplete` carries only a revision, and the `Store` interface has no prune primitive. A stale key survives hydration, restart, and the SSE/browser snapshot replay forever.

Live consequences already observed on the showcase stack (staff-worlds F5): a `complete` task's `manifest.task` row stayed on the maintenance tech's mirror; and the claim beat — a claimed task must leave every other role-holder's inbox — cannot be live-proven, blocking the verticals lane's ★★★ row.

The wire verb and the client's application of it already work end to end: `NatsSubjectAdapter.Delete` publishes `op:"delete"` (`natssubject.go:275-291`), the sync manager tombstones the store, fires `OnChange(key,true)`, and the DOM row is removed (`sync.go:367-413`, `app.js:257-259`). **Only the decision to retract is missing.**

## 2. Grounding ledger (verified in code this fire)

| Fact | Where | Bearing |
|---|---|---|
| Live fan-out re-executes the **full personal cypher per affected actor** ($actorKey-anchored) | `evaluate.go:320-429` → `reprojectActors:435` → `executeFullForActor:183` | the actor's complete current row-set is already computed on every triggering event — the authoritative set is free |
| Link fan-out seeds enumeration from **both endpoints** after idempotently applying the (tombstone) to adjacency | `evaluateLinkFanOut:349-398` | the claim beat's *other* role-holders are enumerated on the `queuedFor` tombstone — retraction can reach them |
| `personalEnvelopeFn` filters (D1 `IsReadable`, Interest Set) by **skip only** | `projection/personal.go:127-174` | a key omitted by the security filter is omitted from the frame ⇒ revocation retracts by omission, and frames leak nothing (post-filter keys only) |
| Plain retraction block gated `actorEnumerator == nil && envelopeFn == nil`; `applyDiffRetraction` **requires an unanchored whole-scan** and a `KeyLister` adapter | `evaluate.go:158-174, 492-537`; `cmd/refractor` activation guard | the shipped diff mechanism is structurally unusable here: personal cyphers are `$actorKey`-anchored by construction, and the adapter is deliberately stateless (`natssubject.go:52-66`) |
| Identity tombstone emits a **cap-shaped** delete key on personal pipelines | `evaluate.go:116-123, 446-458` (no personal `SetActorDeleteKey`) | latent defect: `NatsSubjectAdapter` rejects it (`__actor` absent) — fixed structurally by empty frames (§3.4) |
| Hydrate = `reprojectActors` for one actor, `highWater` captured **before** reprojection, then `hydrationComplete` | `pipeline/hydrate.go:35-78`; fan-out to every lens via `personalHydratorByRuleID` (`control/service.go:953-998`, `5c5cb236`) | hydrate emits frames through the same seam; the capture-before ordering makes concurrent live deltas survive the prune (§3.3) |
| Client: `Store` (`bolt.go`/`idb.go` behind one conformance harness) applies LWW-by-revision per key; delete = retained tombstone; no prune/generation primitive; mirror documented **disposable** | `internal/edge/store/*`, `sync.go:367-413` | the client is the durable stateful party (vault §2) and can host the diff; schema bump ⇒ purge + rehydrate is sanctioned |
| Same-key **multi-lens overlap is established practice** (`edgeTasks` / `edgeTasksQueued` both project `manifest.task.<id>`; `edgeCatalog` / `edgeCatalogRoles` ditto) | staff-worlds design §3.3 + shipped lenses; `pipeline.go:945-951` (per-lens seqs, no cross-pipeline order) | retraction MUST be lens-attributed: on a claim, the queued lens's frame and the assigned lens's upsert land from **independent pipelines with no cross-lens ordering** — an unattributed delete would race the fresh row; attribution makes the outcome converge regardless of interleaving |
| Unknown envelope `op`s are acked + cursor-advanced by old clients | `sync.go:406-408` | frames are additive — old client + new server degrades to today's behavior, no rollout ordering |
| Vault vision: Personal Lenses are *filters not clones*; the Refractor remembers only a revision; recovery = re-hydration | Obsidian *Edge Lattice/Personal Lens.md* §4 | the design's spine — reject server-side emitted-key state |

**Parallel-design check:** no 📐/🏗️ design touches this seam (the three sibling retraction designs — negative/filter-retraction, full-engine anchor-tombstone, GrantTable anchor-tombstone — are all shipped, all KV/Postgres-target-side; the verticals staff-worlds row is a *consumer* of this design, not an overlapping producer).

## 3. The shape

### 3.1 The keyset frame (wire)

Two additive changes to the component-level delta envelope (`natssubject.go`):

- **Every `upsert` gains `lens`** — the producing lens rule ID (deterministic package-minted NanoID; the adapter carries it from construction). This is the attribution the same-key overlap requires.
- **A new op `"keyset"`:**

  ```json
  { "op": "keyset", "lens": "<ruleID>", "keys": ["manifest.task.9uJ…", "manifest.op.Xw2…"],
    "revision": 10481, "projectionSeq": 10481 }
  ```

  Semantics: *as of `revision`, lens `lens` projects exactly `keys` for this subject's identity.* Keys are the built target-key strings the client already stores — the client diffs directly, no key-map inversion. Keys are **post-envelope-filter** (D1 + Interest Set): a key the actor may not read is not named, so frames leak nothing and a revoked anchor retracts by omission.

Published via a new optional adapter interface `KeySetPublisher` (the `HydrationMarkerPublisher` precedent), implemented by `NatsSubjectAdapter` only. Frames ride the same SYNC subject/stream, so ordering vs. the rows they describe is the stream's.

### 3.2 Producer emission (Refractor) — one frame per enumerated actor, per lens, per event

After the pipeline's write loop successfully applies an event's results, the personal path groups the non-delete results by `__actor` and publishes one frame per **enumerated** actor (live: the fan-out's actor list; hydrate: the one identity) carrying that actor's business keys at the event's `projectionSeq` (hydrate: the pre-captured `highWater`).

Three rules, each load-bearing:

1. **Emission is driven by the enumerated-actor list, not the result set.** An actor whose evaluation yields zero surviving rows gets an **empty frame** — that is exactly the last-row retraction (and the `emptyBehavior`-inert lesson from the sibling board row: absence-of-rows must still produce a signal, or the mechanism is silently inert for the case it exists for).
2. **No frame on failure.** An evaluation or write error fails the event (existing disposition path); frames are emitted only after the event's results are fully applied. At-least-once redelivery re-emits — frames are idempotent.
3. **Frames are per-lens.** Each personal pipeline emits independently; cross-lens coordination is the client's refcount (§3.3). No new cross-pipeline machinery.

**Cost honesty:** one frame per (lens, enumerated actor) per triggering event, carrying key strings only. The live path *already* re-publishes the actor's full row set as upserts on every such event (shipped behavior), so frames add a small fraction of existing traffic. A dedup/hashing optimization would require exactly the server-side memory this design refuses — deferred with the PL.6 multicast bandwidth lever, revive on a measured bandwidth driver.

### 3.3 Client reconciliation (Edge) — per-key lens attribution + prune

`store.Entry` gains `Sources map[ruleID]revision` — which lenses currently assert this key, each at its last asserting revision. The mirror rule set:

- **`upsert` (lens L, rev R):** body/`Revision` LWW as today; `Sources[L] = R` iff `R ≥ Sources[L]` (per-source monotonic). Two sharpenings the adversarial pass forced: **(a)** an upsert that *loses* the body LWW must still record its attribution (`Sources[L]=R`) — otherwise the refcount undercounts and the winning lens's next frame prunes a key the losing lens still asserts (the existing early-return shape in `bolt.go:75-78` must not skip it; a conformance vector pins what `applied`/`OnChange` mean for an attribution-only write); **(b)** the store keeps a per-lens **frame high-water** `frameHW[L]` (one uint64 per lens), and an upsert from L with `R < frameHW[L]` whose key is **not currently attributed to L** is dropped — this closes the resurrection race where a Nak'd-then-redelivered stale upsert lands *after* a frame that retracted its never-stored key (omission leaves no tombstone to lose against; `sync.go:383,394` Naks + JetStream redelivery make the reorder real).
- **`keyset` (lens L, rev F):** advance `frameHW[L]` to F; for every stored key attributed to L with `Sources[L] ≤ F` and absent from `keys`: remove L's attribution. A key whose `Sources` empties is tombstoned (existing `ApplyDelete` semantics — `OnChange(key,true)` fires, feeds/DOM drop the row). A key in `keys` the client doesn't hold is ignored (the rows themselves arrive as upserts).
- **`delete` (per-key, lens-attributed):** removes L's attribution at rev R (kept for wire compatibility; nothing emits it after this design — frames subsume it).

Why the revision guard is correct: within one lens, `ProjectionSeq` is the stream sequence (`pipeline.go:945-951`) — a frame racing a **newer** upsert from the same lens cannot kill it (`Sources[L] > F`); a **hydrate** frame (rev = pre-captured `highWater`) cannot kill a concurrent live delta (rev > highWater) — PL.4's capture-before ordering composes unchanged; and the `frameHW` guard covers the one reorder (redelivery) the in-order argument misses. **No rule compares revisions across lenses** — cross-pipeline seqs share no order, and the mechanism doesn't need one: per-lens convergence + attribution refcounting decide every overlap outcome. The optimistic-write overlay is untouched: a prune tombstones the *confirmed* entry exactly as an explicit delete does today; a pending intent survives until its own confirmation cycle (existing semantics).

New `Store` method (`ApplyKeySet`), implemented in both engines (`bolt.go`, `idb.go`) and pinned by the shared conformance harness. **Migration:** store schema version bump; on mismatch, purge + cold hydrate (the store's documented disposable-mirror posture; `ensureFresh` already routes a cursor-less store to hydrate). No in-place entry migration — legacy entries carry no attribution and cannot be safely diffed.

### 3.4 Hydrate and the missing-identity case — the same mechanism

- **Hydrate:** each pipeline's `Hydrate` publishes its rows then its keyset frame at `highWater`; `hydrationComplete` is unchanged. Cold reconnect therefore prunes stale keys **with almost zero hydrate-specific client logic** — frames are uniform across live and cold. (This resolves the F5 residual's stated fork — "Refractor-side per-actor emitted-key tracking vs. client-side full-snapshot-replace on `hydrationComplete`" — with a third shape that is better than both: snapshot-replace *per lens*, delivered incrementally, that also covers the live path the second option couldn't.)
- **Lens decommission (the adversarial pass's second must-fix):** frames from lens L are the only thing that can remove L's attributions — so a lens dropped from the DDL (or re-minted under a new ruleID) would strand its keys on every mirror forever, with no emitter left to heal them. Closed at the hydrate cycle: the `personal.hydrate` **response** (which the client itself calls — `callHydrate`) gains `lenses: [ruleIDs]`, the set of registered personal hydrators that ran (`personalHydratorByRuleID` is exactly this set, `control/service.go:955-977`). After a completed hydrate the client drops every attribution whose lens is not in the set (safe regardless of frame timing — a dead lens emits nothing to race). Absence-of-emitter thereby still produces a signal, on the lens axis as well as the key axis.
- **Identity tombstoned (live):** the actor-tombstone shortcut and the missing-actor branch emit, for personal pipelines, an **empty keyset frame per lens** instead of today's malformed cap-shaped Delete — the device converges to an empty mirror (sign-out UX is the session layer's concern). `Hydrate` keeps refusing a missing identity (existing behavior).

### 3.5 What retracts when (coverage)

| Cause | Trigger reaching the pipeline | Retraction path |
|---|---|---|
| Task claimed (queuedFor → assignedTo) | link tombstone; both endpoints seeded | other holders' frames omit the task; on the claimant's mirror the queued-lens frame and the assigned-lens upsert arrive from independent pipelines — attribution refcounting makes the row **converge present** under every interleaving (a transient tombstone-then-reappear flicker is possible if the queued pipeline outruns the assigned one; the §5 vector asserts convergence, not interleaving) |
| Task completed / WHERE flip | aspect event on the anchor | assignee's frame omits the key |
| `Unwire*` (residesIn / worksAt) | link tombstone | frames omit every row reached through the removed edge |
| Role revoked / read-grant shrink | the underlying link/aspect event enumerates the actor (permission→role→identity is within the BFS bound) | D1 filter drops the keys ⇒ frames omit them — **when the cap-read producer pipeline has re-projected the grant slice first**. The D1 gate is a live KV read of `cap-read.*.<actor>` written by a *sibling* pipeline with no cross-pipeline ordering, and the cap-read bucket write itself is no CDC event the personal pipeline consumes — so if the personal pipeline evaluates first, retraction waits for the next enumerating event or hydrate. Topology-revocations self-heal through the cypher regardless; this window applies only where D1 is the *sole* dropper. Risk row §9. |
| Anchor vertex tombstoned | vertex event, fan-out from it | cypher filters the tombstone ⇒ frames omit |
| Identity tombstoned | actor-type event | empty frame per lens (§3.4) |
| Lens dropped from the DDL / ruleID re-minted | — (no emitter left) | hydrate-response lens-set prune (§3.4) |
| Missed events (Refractor down, event stuck) | — | next hydrate's frames heal (the vault's stated recovery: re-hydration) |

**Inherited enumeration blind spots (pre-existing, delivery-symmetric, named rather than glossed):** the fan-out BFS (a) never traverses *through* actor vertices, so a row for actor I derived via another identity J neither delivers nor retracts live; (b) takes the singleton fast path on identity-vertex events, so *other* actors' rows referencing a mutated identity never refresh live; (c) truncates at 10 000 actors (logs-and-proceeds), and truncated actors get no frame. All three are exactly as stale on the **delivery** side today — no row that arrives live can fail to retract live under this design — and all three heal at hydrate. Widening enumeration is a separate, pre-existing concern this design deliberately does not touch. **Interest-set narrowing** likewise triggers no reprojection (`UpdateInterest` only re-registers); the client posture is: follow a narrowing with `Rehydrate` — the hydrate frames then prune the newly-irrelevant keys, which is a genuine improvement over today's "linger forever."

**Threat-model honesty:** retraction is a *correctness/hygiene* mechanism, not confinement. Data already delivered to a device cannot be un-delivered by any retraction protocol; real revocation of delivered secrets is the crypto-shred path (ciphertext deltas + `IssueSessionKey` denial, PL.5 — shipped). Nothing here weakens the D1 publish-time gate, and frames carry only keys the actor passed that gate for.

## 4. Reconciliation with the existing mental model

- *Didn't we already build retraction?* Three times — filter-retraction (`AnchorProjectionKey`), `applyDiffRetraction`, and the anchor-tombstone paths, plus the auth-plane sweep. Every one diffs against a **durable server-side target ledger** via `KeyLister`. The personal path is the lone structurally different target (append-only stream; state lives on devices) and is explicitly gated out at `evaluate.go:158`. This design is the same convergence idea with the diff relocated to where the state is.
- *Why not give `NatsSubjectAdapter` a `KeyLister` backed by new server state and reuse `applyDiffRetraction`?* Two structural reasons beyond taste: `applyDiffRetraction`'s exactness precondition is an **unanchored whole-scan** lens (its own doc comment), which a `$actorKey`-anchored personal cypher violates by construction; and the ledger would be new mutable security-adjacent state with its own divergence modes. (Alternative A, §6.)
- *Does this duplicate the auth-plane sweep?* No — the sweep heals server-owned targets; there is nothing server-side to sweep here. The client prune driven by frames *is* the sweep, continuously.
- *New state?* Server: none (the design's point). Client: `Sources` on entries the device already owns — the vault's "Edge is the stateful one," made slightly more precise.
- *Does per-event full-slice re-publication contradict the "tiny update packet" vision?* That is the **shipped** live-path behavior (PL.2's `reprojectActors`), not this design's addition; frames add keys-only messages on top. Narrowing reprojection itself is a separate, pre-existing efficiency question this design deliberately does not touch.

## 5. Test strategy

- **Refractor unit/e2e** (`internal/refractor/*_e2e_test.go` precedent): frame emitted per enumerated actor with post-filter keys; **empty frame** on last-row loss; claim vector (two role-holders — loser's frame omits the task; the claimant's mirror **converges** to the assigned-shape row under the client rules — a convergence assertion, never an interleaving assertion); revoke-holdsRole vector (frames shrink by omission — the vector must sequence/poll the cap-read grant projection *before* asserting, or it flakes and can pass for the wrong reason); identity-tombstone → empty frames per lens (and the malformed-cap-key redelivery loop is gone); hydrate emits per-lens frames at `highWater` + the response carries `lenses`; no frame on a failed write.
- **Edge store conformance** (`storetest/conformance.go`, both engines): `ApplyKeySet` prune; per-source monotonic guard (frame-then-newer-upsert, upsert-then-stale-frame, hydrate-frame-vs-concurrent-live-delta); the `frameHW` resurrection guard (Nak'd-then-redelivered stale upsert after a retracting frame is dropped); attribution-only write on body-LWW loss (with pinned `applied`/`OnChange` semantics); refcount survival under same-key two-lens assert/retract; dead-lens attribution prune from the hydrate lens-set; schema-mismatch purge.
- **Sync manager** (`sync_test.go`): `keyset` handling, unknown-op back-compat pin, `OnChange(deleted)` firing on prune, hydrate-response lens-set application; browser-host frame parity + snapshot no longer replays a pruned key (`host.go` / `server.go` vectors).
- **Live acceptance:** the staff-worlds claim beat on the showcase stack — a second role-holder's mirror drops the claimed task; the completed work-order row leaves the tech's mirror. (The offline-claim **UX** green bar stays the verticals lane's F5; this design's bar is the mirror-level retraction it is blocked on.)

## 6. Alternatives considered

- **A. Server-side per-(actor,lens) emitted-key ledger** (Refractor-owned KV; diff → per-key deletes). Precise, but: violates the vault's named statelessness principle; O(identities × rows) mutable state with CAS churn per reprojection; the ledger itself can diverge from both the stream and the truth, demanding a second-order reconciliation (sweep-of-the-ledger); duplicates state the device durably holds. Re-asked per the alternatives discipline: a compressed variant (per-(actor,lens) hash) still carries the state + divergence problem while losing per-key precision. Frames get exactness with zero server state.
- **B. Per-anchor delete derivation** (mirror the shipped `AnchorProjectionKey` filter-retraction per actor). Stateless, but structurally partial: personal rows are neighbor-keyed and multi-row (`AnchorProjectionKey` returns ok=false by construction — the same shape that forced `applyDiffRetraction` for composite lenses); broad losses (role revoke) retract only trigger-adjacent rows; hydrate stays unhealed; and the same-key overlap needs lens attribution anyway. Frames subsume it at similar wire cost.
- **C. Hydrate-only snapshot prune** (no live retraction). Fails the named consumer: the claim beat needs the row gone from other inboxes live, not at next reconnect.
- **D. TTL/expiry on mirror rows.** Wrong semantics both directions — a valid untouched row must not expire; a stale row must go now.

## 7. Decomposition for the Steward (each independently shippable + green)

- **R1 [lattice · Refractor] — frame production.** Envelope `lens` field; `keyset` op + `KeySetPublisher` on `NatsSubjectAdapter`; pipeline emission per enumerated actor after successful write (live + hydrate), empty-frame and identity-tombstone/missing-actor cases (removing the malformed cap-key redelivery loop); `personal.hydrate` response gains `lenses`; Refractor e2e vectors; `docs/components/refractor.md` update. Green alone: old clients ack-and-ignore frames (pinned today in `sync.go:406-408`), so R1 ships with zero client risk. Consumer named and waiting: R2 + the blocked verticals claim beat.
- **R2 [lattice · Edge client] — frame consumption.** `Store.ApplyKeySet` + `Sources` attribution + `frameHW` guard + schema-version purge (both engines + conformance); sync-manager `keyset` handling + hydrate lens-set prune; browser-host/Facet snapshot vectors; the live showcase retraction proof. Green: unit + conformance + the claim-beat mirror vector live. *Depends on R1.* On R2 landing, the Steward notifies the verticals lane that the claim-beat block is lifted (their F5 bar resumes).

Both fires have their consumer today (the blocked ★★★ verticals row) — the dead-scaffolding test passes: build now.

## 8. Adversarial pass — RUN (this fire, 2026-07-22; independent read-only reviewer against the code)

Verdict: no blocker; the spine (client-side diff against per-lens authoritative keysets, zero server state) held under attack, every file:line citation verified accurate. Three must-fix findings, all folded into §3/§5 above: **(1)** a Nak'd-then-redelivered stale upsert could resurrect a key a frame had retracted by omission (no tombstone to lose against) — closed by the client `frameHW[L]` guard (§3.3b); **(2)** a decommissioned/re-minted lens strands its attributions forever (no emitter left) — closed by the hydrate-response lens-set prune (§3.4); **(3)** "revocation retracts live" overstated the grant-shrink case — the D1 gate races the sibling cap-read producer pipeline; coverage table and §5 vector corrected, risk row added. Also folded: the claim-beat argument rewritten to rest only on per-lens ordering + convergence (cross-lens seqs share no order); the three inherited enumeration blind spots and interest-set narrowing named in §3.5; the attribution-only-write conformance vector (§5); and the identity-tombstone defect's real behavior sharpened from "likely DLQs" to an indefinite per-rule redelivery loop. No deferred gate remains for the Steward.

## 9. Risks

- **Wire chattiness** (frame per lens per actor per event). Bounded by keys-only payloads and dwarfed by the existing full-slice upsert re-publication; the dedup lever is deferred with PL.6 (needs the server memory this design refuses) — revive on a measured bandwidth driver, not speculatively.
- **Grant-shrink retraction window** (adversarial finding 3): where D1 is the sole dropper of a key, retraction lands on the revoking event only if the cap-read producer projected first; otherwise on the next enumerating event or hydrate. Same bounded-CDC-lag posture as the write-path Capability KV and D1 M3 — and strictly better than today (never). No new confinement exposure: publish-time filtering is unaffected.
- **A buggy empty frame is client data loss** — mitigated: the mirror is a disposable cache (cloud is truth; rehydrate restores), frames are only emitted after successful writes, and the e2e vectors pin the empty-frame cases. Failure direction is re-hydratable staleness, never wrong data served as fresh (the `frameHW` guard closes the one path where staleness could masquerade as freshness).
- **Multi-instance Refractor (HA future):** frames inherit whatever per-lens single-writer story the HA design gives rows — no new problem introduced; noted for the ratified HA-NATS design to inherit.
- **Schema-bump hydration burst at rollout:** every device cold-hydrates once. Showcase-scale trivial; for a production fleet the rollout note is to deploy R1 (server) first — R2 clients then migrate on their own upgrade schedule.
