# Refractor Failure Tiers

**Component reference** | Audience: implementers + architects

This document classifies the failure modes the Refractor can encounter and the
operational response each requires.

## Base model — four tiers

Refractor inherits the 4-tier failure model from Materializer
(`internal/refractor/failure/`):

| Tier | Source | Lattice meaning | Route |
|---|---|---|---|
| **Infrastructure** | `failure.Infrastructure` | NATS / Postgres / target store outage | fetch-loop pause, buffer in NATS |
| **Structural** | `failure.Structural` | DDL validation failure, lens spec invalid, schema mismatch | pause the affected Lens until reconciled |
| **Terminal** | `failure.Terminal` | Atomic-batch rejection, malformed Core KV event | DLQ for forensics |
| **Transient** | `failure.Transient` | Retryable target write (e.g. transient Postgres error) | deferred retry queue |

## Mapping examples

- **Postgres connection refused** → Infrastructure → fetch-loop pause
- **DDL `permittedCommands` mismatch on lens spec aspect** → Structural → pause this Lens; operator must fix the meta-vertex DDL
- **Malformed payload from CDC** → Terminal → DLQ (the lens's classify path rejected the event)
- **Postgres unique-constraint violation from a network glitch** → Transient → deferred retry per `RetryConfig`

## Health emissions and lag

- Per-instance heartbeat: `health.refractor.<instance>` every 10s
  (`internal/refractor/health/lattice_heartbeater.go`), TTL-purged (NFR-O1).
- Per-lens latency: emitted inline on the `health.refractor.<instance>` heartbeat
  under `metrics.lensLatency` (keyed by lens `canonicalName`) — p95/p99/mean/count
  from the `LatencyRingBuffer` (NFR-P3 instrument).
- Consumer lag: `NumPending` on the lens consumer, polled by `health.LagPoller`
  and surfaced both on `lattice.refractor.metrics.<lensId>` and as the
  `consumerLag` field on the per-lens health entry.

## Delete-projection semantics

Delete projection is **per-lens and mode-dependent** (`targetConfig.deleteMode`),
with **hard delete as the default**. Lineage already lives in Core
KV, so the derived view reflects deletions as removals unless a lens explicitly
opts into tombstones for audit/forensic targets.

- **`hard` (default)** — physically removes the row/key:
  - Postgres: `DELETE FROM "<table>" WHERE <keys>`
  - NATS-KV: `kv.Delete(key)`
- **`soft` (opt-in)** — retains a tombstone:
  - Postgres: `UPDATE ... SET is_deleted=true, deleted_at=NOW()` (requires the
    `is_deleted` / `deleted_at` columns)
  - NATS-KV: PUT a tombstone document `{"isDeleted": true}` (rather than `kv.Delete`)

Both modes are idempotent: deleting an absent row/key is a no-op, not an error.

The **capability plane uses the default hard delete**: the capability authorizer
treats an absent key (`NoCapabilityEntry`) and a tombstone doc identically as
denial (Contract #6 §6.8, "absence equals denial"), and no freshness-ceiling
comparison exists on this plane that would require a tombstone to survive. Hard
delete is the contract-aligned semantics and avoids indefinite tombstone
accumulation in the capability KV.

## Control-plane authorization

The control service capability-gates every control-plane operation (`validate`,
`rebuild`, `pause`, `resume`, `delete`, `register`, `deregister`, health) through a
shared `controlauth.CapabilityKVChecker`: it reads the acting actor's Capability KV
entry and verifies the actor's JWT identity before honoring a control op. This is
default-on (`AuthModeCapability`) and shared across all three control planes
(Refractor / Weaver / Loom) behind the shipped NATS trust floor (FR30). The
data-plane Capability **Lens** that feeds Processor write-path auth is a separate
mechanism and is also live.

An operator reaches these ops two ways. Loupe drives all six from the lens card, but `resume` sits
outside its demo-posture read-only set deliberately — a hosted read-only console should not gain a
mutate verb. The CLI is the other: `lattice lens pause | resume | rebuild | health <lensId>`, each
stamping whichever actor `--actor` (or `--actor-token`) names. The grant required is
`ctrl.refractor.<op>` at scope `any`, matched exactly with no wildcard branch, so root is not
implicitly permitted; on the dev and demo stacks `make dev-seed-console-operator` provisions an
identity holding `consoleOperator`'s grants and persists its key.

A **structural** pause is the one an operator must reach by hand, since nothing in the platform
retires it: the pause survives a process cycle, and re-registration at boot clears a stale lens
*fault* without clearing the pause. Its recorded cause is preserved for the life of the pause (see
`docs/observability/health-kv-schema.md`), so the recovery sequence is `lattice lens health` to read
what failed, fix that — for the common case, a lens that gained a body column and needs
`make provision-readpath` re-run — then `lattice lens resume`. Resuming without fixing the cause
just re-pauses the lens on its next write.

## Privacy / security supersession tiers

Two supersession classifications sit above the four base tiers — both now built.

- **Security-critical — Capability Lens failure.** A projection that feeds
  Capability KV and breaks could let downstream authz fail open, so a
  Capability-Lens failure halts the lens and raises a distinct Health-KV alert
  rather than routing through the base tiers. The `LatticeHeartbeater` raises
  `CapabilityLensPaused` (severity `error` ⇒ instance `unhealthy`) when a
  capability lens is paused and `CapabilityLensLagging` (severity `warning` ⇒
  `degraded`, debounced with a clear-threshold band) when an active one lags past
  the configured threshold — see the "Capability-Lens health" section of
  [refractor.md](./refractor.md). Its generalized sibling
  (`LensProjectionPaused` / `LensProjectionLagging`, warning-only) covers every
  non-auth-plane business lens.

- **Privacy-critical — crypto-shred failure (`CatPrivacyCritical`).** A row whose
  encryption key has been shredded but whose projection still surfaces its values
  is a confidentiality breach. When `internal/refractor/keyshredded` cannot nullify
  a shredded identity's projected row, `failure.Classify` routes the error to
  `CatPrivacyCritical`: the lens is paused immediately, alerted, and **never**
  auto-retried. Vault + crypto-shredding is live; the listener consumes
  `events.privacy.keyShredded` — the one sanctioned event-stream listener in
  Refractor's charter (brainstorm #62), distinct from the Vault key-destruction
  worker that runs co-located with the Processor.
