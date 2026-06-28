# Contract #5 — Health KV Convention

> **Phase 1 schema inventory** lives at `docs/observability/health-kv-schema.md` (Story 6.2). This contract describes the convention; the schema doc enumerates emitted keys per component, reserved namespaces, and the `lattice health summary` rollup semantics.

Health KV is the operational observability plane. Every running component writes its own heartbeat to Health KV; readers (humans, CLI tooling at Phase 1; Lens projections at Phase 2+) observe component liveness and operational metrics. Health KV is a **soft convention at MVP** — Stream 7's Closed-loop Weaver auditor (deferred) is the first automated consumer, at which point the convention hardens into a hard contract.

### 5.1 Bucket and Key Pattern

**Bucket:** A dedicated NATS KV bucket separate from Core KV. Provisioned by `make up` with `allow_msg_ttl: true` enabled.

**Key pattern:**
```
health.<component>.<instance>
```

- `<component>` — canonical component name (lowercase, no dots). Phase 1 values: `processor`, `refractor`. Phase 2+ additions: `loom`, `weaver`, `gateway`.
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

### 5.4 Recommended Metrics Baseline (Phase 1 Components)

These metrics are recommended (not enforced) at MVP. Stream 7 may harden them into requirements.

**Processor:**
- `ops_consumed_total` — JetStream messages consumed (cumulative since startup)
- `ops_committed_total` — operations that reached step 8 successfully (cumulative)
- `ops_rejected_total` — operations rejected at any step before commit (cumulative)
- `p99_starlark_ms` — Starlark execution p99 latency (rolling window, recommend 5 minutes)
- `p99_commit_path_ms` — full commit path p99 latency, step 1 through step 10 (rolling window)
- `lane_lag` — per-lane JetStream consumer lag (messages behind head, by lane name). The **Phase-1** Processor runs a *single* durable consumer (`processor-main`) over all `ops.*` lanes, so a true per-lane split is not separable **today**: the per-lane keys are reported `null` ("not measured per-lane") and the genuine aggregate backlog is surfaced as `lane_lag_total` (a `null` total means the backlog could not be read this tick — never a fabricated `0`). The per-lane keys are **reserved**, not retired: when the Processor adopts the architecture's design-of-record **per-lane consumers** (the 3-lane-consumer model — `lattice-architecture.md`; `LATTICE_PROCESSOR_LANES_*_CONSUMERS`, one `substrate.ConsumerSupervisor` per lane), each lane reports its own real lag here and `lane_lag_total` becomes their sum. A backlog above the configured threshold raises a `ProcessorLaneLagging` warning (`status: degraded`).
- `lane_lag_total` — aggregate JetStream consumer backlog across all lanes (`processor-main` `NumPending`); `null` when unreadable.

**Refractor:**
- `lens_count_active` — number of Lens definitions currently projecting
- `cdc_lag_p99_ms_by_lens` — map of `{lensName: p99LagMs}` for each active Lens (architecture's primary liveness indicator)
- `projection_errors_total` — projection failures count (cumulative)
- `vault_calls_total` — Vault decryption calls count (cumulative; Phase 1 stub may report 0)
- `keyshredded_handled_total` — `KeyShredded` events processed (cumulative)

**Loom / Weaver / Gateway:** TBD in Phase 2; conventions will follow this pattern.

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

### 5.8 Implementation Notes

**For the AI agent implementing Story 1.4 (Dev Harness):**

- Health KV bucket created at `make up` time with `allow_msg_ttl: true`
- Bucket name: `health` (or `lattice_health` if namespace prefixing is required by deployment)

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

The Processor instance, on startup:
1. Generate instance NanoID (20-char custom alphabet via substrate's `nanoid.new()`)
2. Construct instance ID: `proc-<NanoID>`
3. Write initial heartbeat with `status: "starting"` and instance metadata
4. Begin commit path consumer loops
5. Once consumers are running, transition to `status: "healthy"` and begin regular heartbeat cadence on the configured interval (default 10s)
6. Each heartbeat write: read current metrics from in-memory counters, construct the document, write to `health.processor.<instance-id>` with `TTL=100s` (default)
7. On `SIGTERM` / shutdown signal: transition to `status: "shuttingDown"` and write a final heartbeat before exit

**For the AI agent implementing Story 2.x (Refractor — Materializer morph):**

The same pattern applies to Refractor, with Refractor-specific metrics. The Refractor health key is `health.refractor.refr-<NanoID>`.

**For the bypass test suite (Story 1.11):**

Bypass test category #1 (direct KV write to Core KV) does NOT apply to Health KV — Health KV is the explicitly sanctioned direct-write surface. The test suite must NOT include Health KV writes as bypass attempts.
