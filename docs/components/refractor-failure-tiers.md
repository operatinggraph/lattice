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
| **Structural** | `failure.Structural` | DDL validation failure, lens spec invalid, schema mismatch | pause the affected Lens until reconciled — self-adjudicated where the lens's own Probe decides the condition (below), operator-cleared otherwise |
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

A **structural** pause survives a process cycle, and re-registration at boot clears a stale lens
*fault* without clearing the pause. Its recorded cause is preserved for the life of the pause (see
`docs/observability/health-kv-schema.md`), so the operator sequence is `lattice lens health` to read
what failed, fix that — for the common case, a lens that gained a body column and needs
`make provision-readpath` re-run — then `lattice lens resume`. Resuming without fixing the cause
just re-pauses the lens on its next write.

## A structural pause that can adjudicate itself

"Until reconciled" is a claim about a *condition*, not about a person, and for some lenses the
platform already owns the check that decides it. A **protected** or **grant** lens pauses
structurally on `42P01` / `42703` / `42P10` — a table, column or arbiter index that is absent — and
`VerifyProtectedTable` / `VerifyGrantTable` answer exactly that question, read-only and fail-closed.
Those lenses therefore re-run their own probe while paused and resume when it passes, with no
operator involved (`substrate.ConsumerSpec.StructuralProbe`, set in `cmd/refractor/main.go` from the
same `Into` shape as `InitialPause`).

The asymmetry this removes is the one that read as a bug: the *same* absent table at **activation**
was already an infra pause that self-healed the moment someone ran `make provision-readpath`, while
the same table dropped *after* activation stayed dark through restarts and through the operator
fixing it.

Three properties bound it.

- **The opt-in is narrow, and the exclusions are deliberate.** Only protected and grant lenses. The
  plain Postgres adapter's probe is `pool.Ping`, which passes while the condition holds — opting it
  in would produce resume/re-pause churn, not recovery — and a plain lens declares no body columns,
  so completing that probe is not a refactor. NATS-KV and NATS-subject lenses have a real structural
  class that has never been observed live. Omission keeps the operator-only behaviour, so every
  other consumer of the supervisor — Loom, Weaver, Bridge, the Processor — is unchanged.
- **The probe is the security gate, at the same polarity.** A protected lens can only resume by
  passing RLS `ENABLE`+`FORCE`, the §6.14 set-membership policy, the declared columns, and the
  unique index the write path's `ON CONFLICT` depends on. A regressed posture keeps the lens dark.
  A dropped-and-recreated `actor_read_grants` is *empty*, so every protected table's membership
  subquery matches nothing and reads fail closed — under-grant, never over-grant, and exactly what a
  manual resume produces today.
- **If you fixed it by restoring or re-provisioning DATA, you still owe a rebuild — and the
  auto-resume cannot know that.** The probe is a *shape* check: table, columns, types, RLS posture,
  arbiter index. It says nothing about contents. So a pause resolved by restoring
  `actor_read_grants` (or a protected table) from a dump or PITR passes verification and resumes
  with rows as of the backup — including grants revoked since, whose revocations were acked long ago
  and never redeliver. That is an over-grant that persists until someone rebuilds the lens. Resolve a
  structural pause by fixing the **schema** and the resume needs nothing further; resolve it by
  restoring **data** and run `lattice lens rebuild` after. This is not new to self-healing — a manual
  resume after the same restore does the same thing — but self-healing removes the person who just
  ran the restore and would have known.
- **Self-healing is bounded by a relapse latch.** A probe verifies the provisioned schema; it cannot
  see a row-data fault (`23502`, `42804`, `22P02`) or a column the evaluator emits that the lens
  never declared. So after three structural pauses that each followed a probe-driven resume, the
  worker latches: the pause becomes operator-only for the rest of that process, and the recorded
  cause is prefixed `structural pause latched after 3 self-heal attempts:` — the diagnosis *and* the
  fact that the platform tried. An operator `resume` clears the latch. The latch is in-process and
  per-worker, so a restart re-arms it; that is deliberate, since a restart is a deploy or an
  operator act.

A failing structural message is `Nak`ed with the probe interval as its delay rather than left
pending, so a resume is re-tested in ~10s instead of waiting out the 5-minute `AckWait`. Without
that, a lens would publish `active` and learn nothing about whether the fix took for five minutes at
a time. Consumers that do not opt in keep the leave-pending behaviour unchanged.

A recovery that no one can see is how a read model starts rendering green while it is wrong, so a
self-heal raises a heartbeat issue carrying the cause it recovered from and which attempt it was.
**Which code depends on the plane, and a grant lens is not on the plane you might expect:**
`projection.IsAuthPlane` counts every `GrantTable` lens as auth-plane, so all seven of them raise
**`CapabilityLensStructuralPauseAutoRecovered`**, while protected business lenses raise
**`LensStructuralPauseAutoRecovered`**. An operator alerting on only the second one misses exactly
the class that feeds read-path authorization. Both are `warning`: a lens that successfully
recovered is not unhealthy, and raising `error` would turn a working self-heal into a false alarm.

The issue is emitted once the lens has cleared every gate and is about to project — not at the
structural clear, which on the restart path is still one verification short of the truth. The issue
is the *alert*, live for two heartbeat cycles; the durable record is the entry's own
`structuralAutoRecovered*` fields, which survive later status transitions and are what
`lattice lens health` reads.

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
