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
- **Category B — sparse per-instance diagnostic keys** (`health.processor.<instance>`
  `.malformed-operation.<requestId>`, `.claim-attempts.<outcome>`, `.commit-conflicts`): a fixed
  `diagnosticTTL` (default 1h, configurable via `SetDiagnosticTTL`; 0 disables it), wired via
  `KVPutWithTTL`. `.malformed-operation.<requestId>` is write-once per request — its TTL is never
  re-armed. `.claim-attempts.<outcome>` and `.commit-conflicts` are rolling per-instance counters
  (read-modify-write) — their TTL re-arms on every write, so it bounds the key's lifetime to
  `diagnosticTTL` *after the instance's last write*, the same "dead instance's key eventually
  clears" property Category A gets from its heartbeat, applied here to a non-heartbeat breadcrumb.
  The shared, alert-code-scoped `health.alerts.security.<alertCode>` (`HealthAlertEmitter.EmitAlert`)
  joined Category B too: same `diagnosticTTL`, re-armed on every occurrence, so an alert whose
  condition stops recurring clears itself instead of staying "warning" forever with no re-emission
  path to clear it (e.g. `stub-auth-active` can never re-fire once a deployed binary refuses a
  stub-mode start).
  See the [Health-KV TTL design](../../_bmad-output/implementation-artifacts/health-kv-ttl-orphan-expiry-design.md)
  for the full orphan taxonomy (which originally deferred the alert plane as "Category D, out of scope").
- **Category C — durable consumer pause-state** (`health.<component>.consumer-state.<name>`,
  written by the shared `internal/healthkv.ConsumerSink`): **no TTL** — this is durable
  operator/structural pause state, not a liveness signal; a death-tied TTL would risk silently
  resuming a paused consumer after a long downtime (fail-open). The key is deliberately
  **consumer-scoped, not instance-scoped**, so pause-state restores across a restart under a
  fresh instance ID instead of orphaning on every restart.

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
| `health.processor.<instance>.malformed-operation.<requestId>` | per malformed envelope | `internal/processor/health.go` | `HealthHeartbeater.EmitMalformedOperation()` | Category B — fixed 1h default, not re-armed |
| `health.processor.<instance>.claim-attempts.<outcome>` | per `ClaimIdentity` call | `internal/processor/health_alerts.go` | `HealthAlertEmitter.RecordClaimAttempt()` | Category B — 1h default, re-armed each write |
| `health.processor.<instance>.commit-conflicts` | per same-key commit conflict | `internal/processor/health_alerts.go` | `HealthAlertEmitter.RecordCommitConflict()` | Category B — 1h default, re-armed each write |
| `health.alerts.security.<alertCode>` | on security event | `internal/processor/health_alerts.go` | `HealthAlertEmitter.EmitAlert()` | Category B — 1h default, re-armed each write |
| `health.processor.<instance>.auth-trace.<requestId>` | per auth denial | `internal/processor/step3_auth_trace.go` | `AuthTraceEmitter.Emit()` | fixed 1h |

