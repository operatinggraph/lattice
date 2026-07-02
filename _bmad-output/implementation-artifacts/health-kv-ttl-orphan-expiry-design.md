# Health-KV TTL — dead-instance key expiry (orphan reclaim)

**Status: ✅ Andrew-ratified (2026-07-02) — Fire 3 = the consumer-state RE-KEY (the recommended
depth), sequenced after the ratified HealthSink consolidation (one-place change in the shared
`ConsumerSink`).** Fire decomposition COLLAPSED per Andrew's fewer-larger-fires rule: **Fire 1 =
Categories A + B together** (all TTL wiring — heartbeats + diagnostic keys — one mechanical plane,
one fire, incl. the optional `gc-stale` tail) · **Fire 2 = the Category-C re-key** (after the
consolidation build). §8's three-fire split is superseded accordingly.
**Author:** Winston (Designer fire, 2026-06-30)
**Backlog:** Stream-2 Component maintenance — *[Health-KV] Orphaned dead-instance heartbeat keys never expire* (★★, S–M)
**Owning component:** all heartbeat writers (Processor, Refractor, Weaver, Loom, bridge, object-store-manager) via the substrate; docs in `docs/observability/health-kv-schema.md`.

---

## For Andrew

**What it does (two lines).** Every component writes its Health-KV heartbeat with **no TTL** today, so a crashed/redeployed instance leaves `health.<component>.<instance>` (and its per-instance sub-keys) lingering forever — the Lamplighter can't tell *crashed* (was emitting, now gone) from *stale* (still present, just old). This restores the TTL that **Contract #5 §5.6 already mandates** (`TTL = interval × multiplier`, default 100s): live instances re-arm the TTL each heartbeat; a dead instance's keys self-expire and *disappear*.

**Architectural fork:** **none.** This is conformance + completeness within the already-sanctioned Health-KV direct-write plane (architecture P2 exception). No new primitive (`substrate.KVPutWithTTL` already exists and is already used by the auth-trace emitter), no new bucket config (the `health-kv` bucket is already provisioned with `LimitMarkerTTL`, enabling per-key TTL), no read-path/lens change.

**Frozen-contract change:** **none required.** Fire 1 *restores conformance* with Contract #5 §5.6 as written (the code drifted away from it). The auxiliary-key (non-heartbeat) lifecycle is documented in the **non-frozen** schema doc `docs/observability/health-kv-schema.md`, which is explicitly the authority for key-level details. I deliberately did **not** stage a frozen-contract edit — there is nothing to change in §5; see §6.

