# Contract #5 — Health KV Convention

> **Phase 1 schema inventory** lives at `docs/observability/health-kv-schema.md` (Story 6.2). This contract describes the convention; the schema doc enumerates emitted keys per component, reserved namespaces, and the `lattice health summary` rollup semantics.

Health KV is the operational observability plane. Every running component writes its own heartbeat to Health KV; readers (humans, CLI tooling at Phase 1; Lens projections at Phase 2+) observe component liveness and operational metrics. Health KV is a **soft convention at MVP** — Stream 7's Closed-loop Weaver auditor (deferred) is the first automated consumer, at which point the convention hardens into a hard contract.

### 5.1 Bucket and Key Pattern

**Bucket:** A dedicated NATS KV bucket separate from Core KV. Provisioned by `make up` with `allow_msg_ttl: true` enabled.

**Key pattern:**
```
health.<component>.<instance>
```

- `<component>` — canonical component name (lowercase, no dots). The live component inventory is the schema doc's (`docs/observability/health-kv-schema.md`).
- `<instance>` — stable identifier for the running instance. Convention: `<component-prefix>-<NanoID>` where the NanoID is generated once at instance startup (e.g., `proc-Lk2Pn6mQrtwzKbcXvP3T`). The NanoID persists across heartbeats (the same instance keeps writing to the same key); a restart generates a new NanoID and hence a new key.

**Health KV keys do NOT follow Core KV's `vtx`/`asp`/`lnk` patterns.** Health is a separate addressing space in a separate bucket. Direct KV writes to Health are explicitly sanctioned (it's the only sanctioned direct-KV-write pattern outside Refractor's own targets, per architecture P2).

### 5.2 Document Shape

```json
{
  "key": "health.processor.proc-Lk2Pn6mQrtwzKbcXvP3T",
  "component": "processor",
  "instance": "proc-Lk2Pn6mQrtwzKbcXvP3T",
  "version": "1.0",
  "status": "healthy",
  "heartbeatAt": "2026-04-11T14:32:18.142Z",
  "startedAt": "2026-04-08T14:17:00.000Z",
  "uptime": "PT72H15M",
  "metrics": {
    "ops_consumed_total": 14823,
    "ops_committed_total": 14821,
    "ops_rejected_total": 2,
    "p99_starlark_ms": 47,
    "p99_commit_path_ms": 198,
    "lane_lag": {
      "default": 0,
      "meta": 0,
      "urgent": 0,
      "system": 0
    }
  },
  "issues": []
}
```

**Field semantics:**

| Field | Required | Purpose |
|-------|----------|---------|
| `key` | yes | Echo of the Health KV key |
| `component` | yes | Canonical component name (matches `<component>` segment) |
| `instance` | yes | Canonical instance identifier (matches `<instance>` segment) |
| `version` | yes | Health document schema version. Phase 1 = `"1.0"`. Consumers can branch on this; the contract evolves freely until Stream 7. |
| `status` | yes | Component liveness/operational state. Enum: see §5.3 |
| `heartbeatAt` | yes | Timestamp of this heartbeat write. Readers compare against current time + heartbeat interval to detect staleness. |
| `startedAt` | yes | Component startup timestamp (immutable across heartbeats from the same instance). |
| `uptime` | yes | ISO 8601 duration since `startedAt`. Computed at heartbeat time. |
| `metrics` | yes | Component-specific operational counters and gauges. Baseline metrics per component are recommended (§5.4); additional metrics are component-author's discretion. |
| `issues` | yes | Array of structured issue records. Empty `[]` when `status: "healthy"`. Non-empty for `degraded` and `unhealthy`. See §5.5. |

### 5.3 Status Enumeration

