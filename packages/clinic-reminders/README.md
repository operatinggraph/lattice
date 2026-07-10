# clinic-reminders

The clinic vertical's **first orchestration** — a Capability Package that attaches `@at`-driven
forcing functions to `clinic-domain`'s appointments/patients. Three convergences: the **appointment
reminder** ("remind ~24h before"), the **follow-up reminder** ("a documented visit's requested follow-up
is due"), and the **recurring visit series** ("a patient on a standing cadence has a visit coming due") —
each a marker/rolling aspect + op + convergence lens + §10.8 playbook.

It is the convergence **sibling** of the projection-only `clinic-domain`: clinic-domain owns the
`appointment` vertex + its `.schedule`/`.status`/`.encounter` aspects (precomputing
`remindAt = startsAt − 24h`, and normalizing a documented visit's `followUpDate` to a full RFC3339
instant); clinic-reminders *attaches the reminder machinery* onto that vertex (the `loftspace-domain`
idiom of one package adding an aspect onto another package's vertex type — the step-6 write gate keys on
the **aspect** class, not the host vertex's owner). The follow-up half lives in `followups.go`. The
recurring visit series is its own self-contained vertex type (`visitseries.go`, the clinic-domain
patient/provider idiom) rather than an attached aspect, since a series outlives any single appointment.

Install: `lattice-pkg install packages/clinic-reminders` (after `clinic-domain` + `orchestration-base`;
or `make install-clinic` onto a running stack).
Design: [`_bmad-output/implementation-artifacts/clinic-reminders-design.md`](../../_bmad-output/implementation-artifacts/clinic-reminders-design.md)
(reminders) and [`clinic-recurring-visit-series-design.md`](../../_bmad-output/implementation-artifacts/clinic-recurring-visit-series-design.md)
(visit series).

## Inventory

| Kind | Canonical names |
|---|---|
| **DDLs** (8) | `appointmentReminderOp` · `appointmentReminder` (the `.reminder` write gate) · `followUpReminderOp` · `followUpReminder` (the `.followUpReminder` write gate) · `visitseries` (vertex type, owns all four visit-series ops) · `visitSeriesDefinition` / `visitSeriesProgress` / `visitSeriesPaused` (the `.series`/`.progress`/`.paused` write gates) |
| **Operations** (6) | `RecordAppointmentReminder` · `RecordFollowUpReminder` · `StartVisitSeries` · `PauseVisitSeries` · `ResumeVisitSeries` · `AdvanceVisitSeries` |
| **Convergence lenses** (3) | `appointmentReminders` · `followUpReminders` · `visitSeriesDue` → `weaver-targets` (`nats-kv`, `full` engine) |
| **Read lens** (1) | `visitSeriesRead` → Postgres (`protected`, D1.5 RLS — the patient's own series + the clinic-wide staff worklist) |
| **Weaver targets** (3) | `appointmentReminders` — `missing_reminder` → `directOp(RecordAppointmentReminder)` · `followUpReminders` — `missing_followup_reminder` → `directOp(RecordFollowUpReminder)` · `visitSeriesDue` — `missing_series_advance` → `directOp(AdvanceVisitSeries)` |

`Depends`: `clinic-domain` (the appointment + `.schedule.remindAt` / `.encounter.followUpDate`; the
patient/provider vertex types the visit series links to) + `orchestration-base` (`MarkExpired` / the
`freshnessExpiry` marker the `@at` firing writes). All ops are granted to the `operator` role at
`scope: any` (`permissions.go`) — no new capability surface; Weaver's service actor dispatches the
`directOp` under the standing operator grant (the `objects-base` GC `TombstoneObject` idiom).

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
narrow window between the gap opening and this op committing still gets a `.reminder` marker (and a
notification send). That is a rare-window best-effort gap, not a hard guarantee.

The script also fires `external.notification` off its own transactional outbox (no Loom pattern — the
bridge's dispatch path, `internal/bridge/dispatch.go`, is fully generic and needs neither a claim
vertex nor a Loom-parked token), keyed on `appointmentKey:remindedFor` so a redelivery of the *same*
due reminder dedups at the adapter while a **reschedule** (a new `remindedFor`) mints a fresh key and
sends again. The bridge's `"notification"` adapter (`FakeNotification`, mirroring `FakeStripe` — the
platform's established simulated-adapter convention) Executes and posts
`RecordAppointmentReminderNotification` / `RecordFollowUpReminderNotification`
(`notifications.go`) back, which record the outcome as an **audit-only** `.reminderNotification` /
`.followUpReminderNotification` aspect — neither gates the convergence lenses above (still keyed on
`.reminder`/`.followUpReminder`, unchanged). See
`_bmad-output/implementation-artifacts/clinic-reminders-notification-adapter-design.md`.

## Recurring visit series (`visitseries.go`)

A **rolling** generalization of the one-shot reminders above: a patient on a standing cadence
(chronic-care monthly check-ins, weekly PT) gets a self-rearming "next visit due" gap instead of a
per-entity `@every` schedule. Same convergence machinery (aspect + op + `freshUntil`-armed `@at` lens +
`directOp` playbook), except each convergence re-arms its own next deadline instead of firing once.

`StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}` validates both
endpoints are alive, mints its own `vtx.visitseries.<id>` vertex (not an aspect on the appointment —
a series outlives any single appointment) with `.series` (write-once cadence) + `.progress` (rolling
state, seeded `nextDueAt = startAt`) + `forPatient`/`withProvider` links. `PauseVisitSeries` /
`ResumeVisitSeries` toggle `.paused` (absent = not paused); a paused series projects no due gap and no
armed timer, and resuming picks up exactly where `.progress.nextDueAt` left off (no missed-occurrence
catch-up burst). `AdvanceVisitSeries` is the `directOp` the `visitSeriesDue` playbook dispatches when
`missing_series_advance` opens: it rolls `.progress` forward from `dueFor` (the deadline just serviced,
**not** `$now` — keeps the cadence on a fixed grid immune to dispatch latency, the `followUpReminders`
idiom), `nextDueAt = dueFor + intervalDays`, `occurrenceCount + 1`.

Unlike the one-shot reminders, this convergence never permanently closes: each `AdvanceVisitSeries`
rewrites `nextDueAt` to a new future deadline, so the row re-projects pending (re-armed) rather than
done — the series keeps re-arming its own next wake-up until paused or past `activeUntil`. `active =
NOT paused AND (no activeUntil OR nextDueAt <= activeUntil)`; `freshUntil` arms only while `active` and
`nextDueAt` is future.

The D1.5-protected `visitSeriesRead` Postgres lens (patient-anchored, `REQUIRED` `forPatient` walk —
fail-closed) backs both the patient's own "my series" view and the clinic-wide staff worklist (staff
reads the same table under the reserved `WildcardAnchor` grant, the `clinicAppointmentsRead` /
`handleStaffAppointments` precedent) — it carries only the display columns (`active`, `next_due_at`,
`interval_days`, `occurrence_count`), never the Weaver-dispatch columns (`freshUntil`,
`missing_series_advance`, `violating`).

Convergence pinned by `visitseries_cypher_test.go`; the write path by
`TestStartVisitSeries_*`/`TestAdvanceVisitSeries_*` (`integration_test.go`); the read model by
`visitseries_read_test.go`.

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

- **Real vendor integration + real recipient targeting** — the `"notification"` bridge adapter
  (`FakeNotification`) is simulated, same convention as every other adapter this platform ships
  (`FakeStripe`/`FakeBackgroundCheck`/`FakeDocGen`). A real recipient send additionally needs the
  Vault-decrypt-at-send question resolved (patient contact info is Vault-encrypted on `vtx.identity`,
  decryptable only via the Secure-Lens read path — an architectural fork, not implementation work).
  The series occurrence path (`visitSeriesDue`) has no notification wired at all yet.
- **Recurring `@every` schedules** — the visit series above meets clinic's recurring-cadence need with a
  **rolling `@at`** (state lives in the read model; each convergence re-arms the next deadline itself),
  so no engine-level `@every` primitive was needed (see the visit-series design doc §3 for why). A
  genuine per-entity `@every` schedule remains a deferred platform item with no consumer today.