**`<instance>`** follows the convention `proc-<NanoID>` (Contract #5 §5.1).

**`<outcome>` enum** for claim-attempts: `success`, `invalid-key`, `wrong-state`, `flagged`,
`merged`, `credential-already-bound`, `credential-not-provisioned`, `no-target`, `erased`.

`credential-not-provisioned` means the SUBMITTING credential has no live identity vertex — either
never provisioned, or tombstoned (revoked). The claim emits a `boundTo` edge whose source is that
vertex, and `identityCredentialBindingsRead` anchors on it, so binding without one produces a row
the person's sign-in-methods list can never show (Contract #3 §3.5). A sustained count here means
the Gateway's first-touch `ProvisionConsumerIdentity` pre-flight is failing — that path answers
503 rather than proceeding — or a submitter is reaching the Processor without one at all, which is
what `lattice identity provision` exists for. Like `erased`, the word never reaches the caller.

It also takes one population from `credential-already-bound`: an actor with **no vertex but a live
`credentialindex`** — the post-shred residue shape, where the shred retracts the edge and leaves the
index standing. The endpoint guard sits above the already-bound guard, so that case now counts here.
An operator watching the re-bind counter should expect it to go quiet on exactly that population.

`erased` means the identity is sealed for erasure — it carries
`vtx.identity.<NanoID>.erasureRequested`, so no further credential may be bound to it
(erasure-orchestration-design.md §6). Health KV is the **only** channel that carries this outcome:
NFR-S6 reclassifies every claim rejection to one generic wire code, so the word never reaches the
caller. The gate sits *below* the claim-secret comparison precisely so that a wrong-secret attempt
against a sealed identity still counts as `invalid-key` — the counter an operator watches for brute
force — instead of being diverted here.

**`<alertCode>` enum** (known Phase 1 codes): `stub-auth-active`, `privileged-lane-grant-rejected`,
`reserved-operation-grant-rejected` (a runtime-authored grant named a core-reserved operationType and was
refused at step 3 — Contract #6 §6.1).

**Event-driven keys** (only present when the described event occurs — not asserted by the
completeness test):
- `health.processor.<instance>.auth-trace.<requestId>` — per denial only
- `health.processor.<instance>.malformed-operation.<requestId>` — per malformed envelope only
- `health.processor.<instance>.claim-attempts.<outcome>` — per ClaimIdentity call only
- `health.processor.<instance>.commit-conflicts` — per same-key commit conflict only
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

`metrics.lensesRegistered` (int, the started-pipeline registry size) is emitted every
heartbeat, always present once `LensCountProvider` is wired — the counterpart to
`lensLags`/`lensLatency` that stays a legitimate `0` instead of vanishing when the registry
is empty (`refractor-lens-registry-restart-integrity-design.md` §4 Fire B step 1).

`metrics.taxonomyLiveness` (object) reports whether this instance's dynamic-type taxonomy
resolver is **armed** — whether the snapshot every `*`-carrying lens narrows against is backed
by a live, fully-drained invalidation consumer (`dynamic-type-taxonomy-design.md` §4.2).
Emitted every heartbeat once `TaxonomyLivenessProvider` is wired, including the healthy
`armed: true` case, so an observer can render the green state as well as the anomaly.

| field | meaning |
|---|---|
| `armed` | the claim itself. `false` forces **every** `*`-carrying lens onto the broad filter |
| `dead` | the feeding subscription failed terminally — `armed` cannot become true again without a restart. The difference between "waiting" and "waiting forever" |
| `probeFailures` | current run of consecutive drain-probe failures; the ordinary reason a resolver stays unarmed with nothing else to show for it |
| `unarmedSeconds` | how long it has been unarmed. Absent while armed |

`unarmedSeconds` is the field that carries the alert, not `armed`: unarmed for a second during
boot replay is the barrier working as designed, unarmed for ten minutes is an incident, and the
flag alone cannot tell them apart. It is emitted here — on the instance's own entry — rather
than left to be inferred from the per-lens `filterBroadReason`, because that reason
(`taxonomy-unarmed`) is the **lowest-ranked** cause in the vocabulary below: a lens that is also
non-exhaustive for its own reasons reports that instead, and the unarmed state surfaces nowhere
at all.

`LensRegistryIncomplete` (severity `error`) is a heartbeat issue code: a lens declared in
Core KV (a `meta.lens` vertex + spec) but absent from the running registry, raised by a
background registry-reconciliation probe (`internal/refractor/health/registry_probe.go`,
`RegistryProbe`) on a 60s boot-grace-window + 10min tick cadence, `since`-persisted like the
other Refractor heartbeat issues below and cleared once the registry catches up. The direct
detection for the cold-registry incident class — a healthy heartbeat with a silently empty or
partial pipeline set (same design, §4 Fire B step 2).

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

### Vault

Source package: `internal/vault/` (emitter: `cmd/processor/main.go` — the Vault is an embedded
backend with no standalone binary, so its heartbeat is emitted by the Processor that hosts the
authoritative Vault and shares that Processor's instance id).

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.vault.<instance>` | ≥ 10s heartbeat | `internal/vault/local.go` (snapshot) | Processor heartbeater | Category A — `interval×10`, re-armed |

The heartbeat `metrics` carry:

- **`backend`** — the active Vault backend: `local-envelope` (the dev envelope-encryption backend) or
  a KMS adapter id. **An operator reads this to know a shred's guarantee strength** — the local
  backend's `ShredKey` is a deny-list *refusal* (the shared master KEK cannot be per-identity
  destroyed), whereas a KMS backend destroys the per-identity key version (true cryptographic
  erasure).
- `vault_calls_total` — cumulative `Decrypt` calls through this Vault (commit-path decrypt-on-read +
  the trusted-tool `lattice.vault.decrypt` RPC).
- `encrypt_calls_total` — cumulative `Encrypt` calls (commit-path step-6.5 encrypt-on-write).
- `keyshredded_handled_total` — cumulative `ShredKey` calls (the privacy-worker's async key
  destruction).
- `dek_cache_size` — unwrapped DEKs currently held in the TTL working-set cache. A gauge, **not** a
  custody-set total: per-identity wrapped DEKs live in Core KV as `piiKey` aspects, not in the Vault,
  so the backend has no cheap, honest "total keys held" to report.
- `keys_shredded` — identities on the in-memory shred deny-list (gauge).

The identically-named `vault_calls_total` / `keyshredded_handled_total` under **Refractor** count
Refractor's *separate* Secure-Lens Vault instance and are unaffected.

### Weaver

Source package: `internal/weaver/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.weaver.<instance>` | ≥ 10s heartbeat | `internal/weaver/health.go` | `heartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `weaver-<NanoID>` (`cmd/weaver/main.go`; overridable via
`WEAVER_INSTANCE`).

The heartbeat `metrics` carry: `consumers` (map of consumer name → state — `running` / `pausedManual` /
`pausedStructural` / `pausedInfra`), `targets` (registered convergence-target count), `marksInFlight`, the
reconciler sweep counters (`sweepReclaims`, `sweepOrphansDeleted`, `sweepCorrupt`, `sweepLastRunAt`), the
lane-3 temporal counters (`timersScheduled`, `timersFired`), and `contractionTrajectory` (map of targetId →
`shrinking` / `steady` / `diverging` — the planner-mandate design §3.4 contraction monitor: a bounded,
sweep-cadence-sampled ring of each target's current violating-row count, present only once a target has ≥ 2
samples). `issues[]` carry a `ConsumerPaused` warning for each `pausedStructural` consumer, the engine's
active config/data-error alerts (rejected targets, unknown gap columns, template data errors — the FR29
"never silently drop" surface), and a `TargetOscillation` error naming a fighting target pair + contested
aspect path (planner-mandate design §3.4 oscillation detector — both named targets are also disabled).

### Loom

Source package: `internal/loom/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.loom.<instance>` | ≥ 10s heartbeat | `internal/loom/health.go` | `heartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `loom-<NanoID>` (`cmd/loom/main.go`; overridable via `LOOM_INSTANCE`).

The heartbeat `metrics` carry: `consumers` (map of consumer name → state) and `runningInstances` (count of
loom-state `instance.<id>` records with status `running`, scanned on the heartbeat cadence). `issues[]` carry
a `ConsumerPaused` warning for each `pausedStructural` consumer.

### Bridge

Source package: `internal/bridge/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.bridge.<instance>` | ≥ 10s heartbeat | `internal/bridge/health.go` | `heartbeater.emit()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `bridge-<NanoID>` (`cmd/bridge/main.go`).

The heartbeat `metrics` carry: `consumers` (map of consumer name → state) and the dispatch counters
(`dispatched`, `pending`, `skipped`, `adapterErrors`, `timedOut`). `issues[]` carry a `ConsumerPaused`
warning for each `pausedStructural` consumer plus the dispatch-path alerts (`BridgeAdapterMissing`,
`BridgeAdapterFailed`, `BridgeEventUnparseable`, `BridgeReplyPublishFailed`, `BridgeSkipProbeFailed`,
`BridgeDispatchOpMissing`, `BridgeScheduleSubject`, `BridgeScheduleReadFailed`,
`BridgeSchedulePublishFailed`, `BridgePollFailed`) — `status` aggregates these per Contract #5 §5.2/§5.3
(`aggregateStatus`, mirroring Loom/Weaver), so a heartbeat carrying an issue can never self-report
`healthy`.

### Model-runner

Source package: `internal/modelrunner/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.model-runner.<instance>` | ≥ 10s heartbeat | `internal/modelrunner` (via `internal/healthkv`) | `healthkv.Reporter` | Category A — `interval×10`, re-armed |

**`<instance>`** follows the convention `model-runner-<NanoID>` (`cmd/model-runner`).

The heartbeat `metrics` carry the service counters (`acceptedTotal`, `busyTotal`, `completedTotal`,
`refusedTotal`, `failedTotal`, `invalidTotal`, `dedupTotal`, `inFlight`), the vendor usage counters
(`vendorInputTokens`, `vendorOutputTokens`), and the spend gauges (`dailyCount`, `dailyCap` — today's
vendor-call count against `MODEL_RUNNER_DAILY_CAP`). `status` follows Contract #5 §5.2/§5.3.

### Gateway

Source package: `internal/gateway/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.gateway.<instance>` | ≥ 10s heartbeat | `internal/gateway/health.go` | `Heartbeater.emit()` | none (`KVPut`, not TTL'd) |

**`<instance>`** follows the convention `gw-<NanoID>` (`cmd/gateway/main.go`).

The heartbeat `metrics` carry the cumulative counters (`requests_total`, `auth_failures_total`,
`ops_submitted_total`). `issues[]` carry `GatewayRevocationDisabled` (warning) when the token-revocation
bucket failed to open at startup (the kill-switch runs verification-only auth, `cmd/gateway/main.go`) —
`status` aggregates via `aggregateStatus` (mirroring Loom/Weaver/Bridge) so a dormant kill-switch is never
hidden behind a green heartbeat.

The heartbeat also carries a `revocation` block (`gateway-token-revocation-activation-design.md` §2.6,
Fire 2) — the token-revocation kill-switch's live-state summary, read by Loupe's F11 revoke panel:
`consumerConnected` (the materializer's `events.gateway.>` consumer; assumed `true` until a
`revocation.consumerDisconnected` pause), `revokedCount` (scanned live off the `token-revocation` bucket
each heartbeat), `lastEventSeq` / `lastSyncAt` (the backing-stream sequence and wall-clock of the last
successful revoke/unrevoke fold; `lastSyncAt` is `""` until the first fold). A paused consumer surfaces
`revocation.consumerDisconnected` (error) in `issues[]` alongside `consumerConnected: false`.

The heartbeat also carries a `jwks` block (Loupe F11's JWKS panel) — the JWT trust-key set's live-state
summary: `keys[]` lists every currently-trusted kid's provenance (`source`: `"jwks"` or `"static"`; `alg`:
the JWK's advisory signing algorithm when known, `""` otherwise; `addedAt`: when the kid first entered the
trusted set, preserved across re-fetches of an already-trusted key). `lastPoll` / `swaps` describe the
background `JWKSPoller`'s activity (`lastPoll` is the last successful fetch, `""` until one succeeds;
`swaps` counts fetches whose resulting kid set changed, not every poll tick) — both `""`/`0` when no
`GATEWAY_JWKS_URL` is configured (a static/dev-only Gateway still reports `keys[]`, with no poller behind
it).

### Object-store-manager

Source package: `internal/objectmanager/`

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.object-store-manager.<instance>` | ≥ 10s heartbeat | `internal/objectmanager/manager.go` | `Manager.emitHeartbeat()` | Category A — `interval×10`, re-armed |

**`<instance>`** is passed via `Config.Instance` (`cmd/object-store-manager/main.go`).

The heartbeat `metrics` carry `reclaimed_total` (cumulative bytes-reclaimed count, both the tombstone
consumer and the never-attached reconcile). `issues[]` carry `ObjectDeleteFailed` (warning) for a stuck
byte-reclaim (keyed per `storeName`, cleared on the next successful delete) and `ReconcileListFailed`
(warning) when the reconcile pass's `ObjectList` call errors — `status` aggregates via `aggregateStatus`
(mirroring Loom/Weaver/Bridge/Gateway).

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

### Facet

Source package: `internal/healthkv/` (shared `Reporter`), wired from `cmd/facet/health.go`.
`cmd/facet` is the Edge showcase app host — a per-identity multi-tenant process (`engineManager`)
whose per-identity engine connections stay confined to the natsauth callout and carry no platform
credential; this heartbeat is emitted over a SECOND, host-level connection authenticated by its own
narrowest-in-matrix NKey (`internal/natsperm.Matrix`'s `facet` row: publish only `$KV.health-kv.>` +
`$JS.FC.>`, subscribe pinned to `_INBOX.>` — no `$JS.API.>`, so even a plain `KVPut` is denied; the
Reporter's `KVPutWithTTL` is the only write this credential can perform).

| Key Pattern | Frequency | Source File | Emitter | TTL |
|---|---|---|---|---|
| `health.facet.<instance>` | ≥ 10s heartbeat (`FACET_HEARTBEAT_EVERY`) | `cmd/facet/health.go` | `healthkv.Reporter.Run()` | Category A — `interval×10`, re-armed |

**`<instance>`** follows `facet-<NanoID>` (overridable via `FACET_INSTANCE`). The heartbeat is gated
on a configured host credential (`NATS_NKEY` / `NATS_CREDS`) — unconfigured means no reporter runs (a
warn log, not a failure); an absent card is itself the operator signal, same posture as the vertical
apps' "gated on a live NATS dial."

`healthProbe` re-checks the host health connection and, in host-engine mode, the in-process engine
fleet's aggregate connectivity — never per-identity detail (no identity NanoID ever appears in the
marshaled document). In browser-native mode (`FACET_BROWSER_ENGINE`) the engines live in-page,
invisible to the host by design, so the fleet metrics are absent entirely rather than fabricated as
zero. `issues[]` codes: `NatsUnreachable` (error — the host health connection itself is down),
`EngineSyncDegraded` (warning — N engines are in sync-manager restart-backoff, the row's demanded
signal), `EngineNatsDisconnected` (warning — N engines' per-identity NATS connection is currently
down; a distinct axis from sync-loop crash-looping), `ReadModelUnreachable` (warning — the
`identityCredentialsRead` Postgres pool, mirrors the vertical apps). `metrics`: `mode`
(`host-engine` | `browser-native`), and in host-engine mode only: `engines_active`, `engines_pinned`,
`engines_sync_degraded`, `engines_nats_disconnected`.

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
  "version": "1.0",
  "status": "healthy | starting | degraded | unhealthy | shuttingDown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "lensesRegistered": <int>,
    "taxonomyLiveness": {"armed": <bool>, "dead": <bool>, "probeFailures": <int>, "unarmedSeconds": <int64>},
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
      "<lensCanonicalName>": {"status": "active | paused | rebuilding | unknown", "consumerLag": <uint64> | null, "alert": "ok | secure-redaction | paused | unreadable | repair-failing | repair-blocked | sweep-stalled | audit-stalled | unverified | diverged | lagging | structural-pause-auto-recovered", "reconciled": <uint64>, "failingActors": <int>, "unverified": <int>, "blocked": <int>, "blockedByClass": {"retraction": <int>, "content": <int>, "unknown": <int>, "provenance": <int>}, "blockedWorstClass": "retraction | content | unknown | provenance", "unverifiedReason": "<string>", "blockedReason": "<string>", "sweepLastPassAt": "<RFC3339>" | "", "sweepSuppression": "<string>", "unreadable": "<string>", "auditEnrolled": <bool>, "auditRefusal": "<string>"}
    },
    "lensLiveness": {
      "<lensCanonicalName>": {"status": "active | paused | rebuilding | unknown", "projectionLag": <uint64> | null, "lastProjectedAt": "<RFC3339>" | "", "alert": "ok | secure-redaction | paused | unreadable | repair-failing | repair-blocked | sweep-stalled | audit-stalled | unverified | diverged | lagging | structural-pause-auto-recovered", "unreadable": "<string>", "sweepEnrolled": <bool>, "reconciled": <uint64>, "failingActors": <int>, "unverified": <int>, "blocked": <int>, "blockedByClass": {"retraction": <int>, "content": <int>, "unknown": <int>, "provenance": <int>}, "blockedWorstClass": "retraction | content | unknown | provenance", "unverifiedReason": "<string>", "blockedReason": "<string>", "sweepLastPassAt": "<RFC3339>" | "", "sweepSuppression": "<string>", "auditEnrolled": <bool>, "auditRefusal": "<string>", "audited": <int>, "divergentRows": {"missing": <int>, "stale": <int>, "retained": <int>}, "divergentTotal": <int>, "auditUnverified": <int>, "auditUnverifiedReason": "<string>", "auditLastPassAt": "<RFC3339>" | "", "auditCycleCompletedAt": "<RFC3339>" | "", "auditCycleAudited": <int>, "auditCycleDivergentTotal": <int>, "auditCycleUnverified": <int>, "auditCoverageBasis": "key-type", "auditListingSize": <int>, "auditSuppression": "<string>"}
    }
  },
  "issues": [
    {"code": "CapabilityLensPaused", "severity": "error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityLensLagging", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityCoverageDivergence", "severity": "warning | error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityRepairFailing", "severity": "warning | error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityRepairBlocked", "severity": "warning | error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilitySweepStalled", "severity": "warning | error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityLensUnreadable", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "CapabilityLensStructuralPauseAutoRecovered", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensProjectionPaused", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensProjectionLagging", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensProjectionUnreadable", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensCoverageDivergence", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensRepairFailing", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensSweepStalled", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensAuditUnverified", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensRepairBlocked", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensSecureRedaction", "severity": "error", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensStructuralPauseAutoRecovered", "severity": "warning", "message": "<string>", "since": "<RFC3339>"},
    {"code": "LensRegistryIncomplete", "severity": "error", "message": "<string>", "since": "<RFC3339>"}
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
Refractor instance to `unhealthy` the way a frozen auth-plane lens does.

`LensProjectionUnreadable` is the general-lens mirror of `CapabilityLensUnreadable`, with
the same meaning and the same `warning` severity: a business lens whose liveness inputs
could not be read this cycle is reported `status: "unknown"`, `alert: "unreadable"`,
`projectionLag: null` and an `unreadable` field naming the failed read — never dropped from
`metrics.lensLiveness`. An absent lens is indistinguishable from one that was never
installed, which is the monitoring equivalent of reporting healthy. Each `issues[]`
entry's `since` persists across heartbeats while the condition holds and is dropped once
it resolves.

`Lens{CoverageDivergence,RepairFailing,SweepStalled}` are the general-lens mirror of the
three `Capability*` sweep codes below, raised on the same streaks from the same sweeper —
the convergence sweep runs for every actor-aggregate lens that can scope a key listing to
its own rows, not only the auth-plane ones (lens-projection-liveness-design.md §15). Two
differences, both deliberate: they are **always `severity: warning`**, at every streak
length, where the capability codes escalate to `error` — a wrong business read model is one
vertical's outage and must not take the whole instance `unhealthy`; and a business lens
sweeps on a slower clock (`BusinessSweepInterval`, 5 minutes, against the auth plane's 60
seconds), which also scales its own staleness window since that window is a multiple of the
sweep interval. A lens with no sweeper installed reports `sweepEnrolled: false`, omits the
other sweep fields, raises no sweep verdict and can never read as stalled — the install
gate declines any lens that cannot scope a key listing to its own rows, round-trip its own
keys, or whose adapter cannot enumerate under a prefix.

`CapabilityCoverageDivergence` is the auth-plane convergence sweep's alert
(capability-projection-reconciliation-design.md §3.2), and it is the only capability issue
that is **not** derived from consumer state: `CapabilityLens{Paused,Lagging}` watch whether
the pipeline is running and caught up, whereas a pipeline that is active with zero lag can
still have *missed* an event and left a projection hole. The sweep re-derives each anchor's
projection from the graph, heals what diverges, and reports it here — `warning` after one
divergent sweep pass (a repaired incident), escalating to `error` once two consecutive
passes each heal something (events are still being lost, so the sweep is papering over an
ongoing gap rather than a past one), and clearing on the first clean pass.
`metrics.capabilityLens.<name>.reconciled` is the matching cumulative counter: it is
deliberately loud, because a nonzero *rate* is itself the signal to go find the delivery
gap. It reads `0` for a lens with no sweeper — which is **not** "every non-auth-plane
target": since the enrolment gate landed, any actor-aggregate lens that can name its own
rows gets a plan, business lenses included. What has no sweeper is a lens that fails one of
the enrolment conjuncts, or is not actor-aggregate at all (`sweepEnrolled: false` says
which).

`CapabilityRepairFailing` is the sweep's second, independent verdict, and it covers the
blind spot of the code above: `CapabilityCoverageDivergence` is keyed on what the sweep
**healed**, and a repair whose target write *errors* heals nothing — so the divergent
streak clears and, with the consumer active and caught up, every other signal reads as
converged while the row stays wrong. The more thoroughly unwritable the row, the healthier
the lens reads. A single failing pass raises nothing — a failing anchor is retried on the
very next pass, so an isolated write error clears itself inside one interval — then
`warning` at two consecutive failing passes and `error` at three, clearing when a
reprojection of every failing anchor succeeds. It also covers pass-level faults — an
unreadable survey, or a tick abandoned before it could verify anything — which name no
actor and would otherwise be indistinguishable from a clean bucket.
`metrics.capabilityLens.<name>.failingActors` is the matching gauge (anchors currently
unrepaired) and is emitted every beat regardless of the debounce, so the raw count is
visible before the alert raises. `alert` reads `repair-failing` once the warning
threshold is crossed, outranking `lagging` (a row that is wrong now beats a read-model
that is merely behind — `consumerLag` still carries the lag value); `paused`
keeps precedence, since the sweep is suppressed while paused and a lingering failure there
is a frozen artifact rather than a live one. A failing anchor is retried on the next pass,
then backs off by doubling to a sixteen-pass ceiling — the backoff suppresses the retry
*work*, never the signal: a skipped anchor still counts in `failingActors`. Both sweep
streaks are per-process state (a restart opens a fresh escalation window and re-discovers
a live failure on its first pass), unlike the cumulative `reconciled` counter, which is
restored from this document.

`CapabilityRepairBlocked` / `LensRepairBlocked` cover the case the two codes above still
cannot express, and it is the worst of the family: the sweep found a divergence, attempted
the repair, and the **ordering guard declined the write** against an equal-or-fresher
stored watermark (Contract #6 §6.2). Nothing errored — the write returned success having
changed nothing — so `RepairFailing` stays silent, and the previous accounting counted it
as a heal, logging *"healed a divergent projection"* once per pass for a row it provably
could not touch. There is no retry that helps: the row stays wrong until a real CDC event
above that watermark reprojects it, and on the auth plane that row is a permission set the
graph no longer grants. `metrics.*.<name>.blocked` is the standing count and `blockedReason`
the governing cause; the issue message names the **sanctioned repair** — a REBUILD of the lens
(Contract #6 §6.2: a guarded bucket's rebuild forces `truncate=true`, so the purge clears the
stored watermarks together with the data and the stream replays from empty).

**Read the class, not the count.** `metrics.*.<name>.blockedByClass` splits the total by WHICH
condition the guard declined, and `blockedWorstClass` names the most severe class present. Only
the classes that actually fired are emitted, so a class that is not detecting reads as ABSENT
rather than as a zero, and the counts sum to `blocked`. Both planes publish the full
classification. On the **auth plane** it also drives `CapabilityRepairBlocked`'s severity — from
the class, never from parsing `blockedReason`:

| class | meaning | `CapabilityRepairBlocked` severity |
|---|---|---|
| `retraction` | a declined `Delete`: a revoked grant stays live and honoured — the over-grant direction | `error` on sight |
| `content` | a content divergence declined at the guard; no observed ordinary producer | `error` on sight |
| `unknown` | no read-back, so the class cannot be proven — the fail-closed class, never demoted to the benign one | escalates to `error` at two consecutive blocked passes |
| `provenance` | `projectedFromRevisions` drift only (coherence/debug provenance per §6.3, reachable by an ordinary lens-definition write that leaves the MATCH unchanged); the row's meaning is identical | `warning`, does not escalate |

A permanent provenance-only streak is a real, benign, standing condition and stays visible at
`warning` however long it runs — the point is that it must not hold the plane at maximum volume,
where the classes that have no producer cannot be heard over it.

`LensRepairBlocked` carries the same census, the same `blockedWorstClass`, the same class-named
`blockedReason` and the same remedy text, but stays **`severity: warning` at every class and every
streak length**, like every other business-lens code: an `error` there takes the whole Refractor
instance `unhealthy`, and a single vertical's wrong read model must not take the auth plane down
with it. Read `blockedByClass` to tell a benign business drift from a real one — the severity on
that plane deliberately will not.

`CapabilityAuditUnverified` / `LensAuditUnverified` are the third outcome: the sweep
examined an anchor and could reach **no verdict at all**. Every count above is inferred
from a write, so an anchor whose divergence has no repair transport produces no write, no
error, and no heal — which clears the divergent streak and publishes green. That is the
shape behind the twelve `orphanedTaskGrants` rows that sat stale for twelve days behind a
healthy card. `metrics.*.<name>.unverified` is the per-pass count and `unverifiedReason`
the governing cause. It ranks *below* `repair-blocked` (the sweep does not know what it is
looking at, where blocked knows exactly) and *above* `lagging`. It is expected to read `0`
across the current corpus — every shipped actor-aggregate lens arms zero-row retraction —
and that is the point: the silence can no longer happen, not that it is happening.

`LensProjectionDiverged` and `LensAuditStalled` come from a different detector, and the
difference is the point. Every code above is inferred from what the convergence sweep
managed to **write**; the **divergence audit** writes nothing at all
(`lens-projection-divergence-audit-design.md` §4.3). It is the plain corpus's first per-row
correctness signal: on a slow clock (`DefaultAuditInterval`, 15 minutes) it re-runs an
enrolled plain lens's own seeded evaluation over a bounded batch of its anchors
(`DefaultAuditBatch`, 10) and compares each result against the stored row, using the same
`rowsEquivalent` definition of "same row" the sweep and `Reproject` use.

`LensProjectionDiverged` is raised when it finds a disagreement, and it says so with the
per-class breakdown in `divergentRows` — `missing` (the recomputation produces a row the
target does not hold), `stale` (the target holds it and the content differs), `retained` (the
target still holds a row for an anchor that no longer projects it, because its match stopped
matching or the anchor was tombstoned and the retraction was lost). The map carries **only
the classes that fired**, so a direction that has silently stopped detecting reads as absent
rather than as zero; `divergentTotal` is the sum and the single number the alert keys on.
Severity is `warning` at every magnitude — the audit runs only on business lenses, and a
wrong business read model degrades the instance rather than failing it.

**Nothing is repaired.** The audit detects and reports; repair on a shared, unguarded plain
target was rejected (§8.1), so the row stays wrong until an operator drives the control-plane
`reproject` RPC or a `Rebuild`. Read `divergentTotal` against `auditCycleCompletedAt` before
concluding anything from a zero: one pass covers at most one batch, so a clean number is a
claim about *those* anchors until a cycle has closed over the lens. That is what
`auditCycleAudited` / `auditCycleDivergentTotal` / `auditCycleUnverified` carry — the LAST
COMPLETED cycle's totals, stamped with `auditCycleCompletedAt` and describing the same walk.
A cycle is recorded as completed **only when it actually compared anchors**, so a fresh
timestamp always means "this many anchors were compared" and never "a tick happened"; a lens
with no enumerable anchors therefore never earns one, and its `divergentTotal: 0` stays
visibly unsubstantiated. `auditCycleDivergentTotal` is also what keeps a finding from
self-erasing: the per-pass count reads zero on every pass that did not re-examine the
divergent anchor, so `LensProjectionDiverged` is raised on either number and only a NEW cycle
over the same anchors supersedes a cycle's verdict. `auditListingSize` is how
many anchor keys the type filter matched (a pathologically large anchor type is visible
rather than merely expensive), and `auditCoverageBasis` is always `"key-type"` — a real
boundary, not a formality: the executor also admits a vertex whose *body* `class`/`label`
equals the pattern label, and such an anchor is never enumerated and never audited. The
result is under-coverage, never a wrong verdict, and publishing the basis is what keeps
"audited clean" readable as the bounded claim it is.

`auditEnrolled` is published for **every** lens, and `auditRefusal` beside a false one. The
enrolment gate is fail-closed on six conjuncts (§4.4) and every one is re-checked at the top
of every pass, not only at install — they are all mutable pipeline state, so a hot reload
could otherwise leave a lens auditing under a shape it no longer has; a failed re-check
self-suppresses with the reason instead. Most of the corpus refuses by design: an
actor-aggregate lens is the sweep's, a Postgres or subject target cannot read a row back, a
Secure Lens must not have plaintext re-derived by a background job outside a request context,
and a query returning `$now` or `$projectedAt` would read divergent forever because a
recomputation cannot reproduce either. A lens on the **auth plane** is refused outright
(`projection.IsAuthPlane` — `nats_kv` into `capability-kv`, or a Postgres grant table): its
per-row verdicts belong to the convergence sweep and to the `Capability*` codes, which carry
an escalation this detector deliberately does not. `CapabilityProjectionDiverged` /
`CapabilityAuditStalled` exist on the capability path as defence in depth for that refusal
being wrong — a proven divergence on the authorization read model must reach an issue with a
severity and a `since` clock, never sit in the metric map as a number nothing alerts on. A refused lens publishes its reason, raises no verdict,
and can never read as audit-stalled — *not audited* must stay distinguishable from *audited,
clean*.

`LensAuditStalled` is the audit's own liveness, and covers the blind spot every audit field
shares: they all describe the last pass, so an audit that stops passing republishes its final
verdict — including a clean one — indefinitely. `auditLastPassAt` is the clock that
contradicts it and `auditSuppression` names why the most recent tick verified nothing (a
rebuild in flight, a paused lens, an unreadable health entry, or a failed enrolment
re-check). It uses the sweep's own staleness rule scaled off the audit's cadence — **10 audit
intervals**, ~2.5 hours at the default — and stays `warning` on the business path, naming a
fresh suppression reason when one explains it and saying the audit is not ticking when none
does. A paused lens is exempt and re-baselines the clock, exactly as the sweep's stall
detector does.

An anchor the audit could conclude nothing about is counted in `auditUnverified`, with the
governing cause in `auditUnverifiedReason`, and it is counted as **neither clean nor
divergent**. It raises the `unverified` alert but no issue of its own: `LensAuditUnverified`
belongs to the sweep, and one code written by two independent detectors would be two `since`
clocks disagreeing about when the condition began.

**`alert` precedence**, worst first, is a single total order shared by both maps:

`secure-redaction` > `paused` > `unreadable` > `repair-failing` > `repair-blocked` >
`sweep-stalled` > `audit-stalled` > `unverified` > `diverged` > `lagging` >
`structural-pause-auto-recovered` > `ok`.

The field is single-valued and several conditions can hold at once, so the order ships as a
table with a test (`alertRank`, `TestAlertRank_TotalOrder`) rather than as the call sequence
of the branches that set it. Nothing displaced is lost: each condition raises its own issue
and its underlying counters travel in the same metrics map.

The order reads as one argument. `secure-redaction` tops it because that read model is not
stale or frozen but **confidently wrong** and being served that way — a null the reader
cannot distinguish from a lawful erasure — where a frozen lens misleads nobody. Below it,
`paused` and `unreadable` say that everything quieter is a claim made on suppressed or
missing evidence. Then the two repair verdicts (a row that is wrong *now*, its repair
errored or declined), then the two detector-halt verdicts — `sweep-stalled` above
`audit-stalled` only because the sweep both detects and repairs, so its silence stops
repairs too, while the audit is read-only. `unverified` (the detector ran and could conclude
nothing) outranks `diverged` (it concluded, and the conclusion is a named, bounded
wrongness an operator can act on): an unknown of unknown size beats a known of known size.
`lagging` is a read model merely behind and catching up, and
`structural-pause-auto-recovered` is the quietest token and the only one describing a lens
that is fine *right now* — it reports a window that has already closed, so anything
currently wrong displaces it.

`CapabilitySweepStalled` watches the **detector's own liveness**, which is the blind spot
every code above shares: each of them reports what the sweep's last completed pass found,
so a sweep that stops passing keeps republishing that finding indefinitely. A tick the
sweep *skips* records no verdict at all — it is suppressed while a rebuild is in flight,
while the lens is paused, and (fail-closed) whenever its own health entry is unreadable —
so a sweep held for hours reads exactly like a converged one: zero divergences, zero
failures, `alert: "ok"`. `metrics.capabilityLens.<name>.sweepLastPassAt` is the clock that
contradicts it (the last pass that reached a verdict, empty if none ever has) and
`sweepSuppression` the reason the most recent tick was skipped (empty when it ran). The
issue raises once that clock exceeds **10 sweep intervals** (~10 minutes at the 60s sweep
default; `CapabilitySweepStallCycles` overrides the multiplier), scaled off the sweep's own
cadence rather than a second tuned duration, and generously, because a suppressed sweep is
a *detector* outage rather than a data outage.

Severity keys on whether anything explains the stall, and for how long:

- **No fresh suppression reason ⇒ `error` immediately.** The sweep should be ticking and is
  not — a dead goroutine, or a pass wedged inside a read. Nothing will clear it on its own.
  `sweepSuppression` is only trusted for ~2 intervals after it was recorded, because the
  reason describes the *last* tick: a sweep wedged mid-tick keeps publishing the previous
  tick's reason, and taking that at face value would report a wedged sweep as a merely
  suppressed one.
- **A named cause ⇒ `warning`, escalating to `error` at 3× the window** (~30 minutes at the
  defaults). A named cause is something an operator can clear; still unswept half an hour
  later, nobody is clearing it.
- **`status: "rebuilding"` ⇒ `warning`, never escalating.** A rebuild is a *superset* of the
  sweep (truncate + full rescan), so a long one is not an unverified projection — the read
  model is mid-refill, which is worth a degrade, but how long a rebuild may legitimately run
  is the rebuild's own signal to own, not a verdict this detector can reach.
- **`status: "paused"` ⇒ exempt entirely.** The suppression is deliberate, indefinite by
  design, and already an error via `CapabilityLensPaused`; reporting it twice would make one
  operator action look like two failures. The exemption also **re-baselines the clock**, so
  resuming a lens paused for an hour does not immediately read as stalled for that hour —
  its first pass after the resume is still an interval away.

A lens with **no sweep plan installed** has no cadence to be late against and is never
evaluated (`sweepLastPassAt` and `sweepSuppression` stay empty for it). That is every
non-auth-plane target, and also the auth-plane **operation-aggregate** role-by-operation
index, which is keyed by `operationType` rather than by an actor anchor and so has no anchor
walk for a sweep to verify. A lens that has never swept is measured from when the
heartbeater first saw it, not from process start, so a lens installed mid-run gets its own
grace window.

`CapabilityLensUnreadable` covers the case where the lens's liveness inputs — its health
entry, or its consumer's pending count — cannot be read this cycle. Such a lens is reported
with `status: "unknown"`, `alert: "unreadable"`, a `unreadable` field naming the failed
read, and `consumerLag: null` — an explicit null, because "we could not read the lag" and
"the lag is 0" are opposite facts that must not render identically. It is a `warning`, not
an error: what failed is the observation path, and the projection itself may be perfectly
healthy. The alert outranks every other value for that lens, since nothing else read this
cycle is trustworthy — but the sweep verdicts still apply, because they come from the
in-process sweeper rather than the health entry, so a live repair failure is not lost to an
unreadable reporter. The one thing that must never happen is the lens *vanishing* from
`metrics.capabilityLens`: an absent auth-plane lens is indistinguishable from one that was
never installed, which is the monitoring equivalent of reporting healthy.

### `health.weaver.<instance>` — Weaver heartbeat

```json
{
  "key": "health.weaver.<instance>",
  "component": "weaver",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "starting | healthy | degraded | unhealthy | shutdown",
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
  "issues": [{"severity": "warning | error", "code": "<code>", "message": "<string>", "since": "<RFC3339>"}]
}
```

`metrics` keys are present only when their subsystem has data (e.g. `marksInFlight` is omitted if
the scan failed; `timers*` only when the temporal lane is wired).

#### Issue scope: per-entity vs per-target

Weaver's issue latches are keyed internally by what the raised fact is ABOUT, and the scope
decides whose close retires the issue. The key never appears on the wire — it decides how many
`issues[]` entries a condition produces and when each one clears.

| Key shape | Scope | Codes |
|---|---|---|
| `gap:<targetId>.<entityId>.<gapColumn>` | one ROW | `UnroutedTasks` and every other `surface` gap's declared `issueCode`; `GapBudgetExhausted` |
| `gapConfig:<targetId>.<gapColumn>` | the target's PLAYBOOK / deployment | `GapWithoutPlaybook`, `UnresolvedReference`, `PlaybookConfigError` |
| `data:<targetId>.<entityId>.<column>` | one ROW's data | `RowDataError` (a column whose value is not its §10.2 type, an unusable `freshUntil`, a violating row carrying no `entityKey` echo), `TemplateDataError` |
| `effect:<targetId>.<gapColumn>.<actionRef>` | one declared remediation | `LensEffectMismatch` |

A `surface` gap standing open is a fact about ONE subject, so N subjects violating the same
`(target, gap)` raise N entries carrying the SAME `code` — an `issues[]` code is not unique within
a document, and each entry's `message` names its `entity <entityId>`. Each retires on its own
subject's close, so one subject's remediation landing never clears the issue raised for a subject
still stuck. A config fact is identical for every row of the target and only a package re-author
can fix it, so it is raised once per `(target, gap)` however many rows are violating.

The same split governs `data:`. A malformed column value is a fact about the one projected row
carrying it, repaired for that row alone by the next projection, so it is keyed per
`(target, entity, column)` and repairing one row never retires another's. **The read is the
retirement**: a column that parses, or that the next projection drops, clears that row's entry.
Most of the columns these readers surface — `violating`, `inflight_<g>`, `maxretries_<g>`,
`admissionPriority` — have no gap-close or plan-success path of their own, so without that the
entries would accumulate one per `(row, column)` for the process's lifetime. The listing cap below
bounds the *document*, never the cache behind it.

The **missing-`entityKey` echo** is keyed the same way: the entity the body omits is supplied by the
row *key*, and the raise/clear decision is taken on every delivery that reaches reconciliation (the
row key parses, the target is registered, the body parses) — violating or not, target disabled or
not — so a repaired row retires its own entry. Read each entry as "this row projected without its
`entityKey`", so N such rows are N entries.

#### Teardown (`Revoke`)

| Family | Retired on revoke by | Why |
|---|---|---|
| `gap:`, `gapConfig:`, `data:` | **prefix clear** (also on registry removal — `reconcileConsumers` retires the same prefix set, so either teardown route leaves nothing standing) | keys carry a segment below the target, so there is no single key to name; a revoked or unregistered target delivers no rows and keeps no marks, so nothing on the live path would ever retire them |
| `consumer:`, `timer:`, `target:<ownerVertexId>` | key clear | keyed by target alone |
| `effect:` | nothing — **self-reconciling** | `flagEffectMismatches` rebuilds its alert set from a scan every heartbeat and clears whatever the scan no longer lists; `Revoke` deletes the target's `__effect` windows, so its entries self-clear on the next heartbeat |
| `sweep:` | nothing — **self-reconciling** | the sweep reconciles `corruptAlerted` against the marks each pass listed; `Revoke` deletes the target's marks, so its `CorruptMark` entries clear on the next pass |
| `pendingSpec:` | nothing — **not target-keyed** | keyed by the meta-vertex id, and `Revoke` does not touch the vertex or its spec; it clears when the spec drains or is evicted |
| `oscillation:` | **nothing — KNOWN STRANDED** | see below |

Each prefix carries its trailing `.` separator, so revoking `t1` does not touch `t10`.

**`oscillation:` is a known-stranded family.** `TargetOscillation` is raised when two targets fight
over the same subject path, and **nothing clears it anywhere** — not on revoke, not on the live
path. A prefix clear cannot reach it either: its key is `oscillation:<targetA>.<targetB>.<path>`
with the pair sorted, so a revoked target may be the *second* segment. Retiring it needs either a
scan of the keyspace or a second key form, and neither is built. Until then, a `TargetOscillation`
entry stands for the process's lifetime — including after one or both targets are revoked. Treat
it as "this fight was observed since `since`", not as a live condition.

A retired entry is not a suppressed one. Every issue here is level-driven: if a revoked target is
re-enabled and the condition still holds, the next delivery raises it again. Retiring on teardown removes an entry that describes a
target that no longer exists; it does not decide that the underlying fault was fixed.

#### `IssuesTruncated`

The per-entity classes are unbounded in entity count, so a heartbeat lists at most **50** issues.
When more are open, one extra synthetic entry closes the list:

```json
{"severity": "warning | error", "code": "IssuesTruncated",
 "message": "<n> further open issues are not listed in this heartbeat (<total> open in total, 50 listed): <Code> ×<n>, <Code> ×<n>, …",
 "since": "<RFC3339 — the oldest unlisted issue's first-arose stamp>"}
```

**Which 50 are listed is decided by severity first**, ties broken on the deterministic key order.
That ordering is the point of the cap: the unbounded families are all `warning`s, and in key order
they sort *ahead* of the entries that explain a fault — a `gapConfig:` `PlaybookConfigError`, a
`timer:` failure, a paused consumer. Selecting by key alone would let sixty unrouted tasks evict the
one `error` naming the cause, leaving a document that reports `unhealthy` while listing fifty
identical warnings. With severity-first selection an `error` is dropped only once more than 50
errors are open at once.

The marker names the distinct **codes** that went unlisted, with counts, most-numerous first — an
operator who cannot see every instance can still see what kind of thing is missing. That code list
is itself bounded (8 codes, then `+N more codes`), since a `surface` gap's `issueCode` is
package-declared and the vocabulary is open-ended.

Its `severity` is the worst among the issues it stands for, so an `error` among the unlisted is
never presented as a warning. `status` is aggregated over **every** open issue, not over the listed
sample, so §5.3's issues-empty-iff-healthy invariant holds against the full set.

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

### `health.bridge.<instance>` — Bridge heartbeat

```json
{
  "key": "health.bridge.<instance>",
  "component": "bridge",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "starting | healthy | degraded | unhealthy | shutdown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "consumers": {"<consumerName>": "running | pausedManual | pausedStructural | pausedInfra"},
    "dispatched": <int>,
    "pending": <int>,
    "skipped": <int>,
    "adapterErrors": <int>,
    "timedOut": <int>
  },
  "issues": [{"severity": "warning | error", "code": "<code>", "message": "<string>"}]
}
```

### `health.gateway.<instance>` — Gateway heartbeat

```json
{
  "key": "health.gateway.<instance>",
  "component": "gateway",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "healthy | degraded | unhealthy",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "requests_total": <int>,
    "auth_failures_total": <int>,
    "ops_submitted_total": <int>
  },
  "issues": [{"severity": "warning", "code": "GatewayRevocationDisabled", "message": "<string>"}],
  "revocation": {
    "consumerConnected": <bool>,
    "revokedCount": <int>,
    "lastEventSeq": <int>,
    "lastSyncAt": "<RFC3339 | \"\">"
  },
  "jwks": {
    "keys": [
      {"kid": "<string>", "source": "jwks | static", "alg": "<string | \"\">", "addedAt": "<RFC3339 | \"\">"}
    ],
    "lastPoll": "<RFC3339 | \"\">",
    "swaps": <int>
  }
}
```

### `health.object-store-manager.<instance>` — Object-store-manager heartbeat

```json
{
  "key": "health.object-store-manager.<instance>",
  "component": "object-store-manager",
  "instance": "<instance>",
  "version": "0.1.0",
  "status": "healthy | degraded | unhealthy",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "reclaimed_total": <int>
  },
  "issues": [{"severity": "warning", "code": "ObjectDeleteFailed | ReconcileListFailed", "message": "<string>"}]
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

### `health.facet.<instance>` — Facet heartbeat

```json
{
  "key": "health.facet.<instance>",
  "component": "facet",
  "instance": "<instance>",
  "version": "1.0",
  "status": "starting | healthy | degraded | unhealthy | shuttingDown",
  "heartbeatAt": "<RFC3339>",
  "startedAt": "<RFC3339>",
  "uptime": "<ISO-8601-duration>",
  "metrics": {
    "mode": "host-engine | browser-native",
    "engines_active": <int>,
    "engines_pinned": <int>,
    "engines_sync_degraded": <int>,
    "engines_nats_disconnected": <int>
  },
  "issues": [{"code": "EngineSyncDegraded", "severity": "warning", "message": "<string>", "since": "<RFC3339>"}]
}
```

The four `engines_*` metrics are present only in host-engine mode; browser-native mode's `metrics`
carries `mode` alone (the engines live in-page, invisible to the host — see the component section
above).

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
  "projectionLag": <uint64>,
  "peakBindingRows": <uint64>,
  "lagProgressAt": "<RFC3339>",
  "ackPending": <uint64>,
  "ackFloorProgressAt": "<RFC3339>",
  "sweepCursor": "<anchorVertexKey>",
  "sweepReconciled": <uint64>,
  "personalSweepCursor": "<identityId>",
  "personalSweepCycleCompletedAt": "<RFC3339>",
  "personalSweepQueueDepth": <uint64>,
  "secureRedactions": <uint64>,
  "structuralAutoRecoveredAt": "<RFC3339>",
  "structuralAutoRecoveredCause": "<string>",
  "structuralAutoRecoveryAttempts": <int>,
  "filterMode": "narrowed-relation | narrowed-label | broad",
  "filterLabelCount": <int>,
  "filterBroadReason": "not-eligible | non-exhaustive | label-cap | taxonomy-unarmed | taxonomy-unresolvable | install-incomplete | registration-failed"
}
```

`secureRedactions` is the cumulative count of secure-column values this lens projected as
**null because it could not resolve them** — a malformed envelope, a holder type the column never
declared, a missing or unparseable `piiKey`, or a failed authenticated decrypt
(`retention-class-key-custody-design.md` §6.2, fork F2). Omitted while zero, which is the entire
current corpus.

A **legitimate shred is not counted**: erasure projecting null is the mechanism working, and
counting it would bury the defect signal under the expected case. So any nonzero value is a defect
somewhere between a package's custody declaration and its ciphertext. It is the only signal that
distinguishes the two, because the redaction is otherwise **silent at the read model** — the row
still renders, carrying a null exactly where an erased record would carry one.

That is why the derived `LensSecureRedaction` issue is `error` where every other business-lens code
is `warning`, and why the per-lens `alert` token `secure-redaction` outranks even `paused`: the
other conditions describe a read model that is stale or frozen, which is visibly wrong to whoever
reads it; this one describes a read model that is confidently wrong and still being served. The
issue is raised on the CUMULATIVE count, so it persists across cycles in which no new redaction
happens — the null stays in the read model until the cause is fixed and the lens reprojects.

`pauseReason` is `null` when active; `"infra"`, `"structural"`, or `"manual"` when paused.
`lastError` is `null` when no error has occurred. On a **structural** pause it is not merely the
last error but the pause's **diagnosis** — the lens projects nothing until the condition is
reconciled, so that text is the whole of what anyone has to act on. It is therefore guaranteed for
the life of the pause: the clean-registration clear that retires a stale message on every other lens
skips a structurally paused one, and a second pause raised over it (an operator `pause`, which
carries no cause of its own) preserves it rather than nulling it. Read it with
`lattice lens health <lensId>`, which renders `pauseReason` and `lastError` together; the
`LensProjectionPaused` issue message carries it truncated, so a health summary names the failing
column or table, not only the tier. **Who** reconciles it depends on the lens: a protected or
grant-table Postgres lens adjudicates the condition itself and can resume with no operator, which is
what the three `structuralAutoRecovered*` fields below record; every other lens waits for
`lattice lens resume`.

`structuralAutoRecoveredAt` / `structuralAutoRecoveredCause` / `structuralAutoRecoveryAttempts`
record the one recovery a health entry could not otherwise express: a **structural pause that
cleared with nobody involved**. A protected or grant-table lens's probe is a full posture
verification of its target — table, columns, RLS state, the unique constraint its writes need — so a
pass genuinely settles the condition that raised the pause, and the consumer resumes on its own
(`structural-pause-recovery-design.md` §4.2). Every field the entry carries then reads exactly like
a lens that never faulted: `status: "active"`, `pauseReason: null`, and `lastError` gone with the
pause. These three are what survives it. Absent for a lens that has never self-healed, which is the
entire corpus until one does.

They are not a state, they are a **record of an event**, and they persist: every wholesale writer of
this entry (`SetActive`, `SetPaused`, `SetRebuilding`) carries all three forward, which matters
because a self-heal that does not hold re-pauses within about one probe interval — so a stamp any of
them dropped would be observed by no heartbeat at all, and the attempt count would be unreadable in
exactly the flapping case it exists to report.

`structuralAutoRecoveredCause` is what says whether anything is still **owed**, and the answer is not
always "a rebuild". The pause's own backlog **does** replay: the failing message was never acked, the
ack floor never advanced, and everything published while the lens was dark is still pending when it
resumes. So a cause an operator fixed in the schema — a dropped column, a dropped unique constraint —
leaves every row intact and owes nothing. What is genuinely at risk is what the *recovery itself*
destroyed: if the condition was cleared by **re-provisioning or restoring** the target, the rows that
predate the pause were acked long ago, will never redeliver, and a rebuild is owed whose scope is the
**whole lens, not the outage window**. Reading this backwards in either direction is expensive — one
way cries wolf on the common case, the other scopes the repair too narrowly and leaves it half done.
On the likeliest recovery of
all — a lens that was already paused when Refractor restarted, healed by its probe on the way back
up — that cause comes from the entry's own `lastError`, read at restore: the process that resumes
the lens is not the one that paused it and holds no diagnosis of its own. It is readable at all
because a structurally-paused entry keeps its `lastError` for the life of the pause, so that
guarantee is load-bearing for this field, not only for the paused entry itself.
`structuralAutoRecoveryAttempts`
is which self-heal attempt lifted the pause, from 1: it is the lens's distance from the consumer's
relapse latch, which stops probing altogether once a run of self-heals has each failed to hold and
hands the lens back to a human. A recovery reported at 1 healed cleanly; one reported near the limit
is a lens flapping.

The derived `LensStructuralPauseAutoRecovered` issue is the announcement for a business lens, and it
and its auth-plane sibling below are the only codes that fire on a lens whose read model is
**healthy at the moment they fire** — which is precisely why they exist. An auto-heal nobody can see is how a frozen row comes to render
green. It is `warning` (⇒ `degraded`) and deliberately short-lived: raised only while
`structuralAutoRecoveredAt` is younger than **two heartbeat intervals**, carrying the lens name, the
cause and the attempt count. Two rather than one because the recovery is stamped at an arbitrary
point inside a cycle, and a strict one-cycle window can be straddled and emit *nothing* — the silent
self-heal the signal exists to refuse; two guarantees at least one emission and at most two. The
`alert` token `structural-pause-auto-recovered` is the quietest in the table, below `lagging`:
it reports a window that has already closed, so anything currently wrong outranks it in the
single-valued `alert` field — but both issues are still raised, because a lens that healed once and
is down again is a shape an operator needs to see whole. The stamp itself outlives the issue: the
issue answers "did this just happen", the fields answer "what happened and is a rebuild owed".

`CapabilityLensStructuralPauseAutoRecovered` is its auth-plane sibling — same fields, same window,
same `alert` token — and it is the reason this signal is not optional. Every grant-table lens is auth-plane by
definition (a `postgres` target with `grantTable` set), and a grant-table lens is exactly the class
whose probe verifies its own posture — so the lenses that project `actor_read_grants`, the read-path
authorization source of truth, are the ones most able to clear a structural pause unattended. The
conditional above is the whole of it for them: a schema fix replays its own backlog and owes nothing,
but a grant table that was **re-provisioned or restored** has lost every grant written before the
pause, and those never redeliver — a persistent **under**-grant, which is the safe direction (reads
fail closed, never open) and exactly what a manual resume would have produced, but silent. This issue
is the only thing that says so. Its severity stays `warning` there too,
where every other capability code escalates to `error`: those describe an authorization read model
that is frozen, stale or wrong *now*, while this one describes a lens that **successfully
recovered**. Taking the instance `unhealthy` for a self-heal that worked is a false alarm, and a
signal that cries wolf on the working path is one operators learn to skip — which would restore the
silence by another route.

`lastProjectedAt` is the wall-clock of the
lens's last successful target write — `""` until its first projection (design:
lens-projection-liveness-design.md §3.2); a freshness signal, never an alert input on its own
(a genuinely quiet, no-match lens naturally has an old value). `projectionLag` is the
operator-facing alias of `consumerLag` (same NumPending value under both names). `lagProgressAt`
is when `consumerLag` was last observed to decrease — stamped at the lens's first lag poll too,
and re-stamped on every fall, never on a plateau or an uptick (mirrors the rebuild-progress clock
`internal/refractor/pipeline`'s `recordRebuildProgress` uses for the same purpose). `""` before the
lens's first poll. A freshly-activated consumer reads against a bucket-wide Core KV filter, so on a
cold bring-up it must skip every key of every type it does not match before its own backlog reaches
0 — a real, multi-hour `consumerLag` that is harmless as long as it keeps falling ("cold bring-up
replay debt"). `lagProgressAt` is what lets a reader (`lattice health summary`, Loupe) distinguish
that from a genuine stall: `consumerLag > 0` renders yellow only once `lagProgressAt` has gone
longer than the reader's stall window (2 minutes) without advancing; an absent or unparseable
`lagProgressAt` — a Refractor instance that predates this field, or a lens whose first poll hasn't
landed — carries no evidence of active draining and renders yellow immediately, matching the
pre-existing `consumerLag > 0` behavior.
`ackPending` / `ackFloorProgressAt` describe the work the consumer has already been HANDED, which
`consumerLag` structurally cannot see. `consumerLag` is `NumPending` — undelivered backlog — so a
consumer that has been delivered everything and cannot finish it reports `consumerLag` 0 and is
indistinguishable from one that is genuinely drained; `lagStalled` is never even consulted, because
its gate is `consumerLag > 0`. `ackPending` is `NumAckPending`, the messages delivered but not yet
acked, and `ackFloorProgressAt` is when the consumer's ack floor was last observed to advance —
stamped at the lens's first poll and re-stamped whenever the floor MOVES (a rebuild recreates the
durable and resets the floor, so a floor that moved backwards is a new consumer generation, which
is itself forward progress). Together they are the forward-progress clock for delivered work, the
counterpart to `lagProgressAt`'s clock for the undelivered backlog, and a reader renders yellow on
`ackPending > 0` with `ackFloorProgressAt` older than the same 2-minute stall window. Unlike
`lagProgressAt`, an ABSENT `ackFloorProgressAt` renders green, not yellow: the field is newer than
the deployed fleet, and holding any lens with a message in flight yellow would fire on the normal
mid-processing state rather than on a stall. The two fields are written as a pair or not at all — a
poll that cannot read them leaves both alone rather than writing `ackPending` 0 over a real
observation (design: lens-consumer-ack-window-design.md §3).
`peakBindingRows` is the lens's **evaluation cost** gauge, and the counterpart to
`projectionLag`'s throughput one: lag says the lens is behind, this says how expensive a single
evaluation is. It is the largest binding set any of the lens's recent evaluations materialized at
one time — the high-water mark, over a rolling window of the most recent evaluations, of exactly
the per-stage row count the full engine's binding-set cap refuses on
(`REFRACTOR_MAX_BINDINGS`, 1,000,000 by default). Read it against that cap: a lens sitting within
an order of magnitude of it is materializing a product it almost certainly does not need, and a
lens that has just been refused reports the row count that refused it rather than leaving an
operator to reconstruct the incident by hand.

It is a **peak**, not a total and not the projected row count. A stage that expands into a wide
cross product and then folds it into a handful of aggregated rows reports the wide number — which
is the point, since that width is what the evaluation actually held in memory. For a multi-walk
Personal lens it is the widest single branch, not the sum: the branches are evaluated one after
another and their binding sets are never co-resident.

It is a **gauge, so it falls**. The window is rolling — a spike ages out once newer evaluations
displace it — and the published value is the window's current maximum, overwritten each poll
rather than accumulated. The window is also per-process and is **never published while empty**, so
a restart, a pause, or a quiet lens leaves the last real observation standing instead of blanking
it to zero; a rebuild deliberately CARRIES the window, because a rescan walks the widest anchor set
the lens ever sees and those are the samples worth having. ABSENT means no evaluation has reported
one — a lens that has not evaluated, or an entry written by a Refractor that predates the field.
Like every other field here it is observation, not control: nothing in the engine or the pipeline
reads it back.

`sweepCursor` / `sweepReconciled` are the auth-plane convergence sweep's round-robin
position and cumulative heal count; both are omitted for a lens that does not sweep. They
live on this existing entry rather than in new state, so a restarted Refractor resumes the
walk where it stopped instead of re-verifying from the head every boot, and the heal count
an operator reads survives the restart. Neither is reset by a status transition — a
pause/resume must not silently restart the walk.

`personalSweepCursor` / `personalSweepCycleCompletedAt` / `personalSweepQueueDepth` are the
**personal** plane's convergence walk, and read differently from the pair above: one
process-level sweeper covers every Personal Lens, and it writes the same three values onto
each of their entries. So the cursor is the last **identity** it re-drove, not a per-lens
position, and a lens carrying it is saying "the plane's backstop is alive and has got this
far", not "I swept". `personalSweepCycleCompletedAt` is what a moving cursor is worth: a
tick covers at most a batch, so only a closed cycle says the walk has covered the
population. Neither is restored at boot — a restart re-verifies from the top of the
population, which is the safe direction. `personalSweepQueueDepth` is the D1 grant-change
drain's backlog at the moment the sweep published: the fast path's gauge, carried on the
sweep's write because the sweep is the only thing here that reports on a schedule. A depth
that keeps climbing is a mass grant change outrunning the drain, which ends in the
coalescing set overflowing — and that overflow raises its own `errorCount`/`lastError`
fault. All three are omitted for every lens the personal sweep does not drive.

`filterMode` / `filterLabelCount` / `filterBroadReason` are the lens's **Core KV consumer
footprint** — which server-side filter its own derivation chose, and, when that is the broad one,
why (design: `dynamic-type-taxonomy-design.md` §10.3). They are written wherever the filter is
derived: at activation, at every rebuild (a MATCH hot-reload can widen or narrow the referenced
label set between the two), and again if a narrowed registration is refused and falls back. The
three are one decision and are always written together.

They are **observation, not control**. The set of subjects a lens filters on is identical whether
or not these fields are written, and no gate anywhere reads them back — which is what makes them
safe to read as evidence: they report the auth plane's narrowing decision without participating in
it. A filter that narrows differently than the lens's own client-side gate would is an under- or
over-grant, so the value of writing the decision down is that a regression becomes visible in Health
KV rather than only in a delivery that never arrives.

An **absent** `filterMode` means the lens has **never derived a consumer filter** — an entry written
by a Refractor that predates these fields, or a lens that has not reached its derivation yet. It
does **not** mean `broad`: a lens on the broad filter says so, and carries a reason. `filterMode` is
`narrowed-relation` when the filter pins relations as well as vertex types, `narrowed-label` when it
pins types alone (an untyped or variable-length relationship anywhere in the cypher, or a
`labels x (1 + 2 x relations)` subject count over the budget), and `broad` when the
lens subscribes the whole `$KV.<bucket>.>`. Both narrowed modes are reachable by plain and
actor-aware lenses alike: each shape's link arm carries the matching relation conjunct, so a
relation-pinned subject withholds only links that arm skips anyway.
`filterLabelCount` is how many labels the narrowed
filter was BUILT from — a `*` label contributes its taxonomy-resolved concrete types rather than
itself, so this can move without the cypher changing — and is `0` (omitted) on every broad entry.

`filterBroadReason` is a **closed, total** vocabulary: a broad entry always carries exactly one, and
it names the cause the derivation actually acted on, not every condition that happened to hold.

| reason | what it means | who fixes it |
| --- | --- | --- |
| `not-eligible` | the lens cannot narrow as it stands — not on the full engine, or an actor-aware lens missing one of the conjuncts its INSTALLATION supplies (pattern-closure, a sweep plan, its anchor type in the label set, a declared secure holder type in it) | whoever wires the lens; often intentional and permanent |
| `non-exhaustive` | the compiled rule can bind a type no label names (an unlabeled node, a variable-length hop, a name re-seeded after its labelling clause went out of scope), or a `*` label resolved to zero concrete types | the cypher author |
| `label-cap` | the derivation was exhaustive but resolved to more labels than the narrowed filter carries | nobody, necessarily — a **footprint regression**: another package's install can push a one-label lens over the cap |
| `taxonomy-unarmed` | every referenced label resolved, but the resolver's answer is not guaranteed **current** — a snapshot is loaded and the invalidation consumer is not live | **nobody: this one clears on its own** the moment the resolver arms. The only reason in the vocabulary that needs no edit anywhere |
| `taxonomy-unresolvable` | the taxonomy could not answer **at all** — a `subtypeOf` cycle, an over-depth chain, an ambiguous `canonicalName`, a vanished abstract type, or no snapshot ever loaded | **a package author, and waiting never helps.** This never clears by itself; it is the reason `taxonomy-unarmed` must not be used for it |
| `install-incomplete` | the filter was derived before the lens's install stages finished: its cypher **declares** an actor anchor (`{key: $actorKey}`) while its enumerator is not installed, so none of the actor-aware conjuncts could be evaluated and the derivation **refused to answer** rather than answering wrong | **a `cmd/refractor` maintainer — nobody else can.** The only reason in the vocabulary that reports a HOST wiring bug rather than a property of the lens: no lens author can fix it, no cypher edit clears it, and no amount of waiting or data changes it. An install stage was ordered after the filter derivation instead of before it |
| `registration-failed` | the lens DID derive a narrowed filter and JetStream refused to register it | an operator: this is the one reason that is also a **fault**, and the only one that raises `errorCount`/`lastError` alongside |

The two taxonomy reasons are deliberately separate words for the two halves of §4.2's fork, and the
distinction is the whole point: `taxonomy-unarmed` is a lens waiting for something that will arrive,
`taxonomy-unresolvable` is a lens waiting for something that will not. An operator handed the first
where the second is true waits forever.

When more than one reason holds at once, the one reported is the one that **survives fixing the
others** — `install-incomplete` outranks `non-exhaustive` outranks `taxonomy-unresolvable` outranks
`taxonomy-unarmed`. So a lens whose cypher is inexhaustive AND whose resolver is unarmed reads
`non-exhaustive`: arming would not change its verdict, and reporting the transient cause would point
at the wrong repair.

`install-incomplete` sits at the top of that chain for a different reason than the rest of it. The
other three are competing accounts of one derivation that ran; this one says the derivation **did not
run at all**, because the inputs it needed were not installed yet. Every verdict below it would have
been computed from a pipeline shape the lens does not have — an actor-aware lens read as plain — so
none of them is a claim about this lens, and reporting one would send an operator to edit a cypher
whose verdict was never in question.

Only `registration-failed` raises `errorCount`. A cap-driven or eligibility-driven fallback is a lens
reading more of the stream than it needs while projecting every row it should, so it is deliberately
kept out of `errorCount` — putting it there would file a correct lens beside a DLQ write. The
broadening still matters, because a lens whose `filterMode` moves from narrowed to `broad` on a
cypher edit has silently multiplied its delivery volume; `filterBroadReason` is what says whether
that was authored, resolved, or refused.

`install-incomplete` is the one reason that is a **defect without being an `errorCount` fault**. The
lens itself is healthy — it projects every row it should, off a wider filter — so counting it as a
fault would misreport a working lens; but unlike every other broad reason it is nobody's intended
state, so Refractor logs it at `ERROR` (`ruleId` on the line) rather than the `WARN` a footprint
regression gets. An entry carrying it means the deployment is running a lens no ordering in
`cmd/refractor` was supposed to produce.

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
| **green** | All non-event-driven components have a health entry fresher than `--stale-threshold`; no active alerts; every lens has `consumerLag=0`, or a nonzero `consumerLag` still draining (its `lagProgressAt` clock has not gone stalled — see `lagStalled` below) |
| **yellow** | Any component entry is stale (age > threshold) OR any lens's `consumerLag > 0` AND its lag has stopped falling (`lagStalled`); OR any active warning-severity alert |
| **red** | Any error-severity alert; any health entry absent (not just stale); any phase gate expected to have passed but not present |

### Component rollup algorithm

1. **Heartbeat keys** (`health.processor.*`, `health.refractor.*`, `health.weaver.*`,
   `health.loom.*`): extract `heartbeatAt`; compute `age = now - heartbeatAt`. If
   `age > staleThreshold` → yellow. For Weaver/Loom, also scan the inline `issues[]`: any `error`
   severity → red, any `warning` → yellow.
2. **Per-lens keys** (bare NanoID): check `status` field.
   - `"paused"` → yellow
   - `"rebuilding"` → yellow
   - `"active"` → check `consumerLag` (> 0 AND `lagStalled` — its `lagProgressAt` clock has gone
     longer than the 2-minute stall window without falling — → yellow; a large but still-falling
     `consumerLag`, e.g. cold bring-up replay debt on a bucket-wide filter, stays green) and
     `errorCount` for detail
   - Independent of `status`: extract `lastUpdated`; `age > staleThreshold` → status
     `"stale"`, yellow at minimum (worst-of with whatever the status branch above already
     computed) — an unregistered pipeline's reporter entry freezes at its last-written
     `status`/`consumerLag` forever, so this is the only signal that catches it
     (lens-registry-restart-integrity-design.md §4 Fire B step 3).
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
<lensId> (lens)       active      3s ago        consumerLag=0 errorCount=0
<lensId2> (lens)      stale       50400s ago    consumerLag=0 errorCount=0
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
