# Clinic reminders — wire a real notification adapter

**Status:** ✅ Winston-ratified — build-ready (no frozen-contract change; no architectural fork).
**Board row:** [verticals.md → "Reminders never actually reach a patient"](../planning-artifacts/backlog/verticals.md)

## Problem

`RecordAppointmentReminder` / `RecordFollowUpReminder` (`packages/clinic-reminders`) only stamp an
internal `.reminder` / `.followUpReminder` marker aspect — no email/SMS ever actually sends. The
in-code comments have said "the actual notification channel is the deferred bridge-adapter work"
since the package shipped.

## Grounding

- `internal/bridge` already has a proven, generic `Adapter` SPI (`internal/bridge/adapter.go`):
  `Execute(ctx, Request) (Dispatch, error)` + `Poll`. The bridge's durable consumer on
  `events.external.>` (`internal/bridge/dispatch.go`) is **fully generic** — it parses the event
  body, looks up the named adapter, calls `Execute`, and posts the event's `replyOp` back to
  `core-operations`. It has **zero dependency on Loom** (`boundary_test.go` CI-gates that
  `internal/bridge` never imports `internal/loom`).
- Every registered adapter today (`FakeStripe`, `FakeBackgroundCheck`, `FakeDocGen`,
  `FakeAsyncCheck`) is a **simulated reference adapter** — none makes a real vendor call. That is
  this reference platform's established convention: the *orchestration mechanism* (registry,
  dedup, replyOp closing the loop) is real and fully wired; the *vendor* is simulated. A
  "notification adapter" should follow the identical convention, not be held to a different bar.
- `docs/contracts/10-orchestration-weaver.md` §10.8's `nudge`-retirement amendment restricts which
  **action keyword a Weaver playbook** may use for a gap needing external I/O (`triggerLoom`, not a
  bespoke `nudge`). It does **not** restrict what an unrelated op's own DDL script may emit from its
  own transactional outbox. Confirmed generic + inert-if-unmatched: Loom's completion listener
  (`internal/loom/engine.go: handleCompletion`) silently drops any `orchestration.externalTaskCompleted`
  whose `externalRef` doesn't resolve to a live `token.<key>` — no error, no cross-talk — so emitting
  `external.<adapter>` from a plain `directOp` that was never part of a `triggerLoom` pattern is
  mechanically safe.
- Patient contact info (email/phone) lives only on `vtx.identity` (`packages/identity-domain`), is
  declared `sensitive:true`, and is Vault-encrypted at commit (step 6.5) — a Processor Starlark op
  reading `state[...]` only ever sees the ciphertext envelope `{ct, nonce, keyId}`. Plaintext is
  decryptable **only** via the identity-anchored Secure-Lens read path (Refractor's
  `SecureDecryptor`), never inside an op script or an arbitrary Go service — extending that was
  already explicitly **REJECTED** (`vault-crypto-shredding-design.md` ratification decision #2,
  also the reason the "Clinical notes are write-only" board row is `🚧 blocked-on: Vault`). So a
  *real* recipient-targeted send is out of scope for this increment — exactly as it's out of scope
  for `FakeStripe` (no real card number) and `FakeDocGen` (no real signature image); the adapter
  operates on the appointment/reminder identifiers only, never patient PII.

## Design

**No new Loom pattern, no new claim vertex.** The existing `directOp(RecordAppointmentReminder)` /
`directOp(RecordFollowUpReminder)` gap remediation is unchanged — Weaver still dispatches the same
op for the same gap. Each op's own Starlark script (already committing the `.reminder` /
`.followUpReminder` marker) additionally emits an `external.notification` event off its own
transactional outbox, exactly as any DDL is free to do:

```
event_data = {
    "instanceKey": appt_key + ":" + reminded_for,   # deterministic idempotency key
    "adapter": "notification",
    "replyOp": "RecordAppointmentReminderNotification",  # or ...FollowUpReminderNotification
    "externalRef": appt_key + ":" + reminded_for,
    "idempotencyKey": appt_key + ":" + reminded_for,
    "params": {"appointmentKey": appt_key, "reminderType": "appointment", "remindedFor": reminded_for},
}
```

Keying the idempotency/external ref on `(appointmentKey, remindedFor)` — not just `appointmentKey`
— means a redelivery of the *same* due reminder dedups at the adapter (no double-send), while a
**reschedule** (which changes `remindedFor`) naturally mints a fresh key and sends again — the same
re-arm semantics the marker aspect already has.

- **`internal/bridge/fake_notification.go`** — new `FakeNotification` adapter, same shape as
  `FakeStripe`: in-memory idempotency map, `Execute` "sends" (logs + deterministic simulated
  success), `Poll` unreachable (sync-only). Registered in `cmd/bridge/main.go`'s adapter map as
  `"notification"`.
- **`packages/clinic-reminders/notifications.go`** (new file) — two new ops/DDLs, the replyOps the
  bridge posts back: `RecordAppointmentReminderNotification` (writes `.reminderNotification` on the
  appointment) and `RecordFollowUpReminderNotification` (writes `.followUpReminderNotification`).
  Both are audit/observability markers only — they do **not** gate the existing
  `appointmentReminders`/`followUpReminders` convergence lenses (unchanged, zero regression risk to
  the shipped reminder mechanism); FE / Loupe can surface delivery status from them later.
- `ddls.go` / `followups.go` — `recordReminderScript` / `recordFollowUpReminderScript` gain the
  `external.notification` event emission alongside their existing marker mutation + provenance
  event. Stale "deferred bridge-adapter work" doc comments updated to describe the now-wired path.
- `manifest.yaml` / `permissions.go` / `package.go` updated to match (2 new DDLs, 2 new permission
  grants to `operator`, 2 new op-metas).

## Follow-on (not this increment)

Real vendor integration (SendGrid/Twilio) and real recipient targeting both need the Vault-decrypt-
at-send architectural question resolved first (who is trusted to decrypt identity PII outside the
Secure-Lens path) — that's an Andrew-level fork, not implementation work, and is the same fork the
"Clinical notes are write-only" row is already blocked on. Flagging, not blocking this increment:
the gap this board row actually names — "no I/O ever happens" — is fully closed by this change.
