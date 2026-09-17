# Clinic — "An appointment's status has no time" + "A visit the desk cancels or moves tells the patient nothing" (2026-09-17)

**Filed (PO, 2026-09-17), two rows built as one fire.** (1) `.status` is `{value, note?, fee?}`; no transition
stamps when, so the desk cannot see how long a checked-in patient has waited and a cancel has no recorded moment.
(2) Only the 24 h reminder, the follow-up and the arrears reminder ever go out; a staff
`SetAppointmentStatus(cancelled)` and a `RescheduleAppointment` write `.status`/`.schedule` in silence. Live: 23
cancelled, none noticed. Winston-adjudicated (implementation-level; no contract surface, no fork). The two rows
share one write site and one precedent (wellness `f139d128`), and the notice is keyed on the stamp — one fire.

## Grounding

- **Five writers of `.status`, none stamps a time** ([ddls.go](../../packages/clinic-domain/ddls.go)): `CreateAppointment`
  `{value: scheduled}` (:3682); `RescheduleAppointment` re-stamps a live non-terminal status, `scheduled` over
  confirmed/checkedIn (:3908); `SetAppointmentStatus` builds `status_data = {value}` + note / fee / lateCancel, and
  a same-value cancelled re-set carries the fee forward (:4043–4104), its self-confirm leg writes exactly
  `{value: confirmed}` (:4033); `CorrectAppointmentStatus` (:4171); `MarkPastDueNoShow` (:4275). Every write is a
  bare `make_aspect_upsert`. The staff / patient legs are distinguished in the script by `op.authContextTarget`
  (`""` on the operator grant; the identity on the consumer self grant) — on `SetAppointmentStatus` (:4003),
  `RescheduleAppointment` (:3849) and `CreateAppointment` (:3606). Nothing records *who* wrote a status.
- **A move records nothing about itself.** `RescheduleAppointment` rewrites `.schedule = {startsAt, endsAt,
  remindAt}` whole (:3874) — no `movedAt`, no prior time. The reminder lens re-arms on `remindedFor <> startsAt`
  ([clinic-reminders lenses.go:200](../../packages/clinic-reminders/lenses.go)), so a move inside 24 h is
  re-reminded at once — that is the only word a moved patient gets, and a move further out is silent until the new
  remindAt. A cancel (`nonTerminalAppointment` false) closes the reminder gate and says nothing.
- **The notification plumbing is in `clinic-reminders`**: `RecordAppointmentReminder` (Weaver-actor guarded,
  [ddls.go:221](../../packages/clinic-reminders/ddls.go)) emits `external.notification` off its own outbox with
  `instanceKey = idempotencyKey = externalRef = <apptKey>:<remindedFor>`, `adapter: notification`, `replyOp:
  RecordAppointmentReminderNotification` (:268–275); the replyOp ([notifications.go:50](../../packages/clinic-reminders/notifications.go))
  reads nothing and writes the audit marker. The target declares `Reads: [row.entityKey, row.entityKey.schedule]`
  ([targets.go:28–40](../../packages/clinic-reminders/targets.go)).
