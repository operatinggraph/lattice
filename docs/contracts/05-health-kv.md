# Contract #5 — Health KV Convention

> The **schema inventory** lives at `docs/observability/health-kv-schema.md`. This contract describes the convention; the schema doc enumerates emitted keys per component, reserved namespaces, and the `lattice health summary` rollup semantics.

Health KV is the operational observability plane. Every running component writes its own heartbeat to Health KV; readers — humans, `lattice health summary`, console surfaces, and automated consumers (a Weaver `surface` gap raises §5.5 `issues[]` entries, Contract #10 §10.8) — observe component liveness and operational metrics. The convention is **hard**: the shapes here are what every consumer keys on.

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
| `version` | yes | Health document schema version (`"1.0"`). Consumers can branch on this. |
| `status` | yes | Component liveness/operational state. Enum: see §5.3 |
| `heartbeatAt` | yes | Timestamp of this heartbeat write. Readers compare against current time + heartbeat interval to detect staleness. |
| `startedAt` | yes | Component startup timestamp (immutable across heartbeats from the same instance). |
| `uptime` | yes | ISO 8601 duration since `startedAt`. Computed at heartbeat time. |
| `metrics` | yes | Component-specific operational counters and gauges; the per-component inventory is the schema doc's (§5.4). |
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

An open issue persists across heartbeats (`since` marks its onset and holds while it continues); a resolved issue is simply **absent** from the next heartbeat — there is no explicit "resolved" record.

### 5.6 Heartbeat Cadence and TTL

**Heartbeat interval:** Default **10 seconds** per heartbeat (NFR-O1). Configurable per component.

**TTL on each heartbeat write:** Default `TTL = heartbeat_interval × ttl_multiplier` where `ttl_multiplier = 10`. With the 10s default heartbeat, TTL = **100 seconds**. After 100s with no heartbeat write, NATS publishes a `PURGE` marker for the component's health key; observers see "no health entry" rather than stale-looking data.

Both `heartbeat_interval` and `ttl_multiplier` are component-configurable via deployment config; the 10× multiplier is the architecture-locked default (breathing room for transient stalls without false-positive component-death alarms).

**Each heartbeat OVERWRITES the previous heartbeat**, resetting the TTL clock. Continuous heartbeating keeps the entry alive indefinitely; missed heartbeats expire it within the TTL window.

### 5.7 Reading and Writing Semantics

**Writers:** Every component writes its own heartbeat to its own key on the heartbeat interval. The only writes to Health KV are heartbeat writes; no component writes to another component's health entry. A component that stops gracefully emits one final heartbeat with `status: "shuttingDown"` before exit, so an orderly stop is distinguishable from a silent death.

**Readers:** Humans via NATS CLI (`nats kv get health <key>`), `lattice health summary`, and console surfaces.

**Health KV reads are not auth-gated.** Every actor with NATS cluster access can read Health KV.
