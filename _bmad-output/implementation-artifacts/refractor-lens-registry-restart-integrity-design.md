# Refractor lens-registry restart integrity — per-boot replay + registry-reconciliation detection

**Status: 📐 awaiting-Andrew (ratification)**
**Author:** Winston (Designer fire, 2026-07-13)
**Backlog:** Read-model / projection maturity — *[Refractor] lens registry silently empty after restart* (live-found P0-class incident, 2026-07-12/13)
**Owning components:** `internal/refractor/lens/corekv_source.go` (the fix), `internal/refractor/health` + `cmd/refractor/main.go` (detection), `cmd/lattice/health` (staleness surfacing). Docs: `docs/components/refractor.md`.

---

## For Andrew

**What it does (two lines).** Every Refractor restart since a durable consumer first caught up has silently booted with **zero active lens pipelines** while heartbeating `green` — all ~59 lens read models froze for ~14h on the dev stack before Loupe's lag column gave it away. This design (a) migrates the lens source to the **per-boot durable pattern you already ratified for Loom/Weaver** (arch-review 2026-07-02 finding #18, shipped `6aade75`/`e55bbbb`) — the 07-02 sweep fixed the siblings but missed the original afflicted caller — and (b) adds the detection half: a registry-reconciliation probe so "healthy heartbeat, zero/partial registry" becomes a **red** Health-KV issue instead of an invisible state.

**Architectural fork: none.** The fix mirrors a shipped, ratified pattern verbatim (Loom `internal/loom/source.go` `start()`); detection extends the heartbeater issue machinery the ratified lens-projection-liveness design already generalized. No new primitive, no new bucket, no Core-KV write, no contract change.

**Frozen-contract change: none.** Contract #5 §5.5 issue codes are component-defined; the new `LensRegistryIncomplete` issue and `lensesRegistered` metric land in the non-frozen `docs/observability/health-kv-schema.md`.

**Your recollection, answered directly** (you asked whether registry state was in health-kv or never stored): **per-lens *status* is in health-kv; registry *membership* was never stored anywhere.** `health.Reporter` persists `status`/`pauseReason`/`activeSequence` per rule, and `ConsumerSupervisor.restoreState` reads it back at pipeline start — but that restore only runs for a pipeline that *got started*, and the decision *which* pipelines to start lives only in `CoreKVSource.known`, an in-process map rebuilt exclusively from stream replay. When the replay yields nothing, nothing consults health-kv (or anything else) to notice the absence. §3 below covers why the ratified liveness design's signals also couldn't catch it.

---

## 1. The incident and the exact mechanism

**Observed (dev stack, 2026-07-12/13):** all ~59 per-lens health-kv entries frozen at `lastUpdated ≈ 2026-07-12T16:27:5xZ`; `core-events`/`core-operations` receiving new messages with zero AUDIT-stream pickup; Loupe showing 44/59 lenses "lagging"; the Refractor heartbeat **green** throughout. A `SIGQUIT` goroutine dump proved the process ran only the heartbeater, retry queue, adjacency bootstrapper, keyshredded manager, the `vtx.meta.>` watcher, and the control listener — **zero pipeline goroutines**. Recovered live by `nats consumer rm KV_core-kv refractor-lens-source` + restart (fresh full replay; backlog of ~123k pending KV events then drained normally).

**Mechanism.** `CoreKVSource.Start()` populates the lens registry solely from `SubscribeKVChanges(bucket, "vtx.meta.", "refractor-lens-source", {IncludeHistory: true})`. `IncludeHistory` maps to JetStream `DeliverAllPolicy` — which applies **only at consumer creation**. The durable was created 2026-07-08; `CreateOrUpdateConsumer` with the same name thereafter reuses the persisted ack floor. Once the floor reached the stream tip, every subsequent restart replayed **nothing**, so `known` stayed empty and `startPipeline` never ran. The source's own doc comment ("subsequent restarts pick up from the ack floor") describes the behavior accurately — it is simply the wrong behavior for a consumer whose job is to rebuild in-memory state, and the Loom fix's comment states the requirement precisely: *"a genuinely never-before-seen durable name is required"* for IncludeHistory to mean anything on boots after the first.

