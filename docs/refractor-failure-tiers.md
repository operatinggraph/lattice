# Refractor Failure Tiers — Story 2.1

This document classifies the failure modes the Refractor can encounter
and the operational response each requires. Story 2.1 inherits the
4-tier failure model from Materializer (`internal/refractor/failure/`):

| Tier | Materializer source | Lattice meaning | Route |
|---|---|---|---|
| **Infrastructure** | `failure.Infrastructure` | NATS / Postgres / Vault / target store outage | fetch-loop pause, buffer in NATS |
| **Structural** | `failure.Structural` | DDL validation failure, lens spec invalid, schema mismatch | pause the affected Lens until reconciled |
| **Terminal** | `failure.Terminal` | Atomic-batch rejection, malformed Core KV event | DLQ for forensics |
| **Transient** | `failure.Transient` | Vault decryption failure (re-fetchable key), retryable target write | deferred retry queue |

## Privacy-critical (alert, no silent retry)

**Crypto-shred failure (Vault `KeyShredded` event handling).** Per AC #6
the crypto-shred path is privacy-critical. If a row's encryption key has
been shredded but Refractor's projection still surfaces decrypted
values, that's a confidentiality breach.

- Classification: **privacy-critical**, supersedes the four base tiers.
- Action: emit an alert via `health.refractor.<instance>.privacy.<lensId>`
  with status `privacy-critical`; halt the affected Lens (no automatic
  retry); page on-call.
- Story 2.1 status: **listener not yet implemented** (out of scope per
  handoff brief "Not for 2.1"). Documented as a gap for Story 2.2.

## Security-critical (alert, halt affected Lens)

**Capability Lens failure.** Per AC #6 Capability Lens failures are
security-critical: if the projection that feeds the Capability KV
breaks, every authz check downstream may fail open.

- Classification: **security-critical**, supersedes the four base tiers.
- Action: emit an alert via `health.refractor.<instance>.security.<lensId>`
  with status `security-critical`; halt the affected Lens; page on-call.
- Story 2.1 status: Capability Lens is not yet defined (Epic 3 territory).
  The failure-tier mapping is documented here so the structure exists
  when the Capability Lens is wired in.

## Mapping examples

- **Postgres connection refused** → Infrastructure → fetch-loop pause
- **DDL `permittedCommands` mismatch on lens spec aspect** → Structural
  → pause this Lens; operator must fix the meta-vertex DDL
- **Malformed `vtx.contract.*` payload from CDC** → Terminal → DLQ
  (the lens's classify path rejected the event)
- **Postgres unique constraint violation transient (network glitch)** →
  Transient → deferred retry per RetryConfig

## Health emissions and lag

Per AC #6 + NFR-O1 + NFR-O3:

- Per-instance heartbeat: `health.refractor.<instance>` every 10s
  (`internal/refractor/health/lattice_heartbeater.go`)
- Per-Lens lag: `health.refractor.<instance>.lens.<lensId>.lag` every
  10s — current implementation surfaces `NumPending` from the JetStream
  consumer via `LagProvider` on the heartbeater and inlines it into the
  per-instance metrics document (`metrics.lensLags`). A separate
  per-Lens health key is a Story 2.2 enhancement.

## Capability KV auth (stubbed)

The control service in Story 2.1 ships with `StubCapabilityChecker`
(allow-all + log). Real Capability KV integration is Epic 3.
See `internal/refractor/control/capability.go`.

## Tombstone semantics (AC #4)

Refractor's adapters NEVER physically delete:

- Postgres: `UPDATE ... SET is_deleted=true, deleted_at=NOW()`
- NATS-KV: PUT a tombstone document `{"isDeleted": true}` (rather than KVDelete)

Soft-delete preserves lineage for crypto-shred forensics and Capability
audit trails.
