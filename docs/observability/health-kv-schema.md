# Health KV Schema

> **Canonical reference.** This is the authoritative Health KV key inventory across all running components.
> When in doubt, trust this file over [`/docs/contracts/05-health-kv.md`](/docs/contracts/05-health-kv.md) §5
> for key-level details. Contract §5 retains schema-level contracts only (bucket name, key
> naming conventions, document shape).

## Overview

The Health KV bucket (`health-kv`) is the platform observability surface. Every long-running
component writes structured JSON documents to named keys. The CLI (`lattice health summary`)
reads this bucket to produce the green/yellow/red operator rollup.

Health KV is **write-once-overwrite**: each component periodically overwrites its key with a
fresh document. There is no append log. For audit history, use the event stream (Phase 2+).

---

## TTL / Lifecycle

Contract #5 §5.6 mandates a per-key TTL on heartbeat writes so a crashed/redeployed instance's
key **self-expires** rather than orphaning forever (`<instance>` is per-process — a restart
mints a fresh NanoID, Contract #5 §5.1 — so the old instance's key is otherwise never revisited).
Three lifecycle classes:

- **Category A — cadence-rewritten heartbeats** (`health.<component>.<instance>` and the
  per-tick `.step3-latency` sub-key): TTL = `interval × ttlMultiplier`, default multiplier
  **10** (100s at the 10s NFR-O1 floor). Every heartbeat re-arms the TTL clock, so a live,
  continuously-heartbeating instance's key never disappears; a dead instance's key expires
  within the TTL window. Wired for Processor, Refractor, Weaver, Loom, bridge, and
  object-store-manager (each via `KVPutWithTTL`; `SetTTLMultiplier(0)` disables TTL as an
  operator escape hatch). The vertical-app `healthkv.Reporter` (loftspace-app, clinic-app) is
  born TTL-on with the same default.
- **Category B — sparse per-instance diagnostic keys** (e.g.
  `health.processor.<instance>.malformed-operation.<requestId>`, `.claim-attempts.<outcome>`,
  `.commit-conflicts`): a fixed, non-re-armed diagnostic TTL (not yet wired — tracked as the
  next fire of the [Health-KV TTL design](../../_bmad-output/implementation-artifacts/health-kv-ttl-orphan-expiry-design.md)).
- **Category C — durable consumer pause-state** (`health.<component>.<instance>.consumer.<name>`,
  written by the shared `internal/healthkv.ConsumerSink`): **no TTL** — this is durable
  operator/structural pause state, not a liveness signal; a death-tied TTL would risk silently
  resuming a paused consumer after a long downtime (fail-open). Re-keying this to a stable,
  consumer-scoped (non-instance) key is a follow-up fire of the same design.

See the design doc for the full orphan taxonomy and fire-by-fire decomposition.

---

## Bucket and Connection

| Property   | Value                |
|------------|----------------------|
| Bucket     | `health-kv`          |
| Constant   | `bootstrap.HealthKVBucket` (`internal/bootstrap/primordial.go`) |
| Format     | JSON, camelCase keys |
| Auth       | NATS credentials (same as other KV buckets) |

---

## Key Inventory

### Processor

Source package: `internal/processor/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.processor.<instance>` | ≥ 10s heartbeat | `internal/processor/health.go` | `HealthHeartbeater.emit()` | Category A — `interval×10`, re-armed |
| `health.processor.<instance>.step3-latency` | per heartbeat tick | `internal/processor/health.go` | `HealthHeartbeater.emitCapabilityAuthSignals()` | Category A — same TTL, lock-step with the heartbeat |
| `health.processor.<instance>.malformed-operation.<requestId>` | per malformed envelope | `internal/processor/health.go` | `HealthHeartbeater.EmitMalformedOperation()` | Category B — none yet (next fire) |
| `health.processor.<instance>.claim-attempts.<outcome>` | per `ClaimIdentity` call | `internal/processor/health_alerts.go` | `HealthAlertEmitter.RecordClaimAttempt()` | Category B — none yet (next fire) |
| `health.alerts.security.<alertCode>` | on security event | `internal/processor/health_alerts.go` | `HealthAlertEmitter.EmitAlert()` | Category D — alert-code-scoped, out of scope (§ TTL / Lifecycle) |
| `health.processor.<instance>.auth-trace.<requestId>` | per auth denial | `internal/processor/step3_auth_trace.go` | `AuthTraceEmitter.Emit()` | fixed 1h |

**`<instance>`** follows the convention `proc-<NanoID>` (Contract #5 §5.1).

**`<outcome>` enum** for claim-attempts: `success`, `invalid-key`, `wrong-state`, `flagged`,
`merged`, `credential-already-bound`, `no-target`.

**`<alertCode>` enum** (known Phase 1 codes): `stub-auth-active`.

**Event-driven keys** (only present when the described event occurs — not asserted by the
completeness test):
- `health.processor.<instance>.auth-trace.<requestId>` — per denial only
- `health.processor.<instance>.malformed-operation.<requestId>` — per malformed envelope only
- `health.processor.<instance>.claim-attempts.<outcome>` — per ClaimIdentity call only
- `health.alerts.security.<alertCode>` — on event only

### Refractor (instance heartbeat)

Source package: `internal/refractor/health/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.refractor.<instance>` | ≥ 10s heartbeat | `internal/refractor/health/lattice_heartbeater.go` | `LatticeHeartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `rfx-<NanoID>` (Contract #5 §5.1).

The Refractor heartbeat embeds per-lens metrics under `metrics.lensLags` (map of
`lensCanonicalName → lagCount`) and `metrics.lensLatency` (map of `lensCanonicalName →
{count, meanNs, p95Ns, p99Ns}`). These appear inline in the heartbeat document rather than
as separate keys.

### Refractor (per-lens status)

Source package: `internal/refractor/health/`

| Key Pattern | Frequency | Source File | Emitter |
|---|---|---|---|
| `<lensId>` (bare NanoID) | on status change | `internal/refractor/health/reporter.go` | `Reporter.put()` |

> **Key shape note.** The original spec (`epics.md` §Story 6.2) proposed
> `health.refractor.<instance>.lens.<lensId>`. The Phase 1 implementation writes the bare
> `lensId` NanoID directly (`Reporter.put` calls `kv.Put(ctx, r.ruleID, data)` where
> `r.ruleID` is the raw NanoID from `cmd/refractor/main.go:health.New(healthKVHandle, r.ID)`).
>
> The bare-key form is the Phase 1 reality. Phase 2 normalization may align this to
> `health.refractor.<instance>.lens.<lensId>` if disambiguation is needed.
>
> The per-lens key shares the same NanoID as the `vtx.meta.<lensId>` Core KV vertex key that
> defines the Lens.

**Absent from Phase 1 code:** The spec also proposed
`health.refractor.<instance>.lens.capability.*` as a separate per-capability key. No emission
for this pattern exists anywhere in `internal/`. The per-lens lag and latency signals are
emitted inline in the Refractor heartbeat document (under `metrics.lensLags` and
`metrics.lensLatency`). This key is **not emitted** in Phase 1 and is omitted from this
inventory.

### Weaver

Source package: `internal/weaver/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.weaver.<instance>` | ≥ 10s heartbeat | `internal/weaver/health.go` | `heartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `weaver-<NanoID>` (`cmd/weaver/main.go`; overridable via
`WEAVER_INSTANCE`).

The heartbeat `metrics` carry: `consumers` (map of consumer name → state — `running` / `pausedManual` /
`pausedStructural` / `pausedInfra`), `targets` (registered convergence-target count), `marksInFlight`, the
reconciler sweep counters (`sweepReclaims`, `sweepOrphansDeleted`, `sweepCorrupt`, `sweepLastRunAt`), and the
lane-3 temporal counters (`timersScheduled`, `timersFired`). `issues[]` carry a `ConsumerPaused` warning for
each `pausedStructural` consumer plus the engine's active config/data-error alerts (rejected targets, unknown
gap columns, template data errors) — the FR29 "never silently drop" surface.

### Loom

Source package: `internal/loom/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.loom.<instance>` | ≥ 10s heartbeat | `internal/loom/health.go` | `heartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `loom-<NanoID>` (`cmd/loom/main.go`; overridable via `LOOM_INSTANCE`).

The heartbeat `metrics` carry: `consumers` (map of consumer name → state) and `runningInstances` (count of
loom-state `instance.<id>` records with status `running`, scanned on the heartbeat cadence). `issues[]` carry
a `ConsumerPaused` warning for each `pausedStructural` consumer.

### Vertical apps (loftspace-app, clinic-app)

Source package: `internal/healthkv/` (shared `Reporter`), wired from `cmd/loftspace-app/health.go` and
`cmd/clinic-app/health.go`.

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.loftspace-app.<instance>` | ≥ 10s heartbeat (`LOFTSPACE_APP_HEARTBEAT_EVERY`) | `cmd/loftspace-app/health.go` | `healthkv.Reporter.Run()` | Category A — `interval×10`, re-armed |
| `health.clinic-app.<instance>` | ≥ 10s heartbeat (`CLINIC_APP_HEARTBEAT_EVERY`) | `cmd/clinic-app/health.go` | `healthkv.Reporter.Run()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows `loft-<NanoID>` / `clinic-<NanoID>` (overridable via `LOFTSPACE_APP_INSTANCE` /
`CLINIC_APP_INSTANCE`). The heartbeat is gated on a live NATS dial at boot (mirrors
`object-store-manager`); a NATS-down boot never heartbeats until restarted with NATS reachable — an absent
card is itself an operator signal.

Each app's `healthProbe` re-checks its own dependencies every tick (never a static "healthy" ping):
admin actor configured, NATS connected, the protected read-model Postgres pool reachable (if configured),
and a read-auth posture present. `issues[]` codes: `AdminActorUnconfigured` (error), `NatsUnreachable`
(error), `ReadModelUnreachable` (warning), `NoAuthPosture` (warning). `metrics` is empty at v1 (no counters
wired yet).

### Bootstrap

Source package: `internal/bootstrap/`

| Key Pattern | Frequency | Source File | Emitter |
|---|---|---|---|
| `health.bootstrap.complete` | one-shot at bootstrap | `internal/bootstrap/primordial.go` | `MarkBootstrapComplete()` |

Constant: `bootstrap.HealthBootstrapCompleteKey = "health.bootstrap.complete"` (`internal/bootstrap/nanoid.go`).

This key signals that the primordial seeding sequence completed successfully. It is written
once and not overwritten.

### Gates

Phase 1 gates are written by the integration test suites when they pass. They are not
emitted by production binaries.

| Key Pattern | Written By | Source File |
|---|---|---|
| `health.gates.phase1.gate4` | Gate 4 test suite | `internal/aiagent/gate4_rollback_test.go` |
| `health.gates.phase1.gate5` | Gate 5 test suite | `internal/hellolattice/hellolattice_test.go` |

**Gates 2/3 retired.** The Phase-1 security gates (`health.gates.phase1.gate2`,
`.gate3`) had no producer once `make test-bypass`/`make test-capability-adversarial`
were retired — every real defense they proved now ships its own colocated mechanism
test, plus a lean outcome-level residual in `internal/bypass`, all under the normal
`go test` gate ([retire-phase1-security-gates-design.md](../../_bmad-output/implementation-artifacts/retire-phase1-security-gates-design.md)).

**Gate 1 note.** Gate 1 is the bootstrap completion gate. It does not use a
`health.gates.phase1.gate1` key. Instead, bootstrap completion is signaled by
`health.bootstrap.complete` (see Bootstrap section above).

### Alerts

Alert keys use the prefix `health.alerts.security.*` and are documented in the Processor
section above. See that section for the `<alertCode>` enum.

---

## Reserved Namespaces

The `health.weaver.*` and `health.loom.*` namespaces — formerly reserved — are emitted by the
Phase-2 Weaver and Loom heartbeaters (see the Weaver and Loom sections above). No prefixes are
currently reserved-but-unemitted.

---

## Document Shapes (per-key JSON schema)

### `health.processor.<instance>` — Processor heartbeat

```json
{
  "key": "health.processor.<instance>",
  "component": "processor",
  "instance": "<instance>",
  "version": "1.0",
  "status": "healthy | starting | shuttingDown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "ops_consumed_total": <uint64>,
    "ops_committed_total": <uint64>,
    "ops_rejected_total": <uint64>,
    "ops_duplicates_total": <uint64>,
    "ops_malformed_total": <uint64>,
    "lane_lag": {"default": <uint64|null>, "urgent": <uint64|null>, "system": <uint64|null>, "meta": <uint64|null>},
    "lane_lag_total": <uint64|null>
  },
  "issues": []
}
```

### `health.processor.<instance>.step3-latency` — Step 3 auth latency

```json
{
  "key": "health.processor.<instance>.step3-latency",
  "component": "processor",
  "instance": "<instance>",
  "observedAt": "<RFC3339>",
  "count": <int>,
  "meanNs": <int64>,
  "p95Ns": <int64>,
  "p99Ns": <int64>
}
```

### `health.processor.<instance>.malformed-operation.<requestId>`

```json
{
  "key": "health.processor.<instance>.malformed-operation.<requestId>",
  "component": "processor",
  "instance": "<instance>",
  "event": "MalformedOperation",
  "requestId": "<requestId>",
  "reason": "<string>",
  "observedAt": "<RFC3339>"
}
```

### `health.processor.<instance>.claim-attempts.<outcome>`

```json
{
  "key": "health.processor.<instance>.claim-attempts.<outcome>",
  "count": <int64>,
  "lastAt": "<RFC3339>"
}
```

### `health.alerts.security.<alertCode>`

```json
{
  "key": "health.alerts.security.<alertCode>",
  "component": "processor",
  "instance": "<instance>",
  "alertCode": "<alertCode>",
  "severity": "warning | error",
  "message": "<string>",
  "observedAt": "<RFC3339>"
}
```

### `health.refractor.<instance>` — Refractor heartbeat

```json
{
  "key": "health.refractor.<instance>",
  "component": "refractor",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "healthy | starting | shutdown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "lensLags": {"<lensCanonicalName>": <uint64>, ...},
    "lensLatency": {
      "<lensCanonicalName>": {
        "count": <int>,
        "meanNs": <int64>,
        "p95Ns": <int64>,
        "p99Ns": <int64>
      }
    },
    "capabilityLens": {
      "<lensCanonicalName>": {"status": "active | paused | rebuilding", "consumerLag": <uint64>, "alert": "ok | paused | lagging"}
    },
    "lensLiveness": {
      "<lensCanonicalName>": {"status": "active | paused | rebuilding", "projectionLag": <uint64>, "lastProjectedAt": "<RFC3339>", "alert": "ok | paused | lagging"}
    }
  },
  "issues": [
    {"code": "CapabilityLensPaused", "severity": "error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityLensLagging", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensProjectionPaused", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensProjectionLagging", "severity": "warning", "message": "<string>", "since": "<RFC3339>"}
  ]
}
```

`metrics.lensLags` and `metrics.lensLatency` are omitted when no lens is active or no
latency samples have been recorded yet. `metrics.capabilityLens` (auth-plane lenses) and
`metrics.lensLiveness` (every other active lens) are each emitted every heartbeat cycle,
including the healthy `alert: "ok"` state, so observers can render the green state and
(for `lensLiveness`) the freshness clock, not only anomalies
(lens-projection-liveness-design.md §3.3). `LensProjection{Paused,Lagging}` are the
generalized sibling of `CapabilityLens{Paused,Lagging}`: same raise-after-N / clear-band
debounce (default threshold 100, raise after 3 consecutive over-threshold cycles), but
always `severity: warning` (⇒ `status: degraded`) even when paused — a single frozen
business lens is a real outage for that vertical but must not escalate the whole
Refractor instance to `unhealthy` the way a frozen auth-plane lens does. Each `issues[]`
entry's `since` persists across heartbeats while the condition holds and is dropped once
it resolves.

### `health.weaver.<instance>` — Weaver heartbeat

```json
{
  "key": "health.weaver.<instance>",
  "component": "weaver",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "starting | healthy | shutdown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "consumers": {"<consumerName>": "running | pausedManual | pausedStructural | pausedInfra"},
    "targets": <int>,
    "marksInFlight": <int>,
    "sweepReclaims": <int>,
    "sweepOrphansDeleted": <int>,
    "sweepCorrupt": <int>,
    "sweepLastRunAt": "<RFC3339>",
    "timersScheduled": <int>,
    "timersFired": <int>
  },
  "issues": [{"severity": "warning | error", "code": "<code>", "message": "<string>"}]
}
```

`metrics` keys are present only when their subsystem has data (e.g. `marksInFlight` is omitted if
the scan failed; `timers*` only when the temporal lane is wired).

### `health.loom.<instance>` — Loom heartbeat

```json
{
  "key": "health.loom.<instance>",
  "component": "loom",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "starting | healthy | shutdown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "consumers": {"<consumerName>": "running | pausedManual | pausedStructural | pausedInfra"},
    "runningInstances": <int>
  },
  "issues": [{"severity": "warning | error", "code": "<code>", "message": "<string>"}]
}
```

### `health.loftspace-app.<instance>` / `health.clinic-app.<instance>` — vertical-app heartbeat

```json
{
  "key": "health.loftspace-app.<instance>",
  "component": "loftspace-app",
  "instance": "<instance>",
  "version": "1.0",
  "status": "starting | healthy | degraded | unhealthy | shuttingDown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {},
  "issues": [{"code": "AdminActorUnconfigured", "severity": "error", "message": "<string>", "since": "<RFC3339>"}]
}
```

### `<lensId>` — Per-lens reporter status (bare NanoID key)

```json
{
  "ruleId": "<lensId>",
  "status": "active | paused | rebuilding",
  "pauseReason": null,
  "activeSequence": <uint64>,
  "consumerLag": <uint64>,
  "errorCount": <uint64>,
  "lastError": null,
  "lastUpdated": "<RFC3339>",
  "ruleEngine": "<engineName>",
  "lastProjectedAt": "<RFC3339>",
  "projectionLag": <uint64>
}
```

`pauseReason` is `null` when active; `"infra"`, `"structural"`, or `"manual"` when paused.
`lastError` is `null` when no error has occurred. `lastProjectedAt` is the wall-clock of the
lens's last successful target write — `""` until its first projection (design:
lens-projection-liveness-design.md §3.2); a freshness signal, never an alert input on its own
(a genuinely quiet, no-match lens naturally has an old value). `projectionLag` is the
operator-facing alias of `consumerLag` (same NumPending value under both names).

### `health.bootstrap.complete`

```json
{
  "status": "complete",
  "completedAt": "<RFC3339>"
}
```

### `health.gates.phase1.gate<N>`

```json
{
  "passed": true,
  "completedAt": "<RFC3339>",
  "commit": "<git-sha>"
}
```

---

## `lattice health summary` — Rollup Semantics

The `lattice health summary` command reads all keys from the `health-kv` bucket and produces
a green/yellow/red rollup table.

### Stale threshold

Default: `60s`. Configurable via:
- CLI flag: `--stale-threshold <duration>` (e.g. `--stale-threshold 30s`)
- Environment variable: `LATTICE_HEALTH_STALE_THRESHOLD` (overrides the default when the
  flag is not explicitly set)

### Status levels

| Status | Meaning |
|---|---|
| **green** | All non-event-driven components have a health entry fresher than `--stale-threshold`; no active alerts; `consumerLag=0` for all lenses |
| **yellow** | Any component entry is stale (age > threshold) OR `consumerLag > 0` for any lens; OR any active warning-severity alert |
| **red** | Any error-severity alert; any health entry absent (not just stale); any phase gate expected to have passed but not present |

### Component rollup algorithm

1. **Heartbeat keys** (`health.processor.*`, `health.refractor.*`, `health.weaver.*`,
   `health.loom.*`): extract `heartbeatAt`; compute `age = now - heartbeatAt`. If
   `age > staleThreshold` → yellow. For Weaver/Loom, also scan the inline `issues[]`: any `error`
   severity → red, any `warning` → yellow.
2. **Per-lens keys** (bare NanoID): check `status` field.
   - `"paused"` → yellow
   - `"rebuilding"` → yellow
   - `"active"` → check `consumerLag` (> 0 → yellow) and `errorCount` for detail
3. **Alert keys** (`health.alerts.security.*`): check `severity`.
   - `"error"` → red
   - `"warning"` → yellow
4. **Gate keys**: missing gate records → yellow (absence is not red because gates are
   only written after running the corresponding test suite, not on every deploy).
5. **Bootstrap key**: absent → red (bootstrap completion is expected on every running stack).
6. **Overall** = worst of all component statuses.

> **Note:** Sub-component event-driven keys (classified as `processor-event`,
> `refractor-event`, `weaver-event`, or `loom-event` by `classifyKey` — e.g. `step3-latency`,
> `malformed-operation.*`) are intentionally excluded from the rollup. They carry
> point-in-time event data, not steady-state heartbeat data, so they do not contribute
> to the green/yellow/red calculation.

### Table format

```
COMPONENT             STATUS      FRESHNESS     DETAILS
processor.<instance>  green       12s ago       ops_consumed=142 ops_committed=141
refractor.<instance>  green       8s ago        lensLags: capability=0
<lensId> (lens)       active      -             consumerLag=0 errorCount=0
health.bootstrap.comp green       -             one-shot complete
Gates passed: 2/2  (gate4=pass gate5=pass gate1=absent)
Alerts: none
Overall: GREEN
```

---

## NFR-O3 Conformance

NFR-O3 requires every long-running component to emit to Health KV. The following table confirms
the emission surface:

| Component | Key(s) Emitted | Emission Verified |
|---|---|---|
| Processor | `health.processor.<instance>` + derived keys | `internal/processor/health.go`, `health_alerts.go`, `step3_auth_trace.go` |
| Refractor (heartbeat) | `health.refractor.<instance>` | `internal/refractor/health/lattice_heartbeater.go` |
| Refractor (per-lens) | `<lensId>` (bare NanoID) | `internal/refractor/health/reporter.go` |
| Weaver | `health.weaver.<instance>` | `internal/weaver/health.go` |
| Loom | `health.loom.<instance>` | `internal/loom/health.go` |
| Bootstrap | `health.bootstrap.complete` | `internal/bootstrap/primordial.go` |
| Gates | `health.gates.phase1.gate<N>` | integration test suites (gates 4–5; 2–3 retired) |

All long-running components (Processor, Refractor, Weaver, Loom) have a documented emission
surface and are read by the `lattice health summary` rollup. NFR-O3 is satisfied.