**Why hot-reload still worked:** the watch half is live — a *new* lens definition arriving post-boot dispatches normally. Only re-registration of *existing* lenses at boot was broken, which is exactly the shape that hides longest.

## 2. Grounding — the audit and the pattern we mirror

**This trap was found and fixed once already** — for the wrong callers' siblings. Arch-review 2026-07-02 finding #18 named the "cold-registry trap" in Loom's pattern source; `6aade75` (per-boot nonce, loom + weaver) and `e55bbbb` (PruneStaleDurables + delete-own-durable, substrate machinery) shipped the fix; Chronicler's host source got the same shape. The sweep never audited the Refractor's own lens source — the original and highest-blast-radius instance of the pattern.

Full audit of registry-rebuild vs work-queue consumers (the trap applies **only** to consumers that rebuild in-memory state from history replay):

| Consumer | Durable shape | Verdict |
|---|---|---|
| `refractor-lens-source` (`CoreKVSource`) | **fixed name** + IncludeHistory | **THE BUG — this design** |
| Loom pattern source (`internal/loom/source.go`) | per-boot `<prefix>-<instance>-<nonce>` + prune + delete-own | already fixed (`6aade75`) |
| Weaver target source (`internal/weaver/registry.go`) | per-boot, same shape | already fixed (`6aade75`) |
| Chronicler defs source (`internal/chronicler/source.go`) | per-boot, same shape | safe |
| Gateway revocation / credential-bindings materializers | fixed durables, `DeliverAll` | **correct as-is** — they fold into persistent KV buckets, not process memory; ack floor + incremental fold is the right semantics |
| `processor-outbox`, `object-store-cascade`, Refractor rule consumers | fixed durables | correct — true work queues (each message processed once) |

The substrate machinery (`PruneStaleDurables`, `DeleteDurable` — `internal/substrate/subscribe.go`) exists, is documented for exactly this pattern, and needs no change.

## 3. Reconciliation with the existing mental model

- **"Didn't the liveness design (ratified 2026-07-02) already cover silent lens stalls?"** It covers a *registered* pipeline that stops making progress: per-lens `consumerLag`, `lastProjectedAt`, and the lag-hysteresis backstop are all **push-updated by the pipeline's own LagPoller/Reporter**. An *unregistered* pipeline pushes nothing — its health-kv entry doesn't degrade, it **freezes at its last-written values** (status `active`, lag as-of-then), and nothing anywhere evaluates entry age. Self-reporting structurally cannot detect its own absence; that is the gap Fire B closes from the one vantage that always exists (the process-level heartbeater).
- **"Is registry state stored in health-kv?"** Status yes, membership no — see the For-Andrew block. This design deliberately does **not** start persisting membership as new state: Core KV *already is* the persistent registry (the `meta.lens` vertices, P1); the bug is a broken transport from that truth into memory, so the fix repairs the transport rather than adding a second store to drift.
- **"Does per-boot replay change steady-state semantics?"** No — it makes every boot behave exactly like a **fresh deployment's first boot**, which is the already-proven path (full history replay through `handle()`, whose `pendingSpecs` buffering and in-order create→update→tombstone processing converge to current state; `startPipeline` is idempotent behind the registry-exists check).
- **"Did I ratify the per-boot durable pattern?" No — genuine gap on both sides.** The pattern shipped as *maintenance fixes*, never through the design-ratification gate: `e55bbbb` (2026-06-12, prune + delete-own machinery), then arch-review 2026-07-02 finding #18, closed by `6aade75` (2026-07-05, per-boot nonce) as an M-level intake fire with no design doc. **This design is the first time the pattern's shape is actually put up for ratification** — the scrutiny is overdue, not redundant.
- **Parallel-designs check:** the two in-flight 📐 designs (sensitive-ref MAC, global-identity) don't touch this seam; the ratified liveness design is complementary (wedged-registered vs absent-unregistered) and Fire B reuses its issue conventions rather than duplicating machinery.

