# Refractor — Capability-Lens liveness/lag alert

**Component:** Refractor · **Lane:** Lattice (Stream 2) · **Size:** S–M · **Imp:** ★★ (security-adjacent)
**Status:** ✅ Winston-ratified — build-ready (all open questions are implementation-level; no contract change).

## Problem (grounded)

`docs/components/refractor.md` *Known gap*: none of the Refractor health surface is **Capability-Lens-aware**.
The Capability Lenses (`capabilityRoles`, `capabilityRoleIndex` — any lens projecting to the `capability-kv`
bucket, i.e. `projection.IsAuthPlane(r)`) project the **authorization read-model** the Processor's capability
check reads. When such a lens is `paused`, accumulating `consumerLag`, or grossly behind, **nothing fires** —
the per-lens `health.Reporter` entry and the instance heartbeat carry the raw state, but there is no
threshold/alert and the instance heartbeat always emits `status:"healthy"` + `issues:[]`. This is the
operational backstop for the Processor's **absent per-op freshness gate**: a dead/lagging Capability projector
silently serves **stale authz**, and detection today is operator-judgment over generic signals.

## Key realization — the contract already has the channel

**Contract #5 §5.2/§5.5 already defines** the anomaly mechanism and it is **unused** by Refractor:

- `status ∈ {healthy, degraded, unhealthy}` (§5.4): `degraded` ⇒ `issues` non-empty w/ `severity:"warning"`;
  `unhealthy` ⇒ at least one `severity:"error"` (component "cannot fulfill its primary responsibility").
- `issues[]` entries (§5.5): `{code (PascalCase, component-defined), severity, message, since (ISO8601,
  persists across heartbeats while open)}`. "Components hold open issues in memory; the next heartbeat omits a
  resolved issue."

`internal/refractor/health/lattice_heartbeater.go` today hard-codes `Status: status` (lifecycle value —
`starting`/`healthy`/`shutdown`) and `Issues: []any{}`. **This work implements that channel** for the
capability-lens case. **No contract change** — codes are component-defined; the schema already exists.

## Design (Winston-ratified, implementation-level)

1. **Identify capability lenses** by the existing bucket-derived predicate `projection.IsAuthPlane(r)`
   (target bucket `capability-kv`) — never a canonical-name list, never a new aspect. `pipelineEntry` gains
   `authPlane bool`, set at `startPipeline` from `projection.IsAuthPlane(r)`.

2. **New heartbeater provider** (mirrors `LagProvider` / `LensLatencyProvider`):
   ```go
   CapabilityLensProvider func() []CapabilityLensStatus
   ```
   `CapabilityLensStatus{ CanonicalName, RuleID, Status, PauseReason string; ConsumerLag uint64 }`.
   In `cmd/refractor/main.go` it iterates the registry, filters `authPlane` entries, and reads
   `reporter.GetStatus(ctx)` (status + pauseReason) + `pipeline.Pending(ctx)` (lag). Read-only — touches no
   authz path, no Core KV, no projection logic.

3. **Threshold model** (applied in `emit()`):
   - **paused** (any pauseReason) → issue `CapabilityLensPaused`, **severity `error`** → contributes
     `unhealthy`. The authz read-model is frozen: grants/revocations won't project. For a security-critical
     lens, loud is correct.
   - **active + `consumerLag > LagThreshold`** → issue `CapabilityLensLagging`, **severity `warning`** →
     contributes `degraded`. Authz reads may be stale.
   - **rebuilding** / active-within-threshold → no issue (expected transient / healthy).
   - `LagThreshold` is a heartbeater field, default **100** (`defaultCapabilityLensLagThreshold`),
     deployment-overridable. It is a **warning**, self-resolving on the next heartbeat when lag drains — no
     hysteresis in v1 (a one-cycle spike clears itself); hysteresis noted as a future refinement.

4. **Status aggregation.** `effectiveStatus`: when the lifecycle status is `healthy`, override to the worst of
   the open capability issues (`error`→`unhealthy`, `warning`→`degraded`, none→`healthy`). `starting` /
   `shutdown` lifecycle states pass through unchanged (not steady-state).

5. **Issue persistence (§5.5).** The heartbeater holds an in-memory `map[code]since`; an issue's `since` is set
   on first appearance and reused while it stays open, dropped when it clears. Emitted `issues[]` carry
   `{code, severity, message, since}` exactly per §5.5.

6. **Always-on metric.** `metrics.capabilityLens.<canonicalName> = {status, consumerLag, alert}` is emitted on
   every heartbeat (including healthy `alert:"ok"`) so Loupe/Lamplighter can render the green state, not only
   anomalies.

## Consumers

- **Lamplighter** (`agents/lamplighter`) reads Health KV, classifies `status`/`issues` → surfaces the
  capability anomaly as a remediation candidate.
- **Loupe** Health dashboard + system map already render `status` + `issues` (degraded/unhealthy paths
  shipped) — the new issue codes flow through with no Loupe change.
- Dovetails with **FR54** anomaly-detection (the future on-platform closed-loop auditor).

## Review & gates

Security-adjacent **observability** (read-only; no authz decision path, no Core KV, no projection change
touched) → **thorough lead review** + the standard gates (`go build`, `make vet`, `golangci-lint`,
`STRICT=1 lint-conventions`, `go test ./internal/refractor/health/... ./cmd/refractor/...`). Health-emission
change → the canonical Health-KV doc (`docs/components/refractor.md` health table + Known-gap) is updated in
the same change. Stated explicitly so it is overridable: a full 3-layer adversarial pass is available if a
reviewer judges the capability-plane proximity warrants it.

## Out of scope (future)

Lag hysteresis / debounce · a freshness-staleness window (lastUpdated age vs. a heartbeat budget) · the
Gateway token-revocation hard control · wiring this anomaly into the Loupe agent-activity console.
