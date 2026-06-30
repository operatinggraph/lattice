# Clinic — recurring visit series (the `@every` clinic consumer) — design

**Status:** ✅ ratified (steward impl-ratified — no contract change, no fork; per CLAUDE.md rule #2 a
no-contract / no-fork design is the lead's to impl-ratify). Build-ready for a Vertical build fire.
**Owner:** Vertical Steward (pkg = clinic-reminders + clinic-domain; FE = Sally + FE Engineer).
**Backlog row:** verticals.md → "Recurring `@every` schedules — the clinic forcing function" (★★★ M).
**Grounds in:** Contract #10 §10.4 (FROZEN temporal lane), the `freshUntil` convention (§10 lines 169–171),
`packages/clinic-reminders/lenses.go` + `followups.go` (the established convergence pattern),
`packages/clinic-domain/package.go:58–67` (the deferral this closes).

---

## 1. The open question this design resolves

The backlog row was filed `🚧 needs-design`, NOT build-ready, with one explicit fork:

> a clinic consumer needs a design call (package `@at`-rolling-deadline pattern **vs** a new Weaver
> package-recurring lane = lattice), not a blind build.

This doc makes that call. **Decision: the recurring clinic need is built as a package-level
rolling-`@at` convergence series. NO new platform primitive. The `@every` substrate primitive
(`ScheduleEvery`, Fire 1) stays reserved for *singleton platform-wide* cadences.** Rationale below
(§3). This is pure vertical/package work within the frozen §10.4 lane — no lattice gap to file, no
contract change, no paper-over.

## 2. What "recurring" actually means for the clinic

The board names two candidate recurring needs. Ground both before designing:

- **Recurring *availability*** (a provider's weekly hours, e.g. "every Mon/Wed 09:00–17:00"). This is
  **already shipped and is NOT a timer** — `clinic-domain` stores `.hours = {windows: [{day, openSec,
  closeSec}]}` as a static weekly template (`ddls.go:138`), enforced at `CreateAppointment` /
  `RescheduleAppointment` time. It needs no `@every`. The `package.go:67` note ("the
  recurring-availability case still needs @every") is **stale** — weekly hours are a declarative
  template, not a scheduled fire. (This design corrects that comment.)

- **A recurring *visit series*** — the genuine, un-served recurring need. A patient on a standing
  cadence: chronic-care monthly check-ins, weekly physical-therapy, a quarterly review. The clinic
  wants the system to keep a **"next visit in the series is due"** worklist gap rolling forward on its
  own — when occurrence *N* is handled, occurrence *N+1* (one interval later) arms itself, until the
  series ends or is paused. **This is the consumer this design builds.**

A recurring visit series is structurally a **rolling generalization of the one-shot follow-up**
(`followups.go`): a follow-up fires once at a single `followUpDate`; a series re-arms its own next
deadline each time it converges. Same convergence machinery (aspect + op + `freshUntil`-armed `@at`
lens + `directOp` playbook), made to roll.

## 3. Why rolling-`@at`, not a per-series `@every`

`@every` (`substrate.ScheduleEvery`) publishes ONE durable, singleton schedule message that re-fires
into one fired-subject forever (§10.4 "Recurring schedules"). It is the right tool for Weaver's single
global sweep (`schedule.weaver.sweep`, the only recurring consumer today). It is the **wrong** tool for
a per-entity recurring series, for four reasons:

1. **Entity-scoped vs singleton.** A clinic runs thousands of independent series, each with its own
   cadence, start, end, and pause state. Arming a distinct `@every` NATS schedule per series is a
   per-entity proliferation of substrate schedules — the exact "state lives in timers" anti-pattern the
   lattice avoids. In the lattice idiom, **state lives in the read model; timers are *derived* from it.**

2. **Level-reconcile safety / self-healing.** The rolling-`@at` lens **derives** the next deadline from
   persisted state (`lastOccurrenceAt`, `intervalDays`, `activeUntil`), so a lost, delayed, or
   redelivered fire just re-projects the same gap — Weaver's sweep re-arms it. A raw per-series `@every`
   has no such derivation: if its schedule message is lost, that patient's series silently stops, with
   nothing to reconcile against. The convergence model is the whole point of building *on* Lattice.

3. **Lifecycle is a state edit, not a schedule dance.** Pause / resume / end of a series is an aspect
   edit the lens already reflects (deadline projects `null` while paused / past `activeUntil` → no fire;
   resumes when the aspect flips back). A raw `@every` requires explicit `CancelSchedule` + re-arm per
   series, off the convergence path, with its own crash-safety burden.

4. **Zero new platform seam.** Rolling-`@at` is fully expressible with the *shipped, contract-blessed*
   `freshUntil` → `@at` seam (§10 lines 169–171) + a convergence lens + an "advance" op. There is no
   missing primitive → no `lattice.md` gap, no paper-over. (Contrast: a *blind* per-series `@every`
   would still need a brand-new package-facing `@every` seam — a lattice add — to even reach the
   substrate. Rolling-`@at` is both more correct *and* cheaper.)

### 3.1 The boundary — when a package `@every` seam WOULD be justified (and why we don't file it now)

A package-facing `@every` seam (a real lattice add) is justified **only** for a *singleton,
clinic-wide, stateless* cadence — same shape as the Weaver sweep. Example: "every morning project a
clinic-wide day-ahead digest." That is **not** a recurring *series* (no per-entity state, one fire for
the whole clinic). No such consumer exists today, and the No-Paper-Over rule cuts both ways: we do
**not** file a phantom `lattice.md` gap for a primitive with no consumer. If/when a singleton package
cadence is genuinely demanded, *that* fire files the seam (a thin `Lens → contextHint`-style "arm an
`@every` for this target" declaration, mirroring how `freshUntil` arms `@at`). Recorded here so the
boundary is explicit, not rediscovered.

## 4. Build plan (increments — for the build fire)

All in **clinic-reminders** (the convergence-owning package; `clinic-domain` stays projection-only per
`package.go:68–70`) except the series-state aspects, which clinic-domain mints on the patient. Mirror
`followups.go` exactly — it is the closest precedent.

### Inc 1 — series state + the convergence lens + the advance op (package, no FE)

- **Aspect (clinic-domain, on the patient — or a new lightweight `vtx.visitseries.<id>`; pick patient
  to avoid a new vertex type unless a patient can hold >1 concurrent series → then a series vertex).**
  `recommend: a dedicated vtx.visitseries.<NanoID>` keyed to a patient+provider, so multiple
  concurrent cadences per patient are first-class. Aspects:
  - `.series = {patientKey, providerKey, intervalDays, startAt, activeUntil?, reason?}` (the cadence
    definition; `intervalDays` an int, dates RFC3339 UTC normalized to 09:00:00Z like `followUpDate`).
  - `.progress = {lastOccurrenceAt, occurrenceCount}` (advanced by the op; absent until first fire).
  - `.paused = {value: bool}` (optional lifecycle toggle).
- **Ops (clinic-domain or clinic-reminders — keep the *write* of `.series`/`.paused` in clinic-domain
  as domain state; keep the *advance* in clinic-reminders as convergence):**
  - `StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}` → mints the series
    vertex + `.series`. (clinic-domain.)
  - `PauseVisitSeries` / `ResumeVisitSeries` → toggle `.paused`. (clinic-domain.)
  - `AdvanceVisitSeries{seriesKey, dueFor}` → the **directOp the playbook dispatches**: stamps
    `.progress = {lastOccurrenceAt: dueFor, occurrenceCount+1}`, read-guarded on `[seriesKey]`,
    UNCONDITIONED update (idempotent under at-least-once — the `RecordFollowUpReminder` idiom).
    (clinic-reminders.)
- **Lens `visitSeriesDue` (weaver-target, full engine).** One row per active series. The rolling
  deadline:
  - `nextDueAt = (.progress.lastOccurrenceAt ?? .series.startAt) + intervalDays·days` (cypher-computed;
    null-safe — first occurrence anchors on `startAt`).
  - `freshUntil = CASE WHEN active AND nextDueAt > $now THEN nextDueAt ELSE null END` — arms an `@at`
    at `nextDueAt` while it is future; goes null once the deadline passes (the OPEN-the-gap polarity,
    exactly like `appointmentRemindersSpec`, `lenses.go:39`).
  - `series_due = active AND nextDueAt <= $now` — the violating row the playbook converges.
  - `active = NOT paused AND (activeUntil IS null OR nextDueAt <= activeUntil)` — a series past
    `activeUntil` projects no deadline and no gap (clean termination, no cancel needed).
- **Playbook (targets.go):** `series_due → directOp(AdvanceVisitSeries, dueFor: row.nextDueAt)`.
  Exactly the `missing_followup_reminder → directOp(RecordFollowUpReminder)` shape.
- **Convergence semantics:** advance stamps `lastOccurrenceAt = nextDueAt` (NOT `$now` — keeps the
  cadence on a fixed grid, immune to fire latency drift), `nextDueAt` rolls forward one interval,
  `freshUntil` re-arms the next `@at`. Exactly-one fire per occurrence; a catch-up after downtime fires
  each missed occurrence in turn (level-reconcile), which is the desired "don't silently skip a
  patient's check-in" behavior. (If skip-to-latest is ever wanted, the advance op can fast-forward
  `lastOccurrenceAt` to the most recent past grid point — note, don't build, until a PO asks.)
- **Tests:** mirror `followups_cypher_test.go` + `integration_test.go` — first-occurrence-from-startAt,
  roll-forward, pause/resume (deadline drops + re-arms), `activeUntil` termination, catch-up after a
  gap. `make verify-package-clinic-reminders` (DDL/keys touched).

### Inc 2 — FE (Sally → FE Engineer): the series worklist + start/pause

- A clinic-wide **"Visit series due"** worklist (reads the `visitSeriesDue` lens read-model — P5, never
  Core KV), grouped by urgency like the follow-ups worklist; one-click **Book** the due visit (reuses
  the existing booking flow, pre-filling patient+provider) which is what *handles* the occurrence.
- On the patient view: **Start a recurring series** (interval + start + optional end) and
  **Pause/Resume**. In-browser verified per the FE Engineer playbook (cycle `bin/clinic-app`).

## 5. Contract / lattice impact

**None.** Stays entirely within frozen §10.4 + the `freshUntil` seam. No contract edit, no `lattice.md`
gap (§3.1). Self-ratified. The only cross-file touch outside the package is correcting the stale
`clinic-domain/package.go:64–67` deferral comment when Inc 1 lands.

## 6. Why this is the right altitude

This closes a ★★★ deferral with the *simplest extension of existing state* (the Designer-blind-spot
rule): no new mechanism, just the follow-up convergence made to roll. It also demonstrates the
lattice's central claim — that "recurring business process" is a derived read-model projection, not an
external scheduler — on a real vertical need, which is precisely the kind of forcing function the
clinic vertical exists to provide.
