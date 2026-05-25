# Health KV Schema — Phase 1

> **Canonical reference.** This is the authoritative Health KV key inventory for Phase 1.
> When in doubt, trust this file over `_bmad-output/planning-artifacts/data-contracts.md` §5
> for key-level details. Contract §5 retains schema-level contracts only (bucket name, key
> naming conventions, document shape).

## Overview

The Health KV bucket (`health-kv`) is the platform observability surface. Every Phase 1
component writes structured JSON documents to named keys. The CLI (`lattice health summary`)
reads this bucket to produce the green/yellow/red operator rollup.

Health KV is **write-once-overwrite**: each component periodically overwrites its key with a
fresh document. There is no append log. For audit history, use the event stream (Phase 2+).

---

## Bucket and Connection

| Property   | Value                |
|------------|----------------------|
| Bucket     | `health-kv`          |
| Constant   | `bootstrap.HealthKVBucket` (`internal/bootstrap/primordial.go`) |
| Format     | JSON, camelCase keys |
| Auth       | NATS credentials (same as other KV buckets) |

---

## Key Inventory — Phase 1 Components

### Processor

Source package: `internal/processor/`

| Key Pattern | Frequency | Source File | Emitter |
|---|---|---|---|
| `health.processor.<instance>` | ≥ 10s heartbeat | `internal/processor/health.go` | `HealthHeartbeater.emit()` |
| `health.processor.<instance>.step3-latency` | per heartbeat tick | `internal/processor/health.go` | `HealthHeartbeater.emitCapabilityAuthSignals()` |
| `health.processor.<instance>.cap-staleness` | per tick (non-zero window only) | `internal/processor/health.go` | `HealthHeartbeater.emitCapabilityAuthSignals()` |
| `health.processor.<instance>.malformed-operation.<requestId>` | per malformed envelope | `internal/processor/health.go` | `HealthHeartbeater.EmitMalformedOperation()` |
| `health.processor.<instance>.claim-attempts.<outcome>` | per `ClaimIdentity` call | `internal/processor/health_alerts.go` | `HealthAlertEmitter.RecordClaimAttempt()` |
| `health.alerts.security.<alertCode>` | on security event | `internal/processor/health_alerts.go` | `HealthAlertEmitter.EmitAlert()` |
| `health.processor.<instance>.auth-trace.<requestId>` | per auth denial | `internal/processor/step3_auth_trace.go` | `AuthTraceEmitter.Emit()` |

**`<instance>`** follows the convention `proc-<NanoID>` (Contract #5 §5.1).

**`<outcome>` enum** for claim-attempts: `success`, `invalid-key`, `wrong-state`, `flagged`,
`merged`, `credential-already-bound`, `no-target`.

**`<alertCode>` enum** (known Phase 1 codes): `stub-auth-active`, `auth-freshness-exceeded`.

**Event-driven keys** (only present when the described event occurs — not asserted by the
completeness test):
- `health.processor.<instance>.auth-trace.<requestId>` — per denial only
- `health.processor.<instance>.malformed-operation.<requestId>` — per malformed envelope only
- `health.processor.<instance>.claim-attempts.<outcome>` — per ClaimIdentity call only
- `health.processor.<instance>.cap-staleness` — only when non-zero samples exist in the window
- `health.alerts.security.<alertCode>` — on event only

### Refractor (instance heartbeat)

Source package: `internal/refractor/health/`

| Key Pattern | Frequency | Source File | Emitter |
|---|---|---|---|
| `health.refractor.<instance>` | ≥ 10s heartbeat | `internal/refractor/health/lattice_heartbeater.go` | `LatticeHeartbeater.emit()` |

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
| `health.gates.phase1.gate2` | Gate 2 test suite | `internal/bypass/bypass_test.go` |
| `health.gates.phase1.gate3` | Gate 3 test suite | `internal/bypass/gate3_test.go` |
| `health.gates.phase1.gate4` | Gate 4 test suite | `internal/aiagent/gate4_rollback_test.go` |
| `health.gates.phase1.gate5` | Gate 5 test suite | `internal/hellolattice/hellolattice_test.go` |

**Gate 1 note.** Gate 1 is the bootstrap completion gate. It does not use a
`health.gates.phase1.gate1` key. Instead, bootstrap completion is signaled by
`health.bootstrap.complete` (see Bootstrap section above).

### Alerts

Alert keys use the prefix `health.alerts.security.*` and are documented in the Processor
section above. See that section for the `<alertCode>` enum.

---

## Reserved Namespaces (Phase 2+)

The following key prefixes are reserved. No Phase 1 production code emits them. Reservation
prevents future namespace collisions and documents the intended purpose.

| Reserved Prefix | Intended Purpose |
|---|---|
| `health.weaver.*` | Weaver orchestration telemetry — task-graph status, saga state, compensating transaction tracking |
| `health.loom.*` | Loom task-graph telemetry — lens fan-out coordination, multi-lens consistency tracking |

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
    "lane_lag": {"default": 0, "meta": 0, "urgent": 0, "system": 0}
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

### `health.processor.<instance>.cap-staleness` — Capability projection staleness

```json
{
  "key": "health.processor.<instance>.cap-staleness",
  "component": "processor",
  "instance": "<instance>",
  "observedAt": "<RFC3339>",
  "count": <int>,
  "meanMs": <int64>,
  "p95Ms": <int64>,
  "p99Ms": <int64>,
  "exceedingNFRP3": <int>
}
```

Only written when `count > 0` in the current window.

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
    }
  },
  "issues": []
}
```

`metrics.lensLags` and `metrics.lensLatency` are omitted when no lens is active or no
latency samples have been recorded yet.

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
  "ruleEngine": "<engineName>"
}
```

`pauseReason` is `null` when active; `"infra"`, `"structural"`, or `"manual"` when paused.
`lastError` is `null` when no error has occurred.

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

1. **Heartbeat keys** (`health.processor.*`, `health.refractor.*`): extract `heartbeatAt`
   field; compute `age = now - heartbeatAt`. If `age > staleThreshold` → yellow.
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

> **Note:** Sub-component event-driven keys (classified as `processor-event` or
> `refractor-event` by `classifyKey` — e.g. `step3-latency`, `cap-staleness`,
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
Gates passed: 4/5  (gate2=pass gate3=pass gate4=pass gate5=pass gate1=absent)
Alerts: none
Overall: GREEN
```

---

## NFR-O3 Conformance

NFR-O3 requires every Phase 1 component to emit to Health KV. The following table confirms
the Phase 1 emission surface:

| Component | Key(s) Emitted | Emission Verified |
|---|---|---|
| Processor | `health.processor.<instance>` + derived keys | `internal/processor/health.go`, `health_alerts.go`, `step3_auth_trace.go` |
| Refractor (heartbeat) | `health.refractor.<instance>` | `internal/refractor/health/lattice_heartbeater.go` |
| Refractor (per-lens) | `<lensId>` (bare NanoID) | `internal/refractor/health/reporter.go` |
| Bootstrap | `health.bootstrap.complete` | `internal/bootstrap/primordial.go` |
| Gates | `health.gates.phase1.gate<N>` | integration test suites (gates 2–5) |

All Phase 1 components have a documented emission surface. NFR-O3 is satisfied for Phase 1.

**Phase 2 components** (Weaver, Loom) will extend this table when implemented. Their key
namespaces are reserved (see Reserved Namespaces section above).
