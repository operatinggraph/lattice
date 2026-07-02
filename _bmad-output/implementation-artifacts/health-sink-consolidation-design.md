# Consumer HealthSink consolidation — one shared `internal/healthkv` pause-state sink

**Status: ✅ Andrew-ratified (2026-07-02) — Fire 3 (Bridge) INCLUDED; fires COLLAPSED to ONE
(consolidate + rewire Loom/Weaver/Bridge together) per Andrew's fewer-larger-fires rule — §9's
three-fire split is superseded.** Ratification Q&A folded into
§2.1: the Refractor exclusion is semantic (the `rebuilding` third state + mid-rebuild arbitration +
its own entry schema/key with existing readers), and the Processor has **no** sink at all today (lane
specs omit `Health` — pause-persistence is an open posture the per-lane-consumers row must decide
explicitly; if it decides yes, it adopts this shared `ConsumerSink` rather than minting copy #4).
**Author:** Winston (Designer fire, 2026-07-01)
**Backlog:** Stream-2 component maintenance — *[Weaver] HealthSink pause-restore round-trip uncovered* (★★, XS–S) + *[Loom] HealthSink pause-restore round-trip uncovered* (★★, XS–S). Both rows point here; a third, unfiled copy (Bridge) is folded in as a bonus.
**Owning component:** a shared primitive in `internal/healthkv` (the health-KV-plane seam) + three consumers `internal/loom`, `internal/weaver`, `internal/bridge`.

---

## For Andrew

**What it does (two lines).** Loom, Weaver, and the Bridge each carry a **byte-identical** copy of the same per-consumer pause-state sink (`consumerHealthSink` + `consumerStateCache` + `consumerState` + `pauseReasonFromString` + `consumerHealthEntry`, ~180 LOC each) — the code that persists a supervised consumer's pause/active state to `health.<component>.<instance>.consumer.<name>` and restores it across a restart. All three copies sit **uncovered** at the restore branch (the two filed ★★ items). This lifts the one true copy into `internal/healthkv`, tests the restart round-trip **once**, and deletes the three duplicates.

**Architectural fork:** **none.** This is a pure DRY consolidation of already-shipped, byte-identical code onto the existing `substrate.HealthSink` interface. No new mechanism, no new plane, no security surface, no read/write-path change.

**Frozen-contract change:** **none required.** The `.consumer.<name>` health key is a **component-internal** sub-key, explicitly documented in the code as *"a SEPARATE, smaller shape from the Contract #5 heartbeat document"* — it is **not** part of Contract #5's frozen `health.<component>.<instance>` key surface (§5.1) and nothing in `docs/contracts/*` is edited. **No contract edit is staged.**

**The one judgment call for you (not a fork).** The board filed only Loom + Weaver. Grounding surfaced a **third** identical copy in the Bridge (`internal/bridge/health.go`) that no one filed. I recommend folding it in (Fire 3) so we don't leave one of three copies behind — but it is a clean, optional bonus; if you'd rather keep the scope to exactly the two filed items, drop Fire 3 and the design still stands. Called out in §4.3 + §9.

**Reconciliation with the sibling in-flight design (important).** The parallel `vertical-app-health-self-report-design.md` (📐 awaiting-Andrew) *also* introduces `package healthkv` — but a **different** primitive: a `Reporter` for consumer-**less** daemons (the vertical apps' heartbeat loop). Mine adds a `ConsumerSink` for consumer-**bearing** engines (pause-state restore). **They compose, they don't collide** (distinct type names, both import only `substrate`); `internal/healthkv` becomes the coherent home for shared health-KV-plane primitives. Whichever design's build lands first creates the package `doc.go`; the second adds its files. Detailed in §6.

---

## 1. Problem & intent

### 1.1 The symptom (grounded demand)

Every component that runs a `substrate.ConsumerSupervisor` needs to persist *why* a consumer is paused so that a restart re-enters the same pause state (an operator's structural pause must survive a crash; an infra pause must resume when the probe passes). The `substrate.HealthSink` interface (`internal/substrate/consumer_supervisor_spec.go:79`) is the seam: `SetActive` / `SetPaused` / `Load`. Each engine supplies its own implementation, keyed entirely by the caller (the supervisor "never invents or namespaces health keys").

Three components implement that seam with **the same code**:

| Component | File | Shape |
|---|---|---|
| Loom | `internal/loom/health_sink.go` | `consumerHealthSink` + `consumerStateCache` (in `health.go`) |
| Weaver | `internal/weaver/health_sink.go` | byte-identical to Loom's, modulo package name, the `health.loom.`→`health.weaver.` key prefix, and comment wording |
| Bridge | `internal/bridge/health.go` | same shape, minus the `delete` method (the bridge's single consumer is never `Remove`d) |

The two filed items are: the **restore branch is uncovered**. Concretely — `consumerHealthSink.Load`'s *paused* arm (`health_sink.go:75-81`: an entry with `status:"paused"` → restore `StatusPaused` + the persisted reason) and `pauseReasonFromString`'s switch arms have **0% coverage** in Loom and Weaver; the restart-pause-restore round-trip is unexercised end-to-end. No test in either package references `consumerHealthSink`, `Load`, `SetPaused`, or `pauseReasonFromString`.

### 1.2 Why it's uncovered — and why that's the root cause, not the symptom

The restore branch is untested in *all three* places for the same reason: it is **duplicated boilerplate**. Nobody wrote the round-trip test because there is no single home for it — you'd have to write the *same* test three times against three copies. The duplication is not incidental to the coverage gap; it *is* the coverage gap. Fixing "add a test to Loom" and "add a test to Weaver" independently would mean **two copies of the same test suite guarding two copies of the same code** — and would leave the Bridge's third copy still bare. Consolidating first, then testing once, fixes the root cause.

### 1.3 Vision tie

- **Brainstorming #90/#91** — *Health-as-KV* + a *component heartbeat library*. The sibling design takes the first step (a shared `Reporter`); this design takes the second (a shared `ConsumerSink`). Together they turn `internal/healthkv` into the "one heartbeat/health library, not five hand-rolled ones" the inventory calls for.
- This is the maintenance analog of the same instinct that motivated the sibling design's §4.1: *don't perpetuate a hand-rolled copy in an Nth place — lift the shared part into `internal/healthkv` and leave the genuinely-different parts (the rich per-engine heartbeaters) alone.*

---

## 2. Grounding — the pattern this mirrors

Nothing here is greenfield; this is a *subtraction* (three copies → one) onto an existing interface.

| Building block | Where | Status |
|---|---|---|
| `substrate.HealthSink` interface (`SetActive`/`SetPaused`/`Load`) | `internal/substrate/consumer_supervisor_spec.go:79-88` | **Frozen seam.** The shared sink implements it verbatim — unchanged. |
| Supervisor restore path (`restoreState` reads the sink's `Load` at startup) | `internal/substrate/consumer_supervisor_pump.go:383-410` | **Shipped.** Consumes `HealthStatus`+`PauseReason`; unchanged. |
| The three duplicate sinks | `internal/loom/health_sink.go`, `internal/weaver/health_sink.go`, `internal/bridge/health.go` | **Shipped, byte-/near-identical.** The consolidation target. |
| `consumerStateCache` (the in-memory metrics.consumers cache the sink also updates) | `internal/{loom,weaver}/health.go`, `internal/bridge/health.go` | **Shipped, byte-identical.** Moves with the sink (§4.2). |
| `Conn.KVGet`/`KVPut`/`KVDelete` + `ErrKeyNotFound` | `internal/substrate/kv.go` | **Exists.** The sink's only substrate calls; unchanged. |
| Sibling `internal/healthkv` `Reporter` (consumer-less heartbeat) | `vertical-app-health-self-report-design.md` (📐 awaiting-Andrew) | Parallel design; composes in the same package (§6). |

**Read path (P5):** untouched. The sink neither reads nor writes Core KV; it reads/writes the **Health KV** plane (a separate, sanctioned operational plane). No lens, no projection.
**Write path (P2):** untouched. Direct Health-KV writes are the sanctioned P2 exception (Contract #5 §5.7). No Processor op.
**Contract #1 key-shapes:** not involved — Health keys are their own addressing space (§5.1), not `vtx.`/`lnk.` shapes.

### 2.1 What is NOT in scope (and why — pre-empting "did you miss one?")

Two other components implement `substrate.HealthSink` but with a **different** shape — they are correctly left alone:

- **Processor** — has **no per-consumer sink at all** (corrected at ratification, 2026-07-02): `LaneSpecs`
  (`internal/processor/lanes.go:96-121`) builds the four per-lane `ConsumerSpec`s with the `Health` field
  **omitted**, so a Processor lane pause does not survive a restart and no `.consumer.<name>` doc is
  written; its `HealthHeartbeater` is the Contract-#5 heartbeat document (lane-backlog metrics + issues),
  a different job. Defensible posture for the platform's heart (a *persistently* paused `ops.urgent` lane
  could wedge emergency revocation; redelivery already retries) — but the per-lane-consumers row (🏗️)
  must own that posture explicitly. If it decides lanes should persist pause state, it adopts this shared
  `ConsumerSink` (one constructor call), never a fourth copy. Not assumed here (no dead scaffolding).
- **Refractor** (`internal/refractor/pipeline/supervisor_adapt.go`) — excluded for a **semantic** reason,
  not drift: its per-lens entry has a **third state, `rebuilding`**, and the adapter arbitrates the
  collision (a pause recovering **mid-rebuild** re-persists `rebuilding`, never a premature `active` —
  `supervisor_adapt.go:39-54`); the entry schema/bucket/key (the bare `ruleID`) differ and have existing
  readers (Loupe's lens table, the failure tiers). Forcing it into the two-state `ConsumerSink` would
  lose the rebuild arbitration or push rebuild-awareness into a primitive the other three don't need.
  Out of scope; not a copy.

Scope is therefore **exactly** the three byte-/near-identical copies. This is the bounded, honest set — not a speculative "unify every HealthSink."

---

## 3. Didn't we already handle this? (reconciliation with the mental model)

- *"Isn't this just Refractor's inherited health machinery?"* — No. Refractor's `health.Reporter` is a different, richer thing (rebuild/replay-aware). The three copies here are the **minimal** per-consumer pause-doc, deliberately "a SEPARATE, smaller shape from the Contract #5 heartbeat document" (the code's own words). They were hand-copied, not shared, when Loom/Weaver/Bridge each grew a supervisor.
- *"Does this duplicate the sibling `internal/healthkv` design?"* — No; it **complements** it. The sibling adds a consumer-less `Reporter`; this adds a consumer-bearing `ConsumerSink`. Same package, different types, both health-KV-plane. See §6.
- *"Does this introduce new state?"* — No. Zero new keys, zero new fields, zero new documents. The emitted `consumerHealthEntry` and the `.consumer.<name>` key are unchanged; only their *definition site* moves from three packages to one.
- *"Will consolidation change runtime behavior?"* — No. Each consumer keeps its own sink instance keyed by name and its own `consumerStateCache`; the only generalization is the hardcoded key prefix `"health.loom."`/`"health.weaver."`/`"health.bridge."` becomes `"health."+component+"."` (§4.1). Behavior is identical; the diff is a move + one parameter.

---

## 4. The shape

### 4.1 The shared sink API (`internal/healthkv`)

The three current constructors are `newConsumerHealthSink(conn, bucket, instance, name, states)` — with the component baked into the key literal. The shared version takes `component` as a parameter:

```go
// Package healthkv provides shared Health-KV-plane primitives for Lattice
// components: a Reporter for consumer-less daemons (see reporter.go) and a
// ConsumerSink implementing substrate.HealthSink for supervised consumers.
package healthkv

// ConsumerSink implements substrate.HealthSink for one supervised consumer.
// Each consumer gets its own sink instance keyed by name. Every supervisor
// transition is funnelled through this sink: it persists a minimal pause-state
// document to health.<component>.<instance>.consumer.<name> AND updates the
// caller's in-memory ConsumerStateCache, which the component's Contract #5
// heartbeater reads to populate metrics.consumers.
type ConsumerSink struct { /* conn, bucket, key, name, states */ }

// NewConsumerSink builds a per-consumer sink. component is the health-key
// namespace segment ("loom" | "weaver" | "bridge" | …); the key is
// "health." + component + "." + instance + ".consumer." + name.
func NewConsumerSink(conn *substrate.Conn, bucket, component, instance, name string, states *ConsumerStateCache) *ConsumerSink

func (s *ConsumerSink) SetActive(ctx context.Context) error                                   // substrate.HealthSink
func (s *ConsumerSink) SetPaused(ctx context.Context, reason substrate.PauseReason, lastErr string) error
func (s *ConsumerSink) Load(ctx context.Context) (substrate.HealthStatus, substrate.PauseReason, error)

// Delete removes the persisted pause-state entry + the in-memory cache entry
// for this consumer (called on supervisor.Remove, so a future re-add does not
// restore a stale pause). Bridge's single consumer never calls it — that is
// fine; the method is simply unused there.
func (s *ConsumerSink) Delete(ctx context.Context) error
```

The body is the current code verbatim (the `consumerHealthEntry` struct, `put`, and `pauseReasonFromString` become unexported package internals). The `substrate.HealthSink` interface is satisfied unchanged — `Load`'s contract ("a missing or malformed entry must resolve to `(StatusActive, "", nil)`") is preserved exactly.

### 4.2 The state cache (moves with the sink)

`consumerStateCache` is *also* byte-identical across the three, and the sink is coupled to it (`SetActive`/`SetPaused` call `states.set(name, consumerState(...))`; `Delete` calls `states.delete(name)`). So it moves into the same package, co-located with the sink so their mutual calls stay unexported:

```go
// ConsumerStateCache holds the last-known pause/active state of every managed
// consumer, mutex-guarded. The component's heartbeater reads Snapshot() to
// populate metrics.consumers (no supervisor re-query, no per-message KV scan).
type ConsumerStateCache struct { /* mu, states map[string]string */ }

func NewConsumerStateCache() *ConsumerStateCache
func (c *ConsumerStateCache) Snapshot() map[string]string   // read by the caller's heartbeater
// set/delete stay unexported: only the co-located ConsumerSink calls them.
```

`consumerState(paused, reason) string` (renders a pause reason to the `metrics.consumers` state string — `"running"`/`"pausedManual"`/…) is package-internal, called only by the sink.

**Exported surface** (what the three consumer packages call cross-package): `NewConsumerSink`, `(*ConsumerSink).Delete` (Loom/Weaver only), `NewConsumerStateCache`, `(*ConsumerStateCache).Snapshot`. The heartbeater call sites are exactly: `states.snapshot()` → `states.Snapshot()` (Loom `health.go:186` + `control.go:112`; Weaver `health.go`; Bridge `health.go:261`+). Everything else stays internal.

### 4.3 What each consumer keeps (the genuinely-different parts stay put)

The per-component **heartbeater** (`newHeartbeater`) is **not** consolidated — it legitimately differs (Weaver's takes `issues`/`source`/`marks`; Bridge's takes `issues`/`metrics`; Loom's is leaner). Each heartbeater keeps living in its own `health.go`; it just reads the shared `*healthkv.ConsumerStateCache` via `Snapshot()`. This mirrors the sibling design's discipline (§4.1 there): consolidate *only* the byte-identical part, never force-unify the parts that differ. No engine heartbeater's behavior changes.

---

## 5. Contract & config surface

### 5.1 Frozen contracts — no change

Contract #5 (`docs/contracts/05-health-kv.md`) defines the `health.<component>.<instance>` heartbeat key surface. The `.consumer.<name>` sub-key this sink writes is **not** that surface — it is a component-internal pause-state document (the code says so explicitly). It is emitted unchanged. **No `docs/contracts/*` edit is staged.**

### 5.2 Non-frozen schema doc — optional, deferred

`docs/observability/health-kv-schema.md` currently documents the per-component *heartbeat* keys. The `.consumer.<name>` sub-key is a pre-existing runtime key that predates this design and is arguably already-undocumented debt — **not introduced here**. Adding a row for it is a reasonable clean-up but is **out of scope** for this consolidation (it would be equally true without it); leave it to a doc fire or the sibling health-KV-schema pass. No edit in these fires.

### 5.3 Config — none

No new env vars, no new buckets, no cadence changes. The bucket (`HealthKVBucket`), instance id, and per-consumer names are passed through from each engine's existing config exactly as today.

---

## 6. Reconciliation with the sibling `internal/healthkv` design (detailed)

`internal/healthkv` today is a reserved namespace with only `completeness_test.go` (`package healthkv_test`, integration-tagged) — **no production code**. Two designs now want to add `package healthkv` production code:

| Design | Adds | Consumers | Concern |
|---|---|---|---|
| `vertical-app-health-self-report` (📐) | `Reporter` (heartbeat loop for consumer-less daemons) + `Snapshot`/`Issue`/`Probe` | `loftspace-app`, `clinic-app` | consumer-less liveness |
| **this** | `ConsumerSink` + `ConsumerStateCache` | `loom`, `weaver`, `bridge` | consumer pause-state restore |

**They do not collide:** distinct exported type names (`Reporter` vs `ConsumerSink`; the sibling's `Snapshot` is a heartbeat-payload struct, mine is a `ConsumerStateCache` method — no name clash), and both import only `internal/substrate` (no import cycle: `healthkv → substrate`; `loom/weaver/bridge → healthkv → substrate`). The package cohesion is *good* — `internal/healthkv` becomes "the shared Health-KV-plane library," which is exactly the brainstorm-#91 heartbeat-library end-state.

**Soft sequencing (not a hard dependency):** whichever design's build lands first creates `internal/healthkv/doc.go` (the `package healthkv` doc comment); the second adds its file(s) to the existing package. Neither blocks the other. If both are ratified, a natural order is: land the sibling's Fire 1 first (it establishes the package), then this design's Fire 1 adds to it — but the reverse works identically. I note this so the Steward doesn't trip on "who creates `package healthkv`."

---

## 7. Test strategy (the whole point — cover the round-trip once)

A single unit-test suite in `internal/healthkv` covers what is currently 0% in three places. Uses the existing in-memory NATS substrate test harness (`jsstore.Dir(t)` for StoreDir per the CI-parallelism rule) or a fake `*substrate.Conn` KV seam — whichever the touched packages already use:

- **Pause-restore round-trip (the filed gap):** `SetPaused(PauseStructural, "boom")` → a fresh `ConsumerSink.Load` returns `(StatusPaused, PauseStructural, nil)` and seeds the cache with `"pausedStructural"`. Repeat for `PauseInfra`, `PauseManual`.
- **Active round-trip:** `SetActive` → `Load` returns `(StatusActive, "", nil)`.
- **Missing entry:** `Load` on an absent key → `(StatusActive, "", nil)` + cache seeded `"running"` (the `ErrKeyNotFound` arm).
- **Malformed entry:** a non-JSON / wrong-shape value → `(StatusActive, "", nil)` (the unmarshal-error arm) — the exact defensive branch the contract mandates.
- **`pauseReasonFromString`:** every arm (`"manual"`→`PauseManual`, `"structural"`→`PauseStructural`, `"infra"`/`""`/unknown→`PauseInfra` default).
- **`consumerState`:** every arm (`running`, `pausedManual`, `pausedStructural`, `pausedInfra`, default).
- **`Delete`:** removes the KV entry (idempotent on `ErrKeyNotFound`) and the cache entry; a subsequent `Load` restores active (no stale pause after a re-add).
- **`ConsumerStateCache`:** `set`/`delete`/`Snapshot` concurrency-safe (a `-race` table test); `Snapshot` returns a copy (mutating it doesn't affect the cache).
- **Key namespacing:** `NewConsumerSink(…, "weaver", "inst-1", "tgt-x", …)` writes to `health.weaver.inst-1.consumer.tgt-x` (proves the `component` parameterization replaced the hardcoded prefix correctly).

**Per-consumer regression:** each engine package's existing test suite (`go test ./internal/loom/...`, `./internal/weaver/...`, `./internal/bridge/...`) must stay green after its rewire — proving the move is behavior-preserving. No new per-engine test is required (the round-trip now lives in `healthkv`), but the engines' existing supervisor/restart tests exercise the wiring.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, and the touched `go test` packages, per fire.

---

## 8. Risks & alternatives

- **Rejected — "just add a test in each package" (the minimal literal reading of the two items).** This leaves the ~180-LOC duplication in place, requires writing the *same* round-trip test two (really three) times, and leaves the Bridge copy bare. It treats the symptom (uncovered) while ignoring the cause (duplicated). The consolidation is barely more work and fixes both — and it's the move that composes with the sibling `internal/healthkv` direction Andrew is already weighing. This is the "simplest extension that fixes the root cause," not a clever new mechanism.
- **Rejected — unify *all* `substrate.HealthSink` implementers (incl. Processor + Refractor).** That is the greenfield-monolith blind spot: Processor's and Refractor's sinks are genuinely different (§2.1). Forcing them into one type would add conditionals and lose clarity. Scope stays at the three identical copies.
- **Risk — behavior drift during the move.** Mitigated: the body is copied verbatim; the *only* code change is `component` as a parameter (replacing three hardcoded key-prefix literals). The new shared test suite + each engine's existing green suite pin behavior. A `-race` test guards the cache.
- **Risk — import cycle.** None: `healthkv` imports only `substrate`; the three engines already import `substrate`, and adding an import of `healthkv` introduces no cycle (§6).
- **Risk — the Bridge's missing `delete`.** The shared `ConsumerSink` has a `Delete` method the Bridge never calls (its consumer isn't removed at runtime). That is a superset, not a conflict — an unused method is not a defect. If desired, the Bridge simply doesn't invoke it.
- **Risk — collision with the sibling design's `package healthkv`.** Addressed in §6 (distinct names, soft sequencing on `doc.go`).

---

## 9. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable and green; each realizes value (a component's restore round-trip becomes covered + a copy removed). Fires 2–3 are pure mirrors of Fire 1's consumer half.

- **Fire 1 — shared primitive + Loom (end-to-end proof).**
  Add `internal/healthkv` `ConsumerSink` + `ConsumerStateCache` (lift Loom's copy, generalize the key prefix to the `component` param) + the full unit-test suite (§7). Rewire **Loom** (`engine.go`, `health.go`, `control.go`) to `healthkv.NewConsumerSink`/`NewConsumerStateCache`/`Snapshot`; delete `internal/loom/health_sink.go` and the cache/`consumerState` block from `internal/loom/health.go`. Loom green. **Value:** the Loom pause-restore round-trip is now covered (resolves the *[Loom]* item); the shared primitive ships *with* its first real consumer (no dead scaffolding).
- **Fire 2 — Weaver (mirror).**
  Rewire **Weaver** (`engine.go`, `temporal.go`, `sweep_schedule.go`, `control.go`, `health.go`) to the shared primitive; delete `internal/weaver/health_sink.go` + the duplicated cache/`consumerState`. Weaver green. **Value:** resolves the *[Weaver]* item; second copy gone.
- **Fire 3 (bonus, optional) — Bridge.**
  Rewire **Bridge** (`internal/bridge/health.go`, `engine.go`) to the shared primitive; delete the third copy (it lacks `Delete` — simply don't wire it). Bridge green. **Value:** removes the third, unfiled duplicate; the primitive now has three consumers. Drop this fire if Andrew prefers to keep scope to the two filed items (§ For Andrew).

Fires 1–2 are the filed items; Fire 3 is the dividend the consolidation makes almost free.

---

## 10. Adversarial self-review (discharged 2026-07-01)

Proportionate to an XS–S, single-plane, no-contract, no-security consolidation — a focused adversarial pass, not a full 3-layer/party review (that would be ceremony for a byte-identical code move). Findings folded in:

- **"Is this scope-inflation over the board's 'add coverage' framing?"** — Considered and rejected as the *cause-not-symptom* fix (§1.2, §8): testing duplicated code in place writes the same suite twice and leaves a third copy bare. The consolidation is the smaller total change and composes with the sibling `internal/healthkv` design.
- **"Are the copies *really* identical, or is there a hidden behavioral difference I'd erase?"** — Verified by normalized diff: Loom↔Weaver are byte-identical modulo package/prefix/comment; Bridge is the same minus `delete`. The only intended code change is the `component` parameter. The shared test + each engine's existing green suite pin behavior.
- **"Did I miss a fourth copy?"** — Repo-wide grep for `consumerHealthEntry`/`pauseReasonFromString` returns exactly three files. Processor and Refractor implement `substrate.HealthSink` differently and are explicitly out of scope (§2.1) — not silently unified.
- **"Does it collide with the parallel in-flight design?"** — Checked the other 📐/🏗️ `internal/healthkv` design directly (the §2/§6 reconciliation): different type, same package, no cycle, soft `doc.go` sequencing. The simpler-of-two check passes — they're complementary, not redundant.
- **"Any retraction / over-grant hazard (the security-plane reflex)?"** — None: this is operational Health-KV state, not a grant/capability projection. `Delete` on `supervisor.Remove` already prevents stale-pause restoration; unchanged.
- **"Import cycle / exported-surface leakage?"** — `healthkv → substrate` only; exported surface is minimal (constructor, `Delete`, `NewConsumerStateCache`, `Snapshot`); sink↔cache internals stay unexported by co-locating them.

---

## 11. Definition of done

- `internal/healthkv.ConsumerSink` + `ConsumerStateCache` exist, implement `substrate.HealthSink` unchanged, and are unit-tested for the full pause-restore round-trip (paused/active/missing/malformed restore, `pauseReasonFromString`, `consumerState`, `Delete`, cache concurrency, key namespacing) — the currently-0% branch is covered once.
- `internal/loom/health_sink.go` and `internal/weaver/health_sink.go` (and, if Fire 3 lands, the Bridge copy) are deleted; each engine wires the shared primitive; each package's existing suite stays green.
- No frozen-contract change; no new keys/fields/config; no runtime-behavior change; all gates green.
