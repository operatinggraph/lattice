# clinic-reminders — appointment reminders (the clinic vertical's first orchestration)

**Status:** ✅ Winston-ratified — build-ready. No frozen-contract touch, no Andrew gate.
**Owner:** Steward/Winston (autonomous). **Imp ★★★ · Size L.**

## What & why

The clinic vertical (`clinic-domain`) ships pure CRUD — patient / provider / appointment vertices +
projection lenses. It has **no orchestration**. This adds the first: an **appointment reminder** that
fires ~24h before an appointment via the `@at` temporal lane — the build-ready slice the backlog
flagged ("remind 24h before" is `@at` one-shot, NOT `@every`; the recurring-availability /
recurring-follow-up case that genuinely needs `@every` stays a separate, §10.4-amendment-gated item).

This is the forcing-function payoff: it pulls the platform's temporal-convergence machinery
(`freshUntil` column → Weaver `@at` schedule → `MarkExpired` re-touch → re-projection) into a second
vertical, proving it is domain-agnostic.

## The mechanism (verified against shipped code, not assumed)

The platform's `freshUntil` temporal lane is built to **re-open** a converged gap at a deadline
(lease-signing freshness). The reminder **inverts** it to **open** a gap at a deadline:

1. `CreateAppointment` (clinic-domain) precomputes `remindAt = startsAt − 24h` at write time via
   `time.rfc3339_add(startsAt, "-24h")` (a runtime builtin; `ParseDuration` accepts the negative
   duration) and stamps it on the `.schedule` aspect. The lens needs no cypher date arithmetic — only
   the proven RFC3339 lexical compare.
2. The `appointmentReminders` convergence lens (a weaver-target lens, `actorAggregate`, anchor
   `appointment`) projects per appointment:
   - `freshUntil = remindAt` while the reminder is pending — Weaver's temporal lane arms a
     per-entity `@at` at that instant (`internal/weaver/temporal.go` `scheduleFreshness`).
   - `missing_reminder = violating = (reminderSentAt = null) AND (remindAt <= $now) AND
     (startsAt > $now) AND (status <> 'cancelled')` — booleans (`boolColumn` requires bool;
     `$now` is the Refractor-supplied projection param).
3. At `remindAt` the `@at` fires → `handleFiredTimer` submits **`MarkExpired`** (orchestration-base),
   which writes the generic `freshnessExpiry` marker aspect **on the appointment**. That CDC write
   re-projects the appointment row with a fresh `$now` (the projection BFS over-reprojects, never
   under-reprojects) → `remindAt <= $now` is now true → `missing_reminder` flips **false→true**.
4. Weaver dispatches **`directOp(RecordAppointmentReminder)`** over the row (the `objectLiveness` /
   `TombstoneObject` GC pattern: `Params{appointmentKey: row.entityKey}`, `Reads:[row.entityKey]`).
