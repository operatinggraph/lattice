# Privacy-worker

**Component reference** | Audience: implementers + architects | Contract authority: `docs/contracts/03-mutation-batch-event-list.md` §3.10/§3.11 (crypto-shredding); design: `vault-crypto-shredding-design.md` §2.4 (Fire 3/4b)

---

## Overview

Privacy-worker is the **asynchronous half of crypto-shredding**: a durable NATS consumer that turns a
recorded shred *intent* into the actual irreversible key destruction. The `shredIdentityKey` DDL
(`packages/privacy-base`) only marks `piiKey.shredded=true` and emits `events.privacy.keyShredded` on
its synchronous commit path — it never calls the Vault itself, so a KMS round-trip can never block or
fail an operation commit. Privacy-worker is the consumer that closes that loop: on the event, it calls
`Vault.ShredKey(identityKey)` against the **same** `*vault.LocalBackend` instance
[Vault](./vault.md)'s commit-path hooks use, then durably records the destruction so an operator can
see the shred reach completion.

Like Vault, privacy-worker is **not a separate binary** — it is a library
(`internal/privacyworker`) co-located in the **same process as the Processor**
(`cmd/processor`, design §3's "fewer moving parts"). This placement is load-bearing, not just
convenient: `LocalBackend`'s shredded-set and DEK cache are per-instance in-memory state. A
separately-constructed Vault instance built from the same master KEK would **not** observe a shred
recorded by this listener, since decrypt-on-read (commit-path step 4) and encrypt-on-write (step 6.5)
both run against the Processor's own instance. Refractor is deliberately kept Vault-blind for general
projection (it projects ciphertext as-is), so hosting this listener there would mean wiring
master-KEK access into a component the design keeps out of the loop; the Processor already holds the
Vault, so this is the minimal-surface-area placement.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/privacyworker/manager.go` | `Manager` — the durable `events.privacy.keyShredded` consumer, `ShredKey` invocation, and the `RecordShredFinalization` submit |

`internal/refractor/keyshredded` is a **separate, sibling** consumer of the same event (Refractor-side
plain-lens row nullification) — see [Vault's "Adjacent consumers"](./vault.md#adjacent-consumers) for
how the two divide the work. Neither imports the other; neither's failure blocks the other.

---

## What it does, in order

1. **Consume** `events.privacy.keyShredded` on the durable `privacy-worker` consumer
   (`core-events`, redelivery on Nak with a 5s floor).
2. **Shred.** Call `Vault.ShredKey(identityKey)` against the Processor's authoritative instance.
   `ShredKey` is documented idempotent, so a redelivery of the same event safely re-runs in full. A
   failed call is `NakWithDelay` — a shred must never be silently dropped; JetStream's at-least-once
   redelivery is the crash-survival backstop, this Nak loop is the in-process one.
3. **Record (publish-then-ack).** If a privacy service actor is configured (`ActorKey`), submit one
   `RecordShredFinalization{step: "vaultKeyDestroyed"}` op **before** acking the event — a crash
   between the shred and the submit redelivers the whole event; `ShredKey` re-runs idempotently and
   the record's deterministic `requestId` (derived from the identity key + the triggering event's
   stream sequence) collapses a duplicate on the Contract #4 tracker. This finalization record is what
   the `shredStatus` lens projects, so an operator can see an in-flight vs. completed shred rather than
   only "requested."
4. **Ack** the event only after both steps succeed.

An empty `ActorKey` (a pre-Fire-4b kernel with no `identity.system.privacy` service actor) logs a
startup warning and disables the finalization record — the shred itself still runs; `shredStatus` rows
just stay perpetually in-flight.

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| In | `events.privacy.keyShredded` durable (`privacy-worker`, `core-events`) | one message = one identity's shred to finalize; body read as `payload.identityKey` (read-from-body discipline, mirroring `internal/objectmanager`'s tombstoned-event handling) |
| Out | `Vault.ShredKey` call | against the Processor's own `*vault.LocalBackend` instance (shared in-process, not a network call) |
| Out | `RecordShredFinalization` op, `ops.<lane>` (default `system`) | submitted under the `identity.system.privacy` service actor, `ContextHint.Reads` declaring the `piiKey` aspect so the record is hydrated + OCC-conditioned against the sibling `projectionsNullified` record racing it on the same lane, **and the service actor's own vertex**, which the script reads to refuse an attestation written by any other actor (an undeclared actor fails the op closed) |

---

## Key invariants

- **One authoritative Vault instance.** This worker must be constructed with the exact same `Vault`
  instance the Processor's commit path encrypts/decrypts through — the package doc calls this out
  explicitly because it is easy to accidentally wire a second, differently-constructed instance from
  the same KEK and silently break shred-observability.
- **Publish-then-ack ordering.** The finalization op is submitted *before* the event is acked (the
  objectmanager-cascade idiom) — a crash between the two redelivers the event rather than losing the
  finalization record.
- **Idempotent end-to-end.** `ShredKey` idempotency + the deterministic `requestId`'s Contract #4
  tracker collapse mean a redelivered event is always safe to fully re-run, never a double-effect.
- **A shred is never blocked on finalization.** Steps 2 and 3 are sequenced, but ShredKey's own
  success is never conditioned on the record op succeeding first — a stuck finalization retries the
  whole event (including a now-idempotent re-shred), it never masks or reverts the shred.

---

## Failure modes

| Mode | Behavior |
|------|----------|
| `Vault.ShredKey` fails (backend error) | `NakWithDelay` — redeliver and retry; the shred is never silently dropped |
| `RecordShredFinalization` submit fails | `NakWithDelay` on the **whole event** — a redelivery re-runs `ShredKey` (idempotent, no-op) and retries the record |
| Malformed / missing `identityKey` in the event body | `Term` — a malformed event can never be shredded correctly; logged and dropped rather than retried forever |
| No `identity.system.privacy` service actor configured (pre-Fire-4b kernel) | shred still runs; the finalization record is skipped with a startup warning — `shredStatus` rows stay in-flight |

---

## Principles that apply

- **P2.** The worker mutates Core KV only via the ordinary `RecordShredFinalization` op submission —
  it never writes Core KV directly. The actual key destruction (`ShredKey`) is off-graph (a Vault
  backend operation, not a KV write).
- **Minimal-surface-area placement.** Co-located in the Processor process specifically because that is
  the one process already holding both the Vault instance and Core-KV write access — no new binary,
  no new cross-process Vault RPC for this internal consumer.

---

## Implementation status

**Built.** The `events.privacy.keyShredded` consumer, the `ShredKey` call, and the
`RecordShredFinalization` publish-then-ack sequencing are implemented and covered by
`internal/privacyworker`'s own test suite; the end-to-end chain (shred intent → key destruction →
finalization record → `shredStatus` projection) is proven by the Vault Fire 5b crypto-shred e2e test
(`fb66e7c`).

**No independent heartbeat.** Privacy-worker's activity is folded into the `health.vault` heartbeat's
`keyshredded_handled_total` counter (Contract #5 §5.4) rather than a distinct `health.privacy-worker.*`
group — it has no standalone Contract #5 baseline of its own.

---

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **A spine asserted only by its declared shape is not asserted at all** — every core package carried a
  `verify-package-*` gate except the erasure plane, because exercising step 1 destroys a key and so needs
  a subject minted to be destroyed; the cost of not doing it was that no one knew whether the four steps
  ran. Minted: privacy-base verification tooling. Check: `verify-erasure-ceremony` drives the spine on a
  disposable subject, and `verify-package-privacy-base` pins the Loom pattern's step ORDER, not just its
  presence.
- **A destructive verb's grant is revocable but not restorable** — uninstalling the consent package
  tombstones the permission vertex and its `grantedBy` link, and no reinstall path revives either while
  every install still reports success. Minted: the erasure ceremony's live run (the board row's fix).
  Check: none yet — assert the projected `cap.roles.*` entry, never the installer's exit code.
- **A verifying environment's own provisioning gap reads as a product defect** — `provision-readpath`
  shells through `docker compose`, so a natively-built stack lacks `actor_read_grants` and every shred
  logs `grant-table revoke failed (privacy-critical, no retry)`. Minted: the same run. Check: establish
  which target creates a missing relation before filing the error that names it.