- **The wellness mirror** ([design](wellness-booking-change-notices-2026-09-16.md), `f139d128`): a level-triggered
  weaver-target lens with one gap per change kind, keyed on a recorded change value (`promotedAt` / the current
  `startsAt` vs the marker's `movedFor`); one Weaver-actor op `RecordBookingChangeNotice{kind, changeRef}` that
  re-checks the change against the live aspect (`StaleChange`), writes `.changeNotice = {<kind>For…, sentAt}` as a
  create-or-bare-update carrying the other kind's field, and emits `external.notification` keyed
  `<key>:<kind>:<changeRef>`; a replyOp that upserts `.changeNotification` (latest outcome wins, audit only). Two
  build-time findings there bind here: the gap carries the OPTIONAL hop's `<> null` conjunct, and the marker write
  is bare (the OCC lint never bound).
- **Read path**: `.status.value/.note` are projected by `clinicAppointments` (KV, [lenses.go:675](../../packages/clinic-domain/lenses.go)),
  `clinicAppointmentsRead` (:1020) and `providerAppointmentsRead` (:1061) — the last two are Protected Postgres
  lenses; a new column is `Columns` + RETURN alias + `refresh-clinic` (the `ADD COLUMN IF NOT EXISTS` path, verified
  2026-09-13), read by `selectMyAppointmentsSQL` / `selectMyProviderScheduleSQL` into `protectedAppointmentRow`
  ([appointments.go:142](../../cmd/clinic-app/appointments.go)); precedent for the whole column path is `amendedAt`
  (`08888209`). The card is `renderApptCard` ([app.js:5251](../../cmd/clinic-app/web/app.js)): status badge
  (:5365), the reminder-sent line (:5290), `STATUS_PAST` (:5558); goja pins extract top-level declarations
  ([visit_record_ui_test.go:20](../../cmd/clinic-app/visit_record_ui_test.go)).
- **Engine**: the null test is `<> null` (two-valued); `=` on a null operand is false (nil-false), so a conjunct
  `by = 'staff'` is false for every `.status` written before this build — the 23 live cancelled visits are not
  told on install. `coalesce` is supported.
- **Census (Core KV via the PO, 2026-09-17):** 119 live appointments — 20 scheduled, 1 confirmed, 67 noShow,
  23 cancelled, 8 completed; 0 on time-off; `clinic-domain` 0.39.0, `clinic-reminders` 0.12.2.

## Verdict — *every status carries its moment and its author; a change the desk makes is told once, keyed on that moment*

1. **`.status` gains `at` and `by`, recorded at the transition.** Every writer stamps `at =
   time.rfc3339_utc(op.submittedAt)` and `by ∈ {staff, patient, sweep}` (`patient` when `op.authContextTarget
   != ""`, `sweep` for `MarkPastDueNoShow`, `staff` otherwise) **when the value changes**; a same-value re-write
   **carries `at` and `by` from the current aspect** — a note added to a cancelled visit, a scheduled re-stamp on a
   reschedule, and a same-value re-set are not transitions, so "checked in 12 min ago" does not reset and a
   cancel notice is not re-sent. The self-confirm leg writes `{value: confirmed, at, by: patient}` (its
   "exactly `{value}`" comment is rewritten); the same-value re-confirm stays the empty batch.
   `CreateAppointment` stamps `scheduled` at creation. Legacy `.status` without `at` is left as is (no backfill —
   the moment was never recorded, and a lens `coalesce` to a proxy would be a clock in disguise).
2. **`.schedule` gains `movedAt` + `movedBy` on every reschedule.** `RescheduleAppointment` writes them into the
   whole-aspect `sched` (`movedBy` by the same rule); `CreateAppointment` writes neither. A move is a recorded fact
   on the entity, never inferred from the reminder's `remindedFor`.
3. **One level-triggered lens in `clinic-reminders`: `appointmentChangeNotices`** (anchor appointment,
   `actorAggregate`, `weaver-targets`, no `freshUntil`):
   - `missing_cancel_notice = status = 'cancelled' AND by = 'staff' AND at <> null AND changeNotice.cancelledFor <> at
     AND NOT (freshnessExpiry.byTarget.pastDueAppointments >= endsAt)` — a visit cancelled by the desk before it
     ended; a patient's own cancel, a legacy cancel, and a correction after the visit are not told.
   - `missing_move_notice = movedAt <> null AND movedBy = 'staff' AND changeNotice.movedFor <> movedAt AND
     nonTerminalAppointment AND NOT (…pastDueAppointments >= endsAt)` — told once per desk move; a second move
     reopens; a patient's own move is not told; a moved-then-cancelled visit gets the cancel notice only.
   Columns: `entityKey, patientKey, status, statusAt, statusBy, startsAt, endsAt, movedAt, movedBy, cancelledFor,
   movedFor, noticeSentAt`, the two gaps, `violating`. `forPatient` is an OPTIONAL informational hop; no `Params`
   binds it.
4. **One marker, one op: `RecordAppointmentChangeNotice{appointmentKey, kind: cancelled|moved, changeRef}`**
   (Weaver-actor only; reads `[appointmentKey, appointmentKey.status, appointmentKey.schedule]`, optional
   `[appointmentKey.changeNotice]`). Liveness-guards the appointment; re-checks the change against the live
   aspect — `cancelled`: `status.value == 'cancelled' AND status.by == 'staff' AND status.at == changeRef`;
   `moved`: `schedule.movedAt == changeRef AND schedule.movedBy == 'staff'` and a non-terminal status — else
   `StaleChange` (a stale row is refused, not trusted). Writes `.changeNotice = {cancelledFor?, movedFor?,
   sentAt}` (class `appointmentChangeNotice`; the other kind's field carried; create when absent, bare update
   otherwise). Emits `clinic.appointmentChangeNoticeSent` and `external.notification` with `instanceKey =
   idempotencyKey = externalRef = <apptKey>:<kind>:<changeRef>`, `adapter: notification`, `replyOp:
   RecordAppointmentChangeNotification`, params `{appointmentKey, changeType, changeRef, startsAt, endsAt}`.
5. **One audit replyOp in `clinic-reminders`: `RecordAppointmentChangeNotification{externalRef, status,
   result?}`** — reads nothing, splits `externalRef` on `:` into key / kind / changeRef, bare-upserts
   `.changeNotification = {kind, changeRef, status, sentAt}` (class `appointmentChangeNotification`; latest outcome
   wins; audit only, gates nothing). The package already owns the reminder's replyOp; the only emitter is its own
   notice op, so the marker stays with its siblings.
6. **The target `appointmentChangeNotices`**: two gaps → `directOp RecordAppointmentChangeNotice`, `Params
   {appointmentKey: row.entityKey, kind: <literal>, changeRef: row.statusAt | row.movedAt}`, `Reads [row.entityKey,
   row.entityKey.status, row.entityKey.schedule]`, `OptionalReads [row.entityKey.changeNotice]`. No budget column —
   the default 3 is right for a notice, and a closed column retires the latch.
7. **The card says when, and that the patient was told.** The three appointment lenses project `statusAt`,
   `statusBy`, `changeNoticeSentAt`; `protectedAppointmentRow` carries them; `renderApptCard` adds one line:
   `checkedIn` → "⏱ Checked in 12 min ago" (a pure `statusClockLabel(row, nowMs)`, goja-pinned), a terminal value
   → "<STATUS_PAST> · <local date-time>"; and "📣 Patient told · <local time>" once `changeNoticeSentAt` is set.
   Both hats see it (facts about the visit, no PHI).

### Fire brief (build note, 2026-09-17)

1. **Scope** (board rows, verbatim): *`at = op.submittedAt` on every transition (wellness's `bookedAt` idiom),
   projected; "checked in 12 min ago" on the card.* + *a staff `SetAppointmentStatus(cancelled)` and a
   `RescheduleAppointment` more than 24 h out write `.status`/`.schedule` in silence … Wellness's mirror shipped
   `f139d128`.* Green bar: every `.status` write carries `at`/`by` and a same-value re-write carries them
   unchanged; a desk cancel and a desk move each produce exactly one `external.notification` per appointment per
   change, recorded on the appointment, a second move sends again, a patient's own cancel/move sends nothing; the
   desk card renders the check-in age and the told-at.
2. **Touch-list (verified live):** `packages/clinic-domain/`: `ddls.go` `:3584` + `:3606` (Create — `at`/`by`
   on `:3682`), `:3849–3910` (Reschedule — `sched` `:3874` gains `movedAt`/`movedBy`; the `:3908` re-stamp carries
   or stamps), `:4003–4104` (SetAppointmentStatus — self-confirm `:4033`, `status_data` `:4043`, the carry loop
   `:4102` gains `at`/`by`, and a same-value non-terminal re-set carries too), `:4171` (Correct), `:4275`
   (MarkPastDueNoShow, `by: sweep`); the `appointmentStatus` / `appointmentSchedule` aspectType DDL descriptions
   (`:42`, `:963`); `lenses.go` `:675`, `:1020`, `:1061` + each lens's `Columns` (`status_at`, `status_by`,
   `change_notice_sent_at`, Type text); `lens_cypher_test.go`, `protected_lens_test.go:57`,
   `status_clock_guard_test.go`, `integration_test.go`, `late_cancel_test.go` (fixture statuses gain nothing —
   assert the new fields where the op writes); `package.go:150` + `manifest.yaml:2` (0.39.0 → 0.40.0).
   `packages/clinic-reminders/`: new `changenotice.go` (op DDL + aspect DDL + script; mirror
   `wellness-reminders/changenotice.go` and `ddls.go:184–276`), `notifications.go` (+ replyOp DDLs, mirror
   `:50–86` with upsert semantics from `wellness-domain/notifications.go:203–212`), `lenses.go` (+ spec + lens
   entry beside `:50`), `targets.go` (+ target beside `:28`), `permissions.go` (+2 operator grants), `package.go:80`
   + `manifest.yaml:2` (0.12.2 → 0.13.0), `package_test.go:39/46/186` (DDL / OpMeta / permission counts), new
   `changenotice_cypher_test.go` + `changenotice_op_test.go` (harness: `setupRemEnv`, `crSubmitAs`, `crSubmitOpt`,
   `crReadDoc`, `crSeedStartedAppointment` in `integration_test.go:100–420`).
   `scripts/verify-package-clinic-reminders.go:109–111` (+4 `ddlCheck` rows) and `:199–208` (the permission census
   names `remOp` only — extend to the two new ops). `cmd/clinic-app/`: `appointments.go:142–160` (+3 fields),
   `:183` + `:287` SQL + both `Scan`s, `appointments_rls_test.go:123`, `provider_schedule_rls_test.go:61`,
   `staff_appointments_rls_test.go:56` (column lists); `web/app.js` `:5251–5300` (the card line beside the
   reminder line), `:5558` (`STATUS_PAST`); new `status_clock_ui_test.go` (goja, mirror `visit_record_ui_test.go`).
3. **Precedents:** the stamp → wellness `.status.bookedAt` (`time.rfc3339_utc(op.submittedAt)`) and the
   `SetAppointmentStatus` fee carry loop (`:4102`); the lens → `wellnessBookingChangeNoticesSpec`
   ([lenses.go:259](../../packages/wellness-reminders/lenses.go)) + `appointmentRemindersSpec`'s
   `nonTerminalAppointment` / `pastDueAppointments` conjuncts; the op → `wellness-reminders/changenotice.go:205–352`
   (guard, liveness, live re-check, marker carry, create-or-bare-update, event shape); the replyOp →
   `clinic-reminders/notifications.go:50–86` (shape) with the upsert of `wellness-domain/notifications.go`; the
   target → `wellness-reminders/targets.go:78–100`; the column path → `08888209` (`amended_at`, every site); the
   badge → the reminder-sent line (`app.js:5290`).
4. **Increments:** **(1) clinic-domain**: stamps on all five writers + `movedAt`/`movedBy`; DDL descriptions;
   the three lenses' columns; version. Tests: each writer's `at`/`by` (staff, patient, sweep), same-value re-set
   carries `at`/`by` (cancelled with note; checkedIn re-set), reschedule scheduled re-stamp carries, reset
   stamps; self reschedule → `movedBy: patient`; cypher pins for the three new columns on the three lenses;
   protected-lens column pins. Green: `go test ./packages/clinic-domain/ -count=1`, `go test ./internal/refractor/
   -run 'Corpus|Census' -count=1`. **(2) clinic-reminders**: lens, op, replyOp, target, grants, version. Cypher
   pins: staff-cancel untold → gap; told → closed; patient cancel → closed; legacy cancel (no `by`) → closed;
   cancel after the recorded end → closed; staff move untold → gap; told → closed; second move → reopens; patient
   move → closed; moved-then-cancelled → cancel gap only; ended → closed. Op pins: both kinds write the marker +
   emit the keyed event; `StaleChange` on a re-moved / re-set aspect; non-Weaver denied first; tombstoned
   appointment refused; the marker carries the other kind's field; the replyOp lands and overwrites. Green: `go
   test ./packages/clinic-reminders/ -count=1`, the refractor census. **(3) clinic-app**: three fields, SQL, tests'
   column lists, the card line, goja pin (`statusClockLabel` over checkedIn / terminal / none; the told line).
   Green: `go test ./cmd/clinic-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `for f in scripts/lint-*.go; do STRICT=1 go run "$f" || echo "FAIL $f"; done`, `gofmt -l scripts/`.
   **(4) live** (main checkout): `make refresh-clinic` (chains `provision-readpath`), `lattice lens lag` for
   `LensProjectionPaused`, `make verify-package-clinic-domain` + `make verify-package-clinic-reminders`, cycle
   `bin/clinic-app`; drive: desk checks a patient in → card age; desk cancels a scheduled visit → one notice + the
   outcome marker; desk moves a visit → one notice; read `/api/my-appointments`.
5. **Gotchas:** package edits ⇒ version bumps + `Version` mirrors (`lint-package-version`); a lens RETURN edit is
   a corpus edit (refractor census pins); `verify-package-clinic-reminders.go` `ddlCheck` rows for every new DDL
   and its permission census for every new op (the `command count=N want N-1` red); `lint-date-field-normalized`
   binds op-meta *payload* date fields — `at`/`movedAt` are computed, not payload; `lint-refusal-courtesy`: no
   client dispatches the new ops (Weaver / bridge only) — no pairs; `lint-conventions` binds the primordial-actor
   guard to any `Scope:"any"` op forwarding payload keys into an `external.*` event — the notice op carries it as
   its FIRST statement; the reply event class goes through the abstract-event gate — mirror
   `clinic.appointmentReminderNotificationRecorded`'s shape. **Dossier (`_packages.md`), applicable entries:**
   *the "leg" of a guard is every op that WRITES the guarded value* — the guarded values here are `.status.at/by`
   and `.schedule.movedAt/movedBy`: five status writers and one schedule writer, each decided (stamp / carry), and
   the third minter's rule binds — *a same-value re-set on a widened leg is the EMPTY batch; a widened leg lists
   every field the write stamps*; *a dispatch declaration must name what the runtime actually binds* — `Params`
   only on anchor-projected columns the gap's own conjunct requires non-null (`statusAt`, `movedAt`), and every
   `(a)`/`(d)` annotation in the notice op resolves to the playbook's `Reads`/`OptionalReads`; *a gap's budget and
   cadence are derived from its WHOLE loop* — pin each gap FALSE over the state every arm of every neighbour op
   leaves (a patient re-confirm, a note on a cancelled visit, a scheduled re-stamp, the sweep); *a refusal that
   reads a key the op never WRITES is advisory* — `StaleChange` reads `.status`/`.schedule` and writes
   `.changeNotice`: a cancel landing between hydration and commit yields one notice for the pre-race state and the
   level gap then corrects (accepted, as wellness recorded). **Dossier (`vertical-apps.md`):** *a new fact on a row
   is a census of every render gate* — `renderApptCard` is the one card for both hats; *two courtesy surfaces
   naming one instant render from one slice* — the told-at and the status-at use the card's existing
   `toLocaleString` idiom; the goja pin holds every non-terminal status. **Standing checklist:** #1 lifetime —
   `.status.at/by`: stamped at a value change, carried on a same-value write, dies with the aspect; `.changeNotice`:
   created at the first notice, carried per kind, never reset, dies with the appointment; `.changeNotification`:
   latest outcome, audit; #3 every conjunct and `StaleChange` revert-proven, the stamp asserted equal to the op's
   `submittedAt` at the producer; #5 one writer per marker (`.changeNotice` ← the notice op; `.changeNotification`
   ← the replyOp); #6 the wellness precedent's OCC line was wrong — the marker write is bare, mirror the shipped
   code, not the brief.
6. **Adjacent finds:** none beyond the sibling rows already on the board (displaced-by-time-off visit; hours in
   UTC, blocked; no-show count) — the next fire's units, not residuals.
7. **Non-goals:** a notice for a patient's own cancel or move; a notice for the sweep's no-show or a correction;
   deduping the move notice against the reminder that a move inside 24 h re-arms (two true facts, two messages —
   the wellness precedent accepted the same); a backfill of `at` on legacy statuses; the time-off displacement
   row; a real vendor adapter.