## 4. The design

### Fire A — the fix: per-boot durable, mirroring Loom verbatim

In `CoreKVSource` (plumb `instance` in from `cmd/refractor/main.go`, as Loom's `newPatternSource` does):

1. `Start()` derives `durable := "refractor-lens-source-" + instance + "-" + bootNonce` (`substrate.NewNanoID()`).
2. Before subscribing: `conn.PruneStaleDurables(ctx, bucket, "refractor-lens-source", durable, logger)` — **prefix deliberately without a trailing dash**, unlike Loom's `prefix+"-"`: the bare prefix also matches the legacy fixed durable `refractor-lens-source`, so the prune **is** the one-time migration (idempotent, not-found tolerated; no separate migration step, mirrors `DeleteStreamConsumer`'s documented pattern).
   **Hardening (surfaced by the horizontal-scaling stress-test, §4.1): the prune becomes age-guarded.** `PruneStaleDurables` today deletes by name-match alone — under two concurrent instances, A's boot would delete B's *live* durable, killing B's watch mid-run (the silent-gap failure class again, inflicted by a sibling). The shared helper gains a recency check: read each candidate's `ConsumerInfo` (`js.Consumer` — the same call `PendingForConsumer` already uses) and **skip any durable delivered-to or created within a threshold** (10 min). A live sibling's durable is always recently active → never pruned; a crashed boot's leftover just lingers one threshold longer. One change in the shared helper hardens all four meta-sources (Loom/Weaver/Chronicler inherit it for free), and it needs no liveness lookup, no coordination, no HA machinery.
3. Subscribe with the per-boot name, `IncludeHistory: true` (unchanged).
4. `consume()` gains the durable name and calls a `deleteOwnDurable` (fresh bounded context) on `ctx.Done` — Loom's shape verbatim.