**The one judgment call for you (not a fork, but worth a glance):** Category C (consumer pause-state keys) is *durable state*, not a liveness signal — a naive TTL there would risk silently **resuming a structurally-/manually-paused consumer** after a long downtime (fail-open). Fire 3 resolves this by **re-keying pause-state to a stable, consumer-scoped key** (not instance-scoped), which both fixes the orphan *and* makes restore-across-restart actually work (today it silently doesn't for auto-generated instance IDs). If you'd rather not touch the HealthSink shape, Fire 3 can be dropped to "graceful-delete parity + long safety-net TTL" — called out in §9. Fires 1–2 stand alone and carry the bulk of the value.

---

## 1. Problem & intent

### 1.1 The symptom

Health KV (`health-kv` bucket) is the operational observability plane (Contract #5). Every long-running component overwrites `health.<component>.<instance>` on a ≤10s cadence. The `<instance>` segment is **per-process**: a restart generates a fresh NanoID (Contract #5 §5.1), so each restart/crash *changes the key*. With no expiry, the dead instance's key never goes away.

Consequences:
- **Lamplighter ambiguity** (the grounded demand). The Lamplighter (`agents/lamplighter/SKILL.md` §2) must "distinguish *not deployed* (orchestration tier down) from *crashed* (was emitting, now absent)." With orphaned keys, a crashed instance still *looks present* (a stale heartbeat), so the "was emitting, now absent" signal — the cleanest crash detector — never fires. The operator/agent has to reason about `heartbeatAt` age instead, which is weaker and noisier.
- **Unbounded accumulation.** Every redeploy adds a permanent `health.<component>.<NanoID>` key. Over a project's life the bucket fills with dead instances; `lattice health summary` and Loupe's system map must filter them, and one event-driven sub-key (`health.processor.<instance>.malformed-operation.<requestId>`) is *unbounded per instance* — a steady trickle of malformed envelopes leaves permanent keys.

### 1.2 The contract already says to fix it

**Contract #5 §5.6 is unambiguous** and *already frozen*:

> **TTL on each heartbeat write:** Default `TTL = heartbeat_interval × ttl_multiplier` where `ttl_multiplier = 10`. With the 10s default heartbeat, TTL = **100 seconds**. After 100s with no heartbeat write, NATS publishes a `PURGE` marker for the component's health key; observers see "no health entry" rather than stale-looking data.
> … **Each heartbeat OVERWRITES the previous heartbeat … resetting the TTL clock. Continuous heartbeating keeps the entry alive indefinitely; missed heartbeats expire it within the TTL window.**

The implementation never wired this. Every heartbeat goes through `conn.KVPut(...)` (no TTL). This design is therefore **drift correction**, not new architecture — the rare case where the contract is ahead of the code.

### 1.3 Vision tie

- Brainstorming #65 — *"KV bucket provisioning / TTL / replication policy per bucket class."* Health-KV's bucket class is *ephemeral-liveness*; TTL is its defining policy.
- Brainstorming #90/#91 — *Health-as-KV* + the *component heartbeat library* (`status`, `last_processed_sequence`, `error_rate`, `latency_p99`). A heartbeat library without expiry is half a heartbeat — liveness with no death.
- Brainstorming #96 — the *Closed-loop Weaver auditor (reads Health-KV, issues remediation Nudges)*; the Lamplighter (design `agentic-ops-design.md` §4/§6.1) is its dev-loop precursor. TTL-based disappearance is the substrate signal that auditor will key crash-detection on (FR54 anomaly detection).

---

## 2. Grounding — the pattern this mirrors

The fix reuses primitives that already exist; nothing greenfield.

| Building block | Where | Status |
|---|---|---|
| `Conn.KVPutWithTTL(ctx, bucket, key, value, ttl)` | `internal/substrate/kv.go` | **Exists.** Publishes the KV message with a `Nats-TTL` header; `ttl <= 0` falls back to plain `KVPut`. Already the mechanism the Processor auth-trace emitter uses. |
| `health-kv` bucket with per-key TTL enabled | `internal/bootstrap/primordial.go:106` (`cfg.LimitMarkerTTL = 1*time.Second`, "Enables AllowMsgTTL on the underlying stream") | **Exists.** No bucket migration needed; NATS enforces a 1s TTL floor. |
| The precedent: a fixed-TTL Health sub-key | `internal/processor/step3_auth_trace.go` — `health.processor.<instance>.auth-trace.<requestId>` written with **TTL = 1h** via `KVPutWithTTL` | **Shipped.** Proves the auxiliary-key fixed-TTL pattern; Category B mirrors it. |
| Consumer pause-state restore + graceful delete | `internal/{loom,weaver}/health_sink.go` (`Load` restores; `delete` on supervisor Remove) | **Exists** for Loom/Weaver; the bridge sink lacks `delete`. Informs Category C. |

Write path: Health KV is the **explicitly sanctioned direct-write plane** (Contract #5 §5.7; architecture P2 — "the only sanctioned direct-KV writes outside Refractor's own lens targets are Health KV"). Writers write directly; **no Processor op, no lens**. Read path: humans/CLI/Loupe/Lamplighter read `health-kv` directly (Contract #5 §5.7 — Health KV is *not* projected via a Capability Lens at this phase; P5's lens-only rule applies to Core KV, not the Health plane). So this design touches **no P2/P5 boundary and no Contract #1 key shapes** (Health keys are a separate addressing space, §5.1).

---

## 3. The orphan taxonomy (what actually orphans, and the fix per class)

Auditing every `health.*` writer (`grep` of `internal/` + `cmd/`) yields four classes. Only A–C are instance-scoped (the backlog item's "dead-instance keys"); D is noted and excluded with rationale.

### Category A — cadence-rewritten heartbeat keys (the core)
Re-written every ≤10s; lifecycle is exactly Contract #5 §5.6.

| Key | Writer |
|---|---|
| `health.processor.<instance>` | `internal/processor/health.go` `emit()` |
| `health.processor.<instance>.step3-latency` | same file, `emitCapabilityAuthSignals()` (per tick) |
| `health.refractor.<instance>` | `internal/refractor/health/lattice_heartbeater.go` `emit()` |
| `health.weaver.<instance>` | `internal/weaver/health.go` `emit()` |
| `health.loom.<instance>` | `internal/loom/health.go` `emit()` |
| `health.bridge.<instance>` | `internal/bridge/health.go` `emit()` |
| `health.object-store-manager.<instance>` | `internal/objectmanager/manager.go` `emitHeartbeat()` |

**Fix:** swap `KVPut` → `KVPutWithTTL(..., ttl)` with `ttl = heartbeatInterval × ttlMultiplier`, `ttlMultiplier` default **10** (the §5.6 architecture-locked default), component-configurable. Self-healing: each tick re-arms; `interval×10` after the last write, NATS purges the key. The per-tick sub-key (`step3-latency`) takes the *same* TTL — it shares the heartbeat cadence, so it re-arms in lock-step and dies with the instance.

### Category B — sparse per-instance diagnostic keys
Written **once per event**, never re-armed on a cadence; orphan with the instance and (for `malformed-operation`) grow unbounded.

| Key | Writer | Today |
|---|---|---|
| `health.processor.<instance>.malformed-operation.<requestId>` | `processor/health.go` `EmitMalformedOperation` | `KVPut`, no TTL, **unbounded** keyspace |
| `health.processor.<instance>.claim-attempts.<outcome>` | `processor/health_alerts.go` `RecordClaimAttempt` | `KVPut`, no TTL (bounded enum) |
| `health.processor.<instance>.commit-conflicts` | `processor/health_alerts.go` `recordCommitConflict` | `KVPut`, no TTL (one/instance) |
| `health.processor.<instance>.auth-trace.<requestId>` | `processor/step3_auth_trace.go` | **already TTL = 1h** ✓ |

**Fix:** give each a **fixed diagnostic TTL**, mirroring the shipped auth-trace 1h precedent (`KVPutWithTTL` with a configurable `diagnosticTTL`, default **1h**). A fixed (not re-armed) TTL is correct here — these are write-once breadcrumbs, not liveness. This bounds the unbounded `malformed-operation` keyspace and clears a dead instance's breadcrumbs within the window. No re-arm coupling to the heartbeat.

### Category C — durable consumer pause-state keys (the subtle one)
Written on a supervisor **transition** (SetActive/SetPaused), read on restart by `Load` to *restore pause-state* — so a structurally-/manually-paused consumer is **not silently resumed** after a restart.

| Key | Writer |
|---|---|
| `health.bridge.<instance>.consumer.<name>` | `internal/bridge/health.go` `consumerHealthSink.put` (no `delete`) |
| `health.loom.<instance>.consumer.<name>` | `internal/loom/health_sink.go` (has `delete` on Remove) |
| `health.weaver.<instance>.consumer.<name>` | `internal/weaver/health_sink.go` (has `delete` on Remove) |

This class **must not get a liveness TTL.** A long-paused-but-alive consumer writes no transition, so a death-tied TTL would expire its state while the instance lives; on restart it would `Load` *absent* → resume **active** — a fail-open that undoes an operator's pause or re-hits a poison message. (See §9, rejected Alt-3.)

There is a deeper bug hiding here: the key is **instance-scoped**, but the instance ID is per-process (`bridge` default `<hostname>-<pid>-<NanoID>`; Loom/Weaver `<comp>-<NanoID>`). After a restart the *new* instance writes/reads a *different* key, so **restore-across-restart silently never works** unless the operator pins the instance ID via env. So today these keys are simultaneously (a) orphaned on every restart and (b) useless for their stated purpose, except for pinned instances.

**Fix (the principled one):** re-key pause-state to a **stable, consumer-scoped, non-instance key**:
```
health.<component>.consumer-state.<consumerName>
```
This is correct on the merits: a consumer's pause-state is a fact about the *consumer/lane* (a poison message, an operator pause), **not about the process** that happens to host it. Re-keying:
- **Fixes restore-across-restart** for real (the new process finds the same key, regardless of its instance ID).
- **Eliminates the orphan** — the consumer-name set is bounded and stable, the key is reused, nothing accrues per-restart.
- **No TTL needed** (durable state, never expires on liveness) — so no fail-open.
- **Forward-compatible with HA** (memory `[[ha-nats-clustering]]`): when multiple instances of a component run, the pause decision is still consumer-scoped, not per-process — a shared consumer-scoped key is *more* correct than per-instance, not less. (Single-instance-per-component today, so no concurrent-writer contention now; HA will add OCC on this key when it lands — noted, not built here.)
- Retain the **graceful `delete` on supervisor Remove** (Loom/Weaver have it; **add it to the bridge sink** for parity) so a deliberately-removed consumer's state is cleaned immediately.

### Category D — NOT instance-scoped (excluded, with rationale)
These do **not** orphan on instance death (they outlive any single instance by design); they're a *different* concern and out of scope:
- `health.alerts.security.<alertCode>` (`processor/health_alerts.go`) — shared, alert-code-scoped; a separate "alert clears but key lingers" question (a future Lamplighter/alert-lifecycle item).
- Refractor per-lens bare `<lensId>` key (`refractor/health/reporter.go`) — lens-scoped; tied to the lens lifecycle, re-projected on rebuild, not to an instance.

I note these so the audit reads as complete; neither is part of "dead-instance keys."

---

## 4. Design decisions (resolved — no TBDs)

1. **TTL multiplier = 10, configurable.** Matches §5.6's architecture-locked default. Surfaced as a per-component config knob (env, e.g. `LATTICE_<COMP>_HEALTH_TTL_MULTIPLIER`) alongside the existing heartbeat-interval knob; a shared `health` default const so all components agree out of the box.
2. **TTL is derived, not absolute:** `ttl = interval × multiplier`. A component that heartbeats slower (Refractor under load) automatically gets a proportionally longer TTL — no false-death. The `interval` is already the `≥10s` floor enforced in each heartbeater's constructor; `interval × 10 ≥ 100s ≥ the 1s NATS floor`, always valid.
3. **`multiplier = 0` ⇒ TTL disabled** (falls through `KVPutWithTTL`'s `ttl<=0` → plain `KVPut`). An escape hatch for an operator who explicitly wants sticky keys; default is ON.
4. **Category B diagnostic TTL = 1h, configurable**, mirroring the shipped auth-trace constant. Not re-armed (write-once breadcrumbs).
5. **Category C: re-key to `health.<component>.consumer-state.<consumerName>`, no TTL.** (Fire 3; see §9 for the lighter fallback if you prefer not to touch the sink shape.)
6. **Pre-existing orphans (written before this ships) have no TTL and won't self-expire.** Live instances self-heal (their *next* heartbeat re-writes the key *with* a TTL). Already-dead instances' keys persist. Resolution: a one-shot operator cleanup — extend `cmd/lattice health` with a `gc-stale` subcommand (or document the `nats kv` purge) that deletes `health.<component>.*` keys whose `heartbeatAt` is older than `now − interval×multiplier`. Bundled as the small tail of Fire 1 (optional; the steady-state fix doesn't depend on it — no *new* orphans accrue once Fire 1 ships).
7. **No frozen-contract edit.** §5.6 already mandates the Category-A behavior; §5.7's "only heartbeat writes" is pre-existing descriptive drift (the codebase already writes many non-heartbeat keys) and the *authoritative* key-level reference is the non-frozen schema doc, which I update instead. (See §6.)

---

## 5. Contract & doc surface

| Doc | Change | Why |
|---|---|---|
| `docs/contracts/05-health-kv.md` (FROZEN) | **none** | Fire 1 *conforms to* §5.6 as written; the auxiliary-key lifecycle is below §5's altitude (§5 keeps schema-level conventions; §3 of the contract itself defers key-level detail to the schema doc). No edit staged. |
| `docs/observability/health-kv-schema.md` (not frozen; the key-level authority) | **edit (committable)** | Add a TTL column / lifecycle note to the key inventory: Category A keys carry `interval×multiplier` (default 100s); Category B carry the diagnostic TTL (default 1h); Category C is the re-keyed durable `health.<component>.consumer-state.<name>` (no TTL). This is the document future authors read; it must record the convention so un-TTL'd keys aren't re-introduced. |
| `docs/components/*.md` | none | Behavior-preserving for live components; no doc-visible surface change beyond the schema doc. |

If, on review, you want §5.6 to *also* spell out the auxiliary-key (B/C) lifecycle in the frozen contract (so it's a hard rule, not just schema-doc convention), that's a one-paragraph §5.6 addendum I'll stage **uncommitted** on your word — but I judge it unnecessary and have left §5 untouched per the "lean, don't disturb frozen contracts without need" posture.

---

## 6. Migration & test strategy

**Migration:** none structural. The bucket already supports TTL. Rollout is per-component code change; mixed old/new instances coexist fine (an old instance writes a no-TTL key, a new one writes a TTL'd key — independent keys). Pre-existing orphans handled per Decision 6.

**Tests (per fire, all `go test`-level with embedded NATS):**
- **Category A:** with a short interval (e.g. `interval = 1s`, `multiplier = 2` ⇒ TTL = 2s ≥ 1s NATS floor): write a heartbeat, assert the key exists; stop heartbeating; after > TTL assert `KVGet` returns `ErrKeyNotFound` (the crash-detection signal). Conversely, keep heartbeating across > TTL and assert the key *stays present* (re-arm works). Embedded-NATS fixtures must use `jsstore.Dir(t)` for StoreDir per the CI-parallelism rule (memory `[[project_ci_test_parallelism]]`).
- **Category B:** write a `malformed-operation` key with a short diagnostic TTL; assert it expires; assert a live instance's heartbeat is unaffected (independent TTL).
- **Category C (Fire 3):** pause a consumer → restart with a *different* instance ID → assert `Load` still restores `paused` from the consumer-scoped key (the bug fix — fails on the old instance-scoped key); assert supervisor Remove deletes the key; assert no orphan accrues across N restarts.
- **Verification gates** (CLAUDE.md): `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass` (Gate 2 — confirm the bypass suite still excludes Health-KV writes per §5.8), the relevant package `go test`. Category A touches the bypass-relevant Health plane only via the sanctioned direct-write path, so Gate 2 stays green.

**Live check:** `make up-full`, kill one orchestration-tier instance (`docker stop`), and confirm via `lattice health summary` / Loupe that its `health.<comp>.<instance>` key *disappears* within the TTL window — and that the Lamplighter then classifies it "crashed (absent)" not "stale."

---

## 7. Risks & alternatives

**Risks**
- *False death from a long GC pause / network blip.* Mitigated by the 10× multiplier (100s of slack) — this is precisely §5.6's stated rationale. Components with slower cadence get proportionally longer TTLs (Decision 2).
- *Category C fail-open* — the whole reason Fire 3 re-keys instead of TTL-ing (see Alt-3).
- *NATS TTL-marker mechanics.* The `Nats-TTL` header writes a per-message TTL on a `LimitMarkerTTL`-enabled stream; on expiry the server emits a limit/purge marker → `KVGet` returns `ErrKeyNotFound`. This is the same mechanism the shipped auth-trace emitter and the op-tracker path already rely on, so it's proven in this codebase.

**Alternatives considered**
- **Alt-1 — A sweeper that scans Health KV and deletes stale keys** (a Weaver `@every` or a Lamplighter actuator). Rejected: re-introduces a periodic scan and a *second* writer to a plane whose contract says self-expiry; strictly worse than the substrate doing it for free. (TTL *is* the sweeper.)
- **Alt-2 — Make heartbeats Processor operations** (brainstorm #113's "heartbeats become operations"). Rejected at the architecture decision (#113 → "Health-as-KV is a third state plane, NOT a Lens, NOT a Core exception"). Out of bounds.
- **Alt-3 — Put a liveness TTL on Category C consumer-state too.** Rejected: fail-open (resume a paused consumer after downtime). Re-keying to a durable consumer-scoped key (Fire 3) is the correct resolution and also fixes the latent restore-across-restart bug.
- **Alt-4 — Stable instance IDs everywhere** (pin every component's instance). Rejected as *the* fix: it papers over Category C's orphan (the key would be reused) but breaks Category A's whole model — §5.1 *wants* a new key per restart so a crash is visible as a *new* instance superseding an absent old one; stable IDs would hide restarts. Instance IDs stay ephemeral; TTL handles A, re-keying handles C.

---

## 8. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable + green; Fire 1 carries the contract-mandated core and most of the value.

- **Fire 1 — Category A heartbeat TTL (the core, build-to-contract).**
  Switch all seven Category-A writes (`Processor` heartbeat + `step3-latency`, `Refractor`, `Weaver`, `Loom`, `bridge`, `object-store-manager`) from `KVPut` → `KVPutWithTTL(..., interval × multiplier)`. Add the per-component `ttlMultiplier` config (default 10; 0 disables) + a shared `health` default const. Tests per §6 Category A. *(Optional tail: the `lattice health gc-stale` one-shot cleanup, Decision 6 — can split to its own fire if it bloats.)* **Coordination (2026-07-02):** the ratified *vertical-app self-report* design's optional Fire 3 has `object-store-manager` adopt the TTL'd `healthkv.Reporter` — whichever lands first covers objmgr's TTL; no conflict (this fire's direct `KVPutWithTTL` swap is superseded there if the Reporter adoption ships first). The two vertical apps are born TTL-on and need nothing here.
  → Restores Contract #5 §5.6 conformance; new orphans stop accruing; the Lamplighter's "absent = crashed" signal starts working.

- **Fire 2 — Category B diagnostic TTL.**
  Give `malformed-operation.<requestId>`, `claim-attempts.<outcome>`, and `commit-conflicts` a fixed `diagnosticTTL` (default 1h, configurable), mirroring the shipped auth-trace constant. Bounds the unbounded `malformed-operation` keyspace; clears dead-instance breadcrumbs. Tests per §6 Category B.

- **Fire 3 — Category C consumer-state re-key (durable, the subtle one).**
  Re-key the consumer-state sink from `health.<comp>.<instance>.consumer.<name>` →
  `health.<comp>.consumer-state.<name>`. No TTL. Tests per §6 Category C (incl. the
  restore-across-restart bug fix). **Reconciled 2026-07-02 (ratification session):** the *HealthSink
  consolidation* design (✅ ratified same day) lifts the three sinks into one shared
  `internal/healthkv.ConsumerSink` — so this fire **sequences after that consolidation** and becomes a
  **one-place** key change in the shared sink + its test suite (not three parallel edits); the bridge
  `Delete` parity comes with the shared sink for free. Loupe-classification checked: both the old and
  the re-keyed shape classify as `kindEvent` in `classifyHealthKey` (a dotted second segment), so the
  reader is untouched either way — and Loupe 2.0's F4 health absorption owns rendering regardless.
  *The lighter fallback (graceful-delete parity + a long safety-net TTL) remains available, but its
  rationale — "don't touch the HealthSink shape" — is weakened now that the shape lives in exactly one
  tested place; see §9 / For-Andrew.*

Sequence: Fire 1 → 2 → 3 (independent; 1 first as the contract-conformance core). Each leaves CI green on its own.

---

## 9. Open judgment for Andrew (restated, scoped)

The only non-mechanical call is **Fire 3's depth**:
- **(Recommended) Re-key to consumer-scoped durable state.** Fixes the orphan *and* the latent restore-across-restart bug *and* aligns with HA; small, contained, no fail-open.
- **(Lighter) Graceful-delete parity + long safety-net TTL.** Less code, leaves the restore-across-restart bug for auto-instances unfixed and accepts a long-downtime fail-open edge for pinned instances. Only if you want to keep the HealthSink shape frozen.

Everything else (Fires 1–2) is decided and build-ready on ratification.
