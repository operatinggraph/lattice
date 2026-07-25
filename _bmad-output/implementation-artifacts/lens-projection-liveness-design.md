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

**Non-goals (this fire).**

- No `LensProjectionSweepStalled` — the staleness-clock mirror of `CapabilitySweepStalled`. The
  auth-plane version carries substantial escalation machinery; the business-lens sweep ships with its
  divergence + repair verdicts observable and its **liveness clock unreported**. Filed as a row, with
  its consumer named, in the same commit as the ✅ flip.
- No rebuild-on-MatchChange (decision 1). No change to `CapabilityLensProvider` or any auth-plane
  threshold. No contract change.