| Value | Meaning |
|-------|---------|
| `starting` | Component is initializing; not yet ready to handle work |
| `healthy` | Component is operating normally; `issues` is empty |
| `degraded` | Component is functioning but with reduced capability or elevated error rates; `issues` non-empty with `severity: "warning"` entries |
| `unhealthy` | Component cannot fulfill its primary responsibility (e.g., Processor can't write to Core KV; Refractor can't project to any Lens target); `issues` non-empty with at least one `severity: "error"` entry |
| `shuttingDown` | Component received shutdown signal and is draining work; should not receive new requests |

Status transitions are component-author's discretion; the platform does not enforce specific rules about when a component should transition states. The convention: components should err on the side of being honest about degradation rather than reporting false-healthy.

### 5.4 Metrics

`metrics` is component-specific; the **per-component metric inventory is owned by the schema doc**
(`docs/observability/health-kv-schema.md`), which enumerates every emitted key and its document shape.
Two conventions are contractual:

- **An unmeasured metric reports `null`, never a fabricated `0`** — e.g. a per-lane lag key that is
  reserved but not separable under the current consumer topology reports `null`, and a `null`
  aggregate means "could not be read this tick."
- A metric whose name collides across component groups (e.g. `vault_calls_total` under both the Vault
  group and Refractor) counts that component's **own** instance, never a shared total.

### 5.5 Issue Records

Each entry in the `issues` array:

```json
{
  "code": "VaultUnreachable",
  "severity": "error",
  "message": "Cannot reach Vault for sensitive aspect decryption; Secure Lens projections paused",
  "since": "2026-04-11T14:25:00.000Z"
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| `code` | yes | Machine-readable code (PascalCase). Component-defined. |
| `severity` | yes | `warning` (degraded) or `error` (unhealthy). |
| `message` | yes | Human-readable description. |
| `since` | yes | ISO 8601 timestamp of when this issue first arose; persists across heartbeats while the issue continues. |

Issues are component-tracked: a component holds open issues in memory and includes them in each heartbeat. When an issue resolves, the component removes it from its in-memory set; the next heartbeat omits it from the `issues` array.

### 5.6 Heartbeat Cadence and TTL

**Heartbeat interval:** Default **10 seconds** per heartbeat (matches NFR-O1's "every 10 seconds" requirement). Configurable per component — Refractor under heavy CDC load may heartbeat less frequently; components with faster failure profiles may heartbeat more frequently.

**TTL on each heartbeat write:** Default `TTL = heartbeat_interval × ttl_multiplier` where `ttl_multiplier = 10`. With the 10s default heartbeat, TTL = **100 seconds**. After 100s with no heartbeat write, NATS publishes a `PURGE` marker for the component's health key; observers see "no health entry" rather than stale-looking data.

Both `heartbeat_interval` and `ttl_multiplier` are component-configurable via deployment config. The 10× multiplier is the architecture-locked default; it provides breathing room for GC pauses, brief network blips, and other transient events without false-positive component-death alarms.

**Each heartbeat OVERWRITES the previous heartbeat** (NATS KV update with no `expectedRevision`), resetting the TTL clock. Continuous heartbeating keeps the entry alive indefinitely; missed heartbeats expire it within the TTL window.

### 5.7 Reading and Writing Semantics

**Writers:** Every component writes its own heartbeat to its own key on the heartbeat interval. The only writes to Health KV are heartbeat writes; no component writes to another component's health entry.

**Readers (Phase 1):** Humans via NATS CLI (`nats kv get health <key>`), and the Lattice CLI tool (`make health` or equivalent). The console/Lens projections in FR47 and FR52 are Phase 2 — they'll project Health KV via a Lens then.

**Health KV is NOT projected via the Capability Lens at Phase 1.** Every actor with NATS cluster access can read Health KV. This is consistent with the architecture's "Health KV reads are not auth-gated at MVP" note. Phase 2+ may add capability scoping; not in Phase 1 scope.

**Bypass-suite boundary:** direct-KV-write bypass coverage (category #1) does NOT apply to Health KV —
it is the explicitly sanctioned direct-write surface (§5.1); the suite must not count Health KV writes
as bypass attempts.
