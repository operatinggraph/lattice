# Lens projection liveness — per-lens projection lag + freshness, for every lens (not just auth-plane)

**Status: ✅ Andrew-ratified (2026-07-02) — `LensProjectionStalled` ships OFF (the §5.3 call); fires
collapsed to ONE in this lane (instrument + backstop) per the fewer-larger rule; the freshness UI
rides the Loupe lane's F5 (§8).** Ratification condition answered in §5.7: single-instance Refractor
is the deployed topology (HA multi-instance is a shelved design with no driver); under a future
fan-out the alert core (NumPending on the lens's durable) is instance-agnostic and stays correct,
while the per-lens Entry (bare-ruleID key) and the heartbeat `lensLiveness` map need the HA design's
lens-ownership rule — recorded in §5.6 as an explicit inherited seam.
**Author:** Winston (Designer fire, 2026-06-30)
**Backlog:** Stream-2 Read-model / projection maturity — *[Refractor/Loupe] Silent lens-projection stall is undetectable* (★★, M; clinic-PO-filed 2026-06-30)
**Owning components:** `internal/refractor/{pipeline,health}` (signal production), `cmd/refractor/main.go` (wiring), `cmd/loupe/{health.go,systemmap.go,web/app.js}` (display). Docs: `docs/components/refractor.md` + `docs/observability/health-kv-schema.md`.

---

## For Andrew

**What it does (two lines).** Today a Refractor lens can stop projecting — its read model silently diverges from Core KV — while the Refractor self-reports `green`, every lens `active`, and Loupe shows the lens `freshness` as `-`. This design gives **every** lens (not just the auth-plane capability lenses) a real projection-liveness signal: a per-lens `lastProjectedAt` + projection lag emitted into Health KV, a generalized threshold→issue→status-degrade backstop so a stalled **business** lens degrades the Refractor heartbeat, and a populated Loupe `freshness` column.

**Architectural fork:** **none.** This is entirely within the already-sanctioned Health-KV direct-write plane (architecture P2 exception) and the existing per-lens health/heartbeat machinery. No new primitive, no new bucket, no new op, no read-path/lens change, no Core-KV read (the lag is computed from JetStream consumer metadata + in-process counters the pipeline already holds).

**Frozen-contract change:** **none required.** Contract #5 §5.4 already *recommends* `cdc_lag_p99_ms_by_lens` as "the architecture's primary liveness indicator" and marks all Refractor metrics "recommended (not enforced)… component-author's discretion"; §5.5 says issue codes are "component-defined." This design finally *implements the spirit of* §5.4's liveness indicator (the code drifted to emitting `consumerLag`/NumPending + a latency ring instead) and adds component-defined issue codes — both build-to, no `docs/contracts/*` edit. The key-level detail lands in the **non-frozen** schema doc `docs/observability/health-kv-schema.md`, the sanctioned authority for per-key shape.

**The one judgment call for you (not a fork).** The pure *silent-divergence* failure mode — a consumer that is **caught up** (lag 0) yet acks-and-no-ops every event so nothing reaches the target — cannot be turned into a clean auto-alert without a model of *expected* output (alerting on `lastProjectedAt` age alone false-positives on a genuinely quiet lens). I resolve this honestly: the generalized **lag** backstop (Fire 2) auto-alerts the *wedged-consumer* case (the far more likely cause of "stopped reaching **every** clinic read model" — a shared delivery wedge, not 30 simultaneous apply bugs), and `lastProjectedAt` is surfaced as an operator-visible **freshness** signal (Fire 1+3) that makes the silent-divergence case obvious on a busy stack ("last projected 22m ago" on a lens whose stream is moving). Fully *closing* silent-divergence with automatic remediation is the deferred **closed-loop Weaver auditor** (brainstorm #96) / FR54 anomaly tier — explicitly out of scope, and this design is its prerequisite (it gives that auditor honest per-lens liveness to act on). If you'd want the stronger combined auto-alert (lag-sustained **AND** `lastProjectedAt` not advancing despite acks), it's a one-rule addition called out in §5.3 — flagged for your call, defaulted **off** to avoid flapping.

---

## 1. Problem & intent

### 1.1 The grounded symptom (clinic PO, 2026-06-30)

On a long-up dev stack the clinic PO observed: committed ops reached `green` at the Processor, Core KV updated, `core-events` published — but **every** clinic read model stopped updating. Meanwhile the Refractor self-reported `green`, each lens `active`, and Loupe's lens `freshness` column showed `-`. The read models silently diverged from Core KV **with no operator or Lamplighter signal**. This is the worst class of observability failure: a correctness divergence that the health plane renders as healthy.

### 1.2 Why every existing signal missed it

The Refractor already emits a rich per-lens health surface. Each one fails to catch a stall, by construction:

| Existing signal | Where | Why it reads healthy through a stall |
|---|---|---|
| Per-lens `status` (`active`/`paused`/`rebuilding`) | `health.Reporter` Entry (keyed by ruleID) | A wedged consumer is still **`active`** — status only flips on an explicit pause/rebuild lifecycle transition, never on "stopped making progress." |
| `consumerLag` (= NumPending) | `health.LagPoller` → `Reporter.SetConsumerLag` every 5s | Reads **0** when the consumer ack-and-skips (most CDC events don't match a given lens's filter — the consumer stays "caught up" while nothing is projected). And if the `LagPoller` goroutine itself stalls, the field goes **stale-zero** — Loupe still renders the last value. |
| Per-lens latency p95/p99 | `pipeline.LatencyRingBuffer` (128-sample window) → heartbeat `metrics.lensLatency` | A ring buffer of **past** projection latencies. When nothing projects, the old samples persist; p95/p99 look healthy indefinitely (a window, not a freshness clock). |
| `metrics.lensLags` (per-lens NumPending) | `LatticeHeartbeater.LagProvider`, every beat | The data is emitted — but **nothing evaluates it** for a business lens. No threshold, no issue, no status change. |
| **Capability-lens liveness alert** (paused→error, lag→warning, debounced) | `LatticeHeartbeater.CapabilityLensProvider` → `evalCapabilityLenses` | This is the *only* path that turns a lag/pause into a Health-KV **issue** + a degraded Refractor **status** — and it is gated to **`entry.authPlane`** lenses (`cmd/refractor/main.go:168-201`). Clinic/loftspace business lenses are **never evaluated**. |
| Loupe lens row | `cmd/loupe/health.go:259-292` | Renders `consumerLag>0 → yellow` and `errorCount>0 → yellow`, but hardcodes lens **`Freshness: "-"`** (line 260) and reacts only to a `consumerLag` that (per row 2) didn't fire. |

The architecture's stated "primary liveness indicator" — Contract #5 §5.4's `cdc_lag_p99_ms_by_lens` — was **never actually implemented**; the code substituted `consumerLag` (NumPending) and a latency ring. Neither answers the operator's real question: *"is this lens still making forward progress, and how stale is its read model?"*

### 1.3 Intent

Give **every** lens a projection-liveness signal that (a) **survives** a caught-up-but-not-projecting stall, (b) **auto-degrades** the Refractor heartbeat on the wedged-consumer case so the Lamplighter and Loupe surface it, and (c) gives the operator a real **freshness** number per lens. Reuse the well-tuned capability-lens backstop machinery rather than inventing a parallel one. Tie-in to the vision: brainstorm **#96 — "Closed-loop Weaver auditor (reads Health-KV, issues remediation Nudges)"** and FR54 anomaly detection both *consume* Health-KV liveness; today they have nothing honest to read for business lenses. This is their substrate.

---

## 2. Grounding — the pattern we mirror

This design is a **generalization of an existing, shipped pattern**, not a greenfield mechanism. The capability-lens liveness backstop (`refractor.md` §"Capability-Lens health") already does, for auth-plane lenses, *exactly* what we need for all lenses:

- `LatticeHeartbeater.CapabilityLensProvider` returns `[]CapabilityLensStatus{CanonicalName, Status, PauseReason, ConsumerLag}` each beat (read-only: status from the lens `Reporter`, lag from the supervised consumer's NumPending).
- `evalCapabilityLenses` applies a threshold with **hysteresis** (`evalLagHysteresis`: raise-after-N-cycles, lower clear-band) so a one-cycle spike doesn't flap, reconciles open issues so `since` persists (Contract #5 §5.5), and degrades the heartbeat `status` (paused→`unhealthy`, lagging→`degraded`).
- Loupe's `componentLiveness` (`cmd/loupe/health.go:167`) already **fuses** heartbeat freshness + §5.4 status + worst §5.5 issue severity for component cards/system-map nodes.

We mirror this machinery verbatim for the general case, adding only the *new datum* it lacks (a `lastProjectedAt` progress clock) and *widening the population* from auth-plane to all active lenses.

**Invariants honored:**
- **P2 (Processor is the sole Core-KV writer).** This design writes **only Health KV** (the sanctioned direct-write plane, Contract #5 §5.1 / architecture P2) and in-process state. No op, no Core-KV write.
- **P5 (apps read lens projections, never Core KV).** Loupe is the explicit inspector exception and already reads Health KV directly; the new freshness/issue data is read from the same Health-KV entries Loupe already consumes. No new Core-KV read; no app gains a Core-KV dependency.
- **No new engine Core-KV read** ([[feedback_no_new_engine_corekv_reads]]). The projection lag is computed from JetStream consumer metadata (`supervisor.PendingForConsumer`, already used) + in-process counters the pipeline already advances per message. Nothing reads Core KV.
- **Contract #1 key shapes:** unaffected — no new vertices/aspects/links. Health-KV keys are a separate addressing space (Contract #5 §5.1) and unchanged in shape.

---

## 3. The shape

### 3.1 New per-lens progress state (in the pipeline)

The pipeline already handles every CDC event in `Pipeline.handle` and writes results in `writeResults` (`pipeline.go:472, 649`). Add two in-process, atomically-updated fields to `Pipeline`:

- **`lastAppliedSeq uint64`** — set to `msg.Sequence` on **every** acked event (vertex/aspect/link, including ack-and-skip). This is "the consumer's forward cursor": it advances whenever the lens consumes anything, so a wedged consumer (delivering nothing) leaves it frozen.
- **`lastProjectedAt time.Time`** — set to "now" only when `writeResults` performs a **successful adapter write** (a real Create/Update/Delete reaching the target). This is "the read-model's last touch": it advances only on actual output, so a caught-up-but-no-op consumer leaves it frozen even as `lastAppliedSeq` moves.

Both are updated under the pipeline's existing mutex discipline (a tiny `sync.Mutex`, or `atomic` for the seq + a mutex for the time). Exposed via a single accessor:

```go
// ProjectionProgress is the lens's forward-progress snapshot for the health plane.
type ProjectionProgress struct {
    LastAppliedSeq  uint64    // stream seq of the last event this consumer acked (incl. skips)
    LastProjectedAt time.Time // wall-clock of the last successful target write; zero if none yet
}
func (p *Pipeline) Progress() ProjectionProgress { ... }
```

The two fields disambiguate the failure modes:

| `lastAppliedSeq` advancing? | `lastProjectedAt` advancing? | Interpretation |
|---|---|---|
| yes | yes | healthy |
| **no** (frozen) + NumPending climbing | no | **wedged consumer** (Scenario 1 — the likely clinic cause) → auto-alert (Fire 2) |
| yes | **no** (despite matching activity) | **silent divergence** (Scenario 2 — acked-but-no-output) → freshness-visible (Fire 1+3); optional combined alert (§5.3) |
| no | no, NumPending 0 | genuinely **quiet** lens (no inbound matches) → healthy; *not* an alert |

### 3.2 Extend the per-lens health Entry (the Reporter)

`health.Entry` (keyed by ruleID, `reporter.go:28`) gains two additive fields (both `omitempty`-friendly for forward-compat with the unchanged Loupe path during rollout):

```go
LastProjectedAt string `json:"lastProjectedAt,omitempty"` // RFC3339 UTC; "" until first projection
ProjectionLag   uint64 `json:"projectionLag"`             // events behind = NumPending (alias of consumerLag, named for the operator)
```

`ProjectionLag` is the same NumPending the `LagPoller` already reads; we keep `consumerLag` (back-compat) and add `projectionLag` as the operator-facing name. `LastProjectedAt` is written by a new `Reporter.SetProjectionProgress(ctx, progress)` call the `LagPoller` makes on the same 5s cycle it already runs (it already calls `SetConsumerLag` there — one extra field in the same read-modify-write, no new goroutine, no new write).

> **Why fold it into the LagPoller cycle, not a per-message write?** Writing Health KV on every projection would be a write amplification disaster (one health PUT per CDC event). The LagPoller's existing 5s cadence is the right granularity for a freshness signal whose consumer (operator/Lamplighter/Loupe) polls at human/10s scale. `lastProjectedAt` is read from the pipeline's in-process `Progress()` — always current — and *flushed* every 5s.

### 3.3 Generalize the heartbeat liveness backstop (the instance heartbeat)

This is the core fix. Today `cmd/refractor/main.go` wires `CapabilityLensProvider` filtered to `entry.authPlane`. Add a sibling **`LensProvider`** (or widen the existing one — see §5.1 for why a *sibling* is chosen) that returns a liveness snapshot for **all active non-auth-plane lenses**, and a `LatticeHeartbeater.evalLenses` that mirrors `evalCapabilityLenses`:

```go
// LensLivenessStatus — one business lens's liveness snapshot (mirror of CapabilityLensStatus
// + the progress clock). authPlane lenses are excluded (the cap-lens path owns them).
type LensLivenessStatus struct {
    CanonicalName   string
    RuleID          string
    Status          string    // active | paused | rebuilding
    PauseReason     string
    ProjectionLag   uint64    // NumPending
    LastProjectedAt time.Time // zero if never projected
}
```

`evalLenses` reuses the *same* `evalLagHysteresis` helper (already general — it's keyed by lens name, not auth-plane-specific) and the *same* `reconcileCapIssues`-style open-issue reconciliation, emitting:

- **`LensProjectionLagging`** (`severity: warning` ⇒ `degraded`) — an `active` lens whose `ProjectionLag` stays over threshold for N consecutive beats (debounced exactly like the cap path; default threshold 100, raise-cycles 3, clear-band, all deployment-overridable). The wedged-consumer auto-alert.
- **`LensProjectionPaused`** (`severity: warning` ⇒ `degraded`) — an `active`-lifecycle business lens that is actually `paused`. **Note the severity choice:** the cap path raises **`error`/`unhealthy`** for a paused lens because the authorization read-model is platform-critical; a single frozen *business* lens is a real outage **for that vertical** but should not nuke the whole Refractor to `unhealthy` (it would mask other components and over-page). So business-lens paused = **`degraded`** (warning). Stated explicitly so it's an obvious tuning knob, not a buried default.
- The per-lens `metrics.lensLiveness.<canonicalName>` sub-map `{status, projectionLag, lastProjectedAt, alert}` is emitted **every** beat (including `alert:"ok"`) so Loupe/observers can render the green state and the freshness clock, not only anomalies — same convention as `metrics.capabilityLens`.

The optional combined **`LensProjectionStalled`** rule (lag sustained AND `lastProjectedAt` not advancing) is specified in §5.3, defaulted **off**.

### 3.4 Loupe — populate the freshness column + surface the issue (Fire 3)

Two read-only display changes, both within Loupe's existing inspector role (reads Health KV; P5 exception):

1. **Lens freshness column.** `cmd/loupe/health.go:259-292` (`kindLens`) replaces the hardcoded `Freshness: "-"` with `freshness(lastProjectedAt)` parsed from the lens Entry's new `lastProjectedAt` (falling back to `lastUpdated`, then `-` when neither is present — graceful for pre-Fire-1 entries). The existing `consumerLag>0 → yellow` / `errorCount>0 → yellow` rules stay; add `projectionLag` as the preferred field name with `consumerLag` fallback.
2. **Issue surfacing.** `componentLiveness` already fuses the worst §5.5 issue severity into the **refractor component card** and **system-map node** — so once Refractor emits `LensProjectionLagging`/`LensProjectionPaused` in its heartbeat `issues[]` (Fire 2), Loupe surfaces it **with zero Loupe change** (the existing fusion path). Fire 3 additionally renders the per-lens `metrics.lensLiveness` freshness on the lens rows. No Loupe-side change is needed for the *alert* to appear — only for the per-lens freshness column.

---

## 4. Contract surface — exactly what changes where

| Surface | Change? | Detail |
|---|---|---|
| `docs/contracts/05-health-kv.md` §5.4 (Refractor metrics) | **build-to, no edit** | §5.4 already recommends `cdc_lag_p99_ms_by_lens` + marks metrics "component-author's discretion." We add `lensLiveness`/`projectionLag`/`lastProjectedAt` under that latitude. (If Andrew later wants the new metric *named* in §5.4 as the canonical liveness indicator, that's a one-line ratified addition — but it is **not required** to build, so no edit is staged.) |
| `docs/contracts/05-health-kv.md` §5.5 (issue codes) | **build-to, no edit** | §5.5: "Machine-readable code (PascalCase). **Component-defined.**" `LensProjectionLagging`/`LensProjectionPaused`/`LensProjectionStalled` are component-defined, like the existing `CapabilityLensPaused`/`CapabilityLensLagging`. |
| `docs/observability/health-kv-schema.md` (non-frozen) | **edit (Fire 1)** | Document the new per-lens Entry fields (`lastProjectedAt`, `projectionLag`) + the heartbeat `metrics.lensLiveness` sub-map + the new issue codes. This non-frozen schema doc is the sanctioned authority for per-key detail. |
| `docs/components/refractor.md` | **edit (Fires 1–2)** | Add a "Per-lens projection liveness (all lenses)" row to the health table mirroring the Capability-Lens row; note that the backstop now covers business lenses. |

**No frozen-contract edit is staged** (nothing in `docs/contracts/*` is left uncommitted), because nothing in the frozen surface needs to change. This is conformance-plus-completeness within §5's explicitly soft, component-author-discretion latitude.

---

## 5. Decisions resolved (decide-don't-defer) + the adversarial pass

I ran a focused adversarial lens (the three review hats) over this design before flagging it. Findings folded in:

### 5.1 Sibling provider vs. widening the existing one — **sibling `LensProvider`, auth-plane excluded**
Widening `CapabilityLensProvider` to all lenses would double-issue the auth-plane lenses (they'd be evaluated by both the cap path's sharp `error`-severity rule *and* the new general rule) and risk regressing the well-tuned, security-critical cap path. **Decision:** add a **sibling** provider/eval scoped to **non-auth-plane** lenses; the cap path is untouched (zero regression surface on the security plane). A future unification (one path, auth-plane just selects sharper severities) is noted as an *optional* cleanup, **not** in scope — the unattended-green bar favors additive.

### 5.2 False positive on a genuinely quiet lens — **resolved by the two-clock model**
A lens with no matching inbound changes has a naturally-old `lastProjectedAt`. Alerting on `lastProjectedAt` age alone would false-positive. **Decision:** the *auto-alert* (Fire 2) triggers on **`ProjectionLag` (NumPending) over threshold** — which is 0 for a quiet lens (nothing is behind) — never on `lastProjectedAt` age. `lastProjectedAt` is a **display/freshness** signal only (Fire 1+3), never an alert input on its own. A quiet lens stays green.

### 5.3 The silent-divergence (lag-0, acked-but-no-output) case — **freshness-visible now; optional combined alert flagged**
This is the residual Scenario-2 case the lag alert can't see. **Decision:** surface it via the freshness clock (operator sees a moving stream but a frozen `lastProjectedAt`) rather than auto-alert, because a clean auto-alert needs an *expected-output* model this layer doesn't have. The optional combined rule — **`LensProjectionStalled`** (`severity: warning`): `lastAppliedSeq` advanced ≥K beats AND `lastProjectedAt` frozen the whole time AND the lens isn't `rebuilding` — is specified here but defaulted **off** (a stricter config flag `LensStallDetect`), because "acked but produced no row" is *legitimately normal* for a filtering lens (most events don't match). Turning it on safely needs per-lens knowledge of "did any matching event arrive," which is the FR54 anomaly tier's job. **Flagged for Andrew:** ship with it off (my recommendation) vs. on.

### 5.4 Rebuild / startup transients — **excluded, mirroring the cap path**
A `rebuilding` lens legitimately has high lag and no recent projection; the `evalLagHysteresis`/`resetLagState` path already excludes non-`active` states. A freshly-activated lens has a zero `lastProjectedAt` until its first write — Loupe renders `-` (not a false "stale"), and the lag debounce (raise-after-N) rides out the warm-up. No new handling needed.

### 5.5 `LagPoller`-stall blind spot — **partially closed, honestly bounded**
If the `LagPoller` goroutine itself dies, the per-lens Entry stops updating — today invisible. The **instance heartbeat** path (§3.3) reads `Progress()` and `Pending()` **live** every beat (independent of the LagPoller), so the *backstop alert* survives a LagPoller death. The per-lens *Entry* freshness would still stale; Fire 1 makes that staleness **visible** in Loupe via the Entry's `lastUpdated` age (a stale lens Entry now renders as stale rather than green). A dedicated LagPoller-liveness watchdog is out of scope (diminishing returns; the heartbeat backstop already covers the operhe-facing alert).

### 5.6 Multi-instance Refractor (Andrew's ratification question, 2026-07-02) — **not a concern today; one seam inherited by the HA design**
Single-instance Refractor is the deployed and designed-for topology (the HA-NATS clustering design —
"clustering + multi-instance engine fan-out" — is ✅ ratified but 🗄️ shelved with no prod driver).
Under a future multi-instance fan-out: **the alert core is instance-agnostic** — `ProjectionLag` is
NumPending on the lens's durable consumer, a consumer-level metric that reads identically from any
instance, so the wedge alert stays correct under any topology. Two **display** seams depend on the HA
design's lens-ownership rule: (a) the per-lens `health.Entry` is keyed by bare ruleID (deliberately
lens-scoped, not instance-scoped) — with work-shared durables, two instances' `lastProjectedAt`
flushes would last-writer-wins each other (each instance only observes its own writes; the clock could
regress). One-writer-per-lens (the owning instance) resolves it, and is the shape HA's fan-out
partitioning already implies; a max-merge or per-instance sub-keys are the fallbacks. (b) each
instance's heartbeat `metrics.lensLiveness` covers only the lenses its registry hosts — correct under
partitioning by construction. **Inherited seam recorded for the HA design; nothing to build here.**

### 5.7 Threshold defaults — **inherit the cap path's, deployment-overridable**
Threshold 100 events, raise-cycles 3 (≈30s sustained at the 10s floor), clear-band = raise (overridable). These are the cap path's battle-tested defaults; reusing them avoids a fresh tuning exercise and keeps one mental model. All overridable via the heartbeater fields (mirroring `CapabilityLensLag*`).

---

## 6. Migration & test strategy

**Migration:** purely additive. New Entry fields are `omitempty`/back-compat; the old Loupe path renders pre-Fire-1 entries unchanged (no `lastProjectedAt` → falls back to `-`). No data migration, no bucket change, no op. Each fire is independently deployable and the heartbeat/Entry shape only *grows*.

**Tests (per fire):**
- **Fire 1:** `pipeline` unit — `lastAppliedSeq` advances on ack-and-skip events AND on projected events; `lastProjectedAt` advances **only** on a successful adapter write, stays frozen on ack-and-skip and on a write error. `Reporter` unit — `SetProjectionProgress` round-trips `lastProjectedAt`/`projectionLag` and preserves `errorCount`/`consumerLag` (the existing read-modify-write invariant). Heartbeater — `metrics.lensLiveness` sub-map emitted for active lenses with a non-zero progress.
- **Fire 2:** Heartbeater unit (mirror `caplens_alert_test.go`) — a business lens over threshold for N beats raises `LensProjectionLagging`/degrades status; a one-beat spike does **not** (hysteresis); a paused business lens raises `LensProjectionPaused`/degraded (not unhealthy); an auth-plane lens is **not** double-issued by the general path; a quiet (lag-0) lens stays `ok`; `since` persists across beats and the issue drops on resolve.
- **Fire 3:** Loupe `health_test.go` — `kindLens` renders `freshness` from `lastProjectedAt` (and `lastUpdated` fallback, and `-` when absent); `projectionLag>0 → yellow`; the refractor component card surfaces a `LensProjectionLagging` issue via the existing `componentLiveness` fusion (assert no Loupe-logic change was needed for the alert).
- **Integration (optional, post-Fire-2):** an ephemeral-stack e2e that pauses a clinic lens's consumer and asserts the Refractor heartbeat degrades + the issue appears — mirrors the convergence-suite style; gated like the other Postgres/ephemeral e2es (out-of-band, not a CI blocker).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, the `internal/refractor/...` + `cmd/loupe` `go test` packages. No kernel/bypass/capability-adversarial impact (no auth-plane or write-path change — the cap path is deliberately untouched, §5.1).

---

## 7. Risks & alternatives

| Risk / alternative | Disposition |
|---|---|
| **Double-issuing auth-plane lenses** | Avoided by scoping the general path to non-auth-plane lenses (§5.1). The cap path stays canonical for capability lenses. |
| **Write amplification** if `lastProjectedAt` were written per projection | Avoided — flushed on the existing 5s LagPoller cycle from in-process `Progress()` (§3.2). Zero new writes/goroutines. |
| **Flapping** on bursty lenses | The same hysteresis (raise-after-N + clear-band) that tamed the cap path; reused verbatim (§5.7). |
| **False positive on quiet lenses** | The auto-alert keys on NumPending (0 for quiet), never `lastProjectedAt` age (§5.2). |
| **Alt: a separate "projection-stall" Weaver convergence target** (on-platform, not health-plane) | Rejected for now — that's the deferred closed-loop auditor (#96 / FR54). It needs an honest per-lens liveness *signal* to converge on, which is exactly what this design produces. Health-plane first, on-platform remediation later. This design is its prerequisite, not its competitor. |
| **Alt: implement §5.4's `cdc_lag_p99_ms_ by_lens` literally (a time-lag, not seq-lag)** | A wall-clock CDC-lag (now − event-commit-time) is a strictly *richer* metric but needs the committing op's timestamp threaded through the CDC payload to every lens and is sensitive to clock skew. The seq-lag (NumPending) + `lastProjectedAt` pair answers the operator's question ("behind by how many / how stale") with data already in hand. A true time-lag is a clean follow-on once a need is shown; not required to close this gap. |

---

## 8. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable, independently valuable, and lands green. Build only after **✅ Andrew-ratified**.

- **Fire 1 — Per-lens projection-progress instrumentation (emit the signal).**
  `Pipeline` tracks `lastAppliedSeq` (every ack) + `lastProjectedAt` (every successful write) + `Progress()`; `health.Entry` gains `lastProjectedAt`/`projectionLag`; `LagPoller.poll` flushes them via `Reporter.SetProjectionProgress` on its existing 5s cycle; the heartbeater emits `metrics.lensLiveness.<name>` for all active lenses. Schema doc updated. **Value alone:** the freshness + lag data is now in raw Health KV / the Loupe corekv inspector for every lens (an operator can read it), even before any alert. *Green: additive emission + pipeline/reporter/heartbeater unit tests.*

- **Fire 2 — Generalized liveness backstop (auto-alert the wedge).**
  Add the sibling `LensProvider` (non-auth-plane) in `cmd/refractor/main.go` + `LatticeHeartbeater.evalLenses` reusing `evalLagHysteresis`/issue-reconciliation; emit `LensProjectionLagging` (lag) + `LensProjectionPaused` (paused) → degrade the Refractor heartbeat `status`. refractor.md health-table row added. **Value alone:** a stalled/wedged business lens now degrades the Refractor heartbeat → the Lamplighter classifies it and Loupe's component card/system-map node goes yellow **via the existing `componentLiveness` fusion** (no Loupe change). *Green: heartbeater unit tests mirroring `caplens_alert_test.go`; cap path untouched.*

- **Fire 3 — MOVED TO THE LOUPE LANE (2026-07-02, Loupe 2.0 reconciliation).** `cmd/loupe/**` is
  Stream 3's territory and the Loupe 2.0 program already lists this exact feed on its board ("lens
  freshness (F5's slot) ← lattice.md silent lens-projection stall"): **F5's Lens page owns the
  freshness rendering.** This design's contract with Loupe is the Health-KV data alone — the per-lens
  Entry fields + the heartbeat `metrics.lensLiveness` sub-map (and the component-card alert already
  surfaces with zero Loupe change via the existing `componentLiveness` fusion). No `cmd/loupe` edit
  ships from this lane.

- **Fire 4 (optional, flagged §5.3) — `LensProjectionStalled` combined rule, defaulted off** behind a `LensStallDetect` config flag. Only build if Andrew opts in (§5.3). *Green: heartbeater unit test for the combined predicate.*

**Fire collapse (2026-07-02, Andrew's fewer-larger-fires rule):** Fires 1 + 2 ship as **ONE fire**
(instrument + backstop — same plane, `internal/refractor` + `cmd/refractor` wiring, and the backstop
is the instrument's consumer; coupled-ships-together). Net: **one fire in this lane**; the freshness
column rides the Loupe lane's F5; Fire 4 stays an Andrew-gated option.

---

## 9. Summary for the board

A purely-additive, no-contract-change, no-fork observability design that closes a live-observed correctness-visibility gap: every lens (not just auth-plane) gets a projection-liveness signal — a `lastProjectedAt` freshness clock + a generalized lag→issue→status-degrade backstop reusing the shipped capability-lens machinery — so a silently-stalled clinic/loftspace read model degrades the Refractor heartbeat and surfaces in Loupe. Prerequisite for the deferred closed-loop Weaver auditor (#96) / FR54. 3 fires (+1 optional, Andrew-gated). Awaiting Andrew's ratification + the one §5.3 judgment call (ship `LensProjectionStalled` off vs. on).

---

## 10. Fire 5 — the business-lens CONVERGENCE SWEEP (build note / fire brief)

Fires 1+2 gave a business lens a liveness *signal*. It still has no **healer**: a stale or missing
`actorAggregate` row converges only when a future CDC event happens to touch that actor. This fire
closes that, and reports what the healer finds.

**Scope sentence (verbatim, from the board row).** *"Adding a walk to an actorAggregate lens reprojects
nothing already stored — rows refresh only when a CDC event next touches that actor. Only auth-plane
lenses get the convergence sweep; every other actorAggregate has no healer."* Consumers:
`identityAnchors` (whoami hats) + `myTasks`.

**Grounded mechanism (verified `file:line`, not assumed).**

- The sweep is already **fully generic** — `SweepPlan` needs only `AnchorType` / `BuildKey` /
  `AnchorFromKey`, all three read off the `OutputDescriptor` every actorAggregate lens compiles
  (`internal/refractor/projection/driver.go:181`). Nothing in `Sweeper` knows about the auth plane.
- The **only** thing withholding it from a business lens is the `if authPlane` gate at
  `projection/driver.go:180`.
- The sweep is a genuine content healer, not just a key-presence check: direction 3 of
  `Sweeper.candidates` (`pipeline/sweep.go:586`) is a bounded round-robin **deep verify** that
  re-executes the projection — "the only detector for a row that is present but stale". That is
  precisely the after-a-walk-was-added case the row names.
- A **MATCH change** hot-reloads the compiled rule (`cmd/refractor/main.go:881` `UseFullEngine`) but
  triggers **no** rebuild, though `lens.MatchChange` is documented as "a full rebuild is required"
  (`lens/update.go:11`). Auth-plane lenses survive this because the deep verify converges them over
  the following passes; business lenses have nothing.
- `survey`'s KeyLister requirement (`pipeline/sweep.go:468`) is satisfied by **both** shipped
  adapters (`adapter/natskv.go:18`, `adapter/postgres.go:17`), so generalizing does not strand a target.

**Decisions taken here as Winston (impl-level, recorded not parked).**

1. **Generalize the sweep; do not auto-rebuild on MatchChange.** A rebuild is a full stream replay and
   is deliberately operator-initiated + async; firing one on every lens edit is a replay storm. The
   round-robin deep verify is the already-designed, bounded convergence mechanism — use it.
2. **Gate the install on the adapter being a `KeyLister`,** rather than letting a non-lister adapter
   fault the sweep on every tick forever. Auth-plane adapters always qualify, so the auth plane sees
   **zero behavior change** — the new gate is strictly a widening.
3. **Mirror, don't merge, the health path.** `CapabilityLensProvider` stays canonical for auth-plane
   lenses (§5.1). The business-lens sweep verdicts surface through the sibling `LensProvider` under
   general-lens issue names.

**Touch-list.**

- `internal/refractor/projection/driver.go:180` — widen the `SweepPlan` install.
- `internal/refractor/pipeline/sweep.go:468` — the "every auth-plane target is NATS-KV" comment is no
  longer the reason the branch is unreachable; re-state it truthfully.
- `internal/refractor/health/lattice_heartbeater.go` — `LensLivenessStatus` gains `Unreadable` + the
  sweep verdict fields; `evalLenses` raises the new issues.
- `cmd/refractor/main.go:375` — `LensProvider` stops `continue`-ing past an unreadable lens and reads
  the sweeper, mirroring `CapabilityLensProvider` (`main.go:328`, `main.go:345`).

**Increment order + green checks.**

- **Inc 1 — the healer.** Widen the install gate. Green: `go test ./internal/refractor/...`.
- **Inc 2 — report it.** `Unreadable` + sweep verdicts on the general path. Green: heartbeater unit
  tests mirroring `caplens_divergence_test.go` / `caplens_repair_failure_test.go`.

**Folded in (same code, filed row).** *"[Refractor] A business lens whose liveness is unreadable is
dropped, not reported"* — `LensProvider` skipping an errored `GetStatus`/`Pending`
(`cmd/refractor/main.go:389,393`) is the same four lines Inc 2 rewrites. Shipping it separately would
edit the same function twice.

### 10.1 Build outcome — the generalization did NOT ship; the prefilter blocks it

The `LensProjectionUnreadable` half shipped. **Inc 1 (widening the sweep to business lenses)
was built, reviewed, and reverted** — a two-reviewer adversarial pass independently converged
on a defect that invalidates this brief's central grounding claim.

**The sweep is not, in fact, plane-agnostic. Its PREFILTER is not.** `Sweeper.candidates`
direction 1 (`internal/refractor/pipeline/sweep.go:546-568`) treats *a live anchor with no
target key* as a definite divergence. That is sound only for a **total-coverage** lens —
every anchor has a row — which is an auth-plane property (every identity carries a cap doc).
It is false for a **filtering** lens, which is what almost every business actorAggregate lens
is: `unroutedTasks`' own doc says a direct-assigned task *"never matches at all, so it never
gets a weaver-targets row"*. 14 of the 16 lenses this would enrol are that shape.

The consequence, in steady state with nothing wrong:

- `anchors` is `sort.Strings`-ordered and direction 1 has **no cursor**, so the same
  lexicographically-first ~20 anchors fill the whole prefilter budget every tick, forever —
  each costing a Core-KV read plus a full-engine cypher evaluation that can never heal
  anything.
- Direction 2 (orphan retraction) `break`s on its first key and **never executes** for that
  lens class.
- Direction 3 — the round-robin deep verify, the *only* stale-row detector and the entire
  reason this fire wanted the sweep — is throttled to its reserved 5 slots instead of 25, so
  a full re-verify takes 5× the designed budget over anchor types far larger than `identity`.

So the generalization as scoped would have added cost and health noise while *not* delivering
the healer the board row asks for. Two further confirmed findings rode along: the sweep's
divergence/repair verdicts escalate to `severity: error` ⇒ instance-wide `unhealthy`, which
contradicts the business-lens invariant stated in `docs/components/refractor.md` (a business
lens degrades, never fails, the instance); and 14 of 16 lenses target the shared
`weaver-targets` bucket, so each tick fully enumerates that bucket once per lens.

**The honest next increment** is not "re-apply the widening" — it is **make direction 1 sound
for a filtering lens** before anything is enrolled. The grounded shape: a lens whose
`EmptyBehavior` is `delete` legitimately has anchors with no row, so for it direction 1 must
not treat absence as divergence at all (the deep verify, which recomputes, is the correct and
sufficient detector); and direction 1 needs its own cursor regardless so it cannot monopolize
the batch. That change touches the **auth-plane** sweep's selection algorithm, so it is a
security-plane increment in its own right and gets its own fire and its own adversarial
review — it is not a tail to bolt onto this one.

**Non-goals (this fire).**

- No rebuild-on-MatchChange (decision 1). No change to `CapabilityLensProvider` or any auth-plane
  threshold. No contract change.
- The business-lens sweep **verdict reporting** (`LensCoverageDivergence` / `LensRepairFailing`) was
  built and reverted with Inc 1 — it is dead reporting until a business lens actually has a sweeper,
  so it belongs to the direction-1 fire above, not here.

---

## 11. Fire 6 — direction 1 becomes sound for a filtering lens (build note / fire brief)

The fire §10.1 named as "the honest next increment". It is a **security-plane** change: it edits the
auth-plane sweep's own selection algorithm.

**Scope sentence (verbatim, from the board row).** *"Direction 1 counts a live anchor with no target
key as divergence — true only for a total-coverage lens. For a filtering lens (`emptyBehavior:
delete`) that is the normal state, so the same sorted-first anchors fill the batch every tick (no
cursor), direction 2 never runs and the deep verify is throttled 5×. Latent on `capabilityEphemeral`."*

**Two grounding corrections to §10.1 (verified, and they change the shape).**

1. **`EmptyBehavior` is not the discriminator — every shipped actorAggregate lens is `delete`.** All
   19 of them, the three auth-plane ones (`capabilityRoles`, `capabilityServiceAccess`,
   `capabilityEphemeral`) included (`projection/output.go:11-25` for the constants; the corpus
   confirmed across `packages/*/lenses.go`). §10.1's proposed rule — "a lens whose `EmptyBehavior` is
   `delete` must not treat absence as divergence" — would therefore disable direction 1 for **every**
   lens including the total-coverage auth-plane ones, deleting the detector the sweep was built for.
   The discriminator has to come from somewhere else.
2. **The defect is LIVE on the auth plane, not latent.** `capabilityEphemeral` anchors on `identity`
   and matches only an identity holding a live task grant (`packages/orchestration-base/lenses.go:280`
   — `task.data.expiresAt > $now`), so *most identities legitimately have no row*. Its plan is
   installed today (`capability-kv` ⇒ `IsAuthPlane`), so on any cell with ~20 live identities lacking a
   task grant, direction 1 already fills the whole prefilter every tick. The board's "latent" is wrong.

**The sharpest consequence, which §10.1 understated — direction 2 is the ONLY orphan detector.**
Direction 3 walks `anchors` (`sweep.go:605`); an orphan target key's anchor is by definition *not* in
`anchors`, so the deep verify can never reach one. With direction 1 monopolizing the shared
`prefilterCap`, direction 2 `break`s on its first key (`sweep.go:580`) and an orphaned
`cap.ephemeral.<actor>` row — a stale ephemeral **grant** whose identity vertex is gone — is never
retracted. That is a fail-open on the auth plane, live today.

**A fourth defect, found during grounding and not previously filed.** Direction 1 builds the
`expected` key-map *inside* its own budget-limited loop (`sweep.go:553-566`), so when it breaks on a
full budget, `expected` is **incomplete**. Direction 2 then treats every target key whose anchor sorted
after the break point as an orphan. Today that is masked — direction 2's very first `add` fails on the
same full budget — but it is a live trap: the moment direction 2 is given a reserved quota (which
fixing the above requires), it would spend that quota re-projecting perfectly healthy actors. Building
`expected` must be separated from selecting candidates.

**Decisions taken here as Winston (impl-level, recorded not parked).**

1. **Direction 1's budget is *earned by its hit rate*, not granted by a lens classification.** Its
   premise ("no row ⇒ divergent") is a hypothesis the sweep already tests every tick: `Reproject`
   reports `Wrote`. So direction 1 keeps the full prefilter share while its candidates heal something,
   and drops to a floor after a pass in which it selected candidates and **none** wrote. One pass of
   evidence, re-evaluated every pass, restored instantly by a single productive candidate. This needs
   no DDL field, no contract change, and no lens taxonomy — and it is *correct in both regimes*,
   unlike any fixed cap: a mass first-projection loss on a total-coverage lens still gets the full
   share (every candidate writes), while `capabilityEphemeral`'s steady state decays to the floor.
   A fixed small cap was rejected precisely because it would slow real auth-plane mass recovery ~5×.
2. **Direction 2's reservation is sized by the orphans that actually exist** (`min(batch/5,
   orphanCount)`, computed with no I/O from `targets - expected`). So a lens with no orphans hands
   direction 1 exactly the budget it has today — the reservation costs nothing when unneeded, and
   there is no across-the-board regression to direction 1's share.
3. **Direction 1's cursor is in-memory, not persisted.** Direction 3's cursor is persisted because it
   is the *completeness* guarantee. Direction 1 is a priority hint; a restart re-walking it from the
   start is bounded and self-correcting, and keeping it out of `SetSweepProgress` avoids a Health-KV
   schema change in a security-plane fire.
4. **Direction 2's candidate order becomes deterministic** (sorted) instead of Go map order, so a
   capped tick picks the same orphans rather than a random subset. No cursor: a retracted orphan
   leaves the set, so the set drains; a failing one is yielded by the existing per-actor backoff.
   — **WITHDRAWN at review; see §11.1.** The stated reason is false on the plane this fire is
   scoped to, and acting on it would have introduced a fail-open.

**The invariant this fire must not break.** On a total-coverage, converged auth-plane lens
(`capabilityRoles`, `capabilityServiceAccess`) the selection must be **byte-for-byte what it is
today**: direction 1 selects nothing, so it never gathers unproductive evidence and never floors;
orphanCount is 0 so direction 2 reserves nothing; the deep verify gets the full batch. The change is
strictly a widening for the lens shapes that are broken today.

**Touch-list (verified `file:line`).**

- `internal/refractor/pipeline/sweep.go:502-618` — `candidates` restructured: complete `expected` map
  first, then quota computation, then the three cursored/capped directions.
- `internal/refractor/pipeline/sweep.go:122-135` — `Sweeper` gains the direction-1 cursor + the
  productivity flag; `sweep.go:105-121` doc comment re-stated (it asserts the prefilter catches "the
  definite cases", which is what this fire qualifies).
- `internal/refractor/pipeline/sweep.go:250-305` — `pass` attributes writes back to direction 1 so
  the hit rate is measured; an errored reproject is *no evidence*, not negative evidence.
- `internal/refractor/pipeline/sweep_test.go` — new cases beside the existing ones.
- `docs/components/refractor.md` — the sweep's selection description.

**Increment order + green checks.**

- **Inc 1 — separate `expected` from selection + give direction 2 its sized reservation + direction 1
  its cursor.** Green: `go test ./internal/refractor/...`.
- **Inc 2 — the earned budget.** Green: new unit tests proving the floor engages on an all-miss pass,
  releases on a hit, and never engages on a converged total-coverage lens.

**Non-goals (this fire).** No widening of the sweep to business lenses (that is the still-blocked row,
and §10.1's other two findings — the `severity: error` escalation contradicting the business-lens
invariant, and 14 lenses each enumerating the shared `weaver-targets` bucket — remain **its** scope,
not this one). No change to thresholds, health issue names, the Health-KV schema, or any contract.

### 11.1 Build outcome — SHIPPED `7e6030aa`, with two of §11's own premises withdrawn at review

The three named consequences plus the fourth defect §11 found during grounding all shipped. The
3-layer adversarial review (security-refutation · edge-case · acceptance) confirmed each is
regression-guarded by mutation, and confirmed the deliberately-preserved invariant: on a lens whose
every anchor carries a row the selection is byte-for-byte unchanged. It also **refuted two things
§11 itself asserted**, both of which changed the shipped shape.

**§11 decision 4 was a fail-open, not an improvement.** It called for sorting the orphan set "so a
capped tick picks the same orphans rather than a random subset," reasoning that the set drains
because a retracted orphan leaves it. On the auth plane it does not. Every auth-plane target is
guarded, so `Delete` writes a **soft tombstone** that stays a live NATS-KV key and keeps appearing
in `ListKeys` while `GetRow` reports it absent — so a retracted orphan is re-selected and
re-projected *forever*, a permanent zero-value occupant of the direction's budget. Go's randomised
map iteration, which §11 treated as a defect to remove, was the **fairness mechanism**: it sampled
a different window each tick, so every orphan was reached within a few ticks. Sorting it and
walking the head would have made selection the same lexicographic prefix every tick. Quantified by
the security review at production defaults: with ~100 accumulated tombstone orphans, a newly-lost
grant row has **~20% chance of ever being retracted**, against ~5 ticks before. Shipped instead:
**both** hints rotate from their own cursor, so ordering serves the cursor rather than replacing
the fairness it removed.

**§11's justification for the orphan reservation was also wrong, and the reservation is right
anyway.** It claimed that direction is "the only thing that retracts a grant row belonging to a
departed identity." Core KV deletes are logical, so a departed identity **keeps** its vertex key
and stays in the anchor listing; its stale row is therefore not an orphan at all, and the deep
verify is what detects that over-grant. The direction's real triggers are a physically purged
anchor, a row written for an anchor that never existed, and a transiently short anchor listing. It
*is* still the only detector for those (the deep verify walks anchors, and an orphan's anchor is by
definition not among them), so the reservation stands on a corrected premise.

**Also changed from §11 in response to review.** The earned-share record needs **hysteresis** — two
consecutive unproductive passes, not one — because a single pass can be unproductive for reasons
that say nothing about the lens (either listing can come back short; a row landing between the
target listing and the reprojection reads as missing then correct), and because on a converged lens
the hint then selects nothing, gathers no evidence, and would never get the chance to clear a
verdict formed from that transient. A periodic full-share re-test closes the same permanence hole
from the other side. Each demotion/restoration is logged: §11 specified no observability, and a
detector silently running at its floor is exactly what an operator cannot diagnose. Reservations are
sized to the work that exists, so an idle one no longer shrinks the batch.

**Corrected framing.** §11 called `capabilityRoles`/`capabilityServiceAccess` total-coverage. They
are not, quite: `capabilityRoles` carries `emptyBehavior: delete` + a realness filter *precisely*
so an identity holding no role has no row. Total coverage is a deployment property (does every
anchor hold a role?), never a lens property — which is the strongest argument for the shipped
empirical design over any static lens taxonomy, including the `EmptyBehavior` discriminator §11
already had to discard.

**Filed residuals (rows in the Lattice lane, not deferred silently).** `Reproject` reports
`Wrote=true` for an absent-row upsert the ordering-token guard silently dropped at `seq==0` — a
phantom-heal that is also a false-positive channel for the new earned-share experiment (fails
safe: it reads as *productive*, i.e. the pre-change baseline). The coverage walk's `anchorLive` cost
is O(row-less anchors examined), not O(batch), so a large tombstone population is walked every
tick. Neither is a regression and neither is in this fire's scope.

**Still not done here** (unchanged from §11's non-goals): the sweep is **not** widened to business
lenses. That row's remaining scope is §10.1's other two findings — the `severity: error` escalation
contradicting the business-lens invariant, and 14 lenses each enumerating the shared
`weaver-targets` bucket — plus enrolment itself. This fire removed its blocker.

---

## 12. Fire 7 — a reconciliation write the guard drops is not a heal (build note / fire brief)

The precondition increment of the still-open enrolment row. Enrolling a business lens gives it the
sweep's earned-share hints, and those hints are scored on `Reproject`'s `Wrote`. A `Wrote` that is
false is therefore not a reporting blemish — it is corrupt evidence feeding the selection algorithm
§11 just shipped. It gets fixed before anything is enrolled, on the auth plane where it is live today.

**Scope sentence (verbatim, from the board row).** *"`Reproject` claims `Wrote` for a write the
ordering guard dropped — the `seq==0` → `ErrNoOrderingToken` guard fires only when the row is
*present*, so an absent-row upsert reaches `guardedWrite`, which returns nil without writing while
`Reproject` reports `Wrote=true` — inflating `reconciled` and logging a heal that did not happen."*

**Grounded mechanism (verified `file:line`).**

- `reproject.go:148` — the refusal is `present && seq == 0`. With `present == false` control falls
  through to `adpt.Upsert(..., seq)` at `:153` and sets `out.Wrote = true` at `:156` on a nil error.
- `adapter/natskv.go:197-201` — `guardedWrite` returns **nil without writing** whenever
  `incomingSeq == 0`, before any absent-key branch. So the nil `Reproject` reads as success is the
  guard's silent drop.
- **Two comments in the tree contradict each other, and the guard is the runtime authority.**
  `reproject.go:33-35` asserts *"Creating an ABSENT row is unaffected: the guard's absent-key branch
  takes Create … so the lost-first-projection case this whole design exists for still heals from a
  cold pipeline."* `natskv.go:191-195` states the opposite as a deliberate fail-closed choice — a
  seq-0 write is dropped *"so it can neither create a clobberable seq-0 key nor no-op a real
  update."* The guard is what runs; the `reproject.go` paragraph is false and is corrected here.
  Reversing the guard instead was rejected: it would reintroduce the clobberable seq-0 key its author
  deliberately excluded, on the adapter the auth plane writes through.
- **Consumers of the phantom** — `sweep.go:379/385` score it into `coverageHits`/`orphanHits` →
  `noteHintOutcome` (`:394-395`), so a phantom clears the earned-share miss record and holds a
  hint at full budget on evidence of nothing; `sweep.go:389-393` logs `"healed a divergent
  projection"`; `sweep.go:879` adds it to the persisted `SweepStatus.Reconciled`;
  `control/service.go:838` returns it to an operator.

**Decisions taken here as Winston (impl-level, recorded not parked).**

1. **Refuse the write the guard is certain to drop** — extend `ErrNoOrderingToken` to the absent-row
   upsert, mirroring `b9b5b892`'s stated intent for the present-row case. Nothing real is lost: the
   write was never landing. `seedAppliedSeqFromAckFloor` (`pipeline.go:197+`) makes `seq == 0` mean a
   genuinely cold pipeline, so the heal follows the first applied event.
2. **Condition the refusal on the adapter actually being seq-guarded**, via a new optional
   `adapter.SeqGuarded` interface over the existing `NatsKVAdapter.Guarded()` (`natskv.go:86`). One
   rule, no asymmetry: *a seq-0 reconciliation write cannot land through a guarded adapter, so refuse
   rather than report a phantom heal.* Applied to the delete branch (`:126`) and both upsert cases.
   This is **load-bearing for enrolment**, not tidiness: the 14 business actorAggregate lenses write
   through **unguarded** adapters, where a seq-0 write lands normally and `Wrote` is truthful. Left
   unconditional, the refusal would abandon a business sweep pass on every cold pipeline.

**Grounding corrections to §10.1's remaining scope (verified; they resize the enrolment row).**

- **The `severity: error` finding is already moot.** `evalLenses` raises exactly three issues —
  `LensProjectionPaused` / `Lagging` / `Unreadable` — every one `severity: "warning"`
  (`health/lattice_heartbeater.go:1070-1088`); `LensLivenessStatus` (`:194-208`) carries no sweep
  fields; `LensCoverageDivergence` / `LensRepairFailing` exist nowhere in the `.go` tree (the revert
  §10.1 describes). `aggregateStatus` (`:949-960`) only reaches `unhealthy` via an `error`, which no
  business-lens path can emit, and `lens_alert_test.go:56` enforces it. So this is not a defect to
  fix but a **constraint on any new reporting**: business-lens sweep verdicts must be warning-only
  (`docs/components/refractor.md:717`).
- **The shared-bucket *orphan* hazard is already closed** — and it was the dangerous half.
  `ListKeys` enumerates the **whole** bucket (`natskv.go:308-335`), so a `weaver-targets` sweeper
  sees all 12 lenses' rows; `AnchorFromKey`'s exact prefix+suffix+`ParseVertexKey`+`AnchorType` test
  (`output.go:218-246`, applied at `sweep.go:673`) rejects every key the lens does not own. Each of
  the 12 carries a distinct literal prefix, so no lens can retract another's rows. Verified, not
  assumed.
- **The shared-bucket *cost* is real and unfixed**: `survey` calls `ListKeys` once per sweeper per
  tick (`sweep.go:570`), so enrolling 12 `weaver-targets` lenses is 12 full bucket enumerations a
  minute at `DefaultSweepInterval = 60s`.
- **A new enrolment gate the row never named.** `survey` extracts `row["key"]` with the field name
  hardcoded (`sweep.go:576`). That is correct only for a keyOrder of exactly `["key"]`. All 17
  actorAggregate lenses get that by default (`pkgmgr/build.go:435-438`) but **nothing enforces it** —
  a future composite- or renamed-key actorAggregate lens would yield an **empty** `targets` set,
  making direction 1 read every anchor as row-less. Enrolment must gate on the key shape, not assume
  it.

**Touch-list (verified `file:line`).**

- `internal/refractor/adapter/adapter.go:41` — add the optional `SeqGuarded` interface beside `KeyLister`.
- `internal/refractor/pipeline/reproject.go:21-36` — correct the false absent-row paragraph.
- `internal/refractor/pipeline/reproject.go:111-157` — resolve guardedness once; gate `:126` and add
  the absent-row refusal.
- `internal/refractor/pipeline/reproject_token_test.go` — the fake adapter reports `Guarded() = true`
  so the existing cases keep asserting guarded semantics; new cases for guarded-absent-refuses and
  unguarded-absent-writes.

**Increment order + green checks.**

- **Inc 1 — the refusal + the doc correction.** Green: `go test ./internal/refractor/...`.
- **Inc 2 — the regression guard.** New cases proving a guarded absent-row seq-0 upsert issues no
  write and reports no heal, and that an unguarded one still writes.

**Non-goals (this fire).** No enrolment of business lenses (the row stays open, with its scope
resized by the corrections above). No change to `guardedWrite`'s seq-0 policy, to any threshold,
health issue name, the Health-KV schema, or any contract. No new reporting.

### 12.1 Build outcome — SHIPPED, with this brief's own decision 2 withdrawn at review

The refusal shipped. **Decision 2 — conditioning it on a new `adapter.SeqGuarded` interface — did
not**: the 3-layer adversarial pass (security-refutation · edge-case · acceptance) had two reviewers
independently converge on the same refutation, and the premise it rested on is simply false.

**The premise was false.** §12 decision 2 justified the conditioning as "load-bearing for enrolment
… the 14 business actorAggregate lenses write through **unguarded** adapters." They do not.
`projection/driver.go:188` computes `guarded := authPlane || desc.RequiresGuardedTombstone()`, and
`projection/empty.go:47` makes that true for `delete` **and** `softDelete` — while `emptyBehavior` is
mandatory (`output.go:97`) and **every** actorAggregate lens in the tree declares `delete`. So every
lens enrolment would reach is already guarded, and `tokenRequired` was identical to a plain
`seq == 0` for every pipeline that can reach `Reproject`. The conditioning unblocked nothing.

**And it opened a fail-open the unconditional form did not have.** Wrapping breaks optional
interfaces: `GrantWriterAdapter` (`adapter/read_path_adapters.go:27-140`) is seq-guarded in SQL
(`rls.go:295-329`) but spells no `Guarded()`, so the refusal would silently vanish for it — and in
the INTO-only hot-reload state (`cmd/refractor/main.go:836-843` rebuilds an **unguarded**
`NatsKVAdapter` and `EnableProjectionGuard` is never re-applied) an auth-plane lens reports
`Guarded() == false`, turning a previously fail-safe refusal into a token-less write landing in
`capability-kv`. A conditional fail-safe was strictly worse than the unconditional one it replaced.

**A second refutation reshaped where the refusal lives.** §12 moved it *out* of the `canRead` block,
on the claim that a token-less write "cannot land whether the row is present or absent". That is
true of the KV guard only. `PostgresAdapter`'s guard conditions only its `ON CONFLICT DO UPDATE`
branch (`adapter/postgres.go:125-237`), so an **absent**-row plain `INSERT` does land at token zero —
and `ProtectedAdapter` is `Guarded()`-true while implementing no `RowReader`, so the moved refusal
would have blocked a Protected/RLS actor-aggregate lens's cold first projection, reachable through
the `reproject` control RPC (`control/service.go:810-830`). Shipped instead: the refusal stays
**inside** the `canRead` block — entered only by the own-row-reading NATS-KV family, which is exactly
the guard shape that drops a token-less write outright — and the SQL family is deliberately left to
write. The net production diff is one token: `present && seq == 0` becomes `seq == 0`.

**Verified by mutation, not by reading.** With the fix reverted to `present && seq == 0` the new
regression test fails ("expected error … got nil"); the security reviewer independently reproduced
the same result against `main` via `go test -overlay`, and confirmed the case is genuinely an
upsert-shaped result over an absent row rather than the missing-actor delete branch in disguise.

**Filed as rows, not fixed here** (both pre-existing, both out of this fire's scope): the INTO-only
hot-reload dropping the auth-plane projection guard, and `GrantWriterAdapter` carrying the same
phantom-`Wrote` shape this fire closed for the KV family. A third observation is recorded but not
filed: because the refusal now also fires for an absent row, a brand-new auth-plane lens activating
over a populated graph at `seq == 0` abandons sweep passes until its consumer acks, which
`FailedStreak` escalates. It is bounded by the ack-floor seed and is honest reporting rather than a
phantom, so it is behavior this fire accepts.

### 12.2 Correction — the withdrawn conditioning was right, and CI is what proved it

§12.1 recorded decision 2 as withdrawn at review. **That withdrawal was itself wrong**, and shipping
it reddened `main` (`11f22210` → fixed forward by `82f52fc4`). Recorded here because the review
reasoning was persuasive and still incorrect, which is the part worth keeping.

The review's refutation was: every actorAggregate lens is guarded
(`driver.go:188` `authPlane || RequiresGuardedTombstone()`, and all of them declare
`emptyBehavior: delete`), so conditioning on guardedness can only ever cost safety and never buy a
heal. The enumeration was accurate **for lenses installed through the driver**, and that is not the
whole population. `TestRefractor_Reproject_HealsLostProjection_E2E:114` builds its pipeline on a
plain `adapter.New(...)` with **no** `SetGuarded` — and it is precisely the test that reproduces the
incident reconciliation exists for: a grant that reached Core KV while the lens consumer was never
started, so `lastAppliedSeq` is zero and the row is absent. On an unguarded target that create
lands. The unconditional refusal declined a real heal, and the auth-plane e2e caught it.

**Shipped shape.** The refusal binds to the mechanism that drops the write, not to token-zero:
the adapter must both read its own rows back (the NATS-KV family, whose guard returns nil *before*
looking for a stored watermark) **and** report the guard enabled, through a new optional
`adapter.SeqGuarded`. The first condition excludes the SQL family — which the review was right about,
and which the fix keeps — and the second excludes an unguarded target. Both regression tests are
kept: one proving the refusal under the guard, one proving the create still lands without it.

**The process lesson, not the code one.** The local `go test ./...` that "passed" before the withdrawal
shipped was `go test ./... | grep -E '^(FAIL|--- FAIL)' | head`, whose exit status comes from the last
element of the pipe, never from `go test`. It reported success for a run that had already failed.
A verification command whose exit code cannot observe the thing it verifies is not a gate — redirect
to a file and check `$?`.

## 13. Fire 8 — the grant family is guarded in SQL and unguarded in accounting (build note / fire brief)

`8400efd7` settled that the projection-write guard belongs to the **lens**, not to one adapter
instance, and enforced it through `projection.RequiresGuard`. This fire is the discovery that one
whole adapter family sits outside that accounting: `GrantWriterAdapter` writes the read-auth source
of truth under an **unconditional** SQL seq guard, while reporting no guardedness at all — so every
pipeline path that asks "is this adapter guarded?" is told *no* about an adapter whose writes the
storage layer is silently ordering.

**Scope sentence (verbatim, from the board row).** *"[Refractor] `GrantWriterAdapter` carries the
phantom-`Wrote` shape — its SQL guard is UPDATE-conditioned (`rls.go:295-329`), so a present-row
write at token 0 no-ops and returns nil, which `Reproject` books as a heal. It implements neither
`RowReader` nor a guardedness signal, so the KV-family refusal cannot see it. Same defect class as
`eae71b82`, on the read-grant table."*

### 13.1 Scope-diff gate — the row's remedy holds, its mechanism does not

Run item-by-item against the row, narrow-only:

- **`GrantWriterAdapter` implements no guardedness signal** — **CONFIRMED.**
  `read_path_adapters.go:32-35` asserts `Adapter` + `KeyLister` only; there is no `Guarded()` method,
  so the `adapter.SeqGuarded` assertion fails for it. **This half is what the fire builds.**
- **"…which `Reproject` books as a heal"** — **FALSIFIED, and the fire does not build against it.**
  `Reproject` returns `ErrNotActorAggregate` at `reproject.go:89-90` for any lens with no
  `envelopeFn` — installed only by `InstallActorAggregate` and the Personal-Lens installer, so never
  for a grant lens. (The second `ErrNotActorAggregate` at `:102` is the Personal-Lens exclusion and
  is not the one that fires here.) **All seven** shipped `grantTable` lenses declare no
  projection kind (enumerated over `internal/pkgregistry.All()`: `clinicPatientReadGrants`,
  `clinicProviderReadGrants`, `providerIdentityReadGrants`, `patientIdentityReadGrants`,
  `demoOperatorReadGrants`, `consoleOperatorReadGrants`, `staffReadGrants` — every one
  `projectionKind: ""`). `GrantWriterAdapter` therefore **cannot reach** `out.Wrote = true`. The
  phantom-heal framing is inherited from §12's NATS-KV case and does not transfer.
- **`RowReader` is missing** — confirmed but **deliberately NOT built: it has no consumer.**
  `GetRow` exists for exactly two callers — the Chronicler's event→row merge and `Reproject`'s
  present/absent test — and neither reaches a grant lens. Adding it would be a speculative interface
  with no caller, which the deferred-tail rule treats as worse than an honest omission. If a
  `grantTable` + `actorAggregate` lens is ever declared, that is when `GetRow` earns its place.
- **Same defect class as `eae71b82`** — **true, and more load-bearing than the row claims.** The
  class is *"a guard the caller cannot see"*; `eae71b82`/`82f52fc4` fixed it where the caller was the
  reconciler, and it is unfixed here where the callers are the three live pipeline paths below.

**Net:** the row's **remedy** (give the adapter a guardedness signal) is right and ships; the row's
**mechanism and consumer list** are corrected here, and the `RowReader` half is dropped as
consumer-less. Narrowing only — no adjacent mechanism substituted.

### 13.2 Grounded mechanism — why the accounting misses the whole family

`GrantWriterAdapter`'s guard is **unconditional and structural**, not opt-in: `UpsertGrant`
(`rls.go:295-308`) and `RevokeGrant` (`rls.go:316-330`) both end
`WHERE EXCLUDED.projection_seq > "actor_read_grants".projection_seq`. There is no `SetGuarded`, no
flag, no way to build one that is not guarded. Yet nothing reports this upward, because:

- `projection.RequiresGuard` (`driver.go:240`) returns `false` at its first line for any lens that is
  not `IsActorAggregate` — which is every grant lens. So `ApplyGuard` no-ops for the family.
- `EnableProjectionGuard` (`driver.go:257`) accepts **only** `*adapter.NatsKVAdapter` and errors on
  anything else. This is why the family had to fall outside the accounting to activate at all: if
  `RequiresGuard` ever returned true for a grant lens, `buildAdapter` would fail it closed. The two
  facts are load-bearing together — the exemption is what keeps grant lenses running, and it is also
  what hides them.

Three live paths ask `Guarded()` and are told *no* for a grant lens:

1. **`pipeline.go:1397` — the adjacency-watch skip.** `runAdjWatch` is started for **every**
   pipeline unconditionally (`pipeline.go:515`, no lens-kind condition), and the path carries **no
   JetStream sequence**, so it writes at the sentinel `seq = 0`. The guarded-skip that exists to stop
   exactly this does not fire, so a token-less write reaches `UpsertGrant`. Against an **absent** row
   the plain `INSERT … VALUES (…, 0, false)` **lands** — an unordered, un-sequenced **live read
   grant** in the table every protected table's RLS policy consults. (A *present* row is safe:
   `0 > stored` fails, so neither a revoked grant's resurrection nor a fresh row's downgrade is
   reachable — the exposure is creation, not resurrection.) This is the sharp end of the fire.
2. **`pipeline.go:577` — rebuild force-truncate.** A guarded target forces truncation so a historical
   replay does not leave rejected-write holes behind stored watermarks. A grant lens reports
   unguarded, so the force does not fire, and a rebuild replays writes at sequences below what is
   stored — silently dropped, leaving **holes in the read-grant table** (fail-closed: legitimate
   reads denied).
3. **`pipeline.go:478` — `HotReloadInto`'s refusal + latch.** The refusal cannot see the family, and
   the "a guarded adapter arms the requirement wherever it arrives from" latch never arms.

### 13.3 Shipped shape (increment 1)

`GrantWriterAdapter` implements `adapter.SeqGuarded` with `Guarded() bool { return true }` — a
constant, because the guard is a property of the SQL the type always issues, not of a settable flag.
Compile-time `var _ SeqGuarded` assertions are added for **every** adapter that carries a guard, so a
future adapter that gains one and forgets the signal fails the build instead of silently escaping
all three paths. Tests cover the invariant and each of the three consumers.

**Deliberately not in this increment**, filed as rows instead: `GrantWriterAdapter` implements no
`Truncater`, so consumer (2) now takes the "truncate=true but adapter does not implement Truncater"
warn-and-continue branch. That is strictly better than today (no force at all) but not the end state
— a grant lens should truncate its own `grant_source`-scoped rows on rebuild. Filed with its
consumer rather than improvised here, because truncating read grants is a transient total-denial
window and wants its own review.

### 13.4 Build outcome — SHIPPED `f630efc3`, with one signal fixed at review and two residuals filed

The adversarial pass could not refute the change. It confirmed the guard is genuinely
unconditional (one constructor, no flag, both statements `const`), that no consumer infers
NATS-KV / `Truncater` / `RowReader` from guardedness, and — the highest-risk question — that the
adjacency-watch writes now skipped were **not** load-bearing: every grant lens is a plain lens whose
link and aspect events already reach `evalPlainLinkReprojection` / `evalPlainAspectReprojection`
carrying the real stream sequence, and the plain link path rebuilds adjacency for both endpoints
itself, so it is strictly stronger than the seq-0 path it replaces. Three things came back worth
acting on.

**Fixed in the shipped commit — the rebuild force-truncate announced a repair it then declined.**
Reporting the guard made a grant-lens rebuild log *"guarded bucket forces truncate"* and then, two
lines later, *"adapter does not implement Truncater; skipping"*. The data outcome was unchanged
(nothing downstream of `Rebuild` reads `truncate` beyond the `Truncater` call), but the operator was
told the watermarks had been cleared when the replay was still about to be rejected against them —
and a rebuild is precisely what an operator reaches for when the table has diverged. The force is
now conditioned on the target actually being truncatable, and the other branch states the real
consequence. `GrantWriterAdapter`'s refusal to implement `Truncater` is correct and stays:
`actor_read_grants` is shared, so one producer's rebuild must never TRUNCATE it.

**Residual 1 — a diverged grant table has no repair path.** The skipped adj-watch writes were doing
one thing nothing else does: bulk re-inserting rows *absent* from `actor_read_grants` while the
lens's durable is already caught up. Combined with the rebuild that cannot truncate, an operator who
restores or partially wipes the grant table out-of-band now has no mechanism that re-derives the
missing rows — a `DiffRetraction` producer heals on its next CDC event, but the five non-diff
producers wait for one that touches their labels. This is **fail-closed** (reads denied, never
over-granted), and the removed heals were unordered seq-0 inserts that should not have been landing,
so removing them is right; the gap they were incidentally covering is what needs an owner.
**Consumer: an operator repairing `actor_read_grants` after a restore.** Filed.

**Residual 2 — an INTO edit can strand a grant lens's live grants (pre-existing over-grant).**
`HotReloadInto`'s refusal is armed by `requireGuardedAdapter`, which for a grant lens is set
**only inside `HotReloadInto` itself**, after the refusal check — `InstallActorAggregate`, its only
other caller, never runs for this family. So the *first* INTO edit flipping a lens off the grant
table (`grantTable: true → false`) is accepted: `updateCB`'s target/bucket pin does not fire either,
since `entry.guarded` reads `projection.RequiresGuard` (false here) and both `target` and `bucket`
are unchanged by that edit. The swap succeeds and every row the lens already wrote is stranded live
— no producer addresses that `grant_source`, so `DiffRetraction` can never revoke them. This is
**not a regression** (pre-change the swap was *always* accepted; it is now sometimes refused), but a
security control whose behavior depends on whether an unrelated earlier edit happened to arm it is
worse than one that is honestly absent. The durable fix belongs with the three open hot-reload rows
— `entry.guarded` should read the built adapter rather than `RequiresGuard`, and the refusal set
should cover an identity-changing `grantTable`/`grantSource` edit the way it covers `secureColumns`.
**Consumer: an operator editing a grant lens's INTO.** Filed.

## 14. Fire 9 — the INTO-only reload decides in the open, and says so (build note / fire brief)

### 14.1 Scope — four coupled rows, one fire

The board carries four hot-reload rows the callout groups as one coupled fire. Their scope sentences,
verbatim:

1. **`buildAdapter`/`updateCB` are closures in `main()`, so no test binds them.** "The lens-activation and
   INTO-only hot-reload wiring lives in unexported closures inside `cmd/refractor`'s `main()`; deleting the
   `ApplyGuard` call or a hot-reload refusal leaves `go test ./...` green. Extract to package-level functions
   with injected deps."
2. **An INTO-only hot-reload refusal is invisible outside a log line.** "A refused reload (secureColumns,
   target/bucket, unguarded replacement) logs and returns: health stays `active`, no `RecordError`/`SetPaused`,
   and the operator's edit silently no-ops while the lens runs the old spec. Each redelivery re-enters
   `buildAdapter`, which can leave an unused auto-created bucket behind."
3. **An INTO-only reload does not re-install the `Output` descriptor.** "`output` is a separate aspect from
   INTO, so editing it alone classifies `IntoOnly` — which rebuilds the adapter but never re-runs
   `SetEnvelopeFn`/`SetActorDeleteKey`/`SetSweepPlan`. The live envelope keeps the activated empty-behavior
   while the rebuilt adapter is guarded off the new one. Refuse the reload on an `Output` change, as
   `secureColumns` already does."
4. **An INTO edit can strand a grant lens's live grants.** "`HotReloadInto`'s refusal is armed only from inside
   itself for this family (`InstallActorAggregate` never runs for a grant lens), and the target/bucket pin
   misses a `grantTable: true → false` edit — so the first such swap is accepted and every row the lens wrote
   is left unretractable. Over-grant; pre-existing." (§13.4 Residual 2 names the same remedy: *`entry.guarded`
   should read the built adapter rather than `RequiresGuard`, and the refusal set should cover an
   identity-changing `grantTable`/`grantSource` edit the way it covers `secureColumns`*.)

**Scope-diff gate.** Rows 2–4 each name a *decision* the INTO-only path makes (refuse or accept; report or
stay silent). Row 1 names the *reason none of those decisions is testable*: they live in a closure. So row 1
is not adjacent work — it is the same mechanism, and building 2–4 without it would add three untested
security decisions to an untestable function. The fire narrows nothing and substitutes nothing: each row's
remedy is built as its author stated it. **Non-goals:** the `MatchChange` branch's own refusals (unchanged
except for the shared reporting helper), `RequiresGuard`'s definition, `GrantWriterAdapter`'s deliberate
absence of `Truncater` (§13.4), and the diverged-`actor_read_grants` repair path (its own filed row).

### 14.2 Verified touch-list

| Site | What is there now |
|---|---|
| `cmd/refractor/main.go:60-80` | `pipelineEntry` — carries `guarded`, `target`, `bucket`, `secureColumns` as the RUNNING pipeline's baseline. |
| `cmd/refractor/main.go:518-527` | `buildAdapter` closure — `buildTargetAdapter` then `projection.ApplyGuard`. Captures only `buildTargetAdapter`. |
| `cmd/refractor/main.go:788` | `guardRequired, _ := projection.RequiresGuard(r)` — the activation-time source of `entry.guarded`. |
| `cmd/refractor/main.go:791-802` | the registry write that snapshots the baseline. |
| `cmd/refractor/main.go:837-946` | `updateCB` closure — the `IntoOnly` refusals (856, 861, 875), the build (881), `HotReloadInto` (886), and the `MatchChange` branch. Captures `logger`, `mu`, `registry`, `buildAdapter`, `fullEngine`. |
| `internal/refractor/pipeline/pipeline.go:471-492` | `HotReloadInto` — nil check, then the `requireGuardedAdapter && !guarded` refusal, then the arming latch. |
| `internal/refractor/projection/driver.go:147-206` | `InstallActorAggregate` — the only place `SetEnvelopeFn`/`SetActorDeleteKey`/`SetSweepPlan`/`RequireGuardedAdapter` run. Never re-run on reload; never runs at all for a grant lens. |
| `internal/refractor/projection/driver.go:240` | `RequiresGuard` returns false at its first line for a non-actor-aggregate lens — i.e. for every grant lens. |
| `internal/refractor/lens/schema.go:63-116` | `IntoConfig` — the surface fields: `Target`, `Bucket`, `Table`, `DSN`, `GrantTable`, `GrantSource`. |
| `cmd/refractor/main_test.go` | 204 lines, four tests, all over pure helpers (`isOperationRoleIndexLens`, `threadsKeyColumns`, `hotReloadKeyColumns`). No test reaches activation or reload. |

**Precedents to mirror:** `secureColumnsEqual` (`main.go:1110`) for the comparison helper's shape;
`entry.secureColumns` for "the baseline is the RUNNING pipeline's activated value, never the last-seen spec,
so a refused update cannot poison it"; `health.Reporter.RecordError` (`reporter.go:217`) for the operator
signal, as `evaluate.go:301` already uses it for a defect the pipeline detects itself.

### 14.3 Build order

**Inc 1 — make the decision a value, and the decision-maker a function (row 1).** `buildAdapter` becomes a
package-level `buildAdapter(r *lens.Rule, buildTarget targetBuilder)`; the update callback becomes a
package-level `reloader` struct (logger, ctx, registry lookup, `buildAdapter`, `fullEngine`) with an `update`
method, and `main()` passes `rl.update`. The refusal decision is lifted out whole into a **pure** function
`hotReloadRefusal(entry *pipelineEntry, old, newLens *lens.Rule) string` — empty string accepts, a non-empty
string *is* the operator-facing reason. Pure over the entry snapshot plus the two rules, so every refusal
below is one table row in a unit test, and deleting one reddens the build.

**Inc 2 — the refusal set covers what a reload cannot carry (rows 3, 4).** Added to `hotReloadRefusal`:
an **`Output` descriptor change** (row 3 — `IntoOnly` rebuilds the adapter but re-runs none of the
`InstallActorAggregate` wiring, so the live envelope and the rebuilt adapter would disagree about
empty-behavior and the guard predicate), and a **`grantTable` change in either direction** (row 4 — flipping a
lens on or off the shared grant table changes its identity, not its INTO config, and a swap cannot retract
what the old identity already wrote). The guarded-surface pin is widened from `target`/`bucket` to include
`table` and `grantSource`: `bucket` is empty for every Postgres target, so the existing pin was vacuous for
exactly the family row 4 is about.

**Inc 3 — `entry.guarded` reports the adapter that was built, not a predicate that excludes the family
(row 4).** At activation, `guarded` is read from the built adapter's `adapter.SeqGuarded` report — the same
question `HotReloadInto` asks — instead of `projection.RequiresGuard`, which answers `false` for every grant
lens by construction (§13.2). This is what arms the surface pin for the grant family on its *first* edit
rather than only after an unrelated earlier swap happened to latch it.

**Inc 4 — a refused reload is visible where an operator looks (row 2).** Every refusal, and every failure of
the build or the swap that follows, calls `entry.reporter.RecordError` with the same reason string it logs, so
health carries `lastError` + a rising `errorCount` instead of an unbroken `active`. The refusals run *before*
`buildAdapter`, so a refused reload no longer re-enters target construction on each redelivery — which is what
was leaving an auto-created bucket behind. The one refusal that still follows a build is `HotReloadInto`'s
own; with Inc 2+3 it is a backstop rather than a live path, and it reports too.

**Green checks:** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go test ./cmd/refractor/... ./internal/refractor/...` ·
full `go test ./...` (the entry-baseline change is read by the activation path every lens takes).

### 14.4 In-scope gotchas

- **The baseline must stay the RUNNING pipeline's.** Every new comparison reads `entry.*`, never `old.*` — the
  existing comment at `main.go:850` exists because a refused update that updates the baseline wedges the lens.
  `old` is used only where the running value is genuinely not snapshotted (the secure-lens table/DSN pin).
- **A refusal is not a pause.** `SetPaused` would stop the lens from projecting its *current, correct* spec;
  the lens is running the right thing, the operator's *edit* is what did not land. `RecordError` states that
  without taking the projection down.
- **`Output` is a pointer** (`*lens.OutputDescriptorSpec`) with slice fields — the comparison handles nil on
  either side and compares slices elementwise, mirroring `secureColumnsEqual`'s order-sensitive posture (the
  descriptor is authored, not computed).

### 14.5 Build outcome — SHIPPED, with three review rounds and one residual filed

All four rows shipped as one commit. Two adversarial passes ran; **neither could refute the change's
core claims**, and both found real defects around them, which are fixed in the shipped commit.

**Round 1 confirmed** that the `guarded` source swap cannot disarm a pin that was previously armed
(`RequiresGuard(r)` true ⟹ `ApplyGuard` ⟹ `EnableProjectionGuard`'s concrete `*NatsKVAdapter`
assertion ⟹ the built adapter reports guarded; a failed build means the lens never started), that
`ProtectedAdapter` forwards `SeqGuarded` rather than hiding it, and that no shipped `Output`
descriptor is defaulted or re-serialized in a way that could make an unchanged lens refuse forever.
It found three defects:

- **The secure-lens table/DSN pin read a poisoned baseline.** `CoreKVSource` records
  `s.known[lensID] = rule` *before* invoking the callback, so `old` is the last-**seen** spec, not the
  last-**applied** one: a refused DSN edit became the baseline for the next edit, which then let it
  ride in unexamined — with no verify-and-pause on the swap, onto a database whose RLS posture was
  never probed while the rows carry decrypted PII. Fixed by removing the `old` parameter entirely and
  snapshotting `dsn` on the entry, so the running pipeline is the only baseline there is.
- **The `Output` pin was one-sided.** `ClassifyUpdate` keys on the Match string alone, so editing
  `output` *and* the cypher together lands on the MATCH path — which swapped the compiled rule while
  re-installing the envelope no more than the INTO path does. An operator refused on an Output edit
  could apply it by touching the cypher alongside it, and the result (new cypher, old
  `BodyColumns`) silently dropped newly-returned columns. Fixed by running one refusal set for both
  kinds.
- **Column order was treated as content.** `BodyColumns` / `StaticEmptyColumns` name keys the
  envelope writes into a map, so a reorder means nothing; `Lanes` is emitted verbatim. Fixed by
  comparing the first two as multisets and leaving `Lanes` ordered.

**Round 2 attacked those fixes** and cleared them — the DSN-defaulting hypothesis is dead
(`translateSpec` resolves `REFRACTOR_PG_DSN` and fails closed before the Rule exists, and the pool
manager supplies no default of its own), no `*OutputDescriptorSpec` is aliased across loads, and every
consumer of the column lists is name-keyed. It found the pin set was still **incomplete**:

- **`protected` was the unpinned fourth guard source.** `NewProtectedAdapter` forces the §6.2 guard
  onto its inner adapter; a bare `PostgresAdapter` has it off. And this family has **no backstop** —
  `HotReloadInto`'s refusal arms from `RequireGuardedAdapter`, whose sole caller is
  `InstallActorAggregate`, which never runs for a protected postgres lens and structurally cannot
  (the guard-enabler requires a NATS-KV adapter). So `protected: true → false` on a live read model
  would have retired the monotonic write guard silently, on exactly the surface that carries
  read-path authorization. Pinned; with the auth-plane bucket, the tombstone empty-behavior and
  `grantTable` already pinned, the set of guard sources is now closed. This also closes the two-step
  escape round 2 named (acquire the guard mid-life via one accepted edit, then move the surface
  against the stale flag).
- **`dsn` was missing from the guarded surface pin** it had just been added for. Pinned.
- **`actorField`'s parse-time default was not normalized**, so spelling out `"actor"` refused the
  whole update. Normalized.

**Residual — a package upgrade that changes a lens's `Output` no longer half-applies, and does not
apply either.** Lens NanoIDs are version-independent, so a package upgrade is an *update* on
`vtx.meta.<id>.spec`, not a delete-and-recreate; a re-authored actor-aggregate lens that changes its
cypher and `BodyColumns` together (a shipped shape — `b425c25b` did exactly this to
`appointmentReminders`) is now refused whole. That is the correct call — the alternative was applying
the cypher against the old envelope and dropping the new column silently — but nothing re-activates
the lens afterwards, so `lattice-pkg apply` reports success while the running Refractor keeps serving
the old spec until it restarts. The refusal message names the real remedy, and the durable fix (an
update the pipeline can re-activate through rather than refuse) is a new mechanism, not an extension
of this one. **Consumer: a package author upgrading an actor-aggregate lens's `Output`.** Filed as a
row.

---

## 15. Fire 10 — the healer is enrolled by what it can prove it owns (build note / fire brief)

The row §10.1 blocked and §11.1 unblocked. Fire 5 tried to enrol business lenses and reverted; Fire 6
made the prefilter sound for a filtering lens. What is left is enrolment itself plus §10.1's other two
findings.

**Scope sentence (verbatim, from the board row).** *"Adding a walk to an actorAggregate lens reprojects
nothing already stored — rows refresh only when a CDC event next touches that actor. Only auth-plane
lenses get the convergence sweep; every other actorAggregate has no healer."* Consumers:
`identityAnchors` (whoami hats) + `myTasks`, and the 12 convergence lenses on `weaver-targets`.

**Grounded mechanism (verified `file:line`).**

- The install gate is still the single `if authPlane` at `projection/driver.go:180`; everything the
  `SweepPlan` needs (`AnchorType` / `BuildKey` / `AnchorFromKey`) is read off the `OutputDescriptor`
  every actorAggregate lens compiles.
- **Every actorAggregate lens is already guarded.** `RequiresGuard` is
  `AuthPlane || RequiresGuardedTombstone()` (`projection/plan.go:82`), and all 19 shipped
  actorAggregate lenses carry `emptyBehavior: delete` ⇒ `ActionDelete` ⇒ true
  (`projection/empty.go:47`). So a business sweep's reprojections run under the same §6.2 ordering
  token as the auth plane's — enrolment adds no unguarded-write race. It also means every enrollable
  lens is NATS-KV backed (`EnableProjectionGuard` refuses anything else, `driver.go:259`).
- **The target listing is not lens-scoped, and that is what makes enrolment expensive.**
  `KeyLister.ListKeys` takes no prefix (`adapter/adapter.go:41`); `NatsKVAdapter.ListKeys` calls
  `a.kv.ListKeys` over the whole bucket (`adapter/natskv.go:310`). 12 of the enrollable lenses target
  the one shared `weaver-targets` bucket, so each would enumerate all of it every tick — §10.1's
  second finding, restated with the mechanism.
- **Ownership today rests entirely on `AnchorFromKey`**, applied after the listing
  (`pipeline/sweep.go:661-678`): a sibling's key fails the literal prefix/suffix match or the
  `ParseVertexKey` + `AnchorType` check and is dropped. Pinned by
  `TestSweepCandidates_ForeignKeysInASharedBucketAreNotClaimed` (`pipeline/sweep_test.go:172`). It is
  *syntactic*, not identity-based: two lenses sharing a bucket, a literal prefix **and** an anchor
  type would each claim the other's rows.
- **The precedent for a shared target is a scoped listing, not a post-hoc filter.**
  `GrantWriterAdapter.ListKeys` enumerates only rows carrying the lens's declared `grant_source`, and
  a lens that declares none is **refused a listing** rather than given an unscoped one — because
  DiffRetraction over an unscoped listing "would revoke every OTHER package's grants"
  (`adapter/read_path_adapters.go:21-26,132-140`).
- **The verdicts have nowhere to land on the business path.** `LensLivenessStatus`
  (`health/lattice_heartbeater.go:194`) carries no sweep fields and `LensProvider`
  (`cmd/refractor/main.go:403`) never reads `entry.pipeline.Sweeper()` — only `CapabilityLensProvider`
  does (`main.go:349`). Widening enrolment alone gives a healer whose failures are invisible.
- **§10.1's `severity: error` finding is moot as scoped.** The escalation lives in
  `evalCapabilityLenses`; the business path is a deliberate sibling whose issues are `warning`-only
  (`lattice_heartbeater.go:986-989`, `docs/components/refractor.md:717`). Business sweep verdicts are
  new issue codes on that path, not a reuse of the cap ones — so nothing inherits the escalation.

**Decisions taken here as Winston (impl-level, recorded not parked).**

1. **Enrolment is gated on the lens proving it owns its keys — a prefix-scoped listing, not a
   post-hoc filter.** A new optional `adapter.PrefixKeyLister` lets the sweep enumerate only keys
   under the lens's own key-pattern prefix; the install gate refuses a `SweepPlan` when the adapter
   cannot scope, or when the pattern yields no literal prefix terminating on a "." segment boundary
   (an unanchored `{actorSuffix}`-leading pattern scopes to nothing). This mirrors
   `GrantWriterAdapter`'s refusal rather than inventing a posture, closes §10.1's cost finding at its
   source, and is what the board row means by "add a key-shape install gate". `AnchorFromKey` stays as
   the second gate — `cap.` is legitimately a prefix of `cap.roles.`, so scoping is a cost mechanism
   and ownership still needs the parse.
2. **The auth plane's selection must not move, and the shape makes that provable.** `AnchorFromKey`
   `CutPrefix`es the same literal the listing now filters on, so prefix-scoping can only remove keys
   the ownership filter already rejected: `targets` shrinks, `orphans` is identical, every direction's
   input is identical. Byte-for-byte, by construction rather than by test alone — and pinned by a test
   anyway.
3. **A business sweep runs on a slower clock than the auth plane.** `SweepPlan.Interval` already
   exists per plan (`sweep.go:43`); business lenses get 5 minutes against the auth plane's 60 seconds.
   Rationale is the invariant this fire is already honoring on the health side: a stale business read
   model is a vertical's outage, an unhealed cap doc is an authorization failure. It also keeps the
   aggregate steady-state cost at auth-plane parity — 14 lenses × 25 reprojections / 5 min ≈ the 3
   auth-plane lenses × 25 / min the cell already pays — instead of a 5× jump.
4. **Business sweep verdicts are `warning`-only, always.** No streak escalates a business lens to
   `error`/`unhealthy` (`docs/components/refractor.md:717`). The streaks still exist, because the
   message wants them ("divergent for 4 passes"), but severity is constant.

**Verified touch-list.**

- `internal/refractor/adapter/adapter.go:41` — `PrefixKeyLister` beside `KeyLister`.
- `internal/refractor/adapter/natskv.go:310` — implement it over `a.kv.ListKeysPrefix`
  (`substrate/kvhandle.go:71`), sharing the existing keyOrder-mapping body.
- `internal/refractor/pipeline/sweep.go:44` — `SweepPlan.KeyPrefix`; `sweep.go:563` — survey demands a
  `PrefixKeyLister` and scopes the listing.
- `internal/refractor/projection/output.go:218` — expose the pattern's literal prefix
  (`AnchorFromKey`'s own `prefix` computation, factored out) so the plan and the gate share one
  derivation.
- `internal/refractor/projection/driver.go:180` — the gate: key-shape + adapter capability, not
  `authPlane`; business plans get the slower interval.
- `internal/refractor/health/lattice_heartbeater.go:194,991` — sweep fields on `LensLivenessStatus`;
  `evalLenses` raises `LensCoverageDivergence` / `LensRepairFailing` / `LensSweepStalled` at
  `warning`.
- `cmd/refractor/main.go:403` — `LensProvider` reads `entry.pipeline.Sweeper()`, mirroring
  `main.go:349`.
- `docs/components/refractor.md` + the Health-KV schema doc — the new issue codes and the two-clock
  model.

**Increment order + green checks.**

- **Inc 1 — own what you list.** `PrefixKeyLister` + scoped survey + the key-shape gate, auth plane
  only (enrolment unchanged). Green: `go test ./internal/refractor/...`, with a new test proving a
  foreign key is absent from `targets` and that selection is unchanged.
- **Inc 2 — enrol.** Widen the install gate; business interval. Green: same, plus a test that a
  business actorAggregate lens gets a plan and a pattern-less one does not.
- **Inc 3 — report it.** Sweep verdicts on the business path at `warning`. Green: new health tests
  mirroring `caplens_divergence_test.go` / `caplens_repair_failure_test.go` / the stall suite.

**In-scope gotchas.**

- `KVListKeysPrefix` appends `>` to the prefix (`substrate/kv.go:234`), so the prefix must end on a
  segment boundary — that is exactly what the key-shape gate enforces, not an incidental detail.
- Tombstones stay listed (both listings are unfiltered); the prefilter's existing liveness handling is
  unchanged and must stay unchanged.
- `Sweeper()` is read before the reporter on the cap path deliberately (a live repair failure must
  survive an unreadable health entry) — mirror that order, don't "tidy" it.

**Adjacent find, filed now (not built here).** `applyDiffRetraction` (`pipeline/evaluate.go:614`)
derives a `Delete` for **every** key the plain `KeyLister` returns that the fresh projection no longer
produces, with no ownership filter at all. Every DiffRetraction lens shipped today has either a
dedicated target (`duplicate-candidates`, `ClinicProviderSitesBucket`, `read_landlord_units`,
`read_landlord_lease_applications`) or a source-scoped `GrantWriterAdapter`, so there is no live
victim — but nothing *gates* it: a future lens opting into DiffRetraction against a shared bucket would
retract its siblings' rows on its first event. The activation guard (`pipeline/pipeline.go:324`) checks
only that the adapter is *a* `KeyLister`. Filed as a row.

**Non-goals (this fire).** No rebuild-on-MatchChange. No change to auth-plane thresholds, issue names,
or escalation. No contract change. Not the phantom-heal residual, not the `anchorLive` walk cost, not
the shared-bucket rebuild truncate — each is its own filed row.

### 15.1 Build outcome — SHIPPED, with the gate rebuilt at review and two vacuous tests exposed

All three increments shipped as one commit. Two adversarial passes ran — a security-refutation
pass against the auth-plane equivalence claim, and an edge-case/acceptance pass against the ratified
scope. **Neither could break the two load-bearing claims**, and both found the same real defect
around them.

**What could not be broken.** Prefix-scoping the target listing leaves auth-plane selection
byte-for-byte unchanged: `KVListKeysPrefix` becomes `ListKeysFiltered(prefix + ">")`, which in the
pinned nats.go carries the *same* `IgnoreDeletes()+MetaOnly()` options as the unscoped `ListKeys`, so
tombstone semantics are identical; and the only keys `prefix + ">"` drops that a full listing
returned are keys not starting with the prefix and the bare prefix itself — both already rejected by
`AnchorFromKey`'s `CutPrefix`. The de-dup a filtered listing may produce is absorbed by the
`map[string]struct{}` accumulator, and `reapDepartedFailures` re-applies the same ownership test, so
the narrower set is a no-op there too. On wrong-row retraction: all 14 `weaver-targets` prefixes are
distinct and none is a prefix of another; every one sets `KeyColumn`, so a claim implies
`BuildKey(AnchorFromKey(k)) == k` — a lens can only claim keys it would itself write. The three
lens pairs sharing an anchor type (`appointment` ×3, `object` ×2, `leaseapp` ×2) are separated by
prefix.

**The gate as first written was half of what §15 decision 1 specified, and the missing half was
the dangerous one.** It checked the key shape and not the adapter. An actor-aggregate lens whose
`emptyBehavior` is `skip` requires no guard, so nothing forces it onto NATS-KV, and a Postgres /
Protected / GrantWriter target would have enrolled and then failed `survey` on **every** tick —
`errSweepNoKeyLister` is a pass-level fault, so the repair streak climbs and never clears. On the
business path that is a permanent `degraded`; on the auth plane, where the same codes escalate, the
security reviewer traced it to instance-wide `unhealthy`. Unreachable across today's 19 lenses (all
`emptyBehavior: delete`) — which is exactly the incidental protection a structural gate was supposed
to replace. The shipped gate is three-part, and a lens failing any part gets no sweep at all rather
than one that faults forever.

**A derivable prefix does not imply a working inverse.** The pattern grammar permits a repeated
`{actorSuffix}`; `BuildKey` substitutes every occurrence while the inverse brackets the first. Such a
lens enrols with a **silently dead orphan direction** — the only detector for a row whose anchor is
gone — and claiming nothing is indistinguishable from having nothing to claim. The gate now probes
the round trip. A NATS wildcard in the prefix is refused for the same class of reason: `*.{actorSuffix}`
would scope to every 2-token key in a shared target, reaching the full-bucket scan through the
mechanism meant to prevent it.

**Two of the new tests passed vacuously, proven by mutation.** `ALensWithNoSweeperIsNeverStalled` and
`APausedLensIsExemptFromTheStallClock` each ran a single beat, and a single beat stamps the staleness
baseline at the instant it measures from — so `elapsed` is 0 whatever the guard does. Deleting the
guarded arm left both green. Rewritten to the two-beat sequence the cap path's suite already uses;
the same mutations now fail. The reviewer also confirmed `TheStallClockIsNotSharedWithTheCapPath` is
real (pointing it at `sweepBase` makes it fail with its own message) and that the scoping tests are
not tautological, since reverting `survey` to `ListKeys` fails the recorded-prefix assertion. A
real-substrate `ListKeysPrefix` test was added because the pipeline fake's `HasPrefix` is subtly more
permissive than the subject filter it stands in for.

**Also changed in response to review.** The 14 sweepers all started inside one activation loop and so
ticked in lockstep — a burst of enumerations and up to 350 reprojections every five minutes, then
idle. Each lens's first tick is now offset by a hash of its rule ID (derived, not drawn, so a lens
keeps its slot across restarts). And the install gate's refusal was a log line and nothing else, so a
lens running without its only stale-row detector read exactly like one whose sweep keeps finding
nothing; `metrics.lensLiveness.<lens>.sweepEnrolled` now carries that decision, alongside the sweep
fields the cap path already published.

**Filed residuals (rows in the Lattice lane, not deferred silently).** The **anchor** listing is
still unscoped and is now the dominant cost: `ListKeysPrefix("vtx.<type>.")` returns every aspect key
as well as every root, five lenses share `anchorType: identity`, and nothing dedupes across lenses —
a `vtx.<type>.*` single-token filter would drop the aspect keys at the substrate. Steady state is
also 25 reprojections per tick per lens rather than the reserved quota, since the deep verify fills
the batch. Both are cost, not correctness, and neither is a regression. The business-lens
detect-and-heal e2e — the auth-plane one exists and the plane-independent path is covered at the
pipeline level, but nothing proves the whole chain on a business lens against a real substrate — is
filed with it.