**Side effect that pays for itself:** the "two CoreKVSources compete for one shared durable" constraint that the leaseconvergence harness and `edge_manifest_fire1_e2e_test.go` work around in comments **dissolves** (each source gets its own durable). No test code binds the name (it's unexported; those packages only describe it) — update the four stale comments in the same fire.

**Rejected alternatives** (each re-asked as "could a variant beat the recommendation?"):

- **(a) Boot-time `KVListKeys`+`KVGet` scan of `vtx.meta.>`, watch deltas only.** Architecturally attractive ("read the current KV truth, not stream history") and it would also drop replay cost. Rejected because it introduces a scan/watch startup race (mutations between scan and watch attach need an overlap-and-dedup protocol the replay design gets for free from single-stream ordering), and it would make the Refractor the one meta-source diverging from the three siblings' shipped shape — a second load path to maintain and audit. If a future stack's meta history grows large enough that per-boot replay hurts, this is the revisit (noted in §7) — with the reconciliation probe from Fire B already in place as its correctness net.
- **(b) Ephemeral consumer with DeliverAll.** Same replay effect, but loses `SubscribeKVChanges`' redelivery-on-Nak semantics mid-replay and still diverges from the sibling pattern for no gain over (c).
- **(c') Delete-and-recreate the *fixed* durable at boot.** Recreates the shared-name competition the e2e tests document (two sources/instances wipe each other's position mid-run); per-boot names solve identity and cleanup in one move.
- **(d) Persist the registry in a component-own KV bucket (the "stateful cache").** Andrew's question at ratification, and the codebase anticipates it twice: `SubscribeKVOptions.IncludeHistory`'s doc comment ("Future stories that stateful-cache the meta-vertex set can flip this back to false"), and the **adjacency index is this exact pattern, live** — derived state persisted in the refractor-private `refractor-adjacency` bucket + a **fixed** `refractor-adjacency` durable whose ack-floor resume is *correct there*, because the state the deltas fold into survives the restart. The lens-registry variant: persist `lensId → spec` in a refractor-private bucket, keep a fixed durable, boot = load cache + resume deltas. **Why adjacency persists and the lens registry replays: size.** Adjacency is O(entire graph) — its source stream is *all of* `KV_core-kv`, so full replay per boot is unacceptable and persistence is load-bearing. The lens registry is O(installed lenses) (~60 entries; server-side-filtered meta history ~10³ messages, ~1–2s observed), and boot cost is dominated by `startPipeline`, not the replay — so the cache buys nothing measurable while adding a second store that can drift from Core KV (needing the same Fire-B reconciliation probe *plus* cache-invalidation correctness), cache-write machinery on every lens mutation, and a new bucket provisioned in bootstrap (bucket provisioning belongs there — the rejected-`EnsureKV` decision). **Rejected at current scale; recorded as the anticipated end-state** if meta history ever grows to where per-boot replay is material (same revisit trigger as (a), and (d) is the stronger variant of it: the subscribe.go comment names it). The composed picture is coherent: each piece of Refractor state sits in the store whose size fits — membership replays (tiny), pause/lifecycle persists in health-kv (shipped), the adjacency index persists in its own bucket (huge).

### 4.1 How this behaves under horizontal scaling (Andrew's ratification question)

Split the Refractor's state by role and each half scales differently — and correctly:

- **The registry replay is not merely HA-compatible — it is a prerequisite for HA.** The registry is read-only derived config; N instances each holding the *full* ~60-lens registry is the correct semantics. Under the **old fixed shared durable, HA was structurally broken**: N instances pulling one durable *divide* the meta deliveries between them, so every instance boots with a random partial lens set (the exact starvation the leaseconvergence harness comment documents for two sources in one process). Per-instance durables are the only shape where every instance converges to the complete registry.
- **Work distribution is orthogonal and already correct.** The actual projection work runs on the per-lens **fixed** durables (`refractor-<ruleId>`, via the ConsumerSupervisor): under N instances those are shared queue consumers, so CDC events distribute across instances — that is where scale-out happens, untouched by this design.
- **The prune foot-gun is closed by the age guard** (Fire A step 2): a live sibling's durable is recently active and never pruned; no liveness lookup or coordination needed. This was the one place the pattern-as-shipped (all four meta-sources) could actively harm a scaled deployment.
- **Remaining inherited seam — lens-*ownership* partitioning only.** If a future HA design stops running every lens on every instance, then Fire B's probe invariant ("my registry ⊇ installed") must become union/ownership-aware, and the per-lens health-kv entries (bare-ruleID keys, last-writer-wins between instances) need the ownership rule — the *same* seam the ratified liveness design already recorded in its §5.6/§5.7. This design inherits it unchanged and adds no new instance-coupled state.
- **Multi-cell (Phase 3):** the durable prefixes already reserve a cell-id segment (Loom's and this source's comments); prune scope follows the prefix.

### Fire B — detection: the registry-reconciliation probe + staleness surfacing

1. **`lensesRegistered` metric** in the Refractor heartbeat each beat (registry size — the counterpart of the existing `lensLags` map, which is empty rather than alarming when the registry is empty).
2. **Reconciliation probe** in `cmd/refractor` wiring: after a boot grace window (60s), and then on a slow tick (10min), enumerate 3-segment `vtx.meta.*` vertices with envelope class `meta.lens` (skip tombstoned and `eventStream`/Chronicler specs, mirroring `isEventStreamSpec`) via `KVListKeys`+`KVGet` — the Refractor is a platform binary and an established direct Core-KV reader, so this is sanctioned — and diff against the registry. Any missing lens raises a single **`LensRegistryIncomplete` issue, severity `error`** (heartbeat status → degraded/red) naming the missing IDs (capped list), reconciled through the existing `openLensIssues` machinery (Contract #5 §5.5 `since` persistence); clears when the diff empties. Deliberate semantics: the issue covers both "replay never delivered it" *and* "delivered but activation failed" — today a fail-closed `translateSpec`/activation error is one log line and then silence, which is this same incident class in miniature. The probe makes fail-closed **visible**, which is the other half of fail-closed being correct.
3. **`lattice health summary` staleness:** per-lens rows currently render `Freshness: "-"` and evaluate only `consumerLag`/`errorCount` — a frozen entry looks merely "yellow, lagging" forever. Evaluate the entry's `lastUpdated` age like the heartbeat rows already do (stale threshold ⇒ `stale`/yellow at minimum); 59 rows reading "stale 14h" is the signal an operator can't miss. (The Loupe freshness *UI* column stays with the Loupe lane's F5 rider per the liveness design — Loupe already renders heartbeat issues, so `LensRegistryIncomplete` surfaces there with no Loupe-lane work.)

## 5. Test strategy

- **The regression, pinned:** an embedded-NATS unit/e2e that activates lenses through a `CoreKVSource`, stops it, starts a **second** source on the same stream (same instance string, new boot), and asserts every lens loads again — fails on today's code, passes with Fire A. This is the test the 07-02 sweep lacked.
- **Migration:** create a fixed-name `refractor-lens-source` durable, boot the new code, assert it is pruned and the full set loads.
- **Probe:** heartbeater/wiring unit with a fake registry missing one of two KV-installed lens definitions → `LensRegistryIncomplete` raised with the missing ID; register it → issue clears. Grace-window and eventStream-skip cases.
- **Existing suites:** leaseconvergence + edge_manifest e2e keep passing (their workaround becomes unnecessary, not broken); comments updated.
- Gates: `go test ./internal/refractor/... ./internal/substrate/... ./cmd/lattice/...`, full `go build`/vet/lint, and a live-stack restart smoke (`make up` reuse path) verifying `lensesRegistered` > 0 post-restart.

## 6. Migration / compatibility

None beyond the prune-swept legacy durable (§4 Fire A step 2). The dev stack's legacy durable was already hand-deleted during the 2026-07-13 recovery; the code path must still ship for every other deployment (CI ephemeral stacks are unaffected — always first-boot). No health-kv schema break: new metric + new issue code are additive (`docs/observability/health-kv-schema.md` updated in-fire).

## 7. Decomposition for the Steward

**One fire, two increments** (same component, small, independently green — fewer-larger):

1. **A — per-boot durable migration** (`corekv_source.go` + `cmd/refractor` instance plumbing + regression/migration tests + the four stale test comments).
2. **B — detection** (`lensesRegistered` metric, reconciliation probe + `LensRegistryIncomplete` issue, `lattice health summary` lens-row freshness, health-kv-schema doc).

Revisit trigger recorded: if per-boot replay cost ever becomes material (meta history ≫ today's ~10³ messages), promote alternative (a) (boot scan + delta watch) — Fire B's probe is the safety net that makes that refactor safe to attempt.

## 8. Risks

- **Durable churn:** one new consumer per boot, pruned next boot / deleted on clean shutdown — negligible catalog cost (the sibling pattern's accepted trade).
- **Replay cost per boot:** server-side subject-filtered to `$KV.core-kv.vtx.meta.>` — ~165 messages on the live 5-day-old stack; O(meta-history), not O(core-kv).
- **Probe false-positives:** a lens mid-activation at probe time — mitigated by the boot grace window + slow tick + hysteresis-free but reconciled (`since`-persistent) single issue key; a *persistently* missing lens is precisely what must alarm.
- **Concurrent-instance prune** (HA): closed by the age-guarded prune (§4 Fire A step 2 / §4.1); the residual HA seam is lens-ownership partitioning, inherited from the liveness design's §5.6/§5.7 and owned by the shelved HA design.