5. `RecordAppointmentReminder` writes `.reminder = {sentAt}` (unconditioned upsert; idempotent in
   effect like `MarkExpired`'s marker) → re-projection → `reminderSentAt <> null` →
   `missing_reminder` false, `freshUntil` null (timer cleared). Converged.

Edge cases (all handled by the same predicate):
- Appointment booked **< 24h out** → `remindAt` already past → `missing_reminder` true at creation →
  reminds immediately (correct for last-minute bookings).
- **Cancelled** or **past** (`startsAt <= $now`) appointment → never violating, `freshUntil` null →
  no reminder, timer cleared.
- Post-convergence `@at` re-arm of a past `remindAt`: `freshUntil` projects null once `sentAt` is set,
  so no timer re-arms; and even before `sentAt` lands, a re-fired past deadline derives the **same**
  `(subject, fireAt)` requestId → the Contract #4 tracker collapses it (no storm). Same guard
  lease-signing relies on in CI.

## Decisions (Winston-ratified — all implementation-level)

- **`remindAt` lives on clinic-domain's `.schedule` aspect**, computed by `CreateAppointment`. The
  deadline is a *temporal fact* of the appointment (mirrors service-domain's `.outcome.validUntil`
  carrying the freshness deadline that lease-signing's convergence reads). The reminder *policy* (what
  to do at the deadline) lives in the separate `clinic-reminders` package. Keeps the domain↔orchestration
  split that location-domain→lease-signing already models.
- **Fixed 24h lead** (documented constant). A per-appointment configurable lead is a clean refinement
  (a future `remindLeadHours` param); not needed for v1.
- **`clinic-reminders` is a NEW sibling package** (depends `clinic-domain` + `orchestration-base`),
  not folded into clinic-domain — clinic-domain is deliberately orchestration-free (its package doc
  lists "a Weaver convergence lens" as out-of-scope). Mirrors location-domain (base) → lease-signing
  (convergence).
- **Action = `directOp`, not a Loom pattern.** A reminder is a single op; no multi-step externalTask
  flow, so no Loom pattern (unlike bgcheck/payment). `directOp` over the orphan-row is the
  `objectLiveness`→`TombstoneObject` precedent.
- **`RecordAppointmentReminder` stamps `.reminder.sentAt`; the actual notification channel
  (email/SMS) is the deferred bridge-adapter work.** The orchestration loop + the FE surfacing
  ("Reminder sent ✓") is the honest, demonstrable slice; real delivery is a real-adapter follow-on.
- **No `maxretries_reminder` cap needed.** The userTask-duplicate bug (board row) was a gap that
  stays open indefinitely (waiting for a human) being re-dispatched every 30m. This gap **closes
  automatically** the moment the op lands (≈1s), well inside the 30m reconciler mark-lease, so the
  sweep never re-fires it. The unconditioned-overwrite op is itself re-run-safe.

## Package shape (mirrors freshnessMarker/freshnessExpiry + objectLiveness)

`packages/clinic-reminders/`
- `ddls.go` — two DDLs:
  - `recordReminder` (vertexType, `PermittedCommands:[RecordAppointmentReminder]`) — owns the op
    script (writes `.reminder` on a live appointment; type-checks the key, liveness-guards the parent).
  - `appointmentReminder` (aspectType, `PermittedCommands:[RecordAppointmentReminder]`) — declares the
    non-sensitive `.reminder = {sentAt}` aspect; the step-6 write gate.
- `lenses.go` — `appointmentReminders` weaver-target lens (Bucket `weaver-targets`, full engine,
  `AnchorType:appointment`, `OutputKeyPattern:appointmentReminders.{actorSuffix}`, BodyColumns
  `violating, missing_reminder, entityKey, freshUntil, startsAt, patientKey, providerKey,
  reminderSentAt`).
- `targets.go` — `WeaverTargets` binding `missing_reminder → directOp(RecordAppointmentReminder)`.
- `permissions.go` — `RecordAppointmentReminder` grant to `operator` (scope any) — the Weaver service
  actor dispatches it, same idiom as TombstoneObject.
- `package.go` / `manifest.yaml` — `Depends:[clinic-domain, orchestration-base]`.

clinic-domain change: `CreateAppointment` adds `remindAt` to `.schedule`; the `appointmentSchedule`
aspect DDL self-description + the appointment DDL desc + package doc note it.

## Tests & gates

- `lens_cypher_test.go` — real full-engine assertions on the predicate: pending-future (not violating,
  `freshUntil`=remindAt), due (violating), sent (not violating, `freshUntil` null), cancelled, past.
- `integration_test.go` — Processor-driven: install clinic-domain + orchestration-base +
  clinic-reminders, `CreateAppointment`, `RecordAppointmentReminder`, assert `.reminder.sentAt` +
  lens projection.
- `package_test.go` — structural (DDLs present, `TestPackage_NoScans`, column↔playbook seam).
- `scripts/verify-package-clinic-reminders.go` + `make verify-package-clinic-reminders`; register in
  `cmd/lattice-pkg`; `install-clinic` gains orchestration-base + clinic-reminders (dependency order),
  so `up-clinic` drives reminders live.
- Gates: build / vet / golangci / STRICT P5+conventions lint / gofmt + the above. **3-layer
  adversarial review** (orchestration/capability plane).

## Review refinements (3-layer adversarial — applied)

The Blind Hunter / Edge Case Hunter / Acceptance Auditor pass landed Edge=SHIP, Acceptance=ACCEPT
(no frozen contract touched, spec-faithful), Blind=FIX-FIRST. Triage + fixes:

- **`startsAt`/`endsAt` normalized to canonical UTC** (Blind MAJOR / Edge NIT). `CreateAppointment`
  now stores `time.rfc3339_utc(startsAt)` (and endsAt), so the lens's lexical `startsAt > $now` /
  `remindAt <= $now` compares are sound for ANY caller offset / fractional form, not only `Z`-suffixed
  input. (`remindAt` was already canonical via `rfc3339_add`.)
- **`freshUntil` arms only while `remindAt > $now`** (Edge NIT). The CASE gained an
  `AND remindAt > $now` term, so the `@at` timer is a one-shot future wake-up: once the deadline has
  passed the open gap is owned by Weaver's gap-dispatch (violating) path and no timer re-arms. Exactly
  ONE `@at` fire per reminder; a `<24h` booking arms no timer (violating on the creation CDC).
- **Cancel-during-dispatch race — documented, not gated in the op** (Blind MAJOR; Edge + Acceptance did
  not flag). The directOp guards liveness (isDeleted) but NOT status, so an appointment cancelled in the
  narrow window between the gap opening and `RecordAppointmentReminder` committing still gets a
  `.reminder` marker. This is harmless while the marker is **inert** (no notification fires — delivery is
  deferred), and the authoritative "do not notify a cancelled/changed appointment" check belongs at the
  deferred **notification-delivery point** (which reads live state at send time anyway) — threading a
  status read into the marker-write op buys nothing until delivery exists. Noted in the op DDL
  self-description + the Phase-3 backlog (the notification-adapter item).
- **null-`.status` predicate term** (Blind/Edge MINOR) — bounded and left as-is: `CreateAppointment`
  commits root + `.schedule` + `.status` atomically, so a status-less appointment cannot arise from the
  normal path; guarding a structurally-impossible state would be noise.
- Idempotency test strengthened (the re-run's `OutcomeAccepted` is the create-only-vs-overwrite
  discriminator) + the column-seam test de-brittled.
