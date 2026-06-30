# clinic-reminders

The clinic vertical's **first orchestration** — a Capability Package that attaches one-shot `@at`
reminders to `clinic-domain`'s appointments. Two convergences: the **appointment reminder** ("remind
~24h before") and the **follow-up reminder** ("a documented visit's requested follow-up is due"), each a
marker aspect + op + convergence lens + §10.8 playbook — the build-ready slices of the clinic's
scheduling story (recurring availability genuinely needs `@every` and stays a separate,
§10.4-amendment-gated item).

It is the convergence **sibling** of the projection-only `clinic-domain`: clinic-domain owns the
`appointment` vertex + its `.schedule`/`.status`/`.encounter` aspects (precomputing
`remindAt = startsAt − 24h`, and normalizing a documented visit's `followUpDate` to a full RFC3339
instant); clinic-reminders *attaches the reminder machinery* onto that vertex (the `loftspace-domain`
idiom of one package adding an aspect onto another package's vertex type — the step-6 write gate keys on
the **aspect** class, not the host vertex's owner). The follow-up half lives in `followups.go`.

Install: `lattice-pkg install packages/clinic-reminders` (after `clinic-domain` + `orchestration-base`;
or `make install-clinic` onto a running stack).
Design: [`_bmad-output/implementation-artifacts/clinic-reminders-design.md`](../../_bmad-output/implementation-artifacts/clinic-reminders-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **DDLs** (4) | `appointmentReminderOp` · `appointmentReminder` (the `.reminder` write gate) · `followUpReminderOp` · `followUpReminder` (the `.followUpReminder` write gate) |
| **Operations** (2) | `RecordAppointmentReminder` · `RecordFollowUpReminder` |
| **Convergence lenses** (2) | `appointmentReminders` · `followUpReminders` → `weaver-targets` (`nats-kv`, `full` engine) |
| **Weaver targets** (2) | `appointmentReminders` — `missing_reminder` → `directOp(RecordAppointmentReminder)` · `followUpReminders` — `missing_followup_reminder` → `directOp(RecordFollowUpReminder)` |

`Depends`: `clinic-domain` (the appointment + `.schedule.remindAt` / `.encounter.followUpDate`) +
`orchestration-base` (`MarkExpired` / the `freshnessExpiry` marker the `@at` firing writes). Both ops are
granted to the `operator` role at `scope: any` (`permissions.go`) — no new capability surface; Weaver's
service actor dispatches the `directOp` under the standing operator grant (the `objects-base` GC
`TombstoneObject` idiom).

## Follow-up reminders (`followups.go`)

The **same mechanism** as the appointment reminder, keyed on the documented visit's
`.encounter.followUpDate` instead of `.schedule.remindAt`, and firing **at** that date (no lead offset —
the visit is already past and `followUpDate` is the provider's soft target). When `RecordEncounter`
captures `followUpRequested` + a `followUpDate`, clinic-domain normalizes the date-only FE value to a
full RFC3339 instant (`09:00:00Z` "the morning of") so Weaver's `@at` temporal lane can arm a timer at
it. The four-term gate (`remindedFor <> followUpDate` AND `followUpRequested = true` AND
`followUpDate <= $now` AND `status <> 'cancelled'`) opens once the date passes; the `directOp` stamps
`.followUpReminder = {sentAt, remindedFor = followUpDate}` → converged. A re-documented visit that moves
the `followUpDate` re-opens the gate and re-arms the reminder for the new date (the appointment
reminder's reschedule re-arm). Surfaced via the `clinicAppointments` lens's null-safe
`followUpReminderSentAt` soft read (the `reminderSentAt` precedent). Convergence pinned by
`followups_cypher_test.go`; the op write-path by `TestRecordFollowUpReminder_*`.

## Key shapes (Contract #1)

```
vtx.appointment.<id>.reminder = {sentAt, remindedFor}   class=appointmentReminder   (this package; on clinic-domain's appointment)
op RecordAppointmentReminder{appointmentKey, remindedFor?}    writes .reminder on a LIVE appointment
```

The op mints **no vertex of its own type** — it writes the `.reminder` aspect onto an existing
`vtx.appointment` (the `freshnessMarker` idiom). `sentAt` = the op's `submittedAt` normalized to
canonical whole-second UTC; `remindedFor` = the appointment `startsAt` this reminder was for (stored
verbatim so the lens's `remindedFor <> startsAt` compare is byte-exact).

## The mechanism — inverting lease-signing's freshness re-open

lease-signing projects `freshUntil` to **re-open** a converged gap at a deadline. This package inverts
it: it projects `freshUntil = remindAt` so Weaver's `@at` temporal lane fires *at* the deadline, and
the reminder gap **opens** (rather than re-opens) when that deadline passes. The lifecycle for one
appointment:

1. **At create** — `clinic-domain` stamps `.schedule.remindAt = startsAt − 24h`. While `remindAt` is in
   the future, the lens projects `freshUntil = remindAt` → Weaver arms an `@at` timer at `remindAt`.
   `missing_reminder` is false (nothing to do yet).
2. **At `remindAt`** — the `@at` fires → `handleFiredTimer` submits `MarkExpired`, whose `freshnessExpiry`
   marker write on this appointment re-projects the row with a fresh `$now` → `remindAt <= $now` holds →
   `missing_reminder` flips true and `freshUntil` goes null (the one-shot wake-up is not re-armed).
3. **Dispatch** — Weaver's gap-dispatch (the *violating* row, **not** a timer) dispatches
   `directOp(RecordAppointmentReminder)` with `Params{appointmentKey: row.entityKey, remindedFor:
   row.startsAt}` → the op stamps `.reminder = {sentAt, remindedFor = startsAt}` → re-projection →
   `remindedFor = startsAt` → `missing_reminder` false. **Converged.**
4. **On reschedule** — `clinic-domain`'s `RescheduleAppointment` rewrites `.schedule` with a new
   `startsAt` + a re-derived `remindAt`, so the recorded `remindedFor` (the *old* `startsAt`) now differs
   from the new `startsAt` → the gate re-opens. If the new `remindAt` is still future, `freshUntil`
   re-arms a fresh `@at`; if it is already past (a `<24h` move), `missing_reminder` is true at once. The
   new dispatch stamps `remindedFor = the new startsAt` → converged again.

`directOp` (not a Loom pattern) because a reminder is a single op — no multi-step `externalTask` flow —
exactly the `objectLiveness → TombstoneObject` GC precedent.

### The four-term gate

`missing_reminder` (== `violating`) is true when **all four** hold:

| Term | Meaning |
|---|---|
| `remindedFor <> startsAt` | Not yet reminded for the *current* scheduled time. Subsumes **never-reminded** (no `.reminder` → `remindedFor` resolves null → `null <> startsAt` is true in the full engine) **and** **reminded-for-a-stale-time** (a reschedule moved `startsAt`). A reminder for the current `startsAt` reads `remindedFor = startsAt` → false → converged. |
| `remindAt <= $now` | The reminder deadline has passed (lexical RFC3339 compare = chronological on canonical UTC — the `validUntil > $now` idiom). |
| `startsAt > $now` | The appointment is still in the future (never remind for a past appointment). |
| `status <> 'cancelled'` | A cancelled appointment is never reminded. |

`freshUntil` projects `remindAt` only while `remindAt > $now` **and** the gate is otherwise open (a
future wake-up); once the deadline passes the dispatch path owns it, so `freshUntil` is null. Net:
exactly **one `@at` fire per `startsAt`**, and a `<24h` booking (`remindAt` already past) arms no timer
at all — it is violating on the creation CDC and dispatched at once.

## Idempotency, liveness, and the notification boundary

`RecordAppointmentReminder` is **read-guarded** (`ContextHint.Reads = [appointmentKey]`): it reads the
appointment root and rejects (`UnknownAppointment`) if absent or tombstoned, so it never marks a
reminder on a dead appointment. The marker write is an **unconditioned update** (create-if-absent /
overwrite-if-present, no `expectedRevision`) → idempotent in effect, so an at-least-once redelivery or a
sweep reclaim re-stamps it harmlessly (the `MarkExpired` idiom).

It guards **liveness** (`isDeleted`) but deliberately **not status** — an appointment cancelled in the
narrow window between the gap opening and this op committing still gets a `.reminder` marker. That is
harmless while the marker is inert: this op records that a reminder became **due**, not that a
notification was sent. The real notification channel (email / SMS) is the deferred bridge-adapter work,
and the authoritative "do not actually notify a cancelled/changed appointment" check belongs at that
delivery point (which must read live state at send time anyway), not here.

## Where the reminder is surfaced

`clinic-reminders` writes `.reminder.sentAt`; `clinic-domain`'s `clinicAppointments` lens projects it as
a **null-safe soft read** (`reminderSentAt`) — null until a reminder fires and null whenever
clinic-reminders is not installed. So the clinic FE shows "reminder sent" without a build dependency on
this package; this package never reaches into clinic-domain's lenses.

## §10.2 ↔ §10.8 column seam

The playbook routes `Params{appointmentKey: row.entityKey, remindedFor: row.startsAt}` and
`Reads[row.entityKey]`; both `entityKey` and `startsAt` are `appointmentReminders` `BodyColumns`. This
lens-column ↔ playbook-param binding is cross-checked by `TestClinicReminders_PlaybookColumnsMatchLens`.
The convergence behavior is pinned by the `lens_cypher_test.go` cases — `TestReminders_{Pending, Due,
Sent, RescheduledAfterSent, RescheduledIntoWindow, Cancelled, PastAppointment, LastMinuteBooking}` —
and the op by `TestRecordAppointmentReminder_{WritesMarker, RejectsTombstonedAppointment}`.

## Out of scope

- **The notification channel** (email / SMS delivery) — deferred real-adapter work. This package records
  that a reminder became due; it does not send anything.
- **Recurring `@every` schedules** — recurring provider availability / follow-ups genuinely need
  `@every`, which has no consumer today (§10.4 ships `@at` one-shot). That remains a deferred platform
  item the clinic vertical forces, not one this package implements.
</content>
</invoke>
